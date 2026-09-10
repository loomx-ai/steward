package azure

import (
	"context"
	"slices"
	"strings"

	"github.com/loomx-ai/steward/internal/app/governance"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func batchVMKind(kind string) bool {
	return slices.Contains([]string{scaleSetVMType, scaleSetVMExtensionType, scaleSetNICType, scaleSetIPConfigType, scaleSetPublicIPType, diskType}, kind)
}

func batchNodeVM(raw map[string]any) string {
	return strings.ToLower(text(object(raw["virtualMachineInfo"])["scaleSetVmResourceId"]))
}

// The Batch service supplies the exact current VM ID in UserSubscription mode.
// The VM's native Compute/Network APIs supply its owned children. Neither the
// scale set nor resources inferred from a pool name belong to this node.
func (c *client) batchVMTree(ctx context.Context, account batchAccountContext, node map[string]any) ([]batchMember, error) {
	vmID := batchNodeVM(node)
	mode := text(object(account.raw["properties"])["poolAllocationMode"])
	if mode != "UserSubscription" {
		if vmID != "" || mode != "" && mode != "BatchService" {
			return nil, serviceDenied("batch_vm_allocation_mode_mismatch")
		}
		return nil, nil
	}
	id, kind, err := parseID(vmID)
	if err != nil || !strings.EqualFold(kind, scaleSetVMType) || !strings.HasPrefix(id, c.root()+"/") {
		return nil, serviceDenied("batch_node_vm_identity_missing")
	}
	parentID := redisParentID(id)
	parent, err := c.linkedResource(ctx, parentID)
	if err != nil {
		return nil, err
	}
	orchestration, err := scaleSetMode(object(parent["properties"]))
	if err != nil || orchestration != "Uniform" || !strings.EqualFold(resourceRegion(parent), account.location) {
		return nil, serviceDenied("batch_vm_scale_set_changed")
	}
	var members []batchMember
	seen := map[string]bool{}
	var visit func(string, string, string, map[string]any) error
	visit = func(id, kind, parent string, raw map[string]any) error {
		if !batchVMKind(kind) || !strings.HasPrefix(id, c.root()+"/") || seen[id] || ((text(raw["location"]) != "" || kind == scaleSetVMType || kind == diskType) && !strings.EqualFold(resourceRegion(raw), account.location)) {
			return serviceDenied("invalid_batch_vm_member")
		}
		seen[id] = true
		members = append(members, batchMember{id: id, kind: kind, parent: parent, raw: raw})
		children, err := c.serviceChildren(ctx, asset.Identity{NativeID: id, NativeType: kind}, raw)
		if err != nil {
			return err
		}
		for _, child := range children {
			if child.direct {
				return serviceDenied("batch_vm_child_lifetime_unproven")
			}
			if err := visit(child.id, child.kind, id, child.data); err != nil {
				return err
			}
		}
		return nil
	}
	vm, err := c.linkedResource(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := visit(id, scaleSetVMType, strings.ToLower(text(node["url"])), vm); err != nil {
		return nil, err
	}
	return members, nil
}

func (c *client) batchVMResources(members []batchMember) map[string]any {
	result := map[string]any{}
	for _, member := range members {
		result[member.id] = map[string]any{"kind": member.kind, "parent": member.parent, "configuration": c.privateConfiguration(batchSnapshot(member.kind, member.raw))}
	}
	return result
}

func (c *client) batchNodeBinding(node map[string]any, resources map[string]any) string {
	return c.privateConfiguration(map[string]any{"node": strings.ToLower(text(node["url"])), "vm": batchNodeVM(node), "allocationTime": node["allocationTime"], "isDedicated": node["isDedicated"], "resources": resources})
}

func (t batchTopology) verifyAsset(c *client, planned asset.Asset, member batchMember) error {
	if !isBatchType(member.kind) {
		if err := c.servicePrivateIncarnation(planned, member.raw); err != nil {
			return err
		}
		return serviceIncarnation(planned, member.raw)
	}
	if err := batchIncarnation(c, planned, member.raw); err != nil {
		return err
	}
	if member.kind == batchNodeType {
		var members []batchMember
		for _, candidate := range t.members {
			if batchVMKind(candidate.kind) && t.descendant(candidate.id, member.id) {
				members = append(members, candidate)
			}
		}
		if text(planned.Normalized["_batch_vm_binding"]) != c.batchNodeBinding(member.raw, c.batchVMResources(members)) {
			return serviceDenied("batch_node_vm_configuration_changed")
		}
	}
	return nil
}

// Authenticate the complete physical resource set even after the node is gone.
// Readback must not succeed with a removed impact or a forged ARM resource ID.
func (a *batchAction) vmImpacts(request contracts.ActionRequest, impacts map[string]contracts.ActionImpact) error {
	values := map[asset.AssetID]asset.Asset{request.Asset.ID: request.Asset}
	for _, impact := range impacts {
		values[impact.Asset.ID] = impact.Asset
	}
	verified := map[string]bool{}
	for _, node := range values {
		if node.Identity.NativeType != batchNodeType {
			continue
		}
		resources := object(node.Normalized["_batch_vm_resources"])
		if resources == nil || text(node.Normalized["_batch_vm_binding"]) != a.client.batchNodeBinding(node.Normalized, resources) {
			return serviceDenied("invalid_batch_vm_binding")
		}
		for id, entry := range resources {
			member := object(entry)
			impact, exists := impacts[id]
			parent := values[impact.ControllerID]
			if !exists || !batchVMKind(impact.Asset.Identity.NativeType) || member["kind"] != impact.Asset.Identity.NativeType || member["parent"] != strings.ToLower(parent.Identity.NativeID) || verified[id] {
				return serviceDenied("batch_vm_impact_missing_from_plan")
			}
			verified[id] = true
		}
	}
	for id, impact := range impacts {
		if batchVMKind(impact.Asset.Identity.NativeType) && !verified[id] {
			return serviceDenied("unreviewed_batch_vm_impact")
		}
	}
	return nil
}

func (a *batchAction) protectedVM(ctx context.Context, account batchAccountContext, member batchMember, locks []any) error {
	if member.kind == scaleSetVMType {
		parent, err := a.client.linkedResource(ctx, redisParentID(member.id))
		if err != nil {
			return err
		}
		kind, _ := findType(scaleSetType)
		if reason := protectionReason(kind, parent); reason != "" && !(reason == "azure_managed_resource" && strings.EqualFold(text(parent["managedBy"]), account.id)) {
			return serviceDenied(reason)
		}
	}
	groupID := strings.Join(strings.Split(member.id, "/")[:5], "/")
	group, err := a.client.request(ctx, "GET", apiURL(groupID, resourcesVersion))
	if err != nil {
		return err
	}
	if !validResourceResponse(group, groupID, groupType) || (text(group.data["managedBy"]) != "" && !strings.EqualFold(text(group.data["managedBy"]), account.id)) {
		return serviceDenied("azure_managed_resource_group")
	}
	if locked(member.id, locks) || protectedAzureTags(object(group.data["tags"])) {
		return serviceDenied("batch_vm_scope_protected")
	}
	parentKind := batchNodeType
	if _, kind, err := parseID(member.parent); err == nil {
		mapping, _ := findType(kind)
		parentKind = mapping.NativeType
	}
	kind, _ := findType(member.kind)
	if reason := protectionReason(kind, member.raw); reason != "" && !serviceIntrinsicChild(parentKind, member.kind, reason) {
		return serviceDenied(reason)
	}
	return nil
}

func batchManagedNodes(assets []asset.Asset, contribution governance.Contribution) map[asset.AssetID]asset.Asset {
	values := map[asset.AssetID]asset.Asset{}
	parents := map[asset.AssetID]asset.AssetID{}
	for _, value := range assets {
		values[value.ID] = value
	}
	for _, binding := range contribution.Bindings {
		parents[binding.ManagedAssetID] = binding.ControllerAssetID
	}
	result := map[asset.AssetID]asset.Asset{}
	for _, value := range assets {
		if !batchVMKind(value.Identity.NativeType) {
			continue
		}
		seen := map[asset.AssetID]bool{}
		for parent := parents[value.ID]; parent != "" && !seen[parent]; parent = parents[parent] {
			seen[parent] = true
			if values[parent].Identity.NativeType == batchNodeType {
				result[value.ID] = values[parent]
				break
			}
		}
	}
	return result
}

func (a *action) batchNodePrerequisiteAbsent(ctx context.Context, parent, node asset.Asset) error {
	vmID := batchNodeVM(node.Normalized)
	id, kind, err := parseID(vmID)
	if a.kind.NativeType != scaleSetType || err != nil || !strings.EqualFold(kind, scaleSetVMType) || !strings.EqualFold(redisParentID(id), parent.Identity.NativeID) {
		return serviceDenied("invalid_batch_node_prerequisite")
	}
	account, err := a.client.batchAccount(ctx, text(node.Normalized["_batch_account"]))
	if err != nil {
		return err
	}
	if err := batchAssetAccount(a.client, node, account); err != nil {
		return err
	}
	resources := object(node.Normalized["_batch_vm_resources"])
	if text(object(resources[id])["kind"]) != scaleSetVMType {
		return serviceDenied("invalid_batch_vm_binding")
	}
	return a.client.batchNodeAbsent(ctx, account, node)
}

func (c *client) batchNodeAbsent(ctx context.Context, account batchAccountContext, node asset.Asset) error {
	resources := object(node.Normalized["_batch_vm_resources"])
	if resources == nil || text(node.Normalized["_batch_vm_binding"]) != c.batchNodeBinding(node.Normalized, resources) {
		return serviceDenied("invalid_batch_vm_binding")
	}
	if _, err := c.batchRead(ctx, account, node.Identity.NativeID, batchNodeType); !isNotFound(err) {
		if err != nil {
			return err
		}
		return serviceDenied("batch_node_prerequisite_still_exists")
	}
	for member := range resources {
		if _, err := c.linkedResource(ctx, member); !isNotFound(err) {
			if err != nil {
				return err
			}
			return serviceDenied("batch_vm_prerequisite_still_exists")
		}
	}
	return nil
}
