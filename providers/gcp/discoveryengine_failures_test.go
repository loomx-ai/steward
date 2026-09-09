package gcp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func deJSON(req *http.Request, code int, data any) *http.Response {
	raw, _ := json.Marshal(data)
	return apiResponse(req, code, string(raw))
}

func TestDiscoveryEngineInventoryRejectsIncompleteAndForeignResponses(t *testing.T) {
	for _, mode := range []string{"permission", "partial", "unreachable", "embedded_error", "embedded_detail_error", "array_shape", "missing_name", "foreign_project", "foreign_collection", "foreign_region", "wrong_host", "missing_creation", "creation_changed", "configuration_changed", "token_type", "token_cycle", "unsupported_paging"} {
		t.Run(mode, func(t *testing.T) {
			s := newDiscoveryScenario(t)
			r := protocolRuntime(t, s.transport(t))
			target := "/v1/" + deCollection + "/dataStores"
			kind := discoveryDataStoreType
			if mode == "unsupported_paging" {
				target = "/v1/" + deCollection + "/engines"
				kind = discoveryEngineType
			}
			s.handle = func(req *http.Request) (*http.Response, bool) {
				if req.URL.Path == "/v1/"+deStore && req.Method == "GET" && (mode == "creation_changed" || mode == "configuration_changed" || mode == "embedded_detail_error") {
					data := cloneParameters(s.resources[deStore])
					if mode == "creation_changed" {
						data["createTime"] = "2026-09-01T12:00:00Z"
					} else if mode == "embedded_detail_error" {
						data["error"] = map[string]any{"code": 7, "message": "DISCOVERY_PRIVATE_ERROR"}
					} else {
						data["displayName"] = "changed"
					}
					return deJSON(req, 200, data), true
				}
				if req.URL.Path != target || req.Method != "GET" {
					return nil, false
				}
				value := cloneParameters(s.resources[deStore])
				data := map[string]any{"dataStores": []any{value}}
				switch mode {
				case "permission":
					return deJSON(req, 403, map[string]any{}), true
				case "partial":
					return deJSON(req, 206, data), true
				case "unreachable":
					data["unreachable"] = []any{"us"}
				case "embedded_error":
					data["error"] = map[string]any{"code": 7, "message": "DISCOVERY_PRIVATE_ERROR"}
				case "array_shape":
					data["dataStores"] = map[string]any{}
				case "missing_name":
					delete(value, "name")
				case "foreign_project":
					value["name"] = strings.Replace(deStore, "sample-project", "foreign-project", 1)
				case "foreign_collection":
					value["name"] = strings.Replace(deStore, "default_collection", "other", 1)
				case "foreign_region":
					value["name"] = strings.Replace(deStore, "global", "us", 1)
				case "wrong_host":
					value["name"] = "https://us-discoveryengine.googleapis.com/v1/" + deStore
				case "missing_creation":
					delete(value, "createTime")
				case "token_type":
					data["nextPageToken"] = 7
				case "token_cycle":
					data["nextPageToken"] = "loop"
				case "unsupported_paging":
					data = map[string]any{"nextPageToken": "unsupported"}
				default:
					return nil, false
				}
				return deJSON(req, 200, data), true
			}
			req := productRequest(r, kind, "project")
			failed := false
			for i := 0; i < 20; i++ {
				page, err := r.List(context.Background(), req)
				if err != nil {
					failed = true
					break
				}
				if page.Complete {
					break
				}
				req.Cursor = page.NextCursor
			}
			if !failed || len(s.mutations) > 0 {
				t.Fatalf("accepted %s inventory", mode)
			}
		})
	}
}

func TestDiscoveryEnginePreflightRejectsChangedResourcesAndPlans(t *testing.T) {
	for _, mode := range []string{"root_recreated", "root_configuration", "root_identity", "parent_recreated", "child_configuration", "child_recreated", "child_protected", "new_child", "missing_impact", "retained_impact", "duplicate_impact", "duplicate_asset", "foreign_connection", "foreign_partition", "foreign_provider", "wrong_controller", "wrong_child_type", "missing_proof", "forged_parent", "missing_parent_proof", "read_permission", "list_permission", "late_child"} {
		t.Run(mode, func(t *testing.T) {
			s := newDiscoveryScenario(t)
			r, _, _, request := discoveryReviewed(t, s, deEngine)
			driver, err := r.ResolveAction(context.Background(), "connection", request.Asset)
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "root_recreated":
				s.resources[deEngine]["createTime"] = "2026-09-01T00:00:00Z"
			case "root_configuration":
				s.resources[deEngine]["dataStoreIds"] = []any{"other"}
			case "root_identity":
				s.resources[deEngine]["name"] = deEngine + "-other"
			case "parent_recreated":
				s.resources[deCollection]["createTime"] = "2026-09-01T00:00:00Z"
			case "child_configuration":
				s.resources[deEngine+"/sessions/session-1"]["turns"] = []any{map[string]any{"query": map[string]any{"text": "new user query"}}}
			case "child_recreated":
				s.resources[deEngine+"/assistants/default_assistant"]["createTime"] = "2026-09-01T00:00:00Z"
			case "child_protected":
				s.resources[deEngine+"/sessions/session-1"]["labels"] = map[string]any{"steward-protected": "true"}
			case "new_child":
				s.resources[deEngine+"/sessions/new"] = map[string]any{"name": deEngine + "/sessions/new", "startTime": "2026-09-01T00:00:00Z"}
			case "missing_impact":
				request.LifecycleImpacts = request.LifecycleImpacts[1:]
			case "retained_impact":
				request.LifecycleImpacts[0].Delete = false
			case "duplicate_impact":
				request.LifecycleImpacts = append(request.LifecycleImpacts, request.LifecycleImpacts[0])
			case "duplicate_asset":
				request.LifecycleImpacts[0].Asset.ID = request.LifecycleImpacts[1].Asset.ID
			case "foreign_connection":
				request.LifecycleImpacts[0].Asset.Identity.ConnectionID = "other"
			case "foreign_partition":
				request.LifecycleImpacts[0].Asset.Identity.Partition = "other"
			case "foreign_provider":
				request.LifecycleImpacts[0].Asset.Identity.Provider = "azure"
			case "wrong_controller":
				request.LifecycleImpacts[0].ControllerID = "other"
			case "wrong_child_type":
				request.LifecycleImpacts[0].Asset.Identity.NativeType = discoveryDataStoreType
			case "missing_proof":
				delete(request.Asset.Normalized, discoveryProof)
			case "forged_parent":
				request.Asset.Normalized["_discoveryengine_parent_id"] = "//" + discoveryHost + "/" + deUSCollection
			case "missing_parent_proof":
				delete(request.Asset.Normalized, "_discoveryengine_ancestors")
			case "read_permission", "list_permission":
				path := "/v1/" + deEngine + "/sessions"
				if mode == "read_permission" {
					path += "/session-1"
				}
				s.handle = func(req *http.Request) (*http.Response, bool) {
					if req.URL.Path == path {
						return deJSON(req, 403, map[string]any{}), true
					}
					return nil, false
				}
			case "late_child":
				calls := 0
				s.handle = func(req *http.Request) (*http.Response, bool) {
					if req.URL.Path == "/v1/"+deEngine+"/sessions" {
						calls++
						if calls == 2 {
							return deJSON(req, 200, map[string]any{"sessions": []any{s.resources[deEngine+"/sessions/session-1"], map[string]any{"name": deEngine + "/sessions/late"}}}), true
						}
					}
					return nil, false
				}
			}
			if _, err := driver.Execute(context.Background(), request); err == nil || len(s.mutations) > 0 {
				t.Fatalf("allowed %s: %v %v", mode, err, s.mutations)
			}
		})
	}
}

func TestDiscoveryEngineWaitRejectsCorruptStateAndRecreatedResources(t *testing.T) {
	for _, mode := range []string{"foreign_host", "foreign_region", "sibling_operation", "operation_response_name", "operation_type", "operation_error", "operation_error_shape", "done_type", "phase", "phase_resource", "phase_configuration", "phase_operation", "root_recreated", "child_recreated", "child_permission", "operation_permission", "missing_request_proof", "changed_impacts"} {
		t.Run(mode, func(t *testing.T) {
			s := newDiscoveryScenario(t)
			r, _, _, request := discoveryReviewed(t, s, deEngine)
			driver, err := r.ResolveAction(context.Background(), "connection", request.Asset)
			if err != nil {
				t.Fatal(err)
			}
			result, err := driver.Execute(context.Background(), request)
			if err != nil {
				t.Fatal(err)
			}
			operation := s.operations[deEngine+"/operations/delete-1"]
			switch mode {
			case "foreign_host":
				result.ProviderOperationID = strings.Replace(result.ProviderOperationID, discoveryHost, "attacker.example", 1)
				result.Data["operation"] = result.ProviderOperationID
			case "foreign_region":
				result.ProviderOperationID = strings.Replace(result.ProviderOperationID, "/locations/global/", "/locations/us/", 1)
				result.Data["operation"] = result.ProviderOperationID
			case "sibling_operation":
				result.ProviderOperationID = strings.Replace(result.ProviderOperationID, "/engines/search/", "/engines/other/", 1)
				result.Data["operation"] = result.ProviderOperationID
			case "operation_response_name":
				operation["name"] = deStore + "/operations/delete-1"
			case "operation_type":
				object(operation["metadata"])["@type"] = "type.googleapis.com/google.cloud.discoveryengine.v1.CreateEngineMetadata"
			case "operation_error":
				operation["error"] = map[string]any{"code": 7, "message": "DISCOVERY_PRIVATE_ERROR"}
			case "operation_error_shape":
				operation["error"] = "DISCOVERY_PRIVATE_ERROR"
			case "done_type":
				operation["done"] = "true"
			case "phase":
				result.Data["phase"] = "delete"
			case "phase_resource":
				result.Data["resource"] = deStore
			case "phase_configuration":
				result.Data["configuration"] = "forged"
			case "phase_operation":
				result.Data["operation"] = ""
			case "root_recreated":
				s.resources[deEngine]["createTime"] = "2026-09-01T00:00:00Z"
			case "child_recreated":
				delete(s.resources, deEngine)
				s.resources[deEngine+"/assistants/default_assistant"]["createTime"] = "2026-09-01T00:00:00Z"
			case "child_permission", "operation_permission":
				path := "/v1/" + deEngine + "/assistants/default_assistant"
				if mode == "operation_permission" {
					path = "/v1/" + deEngine + "/operations/delete-1"
				} else {
					delete(s.resources, deEngine)
				}
				s.handle = func(req *http.Request) (*http.Response, bool) {
					if req.URL.Path == path {
						return deJSON(req, 403, map[string]any{}), true
					}
					return nil, false
				}
			case "changed_impacts":
				request.LifecycleImpacts = request.LifecycleImpacts[1:]
			case "missing_request_proof":
				delete(request.Asset.Normalized, discoveryProof)
			}
			if wait, err := driver.Wait(context.Background(), request, result); err == nil || wait.Done || len(s.mutations) != 1 {
				t.Fatalf("accepted %s %+v %v", mode, wait, err)
			}
		})
	}
}

func TestDiscoveryEngineCursorBindsAncestorIncarnation(t *testing.T) {
	for _, mode := range []string{"recreated_store", "changed_store", "recreated_collection", "tampered_cursor"} {
		t.Run(mode, func(t *testing.T) {
			s := newDiscoveryScenario(t)
			s.emptyPage = true
			r := protocolRuntime(t, s.transport(t))
			req := productRequest(r, discoveryHost+"/Document", "project")
			first, err := r.List(context.Background(), req)
			if err != nil || first.Complete {
				t.Fatalf("first document page %+v %v", first, err)
			}
			req.Cursor = first.NextCursor
			switch mode {
			case "recreated_store":
				s.resources[deStore]["createTime"] = "2026-09-01T00:00:00Z"
			case "changed_store":
				s.resources[deStore]["aclEnabled"] = true
			case "recreated_collection":
				s.resources[deCollection]["createTime"] = "2026-09-01T00:00:00Z"
			case "tampered_cursor":
				raw, _ := base64.RawURLEncoding.DecodeString(req.Cursor)
				var cursor map[string]any
				_ = json.Unmarshal(raw, &cursor)
				cursor["target"] = -1
				raw, _ = json.Marshal(cursor)
				req.Cursor = base64.RawURLEncoding.EncodeToString(raw)
			}
			if _, err := r.List(context.Background(), req); err == nil {
				t.Fatalf("accepted %s", mode)
			}
		})
	}
}

func TestDiscoveryEngineMissingResourceStillValidatesActionBinding(t *testing.T) {
	for _, mode := range []string{"provider", "connection", "partition", "type", "native_id", "proof"} {
		t.Run(mode, func(t *testing.T) {
			s := newDiscoveryScenario(t)
			r, _, _, request := discoveryReviewed(t, s, deEngine)
			driver, err := r.ResolveAction(context.Background(), "connection", request.Asset)
			if err != nil {
				t.Fatal(err)
			}
			for name := range s.resources {
				if name == deEngine || strings.HasPrefix(name, deEngine+"/") {
					delete(s.resources, name)
				}
			}
			switch mode {
			case "provider":
				request.Asset.Identity.Provider = "azure"
			case "connection":
				request.Asset.Identity.ConnectionID = "other"
			case "partition":
				request.Asset.Identity.Partition = "other"
			case "type":
				request.Asset.Identity.NativeType = discoveryDataStoreType
			case "native_id":
				request.Asset.Identity.NativeID += "-other"
			case "proof":
				delete(request.Asset.Normalized, discoveryProof)
			}
			if _, err := driver.Execute(context.Background(), request); err == nil {
				t.Fatal("absent action skipped binding")
			}
			if _, err := driver.Readback(context.Background(), request); err == nil {
				t.Fatal("absent readback skipped binding")
			}
			if _, err := driver.Wait(context.Background(), request, contracts.ActionResult{}); err == nil {
				t.Fatal("absent waiter skipped binding")
			}
		})
	}
}
