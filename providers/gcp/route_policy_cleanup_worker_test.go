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

func TestRoutePolicySQLiteScanCleanupRestartAndReconciliation(t *testing.T) {
	ctx := t.Context()
	r, _, fixture := routePolicyActionRuntime(t)
	transport := r.transport
	registry := identityRegistry(t, r)
	db := filepath.Join(t.TempDir(), "route-policy-cleanup.db")
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
	scan := func() {
		t.Helper()
		creator, err := inventory.NewCreator(repositories, registry, inventory.WithCreatorClock(func() time.Time { return now }))
		if err != nil {
			t.Fatal(err)
		}
		created, err := creator.Create(ctx, inventory.ScanCreationRequest{ConnectionID: "connection", RequestedBy: "test", RegionMode: inventory.RegionModeSelected, RegionIDs: []string{"us-central1"}, ResourceKindIDs: []asset.ResourceKindID{r.resourceKind(routerType).ID, r.resourceKind(routePolicyType).ID}})
		if err != nil || len(created.Shards) != 2 || len(created.Jobs) != 1 {
			t.Fatal("scan creation", created, err)
		}
		job, err := repositories.Jobs().ClaimNext(ctx, "policy-scan", now, time.Minute, execution.JobScan)
		if err != nil {
			t.Fatal(err)
		}
		if err := inventory.NewScanHandler(repositories, registry, inventory.NewService(repositories.Inventory())).Handle(ctx, job); err != nil {
			t.Fatal(err)
		}
		if err := repositories.Jobs().Complete(ctx, job.ID, "policy-scan", execution.JobSucceeded, "", now); err != nil {
			t.Fatal(err)
		}
		if wall := time.Now().UTC(); wall.After(now) {
			now = wall
		}
		now = now.Add(time.Second)
		job, err = repositories.Jobs().ClaimNext(ctx, "policy-graph", now, time.Minute, execution.JobGraph)
		if err != nil {
			t.Fatal(err)
		}
		if err := governance.NewGraphHandler(repositories, registry, nil).Handle(ctx, job); err != nil {
			t.Fatal(err)
		}
		if err := repositories.Jobs().Complete(ctx, job.ID, "policy-graph", execution.JobSucceeded, "", now); err != nil {
			t.Fatal(err)
		}
	}
	scan()
	page, err := repositories.Inventory().ListAssets(ctx, persistence.ListOptions{Limit: 10})
	if err != nil || len(page.Items) != 2 {
		t.Fatal(page, err)
	}
	var policy, parent asset.Asset
	for _, value := range page.Items {
		if value.Identity.NativeType == routePolicyType {
			policy = value
		} else {
			parent = value
		}
	}
	if policy.ID == "" || !policy.Capabilities.Has(asset.CapabilityActionable) || policy.Normalized[routePolicyRouterID] != "1001" {
		t.Fatal("scan did not capture deletable policy review", policy)
	}
	planner := cleanup.NewService(repositories, registry, cleanup.WithClock(func() time.Time { return now }))
	task, err := planner.CreateTask(ctx, cleanup.CreateTaskRequest{ConnectionID: "connection", Selectors: []plan.CleanupSelector{{Kind: plan.SelectorAsset, AssetID: policy.ID}}, CreatedBy: "test"})
	if err != nil || len(task.Steps) != 1 || task.Steps[0].AssetID != policy.ID {
		t.Fatal("independent policy plan", task, err)
	}
	attempt, err := planner.CreateExecution(ctx, cleanup.CreateExecutionRequest{CleanupTaskID: task.Task.ID, ConnectionID: "connection", RequestedBy: "test", IdempotencyKey: "policy-sqlite", Confirmation: cleanup.ExecutionConfirmation{Acknowledged: true}})
	if err != nil {
		t.Fatal(err)
	}
	job, err := repositories.Jobs().ClaimNext(ctx, "policy-worker", now, time.Minute, execution.JobExecute)
	if err != nil {
		t.Fatal(err)
	}
	for round, want := range []execution.ActionStatus{execution.ActionWaiting, execution.ActionWaiting, execution.ActionReadingBack, execution.ActionSucceeded} {
		repositories, err = sqlite.Open(db, "../../migrations")
		if err != nil {
			t.Fatal(err)
		}
		fresh := protocolRuntime(t, transport.RoundTrip)
		planner = cleanup.NewService(repositories, identityRegistry(t, fresh), cleanup.WithClock(func() time.Time { return now }))
		handler := cleanup.NewExecutionHandler(planner, cleanup.ActionResolverFunc(func(ctx context.Context, value asset.Asset) (cleanup.ActionDriver, error) {
			return fresh.ResolveAction(ctx, value.Identity.ConnectionID, value)
		}))
		if round == 1 {
			fixture.status = "DONE"
		}
		if round == 2 {
			fixture.exists = false
		}
		err = handler.Handle(ctx, job)
		var retry *cleanup.RetryError
		if want == execution.ActionSucceeded && err != nil || want != execution.ActionSucceeded && !errors.As(err, &retry) {
			t.Fatal("restarted worker", round, err)
		}
		actions, err := repositories.Executions().ListActions(ctx, attempt.ID)
		if err != nil || len(actions) != 1 || actions[0].Status != want || actions[0].ProviderResult["phase"] != "route_policy_delete" {
			t.Fatal("persisted native phase", round, actions, err)
		}
		if fixture.deletes != 1 {
			t.Fatal("restart repeated deletion", fixture.deletes)
		}
		now = now.Add(3 * time.Second)
	}
	finished, err := repositories.Executions().GetExecution(ctx, attempt.ID)
	if err != nil || finished.Status != execution.ExecutionSucceeded {
		t.Fatal(finished, err)
	}
	deleted, err := repositories.Inventory().GetAsset(ctx, policy.ID)
	if err != nil || deleted.DeletedAt == nil {
		t.Fatal("missing tombstone", deleted, err)
	}
	retained, err := repositories.Inventory().GetAsset(ctx, parent.ID)
	if err != nil || retained.DeletedAt != nil || retained.ClosedAt != nil {
		t.Fatal("router was deleted", retained, err)
	}
	if err := repositories.Jobs().Complete(ctx, job.ID, "policy-worker", execution.JobSucceeded, "", now); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Minute)
	scan()
	page, err = repositories.Inventory().ListAssets(ctx, persistence.ListOptions{Limit: 10})
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != parent.ID || fixture.deletes != 1 {
		t.Fatal("reconciliation changed parent or restored deleted policy", page, err)
	}
}
