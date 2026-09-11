package azure

import (
	"encoding/json"
	"maps"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

const rbacTestBuiltinName = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
const rbacTestSecondAssignment = "bbbbbbbb-cccc-dddd-eeee-ffffffffffff"

type rbacFixture struct {
	runtime           *Runtime
	resources, scopes map[string]map[string]any
	locks             []any
	calls             map[string]int
	deleted           []string
	hold              bool
	deleteStatus      int
	override          func(*http.Request) (*http.Response, bool)
}

func newRBACFixture(t *testing.T) *rbacFixture {
	t.Helper()
	root := "/subscriptions/" + testSubscription
	group := root + "/resourcegroups/test"
	f := &rbacFixture{resources: map[string]map[string]any{}, scopes: map[string]map[string]any{}, locks: []any{}, calls: map[string]int{}, deleteStatus: 200}
	role := rbacTestBody(t, rbacRoleType, root, rbacTestRoleName)
	object(role["properties"])["assignableScopes"] = []any{group}
	object(role["properties"])["futurePrivateCondition"] = map[string]any{"expression": "private-role-condition"}
	builtin := rbacTestBody(t, rbacRoleType, root, rbacTestBuiltinName)
	object(builtin["properties"])["type"], object(builtin["properties"])["assignableScopes"] = "BuiltInRole", []any{"/"}
	assignment := rbacTestBody(t, rbacAssignmentType, group, rbacTestAssignmentName)
	object(assignment["properties"])["condition"] = "private-assignment-condition"
	object(assignment["properties"])["conditionVersion"] = "2.0"
	source := nativeResource("Microsoft.KeyVault/vaults", "scope-vault", "westus", map[string]any{"tenantId": testTenant, "sku": map[string]any{"family": "A", "name": "standard"}, "accessPolicies": []any{}})
	source["systemData"] = map[string]any{"createdAt": "2026-01-01T00:00:00Z"}
	sourceID := strings.ToLower(text(source["id"]))
	other := rbacTestBody(t, rbacAssignmentType, sourceID, rbacTestSecondAssignment)
	for _, raw := range []map[string]any{role, builtin, assignment, other} {
		f.resources[strings.ToLower(text(raw["id"]))] = raw
	}
	f.scopes[group] = map[string]any{"id": group, "type": groupType, "name": "test", "location": "westus", "properties": map[string]any{"provisioningState": "Succeeded"}}
	f.scopes[sourceID] = source
	f.runtime = protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
		path := strings.ToLower(req.URL.Path)
		f.calls[req.Method+" "+path]++
		if f.override != nil {
			if response, ok := f.override(req); ok {
				return response, nil
			}
		}
		if req.URL.Host != "management.azure.com" || !strings.HasPrefix(path, root+"/") {
			t.Fatal("RBAC fixture crossed subscription", req.URL)
		}
		if req.Method == "GET" && path == root+"/providers/microsoft.authorization/locks" {
			return jsonResponse(200, map[string]any{"value": f.locks}, nil), nil
		}
		if req.Method == "GET" && (path == root+"/resourcegroups" || path == root+"/resources") {
			values := []any{}
			for _, id := range slices.Sorted(maps.Keys(f.scopes)) {
				raw := f.scopes[id]
				if path == root+"/resources" || raw["type"] == groupType {
					values = append(values, raw)
				}
			}
			return jsonResponse(200, map[string]any{"value": values}, nil), nil
		}
		for _, kind := range []string{rbacRoleType, rbacAssignmentType, rbacEligibilityType, rbacScheduleType} {
			if req.Method == "GET" && path == root+"/providers/"+strings.ToLower(kind) {
				if req.URL.Query().Get("api-version") != rbacVersion(kind) {
					t.Fatal("RBAC native collection version changed")
				}
				if kind == rbacRoleType && req.URL.Query().Get("$filter") != "atScopeAndBelow()" {
					t.Fatal("narrower role definitions omitted")
				}
				values := []any{}
				for _, id := range slices.Sorted(maps.Keys(f.resources)) {
					raw := f.resources[id]
					if raw["type"] == kind {
						values = append(values, raw)
					}
				}
				return jsonResponse(200, map[string]any{"value": values}, http.Header{"X-Ms-Request-Id": {"rbac-native-list"}}), nil
			}
		}
		if _, _, kind, err := rbacResourceID(path); err == nil {
			if req.URL.Query().Get("api-version") != rbacVersion(kind) || req.Method != "GET" && req.Method != "DELETE" {
				t.Fatal("unexpected RBAC native operation", req.Method, req.URL)
			}
			raw := f.resources[path]
			if raw == nil {
				return jsonResponse(404, map[string]any{"error": map[string]any{"code": "RoleAssignmentNotFound"}}, nil), nil
			}
			if req.Method == "DELETE" {
				if req.Header.Get("If-Match") != "" {
					t.Fatal("invented RBAC conditional deletion")
				}
				f.deleted = append(f.deleted, path)
				if !f.hold {
					delete(f.resources, path)
				}
				if f.deleteStatus == 204 {
					return &http.Response{StatusCode: 204, Header: http.Header{"X-Ms-Request-Id": {"rbac-native-delete"}}, Body: http.NoBody}, nil
				}
				return jsonResponse(f.deleteStatus, raw, http.Header{"X-Ms-Request-Id": {"rbac-native-delete"}}), nil
			}
			return jsonResponse(200, raw, nil), nil
		}
		if req.Method == "GET" {
			if raw := f.scopes[path]; raw != nil {
				return jsonResponse(200, raw, nil), nil
			}
		}
		if req.Method == "GET" && (path == group || path == sourceID) {
			return jsonResponse(404, map[string]any{"error": map[string]any{"code": "ResourceNotFound"}}, nil), nil
		}
		if response, ok := emptyMonitorIndexResponse(t, req); ok {
			return response, nil
		}
		t.Fatal("unexpected RBAC fixture call", req.Method, req.URL)
		return nil, nil
	})
	return f
}
func (f *rbacFixture) asset(t *testing.T, kind, id string) asset.Asset {
	t.Helper()
	batch, err := f.runtime.List(t.Context(), productRequest(f.runtime, kind))
	if err != nil {
		t.Fatal("RBAC native inventory failed", err)
	}
	for _, item := range batch.Items {
		if item.NativeID == id {
			return asset.Asset{ID: asset.AssetID(id), Identity: asset.Identity{Provider: asset.ProviderAzure, ConnectionID: "connection", Partition: "azure", NativeType: kind, NativeID: id}, ResourceKindID: item.ResourceKind.ID, Location: item.Location, Normalized: item.Normalized, Capabilities: item.ResourceKind.Capabilities, Name: item.Name, Tags: item.Tags}
		}
	}
	t.Fatal("RBAC asset missing", kind, id)
	return asset.Asset{}
}
func (f *rbacFixture) action(t *testing.T, value asset.Asset) contracts.ActionDriver {
	t.Helper()
	driver, err := f.runtime.ResolveAction(t.Context(), "connection", value)
	if err != nil {
		t.Fatal("RBAC action did not resolve", err)
	}
	return driver
}
func rbacTestRoleID() string {
	return "/subscriptions/" + testSubscription + "/providers/microsoft.authorization/roledefinitions/" + rbacTestRoleName
}
func rbacTestAssignmentID() string {
	return "/subscriptions/" + testSubscription + "/resourcegroups/test/providers/microsoft.authorization/roleassignments/" + rbacTestAssignmentName
}

func TestRBACRegisteredInventoryScopesProtectionAndPrivacy(t *testing.T) {
	for _, mode := range []string{"ordinary", "missing-scope", "missing-group", "scope-protected", "group-protected", "scope-lock", "shared-role", "builtin-role", "pim-eligibility", "pim-active", "unknown-scope", "managed-scope"} {
		t.Run(mode, func(t *testing.T) {
			f := newRBACFixture(t)
			root := "/subscriptions/" + testSubscription
			group := root + "/resourcegroups/test"
			source := strings.ToLower(resourceID("Microsoft.KeyVault/vaults", "scope-vault"))
			kind, id := rbacRoleType, rbacTestRoleID()
			wantReason := ""
			switch mode {
			case "missing-scope":
				kind, id = rbacAssignmentType, source+"/providers/microsoft.authorization/roleassignments/"+rbacTestSecondAssignment
				delete(f.scopes, source)
			case "missing-group":
				delete(f.scopes, group)
			case "scope-protected":
				kind, id = rbacAssignmentType, source+"/providers/microsoft.authorization/roleassignments/"+rbacTestSecondAssignment
				f.scopes[source]["tags"] = map[string]any{"steward:protected": "true"}
				wantReason = "azure_protected_tag"
			case "group-protected":
				f.scopes[group]["tags"] = map[string]any{"steward:protected": "true"}
				wantReason = "azure_protected_tag"
			case "scope-lock":
				f.locks = []any{map[string]any{"id": group + "/providers/Microsoft.Authorization/locks/keep", "properties": map[string]any{"level": "CanNotDelete"}}}
				wantReason = "azure_management_lock"
			case "shared-role":
				object(f.resources[id]["properties"])["assignableScopes"] = []any{group, "/subscriptions/" + rbacOtherSubscription}
				wantReason = "azure_rbac_shared_assignable_scope"
			case "builtin-role":
				id = root + "/providers/microsoft.authorization/roledefinitions/" + rbacTestBuiltinName
				wantReason = "azure_rbac_builtin_role"
			case "pim-eligibility", "pim-active":
				pimKind := rbacEligibilityType
				if mode == "pim-active" {
					pimKind = rbacScheduleType
				}
				pim := rbacTestBody(t, pimKind, group, "eeeeeeee-dddd-cccc-bbbb-aaaaaaaaaaaa")
				f.resources[strings.ToLower(text(pim["id"]))] = pim
				wantReason = "azure_rbac_pim_assignment"
			case "unknown-scope":
				object(f.resources[id]["properties"])["assignableScopes"] = []any{group + "/providers/Microsoft.Example/unregistered/private"}
				wantReason = "azure_rbac_unverified_scope"
			case "managed-scope":
				f.scopes[group]["managedBy"] = resourceID(aksType, "controller")
			}
			value := f.asset(t, kind, id)
			if value.Location != "global" || text(value.Normalized["cleanup_protection_reason"]) != wantReason || text(value.Normalized[rbacWireSelector]) == "" {
				t.Fatal("RBAC projection/protection mismatch", value.Normalized)
			}
			raw, _ := json.Marshal(value.Normalized)
			if strings.Contains(string(raw), "private-role-condition") || strings.Contains(string(raw), "private-assignment-condition") {
				t.Fatal("private RBAC condition leaked")
			}
			request := productRequest(f.runtime, kind)
			request.Limit = 1
			first, err := f.runtime.List(t.Context(), request)
			if err != nil || first.Complete || len(first.Items) != 1 || first.RequestID != "rbac-native-list" {
				t.Fatal("RBAC page failed", first.Complete, err)
			}
			request.Cursor = first.NextCursor
			second, err := f.runtime.List(t.Context(), request)
			if err != nil || !second.Complete || len(second.Items) != 1 || second.Items[0].NativeID == first.Items[0].NativeID {
				t.Fatal("RBAC continuation failed", err)
			}
			request.Cursor = ""
			request.Scope = asset.Scope{Kind: asset.ScopeRegion, NativeID: "westus"}
			empty, err := f.runtime.List(t.Context(), request)
			if err != nil || !empty.Complete || len(empty.Items) != 0 {
				t.Fatal("regional scan gained global RBAC assets", err)
			}
			request.Scope = asset.Scope{Kind: asset.ScopeGlobal, NativeID: testSubscription + "/global"}
			request.Limit = 100
			all, err := f.runtime.List(t.Context(), request)
			if err != nil || len(all.Items) != 2 {
				t.Fatal("global RBAC scan failed", err)
			}
		})
	}
}

func TestRBACInventoryRejectsChangedPagesAndIncompleteReads(t *testing.T) {
	for _, mode := range []string{"private-change", "new-assignment", "pim-change", "scope-recreated", "group-change", "permission-denied", "missing-detail", "incomplete-pim", "region-change", "kind-change", "cursor-tamper"} {
		t.Run(mode, func(t *testing.T) {
			f := newRBACFixture(t)
			request := productRequest(f.runtime, rbacAssignmentType)
			request.Limit = 1
			first, err := f.runtime.List(t.Context(), request)
			if err != nil || first.Complete {
				t.Fatal("initial page", err)
			}
			request.Cursor = first.NextCursor
			group := "/subscriptions/" + testSubscription + "/resourcegroups/test"
			switch mode {
			case "private-change":
				object(f.resources[rbacTestAssignmentID()]["properties"])["condition"] = "changed"
			case "new-assignment":
				raw := rbacTestBody(t, rbacAssignmentType, group, "44444444-5555-6666-7777-888888888888")
				f.resources[strings.ToLower(text(raw["id"]))] = raw
			case "pim-change":
				raw := rbacTestBody(t, rbacEligibilityType, group, "44444444-5555-6666-7777-888888888888")
				f.resources[strings.ToLower(text(raw["id"]))] = raw
			case "scope-recreated":
				object(f.scopes[strings.ToLower(resourceID("Microsoft.KeyVault/vaults", "scope-vault"))]["systemData"])["createdAt"] = "2026-02-01T00:00:00Z"
			case "group-change":
				f.scopes[group]["tags"] = map[string]any{"steward:protected": "true"}
			case "permission-denied", "missing-detail", "incomplete-pim":
				f.override = func(req *http.Request) (*http.Response, bool) {
					path := strings.ToLower(req.URL.Path)
					match := path == rbacTestAssignmentID()
					if mode == "incomplete-pim" {
						match = strings.HasSuffix(path, "/roleeligibilityschedules")
					}
					if match {
						status := 403
						if mode == "missing-detail" || mode == "incomplete-pim" {
							status = 404
						}
						return jsonResponse(status, map[string]any{"error": map[string]any{"code": "DeniedOrMissing"}}, nil), true
					}
					return nil, false
				}
			case "region-change":
				request.Scope = asset.Scope{Kind: asset.ScopeRegion, NativeID: "westus"}
			case "kind-change":
				kind := f.runtime.resourceKind(rbacRoleType)
				request.ResourceKind = &kind
			case "cursor-tamper":
				request.Cursor += "bad"
			}
			batch, err := f.runtime.List(t.Context(), request)
			if err == nil || len(batch.Items) != 0 || batch.Complete {
				t.Fatal("unsafe RBAC continuation accepted", batch, err)
			}
		})
	}
}
