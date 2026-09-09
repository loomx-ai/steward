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
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/loomx-ai/steward/internal/server"
	httptransport "github.com/loomx-ai/steward/internal/transport/http"
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
		Short: "Manage the local Steward server",
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
	var authToken string
	var authMode string
	var authSubject string
	var authRole string
	cmd := &cobra.Command{
		Use:   "start",
		Short: "Start the local server",
		RunE: func(cmd *cobra.Command, _ []string) error {
			scanConcurrency, err := positiveEnvInt("STEWARD_SCAN_CONCURRENCY", 4)
			if err != nil {
				return err
			}
			if dsn == "" {
				dsn = os.Getenv("STEWARD_DB_DSN")
			}
			if dbDriver == "" {
				dbDriver = envDefault("STEWARD_DB_DRIVER", "sqlite")
			}
			if dbDriver == "sqlite" && dsn == "" {
				dsn = filepath.Join(".steward", "steward.db")
			}
			role := httptransport.Role(strings.ToLower(strings.TrimSpace(authRole)))
			if role != httptransport.RoleViewer && role != httptransport.RoleOperator && role != httptransport.RoleAdmin {
				return fmt.Errorf("invalid server role %q", authRole)
			}
			var bindings []httptransport.TokenBinding
			if strings.TrimSpace(authToken) != "" {
				bindings = []httptransport.TokenBinding{{Token: authToken, Principal: httptransport.Principal{Subject: authSubject, Roles: []httptransport.Role{role}}}}
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
			fmt.Fprintf(cmd.OutOrStdout(), "Steward server listening on %s\n", serverURL(addr))
			return server.Run(ctx, server.Config{
				Addr:                addr,
				DBDriver:            dbDriver,
				DSN:                 dsn,
				MigrationsDir:       migrationsDir,
				PollInterval:        2 * time.Second,
				ScanConcurrency:     scanConcurrency,
				CredentialMasterKey: os.Getenv("STEWARD_CREDENTIAL_MASTER_KEY"),
				AuthTokens:          bindings,
				AuthMode:            authMode,
			})
		},
	}
	cmd.Flags().StringVar(&addr, "addr", envDefault("STEWARD_ADDR", "127.0.0.1:8585"), "server listen address")
	cmd.Flags().StringVar(&dbDriver, "db-driver", envDefault("STEWARD_DB_DRIVER", "sqlite"), "database driver: sqlite or postgres")
	cmd.Flags().StringVar(&dsn, "db-dsn", "", "database DSN or SQLite path; defaults to STEWARD_DB_DSN")
	cmd.Flags().StringVar(&migrationsDir, "migrations-dir", "", "directory overriding the embedded database migrations")
	cmd.Flags().StringVar(&statusPath, "status-file", defaultStatusPath(), "server status file")
	cmd.Flags().StringVar(&authMode, "auth-mode", os.Getenv("STEWARD_AUTH_MODE"), "authentication mode: local, token, or cloud; defaults to local unless a token is configured")
	cmd.Flags().StringVar(&authToken, "auth-token", os.Getenv("STEWARD_AUTH_TOKEN"), "bearer token for token or cloud authentication")
	cmd.Flags().StringVar(&authSubject, "auth-subject", envDefault("STEWARD_AUTH_SUBJECT", "local-admin"), "server-verified subject for the configured token")
	cmd.Flags().StringVar(&authRole, "auth-role", envDefault("STEWARD_AUTH_ROLE", "admin"), "role for the configured token: viewer, operator, or admin")
	return cmd
}

func positiveEnvInt(name string, fallback int) (int, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed <= 0 {
		return 0, fmt.Errorf("%s must be a positive integer", name)
	}
	return parsed, nil
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
			defer process.Release()
			if err := stopProcess(process); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "requested stop for server pid %d\n", status.PID)
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

func defaultStatusPath() string {
	return filepath.Join(".steward", "server.json")
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
