package azure

import (
	"bytes"
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

type synapseInventoryFixture struct {
	runtime     *Runtime
	objects     map[string]map[string]any
	collections map[string][]string
	reads       map[string]int
	override    func(*http.Request) (*http.Response, bool)
}

// Adapt only fixture identities/location to one test subscription. These are
// protocol tests derived from native schemas, not live service recordings.
func newSynapseInventoryFixture(t *testing.T) *synapseInventoryFixture {
	t.Helper()
	f := &synapseInventoryFixture{objects: map[string]map[string]any{}, collections: map[string][]string{}, reads: map[string]int{}}
	root := "/subscriptions/" + testSubscription
	workspaceCollection := root + "/providers/microsoft.synapse/workspaces"
	for _, parent := range []string{"first", "second"} {
		for _, kind := range []string{synapseType, synapseSparkType, synapseSQLType} {
			example := map[string]string{synapseType: "GetWorkspace", synapseSparkType: "GetBigDataPool", synapseSQLType: "GetSqlPool"}[kind]
			payload, err := os.ReadFile("fixtures/synapse/" + example + ".json")
			var native map[string]any
			if err != nil || json.Unmarshal(payload, &native) != nil {
				t.Fatal("invalid source example", err)
			}
			raw := object(object(object(native["responses"])["200"])["body"])
			id, collection := strings.ToLower(resourceID(synapseType, parent)), workspaceCollection
			name := parent
			if kind != synapseType {
				collection = id + "/" + strings.ToLower(last(kind))
				name = "pool"
				id = collection + "/" + name
			}
			raw["id"], raw["name"], raw["location"] = id, name, "eastus"
			if parent == "second" {
				raw["location"] = "westus"
			}
			if kind == synapseType {
				object(raw["properties"])["connectivityEndpoints"] = map[string]any{"dev": "https://" + parent + ".dev.azuresynapse.net"}
				// Child/network artifacts have separate unfinished coverage; do
				// not reuse the source example's unrelated subscription IDs.
				delete(object(raw["properties"]), "privateEndpointConnections")
				delete(object(raw["properties"]), "purviewConfiguration")
				object(raw["properties"])["sqlAdministratorLoginPassword"] = "password-canary"
			}
			f.objects[id] = raw
			f.collections[collection] = append(f.collections[collection], id)
		}
	}
	storage := nativeResource(storageType, "accountname", "eastus", map[string]any{"provisioningState": "Succeeded", "creationTime": "2025-01-01T00:00:00Z", "primaryEndpoints": map[string]any{"dfs": "https://accountname.dfs.core.windows.net/"}})
	storageID := strings.ToLower(text(storage["id"]))
	f.objects[storageID] = storage
	f.collections[root+"/providers/microsoft.storage/storageaccounts"] = []string{storageID}
	f.runtime = protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
		if req.Method == "GET" && strings.HasSuffix(req.URL.Host, ".dev.azuresynapse.net") {
			if strings.Contains(req.URL.Path, "/livyApi/") {
				return jsonResponse(200, map[string]any{"from": 0, "total": 0, "sessions": []any{}}, nil), nil
			}
			return jsonResponse(200, map[string]any{"value": []any{}}, nil), nil
		}
		if req.Method != "GET" || req.URL.Host != "management.azure.com" {
			t.Fatal("unexpected request", req.Method, req.URL)
		}
		path := strings.ToLower(req.URL.Path)
		f.reads[path]++
		if f.override != nil {
			if response, ok := f.override(req); ok {
				return response, nil
			}
		}
		if strings.Contains(path, "/microsoft.synapse/") && req.URL.Query().Get("api-version") != synapseVersion {
			t.Fatal("wrong Synapse version", req.URL)
		}
		if strings.HasSuffix(path, "/replicationlinks") {
			return jsonResponse(200, map[string]any{"value": []any{}}, nil), nil
		}
		header := http.Header{"X-Ms-Request-Id": {"synapse-inventory-request"}}
		if path == root+"/resourcegroups/test" {
			return jsonResponse(200, map[string]any{"id": path, "name": "test", "type": groupType, "location": "eastus", "properties": map[string]any{}}, header), nil
		}
		if path == root+"/resourcegroups" || path == root+"/providers/microsoft.authorization/locks" {
			return jsonResponse(200, map[string]any{"value": []any{}}, header), nil
		}
		if ids, exists := f.collections[path]; exists {
			values := []any{}
			body := map[string]any{}
			for i, id := range ids {
				if len(ids) > 1 && (req.URL.Query().Get("$skiptoken") == "" && i > 0 || req.URL.Query().Get("$skiptoken") != "" && i == 0) {
					continue
				}
				values = append(values, f.objects[id])
			}
			body["value"] = values
			if len(ids) > 1 && req.URL.Query().Get("$skiptoken") == "" {
				body["nextLink"] = req.URL.String() + "&%24skiptoken=opaque%2B%2F%3D"
			}
			return jsonResponse(200, body, header), nil
		}
		if raw := f.objects[path]; raw != nil {
			return jsonResponse(200, raw, header), nil
		}
		t.Fatal("unexpected endpoint", req.URL)
		return nil, nil
	})
	return f
}

func TestSynapseNativeInventoryAndReferences(t *testing.T) {
	for _, kind := range []string{synapseType, synapseSparkType, synapseSQLType} {
		t.Run(kind, func(t *testing.T) {
			f := newSynapseInventoryFixture(t)
			request := productRequest(f.runtime, kind)
			var items []contracts.InventoryItem
			for pages := 0; ; pages++ {
				batch, err := f.runtime.List(t.Context(), request)
				if err != nil || batch.RequestID != "synapse-inventory-request" || pages > 4 {
					t.Fatal(batch, err)
				}
				items = append(items, batch.Items...)
				if batch.Complete {
					break
				}
				if batch.NextCursor == "" {
					t.Fatal("missing cursor")
				}
				request.Cursor = batch.NextCursor
			}
			if len(items) != 2 {
				t.Fatal("incomplete parent/child inventory", len(items))
			}
			for _, item := range items {
				if kind == synapseSQLType && (item.State != "Online" || item.Normalized["state"] != "Online") {
					t.Fatal("SQL activity state lost", item.State)
				}
				if item.NativeType != kind || item.Actionable == nil || !*item.Actionable || text(item.Normalized["_synapse_private_configuration"]) == "" {
					t.Fatal("incorrect inventory readiness", item)
				}
				if kind == synapseType {
					storage := strings.ToLower(resourceID(storageType, "accountname"))
					if !slices.Contains(stringValues(item.Normalized[referenceKey(storageType)]), storage) || !slices.Contains(item.NetworkReferences, storage+"/blobservices/default/containers/default") {
						t.Fatal("default storage references missing", item.Normalized)
					}
				} else if !slices.Contains(item.NetworkReferences, strings.Join(strings.Split(item.NativeID, "/")[:9], "/")) {
					t.Fatal("workspace relationship missing")
				}
				encoded, _ := json.Marshal(item)
				if strings.Contains(string(encoded), "password-canary") {
					t.Fatal("private configuration escaped inventory")
				}
				if _, err := f.runtime.ResolveAction(t.Context(), "connection", asset.Asset{Identity: asset.Identity{Provider: asset.ProviderAzure, ConnectionID: "connection", NativeType: kind, NativeID: item.NativeID}}); err == nil {
					t.Fatal("unfinished cleanup became actionable")
				}
			}
			request = productRequest(f.runtime, kind)
			request.Scope = asset.Scope{Kind: asset.ScopeRegion, NativeID: "eastus"}
			count := 0
			for {
				batch, err := f.runtime.List(t.Context(), request)
				if err != nil {
					t.Fatal(err)
				}
				for _, item := range batch.Items {
					if item.Location != "eastus" {
						t.Fatal("regional inventory escaped")
					}
					count++
				}
				if batch.Complete {
					break
				}
				request.Cursor = batch.NextCursor
			}
			if count != 1 {
				t.Fatal("regional inventory incomplete", count)
			}
		})
	}
}

func TestSynapseInventoryRejectsIncompleteResponses(t *testing.T) {
	for _, kind := range []string{synapseType, synapseSparkType, synapseSQLType} {
		for _, mode := range []string{"forbidden", "missing-list", "partial", "accepted", "polling", "array", "foreign", "wrong-parent", "duplicate", "metadata", "detail-id", "detail-status", "detail-error", "detail-poll", "listed-drift", "cursor-host", "cursor-filter", "cursor-duplicate-version", "cursor-parent", "cursor-type"} {
			if kind == synapseType && mode == "wrong-parent" {
				continue
			}
			t.Run(kind+"/"+mode, func(t *testing.T) {
				f := newSynapseInventoryFixture(t)
				id := strings.ToLower(resourceID(synapseType, "first"))
				collection := "/subscriptions/" + testSubscription + "/providers/microsoft.synapse/workspaces"
				if kind != synapseType {
					collection = id + "/" + strings.ToLower(last(kind))
					id = collection + "/pool"
				}
				f.override = func(req *http.Request) (*http.Response, bool) {
					path := strings.ToLower(req.URL.Path)
					if strings.HasPrefix(mode, "detail-") || mode == "listed-drift" {
						if path != id {
							return nil, false
						}
						raw := maps.Clone(f.objects[id])
						raw["properties"] = maps.Clone(object(raw["properties"]))
						status, headers := 200, http.Header{}
						switch mode {
						case "detail-id":
							raw["id"] = id + "-other"
						case "detail-status":
							status = 202
						case "detail-error":
							raw["error"] = map[string]any{"code": "Incomplete"}
						case "detail-poll":
							headers.Set("Location", req.URL.String())
						case "listed-drift":
							raw["location"] = "other-region"
						}
						return jsonResponse(status, raw, headers), true
					}
					if path != collection {
						return nil, false
					}
					raw := maps.Clone(f.objects[id])
					body := map[string]any{"value": []any{raw}}
					status, headers := 200, http.Header{}
					switch mode {
					case "forbidden":
						status = 403
					case "missing-list":
						status = 404
					case "partial":
						status = 206
					case "accepted":
						status = 202
					case "polling":
						headers.Set("Azure-Asyncoperation", req.URL.String())
					case "array":
						body["value"] = map[string]any{}
					case "foreign":
						raw["id"] = strings.Replace(id, testSubscription, testTenant, 1)
					case "wrong-parent":
						raw["id"] = strings.Replace(id, "/workspaces/first/", "/workspaces/second/", 1)
					case "duplicate":
						body["value"] = []any{raw, raw}
					case "metadata":
						raw["properties"] = false
					case "cursor-host":
						body["nextLink"] = "https://foreign.example/page"
					case "cursor-filter":
						body["nextLink"] = req.URL.String() + "&%24filter=hidden"
					case "cursor-duplicate-version":
						body["nextLink"] = req.URL.String() + "&api-version=" + synapseVersion
					case "cursor-parent":
						body["nextLink"] = strings.Replace(req.URL.String(), "/workspaces", "/other", 1) + "&skipToken=next"
					case "cursor-type":
						body["nextLink"] = true
					}
					return jsonResponse(status, body, headers), true
				}
				batch, err := f.runtime.List(t.Context(), productRequest(f.runtime, kind))
				if err == nil || len(batch.Items) != 0 || batch.Complete {
					t.Fatal("invalid inventory accepted", batch, err)
				}
			})
		}
	}
}

func TestSynapseInventoryParentAndCursorConsistency(t *testing.T) {
	for _, kind := range []string{synapseSparkType, synapseSQLType} {
		for _, mode := range []string{"parent-configuration", "parent-missing", "parent-partial", "future-state", "cursor-parent-change", "cursor-scope-change", "duplicate-page"} {
			t.Run(kind+"/"+mode, func(t *testing.T) {
				f := newSynapseInventoryFixture(t)
				parent := strings.ToLower(resourceID(synapseType, "first"))
				collection := parent + "/" + strings.ToLower(last(kind))
				listed := false
				f.override = func(req *http.Request) (*http.Response, bool) {
					path := strings.ToLower(req.URL.Path)
					if path == collection {
						listed = true
						if mode == "duplicate-page" {
							body := map[string]any{"value": []any{f.objects[collection+"/pool"]}}
							if req.URL.Query().Get("skipToken") == "" {
								body["nextLink"] = req.URL.String() + "&skipToken=again"
							}
							return jsonResponse(200, body, http.Header{"X-Ms-Request-Id": {"synapse-page"}}), true
						}
					}
					if path == parent && listed {
						switch mode {
						case "parent-missing":
							return jsonResponse(404, map[string]any{}, nil), true
						case "parent-partial":
							return jsonResponse(206, f.objects[parent], nil), true
						case "parent-configuration":
							object(f.objects[parent]["properties"])["managedResourceGroupName"] = "new-managed-group"
						case "future-state":
							object(f.objects[parent]["properties"])["provisioningState"] = "FutureState"
						}
					}
					return nil, false
				}
				request := productRequest(f.runtime, kind)
				request.Limit = 1
				batch, err := f.runtime.List(t.Context(), request)
				if mode == "duplicate-page" {
					if err == nil || len(batch.Items) != 0 || batch.Complete {
						t.Fatal("duplicate index accepted", batch, err)
					}
					return
				}
				if strings.HasPrefix(mode, "parent-") {
					if err == nil || len(batch.Items) != 0 || batch.Complete {
						t.Fatal("parent drift accepted", batch, err)
					}
					return
				}
				if err != nil || len(batch.Items) != 1 || batch.Complete {
					t.Fatal(batch, err)
				}
				if mode == "future-state" {
					return
				}
				request.Cursor = batch.NextCursor
				if mode == "cursor-parent-change" {
					object(f.objects[parent]["properties"])["managedResourceGroupName"] = "new-managed-group"
				}
				if mode == "cursor-scope-change" {
					request.Scope = asset.Scope{Kind: asset.ScopeRegion, NativeID: "eastus"}
				}
				batch, err = f.runtime.List(t.Context(), request)
				if err == nil || len(batch.Items) != 0 || batch.Complete {
					t.Fatal("invalid continuation accepted", batch, err)
				}
			})
		}
	}
}

func TestSynapseInventoryStorageBoundary(t *testing.T) {
	for _, mode := range []string{"unresolved", "missing-metadata", "url-userinfo", "url-query", "filesystem-path", "index-denied", "index-foreign", "storage-drift", "ambiguous", "workspace-drift", "identity", "identity-invalid"} {
		t.Run(mode, func(t *testing.T) {
			f := newSynapseInventoryFixture(t)
			workspace := strings.ToLower(resourceID(synapseType, "first"))
			storage := strings.ToLower(resourceID(storageType, "accountname"))
			index := "/subscriptions/" + testSubscription + "/providers/microsoft.storage/storageaccounts"
			props := object(f.objects[workspace]["properties"])
			lake := object(props["defaultDataLakeStorage"])
			switch mode {
			case "unresolved":
				lake["accountUrl"] = "https://elsewhere.dfs.core.windows.net"
			case "missing-metadata":
				delete(props, "defaultDataLakeStorage")
			case "url-userinfo":
				lake["accountUrl"] = "https://user:password@accountname.dfs.core.windows.net"
			case "url-query":
				lake["accountUrl"] = "https://accountname.dfs.core.windows.net/?sig=secret"
			case "filesystem-path":
				lake["filesystem"] = "../other"
			case "identity", "identity-invalid":
				id := resourceID("Microsoft.ManagedIdentity/userAssignedIdentities", "identity")
				if mode == "identity-invalid" {
					id = storage
				}
				f.objects[workspace]["identity"] = map[string]any{"type": "UserAssigned", "userAssignedIdentities": map[string]any{id: map[string]any{}}}
			}
			f.override = func(req *http.Request) (*http.Response, bool) {
				path := strings.ToLower(req.URL.Path)
				if path == index {
					switch mode {
					case "index-denied":
						return jsonResponse(403, map[string]any{}, nil), true
					case "index-foreign":
						raw := maps.Clone(f.objects[storage])
						raw["id"] = strings.Replace(storage, testSubscription, testTenant, 1)
						return jsonResponse(200, map[string]any{"value": []any{raw}}, nil), true
					case "ambiguous":
						other := maps.Clone(f.objects[storage])
						other["id"], other["name"] = storage+"other", "accountnameother"
						f.objects[storage+"other"] = other
						return jsonResponse(200, map[string]any{"value": []any{f.objects[storage], other}}, nil), true
					}
				}
				if path == storage {
					switch mode {
					case "storage-drift":
						raw := maps.Clone(f.objects[storage])
						raw["properties"] = maps.Clone(object(raw["properties"]))
						object(raw["properties"])["primaryEndpoints"] = map[string]any{"dfs": "https://changed.dfs.core.windows.net"}
						return jsonResponse(200, raw, nil), true
					case "workspace-drift":
						props["managedResourceGroupName"] = "changed-group"
					}
				}
				return nil, false
			}
			request := productRequest(f.runtime, synapseType)
			request.Limit = 1
			batch, err := f.runtime.List(t.Context(), request)
			if mode == "unresolved" || mode == "missing-metadata" || mode == "identity" {
				if err != nil || len(batch.Items) != 1 {
					t.Fatal(batch, err)
				}
				item := batch.Items[0]
				if mode == "identity" {
					id := strings.ToLower(resourceID("Microsoft.ManagedIdentity/userAssignedIdentities", "identity"))
					if !slices.Contains(item.NetworkReferences, id) {
						t.Fatal("identity reference missing")
					}
				} else if item.Normalized["_synapse_unresolved_storage"] == nil || len(stringValues(item.Normalized[referenceKey(storageType)])) != 0 {
					t.Fatal("fabricated storage ownership", item)
				}
			} else if err == nil || len(batch.Items) != 0 || batch.Complete {
				t.Fatal("invalid storage binding accepted", batch, err)
			}
		})
	}
}

func TestSynapseInventoryAndInvocationPrivacy(t *testing.T) {
	f := newSynapseInventoryFixture(t)
	logs := []execution.JobLogEntry{}
	ctx := execution.WithJobLogSink(t.Context(), execution.JobLogSinkFunc(func(_ context.Context, entry execution.JobLogEntry) { logs = append(logs, entry) }))
	batch, err := f.runtime.List(ctx, productRequest(f.runtime, synapseType))
	if err != nil {
		t.Fatal(err)
	}
	result, err := f.runtime.Invoke(ctx, contracts.Invocation{ConnectionID: "connection", Operation: "Azure.Microsoft.Synapse.Workspaces_Get", Parameters: map[string]any{"resourceGroupName": "test", "workspaceName": "first"}})
	if err != nil || result.RequestID != "synapse-inventory-request" || len(logs) == 0 {
		t.Fatal("native call or logging missing", err)
	}
	for _, value := range []any{batch, result, logs} {
		encoded, err := json.Marshal(value)
		if err != nil || strings.Contains(string(encoded), "password-canary") {
			t.Fatal("password escaped shared redaction", err)
		}
	}
	if object(f.objects[strings.ToLower(resourceID(synapseType, "first"))]["properties"])["sqlAdministratorLoginPassword"] != "password-canary" {
		t.Fatal("redaction mutated the native source")
	}
}

func TestSynapseRegisteredInventoryAndKnownAbsence(t *testing.T) {
	f := newSynapseInventoryFixture(t)
	repository, registry, database := azureNativeWorkerRepository(t, f.runtime)
	kinds := []string{synapseType, synapseSparkType, synapseSQLType}
	scan := func(failure bool) []asset.Asset {
		return azureNativeWorkerScan(t, f.runtime, synapseSource, repository, registry, kinds, failure, false)
	}
	values := scan(false)
	if len(values) != 6 {
		t.Fatal("registered inventory incomplete", len(values))
	}
	for _, value := range values {
		if string(value.ID) == value.Identity.NativeID || value.Normalized["_inventory_source"] != synapseSource || !value.Capabilities.Has(asset.CapabilityActionable) {
			t.Fatal("registered resource identity/readiness changed", value)
		}
	}
	// Hiding the workspace list must not erase any known workspace or pool.
	f.collections["/subscriptions/"+testSubscription+"/providers/microsoft.synapse/workspaces"] = []string{}
	values = scan(false)
	if len(values) != 6 {
		t.Fatal("parent list omission erased known resources", len(values))
	}
	target := strings.ToLower(resourceID(synapseType, "first")) + "/sqlpools/pool"
	f.override = func(req *http.Request) (*http.Response, bool) {
		if strings.EqualFold(req.URL.Path, target) {
			return jsonResponse(403, map[string]any{"error": map[string]any{"code": "AuthorizationFailed"}}, nil), true
		}
		return nil, false
	}
	if len(scan(true)) != 6 {
		t.Fatal("failed direct GET closed a known resource")
	}
	f.override = func(req *http.Request) (*http.Response, bool) {
		if strings.EqualFold(req.URL.Path, target) {
			return jsonResponse(404, map[string]any{"error": map[string]any{"code": "ResourceNotFound"}}, nil), true
		}
		return nil, false
	}
	values = scan(false)
	if len(values) != 5 || slices.ContainsFunc(values, func(value asset.Asset) bool { return value.Identity.NativeID == target }) {
		t.Fatal("explicit native absence failed to close the requested pool", len(values))
	}
	payload, err := os.ReadFile(database)
	if err != nil || bytes.Contains(payload, []byte("password-canary")) {
		t.Fatal("native credential persisted", err)
	}
}

func TestSynapseKnownInventoryCursorAndIdentity(t *testing.T) {
	f := newSynapseInventoryFixture(t)
	request := productRequest(f.runtime, synapseSQLType)
	first := strings.ToLower(resourceID(synapseType, "first")) + "/sqlpools/pool"
	second := strings.ToLower(resourceID(synapseType, "second")) + "/sqlpools/pool"
	for _, ids := range [][]string{{first, first}, {strings.ToUpper(first)}, {strings.Replace(first, testSubscription, testTenant, 1)}, {strings.TrimSuffix(first, "/sqlpools/pool")}} {
		request.KnownNativeIDs = ids
		if _, err := f.runtime.List(t.Context(), request); err == nil {
			t.Fatal("invalid known IDs accepted", ids)
		}
	}
	request.KnownNativeIDs, request.Limit = []string{first, second}, 1
	batch, err := f.runtime.List(t.Context(), request)
	if err != nil || batch.Complete || len(batch.Items) != 1 {
		t.Fatal(batch, err)
	}
	request.Cursor = batch.NextCursor
	batch, err = f.runtime.List(t.Context(), request)
	if err != nil || !batch.Complete || len(batch.Items) != 1 || batch.Items[0].NativeID != second {
		t.Fatal("known cursor did not advance", batch, err)
	}
	object(f.objects[second]["properties"])["collation"] = "new-collation"
	if _, err := f.runtime.List(t.Context(), request); err == nil {
		t.Fatal("known cursor accepted changed inventory")
	}
}

func TestSynapseInventoryPreservesManagementLock(t *testing.T) {
	f := newSynapseInventoryFixture(t)
	root := "/subscriptions/" + testSubscription
	f.override = func(req *http.Request) (*http.Response, bool) {
		if strings.EqualFold(req.URL.Path, root+"/providers/Microsoft.Authorization/locks") {
			return jsonResponse(200, map[string]any{"value": []any{map[string]any{"id": root + "/providers/Microsoft.Authorization/locks/keep", "properties": map[string]any{"level": "CanNotDelete"}}}}, nil), true
		}
		return nil, false
	}
	request := productRequest(f.runtime, synapseType)
	request.Limit = 1
	batch, err := f.runtime.List(t.Context(), request)
	if err != nil || len(batch.Items) != 1 || batch.Items[0].Normalized["cleanup_protection_reason"] != "azure_management_lock" {
		t.Fatal("management lock hidden by incomplete cleanup", batch, err)
	}
}
