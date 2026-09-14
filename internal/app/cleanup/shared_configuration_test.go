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

func TestCloudNatPlanSerializesOnlySharedRouter(t *testing.T) {
	input := plan.Input{CleanupTaskID: "nat-plan", Revision: plan.RevisionBinding{InventoryRevision: "i", GraphRevision: "g", SpecBundleRevision: "b"}}
	for _, id := range []string{"a", "b", "c"} {
		router := "one"
		if id == "c" {
			router = "two"
		}
		value := asset.Asset{ID: asset.AssetID(id), Identity: asset.Identity{Provider: asset.ProviderGCP, Partition: "gcp", ConnectionID: "connection", NativeType: "compute.googleapis.com/RouterNat", NativeID: "//compute.googleapis.com/projects/project/regions/region/routers/" + router + "/nats/" + id}, Capabilities: asset.CapabilitySet{asset.CapabilityIndexed, asset.CapabilityActionable}}
		input.Assets = append(input.Assets, value)
		input.ResolvedAssetIDs = append(input.ResolvedAssetIDs, value.ID)
	}
	result, err := solveCleanupPlan(input)
	if err != nil || len(result.Steps) != 3 {
		t.Fatal(result, err)
	}
	byID := map[asset.AssetID]plan.CleanupTaskStep{}
	for _, step := range result.Steps {
		byID[step.AssetID] = step
	}
	if !slices.Contains(byID["b"].DependsOn, byID["a"].ID) || len(byID["c"].DependsOn) != 0 {
		t.Fatal(result.Steps)
	}
}

func TestCloudNatConcurrentExecutionsKeepScopeUntilVerifiedSuccess(t *testing.T) {
	ctx := t.Context()
	db := filepath.Join(t.TempDir(), "nat-scope.db")
	repositories, err := sqlite.Open(db, "../../../migrations")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	scope := "connection/gcp/router-a"
	other := persistence.CleanupTaskAggregate{Task: plan.CleanupTask{ID: "other", ConnectionID: "connection", Status: plan.StatusExecuting, CreatedAt: now, UpdatedAt: &now}, Steps: []plan.CleanupTaskStep{{ID: "other-step", CleanupTaskID: "other", AssetID: "other-nat", Kind: plan.StepDirect, Action: "delete", Evidence: map[string]any{natMutationScope: scope}}}}
	if err := repositories.CleanupTasks().CreateTask(ctx, other.Task, other.Steps, nil); err != nil {
		t.Fatal(err)
	}
	attempt := execution.ExecutionAttempt{ID: "other-execution", ConnectionID: "connection", CleanupTaskID: "other", Status: execution.ExecutionRunning, IdempotencyKey: "other", CreatedAt: now}
	if err := repositories.Executions().CreateExecution(ctx, attempt); err != nil {
		t.Fatal(err)
	}
	current := persistence.CleanupTaskAggregate{Task: plan.CleanupTask{ID: "current", ConnectionID: "connection"}, Steps: []plan.CleanupTaskStep{{ID: "current-step", Evidence: map[string]any{natMutationScope: scope}}}}
	for _, status := range []execution.ExecutionStatus{execution.ExecutionRunning, execution.ExecutionWaiting, execution.ExecutionPaused, execution.ExecutionFailed, execution.ExecutionCanceled} {
		attempt.Status = status
		if err := repositories.Executions().UpdateExecution(ctx, attempt); err != nil {
			t.Fatal(err)
		}
		if err := guardSharedConfiguration(ctx, repositories, current); !errors.Is(err, persistence.ErrConflict) {
			t.Fatal("unresolved sibling update was not blocked", status, err)
		}
	}
	if err := guardSharedConfiguration(ctx, repositories, other); err != nil {
		t.Fatal("same task cannot recover", err)
	}
	current.Steps[0].Evidence[natMutationScope] = "connection/gcp/router-b"
	if err := guardSharedConfiguration(ctx, repositories, current); err != nil {
		t.Fatal("different router blocked", err)
	}
	current.Steps[0].Evidence[natMutationScope] = scope
	action := execution.ActionAttempt{ID: "other-action", ExecutionID: attempt.ID, CleanupTaskStepID: "other-step", AssetID: "other-nat", Action: "delete", Status: execution.ActionSucceeded, IdempotencyKey: "other-action", CreatedAt: now, UpdatedAt: now}
	if err := repositories.Executions().AppendAction(ctx, action); err != nil {
		t.Fatal(err)
	}
	// The completed action remains authoritative across a database/provider restart.
	repositories, err = sqlite.Open(db, "../../../migrations")
	if err != nil {
		t.Fatal(err)
	}
	if err := guardSharedConfiguration(ctx, repositories, current); err != nil {
		t.Fatal("verified success did not release router", err)
	}
}
