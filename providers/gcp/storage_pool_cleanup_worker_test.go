package gcp

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/loomx-ai/steward/internal/app/cleanup"
	"github.com/loomx-ai/steward/internal/app/governance"
	"github.com/loomx-ai/steward/internal/app/inventory"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/internal/persistence"
	"github.com/loomx-ai/steward/internal/persistence/sqlite"
)

func TestStoragePoolSQLiteScanPlanExecutionAndReconciliation(t *testing.T) {
	ctx := t.Context()
	s := newPoolCleanupScenario()
	r := protocolRuntime(t, s.transport(t))
	registry := identityRegistry(t, r)
	db := filepath.Join(t.TempDir(), "pool-cleanup.db")
	repo, err := sqlite.Open(db, "../../migrations")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 14, 0, 0, 0, 0, time.UTC)
	connection := asset.CloudConnection{ID: "connection", Provider: asset.ProviderGCP, Partition: "gcp", Status: asset.ConnectionActive, CreatedAt: now, UpdatedAt: now}
	if err := repo.Connections().PutConnection(ctx, connection); err != nil {
		t.Fatal(err)
	}
	if err := repo.Inventory().PutScope(ctx, asset.Scope{ID: "project", ConnectionID: "connection", Kind: asset.ScopeProject, NativeID: "sample-project", Name: "Project", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := repo.Regions().PutRegion(ctx, asset.ConnectionRegion{ID: "region", ConnectionID: "connection", RegionID: "us-central1", DiscoveredName: "US Central", Origin: asset.RegionOriginAPI, Lifecycle: asset.RegionActive, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	scan := func() {
		creator, err := inventory.NewCreator(repo, registry, inventory.WithCreatorClock(func() time.Time { return now }))
		if err != nil {
			t.Fatal(err)
		}
		created, err := creator.Create(ctx, inventory.ScanCreationRequest{ConnectionID: "connection", RequestedBy: "test", RegionMode: inventory.RegionModeAllActive, ResourceKindIDs: []asset.ResourceKindID{r.resourceKind(storagePoolType).ID, r.resourceKind("compute.googleapis.com/Disk").ID}})
		if err != nil || len(created.Shards) != 2 {
			t.Fatal(created, err)
		}
		handler := inventory.NewScanHandler(repo, registry, inventory.NewService(repo.Inventory(), inventory.WithClock(func() time.Time { return now })))
		for range created.Jobs {
			job, err := repo.Jobs().ClaimNext(ctx, "pool-scan", now, time.Minute, execution.JobScan)
			if err != nil {
				t.Fatal(err)
			}
			if err := handler.Handle(ctx, job); err != nil {
				t.Fatal(err)
			}
			if err := repo.Jobs().Complete(ctx, job.ID, "pool-scan", execution.JobSucceeded, "", now); err != nil {
				t.Fatal(err)
			}
		}
		if wall := time.Now().UTC(); wall.After(now) {
			now = wall
		}
		now = now.Add(time.Second)
		job, err := repo.Jobs().ClaimNext(ctx, "pool-graph", now, time.Minute, execution.JobGraph)
		if err != nil {
			t.Fatal(err)
		}
		graphHandler := governance.NewGraphHandler(repo, registry, poolCleanupContributors{r})
		if err := graphHandler.Handle(ctx, job); err != nil {
			t.Fatal(err)
		}
		if err := repo.Jobs().Complete(ctx, job.ID, "pool-graph", execution.JobSucceeded, "", now); err != nil {
			t.Fatal(err)
		}
	}
	scan()
	page, err := repo.Inventory().ListAssets(ctx, persistence.ListOptions{Limit: 10})
	if err != nil || len(page.Items) != 2 {
		t.Fatal(page, err)
	}
	values := page.Items
	planner := cleanup.NewService(repo, registry, cleanup.WithClock(func() time.Time { return now }))
	selectors := []plan.CleanupSelector{}
	for _, value := range values {
		selectors = append(selectors, plan.CleanupSelector{Kind: plan.SelectorAsset, AssetID: value.ID})
	}
	task, err := planner.CreateTask(ctx, cleanup.CreateTaskRequest{ConnectionID: "connection", Selectors: selectors, CreatedBy: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if len(task.Steps) != 2 {
		t.Fatal("native disk and pool steps missing", task)
	}
	attempt, err := planner.CreateExecution(ctx, cleanup.CreateExecutionRequest{CleanupTaskID: task.Task.ID, ConnectionID: "connection", RequestedBy: "test", IdempotencyKey: "pool-sqlite", Confirmation: cleanup.ExecutionConfirmation{Acknowledged: true}})
	if err != nil {
		t.Fatal(err)
	}
	jobs := make([]execution.Job, 0, len(task.Steps))
	for range task.Steps {
		job, err := repo.Jobs().ClaimNext(ctx, "pool-worker", now, time.Minute, execution.JobExecute)
		if err != nil {
			t.Fatal(err)
		}
		jobs = append(jobs, job)
	}
	completed := false
	for round := 0; round < 12; round++ {
		repo, err = sqlite.Open(db, "../../migrations")
		if err != nil {
			t.Fatal(err)
		}
		fresh := protocolRuntime(t, s.transport(t))
		planner = cleanup.NewService(repo, identityRegistry(t, fresh), cleanup.WithClock(func() time.Time { return now }))
		handler := cleanup.NewExecutionHandler(planner, cleanup.ActionResolverFunc(func(ctx context.Context, value asset.Asset) (cleanup.ActionDriver, error) {
			return fresh.ResolveAction(ctx, value.Identity.ConnectionID, value)
		}))
		for _, job := range jobs {
			err = handler.Handle(ctx, job)
			var retry *cleanup.RetryError
			if err != nil && !errors.As(err, &retry) {
				t.Fatal("restarted pool worker", err)
			}
		}
		actions, err := repo.Executions().ListActions(ctx, attempt.ID)
		if err != nil || len(actions) > 2 {
			t.Fatal(actions, err)
		}
		completed = len(actions) == 2
		for _, action := range actions {
			completed = completed && action.Status == execution.ActionSucceeded
		}
		if completed {
			break
		}
		now = now.Add(3 * time.Second)
	}
	if !completed || len(s.writes) != 2 || s.pool != nil || s.disk != nil {
		t.Fatal("incomplete or replayed cleanup", completed, s.writes)
	}
	finished, err := repo.Executions().GetExecution(ctx, attempt.ID)
	if err != nil || finished.Status != execution.ExecutionSucceeded {
		t.Fatal("execution did not finish", finished, err)
	}
	for _, job := range jobs {
		if err := repo.Jobs().Complete(ctx, job.ID, "pool-worker", execution.JobSucceeded, "", now); err != nil {
			t.Fatal(err)
		}
	}
	for _, prior := range values {
		value, err := repo.Inventory().GetAsset(ctx, prior.ID)
		if err != nil || value.DeletedAt == nil {
			t.Fatal("missing native deletion projection", value, err)
		}
	}
	now = now.Add(time.Minute)
	scan()
	page, err = repo.Inventory().ListAssets(ctx, persistence.ListOptions{Limit: 10})
	if err != nil || len(page.Items) != 0 {
		t.Fatal("deleted resources reappeared", page, err)
	}
}

type poolCleanupContributors struct{ r *Runtime }

func (p poolCleanupContributors) ResolveContributors(ctx context.Context, connection asset.CloudConnection, _ []asset.Asset) ([]governance.Contributor, error) {
	contributor, err := p.r.ComputeLifecycle(ctx, connection.ID)
	return []governance.Contributor{contributor}, err
}
