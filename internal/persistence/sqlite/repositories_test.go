package sqlite_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/persistence"
	"github.com/loomx-ai/steward/internal/persistence/contract"
	"github.com/loomx-ai/steward/internal/persistence/sqlite"
)

func TestSQLiteRepositoryContract(t *testing.T) {
	contract.Run(t, func(t *testing.T) persistence.Repositories {
		t.Helper()
		repositories, err := sqlite.Open(filepath.Join(t.TempDir(), "steward.db"), filepath.Join("..", "..", "..", "migrations"))
		if err != nil {
			t.Fatal(err)
		}
		return repositories
	})
}

// Under WAL an autocommit read must not queue behind an open write
// transaction, and reads inside the transaction must see its own writes.
func TestSQLiteReadsRunBesideAnOpenWriteTransaction(t *testing.T) {
	path := filepath.Join(t.TempDir(), "steward.db")
	repositories, err := sqlite.Open(path, filepath.Join("..", "..", "..", "migrations"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path + "-wal"); err != nil {
		t.Fatalf("database is not in WAL mode: %v", err)
	}
	now := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	committed := asset.CloudConnection{ID: "committed", Name: "committed", Provider: asset.ProviderAWS, Status: asset.ConnectionActive, CreatedAt: now, UpdatedAt: now}
	if err := repositories.Connections().PutConnection(context.Background(), committed); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err = repositories.WithTx(ctx, func(tx persistence.Repositories) error {
		pending := committed
		pending.ID, pending.Name = "pending", "pending"
		if err := tx.Connections().PutConnection(ctx, pending); err != nil {
			return err
		}
		if _, err := tx.Connections().GetConnection(ctx, "pending"); err != nil {
			return fmt.Errorf("transaction read of its own write: %w", err)
		}
		if _, err := repositories.Connections().GetConnection(ctx, "committed"); err != nil {
			return fmt.Errorf("read beside the open transaction: %w", err)
		}
		if _, err := repositories.Connections().GetConnection(ctx, "pending"); !errors.Is(err, persistence.ErrNotFound) {
			return fmt.Errorf("uncommitted row visible outside the transaction: %v", err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repositories.Connections().GetConnection(ctx, "pending"); err != nil {
		t.Fatalf("committed row not visible to the read pool: %v", err)
	}
}
