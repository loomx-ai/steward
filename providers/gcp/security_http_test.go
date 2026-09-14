package gcp

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"testing"
	"time"

	"github.com/loomx-ai/steward/internal/app/governance"
	"github.com/loomx-ai/steward/internal/app/inventory"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/persistence"
	"github.com/loomx-ai/steward/internal/persistence/sqlite"
	providerruntime "github.com/loomx-ai/steward/internal/provider/runtime"
	httptransport "github.com/loomx-ai/steward/internal/transport/http"
)

// Exercise the public API contract with real scan planning, persisted jobs,
// provider dispatch and SQLite queries. Cloud responses are protocol fixtures;
// this is not an independent emulator or live-cloud acceptance test.
func TestSecurityHTTPScanAndQuery(t *testing.T) {
	ctx := t.Context()
	phase := "visible"
	serviceName := "projects/sample-project/locations/eu/securityCenterServices/event-threat-detection"
	billingName := "projects/sample-project/locations/eu/billingMetadata"
	calls := 0
	runtime := protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
		calls++
		if req.Method != http.MethodGet || req.URL.Host != securityServiceHost {
			t.Fatalf("unexpected native request: %s %s", req.Method, req.URL)
		}
		switch req.URL.Path {
		case "/v1/projects/sample-project/locations":
			if phase == "hidden" {
				return dataformResponse(req, 200, map[string]any{}), nil
			}
			return dataformResponse(req, 200, map[string]any{"locations": []any{map[string]any{"name": "projects/sample-project/locations/eu"}}}), nil
		case "/v1/projects/sample-project/locations/eu/securityCenterServices":
			return dataformResponse(req, 200, map[string]any{"securityCenterServices": []any{map[string]any{"name": serviceName}}}), nil
		case "/v1/" + serviceName:
			if phase == "denied" {
				return dataformResponse(req, 403, map[string]any{}), nil
			}
			data := securityServiceData(serviceName)
			if phase == "updated" {
				data["effectiveEnablementState"] = "DISABLED"
			}
			return dataformResponse(req, 200, data), nil
		case "/v1/" + billingName:
			tier := "PREMIUM"
			if phase == "updated" {
				tier = "STANDARD"
			}
			return dataformResponse(req, 200, map[string]any{"name": billingName, "billingTier": tier}), nil
		}
		t.Fatalf("unexpected native path: %s", req.URL)
		return nil, nil
	})
	repositories, err := sqlite.Open(filepath.Join(t.TempDir(), "security-http.db"), filepath.Join("..", "..", "migrations"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	for _, id := range []asset.ConnectionID{"connection", "other"} {
		if err := repositories.Connections().PutConnection(ctx, asset.CloudConnection{ID: id, Provider: asset.ProviderGCP, Partition: "gcp", Status: asset.ConnectionActive, CreatedAt: now, UpdatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	if err := repositories.Inventory().PutScope(ctx, asset.Scope{ID: "project", ConnectionID: "connection", Kind: asset.ScopeProject, NativeID: "sample-project", Name: "Project", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Regions().PutRegion(ctx, asset.ConnectionRegion{ID: "region", ConnectionID: "connection", RegionID: "eu", DiscoveredName: "Europe", Origin: asset.RegionOriginAPI, Lifecycle: asset.RegionActive, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	registry := providerruntime.NewRegistry()
	if err := registry.Register(runtime); err != nil {
		t.Fatal(err)
	}
	if err := registry.RegisterBundle(runtime.Bundle()); err != nil {
		t.Fatal(err)
	}
	creator, err := inventory.NewCreator(repositories, registry)
	if err != nil {
		t.Fatal(err)
	}
	handler := inventory.NewScanHandler(repositories, registry, inventory.NewService(repositories.Inventory()))
	router := httptransport.NewRouter(httptransport.Dependencies{
		Repositories: repositories, Scans: creator, Bundles: registry, Providers: registry,
		Authenticator: httptransport.NewStaticBearerAuthenticator([]httptransport.TokenBinding{
			{Token: "viewer", Principal: httptransport.Principal{Subject: "reader", Roles: []httptransport.Role{httptransport.RoleViewer}}},
			{Token: "operator", Principal: httptransport.Principal{Subject: "scanner", Roles: []httptransport.Role{httptransport.RoleOperator}}},
		}),
	})
	call := func(method, path, token string, body any, status int, output any) {
		t.Helper()
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(method, path, bytes.NewReader(encoded))
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		response := httptest.NewRecorder()
		router.ServeHTTP(response, req)
		if response.Code != status {
			t.Fatalf("%s %s: status=%d want=%d body=%s", method, path, response.Code, status, response.Body.String())
		}
		if output != nil {
			if err := json.Unmarshal(response.Body.Bytes(), output); err != nil {
				t.Fatal(err)
			}
		}
	}
	body := map[string]any{"scope_mode": "selected_regions", "region_ids": []string{"eu"}, "resource_kind_ids": []asset.ResourceKindID{runtime.resourceKind(securityServiceType).ID, runtime.resourceKind(securityBillingType).ID}}
	call("POST", "/api/scans?connection_id=connection", "", body, http.StatusUnauthorized, nil)
	call("POST", "/api/scans?connection_id=connection", "viewer", body, http.StatusForbidden, nil)
	if calls != 0 {
		t.Fatal("unauthorized scan reached cloud")
	}
	previous := map[string]asset.Asset{}
	for _, step := range []string{"visible", "hidden", "denied", "updated"} {
		phase = step
		var created inventory.ScanTaskProjection
		call("POST", "/api/scans?connection_id=connection", "operator", body, http.StatusAccepted, &created)
		if created.RequestedBy != "scanner" || created.Progress.Total != 1 || len(created.TargetProgress) != 1 || created.TargetProgress[0].Total != 2 {
			t.Fatalf("wrong scan projection: %+v", created)
		}
		shards, err := repositories.Inventory().ListScanShardsByRun(ctx, created.ID)
		if err != nil || len(shards) != 2 {
			t.Fatal("planned shards", shards, err)
		}
		for _, shard := range shards {
			expected := securityServiceSource
			if shard.ResourceKindID == runtime.resourceKind(securityBillingType).ID {
				expected = securityBillingSource
			}
			if shard.Source != expected || shard.Authoritative {
				t.Fatalf("unsafe source: %+v", shard)
			}
		}
		jobs, err := repositories.Jobs().ListJobsByAggregate(ctx, "scan_task", string(created.ID))
		if err != nil || len(jobs) != 1 {
			t.Fatal("persisted jobs", jobs, err)
		}
		err = handler.Handle(ctx, jobs[0])
		if (err != nil) != (step == "denied") {
			t.Fatalf("%s worker error: %v", step, err)
		}
		graphJobs, err := repositories.Jobs().ListJobsByAggregate(ctx, "scan_task", string(created.ID))
		if err != nil {
			t.Fatal(err)
		}
		graphCount := 0
		for _, job := range graphJobs {
			if job.Type == execution.JobGraph {
				graphCount++
				if err := governance.NewGraphHandler(repositories, registry, nil).Handle(ctx, job); err != nil {
					t.Fatal(err)
				}
			}
		}
		if graphCount != 1 {
			t.Fatal("missing graph reconciliation job", graphCount)
		}
		var finished inventory.ScanTaskProjection
		call("GET", "/api/scans/"+string(created.ID)+"?connection_id=connection", "viewer", nil, http.StatusOK, &finished)
		wantStatus := asset.ScanSucceeded
		if step == "denied" {
			wantStatus = asset.ScanPartial
		}
		if finished.Status != wantStatus || finished.FinishedAt == nil {
			t.Fatalf("%s terminal scan: %+v", step, finished)
		}
		wantCount, wantFailed := 2, 0
		if step == "hidden" {
			wantCount = 0
		}
		if step == "denied" {
			wantCount, wantFailed = 1, 1
		}
		if finished.Progress.Completed != 1 || len(finished.TargetProgress) != 1 || finished.TargetProgress[0].Completed != 2 || finished.Progress.ResourceCount != wantCount || finished.Progress.Failed != wantFailed {
			t.Fatalf("%s progress: %+v", step, finished)
		}
		call("GET", "/api/scans/"+string(created.ID)+"?connection_id=other", "viewer", nil, http.StatusNotFound, nil)
		state, tier := "INGEST_ONLY", "PREMIUM"
		if step == "updated" {
			state, tier = "DISABLED", "STANDARD"
		}
		for _, query := range []struct{ nativeType, expression string }{
			{securityServiceType, `properties.effectiveEnablementState = "` + state + `" AND properties.intendedEnablementState = "INHERITED"`},
			{securityBillingType, `properties.billingTier = "` + tier + `"`},
		} {
			var page persistence.Page[asset.Asset]
			path := "/api/assets?connection_id=connection&resource_query=" + url.QueryEscape(query.expression)
			call("GET", path, "viewer", nil, http.StatusOK, &page)
			if len(page.Items) != 1 {
				t.Fatalf("%s query %s: %+v", step, query.expression, page)
			}
			value := page.Items[0]
			if value.Capabilities.Has(asset.CapabilityActionable) || value.Identity.NativeType != query.nativeType || value.ClosedAt != nil || value.DeletedAt != nil {
				t.Fatalf("wrong observed asset: %+v", value)
			}
			old, exists := previous[query.nativeType]
			stale := step == "hidden" || step == "denied" && query.nativeType == securityServiceType
			if exists && value.ID != old.ID {
				t.Fatal("rescan changed asset identity")
			}
			if stale && !value.LastSeenAt.Equal(old.LastSeenAt) {
				t.Fatal("unobserved asset presented as fresh")
			}
			if exists && !stale && !value.LastSeenAt.After(old.LastSeenAt) {
				t.Fatal("new observation did not refresh timestamp")
			}
			previous[query.nativeType] = value
		}
	}
	var other persistence.Page[asset.Asset]
	call("GET", "/api/assets?connection_id=other", "viewer", nil, http.StatusOK, &other)
	if len(other.Items) != 0 {
		t.Fatal("security metadata crossed connections")
	}
	call("GET", "/api/assets?connection_id=connection&resource_query="+url.QueryEscape(`properties.inventedBillingField = "PREMIUM"`), "viewer", nil, http.StatusBadRequest, nil)
}
