package azure

import (
	"context"
	"maps"
	"strings"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

type netappPoolAction struct {
	client   *client
	planned  asset.Asset
	boundary map[string]any
}

func newNetappPoolAction(c *client, connection asset.ConnectionID, value asset.Asset) (*netappPoolAction, error) {
	boundary, err := c.netappPoolRecorded(value)
	if err != nil {
		return nil, err
	}
	if value.Identity.ConnectionID != connection || value.Normalized["cleanup_protected"] != false || boundary["protected"] != false || boundary["ready"] != true {
		return nil, serviceDenied("netapp_pool_not_ready")
	}
	return &netappPoolAction{client: c, planned: value, boundary: boundary}, nil
}
func (a *netappPoolAction) binding(req contracts.ActionRequest, result contracts.ActionResult) string {
	req.IdempotencyKey, req.ExecutionResult = "", nil
	data := maps.Clone(result.Data)
	delete(data, "binding")
	return a.client.privateConfiguration(map[string]any{"protocol": "netapp-pool-action-1", "request": req, "origin": result.ProviderOperationID, "data": data})
}
func (a *netappPoolAction) identity(req contracts.ActionRequest) error {
	if req.Action != "delete" || req.Asset.ID != a.planned.ID || req.Asset.Identity != a.planned.Identity || req.Asset.Location != a.planned.Location || len(req.Parameters)+len(req.LifecycleImpacts) != 0 || req.Asset.Normalized[netappPoolProof] != a.planned.Normalized[netappPoolProof] {
		return serviceDenied("netapp_pool_request_changed")
	}
	if _, err := a.client.netappPoolRecorded(req.Asset); err != nil {
		return err
	}
	members := object(a.boundary["members"])
	seen := map[string]bool{}
	for _, impact := range req.PrerequisiteDeletions {
		value := impact.Asset
		id := value.Identity.NativeID
		entry := object(members[id])
		boundary, err := a.client.netappVolumeRecorded(value)
		if err != nil || seen[id] || entry == nil || !impact.Delete || impact.ControllerID != a.planned.ID || value.Identity.ConnectionID != a.planned.Identity.ConnectionID || value.Location != a.planned.Location || entry["configuration"] != value.Normalized["_netapp_configuration"] || entry["uid"] != boundary["file_system_id"] {
			return serviceDenied("netapp_pool_prerequisite_changed")
		}
		seen[id] = true
	}
	for id, value := range members {
		if object(value)["absent"] != true && !seen[id] {
			return serviceDenied("netapp_pool_unreviewed_volume")
		}
	}
	if req.ExecutionResult != nil && req.ExecutionResult.Data["binding"] != a.binding(req, *req.ExecutionResult) {
		return serviceDenied("netapp_pool_receipt_changed")
	}
	return nil
}
func (a *netappPoolAction) Readback(ctx context.Context, req contracts.ActionRequest) (contracts.ReadbackResult, error) {
	if err := a.identity(req); err != nil {
		return contracts.ReadbackResult{}, err
	}
	_, _, region, _, ready, err := a.client.netappRecoveryParents(ctx, a.planned.Identity.NativeID, netappPoolType)
	if err != nil {
		return contracts.ReadbackResult{}, err
	}
	if region != a.planned.Location || !ready {
		return contracts.ReadbackResult{}, serviceDenied("netapp_pool_parent_unavailable")
	}
	own, err := a.client.netappRead(ctx, a.planned.Identity.NativeID, netappPoolType)
	if isNotFound(err) {
		return contracts.ReadbackResult{Exists: false}, nil
	}
	if err != nil {
		return contracts.ReadbackResult{}, err
	}
	if object(own.data["properties"])["poolId"] != a.boundary["pool_id"] || resourceRegion(own.data) != a.planned.Location {
		return contracts.ReadbackResult{}, serviceDenied("netapp_pool_recreated")
	}
	// Volume steps independently verify their own scope before this step runs.
	// This readback closes only the capacity-pool record, never its children.
	return contracts.ReadbackResult{Exists: true, State: text(object(own.data["properties"])["provisioningState"])}, nil
}
func (a *netappPoolAction) current(ctx context.Context, req contracts.ActionRequest) (bool, error) {
	if err := a.identity(req); err != nil {
		return false, err
	}
	_, err := a.client.netappRead(ctx, a.planned.Identity.NativeID, netappPoolType)
	if isNotFound(err) {
		read, err := a.Readback(ctx, req)
		return err == nil && !read.Exists, err
	}
	if err != nil {
		return false, err
	}
	review, err := a.client.netappPoolBoundary(ctx, a.planned.Identity.NativeID, object(a.boundary["members"]))
	if err != nil {
		return false, err
	}
	for _, key := range []string{"region", "configuration", "pool_id"} {
		if review[key] != a.boundary[key] {
			return false, serviceDenied("netapp_pool_context_changed")
		}
	}
	if a.client.privateConfiguration(object(review["parents"])) != a.client.privateConfiguration(object(a.boundary["parents"])) || review["ready"] != true || review["protected"] != false {
		return false, serviceDenied("netapp_pool_context_changed")
	}
	for _, value := range object(review["members"]) {
		if object(value)["absent"] != true {
			return false, serviceDenied("netapp_pool_volumes_remain")
		}
	}
	return false, nil
}
func (a *netappPoolAction) Preflight(ctx context.Context, req contracts.ActionRequest) (contracts.PreflightResult, error) {
	absent, err := a.current(ctx, req)
	if err != nil {
		return contracts.PreflightResult{}, err
	}
	return contracts.PreflightResult{Allowed: true, Absent: absent}, nil
}
func (a *netappPoolAction) Execute(ctx context.Context, req contracts.ActionRequest) (contracts.ActionResult, error) {
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
		op, _ := metadata.catalog.Operation("Azure.Microsoft.NetApp.Pools_Delete")
		parts := strings.Split(id, "/")
		params := map[string]any{"subscriptionId": a.client.subscription, "resourceGroupName": parts[4], "accountName": parts[8], "poolName": parts[10]}
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
func (a *netappPoolAction) Wait(ctx context.Context, req contracts.ActionRequest, result contracts.ActionResult) (contracts.WaitResult, error) {
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
func (*netappPoolAction) DeletionCheckTimeout() time.Duration { return 24 * time.Hour }
