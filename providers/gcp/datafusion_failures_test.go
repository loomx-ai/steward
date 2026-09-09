package gcp

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func TestDataFusionInventoryRejectsIncompleteAndUnstableResponses(t *testing.T) {
	for _, mode := range []string{"permission", "locations_permission", "partial", "unreachable", "error", "array_shape", "duplicate", "missing_name", "missing_creation", "wrong_collection", "foreign_project", "foreign_region", "wrong_host", "bad_url", "detail_identity", "detail_configuration", "detail_error", "token_type", "token_cycle", "namespace_permission", "namespace_policy_missing", "namespace_policy_error", "namespace_policy_code_type", "namespace_policy_status_shape", "namespace_policy_shape", "namespace_parent", "namespace_duplicate", "namespace_changed", "parent_recreated", "invalid_dependency"} {
		t.Run(mode, func(t *testing.T) {
			s := newFusionScenario(t)
			r := protocolRuntime(t, s.transport(t))
			kind := fusionInstanceType
			if strings.HasPrefix(mode, "namespace_") || mode == "parent_recreated" {
				kind = fusionNamespaceType
			}
			parentReads, namespaceReads := 0, 0
			s.handle = func(req *http.Request) (*http.Response, bool) {
				if req.Method != "GET" {
					return nil, false
				}
				switch req.URL.Path {
				case "/v1/projects/sample-project/locations":
					if mode == "locations_permission" {
						return dataformResponse(req, 403, map[string]any{}), true
					}
				case "/v1/" + fusionTestInstance:
					parentReads++
					data := roundTripDataformJSON(t, s.resources[fusionTestInstance])
					switch mode {
					case "detail_identity":
						data["name"] = fusionTestEurope
					case "detail_configuration":
						data["options"] = map[string]any{"custom": "changed"}
					case "detail_error":
						data["error"] = map[string]any{"code": 7}
					case "parent_recreated":
						if parentReads > 1 {
							data["createTime"] = "2026-08-02T00:00:00Z"
						}
					default:
						return nil, false
					}
					return dataformResponse(req, 200, data), true
				case "/v1beta1/" + fusionTestInstance + "/namespaces":
					namespaceReads++
					namespace := roundTripDataformJSON(t, s.resources[fusionTestNS])
					data := map[string]any{"namespaces": []any{namespace}}
					switch mode {
					case "namespace_permission":
						return dataformResponse(req, 403, map[string]any{}), true
					case "namespace_policy_missing":
						delete(namespace, "iamPolicy")
					case "namespace_policy_error":
						object(namespace["iamPolicy"])["status"] = map[string]any{"code": 7, "message": "FUSION_PRIVATE_ERROR"}
					case "namespace_policy_code_type":
						object(namespace["iamPolicy"])["status"] = map[string]any{"code": "0"}
					case "namespace_policy_status_shape":
						object(namespace["iamPolicy"])["status"] = "failed"
					case "namespace_policy_shape":
						object(namespace["iamPolicy"])["policy"] = "invalid"
					case "namespace_parent":
						namespace["name"] = fusionTestEurope + "/namespaces/default"
					case "namespace_duplicate":
						data["namespaces"] = []any{namespace, namespace}
					case "namespace_changed":
						if namespaceReads > 1 {
							object(object(namespace["iamPolicy"])["policy"])["etag"] = "changed"
						}
					default:
						return nil, false
					}
					return dataformResponse(req, 200, data), true
				case "/v1/" + fusionTestParent + "/instances":
					instance := roundTripDataformJSON(t, s.resources[fusionTestInstance])
					data := map[string]any{"instances": []any{instance}}
					switch mode {
					case "permission":
						return dataformResponse(req, 403, map[string]any{}), true
					case "partial":
						return dataformResponse(req, 206, data), true
					case "unreachable":
						data["unreachable"] = []any{"us-central1"}
					case "error":
						data["error"] = map[string]any{"code": 7}
					case "array_shape":
						data["instances"] = map[string]any{}
					case "duplicate":
						data["instances"] = []any{instance, instance}
					case "missing_name":
						delete(instance, "name")
					case "missing_creation":
						delete(instance, "createTime")
					case "wrong_collection":
						instance["name"] = fusionTestDNS
					case "foreign_project":
						instance["name"] = strings.Replace(fusionTestInstance, "sample-project", "foreign-project", 1)
					case "foreign_region":
						instance["name"] = fusionTestEurope
					case "wrong_host":
						instance["name"] = "https://foreign.example/v1/" + fusionTestInstance
					case "bad_url":
						instance["name"] = "https://datafusion.googleapis.com/v1/" + fusionTestInstance + "?arbitrary=query"
					case "token_type":
						data["nextPageToken"] = 7
					case "token_cycle":
						data["nextPageToken"] = "loop"
					case "invalid_dependency":
						object(instance["networkConfig"])["network"] = "https://foreign.example/network"
						s.resources[fusionTestInstance] = instance
					default:
						return nil, false
					}
					return dataformResponse(req, 200, data), true
				}
				return nil, false
			}
			req := productRequest(r, kind, "us-central1")
			failed := false
			for i := 0; i < 12; i++ {
				batch, err := r.List(context.Background(), req)
				if err != nil {
					failed = true
					break
				}
				if batch.Complete {
					break
				}
				req.Cursor = batch.NextCursor
			}
			if !failed || len(s.mutations) != 0 {
				t.Fatalf("accepted Data Fusion inventory fault %s", mode)
			}
		})
	}
}

func TestDataFusionPreflightRejectsChangedIdentityConfigurationAndReview(t *testing.T) {
	for _, mode := range []string{"root_recreated", "root_configuration", "root_identity", "protected", "unknown_state", "beta_state", "child_configuration", "namespace_policy", "unreviewed_dns", "unreviewed_namespace", "missing_impact", "retained_impact", "duplicate_impact", "impact_type", "impact_id", "impact_controller", "impact_connection", "impact_partition", "impact_provider", "impact_proof", "impact_parent", "impact_name", "impact_asset_id", "request_proof", "request_creation", "request_connection", "request_partition", "request_provider", "request_type", "request_id", "request_asset_id", "prerequisite", "parent_permission", "child_permission", "child_collection_404", "child_membership_changes", "parent_changes_during_children"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			s := newFusionScenario(t)
			r, _, _, request := fusionReviewed(t, s, fusionTestInstance)
			driver, err := r.ResolveAction(ctx, "connection", request.Asset)
			if err != nil {
				t.Fatal(err)
			}
			impact := &request.LifecycleImpacts[0]
			switch mode {
			case "root_recreated":
				s.resources[fusionTestInstance]["createTime"] = "2026-08-02T00:00:00Z"
			case "root_configuration":
				s.resources[fusionTestInstance]["options"] = map[string]any{"custom": "changed"}
			case "root_identity":
				s.resources[fusionTestInstance]["name"] = fusionTestEurope
			case "protected":
				object(s.resources[fusionTestInstance]["labels"])["protected"] = "true"
			case "unknown_state", "beta_state":
				state := "UNKNOWN"
				if mode == "beta_state" {
					state = "RUNNING"
				}
				s.resources[fusionTestInstance]["state"] = state
			case "child_configuration":
				s.resources[fusionTestDNS]["targetNetwork"] = "changed"
			case "namespace_policy":
				object(object(s.resources[fusionTestNS]["iamPolicy"])["policy"])["etag"] = "changed"
			case "unreviewed_dns", "unreviewed_namespace":
				original := fusionTestDNS
				if mode == "unreviewed_namespace" {
					original = fusionTestNS
				}
				data := roundTripDataformJSON(t, s.resources[original])
				data["name"] = original + "-new"
				s.resources[text(data["name"])] = data
			case "missing_impact":
				request.LifecycleImpacts = request.LifecycleImpacts[1:]
			case "retained_impact":
				impact.Delete = false
			case "duplicate_impact":
				request.LifecycleImpacts = append(request.LifecycleImpacts, *impact)
			case "impact_type":
				impact.Asset.Identity.NativeType = fusionInstanceType
			case "impact_id":
				impact.Asset.Identity.NativeID = "//datafusion.googleapis.com/" + fusionTestEurope + "/dnsPeerings/private"
			case "impact_controller":
				impact.ControllerID = "foreign"
			case "impact_connection":
				impact.Asset.Identity.ConnectionID = "foreign"
			case "impact_partition":
				impact.Asset.Identity.Partition = "foreign"
			case "impact_provider":
				impact.Asset.Identity.Provider = asset.ProviderAzure
			case "impact_proof":
				delete(impact.Asset.Normalized, fusionProof)
			case "impact_parent":
				impact.Asset.Normalized[fusionParentProof] = "foreign"
			case "impact_name":
				delete(impact.Asset.Normalized, "name")
			case "impact_asset_id":
				impact.Asset.ID = request.Asset.ID
			case "request_proof":
				delete(request.Asset.Normalized, fusionProof)
			case "request_creation":
				delete(request.Asset.Normalized, "createTime")
			case "request_connection":
				request.Asset.Identity.ConnectionID = "foreign"
			case "request_partition":
				request.Asset.Identity.Partition = "foreign"
			case "request_provider":
				request.Asset.Identity.Provider = asset.ProviderAzure
			case "request_type":
				request.Asset.Identity.NativeType = fusionDNSType
			case "request_id":
				request.Asset.Identity.NativeID = "//datafusion.googleapis.com/" + fusionTestEurope
			case "request_asset_id":
				request.Asset.ID = ""
			case "prerequisite":
				request.PrerequisiteDeletions = []contracts.ActionImpact{*impact}
			case "parent_permission", "child_permission", "child_collection_404":
				path, status := "/v1/"+fusionTestInstance, 403
				if mode != "parent_permission" {
					path += "/dnsPeerings"
				}
				if mode == "child_collection_404" {
					status = 404
				}
				s.handle = func(req *http.Request) (*http.Response, bool) {
					if req.URL.Path == path {
						return dataformResponse(req, status, map[string]any{}), true
					}
					return nil, false
				}
			case "child_membership_changes", "parent_changes_during_children":
				reads := 0
				s.handle = func(req *http.Request) (*http.Response, bool) {
					if req.URL.Path == "/v1/"+fusionTestInstance+"/dnsPeerings" {
						reads++
						if reads == 2 {
							if mode == "child_membership_changes" {
								delete(s.resources, fusionTestDNS)
							} else {
								s.resources[fusionTestInstance]["createTime"] = "2026-08-02T00:00:00Z"
							}
						}
					}
					return nil, false
				}
			}
			if check, err := driver.Preflight(ctx, request); err == nil && check.Allowed {
				t.Fatalf("accepted Data Fusion preflight fault %s: %+v", mode, check)
			} else if mode == "child_collection_404" {
				var call *contracts.ProviderCallError
				if !errors.As(err, &call) || call.Provider.Category != execution.ErrorDependencyViolation {
					t.Fatalf("collection 404 became target absence: %v", err)
				}
			}
			// A disappearing child is allowed on a later retry after a fresh,
			// complete snapshot proves it gone; other faults must still prevent writes.
			if mode != "child_membership_changes" {
				if _, err := driver.Execute(ctx, request); err == nil || len(s.mutations) != 0 {
					t.Fatalf("wrote after Data Fusion preflight fault %s: %v %+v", mode, err, s.mutations)
				}
			}
		})
	}
}

func TestDataFusionWaitRejectsChangedOperationsAndPersistedContext(t *testing.T) {
	for _, mode := range []string{"operation_host", "operation_region", "operation_project", "operation_version", "operation_query", "operation_userinfo", "operation_fragment", "operation_name", "done_type", "error_shape", "operation_error", "metadata_type", "metadata_version", "metadata_verb", "metadata_target", "metadata_target_type", "metadata_shape", "cancelled", "cancelled_type", "phase", "phase_resource", "phase_configuration", "phase_parent", "phase_review", "phase_operation", "phase_initial", "changed_impacts", "missing_proof", "root_recreated", "child_reconfigured", "operation_permission", "child_permission"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			s := newFusionScenario(t)
			r, _, _, request := fusionReviewed(t, s, fusionTestInstance)
			driver, _ := r.ResolveAction(ctx, "connection", request.Asset)
			result, err := driver.Execute(ctx, request)
			if err != nil {
				t.Fatal(err)
			}
			op := s.operations[fusionTestParent+"/operations/delete-1"]
			switch mode {
			case "operation_host", "operation_region", "operation_project", "operation_version", "operation_query", "operation_userinfo", "operation_fragment":
				value := result.ProviderOperationID
				switch mode {
				case "operation_host":
					value = strings.Replace(value, "datafusion.googleapis.com", "foreign.example", 1)
				case "operation_region":
					value = strings.Replace(value, "us-central1", "europe-west1", 1)
				case "operation_project":
					value = strings.Replace(value, "sample-project", "foreign-project", 1)
				case "operation_version":
					value = strings.Replace(value, "/v1/", "/v1beta1/", 1)
				case "operation_query":
					value += "?extra=1"
				case "operation_userinfo":
					value = strings.Replace(value, "https://", "https://user@", 1)
				case "operation_fragment":
					value += "#fragment"
				}
				result.ProviderOperationID, result.Data["initial_operation"], result.Data["operation"] = value, value, value
			case "operation_name":
				op["name"] = fusionTestParent + "/operations/foreign"
			case "done_type":
				op["done"] = "true"
			case "error_shape":
				op["error"] = map[string]any{}
			case "operation_error":
				op["error"] = map[string]any{"code": 7, "message": "FUSION_PRIVATE_ERROR"}
			case "metadata_type":
				object(op["metadata"])["@type"] = "type.googleapis.com/foreign"
			case "metadata_version":
				object(op["metadata"])["apiVersion"] = "v1beta1"
			case "metadata_verb":
				object(op["metadata"])["verb"] = "update"
			case "metadata_target":
				object(op["metadata"])["target"] = fusionTestEurope
			case "metadata_target_type":
				object(op["metadata"])["target"] = 7
			case "metadata_shape":
				op["metadata"] = "invalid"
			case "cancelled":
				object(op["metadata"])["requestedCancellation"] = true
			case "cancelled_type":
				object(op["metadata"])["requestedCancellation"] = "false"
			case "phase":
				result.Data["phase"] = "delete"
			case "phase_resource":
				result.Data["resource"] = fusionTestEurope
			case "phase_configuration":
				result.Data["configuration"] = "foreign"
			case "phase_parent":
				result.Data["parent_configuration"] = "foreign"
			case "phase_review":
				result.Data["review"] = "foreign"
			case "phase_operation":
				result.Data["operation"] = 7
			case "phase_initial":
				result.Data["initial_operation"] = "foreign"
			case "changed_impacts":
				request.LifecycleImpacts = request.LifecycleImpacts[1:]
			case "missing_proof":
				delete(request.Asset.Normalized, fusionProof)
			case "root_recreated":
				s.resources[fusionTestInstance]["createTime"] = "2026-08-02T00:00:00Z"
			case "child_reconfigured":
				s.resources[fusionTestDNS]["domain"] = "new.example."
			case "operation_permission", "child_permission":
				path := "/v1/" + fusionTestParent + "/operations/delete-1"
				if mode == "child_permission" {
					path = "/v1beta1/" + fusionTestInstance + "/namespaces"
				}
				s.handle = func(req *http.Request) (*http.Response, bool) {
					if req.URL.Path == path {
						return dataformResponse(req, 403, map[string]any{}), true
					}
					return nil, false
				}
			}
			if wait, err := driver.Wait(ctx, request, result); err == nil || wait.Done || len(s.mutations) != 1 {
				t.Fatalf("accepted Data Fusion wait fault %s: %+v %v", mode, wait, err)
			}
		})
	}
}

func TestDataFusionDNSRequiresUnchangedParentAndCompleteList(t *testing.T) {
	for _, mode := range []string{"parent_proof", "parent_recreated", "dns_reconfigured", "dns_changes_between_reads", "parent_permission", "list_permission", "list_404", "list_partial", "list_duplicate", "list_foreign_parent", "list_token_cycle", "request_identity", "request_impact", "delete_response", "persisted_operation_type", "dns_recreated_after_delete"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			s := newFusionScenario(t)
			r, _, _, request := fusionReviewed(t, s, fusionTestDNS)
			driver, _ := r.ResolveAction(ctx, "connection", request.Asset)
			switch mode {
			case "parent_proof":
				delete(request.Asset.Normalized, fusionParentProof)
			case "parent_recreated":
				s.resources[fusionTestInstance]["createTime"] = "2026-08-02T00:00:00Z"
			case "dns_reconfigured":
				s.resources[fusionTestDNS]["targetProject"] = "new-project"
			case "dns_changes_between_reads":
				reads := 0
				s.handle = func(req *http.Request) (*http.Response, bool) {
					if req.URL.Path == "/v1/"+fusionTestInstance+"/dnsPeerings" {
						reads++
						if reads == 1 {
							response := dataformResponse(req, 200, map[string]any{"dnsPeerings": []any{s.resources[fusionTestDNS]}})
							s.resources[fusionTestDNS]["domain"] = "changed.example."
							return response, true
						}
					}
					return nil, false
				}
			case "request_identity":
				request.Asset.Identity.NativeID += "-other"
			case "request_impact":
				request.LifecycleImpacts = []contracts.ActionImpact{{Asset: request.Asset, ControllerID: request.Asset.ID, Delete: true}}
			case "delete_response":
				s.handle = func(req *http.Request) (*http.Response, bool) {
					if req.Method == "DELETE" {
						return dataformResponse(req, 200, map[string]any{"name": fusionTestParent + "/operations/invented"}), true
					}
					return nil, false
				}
			case "persisted_operation_type", "dns_recreated_after_delete":
				old := roundTripDataformJSON(t, s.resources[fusionTestDNS])
				result, err := driver.Execute(ctx, request)
				if err != nil {
					t.Fatal(err)
				}
				if mode == "persisted_operation_type" {
					result.Data["operation"] = 7
				} else {
					old["domain"] = "new.example."
					s.resources[fusionTestDNS] = old
				}
				if wait, err := driver.Wait(ctx, request, result); err == nil || wait.Done {
					t.Fatalf("DNS wait accepted %s: %+v %v", mode, wait, err)
				}
				return
			default:
				s.handle = func(req *http.Request) (*http.Response, bool) {
					if req.URL.Path == "/v1/"+fusionTestInstance && mode == "parent_permission" {
						return dataformResponse(req, 403, map[string]any{}), true
					}
					if req.URL.Path != "/v1/"+fusionTestInstance+"/dnsPeerings" {
						return nil, false
					}
					dns := roundTripDataformJSON(t, s.resources[fusionTestDNS])
					data := map[string]any{"dnsPeerings": []any{dns}}
					switch mode {
					case "list_permission":
						return dataformResponse(req, 403, map[string]any{}), true
					case "list_404":
						return dataformResponse(req, 404, map[string]any{}), true
					case "list_partial":
						return dataformResponse(req, 206, data), true
					case "list_duplicate":
						data["dnsPeerings"] = []any{dns, dns}
					case "list_foreign_parent":
						dns["name"] = fusionTestEurope + "/dnsPeerings/private"
					case "list_token_cycle":
						data["nextPageToken"] = "loop"
					default:
						return nil, false
					}
					return dataformResponse(req, 200, data), true
				}
			}
			if _, err := driver.Execute(ctx, request); err == nil || len(s.mutations) != 0 {
				t.Fatalf("DNS delete accepted %s: %v %+v", mode, err, s.mutations)
			}
		})
	}
}

func TestDataFusionPreflightRechecksInitialParentAbsence(t *testing.T) {
	for _, target := range []string{fusionTestInstance, fusionTestDNS} {
		for _, changed := range []bool{false, true} {
			t.Run(last(target)+"/changed="+map[bool]string{true: "yes", false: "no"}[changed], func(t *testing.T) {
				ctx := context.Background()
				s := newFusionScenario(t)
				r, _, _, request := fusionReviewed(t, s, target)
				driver, _ := r.ResolveAction(ctx, "connection", request.Asset)
				reads := 0
				s.handle = func(req *http.Request) (*http.Response, bool) {
					if req.URL.Path == "/v1/"+fusionTestInstance {
						reads++
						if reads == 1 {
							return dataformResponse(req, 404, map[string]any{}), true
						}
						if changed {
							s.resources[fusionTestInstance]["createTime"] = "2026-08-02T00:00:00Z"
						}
					}
					return nil, false
				}
				if check, err := driver.Preflight(ctx, request); err == nil || check.Absent || check.Allowed || len(s.mutations) != 0 {
					t.Fatalf("initial parent 404 hid later visibility: %+v %v", check, err)
				}
			})
		}
	}
}

func TestDataFusionAbsentParentStillRequiresReviewedActionBinding(t *testing.T) {
	for _, mode := range []string{"connection", "provider", "partition", "kind", "native_id", "proof", "impact", "child_permission"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			s := newFusionScenario(t)
			r, _, _, request := fusionReviewed(t, s, fusionTestInstance)
			driver, _ := r.ResolveAction(ctx, "connection", request.Asset)
			delete(s.resources, fusionTestInstance)
			switch mode {
			case "connection":
				request.Asset.Identity.ConnectionID = "foreign"
			case "provider":
				request.Asset.Identity.Provider = asset.ProviderAzure
			case "partition":
				request.Asset.Identity.Partition = "foreign"
			case "kind":
				request.Asset.Identity.NativeType = fusionDNSType
			case "native_id":
				request.Asset.Identity.NativeID += "-foreign"
			case "proof":
				delete(request.Asset.Normalized, fusionProof)
			case "impact":
				request.LifecycleImpacts[0].Asset.Identity.ConnectionID = "foreign"
			case "child_permission":
				s.handle = func(req *http.Request) (*http.Response, bool) {
					if req.URL.Path == "/v1beta1/"+fusionTestInstance+"/namespaces" {
						return dataformResponse(req, 403, map[string]any{}), true
					}
					return nil, false
				}
			}
			if check, err := driver.Preflight(ctx, request); err == nil || check.Absent || check.Allowed {
				t.Fatalf("absent parent accepted invalid review: %+v %v", check, err)
			}
			if read, err := driver.Readback(ctx, request); err == nil {
				t.Fatalf("absent parent accepted invalid readback: %+v %v", read, err)
			}
			if wait, err := driver.Wait(ctx, request, contracts.ActionResult{}); err == nil || wait.Done {
				t.Fatalf("absent parent accepted invalid wait: %+v %v", wait, err)
			}
		})
	}
}
