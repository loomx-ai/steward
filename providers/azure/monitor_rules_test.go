package azure

import (
	"encoding/json"
	"maps"
	"net/http"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func monitorRuleExample(t *testing.T, file string) map[string]any {
	t.Helper()
	data, err := os.ReadFile("fixtures/monitoralerts/" + file)
	if strings.HasPrefix(file, "webtest/") {
		data, err = os.ReadFile("fixtures/applicationinsights/stable/2022-06-15/examples/" + strings.TrimPrefix(file, "webtest/"))
		// Only the literal subscription placeholder is adapted. The native
		// WebTest Get body disagrees with its example request's resource group;
		// these read tests bind to the retained body's own complete identity.
		data = []byte(strings.ReplaceAll(string(data), "/subscriptions/subid/", "/subscriptions/"+testSubscription+"/"))
	}
	var example map[string]any
	if err != nil || json.Unmarshal(data, &example) != nil {
		t.Fatal("invalid native monitor fixture", file, err)
	}
	return object(object(object(example["responses"])["200"])["body"])
}

func monitorRuleGetFiles() []string {
	return []string{
		"metric-2026-01-01/getMetricAlertSingleResource.json", "metric-2026-01-01/getMetricAlertMultipleResource.json",
		"metric-2026-01-01/getMetricAlertSubscription.json", "metric-2026-01-01/getMetricAlertResourceGroup.json",
		"metric-2026-01-01/getDynamicMetricAlertSingleResource.json", "metric-2026-01-01/getDynamicMetricAlertMultipleResource.json",
		"metric-2026-01-01/getMetricAlertQuery.json", "metric-2026-01-01/getWebTestMetricAlert.json",
		"actions-2023-01-01/getActionGroup.json", "activity-2026-01-01/ActivityLogAlertRule_Get.json",
		"scheduled-2026-03-01/getScheduledQueryRule.json", "smart-2021-04-01/SmartDetectorAlertRule_Get.json",
		"prometheus-2023-03-01/getPrometheusRuleGroup.json", "processing-2021-08-08/AlertProcessingRules_GetById.json",
		"webtest/WebTestGet.json",
	}
}

func monitorRuleComposedIdentity(t *testing.T, raw map[string]any, file string) map[string]any {
	t.Helper()
	// The retained metric examples have a duplicated /providers segment;
	// Alert Processing examples use bare placeholder names as action-group IDs.
	// First prove that these cannot authorize cleanup, then compose valid IDs
	// only in the runtime scenario. The source files remain unchanged.
	if strings.Contains(text(raw["id"]), "/providers/providers/") {
		if _, _, err := parseID(text(raw["id"])); err == nil {
			t.Fatal("malformed original metric identity was accepted", file)
		}
		raw = maps.Clone(raw)
		raw["id"] = strings.Replace(text(raw["id"]), "/providers/providers/", "/providers/", 1)
	}
	if strings.HasPrefix(file, "processing-") {
		props := object(raw["properties"])
		for _, value := range array(props["actions"]) {
			action := object(value)
			if action["actionGroupIds"] == nil {
				continue
			}
			if _, err := monitorRuleActionGroups(monitorProcessingType, raw); err == nil {
				t.Fatal("bare original action-group placeholder was accepted", file)
			}
			var ids []any
			for _, value := range array(action["actionGroupIds"]) {
				if value != "actiongGroup1" && value != "actiongGroup2" {
					t.Fatal("native placeholder changed", value)
				}
				parts := strings.Split(text(raw["id"]), "/")
				ids = append(ids, strings.Join(parts[:5], "/")+"/providers/Microsoft.Insights/actionGroups/"+text(value))
			}
			action["actionGroupIds"] = ids
		}
	}
	return raw
}

func TestMonitorRuleNativeReads(t *testing.T) {
	for _, file := range monitorRuleGetFiles() {
		t.Run(file, func(t *testing.T) {
			raw := monitorRuleComposedIdentity(t, monitorRuleExample(t, file), file)
			id, typ, err := parseID(text(raw["id"]))
			if err != nil {
				t.Fatal("invalid source identity", err)
			}
			row := monitorRuleKind(typ)
			var wire string
			c := directClient(func(r *http.Request) (*http.Response, error) {
				if r.Method != "GET" || !strings.EqualFold(r.URL.Path, id) || r.URL.Query().Get("api-version") != row.version || r.Header.Get("If-Match") != "" {
					t.Fatal("native monitor GET selector changed", r.URL)
				}
				wire = r.URL.String()
				return jsonResponse(200, raw, nil), nil
			})
			c.subscription = strings.Split(id, "/")[2]
			result, err := c.monitorRuleRead(t.Context(), row.kind, id)
			if err != nil || wire == "" || text(result.data["id"]) != text(raw["id"]) {
				t.Fatal("native monitor GET failed", err)
			}
			deletion, err := c.monitorRuleRequest(row.kind, id, "DELETE")
			if err != nil || deletion.URL != wire || deletion.Method != "DELETE" || len(deletion.Body) != 0 || deletion.Headers["If-Match"] != "" {
				t.Fatal("native monitor DELETE binding differs", err, deletion)
			}
		})
	}
}

func TestMonitorRuleNativeIndexes(t *testing.T) {
	for _, file := range []string{
		"metric-2026-01-01/listMetricAlert.json", "actions-2023-01-01/listActionGroups.json",
		"activity-2026-01-01/ActivityLogAlertRule_ListBySubscriptionId.json", "scheduled-2026-03-01/listScheduledQueryRulesBySubscription.json",
		"smart-2021-04-01/SmartDetectorAlertRule_List.json", "prometheus-2023-03-01/listSubscriptionPrometheusRuleGroups.json",
		"processing-2021-08-08/AlertProcessingRules_List_Subscription.json", "webtest/WebTestList.json",
	} {
		t.Run(file, func(t *testing.T) {
			body := monitorRuleExample(t, file)
			items := array(body["value"])
			for i, raw := range items {
				items[i] = monitorRuleComposedIdentity(t, object(raw), file)
			}
			id, typ, err := parseID(text(object(items[0])["id"]))
			if err != nil {
				t.Fatal(err)
			}
			row := monitorRuleKind(typ)
			root := "/subscriptions/" + strings.Split(id, "/")[2]
			collection := root + "/providers/" + row.kind
			native, reads, pages := map[string]map[string]any{}, 0, 0
			for _, value := range items {
				raw := object(value)
				id, _, _ := parseID(text(raw["id"]))
				native[id] = raw
			}
			c := directClient(func(r *http.Request) (*http.Response, error) {
				if r.Method != "GET" || r.URL.Query().Get("api-version") != row.version {
					t.Fatal("invalid native index request", r.URL)
				}
				if strings.EqualFold(r.URL.Path, collection) {
					pages++
					if r.URL.Query().Get("ctoken") != "" {
						return jsonResponse(200, map[string]any{"value": []any{}}, nil), nil
					}
					// Split the retained native list into pages. Individual reads
					// below use those original rows; this is a composed scenario.
					if r.URL.Query().Get("$skiptoken") == "" {
						return jsonResponse(200, map[string]any{"value": items[:1], "nextLink": apiURL(collection, row.version) + "&$skiptoken=second"}, nil), nil
					}
					return jsonResponse(200, map[string]any{"value": items[1:], "nextLink": body["nextLink"]}, nil), nil
				}
				if raw := native[strings.ToLower(r.URL.Path)]; raw != nil {
					reads++
					return jsonResponse(200, raw, nil), nil
				}
				t.Fatal("unexpected monitor request", r.URL)
				return nil, nil
			})
			c.subscription = strings.Split(id, "/")[2]
			values, _, err := c.monitorRuleIndex(t.Context(), row.kind)
			expectedPages := 2
			if body["nextLink"] != nil && text(body["nextLink"]) != "" {
				expectedPages++
			}
			if err != nil || len(values) != len(items) || reads != len(items) || pages != expectedPages {
				t.Fatal("native monitor index failed", err, len(values), reads, pages)
			}
		})
	}
}

func TestMonitorRuleReferenceShapesAndPrivatePayloads(t *testing.T) {
	group := strings.ToLower(resourceID(monitorActionGroupType, "shared"))
	for _, test := range []struct {
		kind  string
		props map[string]any
	}{
		{monitorMetricAlertType, map[string]any{"actions": []any{map[string]any{"actionGroupId": group, "webHookProperties": map[string]any{"ordinary": group}}}}},
		{monitorActivityAlertType, map[string]any{"actions": map[string]any{"actionGroups": []any{map[string]any{"actionGroupId": group}}}}},
		{monitorScheduledRuleType, map[string]any{"actions": map[string]any{"actionGroups": []any{group}}}},
		{monitorSmartAlertType, map[string]any{"actionGroups": []any{map[string]any{"actionGroupId": group}}}},
		{monitorSmartAlertType, map[string]any{"actionGroups": map[string]any{"groupIds": []any{group}, "customWebhookPayload": group}}},
		{monitorPrometheusType, map[string]any{"rules": []any{map[string]any{"actions": []any{map[string]any{"actionGroupId": group}}}, map[string]any{"actions": []any{map[string]any{"actionGroupId": group}}}}}},
		{monitorProcessingType, map[string]any{"actions": []any{map[string]any{"actionType": "AddActionGroups", "actionGroupIds": []any{group}}}}},
	} {
		t.Run(test.kind, func(t *testing.T) {
			test.props["criteria"] = map[string]any{"query": resourceID(monitorActionGroupType, "private-query")}
			test.props["conditions"] = []any{map[string]any{"values": []any{resourceID(monitorActionGroupType, "private-filter")}}}
			ids, err := monitorRuleActionGroups(test.kind, map[string]any{"properties": test.props})
			if err != nil || !slices.Equal(ids, []string{group}) {
				t.Fatal("native reference shape was not preserved", err, ids)
			}
		})
	}
	for _, actions := range []any{true, []any{group}, []any{map[string]any{}}, []any{map[string]any{"actionGroupId": " " + group}}, []any{map[string]any{"actionGroupId": resourceID(vmType, "other")}}, []any{map[string]any{"actionGroupId": "https://example.invalid/group"}}} {
		if _, err := monitorRuleActionGroups(monitorMetricAlertType, map[string]any{"properties": map[string]any{"actions": actions}}); err == nil {
			t.Fatal("malformed action group reference accepted", actions)
		}
	}
	for _, actions := range []any{map[string]any{}, true, map[string]any{"groupIds": "wrong"}} {
		if _, err := monitorRuleActionGroups(monitorSmartAlertType, map[string]any{"properties": map[string]any{"actionGroups": actions}}); err == nil {
			t.Fatal("malformed smart detector shape accepted", actions)
		}
	}
	for _, scopes := range []any{nil, true, []any{nil}, []any{"/subscriptions/bad"}, []any{group, strings.ToUpper(group)}, []any{" " + group}, []any{"https://example.invalid"}} {
		if _, err := monitorRuleScopes(monitorMetricAlertType, map[string]any{"properties": map[string]any{"scopes": scopes}}); err == nil {
			t.Fatal("malformed monitor scope accepted", scopes)
		}
	}
	scopes, err := monitorRuleScopes(monitorMetricAlertType, map[string]any{"properties": map[string]any{"scopes": []any{"/subscriptions/" + testSubscription, resourceID(groupType, "test"), group}}})
	if err != nil || len(scopes) != 3 {
		t.Fatal("native broad scopes rejected", err, scopes)
	}
}

func TestMonitorRuleSnapshotPrivacyAndObservations(t *testing.T) {
	for _, file := range monitorRuleGetFiles() {
		raw := monitorRuleComposedIdentity(t, monitorRuleExample(t, file), file)
		_, typ, _ := parseID(text(raw["id"]))
		kind := monitorRuleKind(typ).kind
		before, _ := json.Marshal(raw)
		baseline := monitorRuleSnapshot(kind, raw)
		props := maps.Clone(object(raw["properties"]))
		changed := maps.Clone(raw)
		changed["properties"] = props
		switch kind {
		case monitorMetricAlertType:
			props["lastUpdatedTime"], props["isMigrated"] = "later", true
		case monitorScheduledRuleType:
			props["isWorkspaceAlertsStorageConfigured"] = true
		case monitorSmartAlertType:
			detector := maps.Clone(object(props["detector"]))
			detector["name"], detector["parameterDefinitions"] = "metadata changed", []any{"new metadata"}
			props["detector"] = detector
		case monitorActionGroupType:
			receivers := slices.Clone(array(props["emailReceivers"]))
			receiver := maps.Clone(object(receivers[0]))
			receiver["status"] = "Disabled"
			receivers[0], props["emailReceivers"] = receiver, receivers
		case insightsWebTestType:
			props["provisioningState"] = "Updating"
		}
		if !reflect.DeepEqual(baseline, monitorRuleSnapshot(kind, changed)) {
			t.Fatal("readonly observation changed configuration", kind)
		}
		props["ordinaryAuthoredField"] = "private-change"
		if reflect.DeepEqual(baseline, monitorRuleSnapshot(kind, changed)) {
			t.Fatal("authored configuration was ignored", kind)
		}
		after, _ := json.Marshal(raw)
		if string(before) != string(after) {
			t.Fatal("configuration proof changed original native data", kind)
		}
	}
}

func TestMonitorRuleReadBoundaries(t *testing.T) {
	for _, file := range monitorRuleGetFiles() {
		for _, mode := range []string{"foreign-id", "wrong-type", "numeric-type", "wrong-name", "missing-properties", "empty-properties", "bad-tags", "opaque-tags", "foreign-next", "async", "accepted", "denied", "not-found", "aliased-properties"} {
			t.Run(file+"/"+mode, func(t *testing.T) {
				raw := monitorRuleComposedIdentity(t, monitorRuleExample(t, file), file)
				id, typ, _ := parseID(text(raw["id"]))
				row := monitorRuleKind(typ)
				status, headers := 200, http.Header{}
				switch mode {
				case "foreign-id":
					raw["id"] = resourceID(row.kind, "another")
				case "wrong-type":
					raw["type"] = vmType
				case "numeric-type":
					raw["type"] = 12
				case "wrong-name":
					raw["name"] = "another"
				case "missing-properties":
					delete(raw, "properties")
				case "empty-properties":
					raw["properties"] = map[string]any{}
				case "bad-tags":
					raw["tags"] = map[string]any{"protected": true}
				case "opaque-tags":
					raw["tags"] = "opaque"
				case "foreign-next":
					raw["nextLink"] = "https://example.invalid/next"
				case "async":
					headers.Set("Azure-AsyncOperation", "https://management.azure.com/operation")
				case "accepted":
					status = 202
				case "denied":
					status = 403
				case "not-found":
					status = 404
				case "aliased-properties":
					raw["Properties"] = raw["properties"]
				}
				c := directClient(func(*http.Request) (*http.Response, error) { return jsonResponse(status, raw, headers), nil })
				c.subscription = strings.Split(id, "/")[2]
				_, err := c.monitorRuleRead(t.Context(), row.kind, id)
				if err == nil || isNotFound(err) != (mode == "not-found") {
					t.Fatal("native monitor read boundary failed", err)
				}
			})
		}
	}
}

func TestMonitorRuleIndexBoundaries(t *testing.T) {
	for _, file := range monitorRuleGetFiles()[7:] {
		for _, mode := range []string{"duplicate", "foreign-id", "empty-row", "no-value", "null-value", "get-not-found", "private-change", "denied", "async", "filter", "wrong-version", "foreign-collection", "foreign-subscription", "wrong-port", "userinfo", "fragment", "duplicate-token", "repeat-token"} {
			t.Run(file+"/"+mode, func(t *testing.T) {
				raw := monitorRuleComposedIdentity(t, monitorRuleExample(t, file), file)
				id, typ, _ := parseID(text(raw["id"]))
				row := monitorRuleKind(typ)
				root := "/subscriptions/" + strings.Split(id, "/")[2]
				collection := root + "/providers/" + row.kind
				pages, gets := 0, 0
				c := directClient(func(r *http.Request) (*http.Response, error) {
					if r.Method != "GET" {
						t.Fatal("unexpected mutation")
					}
					if strings.EqualFold(r.URL.Path, id) {
						gets++
						if mode == "get-not-found" {
							return jsonResponse(404, map[string]any{}, nil), nil
						}
						current := maps.Clone(raw)
						if mode == "private-change" {
							current["tags"] = map[string]any{"late": "configuration-change"}
						}
						return jsonResponse(200, current, nil), nil
					}
					if !strings.EqualFold(r.URL.Path, collection) {
						t.Fatal("foreign collection requested", r.URL)
					}
					pages++
					if pages > 2 {
						t.Fatal("unbounded monitor paging")
					}
					body := map[string]any{"value": []any{raw}}
					status, headers := 200, http.Header{}
					next := apiURL(collection, row.version) + "&$skiptoken=second"
					switch mode {
					case "duplicate":
						body["value"] = []any{raw, raw}
					case "foreign-id":
						other := maps.Clone(raw)
						other["id"] = resourceID(row.kind, "another")
						body["value"] = []any{other}
					case "empty-row":
						body["value"] = []any{nil}
					case "no-value":
						delete(body, "value")
					case "null-value":
						body["value"] = nil
					case "denied":
						status = 403
					case "async":
						headers.Set("Azure-AsyncOperation", apiURL(id, row.version))
					case "filter":
						body["nextLink"] = next + "&$filter=enabled"
					case "wrong-version":
						body["nextLink"] = apiURL(collection, "2000-01-01") + "&$skiptoken=second"
					case "foreign-collection":
						body["nextLink"] = apiURL(root+"/resources", row.version)
					case "foreign-subscription":
						body["nextLink"] = strings.Replace(next, strings.Split(id, "/")[2], "eeeeeeee-eeee-eeee-eeee-eeeeeeeeeeee", 1)
					case "wrong-port":
						body["nextLink"] = strings.Replace(next, "management.azure.com", "management.azure.com:444", 1)
					case "userinfo":
						body["nextLink"] = strings.Replace(next, "management.azure.com", "reader@management.azure.com", 1)
					case "fragment":
						body["nextLink"] = next + "#fragment"
					case "duplicate-token":
						body["nextLink"] = next + "&$skiptoken=third"
					case "repeat-token":
						body["nextLink"] = next
						if pages == 2 {
							body["value"] = []any{}
						}
					}
					return jsonResponse(status, body, headers), nil
				})
				c.subscription = strings.Split(id, "/")[2]
				if _, _, err := c.monitorRuleIndex(t.Context(), row.kind); err == nil {
					t.Fatal("unsafe monitor index accepted", pages, gets)
				}
			})
		}
	}
}

func TestMonitorRuleContinuationContextAndReferenceAliases(t *testing.T) {
	path := "/subscriptions/" + testSubscription + "/providers/Microsoft.AlertsManagement/actionRules"
	native := "https://management.azure.com:443" + path + "?api-version=2021-08-08&ctoken=%2BRID%3Anative"
	expected := strings.Replace(native, ":443", "", 1)
	if actual := monitorRuleNextLink(native); actual != expected {
		t.Fatal("native default-port continuation changed", actual)
	}
	for _, changed := range []string{
		strings.Replace(native, "https://", "http://", 1),
		strings.Replace(native, ":443", ":444", 1),
		strings.Replace(native, "management.azure.com", "other.azure.com", 1),
		strings.Replace(native, "management.azure.com", "reader@management.azure.com", 1),
		strings.Replace(native, "2021-08-08", "2020-01-01", 1),
		strings.Replace(native, "actionRules", "smartDetectorAlertRules", 1),
		strings.Replace(native, "/actionRules?", "/actionRules/item?", 1),
	} {
		if monitorRuleNextLink(changed) != changed {
			t.Fatal("native continuation exception widened", changed)
		}
	}
	group := resourceID(monitorActionGroupType, "shared")
	for _, test := range []struct {
		kind  string
		props map[string]any
	}{
		{monitorMetricAlertType, map[string]any{"Actions": []any{map[string]any{"actionGroupId": group}}}},
		{monitorMetricAlertType, map[string]any{"actions": []any{map[string]any{"actionGroupID": group}}}},
		{monitorActivityAlertType, map[string]any{"actions": map[string]any{"ActionGroups": []any{group}}}},
		{monitorScheduledRuleType, map[string]any{"actions": map[string]any{"ActionGroups": []any{group}}}},
		{monitorSmartAlertType, map[string]any{"actionGroups": map[string]any{"GroupIds": []any{group}}}},
		{monitorPrometheusType, map[string]any{"rules": []any{map[string]any{"Actions": []any{group}}}}},
		{monitorProcessingType, map[string]any{"actions": []any{map[string]any{"ActionType": "AddActionGroups", "actionGroupIds": []any{group}}}}},
	} {
		if _, err := monitorRuleActionGroups(test.kind, map[string]any{"properties": test.props}); err == nil {
			t.Fatal("ambiguous native reference field accepted", test.kind)
		}
	}
}
