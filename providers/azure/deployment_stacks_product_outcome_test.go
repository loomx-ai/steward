package azure

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func TestDeploymentStackProductOutcome(t *testing.T) {
	for _, mode := range []string{"complete", "native_cascade", "operation_pending", "operation_failed", "root_pending", "product_residual", "product_forbidden", "root_recreated_during_product", "retained_missing_during_product", "changed_member_receipt", "waiting_member_receipt", "duplicate_member_receipt", "changed_stack_receipt", "changed_job"} {
		t.Run(mode, func(t *testing.T) {
			member := actionAsset(diskType, "disk")
			raw := nativeResource(diskType, "disk", "eastus", map[string]any{"uniqueId": "original", "provisioningState": "Succeeded"})
			member.Normalized = map[string]any{"_arm_creation_generation": creationGeneration(raw)}
			keep := actionAsset(groupType, "test")
			keep.ID = "keep"
			keep.Identity.NativeID = "/subscriptions/" + testSubscription + "/resourcegroups/test"
			keep.Location = "eastus"
			_, req := stackDeletePlanFixture(t, false, contracts.ActionImpact{Asset: member, ControllerID: "stack", Delete: true}, contracts.ActionImpact{Asset: keep, ControllerID: "stack", Delete: false})
			req.IdempotencyKey = "final-products-job"
			root := map[string]any{"id": req.Asset.Identity.NativeID, "type": deploymentStackType, "systemData": map[string]any{"createdAt": "2020-02-01T01:01:01.1075056Z"}, "properties": map[string]any{"resources": []any{map[string]any{"id": member.Identity.NativeID, "status": "managed", "denyStatus": "none"}, map[string]any{"id": keep.Identity.NativeID, "status": "managed", "denyStatus": "none"}}}}
			groupRaw := map[string]any{"id": keep.Identity.NativeID, "type": groupType, "location": "eastus", "properties": map[string]any{"provisioningState": "Succeeded"}}
			_, endpoint, _ := stackPollFixture()
			endpoint += "&unmanageAction.ResourcesWithoutDeleteSupport=fail"
			final, gone, keepGone, recreated := false, false, false, false
			deletes, calls, polls, memberReads := 0, 0, 0, 0
			r := protocolRuntime(t, func(q *http.Request) (*http.Response, error) {
				if final {
					calls++
					if q.Method != "GET" {
						t.Fatal("final observation mutated cloud state", q.Method, q.URL)
					}
				}
				if reply, handled := emptyMonitorIndexResponse(t, q); handled {
					return reply, nil
				}
				if reply, handled := emptyDiagnosticSourceIndexResponse(t, q); handled {
					return reply, nil
				}
				if q.URL.String() == endpoint {
					polls++
					state := "succeeded"
					if mode == "operation_pending" {
						state = "deletingResources"
					}
					if mode == "operation_failed" {
						state = "failed"
					}
					return jsonResponse(200, map[string]any{"id": q.URL.Path, "name": testTenant, "status": state}, nil), nil
				}
				switch path := strings.ToLower(q.URL.Path); path {
				case req.Asset.Identity.NativeID:
					if final && mode != "root_pending" && !recreated {
						return jsonResponse(404, map[string]any{}, nil), nil
					}
					return jsonResponse(200, root, nil), nil
				case keep.Identity.NativeID:
					if keepGone {
						return jsonResponse(404, map[string]any{}, nil), nil
					}
					return jsonResponse(200, groupRaw, nil), nil
				case member.Identity.NativeID:
					if q.Method == "DELETE" {
						deletes++
						gone = true
						return jsonResponse(200, map[string]any{}, nil), nil
					}
					if final {
						memberReads++
						if memberReads == 2 {
							switch mode {
							case "product_forbidden":
								return jsonResponse(403, map[string]any{}, nil), nil
							case "product_residual":
								return jsonResponse(200, raw, nil), nil
							case "root_recreated_during_product":
								recreated = true
								object(root["systemData"])["createdAt"] = "2026-09-16T01:00:00Z"
							case "retained_missing_during_product":
								keepGone = true
							}
						}
					}
					if gone {
						return jsonResponse(404, map[string]any{}, nil), nil
					}
					return jsonResponse(200, raw, nil), nil
				case "/subscriptions/" + testSubscription + "/providers/microsoft.authorization/locks":
					return jsonResponse(200, map[string]any{"value": []any{}}, nil), nil
				default:
					t.Fatalf("unexpected final product request %s %s", q.Method, q.URL)
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
			var receipt, waiting map[string]any
			if mode != "native_cascade" {
				for step := 0; step < 4; step++ {
					out, err := r.deploymentStackExecuteMember(t.Context(), req, member.ID, receipt)
					if err != nil {
						t.Fatal("member execution", step, err)
					}
					wire, err := json.Marshal(out.Data)
					if err != nil || json.Unmarshal(wire, &receipt) != nil {
						t.Fatal("member checkpoint", err)
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
				if receipt["phase"] != "complete" || deletes != 1 {
					t.Fatal("member incomplete", receipt, deletes)
				}
			} else {
				gone = true
			}
			// Simulate the accepted native Stack DELETE response. This helper does not
			// enable or execute the unfinished Stack action driver.
			stackReceipt, err := c.deploymentStackExecutionReceipt(req, "eastus", response{status: 202, header: http.Header{"Azure-Asyncoperation": {endpoint}}})
			if err != nil {
				t.Fatal(err)
			}
			executions := []map[string]any{}
			if receipt != nil {
				executions = append(executions, receipt)
			}
			switch mode {
			case "changed_member_receipt":
				receipt["binding"] = "changed"
			case "waiting_member_receipt":
				executions = []map[string]any{waiting}
			case "duplicate_member_receipt":
				executions = append(executions, receipt)
			case "changed_stack_receipt":
				stackReceipt["binding"] = "changed"
			case "changed_job":
				req.IdempotencyKey = "other-job"
			}
			before, _ := json.Marshal(req)
			final = true
			out, err := r.deploymentStackObserveProductOutcome(t.Context(), req, "eastus", stackReceipt, executions...)
			success := mode == "complete" || mode == "native_cascade" || mode == "operation_pending" || mode == "root_pending" || mode == "product_residual"
			if !success {
				if err == nil || out.ProductsReconciled || out.Products != nil || out.Execution.Operation.Data != nil {
					t.Fatal("partial final observation escaped", out, err)
				}
				if (strings.HasPrefix(mode, "changed_") || mode == "waiting_member_receipt" || mode == "duplicate_member_receipt") && calls != 0 {
					t.Fatal("invalid receipt reached native HTTP", calls)
				}
				return
			}
			complete := mode == "complete" || mode == "native_cascade"
			if err != nil || out.ProductsReconciled != complete || len(out.Products) != 1 || polls != 1 || memberReads < 4 {
				t.Fatal("incorrect product reconciliation", out, err, polls, memberReads)
			}
			if mode == "product_residual" && (!out.Execution.ResourcesReconciled || !out.Products[member.ID].Exists) {
				t.Fatal("ARM absence hid product residual", out)
			}
			after, _ := json.Marshal(req)
			if string(before) != string(after) {
				t.Fatal("final readback changed frozen plan")
			}
			if complete {
				wire, err := json.Marshal(out.Execution.Operation.Data)
				if err != nil || json.Unmarshal(wire, &stackReceipt) != nil {
					t.Fatal(err)
				}
				priorReads := memberReads
				out, err = r.deploymentStackObserveProductOutcome(t.Context(), req, "eastus", stackReceipt, executions...)
				if err != nil || !out.ProductsReconciled || polls != 1 || memberReads <= priorReads {
					t.Fatal("resume reused stale product observations", out, err, polls)
				}
				keepGone = true
				out, err = r.deploymentStackObserveProductOutcome(t.Context(), req, "eastus", stackReceipt, executions...)
				if err == nil || out.ProductsReconciled || out.Products != nil {
					t.Fatal("completed receipt hid lost retained group", out, err)
				}
			}
		})
	}
}
