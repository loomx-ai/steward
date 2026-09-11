package azure

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func TestRBACIndependentDeletionOrderingAndRecovery(t *testing.T) {
	for _, status := range []int{200, 204} {
		for _, orphan := range []bool{false, true} {
			t.Run(http.StatusText(status)+map[bool]string{false: "/live-scope", true: "/orphan"}[orphan], func(t *testing.T) {
				f := newRBACFixture(t)
				f.deleteStatus = status
				if orphan {
					clear(f.scopes)
				}
				role := f.asset(t, rbacRoleType, rbacTestRoleID())
				assignments := []asset.Asset{}
				batch, err := f.runtime.List(t.Context(), productRequest(f.runtime, rbacAssignmentType))
				if err != nil {
					t.Fatal(err)
				}
				for _, item := range batch.Items {
					assignments = append(assignments, f.asset(t, rbacAssignmentType, item.NativeID))
				}
				roleRequest := contracts.ActionRequest{Asset: role, Action: "delete", IdempotencyKey: "delete-reviewed-role"}
				if check, err := f.action(t, role).Preflight(t.Context(), roleRequest); err == nil || check.Allowed || len(f.deleted) != 0 {
					t.Fatal("role ignored existing assignments", check, err)
				}
				for index, value := range assignments {
					f.hold = true
					request := contracts.ActionRequest{Asset: value, Action: "delete", IdempotencyKey: "remove-reviewed-assignment"}
					driver := f.action(t, value)
					result, err := driver.Execute(t.Context(), request)
					if err != nil || result.ProviderRequestID != "rbac-native-delete" || result.ProviderOperationID != "" {
						t.Fatal("native RBAC deletion failed", result, err)
					}
					pending, err := driver.Wait(t.Context(), request, result)
					if err != nil || pending.Done {
						t.Fatal("DELETE acknowledgement replaced native absence", pending, err)
					}
					serialized, _ := json.Marshal(struct {
						Request contracts.ActionRequest
						Result  contracts.ActionResult
					}{request, result})
					var recovered struct {
						Request contracts.ActionRequest
						Result  contracts.ActionResult
					}
					if err := json.Unmarshal(serialized, &recovered); err != nil {
						t.Fatal(err)
					}
					f.runtime.clients = map[asset.ConnectionID]*client{}
					driver = f.action(t, recovered.Request.Asset)
					pending, err = driver.Wait(t.Context(), recovered.Request, recovered.Result)
					if err != nil || pending.Done {
						t.Fatal("recreated driver lost pending state", pending, err)
					}
					delete(f.resources, value.Identity.NativeID)
					done, err := driver.Wait(t.Context(), recovered.Request, recovered.Result)
					if err != nil || !done.Done {
						t.Fatal("native assignment absence did not finish", done, err)
					}
					roleRequest.PrerequisiteDeletions = append(roleRequest.PrerequisiteDeletions, contracts.ActionImpact{Asset: value, ControllerID: role.ID, Delete: true})
					if index == 0 {
						if check, err := f.action(t, role).Preflight(t.Context(), roleRequest); err == nil || check.Allowed {
							t.Fatal("second assignment was not required", check, err)
						}
					}
				}
				f.hold = false
				result, err := f.action(t, role).Execute(t.Context(), roleRequest)
				if err != nil {
					t.Fatal("reviewed role deletion failed", err)
				}
				done, err := f.action(t, role).Wait(t.Context(), roleRequest, result)
				if err != nil || !done.Done || len(f.deleted) != 3 || len(f.resources) != 1 || !strings.HasSuffix(f.deleted[2], rbacTestRoleName) {
					t.Fatal("role/assignment deletion did not converge independently", done, f.deleted, err)
				}
				if !orphan && len(f.scopes) != 2 {
					t.Fatal("RBAC deletion removed independent scopes")
				}
			})
		}
	}
}

func TestRBACActionRevalidatesConfigurationScopesAndPIM(t *testing.T) {
	for _, mode := range []string{"condition", "unknown-private-field", "principal", "scope", "role", "source-recreated", "group-protected", "group-deleted", "scope-lock", "pim-eligibility", "pim-active", "scope-denied", "pim-denied", "role-definition-builtin", "shared-role"} {
		t.Run(mode, func(t *testing.T) {
			f := newRBACFixture(t)
			kind, id := rbacAssignmentType, rbacTestAssignmentID()
			if mode == "role-definition-builtin" || mode == "shared-role" {
				kind, id = rbacRoleType, rbacTestRoleID()
				for key, raw := range f.resources {
					if raw["type"] == rbacAssignmentType {
						delete(f.resources, key)
					}
				}
			}
			if mode == "source-recreated" {
				id = strings.ToLower(resourceID("Microsoft.KeyVault/vaults", "scope-vault")) + "/providers/microsoft.authorization/roleassignments/" + rbacTestSecondAssignment
			}
			value := f.asset(t, kind, id)
			driver := f.action(t, value)
			p := object(f.resources[id]["properties"])
			group := "/subscriptions/" + testSubscription + "/resourcegroups/test"
			switch mode {
			case "condition":
				p["condition"] = "changed-condition"
			case "unknown-private-field":
				p["futureCondition"] = map[string]any{"value": "changed"}
			case "principal":
				p["principalId"] = "44444444-5555-6666-7777-888888888888"
			case "scope":
				p["scope"] = "/subscriptions/" + testSubscription
			case "role":
				p["roleDefinitionId"] = "/providers/Microsoft.Authorization/roleDefinitions/" + rbacTestBuiltinName
			case "source-recreated":
				object(f.scopes[strings.ToLower(resourceID("Microsoft.KeyVault/vaults", "scope-vault"))]["systemData"])["createdAt"] = "2026-02-01T00:00:00Z"
			case "group-protected":
				f.scopes[group]["tags"] = map[string]any{"steward:protected": "true"}
			case "group-deleted":
				delete(f.scopes, group)
			case "scope-lock":
				f.locks = []any{map[string]any{"id": group + "/providers/Microsoft.Authorization/locks/keep", "properties": map[string]any{"level": "ReadOnly"}}}
			case "pim-eligibility", "pim-active":
				pimKind := rbacEligibilityType
				if mode == "pim-active" {
					pimKind = rbacScheduleType
				}
				raw := rbacTestBody(t, pimKind, group, "44444444-5555-6666-7777-888888888888")
				f.resources[strings.ToLower(text(raw["id"]))] = raw
			case "scope-denied", "pim-denied":
				f.override = func(req *http.Request) (*http.Response, bool) {
					match := strings.ToLower(req.URL.Path) == group
					if mode == "pim-denied" {
						match = strings.HasSuffix(strings.ToLower(req.URL.Path), "/roleeligibilityschedules")
					}
					if match {
						return jsonResponse(403, map[string]any{"error": map[string]any{"code": "AuthorizationFailed"}}, nil), true
					}
					return nil, false
				}
			case "role-definition-builtin":
				p["type"] = "BuiltInRole"
			case "shared-role":
				p["assignableScopes"] = []any{group, "/subscriptions/" + rbacOtherSubscription}
			}
			result, err := driver.Execute(t.Context(), contracts.ActionRequest{Asset: value, Action: "delete"})
			if err == nil || len(f.deleted) != 0 || len(result.Data) != 0 {
				t.Fatal("RBAC mutation ignored changed evidence", result, err)
			}
		})
	}
}

func TestRBACProtectedInventoryCannotAuthorizeDeletion(t *testing.T) {
	for _, mode := range []string{"builtin", "shared", "pim", "locked"} {
		t.Run(mode, func(t *testing.T) {
			f := newRBACFixture(t)
			id := rbacTestRoleID()
			root := "/subscriptions/" + testSubscription
			group := root + "/resourcegroups/test"
			for key, raw := range f.resources {
				if raw["type"] == rbacAssignmentType {
					delete(f.resources, key)
				}
			}
			switch mode {
			case "builtin":
				id = root + "/providers/microsoft.authorization/roledefinitions/" + rbacTestBuiltinName
			case "shared":
				object(f.resources[id]["properties"])["assignableScopes"] = []any{group, "/subscriptions/" + rbacOtherSubscription}
			case "pim":
				raw := rbacTestBody(t, rbacEligibilityType, group, "44444444-5555-6666-7777-888888888888")
				f.resources[strings.ToLower(text(raw["id"]))] = raw
			case "locked":
				f.locks = []any{map[string]any{"id": group + "/providers/Microsoft.Authorization/locks/keep", "properties": map[string]any{"level": "CanNotDelete"}}}
			}
			value := f.asset(t, rbacRoleType, id)
			if value.Normalized["cleanup_protected"] != true {
				t.Fatal("RBAC protection missing")
			}
			delete(value.Normalized, "cleanup_protected")
			delete(value.Normalized, "cleanup_protection_reason")
			check, err := f.action(t, value).Preflight(t.Context(), contracts.ActionRequest{Asset: value, Action: "delete"})
			if err != nil || check.Allowed || check.Reason == "" || len(f.deleted) != 0 {
				t.Fatal("native protection was bypassed", check, err)
			}
		})
	}
}

func TestRBACActionRejectsForgedSelectorsPrerequisitesAndReceipts(t *testing.T) {
	for _, mode := range []string{"wire", "configuration", "context", "references", "wrong-kind", "wrong-connection", "parameters", "impacts", "receipt", "operation", "foreign-receipt", "prerequisite-reference", "prerequisite-live"} {
		t.Run(mode, func(t *testing.T) {
			f := newRBACFixture(t)
			value := f.asset(t, rbacAssignmentType, rbacTestAssignmentID())
			driver := f.action(t, value)
			request := contracts.ActionRequest{Asset: value, Action: "delete"}
			f.hold = true
			result, err := driver.Execute(t.Context(), request)
			if err != nil {
				t.Fatal(err)
			}
			calls := 0
			for _, count := range f.calls {
				calls += count
			}
			switch mode {
			case "wire":
				request.Asset.Normalized[rbacWireSelector] = strings.Replace(text(request.Asset.Normalized[rbacWireSelector]), rbacTestAssignmentName, rbacTestSecondAssignment, 1)
			case "configuration":
				request.Asset.Normalized[rbacConfigurationProof] = "forged"
			case "context":
				request.Asset.Normalized[rbacContextProof] = "forged"
			case "references":
				request.Asset.Normalized["_rbac_references"] = map[string]any{}
			case "wrong-kind":
				request.Asset.Identity.NativeType = rbacRoleType
			case "wrong-connection":
				request.Asset.Identity.ConnectionID = "other"
			case "parameters":
				request.Parameters = map[string]any{"tenantId": rbacOtherSubscription}
			case "impacts":
				request.LifecycleImpacts = []contracts.ActionImpact{{Asset: value, Delete: true}}
			case "receipt":
				result.Data["_rbac_delete_receipt"] = "forged"
			case "operation":
				result.ProviderOperationID = "https://management.azure.com/operations/forged"
			case "foreign-receipt":
				otherID := strings.ToLower(resourceID("Microsoft.KeyVault/vaults", "scope-vault")) + "/providers/microsoft.authorization/roleassignments/" + rbacTestSecondAssignment
				other := f.asset(t, rbacAssignmentType, otherID)
				foreign, err := f.action(t, other).Execute(t.Context(), contracts.ActionRequest{Asset: other, Action: "delete"})
				if err != nil {
					t.Fatal(err)
				}
				result = foreign
				calls = 0
				for _, count := range f.calls {
					calls += count
				}
			case "prerequisite-reference", "prerequisite-live":
				role := f.asset(t, rbacRoleType, rbacTestRoleID())
				driver = f.action(t, role)
				request = contracts.ActionRequest{Asset: role, Action: "delete", PrerequisiteDeletions: []contracts.ActionImpact{{Asset: value, ControllerID: role.ID, Delete: true}}}
				if mode == "prerequisite-reference" {
					request.PrerequisiteDeletions[0].Asset.Normalized["_rbac_references"] = map[string]any{rbacRoleType: []string{strings.Replace(rbacTestRoleID(), rbacTestRoleName, rbacTestBuiltinName, 1)}}
				}
				calls = 0
				for _, count := range f.calls {
					calls += count
				}
			}
			if strings.HasPrefix(mode, "prerequisite-") {
				_, err = driver.Preflight(t.Context(), request)
			} else {
				_, err = driver.Wait(t.Context(), request, result)
			}
			if err == nil {
				t.Fatal("forged RBAC action state accepted", mode)
			}
			after := 0
			for _, count := range f.calls {
				after += count
			}
			if mode != "prerequisite-live" && after != calls {
				t.Fatal("forged state reached native transport", mode, after-calls)
			}
		})
	}
}

func TestRBACNativeDeleteResponseValidation(t *testing.T) {
	for _, mode := range []string{"wrong-status", "wrong-id", "private-body-change", "empty-200", "body-204", "unexpected-operation", "recreated-after-delete"} {
		t.Run(mode, func(t *testing.T) {
			f := newRBACFixture(t)
			value := f.asset(t, rbacAssignmentType, rbacTestAssignmentID())
			driver := f.action(t, value)
			f.override = func(req *http.Request) (*http.Response, bool) {
				if req.Method != "DELETE" {
					return nil, false
				}
				raw := rbacTestBody(t, rbacAssignmentType, "/subscriptions/"+testSubscription+"/resourcegroups/test", rbacTestAssignmentName)
				p := object(raw["properties"])
				p["condition"], p["conditionVersion"] = "private-assignment-condition", "2.0"
				status := 200
				headers := http.Header{}
				switch mode {
				case "wrong-status":
					status = 202
				case "wrong-id":
					raw["id"] = strings.Replace(text(raw["id"]), rbacTestAssignmentName, rbacTestSecondAssignment, 1)
				case "private-body-change":
					p["condition"] = "changed"
				case "empty-200":
					return &http.Response{StatusCode: 200, Header: headers, Body: http.NoBody}, true
				case "body-204":
					status = 204
				case "unexpected-operation":
					headers.Set("Azure-AsyncOperation", "https://management.azure.com/operations/unexpected")
				case "recreated-after-delete":
					return nil, false
				}
				return jsonResponse(status, raw, headers), true
			}
			request := contracts.ActionRequest{Asset: value, Action: "delete"}
			result, err := driver.Execute(t.Context(), request)
			if mode == "recreated-after-delete" {
				if err != nil {
					t.Fatal(err)
				}
				raw := rbacTestBody(t, rbacAssignmentType, "/subscriptions/"+testSubscription+"/resourcegroups/test", rbacTestAssignmentName)
				object(raw["properties"])["principalId"] = "44444444-5555-6666-7777-888888888888"
				f.resources[value.Identity.NativeID] = raw
				_, err = driver.Wait(t.Context(), request, result)
			}
			if err == nil {
				t.Fatal("invalid native deletion accepted", mode, result)
			}
		})
	}
}
