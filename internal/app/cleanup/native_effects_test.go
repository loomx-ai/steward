package cleanup_test

import (
	"context"
	"testing"
	"time"

	"github.com/loomx-ai/steward/internal/app/cleanup"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/core/graph"
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func TestNativeDeletionEffectsSurvivePlanningPersistenceAndExecution(t *testing.T) {
	ctx := context.Background()
	repositories := openPlanningRepositories(t)
	now := time.Date(2026, 9, 15, 1, 0, 0, 0, time.UTC)
	assets := []asset.Asset{planningAsset("controller", "c-1", "ACS::ROS::Stack", "stack-native", now), planningAsset("remove", "c-1", "ACS::ECS::Instance", "remove-native", now), planningAsset("keep", "c-1", "ACS::ECS::Instance", "keep-native", now)}
	bindings := []graph.LifecycleBinding{}
	for _, child := range assets[1:] {
		b := planningBinding(graph.LifecycleBindingID("effect-"+string(child.ID)), "controller", child.ID, graph.OwnershipShared, graph.CleanupDelegate, "graph-native", now)
		b.Evidence[graph.LifecycleEvidenceNativeDeleteEffect] = true
		b.Evidence[graph.LifecycleEvidenceControllerDeleteGuaranteed] = true
		b.Evidence[graph.LifecycleEvidenceControllerVerifiesManagedAbsence] = true
		b.Evidence["retention_supported"] = true
		bindings = append(bindings, b)
	}
	seedPlanningSnapshot(t, repositories, "scope-a", "graph-native", assets, bindings)
	planner := cleanup.NewService(repositories, bundleResolver{asset.ProviderAliCloud: {Provider: asset.ProviderAliCloud, Revision: "bundle-a", Hash: "spec-a"}}, cleanup.WithClock(func() time.Time { return now }), cleanup.WithTaskIDGenerator(func() string { return "cln-native" }), cleanup.WithExecutionIDGenerator(func() string { return "execution-native" }))
	task, err := planner.CreateTask(ctx, cleanup.CreateTaskRequest{Selectors: []plan.CleanupSelector{assetSelector("controller")}, CreatedBy: "operator", RequestOptions: map[asset.AssetID]map[string]any{"controller": {"retain_resources": []string{"keep"}}}})
	if err != nil || task.Task.Status != plan.StatusReady || len(task.Steps) != 1 || len(task.ImpactItems) != 2 {
		t.Fatal(task, err)
	}
	created, err := planner.CreateExecution(ctx, cleanup.CreateExecutionRequest{CleanupTaskID: task.Task.ID, RequestedBy: "operator", IdempotencyKey: "native-request", Confirmation: cleanup.ExecutionConfirmation{Acknowledged: true}})
	if err != nil {
		t.Fatal(err)
	}
	driver := &scriptedActionDriver{readback: contracts.ReadbackResult{Exists: false}}
	handler := cleanup.NewExecutionHandler(planner, cleanup.ActionResolverFunc(func(_ context.Context, value asset.Asset) (cleanup.ActionDriver, error) {
		if value.ID != "controller" {
			t.Fatalf("unexpected independent child action: %s", value.ID)
		}
		return driver, nil
	}))
	if err := handler.Handle(ctx, cleanupExecutionJobForAsset(t, repositories, task.Task.ID, "controller")); err != nil {
		t.Fatal(err)
	}
	if len(driver.executeRequests) != 1 || len(driver.executeRequests[0].LifecycleImpacts) != 2 {
		t.Fatal(driver.executeRequests)
	}
	for _, impact := range driver.executeRequests[0].LifecycleImpacts {
		if impact.ControllerID != "controller" || impact.Delete != (impact.Asset.ID == "remove") {
			t.Fatal(impact)
		}
	}
	stored, err := repositories.CleanupTasks().GetTask(ctx, task.Task.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, impact := range stored.ImpactItems {
		expected := plan.ImpactDeletedByController
		if impact.AssetID == "keep" {
			expected = plan.ImpactRetainedByPolicy
		}
		if impact.Ownership != graph.OwnershipShared || impact.Result != expected {
			t.Fatal(impact)
		}
		value, err := repositories.Inventory().GetAsset(ctx, impact.AssetID)
		if err != nil || (value.ClosedAt != nil) != (impact.AssetID == "remove") {
			t.Fatal(value, err)
		}
	}
	finished, err := repositories.Executions().GetExecution(ctx, created.ID)
	if err != nil || finished.Status != execution.ExecutionSucceeded {
		t.Fatal(finished, err)
	}
}
