package relational

import (
	"context"
	"fmt"
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

type statementLogger struct {
	logger.Interface
	statements *[]string
}

func (l statementLogger) Trace(ctx context.Context, begin time.Time, sql func() (string, int64), err error) {
	statement, _ := sql()
	*l.statements = append(*l.statements, statement)
}

// A common term pages through the list's ordered index instead of sorting
// every trigram hit; both paths must return the same rows.
func TestKeywordSearchWalksOrderedIndexForCommonTerms(t *testing.T) {
	var statements []string
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "search.db")), &gorm.Config{TranslateError: true, Logger: statementLogger{logger.Default.LogMode(logger.Silent), &statements}})
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
	now := time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)
	values := make([]asset.Asset, 0, keywordIndexProbeLimit+1)
	for index := range keywordIndexProbeLimit + 1 {
		name := fmt.Sprintf("Common-%d", index)
		if index == 7 {
			name = "rare-one"
		}
		values = append(values, asset.Asset{
			ID: asset.AssetID(fmt.Sprintf("ast-%05d", index)), ResourceKindID: "kind",
			Identity:    asset.Identity{Provider: asset.ProviderAliCloud, ConnectionID: "conn", NativeType: "t", NativeID: fmt.Sprintf("n-%d", index)},
			Name:        name,
			FirstSeenAt: now.Add(time.Duration(keywordIndexProbeLimit-index) * time.Second), LastSeenAt: now,
		})
	}
	if err := store.PutAssets(ctx, values); err != nil {
		t.Fatal(err)
	}
	search := func(term string) ([]asset.AssetID, string) {
		t.Helper()
		statements = nil
		var ids []asset.AssetID
		options := persistence.ListOptions{ConnectionID: "conn", Query: term, Limit: 500}
		for {
			page, err := store.ListAssets(ctx, options)
			if err != nil {
				t.Fatal(err)
			}
			for _, value := range page.Items {
				ids = append(ids, value.ID)
			}
			if page.NextCursor == "" {
				return ids, strings.Join(statements, "\n")
			}
			options.Cursor = page.NextCursor
		}
	}
	common, sql := search("COMMON")
	if len(common) != keywordIndexProbeLimit || common[0] != asset.AssetID(fmt.Sprintf("ast-%05d", keywordIndexProbeLimit)) || common[len(common)-1] != "ast-00000" {
		t.Fatalf("common term matched %d assets from %s to %s", len(common), common[0], common[len(common)-1])
	}
	if !strings.Contains(sql, "assets.search_text GLOB") {
		t.Fatalf("common term did not scan search_text:\n%s", sql)
	}
	rare, sql := search("rare-")
	if fmt.Sprint(rare) != "[ast-00007]" || strings.Contains(sql, "assets.search_text GLOB") {
		t.Fatalf("rare term = %v via\n%s", rare, sql)
	}
	// Shorter than a trigram, the index cannot help: skip the probe.
	short, sql := search("e-")
	if fmt.Sprint(short) != "[ast-00007]" || strings.Contains(sql, "count(*)") || !strings.Contains(sql, "assets.search_text GLOB") {
		t.Fatalf("short term = %v via\n%s", short, sql)
	}
}
