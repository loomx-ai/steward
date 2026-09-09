package gcp

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/loomx-ai/steward/internal/app/inventory"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/persistence"
	"github.com/loomx-ai/steward/internal/persistence/sqlite"
	"github.com/loomx-ai/steward/internal/provider/contracts"
	providerruntime "github.com/loomx-ai/steward/internal/provider/runtime"
)

type organizationScenario struct {
	project, folder, organization map[string]any
	calls                         []string
	hook                          func(*http.Request) (*http.Response, bool)
}

func newOrganizationScenario() *organizationScenario {
	return &organizationScenario{
		project:      map[string]any{"name": "projects/123456", "projectId": "sample-project", "state": "ACTIVE", "parent": "folders/456", "createTime": "2025-01-01T00:00:00Z"},
		folder:       map[string]any{"name": "folders/456", "parent": "organizations/123", "displayName": "Platform", "state": "ACTIVE", "createTime": "2024-01-01T00:00:00Z"},
		organization: map[string]any{"name": "organizations/123", "displayName": "example.test", "directoryCustomerId": "C01234567", "state": "ACTIVE", "createTime": "2023-01-01T00:00:00Z", "updateTime": "2025-01-01T00:00:00Z", "etag": "native-etag"},
	}
}

func (s *organizationScenario) transport(t *testing.T, request *http.Request) (*http.Response, error) {
	t.Helper()
	if request.Method != "GET" || request.Header.Get("Authorization") != "Bearer token" || request.URL.RawQuery != "" {
		t.Fatalf("unexpected organization request: %s %s", request.Method, request.URL)
	}
	s.calls = append(s.calls, request.URL.Path)
	if s.hook != nil {
		if response, handled := s.hook(request); handled {
			return response, nil
		}
	}
	if request.URL.Host != resourceManagerHost {
		t.Fatalf("unexpected organization host: %s", request.URL)
	}
	var data map[string]any
	switch request.URL.Path {
	case "/v3/projects/sample-project":
		data = s.project
	case "/v3/folders/456":
		data = s.folder
	case "/v3/organizations/123":
		data = s.organization
	default:
		t.Fatalf("out-of-scope organization request: %s", request.URL)
	}
	return dataformResponse(request, 200, data), nil
}

func (s *organizationScenario) runtime(t *testing.T) *Runtime {
	r := protocolRuntime(t, func(req *http.Request) (*http.Response, error) { return s.transport(t, req) })
	base := r.transport
	r.transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Host == resourceManagerHost && req.URL.Path == "/v3/projects/sample-project" {
			return s.transport(t, req)
		}
		return base.RoundTrip(req)
	})
	return r
}

func organizationRequest(r *Runtime) contracts.InventoryRequest {
	kind := r.resourceKind(organizationType)
	return contracts.InventoryRequest{ConnectionID: "connection", Source: organizationInventorySource, Scope: asset.Scope{Kind: asset.ScopeGlobal, NativeID: "sample-project/global"}, ResourceKind: &kind}
}

func TestOrganizationFixturesMatchNativeSchemas(t *testing.T) {
	compiler := infraFixtureSchemas(t, "fixtures/organization/native-schemas.json", "20260820", "e46acd311b9d23a7ddac98ab85e2d9edaec3df9105f66679821c055c8e1c6131")
	s := newOrganizationScenario()
	for name, data := range map[string]map[string]any{"Project": s.project, "Folder": s.folder, "Organization": s.organization} {
		schema, err := compiler.Compile("https://fixture.test/infra-manager.json#/definitions/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if err := schema.Validate(data); err != nil {
			t.Fatalf("native %s: %v", name, err)
		}
		invalid := cloneParameters(data)
		invalid["name"] = map[string]any{"value": "invented"}
		if schema.Validate(invalid) == nil {
			t.Fatal("native schema accepted an invented name shape")
		}
	}
}

func TestOrganizationNativeAncestryInventoryAndReadOnlyBoundary(t *testing.T) {
	for _, variant := range []string{"nested", "direct", "unowned", "standalone", "deletion_requested"} {
		t.Run(variant, func(t *testing.T) {
			s := newOrganizationScenario()
			switch variant {
			case "direct":
				s.project["parent"] = "organizations/123"
			case "unowned":
				delete(s.project, "parent")
			case "standalone":
				delete(s.organization, "directoryCustomerId")
				s.organization["displayName"] = "Standalone"
			case "deletion_requested":
				s.organization["state"] = "DELETE_REQUESTED"
				s.organization["deleteTime"] = "2026-09-09T00:00:00Z"
			}
			r := s.runtime(t)
			batch, err := r.List(context.Background(), organizationRequest(r))
			if err != nil || !batch.Complete || batch.NextCursor != "" {
				t.Fatalf("ancestry inventory: %+v %v", batch, err)
			}
			if variant == "unowned" {
				if len(batch.Items) != 0 || slices.Contains(s.calls, "/v3/organizations/123") {
					t.Fatal("unowned project gained an organization")
				}
				return
			}
			if len(batch.Items) != 1 {
				t.Fatalf("organization count: %d", len(batch.Items))
			}
			item := batch.Items[0]
			if item.NativeID != "//cloudresourcemanager.googleapis.com/organizations/123" || item.Name != s.organization["displayName"] || item.State != s.organization["state"] || item.Actionable == nil || *item.Actionable || item.Scope.Kind != asset.ScopeGlobal {
				t.Fatalf("invalid native organization: %+v", item)
			}
			if _, present := item.Normalized["project_id"]; present || item.Normalized["_organization_project"] != "projects/123456" {
				t.Fatal("project connection was represented as organization ownership")
			}
			value := asset.Asset{ID: "org", Identity: asset.Identity{Provider: asset.ProviderGCP, ConnectionID: "connection", Partition: "gcp", NativeType: organizationType, NativeID: item.NativeID}, Normalized: item.Normalized}
			if _, err := r.ResolveAction(context.Background(), "connection", value); err == nil {
				t.Fatal("invented organization delete action")
			}
			result, err := r.Invoke(context.Background(), contracts.Invocation{ConnectionID: "connection", Operation: "cloudresourcemanager.organizations.get", Parameters: map[string]any{"name": "organizations/123"}})
			if err != nil || result.Data["name"] != "organizations/123" {
				t.Fatalf("native ancestor GET: %+v %v", result, err)
			}
			for _, name := range []string{"organizations/999", "organizations/0123", "organizations/123/../999", "folders/456"} {
				if _, err := r.Invoke(context.Background(), contracts.Invocation{ConnectionID: "connection", Operation: "cloudresourcemanager.organizations.get", Parameters: map[string]any{"name": name}}); err == nil {
					t.Fatalf("foreign organization accepted: %s", name)
				}
			}
			if _, err := r.Invoke(context.Background(), contracts.Invocation{ConnectionID: "connection", Operation: "compute.firewallPolicies.delete", Parameters: map[string]any{"firewallPolicy": "1001"}}); err == nil {
				t.Fatal("organization ancestry authorized a firewall mutation")
			}
		})
	}
}

func TestOrganizationIncompleteOrChangingAncestryFails(t *testing.T) {
	for name, mutate := range map[string]func(*contracts.InventoryRequest){
		"foreign global": func(r *contracts.InventoryRequest) { r.Scope.NativeID = "foreign-project/global" },
		"foreign project": func(r *contracts.InventoryRequest) {
			r.Scope.Kind, r.Scope.NativeID = asset.ScopeProject, "foreign-project"
		},
		"regional": func(r *contracts.InventoryRequest) {
			r.Scope.Kind, r.Scope.NativeID = asset.ScopeRegion, "us-central1"
		},
		"cursor":  func(r *contracts.InventoryRequest) { r.Cursor = "old-cursor" },
		"kind":    func(r *contracts.InventoryRequest) { r.ResourceKind = nil },
		"network": func(r *contracts.InventoryRequest) { r.NetworkTarget = &asset.ScanTarget{Kind: asset.ScanTargetVPC} },
		"source":  func(r *contracts.InventoryRequest) { r.Source = productInventorySource },
	} {
		t.Run("scope "+name, func(t *testing.T) {
			s := newOrganizationScenario()
			r := s.runtime(t)
			request := organizationRequest(r)
			mutate(&request)
			if _, err := r.List(context.Background(), request); err == nil {
				t.Fatal("invalid organization scan scope accepted")
			}
			if slices.Contains(s.calls, "/v3/organizations/123") {
				t.Fatal("invalid scan reached the organization")
			}
		})
	}
	cases := map[string]func(*organizationScenario){
		"project parent type":    func(s *organizationScenario) { s.project["parent"] = map[string]any{"name": "organizations/123"} },
		"project parent path":    func(s *organizationScenario) { s.project["parent"] = "projects/123" },
		"project creation":       func(s *organizationScenario) { delete(s.project, "createTime") },
		"folder cycle":           func(s *organizationScenario) { s.folder["parent"] = "folders/456" },
		"folder wrong identity":  func(s *organizationScenario) { s.folder["name"] = "folders/789" },
		"folder deleted":         func(s *organizationScenario) { s.folder["state"] = "DELETE_REQUESTED" },
		"folder creation":        func(s *organizationScenario) { s.folder["createTime"] = false },
		"organization identity":  func(s *organizationScenario) { s.organization["name"] = "organizations/999" },
		"organization state":     func(s *organizationScenario) { s.organization["state"] = "STATE_UNSPECIFIED" },
		"organization creation":  func(s *organizationScenario) { delete(s.organization, "createTime") },
		"organization customer":  func(s *organizationScenario) { s.organization["directoryCustomerId"] = []any{} },
		"organization timestamp": func(s *organizationScenario) { s.organization["deleteTime"] = "yesterday" },
	}
	for _, code := range []int{403, 404, 500} {
		cases[http.StatusText(code)] = func(s *organizationScenario) {
			s.hook = func(req *http.Request) (*http.Response, bool) {
				if req.URL.Path == "/v3/folders/456" {
					return dataformResponse(req, code, map[string]any{"error": map[string]any{"code": code, "message": "PRIVATE_ANCESTOR_FAILURE"}}), true
				}
				return nil, false
			}
		}
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			s := newOrganizationScenario()
			mutate(s)
			r := s.runtime(t)
			if _, err := r.List(context.Background(), organizationRequest(r)); err == nil {
				t.Fatal("invalid ancestry established coverage")
			}
		})
	}
	for _, changed := range []string{"project", "folder", "organization"} {
		t.Run("concurrent "+changed, func(t *testing.T) {
			s := newOrganizationScenario()
			reads := 0
			s.hook = func(req *http.Request) (*http.Response, bool) {
				if req.URL.Path == "/v3/organizations/123" {
					reads++
					if reads == 1 {
						response := dataformResponse(req, 200, s.organization)
						switch changed {
						case "project":
							s.project["parent"] = "organizations/123"
						case "folder":
							s.folder["createTime"] = "2026-01-01T00:00:00Z"
						case "organization":
							s.organization["etag"] = "changed"
						}
						return response, true
					}
				}
				return nil, false
			}
			r := s.runtime(t)
			if _, err := r.List(context.Background(), organizationRequest(r)); err == nil {
				t.Fatal("changed ancestry established coverage")
			}
		})
	}
}

func TestOrganizationCreatorWorkerPreservesEarlierAncestry(t *testing.T) {
	ctx := context.Background()
	s := newOrganizationScenario()
	r := s.runtime(t)
	repositories, err := sqlite.Open(filepath.Join(t.TempDir(), "organization.db"), filepath.Join("..", "..", "migrations"))
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
	for _, run := range []string{"initial", "denied", "no_organization"} {
		if run == "denied" {
			s.hook = func(req *http.Request) (*http.Response, bool) {
				if req.URL.Path == "/v3/organizations/123" {
					return dataformResponse(req, 403, map[string]any{"error": map[string]any{"code": 403, "message": "PRIVATE_ANCESTOR_FAILURE"}}), true
				}
				return nil, false
			}
		}
		if run == "no_organization" {
			s.hook = nil
			delete(s.project, "parent")
		}
		created, err := creator.Create(ctx, inventory.ScanCreationRequest{ConnectionID: connection.ID, RequestedBy: "test", RegionMode: inventory.RegionModeAllActive, ResourceKindIDs: []asset.ResourceKindID{r.resourceKind(organizationType).ID}})
		if err != nil || len(created.Shards) != 1 || len(created.Jobs) != 1 {
			t.Fatalf("organization source routing: %+v %v", created, err)
		}
		shard := created.Shards[0]
		if shard.Source != organizationInventorySource || shard.Authoritative {
			t.Fatalf("organization observation authority: %+v", shard)
		}
		err = handler.Handle(ctx, created.Jobs[0])
		if run == "denied" && err == nil || run != "denied" && err != nil {
			t.Fatalf("organization scan %s: %v", run, err)
		}
		finished, err := repositories.Inventory().GetScanShard(ctx, shard.ID)
		if err != nil || finished.Coverage.Complete == (run == "denied") {
			t.Fatalf("organization coverage: %+v %v", finished, err)
		}
		page, err := repositories.Inventory().ListAssets(ctx, persistence.ListOptions{Limit: 10})
		if err != nil || len(page.Items) != 1 || page.Items[0].ClosedAt != nil || page.Items[0].DeletedAt != nil || page.Items[0].Capabilities.Has(asset.CapabilityActionable) {
			t.Fatalf("old organization observation lost: %+v %v", page, err)
		}
		encoded, _ := json.Marshal(page)
		if strings.Contains(string(encoded), "PRIVATE_ANCESTOR_FAILURE") {
			t.Fatal("native permission error escaped redaction")
		}
		now = now.Add(time.Minute)
	}
}
