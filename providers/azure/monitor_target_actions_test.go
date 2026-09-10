package azure

import (
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/core/graph"
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

// Existing isolated product scenarios compose an empty Monitor environment.
// Call this only after a fixture's explicit overrides. The new target scenarios
// use the original Monitor responses instead, including native 403/404 errors.
func emptyMonitorIndexResponse(t *testing.T, req *http.Request) (*http.Response, bool) {
	t.Helper()
	if response, handled := emptyDiagnosticIndexResponse(t, req); handled {
		return response, true
	}
	path := strings.ToLower(req.URL.Path)
	root := "/subscriptions/" + testSubscription
	for kind, version := range map[string]string{
		"microsoft.insights/metricalerts": "2026-01-01", "microsoft.insights/activitylogalerts": "2026-01-01",
		"microsoft.insights/scheduledqueryrules": "2026-03-01", "microsoft.insights/actiongroups": "2023-01-01",
		"microsoft.alertsmanagement/smartdetectoralertrules": "2021-04-01", "microsoft.alertsmanagement/prometheusrulegroups": "2023-03-01",
		"microsoft.alertsmanagement/actionrules": "2021-08-08", "microsoft.insights/webtests": "2022-06-15",
		"microsoft.consumption/budgets": "2024-08-01", "microsoft.costmanagement/budgets": "2025-03-01",
	} {
		if path != root+"/providers/"+kind && !(strings.HasSuffix(kind, "/budgets") && strings.HasPrefix(path, root+"/resourcegroups/") && strings.HasSuffix(path, "/providers/"+kind)) {
			continue
		}
		if req.Method != "GET" || req.URL.Host != "management.azure.com" || len(req.URL.Query()) != 1 || req.URL.Query().Get("api-version") != version {
			t.Fatal("unexpected empty native Monitor index contract", req.Method, req.URL)
		}
		return jsonResponse(200, map[string]any{"value": []any{}}, nil), true
	}
	return nil, false
}

func emptyDiagnosticIndexResponse(t *testing.T, req *http.Request) (*http.Response, bool) {
	t.Helper()
	path := strings.ToLower(req.URL.Path)
	if !strings.HasPrefix(path, "/subscriptions/"+testSubscription+"/") || !strings.HasSuffix(path, "/providers/microsoft.insights/diagnosticsettings") {
		return nil, false
	}
	scope := strings.TrimSuffix(path, "/providers/microsoft.insights/diagnosticsettings")
	if _, err := diagnosticScope(scope); err != nil || req.Method != "GET" || req.URL.Host != "management.azure.com" || len(req.URL.Query()) != 1 || req.URL.Query().Get("api-version") != diagnosticSettingsVersion {
		t.Fatal("unexpected empty native diagnostic index contract", req.Method, req.URL)
	}
	return jsonResponse(200, map[string]any{"value": []any{}}, nil), true
}

// Isolated product fixtures can compose an empty broad ARM index while their
// target-specific diagnostic indexes still run. Call after explicit overrides;
// fixtures with actual broad ARM rows must keep their own collection response.
func emptyDiagnosticSourceIndexResponse(t *testing.T, req *http.Request) (*http.Response, bool) {
	t.Helper()
	if !strings.EqualFold(req.URL.Path, "/subscriptions/"+testSubscription+"/resources") {
		return nil, false
	}
	if req.Method != "GET" || req.URL.Host != "management.azure.com" || len(req.URL.Query()) != 1 || req.URL.Query().Get("api-version") != resourcesVersion {
		t.Fatal("unexpected empty diagnostic source index contract", req.Method, req.URL)
	}
	return jsonResponse(200, map[string]any{"value": []any{}}, nil), true
}

// Tests that inspect a native driver's internal poll/preparation state still
// resolve the actual guarded runtime driver and explicitly inspect its child.
func monitorTargetInner(driver contracts.ActionDriver) contracts.ActionDriver {
	if guard, ok := driver.(*monitorTargetAction); ok {
		return guard.inner
	}
	return driver
}

func monitorTargetTestReceipt(driver contracts.ActionDriver, request contracts.ActionRequest, result contracts.ActionResult) contracts.ActionResult {
	if guard, ok := driver.(*monitorTargetAction); ok {
		result.Data = maps.Clone(result.Data)
		if result.Data == nil {
			result.Data = map[string]any{}
		}
		result.Data[monitorTargetReceipt] = guard.receipt(request)
	}
	return result
}

func monitorDiskTargets(t *testing.T, count int) (*monitorInventoryFixture, *dnsScenario, *Runtime, []asset.Asset, asset.Asset) {
	t.Helper()
	f := newMonitorInventoryFixture(t, monitorActivityAlertType)
	id := slices.Sorted(maps.Keys(f.objects))[0]
	raw := f.objects[id]
	clear(f.objects)
	f.objects[id] = raw
	s := newDNSScenario()
	r := s.runtime(t)
	base := r.transport
	r.transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		path := strings.ToLower(req.URL.Path)
		if path == "/subscriptions/"+testSubscription+"/resourcegroups" || f.groups[path] != nil {
			return f.runtime.transport.RoundTrip(req)
		}
		_, _, _, err := monitorResourceID(path)
		monitor := err == nil
		for _, kind := range monitorInventoryKinds() {
			monitor = monitor || strings.HasSuffix(path, "/providers/"+strings.ToLower(kind))
		}
		if monitor {
			return f.runtime.transport.RoundTrip(req)
		}
		return base.RoundTrip(req)
	})
	var targets []asset.Asset
	scopes := []any{}
	for i := range count {
		raw := nativeResource(diskType, "target-"+string(rune('a'+i)), "eastus", map[string]any{"diskState": "Unattached", "diskSizeGB": 32})
		kind, _ := findType(diskType)
		s.add(raw, kind.Version)
		value := dnsAsset(t, r, raw)
		targets = append(targets, value)
		scopes = append(scopes, value.Identity.NativeID)
	}
	object(raw["properties"])["scopes"] = scopes
	object(raw["properties"])["actions"] = map[string]any{"actionGroups": []any{}}
	return f, s, r, targets, f.asset(t, id)
}

func TestMonitorNativeTargetGraphAndOrderedCleanup(t *testing.T) {
	f, s, r, targets, source := monitorDiskTargets(t, 10)
	contributor, err := r.ServiceLifecycle(t.Context(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	clear(f.calls)
	missing, err := contributor.Contribute(t.Context(), "scope", targets)
	if err != nil || len(missing.Unresolved) != 10 || len(missing.Bindings)+len(missing.Relationships) != 0 {
		t.Fatal("unindexed native rule did not block every target", missing, err)
	}
	for _, ref := range missing.Unresolved {
		if ref.NativeID != source.Identity.NativeID || ref.Relationship != graph.RelationshipDependsOn || ref.Evidence[graph.RelationshipEvidenceRequiredDeletion] != true || ref.Evidence[graph.RelationshipEvidenceAutomaticSelection] != false {
			t.Fatal("unindexed alert acquired automatic ownership", ref)
		}
	}
	for _, kind := range monitorIncomingKinds(diskType) {
		path := "GET /subscriptions/" + testSubscription + "/providers/" + strings.ToLower(kind)
		if f.calls[path] != 2 {
			t.Fatal("target count multiplied native source indexes", kind, f.calls[path])
		}
	}
	values := append(slices.Clone(targets), source)
	contribution, err := contributor.Contribute(t.Context(), "scope", values)
	if err != nil || len(contribution.Unresolved)+len(contribution.Bindings) != 0 || len(contribution.Relationships) != 20 {
		t.Fatal("native source relationships were missing or duplicated", contribution, err)
	}
	input := plan.Input{Assets: values, ResolvedAssetIDs: []asset.AssetID{targets[0].ID}, Relationships: contribution.Relationships}
	if result, err := plan.Solve(input); err != nil || len(result.Blockers) == 0 {
		t.Fatal("target silently selected shared alert", result, err)
	}
	input.ResolvedAssetIDs = append(input.ResolvedAssetIDs, source.ID)
	planned, err := plan.Solve(input)
	if err != nil || len(planned.Blockers) != 0 || len(planned.Steps) != 2 {
		t.Fatal("explicit Monitor/target cleanup plan failed", planned, err)
	}
	request := servicePlanRequest(planned, values, targets[0])
	if len(request.PrerequisiteDeletions) != 1 {
		t.Fatal("native Monitor prerequisite not frozen", request)
	}
	driver, err := r.ResolveAction(t.Context(), "connection", request.Asset)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := driver.(*monitorTargetAction); !ok {
		t.Fatal("registered native target bypassed the shared dependency guard")
	}
	if _, err := driver.Execute(t.Context(), request); err == nil || len(s.deletes) != 0 {
		t.Fatal("target deleted before its reviewed native alert", err)
	}
	sourceDriver, err := r.ResolveAction(t.Context(), "connection", source)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sourceDriver.Execute(t.Context(), contracts.ActionRequest{Asset: source, Action: "delete"}); err != nil {
		t.Fatal("native prerequisite deletion failed", err)
	}
	result, err := driver.Execute(t.Context(), request)
	if err != nil || len(f.deletes) != 1 || len(s.deletes) != 1 || s.deletes[0] != targets[0].Identity.NativeID {
		t.Fatal("native target cleanup failed", result, err, s.deletes)
	}
	encoded, _ := json.Marshal(request)
	if json.Unmarshal(encoded, &request) != nil {
		t.Fatal("invalid recovered target request")
	}
	encoded, _ = json.Marshal(result)
	if json.Unmarshal(encoded, &result) != nil {
		t.Fatal("invalid recovered target receipt")
	}
	driver, err = r.ResolveAction(t.Context(), "connection", request.Asset)
	if err != nil {
		t.Fatal(err)
	}
	if waited, err := driver.Wait(t.Context(), request, result); err != nil || !waited.Done {
		t.Fatal("native target absence failed after JSON recovery", waited, err)
	}
	request.ExecutionResult = &result
	if read, err := driver.Readback(t.Context(), request); err != nil || read.Exists {
		t.Fatal("native target readback failed", read, err)
	}
	request.PrerequisiteDeletions = nil
	if _, err := driver.Readback(t.Context(), request); err == nil {
		t.Fatal("receipt accepted removal of the frozen Monitor prerequisite")
	}
}

func TestMonitorTargetAbsentAndLateSourceBoundaries(t *testing.T) {
	for _, mode := range []string{"present", "absent", "late", "source-group-gone", "index-403", "index-404", "forged-proof", "foreign-prerequisite"} {
		t.Run(mode, func(t *testing.T) {
			f, s, r, targets, source := monitorDiskTargets(t, 1)
			request := contracts.ActionRequest{Asset: targets[0], Action: "delete"}
			driver, err := r.ResolveAction(t.Context(), "connection", request.Asset)
			if err != nil {
				t.Fatal(err)
			}
			if mode != "present" {
				s.gone[targets[0].Identity.NativeID] = true
			}
			if mode == "source-group-gone" {
				_, group, _, _ := monitorResourceID(source.Identity.NativeID)
				delete(f.groups, group)
			}
			if mode == "late" {
				raw := f.objects[source.Identity.NativeID]
				delete(f.objects, source.Identity.NativeID)
				result, err := driver.Execute(t.Context(), request)
				if err != nil {
					t.Fatal(err)
				}
				request.ExecutionResult = &result
				f.objects[source.Identity.NativeID] = raw
			}
			if strings.HasPrefix(mode, "index-") {
				f.override = func(req *http.Request) (*http.Response, bool) {
					if strings.HasSuffix(strings.ToLower(req.URL.Path), "/providers/microsoft.insights/activitylogalerts") {
						status := 403
						if mode == "index-404" {
							status = 404
						}
						return jsonResponse(status, map[string]any{}, nil), true
					}
					return nil, false
				}
			}
			if mode == "forged-proof" || mode == "foreign-prerequisite" {
				delete(f.objects, source.Identity.NativeID)
				if mode == "forged-proof" {
					source.Normalized[monitorReferencesProof] = "forged"
				} else {
					source.Identity.ConnectionID = "another-connection"
				}
				request.PrerequisiteDeletions = []contracts.ActionImpact{{Asset: source, ControllerID: targets[0].ID, Delete: true}}
			}
			if _, err := driver.Preflight(t.Context(), request); err == nil {
				t.Fatal("target preflight ignored a dependency boundary")
			}
			if _, err := driver.Execute(t.Context(), request); err == nil || len(s.deletes) != 0 {
				t.Fatal("target boundary reached DELETE", err)
			}
			_, err = driver.Readback(t.Context(), request)
			if err == nil {
				t.Fatal("target absence hid an unverified native source")
			}
			if mode == "index-404" {
				var call *contracts.ProviderCallError
				if !errors.As(err, &call) || call.Provider.Category != execution.ErrorDependencyViolation || call.Provider.Code != "dependent_resource_not_found" {
					t.Fatal("executor could treat source-index failure as target absence", err)
				}
			}
		})
	}
}

func TestMonitorIncomingBatchDetectsNewSourceDuringRead(t *testing.T) {
	for _, phase := range []string{"graph", "execute", "readback"} {
		t.Run(phase, func(t *testing.T) {
			f, s, r, targets, _ := monitorDiskTargets(t, 1)
			reads := 0
			f.override = func(req *http.Request) (*http.Response, bool) {
				if strings.HasSuffix(strings.ToLower(req.URL.Path), "/providers/microsoft.insights/activitylogalerts") {
					reads++
					if reads == 1 {
						return jsonResponse(200, map[string]any{"value": []any{}}, nil), true
					}
				}
				return nil, false
			}
			var err error
			if phase == "graph" {
				contributor, resolveErr := r.ServiceLifecycle(t.Context(), "connection")
				if resolveErr != nil {
					t.Fatal(resolveErr)
				}
				_, err = contributor.Contribute(t.Context(), "scope", targets)
			} else {
				driver, resolveErr := r.ResolveAction(t.Context(), "connection", targets[0])
				if resolveErr != nil {
					t.Fatal(resolveErr)
				}
				request := contracts.ActionRequest{Asset: targets[0], Action: "delete"}
				if phase == "execute" {
					_, err = driver.Execute(t.Context(), request)
				} else {
					s.gone[targets[0].Identity.NativeID] = true
					_, err = driver.Readback(t.Context(), request)
				}
			}
			if err == nil || !strings.Contains(err.Error(), "monitor_incoming_references_changed") || len(s.deletes) != 0 || reads != 2 {
				t.Fatal("changing native reference set authorized cleanup", err, reads)
			}
		})
	}
}

func TestMonitorTargetManagedGroupInternalAndExternalRules(t *testing.T) {
	for _, destination := range []string{"group", "controller"} {
		for _, mode := range []string{"internal", "external-unindexed", "external-forged-impact"} {
			t.Run(destination+"/"+mode, func(t *testing.T) {
				f := newMonitorInventoryFixture(t, monitorActivityAlertType)
				group := "/subscriptions/" + testSubscription + "/resourcegroups/custom-nodes"
				if destination == "controller" {
					group = strings.ToLower(text(newAKSScenario().cluster["id"]))
				}
				for _, raw := range f.objects {
					object(raw["properties"])["scopes"] = []any{group}
					object(raw["properties"])["actions"] = map[string]any{"actionGroups": []any{}}
				}
				s, r, values, id := monitorManagedScenario(t, f)
				members, err := r.ClusterLifecycle(t.Context(), "connection")
				if err != nil {
					t.Fatal(err)
				}
				membership, err := members.Contribute(t.Context(), "scope", values)
				if err != nil {
					t.Fatal(err)
				}
				services, err := r.ServiceLifecycle(t.Context(), "connection")
				if err != nil {
					t.Fatal(err)
				}
				references, err := services.Contribute(t.Context(), "scope", values)
				if err != nil || len(references.Unresolved)+len(membership.Unresolved) != 0 {
					t.Fatal("native internal Monitor graph failed", references, err)
				}
				planned, err := plan.Solve(plan.Input{Assets: values, ResolvedAssetIDs: []asset.AssetID{values[0].ID}, Relationships: append(membership.Relationships, references.Relationships...), LifecycleBindings: membership.Bindings})
				if err != nil || len(planned.Blockers) != 0 || len(planned.Steps) != 1 {
					t.Fatal("internal rule acquired an independent deletion", planned, err)
				}
				request := servicePlanRequest(planned, values, values[0])
				request.IdempotencyKey = "delete-aks"
				if mode != "internal" {
					payload, _ := json.Marshal(f.objects[id])
					var external map[string]any
					json.Unmarshal(payload, &external)
					external["id"] = strings.Replace(id, "/custom-nodes/", "/outside-group/", 1)
					externalID := f.addRelated(t, external)
					if mode == "external-forged-impact" {
						source := f.asset(t, externalID)
						source.Identity.Partition = values[0].Identity.Partition
						request.LifecycleImpacts = append(request.LifecycleImpacts, contracts.ActionImpact{Asset: source, ControllerID: request.Asset.ID, Delete: true})
					}
				}
				driver, err := r.ResolveAction(t.Context(), "connection", request.Asset)
				if err != nil {
					t.Fatal(err)
				}
				result, err := driver.Execute(t.Context(), request)
				if mode != "internal" {
					if err == nil || s.deletes != 0 || len(f.deletes) != 0 {
						t.Fatal("unindexed/outside rule acquired managed-group ownership", result, err)
					}
					return
				}
				if err != nil || s.deletes != 1 || len(f.deletes) != 0 {
					t.Fatal("reviewed internal rule prevented native group deletion", result, err)
				}
				s.clusterGone, s.groupGone = true, true
				encoded, _ := json.Marshal(request)
				json.Unmarshal(encoded, &request)
				driver, err = r.ResolveAction(t.Context(), "connection", request.Asset)
				if err != nil {
					t.Fatal(err)
				}
				if wait, err := driver.Wait(t.Context(), request, result); err != nil || wait.Done {
					t.Fatal("native group absence hid its surviving internal rule", wait, err)
				}
				delete(f.objects, id)
				if wait, err := driver.Wait(t.Context(), request, result); err != nil || !wait.Done {
					t.Fatal("native internal-rule absence did not finish group recovery", wait, err)
				}
			})
		}
	}
}

func TestMonitorMissingReceiverTargetUsesFrozenIdentity(t *testing.T) {
	for _, mode := range []string{"workspace", "namespace", "hub", "bare-customer", "foreign-customer", "foreign-tenant", "customer-tampered", "proof-tampered", "configuration-tampered"} {
		t.Run(mode, func(t *testing.T) {
			f := newMonitorReceiverFixture(t)
			f.fault = func(req *http.Request) (*http.Response, bool) {
				if strings.HasSuffix(strings.ToLower(req.URL.Path), "/disasterrecoveryconfigs") {
					if req.Method != "GET" || req.URL.Query().Get("api-version") != "2024-01-01" {
						t.Fatal("wrong native namespace recovery index", req.URL)
					}
					return jsonResponse(200, map[string]any{"value": []any{}}, nil), true
				}
				return nil, false
			}
			c, err := f.runtime.resolve(t.Context(), "connection")
			if err != nil {
				t.Fatal(err)
			}
			raw := f.receivers[f.workspaceID]
			if mode == "namespace" || mode == "hub" || mode == "foreign-tenant" {
				raw = f.receivers[f.namespaceID]
				if mode == "hub" {
					raw = map[string]any{"id": f.namespaceID + "/eventhubs/notifications", "name": "notifications", "type": eventHubType, "location": "eastus", "properties": map[string]any{"partitionCount": 2, "status": "Active"}}
				}
			}
			target := dnsAsset(t, f.runtime, raw)
			encoded, _ := json.Marshal(target)
			if json.Unmarshal(encoded, &target) != nil {
				t.Fatal("invalid recovered receiver target")
			}
			target.Normalized = maps.Clone(target.Normalized)
			props := object(f.objects[f.sourceID]["properties"])
			switch mode {
			case "bare-customer":
				object(array(props["itsmReceivers"])[0])["workspaceId"] = monitorReceiverCustomerID
			case "foreign-customer":
				object(array(props["itsmReceivers"])[0])["workspaceId"] = testTenant + "|" + monitorReceiverCustomerID
			case "foreign-tenant":
				object(array(props["eventHubReceivers"])[0])["tenantId"] = testSubscription
			case "customer-tampered":
				target.Normalized["customerId"] = testTenant
			case "proof-tampered":
				target.Normalized[monitorReceiverTargetProof] = "forged"
			case "configuration-tampered":
				target.Normalized["_monitor_private_link_target_configuration"] = "forged"
			}
			if target.Identity.NativeType == insightsWorkspaceType {
				delete(f.receivers, f.workspaceID)
			} else {
				delete(f.receivers, f.namespaceID)
			}
			incoming, err := c.monitorIncoming(t.Context(), target)
			if strings.HasSuffix(mode, "tampered") {
				if err == nil {
					t.Fatal("missing target accepted forged native customer identity", incoming)
				}
				return
			}
			if strings.HasPrefix(mode, "foreign-") {
				if err != nil || len(incoming) != 0 {
					t.Fatal("foreign receiver acquired a local missing target", incoming, err)
				}
				return
			}
			if err != nil || len(incoming) != 1 || incoming[0].id != f.sourceID {
				t.Fatal("native receiver disappeared with its target", incoming, err)
			}
			driver, err := f.runtime.ResolveAction(t.Context(), "connection", target)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := driver.Readback(t.Context(), contracts.ActionRequest{Asset: target, Action: "delete"}); err == nil || !strings.Contains(err.Error(), "monitor_target_has_incoming_references") {
				t.Fatal("registered target readback ignored unresolved native receiver", err)
			}
		})
	}
}
