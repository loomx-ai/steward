package azure

import (
	"context"
	"maps"
	"slices"
	"strings"

	"github.com/loomx-ai/steward/internal/core/asset"
)

func azureLocalStorageResource(kind string) bool {
	return kind == azureLocalDiskType || azureLocalImage(kind)
}

// Native placement is optional in these GET contracts. Without a returned path
// ID, absence of a reference cannot prove that the resource is on another path.
// Treat it as a possible consumer, requiring explicit deletion or reconciliation;
// this is a dependency, never ownership or permission to automatically select it.
func azureLocalRootReference(refs map[string][]string, kind, id string) bool {
	return slices.Contains(refs[kind], id) || kind == azureLocalStorageType && len(refs[kind]) == 0
}

func (c *client) azureLocalRootConsumerRecord(value asset.Asset, target string) error {
	if value.Identity.NativeType == azureLocalVMType {
		return c.azureLocalVMRecord(value)
	}
	if target == azureLocalStorageType && azureLocalStorageResource(value.Identity.NativeType) || target == azureLocalNetworkType && (value.Identity.NativeType == azureLocalNICType || value.Identity.NativeType == azureLocalNetworkType) {
		return c.azureLocalRootRecord(value)
	}
	return serviceDenied("invalid_azure_local_root_consumer")
}

func (c *client) azureLocalStorageHistory(value any) error {
	ids := stringValues(value)
	if c.privateConfiguration(map[string]any{"ids": ids}) != c.privateConfiguration(map[string]any{"ids": value}) {
		return serviceDenied("invalid_azure_local_storage_history")
	}
	seen := map[string]bool{}
	for _, id := range ids {
		_, typ, err := parseID(id)
		kind := azureLocalKind(typ)
		canonical, identityErr := c.azureLocalIdentity(id, kind)
		if err != nil || identityErr != nil || canonical != id || !azureLocalStorageResource(kind) || seen[id] {
			return serviceDenied("invalid_azure_local_storage_history")
		}
		seen[id] = true
	}
	return nil
}

// Discover new roots and reread known IDs independently of indexes. A surviving
// disk/image omitted by an index must still prevent deleting its storage path.
func (c *client) azureLocalStorageResources(ctx context.Context, known []string) (map[string]map[string]any, error) {
	if err := c.azureLocalStorageHistory(known); err != nil {
		return nil, err
	}
	resources := map[string]map[string]any{}
	for _, kind := range []string{azureLocalDiskType, azureLocalImageType, azureLocalMarketplaceType} {
		rows, err := c.unfilteredARMIndex(ctx, c.root()+"/providers/"+kind, azureLocalVersion)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			raw := object(row)
			id, err := c.azureLocalIdentity(text(raw["id"]), kind)
			if err != nil || resources[id] != nil || !strings.EqualFold(text(raw["type"]), kind) {
				return nil, serviceDenied("invalid_azure_local_storage_resource_index")
			}
			live, err := c.azureLocalRead(ctx, id, kind)
			if err != nil {
				return nil, err
			}
			if serviceListedIncarnation(raw, live.data) != nil || !nativeConfigurationContains(raw, live.data) {
				return nil, serviceDenied("azure_local_storage_resource_index_changed")
			}
			resources[id] = live.data
		}
	}
	for _, id := range known {
		if resources[id] != nil {
			continue
		}
		_, typ, _ := parseID(id)
		live, err := c.azureLocalRead(ctx, id, azureLocalKind(typ))
		if isNotFound(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		resources[id] = live.data
	}
	for id, raw := range resources {
		if _, err := azureLocalReferences(id, azureLocalKind(text(raw["type"])), raw); err != nil {
			return nil, err
		}
	}
	return resources, nil
}

func (c *client) azureLocalStorageConsumers(ctx context.Context, id string, known any) ([]string, error) {
	if err := c.azureLocalStorageHistory(known); err != nil {
		return nil, err
	}
	resources, err := c.azureLocalStorageResources(ctx, stringValues(known))
	if err != nil {
		return nil, err
	}
	consumers := []string{}
	for _, candidate := range slices.Sorted(maps.Keys(resources)) {
		refs, err := azureLocalReferences(candidate, azureLocalKind(text(resources[candidate]["type"])), resources[candidate])
		if err != nil {
			return nil, err
		}
		if azureLocalRootReference(refs, azureLocalStorageType, id) {
			consumers = append(consumers, candidate)
		}
	}
	return consumers, nil
}
