package gcp

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func routePolicyFixture(name string) map[string]any {
	return map[string]any{"name": name, "type": "ROUTE_POLICY_TYPE_IMPORT", "description": "Reviewed import policy", "fingerprint": "ZnAx", "terms": []any{map[string]any{"priority": 10, "match": map[string]any{"expression": "destination == '10.0.0.0/8'"}, "actions": []any{map[string]any{"expression": "accept()"}}}}}
}

func TestRoutePolicyNativeInventory(t *testing.T) {
	for _, scope := range []string{"us-central1", "project", "global"} {
		t.Run(scope, func(t *testing.T) {
			var reads []string
			r := protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
				if req.Method != "GET" {
					t.Fatal("inventory attempted mutation", req.URL)
				}
				if req.URL.Host == "cloudasset.googleapis.com" {
					return apiResponse(req, 200, `{"readTime":"2026-09-14T00:00:00Z"}`), nil
				}
				if req.URL.Host != "compute.googleapis.com" {
					t.Fatal("unexpected host", req.URL)
				}
				if req.URL.Path == "/compute/v1/projects/sample-project/regions" {
					return apiResponse(req, 200, `{"items":[{"name":"us-central1"},{"name":"europe-west1"}]}`), nil
				}
				parts := strings.Split(req.URL.Path, "/")
				region := parts[6]
				if strings.HasSuffix(req.URL.Path, "/routers") {
					name := "router-a"
					if req.URL.Query().Get("pageToken") == "router-next" {
						name = "router-b"
					}
					result := map[string]any{"items": []any{map[string]any{"name": name, "id": name, "selfLink": "https://www.googleapis.com" + req.URL.Path + "/" + name, "network": "https://www.googleapis.com/compute/v1/projects/sample-project/global/networks/network-a"}}}
					if name == "router-a" {
						result["nextPageToken"] = "router-next"
					}
					return dataformResponse(req, 200, result), nil
				}
				router := parts[8]
				if strings.HasSuffix(req.URL.Path, "/listRoutePolicies") {
					if req.URL.Query().Get("maxResults") != "1" || req.URL.Query().Get("returnPartialSuccess") != "" {
						t.Fatal("incorrect paging", req.URL)
					}
					name := "policy-a"
					result := map[string]any{}
					if router == "router-a" {
						if req.URL.Query().Get("pageToken") == "policy-next" {
							name = "policy-b"
						} else {
							result["nextPageToken"] = "policy-next"
						}
					}
					result["result"] = []any{map[string]any{"name": name}}
					return dataformResponse(req, 200, result), nil
				}
				if strings.HasSuffix(req.URL.Path, "/getRoutePolicy") {
					name := req.URL.Query().Get("policy")
					reads = append(reads, region+"/"+router+"/"+name)
					return dataformResponse(req, 200, map[string]any{"resource": routePolicyFixture(name)}), nil
				}
				t.Fatal("unexpected request", req.URL)
				return nil, nil
			})
			kind := r.resourceKind(routePolicyType)
			request := contracts.InventoryRequest{ConnectionID: "connection", Source: productInventorySource, ResourceKind: &kind, Scope: asset.Scope{Kind: asset.ScopeRegion, NativeID: scope}, Limit: 1}
			if scope == "project" {
				request.Scope = asset.Scope{Kind: asset.ScopeProject, NativeID: "sample-project"}
			}
			if scope == "global" {
				request.Scope = asset.Scope{Kind: asset.ScopeGlobal, NativeID: "sample-project/global"}
			}
			var ids []string
			for pages := 0; ; pages++ {
				if pages > 10 {
					t.Fatal("pagination did not terminate")
				}
				batch, err := r.List(t.Context(), request)
				if err != nil {
					t.Fatal(err)
				}
				for _, item := range batch.Items {
					ids = append(ids, item.NativeID)
					parent := strings.TrimSuffix(item.NativeID, "/routePolicies/"+item.Name)
					if !slices.Contains(item.NetworkReferences, parent) || len(array(item.Normalized["terms"])) != 1 || item.Normalized["fingerprint"] != "ZnAx" || item.Actionable == nil || *item.Actionable {
						t.Fatalf("lost policy metadata or parent: %+v", item)
					}
					term := object(array(item.Normalized["terms"])[0])
					if object(term["match"])["expression"] != "destination == '10.0.0.0/8'" || object(array(term["actions"])[0])["expression"] != "accept()" {
						t.Fatal("native policy expressions lost", term)
					}
					if item.Scope.NativeID != item.Location || !strings.Contains(item.NativeID, "/regions/"+item.Location+"/routers/") {
						t.Fatal("wrong region", item)
					}
				}
				if batch.Complete {
					break
				}
				if batch.NextCursor == "" || batch.NextCursor == request.Cursor {
					t.Fatal("invalid inventory cursor")
				}
				request.Cursor = batch.NextCursor
			}
			want := 3
			if scope == "project" {
				want = 6
			}
			if scope == "global" {
				want = 0
			}
			slices.Sort(ids)
			if len(ids) != want || len(reads) != want || len(slices.Compact(ids)) != want {
				t.Fatal("missing or duplicate policies", ids, reads)
			}
		})
	}
}

func TestRoutePolicyRejectsIncompleteOrChangedData(t *testing.T) {
	for _, mode := range []string{"parent-denied", "foreign-parent", "parent-name-mismatch", "parent-uid-changed", "list-denied", "list-shape", "partial", "unreachable", "name-path", "duplicate", "detail-denied", "detail-missing", "wrapper", "identity", "terms", "priority", "duplicate-priority", "actions", "expression", "cursor-cycle", "parent-changed"} {
		t.Run(mode, func(t *testing.T) {
			parentLists := 0
			r := protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
				if req.Method != "GET" || req.URL.Host != "compute.googleapis.com" {
					t.Fatal("unexpected native request", req.URL)
				}
				if strings.HasSuffix(req.URL.Path, "/routers") {
					parentLists++
					if mode == "parent-denied" {
						return apiResponse(req, 403, `{}`), nil
					}
					id := "router-a"
					if mode == "parent-changed" && parentLists > 1 {
						id = "router-b"
					}
					path := req.URL.Path + "/" + id
					if mode == "foreign-parent" {
						path = strings.Replace(path, "sample-project", "foreign-project", 1)
					}
					data := map[string]any{"name": id, "id": "1001", "selfLink": "https://www.googleapis.com" + path}
					if mode == "parent-name-mismatch" {
						data["name"] = "router-b"
					}
					if mode == "parent-uid-changed" && parentLists > 1 {
						data["id"] = "1002"
					}
					return dataformResponse(req, 200, map[string]any{"items": []any{data}}), nil
				}
				if strings.HasSuffix(req.URL.Path, "/listRoutePolicies") {
					if mode == "list-denied" {
						return apiResponse(req, 403, `{}`), nil
					}
					result := map[string]any{"result": []any{map[string]any{"name": "policy-a"}}}
					switch mode {
					case "list-shape":
						result["result"] = map[string]any{}
					case "partial":
						result["warning"] = map[string]any{"code": "PARTIAL_SUCCESS"}
					case "unreachable":
						result["unreachables"] = []any{"router-a"}
					case "name-path":
						result["result"] = []any{map[string]any{"name": "other/policy-a"}}
					case "duplicate":
						result["result"] = []any{map[string]any{"name": "policy-a"}, map[string]any{"name": "policy-a"}}
					case "cursor-cycle", "parent-changed", "parent-uid-changed":
						result["nextPageToken"] = "again"
					}
					return dataformResponse(req, 200, result), nil
				}
				if strings.HasSuffix(req.URL.Path, "/getRoutePolicy") {
					if mode == "detail-denied" {
						return apiResponse(req, 403, `{}`), nil
					}
					if mode == "detail-missing" {
						return apiResponse(req, 404, `{}`), nil
					}
					data := routePolicyFixture("policy-a")
					term := object(array(data["terms"])[0])
					switch mode {
					case "wrapper":
						return dataformResponse(req, 200, data), nil
					case "identity":
						data["name"] = "policy-b"
					case "terms":
						data["terms"] = map[string]any{}
					case "priority":
						term["priority"] = 2147483648
					case "duplicate-priority":
						data["terms"] = []any{term, term}
					case "actions":
						term["actions"] = map[string]any{}
					case "expression":
						object(term["match"])["expression"] = true
					}
					return dataformResponse(req, 200, map[string]any{"resource": data}), nil
				}
				t.Fatal("unexpected request", req.URL)
				return nil, nil
			})
			kind := r.resourceKind(routePolicyType)
			request := contracts.InventoryRequest{ConnectionID: "connection", Source: productInventorySource, ResourceKind: &kind, Scope: asset.Scope{Kind: asset.ScopeRegion, NativeID: "us-central1"}}
			batch, err := r.List(t.Context(), request)
			if err == nil && !batch.Complete {
				request.Cursor = batch.NextCursor
				batch, err = r.List(t.Context(), request)
			}
			if err == nil || batch.Complete {
				t.Fatal("invalid route policy scan completed", mode, batch, err)
			}
		})
	}
}

func TestRoutePolicyIdentityAndInvocationBoundary(t *testing.T) {
	calls := 0
	r := protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
		calls++
		if req.URL.Query().Get("policy") != "policy-a" || !strings.HasSuffix(req.URL.Path, "/routers/router-a/getRoutePolicy") {
			t.Fatal("wrong native binding", req.URL)
		}
		return dataformResponse(req, 200, map[string]any{"resource": routePolicyFixture("policy-a")}), nil
	})
	c, err := r.resolve(t.Context(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	id := "//compute.googleapis.com/projects/sample-project/regions/us-central1/routers/router-a/routePolicies/policy-a"
	for _, invalid := range []string{strings.Replace(id, "sample-project", "foreign-project", 1), id + "/extra", id + "?policy=foreign", strings.Replace(id, "policy-a", "..", 1), strings.Replace(id, "compute.googleapis.com", "example.com", 1)} {
		if _, err := c.routePolicyRead(t.Context(), invalid); err == nil {
			t.Fatal("invalid identity accepted", invalid)
		}
	}
	if calls != 0 {
		t.Fatal("invalid identity called cloud")
	}
	for _, project := range []string{"sample-project", "123456"} {
		parameters := map[string]any{"project": project, "region": "us-central1", "router": "router-a", "policy": "policy-a"}
		result, err := r.Invoke(t.Context(), contracts.Invocation{ConnectionID: "connection", Operation: routePolicyGet, Parameters: parameters})
		if err != nil || result.RequestID == "" || object(result.Data["resource"])["fingerprint"] != "ZnAx" {
			t.Fatal("native GET result", result, err)
		}
	}
	before := calls
	for _, changed := range []map[string]any{{"project": "foreign-project"}, {"router": "../router-a"}, {"policy": "policy-a/other"}, {"region": "us-central1?foreign=true"}} {
		parameters := map[string]any{"project": "sample-project", "region": "us-central1", "router": "router-a", "policy": "policy-a"}
		for key, value := range changed {
			parameters[key] = value
		}
		if _, err := r.Invoke(t.Context(), contracts.Invocation{ConnectionID: "connection", Operation: routePolicyGet, Parameters: parameters}); err == nil {
			t.Fatal("foreign invocation accepted")
		}
	}
	if calls != before {
		t.Fatal("invalid invocation called cloud")
	}
	// Terms are ordered by their native priority, not their position in JSON.
	future := routePolicyFixture("policy-a")
	future["type"] = "FUTURE_POLICY_TYPE"
	if err := routePolicyData(future, "policy-a"); err != nil {
		t.Fatal("future enum rejected", err)
	}
	encoded, _ := json.Marshal(future)
	if !strings.Contains(string(encoded), "accept()") {
		t.Fatal("native expressions lost")
	}
}
