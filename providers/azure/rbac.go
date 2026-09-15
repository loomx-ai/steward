package azure

import (
	"context"
	"maps"
	"net/url"
	"slices"
	"strings"

	"github.com/loomx-ai/steward/internal/provider/catalog"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

const (
	rbacRoleType        = "Microsoft.Authorization/roleDefinitions"
	rbacAssignmentType  = "Microsoft.Authorization/roleAssignments"
	rbacEligibilityType = "Microsoft.Authorization/roleEligibilitySchedules"
	rbacScheduleType    = "Microsoft.Authorization/roleAssignmentSchedules"
)

func rbacKind(kind string) string {
	for _, candidate := range []string{rbacRoleType, rbacAssignmentType, rbacEligibilityType, rbacScheduleType} {
		if strings.EqualFold(kind, candidate) {
			return candidate
		}
	}
	return ""
}

// Tenant and management-group scopes are parsed for inherited native responses.
// Only subscription-local scopes may be used to issue a request.
func rbacScope(wire string) (string, error) {
	if wire != strings.TrimSpace(wire) || strings.ContainsAny(wire, "%?#\\\x00\r\n\t") {
		return "", serviceDenied("invalid_rbac_scope")
	}
	id := strings.ToLower(wire)
	if id == "/" {
		return id, nil
	}
	parts := strings.Split(id, "/")
	if len(parts) == 3 && parts[0] == "" && parts[1] == "subscriptions" && uuidPattern.MatchString(parts[2]) {
		return id, nil
	}
	if len(parts) == 5 && parts[0] == "" && parts[1] == "providers" && parts[2] == "microsoft.management" && parts[3] == "managementgroups" && monitorReceiverName(parts[4]) {
		return id, nil
	}
	id, _, err := parseID(wire)
	if err != nil {
		return "", serviceDenied("invalid_rbac_resource_scope")
	}
	return id, nil
}

func rbacResourceID(wire string) (id, scope, kind string, err error) {
	if wire != strings.TrimSpace(wire) || strings.ContainsAny(wire, "%?#\\\x00\r\n\t") {
		return "", "", "", serviceDenied("invalid_rbac_identity")
	}
	id = strings.ToLower(wire)
	const separator = "/providers/microsoft.authorization/"
	index := strings.LastIndex(id, separator)
	if index < 0 {
		return "", "", "", serviceDenied("invalid_rbac_resource_type")
	}
	parts := strings.Split(id[index+len(separator):], "/")
	if len(parts) != 2 || !uuidPattern.MatchString(parts[1]) {
		return "", "", "", serviceDenied("invalid_rbac_resource_name")
	}
	kind = rbacKind("Microsoft.Authorization/" + parts[0])
	if kind == "" {
		return "", "", "", serviceDenied("invalid_rbac_resource_type")
	}
	scope = id[:index]
	if scope == "" {
		scope = "/"
	}
	scope, err = rbacScope(scope)
	if err != nil {
		return "", "", "", err
	}
	return id, scope, kind, nil
}

func (c *client) rbacLocalScope(scope string) bool {
	return scope == c.root() || strings.HasPrefix(scope, c.root()+"/resourcegroups/")
}

// A role's GUID is invariant across native tenant/subscription read aliases.
// Use the connection's subscription endpoint; never follow a response into a
// different subscription or a tenant-wide mutation endpoint.
func (c *client) rbacRoleID(wire string) (string, error) {
	id, _, kind, err := rbacResourceID(wire)
	if err != nil || kind != rbacRoleType {
		return "", serviceDenied("invalid_rbac_role_reference")
	}
	return c.root() + "/providers/microsoft.authorization/roledefinitions/" + last(id), nil
}

// RBAC extensions on a native Cosmos resource inherit its case-sensitive
// source selector even though the inventory identity is a canonical ARM ID.
func rbacWireScope(scope string) (string, error) {
	if _, err := rbacScope(scope); err != nil {
		return "", err
	}
	if scope == "/" || strings.HasPrefix(strings.ToLower(scope), "/providers/microsoft.management/managementgroups/") {
		return strings.ToLower(scope), nil
	}
	return diagnosticSourceWire(scope)
}

func (c *client) rbacWireID(wire string) (string, error) {
	id, _, kind, err := rbacResourceID(wire)
	if err != nil {
		return "", err
	}
	if kind == rbacRoleType {
		return c.rbacRoleID(wire)
	}
	index := strings.LastIndex(strings.ToLower(wire), "/providers/microsoft.authorization/")
	scope := wire[:index]
	if scope == "" {
		scope = "/"
	}
	scope, err = rbacWireScope(scope)
	if err != nil {
		return "", err
	}
	return strings.TrimSuffix(scope, "/") + id[index:], nil
}

func rbacVersion(kind string) string {
	if kind == rbacRoleType || kind == rbacAssignmentType {
		return "2022-04-01"
	}
	if kind == rbacEligibilityType || kind == rbacScheduleType {
		return "2020-10-01"
	}
	return ""
}

func (c *client) rbacOperation(kind, scope, name, method string) (catalog.Operation, map[string]any, error) {
	canonical, err := rbacScope(scope)
	wire, wireErr := rbacWireScope(scope)
	if err != nil || wireErr != nil || wire != scope || !c.rbacLocalScope(canonical) || rbacKind(kind) != kind || name != "" && !uuidPattern.MatchString(name) || method != "GET" && method != "DELETE" || method == "DELETE" && (name == "" || kind != rbacRoleType && kind != rbacAssignmentType) || kind == rbacRoleType && scope != c.root() {
		return catalog.Operation{}, nil, serviceDenied("invalid_rbac_operation_scope")
	}
	prefix, selector := "RoleDefinitions", "roleDefinitionId"
	switch kind {
	case rbacAssignmentType:
		prefix, selector = "RoleAssignments", "roleAssignmentName"
	case rbacEligibilityType:
		prefix, selector = "RoleEligibilitySchedules", "roleEligibilityScheduleName"
	case rbacScheduleType:
		prefix, selector = "RoleAssignmentSchedules", "roleAssignmentScheduleName"
	}
	operation := "Get"
	params := map[string]any{"scope": strings.TrimPrefix(scope, "/")}
	if name == "" {
		operation = "ListForScope"
		if kind == rbacRoleType {
			operation, params["$filter"] = "List", "atScopeAndBelow()"
		}
		if kind == rbacAssignmentType && scope == c.root() {
			operation, params = "ListForSubscription", map[string]any{"subscriptionId": c.subscription}
		}
	} else {
		params[selector] = name
		if method == "DELETE" {
			operation = "Delete"
		}
	}
	metadata, err := providerData()
	if err != nil {
		return catalog.Operation{}, nil, err
	}
	op, ok := metadata.catalog.Operation("Azure.Microsoft.Authorization." + prefix + "_" + operation)
	if !ok || op.Call == nil || op.Call.Method != method || op.Call.Version != rbacVersion(kind) {
		return catalog.Operation{}, nil, serviceDenied("rbac_native_operation_changed")
	}
	return op, params, nil
}

func (c *client) rbacRequest(kind, scope, name, method string) (catalog.RESTRequest, error) {
	op, params, err := c.rbacOperation(kind, scope, name, method)
	if err != nil {
		return catalog.RESTRequest{}, err
	}
	return bindAzureREST(op, params)
}

func rbacStrings(value any, nonempty bool) ([]string, error) {
	values, ok := value.([]any)
	if !ok || nonempty && len(values) == 0 {
		return nil, serviceDenied("invalid_rbac_string_collection")
	}
	result := make([]string, 0, len(values))
	for _, value := range values {
		s, ok := value.(string)
		if !ok || s == "" || s != strings.TrimSpace(s) {
			return nil, serviceDenied("invalid_rbac_string_value")
		}
		result = append(result, s)
	}
	return result, nil
}

func (c *client) rbacValidate(kind string, raw map[string]any) (string, error) {
	if monitorRuleFields(raw, "id", "name", "type", "properties") != nil {
		return "", serviceDenied("ambiguous_rbac_resource_field")
	}
	id, scope, actual, err := rbacResourceID(text(raw["id"]))
	if err != nil || actual != kind || raw["id"] != strings.TrimSpace(text(raw["id"])) || !strings.EqualFold(text(raw["type"]), kind) || !strings.EqualFold(text(raw["name"]), last(id)) {
		return "", serviceDenied("rbac_response_identity_changed")
	}
	props, ok := raw["properties"].(map[string]any)
	if !ok || monitorRuleFields(props, "type", "roleName", "permissions", "assignableScopes", "scope", "roleDefinitionId", "principalId", "principalType", "condition", "conditionVersion", "delegatedManagedIdentityResourceId", "createdOn", "updatedOn", "status") != nil {
		return "", serviceDenied("invalid_rbac_properties")
	}
	if kind == rbacRoleType {
		if text(props["roleName"]) == "" || text(props["type"]) != "BuiltInRole" && text(props["type"]) != "CustomRole" {
			return "", serviceDenied("invalid_rbac_role_type")
		}
		scopes, err := rbacStrings(props["assignableScopes"], true)
		if err != nil {
			return "", err
		}
		seen := map[string]bool{}
		for _, candidate := range scopes {
			canonical, err := rbacScope(candidate)
			if err != nil || seen[canonical] || canonical == "/" && text(props["type"]) != "BuiltInRole" {
				return "", serviceDenied("invalid_rbac_assignable_scope")
			}
			seen[canonical] = true
		}
		permissions, ok := props["permissions"].([]any)
		if !ok || len(permissions) == 0 {
			return "", serviceDenied("rbac_permissions_missing")
		}
		for _, value := range permissions {
			permission, ok := value.(map[string]any)
			if !ok || monitorRuleFields(permission, "actions", "notActions", "dataActions", "notDataActions", "condition") != nil {
				return "", serviceDenied("invalid_rbac_permission")
			}
			for _, field := range []string{"actions", "notActions", "dataActions", "notDataActions"} {
				if value, present := permission[field]; present {
					if _, err := rbacStrings(value, false); err != nil {
						return "", err
					}
				}
			}
		}
		return c.rbacRoleID(id)
	}

	suppliedScope, err := rbacScope(text(props["scope"]))
	if err != nil || suppliedScope != scope || props["scope"] != text(props["scope"]) {
		return "", serviceDenied("rbac_assignment_scope_changed")
	}
	wireScope, wireErr := rbacWireScope(text(props["scope"]))
	wireID, identityErr := c.rbacWireID(text(raw["id"]))
	if wireErr != nil || identityErr != nil || strings.TrimSuffix(wireScope, "/")+strings.TrimPrefix(id, strings.TrimSuffix(scope, "/")) != wireID {
		return "", serviceDenied("rbac_assignment_native_scope_changed")
	}
	if props["roleDefinitionId"] != text(props["roleDefinitionId"]) {
		return "", serviceDenied("invalid_rbac_role_reference")
	}
	if _, err := c.rbacRoleID(text(props["roleDefinitionId"])); err != nil {
		return "", err
	}
	if !uuidPattern.MatchString(text(props["principalId"])) || props["principalId"] != text(props["principalId"]) {
		return "", serviceDenied("invalid_rbac_principal_identity")
	}
	if !slices.Contains([]string{"User", "Group", "ServicePrincipal", "ForeignGroup", "Device", "AgentUser", "AgentServicePrincipal"}, text(props["principalType"])) {
		return "", serviceDenied("invalid_rbac_principal_type")
	}
	for _, field := range []string{"condition", "conditionVersion", "description", "delegatedManagedIdentityResourceId"} {
		if value := props[field]; value != nil {
			if _, ok := value.(string); !ok {
				return "", serviceDenied("invalid_rbac_assignment_field")
			}
		}
	}
	if target := text(props["delegatedManagedIdentityResourceId"]); target != "" {
		_, typ, err := parseID(target)
		if err != nil || typ != "microsoft.managedidentity/userassignedidentities" {
			return "", serviceDenied("invalid_rbac_delegated_identity")
		}
	}
	return id, nil
}

func (c *client) rbacSnapshot(kind string, raw map[string]any) map[string]any {
	result := maps.Clone(raw)
	id, _, _, _ := rbacResourceID(text(raw["id"]))
	if kind == rbacRoleType {
		id, _ = c.rbacRoleID(id)
	}
	result["id"], result["type"], result["name"] = id, kind, last(id)
	result["_source_wire_id"], _ = c.rbacWireID(text(raw["id"]))
	return result
}

func (c *client) rbacRead(ctx context.Context, kind, nativeID string) (response, error) {
	id, _, actual, err := rbacResourceID(nativeID)
	wire, wireErr := c.rbacWireID(nativeID)
	if err != nil || wireErr != nil || wire != nativeID || actual != kind {
		return response{}, serviceDenied("invalid_rbac_read_identity")
	}
	scope := wire[:strings.LastIndex(wire, "/providers/")]
	request, err := c.rbacRequest(kind, scope, last(id), "GET")
	if err != nil {
		return response{}, err
	}
	result, err := c.requestBody(ctx, request.Method, request.URL, request.Body, request.Headers)
	if err != nil {
		return result, err
	}
	current, err := c.rbacValidate(kind, result.data)
	currentWire, wireErr := c.rbacWireID(text(result.data["id"]))
	if err != nil || wireErr != nil || currentWire != wire || current != id || result.status != 200 || operationLocation(result.header) != "" {
		return response{}, serviceDenied("rbac_resource_read_changed")
	}
	return result, nil
}

// Keep the selection query on every page: a filtered continuation cannot turn
// an incomplete role index into authoritative absence. No cross-tenant option
// is accepted. Paging follows only this native collection and API version.
func rbacListQuery(endpoint, initial string) error {
	u, err := url.Parse(endpoint)
	root, initialErr := url.Parse(initial)
	if err != nil || initialErr != nil || !strings.EqualFold(u.Path, root.Path) {
		return serviceDenied("rbac_list_collection_changed")
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil || q.Get("api-version") != root.Query().Get("api-version") || q.Get("$filter") != root.Query().Get("$filter") {
		return serviceDenied("rbac_list_selection_changed")
	}
	for key, values := range q {
		if len(values) != 1 || values[0] == "" || key != "api-version" && key != "$filter" && key != "$skipToken" && key != "$skiptoken" {
			return serviceDenied("invalid_rbac_list_query")
		}
	}
	if q.Has("$skipToken") && q.Has("$skiptoken") {
		return serviceDenied("ambiguous_rbac_list_cursor")
	}
	return nil
}

func (c *client) rbacIndex(ctx context.Context, kind, scope string) (map[string]map[string]any, string, error) {
	request, err := c.rbacRequest(kind, scope, "", "GET")
	if err != nil {
		return nil, "", err
	}
	u, _ := url.Parse(request.URL)
	next, seen, rows := request.URL, map[string]bool{}, map[string]map[string]any{}
	provenance := ""
	for next != "" {
		if seen[next] || len(seen) >= 10000 {
			return nil, "", serviceDenied("rbac_list_repeated_or_excessive_pages")
		}
		seen[next] = true
		if err := rbacListQuery(next, request.URL); err != nil {
			return nil, "", err
		}
		values, following, result, err := c.listPageResult(ctx, next, u.Path)
		if err != nil {
			return nil, "", contracts.DependencyReadError(err)
		}
		if following != "" {
			if err := rbacListQuery(following, request.URL); err != nil {
				return nil, "", err
			}
		}
		if result.requestID != "" {
			provenance = result.requestID
		}
		for _, value := range values {
			raw, ok := value.(map[string]any)
			if !ok {
				return nil, "", serviceDenied("invalid_rbac_list_row")
			}
			id, err := c.rbacValidate(kind, raw)
			if err != nil || rows[id] != nil {
				return nil, "", serviceDenied("invalid_or_duplicate_rbac_list_identity")
			}
			_, resourceScope, _, _ := rbacResourceID(id)
			if !c.rbacLocalScope(resourceScope) {
				// Inherited tenant/management-group assignments may appear in
				// a local index. They are not owned by this subscription and
				// never authorize a read or mutation outside the connection.
				if resourceScope == "/" || strings.HasPrefix(resourceScope, "/providers/microsoft.management/managementgroups/") {
					continue
				}
				return nil, "", serviceDenied("rbac_index_contains_foreign_subscription")
			}
			wire, err := c.rbacWireID(text(raw["id"]))
			if err != nil {
				return nil, "", err
			}
			current, err := c.rbacRead(ctx, kind, wire)
			if err != nil {
				return nil, "", contracts.DependencyReadError(err)
			}
			if c.privateConfiguration(c.rbacSnapshot(kind, raw)) != c.privateConfiguration(c.rbacSnapshot(kind, current.data)) {
				return nil, "", serviceDenied("rbac_list_detail_disagreement")
			}
			rows[id] = current.data
		}
		next = following
	}
	return rows, provenance, nil
}

func rbacPath(path string) bool {
	parts := strings.Split(strings.ToLower(path), "/")
	for i := 0; i+2 < len(parts); i++ {
		if parts[i] == "providers" && parts[i+1] == "microsoft.authorization" && (rbacKind("Microsoft.Authorization/"+parts[i+2]) != "" || parts[i+2] == "denyassignments") {
			return true
		}
	}
	return false
}

// A broad ARM group listing may contain only resource identity metadata. It
// cannot make RBAC extensions controller-owned; their native index verifies the
// full configuration independently in the same dependency walk.
func rbacListedIdentity(raw map[string]any) error {
	id, _, kind, err := rbacResourceID(text(raw["id"]))
	if err != nil || rbacResourceKind(kind) == "" || !strings.EqualFold(kind, text(raw["type"])) || !strings.EqualFold(last(id), text(raw["name"])) {
		return serviceDenied("invalid_rbac_arm_index_identity")
	}
	return nil
}
