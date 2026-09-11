package azure

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/loomx-ai/steward/internal/app/governance"
	"github.com/loomx-ai/steward/internal/app/inventory"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/core/graph"
	"github.com/loomx-ai/steward/internal/persistence/sqlite"
	providerruntime "github.com/loomx-ai/steward/internal/provider/runtime"
)

func TestFleetRegisteredScanWorkerGraphAndKnownAbsence(t *testing.T) {
	ctx := t.Context()
	f := newFleetFixture(t)
	r := f.runtime
	repository, err := sqlite.Open(filepath.Join(t.TempDir(), "fleet.db"), "../../migrations")
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
	for _, region := range []string{"westus", "eastus"} {
		if err := repository.PutRegion(ctx, asset.ConnectionRegion{ID: region, ConnectionID: connection.ID, RegionID: region, Origin: asset.RegionOriginAPI, Lifecycle: asset.RegionActive, CreatedAt: now, UpdatedAt: now}); err != nil {
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
	creator, err := inventory.NewCreator(repository, registry)
	if err != nil {
		t.Fatal(err)
	}
	handler := inventory.NewScanHandler(repository, registry, inventory.NewService(repository))
	scan := func(kinds []string, failure bool) []asset.Asset {
		t.Helper()
		var ids []asset.ResourceKindID
		for _, kind := range kinds {
			ids = append(ids, r.resourceKind(kind).ID)
		}
		created, err := creator.Create(ctx, inventory.ScanCreationRequest{ConnectionID: connection.ID, RequestedBy: "fleet-native-worker", RegionMode: inventory.RegionModeSelected, RegionIDs: []string{"westus", "eastus"}, ResourceKindIDs: ids})
		if err != nil || len(created.Shards) != 2*len(kinds) {
			t.Fatal("Fleet source was not registered for both regions", created, err)
		}
		for _, shard := range created.Shards {
			if shard.Source != fleetInventorySource || shard.Authoritative {
				t.Fatal("Fleet source gained implicit absence authority", shard)
			}
			shard.Authoritative = true // A saved pre-migration flag cannot widen the native source.
			if err := repository.PutScanShard(ctx, shard); err != nil {
				t.Fatal(err)
			}
		}
		for _, job := range created.Jobs {
			if err := handler.Handle(ctx, job); err != nil && !failure {
				t.Fatal("Fleet scan worker failed", err)
			}
		}
		for _, shard := range created.Shards {
			stored, err := repository.GetScanShard(ctx, shard.ID)
			if err != nil || stored.Authoritative || stored.Coverage.Authoritative || !failure && stored.Status != asset.ShardSucceeded || failure && stored.Status == asset.ShardSucceeded {
				t.Fatal("Fleet worker failed source/scan boundaries", stored, err)
			}
		}
		if !failure {
			jobs, err := repository.ListJobsByAggregate(ctx, "scan_task", string(created.ScanRun.ID))
			if err != nil {
				t.Fatal(err)
			}
			for _, job := range jobs {
				if job.Type == execution.JobGraph {
					if err := governance.NewGraphHandler(repository, registry, nil).Handle(ctx, job); err != nil {
						t.Fatal("Fleet spec graph worker failed", err)
					}
				}
			}
		}
		values, err := repository.ListActiveAssetsByConnection(ctx, connection.ID, "")
		if err != nil {
			t.Fatal(err)
		}
		return values
	}
	values := scan(fleetTestKinds, false)
	if len(values) != 7 {
		t.Fatal("Fleet worker lost a registered native kind", len(values))
	}
	byKind := map[string]asset.Asset{}
	for _, value := range values {
		byKind[value.Identity.NativeType] = value
	}
	if byKind[fleetNamespaceType].Location != "eastus" || byKind[fleetGateType].Location != "westus" {
		t.Fatal("Fleet scopes were flattened")
	}
	relationships, err := repository.ListRelationshipsByConnection(ctx, connection.ID)
	if err != nil {
		t.Fatal(err)
	}
	gateRun, profileStrategy := false, false
	for _, relation := range relationships {
		if relation.Type != graph.RelationshipUses {
			t.Fatal("Fleet inventory invented ownership", relation)
		}
		if relation.SourceAssetID == byKind[fleetGateType].ID && relation.TargetAssetID == byKind[fleetRunType].ID {
			gateRun = true
		}
		if relation.SourceAssetID == byKind[fleetProfileType].ID && relation.TargetAssetID == byKind[fleetStrategyType].ID {
			profileStrategy = true
		}
		if relation.SourceAssetID == byKind[fleetRunType].ID && relation.TargetAssetID == byKind[fleetStrategyType].ID {
			t.Fatal("copied strategy became a live dependency")
		}
	}
	if !gateRun || !profileStrategy {
		t.Fatal("Fleet native relationships missing", relationships)
	}
	payload, _ := json.Marshal(values)
	if strings.Contains(string(payload), "fleet-private-configuration") {
		t.Fatal("Fleet private data entered SQLite")
	}
	parent, member := byKind[fleetType].Identity.NativeID, byKind[fleetMemberType].Identity.NativeID
	f.omitted[member], f.omitted[parent] = true, true
	if len(scan([]string{fleetMemberType}, false)) != 7 {
		t.Fatal("LIST omission erased a known live child")
	}
	f.override = func(req *http.Request) (*http.Response, bool) {
		if strings.EqualFold(req.URL.Path, member) {
			return jsonResponse(403, map[string]any{"error": map[string]any{"code": "AuthorizationFailed"}}, nil), true
		}
		return nil, false
	}
	if len(scan([]string{fleetMemberType}, true)) != 7 {
		t.Fatal("failed source erased known assets")
	}
	f.override = nil
	delete(f.resources, parent)
	if len(scan([]string{fleetMemberType}, true)) != 7 {
		t.Fatal("parent absence closed a surviving member")
	}
	delete(f.resources, member)
	values = scan([]string{fleetMemberType}, false)
	if len(values) != 6 {
		t.Fatal("own native absence did not reconcile the member", len(values))
	}
	for _, value := range values {
		if value.Identity.NativeID == member {
			t.Fatal("absent member stayed active")
		}
	}
}
