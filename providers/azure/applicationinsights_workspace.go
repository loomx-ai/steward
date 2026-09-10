package azure

import (
	"context"
	"maps"
	"net/url"
	"slices"
	"strings"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

const insightsWorkspaceType = "Microsoft.OperationalInsights/workspaces"

// A workspace reference is not ownership. The native resource group's managedBy
// must name this component, and the component must still use that workspace.
// Detached managed groups survive a workspace switch and need separate cleanup.
// https://learn.microsoft.com/azure/azure-monitor/app/managed-workspaces
func insightsWorkspaceID(raw map[string]any) (string, error) {
	properties := object(raw["properties"])
	for key := range properties {
		if strings.EqualFold(key, "WorkspaceResourceId") && key != "WorkspaceResourceId" {
			return "", serviceDenied("ambiguous_insights_workspace_reference")
		}
	}
	value, present := properties["WorkspaceResourceId"]
	if !present {
		if strings.EqualFold(text(properties["IngestionMode"]), "LogAnalytics") {
			return "", serviceDenied("insights_workspace_reference_missing")
		}
		return "", nil // Legacy components have no workspace reference.
	}
	name, ok := value.(string)
	id, kind, err := parseID(name)
	if !ok || name != strings.TrimSpace(name) || err != nil || !strings.EqualFold(kind, insightsWorkspaceType) {
		return "", serviceDenied("invalid_insights_workspace_reference")
	}
	return id, nil
}

// Materialize an unfiltered native ARM index. The generic transport already
// guards host, subscription, collection, status, value and continuation identity.
// Here we additionally reject filtered continuations and asynchronous envelopes.
func (c *client) insightsARMIndex(ctx context.Context, path string) ([]any, error) {
	var values []any
	seen := map[string]bool{}
	for endpoint := apiURL(path, resourcesVersion); endpoint != ""; {
		u, err := url.Parse(endpoint)
		if err != nil || seen[endpoint] || strings.Contains(endpoint, "#") {
			return nil, serviceDenied("invalid_insights_arm_index_page")
		}
		query, err := url.ParseQuery(u.RawQuery)
		if err != nil || len(query["api-version"]) != 1 || query.Get("api-version") != resourcesVersion || len(query["$skiptoken"])+len(query["skiptoken"]) > 1 {
			return nil, serviceDenied("invalid_insights_arm_index_query")
		}
		for key, entries := range query {
			if key != "api-version" && key != "$skiptoken" && key != "skiptoken" || len(entries) != 1 || entries[0] == "" {
				return nil, serviceDenied("filtered_insights_arm_index")
			}
		}
		seen[endpoint] = true
		page, next, result, err := c.listPageResult(ctx, endpoint, path)
		if err != nil {
			return nil, err
		}
		if operationLocation(result.header) != "" || result.data["code"] != nil {
			return nil, serviceDenied("incomplete_insights_arm_index")
		}
		values = append(values, page...)
		endpoint = next
	}
	return values, nil
}

func (c *client) insightsGroups(ctx context.Context) (map[string]map[string]any, error) {
	values, err := c.insightsARMIndex(ctx, c.root()+"/resourcegroups")
	if err != nil {
		return nil, err
	}
	groups := map[string]map[string]any{}
	for _, value := range values {
		raw := object(value)
		id, kind, err := parseID(text(raw["id"]))
		if err != nil || !strings.EqualFold(kind, groupType) || !strings.HasPrefix(id, c.root()+"/") || groups[id] != nil || !validResponseType(kind, text(raw["type"])) {
			return nil, serviceDenied("invalid_insights_group_index")
		}
		if _, err := insightsManagedBy(raw); err != nil {
			return nil, err
		}
		groups[id] = raw
	}
	return groups, nil
}

func insightsManagedBy(raw map[string]any) (string, error) {
	for key := range raw {
		if strings.EqualFold(key, "managedBy") && key != "managedBy" {
			return "", serviceDenied("ambiguous_insights_group_owner")
		}
	}
	value, present := raw["managedBy"]
	if !present || value == nil || value == "" {
		return "", nil
	}
	owner, ok := value.(string)
	if !ok || owner != strings.TrimSpace(owner) || strings.ContainsAny(owner, "\x00\r\n") {
		return "", serviceDenied("invalid_insights_group_owner")
	}
	// Other ARM services can have owners outside our supported resource-ID
	// shapes. Only an exact match to the validated component establishes its
	// ownership; an unrelated opaque owner still protects that resource group.
	return strings.ToLower(owner), nil
}

func insightsWorkspaceResourceSnapshot(raw map[string]any) map[string]any {
	snapshot := maps.Clone(raw)
	id, kind, _ := parseID(text(raw["id"]))
	snapshot["id"], snapshot["type"] = id, strings.ToLower(kind)
	if raw["name"] != nil {
		snapshot["name"] = strings.ToLower(text(raw["name"]))
	}
	if raw["managedBy"] != nil {
		snapshot["managedBy"] = strings.ToLower(text(raw["managedBy"]))
	}
	return snapshot
}

func insightsARMReadValid(result response, id, kind string) bool {
	return result.status == 200 && result.data["code"] == nil && operationLocation(result.header) == "" && validResourceResponse(result, id, kind)
}

func (c *client) insightsGroup(ctx context.Context, id string, listed map[string]any) (map[string]any, error) {
	result, err := c.request(ctx, "GET", apiURL(id, resourcesVersion))
	if err != nil {
		return nil, err
	}
	owner, err := insightsManagedBy(result.data)
	expected, listedErr := insightsManagedBy(listed)
	if err != nil || listedErr != nil || owner != expected || !insightsARMReadValid(result, id, groupType) || !nativeConfigurationContains(insightsWorkspaceResourceSnapshot(listed), insightsWorkspaceResourceSnapshot(result.data)) {
		return nil, serviceDenied("insights_inventory_group_disagrees")
	}
	return result.data, nil
}

// This is the native resource-group membership index, including unknown kinds.
// Product GETs bind known resources to their current private configuration. The
// later lifecycle walk must also expand each member's product-specific children.
func (c *client) insightsWorkspaceMembers(ctx context.Context, group, workspace string) (map[string]any, error) {
	values, err := c.insightsARMIndex(ctx, group+"/resources")
	if err != nil {
		return nil, err
	}
	members := map[string]any{}
	for _, value := range values {
		raw := object(value)
		id, kind, err := parseID(text(raw["id"]))
		if err != nil || id == group || !inResourceGroup(id, group) || members[id] != nil || !validResponseType(kind, text(raw["type"])) {
			return nil, serviceDenied("invalid_insights_workspace_group_member")
		}
		if rule, known := findType(kind); known {
			endpoint, err := c.resourceURL(rule, id)
			if err != nil {
				return nil, err
			}
			current, err := c.request(ctx, "GET", endpoint)
			if err != nil {
				return nil, err
			}
			if !insightsARMReadValid(current, id, kind) || !nativeConfigurationContains(insightsWorkspaceResourceSnapshot(raw), insightsWorkspaceResourceSnapshot(current.data)) {
				return nil, serviceDenied("insights_workspace_member_changed")
			}
			raw = current.data
		}
		members[id] = map[string]any{"kind": kind, "configuration": c.privateConfiguration(insightsWorkspaceResourceSnapshot(raw))}
	}
	if members[workspace] == nil {
		return nil, serviceDenied("insights_managed_workspace_missing_from_group")
	}
	return members, nil
}

func (c *client) insightsWorkspaceInventory(ctx context.Context, parent *contracts.InventoryItem, raw map[string]any, groups map[string]map[string]any) error {
	workspace, err := insightsWorkspaceID(raw)
	if err != nil {
		return err
	}
	state := map[string]any{"workspace": workspace, "managed_group": "", "detached_groups": map[string]any{}, "members": map[string]any{}, "incoming": map[string]any{}}
	workspaceGroup := ""
	if strings.HasPrefix(workspace, c.root()+"/") {
		workspaceGroup = strings.Join(strings.Split(workspace, "/")[:5], "/")
		if groups[workspaceGroup] == nil {
			return serviceDenied("insights_workspace_group_missing_from_index")
		}
	}
	for _, id := range slices.Sorted(maps.Keys(groups)) {
		owner, _ := insightsManagedBy(groups[id])
		if owner != parent.NativeID && id != workspaceGroup {
			continue
		}
		group, err := c.insightsGroup(ctx, id, groups[id])
		if err != nil {
			return err
		}
		configuration := c.privateConfiguration(insightsWorkspaceResourceSnapshot(group))
		if id == workspaceGroup {
			state["workspace_group_configuration"] = configuration
		}
		if owner != parent.NativeID {
			continue // Shared workspaces are references, even with a managed name.
		}
		if inResourceGroup(parent.NativeID, id) {
			return serviceDenied("insights_managed_group_contains_controller")
		}
		if id != workspaceGroup {
			object(state["detached_groups"])[id] = configuration
			continue // Switching workspaces does not delete the original group.
		}
		members, err := c.insightsWorkspaceMembers(ctx, id, workspace)
		if err != nil {
			return err
		}
		after, err := c.insightsGroup(ctx, id, group)
		if err != nil {
			return err
		}
		if c.privateConfiguration(insightsWorkspaceResourceSnapshot(after)) != configuration {
			return serviceDenied("insights_managed_workspace_group_changed")
		}
		state["managed_group"], state["members"] = id, members
		incoming, err := c.monitorPrivateLinkIncoming(ctx, asset.Identity{NativeID: workspace, NativeType: insightsWorkspaceType})
		if err != nil {
			return err
		}
		for _, child := range incoming {
			configuration := ""
			if child.data != nil {
				configuration = c.privateConfiguration(monitorPrivateLinkSnapshot(child.kind, child.data))
			}
			object(state["incoming"])[child.id] = configuration
		}
	}
	current, err := c.insightsComponent(ctx, parent.NativeID)
	if err != nil {
		return err
	}
	if c.privateConfiguration(monitorPrivateLinkTargetSnapshot(current)) != text(parent.Normalized["_monitor_private_link_target_configuration"]) {
		return serviceDenied("insights_workspace_component_changed")
	}
	parent.Normalized["_insights_workspace"] = state
	parent.Normalized["_insights_workspace_configuration"] = c.privateConfiguration(state)
	return nil
}
