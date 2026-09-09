package gcp

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/loomx-ai/steward/internal/app/inventory"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/persistence"
	"github.com/loomx-ai/steward/internal/persistence/sqlite"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

type dataformVisibilityRuntime struct{ adapter contracts.InventoryAdapter }

func (r dataformVisibilityRuntime) ResolveInventory(asset.Provider) (contracts.InventoryAdapter, error) {
	return r.adapter, nil
}

// An empty successful, permission-filtered search is not proof of deletion.
// Use the shipped GCP source declaration and resource rule through SQLite
// projection and the real inventory worker's completion/reconciliation path.
func TestDataformVisibleProjectInventoryKeepsHiddenObservations(t *testing.T) {
	ctx := context.Background()
	s := newDataformFolderScenario(t)
	runtime := protocolRuntime(t, s.transport(t))
	var kind asset.ResourceKind
	var source contracts.InventorySource
	for _, compiled := range runtime.Bundle().Specs {
		if compiled.ResourceKind.NativeType == "dataform.googleapis.com/Folder" {
			kind = compiled.ResourceKind
			for _, candidate := range runtime.InventorySources() {
				if candidate.Name == compiled.Definition.Discovery.Source {
					source = candidate
				}
			}
		}
	}
	if kind.ID == "" || source.Name == "" || source.AuthoritativeDefault {
		t.Fatal("Dataform visible source must be nonauthoritative")
	}
	repositories, err := sqlite.Open(filepath.Join(t.TempDir(), "dataform-visibility.db"), filepath.Join("..", "..", "migrations"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 9, 0, 0, 0, 0, time.UTC)
	connection := asset.CloudConnection{ID: "connection", Name: "Dataform", Provider: asset.ProviderGCP, Partition: "google-cloud", Principal: "fixture", Status: asset.ConnectionActive, CreatedAt: now, UpdatedAt: now}
	scope := asset.Scope{ID: "dataform-project", ConnectionID: connection.ID, Kind: asset.ScopeProject, NativeID: "sample-project", Name: "sample-project", CreatedAt: now, UpdatedAt: now}
	run := asset.ScanRun{ID: "dataform-run", ConnectionID: connection.ID, Status: asset.ScanPending, RequestedBy: "fixture", CreatedAt: now}
	shard := asset.ScanShard{ID: "dataform-first", ScanRunID: run.ID, Provider: asset.ProviderGCP, Source: source.Name, ScopeID: scope.ID, ResourceKindID: kind.ID, Authoritative: source.AuthoritativeDefault, Status: asset.ShardPending, CreatedAt: now}
	for _, write := range []func() error{
		func() error { return repositories.Connections().PutConnection(ctx, connection) },
		func() error { return repositories.Inventory().PutScope(ctx, scope) },
		func() error { return repositories.Inventory().PutResourceKind(ctx, kind) },
		func() error { return repositories.Inventory().CreateScanRun(ctx, run) },
		func() error { return repositories.Inventory().PutScanShard(ctx, shard) },
	} {
		if err := write(); err != nil {
			t.Fatal(err)
		}
	}
	service := inventory.NewService(repositories.Inventory(), inventory.WithClock(func() time.Time { return now }))
	handler := inventory.NewScanHandler(repositories, dataformVisibilityRuntime{adapter: runtime}, service)
	if err := handler.Handle(ctx, execution.Job{ID: "visible-scan", Type: execution.JobScan, Payload: map[string]any{"scan_shard_id": string(shard.ID)}}); err != nil {
		t.Fatal(err)
	}
	first, err := repositories.Inventory().ListAssets(ctx, persistence.ListOptions{Limit: 10})
	if err != nil || len(first.Items) != 4 {
		t.Fatalf("native folders were not projected: %+v %v", first, err)
	}
	run.ID = "dataform-hidden-run"
	if err := repositories.Inventory().CreateScanRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	shard.ID = "dataform-hidden"
	shard.ScanRunID = run.ID
	shard.Status = asset.ShardPending
	shard.Coverage = asset.Coverage{}
	if err := repositories.Inventory().PutScanShard(ctx, shard); err != nil {
		t.Fatal(err)
	}
	for query := range s.seeds {
		s.seeds[query] = nil
	}
	if err := handler.Handle(ctx, execution.Job{ID: "hidden-scan", Type: execution.JobScan, Payload: map[string]any{"scan_shard_id": string(shard.ID)}}); err != nil {
		t.Fatal(err)
	}
	page, err := repositories.Inventory().ListAssets(ctx, persistence.ListOptions{Limit: 10})
	if err != nil || len(page.Items) != 4 {
		t.Fatalf("visibility loss was treated as deletion: %+v %v", page, err)
	}
	for _, value := range page.Items {
		if value.ClosedAt != nil || value.DeletedAt != nil {
			t.Fatalf("hidden folder was closed: %+v", value)
		}
	}
	finished, err := repositories.Inventory().GetScanShard(ctx, shard.ID)
	if err != nil || finished.Status != asset.ShardSucceeded || !finished.Coverage.Complete || finished.Authoritative {
		t.Fatalf("incorrect visibility completion: %+v %v", finished, err)
	}
}
