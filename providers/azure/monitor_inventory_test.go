package azure

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"maps"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

type monitorInventoryFixture struct {
	runtime                   *Runtime
	kind, version, collection string
	objects, groups           map[string]map[string]any
	otherObjects              map[string]map[string]any
	locks                     []any
	calls                     map[string]int
	override                  func(*http.Request) (*http.Response, bool)
	groupOnly                 bool
	deletes                   []string
	deleteStatus              int
	deleteBody                any
	deleteHeader              http.Header
	hold                      bool
}

func monitorInventoryKinds() []string {
	return []string{monitorMetricAlertType, monitorActionGroupType, monitorActivityAlertType, monitorScheduledRuleType, monitorSmartAlertType, monitorPrometheusType, monitorProcessingType, insightsWebTestType, monitorConsumptionBudgetType, monitorCostBudgetType}
}

func newMonitorInventoryFixture(t *testing.T, kind string) *monitorInventoryFixture {
	t.Helper()
	f := &monitorInventoryFixture{kind: kind, objects: map[string]map[string]any{}, otherObjects: map[string]map[string]any{}, groups: map[string]map[string]any{}, locks: []any{}, calls: map[string]int{}, deleteStatus: 204}
	f.collection = "/subscriptions/" + testSubscription + "/providers/" + strings.ToLower(kind)
	var values []any
	if budget, version := monitorBudgetKind(kind); budget != "" {
		f.deleteStatus = 200
		f.version = version
		file := "consumption-2024-08-01/BudgetsList.json"
		if kind == monitorCostBudgetType {
			file = "cost-management-2025-03-01/Budgets/List/RBAC/SubscriptionBudgetsList.json"
		}
		values = array(monitorBudgetExample(t, file)["value"])
	} else {
		files := map[string]string{
			monitorMetricAlertType:   "metric-2026-01-01/getWebTestMetricAlert.json",
			monitorActionGroupType:   "actions-2023-01-01/getActionGroup.json",
			monitorActivityAlertType: "activity-2026-01-01/ActivityLogAlertRule_Get.json",
			monitorScheduledRuleType: "scheduled-2026-03-01/getScheduledQueryRule.json",
			monitorSmartAlertType:    "smart-2021-04-01/SmartDetectorAlertRule_Get.json",
			monitorPrometheusType:    "prometheus-2023-03-01/getPrometheusRuleGroup.json",
			monitorProcessingType:    "processing-2021-08-08/AlertProcessingRules_GetById.json",
			insightsWebTestType:      "webtest/WebTestGet.json",
		}
		file := files[kind]
		raw := monitorRuleComposedIdentity(t, monitorRuleExample(t, file), file)
		other := maps.Clone(raw)
		other["name"], other["id"] = "second-resource", strings.TrimSuffix(text(raw["id"]), last(text(raw["id"])))+"second-resource"
		values = []any{raw, other}
		f.version = monitorRuleKind(kind).version
	}
	for _, value := range values {
		raw := object(value)
		id, _, _, err := monitorResourceID(text(raw["id"]))
		if err != nil {
			t.Fatal(err)
		}
		// Compose a single credential boundary from the retained example's
		// subscription. All native properties survive, including private data.
		payload, _ := json.Marshal(raw)
		payload = []byte(strings.ReplaceAll(string(payload), strings.Split(id, "/")[2], testSubscription))
		decoder := json.NewDecoder(strings.NewReader(string(payload)))
		decoder.UseNumber()
		if err := decoder.Decode(&raw); err != nil {
			t.Fatal(err)
		}
		id, scope, _, err := monitorResourceID(text(raw["id"]))
		if err != nil {
			t.Fatal(err)
		}
		f.objects[id] = raw
		if scope != "/subscriptions/"+testSubscription {
			f.groups[scope] = map[string]any{"id": scope, "name": last(scope), "type": groupType, "location": "westus", "tags": map[string]any{}, "properties": map[string]any{"provisioningState": "Succeeded"}}
		}
	}
	f.runtime = protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
		path := strings.ToLower(req.URL.Path)
		f.calls[req.Method+" "+path]++
		if f.override != nil {
			if response, ok := f.override(req); ok {
				return response, nil
			}
		}
		if req.URL.Host != "management.azure.com" || req.Method != "GET" && req.Method != "DELETE" {
			t.Fatal("unexpected monitor inventory request", req.Method, req.URL)
		}
		if path == "/subscriptions/"+testSubscription+"/resourcegroups" {
			var values []any
			for _, id := range slices.Sorted(maps.Keys(f.groups)) {
				values = append(values, f.groups[id])
			}
			if values == nil {
				values = []any{}
			}
			return jsonResponse(200, map[string]any{"value": values}, nil), nil
		}
		if group := f.groups[path]; group != nil {
			return jsonResponse(200, group, nil), nil
		}
		if _, kind, err := parseID(path); err == nil && strings.EqualFold(kind, groupType) {
			return jsonResponse(404, map[string]any{"error": map[string]any{"code": "ResourceGroupNotFound"}}, nil), nil
		}
		if path == "/subscriptions/"+testSubscription+"/providers/microsoft.authorization/locks" {
			return jsonResponse(200, map[string]any{"value": f.locks}, nil), nil
		}
		collectionKind := ""
		for _, candidate := range monitorInventoryKinds() {
			if path == "/subscriptions/"+testSubscription+"/providers/"+strings.ToLower(candidate) {
				collectionKind = candidate
			}
			if budget, _ := monitorBudgetKind(candidate); budget != "" {
				for group := range f.groups {
					if path == group+"/providers/"+strings.ToLower(candidate) {
						collectionKind = candidate
					}
				}
			}
		}
		objects := maps.Clone(f.otherObjects)
		maps.Copy(objects, f.objects)
		if collectionKind != "" {
			version := monitorResourceVersion(collectionKind)
			if req.Method != "GET" || req.URL.Query().Get("api-version") != version {
				t.Fatal("wrong native monitor collection", req.Method, req.URL)
			}
			values := []any{}
			for _, id := range slices.Sorted(maps.Keys(objects)) {
				_, scope, candidate, _ := monitorResourceID(id)
				rootCollection := "/subscriptions/" + testSubscription + "/providers/" + strings.ToLower(collectionKind)
				if candidate != collectionKind || path != rootCollection && path != scope+"/providers/"+strings.ToLower(collectionKind) || f.groupOnly && path == f.collection && scope != "/subscriptions/"+testSubscription {
					continue
				}
				values = append(values, objects[id])
			}
			body := map[string]any{"value": values}
			if len(values) > 1 {
				if req.URL.Query().Get("$skiptoken") == "" {
					body["value"] = values[:1]
					body["nextLink"] = apiURL(req.URL.Path, version) + "&%24skiptoken=monitor-page-2"
				} else {
					body["value"] = values[1:]
				}
			}
			return jsonResponse(200, body, http.Header{"X-Ms-Request-Id": []string{"monitor-native-list"}}), nil
		}
		_, _, resourceKind, identityErr := monitorResourceID(path)
		if identityErr != nil || req.URL.Query().Get("api-version") != monitorResourceVersion(resourceKind) {
			t.Fatal("wrong native monitor resource", req.URL)
		}
		if raw := objects[path]; raw != nil {
			if req.Method == "DELETE" {
				if req.Header.Get("If-Match") != "" {
					t.Fatal("invented monitor delete condition")
				}
				f.deletes = append(f.deletes, path)
				if !f.hold {
					delete(f.objects, path)
					delete(f.otherObjects, path)
				}
				header := f.deleteHeader.Clone()
				if header == nil {
					header = http.Header{}
				}
				header.Set("X-Ms-Request-Id", "monitor-native-delete")
				response := jsonResponse(f.deleteStatus, f.deleteBody, header)
				if f.deleteBody == nil {
					response.Body = io.NopCloser(strings.NewReader(""))
				}
				return response, nil
			}
			return jsonResponse(200, raw, nil), nil
		}
		return jsonResponse(404, map[string]any{"error": map[string]any{"code": "ResourceNotFound"}}, nil), nil
	})
	return f
}

func (f *monitorInventoryFixture) request() contracts.InventoryRequest {
	kind := f.runtime.resourceKind(f.kind)
	return contracts.InventoryRequest{ConnectionID: "connection", Source: productInventorySource, ResourceKind: &kind, Scope: asset.Scope{Kind: asset.ScopeSubscription, NativeID: testSubscription}}
}

func (f *monitorInventoryFixture) addRelated(t *testing.T, raw map[string]any) string {
	t.Helper()
	id, scope, _, err := monitorResourceID(text(raw["id"]))
	if err != nil || !strings.HasPrefix(id, "/subscriptions/"+testSubscription+"/") {
		t.Fatal("invalid related monitor fixture", err)
	}
	f.otherObjects[id] = raw
	if scope != "/subscriptions/"+testSubscription && f.groups[scope] == nil {
		f.groups[scope] = map[string]any{"id": scope, "name": last(scope), "type": groupType, "location": "westus", "tags": map[string]any{}, "properties": map[string]any{"provisioningState": "Succeeded"}}
	}
	return id
}

func (f *monitorInventoryFixture) asset(t *testing.T, id string) asset.Asset {
	t.Helper()
	_, _, kind, err := monitorResourceID(id)
	if err != nil {
		t.Fatal(err)
	}
	request := f.request()
	resourceKind := f.runtime.resourceKind(kind)
	request.ResourceKind = &resourceKind
	batch, err := f.runtime.List(t.Context(), request)
	if err != nil {
		t.Fatal("monitor asset projection failed", err)
	}
	for _, item := range batch.Items {
		if item.NativeID != id {
			continue
		}
		if item.Actionable == nil || !*item.Actionable || !item.ResourceKind.Capabilities.Has(asset.CapabilityActionable) {
			t.Fatal("registered monitor not actionable", kind)
		}
		return asset.Asset{ID: asset.AssetID(id), Identity: asset.Identity{Provider: asset.ProviderAzure, ConnectionID: "connection", Partition: "azure", NativeType: kind, NativeID: id}, ResourceKindID: item.ResourceKind.ID, Location: item.Location, Normalized: item.Normalized, Capabilities: item.ResourceKind.Capabilities, Name: item.Name, Tags: item.Tags}
	}
	t.Fatal("monitor fixture asset missing", id)
	return asset.Asset{}
}

func TestMonitorNativeInventory(t *testing.T) {
	for _, kind := range monitorInventoryKinds() {
		t.Run(kind, func(t *testing.T) {
			f := newMonitorInventoryFixture(t, kind)
			budget, _ := monitorBudgetKind(kind)
			f.groupOnly = budget != "" // Group budgets omitted by the root index remain discoverable.
			request := f.request()
			request.Limit = 1
			var items []contracts.InventoryItem
			for {
				batch, err := f.runtime.List(t.Context(), request)
				if err != nil || len(batch.Items) != 1 || batch.RequestID != "monitor-native-list" {
					t.Fatal("native monitor scan failed", batch, err)
				}
				items = append(items, batch.Items...)
				if batch.Complete {
					if batch.NextCursor != "" {
						t.Fatal("complete scan retained cursor")
					}
					break
				}
				if batch.NextCursor == "" {
					t.Fatal("native scan lost cursor")
				}
				request.Cursor = batch.NextCursor
			}
			if len(items) != len(f.objects) {
				t.Fatal("native scan omitted identity", len(items), len(f.objects))
			}
			for _, item := range items {
				if item.NativeType != kind || item.Normalized["_inventory_source"] != productInventorySource || text(item.Normalized[monitorConfigurationProof]) == "" || text(item.Normalized[monitorGroupProof]) == "" || len(object(item.Normalized["arm_parameters"])) == 0 {
					t.Fatal("monitor projection lost evidence", item.NativeID)
				}
				wire, _ := json.Marshal(item)
				for _, private := range []string{"contactEmails", "johndoe@email.com", "CredentialPassword", "janesmith@email.com", "webhookReceivers", "ticketConfiguration", "RequestUrl", "criteria", "query", "expression", "notifications"} {
					if strings.Contains(string(wire), `"`+private+`"`) || strings.Contains(string(wire), "@email.com") || strings.Contains(string(wire), "<WebTest") {
						t.Fatal("monitor inventory leaked private configuration", private)
					}
				}
				id, scope, _, _ := monitorResourceID(item.NativeID)
				if f.objects[id] == nil {
					t.Fatal("invented monitor identity")
				}
				if budget, _ := monitorBudgetKind(kind); budget != "" {
					if item.Location != "global" || item.Scope.Kind != asset.ScopeGlobal || object(item.Normalized["arm_parameters"])["scope"] != strings.TrimPrefix(scope, "/") {
						t.Fatal("budget acquired a fabricated regional scope", item)
					}
					if scope == "/subscriptions/"+testSubscription && item.Normalized["resource_group"] != nil {
						t.Fatal("subscription budget acquired resource group")
					}
					if len(stringValues(item.Normalized[referenceKey(monitorActionGroupType)])) == 0 {
						t.Fatal("budget notification dependency omitted")
					}
				}
			}
			if f.calls["GET /subscriptions/"+testSubscription+"/resources"] != 0 {
				t.Fatal("native scan fell back to broad inventory")
			}
			request = f.request()
			request.Scope = asset.Scope{Kind: asset.ScopeRegion, NativeID: "not-a-resource-region"}
			batch, err := f.runtime.List(t.Context(), request)
			if err != nil || !batch.Complete || len(batch.Items) != 0 {
				t.Fatal("region filter failed", batch, err)
			}
			request = f.request()
			request.Source = inventorySource
			batch, err = f.runtime.List(t.Context(), request)
			if err != nil || !batch.Complete || len(batch.Items) != 0 {
				t.Fatal("broad inventory duplicated native scan", batch, err)
			}
		})
	}
}

func TestMonitorInventoryBoundaries(t *testing.T) {
	for _, kind := range []string{monitorActionGroupType, monitorCostBudgetType} {
		for _, mode := range []string{"missing-group", "group-owner-changed", "group-private-change", "new-group", "private-change", "new-resource", "deleted-resource", "lock-change", "page-forbidden", "page-missing", "get-missing", "cursor-offset", "cursor-scope", "cursor-connection", "cursor-options", "cursor-known-ids", "cursor-kind", "invalid-source", "invalid-scope"} {
			t.Run(kind+"/"+mode, func(t *testing.T) {
				f := newMonitorInventoryFixture(t, kind)
				request := f.request()
				request.Limit = 1
				first, err := f.runtime.List(t.Context(), request)
				if err != nil || first.NextCursor == "" {
					t.Fatal("initial monitor page failed", err)
				}
				request.Cursor = first.NextCursor
				id := slices.Sorted(maps.Keys(f.objects))[0]
				groupID := slices.Sorted(maps.Keys(f.groups))[0]
				switch mode {
				case "missing-group":
					delete(f.groups, groupID)
				case "group-owner-changed":
					f.groups[groupID]["managedBy"] = "another-owner"
				case "group-private-change":
					f.groups[groupID]["tags"] = map[string]any{"team": "changed"}
				case "new-group":
					other := maps.Clone(f.groups[groupID])
					newID := strings.TrimSuffix(groupID, last(groupID)) + "new-group"
					other["id"], other["name"] = newID, "new-group"
					f.groups[newID] = other
				case "private-change":
					if kind == monitorActionGroupType {
						object(array(object(f.objects[id]["properties"])["emailReceivers"])[0])["emailAddress"] = "PRIVATE_CHANGED_EMAIL"
					} else {
						for _, n := range object(object(f.objects[id]["properties"])["notifications"]) {
							object(n)["contactEmails"] = []any{"PRIVATE_CHANGED_EMAIL"}
						}
					}
				case "new-resource":
					other := maps.Clone(f.objects[id])
					otherID := strings.TrimSuffix(id, last(id)) + "new-resource"
					other["id"], other["name"] = otherID, "new-resource"
					f.objects[otherID] = other
				case "deleted-resource":
					delete(f.objects, id)
				case "lock-change":
					f.locks = []any{map[string]any{"id": groupID + "/providers/Microsoft.Authorization/locks/hold", "properties": map[string]any{"level": "CanNotDelete"}}}
				case "page-forbidden", "page-missing", "get-missing":
					f.override = func(req *http.Request) (*http.Response, bool) {
						if mode == "get-missing" && strings.EqualFold(req.URL.Path, id) || mode != "get-missing" && req.URL.Query().Get("$skiptoken") != "" {
							status := 404
							if mode == "page-forbidden" {
								status = 403
							}
							return jsonResponse(status, map[string]any{"error": map[string]any{"code": "ResourceNotFound"}}, nil), true
						}
						return nil, false
					}
				case "cursor-offset":
					payload, _ := base64.RawURLEncoding.DecodeString(request.Cursor)
					var cursor productCursor
					_ = json.Unmarshal(payload, &cursor)
					cursor.Target = 100000
					payload, _ = json.Marshal(cursor)
					request.Cursor = base64.RawURLEncoding.EncodeToString(payload)
				case "cursor-scope":
					request.Scope = asset.Scope{Kind: asset.ScopeGlobal, NativeID: "global"}
				case "cursor-connection":
					f.runtime.credentials = credentialFunc(func(context.Context, asset.ConnectionID) (contracts.Credential, error) { return testCredential(), nil })
					request.ConnectionID = "other-connection"
				case "cursor-options":
					request.Options = map[string]any{"filter": "limited"}
				case "cursor-known-ids":
					request.KnownNativeIDs = []string{id}
				case "cursor-kind":
					request.ResourceKind.ID = "other-kind"
				case "invalid-source":
					request.Source = insightsWorkbookSource
				case "invalid-scope":
					request.Scope = asset.Scope{Kind: asset.ScopeGlobal, NativeID: "foreign/global"}
				}
				batch, err := f.runtime.List(t.Context(), request)
				if err == nil || isNotFound(err) || len(batch.Items) != 0 || batch.Complete {
					t.Fatal("incomplete monitor scan authorized absence", mode, batch, err)
				}
			})
		}
	}
}

func TestMonitorInventoryProtection(t *testing.T) {
	for _, kind := range []string{monitorActionGroupType, monitorConsumptionBudgetType} {
		for _, mode := range []string{"resource-tag", "group-tag", "managed-group", "subscription-lock", "group-lock", "resource-lock"} {
			t.Run(kind+"/"+mode, func(t *testing.T) {
				f := newMonitorInventoryFixture(t, kind)
				groupID := slices.Sorted(maps.Keys(f.groups))[0]
				reason := "azure_protected_tag"
				switch mode {
				case "resource-tag":
					for _, raw := range f.objects {
						raw["tags"] = map[string]any{"steward:protected": "true"}
					}
				case "group-tag":
					f.groups[groupID]["tags"] = map[string]any{"steward:protected": "true"}
				case "managed-group":
					f.groups[groupID]["managedBy"] = "another-owner"
					reason = "azure_managed_resource_group"
				case "subscription-lock", "group-lock", "resource-lock":
					scope := "/subscriptions/" + testSubscription
					if mode == "group-lock" {
						scope = groupID
					}
					if mode == "resource-lock" {
						scope = slices.Sorted(maps.Keys(f.objects))[0]
					}
					f.locks = []any{map[string]any{"id": scope + "/providers/Microsoft.Authorization/locks/hold", "properties": map[string]any{"level": "CanNotDelete"}}}
					reason = "azure_management_lock"
				}
				batch, err := f.runtime.List(t.Context(), f.request())
				if err != nil {
					t.Fatal(err)
				}
				for _, item := range batch.Items {
					_, scope, _, _ := monitorResourceID(item.NativeID)
					want := reason
					if mode != "resource-tag" && mode != "subscription-lock" && scope != groupID {
						want = ""
					}
					if mode == "resource-lock" {
						want = ""
						if item.NativeID == slices.Sorted(maps.Keys(f.objects))[0] {
							want = reason
						}
					}
					if text(item.Normalized["cleanup_protection_reason"]) != want || want != "" && item.Normalized["cleanup_controller_only"] != true && item.Normalized["cleanup_protected"] != true {
						t.Fatal("monitor protection scope differs", mode, item.NativeID, item.Normalized["cleanup_protection_reason"], want)
					}
				}
			})
		}
	}
}

func TestMonitorInventoryConcurrentChanges(t *testing.T) {
	for _, kind := range []string{monitorActionGroupType, monitorCostBudgetType} {
		for _, mode := range []string{"second-snapshot-private-change", "second-snapshot-group-change", "second-snapshot-new-group", "projection-private-change", "locks-denied", "group-get-missing", "budget-index-drift"} {
			t.Run(kind+"/"+mode, func(t *testing.T) {
				if mode == "budget-index-drift" && kind != monitorCostBudgetType {
					return
				}
				f := newMonitorInventoryFixture(t, kind)
				id := slices.Sorted(maps.Keys(f.objects))[0]
				groupID := slices.Sorted(maps.Keys(f.groups))[0]
				changed := false
				f.override = func(req *http.Request) (*http.Response, bool) {
					path := strings.ToLower(req.URL.Path)
					change := false
					switch mode {
					case "second-snapshot-private-change":
						change = path == f.collection && f.calls["GET "+path] == 3
					case "projection-private-change":
						change = path == id && f.calls["GET "+path] == 2
					case "second-snapshot-group-change", "second-snapshot-new-group":
						if strings.HasSuffix(path, "/resourcegroups") && f.calls["GET "+path] == 2 {
							changed = true
							if mode == "second-snapshot-group-change" {
								f.groups[groupID]["tags"] = map[string]any{"team": "new"}
							} else {
								newID := strings.TrimSuffix(groupID, last(groupID)) + "new-group"
								raw := maps.Clone(f.groups[groupID])
								raw["id"], raw["name"] = newID, "new-group"
								f.groups[newID] = raw
							}
						}
					case "locks-denied":
						if strings.HasSuffix(path, "/providers/microsoft.authorization/locks") {
							changed = true
							return jsonResponse(403, map[string]any{"error": map[string]any{"code": "AuthorizationFailed"}}, nil), true
						}
					case "group-get-missing":
						if path == groupID {
							changed = true
							return jsonResponse(404, map[string]any{"error": map[string]any{"code": "ResourceNotFound"}}, nil), true
						}
					case "budget-index-drift":
						if path == groupID+"/providers/"+strings.ToLower(kind) && !changed {
							for candidate := range f.objects {
								_, scope, _, _ := monitorResourceID(candidate)
								if scope == groupID {
									id = candidate
									change = true
									break
								}
							}
						}
					}
					if change && !changed {
						changed = true
						if kind == monitorActionGroupType {
							object(array(object(f.objects[id]["properties"])["emailReceivers"])[0])["emailAddress"] = "PRIVATE_NEW_DESTINATION"
						} else {
							for _, value := range object(object(f.objects[id]["properties"])["notifications"]) {
								object(value)["contactEmails"] = []any{"PRIVATE_NEW_DESTINATION"}
							}
						}
					}
					return nil, false
				}
				batch, err := f.runtime.List(t.Context(), f.request())
				if !changed || err == nil || isNotFound(err) || len(batch.Items) != 0 || batch.Complete {
					t.Fatal("concurrent native change authorized an absence sweep", mode, changed, batch, err)
				}
			})
		}
	}
}

func TestMonitorInventoryReadOnlyChangesAndLockOrder(t *testing.T) {
	f := newMonitorInventoryFixture(t, monitorCostBudgetType)
	for _, name := range []string{"a", "b"} {
		f.locks = append(f.locks, map[string]any{"id": "/subscriptions/" + testSubscription + "/providers/Microsoft.Authorization/locks/" + name, "properties": map[string]any{"level": "CanNotDelete"}})
	}
	request := f.request()
	request.Limit = 1
	first, err := f.runtime.List(t.Context(), request)
	if err != nil || first.NextCursor == "" {
		t.Fatal(err)
	}
	slices.Reverse(f.locks)
	for _, raw := range f.objects {
		object(raw["properties"])["currentSpend"] = map[string]any{"amount": 1234, "unit": "USD"}
	}
	request.Cursor = first.NextCursor
	request.Limit = 2
	second, err := f.runtime.List(t.Context(), request)
	if err != nil || len(second.Items) != min(2, len(f.objects)-1) || second.Items[0].NativeID == first.Items[0].NativeID {
		t.Fatal("read-only spend or reordered locks invalidated native cursor", second, err)
	}
}
