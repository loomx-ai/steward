package azure

import (
	"context"
	"strings"

	"github.com/loomx-ai/steward/internal/provider/contracts"
)

// Capacity can fall after an explicitly selected node step. Restore only the
// exact reviewed decrement for configuration comparison, after native GETs
// prove the nodes and all their physical resources absent.
func batchCapacityDecrement(original, current any, removed int64) bool {
	before, first := batchInteger(original, 64)
	after, second := batchInteger(current, 64)
	return first == nil && second == nil && after >= 0 && before >= removed && before-removed == after
}

func (a *batchAction) restorePoolCapacity(ctx context.Context, request contracts.ActionRequest, account batchAccountContext, live map[string]any) (map[string]any, error) {
	if a.kind.NativeType != batchPoolType {
		return live, nil
	}
	removed := map[string]int64{"targetDedicatedNodes": 0, "targetLowPriorityNodes": 0}
	count := 0
	for _, prerequisite := range request.PrerequisiteDeletions {
		if prerequisite.Asset.Identity.NativeType != batchNodeType {
			continue
		}
		_, _, _, params, err := batchDataIdentity(prerequisite.Asset.Identity.NativeID)
		dedicated, valid := prerequisite.Asset.Normalized["isDedicated"].(bool)
		if err != nil || !valid || account.id+"/pools/"+strings.ToLower(text(params["poolId"])) != a.id {
			return nil, serviceDenied("invalid_batch_capacity_prerequisite")
		}
		key := "targetLowPriorityNodes"
		if dedicated {
			key = "targetDedicatedNodes"
		}
		removed[key]++
		count++
	}
	if count == 0 {
		return live, nil
	}
	if err := a.prerequisitesAbsent(ctx, request, account); err != nil {
		return nil, err
	}
	copy := batchClone(live)
	original := object(object(request.Asset.Normalized["scaleSettings"])["fixedScale"])
	current := object(object(object(copy["properties"])["scaleSettings"])["fixedScale"])
	for key, n := range removed {
		if original[key] == nil && current[key] == nil && n == 0 {
			continue
		}
		if !batchCapacityDecrement(original[key], current[key], n) {
			return nil, serviceDenied("batch_pool_capacity_changed")
		}
		current[key] = original[key]
	}
	return copy, nil
}

// serviceCascadePreflight has already verified every prerequisite's absence.
// A changed scale-set ETag is acceptable only when this exact native capacity
// decrement is the sole configuration difference.
func batchScaleSetConfigurationMatches(request contracts.ActionRequest, live map[string]any) bool {
	if request.Asset.Identity.NativeType != scaleSetType {
		return false
	}
	var removed int64
	for _, prerequisite := range request.PrerequisiteDeletions {
		if prerequisite.Asset.Identity.NativeType == batchNodeType {
			removed++
		}
	}
	before, after := object(request.Asset.Normalized["sku"])["capacity"], object(live["sku"])["capacity"]
	if removed == 0 || !batchCapacityDecrement(before, after, removed) {
		return false
	}
	copy := batchClone(live)
	object(copy["sku"])["capacity"] = before
	return serviceParentConfigurationMatches(request.Asset, copy)
}
