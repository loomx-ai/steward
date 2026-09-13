package azure

import (
	"context"
	"maps"
	"slices"
	"strings"

	"github.com/loomx-ai/steward/internal/core/asset"
)

func azureLocalOSDisk(raw map[string]any) string {
	return strings.ToLower(text(object(object(object(raw["properties"])["storageProfile"])["osDisk"])["id"]))
}

func (c *client) azureLocalRootBinding(id string, connection asset.ConnectionID, state map[string]any) string {
	protocol := "azure-local-root-1"
	if strings.Contains(id, "/virtualharddisks/") {
		protocol = "azure-local-disk-1"
	}
	return c.privateConfiguration(map[string]any{"protocol": protocol, "id": id, "connection": connection, "state": state})
}

func azureLocalRootConfiguration(configuration string) bool {
	return strings.HasPrefix(configuration, azureLocalRootPrefix) || strings.HasPrefix(configuration, azureLocalNetworkConfiguration+azureLocalRootPrefix)
}

func (c *client) azureLocalRootRecord(value asset.Asset) error {
	if _, err := c.azureLocalRecordedReferences(value); err != nil {
		return err
	}
	state := object(value.Normalized[azureLocalCleanup])
	_, protected := state["protected"].(bool)
	if value.ID == "" || !azureLocalIndependent(value.Identity.NativeType) || (len(state) != 5 && len(state) != 6) || !protected || value.Location == "" || value.Location != state["location"] || text(state["resource"]) == "" || text(state["etag"]) == "" || state["inventory"] != value.Normalized["_azure_local_configuration"] || value.Normalized["cleanup_protected"] != state["protected"] || value.Normalized[azureLocalCleanupProof] != c.azureLocalRootBinding(value.Identity.NativeID, value.Identity.ConnectionID, state) {
		return serviceDenied("invalid_azure_local_disk_record")
	}
	if len(state) == 5 {
		if value.Identity.NativeType != azureLocalDiskType || azureLocalRootConfiguration(text(state["inventory"])) {
			return serviceDenied("azure_local_root_history_missing")
		}
	} else {
		if !azureLocalRootConfiguration(text(state["inventory"])) {
			return serviceDenied("invalid_azure_local_root_inventory")
		}
		vms := stringValues(state["vms"])
		if azureLocalImage(value.Identity.NativeType) && len(vms) != 0 {
			return serviceDenied("azure_local_image_has_no_vm_consumers")
		}
		if c.privateConfiguration(map[string]any{"vms": vms}) != c.privateConfiguration(map[string]any{"vms": state["vms"]}) {
			return serviceDenied("invalid_azure_local_root_history")
		}
		seen := map[string]bool{}
		for _, vm := range vms {
			canonical, err := c.azureLocalIdentity(vm, azureLocalVMType)
			if err != nil || canonical != vm || seen[vm] {
				return serviceDenied("invalid_azure_local_root_history")
			}
			seen[vm] = true
		}
	}
	return nil
}

// A VM's OS-disk reference can authorize a cascade only when another native VM
// does not also use that disk. Recover previously observed VMs even if the Arc
// parent index omits them, and discover new parents before each mutation.
func (c *client) azureLocalDiskConsumers(ctx context.Context, disk string, known []string) ([]string, error) {
	return c.azureLocalVMConsumers(ctx, disk, azureLocalDiskType, known)
}

func azureLocalImage(kind string) bool {
	return kind == azureLocalImageType || kind == azureLocalMarketplaceType
}

func azureLocalIndependent(kind string) bool {
	return kind == azureLocalDiskType || kind == azureLocalNICType || azureLocalImage(kind)
}

func (c *client) azureLocalVMConsumers(ctx context.Context, disk, kind string, known []string) ([]string, error) {
	if disk == "" {
		return nil, nil
	}
	if canonical, err := c.azureLocalIdentity(disk, kind); (kind != azureLocalDiskType && kind != azureLocalNICType) || err != nil || canonical != disk {
		return nil, serviceDenied("invalid_azure_local_os_disk")
	}
	ids := map[string]bool{}
	for _, id := range known {
		if canonical, err := c.azureLocalIdentity(id, azureLocalVMType); err != nil || canonical != id || ids[id] {
			return nil, serviceDenied("invalid_azure_local_disk_consumer_hint")
		}
		ids[id] = true
	}
	rows, err := c.hybridComputeIndex(ctx, hybridMachineType, "")
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, value := range rows {
		raw := object(value)
		id, err := c.hybridComputeIdentity(text(raw["id"]), hybridMachineType)
		if err != nil || seen[id] || !strings.EqualFold(text(raw["type"]), hybridMachineType) {
			return nil, serviceDenied("invalid_azure_local_disk_consumer_index")
		}
		seen[id] = true
		live, err := c.hybridComputeRead(ctx, id, hybridMachineType)
		if err != nil {
			return nil, err
		}
		if serviceListedIncarnation(raw, live.data) != nil {
			return nil, serviceDenied("azure_local_disk_consumer_parent_changed")
		}
		ids[id+"/providers/microsoft.azurestackhci/virtualmachineinstances/default"] = true
	}
	consumers := []string{}
	for _, id := range slices.Sorted(maps.Keys(ids)) {
		res, err := c.azureLocalRead(ctx, id, azureLocalVMType)
		if isNotFound(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		refs, err := azureLocalReferences(id, azureLocalVMType, res.data)
		if err != nil {
			return nil, err
		}
		if slices.Contains(refs[kind], disk) {
			consumers = append(consumers, id)
		}
	}
	return consumers, nil
}
