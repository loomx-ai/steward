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

func fleetRootCleanupRequest(t *testing.T, h *fleetHubFixture) contracts.ActionRequest {
	t.Helper()
	values := h.graphAssets(t)
	root := fleetAssetByKind(t, values, fleetType)
	request := contracts.ActionRequest{Asset: root, Action: "delete", IdempotencyKey: "fleet-root-cleanup"}
	members := object(object(root.Normalized[fleetHubState])["members"])
	for _, value := range values {
		if members[value.Identity.NativeID] != nil {
			request.LifecycleImpacts = append(request.LifecycleImpacts, contracts.ActionImpact{Asset: value, ControllerID: root.ID, Delete: true})
		}
		if slices.Contains(fleetDirectKinds, value.Identity.NativeType) {
			request.PrerequisiteDeletions = append(request.PrerequisiteDeletions, contracts.ActionImpact{Asset: value, ControllerID: root.ID, Delete: true})
		}
	}
	return request
}

func fleetRootInner(t *testing.T, h *fleetHubFixture, request contracts.ActionRequest) *fleetAction {
	t.Helper()
	c, err := h.runtime.resolve(t.Context(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	kind, _ := findType(fleetType)
	a, err := newFleetAction(c, "connection", request.Asset, kind)
	if err != nil {
		t.Fatal("root driver could not bind verified native ownership", err)
	}
	return a
}

func fleetRootRemovePrerequisites(h *fleetHubFixture) {
	for id := range h.fleetFixture.resources {
		if id != h.fleet {
			delete(h.fleetFixture.resources, id)
		}
	}
}

func TestFleetRootNativeHubCleanup(t *testing.T) {
	for _, mode := range []string{"public", "private", "hubless", "native-descendants"} {
		t.Run(mode, func(t *testing.T) {
			h := newFleetHubFixture(t, mode == "private")
			if mode == "native-descendants" {
				h = newFleetHubMembersFixture(t)
			}
			if mode == "hubless" {
				delete(object(h.fleetFixture.resources[h.fleet]["properties"]), "hubProfile")
				delete(h.groups, h.hub)
				delete(h.groups, h.nodes)
				clear(h.resources)
			}
			request := fleetRootCleanupRequest(t, h)
			a := fleetCleanupDriver(t, h.fleetFixture, request)
			if _, err := a.Preflight(t.Context(), request); err == nil {
				t.Fatal("root deletion bypassed independent native children")
			}
			fleetRootRemovePrerequisites(h)
			fallback := h.override
			deletes := 0
			h.override = func(req *http.Request) (*http.Response, bool) {
				if req.Method == "DELETE" {
					fleetAssertMutation(t, h.fleetFixture, request, req, "delete")
					deletes++
					object(h.fleetFixture.resources[h.fleet]["properties"])["provisioningState"] = "Deleting"
					return jsonResponse(204, nil, nil), true
				}
				return fallback(req)
			}
			check, err := a.Preflight(t.Context(), request)
			if err != nil || !check.Allowed || check.Absent {
				t.Fatal("native verified root preflight failed", check, err)
			}
			result, err := a.Execute(t.Context(), request)
			if err != nil || deletes != 1 || result.Data["fleet_phase"] != "delete" {
				t.Fatal("Fleet did not issue its sole conditional root DELETE", result, deletes, err)
			}
			result = fleetSerializedResult(t, result, nil)
			request.ExecutionResult = &result
			if _, err := fleetCleanupDriver(t, h.fleetFixture, request).Execute(t.Context(), request); err != nil || deletes != 1 {
				t.Fatal("root recovery repeated the native deletion", err, deletes)
			}
			wait, err := a.Wait(t.Context(), request, result)
			if err != nil || wait.Done {
				t.Fatal("root native existence was mistaken for completion", wait, err)
			}
			delete(h.fleetFixture.resources, h.fleet)
			read, err := a.Readback(t.Context(), request)
			if err != nil || read.Exists != (mode != "hubless") {
				t.Fatal("root 404 lost native managed residuals", read, err)
			}
			members := object(object(request.Asset.Normalized[fleetHubState])["members"])
			for id := range members {
				h.gone[id] = true
			}
			clear(h.calls)
			wait, err = fleetCleanupDriver(t, h.fleetFixture, request).Wait(t.Context(), request, result)
			if err != nil || !wait.Done {
				t.Fatal("native residual absence did not complete root cleanup", wait, err)
			}
			for id, value := range members {
				if _, known := findType(text(object(value)["kind"])); known && h.calls["GET "+id] == 0 {
					t.Fatal("known managed resource did not receive its own final GET", id)
				}
			}
			for id := range object(object(request.Asset.Normalized[fleetHubState])["children"]) {
				if h.calls["GET "+id] == 0 {
					t.Fatal("known native child did not receive its own final GET", id)
				}
			}
			data, _ := json.Marshal(result)
			for _, private := range []string{"fleet-private-configuration", "hub-authored-secret", "hub-private-proof", "hub-etag", "unknown-private-secret"} {
				if strings.Contains(string(data), private) {
					t.Fatal("root recovery receipt leaked native configuration", private)
				}
			}
		})
	}
}

func TestFleetRootRejectsChangedReviewBeforeReads(t *testing.T) {
	for _, mode := range []string{"missing-impact", "extra-impact", "wrong-kind", "duplicate-native-id", "duplicate-asset-id", "wrong-controller", "retain", "foreign-connection", "foreign-partition", "missing-hub-proof", "changed-hub-proof", "missing-children", "changed-child-kind", "extra-child", "changed-member", "changed-lifecycle-proof", "parameters"} {
		t.Run(mode, func(t *testing.T) {
			h := newFleetHubMembersFixture(t)
			request := fleetRootCleanupRequest(t, h)
			request.PrerequisiteDeletions = nil
			a := fleetRootInner(t, h, request)
			state := object(request.Asset.Normalized[fleetHubState])
			switch mode {
			case "missing-impact":
				request.LifecycleImpacts = request.LifecycleImpacts[1:]
			case "extra-impact":
				copy := request.LifecycleImpacts[0]
				copy.Asset.ID = "unreviewed"
				copy.Asset.Identity.NativeID += "-other"
				request.LifecycleImpacts = append(request.LifecycleImpacts, copy)
			case "wrong-kind":
				request.LifecycleImpacts[0].Asset.Identity.NativeType = vmType
			case "duplicate-native-id", "duplicate-asset-id":
				copy := request.LifecycleImpacts[0]
				if mode == "duplicate-native-id" {
					copy.Asset.ID = "duplicate"
				}
				request.LifecycleImpacts = append(request.LifecycleImpacts, copy)
			case "wrong-controller":
				request.LifecycleImpacts[0].ControllerID = "other"
			case "retain":
				request.LifecycleImpacts[0].Delete = false
			case "foreign-connection":
				request.LifecycleImpacts[0].Asset.Identity.ConnectionID = "other"
			case "foreign-partition":
				request.LifecycleImpacts[0].Asset.Identity.Partition = "other"
			case "missing-hub-proof":
				delete(request.Asset.Normalized, fleetHubProof)
			case "changed-hub-proof":
				request.Asset.Normalized[fleetHubProof] = "forged"
			case "missing-children":
				delete(state, "children")
			case "changed-child-kind":
				for _, child := range object(state["children"]) {
					object(child)["kind"] = vmType
					break
				}
			case "extra-child":
				object(state["children"])[h.fleet+"/members/unreviewed"] = map[string]any{"kind": fleetMemberType, "configuration": "forged"}
			case "changed-member", "changed-lifecycle-proof":
				member := object(object(state["members"])[h.cluster])
				member[map[string]string{"changed-member": "group", "changed-lifecycle-proof": "lifecycle_configuration"}[mode]] = "forged"
			case "parameters":
				request.Parameters = map[string]any{"force": true}
			}
			clear(h.calls)
			if _, err := a.Execute(t.Context(), request); err == nil || len(h.calls) != 0 {
				t.Fatal("changed root review reached the native API", err, h.calls)
			}
		})
	}
}

func TestFleetRootResidualRequiresEveryNativeResource(t *testing.T) {
	for _, mode := range []string{"child-omitted", "gate-with-missing-run", "cluster-with-missing-group", "external-disk", "unknown-contained", "foreign-id", "asynchronous", "forbidden", "unexpected-operation", "recreated", "authored-change", "operational-change"} {
		t.Run(mode, func(t *testing.T) {
			h := newFleetHubMembersFixture(t)
			request := fleetRootCleanupRequest(t, h)
			request.PrerequisiteDeletions = nil
			a := fleetRootInner(t, h, request)
			native := maps.Clone(h.fleetFixture.resources)
			clear(h.fleetFixture.resources)
			state := object(request.Asset.Normalized[fleetHubState])
			for id := range object(state["members"]) {
				h.gone[id] = true
			}
			id := h.cluster
			switch mode {
			case "child-omitted", "gate-with-missing-run":
				kind := fleetMemberType
				if mode == "gate-with-missing-run" {
					kind = fleetGateType
				}
				for child, raw := range native {
					if raw["type"] == kind {
						h.fleetFixture.resources[child], h.omitted[child] = raw, true
					}
				}
			case "external-disk":
				id = "/subscriptions/" + testSubscription + "/resourcegroups/shared/providers/microsoft.compute/disks/scale-data"
				h.gone[id] = false
			case "unknown-contained":
				id = h.hub
				h.gone[id] = false
			case "cluster-with-missing-group", "operational-change":
				h.gone[id] = false
				if mode == "operational-change" {
					h.resources[id]["etag"] = "delete-operation"
					object(h.resources[id]["properties"])["provisioningState"] = "Deleting"
					h.resources[id]["systemData"] = map[string]any{"lastModifiedAt": "2026-09-11T00:00:00Z", "lastModifiedBy": "native-controller"}
				}
			default:
				fallback := h.override
				h.override = func(req *http.Request) (*http.Response, bool) {
					if strings.EqualFold(req.URL.Path, id) {
						raw := maps.Clone(h.resources[id])
						headers, status := http.Header{}, 200
						switch mode {
						case "foreign-id":
							raw["id"] = resourceID(aksType, "unrelated")
						case "asynchronous":
							status = 202
						case "forbidden":
							status, raw = 403, map[string]any{}
						case "unexpected-operation":
							headers.Set("Azure-AsyncOperation", apiURL(h.fleet+"/operations/unrequested", fleetVersion))
						case "recreated":
							raw["systemData"] = map[string]any{"createdAt": "2026-09-10T00:00:00Z"}
						case "authored-change":
							raw["tags"] = map[string]any{"newOwner": "another"}
						}
						return jsonResponse(status, raw, headers), true
					}
					return fallback(req)
				}
			}
			clear(h.calls)
			read, err := a.Readback(t.Context(), request)
			allowed := slices.Contains([]string{"child-omitted", "gate-with-missing-run", "cluster-with-missing-group", "external-disk", "unknown-contained", "operational-change"}, mode)
			if allowed && (err != nil || !read.Exists) || !allowed && (err == nil || isNotFound(err)) {
				t.Fatal("root residual check inferred unsafe absence", read, err)
			}
			for call := range h.calls {
				if !strings.HasPrefix(call, "GET ") {
					t.Fatal("residual check mutated a native resource", call)
				}
			}
		})
	}
}

func TestFleetRootLiveOwnershipAndProtectionBoundaries(t *testing.T) {
	for _, mode := range []string{"hub-owner", "node-owner", "hub-endpoint", "hub-setting", "hub-etag", "new-member", "missing-member", "missing-cluster", "new-root-child", "omitted-root-child", "orphan-gate", "child-index-404", "child-read-403", "hub-read-403", "external-group-owner", "external-group-protected", "external-group-asynchronous", "protected-root", "protected-hub", "protected-group", "locked-hub", "locked-external", "missing-etag", "etag-conflict", "delete-forbidden", "unknown-hub-field", "second-observation-change"} {
		t.Run(mode, func(t *testing.T) {
			h := newFleetHubMembersFixture(t)
			request := fleetRootCleanupRequest(t, h)
			driver := fleetCleanupDriver(t, h.fleetFixture, request)
			native := maps.Clone(h.fleetFixture.resources)
			fleetRootRemovePrerequisites(h)
			fallback := h.override
			root := h.fleetFixture.resources[h.fleet]
			member := h.fleet + "/members/member1"
			switch mode {
			case "hub-owner":
				h.groups[h.hub]["managedBy"] = resourceID(fleetType, "foreign")
			case "node-owner":
				h.groups[h.nodes]["managedBy"] = resourceID(aksType, "foreign")
			case "hub-endpoint":
				object(h.resources[h.cluster]["properties"])["privateFQDN"] = "foreign.privatelink.westus.azmk8s.io"
			case "hub-setting":
				object(h.resources[h.cluster]["properties"])["futurePrivateSetting"] = "changed"
			case "hub-etag":
				h.resources[h.cluster]["etag"] = "changed-operational-etag"
			case "new-member":
				id := h.hub + "/providers/contoso.example/controllers/extra"
				h.resources[id] = map[string]any{"id": id, "type": "Contoso.Example/controllers", "name": "extra"}
			case "missing-member":
				id := h.hub + "/providers/microsoft.insights/actiongroups/hub-alerts"
				h.gone[id] = true
				h.lists["/subscriptions/"+testSubscription+"/providers/microsoft.insights/actiongroups"] = []any{}
			case "missing-cluster":
				h.gone[h.cluster], h.omit[h.cluster] = true, true
			case "new-root-child":
				id := h.fleet + "/members/extra"
				raw := maps.Clone(native[member])
				raw["id"], raw["name"] = id, "extra"
				h.fleetFixture.resources[id] = raw
			case "omitted-root-child":
				h.fleetFixture.resources[member], h.omitted[member] = native[member], true
			case "orphan-gate":
				for id, raw := range native {
					if raw["type"] == fleetGateType {
						h.fleetFixture.resources[id] = raw
					}
				}
			case "external-group-owner":
				h.groups["/subscriptions/"+testSubscription+"/resourcegroups/shared"]["managedBy"] = resourceID(aksType, "foreign")
			case "external-group-protected":
				h.groups["/subscriptions/"+testSubscription+"/resourcegroups/shared"]["tags"] = map[string]any{"steward/protected": "true"}
			case "protected-root":
				root["tags"] = map[string]any{"steward/protected": "true"}
			case "protected-hub":
				h.resources[h.cluster]["tags"] = map[string]any{"steward/protected": "true"}
			case "protected-group":
				h.groups[h.hub]["tags"] = map[string]any{"steward/protected": "true"}
			case "locked-hub", "locked-external":
				id := h.hub
				if mode == "locked-external" {
					id = "/subscriptions/" + testSubscription + "/resourcegroups/shared"
				}
				h.locks = []any{map[string]any{"id": id + "/providers/microsoft.authorization/locks/protect", "properties": map[string]any{"level": "CanNotDelete"}}}
			case "missing-etag":
				delete(root, "eTag")
			case "unknown-hub-field":
				h.resources[h.hub+"/providers/contoso.example/controllers/custom"]["futureSetting"] = "changed"
			}
			mutations, observations := 0, 0
			h.override = func(req *http.Request) (*http.Response, bool) {
				path := strings.ToLower(req.URL.Path)
				if mode == "external-group-asynchronous" && path == "/subscriptions/"+testSubscription+"/resourcegroups/shared" {
					return jsonResponse(202, h.groups[path], nil), true
				}
				if mode == "second-observation-change" && path == h.hub+"/resources" {
					observations++
					if observations == 3 {
						object(h.resources[h.cluster]["properties"])["futurePrivateSetting"] = "changed-between-observations"
					}
				}
				if mode == "child-index-404" && path == h.fleet+"/autoupgradeprofiles" || mode == "child-read-403" && path == member || mode == "hub-read-403" && path == h.cluster {
					status := 403
					if mode == "child-index-404" {
						status = 404
					}
					return jsonResponse(status, map[string]any{}, nil), true
				}
				if req.Method == "DELETE" {
					mutations++
					fleetAssertMutation(t, h.fleetFixture, request, req, "delete")
					status := 204
					if mode == "etag-conflict" {
						status = 412
					}
					if mode == "delete-forbidden" {
						status = 403
					}
					return jsonResponse(status, nil, nil), true
				}
				return fallback(req)
			}
			_, err := driver.Execute(t.Context(), request)
			wantMutations := 0
			if slices.Contains([]string{"hub-etag", "etag-conflict", "delete-forbidden"}, mode) {
				wantMutations = 1
			}
			if (err == nil) != (mode == "hub-etag") || isNotFound(err) || mutations != wantMutations {
				t.Fatal("root boundary lost native ownership or conditional protection", mode, err, mutations, observations)
			}
		})
	}
}

func TestFleetRootKnownChildrenSurviveInventoryOmission(t *testing.T) {
	h := newFleetHubFixture(t, false)
	request := fleetRootCleanupRequest(t, h)
	state := object(request.Asset.Normalized[fleetHubState])
	if len(object(state["children"])) != 6 {
		t.Fatal("root inventory omitted native prerequisites or Gate")
	}
	for id := range object(state["children"]) {
		h.omitted[id] = true
	}
	scan := h.request(fleetType)
	scan.KnownNativeIDs = []string{h.fleet}
	scan.KnownNativeMetadata = map[string]map[string]any{h.fleet: request.Asset.Normalized}
	batch, err := h.runtime.List(t.Context(), scan)
	if err != nil || len(batch.Items) != 1 || object(batch.Items[0].Normalized[fleetHubState])["children"] == nil {
		t.Fatal("root could not recover its saved native child identities", err)
	}
	c, _ := h.runtime.resolve(t.Context(), "connection")
	if c.privateConfiguration(state) != c.privateConfiguration(object(batch.Items[0].Normalized[fleetHubState])) {
		t.Fatal("LIST omission removed known native child ownership evidence")
	}
	for id := range object(state["children"]) {
		if h.calls["GET "+id] < 2 {
			t.Fatal("omitted native child did not receive independent reads", id)
		}
	}
}

func TestFleetRootManagedMonitorReferences(t *testing.T) {
	for _, location := range []string{"hub", "nodes"} {
		for _, mode := range []string{"internal", "external", "unreviewed-internal"} {
			t.Run(location+"/"+mode, func(t *testing.T) {
				h := newFleetHubFixture(t, false)
				group := h.hub
				if location == "nodes" {
					group = h.nodes
				}
				file := "activity-2026-01-01/ActivityLogAlertRule_Get.json"
				raw := monitorRuleComposedIdentity(t, monitorRuleExample(t, file), file)
				id := group + "/providers/microsoft.insights/activitylogalerts/hub-health"
				raw["id"], raw["name"] = id, "hub-health"
				object(raw["properties"])["scopes"] = []any{h.cluster}
				object(raw["properties"])["actions"] = map[string]any{"actionGroups": []any{}}
				h.resources[id], h.omit[id] = raw, true
				collection := "/subscriptions/" + testSubscription + "/providers/microsoft.insights/activitylogalerts"
				h.lists[collection], h.versions[collection] = []any{raw}, "2026-01-01"
				request := fleetRootCleanupRequest(t, h)
				fleetRootRemovePrerequisites(h)
				if mode != "internal" {
					copy := maps.Clone(raw)
					extra := group + "/providers/microsoft.insights/activitylogalerts/unreviewed"
					if mode == "external" {
						extra = strings.Replace(extra, group, strings.ToLower(text(h.group["id"])), 1)
					}
					copy["id"], copy["name"] = extra, "unreviewed"
					h.resources[extra], h.omit[extra] = copy, true
					h.lists[collection] = append(h.lists[collection], copy)
				}
				mutations := 0
				fallback := h.override
				h.override = func(req *http.Request) (*http.Response, bool) {
					if req.Method == "DELETE" {
						fleetAssertMutation(t, h.fleetFixture, request, req, "delete")
						mutations++
						return jsonResponse(204, nil, nil), true
					}
					return fallback(req)
				}
				_, err := fleetCleanupDriver(t, h.fleetFixture, request).Execute(t.Context(), request)
				if mode == "internal" && (err != nil || mutations != 1) || mode != "internal" && (err == nil || mutations != 0) {
					t.Fatal("root Monitor boundary confused managed ownership with incoming references", mode, err, mutations)
				}
			})
		}
	}
}

func TestFleetRootNativeOperationAndDeletingRecovery(t *testing.T) {
	for _, mode := range []string{"location", "status", "already-deleting"} {
		t.Run(mode, func(t *testing.T) {
			h := newFleetHubFixture(t, false)
			request := fleetRootCleanupRequest(t, h)
			fleetRootRemovePrerequisites(h)
			root := h.fleetFixture.resources[h.fleet]
			if mode == "already-deleting" {
				object(root["properties"])["provisioningState"] = "Deleting"
			}
			operation := "https://management.azure.com/subscriptions/" + testSubscription + "/resourceGroups/test/providers/Microsoft.ContainerService/locations/westus/operationResults/" + rbacTestRoleName + "?api-version=2022-02-01"
			fallback := h.override
			mutations, done := 0, false
			h.override = func(req *http.Request) (*http.Response, bool) {
				if req.URL.String() == operation {
					if req.Method != "GET" {
						t.Fatal("root operation poll mutated state")
					}
					state := "InProgress"
					if done {
						state = "Succeeded"
						if mode == "location" {
							return jsonResponse(204, nil, nil), true
						}
					}
					return jsonResponse(200, map[string]any{"status": state}, nil), true
				}
				if req.Method == "DELETE" {
					fleetAssertMutation(t, h.fleetFixture, request, req, "delete")
					mutations++
					object(root["properties"])["provisioningState"] = "Deleting"
					header := "Location"
					if mode == "status" {
						header = "Azure-AsyncOperation"
					}
					headers := http.Header{}
					headers.Set(header, operation)
					return jsonResponse(202, nil, headers), true
				}
				return fallback(req)
			}
			driver := fleetCleanupDriver(t, h.fleetFixture, request)
			result, err := driver.Execute(t.Context(), request)
			wantMutations := 1
			if mode == "already-deleting" {
				wantMutations = 0
			}
			if err != nil || mutations != wantMutations {
				t.Fatal("root operation did not preserve native deletion state", result, err, mutations)
			}
			result = fleetSerializedResult(t, result, nil)
			for _, tamper := range []string{"member", "prerequisite", "operation"} {
				changed, receipt := request, fleetSerializedResult(t, result, nil)
				if tamper == "member" {
					changed.LifecycleImpacts = nil
				}
				if tamper == "prerequisite" {
					changed.PrerequisiteDeletions = nil
				}
				if tamper == "operation" {
					receipt.Data["fleet_phase_operation"] = operation + "&unreviewed=1"
				}
				clear(h.calls)
				if _, err := driver.Wait(t.Context(), changed, receipt); err == nil || len(h.calls) != 0 {
					t.Fatal("root recovery accepted an altered operation or review", tamper, err, h.calls)
				}
			}
			wait := func(wantDone bool) {
				t.Helper()
				waited, err := fleetCleanupDriver(t, h.fleetFixture, request).Wait(t.Context(), request, result)
				if err != nil || waited.Done != wantDone || mutations != wantMutations {
					t.Fatal("root waiter confused operation status with native absence", waited, err, mutations)
				}
				result = fleetSerializedResult(t, result, &waited)
			}
			wait(false)
			done = true
			wait(false)
			delete(h.fleetFixture.resources, h.fleet)
			wait(false)
			for id := range object(object(request.Asset.Normalized[fleetHubState])["members"]) {
				h.gone[id] = true
			}
			wait(true)
		})
	}
}

func TestFleetRootDiagnosticExtensionsRequirePriorDeletion(t *testing.T) {
	for _, mode := range []string{"root", "hub", "hub-absent"} {
		t.Run(mode, func(t *testing.T) {
			h := newFleetHubFixture(t, false)
			request := fleetRootCleanupRequest(t, h)
			fleetRootRemovePrerequisites(h)
			scope := h.cluster
			if mode == "root" {
				scope = h.fleet
			}
			if mode == "hub-absent" {
				delete(h.fleetFixture.resources, h.fleet)
				for id := range object(object(request.Asset.Normalized[fleetHubState])["members"]) {
					h.gone[id] = true
				}
			}
			id := scope + "/providers/microsoft.insights/diagnosticsettings/export"
			raw := map[string]any{"id": id, "name": "export", "type": diagnosticSettingsType, "properties": map[string]any{"storageAccountId": resourceID(storageType, "sink"), "logs": []any{}, "metrics": []any{}}}
			fallback, gone, mutations := h.override, false, 0
			h.override = func(req *http.Request) (*http.Response, bool) {
				path := strings.ToLower(req.URL.Path)
				if path == strings.TrimSuffix(id, "/export") {
					rows := []any{}
					if !gone {
						rows = append(rows, raw)
					}
					return jsonResponse(200, map[string]any{"value": rows}, nil), true
				}
				if path == id {
					if gone {
						return jsonResponse(404, nil, nil), true
					}
					return jsonResponse(200, raw, nil), true
				}
				if req.Method == "DELETE" {
					mutations++
					return jsonResponse(204, nil, nil), true
				}
				return fallback(req)
			}
			driver := fleetCleanupDriver(t, h.fleetFixture, request)
			if _, err := driver.Execute(t.Context(), request); err == nil || !strings.Contains(err.Error(), "monitor_target_has_incoming_references") || mutations != 0 {
				t.Fatal("Fleet silently cascaded a live diagnostic extension", err, mutations)
			}
			gone = true
			check, err := driver.Preflight(t.Context(), request)
			if err != nil || !check.Allowed || check.Absent != (mode == "hub-absent") {
				t.Fatal("removed diagnostic extension still blocked root cleanup", check, err)
			}
		})
	}
}
