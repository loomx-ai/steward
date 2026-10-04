package postgres

import (
	"fmt"
	"time"

	"github.com/loomx-ai/steward/internal/persistence"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// defaultMaxConns caps the pool when no limit is configured, well below
// PostgreSQL's default max_connections of 100, so a burst queues instead of
// exhausting the server.
const defaultMaxConns = 20

// Open connects to PostgreSQL. maxConns caps the connection pool so callers
// queue instead of exceeding a role's CONNECTION LIMIT; zero or less means
// defaultMaxConns.
func Open(dsn, migrationsDirectory string, maxConns int) (*Repositories, error) {
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{TranslateError: true, Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		return nil, fmt.Errorf("open postgres: %w", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("get postgres database: %w", err)
	}
	if maxConns <= 0 {
		maxConns = defaultMaxConns
	}
	sqlDB.SetMaxOpenConns(maxConns)
	sqlDB.SetMaxIdleConns(maxConns)
	sqlDB.SetConnMaxIdleTime(5 * time.Minute)
	if err := persistence.Migrate(sqlDB, "postgres", migrationsDirectory); err != nil {
		_ = sqlDB.Close()
		return nil, err
	}
	return New(db), nil
}
