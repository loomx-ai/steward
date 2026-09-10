package azure

import (
	"context"
	"maps"
	"strings"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/catalog"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

type insightsChildAction struct {
	client               *client
	kind                 resourceType
	id, parent, location string
	connection           asset.ConnectionID
	partition            string
	deletion             catalog.RESTRequest
}

func newInsightsChildAction(c *client, connection asset.ConnectionID, value asset.Asset, kind resourceType) (*insightsChildAction, error) {
	id, parent, typ, _, err := insightsChildIdentity(value.Identity.NativeID)
	if err != nil || id != value.Identity.NativeID || typ != kind.NativeType || value.Identity.Provider != asset.ProviderAzure || value.Identity.ConnectionID != connection || connection == "" || value.Identity.Partition == "" || value.Location == "" {
		return nil, serviceDenied("invalid_insights_legacy_action_identity")
	}
	deletion, err := c.insightsChildRequest(kind, id, "DELETE")
	if err != nil {
		return nil, err
	}
	return &insightsChildAction{client: c, kind: kind, id: id, parent: parent, location: value.Location, connection: connection, partition: value.Identity.Partition, deletion: deletion}, nil
}

func (*insightsChildAction) DeletionCheckTimeout() time.Duration { return time.Hour }

func (a *insightsChildAction) identity(request contracts.ActionRequest) error {
	value := request.Asset
	if request.Action != "delete" || len(request.Parameters) != 0 || len(request.LifecycleImpacts) != 0 || len(request.PrerequisiteDeletions) != 0 || value.Identity.Provider != asset.ProviderAzure || value.Identity.ConnectionID != a.connection || value.Identity.Partition != a.partition || value.Identity.NativeType != a.kind.NativeType || value.Identity.NativeID != a.id || value.Location != a.location || text(value.Normalized["_insights_component"]) != a.parent || text(value.Normalized["_insights_component_configuration"]) == "" || text(value.Normalized[insightsChildProofKey(a.kind.NativeType)]) == "" {
		return serviceDenied("insights_legacy_action_identity_changed")
	}
	return nil
}

func (a *insightsChildAction) current(ctx context.Context, request contracts.ActionRequest) (map[string]any, response, error) {
	parent, parentErr := a.client.insightsComponent(ctx, a.parent)
	if parentErr != nil && !isNotFound(parentErr) {
		return nil, response{}, parentErr
	}
	if parentErr == nil && (resourceRegion(parent) != a.location || a.client.privateConfiguration(monitorPrivateLinkTargetSnapshot(parent)) != text(request.Asset.Normalized["_insights_component_configuration"])) {
		return nil, response{}, serviceDenied("insights_legacy_component_changed")
	}
	live, err := a.client.insightsChildRead(ctx, a.kind, a.id)
	if err != nil {
		return parent, live, err
	}
	if isNotFound(parentErr) {
		return nil, response{}, serviceDenied("insights_legacy_child_survived_parent")
	}
	if a.client.insightsChildConfiguration(a.id, a.kind.NativeType, live.data) != text(request.Asset.Normalized[insightsChildProofKey(a.kind.NativeType)]) {
		return nil, response{}, serviceDenied("insights_legacy_configuration_changed")
	}
	return parent, live, nil
}

func (a *insightsChildAction) Preflight(ctx context.Context, request contracts.ActionRequest) (check contracts.PreflightResult, err error) {
	defer func() { err = contracts.DependencyReadError(err) }()
	if err := a.identity(request); err != nil {
		return check, err
	}
	parent, _, err := a.current(ctx, request)
	if isNotFound(err) {
		return contracts.PreflightResult{Allowed: true, Absent: true}, nil
	}
	if err != nil {
		return check, err
	}
	if reason := protectionReason(resourceType{NativeType: applicationInsightsType}, parent); reason != "" {
		return contracts.PreflightResult{Reason: reason}, nil
	}
	groupID := strings.Join(strings.Split(a.parent, "/")[:5], "/")
	group, err := a.client.request(ctx, "GET", apiURL(groupID, resourcesVersion))
	if err != nil {
		return check, err
	}
	if !validResourceResponse(group, groupID, groupType) || group.status != 200 {
		return check, serviceDenied("insights_legacy_resource_group_mismatch")
	}
	if text(group.data["managedBy"]) != "" {
		return contracts.PreflightResult{Reason: "azure_managed_resource_group"}, nil
	}
	if protectedAzureTags(object(group.data["tags"])) {
		return contracts.PreflightResult{Reason: "azure_protected_tag"}, nil
	}
	locks, err := a.client.managementLocks(ctx)
	if err != nil {
		return check, err
	}
	if locked(a.parent, locks) {
		return contracts.PreflightResult{Reason: "azure_management_lock"}, nil
	}
	if _, _, err := a.current(ctx, request); err != nil {
		if isNotFound(err) {
			return contracts.PreflightResult{Allowed: true, Absent: true}, nil
		}
		return check, err
	}
	return contracts.PreflightResult{Allowed: true}, nil
}

func (a *insightsChildAction) receipt(request contracts.ActionRequest) string {
	return a.client.privateConfiguration(map[string]any{
		"connection": a.connection, "partition": a.partition, "subscription": a.client.subscription,
		"resource": a.id, "kind": a.kind.NativeType, "parent": a.parent, "location": a.location,
		"parent_configuration": request.Asset.Normalized["_insights_component_configuration"],
		"configuration":        request.Asset.Normalized[insightsChildProofKey(a.kind.NativeType)],
		"method":               "DELETE", "version": insightsChildVersion(a.kind.NativeType), "protocol": "synchronous-native-absence",
	})
}

func (a *insightsChildAction) operationResult(request contracts.ActionRequest, result response) contracts.ActionResult {
	return contracts.ActionResult{ProviderRequestID: result.requestID, Data: map[string]any{insightsChildReceiptKey(a.kind.NativeType): a.receipt(request)}, RetryAfter: retryAfter(result.header)}
}

func (a *insightsChildAction) Execute(ctx context.Context, request contracts.ActionRequest) (contracts.ActionResult, error) {
	check, err := a.Preflight(ctx, request)
	if err != nil {
		return contracts.ActionResult{}, err
	}
	if !check.Allowed {
		return contracts.ActionResult{}, serviceDenied(check.Reason)
	}
	if check.Absent {
		return a.operationResult(request, response{}), nil
	}
	deletion := a.deletion
	deletion.Headers = maps.Clone(deletion.Headers)
	if deletion.Headers == nil {
		deletion.Headers = map[string]string{}
	}
	if request.IdempotencyKey != "" {
		deletion.Headers["x-ms-client-request-id"] = azureRequestID(request.IdempotencyKey)
	}
	result, err := a.client.requestAt(ctx, deletion.Method, deletion.URL, deletion.Body, deletion.Headers, func(endpoint string) error { return a.client.insightsChildEndpoint(endpoint, a.id) })
	if isNotFound(err) {
		return a.operationResult(request, result), nil
	}
	if err != nil {
		return contracts.ActionResult{}, err
	}
	if (result.status != 200 && (a.kind.NativeType != insightsLinkedStorageType || result.status != 204)) || operationLocation(result.header) != "" || result.data["error"] != nil || result.data["code"] != nil {
		return contracts.ActionResult{}, serviceDenied("invalid_insights_legacy_delete_response")
	}
	if a.kind.NativeType == insightsExportType {
		_, _, _, selector, _ := insightsLegacyIdentity(a.id)
		if err := insightsLegacyResponseIdentity(insightsLegacyKind(a.kind.NativeType), selector, result.data); err != nil {
			return contracts.ActionResult{}, err
		}
		if a.client.insightsChildConfiguration(a.id, a.kind.NativeType, result.data) != text(request.Asset.Normalized[insightsChildProofKey(a.kind.NativeType)]) {
			return contracts.ActionResult{}, serviceDenied("insights_export_delete_configuration_changed")
		}
	} else if a.kind.NativeType == insightsAPIKeyType {
		if err := insightsARMChildResponseIdentity(a.id, a.kind.NativeType, result.data); err != nil {
			return contracts.ActionResult{}, err
		}
		if a.client.insightsChildConfiguration(a.id, a.kind.NativeType, result.data) != text(request.Asset.Normalized[insightsChildProofKey(a.kind.NativeType)]) {
			return contracts.ActionResult{}, serviceDenied("insights_api_key_delete_configuration_changed")
		}
	} else if len(result.data) != 0 {
		return contracts.ActionResult{}, serviceDenied("unexpected_insights_legacy_delete_body")
	}
	return a.operationResult(request, result), nil
}

func (a *insightsChildAction) verifyReceipt(request contracts.ActionRequest, result contracts.ActionResult) error {
	if result.ProviderOperationID != "" || text(result.Data[insightsChildReceiptKey(a.kind.NativeType)]) != a.receipt(request) {
		return serviceDenied("insights_legacy_operation_receipt_changed")
	}
	return nil
}

func (a *insightsChildAction) Wait(ctx context.Context, request contracts.ActionRequest, result contracts.ActionResult) (contracts.WaitResult, error) {
	if err := a.identity(request); err != nil {
		return contracts.WaitResult{}, err
	}
	if err := a.verifyReceipt(request, result); err != nil {
		return contracts.WaitResult{}, err
	}
	request.ExecutionResult = &result
	read, err := a.Readback(ctx, request)
	if err != nil {
		return contracts.WaitResult{}, err
	}
	return contracts.WaitResult{Done: !read.Exists, State: read.State, RetryAfter: 2 * time.Second}, nil
}

func (a *insightsChildAction) Readback(ctx context.Context, request contracts.ActionRequest) (contracts.ReadbackResult, error) {
	if err := a.identity(request); err != nil {
		return contracts.ReadbackResult{}, err
	}
	if request.ExecutionResult != nil {
		if err := a.verifyReceipt(request, *request.ExecutionResult); err != nil {
			return contracts.ReadbackResult{}, err
		}
	}
	_, _, err := a.current(ctx, request)
	if isNotFound(err) {
		return contracts.ReadbackResult{}, nil
	}
	if err != nil {
		return contracts.ReadbackResult{}, err
	}
	return contracts.ReadbackResult{Exists: true, State: "deleting"}, nil
}

var _ contracts.ActionDriver = (*insightsChildAction)(nil)
