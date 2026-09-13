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

func (c *client) azureLocalDiskBinding(id string, connection asset.ConnectionID, state map[string]any) string {
	return c.privateConfiguration(map[string]any{"protocol": "azure-local-disk-1", "id": id, "connection": connection, "state": state})
}

func (c *client) azureLocalDiskRecord(value asset.Asset) error {
	if _, err := c.azureLocalRecordedReferences(value); err != nil {
		return err
	}
	state := object(value.Normalized[azureLocalCleanup])
	_, protected := state["protected"].(bool)
	if value.ID == "" || value.Identity.NativeType != azureLocalDiskType || len(state) != 5 || !protected || value.Location == "" || value.Location != state["location"] || text(state["resource"]) == "" || text(state["etag"]) == "" || state["inventory"] != value.Normalized["_azure_local_configuration"] || value.Normalized["cleanup_protected"] != state["protected"] || value.Normalized[azureLocalCleanupProof] != c.azureLocalDiskBinding(value.Identity.NativeID, value.Identity.ConnectionID, state) {
		return serviceDenied("invalid_azure_local_disk_record")
	}
	return nil
}

// A VM's OS-disk reference can authorize a cascade only when another native VM
// does not also use that disk. Recover previously observed VMs even if the Arc
// parent index omits them, and discover new parents before each mutation.
func (c *client) azureLocalDiskConsumers(ctx context.Context, disk string, known []string) ([]string, error) {
	if disk == "" {
		return nil, nil
	}
	if canonical, err := c.azureLocalIdentity(disk, azureLocalDiskType); err != nil || canonical != disk {
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
		if slices.Contains(refs[azureLocalDiskType], disk) {
			consumers = append(consumers, id)
		}
	}
	return consumers, nil
}
