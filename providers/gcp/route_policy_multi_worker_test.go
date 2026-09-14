package gcp

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/loomx-ai/steward/internal/app/cleanup"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/internal/persistence"
	"github.com/loomx-ai/steward/internal/persistence/sqlite"
)

func TestRoutePolicySQLiteSequentialDeletesFromOneScan(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		name := "current"
		if legacy {
			name = "legacy"
		}
		t.Run(name, func(t *testing.T) { routePolicySQLiteSharedExecution(t, legacy) })
	}
}

func routePolicySQLiteSharedExecution(t *testing.T, legacy bool) {
	ctx := t.Context()
	r, _, f := multiPolicyRuntime(t)
	registry := identityRegistry(t, r)
	db := filepath.Join(t.TempDir(), "multiple-policies.db")
	repositories, err := sqlite.Open(db, "../../migrations")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := repositories.Connections().PutConnection(ctx, asset.CloudConnection{ID: "connection", Provider: asset.ProviderGCP, Partition: "gcp", Status: asset.ConnectionActive, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Inventory().PutScope(ctx, asset.Scope{ID: "project", ConnectionID: "connection", Kind: asset.ScopeProject, NativeID: "sample-project", Name: "Project", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Regions().PutRegion(ctx, asset.ConnectionRegion{ID: "region", ConnectionID: "connection", RegionID: "us-central1", DiscoveredName: "US Central", Origin: asset.RegionOriginAPI, Lifecycle: asset.RegionActive, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	kinds := []asset.ResourceKindID{r.resourceKind(routerType).ID, r.resourceKind(routePolicyType).ID}
	now = scanRouterComponentAssets(t, repositories, registry, now, kinds)
	page, err := repositories.Inventory().ListAssets(ctx, persistence.ListOptions{Limit: 10})
	if err != nil || len(page.Items) != 3 {
		t.Fatal(page, err)
	}
	var selectors []plan.CleanupSelector
	var parent asset.Asset
	var review string
	for _, value := range page.Items {
		if value.Identity.NativeType == routerType {
			parent = value
			continue
		}
		digest := firewallDigest(value.Normalized[routePolicyPeers])
		if review != "" && review != digest {
			t.Fatal("policies did not share the original scan")
		}
		review = digest
		selectors = append(selectors, plan.CleanupSelector{Kind: plan.SelectorAsset, AssetID: value.ID})
	}
	planner := cleanup.NewService(repositories, registry, cleanup.WithClock(func() time.Time { return now }))
	task, err := planner.CreateTask(ctx, cleanup.CreateTaskRequest{ConnectionID: "connection", Selectors: selectors, CreatedBy: "test"})
	if err != nil || len(task.Steps) != 2 {
		t.Fatal(task, err)
	}
	// Old plans retain the same snapshot hash even without shared Router edges.
	if legacy {
		for i := range task.Steps {
			task.Steps[i].DependsOn = nil
			delete(task.Steps[i].Evidence, "gcp_router_mutation_scope")
		}
		slices.Reverse(task.Steps)
		if err := repositories.CleanupTasks().ReplaceTask(ctx, task.Task, task.Steps, task.ImpactItems); err != nil {
			t.Fatal(err)
		}
	}
	other, err := planner.CreateTask(ctx, cleanup.CreateTaskRequest{ConnectionID: "connection", Selectors: selectors[:1], CreatedBy: "test"})
	if err != nil {
		t.Fatal(err)
	}
	concurrency := 2
	attempt, err := planner.CreateExecution(ctx, cleanup.CreateExecutionRequest{CleanupTaskID: task.Task.ID, ConnectionID: "connection", RequestedBy: "test", IdempotencyKey: "multi-policy", Concurrency: &concurrency, Confirmation: cleanup.ExecutionConfirmation{Acknowledged: true}})
	if err != nil {
		t.Fatal(err)
	}
	if legacy {
		// Also exercise an old failed task continuing before any native invocation.
		// All original jobs are terminal before missing ordering can be backfilled.
		for range 2 {
			job, err := repositories.Jobs().ClaimNext(ctx, "legacy-worker", now, time.Minute, execution.JobExecute)
			if err != nil {
				t.Fatal(err)
			}
			if err := repositories.Jobs().Complete(ctx, job.ID, "legacy-worker", execution.JobFailed, "stopped before invocation", now); err != nil {
				t.Fatal(err)
			}
		}
		saved, err := repositories.CleanupTasks().GetTask(ctx, task.Task.ID)
		if err != nil {
			t.Fatal(err)
		}
		saved.Task.Status = plan.StatusFailed
		for i := range saved.Steps {
			saved.Steps[i].DependsOn = nil
			delete(saved.Steps[i].Evidence, "gcp_router_mutation_scope")
		}
		slices.Reverse(saved.Steps)
		if err := repositories.CleanupTasks().ReplaceTask(ctx, saved.Task, saved.Steps, saved.ImpactItems); err != nil {
			t.Fatal(err)
		}
		attempt.Status = execution.ExecutionFailed
		if err := repositories.Executions().UpdateExecution(ctx, attempt); err != nil {
			t.Fatal(err)
		}
		continued, err := planner.ContinueExecution(ctx, cleanup.ContinueExecutionRequest{CleanupTaskID: task.Task.ID, ConnectionID: "connection", RequestedBy: "test", IdempotencyKey: "continue-legacy", Concurrency: &concurrency})
		if err != nil || continued.ID != attempt.ID {
			t.Fatal("legacy continuation", continued, err)
		}
		attempt = continued
	}
	if _, err := planner.CreateExecution(ctx, cleanup.CreateExecutionRequest{CleanupTaskID: other.Task.ID, ConnectionID: "connection", RequestedBy: "test", IdempotencyKey: "overlapping-policy", Confirmation: cleanup.ExecutionConfirmation{Acknowledged: true}}); !errors.Is(err, persistence.ErrConflict) {
		t.Fatal("overlapping policy execution was not blocked", err)
	}
	stored, err := repositories.CleanupTasks().GetTask(ctx, task.Task.ID)
	if err != nil || len(stored.Steps) != 2 || !slices.Contains(stored.Steps[1].DependsOn, stored.Steps[0].ID) {
		t.Fatal("missing durable Router dependency", stored, err)
	}
	jobs := []execution.Job{}
	for range 2 {
		job, err := repositories.Jobs().ClaimNext(ctx, "multi-policy", now, time.Minute, execution.JobExecute)
		if err != nil {
			t.Fatal(err)
		}
		jobs = append(jobs, job)
	}
	if jobs[0].Payload["cleanup_task_step_id"] != string(stored.Steps[0].ID) {
		slices.Reverse(jobs)
	}
	for jobIndex, job := range jobs {
		done := false
		for round := 0; round < 8; round++ {
			repositories, err = sqlite.Open(db, "../../migrations")
			if err != nil {
				t.Fatal(err)
			}
			fresh := protocolRuntime(t, r.transport.RoundTrip)
			planner = cleanup.NewService(repositories, identityRegistry(t, fresh), cleanup.WithClock(func() time.Time { return now }))
			handler := cleanup.NewExecutionHandler(planner, cleanup.ActionResolverFunc(func(ctx context.Context, value asset.Asset) (cleanup.ActionDriver, error) {
				return fresh.ResolveAction(ctx, value.Identity.ConnectionID, value)
			}))
			if jobIndex == 0 {
				patches, deletes := len(f.patches), len(f.deletes)
				var retry *cleanup.RetryError
				if err := handler.Handle(ctx, jobs[1]); !errors.As(err, &retry) || len(f.patches) != patches || len(f.deletes) != deletes {
					t.Fatal("dependent policy ran before predecessor readback", err, f.patches, f.deletes)
				}
			}
			err = handler.Handle(ctx, job)
			if err == nil {
				done = true
				break
			}
			var retry *cleanup.RetryError
			if !errors.As(err, &retry) {
				t.Fatal("restarted policy worker", round, err)
			}
			now = now.Add(3 * time.Second)
		}
		if !done {
			t.Fatal("policy did not settle")
		}
		if err := repositories.Jobs().Complete(ctx, job.ID, "multi-policy", execution.JobSucceeded, "", now); err != nil {
			t.Fatal(err)
		}
	}
	finished, err := repositories.Executions().GetExecution(ctx, attempt.ID)
	if err != nil || finished.Status != execution.ExecutionSucceeded || len(f.patches) != 2 || len(f.deletes) != 2 {
		t.Fatal(finished, err, f.patches, f.deletes)
	}
	for _, selector := range selectors {
		value, err := repositories.Inventory().GetAsset(ctx, selector.AssetID)
		if err != nil || value.DeletedAt == nil {
			t.Fatal("missing policy tombstone", value, err)
		}
	}
	value, err := repositories.Inventory().GetAsset(ctx, parent.ID)
	if err != nil || value.DeletedAt != nil || value.ClosedAt != nil {
		t.Fatal("Router not retained", value, err)
	}
	now = now.Add(time.Minute)
	scanRouterComponentAssets(t, repositories, registry, now, kinds)
	page, err = repositories.Inventory().ListAssets(ctx, persistence.ListOptions{Limit: 10})
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != parent.ID {
		t.Fatal("rescan resurrected deleted policy", page, err)
	}
}
