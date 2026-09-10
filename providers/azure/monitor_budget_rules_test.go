package azure

import (
	"bytes"
	"encoding/json"
	"maps"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"
)

func monitorBudgetExample(t *testing.T, file string) map[string]any {
	t.Helper()
	data, err := os.ReadFile("fixtures/monitorbudgets/" + file)
	if err != nil {
		t.Fatal(err)
	}
	var example map[string]any
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(&example); err != nil {
		t.Fatal(err)
	}
	return object(object(object(example["responses"])["200"])["body"])
}

func TestMonitorBudgetNativeReads(t *testing.T) {
	for _, file := range []string{"consumption-2024-08-01/Budget.json", "cost-management-2025-03-01/Budgets/Get/Cost/Get-Cost-Budget.json"} {
		t.Run(file, func(t *testing.T) {
			raw := monitorBudgetExample(t, file)
			id, scope, kind, err := monitorBudgetID(text(raw["id"]))
			if err != nil {
				t.Fatal(err)
			}
			_, version := monitorBudgetKind(kind)
			c := directClient(func(r *http.Request) (*http.Response, error) {
				if r.Method != "GET" || !strings.EqualFold(r.URL.Path, id) || r.URL.Query().Get("api-version") != version || r.Header.Get("If-Match") != "" {
					t.Fatal("budget read binding changed", r.URL)
				}
				return jsonResponse(200, raw, nil), nil
			})
			c.subscription = strings.Split(id, "/")[2]
			result, err := c.monitorBudgetRead(t.Context(), id)
			if err != nil || c.privateConfiguration(result.data) != c.privateConfiguration(raw) {
				t.Fatal("native budget read failed", err)
			}
			deletion, err := c.monitorBudgetRequest(kind, scope, last(id), "DELETE")
			if err != nil || !strings.EqualFold(deletion.URL, apiURL(id, version)) || len(deletion.Headers) != 0 || len(deletion.Body) != 0 {
				t.Fatal("native budget deletion binding changed", deletion, err)
			}
			// Both original Get examples request a subscription budget while
			// returning a group budget. Keep the original body and reject that
			// identity mismatch; the valid read above binds to the body identity.
			if monitorBudgetIdentity(raw, c.root()+"/providers/"+strings.ToLower(kind)+"/"+last(id), kind) == nil {
				t.Fatal("original Get scope mismatch was accepted")
			}
			groups, err := monitorBudgetActionGroups(raw)
			if err != nil || len(groups) != 1 || !strings.HasSuffix(groups[0], "/actiongroups/sampleactiongroup") {
				t.Fatal("native budget contact group missing", groups, err)
			}
		})
	}
}

func TestMonitorBudgetNativeIndexes(t *testing.T) {
	for _, file := range []string{"consumption-2024-08-01/BudgetsList.json", "cost-management-2025-03-01/Budgets/List/RBAC/SubscriptionBudgetsList.json", "cost-management-2025-03-01/Budgets/List/RBAC/ResourceGroupBudgetsList.json"} {
		t.Run(file, func(t *testing.T) {
			rows := array(monitorBudgetExample(t, file)["value"])
			id, scope, kind, err := monitorBudgetID(text(object(rows[0])["id"]))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(file, "ResourceGroupBudgetsList") {
				scope = strings.Join(strings.Split(id, "/")[:3], "/")
			}
			_, version := monitorBudgetKind(kind)
			collection := scope + "/providers/" + strings.ToLower(kind)
			objects := map[string]map[string]any{}
			for _, value := range rows {
				raw := object(value)
				id, _, _, err := monitorBudgetID(text(raw["id"]))
				if err != nil {
					t.Fatal(err)
				}
				objects[id] = raw
			}
			pages, gets := 0, 0
			c := directClient(func(r *http.Request) (*http.Response, error) {
				if r.Method != "GET" || r.URL.Query().Get("api-version") != version {
					t.Fatal("budget index selector changed", r.URL)
				}
				if strings.EqualFold(r.URL.Path, collection) {
					pages++
					body := map[string]any{"value": rows[:1]}
					if pages == 1 {
						body["nextLink"] = apiURL(collection, version) + "&$skiptoken=native-test-page"
					} else if pages == 2 && r.URL.Query().Get("$skiptoken") == "native-test-page" {
						body["value"] = rows[1:]
					} else {
						t.Fatal("unexpected budget continuation", r.URL)
					}
					return jsonResponse(200, body, http.Header{"X-Ms-Request-Id": []string{"budget-list-request"}}), nil
				}
				gets++
				raw := objects[strings.ToLower(r.URL.Path)]
				if raw == nil {
					t.Fatal("unbound budget GET", r.URL)
				}
				return jsonResponse(200, raw, nil), nil
			})
			c.subscription = strings.Split(id, "/")[2]
			values, requestID, err := c.monitorBudgetIndex(t.Context(), kind, scope)
			if err != nil || len(values) != len(rows) || gets != len(rows) || pages != 2 || requestID != "budget-list-request" {
				t.Fatal("native budget list failed", err, len(values), pages, gets, requestID)
			}
		})
	}
}

func TestMonitorBudgetReferencesAndPrivateSnapshot(t *testing.T) {
	raw := monitorBudgetExample(t, "consumption-2024-08-01/Budget.json")
	props := object(raw["properties"])
	notification := object(object(props["notifications"])["Actual_GreaterThan_80_Percent"])
	group := text(array(notification["contactGroups"])[0])
	notification["contactGroups"] = []any{group, strings.ToUpper(group)}
	object(props["notifications"])["another"] = maps.Clone(notification)
	props["filter"] = map[string]any{"ordinary": resourceID(monitorActionGroupType, "opaque")}
	groups, err := monitorBudgetActionGroups(raw)
	if err != nil || !slices.Equal(groups, []string{strings.ToLower(group)}) {
		t.Fatal("explicit budget references changed", groups, err)
	}
	c := directClient(nil)
	proof := c.privateConfiguration(monitorBudgetSnapshot(raw))
	props["currentSpend"], props["forecastSpend"] = map[string]any{"amount": 999}, map[string]any{"amount": 1000}
	if proof != c.privateConfiguration(monitorBudgetSnapshot(raw)) {
		t.Fatal("read-only spend altered authored configuration")
	}
	notification["contactEmails"] = []any{"changed@example.test"}
	if proof == c.privateConfiguration(monitorBudgetSnapshot(raw)) {
		t.Fatal("private budget recipient drift ignored")
	}
	for _, value := range []any{nil, "group", []any{nil}, []any{"SampleActionGroup"}, []any{resourceID(vmType, "wrong-kind")}, []any{" " + group}, []any{group + "?secret=true"}} {
		notification["contactGroups"] = value
		if _, err := monitorBudgetActionGroups(raw); err == nil {
			t.Fatal("malformed budget reference accepted", value)
		}
	}
	for _, field := range []string{"ContactGroups", "contactgroups", "CONTACTGROUPS"} {
		delete(notification, "contactGroups")
		notification[field] = []any{group}
		if _, err := monitorBudgetActionGroups(raw); err == nil {
			t.Fatal("ambiguous budget reference key accepted", field)
		}
		delete(notification, field)
	}
	props["Notifications"] = props["notifications"]
	if _, err := monitorBudgetActionGroups(raw); err == nil {
		t.Fatal("ambiguous budget notification map accepted")
	}
}

func TestMonitorBudgetReadAndIndexBoundaries(t *testing.T) {
	for _, kind := range []string{monitorConsumptionBudgetType, monitorCostBudgetType} {
		for _, mode := range []string{"duplicate", "foreign", "wrong-kind", "missing-id", "id-whitespace", "missing-properties", "missing-notification", "null-notification", "invalid-group", "get-private-drift", "get-404", "get-403", "get-202", "list-403", "list-202", "list-lro", "list-missing-value", "list-null-value", "list-upper-next", "list-upper-value", "filter", "version", "collection", "subscription", "host", "port", "userinfo", "fragment", "double-token", "cycle"} {
			t.Run(kind+"/"+mode, func(t *testing.T) {
				file := "consumption-2024-08-01/BudgetsList.json"
				if kind == monitorCostBudgetType {
					file = "cost-management-2025-03-01/Budgets/List/RBAC/SubscriptionBudgetsList.json"
				}
				raw := object(array(monitorBudgetExample(t, file)["value"])[0])
				id, _, _, err := monitorBudgetID(text(raw["id"]))
				if err != nil {
					t.Fatal(err)
				}
				root := strings.Join(strings.Split(id, "/")[:3], "/")
				collection := root + "/providers/" + strings.ToLower(kind)
				_, version := monitorBudgetKind(kind)
				pages, gets := 0, 0
				c := directClient(func(r *http.Request) (*http.Response, error) {
					if r.Method != "GET" || r.URL.Host != "management.azure.com" || !strings.HasPrefix(strings.ToLower(r.URL.Path), root+"/") {
						t.Fatal("unscoped request reached transport", r.URL)
					}
					if strings.EqualFold(r.URL.Path, collection) {
						pages++
						if pages > 2 {
							t.Fatal("unbounded budget continuation")
						}
						status, headers := 200, http.Header{}
						body := map[string]any{"value": []any{raw}}
						next := apiURL(collection, version) + "&skiptoken=more"
						switch mode {
						case "duplicate":
							body["value"] = []any{raw, raw}
						case "foreign":
							raw["id"] = strings.Replace(text(raw["id"]), strings.Split(id, "/")[2], testSubscription, 1)
						case "wrong-kind":
							raw["type"] = vmType
						case "missing-id":
							delete(raw, "id")
						case "id-whitespace":
							raw["id"] = " " + text(raw["id"])
						case "missing-properties":
							raw["properties"] = map[string]any{}
						case "missing-notification":
							object(raw["properties"])["notifications"] = map[string]any{"broken": map[string]any{}}
						case "null-notification":
							object(raw["properties"])["notifications"] = nil
						case "invalid-group":
							object(raw["properties"])["notifications"] = map[string]any{"broken": map[string]any{"contactGroups": []any{"group"}}}
						case "list-403":
							status = 403
						case "list-202":
							status = 202
						case "list-lro":
							headers.Set("Azure-AsyncOperation", apiURL(root+"/providers/Microsoft.CostManagement/operations/1", version))
						case "list-missing-value":
							delete(body, "value")
						case "list-null-value":
							body["value"] = nil
						case "list-upper-next":
							body["NextLink"] = next
						case "list-upper-value":
							body["Value"] = body["value"]
						case "filter":
							body["nextLink"] = next + "&$filter=properties/category%20eq%20'Cost'"
						case "version":
							body["nextLink"] = strings.Replace(next, version, "2001-01-01", 1)
						case "collection":
							body["nextLink"] = strings.Replace(next, "/budgets?", "/unrelated?", 1)
						case "subscription":
							body["nextLink"] = strings.Replace(next, strings.Split(id, "/")[2], testSubscription, 1)
						case "host":
							body["nextLink"] = strings.Replace(next, "management.azure.com", "foreign.example.test", 1)
						case "port":
							body["nextLink"] = strings.Replace(next, "management.azure.com", "management.azure.com:443", 1)
						case "userinfo":
							body["nextLink"] = strings.Replace(next, "management.azure.com", "reader@management.azure.com", 1)
						case "fragment":
							body["nextLink"] = next + "#fragment"
						case "double-token":
							body["nextLink"] = next + "&$skiptoken=another"
						case "cycle":
							body["nextLink"] = next
						}
						return jsonResponse(status, body, headers), nil
					}
					gets++
					if !strings.EqualFold(r.URL.Path, id) {
						t.Fatal("unbound budget GET", r.URL)
					}
					status := 200
					body := maps.Clone(raw)
					switch mode {
					case "get-private-drift":
						props := maps.Clone(object(raw["properties"]))
						body["properties"] = props
						props["filter"] = map[string]any{"dimensions": map[string]any{"name": "added-private-dimension"}}
					case "get-404":
						status = 404
					case "get-403":
						status = 403
					case "get-202":
						status = 202
					}
					return jsonResponse(status, body, nil), nil
				})
				c.subscription = strings.Split(id, "/")[2]
				if _, _, err := c.monitorBudgetIndex(t.Context(), kind, root); err == nil || isNotFound(err) {
					t.Fatal("incomplete budget index accepted as presence or absence", err, pages, gets)
				}
			})
		}
	}
}

func TestMonitorBudgetIdentityScopeBoundaries(t *testing.T) {
	root := "/subscriptions/" + testSubscription
	for _, kind := range []string{monitorConsumptionBudgetType, monitorCostBudgetType} {
		for _, scope := range []string{root, root + "/resourceGroups/group"} {
			id := scope + "/providers/" + kind + "/budget"
			canonical, actualScope, actualKind, err := monitorBudgetID(id)
			if err != nil || canonical != strings.ToLower(id) || actualScope != strings.ToLower(scope) || actualKind != kind {
				t.Fatal("valid native budget scope rejected", id, err)
			}
			for _, bad := range []string{id + "/child", id + "?query=1", id + "#fragment", id + "%2fescape", id + "\\child", " " + id, id + " ", strings.Replace(id, "/budget", "/..", 1), strings.Replace(id, testSubscription, "subid", 1), strings.Replace(id, "/budgets/", "/budget/", 1)} {
				if _, _, _, err := monitorBudgetID(bad); err == nil {
					t.Fatal("invalid native budget identity accepted", bad)
				}
			}
			_, _, _, err = monitorBudgetID(strings.TrimPrefix(id, "/"))
			if (err == nil) != (kind == monitorConsumptionBudgetType) {
				t.Fatal("native leading-slash exception widened", kind, err)
			}
		}
	}
	c := directClient(func(*http.Request) (*http.Response, error) {
		t.Fatal("foreign budget reached transport")
		return nil, nil
	})
	for _, scope := range []string{"/providers/Microsoft.Billing/billingAccounts/account", "/providers/Microsoft.Management/managementGroups/group", root + "/resourcegroups/group/providers/Microsoft.Compute/virtualMachines/vm", "/subscriptions/00000000-0000-0000-0000-000000000000", root + "/resourcegroups/group/extra"} {
		if _, err := c.monitorBudgetRequest(monitorCostBudgetType, scope, "budget", "GET"); err == nil {
			t.Fatal("unsupported budget scope accepted", scope)
		}
	}
	// Root readers also reject ambiguous native list continuation aliases.
	raw := monitorRuleExample(t, "actions-2023-01-01/getActionGroup.json")
	id, _, _ := parseID(text(raw["id"]))
	c = directClient(func(r *http.Request) (*http.Response, error) {
		return jsonResponse(200, map[string]any{"value": []any{}, "NextLink": r.URL.String()}, nil), nil
	})
	c.subscription = strings.Split(id, "/")[2]
	if _, _, err := c.monitorRuleIndex(t.Context(), monitorActionGroupType); err == nil {
		t.Fatal("ambiguous alert continuation ignored")
	}
}

func TestMonitorBudgetGetBoundaries(t *testing.T) {
	for _, file := range []string{"consumption-2024-08-01/Budget.json", "cost-management-2025-03-01/Budgets/Get/Cost/Get-Cost-Budget.json"} {
		for _, mode := range []string{"wrong-id", "wrong-type", "numeric-type", "wrong-name", "id-alias", "null-properties", "empty-properties", "missing-amount", "string-amount", "missing-period", "missing-notifications-enabled", "missing-notifications-threshold", "invalid-contact-emails", "nextLink", "nextLink-alias", "error-envelope", "code-envelope", "lro", "202", "403", "404"} {
			t.Run(file+"/"+mode, func(t *testing.T) {
				raw := monitorBudgetExample(t, file)
				id, _, _, err := monitorBudgetID(text(raw["id"]))
				if err != nil {
					t.Fatal(err)
				}
				props := object(raw["properties"])
				status, headers := 200, http.Header{}
				switch mode {
				case "wrong-id":
					raw["id"] = text(raw["id"]) + "-replacement"
				case "wrong-type":
					raw["type"] = vmType
				case "numeric-type":
					raw["type"] = 1
				case "wrong-name":
					raw["name"] = "replacement"
				case "id-alias":
					raw["ID"] = raw["id"]
				case "null-properties":
					raw["properties"] = nil
				case "empty-properties":
					raw["properties"] = map[string]any{}
				case "missing-amount":
					delete(props, "amount")
				case "string-amount":
					props["amount"] = "100.65"
				case "missing-period":
					delete(props, "timePeriod")
				case "missing-notifications-enabled":
					for _, v := range object(props["notifications"]) {
						delete(object(v), "enabled")
					}
				case "missing-notifications-threshold":
					for _, v := range object(props["notifications"]) {
						delete(object(v), "threshold")
					}
				case "invalid-contact-emails":
					for _, v := range object(props["notifications"]) {
						object(v)["contactEmails"] = []any{1}
					}
				case "nextLink":
					raw["nextLink"] = "more"
				case "nextLink-alias":
					raw["NextLink"] = "more"
				case "error-envelope":
					raw["error"] = map[string]any{"code": "incomplete"}
				case "code-envelope":
					raw["code"] = "incomplete"
				case "lro":
					headers.Set("Operation-Location", apiURL(id+"/operation/1", "2024-08-01"))
				case "202":
					status = 202
				case "403":
					status = 403
				case "404":
					status = 404
				}
				calls := 0
				c := directClient(func(r *http.Request) (*http.Response, error) {
					calls++
					if r.Method != "GET" || !strings.EqualFold(r.URL.Path, id) {
						t.Fatal("unbound budget read", r.URL)
					}
					return jsonResponse(status, raw, headers), nil
				})
				c.subscription = strings.Split(id, "/")[2]
				if _, err := c.monitorBudgetRead(t.Context(), id); err == nil || calls != 1 {
					t.Fatal("invalid budget GET accepted", err, calls)
				}
			})
		}
	}
}
