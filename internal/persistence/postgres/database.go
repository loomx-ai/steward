package postgres

import (
	"fmt"

	"github.com/loomx-ai/steward/internal/persistence"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func Open(dsn, migrationsDirectory string) (*Repositories, error) {
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{TranslateError: true, Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		return nil, fmt.Errorf("open postgres: %w", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("get postgres database: %w", err)
	}
	if err := persistence.Migrate(sqlDB, "postgres", migrationsDirectory); err != nil {
		_ = sqlDB.Close()
		return nil, err
	}
	return New(db), nil
}
