package azure

import (
	"context"
	"maps"
	"slices"
	"strings"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func (c *client) dataMigrationRecordedReferences(value asset.Asset) (map[string]any, error) {
	if err := c.dataMigrationAsset(value); err != nil {
		return nil, err
	}
	return object(value.Normalized["_datamigration_references"]), nil
}

func dataMigrationReferenceMatches(target asset.Asset, refs map[string]any) bool {
	for _, ids := range refs {
		for _, id := range stringValues(ids) {
			if id == target.Identity.NativeID || strings.HasPrefix(id, target.Identity.NativeID+"/") {
				return true
			}
		}
	}
	return false
}

// A forward graph edge alone cannot protect a target from an unindexed or newly
// created migration. Reuse the native family walks, including each known source
// and ancestor GET, before independently deleting a referenced resource.
func (c *client) dataMigrationIncomingObservation(ctx context.Context, targets, known []asset.Asset) (map[string][]monitorIncomingSource, error) {
	incoming := map[string][]monitorIncomingSource{}
	selected := []asset.Asset{}
	for _, target := range targets {
		if dataMigrationKind(target.Identity.NativeType) != "" {
			continue // Native DMS actions verify their own children and consumers.
		}
		if !monitorARMTarget(target) || !strings.HasPrefix(target.Identity.NativeID, c.root()+"/") {
			return nil, serviceDenied("invalid_datamigration_dependency_target")
		}
		selected = append(selected, target)
	}
	if len(selected) == 0 {
		return incoming, nil
	}
	metadata, err := providerData()
	if err != nil {
		return nil, err
	}
	runtime := &Runtime{bundle: metadata.bundle}
	for _, classic := range []bool{true, false} {
		hints := map[string]dataMigrationMember{}
		seen := map[string]asset.AssetID{}
		for _, value := range known {
			if value.Identity.Provider != asset.ProviderAzure || dataMigrationKind(value.Identity.NativeType) == "" || dataMigrationClassic(value.Identity.NativeType) != classic {
				continue
			}
			if err := c.dataMigrationAsset(value); err != nil {
				return nil, err
			}
			id := value.Identity.NativeID
			if previous := seen[id]; previous != "" && previous != value.ID {
				return nil, serviceDenied("ambiguous_datamigration_dependency_source")
			}
			seen[id] = value.ID
			kind := runtime.resourceKind(value.Identity.NativeType)
			current, err := c.dataMigrationKnown(contracts.InventoryRequest{ConnectionID: value.Identity.ConnectionID, ResourceKind: &kind, KnownNativeIDs: []string{id}, KnownNativeMetadata: map[string]map[string]any{id: value.Normalized}})
			if err != nil {
				return nil, err
			}
			maps.Copy(hints, current)
		}
		var forest dataMigrationForest
		if classic {
			forest, err = c.dataMigrationClassicForest(ctx, hints)
		} else {
			forest, err = c.dataMigrationModernForest(ctx, hints)
		}
		if err != nil {
			return nil, err
		}
		// Graph targets may include unresolved resources from other connections
		// or legacy partitions. Bind native source context to its own connection
		// only when matching an authenticated indexed source below.
		items, _, err := runtime.dataMigrationItems(ctx, c, "", forest)
		if err != nil {
			return nil, err
		}
		for _, id := range slices.Sorted(maps.Keys(items)) {
			member, item := forest.members[id], items[id]
			for _, target := range selected {
				if dataMigrationReferenceMatches(target, object(item.Normalized["_datamigration_references"])) {
					incoming[target.Identity.NativeID] = append(incoming[target.Identity.NativeID], monitorIncomingSource{resource: serviceChild{id: id, kind: member.kind, data: member.raw}, references: member.refs, group: item.Normalized})
				}
			}
		}
	}
	return incoming, nil
}

func (c *client) dataMigrationIncomingUnchanged(value asset.Asset, source monitorIncomingSource) error {
	if err := c.dataMigrationAsset(value); err != nil {
		return err
	}
	current := maps.Clone(source.group)
	current["_datamigration_connection"] = string(value.Identity.ConnectionID)
	if value.Normalized[dataMigrationProof] != c.dataMigrationBinding(value.Identity.NativeID, value.Identity.NativeType, current) {
		return serviceDenied("datamigration_dependency_context_changed")
	}
	return nil
}
