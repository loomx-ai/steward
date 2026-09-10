package azure

import (
	"context"
	"maps"
	"net/url"
	"slices"
	"strings"
)

// workspaceLinks is the service's reverse view of gateway configConnections.
// Its GET-only rows describe existing workspace assets, not deletable children.
func apimWorkspaceLink(root string, raw map[string]any) (string, []string, error) {
	kind := apimServiceType + "/workspaceLinks"
	id, actual, err := parseID(text(raw["id"]))
	if err != nil || raw["error"] != nil || !strings.EqualFold(actual, kind) || !strings.EqualFold(text(raw["type"]), kind) || redisParentID(id) != root || validateAPIM(kind, raw) != nil {
		return "", nil, serviceDenied("invalid_apim_workspace_link")
	}
	props := object(raw["properties"])
	workspace, sourceKind, err := parseID(text(props["workspaceId"]))
	if err != nil || !strings.EqualFold(sourceKind, apimWorkspaceType) || workspace != root+"/workspaces/"+last(id) {
		return "", nil, serviceDenied("invalid_apim_workspace_link_source")
	}
	var gateways []string
	if value, exists := props["gateways"]; exists {
		values, ok := value.([]any)
		if !ok {
			return "", nil, serviceDenied("invalid_apim_workspace_link_gateways")
		}
		for _, value := range values {
			gateway, gatewayKind, err := parseID(text(object(value)["id"]))
			if err != nil || !strings.EqualFold(gatewayKind, apimGatewayType) || strings.Split(gateway, "/")[2] != strings.Split(root, "/")[2] || slices.Contains(gateways, gateway) {
				return "", nil, serviceDenied("invalid_apim_workspace_link_gateway")
			}
			gateways = append(gateways, gateway)
		}
	}
	slices.Sort(gateways)
	return workspace, gateways, nil
}

func (c *client) apimWorkspaceLinks(ctx context.Context, root string, connections []serviceChild) (string, error) {
	kind, _ := findType(apimServiceType)
	_, params, err := c.resourceOperation(kind, root, "GET")
	if err != nil {
		return "", err
	}
	metadata, err := providerData()
	if err != nil {
		return "", err
	}
	list, ok := metadata.catalog.Operation("Azure.Microsoft.ApiManagement.ApiManagementWorkspaceLinks_ListByService")
	if !ok {
		return "", serviceDenied("missing_apim_workspace_links_api")
	}
	bound, err := bindAzureREST(list, params)
	if err != nil {
		return "", err
	}
	u, _ := url.Parse(bound.URL)
	rows, err := c.listAllURL(ctx, bound.URL, u.Path)
	if err != nil {
		return "", err
	}
	read, ok := metadata.catalog.Operation("Azure.Microsoft.ApiManagement.ApiManagementWorkspaceLink_Get")
	if !ok {
		return "", serviceDenied("missing_apim_workspace_link_api")
	}
	seen := map[string]bool{}
	pairs := map[string]bool{}
	snapshots := map[string]any{}
	for _, value := range rows {
		row := object(value)
		workspace, gateways, err := apimWorkspaceLink(root, row)
		if err != nil {
			return "", err
		}
		if seen[workspace] {
			return "", serviceDenied("duplicate_apim_workspace_link")
		}
		seen[workspace] = true
		source, err := c.apimResource(ctx, workspace)
		if err != nil {
			return "", err
		}
		if err := apimReady(apimWorkspaceType, source); err != nil {
			return "", err
		}
		parameters := maps.Clone(params)
		parameters["workspaceId"] = last(workspace)
		bound, err := bindAzureREST(read, parameters)
		if err != nil {
			return "", err
		}
		live, err := c.request(ctx, "GET", bound.URL)
		if err != nil {
			return "", err
		}
		current, currentGateways, err := apimWorkspaceLink(root, live.data)
		if err != nil || live.status != 200 || current != workspace || !slices.Equal(gateways, currentGateways) || apimListedIncarnation(apimServiceType+"/workspaceLinks", row, live.data) != nil || text(row["etag"]) != "" && row["etag"] != live.data["etag"] {
			return "", serviceDenied("apim_workspace_link_changed")
		}
		snapshots[workspace] = map[string]any{"link": live.data, "workspace": apimSnapshot(apimWorkspaceType, source), "workspace_etag": apimETag(apimWorkspaceType, source)}
		for _, gateway := range gateways {
			pairs[workspace+"|"+gateway] = true
		}
	}
	expected := map[string]bool{}
	for _, connection := range connections {
		if !strings.EqualFold(connection.kind, apimGatewayConnectionType) {
			continue
		}
		workspace, err := apimGatewaySourceID(connection.id, connection.data)
		if err != nil {
			return "", err
		}
		if apimRootID(workspace) == root {
			expected[workspace+"|"+apimRootID(connection.id)] = true
		}
	}
	if !maps.Equal(pairs, expected) {
		return "", serviceDenied("apim_workspace_gateway_indexes_disagree")
	}
	return c.privateConfiguration(snapshots), nil
}

// The service-level issue view reports a different ID for the same API issue.
// Its explicit apiId selects the canonical API-owned resource and DELETE route.
func apimProjectedIssue(root string, raw map[string]any) (map[string]any, error) {
	kind := apimServiceType + "/issues"
	id, actual, err := parseID(text(raw["id"]))
	if err != nil || raw["error"] != nil || !strings.EqualFold(actual, kind) || !strings.EqualFold(text(raw["type"]), kind) || redisParentID(id) != root || validateAPIM(kind, raw) != nil {
		return nil, serviceDenied("invalid_apim_issue_projection")
	}
	api, apiKind, err := parseID(text(object(raw["properties"])["apiId"]))
	if err != nil || !strings.EqualFold(apiKind, apimAPIType) || redisParentID(api) != root {
		return nil, serviceDenied("invalid_apim_issue_projection_owner")
	}
	result := maps.Clone(raw)
	result["id"], result["type"] = api+"/issues/"+last(id), apimIssueType
	return result, nil
}

func (c *client) apimIssues(ctx context.Context, root string) (map[string]serviceChild, error) {
	kind, _ := findType(apimServiceType)
	_, params, err := c.resourceOperation(kind, root, "GET")
	if err != nil {
		return nil, err
	}
	metadata, err := providerData()
	if err != nil {
		return nil, err
	}
	list, listOK := metadata.catalog.Operation("Azure.Microsoft.ApiManagement.Issue_ListByService")
	read, readOK := metadata.catalog.Operation("Azure.Microsoft.ApiManagement.Issue_Get")
	if !listOK || !readOK {
		return nil, serviceDenied("missing_apim_issue_projection_api")
	}
	bound, err := bindAzureREST(list, params)
	if err != nil {
		return nil, err
	}
	u, _ := url.Parse(bound.URL)
	collect := func() (map[string]serviceChild, error) {
		rows, err := c.listAllURL(ctx, bound.URL, u.Path)
		if err != nil {
			return nil, err
		}
		result := map[string]serviceChild{}
		seen := map[string]bool{}
		for _, value := range rows {
			row, err := apimProjectedIssue(root, object(value))
			if err != nil {
				return nil, err
			}
			id := text(row["id"])
			if seen[last(id)] {
				return nil, serviceDenied("duplicate_apim_issue_projection")
			}
			seen[last(id)] = true // issueId is unique across the entire service.
			parameters := maps.Clone(params)
			parameters["issueId"] = last(id)
			bound, err := bindAzureREST(read, parameters)
			if err != nil {
				return nil, err
			}
			response, err := c.request(ctx, "GET", bound.URL)
			if err != nil {
				return nil, err
			}
			current, err := apimProjectedIssue(root, response.data)
			if err != nil || response.status != 200 || current["id"] != id || apimListedIncarnation(apimIssueType, row, current) != nil {
				return nil, serviceDenied("apim_issue_projection_changed")
			}
			live, err := c.apimResource(ctx, id)
			if err != nil {
				return nil, err
			}
			if apimListedIncarnation(apimIssueType, current, live) != nil || apimETag(apimIssueType, current) != "" && apimETag(apimIssueType, current) != apimETag(apimIssueType, live) {
				return nil, serviceDenied("apim_issue_projection_target_changed")
			}
			result[id] = serviceChild{id: id, kind: apimIssueType, data: live}
		}
		return result, nil
	}
	first, err := collect()
	if err != nil {
		return nil, err
	}
	second, err := collect()
	if err != nil {
		return nil, err
	}
	if !maps.EqualFunc(first, second, func(a, b serviceChild) bool {
		return a.id == b.id && apimETag(apimIssueType, a.data) == apimETag(apimIssueType, b.data) && c.privateConfiguration(apimSnapshot(apimIssueType, a.data)) == c.privateConfiguration(apimSnapshot(apimIssueType, b.data))
	}) {
		return nil, serviceDenied("apim_issue_index_changed")
	}
	return second, nil
}

func (c *client) apimIssuePage(ctx context.Context, api string) ([]any, string, response, error) {
	_, kind, err := parseID(api)
	if err != nil || !strings.EqualFold(kind, apimAPIType) {
		return nil, "", response{}, serviceDenied("invalid_apim_issue_parent")
	}
	collection := api + "/issues"
	rows, provenance, err := c.listAllURLResult(ctx, apiURL(collection, apimVersion), collection)
	if err != nil {
		return nil, "", response{}, err
	}
	index, err := c.apimIssues(ctx, apimRootID(api))
	if err != nil {
		return nil, "", response{}, err
	}
	seen := map[string]bool{}
	values := make([]any, 0, len(rows))
	for _, value := range rows {
		row := object(value)
		id, actual, err := parseID(text(row["id"]))
		live, exists := index[id]
		if err != nil || !strings.EqualFold(actual, apimIssueType) || !validResponseType(apimIssueType, text(row["type"])) || redisParentID(id) != api || seen[id] || !exists || validateAPIM(apimIssueType, row) != nil || apimListedIncarnation(apimIssueType, row, live.data) != nil {
			return nil, "", response{}, serviceDenied("apim_issue_indexes_disagree")
		}
		seen[id] = true
		values = append(values, live.data)
	}
	for id := range index {
		if redisParentID(id) == api && !seen[id] {
			return nil, "", response{}, serviceDenied("apim_issue_indexes_disagree")
		}
	}
	return values, "", provenance, nil
}
