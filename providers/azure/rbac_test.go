package azure

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

const rbacTestRoleName = "11111111-2222-3333-4444-555555555555"
const rbacTestAssignmentName = "22222222-3333-4444-5555-666666666666"
const rbacOtherSubscription = "99999999-8888-7777-6666-555555555555"
const rbacTestPrincipal = "33333333-4444-5555-6666-777777777777"

func rbacTestBody(t *testing.T, kind, scope, name string) map[string]any {
	t.Helper()
	file := "GetRoleDefinitionByName.json"
	switch kind {
	case rbacAssignmentType:
		file = "RoleAssignments_Get.json"
	case rbacEligibilityType:
		file = "GetRoleEligibilityScheduleByName.json"
	case rbacScheduleType:
		file = "GetRoleAssignmentScheduleByName.json"
	}
	result := object(object(object(rbacExample(t, file)["responses"])["200"])["body"])
	result["id"] = strings.TrimSuffix(scope, "/") + "/providers/" + kind + "/" + name
	result["type"], result["name"] = kind, name
	props := object(result["properties"])
	if kind == rbacRoleType {
		props["type"], props["roleName"] = "CustomRole", "Audited custom role"
		props["assignableScopes"] = []any{scope}
	} else {
		props["scope"], props["principalId"], props["principalType"] = scope, rbacTestPrincipal, "ServicePrincipal"
		props["roleDefinitionId"] = "/subscriptions/" + testSubscription + "/providers/Microsoft.Authorization/roleDefinitions/" + rbacTestRoleName
	}
	return result
}

func TestRBACScopeAndNativeOperationBoundaries(t *testing.T) {
	c := directClient(nil)
	group := c.root() + "/resourcegroups/test"
	for _, kind := range []string{rbacRoleType, rbacAssignmentType, rbacEligibilityType, rbacScheduleType} {
		t.Run(last(kind), func(t *testing.T) {
			scope := c.root()
			if kind != rbacRoleType {
				scope = group
			}
			request, err := c.rbacRequest(kind, scope, rbacTestRoleName, "GET")
			if err != nil {
				t.Fatal(err)
			}
			u, _ := url.Parse(request.URL)
			if !strings.EqualFold(u.Path, scope+"/providers/"+kind+"/"+rbacTestRoleName) || u.Query().Get("api-version") != rbacVersion(kind) {
				t.Fatal("native read binding changed", request)
			}
			for _, invalid := range []string{"/", "/providers/microsoft.management/managementgroups/parent", strings.Replace(scope, testSubscription, rbacOtherSubscription, 1), scope + "/../other", scope + "?tenantId=other"} {
				if _, err := c.rbacRequest(kind, invalid, rbacTestRoleName, "GET"); err == nil {
					t.Fatal("invalid operation scope accepted", invalid)
				}
			}
			for _, method := range []string{"PUT", "POST", "PATCH"} {
				if _, err := c.rbacRequest(kind, scope, rbacTestRoleName, method); err == nil {
					t.Fatal("unselected operation accepted", method)
				}
			}
			if _, err := c.rbacRequest(kind, scope, "", "DELETE"); err == nil {
				t.Fatal("collection deletion accepted")
			}
			if _, err := c.rbacRequest(kind, scope, "not-a-guid", "GET"); err == nil {
				t.Fatal("invalid role selector accepted")
			}
			_, err = c.rbacRequest(kind, scope, rbacTestRoleName, "DELETE")
			if (kind == rbacRoleType || kind == rbacAssignmentType) != (err == nil) {
				t.Fatal("wrong native deletion capability", err)
			}
		})
	}
	for _, scope := range []string{"/", c.root(), group, group + "/providers/microsoft.storage/storageaccounts/storage", "/providers/microsoft.management/managementgroups/parent"} {
		nativeID := strings.TrimSuffix(scope, "/") + "/providers/Microsoft.Authorization/roleDefinitions/" + rbacTestRoleName
		id, err := c.rbacRoleID(nativeID)
		if err != nil || id != c.root()+"/providers/microsoft.authorization/roledefinitions/"+rbacTestRoleName {
			t.Fatal("role alias changed identity", scope, err)
		}
	}
	for _, id := range []string{c.root() + "/providers/Microsoft.Authorization/roleDefinitions/not-a-guid", c.root() + "/providers/Microsoft.Authorization/roleDefinitions/" + rbacTestRoleName + "/other/name", " " + c.root() + "/providers/Microsoft.Authorization/roleDefinitions/" + rbacTestRoleName, c.root() + "/providers/Microsoft.Authorization/roleDefinitions/%2e%2e", c.root() + "/providers/Microsoft.Authorization/unknown/" + rbacTestRoleName} {
		if _, _, _, err := rbacResourceID(id); err == nil {
			t.Fatal("invalid RBAC identity accepted", id)
		}
	}
}

func TestRBACNativeMetadataAndPrivateConfiguration(t *testing.T) {
	c := directClient(nil)
	role := rbacTestBody(t, rbacRoleType, c.root(), rbacTestRoleName)
	for _, kind := range []string{rbacRoleType, rbacAssignmentType, rbacEligibilityType, rbacScheduleType} {
		raw := rbacTestBody(t, kind, c.root(), rbacTestAssignmentName)
		if _, err := c.rbacValidate(kind, raw); err != nil {
			t.Fatal("valid native metadata rejected", kind, err)
		}
	}
	before := c.privateConfiguration(c.rbacSnapshot(rbacRoleType, role))
	object(role["properties"])["futurePermission"] = map[string]any{"privateValue": "unpublished permission condition"}
	if before == c.privateConfiguration(c.rbacSnapshot(rbacRoleType, role)) {
		t.Fatal("unknown private permission did not change binding")
	}
	for _, mode := range []string{"wrong-id", "wrong-type", "wrong-name", "wrong-case", "bad-role-type", "missing-scopes", "foreign-format-scope", "duplicate-scopes", "custom-root-scope", "missing-permissions", "malformed-permissions"} {
		t.Run(mode, func(t *testing.T) {
			raw := rbacTestBody(t, rbacRoleType, c.root(), rbacTestRoleName)
			p := object(raw["properties"])
			switch mode {
			case "wrong-id":
				raw["id"] = text(raw["id"]) + " "
			case "wrong-type":
				raw["type"] = rbacAssignmentType
			case "wrong-name":
				raw["name"] = rbacTestAssignmentName
			case "wrong-case":
				p["AssignableScopes"] = p["assignableScopes"]
			case "bad-role-type":
				p["type"] = "SomethingElse"
			case "missing-scopes":
				delete(p, "assignableScopes")
			case "foreign-format-scope":
				p["assignableScopes"] = []any{"https://foreign.example/"}
			case "duplicate-scopes":
				p["assignableScopes"] = []any{c.root(), strings.ToUpper(c.root())}
			case "custom-root-scope":
				p["assignableScopes"] = []any{"/"}
			case "missing-permissions":
				delete(p, "permissions")
			case "malformed-permissions":
				p["permissions"] = []any{map[string]any{"actions": "*"}}
			}
			if _, err := c.rbacValidate(rbacRoleType, raw); err == nil {
				t.Fatal("invalid role metadata accepted")
			}
		})
	}
	for _, field := range []string{"scope", "roleDefinitionId", "principalId", "principalType", "condition", "conditionVersion", "delegatedManagedIdentityResourceId"} {
		t.Run(field, func(t *testing.T) {
			raw := rbacTestBody(t, rbacAssignmentType, c.root(), rbacTestAssignmentName)
			p := object(raw["properties"])
			p[field] = []any{"invalid"}
			if _, err := c.rbacValidate(rbacAssignmentType, raw); err == nil {
				t.Fatal("malformed assignment accepted")
			}
		})
	}
}

func TestRBACIndexUsesNativeReadsAndBindsPagination(t *testing.T) {
	for _, kind := range []string{rbacRoleType, rbacAssignmentType, rbacEligibilityType, rbacScheduleType} {
		for _, mode := range []string{"paged", "alias", "inherited", "duplicate", "read-missing", "denied", "private-drift", "foreign-subscription", "foreign-page", "wrong-version", "filtered-page", "tenant-page", "repeat-page", "partial", "malformed"} {
			t.Run(last(kind)+"/"+mode, func(t *testing.T) {
				root := "/subscriptions/" + testSubscription
				first := rbacTestBody(t, kind, root, rbacTestRoleName)
				second := rbacTestBody(t, kind, root, rbacTestAssignmentName)
				id1, id2 := strings.ToLower(text(first["id"])), strings.ToLower(text(second["id"]))
				gets, lists := 0, 0
				if mode == "alias" && kind == rbacRoleType {
					first["id"] = "/providers/Microsoft.Authorization/roleDefinitions/" + rbacTestRoleName
				}
				c := directClient(func(r *http.Request) (*http.Response, error) {
					if r.Method != "GET" {
						t.Fatal("inventory mutation", r.Method)
					}
					path := strings.ToLower(r.URL.Path)
					if path == id1 || path == id2 {
						gets++
						if mode == "read-missing" {
							return jsonResponse(404, map[string]any{"error": map[string]any{"code": "ResourceNotFound"}}, nil), nil
						}
						body := first
						if path == id2 {
							body = second
						}
						payload, _ := json.Marshal(body)
						var result map[string]any
						_ = json.Unmarshal(payload, &result)
						if mode == "private-drift" {
							object(result["properties"])["privateFutureSetting"] = "changed"
						}
						return jsonResponse(200, result, nil), nil
					}
					if path != strings.ToLower(root+"/providers/"+kind) {
						t.Fatal("unrequested RBAC endpoint", r.URL)
					}
					lists++
					if mode == "denied" {
						return jsonResponse(403, map[string]any{"error": map[string]any{"code": "AuthorizationFailed"}}, nil), nil
					}
					next := *r.URL
					q := next.Query()
					q.Set("$skipToken", "opaque-next-page")
					next.RawQuery = q.Encode()
					values := []any{first}
					response := map[string]any{"value": values, "nextLink": next.String()}
					if lists == 2 {
						response = map[string]any{"value": []any{second}}
					}
					switch mode {
					case "inherited":
						if kind != rbacRoleType {
							inherited := rbacTestBody(t, kind, "/", "44444444-5555-6666-7777-888888888888")
							response["value"] = append(response["value"].([]any), inherited)
						}
					case "duplicate":
						if lists == 2 {
							response["value"] = []any{first}
						}
					case "foreign-subscription":
						changed := rbacTestBody(t, kind, strings.Replace(root, testSubscription, rbacOtherSubscription, 1), rbacTestRoleName)
						if kind == rbacRoleType {
							changed["type"] = rbacAssignmentType
						}
						response["value"] = []any{changed}
					case "foreign-page":
						next.Host = "other.example"
						response["nextLink"] = next.String()
					case "wrong-version":
						q.Set("api-version", "1900-01-01")
						next.RawQuery = q.Encode()
						response["nextLink"] = next.String()
					case "filtered-page":
						q.Set("$filter", "principalId eq 'other'")
						next.RawQuery = q.Encode()
						response["nextLink"] = next.String()
					case "tenant-page":
						q.Set("tenantId", rbacOtherSubscription)
						next.RawQuery = q.Encode()
						response["nextLink"] = next.String()
					case "repeat-page":
						response["nextLink"] = r.URL.String()
					case "malformed":
						response["value"] = []any{"invalid"}
					}
					status := 200
					if mode == "partial" {
						status = 206
					}
					return jsonResponse(status, response, http.Header{"X-Ms-Request-Id": {"rbac-index-request"}}), nil
				})
				rows, requestID, err := c.rbacIndex(t.Context(), kind, root)
				good := mode == "paged" || mode == "alias" || mode == "inherited"
				if good {
					if err != nil || len(rows) != 2 || gets != 2 || lists != 2 || requestID != "rbac-index-request" {
						t.Fatal("native index failed", len(rows), gets, lists, requestID, err)
					}
				} else if err == nil || rows != nil {
					t.Fatal("unsafe index accepted", rows, err)
				}
				if mode == "read-missing" && isNotFound(err) {
					t.Fatal("missing detail proves collection absence")
				}
			})
		}
	}
}
