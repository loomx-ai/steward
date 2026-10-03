package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/loomx-ai/steward/internal/persistence"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// readConns bounds the read-only pool that serves autocommit SELECTs beside
// the single writer connection.
const readConns = 4

// Open opens the database in WAL mode. go-sqlite3 already defaults every
// connection to synchronous=NORMAL and busy_timeout=5000; WAL is what lets a
// commit skip the rollback-journal fsyncs and lets readers proceed while the
// writer holds a long scan transaction.
func Open(dsn, migrationsDirectory string) (*Repositories, error) {
	db, err := gorm.Open(sqlite.Open(withParam(dsn, "_journal_mode", "WAL")), &gorm.Config{TranslateError: true, Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("get sqlite database: %w", err)
	}
	sqlDB.SetMaxOpenConns(1)
	if err := persistence.Migrate(sqlDB, "sqlite3", migrationsDirectory); err != nil {
		_ = sqlDB.Close()
		return nil, err
	}
	// Refresh planner statistics on every start; analysis_limit keeps this a
	// bounded sample on large databases instead of a full table pass.
	if _, err := sqlDB.Exec("PRAGMA analysis_limit = 1000; ANALYZE;"); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("analyze sqlite: %w", err)
	}
	if inMemory(dsn) {
		return New(db), nil
	}
	readDB, err := sql.Open("sqlite3", withParam(dsn, "_query_only", "1"))
	if err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("open sqlite read pool: %w", err)
	}
	readDB.SetMaxOpenConns(readConns)
	readDB.SetMaxIdleConns(readConns)
	router := readRouter{DB: sqlDB, read: readDB}
	db.ConnPool, db.Statement.ConnPool = router, router
	return New(db), nil
}

// readRouter sends autocommit SELECTs to the read-only pool and everything
// else to the single writer. gorm runs every statement of a transaction on
// the *sql.Tx returned by the writer's BeginTx, so reads inside a transaction
// never reach the read pool and still see the transaction's own writes.
type readRouter struct {
	*sql.DB
	read *sql.DB
}

func (r readRouter) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	if isSelect(query) {
		return r.read.QueryContext(ctx, query, args...)
	}
	return r.DB.QueryContext(ctx, query, args...)
}

func (r readRouter) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	if isSelect(query) {
		return r.read.QueryRowContext(ctx, query, args...)
	}
	return r.DB.QueryRowContext(ctx, query, args...)
}

// GetDBConn keeps gorm's DB() returning the writer.
func (r readRouter) GetDBConn() (*sql.DB, error) { return r.DB, nil }

func isSelect(query string) bool {
	query = strings.TrimSpace(query)
	return len(query) >= 6 && strings.EqualFold(query[:6], "SELECT")
}

// withParam adds a go-sqlite3 DSN parameter unless the caller already set it.
// go-sqlite3 reads parameters after "?" for plain paths and file: URIs alike.
func withParam(dsn, key, value string) string {
	if strings.Contains(dsn, key+"=") {
		return dsn
	}
	separator := "?"
	if strings.Contains(dsn, "?") {
		separator = "&"
	}
	return dsn + separator + key + "=" + value
}

// Every connection to an in-memory database is a separate database, so a
// second pool would not see the writer's data.
func inMemory(dsn string) bool {
	return strings.Contains(dsn, ":memory:") || strings.Contains(dsn, "mode=memory")
}
