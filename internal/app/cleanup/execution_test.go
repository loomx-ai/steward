package cleanup_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/loomx-ai/steward/internal/app/cleanup"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/core/graph"
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/internal/core/requestmeta"
	"github.com/loomx-ai/steward/internal/persistence"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func TestCreateExecutionIsIdempotentAndPersistsJobs(t *testing.T) {
	ctx := context.Background()
	repositories, planner, _, now := directExecutionFixture(t, "execution-repeat")
	request := cleanup.CreateExecutionRequest{CleanupTaskID: "cln-direct", RequestedBy: "operator", IdempotencyKey: "request-key", Confirmation: cleanup.ExecutionConfirmation{Acknowledged: true}}

	first, err := planner.CreateExecution(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	second, err := planner.CreateExecution(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID || first.ID != "execution-repeat" || first.Status != execution.ExecutionPending ||
		first.Concurrency != cleanup.DefaultExecutionConcurrency {
		t.Fatalf("first=%+v second=%+v", first, second)
	}
	job, err := repositories.Jobs().ClaimNext(ctx, "worker", now, time.Minute)
	if err != nil || job.Payload["execution_id"] != "execution-repeat" || job.Payload["cleanup_task_step_id"] == "" {
		t.Fatalf("job=%+v err=%v", job, err)
	}
	if _, err := repositories.Jobs().ClaimNext(ctx, "worker", now, time.Minute); !errors.Is(err, persistence.ErrNotFound) {
		t.Fatalf("duplicate execution enqueued another job: %v", err)
	}
	audits, err := repositories.Audits().ListAuditEvents(ctx, persistence.ListOptions{Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	var planRequirement, executionConfirmation map[string]any
	for _, event := range audits.Items {
		switch event.Action {
		case "cleanup.task.create":
			planRequirement, _ = event.Evidence["confirmation_requirement"].(map[string]any)
		case "cleanup.execution.create":
			executionConfirmation, _ = event.Evidence["confirmation"].(map[string]any)
		}
	}
	if fmt.Sprint(planRequirement["methods"]) != "[acknowledged]" || planRequirement["acknowledgement_required"] != true {
		t.Fatalf("plan confirmation requirement audit = %#v", planRequirement)
	}
	if fmt.Sprint(executionConfirmation["methods"]) != "[acknowledged]" || executionConfirmation["acknowledged"] != true {
		t.Fatalf("execution confirmation audit = %#v", executionConfirmation)
	}
}

func TestCleanupExecutionPauseAndResumePendingJobs(t *testing.T) {
	ctx := context.Background()
	repositories, planner, created, now := directExecutionFixture(t, "execution-pause-pending")

	paused, err := planner.PauseExecution(ctx, cleanup.PauseExecutionRequest{
		CleanupTaskID: "cln-direct", RequestedBy: "operator",
	})
	if err != nil || paused.ID != created.ID || paused.Status != execution.ExecutionPaused {
		t.Fatalf("paused=%+v err=%v", paused, err)
	}
	aggregate, err := repositories.CleanupTasks().GetTask(ctx, "cln-direct")
	if err != nil || aggregate.Task.Status != plan.StatusPaused {
		t.Fatalf("paused task=%+v err=%v", aggregate.Task, err)
	}
	jobs, err := repositories.Jobs().ListJobsByAggregate(ctx, "cleanup_task", "cln-direct")
	if err != nil || len(jobs) != 1 || jobs[0].Status != execution.JobPaused {
		t.Fatalf("paused jobs=%+v err=%v", jobs, err)
	}
	if _, err := repositories.Jobs().ClaimNext(ctx, "worker", now, time.Minute); !errors.Is(err, persistence.ErrNotFound) {
		t.Fatalf("paused job was claimable: %v", err)
	}

	resumed, err := planner.ResumeExecution(ctx, cleanup.ResumeExecutionRequest{
		CleanupTaskID: "cln-direct", RequestedBy: "operator",
	})
	if err != nil || resumed.Status != execution.ExecutionPending {
		t.Fatalf("resumed=%+v err=%v", resumed, err)
	}
	aggregate, err = repositories.CleanupTasks().GetTask(ctx, "cln-direct")
	if err != nil || aggregate.Task.Status != plan.StatusExecuting {
		t.Fatalf("resumed task=%+v err=%v", aggregate.Task, err)
	}
	job, err := repositories.Jobs().ClaimNext(ctx, "worker", now, time.Minute)
	if err != nil || job.Status != execution.JobRunning || job.Payload["execution_id"] != string(created.ID) {
		t.Fatalf("resumed job=%+v err=%v", job, err)
	}
}

func TestCleanupExecutionPausesAtProviderCheckpointAndConverges(t *testing.T) {
	ctx := context.Background()
	repositories, planner, created, now := directExecutionFixture(t, "execution-pause-running")
	driver := &statusChangingDriver{}
	var pauseErr error
	driver.preflight = func() {
		driver.preflight = nil
		_, pauseErr = planner.PauseExecution(ctx, cleanup.PauseExecutionRequest{
			CleanupTaskID: "cln-direct", RequestedBy: "operator",
		})
	}
	handler := cleanup.NewExecutionHandler(
		planner,
		cleanup.ActionResolverFunc(func(context.Context, asset.Asset) (cleanup.ActionDriver, error) {
			return driver, nil
		}),
	)
	worker := cleanup.NewWorker(
		repositories.Jobs(),
		map[execution.JobType]cleanup.Handler{execution.JobExecute: handler},
		cleanup.WorkerOptions{
			WorkerID: "pause-worker", LeaseDuration: time.Minute, RenewInterval: time.Hour,
			Now: func() time.Time { return now },
		},
	)

	processed, err := worker.ProcessOne(ctx)
	if err != nil || !processed || pauseErr != nil {
		t.Fatalf("processed=%v err=%v pauseErr=%v", processed, err, pauseErr)
	}
	if driver.execCalls != 0 {
		t.Fatalf("provider delete calls=%d, want 0 after pause", driver.execCalls)
	}
	stored, err := repositories.Executions().GetExecution(ctx, created.ID)
	if err != nil || stored.Status != execution.ExecutionPaused {
		t.Fatalf("paused execution=%+v err=%v", stored, err)
	}
	aggregate, err := repositories.CleanupTasks().GetTask(ctx, "cln-direct")
	if err != nil || aggregate.Task.Status != plan.StatusPaused {
		t.Fatalf("paused task=%+v err=%v", aggregate.Task, err)
	}

	if _, err := planner.ResumeExecution(ctx, cleanup.ResumeExecutionRequest{
		CleanupTaskID: "cln-direct", RequestedBy: "operator",
	}); err != nil {
		t.Fatal(err)
	}
	processed, err = worker.ProcessOne(ctx)
	if err != nil || !processed || driver.execCalls != 1 {
		t.Fatalf("resumed processed=%v executeCalls=%d err=%v", processed, driver.execCalls, err)
	}
	stored, err = repositories.Executions().GetExecution(ctx, created.ID)
	if err != nil || stored.Status != execution.ExecutionSucceeded {
		t.Fatalf("completed execution=%+v err=%v", stored, err)
	}
}

func TestCleanupExecutionReconcilesPauseResumeSettlementRace(t *testing.T) {
	ctx := context.Background()
	repositories, planner, created, now := directExecutionFixture(t, "execution-pause-resume-race")
	created.Status = execution.ExecutionRunning
	created.StartedAt = &now
	if err := repositories.Executions().UpdateExecution(ctx, created); err != nil {
		t.Fatal(err)
	}
	job, err := repositories.Jobs().ClaimNext(ctx, "race-worker", now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := planner.PauseExecution(ctx, cleanup.PauseExecutionRequest{
		CleanupTaskID: "cln-direct", RequestedBy: "operator",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := planner.ResumeExecution(ctx, cleanup.ResumeExecutionRequest{
		CleanupTaskID: "cln-direct", RequestedBy: "operator",
	}); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Jobs().Complete(ctx, job.ID, "race-worker", execution.JobPaused, "", now); err != nil {
		t.Fatal(err)
	}
	handler := cleanup.NewExecutionHandler(
		planner,
		cleanup.ActionResolverFunc(func(context.Context, asset.Asset) (cleanup.ActionDriver, error) {
			return &statusChangingDriver{}, nil
		}),
	)
	if err := handler.ReconcileJobSettlement(ctx, job); err != nil {
		t.Fatal(err)
	}
	storedJob, err := repositories.Jobs().GetJob(ctx, job.ID)
	if err != nil || storedJob.Status != execution.JobPending {
		t.Fatalf("reconciled job=%+v err=%v", storedJob, err)
	}
	storedExecution, err := repositories.Executions().GetExecution(ctx, created.ID)
	if err != nil || storedExecution.Status != execution.ExecutionRunning {
		t.Fatalf("reconciled execution=%+v err=%v", storedExecution, err)
	}
}

func TestCreateExecutionRejectsConcurrencyOutsideOneToOneHundred(t *testing.T) {
	_, planner, _, _ := directExecutionFixture(t, "execution-concurrency-validation")
	for _, value := range []int{-1, 0, 101} {
		value := value
		_, err := planner.CreateExecution(context.Background(), cleanup.CreateExecutionRequest{
			CleanupTaskID: "cln-direct", RequestedBy: "operator",
			IdempotencyKey: fmt.Sprintf("invalid-concurrency-%d", value),
			Concurrency:    &value,
			Confirmation:   cleanup.ExecutionConfirmation{Acknowledged: true},
		})
		if !errors.Is(err, cleanup.ErrExecutionConcurrency) {
			t.Fatalf("concurrency %d error = %v, want ErrExecutionConcurrency", value, err)
		}
	}
}

func TestCreateExecutionAllowsLegacyCoverageOnlyDraftTask(t *testing.T) {
	ctx := context.Background()
	repositories, planner, selectors := rangeCoverageFixture(t, asset.ScanRun{
		ID: "scan-filtered-legacy", Status: asset.ScanSucceeded,
		ScopeMode: asset.ScanAllActiveRegions,
		Targets: []asset.ScanTarget{{
			Key: "region:cn-hangzhou", Kind: asset.ScanTargetRegion, RegionID: "cn-hangzhou",
		}},
		ResourceKindIDs: []asset.ResourceKindID{"kind-instance"},
	})
	created, err := planner.CreateTask(ctx, cleanup.CreateTaskRequest{
		Selectors: []plan.CleanupSelector{selectors[0]}, CreatedBy: "operator",
	})
	if err != nil {
		t.Fatal(err)
	}
	legacy := created.Task
	legacy.Status = plan.StatusDraft
	legacy.Blockers = []plan.Blocker{{
		Code: plan.BlockScanCoverageIncomplete, Message: "cleanup range requires a complete scan",
		Evidence: map[string]any{"coverage": legacy.Coverage},
	}}
	legacy.Warnings = nil
	if err := repositories.CleanupTasks().UpdateTask(ctx, legacy); err != nil {
		t.Fatal(err)
	}

	attempt, err := planner.CreateExecution(ctx, cleanup.CreateExecutionRequest{
		CleanupTaskID: legacy.ID, RequestedBy: "operator", IdempotencyKey: "legacy-coverage",
		Confirmation: cleanup.ExecutionConfirmation{Acknowledged: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if attempt.Status != execution.ExecutionPending {
		t.Fatalf("attempt = %+v", attempt)
	}
}

func TestCreateExecutionRejectsUnverifiedConnection(t *testing.T) {
	ctx := context.Background()
	repositories := openPlanningRepositories(t)
	now := time.Date(2026, 7, 27, 8, 0, 0, 0, time.UTC)
	value := planningAsset("guarded", "connection-guarded", "ACS::ECS::Instance", "i-guarded", now)
	value.ScopeID = "scope-guarded"
	seedPlanningSnapshot(t, repositories, "scope-guarded", "graph-guarded", []asset.Asset{value}, nil)
	planner := cleanup.NewService(repositories, bundleResolver{asset.ProviderAliCloud: {Provider: asset.ProviderAliCloud, Revision: "bundle-a", Hash: "spec-a"}},
		cleanup.WithClock(func() time.Time { return now }),
		cleanup.WithTaskIDGenerator(func() string { return "cln-guarded" }),
		cleanup.WithExecutionIDGenerator(func() string { return "execution-guarded" }),
	)
	if _, err := planner.CreateTask(ctx, cleanup.CreateTaskRequest{Selectors: []plan.CleanupSelector{assetSelector(value.ID)}, CreatedBy: "operator"}); err != nil {
		t.Fatal(err)
	}
	connection, err := repositories.Connections().GetConnection(ctx, value.Identity.ConnectionID)
	if err != nil {
		t.Fatal(err)
	}
	connection.Status = asset.ConnectionUnverified
	connection.UpdatedAt = now.Add(time.Minute)
	if err := repositories.Connections().PutConnection(ctx, connection); err != nil {
		t.Fatal(err)
	}

	_, err = planner.CreateExecution(ctx, cleanup.CreateExecutionRequest{
		CleanupTaskID: "cln-guarded", RequestedBy: "operator", IdempotencyKey: "guarded-key",
		Confirmation: cleanup.ExecutionConfirmation{Acknowledged: true},
	})
	if !errors.Is(err, asset.ErrConnectionNotValidated) {
		t.Fatalf("CreateExecution() error = %v, want ErrConnectionNotValidated", err)
	}
	if _, lookupErr := repositories.Executions().GetExecutionByIdempotencyKey(ctx, "guarded-key"); !errors.Is(lookupErr, persistence.ErrNotFound) {
		t.Fatalf("execution lookup error = %v", lookupErr)
	}
}

func TestExecutionIgnoresAssetMarkedDirtyAfterTaskCreation(t *testing.T) {
	ctx := context.Background()
	repositories := openPlanningRepositories(t)
	now := time.Date(2026, 8, 5, 9, 0, 0, 0, time.UTC)
	value := planningAsset("dirty-later", "connection-dirty", "ACS::ECS::Instance", "i-dirty", now)
	value.ScopeID = "scope-dirty"
	seedPlanningSnapshot(t, repositories, value.ScopeID, "graph-dirty", []asset.Asset{value}, nil)
	planner := cleanup.NewService(
		repositories,
		bundleResolver{asset.ProviderAliCloud: {Provider: asset.ProviderAliCloud, Revision: "bundle-a", Hash: "spec-a"}},
		cleanup.WithClock(func() time.Time { return now }),
		cleanup.WithTaskIDGenerator(func() string { return "cln-dirty-later" }),
		cleanup.WithExecutionIDGenerator(func() string { return "execution-dirty-later" }),
	)
	createdTask, err := planner.CreateTask(ctx, cleanup.CreateTaskRequest{
		Selectors: []plan.CleanupSelector{assetSelector(value.ID)}, CreatedBy: "operator",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repositories.Inventory().SetAssetDirty(ctx, value.ID, true); err != nil {
		t.Fatal(err)
	}
	createdExecution, err := planner.CreateExecution(ctx, cleanup.CreateExecutionRequest{
		CleanupTaskID: createdTask.Task.ID, RequestedBy: "operator", IdempotencyKey: "dirty-later",
		Confirmation: cleanup.ExecutionConfirmation{Acknowledged: true},
	})
	if err != nil {
		t.Fatalf("create execution after dirty mark: %v", err)
	}

	resolverCalled := false
	handler := cleanup.NewExecutionHandler(
		planner,
		cleanup.ActionResolverFunc(func(context.Context, asset.Asset) (cleanup.ActionDriver, error) {
			resolverCalled = true
			return nil, errors.New("provider resolver must not run for dirty assets")
		}),
	)
	if err := handler.Handle(ctx, claimExecutionJob(t, repositories, now)); err != nil {
		t.Fatal(err)
	}
	if resolverCalled {
		t.Fatal("provider resolver ran for a dirty asset")
	}
	actions, err := repositories.Executions().ListActions(ctx, createdExecution.ID)
	if err != nil || len(actions) != 1 ||
		actions[0].Status != execution.ActionSkipped ||
		actions[0].SkipReason != "dirty_asset" {
		t.Fatalf("dirty action = %+v, err = %v", actions, err)
	}
	storedExecution, err := repositories.Executions().GetExecution(ctx, createdExecution.ID)
	if err != nil || storedExecution.Status != execution.ExecutionSucceeded {
		t.Fatalf("execution = %+v, err = %v", storedExecution, err)
	}
	storedTask, err := repositories.CleanupTasks().GetTask(ctx, createdTask.Task.ID)
	if err != nil || storedTask.Task.Status != plan.StatusCompleted {
		t.Fatalf("cleanup task = %+v, err = %v", storedTask.Task, err)
	}
	openAsset, err := repositories.Inventory().GetAsset(ctx, value.ID)
	if err != nil || openAsset.ClosedAt != nil || !openAsset.Dirty {
		t.Fatalf("dirty asset was changed = %+v, err = %v", openAsset, err)
	}
}

func TestCreateExecutionRollsBackWhenConnectionChangesDuringTransaction(t *testing.T) {
	ctx := context.Background()
	repositories := openPlanningRepositories(t)
	now := time.Date(2026, 7, 27, 8, 30, 0, 0, time.UTC)
	value := planningAsset("racing", "connection-racing", "ACS::ECS::Instance", "i-racing", now)
	value.ScopeID = "scope-racing"
	seedPlanningSnapshot(t, repositories, "scope-racing", "graph-racing", []asset.Asset{value}, nil)
	basePlanner := cleanup.NewService(repositories, bundleResolver{asset.ProviderAliCloud: {Provider: asset.ProviderAliCloud, Revision: "bundle-a", Hash: "spec-a"}},
		cleanup.WithClock(func() time.Time { return now }),
		cleanup.WithTaskIDGenerator(func() string { return "cln-racing" }),
	)
	if _, err := basePlanner.CreateTask(ctx, cleanup.CreateTaskRequest{Selectors: []plan.CleanupSelector{assetSelector(value.ID)}, CreatedBy: "operator"}); err != nil {
		t.Fatal(err)
	}
	conflicting := workConflictRepositories{Repositories: repositories}
	planner := cleanup.NewService(conflicting, bundleResolver{asset.ProviderAliCloud: {Provider: asset.ProviderAliCloud, Revision: "bundle-a", Hash: "spec-a"}},
		cleanup.WithClock(func() time.Time { return now }),
		cleanup.WithExecutionIDGenerator(func() string { return "execution-racing" }),
	)

	_, err := planner.CreateExecution(ctx, cleanup.CreateExecutionRequest{
		CleanupTaskID: "cln-racing", RequestedBy: "operator", IdempotencyKey: "racing-key",
		Confirmation: cleanup.ExecutionConfirmation{Acknowledged: true},
	})
	if !errors.Is(err, persistence.ErrConflict) {
		t.Fatalf("CreateExecution() error = %v, want ErrConflict", err)
	}
	if _, lookupErr := repositories.Executions().GetExecutionByIdempotencyKey(ctx, "racing-key"); !errors.Is(lookupErr, persistence.ErrNotFound) {
		t.Fatalf("execution lookup error = %v", lookupErr)
	}
}

func TestExecutionHandlerInvalidatesEmptyReexpandedRangeBeforeProviderResolution(t *testing.T) {
	ctx := context.Background()
	repositories := openPlanningRepositories(t)
	now := time.Date(2026, 7, 24, 13, 0, 0, 0, time.UTC)
	value := planningAsset("worker-range-last", "connection-worker-range", "ACS::ECS::Instance", "i-last", now)
	value.ScopeID = "scope-worker-range"
	seedPlanningSnapshot(t, repositories, value.ScopeID, "graph-worker-range", []asset.Asset{value}, nil)
	seedCompleteScan(t, repositories, value.Identity.ConnectionID, value.ScopeID, now)
	planner := cleanup.NewService(repositories, bundleResolver{
		asset.ProviderAliCloud: {Provider: asset.ProviderAliCloud, Revision: "bundle-a", Hash: "spec-a"},
	},
		cleanup.WithClock(func() time.Time { return now }),
		cleanup.WithTaskIDGenerator(func() string { return "cln-worker-range" }),
		cleanup.WithExecutionIDGenerator(func() string { return "execution-worker-range" }),
	)
	createdTask, err := planner.CreateTask(ctx, cleanup.CreateTaskRequest{
		Selectors: []plan.CleanupSelector{{
			Kind: plan.SelectorScope, ConnectionID: value.Identity.ConnectionID,
			ScopeID: value.ScopeID, Descendants: true,
		}},
		CreatedBy: "operator",
	})
	if err != nil {
		t.Fatalf("create range task: %v", err)
	}
	createdExecution, err := planner.CreateExecution(ctx, cleanup.CreateExecutionRequest{
		CleanupTaskID: createdTask.Task.ID, RequestedBy: "operator", IdempotencyKey: "worker-range-key",
		Confirmation: cleanup.ExecutionConfirmation{TypedNames: map[string]string{string(value.ScopeID): string(value.ScopeID)}},
	})
	if err != nil {
		t.Fatalf("create range execution: %v", err)
	}
	closedAt := now.Add(time.Minute)
	value.ClosedAt = &closedAt
	if err := repositories.Inventory().PutAsset(ctx, value); err != nil {
		t.Fatal(err)
	}
	resolverCalled := false
	handler := cleanup.NewExecutionHandler(planner, cleanup.ActionResolverFunc(func(context.Context, asset.Asset) (cleanup.ActionDriver, error) {
		resolverCalled = true
		return nil, errors.New("provider resolver must not run")
	}))
	job := claimExecutionJob(t, repositories, now)

	err = handler.Handle(ctx, job)
	if !errors.Is(err, plan.ErrCleanupTaskInvalidated) {
		t.Fatalf("handle error = %v", err)
	}
	if resolverCalled {
		t.Fatal("provider resolver ran after the authoritative selection became empty")
	}
	storedTask, getErr := repositories.CleanupTasks().GetTask(ctx, createdTask.Task.ID)
	if getErr != nil || storedTask.Task.Status != plan.StatusInvalidated {
		t.Fatalf("stored cleanup task = %+v, err = %v", storedTask, getErr)
	}
	storedExecution, getErr := repositories.Executions().GetExecution(ctx, createdExecution.ID)
	if getErr != nil || storedExecution.Status != execution.ExecutionFailed {
		t.Fatalf("stored execution = %+v, err = %v", storedExecution, getErr)
	}
}

func TestExecutionHandlerWaitsForInventoryRelationshipReconciliation(t *testing.T) {
	ctx := context.Background()
	repositories, planner, created, now := directExecutionFixture(t, "execution-waits-for-graph")
	if err := repositories.Inventory().CreateScanRun(ctx, asset.ScanRun{
		ID: "scan-reconciling", ConnectionID: created.ConnectionID,
		Status: asset.ScanReconciling, CompletionStatus: asset.ScanSucceeded,
		CreatedAt: now, StartedAt: &now,
	}); err != nil {
		t.Fatal(err)
	}
	resolverCalled := false
	handler := cleanup.NewExecutionHandler(
		planner,
		cleanup.ActionResolverFunc(func(context.Context, asset.Asset) (cleanup.ActionDriver, error) {
			resolverCalled = true
			return nil, errors.New("provider resolver must not run")
		}),
		cleanup.WithExecutionRetryDelay(time.Second),
	)
	job := claimExecutionJob(t, repositories, now)

	err := handler.Handle(ctx, job)
	var retry *cleanup.RetryError
	if !errors.As(err, &retry) || !errors.Is(err, cleanup.ErrInventoryReconciliationPending) {
		t.Fatalf("Handle() error = %v, want retryable inventory reconciliation error", err)
	}
	if resolverCalled {
		t.Fatal("provider resolver ran before inventory relationship reconciliation completed")
	}
	storedExecution, getErr := repositories.Executions().GetExecution(ctx, created.ID)
	if getErr != nil || storedExecution.Status != execution.ExecutionPending || storedExecution.FinishedAt != nil {
		t.Fatalf("execution should remain pending: execution=%+v err=%v", storedExecution, getErr)
	}
	storedTask, getErr := repositories.CleanupTasks().GetTask(ctx, plan.CleanupTaskID(created.CleanupTaskID))
	if getErr != nil || storedTask.Task.Status != plan.StatusExecuting {
		t.Fatalf("cleanup task should remain executing: task=%+v err=%v", storedTask.Task, getErr)
	}
}

func TestCreateExecutionInvalidatesEmptyReexpandedRange(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 24, 15, 30, 0, 0, time.UTC)
	run := asset.ScanRun{
		ID: "scan-create-execution-empty", Status: asset.ScanSucceeded,
		ScopeMode: asset.ScanAllActiveRegions,
		Targets: []asset.ScanTarget{{
			Key: "region:cn-hangzhou", Kind: asset.ScanTargetRegion, RegionID: "cn-hangzhou",
		}},
	}
	repositories, planner, selectors := rangeCoverageFixture(t, run)
	created, err := planner.CreateTask(ctx, cleanup.CreateTaskRequest{
		Selectors: []plan.CleanupSelector{selectors[1]}, CreatedBy: "operator",
	})
	if err != nil || created.Task.Status != plan.StatusReady {
		t.Fatalf("created = %+v, err = %v", created, err)
	}
	for _, id := range created.Task.ResolvedAssetIDs {
		value, err := repositories.Inventory().GetAsset(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		value.ClosedAt = &now
		if err := repositories.Inventory().PutAsset(ctx, value); err != nil {
			t.Fatal(err)
		}
	}

	_, err = planner.CreateExecution(ctx, cleanup.CreateExecutionRequest{
		CleanupTaskID: created.Task.ID, RequestedBy: "operator", IdempotencyKey: "empty-range-execution",
		Confirmation: cleanup.ExecutionConfirmation{TypedNames: map[string]string{"Hangzhou": "Hangzhou"}},
	})
	if !errors.Is(err, plan.ErrCleanupTaskInvalidated) {
		t.Fatalf("create execution error = %v", err)
	}
	stored, getErr := repositories.CleanupTasks().GetTask(ctx, created.Task.ID)
	if getErr != nil || stored.Task.Status != plan.StatusInvalidated {
		t.Fatalf("stored cleanup task = %+v, err = %v", stored, getErr)
	}
	if _, getErr := repositories.Executions().GetExecutionByIdempotencyKey(ctx, "empty-range-execution"); !errors.Is(getErr, persistence.ErrNotFound) {
		t.Fatalf("execution was persisted: %v", getErr)
	}
}

func TestValidateExecutionConfirmation(t *testing.T) {
	tests := []struct {
		name         string
		selectors    []plan.CleanupSelector
		confirmation cleanup.ExecutionConfirmation
		wantErr      bool
	}{
		{
			name:      "connection requires its exact display name",
			selectors: []plan.CleanupSelector{{Kind: plan.SelectorConnection, ConnectionID: "connection-a", DisplayName: "production"}},
			confirmation: cleanup.ExecutionConfirmation{
				TypedNames: map[string]string{"production": "production"},
			},
		},
		{
			name:      "wrong connection name is rejected",
			selectors: []plan.CleanupSelector{{Kind: plan.SelectorConnection, ConnectionID: "connection-a", DisplayName: "production"}},
			confirmation: cleanup.ExecutionConfirmation{
				TypedNames: map[string]string{"production": "Production"},
			},
			wantErr: true,
		},
		{
			name:      "region falls back to its stable identity",
			selectors: []plan.CleanupSelector{{Kind: plan.SelectorScope, ScopeID: "cn-hangzhou", ScopeKind: asset.ScopeRegion}},
			confirmation: cleanup.ExecutionConfirmation{
				TypedNames: map[string]string{"cn-hangzhou": "cn-hangzhou"},
			},
		},
		{
			name:         "asset requires explicit acknowledgement",
			selectors:    []plan.CleanupSelector{{Kind: plan.SelectorAsset, AssetID: "asset-a"}},
			confirmation: cleanup.ExecutionConfirmation{},
			wantErr:      true,
		},
		{
			name: "mixed selection requires both confirmation forms",
			selectors: []plan.CleanupSelector{
				{Kind: plan.SelectorConnection, ConnectionID: "connection-a", DisplayName: "production"},
				{Kind: plan.SelectorAsset, AssetID: "asset-a"},
			},
			confirmation: cleanup.ExecutionConfirmation{TypedNames: map[string]string{"production": "production"}},
			wantErr:      true,
		},
		{
			name: "mixed selection accepts all required proofs",
			selectors: []plan.CleanupSelector{
				{Kind: plan.SelectorConnection, ConnectionID: "connection-a", DisplayName: "production"},
				{Kind: plan.SelectorAsset, AssetID: "asset-a"},
			},
			confirmation: cleanup.ExecutionConfirmation{TypedNames: map[string]string{"production": "production"}, Acknowledged: true},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := cleanup.ValidateExecutionConfirmation(tt.selectors, tt.confirmation)
			if errors.Is(err, cleanup.ErrExecutionConfirmation) != tt.wantErr {
				t.Fatalf("error = %v, want confirmation error = %t", err, tt.wantErr)
			}
		})
	}
}

func TestCreateExecutionEnforcesBackendAuthorization(t *testing.T) {
	ctx := context.Background()
	repositories := openPlanningRepositories(t)
	now := time.Date(2026, 7, 13, 11, 0, 0, 0, time.UTC)
	value := planningAsset("direct", "c-1", "ACS::ECS::Instance", "i-1", now)
	seedPlanningSnapshot(t, repositories, "scope-a", "graph-direct", []asset.Asset{value}, nil)
	denied := errors.New("cleanup permission denied")
	planner := cleanup.NewService(repositories, bundleResolver{asset.ProviderAliCloud: {Provider: asset.ProviderAliCloud, Revision: "bundle-a", Hash: "spec-a"}},
		cleanup.WithTaskIDGenerator(func() string { return "cln-denied" }),
		cleanup.WithExecutionAuthorizer(cleanup.ExecutionAuthorizerFunc(func(context.Context, string, []asset.AssetID) error { return denied })),
	)
	if _, err := planner.CreateTask(ctx, cleanup.CreateTaskRequest{Selectors: []plan.CleanupSelector{assetSelector("direct")}, CreatedBy: "operator"}); err != nil {
		t.Fatal(err)
	}
	if _, err := planner.CreateExecution(ctx, cleanup.CreateExecutionRequest{CleanupTaskID: "cln-denied", RequestedBy: "operator", IdempotencyKey: "denied-key", Confirmation: cleanup.ExecutionConfirmation{Acknowledged: true}}); !errors.Is(err, denied) {
		t.Fatalf("execution authorization error = %v", err)
	}
	if _, err := repositories.Executions().GetExecutionByIdempotencyKey(ctx, "denied-key"); !errors.Is(err, persistence.ErrNotFound) {
		t.Fatalf("denied execution was persisted: %v", err)
	}
}

func TestExecutionHandlerRecoversCrashAfterIntentBeforeProviderCall(t *testing.T) {
	ctx := requestmeta.WithRequestID(context.Background(), "steward-request")
	repositories, planner, created, now := directExecutionFixture(t, "execution-intent")
	driver := &scriptedActionDriver{readback: contracts.ReadbackResult{Exists: false, State: "absent"}}
	intentCrash := errors.New("crash after intent")
	handler := cleanup.NewExecutionHandler(planner, cleanup.ActionResolverFunc(func(context.Context, asset.Asset) (cleanup.ActionDriver, error) {
		return driver, nil
	}), cleanup.WithAfterIntentPersisted(func(execution.ActionAttempt) error { return intentCrash }))
	job := claimExecutionJob(t, repositories, now)

	if err := handler.Handle(ctx, job); !errors.Is(err, intentCrash) {
		t.Fatalf("first handle error = %v", err)
	}
	actions, err := repositories.Executions().ListActions(ctx, created.ID)
	if err != nil || len(actions) != 1 || actions[0].Status != execution.ActionIntentPersisted || driver.executeCalls != 0 {
		t.Fatalf("actions=%+v executeCalls=%d err=%v", actions, driver.executeCalls, err)
	}

	handler = cleanup.NewExecutionHandler(planner, cleanup.ActionResolverFunc(func(context.Context, asset.Asset) (cleanup.ActionDriver, error) {
		return driver, nil
	}))
	if err := handler.Handle(ctx, job); err != nil {
		t.Fatal(err)
	}
	actions, err = repositories.Executions().ListActions(ctx, created.ID)
	if err != nil || actions[0].Status != execution.ActionSucceeded || driver.executeCalls != 1 {
		t.Fatalf("actions=%+v executeCalls=%d err=%v", actions, driver.executeCalls, err)
	}
	closed, err := repositories.Inventory().GetAsset(ctx, "direct")
	if err != nil || closed.ClosedAt == nil || closed.DeletedAt == nil ||
		!closed.DeletedAt.Equal(*closed.ClosedAt) {
		t.Fatalf("readback did not close direct asset: asset=%+v err=%v", closed, err)
	}
	audits, err := repositories.Audits().ListAuditEvents(ctx, persistence.ListOptions{Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	var actionAudit *execution.AuditEvent
	for index := range audits.Items {
		if audits.Items[index].Action == "cleanup.action.succeeded" {
			actionAudit = &audits.Items[index]
			break
		}
	}
	if actionAudit == nil ||
		actionAudit.RequestID != "steward-request" ||
		actionAudit.Evidence["provider_request_id"] != "request-delete" {
		t.Fatalf("action audit = %#v", actionAudit)
	}
}

func TestExecutionHandlerKeepsFrozenSnapshotAfterFirstMultiResourceAction(t *testing.T) {
	ctx := context.Background()
	repositories := openPlanningRepositories(t)
	now := time.Date(2026, 7, 13, 11, 0, 0, 0, time.UTC)
	first := planningAsset("direct-a", "c-1", "ACS::ECS::Instance", "i-a", now)
	second := planningAsset("direct-b", "c-1", "ACS::ECS::Instance", "i-b", now)
	seedPlanningSnapshot(t, repositories, "scope-a", "graph-multi-direct", []asset.Asset{first, second}, nil)
	planner := cleanup.NewService(
		repositories,
		bundleResolver{asset.ProviderAliCloud: {Provider: asset.ProviderAliCloud, Revision: "bundle-a", Hash: "spec-a"}},
		cleanup.WithClock(func() time.Time { return now }),
		cleanup.WithTaskIDGenerator(func() string { return "cln-multi-direct" }),
		cleanup.WithExecutionIDGenerator(func() string { return "execution-multi-direct" }),
	)
	if _, err := planner.CreateTask(ctx, cleanup.CreateTaskRequest{
		Selectors: []plan.CleanupSelector{assetSelector(first.ID), assetSelector(second.ID)},
		CreatedBy: "operator",
	}); err != nil {
		t.Fatal(err)
	}
	created, err := planner.CreateExecution(ctx, cleanup.CreateExecutionRequest{
		CleanupTaskID: "cln-multi-direct", RequestedBy: "operator", IdempotencyKey: "multi-direct-key",
		Confirmation: cleanup.ExecutionConfirmation{Acknowledged: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	driver := &scriptedActionDriver{readback: contracts.ReadbackResult{Exists: false, State: "absent"}}
	handler := cleanup.NewExecutionHandler(planner, cleanup.ActionResolverFunc(func(context.Context, asset.Asset) (cleanup.ActionDriver, error) {
		return driver, nil
	}))

	firstJob := claimExecutionJob(t, repositories, now)
	if err := handler.Handle(ctx, firstJob); err != nil {
		t.Fatalf("first resource cleanup: %v", err)
	}
	secondJob := claimExecutionJob(t, repositories, now)
	if err := handler.Handle(ctx, secondJob); err != nil {
		t.Fatalf("second resource cleanup must reuse the execution snapshot: %v", err)
	}

	actions, err := repositories.Executions().ListActions(ctx, created.ID)
	if err != nil || len(actions) != 2 {
		t.Fatalf("actions=%+v err=%v", actions, err)
	}
	for _, action := range actions {
		if action.Status != execution.ActionSucceeded {
			t.Fatalf("action %s status=%s", action.ID, action.Status)
		}
	}
	storedExecution, err := repositories.Executions().GetExecution(ctx, created.ID)
	if err != nil || storedExecution.Status != execution.ExecutionSucceeded {
		t.Fatalf("execution=%+v err=%v", storedExecution, err)
	}
	storedTask, err := repositories.CleanupTasks().GetTask(ctx, "cln-multi-direct")
	if err != nil || storedTask.Task.Status != plan.StatusCompleted {
		t.Fatalf("cleanup task=%+v err=%v", storedTask.Task, err)
	}
	if driver.executeCalls != 2 {
		t.Fatalf("provider execute calls=%d", driver.executeCalls)
	}
}

func TestExecutionHandlerSpacesWaiterAndReadbackQueries(t *testing.T) {
	ctx := context.Background()
	repositories, planner, created, now := directExecutionFixture(t, "execution-poll-spacing")
	driver := &scriptedActionDriver{
		pollInterval: 2 * time.Second,
		readback:     contracts.ReadbackResult{Exists: false, State: "absent"},
	}
	handler := cleanup.NewExecutionHandler(planner, cleanup.ActionResolverFunc(func(context.Context, asset.Asset) (cleanup.ActionDriver, error) {
		return driver, nil
	}))
	job := claimExecutionJob(t, repositories, now)

	err := handler.Handle(ctx, job)
	var retry *cleanup.RetryError
	if !errors.As(err, &retry) || retry.After != 2*time.Second ||
		driver.executeCalls != 1 || driver.waitCalls != 0 || driver.readbackCalls != 0 {
		t.Fatalf(
			"after delete: retry=%v execute=%d wait=%d readback=%d",
			err, driver.executeCalls, driver.waitCalls, driver.readbackCalls,
		)
	}
	actions, err := repositories.Executions().ListActions(ctx, created.ID)
	if err != nil || len(actions) != 1 ||
		actions[0].Status != execution.ActionWaiting ||
		actions[0].PollIntervalSeconds != 2 {
		t.Fatalf("waiting action=%+v err=%v", actions, err)
	}

	err = handler.Handle(ctx, job)
	if !errors.As(err, &retry) || retry.After != 2*time.Second ||
		driver.executeCalls != 1 || driver.waitCalls != 1 || driver.readbackCalls != 0 {
		t.Fatalf(
			"after waiter: retry=%v execute=%d wait=%d readback=%d",
			err, driver.executeCalls, driver.waitCalls, driver.readbackCalls,
		)
	}
	if err := handler.Handle(ctx, job); err != nil {
		t.Fatal(err)
	}
	if driver.executeCalls != 1 || driver.waitCalls != 1 || driver.readbackCalls != 1 {
		t.Fatalf(
			"after readback: execute=%d wait=%d readback=%d",
			driver.executeCalls, driver.waitCalls, driver.readbackCalls,
		)
	}
}

func TestExecutionHandlerPersistsWaiterDataForTheNextPoll(t *testing.T) {
	ctx := context.Background()
	repositories, planner, created, now := directExecutionFixture(t, "execution-waiter-data")
	driver := &waiterDataDriver{}
	handler := cleanup.NewExecutionHandler(
		planner,
		cleanup.ActionResolverFunc(func(context.Context, asset.Asset) (cleanup.ActionDriver, error) {
			return driver, nil
		}),
	)
	job := claimExecutionJob(t, repositories, now)

	for poll := 0; poll < 3; poll++ {
		var retry *cleanup.RetryError
		if err := handler.Handle(ctx, job); !errors.As(err, &retry) {
			t.Fatalf("poll %d error = %v, want retry", poll, err)
		}
	}
	if len(driver.waitPhases) != 2 || driver.waitPhases[0] != "first" ||
		driver.waitPhases[1] != "second" {
		t.Fatalf("waiter phases = %#v", driver.waitPhases)
	}
	actions, err := repositories.Executions().ListActions(ctx, created.ID)
	if err != nil || len(actions) != 1 || actions[0].ProviderResult["phase"] != "second" {
		t.Fatalf("persisted waiter result = %+v, err=%v", actions, err)
	}
}

func TestCleanupWorkerProcessesTwentyProviderCallsConcurrently(t *testing.T) {
	if cleanup.ExecutionWorkerConcurrency != 20 {
		t.Fatalf("cleanup worker concurrency = %d, want 20", cleanup.ExecutionWorkerConcurrency)
	}
	ctx := context.Background()
	repositories := openPlanningRepositories(t)
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	assets := make([]asset.Asset, 0, cleanup.ExecutionWorkerConcurrency+1)
	selectors := make([]plan.CleanupSelector, 0, cleanup.ExecutionWorkerConcurrency+1)
	for index := 0; index < cleanup.ExecutionWorkerConcurrency+1; index++ {
		id := asset.AssetID(fmt.Sprintf("parallel-%02d", index+1))
		assets = append(assets, planningAsset(id, "c-parallel", "ACS::ECS::Instance", "i-"+string(id), now))
		selectors = append(selectors, assetSelector(id))
	}
	seedPlanningSnapshot(t, repositories, "scope-a", "graph-parallel", assets, nil)
	planner := cleanup.NewService(
		repositories,
		bundleResolver{asset.ProviderAliCloud: {Provider: asset.ProviderAliCloud, Revision: "bundle-a", Hash: "spec-a"}},
		cleanup.WithClock(func() time.Time { return now }),
		cleanup.WithTaskIDGenerator(func() string { return "cln-parallel" }),
		cleanup.WithExecutionIDGenerator(func() string { return "execution-parallel" }),
	)
	if _, err := planner.CreateTask(ctx, cleanup.CreateTaskRequest{Selectors: selectors, CreatedBy: "operator"}); err != nil {
		t.Fatal(err)
	}
	created, err := planner.CreateExecution(ctx, cleanup.CreateExecutionRequest{
		CleanupTaskID: "cln-parallel", RequestedBy: "operator", IdempotencyKey: "parallel-key",
		Confirmation: cleanup.ExecutionConfirmation{Acknowledged: true},
	})
	if err != nil {
		t.Fatal(err)
	}

	started := make(chan asset.AssetID, len(assets))
	release := make(chan struct{})
	driver := &blockingActionDriver{started: started, release: release}
	handler := cleanup.NewExecutionHandler(
		planner,
		cleanup.ActionResolverFunc(func(context.Context, asset.Asset) (cleanup.ActionDriver, error) {
			return driver, nil
		}),
	)
	workerCtx, cancelWorker := context.WithCancel(context.Background())
	workerErrors := make(chan error, len(assets))
	worker := cleanup.NewWorker(
		repositories.Jobs(),
		map[execution.JobType]cleanup.Handler{execution.JobExecute: handler},
		cleanup.WorkerOptions{
			WorkerID: "parallel-cleanup", AllowedTypes: []execution.JobType{execution.JobExecute},
			Concurrency: cleanup.ExecutionWorkerConcurrency, PollInterval: time.Millisecond,
			RenewInterval: time.Hour, Now: func() time.Time { return now },
			OnError: func(err error) { workerErrors <- err },
		},
	)
	workerDone := make(chan error, 1)
	go func() { workerDone <- worker.Run(workerCtx) }()
	workerStopped := false
	t.Cleanup(func() {
		if workerStopped {
			return
		}
		cancelWorker()
		select {
		case <-workerDone:
		case <-time.After(2 * time.Second):
			t.Error("cleanup worker did not stop")
		}
	})

	for index := 0; index < cleanup.ExecutionWorkerConcurrency; index++ {
		select {
		case <-started:
		case err := <-workerErrors:
			t.Fatalf("cleanup worker failed before filling concurrency slots: %v", err)
		case <-time.After(2 * time.Second):
			t.Fatalf("only %d independent cleanup actions started", index)
		}
	}
	select {
	case id := <-started:
		t.Fatalf("cleanup exceeded worker concurrency %d; extra action %s started", cleanup.ExecutionWorkerConcurrency, id)
	case err := <-workerErrors:
		t.Fatalf("cleanup worker failed while actions were blocked: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	close(release)
	select {
	case <-started:
	case err := <-workerErrors:
		t.Fatalf("cleanup worker failed before starting the queued action: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("queued independent cleanup action did not start")
	}

	deadline := time.After(3 * time.Second)
	for {
		stored, err := repositories.Executions().GetExecution(ctx, created.ID)
		if err != nil {
			t.Fatal(err)
		}
		if stored.Status == execution.ExecutionSucceeded {
			break
		}
		select {
		case err := <-workerErrors:
			t.Fatalf("cleanup worker failed: %v", err)
		case <-deadline:
			t.Fatalf("parallel cleanup status = %s, want succeeded", stored.Status)
		case <-time.After(10 * time.Millisecond):
		}
	}
	actions, err := repositories.Executions().ListActions(ctx, created.ID)
	if err != nil || len(actions) != len(assets) {
		t.Fatalf("parallel actions=%d err=%v", len(actions), err)
	}
	for _, action := range actions {
		if action.Status != execution.ActionSucceeded {
			t.Fatalf("parallel action %s status=%s", action.ID, action.Status)
		}
	}
	events, err := repositories.Executions().ListPendingOutbox(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	completedEvents := 0
	for _, event := range events {
		if event.Topic == "execution.completed" && event.AggregateID == string(created.ID) {
			completedEvents++
		}
	}
	if completedEvents != 1 {
		t.Fatalf("execution.completed events=%d, want 1", completedEvents)
	}
	jobsDeadline := time.After(3 * time.Second)
	for {
		jobs, err := repositories.Jobs().ListJobsByAggregate(ctx, "cleanup_task", "cln-parallel")
		if err != nil {
			t.Fatal(err)
		}
		allFinished := len(jobs) == len(assets)
		for _, job := range jobs {
			allFinished = allFinished && job.Status == execution.JobSucceeded
		}
		if allFinished {
			break
		}
		select {
		case err := <-workerErrors:
			t.Fatalf("cleanup worker failed while finishing jobs: %v", err)
		case <-jobsDeadline:
			t.Fatal("parallel cleanup jobs did not finish")
		case <-time.After(10 * time.Millisecond):
		}
	}
	cancelWorker()
	select {
	case err := <-workerDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cleanup worker stop error = %v", err)
		}
		workerStopped = true
	case <-time.After(2 * time.Second):
		t.Fatal("cleanup worker did not stop")
	}
}

func TestCleanupConcurrencyLimitsResourcesAcrossProviderWaits(t *testing.T) {
	ctx := context.Background()
	repositories := openPlanningRepositories(t)
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	assets := make([]asset.Asset, 0, 3)
	selectors := make([]plan.CleanupSelector, 0, 3)
	for index := 1; index <= 3; index++ {
		id := asset.AssetID(fmt.Sprintf("lifecycle-%d", index))
		assets = append(assets, planningAsset(id, "c-lifecycle", "ACS::ECS::Instance", "i-"+string(id), now))
		selectors = append(selectors, assetSelector(id))
	}
	seedPlanningSnapshot(t, repositories, "scope-a", "graph-lifecycle", assets, nil)
	planner := cleanup.NewService(
		repositories,
		bundleResolver{asset.ProviderAliCloud: {Provider: asset.ProviderAliCloud, Revision: "bundle-a", Hash: "spec-a"}},
		cleanup.WithClock(func() time.Time { return now }),
		cleanup.WithTaskIDGenerator(func() string { return "cln-lifecycle" }),
		cleanup.WithExecutionIDGenerator(func() string { return "execution-lifecycle" }),
	)
	if _, err := planner.CreateTask(ctx, cleanup.CreateTaskRequest{Selectors: selectors, CreatedBy: "operator"}); err != nil {
		t.Fatal(err)
	}
	concurrency := 2
	created, err := planner.CreateExecution(ctx, cleanup.CreateExecutionRequest{
		CleanupTaskID: "cln-lifecycle", RequestedBy: "operator", IdempotencyKey: "lifecycle-key",
		Concurrency: &concurrency, Confirmation: cleanup.ExecutionConfirmation{Acknowledged: true},
	})
	if err != nil || created.Concurrency != concurrency {
		t.Fatalf("created execution=%+v err=%v", created, err)
	}
	jobs, err := repositories.Jobs().ListJobsByAggregate(ctx, "cleanup_task", "cln-lifecycle")
	if err != nil {
		t.Fatal(err)
	}
	jobsByAsset := make(map[string]execution.Job, len(jobs))
	for _, job := range jobs {
		jobsByAsset[job.TargetKey] = job
	}
	driver := &scriptedActionDriver{
		pollInterval: time.Hour,
		readback:     contracts.ReadbackResult{Exists: false, State: "absent"},
	}
	handler := cleanup.NewExecutionHandler(
		planner,
		cleanup.ActionResolverFunc(func(context.Context, asset.Asset) (cleanup.ActionDriver, error) {
			return driver, nil
		}),
	)
	for _, id := range []string{"lifecycle-1", "lifecycle-2"} {
		var retry *cleanup.RetryError
		if err := handler.Handle(ctx, jobsByAsset[id]); !errors.As(err, &retry) {
			t.Fatalf("start %s error = %v, want provider wait", id, err)
		}
	}
	var capacityRetry *cleanup.RetryError
	if err := handler.Handle(ctx, jobsByAsset["lifecycle-3"]); !errors.As(err, &capacityRetry) ||
		!strings.Contains(err.Error(), "concurrency limit 2 reached") {
		t.Fatalf("third resource error = %v, want concurrency wait", err)
	}
	actions, err := repositories.Executions().ListActions(ctx, created.ID)
	if err != nil || len(actions) != concurrency || driver.executeCalls != concurrency {
		t.Fatalf("actions=%d provider deletes=%d err=%v", len(actions), driver.executeCalls, err)
	}
	inFlight, err := repositories.Executions().CountInFlightActions(ctx, created.ID)
	if err != nil || inFlight != concurrency {
		t.Fatalf("in-flight resources=%d err=%v", inFlight, err)
	}

	var retry *cleanup.RetryError
	if err := handler.Handle(ctx, jobsByAsset["lifecycle-1"]); !errors.As(err, &retry) {
		t.Fatalf("complete provider wait: %v", err)
	}
	if err := handler.Handle(ctx, jobsByAsset["lifecycle-1"]); err != nil {
		t.Fatalf("complete readback: %v", err)
	}
	if err := handler.Handle(ctx, jobsByAsset["lifecycle-3"]); !errors.As(err, &retry) {
		t.Fatalf("start queued resource: %v", err)
	}
	if driver.executeCalls != 3 {
		t.Fatalf("provider deletes=%d, want queued third resource to start", driver.executeCalls)
	}
	inFlight, err = repositories.Executions().CountInFlightActions(ctx, created.ID)
	if err != nil || inFlight != concurrency {
		t.Fatalf("in-flight resources after slot release=%d err=%v", inFlight, err)
	}
}

func TestFailedExecutionContinuesIndependentActionsAndCanRetryFailure(t *testing.T) {
	ctx := context.Background()
	repositories := openPlanningRepositories(t)
	now := time.Date(2026, 8, 4, 20, 0, 0, 0, time.UTC)
	assets := []asset.Asset{
		planningAsset("active", "c-1", "ACS::VPC::VPC", "vpc-active", now),
		planningAsset("failed", "c-1", "ACS::SLS::Project", "project-failed", now),
		planningAsset("not-started", "c-1", "ACS::ESS::ScalingGroup", "asg-not-started", now),
	}
	seedPlanningSnapshot(t, repositories, "scope-a", "graph-failure-isolation", assets, nil)
	planner := cleanup.NewService(
		repositories,
		bundleResolver{asset.ProviderAliCloud: {Provider: asset.ProviderAliCloud, Revision: "bundle-a", Hash: "spec-a"}},
		cleanup.WithClock(func() time.Time { return now }),
		cleanup.WithTaskIDGenerator(func() string { return "cln-failure-isolation" }),
		cleanup.WithExecutionIDGenerator(func() string { return "execution-failure-isolation" }),
	)
	selectors := make([]plan.CleanupSelector, 0, len(assets))
	for _, value := range assets {
		selectors = append(selectors, assetSelector(value.ID))
	}
	if _, err := planner.CreateTask(ctx, cleanup.CreateTaskRequest{
		Selectors: selectors,
		CreatedBy: "operator",
	}); err != nil {
		t.Fatal(err)
	}
	created, err := planner.CreateExecution(ctx, cleanup.CreateExecutionRequest{
		CleanupTaskID: "cln-failure-isolation", RequestedBy: "operator",
		IdempotencyKey: "failure-isolation-key",
		Confirmation:   cleanup.ExecutionConfirmation{Acknowledged: true},
	})
	if err != nil {
		t.Fatal(err)
	}

	activeDriver := &scriptedActionDriver{
		waitResults: []contracts.WaitResult{
			{Done: false, State: "running", RetryAfter: time.Second},
			{Done: true, State: "done"},
		},
		readback: contracts.ReadbackResult{Exists: false, State: "absent"},
	}
	failedDriver := &scriptedActionDriver{executeErrors: []error{
		&contracts.ProviderCallError{Provider: execution.ProviderError{
			Category: execution.ErrorUnsupported,
			Code:     "OLSInvalidMethod",
			Message:  "the provider rejected the request",
		}},
	}}
	notStartedDriver := &scriptedActionDriver{readback: contracts.ReadbackResult{Exists: false, State: "absent"}}
	drivers := map[asset.AssetID]*scriptedActionDriver{
		"active": activeDriver, "failed": failedDriver, "not-started": notStartedDriver,
	}
	handler := cleanup.NewExecutionHandler(
		planner,
		cleanup.ActionResolverFunc(func(_ context.Context, value asset.Asset) (cleanup.ActionDriver, error) {
			return drivers[value.ID], nil
		}),
	)

	jobs := make(map[string]execution.Job, len(assets))
	for range assets {
		job := claimExecutionJob(t, repositories, now)
		jobs[job.TargetKey] = job
	}
	var retry *cleanup.RetryError
	if err := handler.Handle(ctx, jobs["active"]); !errors.As(err, &retry) {
		t.Fatalf("active action first pass error=%v, want retry", err)
	}
	if err := handler.Handle(ctx, jobs["failed"]); err != nil {
		t.Fatalf("failed action handler returned infrastructure error: %v", err)
	}
	storedExecution, err := repositories.Executions().GetExecution(ctx, created.ID)
	if err != nil || storedExecution.Status != execution.ExecutionRunning {
		t.Fatalf("execution became terminal while independent actions were active: %+v err=%v", storedExecution, err)
	}
	storedTask, err := repositories.CleanupTasks().GetTask(ctx, "cln-failure-isolation")
	if err != nil || storedTask.Task.Status != plan.StatusExecuting {
		t.Fatalf("task became terminal while independent actions were active: %+v err=%v", storedTask.Task, err)
	}
	if err := handler.Handle(ctx, jobs["not-started"]); err != nil {
		t.Fatalf("independent action did not continue after peer failure: %v", err)
	}
	if err := handler.Handle(ctx, jobs["active"]); err != nil {
		t.Fatalf("started action did not finish after peer failure: %v", err)
	}

	actions, err := repositories.Executions().ListActions(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	statuses := make(map[asset.AssetID]execution.ActionStatus, len(actions))
	for _, action := range actions {
		statuses[action.AssetID] = action.Status
	}
	if len(actions) != 3 ||
		statuses["active"] != execution.ActionSucceeded ||
		statuses["failed"] != execution.ActionFailed ||
		statuses["not-started"] != execution.ActionSucceeded {
		t.Fatalf("actions=%+v", actions)
	}
	if notStartedDriver.executeCalls != 1 {
		t.Fatalf("independent provider calls=%d, want 1", notStartedDriver.executeCalls)
	}
	storedExecution, err = repositories.Executions().GetExecution(ctx, created.ID)
	if err != nil || storedExecution.Status != execution.ExecutionFailed {
		t.Fatalf("execution=%+v err=%v", storedExecution, err)
	}
	storedTask, err = repositories.CleanupTasks().GetTask(ctx, "cln-failure-isolation")
	if err != nil || storedTask.Task.Status != plan.StatusFailed {
		t.Fatalf("task=%+v err=%v", storedTask.Task, err)
	}

	for target, job := range jobs {
		// Keep the failed action's original job running to cover the worker
		// completion race: continuing must enqueue a replacement even though
		// that terminal action's old job still holds its lease.
		if target == "failed" {
			continue
		}
		job.Status = execution.JobSucceeded
		job.LeaseOwner = ""
		job.LeaseUntil = nil
		job.UpdatedAt = now
		job.FinishedAt = &now
		if err := repositories.Jobs().UpdateJob(ctx, job); err != nil {
			t.Fatal(err)
		}
	}
	continued, err := planner.ContinueExecution(ctx, cleanup.ContinueExecutionRequest{
		CleanupTaskID: "cln-failure-isolation", RequestedBy: "operator",
		IdempotencyKey: "failure-isolation-continue",
	})
	if err != nil {
		t.Fatalf("continue failed execution: %v", err)
	}
	if continued.ID != created.ID || continued.Status != execution.ExecutionRunning || continued.ContinueCount != 1 {
		t.Fatalf("continued execution=%+v", continued)
	}
	actions, err = repositories.Executions().ListActions(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	statuses = make(map[asset.AssetID]execution.ActionStatus, len(actions))
	for _, action := range actions {
		statuses[action.AssetID] = action.Status
	}
	if statuses["active"] != execution.ActionSucceeded || statuses["failed"] != execution.ActionPending {
		t.Fatalf("continued actions=%+v", actions)
	}

	retryJob := claimExecutionJob(t, repositories, now)
	if retryJob.TargetKey != "failed" || retryJob.RetryGeneration != 1 {
		t.Fatalf("continued job=%+v", retryJob)
	}
	if _, err := planner.ContinueExecution(ctx, cleanup.ContinueExecutionRequest{
		CleanupTaskID: "cln-failure-isolation", RequestedBy: "operator",
		IdempotencyKey: "failure-isolation-continue",
	}); err != nil {
		t.Fatalf("idempotent continue: %v", err)
	}
	if _, err := repositories.Jobs().ClaimNext(ctx, "worker", now, time.Minute); !errors.Is(err, persistence.ErrNotFound) {
		t.Fatalf("idempotent continue enqueued duplicate jobs: %v", err)
	}

	if err := handler.Handle(ctx, retryJob); err != nil {
		t.Fatalf("retried failed action: %v", err)
	}
	storedExecution, err = repositories.Executions().GetExecution(ctx, created.ID)
	if err != nil || storedExecution.Status != execution.ExecutionSucceeded {
		t.Fatalf("completed continued execution=%+v err=%v", storedExecution, err)
	}
	storedTask, err = repositories.CleanupTasks().GetTask(ctx, "cln-failure-isolation")
	if err != nil || storedTask.Task.Status != plan.StatusCompleted {
		t.Fatalf("completed continued task=%+v err=%v", storedTask.Task, err)
	}
	if activeDriver.executeCalls != 1 {
		t.Fatalf("completed action repeated provider call: %d", activeDriver.executeCalls)
	}
	if notStartedDriver.executeCalls != 1 {
		t.Fatalf("independent completed action repeated provider call: %d", notStartedDriver.executeCalls)
	}
}

func TestContinueExecutionClearsFailedControllerImpacts(t *testing.T) {
	ctx := context.Background()
	repositories, planner, created, _ := controllerExecutionFixture(t, "execution-controller-continue", []controllerChild{
		{id: "ecs", ownership: graph.OwnershipExclusive, policy: graph.CleanupDelegate},
	})
	driver := &scriptedActionDriver{
		executeErrors: []error{&contracts.ProviderCallError{Provider: execution.ProviderError{
			Category: execution.ErrorUnsupported,
			Code:     "UnsupportedOperation",
			Message:  "the provider rejected the first request",
		}}},
		readback: contracts.ReadbackResult{Exists: false, State: "absent"},
	}
	handler := cleanup.NewExecutionHandler(
		planner,
		cleanup.ActionResolverFunc(func(context.Context, asset.Asset) (cleanup.ActionDriver, error) {
			return driver, nil
		}),
	)
	originalJob := cleanupExecutionJobForAsset(t, repositories, "cln-controller", "ack")
	if err := handler.Handle(ctx, originalJob); err != nil {
		t.Fatalf("failed controller action returned infrastructure error: %v", err)
	}
	aggregate, err := repositories.CleanupTasks().GetTask(ctx, "cln-controller")
	if err != nil || len(aggregate.ImpactItems) != 1 ||
		aggregate.ImpactItems[0].Result != plan.ImpactCleanupFailed {
		t.Fatalf("failed impacts=%+v err=%v", aggregate.ImpactItems, err)
	}

	if _, err := planner.ContinueExecution(ctx, cleanup.ContinueExecutionRequest{
		CleanupTaskID: "cln-controller", RequestedBy: "operator",
		IdempotencyKey: "controller-continue",
	}); err != nil {
		t.Fatal(err)
	}
	aggregate, err = repositories.CleanupTasks().GetTask(ctx, "cln-controller")
	if err != nil || aggregate.ImpactItems[0].Result != "" {
		t.Fatalf("continued impacts=%+v err=%v", aggregate.ImpactItems, err)
	}

	retryJob := cleanupExecutionJobForAsset(t, repositories, "cln-controller", "ack")
	if err := handler.Handle(ctx, retryJob); err != nil {
		t.Fatalf("continued controller action: %v", err)
	}
	verificationJob := cleanupExecutionJobForAsset(t, repositories, "cln-controller", "ecs")
	if err := handler.Handle(ctx, verificationJob); err != nil {
		t.Fatalf("continued managed-resource verification: %v", err)
	}
	stored, err := repositories.Executions().GetExecution(ctx, created.ID)
	if err != nil || stored.Status != execution.ExecutionSucceeded {
		t.Fatalf("continued execution=%+v err=%v", stored, err)
	}
}

func TestContinueExecutionWakesPendingManagedVerification(t *testing.T) {
	ctx := context.Background()
	repositories, planner, created, now := controllerExecutionFixture(
		t,
		"execution-pending-managed-verification",
		[]controllerChild{{
			id:             "nat-ip",
			ownership:      graph.OwnershipExclusive,
			policy:         graph.CleanupDelegate,
			waitForAbsence: true,
		}},
	)
	controllerDriver := &scriptedActionDriver{
		readback: contracts.ReadbackResult{Exists: false, State: "absent"},
	}
	handler := cleanup.NewExecutionHandler(
		planner,
		cleanup.ActionResolverFunc(func(
			_ context.Context,
			value asset.Asset,
		) (cleanup.ActionDriver, error) {
			if value.ID != "ack" {
				return nil, fmt.Errorf("unexpected verification before continuation for %s", value.ID)
			}
			return controllerDriver, nil
		}),
	)
	controllerJob := cleanupExecutionJobForAsset(t, repositories, "cln-controller", "ack")
	if err := handler.Handle(ctx, controllerJob); err != nil {
		t.Fatalf("controller cleanup: %v", err)
	}
	verificationJob := cleanupExecutionJobForAsset(
		t,
		repositories,
		"cln-controller",
		"nat-ip",
	)
	verificationJob.RunAt = now.Add(time.Hour)
	verificationJob.LastError = "cleanup dependency had not completed"
	verificationJob.UpdatedAt = now
	if err := repositories.Jobs().UpdateJob(ctx, verificationJob); err != nil {
		t.Fatal(err)
	}
	finishedAt := now
	created.Status = execution.ExecutionFailed
	created.FailureReason = "a peer cleanup action failed"
	created.FinishedAt = &finishedAt
	if err := repositories.Executions().UpdateExecution(ctx, created); err != nil {
		t.Fatal(err)
	}
	aggregate, err := repositories.CleanupTasks().GetTask(ctx, "cln-controller")
	if err != nil {
		t.Fatal(err)
	}
	aggregate.Task.Status = plan.StatusFailed
	if err := repositories.CleanupTasks().UpdateTask(ctx, aggregate.Task); err != nil {
		t.Fatal(err)
	}

	if _, err := planner.ContinueExecution(ctx, cleanup.ContinueExecutionRequest{
		CleanupTaskID:  "cln-controller",
		RequestedBy:    "operator",
		IdempotencyKey: "wake-pending-managed-verification",
	}); err != nil {
		t.Fatalf("continue cleanup: %v", err)
	}
	jobs, err := repositories.Jobs().ListJobsByAggregate(
		ctx,
		"cleanup_task",
		"cln-controller",
	)
	if err != nil {
		t.Fatal(err)
	}
	step := cleanupTaskStepForAsset(aggregate.Steps, "nat-ip")
	var resumed execution.Job
	for _, job := range jobs {
		payloadStepID, _ := job.Payload["cleanup_task_step_id"].(string)
		if strings.TrimSpace(payloadStepID) == string(step.ID) &&
			job.Status == execution.JobPending {
			resumed = job
			break
		}
	}
	if resumed.ID == "" ||
		!resumed.RunAt.Equal(now) ||
		resumed.LastError != "" {
		t.Fatalf("resumed managed verification job=%+v", resumed)
	}
}

func TestControllerIntegratedSystemRouteTableClosesWithController(t *testing.T) {
	ctx := context.Background()
	repositories, planner, created, _ := controllerExecutionFixture(
		t,
		"execution-integrated-system-route-table",
		[]controllerChild{{
			id:                   "system-route-table",
			ownership:            graph.OwnershipExclusive,
			policy:               graph.CleanupDelegate,
			controllerIntegrated: true,
		}},
	)
	driver := &scriptedActionDriver{
		readback: contracts.ReadbackResult{Exists: false, State: "absent"},
	}
	handler := cleanup.NewExecutionHandler(
		planner,
		cleanup.ActionResolverFunc(func(
			_ context.Context,
			value asset.Asset,
		) (cleanup.ActionDriver, error) {
			if value.ID != "ack" {
				return nil, fmt.Errorf("unexpected system route table action for %s", value.ID)
			}
			return driver, nil
		}),
	)
	job := cleanupExecutionJobForAsset(t, repositories, "cln-controller", "ack")
	if err := handler.Handle(ctx, job); err != nil {
		t.Fatalf("controller cleanup: %v", err)
	}
	aggregate, err := repositories.CleanupTasks().GetTask(ctx, "cln-controller")
	if err != nil ||
		len(aggregate.Steps) != 1 ||
		len(aggregate.ImpactItems) != 1 ||
		aggregate.ImpactItems[0].Result != plan.ImpactDeletedByController {
		t.Fatalf("controller-integrated cleanup=%+v err=%v", aggregate, err)
	}
	systemRouteTable, err := repositories.Inventory().GetAsset(ctx, "system-route-table")
	if err != nil || systemRouteTable.ClosedAt == nil {
		t.Fatalf("system route table=%+v err=%v", systemRouteTable, err)
	}
	stored, err := repositories.Executions().GetExecution(ctx, created.ID)
	if err != nil || stored.Status != execution.ExecutionSucceeded {
		t.Fatalf("execution=%+v err=%v", stored, err)
	}
	if driver.executeCalls != 1 {
		t.Fatalf("controller provider delete calls=%d, want 1", driver.executeCalls)
	}
}

func TestContinueExecutionRetriesSkippedDNSLookupCheck(t *testing.T) {
	ctx := context.Background()
	repositories, planner, created, now := directExecutionFixture(
		t,
		"execution-dns-lookup-continue",
	)
	driver := &scriptedActionDriver{
		waitErrors: []error{&contracts.ProviderCallError{
			Provider: execution.ProviderError{
				Category: execution.ErrorUnsupported,
				Message: `Post "https://vpc.cn-chengdu.aliyuncs.com/?NatGatewayId=ngw-a": ` +
					`dial tcp: lookup vpc.cn-chengdu.aliyuncs.com: no such host`,
				Summary: map[string]any{
					"skip_reason": string(asset.SkipProviderRegionUnavailable),
				},
			},
		}},
		readback: contracts.ReadbackResult{Exists: false, State: "absent"},
	}
	handler := cleanup.NewExecutionHandler(
		planner,
		cleanup.ActionResolverFunc(func(
			context.Context,
			asset.Asset,
		) (cleanup.ActionDriver, error) {
			return driver, nil
		}),
	)
	job := claimExecutionJob(t, repositories, now)
	if err := handler.Handle(ctx, job); err != nil {
		t.Fatalf("skip DNS lookup check: %v", err)
	}
	actions, err := repositories.Executions().ListActions(ctx, created.ID)
	if err != nil || len(actions) != 1 ||
		actions[0].Status != execution.ActionSkipped ||
		actions[0].SkipReason != string(asset.SkipProviderRegionUnavailable) {
		t.Fatalf("skipped action=%+v err=%v", actions, err)
	}

	storedExecution, err := repositories.Executions().GetExecution(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	storedExecution.Status = execution.ExecutionFailed
	storedExecution.FailureReason = "peer cleanup failed"
	storedExecution.FinishedAt = &now
	if err := repositories.Executions().UpdateExecution(ctx, storedExecution); err != nil {
		t.Fatal(err)
	}
	aggregate, err := repositories.CleanupTasks().GetTask(ctx, "cln-direct")
	if err != nil {
		t.Fatal(err)
	}
	aggregate.Task.Status = plan.StatusFailed
	if err := repositories.CleanupTasks().UpdateTask(ctx, aggregate.Task); err != nil {
		t.Fatal(err)
	}

	if _, err := planner.ContinueExecution(ctx, cleanup.ContinueExecutionRequest{
		CleanupTaskID: "cln-direct", RequestedBy: "operator",
		IdempotencyKey: "dns-lookup-continue",
	}); err != nil {
		t.Fatalf("continue DNS lookup check: %v", err)
	}
	actions, err = repositories.Executions().ListActions(ctx, created.ID)
	if err != nil || len(actions) != 1 ||
		actions[0].Status != execution.ActionPending ||
		actions[0].ResumeStatus != execution.ActionWaiting ||
		actions[0].SkipReason != "" ||
		actions[0].ProviderError != nil {
		t.Fatalf("resumed action=%+v err=%v", actions, err)
	}
	retryJob := claimExecutionJob(t, repositories, now)
	if retryJob.TargetKey != "direct" || retryJob.RetryGeneration != 1 {
		t.Fatalf("retry job=%+v", retryJob)
	}
}

func TestExecutionHandlerPreflightsBeforeDelete(t *testing.T) {
	ctx := context.Background()
	repositories, planner, created, now := directExecutionFixture(t, "execution-preflight")
	driver := &scriptedActionDriver{
		preflightResult: contracts.PreflightResult{
			Allowed:  true,
			Evidence: map[string]any{"provider_request_id": "request-preflight", "state": "Running"},
		},
		readback: contracts.ReadbackResult{Exists: false, State: "absent"},
	}
	handler := cleanup.NewExecutionHandler(planner, cleanup.ActionResolverFunc(func(context.Context, asset.Asset) (cleanup.ActionDriver, error) {
		return driver, nil
	}))

	if err := handler.Handle(ctx, claimExecutionJob(t, repositories, now)); err != nil {
		t.Fatal(err)
	}
	if driver.preflightCalls != 1 || driver.executeCalls != 1 {
		t.Fatalf("preflight calls=%d execute calls=%d", driver.preflightCalls, driver.executeCalls)
	}
	stored, err := repositories.Executions().GetExecution(ctx, created.ID)
	if err != nil || stored.Status != execution.ExecutionSucceeded {
		t.Fatalf("execution=%+v err=%v", stored, err)
	}
	actions, err := repositories.Executions().ListActions(ctx, created.ID)
	if err != nil || len(actions) != 1 ||
		actions[0].PreflightEvidence["state"] != "Running" {
		t.Fatalf("actions=%+v err=%v", actions, err)
	}
}

func TestExecutionHandlerSuppressesDeleteWhenPreflightReportsDeleting(t *testing.T) {
	ctx := context.Background()
	repositories, planner, created, now := directExecutionFixture(t, "execution-preflight-deleting")
	driver := &scriptedActionDriver{preflightResult: contracts.PreflightResult{
		Absent: true,
		Reason: "resource deletion is already in progress",
		Evidence: map[string]any{
			"provider_request_id": "request-preflight-deleting",
			"state":               "Deleting",
		},
	}}
	handler := cleanup.NewExecutionHandler(planner, cleanup.ActionResolverFunc(func(context.Context, asset.Asset) (cleanup.ActionDriver, error) {
		return driver, nil
	}))

	if err := handler.Handle(ctx, claimExecutionJob(t, repositories, now)); err != nil {
		t.Fatal(err)
	}
	if driver.preflightCalls != 1 || driver.executeCalls != 0 ||
		driver.waitCalls != 0 || driver.readbackCalls != 0 {
		t.Fatalf(
			"preflight=%d execute=%d wait=%d readback=%d",
			driver.preflightCalls,
			driver.executeCalls,
			driver.waitCalls,
			driver.readbackCalls,
		)
	}
	actions, err := repositories.Executions().ListActions(ctx, created.ID)
	if err != nil || len(actions) != 1 ||
		actions[0].Status != execution.ActionSucceeded ||
		actions[0].ProviderRequestID != "request-preflight-deleting" ||
		actions[0].PreflightEvidence["state"] != "Deleting" {
		t.Fatalf("actions=%+v err=%v", actions, err)
	}
	closed, err := repositories.Inventory().GetAsset(ctx, "direct")
	if err != nil || closed.ClosedAt == nil || closed.DeletedAt == nil {
		t.Fatalf("closed asset=%+v err=%v", closed, err)
	}
}

func TestExecutionHandlerFailsRejectedPreflightWithoutDeleting(t *testing.T) {
	ctx := context.Background()
	repositories, planner, created, now := directExecutionFixture(t, "execution-preflight-rejected")
	driver := &scriptedActionDriver{preflightResult: contracts.PreflightResult{
		Allowed: false,
		Reason:  "resource is protected",
		Evidence: map[string]any{
			"provider_request_id": "request-preflight-rejected",
			"state":               "Running",
		},
	}}
	handler := cleanup.NewExecutionHandler(planner, cleanup.ActionResolverFunc(func(context.Context, asset.Asset) (cleanup.ActionDriver, error) {
		return driver, nil
	}))

	if err := handler.Handle(ctx, claimExecutionJob(t, repositories, now)); err != nil {
		t.Fatal(err)
	}
	if driver.preflightCalls != 1 || driver.executeCalls != 0 {
		t.Fatalf("preflight=%d execute=%d", driver.preflightCalls, driver.executeCalls)
	}
	actions, err := repositories.Executions().ListActions(ctx, created.ID)
	if err != nil || len(actions) != 1 ||
		actions[0].Status != execution.ActionFailed ||
		actions[0].ProviderError == nil ||
		actions[0].ProviderError.Code != "CleanupPreflightRejected" {
		t.Fatalf("actions=%+v err=%v", actions, err)
	}
}

func TestExecutionHandlerRetriesTransientProviderCheckTimeout(t *testing.T) {
	ctx := context.Background()
	repositories, planner, created, now := directExecutionFixture(t, "execution-check-retry")
	driver := &scriptedActionDriver{
		waitErrors: []error{context.DeadlineExceeded},
		readback:   contracts.ReadbackResult{Exists: false, State: "absent"},
	}
	handler := cleanup.NewExecutionHandler(
		planner,
		cleanup.ActionResolverFunc(func(context.Context, asset.Asset) (cleanup.ActionDriver, error) {
			return driver, nil
		}),
		cleanup.WithDeletionCheckTimeout(time.Minute),
	)
	job := claimExecutionJob(t, repositories, now)

	var retry *cleanup.RetryError
	if err := handler.Handle(ctx, job); !errors.As(err, &retry) {
		t.Fatalf("first check error = %v, want retry", err)
	}
	actions, err := repositories.Executions().ListActions(ctx, created.ID)
	if err != nil || len(actions) != 1 || actions[0].Status != execution.ActionWaiting ||
		actions[0].DeletionCheckStartedAt == nil || actions[0].ProviderError == nil ||
		actions[0].ProviderError.Category != execution.ErrorRetryable {
		t.Fatalf("waiting action=%+v err=%v", actions, err)
	}

	if err := handler.Handle(ctx, job); err != nil {
		t.Fatal(err)
	}
	actions, err = repositories.Executions().ListActions(ctx, created.ID)
	if err != nil || actions[0].Status != execution.ActionSucceeded || actions[0].ProviderError != nil {
		t.Fatalf("completed action=%+v err=%v", actions, err)
	}
	if !driver.waitHadDeadline || !driver.readbackHadDeadline {
		t.Fatalf("check deadlines: wait=%t readback=%t", driver.waitHadDeadline, driver.readbackHadDeadline)
	}
}

func TestDefaultDeletionCheckTimeoutIsTwentySeconds(t *testing.T) {
	if cleanup.DefaultDeletionCheckTimeout != 20*time.Second {
		t.Fatalf(
			"default deletion check timeout = %s, want 20s",
			cleanup.DefaultDeletionCheckTimeout,
		)
	}
}

func TestExecutionHandlerFailsDeletionCheckAtResourceTimeout(t *testing.T) {
	ctx := context.Background()
	repositories, planner, created, now := directExecutionFixture(t, "execution-check-timeout")
	driver := &scriptedActionDriver{
		waitResults: []contracts.WaitResult{{Done: false, State: "Normal", RetryAfter: time.Second}},
	}
	handler := cleanup.NewExecutionHandler(
		planner,
		cleanup.ActionResolverFunc(func(context.Context, asset.Asset) (cleanup.ActionDriver, error) {
			return driver, nil
		}),
		cleanup.WithDeletionCheckTimeout(time.Minute),
	)
	job := claimExecutionJob(t, repositories, now)

	var retry *cleanup.RetryError
	if err := handler.Handle(ctx, job); !errors.As(err, &retry) {
		t.Fatalf("first check error = %v, want retry", err)
	}
	actions, err := repositories.Executions().ListActions(ctx, created.ID)
	if err != nil || len(actions) != 1 || actions[0].DeletionCheckStartedAt == nil {
		t.Fatalf("waiting action=%+v err=%v", actions, err)
	}
	timedOutAt := now.Add(-time.Minute)
	// Older persisted actions have no explicit deletion-check timestamp. Their
	// last transition time remains the durable start of the timeout window.
	actions[0].DeletionCheckStartedAt = nil
	actions[0].UpdatedAt = timedOutAt
	if err := repositories.Executions().UpdateAction(ctx, actions[0]); err != nil {
		t.Fatal(err)
	}

	if err := handler.Handle(ctx, job); err != nil {
		t.Fatalf("timeout returned infrastructure error: %v", err)
	}
	actions, err = repositories.Executions().ListActions(ctx, created.ID)
	if err != nil || actions[0].Status != execution.ActionFailed || actions[0].ProviderError == nil ||
		actions[0].ProviderError.Code != "DeletionCheckTimeout" ||
		actions[0].ProviderError.Summary["last_state"] != "Normal" {
		t.Fatalf("timed out action=%+v err=%v", actions, err)
	}
	if driver.waitCalls != 1 {
		t.Fatalf("provider checks=%d, want no check after timeout", driver.waitCalls)
	}
	storedExecution, err := repositories.Executions().GetExecution(ctx, created.ID)
	if err != nil || storedExecution.Status != execution.ExecutionFailed {
		t.Fatalf("execution=%+v err=%v", storedExecution, err)
	}
	storedTask, err := repositories.CleanupTasks().GetTask(ctx, "cln-direct")
	if err != nil || storedTask.Task.Status != plan.StatusFailed {
		t.Fatalf("task=%+v err=%v", storedTask.Task, err)
	}
}

func TestExecutionHandlerUsesProviderDeletionCheckTimeout(t *testing.T) {
	ctx := context.Background()
	repositories, planner, created, now := directExecutionFixture(t, "execution-provider-check-timeout")
	driver := &scriptedActionDriver{
		deletionCheckTimeout: 30 * time.Minute,
		waitResults: []contracts.WaitResult{
			{Done: false, State: "DELETE_IN_PROGRESS", RetryAfter: time.Second},
			{Done: false, State: "DELETE_IN_PROGRESS", RetryAfter: time.Second},
		},
	}
	handler := cleanup.NewExecutionHandler(
		planner,
		cleanup.ActionResolverFunc(func(context.Context, asset.Asset) (cleanup.ActionDriver, error) {
			return driver, nil
		}),
	)
	job := claimExecutionJob(t, repositories, now)

	var retry *cleanup.RetryError
	if err := handler.Handle(ctx, job); !errors.As(err, &retry) {
		t.Fatalf("first check error = %v, want retry", err)
	}
	actions, err := repositories.Executions().ListActions(ctx, created.ID)
	if err != nil || len(actions) != 1 {
		t.Fatalf("waiting actions=%+v err=%v", actions, err)
	}

	startedAt := now.Add(-15 * time.Second)
	actions[0].DeletionCheckStartedAt = &startedAt
	if err := repositories.Executions().UpdateAction(ctx, actions[0]); err != nil {
		t.Fatal(err)
	}
	if err := handler.Handle(ctx, job); !errors.As(err, &retry) {
		t.Fatalf("check after default timeout error = %v, want provider-timeout retry", err)
	}

	startedAt = now.Add(-30 * time.Minute)
	actions, err = repositories.Executions().ListActions(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	actions[0].DeletionCheckStartedAt = &startedAt
	if err := repositories.Executions().UpdateAction(ctx, actions[0]); err != nil {
		t.Fatal(err)
	}
	if err := handler.Handle(ctx, job); err != nil {
		t.Fatalf("provider timeout returned infrastructure error: %v", err)
	}
	actions, err = repositories.Executions().ListActions(ctx, created.ID)
	if err != nil || actions[0].Status != execution.ActionFailed || actions[0].ProviderError == nil ||
		actions[0].ProviderError.Code != "DeletionCheckTimeout" ||
		actions[0].ProviderError.Summary["timeout_seconds"] != float64(1800) {
		t.Fatalf("timed out action=%+v err=%v", actions, err)
	}
	if driver.waitCalls != 2 {
		t.Fatalf("provider checks=%d, want no check after provider timeout", driver.waitCalls)
	}
}

func TestExecutionHandlerSkipsUnsupportedProviderAction(t *testing.T) {
	ctx := context.Background()
	repositories, planner, created, now := directExecutionFixture(t, "execution-unsupported")
	driver := &scriptedActionDriver{
		executeErrors: []error{&contracts.ProviderCallError{Provider: execution.ProviderError{
			Category: execution.ErrorUnsupported,
			Code:     "CleanupUnsupported.CloudProductPrometheus",
			Message:  "cloud-product Prometheus instances cannot be deleted directly",
			Summary: map[string]any{
				"operation":   "AlibabaCloud.DeletePrometheusInstance",
				"skip_reason": string(asset.SkipProductUnsupported),
			},
		}}},
	}
	handler := cleanup.NewExecutionHandler(planner, cleanup.ActionResolverFunc(func(context.Context, asset.Asset) (cleanup.ActionDriver, error) {
		return driver, nil
	}))

	if err := handler.Handle(ctx, claimExecutionJob(t, repositories, now)); err != nil {
		t.Fatal(err)
	}
	stored, err := repositories.Executions().GetExecution(ctx, created.ID)
	if err != nil || stored.Status != execution.ExecutionSucceeded || stored.FailureReason != "" {
		t.Fatalf("execution=%+v err=%v", stored, err)
	}
	actions, err := repositories.Executions().ListActions(ctx, created.ID)
	if err != nil || len(actions) != 1 ||
		actions[0].Status != execution.ActionSkipped ||
		actions[0].SkipReason != string(asset.SkipProductUnsupported) ||
		actions[0].ProviderError == nil ||
		actions[0].ProviderError.Code != "CleanupUnsupported.CloudProductPrometheus" {
		t.Fatalf("actions=%+v err=%v", actions, err)
	}
}

func TestExecutionHandlerNeverRetriesDeleteForNATManagedSecurityGroup(t *testing.T) {
	ctx := context.Background()
	repositories, planner, created, now := directExecutionFixture(
		t,
		"execution-nat-managed-security-group",
	)
	driver := &scriptedActionDriver{
		executeErrors: []error{&contracts.ProviderCallError{
			Provider: execution.ProviderError{
				Category: execution.ErrorUnsupported,
				Code:     "InvalidOperation.ResourceManagedByCloudProduct",
				Message:  `SecurityGroup "sg-a" is managed by product "natgw".`,
				Summary: map[string]any{
					"skip_reason": "delegated_to_nat_gateway",
				},
			},
		}},
		readback: contracts.ReadbackResult{Exists: true, State: "Available"},
	}
	handler := cleanup.NewExecutionHandler(
		planner,
		cleanup.ActionResolverFunc(func(
			context.Context,
			asset.Asset,
		) (cleanup.ActionDriver, error) {
			return driver, nil
		}),
	)

	job := claimExecutionJob(t, repositories, now)
	var retry *cleanup.RetryError
	if err := handler.Handle(ctx, job); !errors.As(err, &retry) {
		t.Fatalf("managed provider rejection should enter readback: %v", err)
	}
	actions, err := repositories.Executions().ListActions(ctx, created.ID)
	if err != nil || len(actions) != 1 ||
		actions[0].Status != execution.ActionReadingBack ||
		actions[0].Request[plan.ManagedVerificationOnlyRequestKey] != true {
		t.Fatalf("actions=%+v err=%v", actions, err)
	}
	if err := handler.Handle(ctx, job); !errors.As(err, &retry) {
		t.Fatalf("present managed resource should remain under observation: %v", err)
	}
	actions, err = repositories.Executions().ListActions(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	expiredAt := now.Add(-3 * time.Minute)
	actions[0].DeletionCheckStartedAt = &expiredAt
	if err := repositories.Executions().UpdateAction(ctx, actions[0]); err != nil {
		t.Fatal(err)
	}
	if err := handler.Handle(ctx, job); err != nil {
		t.Fatalf("expired verification returned infrastructure error: %v", err)
	}
	stored, err := repositories.Executions().GetExecution(ctx, created.ID)
	if err != nil || stored.Status != execution.ExecutionFailed {
		t.Fatalf("failed verification execution=%+v err=%v", stored, err)
	}
	if _, err := planner.ContinueExecution(ctx, cleanup.ContinueExecutionRequest{
		CleanupTaskID: "cln-direct", RequestedBy: "operator",
		IdempotencyKey: "nat-managed-readback-retry",
	}); err != nil {
		t.Fatal(err)
	}
	actions, err = repositories.Executions().ListActions(ctx, created.ID)
	if err != nil || actions[0].Status != execution.ActionPending ||
		actions[0].ResumeStatus != execution.ActionReadingBack ||
		actions[0].Request[plan.ManagedVerificationRetryOnceRequestKey] != true {
		t.Fatalf("continued action=%+v err=%v", actions, err)
	}
	driver.readback = contracts.ReadbackResult{Exists: false, State: "absent"}
	if err := handler.Handle(ctx, job); err != nil {
		t.Fatalf("one-shot managed readback retry: %v", err)
	}
	if driver.executeCalls != 1 || driver.readbackCalls != 3 {
		t.Fatalf(
			"provider calls: delete=%d readback=%d, want one legacy delete and readback-only retries",
			driver.executeCalls,
			driver.readbackCalls,
		)
	}
}

func TestExecutionHandlerTreatsDeleteNotFoundRaceAsSuccess(t *testing.T) {
	ctx := context.Background()
	repositories, planner, created, now := directExecutionFixture(t, "execution-delete-not-found")
	driver := &scriptedActionDriver{
		executeErrors: []error{&contracts.ProviderCallError{Provider: execution.ProviderError{
			Category:  execution.ErrorNotFound,
			Code:      "ProjectNotExist",
			Message:   "resource does not exist",
			RequestID: "request-not-found",
			Summary: map[string]any{
				"operation": "AlibabaCloud.SLS.DeleteProject",
			},
		}}},
	}
	handler := cleanup.NewExecutionHandler(planner, cleanup.ActionResolverFunc(func(context.Context, asset.Asset) (cleanup.ActionDriver, error) {
		return driver, nil
	}))

	if err := handler.Handle(ctx, claimExecutionJob(t, repositories, now)); err != nil {
		t.Fatal(err)
	}
	if driver.preflightCalls != 1 || driver.executeCalls != 1 ||
		driver.waitCalls != 0 || driver.readbackCalls != 0 {
		t.Fatalf(
			"preflight=%d execute=%d wait=%d readback=%d",
			driver.preflightCalls,
			driver.executeCalls,
			driver.waitCalls,
			driver.readbackCalls,
		)
	}
	actions, err := repositories.Executions().ListActions(ctx, created.ID)
	if err != nil || len(actions) != 1 ||
		actions[0].Status != execution.ActionSucceeded ||
		actions[0].ProviderError != nil ||
		actions[0].ProviderRequestID != "request-not-found" {
		t.Fatalf("actions=%+v err=%v", actions, err)
	}
	closed, err := repositories.Inventory().GetAsset(ctx, "direct")
	if err != nil || closed.ClosedAt == nil || closed.DeletedAt == nil {
		t.Fatalf("closed asset=%+v err=%v", closed, err)
	}
}

func TestExecutionHandlerTreatsIncorrectVSwitchIDAsIdempotentSuccess(t *testing.T) {
	ctx := context.Background()
	repositories, planner, created, now := directExecutionFixture(t, "execution-vswitch-incorrect-id")
	driver := &scriptedActionDriver{
		executeErrors: []error{&contracts.ProviderCallError{Provider: execution.ProviderError{
			Category:  execution.ErrorInvalidRequest,
			Code:      "IncorrectVSwitchId",
			Message:   "The specified VSwitchId is incorrect.",
			RequestID: "request-vswitch-absent",
			Summary: map[string]any{
				"operation": "AlibabaCloud.DeleteVSwitch",
			},
		}}},
	}
	handler := cleanup.NewExecutionHandler(planner, cleanup.ActionResolverFunc(func(context.Context, asset.Asset) (cleanup.ActionDriver, error) {
		return driver, nil
	}))

	if err := handler.Handle(ctx, claimExecutionJob(t, repositories, now)); err != nil {
		t.Fatal(err)
	}
	if driver.executeCalls != 1 || driver.waitCalls != 0 || driver.readbackCalls != 0 {
		t.Fatalf("execute=%d wait=%d readback=%d", driver.executeCalls, driver.waitCalls, driver.readbackCalls)
	}
	if driver.preflightCalls != 1 {
		t.Fatalf("preflight=%d", driver.preflightCalls)
	}
	actions, err := repositories.Executions().ListActions(ctx, created.ID)
	if err != nil || len(actions) != 1 ||
		actions[0].Status != execution.ActionSucceeded ||
		actions[0].ProviderError != nil ||
		actions[0].ProviderRequestID != "request-vswitch-absent" {
		t.Fatalf("actions=%+v err=%v", actions, err)
	}
}

func TestExecutionHandlerMarksVPCPeerDependencyRejectionAsFailed(t *testing.T) {
	ctx := context.Background()
	repositories, planner, created, now := directExecutionFixture(
		t,
		"execution-vpc-peer-dependency",
	)
	driver := &scriptedActionDriver{
		executeErrors: []error{&contracts.ProviderCallError{Provider: execution.ProviderError{
			Category:  execution.ErrorDependencyViolation,
			Code:      "OperationDenied.VpcPeerExists",
			Message:   "The operation is not allowed because the VpcPeer exists.",
			RequestID: "request-vpc-peer-exists",
			Summary: map[string]any{
				"operation": "AlibabaCloud.DeleteVpc",
			},
		}}},
	}
	handler := cleanup.NewExecutionHandler(
		planner,
		cleanup.ActionResolverFunc(func(
			context.Context,
			asset.Asset,
		) (cleanup.ActionDriver, error) {
			return driver, nil
		}),
	)

	if err := handler.Handle(ctx, claimExecutionJob(t, repositories, now)); err != nil {
		t.Fatal(err)
	}
	actions, err := repositories.Executions().ListActions(ctx, created.ID)
	if err != nil || len(actions) != 1 ||
		actions[0].Status != execution.ActionFailed ||
		actions[0].SkipReason != "" ||
		actions[0].ProviderError == nil ||
		actions[0].ProviderError.Code != "OperationDenied.VpcPeerExists" {
		t.Fatalf("actions=%+v err=%v", actions, err)
	}
	stored, err := repositories.Executions().GetExecution(ctx, created.ID)
	if err != nil || stored.Status != execution.ExecutionFailed {
		t.Fatalf("execution=%+v err=%v", stored, err)
	}
}

func TestExecutionHandlerMarksVPCGatewayEndpointDependencyRejectionAsFailed(t *testing.T) {
	ctx := context.Background()
	repositories, planner, created, now := directExecutionFixture(
		t,
		"execution-vpc-gateway-endpoint-dependency",
	)
	driver := &scriptedActionDriver{
		executeErrors: []error{&contracts.ProviderCallError{Provider: execution.ProviderError{
			Category:  execution.ErrorDependencyViolation,
			Code:      "DependencyViolation.GatewayEndpoint",
			Message:   "The VPC contains endpoints and cannot be deleted.",
			RequestID: "request-vpc-gateway-endpoint",
			Summary: map[string]any{
				"operation": "AlibabaCloud.DeleteVpc",
			},
		}}},
	}
	handler := cleanup.NewExecutionHandler(
		planner,
		cleanup.ActionResolverFunc(func(
			context.Context,
			asset.Asset,
		) (cleanup.ActionDriver, error) {
			return driver, nil
		}),
	)

	if err := handler.Handle(ctx, claimExecutionJob(t, repositories, now)); err != nil {
		t.Fatal(err)
	}
	actions, err := repositories.Executions().ListActions(ctx, created.ID)
	if err != nil || len(actions) != 1 ||
		actions[0].Status != execution.ActionFailed ||
		actions[0].SkipReason != "" ||
		actions[0].ProviderError == nil ||
		actions[0].ProviderError.Code != "DependencyViolation.GatewayEndpoint" {
		t.Fatalf("actions=%+v err=%v", actions, err)
	}
	stored, err := repositories.Executions().GetExecution(ctx, created.ID)
	if err != nil || stored.Status != execution.ExecutionFailed {
		t.Fatalf("execution=%+v err=%v", stored, err)
	}
}

func TestExecutionHandlerMarksVPCRouterInterfaceDependencyRejectionAsFailed(t *testing.T) {
	ctx := context.Background()
	repositories, planner, created, now := directExecutionFixture(
		t,
		"execution-vpc-router-interface-dependency",
	)
	driver := &scriptedActionDriver{
		executeErrors: []error{&contracts.ProviderCallError{Provider: execution.ProviderError{
			Category:  execution.ErrorDependencyViolation,
			Code:      "DependencyViolation.RouterInterface",
			Message:   "Specified object has dependent resources RouterInterface.",
			RequestID: "request-vpc-router-interface",
			Summary: map[string]any{
				"operation": "AlibabaCloud.DeleteVpc",
			},
		}}},
	}
	handler := cleanup.NewExecutionHandler(
		planner,
		cleanup.ActionResolverFunc(func(
			context.Context,
			asset.Asset,
		) (cleanup.ActionDriver, error) {
			return driver, nil
		}),
	)

	if err := handler.Handle(ctx, claimExecutionJob(t, repositories, now)); err != nil {
		t.Fatal(err)
	}
	actions, err := repositories.Executions().ListActions(ctx, created.ID)
	if err != nil || len(actions) != 1 ||
		actions[0].Status != execution.ActionFailed ||
		actions[0].SkipReason != "" ||
		actions[0].ProviderError == nil ||
		actions[0].ProviderError.Code != "DependencyViolation.RouterInterface" {
		t.Fatalf("actions=%+v err=%v", actions, err)
	}
	stored, err := repositories.Executions().GetExecution(ctx, created.ID)
	if err != nil || stored.Status != execution.ExecutionFailed {
		t.Fatalf("execution=%+v err=%v", stored, err)
	}
}

func TestExecutionHandlerCompletesScheduledDeletionWithoutClosingAsset(t *testing.T) {
	ctx := context.Background()
	repositories, planner, created, now := directExecutionFixture(t, "execution-scheduled-deletion")
	driver := &scriptedActionDriver{readback: contracts.ReadbackResult{
		Exists: false,
		State:  "scheduled_deletion",
		Data: map[string]any{
			"pending_window_days":   7,
			"scheduled_deletion_at": "2026-08-13T11:54:34Z",
		},
	}}
	handler := cleanup.NewExecutionHandler(planner, cleanup.ActionResolverFunc(func(context.Context, asset.Asset) (cleanup.ActionDriver, error) {
		return driver, nil
	}))

	if err := handler.Handle(ctx, claimExecutionJob(t, repositories, now)); err != nil {
		t.Fatal(err)
	}
	actions, err := repositories.Executions().ListActions(ctx, created.ID)
	var readbackData map[string]any
	if len(actions) == 1 {
		readbackData, _ = actions[0].Readback["data"].(map[string]any)
	}
	if err != nil || len(actions) != 1 ||
		actions[0].Status != execution.ActionSucceeded ||
		actions[0].Readback["state"] != "scheduled_deletion" ||
		readbackData["scheduled_deletion_at"] !=
			"2026-08-13T11:54:34Z" {
		t.Fatalf("actions=%+v err=%v", actions, err)
	}
	storedExecution, err := repositories.Executions().GetExecution(ctx, created.ID)
	if err != nil || storedExecution.Status != execution.ExecutionSucceeded {
		t.Fatalf("execution=%+v err=%v", storedExecution, err)
	}
	storedAsset, err := repositories.Inventory().GetAsset(ctx, "direct")
	if err != nil || storedAsset.ClosedAt != nil || storedAsset.DeletedAt != nil {
		t.Fatalf("scheduled key asset was closed before its deletion window elapsed: %+v err=%v", storedAsset, err)
	}
}

func TestExecutionHandlerTreatsMaxComputeInvalidProjectAsIdempotentSuccess(t *testing.T) {
	ctx := context.Background()
	repositories, planner, created, now := directExecutionFixture(t, "execution-maxcompute-invalid-project")
	driver := &scriptedActionDriver{
		executeErrors: []error{&contracts.ProviderCallError{Provider: execution.ProviderError{
			Category:  execution.ErrorNotFound,
			Code:      "ODPS-0420061",
			Message:   "Invalid parameter in HTTP request - Invalid project: df_uw1_134014",
			RequestID: "request-maxcompute-not-found",
			Summary: map[string]any{
				"operation": "AlibabaCloud.MaxCompute.DeleteProject",
			},
		}}},
	}
	handler := cleanup.NewExecutionHandler(planner, cleanup.ActionResolverFunc(func(context.Context, asset.Asset) (cleanup.ActionDriver, error) {
		return driver, nil
	}))

	if err := handler.Handle(ctx, claimExecutionJob(t, repositories, now)); err != nil {
		t.Fatal(err)
	}
	if driver.executeCalls != 1 || driver.waitCalls != 0 || driver.readbackCalls != 0 {
		t.Fatalf("execute=%d wait=%d readback=%d", driver.executeCalls, driver.waitCalls, driver.readbackCalls)
	}
	actions, err := repositories.Executions().ListActions(ctx, created.ID)
	if err != nil || len(actions) != 1 ||
		actions[0].Status != execution.ActionSucceeded ||
		actions[0].ProviderError != nil ||
		actions[0].ProviderRequestID != "request-maxcompute-not-found" {
		t.Fatalf("actions=%+v err=%v", actions, err)
	}
}

func TestExecutionHandlerTreatsDeletedDataWorksResourceGroupAsIdempotentSuccess(t *testing.T) {
	ctx := context.Background()
	repositories, planner, created, now := directExecutionFixture(t, "execution-dataworks-resource-group-deleted")
	driver := &scriptedActionDriver{
		executeErrors: []error{&contracts.ProviderCallError{Provider: execution.ProviderError{
			Category:  execution.ErrorNotFound,
			Code:      "704203",
			Message:   "资源组订单释放失败: now resource group status is DELETED, not NORMAL",
			RequestID: "019FD70A-5046-852F-9724-36C106E6F34F",
			Summary: map[string]any{
				"operation": "AlibabaCloud.DataWorks.DeleteResourceGroup",
			},
		}}},
	}
	handler := cleanup.NewExecutionHandler(planner, cleanup.ActionResolverFunc(func(context.Context, asset.Asset) (cleanup.ActionDriver, error) {
		return driver, nil
	}))

	if err := handler.Handle(ctx, claimExecutionJob(t, repositories, now)); err != nil {
		t.Fatal(err)
	}
	if driver.executeCalls != 1 || driver.waitCalls != 0 || driver.readbackCalls != 0 {
		t.Fatalf("execute=%d wait=%d readback=%d", driver.executeCalls, driver.waitCalls, driver.readbackCalls)
	}
	actions, err := repositories.Executions().ListActions(ctx, created.ID)
	if err != nil || len(actions) != 1 ||
		actions[0].Status != execution.ActionSucceeded ||
		actions[0].ProviderError != nil ||
		actions[0].ProviderRequestID != "019FD70A-5046-852F-9724-36C106E6F34F" {
		t.Fatalf("actions=%+v err=%v", actions, err)
	}
}

func TestExecutionHandlerTreatsFCServiceNotFoundAsIdempotentSuccess(t *testing.T) {
	ctx := context.Background()
	repositories, planner, created, now := directExecutionFixture(t, "execution-fc-service-not-found")
	driver := &scriptedActionDriver{
		executeErrors: []error{&contracts.ProviderCallError{Provider: execution.ProviderError{
			Category:  execution.ErrorNotFound,
			Code:      "ServiceNotFound",
			Message:   "service 'svc-42d270g0' does not exist",
			RequestID: "1-6a741c76-12daeaae-4f6364a4eb6a",
			Summary: map[string]any{
				"operation": "AlibabaCloud.FC.DeleteService",
			},
		}}},
	}
	handler := cleanup.NewExecutionHandler(planner, cleanup.ActionResolverFunc(func(context.Context, asset.Asset) (cleanup.ActionDriver, error) {
		return driver, nil
	}))

	if err := handler.Handle(ctx, claimExecutionJob(t, repositories, now)); err != nil {
		t.Fatal(err)
	}
	if driver.preflightCalls != 1 || driver.executeCalls != 1 ||
		driver.waitCalls != 0 || driver.readbackCalls != 0 {
		t.Fatalf(
			"preflight=%d execute=%d wait=%d readback=%d",
			driver.preflightCalls,
			driver.executeCalls,
			driver.waitCalls,
			driver.readbackCalls,
		)
	}
	actions, err := repositories.Executions().ListActions(ctx, created.ID)
	if err != nil || len(actions) != 1 ||
		actions[0].Status != execution.ActionSucceeded ||
		actions[0].ProviderError != nil ||
		actions[0].ProviderRequestID != "1-6a741c76-12daeaae-4f6364a4eb6a" {
		t.Fatalf("actions=%+v err=%v", actions, err)
	}
}

func TestExecutionHandlerTreatsROSStackGroupNotFoundAsIdempotentSuccess(t *testing.T) {
	ctx := context.Background()
	repositories, planner, created, now := directExecutionFixture(t, "execution-stack-group-not-found")
	driver := &scriptedActionDriver{
		executeErrors: []error{&contracts.ProviderCallError{Provider: execution.ProviderError{
			Category:  execution.ErrorNotFound,
			Code:      "StackGroupNotFound",
			Message:   "The StackGroup (test-module) could not be found.",
			RequestID: "request-stack-group-not-found",
			Summary: map[string]any{
				"operation": "AlibabaCloud.ROS.DeleteStackGroup",
			},
		}}},
	}
	handler := cleanup.NewExecutionHandler(planner, cleanup.ActionResolverFunc(func(context.Context, asset.Asset) (cleanup.ActionDriver, error) {
		return driver, nil
	}))

	if err := handler.Handle(ctx, claimExecutionJob(t, repositories, now)); err != nil {
		t.Fatal(err)
	}
	stored, err := repositories.Executions().GetExecution(ctx, created.ID)
	if err != nil || stored.Status != execution.ExecutionSucceeded {
		t.Fatalf("execution=%+v err=%v", stored, err)
	}
	closed, err := repositories.Inventory().GetAsset(ctx, "direct")
	if err != nil || closed.ClosedAt == nil || closed.DeletedAt == nil {
		t.Fatalf("closed asset=%+v err=%v", closed, err)
	}
}

func TestExecutionHandlerTreatsCENPeerAttachmentNotFoundAsIdempotentSuccess(t *testing.T) {
	ctx := context.Background()
	repositories, planner, created, now := directExecutionFixture(t, "execution-cen-peer-not-found")
	driver := &scriptedActionDriver{
		executeErrors: []error{&contracts.ProviderCallError{Provider: execution.ProviderError{
			Category:  execution.ErrorNotFound,
			Code:      "InvalidTransitRouterAttachmentId.NotFound",
			Message:   "TransitRouterAttachmentId is not found.",
			RequestID: "request-cen-peer-not-found",
			Summary: map[string]any{
				"operation": "AlibabaCloud.CEN.DeleteTransitRouterPeerAttachment",
			},
		}}},
	}
	handler := cleanup.NewExecutionHandler(planner, cleanup.ActionResolverFunc(func(context.Context, asset.Asset) (cleanup.ActionDriver, error) {
		return driver, nil
	}))

	if err := handler.Handle(ctx, claimExecutionJob(t, repositories, now)); err != nil {
		t.Fatal(err)
	}
	stored, err := repositories.Executions().GetExecution(ctx, created.ID)
	if err != nil || stored.Status != execution.ExecutionSucceeded {
		t.Fatalf("execution=%+v err=%v", stored, err)
	}
	closed, err := repositories.Inventory().GetAsset(ctx, "direct")
	if err != nil || closed.ClosedAt == nil || closed.DeletedAt == nil {
		t.Fatalf("closed asset=%+v err=%v", closed, err)
	}
}

func TestExecutionHandlerTreatsSLSRecycleBinAsAbsentInEveryProviderPhase(t *testing.T) {
	recycleBinError := func() error {
		return &contracts.ProviderCallError{Provider: execution.ProviderError{
			Category:  execution.ErrorPermissionDenied,
			Code:      "ProjectInRecycleBin",
			Message:   "the project is in recycle bin",
			RequestID: "request-sls-recycle-bin",
			Summary: map[string]any{
				"operation": "AlibabaCloud.SLS.GetProject",
			},
		}}
	}
	for _, test := range []struct {
		name      string
		configure func(*scriptedActionDriver)
	}{
		{name: "invoking", configure: func(driver *scriptedActionDriver) {
			driver.executeErrors = []error{recycleBinError()}
		}},
		{name: "waiting", configure: func(driver *scriptedActionDriver) {
			driver.waitErrors = []error{recycleBinError()}
		}},
		{name: "reading back", configure: func(driver *scriptedActionDriver) {
			driver.readbackErrors = []error{recycleBinError()}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			repositories, planner, created, now := directExecutionFixture(
				t,
				"execution-sls-recycle-bin-"+strings.ReplaceAll(test.name, " ", "-"),
			)
			driver := &scriptedActionDriver{}
			test.configure(driver)
			handler := cleanup.NewExecutionHandler(planner, cleanup.ActionResolverFunc(func(context.Context, asset.Asset) (cleanup.ActionDriver, error) {
				return driver, nil
			}))

			if err := handler.Handle(ctx, claimExecutionJob(t, repositories, now)); err != nil {
				t.Fatal(err)
			}
			stored, err := repositories.Executions().GetExecution(ctx, created.ID)
			if err != nil || stored.Status != execution.ExecutionSucceeded {
				t.Fatalf("execution=%+v err=%v", stored, err)
			}
			closed, err := repositories.Inventory().GetAsset(ctx, "direct")
			if err != nil || closed.ClosedAt == nil || closed.DeletedAt == nil {
				t.Fatalf("closed asset=%+v err=%v", closed, err)
			}
		})
	}
}

func TestExecutionHandlerTreatsEIPAllocationNotFoundAsIdempotentSuccess(t *testing.T) {
	ctx := context.Background()
	repositories, planner, created, now := directExecutionFixture(t, "execution-eip-allocation-not-found")
	driver := &scriptedActionDriver{
		executeErrors: []error{&contracts.ProviderCallError{Provider: execution.ProviderError{
			Category:  execution.ErrorNotFound,
			Code:      "InvalidAllocationId.NotFound",
			Message:   "The specified AllocationId does not exist.",
			RequestID: "request-eip-not-found",
			Summary: map[string]any{
				"operation": "AlibabaCloud.ReleaseEipAddress",
			},
		}}},
	}
	handler := cleanup.NewExecutionHandler(planner, cleanup.ActionResolverFunc(func(context.Context, asset.Asset) (cleanup.ActionDriver, error) {
		return driver, nil
	}))

	if err := handler.Handle(ctx, claimExecutionJob(t, repositories, now)); err != nil {
		t.Fatal(err)
	}
	if driver.preflightCalls != 1 || driver.executeCalls != 1 ||
		driver.waitCalls != 0 || driver.readbackCalls != 0 {
		t.Fatalf(
			"preflight=%d execute=%d wait=%d readback=%d",
			driver.preflightCalls,
			driver.executeCalls,
			driver.waitCalls,
			driver.readbackCalls,
		)
	}
	actions, err := repositories.Executions().ListActions(ctx, created.ID)
	if err != nil || len(actions) != 1 ||
		actions[0].Status != execution.ActionSucceeded ||
		actions[0].ProviderError != nil ||
		actions[0].ProviderRequestID != "request-eip-not-found" {
		t.Fatalf("actions=%+v err=%v", actions, err)
	}
	closed, err := repositories.Inventory().GetAsset(ctx, "direct")
	if err != nil || closed.ClosedAt == nil || closed.DeletedAt == nil {
		t.Fatalf("closed asset=%+v err=%v", closed, err)
	}
}

func TestExecutionHandlerTreatsSecurityGroupNotFoundAsIdempotentSuccess(t *testing.T) {
	ctx := context.Background()
	repositories, planner, created, now := directExecutionFixture(t, "execution-security-group-not-found")
	driver := &scriptedActionDriver{
		executeErrors: []error{&contracts.ProviderCallError{Provider: execution.ProviderError{
			Category:  execution.ErrorNotFound,
			Code:      "InvalidSecurityGroup.NotFound",
			Message:   "The specified security group is not found.",
			RequestID: "request-security-group-not-found",
			Summary: map[string]any{
				"operation": "AlibabaCloud.DeleteSecurityGroup",
			},
		}}},
	}
	handler := cleanup.NewExecutionHandler(planner, cleanup.ActionResolverFunc(func(context.Context, asset.Asset) (cleanup.ActionDriver, error) {
		return driver, nil
	}))

	if err := handler.Handle(ctx, claimExecutionJob(t, repositories, now)); err != nil {
		t.Fatal(err)
	}
	if driver.preflightCalls != 1 || driver.executeCalls != 1 ||
		driver.waitCalls != 0 || driver.readbackCalls != 0 {
		t.Fatalf(
			"preflight=%d execute=%d wait=%d readback=%d",
			driver.preflightCalls,
			driver.executeCalls,
			driver.waitCalls,
			driver.readbackCalls,
		)
	}
	actions, err := repositories.Executions().ListActions(ctx, created.ID)
	if err != nil || len(actions) != 1 ||
		actions[0].Status != execution.ActionSucceeded ||
		actions[0].ProviderError != nil ||
		actions[0].ProviderRequestID != "request-security-group-not-found" {
		t.Fatalf("actions=%+v err=%v", actions, err)
	}
	closed, err := repositories.Inventory().GetAsset(ctx, "direct")
	if err != nil || closed.ClosedAt == nil || closed.DeletedAt == nil {
		t.Fatalf("closed asset=%+v err=%v", closed, err)
	}
}

func TestExecutionHandlerDoesNotIgnoreUnverifiedDeleteNotFoundCode(t *testing.T) {
	ctx := context.Background()
	repositories, planner, created, now := directExecutionFixture(t, "execution-unverified-not-found")
	driver := &scriptedActionDriver{
		executeErrors: []error{&contracts.ProviderCallError{Provider: execution.ProviderError{
			Category: execution.ErrorNotFound,
			Code:     "InventedNotFound",
			Message:  "unverified missing-resource response",
			Summary: map[string]any{
				"operation": "AlibabaCloud.SLS.DeleteProject",
			},
		}}},
	}
	handler := cleanup.NewExecutionHandler(planner, cleanup.ActionResolverFunc(func(context.Context, asset.Asset) (cleanup.ActionDriver, error) {
		return driver, nil
	}))

	if err := handler.Handle(ctx, claimExecutionJob(t, repositories, now)); err != nil {
		t.Fatal(err)
	}
	stored, err := repositories.Executions().GetExecution(ctx, created.ID)
	if err != nil || stored.Status != execution.ExecutionFailed {
		t.Fatalf("execution=%+v err=%v", stored, err)
	}
	closed, err := repositories.Inventory().GetAsset(ctx, "direct")
	if err != nil || closed.ClosedAt != nil || closed.DeletedAt != nil {
		t.Fatalf("asset unexpectedly closed=%+v err=%v", closed, err)
	}
}

func TestExecutionHandlerRecoversProviderSuccessBeforePersistence(t *testing.T) {
	ctx := context.Background()
	repositories, planner, created, now := directExecutionFixture(t, "execution-provider-crash")
	driver := &scriptedActionDriver{readback: contracts.ReadbackResult{Exists: false, State: "absent"}}
	providerCrash := errors.New("crash after provider success")
	handler := cleanup.NewExecutionHandler(planner, cleanup.ActionResolverFunc(func(context.Context, asset.Asset) (cleanup.ActionDriver, error) {
		return driver, nil
	}), cleanup.WithAfterProviderCall(func(execution.ActionAttempt, contracts.ActionResult) error { return providerCrash }))
	job := claimExecutionJob(t, repositories, now)

	if err := handler.Handle(ctx, job); !errors.Is(err, providerCrash) {
		t.Fatalf("first handle error = %v", err)
	}
	actions, err := repositories.Executions().ListActions(ctx, created.ID)
	if err != nil || len(actions) != 1 || actions[0].Status != execution.ActionInvoking || driver.executeCalls != 1 {
		t.Fatalf("actions=%+v executeCalls=%d err=%v", actions, driver.executeCalls, err)
	}

	handler = cleanup.NewExecutionHandler(planner, cleanup.ActionResolverFunc(func(context.Context, asset.Asset) (cleanup.ActionDriver, error) {
		return driver, nil
	}))
	if err := handler.Handle(ctx, job); err != nil {
		t.Fatal(err)
	}
	actions, err = repositories.Executions().ListActions(ctx, created.ID)
	if err != nil || actions[0].Status != execution.ActionSucceeded || driver.executeCalls != 2 || len(driver.idempotencyKeys) != 2 || driver.idempotencyKeys[0] != driver.idempotencyKeys[1] {
		t.Fatalf("actions=%+v calls=%d keys=%v err=%v", actions, driver.executeCalls, driver.idempotencyKeys, err)
	}
}

func TestExecutionHandlerFailsThrottledDeleteWithoutAutomaticRetry(t *testing.T) {
	ctx := context.Background()
	repositories, planner, created, now := directExecutionFixture(t, "execution-throttle")
	driver := &scriptedActionDriver{
		executeErrors: []error{&contracts.ProviderCallError{
			Provider:   execution.ProviderError{Category: execution.ErrorThrottled, Code: "Throttling", Message: "slow down", RequestID: "request-throttled"},
			RetryAfter: 17 * time.Second,
		}},
		readback: contracts.ReadbackResult{Exists: false, State: "absent"},
	}
	handler := cleanup.NewExecutionHandler(planner, cleanup.ActionResolverFunc(func(context.Context, asset.Asset) (cleanup.ActionDriver, error) {
		return driver, nil
	}))
	job := claimExecutionJob(t, repositories, now)

	if err := handler.Handle(ctx, job); err != nil {
		t.Fatalf("throttled delete returned infrastructure error: %v", err)
	}
	actions, getErr := repositories.Executions().ListActions(ctx, created.ID)
	if getErr != nil || len(actions) != 1 || actions[0].Status != execution.ActionFailed || actions[0].ProviderError == nil || actions[0].ProviderError.Category != execution.ErrorThrottled || actions[0].ProviderError.RequestID != "request-throttled" {
		t.Fatalf("actions=%+v err=%v", actions, getErr)
	}
	if err := handler.Handle(ctx, job); err != nil {
		t.Fatal(err)
	}
	if driver.executeCalls != 1 {
		t.Fatalf("delete provider calls=%d, want no automatic retry", driver.executeCalls)
	}
}

func TestExecutionHandlerStopsPersistedInvalidDeleteRetryWithoutAnotherProviderCall(t *testing.T) {
	ctx := context.Background()
	repositories, planner, created, now := directExecutionFixture(t, "execution-invalid-delete")
	aggregate, err := repositories.CleanupTasks().GetTask(ctx, "cln-direct")
	if err != nil || len(aggregate.Steps) != 1 {
		t.Fatalf("cleanup task=%+v err=%v", aggregate, err)
	}
	providerError := execution.ProviderError{
		Category:  execution.ErrorRetryable,
		Code:      "InvalidSecurityGroupId.EniMustHaveSecurityGroup",
		Message:   "The specified network card does not have an associated security group.",
		RequestID: "request-invalid-eni",
	}
	if err := repositories.Executions().AppendAction(ctx, execution.ActionAttempt{
		ID: "action-invalid-delete", ExecutionID: created.ID,
		CleanupTaskStepID: string(aggregate.Steps[0].ID), AssetID: aggregate.Steps[0].AssetID,
		Action: "delete", Status: execution.ActionInvoking,
		IdempotencyKey: "action-invalid-delete", ProviderError: &providerError,
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	driver := &scriptedActionDriver{readback: contracts.ReadbackResult{Exists: false, State: "absent"}}
	handler := cleanup.NewExecutionHandler(
		planner,
		cleanup.ActionResolverFunc(func(context.Context, asset.Asset) (cleanup.ActionDriver, error) {
			return driver, nil
		}),
	)
	job := claimExecutionJob(t, repositories, now)

	if err := handler.Handle(ctx, job); err != nil {
		t.Fatalf("persisted invalid delete returned infrastructure error: %v", err)
	}
	actions, getErr := repositories.Executions().ListActions(ctx, created.ID)
	if getErr != nil || len(actions) != 1 || actions[0].Status != execution.ActionFailed ||
		actions[0].ProviderError == nil || actions[0].ProviderError.RequestID != "request-invalid-eni" {
		t.Fatalf("actions=%+v err=%v", actions, getErr)
	}
	if driver.executeCalls != 0 {
		t.Fatalf("delete provider calls=%d, want existing invalid retry stopped locally", driver.executeCalls)
	}
}

func TestACKControllerCleanupClosesTopologyWithoutSchedulingRescanOrChildDelete(t *testing.T) {
	ctx := context.Background()
	repositories, planner, created, _ := controllerExecutionFixture(t, "execution-ack", []controllerChild{
		{id: "ecs", ownership: graph.OwnershipExclusive, policy: graph.CleanupDelegate},
	})
	driver := &scriptedActionDriver{readback: contracts.ReadbackResult{Exists: false, State: "absent"}}
	handler := cleanup.NewExecutionHandler(planner, cleanup.ActionResolverFunc(func(_ context.Context, value asset.Asset) (cleanup.ActionDriver, error) {
		if value.ID != "ack" {
			return nil, fmt.Errorf("unexpected child action for %s", value.ID)
		}
		return driver, nil
	}))
	job := cleanupExecutionJobForAsset(t, repositories, "cln-controller", "ack")

	if err := handler.Handle(ctx, job); err != nil {
		t.Fatal(err)
	}
	scans, err := repositories.Inventory().ListScanRunsByConnection(ctx, "c-1")
	if err != nil || len(scans) != 0 {
		t.Fatalf("cleanup-created scans = %+v, err=%v", scans, err)
	}
	aggregate, err := repositories.CleanupTasks().GetTask(ctx, "cln-controller")
	if err != nil || len(aggregate.ImpactItems) != 1 || aggregate.ImpactItems[0].Result != plan.ImpactDelegated {
		t.Fatalf("aggregate=%+v err=%v", aggregate, err)
	}
	actions, err := repositories.Executions().ListActions(ctx, created.ID)
	if err != nil || len(actions) != 1 || actions[0].AssetID != "ack" || actions[0].Status != execution.ActionSucceeded {
		t.Fatalf("actions=%+v err=%v", actions, err)
	}
	controller, err := repositories.Inventory().GetAsset(ctx, "ack")
	if err != nil || controller.ClosedAt == nil || controller.DeletedAt == nil {
		t.Fatalf("controller projection = %+v, err=%v", controller, err)
	}
	child, err := repositories.Inventory().GetAsset(ctx, "ecs")
	if err != nil || child.ClosedAt != nil {
		t.Fatalf("unverified delegated child projection = %+v, err=%v", child, err)
	}
	bindings, err := repositories.Graph().ListLifecycleBindings(ctx, "ack")
	if err != nil || len(bindings) != 0 {
		t.Fatalf("closed controller topology bindings = %+v, err=%v", bindings, err)
	}
}

func TestControllerGuaranteedDeleteCompletesWithoutAuthoritativeRescan(t *testing.T) {
	ctx := context.Background()
	repositories, planner, created, _ := controllerExecutionFixture(t, "execution-guaranteed-delete", []controllerChild{
		{id: "vtb-system", ownership: graph.OwnershipExclusive, policy: graph.CleanupDelegate, controllerDeleteGuaranteed: true},
	})
	driver := &scriptedActionDriver{readback: contracts.ReadbackResult{Exists: false, State: "absent"}}
	handler := cleanup.NewExecutionHandler(
		planner,
		cleanup.ActionResolverFunc(func(_ context.Context, value asset.Asset) (cleanup.ActionDriver, error) {
			return driver, nil
		}),
	)

	if err := handler.Handle(ctx, cleanupExecutionJobForAsset(t, repositories, "cln-controller", "ack")); err != nil {
		t.Fatal(err)
	}
	if err := handler.Handle(ctx, cleanupExecutionJobForAsset(t, repositories, "cln-controller", "vtb-system")); err != nil {
		t.Fatal(err)
	}
	aggregate, err := repositories.CleanupTasks().GetTask(ctx, "cln-controller")
	if err != nil || len(aggregate.ImpactItems) != 1 ||
		aggregate.ImpactItems[0].Result != plan.ImpactDeletedByController {
		t.Fatalf("aggregate=%+v err=%v", aggregate, err)
	}
	routeTable, err := repositories.Inventory().GetAsset(ctx, "vtb-system")
	if err != nil || routeTable.ClosedAt == nil || routeTable.DeletedAt == nil {
		t.Fatalf("system route table projection = %+v, err=%v", routeTable, err)
	}
	bindings, err := repositories.Graph().ListLifecycleBindings(ctx, "vtb-system")
	if err != nil || len(bindings) != 0 {
		t.Fatalf("system route table topology bindings = %+v, err=%v", bindings, err)
	}
	storedExecution, err := repositories.Executions().GetExecution(ctx, created.ID)
	if err != nil || storedExecution.Status != execution.ExecutionSucceeded {
		t.Fatalf("execution=%+v err=%v", storedExecution, err)
	}
	if driver.executeCalls != 1 || driver.readbackCalls != 2 {
		t.Fatalf(
			"provider calls: delete=%d readback=%d, managed verification must not delete",
			driver.executeCalls,
			driver.readbackCalls,
		)
	}
}

func TestManagedVerificationMarksStillPresentAndContinueReadsOnce(t *testing.T) {
	ctx := context.Background()
	repositories, planner, created, now := controllerExecutionFixture(
		t,
		"execution-managed-still-present",
		[]controllerChild{{
			id:        "ecs",
			ownership: graph.OwnershipExclusive,
			policy:    graph.CleanupDelegate,
		}},
	)
	controllerDriver := &scriptedActionDriver{
		readback: contracts.ReadbackResult{Exists: false, State: "absent"},
	}
	managedDriver := &scriptedActionDriver{
		readback: contracts.ReadbackResult{Exists: true, State: "Running"},
	}
	handler := cleanup.NewExecutionHandler(
		planner,
		cleanup.ActionResolverFunc(func(
			_ context.Context,
			value asset.Asset,
		) (cleanup.ActionDriver, error) {
			switch value.ID {
			case "ack":
				return controllerDriver, nil
			case "ecs":
				return managedDriver, nil
			default:
				return nil, fmt.Errorf("unexpected cleanup asset %s", value.ID)
			}
		}),
	)
	controllerJob := cleanupExecutionJobForAsset(
		t,
		repositories,
		"cln-controller",
		"ack",
	)
	verificationJob := cleanupExecutionJobForAsset(
		t,
		repositories,
		"cln-controller",
		"ecs",
	)
	if err := handler.Handle(ctx, controllerJob); err != nil {
		t.Fatalf("controller cleanup: %v", err)
	}
	var retry *cleanup.RetryError
	if err := handler.Handle(ctx, verificationJob); !errors.As(err, &retry) {
		t.Fatalf("present managed resource should remain under observation: %v", err)
	}
	aggregate, err := repositories.CleanupTasks().GetTask(ctx, "cln-controller")
	if err != nil || len(aggregate.ImpactItems) != 1 ||
		aggregate.ImpactItems[0].Result != plan.ImpactStillPresent {
		t.Fatalf("managed impact=%+v err=%v", aggregate.ImpactItems, err)
	}
	managedAsset, err := repositories.Inventory().GetAsset(ctx, "ecs")
	if err != nil || managedAsset.ClosedAt != nil {
		t.Fatalf("still-present managed asset=%+v err=%v", managedAsset, err)
	}
	actions, err := repositories.Executions().ListActions(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	var verificationAction execution.ActionAttempt
	for _, action := range actions {
		if action.AssetID == "ecs" {
			verificationAction = action
			break
		}
	}
	if verificationAction.Status != execution.ActionReadingBack {
		t.Fatalf("verification action=%+v", verificationAction)
	}
	expiredAt := now.Add(-3 * time.Minute)
	verificationAction.DeletionCheckStartedAt = &expiredAt
	if err := repositories.Executions().UpdateAction(ctx, verificationAction); err != nil {
		t.Fatal(err)
	}
	if err := handler.Handle(ctx, verificationJob); err != nil {
		t.Fatalf("expired managed verification: %v", err)
	}
	if _, err := planner.ContinueExecution(ctx, cleanup.ContinueExecutionRequest{
		CleanupTaskID: "cln-controller", RequestedBy: "operator",
		IdempotencyKey: "managed-verification-read-once",
	}); err != nil {
		t.Fatal(err)
	}
	actions, err = repositories.Executions().ListActions(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, action := range actions {
		if action.AssetID == "ecs" {
			verificationAction = action
			break
		}
	}
	if verificationAction.Status != execution.ActionPending ||
		verificationAction.ResumeStatus != execution.ActionReadingBack ||
		verificationAction.Request[plan.ManagedVerificationRetryOnceRequestKey] != true {
		t.Fatalf("continued verification action=%+v", verificationAction)
	}
	if err := handler.Handle(ctx, verificationJob); err != nil {
		t.Fatalf("one-shot managed verification retry: %v", err)
	}
	if controllerDriver.executeCalls != 1 ||
		managedDriver.executeCalls != 0 ||
		managedDriver.readbackCalls != 3 {
		t.Fatalf(
			"provider calls: controller_delete=%d managed_delete=%d managed_readback=%d",
			controllerDriver.executeCalls,
			managedDriver.executeCalls,
			managedDriver.readbackCalls,
		)
	}
	actions, err = repositories.Executions().ListActions(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, action := range actions {
		if action.AssetID == "ecs" {
			verificationAction = action
			break
		}
	}
	if verificationAction.Status != execution.ActionFailed ||
		verificationAction.ProviderError == nil ||
		verificationAction.ProviderError.Code != "ManagedResourceStillPresent" {
		t.Fatalf("retried verification action=%+v", verificationAction)
	}
}

func TestManagedImpactsRequireReadbackBeforeDependentCleanup(t *testing.T) {
	ctx := context.Background()
	repositories, planner, created, _ := managedImpactBarrierExecutionFixture(
		t,
		"execution-managed-barrier",
	)
	groupDriver := &scriptedActionDriver{
		readback: contracts.ReadbackResult{Exists: false, State: "absent"},
	}
	vpcDriver := &scriptedActionDriver{
		readback: contracts.ReadbackResult{Exists: false, State: "absent"},
	}
	managedDriver := &scriptedActionDriver{
		readback: contracts.ReadbackResult{Exists: false, State: "absent"},
	}
	handler := cleanup.NewExecutionHandler(
		planner,
		cleanup.ActionResolverFunc(func(_ context.Context, value asset.Asset) (cleanup.ActionDriver, error) {
			switch value.ID {
			case "dataworks-group":
				return groupDriver, nil
			case "managed-eni", "managed-sg":
				return managedDriver, nil
			case "vpc":
				return vpcDriver, nil
			default:
				return nil, fmt.Errorf("unexpected direct action for %s", value.ID)
			}
		}),
	)
	groupJob := cleanupExecutionJobForAsset(t, repositories, "cln-managed-barrier", "dataworks-group")
	eniVerificationJob := cleanupExecutionJobForAsset(
		t,
		repositories,
		"cln-managed-barrier",
		"managed-eni",
	)
	securityGroupVerificationJob := cleanupExecutionJobForAsset(
		t,
		repositories,
		"cln-managed-barrier",
		"managed-sg",
	)
	vpcJob := cleanupExecutionJobForAsset(t, repositories, "cln-managed-barrier", "vpc")

	if err := handler.Handle(ctx, groupJob); err != nil {
		t.Fatalf("resource group cleanup: %v", err)
	}
	actions, err := repositories.Executions().ListActions(ctx, created.ID)
	if err != nil || len(actions) != 1 ||
		actions[0].AssetID != "dataworks-group" ||
		actions[0].Status != execution.ActionSucceeded {
		t.Fatalf("resource group action = %+v, err=%v", actions, err)
	}
	relationships, err := repositories.Graph().ListRelationships(ctx, "dataworks-group")
	if err != nil || len(relationships) != 0 {
		t.Fatalf("closed resource group relationships = %+v, err=%v", relationships, err)
	}
	bindings, err := repositories.Graph().ListLifecycleBindings(ctx, "dataworks-group")
	if err != nil || len(bindings) != 0 {
		t.Fatalf("closed resource group lifecycle bindings = %+v, err=%v", bindings, err)
	}
	aggregate, err := repositories.CleanupTasks().GetTask(ctx, "cln-managed-barrier")
	if err != nil {
		t.Fatal(err)
	}
	for _, impact := range aggregate.ImpactItems {
		if impact.Result != plan.ImpactDelegated {
			t.Fatalf("unverified managed impact = %+v", impact)
		}
	}
	var retry *cleanup.RetryError
	if err := handler.Handle(ctx, vpcJob); !errors.As(err, &retry) {
		t.Fatalf("dependent VPC should wait for managed readback: %v", err)
	}
	if vpcDriver.executeCalls != 0 {
		t.Fatalf("dependent VPC delete calls before managed readback = %d", vpcDriver.executeCalls)
	}
	if err := handler.Handle(ctx, eniVerificationJob); err != nil {
		t.Fatalf("verify managed ENI: %v", err)
	}
	if err := handler.Handle(ctx, securityGroupVerificationJob); err != nil {
		t.Fatalf("verify managed security group: %v", err)
	}
	if err := handler.Handle(ctx, vpcJob); err != nil {
		t.Fatalf("delete dependent VPC: %v", err)
	}
	if groupDriver.executeCalls != 1 ||
		managedDriver.executeCalls != 0 ||
		managedDriver.readbackCalls != 2 ||
		vpcDriver.executeCalls != 1 {
		t.Fatalf(
			"provider calls after barrier: resource-group=%d managed-delete=%d managed-readback=%d vpc=%d",
			groupDriver.executeCalls,
			managedDriver.executeCalls,
			managedDriver.readbackCalls,
			vpcDriver.executeCalls,
		)
	}
	aggregate, err = repositories.CleanupTasks().GetTask(ctx, "cln-managed-barrier")
	if err != nil {
		t.Fatal(err)
	}
	for _, impact := range aggregate.ImpactItems {
		if impact.Result != plan.ImpactDeletedByController {
			t.Fatalf("verified managed impact = %+v", impact)
		}
	}
}

func TestLegacyReconcilingActionCompletesWithoutSchedulingRescan(t *testing.T) {
	ctx := context.Background()
	repositories, planner, created, now := controllerExecutionFixture(t, "execution-legacy-reconciling", []controllerChild{
		{id: "ecs", ownership: graph.OwnershipExclusive, policy: graph.CleanupDelegate},
	})
	driver := &scriptedActionDriver{readback: contracts.ReadbackResult{Exists: false, State: "absent"}}
	handler := cleanup.NewExecutionHandler(
		planner,
		cleanup.ActionResolverFunc(func(context.Context, asset.Asset) (cleanup.ActionDriver, error) {
			return driver, nil
		}),
	)
	job := cleanupExecutionJobForAsset(t, repositories, "cln-controller", "ack")
	action := execution.ActionAttempt{
		ID: "act-legacy-reconciling", ExecutionID: created.ID,
		CleanupTaskStepID: job.Payload["cleanup_task_step_id"].(string),
		AssetID:           "ack", Action: "delete", Status: execution.ActionReconciling,
		IdempotencyKey: "legacy-reconciling", CreatedAt: now, UpdatedAt: now,
	}
	if err := repositories.Executions().AppendAction(ctx, action); err != nil {
		t.Fatal(err)
	}
	if err := handler.Handle(ctx, job); err != nil {
		t.Fatal(err)
	}
	actions, err := repositories.Executions().ListActions(ctx, created.ID)
	if err != nil || len(actions) != 1 || actions[0].Status != execution.ActionSucceeded {
		t.Fatalf("legacy action = %+v, err=%v", actions, err)
	}
}

func TestManagedImpactBarrierDoesNotDelayControllerWithoutDependentStep(t *testing.T) {
	ctx := context.Background()
	repositories, planner, created, _ := controllerExecutionFixture(
		t,
		"execution-managed-barrier-no-dependent",
		[]controllerChild{{
			id:             "ecs",
			ownership:      graph.OwnershipExclusive,
			policy:         graph.CleanupDelegate,
			waitForAbsence: true,
		}},
	)
	driver := &scriptedActionDriver{
		readback: contracts.ReadbackResult{Exists: false, State: "absent"},
	}
	handler := cleanup.NewExecutionHandler(
		planner,
		cleanup.ActionResolverFunc(func(context.Context, asset.Asset) (cleanup.ActionDriver, error) {
			return driver, nil
		}),
	)
	job := cleanupExecutionJobForAsset(t, repositories, "cln-controller", "ack")

	if err := handler.Handle(ctx, job); err != nil {
		t.Fatalf("controller without a dependent cleanup step should not wait: %v", err)
	}
	actions, err := repositories.Executions().ListActions(ctx, created.ID)
	if err != nil || len(actions) != 1 || actions[0].Status != execution.ActionSucceeded {
		t.Fatalf("controller action = %+v, err=%v", actions, err)
	}
	aggregate, err := repositories.CleanupTasks().GetTask(ctx, "cln-controller")
	if err != nil || len(aggregate.ImpactItems) != 1 ||
		aggregate.ImpactItems[0].Result != plan.ImpactDelegated {
		t.Fatalf("controller impacts = %+v, err=%v", aggregate.ImpactItems, err)
	}
}

func TestControllerCleanupClassifiesRetainedAndUnverifiedDelegatedResources(t *testing.T) {
	ctx := context.Background()
	repositories, planner, created, _ := controllerExecutionFixture(t, "execution-retained", []controllerChild{
		{id: "vpc", ownership: graph.OwnershipShared, policy: graph.CleanupRetain},
		{id: "slb", ownership: graph.OwnershipExclusive, policy: graph.CleanupDelegate, explicitRetain: true},
		{id: "ecs", ownership: graph.OwnershipExclusive, policy: graph.CleanupDelegate},
	})
	driver := &scriptedActionDriver{readback: contracts.ReadbackResult{Exists: false, State: "absent"}}
	handler := cleanup.NewExecutionHandler(planner, cleanup.ActionResolverFunc(func(_ context.Context, value asset.Asset) (cleanup.ActionDriver, error) {
		if value.ID != "ack" {
			return nil, fmt.Errorf("unexpected child action for %s", value.ID)
		}
		return driver, nil
	}))
	job := cleanupExecutionJobForAsset(t, repositories, "cln-controller", "ack")

	if err := handler.Handle(ctx, job); err != nil {
		t.Fatal(err)
	}
	aggregate, err := repositories.CleanupTasks().GetTask(ctx, "cln-controller")
	if err != nil {
		t.Fatal(err)
	}
	results := make(map[asset.AssetID]plan.ImpactResult)
	for _, impact := range aggregate.ImpactItems {
		results[impact.AssetID] = impact.Result
	}
	if results["vpc"] != plan.ImpactRetainedShared || results["slb"] != plan.ImpactRetainedByPolicy || results["ecs"] != plan.ImpactDelegated {
		t.Fatalf("results=%+v", results)
	}
	residuals, err := repositories.Findings().ListFindingsByAsset(ctx, "ecs")
	if err != nil || len(residuals) != 0 {
		t.Fatalf("residuals=%+v err=%v", residuals, err)
	}
	actions, err := repositories.Executions().ListActions(ctx, created.ID)
	if err != nil || len(actions) != 1 || actions[0].AssetID != "ack" {
		t.Fatalf("actions=%+v err=%v", actions, err)
	}
}

type scriptedActionDriver struct {
	preflightResult      contracts.PreflightResult
	preflightCalls       int
	executeErrors        []error
	executeCalls         int
	idempotencyKeys      []string
	pollInterval         time.Duration
	deletionCheckTimeout time.Duration
	waitResults          []contracts.WaitResult
	waitErrors           []error
	waitCalls            int
	waitHadDeadline      bool
	readback             contracts.ReadbackResult
	readbackErrors       []error
	readbackCalls        int
	readbackHadDeadline  bool
}

func (d *scriptedActionDriver) DeletionCheckTimeout() time.Duration {
	return d.deletionCheckTimeout
}

type waiterDataDriver struct {
	waitPhases []string
}

func (*waiterDataDriver) Preflight(context.Context, contracts.ActionRequest) (contracts.PreflightResult, error) {
	return contracts.PreflightResult{Allowed: true}, nil
}

func (*waiterDataDriver) Execute(context.Context, contracts.ActionRequest) (contracts.ActionResult, error) {
	return contracts.ActionResult{
		Data: map[string]any{"phase": "first"}, RetryAfter: time.Second,
	}, nil
}

func (d *waiterDataDriver) Wait(
	_ context.Context,
	_ contracts.ActionRequest,
	result contracts.ActionResult,
) (contracts.WaitResult, error) {
	phase, _ := result.Data["phase"].(string)
	d.waitPhases = append(d.waitPhases, phase)
	if phase == "first" {
		return contracts.WaitResult{
			Done: false, State: "advanced", RetryAfter: time.Second,
			Data: map[string]any{"phase": "second"},
		}, nil
	}
	return contracts.WaitResult{Done: true, State: "done", Data: result.Data}, nil
}

func (*waiterDataDriver) Readback(context.Context, contracts.ActionRequest) (contracts.ReadbackResult, error) {
	return contracts.ReadbackResult{Exists: false, State: "absent"}, nil
}

type blockingActionDriver struct {
	started chan<- asset.AssetID
	release <-chan struct{}
}

func (*blockingActionDriver) Preflight(context.Context, contracts.ActionRequest) (contracts.PreflightResult, error) {
	return contracts.PreflightResult{Allowed: true}, nil
}

func (d *blockingActionDriver) Execute(_ context.Context, request contracts.ActionRequest) (contracts.ActionResult, error) {
	d.started <- request.Asset.ID
	<-d.release
	return contracts.ActionResult{ProviderRequestID: "parallel-request"}, nil
}

func (*blockingActionDriver) Wait(context.Context, contracts.ActionRequest, contracts.ActionResult) (contracts.WaitResult, error) {
	return contracts.WaitResult{Done: true, State: "done"}, nil
}

func (*blockingActionDriver) Readback(context.Context, contracts.ActionRequest) (contracts.ReadbackResult, error) {
	return contracts.ReadbackResult{Exists: false, State: "absent"}, nil
}

func (d *scriptedActionDriver) Preflight(context.Context, contracts.ActionRequest) (contracts.PreflightResult, error) {
	d.preflightCalls++
	if d.preflightResult.Reason != "" || d.preflightResult.Evidence != nil {
		return d.preflightResult, nil
	}
	return contracts.PreflightResult{Allowed: true}, nil
}

func (d *scriptedActionDriver) Execute(_ context.Context, request contracts.ActionRequest) (contracts.ActionResult, error) {
	d.executeCalls++
	d.idempotencyKeys = append(d.idempotencyKeys, request.IdempotencyKey)
	if len(d.executeErrors) > 0 {
		err := d.executeErrors[0]
		d.executeErrors = d.executeErrors[1:]
		if err != nil {
			return contracts.ActionResult{}, err
		}
	}
	return contracts.ActionResult{
		ProviderRequestID: "request-delete", ProviderOperationID: "operation-delete",
		RetryAfter: d.pollInterval,
	}, nil
}

func (d *scriptedActionDriver) Wait(ctx context.Context, _ contracts.ActionRequest, _ contracts.ActionResult) (contracts.WaitResult, error) {
	d.waitCalls++
	if _, ok := ctx.Deadline(); ok {
		d.waitHadDeadline = true
	}
	if len(d.waitErrors) > 0 {
		err := d.waitErrors[0]
		d.waitErrors = d.waitErrors[1:]
		if err != nil {
			return contracts.WaitResult{}, err
		}
	}
	if len(d.waitResults) > 0 {
		result := d.waitResults[0]
		d.waitResults = d.waitResults[1:]
		return result, nil
	}
	return contracts.WaitResult{Done: true, State: "done"}, nil
}

func (d *scriptedActionDriver) Readback(ctx context.Context, _ contracts.ActionRequest) (contracts.ReadbackResult, error) {
	d.readbackCalls++
	if _, ok := ctx.Deadline(); ok {
		d.readbackHadDeadline = true
	}
	if len(d.readbackErrors) > 0 {
		err := d.readbackErrors[0]
		d.readbackErrors = d.readbackErrors[1:]
		if err != nil {
			return contracts.ReadbackResult{}, err
		}
	}
	return d.readback, nil
}

type controllerChild struct {
	id                         asset.AssetID
	ownership                  graph.Ownership
	policy                     graph.CleanupPolicy
	explicitRetain             bool
	waitForAbsence             bool
	controllerDeleteGuaranteed bool
	controllerIntegrated       bool
}

func directExecutionFixture(t *testing.T, executionID string) (persistence.Repositories, *cleanup.Service, execution.ExecutionAttempt, time.Time) {
	t.Helper()
	ctx := context.Background()
	repositories := openPlanningRepositories(t)
	now := time.Date(2026, 7, 13, 11, 0, 0, 0, time.UTC)
	value := planningAsset("direct", "c-1", "ACS::ECS::Instance", "i-1", now)
	seedPlanningSnapshot(t, repositories, "scope-a", "graph-direct", []asset.Asset{value}, nil)
	planner := cleanup.NewService(repositories, bundleResolver{asset.ProviderAliCloud: {Provider: asset.ProviderAliCloud, Revision: "bundle-a", Hash: "spec-a"}},
		cleanup.WithClock(func() time.Time { return now }),
		cleanup.WithTaskIDGenerator(func() string { return "cln-direct" }),
		cleanup.WithExecutionIDGenerator(func() string { return executionID }),
	)
	if _, err := planner.CreateTask(ctx, cleanup.CreateTaskRequest{Selectors: []plan.CleanupSelector{assetSelector("direct")}, CreatedBy: "operator"}); err != nil {
		t.Fatal(err)
	}
	created, err := planner.CreateExecution(ctx, cleanup.CreateExecutionRequest{CleanupTaskID: "cln-direct", RequestedBy: "operator", IdempotencyKey: "request-key", Confirmation: cleanup.ExecutionConfirmation{Acknowledged: true}})
	if err != nil {
		t.Fatal(err)
	}
	return repositories, planner, created, now
}

func controllerExecutionFixture(t *testing.T, executionID string, children []controllerChild) (persistence.Repositories, *cleanup.Service, execution.ExecutionAttempt, time.Time) {
	t.Helper()
	ctx := context.Background()
	repositories := openPlanningRepositories(t)
	now := time.Date(2026, 7, 13, 11, 0, 0, 0, time.UTC)
	assets := []asset.Asset{planningAsset("ack", "c-1", "ACS::CS::Cluster", "cluster-1", now)}
	bindings := make([]graph.LifecycleBinding, 0, len(children))
	requestOptions := map[asset.AssetID]map[string]any{}
	for _, child := range children {
		nativeType := "ACS::ECS::Instance"
		if child.id == "vpc" {
			nativeType = "ACS::VPC::VPC"
		} else if child.id == "slb" {
			nativeType = "ACS::SLB::LoadBalancer"
		} else if child.id == "system-route-table" {
			nativeType = "ACS::VPC::RouteTable"
		}
		value := planningAsset(child.id, "c-1", nativeType, string(child.id)+"-native", now)
		assets = append(assets, value)
		binding := planningBinding(graph.LifecycleBindingID("binding-"+string(child.id)), "ack", child.id, child.ownership, child.policy, "graph-controller", now)
		binding.Evidence["resource_type"] = nativeType
		binding.Evidence["instance_id"] = value.Identity.NativeID
		if child.waitForAbsence {
			binding.Evidence[graph.LifecycleEvidenceWaitUntilAbsentBeforeDependents] = true
			binding.Evidence[graph.LifecycleEvidenceWaitTimeoutSeconds] = 300
			binding.Evidence[graph.LifecycleEvidenceWaitPollSeconds] = 15
		}
		if child.controllerDeleteGuaranteed {
			binding.Evidence[graph.LifecycleEvidenceControllerDeleteGuaranteed] = true
		}
		if child.controllerIntegrated {
			binding.Evidence["lifecycle_kind"] = "vpc_system_route_table"
			binding.Evidence[graph.LifecycleEvidenceControllerIntegratedResource] = true
		}
		bindings = append(bindings, binding)
		if child.explicitRetain {
			requestOptions["ack"] = map[string]any{"retain_resources": []string{value.Identity.NativeID}}
		}
	}
	seedPlanningSnapshot(t, repositories, "scope-a", "graph-controller", assets, bindings)
	planner := cleanup.NewService(repositories, bundleResolver{asset.ProviderAliCloud: {Provider: asset.ProviderAliCloud, Revision: "bundle-a", Hash: "spec-a"}},
		cleanup.WithClock(func() time.Time { return now }),
		cleanup.WithTaskIDGenerator(func() string { return "cln-controller" }),
		cleanup.WithExecutionIDGenerator(func() string { return executionID }),
	)
	selected := make([]asset.AssetID, 0, len(assets))
	for _, value := range assets {
		selected = append(selected, value.ID)
	}
	selectors := make([]plan.CleanupSelector, 0, len(selected))
	for _, id := range selected {
		selectors = append(selectors, assetSelector(id))
	}
	if _, err := planner.CreateTask(ctx, cleanup.CreateTaskRequest{Selectors: selectors, CreatedBy: "operator", RequestOptions: requestOptions}); err != nil {
		t.Fatal(err)
	}
	created, err := planner.CreateExecution(ctx, cleanup.CreateExecutionRequest{CleanupTaskID: "cln-controller", RequestedBy: "operator", IdempotencyKey: "request-key", Confirmation: cleanup.ExecutionConfirmation{Acknowledged: true}})
	if err != nil {
		t.Fatal(err)
	}
	return repositories, planner, created, now
}

func managedImpactBarrierExecutionFixture(
	t *testing.T,
	executionID string,
) (persistence.Repositories, *cleanup.Service, execution.ExecutionAttempt, *time.Time) {
	t.Helper()
	ctx := context.Background()
	repositories := openPlanningRepositories(t)
	current := time.Date(2026, 8, 5, 11, 0, 0, 0, time.UTC)
	assets := []asset.Asset{
		planningAsset(
			"dataworks-group",
			"c-1",
			"ACS::DataWorks::DwResourceGroup",
			"Serverless_res_group_210724245979777_717305919406626",
			current,
		),
		planningAsset("managed-sg", "c-1", "ACS::ECS::SecurityGroup", "sg-d7ob1fsemhw32ptq0jav", current),
		planningAsset("managed-eni", "c-1", "ACS::ECS::NetworkInterface", "eni-d7o0kwoemjg2fgi3wym7", current),
		planningAsset("vpc", "c-1", "ACS::VPC::VPC", "vpc-london", current),
	}
	bindings := []graph.LifecycleBinding{
		planningBinding(
			"binding-managed-sg",
			"dataworks-group",
			"managed-sg",
			graph.OwnershipExclusive,
			graph.CleanupDelegate,
			"graph-managed-barrier",
			current,
		),
		planningBinding(
			"binding-managed-eni",
			"dataworks-group",
			"managed-eni",
			graph.OwnershipExclusive,
			graph.CleanupDelegate,
			"graph-managed-barrier",
			current,
		),
	}
	for index := range bindings {
		bindings[index].Evidence[graph.LifecycleEvidenceWaitUntilAbsentBeforeDependents] = true
		bindings[index].Evidence[graph.LifecycleEvidenceWaitTimeoutSeconds] = 300
		bindings[index].Evidence[graph.LifecycleEvidenceWaitPollSeconds] = 15
	}
	relationships := []graph.Relationship{{
		ID:            "dataworks-group-uses-vpc",
		SourceAssetID: "dataworks-group",
		TargetAssetID: "vpc",
		Type:          graph.RelationshipUses,
		Source:        "dataworks:ListNetworks",
		Confidence:    1,
		GraphRevision: "graph-managed-barrier",
		ObservedAt:    current,
	}}
	seedPlanningGraph(
		t,
		repositories,
		"scope-a",
		"graph-managed-barrier",
		assets,
		relationships,
		bindings,
	)
	planner := cleanup.NewService(
		repositories,
		bundleResolver{
			asset.ProviderAliCloud: {
				Provider: asset.ProviderAliCloud,
				Revision: "bundle-a",
				Hash:     "spec-a",
			},
		},
		cleanup.WithClock(func() time.Time { return current }),
		cleanup.WithTaskIDGenerator(func() string { return "cln-managed-barrier" }),
		cleanup.WithExecutionIDGenerator(func() string { return executionID }),
	)
	selectors := make([]plan.CleanupSelector, 0, len(assets))
	for _, value := range assets {
		selectors = append(selectors, assetSelector(value.ID))
	}
	aggregate, err := planner.CreateTask(ctx, cleanup.CreateTaskRequest{
		Selectors: selectors,
		CreatedBy: "operator",
	})
	if err != nil {
		t.Fatal(err)
	}
	groupStep := cleanupTaskStepForAsset(aggregate.Steps, "dataworks-group")
	vpcStep := cleanupTaskStepForAsset(aggregate.Steps, "vpc")
	if len(aggregate.Steps) != 4 {
		t.Fatalf("managed barrier plan = %+v", aggregate.Steps)
	}
	requiredVPCDependencies := map[plan.StepID]bool{groupStep.ID: true}
	for _, managedID := range []asset.AssetID{"managed-sg", "managed-eni"} {
		verification := cleanupTaskStepForAsset(aggregate.Steps, managedID)
		if verification.Kind != plan.StepVerification ||
			len(verification.DependsOn) != 1 ||
			verification.DependsOn[0] != groupStep.ID {
			t.Fatalf("managed verification step = %+v", verification)
		}
		requiredVPCDependencies[verification.ID] = true
	}
	for _, dependency := range vpcStep.DependsOn {
		delete(requiredVPCDependencies, dependency)
	}
	if len(requiredVPCDependencies) != 0 {
		t.Fatalf(
			"managed barrier plan = %+v, missing VPC dependencies = %+v",
			aggregate.Steps,
			requiredVPCDependencies,
		)
	}
	created, err := planner.CreateExecution(ctx, cleanup.CreateExecutionRequest{
		CleanupTaskID:  "cln-managed-barrier",
		RequestedBy:    "operator",
		IdempotencyKey: "request-key",
		Confirmation: cleanup.ExecutionConfirmation{
			Acknowledged: true,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return repositories, planner, created, &current
}

func cleanupExecutionJobForAsset(
	t *testing.T,
	repositories persistence.Repositories,
	cleanupTaskID plan.CleanupTaskID,
	assetID asset.AssetID,
) execution.Job {
	t.Helper()
	ctx := context.Background()
	aggregate, err := repositories.CleanupTasks().GetTask(ctx, cleanupTaskID)
	if err != nil {
		t.Fatal(err)
	}
	var stepID plan.StepID
	for _, step := range aggregate.Steps {
		if step.AssetID == assetID {
			stepID = step.ID
			break
		}
	}
	if stepID == "" {
		t.Fatalf("cleanup task %q has no step for asset %q", cleanupTaskID, assetID)
	}
	jobs, err := repositories.Jobs().ListJobsByAggregate(ctx, "cleanup_task", string(cleanupTaskID))
	if err != nil {
		t.Fatal(err)
	}
	for _, job := range jobs {
		payloadStepID, _ := job.Payload["cleanup_task_step_id"].(string)
		if strings.TrimSpace(payloadStepID) == string(stepID) {
			return job
		}
	}
	t.Fatalf("cleanup task %q has no execution job for asset %q", cleanupTaskID, assetID)
	return execution.Job{}
}

func cleanupTaskStepForAsset(
	steps []plan.CleanupTaskStep,
	assetID asset.AssetID,
) plan.CleanupTaskStep {
	for _, step := range steps {
		if step.AssetID == assetID {
			return step
		}
	}
	return plan.CleanupTaskStep{}
}

func claimExecutionJob(t *testing.T, repositories persistence.Repositories, now time.Time) execution.Job {
	t.Helper()
	job, err := repositories.Jobs().ClaimNext(context.Background(), "test-worker", now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	return job
}

type workConflictRepositories struct {
	persistence.Repositories
}

func (r workConflictRepositories) WithTx(ctx context.Context, action func(persistence.Repositories) error) error {
	return r.Repositories.WithTx(ctx, func(repositories persistence.Repositories) error {
		return action(workConflictRepositories{Repositories: repositories})
	})
}

func (r workConflictRepositories) Connections() persistence.ConnectionRepository {
	return workConflictConnectionRepository{ConnectionRepository: r.Repositories.Connections()}
}

type workConflictConnectionRepository struct {
	persistence.ConnectionRepository
}

func (workConflictConnectionRepository) PutConnectionIfUnchanged(context.Context, asset.CloudConnection, time.Time) error {
	return persistence.ErrConflict
}
