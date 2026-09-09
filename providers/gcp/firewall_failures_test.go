package gcp

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func firewallChildRequest(t *testing.T, s *firewallScenario, root string) (*Runtime, contracts.ActionRequest) {
	t.Helper()
	r, values, solved, request := firewallReviewed(t, s, root)
	return r, dataformRequest(t, solved, values, request.PrerequisiteDeletions[0].Asset)
}

func TestFirewallNativeChangesStopOldPlans(t *testing.T) {
	for _, root := range []string{firewallTestHierarchy, firewallTestGlobal, firewallTestRegional} {
		for _, test := range []struct {
			name   string
			change func(*firewallScenario, string)
		}{
			{"rules", func(s *firewallScenario, root string) {
				object(array(s.policies[root]["rules"])[0])["action"] = "allow"
			}},
			{"fingerprint_without_removal", func(s *firewallScenario, root string) { s.policies[root]["fingerprint"] = "changed" }},
			{"description", func(s *firewallScenario, root string) { s.policies[root]["description"] = "changed" }},
			{"policy_recreated", func(s *firewallScenario, root string) { s.policies[root]["id"] = "1009" }},
			{"policy_created_time", func(s *firewallScenario, root string) { s.policies[root]["creationTimestamp"] = "2026-01-03T00:00:00Z" }},
			{"new_association", func(s *firewallScenario, root string) {
				row := roundTripDataformJSON(t, object(array(s.policies[root]["associations"])[0]))
				row["name"] = "new-association"
				s.policies[root]["associations"] = append(array(s.policies[root]["associations"]), row)
			}},
			{"changed_association", func(s *firewallScenario, root string) {
				object(array(s.policies[root]["associations"])[0])["shortName"] = "changed"
			}},
			{"target_recreated_or_moved", func(s *firewallScenario, root string) {
				if root == firewallTestHierarchy {
					s.containers["folders/456"]["parent"] = "folders/789"
				} else {
					s.networks["projects/sample-project/global/networks/network-a"]["id"] = "9876"
				}
			}},
			{"target_unreadable", func(s *firewallScenario, root string) {
				s.hook = func(req *http.Request) (*http.Response, bool) {
					if strings.Contains(req.URL.Path, "/networks/") || req.URL.Path == "/v3/folders/456" {
						return dataformResponse(req, 403, map[string]any{}), true
					}
					return nil, false
				}
			}},
			{"policy_unreadable", func(s *firewallScenario, root string) {
				s.hook = func(req *http.Request) (*http.Response, bool) {
					if req.Method == "GET" && req.URL.Path == "/compute/v1/"+root {
						return dataformResponse(req, 403, map[string]any{}), true
					}
					return nil, false
				}
			}},
			{"association_unreadable", func(s *firewallScenario, root string) {
				s.hook = func(req *http.Request) (*http.Response, bool) {
					if req.Method == "GET" && strings.HasSuffix(req.URL.Path, "/getAssociation") {
						return dataformResponse(req, 403, map[string]any{}), true
					}
					return nil, false
				}
			}},
			{"association_404_but_inline_exists", func(s *firewallScenario, root string) {
				s.hook = func(req *http.Request) (*http.Response, bool) {
					if req.Method == "GET" && strings.HasSuffix(req.URL.Path, "/getAssociation") {
						return dataformResponse(req, 404, map[string]any{}), true
					}
					return nil, false
				}
			}},
		} {
			t.Run(root+"/"+test.name, func(t *testing.T) {
				s := newFirewallScenario()
				r, request := firewallChildRequest(t, s, root)
				driver, err := r.ResolveAction(context.Background(), "connection", request.Asset)
				if err != nil {
					t.Fatal(err)
				}
				test.change(s, root)
				if _, err := driver.Execute(context.Background(), request); err == nil {
					t.Fatal("changed native state permitted mutation")
				}
				if _, err := driver.Readback(context.Background(), request); err == nil {
					t.Fatal("changed native state passed readback")
				}
				if len(s.writes) != 0 {
					t.Fatal("unsafe mutation", s.writes)
				}
			})
		}
	}
}

func TestFirewallReviewedRequestCannotBeRebound(t *testing.T) {
	for _, test := range []struct {
		name   string
		root   bool
		change func(*contracts.ActionRequest)
	}{
		{"connection", false, func(r *contracts.ActionRequest) { r.Asset.Identity.ConnectionID = "other" }},
		{"partition", false, func(r *contracts.ActionRequest) { r.Asset.Identity.Partition = "other" }},
		{"asset_id", false, func(r *contracts.ActionRequest) { r.Asset.ID = "" }},
		{"resource", false, func(r *contracts.ActionRequest) { r.Asset.Identity.NativeID += "other" }},
		{"association_target", false, func(r *contracts.ActionRequest) { r.Asset.Normalized["attachmentTarget"] = "folders/789" }},
		{"snapshot", false, func(r *contracts.ActionRequest) { r.Asset.Normalized[firewallSnapshotKey] = "changed" }},
		{"configuration", false, func(r *contracts.ActionRequest) { r.Asset.Normalized[firewallProof] = "changed" }},
		{"parent_configuration", false, func(r *contracts.ActionRequest) { r.Asset.Normalized[firewallParentProof] = "changed" }},
		{"owner", false, func(r *contracts.ActionRequest) { r.Asset.Normalized[firewallOwnerName] = "folders/789" }},
		{"scope", false, func(r *contracts.ActionRequest) { r.Asset.Normalized[firewallScope] = "folders/456" }},
		{"parameters", false, func(r *contracts.ActionRequest) { r.Parameters = map[string]any{"force": true} }},
		{"unreviewed_impact", false, func(r *contracts.ActionRequest) {
			r.LifecycleImpacts = []contracts.ActionImpact{{Asset: r.Asset, Delete: true}}
		}},
		{"prerequisite_missing", true, func(r *contracts.ActionRequest) { r.PrerequisiteDeletions = r.PrerequisiteDeletions[:1] }},
		{"prerequisite_duplicate", true, func(r *contracts.ActionRequest) { r.PrerequisiteDeletions[1] = r.PrerequisiteDeletions[0] }},
		{"prerequisite_target", true, func(r *contracts.ActionRequest) {
			r.PrerequisiteDeletions[0].Asset.Normalized["attachmentTarget"] = "folders/789"
		}},
		{"prerequisite_retained", true, func(r *contracts.ActionRequest) { r.PrerequisiteDeletions[0].Delete = false }},
		{"prerequisite_controller", true, func(r *contracts.ActionRequest) { r.PrerequisiteDeletions[0].ControllerID = "other" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			s := newFirewallScenario()
			r, values, solved, request := firewallReviewed(t, s, firewallTestHierarchy)
			if !test.root {
				request = dataformRequest(t, solved, values, request.PrerequisiteDeletions[0].Asset)
			}
			driver, err := r.ResolveAction(context.Background(), "connection", request.Asset)
			if err != nil {
				t.Fatal(err)
			}
			request = roundTripDataformJSON(t, request)
			test.change(&request)
			before := len(s.calls)
			if _, err := driver.Execute(context.Background(), request); err == nil {
				t.Fatal("tampered request executed")
			}
			if _, err := driver.Wait(context.Background(), request, contracts.ActionResult{}); err == nil {
				t.Fatal("tampered request resumed")
			}
			if _, err := driver.Readback(context.Background(), request); err == nil {
				t.Fatal("tampered request read back as complete")
			}
			if len(s.writes) != 0 || len(s.calls) != before {
				t.Fatal("tampered reviewed request reached native API")
			}
		})
	}
}

func TestFirewallOperationAndPhaseFailures(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(map[string]any)
	}{
		{"foreign_target", func(d map[string]any) {
			d["targetLink"] = "https://www.googleapis.com/compute/v1/locations/global/firewallPolicies/9090"
		}},
		{"target_recreated", func(d map[string]any) { d["targetId"] = "1009" }},
		{"request_id", func(d map[string]any) { d["clientOperationId"] = "other" }},
		{"operation_type", func(d map[string]any) { d["operationType"] = "changed" }},
		{"operation_name", func(d map[string]any) { d["name"] = "different-operation" }},
		{"foreign_selflink", func(d map[string]any) {
			d["selfLink"] = "https://www.googleapis.com/compute/v1/projects/other/global/operations/operation-1"
		}},
		{"unknown_status", func(d map[string]any) { d["status"] = "CANCELLED" }},
		{"done_string", func(d map[string]any) { d["status"] = true }},
		{"error", func(d map[string]any) {
			d["error"] = map[string]any{"errors": []any{map[string]any{"code": "PERMISSION_DENIED", "message": "FIREWALL_PRIVATE_ERROR"}}}
		}},
		{"empty_error", func(d map[string]any) { d["error"] = map[string]any{} }},
		{"malformed_error", func(d map[string]any) { d["error"] = "FIREWALL_PRIVATE_ERROR" }},
		{"http_error", func(d map[string]any) { d["httpErrorStatusCode"] = float64(503) }},
		{"http_error_message", func(d map[string]any) { d["httpErrorMessage"] = "FIREWALL_PRIVATE_ERROR" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			s := newFirewallScenario()
			r, request := firewallChildRequest(t, s, firewallTestHierarchy)
			driver, err := r.ResolveAction(context.Background(), "connection", request.Asset)
			if err != nil {
				t.Fatal(err)
			}
			result, err := driver.Execute(context.Background(), request)
			if err != nil {
				t.Fatal(err)
			}
			s.finish(result, request)
			for _, op := range s.operations {
				test.change(op)
			}
			if wait, err := driver.Wait(context.Background(), request, roundTripDataformJSON(t, result)); err == nil || wait.Done {
				t.Fatalf("bad operation accepted: %+v %v", wait, err)
			}
			if len(s.writes) != 1 {
				t.Fatal("operation retry mutated API")
			}
		})
	}
	for _, test := range []struct {
		name   string
		change func(*contracts.ActionResult)
	}{
		{"url_host", func(r *contracts.ActionResult) {
			r.ProviderOperationID = strings.Replace(r.ProviderOperationID, "compute.googleapis.com", "example.com", 1)
		}},
		{"url_query", func(r *contracts.ActionResult) { r.ProviderOperationID += "?redirect=other" }},
		{"url_fragment", func(r *contracts.ActionResult) { r.ProviderOperationID += "#other" }},
		{"phase_resource", func(r *contracts.ActionResult) { r.Data["resource"] = "other" }},
		{"phase_snapshot", func(r *contracts.ActionResult) { r.Data["snapshot"] = "other" }},
		{"phase_review", func(r *contracts.ActionResult) { r.Data["review"] = "other" }},
		{"phase_request_id", func(r *contracts.ActionResult) { r.Data["request_id"] = "other" }},
		{"phase_extra", func(r *contracts.ActionResult) { r.Data["extra"] = true }},
		{"phase_missing", func(r *contracts.ActionResult) { r.Data = nil }},
	} {
		t.Run(test.name, func(t *testing.T) {
			s := newFirewallScenario()
			r, request := firewallChildRequest(t, s, firewallTestHierarchy)
			driver, err := r.ResolveAction(context.Background(), "connection", request.Asset)
			if err != nil {
				t.Fatal(err)
			}
			result, err := driver.Execute(context.Background(), request)
			if err != nil {
				t.Fatal(err)
			}
			result = roundTripDataformJSON(t, result)
			test.change(&result)
			before := len(s.calls)
			if _, err := driver.Wait(context.Background(), request, result); err == nil {
				t.Fatal("tampered phase accepted")
			}
			if len(s.calls) != before {
				t.Fatal("tampered phase reached API")
			}
		})
	}
}

func TestFirewallOperationExpiryAndIndependentAbsence(t *testing.T) {
	for _, root := range []string{firewallTestHierarchy, firewallTestGlobal, firewallTestRegional} {
		t.Run(root, func(t *testing.T) {
			s := newFirewallScenario()
			r, request := firewallChildRequest(t, s, root)
			driver, err := r.ResolveAction(context.Background(), "connection", request.Asset)
			if err != nil {
				t.Fatal(err)
			}
			result, err := driver.Execute(context.Background(), request)
			if err != nil {
				t.Fatal(err)
			}
			for _, op := range s.operations {
				op["status"] = "DONE"
			}
			if wait, err := driver.Wait(context.Background(), request, result); err != nil || wait.Done {
				t.Fatalf("DONE alone proved absence: %+v %v", wait, err)
			}
			s.operations = map[string]map[string]any{}
			if wait, err := driver.Wait(context.Background(), request, result); err != nil || wait.Done {
				t.Fatalf("expired LRO alone proved absence: %+v %v", wait, err)
			}
			s.finish(result, request)
			if wait, err := driver.Wait(context.Background(), request, result); err != nil || !wait.Done {
				t.Fatalf("expired LRO and native absence: %+v %v", wait, err)
			}
			// The target still exists; unlink must not remove its VPC/folder.
			if len(s.networks) != 2 || len(s.containers) != 3 {
				t.Fatal("unlink deleted its attachment target")
			}
			if root == firewallTestHierarchy {
				s.hook = func(req *http.Request) (*http.Response, bool) {
					if strings.HasSuffix(req.URL.Path, "/listAssociations") {
						return dataformResponse(req, 200, map[string]any{"associations": []any{map[string]any{"name": request.Asset.Normalized["name"], "firewallPolicyId": request.Asset.Normalized["firewallPolicyId"], "attachmentTarget": request.Asset.Normalized["attachmentTarget"]}}}), true
					}
					return nil, false
				}
				if _, err := driver.Readback(context.Background(), request); err == nil {
					t.Fatal("reverse association survived unlink")
				}
			}
		})
	}
}

func TestFirewallHierarchyScopeAndInventoryFailures(t *testing.T) {
	t.Run("optional_scope", func(t *testing.T) {
		s := newFirewallScenario()
		s.root = ""
		r := s.runtime(t)
		req := productRequest(r, firewallPolicyType, "global")
		req.Source = firewallInventorySource
		batch, err := r.List(context.Background(), req)
		if err != nil || !batch.Complete || len(batch.Items) != 0 || slices.ContainsFunc(s.calls, func(call string) bool {
			return strings.Contains(call, "/firewallPolicies") || strings.Contains(call, "/folders") || strings.Contains(call, "/organizations")
		}) {
			t.Fatalf("project implicitly authorized org policies: %+v %v", batch, err)
		}
	})
	t.Run("scope_rotation", func(t *testing.T) {
		s := newFirewallScenario()
		r, request := firewallChildRequest(t, s, firewallTestHierarchy)
		s.root = "folders/456"
		driver, err := r.ResolveAction(context.Background(), "connection", request.Asset)
		if err == nil {
			_, err = driver.Execute(context.Background(), request)
		}
		if err == nil || len(s.writes) != 0 {
			t.Fatal("credential rotation reused broader hierarchy scope")
		}
	})
	t.Run("cursor_change", func(t *testing.T) {
		s := newFirewallScenario()
		r := s.runtime(t)
		req := productRequest(r, firewallAssociationType, "global")
		req.Source = firewallInventorySource
		req.Limit = 1
		first, err := r.List(context.Background(), req)
		if err != nil || first.NextCursor == "" {
			t.Fatal(first, err)
		}
		req.Cursor = first.NextCursor
		s.containers["folders/789"]["parent"] = "folders/456"
		if _, err := r.List(context.Background(), req); err == nil {
			t.Fatal("cursor silently skipped moved folder associations")
		}
	})
	for _, test := range []struct {
		name string
		data map[string]any
	}{
		{"duplicate", map[string]any{}},
		{"null_items", map[string]any{"items": nil}},
		{"wrong_collection", map[string]any{"associations": []any{}}},
		{"not_array", map[string]any{"items": map[string]any{}}},
		{"not_object", map[string]any{"items": []any{"policy"}}},
		{"wrong_token", map[string]any{"nextPageToken": true}},
		{"repeated_token", map[string]any{"nextPageToken": "repeat"}},
		{"partial_error", map[string]any{"warning": map[string]any{"code": "PARTIAL_SUCCESS", "message": "partial"}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			s := newFirewallScenario()
			r := s.runtime(t)
			if test.name == "duplicate" {
				test.data["items"] = []any{s.policies[firewallTestHierarchy], s.policies[firewallTestHierarchy]}
			}
			s.hook = func(req *http.Request) (*http.Response, bool) {
				if req.URL.Path == "/compute/v1/locations/global/firewallPolicies" && req.URL.Query().Get("parentId") == "folders/456" {
					return dataformResponse(req, 200, test.data), true
				}
				return nil, false
			}
			req := productRequest(r, firewallPolicyType, "global")
			req.Source = firewallInventorySource
			if _, err := r.List(context.Background(), req); err == nil {
				t.Fatal("incomplete policy list accepted")
			}
		})
	}
}

func TestFirewallContributorRequiresExactVisibleAssociations(t *testing.T) {
	s := newFirewallScenario()
	r := s.runtime(t)
	values := s.inventory(t, r)
	contributor, err := r.ServiceLifecycle(context.Background(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	missing := slices.Clone(values)
	index := slices.IndexFunc(missing, func(value asset.Asset) bool { return value.Identity.NativeType == firewallAssociationType })
	removed := missing[index]
	missing = append(missing[:index], missing[index+1:]...)
	contribution, err := contributor.Contribute(context.Background(), "scope", missing)
	if err != nil || len(contribution.Unresolved) != 1 || contribution.Unresolved[0].NativeID != removed.Identity.NativeID {
		t.Fatalf("missing association omitted: %+v %v", contribution, err)
	}
	duplicate := append(slices.Clone(values), removed)
	if _, err := contributor.Contribute(context.Background(), "scope", duplicate); err == nil {
		t.Fatal("ambiguous association accepted")
	}
	contribution, err = contributor.Contribute(context.Background(), "scope", values)
	if err != nil {
		t.Fatal(err)
	}
	root := batchAsset(values, firewallTestHierarchy)
	result, err := plan.Solve(plan.Input{Assets: values, ResolvedAssetIDs: []asset.AssetID{root.ID}, Relationships: contribution.Relationships, LifecycleBindings: contribution.Bindings, RequestOptions: map[asset.AssetID]map[string]any{root.ID: {"retain_resources": []string{removed.Identity.NativeID}}}})
	if err == nil && len(result.Blockers) == 0 {
		t.Fatal("policy deletion allowed retention of a mandatory association")
	}
	if len(s.writes) != 0 {
		t.Fatal("planning mutated policy")
	}
}
