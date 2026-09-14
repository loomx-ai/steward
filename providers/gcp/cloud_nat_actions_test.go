package gcp

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func cloudNatActionRuntime(t *testing.T) (*Runtime, contracts.ActionRequest, *routePolicyActionFixture) {
	t.Helper()
	f := &routePolicyActionFixture{exists: true, status: "RUNNING", policy: cloudNatFixture("nat-a"), parent: cloudNatParent("/compute/v1/projects/sample-project/regions/us-central1/routers/router-a")}
	normalized := cloneParameters(f.policy)
	normalized[cloudNatRouterID] = "1001"
	normalized[cloudNatReview] = cloneParameters(f.policy)
	request := contracts.ActionRequest{Asset: asset.Asset{ID: "nat", Identity: asset.Identity{Provider: asset.ProviderGCP, Partition: "gcp", ConnectionID: "connection", NativeType: cloudNatType, NativeID: "//compute.googleapis.com/projects/sample-project/regions/us-central1/routers/router-a/nats/nat-a"}, Normalized: normalized}, Action: "delete", IdempotencyKey: "delete-nat"}
	r := protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
		if req.URL.Host != "compute.googleapis.com" {
			t.Fatal(req.URL)
		}
		if req.Method == "GET" {
			f.reads++
			if strings.HasSuffix(req.URL.Path, "/routers") {
				return dataformResponse(req, 200, map[string]any{"items": []any{f.parent}}), nil
			}
			if strings.HasSuffix(req.URL.Path, "/routers/router-a") {
				switch f.mode {
				case "parent-denied", "policy-denied":
					return apiResponse(req, 403, `{}`), nil
				case "parent-missing":
					return apiResponse(req, 404, `{}`), nil
				}
				parent := cloneParameters(f.parent)
				nats := []any{}
				if f.exists {
					nats = append(nats, f.policy)
				}
				for _, sibling := range f.policies {
					nats = append(nats, sibling)
				}
				parent["nats"] = nats
				if f.mode == "partial" {
					parent["warning"] = map[string]any{"code": "PARTIAL_SUCCESS"}
				}
				if f.mode == "null-nats" {
					parent["nats"] = nil
				}
				return dataformResponse(req, 200, parent), nil
			}
			if strings.HasSuffix(req.URL.Path, "/operations/operation-a") {
				if f.mode == "poll-denied" {
					return apiResponse(req, 403, `{}`), nil
				}
				if f.mode == "poll-expired" {
					return apiResponse(req, 404, `{}`), nil
				}
				data := cloneParameters(f.operation)
				data["status"] = f.status
				return dataformResponse(req, 200, data), nil
			}
		}
		if req.Method == "PATCH" && strings.HasSuffix(req.URL.Path, "/routers/router-a") {
			f.deletes++
			f.requestIDs = append(f.requestIDs, req.URL.Query().Get("requestId"))
			var body map[string]any
			if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if len(body) != 1 || body["nats"] == nil || len(req.URL.Query()) != 1 || req.URL.Query().Get("requestId") == "" {
				t.Fatal("invalid native NAT patch", body, req.URL)
			}
			f.patchPeers = array(body["nats"])
			expected := []any{}
			for _, sibling := range f.policies {
				expected = append(expected, cloudNatConfiguration(sibling))
			}
			if firewallDigest(expected) != firewallDigest(f.patchPeers) {
				t.Fatal("sibling NAT changed", body, expected)
			}
			switch f.mode {
			case "delete-denied":
				return apiResponse(req, 403, `{}`), nil
			case "delete-in-use":
				return apiResponse(req, 409, `{}`), nil
			case "delete-missing", "delete-absent":
				return apiResponse(req, 404, `{}`), nil
			case "delete-retry":
				return apiResponse(req, 503, `{}`), nil
			}
			f.operation = map[string]any{"name": "operation-a", "status": "PENDING", "operationType": "patch", "targetLink": f.parent["selfLink"], "targetId": "1001", "clientOperationId": req.URL.Query().Get("requestId")}
			switch f.mode {
			case "operation-target":
				f.operation["targetLink"] = "https://www.googleapis.com/compute/v1/projects/sample-project/regions/us-central1/routers/other"
			case "operation-scope":
				f.operation["selfLink"] = "https://www.googleapis.com/compute/v1/projects/foreign/regions/us-central1/operations/operation-a"
			case "operation-request":
				f.operation["clientOperationId"] = "foreign"
			case "operation-state":
				f.operation["status"] = "UNKNOWN"
			case "operation-error":
				f.operation["error"] = map[string]any{"errors": []any{map[string]any{"code": "FAILED"}}}
			case "operation-http-error":
				f.operation["httpErrorStatusCode"] = 400
			case "operation-empty":
				f.operation = map[string]any{}
			case "operation-minimal":
				f.operation = map[string]any{"name": "operation-a", "status": "PENDING", "operationType": "patch"}
			}
			return dataformResponse(req, 200, f.operation), nil
		}
		t.Fatal("unexpected NAT endpoint/method", req.Method, req.URL)
		return nil, nil
	})
	return r, request, f
}

func TestCloudNatDeleteNativeOperationAndRestart(t *testing.T) {
	testRouterComponentDeleteNativeOperationAndRestart(t, cloudNatType)
}
func TestCloudNatDeleteGuardsAndFailures(t *testing.T) {
	testRouterComponentDeleteGuardsAndFailures(t, cloudNatType)
}
func TestCloudNatDeleteReceiptAndPollingBoundaries(t *testing.T) {
	testRouterComponentDeleteReceiptAndPollingBoundaries(t, cloudNatType)
}
func TestCloudNatDeleteIdempotencyAndAlreadyAbsent(t *testing.T) {
	testRouterComponentDeleteIdempotencyAndAlreadyAbsent(t, cloudNatType)
}
func TestCloudNatDeleteActionIdentity(t *testing.T) {
	testRouterComponentActionIdentityAndTermOrder(t, cloudNatType)
}
func TestCloudNatSQLiteScanCleanupRestartAndReconciliation(t *testing.T) {
	routerComponentSQLiteCleanup(t, cloudNatType, false, false, false)
}

func TestCloudNatDeletePreservesFreshSiblingsAndUnknownFields(t *testing.T) {
	r, request, f := cloudNatActionRuntime(t)
	sibling := cloudNatFixture("nat-b")
	sibling["futureNativeSetting"] = map[string]any{"keep": true}
	sibling["effectiveTcpTimeWaitTimeoutSec"] = 120
	f.policies = []map[string]any{sibling}
	driver, err := r.ResolveAction(t.Context(), "connection", request.Asset)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := driver.Preflight(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	// Siblings may legitimately change since the scan/earlier preflight. Preserve
	// their latest native configuration, including fields unknown to this version.
	sibling["maxPortsPerVm"] = 2048
	result, err := driver.Execute(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.patchPeers) != 1 || object(f.patchPeers[0])["effectiveTcpTimeWaitTimeoutSec"] != nil || object(f.patchPeers[0])["futureNativeSetting"] == nil {
		t.Fatal(f.patchPeers)
	}
	f.exists = false
	f.status = "DONE"
	wait, err := driver.Wait(t.Context(), request, result)
	if err != nil || !wait.Done {
		t.Fatal(wait, err)
	}
	for _, mode := range []string{"parent-denied", "parent-missing", "partial", "null-nats"} {
		f.mode = mode
		if _, err := driver.Wait(t.Context(), request, result); err == nil {
			t.Fatal("invalid absence accepted", mode)
		}
	}
}

func TestCloudNatSequentialDeletesDoNotRestoreSibling(t *testing.T) {
	r, request, f := cloudNatActionRuntime(t)
	sibling := cloudNatFixture("nat-b")
	f.policies = []map[string]any{sibling}
	for round := 0; round < 2; round++ {
		driver, err := r.ResolveAction(t.Context(), "connection", request.Asset)
		if err != nil {
			t.Fatal(err)
		}
		result, err := driver.Execute(t.Context(), request)
		if err != nil {
			t.Fatal(err)
		}
		if len(f.patchPeers) != 1-round {
			t.Fatal("previous deletion was restored", f.patchPeers)
		}
		f.exists = false
		f.status = "DONE"
		wait, err := driver.Wait(t.Context(), request, result)
		if err != nil || !wait.Done {
			t.Fatal(wait, err)
		}
		if round == 0 {
			// The native set now contains only nat-b; represent it as the next target.
			f.policy = sibling
			f.policies = nil
			f.exists = true
			f.status = "RUNNING"
			request.Asset.ID = "nat-b"
			request.Asset.Identity.NativeID = strings.TrimSuffix(request.Asset.Identity.NativeID, "nat-a") + "nat-b"
			request.Asset.Normalized = cloneParameters(sibling)
			request.Asset.Normalized[cloudNatReview] = cloneParameters(sibling)
			request.Asset.Normalized[cloudNatRouterID] = "1001"
			request.IdempotencyKey = "delete-nat-b"
		}
	}
	if len(f.requestIDs) != 2 || f.requestIDs[0] == f.requestIDs[1] {
		t.Fatal("distinct NAT intents shared request ID", f.requestIDs)
	}
}
