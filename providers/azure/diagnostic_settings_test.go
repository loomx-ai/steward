package azure

import (
	"encoding/json"
	"maps"
	"net/http"
	"slices"
	"strings"
	"testing"
)

func diagnosticBody(t *testing.T, file string) map[string]any {
	t.Helper()
	return object(object(object(diagnosticExample(t, file)["responses"])["200"])["body"])
}

func TestDiagnosticExtensionProviderControlsNativePaginationVersion(t *testing.T) {
	for _, sourceKind := range []string{"Microsoft.ApiManagement/service", batchAccountType, streamAnalyticsJobType, cognitiveType} {
		t.Run(sourceKind, func(t *testing.T) {
			scope := strings.ToLower(resourceID(sourceKind, "diagnosticsource"))
			first := diagnosticBody(t, "getDiagnosticSetting.json")
			first["id"], first["name"] = scope+"/providers/microsoft.insights/diagnosticsettings/one", "one"
			second := maps.Clone(first)
			second["id"], second["name"] = scope+"/providers/microsoft.insights/diagnosticsettings/two", "two"
			calls := 0
			r := protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
				calls++
				if req.Method != "GET" || req.URL.Query().Get("api-version") != diagnosticSettingsVersion {
					t.Fatal("diagnostic extension borrowed its source's API version", req.Method, req.URL)
				}
				path := strings.ToLower(req.URL.Path)
				if path == text(first["id"]) {
					return jsonResponse(200, first, nil), nil
				}
				if path == text(second["id"]) {
					return jsonResponse(200, second, nil), nil
				}
				if path != scope+"/providers/microsoft.insights/diagnosticsettings" {
					t.Fatal("diagnostic extension changed collection", req.URL)
				}
				if req.URL.Query().Get("$skiptoken") == "second" {
					return jsonResponse(200, map[string]any{"value": []any{second}}, nil), nil
				}
				return jsonResponse(200, map[string]any{"value": []any{first}, "nextLink": apiURL(path, diagnosticSettingsVersion) + "&%24skiptoken=second"}, nil), nil
			})
			c, _ := r.resolve(t.Context(), "connection")
			values, _, err := c.diagnosticIndex(t.Context(), scope, diagnosticSettingsType)
			if err != nil || len(values) != 2 || calls != 4 {
				t.Fatal("native extension list/GET pagination failed", len(values), calls, err)
			}
			if sourceKind != cognitiveType {
				// The native source's own list must retain its original version
				// check; allowing an extension does not weaken that boundary.
				collection := strings.TrimSuffix(scope, "/diagnosticsource")
				if _, _, err := c.listPage(t.Context(), apiURL(collection, diagnosticSettingsVersion), collection); err == nil || calls != 4 {
					t.Fatal("diagnostic extension weakened native source query validation", calls, err)
				}
			}
		})
	}
}

func TestDiagnosticNativeReadsAndIdentityDiscrepancies(t *testing.T) {
	for _, file := range []string{"getDiagnosticSetting.json", "getDiagnosticSettingCategory.json", "getSubscriptionDiagnosticSetting.json", "getSubscriptionDiagnosticSettingCategory.json", "getDiagnosticSettingsCategory.json"} {
		t.Run(file, func(t *testing.T) {
			example, raw := diagnosticExample(t, file), diagnosticBody(t, file)
			params := object(example["parameters"])
			scope := "/" + text(params["resourceUri"])
			if params["subscriptionId"] != nil {
				scope = "/subscriptions/" + text(params["subscriptionId"])
			}
			kind := diagnosticSettingsType
			if file == "getDiagnosticSettingsCategory.json" {
				kind = diagnosticCategoryType
			}
			id := strings.ToLower(scope + "/providers/" + kind + "/" + text(params["name"]))
			calls := 0
			c := directClient(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.Method != "GET" || !strings.EqualFold(r.URL.Path, id) || r.URL.Query().Get("api-version") != diagnosticSettingsVersion || r.Header.Get("If-Match") != "" {
					t.Fatal("native diagnostic GET changed", r.URL)
				}
				return jsonResponse(200, raw, http.Header{"X-Ms-Request-Id": {"native-diagnostic-get"}}), nil
			})
			c.subscription = strings.Split(scope, "/")[2]
			result, err := c.diagnosticRead(t.Context(), id, kind)
			invalid := file == "getDiagnosticSettingCategory.json" || strings.HasPrefix(file, "getSubscription")
			if (err != nil) != invalid || calls != 1 {
				t.Fatal("original diagnostic identity discrepancy changed", invalid, err, calls)
			}
			if !invalid && result.requestID != "native-diagnostic-get" {
				t.Fatal("missing native request provenance")
			}
			if strings.HasPrefix(file, "getSubscription") {
				// Preserve the original response. Only this composed request uses
				// the response's actual ds4 name, instead of original mysetting.
				id, _, _, err = diagnosticResourceID(text(raw["id"]))
				if err != nil {
					t.Fatal(err)
				}
				if _, err := c.diagnosticRead(t.Context(), id, kind); err != nil {
					t.Fatal("documented subscription alias rejected", err)
				}
			}
			if kind == diagnosticSettingsType {
				deletion, err := c.diagnosticRequest(scope, last(id), kind, "DELETE")
				if err != nil || deletion.Method != "DELETE" || !strings.Contains(strings.ToLower(deletion.URL), id+"?") || len(deletion.Body) != 0 || deletion.Headers["If-Match"] != "" {
					t.Fatal("native synchronous deletion binding changed", deletion, err)
				}
			} else if _, err := c.diagnosticRequest(scope, last(id), kind, "DELETE"); err == nil {
				t.Fatal("category capability became deletable")
			}
		})
	}
}

func TestDiagnosticNativeIndexes(t *testing.T) {
	for _, file := range []string{"listDiagnosticSettings.json", "listDiagnosticSettingsCategory.json", "listSubscriptionDiagnosticSettings.json", "listSubscriptionDiagnosticSettingsCategory.json", "listDiagnosticSettingsCategories.json"} {
		t.Run(file, func(t *testing.T) {
			example, body := diagnosticExample(t, file), diagnosticBody(t, file)
			params := object(example["parameters"])
			scope := "/" + text(params["resourceUri"])
			if params["subscriptionId"] != nil {
				scope = "/subscriptions/" + text(params["subscriptionId"])
			}
			kind := diagnosticSettingsType
			if file == "listDiagnosticSettingsCategories.json" {
				kind = diagnosticCategoryType
			}
			values, native := array(body["value"]), map[string]map[string]any{}
			for _, value := range values {
				raw := object(value)
				id, _, _, _ := diagnosticResourceID(text(raw["id"]))
				native[id] = raw
			}
			collection := strings.ToLower(scope + "/providers/" + kind)
			pages, reads := 0, 0
			c := directClient(func(r *http.Request) (*http.Response, error) {
				if r.Method != "GET" || r.URL.Query().Get("api-version") != diagnosticSettingsVersion {
					t.Fatal("invalid diagnostic index request", r.URL)
				}
				if strings.EqualFold(r.URL.Path, collection) {
					pages++
					if len(strings.Split(scope, "/")) == 3 {
						return jsonResponse(200, body, nil), nil
					}
					// Compose pagination from unchanged original rows. GET uses
					// those rows too; the original setting GET has another rule ID.
					if r.URL.Query().Get("$skiptoken") == "" {
						return jsonResponse(200, map[string]any{"value": values[:1], "nextLink": apiURL(collection, diagnosticSettingsVersion) + "&$skiptoken=second"}, nil), nil
					}
					return jsonResponse(200, map[string]any{"value": values[1:]}, http.Header{"X-Ms-Request-Id": {"native-diagnostic-last-page"}}), nil
				}
				if raw := native[strings.ToLower(r.URL.Path)]; raw != nil {
					reads++
					return jsonResponse(200, raw, nil), nil
				}
				t.Fatal("unexpected diagnostic request", r.URL)
				return nil, nil
			})
			c.subscription = strings.Split(scope, "/")[2]
			result, provenance, err := c.diagnosticIndex(t.Context(), scope, kind)
			if file == "listDiagnosticSettingsCategory.json" {
				if err == nil || reads != 0 || pages != 1 {
					t.Fatal("original malformed resource list identity was accepted", err, reads, pages)
				}
				return
			}
			expectedPages := 2
			if scope == c.root() {
				expectedPages = 1
			} else if provenance != "native-diagnostic-last-page" {
				t.Fatal("lost final-page provenance")
			}
			if err != nil || len(result) != len(values) || pages != expectedPages || reads != len(values) {
				t.Fatal("native diagnostic index failed", err, len(result), pages, reads)
			}
		})
	}
}

func TestDiagnosticScopeBoundaries(t *testing.T) {
	root := "/subscriptions/" + testSubscription
	source := strings.ToLower(resourceID(vmType, "observed"))
	for _, scope := range []string{root, root + "/resourcegroups/test", source, source + "/extensions/agent"} {
		wire := scope + "/providers/" + diagnosticSettingsType + "/setting"
		id, actual, kind, err := diagnosticResourceID(wire)
		if err != nil || id != strings.ToLower(wire) || actual != scope || kind != diagnosticSettingsType {
			t.Fatal("valid native diagnostic scope rejected", wire, err)
		}
	}
	for _, wire := range []string{
		root + "/providers/AzureResourceManager/diagnosticSettings/name",
		strings.TrimPrefix(root, "/") + "/providers/AzureResourceManager/diagnosticSettings/name",
	} {
		if _, _, _, err := diagnosticResourceID(wire); err != nil {
			t.Fatal("documented subscription alias rejected", err)
		}
	}
	for _, wire := range []string{
		source + "/providers/AzureResourceManager/diagnosticSettings/name",
		strings.TrimPrefix(source, "/") + "/providers/Microsoft.Insights/diagnosticSettings/name",
		strings.TrimPrefix(root, "/") + "/providers/Microsoft.Insights/diagnosticSettings/name",
		root + "/providers/Microsoft.Insights/diagnosticSettingsCategories/name",
		"/providers/Microsoft.Management/managementGroups/group/providers/Microsoft.Insights/diagnosticSettings/name",
		source + "/diagnosticSettings/name",
		source + "/providers/Microsoft.Insights/diagnosticSettings/a/providers/Microsoft.Insights/diagnosticSettings/b",
		source + "/providers/Microsoft.Insights/diagnosticSettings/../name",
		source + "/providers/Microsoft.Insights/diagnosticSettings/name%2fother",
		source + "/providers/Microsoft.Insights/diagnosticSettings/name?x=y",
		source + "/providers/Microsoft.Insights/diagnosticSettings/name\t",
	} {
		if _, _, _, err := diagnosticResourceID(wire); err == nil {
			t.Fatal("invalid diagnostic identity accepted", wire)
		}
	}
	c := directClient(func(*http.Request) (*http.Response, error) {
		t.Fatal("invalid selector reached transport")
		return nil, nil
	})
	for _, scope := range []string{strings.ToUpper(source), source + "/", strings.Replace(source, testSubscription, "eeeeeeee-eeee-eeee-eeee-eeeeeeeeeeee", 1)} {
		if _, _, err := c.diagnosticIndex(t.Context(), scope, diagnosticSettingsType); err == nil {
			t.Fatal("invalid diagnostic request scope accepted", scope)
		}
	}
}

func TestDiagnosticReadBoundaries(t *testing.T) {
	for _, file := range []string{"getDiagnosticSetting.json", "getSubscriptionDiagnosticSetting.json", "getDiagnosticSettingsCategory.json"} {
		for _, mode := range []string{"foreign-id", "wrong-name", "wrong-type", "numeric-type", "missing-properties", "aliased-properties", "error", "aliased-error", "next", "aliased-next", "async", "accepted", "denied", "missing", "bad-config", "aliased-config", "opaque-tags", "invalid-tag", "invalid-location"} {
			t.Run(file+"/"+mode, func(t *testing.T) {
				raw := diagnosticBody(t, file)
				id, _, kind, _ := diagnosticResourceID(text(raw["id"]))
				status, headers := 200, http.Header{}
				props := object(raw["properties"])
				switch mode {
				case "foreign-id":
					raw["id"] = strings.Replace(text(raw["id"]), strings.Split(id, "/")[2], testSubscription, 1)
				case "wrong-name":
					raw["name"] = "another"
				case "wrong-type":
					raw["type"] = vmType
				case "numeric-type":
					raw["type"] = 10
				case "missing-properties":
					delete(raw, "properties")
				case "aliased-properties":
					raw["Properties"] = props
				case "error":
					raw["error"] = map[string]any{"code": "Denied"}
				case "aliased-error":
					raw["Error"] = map[string]any{"code": "Denied"}
				case "next":
					raw["nextLink"] = "https://example.test"
				case "aliased-next":
					raw["NextLink"] = nil
				case "async":
					headers.Set("Azure-AsyncOperation", "https://management.azure.com/operations/pending")
				case "accepted":
					status = 202
				case "denied":
					status = 403
				case "missing":
					status = 404
				case "opaque-tags":
					raw["tags"] = "opaque"
				case "invalid-tag":
					raw["tags"] = map[string]any{"protected": true}
				case "invalid-location":
					raw["location"] = true
				case "bad-config":
					if kind == diagnosticCategoryType {
						props["categoryGroups"] = true
					} else {
						props["logs"] = []any{map[string]any{"enabled": "true"}}
					}
				case "aliased-config":
					if kind == diagnosticCategoryType {
						props["CategoryType"] = "Logs"
					} else {
						props["StorageAccountId"] = ""
					}
				}
				c := directClient(func(*http.Request) (*http.Response, error) { return jsonResponse(status, raw, headers), nil })
				c.subscription = strings.Split(id, "/")[2]
				if _, err := c.diagnosticRead(t.Context(), id, kind); err == nil || isNotFound(err) != (mode == "missing") {
					t.Fatal("diagnostic read boundary failed", err)
				}
			})
		}
	}
}

func TestDiagnosticIndexBoundaries(t *testing.T) {
	for _, file := range []string{"getDiagnosticSetting.json", "getSubscriptionDiagnosticSetting.json", "getDiagnosticSettingsCategory.json"} {
		for _, mode := range []string{"duplicate", "foreign-id", "empty-row", "no-value", "null-value", "aliased-value", "aliased-next", "get-missing", "private-change", "denied", "index-missing", "async", "error", "filter", "wrong-version", "foreign-collection", "foreign-subscription", "foreign-host", "port", "userinfo", "fragment", "duplicate-token", "repeat-token", "subscription-page"} {
			t.Run(file+"/"+mode, func(t *testing.T) {
				raw := diagnosticBody(t, file)
				id, scope, kind, _ := diagnosticResourceID(text(raw["id"]))
				if mode == "subscription-page" && len(strings.Split(scope, "/")) != 3 {
					return
				}
				collection := scope + "/providers/" + kind
				pages := 0
				c := directClient(func(r *http.Request) (*http.Response, error) {
					if r.Method != "GET" {
						t.Fatal("diagnostic inventory mutated resource")
					}
					if strings.EqualFold(r.URL.Path, collection) {
						pages++
						if pages > 2 {
							t.Fatal("diagnostic pagination repeated")
						}
						body := map[string]any{"value": []any{raw}}
						status, headers := 200, http.Header{}
						next := apiURL(collection, diagnosticSettingsVersion) + "&$skiptoken=second"
						switch mode {
						case "duplicate":
							body["value"] = []any{raw, raw}
						case "foreign-id":
							copy := maps.Clone(raw)
							copy["id"] = strings.Replace(text(raw["id"]), strings.Split(id, "/")[2], testSubscription, 1)
							body["value"] = []any{copy}
						case "empty-row":
							body["value"] = []any{nil}
						case "no-value":
							delete(body, "value")
						case "null-value":
							body["value"] = nil
						case "aliased-value":
							body["Value"] = []any{}
						case "aliased-next":
							body["NextLink"] = nil
						case "denied":
							status = 403
						case "index-missing":
							status = 404
						case "async":
							headers.Set("Azure-AsyncOperation", "https://management.azure.com/operations/pending")
						case "error":
							body["code"] = "Denied"
						case "filter":
							body["nextLink"] = next + "&$filter=enabled"
						case "wrong-version":
							body["nextLink"] = strings.Replace(next, diagnosticSettingsVersion, "2000-01-01", 1)
						case "foreign-collection":
							body["nextLink"] = apiURL(scope+"/other", diagnosticSettingsVersion)
						case "foreign-subscription":
							body["nextLink"] = strings.Replace(next, strings.Split(id, "/")[2], testSubscription, 1)
						case "foreign-host":
							body["nextLink"] = strings.Replace(next, "management.azure.com", "foreign.example.test", 1)
						case "port":
							body["nextLink"] = strings.Replace(next, "management.azure.com", "management.azure.com:443", 1)
						case "userinfo":
							body["nextLink"] = strings.Replace(next, "management.azure.com", "reader@management.azure.com", 1)
						case "fragment":
							body["nextLink"] = next + "#fragment"
						case "duplicate-token":
							body["nextLink"] = next + "&$skiptoken=third"
						case "repeat-token", "subscription-page":
							body["value"], body["nextLink"] = []any{}, next
						}
						return jsonResponse(status, body, headers), nil
					}
					if !strings.EqualFold(r.URL.Path, id) {
						t.Fatal("diagnostic continuation escaped scope", r.URL)
					}
					if mode == "get-missing" {
						return jsonResponse(404, map[string]any{"error": map[string]any{"code": "ResourceNotFound"}}, nil), nil
					}
					if mode == "private-change" {
						copy := maps.Clone(raw)
						copy["properties"] = maps.Clone(object(raw["properties"]))
						object(copy["properties"])["ordinaryAuthoredField"] = "changed"
						return jsonResponse(200, copy, nil), nil
					}
					return jsonResponse(200, raw, nil), nil
				})
				c.subscription = strings.Split(id, "/")[2]
				if _, _, err := c.diagnosticIndex(t.Context(), scope, kind); err == nil || isNotFound(err) {
					t.Fatal("incomplete diagnostic index became absence", err)
				}
			})
		}
	}
}

func TestDiagnosticReferencesAndPrivateConfiguration(t *testing.T) {
	raw := diagnosticBody(t, "getDiagnosticSetting.json")
	id, source, _, _ := diagnosticResourceID(text(raw["id"]))
	props := object(raw["properties"])
	before, _ := json.Marshal(raw)
	refs, err := diagnosticReferences(id, raw)
	if err != nil || !slices.Equal(refs["Microsoft.Logic/workflows"], []string{source}) || len(refs["Microsoft.Storage/storageAccounts"]) != 1 || len(refs["Microsoft.EventHub/namespaces/authorizationRules"]) != 1 || len(refs["Microsoft.EventHub/namespaces"]) != 1 || len(refs["Microsoft.EventHub/namespaces/eventhubs"]) != 0 || len(refs["microsoft.datadog/monitors"]) != 1 {
		t.Fatal("diagnostic source or shared destinations lost", refs, err)
	}
	baseline := diagnosticSnapshot(raw)
	after, _ := json.Marshal(raw)
	if string(before) != string(after) {
		t.Fatal("diagnostic snapshot mutated native response")
	}
	c := directClient(nil)
	changed := maps.Clone(raw)
	changed["properties"] = maps.Clone(props)
	object(changed["properties"])["ordinaryAuthoredField"] = map[string]any{"private": "changed"}
	if c.privateConfiguration(baseline) == c.privateConfiguration(diagnosticSnapshot(changed)) {
		t.Fatal("unknown private configuration was ignored")
	}
	props["eventHubName"] = "explicit-hub"
	props["serviceBusRuleId"] = resourceID("Microsoft.ServiceBus/namespaces", "legacy") + "/authorizationRules/send"
	refs, err = diagnosticReferences(id, raw)
	if err != nil || len(refs["Microsoft.EventHub/namespaces/eventhubs"]) != 1 || len(refs["Microsoft.ServiceBus/namespaces"]) != 1 || len(refs["Microsoft.ServiceBus/namespaces/authorizationRules"]) != 1 {
		t.Fatal("explicit and legacy destinations lost", refs, err)
	}
	for field, value := range map[string]any{"storageAccountId": resourceID(vmType, "wrong"), "workspaceId": true, "marketplacePartnerId": "https://partner.example.test", "eventHubAuthorizationRuleId": resourceID("Microsoft.EventHub/namespaces", "ns") + "/eventhubs/hub/authorizationRules/wrong", "serviceBusRuleId": "opaque", "eventHubName": "nested/hub"} {
		copy := maps.Clone(raw)
		copy["properties"] = maps.Clone(props)
		object(copy["properties"])[field] = value
		if _, err := diagnosticReferences(id, copy); err == nil {
			t.Fatal("malformed diagnostic destination accepted", field)
		}
	}
}
