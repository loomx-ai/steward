package azure

import (
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/loomx-ai/steward/internal/app/inventory"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/persistence/sqlite"
	providerruntime "github.com/loomx-ai/steward/internal/provider/runtime"
)

func TestFleetHubScanWorkerPersistsAndRecoversMembers(t *testing.T) {
	ctx := t.Context()
	h := newFleetHubMembersFixture(t)
	path := filepath.Join(t.TempDir(), "fleet-hub.db")
	repository, err := sqlite.Open(path, "../../migrations")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	connection := asset.CloudConnection{ID: "connection", Provider: asset.ProviderAzure, Partition: "azure", Status: asset.ConnectionActive, CreatedAt: now, UpdatedAt: now}
	if err := repository.PutConnection(ctx, connection); err != nil {
		t.Fatal(err)
	}
	if err := repository.PutCredential(ctx, asset.ConnectionCredential{ConnectionID: connection.ID, Provider: asset.ProviderAzure, Type: asset.CredentialAzureServicePrincipal, EnvelopeVersion: 1, Nonce: "test", Ciphertext: "test", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	root := asset.Scope{ID: "root", ConnectionID: connection.ID, Kind: asset.ScopeSubscription, NativeID: testSubscription, CreatedAt: now, UpdatedAt: now}
	if err := repository.PutScope(ctx, root); err != nil {
		t.Fatal(err)
	}
	if err := repository.PutRegion(ctx, asset.ConnectionRegion{ID: "west", ConnectionID: connection.ID, RegionID: "westus", Origin: asset.RegionOriginAPI, Lifecycle: asset.RegionActive, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	member := h.hub + "/providers/microsoft.insights/actiongroups/hub-alerts"
	var first asset.Asset
	var previousProof string
	for _, mode := range []string{"initial", "omitted", "forbidden", "missing"} {
		if mode == "omitted" {
			h.lists["/subscriptions/"+testSubscription+"/providers/microsoft.insights/actiongroups"] = []any{}
		}
		if mode == "forbidden" {
			h.override = func(req *http.Request) (*http.Response, bool) {
				if strings.EqualFold(req.URL.Path, member) {
					return jsonResponse(403, map[string]any{}, nil), true
				}
				return nil, false
			}
		}
		if mode == "missing" {
			h.override, h.gone[member] = nil, true
		}
		// Reopen storage and recreate the provider, client, registry and worker.
		// Recovery must come from persisted normalized metadata, not memory.
		repository, err := sqlite.Open(path, "../../migrations")
		if err != nil {
			t.Fatal(err)
		}
		r, err := NewRuntime(h.runtime.credentials)
		if err != nil {
			t.Fatal(err)
		}
		r.transport = h.runtime.transport
		registry := providerruntime.NewRegistry()
		if err := registry.Register(r); err != nil {
			t.Fatal(err)
		}
		if err := registry.RegisterBundle(r.Bundle()); err != nil {
			t.Fatal(err)
		}
		creator, err := inventory.NewCreator(repository, registry)
		if err != nil {
			t.Fatal(err)
		}
		created, err := creator.Create(ctx, inventory.ScanCreationRequest{ConnectionID: connection.ID, RequestedBy: "fleet-hub-worker", RegionMode: inventory.RegionModeSelected, RegionIDs: []string{"westus"}, ResourceKindIDs: []asset.ResourceKindID{r.resourceKind(fleetType).ID}})
		if err != nil || len(created.Shards) != 1 || created.Shards[0].Authoritative || created.Shards[0].Source != fleetInventorySource {
			t.Fatal("Fleet Hub scan lost its registered source", created, err)
		}
		handler := inventory.NewScanHandler(repository, registry, inventory.NewService(repository))
		before := h.calls["GET "+member]
		for _, job := range created.Jobs {
			if err := handler.Handle(ctx, job); err != nil && mode != "forbidden" {
				t.Fatal("Fleet Hub scan worker failed", mode, err)
			}
		}
		shard, err := repository.GetScanShard(ctx, created.Shards[0].ID)
		if err != nil || shard.Authoritative || shard.Coverage.Authoritative || (shard.Status == asset.ShardSucceeded) == (mode == "forbidden") || h.calls["GET "+member] <= before {
			t.Fatal("Fleet Hub worker lost own-read/coverage boundaries", mode, shard, err)
		}
		values, err := repository.ListActiveAssetsByConnection(ctx, connection.ID, "")
		if err != nil || len(values) != 1 {
			t.Fatal("member omission deleted or duplicated the Fleet", mode, values, err)
		}
		value := values[0]
		c, err := r.resolve(ctx, connection.ID)
		if err != nil {
			t.Fatal(err)
		}
		state, err := c.fleetRecordedHub(value.Identity.NativeID, value.Normalized)
		if err != nil || state["mode"] != "managed" || value.Capabilities.Has(asset.CapabilityActionable) {
			t.Fatal("stored Hub membership proof or root protection changed", state, err)
		}
		members := object(state["members"])
		proof := text(value.Normalized[fleetHubProof])
		if mode == "initial" {
			first, previousProof = value, proof
		}
		if value.ID != first.ID || mode != "missing" && (len(members) != 17 || proof != previousProof) || mode == "missing" && (len(members) != 16 || members[member] != nil || proof == previousProof) {
			t.Fatal("worker failed persisted Hub member reconciliation", mode, members)
		}
	}
}
