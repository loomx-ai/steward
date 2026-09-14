package plan

import (
	"fmt"

	"github.com/loomx-ai/steward/internal/core/asset"
)

// OrderSteps preserves persisted step IDs while validating and ordering their DAG.
func OrderSteps(values []CleanupTaskStep) ([]CleanupTaskStep, error) {
	steps := map[asset.AssetID]CleanupTaskStep{}
	ids := map[StepID]asset.AssetID{}
	for _, step := range values {
		if step.ID == "" || step.AssetID == "" {
			return nil, fmt.Errorf("cleanup step identity is missing")
		}
		if _, exists := ids[step.ID]; exists {
			return nil, fmt.Errorf("duplicate cleanup step %q", step.ID)
		}
		if _, exists := steps[step.AssetID]; exists {
			return nil, fmt.Errorf("duplicate cleanup asset %q", step.AssetID)
		}
		ids[step.ID], steps[step.AssetID] = step.AssetID, step
	}
	dependencies := map[asset.AssetID]map[asset.AssetID]struct{}{}
	for _, step := range values {
		dependencies[step.AssetID] = map[asset.AssetID]struct{}{}
		for _, id := range step.DependsOn {
			dependency, exists := ids[id]
			if !exists {
				return nil, fmt.Errorf("cleanup dependency %q is missing", id)
			}
			dependencies[step.AssetID][dependency] = struct{}{}
		}
	}
	ordered, ok := topologicalSteps(steps, dependencies)
	if !ok {
		return nil, fmt.Errorf("cleanup dependencies contain a cycle")
	}
	return ordered, nil
}
