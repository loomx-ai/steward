package sqlite

import (
	"fmt"

	"github.com/loomx-ai/steward/internal/persistence"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func Open(dsn, migrationsDirectory string) (*Repositories, error) {
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{TranslateError: true, Logger: logger.Default.LogMode(logger.Silent)})
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
	return New(db), nil
}
