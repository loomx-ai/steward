package azure

import (
	"context"
	"maps"
	"strings"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

type netappRecoveryAction struct {
	client   *client
	planned  asset.Asset
	boundary map[string]any
}

func newNetappRecoveryAction(c *client, connection asset.ConnectionID, value asset.Asset) (*netappRecoveryAction, error) {
	boundary, err := c.netappRecoveryRecorded(value)
	if err != nil {
		return nil, err
	}
	if value.Identity.ConnectionID != connection || value.Normalized["cleanup_protected"] != false || boundary["protected"] != false || boundary["ready"] != true {
		return nil, serviceDenied("netapp_recovery_not_ready")
	}
	return &netappRecoveryAction{client: c, planned: value, boundary: boundary}, nil
}
func (a *netappRecoveryAction) binding(req contracts.ActionRequest, result contracts.ActionResult) string {
	req.IdempotencyKey, req.ExecutionResult = "", nil
	data := maps.Clone(result.Data)
	delete(data, "binding")
	return a.client.privateConfiguration(map[string]any{"protocol": "netapp-recovery-action-1", "request": req, "origin": result.ProviderOperationID, "data": data})
}
func (a *netappRecoveryAction) identity(req contracts.ActionRequest) error {
	if req.Action != "delete" || req.Asset.ID != a.planned.ID || req.Asset.Identity != a.planned.Identity || req.Asset.Location != a.planned.Location || len(req.Parameters)+len(req.PrerequisiteDeletions)+len(req.LifecycleImpacts) != 0 || req.Asset.Normalized[netappRecoveryProof] != a.planned.Normalized[netappRecoveryProof] {
		return serviceDenied("netapp_recovery_request_changed")
	}
	if _, err := a.client.netappRecoveryRecorded(req.Asset); err != nil {
		return err
	}
	if req.ExecutionResult != nil && req.ExecutionResult.Data["binding"] != a.binding(req, *req.ExecutionResult) {
		return serviceDenied("netapp_recovery_receipt_changed")
	}
	return nil
}
func (c *client) netappRecoveryRecorded(value asset.Asset) (map[string]any, error) {
	kind := value.Identity.NativeType
	id := value.Identity.NativeID
	review := object(value.Normalized[netappRecoveryReview])
	if !netappRecoveryKind(kind) || c.netappIdentity(id, kind) != nil || value.ID == "" || value.Identity.Provider != asset.ProviderAzure || value.Identity.ConnectionID == "" || value.Identity.Partition != "azure" || len(review) != 10 || value.Location != review["region"] || review["resource"] != value.Normalized["_netapp_configuration"] || value.Normalized[netappRecoveryProof] != c.netappRecoveryProofFor(id, value.Identity.ConnectionID, review) {
		return nil, serviceDenied("invalid_netapp_recovery_review")
	}
	return review, nil
}
func (a *netappRecoveryAction) Readback(ctx context.Context, req contracts.ActionRequest) (contracts.ReadbackResult, error) {
	if err := a.identity(req); err != nil {
		return contracts.ReadbackResult{}, err
	}
	id, kind := a.planned.Identity.NativeID, a.planned.Identity.NativeType
	_, incarnations, region, _, ready, err := a.client.netappRecoveryParents(ctx, id, kind)
	if err != nil {
		return contracts.ReadbackResult{}, err
	}
	if region != a.planned.Location || a.client.privateConfiguration(incarnations) != a.client.privateConfiguration(object(a.boundary["incarnations"])) {
		return contracts.ReadbackResult{}, serviceDenied("netapp_recovery_parent_recreated")
	}
	res, err := a.client.netappRead(ctx, id, kind)
	if isNotFound(err) {
		if !ready {
			return contracts.ReadbackResult{}, serviceDenied("netapp_recovery_lookup_unavailable")
		}
		return contracts.ReadbackResult{Exists: false}, nil
	}
	if err != nil {
		return contracts.ReadbackResult{}, err
	}
	createdKey := "created"
	if kind == netappBackupType {
		createdKey = "creationDate"
	}
	if netappRecoveryIncarnation(kind, res.data) != a.boundary["uid"] || object(res.data["properties"])[createdKey] != a.boundary["created"] {
		return contracts.ReadbackResult{}, serviceDenied("netapp_recovery_resource_recreated")
	}
	return contracts.ReadbackResult{Exists: true, State: text(object(res.data["properties"])["provisioningState"])}, nil
}
func (a *netappRecoveryAction) current(ctx context.Context, req contracts.ActionRequest) (bool, error) {
	if err := a.identity(req); err != nil {
		return false, err
	}
	_, err := a.client.netappRead(ctx, a.planned.Identity.NativeID, a.planned.Identity.NativeType)
	if isNotFound(err) {
		read, err := a.Readback(ctx, req)
		return err == nil && !read.Exists, err
	}
	if err != nil {
		return false, err
	}
	review, absent, err := a.client.netappRecoveryBoundary(ctx, a.planned.Identity.NativeID, a.planned.Identity.NativeType, object(a.boundary["siblings"]))
	if err != nil {
		return false, err
	}
	if absent {
		read, err := a.Readback(ctx, req)
		return err == nil && !read.Exists, err
	}
	if a.client.privateConfiguration(review) != a.client.privateConfiguration(a.boundary) || review["ready"] != true || review["protected"] != false {
		return false, serviceDenied("netapp_recovery_context_changed")
	}
	return false, nil
}
func (a *netappRecoveryAction) Preflight(ctx context.Context, req contracts.ActionRequest) (contracts.PreflightResult, error) {
	absent, err := a.current(ctx, req)
	if err != nil {
		return contracts.PreflightResult{}, err
	}
	return contracts.PreflightResult{Allowed: true, Absent: absent}, nil
}
func (a *netappRecoveryAction) Execute(ctx context.Context, req contracts.ActionRequest) (contracts.ActionResult, error) {
	if err := a.identity(req); err != nil {
		return contracts.ActionResult{}, err
	}
	if req.ExecutionResult != nil {
		return *req.ExecutionResult, nil
	}
	absent, err := a.current(ctx, req)
	if err != nil {
		return contracts.ActionResult{}, err
	}
	result := contracts.ActionResult{Data: map[string]any{"operation_done": false}}
	if !absent {
		id := a.planned.Identity.NativeID
		metadata, err := providerData()
		if err != nil {
			return result, err
		}
		operation := "Snapshots_Delete"
		if a.planned.Identity.NativeType == netappBackupType {
			operation = "Backups_Delete"
		}
		op, _ := metadata.catalog.Operation("Azure.Microsoft.NetApp." + operation)
		parts := strings.Split(id, "/")
		params := map[string]any{"subscriptionId": a.client.subscription, "resourceGroupName": parts[4], "accountName": parts[8]}
		if a.planned.Identity.NativeType == netappBackupType {
			params["backupVaultName"], params["backupName"] = parts[10], parts[12]
		} else {
			params["poolName"], params["volumeName"], params["snapshotName"] = parts[10], parts[12], parts[14]
		}
		bound, err := bindAzureREST(op, params)
		if err != nil {
			return result, err
		}
		res, err := a.client.requestBody(ctx, bound.Method, bound.URL, bound.Body, map[string]string{"x-ms-client-request-id": azureRequestID(req.IdempotencyKey + ":delete:" + id)})
		if err != nil && !isNotFound(err) {
			return result, err
		}
		result.ProviderRequestID, result.RetryAfter = res.requestID, retryAfter(res.header)
		if err == nil {
			receipt, err := a.client.netappDeleteReceipt(id, a.planned.Location, res)
			if err != nil {
				return result, err
			}
			result.Data["operation"] = receipt
		}
	}
	result.Data["binding"] = a.binding(req, result)
	// No network request follows acceptance before the worker can save it.
	return result, nil
}
func (a *netappRecoveryAction) Wait(ctx context.Context, req contracts.ActionRequest, result contracts.ActionResult) (contracts.WaitResult, error) {
	req.ExecutionResult = &result
	if err := a.identity(req); err != nil {
		return contracts.WaitResult{}, err
	}
	out := contracts.WaitResult{State: "Deleting", RetryAfter: 2 * time.Second, Data: batchClone(result.Data)}
	if receipt := object(result.Data["operation"]); receipt != nil && result.Data["operation_done"] != true && result.Data["scope_absent"] != true {
		poll, err := a.client.netappPoll(ctx, a.planned.Identity.NativeID, a.planned.Location, receipt)
		if err != nil {
			if ctx.Err() != nil {
				return out, err
			}
			read, ownErr := a.Readback(ctx, req)
			if ownErr == nil && !read.Exists {
				out.Done = true
				out.Data["scope_absent"] = true
				updated := result
				updated.Data = out.Data
				out.Data["binding"] = a.binding(req, updated)
				return out, nil
			}
			return out, err
		}
		out.Data["operation"], out.Data["operation_done"] = poll.Data, poll.Done
		updated := result
		updated.Data = out.Data
		out.Data["binding"] = a.binding(req, updated)
		if poll.RetryAfter > 0 {
			out.RetryAfter = poll.RetryAfter
		}
		return out, nil
	}
	read, err := a.Readback(ctx, req)
	out.Done = err == nil && !read.Exists
	return out, err
}
func (*netappRecoveryAction) DeletionCheckTimeout() time.Duration { return 24 * time.Hour }
