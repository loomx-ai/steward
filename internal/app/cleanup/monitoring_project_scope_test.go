package cleanup

import (
	"errors"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/internal/persistence"
	"github.com/loomx-ai/steward/internal/persistence/sqlite"
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

func TestMonitoringLegacyCollectionReservationBlocksOtherKinds(t *testing.T) {
	for _, collection := range []string{"alertPolicies", "notificationChannels", "groups", "dashboards", "uptimeCheckConfigs"} {
		t.Run(collection, func(t *testing.T) {
			ctx := t.Context()
			repos, err := sqlite.Open(filepath.Join(t.TempDir(), "legacy.db"), "../../../migrations")
			if err != nil {
				t.Fatal(err)
			}
			now := time.Now().UTC()
			old := persistence.CleanupTaskAggregate{Task: plan.CleanupTask{ID: "old", ConnectionID: "connection", Status: plan.StatusExecuting, CreatedAt: now}, Steps: []plan.CleanupTaskStep{{ID: "old-step", AssetID: "old-asset", Action: "delete", Evidence: map[string]any{routerMutationScope: "connection/google-cloud///monitoring.googleapis.com/projects/sample-project/" + collection}}}}
			if err := repos.CleanupTasks().CreateTask(ctx, old.Task, old.Steps, nil); err != nil {
				t.Fatal(err)
			}
			attempt := execution.ExecutionAttempt{ID: "old-attempt", ConnectionID: "connection", CleanupTaskID: "old", Status: execution.ExecutionFailed, CreatedAt: now}
			if err := repos.Executions().CreateExecution(ctx, attempt); err != nil {
				t.Fatal(err)
			}
			action := execution.ActionAttempt{ID: "old-action", ExecutionID: attempt.ID, CleanupTaskStepID: "old-step", AssetID: "old-asset", Action: "delete", Status: execution.ActionInvoking, CreatedAt: now, UpdatedAt: now}
			if err := repos.Executions().AppendAction(ctx, action); err != nil {
				t.Fatal(err)
			}
			for _, project := range []string{"sample-project", "other-project"} {
				value := monitoringConfigurationAsset("new-uptime", project, false, false, false, false, true)
				if collection == "uptimeCheckConfigs" {
					value = monitoringConfigurationAsset("new-dashboard", project, false, false, false, true)
				}
				current := persistence.CleanupTaskAggregate{Task: plan.CleanupTask{ID: "new", ConnectionID: "connection"}, Steps: []plan.CleanupTaskStep{{ID: "new-step", AssetID: value.ID, Action: "delete", Evidence: map[string]any{plan.EvidencePlannedAsset: value}}}}
				err := guardSharedConfiguration(ctx, repos, current, nil, "")
				if project == "sample-project" {
					if !errors.Is(err, persistence.ErrConflict) {
						t.Fatal("legacy scope released", collection, err)
					}
				} else if err != nil {
					t.Fatal("different project blocked", err)
				}
			}
		})
	}
}
