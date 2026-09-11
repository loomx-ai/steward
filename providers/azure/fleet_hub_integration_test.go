package azure

import (
	"context"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/loomx-ai/steward/internal/app/governance"
	"github.com/loomx-ai/steward/internal/app/inventory"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/persistence/sqlite"
	providerruntime "github.com/loomx-ai/steward/internal/provider/runtime"
)

type fleetHubGraphContributors struct{ runtime *Runtime }

func (r fleetHubGraphContributors) ResolveContributors(ctx context.Context, connection asset.CloudConnection, _ []asset.Asset) ([]governance.Contributor, error) {
	service, err := r.runtime.ServiceLifecycle(ctx, connection.ID)
	if err != nil {
		return nil, err
	}
	clusters, err := r.runtime.ClusterLifecycle(ctx, connection.ID)
	return []governance.Contributor{NewResourceAttachments(), service, clusters}, err
}

func TestFleetHubGraphWorkerPersistsExclusiveOwnership(t *testing.T) {
	ctx := t.Context()
	h := newFleetHubMembersFixture(t)
	values := h.graphAssets(t)
	root := fleetAssetByKind(t, values, fleetType)
	path := filepath.Join(t.TempDir(), "fleet-hub-graph.db")
	repository, err := sqlite.Open(path, "../../migrations")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	connection := asset.CloudConnection{ID: "connection", Provider: asset.ProviderAzure, Partition: "azure", Status: asset.ConnectionActive, CreatedAt: now, UpdatedAt: now}
	if err := repository.PutConnection(ctx, connection); err != nil {
		t.Fatal(err)
	}
	scope := asset.Scope{ID: "root", ConnectionID: connection.ID, Kind: asset.ScopeSubscription, NativeID: testSubscription, CreatedAt: now, UpdatedAt: now}
	if err := repository.PutScope(ctx, scope); err != nil {
		t.Fatal(err)
	}
	// Use the real product normalizers, then persist/reload their metadata.
	// Native inventory worker recovery is separately covered below.
	for _, value := range values {
		value.ScopeID, value.ResourceKindID = scope.ID, h.runtime.resourceKind(value.Identity.NativeType).ID
		value.FirstSeenAt, value.LastSeenAt = now, now
		if err := repository.PutAsset(ctx, value); err != nil {
			t.Fatal(err)
		}
	}
	previous := ""
	fallback := h.override
	for _, mode := range []string{"initial", "omitted", "tampered", "forbidden"} {
		// A fresh provider and repository ensure this is saved-state recovery.
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
		id := h.hub + "/providers/microsoft.insights/actiongroups/hub-alerts"
		if mode == "omitted" {
			h.lists["/subscriptions/"+testSubscription+"/providers/microsoft.insights/actiongroups"] = []any{}
		}
		if mode == "tampered" {
			stored, err := repository.GetAsset(ctx, root.ID)
			if err != nil {
				t.Fatal(err)
			}
			stored.Normalized[fleetHubProof] = "forged"
			if err := repository.PutAsset(ctx, stored); err != nil {
				t.Fatal(err)
			}
		}
		if mode == "forbidden" {
			h.override = func(req *http.Request) (*http.Response, bool) {
				if strings.EqualFold(req.URL.Path, id) {
					return jsonResponse(403, map[string]any{}, nil), true
				}
				return fallback(req)
			}
		}
		run := asset.ScanRun{ID: asset.ScanRunID(mode), ConnectionID: connection.ID, Status: asset.ScanReconciling, CompletionStatus: asset.ScanSucceeded, CreatedAt: now, StartedAt: &now}
		if err := repository.CreateScanRun(ctx, run); err != nil {
			t.Fatal(err)
		}
		if err := repository.PutScanShard(ctx, asset.ScanShard{ID: asset.ScanShardID(mode), ScanRunID: run.ID, Provider: asset.ProviderAzure, Source: fleetInventorySource, ScopeID: scope.ID, Status: asset.ShardSucceeded, Coverage: asset.Coverage{ScopeID: scope.ID, Complete: true, FreshAt: now}, CreatedAt: now, FinishedAt: &now}); err != nil {
			t.Fatal(err)
		}
		handler := governance.NewGraphHandler(repository, registry, fleetHubGraphContributors{r})
		err = handler.Handle(ctx, execution.Job{Type: execution.JobGraph, Payload: map[string]any{"scan_run_id": mode}})
		failed := mode == "tampered" || mode == "forbidden"
		if (err != nil) != failed {
			t.Fatal("Fleet Hub graph worker lost its failure boundary", mode, err)
		}
		storedRun, err := repository.GetScanRun(ctx, run.ID)
		if err != nil || (storedRun.Status == asset.ScanSucceeded) == failed {
			t.Fatal("invalid Hub graph finalized a successful scan", mode, storedRun, err)
		}
		if !failed {
			previous = mode
		}
		revision, err := repository.GetGraphRevision(ctx, scope.ID)
		if err != nil || revision != previous {
			t.Fatal("failed Hub observation replaced the accepted graph", mode, revision, err)
		}
		bindings, err := repository.ListLifecycleBindingsByConnection(ctx, connection.ID)
		if err != nil {
			t.Fatal(err)
		}
		seen, count := map[asset.AssetID]bool{}, 0
		for _, binding := range bindings {
			if seen[binding.ManagedAssetID] {
				t.Fatal("persisted Hub graph has multiple exclusive controllers", binding)
			}
			seen[binding.ManagedAssetID] = true
			if binding.EvidenceSource == fleetHubSource {
				count++
				if binding.ControllerAssetID != root.ID || binding.DirectCleanupAllowed {
					t.Fatal("persisted Hub acquired an independent cleanup", binding)
				}
			}
		}
		if count != 17 {
			t.Fatal("persisted Hub graph lost native descendants", count)
		}
		if mode == "tampered" {
			stored, _ := repository.GetAsset(ctx, root.ID)
			stored.Normalized = root.Normalized
			if err := repository.PutAsset(ctx, stored); err != nil {
				t.Fatal(err)
			}
		}
	}
}

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
