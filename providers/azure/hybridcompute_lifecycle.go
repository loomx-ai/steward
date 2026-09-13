package azure

import (
	"context"
	"maps"
	"slices"

	"github.com/loomx-ai/steward/internal/app/governance"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
)

func (s *serviceCascades) contributeHybridComputeMachines(ctx context.Context, values []asset.Asset, result *governance.Contribution) error {
	parents, seen := map[string]asset.Asset{}, map[asset.AssetID]bool{}
	for _, value := range values {
		if value.Identity.Provider != asset.ProviderAzure || value.Identity.NativeType != hybridMachineType {
			continue
		}
		if err := s.client.hybridComputeCleanupRecord(value); err != nil {
			return err
		}
		if value.Identity.ConnectionID != s.connectionID || parents[value.Identity.NativeID].ID != "" || seen[value.ID] {
			return serviceDenied("ambiguous_hybrid_compute_machine_graph")
		}
		parents[value.Identity.NativeID], seen[value.ID] = value, true
	}
	for _, id := range slices.Sorted(maps.Keys(parents)) {
		parent := parents[id]
		if err := s.contributeAzureLocalRegistration(ctx, parent, values, result); err != nil {
			return err
		}
		state := object(parent.Normalized[hybridComputeCleanup])
		known := maps.Clone(object(state["members"]))
		selected := map[string]asset.Asset{}
		for _, value := range values {
			if value.Identity.Provider != asset.ProviderAzure || !hybridComputeChild(value.Identity.NativeType) {
				continue
			}
			canonical, err := s.client.hybridComputeIdentity(value.Identity.NativeID, value.Identity.NativeType)
			if err != nil {
				return err
			}
			if hybridComputeParent(canonical, value.Identity.NativeType) != id {
				continue
			}
			if err := s.client.hybridComputeCleanupRecord(value); err != nil {
				return err
			}
			if value.Identity.ConnectionID != parent.Identity.ConnectionID || value.Identity.Partition != parent.Identity.Partition || selected[canonical].ID != "" || seen[value.ID] || object(value.Normalized[hybridComputeCleanup])["parent"] != state["registration"] || value.Location != parent.Location {
				return serviceDenied("hybrid_compute_graph_child_context_changed")
			}
			selected[canonical], seen[value.ID] = value, true
			known[canonical] = map[string]any{"kind": value.Identity.NativeType, "configuration": object(value.Normalized[hybridComputeCleanup])["resource"]}
		}
		var members map[string]any
		before := ""
		for range 2 {
			res, err := s.client.hybridComputeRead(ctx, id, hybridMachineType)
			present := err == nil
			if err != nil && !isNotFound(err) {
				return err
			}
			if present && (s.client.privateConfiguration(hybridComputeMachineSnapshot(res.data)) != state["resource"] || s.client.privateConfiguration(hybridComputeParentStamp(res.data)) != state["registration"]) {
				return serviceDenied("hybrid_compute_graph_machine_changed")
			}
			children, err := s.client.hybridComputeMachineChildren(ctx, id, known, present)
			if err != nil {
				return err
			}
			members = s.client.hybridComputeMachineMembers(children)
			for child, value := range selected {
				if entry := object(members[child]); entry != nil && entry["configuration"] != object(value.Normalized[hybridComputeCleanup])["resource"] {
					return serviceDenied("hybrid_compute_graph_child_changed")
				}
			}
			current := s.client.privateConfiguration(map[string]any{"present": present, "members": members})
			if before != "" && before != current {
				return serviceDenied("hybrid_compute_graph_changed_during_walk")
			}
			before = current
		}
		// Keep a reviewed child whose own GET is already 404 as a direct absence
		// step until the application reconciles it. Never infer it from parent 404.
		for child := range selected {
			if members[child] == nil {
				members[child] = known[child]
			}
		}
		for _, childID := range slices.Sorted(maps.Keys(members)) {
			entry := object(members[childID])
			evidence := map[string]any{"resource_type": entry["kind"], "instance_id": childID, "delete_by_default": true, "retention_supported": false}
			target, found := selected[childID]
			if !found {
				result.Unresolved = append(result.Unresolved, graph.UnresolvedReference{BlocksCleanup: true, Provider: parent.Identity.Provider, ConnectionID: parent.Identity.ConnectionID, NativeType: text(entry["kind"]), NativeID: childID, ControllerID: parent.ID, Relationship: graph.RelationshipAttachedTo, Evidence: evidence})
				continue
			}
			allowed := target.Normalized["cleanup_protected"] == false
			result.Bindings = append(result.Bindings, graph.LifecycleBinding{ControllerAssetID: parent.ID, ManagedAssetID: target.ID, Authority: graph.AuthorityAuthoritative, Ownership: graph.OwnershipExclusive, CleanupPolicy: graph.CleanupDirect, DirectCleanupAllowed: allowed, EvidenceSource: serviceCascadeSource, Evidence: evidence, Confidence: 1})
			result.Relationships = append(result.Relationships, graph.Relationship{SourceAssetID: target.ID, TargetAssetID: parent.ID, Type: graph.RelationshipAttachedTo, Source: serviceCascadeSource, Evidence: evidence, Confidence: 1})
		}
	}
	return nil
}
