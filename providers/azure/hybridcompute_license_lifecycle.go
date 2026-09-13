package azure

import (
	"context"
	"maps"
	"slices"

	"github.com/loomx-ai/steward/internal/app/governance"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
)

func (s *serviceCascades) contributeHybridComputeLicenses(ctx context.Context, values []asset.Asset, result *governance.Contribution) error {
	for _, license := range values {
		if license.Identity.Provider != asset.ProviderAzure || license.Identity.NativeType != hybridLicenseType {
			continue
		}
		if err := s.client.hybridComputeCleanupRecord(license); err != nil {
			return err
		}
		if license.Identity.ConnectionID != s.connectionID {
			return serviceDenied("hybrid_compute_license_graph_connection_changed")
		}
		state := object(license.Normalized[hybridComputeCleanup])
		known := maps.Clone(object(state["assignments"]))
		selected := map[string]asset.Asset{}
		for _, profile := range values {
			if profile.Identity.Provider != asset.ProviderAzure || profile.Identity.NativeType != hybridProfileType {
				continue
			}
			refs, err := s.client.hybridComputeRecordedReferences(profile)
			if err != nil {
				return err
			}
			if !slices.Contains(refs[hybridLicenseType], license.Identity.NativeID) {
				continue
			}
			if err := s.client.hybridComputeCleanupRecord(profile); err != nil {
				return err
			}
			if profile.Identity.ConnectionID != license.Identity.ConnectionID || profile.Identity.Partition != license.Identity.Partition || selected[profile.Identity.NativeID].ID != "" || profile.ID == license.ID {
				return serviceDenied("invalid_hybrid_compute_license_graph_profile")
			}
			known[profile.Identity.NativeID] = object(profile.Normalized[hybridComputeCleanup])["resource"]
			selected[profile.Identity.NativeID] = profile
		}
		var assignments map[string]map[string]any
		before := ""
		for range 2 {
			res, err := s.client.hybridComputeRead(ctx, license.Identity.NativeID, hybridLicenseType)
			if err != nil && !isNotFound(err) {
				return err
			}
			present := err == nil
			if present && s.client.privateConfiguration(hybridComputeLicenseSnapshot(res.data)) != state["resource"] {
				return serviceDenied("hybrid_compute_license_graph_changed")
			}
			assignments, err = s.client.hybridComputeLicenseAssignments(ctx, license.Identity.NativeID, known, present)
			if err != nil {
				return err
			}
			snapshot := s.client.privateConfiguration(map[string]any{"present": present, "assignments": s.client.hybridComputeAssignmentRecords(assignments)})
			if before != "" && snapshot != before {
				return serviceDenied("hybrid_compute_license_assignments_changed_during_walk")
			}
			before = snapshot
		}
		for _, id := range slices.Sorted(maps.Keys(assignments)) {
			evidence := map[string]any{graph.RelationshipEvidenceRequiredDeletion: true, graph.RelationshipEvidenceAutomaticSelection: false, graph.RelationshipEvidenceAuthority: graph.AuthorityAuthoritative, graph.RelationshipEvidenceDeletionOrder: graph.DeletionOrderTargetBeforeSource, "resource_type": hybridProfileType, "instance_id": id}
			profile, found := selected[id]
			if !found {
				result.Unresolved = append(result.Unresolved, graph.UnresolvedReference{BlocksCleanup: true, Provider: license.Identity.Provider, ConnectionID: license.Identity.ConnectionID, NativeType: hybridProfileType, NativeID: id, ControllerID: license.ID, Relationship: graph.RelationshipDependsOn, Evidence: evidence})
				continue
			}
			if s.client.privateConfiguration(hybridComputeChildSnapshot(assignments[id])) != object(profile.Normalized[hybridComputeCleanup])["resource"] {
				return serviceDenied("hybrid_compute_license_reviewed_assignment_changed")
			}
			result.Relationships = append(result.Relationships, graph.Relationship{SourceAssetID: license.ID, TargetAssetID: profile.ID, Type: graph.RelationshipDependsOn, Source: "azure:arc-license-assignment", Evidence: evidence, Confidence: 1})
		}
	}
	return nil
}
