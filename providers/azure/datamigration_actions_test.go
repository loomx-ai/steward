package azure

import (
	"encoding/json"
	"maps"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func dataMigrationPlan(t *testing.T, f *dataMigrationFixture, values []asset.Asset, selected ...asset.AssetID) plan.Result {
	t.Helper()
	lifecycle, err := f.runtime.ServiceLifecycle(t.Context(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	contribution, err := lifecycle.Contribute(t.Context(), "scope", values)
	if err != nil {
		t.Fatal("native DMS graph", err)
	}
	result, err := plan.Solve(plan.Input{Assets: values, ResolvedAssetIDs: selected, LifecycleBindings: contribution.Bindings, Relationships: contribution.Relationships})
	if err != nil || len(result.Blockers) != 0 {
		t.Fatal("native DMS plan", err, result.Blockers)
	}
	return result
}

func dataMigrationDriver(t *testing.T, f *dataMigrationFixture, request contracts.ActionRequest) contracts.ActionDriver {
	t.Helper()
	driver, err := f.runtime.ResolveAction(t.Context(), "connection", request.Asset)
	if err != nil {
		t.Fatal("resolve native DMS driver", err)
	}
	guard, ok := driver.(*monitorTargetAction)
	if !ok {
		t.Fatal("DMS escaped shared ARM dependency guard")
	}
	if _, ok := guard.inner.(*dataMigrationAction); !ok {
		t.Fatal("DMS escaped native action driver")
	}
	return driver
}

// Compose a complete cleanup flow from native request paths and response
// shapes. The immutable CLI replay tests separately retain original payloads.
func (f *dataMigrationFixture) mutations(t *testing.T) map[string]int {
	t.Helper()
	counts := map[string]int{}
	type pending struct {
		id, phase string
		count     int
		location  bool
	}
	polls := map[string]*pending{}
	previous := f.override
	f.override = func(req *http.Request) (*http.Response, bool) {
		id := strings.ToLower(req.URL.Path)
		if op := polls[req.URL.String()]; op != nil {
			if req.Method != "GET" {
				t.Fatal("native operation was mutated")
			}
			op.count++
			if op.count > 2 {
				t.Fatal("completed operation was replayed")
			}
			if op.count == 2 {
				if op.phase == "delete" {
					delete(f.resources, op.id)
				} else {
					props := object(f.resources[op.id]["properties"])
					props["migrationStatus"], props["provisioningState"] = "Canceled", "Succeeded"
				}
			}
			if op.location {
				status := 202
				if op.count == 2 {
					status = 204
				}
				res := jsonResponse(status, nil, nil)
				res.Body = http.NoBody
				return res, true
			}
			state := "InProgress"
			if op.count == 2 {
				state = "Succeeded"
			}
			return jsonResponse(200, map[string]any{"name": last(req.URL.Path), "status": state}, nil), true
		}
		if req.Method != "DELETE" && !(req.Method == "POST" && (strings.HasSuffix(id, "/cancel") || strings.HasSuffix(id, "/deletenode"))) {
			return previous(req)
		}
		phase, item := "delete", ""
		if strings.HasSuffix(id, "/cancel") {
			id, phase = strings.TrimSuffix(id, "/cancel"), "cancel"
		}
		if strings.HasSuffix(id, "/deletenode") {
			id, phase = strings.TrimSuffix(id, "/deletenode"), "delete-node"
		}
		kind := f.kinds[id]
		if f.resources[id] == nil || dataMigrationKind(kind) == "" || req.URL.Query().Get("api-version") != dataMigrationVersion || req.Header.Get("If-Match") != "" || req.Header.Get("x-ms-client-request-id") == "" {
			t.Fatal("invalid DMS mutation", req.Method, req.URL)
		}
		if phase == "delete-node" {
			body := map[string]any{}
			if json.NewDecoder(req.Body).Decode(&body) != nil || len(body) != 2 || kind != dataMigrationSQLServiceType || body["integrationRuntimeName"] != f.nodes[id]["name"] {
				t.Fatal("invalid deregistration body")
			}
			item = text(body["nodeName"])
			found := false
			f.nodes[id]["nodes"] = slices.DeleteFunc(array(f.nodes[id]["nodes"]), func(value any) bool {
				if object(value)["nodeName"] != item {
					return false
				}
				jobs, err := batchInteger(object(value)["concurrentJobsRunning"], 32)
				if err != nil || jobs != 0 {
					t.Fatal("deregistered node with active work")
				}
				found = true
				return true
			})
			if !found {
				t.Fatal("deregistered unknown node")
			}
			key := phase + "/" + id + "/" + item
			counts[key]++
			if counts[key] != 1 {
				t.Fatal("duplicate deregistration")
			}
			return jsonResponse(200, body, nil), true
		}
		key := phase + "/" + id
		counts[key]++
		if counts[key] != 1 {
			t.Fatal("native mutation replayed", key)
		}
		if phase == "cancel" && kind != dataMigrationType {
			if req.ContentLength > 0 || len(req.URL.Query()) != 1 {
				t.Fatal("classic cancel gained arguments")
			}
			object(f.resources[id]["properties"])["state"] = "Canceled"
			return jsonResponse(200, f.resources[id], nil), true
		}
		_, targetKind := dataMigrationTarget(id)
		if phase == "cancel" {
			body := map[string]any{}
			if json.NewDecoder(req.Body).Decode(&body) != nil || len(body) != 1 || body["migrationOperationId"] != object(f.resources[id]["properties"])["migrationOperationId"] || targetKind == "MongoToCosmosDbMongo" {
				t.Fatal("cancel used another migration operation")
			}
			object(f.resources[id]["properties"])["migrationStatus"] = "Canceling"
		} else {
			if req.ContentLength > 0 {
				t.Fatal("DELETE unexpectedly sent a body")
			}
			if dataMigrationClassic(kind) && kind != dataMigrationFileType && (req.URL.Query().Get("deleteRunningTasks") != "false" || len(req.URL.Query()) != 2) {
				t.Fatal("classic DELETE force-canceled children", req.URL)
			}
			if kind == dataMigrationType {
				force := "false"
				if targetKind == "MongoToCosmosDbMongo" {
					force = "true"
				}
				if req.URL.Query().Get("force") != force || len(req.URL.Query()) != 2 {
					t.Fatal("migration DELETE force policy changed", req.URL)
				}
				if force == "false" && object(f.resources[id]["properties"])["migrationStatus"] != "Canceled" {
					t.Fatal("SQL migration deleted before cancellation completed")
				}
			}
			if kind == dataMigrationSQLServiceType && len(array(f.nodes[id]["nodes"])) != 0 {
				t.Fatal("service deleted before deregistering nodes")
			}
			if dataMigrationClassic(kind) && kind != dataMigrationServiceType {
				delete(f.resources, id)
				return jsonResponse(204, nil, nil), true
			}
		}
		operationID := azureRequestID(key)
		base := "/subscriptions/" + testSubscription + "/providers/Microsoft.DataMigration/locations/japaneast/"
		path, locationOnly := "operationStatuses/"+operationID, false
		if kind == dataMigrationType {
			typ := "drop" + strings.ToLower(targetKind) + "migration"
			if phase == "cancel" {
				typ = "cancel" + strings.ToLower(targetKind) + "migration"
			}
			if targetKind == "MongoToCosmosDbMongo" {
				typ = "dropcosmosdbmongomigration"
			}
			path = "operationTypes/" + typ + "/operationResults/" + operationID
		}
		if kind == dataMigrationSQLServiceType {
			path = "operationTypes/deletesqlmigrationservice/operationResults/" + operationID
		}
		if kind == dataMigrationMongoServiceType {
			path, locationOnly = "migrationServiceOperationResults/"+operationID, true
		}
		endpoint := apiURL(base+path, dataMigrationVersion)
		polls[endpoint] = &pending{id: id, phase: phase, location: locationOnly}
		header := http.Header{}
		if locationOnly {
			header.Set("Location", endpoint)
		} else {
			header.Set("Azure-AsyncOperation", endpoint)
		}
		if kind == dataMigrationServiceType {
			header.Set("Location", apiURL(base+"operationResults/"+operationID, dataMigrationVersion))
		}
		if kind == dataMigrationSQLServiceType {
			header.Set("Location", apiURL(base+"sqlMigrationServiceOperationResults/"+operationID, dataMigrationVersion))
		}
		if phase == "cancel" {
			// The real SQL CLI returns 200 with both an async header and a
			// partial migration resource. Completion comes from later own GETs.
			body := map[string]any{"id": id, "name": last(id), "type": kind, "properties": map[string]any{"scope": object(f.resources[id]["properties"])["scope"], "kind": targetKind, "provisioningState": "Canceling"}}
			return jsonResponse(200, body, header), true
		}
		object(f.resources[id]["properties"])["provisioningState"] = "Deleting"
		status := 202
		if kind == dataMigrationType && targetKind != "MongoToCosmosDbMongo" {
			status = 200
		}
		return jsonResponse(status, nil, header), true
	}
	return counts
}

func TestDataMigrationReviewedCleanupAndPhaseRecovery(t *testing.T) {
	f := newDataMigrationFixture(t)
	f.schemaFileTask()
	for _, kind := range []string{dataMigrationTaskType, dataMigrationServiceTaskType} {
		object(f.resources[f.ids[kind]]["properties"])["state"] = "Running"
	}
	for _, node := range array(f.nodes[f.ids[dataMigrationSQLServiceType]]["nodes"]) {
		object(node)["concurrentJobsRunning"] = 0
	}
	values := f.assets(t)
	selected := []asset.AssetID{}
	for _, value := range values {
		selected = append(selected, value.ID)
	}
	solved := dataMigrationPlan(t, f, values, selected...)
	if len(solved.Steps) != 12 || len(solved.ImpactItems) != 0 {
		t.Fatal("DMS cleanup plan lost native steps")
	}
	beforeTargets := map[string]string{}
	for id, raw := range f.resources {
		if dataMigrationKind(f.kinds[id]) == "" {
			beforeTargets[id] = f.client.privateConfiguration(raw)
		}
	}
	counts := f.mutations(t)
	for _, step := range solved.Steps {
		value := values[slices.IndexFunc(values, func(value asset.Asset) bool { return value.ID == step.AssetID })]
		request := servicePlanRequest(solved, values, value)
		request.IdempotencyKey = "dms-cleanup-" + value.Identity.NativeID
		driver := dataMigrationDriver(t, f, request)
		check, err := driver.Preflight(t.Context(), request)
		if err != nil || !check.Allowed || check.Absent {
			t.Fatal("native preflight", value.Identity.NativeType, check, err)
		}
		result, err := driver.Execute(t.Context(), request)
		if err != nil {
			t.Fatal("native execute", value.Identity.NativeType, err)
		}
		done := false
		for range 16 {
			result = fleetSerializedResult(t, result, nil)
			request.ExecutionResult = &result
			driver = dataMigrationDriver(t, f, request)
			prior := maps.Clone(counts)
			if _, err := driver.Execute(t.Context(), request); err != nil || !maps.Equal(prior, counts) {
				t.Fatal("restart replayed native mutation", err)
			}
			waited, err := driver.Wait(t.Context(), request, result)
			if err != nil {
				t.Fatal("native wait", value.Identity.NativeType, result.Data["phase"], err)
			}
			result = fleetSerializedResult(t, result, &waited)
			if waited.Done {
				done = true
				break
			}
		}
		if !done {
			t.Fatal("native cleanup never completed", value.Identity.NativeType, result.Data["phase"])
		}
		request.ExecutionResult = &result
		read, err := driver.Readback(t.Context(), request)
		if err != nil || read.Exists {
			t.Fatal("native own absence not verified", value.Identity.NativeType, read, err)
		}
	}
	deleted, canceled, nodes := 0, 0, 0
	for key, count := range counts {
		if count != 1 {
			t.Fatal("mutation replayed", key)
		}
		switch {
		case strings.HasPrefix(key, "delete/"):
			deleted++
		case strings.HasPrefix(key, "cancel/"):
			canceled++
		case strings.HasPrefix(key, "delete-node/"):
			nodes++
		}
	}
	if deleted != 12 || canceled != 5 || nodes == 0 {
		t.Fatal("native cleanup phases changed", deleted, canceled, nodes)
	}
	for id, expected := range beforeTargets {
		if f.client.privateConfiguration(f.resources[id]) != expected {
			t.Fatal("independent migration target changed", id)
		}
	}
}

func TestDataMigrationActionRequiresReviewedPrerequisitesAndContext(t *testing.T) {
	for _, mode := range []string{"missing-review", "retained-review", "wrong-controller", "extra-review", "wrong-child-proof", "own-proof", "connection", "partition", "location", "parameters", "action", "private-input", "migration-operation", "target-config", "group-tag", "resource-tag", "resource-managed", "etag", "file-etag", "known-child-live", "known-child-omitted", "new-child", "new-migration", "new-node", "node-config", "forbidden-child"} {
		t.Run(mode, func(t *testing.T) {
			f := newDataMigrationFixture(t)
			f.schemaFileTask()
			kind := dataMigrationSQLServiceType
			if strings.Contains(mode, "child") || mode == "forbidden-child" {
				kind = dataMigrationServiceType
			}
			if mode == "private-input" || mode == "migration-operation" || mode == "target-config" || mode == "etag" {
				kind = dataMigrationType
			}
			if mode == "file-etag" {
				kind = dataMigrationFileType
			}
			values := f.assets(t)
			selected := []asset.AssetID{}
			for _, value := range values {
				selected = append(selected, value.ID)
			}
			solved := dataMigrationPlan(t, f, values, selected...)
			value := cdnAsset(t, values, kind)
			request := servicePlanRequest(solved, values, value)
			request.IdempotencyKey = "guarded-dms"
			id := value.Identity.NativeID
			for _, key := range []string{dataMigrationMembers, "_datamigration_dependents"} {
				for child := range object(value.Normalized[key]) {
					if mode == "known-child-live" || mode == "known-child-omitted" || mode == "forbidden-child" {
						continue
					}
					delete(f.resources, child)
				}
			}
			switch mode {
			case "missing-review":
				request.PrerequisiteDeletions = nil
			case "retained-review":
				request.PrerequisiteDeletions[0].Delete = false
			case "wrong-controller":
				request.PrerequisiteDeletions[0].ControllerID = "other"
			case "extra-review":
				request.PrerequisiteDeletions = append(request.PrerequisiteDeletions, request.PrerequisiteDeletions[0])
			case "wrong-child-proof":
				request.PrerequisiteDeletions[0].Asset.Normalized[dataMigrationProof] = "forged"
			case "own-proof":
				request.Asset.Normalized[dataMigrationProof] = "forged"
			case "connection":
				request.Asset.Identity.ConnectionID = "other"
			case "partition":
				request.Asset.Identity.Partition = "other"
			case "location":
				request.Asset.Location = "other"
			case "parameters":
				request.Parameters = map[string]any{"force": true}
			case "action":
				request.Action = "cancel"
			case "private-input":
				f.resources[id]["futurePrivateSetting"] = "changed"
			case "migration-operation":
				object(f.resources[id]["properties"])["migrationOperationId"] = "99999999-9999-9999-9999-999999999999"
			case "target-config":
				target, _ := dataMigrationTarget(id)
				object(f.resources[target]["properties"])["futurePrivateSetting"] = "changed"
			case "group-tag":
				f.group["tags"] = map[string]any{"steward:protected": "true"}
			case "resource-tag":
				f.resources[id]["tags"] = map[string]any{"steward:protected": "true"}
			case "resource-managed":
				f.resources[id]["managedBy"] = resourceID("Microsoft.Solutions/applications", "controller")
			case "etag", "file-etag":
				f.resources[id]["etag"] = "different-generation"
			case "known-child-omitted":
				for child := range object(value.Normalized[dataMigrationMembers]) {
					f.omitted[child] = true
				}
			case "new-child":
				child := id + "/projects/new"
				raw := dataMigrationBody(t, "Projects_Get")
				raw["id"], raw["name"], raw["type"], raw["location"] = child, "new", dataMigrationProjectType, value.Location
				f.resources[child], f.kinds[child] = raw, dataMigrationProjectType
			case "new-migration":
				child := f.ids["SqlDb"] + "new"
				target, _ := dataMigrationTarget(child)
				raw := dataMigrationBody(t, "DatabaseMigrationsSqlDb_Get")
				raw["id"], raw["name"], raw["type"] = child, last(child), dataMigrationType
				props := object(raw["properties"])
				props["scope"], props["migrationService"] = target, id
				f.resources[child], f.kinds[child] = raw, dataMigrationType
			case "new-node":
				node := batchClone(object(array(f.nodes[id]["nodes"])[0]))
				node["nodeName"] = "new-node"
				f.nodes[id]["nodes"] = append(array(f.nodes[id]["nodes"]), node)
			case "node-config":
				object(array(f.nodes[id]["nodes"])[0])["futurePrivateSetting"] = "changed"
			}
			previous, writes := f.override, 0
			f.override = func(req *http.Request) (*http.Response, bool) {
				if req.Method == "DELETE" || req.Method == "POST" && (strings.HasSuffix(strings.ToLower(req.URL.Path), "/cancel") || strings.HasSuffix(strings.ToLower(req.URL.Path), "/deletenode")) {
					writes++
					return jsonResponse(500, nil, nil), true
				}
				if mode == "forbidden-child" && strings.EqualFold(req.URL.Path, f.ids[dataMigrationTaskType]) {
					return jsonResponse(403, map[string]any{"error": map[string]any{"code": "AuthorizationFailed"}}, nil), true
				}
				return previous(req)
			}
			driver, err := f.runtime.ResolveAction(t.Context(), "connection", request.Asset)
			if err == nil {
				_, err = driver.Execute(t.Context(), request)
			}
			if err == nil || writes != 0 {
				t.Fatal("unreviewed native cleanup reached mutation", mode, err, writes)
			}
		})
	}
}

func TestDataMigrationWaitsForUnattributedNodeWork(t *testing.T) {
	f := newDataMigrationFixture(t)
	for _, node := range array(f.nodes[f.ids[dataMigrationSQLServiceType]]["nodes"]) {
		object(node)["concurrentJobsRunning"] = 1
	}
	values := f.assets(t)
	root := cdnAsset(t, values, dataMigrationSQLServiceType)
	selected := []asset.AssetID{root.ID}
	for _, value := range values {
		if object(root.Normalized["_datamigration_dependents"])[value.Identity.NativeID] != nil {
			selected = append(selected, value.ID)
		}
	}
	solved := dataMigrationPlan(t, f, values, selected...)
	request := servicePlanRequest(solved, values, root)
	request.IdempotencyKey = "native-dms-node-work"
	for _, prior := range request.PrerequisiteDeletions {
		delete(f.resources, prior.Asset.Identity.NativeID)
	}
	counts := f.mutations(t)
	driver := dataMigrationDriver(t, f, request)
	result, err := driver.Execute(t.Context(), request)
	if err != nil || result.Data["phase"] != "wait" || len(counts) != 0 {
		t.Fatal("unknown node work did not wait", result, err)
	}
	for range 2 {
		waited, err := driver.Wait(t.Context(), request, result)
		if err != nil || waited.Done || len(counts) != 0 {
			t.Fatal("node with running jobs was deregistered", waited, err)
		}
		result = fleetSerializedResult(t, result, &waited)
	}
	for _, node := range array(f.nodes[root.Identity.NativeID]["nodes"]) {
		object(node)["concurrentJobsRunning"] = 0
	}
	done := false
	for range 10 {
		request.ExecutionResult = &result
		driver = dataMigrationDriver(t, f, request)
		waited, err := driver.Wait(t.Context(), request, result)
		if err != nil {
			t.Fatal("native drained-node recovery", err)
		}
		result = fleetSerializedResult(t, result, &waited)
		if waited.Done {
			done = true
			break
		}
	}
	if !done || len(counts) < 2 {
		t.Fatal("service did not resume after node work drained", counts)
	}
}

func TestDataMigrationReadbackDoesNotCollapseMissingParents(t *testing.T) {
	for _, mode := range []string{"classic-parent", "classic-project", "modern-parent", "forbidden-residual", "changed-residual", "all-gone"} {
		t.Run(mode, func(t *testing.T) {
			f := newDataMigrationFixture(t)
			values := f.assets(t)
			kind := dataMigrationServiceType
			if mode == "modern-parent" {
				kind = dataMigrationSQLServiceType
			}
			if mode == "classic-project" {
				kind = dataMigrationProjectType
			}
			root := cdnAsset(t, values, kind)
			selected := []asset.AssetID{}
			for _, value := range values {
				selected = append(selected, value.ID)
			}
			solved := dataMigrationPlan(t, f, values, selected...)
			request := servicePlanRequest(solved, values, root)
			delete(f.resources, root.Identity.NativeID)
			if mode == "all-gone" {
				for id := range object(root.Normalized[dataMigrationMembers]) {
					delete(f.resources, id)
				}
			}
			child := f.ids[dataMigrationTaskType]
			if mode == "changed-residual" {
				f.resources[child]["futurePrivateSetting"] = "changed"
			}
			before, previous := f.calls["GET "+child], f.override
			f.override = func(req *http.Request) (*http.Response, bool) {
				if req.Method != "GET" && !strings.HasSuffix(strings.ToLower(req.URL.Path), "/listmonitoringdata") {
					t.Fatal("parent absence triggered a mutation")
				}
				if mode == "forbidden-residual" && strings.EqualFold(req.URL.Path, child) {
					return jsonResponse(403, map[string]any{"error": map[string]any{"code": "AuthorizationFailed"}}, nil), true
				}
				return previous(req)
			}
			driver := dataMigrationDriver(t, f, request)
			read, err := driver.Readback(t.Context(), request)
			if mode == "forbidden-residual" || mode == "changed-residual" {
				if err == nil || isNotFound(err) {
					t.Fatal("unverified child established parent deletion", err)
				}
			} else if err != nil || read.Exists != (mode != "all-gone") {
				t.Fatal("missing parent concealed a surviving resource", mode, read, err)
			}
			if mode != "modern-parent" && f.calls["GET "+child] <= before {
				t.Fatal("descendant own GET skipped")
			}
		})
	}
}

func TestDataMigrationAcceptedReceiptCannotBeChanged(t *testing.T) {
	for _, mode := range []string{"phase", "operation", "origin", "accepted", "touched", "binding", "item", "completion", "asset", "parameters"} {
		t.Run(mode, func(t *testing.T) {
			f := newDataMigrationFixture(t)
			values := f.assets(t)
			value := cdnAsset(t, values, dataMigrationType)
			// cdnAsset returns the first migration; select the SQL DB explicitly.
			for _, candidate := range values {
				if candidate.Identity.NativeID == f.ids["SqlDb"] {
					value = candidate
				}
			}
			solved := dataMigrationPlan(t, f, values, value.ID)
			request := servicePlanRequest(solved, values, value)
			request.IdempotencyKey = "signed-dms-cancel"
			f.mutations(t)
			driver := dataMigrationDriver(t, f, request)
			result, err := driver.Execute(t.Context(), request)
			if err != nil {
				t.Fatal(err)
			}
			result = fleetSerializedResult(t, result, nil)
			switch mode {
			case "phase":
				result.Data["phase"] = "delete"
			case "operation":
				object(result.Data["operation"])["status_url"] = "https://example.invalid/operation"
			case "origin":
				result.ProviderOperationID = "different-origin"
			case "accepted":
				result.Data["accepted"] = map[string]any{}
			case "touched":
				result.Data["touched"] = false
			case "binding":
				result.Data["binding"] = "forged"
			case "item":
				result.Data["item"] = "other"
			case "completion":
				result.Data["operation_done"] = true
			case "asset":
				request.Asset.Normalized[dataMigrationMembers] = map[string]any{"new": true}
			case "parameters":
				request.Parameters = map[string]any{"force": true}
			}
			previous := f.override
			f.override = func(req *http.Request) (*http.Response, bool) {
				if armPathProvider(req.URL.Path) == "microsoft.datamigration" || req.Method != "GET" {
					t.Fatal("forged DMS receipt reached native transport", req.Method, req.URL.Path)
				}
				return previous(req)
			}
			if _, err := driver.Wait(t.Context(), request, result); err == nil {
				t.Fatal("forged native receipt accepted", mode)
			}
			request.ExecutionResult = &result
			if _, err := driver.Execute(t.Context(), request); err == nil {
				t.Fatal("forged receipt replayed", mode)
			}
			if _, err := driver.Readback(t.Context(), request); err == nil {
				t.Fatal("forged receipt proved absence", mode)
			}
		})
	}
}
