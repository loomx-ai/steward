package azure

import (
	"context"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
	"maps"
	"net/http"
	"slices"
	"time"
)

type protectionVaultAction struct {
	client  *client
	planned asset.Asset
}

func newProtectionVaultAction(c *client, connection asset.ConnectionID, value asset.Asset) (*protectionVaultAction, error) {
	id, err := c.dataProtectionIdentity(value.Identity.NativeID, dataProtectionVault)
	if err != nil || id != value.Identity.NativeID || value.ID == "" || value.Identity.NativeType != dataProtectionVault || value.Identity.Provider != asset.ProviderAzure || value.Identity.Partition != "azure" || value.Identity.ConnectionID != connection {
		return nil, serviceDenied("invalid_backup_vault_action")
	}
	a := &protectionVaultAction{client: c, planned: value}
	return a, a.identity(contracts.ActionRequest{Asset: value, Action: "delete"})
}
func (a *protectionVaultAction) binding(req contracts.ActionRequest, result contracts.ActionResult) string {
	req.IdempotencyKey, req.ExecutionResult = "", nil
	data := maps.Clone(result.Data)
	delete(data, "binding")
	return a.client.privateConfiguration(map[string]any{"protocol": "data-protection-vault-delete-1", "request": req, "data": data, "operation": result.ProviderOperationID})
}
func (a *protectionVaultAction) identity(req contracts.ActionRequest) error {
	review := object(req.Asset.Normalized[dataProtectionVaultReview])
	if req.Action != "delete" || req.Asset.ID != a.planned.ID || req.Asset.Identity != a.planned.Identity || len(req.Parameters)+len(req.LifecycleImpacts)+len(req.PrerequisiteDeletions) != 0 || len(review) != 9 || review["region"] != req.Asset.Location || review["observed"] != req.Asset.Normalized["_data_protection_configuration"] || review["ready"] != true || review["protected"] != false || req.Asset.Normalized["cleanup_protected"] != false || req.Asset.Normalized[dataProtectionVaultProof] != a.planned.Normalized[dataProtectionVaultProof] || req.Asset.Normalized[dataProtectionVaultProof] != a.client.dataProtectionVaultProofFor(req.Asset.Identity.NativeID, req.Asset.Identity.ConnectionID, review) {
		return serviceDenied("backup_vault_review_changed")
	}
	if req.ExecutionResult != nil && req.ExecutionResult.Data["binding"] != a.binding(req, *req.ExecutionResult) {
		return serviceDenied("backup_vault_receipt_changed")
	}
	return nil
}
func (a *protectionVaultAction) Readback(ctx context.Context, req contracts.ActionRequest) (contracts.ReadbackResult, error) {
	if err := a.identity(req); err != nil {
		return contracts.ReadbackResult{}, err
	}
	id := a.planned.Identity.NativeID
	expected := object(req.Asset.Normalized[dataProtectionVaultReview])
	group, err := a.client.dataProtectionGroup(ctx, id)
	if err != nil {
		return contracts.ReadbackResult{}, err
	}
	if group["configuration"] != expected["group"] || group["protected"] != false {
		return contracts.ReadbackResult{}, serviceDenied("backup_vault_group_changed")
	}
	own, err := a.client.dataProtectionRead(ctx, id, dataProtectionVault)
	if err == nil {
		if a.client.privateConfiguration(hybridComputeChildSnapshot(own.data)) != expected["configuration"] || resourceRegion(own.data) != a.planned.Location {
			return contracts.ReadbackResult{}, serviceDenied("backup_vault_configuration_changed")
		}
		return contracts.ReadbackResult{Exists: true, State: text(object(own.data["properties"])["provisioningState"])}, nil
	}
	if !isNotFound(err) {
		return contracts.ReadbackResult{}, err
	}
	retained, err := a.client.dataProtectionRetainedVaults(ctx, id, a.planned.Location, object(expected["retained"]))
	if err != nil {
		return contracts.ReadbackResult{}, err
	}
	after, err := a.client.dataProtectionGroup(ctx, id)
	if err != nil {
		return contracts.ReadbackResult{}, err
	}
	if a.client.privateConfiguration(group) != a.client.privateConfiguration(after) {
		return contracts.ReadbackResult{}, serviceDenied("backup_vault_group_changed_during_read")
	}
	_, err = a.client.dataProtectionRead(ctx, id, dataProtectionVault)
	if !isNotFound(err) {
		if err == nil {
			err = serviceDenied("backup_vault_reappeared")
		}
		return contracts.ReadbackResult{}, err
	}
	ids := slices.Sorted(maps.Keys(retained))
	// These are historical records, not cascade-owned assets of the active vault.
	state := "active_absent"
	if len(ids) > 0 {
		state = "active_absent_with_retained_history"
	}
	return contracts.ReadbackResult{Exists: false, State: state, Data: map[string]any{"retained_vault_ids": ids, "permanent_purge_verified": false}}, nil
}
func (a *protectionVaultAction) Preflight(ctx context.Context, req contracts.ActionRequest) (contracts.PreflightResult, error) {
	read, err := a.Readback(ctx, req)
	if err != nil {
		return contracts.PreflightResult{}, err
	}
	if !read.Exists {
		return contracts.PreflightResult{Allowed: true, Absent: true}, nil
	}
	expected := object(req.Asset.Normalized[dataProtectionVaultReview])
	review, err := a.client.dataProtectionVaultReviewFor(ctx, a.planned.Identity.NativeID, expected)
	if err != nil {
		return contracts.PreflightResult{}, err
	}
	for _, key := range []string{"configuration", "group", "region"} {
		if review[key] != expected[key] {
			return contracts.PreflightResult{}, serviceDenied("backup_vault_context_changed")
		}
	}
	if review["ready"] != true || review["protected"] != false || a.client.privateConfiguration(object(review["guards"])) != a.client.privateConfiguration(object(expected["guards"])) {
		return contracts.PreflightResult{}, serviceDenied("backup_vault_not_deletable")
	}
	for id, value := range object(review["children"]) {
		if a.client.privateConfiguration(object(value)) != a.client.privateConfiguration(object(object(expected["children"])[id])) {
			return contracts.PreflightResult{}, serviceDenied("backup_vault_retained_child_changed")
		}
	}
	return contracts.PreflightResult{Allowed: true}, nil
}
func (a *protectionVaultAction) Execute(ctx context.Context, req contracts.ActionRequest) (contracts.ActionResult, error) {
	if err := a.identity(req); err != nil {
		return contracts.ActionResult{}, err
	}
	if req.ExecutionResult != nil {
		return *req.ExecutionResult, nil
	}
	pre, err := a.Preflight(ctx, req)
	if err != nil {
		return contracts.ActionResult{}, err
	}
	result := contracts.ActionResult{Data: map[string]any{"already_absent": pre.Absent}}
	if !pre.Absent {
		kind, _ := findType(dataProtectionVault)
		op, params, err := a.client.resourceOperation(kind, a.planned.Identity.NativeID, "DELETE")
		if err != nil {
			return result, err
		}
		bound, err := bindAzureREST(op, params)
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
		res, err := a.client.requestUsing(ctx, bound.Method, bound.URL, bound.Body, map[string]string{"x-ms-client-request-id": azureRequestID(req.IdempotencyKey + ":delete:" + a.planned.Identity.NativeID)}, a.client.validateURL, &transport, false)
		if err != nil {
			return result, err
		}
		receipt, err := a.client.dataProtectionDeleteReceipt(a.planned.Identity.NativeID, a.planned.Location, res, ack.empty)
		if err != nil {
			return result, err
		}
		result.Data["operation"] = receipt
		result.ProviderRequestID, result.RetryAfter = res.requestID, retryAfter(res.header)
	}
	result.Data["binding"] = a.binding(req, result)
	return result, nil
}
func (a *protectionVaultAction) Wait(ctx context.Context, req contracts.ActionRequest, result contracts.ActionResult) (contracts.WaitResult, error) {
	req.ExecutionResult = &result
	if err := a.identity(req); err != nil {
		return contracts.WaitResult{}, err
	}
	out := contracts.WaitResult{State: "Deleting", RetryAfter: 2 * time.Second, Data: batchClone(result.Data)}
	if result.Data["already_absent"] != true && result.Data["operation_done"] != true {
		poll, err := a.client.dataProtectionPoll(ctx, a.planned.Identity.NativeID, a.planned.Location, object(result.Data["operation"]))
		if err != nil {
			return out, err
		}
		out.Data["operation"], out.Data["operation_done"] = poll.Data, poll.Done
		if poll.RetryAfter > 0 {
			out.RetryAfter = poll.RetryAfter
		}
		updated := result
		updated.Data = out.Data
		out.Data["binding"] = a.binding(req, updated)
		return out, nil
	}
	read, err := a.Readback(ctx, req)
	out.Done, out.State = err == nil && !read.Exists, read.State
	if err == nil && !out.Done {
		out.State = "Deleting"
	}
	if out.Done {
		out.Data["retention"] = read.Data
		updated := result
		updated.Data = out.Data
		out.Data["binding"] = a.binding(req, updated)
	}
	return out, err
}
func (*protectionVaultAction) DeletionCheckTimeout() time.Duration { return 24 * time.Hour }
