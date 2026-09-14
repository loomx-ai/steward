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
	for _, logging := range []bool{false, true} {
		name := "metric"
		if logging {
			name = "logging"
		}
		t.Run(name, func(t *testing.T) { testMonitoringDependencySQLitePlanExecutionRestart(t, logging) })
	}
}
func testMonitoringDependencySQLitePlanExecutionRestart(t *testing.T, logging bool) {
	s := monitoringDependencyFixture(t)
	if logging {
		s.loggingFilter(t, `labels.check_id="ＰＵＢＬＩＣ-ＣＨＥＣＫ" AND NOT jsonPayload.PRIVATE_STATE="ok"`)
	}
	ctx := t.Context()
	dsn := filepath.Join(t.TempDir(), "monitoring.db")

	repos, closeDB := monitoringSQLite(t, dsn)
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
		repos, closeDB = monitoringSQLite(t, dsn)
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

func monitoringSQLite(t *testing.T, dsn string) (*sqlite.Repositories, func()) {
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

func TestLoggingRoutingSQLitePreservesBlockedPlanAfterReadFailure(t *testing.T) {
	s := newLoggingRoutingScenario(t)
	dsn := filepath.Join(t.TempDir(), "routing.db")
	repos, closeDB := monitoringSQLite(t, dsn)
	ctx := t.Context()
	now := time.Now().UTC()
	connection := asset.CloudConnection{ID: "connection", Provider: asset.ProviderGCP, Partition: "gcp", Status: asset.ConnectionActive, CreatedAt: now, UpdatedAt: now}
	scope := asset.Scope{ID: "global", ConnectionID: connection.ID, Kind: asset.ScopeGlobal, NativeID: "sample-project/global", CreatedAt: now, UpdatedAt: now}
	kind := s.r.resourceKind(uptimeType)
	value := s.request.Asset
	value.ResourceKindID, value.ScopeID, value.FirstSeenAt, value.LastSeenAt = kind.ID, scope.ID, now, now
	for _, err := range []error{repos.Connections().PutConnection(ctx, connection), repos.Inventory().PutScope(ctx, scope), repos.Inventory().PutResourceKind(ctx, kind), repos.Inventory().PutAsset(ctx, value)} {
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := repos.Inventory().CreateScanRun(ctx, asset.ScanRun{ID: "routing", ConnectionID: connection.ID, Status: asset.ScanSucceeded, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := repos.Inventory().PutScanShard(ctx, asset.ScanShard{ID: "routing", ScanRunID: "routing", Provider: asset.ProviderGCP, ScopeID: scope.ID, ResourceKindID: kind.ID, Source: productInventorySource, Status: asset.ShardSucceeded, Authoritative: true, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	rebuild := func() error {
		fresh := protocolRuntime(t, s.r.transport.RoundTrip)
		fresh.transport = s.r.transport // Preserve explicit ancestry responses across client restart.
		handler := governance.NewGraphHandler(repos, identityRegistry(t, fresh), monitoringDependencyContributors{fresh})
		return handler.Handle(ctx, execution.Job{Type: execution.JobGraph, Payload: map[string]any{"scan_run_id": "routing"}})
	}
	if err := rebuild(); err != nil {
		t.Fatal(err)
	}
	for round := 0; round < 3; round++ {
		closeDB()
		repos, closeDB = monitoringSQLite(t, dsn)
		if round == 1 {
			s.mode = "ancestor-denied"
			if err := rebuild(); err == nil {
				t.Fatal("failed native ancestry erased graph")
			}
		}
		if round == 2 {
			s.mode = ""
			s.sinks["projects/sample-project"] = nil
			if err := rebuild(); err != nil {
				t.Fatal(err)
			}
		}
		unresolved, err := repos.Graph().ListUnresolvedByConnection(ctx, connection.ID)
		if err != nil {
			t.Fatal(err)
		}
		wantBlocked := round < 2
		if wantBlocked && (len(unresolved) != 1 || !unresolved[0].BlocksCleanup) || !wantBlocked && len(unresolved) != 0 {
			t.Fatal("incorrect persisted routing boundary", round, unresolved)
		}
		encoded, _ := json.Marshal(unresolved)
		if strings.Contains(string(encoded), "PRIVATE_") || strings.Contains(string(encoded), "writer@example") {
			t.Fatal("persisted private sink configuration")
		}
		service := cleanup.NewService(repos, identityRegistry(t, s.r), cleanup.WithClock(func() time.Time { return now }))
		task, err := service.CreateTask(ctx, cleanup.CreateTaskRequest{ConnectionID: connection.ID, CreatedBy: "test", Selectors: []plan.CleanupSelector{{Kind: plan.SelectorAsset, AssetID: value.ID}}})
		if err != nil || (len(task.Task.Blockers) > 0) != wantBlocked {
			t.Fatal("restart changed plan safety", round, task, err)
		}
	}
	closeDB()
	if *s.uptimeDeletes != 0 || s.policyDeletes != 0 {
		t.Fatal("graph/plan mutated cloud")
	}
}

func TestNotificationChannelSQLiteDependencyHistory(t *testing.T) {
	s, _, _, deletes := channelDependencyFixture(t)
	ctx := t.Context()
	dsn := filepath.Join(t.TempDir(), "channels.db")
	repos, closeDB := monitoringSQLite(t, dsn)
	now := time.Now().UTC()
	connection := asset.CloudConnection{ID: "connection", Provider: asset.ProviderGCP, Partition: "gcp", Status: asset.ConnectionActive, CreatedAt: now, UpdatedAt: now}
	scope := asset.Scope{ID: "global", ConnectionID: connection.ID, Kind: asset.ScopeGlobal, NativeID: "sample-project/global", CreatedAt: now, UpdatedAt: now}
	kind := s.r.resourceKind(notificationChannelType)
	value := s.request.Asset
	value.ResourceKindID, value.ScopeID, value.FirstSeenAt, value.LastSeenAt = kind.ID, scope.ID, now, now
	for _, err := range []error{repos.Connections().PutConnection(ctx, connection), repos.Inventory().PutScope(ctx, scope), repos.Inventory().PutResourceKind(ctx, kind), repos.Inventory().PutAsset(ctx, value)} {
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := repos.Inventory().CreateScanRun(ctx, asset.ScanRun{ID: "channels", ConnectionID: connection.ID, Status: asset.ScanSucceeded, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := repos.Inventory().PutScanShard(ctx, asset.ScanShard{ID: "channels", ScanRunID: "channels", Provider: asset.ProviderGCP, ScopeID: scope.ID, ResourceKindID: kind.ID, Source: productInventorySource, Status: asset.ShardSucceeded, Authoritative: true, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	for round := 0; round < 4; round++ {
		if round > 0 {
			closeDB()
			repos, closeDB = monitoringSQLite(t, dsn)
		}
		if round == 1 {
			policyKind := s.r.resourceKind(alertPolicyType)
			if err := repos.Inventory().PutResourceKind(ctx, policyKind); err != nil {
				t.Fatal(err)
			}
			s.policy.ResourceKindID, s.policy.ScopeID, s.policy.FirstSeenAt, s.policy.LastSeenAt = policyKind.ID, scope.ID, now, now
			if err := repos.Inventory().PutAsset(ctx, s.policy); err != nil {
				t.Fatal(err)
			}
		}
		if round == 2 {
			s.mode = "get-denied"
		}
		if round == 3 {
			s.mode = ""
			s.data["notificationChannels"] = []any{}
		}
		fresh := protocolRuntime(t, s.r.transport.RoundTrip)
		handler := governance.NewGraphHandler(repos, identityRegistry(t, fresh), monitoringDependencyContributors{fresh})
		err := handler.Handle(ctx, execution.Job{Type: execution.JobGraph, Payload: map[string]any{"scan_run_id": "channels"}})
		if (err != nil) != (round == 2) {
			t.Fatal(round, err)
		}
		unresolved, err := repos.Graph().ListUnresolvedByConnection(ctx, connection.ID)
		if err != nil {
			t.Fatal(err)
		}
		edges, err := repos.Graph().ListRelationshipsForAsset(ctx, connection.ID, value.ID)
		if err != nil {
			t.Fatal(err)
		}
		wantEdges, wantUnresolved := 0, 0
		if round == 0 {
			wantUnresolved = 1
		}
		if round == 1 || round == 2 {
			wantEdges = 1
		}
		if len(edges) != wantEdges || len(unresolved) != wantUnresolved {
			t.Fatal("restart or failed read changed authoritative graph", round, edges, unresolved)
		}
		b, _ := json.Marshal(map[string]any{"edges": edges, "unresolved": unresolved})
		if strings.Contains(string(b), "PRIVATE_") {
			t.Fatal("private channel or policy configuration persisted")
		}
	}
	closeDB()
	if *deletes != 0 || s.policyDeletes != 0 {
		t.Fatal("graph mutated cloud")
	}
}
