package hooks_test

import (
	"context"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
	"github.com/loomx-ai/steward/providers/alicloud"
	"github.com/loomx-ai/steward/providers/alicloud/hooks"
)

func TestARMSEnvironmentsDelegatesUniquePrometheusPrimaryENI(t *testing.T) {
	t.Parallel()

	environment := serviceManagedNetworkAsset(
		"environment", alicloud.ARMSEnvironmentNativeType, "env-a", map[string]any{
			"environmentType": "ECS", "bindResourceType": "VPC",
			"vpcId": "vpc-a", "vSwitchId": "vsw-a", "securityGroupId": "sg-a",
		},
	)
	primary := serviceManagedNetworkAsset(
		"eni", "ACS::ECS::NetworkInterface", "eni-a", map[string]any{
			"vpc_id": "vpc-a", "vswitch_id": "vsw-a",
			alicloud.NormalizedSecurityGroupIDsField: []any{"sg-a"},
			"configuration": map[string]any{
				"Type": "Primary", "InstanceId": "", "DeleteOnRelease": true,
				"Description": "created by CloudMonitor Prometheus",
			},
		},
	)
	contribution, err := hooks.NewARMSEnvironments().Contribute(
		context.Background(), "scope-a", []asset.Asset{primary, environment},
	)
	if err != nil {
		t.Fatalf("contribute ARMS environment: %v", err)
	}
	if len(contribution.Relationships) != 1 || len(contribution.Bindings) != 1 {
		t.Fatalf("ARMS environment contribution = %+v", contribution)
	}
	binding := contribution.Bindings[0]
	if binding.ControllerAssetID != environment.ID || binding.ManagedAssetID != primary.ID ||
		binding.Authority != graph.AuthorityAuthoritative ||
		binding.Ownership != graph.OwnershipExclusive ||
		binding.CleanupPolicy != graph.CleanupDelegate || binding.DirectCleanupAllowed ||
		binding.Evidence["lifecycle_kind"] != "arms_environment_primary_eni" ||
		binding.Evidence[graph.LifecycleEvidenceWaitUntilAbsentBeforeDependents] != true {
		t.Fatalf("ARMS environment lifecycle binding = %+v", binding)
	}
}

func TestARMSEnvironmentsRejectsAmbiguousOrNonCloudMonitorPrimaryENI(t *testing.T) {
	t.Parallel()

	environment := serviceManagedNetworkAsset(
		"environment", alicloud.ARMSEnvironmentNativeType, "env-a", map[string]any{
			"environmentType": "ECS", "bindResourceType": "VPC",
			"vpcId": "vpc-a", "vSwitchId": "vsw-a", "securityGroupId": "sg-a",
		},
	)
	matching := func(id asset.AssetID, description string) asset.Asset {
		return serviceManagedNetworkAsset(id, "ACS::ECS::NetworkInterface", string(id), map[string]any{
			"vpc_id": "vpc-a", "vswitch_id": "vsw-a",
			alicloud.NormalizedSecurityGroupIDsField: "sg-a",
			"configuration": map[string]any{
				"Type": "Primary", "DeleteOnRelease": true, "Description": description,
			},
		})
	}
	for name, assets := range map[string][]asset.Asset{
		"ambiguous": {
			environment,
			matching("eni-a", "created by CloudMonitor Prometheus"),
			matching("eni-b", "created by CloudMonitor Prometheus"),
		},
		"foreign": {environment, matching("eni-a", "created by another service")},
	} {
		t.Run(name, func(t *testing.T) {
			contribution, err := hooks.NewARMSEnvironments().Contribute(
				context.Background(), "scope-a", assets,
			)
			if err != nil {
				t.Fatalf("contribute ARMS environment: %v", err)
			}
			if len(contribution.Relationships) != 0 || len(contribution.Bindings) != 0 {
				t.Fatalf("unsafe ARMS environment contribution = %+v", contribution)
			}
		})
	}
}
