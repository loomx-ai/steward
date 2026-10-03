package cleanup

import (
	"slices"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/plan"
)

func TestMonitoringMixedKindsSerializeOneProject(t *testing.T) {
	input := plan.Input{CleanupTaskID: "mixed-monitoring"}
	values := []asset.Asset{
		monitoringConfigurationAsset("policy", "sample-project", false),
		monitoringConfigurationAsset("channel", "sample-project", true),
		monitoringConfigurationAsset("group", "sample-project", false, false, true),
		monitoringConfigurationAsset("dashboard", "sample-project", false, false, false, true),
		monitoringConfigurationAsset("uptime", "sample-project", false, false, false, false, true),
	}
	for _, value := range values {
		input.Assets = append(input.Assets, value)
		input.ResolvedAssetIDs = append(input.ResolvedAssetIDs, value.ID)
	}
	result, err := solveCleanupPlan(input)
	if err != nil || len(result.Steps) != 5 {
		t.Fatal(result, err)
	}
	ordered, err := plan.OrderSteps(result.Steps)
	if err != nil {
		t.Fatal(err)
	}
	for i, step := range ordered {
		if step.Evidence[routerMutationScope] != "connection/gcp///monitoring.googleapis.com/projects/sample-project" {
			t.Fatal(step)
		}
		if i > 0 && !slices.Contains(step.DependsOn, ordered[i-1].ID) {
			t.Fatal("mixed writes not serialized", ordered)
		}
	}
}
