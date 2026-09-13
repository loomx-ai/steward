package azure

import (
	"context"
	"slices"
	"strings"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

const azureLocalVMPrefix = "azure-local-vm:"

func azureLocalVMMember(id, kind, vm, osDisk string) bool {
	if kind == azureLocalDiskType {
		return id != "" && id == osDisk
	}
	if kind == azureLocalAgentType || kind == azureLocalIdentityType {
		return id == vm+"/"+strings.ToLower(last(kind))+"/default"
	}
	return hybridComputeChild(kind) && hybridComputeParent(id, kind) == azureLocalMachine(vm)
}

func (c *client) azureLocalVMRecorded(id string, connection asset.ConnectionID, normalized map[string]any) error {
	canonical, err := c.azureLocalIdentity(id, azureLocalVMType)
	state := object(normalized[azureLocalCleanup])
	_, protected := state["protected"].(bool)
	members, memberMap := state["members"].(map[string]any)
	location := text(state["location"])
	inventory := text(state["inventory"])
	if err != nil || canonical != id || connection == "" || len(state) != 9 || !protected || !memberMap || location == "" || location != strings.ToLower(location) || text(state["resource"]) == "" || text(state["machine"]) == "" || text(state["etag"]) == "" || inventory != text(normalized["_azure_local_configuration"]) || !strings.HasPrefix(inventory, azureLocalVMPrefix) || normalized["cleanup_protected"] != state["protected"] || normalized[azureLocalCleanupProof] != c.azureLocalVMBinding(id, connection, state) {
		return serviceDenied("invalid_azure_local_vm_record")
	}
	osDisk, diskString := state["os_disk"].(string)
	vms := stringValues(state["vms"])
	if !diskString || !slices.Contains(vms, id) {
		return serviceDenied("invalid_azure_local_vm_disk_context")
	}
	if osDisk != "" {
		if disk, err := c.azureLocalIdentity(osDisk, azureLocalDiskType); err != nil || disk != osDisk {
			return serviceDenied("invalid_azure_local_vm_os_disk")
		}
	}
	seen := map[string]bool{}
	for _, vm := range vms {
		if canonical, err := c.azureLocalIdentity(vm, azureLocalVMType); err != nil || canonical != vm || seen[vm] {
			return serviceDenied("invalid_azure_local_vm_disk_context")
		}
		seen[vm] = true
	}
	for childID, value := range members {
		entry := object(value)
		child, typ, err := parseID(childID)
		kind := azureLocalKind(typ)
		if kind == "" {
			kind = hybridComputeKind(typ)
		}
		if err != nil || child != childID || !azureLocalVMMember(childID, kind, id, osDisk) || len(entry) != 2 || entry["kind"] != kind || text(entry["configuration"]) == "" {
			return serviceDenied("invalid_azure_local_vm_member")
		}
	}
	return nil
}

func (c *client) azureLocalVMBinding(id string, connection asset.ConnectionID, state map[string]any) string {
	return c.privateConfiguration(map[string]any{"protocol": "azure-local-vm-1", "id": id, "connection": connection, "state": state})
}

func (c *client) azureLocalVMRecord(value asset.Asset) error {
	if _, err := c.azureLocalRecordedReferences(value); err != nil {
		return err
	}
	if value.ID == "" || value.Identity.NativeType != azureLocalVMType || value.Location != text(object(value.Normalized[azureLocalCleanup])["location"]) {
		return serviceDenied("invalid_azure_local_vm_asset")
	}
	return c.azureLocalVMRecorded(value.Identity.NativeID, value.Identity.ConnectionID, value.Normalized)
}

// Local guest resources have exactly one native default identity. Probe their
// own GET even after the VM disappears. Native Arc indexes find new siblings;
// reviewed IDs recover index omissions and remain readable after parent loss.
func (c *client) azureLocalVMChildren(ctx context.Context, vm string, known map[string]any, machinePresent bool, location, osDisk string) (map[string]map[string]any, error) {
	canonical, err := c.azureLocalIdentity(vm, azureLocalVMType)
	if err != nil || canonical != vm {
		return nil, serviceDenied("invalid_azure_local_vm_children_scope")
	}
	arc := map[string]any{}
	for id, value := range known {
		kind := text(object(value)["kind"])
		if !azureLocalVMMember(id, kind, vm, osDisk) {
			return nil, serviceDenied("invalid_azure_local_vm_child_hint")
		}
		if hybridComputeChild(kind) {
			arc[id] = value
		}
	}
	out, err := c.hybridComputeMachineChildren(ctx, azureLocalMachine(vm), arc, machinePresent)
	if err != nil {
		return nil, err
	}
	for _, kind := range []string{azureLocalAgentType, azureLocalIdentityType} {
		id := vm + "/" + strings.ToLower(last(kind)) + "/default"
		res, err := c.azureLocalRead(ctx, id, kind)
		if isNotFound(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		out[id] = res.data
	}
	if osDisk != "" {
		res, err := c.azureLocalRead(ctx, osDisk, azureLocalDiskType)
		if err != nil && !isNotFound(err) {
			return nil, err
		}
		if err == nil {
			out[osDisk] = res.data
		}
	}
	for _, raw := range out {
		if text(raw["location"]) != "" && resourceRegion(raw) != location {
			return nil, serviceDenied("azure_local_vm_child_location_changed")
		}
	}
	return out, nil
}

func (c *client) azureLocalVMMembers(values map[string]map[string]any) map[string]any {
	out := map[string]any{}
	for id, raw := range values {
		kind := azureLocalKind(text(raw["type"]))
		snapshot := azureLocalCleanupSnapshot(raw)
		if kind == "" {
			kind = hybridComputeKind(text(raw["type"]))
			snapshot = hybridComputeChildSnapshot(raw)
		}
		out[id] = map[string]any{"kind": kind, "configuration": c.privateConfiguration(snapshot)}
	}
	return out
}

func (a *azureLocalAction) vmRequest(request contracts.ActionRequest) error {
	state := object(request.Asset.Normalized[azureLocalCleanup])
	members := object(state["members"])
	seen, assets := map[string]bool{}, map[asset.AssetID]bool{a.planned.ID: true}
	for _, group := range [][]contracts.ActionImpact{request.LifecycleImpacts, request.PrerequisiteDeletions} {
		for _, impact := range group {
			child := impact.Asset
			kind := child.Identity.NativeType
			if !impact.Delete || impact.ControllerID != a.planned.ID || seen[child.Identity.NativeID] || assets[child.ID] || child.Identity.ConnectionID != request.Asset.Identity.ConnectionID || child.Identity.Partition != request.Asset.Identity.Partition || child.Location != request.Asset.Location || !azureLocalVMMember(child.Identity.NativeID, kind, a.planned.Identity.NativeID, text(state["os_disk"])) {
				return serviceDenied("invalid_azure_local_vm_impact")
			}
			if err := a.vmMemberAsset(child); err != nil {
				return err
			}
			if child.Normalized["cleanup_protected"] != false {
				return serviceDenied("azure_local_vm_child_protected")
			}
			seen[child.Identity.NativeID], assets[child.ID] = true, true
		}
	}
	for _, impact := range request.LifecycleImpacts {
		if impact.Asset.Identity.NativeType != azureLocalIdentityType && impact.Asset.Identity.NativeType != azureLocalDiskType {
			return serviceDenied("azure_local_vm_direct_child_delegated")
		}
	}
	for _, impact := range request.PrerequisiteDeletions {
		if impact.Asset.Identity.NativeType == azureLocalIdentityType || impact.Asset.Identity.NativeType == azureLocalDiskType {
			return serviceDenied("azure_local_identity_has_no_delete")
		}
	}
	for id, member := range members {
		if (object(member)["kind"] == azureLocalIdentityType || object(member)["kind"] == azureLocalDiskType) && !seen[id] {
			return serviceDenied("azure_local_vm_identity_review_missing")
		}
	}
	return nil
}

func (a *azureLocalAction) vmMemberAsset(child asset.Asset) error {
	state := object(a.planned.Normalized[azureLocalCleanup])
	members := object(state["members"])
	kind := child.Identity.NativeType
	if child.ID == "" || child.ID == a.planned.ID || child.Identity.ConnectionID != a.planned.Identity.ConnectionID || child.Identity.Partition != a.planned.Identity.Partition || child.Location != a.planned.Location || !azureLocalVMMember(child.Identity.NativeID, kind, a.planned.Identity.NativeID, text(state["os_disk"])) {
		return serviceDenied("invalid_azure_local_vm_child_asset")
	}
	var configuration any
	if hybridComputeChild(kind) {
		if err := a.client.hybridComputeCleanupRecord(child); err != nil {
			return err
		}
		if object(child.Normalized[hybridComputeCleanup])["parent"] != state["machine"] {
			return serviceDenied("azure_local_vm_arc_registration_changed")
		}
		configuration = object(child.Normalized[hybridComputeCleanup])["resource"]
	} else if kind == azureLocalDiskType {
		if err := a.client.azureLocalRootRecord(child); err != nil {
			return err
		}
		configuration = object(child.Normalized[azureLocalCleanup])["resource"]
	} else {
		if err := a.client.azureLocalChildRecord(child); err != nil {
			return err
		}
		childState := object(child.Normalized[azureLocalCleanup])
		if childState["vm"] != state["resource"] || childState["machine"] != state["machine"] {
			return serviceDenied("azure_local_vm_child_context_changed")
		}
		configuration = childState["resource"]
	}
	entry := object(members[child.Identity.NativeID])
	if entry["kind"] != kind || entry["configuration"] != configuration {
		return serviceDenied("azure_local_vm_impact_changed")
	}
	return nil
}

func (a *azureLocalAction) vmObserve(ctx context.Context) (vm, machine map[string]any, children map[string]map[string]any, err error) {
	id := a.planned.Identity.NativeID
	state := object(a.planned.Normalized[azureLocalCleanup])
	res, err := a.client.azureLocalRead(ctx, id, azureLocalVMType)
	if err != nil && !isNotFound(err) {
		return nil, nil, nil, err
	}
	if err == nil {
		vm = res.data
		if a.client.privateConfiguration(azureLocalCleanupSnapshot(vm)) != state["resource"] {
			return vm, nil, nil, serviceDenied("azure_local_vm_configuration_changed")
		}
	}
	res, err = a.client.hybridComputeRead(ctx, azureLocalMachine(id), hybridMachineType)
	if err != nil && !isNotFound(err) {
		return vm, nil, nil, err
	}
	if err == nil {
		machine = res.data
		if a.client.privateConfiguration(hybridComputeParentStamp(machine)) != state["machine"] || resourceRegion(machine) != a.planned.Location {
			return vm, machine, nil, serviceDenied("azure_local_machine_registration_changed")
		}
	}
	children, err = a.client.azureLocalVMChildren(ctx, id, object(state["members"]), machine != nil, a.planned.Location, text(state["os_disk"]))
	if err != nil {
		return vm, machine, nil, err
	}
	members := a.client.azureLocalVMMembers(children)
	for child, member := range members {
		expected := object(object(state["members"])[child])
		if expected == nil || object(member)["configuration"] != expected["configuration"] {
			return vm, machine, children, serviceDenied("azure_local_vm_children_changed")
		}
	}
	consumers, err := a.client.azureLocalDiskConsumers(ctx, text(state["os_disk"]), stringValues(state["vms"]))
	if err != nil {
		return vm, machine, children, err
	}
	for _, consumer := range consumers {
		if consumer != id {
			return vm, machine, children, serviceDenied("azure_local_os_disk_has_other_consumers")
		}
	}
	return vm, machine, children, nil
}

func (a *azureLocalAction) vmPreflight(ctx context.Context, request contracts.ActionRequest) (contracts.PreflightResult, error) {
	state := object(request.Asset.Normalized[azureLocalCleanup])
	for range 2 {
		vm, machine, children, err := a.vmObserve(ctx)
		if err != nil {
			return contracts.PreflightResult{}, err
		}
		if vm == nil || machine == nil || (strings.EqualFold(text(object(vm["properties"])["provisioningState"]), "Deleting") || strings.EqualFold(text(object(machine["properties"])["provisioningState"]), "Deleting")) {
			return contracts.PreflightResult{Allowed: true, Absent: vm == nil && len(children) == 0, Evidence: map[string]any{"azure_local_wait": true}}, nil
		}
		for _, child := range children {
			if !strings.EqualFold(text(child["type"]), azureLocalIdentityType) && !strings.EqualFold(text(child["type"]), azureLocalDiskType) {
				return contracts.PreflightResult{}, serviceDenied("azure_local_vm_prerequisite_still_exists")
			}
			if reason := protectionReason(resourceType{NativeType: text(child["type"])}, child); reason != "" {
				return contracts.PreflightResult{}, serviceDenied(reason)
			}
		}
		for _, impact := range request.LifecycleImpacts {
			if raw := children[impact.Asset.Identity.NativeID]; raw != nil && a.client.privateConfiguration(map[string]any{"etag": raw["etag"], "eTag": raw["eTag"]}) != object(impact.Asset.Normalized[azureLocalCleanup])["etag"] {
				return contracts.PreflightResult{}, serviceDenied("azure_local_vm_managed_etag_changed")
			}
		}
		if len(request.PrerequisiteDeletions) == 0 && a.client.privateConfiguration(map[string]any{"etag": vm["etag"], "eTag": vm["eTag"]}) != state["etag"] {
			return contracts.PreflightResult{}, serviceDenied("azure_local_vm_etag_changed")
		}
		if err := a.protection(ctx, vm, vm, machine, children); err != nil {
			return contracts.PreflightResult{}, err
		}
	}
	return contracts.PreflightResult{Allowed: true}, nil
}

func (a *azureLocalAction) vmReadback(ctx context.Context) (contracts.ReadbackResult, error) {
	vm, _, children, err := a.vmObserve(ctx)
	return contracts.ReadbackResult{Exists: vm != nil || len(children) != 0, State: "azure_local_vm_deleting"}, contracts.DependencyReadError(err)
}
