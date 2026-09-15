package azure

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func TestDeploymentStackPrerequisiteExecution(t *testing.T) {
	for _, mode := range []string{"complete", "pending", "operation_failed", "delete_forbidden", "readback_forbidden", "changed_job", "changed_state", "unknown_state_field", "changed_active_receipt", "resume_progress", "missing_job", "root_changed", "completed_child_returns"} {
		t.Run(mode, func(t *testing.T) {
			parent := actionAsset(hostGroupType, "parent")
			a, z := actionAsset(hostType, "a"), actionAsset(hostType, "z")
			a.Identity.NativeID = parent.Identity.NativeID + "/hosts/a"
			z.Identity.NativeID = parent.Identity.NativeID + "/hosts/z"
			_, req := stackDeletePlanFixture(t, false, contracts.ActionImpact{Asset: parent, ControllerID: "stack", Delete: true}, contracts.ActionImpact{Asset: z, ControllerID: "stack", Delete: true}, contracts.ActionImpact{Asset: a, ControllerID: "stack", Delete: true})
			req.IdempotencyKey = "prerequisite-stage-job"
			root := map[string]any{"id": req.Asset.Identity.NativeID, "type": deploymentStackType, "systemData": map[string]any{"createdAt": "2020-02-01T01:01:01.1075056Z"}, "properties": map[string]any{"resources": []any{map[string]any{"id": parent.Identity.NativeID, "status": "managed", "denyStatus": "none"}, map[string]any{"id": a.Identity.NativeID, "status": "managed", "denyStatus": "none"}, map[string]any{"id": z.Identity.NativeID, "status": "managed", "denyStatus": "none"}}}}
			parentRaw := map[string]any{"id": parent.Identity.NativeID, "type": hostGroupType, "location": "eastus", "properties": map[string]any{"provisioningState": "Succeeded"}}
			raw := map[string]map[string]any{}
			for _, id := range []string{a.Identity.NativeID, z.Identity.NativeID} {
				raw[id] = map[string]any{"id": id, "type": hostType, "location": "eastus", "properties": map[string]any{"provisioningState": "Succeeded"}}
			}
			gone := map[string]bool{}
			deletes := []string{}
			calls, lists := 0, 0
			fault := false
			kind, _ := findType(hostType)
			operation := apiURL("/subscriptions/"+testSubscription+"/providers/Microsoft.Compute/locations/eastus/operations/"+testTenant, kind.Version)
			activeTarget := ""
			polls := map[string]int{}
			r := protocolRuntime(t, func(q *http.Request) (*http.Response, error) {
				calls++
				if reply, handled := emptyMonitorIndexResponse(t, q); handled {
					return reply, nil
				}
				if reply, handled := emptyDiagnosticSourceIndexResponse(t, q); handled {
					return reply, nil
				}
				if q.URL.String() == operation {
					polls[activeTarget]++
					status := "Succeeded"
					if polls[activeTarget] == 1 {
						status = "Running"
					}
					if mode == "operation_failed" {
						status = "Failed"
					}
					if status == "Succeeded" {
						gone[activeTarget] = true
					}
					return jsonResponse(200, map[string]any{"status": status}, nil), nil
				}
				path := strings.ToLower(q.URL.Path)
				if child, exists := raw[path]; exists {
					if q.Method == "DELETE" {
						deletes = append(deletes, path)
						if mode == "delete_forbidden" {
							return jsonResponse(403, map[string]any{}, nil), nil
						}
						if mode == "pending" || mode == "operation_failed" {
							activeTarget = path
							headers := http.Header{}
							headers.Set("Azure-AsyncOperation", operation)
							return jsonResponse(202, map[string]any{}, headers), nil
						}
						gone[path] = true
						return jsonResponse(200, map[string]any{}, nil), nil
					}
					if mode == "readback_forbidden" && fault {
						return jsonResponse(403, map[string]any{}, nil), nil
					}
					if gone[path] {
						return jsonResponse(404, map[string]any{}, nil), nil
					}
					return jsonResponse(200, child, nil), nil
				}
				if q.Method != "GET" {
					t.Fatal("stage mutated parent or Stack", q.Method, q.URL)
				}
				switch path {
				case req.Asset.Identity.NativeID:
					return jsonResponse(200, root, nil), nil
				case parent.Identity.NativeID:
					return jsonResponse(200, parentRaw, nil), nil
				case parent.Identity.NativeID + "/hosts":
					lists++
					rows := []any{}
					for _, id := range []string{z.Identity.NativeID, a.Identity.NativeID} {
						if !gone[id] {
							rows = append(rows, raw[id])
						}
					}
					return jsonResponse(200, map[string]any{"value": rows}, nil), nil
				case "/subscriptions/" + testSubscription + "/resourcegroups/test":
					return jsonResponse(200, map[string]any{"id": path, "type": groupType}, nil), nil
				case "/subscriptions/" + testSubscription + "/providers/microsoft.authorization/locks":
					return jsonResponse(200, map[string]any{"value": []any{}}, nil), nil
				default:
					t.Fatalf("unexpected prerequisite stage request %s", q.URL)
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
			if mode == "missing_job" {
				req.IdempotencyKey = ""
			}
			before, _ := json.Marshal(req)
			initialCalls := calls
			out, err := r.deploymentStackAdvancePrerequisites(t.Context(), req, deploymentStackProgress{}, nil)
			if mode == "delete_forbidden" || mode == "missing_job" {
				if err == nil || out.Data != nil || out.Done {
					t.Fatal("invalid stage started", out, err)
				}
				if mode == "missing_job" && (calls != initialCalls || len(deletes) != 0) {
					t.Fatal("missing job reached native HTTP")
				}
				return
			}
			if err != nil || out.Done || !slices.Equal(deletes, []string{a.Identity.NativeID}) {
				t.Fatal("wrong first native step", out, err, deletes)
			}
			fault = true
			for step := 0; step < 10; step++ {
				wire, err := json.Marshal(out.Data)
				if err != nil {
					t.Fatal(err)
				}
				var saved map[string]any
				if err = json.Unmarshal(wire, &saved); err != nil {
					t.Fatal(err)
				}
				if step == 0 {
					switch mode {
					case "changed_job":
						req.IdempotencyKey = "other-job"
					case "changed_state":
						saved["binding"] = "changed"
					case "unknown_state_field":
						object(saved["state"])["skip_validation"] = true
					case "changed_active_receipt":
						object(object(saved["state"])["active"])["binding"] = "changed"
					case "root_changed":
						root["tags"] = map[string]any{"changed": "yes"}
					}
				}
				priorCalls, priorDeletes, priorLists := calls, len(deletes), lists
				if mode == "resume_progress" {
					out, err = r.deploymentStackAdvancePrerequisites(t.Context(), req, deploymentStackProgress{Executions: []map[string]any{{}}}, saved)
				} else {
					out, err = r.deploymentStackAdvancePrerequisites(t.Context(), req, deploymentStackProgress{}, saved)
				}
				if mode != "complete" && mode != "pending" && mode != "completed_child_returns" {
					if err == nil || out.Done || out.Data != nil || len(deletes) != 1 {
						t.Fatal("invalid stage resumed", out, err, deletes)
					}
					if mode != "root_changed" && mode != "readback_forbidden" && mode != "operation_failed" && calls != priorCalls {
						t.Fatal("invalid checkpoint reached HTTP", calls, priorCalls)
					}
					return
				}
				if err != nil || len(deletes) > priorDeletes+1 {
					t.Fatal("stage failed or chained mutations", out, err, deletes)
				}
				var priorState deploymentStackPrerequisiteState
				stateWire, _ := json.Marshal(saved["state"])
				if err := json.Unmarshal(stateWire, &priorState); err != nil {
					t.Fatal(err)
				}
				if priorState.Active != nil && lists != priorLists {
					t.Fatal("active missing member was re-enumerated before readback")
				}
				if out.Done {
					break
				}
			}
			if !out.Done || !slices.Equal(deletes, []string{a.Identity.NativeID, z.Identity.NativeID}) {
				t.Fatal("stage did not complete once per prerequisite", out, deletes)
			}
			if mode == "pending" && (polls[a.Identity.NativeID] != 2 || polls[z.Identity.NativeID] != 2) {
				t.Fatal("native pending phases were skipped", polls)
			}
			if mode == "complete" {
				initial := out.Data["state"].(deploymentStackPrerequisiteState).Progress
				beforeProgress, _ := json.Marshal(initial)
				probe, err := r.deploymentStackAdvancePrerequisites(t.Context(), req, initial, nil)
				if err != nil || !probe.Done {
					t.Fatal("completed initial progress rejected", probe, err)
				}
				copied := probe.Data["state"].(deploymentStackPrerequisiteState)
				copied.Progress.Executions[0]["phase"] = "changed-by-caller"
				afterProgress, _ := json.Marshal(initial)
				if string(beforeProgress) != string(afterProgress) {
					t.Fatal("returned stage state aliases caller progress")
				}
			}
			wire, _ := json.Marshal(out.Data)
			var saved map[string]any
			if err = json.Unmarshal(wire, &saved); err != nil {
				t.Fatal(err)
			}
			priorCalls := calls
			if mode == "completed_child_returns" {
				gone[a.Identity.NativeID] = false
			}
			out, err = r.deploymentStackAdvancePrerequisites(t.Context(), req, deploymentStackProgress{}, saved)
			if mode == "completed_child_returns" {
				if err == nil || out.Done || out.Data != nil {
					t.Fatal("completed checkpoint hid returned prerequisite", out, err)
				}
			} else if err != nil || !out.Done || calls <= priorCalls {
				t.Fatal("completed stage reused stale closure", out, err)
			}
			after, _ := json.Marshal(req)
			if string(before) != string(after) {
				t.Fatal("stage changed frozen request")
			}
		})
	}
}
