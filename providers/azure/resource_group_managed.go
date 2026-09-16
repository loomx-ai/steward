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
	for id, parent := range byID {
		if parent.Asset.Identity.NativeType != aksType || !parent.Delete {
			continue
		}
		group, err := aksNodeGroup(c.subscription, parent.Asset.Normalized)
		if err != nil {
			return nil, err
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
		kind, _ := findType(aksType)
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
}

func (s *resourceGroupManagedPreflight) permits(member asset.Asset, group map[string]any) bool {
	return s != nil && strings.EqualFold(member.Identity.NativeID, s.members[member.ID]) && inResourceGroup(member.Identity.NativeID, s.group) && strings.EqualFold(text(group["id"]), s.group) && strings.EqualFold(text(group["managedBy"]), s.owner)
}

// Recover product-local attachment chains without changing the AKS flat graph.
func resourceGroupManagedAttachmentParents(byID map[asset.AssetID]contracts.ActionImpact, owners map[asset.AssetID]asset.AssetID) (map[asset.AssetID]asset.AssetID, error) {
	parents := map[asset.AssetID]asset.AssetID{}
	for id, owner := range owners {
		parents[id] = owner
		var attachmentParent asset.AssetID
		for candidate, candidateOwner := range owners {
			if candidate == id || candidateOwner != owner {
				continue
			}
			related, err := deploymentStackAttachmentRelation(byID[candidate].Asset, byID[id].Asset)
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
