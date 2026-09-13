package azure

import (
	"context"
	"maps"
	"slices"
	"strings"

	"github.com/loomx-ai/steward/internal/app/governance"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func (c *client) dataMigrationAsset(value asset.Asset) error {
	if value.ID == "" || value.Identity.Provider != asset.ProviderAzure || value.Identity.ConnectionID == "" || value.Normalized["_datamigration_connection"] != string(value.Identity.ConnectionID) || value.Identity.Partition != "azure" || value.Location != text(value.Normalized["_datamigration_location"]) {
		return serviceDenied("invalid_datamigration_asset_identity")
	}
	return c.dataMigrationRecorded(value.Identity.NativeID, value.Identity.NativeType, value.Normalized)
}

func (s *serviceCascades) dataMigrationGraphSnapshot(ctx context.Context, selected map[string]asset.Asset) (map[string]dataMigrationMember, map[string]contracts.InventoryItem, map[string]any, error) {
	metadata, err := providerData()
	if err != nil {
		return nil, nil, nil, err
	}
	runtime := &Runtime{bundle: metadata.bundle}
	members, items, bindings := map[string]dataMigrationMember{}, map[string]contracts.InventoryItem{}, map[string]any{}
	for _, classic := range []bool{true, false} {
		hints := map[string]dataMigrationMember{}
		for id, value := range selected {
			if dataMigrationClassic(value.Identity.NativeType) != classic {
				continue
			}
			kind := runtime.resourceKind(value.Identity.NativeType)
			known, err := s.client.dataMigrationKnown(contracts.InventoryRequest{ConnectionID: value.Identity.ConnectionID, ResourceKind: &kind, KnownNativeIDs: []string{id}, KnownNativeMetadata: map[string]map[string]any{id: value.Normalized}})
			if err != nil {
				return nil, nil, nil, err
			}
			maps.Copy(hints, known)
		}
		if len(hints) == 0 {
			continue
		}
		var forest dataMigrationForest
		if classic {
			forest, err = s.client.dataMigrationClassicForest(ctx, hints)
		} else {
			forest, err = s.client.dataMigrationModernForest(ctx, hints)
		}
		if err != nil {
			return nil, nil, nil, err
		}
		current, proofs, err := runtime.dataMigrationItems(ctx, s.client, s.connectionID, forest)
		if err != nil {
			return nil, nil, nil, err
		}
		maps.Copy(members, forest.members)
		maps.Copy(items, current)
		maps.Copy(bindings, proofs)
	}
	for id, value := range selected {
		if items[id].NativeType != value.Identity.NativeType || items[id].Normalized[dataMigrationProof] != value.Normalized[dataMigrationProof] {
			return nil, nil, nil, serviceDenied("datamigration_graph_inventory_changed")
		}
	}
	return members, items, bindings, nil
}

func (s *serviceCascades) contributeDataMigration(ctx context.Context, assets []asset.Asset, result *governance.Contribution) error {
	selected, seen := map[string]asset.Asset{}, map[asset.AssetID]bool{}
	for _, value := range assets {
		if value.Identity.Provider != asset.ProviderAzure || dataMigrationKind(value.Identity.NativeType) == "" {
			continue
		}
		if err := s.client.dataMigrationAsset(value); err != nil {
			return err
		}
		if value.Identity.ConnectionID != s.connectionID || selected[value.Identity.NativeID].ID != "" || seen[value.ID] {
			return serviceDenied("ambiguous_datamigration_graph_asset")
		}
		selected[value.Identity.NativeID], seen[value.ID] = value, true
	}
	if len(selected) == 0 {
		return nil
	}
	members, items, before, err := s.dataMigrationGraphSnapshot(ctx, selected)
	if err != nil {
		return err
	}
	_, _, after, err := s.dataMigrationGraphSnapshot(ctx, selected)
	if err != nil {
		return err
	}
	if s.client.privateConfiguration(before) != s.client.privateConfiguration(after) {
		return serviceDenied("datamigration_graph_changed_during_walk")
	}
	for _, id := range slices.Sorted(maps.Keys(selected)) {
		value := selected[id]
		// Classic service/project children have native DELETE operations. Each
		// immediate child is a prerequisite; no parent DELETE cancels unreviewed work.
		for _, childID := range slices.Sorted(maps.Keys(members)) {
			child := members[childID]
			if child.parent != id {
				continue
			}
			evidence := map[string]any{"resource_type": child.kind, "instance_id": child.id, "delete_by_default": true, "retention_supported": false}
			target, found := selected[childID]
			if !found {
				result.Unresolved = append(result.Unresolved, graph.UnresolvedReference{BlocksCleanup: true, Provider: value.Identity.Provider, ConnectionID: value.Identity.ConnectionID, NativeType: child.kind, NativeID: childID, ControllerID: value.ID, Relationship: graph.RelationshipAttachedTo, Evidence: evidence})
				continue
			}
			allowed := items[childID].Actionable != nil && *items[childID].Actionable
			result.Bindings = append(result.Bindings, graph.LifecycleBinding{ControllerAssetID: value.ID, ManagedAssetID: target.ID, Authority: graph.AuthorityAuthoritative, Ownership: graph.OwnershipExclusive, CleanupPolicy: graph.CleanupDirect, DirectCleanupAllowed: allowed, EvidenceSource: serviceCascadeSource, Evidence: evidence, Confidence: 1})
			result.Relationships = append(result.Relationships, graph.Relationship{SourceAssetID: target.ID, TargetAssetID: value.ID, Type: graph.RelationshipAttachedTo, Source: serviceCascadeSource, Evidence: evidence, Confidence: 1})
		}
		refs := maps.Clone(members[id].refs)
		for kind, ids := range refs {
			refs[kind] = slices.DeleteFunc(slices.Clone(ids), func(target string) bool { return target == members[id].parent || target == id })
		}
		references, err := s.client.contributeNativeReferences(value, assets, refs, "azure:datamigration-reference")
		if err != nil {
			return err
		}
		result.Relationships = append(result.Relationships, references.Relationships...)
		result.Unresolved = append(result.Unresolved, references.Unresolved...)
		// Target-scoped modern migrations are independent resources, and files
		// may be consumed by a task in another project. Both need reviewed deletion.
		for _, sourceID := range slices.Sorted(maps.Keys(object(items[id].Normalized["_datamigration_dependents"]))) {
			entry := object(object(items[id].Normalized["_datamigration_dependents"])[sourceID])
			evidence := map[string]any{graph.RelationshipEvidenceRequiredDeletion: true, graph.RelationshipEvidenceAutomaticSelection: false, graph.RelationshipEvidenceAuthority: graph.AuthorityAuthoritative, graph.RelationshipEvidenceDeletionOrder: graph.DeletionOrderTargetBeforeSource}
			controllers := map[string]any{}
			if members[id].kind == dataMigrationFileType {
				for ancestor := range object(value.Normalized["_datamigration_ancestors"]) {
					if controller := selected[ancestor]; controller.ID != "" && strings.HasPrefix(sourceID, ancestor+"/") {
						controllers[string(controller.ID)] = true
					}
				}
			}
			evidence[graph.RelationshipEvidenceDeletionCascadeControllers] = controllers
			source, found := selected[sourceID]
			if !found {
				result.Unresolved = append(result.Unresolved, graph.UnresolvedReference{BlocksCleanup: true, Provider: value.Identity.Provider, ConnectionID: value.Identity.ConnectionID, NativeType: text(entry["kind"]), NativeID: sourceID, ControllerID: value.ID, Relationship: graph.RelationshipDependsOn, Evidence: evidence})
				continue
			}
			if source.Identity.NativeType != entry["kind"] || source.Normalized[dataMigrationConfiguration] != entry["configuration"] {
				return serviceDenied("datamigration_graph_consumer_changed")
			}
			result.Relationships = append(result.Relationships, graph.Relationship{SourceAssetID: value.ID, TargetAssetID: source.ID, Type: graph.RelationshipDependsOn, Source: "azure:datamigration-reference", Evidence: evidence, Confidence: 1})
		}
	}
	return nil
}
