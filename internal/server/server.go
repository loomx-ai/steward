package server

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/prodesire/cloud-steward/internal/api"
	"github.com/prodesire/cloud-steward/internal/governance"
	"github.com/prodesire/cloud-steward/internal/scanner"
	"github.com/prodesire/cloud-steward/internal/store"
	"github.com/prodesire/cloud-steward/internal/webui"
)

type Config struct {
	Addr          string
	DBDriver      string
	DSN           string
	MigrationsDir string
	PollInterval  time.Duration
	Executor      string
}

func Run(ctx context.Context, cfg Config) error {
	if cfg.Addr == "" {
		cfg.Addr = "127.0.0.1:8585"
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 2 * time.Second
	}

	repo, err := openRepository(cfg)
	if err != nil {
		return err
	}
	executor, err := planExecutor(repo, cfg.Executor)
	if err != nil {
		return err
	}
	scanService := scanner.NewService(repo, scanner.Registry{
		"demo":     scanner.DemoConnector{},
		"alicloud": scanner.NewAliCloudConnector(),
	})
	go runWorker(ctx, scanService, cfg.PollInterval)

	handler := withStaticFallback(api.NewRouterWithExecutor(repo, executor))
	httpServer := &http.Server{
		Addr:              cfg.Addr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- httpServer.ListenAndServe()
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return httpServer.Shutdown(shutdownCtx)
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

func planExecutor(repo store.Repository, name string) (governance.PlanExecutor, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "", "dry-run", "dryrun":
		return governance.DryRunExecutor{}, nil
	case "alicloud-tag":
		return governance.TagExecutor{Client: governance.NewAliCloudTagClient(repo)}, nil
	default:
		return nil, errors.New("unsupported plan executor: " + name)
	}
}

func openRepository(cfg Config) (store.Repository, error) {
	driver := strings.ToLower(strings.TrimSpace(cfg.DBDriver))
	if driver == "" {
		if cfg.DSN == "" {
			driver = "memory"
		} else {
			driver = "mysql"
		}
	}
	switch driver {
	case "memory":
		return store.NewMemoryStore(), nil
	case "sqlite":
		if cfg.DSN == "" {
			cfg.DSN = filepath.Join(".cloud-steward", "cloud-steward.db")
		}
		if err := os.MkdirAll(filepath.Dir(cfg.DSN), 0o755); err != nil {
			return nil, err
		}
		db, err := store.OpenSQLite(cfg.DSN)
		if err != nil {
			return nil, err
		}
		if err := store.AutoMigrate(db); err != nil {
			return nil, err
		}
		return store.NewSQLStore(db), nil
	case "mysql":
		db, err := store.OpenMySQL(cfg.DSN)
		if err != nil {
			return nil, err
		}
		if cfg.MigrationsDir != "" {
			sqlDB, err := db.DB()
			if err != nil {
				return nil, err
			}
			if err := store.RunMigrations(sqlDB, cfg.MigrationsDir); err != nil {
				return nil, err
			}
		}
		return store.NewMySQLStore(db), nil
	default:
		return nil, errors.New("unsupported database driver: " + cfg.DBDriver)
	}
}

func runWorker(ctx context.Context, service *scanner.Service, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		for {
			processed, err := service.ProcessNext(ctx)
			if err != nil {
				slog.Error("scan worker failed", "error", err)
			}
			if !processed {
				break
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func withStaticFallback(apiHandler http.Handler) http.Handler {
	static := webui.Handler()
	mux := http.NewServeMux()
	mux.Handle("/api/", apiHandler)
	mux.Handle("/", static)
	return mux
}
