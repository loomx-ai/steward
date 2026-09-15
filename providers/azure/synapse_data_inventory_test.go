package azure

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

type synapseDataInventoryFixture struct {
	*synapseTransportFixture
	items                     map[string]map[string]any
	hidden                    map[string]bool
	hideWorkspaces, hidePools bool
	intercept                 func(*http.Request) (*http.Response, bool)
}

var synapseDataKinds = []string{synapseBatchType, synapseSessionType, synapseNotebookType, synapseJobDefinitionType}

func newSynapseDataInventoryFixture(t *testing.T) *synapseDataInventoryFixture {
	t.Helper()
	f := &synapseDataInventoryFixture{synapseTransportFixture: newSynapseTransportFixture(t), items: map[string]map[string]any{}, hidden: map[string]bool{}}
	delete(object(f.workspace["properties"]), "defaultDataLakeStorage")
	for _, kind := range synapseDataKinds {
		d := synapseDataKind(kind)
		f.items[kind] = f.item(d.read)
		if d.spark {
			f.items[kind]["schedulerInfo"] = map[string]any{"submittedAt": "2025-02-24T09:47:41Z", "currentState": "Scheduled"}
		}
		if kind == synapseJobDefinitionType {
			props := object(f.items[kind]["properties"])
			props["targetBigDataPool"] = props["bigDataPool"]
			delete(props, "bigDataPool")
		}
	}
	f.override = func(q *http.Request) (*http.Response, bool) {
		if f.intercept != nil {
			if res, ok := f.intercept(q); ok {
				return res, true
			}
		}
		path := strings.ToLower(q.URL.Path)
		if q.URL.Host == "management.azure.com" {
			if path == "/subscriptions/"+testSubscription+"/resourcegroups/test" {
				return jsonResponse(200, map[string]any{"id": path, "name": "test", "type": groupType, "location": "eastus", "properties": map[string]any{}}, nil), true
			}
			if path == "/subscriptions/"+testSubscription+"/resourcegroups" || path == "/subscriptions/"+testSubscription+"/providers/microsoft.authorization/locks" {
				return jsonResponse(200, map[string]any{"value": []any{}}, nil), true
			}
			if path == strings.ToLower(text(f.workspace["id"]))+"/bigdatapools" {
				rows := []any{}
				if !f.hidePools {
					rows = append(rows, f.pool)
				}
				return jsonResponse(200, map[string]any{"value": rows}, nil), true
			}
			if f.hideWorkspaces && path == "/subscriptions/"+testSubscription+"/providers/microsoft.synapse/workspaces" {
				return jsonResponse(200, map[string]any{"value": []any{}}, nil), true
			}
		}
		return nil, false
	}
	f.data = func(q *http.Request) *http.Response {
		if q.URL.Path == "/pipelines" {
			return jsonResponse(200, map[string]any{"value": []any{}}, nil)
		}
		for _, kind := range synapseDataKinds {
			d := synapseDataKind(kind)
			collection := "/" + d.collection
			if d.spark {
				collection = "/livyApi/versions/" + synapseDataVersion + "/sparkPools/pool/" + d.collection
			}
			if !strings.EqualFold(q.URL.Path, collection) && !strings.HasPrefix(strings.ToLower(q.URL.Path), strings.ToLower(collection)+"/") {
				continue
			}
			raw := f.items[kind]
			header := http.Header{"X-Ms-Request-Id": {"synapse-data-inventory"}}
			if strings.EqualFold(q.URL.Path, collection) {
				rows := []any{}
				if raw != nil && !f.hidden[kind] {
					rows = append(rows, raw)
				}
				if d.spark {
					return jsonResponse(200, map[string]any{"from": 0, "total": len(rows), "sessions": rows}, header)
				}
				return jsonResponse(200, map[string]any{"value": rows}, header)
			}
			expected := "item"
			if d.spark {
				expected = "0"
			}
			if raw == nil || last(q.URL.Path) != expected {
				return jsonResponse(404, map[string]any{"error": map[string]any{"code": "NotFound"}}, header)
			}
			return jsonResponse(200, raw, header)
		}
		t.Fatal("unknown native data collection", q.URL)
		return nil
	}
	return f
}
func synapseDataInventoryRequest(r *Runtime, kind string) contracts.InventoryRequest {
	request := productRequest(r, kind)
	request.Source = synapseDataInventorySource
	return request
}

func TestSynapseDataInventoryAssetsReferencesAndPrivacy(t *testing.T) {
	for _, kind := range synapseDataKinds {
		t.Run(kind, func(t *testing.T) {
			f := newSynapseDataInventoryFixture(t)
			request := synapseDataInventoryRequest(f.runtime, kind)
			batch, err := f.runtime.List(t.Context(), request)
			if err != nil || !batch.Complete || len(batch.Items) != 1 || batch.RequestID != "synapse-data-inventory" {
				t.Fatal("missing data-plane inventory", batch, err)
			}
			item := batch.Items[0]
			d := synapseDataKind(kind)
			if item.NativeType != kind || item.Location != "eastus" || item.Actionable == nil || *item.Actionable || item.Normalized["_inventory_source"] != synapseDataInventorySource || item.Normalized["_synapse_connection"] != "connection" || item.Normalized["_synapse_private_configuration"] == "" {
				t.Fatal("invalid asset metadata", item)
			}
			expected := strings.ToLower(text(f.items[kind]["id"]))
			if d.spark {
				expected = "https://first.dev.azuresynapse.net/livyApi/versions/2020-12-01/sparkPools/pool/" + d.collection + "/0"
			}
			if item.NativeID != expected || !slices.Contains(item.NetworkReferences, strings.ToLower(text(f.workspace["id"]))) || !slices.Contains(item.NetworkReferences, strings.ToLower(text(f.pool["id"]))) {
				t.Fatal("wrong identity or dependencies", item.NativeID, item.NetworkReferences)
			}
			encoded, _ := json.Marshal(batch)
			if bytes.Contains(encoded, []byte("data-secret-canary")) {
				t.Fatal("code/config leaked into asset or cursor")
			}
			if d.spark {
				if object(item.Raw["livyInfo"])["jobCreationRequest"] != nil {
					t.Fatal("job configuration escaped")
				}
			} else if object(item.Raw["properties"])["cells"] != nil {
				t.Fatal("notebook cells escaped")
			}
			if _, err := f.runtime.ResolveAction(t.Context(), "connection", asset.Asset{Identity: asset.Identity{Provider: asset.ProviderAzure, ConnectionID: "connection", NativeID: item.NativeID, NativeType: kind}}); err == nil {
				t.Fatal("unfinished lifecycle became actionable")
			}
		})
	}
}

func TestSynapseDataInventoryKnownAbsence(t *testing.T) {
	for _, kind := range synapseDataKinds {
		t.Run(kind, func(t *testing.T) {
			f := newSynapseDataInventoryFixture(t)
			request := synapseDataInventoryRequest(f.runtime, kind)
			first, err := f.runtime.List(t.Context(), request)
			if err != nil {
				t.Fatal(err)
			}
			item := first.Items[0]
			request.KnownNativeIDs = []string{item.NativeID}
			request.KnownNativeMetadata = map[string]map[string]any{item.NativeID: item.Normalized}
			f.hideWorkspaces, f.hidePools = true, true
			f.hidden[kind] = true
			batch, err := f.runtime.List(t.Context(), request)
			if err != nil || len(batch.Items) != 1 || len(batch.AbsentNativeIDs) != 0 {
				t.Fatal("hidden known resource disappeared", batch, err)
			}
			f.intercept = func(q *http.Request) (*http.Response, bool) {
				if q.URL.Host == "first.dev.azuresynapse.net" && strings.HasSuffix(q.URL.Path, "/"+last(item.NativeID)) {
					return jsonResponse(403, nil, nil), true
				}
				return nil, false
			}
			if batch, err = f.runtime.List(t.Context(), request); err == nil || len(batch.AbsentNativeIDs) != 0 {
				t.Fatal("permission failure closed known asset", batch, err)
			}
			f.intercept = nil
			delete(f.items, kind)
			batch, err = f.runtime.List(t.Context(), request)
			if err != nil || len(batch.Items) != 0 || !slices.Equal(batch.AbsentNativeIDs, []string{item.NativeID}) {
				t.Fatal("own 404 did not close exact known identity", batch, err)
			}
		})
	}
}

func TestSynapseDataInventoryStablePagingAndDrift(t *testing.T) {
	for _, kind := range synapseDataKinds {
		t.Run(kind, func(t *testing.T) {
			f := newSynapseDataInventoryFixture(t)
			d := synapseDataKind(kind)
			first := batchClone(f.items[kind])
			second := batchClone(first)
			if d.spark {
				second["id"] = 1
			} else {
				second["id"] = strings.TrimSuffix(text(second["id"]), "item") + "other"
				second["name"] = "other"
			}
			collection := "/" + d.collection
			if d.spark {
				collection = "/livyApi/versions/" + synapseDataVersion + "/sparkPools/pool/" + d.collection
			}
			f.intercept = func(q *http.Request) (*http.Response, bool) {
				if q.URL.Host != "first.dev.azuresynapse.net" {
					return nil, false
				}
				if q.URL.Path == collection {
					row := first
					offset := 0
					if q.URL.Query().Get("from") == "1" || q.URL.Query().Get("continuationToken") == "next" {
						row = second
						offset = 1
					}
					if d.spark {
						return jsonResponse(200, map[string]any{"from": offset, "total": 2, "sessions": []any{row}}, nil), true
					}
					body := map[string]any{"value": []any{row}}
					if offset == 0 {
						body["nextLink"] = "https://first.dev.azuresynapse.net" + collection + "?api-version=" + synapseDataVersion + "&continuationToken=next"
					}
					return jsonResponse(200, body, nil), true
				}
				tail := "other"
				if d.spark {
					tail = "1"
				}
				if q.URL.Path == collection+"/"+tail {
					return jsonResponse(200, second, nil), true
				}
				return nil, false
			}
			request := synapseDataInventoryRequest(f.runtime, kind)
			request.Limit = 1
			page, err := f.runtime.List(t.Context(), request)
			if err != nil || page.Complete || len(page.Items) != 1 || page.NextCursor == "" {
				t.Fatal("client pagination missing", page, err)
			}
			request.Cursor = page.NextCursor
			lastPage, err := f.runtime.List(t.Context(), request)
			if err != nil || !lastPage.Complete || len(lastPage.Items) != 1 || lastPage.Items[0].NativeID == page.Items[0].NativeID {
				t.Fatal("client pagination skipped or repeated an asset", lastPage, err)
			}
			if d.spark {
				object(second["livyInfo"])["jobCreationRequest"] = map[string]any{"args": []any{"changed"}}
			} else {
				object(second["properties"])["cells"] = []any{map[string]any{"source": []any{"changed"}}}
			}
			if _, err = f.runtime.List(t.Context(), request); err == nil {
				t.Fatal("changed private configuration accepted across cursor")
			}
		})
	}
}

func TestSynapseDataRegisteredScanReconciliation(t *testing.T) {
	f := newSynapseDataInventoryFixture(t)
	repository, registry, database := azureNativeWorkerRepository(t, f.runtime)
	values := azureNativeWorkerScan(t, f.runtime, synapseSource, repository, registry, []string{synapseType, synapseSparkType}, false, false)
	if len(values) != 2 {
		t.Fatal("missing ARM parents", len(values))
	}
	scan := func(failure bool) []asset.Asset {
		return azureNativeWorkerScan(t, f.runtime, synapseDataInventorySource, repository, registry, synapseDataKinds, failure, false)
	}
	values = scan(false)
	if len(values) != 6 {
		t.Fatal("registered data assets missing", len(values))
	}
	parents := map[asset.AssetID]bool{}
	for _, value := range values {
		if value.Identity.NativeType == synapseType || value.Identity.NativeType == synapseSparkType {
			parents[value.ID] = true
		}
	}
	for _, value := range values {
		if synapseDataKind(value.Identity.NativeType).kind == "" {
			continue
		}
		edges, err := repository.Graph().ListRelationships(t.Context(), value.ID)
		if err != nil || len(edges) != 2 {
			t.Fatal("native workspace/pool graph edges missing", value.Identity.NativeType, edges, err)
		}
		for _, edge := range edges {
			if edge.SourceAssetID != value.ID || !parents[edge.TargetAssetID] || edge.Type != graph.RelationshipUses {
				t.Fatal("wrong native dependency edge", edge)
			}
		}
	}
	f.hideWorkspaces, f.hidePools = true, true
	for _, kind := range synapseDataKinds {
		f.hidden[kind] = true
	}
	if len(scan(false)) != 6 {
		t.Fatal("hidden parent indexes erased assets")
	}
	f.intercept = func(q *http.Request) (*http.Response, bool) {
		if q.URL.Host == "first.dev.azuresynapse.net" && strings.HasSuffix(q.URL.Path, "/notebooks/item") {
			return jsonResponse(403, nil, nil), true
		}
		return nil, false
	}
	if len(scan(true)) != 6 {
		t.Fatal("failed data read erased an asset")
	}
	f.intercept = nil
	delete(f.items, synapseNotebookType)
	values = scan(false)
	if len(values) != 5 {
		t.Fatal("native notebook 404 did not reconcile precisely", len(values))
	}
	for _, v := range values {
		if synapseDataKind(v.Identity.NativeType).kind != "" && v.Capabilities.Has(asset.CapabilityActionable) {
			t.Fatal("unfinished data cleanup enabled")
		}
	}
	persisted, err := os.ReadFile(database)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(persisted, []byte("data-secret-canary")) {
		t.Fatal("private data persisted")
	}
}

// Keep case-sensitive selectors, while rejecting foreign identities and cursor
// metadata from a different connection before they can authorize a data request.
func TestSynapseDataIdentityBoundaries(t *testing.T) {
	c := &client{subscription: testSubscription}
	for _, kind := range synapseDataKinds {
		d := synapseDataKind(kind)
		valid := strings.ToLower(resourceID(synapseType, "first")) + "/" + strings.ToLower(d.collection) + "/item"
		if d.spark {
			valid = "https://first.dev.azuresynapse.net/livyApi/versions/2020-12-01/sparkPools/PoolCase/" + d.collection + "/0"
		}
		canonical, params, err := c.synapseDataIdentity(valid, kind)
		if err != nil || canonical != valid {
			t.Fatal(valid, err)
		}
		if d.spark && params["sparkPoolName"] != "PoolCase" {
			t.Fatal("native pool selector changed case")
		}
		for _, bad := range []string{valid + "?api-version=other", valid + "/../other", strings.Replace(valid, "/item", "/item%2fother", 1), strings.Replace(valid, testSubscription, testTenant, 1)} {
			if bad == valid {
				continue
			}
			if _, _, err := c.synapseDataIdentity(bad, kind); err == nil {
				t.Fatal("invalid native identity accepted", bad)
			}
		}
	}
}

func TestSynapseDataInventoryRejectsChangingIndexesAndParents(t *testing.T) {
	for _, kind := range synapseDataKinds {
		t.Run(kind, func(t *testing.T) {
			for _, fault := range []string{"index changed", "duplicate", "listed detail changed", "parent changed", "forbidden list"} {
				t.Run(fault, func(t *testing.T) {
					f := newSynapseDataInventoryFixture(t)
					d := synapseDataKind(kind)
					lists := 0
					collection := "/" + d.collection
					if d.spark {
						collection = "/livyApi/versions/" + synapseDataVersion + "/sparkPools/pool/" + d.collection
					}
					f.intercept = func(q *http.Request) (*http.Response, bool) {
						if q.URL.Host != "first.dev.azuresynapse.net" {
							return nil, false
						}
						if q.URL.Path == collection {
							lists++
							raw := batchClone(f.items[kind])
							rows := []any{raw}
							switch fault {
							case "forbidden list":
								return jsonResponse(403, nil, nil), true
							case "duplicate":
								rows = append(rows, raw)
							case "index changed":
								if lists > 1 {
									rows = []any{}
								}
							case "listed detail changed":
								if d.spark {
									raw["name"] = "different"
								} else {
									object(raw["properties"])["newProperty"] = "changed"
								}
							case "parent changed":
								object(f.workspace["properties"])["workspaceUID"] = "new-incarnation"
							}
							if d.spark {
								return jsonResponse(200, map[string]any{"from": 0, "total": len(rows), "sessions": rows}, nil), true
							}
							return jsonResponse(200, map[string]any{"value": rows}, nil), true
						}
						return nil, false
					}
					if batch, err := f.runtime.List(t.Context(), synapseDataInventoryRequest(f.runtime, kind)); err == nil || batch.Complete || len(batch.Items)+len(batch.AbsentNativeIDs) != 0 {
						t.Fatal("uncertain inventory escaped", fault, batch, err)
					}
				})
			}
		})
	}
}

func TestSynapseDataInventoryKnownMetadataAndScope(t *testing.T) {
	for _, kind := range synapseDataKinds {
		for _, fault := range []string{"connection", "endpoint", "parent", "duplicate", "unrelated metadata", "scope", "source", "options"} {
			t.Run(kind+"/"+fault, func(t *testing.T) {
				f := newSynapseDataInventoryFixture(t)
				request := synapseDataInventoryRequest(f.runtime, kind)
				first, err := f.runtime.List(t.Context(), request)
				if err != nil {
					t.Fatal(err)
				}
				item := first.Items[0]
				normalized := batchClone(item.Normalized)
				request.KnownNativeIDs = []string{item.NativeID}
				request.KnownNativeMetadata = map[string]map[string]any{item.NativeID: normalized}
				switch fault {
				case "connection":
					normalized["_synapse_connection"] = "foreign"
				case "endpoint":
					normalized["_synapse_endpoint"] = "https://foreign.dev.azuresynapse.net"
				case "parent":
					normalized["_synapse_workspace"] = strings.Replace(text(normalized["_synapse_workspace"]), testSubscription, testTenant, 1)
				case "duplicate":
					request.KnownNativeIDs = append(request.KnownNativeIDs, item.NativeID)
				case "unrelated metadata":
					request.KnownNativeMetadata["foreign"] = map[string]any{}
				case "scope":
					request.Scope = asset.Scope{Kind: asset.ScopeSubscription, NativeID: testTenant}
				case "source":
					request.Source = synapseSource
				case "options":
					request.Options = map[string]any{"filter": "anything"}
				}
				previous := f.dataCalls
				if _, err = f.runtime.List(t.Context(), request); err == nil || f.dataCalls != previous {
					t.Fatal("invalid selector metadata reached data plane", fault, err)
				}
			})
		}
	}
}

func TestSynapseArtifactDanglingPoolAndProtectedParent(t *testing.T) {
	for _, kind := range []string{synapseNotebookType, synapseJobDefinitionType} {
		t.Run(kind, func(t *testing.T) {
			f := newSynapseDataInventoryFixture(t)
			f.workspace["tags"] = map[string]any{"steward:protected": "true"}
			f.intercept = func(q *http.Request) (*http.Response, bool) {
				if strings.EqualFold(q.URL.Path, text(f.pool["id"])) {
					return jsonResponse(404, nil, nil), true
				}
				return nil, false
			}
			batch, err := f.runtime.List(t.Context(), synapseDataInventoryRequest(f.runtime, kind))
			if err != nil || len(batch.Items) != 1 || batch.Items[0].Normalized["_synapse_unresolved_pool"] != strings.ToLower(text(f.pool["id"])) {
				t.Fatal("dangling pool reference erased artifact", batch, err)
			}
			if batch.Items[0].Normalized["cleanup_protection_reason"] != "azure_protected_tag" {
				t.Fatal("parent protection lost", batch.Items[0].Normalized["cleanup_protection_reason"])
			}
			f.intercept = func(q *http.Request) (*http.Response, bool) {
				if strings.EqualFold(q.URL.Path, text(f.pool["id"])) {
					return jsonResponse(403, nil, nil), true
				}
				return nil, false
			}
			if _, err = f.runtime.List(t.Context(), synapseDataInventoryRequest(f.runtime, kind)); err == nil {
				t.Fatal("unreadable pool treated as dangling")
			}
		})
	}
}
