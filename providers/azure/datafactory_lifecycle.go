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

func (c *client) dataFactoryAsset(value asset.Asset) error {
	if value.ID == "" || value.Identity.Provider != asset.ProviderAzure || value.Identity.ConnectionID == "" || value.Normalized["_datafactory_connection"] != string(value.Identity.ConnectionID) || value.Identity.Partition != "azure" || value.Location != text(value.Normalized["_datafactory_location"]) {
		return serviceDenied("invalid_datafactory_asset_identity")
	}
	_, err := c.dataFactoryRecorded(value.Identity.NativeID, value.Identity.NativeType, value.Normalized)
	return err
}

func (s *serviceCascades) dataFactoryGraphSnapshot(ctx context.Context, selected map[string]asset.Asset) (map[string]dataFactoryTree, map[string]contracts.InventoryItem, map[string]any, error) {
	metadata, err := providerData()
	if err != nil {
		return nil, nil, nil, err
	}
	runtime := &Runtime{bundle: metadata.bundle}
	hints, known := map[string]dataFactoryMember{}, map[string]map[string]any{}
	for id, value := range selected {
		kind := runtime.resourceKind(value.Identity.NativeType)
		current, err := s.client.dataFactoryKnown(contracts.InventoryRequest{ConnectionID: value.Identity.ConnectionID, ResourceKind: &kind, KnownNativeIDs: []string{id}, KnownNativeMetadata: map[string]map[string]any{id: value.Normalized}})
		if err != nil {
			return nil, nil, nil, err
		}
		for id, hint := range current {
			if previous := hints[id]; previous.id != "" && (previous.kind != hint.kind || previous.nodeName != hint.nodeName) {
				return nil, nil, nil, serviceDenied("conflicting_datafactory_graph_identity")
			}
			hints[id] = hint
		}
		known[id] = value.Normalized
	}
	trees, _, err := s.client.dataFactoryContext(ctx, hints, known)
	if err != nil {
		return nil, nil, nil, err
	}
	items, bindings, err := runtime.dataFactoryItems(ctx, s.client, s.connectionID, trees)
	if err != nil {
		return nil, nil, nil, err
	}
	for id, value := range selected {
		if items[id].NativeType != value.Identity.NativeType || items[id].Normalized[dataFactoryProof] != value.Normalized[dataFactoryProof] {
			return nil, nil, nil, serviceDenied("datafactory_graph_inventory_changed")
		}
	}
	return trees, items, bindings, nil
}

func (s *serviceCascades) contributeDataFactory(ctx context.Context, assets []asset.Asset, result *governance.Contribution) error {
	selected, seen := map[string]asset.Asset{}, map[asset.AssetID]bool{}
	for _, value := range assets {
		if value.Identity.Provider != asset.ProviderAzure || dataFactoryKind(value.Identity.NativeType) == "" {
			continue
		}
		if err := s.client.dataFactoryAsset(value); err != nil {
			return err
		}
		if value.Identity.ConnectionID != s.connectionID || selected[value.Identity.NativeID].ID != "" || seen[value.ID] {
			return serviceDenied("ambiguous_datafactory_graph_asset")
		}
		selected[value.Identity.NativeID], seen[value.ID] = value, true
	}
	if len(selected) == 0 {
		return nil
	}
	trees, items, before, err := s.dataFactoryGraphSnapshot(ctx, selected)
	if err != nil {
		return err
	}
	_, _, after, err := s.dataFactoryGraphSnapshot(ctx, selected)
	if err != nil {
		return err
	}
	if s.client.privateConfiguration(before) != s.client.privateConfiguration(after) {
		return serviceDenied("datafactory_graph_changed_during_walk")
	}
	for _, id := range slices.Sorted(maps.Keys(selected)) {
		value := selected[id]
		tree := trees[dataFactoryRoot(id)]
		direct := dataFactoryDirectMembers(tree)
		for _, childID := range slices.Sorted(maps.Keys(tree.members)) {
			child := tree.members[childID]
			if dataFactoryController(child) != id {
				continue
			}
			policy := graph.CleanupDelegate
			evidence := map[string]any{"resource_type": child.kind, "instance_id": child.id, "delete_by_default": true, "retention_supported": false, graph.LifecycleEvidenceControllerDeleteGuaranteed: true, graph.LifecycleEvidenceControllerVerifiesManagedAbsence: true}
			if direct[childID] {
				policy = graph.CleanupDirect
				delete(evidence, graph.LifecycleEvidenceControllerDeleteGuaranteed)
				delete(evidence, graph.LifecycleEvidenceControllerVerifiesManagedAbsence)
			}
			target, found := selected[childID]
			if !found {
				result.Unresolved = append(result.Unresolved, graph.UnresolvedReference{Provider: value.Identity.Provider, ConnectionID: value.Identity.ConnectionID, NativeType: child.kind, NativeID: childID, ControllerID: value.ID, Relationship: graph.RelationshipAttachedTo, Evidence: evidence})
				continue
			}
			allowed := items[childID].Actionable != nil && *items[childID].Actionable
			result.Bindings = append(result.Bindings, graph.LifecycleBinding{ControllerAssetID: value.ID, ManagedAssetID: target.ID, Authority: graph.AuthorityAuthoritative, Ownership: graph.OwnershipExclusive, CleanupPolicy: policy, DirectCleanupAllowed: allowed, EvidenceSource: serviceCascadeSource, Evidence: evidence, Confidence: 1})
			result.Relationships = append(result.Relationships, graph.Relationship{SourceAssetID: target.ID, TargetAssetID: value.ID, Type: graph.RelationshipAttachedTo, Source: serviceCascadeSource, Evidence: evidence, Confidence: 1})
		}
		member := tree.members[id]
		refs := map[string][]string{}
		for kind, ids := range member.refs {
			refs[kind] = slices.DeleteFunc(slices.Clone(ids), func(target string) bool { return target == dataFactoryController(member) || target == id })
		}
		references, err := s.client.contributeNativeReferences(value, assets, refs, "azure:datafactory-reference")
		if err != nil {
			return err
		}
		result.Relationships = append(result.Relationships, references.Relationships...)
		result.Unresolved = append(result.Unresolved, references.Unresolved...)
		for _, sourceID := range slices.Sorted(maps.Keys(object(tree.incoming[id]))) {
			entry := object(object(tree.incoming[id])[sourceID])
			evidence := map[string]any{graph.RelationshipEvidenceRequiredDeletion: true, graph.RelationshipEvidenceAutomaticSelection: false, graph.RelationshipEvidenceAuthority: graph.AuthorityAuthoritative, graph.RelationshipEvidenceDeletionOrder: graph.DeletionOrderTargetBeforeSource}
			controllers := map[string]any{}
			if controller := selected[tree.root]; controller.ID != "" && strings.HasPrefix(sourceID, tree.root+"/") {
				controllers[string(controller.ID)] = true
			}
			evidence[graph.RelationshipEvidenceDeletionCascadeControllers] = controllers
			source, found := selected[sourceID]
			if !found {
				result.Unresolved = append(result.Unresolved, graph.UnresolvedReference{Provider: value.Identity.Provider, ConnectionID: value.Identity.ConnectionID, NativeType: text(entry["kind"]), NativeID: sourceID, ControllerID: value.ID, Relationship: graph.RelationshipDependsOn, Evidence: evidence})
				continue
			}
			if source.Identity.NativeType != entry["kind"] || source.Normalized[dataFactoryConfiguration] != entry["configuration"] {
				return serviceDenied("datafactory_graph_consumer_changed")
			}
			result.Relationships = append(result.Relationships, graph.Relationship{SourceAssetID: value.ID, TargetAssetID: source.ID, Type: graph.RelationshipDependsOn, Source: "azure:datafactory-reference", Evidence: evidence, Confidence: 1})
		}
	}
	return nil
}
