package azure

import (
	"context"
	"strings"

	"github.com/loomx-ai/steward/internal/app/governance"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
)

// sourceId, BYOS storage and assigned identities are references. Deleting the
// workbook does not authorize deleting those independently managed resources.
func (c *client) contributeWorkbookReferences(ctx context.Context, parent asset.Asset, assets []asset.Asset, index *assetIndex) (governance.Contribution, error) {
	result := governance.Contribution{}
	record, err := c.workbookRecord(ctx, parent.Identity.NativeType, parent.Identity.NativeID)
	if err != nil {
		return result, err
	}
	if expected := text(parent.Normalized[insightsWorkbookProof]); expected == "" || expected != c.workbookConfiguration(record) {
		return result, serviceDenied("workbook_graph_configuration_changed")
	}
	return c.contributeIndexedReferences(parent, assets, index, workbookReferences(parent.Identity.NativeType, parent.Identity.NativeID, record.raw), "azure:workbook-reference")
}

func (c *client) contributeNativeReferences(parent asset.Asset, assets []asset.Asset, references map[string][]string, source string) (governance.Contribution, error) {
	return c.contributeIndexedReferences(parent, assets, nil, references, source)
}

// A nil index scans assets; callers that resolve many parents share one index.
func (c *client) contributeIndexedReferences(parent asset.Asset, assets []asset.Asset, index *assetIndex, references map[string][]string, source string) (governance.Contribution, error) {
	result := governance.Contribution{}
	for kind, ids := range references {
		for _, id := range ids {
			var target *asset.Asset
			if strings.HasPrefix(id, c.root()+"/") {
				var matches []int
				if index != nil {
					matches = index.byIdentity[serviceAssetKeyOf(parent.Identity, kind, id)]
				} else {
					for i := range assets {
						if candidate := &assets[i]; candidate.Identity.Provider == parent.Identity.Provider && candidate.Identity.ConnectionID == parent.Identity.ConnectionID && candidate.Identity.Partition == parent.Identity.Partition && strings.EqualFold(candidate.Identity.NativeType, kind) && strings.EqualFold(candidate.Identity.NativeID, id) {
							matches = append(matches, i)
						}
					}
				}
				for _, i := range matches {
					candidate := &assets[i]
					if target != nil || candidate.ID == "" || candidate.ID == parent.ID {
						return result, serviceDenied("ambiguous_azure_resource_reference")
					}
					target = candidate
				}
			}
			evidence := map[string]any{"resource_type": kind, "instance_id": id}
			if target == nil {
				result.Unresolved = append(result.Unresolved, graph.UnresolvedReference{Provider: parent.Identity.Provider, ConnectionID: parent.Identity.ConnectionID, NativeType: kind, NativeID: id, ControllerID: parent.ID, Relationship: graph.RelationshipUses, Evidence: evidence})
				continue
			}
			result.Relationships = append(result.Relationships, graph.Relationship{SourceAssetID: parent.ID, TargetAssetID: target.ID, Type: graph.RelationshipUses, Source: source, Evidence: evidence, Confidence: 1})
		}
	}
	return result, nil
}
