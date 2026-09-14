package gcp

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/loomx-ai/steward/internal/app/inventory"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/core/resourcequery"
	"github.com/loomx-ai/steward/internal/persistence"
	"github.com/loomx-ai/steward/internal/persistence/sqlite"
	"github.com/loomx-ai/steward/internal/provider/contracts"
	providerruntime "github.com/loomx-ai/steward/internal/provider/runtime"
)

// A successful LIST followed by a failed detail GET cannot close the previously
// observed service or replace its full metadata with a list summary.
func TestSecurityServicesDetailFailurePreservesSQLiteObservations(t *testing.T) {
	for _, parent := range []string{"projects/sample-project", "folders/456", "organizations/123"} {
		t.Run(parent, func(t *testing.T) { testSecurityServicesDetailFailurePreservesSQLiteObservations(t, parent) })
	}
}

func testSecurityServicesDetailFailurePreservesSQLiteObservations(t *testing.T, parent string) {
	ctx := context.Background()
	failure := ""
	name := parent + "/locations/eu/securityCenterServices/event-threat-detection"
	transport := func(req *http.Request) (*http.Response, error) {
		if req.Method != "GET" || req.URL.Host != securityServiceHost {
			t.Fatalf("unexpected request %s", req.URL)
		}
		if strings.HasSuffix(req.URL.Path, "/securityCenterServices") && req.URL.Path != "/v1/"+parent+"/locations/eu/securityCenterServices" {
			return apiResponse(req, 200, `{}`), nil
		}
		switch req.URL.Path {
		case "/v1/projects/sample-project/locations":
			if failure == "hidden_location" {
				return dataformResponse(req, 200, map[string]any{}), nil
			}
			return dataformResponse(req, 200, map[string]any{"locations": []any{map[string]any{"name": "projects/sample-project/locations/eu"}}}), nil
		case "/v1/" + parent + "/locations/eu/securityCenterServices":
			if failure == "hidden_service" {
				return dataformResponse(req, 200, map[string]any{}), nil
			}
			record := map[string]any{"name": name}
			if failure == "list_state" {
				record["effectiveEnablementState"] = true
			}
			if failure == "list_partial" {
				record["unreachable"] = []any{"eu"}
			}
			body := map[string]any{"securityCenterServices": []any{record}}
			if failure == "list_token" {
				body["nextPageToken"] = 12
			}
			return dataformResponse(req, 200, body), nil
		case "/v1/" + name:
			if failure == "denied" {
				return dataformResponse(req, 403, map[string]any{}), nil
			}
			if failure == "missing" {
				return dataformResponse(req, 404, map[string]any{}), nil
			}
			data := securityServiceData(name)
			data["extension"] = map[string]any{"serviceConfig": map[string]any{"ordinary": "PRIVATE_NESTED_CONFIG"}}
			if failure == "detail_partial" {
				data["unreachable"] = []any{"eu"}
			}
			if failure == "updated" {
				data["effectiveEnablementState"] = "DISABLED"
			}
			if failure == "changed" {
				data["name"] = name + "-other"
			}
			return dataformResponse(req, 200, data), nil
		}
		t.Fatalf("unexpected request %s", req.URL)
		return nil, nil
	}
	var r *Runtime
	ancestry := newOrganizationScenario()
	if parent == "projects/sample-project" {
		r = protocolRuntime(t, transport)
	} else {
		ancestry.hook = func(req *http.Request) (*http.Response, bool) {
			if failure == "ancestor_denied" && req.URL.Path == "/v3/folders/456" {
				return apiResponse(req, 403, `{}`), true
			}
			return nil, false
		}
		r = securityAncestorRuntime(t, ancestry, transport)
	}
	kind := r.resourceKind(securityServiceType)
	var source contracts.InventorySource
	for _, value := range r.InventorySources() {
		if value.Name == securityServiceSource {
			source = value
		}
	}
	if source.AuthoritativeDefault || !source.KindSpecific || source.NetworkClosure {
		t.Fatal("missing native product source")
	}
	repositories, err := sqlite.Open(filepath.Join(t.TempDir(), "security-services-inventory.db"), filepath.Join("..", "..", "migrations"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 9, 0, 0, 0, 0, time.UTC)
	connection := asset.CloudConnection{ID: "connection", Name: "SecurityServices", Provider: asset.ProviderGCP, Partition: "google-cloud", Principal: "fixture", Status: asset.ConnectionActive, CreatedAt: now, UpdatedAt: now}
	scope := asset.Scope{ID: "security-services-region", ConnectionID: connection.ID, Kind: asset.ScopeRegion, NativeID: "eu", Name: "eu", CreatedAt: now, UpdatedAt: now}
	for _, write := range []func() error{
		func() error { return repositories.Connections().PutConnection(ctx, connection) },
		func() error { return repositories.Inventory().PutScope(ctx, scope) },
		func() error { return repositories.Inventory().PutResourceKind(ctx, kind) },
	} {
		if err := write(); err != nil {
			t.Fatal(err)
		}
	}
	service := inventory.NewService(repositories.Inventory(), inventory.WithClock(func() time.Time { return now }))
	handler := inventory.NewScanHandler(repositories, dataformVisibilityRuntime{adapter: r}, service)
	var lastObserved time.Time
	runs := []string{"first", "denied", "missing", "changed", "list_state", "list_partial", "list_token", "detail_partial", "hidden_location", "hidden_service", "updated", "legacy"}
	if parent != "projects/sample-project" {
		runs = append([]string{"first", "ancestor_denied", "ancestry_hidden"}, runs[1:]...)
	}
	for _, runID := range runs {
		run := asset.ScanRun{ID: asset.ScanRunID(runID), ConnectionID: connection.ID, Status: asset.ScanPending, RequestedBy: "fixture", CreatedAt: now}
		shard := asset.ScanShard{ID: asset.ScanShardID("security-services-" + runID), ScanRunID: run.ID, Provider: asset.ProviderGCP, Source: source.Name, ScopeID: scope.ID, ResourceKindID: kind.ID, Authoritative: source.AuthoritativeDefault, Status: asset.ShardPending, CreatedAt: now}
		if runID == "legacy" {
			shard.Source, shard.Authoritative = productInventorySource, true
		}
		if err := repositories.Inventory().CreateScanRun(ctx, run); err != nil {
			t.Fatal(err)
		}
		if err := repositories.Inventory().PutScanShard(ctx, shard); err != nil {
			t.Fatal(err)
		}
		if parent != "projects/sample-project" {
			ancestry.project["parent"] = "folders/456"
			if runID == "ancestry_hidden" {
				ancestry.project["parent"] = ""
			}
		}
		failure = runID
		err := handler.Handle(ctx, execution.Job{ID: execution.JobID("security-services-" + runID), Type: execution.JobScan, Payload: map[string]any{"scan_shard_id": string(shard.ID)}})
		failed := runID == "list_state" || runID == "list_partial" || runID == "list_token" || runID == "detail_partial" || runID == "ancestor_denied" || runID == "denied" || runID == "missing" || runID == "changed" || runID == "legacy"
		if !failed && err != nil || failed && err == nil {
			t.Fatalf("scan %s: %v", runID, err)
		}
		finished, err := repositories.Inventory().GetScanShard(ctx, shard.ID)
		if err != nil {
			t.Fatal(err)
		}
		if !failed && (!finished.Coverage.Complete || finished.Status != asset.ShardSucceeded) || failed && (finished.Coverage.Complete || finished.Status != asset.ShardFailed) {
			t.Fatalf("wrong coverage: %+v", finished)
		}
		page, err := repositories.Inventory().ListAssets(ctx, persistence.ListOptions{Limit: 10})
		if err != nil || len(page.Items) != 1 {
			t.Fatalf("Service projection: %d %v", len(page.Items), err)
		}
		encoded, _ := json.Marshal(page.Items)
		if strings.Contains(string(encoded), "PRIVATE_NESTED_CONFIG") {
			t.Fatal("private configuration persisted")
		}
		wantState := "INGEST_ONLY"
		if runID == "updated" || runID == "legacy" {
			wantState = "DISABLED"
		}
		if runID == "first" || runID == "updated" {
			if page.Items[0].LastSeenAt.IsZero() || runID == "updated" && !page.Items[0].LastSeenAt.After(lastObserved) {
				t.Fatal("fresh service observation has no new timestamp")
			}
			lastObserved = page.Items[0].LastSeenAt
		}
		for _, value := range page.Items {
			if value.Normalized["configurationParent"] != parent {
				t.Fatal("configuration parent lost", value.Normalized)
			}
			if !value.LastSeenAt.Equal(lastObserved) {
				t.Fatal("unobserved service was marked fresh", runID, value.LastSeenAt, lastObserved)
			}
			if value.ClosedAt != nil || value.DeletedAt != nil || value.Normalized["effectiveEnablementState"] != wantState || len(object(value.Normalized["modules"])) != 1 {
				t.Fatal("failed detail scan lost the last complete service", value)
			}
		}
		expression, err := resourcequery.Parse(`properties.effectiveEnablementState = "` + wantState + `" AND properties.intendedEnablementState = "INHERITED" AND properties.configurationParent = "` + parent + `"`)
		if err != nil {
			t.Fatal(err)
		}
		if err := expression.Validate([]asset.ResourceKind{kind}); err != nil {
			t.Fatal(err)
		}
		matched, err := repositories.Inventory().ListAssets(ctx, persistence.ListOptions{Limit: 10, ResourceQuery: expression})
		if err != nil || len(matched.Items) != 1 {
			t.Fatal("native service query lost after detail scan failure", matched, err)
		}
		now = now.Add(time.Minute)
	}
}

func TestSecurityServicesCreatorUsesNonAuthoritativeSource(t *testing.T) {
	ctx := t.Context()
	r := protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
		t.Fatal("scan creation must not call service API", req.URL)
		return nil, nil
	})
	repositories, err := sqlite.Open(filepath.Join(t.TempDir(), "service-creator.db"), filepath.Join("..", "..", "migrations"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 14, 0, 0, 0, 0, time.UTC)
	connection := asset.CloudConnection{ID: "connection", Provider: asset.ProviderGCP, Partition: "gcp", Status: asset.ConnectionActive, CreatedAt: now, UpdatedAt: now}
	root := asset.Scope{ID: "project", ConnectionID: connection.ID, Kind: asset.ScopeProject, NativeID: "sample-project", Name: "Project", CreatedAt: now, UpdatedAt: now}
	region := asset.ConnectionRegion{ID: "region", ConnectionID: connection.ID, RegionID: "eu", DiscoveredName: "Europe", Origin: asset.RegionOriginAPI, Lifecycle: asset.RegionActive, CreatedAt: now, UpdatedAt: now}
	for _, write := range []func() error{func() error { return repositories.Connections().PutConnection(ctx, connection) }, func() error { return repositories.Inventory().PutScope(ctx, root) }, func() error { return repositories.Regions().PutRegion(ctx, region) }} {
		if err := write(); err != nil {
			t.Fatal(err)
		}
	}
	registry := providerruntime.NewRegistry()
	if err := registry.Register(r); err != nil {
		t.Fatal(err)
	}
	if err := registry.RegisterBundle(r.Bundle()); err != nil {
		t.Fatal(err)
	}
	creator, err := inventory.NewCreator(repositories, registry, inventory.WithCreatorClock(func() time.Time { return now }))
	if err != nil {
		t.Fatal(err)
	}
	for _, location := range []string{"eu", "global"} {
		created, err := creator.Create(ctx, inventory.ScanCreationRequest{ConnectionID: connection.ID, RequestedBy: "test", RegionMode: inventory.RegionModeSelected, RegionIDs: []string{location}, ResourceKindIDs: []asset.ResourceKindID{r.resourceKind(securityServiceType).ID}})
		if err != nil || len(created.Shards) != 1 || len(created.Jobs) != 1 {
			t.Fatal("service source routing", location, created, err)
		}
		shard := created.Shards[0]
		if shard.Source != securityServiceSource || shard.Authoritative || shard.Coverage.Authoritative || shard.RegionID != location {
			t.Fatal("incorrect service authority", shard)
		}
		stored, err := repositories.Inventory().GetScanShard(ctx, shard.ID)
		if err != nil || stored.Source != securityServiceSource || stored.Authoritative {
			t.Fatal("stored service authority", stored, err)
		}
		now = now.Add(time.Minute)
	}
}
