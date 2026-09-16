package azure

import (
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func TestDeploymentStackSetupHandoff(t *testing.T) {
	for _, flat := range []bool{false, true} {
		name := "product_controller"
		if flat {
			name = "stack_controller"
		}
		t.Run(name, func(t *testing.T) { testDeploymentStackSetupHandoff(t, flat) })
	}
}
func testDeploymentStackSetupHandoff(t *testing.T, flat bool) {
	for _, mode := range []string{"complete", "job", "unknown", "skip_preparation", "nested_preparation", "handoff_drift", "lost_preparation_context", "returned_prerequisite", "root_protected", "root_lock", "subscription_lock", "locks_forbidden", "root_forbidden", "late_lock", "late_protected", "attachment_changed", "outcome_missing", "outcome_recreated", "outcome_nic_returned"} {
		t.Run(mode, func(t *testing.T) {
			req, root := stackAttachmentRequest(t, directClient(nil))
			root["location"] = "eastus"
			if flat {
				for i := range req.LifecycleImpacts {
					if req.LifecycleImpacts[i].Asset.ID == "nic" {
						req.LifecycleImpacts[i].ControllerID = req.Asset.ID
						properties := object(root["properties"])
						properties["resources"] = append(properties["resources"].([]any), map[string]any{"id": req.LifecycleImpacts[i].Asset.Identity.NativeID, "status": "managed", "denyStatus": "none"})
					}
				}
			}
			if mode == "root_protected" {
				root["tags"] = map[string]any{"steward/protected": "true"}
			}
			guardActive := mode != "late_lock" && mode != "late_protected"
			live := attachmentResources()
			if mode == "attachment_changed" {
				object(object(object(live["vm"]["properties"])["networkProfile"])["networkInterfaces"].([]any)[0])["properties"] = map[string]any{"primary": true, "deleteOption": "Detach"}
			}
			nativeDelete := mode == "complete" || strings.HasPrefix(mode, "outcome_")
			outcomeFault := false
			parent, child := actionAsset(hostGroupType, "host-parent"), actionAsset(hostType, "host-child")
			parent.Identity.Partition = req.Asset.Identity.Partition
			child.Identity.Partition = req.Asset.Identity.Partition
			child.Identity.NativeID = parent.Identity.NativeID + "/hosts/host-child"
			req.LifecycleImpacts = append(req.LifecycleImpacts, contracts.ActionImpact{Asset: parent, ControllerID: req.Asset.ID, Delete: true}, contracts.ActionImpact{Asset: child, ControllerID: parent.ID, Delete: true})
			properties := object(root["properties"])
			properties["resources"] = append(properties["resources"].([]any), map[string]any{"id": parent.Identity.NativeID, "status": "managed", "denyStatus": "none"})
			parentRaw := map[string]any{"id": parent.Identity.NativeID, "type": hostGroupType, "location": "eastus", "properties": map[string]any{"provisioningState": "Succeeded"}}
			childRaw := map[string]any{"id": child.Identity.NativeID, "type": hostType, "location": "eastus", "properties": map[string]any{"provisioningState": "Succeeded"}}
			for i := range req.LifecycleImpacts {
				if raw := live[string(req.LifecycleImpacts[i].Asset.ID)]; raw != nil {
					field := map[string]string{"vm": "vmId", "nic": "resourceGuid", "boot": "uniqueId", "data": "uniqueId", "ip": "resourceGuid"}[string(req.LifecycleImpacts[i].Asset.ID)]
					object(raw["properties"])[field] = "11111111-1111-1111-1111-111111111111"
					req.LifecycleImpacts[i].Asset.Normalized["_arm_creation_generation"] = creationGeneration(raw)
					req.LifecycleImpacts[i].Asset.Normalized["_arm_generation"] = productGeneration(raw)
				}
			}
			writes := []string{}
			calls := 0
			gone, stackGone := false, false
			r := protocolRuntime(t, func(q *http.Request) (*http.Response, error) {
				calls++
				if reply, handled := emptyMonitorIndexResponse(t, q); handled {
					return reply, nil
				}
				if reply, handled := emptyDiagnosticSourceIndexResponse(t, q); handled {
					return reply, nil
				}
				path := strings.ToLower(q.URL.Path)
				switch path {
				case req.Asset.Identity.NativeID:
					if q.Method == "DELETE" && nativeDelete {
						if !slices.Equal(writes, []string{"PUT nic", "PATCH vm", "DELETE host-child"}) {
							t.Fatal("Stack delete preceded setup", writes)
						}
						writes = append(writes, "DELETE stack")
						stackGone = true
						delete(live["boot"], "managedBy")
						delete(live["data"], "managedBy")
						return &http.Response{StatusCode: 204, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(""))}, nil
					}
					if stackGone {
						return jsonResponse(404, map[string]any{}, nil), nil
					}
					if q.Method != "GET" {
						t.Fatal("setup deleted Stack")
					}
					if mode == "root_forbidden" {
						return jsonResponse(403, map[string]any{}, nil), nil
					}
					return jsonResponse(200, root, nil), nil
				case parent.Identity.NativeID:
					if stackGone {
						return jsonResponse(404, map[string]any{}, nil), nil
					}
					if q.Method != "GET" {
						t.Fatal("setup deleted non-prerequisite parent")
					}
					return jsonResponse(200, parentRaw, nil), nil
				case child.Identity.NativeID:
					if q.Method == "DELETE" {
						if !slices.Equal(writes, []string{"PUT nic", "PATCH vm"}) {
							t.Fatal("deletion preceded retention preparation", writes)
						}
						writes = append(writes, "DELETE host-child")
						gone = true
						return jsonResponse(200, map[string]any{}, nil), nil
					}
					if gone {
						return jsonResponse(404, map[string]any{}, nil), nil
					}
					return jsonResponse(200, childRaw, nil), nil
				case parent.Identity.NativeID + "/hosts":
					rows := []any{}
					if !gone {
						rows = append(rows, childRaw)
					}
					return jsonResponse(200, map[string]any{"value": rows}, nil), nil
				}
				if strings.HasSuffix(path, "/locks") && guardActive {
					if mode == "locks_forbidden" {
						return jsonResponse(403, map[string]any{}, nil), nil
					}
					if mode == "root_lock" || mode == "subscription_lock" || mode == "late_lock" {
						scope := req.Asset.Identity.NativeID
						if mode == "subscription_lock" {
							scope = "/subscriptions/" + testSubscription
						}
						return jsonResponse(200, map[string]any{"value": []any{map[string]any{"id": scope + "/providers/Microsoft.Authorization/locks/hold", "properties": map[string]any{"level": "CanNotDelete"}}}}, nil), nil
					}
				}
				if strings.HasSuffix(path, "/locks") || strings.EqualFold(q.URL.Path, text(live["vm"]["id"])+"/extensions") {
					return jsonResponse(200, map[string]any{"value": []any{}}, nil), nil
				}
				if strings.HasSuffix(path, "/resourcegroups/test") {
					return jsonResponse(200, map[string]any{"id": q.URL.Path, "type": groupType, "location": "eastus"}, nil), nil
				}
				for name, raw := range live {
					if !strings.EqualFold(q.URL.Path, text(raw["id"])) {
						continue
					}
					if q.Method == "GET" {
						if stackGone && outcomeFault && mode == "outcome_missing" && name == "boot" {
							return jsonResponse(404, map[string]any{}, nil), nil
						}
						if stackGone && (name == "vm" || name == "nic" && !(outcomeFault && mode == "outcome_nic_returned")) {
							return jsonResponse(404, map[string]any{}, nil), nil
						}
						return jsonResponse(200, raw, nil), nil
					}
					if name != "vm" && name != "nic" || q.Method != "PUT" && q.Method != "PATCH" {
						t.Fatal("unexpected setup mutation", name, q.Method)
					}
					var body map[string]any
					if err := json.NewDecoder(q.Body).Decode(&body); err != nil {
						t.Fatal(err)
					}
					if q.Method == "PUT" {
						previous := object(raw["properties"])
						raw["properties"] = body["properties"]
						for _, key := range []string{"resourceGuid", "virtualMachine", "privateEndpoint", "hostedWorkloads"} {
							if value, found := previous[key]; found {
								object(raw["properties"])[key] = value
							}
						}
					} else {
						for key, value := range object(body["properties"]) {
							object(raw["properties"])[key] = value
						}
					}
					raw["etag"] = "after-" + name
					writes = append(writes, q.Method+" "+name)
					return jsonResponse(200, raw, nil), nil
				}
				t.Fatalf("unexpected setup request %s %s", q.Method, q.URL)
				return nil, nil
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
			before, _ := json.Marshal(req)
			var saved map[string]any
			var out contracts.WaitResult
			tampered := false
			for step := 0; step < 14; step++ {
				if step > 0 {
					state := object(saved["state"])
					switch {
					case step == 1 && mode == "job":
						req.IdempotencyKey = "other-job"
						tampered = true
					case step == 1 && mode == "unknown":
						state["ignore_review"] = true
						tampered = true
					case step == 1 && mode == "skip_preparation":
						state["phase"] = "prerequisites"
						tampered = true
					case step == 1 && mode == "nested_preparation":
						object(state["preparation"])["binding"] = "changed"
						tampered = true
					case state["phase"] == "prerequisites" && state["prerequisites"] == nil && mode == "handoff_drift":
						object(object(live["nic"]["properties"])["dnsSettings"])["dnsServers"] = []any{"10.0.0.9"}
						tampered = true
					case state["prerequisites"] != nil && mode == "lost_preparation_context":
						nested := object(state["prerequisites"])
						wire, _ := json.Marshal(nested["state"])
						var ps deploymentStackPrerequisiteState
						if err := json.Unmarshal(wire, &ps); err != nil {
							t.Fatal(err)
						}
						ps.Progress.Preparations = nil
						binding, err := c.deploymentStackPrerequisiteBinding(req, ps)
						if err != nil {
							t.Fatal(err)
						}
						nested["state"], nested["binding"] = ps, binding
						tampered = true
					}
				}
				if len(writes) == 1 && (mode == "late_lock" || mode == "late_protected") {
					guardActive = true
					if mode == "late_protected" {
						root["tags"] = map[string]any{"steward/protected": "true"}
					}
				}
				priorCalls, priorWrites := calls, len(writes)
				out, err = r.deploymentStackAdvanceSetup(t.Context(), req, saved)
				if guardActive && (strings.HasPrefix(mode, "root_") || mode == "subscription_lock" || mode == "locks_forbidden" || strings.HasPrefix(mode, "late_")) {
					if err == nil || out.Data != nil || out.Done || len(writes) != priorWrites {
						t.Fatal("root guard allowed setup mutation", out, err, writes)
					}
					expected := 0
					if strings.HasPrefix(mode, "late_") {
						expected = 1
					}
					if len(writes) != expected {
						t.Fatal("root guard checked too late", writes)
					}
					if strings.HasPrefix(mode, "late_") {
						guardActive = false
						delete(root, "tags")
						resumed, err := r.deploymentStackAdvanceSetup(t.Context(), req, saved)
						if err != nil || resumed.Data == nil || len(writes) > priorWrites+1 {
							t.Fatal("unchanged checkpoint could not resume after protection cleared", resumed, err, writes)
						}
						puts := 0
						for _, write := range writes {
							if write == "PUT nic" {
								puts++
							}
						}
						if puts != 1 {
							t.Fatal("resumption repeated prepared NIC write", writes)
						}
					}
					return
				}
				if mode == "attachment_changed" {
					if err == nil || out.Data != nil || len(writes) != 0 {
						t.Fatal("changed native delete option allowed preparation", out, err, writes)
					}
					return
				}
				if tampered {
					if err == nil || out.Done || out.Data != nil {
						t.Fatal("invalid phase handoff accepted", out, err)
					}
					if mode != "handoff_drift" && calls != priorCalls {
						t.Fatal("invalid setup context reached HTTP", calls, priorCalls)
					}
					if len(writes) != priorWrites {
						t.Fatal("invalid setup context mutated cloud")
					}
					if mode == "lost_preparation_context" && !strings.Contains(err.Error(), "deployment_stack_setup_preparation_changed") {
						t.Fatal("wrong handoff rejection", err)
					}
					return
				}
				if err != nil || len(writes) > priorWrites+1 {
					t.Fatal("setup failed or chained mutations", step, out, err, writes)
				}
				wire, err := json.Marshal(out.Data)
				if err != nil || json.Unmarshal(wire, &saved) != nil {
					t.Fatal("persist setup", err)
				}
				if out.Done {
					break
				}
			}
			if !out.Done || !slices.Equal(writes, []string{"PUT nic", "PATCH vm", "DELETE host-child"}) {
				t.Fatal("setup not completed in order", out, writes)
			}
			if mode == "returned_prerequisite" {
				gone = false
			}
			priorWrites, priorCalls := len(writes), calls
			out, err = r.deploymentStackAdvanceSetup(t.Context(), req, saved)
			if mode == "returned_prerequisite" {
				if err == nil || out.Done || out.Data != nil {
					t.Fatal("finished setup hid returned child", out, err)
				}
			} else if err != nil || !out.Done || calls <= priorCalls {
				t.Fatal("setup resumed preparation after deletion or reused stale observations", out, err)
			}
			if len(writes) != priorWrites {
				t.Fatal("completed setup repeated mutation")
			}
			if nativeDelete {
				result, err := r.deploymentStackStartDelete(t.Context(), req, out.Data)
				if err != nil {
					t.Fatal("native attachment cascade submission", err)
				}
				wire, _ := json.Marshal(result.Data)
				var checkpoint map[string]any
				if err = json.Unmarshal(wire, &checkpoint); err != nil {
					t.Fatal(err)
				}
				for step := 0; step < 2; step++ {
					outcomeFault = step == 1 && strings.HasPrefix(mode, "outcome_")
					if outcomeFault && mode == "outcome_recreated" {
						object(live["boot"]["properties"])["uniqueId"] = "22222222-2222-2222-2222-222222222222"
					}
					final, err := r.deploymentStackResumeDeletion(t.Context(), req, checkpoint)
					if outcomeFault {
						if err == nil && final.Products.ProductsReconciled || len(writes) != 4 {
							t.Fatal("completed checkpoint hid changed attachment outcome", final, err, writes)
						}
						if err != nil && final.Data != nil {
							t.Fatal("partial failed outcome", final, err)
						}
						break
					}
					if err != nil || !final.Products.ProductsReconciled {
						t.Fatal("native attachment cascade outcome", final, err)
					}
					if !slices.Equal(writes, []string{"PUT nic", "PATCH vm", "DELETE host-child", "DELETE stack"}) {
						t.Fatal("repeated or independent attachment DELETE", writes)
					}
					for _, id := range []string{"boot", "data", "ip"} {
						if final.Products.Execution.Outcome.RetentionEvidence[asset.AssetID(id)] != "creation_identity" {
							t.Fatal("retained attachment not verified", id, final)
						}
					}
					checkpoint = final.Data
				}
			}
			after, _ := json.Marshal(req)
			if string(before) != string(after) {
				t.Fatal("setup mutated frozen request")
			}
		})
	}
}
