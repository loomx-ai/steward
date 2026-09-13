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
				result.Unresolved = append(result.Unresolved, graph.UnresolvedReference{Provider: asset.ProviderAzure, ConnectionID: value.Identity.ConnectionID, NativeType: elasticSanSnapshotType, NativeID: id, ControllerID: value.ID, Relationship: graph.RelationshipAttachedTo, Evidence: evidence})
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
