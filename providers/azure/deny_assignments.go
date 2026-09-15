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

const denyAssignmentType = "Microsoft.Authorization/denyAssignments"

func denyAssignmentID(wire string) (id, scope string, err error) {
	id, kind, err := deploymentStackMemberID(wire)
	if err != nil || !strings.EqualFold(kind, denyAssignmentType) {
		return "", "", serviceDenied("invalid_deny_assignment_id")
	}
	separator := "/providers/microsoft.authorization/denyassignments/"
	position := strings.LastIndex(id, separator)
	if position < 0 || strings.Contains(id[position+len(separator):], "/") {
		return "", "", serviceDenied("invalid_deny_assignment_id")
	}
	return id, id[:position], nil
}
func (c *client) denyLocalScope(wire string) (string, error) {
	scope, _, err := deploymentStackMemberID(wire)
	if err != nil || !(strings.EqualFold(scope, c.root()) || strings.HasPrefix(scope, strings.ToLower(c.root())+"/")) {
		return "", serviceDenied("deny_assignment_scope_outside_connection")
	}
	return scope, nil
}
func (c *client) denyAssignmentRequest(scope, name string) (catalog.RESTRequest, error) {
	_, err := c.denyLocalScope(scope)
	if err != nil {
		return catalog.RESTRequest{}, err
	}
	parameters := map[string]any{"scope": strings.TrimPrefix(scope, "/")}
	operation := "DenyAssignments_ListForScope"
	if name != "" {
		if _, _, err := denyAssignmentID(scope + "/providers/" + denyAssignmentType + "/" + name); err != nil {
			return catalog.RESTRequest{}, err
		}
		operation = "DenyAssignments_Get"
		parameters["denyAssignmentId"] = name
	}
	metadata, err := providerData()
	if err != nil {
		return catalog.RESTRequest{}, err
	}
	selected, found := metadata.catalog.Operation("Azure.Microsoft.Authorization." + operation)
	if !found {
		return catalog.RESTRequest{}, serviceDenied("missing_deny_assignment_operation")
	}
	return catalog.BindREST(selected, parameters)
}
func (c *client) validateDenyAssignment(raw map[string]any) (string, string, error) {
	wire, ok := raw["id"].(string)
	if !ok {
		return "", "", serviceDenied("invalid_deny_assignment_id")
	}
	id, scope, err := denyAssignmentID(wire)
	if err != nil {
		return "", "", err
	}
	if _, err = c.denyLocalScope(scope); err != nil {
		return "", "", err
	}
	properties := object(raw["properties"])
	actualScope, scopeOK := properties["scope"].(string)
	actualType, typeOK := raw["type"].(string)
	actualName, nameOK := raw["name"].(string)
	wireScope := wire[:strings.LastIndex(strings.ToLower(wire), "/providers/microsoft.authorization/denyassignments/")]
	if properties == nil || !scopeOK || actualScope != strings.TrimSpace(actualScope) || !typeOK || !nameOK || !strings.EqualFold(actualType, denyAssignmentType) || !strings.EqualFold(actualName, last(id)) || !strings.EqualFold(actualScope, scope) || denyScopeSignature(actualScope) != denyScopeSignature(wireScope) {
		return "", "", serviceDenied("deny_assignment_response_identity_changed")
	}
	return id, scope, nil
}
func (c *client) denyAssignmentRead(ctx context.Context, id string) (response, error) {
	canonical, _, err := denyAssignmentID(id)
	if err != nil {
		return response{}, err
	}
	scope := id[:strings.LastIndex(strings.ToLower(id), "/providers/microsoft.authorization/denyassignments/")]
	request, err := c.denyAssignmentRequest(scope, last(canonical))
	if err != nil {
		return response{}, err
	}
	current, err := c.requestBody(ctx, request.Method, request.URL, request.Body, request.Headers)
	if err != nil {
		return current, err
	}
	actual, _, err := c.validateDenyAssignment(current.data)
	if err != nil {
		return response{}, err
	}
	if current.status != 200 || current.data["error"] != nil || actual != canonical || denyWireSignature(text(current.data["id"])) != denyWireSignature(id) || operationLocation(current.header) != "" {
		return response{}, serviceDenied("invalid_deny_assignment_read")
	}
	return current, nil
}
func denyRelatedScope(a, b string) bool {
	return a == b || strings.HasPrefix(a, b+"/") || strings.HasPrefix(b, a+"/")
}

// Unfiltered native scope enumeration. Never substitute atScope() (ancestors
// only) or a principal/GDPR filter for this index. Returned records are private
// observations, not proof of Stack ownership or permission to remove a deny.
func (c *client) denyAssignmentIndex(ctx context.Context, scope string, known []string) (map[string]map[string]any, []string, error) {
	_, err := c.denyLocalScope(scope)
	if err != nil {
		return nil, nil, err
	}
	ids := map[string]string{}
	for _, wire := range known {
		id, parent, err := denyAssignmentID(wire)
		if err != nil {
			return nil, nil, err
		}
		if _, err = c.denyLocalScope(parent); err != nil {
			return nil, nil, err
		}
		wireParent := wire[:strings.LastIndex(strings.ToLower(wire), "/providers/microsoft.authorization/denyassignments/")]
		if !denyRelatedScope(denyScopeSignature(wireParent), denyScopeSignature(scope)) || ids[id] != "" && denyWireSignature(ids[id]) != denyWireSignature(wire) {
			return nil, nil, serviceDenied("deny_assignment_known_scope_changed")
		}
		ids[id] = wire
	}
	request, err := c.denyAssignmentRequest(scope, "")
	if err != nil {
		return nil, nil, err
	}
	u, _ := url.Parse(request.URL)
	next, seen, listed := request.URL, map[string]bool{}, map[string]bool{}
	for next != "" {
		if seen[next] || len(seen) >= 10000 {
			return nil, nil, serviceDenied("deny_assignment_repeated_or_excessive_pages")
		}
		seen[next] = true
		if err := rbacListQuery(next, request.URL); err != nil {
			return nil, nil, err
		}
		pageURL, _ := url.Parse(next)
		pageScope := pageURL.Path[:len(pageURL.Path)-len("/providers/Microsoft.Authorization/denyAssignments")]
		if denyScopeSignature(pageScope) != denyScopeSignature(scope) {
			return nil, nil, serviceDenied("deny_assignment_page_scope_changed")
		}
		rows, following, res, err := c.listPageResult(ctx, next, u.Path)
		if err != nil {
			return nil, nil, err
		}
		if operationLocation(res.header) != "" {
			return nil, nil, serviceDenied("incomplete_deny_assignment_list")
		}
		for _, row := range rows {
			raw := object(row)
			id, _, err := c.validateDenyAssignment(raw)
			if err != nil {
				return nil, nil, err
			}
			wire := raw["id"].(string)
			wireParent := wire[:strings.LastIndex(strings.ToLower(wire), "/providers/microsoft.authorization/denyassignments/")]
			if listed[id] || !denyRelatedScope(denyScopeSignature(wireParent), denyScopeSignature(scope)) || ids[id] != "" && denyWireSignature(ids[id]) != denyWireSignature(wire) {
				return nil, nil, serviceDenied("deny_assignment_list_scope_changed")
			}
			listed[id], ids[id] = true, wire
		}
		next = following
	}
	current := map[string]map[string]any{}
	absent := []string{}
	for _, id := range slices.Sorted(maps.Keys(ids)) {
		own, err := c.denyAssignmentRead(ctx, ids[id])
		if isNotFound(err) && !listed[id] {
			absent = append(absent, id)
			continue
		}
		if err != nil {
			return nil, nil, err
		}
		current[id] = own.data
	}
	return current, absent, nil
}

// Reconcile known IDs with own GETs and compare two complete observations.
// Inherited rows outside this connection fail explicitly rather than disappearing
// from an allegedly complete snapshot. No native mutation is performed here.
func (c *client) denyAssignmentSnapshot(ctx context.Context, scope string, known []string) (rows map[string]map[string]any, absent []string, err error) {
	defer func() { err = contracts.DependencyReadError(err) }()
	first, gone, err := c.denyAssignmentIndex(ctx, scope, known)
	if err != nil {
		return nil, nil, err
	}
	second, missing, err := c.denyAssignmentIndex(ctx, scope, known)
	if err != nil {
		return nil, nil, err
	}
	fingerprint := func(rows map[string]map[string]any, absent []string) string {
		return c.privateConfiguration(map[string]any{"rows": rows, "absent": absent})
	}
	if fingerprint(first, gone) != fingerprint(second, missing) {
		return nil, nil, serviceDenied("deny_assignment_snapshot_changed")
	}
	return second, missing, nil
}

// Match existing RBAC semantics for Cosmos extension scopes while keeping
// ordinary ARM paths case insensitive. This is identity, never authorization.
func denyScopeSignature(scope string) string {
	if _, kind, err := parseID(scope); err == nil && isCosmosType(kind) {
		return cosmosWireSignature(responseID(kind, scope))
	}
	return strings.ToLower(scope)
}
func denyWireSignature(id string) string {
	marker := "/providers/microsoft.authorization/denyassignments/"
	at := strings.LastIndex(strings.ToLower(id), marker)
	if at < 0 {
		return ""
	}
	return denyScopeSignature(id[:at]) + strings.ToLower(id[at:])
}

func denyAssignmentPath(path string) bool {
	path = strings.ToLower(path)
	marker := "/providers/microsoft.authorization/denyassignments"
	return strings.HasSuffix(path, marker) || strings.Contains(path, marker+"/")
}

func denyAssignmentSafeValue(value any) any {
	switch value := value.(type) {
	case map[string]any:
		result := map[string]any{}
		for key, entry := range value {
			switch strings.ToLower(key) {
			case "id", "name", "type", "request_id":
				if text, ok := entry.(string); ok {
					result[key] = text
				}
			case "status_code":
				switch number := entry.(type) {
				case int:
					result[key] = number
				case float64:
					result[key] = number
				}
			case "body", "value":
				result[key] = denyAssignmentSafeValue(entry)
			}
		}
		return result
	case []any:
		result := make([]any, len(value))
		for i, entry := range value {
			result[i] = denyAssignmentSafeValue(entry)
		}
		return result
	default:
		return nil
	}
}
