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
		// Rows are checked in order before any read. Rows past the first
		// invalid one are not read, and its error follows the earlier reads.
		var listed []map[string]any
		var ids []string
		var invalid error
		queued := map[string]bool{}
		for _, row := range rows {
			raw := object(row)
			id, err := c.azureLocalIdentity(text(raw["id"]), kind)
			if err != nil || resources[id] != nil || queued[id] || !strings.EqualFold(text(raw["type"]), kind) {
				invalid = serviceDenied("invalid_azure_local_storage_resource_index")
				break
			}
			queued[id] = true
			listed, ids = append(listed, raw), append(ids, id)
		}
		lives, errs := readConcurrently(len(ids), func(i int) (response, error) { return c.azureLocalRead(ctx, ids[i], kind) })
		for i, raw := range listed {
			if errs[i] != nil {
				return nil, errs[i]
			}
			if serviceListedIncarnation(raw, lives[i].data) != nil || !nativeConfigurationContains(raw, lives[i].data) {
				return nil, serviceDenied("azure_local_storage_resource_index_changed")
			}
			resources[ids[i]] = lives[i].data
		}
		if invalid != nil {
			return nil, invalid
		}
	}
	var omitted []string
	for _, id := range known {
		if resources[id] == nil {
			omitted = append(omitted, id) // Known IDs are distinct (history check).
		}
	}
	lives, errs := readConcurrently(len(omitted), func(i int) (response, error) {
		_, typ, _ := parseID(omitted[i])
		return c.azureLocalRead(ctx, omitted[i], azureLocalKind(typ))
	})
	for i, id := range omitted {
		if isNotFound(errs[i]) {
			continue
		}
		if errs[i] != nil {
			return nil, errs[i]
		}
		resources[id] = lives[i].data
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
