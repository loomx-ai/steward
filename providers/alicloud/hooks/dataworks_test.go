package hooks_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/providers/alicloud"
	"github.com/loomx-ai/steward/providers/alicloud/hooks"
)

func TestDataWorksBuildsWorkspaceAndManagedNetworkLifecycle(t *testing.T) {
	t.Parallel()

	const (
		resourceGroupID    = "Serverless_res_group_210724245979777_717305919406626"
		securityGroupID    = "sg-d7ob1fsemhw32ptq0jav"
		networkInterfaceID = "eni-d7o0kwoemjg2fgi3wym7"
	)
	assets := []asset.Asset{
		dataWorksAsset("resource-group", alicloud.DataWorksResourceGroupNativeType, resourceGroupID, resourceGroupID, map[string]any{
			alicloud.NormalizedDataWorksProjectIDsField:       []any{"3"},
			alicloud.NormalizedDataWorksProjectRequestIDField: "request-projects",
			alicloud.NormalizedDataWorksNetworksField: []any{map[string]any{
				"network_id": "1000", "resource_group_id": resourceGroupID,
				"vpc_id": "vpc-london", "vswitch_id": "vsw-london",
				"security_group_id": securityGroupID, "status": "Running",
				"request_id": "request-networks",
			}},
		}),
		dataWorksAsset("workspace", alicloud.DataWorksProjectNativeType, "3", "default_workspace_3vtv", nil),
		dataWorksAsset("vpc", "ACS::VPC::VPC", "vpc-london", "london-vpc", map[string]any{"vpc_id": "vpc-london"}),
		dataWorksAsset("vswitch", "ACS::VPC::VSwitch", "vsw-london", "london-vswitch", map[string]any{
			"vpc_id": "vpc-london", "vswitch_id": "vsw-london",
		}),
		dataWorksAsset("security-group", "ACS::ECS::SecurityGroup", securityGroupID, securityGroupID, map[string]any{
			"vpc_id":                               "vpc-london",
			alicloud.NormalizedServiceManagedField: true,
			alicloud.NormalizedServiceIDField:      json.Number("429"),
		}),
		dataWorksAsset("network-interface", "ACS::ECS::NetworkInterface", networkInterfaceID, networkInterfaceID, map[string]any{
			"vpc_id": "vpc-london", "vswitch_id": "vsw-london",
			alicloud.NormalizedSecurityGroupIDsField: []any{securityGroupID},
			alicloud.NormalizedServiceManagedField:   float64(1),
			alicloud.NormalizedServiceIDField:        json.Number("429"),
		}),
		dataWorksAsset("foreign-service-eni", "ACS::ECS::NetworkInterface", "eni-foreign", "eni-foreign", map[string]any{
			"vpc_id": "vpc-london", "vswitch_id": "vsw-london",
			alicloud.NormalizedSecurityGroupIDsField: []any{securityGroupID},
			alicloud.NormalizedServiceManagedField:   true,
			alicloud.NormalizedServiceIDField:        json.Number("999"),
		}),
	}

	contribution, err := hooks.NewDataWorks().Contribute(context.Background(), "scope-region", assets)
	if err != nil {
		t.Fatalf("build DataWorks topology: %v", err)
	}
	wantRelationships := map[string]struct{}{
		"resource-group|member_of|workspace":         {},
		"resource-group|uses|vpc":                    {},
		"resource-group|uses|vswitch":                {},
		"security-group|member_of|resource-group":    {},
		"network-interface|member_of|resource-group": {},
	}
	gotRelationships := make(map[string]struct{}, len(contribution.Relationships))
	for _, relationship := range contribution.Relationships {
		gotRelationships[string(relationship.SourceAssetID)+"|"+string(relationship.Type)+"|"+string(relationship.TargetAssetID)] = struct{}{}
	}
	if len(gotRelationships) != len(wantRelationships) {
		t.Fatalf("DataWorks relationships = %#v, want %#v", gotRelationships, wantRelationships)
	}
	for key := range wantRelationships {
		if _, exists := gotRelationships[key]; !exists {
			t.Fatalf("missing DataWorks relationship %q in %#v", key, gotRelationships)
		}
	}
	if len(contribution.Unresolved) != 0 {
		t.Fatalf("DataWorks unresolved references = %#v", contribution.Unresolved)
	}
	bindings := make(map[asset.AssetID]graph.LifecycleBinding)
	for _, binding := range contribution.Bindings {
		bindings[binding.ManagedAssetID] = binding
	}
	for _, managedID := range []asset.AssetID{"security-group", "network-interface"} {
		binding, exists := bindings[managedID]
		if !exists ||
			binding.ControllerAssetID != "resource-group" ||
			binding.Authority != graph.AuthorityAuthoritative ||
			binding.Ownership != graph.OwnershipExclusive ||
			binding.CleanupPolicy != graph.CleanupDelegate ||
			binding.Confidence != 1 ||
			binding.Evidence["delete_by_default"] != true ||
			binding.Evidence[graph.LifecycleEvidenceControllerDeleteGuaranteed] != true ||
			binding.Evidence[graph.LifecycleEvidenceWaitUntilAbsentBeforeDependents] != true ||
			binding.Evidence[graph.LifecycleEvidenceWaitTimeoutSeconds] != 300 ||
			binding.Evidence[graph.LifecycleEvidenceWaitPollSeconds] != 15 {
			t.Fatalf("DataWorks lifecycle binding for %s = %#v", managedID, binding)
		}
	}
	if _, exists := bindings["foreign-service-eni"]; exists {
		t.Fatalf("foreign service ENI received DataWorks lifecycle authority: %#v", bindings)
	}

	result, err := plan.Solve(plan.Input{
		ResolvedAssetIDs:  []asset.AssetID{"resource-group", "security-group", "network-interface", "vpc"},
		Assets:            assets,
		Relationships:     contribution.Relationships,
		LifecycleBindings: contribution.Bindings,
	})
	if err != nil {
		t.Fatalf("plan DataWorks cleanup: %v", err)
	}
	if len(result.Blockers) != 0 ||
		len(result.Steps) != 4 ||
		len(result.ImpactItems) != 2 {
		t.Fatalf("DataWorks cleanup plan = %+v", result)
	}
	controller := requireCleanupStepForAsset(t, result.Steps, "resource-group")
	vpcStep := requireCleanupStepForAsset(t, result.Steps, "vpc")
	requiredVPCDependencies := map[plan.StepID]bool{controller.ID: true}
	for _, managedID := range []asset.AssetID{"security-group", "network-interface"} {
		verification := requireCleanupStepForAsset(t, result.Steps, managedID)
		if verification.Kind != plan.StepVerification ||
			verification.Action != plan.ActionVerifyManagedAbsent ||
			len(verification.DependsOn) != 1 ||
			verification.DependsOn[0] != controller.ID {
			t.Fatalf(
				"DataWorks managed verification %s: controller=%+v verification=%+v",
				managedID,
				controller,
				verification,
			)
		}
		requiredVPCDependencies[verification.ID] = true
	}
	for _, dependency := range vpcStep.DependsOn {
		delete(requiredVPCDependencies, dependency)
	}
	if controller.Kind != plan.StepController || len(requiredVPCDependencies) != 0 {
		t.Fatalf(
			"DataWorks controller=%+v vpc=%+v missing dependencies=%+v",
			controller,
			vpcStep,
			requiredVPCDependencies,
		)
	}

	directManaged, err := plan.Solve(plan.Input{
		ResolvedAssetIDs:  []asset.AssetID{"network-interface"},
		Assets:            assets,
		Relationships:     contribution.Relationships,
		LifecycleBindings: contribution.Bindings,
	})
	if err != nil {
		t.Fatalf("plan direct managed ENI cleanup: %v", err)
	}
	if len(directManaged.Blockers) != 1 ||
		directManaged.Blockers[0].Code != plan.BlockManagedByController ||
		directManaged.Blockers[0].ControllerID != "resource-group" ||
		len(directManaged.Steps) != 0 {
		t.Fatalf("direct managed ENI cleanup plan = %+v", directManaged)
	}
}

func dataWorksAsset(
	id asset.AssetID,
	nativeType string,
	nativeID string,
	name string,
	normalized map[string]any,
) asset.Asset {
	capabilities := asset.CapabilitySet{asset.CapabilityIndexed}
	if nativeType == alicloud.DataWorksResourceGroupNativeType ||
		nativeType == "ACS::ECS::SecurityGroup" ||
		nativeType == "ACS::ECS::NetworkInterface" ||
		nativeType == "ACS::VPC::VPC" {
		capabilities = append(capabilities, asset.CapabilityActionable)
	}
	return asset.Asset{
		ID: id,
		Identity: asset.Identity{
			Provider: asset.ProviderAliCloud, Partition: "public", ConnectionID: "connection-a",
			NativeType: nativeType, NativeID: nativeID, ScopeKey: "region:eu-west-1",
		},
		ScopeID: "scope-region", Name: name, Location: "eu-west-1",
		Capabilities: capabilities, Normalized: normalized,
	}
}
