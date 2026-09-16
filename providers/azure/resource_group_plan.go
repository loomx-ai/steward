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
		if err != nil || v.ID == "" || v.ID == root.ID || v.Identity.Provider != root.Identity.Provider || v.Identity.Partition != root.Identity.Partition || v.Identity.ConnectionID != root.Identity.ConnectionID || !strings.HasPrefix(id, strings.ToLower(c.root())+"/") || !strings.EqualFold(kind, v.Identity.NativeType) || strings.EqualFold(kind, groupType) || native[id] {
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
			if !relation {
				return nil, serviceDenied("resource_group_product_relation_unverified")
			}
			current = parent
		}
	}
	prerequisites := map[asset.AssetID]bool{}
	for _, p := range req.PrerequisiteDeletions {
		parent, found := byID[p.ControllerID]
		if p.ControllerID == root.ID && !inResourceGroup(p.Asset.Identity.NativeID, root.Identity.NativeID) {
			return nil, serviceDenied("resource_group_external_prerequisite_unverified")
		}
		if !p.Delete || prerequisites[p.Asset.ID] || byID[p.Asset.ID].Asset.ID != "" || p.ControllerID != root.ID && (!found || !parent.Delete) {
			return nil, serviceDenied("invalid_resource_group_prerequisite")
		}
		if err := validMember(p.Asset); err != nil {
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
