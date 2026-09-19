package hooks_test

import (
	"context"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/providers/alicloud/hooks"
)

func TestOwnedSubresourcesAreDeletedBeforeTheirParent(t *testing.T) {
	t.Parallel()

	for _, test := range []struct{ parentType, childType, childID, source string }{
		{"ACS::ALB::LoadBalancer", "ACS::ALB::Listener", "lsn-a", "alb:ListListeners"},
		{"ACS::NLB::LoadBalancer", "ACS::NLB::Listener", "lsn-b", "nlb:ListListeners"},
		{"ACS::SLB::LoadBalancer", "ACS::SLB::VServerGroup", "rsp-a", "slb:DescribeVServerGroups"},
	} {
		t.Run(test.childType, func(t *testing.T) {
			t.Parallel()
			parent := nasAsset("parent", test.parentType, "lb-a", nil)
			child := nasAsset("child", test.childType, test.childID, map[string]any{"loadBalancerId": "lb-a"})
			other := nasAsset("other", test.childType, "other", map[string]any{"loadBalancerId": "lb-unscanned"})
			contribution, err := hooks.NewSubresourceOwnership().Contribute(context.Background(), "scope-hangzhou", []asset.Asset{child, other, parent})
			if err != nil {
				t.Fatal(err)
			}
			if len(contribution.Bindings) != 1 {
				t.Fatalf("contribution = %+v", contribution)
			}
			binding := contribution.Bindings[0]
			if binding.ControllerAssetID != parent.ID || binding.ManagedAssetID != child.ID ||
				binding.Ownership != graph.OwnershipExclusive || binding.CleanupPolicy != graph.CleanupDirect ||
				binding.EvidenceSource != test.source {
				t.Fatalf("binding = %+v", binding)
			}
			cleanupPlan, err := plan.Solve(plan.Input{
				CleanupTaskID: "cleanup", ResolvedAssetIDs: []asset.AssetID{parent.ID},
				Assets: []asset.Asset{parent, child}, LifecycleBindings: contribution.Bindings,
				Revision: plan.RevisionBinding{InventoryRevision: "i", GraphRevision: "g", SpecBundleRevision: "b", SpecHash: "s"},
			})
			if err != nil || len(cleanupPlan.Blockers) != 0 || len(cleanupPlan.Steps) != 2 {
				t.Fatalf("plan = %+v, err = %v", cleanupPlan, err)
			}
			childStep := requireCleanupStepForAsset(t, cleanupPlan.Steps, child.ID)
			parentStep := requireCleanupStepForAsset(t, cleanupPlan.Steps, parent.ID)
			if len(parentStep.DependsOn) != 1 || parentStep.DependsOn[0] != childStep.ID {
				t.Fatalf("order: child=%+v parent=%+v", childStep, parentStep)
			}
		})
	}
}
