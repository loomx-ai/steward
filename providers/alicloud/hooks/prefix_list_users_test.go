package hooks

import (
	"context"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
	"github.com/loomx-ai/steward/providers/alicloud"
)

func TestPrefixListUsersAreDeletedBeforeTheList(t *testing.T) {
	scoped := func(id, nativeType, nativeID string, normalized map[string]any) asset.Asset {
		return asset.Asset{ID: asset.AssetID(id), Identity: asset.Identity{Provider: asset.ProviderAliCloud, ConnectionID: "connection-a", NativeType: nativeType, NativeID: nativeID}, Location: "cn-hangzhou", Normalized: normalized}
	}
	assets := []asset.Asset{
		scoped("ecs-list", alicloud.ECSPrefixListNativeType, "pl-ecs", map[string]any{alicloud.NormalizedPrefixListAssociationsField: []any{
			map[string]any{"resourceId": "sg-a", "resourceType": "securitygroup"},
			map[string]any{"resourceId": "sg-unscanned", "resourceType": "securitygroup"},
		}}),
		scoped("vpc-list", alicloud.VPCPrefixListNativeType, "pl-vpc", map[string]any{alicloud.NormalizedPrefixListAssociationsField: []any{
			map[string]any{"resourceId": "vtb-a", "resourceType": "vpcRouteTable"},
		}}),
		scoped("acl", alicloud.ALBAclNativeType, "acl-a", map[string]any{alicloud.NormalizedPrefixListAssociationsField: []any{
			map[string]any{"resourceId": "lsn-a", "resourceType": "listener"},
		}}),
		scoped("listener", "ACS::ALB::Listener", "lsn-a", nil),
		scoped("group", securityGroupNativeType, "sg-a", nil),
		scoped("table", routeTableNativeType, "vtb-a", nil),
	}
	contribution, err := NewPrefixListUsers().Contribute(context.Background(), "scope", assets)
	if err != nil {
		t.Fatal(err)
	}
	edges := map[string]bool{}
	for _, relationship := range contribution.Relationships {
		if relationship.Type != graph.RelationshipUses {
			t.Fatalf("relationship = %+v", relationship)
		}
		edges[string(relationship.SourceAssetID)+">"+string(relationship.TargetAssetID)] = true
	}
	if len(edges) != 3 || !edges["group>ecs-list"] || !edges["table>vpc-list"] || !edges["listener>acl"] {
		t.Fatalf("edges = %v", edges)
	}
}
