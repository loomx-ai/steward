package azure

import (
	"encoding/json"
	"maps"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func monitorSourceToActionGroup(t *testing.T, kind, target string) map[string]any {
	t.Helper()
	f := newMonitorInventoryFixture(t, kind)
	raw := f.objects[slices.Sorted(maps.Keys(f.objects))[0]]
	props := object(raw["properties"])
	switch kind {
	case monitorMetricAlertType:
		props["actions"] = []any{map[string]any{"actionGroupId": target}}
	case monitorActivityAlertType:
		props["actions"] = map[string]any{"actionGroups": []any{map[string]any{"actionGroupId": target}}}
	case monitorScheduledRuleType:
		props["actions"] = map[string]any{"actionGroups": []any{target}}
	case monitorSmartAlertType:
		props["actionGroups"] = map[string]any{"groupIds": []any{target}}
	case monitorPrometheusType:
		found := false
		for _, value := range array(props["rules"]) {
			rule := object(value)
			if text(rule["alert"]) != "" {
				rule["actions"] = []any{map[string]any{"actionGroupId": target}}
				found = true
			}
		}
		if !found {
			t.Fatal("native Prometheus example has no alert rule")
		}
	case monitorProcessingType:
		props["actions"] = []any{map[string]any{"actionType": "AddActionGroups", "actionGroupIds": []any{target}}}
	case monitorConsumptionBudgetType, monitorCostBudgetType:
		for _, value := range object(props["notifications"]) {
			object(value)["contactGroups"] = []any{target}
		}
	default:
		t.Fatal("unsupported native action-group source", kind)
	}
	return raw
}

func TestMonitorUnindexedAndLateIncomingReferences(t *testing.T) {
	for _, kind := range monitorIncomingKinds(monitorActionGroupType) {
		for _, mode := range []string{"unindexed", "root-absent", "late-after-delete"} {
			t.Run(kind+"/"+mode, func(t *testing.T) {
				f := newMonitorInventoryFixture(t, monitorActionGroupType)
				targetID := slices.Sorted(maps.Keys(f.objects))[0]
				target := f.asset(t, targetID)
				driver, err := f.runtime.ResolveAction(t.Context(), "connection", target)
				if err != nil {
					t.Fatal(err)
				}
				request := contracts.ActionRequest{Asset: target, Action: "delete"}
				var result contracts.ActionResult
				if mode == "late-after-delete" {
					result, err = driver.Execute(t.Context(), request)
					if err != nil {
						t.Fatal(err)
					}
				}
				source := monitorSourceToActionGroup(t, kind, targetID)
				sourceID := f.addRelated(t, source)
				if mode == "root-absent" {
					delete(f.objects, targetID)
				}
				if mode == "late-after-delete" {
					if wait, err := driver.Wait(t.Context(), request, result); err == nil || wait.Done || isNotFound(err) {
						t.Fatal("late unindexed rule/budget disappeared with root absence", kind, wait, err)
					}
					return
				}
				if _, err := driver.Execute(t.Context(), request); err == nil || isNotFound(err) || len(f.deletes) != 0 {
					t.Fatal("unindexed native reference did not protect action group", mode, err)
				}
				if mode == "unindexed" {
					contributor, err := f.runtime.ServiceLifecycle(t.Context(), "connection")
					if err != nil {
						t.Fatal(err)
					}
					result, err := contributor.Contribute(t.Context(), "scope", []asset.Asset{target})
					if err != nil || len(result.Unresolved) != 1 || len(result.Bindings) != 0 {
						t.Fatal("unindexed native source not retained in graph", result, err)
					}
					ref := result.Unresolved[0]
					if ref.NativeID != sourceID || ref.NativeType != kind || ref.ControllerID != target.ID || ref.Relationship != graph.RelationshipDependsOn || ref.Evidence[graph.RelationshipEvidenceRequiredDeletion] != true || ref.Evidence[graph.RelationshipEvidenceAutomaticSelection] != false {
						t.Fatal("unindexed source acquired ownership or automatic selection", ref)
					}
				}
			})
		}
	}
}

func TestMonitorIncomingReadFailureIsNotAbsence(t *testing.T) {
	for _, sourceKind := range []string{monitorMetricAlertType, monitorConsumptionBudgetType, monitorCostBudgetType} {
		for _, mode := range []string{"list-403", "list-404", "list-202", "get-404", "get-403", "group-404"} {
			t.Run(sourceKind+"/"+mode, func(t *testing.T) {
				f := newMonitorInventoryFixture(t, monitorActionGroupType)
				targetID := slices.Sorted(maps.Keys(f.objects))[0]
				target := f.asset(t, targetID)
				driver, err := f.runtime.ResolveAction(t.Context(), "connection", target)
				if err != nil {
					t.Fatal(err)
				}
				sourceID := f.addRelated(t, monitorSourceToActionGroup(t, sourceKind, targetID))
				_, sourceScope, _, _ := monitorResourceID(sourceID)
				collection := "/subscriptions/" + testSubscription + "/providers/" + strings.ToLower(sourceKind)
				request := contracts.ActionRequest{Asset: target, Action: "delete"}
				delete(f.objects, targetID)
				f.override = func(req *http.Request) (*http.Response, bool) {
					path := strings.ToLower(req.URL.Path)
					matches := strings.HasPrefix(mode, "list-") && path == collection || strings.HasPrefix(mode, "get-") && path == sourceID || mode == "group-404" && (path == sourceScope || sourceScope == "/subscriptions/"+testSubscription && path == slices.Sorted(maps.Keys(f.groups))[0])
					if !matches {
						return nil, false
					}
					status := 404
					if strings.HasSuffix(mode, "403") {
						status = 403
					}
					if mode == "list-202" {
						return jsonResponse(202, map[string]any{"value": []any{}}, nil), true
					}
					return jsonResponse(status, map[string]any{"error": map[string]any{"code": "ResourceNotFound"}}, nil), true
				}
				if check, err := driver.Preflight(t.Context(), request); err == nil || isNotFound(err) || check.Absent || check.Allowed {
					t.Fatal("incoming failure authorized absent destination", mode, check, err)
				}
				if read, err := driver.Readback(t.Context(), request); err == nil || isNotFound(err) {
					t.Fatal("incoming failure completed readback", mode, read, err)
				}
			})
		}
	}
}

func TestMonitorFrozenPrerequisiteBoundaries(t *testing.T) {
	for _, mode := range []string{"valid", "other-destination", "public-ref-forgery", "record-ref-forgery", "missing-reference-proof", "missing-configuration-proof", "changed-configuration-proof", "retained", "controller", "wrong-connection", "wrong-partition", "wrong-kind", "wrong-id", "asset-collision", "duplicate", "recreated", "absent-target-with-recreated-source"} {
		t.Run(mode, func(t *testing.T) {
			f := newMonitorInventoryFixture(t, monitorActionGroupType)
			ids := slices.Sorted(maps.Keys(f.objects))
			targetID := ids[0]
			target := f.asset(t, targetID)
			reference := targetID
			if mode == "other-destination" || mode == "public-ref-forgery" || mode == "record-ref-forgery" {
				reference = ids[1]
			}
			sourceID := f.addRelated(t, monitorSourceToActionGroup(t, monitorConsumptionBudgetType, reference))
			source := f.asset(t, sourceID)
			original := f.otherObjects[sourceID]
			delete(f.otherObjects, sourceID)
			request := contracts.ActionRequest{Asset: target, Action: "delete", PrerequisiteDeletions: []contracts.ActionImpact{{Asset: source, ControllerID: target.ID, Delete: true}}}
			wire, _ := json.Marshal(request)
			if err := json.Unmarshal(wire, &request); err != nil {
				t.Fatal(err)
			}
			prerequisite := &request.PrerequisiteDeletions[0]
			switch mode {
			case "public-ref-forgery":
				prerequisite.Asset.Normalized[referenceKey(monitorActionGroupType)] = []any{targetID}
			case "record-ref-forgery":
				prerequisite.Asset.Normalized["_monitor_references"] = map[string]any{monitorActionGroupType: []any{targetID}}
			case "missing-reference-proof":
				delete(prerequisite.Asset.Normalized, monitorReferencesProof)
			case "missing-configuration-proof":
				delete(prerequisite.Asset.Normalized, monitorConfigurationProof)
			case "changed-configuration-proof":
				prerequisite.Asset.Normalized[monitorConfigurationProof] = "changed"
			case "retained":
				prerequisite.Delete = false
			case "controller":
				prerequisite.ControllerID = "another-controller"
			case "wrong-connection":
				prerequisite.Asset.Identity.ConnectionID = "another-connection"
			case "wrong-partition":
				prerequisite.Asset.Identity.Partition = "another-partition"
			case "wrong-kind":
				prerequisite.Asset.Identity.NativeType = monitorCostBudgetType
			case "wrong-id":
				prerequisite.Asset.Identity.NativeID = sourceID + "-other"
			case "asset-collision":
				prerequisite.Asset.ID = target.ID
			case "duplicate":
				request.PrerequisiteDeletions = append(request.PrerequisiteDeletions, *prerequisite)
			case "recreated", "absent-target-with-recreated-source":
				f.otherObjects[sourceID] = original
				if mode == "absent-target-with-recreated-source" {
					delete(f.objects, targetID)
				}
			}
			driver, err := f.runtime.ResolveAction(t.Context(), "connection", target)
			if err != nil {
				t.Fatal(err)
			}
			result, err := driver.Execute(t.Context(), request)
			if mode == "valid" {
				if err != nil || !slices.Equal(f.deletes, []string{targetID}) {
					t.Fatal("signed absent prerequisite rejected", result, err)
				}
				return
			}
			if err == nil || isNotFound(err) || len(f.deletes) != 0 {
				t.Fatal("forged/retained prerequisite authorized cleanup", mode, result, err, f.deletes)
			}
		})
	}
}
