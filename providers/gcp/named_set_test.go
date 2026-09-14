package gcp

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func namedSetFixture(name string) map[string]any {
	return map[string]any{"name": name, "type": "NAMED_SET_TYPE_PREFIX", "description": "Reviewed prefix set", "fingerprint": "ZnAx", "elements": []any{map[string]any{"expression": "prefix('10.0.0.0/20').orLonger()", "title": "Local prefixes"}}}
}

func TestNamedSetNativeInventory(t *testing.T) { testRouterComponentInventory(t, namedSetType) }
func TestNamedSetSQLiteFailureAbsenceAndRecovery(t *testing.T) {
	testRouterComponentSQLiteRecovery(t, namedSetType)
}

func TestNamedSetNativeInventoryFailures(t *testing.T) {
	for _, mode := range []string{"parent-denied", "parent-name-mismatch", "parent-uid-change", "list-denied", "list-partial", "list-unreachable", "list-invalid", "list-name-invalid", "list-cycle", "detail-denied", "detail-missing", "detail-wrapper", "detail-name", "elements-null", "element-scalar", "expression-type", "fingerprint-type"} {
		t.Run(mode, func(t *testing.T) {
			parentReads := 0
			r := protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
				if req.Method != "GET" || req.URL.Host != "compute.googleapis.com" {
					t.Fatal("unexpected mutation or host", req.Method, req.URL)
				}
				if strings.HasSuffix(req.URL.Path, "/routers") {
					parentReads++
					if mode == "parent-denied" {
						return apiResponse(req, 403, `{}`), nil
					}
					parent := map[string]any{"id": "1001", "name": "router-a", "selfLink": "https://www.googleapis.com" + req.URL.Path + "/router-a"}
					if mode == "parent-name-mismatch" {
						parent["name"] = "wrong-router"
					}
					if mode == "parent-uid-change" && parentReads > 1 {
						parent["id"] = "1002"
					}
					return dataformResponse(req, 200, map[string]any{"items": []any{parent}}), nil
				}
				if strings.HasSuffix(req.URL.Path, "/listNamedSets") {
					if mode == "list-denied" {
						return apiResponse(req, 403, `{}`), nil
					}
					result := map[string]any{"result": []any{map[string]any{"name": "set-a"}}}
					switch mode {
					case "list-partial":
						result["warning"] = map[string]any{"code": "PARTIAL_SUCCESS"}
					case "list-unreachable":
						result["unreachables"] = []any{"zone-a"}
					case "list-invalid":
						result["result"] = map[string]any{}
					case "list-name-invalid":
						result["result"] = []any{map[string]any{"name": "../foreign"}}
					case "list-cycle", "parent-uid-change":
						result["nextPageToken"] = "next"
					}
					return dataformResponse(req, 200, result), nil
				}
				if strings.HasSuffix(req.URL.Path, "/getNamedSet") {
					if req.URL.Query().Get("namedSet") != "set-a" || req.URL.Query().Get("policy") != "" {
						t.Fatal("wrong query", req.URL)
					}
					if mode == "detail-denied" {
						return apiResponse(req, 403, `{}`), nil
					}
					if mode == "detail-missing" {
						return apiResponse(req, 404, `{}`), nil
					}
					data := namedSetFixture("set-a")
					switch mode {
					case "detail-wrapper":
						return dataformResponse(req, 200, data), nil
					case "detail-name":
						data["name"] = "other"
					case "elements-null":
						data["elements"] = nil
					case "element-scalar":
						data["elements"] = []any{"not-an-expression-object"}
					case "expression-type":
						data["elements"] = []any{map[string]any{"expression": false}}
					case "fingerprint-type":
						data["fingerprint"] = 123
					}
					return dataformResponse(req, 200, map[string]any{"resource": data}), nil
				}
				t.Fatal("unexpected path", req.URL)
				return nil, nil
			})
			kind := r.resourceKind(namedSetType)
			request := contracts.InventoryRequest{ConnectionID: "connection", Source: productInventorySource, ResourceKind: &kind, Scope: asset.Scope{Kind: asset.ScopeRegion, NativeID: "us-central1"}}
			var err error
			for attempt := 0; attempt < 3; attempt++ {
				var batch contracts.InventoryBatch
				batch, err = r.List(t.Context(), request)
				if err != nil {
					break
				}
				if batch.Complete {
					t.Fatal("invalid source passed", mode)
				}
				request.Cursor = batch.NextCursor
			}
			if err == nil {
				t.Fatal("invalid continuation passed")
			}
		})
	}
}

func TestNamedSetInvocationAndIdentityBoundaries(t *testing.T) {
	calls := 0
	r := protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
		calls++
		if req.Method != "GET" || !strings.HasSuffix(req.URL.Path, "/routers/router-a/getNamedSet") || req.URL.Query().Get("namedSet") != "set-a" {
			t.Fatal("wrong native request", req.URL)
		}
		data := namedSetFixture("set-a")
		data["type"] = "NAMED_SET_TYPE_COMMUNITY"
		data["elements"] = []any{map[string]any{"expression": "'64500:100'"}}
		return dataformResponse(req, 200, map[string]any{"resource": data}), nil
	})
	for _, project := range []string{"sample-project", "123456"} {
		result, err := r.Invoke(t.Context(), contracts.Invocation{ConnectionID: "connection", Operation: namedSetGet, Parameters: map[string]any{"project": project, "region": "us-central1", "router": "router-a", "namedSet": "set-a"}})
		if err != nil || result.RequestID == "" || object(result.Data["resource"])["type"] != "NAMED_SET_TYPE_COMMUNITY" {
			t.Fatal(result, err)
		}
	}
	count := calls
	for key, value := range map[string]any{"project": "foreign", "region": "../region", "router": "router/foreign", "namedSet": "../foreign"} {
		parameters := map[string]any{"project": "sample-project", "region": "us-central1", "router": "router-a", "namedSet": "set-a"}
		parameters[key] = value
		if _, err := r.Invoke(t.Context(), contracts.Invocation{ConnectionID: "connection", Operation: namedSetGet, Parameters: parameters}); err == nil {
			t.Fatal("foreign parameter accepted", key)
		}
	}
	if calls != count {
		t.Fatal("invalid invoke reached cloud")
	}
	c := &client{project: "sample-project", number: "123456"}
	for _, id := range []string{"//compute.googleapis.com/projects/foreign/regions/us-central1/routers/router-a/namedSets/set-a", "//compute.googleapis.com/projects/sample-project/regions/us-central1/routers/router-a/routePolicies/set-a", "//compute.googleapis.com/projects/sample-project/regions/us-central1/routers/router-a/namedSets/../set-a"} {
		if _, _, err := c.routerComponentOperation(namedSetType, id, "GET"); err == nil {
			t.Fatal("wrong identity accepted", id)
		}
	}
}

func TestNamedSetFixturesMatchNativeSchema(t *testing.T) {
	raw, err := os.ReadFile("catalog/source/discovery.json")
	if err != nil {
		t.Fatal(err)
	}
	var source map[string]any
	if err = json.Unmarshal(raw, &source); err != nil {
		t.Fatal(err)
	}
	for _, value := range array(source["documents"]) {
		document := object(object(value)["document"])
		if object(document["methods"])[namedSetGet] == nil {
			continue
		}
		compiler := discoveryFixtureSchemaCompiler(t, object(document["schemas"]))
		schema, err := compiler.Compile("https://fixture.test/infra-manager.json#/definitions/NamedSet")
		if err != nil {
			t.Fatal(err)
		}
		for _, kind := range []string{"NAMED_SET_TYPE_PREFIX", "NAMED_SET_TYPE_COMMUNITY"} {
			data := namedSetFixture("set-a")
			data["type"] = kind
			if kind == "NAMED_SET_TYPE_COMMUNITY" {
				data["elements"] = []any{map[string]any{"expression": "'64500:100'"}}
			}
			if err = schema.Validate(data); err != nil {
				t.Fatal(err)
			}
		}
		invalid := namedSetFixture("set-a")
		invalid["elements"] = []any{"bad"}
		if schema.Validate(invalid) == nil {
			t.Fatal("native schema accepted malformed CEL")
		}
		future := namedSetFixture("set-a")
		future["type"] = "FUTURE_NATIVE_TYPE"
		if routerComponentData(namedSetType, future, "set-a") != nil {
			t.Fatal("forward-compatible native enum rejected")
		}
		return
	}
	t.Fatal("native named-set schema missing")
}
