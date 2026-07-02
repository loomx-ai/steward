package store

import (
	"database/sql"

	"github.com/pressly/goose/v3"
)

func RunMigrations(db *sql.DB, dir string) error {
	if err := goose.SetDialect("mysql"); err != nil {
		return err
	}
	return goose.Up(db, dir)
}
