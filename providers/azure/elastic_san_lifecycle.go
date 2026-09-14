package azure

import (
	"context"
	"github.com/loomx-ai/steward/internal/app/governance"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
	"maps"
	"slices"
)

// Volume snapshots share their source volume's lifetime, but native independent
// snapshot DELETE lets the planner review/order each removal without force.
func (s *serviceCascades) contributeElasticSanVolumes(ctx context.Context, assets []asset.Asset, result *governance.Contribution) error {
	byID := map[string]asset.Asset{}
	for _, value := range assets {
		if value.Identity.Provider == asset.ProviderAzure && value.Identity.ConnectionID == s.connectionID {
			byID[value.Identity.NativeID] = value
		}
	}
	for _, value := range assets {
		if value.Identity.Provider != asset.ProviderAzure || value.Identity.NativeType != elasticSanVolumeType {
			continue
		}
		state := object(value.Normalized[elasticSanSnapshotCleanup])
		if value.Identity.ConnectionID != s.connectionID || value.ID == "" || len(state) != 4 || value.Normalized[elasticSanSnapshotCleanupProof] != s.client.elasticSanChildBinding(value, state) {
			return serviceDenied("elastic_san_volume_graph_changed")
		}
		if _, err := s.client.elasticSanRecorded(value); err != nil {
			return err
		}
		expected := object(object(state["volume"])["snapshots"])
		known := maps.Clone(expected)
		if known == nil {
			known = map[string]any{}
		}
		// Older volume records may predate snapshot membership enrichment. A known
		// signed snapshot asset must still prevent a list omission from hiding it.
		for id, child := range byID {
			if child.Identity.NativeType != elasticSanSnapshotType {
				continue
			}
			refs, err := s.client.elasticSanRecordedReferences(child)
			if err != nil {
				return err
			}
			if slices.Contains(refs[elasticSanVolumeType], value.Identity.NativeID) {
				known[id] = true
			}
		}
		children, err := s.client.elasticSanVolumeSnapshots(ctx, value.Identity.NativeID, known)
		if err != nil {
			return err
		}
		hashes := map[string]any{}
		for id, raw := range children {
			hashes[id] = s.client.privateConfiguration(hybridComputeChildSnapshot(raw))
		}
		if s.client.privateConfiguration(hashes) != s.client.privateConfiguration(expected) {
			return serviceDenied("elastic_san_volume_graph_snapshots_changed")
		}
		for id, raw := range children {
			target := byID[id]
			evidence := map[string]any{"resource_type": elasticSanSnapshotType, "instance_id": id, "delete_by_default": true, "retention_supported": false}
			if target.ID == "" {
				result.Unresolved = append(result.Unresolved, graph.UnresolvedReference{BlocksCleanup: true, Provider: asset.ProviderAzure, ConnectionID: value.Identity.ConnectionID, NativeType: elasticSanSnapshotType, NativeID: id, ControllerID: value.ID, Relationship: graph.RelationshipAttachedTo, Evidence: evidence})
				continue
			}
			if target.Identity.NativeType != elasticSanSnapshotType || object(target.Normalized[elasticSanSnapshotCleanup])["resource"] != hashes[id] || target.Normalized[elasticSanSnapshotCleanupProof] != s.client.elasticSanChildBinding(target, object(target.Normalized[elasticSanSnapshotCleanup])) {
				return serviceDenied("elastic_san_volume_graph_snapshot_changed")
			}
			allowed := s.client.elasticSanChildProtection(elasticSanSnapshotType, raw) == ""
			result.Bindings = append(result.Bindings, graph.LifecycleBinding{ControllerAssetID: value.ID, ManagedAssetID: target.ID, Authority: graph.AuthorityAuthoritative, Ownership: graph.OwnershipExclusive, CleanupPolicy: graph.CleanupDirect, DirectCleanupAllowed: allowed, EvidenceSource: serviceCascadeSource, Evidence: evidence, Confidence: 1})
			result.Relationships = append(result.Relationships, graph.Relationship{SourceAssetID: target.ID, TargetAssetID: value.ID, Type: graph.RelationshipAttachedTo, Source: serviceCascadeSource, Evidence: evidence, Confidence: 1})
		}
	}
	return nil
}

// Native group membership owns volumes, while incoming private connections keep
// independent selection. Active volume cleanup orders its own snapshots first.
func (s *serviceCascades) contributeElasticSanGroups(ctx context.Context, assets []asset.Asset, result *governance.Contribution) error {
	byID := map[string]asset.Asset{}
	for _, value := range assets {
		if value.Identity.Provider == asset.ProviderAzure && value.Identity.ConnectionID == s.connectionID {
			byID[value.Identity.NativeID] = value
		}
	}
	for _, value := range assets {
		if value.Identity.Provider != asset.ProviderAzure || value.Identity.ConnectionID != s.connectionID || value.Identity.NativeType != elasticSanGroupType {
			continue
		}
		state, err := s.client.elasticSanGroupRecorded(value)
		if err != nil {
			return err
		}
		if value.Normalized["retained"] == true {
			continue
		}
		a := &elasticSanChildAction{client: s.client, planned: value}
		stale := false
		for range 2 {
			own, err := s.client.elasticSanRead(ctx, value.Identity.NativeID, elasticSanGroupType)
			if err != nil {
				return err
			}
			nodes, err := a.groupMembers(ctx, false)
			if err != nil {
				return err
			}
			current, err := s.client.elasticSanGroupState(value.Identity.NativeID, own.data, false, nodes, nil)
			if err != nil {
				return err
			}
			if s.client.privateConfiguration(current) != s.client.privateConfiguration(state) {
				stale = true
				break
			}
		}
		if stale {
			// A child-only scan may leave its parent's frozen context behind.
			// Require a fresh group scan for group cleanup without preventing
			// unrelated resource reconciliation in the same scope.
			result.Unresolved = append(result.Unresolved, graph.UnresolvedReference{BlocksCleanup: true, Provider: value.Identity.Provider, ConnectionID: value.Identity.ConnectionID, NativeType: elasticSanGroupType, NativeID: value.Identity.NativeID, ControllerID: value.ID, Relationship: graph.RelationshipAttachedTo, Evidence: map[string]any{"reason": "elastic_san_group_membership_requires_refresh"}})
			continue
		}
		members := object(state["members"])
		all := maps.Clone(members)
		for id := range object(state["connections"]) {
			all[id] = map[string]any{"kind": elasticSanEndpointType}
		}
		for _, id := range slices.Sorted(maps.Keys(all)) {
			entry := object(all[id])
			kind := text(entry["kind"])
			source := object(members[text(entry["source"])])
			if kind == elasticSanSnapshotType && source["kind"] == elasticSanVolumeType && source["retained"] == false {
				continue
			}
			target := byID[id]
			retained := entry["retained"] == true
			evidence := map[string]any{"resource_type": kind, "instance_id": id, "delete_by_default": !retained, "retention_supported": retained}
			relation := graph.RelationshipAttachedTo
			if kind == elasticSanEndpointType {
				relation = graph.RelationshipDependsOn
				evidence[graph.RelationshipEvidenceRequiredDeletion] = true
				evidence[graph.RelationshipEvidenceAuthority] = graph.AuthorityAuthoritative
				evidence[graph.RelationshipEvidenceAutomaticSelection] = false
				evidence[graph.RelationshipEvidenceDeletionOrder] = graph.DeletionOrderTargetBeforeSource
			}
			if target.ID == "" {
				result.Unresolved = append(result.Unresolved, graph.UnresolvedReference{BlocksCleanup: true, Provider: asset.ProviderAzure, ConnectionID: value.Identity.ConnectionID, NativeType: kind, NativeID: id, ControllerID: value.ID, Relationship: relation, Evidence: evidence})
				continue
			}
			if target.Identity.NativeType != kind || s.client.elasticSanChildRecord(target) != nil {
				return serviceDenied("elastic_san_group_graph_member_changed")
			}
			if kind == elasticSanEndpointType {
				if object(object(state["connections"])[id])["mapped"] != true {
					return serviceDenied("elastic_san_group_graph_connection_unverified")
				}
				result.Relationships = append(result.Relationships, graph.Relationship{SourceAssetID: value.ID, TargetAssetID: target.ID, Type: relation, Source: serviceCascadeSource, Evidence: evidence, Confidence: 1})
				continue
			}
			if object(target.Normalized[elasticSanSnapshotCleanup])["resource"] != entry["configuration"] || target.Normalized["retained"] != retained {
				return serviceDenied("elastic_san_group_graph_member_configuration_changed")
			}
			policy := graph.CleanupDirect
			if retained {
				policy = graph.CleanupRetain
			}
			result.Bindings = append(result.Bindings, graph.LifecycleBinding{ControllerAssetID: value.ID, ManagedAssetID: target.ID, Authority: graph.AuthorityAuthoritative, Ownership: graph.OwnershipExclusive, CleanupPolicy: policy, DirectCleanupAllowed: !retained, EvidenceSource: serviceCascadeSource, Evidence: evidence, Confidence: 1})
			result.Relationships = append(result.Relationships, graph.Relationship{SourceAssetID: target.ID, TargetAssetID: value.ID, Type: relation, Source: serviceCascadeSource, Evidence: evidence, Confidence: 1})
		}
	}
	return nil
}

// Groups retain their existing direct volume/snapshot ordering. SAN deletion
// follows group deletion and independently selected private connections.
func (s *serviceCascades) contributeElasticSanRoots(ctx context.Context, assets []asset.Asset, result *governance.Contribution) error {
	byID := map[string]asset.Asset{}
	for _, value := range assets {
		if value.Identity.Provider == asset.ProviderAzure && value.Identity.ConnectionID == s.connectionID {
			byID[value.Identity.NativeID] = value
		}
	}
	for _, value := range assets {
		if value.Identity.Provider != asset.ProviderAzure || value.Identity.ConnectionID != s.connectionID || value.Identity.NativeType != elasticSanType {
			continue
		}
		unresolved := func(kind, id, reason string) {
			result.Unresolved = append(result.Unresolved, graph.UnresolvedReference{BlocksCleanup: true, Provider: value.Identity.Provider, ConnectionID: value.Identity.ConnectionID, ControllerID: value.ID, NativeType: kind, NativeID: id, Relationship: graph.RelationshipAttachedTo, Evidence: map[string]any{"reason": reason}})
		}
		if value.Normalized[elasticSanBoundary] == nil || value.Normalized[elasticSanSnapshotCleanup] == nil {
			unresolved(elasticSanType, value.Identity.NativeID, "elastic_san_boundary_requires_refresh")
			continue
		}
		cleanupState := object(value.Normalized[elasticSanSnapshotCleanup])
		if value.Normalized[elasticSanSnapshotCleanupProof] != s.client.elasticSanChildBinding(value, cleanupState) {
			return serviceDenied("elastic_san_root_graph_proof_changed")
		}
		boundary, err := s.client.elasticSanBoundaryRecorded(value)
		if err != nil {
			return err
		}
		if boundary["complete"] != true {
			unresolved(elasticSanType, value.Identity.NativeID, "elastic_san_boundary_incomplete")
			continue
		}
		action := &elasticSanChildAction{client: s.client, planned: value}
		stale := false
		for range 2 {
			nodes, err := action.rootMembers(ctx, false)
			if err != nil {
				return err
			}
			current := s.client.elasticSanBoundaryState(value.Identity.NativeID, nodes, nil)
			if s.client.privateConfiguration(current) != s.client.privateConfiguration(boundary) {
				stale = true
				break
			}
		}
		if stale {
			unresolved(elasticSanType, value.Identity.NativeID, "elastic_san_boundary_requires_refresh")
			continue
		}
		children := object(object(value.Normalized[elasticSanSnapshotCleanup])["children"])
		for _, id := range slices.Sorted(maps.Keys(object(boundary["members"]))) {
			member := object(object(boundary["members"])[id])
			kind := text(member["kind"])
			target := byID[id]
			if member["retained"] == true {
				unresolved(kind, id, "elastic_san_retained_children_require_resolution")
				continue
			}
			if target.ID == "" {
				unresolved(kind, id, "elastic_san_member_requires_inventory")
				continue
			}
			state := object(target.Normalized[elasticSanSnapshotCleanup])
			if target.Identity.NativeType != kind || state["resource"] != object(children[id])["configuration"] || target.Normalized[elasticSanSnapshotCleanupProof] != s.client.elasticSanChildBinding(target, state) {
				unresolved(kind, id, "elastic_san_member_requires_refresh")
				continue
			}
			if kind != elasticSanGroupType && kind != elasticSanEndpointType {
				parent := elasticSanParent(id, kind)
				if object(boundary["members"])[parent] == nil {
					unresolved(elasticSanGroupType, parent, "elastic_san_member_parent_requires_resolution")
				}
				continue
			}
			evidence := map[string]any{"resource_type": kind, "instance_id": id, "delete_by_default": true, "retention_supported": false}
			if kind == elasticSanEndpointType {
				evidence[graph.RelationshipEvidenceRequiredDeletion] = true
				evidence[graph.RelationshipEvidenceAuthority] = graph.AuthorityAuthoritative
				evidence[graph.RelationshipEvidenceAutomaticSelection] = false
				evidence[graph.RelationshipEvidenceDeletionOrder] = graph.DeletionOrderTargetBeforeSource
				result.Relationships = append(result.Relationships, graph.Relationship{SourceAssetID: value.ID, TargetAssetID: target.ID, Type: graph.RelationshipDependsOn, Source: serviceCascadeSource, Evidence: evidence, Confidence: 1})
			} else {
				result.Bindings = append(result.Bindings, graph.LifecycleBinding{ControllerAssetID: value.ID, ManagedAssetID: target.ID, Authority: graph.AuthorityAuthoritative, Ownership: graph.OwnershipExclusive, CleanupPolicy: graph.CleanupDirect, DirectCleanupAllowed: true, EvidenceSource: serviceCascadeSource, Evidence: evidence, Confidence: 1})
				result.Relationships = append(result.Relationships, graph.Relationship{SourceAssetID: target.ID, TargetAssetID: value.ID, Type: graph.RelationshipAttachedTo, Source: serviceCascadeSource, Evidence: evidence, Confidence: 1})
			}
		}
	}
	return nil
}
