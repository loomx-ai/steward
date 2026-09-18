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

	"github.com/loomx-ai/steward/internal/datadir"
	"github.com/loomx-ai/steward/internal/server"
	httptransport "github.com/loomx-ai/steward/internal/transport/http"
	"github.com/loomx-ai/steward/internal/workloadidentity"
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
		Short: tr("Manage the local Steward server", "管理本地 Steward 服务"),
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
		Short: tr("Start the local server", "启动本地服务"),
		RunE: func(cmd *cobra.Command, _ []string) error {
			scanConcurrency, err := positiveEnvInt("STEWARD_SCAN_CONCURRENCY", 4)
			if err != nil {
				return err
			}
			dbMaxConns, err := positiveEnvInt("STEWARD_DB_MAX_CONNS", 0)
			if err != nil {
				return err
			}
			if dsn == "" {
				dsn = os.Getenv("STEWARD_DB_DSN")
			}
			if dbDriver == "" {
				dbDriver = envDefault("STEWARD_DB_DRIVER", "sqlite")
			}
			data, err := datadir.Resolve()
			if err != nil {
				return err
			}
			if data.WorkingDirectory {
				fmt.Fprintf(cmd.ErrOrStderr(), tr("Using data in %s. Move it to ~/%s or set STEWARD_HOME to keep one location.\n", "正在使用 %s 中的数据。请将其移至 ~/%s，或设置 STEWARD_HOME 固定数据位置。\n"), data.Path, datadir.Name)
			}
			if dbDriver == "sqlite" && dsn == "" {
				dsn = filepath.Join(data.Path, "steward.db")
			}
			if statusPath == "" {
				statusPath = filepath.Join(data.Path, "server.json")
			}
			role := httptransport.Role(strings.ToLower(strings.TrimSpace(authRole)))
			if role != httptransport.RoleViewer && role != httptransport.RoleOperator && role != httptransport.RoleAdmin {
				return fmt.Errorf(tr("invalid server role %q", "无效的服务角色 %q"), authRole)
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
			fmt.Fprintf(cmd.OutOrStdout(), tr("Steward server listening on %s\n", "Steward 服务已在 %s 监听\n"), serverURL(addr))
			return server.Run(ctx, server.Config{
				Addr:                addr,
				DBDriver:            dbDriver,
				DSN:                 dsn,
				MigrationsDir:       migrationsDir,
				PollInterval:        2 * time.Second,
				ScanConcurrency:     scanConcurrency,
				DBMaxConns:          dbMaxConns,
				CredentialMasterKey: os.Getenv("STEWARD_CREDENTIAL_MASTER_KEY"),
				OIDC:                workloadidentity.Config{IssuerURL: os.Getenv("STEWARD_OIDC_ISSUER_URL"), WorkspaceID: os.Getenv("STEWARD_OIDC_WORKSPACE_ID"), SigningKeyFile: os.Getenv("STEWARD_OIDC_SIGNING_KEY_FILE")},
				AuthTokens:          bindings,
				AuthMode:            authMode,
			})
		},
	}
	cmd.Flags().StringVar(&addr, "addr", envDefault("STEWARD_ADDR", "127.0.0.1:8585"), tr("server listen address", "服务监听地址"))
	cmd.Flags().StringVar(&dbDriver, "db-driver", envDefault("STEWARD_DB_DRIVER", "sqlite"), tr("database driver: sqlite or postgres", "数据库驱动：sqlite 或 postgres"))
	cmd.Flags().StringVar(&dsn, "db-dsn", "", tr("database DSN or SQLite path; defaults to STEWARD_DB_DSN", "数据库 DSN 或 SQLite 路径；默认读取 STEWARD_DB_DSN"))
	cmd.Flags().StringVar(&migrationsDir, "migrations-dir", "", tr("directory overriding the embedded database migrations", "替代内置数据库迁移的目录"))
	cmd.Flags().StringVar(&statusPath, "status-file", "", tr("server status file; defaults to server.json in the data directory", "服务状态文件；默认为数据目录中的 server.json"))
	cmd.Flags().StringVar(&authMode, "auth-mode", os.Getenv("STEWARD_AUTH_MODE"), tr("authentication mode: local, token, or cloud; defaults to local unless a token is configured", "认证模式：local、token 或 cloud；未配置 Token 时默认 local"))
	cmd.Flags().StringVar(&authToken, "auth-token", os.Getenv("STEWARD_AUTH_TOKEN"), tr("bearer token for token or cloud authentication", "token 或 cloud 认证使用的 Bearer Token"))
	cmd.Flags().StringVar(&authSubject, "auth-subject", envDefault("STEWARD_AUTH_SUBJECT", "local-admin"), tr("server-verified subject for the configured token", "所配置 Token 对应的服务端认证主体"))
	cmd.Flags().StringVar(&authRole, "auth-role", envDefault("STEWARD_AUTH_ROLE", "admin"), tr("role for the configured token: viewer, operator, or admin", "所配置 Token 的角色：viewer、operator 或 admin"))
	return cmd
}

func positiveEnvInt(name string, fallback int) (int, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed <= 0 {
		return 0, fmt.Errorf(tr("%s must be a positive integer", "%s 必须是正整数"), name)
	}
	return parsed, nil
}

func newServerStopCommand() *cobra.Command {
	var statusPath string
	cmd := &cobra.Command{
		Use:   "stop",
		Short: tr("Stop the local server", "停止本地服务"),
		RunE: func(cmd *cobra.Command, _ []string) error {
			statusPath, err := resolveStatusPath(statusPath)
			if err != nil {
				return err
			}
			status, err := readServerStatus(statusPath)
			if errors.Is(err, os.ErrNotExist) {
				return errors.New(tr("Steward server is not running", "Steward 服务未运行"))
			}
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
			fmt.Fprintf(cmd.OutOrStdout(), tr("requested stop for server pid %d\n", "已请求停止服务进程 %d\n"), status.PID)
			return nil
		},
	}
	cmd.Flags().StringVar(&statusPath, "status-file", "", tr("server status file; defaults to server.json in the data directory", "服务状态文件；默认为数据目录中的 server.json"))
	return cmd
}

func newServerStatusCommand() *cobra.Command {
	var statusPath string
	cmd := &cobra.Command{
		Use:   "status",
		Short: tr("Show local server status", "查看本地服务状态"),
		RunE: func(cmd *cobra.Command, _ []string) error {
			statusPath, err := resolveStatusPath(statusPath)
			if err != nil {
				return err
			}
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
	cmd.Flags().StringVar(&statusPath, "status-file", "", tr("server status file; defaults to server.json in the data directory", "服务状态文件；默认为数据目录中的 server.json"))
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

func resolveStatusPath(path string) (string, error) {
	if path != "" {
		return path, nil
	}
	data, err := datadir.Path()
	if err != nil {
		return "", err
	}
	return filepath.Join(data, "server.json"), nil
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
