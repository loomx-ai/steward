package hooks_test

import (
	"context"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
	"github.com/loomx-ai/steward/providers/alicloud"
	"github.com/loomx-ai/steward/providers/alicloud/hooks"
)

func TestServiceManagedNetworksContributesNLB_NATAndECILifecycle(t *testing.T) {
	t.Parallel()

	assets := []asset.Asset{
		serviceManagedNetworkAsset("nlb", "ACS::NLB::LoadBalancer", "nlb-a", map[string]any{
			"configuration": map[string]any{"ZoneMappings": []any{
				map[string]any{"EniId": []any{"eni-nlb"}},
			}},
		}),
		serviceManagedNetworkAsset("nat", "ACS::NAT::NatGateway", "ngw-a", map[string]any{
			"configuration": map[string]any{
				"NatGatewayPrivateInfo": map[string]any{"EniInstanceId": "eni-nat"},
			},
		}),
		serviceManagedNetworkAsset("eci", "ACS::ECI::ContainerGroup", "eci-a", map[string]any{
			"configuration": map[string]any{"EniInstanceId": "eni-eci"},
		}),
		serviceManagedNetworkAsset("eni-nlb", "ACS::ECS::NetworkInterface", "eni-nlb", map[string]any{
			alicloud.NormalizedServiceManagedField: true,
		}),
		serviceManagedNetworkAsset("eni-nat", "ACS::ECS::NetworkInterface", "eni-nat", map[string]any{
			"configuration":                          map[string]any{"ServiceManaged": true},
			alicloud.NormalizedServiceManagedField:   true,
			alicloud.NormalizedServiceIDField:        "1679259531804325",
			alicloud.NormalizedSecurityGroupIDsField: []any{"sg-nat"},
		}),
		serviceManagedNetworkAsset("sg-nat", "ACS::ECS::SecurityGroup", "sg-nat", map[string]any{
			alicloud.NormalizedServiceManagedField: true,
			alicloud.NormalizedServiceIDField:      "1679259531804325",
		}),
		serviceManagedNetworkAsset("eni-eci", "ACS::ECS::NetworkInterface", "eni-eci", map[string]any{
			"configuration": map[string]any{
				"InstanceId": "eci-a", "DeleteOnRelease": true,
			},
		}),
		serviceManagedNetworkAsset("disk-eci", "ACS::ECS::Disk", "d-eci", map[string]any{
			"attached_instance_id": "eci-a",
			"configuration": map[string]any{
				"DeleteWithInstance": true, "Type": "data",
			},
		}),
	}

	contribution, err := hooks.NewServiceManagedNetworks().Contribute(
		context.Background(),
		"scope-a",
		assets,
	)
	if err != nil {
		t.Fatalf("contribute service-managed networks: %v", err)
	}
	if len(contribution.Unresolved) != 0 ||
		len(contribution.Relationships) != 5 ||
		len(contribution.Bindings) != 5 {
		t.Fatalf("service-managed contribution = %+v", contribution)
	}

	relationships := make(map[string]graph.Relationship)
	for _, relationship := range contribution.Relationships {
		relationships[string(relationship.SourceAssetID)+"|"+string(relationship.TargetAssetID)] =
			relationship
	}
	for _, expected := range []struct {
		key              string
		relationshipType graph.RelationshipType
	}{
		{key: "eni-nlb|nlb", relationshipType: graph.RelationshipConnectedTo},
		{key: "eni-nat|nat", relationshipType: graph.RelationshipConnectedTo},
		{key: "sg-nat|nat", relationshipType: graph.RelationshipConnectedTo},
		{key: "eni-eci|eci", relationshipType: graph.RelationshipConnectedTo},
		{key: "disk-eci|eci", relationshipType: graph.RelationshipAttachedTo},
	} {
		relationship, found := relationships[expected.key]
		if !found || relationship.Type != expected.relationshipType ||
			relationship.Confidence != 1 {
			t.Errorf("relationship %q = %+v, found=%t", expected.key, relationship, found)
		}
	}
	for _, binding := range contribution.Bindings {
		if binding.Authority != graph.AuthorityAuthoritative ||
			binding.Ownership != graph.OwnershipExclusive ||
			binding.CleanupPolicy != graph.CleanupDelegate ||
			binding.DirectCleanupAllowed {
			t.Errorf("binding = %+v", binding)
		}
	}
	managedSecurityGroupBinding := contribution.Bindings[0]
	for _, binding := range contribution.Bindings {
		if binding.ManagedAssetID == "sg-nat" {
			managedSecurityGroupBinding = binding
			break
		}
	}
	if managedSecurityGroupBinding.ManagedAssetID != "sg-nat" ||
		managedSecurityGroupBinding.ControllerAssetID != "nat" ||
		managedSecurityGroupBinding.EvidenceSource != "nat:service-managed-security-group" ||
		managedSecurityGroupBinding.Evidence["lifecycle_kind"] != "nat_service_managed_security_group" ||
		managedSecurityGroupBinding.Evidence["service_id"] != "1679259531804325" {
		t.Errorf("NAT-managed security group binding = %+v", managedSecurityGroupBinding)
	}
}

func TestServiceManagedNetworksDoesNotClaimAmbiguousNATSecurityGroup(t *testing.T) {
	t.Parallel()

	assets := []asset.Asset{
		serviceManagedNetworkAsset("nat-a", "ACS::NAT::NatGateway", "ngw-a", map[string]any{
			"configuration": map[string]any{"EniInstanceId": "eni-a"},
		}),
		serviceManagedNetworkAsset("nat-b", "ACS::NAT::NatGateway", "ngw-b", map[string]any{
			"configuration": map[string]any{"EniInstanceId": "eni-b"},
		}),
		serviceManagedNetworkAsset("eni-a", "ACS::ECS::NetworkInterface", "eni-a", map[string]any{
			alicloud.NormalizedServiceManagedField:   true,
			alicloud.NormalizedSecurityGroupIDsField: []any{"sg-shared"},
		}),
		serviceManagedNetworkAsset("eni-b", "ACS::ECS::NetworkInterface", "eni-b", map[string]any{
			alicloud.NormalizedServiceManagedField:   true,
			alicloud.NormalizedSecurityGroupIDsField: []any{"sg-shared"},
		}),
		serviceManagedNetworkAsset("sg-shared", "ACS::ECS::SecurityGroup", "sg-shared", map[string]any{
			alicloud.NormalizedServiceManagedField: true,
		}),
	}

	contribution, err := hooks.NewServiceManagedNetworks().Contribute(
		context.Background(),
		"scope-a",
		assets,
	)
	if err != nil {
		t.Fatalf("contribute ambiguous NAT security group: %v", err)
	}
	for _, binding := range contribution.Bindings {
		if binding.ManagedAssetID == "sg-shared" {
			t.Fatalf("ambiguous NAT security group binding = %+v", binding)
		}
	}
}

func TestServiceManagedNetworksRejectsUnmanagedNetworkInterface(t *testing.T) {
	t.Parallel()

	contribution, err := hooks.NewServiceManagedNetworks().Contribute(
		context.Background(),
		"scope-a",
		[]asset.Asset{
			serviceManagedNetworkAsset("nlb", "ACS::NLB::LoadBalancer", "nlb-a", map[string]any{
				"configuration": map[string]any{
					"ZoneMappings": []any{map[string]any{"EniId": []any{"eni-a"}}},
				},
			}),
			serviceManagedNetworkAsset("eni", "ACS::ECS::NetworkInterface", "eni-a", map[string]any{
				"configuration": map[string]any{"ServiceManaged": false},
			}),
		},
	)
	if err != nil {
		t.Fatalf("contribute unmanaged network: %v", err)
	}
	if len(contribution.Relationships) != 0 ||
		len(contribution.Bindings) != 0 ||
		len(contribution.Unresolved) != 0 {
		t.Fatalf("unmanaged contribution = %+v", contribution)
	}
}

func serviceManagedNetworkAsset(
	id asset.AssetID,
	nativeType string,
	nativeID string,
	normalized map[string]any,
) asset.Asset {
	return asset.Asset{
		ID: id,
		Identity: asset.Identity{
			Provider: asset.ProviderAliCloud, Partition: "public",
			ConnectionID: "connection-a", NativeType: nativeType,
			NativeID: nativeID, ScopeKey: "region:cn-hangzhou",
		},
		ScopeID: "scope-a", Location: "cn-hangzhou", Normalized: normalized,
	}
}
