package gcp

import (
	"context"
	"encoding/hex"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func (a *action) uptimeActionIdentity(request contracts.ActionRequest) error {
	proof, err := hex.DecodeString(text(request.Asset.Normalized[uptimeReview]))
	if err != nil || len(proof) != 32 || request.Asset.ID == "" || request.Action != "delete" || request.Asset.Identity != a.identity || a.identity.Provider != asset.ProviderGCP || !gcpPartition(a.identity.Partition) || len(request.LifecycleImpacts) != 0 || len(request.PrerequisiteDeletions) != 0 {
		return groupDenied("uptime_action_review_changed")
	}
	return nil
}

func (a *action) uptimeReadback(ctx context.Context, request contracts.ActionRequest) (contracts.ReadbackResult, error) {
	if err := a.uptimeActionIdentity(request); err != nil {
		return contracts.ReadbackResult{}, err
	}
	data, err := a.client.uptimeRead(ctx, a.identity.NativeID)
	if isNotFound(err) {
		return contracts.ReadbackResult{Exists: false}, nil
	}
	if err != nil {
		return contracts.ReadbackResult{}, contracts.DependencyReadError(err)
	}
	if uptimeConfiguration(a.identity.NativeID, data) != text(request.Asset.Normalized[uptimeReview]) {
		return contracts.ReadbackResult{}, groupDenied("uptime_configuration_changed")
	}
	if protectedComputeLabels(map[string]any{"labels": data["userLabels"]}) {
		return contracts.ReadbackResult{}, groupDenied("protected_labels")
	}
	if reason := protectionReason(uptimeType, data); reason != "" {
		return contracts.ReadbackResult{}, groupDenied(reason)
	}
	return contracts.ReadbackResult{Exists: true}, nil
}

func (a *action) uptimePreflight(ctx context.Context, request contracts.ActionRequest) (contracts.PreflightResult, error) {
	read, err := a.uptimeReadback(ctx, request)
	return contracts.PreflightResult{Allowed: err == nil, Absent: err == nil && !read.Exists}, err
}

func uptimePhase(request contracts.ActionRequest) map[string]any {
	return map[string]any{"phase": "uptime_delete", "review": firewallDigest(map[string]any{"asset_id": request.Asset.ID, "identity": request.Asset.Identity, "configuration": request.Asset.Normalized[uptimeReview], "action": request.Action, "idempotency_key": request.IdempotencyKey})}
}

func (a *action) executeUptime(ctx context.Context, request contracts.ActionRequest) (contracts.ActionResult, error) {
	// Execute's initial preflight and this final read both compare the frozen review.
	read, err := a.uptimeReadback(ctx, request)
	if err != nil {
		return contracts.ActionResult{}, err
	}
	if !read.Exists {
		return contracts.ActionResult{}, nil
	}
	response, err := a.client.requestResult(ctx, "DELETE", a.endpoint, nil, nil)
	if isNotFound(err) {
		live, readErr := a.uptimeReadback(ctx, request)
		if readErr != nil {
			return contracts.ActionResult{}, readErr
		}
		if live.Exists {
			return contracts.ActionResult{}, contracts.DependencyReadError(err)
		}
	} else if err != nil {
		return contracts.ActionResult{}, err
	} else if len(response.Data) != 0 {
		return contracts.ActionResult{}, groupDenied("uptime_delete_response_invalid")
	}
	return contracts.ActionResult{ProviderRequestID: response.RequestID, Data: uptimePhase(request), RetryAfter: 2 * time.Second}, nil
}

func (a *action) waitUptime(ctx context.Context, request contracts.ActionRequest, result contracts.ActionResult) (contracts.WaitResult, error) {
	if err := a.uptimeActionIdentity(request); err != nil {
		return contracts.WaitResult{}, err
	}
	if result.ProviderOperationID != "" || len(result.Data) != 0 && firewallDigest(result.Data) != firewallDigest(uptimePhase(request)) {
		return contracts.WaitResult{}, groupDenied("uptime_delete_receipt_changed")
	}
	read, err := a.uptimeReadback(ctx, request)
	return contracts.WaitResult{Done: err == nil && !read.Exists, RetryAfter: 2 * time.Second}, err
}
