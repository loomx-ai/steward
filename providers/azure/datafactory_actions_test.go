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

func dataFactoryPlan(t *testing.T, f *dataFactoryFixture, values []asset.Asset, targets ...asset.AssetID) plan.Result {
	t.Helper()
	lifecycle, err := f.runtime.ServiceLifecycle(t.Context(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	contribution, err := lifecycle.Contribute(t.Context(), "scope", values)
	if err != nil {
		t.Fatal("Data Factory native graph", err)
	}
	result, err := plan.Solve(plan.Input{Assets: values, ResolvedAssetIDs: targets, LifecycleBindings: contribution.Bindings, Relationships: contribution.Relationships})
	if err != nil || len(result.Blockers) != 0 {
		t.Fatal("Data Factory native plan", err, result.Blockers)
	}
	return result
}

func dataFactoryDriver(t *testing.T, f *dataFactoryFixture, request contracts.ActionRequest) contracts.ActionDriver {
	t.Helper()
	driver, err := f.runtime.ResolveAction(t.Context(), "connection", request.Asset)
	if err != nil {
		t.Fatal("resolve Data Factory driver", err)
	}
	inner := driver
	if guard, ok := driver.(*monitorTargetAction); ok {
		inner = guard.inner
	} else if request.Asset.Identity.NativeType != dataFactoryNodeType {
		t.Fatal("ARM driver escaped shared dependency guard")
	}
	if _, ok := inner.(*dataFactoryAction); !ok {
		t.Fatal("Data Factory escaped its native action driver")
	}
	return driver
}

func (f *dataFactoryFixture) deleteResource(id string) {
	delete(f.resources, id)
	for owner, status := range f.statuses {
		props := object(object(status["properties"])["typeProperties"])
		if nodes, ok := props["nodes"].([]any); ok {
			props["nodes"] = slices.DeleteFunc(nodes, func(raw any) bool { return owner+"/nodes/"+strings.ToLower(text(object(raw)["nodeName"])) == id })
		}
	}
}

func TestDataFactoryReviewedFactoryDeletionAndResidualRecovery(t *testing.T) {
	f := newDataFactoryFixture(t)
	values := f.assets(t)
	root := cdnAsset(t, values, dataFactoryType)
	solved := dataFactoryPlan(t, f, values, root.ID)
	original := f.override
	deletions := map[string]int{}
	f.override = func(req *http.Request) (*http.Response, bool) {
		id := strings.ToLower(req.URL.Path)
		if req.Method == "DELETE" {
			if req.URL.Query().Get("api-version") != dataFactoryVersion || len(req.URL.Query()) != 1 || req.Header.Get("If-Match") != "" || req.ContentLength > 0 || f.resources[id] == nil || deletions[id] != 0 {
				t.Fatal("invalid or duplicate native deletion", req.Method, req.URL)
			}
			deletions[id]++
			f.deleteResource(id)
			return jsonResponse(200, nil, nil), true
		}
		return original(req)
	}
	for _, step := range solved.Steps {
		value := values[slices.IndexFunc(values, func(v asset.Asset) bool { return v.ID == step.AssetID })]
		request := servicePlanRequest(solved, values, value)
		request.IdempotencyKey = "datafactory-delete-" + value.Identity.NativeID
		driver := dataFactoryDriver(t, f, request)
		check, err := driver.Preflight(t.Context(), request)
		if err != nil || !check.Allowed {
			t.Fatal("native preflight", value.Identity.NativeType, check, err)
		}
		result, err := driver.Execute(t.Context(), request)
		if err != nil {
			t.Fatal("native execute", value.Identity.NativeType, err)
		}
		waited, err := driver.Wait(t.Context(), request, result)
		if err != nil {
			t.Fatal("native wait", value.Identity.NativeType, err)
		}
		if value.ID == root.ID {
			if waited.Done {
				t.Fatal("factory 404 concealed surviving children")
			}
			for _, id := range slices.Sorted(maps.Keys(f.resources)) {
				f.deleteResource(id)
			}
		} else if !waited.Done {
			t.Fatal("absent direct prerequisite still pending", waited)
		}
		result = fleetSerializedResult(t, result, &waited)
		request.ExecutionResult = &result
		driver = dataFactoryDriver(t, f, request)
		resumed, err := driver.Execute(t.Context(), request)
		if err != nil || resumed.ProviderOperationID != result.ProviderOperationID || deletions[value.Identity.NativeID] != 1 {
			t.Fatal("recovery repeated deletion", err, deletions)
		}
		waited, err = driver.Wait(t.Context(), request, result)
		if err != nil || !waited.Done {
			t.Fatal("recovered native absence", value.Identity.NativeType, err, waited)
		}
	}
	if len(deletions) != 3 {
		t.Fatal("factory artifacts did not use the native cascade", deletions)
	}
}

func TestDataFactoryRegisteredIndependentDeletion(t *testing.T) {
	for _, kind := range append(dataFactoryChildKinds(dataFactoryType), dataFactoryNodeType, dataFactoryEndpointType) {
		if kind == dataFactoryNetworkType {
			continue
		}
		t.Run(last(kind), func(t *testing.T) {
			f := newDataFactoryFixture(t)
			values := f.assets(t)
			value := cdnAsset(t, values, kind)
			targets := []asset.AssetID{value.ID}
			for i := 0; i < len(targets); i++ {
				current := values[slices.IndexFunc(values, func(v asset.Asset) bool { return v.ID == targets[i] })]
				for source := range object(object(current.Normalized["_datafactory_incoming"])[current.Identity.NativeID]) {
					if !slices.Contains(targets, asset.AssetID(source)) {
						targets = append(targets, asset.AssetID(source))
					}
				}
			}
			solved := dataFactoryPlan(t, f, values, targets...)
			request := servicePlanRequest(solved, values, value)
			request.IdempotencyKey = "independent-datafactory"
			for _, prior := range request.PrerequisiteDeletions {
				f.deleteResource(prior.Asset.Identity.NativeID)
			}
			original := f.override
			deletes := 0
			f.override = func(req *http.Request) (*http.Response, bool) {
				if req.Method == "DELETE" {
					if strings.ToLower(req.URL.Path) != value.Identity.NativeID || req.Header.Get("If-Match") != "" || req.Header.Get("x-ms-client-request-id") != azureRequestID(request.IdempotencyKey+":delete:"+value.Identity.NativeID+":") {
						t.Fatal("wrong native independent DELETE", req.Method, req.URL, req.Header)
					}
					if kind == dataFactoryNodeType && last(req.URL.Path) != "Node_1" {
						t.Fatal("native node spelling changed")
					}
					deletes++
					f.deleteResource(value.Identity.NativeID)
					return jsonResponse(204, nil, nil), true
				}
				return original(req)
			}
			driver := dataFactoryDriver(t, f, request)
			result, err := driver.Execute(t.Context(), request)
			if err != nil {
				t.Fatal("independent native execute", err)
			}
			waited, err := driver.Wait(t.Context(), request, result)
			if err != nil {
				t.Fatal("independent native wait", err)
			}
			if len(request.LifecycleImpacts) != 0 {
				if waited.Done {
					t.Fatal("runtime 404 concealed a registered node")
				}
				for _, impact := range request.LifecycleImpacts {
					f.deleteResource(impact.Asset.Identity.NativeID)
				}
				result = fleetSerializedResult(t, result, &waited)
				waited, err = driver.Wait(t.Context(), request, result)
			}
			if err != nil || !waited.Done || deletes != 1 || f.resources[f.ids[dataFactoryType]] == nil {
				t.Fatal("independent delete escaped scope", err, waited, deletes)
			}
		})
	}
}

func TestDataFactoryTriggerAndCDCPreparationRecovery(t *testing.T) {
	for _, kind := range []string{dataFactoryTriggerType, dataFactoryCDCType} {
		t.Run(last(kind), func(t *testing.T) {
			f := newDataFactoryFixture(t)
			id := f.ids[kind]
			state := "Running"
			if kind == dataFactoryTriggerType {
				state = "Started"
				object(f.resources[id]["properties"])["runtimeState"] = state
			}
			f.override = func(req *http.Request) (*http.Response, bool) {
				if req.Method == "GET" && strings.ToLower(req.URL.Path) == id+"/status" {
					return jsonResponse(200, state, nil), true
				}
				return nil, false
			}
			values := f.assets(t)
			value := cdnAsset(t, values, kind)
			solved := dataFactoryPlan(t, f, values, value.ID)
			request := servicePlanRequest(solved, values, value)
			request.IdempotencyKey = "datafactory-stop"
			original := f.override
			stops, deletes := 0, 0
			f.override = func(req *http.Request) (*http.Response, bool) {
				path := strings.ToLower(req.URL.Path)
				if req.Method == "POST" && path == id+"/stop" {
					stops++
					return jsonResponse(200, nil, nil), true
				}
				if req.Method == "DELETE" {
					deletes++
					if state != "Stopped" {
						t.Fatal("delete before native Stopped")
					}
					f.deleteResource(id)
					return jsonResponse(200, nil, nil), true
				}
				return original(req)
			}
			driver := dataFactoryDriver(t, f, request)
			result, err := driver.Execute(t.Context(), request)
			if err != nil || stops != 1 || deletes != 0 {
				t.Fatal("native stop preparation", err, stops, deletes)
			}
			for i := 0; i < 2; i++ {
				result = fleetSerializedResult(t, result, nil)
				request.ExecutionResult = &result
				driver = dataFactoryDriver(t, f, request)
				if _, err := driver.Execute(t.Context(), request); err != nil {
					t.Fatal("restore stop receipt", err)
				}
				waited, err := driver.Wait(t.Context(), request, result)
				if err != nil || waited.Done || stops != 1 || deletes != 0 {
					t.Fatal("accepted Stop repeated or became deletion", err, waited, stops, deletes)
				}
				result = fleetSerializedResult(t, result, &waited)
			}
			state = "Stopped"
			if kind == dataFactoryTriggerType {
				object(f.resources[id]["properties"])["runtimeState"] = state
			}
			waited, err := driver.Wait(t.Context(), request, result)
			if err != nil || waited.Done || deletes != 1 {
				t.Fatal("native stop did not advance", err, waited, deletes)
			}
			result = fleetSerializedResult(t, result, &waited)
			waited, err = driver.Wait(t.Context(), request, result)
			if err != nil || !waited.Done || stops != 1 || deletes != 1 {
				t.Fatal("prepared delete not complete", err, waited)
			}
		})
	}
}

func dataFactoryCloneRequest(t *testing.T, request contracts.ActionRequest) contracts.ActionRequest {
	t.Helper()
	payload, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	var result contracts.ActionRequest
	if err := json.Unmarshal(payload, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestDataFactorySSISRecordedStopRecoversBeforeNativeStopped(t *testing.T) {
	f := newDataFactoryFixture(t)
	id := f.ids[dataFactoryIRType]
	props := object(f.resources[id]["properties"])
	props["type"], props["typeProperties"] = "Managed", map[string]any{"ssisProperties": map[string]any{"edition": "Standard"}}
	object(f.statuses[id]["properties"])["type"], object(f.statuses[id]["properties"])["state"] = "Managed", "Started"
	f.deleteResource(f.ids[dataFactoryNodeType])
	values := f.assets(t)
	value := cdnAsset(t, values, dataFactoryIRType)
	solved := dataFactoryPlan(t, f, values, value.ID)
	request := servicePlanRequest(solved, values, value)
	rows := map[int]dataFactoryRecording{}
	for _, row := range dataFactoryRecordings(t) {
		if row.Index >= 69 && row.Index <= 81 && strings.HasSuffix(row.SourceURI, "main.yaml") {
			// Keep original bodies; compose only the fixture's owner scope.
			for key, headers := range row.Headers {
				for i, value := range headers {
					if strings.HasPrefix(value, "https://management.azure.com/") {
						headers[i] = apiURL(id+value[strings.Index(value, "/stop/"):strings.Index(value, "?")], dataFactoryVersion)
					}
				}
				row.Headers[key] = headers
			}
			rows[row.Index] = row
		}
	}
	original := f.override
	stops, deletes, polls := 0, 0, 0
	f.override = func(req *http.Request) (*http.Response, bool) {
		path := strings.ToLower(req.URL.Path)
		if req.Method == "POST" && path == id+"/stop" {
			stops++
			object(f.statuses[id]["properties"])["state"] = "Stopping"
			return rows[69].response(), true
		}
		if req.Method == "GET" && strings.Contains(path, "/stop/operation") {
			polls++
			if strings.Contains(path, "operationresults") {
				return rows[81].response(), true
			}
			if polls == 1 {
				return rows[70].response(), true
			}
			return rows[80].response(), true
		}
		if req.Method == "DELETE" {
			deletes++
			if object(f.statuses[id]["properties"])["state"] != "Stopped" {
				t.Fatal("SSIS deleted before own Stopped state")
			}
			f.deleteResource(id)
			return jsonResponse(200, nil, nil), true
		}
		return original(req)
	}
	driver := dataFactoryDriver(t, f, request)
	result, err := driver.Execute(t.Context(), request)
	if err != nil || stops != 1 || result.ProviderOperationID == "" {
		t.Fatal("native Stop receipt", err, result)
	}
	origin := result.ProviderOperationID
	for i := 0; i < 3; i++ {
		result = fleetSerializedResult(t, result, nil)
		request.ExecutionResult = &result
		driver = dataFactoryDriver(t, f, request)
		if _, err := driver.Execute(t.Context(), request); err != nil {
			t.Fatal("restore SSIS Stop", err)
		}
		waited, err := driver.Wait(t.Context(), request, result)
		if err != nil || waited.Done || stops != 1 || deletes != 0 {
			t.Fatal("SSIS Stop repeated or status completion proved deletion", err, waited, stops, deletes)
		}
		result = fleetSerializedResult(t, result, &waited)
	}
	if polls != 3 {
		t.Fatal("completed operation was polled again while runtime was Stopping", polls)
	}
	object(f.statuses[id]["properties"])["state"] = "Stopped"
	waited, err := driver.Wait(t.Context(), request, result)
	if err != nil || waited.Done || deletes != 1 {
		t.Fatal("Stopped SSIS did not advance", err, waited, deletes)
	}
	result = fleetSerializedResult(t, result, &waited)
	if result.ProviderOperationID != origin {
		t.Fatal("worker origin changed during phase transition")
	}
	waited, err = driver.Wait(t.Context(), request, result)
	if err != nil || !waited.Done || polls != 3 || stops != 1 {
		t.Fatal("SSIS final absence", err, waited, polls, stops)
	}
}

func TestDataFactoryEventUnsubscribeUsesNativePOSTStatus(t *testing.T) {
	f := newDataFactoryFixture(t)
	id := f.ids[dataFactoryTriggerType]
	props := object(f.resources[id]["properties"])
	props["type"], props["runtimeState"], props["typeProperties"] = "BlobEventsTrigger", "Started", map[string]any{"events": []any{"Microsoft.Storage.BlobCreated"}, "scope": resourceID(storageType, "events")}
	state := "Enabled"
	f.override = func(req *http.Request) (*http.Response, bool) {
		if strings.EqualFold(req.URL.Path, id+"/geteventsubscriptionstatus") {
			if req.Method != "POST" {
				t.Fatal("invented GET for native event subscription status")
			}
			return jsonResponse(200, map[string]any{"triggerName": last(id), "status": state}, nil), true
		}
		return nil, false
	}
	values := f.assets(t)
	value := cdnAsset(t, values, dataFactoryTriggerType)
	solved := dataFactoryPlan(t, f, values, value.ID)
	request := servicePlanRequest(solved, values, value)
	original := f.override
	stops, unsubscribes, deletes := 0, 0, 0
	f.override = func(req *http.Request) (*http.Response, bool) {
		path := strings.ToLower(req.URL.Path)
		if req.Method == "POST" && path == id+"/stop" {
			stops++
			props["runtimeState"] = "Stopped"
			return jsonResponse(200, nil, nil), true
		}
		if req.Method == "POST" && path == id+"/unsubscribefromevents" {
			unsubscribes++
			state = "Deprovisioning"
			return jsonResponse(202, nil, http.Header{"Location": {apiURL(id+"/getEventSubscriptionStatus", dataFactoryVersion)}}), true
		}
		if req.Method == "DELETE" {
			deletes++
			if state != "Disabled" {
				t.Fatal("event trigger deleted before disabled subscription")
			}
			f.deleteResource(id)
			return jsonResponse(200, nil, nil), true
		}
		return original(req)
	}
	driver := dataFactoryDriver(t, f, request)
	result, err := driver.Execute(t.Context(), request)
	if err != nil || stops != 1 {
		t.Fatal("native event trigger stop", err)
	}
	waited, err := driver.Wait(t.Context(), request, result)
	if err != nil || waited.Done || unsubscribes != 1 || deletes != 0 {
		t.Fatal("native event unsubscribe", err, waited, unsubscribes)
	}
	result = fleetSerializedResult(t, result, &waited)
	request.ExecutionResult = &result
	driver = dataFactoryDriver(t, f, request)
	if _, err := driver.Execute(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	waited, err = driver.Wait(t.Context(), request, result)
	if err != nil || waited.Done || unsubscribes != 1 || deletes != 0 {
		t.Fatal("event unsubscribe replayed or completed early", err, waited)
	}
	result = fleetSerializedResult(t, result, &waited)
	state = "Disabled"
	waited, err = driver.Wait(t.Context(), request, result)
	if err != nil || deletes != 1 {
		t.Fatal("disabled subscription did not permit native delete", err, waited)
	}
	result = fleetSerializedResult(t, result, &waited)
	waited, err = driver.Wait(t.Context(), request, result)
	if err != nil || !waited.Done || unsubscribes != 1 || stops != 1 {
		t.Fatal("event cleanup did not finish", err, waited)
	}
}

func TestDataFactoryRunCancelAndDebugDeletePersistIndividualWork(t *testing.T) {
	for _, kind := range []string{dataFactoryType, dataFactoryPipelineType} {
		t.Run(last(kind), func(t *testing.T) {
			f := newDataFactoryFixture(t)
			run, debug := dataFactoryWorkFixture(t, f)
			root := f.ids[dataFactoryType]
			debugActive, omitRun := true, false
			f.override = func(req *http.Request) (*http.Response, bool) {
				path := strings.ToLower(req.URL.Path)
				switch path {
				case root + "/querypipelineruns":
					rows := []any{run}
					if omitRun {
						rows = []any{}
					}
					return jsonResponse(200, map[string]any{"value": rows}, nil), true
				case root + "/pipelineruns/" + text(run["runId"]):
					return jsonResponse(200, run, nil), true
				case root + "/querydataflowdebugsessions":
					rows := []any{}
					if debugActive {
						rows = append(rows, debug)
					}
					return jsonResponse(200, map[string]any{"value": rows}, nil), true
				}
				return nil, false
			}
			values := f.assets(t)
			value := cdnAsset(t, values, kind)
			targets := []asset.AssetID{value.ID}
			if kind == dataFactoryPipelineType {
				targets = append(targets, cdnAsset(t, values, dataFactoryTriggerType).ID)
			}
			solved := dataFactoryPlan(t, f, values, targets...)
			request := servicePlanRequest(solved, values, value)
			for _, prior := range request.PrerequisiteDeletions {
				f.deleteResource(prior.Asset.Identity.NativeID)
			}
			original := f.override
			cancels, debugDeletes, deletes := 0, 0, 0
			f.override = func(req *http.Request) (*http.Response, bool) {
				path := strings.ToLower(req.URL.Path)
				if req.Method == "POST" && path == root+"/pipelineruns/"+text(run["runId"])+"/cancel" {
					if req.URL.Query().Get("isRecursive") != "false" || len(req.URL.Query()) != 2 || req.ContentLength > 0 {
						t.Fatal("cancel escaped the individual reviewed run", req.URL)
					}
					cancels++
					run["status"] = "Canceling"
					omitRun = true
					return jsonResponse(200, "", nil), true // Unchanged native CLI response shape.
				}
				if req.Method == "POST" && path == root+"/deletedataflowdebugsession" {
					var body map[string]any
					if json.NewDecoder(req.Body).Decode(&body) != nil || len(body) != 1 || body["sessionId"] != debug["sessionId"] || kind != dataFactoryType {
						t.Fatal("debug delete lost its reviewed session scope", body)
					}
					debugDeletes++
					return jsonResponse(200, nil, nil), true
				}
				if req.Method == "DELETE" {
					deletes++
					if dataFactoryRunActive(run) || kind == dataFactoryType && debugActive {
						t.Fatal("deletion bypassed active reviewed work")
					}
					for _, id := range slices.Sorted(maps.Keys(f.resources)) {
						if id == value.Identity.NativeID || strings.HasPrefix(id, value.Identity.NativeID+"/") {
							f.deleteResource(id)
						}
					}
					return jsonResponse(200, nil, nil), true
				}
				return original(req)
			}
			driver := dataFactoryDriver(t, f, request)
			result, err := driver.Execute(t.Context(), request)
			if err != nil || cancels != 1 || deletes != 0 || debugDeletes != 0 {
				t.Fatal("native individual cancellation", err, cancels, debugDeletes, deletes)
			}
			for range 2 {
				result = fleetSerializedResult(t, result, nil)
				request.ExecutionResult = &result
				driver = dataFactoryDriver(t, f, request)
				if _, err := driver.Execute(t.Context(), request); err != nil {
					t.Fatal(err)
				}
				waited, err := driver.Wait(t.Context(), request, result)
				if err != nil || waited.Done || cancels != 1 || deletes != 0 || debugDeletes != 0 {
					t.Fatal("omitted Canceling run disappeared or cancellation replayed", err, waited)
				}
				result = fleetSerializedResult(t, result, &waited)
			}
			run["status"], run["runEnd"] = "Cancelled", "2026-09-01T00:05:00Z"
			waited, err := driver.Wait(t.Context(), request, result)
			if err != nil {
				t.Fatal("terminal named run", err)
			}
			result = fleetSerializedResult(t, result, &waited)
			if kind == dataFactoryType {
				if debugDeletes != 1 || deletes != 0 {
					t.Fatal("factory did not delete reviewed debug session", debugDeletes, deletes)
				}
				waited, err = driver.Wait(t.Context(), request, result)
				if err != nil || waited.Done || debugDeletes != 1 || deletes != 0 {
					t.Fatal("debug deletion replayed before complete query absence", err, waited)
				}
				result = fleetSerializedResult(t, result, &waited)
				debugActive = false
				waited, err = driver.Wait(t.Context(), request, result)
				if err != nil || deletes != 1 {
					t.Fatal("absent debug session did not advance", err, waited)
				}
				result = fleetSerializedResult(t, result, &waited)
			} else if debugDeletes != 0 || deletes != 1 {
				t.Fatal("pipeline cleanup changed an independent debug session", debugDeletes, deletes)
			}
			waited, err = driver.Wait(t.Context(), request, result)
			if err != nil || !waited.Done || cancels != 1 || deletes != 1 {
				t.Fatal("work cleanup did not finish", err, waited)
			}
			encoded, _ := json.Marshal(result)
			if strings.Contains(string(encoded), "sensitive-") {
				t.Fatal("private work entered the execution receipt")
			}
		})
	}
}

func TestDataFactorySharedRuntimeRemoveLinksRequiresWholeFactoryGroup(t *testing.T) {
	for _, kind := range []string{dataFactoryType, dataFactoryIRType} {
		for _, mode := range []string{"reviewed", "consumer-factory-absent", "consumer-still-live", "consumer-list-omitted", "new-consumer", "changed-link", "unresolved-link", "reappeared-link"} {
			t.Run(last(kind)+"/"+mode, func(t *testing.T) {
				f := newDataFactoryFixture(t)
				consumerRoot, first := dataFactorySharingFixture(t, f, "Key")
				second := consumerRoot + "/integrationruntimes/second-runtime"
				f.resources[second] = batchClone(f.resources[first])
				f.resources[second]["id"], f.resources[second]["name"], f.kinds[second] = second, last(second), dataFactoryIRType
				f.statuses[second] = batchClone(f.statuses[first])
				f.statuses[second]["name"] = last(second)
				owner := f.ids[dataFactoryIRType]
				props := object(object(f.statuses[owner]["properties"])["typeProperties"])
				link := batchClone(object(array(props["links"])[0]))
				link["name"] = last(second)
				props["links"] = append(array(props["links"]), link)
				values := f.assets(t)
				value := cdnAsset(t, values, kind)
				if value.Identity.NativeID != f.ids[kind] {
					value = values[slices.IndexFunc(values, func(v asset.Asset) bool { return v.Identity.NativeID == f.ids[kind] })]
				}
				solved := dataFactoryPlan(t, f, values, value.ID, asset.AssetID(first), asset.AssetID(second))
				request := servicePlanRequest(solved, values, value)
				for _, prior := range request.PrerequisiteDeletions {
					if prior.Asset.Identity.NativeID != second || mode != "consumer-still-live" && mode != "consumer-list-omitted" {
						f.deleteResource(prior.Asset.Identity.NativeID)
					}
				}
				if mode == "consumer-list-omitted" {
					f.omitted[second] = true
					props["links"] = array(props["links"])[:1]
				}
				if mode == "consumer-factory-absent" {
					f.deleteResource(consumerRoot)
				}
				if mode == "new-consumer" {
					extra := batchClone(link)
					extra["name"] = "unreviewed"
					props["links"] = append(array(props["links"]), extra)
				}
				if mode == "changed-link" {
					object(array(props["links"])[0])["createTime"] = "2026-09-01T00:00:00Z"
				}
				if mode == "unresolved-link" {
					object(array(props["links"])[0])["subscriptionId"] = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
				}
				original := f.override
				removes, deletes := 0, 0
				f.override = func(req *http.Request) (*http.Response, bool) {
					if req.Method == "POST" && strings.EqualFold(req.URL.Path, owner+"/removelinks") {
						var body map[string]any
						if json.NewDecoder(req.Body).Decode(&body) != nil || len(body) != 1 || body["factoryName"] != last(consumerRoot) || f.resources[first] != nil || f.resources[second] != nil {
							t.Fatal("RemoveLinks escaped the fully reviewed consumer factory", body)
						}
						removes++
						return jsonResponse(200, nil, nil), true
					}
					if req.Method == "DELETE" {
						if len(array(props["links"])) != 0 {
							t.Fatal("DELETE preceded native link removal")
						}
						deletes++
						for _, id := range slices.Sorted(maps.Keys(f.resources)) {
							if id == value.Identity.NativeID || strings.HasPrefix(id, value.Identity.NativeID+"/") {
								f.deleteResource(id)
							}
						}
						return jsonResponse(200, nil, nil), true
					}
					return original(req)
				}
				driver := dataFactoryDriver(t, f, request)
				result, err := driver.Execute(t.Context(), request)
				if mode != "reviewed" && mode != "consumer-factory-absent" && mode != "reappeared-link" {
					if err == nil || removes != 0 || deletes != 0 {
						t.Fatal("unreviewed sharing accepted", err, removes, deletes)
					}
					return
				}
				if err != nil || removes != 1 || deletes != 0 {
					t.Fatal("native RemoveLinks preparation", err, removes, deletes)
				}
				result = fleetSerializedResult(t, result, nil)
				request.ExecutionResult = &result
				driver = dataFactoryDriver(t, f, request)
				if _, err := driver.Execute(t.Context(), request); err != nil {
					t.Fatal("restore RemoveLinks", err)
				}
				waited, err := driver.Wait(t.Context(), request, result)
				if err != nil || waited.Done || removes != 1 || deletes != 0 {
					t.Fatal("RemoveLinks replayed or completed early", err, waited)
				}
				result = fleetSerializedResult(t, result, &waited)
				if mode == "reappeared-link" {
					object(array(props["links"])[0])["createTime"] = "2026-09-01T00:00:00Z"
				} else {
					props["links"] = []any{}
				}
				waited, err = driver.Wait(t.Context(), request, result)
				if mode == "reappeared-link" {
					if err == nil || removes != 1 || deletes != 0 {
						t.Fatal("changed sharing during restore accepted", err)
					}
					return
				}
				if err != nil || deletes != 1 || removes != 1 {
					t.Fatal("native links absence did not advance", err, waited)
				}
				result = fleetSerializedResult(t, result, &waited)
				waited, err = driver.Wait(t.Context(), request, result)
				if err != nil || !waited.Done || (f.resources[consumerRoot] == nil) != (mode == "consumer-factory-absent") {
					t.Fatal("shared host cleanup escaped its selected scope", err, waited)
				}
			})
		}
	}
}

func TestDataFactoryActionBoundaryRejectsChangedReviewAndNativeContext(t *testing.T) {
	for _, mode := range []string{"parameters", "action", "asset-id", "connection", "location", "partition", "binding", "configuration", "members", "missing-impact", "duplicate-impact", "retained-impact", "impact-controller", "impact-connection", "impact-binding", "missing-prerequisite", "duplicate-prerequisite", "prerequisite-controller", "prerequisite-live", "omitted-prerequisite", "changed-root", "changed-child", "changed-node", "new-child", "new-lock", "group-tag", "group-changed", "root-etag", "child-etag", "status-403", "status-404", "child-403", "group-404", "new-run", "new-debug", "late-child", "reappeared-prerequisite"} {
		t.Run(mode, func(t *testing.T) {
			f := newDataFactoryFixture(t)
			values := f.assets(t)
			value := cdnAsset(t, values, dataFactoryType)
			solved := dataFactoryPlan(t, f, values, value.ID)
			request := dataFactoryCloneRequest(t, servicePlanRequest(solved, values, value))
			driver := dataFactoryDriver(t, f, request)
			trigger := batchClone(f.resources[f.ids[dataFactoryTriggerType]])
			for _, prior := range request.PrerequisiteDeletions {
				if mode != "prerequisite-live" && mode != "omitted-prerequisite" {
					f.deleteResource(prior.Asset.Identity.NativeID)
				} else {
					f.omitted[prior.Asset.Identity.NativeID] = true
				}
			}
			original := f.override
			root, dataset, node := f.ids[dataFactoryType], f.ids[dataFactoryDatasetType], f.ids[dataFactoryNodeType]
			switch mode {
			case "parameters":
				request.Parameters = map[string]any{"force": true}
			case "action":
				request.Action = "stop"
			case "asset-id":
				request.Asset.ID = "other"
			case "connection":
				request.Asset.Identity.ConnectionID = "other"
			case "location":
				request.Asset.Location = "westus"
			case "partition":
				request.Asset.Identity.Partition = "other"
			case "binding":
				request.Asset.Normalized[dataFactoryProof] = "wrong"
			case "configuration":
				request.Asset.Normalized[dataFactoryConfiguration] = "wrong"
			case "members":
				delete(object(request.Asset.Normalized[dataFactoryMembers]), dataset)
			case "missing-impact":
				request.LifecycleImpacts = request.LifecycleImpacts[1:]
			case "duplicate-impact":
				request.LifecycleImpacts = append(request.LifecycleImpacts, request.LifecycleImpacts[0])
			case "retained-impact":
				request.LifecycleImpacts[0].Delete = false
			case "impact-controller":
				request.LifecycleImpacts[0].ControllerID = "other"
			case "impact-connection":
				request.LifecycleImpacts[0].Asset.Identity.ConnectionID = "other"
			case "impact-binding":
				request.LifecycleImpacts[0].Asset.Normalized[dataFactoryProof] = "wrong"
			case "missing-prerequisite":
				request.PrerequisiteDeletions = request.PrerequisiteDeletions[1:]
			case "duplicate-prerequisite":
				request.PrerequisiteDeletions = append(request.PrerequisiteDeletions, request.PrerequisiteDeletions[0])
			case "prerequisite-controller":
				request.PrerequisiteDeletions[0].ControllerID = "other"
			case "changed-root":
				object(f.resources[root]["properties"])["createTime"] = "2026-09-01T00:00:00Z"
			case "changed-child":
				f.resources[dataset]["futurePrivateSetting"] = "changed"
			case "changed-node":
				f.resources[node]["registerTime"] = "2026-09-01T00:00:00Z"
			case "new-child":
				id := root + "/datasets/new-dataset"
				f.resources[id] = batchClone(f.resources[dataset])
				f.resources[id]["id"], f.resources[id]["name"], f.kinds[id] = id, last(id), dataFactoryDatasetType
			case "new-lock":
				f.locks = []any{map[string]any{"id": dataset + "/providers/Microsoft.Authorization/locks/new", "properties": map[string]any{"level": "CanNotDelete"}}}
			case "group-tag":
				f.group["tags"] = map[string]any{"steward:protect": "true"}
			case "group-changed":
				f.group["managedBy"] = resourceID("Microsoft.Solutions/applications", "owner")
			case "root-etag":
				f.resources[root]["etag"] = "changed"
			case "child-etag":
				f.resources[dataset]["etag"] = "changed"
			}
			run, debug := dataFactoryWorkFixture(t, f)
			rootReads := 0
			f.override = func(req *http.Request) (*http.Response, bool) {
				path := strings.ToLower(req.URL.Path)
				if req.Method == "DELETE" || req.Method == "POST" && (strings.HasSuffix(path, "/stop") || strings.HasSuffix(path, "/cancel") || strings.HasSuffix(path, "/deletedataflowdebugsession") || strings.HasSuffix(path, "/removelinks")) {
					t.Fatal("changed native boundary reached mutation", mode, req.Method, req.URL)
				}
				code := 0
				if (mode == "status-403" || mode == "status-404") && strings.EqualFold(path, f.ids[dataFactoryIRType]+"/getstatus") {
					code = 403
					if mode == "status-404" {
						code = 404
					}
				}
				if mode == "child-403" && path == dataset {
					code = 403
				}
				if mode == "group-404" && path == text(f.group["id"]) {
					code = 404
				}
				if code != 0 {
					return jsonResponse(code, map[string]any{"error": map[string]any{"code": "Unavailable"}}, nil), true
				}
				if mode == "new-run" {
					if path == root+"/querypipelineruns" {
						return jsonResponse(200, map[string]any{"value": []any{run}}, nil), true
					}
					if path == root+"/pipelineruns/"+text(run["runId"]) {
						return jsonResponse(200, run, nil), true
					}
				}
				if mode == "new-debug" && path == root+"/querydataflowdebugsessions" {
					return jsonResponse(200, map[string]any{"value": []any{debug}}, nil), true
				}
				if (mode == "late-child" || mode == "reappeared-prerequisite") && path == root {
					rootReads++
					if mode == "reappeared-prerequisite" && rootReads == 2 {
						f.resources[f.ids[dataFactoryTriggerType]] = trigger
					}
					if mode == "late-child" && rootReads == 4 {
						f.resources[dataset]["futurePrivateSetting"] = "late-change"
					}
				}
				return original(req)
			}
			check, err := driver.Preflight(t.Context(), request)
			if err == nil || check.Absent || isNotFound(err) {
				t.Fatal("changed native boundary accepted as safe or absent", mode, err, check)
			}
			if _, err := driver.Execute(t.Context(), request); err == nil || isNotFound(err) {
				t.Fatal("Execute bypassed native preflight", mode, err)
			}
		})
	}
}

func TestDataFactoryUnselectedWorkWaitsWithoutCancellation(t *testing.T) {
	f := newDataFactoryFixture(t)
	run, debug := dataFactoryWorkFixture(t, f)
	root := f.ids[dataFactoryType]
	debugActive := true
	f.override = func(req *http.Request) (*http.Response, bool) {
		switch strings.ToLower(req.URL.Path) {
		case root + "/querypipelineruns":
			return jsonResponse(200, map[string]any{"value": []any{run}}, nil), true
		case root + "/pipelineruns/" + text(run["runId"]):
			return jsonResponse(200, run, nil), true
		case root + "/querydataflowdebugsessions":
			rows := []any{}
			if debugActive {
				rows = append(rows, debug)
			}
			return jsonResponse(200, map[string]any{"value": rows}, nil), true
		}
		return nil, false
	}
	values := f.assets(t)
	value := cdnAsset(t, values, dataFactoryNodeType)
	solved := dataFactoryPlan(t, f, values, value.ID)
	request := servicePlanRequest(solved, values, value)
	original := f.override
	deletes := 0
	f.override = func(req *http.Request) (*http.Response, bool) {
		path := strings.ToLower(req.URL.Path)
		if req.Method == "POST" && (strings.HasSuffix(path, "/cancel") || strings.HasSuffix(path, "/deletedataflowdebugsession") || strings.HasSuffix(path, "/stop")) {
			t.Fatal("independent registration cancelled unselected factory work")
		}
		if req.Method == "DELETE" {
			deletes++
			if dataFactoryRunActive(run) || debugActive {
				t.Fatal("registration deleted during factory work")
			}
			f.deleteResource(value.Identity.NativeID)
			return jsonResponse(200, nil, nil), true
		}
		return original(req)
	}
	driver := dataFactoryDriver(t, f, request)
	result, err := driver.Execute(t.Context(), request)
	if err != nil || result.Data["phase"] != "wait" || deletes != 0 {
		t.Fatal("independent cleanup did not wait for factory work", err, result)
	}
	for i := 0; i < 2; i++ {
		if i == 1 {
			run["status"] = "Succeeded"
		}
		waited, err := driver.Wait(t.Context(), request, result)
		if err != nil || waited.Done || deletes != 0 {
			t.Fatal("unselected work wait escaped scope", err, waited)
		}
		result = fleetSerializedResult(t, result, &waited)
	}
	debugActive = false
	waited, err := driver.Wait(t.Context(), request, result)
	if err != nil || deletes != 1 {
		t.Fatal("finished factory work did not permit registration deletion", err, waited)
	}
	result = fleetSerializedResult(t, result, &waited)
	waited, err = driver.Wait(t.Context(), request, result)
	if err != nil || !waited.Done {
		t.Fatal("registration absence", err, waited)
	}
}

func TestDataFactoryPersistedPreparationRejectsTamperingAndLateChanges(t *testing.T) {
	for _, mode := range []string{"phase", "target", "item", "operation", "binding", "origin", "accepted", "touched", "operation-done", "request", "new-lock", "changed-resource", "changed-parent", "replaced-prerequisite", "unknown-state", "status-403", "status-404"} {
		t.Run(mode, func(t *testing.T) {
			f := newDataFactoryFixture(t)
			id := f.ids[dataFactoryCDCType]
			state := "Running"
			f.override = func(req *http.Request) (*http.Response, bool) {
				if req.Method == "GET" && strings.EqualFold(req.URL.Path, id+"/status") {
					return jsonResponse(200, state, nil), true
				}
				return nil, false
			}
			values := f.assets(t)
			value := cdnAsset(t, values, dataFactoryCDCType)
			solved := dataFactoryPlan(t, f, values, value.ID)
			request := servicePlanRequest(solved, values, value)
			original := f.override
			stops := 0
			f.override = func(req *http.Request) (*http.Response, bool) {
				if req.Method == "POST" && strings.EqualFold(req.URL.Path, id+"/stop") {
					stops++
					return jsonResponse(200, nil, nil), true
				}
				return original(req)
			}
			driver := dataFactoryDriver(t, f, request)
			result, err := driver.Execute(t.Context(), request)
			if err != nil || stops != 1 {
				t.Fatal("prepare native CDC receipt", err)
			}
			result = fleetSerializedResult(t, result, nil)
			state = "Stopped"
			switch mode {
			case "phase":
				result.Data["phase"] = "delete"
			case "target":
				result.Data["target"] = f.ids[dataFactoryType]
			case "item":
				result.Data["item"] = "unreviewed"
			case "operation":
				result.Data["operation"] = map[string]any{"result_url": "https://foreign.invalid/operation"}
			case "binding":
				result.Data["binding"] = "forged"
			case "origin":
				result.ProviderOperationID = "https://foreign.invalid/origin"
			case "accepted":
				result.Data["accepted"] = map[string]any{}
			case "touched":
				result.Data["touched"] = map[string]any{}
			case "operation-done":
				result.Data["operation_done"] = true
			case "request":
				request.Parameters = map[string]any{"force": true}
			case "new-lock":
				f.locks = []any{map[string]any{"id": id + "/providers/Microsoft.Authorization/locks/new", "properties": map[string]any{"level": "ReadOnly"}}}
			case "changed-resource":
				f.resources[id]["futurePrivateSetting"] = "changed"
			case "changed-parent":
				object(f.resources[f.ids[dataFactoryType]]["properties"])["createTime"] = "2026-09-01T00:00:00Z"
			case "replaced-prerequisite":
				f.resources[id]["etag"] = "replacement"
				f.resources[id]["futurePrivateSetting"] = "replacement"
			case "unknown-state":
				state = "Unknown"
			}
			previous := f.override
			f.override = func(req *http.Request) (*http.Response, bool) {
				path := strings.ToLower(req.URL.Path)
				if req.Method == "DELETE" || req.Method == "POST" && strings.HasSuffix(path, "/stop") {
					t.Fatal("changed preparation reached a mutation", mode, req.Method, req.URL)
				}
				if strings.EqualFold(path, id+"/status") && (mode == "status-403" || mode == "status-404") {
					code := 403
					if mode == "status-404" {
						code = 404
					}
					return jsonResponse(code, map[string]any{"error": map[string]any{"code": "Unavailable"}}, nil), true
				}
				return previous(req)
			}
			waited, err := driver.Wait(t.Context(), request, result)
			if err == nil || waited.Done || isNotFound(err) || stops != 1 {
				t.Fatal("changed persisted preparation accepted", mode, err, waited)
			}
		})
	}
}

func TestDataFactoryResidualOwnReadsSurviveMissingAncestors(t *testing.T) {
	for _, mode := range []string{"node-present", "node-forbidden", "node-recreated", "runtime-status-missing", "runtime-forbidden", "all-absent"} {
		t.Run(mode, func(t *testing.T) {
			f := newDataFactoryFixture(t)
			values := f.assets(t)
			value := cdnAsset(t, values, dataFactoryType)
			solved := dataFactoryPlan(t, f, values, value.ID)
			request := servicePlanRequest(solved, values, value)
			for _, prior := range request.PrerequisiteDeletions {
				f.deleteResource(prior.Asset.Identity.NativeID)
			}
			original := f.override
			f.override = func(req *http.Request) (*http.Response, bool) {
				if req.Method == "DELETE" {
					f.deleteResource(value.Identity.NativeID)
					return jsonResponse(200, nil, nil), true
				}
				return original(req)
			}
			driver := dataFactoryDriver(t, f, request)
			result, err := driver.Execute(t.Context(), request)
			if err != nil {
				t.Fatal("prepare deleted factory", err)
			}
			node, runtime := f.ids[dataFactoryNodeType], f.ids[dataFactoryIRType]
			for _, id := range slices.Sorted(maps.Keys(f.resources)) {
				if id == node && mode != "all-absent" || id == runtime && (mode == "runtime-status-missing" || mode == "runtime-forbidden") {
					continue
				}
				f.deleteResource(id)
			}
			if mode == "node-recreated" {
				f.resources[node]["registerTime"] = "2026-09-01T00:00:00Z"
			}
			prior := f.override
			f.override = func(req *http.Request) (*http.Response, bool) {
				path := strings.ToLower(req.URL.Path)
				code := 0
				if mode == "node-forbidden" && path == node || mode == "runtime-forbidden" && path == runtime {
					code = 403
				}
				if mode == "runtime-status-missing" && path == runtime+"/getstatus" {
					code = 404
				}
				if code != 0 {
					return jsonResponse(code, map[string]any{"error": map[string]any{"code": "Unavailable"}}, nil), true
				}
				return prior(req)
			}
			result = fleetSerializedResult(t, result, nil)
			request.ExecutionResult = &result
			driver = dataFactoryDriver(t, f, request)
			waited, err := driver.Wait(t.Context(), request, result)
			if mode == "all-absent" {
				if err != nil || !waited.Done {
					t.Fatal("complete native absence", err, waited)
				}
			} else if mode == "node-present" {
				if err != nil || waited.Done {
					t.Fatal("missing ancestors hid a live registration", err, waited)
				}
			} else if err == nil || waited.Done || isNotFound(err) {
				t.Fatal("uncertain child read became successful absence", mode, err, waited)
			}
		})
	}
}

func TestDataFactorySSISFactoryPlanExecutesAllNativePrerequisites(t *testing.T) {
	f := newDataFactoryFixture(t)
	id := f.ids[dataFactoryIRType]
	props := object(f.resources[id]["properties"])
	props["type"], props["typeProperties"] = "Managed", map[string]any{"ssisProperties": map[string]any{"edition": "Standard"}}
	object(f.statuses[id]["properties"])["type"], object(f.statuses[id]["properties"])["state"] = "Managed", "Started"
	f.deleteResource(f.ids[dataFactoryNodeType])
	object(f.resources[f.ids[dataFactoryLinkedType]]["properties"])["connectVia"] = map[string]any{"type": "IntegrationRuntimeReference", "referenceName": last(id)}
	values := f.assets(t)
	root := cdnAsset(t, values, dataFactoryType)
	solved := dataFactoryPlan(t, f, values, root.ID)
	original := f.override
	order := []string{}
	f.override = func(req *http.Request) (*http.Response, bool) {
		path := strings.ToLower(req.URL.Path)
		if req.Method == "POST" && path == id+"/stop" {
			order = append(order, path)
			object(f.statuses[id]["properties"])["state"] = "Stopped"
			return jsonResponse(200, nil, nil), true
		}
		if req.Method == "DELETE" {
			if path == id && object(f.statuses[id]["properties"])["state"] != "Stopped" {
				t.Fatal("SSIS deletion preceded native stop")
			}
			if path == root.Identity.NativeID && f.resources[id] != nil {
				t.Fatal("factory deletion preceded SSIS removal")
			}
			order = append(order, path)
			for _, target := range slices.Sorted(maps.Keys(f.resources)) {
				if target == path || strings.HasPrefix(target, path+"/") {
					f.deleteResource(target)
				}
			}
			return jsonResponse(200, nil, nil), true
		}
		return original(req)
	}
	for _, step := range solved.Steps {
		value := values[slices.IndexFunc(values, func(v asset.Asset) bool { return v.ID == step.AssetID })]
		request := servicePlanRequest(solved, values, value)
		driver := dataFactoryDriver(t, f, request)
		result, err := driver.Execute(t.Context(), request)
		if err != nil {
			t.Fatal("execute complete SSIS plan", value.Identity.NativeType, err)
		}
		done := false
		for range 3 {
			waited, err := driver.Wait(t.Context(), request, result)
			if err != nil {
				t.Fatal("wait complete SSIS plan", value.Identity.NativeType, err)
			}
			result = fleetSerializedResult(t, result, &waited)
			if waited.Done {
				done = true
				break
			}
		}
		if !done {
			t.Fatal("native SSIS step did not complete", value.Identity.NativeType)
		}
	}
	if len(order) != 8 || len(f.resources) != 0 || slices.Index(order, id+"/stop") >= slices.Index(order, id) || slices.Index(order, id) >= slices.Index(order, root.Identity.NativeID) {
		t.Fatal("complete SSIS factory native order changed", order, len(f.resources))
	}
}

func TestDataFactoryDeletedHostStillVerifiesExternalPrerequisites(t *testing.T) {
	for _, mode := range []string{"present", "changed", "forbidden"} {
		t.Run(mode, func(t *testing.T) {
			f := newDataFactoryFixture(t)
			consumerRoot, consumer := dataFactorySharingFixture(t, f, "Key")
			originalConsumer := batchClone(f.resources[consumer])
			values := f.assets(t)
			value := cdnAsset(t, values, dataFactoryType)
			for _, candidate := range values {
				if candidate.Identity.NativeID == f.ids[dataFactoryType] {
					value = candidate
				}
			}
			solved := dataFactoryPlan(t, f, values, value.ID, asset.AssetID(consumer))
			request := servicePlanRequest(solved, values, value)
			for _, prerequisite := range request.PrerequisiteDeletions {
				f.deleteResource(prerequisite.Asset.Identity.NativeID)
			}
			object(object(f.statuses[f.ids[dataFactoryIRType]]["properties"])["typeProperties"])["links"] = []any{}
			previous := f.override
			f.override = func(req *http.Request) (*http.Response, bool) {
				if req.Method == "DELETE" {
					if strings.ToLower(req.URL.Path) != value.Identity.NativeID {
						t.Fatal("unexpected host deletion")
					}
					f.deleteResource(value.Identity.NativeID)
					return jsonResponse(200, nil, nil), true
				}
				return previous(req)
			}
			driver := dataFactoryDriver(t, f, request)
			result, err := driver.Execute(t.Context(), request)
			if err != nil || result.Data["phase"] != "delete" {
				t.Fatal("prepare external prerequisite readback", err)
			}
			for _, id := range slices.Sorted(maps.Keys(f.resources)) {
				if id != consumerRoot {
					f.deleteResource(id)
				}
			}
			f.resources[consumer] = originalConsumer
			if mode == "changed" {
				f.resources[consumer]["futurePrivateSetting"] = "new-consumer"
			}
			prior := f.override
			f.override = func(req *http.Request) (*http.Response, bool) {
				if mode == "forbidden" && strings.EqualFold(req.URL.Path, consumer) {
					return jsonResponse(403, map[string]any{"error": map[string]any{"code": "Forbidden"}}, nil), true
				}
				return prior(req)
			}
			result = fleetSerializedResult(t, result, nil)
			request.ExecutionResult = &result
			driver = dataFactoryDriver(t, f, request)
			waited, err := driver.Wait(t.Context(), request, result)
			if waited.Done || isNotFound(err) || mode == "present" && err != nil || mode != "present" && err == nil {
				t.Fatal("deleted host concealed a reappeared or unreadable required consumer", mode, waited.Done, err)
			}
		})
	}
}
