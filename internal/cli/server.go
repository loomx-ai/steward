package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/prodesire/cloud-steward/internal/server"
	"github.com/spf13/cobra"
)

type ServerStatus struct {
	PID       int       `json:"pid"`
	Addr      string    `json:"addr"`
	Version   string    `json:"version"`
	StartedAt time.Time `json:"started_at"`
}

func newServerCommand(version string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "server",
		Short: "Manage the local Cloud Steward server",
	}
	cmd.AddCommand(newServerStartCommand(version))
	cmd.AddCommand(newServerStopCommand())
	cmd.AddCommand(newServerStatusCommand())
	return cmd
}

func newServerStartCommand(version string) *cobra.Command {
	var addr string
	var dbDriver string
	var dsn string
	var migrationsDir string
	var statusPath string
	var executor string
	cmd := &cobra.Command{
		Use:   "start",
		Short: "Start the local server",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if dsn == "" {
				dsn = os.Getenv("CLOUD_STEWARD_DB_DSN")
			}
			if dbDriver == "" {
				dbDriver = envDefault("CLOUD_STEWARD_DB_DRIVER", "sqlite")
			}
			if dbDriver == "sqlite" && dsn == "" {
				dsn = filepath.Join(".cloud-steward", "cloud-steward.db")
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			status := ServerStatus{
				PID:       os.Getpid(),
				Addr:      addr,
				Version:   version,
				StartedAt: time.Now().UTC(),
			}
			if err := writeServerStatus(statusPath, status); err != nil {
				return err
			}
			defer os.Remove(statusPath)
			fmt.Fprintf(cmd.OutOrStdout(), "Cloud Steward server listening on %s\n", serverURL(addr))
			return server.Run(ctx, server.Config{
				Addr:          addr,
				DBDriver:      dbDriver,
				DSN:           dsn,
				MigrationsDir: migrationsDir,
				PollInterval:  2 * time.Second,
				Executor:      executor,
			})
		},
	}
	cmd.Flags().StringVar(&addr, "addr", envDefault("CLOUD_STEWARD_ADDR", "127.0.0.1:8585"), "server listen address")
	cmd.Flags().StringVar(&dbDriver, "db-driver", envDefault("CLOUD_STEWARD_DB_DRIVER", "sqlite"), "database driver: sqlite, mysql, or memory")
	cmd.Flags().StringVar(&dsn, "db-dsn", "", "database DSN or SQLite path; defaults to CLOUD_STEWARD_DB_DSN")
	cmd.Flags().StringVar(&migrationsDir, "migrations-dir", "migrations", "goose migration directory")
	cmd.Flags().StringVar(&statusPath, "status-file", defaultStatusPath(), "server status file")
	cmd.Flags().StringVar(&executor, "executor", envDefault("CLOUD_STEWARD_EXECUTOR", "dry-run"), "plan executor: dry-run or alicloud-tag")
	return cmd
}

func newServerStopCommand() *cobra.Command {
	var statusPath string
	cmd := &cobra.Command{
		Use:   "stop",
		Short: "Stop the local server",
		RunE: func(cmd *cobra.Command, _ []string) error {
			status, err := readServerStatus(statusPath)
			if err != nil {
				return err
			}
			process, err := os.FindProcess(status.PID)
			if err != nil {
				return err
			}
			if err := process.Signal(syscall.SIGTERM); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "sent SIGTERM to server pid %d\n", status.PID)
			return nil
		},
	}
	cmd.Flags().StringVar(&statusPath, "status-file", defaultStatusPath(), "server status file")
	return cmd
}

func newServerStatusCommand() *cobra.Command {
	var statusPath string
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show local server status",
		RunE: func(cmd *cobra.Command, _ []string) error {
			status, err := readServerStatus(statusPath)
			if err != nil {
				fmt.Fprintln(cmd.OutOrStdout(), "status=stopped")
				return nil
			}
			running := processRunning(status.PID)
			state := "stopped"
			if running {
				state = "running"
			}
			fmt.Fprintf(cmd.OutOrStdout(), "status=%s pid=%d addr=%s version=%s started_at=%s\n",
				state, status.PID, status.Addr, status.Version, status.StartedAt.Format(time.RFC3339))
			return nil
		},
	}
	cmd.Flags().StringVar(&statusPath, "status-file", defaultStatusPath(), "server status file")
	return cmd
}

func writeServerStatus(path string, status ServerStatus) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()
	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	return encoder.Encode(status)
}

func readServerStatus(path string) (ServerStatus, error) {
	file, err := os.Open(path)
	if err != nil {
		return ServerStatus{}, err
	}
	defer file.Close()
	var status ServerStatus
	if err := json.NewDecoder(file).Decode(&status); err != nil {
		return ServerStatus{}, err
	}
	if status.PID == 0 {
		return ServerStatus{}, errors.New("server status is missing pid")
	}
	return status, nil
}

func processRunning(pid int) bool {
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return process.Signal(syscall.Signal(0)) == nil
}

func defaultStatusPath() string {
	return filepath.Join(".cloud-steward", "server.json")
}

func envDefault(name string, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func serverURL(addr string) string {
	addr = strings.TrimSpace(addr)
	if strings.HasPrefix(addr, "http://") || strings.HasPrefix(addr, "https://") {
		return addr
	}
	host, port, err := net.SplitHostPort(addr)
	if err == nil && host == "" {
		return "http://127.0.0.1:" + port
	}
	return "http://" + addr
}

func backgroundContext() context.Context {
	return context.Background()
}
