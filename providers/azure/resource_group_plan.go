package azure

import (
	"bytes"
	"encoding/json"
	"slices"
	"strings"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

// Project an explicit reviewed group controller tree into real product requests.
// This validates scope and relationships only: product preflight, native indexes,
// prerequisites and final readbacks must still run before cleanup is enabled.
// In particular, group membership never authorizes deleting an external resource.
func (c *client) resourceGroupProductRequests(req contracts.ActionRequest) (map[asset.AssetID]contracts.ActionRequest, error) {
	if _, err := c.resourceGroupOperationBinding(req, nil); err != nil {
		return nil, err
	}
	root := req.Asset
	if root.ID == "" || root.Identity.Provider != asset.ProviderAzure || root.Identity.Partition != "azure" || root.Identity.ConnectionID == "" {
		return nil, serviceDenied("invalid_resource_group_plan_owner")
	}
	// Freeze nested maps too. Product drivers must not be able to mutate the
	// reviewed parent request through aliased normalized configuration maps.
	wire, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	var frozen contracts.ActionRequest
	decoder := json.NewDecoder(bytes.NewReader(wire))
	decoder.UseNumber()
	if err = decoder.Decode(&frozen); err != nil {
		return nil, err
	}
	req = frozen
	byID := map[asset.AssetID]contracts.ActionImpact{}
	native := map[string]bool{strings.ToLower(root.Identity.NativeID): true}
	validMember := func(v asset.Asset) error {
		id, kind, err := deploymentStackMemberID(v.Identity.NativeID)
		if err != nil || v.ID == "" || v.ID == root.ID || v.Identity.Provider != root.Identity.Provider || v.Identity.Partition != root.Identity.Partition || v.Identity.ConnectionID != root.Identity.ConnectionID || !strings.HasPrefix(id, strings.ToLower(c.root())+"/") || !strings.EqualFold(kind, v.Identity.NativeType) || native[id] {
			return serviceDenied("invalid_resource_group_plan_member")
		}
		native[id] = true
		return nil
	}
	for _, impact := range req.LifecycleImpacts {
		if _, exists := byID[impact.Asset.ID]; exists {
			return nil, serviceDenied("duplicate_resource_group_plan_member")
		}
		if err := validMember(impact.Asset); err != nil {
			return nil, err
		}
		if !impact.Delete && inResourceGroup(impact.Asset.Identity.NativeID, root.Identity.NativeID) {
			return nil, serviceDenied("resource_group_cannot_retain_contained_member")
		}
		byID[impact.Asset.ID] = impact
	}
	managed, err := c.resourceGroupManagedMembers(byID)
	if err != nil {
		return nil, err
	}
	for _, impact := range req.LifecycleImpacts {
		if impact.Asset.Identity.NativeType == groupType && managed[impact.Asset.ID] == "" {
			return nil, serviceDenied("invalid_resource_group_plan_member")
		}
	}
	for _, impact := range req.LifecycleImpacts {
		seen := map[asset.AssetID]bool{impact.Asset.ID: true}
		for current := impact; ; {
			if current.ControllerID == root.ID {
				if !current.Delete || !inResourceGroup(current.Asset.Identity.NativeID, root.Identity.NativeID) {
					return nil, serviceDenied("resource_group_external_member_has_no_native_controller")
				}
				break
			}
			parent, exists := byID[current.ControllerID]
			if !exists || seen[current.ControllerID] || current.Delete && !parent.Delete {
				return nil, serviceDenied("invalid_resource_group_controller_tree")
			}
			seen[current.ControllerID] = true
			relation, err := resourceGroupProductRelation(parent.Asset, current.Asset, current.Delete)
			if err != nil {
				return nil, err
			}
			if !relation && managed[current.Asset.ID] != parent.Asset.ID {
				return nil, serviceDenied("resource_group_product_relation_unverified")
			}
			current = parent
		}
	}
	attachmentParents, err := resourceGroupManagedAttachmentParents(byID, managed)
	if err != nil {
		return nil, err
	}
	prerequisites := map[asset.AssetID]bool{}
	for i, p := range req.PrerequisiteDeletions {
		// The cleanup worker binds prerequisites to the outer group step. Recover
		// their component owner only from a validated native child identity.
		if insightsLegacyKind(p.Asset.Identity.NativeType).kind != "" || insightsARMChildKind(p.Asset.Identity.NativeType) != "" {
			id, owner, kind, _, err := insightsChildIdentity(p.Asset.Identity.NativeID)
			if err != nil || id != p.Asset.Identity.NativeID || kind != p.Asset.Identity.NativeType {
				return nil, serviceDenied("invalid_resource_group_insights_prerequisite")
			}
			if p.ControllerID == root.ID {
				for memberID, candidate := range byID {
					if candidate.Delete && candidate.Asset.Identity.NativeType == applicationInsightsType && candidate.Asset.Identity.NativeID == owner {
						p.ControllerID = memberID
						break
					}
				}
			}
			parent, found := byID[p.ControllerID]
			if !found || !parent.Delete || parent.Asset.Identity.NativeType != applicationInsightsType || parent.Asset.Identity.NativeID != owner {
				return nil, serviceDenied("resource_group_insights_parent_changed")
			}
			req.PrerequisiteDeletions[i] = p
		}
		if p.Asset.Identity.NativeType == monitorScopedResourceType {
			owner, err := c.resourceGroupInsightsLinkOwner(p.Asset, byID)
			if err != nil {
				return nil, err
			}
			if owner != "" {
				if p.ControllerID != root.ID && p.ControllerID != owner {
					return nil, serviceDenied("resource_group_insights_link_owner_changed")
				}
				p.ControllerID = owner
				req.PrerequisiteDeletions[i] = p
			}
		}
		parent, found := byID[p.ControllerID]
		if p.ControllerID == root.ID && !inResourceGroup(p.Asset.Identity.NativeID, root.Identity.NativeID) {
			return nil, serviceDenied("resource_group_external_prerequisite_unverified")
		}
		if strings.EqualFold(p.Asset.Identity.NativeType, groupType) || !p.Delete || prerequisites[p.Asset.ID] || byID[p.Asset.ID].Asset.ID != "" || p.ControllerID != root.ID && (!found || !parent.Delete) {
			return nil, serviceDenied("invalid_resource_group_prerequisite")
		}
		if insightsLegacyKind(p.Asset.Identity.NativeType).kind != "" {
			// Legacy selectors are case-sensitive opaque URL identities, not ARM IDs.
			// They are independent prerequisites of their exact reviewed component.
			kind, known := findType(p.Asset.Identity.NativeType)
			if !known || !found || parent.Asset.Identity.NativeType != applicationInsightsType || p.Asset.ID == "" || p.Asset.ID == root.ID || p.Asset.Identity.Partition != root.Identity.Partition || native[p.Asset.Identity.NativeID] {
				return nil, serviceDenied("invalid_resource_group_legacy_prerequisite")
			}
			driver, err := newInsightsChildAction(c, root.Identity.ConnectionID, p.Asset, kind)
			if err != nil {
				return nil, err
			}
			if driver.parent != parent.Asset.Identity.NativeID {
				return nil, serviceDenied("resource_group_legacy_parent_changed")
			}
			if err := driver.identity(contracts.ActionRequest{Asset: p.Asset, Action: "delete"}); err != nil {
				return nil, err
			}
			native[p.Asset.Identity.NativeID] = true
		} else if err := validMember(p.Asset); err != nil {
			return nil, err
		}
		prerequisites[p.Asset.ID] = true
	}
	out := map[asset.AssetID]contracts.ActionRequest{}
	compare := func(a, b contracts.ActionImpact) int { return strings.Compare(string(a.Asset.ID), string(b.Asset.ID)) }
	for id, impact := range byID {
		if !impact.Delete {
			continue
		}
		if managed[id] != "" {
			_, known := findType(impact.Asset.Identity.NativeType)
			if !known || impact.Asset.Identity.NativeType == groupType {
				continue
			}
		}
		member := contracts.ActionRequest{Asset: impact.Asset, Action: "delete", IdempotencyKey: req.IdempotencyKey + ":member:" + string(id)}
		for _, child := range req.LifecycleImpacts {
			if child.Asset.ID == id {
				continue
			}
			for parent := child.ControllerID; parent != root.ID; parent = byID[parent].ControllerID {
				if parent == id {
					member.LifecycleImpacts = append(member.LifecycleImpacts, child)
					break
				}
			}
		}
		if owner := managed[id]; owner != "" {
			for _, child := range req.LifecycleImpacts {
				if child.Asset.ID == id || managed[child.Asset.ID] != owner {
					continue
				}
				for parent := attachmentParents[child.Asset.ID]; parent != owner; parent = attachmentParents[parent] {
					if parent == id {
						child.ControllerID = attachmentParents[child.Asset.ID]
						member.LifecycleImpacts = append(member.LifecycleImpacts, child)
						break
					}
				}
			}
		}
		for _, p := range req.PrerequisiteDeletions {
			if p.ControllerID == id {
				member.PrerequisiteDeletions = append(member.PrerequisiteDeletions, p)
			}
		}
		slices.SortFunc(member.LifecycleImpacts, compare)
		slices.SortFunc(member.PrerequisiteDeletions, compare)
		// Each product request gets its own copy, including siblings' descendants.
		wire, err := json.Marshal(member)
		if err != nil {
			return nil, err
		}
		var detached contracts.ActionRequest
		decoder := json.NewDecoder(bytes.NewReader(wire))
		decoder.UseNumber()
		if err = decoder.Decode(&detached); err != nil {
			return nil, err
		}
		out[id] = detached
	}
	return out, nil
}

func resourceGroupProductRelation(parent, child asset.Asset, deleting bool) (bool, error) {
	if parent.Identity.NativeType == vmType || parent.Identity.NativeType == nicType {
		id, _, err := parseID(parent.Identity.NativeID)
		if err != nil {
			return false, err
		}
		attachments, err := resourceAttachments(strings.Split(id, "/")[2], parent.Identity.NativeType, parent.Normalized)
		if err != nil {
			return false, err
		}
		for _, a := range attachments {
			if strings.EqualFold(a.id, child.Identity.NativeID) && strings.EqualFold(a.kind, child.Identity.NativeType) {
				return !deleting || a.delete, nil
			}
		}
	}
	return deleting && !servicePrerequisiteKind(parent.Identity.NativeType, child.Identity.NativeType) && slices.ContainsFunc(serviceChildKinds(parent.Identity.NativeType), func(k string) bool { return strings.EqualFold(k, child.Identity.NativeType) }) && serviceChildRelation(parent, child), nil
}

// An external AMPLS association belongs in the component request only when it
// targets that component or its authenticated managed workspace. The association
// itself still executes independently through its registered product driver.
func (c *client) resourceGroupInsightsLinkOwner(link asset.Asset, members map[asset.AssetID]contracts.ActionImpact) (asset.AssetID, error) {
	target, err := monitorPrivateLinkReference(map[string]any{"properties": link.Normalized})
	if err != nil {
		return "", err
	}
	var owner asset.AssetID
	for id, member := range members {
		if !member.Delete || member.Asset.Identity.NativeType != applicationInsightsType {
			continue
		}
		state, err := c.insightsWorkspacePlan(member.Asset)
		if err != nil {
			return "", err
		}
		workspace := target == text(state["workspace"]) && text(state["managed_group"]) != ""
		if target != member.Asset.Identity.NativeID && !workspace {
			continue
		}
		proof := text(link.Normalized["_monitor_private_link_private_configuration"])
		if proof == "" || workspace && proof != text(object(state["incoming"])[link.Identity.NativeID]) {
			return "", serviceDenied("resource_group_insights_link_configuration_changed")
		}
		if owner != "" {
			return "", serviceDenied("resource_group_insights_link_ambiguous_owner")
		}
		owner = id
	}
	return owner, nil
}
