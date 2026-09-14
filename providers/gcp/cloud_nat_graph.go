package gcp

import (
	"context"
	"slices"

	"github.com/loomx-ai/steward/internal/app/governance"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
)

// Regional NATs reference globally scoped Hubs by complete native identity.
// Scope keys cannot be copied from the source to resolve this relationship.
type CloudNatHubs struct{}

func NewCloudNatHubs() *CloudNatHubs { return &CloudNatHubs{} }

func (*CloudNatHubs) Contribute(_ context.Context, _ asset.ScopeID, assets []asset.Asset) (governance.Contribution, error) {
	result := governance.Contribution{}
	for _, nat := range assets {
		if nat.Identity.Provider != asset.ProviderGCP || nat.Identity.NativeType != cloudNatType || nat.ClosedAt != nil {
			continue
		}
		c := &client{project: text(nat.Normalized["project_id"]), number: text(nat.Normalized["project_number"])}
		hubs, membership, err := c.cloudNatHubReferences(cloudNatConfiguration(nat.Normalized))
		if err != nil {
			return result, err
		}
		if membership {
			if text(nat.Normalized["_cloud_nat_hub_membership"]) == "" {
				return result, groupDenied("cloud_nat_hub_rescan_required")
			}
			saved, err := discoveryStrings(nat.Normalized[referenceKey(cloudNatHubType)])
			if err != nil {
				return result, err
			}
			hubs = append(hubs, saved...)
		}
		slices.Sort(hubs)
		for _, id := range slices.Compact(hubs) {
			canonical, err := c.cloudNatHubName(id)
			if err != nil || canonical != id {
				return result, groupDenied("cloud_nat_hub_reference_invalid")
			}
			var target *asset.Asset
			for i := range assets {
				candidate := &assets[i]
				if candidate.ClosedAt == nil && candidate.Identity.Provider == nat.Identity.Provider && candidate.Identity.Partition == nat.Identity.Partition && candidate.Identity.ConnectionID == nat.Identity.ConnectionID && candidate.Identity.NativeType == cloudNatHubType && candidate.Identity.NativeID == id {
					if target != nil {
						return result, groupDenied("cloud_nat_hub_identity_ambiguous")
					}
					target = candidate
				}
			}
			evidence := map[string]any{"target_native_id": id, "source": "native_nat_rule"}
			if target == nil {
				result.Unresolved = append(result.Unresolved, graph.UnresolvedReference{Provider: nat.Identity.Provider, ConnectionID: nat.Identity.ConnectionID, ControllerID: nat.ID, NativeType: cloudNatHubType, NativeID: id, Relationship: graph.RelationshipDependsOn, Evidence: evidence})
			} else {
				result.Relationships = append(result.Relationships, graph.Relationship{SourceAssetID: nat.ID, TargetAssetID: target.ID, Type: graph.RelationshipDependsOn, Source: "gcp:cloud-nat-hubs", Confidence: 1, Evidence: evidence})
			}
		}
	}
	return result, nil
}
