package hooks_test

import (
	"context"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
	"github.com/loomx-ai/steward/providers/alicloud/hooks"
)

func TestConfigurationTopologyContributesReviewedRelationships(t *testing.T) {
	t.Parallel()

	assets := []asset.Asset{
		configurationTopologyAsset("slb", "ACS::SLB::LoadBalancer", "lb-a", "cn-hangzhou", map[string]any{
			"BackendServers": map[string]any{"BackendServer": []any{
				map[string]any{"ServerId": "eni-a"},
				map[string]any{"ServerId": "i-a"},
			}},
		}),
		configurationTopologyAsset("eni", "ACS::ECS::NetworkInterface", "eni-a", "cn-hangzhou", nil),
		configurationTopologyAsset("ecs", "ACS::ECS::Instance", "i-a", "cn-hangzhou", nil),
		configurationTopologyAsset("zone", "ACS::PrivateZone::Zone", "zone-a", "global", map[string]any{
			"BindVpcs": map[string]any{"Vpc": []any{
				map[string]any{"VpcId": "vpc-a"},
			}},
		}),
		configurationTopologyAsset("vpc", "ACS::VPC::VPC", "vpc-a", "cn-shanghai", nil),
		configurationTopologyAsset("peer", "ACS::VPC::PeerConnection", "pcc-a", "cn-hangzhou", map[string]any{
			"VpcId": "vpc-requester", "AcceptingVpcId": "vpc-a",
		}),
		configurationTopologyAsset("vswitch", "ACS::VPC::VSwitch", "vsw-a", "cn-hangzhou", map[string]any{
			"RouteTable": map[string]any{"RouteTableId": "vtb-a"},
		}),
		configurationTopologyAsset("route-table", "ACS::VPC::RouteTable", "vtb-a", "cn-hangzhou", map[string]any{
			"RouteEntrys": map[string]any{"RouteEntry": []any{
				map[string]any{"InstanceId": "ngw-a"},
			}},
		}),
		configurationTopologyAsset("nat", "ACS::NAT::NatGateway", "ngw-a", "cn-hangzhou", nil),
		configurationTopologyAsset("snapshot", "ACS::ECS::Snapshot", "s-copy", "cn-shanghai", map[string]any{
			"SourceSnapshotId": "s-source",
			"KMSKeyId":         "key-a",
		}),
		configurationTopologyAsset("source-snapshot", "ACS::ECS::Snapshot", "s-source", "cn-hangzhou", nil),
		configurationTopologyAsset("kms", "ACS::KMS::Key", "key-a", "cn-hangzhou", nil),
	}
	productPeer := configurationTopologyAsset(
		"product-peer",
		"ACS::VPC::PeerConnection",
		"pcc-product",
		"cn-hangzhou",
		nil,
	)
	productPeer.Normalized["acceptingVpcId"] = "vpc-a"
	assets = append(assets, productPeer)

	contribution, err := hooks.NewConfigurationTopology().Contribute(
		context.Background(),
		"scope-a",
		assets,
	)
	if err != nil {
		t.Fatalf("contribute configuration topology: %v", err)
	}
	want := map[string]graph.RelationshipType{
		"slb|eni":                  graph.RelationshipUses,
		"slb|ecs":                  graph.RelationshipUses,
		"zone|vpc":                 graph.RelationshipConnectedTo,
		"peer|vpc":                 graph.RelationshipMemberOf,
		"product-peer|vpc":         graph.RelationshipMemberOf,
		"vswitch|route-table":      graph.RelationshipConnectedTo,
		"route-table|nat":          graph.RelationshipConnectedTo,
		"snapshot|source-snapshot": graph.RelationshipCreatedFrom,
		"snapshot|kms":             graph.RelationshipUses,
	}
	if len(contribution.Relationships) != len(want) ||
		len(contribution.Bindings) != 0 ||
		len(contribution.Unresolved) != 0 {
		t.Fatalf("configuration topology contribution = %+v", contribution)
	}
	for _, relationship := range contribution.Relationships {
		key := string(relationship.SourceAssetID) + "|" + string(relationship.TargetAssetID)
		if expected, found := want[key]; !found ||
			relationship.Type != expected ||
			relationship.Source != "resource-center:configuration-topology" ||
			relationship.Confidence != 1 {
			t.Errorf("relationship %q = %+v, expected=%q found=%t", key, relationship, expected, found)
		}
	}
}

func TestConfigurationTopologyRejectsAmbiguousCrossScopeAndSelfReferences(t *testing.T) {
	t.Parallel()

	assets := []asset.Asset{
		configurationTopologyAsset("snapshot", "ACS::ECS::Snapshot", "s-a", "cn-shanghai", map[string]any{
			"SourceSnapshotId": "s-a",
		}),
		configurationTopologyAsset("zone", "ACS::PrivateZone::Zone", "zone-a", "global", map[string]any{
			"VpcId": "vpc-a",
		}),
		configurationTopologyAsset("vpc-hangzhou", "ACS::VPC::VPC", "vpc-a", "cn-hangzhou", nil),
		configurationTopologyAsset("vpc-shanghai", "ACS::VPC::VPC", "vpc-a", "cn-shanghai", nil),
	}

	contribution, err := hooks.NewConfigurationTopology().Contribute(
		context.Background(),
		"scope-a",
		assets,
	)
	if err != nil {
		t.Fatalf("contribute ambiguous configuration topology: %v", err)
	}
	if len(contribution.Relationships) != 0 {
		t.Fatalf("ambiguous or self relationships were contributed: %+v", contribution.Relationships)
	}
}

func configurationTopologyAsset(
	id asset.AssetID,
	nativeType string,
	nativeID string,
	location string,
	configuration map[string]any,
) asset.Asset {
	scopeKey := "region:" + location
	if location == "global" {
		scopeKey = "global"
	}
	normalized := map[string]any{}
	if configuration != nil {
		normalized["configuration"] = configuration
	}
	return asset.Asset{
		ID: id,
		Identity: asset.Identity{
			Provider: asset.ProviderAliCloud, Partition: "public",
			ConnectionID: "connection-a", NativeType: nativeType,
			NativeID: nativeID, ScopeKey: scopeKey,
		},
		ScopeID: asset.ScopeID("scope-" + location), Location: location, Normalized: normalized,
	}
}
