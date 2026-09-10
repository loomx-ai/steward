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

type insightsWorkbookAction struct {
	client                                              *client
	assetID                                             asset.AssetID
	connection                                          asset.ConnectionID
	partition, id, kind, location, configuration, group string
	deletion                                            catalog.RESTRequest
}

func newInsightsWorkbookAction(c *client, connection asset.ConnectionID, value asset.Asset) (*insightsWorkbookAction, error) {
	id, typ, err := parseID(value.Identity.NativeID)
	kind := insightsWorkbookKind(typ)
	if err != nil || kind == "" || value.ID == "" || id != value.Identity.NativeID || kind != value.Identity.NativeType || value.Identity.Provider != asset.ProviderAzure || value.Identity.ConnectionID != connection || connection == "" || value.Identity.Partition == "" || value.Location == "" || value.Location != strings.ToLower(value.Location) || text(value.Normalized[insightsWorkbookProof]) == "" || text(value.Normalized["_insights_workbook_group"]) == "" {
		return nil, serviceDenied("invalid_workbook_action_identity")
	}
	deletion, err := c.workbookRequest(kind, id, "DELETE")
	if err != nil {
		return nil, err
	}
	return &insightsWorkbookAction{client: c, assetID: value.ID, connection: connection, partition: value.Identity.Partition, id: id, kind: kind, location: value.Location, configuration: text(value.Normalized[insightsWorkbookProof]), group: text(value.Normalized["_insights_workbook_group"]), deletion: deletion}, nil
}

func (*insightsWorkbookAction) DeletionCheckTimeout() time.Duration { return time.Hour }

func (a *insightsWorkbookAction) identity(request contracts.ActionRequest) error {
	value := request.Asset
	if request.Action != "delete" || len(request.Parameters)+len(request.LifecycleImpacts)+len(request.PrerequisiteDeletions) != 0 || value.ID != a.assetID || value.Identity.Provider != asset.ProviderAzure || value.Identity.ConnectionID != a.connection || value.Identity.Partition != a.partition || value.Identity.NativeType != a.kind || value.Identity.NativeID != a.id || value.Location != a.location || text(value.Normalized[insightsWorkbookProof]) != a.configuration || text(value.Normalized["_insights_workbook_group"]) != a.group {
		return serviceDenied("workbook_action_identity_changed")
	}
	return nil
}

func (a *insightsWorkbookAction) current(ctx context.Context) (insightsWorkbookRecord, error) {
	record, err := a.client.workbookRecord(ctx, a.kind, a.id)
	if err != nil {
		return record, err
	}
	if resourceRegion(record.raw) != a.location || a.client.workbookConfiguration(record) != a.configuration {
		return record, serviceDenied("workbook_configuration_changed")
	}
	return record, nil
}

func (a *insightsWorkbookAction) Preflight(ctx context.Context, request contracts.ActionRequest) (check contracts.PreflightResult, err error) {
	defer func() { err = contracts.DependencyReadError(err) }()
	if err := a.identity(request); err != nil {
		return check, err
	}
	for range 2 {
		record, err := a.current(ctx)
		if isNotFound(err) {
			return contracts.PreflightResult{Allowed: true, Absent: true}, nil
		}
		if err != nil {
			return check, err
		}
		mapping, _ := findType(a.kind)
		if reason := protectionReason(mapping, record.raw); reason != "" {
			return contracts.PreflightResult{Reason: reason}, nil
		}
		group, err := a.client.workbookGroup(ctx, a.id)
		if err != nil {
			return check, err
		}
		if text(group["managedBy"]) != "" {
			return contracts.PreflightResult{Reason: "azure_managed_resource_group"}, nil
		}
		if protectedAzureTags(object(group["tags"])) {
			return contracts.PreflightResult{Reason: "azure_protected_tag"}, nil
		}
		if a.client.privateConfiguration(insightsWorkspaceResourceSnapshot(group)) != a.group {
			return check, serviceDenied("workbook_resource_group_changed")
		}
		locks, err := a.client.managementLocks(ctx)
		if err != nil {
			return check, err
		}
		if locked(a.id, locks) {
			return contracts.PreflightResult{Reason: "azure_management_lock"}, nil
		}
	}
	return contracts.PreflightResult{Allowed: true}, nil
}

func (a *insightsWorkbookAction) receipt() string {
	return a.client.privateConfiguration(map[string]any{"connection": a.connection, "partition": a.partition, "subscription": a.client.subscription, "asset": a.assetID, "id": a.id, "kind": a.kind, "location": a.location, "configuration": a.configuration, "group": a.group, "method": "DELETE", "version": insightsWorkbookVersion(a.kind), "protocol": "synchronous-active-resource-absence"})
}

func (a *insightsWorkbookAction) result(response response) contracts.ActionResult {
	return contracts.ActionResult{ProviderRequestID: response.requestID, RetryAfter: retryAfter(response.header), Data: map[string]any{"_insights_workbook_receipt": a.receipt()}}
}

func (a *insightsWorkbookAction) Execute(ctx context.Context, request contracts.ActionRequest) (contracts.ActionResult, error) {
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
			return serviceDenied("workbook_delete_endpoint_changed")
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
		return contracts.ActionResult{}, serviceDenied("invalid_workbook_delete_response")
	}
	return a.result(result), nil
}

func (a *insightsWorkbookAction) verifyReceipt(result contracts.ActionResult) error {
	if result.ProviderOperationID != "" || text(result.Data["_insights_workbook_receipt"]) != a.receipt() {
		return serviceDenied("workbook_operation_receipt_changed")
	}
	return nil
}

func (a *insightsWorkbookAction) Wait(ctx context.Context, request contracts.ActionRequest, result contracts.ActionResult) (contracts.WaitResult, error) {
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

func (a *insightsWorkbookAction) Readback(ctx context.Context, request contracts.ActionRequest) (contracts.ReadbackResult, error) {
	if err := a.identity(request); err != nil {
		return contracts.ReadbackResult{}, err
	}
	if request.ExecutionResult != nil {
		if err := a.verifyReceipt(*request.ExecutionResult); err != nil {
			return contracts.ReadbackResult{}, err
		}
	}
	_, err := a.current(ctx)
	if isNotFound(err) {
		return contracts.ReadbackResult{}, nil
	}
	if err != nil {
		return contracts.ReadbackResult{}, err
	}
	return contracts.ReadbackResult{Exists: true, State: "deleting"}, nil
}

var _ contracts.ActionDriver = (*insightsWorkbookAction)(nil)
