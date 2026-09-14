package gcp

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

type multiPolicyFixture struct {
	parent     map[string]any
	policies   map[string]map[string]any
	operations map[string]map[string]any
	patches    [][]any
	deletes    []string
}

func multiPolicyRuntime(t *testing.T) (*Runtime, []contracts.ActionRequest, *multiPolicyFixture) {
	t.Helper()
	parentID := "//compute.googleapis.com/projects/sample-project/regions/us-central1/routers/router-a"
	f := &multiPolicyFixture{parent: cloudNatParent("/compute/v1/projects/sample-project/regions/us-central1/routers/router-a"), policies: map[string]map[string]any{}, operations: map[string]map[string]any{}}
	f.parent["bgpPeers"] = []any{map[string]any{"name": "peer-a", "peerAsn": 64513, "futureOption": "preserve", "importPolicies": []any{"policy-a", "policy-b", "keep"}, "exportPolicies": []any{"keep-export"}}}
	f.parent["nats"] = []any{cloudNatFixture("nat-a")}
	var requests []contracts.ActionRequest
	for _, name := range []string{"policy-a", "policy-b"} {
		policy := routePolicyFixture(name)
		f.policies[name] = policy
		normalized := cloneParameters(policy)
		normalized[routePolicyRouterID] = "1001"
		normalized[routePolicyPeers], _ = routePolicyBGPPeers(f.parent)
		requests = append(requests, contracts.ActionRequest{Asset: asset.Asset{ID: asset.AssetID(name), Identity: asset.Identity{Provider: asset.ProviderGCP, Partition: "gcp", ConnectionID: "connection", NativeType: routePolicyType, NativeID: parentID + "/routePolicies/" + name}, Normalized: normalized}, Action: "delete", IdempotencyKey: "delete-" + name})
	}
	r := protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
		if req.Method == "GET" {
			if strings.HasSuffix(req.URL.Path, "/routers") {
				return dataformResponse(req, 200, map[string]any{"items": []any{f.parent}}), nil
			}
			if strings.HasSuffix(req.URL.Path, "/listRoutePolicies") {
				var values []any
				for _, name := range []string{"policy-a", "policy-b"} {
					if f.policies[name] != nil {
						values = append(values, map[string]any{"name": name})
					}
				}
				data := map[string]any{}
				if len(values) != 0 {
					data["result"] = values
				}
				return dataformResponse(req, 200, data), nil
			}
			if strings.HasSuffix(req.URL.Path, "/routers/router-a") {
				return dataformResponse(req, 200, f.parent), nil
			}
			if strings.HasSuffix(req.URL.Path, "/getRoutePolicy") {
				data, ok := f.policies[req.URL.Query().Get("policy")]
				if !ok {
					return apiResponse(req, 404, `{}`), nil
				}
				return dataformResponse(req, 200, map[string]any{"resource": data}), nil
			}
			if strings.Contains(req.URL.Path, "/operations/") {
				return dataformResponse(req, 200, f.operations[last(req.URL.Path)]), nil
			}
		}
		kind := ""
		if req.Method == "PATCH" && strings.HasSuffix(req.URL.Path, "/routers/router-a") {
			var body map[string]any
			if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if len(body) != 1 || body["bgpPeers"] == nil {
				t.Fatal("unexpected Router mutation", body)
			}
			f.parent["bgpPeers"] = body["bgpPeers"]
			f.patches = append(f.patches, array(body["bgpPeers"]))
			kind = "patch"
		} else if req.Method == "POST" && strings.HasSuffix(req.URL.Path, "/deleteRoutePolicy") {
			name := req.URL.Query().Get("policy")
			if f.policies[name] == nil {
				t.Fatal("duplicate/foreign deletion", name)
			}
			delete(f.policies, name)
			f.deletes = append(f.deletes, name)
			kind = "deleteRoutePolicy"
		} else {
			t.Fatal("unexpected native request", req.Method, req.URL)
		}
		if req.URL.Query().Get("requestId") == "" {
			t.Fatal("missing UUID")
		}
		name := fmt.Sprintf("operation-%d", len(f.operations)+1)
		operation := map[string]any{"name": name, "operationType": kind, "status": "DONE", "targetLink": f.parent["selfLink"], "targetId": "1001", "clientOperationId": req.URL.Query().Get("requestId")}
		f.operations[name] = operation
		return dataformResponse(req, 200, operation), nil
	})
	return r, requests, f
}

func TestRoutePolicySequentialDeletesPreserveEarlierRemoval(t *testing.T) {
	r, requests, f := multiPolicyRuntime(t)
	original := firewallDigest(f.parent["nats"])
	for _, request := range requests {
		// Both requests retain the original scan; each stage recreates the runtime.
		driver, err := r.ResolveAction(t.Context(), "connection", request.Asset)
		if err != nil {
			t.Fatal(err)
		}
		result, err := driver.Execute(t.Context(), request)
		if err != nil {
			t.Fatal(request.Asset.ID, err)
		}
		done := false
		for i := 0; i < 3; i++ {
			encoded, _ := json.Marshal(result)
			if json.Unmarshal(encoded, &result) != nil {
				t.Fatal("receipt")
			}
			fresh := protocolRuntime(t, r.transport.RoundTrip)
			driver, err = fresh.ResolveAction(t.Context(), "connection", request.Asset)
			if err != nil {
				t.Fatal(err)
			}
			wait, err := driver.Wait(t.Context(), request, result)
			if err != nil {
				t.Fatal(err)
			}
			if wait.Data != nil {
				result.Data = wait.Data
			}
			if wait.Done {
				done = true
				break
			}
		}
		if !done {
			t.Fatal("policy did not finish", request.Asset.ID)
		}
	}
	if len(f.patches) != 2 || len(f.deletes) != 2 || len(f.policies) != 0 {
		t.Fatal(f.patches, f.deletes, f.policies)
	}
	peer := object(f.patches[1][0])
	if firewallDigest(peer["importPolicies"]) != firewallDigest([]any{"keep"}) || firewallDigest(peer["exportPolicies"]) != firewallDigest([]any{"keep-export"}) || peer["futureOption"] != "preserve" || firewallDigest(f.parent["nats"]) != original {
		t.Fatal("sibling removal or unrelated configuration overwritten", peer)
	}
}

func TestRoutePolicySiblingRemovalRequiresNativeAbsenceAndStablePeers(t *testing.T) {
	for _, mode := range []string{"absent", "live", "denied", "malformed", "parent-missing", "parent-recreated", "parent-partial", "changed-during-read", "new-peer", "peer-setting", "reorder", "new-policy", "partial-target-detach"} {
		t.Run(mode, func(t *testing.T) {
			base, requests, f := multiPolicyRuntime(t)
			request := requests[1]
			_, peers, err := routePolicyBGPReview(map[string]any{routePolicyPeers: f.parent["bgpPeers"]}, "policy-a")
			if err != nil {
				t.Fatal(err)
			}
			f.parent["bgpPeers"] = peers
			if mode != "live" {
				delete(f.policies, "policy-a")
			}
			switch mode {
			case "new-peer":
				f.parent["bgpPeers"] = append(peers, map[string]any{"name": "new-peer"})
			case "peer-setting":
				object(peers[0])["peerAsn"] = 64520
			case "reorder":
				object(peers[0])["importPolicies"] = []any{"keep", "policy-b"}
			case "new-policy":
				object(peers[0])["importPolicies"] = []any{"policy-b", "keep", "new"}
			case "partial-target-detach":
				// One target reference was already removed; the other must still be detached.
				object(array(request.Asset.Normalized[routePolicyPeers])[0])["exportPolicies"] = []any{"policy-b", "keep-export"}
			}
			missingParent := false
			r := protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
				if missingParent && strings.HasSuffix(req.URL.Path, "/routers/router-a") {
					return apiResponse(req, 404, `{}`), nil
				}
				if req.Method == "GET" && strings.HasSuffix(req.URL.Path, "/getRoutePolicy") && req.URL.Query().Get("policy") == "policy-a" {
					switch mode {
					case "denied":
						return apiResponse(req, 403, `{}`), nil
					case "malformed":
						return apiResponse(req, 200, `{"resource":null}`), nil
					case "parent-missing":
						missingParent = true
					case "parent-recreated":
						f.parent["id"] = "2000"
					case "parent-partial":
						f.parent["warning"] = map[string]any{"code": "PARTIAL_SUCCESS"}
					case "changed-during-read":
						object(array(f.parent["bgpPeers"])[0])["peerAsn"] = 64520
					}
				}
				return base.transport.RoundTrip(req)
			})
			driver, err := r.ResolveAction(t.Context(), "connection", request.Asset)
			if err != nil {
				t.Fatal(err)
			}
			_, err = driver.Execute(t.Context(), request)
			allowed := mode == "absent" || mode == "partial-target-detach"
			if allowed && (err != nil || len(f.patches) != 1) || !allowed && (err == nil || len(f.patches) != 0 || len(f.deletes) != 0) {
				t.Fatal(mode, err, f.patches, f.deletes)
			}
		})
	}
}

func TestRoutePolicyLastReadPreservesNewlyCompletedSiblingDeletion(t *testing.T) {
	base, requests, f := multiPolicyRuntime(t)
	request := requests[1]
	parentReads := 0
	r := protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
		if req.Method == "GET" && strings.HasSuffix(req.URL.Path, "/routers/router-a") {
			parentReads++
			// Execute's preflight and initial attached check saw the original peers.
			// The final native GET must build PATCH from the now-current array.
			if parentReads == 4 {
				_, peers, err := routePolicyBGPReview(map[string]any{routePolicyPeers: f.parent["bgpPeers"]}, "policy-a")
				if err != nil {
					t.Fatal(err)
				}
				f.parent["bgpPeers"] = peers
				delete(f.policies, "policy-a")
			}
		}
		return base.transport.RoundTrip(req)
	})
	driver, err := r.ResolveAction(t.Context(), "connection", request.Asset)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := driver.Execute(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	if len(f.patches) != 1 || firewallDigest(object(f.patches[0][0])["importPolicies"]) != firewallDigest([]any{"keep"}) {
		t.Fatal("PATCH resurrected sibling reference", parentReads, f.patches)
	}
}
