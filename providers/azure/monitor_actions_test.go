package azure

import (
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func TestMonitorRegisteredNativeActions(t *testing.T) {
	for _, kind := range monitorInventoryKinds() {
		t.Run(kind, func(t *testing.T) {
			f := newMonitorInventoryFixture(t, kind)
			id := slices.Sorted(maps.Keys(f.objects))[0]
			value := f.asset(t, id)
			request := contracts.ActionRequest{Asset: value, Action: "delete", IdempotencyKey: "native-monitor-delete"}
			driver, err := f.runtime.ResolveAction(t.Context(), "connection", value)
			if err != nil {
				t.Fatal("registered monitor lacks driver", err)
			}
			checkedHeader := false
			f.override = func(req *http.Request) (*http.Response, bool) {
				if req.Method == "DELETE" {
					checkedHeader = true
					if req.Header.Get("X-Ms-Client-Request-Id") != azureRequestID(request.IdempotencyKey) || !strings.EqualFold(req.URL.Path, id) || req.Header.Get("If-Match") != "" {
						t.Fatal("native delete identity changed", req.URL)
					}
				}
				return nil, false
			}
			result, err := driver.Execute(t.Context(), request)
			if err != nil || !checkedHeader || !slices.Equal(f.deletes, []string{id}) || result.ProviderRequestID != "monitor-native-delete" || result.ProviderOperationID != "" {
				t.Fatal("native monitor delete failed", result, err, f.deletes)
			}
			encoded, _ := json.Marshal(request)
			if err := json.Unmarshal(encoded, &request); err != nil {
				t.Fatal(err)
			}
			driver, err = f.runtime.ResolveAction(t.Context(), "connection", request.Asset)
			if err != nil {
				t.Fatal("stored monitor action failed to resolve", err)
			}
			if waited, err := driver.Wait(t.Context(), request, result); err != nil || !waited.Done {
				t.Fatal("native monitor absence not verified", waited, err)
			}
			request.ExecutionResult = &result
			if read, err := driver.Readback(t.Context(), request); err != nil || read.Exists {
				t.Fatal("stored native readback failed", read, err)
			}
			if _, err := driver.Execute(t.Context(), request); err != nil || len(f.deletes) != 1 {
				t.Fatal("idempotent monitor retry mutated again", err, f.deletes)
			}
			if len(f.objects) == 0 {
				t.Fatal("independent deletion removed sibling monitor")
			}
		})
	}
}

func TestMonitorActionProtectionAndDrift(t *testing.T) {
	for _, kind := range []string{monitorActionGroupType, monitorConsumptionBudgetType, insightsWebTestType} {
		for _, mode := range []string{"private-change", "group-change", "group-owner", "resource-tag", "group-tag", "subscription-lock", "group-lock", "resource-lock", "lock-denied", "group-missing", "late-private-change", "cancelled", "wrong-identity", "wrong-connection", "wrong-partition", "wrong-location", "configuration-proof", "group-proof", "reference-proof", "reference-map", "parameters", "unreviewed-impact"} {
			t.Run(kind+"/"+mode, func(t *testing.T) {
				f := newMonitorInventoryFixture(t, kind)
				id := ""
				for _, candidate := range slices.Sorted(maps.Keys(f.objects)) {
					_, scope, _, _ := monitorResourceID(candidate)
					if scope != "/subscriptions/"+testSubscription {
						id = candidate
						break
					}
				}
				value := f.asset(t, id)
				driver, err := f.runtime.ResolveAction(t.Context(), "connection", value)
				if err != nil {
					t.Fatal(err)
				}
				request := contracts.ActionRequest{Asset: value, Action: "delete"}
				_, group, _, _ := monitorResourceID(id)
				ctx := t.Context()
				change := func() {
					switch kind {
					case monitorActionGroupType:
						object(array(object(f.objects[id]["properties"])["emailReceivers"])[0])["emailAddress"] = "PRIVATE_CHANGED_RECEIVER"
					case insightsWebTestType:
						object(object(f.objects[id]["properties"])["Configuration"])["WebTest"] = "PRIVATE_CHANGED_WEBTEST"
					default:
						for _, notification := range object(object(f.objects[id]["properties"])["notifications"]) {
							object(notification)["contactEmails"] = []any{"PRIVATE_CHANGED_RECEIVER"}
						}
					}
				}
				switch mode {
				case "private-change":
					change()
				case "group-change":
					f.groups[group]["tags"] = map[string]any{"owner": "changed"}
				case "group-owner":
					f.groups[group]["managedBy"] = "managed-owner"
				case "resource-tag":
					f.objects[id]["tags"] = map[string]any{"steward:protected": "true"}
				case "group-tag":
					f.groups[group]["tags"] = map[string]any{"steward:protected": "true"}
				case "subscription-lock", "group-lock", "resource-lock":
					scope := group
					if mode == "subscription-lock" {
						scope = "/subscriptions/" + testSubscription
					}
					if mode == "resource-lock" {
						scope = id
					}
					f.locks = []any{map[string]any{"id": scope + "/providers/Microsoft.Authorization/locks/hold", "properties": map[string]any{"level": "CanNotDelete"}}}
				case "lock-denied":
					f.override = func(req *http.Request) (*http.Response, bool) {
						if strings.HasSuffix(strings.ToLower(req.URL.Path), "/providers/microsoft.authorization/locks") {
							return jsonResponse(403, map[string]any{"error": map[string]any{"code": "AuthorizationFailed"}}, nil), true
						}
						return nil, false
					}
				case "group-missing":
					delete(f.groups, group)
				case "late-private-change":
					reads := 0
					f.override = func(req *http.Request) (*http.Response, bool) {
						if strings.HasSuffix(strings.ToLower(req.URL.Path), "/providers/microsoft.authorization/locks") {
							reads++
							if reads == 2 {
								change()
							}
						}
						return nil, false
					}
				case "cancelled":
					var cancel context.CancelFunc
					ctx, cancel = context.WithCancel(ctx)
					cancel()
				case "wrong-identity":
					request.Asset.Identity.NativeID = id + "-other"
				case "wrong-connection":
					request.Asset.Identity.ConnectionID = "other"
				case "wrong-partition":
					request.Asset.Identity.Partition = "other"
				case "wrong-location":
					request.Asset.Location = "other"
				case "configuration-proof":
					request.Asset.Normalized[monitorConfigurationProof] = "forged"
				case "group-proof":
					request.Asset.Normalized[monitorGroupProof] = "forged"
				case "reference-proof":
					request.Asset.Normalized[monitorReferencesProof] = "forged"
				case "reference-map":
					request.Asset.Normalized["_monitor_references"] = map[string]any{monitorActionGroupType: []any{group + "/providers/microsoft.insights/actiongroups/forged"}}
				case "parameters":
					request.Parameters = map[string]any{"scope": "another-subscription"}
				case "unreviewed-impact":
					request.LifecycleImpacts = []contracts.ActionImpact{{Asset: value}}
				}
				if _, err := driver.Execute(ctx, request); err == nil || len(f.deletes) != 0 || isNotFound(err) {
					t.Fatal("unsafe native monitor action executed", mode, err, f.deletes)
				}
			})
		}
	}
}

func TestMonitorDeleteReceiptsAndReadback(t *testing.T) {
	for _, kind := range []string{monitorActionGroupType, monitorCostBudgetType} {
		for _, mode := range []string{"body", "async-header", "accepted-status", "undocumented-status", "delete-404", "live-root", "replacement", "read-denied", "read-accepted", "receipt", "operation", "wrong-target", "cancelled"} {
			t.Run(kind+"/"+mode, func(t *testing.T) {
				f := newMonitorInventoryFixture(t, kind)
				id := slices.Sorted(maps.Keys(f.objects))[0]
				value := f.asset(t, id)
				request := contracts.ActionRequest{Asset: value, Action: "delete"}
				driver, err := f.runtime.ResolveAction(t.Context(), "connection", value)
				if err != nil {
					t.Fatal(err)
				}
				original := f.objects[id]
				switch mode {
				case "body":
					f.deleteBody = map[string]any{"status": "Succeeded"}
				case "async-header":
					f.deleteHeader = http.Header{"Azure-Asyncoperation": []string{"https://management.azure.com/subscriptions/" + testSubscription + "/providers/Microsoft.Insights/operations/unknown"}}
				case "accepted-status":
					f.deleteStatus = 202
				case "undocumented-status":
					f.deleteStatus = 201
					if kind == monitorCostBudgetType {
						f.deleteStatus = 204
					}
				case "live-root":
					f.hold = true
				case "delete-404":
					f.override = func(req *http.Request) (*http.Response, bool) {
						if req.Method == "DELETE" {
							delete(f.objects, id)
							return jsonResponse(404, map[string]any{"error": map[string]any{"code": "ResourceNotFound"}}, nil), true
						}
						return nil, false
					}
				}
				result, err := driver.Execute(t.Context(), request)
				if slices.Contains([]string{"body", "async-header", "accepted-status", "undocumented-status"}, mode) {
					if err == nil || len(f.deletes) != 1 {
						t.Fatal("invalid monitor acknowledgement accepted", mode, result, err)
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				ctx := t.Context()
				switch mode {
				case "replacement":
					copy := maps.Clone(original)
					copy["properties"] = maps.Clone(object(original["properties"]))
					object(copy["properties"])["description"] = "new-incarnation"
					f.objects[id] = copy
				case "read-denied", "read-accepted":
					f.override = func(req *http.Request) (*http.Response, bool) {
						if req.Method == "GET" && strings.EqualFold(req.URL.Path, id) {
							if mode == "read-denied" {
								return jsonResponse(403, map[string]any{"error": map[string]any{"code": "AuthorizationFailed"}}, nil), true
							}
							return jsonResponse(202, original, nil), true
						}
						return nil, false
					}
				case "receipt":
					result.Data["_monitor_delete_receipt"] = "forged"
				case "operation":
					result.ProviderOperationID = "invented-operation"
				case "wrong-target":
					request.Asset.Identity.NativeID = id + "-other"
				case "cancelled":
					var cancel context.CancelFunc
					ctx, cancel = context.WithCancel(ctx)
					cancel()
				}
				waited, err := driver.Wait(ctx, request, result)
				if mode == "live-root" {
					if err != nil || waited.Done {
						t.Fatal("live monitor root disappeared", waited, err)
					}
					return
				}
				if mode == "delete-404" {
					if err != nil || !waited.Done {
						t.Fatal("native absence could not reconcile 404", waited, err)
					}
					return
				}
				if err == nil || waited.Done {
					t.Fatal("invalid monitor readback completed", mode, waited, err)
				}
			})
		}
	}
}

func TestMonitorBudgetRequiredDeletionExecution(t *testing.T) {
	for _, budgetKind := range []string{monitorConsumptionBudgetType, monitorCostBudgetType} {
		t.Run(budgetKind, func(t *testing.T) {
			f := newMonitorInventoryFixture(t, monitorActionGroupType)
			targetID := slices.Sorted(maps.Keys(f.objects))[0]
			sourceFixture := newMonitorInventoryFixture(t, budgetKind)
			sourceID := slices.Sorted(maps.Keys(sourceFixture.objects))[0]
			raw := sourceFixture.objects[sourceID]
			for _, n := range object(object(raw["properties"])["notifications"]) {
				object(n)["contactGroups"] = []any{targetID}
			}
			f.addRelated(t, raw)
			target, source := f.asset(t, targetID), f.asset(t, sourceID)
			values := []asset.Asset{target, source}
			contributor, err := f.runtime.ServiceLifecycle(t.Context(), "connection")
			if err != nil {
				t.Fatal(err)
			}
			contribution, err := contributor.Contribute(t.Context(), "scope", values)
			if err != nil || len(contribution.Unresolved) != 0 {
				t.Fatal("native monitor dependency not resolved", contribution, err)
			}
			planned, err := plan.Solve(plan.Input{Assets: values, Relationships: contribution.Relationships, ResolvedAssetIDs: []asset.AssetID{target.ID, source.ID}})
			if err != nil || len(planned.Blockers) != 0 || len(planned.Steps) != 2 {
				t.Fatal("native monitor plan failed", planned, err)
			}
			targetRequest := servicePlanRequest(planned, values, target)
			targetDriver, err := f.runtime.ResolveAction(t.Context(), "connection", target)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := targetDriver.Execute(t.Context(), targetRequest); err == nil || len(f.deletes) != 0 {
				t.Fatal("live budget prerequisite allowed action group deletion", err)
			}
			sourceDriver, err := f.runtime.ResolveAction(t.Context(), "connection", source)
			if err != nil {
				t.Fatal(err)
			}
			f.deleteStatus = 200
			sourceRequest := servicePlanRequest(planned, values, source)
			sourceResult, err := sourceDriver.Execute(t.Context(), sourceRequest)
			if err != nil {
				t.Fatal("budget prerequisite deletion failed", err)
			}
			if wait, err := sourceDriver.Wait(t.Context(), sourceRequest, sourceResult); err != nil || !wait.Done {
				t.Fatal("budget prerequisite absence failed", wait, err)
			}
			wire, _ := json.Marshal(targetRequest)
			if err := json.Unmarshal(wire, &targetRequest); err != nil {
				t.Fatal(err)
			}
			targetDriver, err = f.runtime.ResolveAction(t.Context(), "connection", targetRequest.Asset)
			if err != nil {
				t.Fatal(err)
			}
			result, err := targetDriver.Execute(t.Context(), targetRequest)
			if err != nil || !slices.Equal(f.deletes, []string{sourceID, targetID}) {
				t.Fatal("stored prerequisite did not authorize ordered cleanup", result, err, f.deletes)
			}
			if waited, err := targetDriver.Wait(t.Context(), targetRequest, result); err != nil || !waited.Done {
				t.Fatal("native action group did not reconcile", waited, err)
			}
			if !slices.ContainsFunc(contribution.Relationships, func(ref graph.Relationship) bool {
				return ref.SourceAssetID == target.ID && ref.TargetAssetID == source.ID && ref.Evidence[graph.RelationshipEvidenceAutomaticSelection] == false
			}) {
				t.Fatal("budget acquired automatic selection")
			}
		})
	}
}
