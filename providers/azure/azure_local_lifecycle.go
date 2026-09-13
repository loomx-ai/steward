package azure

import (
	"context"
	"maps"
	"slices"

	"github.com/loomx-ai/steward/internal/app/governance"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
)

func (s *serviceCascades) contributeAzureLocalVMs(ctx context.Context, values []asset.Asset, result *governance.Contribution) error {
	selected, parents, seen := map[string]asset.Asset{}, map[string]asset.Asset{}, map[asset.AssetID]bool{}
	for _, value := range values {
		if value.Identity.Provider != asset.ProviderAzure || azureLocalKind(value.Identity.NativeType) == "" && !hybridComputeChild(value.Identity.NativeType) {
			continue
		}
		if value.Identity.ConnectionID != s.connectionID || selected[value.Identity.NativeID].ID != "" || seen[value.ID] {
			return serviceDenied("ambiguous_azure_local_vm_graph")
		}
		selected[value.Identity.NativeID], seen[value.ID] = value, true
		if value.Identity.NativeType == azureLocalVMType {
			if err := s.client.azureLocalVMRecord(value); err != nil {
				return err
			}
			parents[value.Identity.NativeID] = value
		}
	}
	for _, id := range slices.Sorted(maps.Keys(parents)) {
		parent := parents[id]
		a := &azureLocalAction{client: s.client, planned: parent}
		state := object(parent.Normalized[azureLocalCleanup])
		members := object(state["members"])
		before := ""
		for range 2 {
			vm, machine, children, err := a.vmObserve(ctx)
			if err != nil {
				return err
			}
			current := s.client.privateConfiguration(map[string]any{"vm": vm != nil, "machine": machine != nil, "children": s.client.azureLocalVMMembers(children)})
			if before != "" && before != current {
				return serviceDenied("azure_local_vm_graph_changed_during_walk")
			}
			before = current
		}
		for child, value := range selected {
			if azureLocalVMMember(child, value.Identity.NativeType, id, text(state["os_disk"])) {
				if err := a.vmMemberAsset(value); err != nil {
					return err
				}
			}
		}
		for _, childID := range slices.Sorted(maps.Keys(members)) {
			kind := text(object(members[childID])["kind"])
			evidence := map[string]any{"resource_type": kind, "instance_id": childID, "delete_by_default": true, "retention_supported": false}
			relation := graph.RelationshipAttachedTo
			if hybridComputeChild(kind) {
				// These are owned by the Arc registration, which remains a
				// separate resource. They must finish before removing its VM.
				relation = graph.RelationshipDependsOn
				evidence[graph.RelationshipEvidenceRequiredDeletion] = true
				evidence[graph.RelationshipEvidenceAuthority] = graph.AuthorityAuthoritative
				evidence[graph.RelationshipEvidenceDeletionOrder] = graph.DeletionOrderTargetBeforeSource
			}
			target, found := selected[childID]
			if !found {
				result.Unresolved = append(result.Unresolved, graph.UnresolvedReference{Provider: parent.Identity.Provider, ConnectionID: parent.Identity.ConnectionID, NativeType: kind, NativeID: childID, ControllerID: parent.ID, Relationship: relation, Evidence: evidence})
				continue
			}
			if hybridComputeChild(kind) {
				result.Relationships = append(result.Relationships, graph.Relationship{SourceAssetID: parent.ID, TargetAssetID: target.ID, Type: relation, Source: serviceCascadeSource, Evidence: evidence, Confidence: 1})
				continue
			}
			policy := graph.CleanupDirect
			if kind == azureLocalIdentityType || kind == azureLocalDiskType {
				policy = graph.CleanupDelegate
				if kind == azureLocalIdentityType {
					evidence[graph.LifecycleEvidenceControllerMetadata] = true
				}
				evidence[graph.LifecycleEvidenceControllerVerifiesManagedAbsence] = true
			}
			result.Bindings = append(result.Bindings, graph.LifecycleBinding{ControllerAssetID: parent.ID, ManagedAssetID: target.ID, Authority: graph.AuthorityAuthoritative, Ownership: graph.OwnershipExclusive, CleanupPolicy: policy, DirectCleanupAllowed: kind == azureLocalAgentType && target.Normalized["cleanup_protected"] == false, EvidenceSource: serviceCascadeSource, Evidence: evidence, Confidence: 1})
		}
	}
	return nil
}
