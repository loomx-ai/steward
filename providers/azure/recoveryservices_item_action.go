package azure

import (
	"context"
	"maps"
	"net/http"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

type recoveryItemAction struct {
	client  *client
	planned asset.Asset
}

func newRecoveryItemAction(c *client, connection asset.ConnectionID, value asset.Asset) (*recoveryItemAction, error) {
	id, err := c.recoveryServicesIdentity(value.Identity.NativeID, recoveryServicesItem)
	if err != nil || id != value.Identity.NativeID || value.ID == "" || value.Identity.NativeType != recoveryServicesItem || value.Identity.Provider != asset.ProviderAzure || value.Identity.Partition != "azure" || value.Identity.ConnectionID != connection {
		return nil, serviceDenied("invalid_recovery_item_action")
	}
	a := &recoveryItemAction{client: c, planned: value}
	if err = a.identity(contracts.ActionRequest{Asset: value, Action: "delete"}); err != nil {
		return nil, err
	}
	return a, nil
}

func (a *recoveryItemAction) binding(req contracts.ActionRequest, result contracts.ActionResult) string {
	req.IdempotencyKey, req.ExecutionResult = "", nil
	data := maps.Clone(result.Data)
	delete(data, "binding")
	return a.client.privateConfiguration(map[string]any{"protocol": "recovery-item-delete-1", "request": req, "data": data, "operation": result.ProviderOperationID})
}

func (a *recoveryItemAction) identity(req contracts.ActionRequest) error {
	review := object(req.Asset.Normalized[recoveryItemReviewKey])
	if req.Action != "delete" || req.Asset.ID != a.planned.ID || req.Asset.Identity != a.planned.Identity || len(req.Parameters)+len(req.LifecycleImpacts) != 0 || review["region"] != req.Asset.Location || review["observed"] != req.Asset.Normalized["_recovery_services_configuration"] || review["parent_observed"] != req.Asset.Normalized["_recovery_services_parent_configuration"] || review["container_observed"] != req.Asset.Normalized["_recovery_services_container_configuration"] || review["ready"] != true || review["protected"] != false || review["retained"] != false || req.Asset.Normalized["retained"] != false || req.Asset.Normalized["cleanup_protected"] != false || req.Asset.Normalized[recoveryItemProofKey] != a.planned.Normalized[recoveryItemProofKey] || req.Asset.Normalized[recoveryItemProofKey] != a.client.recoveryItemProof(req.Asset.Identity.NativeID, req.Asset.Identity.ConnectionID, review) {
		return serviceDenied("recovery_item_review_changed")
	}
	if req.ExecutionResult != nil && req.ExecutionResult.Data["binding"] != a.binding(req, *req.ExecutionResult) {
		return serviceDenied("recovery_item_execution_receipt_changed")
	}
	return nil
}

func (a *recoveryItemAction) prerequisites(req contracts.ActionRequest) error {
	expected := object(object(a.planned.Normalized[recoveryItemReviewKey])["prerequisites"])
	if len(req.PrerequisiteDeletions) != len(expected) {
		return serviceDenied("recovery_item_prerequisite_selection_changed")
	}
	seen := map[string]bool{}
	for _, impact := range req.PrerequisiteDeletions {
		value := impact.Asset
		id := value.Identity.NativeID
		member := object(expected[id])
		if seen[id] || member == nil || impact.ControllerID != a.planned.ID || !impact.Delete || value.ID == "" || value.Identity.Provider != a.planned.Identity.Provider || value.Identity.Partition != a.planned.Identity.Partition || value.Identity.ConnectionID != a.planned.Identity.ConnectionID || value.Identity.NativeType != recoveryServicesItem || value.Normalized["_recovery_services_configuration"] != member["observed"] {
			return serviceDenied("recovery_item_prerequisite_changed")
		}
		seen[id] = true
	}
	return nil
}

func (a *recoveryItemAction) current(ctx context.Context, req contracts.ActionRequest) (contracts.ReadbackResult, error) {
	if err := a.identity(req); err != nil {
		return contracts.ReadbackResult{}, err
	}
	if err := a.prerequisites(req); err != nil {
		return contracts.ReadbackResult{}, err
	}
	expected := object(a.planned.Normalized[recoveryItemReviewKey])
	review, raw, err := a.client.recoveryItemReview(ctx, a.planned.Identity.NativeID, expected)
	if err != nil {
		return contracts.ReadbackResult{}, err
	}
	for _, key := range []string{"vault", "group", "region", "container", "reference_policy", "policy_configuration", "immutability"} {
		if review[key] != expected[key] {
			return contracts.ReadbackResult{}, serviceDenied("recovery_item_dependency_changed")
		}
	}
	if a.client.privateConfiguration(object(review["guards"])) != a.client.privateConfiguration(object(expected["guards"])) || review["ready"] != true || review["protected"] != false {
		return contracts.ReadbackResult{}, serviceDenied("recovery_item_protection_changed")
	}
	if len(object(review["prerequisites"])) != 0 {
		return contracts.ReadbackResult{}, serviceDenied("recovery_hana_instance_protection_still_active")
	}
	// A selected snapshot may transition to stopped/retained, but it may not be
	// replaced with different authored configuration while deleting its database.
	for id, value := range object(review["snapshots"]) {
		before := object(object(expected["snapshots"])[id])
		if before != nil && object(value)["configuration"] != before["configuration"] {
			return contracts.ReadbackResult{}, serviceDenied("recovery_hana_snapshot_configuration_changed")
		}
	}
	if raw == nil {
		return contracts.ReadbackResult{State: "absent", Data: map[string]any{"outcome": "absent", "backup_data_purge_performed": false}}, nil
	}
	retained, err := a.client.recoveryItemRetainedOutcome(expected, raw)
	if err != nil {
		return contracts.ReadbackResult{}, err
	}
	if retained {
		return contracts.ReadbackResult{State: "soft_deleted", Data: map[string]any{"outcome": "soft_deleted", "retained_native_id": a.planned.Identity.NativeID, "backup_data_purge_performed": false}}, nil
	}
	return contracts.ReadbackResult{Exists: true, State: text(review["state"])}, nil
}

func (a *recoveryItemAction) Preflight(ctx context.Context, req contracts.ActionRequest) (contracts.PreflightResult, error) {
	read, err := a.current(ctx, req)
	if err != nil {
		return contracts.PreflightResult{}, err
	}
	return contracts.PreflightResult{Allowed: true, Absent: !read.Exists}, nil
}

func (a *recoveryItemAction) Execute(ctx context.Context, req contracts.ActionRequest) (contracts.ActionResult, error) {
	if err := a.identity(req); err != nil {
		return contracts.ActionResult{}, err
	}
	if req.ExecutionResult != nil {
		return *req.ExecutionResult, nil
	}
	read, err := a.current(ctx, req)
	if err != nil {
		return contracts.ActionResult{}, err
	}
	id := a.planned.Identity.NativeID
	result := contracts.ActionResult{Data: map[string]any{"accepted_status": 0, "operation": a.client.recoveryItemReceipt(id, map[string]any{"operation_done": true, "status_done": true})}}
	if read.Exists {
		kind, ok := findType(recoveryServicesItem)
		if !ok {
			return result, serviceDenied("missing_recovery_item_kind")
		}
		op, parameters, err := a.client.resourceOperation(kind, id, "DELETE")
		if err != nil {
			return result, err
		}
		bound, err := bindAzureREST(op, parameters)
		if err != nil {
			return result, err
		}
		transport := *a.client.http
		base := transport.Transport
		if base == nil {
			base = http.DefaultTransport
		}
		ack := &synapseRestoreAckTransport{base: base}
		transport.Transport = ack
		res, err := a.client.requestUsing(ctx, bound.Method, bound.URL, bound.Body, map[string]string{"x-ms-client-request-id": azureRequestID(req.IdempotencyKey + ":delete-backup-item:" + id)}, a.client.validateURL, &transport, false)
		if err != nil {
			return result, err
		}
		receipt, err := a.client.recoveryItemDeleteReceipt(id, res, ack.empty)
		if err != nil {
			return result, err
		}
		result.Data["operation"], result.Data["accepted_status"] = receipt, res.status
		result.ProviderRequestID, result.RetryAfter = res.requestID, retryAfter(res.header)
		role, endpoint := "status", text(receipt["status_url"])
		if endpoint == "" {
			role, endpoint = "result", text(receipt["result_url"])
		}
		if endpoint != "" {
			result.ProviderOperationID, _, _ = a.client.recoveryBackupCallback(id, recoveryServicesItem, endpoint, role)
		}
	} else {
		result.Data["outcome"] = read.Data
	}
	result.Data["binding"] = a.binding(req, result)
	return result, nil
}

func (a *recoveryItemAction) Readback(ctx context.Context, req contracts.ActionRequest) (contracts.ReadbackResult, error) {
	if err := a.identity(req); err != nil {
		return contracts.ReadbackResult{}, err
	}
	if req.ExecutionResult != nil && object(req.ExecutionResult.Data["operation"])["operation_done"] != true {
		return contracts.ReadbackResult{Exists: true, State: "Deleting"}, nil
	}
	return a.current(ctx, req)
}

func (a *recoveryItemAction) Wait(ctx context.Context, req contracts.ActionRequest, result contracts.ActionResult) (contracts.WaitResult, error) {
	req.ExecutionResult = &result
	if err := a.identity(req); err != nil {
		return contracts.WaitResult{}, err
	}
	operation, delay, err := a.client.recoveryItemPoll(ctx, a.planned.Identity.NativeID, object(result.Data["operation"]))
	if err != nil {
		return contracts.WaitResult{}, err
	}
	updated := result
	updated.Data = maps.Clone(result.Data)
	updated.Data["operation"] = operation
	updated.Data["binding"] = a.binding(req, updated)
	if delay <= 0 {
		delay = 2 * time.Second
	}
	wait := contracts.WaitResult{RetryAfter: delay, State: "Deleting", Data: updated.Data}
	if operation["operation_done"] != true {
		return wait, nil
	}
	req.ExecutionResult = &updated
	read, err := a.Readback(ctx, req)
	if err != nil {
		return contracts.WaitResult{}, err
	}
	updated.Data["outcome"] = read.Data
	updated.Data["binding"] = a.binding(req, updated)
	wait.Done, wait.State, wait.Data = !read.Exists, read.State, updated.Data
	return wait, nil
}
func (*recoveryItemAction) DeletionCheckTimeout() time.Duration { return 24 * time.Hour }
