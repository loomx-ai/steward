package azure

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/app/governance"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/core/graph"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func addLocalAKSFixture(t *testing.T, f *localNetworkCleanupFixture) (string, string) {
	t.Helper()
	parent := strings.ToLower(resourceID(fleetArcClusterType, "local-aks"))
	id := parent + azureLocalAKSSuffix
	load := func(file string) map[string]any {
		b, err := os.ReadFile("fixtures/azure-local/network-consumers/" + file + ".json")
		if err != nil {
			t.Fatal(err)
		}
		var v map[string]any
		if json.Unmarshal(b, &v) != nil {
			t.Fatal("fixture")
		}
		return object(object(object(v["responses"])["200"])["body"])
	}
	arc := load("ConnectedCluster_Get")
	arc["id"], arc["name"], arc["location"] = parent, last(parent), "eastus"
	f.values[parent] = arc
	raw := load("provisionedClusterInstances_Get")
	raw["id"], raw["name"] = id, last(parent)
	raw["extendedLocation"] = batchClone(object(f.values[f.id]["extendedLocation"]))
	object(object(object(raw["properties"])["cloudProviderProfile"])["infraNetworkProfile"])["vnetSubnetIds"] = []any{f.id}
	object(raw["properties"])["futurePrivateConfiguration"] = "private-local-aks-secret"
	object(arc["properties"])["agentPublicKeyCertificate"] = "private-local-aks-certificate"
	f.values[id] = raw
	previous := f.override
	f.override = func(req *http.Request) (*http.Response, bool) {
		path := strings.ToLower(req.URL.Path)
		if path != parent && path != id && path != strings.TrimSuffix(id, "/default") && path != f.client.root()+"/providers/microsoft.kubernetes/connectedclusters" {
			return previous(req)
		}
		if req.Method != "GET" || req.URL.Query().Get("api-version") != "2024-01-01" {
			t.Fatal("invalid native AKS request", req.Method, req.URL)
		}
		if path == parent || path == id {
			if f.values[path] == nil {
				return jsonResponse(404, map[string]any{"error": map[string]any{"code": "ResourceNotFound"}}, nil), true
			}
			return jsonResponse(200, f.values[path], http.Header{"X-Ms-Request-Id": {"local-aks-read"}}), true
		}
		member := parent
		if path == strings.TrimSuffix(id, "/default") {
			member = id
		}
		rows := []any{}
		if f.values[member] != nil && !f.omitted[member] {
			rows = append(rows, f.values[member])
		}
		return jsonResponse(200, map[string]any{"value": rows}, nil), true
	}
	return parent, id
}

func TestAzureLocalAKSNetworkConsumers(t *testing.T) {
	for _, role := range []string{"Workload", "Infrastructure"} {
		for _, mode := range []string{"attached", "known-omitted", "parent-gone", "instance-index-missing", "missing-placement", "other-network", "other-location", "malformed-profile", "foreign-reference", "new-cluster", "native-case", "own-absent", "denied", "private"} {
			t.Run(role+"/"+mode, func(t *testing.T) {
				f := newLocalNetworkCleanupFixture(t, role)
				f.clearConsumers()
				parent, id := addLocalAKSFixture(t, f)
				if mode == "new-cluster" {
					delete(f.values, parent)
					delete(f.values, id)
				}
				request := f.requestAsset(t)
				if mode == "new-cluster" {
					parent, id = addLocalAKSFixture(t, f)
				}
				raw := f.values[id]
				allowed := false
				switch mode {
				case "known-omitted":
					f.omitted[parent], f.omitted[id] = true, true
				case "parent-gone":
					delete(f.values, parent)
					f.omitted[id] = true
				case "instance-index-missing":
					previous := f.override
					f.override = func(req *http.Request) (*http.Response, bool) {
						if strings.EqualFold(req.URL.Path, strings.TrimSuffix(id, "/default")) {
							return jsonResponse(404, map[string]any{"error": map[string]any{"code": "ResourceNotFound"}}, nil), true
						}
						return previous(req)
					}
				case "missing-placement":
					delete(object(raw["properties"]), "cloudProviderProfile")
				case "other-network", "foreign-reference":
					target := f.id + "-other"
					if mode == "foreign-reference" {
						target = strings.Replace(f.id, testSubscription, testTenant, 1)
					}
					object(object(object(raw["properties"])["cloudProviderProfile"])["infraNetworkProfile"])["vnetSubnetIds"] = []any{target}
					allowed = role == "Workload"
				case "other-location":
					object(raw["extendedLocation"])["name"] = text(object(raw["extendedLocation"])["name"]) + "-other"
					allowed = role == "Infrastructure"
				case "malformed-profile":
					object(raw["properties"])["cloudProviderProfile"] = false
				case "native-case":
					raw["type"] = strings.ToLower(azureLocalAKSType)
				case "own-absent":
					delete(f.values, id)
					allowed = true
				case "denied":
					previous := f.override
					f.override = func(req *http.Request) (*http.Response, bool) {
						if strings.EqualFold(req.URL.Path, id) {
							return jsonResponse(403, map[string]any{"error": map[string]any{"code": "AuthorizationFailed"}}, nil), true
						}
						return previous(req)
					}
				}
				logs := []execution.JobLogEntry{}
				ctx := execution.WithJobLogSink(t.Context(), execution.JobLogSinkFunc(func(_ context.Context, e execution.JobLogEntry) { logs = append(logs, e) }))
				driver, err := f.runtime.ResolveAction(ctx, "connection", request.Asset)
				if err == nil {
					_, err = driver.Preflight(ctx, request)
				}
				if (err == nil) != allowed {
					t.Fatal("AKS consumer boundary", allowed, err)
				}
				if mode == "attached" {
					result := governance.Contribution{}
					s := &serviceCascades{client: f.client, connectionID: "connection"}
					if err := s.contributeAzureLocalRoots(ctx, []asset.Asset{request.Asset}, &result); err != nil {
						t.Fatal(err)
					}
					if len(result.Unresolved) != 1 || result.Unresolved[0].NativeID != id || result.Unresolved[0].NativeType != azureLocalAKSType || result.Unresolved[0].Relationship != graph.RelationshipDependsOn || result.Unresolved[0].Evidence[graph.RelationshipEvidenceAutomaticSelection] != false {
						t.Fatal("AKS consumer not represented", result)
					}
				}
				encoded, _ := json.Marshal(map[string]any{"asset": request.Asset, "logs": logs})
				if strings.Contains(string(encoded), "private-local-aks") {
					t.Fatal("private AKS state exposed")
				}
				if f.rootDeletes != 0 || len(f.deletes) != 0 {
					t.Fatal("readiness check mutated consumers")
				}
			})
		}
	}
}

func TestAzureLocalAKSNativeReadBoundaries(t *testing.T) {
	for _, mode := range []string{"parent-index-duplicate", "parent-index-foreign", "parent-replaced", "instance-duplicate", "instance-foreign", "instance-wrong-type", "instance-stale-index", "instance-wrong-name", "instance-async", "parent-denied", "missing-singleton", "malformed-network-list", "wrong-network-kind"} {
		t.Run(mode, func(t *testing.T) {
			f := newLocalNetworkCleanupFixture(t, "Workload")
			parent, id := addLocalAKSFixture(t, f)
			request := f.requestAsset(t)
			f.clearConsumers()
			previous := f.override
			f.override = func(req *http.Request) (*http.Response, bool) {
				path := strings.ToLower(req.URL.Path)
				switch mode {
				case "parent-index-duplicate", "parent-index-foreign":
					if path == f.client.root()+"/providers/microsoft.kubernetes/connectedclusters" {
						rows := []any{f.values[parent], f.values[parent]}
						if mode == "parent-index-foreign" {
							raw := batchClone(f.values[parent])
							raw["id"] = strings.Replace(parent, testSubscription, testTenant, 1)
							rows = []any{raw}
						}
						return jsonResponse(200, map[string]any{"value": rows}, nil), true
					}
				case "parent-replaced":
					if path == parent {
						raw := batchClone(f.values[parent])
						raw["location"] = "westus"
						return jsonResponse(200, raw, nil), true
					}
				case "instance-duplicate", "instance-foreign", "instance-wrong-type", "instance-stale-index":
					if path == strings.TrimSuffix(id, "/default") {
						raw := batchClone(f.values[id])
						rows := []any{raw}
						switch mode {
						case "instance-duplicate":
							rows = append(rows, raw)
						case "instance-foreign":
							raw["id"] = strings.Replace(id, testSubscription, testTenant, 1)
						case "instance-wrong-type":
							raw["type"] = azureLocalVMType
						case "instance-stale-index":
							raw["etag"] = "different"
						}
						return jsonResponse(200, map[string]any{"value": rows}, nil), true
					}
				case "instance-wrong-name":
					if path == id {
						raw := batchClone(f.values[id])
						raw["name"] = "wrong"
						return jsonResponse(200, raw, nil), true
					}
				case "instance-async":
					if path == id {
						return jsonResponse(202, f.values[id], nil), true
					}
				case "parent-denied":
					if path == parent {
						return jsonResponse(403, map[string]any{"error": map[string]any{"code": "AuthorizationFailed"}}, nil), true
					}
				case "missing-singleton":
					if path == id {
						return jsonResponse(404, map[string]any{"error": map[string]any{"code": "ResourceNotFound"}}, nil), true
					}
				case "malformed-network-list", "wrong-network-kind":
					if path == id {
						raw := batchClone(f.values[id])
						v := any(false)
						if mode == "wrong-network-kind" {
							v = []any{f.ids[azureLocalNICType]}
						}
						object(object(object(raw["properties"])["cloudProviderProfile"])["infraNetworkProfile"])["vnetSubnetIds"] = v
						return jsonResponse(200, raw, nil), true
					}
				}
				return previous(req)
			}
			driver, err := f.runtime.ResolveAction(t.Context(), "connection", request.Asset)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := driver.Preflight(t.Context(), request); err == nil {
				t.Fatal("bad dependency allowed network deletion")
			}
			delete(f.values, f.id)
			if read, err := driver.Readback(t.Context(), request); err == nil || isNotFound(err) {
				t.Fatal("dependency failure became network absence", read, err)
			}
		})
	}
}

func TestAzureLocalAKSInvocationScope(t *testing.T) {
	f := newLocalNetworkCleanupFixture(t, "Workload")
	parent, id := addLocalAKSFixture(t, f)
	invocation := contracts.Invocation{ConnectionID: "connection", Operation: "Azure.Microsoft.HybridContainerService.provisionedClusterInstances_Get", Parameters: map[string]any{"connectedClusterResourceUri": strings.TrimPrefix(parent, "/")}}
	result, err := f.runtime.Invoke(t.Context(), invocation)
	if err != nil || result.Data["id"] != id {
		t.Fatal(result, err)
	}
	encoded, _ := json.Marshal(result)
	if strings.Contains(string(encoded), "private-local-aks") {
		t.Fatal("private native invocation")
	}
	for _, bad := range []string{parent, parent + "/", strings.TrimPrefix(parent, "/") + "/providers/Microsoft.HybridContainerService/provisionedClusterInstances/default", strings.TrimPrefix(f.ids[hybridMachineType], "/"), strings.TrimPrefix(strings.Replace(parent, testSubscription, testTenant, 1), "/"), strings.TrimPrefix(parent, "/") + "?force=true"} {
		invocation.Parameters = map[string]any{"connectedClusterResourceUri": bad}
		if _, err := f.runtime.Invoke(t.Context(), invocation); err == nil {
			t.Fatal("invalid AKS parent accepted", bad)
		}
	}
}
