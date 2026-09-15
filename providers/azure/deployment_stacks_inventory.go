package azure

import (
	"context"
	"maps"
	"net/url"
	"sort"
	"strings"
)

// This subscription-bound reader does not authorize management-group access.
// A stack's deploymentScope does not authorize access to its storage scope.
func (c *client) deploymentStackCollection(scope string) (string, error) {
	kind, params, err := deploymentStackParameters(scope + "/providers/Microsoft.Resources/deploymentStacks/probe")
	if err != nil || kind == "ManagementGroup" || !strings.EqualFold(text(params["subscriptionId"]), c.subscription) {
		return "", serviceDenied("invalid_deployment_stack_inventory_scope")
	}
	return scope + "/providers/Microsoft.Resources/deploymentStacks", nil
}

func (c *client) deploymentStackRead(ctx context.Context, id string) (response, error) {
	kind, params, err := deploymentStackParameters(id)
	if err != nil || kind == "ManagementGroup" || !strings.EqualFold(text(params["subscriptionId"]), c.subscription) {
		return response{}, serviceDenied("invalid_deployment_stack_read_scope")
	}
	res, err := c.request(ctx, "GET", apiURL(id, deploymentStackVersion))
	if err != nil {
		return res, err
	}
	return res, validateDeploymentStackRead(res, id)
}

// Return native own-read objects for later member/consequence review. These
// objects contain private inputs and must not be persisted or logged directly.
// No provisioning state, including an unknown state, authorizes cleanup here.
func (c *client) deploymentStackInventory(ctx context.Context, scope string, known []string) ([]map[string]any, []string, error) {
	path, err := c.deploymentStackCollection(scope)
	if err != nil {
		return nil, nil, err
	}
	ids := map[string]string{}
	listed := map[string]bool{}
	add := func(id string, fromList bool) error {
		_, _, err := deploymentStackParameters(id)
		if err != nil || !strings.EqualFold(id[:strings.LastIndex(id, "/")], path) {
			return serviceDenied("invalid_deployment_stack_inventory_identity")
		}
		canonical := strings.ToLower(id)
		if fromList && listed[canonical] {
			return serviceDenied("duplicate_deployment_stack_inventory_identity")
		}
		ids[canonical] = id
		if fromList {
			listed[canonical] = true
		}
		return nil
	}
	// Validate hints before issuing requests, even if the index would be empty.
	for _, id := range known {
		if err := add(id, false); err != nil {
			return nil, nil, err
		}
	}
	next := apiURL(path, deploymentStackVersion)
	seen := map[string]bool{}
	for next != "" {
		u, err := url.Parse(next)
		if err != nil || seen[next] {
			return nil, nil, serviceDenied("invalid_deployment_stack_page")
		}
		seen[next] = true
		query, err := url.ParseQuery(u.RawQuery)
		if err != nil || query.Get("api-version") != deploymentStackVersion {
			return nil, nil, serviceDenied("invalid_deployment_stack_page_version")
		}
		for key, values := range query {
			if len(values) != 1 || key != "api-version" && key != "$skiptoken" && key != "skipToken" {
				return nil, nil, serviceDenied("filtered_deployment_stack_page")
			}
		}
		page, cursor, res, err := c.listPageResult(ctx, next, path)
		if err != nil {
			return nil, nil, err
		}
		if operationLocation(res.header) != "" {
			return nil, nil, serviceDenied("incomplete_deployment_stack_page")
		}
		for _, row := range page {
			raw := object(row)
			if !strings.EqualFold(text(raw["type"]), deploymentStackType) {
				return nil, nil, serviceDenied("invalid_deployment_stack_inventory_type")
			}
			if err := add(text(raw["id"]), true); err != nil {
				return nil, nil, err
			}
		}
		next = cursor
	}
	ordered := make([]string, 0, len(ids))
	for id := range ids {
		ordered = append(ordered, id)
	}
	sort.Strings(ordered)
	rows := []map[string]any{}
	absent := []string{}
	for _, id := range ordered {
		res, err := c.deploymentStackRead(ctx, ids[id])
		if err != nil {
			if isNotFound(err) && !listed[id] {
				absent = append(absent, id)
				continue
			}
			return nil, nil, err
		}
		review, err := c.deploymentStackMemberReview(res.data)
		if err != nil {
			return nil, nil, err
		}
		observed := maps.Clone(res.data)
		observed["_deployment_stack_review"] = review
		rows = append(rows, observed)
	}
	return rows, absent, nil
}
