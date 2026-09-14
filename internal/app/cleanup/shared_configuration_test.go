package cleanup

import (
	"context"
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
	"github.com/loomx-ai/steward/internal/provider/contracts"
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
	action := execution.ActionAttempt{ID: "other-action", ExecutionID: attempt.ID, CleanupTaskStepID: "other-step", AssetID: "other-nat", Action: "delete", Status: execution.ActionInvoking, IdempotencyKey: "other-action", CreatedAt: now, UpdatedAt: now}
	if err := repositories.Executions().AppendAction(ctx, action); err != nil {
		t.Fatal(err)
	}
	for _, status := range []execution.ExecutionStatus{execution.ExecutionRunning, execution.ExecutionWaiting, execution.ExecutionPaused, execution.ExecutionFailed, execution.ExecutionCanceled} {
		attempt.Status = status
		if err := repositories.Executions().UpdateExecution(ctx, attempt); err != nil {
			t.Fatal(err)
		}
		if err := guardSharedConfiguration(ctx, repositories, current, nil, ""); !errors.Is(err, persistence.ErrConflict) {
			t.Fatal("unresolved sibling update was not blocked", status, err)
		}
	}
	// A stale pre-lock task snapshot must not create a second overlapping run.
	if err := guardSharedConfiguration(ctx, repositories, other, nil, ""); !errors.Is(err, persistence.ErrConflict) {
		t.Fatal("same-task outstanding execution was ignored", err)
	}
	if err := guardSharedConfiguration(ctx, repositories, other, nil, attempt.ID); err != nil {
		t.Fatal("same task cannot recover", err)
	}
	current.Steps[0].Evidence[natMutationScope] = "connection/gcp/router-b"
	if err := guardSharedConfiguration(ctx, repositories, current, nil, ""); err != nil {
		t.Fatal("different router blocked", err)
	}
	current.Steps[0].Evidence[natMutationScope] = scope
	action.Status = execution.ActionSucceeded
	if err := repositories.Executions().UpdateAction(ctx, action); err != nil {
		t.Fatal(err)
	}
	// The completed action remains authoritative across a database/provider restart.
	repositories, err = sqlite.Open(db, "../../../migrations")
	if err != nil {
		t.Fatal(err)
	}
	if err := guardSharedConfiguration(ctx, repositories, current, nil, ""); err != nil {
		t.Fatal("verified success did not release router", err)
	}
}

func TestCloudNatTerminalScopeWithoutInvocation(t *testing.T) {
	for _, mode := range []string{"no-action", "intent", "pending-resume", "invoking", "receipt-in-intent", "running-job", "expired-lease", "paused-job", "pending-job", "finished-job", "active-execution"} {
		t.Run(mode, func(t *testing.T) {
			ctx := t.Context()
			repositories, err := sqlite.Open(filepath.Join(t.TempDir(), "nat.db"), "../../../migrations")
			if err != nil {
				t.Fatal(err)
			}
			now := time.Now().UTC()
			scope := "connection/gcp/router"
			task := persistence.CleanupTaskAggregate{Task: plan.CleanupTask{ID: "old", ConnectionID: "connection", Status: plan.StatusFailed, CreatedAt: now}, Steps: []plan.CleanupTaskStep{{ID: "step", CleanupTaskID: "old", AssetID: "nat", Action: "delete", Evidence: map[string]any{natMutationScope: scope}}}}
			if err := repositories.CleanupTasks().CreateTask(ctx, task.Task, task.Steps, nil); err != nil {
				t.Fatal(err)
			}
			attempt := execution.ExecutionAttempt{ID: "old-run", ConnectionID: "connection", CleanupTaskID: "old", Status: execution.ExecutionCanceled, IdempotencyKey: "old", CreatedAt: now}
			if mode == "active-execution" {
				attempt.Status = execution.ExecutionRunning
			}
			if err := repositories.Executions().CreateExecution(ctx, attempt); err != nil {
				t.Fatal(err)
			}
			if mode == "intent" || mode == "pending-resume" || mode == "invoking" || mode == "receipt-in-intent" {
				action := execution.ActionAttempt{ID: "action", ExecutionID: attempt.ID, CleanupTaskStepID: "step", AssetID: "nat", Status: execution.ActionIntentPersisted, IdempotencyKey: "action", CreatedAt: now, UpdatedAt: now}
				if mode == "pending-resume" {
					action.Status = execution.ActionPending
					action.ResumeStatus = execution.ActionInvoking
				}
				if mode == "invoking" {
					action.Status = execution.ActionInvoking
				}
				if mode == "receipt-in-intent" {
					action.ProviderOperationID = "native-op"
				}
				if err := repositories.Executions().AppendAction(ctx, action); err != nil {
					t.Fatal(err)
				}
			}
			statuses := map[string]execution.JobStatus{"running-job": execution.JobRunning, "expired-lease": execution.JobRunning, "paused-job": execution.JobPaused, "pending-job": execution.JobPending, "finished-job": execution.JobCanceled}
			if status, ok := statuses[mode]; ok {
				past := now.Add(-time.Minute)
				job := execution.Job{ID: "job", Type: execution.JobExecute, Status: status, AggregateType: "cleanup_task", AggregateID: "old", TargetKey: "nat", Payload: map[string]any{"execution_id": "old-run"}, IdempotencyKey: "job", CreatedAt: now, UpdatedAt: now, RunAt: now}
				if mode == "expired-lease" {
					job.LeaseOwner = "old-worker"
					job.LeaseUntil = &past
				}
				if err := repositories.Jobs().Enqueue(ctx, job); err != nil {
					t.Fatal(err)
				}
			}
			current := persistence.CleanupTaskAggregate{Task: plan.CleanupTask{ID: "new", ConnectionID: "connection"}, Steps: []plan.CleanupTaskStep{{Evidence: map[string]any{natMutationScope: scope}}}}
			err = guardSharedConfiguration(ctx, repositories, current, nil, "")
			safe := mode == "no-action" || mode == "intent" || mode == "finished-job"
			if safe && err != nil || !safe && !errors.Is(err, persistence.ErrConflict) {
				t.Fatal("incorrect scope release", mode, err)
			}
		})
	}
}

type mutationProofRegistry struct {
	contracts.ActionDriver // Any accidental action call panics; only settlement is allowed.
	calls                  int
}

func (r *mutationProofRegistry) ResolveAction(context.Context, asset.ConnectionID, asset.Asset) (contracts.ActionDriver, error) {
	return r, nil
}
func (r *mutationProofRegistry) MutationSettled(_ context.Context, request contracts.ActionRequest, _ contracts.ActionResult) (contracts.MutationSettlement, error) {
	r.calls++
	return contracts.MutationSettlement{Settled: request.Asset.ID == "nat-a", Operation: "native-operation"}, nil
}

func TestCloudNatSettlementProofPersistsAndInvalidatesOnActionChange(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "proof.db")
	repositories, err := sqlite.Open(path, "../../../migrations")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	other := persistence.CleanupTaskAggregate{Task: plan.CleanupTask{ID: "old", ConnectionID: "connection", Status: plan.StatusFailed, CreatedAt: now}}
	attempt := execution.ExecutionAttempt{ID: "old-run", ConnectionID: "connection", CleanupTaskID: "old", Status: execution.ExecutionFailed, IdempotencyKey: "old", CreatedAt: now}
	if err := repositories.Executions().CreateExecution(ctx, attempt); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"nat-a", "nat-b"} {
		value := asset.Asset{ID: asset.AssetID(id), Identity: asset.Identity{Provider: asset.ProviderGCP, Partition: "gcp", ConnectionID: "connection", NativeType: "compute.googleapis.com/RouterNat", NativeID: "//compute.googleapis.com/projects/project/regions/region/routers/" + id + "/nats/nat"}}
		if err := repositories.Inventory().PutAsset(ctx, value); err != nil {
			t.Fatal(err)
		}
		step := plan.CleanupTaskStep{ID: plan.StepID(id), CleanupTaskID: "old", AssetID: value.ID, Action: "delete", Evidence: map[string]any{natMutationScope: id, plan.EvidencePlannedAsset: value}}
		other.Steps = append(other.Steps, step)
		action := execution.ActionAttempt{ID: execution.ActionAttemptID(id), ExecutionID: attempt.ID, CleanupTaskStepID: id, AssetID: value.ID, Action: "delete", Status: execution.ActionWaiting, IdempotencyKey: id, ProviderOperationID: "native-operation", CreatedAt: now, UpdatedAt: now}
		if err := repositories.Executions().AppendAction(ctx, action); err != nil {
			t.Fatal(err)
		}
	}
	if err := repositories.CleanupTasks().CreateTask(ctx, other.Task, other.Steps, nil); err != nil {
		t.Fatal(err)
	}
	current := persistence.CleanupTaskAggregate{Task: plan.CleanupTask{ID: "new", ConnectionID: "connection"}, Steps: other.Steps}
	registry := &mutationProofRegistry{}
	var semanticErr error
	if err := repositories.WithTx(ctx, func(tx persistence.Repositories) error {
		semanticErr = guardSharedConfiguration(ctx, tx, current, registry, "")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(semanticErr, persistence.ErrConflict) || registry.calls != 2 {
		t.Fatal(semanticErr, registry.calls)
	}
	saved, err := repositories.Executions().GetAction(ctx, "nat-a")
	if err != nil || saved.MutationSettlement == nil || saved.Status != execution.ActionWaiting {
		t.Fatal(saved, err)
	}
	originalDigest := saved.MutationSettlement.Digest
	// A blocked second scope must not discard the first scope's terminal proof.
	repositories, err = sqlite.Open(path, "../../../migrations")
	if err != nil {
		t.Fatal(err)
	}
	current.Steps = current.Steps[:1]
	registry.calls = 0
	if err := repositories.WithTx(ctx, func(tx persistence.Repositories) error {
		return guardSharedConfiguration(ctx, tx, current, registry, "")
	}); err != nil || registry.calls != 0 {
		t.Fatal("persisted proof was not reused", err, registry.calls)
	}
	// Reusing the original action after a worker update requires a fresh check.
	saved.UpdatedAt = saved.UpdatedAt.Add(time.Second)
	if err := repositories.Executions().UpdateAction(ctx, saved); err != nil {
		t.Fatal(err)
	}
	if err := repositories.WithTx(ctx, func(tx persistence.Repositories) error {
		return guardSharedConfiguration(ctx, tx, current, registry, "")
	}); err != nil || registry.calls != 1 {
		t.Fatal("stale proof survived worker update", err, registry.calls)
	}
	refreshed, err := repositories.Executions().GetAction(ctx, "nat-a")
	if err != nil || refreshed.MutationSettlement.Digest == originalDigest || refreshed.Status != saved.Status || refreshed.UpdatedAt != saved.UpdatedAt {
		t.Fatal(refreshed, err)
	}
}
