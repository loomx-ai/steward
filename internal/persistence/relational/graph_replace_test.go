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

func TestReplaceGraphRewritesOnlyChangedRows(t *testing.T) {
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
	first, second := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 10, 2, 0, 0, 0, 123456789, time.UTC)
	own := time.Date(2026, 9, 1, 0, 0, 0, 0, time.FixedZone("x", 3600))
	build := func(revision string, observedAt time.Time, ids ...string) []graph.Relationship {
		var values []graph.Relationship
		for _, id := range ids {
			values = append(values, graph.Relationship{ID: graph.RelationshipID(id), SourceAssetID: asset.AssetID("src-" + id), TargetAssetID: "target", Type: graph.RelationshipMemberOf, Source: "spec", GraphRevision: revision, ObservedAt: observedAt})
		}
		return values
	}
	firstGraph := append(build("first", first, "kept", "changed", "removed", "closed"), graph.Relationship{ID: "own-time", SourceAssetID: "src-own", TargetAssetID: "target", Type: graph.RelationshipMemberOf, GraphRevision: "first", ObservedAt: own})
	if err := store.ReplaceGraph(ctx, "scope", "first", firstGraph, []graph.LifecycleBinding{{ID: "binding", ControllerAssetID: "target", ManagedAssetID: "src-kept", GraphRevision: "first", ObservedAt: first}}); err != nil {
		t.Fatal(err)
	}
	closedAt := first
	if err := store.PutAsset(ctx, asset.Asset{ID: "src-closed", Identity: asset.Identity{Provider: asset.ProviderAliCloud, ConnectionID: "conn", NativeType: "t", NativeID: "closed"}, FirstSeenAt: first, LastSeenAt: first, ClosedAt: &closedAt}); err != nil {
		t.Fatal(err)
	}
	secondGraph := append(build("second", second, "kept", "changed", "added", "closed"), graph.Relationship{ID: "own-time", SourceAssetID: "src-own", TargetAssetID: "target", Type: graph.RelationshipMemberOf, GraphRevision: "second", ObservedAt: own})
	secondGraph[1].Confidence = 1
	if err := store.ReplaceGraph(ctx, "scope", "second", secondGraph, []graph.LifecycleBinding{{ID: "binding", ControllerAssetID: "target", ManagedAssetID: "src-kept", GraphRevision: "second", ObservedAt: second}}); err != nil {
		t.Fatal(err)
	}
	// Row-level graph_revision records the build that last wrote the row.
	var written []string
	if err := db.Table("relationships").Where("graph_revision = ?", "second").Order("id").Pluck("id", &written).Error; err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(written) != "[added changed]" {
		t.Fatalf("rewritten rows = %v, want only the added and changed rows", written)
	}
	values, err := store.ListRelationshipsByScope(ctx, "scope")
	if err != nil {
		t.Fatal(err)
	}
	got := map[graph.RelationshipID]graph.Relationship{}
	for _, value := range values {
		got[value.ID] = value
	}
	if len(got) != 4 || got["removed"].ID != "" || got["closed"].ID != "" {
		t.Fatalf("open relationships = %v, want kept, changed, added and own-time", values)
	}
	for _, id := range []graph.RelationshipID{"kept", "changed", "added", "own-time"} {
		want := second
		if id == "own-time" {
			want = own
		}
		if value := got[id]; value.GraphRevision != "second" || value.ObservedAt.Format(time.RFC3339Nano) != want.Format(time.RFC3339Nano) {
			t.Fatalf("relationship %s = %+v, want revision second observed %v", id, value, want)
		}
	}
	bindings, err := store.ListLifecycleBindingsForAsset(ctx, "conn", "src-kept")
	if err == nil {
		t.Fatalf("bindings for a missing asset = %v", bindings)
	}
	bindings, err = store.ListLifecycleBindingsByScope(ctx, "scope")
	if err != nil || len(bindings) != 1 || bindings[0].GraphRevision != "second" || !bindings[0].ObservedAt.Equal(second) {
		t.Fatalf("bindings = %+v, err = %v", bindings, err)
	}
}
