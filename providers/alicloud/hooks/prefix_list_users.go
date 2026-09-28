package hooks

import (
	"context"
	"strings"

	"github.com/loomx-ai/steward/internal/app/governance"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
	"github.com/loomx-ai/steward/providers/alicloud"
)

const prefixListUsersEvidence = "alicloud:prefix-list-associations"

// prefixListUserTypes maps the association resource types each prefix list
// API reports to the native types that reference the list.
var prefixListUserTypes = map[string]map[string]string{
	alicloud.ECSPrefixListNativeType: {"securitygroup": securityGroupNativeType},
	// Transit router route tables are ordered by the CEN topology from their
	// own prefix list associations.
	alicloud.VPCPrefixListNativeType: {"vpcroutetable": routeTableNativeType},
}

// PrefixListUsers orders the security groups and route tables that reference
// a prefix list before the list, which cannot be deleted while referenced.
type PrefixListUsers struct{}

func NewPrefixListUsers() *PrefixListUsers { return &PrefixListUsers{} }

func (*PrefixListUsers) Contribute(_ context.Context, _ asset.ScopeID, assets []asset.Asset) (governance.Contribution, error) {
	result := governance.Contribution{}
	for _, list := range assets {
		types, ok := prefixListUserTypes[list.Identity.NativeType]
		if list.Identity.Provider != asset.ProviderAliCloud || !ok {
			continue
		}
		associations, _ := list.Normalized[alicloud.NormalizedPrefixListAssociationsField].([]any)
		for _, raw := range associations {
			association, _ := raw.(map[string]any)
			userType := types[strings.ToLower(normalizedScalar(association["resourceType"]))]
			userID := normalizedScalar(association["resourceId"])
			if userType == "" || userID == "" {
				continue
			}
			user, found := resolveScopedAsset(list, userType, userID, assets)
			if !found {
				continue // Not scanned, so not deleted; the list deletion stays blocked by it.
			}
			result.Relationships = append(result.Relationships, graph.Relationship{
				SourceAssetID: user.ID, TargetAssetID: list.ID, Type: graph.RelationshipUses,
				Source: prefixListUsersEvidence, Confidence: 1,
				Evidence: map[string]any{"source": prefixListUsersEvidence, "prefix_list_id": list.Identity.NativeID, "resource_id": userID},
			})
		}
	}
	return result, nil
}
