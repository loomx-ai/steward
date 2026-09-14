package gcp

import (
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

type routerCascadeFixture struct {
	*multiPolicyFixture
	sets                      map[string]map[string]any
	parentExists              bool
	routerDeletes, setDeletes int
	pending                   bool
}

func routerCascadeRuntime(t *testing.T) (*Runtime, []asset.Asset, *routerCascadeFixture) {
	base, requests, multi := multiPolicyRuntime(t)
	f := &routerCascadeFixture{multiPolicyFixture: multi, sets: map[string]map[string]any{"set-a": namedSetFixture("set-a")}, parentExists: true}
	object(array(f.parent["bgpPeers"])[0])["importPolicies"] = []any{"policy-a", "policy-b"}
	object(array(f.parent["bgpPeers"])[0])["exportPolicies"] = []any{"policy-b"}
	requests[0].Asset.Normalized[routePolicyPeers], _ = routePolicyBGPPeers(f.parent)
	object(array(f.policies["policy-b"]["terms"])[0])["match"] = map[string]any{"expression": `prefixSets("set-a").matches(destination)`}
	requests[1].Asset.Normalized = cloneParameters(f.policies["policy-b"])
	requests[1].Asset.Normalized[routePolicyRouterID] = "1001"
	requests[1].Asset.Normalized[routePolicyPeers], _ = routePolicyBGPPeers(f.parent)
	root := asset.Asset{ID: "router", Identity: asset.Identity{Provider: asset.ProviderGCP, ConnectionID: "connection", Partition: "gcp", NativeType: routerType, NativeID: "//compute.googleapis.com/projects/sample-project/regions/us-central1/routers/router-a"}, Normalized: cloneParameters(f.parent)}
	root.Normalized[routerReview] = routerConfiguration(f.parent, false)
	root.Normalized[routerBaseReview] = routerConfiguration(f.parent, true)
	values := []asset.Asset{root, requests[0].Asset, requests[1].Asset}
	for _, entry := range []struct {
		kind, name, collection, key string
		data                        map[string]any
	}{{cloudNatType, "nat-a", "nats", cloudNatRouterID, object(array(f.parent["nats"])[0])}, {namedSetType, "set-a", "namedSets", namedSetRouterID, f.sets["set-a"]}} {
		value := asset.Asset{ID: asset.AssetID(entry.name), Identity: root.Identity, Normalized: cloneParameters(entry.data)}
		value.Identity.NativeType = entry.kind
		value.Identity.NativeID += "/" + entry.collection + "/" + entry.name
		value.Normalized[entry.key] = "1001"
		if entry.kind == cloudNatType {
			value.Normalized[cloudNatReview] = cloudNatConfiguration(entry.data)
		}
		values = append(values, value)
	}
	for i := range values {
		values[i].Capabilities = asset.CapabilitySet{asset.CapabilityIndexed, asset.CapabilityActionable}
	}
	r := protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
		if req.Method == "GET" {
			if strings.HasSuffix(req.URL.Path, "/routers") && !f.parentExists {
				return apiResponse(req, 200, `{}`), nil
			}
			if strings.HasSuffix(req.URL.Path, "/routers/router-a") && !f.parentExists {
				return apiResponse(req, 404, `{}`), nil
			}
			if strings.HasSuffix(req.URL.Path, "/listNamedSets") {
				if len(f.sets) == 0 {
					return apiResponse(req, 200, `{}`), nil
				}
				return apiResponse(req, 200, `{"result":[{"name":"set-a"}]}`), nil
			}
			if strings.HasSuffix(req.URL.Path, "/getNamedSet") {
				value, ok := f.sets[req.URL.Query().Get("namedSet")]
				if !ok {
					return apiResponse(req, 404, `{}`), nil
				}
				return dataformResponse(req, 200, map[string]any{"resource": value}), nil
			}
		}
		kind, name := "", ""
		if req.Method == "DELETE" && strings.HasSuffix(req.URL.Path, "/routers/router-a") {
			if len(f.policies) != 0 || len(f.sets) != 0 {
				t.Fatal("Router deleted before native prerequisites", f.policies, f.sets)
			}
			f.routerDeletes++
			f.parentExists = false
			kind, name = "delete", "router-delete"
		} else if req.Method == "POST" && strings.HasSuffix(req.URL.Path, "/deleteNamedSet") {
			if f.policies["policy-b"] != nil {
				t.Fatal("set deleted before its referencing policy")
			}
			delete(f.sets, req.URL.Query().Get("namedSet"))
			f.setDeletes++
			kind, name = "deleteNamedSet", "set-delete"
		}
		if kind != "" {
			if req.Body != nil {
				body, err := io.ReadAll(req.Body)
				if err != nil || len(body) != 0 {
					t.Fatal("unexpected native delete body", string(body), err)
				}
			}
			if req.URL.Query().Get("requestId") == "" {
				t.Fatal("missing request UUID")
			}
			state := "DONE"
			if f.pending {
				state = "RUNNING"
			}
			op := map[string]any{"name": name, "status": state, "operationType": kind, "targetLink": f.parent["selfLink"], "targetId": "1001", "clientOperationId": req.URL.Query().Get("requestId")}
			f.operations[name] = op
			return dataformResponse(req, 200, op), nil
		}
		return base.transport.RoundTrip(req)
	})
	return r, roundTripDataformJSON(t, values), f
}

func routerCascadePlan(t *testing.T, r *Runtime, values []asset.Asset) plan.Result {
	t.Helper()
	hook, err := r.ServiceLifecycle(t.Context(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	contribution, err := hook.Contribute(t.Context(), "project", values)
	if err != nil || len(contribution.Bindings) != 4 || len(contribution.Unresolved) != 0 {
		t.Fatal(contribution, err)
	}
	for _, binding := range contribution.Bindings {
		if binding.ManagedAssetID == "nat-a" {
			if binding.CleanupPolicy != graph.CleanupDelegate || binding.Evidence[graph.LifecycleEvidenceControllerVerifiesManagedAbsence] != true || !binding.DirectCleanupAllowed {
				t.Fatal(binding)
			}
		} else if binding.CleanupPolicy != graph.CleanupDirect {
			t.Fatal(binding)
		}
	}
	// The native policy expression contributes the set dependency during real scans.
	contribution.Relationships = append(contribution.Relationships, graph.Relationship{SourceAssetID: "policy-b", TargetAssetID: "set-a", Type: graph.RelationshipDependsOn})
	solved, err := plan.Solve(plan.Input{CleanupTaskID: "router-cleanup", Assets: values, ResolvedAssetIDs: []asset.AssetID{"router"}, LifecycleBindings: contribution.Bindings, Relationships: contribution.Relationships})
	if err != nil || len(solved.Blockers) != 0 || len(solved.Steps) != 4 || len(solved.ImpactItems) != 1 {
		t.Fatal(solved, err)
	}
	return solved
}

func TestRouterCascadeNativePrerequisitesAndRestart(t *testing.T) {
	r, values, f := routerCascadeRuntime(t)
	solved := routerCascadePlan(t, r, values)
	rootRequest := dataformRequest(t, solved, values, values[0])
	rootDriver, err := r.ResolveAction(t.Context(), "connection", rootRequest.Asset)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rootDriver.Execute(t.Context(), rootRequest); err == nil || f.routerDeletes != 0 {
		t.Fatal("premature Router delete", err)
	}
	byID := map[asset.AssetID]asset.Asset{}
	for _, value := range values {
		byID[value.ID] = value
	}
	for _, step := range solved.Steps {
		request := dataformRequest(t, solved, values, byID[step.AssetID])
		driver, err := r.ResolveAction(t.Context(), "connection", request.Asset)
		if err != nil {
			t.Fatal(err)
		}
		result, err := driver.Execute(t.Context(), request)
		if err != nil {
			t.Fatal(step.AssetID, err)
		}
		done := false
		for range 4 {
			encoded, _ := json.Marshal(result)
			if err := json.Unmarshal(encoded, &result); err != nil {
				t.Fatal(err)
			}
			fresh := protocolRuntime(t, r.transport.RoundTrip)
			driver, err = fresh.ResolveAction(t.Context(), "connection", request.Asset)
			if err != nil {
				t.Fatal(err)
			}
			wait, err := driver.Wait(t.Context(), request, result)
			if err != nil {
				t.Fatal(step.AssetID, err)
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
			t.Fatal("unfinished native step", step)
		}
	}
	if f.routerDeletes != 1 || f.setDeletes != 1 || len(f.deletes) != 2 || len(f.patches) != 2 || f.parentExists {
		t.Fatal(f)
	}
	if len(array(f.parent["nats"])) != 1 {
		t.Fatal("NAT was separately patched", f.parent)
	}
}

func TestRouterCascadeDiscoveryRejectsDriftAndMissingCoverage(t *testing.T) {
	for _, mode := range []string{"missing-nat", "missing-policy", "missing-set", "nat-changed", "policy-changed", "peer-changed", "base-changed", "missing-proof", "vpn", "vlan", "partial", "bad-result", "bad-token", "duplicate", "new-policy"} {
		t.Run(mode, func(t *testing.T) {
			base, values, f := routerCascadeRuntime(t)
			switch mode {
			case "missing-nat":
				values = slices.DeleteFunc(values, func(v asset.Asset) bool { return v.ID == "nat-a" })
			case "missing-policy":
				values = slices.DeleteFunc(values, func(v asset.Asset) bool { return v.ID == "policy-a" })
			case "missing-set":
				values = slices.DeleteFunc(values, func(v asset.Asset) bool { return v.ID == "set-a" })
			case "nat-changed":
				object(array(f.parent["nats"])[0])["minPortsPerVm"] = 128
			case "policy-changed":
				f.policies["policy-a"]["fingerprint"] = "changed"
			case "peer-changed":
				object(array(f.parent["bgpPeers"])[0])["peerAsn"] = 65000
			case "base-changed":
				f.parent["description"] = "changed"
			case "missing-proof":
				delete(values[0].Normalized, routerReview)
			case "vpn", "vlan":
				field, collection := "linkedVpnTunnel", "vpnTunnels"
				if mode == "vlan" {
					field, collection = "linkedInterconnectAttachment", "interconnectAttachments"
				}
				f.parent["interfaces"] = []any{map[string]any{"name": "interface", field: "https://www.googleapis.com/compute/v1/projects/sample-project/regions/us-central1/" + collection + "/attachment-a"}}
				values[0].Normalized = cloneParameters(f.parent)
				values[0].Normalized[routerReview] = routerConfiguration(f.parent, false)
				values[0].Normalized[routerBaseReview] = routerConfiguration(f.parent, true)
			}
			lists := 0
			r := protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
				if req.Method == "GET" && strings.HasSuffix(req.URL.Path, "/listRoutePolicies") {
					lists++
					switch mode {
					case "partial":
						return apiResponse(req, 200, `{"warning":{"code":"PARTIAL_SUCCESS"}}`), nil
					case "bad-result":
						return apiResponse(req, 200, `{"result":null}`), nil
					case "bad-token":
						return apiResponse(req, 200, `{"nextPageToken":null}`), nil
					case "duplicate":
						return apiResponse(req, 200, `{"result":[{"name":"policy-a"},{"name":"policy-a"}]}`), nil
					case "new-policy":
						if lists > 1 {
							return apiResponse(req, 200, `{"result":[{"name":"policy-a"}]}`), nil
						}
					}
				}
				return base.transport.RoundTrip(req)
			})
			hook, err := r.ServiceLifecycle(t.Context(), "connection")
			if err != nil {
				t.Fatal(err)
			}
			contribution, err := hook.Contribute(t.Context(), "project", values)
			coverage := strings.HasPrefix(mode, "missing-") && mode != "missing-proof" || mode == "vpn" || mode == "vlan"
			if coverage {
				if err != nil || len(contribution.Unresolved) != 1 || !contribution.Unresolved[0].BlocksCleanup {
					t.Fatal(mode, contribution, err)
				}
			} else if err == nil {
				t.Fatal("drift accepted", mode, contribution)
			}
			if f.routerDeletes != 0 {
				t.Fatal("discovery mutated router")
			}
		})
	}
}

func prepareRouterCascadeDeletion(t *testing.T, r *Runtime, values []asset.Asset, f *routerCascadeFixture) contracts.ActionRequest {
	t.Helper()
	solved := routerCascadePlan(t, r, values)
	request := dataformRequest(t, solved, values, values[0])
	for name := range f.policies {
		_, peers, err := routePolicyBGPReview(map[string]any{routePolicyPeers: f.parent["bgpPeers"]}, name)
		if err != nil {
			t.Fatal(err)
		}
		f.parent["bgpPeers"] = peers
		delete(f.policies, name)
	}
	clear(f.sets)
	return request
}

func TestRouterCascadePreflightRejectsChangedImpactsAndConfiguration(t *testing.T) {
	for _, mode := range []string{"missing-impact", "retained", "foreign", "duplicate", "wrong-parent", "missing-review", "action", "parameters", "base", "peers", "nat", "new-nat", "removed-nat", "parent-absent", "recreated", "denied", "partial", "policy-present", "changed-after-read", "delete-404-live"} {
		t.Run(mode, func(t *testing.T) {
			base, values, f := routerCascadeRuntime(t)
			request := prepareRouterCascadeDeletion(t, base, values, f)
			switch mode {
			case "missing-impact":
				request.LifecycleImpacts = nil
			case "retained":
				request.LifecycleImpacts[0].Delete = false
			case "foreign":
				request.LifecycleImpacts[0].Asset.Identity.ConnectionID = "other"
			case "duplicate":
				request.PrerequisiteDeletions = append(request.PrerequisiteDeletions, request.PrerequisiteDeletions[0])
			case "wrong-parent":
				request.LifecycleImpacts[0].Asset.Normalized[cloudNatRouterID] = "2000"
			case "missing-review":
				delete(request.Asset.Normalized, routerBaseReview)
			case "action":
				request.Action = "update"
			case "parameters":
				request.Parameters = map[string]any{"force": true}
			case "base":
				f.parent["network"] = "changed"
			case "peers":
				object(array(f.parent["bgpPeers"])[0])["peerAsn"] = 65000
			case "nat":
				object(array(f.parent["nats"])[0])["minPortsPerVm"] = 256
			case "new-nat":
				f.parent["nats"] = append(array(f.parent["nats"]), cloudNatFixture("new-nat"))
			case "removed-nat":
				f.parent["nats"] = []any{}
			case "parent-absent":
				f.parentExists = false
			case "recreated":
				f.parent["id"] = "2000"
			case "partial":
				f.parent["warning"] = map[string]any{"code": "PARTIAL_SUCCESS"}
			case "policy-present":
				f.policies["policy-a"] = routePolicyFixture("policy-a")
			}
			reads := 0
			r := protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
				if req.Method == "GET" && strings.HasSuffix(req.URL.Path, "/routers/router-a") {
					reads++
					if mode == "denied" {
						return apiResponse(req, 403, `{}`), nil
					}
					if mode == "changed-after-read" && reads == 2 {
						f.parent["description"] = "changed-after-child-reads"
					}
				}
				if req.Method == "DELETE" && mode == "delete-404-live" {
					return apiResponse(req, 404, `{}`), nil
				}
				return base.transport.RoundTrip(req)
			})
			driver, err := r.ResolveAction(t.Context(), "connection", request.Asset)
			if err != nil {
				t.Fatal(err)
			}
			_, err = driver.Execute(t.Context(), request)
			allowed := mode == "removed-nat" || mode == "parent-absent"
			if allowed && err != nil || !allowed && (err == nil || f.routerDeletes != 0) {
				t.Fatal(mode, err, f.routerDeletes)
			}
			if mode == "parent-absent" && f.routerDeletes != 0 {
				t.Fatal("absent Router deleted again")
			}
		})
	}
}

func TestRouterCascadeOperationReceiptsAndVisibility(t *testing.T) {
	for _, mode := range []string{"pending", "visible", "expired", "failed", "target", "incarnation", "uuid", "type", "receipt", "review", "disappear-during-list", "disappear-final-read", "recreated-after-absence", "child-list-404-parent-live"} {
		t.Run(mode, func(t *testing.T) {
			base, values, f := routerCascadeRuntime(t)
			request := prepareRouterCascadeDeletion(t, base, values, f)
			waiting, parentReads := false, 0
			r := protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
				if waiting && mode == "recreated-after-absence" && strings.HasSuffix(req.URL.Path, "/routers/router-a") {
					parentReads++
					if parentReads == 2 {
						f.parentExists = true
						f.parent["id"] = "2000"
					}
				}
				if waiting && mode == "child-list-404-parent-live" && strings.HasSuffix(req.URL.Path, "/listRoutePolicies") {
					return apiResponse(req, 404, `{}`), nil
				}
				if waiting && mode == "disappear-during-list" && strings.HasSuffix(req.URL.Path, "/listRoutePolicies") {
					f.parentExists = false
					return apiResponse(req, 404, `{}`), nil
				}
				if waiting && mode == "disappear-final-read" && strings.HasSuffix(req.URL.Path, "/routers/router-a") {
					parentReads++
					if parentReads == 2 {
						f.parentExists = false
					}
				}
				if mode == "expired" && req.Method == "GET" && strings.Contains(req.URL.Path, "/operations/") {
					return apiResponse(req, 404, `{}`), nil
				}
				return base.transport.RoundTrip(req)
			})
			driver, err := r.ResolveAction(t.Context(), "connection", request.Asset)
			if err != nil {
				t.Fatal(err)
			}
			result, err := driver.Execute(t.Context(), request)
			if err != nil {
				t.Fatal(err)
			}
			op := f.operations["router-delete"]
			switch mode {
			case "pending":
				op["status"] = "RUNNING"
			case "visible":
				f.parentExists = true
			case "disappear-during-list", "disappear-final-read":
				f.parentExists = true
				waiting = true
			case "recreated-after-absence":
				waiting = true
			case "child-list-404-parent-live":
				waiting = true
				f.parentExists = true
			case "failed":
				op["error"] = map[string]any{"errors": []any{map[string]any{"code": "RESOURCE_IN_USE_BY_ANOTHER_RESOURCE"}}}
			case "target":
				op["targetLink"] = "https://www.googleapis.com/compute/v1/projects/sample-project/regions/us-central1/routers/other"
			case "incarnation":
				op["targetId"] = "2000"
			case "uuid":
				op["clientOperationId"] = "other-request"
			case "type":
				op["operationType"] = "patch"
			case "receipt":
				result.Data["configuration"] = "other-review"
			case "review":
				request.LifecycleImpacts = nil
			}
			encoded, _ := json.Marshal(result)
			if err := json.Unmarshal(encoded, &result); err != nil {
				t.Fatal(err)
			}
			fresh := protocolRuntime(t, r.transport.RoundTrip)
			driver, err = fresh.ResolveAction(t.Context(), "connection", request.Asset)
			if err != nil {
				t.Fatal(err)
			}
			wait, err := driver.Wait(t.Context(), request, result)
			switch mode {
			case "pending", "visible":
				if err != nil || wait.Done {
					t.Fatal(mode, wait, err)
				}
				op["status"] = "DONE"
				f.parentExists = false
				if wait, err = driver.Wait(t.Context(), request, result); err != nil || !wait.Done {
					t.Fatal(mode, wait, err)
				}
			case "expired", "disappear-during-list", "disappear-final-read":
				if err != nil || !wait.Done {
					t.Fatal(wait, err)
				}
			default:
				if err == nil || wait.Done {
					t.Fatal("invalid completion accepted", mode, wait, err)
				}
			}
			if f.routerDeletes != 1 {
				t.Fatal("wait repeated deletion", f.routerDeletes)
			}
		})
	}
}

func TestRouterCascadeNativeChildPagination(t *testing.T) {
	base, values, _ := routerCascadeRuntime(t)
	pages := 0
	r := protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
		if req.Method == "GET" && strings.HasSuffix(req.URL.Path, "/listRoutePolicies") {
			pages++
			if req.URL.Query().Get("maxResults") != "500" {
				t.Fatal(req.URL)
			}
			if req.URL.Query().Get("pageToken") == "next" {
				return apiResponse(req, 200, `{"result":[{"name":"policy-b"}]}`), nil
			}
			return apiResponse(req, 200, `{"result":[{"name":"policy-a"}],"nextPageToken":"next"}`), nil
		}
		return base.transport.RoundTrip(req)
	})
	routerCascadePlan(t, r, values)
	if pages != 4 {
		t.Fatal("did not verify both complete memberships", pages)
	}
}

func TestRouterCascadeCannotRetainChildrenButAllowsIndependentNat(t *testing.T) {
	r, values, _ := routerCascadeRuntime(t)
	hook, err := r.ServiceLifecycle(t.Context(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	contribution, err := hook.Contribute(t.Context(), "project", values)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"nat-a", "policy-a", "set-a"} {
		result, err := plan.Solve(plan.Input{Assets: values, ResolvedAssetIDs: []asset.AssetID{"router"}, LifecycleBindings: contribution.Bindings, Relationships: contribution.Relationships, RequestOptions: map[asset.AssetID]map[string]any{"router": {"retain_resources": []string{id}}}})
		if err != nil || len(result.Blockers) == 0 {
			t.Fatal("retained Router child accepted", id, result, err)
		}
	}
	result, err := plan.Solve(plan.Input{Assets: values, ResolvedAssetIDs: []asset.AssetID{"nat-a"}, LifecycleBindings: contribution.Bindings, Relationships: contribution.Relationships})
	if err != nil || len(result.Blockers) != 0 || len(result.Steps) != 1 || result.Steps[0].AssetID != "nat-a" {
		t.Fatal("independent NAT cleanup blocked", result, err)
	}
}
