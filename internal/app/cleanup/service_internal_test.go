package cleanup

import (
	"errors"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/core/graph"
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/internal/provider/contracts"
	"github.com/loomx-ai/steward/internal/provider/spec"
)

func TestRequestedExecutionConcurrencyDefaultsAndValidatesBounds(t *testing.T) {
	t.Parallel()

	if got, err := requestedExecutionConcurrency(nil); err != nil || got != 20 {
		t.Fatalf("default concurrency=%d err=%v, want 20", got, err)
	}
	for _, value := range []int{1, 20, 100} {
		value := value
		if got, err := requestedExecutionConcurrency(&value); err != nil || got != value {
			t.Fatalf("concurrency %d resolved to %d err=%v", value, got, err)
		}
	}
	for _, value := range []int{-1, 0, 101} {
		value := value
		if _, err := requestedExecutionConcurrency(&value); !errors.Is(err, ErrExecutionConcurrency) {
			t.Fatalf("concurrency %d error=%v, want ErrExecutionConcurrency", value, err)
		}
	}
}

func TestRetryableProviderSkipResumesFromInvoking(t *testing.T) {
	t.Parallel()

	action := execution.ActionAttempt{
		Status:            execution.ActionSkipped,
		SkipReason:        string(asset.SkipProductUnsupported),
		ProviderRequestID: "failed-prerequisite-request",
		ProviderError: &execution.ProviderError{
			Code: "UnsupportedHTTPMethod",
			Summary: map[string]any{
				"operation": "AlibabaCloud.NAS.DescribeLifecyclePolicies",
			},
		},
	}
	if status := actionResumeStatus(action); status != execution.ActionInvoking {
		t.Fatalf("retryable provider skip resume status = %s, want invoking", status)
	}
}

func TestAppendSelectionWarningsDescribesPublicImagePreparation(t *testing.T) {
	t.Parallel()

	image := asset.Asset{
		ID: "image-a",
		Identity: asset.Identity{
			NativeType: "ACS::ECS::Image",
		},
		Normalized: map[string]any{
			"configuration": map[string]any{"IsPublic": true},
		},
	}
	warnings := appendSelectionWarnings(nil, plan.Input{
		Assets: []asset.Asset{image},
	}, plan.Result{
		Steps: []plan.CleanupTaskStep{{AssetID: image.ID}},
	})
	if len(warnings) != 1 ||
		warnings[0].Code != plan.WarningPublicImageMadePrivate ||
		warnings[0].AssetID != image.ID ||
		warnings[0].Evidence["operation"] != "make_image_private" {
		t.Fatalf("warnings=%+v", warnings)
	}
}

func TestAppendSelectionWarningsDescribesScalingGroupForceDelete(t *testing.T) {
	t.Parallel()

	scalingGroup := asset.Asset{
		ID: "scaling-group-a",
		Identity: asset.Identity{
			NativeType: "ACS::ESS::ScalingGroup",
		},
		Normalized: map[string]any{
			"configuration": map[string]any{"TotalInstanceCount": float64(3)},
		},
	}
	warnings := appendSelectionWarnings(nil, plan.Input{
		Assets: []asset.Asset{scalingGroup},
	}, plan.Result{
		Steps: []plan.CleanupTaskStep{{AssetID: scalingGroup.ID}},
	})
	if len(warnings) != 1 ||
		warnings[0].Code != plan.WarningScalingGroupForceDelete ||
		warnings[0].Evidence["instance_count"] != float64(3) {
		t.Fatalf("warnings=%+v", warnings)
	}
}

func TestRefreshPlanningActionabilityPreservesServiceManagedSuppression(t *testing.T) {
	t.Parallel()

	bundles := map[asset.Provider]spec.Bundle{
		asset.ProviderAliCloud: {
			Provider: asset.ProviderAliCloud,
			Specs: []spec.CompiledSpec{{ResourceKind: asset.ResourceKind{
				Provider: asset.ProviderAliCloud, NativeType: "ACS::Test::Resource",
				Capabilities: asset.CapabilitySet{
					asset.CapabilityIndexed, asset.CapabilityActionable,
				},
			}}},
		},
	}
	values := []asset.Asset{
		{
			ID: "stale-scan-only",
			Identity: asset.Identity{
				Provider: asset.ProviderAliCloud, NativeType: "ACS::Test::Resource",
			},
			Capabilities: asset.CapabilitySet{asset.CapabilityIndexed},
		},
		{
			ID: "service-managed",
			Identity: asset.Identity{
				Provider: asset.ProviderAliCloud, NativeType: "ACS::Test::Resource",
			},
			Capabilities: asset.CapabilitySet{asset.CapabilityIndexed},
			Normalized:   map[string]any{"_service_managed": true},
		},
	}

	refreshed := refreshPlanningActionability(values, bundles)
	if !refreshed[0].Capabilities.Has(asset.CapabilityActionable) {
		t.Fatalf("stale capability was not refreshed: %+v", refreshed[0])
	}
	if refreshed[1].Capabilities.Has(asset.CapabilityActionable) {
		t.Fatalf("service-managed capability suppression was lost: %+v", refreshed[1])
	}
	if values[0].Capabilities.Has(asset.CapabilityActionable) {
		t.Fatalf("input asset was mutated: %+v", values[0])
	}
}

func TestVerifiedSafeProviderInvokeRetryCoversObservedCleanupTransients(t *testing.T) {
	t.Parallel()

	for _, test := range []execution.ProviderError{
		{Code: "SnapshotCreatedImage", Summary: map[string]any{"operation": "AlibabaCloud.DeleteSnapshot"}},
		{Code: "InternalServerError", Category: execution.ErrorRetryable, Summary: map[string]any{"operation": "AlibabaCloud.FC.DeleteService"}},
		{Category: execution.ErrorRetryable, Summary: map[string]any{"operation": "AlibabaCloud.ECI.DeleteContainerGroup"}},
		{Code: "TaskConflict", Summary: map[string]any{"operation": "AlibabaCloud.DeleteVSwitch"}},
		{Code: "InstanceInUse", Summary: map[string]any{"operation": "AlibabaCloud.ESS.DeleteScalingGroup"}},
		{Code: "InvalidOperation.InvalidEniState", Summary: map[string]any{"operation": "AlibabaCloud.DeleteNetworkInterface"}},
	} {
		if !verifiedSafeProviderInvokeRetry(test) {
			t.Fatalf("provider error was not classified for bounded retry: %+v", test)
		}
	}
}

func TestIncorrectVSwitchIDIsAbsentOnlyForDeleteVSwitch(t *testing.T) {
	t.Parallel()

	providerError := &contracts.ProviderCallError{Provider: execution.ProviderError{
		Category: execution.ErrorInvalidRequest,
		Code:     "IncorrectVSwitchId",
		Summary:  map[string]any{"operation": "AlibabaCloud.DeleteVSwitch"},
	}}
	if !isVerifiedAbsentProviderError(providerError) {
		t.Fatal("DeleteVSwitch IncorrectVSwitchId must be treated as absent")
	}

	providerError.Provider.Summary["operation"] = "AlibabaCloud.DescribeVSwitches"
	if isVerifiedAbsentProviderError(providerError) {
		t.Fatal("IncorrectVSwitchId must not be treated as absent for non-delete operations")
	}
}

func TestScheduledSafeProviderRetryIsNotSuppressedAsTerminal(t *testing.T) {
	t.Parallel()

	providerError := execution.ProviderError{
		Category: execution.ErrorDependencyViolation,
		Code:     "SnapshotCreatedImage",
		Summary:  map[string]any{"operation": "AlibabaCloud.DeleteSnapshot"},
	}
	action := execution.ActionAttempt{
		ProviderError: &providerError,
		Request: map[string]any{
			providerInvokeRetryCountRequestKey: float64(1),
		},
	}
	if terminalDeterministicProviderRejection(action) {
		t.Fatal("a scheduled bounded snapshot retry must be allowed to invoke")
	}

	action.Request[providerInvokeRetryCountRequestKey] = float64(maxVerifiedProviderInvokeRetries + 1)
	if !terminalDeterministicProviderRejection(action) {
		t.Fatal("a deterministic rejection beyond the retry bound must remain terminal")
	}
}

func TestCreatedFromDependencyIsBackfilledForLegacyCleanupStep(t *testing.T) {
	t.Parallel()

	image := plan.CleanupTaskStep{ID: "step-image", AssetID: "image"}
	snapshot := plan.CleanupTaskStep{ID: "step-snapshot", AssetID: "snapshot"}
	updated, added := appendCreatedFromRuntimeDependencies(
		snapshot,
		[]plan.CleanupTaskStep{snapshot, image},
		[]graph.Relationship{{
			SourceAssetID: "image",
			TargetAssetID: "snapshot",
			Type:          graph.RelationshipCreatedFrom,
		}},
	)
	if added != 1 || len(updated.DependsOn) != 1 || updated.DependsOn[0] != image.ID {
		t.Fatalf("updated legacy snapshot step=%+v added=%d", updated, added)
	}

	updated, added = appendCreatedFromRuntimeDependencies(
		updated,
		[]plan.CleanupTaskStep{updated, image},
		[]graph.Relationship{{
			SourceAssetID: "image",
			TargetAssetID: "snapshot",
			Type:          graph.RelationshipCreatedFrom,
		}},
	)
	if added != 0 || len(updated.DependsOn) != 1 {
		t.Fatalf("dependency backfill is not idempotent: %+v added=%d", updated, added)
	}
}

func TestCreatedFromDependenciesAreBackfilledWithoutChangingLegacyStepIDs(t *testing.T) {
	t.Parallel()

	steps := []plan.CleanupTaskStep{
		{ID: "step-snapshot", AssetID: "snapshot"},
		{ID: "step-image", AssetID: "image"},
	}
	updated, added := appendCreatedFromTaskDependencies(
		steps,
		[]graph.Relationship{{
			SourceAssetID: "image",
			TargetAssetID: "snapshot",
			Type:          graph.RelationshipCreatedFrom,
		}},
	)
	if added != 1 || updated[0].ID != "step-snapshot" ||
		len(updated[0].DependsOn) != 1 || updated[0].DependsOn[0] != "step-image" {
		t.Fatalf("updated legacy task steps=%+v added=%d", updated, added)
	}
	if len(steps[0].DependsOn) != 0 {
		t.Fatalf("input steps were mutated: %+v", steps)
	}
}

func TestCleanupStepsSettleWhenActiveActionIsBlockedByFailedDependency(t *testing.T) {
	t.Parallel()

	steps := []plan.CleanupTaskStep{
		{ID: "step-image", AssetID: "image"},
		{ID: "step-snapshot", AssetID: "snapshot", DependsOn: []plan.StepID{"step-image"}},
	}
	settled, failed := cleanupStepsSettled(
		steps,
		map[string]execution.ActionAttempt{
			"step-image":    {Status: execution.ActionFailed},
			"step-snapshot": {Status: execution.ActionInvoking},
		},
	)
	if !settled || !failed {
		t.Fatalf("settled=%t failed=%t, want blocked execution to settle as failed", settled, failed)
	}
}

func TestAcceptedProviderRequestResumesFromWaiting(t *testing.T) {
	t.Parallel()

	action := execution.ActionAttempt{ProviderRequestID: "accepted-delete-request"}
	if status := actionResumeStatus(action); status != execution.ActionWaiting {
		t.Fatalf("accepted provider request resume status = %s, want waiting", status)
	}
}

func TestFailedROSStackInstanceOperationResumesFromInvoking(t *testing.T) {
	t.Parallel()

	action := execution.ActionAttempt{
		Status:         execution.ActionFailed,
		FailedFrom:     execution.ActionWaiting,
		IdempotencyKey: "action-stack-group",
		ProviderResult: map[string]any{
			"phase": "delete_stack_instances", "operation_id": "operation-failed",
		},
		ProviderError: &execution.ProviderError{Code: "FAILED"},
	}
	if status := actionResumeStatus(action); status != execution.ActionInvoking {
		t.Fatalf("failed ROS stack instance resume status = %s, want invoking", status)
	}
	action.Status = execution.ActionInvoking
	key := resumedProviderIdempotencyKey(execution.ExecutionAttempt{ContinueCount: 2}, action)
	if key != "action-stack-group:continue:2" {
		t.Fatalf("resumed provider idempotency key = %q", key)
	}
}

func TestRetryableDNSLookupSkipResumesFromWaiting(t *testing.T) {
	t.Parallel()

	action := execution.ActionAttempt{
		Status:            execution.ActionSkipped,
		SkipReason:        string(asset.SkipProviderRegionUnavailable),
		ProviderRequestID: "accepted-delete-request",
		ProviderError: &execution.ProviderError{
			Message: "dial tcp: lookup example.invalid: no such host",
		},
	}
	if status := actionResumeStatus(action); status != execution.ActionWaiting {
		t.Fatalf("retryable DNS lookup skip resume status = %s, want waiting", status)
	}
}

func TestDeletionTimeoutInSteadyStateRetriesDelete(t *testing.T) {
	t.Parallel()

	action := execution.ActionAttempt{
		Status:     execution.ActionFailed,
		FailedFrom: execution.ActionWaiting,
		ProviderError: &execution.ProviderError{
			Code:    "DeletionCheckTimeout",
			Summary: map[string]any{"last_state": "Running"},
		},
	}
	if status := actionResumeStatus(action); status != execution.ActionInvoking {
		t.Fatalf("steady-state timeout resume status = %s, want invoking", status)
	}
}

func TestDeletionTimeoutInProgressKeepsWaiting(t *testing.T) {
	t.Parallel()

	action := execution.ActionAttempt{
		Status:     execution.ActionFailed,
		FailedFrom: execution.ActionWaiting,
		ProviderError: &execution.ProviderError{
			Code:    "DeletionCheckTimeout",
			Summary: map[string]any{"last_state": "Deleting"},
		},
	}
	if status := actionResumeStatus(action); status != execution.ActionWaiting {
		t.Fatalf("in-progress timeout resume status = %s, want waiting", status)
	}
}
