package azure

import (
	"context"
	"maps"
	"slices"
	"strings"

	"github.com/loomx-ai/steward/internal/core/asset"
)

const azureLocalSource = "azure-local"
const azureLocalVersion = "2024-01-01"
const azureLocalVMType = "Microsoft.AzureStackHCI/virtualMachineInstances"
const azureLocalAgentType = azureLocalVMType + "/guestAgents"
const azureLocalIdentityType = azureLocalVMType + "/hybridIdentityMetadata"
const azureLocalNICType = "Microsoft.AzureStackHCI/networkInterfaces"
const azureLocalDiskType = "Microsoft.AzureStackHCI/virtualHardDisks"
const azureLocalNetworkType = "Microsoft.AzureStackHCI/logicalNetworks"
const azureLocalStorageType = "Microsoft.AzureStackHCI/storageContainers"
const azureLocalImageType = "Microsoft.AzureStackHCI/galleryImages"
const azureLocalMarketplaceType = "Microsoft.AzureStackHCI/marketplaceGalleryImages"
const azureLocalLocationType = "Microsoft.ExtendedLocation/customLocations"

func azureLocalKind(value string) string {
	for _, kind := range []string{azureLocalVMType, azureLocalAgentType, azureLocalIdentityType, azureLocalNICType, azureLocalDiskType, azureLocalNetworkType, azureLocalStorageType, azureLocalImageType, azureLocalMarketplaceType} {
		if strings.EqualFold(value, kind) {
			return kind
		}
	}
	return ""
}

// Only canonical resource IDs reach these helpers. Azure Local's VM and guest
// resources have fixed singleton names and extend a direct HybridCompute machine.
func azureLocalMachine(id string) string {
	if i := strings.Index(id, "/providers/microsoft.azurestackhci/virtualmachineinstances/"); i >= 0 {
		return id[:i]
	}
	return ""
}

func azureLocalParent(id, kind string) string {
	if kind == azureLocalVMType {
		return azureLocalMachine(id)
	}
	if kind == azureLocalAgentType || kind == azureLocalIdentityType {
		return id[:strings.LastIndex(id[:strings.LastIndex(id, "/")], "/")]
	}
	return ""
}

func (c *client) azureLocalIdentity(wire, kind string) (string, error) {
	id, typ, err := parseID(wire)
	if err != nil || wire != strings.TrimSpace(wire) || kind == "" || azureLocalKind(typ) != kind || !strings.HasPrefix(id, c.root()+"/resourcegroups/") {
		return "", serviceDenied("invalid_azure_local_identity")
	}
	parts := strings.Split(id, "/")
	if kind == azureLocalVMType || kind == azureLocalAgentType || kind == azureLocalIdentityType {
		machine := azureLocalMachine(id)
		_, parentKind, err := parseID(machine)
		if err != nil || parentKind != strings.ToLower(hybridMachineType) || len(strings.Split(machine, "/")) != 9 || kind == azureLocalVMType && len(parts) != 13 || kind != azureLocalVMType && len(parts) != 15 || parts[12] != "default" || last(id) != "default" {
			return "", serviceDenied("invalid_azure_local_extension_identity")
		}
	} else if len(parts) != 9 {
		return "", serviceDenied("invalid_azure_local_root_identity")
	}
	return id, nil
}

func (c *client) azureLocalRead(ctx context.Context, id, kind string) (response, error) {
	canonical, err := c.azureLocalIdentity(id, kind)
	if err != nil || canonical != id {
		return response{}, serviceDenied("invalid_azure_local_read_identity")
	}
	mapping, ok := findType(kind)
	if !ok {
		return response{}, serviceDenied("azure_local_mapping_missing")
	}
	endpoint, err := c.resourceURL(mapping, id)
	if err != nil {
		return response{}, err
	}
	res, err := c.request(ctx, "GET", endpoint)
	if err != nil {
		return res, err
	}
	actual, identityErr := c.azureLocalIdentity(text(res.data["id"]), kind)
	if res.status != 200 || operationLocation(res.header) != "" || identityErr != nil || actual != id || !strings.EqualFold(text(res.data["type"]), kind) || !strings.EqualFold(text(res.data["name"]), last(id)) || object(res.data["properties"]) == nil {
		return res, serviceDenied("invalid_azure_local_native_response")
	}
	if azureLocalMachine(id) == "" && text(res.data["location"]) == "" {
		return res, serviceDenied("azure_local_location_missing")
	}
	for _, key := range []string{"etag", "eTag", "managedBy", "location"} {
		if v := res.data[key]; v != nil {
			if _, ok := v.(string); !ok {
				return res, serviceDenied("invalid_azure_local_metadata_type")
			}
		}
	}
	if v := res.data["tags"]; v != nil {
		tags, ok := v.(map[string]any)
		if !ok {
			return res, serviceDenied("invalid_azure_local_tags")
		}
		for _, v := range tags {
			if _, ok := v.(string); !ok {
				return res, serviceDenied("invalid_azure_local_tag_value")
			}
		}
	}
	if v := object(res.data["properties"])["provisioningState"]; v != nil {
		if _, ok := v.(string); !ok {
			return res, serviceDenied("invalid_azure_local_state")
		}
	}
	return res, nil
}

func azureLocalReferences(id, kind string, raw map[string]any) (map[string][]string, error) {
	refs := map[string][]string{}
	add := func(value any, allowed ...string) error {
		if value == nil || value == "" {
			return nil
		}
		wire, ok := value.(string)
		target, typ, err := parseID(wire)
		if !ok || err != nil || target == id || wire != strings.TrimSpace(wire) {
			return serviceDenied("invalid_azure_local_reference")
		}
		matched := len(allowed) == 0
		for _, candidate := range allowed {
			if strings.EqualFold(typ, candidate) {
				typ, matched = candidate, true
				break
			}
		}
		if !matched {
			return serviceDenied("wrong_azure_local_reference_type")
		}
		if localKind := azureLocalKind(typ); localKind != "" {
			// Cross-subscription references remain unresolved graph edges, but
			// still require the same native shape as local resource identities.
			scope := &client{subscription: strings.Split(target, "/")[2]}
			if _, err := scope.azureLocalIdentity(target, localKind); err != nil {
				return err
			}
		}
		if mapping, ok := findType(typ); ok {
			typ = mapping.NativeType
		}
		refs[typ] = append(refs[typ], target)
		return nil
	}
	if parent := azureLocalParent(id, kind); parent != "" {
		if err := add(parent); err != nil {
			return nil, err
		}
	}
	if err := add(raw["managedBy"]); err != nil {
		return nil, err
	}
	if raw["extendedLocation"] != nil {
		location := object(raw["extendedLocation"])
		if location == nil || location["type"] != "CustomLocation" || text(location["name"]) == "" {
			return nil, serviceDenied("invalid_azure_local_extended_location")
		}
		if err := add(location["name"], azureLocalLocationType); err != nil {
			return nil, err
		}
	}
	props := object(raw["properties"])
	if err := add(props["containerId"], azureLocalStorageType); err != nil {
		return nil, err
	}
	if kind == azureLocalVMType {
		for _, key := range []string{"storageProfile", "networkProfile"} {
			if props[key] != nil && object(props[key]) == nil {
				return nil, serviceDenied("invalid_azure_local_profile")
			}
		}
		storage := object(props["storageProfile"])
		for _, key := range []string{"imageReference", "osDisk"} {
			if storage[key] != nil && object(storage[key]) == nil {
				return nil, serviceDenied("invalid_azure_local_storage_reference")
			}
		}
		for _, ref := range []struct {
			value any
			types []string
		}{
			{storage["vmConfigStoragePathId"], []string{azureLocalStorageType}},
			{object(storage["imageReference"])["id"], []string{azureLocalImageType, azureLocalMarketplaceType}},
			{object(storage["osDisk"])["id"], []string{azureLocalDiskType}},
		} {
			if err := add(ref.value, ref.types...); err != nil {
				return nil, err
			}
		}
		for _, ref := range []struct {
			value any
			kind  string
		}{{storage["dataDisks"], azureLocalDiskType}, {object(props["networkProfile"])["networkInterfaces"], azureLocalNICType}} {
			if ref.value == nil {
				continue
			}
			rows, ok := ref.value.([]any)
			if !ok {
				return nil, serviceDenied("invalid_azure_local_reference_list")
			}
			for _, row := range rows {
				if text(object(row)["id"]) == "" {
					return nil, serviceDenied("missing_azure_local_reference_id")
				}
				if err := add(object(row)["id"], ref.kind); err != nil {
					return nil, err
				}
			}
		}
	}
	if kind == azureLocalNICType && props["ipConfigurations"] != nil {
		rows, ok := props["ipConfigurations"].([]any)
		if !ok {
			return nil, serviceDenied("invalid_azure_local_ip_configurations")
		}
		for _, row := range rows {
			config := object(object(row)["properties"])
			if config == nil {
				return nil, serviceDenied("invalid_azure_local_ip_configuration")
			}
			if subnet := config["subnet"]; subnet != nil {
				if text(object(subnet)["id"]) == "" {
					return nil, serviceDenied("missing_azure_local_network_id")
				}
				if err := add(object(subnet)["id"], azureLocalNetworkType); err != nil {
					return nil, err
				}
			}
		}
	}
	for kind, ids := range refs {
		slices.Sort(ids)
		refs[kind] = slices.Compact(ids)
	}
	return refs, nil
}

func (c *client) azureLocalRecordedReferences(value asset.Asset) (map[string][]string, error) {
	id, err := c.azureLocalIdentity(value.Identity.NativeID, azureLocalKind(value.Identity.NativeType))
	refs := map[string][]string{}
	for kind, ids := range object(value.Normalized["_azure_local_references"]) {
		refs[kind] = stringValues(ids)
	}
	expected := c.privateConfiguration(map[string]any{"id": id, "connection": value.Identity.ConnectionID, "configuration": value.Normalized["_azure_local_configuration"], "references": refs})
	if err != nil || id != value.Identity.NativeID || value.Identity.Provider != asset.ProviderAzure || value.Identity.ConnectionID == "" || value.Identity.Partition != "azure" || value.Normalized["_inventory_source"] != azureLocalSource || text(value.Normalized["_azure_local_configuration"]) == "" || value.Normalized["_azure_local_reference_binding"] != expected {
		return nil, serviceDenied("invalid_azure_local_recorded_references")
	}
	return maps.Clone(refs), nil
}
