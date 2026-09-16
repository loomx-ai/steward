package azure

import (
	"context"
	"maps"
	"net/http"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

type recoveryContainerAction struct {
	client  *client
	planned asset.Asset
}

func newRecoveryContainerAction(c *client, connection asset.ConnectionID, value asset.Asset) (*recoveryContainerAction, error) {
	id, err := c.recoveryServicesIdentity(value.Identity.NativeID, recoveryServicesContainer)
	if err != nil || id != value.Identity.NativeID || value.ID == "" || value.Identity.NativeType != recoveryServicesContainer || value.Identity.Provider != asset.ProviderAzure || value.Identity.Partition != "azure" || value.Identity.ConnectionID != connection {
		return nil, serviceDenied("invalid_recovery_container_action")
	}
	a := &recoveryContainerAction{client: c, planned: value}
	if err = a.identity(contracts.ActionRequest{Asset: value, Action: "delete"}); err != nil {
		return nil, err
	}
	return a, nil
}

func (a *recoveryContainerAction) binding(req contracts.ActionRequest, result contracts.ActionResult) string {
	req.IdempotencyKey, req.ExecutionResult = "", nil
	data := maps.Clone(result.Data)
	delete(data, "binding")
	return a.client.privateConfiguration(map[string]any{"protocol": "recovery-container-unregister-1", "request": req, "data": data, "operation": result.ProviderOperationID})
}

func (a *recoveryContainerAction) identity(req contracts.ActionRequest) error {
	review := object(req.Asset.Normalized[recoveryContainerReview])
	if req.Action != "delete" || req.Asset.ID != a.planned.ID || req.Asset.Identity != a.planned.Identity || len(req.Parameters)+len(req.LifecycleImpacts)+len(req.PrerequisiteDeletions) != 0 || len(review) != 10 || review["region"] != req.Asset.Location || review["observed"] != req.Asset.Normalized["_recovery_services_configuration"] || review["parent_observed"] != req.Asset.Normalized["_recovery_services_parent_configuration"] || review["ready"] != true || review["state"] != "Registered" || review["protected"] != false || len(object(review["consumers"])) != 0 || req.Asset.Normalized["cleanup_protected"] != false || req.Asset.Normalized[recoveryContainerProof] != a.planned.Normalized[recoveryContainerProof] || req.Asset.Normalized[recoveryContainerProof] != a.client.recoveryContainerProofFor(req.Asset.Identity.NativeID, req.Asset.Identity.ConnectionID, review) {
		return serviceDenied("recovery_container_review_changed")
	}
	if req.ExecutionResult != nil && req.ExecutionResult.Data["binding"] != a.binding(req, *req.ExecutionResult) {
		return serviceDenied("recovery_container_execution_receipt_changed")
	}
	return nil
}

func (a *recoveryContainerAction) current(ctx context.Context, req contracts.ActionRequest, requireRegistered bool) (bool, error) {
	if err := a.identity(req); err != nil {
		return false, err
	}
	expected := object(req.Asset.Normalized[recoveryContainerReview])
	review, absent, err := a.client.recoveryContainerReviewFor(ctx, a.planned.Identity.NativeID, object(expected["consumers"]))
	if err != nil {
		return false, err
	}
	for _, key := range []string{"vault", "group", "region"} {
		if review[key] != expected[key] {
			return false, serviceDenied("recovery_container_parent_changed")
		}
	}
	if review["ready"] != true || review["protected"] != false || len(object(review["consumers"])) != 0 {
		return false, serviceDenied("recovery_container_not_empty_or_protected")
	}
	if !absent && (review["configuration"] != expected["configuration"] || requireRegistered && review["state"] != "Registered") {
		return false, serviceDenied("recovery_container_configuration_changed")
	}
	return absent, nil
}

func (a *recoveryContainerAction) Preflight(ctx context.Context, req contracts.ActionRequest) (contracts.PreflightResult, error) {
	absent, err := a.current(ctx, req, true)
	if err != nil {
		return contracts.PreflightResult{}, err
	}
	return contracts.PreflightResult{Allowed: true, Absent: absent}, nil
}

func (a *recoveryContainerAction) Execute(ctx context.Context, req contracts.ActionRequest) (contracts.ActionResult, error) {
	if err := a.identity(req); err != nil {
		return contracts.ActionResult{}, err
	}
	if req.ExecutionResult != nil {
		return *req.ExecutionResult, nil
	}
	absent, err := a.current(ctx, req, true)
	if err != nil {
		return contracts.ActionResult{}, err
	}
	id := a.planned.Identity.NativeID
	result := contracts.ActionResult{Data: map[string]any{"accepted_status": 0, "operation": a.client.recoveryContainerReceipt(id, map[string]any{"operation_done": true})}}
	if !absent {
		kind, ok := findType(recoveryServicesContainer)
		if !ok {
			return result, serviceDenied("missing_recovery_container_kind")
		}
		op, params, err := a.client.resourceOperation(kind, id, "DELETE")
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
		res, err := a.client.requestUsing(ctx, bound.Method, bound.URL, bound.Body, map[string]string{"x-ms-client-request-id": azureRequestID(req.IdempotencyKey + ":unregister:" + id)}, a.client.validateURL, &transport, false)
		if err != nil {
			return result, err
		}
		receipt, err := a.client.recoveryContainerDeleteReceipt(id, res, ack.empty)
		if err != nil {
			return result, err
		}
		result.Data["operation"], result.Data["accepted_status"] = receipt, res.status
		result.ProviderRequestID, result.RetryAfter = res.requestID, retryAfter(res.header)
		if endpoint := text(receipt["poll_url"]); endpoint != "" {
			result.ProviderOperationID, _, _ = a.client.recoveryContainerCallback(id, endpoint, "result")
		}
	}
	result.Data["binding"] = a.binding(req, result)
	return result, nil
}

func (a *recoveryContainerAction) Readback(ctx context.Context, req contracts.ActionRequest) (contracts.ReadbackResult, error) {
	if err := a.identity(req); err != nil {
		return contracts.ReadbackResult{}, err
	}
	if req.ExecutionResult != nil && object(req.ExecutionResult.Data["operation"])["operation_done"] != true {
		return contracts.ReadbackResult{Exists: true, State: "Unregistering"}, nil
	}
	absent, err := a.current(ctx, req, false)
	if err != nil {
		return contracts.ReadbackResult{}, err
	}
	state := "Unregistering"
	if absent {
		state = "unregistered"
	}
	return contracts.ReadbackResult{Exists: !absent, State: state, Data: map[string]any{"backup_data_purge_performed": false}}, nil
}

func (a *recoveryContainerAction) Wait(ctx context.Context, req contracts.ActionRequest, result contracts.ActionResult) (contracts.WaitResult, error) {
	req.ExecutionResult = &result
	if err := a.identity(req); err != nil {
		return contracts.WaitResult{}, err
	}
	operation, delay, err := a.client.recoveryContainerPoll(ctx, a.planned.Identity.NativeID, object(result.Data["operation"]))
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
	wait := contracts.WaitResult{RetryAfter: delay, State: "Unregistering", Data: updated.Data}
	if operation["operation_done"] != true {
		return wait, nil
	}
	req.ExecutionResult = &updated
	read, err := a.Readback(ctx, req)
	if err != nil {
		return contracts.WaitResult{}, err
	}
	wait.Done, wait.State = !read.Exists, read.State
	return wait, nil
}
func (*recoveryContainerAction) DeletionCheckTimeout() time.Duration { return 24 * time.Hour }
