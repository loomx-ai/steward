package azure

import (
	"encoding/json"
	"maps"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func workbookExample(t *testing.T, file string) any {
	t.Helper()
	bytes, err := os.ReadFile("fixtures/applicationinsights/stable/" + file)
	if err != nil {
		t.Fatal(err)
	}
	// Keep all native response fields. Only the example subscription is bound
	// to this fixture's credential; malformed native example fields stay intact.
	bytes = []byte(strings.ReplaceAll(string(bytes), "6b643656-33eb-422f-aee8-3ac145d124af", testSubscription))
	var raw map[string]any
	decoder := json.NewDecoder(strings.NewReader(string(bytes)))
	decoder.UseNumber()
	if err := decoder.Decode(&raw); err != nil {
		t.Fatal(err)
	}
	return object(object(raw["responses"])["200"])["body"]
}

type workbookFixture struct {
	runtime        *Runtime
	kind, groupID  string
	group          map[string]any
	objects        map[string]map[string]any
	revisions      map[string]map[string]map[string]any
	armRows, locks []any
	queries        []url.Values
	deletes        []string
	calls          map[string]int
	override       func(*http.Request) (*http.Response, bool)
	deleteStatus   int
	deleteBody     any
	deleteHeader   http.Header
	hold           bool
}

func newWorkbookFixture(t *testing.T, kind string) *workbookFixture {
	t.Helper()
	f := &workbookFixture{kind: kind, objects: map[string]map[string]any{}, revisions: map[string]map[string]map[string]any{}, calls: map[string]int{}, armRows: []any{}, locks: []any{}, deleteStatus: 204}
	f.groupID = "/subscriptions/" + testSubscription + "/resourcegroups/my-resource-group"
	f.group = map[string]any{"id": f.groupID, "name": "my-resource-group", "type": groupType, "location": "westus", "tags": map[string]any{}, "properties": map[string]any{"provisioningState": "Succeeded"}}
	var rows []any
	if kind == insightsWorkbookTemplateType {
		rows = array(object(workbookExample(t, "2020-11-20/examples/WorkbookTemplatesList.json"))["value"])
	} else {
		file := "2023-06-01/examples/WorkbookGet.json"
		if kind == insightsMyWorkbookType {
			file = "2021-03-08/examples/MyWorkbookGet.json"
		}
		raw := object(workbookExample(t, file))
		other := maps.Clone(raw)
		// Compose a second identity using the other UUID in the original list.
		other["name"] = "c0deea5e-3344-40f2-96f8-6f8e1c3b5722"
		id, _, _ := parseID(text(raw["id"]))
		other["id"] = strings.TrimSuffix(id, last(id)) + text(other["name"])
		rows = []any{raw, other}
	}
	for _, row := range rows {
		raw := object(row)
		id, _, err := parseID(text(raw["id"]))
		if err != nil {
			t.Fatal(err)
		}
		// Give composed siblings independent nested values for mutation tests.
		bytes, _ := json.Marshal(raw)
		decoder := json.NewDecoder(strings.NewReader(string(bytes)))
		decoder.UseNumber()
		if err := decoder.Decode(&raw); err != nil {
			t.Fatal(err)
		}
		f.objects[id] = raw
		if kind == insightsWorkbookType {
			f.revisions[id] = map[string]map[string]any{}
			listed := array(object(workbookExample(t, "2023-06-01/examples/WorkbookRevisionsList.json"))["value"])
			content := object(object(workbookExample(t, "2023-06-01/examples/WorkbookRevisionGet.json"))["properties"])["serializedData"]
			for _, row := range listed {
				version := object(row)
				version["id"], version["name"] = id, last(id)
				object(version["properties"])["serializedData"] = content
				f.revisions[id][text(object(version["properties"])["revision"])] = version
			}
		}
	}
	f.runtime = protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
		path := strings.ToLower(req.URL.Path)
		f.calls[req.Method+" "+path]++
		if f.override != nil {
			if res, ok := f.override(req); ok {
				return res, nil
			}
		}
		if response, handled := emptyMonitorIndexResponse(t, req); handled {
			return response, nil
		}
		if req.URL.Host != "management.azure.com" {
			t.Fatal("foreign workbook endpoint", req.URL)
		}
		if path == "/subscriptions/"+testSubscription+"/resourcegroups" {
			return jsonResponse(200, map[string]any{"value": []any{f.group}}, nil), nil
		}
		if path == f.groupID {
			return jsonResponse(200, f.group, nil), nil
		}
		if path == "/subscriptions/"+testSubscription+"/resources" {
			return jsonResponse(200, map[string]any{"value": f.armRows}, nil), nil
		}
		if path == "/subscriptions/"+testSubscription+"/providers/microsoft.authorization/locks" {
			return jsonResponse(200, map[string]any{"value": f.locks}, nil), nil
		}
		if req.URL.Query().Get("api-version") != insightsWorkbookVersion(kind) {
			t.Fatal("wrong native workbook version", req.URL)
		}
		collection := "/subscriptions/" + testSubscription + "/providers/" + strings.ToLower(kind)
		if kind == insightsWorkbookTemplateType {
			collection = f.groupID + "/providers/" + strings.ToLower(kind)
		}
		if path == collection {
			if req.Method != "GET" {
				t.Fatal("unexpected workbook collection mutation")
			}
			query := req.URL.Query()
			if kind == insightsWorkbookTemplateType {
				if len(query) != 1 {
					t.Fatal("filtered template discovery", query)
				}
			} else if len(query) != 3 || query.Get("category") == "" || query.Get("canFetchContent") != "true" {
				t.Fatal("incomplete workbook discovery", query)
			}
			f.queries = append(f.queries, query)
			var rows []any
			for _, id := range slices.Sorted(maps.Keys(f.objects)) {
				raw := f.objects[id]
				if kind == insightsWorkbookTemplateType || insightsWorkbookCategory(text(object(raw["properties"])["category"])) == query.Get("category") {
					rows = append(rows, raw)
				}
			}
			if rows == nil {
				rows = []any{}
			}
			body := any(map[string]any{"value": rows})
			if kind == insightsMyWorkbookType {
				body = rows
			} // Published direct-array shape.
			return jsonResponse(200, body, http.Header{"X-Ms-Request-Id": {"workbook-native-list"}}), nil
		}
		if root, suffix, found := strings.Cut(path, "/revisions"); found {
			if kind != insightsWorkbookType || req.Method != "GET" || len(req.URL.Query()) != 1 {
				t.Fatal("invalid revision read", req.URL)
			}
			if suffix == "" {
				var rows []any
				for _, revision := range slices.Sorted(maps.Keys(f.revisions[root])) {
					raw := maps.Clone(f.revisions[root][revision])
					raw["properties"] = maps.Clone(object(raw["properties"]))
					object(raw["properties"])["serializedData"] = nil // Native revision summaries omit content.
					rows = append(rows, raw)
				}
				if rows == nil {
					rows = []any{}
				}
				return jsonResponse(200, map[string]any{"value": rows, "nextLink": nil}, nil), nil
			}
			if raw := f.revisions[root][strings.TrimPrefix(suffix, "/")]; raw != nil {
				return jsonResponse(200, raw, nil), nil
			}
			return jsonResponse(404, nil, nil), nil
		}
		id, typ, err := parseID(path)
		if err != nil || insightsWorkbookKind(typ) != kind {
			t.Fatal("unexpected native workbook request", req.Method, req.URL)
		}
		if req.Method == "GET" {
			if kind == insightsWorkbookType {
				if len(req.URL.Query()) != 2 || req.URL.Query().Get("canFetchContent") != "true" {
					t.Fatal("workbook GET omitted full content", req.URL)
				}
			} else if len(req.URL.Query()) != 1 {
				t.Fatal("unexpected workbook GET query", req.URL)
			}
			if f.objects[id] == nil {
				return jsonResponse(404, nil, nil), nil
			}
			return jsonResponse(200, f.objects[id], nil), nil
		}
		if req.Method != "DELETE" || len(req.URL.Query()) != 1 || req.Header.Get("If-Match") != "" || req.ContentLength > 0 || req.Header.Get("x-ms-client-request-id") == "" {
			t.Fatal("invented workbook deletion contract", req.Method, req.URL, req.Header)
		}
		f.deletes = append(f.deletes, id)
		if !f.hold && f.deleteStatus < 300 {
			delete(f.objects, id)
		}
		if f.deleteBody != nil {
			return jsonResponse(f.deleteStatus, f.deleteBody, f.deleteHeader), nil
		}
		return &http.Response{StatusCode: f.deleteStatus, Header: f.deleteHeader, Body: http.NoBody}, nil
	})
	return f
}

func TestApplicationInsightsWorkbooksNativeInventoryAndCleanup(t *testing.T) {
	for _, kind := range []string{insightsWorkbookType, insightsMyWorkbookType, insightsWorkbookTemplateType} {
		t.Run(kind, func(t *testing.T) {
			f := newWorkbookFixture(t, kind)
			request := productRequest(f.runtime, kind)
			request.Scope.Kind = asset.ScopeRegion
			request.Scope.NativeID = "westus"
			request.Limit = 1
			first, err := f.runtime.List(t.Context(), request)
			if err != nil || first.Complete || first.NextCursor == "" || len(first.Items) != 1 || first.RequestID != "workbook-native-list" {
				t.Fatal("native workbook discovery failed", first, err)
			}
			request.Cursor = first.NextCursor
			second, err := f.runtime.List(t.Context(), request)
			if err != nil || !second.Complete || len(second.Items) != 1 || second.Items[0].NativeID == first.Items[0].NativeID {
				t.Fatal("native workbook cursor failed", second, err)
			}
			for _, item := range append(first.Items, second.Items...) {
				if item.NativeType != kind || item.Location != "westus" || item.Normalized[insightsWorkbookProof] == "" || item.Normalized["_inventory_source"] != insightsInventorySource(kind) || item.Actionable == nil || !*item.Actionable {
					t.Fatal("workbook provenance/configuration missing", item)
				}
				if kind == insightsWorkbookType && len(array(item.Normalized["revisions"])) != 2 {
					t.Fatal("native workbook history was lost", item.Normalized)
				}
			}
			encoded, _ := json.Marshal(append(first.Items, second.Items...))
			for _, secret := range []string{"serializedData", "templateData", "union withsource", "Welcome to your new workbook"} {
				if strings.Contains(string(encoded), secret) {
					t.Fatal("opaque workbook content leaked", secret)
				}
			}
			if kind != insightsWorkbookTemplateType {
				seen := map[string]bool{}
				for _, query := range f.queries {
					seen[query.Get("category")] = true
				}
				if len(seen) != 4 {
					t.Fatal("documented workbook categories omitted", seen)
				}
			}
			request.Source, request.Cursor = inventorySource, ""
			if page, err := f.runtime.List(t.Context(), request); err != nil || !page.Complete || len(page.Items) != 0 {
				t.Fatal("broad source overwrote workbook records", page, err)
			}
			value := dnsAsset(t, f.runtime, f.objects[first.Items[0].NativeID])
			actionRequest, _ := dnsRequest(t, f.runtime, []asset.Asset{value}, value)
			actionRequest.IdempotencyKey = "native-workbook"
			driver, err := f.runtime.ResolveAction(t.Context(), "connection", value)
			if err != nil {
				t.Fatal(err)
			}
			result, err := driver.Execute(t.Context(), actionRequest)
			if err != nil || len(f.deletes) != 1 || f.deletes[0] != value.Identity.NativeID || len(f.objects) != 1 {
				t.Fatal("independent workbook cleanup failed", result, err, f.deletes)
			}
			encoded, _ = json.Marshal(actionRequest)
			var restored contracts.ActionRequest
			if err := json.Unmarshal(encoded, &restored); err != nil {
				t.Fatal(err)
			}
			driver, err = f.runtime.ResolveAction(t.Context(), "connection", restored.Asset)
			if err != nil {
				t.Fatal(err)
			}
			if wait, err := driver.Wait(t.Context(), restored, result); err != nil || !wait.Done {
				t.Fatal("workbook absence did not survive restart", wait, err)
			}
		})
	}
}

func TestApplicationInsightsWorkbooksReadBoundaries(t *testing.T) {
	for _, kind := range []string{insightsWorkbookType, insightsMyWorkbookType, insightsWorkbookTemplateType} {
		for _, mode := range []string{"list-403", "list-404", "list-partial", "list-duplicate", "list-next-filter", "get-403", "get-404", "get-partial", "get-lro", "get-other-id", "get-number-type", "get-other-type", "get-no-content", "get-content-drift", "group-missing", "group-denied", "snapshot-drift"} {
			t.Run(last(kind)+"/"+mode, func(t *testing.T) {
				f := newWorkbookFixture(t, kind)
				id := slices.Sorted(maps.Keys(f.objects))[0]
				f.override = func(req *http.Request) (*http.Response, bool) {
					path := strings.ToLower(req.URL.Path)
					collection := "/subscriptions/" + testSubscription + "/providers/" + strings.ToLower(kind)
					if kind == insightsWorkbookTemplateType {
						collection = f.groupID + "/providers/" + strings.ToLower(kind)
					}
					if path == collection && (kind == insightsWorkbookTemplateType || req.URL.Query().Get("category") == "workbook") {
						switch mode {
						case "list-403":
							return jsonResponse(403, nil, nil), true
						case "list-404":
							return jsonResponse(404, nil, nil), true
						case "list-partial":
							return jsonResponse(206, map[string]any{"value": []any{f.objects[id]}}, nil), true
						case "list-duplicate":
							return jsonResponse(200, map[string]any{"value": []any{f.objects[id], f.objects[id]}}, nil), true
						case "list-next-filter":
							return jsonResponse(200, map[string]any{"value": []any{}, "nextLink": req.URL.String() + "&tags=hidden"}, nil), true
						}
					}
					if path == "/subscriptions/"+testSubscription+"/resourcegroups" && mode == "group-missing" {
						if kind == insightsWorkbookTemplateType {
							return jsonResponse(404, nil, nil), true
						}
						return jsonResponse(200, map[string]any{"value": []any{}}, nil), true
					}
					if path == f.groupID && mode == "group-denied" {
						return jsonResponse(403, nil, nil), true
					}
					if path != id {
						return nil, false
					}
					raw := maps.Clone(f.objects[id])
					raw["properties"] = maps.Clone(object(raw["properties"]))
					switch mode {
					case "get-403":
						return jsonResponse(403, nil, nil), true
					case "get-404":
						return jsonResponse(404, nil, nil), true
					case "get-partial":
						return jsonResponse(206, raw, nil), true
					case "get-lro":
						return jsonResponse(200, raw, http.Header{"Azure-Asyncoperation": {req.URL.String()}}), true
					case "get-other-id":
						raw["id"] = strings.Replace(id, testSubscription, testTenant, 1)
					case "get-number-type":
						raw["type"] = 123
					case "get-other-type":
						raw["type"] = storageType
					case "get-no-content":
						if kind == insightsWorkbookTemplateType {
							delete(object(raw["properties"]), "templateData")
						} else {
							object(raw["properties"])["serializedData"] = nil
						}
					case "get-content-drift":
						if kind == insightsWorkbookTemplateType {
							object(raw["properties"])["templateData"] = map[string]any{"private": "changed"}
						} else {
							object(raw["properties"])["serializedData"] = "PRIVATE_CHANGED_CONTENT"
						}
					case "snapshot-drift":
						if f.calls["GET /subscriptions/"+testSubscription+"/resourcegroups"] < 2 {
							return nil, false
						}
						if kind == insightsWorkbookTemplateType {
							object(f.objects[id]["properties"])["templateData"] = map[string]any{"private": "changed"}
						} else {
							object(f.objects[id]["properties"])["serializedData"] = "PRIVATE_CHANGED_CONTENT"
						}
						return nil, false
					default:
						return nil, false
					}
					return jsonResponse(200, raw, nil), true
				}
				if page, err := f.runtime.List(t.Context(), productRequest(f.runtime, kind)); err == nil || page.Complete || len(page.Items) != 0 {
					t.Fatal("incomplete workbook inventory succeeded", page, err)
				}
			})
		}
	}
}

func TestApplicationInsightsWorkbooksNativePagingAndCursorChanges(t *testing.T) {
	for _, kind := range []string{insightsWorkbookType, insightsMyWorkbookType, insightsWorkbookTemplateType} {
		for _, mode := range []string{"native-pages", "content", "known-ids", "scope", "source"} {
			t.Run(last(kind)+"/"+mode, func(t *testing.T) {
				f := newWorkbookFixture(t, kind)
				ids := slices.Sorted(maps.Keys(f.objects))
				f.override = func(req *http.Request) (*http.Response, bool) {
					if !strings.EqualFold(last(req.URL.Path), last(kind)) || kind != insightsWorkbookTemplateType && req.URL.Query().Get("category") != "workbook" {
						return nil, false
					}
					if req.URL.Query().Get("$skiptoken") == "second" {
						return jsonResponse(200, map[string]any{"value": []any{f.objects[ids[1]]}}, nil), true
					}
					return jsonResponse(200, map[string]any{"value": []any{f.objects[ids[0]]}, "nextLink": req.URL.String() + "&$skiptoken=second"}, nil), true
				}
				request := productRequest(f.runtime, kind)
				request.Limit = 1
				page, err := f.runtime.List(t.Context(), request)
				if err != nil || page.Complete || len(page.Items) != 1 || page.NextCursor == "" {
					t.Fatal("native paging failed", page, err)
				}
				request.Cursor = page.NextCursor
				switch mode {
				case "content":
					if kind == insightsWorkbookTemplateType {
						object(f.objects[ids[0]]["properties"])["templateData"] = map[string]any{"query": "PRIVATE_CHANGED"}
					} else {
						object(f.objects[ids[0]]["properties"])["serializedData"] = "PRIVATE_CHANGED"
					}
				case "known-ids":
					request.KnownNativeIDs = ids
				case "scope":
					request.Scope = asset.Scope{Kind: asset.ScopeRegion, NativeID: "westus"}
				case "source":
					request.Source = insightsAnnotationSource
				}
				page, err = f.runtime.List(t.Context(), request)
				if mode == "native-pages" {
					if err != nil || !page.Complete || len(page.Items) != 1 || page.Items[0].NativeID != ids[1] {
						t.Fatal("stable native/client paging failed", page, err)
					}
				} else if err == nil || page.Complete || len(page.Items) != 0 {
					t.Fatal("changed workbook cursor accepted", page, err)
				}
			})
		}
	}
}

func TestApplicationInsightsWorkbooksCustomCategoryAndKnownIDs(t *testing.T) {
	for _, kind := range []string{insightsWorkbookType, insightsMyWorkbookType} {
		for _, mode := range []string{"arm-category", "known-category", "known-gone", "foreign-id", "wrong-kind", "duplicate-id", "unregistered-source", "custom-options", "original-my-list"} {
			t.Run(last(kind)+"/"+mode, func(t *testing.T) {
				if mode == "original-my-list" && kind != insightsMyWorkbookType {
					t.Skip("native discrepancy belongs to MyWorkbooks")
				}
				f := newWorkbookFixture(t, kind)
				id := slices.Sorted(maps.Keys(f.objects))[0]
				object(f.objects[id]["properties"])["category"] = "private-custom-category"
				request := productRequest(f.runtime, kind)
				switch mode {
				case "arm-category":
					f.armRows = []any{map[string]any{"id": id, "type": kind}}
				case "known-category":
					request.KnownNativeIDs = []string{id}
				case "known-gone":
					request.KnownNativeIDs = []string{id}
					delete(f.objects, id)
				case "foreign-id":
					request.KnownNativeIDs = []string{strings.Replace(id, testSubscription, testTenant, 1)}
				case "wrong-kind":
					request.KnownNativeIDs = []string{resourceID(applicationInsightsType, "another")}
				case "duplicate-id":
					request.KnownNativeIDs = []string{id, id}
				case "unregistered-source":
					request.Source = productInventorySource
				case "custom-options":
					request.Options = map[string]any{"category": "workbook"}
				case "original-my-list":
					f.override = func(req *http.Request) (*http.Response, bool) {
						if strings.EqualFold(last(req.URL.Path), last(kind)) && req.URL.Query().Get("category") == "workbook" {
							return jsonResponse(200, workbookExample(t, "2021-03-08/examples/MyWorkbooksList.json"), nil), true
						}
						return nil, false
					}
				}
				page, err := f.runtime.List(t.Context(), request)
				if mode != "arm-category" && mode != "known-category" && mode != "known-gone" {
					if err == nil || page.Complete || len(page.Items) != 0 {
						t.Fatal("unsafe workbook scope accepted", page, err)
					}
					return
				}
				want := 2
				if mode == "known-gone" {
					want = 1
				}
				if err != nil || !page.Complete || len(page.Items) != want {
					t.Fatal("custom/saved workbook disappeared", page, err)
				}
				if mode != "known-gone" && !slices.ContainsFunc(f.queries, func(q url.Values) bool { return q.Get("category") == "private-custom-category" }) {
					t.Fatal("discovered category was never reconciled natively")
				}
			})
		}
	}
}

func TestApplicationInsightsWorkbookRevisionAbsenceCannotProveRootAbsence(t *testing.T) {
	for _, mode := range []string{"list", "get"} {
		t.Run(mode, func(t *testing.T) {
			f := newWorkbookFixture(t, insightsWorkbookType)
			id := slices.Sorted(maps.Keys(f.objects))[0]
			value := dnsAsset(t, f.runtime, f.objects[id])
			request := contracts.ActionRequest{Asset: value, Action: "delete", IdempotencyKey: "revision-read-failure"}
			driver, err := f.runtime.ResolveAction(t.Context(), "connection", value)
			if err != nil {
				t.Fatal(err)
			}
			f.override = func(req *http.Request) (*http.Response, bool) {
				path := strings.ToLower(req.URL.Path)
				if mode == "list" && path == id+"/revisions" || mode == "get" && strings.HasPrefix(path, id+"/revisions/") {
					return jsonResponse(404, nil, nil), true
				}
				return nil, false
			}
			if _, err := driver.Execute(t.Context(), request); err == nil || len(f.deletes) != 0 {
				t.Fatal("revision absence authorized root deletion", err)
			}
			if read, err := driver.Readback(t.Context(), request); err == nil {
				t.Fatal("revision absence completed a live root", read)
			}
		})
	}
}

func changeWorkbookContent(kind string, raw map[string]any) {
	if kind == insightsWorkbookTemplateType {
		object(raw["properties"])["templateData"] = map[string]any{"query": "PRIVATE_CHANGED_WORKBOOK"}
	} else {
		object(raw["properties"])["serializedData"] = "PRIVATE_CHANGED_WORKBOOK"
	}
}

func TestApplicationInsightsWorkbooksActionGuards(t *testing.T) {
	for _, kind := range []string{insightsWorkbookType, insightsMyWorkbookType, insightsWorkbookTemplateType} {
		for _, mode := range []string{"content", "group-owner", "group-tag", "resource-tag", "group-content", "lock", "group-404", "root-403", "second-root-404", "late-content", "asset", "connection", "partition", "location", "proof", "parameters", "root-absent"} {
			t.Run(last(kind)+"/"+mode, func(t *testing.T) {
				f := newWorkbookFixture(t, kind)
				id := slices.Sorted(maps.Keys(f.objects))[0]
				value := dnsAsset(t, f.runtime, f.objects[id])
				request := contracts.ActionRequest{Asset: value, Action: "delete", IdempotencyKey: "guarded-workbook"}
				driver, err := f.runtime.ResolveAction(t.Context(), "connection", value)
				if err != nil {
					t.Fatal(err)
				}
				switch mode {
				case "content":
					changeWorkbookContent(kind, f.objects[id])
				case "group-owner":
					f.group["managedBy"] = resourceID(applicationInsightsType, "controller")
				case "group-tag":
					f.group["tags"] = map[string]any{"steward:protected": "true"}
				case "resource-tag":
					f.objects[id]["tags"] = map[string]any{"steward:protected": "true"}
				case "group-content":
					object(f.group["properties"])["private"] = "PRIVATE_CHANGED_GROUP"
				case "lock":
					f.locks = []any{map[string]any{"id": f.groupID + "/providers/microsoft.authorization/locks/keep", "properties": map[string]any{"level": "CanNotDelete"}}}
				case "asset":
					request.Asset.ID = "another"
				case "connection":
					request.Asset.Identity.ConnectionID = "another"
				case "partition":
					request.Asset.Identity.Partition = "another"
				case "location":
					request.Asset.Location = "eastus"
				case "proof":
					request.Asset.Normalized = maps.Clone(value.Normalized)
					delete(request.Asset.Normalized, insightsWorkbookProof)
				case "parameters":
					request.Parameters = map[string]any{"force": true}
				case "root-absent":
					delete(f.objects, id)
				default:
					reads := 0
					f.override = func(req *http.Request) (*http.Response, bool) {
						path := strings.ToLower(req.URL.Path)
						if path == f.groupID && mode == "group-404" {
							return jsonResponse(404, nil, nil), true
						}
						if path == id {
							reads++
							if mode == "root-403" {
								return jsonResponse(403, nil, nil), true
							}
							if reads == 2 && mode == "second-root-404" {
								return jsonResponse(404, nil, nil), true
							}
							if reads == 4 && mode == "late-content" {
								changeWorkbookContent(kind, f.objects[id])
							}
						}
						return nil, false
					}
				}
				result, err := driver.Execute(t.Context(), request)
				if mode == "root-absent" {
					if err != nil {
						t.Fatal(err)
					}
					if wait, err := driver.Wait(t.Context(), request, result); err != nil || !wait.Done {
						t.Fatal("native root absence failed", wait, err)
					}
				} else if err == nil {
					t.Fatal("changed/protected workbook authorized deletion", mode)
				}
				if len(f.deletes) != 0 {
					t.Fatal("guarded workbook received DELETE", f.deletes)
				}
				if err != nil && strings.Contains(err.Error(), "PRIVATE_") {
					t.Fatal("private configuration leaked into error")
				}
			})
		}
	}
}

func TestApplicationInsightsWorkbooksSynchronousDeletionAndReceipts(t *testing.T) {
	for _, kind := range []string{insightsWorkbookType, insightsMyWorkbookType, insightsWorkbookTemplateType} {
		for _, mode := range []string{"200", "204", "404-live", "accepted", "body", "lro", "denied", "missing-receipt", "receipt", "operation", "recreated", "read-denied"} {
			t.Run(last(kind)+"/"+mode, func(t *testing.T) {
				f := newWorkbookFixture(t, kind)
				id := slices.Sorted(maps.Keys(f.objects))[0]
				value := dnsAsset(t, f.runtime, f.objects[id])
				request := contracts.ActionRequest{Asset: value, Action: "delete", IdempotencyKey: "sync-workbook"}
				driver, err := f.runtime.ResolveAction(t.Context(), "connection", value)
				if err != nil {
					t.Fatal(err)
				}
				f.hold = true
				switch mode {
				case "200":
					f.deleteStatus = 200
				case "404-live":
					f.deleteStatus = 404
				case "accepted":
					f.deleteStatus = 202
				case "body":
					f.deleteStatus, f.deleteBody = 200, map[string]any{"status": "Succeeded"}
				case "lro":
					f.deleteHeader = http.Header{"Azure-Asyncoperation": {apiURL(id, insightsWorkbookVersion(kind))}}
				case "denied":
					f.deleteStatus = 403
				}
				result, err := driver.Execute(t.Context(), request)
				if slices.Contains([]string{"accepted", "body", "lro", "denied"}, mode) {
					if err == nil {
						t.Fatal("invented asynchronous or body contract accepted", result)
					}
					return
				}
				if err != nil || len(f.deletes) != 1 {
					t.Fatal("native deletion failed", result, err)
				}
				encoded, _ := json.Marshal(struct {
					Request contracts.ActionRequest
					Result  contracts.ActionResult
				}{request, result})
				var restored struct {
					Request contracts.ActionRequest
					Result  contracts.ActionResult
				}
				if err := json.Unmarshal(encoded, &restored); err != nil {
					t.Fatal(err)
				}
				request, result = restored.Request, restored.Result
				driver, err = f.runtime.ResolveAction(t.Context(), "connection", request.Asset)
				if err != nil {
					t.Fatal(err)
				}
				if wait, err := driver.Wait(t.Context(), request, result); err != nil || wait.Done {
					t.Fatal("DELETE response completed a live workbook", wait, err)
				}
				switch mode {
				case "missing-receipt":
					result.Data = nil
				case "receipt":
					result.Data["_insights_workbook_receipt"] = "forged"
				case "operation":
					result.ProviderOperationID = apiURL(id, insightsWorkbookVersion(kind))
				case "recreated":
					changeWorkbookContent(kind, f.objects[id])
				case "read-denied":
					f.override = func(req *http.Request) (*http.Response, bool) {
						if strings.EqualFold(req.URL.Path, id) {
							return jsonResponse(403, nil, nil), true
						}
						return nil, false
					}
				default:
					delete(f.objects, id)
					if wait, err := driver.Wait(t.Context(), request, result); err != nil || !wait.Done {
						t.Fatal("native absence failed after restart", wait, err)
					}
					return
				}
				if wait, err := driver.Wait(t.Context(), request, result); err == nil || wait.Done {
					t.Fatal("invalid receipt/read completed deletion", wait, err)
				}
			})
		}
	}
}

func TestApplicationInsightsWorkbookRevisionChangesAndBYOS(t *testing.T) {
	for _, mode := range []string{"history-content", "history-added", "history-removed", "history-duplicate", "history-foreign", "history-no-content", "byos"} {
		t.Run(mode, func(t *testing.T) {
			f := newWorkbookFixture(t, insightsWorkbookType)
			id := slices.Sorted(maps.Keys(f.objects))[0]
			if mode == "byos" {
				raw := object(workbookExample(t, "2023-06-01/examples/WorkbookManagedGet.json"))
				raw["id"], raw["name"] = id, last(id)
				f.objects[id] = raw
			}
			value := dnsAsset(t, f.runtime, f.objects[id])
			request := contracts.ActionRequest{Asset: value, Action: "delete", IdempotencyKey: "workbook-history"}
			driver, err := f.runtime.ResolveAction(t.Context(), "connection", value)
			if err != nil {
				t.Fatal(err)
			}
			revision := slices.Sorted(maps.Keys(f.revisions[id]))[0]
			switch mode {
			case "history-content":
				object(f.revisions[id][revision]["properties"])["serializedData"] = "PRIVATE_OLD_REVISION_CHANGED"
			case "history-removed":
				delete(f.revisions[id], revision)
			case "history-added":
				version := maps.Clone(f.revisions[id][revision])
				version["properties"] = maps.Clone(object(version["properties"]))
				object(version["properties"])["revision"] = "AddedRevision"
				f.revisions[id]["addedrevision"] = version
			case "history-no-content":
				object(f.revisions[id][revision]["properties"])["serializedData"] = nil
			case "history-foreign":
				f.revisions[id][revision]["id"] = strings.Replace(id, testSubscription, testTenant, 1)
			case "history-duplicate":
				f.override = func(req *http.Request) (*http.Response, bool) {
					if strings.EqualFold(req.URL.Path, id+"/revisions") {
						raw := f.revisions[id][revision]
						return jsonResponse(200, map[string]any{"value": []any{raw, raw}}, nil), true
					}
					return nil, false
				}
			}
			result, err := driver.Execute(t.Context(), request)
			if mode != "byos" {
				if err == nil || len(f.deletes) != 0 {
					t.Fatal("unreviewed history reached root DELETE", result, err)
				}
				return
			}
			if err != nil || !slices.Equal(f.deletes, []string{id}) {
				t.Fatal("BYOS workbook deletion failed", result, err)
			}
			if f.calls["GET "+id+"/revisions"] != 0 {
				t.Fatal("BYOS invented provider-managed history")
			}
			if wait, err := driver.Wait(t.Context(), request, result); err != nil || !wait.Done {
				t.Fatal("BYOS active resource absence failed", wait, err)
			}
		})
	}
}

func TestApplicationInsightsWorkbookRecordedNullableType(t *testing.T) {
	payload, err := os.ReadFile("fixtures/applicationinsights/cli-recordings.json")
	if err != nil {
		t.Fatal(err)
	}
	// The complete recording file also contains array bodies, so retain body
	// values generically and select the actual workbook GET below.
	var records []map[string]any
	decoder := json.NewDecoder(strings.NewReader(string(payload)))
	decoder.UseNumber()
	if err := decoder.Decode(&records); err != nil {
		t.Fatal(err)
	}
	for _, file := range records {
		if text(file["file"]) != "test_appinsights_workbook.yaml" {
			continue
		}
		for _, value := range array(file["recordings"]) {
			record := object(value)
			if text(record["method"]) != "GET" || object(record["body"])["id"] == nil {
				continue
			}
			f := newWorkbookFixture(t, insightsWorkbookType)
			id := slices.Sorted(maps.Keys(f.objects))[0]
			raw := object(record["body"])
			if raw["type"] != nil || object(raw["properties"])["serializedData"] != nil {
				t.Fatal("recorded null fields changed")
			}
			// Preserve the original metadata and null type. Bind only this
			// fixture's scope, then compose a full-content GET from the native
			// example; the original CLI request omitted canFetchContent.
			raw["id"], raw["name"] = id, last(id)
			f.objects[id] = raw
			c, err := f.runtime.resolve(t.Context(), "connection")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := c.workbookRead(t.Context(), insightsWorkbookType, id); err == nil {
				t.Fatal("recorded content omission authorized cleanup")
			}
			object(raw["properties"])["serializedData"] = object(object(workbookExample(t, "2023-06-01/examples/WorkbookGet.json"))["properties"])["serializedData"]
			page, err := f.runtime.List(t.Context(), productRequest(f.runtime, insightsWorkbookType))
			if err != nil || len(page.Items) != 2 {
				t.Fatal("native nullable type rejected after full content GET", page, err)
			}
			return
		}
	}
	t.Fatal("native workbook GET recording missing")
}
