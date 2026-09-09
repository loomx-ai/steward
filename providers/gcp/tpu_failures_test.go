package gcp

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func TestTPUInventoryRejectsIncompleteForeignAndUnstableResponses(t *testing.T) {
	for _, mode := range []string{"permission", "locations_permission", "partial", "unreachable", "error", "array_shape", "duplicate", "missing_name", "wrong_collection", "foreign_project", "foreign_zone", "wrong_host", "region_not_zone", "missing_uid", "missing_creation", "detail_identity", "detail_configuration", "detail_error", "disk_missing", "disk_identity", "disk_shape", "queue_changed", "token_type", "token_cycle"} {
		t.Run(mode, func(t *testing.T) {
			s := newTPUScenario(t)
			r := protocolRuntime(t, s.transport(t))
			reads := 0
			s.handle = func(req *http.Request) (*http.Response, bool) {
				if req.Method != "GET" {
					return nil, false
				}
				switch {
				case req.URL.Path == "/v2/projects/sample-project/locations" && mode == "locations_permission":
					return dataformResponse(req, 403, map[string]any{}), true
				case req.URL.Path == "/compute/v1/"+tpuTestDisk:
					if mode == "disk_missing" {
						return dataformResponse(req, 404, map[string]any{}), true
					}
					if mode == "disk_identity" {
						data := cloneParameters(s.resources[tpuTestDisk])
						data["selfLink"] = "https://www.googleapis.com/compute/v1/" + tpuTestDisk + "-foreign"
						return dataformResponse(req, 200, data), true
					}
				case req.URL.Path == "/v2/"+tpuTestQueue && mode == "queue_changed":
					reads++
					data := roundTripDataformJSON(t, s.resources[tpuTestQueue])
					if reads > 1 {
						data["createTime"] = "2026-08-01T00:00:01Z"
					}
					return dataformResponse(req, 200, data), true
				case req.URL.Path == "/v2/"+tpuTestNode:
					data := roundTripDataformJSON(t, s.resources[tpuTestNode])
					switch mode {
					case "detail_identity":
						data["id"] = "99111"
					case "detail_configuration":
						data["metadata"] = map[string]any{"custom": "changed"}
					case "detail_error":
						data["error"] = map[string]any{"code": 7, "message": "TPU_PRIVATE_ERROR"}
					default:
						return nil, false
					}
					return dataformResponse(req, 200, data), true
				case req.URL.Path == "/v2/"+tpuTestParent+"/nodes":
					node := roundTripDataformJSON(t, s.resources[tpuTestNode])
					data := map[string]any{"nodes": []any{node}}
					switch mode {
					case "permission":
						return dataformResponse(req, 403, map[string]any{}), true
					case "partial":
						return dataformResponse(req, 206, data), true
					case "unreachable":
						data["unreachable"] = []any{"us-central2-b"}
					case "error":
						data["error"] = map[string]any{"code": 7}
					case "array_shape":
						data["nodes"] = map[string]any{}
					case "duplicate":
						data["nodes"] = []any{node, node}
					case "missing_name":
						delete(node, "name")
					case "wrong_collection":
						node["name"] = tpuTestQueue
					case "foreign_project":
						node["name"] = strings.Replace(tpuTestNode, "sample-project", "foreign", 1)
					case "foreign_zone":
						node["name"] = strings.Replace(tpuTestNode, "us-central2-b", "us-central2-c", 1)
					case "wrong_host":
						node["name"] = "https://foreign.example/v2/" + tpuTestNode
					case "region_not_zone":
						node["name"] = strings.Replace(tpuTestNode, "us-central2-b", "us-central2", 1)
					case "missing_uid":
						delete(node, "id")
					case "missing_creation":
						delete(node, "createTime")
					case "disk_shape":
						node["dataDisks"] = map[string]any{}
					case "token_type":
						data["nextPageToken"] = 7
					case "token_cycle":
						data["nextPageToken"] = "loop"
					default:
						return nil, false
					}
					return dataformResponse(req, 200, data), true
				}
				return nil, false
			}
			req := productRequest(r, tpuNodeType, "us-central2")
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
				t.Fatalf("accepted TPU inventory fault %s", mode)
			}
		})
	}
}

func TestTPUPreflightRejectsChangedNodeQueueDisksAndPlans(t *testing.T) {
	for _, mode := range []string{"node_uid", "node_create_time", "node_configuration", "node_identity", "node_state", "node_protected", "queue_recreated", "queue_configuration", "queue_missing", "queue_backlink", "disk_missing", "disk_recreated", "disk_configuration", "new_disk", "disk_mode", "missing_proof", "missing_base_proof", "missing_disk_proof", "missing_queue_proof", "missing_impact", "delete_disk", "duplicate_impact", "wrong_controller", "wrong_kind", "foreign_provider", "foreign_connection", "foreign_partition", "wrong_asset", "extra_prerequisite", "read_denied", "late_node_replacement"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			s := newTPUScenario(t)
			r, assets, result := tpuReviewed(t, s, tpuTestNode)
			node := batchAsset(assets, tpuTestNode)
			request := dataformRequest(t, result, assets, node)
			driver, err := r.ResolveAction(ctx, "connection", node)
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "node_uid":
				s.resources[tpuTestNode]["id"] = "123999"
			case "node_create_time":
				s.resources[tpuTestNode]["createTime"] = "2026-08-02T00:00:00Z"
			case "node_configuration":
				s.resources[tpuTestNode]["metadata"] = map[string]any{"custom": "new-private-value"}
			case "node_identity":
				s.resources[tpuTestNode]["name"] = tpuTestNode1
			case "node_state":
				s.resources[tpuTestNode]["state"] = "NEW_NATIVE_STATE"
			case "node_protected":
				s.resources[tpuTestNode]["deletionProtection"] = true
			case "queue_recreated":
				s.resources[tpuTestQueue]["createTime"] = "2026-08-01T00:00:01Z"
			case "queue_configuration":
				s.resources[tpuTestQueue]["guaranteed"] = map[string]any{"minDuration": "3600s"}
			case "queue_missing":
				delete(s.resources, tpuTestQueue)
			case "queue_backlink":
				s.resources[tpuTestNode]["queuedResource"] = tpuTestParent + "/queuedResources/foreign"
			case "disk_missing":
				delete(s.resources, tpuTestDisk)
			case "disk_recreated":
				s.resources[tpuTestDisk]["id"] = "9991"
			case "disk_configuration":
				s.resources[tpuTestDisk]["sizeGb"] = "200"
			case "new_disk":
				s.resources[tpuTestNode]["dataDisks"] = []any{map[string]any{"sourceDisk": strings.Replace(tpuTestDisk, "data-0", "data-1", 1)}}
			case "disk_mode":
				array(s.resources[tpuTestNode]["dataDisks"])[0].(map[string]any)["mode"] = "READ_ONLY"
			case "missing_proof":
				delete(request.Asset.Normalized, tpuProof)
			case "missing_base_proof":
				delete(request.Asset.Normalized, tpuBaseProof)
			case "missing_disk_proof":
				delete(request.Asset.Normalized, tpuDiskProofs)
			case "missing_queue_proof":
				delete(request.Asset.Normalized, tpuQueueProof)
			case "missing_impact":
				request.LifecycleImpacts = nil
			case "delete_disk":
				request.LifecycleImpacts[0].Delete = true
			case "duplicate_impact":
				request.LifecycleImpacts = append(request.LifecycleImpacts, request.LifecycleImpacts[0])
			case "wrong_controller":
				request.LifecycleImpacts[0].ControllerID = "foreign"
			case "wrong_kind":
				request.LifecycleImpacts[0].Asset.Identity.NativeType = "compute.googleapis.com/RegionDisk"
			case "foreign_provider":
				request.LifecycleImpacts[0].Asset.Identity.Provider = asset.ProviderAzure
			case "foreign_connection":
				request.LifecycleImpacts[0].Asset.Identity.ConnectionID = "foreign"
			case "foreign_partition":
				request.LifecycleImpacts[0].Asset.Identity.Partition = "foreign"
			case "wrong_asset":
				request.Asset.Identity.NativeID += "-foreign"
			case "extra_prerequisite":
				request.PrerequisiteDeletions = []contracts.ActionImpact{{Asset: node, Delete: true, ControllerID: node.ID}}
			case "read_denied":
				s.handle = func(req *http.Request) (*http.Response, bool) {
					if req.URL.Path == "/v2/"+tpuTestNode {
						return dataformResponse(req, 403, map[string]any{}), true
					}
					return nil, false
				}
			case "late_node_replacement":
				reads := 0
				s.handle = func(req *http.Request) (*http.Response, bool) {
					if req.Method == "GET" && req.URL.Path == "/v2/"+tpuTestNode {
						reads++
						if reads == 3 {
							data := cloneParameters(s.resources[tpuTestNode])
							data["id"] = "999999"
							return dataformResponse(req, 200, data), true
						}
					}
					return nil, false
				}
			}
			if _, err := driver.Execute(ctx, request); err == nil || len(s.mutations) != 0 {
				t.Fatalf("accepted TPU preflight fault %s writes=%v err=%v", mode, s.mutations, err)
			}
		})
	}
}

func TestTPUWaitRejectsChangedPhasesOperationsAndResources(t *testing.T) {
	for _, mode := range []string{"phase", "resource", "configuration", "review", "operation_type", "operation_host", "operation_version", "operation_zone", "operation_name", "initial_operation", "done_type", "error_shape", "operation_error", "metadata_type", "metadata_version", "metadata_verb", "metadata_target", "metadata_target_type", "metadata_shape", "cancelled", "recreated_node", "recreated_disk", "recreated_queue", "late_recreation"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			s := newTPUScenario(t)
			r, assets, planResult := tpuReviewed(t, s, tpuTestNode)
			node := batchAsset(assets, tpuTestNode)
			request := dataformRequest(t, planResult, assets, node)
			driver, _ := r.ResolveAction(ctx, "connection", node)
			result, err := driver.Execute(ctx, request)
			if err != nil {
				t.Fatal(err)
			}
			result = roundTripDataformJSON(t, result)
			opName := strings.TrimPrefix(result.ProviderOperationID, "https://tpu.googleapis.com/v2/")
			op := s.operations[opName]
			switch mode {
			case "phase":
				result.Data["phase"] = "tpu_queue_settle"
			case "resource":
				result.Data["resource"] = tpuTestNode1
			case "configuration":
				result.Data["configuration"] = "foreign"
			case "review":
				result.Data["review"] = "foreign"
			case "operation_type":
				result.Data["operation"] = 7
			case "operation_host":
				result.Data["operation"] = strings.Replace(result.ProviderOperationID, "tpu.googleapis.com", "foreign.example", 1)
			case "operation_version":
				result.Data["operation"] = strings.Replace(result.ProviderOperationID, "/v2/", "/v2alpha1/", 1)
			case "operation_zone":
				result.Data["operation"] = strings.Replace(result.ProviderOperationID, "us-central2-b", "us-central2-c", 1)
			case "operation_name":
				op["name"] = tpuTestParent + "/operations/foreign"
			case "initial_operation":
				result.Data["initial_operation"] = "foreign"
			case "done_type":
				op["done"] = "true"
			case "error_shape":
				op["error"] = map[string]any{}
			case "operation_error":
				op["error"] = map[string]any{"code": 7, "message": "TPU_PRIVATE_ERROR"}
			case "metadata_type":
				object(op["metadata"])["@type"] = "type.googleapis.com/foreign"
			case "metadata_version":
				object(op["metadata"])["apiVersion"] = "v2alpha1"
			case "metadata_verb":
				object(op["metadata"])["verb"] = "delete"
			case "metadata_target":
				object(op["metadata"])["target"] = tpuTestNode1
			case "metadata_target_type":
				object(op["metadata"])["target"] = 7
			case "metadata_shape":
				op["metadata"] = "invalid"
			case "cancelled":
				object(op["metadata"])["cancelRequested"] = true
			case "recreated_node":
				s.resources[tpuTestNode]["id"] = "new-id"
			case "recreated_disk":
				s.resources[tpuTestDisk]["id"] = "new-id"
			case "recreated_queue":
				s.resources[tpuTestQueue]["createTime"] = "2026-08-01T00:00:01Z"
			case "late_recreation":
				reads := 0
				s.handle = func(req *http.Request) (*http.Response, bool) {
					if req.Method == "GET" && req.URL.Path == "/v2/"+tpuTestNode {
						reads++
						if reads == 1 {
							return dataformResponse(req, 404, map[string]any{}), true
						}
						data := cloneParameters(s.resources[tpuTestNode])
						data["id"] = "99999"
						return dataformResponse(req, 200, data), true
					}
					return nil, false
				}
			}
			driver, _ = r.ResolveAction(ctx, "connection", node)
			if wait, err := driver.Wait(ctx, request, result); err == nil || wait.Done || len(s.mutations) != 1 {
				t.Fatalf("accepted TPU wait fault %s: %+v %v writes=%v", mode, wait, err, s.mutations)
			}
		})
	}
}

func TestTPULogsAndInvokeRedactNodeAndTemplateMetadata(t *testing.T) {
	var logs []execution.JobLogEntry
	ctx := execution.WithJobLogSink(context.Background(), execution.JobLogSinkFunc(func(_ context.Context, entry execution.JobLogEntry) { logs = append(logs, entry) }))
	s := newTPUScenario(t)
	r := protocolRuntime(t, s.transport(t))
	for _, entry := range []struct{ method, name string }{{"tpu.projects.locations.nodes.get", tpuTestNode}, {"tpu.projects.locations.queuedResources.get", tpuTestQueue}} {
		result, err := r.Invoke(ctx, contracts.Invocation{ConnectionID: "connection", Operation: entry.method, Parameters: map[string]any{"name": entry.name}})
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(result)
		if strings.Contains(string(raw), "TPU_PRIVATE_") {
			t.Fatalf("TPU invoke leak: %s", raw)
		}
	}
	raw, _ := json.Marshal(logs)
	if strings.Contains(string(raw), "TPU_PRIVATE_") {
		t.Fatalf("TPU log leak: %s", raw)
	}
	c, _ := r.resolve(ctx, "connection")
	live, err := c.tpuRead(ctx, tpuNodeType, "//tpu.googleapis.com/"+tpuTestNode)
	if err != nil || object(live["metadata"])["custom"] != "TPU_PRIVATE_NODE" {
		t.Fatal("sanitization mutated native configuration proof input")
	}
}

func TestTPUQueueRequiresExactReviewedNodesAndRetainedDisks(t *testing.T) {
	for _, mode := range []string{"still_exists", "missing_prerequisite", "duplicate_prerequisite", "wrong_controller", "foreign_connection", "foreign_partition", "retained_prerequisite", "wrong_node_proof", "wrong_queue_proof", "wrong_disk_proof", "missing_membership", "missing_disk", "recreated_disk", "new_node", "wrong_backlink", "list_denied", "list_partial", "late_new_node"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			s := newTPUScenario(t)
			r, assets, result := tpuReviewed(t, s, tpuTestQueue)
			queue := batchAsset(assets, tpuTestQueue)
			request := dataformRequest(t, result, assets, queue)
			driver, _ := r.ResolveAction(ctx, "connection", queue)
			original := roundTripDataformJSON(t, s.resources[tpuTestNode])
			delete(s.resources, tpuTestNode)
			delete(s.resources, tpuTestNode1)
			s.resources[tpuTestQueue]["state"] = map[string]any{"state": "SUSPENDED"}
			switch mode {
			case "still_exists":
				s.resources[tpuTestNode] = original
			case "missing_prerequisite":
				request.PrerequisiteDeletions = request.PrerequisiteDeletions[:1]
			case "duplicate_prerequisite":
				request.PrerequisiteDeletions[1] = request.PrerequisiteDeletions[0]
			case "wrong_controller":
				request.PrerequisiteDeletions[0].ControllerID = "foreign"
			case "foreign_connection":
				request.PrerequisiteDeletions[0].Asset.Identity.ConnectionID = "foreign"
			case "foreign_partition":
				request.PrerequisiteDeletions[0].Asset.Identity.Partition = "foreign"
			case "retained_prerequisite":
				request.PrerequisiteDeletions[0].Delete = false
			case "wrong_node_proof":
				request.PrerequisiteDeletions[0].Asset.Normalized[tpuProof] = "foreign"
			case "wrong_queue_proof":
				request.PrerequisiteDeletions[0].Asset.Normalized[tpuQueueProof] = "foreign"
			case "wrong_disk_proof":
				request.PrerequisiteDeletions[0].Asset.Normalized[tpuDiskProofs] = "null"
			case "missing_membership":
				delete(request.Asset.Normalized, tpuNodeProofs)
			case "missing_disk":
				delete(s.resources, tpuTestDisk)
			case "recreated_disk":
				s.resources[tpuTestDisk]["id"] = "9991"
			case "new_node":
				original["name"] = tpuTestParent + "/nodes/new-unreviewed"
				s.resources[text(original["name"])] = original
			case "wrong_backlink":
				original["queuedResource"] = tpuTestParent + "/queuedResources/foreign"
				s.resources[tpuTestNode] = original
			case "list_denied", "list_partial":
				s.handle = func(req *http.Request) (*http.Response, bool) {
					if req.URL.Path == "/v2/"+tpuTestParent+"/nodes" {
						code := 403
						if mode == "list_partial" {
							code = 206
						}
						return dataformResponse(req, code, map[string]any{}), true
					}
					return nil, false
				}
			case "late_new_node":
				lists := 0
				s.handle = func(req *http.Request) (*http.Response, bool) {
					if req.URL.Path == "/v2/"+tpuTestParent+"/nodes" {
						lists++
						if lists == 2 {
							original["name"] = tpuTestParent + "/nodes/new-unreviewed"
							s.resources[text(original["name"])] = original
						}
					}
					return nil, false
				}
			}
			if _, err := driver.Execute(ctx, request); err == nil || len(s.mutations) != 0 {
				t.Fatalf("accepted queued-resource fault %s writes=%v err=%v", mode, s.mutations, err)
			}
		})
	}
}

func TestTPUDependencyAbsenceNeverMeansCleanupCompleted(t *testing.T) {
	for _, kind := range []string{tpuNodeType, tpuQueueType} {
		t.Run(last(kind), func(t *testing.T) {
			ctx := context.Background()
			s := newTPUScenario(t)
			root := tpuTestNode
			if kind == tpuQueueType {
				root = tpuTestQueue
			}
			r, assets, result := tpuReviewed(t, s, root)
			value := batchAsset(assets, root)
			request := dataformRequest(t, result, assets, value)
			driver, _ := r.ResolveAction(ctx, "connection", value)
			delete(s.resources, tpuTestNode)
			delete(s.resources, tpuTestNode1)
			if kind == tpuQueueType {
				delete(s.resources, tpuTestQueue)
			}
			delete(s.resources, tpuTestDisk)
			if read, err := driver.Readback(ctx, request); err == nil || isNotFound(err) {
				t.Fatalf("missing retained disk became root absence: %+v %v", read, err)
			}
			if wait, err := driver.Wait(ctx, request, contracts.ActionResult{}); err == nil || isNotFound(err) || wait.Done {
				t.Fatalf("missing retained disk completed wait: %+v %v", wait, err)
			}
		})
	}
}

func TestTPUMissingRootStillRejectsChangedActionBinding(t *testing.T) {
	for _, mode := range []string{"connection", "provider", "partition", "kind", "identity", "proof", "disk_proof"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			s := newTPUScenario(t)
			r, assets, result := tpuReviewed(t, s, tpuTestNode)
			node := batchAsset(assets, tpuTestNode)
			request := dataformRequest(t, result, assets, node)
			driver, _ := r.ResolveAction(ctx, "connection", node)
			delete(s.resources, tpuTestNode)
			switch mode {
			case "connection":
				request.Asset.Identity.ConnectionID = "foreign"
			case "provider":
				request.Asset.Identity.Provider = asset.ProviderAzure
			case "partition":
				request.Asset.Identity.Partition = "foreign"
			case "kind":
				request.Asset.Identity.NativeType = tpuQueueType
			case "identity":
				request.Asset.Identity.NativeID += "-foreign"
			case "proof":
				delete(request.Asset.Normalized, tpuProof)
			case "disk_proof":
				delete(request.Asset.Normalized, tpuDiskProofs)
			}
			if _, err := driver.Preflight(ctx, request); err == nil {
				t.Fatal("changed action binding accepted on root absence")
			}
			if _, err := driver.Readback(ctx, request); err == nil {
				t.Fatal("changed action binding accepted on readback")
			}
			if _, err := driver.Wait(ctx, request, contracts.ActionResult{}); err == nil {
				t.Fatal("changed action binding accepted on wait")
			}
		})
	}
}

func TestTPUInventoryMembershipProofRejectsUnreviewedRequests(t *testing.T) {
	for _, mode := range []string{"foreign_parent", "missing_template", "wrong_multislice_count", "duplicate_specs", "changed_members", "changed_disk", "unexpected_node", "disk_worker_scope", "non_zonal_location"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			s := newTPUScenario(t)
			r := protocolRuntime(t, s.transport(t))
			spec := object(array(object(s.resources[tpuTestQueue]["tpu"])["nodeSpec"])[0])
			switch mode {
			case "foreign_parent":
				spec["parent"] = "projects/foreign/locations/us-central2-b"
			case "missing_template":
				delete(spec, "node")
			case "wrong_multislice_count":
				object(spec["multisliceParams"])["nodeCount"] = 1
			case "duplicate_specs":
				object(s.resources[tpuTestQueue]["tpu"])["nodeSpec"] = []any{spec, spec}
			case "unexpected_node":
				node := roundTripDataformJSON(t, s.resources[tpuTestNode])
				node["name"] = tpuTestParent + "/nodes/unexpected"
				s.resources[text(node["name"])] = node
			case "disk_worker_scope":
				object(array(s.resources[tpuTestNode]["dataDisks"])[0])["workerIds"] = []any{"0"}
			case "changed_members", "changed_disk":
				reads := 0
				s.handle = func(req *http.Request) (*http.Response, bool) {
					if req.URL.Path == "/v2/"+tpuTestParent+"/nodes" {
						reads++
						if reads == 2 {
							if mode == "changed_members" {
								delete(s.resources, tpuTestNode1)
							} else {
								s.resources[tpuTestDisk]["id"] = "999999"
							}
						}
					}
					return nil, false
				}
			case "non_zonal_location":
				s.handle = func(req *http.Request) (*http.Response, bool) {
					if req.URL.Path == "/v2/projects/sample-project/locations" {
						return dataformResponse(req, 200, map[string]any{"locations": []any{map[string]any{"name": "projects/sample-project/locations/us-central2", "locationId": "us-central2"}}}), true
					}
					return nil, false
				}
			}
			req := productRequest(r, tpuQueueType, "us-central2")
			failed := false
			for i := 0; i < 10; i++ {
				batch, err := r.List(ctx, req)
				if err != nil {
					failed = true
					break
				}
				if batch.Complete {
					break
				}
				req.Cursor = batch.NextCursor
			}
			if !failed || len(s.mutations) > 0 {
				t.Fatalf("accepted TPU membership fault %s", mode)
			}
		})
	}
}

func TestTPUPreflightRechecksAbsenceAfterDependencyReads(t *testing.T) {
	for _, root := range []string{tpuTestNode, tpuTestQueue} {
		t.Run(last(root), func(t *testing.T) {
			ctx := context.Background()
			s := newTPUScenario(t)
			r, assets, result := tpuReviewed(t, s, root)
			value := batchAsset(assets, root)
			request := dataformRequest(t, result, assets, value)
			driver, _ := r.ResolveAction(ctx, "connection", value)
			replacement := roundTripDataformJSON(t, s.resources[root])
			replacement["createTime"] = "2026-09-09T00:00:00Z"
			if root == tpuTestQueue {
				delete(s.resources, tpuTestNode)
				delete(s.resources, tpuTestNode1)
			}
			reads := 0
			s.handle = func(req *http.Request) (*http.Response, bool) {
				if req.Method == "GET" && req.URL.Path == "/v2/"+root {
					reads++
					if reads == 1 {
						return dataformResponse(req, 404, map[string]any{}), true
					}
					return dataformResponse(req, 200, replacement), true
				}
				return nil, false
			}
			check, err := driver.Preflight(ctx, request)
			if err == nil || check.Absent || len(s.mutations) != 0 {
				t.Fatalf("earlier 404 hid TPU replacement %+v %v", check, err)
			}
		})
	}
}
