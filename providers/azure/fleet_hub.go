package azure

import (
	"context"
	"maps"
	"net"
	"slices"
	"strings"

	"github.com/loomx-ai/steward/internal/provider/contracts"
)

const (
	fleetHubState = "_fleet_hub"
	fleetHubProof = "_fleet_hub_binding"
)

type fleetHubReadContextKey struct{}

func (c *client) fleetHubBinding(id string, normalized map[string]any) string {
	return c.privateConfiguration(map[string]any{"id": id, "configuration": normalized[fleetConfigurationProof], "context": normalized[fleetContextProof], "hub": normalized[fleetHubState], "protocol": "fleet-hub-1"})
}

// Saved anchors can recover groups or the cluster omitted from a later LIST.
// They authorize individual reads, never an inference that an omission is gone.
func (c *client) fleetRecordedHub(id string, normalized map[string]any) (map[string]any, error) {
	state, ok := normalized[fleetHubState].(map[string]any)
	if !ok || state == nil || text(normalized[fleetConfigurationProof]) == "" || text(normalized[fleetContextProof]) == "" || text(normalized[fleetHubProof]) != c.fleetHubBinding(id, normalized) {
		return nil, serviceDenied("fleet_hub_proof_changed")
	}
	return state, nil
}

func fleetHubFQDN(value any) (string, error) {
	name, ok := value.(string)
	if !ok || len(name) > 253 || !strings.Contains(name, ".") || net.ParseIP(name) != nil {
		return "", serviceDenied("invalid_fleet_hub_fqdn")
	}
	name = strings.ToLower(name)
	for _, label := range strings.Split(name, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", serviceDenied("invalid_fleet_hub_fqdn")
		}
		for _, char := range label {
			if !(char >= 'a' && char <= 'z' || char >= '0' && char <= '9' || char == '-') {
				return "", serviceDenied("invalid_fleet_hub_fqdn")
			}
		}
	}
	return name, nil
}

func fleetHubPrivate(profile map[string]any) (bool, error) {
	if monitorRuleFields(profile, "enablePrivateCluster") != nil {
		return false, serviceDenied("ambiguous_fleet_hub_access")
	}
	if value, present := profile["enablePrivateCluster"]; present {
		private, ok := value.(bool)
		if !ok {
			return false, serviceDenied("invalid_fleet_hub_access")
		}
		return private, nil
	}
	return false, nil
}

func (c *client) fleetHubCluster(ctx context.Context, id string, listed map[string]any) (map[string]any, error) {
	rule, _ := findType(aksType)
	endpoint, err := c.resourceURL(rule, id)
	if err != nil {
		return nil, err
	}
	live, err := c.request(ctx, "GET", endpoint)
	if err != nil {
		return nil, err
	}
	if !insightsARMReadValid(live, id, aksType) || live.data["id"] != text(live.data["id"]) || monitorRuleFields(live.data, "id", "name", "type", "location", "properties", "managedBy", "tags", "systemData", "etag") != nil || !nativeConfigurationContains(insightsWorkspaceResourceSnapshot(listed), insightsWorkspaceResourceSnapshot(live.data)) {
		return nil, serviceDenied("fleet_hub_cluster_changed")
	}
	props, ok := live.data["properties"].(map[string]any)
	if !ok || props == nil || monitorRuleFields(props, "fqdn", "privateFQDN", "apiServerAccessProfile", "nodeResourceGroup") != nil {
		return nil, serviceDenied("invalid_fleet_hub_cluster")
	}
	if profile, present := props["apiServerAccessProfile"]; present {
		if _, ok := profile.(map[string]any); !ok {
			return nil, serviceDenied("invalid_fleet_hub_cluster_access")
		}
	}
	return live.data, nil
}

// Fleet exposes the hub's FQDN, not its ARM ID. Native managedBy plus the
// matching AKS API endpoint establishes the hub anchor; AKS then names its
// node resource group, whose native owner must point back to that AKS ID.
// Names such as FL_* and MC_FL_* never establish ownership.
// https://learn.microsoft.com/azure/kubernetes-fleet/concepts-lifecycle
func (c *client) fleetHubObservation(ctx context.Context, raw map[string]any, indexedGroups map[string]map[string]any, known map[string]any) (map[string]any, error) {
	ctx = context.WithValue(ctx, fleetHubReadContextKey{}, true)
	id, kind, err := fleetIdentity(text(raw["id"]))
	if err != nil || kind != fleetType || fleetValidate(kind, raw) != nil || !strings.HasPrefix(id, c.root()+"/") {
		return nil, serviceDenied("invalid_fleet_hub_controller")
	}
	state := map[string]any{"mode": "unverified", "location": resourceRegion(raw), "anchors": map[string]any{}, "candidates": map[string]any{}}
	unverified := func(reason string) (map[string]any, error) {
		state["reason"] = reason
		return state, nil
	}
	groups := maps.Clone(indexedGroups)
	if previous := text(known["group"]); previous != "" && groups[previous] == nil {
		res, err := c.request(ctx, "GET", apiURL(previous, resourcesVersion))
		if err != nil && !isNotFound(err) {
			return nil, err
		}
		if err == nil {
			if !insightsARMReadValid(res, previous, groupType) {
				return nil, serviceDenied("invalid_fleet_known_hub_group")
			}
			groups[previous] = res.data
		}
	}
	var owned []string
	for _, groupID := range slices.Sorted(maps.Keys(groups)) {
		owner, err := insightsManagedBy(groups[groupID])
		if err != nil {
			return nil, err
		}
		if owner != id {
			continue
		}
		group, err := c.insightsGroup(ctx, groupID, groups[groupID])
		if err != nil {
			return nil, err
		}
		groups[groupID] = group
		owned = append(owned, groupID)
		object(state["candidates"])[groupID] = c.privateConfiguration(insightsWorkspaceResourceSnapshot(group))
	}
	hub, present := object(raw["properties"])["hubProfile"]
	if !present {
		if len(owned) != 0 {
			return unverified("fleet_hubless_has_managed_group")
		}
		state["mode"] = "none"
		return state, nil
	}
	profile := object(hub)
	if monitorRuleFields(profile, "fqdn", "apiServerAccessProfile") != nil {
		return nil, serviceDenied("ambiguous_fleet_hub_profile")
	}
	if len(owned) != 1 {
		return unverified("fleet_hub_group_not_unique")
	}
	group := owned[0]
	if inResourceGroup(id, group) {
		return unverified("fleet_hub_group_contains_controller")
	}
	fqdn, err := fleetHubFQDN(profile["fqdn"])
	if err != nil {
		return unverified("fleet_hub_endpoint_unavailable")
	}
	private, err := fleetHubPrivate(object(profile["apiServerAccessProfile"]))
	if err != nil {
		return nil, err
	}
	values, err := c.insightsARMIndex(ctx, group+"/resources")
	if err != nil {
		return nil, err
	}
	clusters := map[string]map[string]any{}
	seen := map[string]bool{}
	for _, value := range values {
		listed := object(value)
		childID, childType, err := parseID(text(listed["id"]))
		if err != nil || childID == group || !inResourceGroup(childID, group) || !validResponseType(childType, text(listed["type"])) || seen[childID] {
			return nil, serviceDenied("invalid_fleet_hub_group_member")
		}
		seen[childID] = true
		if !strings.EqualFold(childType, aksType) {
			continue
		}
		cluster, err := c.fleetHubCluster(ctx, childID, listed)
		if err != nil {
			return nil, err
		}
		clusters[childID] = cluster
	}
	if previous := text(known["cluster"]); previous != "" && inResourceGroup(previous, group) && clusters[previous] == nil {
		cluster, err := c.fleetHubCluster(ctx, previous, map[string]any{"id": previous, "type": aksType})
		if err != nil && !isNotFound(err) {
			return nil, err
		}
		if err == nil {
			clusters[previous] = cluster
		}
	}
	if len(clusters) != 1 {
		return unverified("fleet_hub_cluster_not_unique")
	}
	clusterID := slices.Sorted(maps.Keys(clusters))[0]
	cluster := clusters[clusterID]
	props := object(cluster["properties"])
	clusterPrivate, err := fleetHubPrivate(object(props["apiServerAccessProfile"]))
	if err != nil {
		return nil, err
	}
	field := "fqdn"
	if private {
		field = "privateFQDN"
	}
	clusterFQDN, err := fleetHubFQDN(props[field])
	if err != nil || clusterFQDN != fqdn || private != clusterPrivate || resourceRegion(cluster) != resourceRegion(raw) {
		return unverified("fleet_hub_cluster_reference_disagrees")
	}
	nodeGroup, err := aksNodeGroup(c.subscription, props)
	if err != nil || props["nodeResourceGroup"] != text(props["nodeResourceGroup"]) || nodeGroup == group || inResourceGroup(id, nodeGroup) {
		return unverified("fleet_hub_node_group_unavailable")
	}
	listed := groups[nodeGroup]
	if listed == nil {
		listed = map[string]any{"id": nodeGroup, "type": groupType, "managedBy": clusterID}
	}
	nodes, err := c.insightsGroup(ctx, nodeGroup, listed)
	if err != nil {
		return nil, err
	}
	owner, err := insightsManagedBy(nodes)
	if err != nil || owner != clusterID {
		return unverified("fleet_hub_node_group_owner_disagrees")
	}
	state["mode"], state["group"], state["cluster"], state["node_group"] = "managed", group, clusterID, nodeGroup
	for anchorID, resource := range map[string]map[string]any{group: groups[group], clusterID: cluster, nodeGroup: nodes} {
		_, typ, _ := parseID(anchorID)
		object(state["anchors"])[anchorID] = map[string]any{"kind": typ, "configuration": c.privateConfiguration(insightsWorkspaceResourceSnapshot(resource))}
	}
	return state, nil
}

func (r *Runtime) fleetHubInventory(ctx context.Context, c *client, item *contracts.InventoryItem, raw map[string]any, groups map[string]map[string]any, known map[string]any) error {
	ctx = context.WithValue(ctx, fleetHubReadContextKey{}, true)
	var previous map[string]any
	if known[fleetHubState] != nil || known[fleetHubProof] != nil {
		var err error
		previous, err = c.fleetRecordedHub(item.NativeID, known)
		if err != nil {
			return err
		}
	}
	state, err := c.fleetHubObservation(ctx, raw, groups, previous)
	if err != nil {
		return err
	}
	// Recheck every anchor and the Fleet after the join. A configuration or
	// ownership change while discovering the other side invalidates the scan.
	for _, id := range slices.Sorted(maps.Keys(object(state["anchors"]))) {
		anchor := object(object(state["anchors"])[id])
		kind := text(anchor["kind"])
		rule, ok := findType(kind)
		if !ok {
			return serviceDenied("unknown_fleet_hub_anchor")
		}
		endpoint, err := c.resourceURL(rule, id)
		if err != nil {
			return err
		}
		current, err := c.request(ctx, "GET", endpoint)
		if err != nil {
			return err
		}
		if !insightsARMReadValid(current, id, kind) || text(anchor["configuration"]) != c.privateConfiguration(insightsWorkspaceResourceSnapshot(current.data)) {
			return serviceDenied("fleet_hub_anchor_changed_during_scan")
		}
	}
	current, err := c.fleetRead(ctx, fleetType, item.NativeID)
	if err != nil {
		return err
	}
	if text(item.Normalized[fleetConfigurationProof]) != c.privateConfiguration(fleetSnapshot(fleetType, current.data)) {
		return serviceDenied("fleet_hub_controller_changed_during_scan")
	}
	item.Normalized[fleetHubState] = state
	item.Normalized[fleetHubProof] = c.fleetHubBinding(item.NativeID, item.Normalized)
	return nil
}
