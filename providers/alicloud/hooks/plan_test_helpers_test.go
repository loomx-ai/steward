package hooks_test

import (
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/plan"
)

func requireCleanupStepForAsset(
	t *testing.T,
	steps []plan.CleanupTaskStep,
	id asset.AssetID,
) plan.CleanupTaskStep {
	t.Helper()
	for _, step := range steps {
		if step.AssetID == id {
			return step
		}
	}
	t.Fatalf("cleanup step for asset %q not found in %+v", id, steps)
	return plan.CleanupTaskStep{}
}

func cleanupStepForAssetIfPresent(
	steps []plan.CleanupTaskStep,
	id asset.AssetID,
) plan.CleanupTaskStep {
	for _, step := range steps {
		if step.AssetID == id {
			return step
		}
	}
	return plan.CleanupTaskStep{}
}
