package azure

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func TestResourceGroupNativeLifecycle(t *testing.T) {
	for _, mode := range []string{"sql", "sql_missing_child", "sql_child_forbidden", "sql_child_remains", "sync", "async", "group_forbidden", "list_forbidden", "member_forbidden", "new_member", "duplicate", "omitted", "changed_group", "managed_group", "group_lock", "member_protected", "monitor_forbidden", "rbac_forbidden", "diagnostic_forbidden", "budget_forbidden", "unreviewed_budget", "member_remains", "member_returns", "group_returns", "readback_forbidden", "invalid_receipt"} {
		t.Run(mode, func(t *testing.T) {
			member := actionAsset(diskType, "disk")
			if strings.HasPrefix(mode, "sql") {
				member = actionAsset(sqlServerType, "sql")
			}
			member.Normalized = map[string]any{}
			group := member
			group.ID = "group"
			group.Identity.NativeID = "/subscriptions/" + testSubscription + "/resourcegroups/test"
			group.Identity.NativeType = groupType
			group.Location = "global"
			req := contracts.ActionRequest{Asset: group, Action: "delete", IdempotencyKey: "group-delete-job", LifecycleImpacts: []contracts.ActionImpact{{Asset: member, ControllerID: group.ID, Delete: true}}}
			root := map[string]any{"id": group.Identity.NativeID, "type": groupType, "name": "test", "location": "eastus", "properties": map[string]any{"provisioningState": "Succeeded"}}
			disk := map[string]any{"id": member.Identity.NativeID, "type": diskType, "location": "eastus", "properties": map[string]any{"provisioningState": "Succeeded", "uniqueId": "original-disk"}}

			child := actionAsset(sqlDatabaseType, "master")
			child.Identity.NativeID = member.Identity.NativeID + "/databases/master"
			childRaw := map[string]any{"id": child.Identity.NativeID, "type": sqlDatabaseType, "location": "eastus", "properties": map[string]any{"status": "Online"}}
			if strings.HasPrefix(mode, "sql") {
				disk["type"] = sqlServerType
				disk["properties"] = map[string]any{"state": "Ready"}
				req.LifecycleImpacts = append(req.LifecycleImpacts, contracts.ActionImpact{Asset: child, ControllerID: member.ID, Delete: true})
			}
			req.LifecycleImpacts[0].Asset.Normalized["_arm_creation_generation"] = creationGeneration(disk)
			budget := object(array(monitorBudgetExample(t, "consumption-2024-08-01/BudgetsList.json")["value"])[0])
			budget["id"] = group.Identity.NativeID + "/providers/Microsoft.Consumption/budgets/hidden"
			budget["name"] = "hidden"
			budget["type"] = monitorConsumptionBudgetType
			calls, deletes, polls, groupReads, listReads, productReads := 0, 0, 0, 0, 0, 0
			gone, resuming := false, false
			r := protocolRuntime(t, func(q *http.Request) (*http.Response, error) {
				calls++
				path := strings.ToLower(q.URL.Path)
				if q.Method == "DELETE" {
					if path != group.Identity.NativeID || q.URL.Query().Get("api-version") != resourcesVersion || len(q.URL.Query()) != 1 || q.Header.Get("x-ms-client-request-id") != azureRequestID(req.IdempotencyKey+":resource-group-delete") {
						t.Fatal("wrong native delete", q.URL)
					}
					deletes++
					gone = true
					status, h := 204, http.Header{}
					if mode == "async" {
						status = 202
						h.Set("Location", groupOperationEndpoint())
					}
					return &http.Response{StatusCode: status, Header: h, Body: io.NopCloser(strings.NewReader(""))}, nil
				}
				if q.Method != "GET" {
					t.Fatal("unexpected mutation", q.Method, q.URL)
				}
				if strings.Contains(path, "/operationresults/") {
					polls++
					status := 202
					if polls > 1 {
						status = 200
					}
					return &http.Response{StatusCode: status, Header: http.Header{"Retry-After": {"3"}}, Body: io.NopCloser(strings.NewReader(""))}, nil
				}
				if mode == "rbac_forbidden" && strings.HasSuffix(path, "/providers/microsoft.authorization/roleassignments") || mode == "diagnostic_forbidden" && strings.HasSuffix(path, "/providers/microsoft.insights/diagnosticsettings") || mode == "budget_forbidden" && path == group.Identity.NativeID+"/providers/microsoft.consumption/budgets" {
					return jsonResponse(403, map[string]any{}, nil), nil
				}
				if mode == "monitor_forbidden" && strings.HasSuffix(path, "/providers/microsoft.insights/metricalerts") {
					return jsonResponse(403, map[string]any{}, nil), nil
				}
				if mode == "unreviewed_budget" {
					if path == group.Identity.NativeID+"/providers/microsoft.consumption/budgets" {
						return jsonResponse(200, map[string]any{"value": []any{budget}}, nil), nil
					}
					if path == strings.ToLower(text(budget["id"])) {
						return jsonResponse(200, budget, nil), nil
					}
				}
				if reply, handled := emptyMonitorIndexResponse(t, q); handled {
					return reply, nil
				}
				if reply, handled := emptyDiagnosticSourceIndexResponse(t, q); handled {
					return reply, nil
				}
				switch path {
				case group.Identity.NativeID:
					groupReads++
					if mode == "group_forbidden" {
						return jsonResponse(403, map[string]any{}, nil), nil
					}
					if gone && !(mode == "group_returns" && resuming && productReads > 0) {
						return jsonResponse(404, map[string]any{}, nil), nil
					}
					if mode == "changed_group" && listReads > 0 {
						root["tags"] = map[string]any{"late": "change"}
					}
					return jsonResponse(200, root, nil), nil
				case member.Identity.NativeID:
					if resuming {
						productReads++
					}
					if mode == "member_forbidden" || mode == "readback_forbidden" && resuming {
						return jsonResponse(403, map[string]any{}, nil), nil
					}
					if gone && mode != "member_remains" && !(mode == "member_returns" && productReads > 2) {
						return jsonResponse(404, map[string]any{}, nil), nil
					}
					return jsonResponse(200, disk, nil), nil
				case child.Identity.NativeID:
					if mode == "sql_child_forbidden" {
						return jsonResponse(403, map[string]any{}, nil), nil
					}
					if gone && mode != "sql_child_remains" {
						return jsonResponse(404, map[string]any{}, nil), nil
					}
					return jsonResponse(200, childRaw, nil), nil
				case member.Identity.NativeID + "/databases":
					rows := []any{childRaw}
					if mode == "sql_missing_child" || gone && mode != "sql_child_remains" {
						rows = []any{}
					}
					return jsonResponse(200, map[string]any{"value": rows}, nil), nil
				case member.Identity.NativeID + "/elasticpools":
					return jsonResponse(200, map[string]any{"value": []any{}}, nil), nil
				case group.Identity.NativeID + "/resources":
					listReads++
					if len(q.URL.Query()) != 1 || q.URL.Query().Get("api-version") != resourcesVersion {
						t.Fatal("filtered group index")
					}
					if mode == "list_forbidden" {
						return jsonResponse(403, map[string]any{}, nil), nil
					}
					rows := []any{disk}
					switch mode {
					case "new_member":
						rows = append(rows, map[string]any{"id": resourceID(diskType, "new"), "type": diskType})
					case "duplicate":
						rows = append(rows, disk)
					case "omitted":
						rows = []any{}
					}
					return jsonResponse(200, map[string]any{"value": rows}, nil), nil
				case "/subscriptions/" + testSubscription + "/providers/microsoft.authorization/locks":
					rows := []any{}
					if mode == "group_lock" {
						rows = append(rows, map[string]any{"id": group.Identity.NativeID + "/providers/Microsoft.Authorization/locks/hold", "properties": map[string]any{"level": "CanNotDelete"}})
					}
					return jsonResponse(200, map[string]any{"value": rows}, nil), nil
				default:
					t.Fatalf("unexpected lifecycle request %s", q.URL)
					return nil, nil
				}
			})
			c, err := r.resolve(t.Context(), group.Identity.ConnectionID)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "managed_group" {
				root["managedBy"] = resourceID("Microsoft.Solutions/applications", "owner")
			}
			if mode == "member_protected" {
				disk["tags"] = map[string]any{"steward/protected": "true"}
			}
			item, err := r.inventoryItem(t.Context(), c, root, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			req.Asset.Normalized = item.Normalized
			if req.Asset.Normalized["_resource_group_configuration"] != c.privateConfiguration(root) || req.Asset.Normalized["_resource_group_location"] != "eastus" {
				t.Fatal("inventory did not bind native group review")
			}
			before, _ := json.Marshal(req)
			result, err := r.resourceGroupStartDelete(t.Context(), req)
			accepted := mode == "sql" || mode == "sql_child_remains" || mode == "sync" || mode == "async" || mode == "member_remains" || mode == "member_returns" || mode == "group_returns" || mode == "readback_forbidden" || mode == "invalid_receipt"
			if !accepted {
				if mode == "unreviewed_budget" && (err == nil || !strings.Contains(err.Error(), "resource_group_monitor_member_not_reviewed")) {
					t.Fatal("hidden budget was not reviewed through native index", err)
				}
				if err == nil || deletes != 0 || result.Data != nil {
					t.Fatal("unsafe submission", mode, deletes, err)
				}
				return
			}
			if err != nil || deletes != 1 || listReads != 2 || groupReads < 5 {
				t.Fatal("missing native acceptance or scope checks", result, err, deletes, listReads, groupReads)
			}
			resuming = true
			saved := groupOperationJSON(t, result.Data)
			if mode == "invalid_receipt" {
				saved["done"] = false
				saved["url"] = groupOperationEndpoint()
			}
			initialCalls := calls
			for step := 0; step < 3; step++ {
				next, err := r.resourceGroupResumeDeletion(t.Context(), req, saved)
				if mode == "invalid_receipt" || mode == "readback_forbidden" {
					if err == nil || next.Done || next.Data != nil {
						t.Fatal("failed readback reported success", next, err)
					}
					if mode == "invalid_receipt" && calls != initialCalls {
						t.Fatal("forged receipt reached HTTP")
					}
					break
				}
				want := mode == "sql" || mode == "sync" || mode == "async" && step > 0
				if err != nil || next.Done != want {
					t.Fatal("incorrect completion", step, next, err)
				}
				saved = groupOperationJSON(t, next.Data)
			}
			if deletes != 1 || mode == "async" && polls != 2 {
				t.Fatal("repeated native operation", deletes, polls)
			}
			after, _ := json.Marshal(req)
			if string(before) != string(after) {
				t.Fatal("mutated reviewed request")
			}
		})
	}
}

func TestResourceGroupNativeLifecycleRegistration(t *testing.T) {
	kind, _ := findType(groupType)
	if kind.ReadOnly || len(kind.DeleteOperations) != 1 || kind.DeleteOperations[0] != "Azure.ResourceManagementClient.ResourceGroups_Delete" {
		t.Fatal("group native delete binding unavailable")
	}
}

func TestResourceGroupNativeRetentionRequiresAppliedPreparation(t *testing.T) {
	for _, kind := range []string{vmType, nicType} {
		for _, mode := range []string{"unprepared", "prepared", "retained_missing", "retained_recreated"} {
			t.Run(kind+"/"+mode, func(t *testing.T) {
				parent := actionAsset(kind, "controller")
				childKind := diskType
				if kind == nicType {
					childKind = "Microsoft.Network/publicIPAddresses"
				}
				child := actionAsset(childKind, "retained")
				child.Identity.NativeID = strings.Replace(strings.ToLower(child.Identity.NativeID), "/resourcegroups/test/", "/resourcegroups/external/", 1)
				properties := map[string]any{"provisioningState": "Succeeded", "vmId": "original-vm", "storageProfile": map[string]any{"osDisk": map[string]any{"managedDisk": map[string]any{"id": child.Identity.NativeID}, "deleteOption": "Delete"}}}
				if kind == nicType {
					properties = map[string]any{"provisioningState": "Succeeded", "resourceGuid": "original-nic", "ipConfigurations": []any{map[string]any{"name": "primary", "properties": map[string]any{"publicIPAddress": map[string]any{"id": child.Identity.NativeID, "properties": map[string]any{"deleteOption": "Delete"}}}}}}
				}
				raw := map[string]any{"id": parent.Identity.NativeID, "type": kind, "location": "eastus", "properties": properties}
				wire, _ := json.Marshal(properties)
				if err := json.Unmarshal(wire, &parent.Normalized); err != nil {
					t.Fatal(err)
				}
				parent.Normalized["_arm_creation_generation"] = creationGeneration(raw)
				retained := map[string]any{"id": child.Identity.NativeID, "type": childKind, "location": "eastus", "properties": map[string]any{"uniqueId": "original-retained", "resourceGuid": "original-retained", "provisioningState": "Succeeded"}}
				child.Normalized = map[string]any{"_arm_creation_generation": creationGeneration(retained)}
				if mode != "unprepared" {
					if kind == vmType {
						object(object(properties["storageProfile"])["osDisk"])["deleteOption"] = "Detach"
					} else {
						object(object(object(object(properties["ipConfigurations"].([]any)[0])["properties"])["publicIPAddress"])["properties"])["deleteOption"] = "Detach"
					}
				}
				group := parent
				group.ID = "group"
				group.Identity.NativeID = "/subscriptions/" + testSubscription + "/resourcegroups/test"
				group.Identity.NativeType = groupType
				group.Location = "global"
				root := map[string]any{"id": group.Identity.NativeID, "type": groupType, "name": "test", "location": "eastus", "properties": map[string]any{"provisioningState": "Succeeded"}}
				req := contracts.ActionRequest{Asset: group, Action: "delete", IdempotencyKey: "group-retention-job", LifecycleImpacts: []contracts.ActionImpact{{Asset: parent, ControllerID: group.ID, Delete: true}, {Asset: child, ControllerID: parent.ID, Delete: false}}}
				gone := false
				deletes := 0
				r := protocolRuntime(t, func(q *http.Request) (*http.Response, error) {
					path := strings.ToLower(q.URL.Path)
					if q.Method == "DELETE" {
						if path != group.Identity.NativeID {
							t.Fatal("unexpected delete", q.URL)
						}
						deletes++
						gone = true
						return &http.Response{StatusCode: 204, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(""))}, nil
					}
					if q.Method != "GET" {
						t.Fatal("native group start attempted uncheckpointed preparation", q.Method, q.URL)
					}
					if reply, handled := emptyMonitorIndexResponse(t, q); handled {
						return reply, nil
					}
					if reply, handled := emptyDiagnosticSourceIndexResponse(t, q); handled {
						return reply, nil
					}
					switch path {
					case group.Identity.NativeID:
						if gone {
							return jsonResponse(404, map[string]any{}, nil), nil
						}
						return jsonResponse(200, root, nil), nil
					case parent.Identity.NativeID:
						if gone {
							return jsonResponse(404, map[string]any{}, nil), nil
						}
						return jsonResponse(200, raw, nil), nil
					case child.Identity.NativeID:
						if gone && mode == "retained_missing" {
							return jsonResponse(404, map[string]any{}, nil), nil
						}
						if gone && mode == "retained_recreated" {
							object(retained["properties"])["uniqueId"] = "replacement"
							object(retained["properties"])["resourceGuid"] = "replacement"
						}
						return jsonResponse(200, retained, nil), nil
					case group.Identity.NativeID + "/resources":
						return jsonResponse(200, map[string]any{"value": []any{raw}}, nil), nil
					case parent.Identity.NativeID + "/extensions", "/subscriptions/" + testSubscription + "/providers/microsoft.authorization/locks":
						return jsonResponse(200, map[string]any{"value": []any{}}, nil), nil
					default:
						t.Fatal("unexpected retention read", q.URL)
						return nil, nil
					}
				})
				c, err := r.resolve(t.Context(), group.Identity.ConnectionID)
				if err != nil {
					t.Fatal(err)
				}
				item, err := r.inventoryItem(t.Context(), c, root, nil, nil)
				if err != nil {
					t.Fatal(err)
				}
				req.Asset.Normalized = item.Normalized
				// Establish the distinction: normal product preflight allows its own later
				// Execute preparation, which a group DELETE will not perform.
				products, err := c.resourceGroupProductRequests(req)
				if err != nil {
					t.Fatal(err)
				}
				driver, err := r.ResolveAction(t.Context(), parent.Identity.ConnectionID, parent)
				if err != nil {
					t.Fatal(err)
				}
				check, err := driver.Preflight(t.Context(), products[parent.ID])
				if err != nil || !check.Allowed || check.Absent {
					t.Fatal("ordinary preflight fixture not ready", check, err)
				}
				result, err := r.resourceGroupStartDelete(t.Context(), req)
				if mode == "unprepared" {
					if err == nil || !strings.Contains(err.Error(), "resource_group_attachment_preparation_pending") || deletes != 0 {
						t.Fatal("native cascade allowed unprepared retention", deletes, err)
					}
					return
				}
				if err != nil || deletes != 1 {
					t.Fatal("prepared native cascade rejected", deletes, err)
				}
				next, err := r.resourceGroupResumeDeletion(t.Context(), req, groupOperationJSON(t, result.Data))
				if mode == "prepared" {
					if err != nil || !next.Done {
						t.Fatal(next, err)
					}
				} else if err == nil || next.Done || next.Data != nil {
					t.Fatal("retention failure reported as complete", next, err)
				}
				if deletes != 1 {
					t.Fatal("resubmitted native delete")
				}
			})
		}
	}
}
