package azure

import (
	"context"
	"encoding/json"
	"maps"
	"slices"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

const netappSnapshotPolicyType = netappAccountType + "/snapshotPolicies"

func netappDetachedSnapshotVolume(raw map[string]any) map[string]any {
	out := hybridComputeChildSnapshot(raw)
	p := object(out["properties"])
	dp := object(p["dataProtection"])
	snapshot := object(dp["snapshot"])
	delete(snapshot, "snapshotPolicyId")
	if len(snapshot) == 0 {
		delete(dp, "snapshot")
	}
	if len(dp) == 0 {
		delete(p, "dataProtection")
	}
	return out
}

type netappSnapshotPolicyAction struct {
	client  *client
	planned asset.Asset
	review  map[string]any
}

func newNetappSnapshotPolicyAction(c *client, connection asset.ConnectionID, value asset.Asset) (*netappSnapshotPolicyAction, error) {
	review := object(value.Normalized[netappAssignmentReview])
	if value.ID == "" || value.Identity.Provider != asset.ProviderAzure || value.Identity.Partition != "azure" || value.Identity.ConnectionID != connection || value.Identity.NativeType != netappSnapshotPolicyType || c.netappIdentity(value.Identity.NativeID, netappSnapshotPolicyType) != nil || len(review) != 6 || review["native_index_complete"] != true || review["region"] != value.Location || review["configuration"] != value.Normalized["_netapp_configuration"] || value.Normalized[netappAssignmentProof] != c.netappAssignmentProofFor(value.Identity.NativeID, connection, review) || value.Normalized["cleanup_protected"] != false {
		return nil, serviceDenied("invalid_netapp_snapshot_policy_review")
	}
	return &netappSnapshotPolicyAction{c, value, review}, nil
}
func (a *netappSnapshotPolicyAction) binding(req contracts.ActionRequest, data map[string]any) string {
	req.IdempotencyKey = ""
	req.ExecutionResult = nil
	data = maps.Clone(data)
	delete(data, "binding")
	return a.client.privateConfiguration(map[string]any{"protocol": "netapp-snapshot-policy-delete-1", "request": req, "data": data})
}
func (a *netappSnapshotPolicyAction) identity(req contracts.ActionRequest) error {
	if req.Action != "delete" || req.Asset.ID != a.planned.ID || req.Asset.Identity != a.planned.Identity || req.Asset.Location != a.planned.Location || req.Asset.Normalized[netappAssignmentProof] != a.planned.Normalized[netappAssignmentProof] || len(req.Parameters)+len(req.PrerequisiteDeletions) != 0 {
		return serviceDenied("netapp_snapshot_policy_request_changed")
	}
	if _, err := newNetappSnapshotPolicyAction(a.client, a.planned.Identity.ConnectionID, req.Asset); err != nil {
		return err
	}
	consumers := object(a.review["consumers"])
	seen := map[string]bool{}
	children := map[string]map[string]any{}
	for _, impact := range req.LifecycleImpacts {
		v := impact.Asset
		if v.Identity.NativeType != netappVolumeType {
			continue
		}
		id := v.Identity.NativeID
		entry := object(consumers[id])
		if impact.Delete || impact.ControllerID != a.planned.ID || seen[id] || entry == nil || v.Identity.ConnectionID != a.planned.Identity.ConnectionID || v.Location != a.planned.Location {
			return serviceDenied("netapp_snapshot_policy_impact_changed")
		}
		boundary, err := a.client.netappVolumeRecorded(v)
		if err != nil || entry["configuration"] != v.Normalized["_netapp_configuration"] || entry["uid"] != boundary["file_system_id"] || text(entry["detached_configuration"]) == "" {
			return serviceDenied("netapp_snapshot_policy_volume_changed")
		}
		seen[id] = true
		for child, raw := range object(boundary["members"]) {
			member := maps.Clone(object(raw))
			member["controller"] = v.ID
			children[child] = member
		}
	}
	if len(seen) != len(consumers) {
		return serviceDenied("netapp_snapshot_policy_unreviewed_consumer")
	}
	seenChildren := map[string]bool{}
	for _, impact := range req.LifecycleImpacts {
		v := impact.Asset
		if v.Identity.NativeType == netappVolumeType {
			continue
		}
		id := v.Identity.NativeID
		entry := children[id]
		if entry == nil || seenChildren[id] || impact.Delete || impact.ControllerID != entry["controller"] || v.ID == "" || v.Identity.Provider != a.planned.Identity.Provider || v.Identity.Partition != a.planned.Identity.Partition || v.Identity.ConnectionID != a.planned.Identity.ConnectionID || v.Location != a.planned.Location || v.Identity.NativeType != entry["kind"] || v.Normalized["_netapp_configuration"] != entry["configuration"] {
			return serviceDenied("netapp_snapshot_policy_retained_child_changed")
		}
		seenChildren[id] = true
	}
	if len(seenChildren) != len(children) {
		return serviceDenied("netapp_snapshot_policy_unreviewed_retained_child")
	}
	if req.ExecutionResult != nil && req.ExecutionResult.Data["binding"] != a.binding(req, req.ExecutionResult.Data) {
		return serviceDenied("netapp_snapshot_policy_receipt_changed")
	}
	return nil
}
func (a *netappSnapshotPolicyAction) root(ctx context.Context, deleting bool) (map[string]any, bool, error) {
	parents, _, region, protected, ready, err := a.client.netappRecoveryParents(ctx, a.planned.Identity.NativeID, netappSnapshotPolicyType)
	if err != nil {
		return nil, false, err
	}
	if region != a.planned.Location || a.client.privateConfiguration(parents) != a.client.privateConfiguration(object(a.review["parents"])) || protected || !ready {
		return nil, false, serviceDenied("netapp_snapshot_policy_parent_changed")
	}
	own, err := a.client.netappRead(ctx, a.planned.Identity.NativeID, netappSnapshotPolicyType)
	if isNotFound(err) {
		return nil, true, nil
	}
	if err != nil {
		return nil, false, err
	}
	if resourceRegion(own.data) != region || !deleting && object(own.data["properties"])["provisioningState"] != "Succeeded" || protectedAzureTags(object(own.data["tags"])) || text(own.data["managedBy"]) != "" || a.client.privateConfiguration(hybridComputeChildSnapshot(own.data)) != a.review["policy_configuration"] {
		return nil, false, serviceDenied("netapp_snapshot_policy_changed")
	}
	return own.data, false, nil
}
func (a *netappSnapshotPolicyAction) volume(ctx context.Context, id string, detached bool) (map[string]any, error) {
	entry := object(object(a.review["consumers"])[id])
	own, err := a.client.netappRead(ctx, id, netappVolumeType)
	if err != nil {
		return nil, err
	}
	p := object(own.data["properties"])
	if !slices.Contains([]string{"Succeeded", "Updating"}, text(p["provisioningState"])) {
		return nil, serviceDenied("netapp_snapshot_policy_volume_not_ready")
	}
	if resourceRegion(own.data) != a.planned.Location || p["fileSystemId"] != entry["uid"] || protectedAzureTags(object(own.data["tags"])) || text(own.data["managedBy"]) != "" || p["isRestoring"] != nil && p["isRestoring"] != false || p["cloneProgress"] != nil && p["cloneProgress"] != json.Number("100") {
		return nil, serviceDenied("netapp_snapshot_policy_volume_context_changed")
	}
	assignments, err := netappAssignments(own.data)
	if err != nil {
		return nil, err
	}
	current := assignments[netappSnapshotPolicyType]
	if current != "" && current != a.planned.Identity.NativeID || detached && current != "" || a.client.privateConfiguration(netappDetachedSnapshotVolume(own.data)) != entry["detached_configuration"] {
		return nil, serviceDenied("netapp_snapshot_policy_assignment_changed")
	}
	pool, err := a.client.netappRead(ctx, redisParentID(id), netappPoolType)
	if err != nil {
		return nil, err
	}
	if a.client.privateConfiguration(netappPoolSnapshot(pool.data)) != entry["pool"] || object(pool.data["properties"])["provisioningState"] != "Succeeded" {
		return nil, serviceDenied("netapp_snapshot_policy_pool_changed")
	}
	locks, err := a.client.managementLocks(ctx)
	if err != nil {
		return nil, err
	}
	if locked(id, locks) {
		return nil, serviceDenied("netapp_snapshot_policy_volume_locked")
	}
	return own.data, nil
}
func (a *netappSnapshotPolicyAction) consumers(ctx context.Context, raw map[string]any) error {
	current, complete, err := a.client.netappAssignmentConsumers(ctx, a.planned.Identity.NativeID, netappSnapshotPolicyType, a.planned.Location, raw, object(a.review["consumers"]))
	if err != nil {
		return err
	}
	if !complete {
		return serviceDenied("netapp_snapshot_policy_consumers_incomplete")
	}
	for id, entry := range current {
		if object(a.review["consumers"])[id] == nil || object(entry)["uid"] != object(object(a.review["consumers"])[id])["uid"] {
			return serviceDenied("netapp_snapshot_policy_new_consumer")
		}
	}
	return nil
}
func (a *netappSnapshotPolicyAction) Preflight(ctx context.Context, req contracts.ActionRequest) (contracts.PreflightResult, error) {
	if err := a.identity(req); err != nil {
		return contracts.PreflightResult{}, err
	}
	raw, absent, err := a.root(ctx, false)
	if err != nil {
		return contracts.PreflightResult{}, err
	}
	if !absent {
		if err := a.consumers(ctx, raw); err != nil {
			return contracts.PreflightResult{}, err
		}
	}
	for id := range object(a.review["consumers"]) {
		v, err := a.volume(ctx, id, absent)
		if err != nil {
			return contracts.PreflightResult{}, err
		}
		if object(v["properties"])["provisioningState"] != "Succeeded" {
			return contracts.PreflightResult{}, serviceDenied("netapp_snapshot_policy_volume_not_ready")
		}
	}
	return contracts.PreflightResult{Allowed: true, Absent: absent}, nil
}
func (a *netappSnapshotPolicyAction) Execute(ctx context.Context, req contracts.ActionRequest) (contracts.ActionResult, error) {
	if err := a.identity(req); err != nil {
		return contracts.ActionResult{}, err
	}
	if req.ExecutionResult != nil {
		return *req.ExecutionResult, nil
	}
	if _, err := a.Preflight(ctx, req); err != nil {
		return contracts.ActionResult{}, err
	}
	data := map[string]any{"phase": "detach", "index": 0}
	data["binding"] = a.binding(req, data)
	return contracts.ActionResult{Data: data}, nil
}
func (a *netappSnapshotPolicyAction) Readback(ctx context.Context, req contracts.ActionRequest) (contracts.ReadbackResult, error) {
	if err := a.identity(req); err != nil {
		return contracts.ReadbackResult{}, err
	}
	_, absent, err := a.root(ctx, true)
	if err != nil {
		return contracts.ReadbackResult{}, err
	}
	if absent {
		for id := range object(a.review["consumers"]) {
			v, err := a.volume(ctx, id, true)
			if err != nil {
				return contracts.ReadbackResult{}, err
			}
			if object(v["properties"])["provisioningState"] != "Succeeded" {
				return contracts.ReadbackResult{}, serviceDenied("netapp_snapshot_policy_retained_volume_not_ready")
			}
		}
	}
	return contracts.ReadbackResult{Exists: !absent}, nil
}
func (a *netappSnapshotPolicyAction) Wait(ctx context.Context, req contracts.ActionRequest, result contracts.ActionResult) (contracts.WaitResult, error) {
	req.ExecutionResult = &result
	if err := a.identity(req); err != nil {
		return contracts.WaitResult{}, err
	}
	data := batchClone(result.Data)
	out := contracts.WaitResult{Data: data, RetryAfter: 2 * time.Second}
	save := func() { data["binding"] = a.binding(req, data) }
	ids := slices.Sorted(maps.Keys(object(a.review["consumers"])))
	index, err := batchInteger(data["index"], 32)
	if err != nil || index < 0 || index > int64(len(ids)) {
		return out, serviceDenied("invalid_netapp_snapshot_policy_phase")
	}
	if data["phase"] == "detach" && index < int64(len(ids)) {
		id := ids[index]
		entry := object(object(a.review["consumers"])[id])
		if receipt := object(data["operation"]); receipt != nil {
			poll, err := a.client.netappPollUpdate(ctx, id, a.planned.Location, text(entry["uid"]), receipt)
			if err != nil {
				if ctx.Err() != nil {
					return out, err
				}
				own, readErr := a.volume(ctx, id, true)
				if readErr != nil || object(own["properties"])["provisioningState"] != "Succeeded" {
					return out, err
				}
				data["operation_done"] = true
			} else {
				data["operation"] = poll.Data
				data["operation_done"] = poll.Done
			}
			if data["operation_done"] != true {
				save()
				return out, nil
			}
			own, err := a.volume(ctx, id, false)
			if err != nil {
				return out, err
			}
			assignments, _ := netappAssignments(own)
			if assignments[netappSnapshotPolicyType] != "" || object(own["properties"])["provisioningState"] != "Succeeded" {
				save()
				return out, nil
			}
			data["index"] = index + 1
			delete(data, "operation")
			delete(data, "operation_done")
			save()
			return out, nil
		}
		raw, absent, err := a.root(ctx, false)
		if err != nil {
			return out, err
		}
		if absent {
			return out, serviceDenied("netapp_snapshot_policy_removed_before_unassignment")
		}
		if err := a.consumers(ctx, raw); err != nil {
			return out, err
		}
		own, err := a.volume(ctx, id, false)
		if err != nil {
			return out, err
		}
		if object(own["properties"])["provisioningState"] != "Succeeded" {
			return out, serviceDenied("netapp_snapshot_policy_volume_not_ready")
		}
		assignments, _ := netappAssignments(own)
		if assignments[netappSnapshotPolicyType] == "" {
			data["index"] = index + 1
			save()
			return out, nil
		}
		body := map[string]any{"properties": map[string]any{"dataProtection": map[string]any{"snapshot": map[string]any{"snapshotPolicyId": ""}}}}
		wire, err := json.Marshal(body)
		if err != nil {
			return out, err
		}
		res, err := a.client.requestBody(ctx, "PATCH", apiURL(id, netappVersion), wire, map[string]string{"x-ms-client-request-id": azureRequestID(req.IdempotencyKey + ":detach:" + id)})
		if err != nil {
			return out, err
		}
		receipt, err := a.client.netappUpdateReceipt(id, a.planned.Location, text(entry["uid"]), res)
		if err != nil {
			return out, err
		}
		data["operation"] = receipt
		save()
		return out, nil
	}
	if data["phase"] == "detach" {
		raw, absent, err := a.root(ctx, false)
		if err != nil {
			return out, err
		}
		for _, id := range ids {
			own, err := a.volume(ctx, id, true)
			if err != nil {
				return out, err
			}
			if object(own["properties"])["provisioningState"] != "Succeeded" {
				return out, serviceDenied("netapp_snapshot_policy_retained_volume_not_ready")
			}
		}
		if !absent {
			current, complete, err := a.client.netappAssignmentConsumers(ctx, a.planned.Identity.NativeID, netappSnapshotPolicyType, a.planned.Location, raw, object(a.review["consumers"]))
			if err != nil {
				return out, err
			}
			if !complete || len(current) != 0 {
				return out, serviceDenied("netapp_snapshot_policy_still_assigned")
			}
			res, err := a.client.requestBody(ctx, "DELETE", apiURL(a.planned.Identity.NativeID, netappVersion), nil, map[string]string{"x-ms-client-request-id": azureRequestID(req.IdempotencyKey + ":delete:" + a.planned.Identity.NativeID)})
			if err != nil && !isNotFound(err) {
				return out, err
			}
			if err == nil {
				receipt, err := a.client.netappDeleteReceipt(a.planned.Identity.NativeID, a.planned.Location, res)
				if err != nil {
					return out, err
				}
				data["delete_operation"] = receipt
			}
		}
		data["phase"] = "delete"
		save()
		return out, nil
	}
	if data["phase"] != "delete" {
		return out, serviceDenied("invalid_netapp_snapshot_policy_phase")
	}
	if receipt := object(data["delete_operation"]); receipt != nil && data["delete_done"] != true {
		poll, err := a.client.netappPoll(ctx, a.planned.Identity.NativeID, a.planned.Location, receipt)
		if err != nil {
			read, ownErr := a.Readback(ctx, req)
			if ownErr != nil || read.Exists {
				return out, err
			}
			data["delete_done"] = true
		} else {
			data["delete_operation"] = poll.Data
			data["delete_done"] = poll.Done
		}
		save()
		return out, nil
	}
	read, err := a.Readback(ctx, req)
	out.Done = err == nil && !read.Exists
	return out, err
}
func (*netappSnapshotPolicyAction) DeletionCheckTimeout() time.Duration { return 24 * time.Hour }
