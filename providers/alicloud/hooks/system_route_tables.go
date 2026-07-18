package hooks

import (
	"context"
	"strings"

	"github.com/loomx-ai/steward/internal/app/governance"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
)

const (
	systemRouteTableEvidence = "vpc:system-route-table"
	systemRouteTableKind     = "vpc_system_route_table"
	routeTableNativeType     = "ACS::VPC::RouteTable"
	vpcNativeType            = "ACS::VPC::VPC"
)

// SystemRouteTables derives the provider-owned lifecycle relationship between
// a VPC and its system route table from inventory configuration.
type SystemRouteTables struct{}

func NewSystemRouteTables() *SystemRouteTables {
	return &SystemRouteTables{}
}

func (*SystemRouteTables) Contribute(_ context.Context, _ asset.ScopeID, assets []asset.Asset) (governance.Contribution, error) {
	byIdentity := make(map[string]asset.Asset, len(assets))
	for _, value := range assets {
		byIdentity[value.Identity.Key()] = value
	}

	result := governance.Contribution{}
	for _, routeTable := range assets {
		if routeTable.Identity.Provider != asset.ProviderAliCloud ||
			routeTable.Identity.NativeType != routeTableNativeType ||
			!strings.EqualFold(routeTableValue(routeTable, "routeTableType", "route_table_type", "RouteTableType"), "System") {
			continue
		}
		vpcID := routeTableValue(routeTable, "vpcId", "vpc_id", "VpcId")
		if vpcID == "" {
			continue
		}
		vpcIdentity := asset.Identity{
			Provider: routeTable.Identity.Provider, Partition: routeTable.Identity.Partition,
			ConnectionID: routeTable.Identity.ConnectionID, NativeType: vpcNativeType, NativeID: vpcID,
			ScopeKey: routeTable.Identity.ScopeKey,
		}
		vpc, found := byIdentity[vpcIdentity.Key()]
		if !found {
			continue
		}
		evidence := map[string]any{
			"source": systemRouteTableEvidence, "lifecycle_kind": systemRouteTableKind,
			"route_table_type": "System", "route_table_id": routeTable.Identity.NativeID,
			"vpc_id": vpcID, "delete_by_default": true,
			graph.LifecycleEvidenceControllerDeleteGuaranteed:   true,
			graph.LifecycleEvidenceControllerIntegratedResource: true,
		}
		result.Relationships = append(result.Relationships, graph.Relationship{
			SourceAssetID: routeTable.ID, TargetAssetID: vpc.ID, Type: graph.RelationshipMemberOf,
			Source: systemRouteTableEvidence, Evidence: evidence, Confidence: 1,
		})
		result.Bindings = append(result.Bindings, graph.LifecycleBinding{
			ControllerAssetID: vpc.ID, ManagedAssetID: routeTable.ID,
			Authority: graph.AuthorityAuthoritative, Ownership: graph.OwnershipExclusive,
			CleanupPolicy: graph.CleanupDelegate, DirectCleanupAllowed: false,
			EvidenceSource: systemRouteTableEvidence, Evidence: evidence, Confidence: 1,
		})
	}
	return result, nil
}

func routeTableValue(value asset.Asset, normalizedKeys ...string) string {
	for _, key := range normalizedKeys {
		if text := stringField(value.Normalized, key); text != "" {
			return text
		}
	}
	configuration, _ := value.Normalized["configuration"].(map[string]any)
	for _, key := range normalizedKeys {
		if text := stringField(configuration, key); text != "" {
			return text
		}
	}
	return ""
}

func stringField(values map[string]any, want string) string {
	for key, value := range values {
		if strings.EqualFold(strings.TrimSpace(key), want) {
			if text, ok := value.(string); ok {
				return strings.TrimSpace(text)
			}
		}
	}
	return ""
}
