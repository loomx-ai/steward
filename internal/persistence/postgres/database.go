package postgres

import (
	"fmt"
	"time"

	"github.com/loomx-ai/steward/internal/persistence"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// unboundedIdleConns is how many idle connections an uncapped pool keeps so
// bursts reuse connections instead of reconnecting past database/sql's default
// of two.
const unboundedIdleConns = 16

// Open connects to PostgreSQL. A positive maxConns caps the connection pool so
// callers queue instead of exceeding a role's CONNECTION LIMIT.
func Open(dsn, migrationsDirectory string, maxConns int) (*Repositories, error) {
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{TranslateError: true, Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		return nil, fmt.Errorf("open postgres: %w", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("get postgres database: %w", err)
	}
	idleConns := unboundedIdleConns
	if maxConns > 0 {
		sqlDB.SetMaxOpenConns(maxConns)
		idleConns = maxConns
	}
	sqlDB.SetMaxIdleConns(idleConns)
	sqlDB.SetConnMaxIdleTime(5 * time.Minute)
	if err := persistence.Migrate(sqlDB, "postgres", migrationsDirectory); err != nil {
		_ = sqlDB.Close()
		return nil, err
	}
	return New(db), nil
}
