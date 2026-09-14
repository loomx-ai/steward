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
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/internal/persistence"
	"github.com/loomx-ai/steward/internal/persistence/sqlite"
	sqlitedriver "gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type monitoringDependencyContributors struct{ r *Runtime }

func (r monitoringDependencyContributors) ResolveContributors(ctx context.Context, connection asset.CloudConnection, _ []asset.Asset) ([]governance.Contributor, error) {
	contributor, err := r.r.MonitoringDependencies(ctx, connection.ID)
	return []governance.Contributor{contributor}, err
}
func TestMonitoringDependencySQLitePlanExecutionRestart(t *testing.T) {
	s := monitoringDependencyFixture(t)
	ctx := t.Context()
	dsn := filepath.Join(t.TempDir(), "monitoring.db")
	open := func() (*sqlite.Repositories, func()) {
		t.Helper()
		db, err := gorm.Open(sqlitedriver.Open(dsn), &gorm.Config{TranslateError: true, Logger: logger.Default.LogMode(logger.Silent)})
		if err != nil {
			t.Fatal(err)
		}
		sqlDB, err := db.DB()
		if err != nil {
			t.Fatal(err)
		}
		sqlDB.SetMaxOpenConns(1)
		if err := persistence.Migrate(sqlDB, "sqlite3", "../../migrations"); err != nil {
			t.Fatal(err)
		}
		return sqlite.New(db), func() {
			if err := sqlDB.Close(); err != nil {
				t.Fatal(err)
			}
		}
	}
	repos, closeDB := open()
	now := time.Now().UTC()
	connection := asset.CloudConnection{ID: "connection", Provider: asset.ProviderGCP, Partition: "gcp", Status: asset.ConnectionActive, CreatedAt: now, UpdatedAt: now}
	scope := asset.Scope{ID: "global", ConnectionID: connection.ID, Kind: asset.ScopeGlobal, NativeID: "sample-project/global", CreatedAt: now, UpdatedAt: now}
	if err := repos.Connections().PutConnection(ctx, connection); err != nil {
		t.Fatal(err)
	}
	if err := repos.Inventory().PutScope(ctx, scope); err != nil {
		t.Fatal(err)
	}
	for _, value := range []*asset.Asset{&s.request.Asset, &s.policy} {
		kind := s.r.resourceKind(value.Identity.NativeType)
		if err := repos.Inventory().PutResourceKind(ctx, kind); err != nil {
			t.Fatal(err)
		}
		value.ResourceKindID, value.ScopeID, value.FirstSeenAt, value.LastSeenAt = kind.ID, scope.ID, now, now
		if err := repos.Inventory().PutAsset(ctx, *value); err != nil {
			t.Fatal(err)
		}
	}
	if err := repos.Inventory().CreateScanRun(ctx, asset.ScanRun{ID: "monitoring", ConnectionID: connection.ID, Status: asset.ScanSucceeded, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := repos.Inventory().PutScanShard(ctx, asset.ScanShard{ID: "monitoring", ScanRunID: "monitoring", Provider: asset.ProviderGCP, ScopeID: scope.ID, ResourceKindID: s.request.Asset.ResourceKindID, Source: productInventorySource, Status: asset.ShardSucceeded, Authoritative: true, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	handler := governance.NewGraphHandler(repos, identityRegistry(t, s.r), monitoringDependencyContributors{s.r})
	if err := handler.Handle(ctx, execution.Job{Type: execution.JobGraph, Payload: map[string]any{"scan_run_id": "monitoring"}}); err != nil {
		t.Fatal(err)
	}
	service := cleanup.NewService(repos, identityRegistry(t, s.r), cleanup.WithClock(func() time.Time { return now }))
	blocked, err := service.CreateTask(ctx, cleanup.CreateTaskRequest{ConnectionID: connection.ID, CreatedBy: "test", Selectors: []plan.CleanupSelector{{Kind: plan.SelectorAsset, AssetID: s.request.Asset.ID}}})
	if err != nil || len(blocked.Task.Blockers) == 0 {
		t.Fatal("unselected policy was not blocked", blocked, err)
	}
	task, err := service.CreateTask(ctx, cleanup.CreateTaskRequest{ConnectionID: connection.ID, CreatedBy: "test", Selectors: []plan.CleanupSelector{{Kind: plan.SelectorAsset, AssetID: s.request.Asset.ID}, {Kind: plan.SelectorAsset, AssetID: s.policy.ID}}})
	if err != nil || len(task.Task.Blockers) != 0 || len(task.Steps) != 2 || task.Steps[0].AssetID != s.policy.ID || task.Steps[1].AssetID != s.request.Asset.ID {
		t.Fatal(task, err)
	}
	attempt, err := service.CreateExecution(ctx, cleanup.CreateExecutionRequest{CleanupTaskID: task.Task.ID, ConnectionID: connection.ID, RequestedBy: "test", IdempotencyKey: "monitoring-order", Confirmation: cleanup.ExecutionConfirmation{Acknowledged: true}})
	if err != nil {
		t.Fatal(err)
	}
	job, err := repos.Jobs().ClaimNext(ctx, "monitoring-worker", now, time.Minute, execution.JobExecute)
	if err != nil {
		t.Fatal(err)
	}
	second, err := repos.Jobs().ClaimNext(ctx, "monitoring-worker", now, time.Minute, execution.JobExecute)
	if err != nil || second.ID == job.ID {
		t.Fatal("missing independent prerequisite job", err)
	}
	jobs := []execution.Job{job, second}
	done := map[execution.JobID]bool{}
	finished := false
	for round := 0; round < 12; round++ {
		closeDB()
		repos, closeDB = open()
		fresh := protocolRuntime(t, s.r.transport.RoundTrip)
		service = cleanup.NewService(repos, identityRegistry(t, fresh), cleanup.WithClock(func() time.Time { return now }))
		worker := cleanup.NewExecutionHandler(service, cleanup.ActionResolverFunc(func(ctx context.Context, value asset.Asset) (cleanup.ActionDriver, error) {
			return fresh.ResolveAction(ctx, value.Identity.ConnectionID, value)
		}))
		if *s.uptimeDeletes > 0 {
			*s.uptimeMode = "gone"
		}
		for _, job := range jobs {
			if done[job.ID] {
				continue
			}
			err := worker.Handle(ctx, job)
			var retry *cleanup.RetryError
			if err != nil && !errors.As(err, &retry) {
				t.Fatal(round, err)
			}
			if err == nil {
				done[job.ID] = true
				if err := repos.Jobs().Complete(ctx, job.ID, "monitoring-worker", execution.JobSucceeded, "", now); err != nil {
					t.Fatal(err)
				}
			}
		}
		if *s.uptimeDeletes > 0 && (s.policyDeletes != 1 || s.data != nil) {
			t.Fatal("check deleted before policy absence")
		}
		current, err := repos.Executions().GetExecution(ctx, attempt.ID)
		if err != nil {
			t.Fatal(err)
		}
		if current.Status == execution.ExecutionSucceeded {
			finished = true
			break
		}
		now = now.Add(3 * time.Second)
	}
	if !finished || s.policyDeletes != 1 || *s.uptimeDeletes != 1 {
		actions, _ := repos.Executions().ListActions(ctx, attempt.ID)
		payload, _ := json.MarshalIndent(actions, "", "  ")
		t.Fatal("restart lost ordered deletion", finished, s.policyDeletes, *s.uptimeDeletes, string(payload))
	}
	actions, err := repos.Executions().ListActions(ctx, attempt.ID)
	if err != nil || len(actions) != 2 {
		t.Fatal(actions, err)
	}
	for _, action := range actions {
		encoded, _ := json.Marshal(action)
		if action.Status != execution.ActionSucceeded || strings.Contains(string(encoded), "PRIVATE_") {
			t.Fatal(action)
		}
	}
	for _, id := range []asset.AssetID{s.policy.ID, s.request.Asset.ID} {
		value, err := repos.Inventory().GetAsset(ctx, id)
		if err != nil || value.DeletedAt == nil {
			t.Fatal("missing verified tombstone", id, err)
		}
	}
	closeDB()
}
