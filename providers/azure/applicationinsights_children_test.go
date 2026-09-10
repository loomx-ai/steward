package azure

import (
	"encoding/json"
	"maps"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

type insightsARMInventoryFixture struct {
	*insightsInventoryFixture
	hold         bool
	deleteStatus int
	deleteBody   any
	deleteHeader http.Header
	overrideBody bool
	reads        int
	response     func(*http.Request) (*http.Response, bool)
}

// Preserve native response fields, including friendly names, creation dates,
// permission paths and the documented ServiceProfiler name discrepancy. Only
// the example's subscription/group/component scope is explicitly substituted.
func insightsScopedExample(t *testing.T, file, parent string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile("fixtures/applicationinsights/" + file)
	if err != nil {
		t.Fatal(err)
	}
	var example map[string]any
	if err := json.Unmarshal(raw, &example); err != nil {
		t.Fatal(err)
	}
	params := object(example["parameters"])
	parts := strings.Split(parent, "/")
	adapted := strings.NewReplacer(text(params["subscriptionId"]), parts[2], text(params["resourceGroupName"]), parts[4], text(params["resourceName"]), parts[8]).Replace(string(raw))
	if err := json.Unmarshal([]byte(adapted), &example); err != nil {
		t.Fatal(err)
	}
	return object(object(object(example["responses"])["200"])["body"])
}

func newInsightsARMInventoryFixture(t *testing.T) *insightsARMInventoryFixture {
	t.Helper()
	f := &insightsARMInventoryFixture{insightsInventoryFixture: newInsightsInventoryFixture(t), deleteStatus: 200}
	f.children = map[string]map[string]any{}
	keys := insightsScopedExample(t, "stable/2015-05-01/examples/APIKeysList.json", f.parentID)
	for _, value := range array(keys["value"]) {
		raw := object(value)
		id, _, _ := parseID(text(raw["id"]))
		f.children[id] = raw
	}
	linked := insightsScopedExample(t, "preview/2020-03-01-preview/examples/ComponentLinkedStorageAccountsGet.json", f.parentID)
	id, _, _ := parseID(text(linked["id"]))
	f.children[id] = linked
	f.override = func(req *http.Request) (*http.Response, bool) {
		if f.response != nil {
			if response, ok := f.response(req); ok {
				return response, true
			}
		}
		path := strings.ToLower(req.URL.Path)
		if path == f.parentID+"/apikeys" {
			if req.Method != "GET" || req.URL.Query().Get("api-version") != insightsLegacyVersion || len(req.URL.Query()) != 1 {
				t.Fatal("unexpected native API key list", req.Method, req.URL)
			}
			f.childLists++
			values := []any{}
			for _, id := range slices.Sorted(maps.Keys(f.children)) {
				if strings.Contains(id, "/apikeys/") {
					values = append(values, f.children[id])
				}
			}
			return jsonResponse(200, map[string]any{"value": values}, nil), true
		}
		id, _, kind, _, err := insightsChildIdentity(path)
		if err != nil {
			return nil, false
		}
		if req.URL.Query().Get("api-version") != insightsChildVersion(kind) || len(req.URL.Query()) != 1 {
			t.Fatal("native child version or parameters changed", req.URL)
		}
		if kind == insightsLinkedStorageType && last(req.URL.Path) != "ServiceProfiler" {
			t.Fatal("native enum spelling was lost", req.URL)
		}
		raw, exists := f.children[id]
		if req.Method == "GET" {
			f.reads++
			if !exists {
				return jsonResponse(404, map[string]any{}, nil), true
			}
			return jsonResponse(200, raw, nil), true
		}
		if req.Method != "DELETE" {
			t.Fatal("unexpected native child write", req.Method)
		}
		if req.Header.Get("If-Match") != "" {
			t.Fatal("unsupported conditional request")
		}
		f.deletes = append(f.deletes, id)
		if !f.hold {
			delete(f.children, id)
		}
		body := f.deleteBody
		if !f.overrideBody && kind == insightsAPIKeyType {
			body = raw
		}
		if body == nil {
			return &http.Response{StatusCode: f.deleteStatus, Header: f.deleteHeader, Body: http.NoBody}, true
		}
		return jsonResponse(f.deleteStatus, body, f.deleteHeader), true
	}
	return f
}

func insightsARMAction(t *testing.T, f *insightsARMInventoryFixture, kind string) (contracts.ActionDriver, contracts.ActionRequest) {
	t.Helper()
	request := productRequest(f.runtime, kind)
	page, err := f.runtime.List(t.Context(), request)
	if err != nil || len(page.Items) == 0 {
		t.Fatal("native inventory unavailable", err)
	}
	item := page.Items[0]
	value := asset.Asset{ID: "native-child", Identity: asset.Identity{Provider: asset.ProviderAzure, ConnectionID: request.ConnectionID, Partition: "azure", NativeType: kind, NativeID: item.NativeID}, Location: item.Location, Normalized: item.Normalized}
	driver, err := f.runtime.ResolveAction(t.Context(), request.ConnectionID, value)
	if err != nil {
		t.Fatal(err)
	}
	return driver, contracts.ActionRequest{Asset: value, Action: "delete", IdempotencyKey: "native-child-delete"}
}

func TestApplicationInsightsARMChildInventory(t *testing.T) {
	for _, kind := range []string{insightsAPIKeyType, insightsLinkedStorageType} {
		t.Run(kind, func(t *testing.T) {
			f := newInsightsARMInventoryFixture(t)
			request := productRequest(f.runtime, kind)
			page, err := f.runtime.List(t.Context(), request)
			want := 2
			if kind == insightsLinkedStorageType {
				want = 1
			}
			if err != nil || !page.Complete || len(page.Items) != want {
				t.Fatal("native children incomplete", page, err)
			}
			for _, item := range page.Items {
				if item.NativeType != kind || item.Location != "eastus" || item.Actionable == nil || !*item.Actionable || item.Name == last(item.NativeID) || item.Normalized[insightsChildProofKey(kind)] == "" || !slices.Equal(item.NativeAliases, []string{item.NativeID}) {
					t.Fatal("native friendly name or action identity lost", item)
				}
				if kind == insightsAPIKeyType {
					if !slices.Equal(item.NetworkReferences, []string{f.parentID}) || item.Raw["type"] != nil || item.Raw["properties"] != nil || item.Raw["createdDate"] == nil {
						t.Fatal("flat key was rewritten or permission paths became assets", item)
					}
				} else {
					target, _, _ := parseID(text(object(item.Raw["properties"])["linkedStorageAccount"]))
					if len(item.NetworkReferences) != 2 || !slices.Contains(item.NetworkReferences, target) {
						t.Fatal("linked storage reference lost", item)
					}
				}
			}
			request.Source = ""
			if page, err := f.runtime.List(t.Context(), request); err != nil || len(page.Items) != want {
				t.Fatal("typed discovery escaped native protocol", err)
			}
			request.Source = inventorySource
			if page, err := f.runtime.List(t.Context(), request); err != nil || !page.Complete || len(page.Items) != 0 {
				t.Fatal("broad inventory overwrote native proof", err)
			}
		})
	}
}

func TestApplicationInsightsARMChildDeleteLifecycle(t *testing.T) {
	for _, kind := range []string{insightsAPIKeyType, insightsLinkedStorageType} {
		t.Run(kind, func(t *testing.T) {
			f := newInsightsARMInventoryFixture(t)
			driver, request := insightsARMAction(t, f, kind)
			f.hold = true
			result, err := driver.Execute(t.Context(), request)
			if err != nil || len(f.deletes) != 1 {
				t.Fatal("native delete failed", result, err)
			}
			encoded, _ := json.Marshal(result)
			var restored contracts.ActionResult
			if json.Unmarshal(encoded, &restored) != nil || strings.Contains(string(encoded), "linkedReadProperties") {
				t.Fatal("receipt did not preserve private operation identity")
			}
			driver, err = f.runtime.ResolveAction(t.Context(), request.Asset.Identity.ConnectionID, request.Asset)
			if err != nil {
				t.Fatal(err)
			}
			if wait, err := driver.Wait(t.Context(), request, restored); err != nil || wait.Done {
				t.Fatal("HTTP success hid surviving native child", wait, err)
			}
			delete(f.children, request.Asset.Identity.NativeID)
			if wait, err := driver.Wait(t.Context(), request, restored); err != nil || !wait.Done {
				t.Fatal("native absence failed after restart", wait, err)
			}
			if _, err := driver.Execute(t.Context(), request); err != nil || len(f.deletes) != 1 {
				t.Fatal("absent child was deleted again", err)
			}
			request.Asset.Normalized[insightsChildProofKey(kind)] = "another-review"
			if _, err := driver.Wait(t.Context(), request, restored); err == nil {
				t.Fatal("receipt survived configuration change")
			}
		})
	}
}

func TestApplicationInsightsAPIKeyInventoryBoundaries(t *testing.T) {
	for _, mode := range []string{"list-403", "list-404", "list-206", "list-next", "list-duplicate", "list-foreign", "list-missing-id", "get-404", "get-206", "get-code", "get-lro", "get-identity", "get-type", "get-private-drift"} {
		t.Run(mode, func(t *testing.T) {
			f := newInsightsARMInventoryFixture(t)
			f.response = func(req *http.Request) (*http.Response, bool) {
				path := strings.ToLower(req.URL.Path)
				isList := path == f.parentID+"/apikeys"
				if strings.HasPrefix(mode, "list-") && !isList || strings.HasPrefix(mode, "get-") && !strings.Contains(path, "/apikeys/") {
					return nil, false
				}
				var raw map[string]any
				for id, value := range f.children {
					if strings.Contains(id, "/apikeys/") && (isList || id == path) {
						raw = maps.Clone(value)
						break
					}
				}
				switch mode {
				case "list-403":
					return jsonResponse(403, nil, nil), true
				case "list-404", "get-404":
					return jsonResponse(404, nil, nil), true
				case "list-206":
					return jsonResponse(206, map[string]any{"value": []any{raw}}, nil), true
				case "list-next":
					return jsonResponse(200, map[string]any{"value": []any{raw}, "nextLink": req.URL.String() + "&skiptoken=next"}, nil), true
				case "list-duplicate":
					return jsonResponse(200, map[string]any{"value": []any{raw, raw}}, nil), true
				case "list-foreign":
					raw["id"] = strings.Replace(text(raw["id"]), testSubscription, testTenant, 1)
				case "list-missing-id":
					delete(raw, "id")
				case "get-206":
					return jsonResponse(206, raw, nil), true
				case "get-code":
					raw["code"] = "UnexpectedResult"
				case "get-lro":
					return jsonResponse(200, raw, http.Header{"Azure-Asyncoperation": {apiURL(f.parentID, insightsComponentVersion)}}), true
				case "get-identity":
					raw["id"] = text(raw["id"]) + "-replacement"
				case "get-type":
					raw["type"] = storageType
				case "get-private-drift":
					raw["linkedReadProperties"] = []any{}
				}
				if isList {
					return jsonResponse(200, map[string]any{"value": []any{raw}}, nil), true
				}
				return jsonResponse(200, raw, nil), true
			}
			if page, err := f.runtime.List(t.Context(), productRequest(f.runtime, insightsAPIKeyType)); err == nil || len(page.Items) != 0 || len(f.deletes) != 0 {
				t.Fatal("partial or changed API key inventory was accepted", mode, page, err)
			}
		})
	}
}

func TestApplicationInsightsLinkedStorageSingletonBoundaries(t *testing.T) {
	for _, mode := range []string{"absent", "parent-absent", "parent-changed", "membership-changed", "type-missing", "target-missing", "target-wrong-type", "foreign-storage", "get-403", "get-204"} {
		t.Run(mode, func(t *testing.T) {
			f := newInsightsARMInventoryFixture(t)
			id := f.parentID + "/linkedstorageaccounts/serviceprofiler"
			if mode == "absent" || mode == "parent-absent" || mode == "parent-changed" {
				delete(f.children, id)
			}
			switch mode {
			case "type-missing":
				delete(f.children[id], "type")
			case "target-missing":
				delete(object(f.children[id]["properties"]), "linkedStorageAccount")
			case "target-wrong-type":
				object(f.children[id]["properties"])["linkedStorageAccount"] = resourceID(vmType, "wrong")
			case "foreign-storage":
				object(f.children[id]["properties"])["linkedStorageAccount"] = strings.Replace(resourceID(storageType, "external"), testSubscription, testTenant, 1)
			}
			f.response = func(req *http.Request) (*http.Response, bool) {
				path := strings.ToLower(req.URL.Path)
				if path == f.parentID && f.reads > 0 {
					if mode == "parent-absent" {
						return jsonResponse(404, nil, nil), true
					}
					if mode == "parent-changed" {
						object(f.parent["properties"])["AppId"] = "replacement"
					}
				}
				if path == id {
					if mode == "membership-changed" && f.reads > 0 {
						return jsonResponse(404, nil, nil), true
					}
					if mode == "get-403" {
						return jsonResponse(403, nil, nil), true
					}
					if mode == "get-204" {
						return &http.Response{StatusCode: 204, Header: http.Header{}, Body: http.NoBody}, true
					}
				}
				return nil, false
			}
			page, err := f.runtime.List(t.Context(), productRequest(f.runtime, insightsLinkedStorageType))
			if mode == "absent" || mode == "foreign-storage" {
				want := 0
				if mode == "foreign-storage" {
					want = 1
				}
				if err != nil || !page.Complete || len(page.Items) != want {
					t.Fatal("native singleton boundary rejected", page, err)
				}
			} else if err == nil || len(page.Items) != 0 {
				t.Fatal("invalid singleton became authoritative", page, err)
			}
		})
	}
}

func TestApplicationInsightsARMChildActionProtectionAndDrift(t *testing.T) {
	for _, kind := range []string{insightsAPIKeyType, insightsLinkedStorageType} {
		for _, mode := range []string{"parent", "group-tag", "managed-group", "lock", "configuration", "creation-date", "name", "parent-missing", "read-403", "final-read-drift", "proof-missing", "cross-connection"} {
			t.Run(kind+"/"+mode, func(t *testing.T) {
				f := newInsightsARMInventoryFixture(t)
				driver, request := insightsARMAction(t, f, kind)
				id := request.Asset.Identity.NativeID
				switch mode {
				case "parent":
					object(f.parent["properties"])["AppId"] = "replacement"
				case "group-tag":
					f.group["tags"] = map[string]any{"steward:protected": "true"}
				case "managed-group":
					f.group["managedBy"] = resourceID(applicationInsightsType, "controller")
				case "lock":
					f.locks = []any{map[string]any{"id": f.parentID + "/providers/Microsoft.Authorization/locks/protect", "properties": map[string]any{"level": "CanNotDelete"}}}
				case "configuration":
					if kind == insightsAPIKeyType {
						f.children[id]["linkedReadProperties"] = []any{}
					} else {
						object(f.children[id]["properties"])["linkedStorageAccount"] = resourceID(storageType, "replacement")
					}
				case "creation-date":
					f.children[id]["createdDate"] = "replacement date"
				case "name":
					f.children[id]["name"] = "replacement name"
				case "proof-missing":
					delete(request.Asset.Normalized, insightsChildProofKey(kind))
				case "cross-connection":
					request.Asset.Identity.ConnectionID = "another-connection"
				}
				f.reads = 0
				f.response = func(req *http.Request) (*http.Response, bool) {
					path := strings.ToLower(req.URL.Path)
					if mode == "parent-missing" && path == f.parentID {
						return jsonResponse(404, nil, nil), true
					}
					if req.Method == "GET" && path == id {
						if mode == "read-403" {
							return jsonResponse(403, nil, nil), true
						}
						if mode == "final-read-drift" && f.reads > 0 {
							f.children[id]["name"] = "replaced during preflight"
						}
					}
					return nil, false
				}
				if check, err := driver.Preflight(t.Context(), request); err == nil && check.Allowed {
					t.Fatal("protected or changed child passed preflight", check)
				}
				if _, err := driver.Execute(t.Context(), request); err == nil || len(f.deletes) != 0 {
					t.Fatal("protected or changed child was mutated", err)
				}
			})
		}
	}
}

func TestApplicationInsightsARMChildDeleteResponseBoundaries(t *testing.T) {
	for _, kind := range []string{insightsAPIKeyType, insightsLinkedStorageType} {
		for _, mode := range []string{"204", "202", "206", "lro", "wrong-body", "changed-body", "code", "not-found"} {
			t.Run(kind+"/"+mode, func(t *testing.T) {
				f := newInsightsARMInventoryFixture(t)
				driver, request := insightsARMAction(t, f, kind)
				switch mode {
				case "204":
					f.deleteStatus, f.overrideBody = 204, true
				case "202":
					f.deleteStatus = 202
				case "206":
					f.deleteStatus = 206
				case "lro":
					f.deleteHeader = http.Header{"Location": {apiURL(f.parentID, insightsComponentVersion)}}
				case "wrong-body":
					f.deleteBody, f.overrideBody = map[string]any{"id": resourceID(storageType, "wrong")}, true
				case "changed-body":
					raw := maps.Clone(f.children[request.Asset.Identity.NativeID])
					raw["name"] = "replacement"
					f.deleteBody, f.overrideBody = raw, true
				case "code":
					f.deleteBody, f.overrideBody = map[string]any{"code": "Failed"}, true
				case "not-found":
					f.deleteStatus, f.overrideBody = 404, true
				}
				result, err := driver.Execute(t.Context(), request)
				valid := mode == "not-found" || mode == "204" && kind == insightsLinkedStorageType
				if valid {
					if err != nil {
						t.Fatal("documented native deletion rejected", err)
					}
					if wait, err := driver.Wait(t.Context(), request, result); err != nil || !wait.Done {
						t.Fatal("native deletion did not establish absence", wait, err)
					}
				} else if err == nil {
					t.Fatal("undocumented native deletion accepted", result)
				}
			})
		}
	}
}

func TestApplicationInsightsARMChildProjectionGraphAndAction(t *testing.T) {
	f := newInsightsARMInventoryFixture(t)
	storage := nativeResource(storageType, "storageaccountname", "westus", map[string]any{})
	retained := dnsAsset(t, f.runtime, storage)
	testInsightsInventoryPipeline(t, f.insightsInventoryFixture, []string{applicationInsightsType, insightsAPIKeyType, insightsLinkedStorageType}, []asset.Asset{retained})
}

func TestApplicationInsightsAPIKeyCursorPrivateBindings(t *testing.T) {
	for _, mode := range []string{"unchanged", "id-case", "secret", "opaque", "permissions", "created", "added", "removed", "parent", "kind"} {
		t.Run(mode, func(t *testing.T) {
			f := newInsightsARMInventoryFixture(t)
			for _, raw := range f.children {
				raw["apiKey"], raw["API_KEY"] = "PRIVATE_API_KEY", "PRIVATE_CASE_KEY"
				raw["configuration"] = map[string]any{"opaque": "PRIVATE_CONFIGURATION"}
			}
			request := productRequest(f.runtime, insightsAPIKeyType)
			request.Limit = 1
			page, err := f.runtime.List(t.Context(), request)
			if err != nil || page.Complete || len(page.Items) != 1 || page.NextCursor == "" {
				t.Fatal("native API key pagination unavailable", page, err)
			}
			encoded, _ := json.Marshal(page)
			if strings.Contains(string(encoded), "PRIVATE_") {
				t.Fatal("native private key or opaque configuration escaped inventory")
			}
			id := page.Items[0].NativeID
			switch mode {
			case "id-case":
				f.children[id]["id"] = strings.ToUpper(id)
			case "secret":
				f.children[id]["apiKey"] = "replacement secret"
			case "opaque":
				object(f.children[id]["configuration"])["opaque"] = "replacement configuration"
			case "permissions":
				f.children[id]["linkedReadProperties"] = []any{}
			case "created":
				f.children[id]["createdDate"] = "another generation"
			case "added":
				added := maps.Clone(f.children[id])
				newID := f.parentID + "/apikeys/00000000-0000-0000-0000-000000000001"
				added["id"] = newID
				f.children[newID] = added
			case "removed":
				delete(f.children, id)
			case "parent":
				object(f.parent["properties"])["AppId"] = "replacement parent"
			case "kind":
				request.ResourceKind = productRequest(f.runtime, insightsLinkedStorageType).ResourceKind
			}
			request.Cursor = page.NextCursor
			continued, err := f.runtime.List(t.Context(), request)
			if mode == "unchanged" || mode == "id-case" {
				if err != nil || !continued.Complete || len(continued.Items) != 1 || continued.Items[0].NativeID == id {
					t.Fatal("stable native snapshot did not resume", continued, err)
				}
			} else if err == nil || len(continued.Items) != 0 {
				t.Fatal("changed native inventory reused a cursor", continued, err)
			}
		})
	}
}

func TestApplicationInsightsARMChildIdentityAndOperationBoundaries(t *testing.T) {
	f := newInsightsARMInventoryFixture(t)
	c, err := f.runtime.resolve(t.Context(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{insightsAPIKeyType, insightsLinkedStorageType} {
		mapping, _ := findType(kind)
		var id string
		for key := range f.children {
			_, _, typ, _, _ := insightsChildIdentity(key)
			if typ == kind {
				id = key
				break
			}
		}
		for _, bad := range []string{id + "/nested/name", id + "?extra=1", id + "%2f", " " + id, strings.Replace(id, testSubscription, testTenant, 1), f.parentID + "/linkedstorageaccounts/other"} {
			if request, err := c.insightsChildRequest(mapping, bad, "DELETE"); err == nil {
				t.Fatal("invalid child identity bound a native mutation", request)
			}
		}
		request, err := c.insightsChildRequest(mapping, id, "GET")
		if err != nil {
			t.Fatal(err)
		}
		for _, endpoint := range []string{request.URL + "&extra=1", request.URL + "&api-version=" + insightsChildVersion(kind), strings.Replace(request.URL, insightsChildVersion(kind), "different", 1), request.URL + "#", strings.Replace(request.URL, "management.azure.com", "evil.invalid", 1)} {
			if err := c.insightsChildEndpoint(endpoint, id); err == nil {
				t.Fatal("changed native endpoint accepted", endpoint)
			}
		}
		mapping.DeleteOperations = []string{insightsOperationPrefix + "Components_Delete"}
		if _, err := c.insightsChildRequest(mapping, id, "DELETE"); err == nil {
			t.Fatal("child action rebound to component deletion")
		}
	}
}

func TestApplicationInsightsAPIKeyOfficialGetAndDeleteBodies(t *testing.T) {
	f := newInsightsARMInventoryFixture(t)
	read := insightsScopedExample(t, "stable/2015-05-01/examples/APIKeysGet.json", f.parentID)
	deleted := insightsScopedExample(t, "stable/2015-05-01/examples/APIKeysDelete.json", f.parentID)
	id, _, _ := parseID(text(read["id"]))
	f.response = func(req *http.Request) (*http.Response, bool) {
		if req.Method == "GET" && strings.EqualFold(req.URL.Path, id) {
			return jsonResponse(200, read, nil), true
		}
		return nil, false
	}
	driver, request := insightsARMAction(t, f, insightsAPIKeyType)
	if request.Asset.Identity.NativeID != id {
		t.Fatal("official example key was not selected")
	}
	f.deleteBody, f.overrideBody = deleted, true
	if _, err := driver.Execute(t.Context(), request); err != nil || len(f.deletes) != 1 {
		t.Fatal("official flat deletion body rejected", err)
	}
}
