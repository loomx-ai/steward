package gcp

import (
	"context"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/loomx-ai/steward/internal/app/inventory"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/core/resourcequery"
	"github.com/loomx-ai/steward/internal/persistence"
	"github.com/loomx-ai/steward/internal/persistence/sqlite"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

// A successful LIST followed by a failed detail GET cannot close the previously
// observed service or replace its full metadata with a list summary.
func TestSecurityServicesDetailFailurePreservesSQLiteObservations(t *testing.T) {
	ctx := context.Background()
	failure := ""
	name := "projects/sample-project/locations/global/securityCenterServices/event-threat-detection"
	r := protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
		if req.Method != "GET" || req.URL.Host != securityServiceHost {
			t.Fatalf("unexpected request %s", req.URL)
		}
		switch req.URL.Path {
		case "/v1/projects/sample-project/locations/global/securityCenterServices":
			return dataformResponse(req, 200, map[string]any{"securityCenterServices": []any{map[string]any{"name": name}}}), nil
		case "/v1/" + name:
			if failure == "denied" {
				return dataformResponse(req, 403, map[string]any{}), nil
			}
			if failure == "missing" {
				return dataformResponse(req, 404, map[string]any{}), nil
			}
			data := securityServiceData(name)
			if failure == "changed" {
				data["name"] = name + "-other"
			}
			return dataformResponse(req, 200, data), nil
		}
		t.Fatalf("unexpected request %s", req.URL)
		return nil, nil
	})
	kind := r.resourceKind(securityServiceType)
	var source contracts.InventorySource
	for _, value := range r.InventorySources() {
		if value.Name == productInventorySource {
			source = value
		}
	}
	if !source.AuthoritativeDefault || !source.KindSpecific {
		t.Fatal("missing native product source")
	}
	repositories, err := sqlite.Open(filepath.Join(t.TempDir(), "security-services-inventory.db"), filepath.Join("..", "..", "migrations"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 9, 0, 0, 0, 0, time.UTC)
	connection := asset.CloudConnection{ID: "connection", Name: "SecurityServices", Provider: asset.ProviderGCP, Partition: "google-cloud", Principal: "fixture", Status: asset.ConnectionActive, CreatedAt: now, UpdatedAt: now}
	scope := asset.Scope{ID: "security-services-region", ConnectionID: connection.ID, Kind: asset.ScopeGlobal, NativeID: "global", Name: "global", CreatedAt: now, UpdatedAt: now}
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
	for _, runID := range []string{"first", "denied", "missing", "changed"} {
		run := asset.ScanRun{ID: asset.ScanRunID(runID), ConnectionID: connection.ID, Status: asset.ScanPending, RequestedBy: "fixture", CreatedAt: now}
		shard := asset.ScanShard{ID: asset.ScanShardID("security-services-" + runID), ScanRunID: run.ID, Provider: asset.ProviderGCP, Source: source.Name, ScopeID: scope.ID, ResourceKindID: kind.ID, Authoritative: source.AuthoritativeDefault, Status: asset.ShardPending, CreatedAt: now}
		if err := repositories.Inventory().CreateScanRun(ctx, run); err != nil {
			t.Fatal(err)
		}
		if err := repositories.Inventory().PutScanShard(ctx, shard); err != nil {
			t.Fatal(err)
		}
		failure = runID
		err := handler.Handle(ctx, execution.Job{ID: execution.JobID("security-services-" + runID), Type: execution.JobScan, Payload: map[string]any{"scan_shard_id": string(shard.ID)}})
		if runID == "first" && err != nil || runID != "first" && err == nil {
			t.Fatalf("scan %s: %v", runID, err)
		}
		finished, err := repositories.Inventory().GetScanShard(ctx, shard.ID)
		if err != nil {
			t.Fatal(err)
		}
		if runID == "first" && (!finished.Coverage.Complete || finished.Status != asset.ShardSucceeded) || runID != "first" && (finished.Coverage.Complete || finished.Status != asset.ShardFailed) {
			t.Fatalf("wrong coverage: %+v", finished)
		}
		page, err := repositories.Inventory().ListAssets(ctx, persistence.ListOptions{Limit: 10})
		if err != nil || len(page.Items) != 1 {
			t.Fatalf("Service projection: %d %v", len(page.Items), err)
		}
		for _, value := range page.Items {
			if value.ClosedAt != nil || value.DeletedAt != nil || value.Normalized["effectiveEnablementState"] != "INGEST_ONLY" || len(object(value.Normalized["modules"])) != 1 {
				t.Fatal("failed detail scan lost the last complete service", value)
			}
		}
		expression, err := resourcequery.Parse(`properties.effectiveEnablementState = "INGEST_ONLY" AND properties.intendedEnablementState = "INHERITED"`)
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
