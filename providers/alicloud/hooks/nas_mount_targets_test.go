package hooks_test

import (
	"context"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/providers/alicloud/hooks"
)

func TestNASMountTargetsAreDirectChildrenDeletedBeforeFileSystem(t *testing.T) {
	t.Parallel()

	fileSystem := nasAsset(
		"file-system",
		"ACS::NAS::FileSystem",
		"31a8e4",
		nil,
	)
	mountTarget := nasAsset(
		"mount-target",
		"ACS::NAS::MountTarget",
		"31a8e4-w.cn-hangzhou.nas.aliyuncs.com",
		map[string]any{"fileSystemId": "31a8e4"},
	)
	contribution, err := hooks.NewNASMountTargets().Contribute(
		context.Background(),
		"scope-hangzhou",
		[]asset.Asset{mountTarget, fileSystem},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(contribution.Bindings) != 1 {
		t.Fatalf("NAS mount target contribution = %+v", contribution)
	}
	binding := contribution.Bindings[0]
	if binding.ControllerAssetID != fileSystem.ID ||
		binding.ManagedAssetID != mountTarget.ID ||
		binding.Authority != graph.AuthorityAuthoritative ||
		binding.Ownership != graph.OwnershipExclusive ||
		binding.CleanupPolicy != graph.CleanupDirect ||
		binding.DirectCleanupAllowed ||
		binding.EvidenceSource != "nas:DescribeMountTargets" ||
		binding.Evidence["lifecycle_kind"] != "nas_mount_target" ||
		binding.Confidence != 1 {
		t.Fatalf("NAS mount target binding = %+v", binding)
	}

	cleanupPlan, err := plan.Solve(plan.Input{
		CleanupTaskID:     "cleanup-nas",
		ResolvedAssetIDs:  []asset.AssetID{fileSystem.ID},
		Assets:            []asset.Asset{fileSystem, mountTarget},
		LifecycleBindings: contribution.Bindings,
		Revision: plan.RevisionBinding{
			InventoryRevision:  "inventory-a",
			GraphRevision:      "graph-a",
			SpecBundleRevision: "bundle-a",
			SpecHash:           "spec-a",
		},
	})
	if err != nil ||
		len(cleanupPlan.Blockers) != 0 ||
		len(cleanupPlan.Steps) != 2 ||
		len(cleanupPlan.ImpactItems) != 0 {
		t.Fatalf("NAS cleanup plan = %+v, err=%v", cleanupPlan, err)
	}
	mountTargetStep := requireCleanupStepForAsset(
		t,
		cleanupPlan.Steps,
		mountTarget.ID,
	)
	fileSystemStep := requireCleanupStepForAsset(
		t,
		cleanupPlan.Steps,
		fileSystem.ID,
	)
	if mountTargetStep.Kind != plan.StepDirect ||
		fileSystemStep.Kind != plan.StepController ||
		len(fileSystemStep.DependsOn) != 1 ||
		fileSystemStep.DependsOn[0] != mountTargetStep.ID {
		t.Fatalf(
			"NAS cleanup order: mount target=%+v file system=%+v",
			mountTargetStep,
			fileSystemStep,
		)
	}
}

func nasAsset(
	id asset.AssetID,
	nativeType string,
	nativeID string,
	normalized map[string]any,
) asset.Asset {
	return asset.Asset{
		ID: id, ScopeID: "scope-hangzhou", Location: "cn-hangzhou",
		Identity: asset.Identity{
			Provider: asset.ProviderAliCloud, Partition: "public",
			ConnectionID: "connection-a", NativeType: nativeType,
			NativeID: nativeID, ScopeKey: "region:cn-hangzhou",
		},
		Capabilities: asset.CapabilitySet{
			asset.CapabilityIndexed,
			asset.CapabilityActionable,
		},
		Normalized: normalized,
	}
}
