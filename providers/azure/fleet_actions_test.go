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

func fleetCleanupRequest(t *testing.T, f *fleetFixture, kind string) contracts.ActionRequest {
	t.Helper()
	values := f.assets(t)
	target := fleetAssetByKind(t, values, kind)
	request := contracts.ActionRequest{Asset: target, Action: "delete", IdempotencyKey: "fleet-cleanup"}
	if kind == fleetRunType {
		for _, value := range values {
			if value.Identity.NativeType == fleetGateType {
				request.LifecycleImpacts = append(request.LifecycleImpacts, contracts.ActionImpact{Asset: value, ControllerID: target.ID, Delete: true})
			}
		}
	}
	f.override = func(req *http.Request) (*http.Response, bool) { return fleetGraphEmptyIndexes(t, req) }
	return request
}

func fleetCleanupDriver(t *testing.T, f *fleetFixture, request contracts.ActionRequest) contracts.ActionDriver {
	t.Helper()
	driver, err := f.runtime.ResolveAction(t.Context(), "connection", request.Asset)
	if err != nil {
		t.Fatal("Fleet native driver is not registered", err)
	}
	if guard, ok := driver.(*monitorTargetAction); !ok {
		t.Fatal("Fleet escaped shared incoming dependencies")
	} else if _, ok := guard.inner.(*fleetAction); !ok {
		t.Fatal("Fleet bypassed native lifecycle")
	}
	return driver
}

func fleetSerializedResult(t *testing.T, result contracts.ActionResult, waited *contracts.WaitResult) contracts.ActionResult {
	t.Helper()
	if waited != nil && waited.Data != nil {
		result.Data = maps.Clone(result.Data)
		maps.Copy(result.Data, waited.Data)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var decoded contracts.ActionResult
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	return decoded
}

func fleetAssertMutation(t *testing.T, f *fleetFixture, request contracts.ActionRequest, req *http.Request, phase string) {
	t.Helper()
	id := request.Asset.Identity.NativeID
	path, method := id, "DELETE"
	if phase == "stop" {
		path, method = path+"/stop", "POST"
	}
	if req.Method != method || !strings.EqualFold(req.URL.Path, path) || req.URL.Host != "management.azure.com" || len(req.URL.Query()) != 1 || req.URL.Query().Get("api-version") != fleetVersion || req.ContentLength > 0 || req.Header.Get("If-Match") != text(f.resources[id]["eTag"]) || req.Header.Get("x-ms-client-request-id") != azureRequestID(request.IdempotencyKey+":fleet:"+phase) {
		t.Fatal("Fleet mutation changed its native bound contract", req.Method, req.URL, req.Header)
	}
}

func TestFleetRegisteredIndependentChildDeletion(t *testing.T) {
	for _, kind := range fleetDirectKinds {
		if kind == fleetMeshType {
			continue // Mesh has a separate conditional disconnect lifecycle.
		}
		t.Run(last(kind), func(t *testing.T) {
			f := newFleetFixture(t)
			for id, raw := range f.resources {
				if kind == fleetMemberType && raw["type"] == fleetNamespaceType || kind == fleetStrategyType && raw["type"] == fleetProfileType {
					delete(f.resources, id) // These independently reviewed prerequisites are already absent.
				}
			}
			request := fleetCleanupRequest(t, f, kind)
			id := request.Asset.Identity.NativeID
			if kind == fleetRunType {
				object(object(object(f.resources[id]["properties"])["status"])["status"])["state"] = "Completed"
			}
			deletes := 0
			f.override = func(req *http.Request) (*http.Response, bool) {
				if req.Method != "GET" {
					fleetAssertMutation(t, f, request, req, "delete")
					deletes++
					delete(f.resources, id)
					for _, impact := range request.LifecycleImpacts {
						delete(f.resources, impact.Asset.Identity.NativeID)
					}
					return jsonResponse(204, nil, nil), true
				}
				return fleetGraphEmptyIndexes(t, req)
			}
			driver := fleetCleanupDriver(t, f, request)
			check, err := driver.Preflight(t.Context(), request)
			if err != nil || !check.Allowed || check.Absent {
				t.Fatal("Fleet preflight failed", check, err)
			}
			result, err := driver.Execute(t.Context(), request)
			if err != nil || deletes != 1 || result.Data["fleet_phase"] != "delete" {
				t.Fatal("Fleet native deletion failed", result, err)
			}
			result = fleetSerializedResult(t, result, nil)
			// Resolve again after serialization, as a separate execution worker does.
			driver = fleetCleanupDriver(t, f, request)
			waited, err := driver.Wait(t.Context(), request, result)
			if err != nil || !waited.Done {
				t.Fatal("Fleet deletion did not verify native absence", waited, err)
			}
			request.ExecutionResult = &result
			read, err := driver.Readback(t.Context(), request)
			if err != nil || read.Exists || deletes != 1 {
				t.Fatal("Fleet residual readback failed", read, err)
			}
			if f.resources[strings.ToLower(resourceID(fleetType, "fleet1"))] == nil {
				t.Fatal("child deletion removed shared parent")
			}
			for call := range f.calls {
				if strings.Contains(call, "/managedclusters/") {
					t.Fatal("member enrollment became AKS ownership", call)
				}
			}
		})
	}
}

func TestFleetUpdateRunStopDeleteAndRecovery(t *testing.T) {
	for _, initial := range []string{"Running", "Pending", "Skipped", "Stopping"} {
		for _, responseMode := range []string{"synchronous", "asynchronous"} {
			t.Run(initial+"/"+responseMode, func(t *testing.T) {
				f := newFleetFixture(t)
				request := fleetCleanupRequest(t, f, fleetRunType)
				id := request.Asset.Identity.NativeID
				gate := request.LifecycleImpacts[0].Asset.Identity.NativeID
				run := f.resources[id]
				progress := object(object(object(run["properties"])["status"])["status"])
				progress["state"] = initial
				stopURL := "https://management.azure.com/subscriptions/" + testSubscription + "/resourceGroups/test/providers/Microsoft.ContainerService/locations/westus/operationResults/" + rbacTestRoleName + "?api-version=2022-02-01"
				deleteURL := strings.Replace(stopURL, rbacTestRoleName, rbacTestAssignmentName, 1)
				stopDone, deletes, stops := false, 0, 0
				f.override = func(req *http.Request) (*http.Response, bool) {
					if req.URL.String() == stopURL || req.URL.String() == deleteURL {
						if req.Method != "GET" {
							t.Fatal("poll mutated the operation")
						}
						state := "Succeeded"
						if req.URL.String() == stopURL && !stopDone {
							state = "InProgress"
						}
						var headers http.Header
						if state == "InProgress" {
							headers = http.Header{"Azure-Asyncoperation": {stopURL}}
						}
						return jsonResponse(200, map[string]any{"status": state}, headers), true
					}
					if req.Method == "POST" {
						fleetAssertMutation(t, f, request, req, "stop")
						stops++
						progress["state"], run["eTag"] = "Stopping", `"stop-etag"`
						if responseMode == "synchronous" {
							return jsonResponse(200, run, nil), true
						}
						return jsonResponse(202, nil, http.Header{"Azure-Asyncoperation": {stopURL}}), true
					}
					if req.Method == "DELETE" {
						fleetAssertMutation(t, f, request, req, "delete")
						if progress["state"] != "Stopped" {
							t.Fatal("Fleet deleted an active update run")
						}
						deletes++
						object(run["properties"])["provisioningState"] = "Deleting"
						return jsonResponse(202, nil, http.Header{"Location": {deleteURL}}), true
					}
					return fleetGraphEmptyIndexes(t, req)
				}
				driver := fleetCleanupDriver(t, f, request)
				result, err := driver.Execute(t.Context(), request)
				if err != nil || result.Data["fleet_phase"] != "stop" || deletes != 0 {
					t.Fatal("Fleet did not stop before deletion", result, err)
				}
				origin := result.ProviderOperationID
				wantStops := 1
				if initial == "Stopping" {
					wantStops = 0
				}
				if stops != wantStops {
					t.Fatal("existing stop was repeated", stops)
				}
				wait := func() contracts.WaitResult {
					t.Helper()
					result = fleetSerializedResult(t, result, nil)
					driver = fleetCleanupDriver(t, f, request)
					waited, err := driver.Wait(t.Context(), request, result)
					if err != nil {
						t.Fatal("serialized Fleet phase failed", waited, err)
					}
					result = fleetSerializedResult(t, result, &waited)
					if result.ProviderOperationID != origin {
						t.Fatal("worker's original operation ID was overwritten")
					}
					return waited
				}
				if waited := wait(); waited.Done || deletes != 0 {
					t.Fatal("unfinished stop permitted deletion", waited)
				}
				stopDone = true
				if waited := wait(); waited.Done || deletes != 0 {
					t.Fatal("stop operation success became run termination", waited)
				}
				progress["state"], run["eTag"] = "Stopped", `"stopped-etag"`
				object(f.resources[gate]["properties"])["state"] = "Skipped"
				if waited := wait(); waited.Done || deletes != 1 || result.Data["fleet_phase"] != "delete" || result.Data["fleet_phase_operation"] != deleteURL {
					t.Fatal("stopped run did not enter native deletion", waited, result, deletes)
				}
				if object(result.Data["fleet_operation"])["fleet_poll_operation"] != nil {
					t.Fatal("Stop polling successor survived the Delete phase")
				}
				if waited := wait(); waited.Done {
					t.Fatal("operation success became live run absence", waited)
				}
				delete(f.resources, id)
				if waited := wait(); waited.Done {
					t.Fatal("run disappearance hid a residual Gate", waited)
				}
				delete(f.resources, gate)
				if waited := wait(); !waited.Done || stops != wantStops || deletes != 1 {
					t.Fatal("Fleet did not finish without repeating mutations", waited, stops, deletes)
				}
				request.ExecutionResult = &result
				if read, err := driver.Readback(t.Context(), request); err != nil || read.Exists {
					t.Fatal("Fleet final readback failed", read, err)
				}
			})
		}
	}
}

func TestFleetCleanupPlanAndReceiptTamperingBeforeIO(t *testing.T) {
	f := newFleetFixture(t)
	request := fleetCleanupRequest(t, f, fleetRunType)
	id := request.Asset.Identity.NativeID
	object(object(object(f.resources[id]["properties"])["status"])["status"])["state"] = "Stopping"
	driver := fleetCleanupDriver(t, f, request)
	result, err := driver.Execute(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"phase", "operation", "origin", "polling", "signature", "proof", "request-proof", "request-impact", "request-identity"} {
		t.Run(mode, func(t *testing.T) {
			candidate := fleetSerializedResult(t, result, nil)
			changed := request
			changed.Asset.Normalized = maps.Clone(request.Asset.Normalized)
			switch mode {
			case "phase":
				candidate.Data["fleet_phase"] = "delete"
			case "operation":
				candidate.Data["fleet_phase_operation"] = "https://foreign.invalid/poll"
			case "origin":
				candidate.ProviderOperationID = "different"
			case "polling":
				object(candidate.Data["fleet_operation"])["polling"] = "status"
			case "signature":
				candidate.Data["fleet_phase_binding"] = "forged"
			case "proof":
				object(candidate.Data["fleet_operation"])["fleet_operation_binding"] = "forged"
			case "request-proof":
				changed.Asset.Normalized[fleetConfigurationProof] = "different"
			case "request-impact":
				changed.LifecycleImpacts = nil
			case "request-identity":
				changed.Asset.Identity.ConnectionID = "another"
			}
			clear(f.calls)
			if _, err := driver.Wait(t.Context(), changed, candidate); err == nil || len(f.calls) != 0 {
				t.Fatal("invalid Fleet phase reached an API", err, f.calls)
			}
			changed.ExecutionResult = &candidate
			if _, err := driver.Readback(t.Context(), changed); err == nil || len(f.calls) != 0 {
				t.Fatal("invalid Fleet readback receipt reached an API", err, f.calls)
			}
		})
	}
	// A retained request with a genuine receipt resumes without a second POST.
	request.ExecutionResult = &result
	clear(f.calls)
	if _, err := driver.Execute(t.Context(), request); err != nil || f.calls["POST "+id+"/stop"] != 0 {
		t.Fatal("execution recovery repeated stop", err)
	}
}

func TestFleetCleanupRevalidatesNativeBoundaries(t *testing.T) {
	for _, scenario := range []string{"private change", "parent change", "group ownership", "protected group", "protected parent", "protected resource", "lock", "child lock", "unknown state", "status alias", "resource busy", "missing etag", "wildcard etag", "etag disagreement", "gate configuration", "gate retargeted", "gate omitted", "unreviewed gate", "gate forbidden", "gate list forbidden", "gate list 404"} {
		t.Run(scenario, func(t *testing.T) {
			f := newFleetFixture(t)
			request := fleetCleanupRequest(t, f, fleetRunType)
			id := request.Asset.Identity.NativeID
			gate := request.LifecycleImpacts[0].Asset.Identity.NativeID
			run := f.resources[id]
			progress := object(object(object(run["properties"])["status"])["status"])
			progress["state"] = "Completed"
			var override func(*http.Request) (*http.Response, bool)
			switch scenario {
			case "private change":
				object(run["properties"])["futurePrivateSetting"] = "changed"
			case "parent change":
				object(f.resources[fleetParent(id, fleetRunType)]["properties"])["futurePrivateSetting"] = "changed"
			case "group ownership":
				f.group["managedBy"] = resourceID(fleetType, "another")
			case "protected group":
				f.group["tags"] = map[string]any{"steward:protected": "true"}
			case "protected parent":
				f.resources[fleetParent(id, fleetRunType)]["tags"] = map[string]any{"steward:protected": "true"}
			case "protected resource":
				run["tags"] = map[string]any{"steward:protected": "true"}
			case "lock", "child lock":
				lockedID := id
				if scenario == "child lock" {
					lockedID = gate
				}
				f.locks = []any{map[string]any{"id": lockedID + "/providers/Microsoft.Authorization/locks/protect", "properties": map[string]any{"level": "CanNotDelete"}}}
			case "unknown state":
				progress["state"] = "Restarting"
			case "status alias":
				progress["State"] = "Running"
			case "resource busy":
				object(run["properties"])["provisioningState"] = "Updating"
			case "missing etag":
				delete(run, "eTag")
			case "wildcard etag":
				run["eTag"] = "*"
			case "etag disagreement":
				override = func(req *http.Request) (*http.Response, bool) {
					if strings.EqualFold(req.URL.Path, id) {
						return jsonResponse(200, run, http.Header{"Etag": {`"other"`}}), true
					}
					return nil, false
				}
			case "gate configuration":
				object(f.resources[gate]["properties"])["futurePrivateSetting"] = "changed"
			case "gate retargeted":
				object(object(f.resources[gate]["properties"])["target"])["id"] = id + "other"
			case "gate omitted":
				f.omitted[gate] = true
			case "unreviewed gate":
				request.LifecycleImpacts = nil
			case "gate forbidden", "gate list forbidden", "gate list 404":
				override = func(req *http.Request) (*http.Response, bool) {
					target := gate
					if scenario != "gate forbidden" {
						target = fleetParent(id, fleetRunType) + "/gates"
					}
					if strings.EqualFold(req.URL.Path, target) {
						code, status := "AuthorizationFailed", 403
						if scenario == "gate list 404" {
							code, status = "ResourceNotFound", 404
						}
						return jsonResponse(status, map[string]any{"error": map[string]any{"code": code}}, nil), true
					}
					return nil, false
				}
			}
			mutations := 0
			f.override = func(req *http.Request) (*http.Response, bool) {
				if req.Method != "GET" {
					mutations++
					fleetAssertMutation(t, f, request, req, "delete")
					return jsonResponse(204, nil, nil), true
				}
				if override != nil {
					if response, ok := override(req); ok {
						return response, true
					}
				}
				return fleetGraphEmptyIndexes(t, req)
			}
			driver := fleetCleanupDriver(t, f, request)
			_, err := driver.Execute(t.Context(), request)
			if scenario == "gate omitted" {
				if err != nil || mutations != 1 || f.calls["GET "+gate] < 2 {
					t.Fatal("known Gate was not reconciled", err, f.calls)
				}
			} else if err == nil || mutations != 0 || isNotFound(err) {
				t.Fatal("changed/unreadable Fleet boundary permitted mutation or absence", err, mutations)
			}
		})
	}
}

func TestFleetTerminalRunStatesAndUnverifiedRootActions(t *testing.T) {
	for _, state := range []string{"NotStarted", "Stopped", "Failed", "Completed"} {
		t.Run(state, func(t *testing.T) {
			f := newFleetFixture(t)
			request := fleetCleanupRequest(t, f, fleetRunType)
			object(object(object(f.resources[request.Asset.Identity.NativeID]["properties"])["status"])["status"])["state"] = state
			deleted := false
			f.override = func(req *http.Request) (*http.Response, bool) {
				if req.Method != "GET" {
					fleetAssertMutation(t, f, request, req, "delete")
					deleted = true
					return jsonResponse(204, nil, nil), true
				}
				return fleetGraphEmptyIndexes(t, req)
			}
			if _, err := fleetCleanupDriver(t, f, request).Execute(t.Context(), request); err != nil || !deleted {
				t.Fatal("terminal Fleet run could not be deleted", state, err)
			}
		})
	}
	f := newFleetFixture(t)
	for _, value := range f.assets(t) {
		if !slices.Contains([]string{fleetType, fleetGateType}, value.Identity.NativeType) {
			continue
		}
		clear(f.calls)
		if _, err := f.runtime.ResolveAction(t.Context(), asset.ConnectionID("connection"), value); err == nil || len(f.calls) != 0 {
			t.Fatal("unverified root or independently unsupported Gate became actionable", err)
		}
	}
}

func TestFleetNamespaceDeletionPolicyAndConditionalFailures(t *testing.T) {
	for _, policy := range []string{"Keep", "Delete"} {
		for _, scenario := range []string{"native", "changed policy", "conditional conflict", "forbidden mutation", "invalid response", "parent absent"} {
			t.Run(policy+"/"+scenario, func(t *testing.T) {
				f := newFleetFixture(t)
				var id string
				for key, raw := range f.resources {
					if raw["type"] == fleetNamespaceType {
						id = key
						object(raw["properties"])["deletePolicy"] = policy
					}
				}
				request := fleetCleanupRequest(t, f, fleetNamespaceType)
				mutations := 0
				if scenario == "changed policy" {
					next := "Delete"
					if policy == "Delete" {
						next = "Keep"
					}
					object(f.resources[id]["properties"])["deletePolicy"] = next
				}
				if scenario == "parent absent" {
					delete(f.resources, fleetParent(id, fleetNamespaceType))
				}
				f.override = func(req *http.Request) (*http.Response, bool) {
					if req.Method != "GET" {
						fleetAssertMutation(t, f, request, req, "delete")
						mutations++
						switch scenario {
						case "conditional conflict":
							return jsonResponse(412, map[string]any{"error": map[string]any{"code": "PreconditionFailed"}}, nil), true
						case "forbidden mutation":
							return jsonResponse(403, map[string]any{"error": map[string]any{"code": "AuthorizationFailed"}}, nil), true
						case "invalid response":
							return jsonResponse(202, nil, nil), true
						}
						if object(f.resources[id]["properties"])["deletePolicy"] != policy {
							t.Fatal("native deletion policy was rewritten")
						}
						delete(f.resources, id)
						return jsonResponse(204, nil, nil), true
					}
					return fleetGraphEmptyIndexes(t, req)
				}
				driver := fleetCleanupDriver(t, f, request)
				result, err := driver.Execute(t.Context(), request)
				if scenario == "native" {
					if err != nil || mutations != 1 {
						t.Fatal("native namespace policy could not be deleted", policy, err)
					}
					if waited, err := driver.Wait(t.Context(), request, fleetSerializedResult(t, result, nil)); err != nil || !waited.Done {
						t.Fatal("native namespace deletion did not verify its own absence", waited, err)
					}
				} else {
					expected := 1
					if scenario == "changed policy" || scenario == "parent absent" {
						expected = 0
					}
					if err == nil || mutations != expected || isNotFound(err) {
						t.Fatal("namespace boundary produced unsafe success or absence", scenario, err, mutations)
					}
				}
			})
		}
	}
}

func TestFleetCleanupOwnAbsenceAndReadbackFailures(t *testing.T) {
	for _, kind := range []string{fleetProfileType, fleetRunType} {
		for _, scenario := range []string{"already gone", "mutation 404 live", "mutation 404 gone", "read forbidden", "recreated", "parent gone", "gate forbidden", "unreviewed residual gate"} {
			if kind != fleetRunType && strings.Contains(scenario, "gate") {
				continue
			}
			t.Run(last(kind)+"/"+scenario, func(t *testing.T) {
				f := newFleetFixture(t)
				request := fleetCleanupRequest(t, f, kind)
				id := request.Asset.Identity.NativeID
				if kind == fleetRunType {
					object(object(object(f.resources[id]["properties"])["status"])["status"])["state"] = "Completed"
				}
				gone := func() {
					delete(f.resources, id)
					for _, impact := range request.LifecycleImpacts {
						delete(f.resources, impact.Asset.Identity.NativeID)
					}
				}
				if scenario == "already gone" {
					gone()
				}
				deleted := false
				f.override = func(req *http.Request) (*http.Response, bool) {
					path := strings.ToLower(req.URL.Path)
					if req.Method != "GET" {
						fleetAssertMutation(t, f, request, req, "delete")
						if deleted {
							t.Fatal("Fleet repeated deletion during readback")
						}
						deleted = true
						if scenario == "mutation 404 gone" {
							gone()
						}
						if strings.HasPrefix(scenario, "mutation 404") {
							return jsonResponse(404, map[string]any{"error": map[string]any{"code": "ResourceNotFound"}}, nil), true
						}
						return jsonResponse(204, nil, nil), true
					}
					if deleted && (scenario == "read forbidden" && path == id || scenario == "gate forbidden" && len(request.LifecycleImpacts) > 0 && path == request.LifecycleImpacts[0].Asset.Identity.NativeID) {
						return jsonResponse(403, map[string]any{"error": map[string]any{"code": "AuthorizationFailed"}}, nil), true
					}
					return fleetGraphEmptyIndexes(t, req)
				}
				driver := fleetCleanupDriver(t, f, request)
				result, err := driver.Execute(t.Context(), request)
				if err != nil {
					t.Fatal(err)
				}
				switch scenario {
				case "recreated":
					object(f.resources[id]["systemData"])["createdAt"] = "2026-09-11T00:00:00Z"
				case "parent gone":
					delete(f.resources, fleetParent(id, kind))
				case "gate forbidden":
					delete(f.resources, id)
				case "unreviewed residual gate":
					late := fleetTestBody(t, fleetGateType, rbacTestAssignmentName)
					f.resources[text(late["id"])] = late
					gone()
				}
				waited, err := driver.Wait(t.Context(), request, fleetSerializedResult(t, result, nil))
				switch scenario {
				case "already gone", "mutation 404 gone":
					if err != nil || !waited.Done {
						t.Fatal("verified Fleet absence did not complete", waited, err)
					}
				case "mutation 404 live":
					if err != nil || waited.Done {
						t.Fatal("mutation 404 hid a live Fleet resource", waited, err)
					}
				default:
					if err == nil || isNotFound(err) || waited.Done {
						t.Fatal("missing context or unreadable residual became Fleet absence", scenario, waited, err)
					}
				}
			})
		}
	}
}
