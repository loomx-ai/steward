package azure

import (
	"context"
	"maps"
	"strings"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

type netappVolumeAction struct {
	client   *client
	planned  asset.Asset
	boundary map[string]any
}

func newNetappVolumeAction(c *client, connection asset.ConnectionID, value asset.Asset) (*netappVolumeAction, error) {
	boundary, err := c.netappVolumeRecorded(value)
	if err != nil {
		return nil, err
	}
	if value.Identity.ConnectionID != connection || value.Normalized["cleanup_protected"] != false || boundary["protected"] != false || boundary["ready"] != true {
		return nil, serviceDenied("netapp_volume_not_ready")
	}
	return &netappVolumeAction{client: c, planned: value, boundary: boundary}, nil
}
func (a *netappVolumeAction) binding(req contracts.ActionRequest, result contracts.ActionResult) string {
	req.IdempotencyKey, req.ExecutionResult = "", nil
	data := maps.Clone(result.Data)
	delete(data, "binding")
	return a.client.privateConfiguration(map[string]any{"protocol": "netapp-volume-action-1", "request": req, "origin": result.ProviderOperationID, "data": data})
}
func (a *netappVolumeAction) identity(req contracts.ActionRequest) error {
	if req.Action != "delete" || req.Asset.ID != a.planned.ID || req.Asset.Identity != a.planned.Identity || req.Asset.Location != a.planned.Location || len(req.Parameters)+len(req.PrerequisiteDeletions) != 0 || req.Asset.Normalized[netappVolumeProof] != a.planned.Normalized[netappVolumeProof] {
		return serviceDenied("netapp_volume_request_changed")
	}
	if _, err := a.client.netappVolumeRecorded(req.Asset); err != nil {
		return err
	}
	members := object(a.boundary["members"])
	seen := map[string]bool{}
	for _, impact := range req.LifecycleImpacts {
		v := impact.Asset
		id := v.Identity.NativeID
		entry := object(members[id])
		if seen[id] || entry == nil || !impact.Delete || impact.ControllerID != a.planned.ID || v.ID == "" || v.Identity.Provider != asset.ProviderAzure || v.Identity.ConnectionID != a.planned.Identity.ConnectionID || v.Identity.Partition != a.planned.Identity.Partition || v.Identity.NativeType != entry["kind"] || v.Location != a.planned.Location || v.Normalized["_netapp_configuration"] != entry["configuration"] {
			return serviceDenied("netapp_volume_impact_changed")
		}
		seen[id] = true
	}
	for id, value := range members {
		if !seen[id] && object(value)["absent"] != true {
			return serviceDenied("netapp_volume_unreviewed_member")
		}
	}
	if req.ExecutionResult != nil && req.ExecutionResult.Data["binding"] != a.binding(req, *req.ExecutionResult) {
		return serviceDenied("netapp_volume_receipt_changed")
	}
	return nil
}
func (a *netappVolumeAction) Readback(ctx context.Context, req contracts.ActionRequest) (contracts.ReadbackResult, error) {
	if err := a.identity(req); err != nil {
		return contracts.ReadbackResult{}, err
	}
	id := a.planned.Identity.NativeID
	// A readable, unchanged capacity-pool incarnation is required before a volume
	// 404 can describe this reviewed scope rather than an unavailable parent.
	pool, err := a.client.netappRead(ctx, redisParentID(id), netappPoolType)
	if err != nil {
		return contracts.ReadbackResult{}, contracts.DependencyReadError(err)
	}
	if object(pool.data["properties"])["poolId"] != a.boundary["pool_id"] {
		return contracts.ReadbackResult{}, serviceDenied("netapp_volume_pool_recreated")
	}
	res, err := a.client.netappRead(ctx, id, netappVolumeType)
	if err == nil {
		if object(res.data["properties"])["fileSystemId"] != a.boundary["file_system_id"] || resourceRegion(res.data) != a.planned.Location {
			return contracts.ReadbackResult{}, serviceDenied("netapp_volume_recreated")
		}
		return contracts.ReadbackResult{Exists: true, State: text(object(res.data["properties"])["provisioningState"])}, nil
	}
	if !isNotFound(err) {
		return contracts.ReadbackResult{}, err
	}
	for child, value := range object(a.boundary["members"]) {
		kind := text(object(value)["kind"])
		_, err := a.client.netappRead(ctx, child, kind)
		if isNotFound(err) {
			continue
		}
		if err != nil {
			return contracts.ReadbackResult{}, contracts.DependencyReadError(err)
		}
		return contracts.ReadbackResult{Exists: true, State: "Deleting"}, nil
	}
	return contracts.ReadbackResult{Exists: false}, nil
}
func (a *netappVolumeAction) current(ctx context.Context, req contracts.ActionRequest) (bool, error) {
	if err := a.identity(req); err != nil {
		return false, err
	}
	own, err := a.client.netappRead(ctx, a.planned.Identity.NativeID, netappVolumeType)
	if isNotFound(err) {
		read, err := a.Readback(ctx, req)
		if err != nil {
			return false, err
		}
		if read.Exists {
			return false, serviceDenied("netapp_volume_members_survive")
		}
		return true, nil
	}
	if err != nil {
		return false, err
	}
	if a.client.privateConfiguration(own.data) != a.boundary["volume"] {
		return false, serviceDenied("netapp_volume_changed")
	}
	fresh, err := a.client.netappVolumeBoundary(ctx, a.planned.Identity.NativeID, object(a.boundary["members"]))
	if err != nil {
		return false, err
	}
	for _, key := range []string{"volume", "pool", "account", "group", "region", "file_system_id", "pool_id", "replications"} {
		if fresh[key] != a.boundary[key] {
			return false, serviceDenied("netapp_volume_context_changed")
		}
	}
	if err := a.client.netappNetworkMatches(ctx, object(a.boundary["network"]), object(fresh["network"])); err != nil {
		return false, err
	}
	if fresh["protected"] != false || fresh["ready"] != true {
		return false, serviceDenied("netapp_volume_not_ready")
	}
	old := object(a.boundary["members"])
	for id, value := range object(fresh["members"]) {
		entry, expected := object(value), object(old[id])
		if expected == nil || entry["kind"] != expected["kind"] || entry["configuration"] != expected["configuration"] || expected["absent"] == true && entry["absent"] != true {
			return false, serviceDenied("netapp_volume_members_changed")
		}
	}
	return false, nil
}
func (a *netappVolumeAction) Preflight(ctx context.Context, req contracts.ActionRequest) (contracts.PreflightResult, error) {
	absent, err := a.current(ctx, req)
	if err != nil {
		return contracts.PreflightResult{}, err
	}
	return contracts.PreflightResult{Allowed: true, Absent: absent}, nil
}
func (a *netappVolumeAction) Execute(ctx context.Context, req contracts.ActionRequest) (contracts.ActionResult, error) {
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
		op, _ := metadata.catalog.Operation("Azure.Microsoft.NetApp.Volumes_Delete")
		parts := strings.Split(id, "/")
		bound, err := bindAzureREST(op, map[string]any{"subscriptionId": a.client.subscription, "resourceGroupName": parts[4], "accountName": parts[8], "poolName": parts[10], "volumeName": parts[12]})
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
func (a *netappVolumeAction) Wait(ctx context.Context, req contracts.ActionRequest, result contracts.ActionResult) (contracts.WaitResult, error) {
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
func (*netappVolumeAction) DeletionCheckTimeout() time.Duration { return 24 * time.Hour }
