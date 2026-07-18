package relational

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/persistence"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type queryCountingLogger struct {
	logger.Interface
	enabled *bool
	count   *int
}

func (l queryCountingLogger) Trace(ctx context.Context, begin time.Time, sql func() (string, int64), err error) {
	if *l.enabled {
		(*l.count)++
	}
	l.Interface.Trace(ctx, begin, sql, err)
}

func TestListScanRunListItemsUsesOneScanTasksQuery(t *testing.T) {
	countQueries := false
	queryCount := 0
	db := openScanReadModelTestDB(t, queryCountingLogger{
		Interface: logger.Default.LogMode(logger.Silent),
		enabled:   &countQueries,
		count:     &queryCount,
	})
	store := New(db)
	now := time.Date(2026, 8, 4, 8, 0, 0, 0, time.UTC)
	if err := store.CreateScanRun(context.Background(), asset.ScanRun{
		ID: "scan-1", ConnectionID: "connection-a", Status: asset.ScanSucceeded, CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Table("scan_tasks").Where("id = ?", "scan-1").Updates(map[string]any{
		"resource_count":    77,
		"duration_ms":       12_000,
		"duration_recorded": true,
	}).Error; err != nil {
		t.Fatal(err)
	}

	countQueries = true
	page, err := store.ListScanRunListItems(context.Background(), persistence.ListOptions{
		ConnectionID: "connection-a",
		Limit:        50,
	})
	countQueries = false
	if err != nil {
		t.Fatal(err)
	}
	if queryCount != 1 {
		t.Fatalf("ListScanRunListItems query count = %d, want 1", queryCount)
	}
	if len(page.Items) != 1 ||
		page.Items[0].ScanRun.ID != "scan-1" ||
		page.Items[0].ResourceCount != 77 ||
		page.Items[0].DurationMS != 12_000 ||
		!page.Items[0].DurationRecorded {
		t.Fatalf("scan list items = %#v", page)
	}
}

func TestPutScanShardMaintainsScanTaskResourceCount(t *testing.T) {
	db := openScanReadModelTestDB(t, logger.Default.LogMode(logger.Silent))
	store := New(db)
	ctx := context.Background()
	now := time.Date(2026, 8, 4, 8, 0, 0, 0, time.UTC)
	if err := store.CreateScanRun(ctx, asset.ScanRun{
		ID: "scan-1", ConnectionID: "connection-a", Status: asset.ScanRunning, CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	first := asset.ScanShard{
		ID: "shard-1", ScanTaskID: "scan-1", ScopeID: "scope-1", Source: "test",
		Status: asset.ShardRunning, Coverage: asset.Coverage{ItemCount: 3}, CreatedAt: now,
	}
	if err := store.PutScanShard(ctx, first); err != nil {
		t.Fatal(err)
	}
	first.Coverage.ItemCount = 8
	if err := store.PutScanShard(ctx, first); err != nil {
		t.Fatal(err)
	}
	if err := store.PutScanShard(ctx, asset.ScanShard{
		ID: "shard-2", ScanTaskID: "scan-1", ScopeID: "scope-1", Source: "test",
		Status: asset.ShardSucceeded, Coverage: asset.Coverage{ItemCount: 2}, CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	first.Coverage.ItemCount = 0
	if err := store.PutScanShard(ctx, first); err != nil {
		t.Fatal(err)
	}

	page, err := store.ListScanRunListItems(ctx, persistence.ListOptions{ConnectionID: "connection-a"})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].ResourceCount != 2 {
		t.Fatalf("scan list items = %#v, want resource_count 2", page)
	}
}

func TestScanTaskReadModelMigrationUpgradesExistingDatabase(t *testing.T) {
	db, err := gorm.Open(
		sqlite.Open(filepath.Join(t.TempDir(), "scan-read-model-upgrade.db")),
		&gorm.Config{TranslateError: true, Logger: logger.Default.LogMode(logger.Silent)},
	)
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`CREATE TABLE goose_db_version (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			version_id INTEGER NOT NULL,
			is_applied INTEGER NOT NULL,
			tstamp TIMESTAMP DEFAULT CURRENT_TIMESTAMP
		)`,
		`INSERT INTO goose_db_version (version_id, is_applied)
		 VALUES (1, 1), (2, 1), (3, 1), (4, 1)`,
		`CREATE TABLE scan_tasks (
			id VARCHAR(128) PRIMARY KEY,
			connection_id VARCHAR(128) NOT NULL,
			status VARCHAR(32) NOT NULL,
			scope_mode VARCHAR(32) NOT NULL,
			retry_generation INTEGER NOT NULL DEFAULT 0,
			control_version BIGINT NOT NULL DEFAULT 0,
			created_at TIMESTAMP NOT NULL,
			payload TEXT NOT NULL
		)`,
		`CREATE TABLE scan_shards (
			id VARCHAR(128) PRIMARY KEY,
			scan_task_id VARCHAR(128) NOT NULL,
			target_key VARCHAR(1024) NOT NULL,
			retry_generation INTEGER NOT NULL DEFAULT 0,
			scope_id VARCHAR(128) NOT NULL,
			resource_kind_id VARCHAR(256),
			source VARCHAR(128) NOT NULL,
			authoritative BOOLEAN NOT NULL DEFAULT FALSE,
			status VARCHAR(32) NOT NULL,
			created_at TIMESTAMP NOT NULL,
			payload TEXT NOT NULL
		)`,
		`CREATE TABLE scopes (
			id VARCHAR(128) PRIMARY KEY,
			superseded_by_scope_id VARCHAR(128)
		)`,
		`CREATE TABLE assets (
			id VARCHAR(128) PRIMARY KEY,
			closed_at TIMESTAMP
		)`,
		`CREATE TABLE action_attempts (
			asset_id VARCHAR(128) NOT NULL,
			status VARCHAR(32) NOT NULL,
			updated_at TIMESTAMP NOT NULL,
			payload TEXT NOT NULL
		)`,
		`INSERT INTO assets (id) VALUES ('asset-cleanup-deleted')`,
		`INSERT INTO action_attempts (asset_id, status, updated_at, payload)
		 VALUES (
			'asset-cleanup-deleted', 'succeeded',
			'2026-08-04 08:30:00+00:00', '{"action":"delete"}'
		)`,
		`INSERT INTO scan_tasks (
			id, connection_id, status, scope_mode, created_at, payload
		) VALUES (
			'scan-existing', 'connection-a', 'succeeded', 'selected_regions',
			'2026-08-04 08:00:00+00:00', '{}'
		)`,
	} {
		if err := db.Exec(statement).Error; err != nil {
			t.Fatal(err)
		}
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	if err := persistence.Migrate(sqlDB, "sqlite3", filepath.Join("..", "..", "..", "migrations")); err != nil {
		t.Fatal(err)
	}

	var columns []struct {
		Name string `gorm:"column:name"`
	}
	if err := db.Raw(`PRAGMA table_info(scan_tasks)`).Scan(&columns).Error; err != nil {
		t.Fatal(err)
	}
	columnNames := make(map[string]struct{}, len(columns))
	for _, column := range columns {
		columnNames[column.Name] = struct{}{}
	}
	for _, name := range []string{
		"resource_count",
		"duration_ms",
		"duration_recorded",
		"duration_active",
		"duration_calculated_at",
		"updated_at",
	} {
		if _, exists := columnNames[name]; !exists {
			t.Fatalf("scan_tasks column %q was not added", name)
		}
	}
	var itemCountColumn int
	if err := db.Raw(
		`SELECT COUNT(*) FROM pragma_table_info('scan_shards') WHERE name = 'item_count'`,
	).Scan(&itemCountColumn).Error; err != nil {
		t.Fatal(err)
	}
	if itemCountColumn != 1 {
		t.Fatal("scan_shards.item_count was not added")
	}
	var dirtyColumn int
	if err := db.Raw(
		`SELECT COUNT(*) FROM pragma_table_info('assets') WHERE name = 'dirty'`,
	).Scan(&dirtyColumn).Error; err != nil {
		t.Fatal(err)
	}
	if dirtyColumn != 1 {
		t.Fatal("assets.dirty was not added")
	}
	var deletedAt, closedAt time.Time
	if err := db.Table("assets").
		Select("deleted_at, closed_at").
		Where("id = ?", "asset-cleanup-deleted").
		Row().
		Scan(&deletedAt, &closedAt); err != nil {
		t.Fatal(err)
	}
	wantDeletedAt := time.Date(2026, 8, 4, 8, 30, 0, 0, time.UTC)
	if !deletedAt.Equal(wantDeletedAt) || !closedAt.Equal(wantDeletedAt) {
		t.Fatalf("cleanup deletion backfill deleted_at=%s closed_at=%s, want %s", deletedAt, closedAt, wantDeletedAt)
	}

	var updatedAt time.Time
	if err := db.Table("scan_tasks").
		Select("updated_at").
		Where("id = ?", "scan-existing").
		Scan(&updatedAt).Error; err != nil {
		t.Fatal(err)
	}
	wantUpdatedAt := time.Date(2026, 8, 4, 8, 0, 0, 0, time.UTC)
	if !updatedAt.Equal(wantUpdatedAt) {
		t.Fatalf("existing scan updated_at = %s, want %s", updatedAt, wantUpdatedAt)
	}

	store := New(db)
	createdAt := wantUpdatedAt.Add(time.Hour)
	if err := store.CreateScanRun(context.Background(), asset.ScanRun{
		ID: "scan-new", ConnectionID: "connection-a", Status: asset.ScanPending,
		ScopeMode: asset.ScanSelectedRegions, CreatedAt: createdAt,
	}); err != nil {
		t.Fatalf("create scan after migration: %v", err)
	}
	if err := store.PutScanShard(context.Background(), asset.ScanShard{
		ID: "shard-new", ScanTaskID: "scan-new", TargetKey: "region:me-east-1",
		ScopeID: "scope-me-east-1", Source: "test", Status: asset.ShardPending,
		CreatedAt: createdAt,
	}); err != nil {
		t.Fatalf("create scan shard after migration: %v", err)
	}
}

func TestJobLifecycleMaintainsScanTaskTiming(t *testing.T) {
	db, err := gorm.Open(
		sqlite.Open(filepath.Join(t.TempDir(), "scan-timing.db")),
		&gorm.Config{TranslateError: true, Logger: logger.Default.LogMode(logger.Silent)},
	)
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	if err := persistence.Migrate(sqlDB, "sqlite3", filepath.Join("..", "..", "..", "migrations")); err != nil {
		t.Fatal(err)
	}
	store := New(db)
	ctx := context.Background()
	createdAt := time.Date(2026, 8, 4, 8, 0, 0, 0, time.UTC)
	if err := store.CreateScanRun(ctx, asset.ScanRun{
		ID: "scan-1", ConnectionID: "connection-a", Status: asset.ScanPending, CreatedAt: createdAt,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Enqueue(ctx, execution.Job{
		ID: "job-1", ConnectionID: "connection-a", AggregateType: "scan_task", AggregateID: "scan-1",
		Type: execution.JobScan, Status: execution.JobPending,
		RunAt: createdAt, CreatedAt: createdAt, UpdatedAt: createdAt,
	}); err != nil {
		t.Fatal(err)
	}
	startedAt := createdAt.Add(time.Minute)
	if _, err := store.ClaimNext(ctx, "worker-1", startedAt, time.Minute, execution.JobScan); err != nil {
		t.Fatal(err)
	}
	running, err := store.ListScanRunListItems(ctx, persistence.ListOptions{ConnectionID: "connection-a"})
	if err != nil {
		t.Fatal(err)
	}
	if len(running.Items) != 1 ||
		!running.Items[0].DurationRecorded ||
		!running.Items[0].DurationActive ||
		running.Items[0].DurationMS != 0 {
		t.Fatalf("running timing = %#v", running)
	}

	finishedAt := startedAt.Add(10 * time.Second)
	if err := store.Complete(ctx, "job-1", "worker-1", execution.JobSucceeded, "", finishedAt); err != nil {
		t.Fatal(err)
	}
	finished, err := store.ListScanRunListItems(ctx, persistence.ListOptions{ConnectionID: "connection-a"})
	if err != nil {
		t.Fatal(err)
	}
	if len(finished.Items) != 1 ||
		!finished.Items[0].DurationRecorded ||
		finished.Items[0].DurationActive ||
		finished.Items[0].DurationMS != 10_000 ||
		!finished.Items[0].UpdatedAt.Equal(finishedAt) {
		t.Fatalf("finished timing = %#v", finished)
	}
}

func openScanReadModelTestDB(t *testing.T, configuredLogger logger.Interface) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(
		sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())),
		&gorm.Config{Logger: configuredLogger},
	)
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`CREATE TABLE scan_tasks (
			id TEXT PRIMARY KEY,
			connection_id TEXT NOT NULL,
			status TEXT NOT NULL,
			scope_mode TEXT NOT NULL,
			retry_generation INTEGER NOT NULL,
			control_version INTEGER NOT NULL,
			resource_count INTEGER NOT NULL DEFAULT 0,
			duration_ms INTEGER NOT NULL DEFAULT 0,
			duration_recorded BOOLEAN NOT NULL DEFAULT FALSE,
			duration_active BOOLEAN NOT NULL DEFAULT FALSE,
			duration_calculated_at TIMESTAMP,
			created_at TIMESTAMP NOT NULL,
			updated_at TIMESTAMP NOT NULL,
			payload TEXT NOT NULL
		)`,
		`CREATE TABLE scan_shards (
			id TEXT PRIMARY KEY,
			scan_task_id TEXT NOT NULL,
			target_key TEXT NOT NULL,
			retry_generation INTEGER NOT NULL,
			scope_id TEXT NOT NULL,
			resource_kind_id TEXT,
			source TEXT NOT NULL,
			authoritative BOOLEAN NOT NULL,
			status TEXT NOT NULL,
			item_count INTEGER NOT NULL DEFAULT 0,
			created_at TIMESTAMP NOT NULL,
			payload TEXT NOT NULL
		)`,
		`CREATE TABLE scopes (
			id TEXT PRIMARY KEY,
			superseded_by_scope_id TEXT
		)`,
	} {
		if err := db.Exec(statement).Error; err != nil {
			t.Fatal(err)
		}
	}
	return db
}
