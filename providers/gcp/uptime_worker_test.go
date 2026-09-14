package gcp

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
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

func TestUptimeSQLiteScanHistoryCleanupRestartAndReconciliation(t *testing.T) {
	ctx := t.Context()
	r, _, data, mode, deletes := uptimeScenario(t)
	db := filepath.Join(t.TempDir(), "uptime.db")
	repos, err := sqlite.Open(db, "../../migrations")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	connection := asset.CloudConnection{ID: "connection", Provider: asset.ProviderGCP, Partition: "gcp", Status: asset.ConnectionActive, CreatedAt: now, UpdatedAt: now}
	scope := asset.Scope{ID: "global", ConnectionID: connection.ID, Kind: asset.ScopeGlobal, NativeID: "sample-project/global", CreatedAt: now, UpdatedAt: now}
	kind := r.resourceKind(uptimeType)
	for _, write := range []func() error{
		func() error { return repos.Connections().PutConnection(ctx, connection) },
		func() error { return repos.Inventory().PutScope(ctx, scope) },
		func() error { return repos.Inventory().PutResourceKind(ctx, kind) },
	} {
		if err := write(); err != nil {
			t.Fatal(err)
		}
	}
	registry := identityRegistry(t, r)
	scan := func(name string, wantFailure bool) {
		t.Helper()
		run := asset.ScanRun{ID: asset.ScanRunID(name), ConnectionID: connection.ID, Status: asset.ScanPending, RequestedBy: "test", CreatedAt: now}
		shard := asset.ScanShard{ID: asset.ScanShardID(name), ScanRunID: run.ID, Provider: asset.ProviderGCP, Source: productInventorySource, ScopeID: scope.ID, ResourceKindID: kind.ID, Authoritative: true, Status: asset.ShardPending, CreatedAt: now}
		if err := repos.Inventory().CreateScanRun(ctx, run); err != nil {
			t.Fatal(err)
		}
		if err := repos.Inventory().PutScanShard(ctx, shard); err != nil {
			t.Fatal(err)
		}
		handler := inventory.NewScanHandler(repos, registry, inventory.NewService(repos.Inventory(), inventory.WithClock(func() time.Time { return now })))
		err := handler.Handle(ctx, execution.Job{ID: execution.JobID(name), Type: execution.JobScan, Payload: map[string]any{"scan_shard_id": name}})
		if (err != nil) != wantFailure {
			t.Fatal(name, err)
		}
		finished, err := repos.Inventory().GetScanShard(ctx, shard.ID)
		if err != nil || finished.Coverage.Complete == wantFailure || wantFailure && finished.Status != asset.ShardFailed || !wantFailure && finished.Status != asset.ShardSucceeded {
			t.Fatal(name, finished, err)
		}
		if !wantFailure {
			if err := governance.NewGraphHandler(repos, registry, nil).Handle(ctx, execution.Job{Type: execution.JobGraph, Payload: map[string]any{"scan_run_id": name}}); err != nil {
				t.Fatal(name, err)
			}
		}
		now = now.Add(time.Second)
	}
	var first, current asset.Asset
	for _, phase := range []string{"first", "get-denied", "gone", "detail-drift", "list-null", "list-token", "list-partial", "list-empty", "recovered"} {
		*mode = phase
		failed := phase != "first" && phase != "list-empty" && phase != "recovered"
		if phase == "recovered" {
			(*data)["displayName"] = "Recovered check"
		}
		scan(phase, failed)
		page, err := repos.Inventory().ListAssets(ctx, persistence.ListOptions{Limit: 10})
		if err != nil {
			t.Fatal(err)
		}
		if phase == "list-empty" {
			if len(page.Items) != 0 {
				t.Fatal("complete empty list kept asset")
			}
			old, err := repos.Inventory().GetAsset(ctx, first.ID)
			if err != nil || old.ClosedAt == nil || !old.LastSeenAt.Equal(first.LastSeenAt) {
				t.Fatal(old, err)
			}
			continue
		}
		if len(page.Items) != 1 {
			t.Fatal(phase, page)
		}
		current = page.Items[0]
		if phase == "first" {
			first = current
		}
		if failed && (!current.LastSeenAt.Equal(first.LastSeenAt) || current.Normalized[uptimeReview] != first.Normalized[uptimeReview]) {
			t.Fatal("failure replaced observation", phase)
		}
		if phase == "recovered" && (current.ID != first.ID || current.Normalized[uptimeReview] == first.Normalized[uptimeReview]) {
			t.Fatal("recovery failed to replace review")
		}
		encoded, _ := json.Marshal(current)
		if strings.Contains(string(encoded), "PRIVATE_UPTIME") || strings.Contains(string(encoded), "UFJJVkFURV9VUFRJTUVfQk9EWQ==") {
			t.Fatal("persisted native authentication")
		}
	}
	if err := governance.NewGraphHandler(repos, registry, nil).Handle(ctx, execution.Job{Type: execution.JobGraph, Payload: map[string]any{"scan_run_id": "recovered"}}); err != nil {
		t.Fatal(err)
	}
	planner := cleanup.NewService(repos, registry, cleanup.WithClock(func() time.Time { return now }))
	task, err := planner.CreateTask(ctx, cleanup.CreateTaskRequest{ConnectionID: connection.ID, CreatedBy: "test", Selectors: []plan.CleanupSelector{{Kind: plan.SelectorAsset, AssetID: current.ID}}})
	if err != nil || len(task.Steps) != 1 || len(task.ImpactItems) != 0 {
		t.Fatal(task, err)
	}
	attempt, err := planner.CreateExecution(ctx, cleanup.CreateExecutionRequest{CleanupTaskID: task.Task.ID, ConnectionID: connection.ID, RequestedBy: "test", IdempotencyKey: "uptime-worker", Confirmation: cleanup.ExecutionConfirmation{Acknowledged: true}})
	if err != nil {
		t.Fatal(err)
	}
	job, err := repos.Jobs().ClaimNext(ctx, "uptime-worker", now, time.Minute, execution.JobExecute)
	if err != nil {
		t.Fatal(err)
	}
	for round, want := range []execution.ActionStatus{execution.ActionWaiting, execution.ActionWaiting, execution.ActionReadingBack, execution.ActionSucceeded} {
		repos, err = sqlite.Open(db, "../../migrations")
		if err != nil {
			t.Fatal(err)
		}
		fresh := protocolRuntime(t, r.transport.RoundTrip)
		planner = cleanup.NewService(repos, identityRegistry(t, fresh), cleanup.WithClock(func() time.Time { return now }))
		handler := cleanup.NewExecutionHandler(planner, cleanup.ActionResolverFunc(func(ctx context.Context, value asset.Asset) (cleanup.ActionDriver, error) {
			return fresh.ResolveAction(ctx, value.Identity.ConnectionID, value)
		}))
		if round >= 2 {
			*mode = "gone"
		}
		err = handler.Handle(ctx, job)
		var retry *cleanup.RetryError
		if want == execution.ActionSucceeded && err != nil || want != execution.ActionSucceeded && !errors.As(err, &retry) {
			t.Fatal(round, err)
		}
		actions, err := repos.Executions().ListActions(ctx, attempt.ID)
		if err != nil || len(actions) != 1 || actions[0].Status != want || actions[0].ProviderResult["phase"] != "uptime_delete" || *deletes != 1 {
			t.Fatal(round, actions, err, *deletes)
		}
		now = now.Add(3 * time.Second)
	}
	finished, err := repos.Executions().GetExecution(ctx, attempt.ID)
	if err != nil || finished.Status != execution.ExecutionSucceeded {
		t.Fatal(finished, err)
	}
	deleted, err := repos.Inventory().GetAsset(ctx, current.ID)
	if err != nil || deleted.DeletedAt == nil {
		t.Fatal("missing confirmed tombstone", err)
	}
	if err := repos.Jobs().Complete(ctx, job.ID, "uptime-worker", execution.JobSucceeded, "", now); err != nil {
		t.Fatal(err)
	}
	*mode = "list-empty"
	scan("reconciliation", false)
	page, err := repos.Inventory().ListAssets(ctx, persistence.ListOptions{Limit: 10})
	if err != nil || len(page.Items) != 0 || *deletes != 1 {
		t.Fatal(page, err, *deletes)
	}
}
