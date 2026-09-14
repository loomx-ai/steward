package gcp

import (
	"encoding/json"
	"github.com/loomx-ai/steward/internal/core/asset"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func routePolicyAttachFixture(t *testing.T, request *contracts.ActionRequest, fixture *routePolicyActionFixture) {
	t.Helper()
	fixture.parent["bgpPeers"] = []any{
		map[string]any{"name": "peer-b", "peerAsn": float64(64513), "importPolicies": []any{"policy-a"}, "managementType": "MANAGED_BY_ATTACHMENT", "bfd": map[string]any{"sessionInitializationMode": "ACTIVE", "minReceiveInterval": float64(1000)}},
		map[string]any{"name": "peer-a", "peerAsn": float64(64512), "importPolicies": []any{"first-policy", "policy-a", "last-policy"}, "exportPolicies": []any{"export-policy"}, "managementType": "MANAGED_BY_USER", "enable": "TRUE", "interfaceName": "interface-a", "md5AuthenticationKeyName": "key-a", "customLearnedIpRanges": []any{map[string]any{"range": "10.4.0.0/16"}}},
		map[string]any{"name": "peer-unrelated", "peerAsn": float64(64514), "importPolicies": []any{"other-policy"}},
	}
	fixture.parent["nats"] = []any{map[string]any{"name": "nat-a"}}
	fixture.parent["interfaces"] = []any{map[string]any{"name": "interface-a", "linkedVpnTunnel": "tunnel-a"}}
	fixture.parent["md5AuthenticationKeys"] = []any{map[string]any{"name": "key-a", "key": "router-only-secret"}}
	peers, err := routePolicyBGPPeers(fixture.parent)
	if err != nil {
		t.Fatal(err)
	}
	request.Asset.Normalized[routePolicyPeers] = peers
}

func applyRoutePolicyPatch(t *testing.T, fixture *routePolicyActionFixture) {
	t.Helper()
	if fixture.patches == 0 {
		t.Fatal("no patch to apply")
	}
	peers := []any{}
	for _, value := range fixture.patchPeers {
		peer := cloneParameters(object(value))
		for _, original := range array(fixture.parent["bgpPeers"]) {
			if object(original)["name"] == peer["name"] {
				if management, exists := object(original)["managementType"]; exists {
					peer["managementType"] = management
				}
			}
		}
		peers = append(peers, peer)
	}
	fixture.parent["bgpPeers"] = peers
}

func TestRoutePolicyBGPDetachAndDeleteRestart(t *testing.T) {
	r, request, f := routePolicyActionRuntime(t)
	routePolicyAttachFixture(t, &request, f)
	before, _ := json.Marshal(f.parent)
	driver, err := r.ResolveAction(t.Context(), "connection", request.Asset)
	if err != nil {
		t.Fatal(err)
	}
	result, err := driver.Execute(t.Context(), request)
	if err != nil || f.patches != 1 || f.deletes != 0 || result.Data["phase"] != routePolicyDetach {
		t.Fatal(result, err, f.patches, f.deletes)
	}
	// Explicit expected payload, including retained order and an empty import list.
	expected := `[{"name":"peer-a","peerAsn":64512,"importPolicies":["first-policy","last-policy"],"exportPolicies":["export-policy"],"enable":"TRUE","interfaceName":"interface-a","md5AuthenticationKeyName":"key-a","customLearnedIpRanges":[{"range":"10.4.0.0/16"}]},{"name":"peer-b","peerAsn":64513,"exportPolicies":[],"importPolicies":[],"bfd":{"sessionInitializationMode":"ACTIVE","minReceiveInterval":1000}},{"name":"peer-unrelated","peerAsn":64514,"importPolicies":["other-policy"],"exportPolicies":[]}]`
	var wanted []any
	if err := json.Unmarshal([]byte(expected), &wanted); err != nil {
		t.Fatal(err)
	}
	if firewallDigest(f.patchPeers) != firewallDigest(wanted) {
		t.Fatal("wrong native replacement body", f.patchPeers)
	}
	// Every phase is resumed using only JSON-persisted review/receipt plus a new driver.
	wait := func() contracts.WaitResult {
		t.Helper()
		raw, err := json.Marshal(struct {
			Request contracts.ActionRequest
			Result  contracts.ActionResult
		}{request, result})
		if err != nil {
			t.Fatal(err)
		}
		var resumed struct {
			Request contracts.ActionRequest
			Result  contracts.ActionResult
		}
		if err = json.Unmarshal(raw, &resumed); err != nil {
			t.Fatal(err)
		}
		fresh := protocolRuntime(t, r.transport.RoundTrip)
		driver, err = fresh.ResolveAction(t.Context(), "connection", resumed.Request.Asset)
		if err != nil {
			t.Fatal(err)
		}
		value, err := driver.Wait(t.Context(), resumed.Request, resumed.Result)
		if err != nil {
			t.Fatal(err)
		}
		if value.Data != nil {
			result.Data = value.Data
		}
		return value
	}
	if w := wait(); w.Done || f.deletes != 0 {
		t.Fatal("pending patch", w)
	}
	f.patchStatus = "DONE"
	if w := wait(); w.Done || f.deletes != 0 {
		t.Fatal("unobserved patch", w)
	}
	applyRoutePolicyPatch(t, f)
	if w := wait(); w.Done || f.deletes != 1 || result.Data["phase"] != "route_policy_delete" || result.ProviderOperationID == result.Data["operation"] {
		t.Fatal("delete continuation", w, result, f.deletes)
	}
	if f.patchIDs[0] == f.requestIDs[0] {
		t.Fatal("phase request IDs collide")
	}
	if w := wait(); w.Done {
		t.Fatal("pending delete", w)
	}
	f.status = "DONE"
	if w := wait(); w.Done {
		t.Fatal("policy still exists", w)
	}
	f.exists = false
	if w := wait(); !w.Done {
		t.Fatal("absence", w)
	}
	request.ExecutionResult = &result
	read, err := driver.Readback(t.Context(), request)
	if err != nil || read.Exists || f.patches != 1 || f.deletes != 1 {
		t.Fatal(read, err, f.patches, f.deletes)
	}
	var original map[string]any
	_ = json.Unmarshal(before, &original)
	delete(original, "bgpPeers")
	final := cloneParameters(f.parent)
	delete(final, "bgpPeers")
	if firewallDigest(original) != firewallDigest(final) {
		t.Fatal("unrelated router fields changed")
	}
}

func TestRoutePolicyBGPChangedReviewAndNativeFailures(t *testing.T) {
	for _, mode := range []string{"missing-review", "invalid-peers", "duplicate-peer", "invalid-policy-array", "invalid-policy-name", "new-peer", "reordered-policies", "peer-setting", "patch-denied", "patch-conflict"} {
		t.Run(mode, func(t *testing.T) {
			r, request, f := routePolicyActionRuntime(t)
			routePolicyAttachFixture(t, &request, f)
			switch mode {
			case "missing-review":
				delete(request.Asset.Normalized, routePolicyPeers)
			case "invalid-peers":
				request.Asset.Normalized[routePolicyPeers] = nil
			case "duplicate-peer":
				f.parent["bgpPeers"] = append(array(f.parent["bgpPeers"]), object(array(f.parent["bgpPeers"])[0]))
			case "invalid-policy-array":
				object(array(f.parent["bgpPeers"])[0])["importPolicies"] = nil
			case "invalid-policy-name":
				object(array(f.parent["bgpPeers"])[0])["importPolicies"] = []any{"../foreign"}
			case "new-peer":
				f.parent["bgpPeers"] = append(array(f.parent["bgpPeers"]), map[string]any{"name": "new-peer"})
			case "reordered-policies":
				object(array(f.parent["bgpPeers"])[1])["importPolicies"] = []any{"last-policy", "policy-a", "first-policy"}
			case "peer-setting":
				object(array(f.parent["bgpPeers"])[1])["peerAsn"] = float64(64515)
			default:
				f.mode = mode
			}
			driver, err := r.ResolveAction(t.Context(), "connection", request.Asset)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = driver.Execute(t.Context(), request); err == nil || f.deletes != 0 {
				t.Fatal("unsafe execution", err, f.deletes)
			}
			if mode != "patch-denied" && mode != "patch-conflict" && f.patches != 0 {
				t.Fatal("mutated invalid review")
			}
		})
	}
}

func TestRoutePolicyBGPWaitFailureAndExpiredOperation(t *testing.T) {
	for _, mode := range []string{"patch-poll-denied", "patch-error", "operation-target", "operation-request", "receipt-peer-review", "receipt-stage", "new-peer", "parent-recreated", "policy-changed", "patch-poll-expired"} {
		t.Run(mode, func(t *testing.T) {
			r, request, f := routePolicyActionRuntime(t)
			routePolicyAttachFixture(t, &request, f)
			driver, err := r.ResolveAction(t.Context(), "connection", request.Asset)
			if err != nil {
				t.Fatal(err)
			}
			result, err := driver.Execute(t.Context(), request)
			if err != nil {
				t.Fatal(err)
			}
			f.patchStatus = "DONE"
			switch mode {
			case "patch-error":
				f.patchOperation["error"] = map[string]any{"errors": []any{map[string]any{"code": "FAILED"}}}
			case "operation-target":
				f.patchOperation["targetId"] = "1002"
			case "operation-request":
				f.patchOperation["clientOperationId"] = "foreign"
			case "receipt-peer-review":
				result.Data["bgp_review"] = "foreign"
			case "receipt-stage":
				result.Data["phase"] = "route_policy_delete"
			case "new-peer":
				f.parent["bgpPeers"] = append(array(f.parent["bgpPeers"]), map[string]any{"name": "new-peer"})
			case "parent-recreated":
				f.parent["id"] = "1002"
			case "policy-changed":
				f.policy["fingerprint"] = "replacement"
			default:
				f.mode = mode
			}
			w, err := driver.Wait(t.Context(), request, result)
			if mode == "patch-poll-expired" {
				if err != nil || w.Done || f.deletes != 0 {
					t.Fatal("expired patch is not detachment", w, err)
				}
				applyRoutePolicyPatch(t, f)
				w, err = driver.Wait(t.Context(), request, result)
				if err != nil || w.Done || f.deletes != 1 {
					t.Fatal("observed patch continuation", w, err)
				}
			} else if err == nil || f.deletes != 0 {
				t.Fatal("invalid wait accepted", w, err)
			}
			if f.patches != 1 {
				t.Fatal("wait repeated patch")
			}
		})
	}
}

func TestRoutePolicyBGPRetryAndAlreadyDetached(t *testing.T) {
	r, request, f := routePolicyActionRuntime(t)
	routePolicyAttachFixture(t, &request, f)
	driver, err := r.ResolveAction(t.Context(), "connection", request.Asset)
	if err != nil {
		t.Fatal(err)
	}
	f.mode = "patch-retry"
	for i := 0; i < 2; i++ {
		if _, err = driver.Execute(t.Context(), request); err == nil {
			t.Fatal("expected native retry failure")
		}
	}
	if len(f.patchIDs) < 2 {
		t.Fatal(f.patchIDs)
	}
	for _, id := range f.patchIDs {
		if id != f.patchIDs[0] {
			t.Fatal("patch retry changed UUID")
		}
	}
	// Simulate a lost successful PATCH receipt. The next attempt observes its exact
	// desired state and proceeds to policy deletion instead of reapplying the patch.
	applyRoutePolicyPatch(t, f)
	f.mode = ""
	patches := f.patches
	result, err := driver.Execute(t.Context(), request)
	if err != nil || f.patches != patches || f.deletes != 1 || result.Data["phase"] != "route_policy_delete" {
		t.Fatal(result, err)
	}
}

func TestRoutePolicyBGPPolicyAbsentStillUnlinksReferences(t *testing.T) {
	r, request, f := routePolicyActionRuntime(t)
	routePolicyAttachFixture(t, &request, f)
	f.exists = false
	driver, err := r.ResolveAction(t.Context(), "connection", request.Asset)
	if err != nil {
		t.Fatal(err)
	}
	check, err := driver.Preflight(t.Context(), request)
	if err != nil || check.Absent || !check.Allowed {
		t.Fatal("dangling references overlooked", check, err)
	}
	result, err := driver.Execute(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	applyRoutePolicyPatch(t, f)
	if w, err := driver.Wait(t.Context(), request, result); err != nil || w.Done {
		t.Fatal("pending patch", w, err)
	}
	f.patchStatus = "DONE"
	if w, err := driver.Wait(t.Context(), request, result); err != nil || !w.Done || f.deletes != 0 {
		t.Fatal("dangling reference cleanup", w, err)
	}
}

func TestRoutePolicyBGPExportPolicyAndReadbackDrift(t *testing.T) {
	r, request, f := routePolicyActionRuntime(t)
	f.policy["type"] = "ROUTE_POLICY_TYPE_EXPORT"
	request.Asset.Normalized["type"] = "ROUTE_POLICY_TYPE_EXPORT"
	f.parent["bgpPeers"] = []any{map[string]any{"name": "peer-a", "importPolicies": []any{"import-policy"}, "exportPolicies": []any{"first-export", "policy-a", "last-export"}}}
	request.Asset.Normalized[routePolicyPeers], _ = routePolicyBGPPeers(f.parent)
	driver, err := r.ResolveAction(t.Context(), "connection", request.Asset)
	if err != nil {
		t.Fatal(err)
	}
	result, err := driver.Execute(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	peer := object(f.patchPeers[0])
	if firewallDigest(peer["importPolicies"]) != firewallDigest([]any{"import-policy"}) || firewallDigest(peer["exportPolicies"]) != firewallDigest([]any{"first-export", "last-export"}) {
		t.Fatal("wrong export detachment", peer)
	}
	applyRoutePolicyPatch(t, f)
	f.patchStatus = "DONE"
	w, err := driver.Wait(t.Context(), request, result)
	if err != nil || w.Done || f.deletes != 1 {
		t.Fatal(w, err)
	}
	result.Data = w.Data
	f.status = "DONE"
	f.exists = false
	object(array(f.parent["bgpPeers"])[0])["exportPolicies"] = []any{"different-policy"}
	if _, err = driver.Wait(t.Context(), request, result); err == nil {
		t.Fatal("unrelated policy change passed final readback")
	}
}

func TestRoutePolicyBGPPatchMatchesNativeSchema(t *testing.T) {
	raw, err := os.ReadFile("catalog/source/discovery.json")
	if err != nil {
		t.Fatal(err)
	}
	var source map[string]any
	if err = json.Unmarshal(raw, &source); err != nil {
		t.Fatal(err)
	}
	var schemas map[string]any
	for _, value := range array(source["documents"]) {
		document := object(object(value)["document"])
		if object(document["methods"])["compute.routers.patch"] != nil {
			if document["revision"] != "20260908" || object(value)["source_sha256"] != "aa1078267f6ad9c82274e6c62572bae328b0de11c6f08861f20488b6617afda7" {
				t.Fatal("unreviewed native source")
			}
			schemas = object(document["schemas"])
		}
	}
	if schemas == nil {
		t.Fatal("missing native PATCH schema")
	}
	compiler := discoveryFixtureSchemaCompiler(t, schemas)
	schema, err := compiler.Compile("https://fixture.test/infra-manager.json#/definitions/Router")
	if err != nil {
		t.Fatal(err)
	}
	r, request, f := routePolicyActionRuntime(t)
	routePolicyAttachFixture(t, &request, f)
	driver, err := r.ResolveAction(t.Context(), "connection", request.Asset)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = driver.Execute(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	body := map[string]any{"bgpPeers": f.patchPeers}
	if err = schema.Validate(body); err != nil {
		t.Fatal("native PATCH schema rejected body", err)
	}
	object(f.patchPeers[0])["importPolicies"] = "invalid-array"
	if schema.Validate(body) == nil {
		t.Fatal("native schema failed to reject wrong array type")
	}
}

func TestRoutePolicyBGPContinuationLostReceiptUsesSameRequestID(t *testing.T) {
	r, request, f := routePolicyActionRuntime(t)
	routePolicyAttachFixture(t, &request, f)
	driver, err := r.ResolveAction(t.Context(), "connection", request.Asset)
	if err != nil {
		t.Fatal(err)
	}
	result, err := driver.Execute(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	applyRoutePolicyPatch(t, f)
	f.patchStatus = "DONE"
	for i := 0; i < 2; i++ {
		fresh := protocolRuntime(t, r.transport.RoundTrip)
		driver, err = fresh.ResolveAction(t.Context(), "connection", request.Asset)
		if err != nil {
			t.Fatal(err)
		}
		// The first continuation response is deliberately lost before local persistence.
		w, err := driver.Wait(t.Context(), request, result)
		if err != nil || w.Done || w.Data["phase"] != "route_policy_delete" {
			t.Fatal(w, err)
		}
	}
	if f.patches != 1 || f.deletes != 2 || f.requestIDs[0] != f.requestIDs[1] {
		t.Fatal("lost receipt changed native idempotency", f.patchIDs, f.requestIDs)
	}
}

func TestRoutePolicyBGPInventoryCursorBindsPeerSnapshot(t *testing.T) {
	for _, mode := range []string{"unchanged", "peer-order", "peer-setting", "policy-order", "malformed"} {
		t.Run(mode, func(t *testing.T) {
			base, review, f := routePolicyActionRuntime(t)
			routePolicyAttachFixture(t, &review, f)
			pages := 0
			r := protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
				if strings.HasSuffix(req.URL.Path, "/listRoutePolicies") {
					pages++
					if req.URL.Query().Get("pageToken") == "next" {
						return apiResponse(req, 200, `{}`), nil
					}
					return apiResponse(req, 200, `{"result":[{"name":"policy-a"}],"nextPageToken":"next"}`), nil
				}
				return base.transport.RoundTrip(req)
			})
			kind := r.resourceKind(routePolicyType)
			request := contracts.InventoryRequest{ConnectionID: "connection", Source: productInventorySource, ResourceKind: &kind, Scope: asset.Scope{Kind: asset.ScopeRegion, NativeID: "us-central1"}, Limit: 1}
			batch, err := r.List(t.Context(), request)
			if err != nil || batch.Complete || batch.NextCursor == "" || len(batch.Items) != 1 {
				t.Fatal(batch, err)
			}
			if len(array(batch.Items[0].Normalized["bgpReferences"])) != 2 || firewallDigest(batch.Items[0].Normalized[routePolicyPeers]) != firewallDigest(review.Asset.Normalized[routePolicyPeers]) {
				t.Fatal("lost peer review")
			}
			switch mode {
			case "peer-order":
				slices.Reverse(array(f.parent["bgpPeers"]))
			case "peer-setting":
				object(array(f.parent["bgpPeers"])[0])["enable"] = "FALSE"
			case "policy-order":
				object(array(f.parent["bgpPeers"])[1])["importPolicies"] = []any{"last-policy", "policy-a", "first-policy"}
			case "malformed":
				f.parent["bgpPeers"] = nil
			}
			request.Cursor = batch.NextCursor
			batch, err = r.List(t.Context(), request)
			if mode == "unchanged" || mode == "peer-order" {
				if err != nil || !batch.Complete || pages != 2 {
					t.Fatal("stable cursor rejected", batch, err, pages)
				}
			} else if err == nil || pages != 1 {
				t.Fatal("changed peer snapshot continued stale cursor", err, pages)
			}
		})
	}
}

func TestRoutePolicyBGPRepeatedReferencesPreserveOtherOccurrences(t *testing.T) {
	r, request, f := routePolicyActionRuntime(t)
	f.parent["bgpPeers"] = []any{map[string]any{"name": "peer-a", "importPolicies": []any{"other-policy", "policy-a", "other-policy", "policy-a"}}}
	request.Asset.Normalized[routePolicyPeers], _ = routePolicyBGPPeers(f.parent)
	driver, err := r.ResolveAction(t.Context(), "connection", request.Asset)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = driver.Execute(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	if firewallDigest(object(f.patchPeers[0])["importPolicies"]) != firewallDigest([]any{"other-policy", "other-policy"}) {
		t.Fatal("rewrote other occurrences", f.patchPeers)
	}
}
