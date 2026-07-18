package hooks_test

import (
	"context"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
	"github.com/loomx-ai/steward/internal/core/plan"
	coretopology "github.com/loomx-ai/steward/internal/core/topology"
	"github.com/loomx-ai/steward/providers/alicloud"
	"github.com/loomx-ai/steward/providers/alicloud/hooks"
)

func TestNLBEIPsLinksAssociationsAndDelegatesOnlyProviderManagedEIPs(t *testing.T) {
	t.Parallel()

	loadBalancer := nlbAsset(
		"nlb",
		"ACS::NLB::LoadBalancer",
		"nlb-3zjq7ee4h4dq0ycuca",
		"",
		map[string]any{
			alicloud.NormalizedNLBEIPIDsField: []any{
				"eip-bp1xhxylmy3se94101d88",
				"eip-bp15ip0uqhwivngetljxd",
				"eip-user-supplied",
			},
			"vpc_id": "vpc-a",
		},
	)
	first := nlbAsset(
		"eip-first",
		"ACS::EIP::EipAddress",
		"eip-bp1xhxylmy3se94101d88",
		"CREATE_BY_NLB.nlb-3zjq7ee4h4dq0ycuca",
		map[string]any{"configuration": map[string]any{
			"Name":           "CREATE_BY_NLB.nlb-3zjq7ee4h4dq0ycuca",
			"ServiceManaged": float64(1),
		}},
	)
	second := nlbAsset(
		"eip-second",
		"ACS::EIP::EipAddress",
		"eip-bp15ip0uqhwivngetljxd",
		"CREATE_BY_NLB.nlb-3zjq7ee4h4dq0ycuca",
		map[string]any{"configuration": map[string]any{
			"Description":    "CREATE_BY_NLB.nlb-3zjq7ee4h4dq0ycuca",
			"ServiceManaged": true,
		}},
	)
	userSupplied := nlbAsset(
		"eip-user",
		"ACS::EIP::EipAddress",
		"eip-user-supplied",
		"user-eip",
		map[string]any{"configuration": map[string]any{
			"Name": "user-eip", "ServiceManaged": float64(0),
		}},
	)

	contribution, err := hooks.NewNLBEIPs().Contribute(
		context.Background(),
		"scope-hangzhou",
		[]asset.Asset{userSupplied, second, loadBalancer, first},
	)
	if err != nil {
		t.Fatalf("build NLB EIP topology: %v", err)
	}
	if len(contribution.Relationships) != 3 {
		t.Fatalf("NLB EIP relationships = %+v", contribution.Relationships)
	}
	for _, relationship := range contribution.Relationships {
		if relationship.TargetAssetID != loadBalancer.ID ||
			relationship.Type != graph.RelationshipAttachedTo ||
			relationship.Source != "nlb:allocation" ||
			relationship.Evidence[graph.RelationshipEvidenceDeletionOrder] !=
				graph.DeletionOrderTargetBeforeSource {
			t.Fatalf("NLB EIP relationship = %+v", relationship)
		}
	}
	if len(contribution.Bindings) != 2 {
		t.Fatalf("NLB managed EIP bindings = %+v", contribution.Bindings)
	}
	bound := map[asset.AssetID]graph.LifecycleBinding{}
	for _, binding := range contribution.Bindings {
		bound[binding.ManagedAssetID] = binding
	}
	for _, managedID := range []asset.AssetID{first.ID, second.ID} {
		binding, exists := bound[managedID]
		if !exists ||
			binding.ControllerAssetID != loadBalancer.ID ||
			binding.Authority != graph.AuthorityAuthoritative ||
			binding.Ownership != graph.OwnershipExclusive ||
			binding.CleanupPolicy != graph.CleanupDelegate ||
			!binding.Evidence[graph.LifecycleEvidenceControllerDeleteGuaranteed].(bool) {
			t.Fatalf("managed EIP binding %s = %+v", managedID, binding)
		}
	}
	if _, delegated := bound[userSupplied.ID]; delegated {
		t.Fatalf("user-supplied EIP was delegated: %+v", bound[userSupplied.ID])
	}

	cleanupPlan, err := plan.Solve(plan.Input{
		CleanupTaskID: "cleanup-nlb",
		ResolvedAssetIDs: []asset.AssetID{
			loadBalancer.ID, first.ID, second.ID,
		},
		Assets:            []asset.Asset{loadBalancer, first, second, userSupplied},
		Relationships:     contribution.Relationships,
		LifecycleBindings: contribution.Bindings,
		Revision: plan.RevisionBinding{
			InventoryRevision: "inventory-a", GraphRevision: "graph-a",
			SpecBundleRevision: "bundle-a", SpecHash: "spec-a",
		},
	})
	if err != nil ||
		len(cleanupPlan.Blockers) != 0 ||
		len(cleanupPlan.Steps) != 3 ||
		len(cleanupPlan.ImpactItems) != 2 {
		t.Fatalf("NLB managed EIP cleanup plan = %+v, err=%v", cleanupPlan, err)
	}
	controller := requireCleanupStepForAsset(t, cleanupPlan.Steps, loadBalancer.ID)
	for _, impact := range cleanupPlan.ImpactItems {
		verification := requireCleanupStepForAsset(t, cleanupPlan.Steps, impact.AssetID)
		if impact.Expected != plan.ExpectedDelegatedDelete ||
			impact.DelegatedTo != controller.ID ||
			verification.Kind != plan.StepVerification ||
			verification.Action != plan.ActionVerifyManagedAbsent ||
			len(verification.DependsOn) != 1 ||
			verification.DependsOn[0] != controller.ID {
			t.Fatalf(
				"NLB managed EIP impact=%+v controller=%+v verification=%+v",
				impact,
				controller,
				verification,
			)
		}
	}

	vpc := nlbAsset("vpc", "ACS::VPC::VPC", "vpc-a", "vpc-a", map[string]any{
		"vpc_id": "vpc-a",
	})
	projected, err := coretopology.Project(coretopology.Input{
		Focus: coretopology.Focus{
			Kind: coretopology.FocusVPC, RegionID: "cn-hangzhou", VPCID: "vpc-a",
		},
		Connection: asset.CloudConnection{
			ID: "connection-a", Provider: asset.ProviderAliCloud,
		},
		Scopes: []asset.Scope{{
			ID: "scope-hangzhou", ConnectionID: "connection-a",
			Kind: asset.ScopeRegion, NativeID: "cn-hangzhou", Location: "cn-hangzhou",
		}},
		Assets: []asset.Asset{vpc, loadBalancer, first, second, userSupplied},
		Kinds: map[asset.ResourceKindID]asset.ResourceKind{
			"ACS::VPC::VPC": {
				ID: "ACS::VPC::VPC", Class: "network.vpc",
			},
			"ACS::NLB::LoadBalancer": {
				ID: "ACS::NLB::LoadBalancer", Class: "network.load_balancer",
			},
			"ACS::EIP::EipAddress": {
				ID: "ACS::EIP::EipAddress", Class: "network.public_ip",
			},
		},
		Relationships:     contribution.Relationships,
		LifecycleBindings: contribution.Bindings,
		Limit:             100,
	})
	if err != nil {
		t.Fatalf("project NLB EIP topology: %v", err)
	}
	vpcView, ok := projected.View.(coretopology.VPCView)
	if !ok || len(vpcView.Resources) != 4 || len(vpcView.Edges) != 5 {
		t.Fatalf("NLB EIP VPC topology = %#v", projected.View)
	}
}

func nlbAsset(
	id asset.AssetID,
	nativeType string,
	nativeID string,
	name string,
	normalized map[string]any,
) asset.Asset {
	return asset.Asset{
		ID: id, Name: name, Location: "cn-hangzhou", ScopeID: "scope-hangzhou",
		ResourceKindID: asset.ResourceKindID(nativeType),
		Capabilities: asset.CapabilitySet{
			asset.CapabilityIndexed, asset.CapabilityActionable,
		},
		Identity: asset.Identity{
			Provider: asset.ProviderAliCloud, Partition: "public",
			ConnectionID: "connection-a", ScopeKey: "region:cn-hangzhou",
			NativeType: nativeType, NativeID: nativeID,
		},
		Normalized: normalized,
	}
}
