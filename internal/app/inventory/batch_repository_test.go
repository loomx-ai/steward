package inventory_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/loomx-ai/steward/internal/app/inventory"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/persistence"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

type countingInventory struct {
	persistence.InventoryRepository
	calls map[string]int
}

func (r countingInventory) WithinInventoryTx(ctx context.Context, fn func(persistence.InventoryRepository) error) error {
	return r.InventoryRepository.WithinInventoryTx(ctx, func(tx persistence.InventoryRepository) error {
		return fn(countingInventory{tx, r.calls})
	})
}

func (r countingInventory) PutResourceKind(ctx context.Context, kind asset.ResourceKind) error {
	r.calls["PutResourceKind"]++
	return r.InventoryRepository.PutResourceKind(ctx, kind)
}

func (r countingInventory) GetScope(ctx context.Context, id asset.ScopeID) (asset.Scope, error) {
	r.calls["GetScope"]++
	return r.InventoryRepository.GetScope(ctx, id)
}

func (r countingInventory) GetAssetByIdentity(ctx context.Context, identity asset.Identity) (asset.Asset, error) {
	r.calls["GetAssetByIdentity"]++
	return r.InventoryRepository.GetAssetByIdentity(ctx, identity)
}

func (r countingInventory) GetObservation(ctx context.Context, id asset.ObservationID) (asset.Observation, error) {
	r.calls["GetObservation"]++
	return r.InventoryRepository.GetObservation(ctx, id)
}

func (r countingInventory) PutAsset(ctx context.Context, value asset.Asset) error {
	r.calls["PutAsset"]++
	return r.InventoryRepository.PutAsset(ctx, value)
}

func (r countingInventory) PutAssets(ctx context.Context, values []asset.Asset) error {
	r.calls["PutAssets"]++
	return r.InventoryRepository.PutAssets(ctx, values)
}

func (r countingInventory) AppendObservations(ctx context.Context, observations []asset.Observation) error {
	r.calls["AppendObservations"]++
	return r.InventoryRepository.AppendObservations(ctx, observations)
}

func (r countingInventory) RecordAssetChanges(ctx context.Context, changes []asset.AssetChange) error {
	r.calls["RecordAssetChanges"]++
	return r.InventoryRepository.RecordAssetChanges(ctx, changes)
}

// A batch reads its assets and scopes once and writes its observations,
// assets and changes in one call each.
func TestProjectBatchReadsAssetsAndScopesOncePerBatch(t *testing.T) {
	ctx, now := context.Background(), time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC)
	repositories := openInventoryWorkerRepositories(t)
	seedScanWorker(t, repositories, now)
	repository := countingInventory{repositories.Inventory(), map[string]int{}}
	service := inventory.NewService(repository)
	connection := asset.CloudConnection{ID: "connection-worker", Provider: asset.ProviderAliCloud, Partition: "public"}
	shard := asset.ScanShard{ID: "shard-worker", ScanRunID: "run-worker", Provider: asset.ProviderAliCloud, Source: "resource-center", ScopeID: "scope-worker", ResourceKindID: "kind-worker", Authoritative: true, CreatedAt: now}
	items := make([]contracts.InventoryItem, 0, 51)
	for index := range 50 {
		items = append(items, contracts.InventoryItem{
			NativeType: workerKind().NativeType, NativeID: fmt.Sprintf("i-%02d", index), ResourceKind: workerKind(),
			Name: "first", Raw: map[string]any{"name": "first"},
		})
	}
	if err := service.ProjectBatch(ctx, &shard, connection, contracts.InventoryBatch{Items: items}, inventory.ProjectionOptions{ObservedAt: now}); err != nil {
		t.Fatal(err)
	}
	clear(repository.calls)
	for index := range items {
		items[index].Name, items[index].Raw = "second", map[string]any{"name": "second"}
	}
	// The same resource twice in one batch must see its own earlier write,
	// which is still held for the batch's single asset write.
	duplicate := items[0]
	duplicate.Name, duplicate.Raw = "third", map[string]any{"name": "third"}
	items = append(items, duplicate)
	if err := service.ProjectBatch(ctx, &shard, connection, contracts.InventoryBatch{Items: items}, inventory.ProjectionOptions{ObservedAt: now.Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	want := map[string]int{"PutResourceKind": 1, "GetScope": 1, "AppendObservations": 1, "PutAssets": 1, "RecordAssetChanges": 1}
	if fmt.Sprint(repository.calls) != fmt.Sprint(want) {
		t.Fatalf("repository calls = %v, want %v", repository.calls, want)
	}
	// Both batches belong to one scan, so each asset's changes fold into one.
	changes, err := repositories.Inventory().ListAssetChanges(ctx, persistence.AssetChangeListOptions{ScanTaskID: "run-worker", Limit: 100})
	if err != nil || len(changes.Items) != 50 {
		t.Fatalf("changes = %d, err = %v", len(changes.Items), err)
	}
	for _, change := range changes.Items {
		want := "second"
		if change.NativeID == "i-00" {
			want = "third"
		}
		if change.Type != asset.ChangeAdded || change.Name != want {
			t.Fatalf("change = %+v, want an addition named %q", change, want)
		}
	}
	assets, err := repositories.Inventory().ListActiveAssetsByConnection(ctx, connection.ID, "kind-worker")
	if err != nil || len(assets) != 50 {
		t.Fatalf("assets = %d, err = %v", len(assets), err)
	}
	for _, value := range assets {
		want := "second"
		if value.Identity.NativeID == "i-00" {
			want = "third"
		}
		current, err := repositories.Inventory().GetObservation(ctx, value.CurrentObservationID)
		if err != nil || value.Name != want || !current.ObservedAt.Equal(now.Add(time.Minute)) {
			t.Fatalf("asset %s = %q, current %#v, err = %v", value.Identity.NativeID, value.Name, current, err)
		}
	}
}
