package hooks_test

import (
	"context"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/providers/alicloud"
	"github.com/loomx-ai/steward/providers/alicloud/hooks"
)

func TestPrivateLinkEndpointsOwnManagedENIsAndOrderVPCDeletion(t *testing.T) {
	t.Parallel()

	endpoint := privateLinkAsset(
		"endpoint",
		"ACS::PrivateLink::VpcEndpoint",
		"ep-2vcr8878a2d95456ad5b",
		map[string]any{
			alicloud.NormalizedPrivateLinkENIIDsField: []any{
				"eni-2vcctvm3a93n1th0rpp6",
				"eni-2vcf7po5ka8sz3r6n3ep",
			},
			alicloud.NormalizedPrivateLinkEndpointZonesField: []any{
				map[string]any{
					"zone_id": "cn-chengdu-a", "zone_status": "Connected",
					"vswitch_id": "vsw-a", "eni_id": "eni-2vcctvm3a93n1th0rpp6",
				},
				map[string]any{
					"zone_id": "cn-chengdu-b", "zone_status": "Connected",
					"service_status": "Normal", "vswitch_id": "vsw-b",
					"eni_id":     "eni-2vcf7po5ka8sz3r6n3ep",
					"request_id": "019FD66C-692D-5A1A-857E-526902E8AB91",
				},
			},
		},
	)
	first := privateLinkAsset(
		"eni-first",
		"ACS::ECS::NetworkInterface",
		"eni-2vcctvm3a93n1th0rpp6",
		map[string]any{"vpc_id": "vpc-a", "vswitch_id": "vsw-a"},
	)
	second := privateLinkAsset(
		"eni-second",
		"ACS::ECS::NetworkInterface",
		"eni-2vcf7po5ka8sz3r6n3ep",
		map[string]any{"vpc_id": "vpc-a", "vswitch_id": "vsw-b"},
	)
	systemSecurityGroup := privateLinkAsset(
		"sg-system",
		"ACS::ECS::SecurityGroup",
		"sg-system-a",
		map[string]any{
			"name":                                 "privatelink_system_security_group." + endpoint.Identity.NativeID,
			alicloud.NormalizedServiceManagedField: true,
			alicloud.NormalizedServiceIDField:      "1341752557108666",
		},
	)
	systemSecurityGroup.Name = "privatelink_system_security_group." + endpoint.Identity.NativeID
	userSecurityGroup := privateLinkAsset(
		"sg-user",
		"ACS::ECS::SecurityGroup",
		"sg-user-a",
		map[string]any{
			"name":                                 "application-security-group",
			alicloud.NormalizedServiceManagedField: false,
		},
	)
	vpc := privateLinkAsset("vpc", "ACS::VPC::VPC", "vpc-a", map[string]any{"vpc_id": "vpc-a"})
	assets := []asset.Asset{vpc, userSecurityGroup, second, endpoint, systemSecurityGroup, first}

	contribution, err := hooks.NewPrivateLinkEndpoints().Contribute(
		context.Background(),
		"scope-chengdu",
		assets,
	)
	if err != nil {
		t.Fatalf("build PrivateLink endpoint topology: %v", err)
	}
	if len(contribution.Unresolved) != 0 || len(contribution.Relationships) != 3 ||
		len(contribution.Bindings) != 3 {
		t.Fatalf("PrivateLink endpoint contribution = %+v", contribution)
	}
	bindings := make(map[asset.AssetID]graph.LifecycleBinding)
	for _, binding := range contribution.Bindings {
		bindings[binding.ManagedAssetID] = binding
	}
	for _, eni := range []asset.Asset{first, second} {
		binding, exists := bindings[eni.ID]
		if !exists || binding.ControllerAssetID != endpoint.ID ||
			binding.Authority != graph.AuthorityAuthoritative ||
			binding.Ownership != graph.OwnershipExclusive ||
			binding.CleanupPolicy != graph.CleanupDelegate ||
			binding.DirectCleanupAllowed ||
			binding.Evidence["delete_by_default"] != true ||
			binding.Evidence[graph.LifecycleEvidenceControllerDeleteGuaranteed] != true {
			t.Fatalf("PrivateLink ENI binding %s = %+v", eni.ID, binding)
		}
	}
	securityGroupBinding, exists := bindings[systemSecurityGroup.ID]
	if !exists || securityGroupBinding.ControllerAssetID != endpoint.ID ||
		securityGroupBinding.CleanupPolicy != graph.CleanupDelegate ||
		securityGroupBinding.DirectCleanupAllowed ||
		securityGroupBinding.Evidence["lifecycle_kind"] != "privatelink_system_security_group" ||
		securityGroupBinding.Evidence[graph.LifecycleEvidenceWaitTimeoutSeconds] != 30 {
		t.Fatalf("PrivateLink system security group binding = %+v", securityGroupBinding)
	}
	if _, exists := bindings[userSecurityGroup.ID]; exists {
		t.Fatalf("user security group unexpectedly managed = %+v", bindings[userSecurityGroup.ID])
	}
	if contribution.Relationships[0].Type != graph.RelationshipConnectedTo ||
		contribution.Relationships[0].TargetAssetID != endpoint.ID {
		t.Fatalf("PrivateLink ENI relationship = %+v", contribution.Relationships[0])
	}

	relationships := append([]graph.Relationship(nil), contribution.Relationships...)
	for index, eni := range []asset.Asset{first, second} {
		relationships = append(relationships, graph.Relationship{
			ID:            graph.RelationshipID("eni-vpc-" + string(rune('a'+index))),
			SourceAssetID: eni.ID, TargetAssetID: vpc.ID,
			Type: graph.RelationshipMemberOf, Source: "product_api", Confidence: 1,
		})
	}
	result, err := plan.Solve(plan.Input{
		CleanupTaskID: "cleanup-privatelink",
		ResolvedAssetIDs: []asset.AssetID{
			endpoint.ID, first.ID, second.ID, systemSecurityGroup.ID, vpc.ID,
		},
		Assets: assets, Relationships: relationships,
		LifecycleBindings: contribution.Bindings,
	})
	if err != nil || len(result.Blockers) != 0 || len(result.Steps) != 5 ||
		len(result.ImpactItems) != 3 {
		t.Fatalf("PrivateLink endpoint cleanup plan = %+v, err=%v", result, err)
	}
	controller := requireCleanupStepForAsset(t, result.Steps, endpoint.ID)
	vpcStep := requireCleanupStepForAsset(t, result.Steps, vpc.ID)
	requiredVPCDependencies := map[plan.StepID]bool{}
	for _, managedID := range []asset.AssetID{first.ID, second.ID, systemSecurityGroup.ID} {
		verification := requireCleanupStepForAsset(t, result.Steps, managedID)
		if verification.Kind != plan.StepVerification ||
			verification.Action != plan.ActionVerifyManagedAbsent ||
			len(verification.DependsOn) != 1 ||
			verification.DependsOn[0] != controller.ID {
			t.Fatalf(
				"PrivateLink managed verification %s: controller=%+v verification=%+v",
				managedID,
				controller,
				verification,
			)
		}
		if managedID == first.ID || managedID == second.ID {
			requiredVPCDependencies[verification.ID] = true
		}
	}
	for _, dependency := range vpcStep.DependsOn {
		delete(requiredVPCDependencies, dependency)
	}
	if controller.Kind != plan.StepController || len(requiredVPCDependencies) != 0 {
		t.Fatalf(
			"PrivateLink controller=%+v vpc=%+v missing dependencies=%+v",
			controller,
			vpcStep,
			requiredVPCDependencies,
		)
	}

	direct, err := plan.Solve(plan.Input{
		ResolvedAssetIDs:  []asset.AssetID{first.ID},
		Assets:            assets,
		Relationships:     relationships,
		LifecycleBindings: contribution.Bindings,
	})
	if err != nil || len(direct.Steps) != 0 || len(direct.Blockers) != 1 ||
		direct.Blockers[0].Code != plan.BlockManagedByController ||
		direct.Blockers[0].ControllerID != endpoint.ID {
		t.Fatalf("direct PrivateLink ENI cleanup plan = %+v, err=%v", direct, err)
	}
}

func privateLinkAsset(
	id asset.AssetID,
	nativeType string,
	nativeID string,
	normalized map[string]any,
) asset.Asset {
	return asset.Asset{
		ID: id, ScopeID: "scope-chengdu", Location: "cn-chengdu",
		ResourceKindID: asset.ResourceKindID(nativeType),
		Capabilities:   asset.CapabilitySet{asset.CapabilityIndexed, asset.CapabilityActionable},
		Identity: asset.Identity{
			Provider: asset.ProviderAliCloud, Partition: "public", ConnectionID: "connection-a",
			ScopeKey: "region:cn-chengdu", NativeType: nativeType, NativeID: nativeID,
		},
		Normalized: normalized,
	}
}
