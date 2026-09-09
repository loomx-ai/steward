package persistence_test

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/loomx-ai/steward/internal/persistence"
	_ "github.com/mattn/go-sqlite3"
)

func TestEmbeddedMigrationsAndExplicitOverride(t *testing.T) {
	t.Chdir(t.TempDir())
	open := func() *sql.DB {
		db, err := sql.Open("sqlite3", ":memory:")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { db.Close() })
		return db
	}
	db := open()
	if err := persistence.Migrate(db, "sqlite3", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("SELECT id FROM cloud_connections"); err != nil {
		t.Fatal(err)
	}
	if err := persistence.Migrate(db, "sqlite3", ""); err != nil {
		t.Fatalf("restarting must not rerun applied migrations: %v", err)
	}
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "00001_custom.sql"), []byte("-- +goose Up\nCREATE TABLE custom_schema (id INTEGER);\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	custom := open()
	if err := persistence.Migrate(custom, "sqlite3", directory); err != nil {
		t.Fatal(err)
	}
	if _, err := custom.Exec("SELECT id FROM custom_schema"); err != nil {
		t.Fatal(err)
	}
	if err := persistence.Migrate(open(), "sqlite3", "missing-directory"); err == nil {
		t.Fatal("an explicit missing directory must fail, not fall back to embedded migrations")
	}
	if err := persistence.Migrate(open(), "sqlite3", ""); err != nil {
		t.Fatalf("external overrides must not leak into subsequent embedded migrations: %v", err)
	}
}
