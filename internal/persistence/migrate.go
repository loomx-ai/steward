package persistence

import (
	"database/sql"
	"fmt"
	"sync"

	"github.com/pressly/goose/v3"
)

// goose keeps the selected dialect in package-global state. Keep dialect
// selection and migration in one critical section so concurrent repository
// opens cannot change another migration's SQL dialect or race internally.
var migrationMu sync.Mutex

func Migrate(db *sql.DB, dialect, directory string) error {
	migrationMu.Lock()
	defer migrationMu.Unlock()
	if err := goose.SetDialect(dialect); err != nil {
		return fmt.Errorf("set migration dialect: %w", err)
	}
	if err := goose.Up(db, directory); err != nil {
		return fmt.Errorf("run migrations: %w", err)
	}
	return nil
}
