package azure

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestDeploymentStackRetainedGroupNativeContract(t *testing.T) {
	for _, mode := range []string{"complete", "pending", "group_missing", "group_forbidden", "wrong_id", "missing_location", "missing_planned_location", "location_changed", "second_read_missing", "second_read_location_changed", "not_ready", "known_birth", "known_birth_lost", "known_birth_changed", "changed_job"} {
		t.Run(mode, func(t *testing.T) {
			_, initial := stackDeletePlanFixture(t, false)
			vm := initial.LifecycleImpacts[0]
			group := vm
			group.Asset.ID = "retained-group"
			group.Asset.Identity.NativeID = strings.Split(vm.Asset.Identity.NativeID, "/providers/")[0]
			group.Asset.Identity.NativeType = groupType
			group.Asset.Location = "eastus"
			group.Asset.Normalized = map[string]any{}
			group.Delete = false
			// These are ResourceGroups_Get contract fields; no invented creation token.
			raw := map[string]any{"id": group.Asset.Identity.NativeID, "type": groupType, "name": "test", "location": "eastus", "tags": map[string]any{"owner": "fixture"}, "properties": map[string]any{"provisioningState": "Succeeded"}}
			if strings.HasPrefix(mode, "known_birth") {
				raw["systemData"] = map[string]any{"createdAt": "2020-02-01T01:01:01Z"}
				group.Asset.Normalized["_arm_creation_generation"] = creationGeneration(raw)
				if mode == "known_birth_lost" {
					delete(raw, "systemData")
				}
				if mode == "known_birth_changed" {
					object(raw["systemData"])["createdAt"] = "2026-09-16T00:00:00Z"
				}
			}
			if mode == "missing_planned_location" {
				group.Asset.Location = ""
			}
			c, req := stackDeletePlanFixture(t, false, group, vm)
			req.LifecycleImpacts[1].ControllerID = group.Asset.ID
			req.IdempotencyKey = "delete-resources-retain-group"
			_, parameters, err := c.deploymentStackDeletePlan(req)
			if err != nil || parameters["unmanageAction.Resources"] != "delete" || parameters["unmanageAction.ResourceGroups"] != "detach" {
				t.Fatal(parameters, err)
			}
			_, endpoint, _ := stackPollFixture()
			endpoint += "&unmanageAction.ResourcesWithoutDeleteSupport=fail"
			response := response{status: 204}
			if mode == "pending" {
				response.status = 202
				response.header = http.Header{"Azure-Asyncoperation": {endpoint}}
			}
			saved, err := c.deploymentStackExecutionReceipt(req, "eastus", response)
			if err != nil {
				t.Fatal(err)
			}
			wire, err := json.Marshal(req)
			if err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(wire, &req); err != nil {
				t.Fatal(err)
			}
			wire, err = json.Marshal(saved)
			if err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(wire, &saved); err != nil {
				t.Fatal(err)
			}
			if mode == "changed_job" {
				req.IdempotencyKey = "other-job"
			}
			calls, groups, vms, roots := 0, 0, 0, 0
			c.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.Method != "GET" {
					t.Fatal("readback mutated a resource")
				}
				if r.URL.String() == endpoint {
					return jsonResponse(200, map[string]any{"id": r.URL.Path, "name": testTenant, "status": "deletingResources"}, nil), nil
				}
				if strings.EqualFold(r.URL.Path, req.Asset.Identity.NativeID) {
					roots++
					return jsonResponse(404, map[string]any{}, nil), nil
				}
				if strings.EqualFold(r.URL.Path, vm.Asset.Identity.NativeID) {
					vms++
					return jsonResponse(404, map[string]any{}, nil), nil
				}
				if !strings.EqualFold(r.URL.Path, group.Asset.Identity.NativeID) {
					t.Fatal("unexpected resource", r.URL.Path)
				}
				groups++
				switch mode {
				case "group_missing":
					return jsonResponse(404, map[string]any{}, nil), nil
				case "group_forbidden":
					return jsonResponse(403, map[string]any{}, nil), nil
				case "wrong_id":
					raw["id"] = group.Asset.Identity.NativeID + "-other"
				case "missing_location":
					delete(raw, "location")
				case "location_changed":
					raw["location"] = "westus"
				case "not_ready":
					object(raw["properties"])["provisioningState"] = "Deleting"
				case "second_read_missing":
					if groups == 2 {
						return jsonResponse(404, map[string]any{}, nil), nil
					}
				case "second_read_location_changed":
					if groups == 2 {
						raw["location"] = "westus"
					}
				}
				return jsonResponse(200, raw, nil), nil
			})
			out, err := c.deploymentStackObserveExecution(t.Context(), req, "eastus", saved)
			success := mode == "complete" || mode == "pending" || mode == "known_birth"
			if !success {
				if err == nil || out.ResourcesReconciled || out.Outcome.RetentionEvidence != nil || out.Outcome.MembersAbsent != nil || out.Operation.Data != nil {
					t.Fatal("invalid retention accepted", mode, out, err)
				}
				if mode == "changed_job" && calls != 0 {
					t.Fatal("changed execution reached HTTP")
				}
				return
			}
			evidence := "resource_id_and_location"
			if mode == "known_birth" {
				evidence = "creation_identity"
			}
			if err != nil || out.ResourcesReconciled != (mode != "pending") || out.Outcome.RetentionEvidence[group.Asset.ID] != evidence || out.Outcome.MembersAbsent[group.Asset.ID] || !out.Outcome.MembersAbsent[vm.Asset.ID] || groups != 2 || vms != 1 || roots != 2 {
				t.Fatal(mode, out, err, groups, vms, roots)
			}
			if _, exists := out.Outcome.RetentionEvidence[vm.Asset.ID]; exists {
				t.Fatal("deleted VM reported as retained")
			}
		})
	}
}
