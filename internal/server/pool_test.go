package server

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/workspace"
	"github.com/loomx-ai/steward/internal/credential"
	persistencepostgres "github.com/loomx-ai/steward/internal/persistence/postgres"
	"github.com/loomx-ai/steward/internal/provider/contracts"
	httptransport "github.com/loomx-ai/steward/internal/transport/http"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// A cloud server serves every workspace from one database. Each request must
// stay inside the workspace the gateway names, and every read path must carry
// that workspace, or the strict repositories refuse it.
func TestCloudServerKeepsWorkspacesApart(t *testing.T) {
	dsn := os.Getenv("STEWARD_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("STEWARD_TEST_POSTGRES_DSN is not configured")
	}
	admin, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("steward_pool_%d_%d", os.Getpid(), time.Now().UnixNano())
	if err := admin.Exec("CREATE SCHEMA " + schema).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { admin.Exec("DROP SCHEMA IF EXISTS " + schema + " CASCADE") })
	parsed, _ := url.Parse(dsn)
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	_ = listener.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, Config{
			Addr: addr, DBDriver: "postgres", DSN: parsed.String(), MigrationsDir: filepath.Join("..", "..", "migrations"),
			AuthMode: "cloud", AuthTokens: []httptransport.TokenBinding{{Token: "pool-token", Principal: httptransport.Principal{Subject: "gateway", Roles: []httptransport.Role{httptransport.RoleAdmin}}}},
			CredentialMasterKey: "AQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQE=", CredentialSource: failingCredentials{},
		})
	}()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("server stopped: %v", err)
		}
	})

	call := func(method, path, tenant, body string) (int, string) {
		t.Helper()
		request, _ := http.NewRequest(method, "http://"+addr+path, strings.NewReader(body))
		request.Header.Set("Authorization", "Bearer pool-token")
		request.Header.Set("X-Steward-Subject", "usr_test")
		request.Header.Set("X-Steward-Role", "admin")
		if tenant != "" {
			request.Header.Set("X-Steward-Workspace", tenant)
		}
		request.Header.Set("Content-Type", "application/json")
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			return 0, err.Error()
		}
		defer response.Body.Close()
		data, _ := io.ReadAll(response.Body)
		return response.StatusCode, string(data)
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		if status, _ := call(http.MethodGet, "/api/connections", "ws_a", ""); status == http.StatusOK {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("server did not start")
		}
		time.Sleep(100 * time.Millisecond)
	}

	if status, body := call(http.MethodGet, "/api/connections", "", ""); status != http.StatusUnauthorized {
		t.Fatalf("request without a workspace = %d %s", status, body)
	}
	stored, err := persistencepostgres.Open(parsed.String(), filepath.Join("..", "..", "migrations"), 2)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	first := workspace.With(context.Background(), "ws_a")
	if err := stored.Connections().PutConnection(first, asset.CloudConnection{
		ID: "con-a", Name: "a", Provider: asset.ProviderAliCloud, Partition: "public", Principal: "a", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	vault, err := credential.NewVault("AQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQE=", stored.Credentials())
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := vault.Seal(first, "con-a", asset.ProviderAliCloud, contracts.Credential{Type: asset.CredentialAliCloudAccessKey, Values: map[string]string{"access_key_id": "ak", "access_key_secret": "sk"}}, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := stored.Credentials().PutCredential(first, sealed); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/connections", "/api/scan-schedule-overview", "/api/scan-schedule-settings", "/api/notification-channels"} {
		if status, body := call(http.MethodGet, path, "ws_a", ""); status != http.StatusOK {
			t.Errorf("GET %s = %d %s", path, status, body)
		}
	}
	for _, path := range []string{"/api/scans", "/api/assets", "/api/findings", "/api/cleanup", "/api/execution-attempts", "/api/scan-schedules", "/api/scopes", "/api/topology"} {
		if status, body := call(http.MethodGet, path+"?connection_id=con-a", "ws_a", ""); status != http.StatusOK {
			t.Errorf("GET %s = %d %s", path, status, body)
		}
		if status, body := call(http.MethodGet, path+"?connection_id=con-a", "ws_b", ""); status != http.StatusNotFound {
			t.Errorf("GET %s from another workspace = %d %s", path, status, body)
		}
	}
	if _, body := call(http.MethodGet, "/api/connections", "ws_b", ""); strings.Contains(body, "con-a") {
		t.Fatalf("second workspace lists the first one's connection: %s", body)
	}

	if status, body := call(http.MethodPut, "/api/scan-schedule-settings", "ws_a", `{"default_schedule_enabled":false,"retention_days":7,"default_timezone":"UTC"}`); status != http.StatusOK {
		t.Fatalf("update settings = %d %s", status, body)
	}
	if status, body := call(http.MethodPost, "/api/notification-channels", "ws_a", `{"name":"ops","type":"webhook","url":"https://hooks.example.com/a","events":["scan_failed"]}`); status/100 != 2 {
		t.Fatalf("create channel = %d %s", status, body)
	}
	if _, body := call(http.MethodGet, "/api/scan-schedule-settings", "ws_b", ""); strings.Contains(body, `"retention_days":7`) {
		t.Fatalf("second workspace sees the first one's settings: %s", body)
	}
	if _, body := call(http.MethodGet, "/api/notification-channels", "ws_b", ""); strings.Contains(body, "ops") {
		t.Fatalf("second workspace sees the first one's channel: %s", body)
	}
	if _, body := call(http.MethodGet, "/api/notification-channels", "ws_a", ""); !strings.Contains(body, "ops") {
		t.Fatalf("first workspace lost its channel: %s", body)
	}
}
