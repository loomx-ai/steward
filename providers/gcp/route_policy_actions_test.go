package gcp

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

type routePolicyActionFixture struct {
	mode           string
	patches        int
	patchStatus    string
	patchOperation map[string]any
	patchPeers     []any
	patchIDs       []string
	exists         bool
	status         string
	deletes        int
	reads          int
	requestIDs     []string
	parent         map[string]any
	policy         map[string]any
	operation      map[string]any
}

func routePolicyActionRuntime(t *testing.T) (*Runtime, contracts.ActionRequest, *routePolicyActionFixture) {
	t.Helper()
	parentID := "//compute.googleapis.com/projects/sample-project/regions/us-central1/routers/router-a"
	policy := routePolicyFixture("policy-a")
	fixture := &routePolicyActionFixture{exists: true, status: "RUNNING", policy: policy, parent: map[string]any{"name": "router-a", "id": "1001", "selfLink": "https://www.googleapis.com/compute/v1/projects/sample-project/regions/us-central1/routers/router-a", "bgpPeers": []any{map[string]any{"name": "peer-a", "importPolicies": []any{"other-policy"}, "exportPolicies": []any{"export-policy"}}}}}
	normalized := cloneParameters(policy)
	normalized[routePolicyRouterID] = "1001"
	normalized[routePolicyPeers], _ = routePolicyBGPPeers(fixture.parent)
	request := contracts.ActionRequest{Asset: asset.Asset{ID: "policy", Identity: asset.Identity{Provider: asset.ProviderGCP, Partition: "gcp", ConnectionID: "connection", NativeType: routePolicyType, NativeID: parentID + "/routePolicies/policy-a"}, Normalized: normalized}, Action: "delete", IdempotencyKey: "delete-policy"}
	runtime := protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
		if req.URL.Host != "compute.googleapis.com" {
			t.Fatal("unexpected host", req.URL)
		}
		if req.Method == "GET" {
			fixture.reads++
			if strings.HasSuffix(req.URL.Path, "/regions/us-central1/routers") {
				return dataformResponse(req, 200, map[string]any{"items": []any{fixture.parent}}), nil
			}
			if strings.HasSuffix(req.URL.Path, "/routers/router-a/listRoutePolicies") {
				data := map[string]any{}
				if fixture.exists {
					data["result"] = []any{map[string]any{"name": "policy-a"}}
				}
				return dataformResponse(req, 200, data), nil
			}
			if strings.HasSuffix(req.URL.Path, "/routers/router-a") {
				if fixture.mode == "parent-denied" {
					return apiResponse(req, 403, `{}`), nil
				}
				if fixture.mode == "parent-missing" {
					return apiResponse(req, 404, `{}`), nil
				}
				return dataformResponse(req, 200, fixture.parent), nil
			}
			if strings.HasSuffix(req.URL.Path, "/routers/router-a/getRoutePolicy") {
				if req.URL.Query().Get("policy") != "policy-a" {
					t.Fatal("wrong policy", req.URL)
				}
				if fixture.mode == "policy-denied" {
					return apiResponse(req, 403, `{}`), nil
				}
				if !fixture.exists {
					return apiResponse(req, 404, `{}`), nil
				}
				return dataformResponse(req, 200, map[string]any{"resource": fixture.policy}), nil
			}
			if strings.HasSuffix(req.URL.Path, "/regions/us-central1/operations/bgp-operation") {
				if fixture.mode == "patch-poll-denied" {
					return apiResponse(req, 403, `{}`), nil
				}
				if fixture.mode == "patch-poll-expired" {
					return apiResponse(req, 404, `{}`), nil
				}
				data := cloneParameters(fixture.patchOperation)
				data["status"] = fixture.patchStatus
				return dataformResponse(req, 200, data), nil
			}
			if strings.HasSuffix(req.URL.Path, "/regions/us-central1/operations/operation-a") {
				if fixture.mode == "poll-denied" {
					return apiResponse(req, 403, `{}`), nil
				}
				if fixture.mode == "poll-expired" {
					return apiResponse(req, 404, `{}`), nil
				}
				result := cloneParameters(fixture.operation)
				result["status"] = fixture.status
				return dataformResponse(req, 200, result), nil
			}
		}
		if req.Method == "PATCH" && strings.HasSuffix(req.URL.Path, "/routers/router-a") {
			fixture.patches++
			fixture.patchIDs = append(fixture.patchIDs, req.URL.Query().Get("requestId"))
			var body map[string]any
			if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if len(body) != 1 || body["bgpPeers"] == nil || len(req.URL.Query()) != 1 || req.URL.Query().Get("requestId") == "" {
				t.Fatal("invalid patch", body, req.URL)
			}
			fixture.patchPeers = array(body["bgpPeers"])
			for _, v := range fixture.patchPeers {
				peer := object(v)
				if _, ok := peer["managementType"]; ok {
					t.Fatal("output-only field sent", peer)
				}
				for _, direction := range []string{"importPolicies", "exportPolicies"} {
					for _, policy := range array(peer[direction]) {
						if policy == "policy-a" {
							t.Fatal("selected reference remains", peer)
						}
					}
				}
			}
			switch fixture.mode {
			case "patch-denied":
				return apiResponse(req, 403, `{}`), nil
			case "patch-conflict":
				return apiResponse(req, 409, `{}`), nil
			case "patch-retry":
				return apiResponse(req, 503, `{}`), nil
			}
			fixture.patchStatus = "RUNNING"
			fixture.patchOperation = map[string]any{"name": "bgp-operation", "operationType": "patch", "status": "PENDING", "targetLink": fixture.parent["selfLink"], "targetId": "1001", "clientOperationId": req.URL.Query().Get("requestId")}
			return dataformResponse(req, 200, fixture.patchOperation), nil
		}
		if req.Method == "POST" && strings.HasSuffix(req.URL.Path, "/routers/router-a/deleteRoutePolicy") {
			fixture.deletes++
			fixture.requestIDs = append(fixture.requestIDs, req.URL.Query().Get("requestId"))
			if req.URL.Query().Get("policy") != "policy-a" || len(req.URL.Query()) != 2 {
				t.Fatal("wrong delete parameters", req.URL)
			}
			if req.Body != nil {
				body, err := io.ReadAll(req.Body)
				if err != nil || len(body) != 0 {
					t.Fatal("native delete must have no body", string(body), err)
				}
			}
			switch fixture.mode {
			case "delete-denied":
				return apiResponse(req, 403, `{}`), nil
			case "delete-in-use":
				return apiResponse(req, 409, `{"error":{"code":409,"status":"ABORTED","message":"resource in use"}}`), nil
			case "delete-missing":
				return apiResponse(req, 404, `{}`), nil
			case "delete-absent":
				fixture.exists = false
				return apiResponse(req, 404, `{}`), nil
			case "delete-retry":
				return apiResponse(req, 503, `{}`), nil
			}
			fixture.operation = map[string]any{"name": "operation-a", "status": "PENDING", "operationType": "deleteRoutePolicy", "selfLink": "https://www.googleapis.com/compute/v1/projects/sample-project/regions/us-central1/operations/operation-a", "targetLink": fixture.parent["selfLink"], "targetId": "1001", "clientOperationId": req.URL.Query().Get("requestId"), "httpErrorStatusCode": 0}
			switch fixture.mode {
			case "operation-policy-target":
				fixture.operation["targetLink"] = text(fixture.parent["selfLink"]) + "/getRoutePolicy?policy=policy-a"
			case "operation-target":
				fixture.operation["targetLink"] = "https://www.googleapis.com/compute/v1/projects/sample-project/regions/us-central1/routers/other"
			case "operation-scope":
				fixture.operation["selfLink"] = "https://www.googleapis.com/compute/v1/projects/foreign/regions/us-central1/operations/operation-a"
			case "operation-request":
				fixture.operation["clientOperationId"] = "foreign"
			case "operation-state":
				fixture.operation["status"] = "UNKNOWN"
			case "operation-error":
				fixture.operation["error"] = map[string]any{"errors": []any{map[string]any{"code": "FAILED"}}}
			case "operation-http-error":
				fixture.operation["httpErrorStatusCode"] = 400
			case "operation-empty":
				fixture.operation = map[string]any{}
			case "operation-minimal":
				fixture.operation = map[string]any{"name": "operation-a", "status": "PENDING", "operationType": "deleteRoutePolicy"}
			}
			return dataformResponse(req, 200, fixture.operation), nil
		}
		t.Fatal("unexpected or unrelated mutation", req.Method, req.URL)
		return nil, nil
	})
	return runtime, request, fixture
}

func TestRoutePolicyDeleteNativeOperationAndRestart(t *testing.T) {
	for _, mode := range []string{"", "operation-minimal", "operation-policy-target"} {
		t.Run(mode, func(t *testing.T) {
			r, request, fixture := routePolicyActionRuntime(t)
			fixture.mode = mode
			parentBefore, _ := json.Marshal(fixture.parent)
			driver, err := r.ResolveAction(t.Context(), "connection", request.Asset)
			if err != nil {
				t.Fatal(err)
			}
			check, err := driver.Preflight(t.Context(), request)
			if err != nil || !check.Allowed || check.Absent {
				t.Fatal(check, err)
			}
			result, err := driver.Execute(t.Context(), request)
			if err != nil || result.ProviderRequestID == "" || result.ProviderOperationID != "https://compute.googleapis.com/compute/v1/projects/sample-project/regions/us-central1/operations/operation-a" || fixture.deletes != 1 {
				t.Fatal("delete", result, err)
			}
			// Only serialized request/receipt data survive the simulated process restart.
			encoded, _ := json.Marshal(struct {
				Request contracts.ActionRequest
				Result  contracts.ActionResult
			}{request, result})
			var restored struct {
				Request contracts.ActionRequest
				Result  contracts.ActionResult
			}
			if err := json.Unmarshal(encoded, &restored); err != nil {
				t.Fatal(err)
			}
			driver, err = r.ResolveAction(t.Context(), "connection", restored.Request.Asset)
			if err != nil {
				t.Fatal(err)
			}
			wait, err := driver.Wait(t.Context(), restored.Request, restored.Result)
			if err != nil || wait.Done {
				t.Fatal("running operation", wait, err)
			}
			fixture.status = "DONE"
			wait, err = driver.Wait(t.Context(), restored.Request, restored.Result)
			if err != nil || wait.Done {
				t.Fatal("DONE is not policy absence", wait, err)
			}
			fixture.exists = false
			wait, err = driver.Wait(t.Context(), restored.Request, restored.Result)
			if err != nil || !wait.Done {
				t.Fatal("final absence", wait, err)
			}
			restored.Request.ExecutionResult = &restored.Result
			read, err := driver.Readback(t.Context(), restored.Request)
			if err != nil || read.Exists {
				t.Fatal("readback", read, err)
			}
			parentAfter, _ := json.Marshal(fixture.parent)
			if string(parentBefore) != string(parentAfter) || fixture.deletes != 1 {
				t.Fatal("changed containing router or repeated deletion")
			}
		})
	}
}

func TestRoutePolicyDeleteGuardsAndFailures(t *testing.T) {
	for _, mode := range []string{"parent-denied", "parent-missing", "policy-denied", "parent-recreated", "parent-identity", "policy-changed", "review-missing", "fingerprint-missing", "delete-denied", "delete-in-use", "delete-missing", "operation-target", "operation-scope", "operation-request", "operation-state", "operation-error", "operation-http-error", "operation-empty"} {
		t.Run(mode, func(t *testing.T) {
			r, request, fixture := routePolicyActionRuntime(t)
			fixture.mode = mode
			switch mode {
			case "parent-recreated":
				fixture.parent["id"] = "1002"
			case "parent-identity":
				fixture.parent["name"] = "other"
			case "policy-changed":
				fixture.policy = cloneParameters(fixture.policy)
				fixture.policy["fingerprint"] = "ZnAy"
			case "review-missing":
				delete(request.Asset.Normalized, routePolicyRouterID)
			case "fingerprint-missing":
				delete(request.Asset.Normalized, "fingerprint")
			}
			driver, err := r.ResolveAction(t.Context(), "connection", request.Asset)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = driver.Execute(t.Context(), request); err == nil {
				t.Fatal("invalid cleanup accepted")
			}
			if !strings.HasPrefix(mode, "delete-") && !strings.HasPrefix(mode, "operation-") && fixture.deletes != 0 {
				t.Fatal("preflight failure reached mutation")
			}
			if (mode == "parent-missing" || mode == "parent-denied") && isNotFound(err) {
				t.Fatal("parent read failure became verified policy absence")
			}
		})
	}
}

func TestRoutePolicyDeleteIdempotencyAndAlreadyAbsent(t *testing.T) {
	r, request, fixture := routePolicyActionRuntime(t)
	driver, err := r.ResolveAction(t.Context(), "connection", request.Asset)
	if err != nil {
		t.Fatal(err)
	}
	fixture.mode = "delete-retry"
	if _, err := driver.Execute(t.Context(), request); err == nil {
		t.Fatal("expected transient failure")
	}
	fixture.mode = ""
	result, err := driver.Execute(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if fixture.deletes != 2 || fixture.requestIDs[0] == "" || fixture.requestIDs[0] != fixture.requestIDs[1] {
		t.Fatal("request ID changed across retry", fixture.requestIDs)
	}
	fixture.mode = "poll-expired"
	wait, err := driver.Wait(t.Context(), request, result)
	if err != nil || wait.Done {
		t.Fatal("expired receipt invented absence", wait, err)
	}
	fixture.exists = false
	wait, err = driver.Wait(t.Context(), request, result)
	if err != nil || !wait.Done {
		t.Fatal(wait, err)
	}
	if _, err := driver.Execute(t.Context(), request); err != nil || fixture.deletes != 2 {
		t.Fatal("absent policy was deleted again", err)
	}
	fixture.exists = true
	fixture.mode = "delete-absent"
	result, err = driver.Execute(t.Context(), request)
	if err != nil {
		t.Fatal("404 followed by verified absence", err)
	}
	wait, err = driver.Wait(t.Context(), request, result)
	if err != nil || !wait.Done {
		t.Fatal(wait, err)
	}
}

func TestRoutePolicyDeleteReceiptAndPollingBoundaries(t *testing.T) {
	for _, mode := range []string{"receipt-policy", "receipt-connection", "receipt-operation", "receipt-key", "poll-denied", "poll-name", "poll-target", "poll-request", "poll-type", "poll-region", "poll-zone", "poll-error", "poll-empty-error", "parent-recreated", "replacement"} {
		t.Run(mode, func(t *testing.T) {
			r, request, fixture := routePolicyActionRuntime(t)
			driver, err := r.ResolveAction(t.Context(), "connection", request.Asset)
			if err != nil {
				t.Fatal(err)
			}
			result, err := driver.Execute(t.Context(), request)
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "receipt-policy":
				result.Data["resource"] = "other"
			case "receipt-connection":
				result.Data["connection"] = "other"
			case "receipt-operation":
				result.ProviderOperationID = strings.Replace(result.ProviderOperationID, "sample-project", "foreign-project", 1)
			case "receipt-key":
				request.IdempotencyKey = "other"
			case "poll-name":
				fixture.operation["name"] = "operation-b"
			case "poll-target":
				fixture.operation["targetId"] = "1002"
			case "poll-request":
				fixture.operation["clientOperationId"] = "other"
			case "poll-region":
				fixture.operation["region"] = "https://www.googleapis.com/compute/v1/projects/sample-project/regions/europe-west1"
			case "poll-zone":
				fixture.operation["zone"] = "https://www.googleapis.com/compute/v1/projects/sample-project/zones/us-central1-a"
			case "poll-type":
				fixture.operation["operationType"] = "patch"
			case "poll-error":
				fixture.operation["error"] = map[string]any{"errors": []any{map[string]any{"code": "FAILED"}}}
			case "poll-empty-error":
				fixture.operation["error"] = map[string]any{}
			case "parent-recreated":
				fixture.parent["id"] = "1002"
			case "replacement":
				fixture.policy = cloneParameters(fixture.policy)
				fixture.policy["fingerprint"] = "ZnAy"
			}
			fixture.mode = mode
			before := fixture.reads
			if _, err := driver.Wait(t.Context(), request, result); err == nil {
				t.Fatal("invalid receipt/readback accepted")
			}
			if strings.HasPrefix(mode, "receipt-") && fixture.reads != before {
				t.Fatal("invalid receipt called cloud")
			}
			if fixture.deletes != 1 {
				t.Fatal("poll retried a mutation")
			}
		})
	}
}

func TestRoutePolicyActionIdentityAndTermOrder(t *testing.T) {
	r, request, fixture := routePolicyActionRuntime(t)
	if _, err := r.ResolveAction(t.Context(), "other", request.Asset); err == nil {
		t.Fatal("cross-connection resolution accepted")
	}
	driver, err := r.ResolveAction(t.Context(), "connection", request.Asset)
	if err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*contracts.ActionRequest){
		func(r *contracts.ActionRequest) { r.Asset.Identity.NativeID += "other" },
		func(r *contracts.ActionRequest) { r.Asset.Identity.ConnectionID = "other" },
		func(r *contracts.ActionRequest) { r.Parameters = map[string]any{"policy": "other"} },
		func(r *contracts.ActionRequest) { r.LifecycleImpacts = []contracts.ActionImpact{{Asset: r.Asset}} },
	} {
		copy := request
		mutate(&copy)
		before := fixture.reads
		if _, err := driver.Execute(t.Context(), copy); err == nil || fixture.reads != before {
			t.Fatal("changed action reached cloud", err)
		}
	}
	first := object(array(fixture.policy["terms"])[0])
	second := map[string]any{"priority": 20, "actions": []any{map[string]any{"expression": "drop()"}}}
	request.Asset.Normalized["terms"] = []any{first, second}
	fixture.policy = cloneParameters(fixture.policy)
	fixture.policy["terms"] = []any{second, first}
	check, err := driver.Preflight(t.Context(), request)
	if err != nil || !check.Allowed {
		t.Fatal("native term reordering rejected", check, err)
	}
}
