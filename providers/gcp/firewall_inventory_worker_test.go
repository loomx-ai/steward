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
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/internal/persistence"
	"github.com/loomx-ai/steward/internal/persistence/sqlite"
	providerruntime "github.com/loomx-ai/steward/internal/provider/runtime"
)

// Use the actual provider registry, Creator, worker and SQLite projection. This
// catches differences hidden by direct List calls (source routing, root scope,
// connection partition, normalized proofs and authoritative reconciliation).
func TestFirewallCreatorWorkerPreserveHierarchyObservations(t *testing.T) {
	ctx := context.Background()
	s := newFirewallScenario()
	r := s.runtime(t)
	credential, err := r.credentials.Resolve(ctx, "connection")
	if err != nil {
		t.Fatal(err)
	}
	identity, err := r.ValidateConnection(ctx, credential)
	if err != nil {
		t.Fatal(err)
	}
	repositories, err := sqlite.Open(filepath.Join(t.TempDir(), "firewall.db"), filepath.Join("..", "..", "migrations"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 9, 0, 0, 0, 0, time.UTC)
	connection := asset.CloudConnection{ID: "connection", Name: "Firewall", Provider: asset.ProviderGCP, Partition: identity.Partition, Principal: identity.Principal, Status: asset.ConnectionActive, CreatedAt: now, UpdatedAt: now}
	candidate := identity.RootScopes[0]
	root := asset.Scope{ID: "firewall-project", ConnectionID: connection.ID, Kind: candidate.Kind, NativeID: candidate.NativeID, Name: candidate.Name, CreatedAt: now, UpdatedAt: now}
	region := asset.ConnectionRegion{ID: "firewall-region", ConnectionID: connection.ID, RegionID: "us-central1", DiscoveredName: "US Central", Origin: asset.RegionOriginAPI, Lifecycle: asset.RegionActive, CreatedAt: now, UpdatedAt: now}
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
	service := inventory.NewService(repositories.Inventory(), inventory.WithClock(func() time.Time { return now }))
	handler := inventory.NewScanHandler(repositories, registry, service)
	kinds := []asset.ResourceKindID{}
	for _, kind := range []string{firewallPolicyType, firewallAssociationType, networkFirewallPolicyType, networkFirewallAssociationType} {
		kinds = append(kinds, r.resourceKind(kind).ID)
	}
	for _, run := range []string{"initial", "denied", "scope_removed"} {
		if run == "denied" {
			s.hook = func(req *http.Request) (*http.Response, bool) {
				if req.URL.Path == "/compute/v1/locations/global/firewallPolicies" {
					return dataformResponse(req, 403, map[string]any{"error": map[string]any{"code": 403, "message": "FIREWALL_PRIVATE_PERMISSION_ERROR"}}), true
				}
				return nil, false
			}
		}
		if run == "scope_removed" {
			s.hook = nil
			s.root = ""
		}
		created, err := creator.Create(ctx, inventory.ScanCreationRequest{ConnectionID: connection.ID, RequestedBy: "test", RegionMode: inventory.RegionModeAllActive, ResourceKindIDs: kinds})
		if err != nil || len(created.Shards) != 6 {
			t.Fatalf("native source/shard generation: %+v %v", created, err)
		}
		failures := 0
		for _, job := range created.Jobs {
			if err := handler.Handle(ctx, job); err != nil {
				failures++
			}
		}
		expected := 0
		if run == "denied" {
			expected = 1
		}
		if failures != expected {
			t.Fatalf("scan %s failed %d target jobs, expected %d", run, failures, expected)
		}
		for _, shard := range created.Shards {
			scope, err := repositories.Inventory().GetScope(ctx, shard.ScopeID)
			if err != nil {
				t.Fatal(err)
			}
			if scope.ParentID != root.ID {
				t.Fatalf("invented organization root: %+v", scope)
			}
			completed, err := repositories.Inventory().GetScanShard(ctx, shard.ID)
			if err != nil {
				t.Fatal(err)
			}
			if shard.Source == firewallInventorySource {
				if scope.Kind != asset.ScopeGlobal || shard.Authoritative {
					t.Fatalf("hierarchical source scope/authority: %+v %+v", shard, scope)
				}
				if run == "denied" {
					if completed.Status != asset.ShardFailed || completed.Coverage.Complete {
						t.Fatal("permission failure established absence")
					}
				} else if completed.Status != asset.ShardSucceeded || !completed.Coverage.Complete {
					t.Fatalf("hierarchical scan incomplete: %+v", completed)
				}
			} else if !shard.Authoritative || shard.Source != productInventorySource {
				t.Fatalf("network policies lost native authoritative source: %+v", shard)
			}
		}
		page, err := repositories.Inventory().ListAssets(ctx, persistence.ListOptions{Limit: 20})
		if err != nil || len(page.Items) != 9 {
			t.Fatalf("firewall projection %s: %d %v", run, len(page.Items), err)
		}
		for _, value := range page.Items {
			if value.ClosedAt != nil || value.DeletedAt != nil || value.Identity.Partition != identity.Partition || text(value.Normalized[firewallSnapshotKey]) == "" {
				t.Fatalf("scope change or denied scan lost asset: %+v", value)
			}
			if firewallParentType(value.Identity.NativeType) == firewallPolicyType {
				if _, present := value.Normalized["project_id"]; present {
					t.Fatal("organization policy was assigned project ownership")
				}
			}
			if run == "initial" {
				c, err := r.resolve(ctx, "connection")
				if err != nil {
					t.Fatal(err)
				}
				if _, err := c.firewallSaved(value); err != nil {
					t.Fatalf("SQLite projection invalidated native proof: %v", err)
				}
			}
		}

		if run == "initial" {
			contributor, err := r.ServiceLifecycle(ctx, connection.ID)
			if err != nil {
				t.Fatal(err)
			}
			contribution, err := contributor.Contribute(ctx, root.ID, page.Items)
			if err != nil || len(contribution.Unresolved) != 0 {
				t.Fatalf("projected governance: %+v %v", contribution, err)
			}
			selected := batchAsset(page.Items, firewallTestHierarchy)
			solved, err := plan.Solve(plan.Input{Assets: page.Items, ResolvedAssetIDs: []asset.AssetID{selected.ID}, Relationships: contribution.Relationships, LifecycleBindings: contribution.Bindings})
			if err != nil || len(solved.Blockers) != 0 || len(solved.Steps) != 3 {
				t.Fatalf("projected plan: %+v %v", solved, err)
			}
			parentRequest := dataformRequest(t, solved, page.Items, selected)
			childRequest := dataformRequest(t, solved, page.Items, parentRequest.PrerequisiteDeletions[0].Asset)
			driver, err := r.ResolveAction(ctx, connection.ID, childRequest.Asset)
			if err != nil {
				t.Fatal(err)
			}
			if check, err := driver.Preflight(ctx, childRequest); err != nil || !check.Allowed || check.Absent {
				t.Fatalf("projected child not executable: %+v %v", check, err)
			}
		}
		encoded, _ := json.Marshal(page)
		if strings.Contains(string(encoded), "FIREWALL_PRIVATE_") {
			t.Fatal("native permission error escaped redaction")
		}
		now = now.Add(time.Minute)
	}
}
