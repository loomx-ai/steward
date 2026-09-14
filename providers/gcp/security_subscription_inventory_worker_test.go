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
	"github.com/loomx-ai/steward/internal/core/resourcequery"
	"github.com/loomx-ai/steward/internal/persistence"
	"github.com/loomx-ai/steward/internal/persistence/sqlite"
	providerruntime "github.com/loomx-ai/steward/internal/provider/runtime"
)

func TestSecuritySubscriptionCreatorWorkerPreservesEarlierObservation(t *testing.T) {
	ctx := context.Background()
	data := securitySubscriptionData()
	s, r := subscriptionScenario(t, data)
	native := s.hook
	repositories, err := sqlite.Open(filepath.Join(t.TempDir(), "security-subscription.db"), filepath.Join("..", "..", "migrations"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 9, 0, 0, 0, 0, time.UTC)
	connection := asset.CloudConnection{ID: "connection", Provider: asset.ProviderGCP, Partition: "gcp", Status: asset.ConnectionActive, CreatedAt: now, UpdatedAt: now}
	root := asset.Scope{ID: "project", ConnectionID: connection.ID, Kind: asset.ScopeProject, NativeID: "sample-project", Name: "Project", CreatedAt: now, UpdatedAt: now}
	if err := repositories.Connections().PutConnection(ctx, connection); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Inventory().PutScope(ctx, root); err != nil {
		t.Fatal(err)
	}
	region := asset.ConnectionRegion{ID: "region", ConnectionID: connection.ID, RegionID: "us-central1", DiscoveredName: "US Central", Origin: asset.RegionOriginAPI, Lifecycle: asset.RegionActive, CreatedAt: now, UpdatedAt: now}
	if err := repositories.Regions().PutRegion(ctx, region); err != nil {
		t.Fatal(err)
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
	for _, run := range []string{"initial", "denied", "missing", "changed", "no_organization"} {
		if run == "denied" || run == "missing" || run == "changed" {
			s.hook = func(req *http.Request) (*http.Response, bool) {
				if req.URL.Host == securitySubscriptionHost {
					status := 403
					if run == "missing" {
						status = 404
					}
					if run == "changed" {
						return dataformResponse(req, 200, map[string]any{"name": "organizations/999/subscription"}), true
					}
					return dataformResponse(req, status, map[string]any{"error": map[string]any{"code": status, "message": "PRIVATE_ANCESTOR_FAILURE"}}), true
				}
				return native(req)
			}
		}
		if run == "no_organization" {
			s.hook = native
			delete(s.project, "parent")
		}
		created, err := creator.Create(ctx, inventory.ScanCreationRequest{ConnectionID: connection.ID, RequestedBy: "test", RegionMode: inventory.RegionModeAllActive, ResourceKindIDs: []asset.ResourceKindID{r.resourceKind(securitySubscriptionType).ID}})
		if err != nil || len(created.Shards) != 1 || len(created.Jobs) != 1 {
			t.Fatalf("organization source routing: %+v %v", created, err)
		}
		shard := created.Shards[0]
		if shard.Source != organizationInventorySource || shard.Authoritative {
			t.Fatalf("organization observation authority: %+v", shard)
		}
		failed := run != "initial" && run != "no_organization"
		err = handler.Handle(ctx, created.Jobs[0])
		if failed && err == nil || !failed && err != nil {
			t.Fatalf("organization scan %s: %v", run, err)
		}
		finished, err := repositories.Inventory().GetScanShard(ctx, shard.ID)
		if err != nil || finished.Coverage.Complete == failed {
			t.Fatalf("organization coverage: %+v %v", finished, err)
		}
		page, err := repositories.Inventory().ListAssets(ctx, persistence.ListOptions{Limit: 10})
		if err != nil || len(page.Items) != 1 || page.Items[0].ClosedAt != nil || page.Items[0].DeletedAt != nil || page.Items[0].Capabilities.Has(asset.CapabilityActionable) {
			t.Fatalf("old organization observation lost: %+v %v", page, err)
		}
		if page.Items[0].Normalized["tier"] != "PREMIUM" || page.Items[0].Normalized["subscriptionType"] != "TRIAL" || page.Items[0].Normalized["endTime"] != "2026-10-01T00:00:00Z" {
			t.Fatal("lost complete subscription", page)
		}
		expression, err := resourcequery.Parse(`properties.tier = "PREMIUM" AND properties.subscriptionType = "TRIAL" AND properties.endTime = "2026-10-01T00:00:00Z"`)
		if err != nil {
			t.Fatal(err)
		}
		if err := expression.Validate([]asset.ResourceKind{r.resourceKind(securitySubscriptionType)}); err != nil {
			t.Fatal(err)
		}
		matches, err := repositories.Inventory().ListAssets(ctx, persistence.ListOptions{Limit: 10, ResourceQuery: expression})
		if err != nil || len(matches.Items) != 1 {
			t.Fatal("subscription no longer searchable", matches, err)
		}
		encoded, _ := json.Marshal(page)
		if strings.Contains(string(encoded), "PRIVATE_ANCESTOR_FAILURE") {
			t.Fatal("native permission error escaped redaction")
		}
		now = now.Add(time.Minute)
	}
}
