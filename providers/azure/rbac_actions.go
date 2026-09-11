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

type rbacAction struct {
	client                                             *client
	planned                                            asset.Asset
	id, kind, wire, configuration, context, references string
	deletion                                           catalog.RESTRequest
}

func newRBACAction(c *client, connection asset.ConnectionID, value asset.Asset) (*rbacAction, error) {
	if value.ID == "" || value.Identity.Provider != asset.ProviderAzure || value.Identity.ConnectionID != connection || connection == "" || value.Identity.Partition == "" || value.Location != "global" || rbacResourceKind(value.Identity.NativeType) == "" {
		return nil, serviceDenied("invalid_rbac_action_identity")
	}
	if _, err := c.rbacRecordedReferences(value); err != nil {
		return nil, err
	}
	wire := text(value.Normalized[rbacWireSelector])
	scope := wire[:strings.LastIndex(wire, "/providers/")]
	deletion, err := c.rbacRequest(value.Identity.NativeType, scope, last(wire), "DELETE")
	if err != nil {
		return nil, err
	}
	return &rbacAction{client: c, planned: value, id: value.Identity.NativeID, kind: value.Identity.NativeType, wire: wire, configuration: text(value.Normalized[rbacConfigurationProof]), context: text(value.Normalized[rbacContextProof]), references: text(value.Normalized[rbacReferencesProof]), deletion: deletion}, nil
}

func (*rbacAction) DeletionCheckTimeout() time.Duration { return time.Hour }
func (a *rbacAction) identity(request contracts.ActionRequest) error {
	value := request.Asset
	if request.Action != "delete" || len(request.Parameters)+len(request.LifecycleImpacts) != 0 || value.ID != a.planned.ID || value.Identity != a.planned.Identity || value.Location != "global" || text(value.Normalized[rbacWireSelector]) != a.wire || text(value.Normalized[rbacConfigurationProof]) != a.configuration || text(value.Normalized[rbacContextProof]) != a.context || text(value.Normalized[rbacReferencesProof]) != a.references {
		return serviceDenied("rbac_action_identity_changed")
	}
	_, err := a.client.rbacRecordedReferences(value)
	return err
}

func (a *rbacAction) prerequisitesAbsent(ctx context.Context, request contracts.ActionRequest) error {
	seen, assets := map[string]bool{a.id: true}, map[asset.AssetID]bool{a.planned.ID: true}
	for _, prerequisite := range request.PrerequisiteDeletions {
		value := prerequisite.Asset
		if a.kind != rbacRoleType || !prerequisite.Delete || prerequisite.ControllerID != a.planned.ID || value.ID == "" || value.Identity.Provider != asset.ProviderAzure || value.Identity.ConnectionID != a.planned.Identity.ConnectionID || value.Identity.Partition != a.planned.Identity.Partition || value.Identity.NativeType != rbacAssignmentType || seen[value.Identity.NativeID] || assets[value.ID] {
			return serviceDenied("invalid_rbac_prerequisite")
		}
		seen[value.Identity.NativeID], assets[value.ID] = true, true
		refs, err := a.client.rbacRecordedReferences(value)
		if err != nil || !slices.Contains(stringValues(refs[rbacRoleType]), a.id) {
			return serviceDenied("rbac_prerequisite_reference_changed")
		}
		_, err = a.client.rbacRead(ctx, rbacAssignmentType, text(value.Normalized[rbacWireSelector]))
		if !isNotFound(err) {
			if err != nil {
				return err
			}
			return serviceDenied("rbac_assignment_requires_prior_deletion")
		}
	}
	if a.kind == rbacRoleType {
		assignments, _, err := a.client.rbacIndex(ctx, rbacAssignmentType, a.client.root())
		if err != nil {
			return err
		}
		for _, raw := range assignments {
			role, err := a.client.rbacRoleID(text(object(raw["properties"])["roleDefinitionId"]))
			if err != nil {
				return err
			}
			if role == a.id {
				return serviceDenied("rbac_role_has_assignments")
			}
		}
	}
	return nil
}

func (a *rbacAction) current(ctx context.Context) (response, string, error) {
	current, err := a.client.rbacRead(ctx, a.kind, a.wire)
	if err != nil {
		return current, "", err
	}
	if a.client.privateConfiguration(a.client.rbacSnapshot(a.kind, current.data)) != a.configuration {
		return current, "", serviceDenied("rbac_configuration_changed")
	}
	locks, err := a.client.managementLocks(ctx)
	if err != nil {
		return current, "", contracts.DependencyReadError(err)
	}
	pim, err := a.client.rbacPIM(ctx)
	if err != nil {
		return current, "", err
	}
	state, reason, err := a.client.rbacContext(ctx, a.kind, current.data, locks, pim, nil)
	if err != nil {
		return current, "", err
	}
	if a.client.privateConfiguration(state) != a.context {
		return current, "", serviceDenied("rbac_scope_or_pim_changed")
	}
	refs, err := a.client.rbacReferences(a.kind, a.id, current.data)
	if err != nil {
		return current, "", err
	}
	if a.client.rbacReferenceBinding(a.id, a.kind, a.wire, a.configuration, a.context, monitorReferenceProjection(refs)) != a.references {
		return current, "", serviceDenied("rbac_native_reference_changed")
	}
	return current, reason, nil
}

func (a *rbacAction) Preflight(ctx context.Context, request contracts.ActionRequest) (result contracts.PreflightResult, err error) {
	defer func() { err = contracts.DependencyReadError(err) }()
	if err := a.identity(request); err != nil {
		return result, err
	}
	for range 2 {
		if err := a.prerequisitesAbsent(ctx, request); err != nil {
			return result, err
		}
		_, reason, err := a.current(ctx)
		if isNotFound(err) {
			return contracts.PreflightResult{Allowed: true, Absent: true}, nil
		}
		if err != nil {
			return result, err
		}
		if reason != "" {
			return contracts.PreflightResult{Reason: reason}, nil
		}
	}
	return contracts.PreflightResult{Allowed: true}, nil
}

func (a *rbacAction) receipt() string {
	return a.client.privateConfiguration(map[string]any{"asset": a.planned.ID, "identity": a.planned.Identity, "wire": a.wire, "configuration": a.configuration, "context": a.context, "references": a.references, "version": rbacVersion(a.kind), "protocol": "rbac-independent-delete-1"})
}
func (a *rbacAction) result(response response) contracts.ActionResult {
	return contracts.ActionResult{ProviderRequestID: response.requestID, RetryAfter: retryAfter(response.header), Data: map[string]any{"_rbac_delete_receipt": a.receipt()}}
}
func (a *rbacAction) Execute(ctx context.Context, request contracts.ActionRequest) (contracts.ActionResult, error) {
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
			return serviceDenied("rbac_delete_endpoint_changed")
		}
		return a.client.validateURL(endpoint)
	})
	if isNotFound(err) {
		return a.result(result), nil
	}
	if err != nil {
		return contracts.ActionResult{}, err
	}
	if operationLocation(result.header) != "" || result.status != 200 && result.status != 204 {
		return contracts.ActionResult{}, serviceDenied("invalid_rbac_delete_response")
	}
	if result.status == 204 {
		if len(result.data) != 0 {
			return contracts.ActionResult{}, serviceDenied("invalid_rbac_delete_absence_body")
		}
	} else {
		id, err := a.client.rbacValidate(a.kind, result.data)
		if err != nil || id != a.id || a.client.privateConfiguration(a.client.rbacSnapshot(a.kind, result.data)) != a.configuration {
			return contracts.ActionResult{}, serviceDenied("rbac_delete_response_changed")
		}
	}
	return a.result(result), nil
}
func (a *rbacAction) verifyReceipt(result contracts.ActionResult) error {
	if result.ProviderOperationID != "" || text(result.Data["_rbac_delete_receipt"]) != a.receipt() {
		return serviceDenied("rbac_delete_receipt_changed")
	}
	return nil
}
func (a *rbacAction) Readback(ctx context.Context, request contracts.ActionRequest) (result contracts.ReadbackResult, err error) {
	defer func() { err = contracts.DependencyReadError(err) }()
	if err := a.identity(request); err != nil {
		return result, err
	}
	if request.ExecutionResult != nil {
		if err := a.verifyReceipt(*request.ExecutionResult); err != nil {
			return result, err
		}
	}
	if err := a.prerequisitesAbsent(ctx, request); err != nil {
		return result, err
	}
	current, err := a.client.rbacRead(ctx, a.kind, a.wire)
	if isNotFound(err) {
		return result, nil
	}
	if err != nil {
		return result, err
	}
	if a.client.privateConfiguration(a.client.rbacSnapshot(a.kind, current.data)) != a.configuration {
		return result, serviceDenied("rbac_resource_recreated_after_delete")
	}
	return contracts.ReadbackResult{Exists: true, State: "deleting"}, nil
}
func (a *rbacAction) Wait(ctx context.Context, request contracts.ActionRequest, result contracts.ActionResult) (contracts.WaitResult, error) {
	if err := a.verifyReceipt(result); err != nil {
		return contracts.WaitResult{}, err
	}
	request.ExecutionResult = &result
	read, err := a.Readback(ctx, request)
	if err != nil {
		return contracts.WaitResult{}, err
	}
	return contracts.WaitResult{Done: !read.Exists, State: read.State, RetryAfter: 2 * time.Second}, err
}

var _ contracts.ActionDriver = (*rbacAction)(nil)
