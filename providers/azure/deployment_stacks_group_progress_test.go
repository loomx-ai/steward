package azure

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func TestDeploymentStackGroupClosureWithCompletedMember(t *testing.T) {
	for _, mode := range []string{"complete", "stale", "converging", "duplicate", "unreviewed", "unreviewed_rbac", "unreviewed_diagnostic", "reappeared", "forbidden", "own_forbidden", "foreign", "no_receipt", "waiting_receipt", "tampered", "group_changed", "root_changed"} {
		t.Run(mode, func(t *testing.T) {
			group := actionAsset(groupType, "test")
			group.ID = "group"
			group.Identity.NativeID = "/subscriptions/" + testSubscription + "/resourcegroups/test"
			member := actionAsset(diskType, "disk")
			raw := nativeResource(diskType, "disk", "eastus", map[string]any{"uniqueId": "original", "provisioningState": "Succeeded"})
			member.Normalized = map[string]any{"_arm_creation_generation": creationGeneration(raw)}
			_, req := stackDeletePlanFixture(t, false, contracts.ActionImpact{Asset: group, ControllerID: "stack", Delete: true}, contracts.ActionImpact{Asset: member, ControllerID: group.ID, Delete: true})
			req.IdempotencyKey = "group-progress-job"
			root := map[string]any{"id": req.Asset.Identity.NativeID, "type": deploymentStackType, "systemData": map[string]any{"createdAt": "2020-02-01T01:01:01.1075056Z"}, "properties": map[string]any{"resources": []any{map[string]any{"id": group.Identity.NativeID, "status": "managed", "denyStatus": "none"}}}}
			groupRaw := map[string]any{"id": group.Identity.NativeID, "type": groupType, "location": "eastus", "properties": map[string]any{"provisioningState": "Succeeded"}}
			gone, active := false, false
			deletes, lists, ownReads := 0, 0, 0
			r := protocolRuntime(t, func(q *http.Request) (*http.Response, error) {
				if active && q.Method != "GET" {
					t.Fatal("closure mutated cloud state", q.Method, q.URL)
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
				case group.Identity.NativeID:
					return jsonResponse(200, groupRaw, nil), nil
				case member.Identity.NativeID:
					if q.Method == "DELETE" {
						deletes++
						gone = true
						return jsonResponse(200, map[string]any{}, nil), nil
					}
					if active {
						ownReads++
						if mode == "own_forbidden" {
							return jsonResponse(403, map[string]any{}, nil), nil
						}
					}
					if gone {
						return jsonResponse(404, map[string]any{}, nil), nil
					}
					return jsonResponse(200, raw, nil), nil
				case group.Identity.NativeID + "/resources":
					lists++
					if q.URL.Query().Get("api-version") != resourcesVersion || q.URL.Query().Has("$filter") {
						t.Fatal("filtered group index", q.URL)
					}
					if mode == "forbidden" {
						return jsonResponse(403, map[string]any{}, nil), nil
					}
					if mode == "reappeared" {
						gone = false
					}
					if mode == "group_changed" && lists == 2 {
						groupRaw["tags"] = map[string]any{"changed": "yes"}
					}
					if mode == "root_changed" {
						root["tags"] = map[string]any{"changed": "yes"}
					}
					entry := map[string]any{"id": member.Identity.NativeID, "type": diskType}
					rows := []any{}
					switch mode {
					case "stale", "duplicate", "reappeared":
						rows = append(rows, entry)
					case "converging":
						if lists == 1 {
							rows = append(rows, entry)
						}
					case "foreign":
						entry["id"] = strings.Replace(member.Identity.NativeID, "/resourcegroups/test/", "/resourcegroups/other/", 1)
						rows = append(rows, entry)
					case "unreviewed":
						entry["id"] = resourceID(diskType, "unreviewed")
						rows = append(rows, entry)
					case "unreviewed_rbac":
						entry["id"] = group.Identity.NativeID + "/providers/Microsoft.Authorization/roleAssignments/" + testTenant
						entry["type"] = "Microsoft.Authorization/roleAssignments"
						rows = append(rows, entry)
					case "unreviewed_diagnostic":
						entry["id"] = member.Identity.NativeID + "/providers/Microsoft.Insights/diagnosticSettings/setting"
						entry["type"] = diagnosticSettingsType
						rows = append(rows, entry)
					}
					if mode == "duplicate" {
						rows = append(rows, entry)
					}
					return jsonResponse(200, map[string]any{"value": rows}, nil), nil
				case "/subscriptions/" + testSubscription + "/providers/microsoft.authorization/locks":
					return jsonResponse(200, map[string]any{"value": []any{}}, nil), nil
				default:
					t.Fatalf("unexpected group progress request %s %s", q.Method, q.URL)
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
				out, err := r.deploymentStackExecuteMember(t.Context(), req, member.ID, saved)
				if err != nil {
					t.Fatal("native member lifecycle", step, err)
				}
				wire, err := json.Marshal(out.Data)
				if err != nil || json.Unmarshal(wire, &saved) != nil {
					t.Fatal("checkpoint", err)
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
				t.Fatal("member lifecycle incomplete", saved, deletes)
			}
			progress := deploymentStackProgress{Executions: []map[string]any{saved}}
			switch mode {
			case "no_receipt":
				progress.Executions = nil
			case "waiting_receipt":
				progress.Executions = []map[string]any{waiting}
			case "tampered":
				saved["binding"] = "invalid"
			}
			before, _ := json.Marshal(req)
			active = true
			groups, err := r.deploymentStackObserveGroupClosureWithProgress(t.Context(), req, progress)
			if mode == "complete" || mode == "stale" || mode == "converging" {
				if err != nil || !slices.Equal(groups, []asset.AssetID{group.ID}) || lists != 2 || ownReads < 4 {
					t.Fatal("completed member rejected", groups, err, lists, ownReads)
				}
			} else if err == nil || groups != nil {
				t.Fatal("invalid closure accepted", groups, err)
			}
			expected := map[string]string{"duplicate": "invalid_deployment_stack_group_resource", "foreign": "invalid_deployment_stack_group_resource", "unreviewed": "deployment_stack_group_resource_not_reviewed", "unreviewed_rbac": "deployment_stack_group_resource_not_reviewed", "unreviewed_diagnostic": "deployment_stack_group_resource_not_reviewed", "reappeared": "deployment_stack_completed_group_member_reappeared", "group_changed": "deployment_stack_group_changed_during_read"}[mode]
			if expected != "" && (err == nil || !strings.Contains(err.Error(), expected)) {
				t.Fatal("wrong closure rejection", mode, err)
			}
			if (mode == "tampered" || mode == "waiting_receipt") && (lists != 0 || ownReads != 0) {
				t.Fatal("invalid receipt reached member HTTP", lists, ownReads)
			}
			after, _ := json.Marshal(req)
			if string(before) != string(after) || deletes != 1 {
				t.Fatal("closure changed request or repeated deletion")
			}
		})
	}
}
