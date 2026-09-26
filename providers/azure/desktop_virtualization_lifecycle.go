package azure

import (
	"strings"

	"github.com/loomx-ai/steward/internal/app/governance"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
)

const desktopVirtualizationSource = "azure:desktop-virtualization"

// An application group names its host pool in hostPoolArmPath, and a host pool
// with application groups cannot be deleted. The groups hold only published
// application and desktop assignments, so the host pool's deletion selects
// them; their session hosts are separate prerequisites of the host pool.
func contributeDesktopVirtualization(assets []asset.Asset, result *governance.Contribution) {
	pools := map[string][]asset.Asset{}
	for _, value := range assets {
		if value.Identity.Provider == asset.ProviderAzure && value.ClosedAt == nil && strings.EqualFold(value.Identity.NativeType, avdHostPoolType) {
			key := string(value.Identity.ConnectionID) + "\x00" + strings.ToLower(value.Identity.NativeID)
			pools[key] = append(pools[key], value)
		}
	}
	for _, group := range assets {
		if group.Identity.Provider != asset.ProviderAzure || group.ClosedAt != nil || !strings.EqualFold(group.Identity.NativeType, avdApplicationGroupType) {
			continue
		}
		path := strings.TrimSpace(text(object(group.Normalized["properties"])["hostPoolArmPath"]))
		if path == "" {
			path = strings.TrimSpace(text(group.Normalized["hostPoolArmPath"]))
		}
		id, kind, err := parseID(path)
		if err != nil || !strings.EqualFold(kind, avdHostPoolType) {
			continue
		}
		matched := pools[string(group.Identity.ConnectionID)+"\x00"+strings.ToLower(id)]
		if len(matched) != 1 {
			continue // The host pool is outside the scan, so it is not deleted.
		}
		result.Relationships = append(result.Relationships, graph.Relationship{
			SourceAssetID: matched[0].ID, TargetAssetID: group.ID, Type: graph.RelationshipDependsOn, Source: desktopVirtualizationSource, Confidence: 1,
			Evidence: map[string]any{
				graph.RelationshipEvidenceRequiredDeletion: true, graph.RelationshipEvidenceAutomaticSelection: true,
				graph.RelationshipEvidenceAuthority: graph.AuthorityAuthoritative, graph.RelationshipEvidenceDeletionOrder: graph.DeletionOrderTargetBeforeSource,
				"resource_type": avdApplicationGroupType, "instance_id": group.Identity.NativeID, "host_pool_arm_path": path,
			},
		})
	}
}

// logAnalyticsTableCreator is the native schema.tableType: Microsoft for
// built-in tables, CustomLog, RestoredLogs or SearchResults otherwise.
func logAnalyticsTableCreator(raw map[string]any) string {
	return text(object(object(raw["properties"])["schema"])["tableType"])
}
