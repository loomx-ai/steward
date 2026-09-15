package azure

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func TestDeploymentStackCompletedPrerequisitePreflight(t *testing.T) {
	for _, mode := range []string{"complete", "no_receipt", "waiting_receipt", "duplicate", "changed_job", "changed_receipt", "second_receipt_invalid", "recreated", "forbidden", "parent_config_changed", "parent_protected", "missing_parent_fingerprint", "second_parent_change", "child_returns_during_parent_read", "root_change", "parent_config_changed_same_etag", "execute_parent", "execute_parent_forbidden", "execute_parent_changed_context", "execute_parent_invalid_projection", "execute_parent_child_returns", "execute_parent_resume_progress", "closure_service_complete", "closure_service_stale", "closure_service_duplicate", "closure_service_unreviewed", "closure_service_reappeared", "closure_service_forbidden", "closure_service_no_receipt"} {
		t.Run(mode, func(t *testing.T) {
			parent := actionAsset(hostGroupType, "parent")
			child := actionAsset(hostType, "child")
			child.Identity.NativeID = parent.Identity.NativeID + "/hosts/child"
			parentRaw := map[string]any{"id": parent.Identity.NativeID, "type": hostGroupType, "etag": "before", "location": "eastus", "properties": map[string]any{"provisioningState": "Succeeded", "platformFaultDomainCount": 2, "hosts": []any{map[string]any{"id": child.Identity.NativeID}}}}
			childRaw := map[string]any{"id": child.Identity.NativeID, "type": hostType, "etag": "child-etag", "location": "eastus", "properties": map[string]any{"provisioningState": "Succeeded"}}
			parent.Normalized = map[string]any{"_arm_generation": productGeneration(parentRaw), "_arm_parent_configuration": serviceParentConfiguration(hostGroupType, parentRaw)}
			if mode == "missing_parent_fingerprint" {
				delete(parent.Normalized, "_arm_parent_configuration")
			}
			_, req := stackDeletePlanFixture(t, false, contracts.ActionImpact{Asset: parent, ControllerID: "stack", Delete: true}, contracts.ActionImpact{Asset: child, ControllerID: parent.ID, Delete: true})
			req.IdempotencyKey = "completed-child-job"
			root := map[string]any{"id": req.Asset.Identity.NativeID, "type": deploymentStackType, "systemData": map[string]any{"createdAt": "2020-02-01T01:01:01.1075056Z"}, "properties": map[string]any{"resources": []any{map[string]any{"id": parent.Identity.NativeID, "status": "managed", "denyStatus": "none"}}}}
			gone, active, parentGone, executingParent := false, false, false, false
			parentDeletes := 0
			deletes, calls, parentReads, childReads, lists := 0, 0, 0, 0, 0
			r := protocolRuntime(t, func(q *http.Request) (*http.Response, error) {
				if active {
					calls++
					if q.Method != "GET" && !executingParent {
						t.Fatal("completed prerequisite validation mutated state", q.Method, q.URL)
					}
				}
				if q.Method != "GET" && (q.Method != "DELETE" || !strings.EqualFold(q.URL.Path, parent.Identity.NativeID) && !strings.EqualFold(q.URL.Path, child.Identity.NativeID)) {
					t.Fatal("unexpected mutation", q.Method, q.URL)
				}
				if reply, handled := emptyMonitorIndexResponse(t, q); handled {
					return reply, nil
				}
				if reply, handled := emptyDiagnosticSourceIndexResponse(t, q); handled {
					return reply, nil
				}
				switch path := strings.ToLower(q.URL.Path); path {
				case req.Asset.Identity.NativeID:
					return jsonResponse(200, root, nil), nil
				case parent.Identity.NativeID:
					if q.Method == "DELETE" {
						parentDeletes++
						if mode == "execute_parent_forbidden" {
							return jsonResponse(403, map[string]any{}, nil), nil
						}
						parentGone = true
						return jsonResponse(200, map[string]any{}, nil), nil
					}
					if parentGone {
						return jsonResponse(404, map[string]any{}, nil), nil
					}
					if active {
						parentReads++
						if parentReads > 1 && mode == "second_parent_change" {
							object(parentRaw["properties"])["platformFaultDomainCount"] = 3
							parentRaw["etag"] = "changed-again"
						}
						if mode == "child_returns_during_parent_read" {
							gone = false
						}
					}
					return jsonResponse(200, parentRaw, nil), nil
				case child.Identity.NativeID:
					if mode == "execute_parent_child_returns" && parentGone {
						gone = false
					}
					if q.Method == "DELETE" {
						deletes++
						gone = true
						parentRaw["etag"] = "after-child-deletion"
						object(parentRaw["properties"])["hosts"] = []any{}
						return jsonResponse(200, map[string]any{}, nil), nil
					}
					if active {
						childReads++
						if mode == "forbidden" {
							return jsonResponse(403, map[string]any{}, nil), nil
						}
					}
					if gone {
						return jsonResponse(404, map[string]any{}, nil), nil
					}
					return jsonResponse(200, childRaw, nil), nil
				case parent.Identity.NativeID + "/hosts/unreviewed":
					return jsonResponse(200, map[string]any{"id": parent.Identity.NativeID + "/hosts/unreviewed", "type": hostType, "properties": map[string]any{}}, nil), nil
				case parent.Identity.NativeID + "/hosts":
					lists++
					if mode == "closure_service_forbidden" {
						return jsonResponse(403, map[string]any{}, nil), nil
					}
					if mode == "closure_service_reappeared" {
						gone = false
					}
					rows := []any{}
					if !gone || mode == "closure_service_stale" || mode == "closure_service_duplicate" {
						rows = append(rows, childRaw)
					}
					if mode == "closure_service_duplicate" {
						rows = append(rows, childRaw)
					}
					if mode == "closure_service_unreviewed" {
						rows = append(rows, map[string]any{"id": parent.Identity.NativeID + "/hosts/unreviewed", "type": hostType, "properties": map[string]any{}})
					}
					return jsonResponse(200, map[string]any{"value": rows}, nil), nil
				case "/subscriptions/" + testSubscription + "/resourcegroups/test":
					return jsonResponse(200, map[string]any{"id": path, "type": groupType}, nil), nil
				case "/subscriptions/" + testSubscription + "/providers/microsoft.authorization/locks":
					return jsonResponse(200, map[string]any{"value": []any{}}, nil), nil
				default:
					t.Fatalf("unexpected completed prerequisite request %s %s", q.Method, q.URL)
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
			var saved, waiting map[string]any
			for step := 0; step < 4; step++ {
				out, err := r.deploymentStackExecuteMember(t.Context(), req, child.ID, saved)
				if err != nil {
					t.Fatal("could not complete native child lifecycle", step, err)
				}
				wire, err := json.Marshal(out.Data)
				if err != nil || json.Unmarshal(wire, &saved) != nil {
					t.Fatal("could not persist child execution", err)
				}
				if step == 0 {
					if err := json.Unmarshal(wire, &waiting); err != nil {
						t.Fatal(err)
					}
				}
				if out.Done {
					break
				}
			}
			if saved["phase"] != "complete" || deletes != 1 {
				t.Fatal("child did not finish exactly one native deletion", saved, deletes)
			}
			progress := deploymentStackProgress{Executions: []map[string]any{saved}}
			switch mode {
			case "no_receipt", "closure_service_no_receipt":
				progress.Executions = nil
			case "waiting_receipt":
				progress.Executions = []map[string]any{waiting}
			case "duplicate":
				progress.Executions = append(progress.Executions, saved)
			case "changed_job":
				req.IdempotencyKey = "other-job"
			case "changed_receipt":
				saved["binding"] = "changed"
			case "second_receipt_invalid":
				progress.Executions = append(progress.Executions, map[string]any{"member": string(parent.ID), "phase": "complete", "binding": "invalid"})
			case "recreated":
				gone = false
			case "parent_config_changed":
				object(parentRaw["properties"])["platformFaultDomainCount"] = 3
			case "parent_config_changed_same_etag":
				parentRaw["etag"] = "before"
				object(parentRaw["properties"])["platformFaultDomainCount"] = 3
			case "parent_protected":
				parentRaw["tags"] = map[string]any{"steward/protected": "true"}
			case "root_change":
				root["tags"] = map[string]any{"changed": true}
			}
			wire, err := json.Marshal(req)
			if err != nil || json.Unmarshal(wire, &req) != nil {
				t.Fatal("could not resume Stack request", err)
			}
			before, _ := json.Marshal(req)
			active = true
			if strings.HasPrefix(mode, "closure_service_") {
				closure, err := r.deploymentStackObserveServiceClosureWithProgress(t.Context(), req, progress)
				if mode == "closure_service_complete" {
					if err != nil || len(closure.Parents) != 1 || closure.Parents[0] != parent.ID || len(closure.DirectChildren) != 0 || childReads < 4 {
						t.Fatal("completed child was not reconciled through service closure", closure, err, childReads)
					}
				} else if err == nil || len(closure.Parents)+len(closure.DirectChildren) != 0 {
					t.Fatal("invalid service closure returned partial success", closure, err)
				}
				if mode != "closure_service_no_receipt" && lists == 0 {
					t.Fatal("service closure did not reach native child enumeration", err)
				}
				after, _ := json.Marshal(req)
				if string(before) != string(after) || deletes != 1 || parentDeletes != 0 {
					t.Fatal("service closure mutated reviewed state")
				}
				return
			}
			checks, err := r.deploymentStackPreflightProductsWithProgress(t.Context(), req, progress)
			after, _ := json.Marshal(req)
			if string(before) != string(after) || deletes != 1 {
				t.Fatal("progress changed the frozen request or repeated deletion")
			}
			if mode == "complete" || strings.HasPrefix(mode, "execute_parent") {
				if err != nil || len(checks) != 1 || checks[0].Member != parent.ID || !checks[0].Check.Allowed || lists == 0 || parentReads < 4 || childReads < 4 {
					t.Fatal("native parent preflight did not accept verified prerequisite", checks, err, lists, parentReads, childReads)
				}
				closure, err := r.deploymentStackObserveServiceClosureWithProgress(t.Context(), req, progress)
				if err != nil || len(closure.Parents) != 1 || closure.Parents[0] != parent.ID || len(closure.DirectChildren) != 0 {
					t.Fatal("service closure lost completed parent prerequisites", closure, err)
				}
				if mode == "complete" {
					return
				}
				executingParent = true
				out, err := r.deploymentStackExecuteMemberWithProgress(t.Context(), req, parent.ID, nil, progress)
				if mode == "execute_parent_forbidden" {
					if err == nil || out.Data != nil || parentDeletes != 1 || deletes != 1 {
						t.Fatal("native parent refusal was bypassed", out, err, parentDeletes, deletes)
					}
					return
				}
				if err != nil || out.Done || len(out.Data) != 5 || parentDeletes != 1 || deletes != 1 {
					t.Fatal("parent did not execute with its completed prerequisite", out, err, parentDeletes, deletes)
				}
				beforeResume := calls
				for step := 0; step < 3; step++ {
					wire, err := json.Marshal(out.Data)
					var parentReceipt map[string]any
					if err != nil || json.Unmarshal(wire, &parentReceipt) != nil {
						t.Fatal("could not persist native parent request", err)
					}
					stored := object(parentReceipt["request"])
					if object(object(stored["asset"])["normalized"])["_arm_generation"] != parent.Normalized["_arm_generation"] {
						t.Fatal("recorded product request discarded original generation")
					}
					if mode == "execute_parent_changed_context" {
						stored["idempotency_key"] = "changed"
					}
					if mode == "execute_parent_invalid_projection" {
						delete(stored, "prerequisite_deletions")
						parentReceipt["binding"], err = c.deploymentStackMemberExecutionBinding(req, parentReceipt)
						if err != nil {
							t.Fatal(err)
						}
					}
					if mode == "execute_parent_resume_progress" {
						out, err = r.deploymentStackExecuteMemberWithProgress(t.Context(), req, parent.ID, parentReceipt, progress)
					} else {
						out, err = r.deploymentStackExecuteMember(t.Context(), req, parent.ID, parentReceipt)
					}
					if mode != "execute_parent" {
						if err == nil || out.Done || out.Data != nil || parentDeletes != 1 || deletes != 1 {
							t.Fatal("invalid parent resume succeeded", out, err)
						}
						if mode != "execute_parent_child_returns" && calls != beforeResume {
							t.Fatal("invalid stored request reached native HTTP", calls, beforeResume)
						}
						return
					}
					if err != nil || out.Done != (step > 0) || len(out.Data) != 5 || parentDeletes != 1 || deletes != 1 {
						t.Fatal("parent resume lost its native request or repeated deletion", out, err, step)
					}
				}
				observed, err := r.deploymentStackObserveProgress(t.Context(), req, deploymentStackProgress{Executions: []map[string]any{saved, out.Data}})
				if err != nil || len(observed.Completed) != 2 || len(observed.Members) != 0 || parentDeletes != 1 || deletes != 1 {
					t.Fatal("completed parent/child receipts did not reconcile", observed, err)
				}
				closure, err = r.deploymentStackObserveServiceClosureWithProgress(t.Context(), req, deploymentStackProgress{Executions: []map[string]any{saved, out.Data}})
				if err != nil || len(closure.Parents)+len(closure.DirectChildren) != 0 {
					t.Fatal("completed parent was enumerated as an active service", closure, err)
				}
				after, _ = json.Marshal(req)
				if string(before) != string(after) {
					t.Fatal("parent execution mutated the frozen Stack request")
				}
				return
			}
			if err == nil || checks != nil {
				t.Fatal("invalid completed prerequisite authorized product preflight", checks, err)
			}
			if mode == "waiting_receipt" || mode == "duplicate" || mode == "changed_job" || mode == "changed_receipt" || mode == "second_receipt_invalid" {
				if calls != 0 {
					t.Fatal("incomplete receipt validation reached native reads", calls)
				}
			}
		})
	}
}
