package azure

import (
	"context"
	"maps"
	"slices"
	"strings"

	"github.com/loomx-ai/steward/internal/app/governance"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
)

// A Local VM is an extension of its Arc registration. Keep this context after
// its own GET becomes 404 so a later scan can finish the native CLI sequence.
// A bare HCI host without previously observed VM evidence remains protected.
func (c *client) azureLocalRegistrationInventory(ctx context.Context, raw, prior map[string]any) (map[string]any, error) {
	if !strings.EqualFold(text(raw["kind"]), "HCI") {
		return nil, nil
	}
	id := strings.ToLower(text(raw["id"])) + "/providers/microsoft.azurestackhci/virtualmachineinstances/default"
	res, err := c.azureLocalRead(ctx, id, azureLocalVMType)
	if isNotFound(err) {
		if prior["registration"] != c.privateConfiguration(hybridComputeParentStamp(raw)) {
			return nil, nil
		}
		local := object(prior["local_vm"])
		if _, err := c.azureLocalRegistrationObserve(ctx, local, resourceRegion(raw)); err != nil {
			return nil, err
		}
		return local, nil
	}
	if err != nil {
		return nil, err
	}
	if text(res.data["location"]) != "" && resourceRegion(res.data) != resourceRegion(raw) {
		return nil, serviceDenied("azure_local_registration_location_changed")
	}
	disk := azureLocalOSDisk(res.data)
	children, err := c.azureLocalVMChildren(ctx, id, nil, true, resourceRegion(raw), disk)
	if err != nil {
		return nil, err
	}
	for child, value := range children {
		if hybridComputeChild(hybridComputeKind(text(value["type"]))) {
			delete(children, child)
		}
	}
	return map[string]any{"id": id, "resource": c.privateConfiguration(azureLocalCleanupSnapshot(res.data)), "os_disk": disk, "members": c.azureLocalVMMembers(children)}, nil
}

func (c *client) azureLocalRegistrationRecorded(machine string, state map[string]any) error {
	local, present := state["local_vm"]
	if !present {
		return nil
	}
	record := object(local)
	id := text(record["id"])
	canonical, err := c.azureLocalIdentity(id, azureLocalVMType)
	members, valid := record["members"].(map[string]any)
	disk, diskString := record["os_disk"].(string)
	if err != nil || id != canonical || azureLocalMachine(id) != machine || len(record) != 4 || !valid || !diskString || text(record["resource"]) == "" {
		return serviceDenied("invalid_azure_local_registration_record")
	}
	if disk != "" {
		if canonical, err := c.azureLocalIdentity(disk, azureLocalDiskType); err != nil || canonical != disk {
			return serviceDenied("invalid_azure_local_registration_disk")
		}
	}
	for child, value := range members {
		member := object(value)
		kind := text(member["kind"])
		if (kind != azureLocalAgentType && kind != azureLocalIdentityType && kind != azureLocalDiskType) || !azureLocalVMMember(child, kind, id, disk) || len(member) != 2 || text(member["configuration"]) == "" {
			return serviceDenied("invalid_azure_local_registration_member")
		}
	}
	return nil
}

func (c *client) azureLocalRegistrationObserve(ctx context.Context, local map[string]any, location string) (map[string]map[string]any, error) {
	out := map[string]map[string]any{}
	if local == nil {
		return out, nil
	}
	id := text(local["id"])
	ids := map[string]string{id: azureLocalVMType, id + "/guestagents/default": azureLocalAgentType, id + "/hybrididentitymetadata/default": azureLocalIdentityType}
	if disk := text(local["os_disk"]); disk != "" {
		ids[disk] = azureLocalDiskType
	}
	for _, target := range slices.Sorted(maps.Keys(ids)) {
		kind := ids[target]
		res, err := c.azureLocalRead(ctx, target, kind)
		if isNotFound(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		expected := object(object(local["members"])[target])["configuration"]
		if kind == azureLocalVMType {
			expected = local["resource"]
		}
		if (text(res.data["location"]) != "" && resourceRegion(res.data) != location) || expected != c.privateConfiguration(azureLocalCleanupSnapshot(res.data)) {
			return nil, serviceDenied("azure_local_registration_dependency_changed")
		}
		out[target] = res.data
	}
	return out, nil
}

func (c *client) azureLocalRegistrationVM(parent, vm asset.Asset) error {
	if err := c.azureLocalVMRecord(vm); err != nil {
		return err
	}
	state := object(parent.Normalized[hybridComputeCleanup])
	local, inner := object(state["local_vm"]), object(vm.Normalized[azureLocalCleanup])
	if local == nil || vm.ID == parent.ID || vm.Identity.NativeID != local["id"] || vm.Identity.ConnectionID != parent.Identity.ConnectionID || vm.Identity.Partition != parent.Identity.Partition || vm.Location != parent.Location || inner["resource"] != local["resource"] || inner["machine"] != state["registration"] || inner["os_disk"] != local["os_disk"] {
		return serviceDenied("azure_local_registration_vm_context_changed")
	}
	for id, member := range object(local["members"]) {
		if c.privateConfiguration(object(member)) != c.privateConfiguration(object(object(inner["members"])[id])) {
			return serviceDenied("azure_local_registration_vm_children_changed")
		}
	}
	return nil
}

func (s *serviceCascades) contributeAzureLocalRegistration(ctx context.Context, parent asset.Asset, values []asset.Asset, result *governance.Contribution) error {
	local := object(object(parent.Normalized[hybridComputeCleanup])["local_vm"])
	if local == nil {
		return nil
	}
	var selected asset.Asset
	for _, value := range values {
		if value.Identity.Provider != asset.ProviderAzure || value.Identity.NativeID != local["id"] {
			continue
		}
		if selected.ID != "" {
			return serviceDenied("ambiguous_azure_local_registration_vm")
		}
		if err := s.client.azureLocalRegistrationVM(parent, value); err != nil {
			return err
		}
		selected = value
	}
	before := ""
	var children map[string]map[string]any
	for range 2 {
		var err error
		children, err = s.client.azureLocalRegistrationObserve(ctx, local, parent.Location)
		if err != nil {
			return err
		}
		current := s.client.privateConfiguration(map[string]any{"children": children})
		if before != "" && before != current {
			return serviceDenied("azure_local_registration_graph_changed")
		}
		before = current
	}
	evidence := map[string]any{"resource_type": azureLocalVMType, "instance_id": local["id"], "delete_by_default": true, "retention_supported": false}
	if selected.ID == "" {
		if len(children) != 0 {
			result.Unresolved = append(result.Unresolved, graph.UnresolvedReference{BlocksCleanup: true, Provider: parent.Identity.Provider, ConnectionID: parent.Identity.ConnectionID, NativeType: azureLocalVMType, NativeID: text(local["id"]), ControllerID: parent.ID, Relationship: graph.RelationshipAttachedTo, Evidence: evidence})
		}
		return nil
	}
	result.Bindings = append(result.Bindings, graph.LifecycleBinding{ControllerAssetID: parent.ID, ManagedAssetID: selected.ID, Authority: graph.AuthorityAuthoritative, Ownership: graph.OwnershipExclusive, CleanupPolicy: graph.CleanupDirect, DirectCleanupAllowed: selected.Normalized["cleanup_protected"] == false, EvidenceSource: serviceCascadeSource, Evidence: evidence, Confidence: 1})
	return nil
}

func hybridComputeRegistrationProtection(raw, state map[string]any) string {
	if object(state["local_vm"]) != nil && strings.EqualFold(text(raw["kind"]), "HCI") {
		return hybridComputeChildProtection(raw, raw)
	}
	return hybridComputeMachineProtection(raw)
}
