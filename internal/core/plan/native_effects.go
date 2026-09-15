package plan

import (
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
)

// Activate native operation effects only below selected controllers. The input
// bindings remain unchanged and are still included in the plan's snapshot.
func nativeExecutionBindings(bindings []graph.LifecycleBinding, selected map[asset.AssetID]struct{}, assets map[asset.AssetID]asset.Asset, options map[asset.AssetID]map[string]any) []graph.LifecycleBinding {
	hasEffects := false
	for _, b := range bindings {
		hasEffects = hasEffects || b.Evidence[graph.LifecycleEvidenceNativeDeleteEffect] == true
	}
	if !hasEffects {
		return bindings
	}
	active := map[asset.AssetID]bool{}
	children := map[asset.AssetID][]graph.LifecycleBinding{}
	queue := []asset.AssetID{}
	for id := range selected {
		active[id] = true
		queue = append(queue, id)
	}
	for _, b := range bindings {
		children[b.ControllerAssetID] = append(children[b.ControllerAssetID], b)
	}
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		for _, b := range children[id] {
			if active[b.ManagedAssetID] || !(b.Ownership == graph.OwnershipExclusive && b.CleanupPolicy == graph.CleanupDelegate || b.Evidence[graph.LifecycleEvidenceNativeDeleteEffect] == true) {
				continue
			}
			active[b.ManagedAssetID] = true
			queue = append(queue, b.ManagedAssetID)
		}
	}
	// An explicitly requested native child deletion can be independent of a
	// retained parent. Keep that direct operation edge instead of routing the
	// deletion through a controller which the user chose to retain.
	independent := map[asset.AssetID]map[asset.AssetID]bool{}
	for _, effect := range bindings {
		if !active[effect.ControllerAssetID] || !graph.NativeDeleteEffect(effect) || explicitlyRetained(assets[effect.ManagedAssetID], effect, options[effect.ControllerAssetID]) {
			continue
		}
		requested := false
		items, _ := options[effect.ControllerAssetID]["delete_options"].([]any)
		for _, item := range items {
			choice, _ := item.(map[string]any)
			kind, _ := effect.Evidence["resource_type"].(string)
			requested = requested || kind != "" && choice["resource_type"] == kind && choice["delete_mode"] == "delete"
		}
		if !requested {
			continue
		}
		for _, parent := range children[effect.ControllerAssetID] {
			if !graph.NativeDeleteEffect(parent) || !explicitlyRetained(assets[parent.ManagedAssetID], parent, options[effect.ControllerAssetID]) {
				continue
			}
			if independent[effect.ManagedAssetID] == nil {
				independent[effect.ManagedAssetID] = map[asset.AssetID]bool{}
			}
			independent[effect.ManagedAssetID][parent.ManagedAssetID] = true
		}
	}
	parents := map[asset.AssetID][]graph.LifecycleBinding{}
	candidates := []graph.LifecycleBinding{}
	for _, b := range bindings {
		if independent[b.ManagedAssetID][b.ControllerAssetID] && b.Ownership == graph.OwnershipExclusive && b.CleanupPolicy == graph.CleanupDelegate && b.Authority == graph.AuthorityAuthoritative && b.Confidence >= graph.ExecutableConfidence && b.Confidence <= 1 {
			continue
		}
		if b.Evidence[graph.LifecycleEvidenceNativeDeleteEffect] == true && !active[b.ControllerAssetID] {
			continue
		}
		candidates = append(candidates, b)
		if graph.NativeDeleteEffect(b) || b.Ownership == graph.OwnershipExclusive && b.CleanupPolicy == graph.CleanupDelegate && b.Authority == graph.AuthorityAuthoritative && b.Confidence >= graph.ExecutableConfidence && b.Confidence <= 1 {
			parents[b.ManagedAssetID] = append(parents[b.ManagedAssetID], b)
		}
	}
	reaches := func(from, to, excluded asset.AssetID) bool {
		seen := map[asset.AssetID]bool{excluded: true}
		todo := []asset.AssetID{from}
		for len(todo) > 0 {
			current := todo[len(todo)-1]
			todo = todo[:len(todo)-1]
			if seen[current] {
				continue
			}
			seen[current] = true
			if current == to {
				return true
			}
			for _, b := range parents[current] {
				todo = append(todo, b.ControllerAssetID)
			}
		}
		return false
	}
	result := make([]graph.LifecycleBinding, 0, len(candidates))
	for _, b := range candidates {
		redundant := false
		if graph.NativeDeleteEffect(b) {
			for _, other := range parents[b.ManagedAssetID] {
				if other.ControllerAssetID == b.ControllerAssetID {
					continue
				}
				// A flat native member may already have a nearer product controller.
				// Never collapse an ambiguous/cyclic controller relationship.
				if reaches(other.ControllerAssetID, b.ControllerAssetID, b.ManagedAssetID) && !reaches(b.ControllerAssetID, other.ControllerAssetID, b.ManagedAssetID) {
					redundant = true
					break
				}
			}
		}
		if !redundant {
			result = append(result, b)
		}
	}
	return result
}
