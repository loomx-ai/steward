package azure

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func TestDeploymentStackIntrinsicProductPreflight(t *testing.T) {
	for _, mode := range []string{"complete", "parent_protected", "child_protected", "child_lock", "parent_lock", "late_child_lock", "list_forbidden", "child_forbidden", "missing_child", "new_child", "monitor_forbidden", "child_monitor_forbidden"} {
		t.Run(mode, func(t *testing.T) {
			parent, child := actionAsset(sqlServerType, "z-parent"), actionAsset(sqlDatabaseType, "a-master")
			child.Identity.NativeID = parent.Identity.NativeID + "/databases/master"
			_, req := stackDeletePlanFixture(t, false, contracts.ActionImpact{Asset: child, ControllerID: parent.ID, Delete: true}, contracts.ActionImpact{Asset: parent, ControllerID: "stack", Delete: true})
			req.IdempotencyKey = "sql-cascade-job"
			req.Asset.Location = "eastus"
			root := map[string]any{"id": req.Asset.Identity.NativeID, "type": deploymentStackType, "location": "eastus", "systemData": map[string]any{"createdAt": "2020-02-01T01:01:01.1075056Z"}, "properties": map[string]any{"resources": []any{map[string]any{"id": parent.Identity.NativeID, "status": "managed", "denyStatus": "none"}}}}
			parentRaw := map[string]any{"id": parent.Identity.NativeID, "type": sqlServerType, "location": "eastus", "properties": map[string]any{"state": "Ready"}}
			childRaw := map[string]any{"id": child.Identity.NativeID, "type": sqlDatabaseType, "location": "eastus", "properties": map[string]any{"status": "Online"}}
			if mode == "parent_protected" {
				parentRaw["tags"] = map[string]any{"steward/protected": "true"}
			}
			if mode == "child_protected" {
				childRaw["tags"] = map[string]any{"steward/protected": "true"}
			}
			deletes, locks, monitorReads := 0, 0, 0
			gone := false
			r := protocolRuntime(t, func(q *http.Request) (*http.Response, error) {
				path := strings.ToLower(q.URL.Path)
				if q.Method != "GET" {
					if q.Method != "DELETE" || path != req.Asset.Identity.NativeID || mode != "complete" {
						t.Fatal("unexpected mutation", q.Method, q.URL)
					}
					deletes++
					gone = true
					return &http.Response{StatusCode: 204, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(""))}, nil
				}
				if strings.HasSuffix(path, "/providers/microsoft.insights/metricalerts") {
					monitorReads++
					if mode == "monitor_forbidden" || mode == "child_monitor_forbidden" && monitorReads > 2 {
						return jsonResponse(403, map[string]any{}, nil), nil
					}
				}
				if reply, handled := emptyMonitorIndexResponse(t, q); handled {
					return reply, nil
				}
				if reply, handled := emptyDiagnosticSourceIndexResponse(t, q); handled {
					return reply, nil
				}
				switch path {
				case req.Asset.Identity.NativeID:
					if gone {
						return jsonResponse(404, map[string]any{}, nil), nil
					}
					return jsonResponse(200, root, nil), nil
				case parent.Identity.NativeID:
					if gone {
						return jsonResponse(404, map[string]any{}, nil), nil
					}
					return jsonResponse(200, parentRaw, nil), nil
				case child.Identity.NativeID:
					if mode == "child_forbidden" {
						return jsonResponse(403, map[string]any{}, nil), nil
					}
					if gone {
						return jsonResponse(404, map[string]any{}, nil), nil
					}
					return jsonResponse(200, childRaw, nil), nil
				case parent.Identity.NativeID + "/databases/other":
					return jsonResponse(200, map[string]any{"id": path, "type": sqlDatabaseType, "location": "eastus", "properties": map[string]any{}}, nil), nil
				case parent.Identity.NativeID + "/databases":
					if mode == "list_forbidden" {
						return jsonResponse(403, map[string]any{}, nil), nil
					}
					rows := []any{childRaw}
					if gone || mode == "missing_child" {
						rows = nil
					}
					if mode == "new_child" {
						rows = append(rows, map[string]any{"id": parent.Identity.NativeID + "/databases/other", "type": sqlDatabaseType})
					}
					return jsonResponse(200, map[string]any{"value": rows}, nil), nil
				case parent.Identity.NativeID + "/elasticpools":
					return jsonResponse(200, map[string]any{"value": []any{}}, nil), nil
				case "/subscriptions/" + testSubscription + "/resourcegroups/test":
					return jsonResponse(200, map[string]any{"id": path, "type": groupType, "location": "eastus"}, nil), nil
				case "/subscriptions/" + testSubscription + "/providers/microsoft.authorization/locks":
					locks++
					rows := []any{}
					if mode == "child_lock" || mode == "parent_lock" || mode == "late_child_lock" && locks > 1 {
						scope := child.Identity.NativeID
						if mode == "parent_lock" {
							scope = parent.Identity.NativeID
						}
						rows = append(rows, map[string]any{"id": scope + "/providers/Microsoft.Authorization/locks/hold", "properties": map[string]any{"level": "CanNotDelete"}})
					}
					return jsonResponse(200, map[string]any{"value": rows}, nil), nil
				default:
					t.Fatalf("unexpected intrinsic preflight request %s", q.URL)
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
			before, _ := json.Marshal(req)
			checks, err := r.deploymentStackPreflightProducts(t.Context(), req)
			allowed := err == nil && len(checks) == 2
			for _, check := range checks {
				allowed = allowed && check.Check.Allowed && !check.Check.Absent
			}
			if mode != "complete" {
				if allowed || deletes != 0 {
					t.Fatal("unsafe intrinsic plan accepted", checks, err)
				}
				if err != nil && checks != nil {
					t.Fatal("partial preflight result", checks, err)
				}
				return
			}
			if !allowed || locks < 2 || monitorReads < 4 {
				t.Fatal("intrinsic cascade rejected or child checks skipped", checks, err, locks, monitorReads)
			}
			// The same resolved product remains protected when requested independently.
			driver, err := r.ResolveAction(t.Context(), "connection", child)
			if err != nil {
				t.Fatal(err)
			}
			member, err := c.deploymentStackMemberRequest(req, child.ID)
			if err != nil {
				t.Fatal(err)
			}
			standalone, err := driver.Preflight(t.Context(), member)
			if err != nil || standalone.Allowed || standalone.Reason != "azure_system_database" {
				t.Fatal("standalone protection changed", standalone, err)
			}
			if _, err = driver.Execute(t.Context(), member); err == nil || deletes != 0 {
				t.Fatal("standalone system database deletion permitted", err)
			}
			var setup map[string]any
			ready := false
			for step := 0; step < 5; step++ {
				out, err := r.deploymentStackAdvanceSetup(t.Context(), req, setup)
				if err != nil {
					t.Fatal(err)
				}
				wire, _ := json.Marshal(out.Data)
				setup = nil
				if err = json.Unmarshal(wire, &setup); err != nil {
					t.Fatal(err)
				}
				if out.Done {
					ready = true
					break
				}
			}
			if !ready {
				t.Fatal("setup did not finish")
			}
			result, err := r.deploymentStackStartDelete(t.Context(), req, setup)
			if err != nil || deletes != 1 {
				t.Fatal("native Stack cascade failed", result, err, deletes)
			}
			wire, _ := json.Marshal(result.Data)
			var saved map[string]any
			if err = json.Unmarshal(wire, &saved); err != nil {
				t.Fatal(err)
			}
			for step := 0; step < 2; step++ {
				out, err := r.deploymentStackResumeDeletion(t.Context(), req, saved)
				if err != nil || !out.Products.ProductsReconciled || deletes != 1 {
					t.Fatal("native cascade readback failed", out, err, deletes)
				}
				saved = out.Data
			}
			after, _ := json.Marshal(req)
			if string(before) != string(after) {
				t.Fatal("frozen plan mutated")
			}
		})
	}
}
