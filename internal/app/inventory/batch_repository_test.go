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
	// The same resource twice in one batch must see its own earlier write.
	duplicate := items[0]
	duplicate.Name, duplicate.Raw = "third", map[string]any{"name": "third"}
	items = append(items, duplicate)
	if err := service.ProjectBatch(ctx, &shard, connection, contracts.InventoryBatch{Items: items}, inventory.ProjectionOptions{ObservedAt: now.Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	if want := map[string]int{"PutResourceKind": 1, "GetScope": 1, "GetAssetByIdentity": 1, "GetObservation": 1}; fmt.Sprint(repository.calls) != fmt.Sprint(want) {
		t.Fatalf("repository reads = %v, want %v", repository.calls, want)
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
