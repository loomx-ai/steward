package azure

import (
	"context"
	"slices"
	"strings"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

// Only order the independent service prerequisites found by fresh native
// closure. An empty order says nothing about remaining preparation, group
// closure, ordinary product lifecycles or the native Stack operation.
func (r *Runtime) deploymentStackOrderPrerequisites(ctx context.Context, req contracts.ActionRequest, progress deploymentStackProgress) ([]contracts.ActionImpact, error) {
	closure, err := r.deploymentStackObserveServiceClosureWithProgress(ctx, req, progress)
	if err != nil {
		return nil, err
	}
	return deploymentStackPrerequisiteOrder(closure)
}

func deploymentStackPrerequisiteOrder(closure deploymentStackServiceClosure) ([]contracts.ActionImpact, error) {
	candidates := map[asset.AssetID]contracts.ActionImpact{}
	for _, impact := range closure.DirectChildren {
		if impact.Asset.ID == "" || !impact.Delete {
			return nil, serviceDenied("invalid_deployment_stack_prerequisite_order")
		}
		if _, exists := candidates[impact.Asset.ID]; exists {
			return nil, serviceDenied("duplicate_deployment_stack_prerequisite")
		}
		candidates[impact.Asset.ID] = impact
	}
	// Every dependency must refer to a verified independent child, even if its
	// parent itself will be deleted by another native lifecycle.
	parents := map[asset.AssetID]bool{}
	for _, id := range closure.Parents {
		parents[id] = true
	}
	referenced := map[asset.AssetID]bool{}
	for parent, children := range closure.Prerequisites {
		if !parents[parent] {
			return nil, serviceDenied("unverified_deployment_stack_prerequisite_parent")
		}
		seen := map[asset.AssetID]bool{}
		for _, child := range children {
			if _, exists := candidates[child]; !exists || seen[child] {
				return nil, serviceDenied("unverified_deployment_stack_prerequisite_child")
			}
			seen[child] = true
			referenced[child] = true
		}
	}
	for id := range candidates {
		if !referenced[id] {
			return nil, serviceDenied("unverified_deployment_stack_prerequisite_child")
		}
	}
	// Kahn's algorithm, with a sorted ready set, preserves shared prerequisite
	// edges and emits each resource once. No ARM-prefix ownership is inferred.
	pending := map[asset.AssetID]int{}
	dependents := map[asset.AssetID][]asset.AssetID{}
	for id := range candidates {
		pending[id] = 0
	}
	for parent, children := range closure.Prerequisites {
		if _, independent := candidates[parent]; !independent {
			continue
		}
		for _, child := range children {
			pending[parent]++
			dependents[child] = append(dependents[child], parent)
		}
	}
	ready := []asset.AssetID{}
	for id, count := range pending {
		if count == 0 {
			ready = append(ready, id)
		}
	}
	ordered := make([]contracts.ActionImpact, 0, len(candidates))
	for len(ready) > 0 {
		slices.SortFunc(ready, func(a, b asset.AssetID) int { return strings.Compare(string(a), string(b)) })
		id := ready[0]
		ready = ready[1:]
		ordered = append(ordered, candidates[id])
		for _, parent := range dependents[id] {
			pending[parent]--
			if pending[parent] == 0 {
				ready = append(ready, parent)
			}
		}
	}
	if len(ordered) != len(candidates) {
		return nil, serviceDenied("deployment_stack_prerequisite_cycle")
	}
	return ordered, nil
}
