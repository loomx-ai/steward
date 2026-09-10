package azure

import (
	"encoding/json"
	"maps"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

type diagnosticFixture struct {
	runtime                   *Runtime
	sources, settings, groups map[string]map[string]any
	locks                     []any
	calls                     map[string]int
	deleted                   []string
	override                  func(*http.Request) (*http.Response, bool)
	deleteStatus              int
	hold                      bool
}

func newDiagnosticFixture(t *testing.T) *diagnosticFixture {
	t.Helper()
	f := &diagnosticFixture{sources: map[string]map[string]any{}, settings: map[string]map[string]any{}, groups: map[string]map[string]any{}, locks: []any{}, calls: map[string]int{}, deleteStatus: 204}
	source := nativeResource("Microsoft.KeyVault/vaults", "diagnostic-source", "westus", map[string]any{"tenantId": testTenant, "sku": map[string]any{"family": "A", "name": "standard"}, "accessPolicies": []any{}})
	source["systemData"] = map[string]any{"createdAt": "2026-01-01T00:00:00Z"}
	sourceID := strings.ToLower(text(source["id"]))
	f.sources[sourceID] = source
	group := "/subscriptions/" + testSubscription + "/resourcegroups/test"
	f.groups[group] = map[string]any{"id": group, "name": "test", "type": groupType, "location": "westus", "tags": map[string]any{}, "properties": map[string]any{"provisioningState": "Succeeded"}}
	for _, row := range []struct{ file, scope, name string }{
		{"getDiagnosticSetting.json", sourceID, "resource-a"},
		{"getDiagnosticSetting.json", sourceID, "resource-b"},
		{"getSubscriptionDiagnosticSetting.json", "/subscriptions/" + testSubscription, "subscription-setting"},
	} {
		raw := diagnosticBody(t, row.file)
		// Compose local source identities and names. All original destination
		// properties are retained, including foreign subscription references.
		id := strings.ToLower(row.scope + "/providers/" + diagnosticSettingsType + "/" + row.name)
		raw["id"], raw["name"] = id, row.name
		if metrics := array(object(raw["properties"])["metrics"]); len(metrics) != 0 {
			object(metrics[0])["category"] = "AllMetrics"
		}
		f.settings[id] = raw
	}
	f.runtime = protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
		path := strings.ToLower(req.URL.Path)
		f.calls[req.Method+" "+path]++
		if f.override != nil {
			if response, ok := f.override(req); ok {
				return response, nil
			}
		}
		if req.URL.Host != "management.azure.com" || !strings.HasPrefix(path, "/subscriptions/"+testSubscription+"/") {
			t.Fatal("diagnostic fixture crossed subscription boundary", req.URL)
		}
		if req.Method == "GET" && path == "/subscriptions/"+testSubscription+"/resources" {
			values := []any{}
			for _, id := range slices.Sorted(maps.Keys(f.sources)) {
				values = append(values, f.sources[id])
			}
			return jsonResponse(200, map[string]any{"value": values}, nil), nil
		}
		if req.Method == "GET" && strings.HasSuffix(path, "/providers/microsoft.authorization/locks") {
			return jsonResponse(200, map[string]any{"value": f.locks}, nil), nil
		}
		if req.Method == "GET" && strings.HasSuffix(path, "/providers/microsoft.insights/diagnosticsettings") {
			if req.URL.Query().Get("api-version") != diagnosticSettingsVersion {
				t.Fatal("diagnostic collection API version changed")
			}
			values := []any{}
			for _, id := range slices.Sorted(maps.Keys(f.settings)) {
				if strings.TrimSuffix(id, "/"+last(id)) == path {
					values = append(values, f.settings[id])
				}
			}
			return jsonResponse(200, map[string]any{"value": values}, http.Header{"X-Ms-Request-Id": {"diagnostic-native-list"}}), nil
		}
		if _, _, kind, err := diagnosticResourceID(path); err == nil && kind == diagnosticSettingsType {
			if req.URL.Query().Get("api-version") != diagnosticSettingsVersion || req.Method != "GET" && req.Method != "DELETE" {
				t.Fatal("unexpected diagnostic operation", req.Method, req.URL)
			}
			if raw := f.settings[path]; raw != nil {
				if req.Method == "DELETE" {
					if req.Header.Get("If-Match") != "" {
						t.Fatal("invented diagnostic deletion condition")
					}
					f.deleted = append(f.deleted, path)
					if !f.hold {
						delete(f.settings, path)
					}
					return &http.Response{StatusCode: f.deleteStatus, Header: http.Header{"X-Ms-Request-Id": {"diagnostic-native-delete"}}, Body: http.NoBody}, nil
				}
				return jsonResponse(200, raw, nil), nil
			}
			return jsonResponse(404, map[string]any{"error": map[string]any{"code": "ResourceNotFound"}}, nil), nil
		}
		if response, ok := emptyMonitorIndexResponse(t, req); ok {
			return response, nil
		}
		if req.Method == "GET" {
			if source := f.sources[path]; source != nil {
				return jsonResponse(200, source, nil), nil
			}
			if group := f.groups[path]; group != nil {
				return jsonResponse(200, group, nil), nil
			}
			if _, kind, err := parseID(path); err == nil && (strings.EqualFold(kind, "Microsoft.KeyVault/vaults") || strings.EqualFold(kind, storageType) || strings.EqualFold(kind, groupType)) {
				return jsonResponse(404, map[string]any{"error": map[string]any{"code": "ResourceNotFound"}}, nil), nil
			}
		}
		t.Fatal("unexpected diagnostic fixture request", req.Method, req.URL)
		return nil, nil
	})
	return f
}

func (f *diagnosticFixture) request() contracts.InventoryRequest {
	kind := f.runtime.resourceKind(diagnosticSettingsType)
	return contracts.InventoryRequest{ConnectionID: "connection", Source: diagnosticInventorySource, ResourceKind: &kind, Scope: asset.Scope{Kind: asset.ScopeSubscription, NativeID: testSubscription}}
}

func (f *diagnosticFixture) list(t *testing.T, request contracts.InventoryRequest) (contracts.InventoryBatch, error) {
	t.Helper()
	return f.runtime.List(t.Context(), request)
}

func TestDiagnosticInventoryNativeScopesAndKnownOrphans(t *testing.T) {
	for _, mode := range []string{"native", "orphan", "source-and-group-absent", "known-missing", "unknown-source", "protected-source", "protected-group", "locked"} {
		t.Run(mode, func(t *testing.T) {
			f := newDiagnosticFixture(t)
			request := f.request()
			request.KnownNativeIDs = slices.Sorted(maps.Keys(f.settings))
			sourceID := slices.Sorted(maps.Keys(f.sources))[0]
			groupID := slices.Sorted(maps.Keys(f.groups))[0]
			expected := 3
			switch mode {
			case "orphan":
				delete(f.sources, sourceID)
			case "source-and-group-absent":
				delete(f.sources, sourceID)
				delete(f.groups, groupID)
			case "known-missing":
				for id := range f.settings {
					if strings.HasPrefix(id, sourceID+"/") {
						delete(f.settings, id)
						expected--
					}
				}
				delete(f.sources, sourceID)
			case "unknown-source":
				source := f.sources[sourceID]
				newID := strings.Replace(sourceID, "microsoft.keyvault/vaults", "microsoft.example/unregistered", 1)
				source["id"], source["type"] = newID, "Microsoft.Example/unregistered"
				delete(f.sources, sourceID)
				f.sources[newID] = source
				for id, raw := range f.settings {
					if strings.HasPrefix(id, sourceID+"/") {
						newSettingID := strings.Replace(id, sourceID, newID, 1)
						raw["id"] = newSettingID
						delete(f.settings, id)
						f.settings[newSettingID] = raw
					}
				}
				request.KnownNativeIDs = nil
			case "protected-source":
				f.sources[sourceID]["tags"] = map[string]any{"steward:protected": "true"}
			case "protected-group":
				f.groups[groupID]["tags"] = map[string]any{"steward:protected": "true"}
			case "locked":
				f.locks = []any{map[string]any{"id": groupID + "/providers/Microsoft.Authorization/locks/keep", "properties": map[string]any{"level": "CanNotDelete"}}}
			}
			request.Limit = 1
			var items []contracts.InventoryItem
			for {
				batch, err := f.list(t, request)
				if err != nil || len(batch.Items) != 1 || batch.RequestID != "diagnostic-native-list" {
					t.Fatal("diagnostic native inventory failed", len(batch.Items), batch.RequestID, err)
				}
				items = append(items, batch.Items...)
				if batch.Complete {
					break
				}
				request.Cursor = batch.NextCursor
			}
			if len(items) != expected || len(f.deleted) != 0 {
				t.Fatal("diagnostic inventory lost identities or mutated resources", len(items), expected)
			}
			for _, item := range items {
				if item.Location != "global" || item.Scope.Kind != asset.ScopeGlobal || text(item.Normalized[diagnosticConfigurationProof]) == "" || text(item.Normalized[diagnosticContextProof]) == "" || text(item.Normalized[diagnosticReferencesProof]) == "" {
					t.Fatal("diagnostic scope or configuration proof lost")
				}
				_, scope, _, _ := diagnosticResourceID(item.NativeID)
				protected := mode == "unknown-source" || mode == "protected-source" || mode == "protected-group" || mode == "locked"
				if scope != "/subscriptions/"+testSubscription && (item.Normalized["cleanup_protected"] == true) != protected {
					t.Fatal("diagnostic protection changed", item.Normalized["cleanup_protection_reason"])
				}
				payload, _ := json.Marshal(item)
				if strings.Contains(string(payload), "retentionPolicy") || strings.Contains(string(payload), "categoryGroup") {
					t.Fatal("private diagnostic properties leaked into inventory")
				}
			}
		})
	}
}

func TestDiagnosticInventoryIncompleteAndChangingCollections(t *testing.T) {
	for _, mode := range []string{"source-index-denied", "source-index-missing", "setting-index-missing", "source-get-missing", "source-recreated", "private-change", "group-change", "known-omitted", "foreign-known", "duplicate-known", "cursor-change"} {
		t.Run(mode, func(t *testing.T) {
			f := newDiagnosticFixture(t)
			request := f.request()
			sourceID := slices.Sorted(maps.Keys(f.sources))[0]
			settingID := sourceID + "/providers/microsoft.insights/diagnosticsettings/resource-a"
			groupID := slices.Sorted(maps.Keys(f.groups))[0]
			request.KnownNativeIDs = []string{settingID}
			if mode == "foreign-known" {
				request.KnownNativeIDs[0] = strings.Replace(settingID, testSubscription, testTenant, 1)
			}
			if mode == "duplicate-known" {
				request.KnownNativeIDs = []string{settingID, settingID}
			}
			if mode == "cursor-change" {
				request.Limit = 1
				first, err := f.list(t, request)
				if err != nil || first.Complete {
					t.Fatal("diagnostic cursor fixture failed", err)
				}
				request.Cursor = first.NextCursor
				object(f.settings[settingID]["properties"])["ordinaryAuthoredField"] = "private drift"
			}
			f.override = func(req *http.Request) (*http.Response, bool) {
				path := strings.ToLower(req.URL.Path)
				call := f.calls[req.Method+" "+path]
				if path == "/subscriptions/"+testSubscription+"/resources" && (mode == "source-index-denied" || mode == "source-index-missing") {
					status := 403
					if mode == "source-index-missing" {
						status = 404
					}
					return jsonResponse(status, map[string]any{}, nil), true
				}
				if path == sourceID && mode == "source-get-missing" {
					return jsonResponse(404, map[string]any{}, nil), true
				}
				if path == sourceID && mode == "source-recreated" && call >= 2 {
					object(f.sources[sourceID]["systemData"])["createdAt"] = "2026-02-01T00:00:00Z"
				}
				if path == settingID && mode == "private-change" && call >= 3 {
					object(f.settings[settingID]["properties"])["ordinaryAuthoredField"] = "private drift"
				}
				if path == groupID && mode == "group-change" && call >= 3 {
					f.groups[groupID]["managedBy"] = resourceID(aksType, "new-controller")
				}
				if path == sourceID+"/providers/microsoft.insights/diagnosticsettings" {
					if mode == "setting-index-missing" {
						return jsonResponse(404, map[string]any{}, nil), true
					}
					if mode == "known-omitted" {
						return jsonResponse(200, map[string]any{"value": []any{}}, nil), true
					}
				}
				return nil, false
			}
			if _, err := f.list(t, request); err == nil || isNotFound(err) || len(f.deleted) != 0 {
				t.Fatal("incomplete diagnostic scan accepted", err)
			}
		})
	}
}

func TestDiagnosticStorageServiceScopes(t *testing.T) {
	f := newDiagnosticFixture(t)
	clear(f.sources)
	clear(f.settings)
	storage := nativeResource(storageType, "diagnosticstorage", "westus", map[string]any{"creationTime": "2026-01-01T00:00:00Z", "primaryEndpoints": map[string]any{
		"blob": "https://diagnosticstorage.blob.core.windows.net/", "file": "https://diagnosticstorage.file.core.windows.net/", "queue": "https://diagnosticstorage.queue.core.windows.net/", "table": "https://diagnosticstorage.table.core.windows.net/",
	}})
	storage["kind"], storage["sku"] = "StorageV2", map[string]any{"name": "Standard_LRS"}
	id := strings.ToLower(text(storage["id"]))
	f.sources[id] = storage
	for _, service := range []string{"blob", "file", "queue", "table"} {
		raw := diagnosticBody(t, "getDiagnosticSetting.json")
		settingID := id + "/" + service + "services/default/providers/microsoft.insights/diagnosticsettings/setting"
		raw["id"], raw["name"] = settingID, "setting"
		f.settings[settingID] = raw
	}
	f.override = func(req *http.Request) (*http.Response, bool) {
		if strings.EqualFold(req.URL.Path, id+"/blobservices/default/containers") {
			return jsonResponse(200, map[string]any{"value": []any{}}, nil), true
		}
		return nil, false
	}
	batch, err := f.list(t, f.request())
	if err != nil || !batch.Complete || len(batch.Items) != 4 {
		t.Fatal("storage service diagnostic scopes were not discovered", len(batch.Items), err)
	}
	for _, item := range batch.Items {
		if item.Normalized["cleanup_protected"] == true {
			t.Fatal("native storage service incarnation was not established", item.Normalized["cleanup_protection_reason"])
		}
		_, scope, _, _ := diagnosticResourceID(item.NativeID)
		if f.calls["GET "+scope+"/providers/microsoft.insights/diagnosticsettings"] != 2 {
			t.Fatal("storage service scope did not receive two native observations", scope)
		}
		if !slices.Contains(stringValues(object(item.Normalized["_diagnostic_references"])[storageType]), id) {
			t.Fatal("service setting did not protect its storage-account ancestor")
		}
	}
}
