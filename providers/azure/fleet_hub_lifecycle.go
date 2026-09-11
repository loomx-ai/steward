package azure

import (
	"context"
	"slices"
	"strings"

	"github.com/loomx-ai/steward/internal/app/governance"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

const fleetHubSource = "azure:fleet-managed-hub"

// Authenticate every Fleet before suppressing an ordinary AKS ownership walk.
// The service contributor below independently verifies these saved members
// against current native indexes. A saved proof cannot authorize a new member.
func (c *client) fleetHubOwners(assets []asset.Asset) (map[managedGroupMemberKey]asset.AssetID, error) {
	owners := map[managedGroupMemberKey]asset.AssetID{}
	seen := map[managedGroupMemberKey]bool{}
	for _, parent := range assets {
		if parent.Identity.Provider != asset.ProviderAzure || !strings.EqualFold(parent.Identity.NativeType, fleetType) {
			continue
		}
		key := managedGroupKey(parent.Identity, parent.Identity.NativeID)
		if parent.ID == "" || parent.Identity.ConnectionID == "" || parent.Identity.Partition == "" || seen[key] {
			return nil, serviceDenied("invalid_fleet_hub_graph_identity")
		}
		seen[key] = true
		if _, err := c.fleetRecordedReferences(parent); err != nil {
			return nil, err
		}
		state, err := c.fleetRecordedHub(parent.Identity.NativeID, parent.Normalized)
		if err != nil {
			return nil, err
		}
		if state["mode"] != "managed" {
			continue
		}
		if len(object(state["members"])) < 3 {
			return nil, serviceDenied("fleet_hub_members_require_refresh")
		}
		for id := range object(state["members"]) {
			key := managedGroupKey(parent.Identity, id)
			if owners[key] != "" {
				return nil, serviceDenied("ambiguous_fleet_hub_owner")
			}
			owners[key] = parent.ID
		}
	}
	return owners, nil
}

func (c *client) contributeFleetHub(ctx context.Context, parent asset.Asset, assets []asset.Asset) (result governance.Contribution, err error) {
	defer func() { err = contracts.DependencyReadError(err) }()
	ctx = context.WithValue(ctx, fleetHubReadContextKey{}, true)
	planned, err := c.fleetRecordedHub(parent.Identity.NativeID, parent.Normalized)
	if err != nil {
		return result, err
	}
	raw, err := c.fleetRead(ctx, fleetType, parent.Identity.NativeID)
	if err != nil {
		return result, err
	}
	if err := c.fleetIncarnation(parent, raw.data); err != nil {
		return result, err
	}
	if err := c.fleetContext(ctx, parent, raw.data); err != nil {
		return result, err
	}
	var resources map[string]map[string]any
	for range 2 {
		groups, err := c.insightsGroups(ctx)
		if err != nil {
			return result, err
		}
		state, current, err := c.readFleetHub(ctx, parent.Identity.NativeID, raw.data, groups, planned)
		if err != nil {
			return result, err
		}
		if c.privateConfiguration(state) != c.privateConfiguration(planned) {
			return result, serviceDenied("fleet_hub_inventory_changed")
		}
		resources = current
	}
	if planned["mode"] != "managed" {
		return result, nil
	}
	groups := []string{text(planned["group"]), text(planned["node_group"])}
	seen := map[asset.AssetID]bool{parent.ID: true}
	for _, value := range assets {
		identity := value.Identity
		if identity.Provider != parent.Identity.Provider || identity.ConnectionID != parent.Identity.ConnectionID || identity.Partition != parent.Identity.Partition {
			continue
		}
		id, kind, err := parseID(identity.NativeID)
		if resources[id] == nil && !slices.ContainsFunc(groups, func(group string) bool { return inResourceGroup(id, group) }) {
			continue
		}
		if rbacResourceKind(kind) != "" || strings.EqualFold(kind, diagnosticSettingsType) {
			continue // Independently reviewed prerequisites remain separate.
		}
		if err != nil || id != identity.NativeID || !strings.EqualFold(kind, identity.NativeType) || value.ID == "" || seen[value.ID] || resources[id] == nil {
			return result, serviceDenied("invalid_fleet_hub_managed_asset")
		}
		if id == text(planned["cluster"]) {
			group, err := aksNodeGroup(c.subscription, value.Normalized)
			if err != nil || group != text(planned["node_group"]) {
				return result, serviceDenied("fleet_hub_cluster_asset_changed")
			}
		}
		seen[value.ID] = true
	}
	for _, group := range groups {
		var members []map[string]any
		for id, member := range object(planned["members"]) {
			if text(object(member)["group"]) == group {
				members = append(members, resources[id])
			}
		}
		contribution, err := c.bindManagedGroup(ctx, parent, group, fleetHubSource, raw.requestID, members, assets)
		if err != nil {
			return governance.Contribution{}, err
		}
		// Fleet remains non-actionable until its root driver verifies every
		// residual member. Do not advertise that unimplemented readback yet.
		for _, binding := range contribution.Bindings {
			delete(binding.Evidence, graph.LifecycleEvidenceControllerVerifiesManagedAbsence)
		}
		for _, reference := range contribution.Unresolved {
			delete(reference.Evidence, graph.LifecycleEvidenceControllerVerifiesManagedAbsence)
		}
		result.Bindings = append(result.Bindings, contribution.Bindings...)
		result.Relationships = append(result.Relationships, contribution.Relationships...)
		result.Unresolved = append(result.Unresolved, contribution.Unresolved...)
	}
	return result, nil
}
