package azure

import (
	"context"
	"maps"
	"slices"
	"strings"

	"github.com/loomx-ai/steward/internal/core/asset"
)

func azureLocalNetworkResource(kind string) bool {
	return kind == azureLocalNICType || kind == azureLocalNetworkType || kind == azureLocalAKSType
}

func azureLocalCustomLocation(refs map[string][]string) (string, error) {
	ids := refs[azureLocalLocationType]
	if len(ids) == 0 {
		return "", nil
	}
	if len(ids) != 1 {
		return "", serviceDenied("ambiguous_azure_local_custom_location")
	}
	id, kind, err := parseID(ids[0])
	if err != nil || id != ids[0] || kind != strings.ToLower(azureLocalLocationType) || len(strings.Split(id, "/")) != 9 {
		return "", serviceDenied("invalid_azure_local_custom_location")
	}
	return id, nil
}

func azureLocalNetworkProtection(raw map[string]any) string {
	if azureLocalNetworkRole(raw) == "Unknown" {
		return "azure_local_network_type_unverified"
	}
	refs, err := azureLocalReferences(text(raw["id"]), azureLocalNetworkType, raw)
	if err != nil {
		return "azure_local_network_location_unverified"
	}
	location, err := azureLocalCustomLocation(refs)
	if err != nil || location == "" {
		return "azure_local_network_location_unverified"
	}
	return protectionReason(resourceType{NativeType: azureLocalNetworkType}, raw)
}

func (c *client) azureLocalNetworkHistory(value any) error {
	ids := stringValues(value)
	if c.privateConfiguration(map[string]any{"ids": ids}) != c.privateConfiguration(map[string]any{"ids": value}) {
		return serviceDenied("invalid_azure_local_network_history")
	}
	seen := map[string]bool{}
	for _, id := range ids {
		_, typ, err := parseID(id)
		kind := azureLocalKind(typ)
		var canonical string
		if strings.EqualFold(typ, azureLocalAKSType) {
			kind = azureLocalAKSType
			canonical, err = c.azureLocalAKSIdentity(id, kind)
		} else {
			canonical, err = c.azureLocalIdentity(id, kind)
		}
		if err != nil || canonical != id || !azureLocalNetworkResource(kind) || seen[id] {
			return serviceDenied("invalid_azure_local_network_history")
		}
		seen[id] = true
	}
	return nil
}

func (c *client) azureLocalNetworkResources(ctx context.Context, known []string) (map[string]map[string]any, error) {
	if err := c.azureLocalNetworkHistory(known); err != nil {
		return nil, err
	}
	resources := map[string]map[string]any{}
	for _, kind := range []string{azureLocalNICType, azureLocalNetworkType} {
		mapping, _ := findType(kind)
		rows, err := c.unfilteredARMIndex(ctx, c.root()+"/providers/"+kind, mapping.Version)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			raw := object(row)
			id, e := c.azureLocalIdentity(text(raw["id"]), kind)
			if e != nil || resources[id] != nil || !strings.EqualFold(text(raw["type"]), kind) {
				return nil, serviceDenied("invalid_azure_local_network_resource_index")
			}
			live, e := c.azureLocalRead(ctx, id, kind)
			if e != nil {
				return nil, e
			}
			if serviceListedIncarnation(raw, live.data) != nil || !nativeConfigurationContains(raw, live.data) {
				return nil, serviceDenied("azure_local_network_resource_index_changed")
			}
			resources[id] = live.data
		}
	}
	aksKnown := []string{}
	for _, id := range known {
		_, typ, _ := parseID(id)
		if strings.EqualFold(typ, azureLocalAKSType) {
			aksKnown = append(aksKnown, id)
			continue
		}
		if resources[id] != nil {
			continue
		}
		live, err := c.azureLocalRead(ctx, id, azureLocalKind(typ))
		if isNotFound(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		resources[id] = live.data
	}
	aks, err := c.azureLocalAKSResources(ctx, aksKnown)
	if err != nil {
		return nil, err
	}
	maps.Copy(resources, aks)
	for id, raw := range resources {
		refs, err := azureLocalNetworkReferences(id, raw)
		if err != nil {
			return nil, err
		}
		if _, err = azureLocalCustomLocation(refs); err != nil {
			return nil, err
		}
	}
	return resources, nil
}

func azureLocalNetworkReferences(id string, raw map[string]any) (map[string][]string, error) {
	if strings.EqualFold(text(raw["type"]), azureLocalAKSType) {
		return azureLocalAKSReferences(raw)
	}
	return azureLocalReferences(id, azureLocalKind(text(raw["type"])), raw)
}

func azureLocalNetworkReference(refs map[string][]string, kind, id, role, location string) (bool, error) {
	if role == "Workload" {
		return (strings.EqualFold(kind, azureLocalNICType) || strings.EqualFold(kind, azureLocalAKSType)) && (slices.Contains(refs[azureLocalNetworkType], id) || len(refs[azureLocalNetworkType]) == 0), nil
	}
	if role != "Infrastructure" || location == "" {
		return false, serviceDenied("azure_local_network_scope_unverified")
	}
	candidate, err := azureLocalCustomLocation(refs)
	return candidate == "" || candidate == location, err
}

func (c *client) azureLocalRootConsumerReference(target, source asset.Asset) (bool, error) {
	refs, err := c.azureLocalRecordedReferences(source)
	if err != nil {
		return false, err
	}
	if target.Identity.NativeType != azureLocalNetworkType {
		return azureLocalRootReference(refs, target.Identity.NativeType, target.Identity.NativeID), nil
	}
	targetRefs, err := c.azureLocalRecordedReferences(target)
	if err != nil {
		return false, err
	}
	location, err := azureLocalCustomLocation(targetRefs)
	if err != nil {
		return false, err
	}
	return azureLocalNetworkReference(refs, source.Identity.NativeType, target.Identity.NativeID, text(object(target.Normalized[azureLocalCleanup])["network_type"]), location)
}

func (c *client) azureLocalNetworkConsumers(ctx context.Context, value asset.Asset, raw map[string]any) ([]string, error) {
	state := object(value.Normalized[azureLocalCleanup])
	refs, err := c.azureLocalRecordedReferences(value)
	if err != nil {
		return nil, err
	}
	location, err := azureLocalCustomLocation(refs)
	if err != nil || location == "" {
		return nil, serviceDenied("azure_local_network_location_unverified")
	}
	devices, err := azureLocalNetworkNICReferences(raw)
	if err != nil {
		return nil, err
	}
	known := append(slices.Clone(stringValues(state["resources"])), devices...)
	slices.Sort(known)
	known = slices.Compact(known)
	resources, err := c.azureLocalNetworkResources(ctx, known)
	if err != nil {
		return nil, err
	}
	role := text(state["network_type"])
	if role == "Infrastructure" {
		vms, e := c.azureLocalVMResources(ctx, stringValues(state["vms"]))
		if e != nil {
			return nil, e
		}
		maps.Copy(resources, vms)
	} else if role != "Workload" {
		return nil, serviceDenied("azure_local_network_type_unverified")
	}
	consumers := slices.Clone(devices)
	for _, id := range slices.Sorted(maps.Keys(resources)) {
		if id == value.Identity.NativeID {
			continue
		}
		raw := resources[id]
		refs, e := azureLocalNetworkReferences(id, raw)
		if e != nil {
			return nil, e
		}
		matched, e := azureLocalNetworkReference(refs, text(raw["type"]), value.Identity.NativeID, role, location)
		if e != nil {
			return nil, e
		}
		if matched {
			consumers = append(consumers, id)
		}
	}
	slices.Sort(consumers)
	return slices.Compact(consumers), nil
}

// The preview schema declares uppercase ID as an ARM network-interface ID.
// Read this reverse index independently of the NIC collection.
func azureLocalNetworkNICReferences(raw map[string]any) ([]string, error) {
	result := []string{}
	if raw == nil {
		return result, nil
	}
	value := object(raw["properties"])["subnets"]
	if value == nil {
		return result, nil
	}
	subnets, ok := value.([]any)
	if !ok {
		return nil, serviceDenied("invalid_azure_local_network_subnets")
	}
	for _, subnet := range subnets {
		props := object(object(subnet)["properties"])
		if props == nil {
			return nil, serviceDenied("invalid_azure_local_network_subnet")
		}
		value := props["ipConfigurationReferences"]
		if value == nil {
			continue
		}
		rows, ok := value.([]any)
		if !ok {
			return nil, serviceDenied("invalid_azure_local_network_devices")
		}
		for _, row := range rows {
			wire, ok := object(row)["ID"].(string)
			id, kind, err := parseID(wire)
			if !ok || err != nil || wire != strings.TrimSpace(wire) || kind != strings.ToLower(azureLocalNICType) || len(strings.Split(id, "/")) != 9 {
				return nil, serviceDenied("invalid_azure_local_network_device_id")
			}
			result = append(result, id)
		}
	}
	slices.Sort(result)
	return slices.Compact(result), nil
}
