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

func (s *serviceCascades) communicationGraphSnapshot(ctx context.Context, selected map[string]asset.Asset) (map[string]communicationTree, map[string]contracts.InventoryItem, error) {
	metadata, err := providerData()
	if err != nil {
		return nil, nil, err
	}
	runtime := &Runtime{bundle: metadata.bundle}
	roots := map[string]string{}
	hints := map[string]communicationMember{}
	known := map[string]any{}
	for _, value := range selected {
		kind := runtime.resourceKind(value.Identity.NativeType)
		current, _, err := s.client.communicationKnown(contracts.InventoryRequest{ConnectionID: value.Identity.ConnectionID, ResourceKind: &kind, KnownNativeIDs: []string{value.Identity.NativeID}, KnownNativeMetadata: map[string]map[string]any{value.Identity.NativeID: value.Normalized}})
		if err != nil {
			return nil, nil, err
		}
		for id, hint := range current {
			if previous := hints[id]; previous.id != "" && (previous.kind != hint.kind || previous.parent != hint.parent || previous.root != hint.root) {
				return nil, nil, serviceDenied("communication_graph_member_conflict")
			}
			hints[id] = hint
		}
		roots[text(value.Normalized["_communication_root"])] = communicationRootKind(value.Identity.NativeType)
		if err := communicationMergeIncoming(known, object(value.Normalized["_communication_incoming"])); err != nil {
			return nil, nil, err
		}
	}
	trees := map[string]communicationTree{}
	domains := map[string]bool{}
	for _, id := range slices.Sorted(maps.Keys(roots)) {
		raw, err := s.client.communicationARMRead(ctx, id, roots[id])
		if err != nil {
			return nil, nil, err
		}
		tree, err := s.client.communicationTree(ctx, raw, hints)
		if err != nil {
			return nil, nil, err
		}
		trees[id] = tree
		for id, member := range tree.members {
			if member.kind == communicationDomainType {
				domains[id] = true
			}
		}
	}
	incoming, _, err := s.client.communicationIncoming(ctx, domains, known)
	if err != nil {
		return nil, nil, err
	}
	locks, err := s.client.managementLocks(ctx)
	if err != nil {
		return nil, nil, err
	}
	listedGroups, err := s.client.insightsGroups(ctx)
	if err != nil {
		return nil, nil, err
	}
	groups := map[string]map[string]any{}
	items := map[string]contracts.InventoryItem{}
	for _, root := range slices.Sorted(maps.Keys(trees)) {
		tree := trees[root]
		tree.incoming = incoming
		trees[root] = tree
		groupID := strings.Join(strings.Split(root, "/")[:5], "/")
		if groups[groupID] == nil {
			if listedGroups[groupID] == nil {
				return nil, nil, serviceDenied("communication_graph_group_missing")
			}
			groups[groupID], err = s.client.insightsGroup(ctx, groupID, listedGroups[groupID])
			if err != nil {
				return nil, nil, err
			}
		}
		for _, id := range slices.Sorted(maps.Keys(tree.members)) {
			member := tree.members[id]
			if _, exists := items[id]; exists {
				return nil, nil, serviceDenied("ambiguous_communication_graph_owner")
			}
			item, err := runtime.communicationInventoryItem(s.client, s.connectionID, tree, member, groups[groupID], locks)
			if err != nil {
				return nil, nil, err
			}
			items[id] = item
		}
	}
	for id, value := range selected {
		if items[id].NativeType != value.Identity.NativeType || items[id].Normalized[communicationProof] != value.Normalized[communicationProof] {
			return nil, nil, serviceDenied("communication_graph_inventory_changed")
		}
	}
	return trees, items, nil
}

func (s *serviceCascades) contributeCommunication(ctx context.Context, assets []asset.Asset, result *governance.Contribution) error {
	selected := map[string]asset.Asset{}
	seen := map[asset.AssetID]bool{}
	for _, value := range assets {
		if value.Identity.Provider != asset.ProviderAzure || communicationKind(value.Identity.NativeType) == "" {
			continue
		}
		if err := s.client.communicationAsset(value); err != nil {
			return err
		}
		if value.Identity.ConnectionID != s.connectionID || selected[value.Identity.NativeID].ID != "" || seen[value.ID] {
			return serviceDenied("ambiguous_communication_graph_asset")
		}
		selected[value.Identity.NativeID], seen[value.ID] = value, true
	}
	if len(selected) == 0 {
		return nil
	}
	trees, items, err := s.communicationGraphSnapshot(ctx, selected)
	if err != nil {
		return err
	}
	_, after, err := s.communicationGraphSnapshot(ctx, selected)
	if err != nil {
		return err
	}
	if s.client.privateConfiguration(map[string]any{"items": items}) != s.client.privateConfiguration(map[string]any{"items": after}) {
		return serviceDenied("communication_graph_changed_during_walk")
	}
	for _, id := range slices.Sorted(maps.Keys(selected)) {
		value := selected[id]
		tree := trees[text(value.Normalized["_communication_root"])]
		for _, childID := range slices.Sorted(maps.Keys(tree.members)) {
			child := tree.members[childID]
			if child.parent != id {
				continue
			}
			// Account deletion releases its phone numbers natively. Other
			// children use their own reviewed DELETE before their parent.
			policy := graph.CleanupDirect
			evidence := map[string]any{"resource_type": child.kind, "instance_id": child.id, "delete_by_default": true, "retention_supported": false}
			if child.kind == communicationPhoneType {
				policy = graph.CleanupDelegate
				evidence[graph.LifecycleEvidenceControllerDeleteGuaranteed] = true
				evidence[graph.LifecycleEvidenceControllerVerifiesManagedAbsence] = true
			}
			target, found := selected[childID]
			if !found {
				result.Unresolved = append(result.Unresolved, graph.UnresolvedReference{BlocksCleanup: true, Provider: value.Identity.Provider, ConnectionID: value.Identity.ConnectionID, NativeType: child.kind, NativeID: child.id, ControllerID: value.ID, Relationship: graph.RelationshipAttachedTo, Evidence: evidence})
				continue
			}
			directAllowed := items[childID].Actionable != nil && *items[childID].Actionable
			result.Bindings = append(result.Bindings, graph.LifecycleBinding{ControllerAssetID: value.ID, ManagedAssetID: target.ID, Authority: graph.AuthorityAuthoritative, Ownership: graph.OwnershipExclusive, CleanupPolicy: policy, DirectCleanupAllowed: directAllowed, EvidenceSource: serviceCascadeSource, Evidence: evidence, Confidence: 1})
			result.Relationships = append(result.Relationships, graph.Relationship{SourceAssetID: target.ID, TargetAssetID: value.ID, Type: graph.RelationshipAttachedTo, Source: serviceCascadeSource, Evidence: evidence, Confidence: 1})
		}
		member := tree.members[id]
		refs, err := communicationReferences(member)
		if err != nil {
			return err
		}
		// Ownership already represents each immediate parent. Preserve only
		// references to independently managed resources here.
		for kind, ids := range refs {
			refs[kind] = slices.DeleteFunc(ids, func(candidate string) bool { return candidate == member.parent })
		}
		references, err := s.client.contributeNativeReferences(value, assets, refs, "azure:communication-reference")
		if err != nil {
			return err
		}
		result.Relationships = append(result.Relationships, references.Relationships...)
		result.Unresolved = append(result.Unresolved, references.Unresolved...)
		if member.kind != communicationDomainType {
			continue
		}
		for _, accountID := range slices.Sorted(maps.Keys(object(tree.incoming[id]))) {
			evidence := map[string]any{graph.RelationshipEvidenceRequiredDeletion: true, graph.RelationshipEvidenceAutomaticSelection: false, graph.RelationshipEvidenceAuthority: graph.AuthorityAuthoritative, graph.RelationshipEvidenceDeletionOrder: graph.DeletionOrderTargetBeforeSource}
			account, found := selected[accountID]
			if !found {
				result.Unresolved = append(result.Unresolved, graph.UnresolvedReference{BlocksCleanup: true, Provider: value.Identity.Provider, ConnectionID: value.Identity.ConnectionID, NativeType: communicationType, NativeID: accountID, ControllerID: value.ID, Relationship: graph.RelationshipDependsOn, Evidence: evidence})
				continue
			}
			result.Relationships = append(result.Relationships, graph.Relationship{SourceAssetID: value.ID, TargetAssetID: account.ID, Type: graph.RelationshipDependsOn, Source: serviceCascadeSource, Evidence: evidence, Confidence: 1})
		}
	}
	return nil
}
