package gcp

import (
	"context"
	"encoding/hex"
	"net/url"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func (a *action) monitoringActionIdentity(request contracts.ActionRequest) error {
	if a.kind.NativeType == notificationChannelType && len(request.Parameters) != 0 {
		return groupDenied("notification_channel_parameters_unsupported")
	}
	proof, err := hex.DecodeString(text(request.Asset.Normalized[monitoringReviewKey(request.Asset.Identity.NativeType)]))
	if err != nil || len(proof) != 32 || request.Asset.ID == "" || request.Action != "delete" || request.Asset.Identity != a.identity || a.identity.Provider != asset.ProviderGCP || !gcpPartition(a.identity.Partition) || len(request.LifecycleImpacts) != 0 {
		return groupDenied("monitoring_action_review_changed")
	}
	return a.monitoringPrerequisites(request)
}

func (a *action) monitoringReadback(ctx context.Context, request contracts.ActionRequest) (contracts.ReadbackResult, error) {
	if err := a.monitoringActionIdentity(request); err != nil {
		return contracts.ReadbackResult{}, err
	}
	data, err := a.client.monitoringRead(ctx, a.kind.NativeType, a.identity.NativeID)
	if isNotFound(err) {
		return contracts.ReadbackResult{Exists: false}, nil
	}
	if err != nil {
		return contracts.ReadbackResult{}, contracts.DependencyReadError(err)
	}
	if monitoringConfiguration(a.kind.NativeType, a.identity.NativeID, data) != text(request.Asset.Normalized[monitoringReviewKey(request.Asset.Identity.NativeType)]) {
		return contracts.ReadbackResult{}, groupDenied("monitoring_configuration_changed")
	}
	if protectedComputeLabels(map[string]any{"labels": data["userLabels"]}) {
		return contracts.ReadbackResult{}, groupDenied("protected_labels")
	}
	if reason := protectionReason(a.kind.NativeType, data); reason != "" {
		return contracts.ReadbackResult{}, groupDenied(reason)
	}
	return contracts.ReadbackResult{Exists: true}, nil
}

func (a *action) monitoringPreflight(ctx context.Context, request contracts.ActionRequest) (contracts.PreflightResult, error) {
	read, err := a.monitoringReadback(ctx, request)
	if err == nil && read.Exists {
		err = a.monitoringIncoming(ctx, request)
	}
	return contracts.PreflightResult{Allowed: err == nil, Absent: err == nil && !read.Exists}, err
}

func uptimePhase(request contracts.ActionRequest) map[string]any {
	phase := "uptime_delete"
	if request.Asset.Identity.NativeType == notificationChannelType {
		phase = "notification_channel_delete"
	}
	if request.Asset.Identity.NativeType == alertPolicyType {
		phase = "alert_policy_delete"
	}
	review := map[string]any{"asset_id": request.Asset.ID, "identity": request.Asset.Identity, "configuration": request.Asset.Normalized[monitoringReviewKey(request.Asset.Identity.NativeType)], "action": request.Action, "idempotency_key": request.IdempotencyKey}
	if len(request.PrerequisiteDeletions) != 0 {
		review["prerequisites"] = request.PrerequisiteDeletions
	}
	return map[string]any{"phase": phase, "review": firewallDigest(review)}
}

func (a *action) executeMonitoring(ctx context.Context, request contracts.ActionRequest) (contracts.ActionResult, error) {
	// Execute's initial preflight and this final read both compare the frozen review.
	read, err := a.monitoringReadback(ctx, request)
	if err != nil {
		return contracts.ActionResult{}, err
	}
	if !read.Exists {
		return contracts.ActionResult{}, nil
	}
	if a.kind.NativeType == uptimeType || a.kind.NativeType == notificationChannelType {
		if err := a.monitoringIncoming(ctx, request); err != nil {
			return contracts.ActionResult{}, err
		}
		// Incoming reads can take many pages; recheck the target immediately before DELETE.
		read, err = a.monitoringReadback(ctx, request)
		if err != nil {
			return contracts.ActionResult{}, err
		}
		if !read.Exists {
			return contracts.ActionResult{}, nil
		}
	}
	var query url.Values
	if a.kind.NativeType == notificationChannelType {
		query = url.Values{"force": {"false"}}
	}
	response, err := a.client.requestResult(ctx, "DELETE", a.endpoint, query, nil)
	if isNotFound(err) {
		live, readErr := a.monitoringReadback(ctx, request)
		if readErr != nil {
			return contracts.ActionResult{}, readErr
		}
		if live.Exists {
			return contracts.ActionResult{}, contracts.DependencyReadError(err)
		}
	} else if err != nil {
		return contracts.ActionResult{}, err
	} else if len(response.Data) != 0 {
		return contracts.ActionResult{}, groupDenied("monitoring_delete_response_invalid")
	}
	return contracts.ActionResult{ProviderRequestID: response.RequestID, Data: uptimePhase(request), RetryAfter: 2 * time.Second}, nil
}

func (a *action) waitMonitoring(ctx context.Context, request contracts.ActionRequest, result contracts.ActionResult) (contracts.WaitResult, error) {
	if err := a.monitoringActionIdentity(request); err != nil {
		return contracts.WaitResult{}, err
	}
	if result.ProviderOperationID != "" || len(result.Data) != 0 && firewallDigest(result.Data) != firewallDigest(uptimePhase(request)) {
		return contracts.WaitResult{}, groupDenied("monitoring_delete_receipt_changed")
	}
	read, err := a.monitoringReadback(ctx, request)
	return contracts.WaitResult{Done: err == nil && !read.Exists, RetryAfter: 2 * time.Second}, err
}
