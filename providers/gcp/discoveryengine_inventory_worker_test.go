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
)

// Exercise the shipped product source through the actual SQLite projection and
// scan worker. Losing access to parent data stores cannot close their observed auxiliary documents.
func TestDiscoveryEngineDocumentScanPermissionFailurePreservesObservations(t *testing.T) {
	ctx := context.Background()
	s := newDiscoveryScenario(t)
	s.emptyPage = true
	r := protocolRuntime(t, s.transport(t))
	kind := r.resourceKind(discoveryHost + "/Document")
	var source contracts.InventorySource
	for _, value := range r.InventorySources() {
		if value.Name == productInventorySource {
			source = value
		}
	}
	if !source.AuthoritativeDefault || !source.KindSpecific {
		t.Fatal("missing native product source")
	}
	repositories, err := sqlite.Open(filepath.Join(t.TempDir(), "discoveryengine-inventory.db"), filepath.Join("..", "..", "migrations"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 9, 0, 0, 0, 0, time.UTC)
	connection := asset.CloudConnection{ID: "connection", Name: "Discovery Engine", Provider: asset.ProviderGCP, Partition: "google-cloud", Principal: "fixture", Status: asset.ConnectionActive, CreatedAt: now, UpdatedAt: now}
	scope := asset.Scope{ID: "discoveryengine-region", ConnectionID: connection.ID, Kind: asset.ScopeGlobal, NativeID: "global", Name: "global", CreatedAt: now, UpdatedAt: now}
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
	for _, runID := range []string{"first", "denied"} {
		run := asset.ScanRun{ID: asset.ScanRunID(runID), ConnectionID: connection.ID, Status: asset.ScanPending, RequestedBy: "fixture", CreatedAt: now}
		shard := asset.ScanShard{ID: asset.ScanShardID("discoveryengine-" + runID), ScanRunID: run.ID, Provider: asset.ProviderGCP, Source: source.Name, ScopeID: scope.ID, ResourceKindID: kind.ID, Authoritative: source.AuthoritativeDefault, Status: asset.ShardPending, CreatedAt: now}
		if err := repositories.Inventory().CreateScanRun(ctx, run); err != nil {
			t.Fatal(err)
		}
		if err := repositories.Inventory().PutScanShard(ctx, shard); err != nil {
			t.Fatal(err)
		}
		if runID == "denied" {
			s.handle = func(req *http.Request) (*http.Response, bool) {
				if req.URL.Path == "/v1alpha/projects/sample-project/locations/global/collections" {
					return dataformResponse(req, 403, map[string]any{}), true
				}
				return nil, false
			}
		}
		err := handler.Handle(ctx, execution.Job{ID: execution.JobID("discoveryengine-" + runID), Type: execution.JobScan, Payload: map[string]any{"scan_shard_id": string(shard.ID)}})
		if runID == "first" && err != nil || runID == "denied" && err == nil {
			t.Fatalf("scan %s: %v", runID, err)
		}
		finished, err := repositories.Inventory().GetScanShard(ctx, shard.ID)
		if err != nil {
			t.Fatal(err)
		}
		if runID == "first" && (!finished.Coverage.Complete || finished.Status != asset.ShardSucceeded) || runID == "denied" && (finished.Coverage.Complete || finished.Status != asset.ShardFailed) {
			t.Fatalf("wrong coverage: %+v", finished)
		}
		page, err := repositories.Inventory().ListAssets(ctx, persistence.ListOptions{Limit: 10})
		if err != nil || len(page.Items) != 1 {
			t.Fatalf("Document projection: %d %v", len(page.Items), err)
		}
		for _, value := range page.Items {
			if value.ClosedAt != nil || value.DeletedAt != nil || text(value.Normalized[discoveryParentProof]) == "" || text(value.Normalized["_discoveryengine_ancestors"]) == "" {
				t.Fatalf("failed scan lost native document proof: %+v", value)
			}
		}
		expression, err := resourcequery.Parse(`properties.indexedAt = "2026-08-01T13:00:00Z" AND properties.id = "document-1"`)
		if err != nil {
			t.Fatal(err)
		}
		if err := expression.Validate([]asset.ResourceKind{kind}); err != nil {
			t.Fatal(err)
		}
		matched, err := repositories.Inventory().ListAssets(ctx, persistence.ListOptions{Limit: 10, ResourceQuery: expression})
		if err != nil || len(matched.Items) != 1 {
			t.Fatal("native index timestamp query lost after scan or failure", matched, err)
		}
		encoded, _ := json.Marshal(page)
		if strings.Contains(string(encoded), "DISCOVERY_PRIVATE_") {
			t.Fatal("document payload escaped SQLite redaction")
		}
		now = now.Add(time.Minute)
	}
}
