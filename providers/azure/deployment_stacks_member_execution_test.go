package azure

import (
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func TestDeploymentStackMemberExecution(t *testing.T) {
	for _, mode := range []string{"complete", "pending", "failed", "delete_forbidden", "readback_forbidden", "recreated", "changed_job", "changed_member", "changed_receipt", "changed_choice", "root_changed", "missing_before_delete", "retained", "changed_result", "root_changes_during_readback"} {
		t.Run(mode, func(t *testing.T) {
			member := actionAsset(diskType, "disk")
			raw := nativeResource(diskType, "disk", "eastus", map[string]any{"uniqueId": "original-disk", "provisioningState": "Succeeded"})
			member.Normalized = map[string]any{"_arm_creation_generation": creationGeneration(raw)}
			_, req := stackDeletePlanFixture(t, false, contracts.ActionImpact{Asset: member, ControllerID: "stack", Delete: mode != "retained"})
			req.IdempotencyKey = "member-job"
			root := map[string]any{"id": req.Asset.Identity.NativeID, "type": deploymentStackType, "systemData": map[string]any{"createdAt": "2020-02-01T01:01:01.1075056Z"}, "properties": map[string]any{"resources": []any{map[string]any{"id": member.Identity.NativeID, "status": "managed", "denyStatus": "none"}}}}
			kind, _ := findType(diskType)
			operation := apiURL("/subscriptions/"+testSubscription+"/providers/Microsoft.Compute/locations/eastus/operations/"+testTenant, kind.Version)
			calls, deletes, polls, ownReads := 0, 0, 0, 0
			deleted, readbackFault := false, false
			r := protocolRuntime(t, func(q *http.Request) (*http.Response, error) {
				calls++
				if q.Method != "GET" && (q.Method != "DELETE" || !strings.EqualFold(q.URL.Path, member.Identity.NativeID)) {
					t.Fatal("member execution changed another resource", q.Method, q.URL)
				}
				if reply, handled := emptyMonitorIndexResponse(t, q); handled {
					return reply, nil
				}
				if reply, handled := emptyDiagnosticSourceIndexResponse(t, q); handled {
					return reply, nil
				}
				if q.URL.String() == operation {
					polls++
					state := "Succeeded"
					if mode == "pending" {
						state = "Running"
					}
					if mode == "failed" {
						state = "Failed"
					}
					if state == "Succeeded" {
						deleted = true
					}
					return jsonResponse(200, map[string]any{"status": state}, nil), nil
				}
				switch path := strings.ToLower(q.URL.Path); path {
				case req.Asset.Identity.NativeID:
					return jsonResponse(200, root, nil), nil
				case member.Identity.NativeID:
					if q.Method == "DELETE" {
						deletes++
						if q.Header.Get("x-ms-client-request-id") != azureRequestID(req.IdempotencyKey+":member:disk") {
							t.Fatal("member delete lost stable operation identity")
						}
						if mode == "delete_forbidden" {
							return jsonResponse(403, map[string]any{}, nil), nil
						}
						headers := http.Header{}
						headers.Set("Azure-AsyncOperation", operation)
						return jsonResponse(202, map[string]any{}, headers), nil
					}
					ownReads++
					if readbackFault {
						if mode == "root_changes_during_readback" {
							root["tags"] = map[string]any{"changed": true}
						}
						if mode == "readback_forbidden" {
							return jsonResponse(403, map[string]any{}, nil), nil
						}
						if mode == "recreated" {
							object(raw["properties"])["uniqueId"] = "replacement-disk"
							return jsonResponse(200, raw, nil), nil
						}
					}
					if deleted || mode == "missing_before_delete" {
						return jsonResponse(404, map[string]any{}, nil), nil
					}
					return jsonResponse(200, raw, nil), nil
				case "/subscriptions/" + testSubscription + "/resourcegroups/test":
					return jsonResponse(200, map[string]any{"id": path, "type": groupType}, nil), nil
				case "/subscriptions/" + testSubscription + "/providers/microsoft.authorization/locks":
					return jsonResponse(200, map[string]any{"value": []any{}}, nil), nil
				default:
					t.Fatalf("unexpected member execution request %s", q.URL)
					return nil, nil
				}
			})
			c, err := r.resolve(t.Context(), "connection")
			if err != nil {
				t.Fatal(err)
			}
			review, err := c.deploymentStackMemberReview(root)
			if err != nil {
				t.Fatal(err)
			}
			req.Asset.Normalized[deploymentStackReviewKey] = review
			req.Asset.Normalized[deploymentStackProofKey] = c.deploymentStackProof(req.Asset.Identity.NativeID, "connection", review)
			out, err := r.deploymentStackExecuteMember(t.Context(), req, member.ID, nil)
			if mode == "missing_before_delete" {
				var call *contracts.ProviderCallError
				if !errors.As(err, &call) || call.Provider.Category != execution.ErrorDependencyViolation {
					t.Fatal("member absence could be mistaken for Stack absence", err)
				}
			}
			if mode == "delete_forbidden" || mode == "missing_before_delete" || mode == "retained" {
				if err == nil || out.Data != nil || out.Done || (mode != "delete_forbidden" && deletes != 0) {
					t.Fatal("invalid member deletion started", out, err, deletes)
				}
				return
			}
			if err != nil || out.Done || deletes != 1 || out.Data["phase"] != "wait" {
				t.Fatal("native Execute did not yield persisted progress", out, err, deletes)
			}
			id := member.ID
			before := calls
			switch mode {
			case "changed_job":
				req.IdempotencyKey = "another-job"
			case "changed_member":
				id = asset.AssetID("other")
			case "changed_receipt":
				out.Data["phase"] = "complete"
			case "changed_result":
				out.Data["result"] = contracts.ActionResult{ProviderOperationID: "https://other.invalid/operation"}
			case "changed_choice":
				req.LifecycleImpacts[0].Delete = false
			case "root_changed":
				root["tags"] = map[string]any{"changed": true}
			}
			for step := 0; step < 3; step++ {
				wire, err := json.Marshal(out.Data)
				if err != nil {
					t.Fatal(err)
				}
				var saved map[string]any
				if err := json.Unmarshal(wire, &saved); err != nil {
					t.Fatal(err)
				}
				requestWire, err := json.Marshal(req)
				if err != nil || json.Unmarshal(requestWire, &req) != nil {
					t.Fatal("could not persist member request", err)
				}
				out, err = r.deploymentStackExecuteMember(t.Context(), req, id, saved)
				if strings.HasPrefix(mode, "changed_") || mode == "root_changed" || mode == "failed" {
					if err == nil || out.Done || out.Data != nil || deletes != 1 {
						t.Fatal("invalid member execution resumed", out, err, deletes)
					}
					if strings.HasPrefix(mode, "changed_") && calls != before {
						t.Fatal("tampered checkpoint reached native HTTP")
					}
					return
				}
				if err != nil || deletes != 1 {
					t.Fatal("member execution repeated deletion", out, err, deletes)
				}
				if mode == "pending" {
					if out.Done || out.Data["phase"] != "wait" || polls != step+1 {
						t.Fatal("pending native operation completed", out, polls)
					}
					continue
				}
				if out.Done != (step > 0) || polls != 1 {
					t.Fatal("readback phase or completed resume lost", out, step, polls)
				}
			}
			if mode == "readback_forbidden" || mode == "recreated" || mode == "root_changes_during_readback" {
				readbackFault = true
				beforeReads := ownReads
				out, err = r.deploymentStackExecuteMember(t.Context(), req, member.ID, out.Data)
				if err == nil || out.Done || out.Data != nil || deletes != 1 || ownReads <= beforeReads {
					t.Fatal("completed checkpoint hid new member state", out, err, deletes, ownReads)
				}
			}
		})
	}
}

func TestDeploymentStackMemberExecutionPreservesProductPhases(t *testing.T) {
	for _, mode := range []string{"native_phases", "prepared", "flat_prepared", "delete_all"} {
		t.Run(mode, func(t *testing.T) {
			live := attachmentResources()
			var req contracts.ActionRequest
			var root map[string]any
			writes := []string{}
			deleted := false
			observing, sawVM, sawRetained := false, false, false
			fault := ""
			calls, childDependencyReads := 0, 0
			r := protocolRuntime(t, func(q *http.Request) (*http.Response, error) {
				calls++
				if observing && !sawVM && strings.HasSuffix(strings.ToLower(q.URL.Path), "/providers/microsoft.insights/metricalerts") {
					childDependencyReads++
					if fault == "child_readback_forbidden" {
						return jsonResponse(403, map[string]any{}, nil), nil
					}
				}
				if reply, handled := emptyMonitorIndexResponse(t, q); handled {
					return reply, nil
				}
				if reply, handled := emptyDiagnosticSourceIndexResponse(t, q); handled {
					return reply, nil
				}
				if strings.EqualFold(q.URL.Path, req.Asset.Identity.NativeID) && q.Method == "GET" {
					return jsonResponse(200, root, nil), nil
				}
				if q.Method == "GET" && (strings.HasSuffix(strings.ToLower(q.URL.Path), "/locks") || strings.EqualFold(q.URL.Path, text(live["vm"]["id"])+"/extensions")) {
					return jsonResponse(200, map[string]any{"value": []any{}}, nil), nil
				}
				if q.Method == "GET" && strings.HasSuffix(strings.ToLower(q.URL.Path), "/resourcegroups/test") {
					return jsonResponse(200, map[string]any{"id": q.URL.Path, "type": groupType}, nil), nil
				}
				for name, raw := range live {
					if !strings.EqualFold(q.URL.Path, text(raw["id"])) {
						continue
					}
					if q.Method == "GET" {
						if observing {
							if name == "vm" {
								sawVM = true
							}
							if name == "boot" {
								sawRetained = true
							}
							if fault == "nic_forbidden" && name == "nic" {
								return jsonResponse(403, map[string]any{}, nil), nil
							}
							if fault == "ip_missing" && name == "ip" {
								return jsonResponse(404, map[string]any{}, nil), nil
							}
							if fault == "nic_returned" && name == "nic" || fault == "vm_returned" && name == "vm" || fault == "nic_returns_late" && name == "nic" && sawRetained {
								return jsonResponse(200, raw, nil), nil
							}
						}
						if deleted && (mode == "delete_all" || name == "vm" || name == "nic") {
							return jsonResponse(404, map[string]any{}, nil), nil
						}
						return jsonResponse(200, raw, nil), nil
					}
					writes = append(writes, q.Method+" "+name)
					if q.Method == "DELETE" && name == "vm" {
						deleted = true
						delete(live["boot"], "managedBy")
						delete(live["data"], "managedBy")
						return jsonResponse(200, map[string]any{}, nil), nil
					}
					if q.Method != "PUT" && q.Method != "PATCH" || name != "vm" && name != "nic" {
						t.Fatal("unreviewed product mutation", q.Method, name)
					}
					var body map[string]any
					if err := json.NewDecoder(q.Body).Decode(&body); err != nil {
						t.Fatal(err)
					}
					previous := object(raw["properties"])
					if q.Method == "PUT" {
						raw["properties"] = body["properties"]
						for _, key := range []string{"resourceGuid", "virtualMachine", "privateEndpoint", "hostedWorkloads"} {
							if value, found := previous[key]; found {
								object(raw["properties"])[key] = value
							}
						}
					} else {
						for key, value := range object(body["properties"]) {
							previous[key] = value
						}
					}
					raw["etag"] = "after-" + name
					return jsonResponse(200, raw, nil), nil
				}
				t.Fatalf("unexpected product phase request %s %s", q.Method, q.URL)
				return nil, nil
			})
			c, err := r.resolve(t.Context(), "connection")
			if err != nil {
				t.Fatal(err)
			}
			req, root = stackAttachmentRequest(t, c)
			if mode == "delete_all" {
				for i := range req.LifecycleImpacts {
					req.LifecycleImpacts[i].Delete = true
				}
			}
			if mode == "flat_prepared" {
				for i := range req.LifecycleImpacts {
					if req.LifecycleImpacts[i].Asset.ID == "nic" {
						req.LifecycleImpacts[i].ControllerID = req.Asset.ID
						properties := object(root["properties"])
						properties["resources"] = append(properties["resources"].([]any), map[string]any{"id": req.LifecycleImpacts[i].Asset.Identity.NativeID, "status": "managed", "denyStatus": "none"})
					}
				}
				review, err := c.deploymentStackMemberReview(root)
				if err != nil {
					t.Fatal(err)
				}
				req.Asset.Normalized[deploymentStackReviewKey] = review
				req.Asset.Normalized[deploymentStackProofKey] = c.deploymentStackProof(req.Asset.Identity.NativeID, "connection", review)
			}
			progress := deploymentStackProgress{}
			if strings.Contains(mode, "prepared") {
				for i := range req.LifecycleImpacts {
					if req.LifecycleImpacts[i].Asset.ID == "vm" {
						req.LifecycleImpacts[i].Asset.Normalized["_arm_generation"] = productGeneration(live["vm"])
					}
				}
				var preparation map[string]any
				ready := false
				for step := 0; step < 4; step++ {
					out, err := c.deploymentStackPrepareMember(t.Context(), req, "vm", preparation)
					if err != nil {
						t.Fatal("Stack retention preparation failed", err)
					}
					wire, err := json.Marshal(out.Data)
					if err != nil || json.Unmarshal(wire, &preparation) != nil {
						t.Fatal("could not persist preparation", err)
					}
					if out.Done {
						ready = true
						break
					}
				}
				if !ready {
					t.Fatal("Stack preparation did not complete")
				}
				progress.Preparations = []map[string]any{preparation}
			}
			var saved map[string]any
			completed := false
			for step := 0; step < 8; step++ {
				// The full request binding treats impact order as immaterial; the native
				// product receipt must use the same deterministic member projection.
				slices.Reverse(req.LifecycleImpacts)
				var out contracts.WaitResult
				var err error
				if step == 0 {
					out, err = r.deploymentStackExecuteMemberWithProgress(t.Context(), req, "vm", nil, progress)
				} else {
					out, err = r.deploymentStackExecuteMember(t.Context(), req, "vm", saved)
				}
				if err != nil {
					t.Fatal("product phase failed", step, writes, err)
				}
				wire, err := json.Marshal(out.Data)
				if err != nil || json.Unmarshal(wire, &saved) != nil {
					t.Fatal("product receipt could not survive restart", err)
				}
				if out.Done {
					completed = true
					break
				}
			}
			expectedWrites, expectedCompleted := "PUT nic,PATCH vm,DELETE vm", 2
			if mode == "delete_all" {
				expectedWrites, expectedCompleted = "DELETE vm", 5
			}
			writeCount := len(strings.Split(expectedWrites, ","))
			if !completed || strings.Join(writes, ",") != expectedWrites {
				t.Fatal("product phases skipped or repeated", completed, writes)
			}
			out, err := r.deploymentStackExecuteMember(t.Context(), req, "vm", saved)
			if err != nil || !out.Done || len(writes) != writeCount {
				t.Fatal("completed product execution repeated a mutation", out, err, writes)
			}
			for _, testFault := range []string{"none", "no_receipt", "waiting", "tampered", "job", "invalid_second_receipt", "nic_forbidden", "nic_returned", "vm_returned", "nic_returns_late", "ip_missing", "child_readback_forbidden"} {
				if mode == "delete_all" && (testFault == "ip_missing" || testFault == "nic_returns_late") {
					continue
				}
				t.Run("progress_"+testFault, func(t *testing.T) {
					wire, _ := json.Marshal(out.Data)
					var receipt map[string]any
					if err := json.Unmarshal(wire, &receipt); err != nil {
						t.Fatal(err)
					}
					executionProgress := deploymentStackProgress{Executions: []map[string]any{receipt}}
					current := req
					switch testFault {
					case "no_receipt":
						executionProgress.Executions = nil
					case "waiting":
						receipt["phase"] = "wait"
						binding, err := c.deploymentStackMemberExecutionBinding(req, receipt)
						if err != nil {
							t.Fatal(err)
						}
						receipt["binding"] = binding
					case "tampered":
						receipt["binding"] = "changed"
					case "job":
						current.IdempotencyKey = "other-job"
					case "invalid_second_receipt":
						executionProgress.Executions = append(executionProgress.Executions, map[string]any{"member": "nic", "phase": "complete", "binding": "invalid"})
					}
					before, _ := json.Marshal(executionProgress)
					priorCalls, priorChildReads := calls, childDependencyReads
					observing, sawVM, sawRetained = true, false, false
					fault = testFault
					observed, err := r.deploymentStackObserveProgress(t.Context(), current, executionProgress)
					observing = false
					if testFault == "none" {
						if err != nil || !observed.Completed["vm"] || !observed.Completed["nic"] || observed.CascadedFrom["nic"] != "vm" || childDependencyReads <= priorChildReads {
							t.Fatal("native NIC consequence did not receive its own product readback", observed, err)
						}
						if mode == "delete_all" {
							for _, id := range []asset.AssetID{"boot", "data", "nic", "ip"} {
								if !observed.Completed[id] || observed.CascadedFrom[id] != "vm" {
									t.Fatal("recursive attachment consequence missing", id, observed)
								}
							}
						}
						if len(observed.Completed) != expectedCompleted || len(observed.CascadedFrom) != expectedCompleted-1 || len(executionProgress.Executions) != 1 {
							t.Fatal("retained child completed or child receipt fabricated", observed, executionProgress)
						}
					} else {
						if err == nil || observed.Completed != nil || observed.Members != nil || observed.CascadedFrom != nil {
							t.Fatal("unverified consequence accepted", observed, err)
						}
						if (testFault == "waiting" || testFault == "tampered" || testFault == "job" || testFault == "invalid_second_receipt") && calls != priorCalls {
							t.Fatal("invalid parent receipt reached native HTTP", calls, priorCalls)
						}
						if testFault == "child_readback_forbidden" && childDependencyReads != priorChildReads+1 {
							t.Fatal("child product dependency check not exercised", childDependencyReads, priorChildReads)
						}
					}
					after, _ := json.Marshal(executionProgress)
					if string(before) != string(after) || len(writes) != writeCount {
						t.Fatal("progress mutated checkpoint or cloud", writes)
					}
				})
			}

		})
	}
}
