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
	// Children are selected in parent order before any read: the selection
	// shares seen across parents. Parents past the first invalid selection are
	// not read; that parent's own registration walk still precedes its error.
	type machineSelection struct {
		known    map[string]any
		selected map[string]asset.Asset
		invalid  error
	}
	ids := slices.Sorted(maps.Keys(parents))
	var selections []machineSelection
	for _, id := range ids {
		parent := parents[id]
		state := object(parent.Normalized[hybridComputeCleanup])
		next := machineSelection{known: maps.Clone(object(state["members"])), selected: map[string]asset.Asset{}}
		for _, value := range values {
			if value.Identity.Provider != asset.ProviderAzure || !hybridComputeChild(value.Identity.NativeType) {
				continue
			}
			canonical, err := s.client.hybridComputeIdentity(value.Identity.NativeID, value.Identity.NativeType)
			if err != nil {
				next.invalid = err
				break
			}
			if hybridComputeParent(canonical, value.Identity.NativeType) != id {
				continue
			}
			if err := s.client.hybridComputeCleanupRecord(value); err != nil {
				next.invalid = err
				break
			}
			if value.Identity.ConnectionID != parent.Identity.ConnectionID || value.Identity.Partition != parent.Identity.Partition || next.selected[canonical].ID != "" || seen[value.ID] || object(value.Normalized[hybridComputeCleanup])["parent"] != state["registration"] || value.Location != parent.Location {
				next.invalid = serviceDenied("hybrid_compute_graph_child_context_changed")
				break
			}
			next.selected[canonical], seen[value.ID] = value, true
			next.known[canonical] = map[string]any{"kind": value.Identity.NativeType, "configuration": object(value.Normalized[hybridComputeCleanup])["resource"]}
		}
		selections = append(selections, next)
		if next.invalid != nil {
			break
		}
	}
	contributions, errs := readConcurrently(len(selections), func(i int) (governance.Contribution, error) {
		id, parent, selection := ids[i], parents[ids[i]], selections[i]
		var out governance.Contribution
		if err := s.contributeAzureLocalRegistration(ctx, parent, values, &out); err != nil {
			return out, err
		}
		if selection.invalid != nil {
			return out, selection.invalid
		}
		state := object(parent.Normalized[hybridComputeCleanup])
		known, selected := selection.known, selection.selected
		var members map[string]any
		before := ""
		for range 2 {
			res, err := s.client.hybridComputeRead(ctx, id, hybridMachineType)
			present := err == nil
			if err != nil && !isNotFound(err) {
				return out, err
			}
			if present && (s.client.privateConfiguration(hybridComputeMachineSnapshot(res.data)) != state["resource"] || s.client.privateConfiguration(hybridComputeParentStamp(res.data)) != state["registration"]) {
				return out, serviceDenied("hybrid_compute_graph_machine_changed")
			}
			children, err := s.client.hybridComputeMachineChildren(ctx, id, known, present)
			if err != nil {
				return out, err
			}
			members = s.client.hybridComputeMachineMembers(children)
			for child, value := range selected {
				if entry := object(members[child]); entry != nil && entry["configuration"] != object(value.Normalized[hybridComputeCleanup])["resource"] {
					return out, serviceDenied("hybrid_compute_graph_child_changed")
				}
			}
			current := s.client.privateConfiguration(map[string]any{"present": present, "members": members})
			if before != "" && before != current {
				return out, serviceDenied("hybrid_compute_graph_changed_during_walk")
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
				out.Unresolved = append(out.Unresolved, graph.UnresolvedReference{BlocksCleanup: true, Provider: parent.Identity.Provider, ConnectionID: parent.Identity.ConnectionID, NativeType: text(entry["kind"]), NativeID: childID, ControllerID: parent.ID, Relationship: graph.RelationshipAttachedTo, Evidence: evidence})
				continue
			}
			allowed := target.Normalized["cleanup_protected"] == false
			out.Bindings = append(out.Bindings, graph.LifecycleBinding{ControllerAssetID: parent.ID, ManagedAssetID: target.ID, Authority: graph.AuthorityAuthoritative, Ownership: graph.OwnershipExclusive, CleanupPolicy: graph.CleanupDirect, DirectCleanupAllowed: allowed, EvidenceSource: serviceCascadeSource, Evidence: evidence, Confidence: 1})
			out.Relationships = append(out.Relationships, graph.Relationship{SourceAssetID: target.ID, TargetAssetID: parent.ID, Type: graph.RelationshipAttachedTo, Source: serviceCascadeSource, Evidence: evidence, Confidence: 1})
		}
		return out, nil
	})
	for i := range selections {
		if errs[i] != nil {
			return errs[i]
		}
		result.Relationships = append(result.Relationships, contributions[i].Relationships...)
		result.Bindings = append(result.Bindings, contributions[i].Bindings...)
		result.Unresolved = append(result.Unresolved, contributions[i].Unresolved...)
	}
	return nil
}
