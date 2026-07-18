package postgres_test

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/loomx-ai/steward/internal/persistence"
	"github.com/loomx-ai/steward/internal/persistence/contract"
	"github.com/loomx-ai/steward/internal/persistence/postgres"
	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestPostgresRepositoryContract(t *testing.T) {
	dsn := os.Getenv("STEWARD_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("STEWARD_TEST_POSTGRES_DSN is not configured")
	}
	admin, err := gorm.Open(gormpostgres.Open(dsn), &gorm.Config{
		TranslateError: true,
		Logger:         logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatal(err)
	}
	adminSQL, err := admin.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := adminSQL.Close(); err != nil {
			t.Errorf("close PostgreSQL test database: %v", err)
		}
	})
	migrationsDirectory := filepath.Join("..", "..", "..", "migrations")
	contract.Run(t, func(t *testing.T) persistence.Repositories {
		t.Helper()
		schema := fmt.Sprintf("steward_contract_%d_%d", os.Getpid(), time.Now().UnixNano())
		if err := admin.Exec("CREATE SCHEMA " + schema).Error; err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := admin.Exec("DROP SCHEMA IF EXISTS " + schema + " CASCADE").Error; err != nil {
				t.Errorf("drop PostgreSQL test schema: %v", err)
			}
		})
		db, err := gorm.Open(gormpostgres.Open(withSearchPath(dsn, schema)), &gorm.Config{
			TranslateError: true,
			Logger:         logger.Default.LogMode(logger.Silent),
		})
		if err != nil {
			t.Fatal(err)
		}
		sqlDB, err := db.DB()
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := sqlDB.Close(); err != nil {
				t.Errorf("close PostgreSQL test schema connection: %v", err)
			}
		})
		if err := persistence.Migrate(sqlDB, "postgres", migrationsDirectory); err != nil {
			t.Fatal(err)
		}
		return postgres.New(db)
	})
}

func withSearchPath(dsn, schema string) string {
	parsed, err := url.Parse(dsn)
	if err == nil && (parsed.Scheme == "postgres" || parsed.Scheme == "postgresql") {
		query := parsed.Query()
		query.Set("search_path", schema)
		parsed.RawQuery = query.Encode()
		return parsed.String()
	}
	return strings.TrimSpace(dsn) + " search_path=" + schema
}

func TestWithSearchPath(t *testing.T) {
	tests := []struct {
		name string
		dsn  string
		want string
	}{
		{
			name: "URL",
			dsn:  "postgres://steward:secret@localhost/steward_test?sslmode=disable",
			want: "postgres://steward:secret@localhost/steward_test?search_path=isolated&sslmode=disable",
		},
		{
			name: "keyword value",
			dsn:  "host=localhost dbname=steward_test sslmode=disable",
			want: "host=localhost dbname=steward_test sslmode=disable search_path=isolated",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := withSearchPath(test.dsn, "isolated"); got != test.want {
				t.Fatalf("withSearchPath() = %q, want %q", got, test.want)
			}
		})
	}
}
