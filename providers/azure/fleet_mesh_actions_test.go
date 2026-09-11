package azure

import (
	"encoding/json"
	"maps"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/provider/contracts"
)

type fleetMeshCleanupFixture struct {
	*fleetFixture
	request             contracts.ActionRequest
	id, member, pollURL string
	mutations           []string
	putStatus           int
	async               bool
}

func newFleetMeshCleanupFixture(t *testing.T) *fleetMeshCleanupFixture {
	t.Helper()
	f, id := newFleetMeshFixture(t)
	values := fleetMeshAssets(t, f)
	profile := fleetAssetByKind(t, values, fleetMeshType)
	h := &fleetMeshCleanupFixture{fleetFixture: f, id: id, member: fleetParent(id, fleetMeshType) + "/members/member1", putStatus: 200, request: contracts.ActionRequest{Asset: profile, Action: "delete", IdempotencyKey: "fleet-mesh-cleanup"}}
	h.pollURL = "https://management.azure.com/subscriptions/" + testSubscription + "/resourceGroups/test/providers/Microsoft.ContainerService/locations/westus/operationResults/" + rbacTestRoleName + "?api-version=2022-02-01"
	f.override = func(req *http.Request) (*http.Response, bool) {
		if req.URL.String() == h.pollURL {
			if req.Method != "GET" {
				t.Fatal("mesh poll was mutated")
			}
			return jsonResponse(200, map[string]any{"status": "Succeeded"}, nil), true
		}
		if req.Method == "GET" {
			return fleetGraphEmptyIndexes(t, req)
		}
		phase := map[string]string{"PUT": "mesh-prepare", "POST": "mesh-apply", "DELETE": "delete"}[req.Method]
		path := id
		if req.Method == "POST" {
			path += "/apply"
		}
		raw := f.resources[id]
		if phase == "" || !strings.EqualFold(req.URL.Path, path) || req.URL.Host != "management.azure.com" || req.URL.Query().Get("api-version") != fleetMeshVersion || len(req.URL.Query()) != 1 || req.Header.Get("If-Match") != text(raw["eTag"]) || req.Header.Get("x-ms-client-request-id") != azureRequestID(h.request.IdempotencyKey+":fleet:"+phase) {
			t.Fatal("mesh mutation escaped its conditional native contract", req.Method, req.URL, req.Header)
		}
		h.mutations = append(h.mutations, req.Method)
		switch req.Method {
		case "PUT":
			var body map[string]any
			if json.NewDecoder(req.Body).Decode(&body) != nil || len(body) != 1 || !fleetMeshSelectorEmpty(body) || object(body["properties"])["futurePrivateSetting"] != "mesh-private-configuration" || object(body["properties"])["status"] != nil || object(body["properties"])["provisioningState"] != nil {
				t.Fatal("mesh disconnect overwrote authored fields or selected new members", body)
			}
			status := object(raw["properties"])["status"]
			raw["properties"] = body["properties"]
			object(raw["properties"])["provisioningState"], object(raw["properties"])["status"] = "Succeeded", status
			// Microsoft's retained PUT response resets createdAt. The phase
			// must authenticate the actual response without losing its origin.
			object(raw["systemData"])["createdAt"] = "2026-07-13T11:46:50.2088924Z"
			raw["eTag"] = "\"mesh-prepared\""
			return jsonResponse(h.putStatus, raw, nil), true
		case "POST":
			if req.ContentLength > 0 || !fleetMeshSelectorEmpty(raw) {
				t.Fatal("mesh Apply could connect unreviewed members")
			}
			object(raw["properties"])["status"] = map[string]any{"state": "Applying", "lastAppliedMemberSelector": map[string]any{"byLabel": ""}}
			object(object(object(f.resources[h.member]["properties"])["meshProperties"])["status"])["state"] = "Disconnecting"
			raw["eTag"] = "\"mesh-applying\""
			if h.async {
				return jsonResponse(202, nil, http.Header{"Location": {h.pollURL}}), true
			}
			return jsonResponse(200, raw, nil), true
		case "DELETE":
			if req.ContentLength > 0 || object(f.resources[h.member]["properties"])["meshProperties"] != nil {
				t.Fatal("mesh deleted before its member disconnected")
			}
			object(raw["properties"])["provisioningState"] = "Deleting"
			return jsonResponse(204, nil, nil), true
		}
		return nil, false
	}
	return h
}

func (h *fleetMeshCleanupFixture) disconnected() {
	delete(object(h.resources[h.member]["properties"]), "meshProperties")
	object(object(h.resources[h.id]["properties"])["status"])["state"] = "NotConnected"
	h.resources[h.id]["eTag"] = "\"mesh-disconnected\""
}

func TestFleetMeshRegisteredDisconnectDeleteAndRecovery(t *testing.T) {
	for _, putStatus := range []int{200, 201} {
		for _, asynchronous := range []bool{false, true} {
			t.Run(strings.Join([]string{http.StatusText(putStatus), map[bool]string{false: "synchronous", true: "asynchronous"}[asynchronous]}, "/"), func(t *testing.T) {
				h := newFleetMeshCleanupFixture(t)
				h.putStatus, h.async = putStatus, asynchronous
				driver := fleetCleanupDriver(t, h.fleetFixture, h.request)
				if check, err := driver.Preflight(t.Context(), h.request); err != nil || !check.Allowed || check.Absent {
					t.Fatal("mesh preflight failed", check, err)
				}
				result, err := driver.Execute(t.Context(), h.request)
				if err != nil || result.Data["fleet_phase"] != "mesh-prepare" || !slices.Equal(h.mutations, []string{"PUT"}) {
					t.Fatal("mesh did not prepare disconnection", result, err, h.mutations)
				}
				result = fleetSerializedResult(t, result, nil)
				driver = fleetCleanupDriver(t, h.fleetFixture, h.request)
				waited, err := driver.Wait(t.Context(), h.request, result)
				if err != nil || waited.Done || waited.Data["fleet_phase"] != "mesh-apply" || !slices.Equal(h.mutations, []string{"PUT", "POST"}) {
					t.Fatal("prepared mesh did not start Apply", waited, err, h.mutations)
				}
				result = fleetSerializedResult(t, result, &waited)
				driver = fleetCleanupDriver(t, h.fleetFixture, h.request)
				waited, err = driver.Wait(t.Context(), h.request, result)
				if err != nil || waited.Done || len(h.mutations) != 2 {
					t.Fatal("Apply acknowledgment became disconnection", waited, err)
				}
				result = fleetSerializedResult(t, result, &waited)
				h.disconnected()
				waited, err = driver.Wait(t.Context(), h.request, result)
				if err != nil || waited.Done || waited.Data["fleet_phase"] != "delete" || !slices.Equal(h.mutations, []string{"PUT", "POST", "DELETE"}) {
					t.Fatal("disconnected mesh did not proceed to delete", waited, err, h.mutations)
				}
				result = fleetSerializedResult(t, result, &waited)
				driver = fleetCleanupDriver(t, h.fleetFixture, h.request)
				waited, err = driver.Wait(t.Context(), h.request, result)
				if err != nil || waited.Done {
					t.Fatal("mesh DELETE acknowledgment became absence", waited, err)
				}
				delete(h.resources, h.id)
				waited, err = driver.Wait(t.Context(), h.request, result)
				if err != nil || !waited.Done {
					t.Fatal("mesh did not verify profile and member residuals", waited, err)
				}
				h.request.ExecutionResult = &result
				if _, err := driver.Execute(t.Context(), h.request); err != nil || len(h.mutations) != 3 {
					t.Fatal("restored mesh phase repeated a mutation", err, h.mutations)
				}
				if h.resources[h.member] == nil || h.resources[fleetParent(h.id, fleetMeshType)+"/members/member2"] == nil {
					t.Fatal("mesh cleanup removed Fleet members")
				}
				for call := range h.calls {
					if strings.Contains(call, "/managedclusters/") || strings.Contains(call, "/connectedclusters/") {
						t.Fatal("mesh cleanup independently accessed a member cluster", call)
					}
				}
			})
		}
	}
}

func TestFleetMeshApplyingAndEmptyProfiles(t *testing.T) {
	for _, initial := range []string{"Applying", "NotConnected", "Failed"} {
		t.Run(initial, func(t *testing.T) {
			f, id := newFleetMeshFixture(t)
			memberID := fleetParent(id, fleetMeshType) + "/members/member1"
			object(object(f.resources[id]["properties"])["status"])["state"] = initial
			if initial != "Applying" {
				delete(object(f.resources[memberID]["properties"]), "meshProperties")
			}
			profile := fleetAssetByKind(t, fleetMeshAssets(t, f), fleetMeshType)
			request := contracts.ActionRequest{Asset: profile, Action: "delete", IdempotencyKey: "mesh-empty"}
			deletes := 0
			f.override = func(req *http.Request) (*http.Response, bool) {
				if req.Method != "GET" {
					if req.Method != "DELETE" || !strings.EqualFold(req.URL.Path, id) {
						t.Fatal("empty mesh or ongoing Apply was modified", req.Method, req.URL)
					}
					deletes++
					delete(f.resources, id)
					return jsonResponse(204, nil, nil), true
				}
				return fleetGraphEmptyIndexes(t, req)
			}
			driver := fleetCleanupDriver(t, f, request)
			result, err := driver.Execute(t.Context(), request)
			if err != nil {
				t.Fatal(err)
			}
			if initial == "Applying" {
				if result.Data["fleet_phase"] != "mesh-wait" || deletes != 0 {
					t.Fatal("existing Apply was interrupted", result, deletes)
				}
				if waited, err := driver.Wait(t.Context(), request, fleetSerializedResult(t, result, nil)); err != nil || waited.Done || deletes != 0 {
					t.Fatal("existing Apply did not remain pending", waited, err)
				}
			} else if waited, err := driver.Wait(t.Context(), request, fleetSerializedResult(t, result, nil)); err != nil || !waited.Done || deletes != 1 {
				t.Fatal("empty mesh needed an unnecessary Apply", waited, err, deletes)
			}
		})
	}
}

func TestFleetMeshDeletionRequiresEveryKnownAttachmentGone(t *testing.T) {
	for _, scenario := range []string{"live attachment", "omitted attachment", "missing parent", "forbidden member", "member absent", "disconnected member"} {
		t.Run(scenario, func(t *testing.T) {
			h := newFleetMeshCleanupFixture(t)
			delete(h.resources, h.id)
			switch scenario {
			case "omitted attachment":
				h.omitted[h.member] = true
			case "missing parent":
				delete(h.resources, fleetParent(h.id, fleetMeshType))
			case "forbidden member":
				previous := h.override
				h.override = func(req *http.Request) (*http.Response, bool) {
					if strings.EqualFold(req.URL.Path, h.member) {
						return jsonResponse(403, map[string]any{"error": map[string]any{"code": "AuthorizationFailed"}}, nil), true
					}
					return previous(req)
				}
			case "member absent":
				delete(h.resources, h.member)
			case "disconnected member":
				delete(object(h.resources[h.member]["properties"]), "meshProperties")
			}
			driver := fleetCleanupDriver(t, h.fleetFixture, h.request)
			read, err := driver.Readback(t.Context(), h.request)
			if scenario == "forbidden member" {
				if err == nil || isNotFound(err) {
					t.Fatal("forbidden attachment became absence", read, err)
				}
			} else if err != nil || read.Exists != (scenario != "member absent" && scenario != "disconnected member") {
				t.Fatal("profile or parent absence hid a known attachment", read, err)
			}
			if len(h.mutations) != 0 || h.calls["GET "+h.member] == 0 {
				t.Fatal("residual verification did not use the member's own GET", h.mutations, h.calls)
			}
		})
	}
}

func TestFleetMeshDisconnectBoundaryFailures(t *testing.T) {
	for _, scenario := range []string{"changed selector", "new member", "member configuration", "member lock", "member protection", "unknown state", "unknown selector", "missing etag", "forbidden PUT", "conditional conflict", "changed PUT response", "changed prepared configuration", "late member", "failed Apply", "incomplete Apply", "tampered phase", "tampered configuration"} {
		t.Run(scenario, func(t *testing.T) {
			h := newFleetMeshCleanupFixture(t)
			props := object(h.resources[h.id]["properties"])
			attach := func() {
				id := fleetParent(h.id, fleetMeshType) + "/members/member2"
				object(h.resources[id]["properties"])["meshProperties"] = object(object(h.resources[h.member]["properties"])["meshProperties"])
			}
			switch scenario {
			case "changed selector":
				object(props["memberSelector"])["byLabel"] = "env=other"
			case "new member":
				attach()
			case "member configuration":
				object(h.resources[h.member]["properties"])["futurePrivateSetting"] = "changed"
			case "member lock":
				h.locks = []any{map[string]any{"id": h.member + "/providers/Microsoft.Authorization/locks/keep", "properties": map[string]any{"level": "CanNotDelete"}}}
			case "member protection":
				h.resources[h.member]["tags"] = map[string]any{"steward:protected": "true"}
			case "unknown state":
				object(props["status"])["state"] = "FutureState"
			case "unknown selector":
				// Preserve a legitimately observed future selector, then ensure
				// cleanup does not discard it while setting an empty byLabel.
				object(props["memberSelector"])["futureSelection"] = "private"
				h.request.Asset = fleetAssetByKind(t, fleetMeshAssets(t, h.fleetFixture), fleetMeshType)
			case "missing etag":
				delete(h.resources[h.id], "eTag")
			case "forbidden PUT", "conditional conflict", "changed PUT response":
				previous := h.override
				h.override = func(req *http.Request) (*http.Response, bool) {
					if req.Method == "PUT" {
						if scenario == "changed PUT response" {
							res, _ := previous(req)
							res.Body.Close()
							object(h.resources[h.id]["properties"])["futurePrivateSetting"] = "changed-by-provider"
							return jsonResponse(200, h.resources[h.id], nil), true
						}
						status := 403
						if scenario == "conditional conflict" {
							status = 412
						}
						h.mutations = append(h.mutations, "PUT")
						return jsonResponse(status, map[string]any{"error": map[string]any{"code": "Denied"}}, nil), true
					}
					return previous(req)
				}
			}
			driver := fleetCleanupDriver(t, h.fleetFixture, h.request)
			result, err := driver.Execute(t.Context(), h.request)
			later := slices.Contains([]string{"changed prepared configuration", "late member", "failed Apply", "incomplete Apply", "tampered phase", "tampered configuration"}, scenario)
			if !later {
				if err == nil || slices.Contains(h.mutations, "POST") || slices.Contains(h.mutations, "DELETE") {
					t.Fatal("unsafe mesh preparation proceeded", result, err, h.mutations)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			result = fleetSerializedResult(t, result, nil)
			switch scenario {
			case "changed prepared configuration":
				object(h.resources[h.id]["properties"])["futurePrivateSetting"] = "changed"
			case "late member":
				attach()
			case "tampered phase", "tampered configuration":
				result.Data = maps.Clone(result.Data)
				if scenario == "tampered phase" {
					result.Data["fleet_phase"] = "delete"
				} else {
					result.Data["fleet_mesh_configuration"] = "forged"
				}
				clear(h.calls)
			case "failed Apply", "incomplete Apply":
				waited, err := driver.Wait(t.Context(), h.request, result)
				if err != nil {
					t.Fatal(err)
				}
				result = fleetSerializedResult(t, result, &waited)
				object(object(h.resources[h.id]["properties"])["status"])["state"] = "Failed"
				if scenario == "incomplete Apply" {
					object(object(h.resources[h.id]["properties"])["status"])["state"] = "Connected"
				}
				object(object(object(h.resources[h.member]["properties"])["meshProperties"])["status"])["state"] = "Failed"
			}
			if _, err := driver.Wait(t.Context(), h.request, result); err == nil || slices.Contains(h.mutations, "DELETE") {
				t.Fatal("unsafe mesh phase completed or deleted the profile", err, h.mutations)
			}
			if strings.HasPrefix(scenario, "tampered") && len(h.calls) != 0 {
				t.Fatal("forged mesh phase reached an API", h.calls)
			}
		})
	}
}
