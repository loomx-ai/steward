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
// observed table or replace its full metadata with a list summary.
func TestBigtableDetailFailurePreservesSQLiteObservations(t *testing.T) {
	ctx := context.Background()
	failure := ""
	r := protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
		if req.Method != "GET" || req.URL.Host != bigtableTestHost {
			t.Fatalf("unexpected request %s", req.URL)
		}
		switch req.URL.Path {
		case "/v2/projects/sample-project/instances":
			return dataformResponse(req, 200, map[string]any{"instances": []any{map[string]any{"name": bigtableTestParent}}}), nil
		case "/v2/" + bigtableTestParent + "/tables":
			return dataformResponse(req, 200, map[string]any{"tables": []any{map[string]any{"name": bigtableTestName}}}), nil
		case "/v2/" + bigtableTestName:
			if req.URL.Query().Get("view") != "FULL" {
				t.Fatal("scan requested incomplete view", req.URL)
			}
			if failure == "denied" {
				return dataformResponse(req, 403, map[string]any{}), nil
			}
			if failure == "missing" {
				return dataformResponse(req, 404, map[string]any{}), nil
			}
			data := bigtableTestData(bigtableTestName)
			if failure == "changed" {
				data["name"] = bigtableTestName + "-other"
			}
			return dataformResponse(req, 200, data), nil
		}
		t.Fatalf("unexpected request %s", req.URL)
		return nil, nil
	})
	kind := r.resourceKind(bigtableTestKind)
	var source contracts.InventorySource
	for _, value := range r.InventorySources() {
		if value.Name == productInventorySource {
			source = value
		}
	}
	if !source.AuthoritativeDefault || !source.KindSpecific {
		t.Fatal("missing native product source")
	}
	repositories, err := sqlite.Open(filepath.Join(t.TempDir(), "bigtable-inventory.db"), filepath.Join("..", "..", "migrations"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 9, 0, 0, 0, 0, time.UTC)
	connection := asset.CloudConnection{ID: "connection", Name: "Bigtable", Provider: asset.ProviderGCP, Partition: "google-cloud", Principal: "fixture", Status: asset.ConnectionActive, CreatedAt: now, UpdatedAt: now}
	scope := asset.Scope{ID: "bigtable-region", ConnectionID: connection.ID, Kind: asset.ScopeGlobal, NativeID: "global", Name: "global", CreatedAt: now, UpdatedAt: now}
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
		shard := asset.ScanShard{ID: asset.ScanShardID("bigtable-" + runID), ScanRunID: run.ID, Provider: asset.ProviderGCP, Source: source.Name, ScopeID: scope.ID, ResourceKindID: kind.ID, Authoritative: source.AuthoritativeDefault, Status: asset.ShardPending, CreatedAt: now}
		if err := repositories.Inventory().CreateScanRun(ctx, run); err != nil {
			t.Fatal(err)
		}
		if err := repositories.Inventory().PutScanShard(ctx, shard); err != nil {
			t.Fatal(err)
		}
		failure = runID
		err := handler.Handle(ctx, execution.Job{ID: execution.JobID("bigtable-" + runID), Type: execution.JobScan, Payload: map[string]any{"scan_shard_id": string(shard.ID)}})
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
			t.Fatalf("Table projection: %d %v", len(page.Items), err)
		}
		for _, value := range page.Items {
			if value.ClosedAt != nil || value.DeletedAt != nil || value.Normalized["deletionProtection"] != true || len(object(value.Normalized["columnFamilies"])) != 1 {
				t.Fatal("failed detail scan lost the last complete table", value)
			}
		}
		expression, err := resourcequery.Parse(`properties.deletionProtection = true AND properties.granularity = "MILLIS"`)
		if err != nil {
			t.Fatal(err)
		}
		if err := expression.Validate([]asset.ResourceKind{kind}); err != nil {
			t.Fatal(err)
		}
		matched, err := repositories.Inventory().ListAssets(ctx, persistence.ListOptions{Limit: 10, ResourceQuery: expression})
		if err != nil || len(matched.Items) != 1 {
			t.Fatal("native table query lost after detail scan failure", matched, err)
		}
		now = now.Add(time.Minute)
	}
}
