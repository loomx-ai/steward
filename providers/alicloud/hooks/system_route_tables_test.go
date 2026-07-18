package hooks_test

import (
	"context"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
	"github.com/loomx-ai/steward/providers/alicloud/hooks"
)

func TestSystemRouteTablesContributeDelegatedVPCLifecycle(t *testing.T) {
	t.Parallel()

	vpc := routeTableAsset("vpc", "ACS::VPC::VPC", "vpc-a")
	system := routeTableAsset("system", "ACS::VPC::RouteTable", "vtb-system")
	system.Normalized = map[string]any{"configuration": map[string]any{
		"RouteTableType": "System", "VpcId": "vpc-a",
	}}
	custom := routeTableAsset("custom", "ACS::VPC::RouteTable", "vtb-custom")
	custom.Normalized = map[string]any{"routeTableType": "Custom", "vpcId": "vpc-a"}
	orphan := routeTableAsset("orphan", "ACS::VPC::RouteTable", "vtb-orphan")
	orphan.Normalized = map[string]any{"routeTableType": "System", "vpcId": "vpc-missing"}

	contribution, err := hooks.NewSystemRouteTables().Contribute(
		context.Background(), "scope", []asset.Asset{custom, orphan, system, vpc},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(contribution.Bindings) != 1 || len(contribution.Relationships) != 1 || len(contribution.Unresolved) != 0 {
		t.Fatalf("contribution = %+v", contribution)
	}
	binding := contribution.Bindings[0]
	if binding.ControllerAssetID != vpc.ID ||
		binding.ManagedAssetID != system.ID ||
		binding.Authority != graph.AuthorityAuthoritative ||
		binding.Ownership != graph.OwnershipExclusive ||
		binding.CleanupPolicy != graph.CleanupDelegate ||
		binding.DirectCleanupAllowed ||
		binding.EvidenceSource != "vpc:system-route-table" ||
		binding.Evidence["lifecycle_kind"] != "vpc_system_route_table" ||
		binding.Evidence[graph.LifecycleEvidenceControllerDeleteGuaranteed] != true ||
		binding.Evidence[graph.LifecycleEvidenceControllerIntegratedResource] != true ||
		binding.Confidence != 1 {
		t.Fatalf("binding = %+v", binding)
	}
	relationship := contribution.Relationships[0]
	if relationship.SourceAssetID != system.ID ||
		relationship.TargetAssetID != vpc.ID ||
		relationship.Type != graph.RelationshipMemberOf {
		t.Fatalf("relationship = %+v", relationship)
	}
}

func routeTableAsset(id asset.AssetID, nativeType, nativeID string) asset.Asset {
	return asset.Asset{
		ID: id, ScopeID: "scope", Location: "cn-hangzhou",
		Identity: asset.Identity{
			Provider: asset.ProviderAliCloud, Partition: "aliyun", ConnectionID: "connection",
			NativeType: nativeType, NativeID: nativeID,
		},
	}
}
