package azure

import (
	"context"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/catalog"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

type monitorAction struct {
	client                                                                 *client
	assetID                                                                asset.AssetID
	connection                                                             asset.ConnectionID
	partition, id, kind, scope, location, configuration, group, references string
	deletion                                                               catalog.RESTRequest
}

func monitorResourceVersion(kind string) string {
	if row := monitorRuleKind(kind); row.kind != "" {
		return row.version
	}
	_, version := monitorBudgetKind(kind)
	return version
}

func newMonitorAction(c *client, connection asset.ConnectionID, value asset.Asset) (*monitorAction, error) {
	id, scope, kind, err := monitorResourceID(value.Identity.NativeID)
	if err != nil || id != value.Identity.NativeID || kind != value.Identity.NativeType || value.ID == "" || value.Identity.Provider != asset.ProviderAzure || value.Identity.ConnectionID != connection || connection == "" || value.Identity.Partition == "" || value.Location == "" || value.Location != strings.ToLower(value.Location) {
		return nil, serviceDenied("invalid_monitor_action_identity")
	}
	if budget, _ := monitorBudgetKind(kind); budget != "" && value.Location != "global" {
		return nil, serviceDenied("invalid_monitor_budget_action_scope")
	}
	if _, err := c.monitorRecordedReferences(value); err != nil {
		return nil, err
	}
	var deletion catalog.RESTRequest
	if budget, _ := monitorBudgetKind(kind); budget != "" {
		deletion, err = c.monitorBudgetRequest(kind, scope, last(id), "DELETE")
	} else {
		deletion, err = c.monitorRuleRequest(kind, id, "DELETE")
	}
	if err != nil {
		return nil, err
	}
	return &monitorAction{client: c, assetID: value.ID, connection: connection, partition: value.Identity.Partition, id: id, kind: kind, scope: scope, location: value.Location, configuration: text(value.Normalized[monitorConfigurationProof]), group: text(value.Normalized[monitorGroupProof]), references: text(value.Normalized[monitorReferencesProof]), deletion: deletion}, nil
}

func (*monitorAction) DeletionCheckTimeout() time.Duration { return time.Hour }

func (a *monitorAction) identity(request contracts.ActionRequest) error {
	value := request.Asset
	if request.Action != "delete" || len(request.Parameters)+len(request.LifecycleImpacts) != 0 || value.ID != a.assetID || value.Identity.Provider != asset.ProviderAzure || value.Identity.ConnectionID != a.connection || value.Identity.Partition != a.partition || value.Identity.NativeType != a.kind || value.Identity.NativeID != a.id || value.Location != a.location || text(value.Normalized[monitorConfigurationProof]) != a.configuration || text(value.Normalized[monitorGroupProof]) != a.group || text(value.Normalized[monitorReferencesProof]) != a.references {
		return serviceDenied("monitor_action_identity_changed")
	}
	_, err := a.client.monitorRecordedReferences(value)
	return err
}

func (a *monitorAction) prerequisitesAbsent(ctx context.Context, request contracts.ActionRequest) error {
	seenIDs, seenAssets := map[string]bool{a.id: true}, map[asset.AssetID]bool{a.assetID: true}
	for _, prerequisite := range request.PrerequisiteDeletions {
		value := prerequisite.Asset
		id, _, kind, err := monitorResourceID(value.Identity.NativeID)
		if value.Identity.NativeType == diagnosticSettingsType {
			id, _, kind, err = diagnosticResourceID(value.Identity.NativeID)
		}
		if rbacResourceKind(value.Identity.NativeType) != "" {
			id, _, kind, err = rbacResourceID(value.Identity.NativeID)
		}
		if err != nil || id != value.Identity.NativeID || kind != value.Identity.NativeType || !strings.HasPrefix(id, a.client.root()+"/") || value.ID == "" || seenIDs[id] || seenAssets[value.ID] || !prerequisite.Delete || prerequisite.ControllerID != a.assetID || value.Identity.Provider != asset.ProviderAzure || value.Identity.ConnectionID != a.connection || value.Identity.Partition != a.partition {
			return serviceDenied("invalid_monitor_prerequisite_identity")
		}
		seenIDs[id], seenAssets[value.ID] = true, true
		var refs map[string]any
		if rbacResourceKind(kind) != "" {
			refs, err = a.client.rbacRecordedReferences(value)
		} else if kind == diagnosticSettingsType {
			refs, err = a.client.diagnosticRecordedReferences(value)
		} else {
			refs, err = a.client.monitorRecordedReferences(value)
		}
		if err != nil {
			return err
		}
		linked := false
		for typ, ids := range refs {
			linked = linked || strings.EqualFold(typ, a.kind) && slices.Contains(stringValues(ids), a.id)
		}
		if !linked {
			return serviceDenied("monitor_prerequisite_reference_changed")
		}
		if rbacResourceKind(kind) != "" {
			_, err = a.client.rbacRead(ctx, kind, text(value.Normalized[rbacWireSelector]))
		} else if kind == diagnosticSettingsType {
			_, err = a.client.diagnosticRead(ctx, text(value.Normalized[diagnosticWireSelector]), kind)
		} else {
			_, err = a.client.monitorResourceRead(ctx, kind, id)
		}
		if !isNotFound(err) {
			if err != nil {
				return err
			}
			return serviceDenied("monitor_prerequisite_requires_prior_deletion")
		}
	}
	return nil
}

func (a *monitorAction) dependenciesAbsent(ctx context.Context, request contracts.ActionRequest) error {
	if err := a.prerequisitesAbsent(ctx, request); err != nil {
		return err
	}
	known := []asset.Asset{}
	for _, prerequisite := range request.PrerequisiteDeletions {
		known = append(known, prerequisite.Asset)
	}
	incoming, err := a.client.monitorIncoming(ctx, request.Asset, known...)
	if err != nil {
		return err
	}
	if len(incoming) != 0 {
		return serviceDenied("monitor_resource_has_incoming_references")
	}
	return nil
}

func (a *monitorAction) current(ctx context.Context) (response, error) {
	current, err := a.client.monitorResourceRead(ctx, a.kind, a.id)
	if err != nil {
		return current, err
	}
	if monitorResourceRegion(a.kind, current.data) != a.location || a.client.privateConfiguration(monitorResourceSnapshot(a.kind, current.data)) != a.configuration {
		return current, serviceDenied("monitor_resource_configuration_changed")
	}
	refs, err := a.client.monitorReferences(ctx, a.kind, a.id, current.data)
	if err != nil {
		return current, err
	}
	if a.client.monitorReferencesBinding(a.id, a.kind, a.configuration, a.group, monitorReferenceProjection(refs)) != a.references {
		return current, serviceDenied("monitor_receiver_resolution_changed")
	}
	return current, nil
}

func (a *monitorAction) Preflight(ctx context.Context, request contracts.ActionRequest) (check contracts.PreflightResult, err error) {
	defer func() { err = contracts.DependencyReadError(err) }()
	if err := a.identity(request); err != nil {
		return check, err
	}
	for range 2 {
		if err := a.dependenciesAbsent(ctx, request); err != nil {
			return check, err
		}
		current, err := a.current(ctx)
		if isNotFound(err) {
			return contracts.PreflightResult{Allowed: true, Absent: true}, nil
		}
		if err != nil {
			return check, err
		}
		mapping, _ := findType(a.kind)
		if reason := protectionReason(mapping, current.data); reason != "" {
			return contracts.PreflightResult{Reason: reason}, nil
		}
		group := map[string]any{}
		if a.scope != a.client.root() {
			group, err = a.client.workbookGroup(ctx, a.id)
			if err != nil {
				return check, err
			}
			if text(group["managedBy"]) != "" {
				return contracts.PreflightResult{Reason: "azure_managed_resource_group"}, nil
			}
			if protectedAzureTags(object(group["tags"])) {
				return contracts.PreflightResult{Reason: "azure_protected_tag"}, nil
			}
		}
		if a.client.privateConfiguration(insightsWorkspaceResourceSnapshot(group)) != a.group {
			return check, serviceDenied("monitor_action_resource_group_changed")
		}
		locks, err := a.client.managementLocks(ctx)
		if err != nil {
			return check, err
		}
		if locked(a.id, locks) {
			return contracts.PreflightResult{Reason: "azure_management_lock"}, nil
		}
	}
	if _, err := a.current(ctx); err != nil {
		return check, contracts.DependencyReadError(err)
	}
	return contracts.PreflightResult{Allowed: true}, nil
}

func (a *monitorAction) receipt() string {
	return a.client.privateConfiguration(map[string]any{"asset": a.assetID, "connection": a.connection, "partition": a.partition, "id": a.id, "kind": a.kind, "scope": a.scope, "location": a.location, "configuration": a.configuration, "group": a.group, "references": a.references, "method": "DELETE", "version": monitorResourceVersion(a.kind), "protocol": "synchronous-active-resource-absence"})
}

func (a *monitorAction) result(response response) contracts.ActionResult {
	return contracts.ActionResult{ProviderRequestID: response.requestID, RetryAfter: retryAfter(response.header), Data: map[string]any{"_monitor_delete_receipt": a.receipt()}}
}

func (a *monitorAction) Execute(ctx context.Context, request contracts.ActionRequest) (contracts.ActionResult, error) {
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
			return serviceDenied("monitor_delete_endpoint_changed")
		}
		return a.client.validateURL(endpoint)
	})
	if isNotFound(err) {
		return a.result(result), nil
	}
	if err != nil {
		return contracts.ActionResult{}, err
	}
	budget, _ := monitorBudgetKind(a.kind)
	if result.status != 200 && (budget != "" || result.status != 204) || len(result.data) != 0 || operationLocation(result.header) != "" {
		return contracts.ActionResult{}, serviceDenied("invalid_monitor_delete_response")
	}
	return a.result(result), nil
}

func (a *monitorAction) verifyReceipt(result contracts.ActionResult) error {
	if result.ProviderOperationID != "" || text(result.Data["_monitor_delete_receipt"]) != a.receipt() {
		return serviceDenied("monitor_delete_receipt_changed")
	}
	return nil
}

func (a *monitorAction) Readback(ctx context.Context, request contracts.ActionRequest) (result contracts.ReadbackResult, err error) {
	defer func() { err = contracts.DependencyReadError(err) }()
	if err := a.identity(request); err != nil {
		return result, err
	}
	if request.ExecutionResult != nil {
		if err := a.verifyReceipt(*request.ExecutionResult); err != nil {
			return result, err
		}
	}
	if err := a.dependenciesAbsent(ctx, request); err != nil {
		return result, err
	}
	_, err = a.current(ctx)
	if isNotFound(err) {
		return result, nil
	}
	if err != nil {
		return result, err
	}
	return contracts.ReadbackResult{Exists: true, State: "deleting"}, nil
}

func (a *monitorAction) Wait(ctx context.Context, request contracts.ActionRequest, result contracts.ActionResult) (contracts.WaitResult, error) {
	if err := a.verifyReceipt(result); err != nil {
		return contracts.WaitResult{}, err
	}
	request.ExecutionResult = &result
	read, err := a.Readback(ctx, request)
	if err != nil {
		return contracts.WaitResult{}, err
	}
	return contracts.WaitResult{Done: !read.Exists, State: read.State, RetryAfter: 2 * time.Second}, nil
}

var _ contracts.ActionDriver = (*monitorAction)(nil)
