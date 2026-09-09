package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/loomx-ai/steward/internal/app/cleanup"
	connectionapp "github.com/loomx-ai/steward/internal/app/connection"
	"github.com/loomx-ai/steward/internal/app/governance"
	"github.com/loomx-ai/steward/internal/app/inventory"
	regionapp "github.com/loomx-ai/steward/internal/app/region"
	topologyapp "github.com/loomx-ai/steward/internal/app/topology"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/credential"
	"github.com/loomx-ai/steward/internal/persistence"
	"github.com/loomx-ai/steward/internal/persistence/postgres"
	"github.com/loomx-ai/steward/internal/persistence/sqlite"
	"github.com/loomx-ai/steward/internal/provider/contracts"
	providerruntime "github.com/loomx-ai/steward/internal/provider/runtime"
	httptransport "github.com/loomx-ai/steward/internal/transport/http"
	"github.com/loomx-ai/steward/internal/webui"
	"github.com/loomx-ai/steward/providers/alicloud"
	provideraws "github.com/loomx-ai/steward/providers/aws"
	"github.com/loomx-ai/steward/providers/azure"
	"github.com/loomx-ai/steward/providers/gcp"
)

type Config struct {
	Addr                string
	DBDriver            string
	DSN                 string
	MigrationsDir       string
	PollInterval        time.Duration
	ScanConcurrency     int
	AuthTokens          []httptransport.TokenBinding
	AuthMode            string
	CredentialMasterKey string
	CredentialSource    contracts.CredentialSource
}

func Run(ctx context.Context, config Config) error {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	if config.Addr == "" {
		config.Addr = "127.0.0.1:8585"
	}
	if config.PollInterval <= 0 {
		config.PollInterval = time.Second
	}
	if config.ScanConcurrency <= 0 {
		config.ScanConcurrency = 4
	}
	authenticator, authMode, err := resolveAuthenticator(config)
	if err != nil {
		return err
	}
	repositories, err := openRepositories(config)
	if err != nil {
		return err
	}
	_, loopbackErr := httptransport.NewLocalAuthenticator(config.Addr)
	if config.CredentialMasterKey == "" && loopbackErr == nil && (config.DBDriver == "" || config.DBDriver == "sqlite") {
		connections, err := repositories.Connections().ListConnections(ctx, persistence.ListOptions{Limit: 1})
		if err != nil {
			return fmt.Errorf("check existing connections before initializing encryption: %w", err)
		}
		dsn := config.DSN
		if dsn == "" {
			dsn = filepath.Join(".steward", "steward.db")
		}
		config.CredentialMasterKey, err = localCredentialKey(filepath.Dir(dsn), len(connections.Items) == 0)
		if err != nil {
			return err
		}
	}
	vault, err := credential.NewVault(config.CredentialMasterKey, repositories.Credentials())
	if err != nil {
		return err
	}
	credentialSource := config.CredentialSource
	if credentialSource == nil {
		credentialSource = vault
	}
	credentialSource, err = credential.NewValidatedSource(repositories.Connections(), repositories.Credentials(), credentialSource)
	if err != nil {
		return err
	}
	registry, err := providerRegistry(credentialSource)
	if err != nil {
		return err
	}
	regionService, err := regionapp.NewService(repositories)
	if err != nil {
		return err
	}
	regionQueue, err := regionapp.NewRefreshQueue(repositories)
	if err != nil {
		return err
	}
	connectionService, err := connectionapp.NewService(repositories, vault, registry, regionQueue)
	if err != nil {
		return err
	}
	planner := cleanup.NewService(repositories, registry)
	topologyService := topologyapp.NewService(repositories, registry)
	scanCreator, err := inventory.NewCreator(repositories, registry)
	if err != nil {
		return err
	}
	scanControls, err := inventory.NewControlService(
		repositories,
		inventory.WithControlDirectory(registry),
	)
	if err != nil {
		return err
	}
	oauthFlows := alicloud.NewOAuthFlowManager()
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := oauthFlows.Close(closeCtx); err != nil {
			slog.Error("Alibaba Cloud OAuth flow service did not close cleanly", "error", err)
		}
	}()
	apiHandler := httptransport.NewRouter(httptransport.Dependencies{
		Repositories: repositories, CleanupTasks: planner, Connections: connectionService, Regions: regionService, RegionRefreshes: regionQueue, Scans: scanCreator, ScanControls: scanControls, NetworkTargets: registry, Topology: topologyService, Bundles: registry, Providers: registry, OAuthFlows: oauthFlows, Authenticator: authenticator,
		SSEPollInterval: config.PollInterval,
		AuthMode:        authMode,
	})

	actionResolver := cleanup.NewRepositoryActionResolver(repositories.Connections(), registry)
	executionHandler := cleanup.NewExecutionHandler(planner, actionResolver)
	inventoryService := inventory.NewService(repositories.Inventory())
	scanHandler := inventory.NewScanHandler(repositories, registry, inventoryService)
	graphHandler := governance.NewGraphHandler(repositories, registry, newLifecycleContributorResolver(registry))
	regionRefreshHandler := regionapp.NewRefreshHandler(repositories, registry, regionService)
	controlWorkers := []*cleanup.Worker{
		cleanup.NewWorker(repositories.Jobs(), map[execution.JobType]cleanup.Handler{
			execution.JobExecute: executionHandler,
		}, cleanup.WorkerOptions{
			WorkerID: "server-execution", AllowedTypes: []execution.JobType{execution.JobExecute}, Concurrency: cleanup.ExecutionWorkerConcurrency, PollInterval: config.PollInterval,
			OnError: func(err error) { slog.Error("durable execution job failed", "error", err) },
		}),
		cleanup.NewWorker(repositories.Jobs(), map[execution.JobType]cleanup.Handler{
			execution.JobGraph: graphHandler,
		}, cleanup.WorkerOptions{
			WorkerID: "server-graph", AllowedTypes: []execution.JobType{execution.JobGraph}, PollInterval: config.PollInterval,
			OnError: func(err error) { slog.Error("durable graph job failed", "error", err) },
		}),
		cleanup.NewWorker(repositories.Jobs(), map[execution.JobType]cleanup.Handler{
			execution.JobRegionRefresh: regionRefreshHandler,
		}, cleanup.WorkerOptions{
			WorkerID: "server-region", AllowedTypes: []execution.JobType{execution.JobRegionRefresh}, PollInterval: config.PollInterval,
			OnError: func(err error) { slog.Error("durable region refresh job failed", "error", err) },
		}),
	}
	for _, controlWorker := range controlWorkers {
		go func() {
			if err := controlWorker.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
				slog.Error("durable control worker stopped", "error", err)
			}
		}()
	}
	for index := range config.ScanConcurrency {
		scanWorker := cleanup.NewWorker(repositories.Jobs(), map[execution.JobType]cleanup.Handler{execution.JobScan: scanHandler}, cleanup.WorkerOptions{
			WorkerID: fmt.Sprintf("server-scan-%d", index+1), AllowedTypes: []execution.JobType{execution.JobScan}, PollInterval: config.PollInterval,
			OnError: func(err error) { slog.Error("durable scan target failed", "error", err) },
		})
		go func() {
			if err := scanWorker.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
				slog.Error("durable scan worker stopped", "error", err)
			}
		}()
	}

	httpServer := &http.Server{Addr: config.Addr, Handler: withStaticFallback(apiHandler), ReadHeaderTimeout: 5 * time.Second}
	errCh := make(chan error, 1)
	go func() { errCh <- httpServer.ListenAndServe() }()
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

func resolveAuthenticator(config Config) (httptransport.Authenticator, string, error) {
	mode := strings.ToLower(strings.TrimSpace(config.AuthMode))
	if mode == "" {
		mode = "local"
		if len(config.AuthTokens) > 0 {
			mode = "token"
		}
	}
	switch mode {
	case "local":
		auth, err := httptransport.NewLocalAuthenticator(config.Addr)
		return auth, mode, err
	case "token", "cloud":
		if len(config.AuthTokens) == 0 {
			return nil, mode, fmt.Errorf("%s authentication requires a configured bearer token", mode)
		}
		for _, binding := range config.AuthTokens {
			if strings.TrimSpace(binding.Token) == "" || strings.TrimSpace(binding.Principal.Subject) == "" {
				return nil, mode, fmt.Errorf("authentication token and subject must not be empty")
			}
		}
		if mode == "cloud" {
			return httptransport.NewCloudAuthenticator(config.AuthTokens), mode, nil
		}
		return httptransport.NewStaticBearerAuthenticator(config.AuthTokens), mode, nil
	default:
		return nil, mode, fmt.Errorf("unsupported authentication mode %q: use local, token, or cloud", mode)
	}
}

func openRepositories(config Config) (persistence.Repositories, error) {
	driver := strings.ToLower(strings.TrimSpace(config.DBDriver))
	if driver == "" {
		driver = "sqlite"
	}
	switch driver {
	case "sqlite":
		if strings.TrimSpace(config.DSN) == "" {
			config.DSN = filepath.Join(".steward", "steward.db")
		}
		if err := os.MkdirAll(filepath.Dir(config.DSN), 0o755); err != nil {
			return nil, err
		}
		return sqlite.Open(config.DSN, config.MigrationsDir)
	case "postgres":
		if strings.TrimSpace(config.DSN) == "" {
			return nil, fmt.Errorf("PostgreSQL DSN is required")
		}
		return postgres.Open(config.DSN, config.MigrationsDir)
	default:
		return nil, fmt.Errorf("unsupported database driver %q: only sqlite and postgres are supported", config.DBDriver)
	}
}

func providerRegistry(credentials contracts.CredentialSource) (*providerruntime.Registry, error) {
	if credentials == nil {
		return nil, fmt.Errorf("credential source is required")
	}
	aliRuntime, err := alicloud.NewRuntime(credentials)
	if err != nil {
		return nil, err
	}
	registry := providerruntime.NewRegistry()
	if err := registry.Register(aliRuntime); err != nil {
		return nil, err
	}
	if err := registry.RegisterBundle(aliRuntime.Bundle()); err != nil {
		return nil, err
	}
	awsRuntime, err := provideraws.NewRuntime(credentials)
	if err != nil {
		return nil, err
	}
	if err := registry.Register(awsRuntime); err != nil {
		return nil, err
	}
	if err := registry.RegisterBundle(awsRuntime.Bundle()); err != nil {
		return nil, err
	}
	gcpRuntime, err := gcp.NewRuntime(credentials)
	if err != nil {
		return nil, err
	}
	if err := registry.Register(gcpRuntime); err != nil {
		return nil, err
	}
	if err := registry.RegisterBundle(gcpRuntime.Bundle()); err != nil {
		return nil, err
	}
	azureRuntime, err := azure.NewRuntime(credentials)
	if err != nil {
		return nil, err
	}
	if err := registry.Register(azureRuntime); err != nil {
		return nil, err
	}
	if err := registry.RegisterBundle(azureRuntime.Bundle()); err != nil {
		return nil, err
	}
	return registry, nil
}

func withStaticFallback(apiHandler http.Handler) http.Handler {
	static := webui.Handler()
	mux := http.NewServeMux()
	mux.Handle("/api/", apiHandler)
	mux.Handle("/", static)
	return mux
}
