package persistence

import (
	"database/sql"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"sync"

	"github.com/loomx-ai/steward/migrations"
	"github.com/pressly/goose/v3"
)

// goose keeps the selected dialect in package-global state. Keep dialect
// selection and migration in one critical section so concurrent repository
// opens cannot change another migration's SQL dialect or race internally.
var migrationMu sync.Mutex

// Migrate applies the shared migrations plus those written for dialect. A
// migration that only one dialect can express is named
// NNNNN_name.<dialect>.sql (sqlite3 or postgres); the other dialect never
// sees it, so both keep one version sequence.
func Migrate(db *sql.DB, dialect, directory string) error {
	migrationMu.Lock()
	defer migrationMu.Unlock()
	var files fs.FS = migrations.Files
	if directory != "" {
		files = os.DirFS(directory)
	}
	goose.SetBaseFS(dialectFS{files: files, dialect: dialect})
	defer goose.SetBaseFS(nil)
	if err := goose.SetDialect(dialect); err != nil {
		return fmt.Errorf("set migration dialect: %w", err)
	}
	if err := goose.Up(db, "."); err != nil {
		return fmt.Errorf("run migrations: %w", err)
	}
	return nil
}

// dialectFS hides the migrations written for other dialects.
type dialectFS struct {
	files   fs.FS
	dialect string
}

func (f dialectFS) Open(name string) (fs.File, error) { return f.files.Open(name) }

func (f dialectFS) ReadDir(name string) ([]fs.DirEntry, error) {
	entries, err := fs.ReadDir(f.files, name)
	if err != nil {
		return nil, err
	}
	result := entries[:0]
	for _, entry := range entries {
		base := strings.TrimSuffix(entry.Name(), ".sql")
		if index := strings.LastIndexByte(base, '.'); index >= 0 && base[index+1:] != f.dialect {
			continue
		}
		result = append(result, entry)
	}
	return result, nil
}
