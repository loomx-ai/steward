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

// Keep inventory generation separate from configuration which can legitimately
// change when the reviewed AMPLS prerequisites are removed.
func insightsWorkspaceLifecycleSnapshot(raw map[string]any) map[string]any {
	_, kind, _ := parseID(text(raw["id"]))
	if monitorPrivateLinkTarget(kind) || strings.EqualFold(kind, groupType) {
		raw = monitorPrivateLinkTargetSnapshot(raw)
	}
	return insightsWorkspaceResourceSnapshot(raw)
}

func (c *client) insightsWorkspaceMemberBindings(resources []map[string]any) map[string]any {
	members := map[string]any{}
	for _, raw := range resources {
		id, kind, _ := parseID(text(raw["id"]))
		members[id] = map[string]any{"kind": kind, "configuration": c.privateConfiguration(insightsWorkspaceResourceSnapshot(raw)), "lifecycle_configuration": c.privateConfiguration(insightsWorkspaceLifecycleSnapshot(raw))}
	}
	return members
}

// The unfiltered ARM group index also contains unknown kinds. Expand known
// product trees using the same native child adapters as independent inventory,
// then bind their full GET bodies. External resources require an existing,
// documented deletion relationship; a reference alone never makes them owned.
func (c *client) insightsWorkspaceResources(ctx context.Context, group, workspace string) ([]map[string]any, error) {
	values, err := c.insightsARMIndex(ctx, group+"/resources")
	if err != nil {
		return nil, err
	}
	resources := map[string]map[string]any{}
	var visit func(map[string]any, bool) error
	visit = func(raw map[string]any, indexed bool) error {
		id, kind, err := parseID(text(raw["id"]))
		if err != nil || id == group || !strings.HasPrefix(id, c.root()+"/") || !validResponseType(kind, text(raw["type"])) {
			return serviceDenied("invalid_insights_workspace_group_member")
		}
		if previous := resources[id]; previous != nil {
			if !nativeConfigurationContains(insightsWorkspaceResourceSnapshot(raw), insightsWorkspaceResourceSnapshot(previous)) {
				return serviceDenied("insights_workspace_descendant_disagrees")
			}
			return nil
		}
		rule, known := findType(kind)
		if known {
			endpoint, err := c.resourceURL(rule, id)
			if err != nil {
				return err
			}
			current, err := c.request(ctx, "GET", endpoint)
			if err != nil {
				return err
			}
			listed := insightsWorkspaceResourceSnapshot(raw)
			if !indexed && current.data["location"] == nil {
				delete(listed, "location") // c.children supplies inherited proxy locations.
			}
			if !insightsARMReadValid(current, id, kind) || !nativeConfigurationContains(listed, insightsWorkspaceResourceSnapshot(current.data)) {
				return serviceDenied("insights_workspace_member_changed")
			}
			raw = current.data
		}
		resources[id] = raw
		if !known {
			return nil
		}
		if strings.EqualFold(kind, applicationInsightsType) || strings.EqualFold(kind, aksType) || strings.EqualFold(kind, monitorWorkspaceType) {
			return serviceDenied("insights_workspace_nested_managed_controller")
		}
		children, err := c.children(ctx, rule, raw)
		if err != nil {
			return err
		}
		if rule.NativeType == vnetType {
			links, err := c.virtualNetworkDNSLinks(ctx, id)
			if err != nil {
				return err
			}
			for _, link := range links {
				children = append(children, link.data)
			}
		}
		for _, child := range children {
			if !inResourceGroup(text(child["id"]), group) && !aksExternalRelation(aksNativeAsset(raw), aksNativeAsset(child)) {
				return serviceDenied("insights_workspace_external_dependency_requires_unlink")
			}
			if err := visit(child, false); err != nil {
				return err
			}
		}
		return nil
	}
	indexed := map[string]bool{}
	for _, value := range values {
		raw := object(value)
		id, _, err := parseID(text(raw["id"]))
		if err != nil || !inResourceGroup(id, group) || indexed[id] {
			return nil, serviceDenied("invalid_insights_workspace_group_member")
		}
		indexed[id] = true
		if err := visit(raw, true); err != nil {
			return nil, err
		}
	}
	if resources[workspace] == nil {
		return nil, serviceDenied("insights_managed_workspace_missing_from_group")
	}
	result := make([]map[string]any, 0, len(resources))
	for _, id := range slices.Sorted(maps.Keys(resources)) {
		result = append(result, resources[id])
	}
	return result, nil
}

// Native bodies stay in this transient value. Only IDs and keyed configuration
// digests from state are projected into inventory or persisted with a plan.
type insightsWorkspaceSnapshot struct {
	state     map[string]any
	group     map[string]any
	resources []map[string]any
	incoming  []serviceChild
}

func (c *client) readInsightsWorkspace(ctx context.Context, parent string, raw map[string]any, groups map[string]map[string]any) (*insightsWorkspaceSnapshot, error) {
	workspace, err := insightsWorkspaceID(raw)
	if err != nil {
		return nil, err
	}
	state := map[string]any{"workspace": workspace, "managed_group": "", "detached_groups": map[string]any{}, "members": map[string]any{}, "incoming": map[string]any{}}
	snapshot := &insightsWorkspaceSnapshot{state: state}
	workspaceGroup := ""
	if strings.HasPrefix(workspace, c.root()+"/") {
		workspaceGroup = strings.Join(strings.Split(workspace, "/")[:5], "/")
		if groups[workspaceGroup] == nil {
			return nil, serviceDenied("insights_workspace_group_missing_from_index")
		}
	}
	for _, id := range slices.Sorted(maps.Keys(groups)) {
		owner, _ := insightsManagedBy(groups[id])
		if owner != parent && id != workspaceGroup {
			continue
		}
		group, err := c.insightsGroup(ctx, id, groups[id])
		if err != nil {
			return nil, err
		}
		configuration := c.privateConfiguration(insightsWorkspaceResourceSnapshot(group))
		if id == workspaceGroup {
			state["workspace_group_configuration"] = configuration
			state["workspace_group_lifecycle_configuration"] = c.privateConfiguration(insightsWorkspaceLifecycleSnapshot(group))
		}
		if owner != parent {
			continue // Shared workspaces are references, even with a managed name.
		}
		if inResourceGroup(parent, id) {
			return nil, serviceDenied("insights_managed_group_contains_controller")
		}
		if id != workspaceGroup {
			object(state["detached_groups"])[id] = configuration
			continue // Switching workspaces does not delete the original group.
		}
		resources, err := c.insightsWorkspaceResources(ctx, id, workspace)
		if err != nil {
			return nil, err
		}
		after, err := c.insightsGroup(ctx, id, group)
		if err != nil {
			return nil, err
		}
		if c.privateConfiguration(insightsWorkspaceResourceSnapshot(after)) != configuration {
			return nil, serviceDenied("insights_managed_workspace_group_changed")
		}
		state["managed_group"], state["members"] = id, c.insightsWorkspaceMemberBindings(resources)
		incoming, err := c.monitorPrivateLinkIncoming(ctx, asset.Identity{NativeID: workspace, NativeType: insightsWorkspaceType})
		if err != nil {
			return nil, err
		}
		snapshot.group, snapshot.resources, snapshot.incoming = group, resources, incoming
		for _, child := range incoming {
			configuration := ""
			if child.data != nil {
				configuration = c.privateConfiguration(monitorPrivateLinkSnapshot(child.kind, child.data))
			}
			object(state["incoming"])[child.id] = configuration
		}
	}
	current, err := c.insightsComponent(ctx, parent)
	if err != nil {
		return nil, err
	}
	if c.privateConfiguration(monitorPrivateLinkTargetSnapshot(current)) != c.privateConfiguration(monitorPrivateLinkTargetSnapshot(raw)) {
		return nil, serviceDenied("insights_workspace_component_changed")
	}
	return snapshot, nil
}

func (c *client) insightsWorkspaceInventory(ctx context.Context, parent *contracts.InventoryItem, raw map[string]any, groups map[string]map[string]any) error {
	if c.privateConfiguration(monitorPrivateLinkTargetSnapshot(raw)) != text(parent.Normalized["_monitor_private_link_target_configuration"]) {
		return serviceDenied("insights_workspace_component_changed")
	}
	snapshot, err := c.readInsightsWorkspace(ctx, parent.NativeID, raw, groups)
	if err != nil {
		return err
	}
	parent.Normalized["_insights_workspace"] = snapshot.state
	parent.Normalized["_insights_workspace_configuration"] = c.privateConfiguration(snapshot.state)
	return nil
}

func (c *client) insightsWorkspacePlan(parent asset.Asset) (map[string]any, error) {
	state, ok := parent.Normalized["_insights_workspace"].(map[string]any)
	if !ok || text(parent.Normalized["_insights_workspace_configuration"]) == "" || text(parent.Normalized["_insights_workspace_configuration"]) != c.privateConfiguration(state) {
		return nil, serviceDenied("insights_workspace_plan_changed")
	}
	workspace := text(state["workspace"])
	if workspace != "" {
		id, kind, err := parseID(workspace)
		if err != nil || id != workspace || !strings.EqualFold(kind, insightsWorkspaceType) {
			return nil, serviceDenied("invalid_insights_workspace_plan")
		}
	}
	group := text(state["managed_group"])
	members, membersOK := state["members"].(map[string]any)
	incoming, incomingOK := state["incoming"].(map[string]any)
	if !membersOK || !incomingOK {
		return nil, serviceDenied("incomplete_insights_workspace_plan")
	}
	if group == "" {
		if len(members) != 0 || len(incoming) != 0 {
			return nil, serviceDenied("unowned_insights_workspace_plan")
		}
		return state, nil
	}
	id, kind, err := parseID(group)
	if err != nil || id != group || !strings.EqualFold(kind, groupType) || !strings.HasPrefix(group, c.root()+"/") || !inResourceGroup(workspace, group) || inResourceGroup(parent.Identity.NativeID, group) || members[workspace] == nil || text(state["workspace_group_configuration"]) == "" || text(state["workspace_group_lifecycle_configuration"]) == "" {
		return nil, serviceDenied("invalid_insights_managed_group_plan")
	}
	for id, value := range members {
		member := object(value)
		canonical, kind, err := parseID(id)
		if err != nil || canonical != id || !strings.HasPrefix(id, c.root()+"/") || text(member["kind"]) != kind || text(member["configuration"]) == "" || text(member["lifecycle_configuration"]) == "" {
			return nil, serviceDenied("invalid_insights_workspace_member_plan")
		}
	}
	return state, nil
}

func insightsWorkspaceLifecycleState(state map[string]any) map[string]any {
	members := map[string]any{}
	for id, value := range object(state["members"]) {
		member := object(value)
		members[id] = map[string]any{"kind": member["kind"], "configuration": member["lifecycle_configuration"]}
	}
	return map[string]any{"workspace": state["workspace"], "managed_group": state["managed_group"], "detached_groups": state["detached_groups"], "group_configuration": state["workspace_group_lifecycle_configuration"], "members": members}
}
