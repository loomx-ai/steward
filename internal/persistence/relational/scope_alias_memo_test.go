package relational

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/persistence"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type aliasQueryCounter struct {
	logger.Interface
	count *int
}

func (l aliasQueryCounter) Trace(ctx context.Context, begin time.Time, sql func() (string, int64), err error) {
	if statement, _ := sql(); strings.Contains(statement, "superseded_by_scope_id IS NOT NULL") {
		(*l.count)++
	}
	l.Interface.Trace(ctx, begin, sql, err)
}

func TestScopeAliasesAreReadOncePerTransactionUntilConsolidation(t *testing.T) {
	aliasQueries := 0
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "aliases.db")), &gorm.Config{
		TranslateError: true,
		Logger:         aliasQueryCounter{Interface: logger.Default.LogMode(logger.Silent), count: &aliasQueries},
	})
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
	for _, scope := range []asset.Scope{
		{ID: "scope-canonical", ConnectionID: "conn", Kind: asset.ScopeGlobal, NativeID: "global", CreatedAt: now, UpdatedAt: now},
		{ID: "scope-old", ConnectionID: "conn", Kind: asset.ScopeGlobal, NativeID: "legacy-global", CreatedAt: now, UpdatedAt: now},
	} {
		if err := store.PutScope(ctx, scope); err != nil {
			t.Fatal(err)
		}
	}
	newAsset := func(id string) asset.Asset {
		identity, err := asset.NewIdentity("alicloud", "public", "conn", "ACS::ECS::Instance", id)
		if err != nil {
			t.Fatal(err)
		}
		return asset.Asset{ID: asset.AssetID(id), Identity: identity, ScopeID: "scope-old", ResourceKindID: "kind", FirstSeenAt: now, LastSeenAt: now}
	}
	err = store.WithTx(ctx, func(repositories persistence.Repositories) error {
		inventory := repositories.Inventory()
		for _, id := range []string{"asset-a", "asset-b"} {
			if err := inventory.PutAsset(ctx, newAsset(id)); err != nil {
				return err
			}
		}
		if _, err := inventory.GetAsset(ctx, "asset-a"); err != nil {
			return err
		}
		if aliasQueries != 1 {
			t.Fatalf("alias queries before consolidation = %d, want 1", aliasQueries)
		}
		if err := inventory.ConsolidateScopes(ctx, "scope-canonical", []asset.ScopeID{"scope-old"}, ""); err != nil {
			return err
		}
		if err := inventory.PutAsset(ctx, newAsset("asset-c")); err != nil {
			return err
		}
		stored, err := inventory.GetAssetByIdentity(ctx, newAsset("asset-c").Identity)
		if err != nil {
			return err
		}
		if stored.ScopeID != "scope-canonical" {
			t.Fatalf("asset written after consolidation kept scope %q", stored.ScopeID)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if aliasQueries != 2 {
		t.Fatalf("alias queries = %d, want 2 (one per alias generation)", aliasQueries)
	}
}

// A row written against a scope that is consolidated meanwhile still lists
// under the canonical scope, also through a chain of consolidations.
func TestListAssetsResolvesConsolidatedScopes(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "aliases.db")), &gorm.Config{TranslateError: true, Logger: logger.Default.LogMode(logger.Silent)})
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
	for _, id := range []asset.ScopeID{"scope-a", "scope-b", "scope-c"} {
		if err := store.PutScope(ctx, asset.Scope{ID: id, ConnectionID: "conn", Kind: asset.ScopeGlobal, NativeID: string(id), CreatedAt: now, UpdatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.ConsolidateScopes(ctx, "scope-b", []asset.ScopeID{"scope-a"}, ""); err != nil {
		t.Fatal(err)
	}
	if err := store.ConsolidateScopes(ctx, "scope-c", []asset.ScopeID{"scope-b"}, ""); err != nil {
		t.Fatal(err)
	}
	// What a writer that read the aliases before both consolidations stores.
	if err := db.Table("assets").Create(&assetRow{
		ID: "asset-stale", Provider: "alicloud", PartitionName: "public", ConnectionID: "conn", NativeType: "ACS::ECS::Instance", NativeID: "i-stale",
		ScopeID: "scope-a", ResourceKindID: "kind", FirstSeenAt: now, LastSeenAt: now,
		Payload: `{"id":"asset-stale","scope_id":"scope-a","identity":{"native_id":"i-stale"}}`,
	}).Error; err != nil {
		t.Fatal(err)
	}
	page, err := store.ListAssets(ctx, persistence.ListOptions{ConnectionID: "conn"})
	if err != nil || len(page.Items) != 1 || page.Items[0].ScopeID != "scope-c" {
		t.Fatalf("listed assets = %#v, err = %v", page.Items, err)
	}
	listed, err := store.ListActiveAssetsByConnection(ctx, "conn", "")
	if err != nil || len(listed) != 1 || listed[0].ScopeID != "scope-c" {
		t.Fatalf("active assets = %#v, err = %v", listed, err)
	}
}
