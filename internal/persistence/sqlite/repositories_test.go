package sqlite_test

import (
	"path/filepath"
	"testing"

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
