package hooks_test

import (
	"context"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/providers/alicloud/hooks"
)

func TestNATIPsDelegateOnlyDefaultAddressToGateway(t *testing.T) {
	t.Parallel()

	gateway := privateLinkAsset(
		"nat-gateway",
		"ACS::NAT::NatGateway",
		"ngw-a",
		map[string]any{},
	)
	defaultIP := privateLinkAsset(
		"nat-ip-default",
		"ACS::NAT::NatIp",
		"vpcnatip-a",
		map[string]any{"natGatewayId": "ngw-a", "isDefault": true},
	)
	extraIP := privateLinkAsset(
		"nat-ip-extra",
		"ACS::NAT::NatIp",
		"vpcnatip-b",
		map[string]any{"natGatewayId": "ngw-a", "isDefault": false},
	)

	contribution, err := hooks.NewNATIPs().Contribute(
		context.Background(),
		"scope-chengdu",
		[]asset.Asset{extraIP, defaultIP, gateway},
	)
	if err != nil {
		t.Fatalf("build NAT IP lifecycle: %v", err)
	}
	if len(contribution.Unresolved) != 0 || len(contribution.Relationships) != 1 ||
		len(contribution.Bindings) != 1 {
		t.Fatalf("NAT IP contribution = %+v", contribution)
	}
	binding := contribution.Bindings[0]
	if binding.ControllerAssetID != gateway.ID || binding.ManagedAssetID != defaultIP.ID ||
		binding.CleanupPolicy != graph.CleanupDelegate || binding.DirectCleanupAllowed ||
		binding.Evidence["is_default"] != true ||
		binding.Evidence[graph.LifecycleEvidenceControllerDeleteGuaranteed] != true {
		t.Fatalf("default NAT IP binding = %+v", binding)
	}

	result, err := plan.Solve(plan.Input{
		CleanupTaskID:    "cleanup-nat-ips",
		ResolvedAssetIDs: []asset.AssetID{defaultIP.ID, extraIP.ID, gateway.ID},
		Assets:           []asset.Asset{extraIP, defaultIP, gateway},
		Relationships: append(contribution.Relationships, graph.Relationship{
			SourceAssetID: extraIP.ID,
			TargetAssetID: gateway.ID,
			Type:          graph.RelationshipMemberOf,
			Source:        "resource_spec",
			Confidence:    1,
		}),
		LifecycleBindings: contribution.Bindings,
	})
	if err != nil || len(result.Blockers) != 0 || len(result.Steps) != 3 ||
		len(result.ImpactItems) != 1 || result.ImpactItems[0].AssetID != defaultIP.ID {
		t.Fatalf("NAT IP cleanup plan = %+v, err=%v", result, err)
	}
	extraStep := requireCleanupStepForAsset(t, result.Steps, extraIP.ID)
	gatewayStep := requireCleanupStepForAsset(t, result.Steps, gateway.ID)
	verificationStep := requireCleanupStepForAsset(t, result.Steps, defaultIP.ID)
	if len(gatewayStep.DependsOn) != 1 ||
		gatewayStep.DependsOn[0] != extraStep.ID ||
		verificationStep.Kind != plan.StepVerification ||
		verificationStep.Action != plan.ActionVerifyManagedAbsent ||
		len(verificationStep.DependsOn) != 1 ||
		verificationStep.DependsOn[0] != gatewayStep.ID {
		t.Fatalf(
			"NAT IP cleanup steps: extra=%+v gateway=%+v verification=%+v",
			extraStep,
			gatewayStep,
			verificationStep,
		)
	}
}
