package relational

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
	"github.com/loomx-ai/steward/internal/persistence"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestReplaceGraphKeepsOnlyTheCurrentGraph(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "graph.db")), &gorm.Config{TranslateError: true, Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := persistence.Migrate(sqlDB, "sqlite3", filepath.Join("..", "..", "..", "migrations")); err != nil {
		t.Fatal(err)
	}
	store := New(db)
	ctx := context.Background()
	now := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	build := func(revision string, count int) ([]graph.Relationship, []graph.LifecycleBinding) {
		relationships := make([]graph.Relationship, 0, count+1)
		bindings := make([]graph.LifecycleBinding, 0, count)
		for index := range count {
			source, target := asset.AssetID(fmt.Sprintf("source-%d", index)), asset.AssetID("target")
			relationships = append(relationships, graph.Relationship{ID: graph.RelationshipID(fmt.Sprintf("%s-rel-%d", revision, index)), SourceAssetID: source, TargetAssetID: target, Type: graph.RelationshipMemberOf, ObservedAt: now})
			bindings = append(bindings, graph.LifecycleBinding{ID: graph.LifecycleBindingID(fmt.Sprintf("%s-lcb-%d", revision, index)), ControllerAssetID: target, ManagedAssetID: source, ObservedAt: now})
		}
		// A repeated ID must keep its last value instead of failing the batch.
		repeated := relationships[0]
		repeated.Source = "last"
		return append(relationships, repeated), bindings
	}
	for _, revision := range []string{"first", "second"} {
		relationships, bindings := build(revision, 1200)
		if err := store.ReplaceGraph(ctx, "scope", revision, relationships, bindings); err != nil {
			t.Fatal(err)
		}
	}
	for _, table := range []string{"relationships", "lifecycle_bindings"} {
		var total, current int64
		if err := db.Table(table).Count(&total).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Table(table).Where("graph_revision = ? AND closed_at IS NULL", "second").Count(&current).Error; err != nil {
			t.Fatal(err)
		}
		if total != 1200 || current != 1200 {
			t.Fatalf("%s rows = %d (current %d), want only the 1200 current rows", table, total, current)
		}
	}
	values, err := store.ListRelationshipsByAssetIDs(ctx, []asset.AssetID{"source-0"})
	if err != nil || len(values) != 1 || values[0].Source != "last" {
		t.Fatalf("repeated relationship = %#v, err = %v", values, err)
	}
}
