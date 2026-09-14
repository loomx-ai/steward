package gcp

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"

	"github.com/loomx-ai/steward/internal/provider/catalog"
)

const routePolicyType = "compute.googleapis.com/RoutePolicy"
const routePolicyGet = "compute.routers.getRoutePolicy"
const routePolicyList = "compute.routers.listRoutePolicies"
const routerType = "compute.googleapis.com/Router"

var routePolicySegment = regexp.MustCompile(`^[a-z]([-a-z0-9]{0,61}[a-z0-9])?$`)

// Route policies have a router-local name and a query-addressed GET. This
// composite inventory identity is not an independently callable REST path.
func (c *client) routePolicyOperation(id, method string) (catalog.Operation, map[string]any, error) {
	name := strings.TrimPrefix(id, "//compute.googleapis.com/")
	parts := strings.Split(name, "/")
	if method != "GET" || name == id || len(parts) != 8 || parts[0] != "projects" || (parts[1] != c.project && parts[1] != c.number) || parts[2] != "regions" || parts[4] != "routers" || parts[6] != "routePolicies" || !routePolicySegment.MatchString(parts[3]) || !routePolicySegment.MatchString(parts[5]) || !routePolicySegment.MatchString(parts[7]) {
		return catalog.Operation{}, nil, groupDenied("route_policy_identity_invalid")
	}
	metadata, err := providerData()
	if err != nil {
		return catalog.Operation{}, nil, err
	}
	op, ok := metadata.catalog.Operation(routePolicyGet)
	if !ok {
		return catalog.Operation{}, nil, groupDenied("route_policy_method_missing")
	}
	return op, map[string]any{"project": c.project, "region": parts[3], "router": parts[5], "policy": parts[7]}, nil
}

func (c *client) routePolicyRead(ctx context.Context, id string) (map[string]any, error) {
	op, parameters, err := c.routePolicyOperation(id, "GET")
	if err != nil {
		return nil, err
	}
	bound, err := catalog.BindREST(op, parameters)
	if err != nil {
		return nil, err
	}
	result, err := c.requestResult(ctx, bound.Method, bound.URL, nil, nil)
	if err != nil {
		return nil, err
	}
	data := object(result.Data["resource"])
	if err := routePolicyData(data, text(parameters["policy"])); err != nil {
		return nil, err
	}
	return data, nil
}

func routePolicyData(data map[string]any, name string) error {
	if data == nil || data["name"] != name || !routePolicySegment.MatchString(name) {
		return groupDenied("route_policy_identity_changed")
	}
	for _, key := range []string{"type", "description", "fingerprint"} {
		if value, exists := data[key]; exists {
			if _, ok := value.(string); !ok {
				return groupDenied("route_policy_field_invalid")
			}
		}
	}
	value, exists := data["terms"]
	if !exists {
		return nil
	}
	terms, ok := value.([]any)
	if !ok {
		return groupDenied("route_policy_terms_invalid")
	}
	priorities := map[int32]bool{}
	for _, value := range terms {
		term, ok := value.(map[string]any)
		if !ok {
			return groupDenied("route_policy_term_invalid")
		}
		var priority int32
		if raw, exists := term["priority"]; exists {
			encoded, err := json.Marshal(raw)
			if err != nil || raw == nil || json.Unmarshal(encoded, &priority) != nil || priority < 0 {
				return groupDenied("route_policy_priority_invalid")
			}
		}
		if priorities[priority] {
			return groupDenied("route_policy_priority_duplicate")
		}
		priorities[priority] = true
		expressions := []any{}
		if match, exists := term["match"]; exists {
			expressions = append(expressions, match)
		}
		if value, exists := term["actions"]; exists {
			actions, ok := value.([]any)
			if !ok {
				return groupDenied("route_policy_actions_invalid")
			}
			expressions = append(expressions, actions...)
		}
		for _, value := range expressions {
			expression, ok := value.(map[string]any)
			if !ok {
				return groupDenied("route_policy_expression_invalid")
			}
			for _, key := range []string{"expression", "title", "description", "location"} {
				if value, exists := expression[key]; exists {
					if _, ok := value.(string); !ok {
						return groupDenied("route_policy_expression_invalid")
					}
				}
			}
		}
	}
	return nil
}
