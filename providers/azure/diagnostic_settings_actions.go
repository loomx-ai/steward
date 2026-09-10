package azure

import (
	"context"
	"maps"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/catalog"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

type diagnosticAction struct {
	client                                                    *client
	planned                                                   asset.Asset
	id, wire, scope, configuration, sourceContext, references string
	deletion                                                  catalog.RESTRequest
}

func newDiagnosticAction(c *client, connection asset.ConnectionID, value asset.Asset) (*diagnosticAction, error) {
	id, _, kind, err := diagnosticResourceID(value.Identity.NativeID)
	if err != nil || id != value.Identity.NativeID || kind != diagnosticSettingsType || value.Identity.NativeType != kind || value.ID == "" || value.Identity.Provider != asset.ProviderAzure || value.Identity.ConnectionID != connection || connection == "" || value.Identity.Partition == "" || value.Location != "global" {
		return nil, serviceDenied("invalid_diagnostic_action_identity")
	}
	if _, err := c.diagnosticRecordedReferences(value); err != nil {
		return nil, err
	}
	wire, err := c.diagnosticPlannedWire(id, value.Normalized)
	if err != nil {
		return nil, err
	}
	scope := diagnosticWireScope(wire)
	deletion, err := c.diagnosticRequest(scope, last(id), kind, "DELETE")
	if err != nil {
		return nil, err
	}
	return &diagnosticAction{client: c, planned: value, id: id, wire: wire, scope: scope, configuration: text(value.Normalized[diagnosticConfigurationProof]), sourceContext: text(value.Normalized[diagnosticContextProof]), references: text(value.Normalized[diagnosticReferencesProof]), deletion: deletion}, nil
}

func (*diagnosticAction) DeletionCheckTimeout() time.Duration { return time.Hour }

func (a *diagnosticAction) identity(request contracts.ActionRequest) error {
	value := request.Asset
	if request.Action != "delete" || len(request.Parameters)+len(request.LifecycleImpacts)+len(request.PrerequisiteDeletions) != 0 || value.ID != a.planned.ID || value.Identity != a.planned.Identity || value.Location != "global" || text(value.Normalized[diagnosticConfigurationProof]) != a.configuration || text(value.Normalized[diagnosticContextProof]) != a.sourceContext || text(value.Normalized[diagnosticReferencesProof]) != a.references {
		return serviceDenied("diagnostic_action_identity_changed")
	}
	_, err := a.client.diagnosticRecordedReferences(value)
	if err != nil {
		return err
	}
	if text(value.Normalized[diagnosticWireSelector]) != a.wire {
		return serviceDenied("diagnostic_action_native_selector_changed")
	}
	return nil
}

func (a *diagnosticAction) current(ctx context.Context) (response, diagnosticContextState, error) {
	current, err := a.client.diagnosticRead(ctx, a.wire, diagnosticSettingsType)
	if err != nil {
		return current, diagnosticContextState{}, err
	}
	if a.client.privateConfiguration(diagnosticSnapshot(current.data)) != a.configuration {
		return current, diagnosticContextState{}, serviceDenied("diagnostic_configuration_changed")
	}
	state, err := a.client.diagnosticContext(ctx, a.scope)
	if err != nil {
		return current, state, contracts.DependencyReadError(err)
	}
	if a.client.privateConfiguration(state.state) != a.sourceContext {
		return current, state, serviceDenied("diagnostic_source_context_changed")
	}
	refs, err := diagnosticReferences(a.id, current.data)
	if err != nil {
		return current, state, err
	}
	if a.client.diagnosticReferenceBinding(a.id, a.configuration, a.sourceContext, monitorReferenceProjection(refs)) != a.references {
		return current, state, serviceDenied("diagnostic_references_changed")
	}
	return current, state, nil
}

func (a *diagnosticAction) Preflight(ctx context.Context, request contracts.ActionRequest) (check contracts.PreflightResult, err error) {
	defer func() { err = contracts.DependencyReadError(err) }()
	if err := a.identity(request); err != nil {
		return check, err
	}
	for range 2 {
		current, state, err := a.current(ctx)
		if isNotFound(err) {
			return contracts.PreflightResult{Allowed: true, Absent: true}, nil
		}
		if err != nil {
			return check, err
		}
		if !state.verified {
			return contracts.PreflightResult{Reason: "diagnostic_source_identity_unverified"}, nil
		}
		if protectedAzureTags(object(current.data["tags"])) || protectedAzureTags(object(state.source["tags"])) || protectedAzureTags(object(state.group["tags"])) {
			return contracts.PreflightResult{Reason: "azure_protected_tag"}, nil
		}
		locks, err := a.client.managementLocks(ctx)
		if err != nil {
			return check, err
		}
		if locked(a.id, locks) {
			return contracts.PreflightResult{Reason: "azure_management_lock"}, nil
		}
	}
	if _, _, err := a.current(ctx); err != nil {
		return check, contracts.DependencyReadError(err)
	}
	return contracts.PreflightResult{Allowed: true}, nil
}

func (a *diagnosticAction) receipt() string {
	return a.client.privateConfiguration(map[string]any{"asset": a.planned.ID, "identity": a.planned.Identity, "wire_id": a.wire, "location": "global", "configuration": a.configuration, "context": a.sourceContext, "references": a.references, "version": diagnosticSettingsVersion, "method": "DELETE", "protocol": "synchronous-diagnostic-setting-absence"})
}

func (a *diagnosticAction) result(response response) contracts.ActionResult {
	return contracts.ActionResult{ProviderRequestID: response.requestID, RetryAfter: retryAfter(response.header), Data: map[string]any{"_diagnostic_delete_receipt": a.receipt()}}
}

func (a *diagnosticAction) Execute(ctx context.Context, request contracts.ActionRequest) (contracts.ActionResult, error) {
	check, err := a.Preflight(ctx, request)
	if err != nil {
		return contracts.ActionResult{}, err
	}
	if !check.Allowed {
		return contracts.ActionResult{}, serviceDenied(check.Reason)
	}
	if check.Absent {
		return a.result(response{}), nil
	}
	deletion := a.deletion
	deletion.Headers = maps.Clone(deletion.Headers)
	if deletion.Headers == nil {
		deletion.Headers = map[string]string{}
	}
	if request.IdempotencyKey != "" {
		deletion.Headers["x-ms-client-request-id"] = azureRequestID(request.IdempotencyKey)
	}
	result, err := a.client.requestAt(ctx, deletion.Method, deletion.URL, deletion.Body, deletion.Headers, func(endpoint string) error {
		if endpoint != a.deletion.URL {
			return serviceDenied("diagnostic_delete_endpoint_changed")
		}
		return a.client.validateURL(endpoint)
	})
	if isNotFound(err) {
		return a.result(result), nil
	}
	if err != nil {
		return contracts.ActionResult{}, err
	}
	if result.status != 200 && result.status != 204 || len(result.data) != 0 || operationLocation(result.header) != "" {
		return contracts.ActionResult{}, serviceDenied("invalid_diagnostic_delete_response")
	}
	return a.result(result), nil
}

func (a *diagnosticAction) verifyReceipt(result contracts.ActionResult) error {
	if result.ProviderOperationID != "" || text(result.Data["_diagnostic_delete_receipt"]) != a.receipt() {
		return serviceDenied("diagnostic_delete_receipt_changed")
	}
	return nil
}

func (a *diagnosticAction) Readback(ctx context.Context, request contracts.ActionRequest) (result contracts.ReadbackResult, err error) {
	defer func() { err = contracts.DependencyReadError(err) }()
	if err := a.identity(request); err != nil {
		return result, err
	}
	if request.ExecutionResult != nil {
		if err := a.verifyReceipt(*request.ExecutionResult); err != nil {
			return result, err
		}
	}
	_, _, err = a.current(ctx)
	if isNotFound(err) {
		return contracts.ReadbackResult{Exists: false}, nil
	}
	if err != nil {
		return result, err
	}
	return contracts.ReadbackResult{Exists: true, State: "deleting"}, nil
}

func (a *diagnosticAction) Wait(ctx context.Context, request contracts.ActionRequest, result contracts.ActionResult) (contracts.WaitResult, error) {
	if err := a.verifyReceipt(result); err != nil {
		return contracts.WaitResult{}, err
	}
	request.ExecutionResult = &result
	read, err := a.Readback(ctx, request)
	if err != nil {
		return contracts.WaitResult{}, err
	}
	return contracts.WaitResult{Done: !read.Exists, State: read.State, RetryAfter: max(result.RetryAfter, 2*time.Second)}, nil
}

var _ contracts.ActionDriver = (*diagnosticAction)(nil)
