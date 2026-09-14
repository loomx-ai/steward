package gcp

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/app/cleanup"
	"github.com/loomx-ai/steward/internal/app/governance"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/core/graph"
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/internal/persistence"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func cloudNatFixture(name string) map[string]any {
	base := "https://www.googleapis.com/compute/v1/projects/sample-project/regions/us-central1/"
	return map[string]any{"name": name, "type": "PUBLIC", "natIpAllocateOption": "MANUAL_ONLY", "natIps": []any{base + "addresses/nat-ip"}, "drainNatIps": []any{base + "addresses/drain-ip"}, "sourceSubnetworkIpRangesToNat": "LIST_OF_SUBNETWORKS", "subnetworks": []any{map[string]any{"name": base + "subnetworks/subnet-a", "sourceIpRangesToNat": []any{"PRIMARY_IP_RANGE", "LIST_OF_SECONDARY_IP_RANGES"}, "secondaryIpRangeNames": []any{"pods"}}}, "sourceSubnetworkIpRangesToNat64": "LIST_OF_IPV6_SUBNETWORKS", "nat64Subnetworks": []any{map[string]any{"name": base + "subnetworks/ipv6"}}, "enableDynamicPortAllocation": true, "minPortsPerVm": 64, "maxPortsPerVm": 1024, "logConfig": map[string]any{"enable": true, "filter": "ERRORS_ONLY"}, "endpointTypes": []any{"ENDPOINT_TYPE_VM"}, "rules": []any{map[string]any{"ruleNumber": 100, "match": "destination.ip == '203.0.113.1'", "action": map[string]any{"sourceNatActiveIps": []any{base + "addresses/rule-ip"}}}}}
}

func cloudNatParent(path string) map[string]any {
	return map[string]any{"name": last(path), "id": map[string]string{"router-a": "1001", "router-b": "1002"}[last(path)], "selfLink": "https://www.googleapis.com" + path, "network": "https://www.googleapis.com/compute/v1/projects/sample-project/global/networks/network-a", "bgpPeers": []any{map[string]any{"name": "peer-a"}}, "md5AuthenticationKeys": []any{map[string]any{"name": "key", "key": "router-only-secret"}}}
}

func TestCloudNatNativeInventoryAndNetworkScope(t *testing.T) {
	for _, scope := range []string{"us-central1", "project", "global", "network"} {
		t.Run(scope, func(t *testing.T) {
			reads := 0
			r := protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
				if req.Method == "GET" && req.URL.Host == "cloudasset.googleapis.com" {
					return apiResponse(req, 200, `{"readTime":"2026-09-14T00:00:00Z"}`), nil
				}
				if req.Method != "GET" || req.URL.Host != "compute.googleapis.com" {
					t.Fatal("unexpected mutation or host", req.Method, req.URL)
				}
				if strings.HasSuffix(req.URL.Path, "/regions") {
					return apiResponse(req, 200, `{"items":[{"name":"us-central1"},{"name":"europe-west1"}]}`), nil
				}
				if strings.HasSuffix(req.URL.Path, "/routers") {
					name := "router-a"
					result := map[string]any{}
					if req.URL.Query().Get("pageToken") == "next" {
						name = "router-b"
					} else {
						result["nextPageToken"] = "next"
					}
					result["items"] = []any{cloudNatParent(req.URL.Path + "/" + name)}
					return dataformResponse(req, 200, result), nil
				}
				if strings.HasSuffix(req.URL.Path, "/router-a") || strings.HasSuffix(req.URL.Path, "/router-b") {
					reads++
					if len(req.URL.Query()) != 0 {
						t.Fatal("invented NAT list pagination/query", req.URL)
					}
					parent := cloudNatParent(req.URL.Path)
					public := cloudNatFixture("nat-a")
					private := cloudNatFixture("nat-b")
					private["type"] = "PRIVATE"
					delete(private, "natIps")
					delete(private, "drainNatIps")
					delete(private, "natIpAllocateOption")
					delete(private, "nat64Subnetworks")
					delete(private, "sourceSubnetworkIpRangesToNat64")
					private["rules"] = []any{map[string]any{"ruleNumber": 1, "match": "nexthop.hub == '//networkconnectivity.googleapis.com/projects/sample-project/locations/global/hubs/hub-a'", "action": map[string]any{"sourceNatActiveRanges": []any{"https://www.googleapis.com/compute/v1/projects/sample-project/regions/us-central1/subnetworks/private-nat"}}}}
					parent["nats"] = []any{public, private}
					return dataformResponse(req, 200, parent), nil
				}
				t.Fatal("unexpected endpoint", req.URL)
				return nil, nil
			})
			kind := r.resourceKind(cloudNatType)
			request := contracts.InventoryRequest{ConnectionID: "connection", Source: productInventorySource, ResourceKind: &kind, Scope: asset.Scope{Kind: asset.ScopeRegion, NativeID: "us-central1"}, Limit: 1}
			if scope == "project" {
				request.Scope = asset.Scope{Kind: asset.ScopeProject, NativeID: "sample-project"}
			}
			if scope == "global" {
				request.Scope = asset.Scope{Kind: asset.ScopeGlobal, NativeID: "sample-project/global"}
			}
			if scope == "network" {
				request.NetworkTarget = &asset.ScanTarget{Kind: asset.ScanTargetVPC, NativeID: "//compute.googleapis.com/projects/sample-project/global/networks/network-a"}
			}
			seen := map[string]bool{}
			for pages := 0; ; pages++ {
				if pages > 5 {
					t.Fatal("cursor did not terminate")
				}
				batch, err := r.List(t.Context(), request)
				if err != nil {
					t.Fatal(err)
				}
				for _, item := range batch.Items {
					if seen[item.NativeID] {
						t.Fatal("duplicate NAT identity", item.NativeID)
					}
					seen[item.NativeID] = true
					if !strings.Contains(item.NativeID, "/routers/") || !strings.Contains(item.NativeID, "/nats/") || item.Normalized[cloudNatRouterID] == nil || item.Actionable == nil || !*item.Actionable || item.Normalized[cloudNatReview] == nil {
						t.Fatal("invalid native component", item)
					}
					encoded, _ := json.Marshal(item)
					if strings.Contains(string(encoded), "router-only-secret") || item.Normalized["bgpPeers"] != nil {
						t.Fatal("parent-only configuration escaped into NAT")
					}
					for _, ref := range []string{strings.TrimSuffix(item.NativeID, "/nats/"+item.Name), "//compute.googleapis.com/projects/sample-project/global/networks/network-a", "//compute.googleapis.com/projects/sample-project/regions/us-central1/subnetworks/subnet-a"} {
						if !slices.Contains(item.NetworkReferences, ref) {
							t.Fatal("missing NAT dependency", ref, item.NetworkReferences)
						}
					}
					if item.Name == "nat-a" && !slices.Contains(item.NetworkReferences, "//compute.googleapis.com/projects/sample-project/regions/us-central1/addresses/rule-ip") {
						t.Fatal("rule IP dependency missing", item.NetworkReferences)
					}
					if item.Name == "nat-b" && !slices.Contains(item.NetworkReferences, "//networkconnectivity.googleapis.com/projects/sample-project/locations/global/hubs/hub-a") {
						t.Fatal("CEL Hub dependency missing", item.NetworkReferences)
					}
					value := asset.Asset{Identity: asset.Identity{NativeType: cloudNatType, NativeID: item.NativeID}, Normalized: item.Normalized}
					assertGCPPropertyQuery(t, r, []asset.Asset{value}, cloudNatType, "/nats/"+item.Name, `properties.enableDynamicPortAllocation = true AND properties.minPortsPerVm = 64`)
				}
				if batch.Complete {
					break
				}
				request.Cursor = batch.NextCursor
			}
			want := 4
			if scope == "project" {
				want = 8
			}
			if scope == "global" {
				want = 0
			}
			if len(seen) != want || reads != want/2 {
				t.Fatal("incorrect region/router fanout", seen, reads)
			}
		})
	}
}

func TestCloudNatInventoryFailuresAndNativeShape(t *testing.T) {
	for _, mode := range []string{"denied", "missing", "partial", "token", "parent-name", "parent-id", "parent-link", "nats-null", "nats-object", "nat-scalar", "duplicate", "name", "string", "boolean", "integer", "ips", "ip-scalar", "subnet", "subnet-name", "log", "rule", "rule-number", "rule-duplicate", "action", "ranges"} {
		t.Run(mode, func(t *testing.T) {
			r := protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
				if req.Method != "GET" {
					t.Fatal(req.Method)
				}
				if strings.HasSuffix(req.URL.Path, "/routers") {
					return dataformResponse(req, 200, map[string]any{"items": []any{cloudNatParent(req.URL.Path + "/router-a")}}), nil
				}
				if !strings.HasSuffix(req.URL.Path, "/router-a") {
					t.Fatal(req.URL)
				}
				if mode == "denied" {
					return apiResponse(req, 403, `{}`), nil
				}
				if mode == "missing" {
					return apiResponse(req, 404, `{}`), nil
				}
				parent := cloudNatParent(req.URL.Path)
				nat := cloudNatFixture("nat-a")
				parent["nats"] = []any{nat}
				switch mode {
				case "partial":
					parent["warning"] = map[string]any{"code": "PARTIAL_SUCCESS"}
				case "token":
					parent["nextPageToken"] = 123
				case "parent-name":
					parent["name"] = "other"
				case "parent-id":
					parent["id"] = "2000"
				case "parent-link":
					parent["selfLink"] = "https://www.googleapis.com/compute/v1/projects/foreign/regions/us-central1/routers/router-a"
				case "nats-null":
					parent["nats"] = nil
				case "nats-object":
					parent["nats"] = map[string]any{}
				case "nat-scalar":
					parent["nats"] = []any{"nat-a"}
				case "duplicate":
					parent["nats"] = []any{nat, nat}
				case "name":
					nat["name"] = "../foreign"
				case "string":
					nat["type"] = 42
				case "boolean":
					nat["enableDynamicPortAllocation"] = "true"
				case "integer":
					nat["minPortsPerVm"] = 1.5
				case "ips":
					nat["natIps"] = nil
				case "ip-scalar":
					nat["natIps"] = []any{42}
				case "subnet":
					nat["subnetworks"] = []any{"subnet-a"}
				case "subnet-name":
					nat["subnetworks"] = []any{map[string]any{}}
				case "log":
					nat["logConfig"] = map[string]any{"enable": "true"}
				case "rule":
					nat["rules"] = []any{"bad"}
				case "rule-number":
					object(array(nat["rules"])[0])["ruleNumber"] = 65001
				case "rule-duplicate":
					nat["rules"] = append(array(nat["rules"]), array(nat["rules"])[0])
				case "action":
					object(array(nat["rules"])[0])["action"] = false
				case "ranges":
					object(object(array(nat["rules"])[0])["action"])["sourceNatActiveRanges"] = []any{42}
				}
				return dataformResponse(req, 200, parent), nil
			})
			if batch, err := r.List(t.Context(), productRequest(r, cloudNatType, "us-central1")); err == nil || len(batch.Items) != 0 {
				t.Fatal("bad native NAT source accepted", batch, err)
			}
		})
	}
}

func TestCloudNatFixturesMatchNativeSchemaAndIdentity(t *testing.T) {
	raw, err := os.ReadFile("catalog/source/discovery.json")
	if err != nil {
		t.Fatal(err)
	}
	var source map[string]any
	if json.Unmarshal(raw, &source) != nil {
		t.Fatal("source")
	}
	schemas := object(object(object(array(source["documents"])[0])["document"])["schemas"])
	compiler := discoveryFixtureSchemaCompiler(t, schemas)
	schema, err := compiler.Compile("https://fixture.test/infra-manager.json#/definitions/RouterNat")
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"PUBLIC", "PRIVATE"} {
		nat := cloudNatFixture("nat-a")
		nat["type"] = kind
		if err := schema.Validate(roundTripDataformJSON(t, nat)); err != nil {
			t.Fatal(err)
		}
	}
	nat := cloudNatFixture("nat-a")
	nat["type"] = "FUTURE_NATIVE_TYPE"
	nat["futureField"] = map[string]any{"value": "preserved"}
	if err := cloudNatData(nat, "nat-a"); err != nil {
		t.Fatal(err)
	}
	c := &client{project: "sample-project", number: "123456"}
	for _, id := range []string{"//compute.googleapis.com/projects/foreign/regions/us-central1/routers/router-a/nats/nat-a", "//compute.googleapis.com/projects/sample-project/regions/us-central1/routers/router-a/namedSets/nat-a", "//compute.googleapis.com/projects/sample-project/regions/us-central1/routers/router-a/nats/../other"} {
		if _, _, err := c.routerComponentOperation(cloudNatType, id, "GET"); err == nil {
			t.Fatal("foreign component", id)
		}
	}
	for _, project := range []string{"sample-project", "123456"} {
		id := "//compute.googleapis.com/projects/" + project + "/regions/us-central1/routers/router-a/nats/nat-a"
		op, params, err := c.routerComponentOperation(cloudNatType, id, "GET")
		if err != nil || op.ID != "compute.routers.get" || len(params) != 3 {
			t.Fatal(op, params, err)
		}
		if op, _, err := c.routerComponentOperation(cloudNatType, id, "DELETE"); err != nil || op.ID != "compute.routers.patch" {
			t.Fatal("NAT removal must use native array PATCH", op, err)
		}
	}
}

func TestCloudNatSQLiteFailureAbsenceAndRecovery(t *testing.T) {
	testRouterComponentSQLiteRecovery(t, cloudNatType)
}

func assertCloudNatSQLiteParentGraph(t *testing.T, repositories persistence.Repositories, r *Runtime, value asset.Asset) {
	t.Helper()
	ctx := t.Context()
	parentID := strings.TrimSuffix(value.Identity.NativeID, "/nats/"+value.Name)
	nativePath := "/compute/v1/" + strings.TrimPrefix(parentID, "//compute.googleapis.com/")
	parentKind := r.resourceKind(routerType)
	parent := asset.Asset{ID: "nat-parent", ScopeID: value.ScopeID, ResourceKindID: parentKind.ID, Identity: value.Identity, Name: "router-a", Normalized: cloudNatParent(nativePath), FirstSeenAt: value.FirstSeenAt, LastSeenAt: value.LastSeenAt}
	parent.Identity.NativeType = routerType
	parent.Identity.NativeID = parentID
	if err := repositories.Inventory().PutResourceKind(ctx, parentKind); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Inventory().PutAsset(ctx, parent); err != nil {
		t.Fatal(err)
	}
	hubKind := r.resourceKind(cloudNatHubType)
	hub := asset.Asset{ID: "nat-hub", ScopeID: "hub-global", ResourceKindID: hubKind.ID, Identity: value.Identity, Name: "hub-a", Normalized: map[string]any{"name": "projects/sample-project/locations/global/hubs/hub-a"}, Capabilities: value.Capabilities, FirstSeenAt: value.FirstSeenAt, LastSeenAt: value.LastSeenAt}
	hub.Identity.NativeType, hub.Identity.NativeID = cloudNatHubType, natHubA
	hub.Identity.ScopeKey = "global:sample-project/global"
	for _, write := range []func() error{
		func() error {
			return repositories.Inventory().PutScope(ctx, asset.Scope{ID: hub.ScopeID, ParentID: "project", ConnectionID: value.Identity.ConnectionID, Kind: asset.ScopeGlobal, NativeID: "sample-project/global", CreatedAt: value.FirstSeenAt, UpdatedAt: value.LastSeenAt})
		},
		func() error { return repositories.Inventory().PutResourceKind(ctx, hubKind) },
		func() error { return repositories.Inventory().PutAsset(ctx, hub) },
	} {
		if err := write(); err != nil {
			t.Fatal(err)
		}
	}
	if err := governance.NewGraphHandler(repositories, identityRegistry(t, r), cloudNatHubContributors{}).Handle(ctx, execution.Job{Type: execution.JobGraph, Payload: map[string]any{"scan_run_id": "first"}}); err != nil {
		t.Fatal(err)
	}
	relations, err := repositories.Graph().ListRelationshipsForAsset(ctx, value.Identity.ConnectionID, value.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range []asset.AssetID{parent.ID, hub.ID} {
		found := false
		for _, relation := range relations {
			if relation.SourceAssetID == value.ID && relation.TargetAssetID == target && relation.Type == graph.RelationshipDependsOn {
				found = true
			}
		}
		if !found {
			t.Fatal("native NAT dependency was not persisted", target, relations)
		}
	}
	planner := cleanup.NewService(repositories, identityRegistry(t, r))
	for _, selection := range [][]asset.AssetID{{hub.ID}, {value.ID}, {value.ID, hub.ID}} {
		selectors := []plan.CleanupSelector{}
		for _, id := range selection {
			selectors = append(selectors, plan.CleanupSelector{Kind: plan.SelectorAsset, AssetID: id})
		}
		task, err := planner.CreateTask(ctx, cleanup.CreateTaskRequest{ConnectionID: value.Identity.ConnectionID, Selectors: selectors, CreatedBy: "test"})
		if err != nil {
			t.Fatal(err)
		}
		if len(selection) == 1 && selection[0] == hub.ID {
			blocked := false
			for _, blocker := range task.Task.Blockers {
				if blocker.Code == plan.BlockCrossScopeDependency && blocker.AssetID == hub.ID {
					blocked = true
				}
			}
			if !blocked {
				t.Fatal("retained NAT did not block Hub deletion", task)
			}
			continue
		}
		if len(task.Steps) != len(selection) || task.Steps[0].AssetID != value.ID {
			t.Fatal("NAT plan changed its dependency scope", task.Steps)
		}
		if len(selection) == 2 && (task.Steps[1].AssetID != hub.ID || !slices.Contains(task.Steps[1].DependsOn, task.Steps[0].ID)) {
			t.Fatal("Hub can run before referring NAT", task.Steps)
		}
	}

}

func TestCloudNatCursorBindsParentIncarnations(t *testing.T) {
	changed := false
	r := protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
		if req.Method != "GET" {
			t.Fatal(req.Method)
		}
		if strings.HasSuffix(req.URL.Path, "/routers") {
			a, b := cloudNatParent(req.URL.Path+"/router-a"), cloudNatParent(req.URL.Path+"/router-b")
			if changed {
				b["id"] = "3000"
			}
			return dataformResponse(req, 200, map[string]any{"items": []any{a, b}}), nil
		}
		if !strings.HasSuffix(req.URL.Path, "/router-a") {
			t.Fatal("stale cursor reached another parent", req.URL)
		}
		parent := cloudNatParent(req.URL.Path)
		parent["nats"] = []any{cloudNatFixture("nat-a")}
		return dataformResponse(req, 200, parent), nil
	})
	request := productRequest(r, cloudNatType, "us-central1")
	batch, err := r.List(t.Context(), request)
	if err != nil || batch.Complete || batch.NextCursor == "" {
		t.Fatal(batch, err)
	}
	changed = true
	request.Cursor = batch.NextCursor
	if _, err := r.List(t.Context(), request); err == nil {
		t.Fatal("changed parent accepted old NAT cursor")
	}
}

type cloudNatHubContributors struct{}

func (cloudNatHubContributors) ResolveContributors(_ context.Context, _ asset.CloudConnection, _ []asset.Asset) ([]governance.Contributor, error) {
	return []governance.Contributor{NewCloudNatHubs()}, nil
}
