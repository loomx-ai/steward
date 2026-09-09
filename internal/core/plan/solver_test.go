package plan_test

import (
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
	"github.com/loomx-ai/steward/internal/core/plan"
)

func TestDirectControllerFallbackIncludesItsTransitiveImpacts(t *testing.T) {
	parent := binding("cluster", "pool", graph.OwnershipExclusive, graph.CleanupDelegate, 1)
	parent.DirectCleanupAllowed = true
	child := binding("pool", "vm", graph.OwnershipExclusive, graph.CleanupDelegate, 1)
	disk := binding("vm", "disk", graph.OwnershipExclusive, graph.CleanupDelegate, 1)
	input := plan.Input{Assets: []asset.Asset{actionable("cluster"), actionable("pool"), actionable("vm"), actionable("disk")}, ResolvedAssetIDs: []asset.AssetID{"pool"}, LifecycleBindings: []graph.LifecycleBinding{parent, child, disk}}
	result, err := plan.Solve(input)
	if err != nil || len(result.Blockers) != 0 || len(result.ImpactItems) != 2 || len(result.Warnings) != 1 {
		t.Fatalf("direct controller lost descendants: %+v %v", result, err)
	}
	step := stepForAsset(result.Steps, "pool")
	for _, impact := range result.ImpactItems {
		if impact.DelegatedTo != step.ID || impact.Expected != plan.ExpectedDelegatedDelete {
			t.Fatalf("descendant has wrong effective controller: %+v", impact)
		}
	}
	input.ResolvedAssetIDs = []asset.AssetID{"cluster", "pool"}
	result, err = plan.Solve(input)
	if err != nil || len(result.Blockers) != 0 || len(result.ImpactItems) != 3 || len(result.Warnings) != 0 || stepForAsset(result.Steps, "pool").Action != plan.ActionVerifyManagedAbsent {
		t.Fatalf("selected parent did not recover complete authority: %+v %v", result, err)
	}
	input.ResolvedAssetIDs = []asset.AssetID{"vm"}
	result, err = plan.Solve(input)
	if err != nil || len(result.Blockers) == 0 {
		t.Fatalf("direct fallback incorrectly allowed unmanaged grandchild deletion: %+v %v", result, err)
	}
}

func TestNestedDirectFallbackRespectsEveryOwnerAndSelectedController(t *testing.T) {
	outer := binding("scale-set", "vm", graph.OwnershipExclusive, graph.CleanupDelegate, 1)
	outer.DirectCleanupAllowed = true
	inner := binding("vm", "extension", graph.OwnershipExclusive, graph.CleanupDelegate, 1)
	inner.DirectCleanupAllowed = true
	disk := binding("vm", "disk", graph.OwnershipExclusive, graph.CleanupDelegate, 1)
	input := plan.Input{Assets: []asset.Asset{actionable("scale-set"), actionable("vm"), actionable("extension"), actionable("disk")}, ResolvedAssetIDs: []asset.AssetID{"extension"}, LifecycleBindings: []graph.LifecycleBinding{outer, inner, disk}}
	result, err := plan.Solve(input)
	if err != nil || len(result.Blockers) != 0 || len(result.Steps) != 1 || result.Steps[0].AssetID != "extension" || string(result.Steps[0].Action) != "delete" {
		t.Fatalf("nested direct cleanup failed: %+v %v", result, err)
	}
	input.ResolvedAssetIDs = []asset.AssetID{"vm", "extension", "disk"}
	result, err = plan.Solve(input)
	if err != nil || len(result.Blockers) != 0 || len(result.ImpactItems) != 2 || len(result.Warnings) != 1 {
		t.Fatalf("selected intermediate controller lost children: %+v %v", result, err)
	}
	for _, step := range result.Steps {
		if step.AssetID != "vm" && step.Action != plan.ActionVerifyManagedAbsent {
			t.Fatalf("child got a second delete action: %+v", step)
		}
	}
	input.LifecycleBindings[0].DirectCleanupAllowed = false
	input.ResolvedAssetIDs = []asset.AssetID{"extension"}
	result, err = plan.Solve(input)
	if err != nil || len(result.Blockers) == 0 || len(result.Steps) != 0 {
		t.Fatalf("ancestor direct-cleanup restriction bypassed: %+v %v", result, err)
	}
}

func TestRetentionFollowsDirectChildrenAcrossExecutionSteps(t *testing.T) {
	for _, parentID := range []asset.AssetID{"a-scale-set", "z-scale-set"} {
		t.Run(string(parentID), func(t *testing.T) {
			member := binding(parentID, "vm", graph.OwnershipExclusive, graph.CleanupDirect, 1)
			member.Evidence = map[string]any{"retention_supported": false}
			disk := binding("vm", "disk", graph.OwnershipExclusive, graph.CleanupDelegate, 1)
			input := plan.Input{Assets: []asset.Asset{actionable(parentID), actionable("vm"), actionable("disk")}, ResolvedAssetIDs: []asset.AssetID{parentID, "vm"}, LifecycleBindings: []graph.LifecycleBinding{member, disk}, RequestOptions: map[asset.AssetID]map[string]any{parentID: {"retain_resources": []string{"disk"}}}}
			result, err := plan.Solve(input)
			if err != nil || len(result.Blockers) != 0 || len(result.ImpactItems) != 1 || result.ImpactItems[0].Expected != plan.ExpectedRetainExplicit || result.ImpactItems[0].DelegatedTo != stepForAsset(result.Steps, "vm").ID {
				t.Fatalf("direct child lost ancestor retention: %+v %v", result, err)
			}
			input.RequestOptions[parentID]["retain_resources"] = []string{"vm"}
			result, err = plan.Solve(input)
			if err != nil || len(result.Blockers) == 0 {
				t.Fatalf("unsupported member retention accepted: %+v %v", result, err)
			}
		})
	}
}

func TestRetainingControllerRetainsItsTransitiveChildren(t *testing.T) {
	for _, policy := range []graph.CleanupPolicy{graph.CleanupDelegate, graph.CleanupDirect} {
		t.Run(string(policy), func(t *testing.T) {
			result, err := plan.Solve(plan.Input{
				ResolvedAssetIDs:  []asset.AssetID{"vm", "nic", "ip"},
				Assets:            []asset.Asset{actionable("vm"), actionable("nic"), actionable("ip")},
				LifecycleBindings: []graph.LifecycleBinding{binding("vm", "nic", graph.OwnershipExclusive, graph.CleanupDelegate, 1), binding("nic", "ip", graph.OwnershipExclusive, policy, 1)},
				RequestOptions:    map[asset.AssetID]map[string]any{"vm": {"retain_resources": []string{"nic"}}},
			})
			if err != nil || len(result.Blockers) != 0 || len(result.ImpactItems) != 2 || len(result.Steps) != 1 || result.Steps[0].AssetID != "vm" {
				t.Fatalf("plan=%+v err=%v", result, err)
			}
			for _, impact := range result.ImpactItems {
				if impact.Expected != plan.ExpectedRetainExplicit {
					t.Fatalf("retained controller loses child: %+v", impact)
				}
			}
		})
	}
}

func TestNonRetainableChildCanRemainWithRetainedController(t *testing.T) {
	parent := binding("parent", "cluster", graph.OwnershipExclusive, graph.CleanupDelegate, 1)
	child := binding("cluster", "node", graph.OwnershipExclusive, graph.CleanupDelegate, 1)
	child.Evidence = map[string]any{"retention_supported": false}
	input := plan.Input{Assets: []asset.Asset{actionable("parent"), actionable("cluster"), actionable("node")}, ResolvedAssetIDs: []asset.AssetID{"parent"}, LifecycleBindings: []graph.LifecycleBinding{parent, child}, RequestOptions: map[asset.AssetID]map[string]any{"parent": {"retain_resources": []string{"cluster"}}}}
	result, err := plan.Solve(input)
	if err != nil || len(result.Blockers) != 0 {
		t.Fatalf("retained cluster incorrectly blocked: blockers=%+v err=%v", result.Blockers, err)
	}
	for _, impact := range result.ImpactItems {
		if impact.Expected != plan.ExpectedRetainExplicit {
			t.Fatalf("retained cluster loses nodes: %+v", impact)
		}
	}
	input.RequestOptions["parent"]["retain_resources"] = []string{"node"}
	result, err = plan.Solve(input)
	if err != nil || len(result.Blockers) != 1 || result.Blockers[0].AssetID != "node" {
		t.Fatalf("unavailable child retention accepted: blockers=%+v err=%v", result.Blockers, err)
	}
}

func TestProviderAssetsAreFrozenAtPlanningTime(t *testing.T) {
	vm, disk := actionable("vm"), actionable("disk")
	vm.Normalized = map[string]any{"disks": []any{map[string]any{"autoDelete": true}}}
	disk.Normalized = map[string]any{"attached": "vm"}
	result, err := plan.Solve(plan.Input{ResolvedAssetIDs: []asset.AssetID{"vm"}, Assets: []asset.Asset{vm, disk}, LifecycleBindings: []graph.LifecycleBinding{binding("vm", "disk", graph.OwnershipExclusive, graph.CleanupDelegate, 1)}})
	if err != nil {
		t.Fatal(err)
	}
	vm.Normalized["disks"] = []any{}
	disk.Normalized["attached"] = "other"
	frozenVM, err := plan.PlannedAsset(stepForAsset(result.Steps, "vm").Evidence, vm)
	if err != nil || len(frozenVM.Normalized["disks"].([]any)) != 1 {
		t.Fatalf("VM snapshot changed: %+v %v", frozenVM, err)
	}
	frozenDisk, err := plan.PlannedAsset(result.ImpactItems[0].Evidence, disk)
	if err != nil || frozenDisk.Normalized["attached"] != "vm" {
		t.Fatalf("child snapshot changed: %+v %v", frozenDisk, err)
	}
	disk.Identity.ConnectionID = "different"
	if _, err := plan.PlannedAsset(result.ImpactItems[0].Evidence, disk); err == nil {
		t.Fatal("foreign identity accepted in immutable snapshot")
	}
}

func TestSolverCollapsesACKAndManagedChildren(t *testing.T) {
	t.Parallel()

	result, err := plan.Solve(plan.Input{
		CleanupTaskID: "cln-a", ResolvedAssetIDs: []asset.AssetID{"ack", "ecs", "slb"},
		Assets: []asset.Asset{actionable("ack"), actionable("ecs"), actionable("slb")},
		LifecycleBindings: []graph.LifecycleBinding{
			binding("ack", "ecs", graph.OwnershipExclusive, graph.CleanupDelegate, 1),
			binding("ack", "slb", graph.OwnershipExclusive, graph.CleanupDelegate, 1),
		},
		Revision: revision("inventory-a", "graph-a", "bundle-a", "spec-a"),
	})
	if err != nil || len(result.Blockers) != 0 || len(result.Steps) != 3 || len(result.ImpactItems) != 2 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	controller := stepForAsset(result.Steps, "ack")
	if controller.Kind != plan.StepController {
		t.Fatalf("controller step=%+v", controller)
	}
	for _, impact := range result.ImpactItems {
		verification := stepForAsset(result.Steps, impact.AssetID)
		if impact.Expected != plan.ExpectedDelegatedDelete ||
			impact.DelegatedTo != controller.ID ||
			verification.Kind != plan.StepVerification ||
			verification.Action != plan.ActionVerifyManagedAbsent ||
			len(verification.DependsOn) != 1 ||
			verification.DependsOn[0] != controller.ID {
			t.Fatalf("impact=%+v controller=%+v verification=%+v", impact, controller, verification)
		}
	}
}

func TestSolverDoesNotVerifyControllerIntegratedSystemRouteTable(t *testing.T) {
	t.Parallel()

	lifecycle := binding(
		"vpc",
		"system-route-table",
		graph.OwnershipExclusive,
		graph.CleanupDelegate,
		1,
	)
	lifecycle.Evidence = map[string]any{
		"lifecycle_kind": "vpc_system_route_table",
		graph.LifecycleEvidenceControllerIntegratedResource: true,
	}
	result, err := plan.Solve(plan.Input{
		ResolvedAssetIDs: []asset.AssetID{"vpc", "system-route-table"},
		Assets: []asset.Asset{
			actionable("vpc"),
			actionable("system-route-table"),
		},
		LifecycleBindings: []graph.LifecycleBinding{lifecycle},
		Revision:          revision("inventory-a", "graph-a", "bundle-a", "spec-a"),
	})
	if err != nil ||
		len(result.Blockers) != 0 ||
		len(result.Steps) != 1 ||
		result.Steps[0].AssetID != "vpc" ||
		len(result.ImpactItems) != 1 ||
		!plan.ControllerDeletionImpliesAbsence(result.ImpactItems[0].Evidence) {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestSolverWaitsForManagedVerificationBeforeDeletingDependents(t *testing.T) {
	t.Parallel()

	lifecycle := binding(
		"controller",
		"managed-eni",
		graph.OwnershipExclusive,
		graph.CleanupDelegate,
		1,
	)
	lifecycle.Evidence = map[string]any{
		graph.LifecycleEvidenceWaitUntilAbsentBeforeDependents: true,
	}
	result, err := plan.Solve(plan.Input{
		ResolvedAssetIDs: []asset.AssetID{"controller", "managed-eni", "vpc"},
		Assets: []asset.Asset{
			actionable("controller"),
			actionable("managed-eni"),
			actionable("vpc"),
		},
		Relationships: []graph.Relationship{{
			SourceAssetID: "managed-eni",
			TargetAssetID: "vpc",
			Type:          graph.RelationshipMemberOf,
			Confidence:    1,
		}},
		LifecycleBindings: []graph.LifecycleBinding{lifecycle},
		Revision:          revision("inventory-a", "graph-a", "bundle-a", "spec-a"),
	})
	if err != nil || len(result.Blockers) != 0 || len(result.Steps) != 3 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	controller := stepForAsset(result.Steps, "controller")
	verification := stepForAsset(result.Steps, "managed-eni")
	dependent := stepForAsset(result.Steps, "vpc")
	if len(verification.DependsOn) != 1 ||
		verification.DependsOn[0] != controller.ID ||
		len(dependent.DependsOn) != 1 ||
		dependent.DependsOn[0] != verification.ID {
		t.Fatalf(
			"controller=%+v verification=%+v dependent=%+v",
			controller,
			verification,
			dependent,
		)
	}
}

func TestSolverDeletesImageBeforeCreatedSnapshot(t *testing.T) {
	t.Parallel()

	result, err := plan.Solve(plan.Input{
		ResolvedAssetIDs: []asset.AssetID{"image", "snapshot"},
		Assets:           []asset.Asset{actionable("image"), actionable("snapshot")},
		Relationships: []graph.Relationship{{
			SourceAssetID: "image",
			TargetAssetID: "snapshot",
			Type:          graph.RelationshipCreatedFrom,
			Confidence:    1,
		}},
		Revision: revision("inventory-a", "graph-a", "bundle-a", "spec-a"),
	})
	if err != nil || len(result.Blockers) != 0 || len(result.Steps) != 2 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	imageStep := stepForAsset(result.Steps, "image")
	snapshotStep := stepForAsset(result.Steps, "snapshot")
	if len(imageStep.DependsOn) != 0 ||
		len(snapshotStep.DependsOn) != 1 ||
		snapshotStep.DependsOn[0] != imageStep.ID {
		t.Fatalf("image=%+v snapshot=%+v", imageStep, snapshotStep)
	}
}

func TestSolverBlocksManagedChildWithoutHighestController(t *testing.T) {
	t.Parallel()

	result, err := plan.Solve(plan.Input{
		ResolvedAssetIDs: []asset.AssetID{"ecs"}, Assets: []asset.Asset{actionable("ack"), actionable("ecs")},
		LifecycleBindings: []graph.LifecycleBinding{binding("ack", "ecs", graph.OwnershipExclusive, graph.CleanupDelegate, 1)},
		Revision:          revision("inventory-a", "graph-a", "bundle-a", "spec-a"),
	})
	if err != nil || len(result.Steps) != 0 || !hasBlocker(result.Blockers, "ecs", plan.BlockManagedByController) || result.Blockers[0].ControllerID != "ack" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if result.Blockers[0].Evidence["immediate_controller_id"] != asset.AssetID("ack") {
		t.Fatalf("blocker evidence=%+v", result.Blockers[0].Evidence)
	}
}

func TestSolverAllowsExplicitDirectCleanupFallbackWithWarning(t *testing.T) {
	t.Parallel()

	lifecycle := binding("stack", "ecs", graph.OwnershipExclusive, graph.CleanupDelegate, 1)
	lifecycle.DirectCleanupAllowed = true
	result, err := plan.Solve(plan.Input{
		ResolvedAssetIDs: []asset.AssetID{"ecs"},
		Assets:           []asset.Asset{actionable("stack"), actionable("ecs")},
		LifecycleBindings: []graph.LifecycleBinding{
			lifecycle,
		},
		Revision: revision("inventory-a", "graph-a", "bundle-a", "spec-a"),
	})
	if err != nil || len(result.Blockers) != 0 || len(result.Steps) != 1 || result.Steps[0].AssetID != "ecs" || result.Steps[0].Kind != plan.StepDirect {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if len(result.Warnings) != 1 ||
		result.Warnings[0].Code != plan.WarningManagedResourceDirectCleanup ||
		result.Warnings[0].AssetID != "ecs" ||
		result.Warnings[0].ControllerID != "stack" {
		t.Fatalf("warnings=%+v", result.Warnings)
	}
}

func TestSolverSkipsSelectedNonActionableAssetsWithoutBlockingOtherCleanup(t *testing.T) {
	t.Parallel()

	result, err := plan.Solve(plan.Input{
		ResolvedAssetIDs: []asset.AssetID{"actionable", "unsupported"},
		Assets: []asset.Asset{
			actionable("actionable"),
			{ID: "unsupported", Capabilities: asset.CapabilitySet{asset.CapabilityIndexed}},
		},
		Revision: revision("inventory-a", "graph-a", "bundle-a", "spec-a"),
	})
	if err != nil || len(result.Blockers) != 0 || len(result.Steps) != 1 || result.Steps[0].AssetID != "actionable" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if len(result.Warnings) != 1 ||
		result.Warnings[0].Code != plan.WarningNotActionableSkipped ||
		result.Warnings[0].AssetID != "unsupported" {
		t.Fatalf("warnings=%+v", result.Warnings)
	}
}

func TestSolverSkipsManagedResourceUntilControllerIsSelected(t *testing.T) {
	t.Parallel()

	lifecycle := binding(
		"transit-router",
		"system-route-map",
		graph.OwnershipExclusive,
		graph.CleanupDelegate,
		1,
	)
	lifecycle.Evidence = map[string]any{
		"lifecycle_kind": "cen_system_route_map",
		graph.LifecycleEvidenceControllerDeleteGuaranteed: true,
		graph.LifecycleEvidenceUnselectedControllerAction: graph.LifecycleUnselectedControllerSkip,
	}
	assets := []asset.Asset{
		actionable("transit-router"),
		{
			ID:           "system-route-map",
			Capabilities: asset.CapabilitySet{asset.CapabilityIndexed},
		},
	}

	skipped, err := plan.Solve(plan.Input{
		ResolvedAssetIDs:  []asset.AssetID{"system-route-map"},
		Assets:            assets,
		LifecycleBindings: []graph.LifecycleBinding{lifecycle},
		Revision:          revision("inventory-a", "graph-a", "bundle-a", "spec-a"),
	})
	if err != nil ||
		len(skipped.Blockers) != 0 ||
		len(skipped.Steps) != 0 ||
		len(skipped.ImpactItems) != 0 ||
		len(skipped.Warnings) != 1 ||
		skipped.Warnings[0].Code != plan.WarningManagedByControllerSkipped ||
		skipped.Warnings[0].AssetID != "system-route-map" ||
		skipped.Warnings[0].ControllerID != "transit-router" ||
		skipped.Warnings[0].Evidence["lifecycle_kind"] != "cen_system_route_map" {
		t.Fatalf("skipped result=%+v err=%v", skipped, err)
	}

	delegated, err := plan.Solve(plan.Input{
		ResolvedAssetIDs: []asset.AssetID{
			"system-route-map",
			"transit-router",
		},
		Assets:            assets,
		LifecycleBindings: []graph.LifecycleBinding{lifecycle},
		Revision:          revision("inventory-a", "graph-a", "bundle-a", "spec-a"),
	})
	if err != nil ||
		len(delegated.Blockers) != 0 ||
		len(delegated.Warnings) != 0 ||
		len(delegated.Steps) != 2 ||
		len(delegated.ImpactItems) != 1 ||
		delegated.ImpactItems[0].AssetID != "system-route-map" ||
		delegated.ImpactItems[0].Expected != plan.ExpectedDelegatedDelete {
		t.Fatalf("delegated result=%+v err=%v", delegated, err)
	}
	controller := stepForAsset(delegated.Steps, "transit-router")
	verification := stepForAsset(delegated.Steps, "system-route-map")
	if controller.Kind != plan.StepController ||
		verification.Kind != plan.StepVerification ||
		len(verification.DependsOn) != 1 ||
		verification.DependsOn[0] != controller.ID {
		t.Fatalf("controller=%+v verification=%+v", controller, verification)
	}
}

func TestSolverRetainsSharedAndUnknownControllerImpacts(t *testing.T) {
	t.Parallel()

	result, err := plan.Solve(plan.Input{
		ResolvedAssetIDs: []asset.AssetID{"ack"},
		Assets:           []asset.Asset{actionable("ack"), actionable("vpc"), actionable("disk")},
		LifecycleBindings: []graph.LifecycleBinding{
			binding("ack", "vpc", graph.OwnershipShared, graph.CleanupRetain, 1),
			binding("ack", "disk", graph.OwnershipUnknown, graph.CleanupRetain, .7),
		},
		Revision: revision("inventory-a", "graph-a", "bundle-a", "spec-a"),
	})
	if err != nil || !hasCode(result.Blockers, plan.BlockLifecycleAuthority) || len(result.Steps) != 1 || len(result.ImpactItems) != 2 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	impacts := impactByAsset(result.ImpactItems)
	if impacts["vpc"].Expected != plan.ExpectedRetainShared || impacts["disk"].Expected != plan.ExpectedUnknown || !impacts["vpc"].MayContinueBilling || !impacts["disk"].MayContinueBilling {
		t.Fatalf("impacts=%+v", impacts)
	}
}

func TestSolverSelectsHighestLifecycleController(t *testing.T) {
	t.Parallel()

	result, err := plan.Solve(plan.Input{
		CleanupTaskID: "cln-top", ResolvedAssetIDs: []asset.AssetID{"ecs", "ack", "ros"},
		Assets: []asset.Asset{actionable("ecs"), actionable("ack"), actionable("ros")},
		LifecycleBindings: []graph.LifecycleBinding{
			binding("ack", "ecs", graph.OwnershipExclusive, graph.CleanupDelegate, 1),
			binding("ros", "ack", graph.OwnershipExclusive, graph.CleanupDelegate, 1),
		},
		Revision: revision("inventory-a", "graph-a", "bundle-a", "spec-a"),
	})
	if err != nil || len(result.Blockers) != 0 || len(result.Steps) != 3 || len(result.ImpactItems) != 2 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	root := stepForAsset(result.Steps, "ros")
	for _, managedID := range []asset.AssetID{"ack", "ecs"} {
		verification := stepForAsset(result.Steps, managedID)
		if verification.Kind != plan.StepVerification ||
			len(verification.DependsOn) != 1 ||
			verification.DependsOn[0] != root.ID {
			t.Fatalf("root=%+v verification=%+v", root, verification)
		}
	}
}

func TestSolverBlocksLifecycleConflictCycleAndLowConfidence(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		selected  []asset.AssetID
		assets    []asset.Asset
		bindings  []graph.LifecycleBinding
		blockCode plan.BlockCode
	}{
		{
			name: "conflict", selected: []asset.AssetID{"child", "controller-a", "controller-b"},
			assets: []asset.Asset{actionable("child"), actionable("controller-a"), actionable("controller-b")},
			bindings: []graph.LifecycleBinding{
				binding("controller-a", "child", graph.OwnershipExclusive, graph.CleanupDelegate, 1),
				binding("controller-b", "child", graph.OwnershipExclusive, graph.CleanupDelegate, 1),
			}, blockCode: plan.BlockLifecycleConflict,
		},
		{
			name: "cycle", selected: []asset.AssetID{"a", "b"}, assets: []asset.Asset{actionable("a"), actionable("b")},
			bindings: []graph.LifecycleBinding{
				binding("a", "b", graph.OwnershipExclusive, graph.CleanupDelegate, 1),
				binding("b", "a", graph.OwnershipExclusive, graph.CleanupDelegate, 1),
			}, blockCode: plan.BlockLifecycleCycle,
		},
		{
			name: "confidence", selected: []asset.AssetID{"child", "controller"}, assets: []asset.Asset{actionable("child"), actionable("controller")},
			bindings: []graph.LifecycleBinding{
				binding("controller", "child", graph.OwnershipExclusive, graph.CleanupDelegate, .7),
			}, blockCode: plan.BlockLifecycleConfidence,
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			result, err := plan.Solve(plan.Input{ResolvedAssetIDs: test.selected, Assets: test.assets, LifecycleBindings: test.bindings, Revision: revision("i", "g", "b", "s")})
			if err != nil || !hasCode(result.Blockers, test.blockCode) {
				t.Fatalf("result=%+v err=%v", result, err)
			}
		})
	}
}

func TestSolverAppliesProtectionToControllerAndExpectedDeletedImpacts(t *testing.T) {
	t.Parallel()

	result, err := plan.Solve(plan.Input{
		ResolvedAssetIDs: []asset.AssetID{"ack"}, Assets: []asset.Asset{actionable("ack"), actionable("ecs")},
		LifecycleBindings: []graph.LifecycleBinding{binding("ack", "ecs", graph.OwnershipExclusive, graph.CleanupDelegate, 1)},
		Protections:       []plan.ProtectionPolicy{{AssetID: "ecs", Protected: true, Reason: "production policy", Source: "policy"}},
		Revision:          revision("i", "g", "b", "s"),
	})
	if err != nil || !hasBlocker(result.Blockers, "ecs", plan.BlockProtected) {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestSolverBuildsDependencyDAGInDeleteOrder(t *testing.T) {
	t.Parallel()

	result, err := plan.Solve(plan.Input{
		CleanupTaskID: "cln-dag", ResolvedAssetIDs: []asset.AssetID{"app", "network"},
		Assets:        []asset.Asset{actionable("app"), actionable("network")},
		Relationships: []graph.Relationship{{SourceAssetID: "app", TargetAssetID: "network", Type: graph.RelationshipDependsOn}},
		Revision:      revision("i", "g", "b", "s"),
	})
	if err != nil || len(result.Blockers) != 0 || len(result.Steps) != 2 || result.Steps[0].AssetID != "app" || result.Steps[1].AssetID != "network" || len(result.Steps[1].DependsOn) != 1 || result.Steps[1].DependsOn[0] != result.Steps[0].ID {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestSolverCanDeleteAttachmentTargetBeforeSource(t *testing.T) {
	t.Parallel()

	result, err := plan.Solve(plan.Input{
		CleanupTaskID: "cln-detach", ResolvedAssetIDs: []asset.AssetID{"disk", "instance"},
		Assets: []asset.Asset{actionable("disk"), actionable("instance")},
		Relationships: []graph.Relationship{{
			SourceAssetID: "disk", TargetAssetID: "instance", Type: graph.RelationshipAttachedTo,
			Evidence: map[string]any{
				graph.RelationshipEvidenceDeletionOrder: graph.DeletionOrderTargetBeforeSource,
			},
		}},
		Revision: revision("i", "g", "b", "s"),
	})
	if err != nil || len(result.Blockers) != 0 || len(result.Steps) != 2 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if result.Steps[0].AssetID != "instance" ||
		result.Steps[1].AssetID != "disk" ||
		len(result.Steps[1].DependsOn) != 1 ||
		result.Steps[1].DependsOn[0] != result.Steps[0].ID {
		t.Fatalf("steps=%+v", result.Steps)
	}
}

func TestSolverOrdersDependentStepBeforeControllerDeletingNetworkImpact(t *testing.T) {
	t.Parallel()

	result, err := plan.Solve(plan.Input{
		CleanupTaskID:    "cln-network-impact",
		ResolvedAssetIDs: []asset.AssetID{"stack", "external-instance"},
		Assets: []asset.Asset{
			actionable("stack"), actionable("stack-vswitch"), actionable("external-instance"),
		},
		LifecycleBindings: []graph.LifecycleBinding{
			binding("stack", "stack-vswitch", graph.OwnershipExclusive, graph.CleanupDelegate, 1),
		},
		Relationships: []graph.Relationship{{
			SourceAssetID: "external-instance", TargetAssetID: "stack-vswitch",
			Type: graph.RelationshipDependsOn,
		}},
		Revision: revision("i", "g", "b", "s"),
	})
	if err != nil || len(result.Blockers) != 0 || len(result.Steps) != 3 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	external := stepForAsset(result.Steps, "external-instance")
	controller := stepForAsset(result.Steps, "stack")
	verification := stepForAsset(result.Steps, "stack-vswitch")
	if len(controller.DependsOn) != 1 ||
		controller.DependsOn[0] != external.ID ||
		verification.Kind != plan.StepVerification ||
		len(verification.DependsOn) != 1 ||
		verification.DependsOn[0] != controller.ID {
		t.Fatalf("steps=%+v", result.Steps)
	}
}

func TestSolverRoutesNestedImpactsToDirectLifecycleStep(t *testing.T) {
	t.Parallel()

	result, err := plan.Solve(plan.Input{
		CleanupTaskID: "cln-direct", ResolvedAssetIDs: []asset.AssetID{"stack"},
		Assets: []asset.Asset{actionable("stack"), actionable("ack"), actionable("ecs")},
		LifecycleBindings: []graph.LifecycleBinding{
			binding("stack", "ack", graph.OwnershipExclusive, graph.CleanupDirect, 1),
			binding("ack", "ecs", graph.OwnershipExclusive, graph.CleanupDelegate, 1),
		},
		Revision: revision("i", "g", "b", "s"),
	})
	if err != nil || len(result.Blockers) != 0 || len(result.Steps) != 3 || len(result.ImpactItems) != 1 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	controller := stepForAsset(result.Steps, "ack")
	root := stepForAsset(result.Steps, "stack")
	verification := stepForAsset(result.Steps, "ecs")
	if controller.Kind != plan.StepController ||
		len(root.DependsOn) != 1 ||
		root.DependsOn[0] != controller.ID ||
		verification.Kind != plan.StepVerification ||
		len(verification.DependsOn) != 1 ||
		verification.DependsOn[0] != controller.ID {
		t.Fatalf("steps=%+v", result.Steps)
	}
	if result.ImpactItems[0].AssetID != "ecs" || result.ImpactItems[0].DelegatedTo != controller.ID {
		t.Fatalf("impact=%+v", result.ImpactItems[0])
	}
}

func TestSolverHonorsExplicitControllerRetainOptions(t *testing.T) {
	t.Parallel()

	lifecycle := binding("ack", "slb", graph.OwnershipExclusive, graph.CleanupDelegate, 1)
	lifecycle.Evidence = map[string]any{"resource_type": "ACS::SLB::LoadBalancer", "instance_id": "lb-1", "delete_by_default": true}
	result, err := plan.Solve(plan.Input{
		ResolvedAssetIDs: []asset.AssetID{"ack"},
		Assets: []asset.Asset{
			actionable("ack"),
			{ID: "slb", Identity: asset.Identity{NativeType: "ACS::SLB::LoadBalancer", NativeID: "lb-1"}, Capabilities: asset.CapabilitySet{asset.CapabilityIndexed, asset.CapabilityActionable}},
		},
		LifecycleBindings: []graph.LifecycleBinding{lifecycle},
		RequestOptions: map[asset.AssetID]map[string]any{
			"ack": {"delete_options": []any{map[string]any{"resource_type": "ACS::SLB::LoadBalancer", "delete_mode": "retain"}}},
		},
		Revision: revision("i", "g", "b", "s"),
	})
	if err != nil || len(result.Blockers) != 0 || len(result.ImpactItems) != 1 || result.ImpactItems[0].Expected != plan.ExpectedRetainExplicit || !result.ImpactItems[0].MayContinueBilling {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestSolverBlocksDependencyCycles(t *testing.T) {
	t.Parallel()

	result, err := plan.Solve(plan.Input{
		ResolvedAssetIDs: []asset.AssetID{"a", "b"}, Assets: []asset.Asset{actionable("a"), actionable("b")},
		Relationships: []graph.Relationship{
			{SourceAssetID: "a", TargetAssetID: "b", Type: graph.RelationshipDependsOn},
			{SourceAssetID: "b", TargetAssetID: "a", Type: graph.RelationshipDependsOn},
		},
		Revision: revision("i", "g", "b", "s"),
	})
	if err != nil || !hasCode(result.Blockers, plan.BlockDependencyCycle) {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestCleanupTaskFreshnessBindsEveryRevisionAndImpactSnapshot(t *testing.T) {
	t.Parallel()

	input := plan.Input{
		ResolvedAssetIDs: []asset.AssetID{"ack"}, Assets: []asset.Asset{actionable("ack"), actionable("ecs")},
		LifecycleBindings: []graph.LifecycleBinding{binding("ack", "ecs", graph.OwnershipExclusive, graph.CleanupDelegate, 1)},
		Revision:          revision("inventory-a", "graph-a", "bundle-a", "spec-a"),
	}
	result, err := plan.Solve(input)
	if err != nil {
		t.Fatal(err)
	}
	stored := plan.CleanupTask{Revision: input.Revision, SnapshotHash: result.SnapshotHash}
	if err := plan.CheckFresh(stored, input.Revision, result.SnapshotHash); err != nil {
		t.Fatalf("unchanged plan is stale: %v", err)
	}
	changed := input
	changed.LifecycleBindings = []graph.LifecycleBinding{binding("ack", "ecs", graph.OwnershipShared, graph.CleanupRetain, 1)}
	changedResult, err := plan.Solve(changed)
	if err != nil {
		t.Fatal(err)
	}
	if err := plan.CheckFresh(stored, changed.Revision, changedResult.SnapshotHash); err == nil {
		t.Fatal("changed impact set must invalidate plan")
	}
	changed.Revision.GraphRevision = "graph-b"
	if err := plan.CheckFresh(stored, changed.Revision, result.SnapshotHash); err == nil {
		t.Fatal("changed graph revision must invalidate plan")
	}
	for name, mutate := range map[string]func(*plan.RevisionBinding){
		"inventory": func(value *plan.RevisionBinding) { value.InventoryRevision = "inventory-b" },
		"bundle":    func(value *plan.RevisionBinding) { value.SpecBundleRevision = "bundle-b" },
		"spec hash": func(value *plan.RevisionBinding) { value.SpecHash = "spec-b" },
	} {
		t.Run(name, func(t *testing.T) {
			current := input.Revision
			mutate(&current)
			if err := plan.CheckFresh(stored, current, result.SnapshotHash); err == nil {
				t.Fatalf("changed %s must invalidate plan", name)
			}
		})
	}
}

func actionable(id asset.AssetID) asset.Asset {
	return asset.Asset{ID: id, Capabilities: asset.CapabilitySet{asset.CapabilityIndexed, asset.CapabilityActionable}}
}

func stepForAsset(steps []plan.CleanupTaskStep, id asset.AssetID) plan.CleanupTaskStep {
	for _, step := range steps {
		if step.AssetID == id {
			return step
		}
	}
	return plan.CleanupTaskStep{}
}

func binding(controller, managed asset.AssetID, ownership graph.Ownership, policy graph.CleanupPolicy, confidence float64) graph.LifecycleBinding {
	return graph.LifecycleBinding{
		ControllerAssetID: controller, ManagedAssetID: managed, Authority: graph.AuthorityAuthoritative,
		Ownership: ownership, CleanupPolicy: policy, Confidence: confidence, EvidenceSource: "provider",
	}
}

func revision(inventory, graphRevision, bundle, specHash string) plan.RevisionBinding {
	return plan.RevisionBinding{InventoryRevision: inventory, GraphRevision: graphRevision, SpecBundleRevision: bundle, SpecHash: specHash}
}

func hasBlocker(values []plan.Blocker, assetID asset.AssetID, code plan.BlockCode) bool {
	for _, value := range values {
		if value.AssetID == assetID && value.Code == code {
			return true
		}
	}
	return false
}

func hasCode(values []plan.Blocker, code plan.BlockCode) bool {
	for _, value := range values {
		if value.Code == code {
			return true
		}
	}
	return false
}

func impactByAsset(values []plan.ImpactItem) map[asset.AssetID]plan.ImpactItem {
	result := make(map[asset.AssetID]plan.ImpactItem, len(values))
	for _, value := range values {
		result[value.AssetID] = value
	}
	return result
}

func TestOptionalDirectChildRetainsHistoryUnlessSelected(t *testing.T) {
	for _, test := range []struct {
		name                                      string
		selected, retain, protect, weak, required bool
		steps, impacts, blockers                  int
		outcome                                   plan.ExpectedOutcome
	}{
		{name: "default retention", steps: 1, impacts: 1, outcome: plan.ExpectedProviderDefaultRetain},
		{name: "selected child", selected: true, steps: 2},
		{name: "explicit retention overrides selection", selected: true, retain: true, steps: 1, impacts: 1, outcome: plan.ExpectedRetainExplicit},
		{name: "protected history survives", protect: true, steps: 1, impacts: 1, outcome: plan.ExpectedProviderDefaultRetain},
		{name: "protected selected child", selected: true, protect: true, blockers: 1},
		{name: "weak authority cannot retain silently", weak: true, blockers: 1},
		{name: "mandatory child remains mandatory", required: true, steps: 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			member := binding("cluster", "history", graph.OwnershipExclusive, graph.CleanupDirect, 1)
			member.DirectCleanupAllowed = true
			member.Evidence = map[string]any{"retention_supported": !test.required, "delete_by_default": test.required}
			if test.weak {
				member.Confidence = 0.1
			}
			input := plan.Input{Assets: []asset.Asset{actionable("cluster"), actionable("history")}, ResolvedAssetIDs: []asset.AssetID{"cluster"}, LifecycleBindings: []graph.LifecycleBinding{member}}
			if test.selected {
				input.ResolvedAssetIDs = append(input.ResolvedAssetIDs, "history")
			}
			if test.retain {
				input.RequestOptions = map[asset.AssetID]map[string]any{"cluster": {"retain_resources": []string{"history"}}}
			}
			if test.protect {
				input.Protections = []plan.ProtectionPolicy{{AssetID: "history", Protected: true}}
			}
			result, err := plan.Solve(input)
			if err != nil {
				t.Fatal(err)
			}
			if test.blockers > 0 {
				if len(result.Blockers) == 0 {
					t.Fatalf("missing blocker: %+v", result)
				}
				return
			}
			if len(result.Blockers) != 0 || len(result.Steps) != test.steps || len(result.ImpactItems) != test.impacts {
				t.Fatalf("wrong plan: %+v", result)
			}
			if test.impacts > 0 && result.ImpactItems[0].Expected != test.outcome {
				t.Fatalf("wrong retention: %+v", result.ImpactItems)
			}
			if test.selected && !test.retain {
				root, child := stepForAsset(result.Steps, "cluster"), stepForAsset(result.Steps, "history")
				if len(root.DependsOn) != 1 || root.DependsOn[0] != child.ID {
					t.Fatalf("child deletion is not prerequisite: %+v", result.Steps)
				}
			}
		})
	}
}
