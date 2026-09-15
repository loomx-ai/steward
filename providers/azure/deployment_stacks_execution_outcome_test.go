package azure

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func TestDeploymentStackExecutionReconcilesResources(t *testing.T) {
	for _, mode := range []string{"complete", "operation_pending", "member_pending", "parent_pending", "retained_missing", "pending_retained_missing", "retained_recreated", "retained_forbidden", "parent_recreated", "operation_failed", "changed_job"} {
		t.Run(mode, func(t *testing.T) {
			_, initial := stackDeletePlanFixture(t, false)
			deleted := initial.LifecycleImpacts[0]
			retained := contracts.ActionImpact{Asset: actionAsset(diskType, "keep"), ControllerID: deleted.Asset.ID}
			retained.Asset.Identity.Partition = "azure"
			retainedRaw := map[string]any{"id": retained.Asset.Identity.NativeID, "type": diskType, "properties": map[string]any{"uniqueId": "original-disk"}}
			retained.Asset.Normalized = map[string]any{"_arm_creation_generation": creationGeneration(retainedRaw)}
			c, req := stackDeletePlanFixture(t, false, deleted, retained)
			req.IdempotencyKey = "reviewed-job"
			_, endpoint, _ := stackPollFixture()
			endpoint += "&unmanageAction.ResourcesWithoutDeleteSupport=fail"
			saved, err := c.deploymentStackExecutionReceipt(req, "eastus", response{status: 202, header: http.Header{"Azure-Asyncoperation": {endpoint}}})
			if err != nil {
				t.Fatal(err)
			}
			wire, err := json.Marshal(saved)
			if err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(wire, &saved); err != nil {
				t.Fatal(err)
			}
			if mode == "changed_job" {
				req.IdempotencyKey = "other-job"
			}
			calls, polls, roots, members, keeps := 0, 0, 0, 0, 0
			retainedGone := false
			c.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.Method != "GET" {
					t.Fatal("observation mutated resources", r.Method)
				}
				if r.URL.String() == endpoint {
					polls++
					state := "succeeded"
					if mode == "operation_pending" || mode == "pending_retained_missing" {
						state = "deletingResources"
					}
					if mode == "operation_failed" {
						state = "failed"
					}
					return jsonResponse(200, map[string]any{"id": r.URL.Path, "name": testTenant, "status": state}, nil), nil
				}
				if strings.EqualFold(r.URL.Path, req.Asset.Identity.NativeID) {
					roots++
					if mode == "parent_pending" || mode == "parent_recreated" {
						birth := "2020-02-01T01:01:01.1075056Z"
						if mode == "parent_recreated" {
							birth = "2026-09-15T01:00:00Z"
						}
						return jsonResponse(200, map[string]any{"id": req.Asset.Identity.NativeID, "type": deploymentStackType, "systemData": map[string]any{"createdAt": birth}, "properties": map[string]any{"provisioningState": "deleting"}}, nil), nil
					}
					return jsonResponse(404, map[string]any{}, nil), nil
				}
				if strings.EqualFold(r.URL.Path, deleted.Asset.Identity.NativeID) {
					members++
					if mode == "member_pending" || mode == "retained_missing" {
						return jsonResponse(200, map[string]any{"id": deleted.Asset.Identity.NativeID, "type": vmType}, nil), nil
					}
					return jsonResponse(404, map[string]any{}, nil), nil
				}
				if strings.EqualFold(r.URL.Path, retained.Asset.Identity.NativeID) {
					keeps++
					if retainedGone {
						return jsonResponse(404, map[string]any{}, nil), nil
					}
					switch mode {
					case "retained_missing", "pending_retained_missing":
						return jsonResponse(404, map[string]any{}, nil), nil
					case "retained_forbidden":
						return jsonResponse(403, map[string]any{}, nil), nil
					case "retained_recreated":
						object(retainedRaw["properties"])["uniqueId"] = "replacement-disk"
					}
					return jsonResponse(200, retainedRaw, nil), nil
				}
				t.Fatalf("unexpected resource read: %s", r.URL.Path)
				return nil, nil
			})
			out, err := c.deploymentStackObserveExecution(t.Context(), req, "eastus", saved)
			success := mode == "complete" || mode == "operation_pending" || mode == "member_pending" || mode == "parent_pending"
			if !success {
				if err == nil || out.ResourcesReconciled || out.Operation.Data != nil || out.Outcome.MembersAbsent != nil {
					t.Fatal("partial result escaped failure", out, err)
				}
				if mode == "changed_job" && calls != 0 {
					t.Fatal("changed execution reached HTTP", calls)
				}
				if mode == "operation_failed" && calls != 1 {
					t.Fatal("failed native operation continued", calls)
				}
				if (mode == "retained_missing" || mode == "pending_retained_missing") && keeps != 1 {
					t.Fatal("retained absence hidden by pending deletion")
				}
				return
			}
			if err != nil || out.ResourcesReconciled != (mode == "complete") || roots != 2 || members != 1 || keeps != 1 || polls != 1 {
				t.Fatal(mode, out, err, roots, members, keeps, polls)
			}
			// A worker restart must preserve native completion but repeat every resource
			// read; a retained disk can disappear after the previous successful check.
			if mode == "complete" {
				wire, err := json.Marshal(out.Operation.Data)
				if err != nil {
					t.Fatal(err)
				}
				if err := json.Unmarshal(wire, &saved); err != nil {
					t.Fatal(err)
				}
				out, err = c.deploymentStackObserveExecution(t.Context(), req, "eastus", saved)
				if err != nil || !out.ResourcesReconciled || polls != 1 || roots != 4 || members != 2 || keeps != 2 {
					t.Fatal("resume reused stale resource observations", out, err)
				}
				retainedGone = true
				out, err = c.deploymentStackObserveExecution(t.Context(), req, "eastus", saved)
				if err == nil || out.ResourcesReconciled || out.Operation.Data != nil || polls != 1 || keeps != 3 {
					t.Fatal("completed receipt hid lost retained disk", out, err, polls, keeps)
				}

			}
		})
	}
}
