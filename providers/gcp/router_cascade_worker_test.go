package gcp

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/loomx-ai/steward/internal/app/cleanup"
	"github.com/loomx-ai/steward/internal/app/governance"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/internal/persistence"
	"github.com/loomx-ai/steward/internal/persistence/sqlite"
)

type routerCleanupContributors struct{ r *Runtime }

func (c routerCleanupContributors) ResolveContributors(ctx context.Context, connection asset.CloudConnection, _ []asset.Asset) ([]governance.Contributor, error) {
	contributor, err := c.r.ServiceLifecycle(ctx, connection.ID)
	return []governance.Contributor{contributor}, err
}

func TestRouterCascadeSQLiteScanPlanRestartAndReconciliation(t *testing.T) {
	routerCascadeSQLiteCleanup(t)
}

type routerCascadeCheckpoint struct {
	DatabasePath string
	Repositories persistence.Repositories
	Runtime      *Runtime
	Fixture      *routerCascadeFixture
	Task         persistence.CleanupTaskAggregate
	Attempt      execution.ExecutionAttempt
	Job          execution.Job
}

func routerCascadeSQLiteCleanup(t *testing.T, checkpoints ...func(routerCascadeCheckpoint)) {
	ctx := t.Context()
	r, _, f := routerCascadeRuntime(t)
	registry := identityRegistry(t, r)
	db := filepath.Join(t.TempDir(), "router-cascade.db")
	repos, err := sqlite.Open(db, "../../migrations")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := repos.Connections().PutConnection(ctx, asset.CloudConnection{ID: "connection", Provider: asset.ProviderGCP, Partition: "gcp", Status: asset.ConnectionActive, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := repos.Inventory().PutScope(ctx, asset.Scope{ID: "project", ConnectionID: "connection", Kind: asset.ScopeProject, NativeID: "sample-project", Name: "Project", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := repos.Regions().PutRegion(ctx, asset.ConnectionRegion{ID: "region", ConnectionID: "connection", RegionID: "us-central1", Origin: asset.RegionOriginAPI, Lifecycle: asset.RegionActive, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	kinds := []asset.ResourceKindID{}
	for _, kind := range []string{routerType, cloudNatType, routePolicyType, namedSetType} {
		kinds = append(kinds, r.resourceKind(kind).ID)
	}
	scan := func() { now = scanRouterComponentAssets(t, repos, registry, now, kinds, routerCleanupContributors{r}) }
	scan()
	page, err := repos.Inventory().ListAssets(ctx, persistence.ListOptions{Limit: 20})
	if err != nil || len(page.Items) != 5 {
		t.Fatal(page, err)
	}
	var parent asset.Asset
	for _, value := range page.Items {
		if value.Identity.NativeType == routerType {
			parent = value
		}
	}
	planner := cleanup.NewService(repos, registry, cleanup.WithClock(func() time.Time { return now }))
	task, err := planner.CreateTask(ctx, cleanup.CreateTaskRequest{ConnectionID: "connection", Selectors: []plan.CleanupSelector{{Kind: plan.SelectorAsset, AssetID: parent.ID}}, CreatedBy: "test"})
	if err != nil || len(task.Steps) != 4 || len(task.ImpactItems) != 1 || len(task.Task.Blockers) != 0 {
		t.Fatal(task, err)
	}
	if task.ImpactItems[0].Expected != plan.ExpectedDelegatedDelete {
		t.Fatal(task.ImpactItems)
	}
	concurrency := 4
	attempt, err := planner.CreateExecution(ctx, cleanup.CreateExecutionRequest{CleanupTaskID: task.Task.ID, ConnectionID: "connection", RequestedBy: "test", IdempotencyKey: "router-cascade", Concurrency: &concurrency, Confirmation: cleanup.ExecutionConfirmation{Acknowledged: true}})
	if err != nil {
		t.Fatal(err)
	}
	positions := map[string]int{}
	for i, step := range task.Steps {
		positions[string(step.ID)] = i
	}
	jobs := []execution.Job{}
	for range len(task.Steps) {
		job, err := repos.Jobs().ClaimNext(ctx, "router-worker", now, time.Minute, execution.JobExecute)
		if err != nil {
			t.Fatal(err)
		}
		jobs = append(jobs, job)
	}
	slices.SortFunc(jobs, func(a, b execution.Job) int {
		return positions[a.Payload["cleanup_task_step_id"].(string)] - positions[b.Payload["cleanup_task_step_id"].(string)]
	})
	for index, job := range jobs {
		if index == len(jobs)-1 && len(checkpoints) > 0 {
			f.pending = true
		}
		done := false
		for round := 0; round < 10; round++ {
			repos, err = sqlite.Open(db, "../../migrations")
			if err != nil {
				t.Fatal(err)
			}
			fresh := protocolRuntime(t, r.transport.RoundTrip)
			planner = cleanup.NewService(repos, identityRegistry(t, fresh), cleanup.WithClock(func() time.Time { return now }))
			handler := cleanup.NewExecutionHandler(planner, cleanup.ActionResolverFunc(func(ctx context.Context, value asset.Asset) (cleanup.ActionDriver, error) {
				return fresh.ResolveAction(ctx, value.Identity.ConnectionID, value)
			}))
			if index < len(jobs)-1 {
				var retry *cleanup.RetryError
				if err := handler.Handle(ctx, jobs[len(jobs)-1]); !errors.As(err, &retry) || f.routerDeletes != 0 {
					t.Fatal("Router ran before prerequisite", err, f.routerDeletes)
				}
			}
			err = handler.Handle(ctx, job)
			if index == len(jobs)-1 && round == 0 && len(checkpoints) > 0 {
				var retry *cleanup.RetryError
				if !errors.As(err, &retry) || f.routerDeletes != 1 {
					t.Fatal("Router did not reach pending native deletion", err)
				}
				checkpoints[0](routerCascadeCheckpoint{db, repos, fresh, f, task, attempt, job})
				return
			}
			if err == nil {
				done = true
				break
			}
			var retry *cleanup.RetryError
			if !errors.As(err, &retry) {
				t.Fatal("native worker", index, round, err)
			}
			now = now.Add(3 * time.Second)
		}
		if !done {
			t.Fatal("native step did not finish", index)
		}
		if err := repos.Jobs().Complete(ctx, job.ID, "router-worker", execution.JobSucceeded, "", now); err != nil {
			t.Fatal(err)
		}
	}
	final, err := repos.Executions().GetExecution(ctx, attempt.ID)
	if err != nil || final.Status != execution.ExecutionSucceeded || f.routerDeletes != 1 || f.setDeletes != 1 || len(f.deletes) != 2 || len(f.patches) != 2 {
		t.Fatal(final, err, f)
	}
	for _, original := range page.Items {
		value, err := repos.Inventory().GetAsset(ctx, original.ID)
		if err != nil || value.DeletedAt == nil {
			t.Fatal("missing parent/prerequisite/NAT tombstone", value, err)
		}
	}
	impacts, err := repos.CleanupTasks().GetTask(ctx, task.Task.ID)
	if err != nil || impacts.ImpactItems[0].Result != plan.ImpactDeletedByController {
		t.Fatal(impacts, err)
	}
	now = now.Add(time.Minute)
	scan()
	page, err = repos.Inventory().ListAssets(ctx, persistence.ListOptions{Limit: 20})
	if err != nil || len(page.Items) != 0 {
		t.Fatal("cascade resources reappeared", page, err)
	}
}
