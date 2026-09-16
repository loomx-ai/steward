package azure

import (
	"strings"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

// Validate the saved product shape only. The actual product preflight must still
// authenticate the controller, group and every member against native reads.
func (c *client) resourceGroupManagedMembers(byID map[asset.AssetID]contracts.ActionImpact) (map[asset.AssetID]asset.AssetID, error) {
	owners := map[asset.AssetID]asset.AssetID{}
	// Fleet records its Hub and node-group members in one authenticated flat
	// manifest. Validate that complete shape before projecting nested AKS work.
	for id, parent := range byID {
		if !parent.Delete || parent.Asset.Identity.NativeType != fleetType {
			continue
		}
		kind, _ := findType(fleetType)
		driver, err := newFleetAction(c, parent.Asset.Identity.ConnectionID, parent.Asset, kind)
		if err != nil {
			return nil, err
		}
		request := contracts.ActionRequest{Asset: parent.Asset, Action: "delete"}
		for _, child := range byID {
			if child.ControllerID == id {
				request.LifecycleImpacts = append(request.LifecycleImpacts, child)
			}
		}
		members, err := driver.rootImpacts(request)
		if err != nil {
			return nil, err
		}
		for _, child := range members {
			if owners[child.Asset.ID] != "" {
				return nil, serviceDenied("resource_group_ambiguous_fleet_member")
			}
			owners[child.Asset.ID] = id
		}
	}
	for id, parent := range byID {
		if owners[id] != "" {
			continue // The Fleet root already authenticated this Hub member.
		}
		if (parent.Asset.Identity.NativeType != aksType && parent.Asset.Identity.NativeType != monitorWorkspaceType && parent.Asset.Identity.NativeType != applicationInsightsType) || !parent.Delete {
			continue
		}
		group, err := c.resourceGroupManagedGroup(parent.Asset)
		if err != nil {
			return nil, err
		}
		if group == "" {
			continue
		}
		request := contracts.ActionRequest{Asset: parent.Asset, Action: "delete"}
		foundGroup := false
		for _, child := range byID {
			if child.ControllerID != id {
				continue
			}
			request.LifecycleImpacts = append(request.LifecycleImpacts, child)
			if strings.EqualFold(child.Asset.Identity.NativeID, group) && child.Asset.Identity.NativeType == groupType {
				foundGroup = true
			}
		}
		if !foundGroup {
			return nil, serviceDenied("resource_group_managed_group_missing")
		}
		kind, _ := findType(parent.Asset.Identity.NativeType)
		driver := action{client: c, kind: kind, id: strings.ToLower(parent.Asset.Identity.NativeID)}
		if _, err := driver.managedGroupImpacts(request, group); err != nil {
			return nil, err
		}
		for _, child := range request.LifecycleImpacts {
			owners[child.Asset.ID] = id
		}
	}
	return owners, nil
}

// Constructed only after the parent's actual native preflight succeeds. This
// context is not stored on a driver, serialized, or accepted as an action option.
type resourceGroupManagedPreflight struct {
	owner   string
	group   string
	members map[asset.AssetID]string
	// Present only after the Fleet driver's signed manifest and live preflight.
	fleetMembers map[string]any
}

func (s *resourceGroupManagedPreflight) permits(member asset.Asset, group map[string]any) bool {
	return s != nil && strings.EqualFold(member.Identity.NativeID, s.members[member.ID]) && inResourceGroup(member.Identity.NativeID, s.group) && strings.EqualFold(text(group["id"]), s.group) && strings.EqualFold(text(group["managedBy"]), s.owner)
}

// Recover native product and attachment chains without changing the flat review.
func resourceGroupManagedAttachmentParents(byID map[asset.AssetID]contracts.ActionImpact, owners map[asset.AssetID]asset.AssetID) (map[asset.AssetID]asset.AssetID, error) {
	parents := map[asset.AssetID]asset.AssetID{}
	for id, owner := range owners {
		parents[id] = owner
		var attachmentParent asset.AssetID
		for candidate, candidateOwner := range owners {
			if candidate == id || candidateOwner != owner {
				continue
			}
			related, err := resourceGroupProductRelation(byID[candidate].Asset, byID[id].Asset, byID[id].Delete)
			if err != nil {
				return nil, err
			}
			if !related {
				continue
			}
			if attachmentParent != "" && attachmentParent != candidate {
				return nil, serviceDenied("resource_group_ambiguous_managed_attachment")
			}
			attachmentParent = candidate
		}
		if attachmentParent != "" {
			parents[id] = attachmentParent
		}
	}
	for id, owner := range owners {
		seen := map[asset.AssetID]bool{}
		for current := id; current != owner; current = parents[current] {
			if current == "" || seen[current] {
				return nil, serviceDenied("resource_group_managed_attachment_cycle")
			}
			seen[current] = true
		}
	}
	return parents, nil
}

func (c *client) resourceGroupManagedGroup(parent asset.Asset) (string, error) {
	if parent.Identity.NativeType == applicationInsightsType {
		state, err := c.insightsWorkspacePlan(parent)
		if err != nil {
			return "", err
		}
		return text(state["managed_group"]), nil
	}
	return controllerResourceGroup(c.subscription, parent.Identity.NativeType, parent.Normalized)
}

// Azure deletes a VNet's external private-DNS links as native effects. A Fleet
// group operation may rely on that only for links in its authenticated manifest;
// their real product checks and own readbacks still run independently of this check.
func (c *client) resourceGroupFleetDNSLinks(network asset.Asset, links []serviceChild, scope *resourceGroupManagedPreflight) (bool, error) {
	if len(links) == 0 {
		return true, nil
	}
	if scope == nil || scope.members[network.ID] != network.Identity.NativeID || !inResourceGroup(network.Identity.NativeID, scope.group) || scope.fleetMembers == nil {
		return false, nil
	}
	for _, link := range links {
		member := object(scope.fleetMembers[link.id])
		proof := text(member["lifecycle_configuration"])
		if !strings.EqualFold(link.kind, privateDNSLinkType) || !strings.EqualFold(text(member["kind"]), privateDNSLinkType) || text(member["group"]) != scope.group || proof == "" || !aksExternalRelation(network, aksNativeAsset(link.data)) {
			return false, nil
		}
		if proof != c.privateConfiguration(fleetHubLifecycleSnapshot(link.data)) {
			return false, serviceDenied("resource_group_fleet_dns_link_changed")
		}
	}
	return true, nil
}
