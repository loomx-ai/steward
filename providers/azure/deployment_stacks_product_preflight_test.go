package azure

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func TestDeploymentStackProductPreflight(t *testing.T) {
	for _, mode := range []string{"complete", "protected", "locked", "managed_group", "forbidden", "missing", "disappears", "final_change", "root_change", "invalid_plan", "invalid_checkpoint", "retained", "child_lifecycle", "dependency_forbidden"} {
		t.Run(mode, func(t *testing.T) {
			kind := diskType
			if mode == "child_lifecycle" {
				kind = hostGroupType
			}
			member := actionAsset(kind, "a-member")
			member.Normalized = map[string]any{}
			choices := []contracts.ActionImpact{{Asset: member, ControllerID: "stack", Delete: mode != "retained"}}
			rows := []any{map[string]any{"id": member.Identity.NativeID, "status": "managed", "denyStatus": "none"}}
			raw := map[string]any{"id": member.Identity.NativeID, "type": kind, "location": "eastus", "etag": "original", "properties": map[string]any{"provisioningState": "Succeeded"}}
			member.Normalized["_arm_generation"] = productGeneration(raw)
			if mode == "protected" || mode == "retained" {
				raw["tags"] = map[string]any{"steward/protected": "true"}
			}
			child := member
			child.ID, child.Identity.NativeType, child.Identity.NativeID = "child", hostType, member.Identity.NativeID+"/hosts/child"
			child.Normalized = nil
			childRaw := map[string]any{"id": child.Identity.NativeID, "type": hostType, "location": "eastus", "properties": map[string]any{}}
			if mode == "child_lifecycle" {
				choices = append(choices, contracts.ActionImpact{Asset: child, ControllerID: member.ID, Delete: true})
			}
			_, req := stackDeletePlanFixture(t, false, choices...)
			req.IdempotencyKey = "stack-job"
			root := map[string]any{"id": req.Asset.Identity.NativeID, "type": deploymentStackType, "systemData": map[string]any{"createdAt": "2020-02-01T01:01:01.1075056Z"}, "properties": map[string]any{"resources": rows}}
			reads, rootReads, groupReads, calls := 0, 0, 0, 0
			r := protocolRuntime(t, func(q *http.Request) (*http.Response, error) {
				calls++
				if q.Method != "GET" {
					t.Fatal("product preflight mutated native state", q.Method, q.URL)
				}
				if mode == "dependency_forbidden" && strings.EqualFold(q.URL.Path, "/subscriptions/"+testSubscription+"/providers/Microsoft.Insights/metricAlerts") {
					return jsonResponse(403, map[string]any{}, nil), nil
				}
				if reply, handled := emptyMonitorIndexResponse(t, q); handled {
					return reply, nil
				}
				if reply, handled := emptyDiagnosticSourceIndexResponse(t, q); handled {
					return reply, nil
				}
				path := strings.ToLower(q.URL.Path)
				switch path {
				case req.Asset.Identity.NativeID:
					rootReads++
					if mode == "root_change" && rootReads > 2 {
						root["tags"] = map[string]any{"changed": "true"}
					}
					return jsonResponse(200, root, nil), nil
				case member.Identity.NativeID:
					reads++
					if mode == "forbidden" {
						return jsonResponse(403, map[string]any{}, nil), nil
					}
					if mode == "missing" || mode == "disappears" && reads > 1 {
						return jsonResponse(404, map[string]any{}, nil), nil
					}
					if mode == "final_change" && groupReads > 0 {
						raw["etag"] = "changed"
					}
					return jsonResponse(200, raw, nil), nil
				case child.Identity.NativeID:
					return jsonResponse(200, childRaw, nil), nil
				case member.Identity.NativeID + "/hosts":
					return jsonResponse(200, map[string]any{"value": []any{childRaw}}, nil), nil
				case "/subscriptions/" + testSubscription + "/resourcegroups/test":
					groupReads++
					group := map[string]any{"id": path, "type": groupType}
					if mode == "managed_group" {
						group["managedBy"] = resourceID(aksType, "cluster")
					}
					return jsonResponse(200, group, nil), nil
				case "/subscriptions/" + testSubscription + "/providers/microsoft.authorization/locks":
					locks := []any{}
					if mode == "locked" {
						locks = append(locks, map[string]any{"id": member.Identity.NativeID + "/providers/Microsoft.Authorization/locks/protect", "properties": map[string]any{"level": "CanNotDelete"}})
					}
					return jsonResponse(200, map[string]any{"value": locks}, nil), nil
				default:
					t.Fatalf("unexpected native preflight request %s", q.URL)
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
			if mode == "invalid_plan" {
				req.LifecycleImpacts = nil
			}
			var preparations []map[string]any
			if mode == "invalid_checkpoint" {
				preparations = []map[string]any{{"member": "a-member", "binding": "changed"}}
			}
			before, err := json.Marshal(req)
			if err != nil {
				t.Fatal(err)
			}
			checks, err := r.deploymentStackPreflightProducts(t.Context(), req, preparations...)
			after, marshalErr := json.Marshal(req)
			if marshalErr != nil || string(before) != string(after) {
				t.Fatal("product preflight changed the reviewed request")
			}
			if mode == "invalid_plan" || mode == "invalid_checkpoint" {
				if calls != 0 {
					t.Fatal("invalid review performed native reads", calls)
				}
			}
			switch mode {
			case "complete", "protected", "locked", "managed_group":
				if err != nil || len(checks) != 1 || checks[0].Member != member.ID || checks[0].Check.Absent {
					t.Fatal(checks, err)
				}
				if checks[0].Check.Allowed != (mode == "complete") {
					t.Fatal("product deletion guard lost", checks)
				}
				reason := map[string]string{"protected": "azure_protected_tag", "locked": "azure_management_lock", "managed_group": "azure_managed_resource_group"}[mode]
				if checks[0].Check.Reason != reason || reads < 3 || rootReads != 4 {
					t.Fatal("native product preflight or final verification missing", checks, reads, rootReads)
				}
			case "retained":
				if err != nil || len(checks) != 0 || reads != 2 || groupReads != 0 {
					t.Fatal("retention invoked product deletion preflight", checks, err, reads, groupReads)
				}
			default:
				if err == nil || len(checks) != 0 {
					t.Fatal("failed read returned partial product checks", checks, err)
				}
				if mode == "child_lifecycle" && !strings.Contains(err.Error(), "service_child_requires_prior_deletion") {
					t.Fatal("native child lifecycle guard was not preserved", err)
				}
			}
		})
	}
}

func TestDeploymentStackPreparedProductPreflight(t *testing.T) {
	for _, mode := range []string{"complete", "without_checkpoint", "changed_config", "changed_birth", "changed_during_preflight", "invalid_binding"} {
		t.Run(mode, func(t *testing.T) {
			member := actionAsset(vmType, "vm")
			raw := nativeResource(vmType, "vm", "eastus", map[string]any{"vmId": "original-vm", "provisioningState": "Succeeded", "hardwareProfile": map[string]any{"vmSize": "Standard_D2s_v3"}})
			raw["etag"] = "before-preparation"
			member.Normalized = map[string]any{"_arm_generation": productGeneration(raw), "_arm_creation_generation": creationGeneration(raw)}
			_, req := stackDeletePlanFixture(t, false, contracts.ActionImpact{Asset: member, ControllerID: "stack", Delete: true})
			req.IdempotencyKey = "prepared-stack-job"
			root := map[string]any{"id": req.Asset.Identity.NativeID, "type": deploymentStackType, "systemData": map[string]any{"createdAt": "2020-02-01T01:01:01.1075056Z"}, "properties": map[string]any{"resources": []any{map[string]any{"id": member.Identity.NativeID, "status": "managed", "denyStatus": "none"}}}}
			calls, productGroups := 0, 0
			r := protocolRuntime(t, func(q *http.Request) (*http.Response, error) {
				calls++
				if q.Method != "GET" {
					t.Fatal("product preflight repeated preparation", q.Method)
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
				case member.Identity.NativeID:
					return jsonResponse(200, raw, nil), nil
				case member.Identity.NativeID + "/extensions", "/subscriptions/" + testSubscription + "/providers/microsoft.authorization/locks":
					return jsonResponse(200, map[string]any{"value": []any{}}, nil), nil
				case "/subscriptions/" + testSubscription + "/resourcegroups/test":
					productGroups++
					if mode == "changed_during_preflight" {
						object(object(raw["properties"])["hardwareProfile"])["vmSize"] = "Standard_D8s_v3"
					}
					return jsonResponse(200, map[string]any{"id": path, "type": groupType}, nil), nil
				default:
					t.Fatalf("unexpected prepared product request %s", q.URL)
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
			raw["etag"] = "after-preparation"
			expected, err := attachmentPreparedConfiguration(vmType, raw, nil)
			if err != nil {
				t.Fatal(err)
			}
			saved := map[string]any{"member": string(member.ID), "phase": "attachments_prepared", "result": contracts.ActionResult{Data: map[string]any{"phase": "attachments_prepared"}}, "configurations": map[string]any{member.Identity.NativeID: map[string]any{"configuration": c.privateConfiguration(expected), "creation": creationGeneration(raw)}}}
			saved["binding"], err = c.deploymentStackPreparationBinding(req, saved)
			if err != nil {
				t.Fatal(err)
			}
			// Both inputs must survive worker persistence without relaxing identity.
			for _, value := range []any{&req, &saved} {
				wire, err := json.Marshal(value)
				if err != nil || json.Unmarshal(wire, value) != nil {
					t.Fatal("could not persist product preflight inputs", err)
				}
			}
			checkpoints := []map[string]any{saved}
			switch mode {
			case "without_checkpoint":
				checkpoints = nil
			case "changed_config":
				object(object(raw["properties"])["hardwareProfile"])["vmSize"] = "Standard_D8s_v3"
			case "changed_birth":
				object(raw["properties"])["vmId"] = "replacement-vm"
			case "invalid_binding":
				saved["binding"] = "changed"
			}
			before, _ := json.Marshal(req)
			checks, err := r.deploymentStackPreflightProducts(t.Context(), req, checkpoints...)
			after, _ := json.Marshal(req)
			if string(before) != string(after) {
				t.Fatal("product projection removed identity from the original plan")
			}
			if mode == "complete" {
				if err != nil || len(checks) != 1 || !checks[0].Check.Allowed || productGroups == 0 {
					t.Fatal("prepared VM did not reach its real product preflight", checks, err, productGroups)
				}
			} else if err == nil || len(checks) != 0 {
				t.Fatal("invalid prepared product returned checks", checks, err)
			}
			if mode == "invalid_binding" && calls != 0 {
				t.Fatal("invalid checkpoint reached native HTTP", calls)
			}
		})
	}
}
