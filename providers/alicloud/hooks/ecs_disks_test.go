package hooks_test

import (
	"context"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
	"github.com/loomx-ai/steward/providers/alicloud/hooks"
)

func TestECSDisksContributeDeleteWithInstanceLifecycle(t *testing.T) {
	t.Parallel()

	instance := ecsDiskAsset("instance", "ACS::ECS::Instance", "i-a")
	disk := ecsDiskAsset("disk", "ACS::ECS::Disk", "d-a")
	disk.Normalized = map[string]any{"configuration": map[string]any{
		"InstanceId": "i-a", "Type": "system", "DeleteWithInstance": true,
	}}

	contribution, err := hooks.NewECSDisks().Contribute(
		context.Background(), "scope", []asset.Asset{disk, instance},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(contribution.Bindings) != 1 || len(contribution.Relationships) != 1 {
		t.Fatalf("contribution = %+v", contribution)
	}
	binding := contribution.Bindings[0]
	if binding.ControllerAssetID != instance.ID ||
		binding.ManagedAssetID != disk.ID ||
		binding.Authority != graph.AuthorityAuthoritative ||
		binding.Ownership != graph.OwnershipExclusive ||
		binding.CleanupPolicy != graph.CleanupDelegate ||
		binding.DirectCleanupAllowed ||
		binding.EvidenceSource != "ecs:disk-attachment" ||
		binding.Evidence["lifecycle_kind"] != "ecs_disk_delete_with_instance" ||
		binding.Evidence[graph.LifecycleEvidenceControllerDeleteGuaranteed] != true ||
		binding.Confidence != 1 {
		t.Fatalf("binding = %+v", binding)
	}
	if _, reversed := contribution.Relationships[0].Evidence[graph.RelationshipEvidenceDeletionOrder]; reversed {
		t.Fatalf("delete-with-instance relationship reversed = %+v", contribution.Relationships[0])
	}
}

func TestECSDisksDeleteRetainedDiskAfterInstance(t *testing.T) {
	t.Parallel()

	instance := ecsDiskAsset("instance", "ACS::ECS::Instance", "i-a")
	disk := ecsDiskAsset("disk", "ACS::ECS::Disk", "d-a")
	disk.Normalized = map[string]any{
		"attached_instance_id": "i-a", "disk_type": "system", "delete_with_instance": false,
	}

	contribution, err := hooks.NewECSDisks().Contribute(
		context.Background(), "scope", []asset.Asset{instance, disk},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(contribution.Bindings) != 0 || len(contribution.Relationships) != 1 {
		t.Fatalf("contribution = %+v", contribution)
	}
	relationship := contribution.Relationships[0]
	if relationship.SourceAssetID != disk.ID ||
		relationship.TargetAssetID != instance.ID ||
		relationship.Type != graph.RelationshipAttachedTo ||
		relationship.Evidence[graph.RelationshipEvidenceDeletionOrder] != graph.DeletionOrderTargetBeforeSource {
		t.Fatalf("relationship = %+v", relationship)
	}
}

func TestECSDisksIgnoreUnknownDeleteWithInstance(t *testing.T) {
	t.Parallel()

	instance := ecsDiskAsset("instance", "ACS::ECS::Instance", "i-a")
	disk := ecsDiskAsset("disk", "ACS::ECS::Disk", "d-a")
	disk.Normalized = map[string]any{"attached_instance_id": "i-a", "disk_type": "system"}
	contribution, err := hooks.NewECSDisks().Contribute(
		context.Background(), "scope", []asset.Asset{instance, disk},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(contribution.Bindings) != 0 || len(contribution.Relationships) != 0 {
		t.Fatalf("unknown lifecycle contribution = %+v", contribution)
	}
}

func TestECSDisksLeaveAttachedDataDisksAsDirectCleanupSteps(t *testing.T) {
	t.Parallel()

	instance := ecsDiskAsset("instance", "ACS::ECS::Instance", "i-a")
	disk := ecsDiskAsset("disk", "ACS::ECS::Disk", "d-a")
	disk.Normalized = map[string]any{
		"attached_instance_id": "i-a", "disk_type": "data", "delete_with_instance": true,
	}
	contribution, err := hooks.NewECSDisks().Contribute(
		context.Background(), "scope", []asset.Asset{instance, disk},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(contribution.Bindings) != 0 || len(contribution.Relationships) != 0 {
		t.Fatalf("data disk lifecycle contribution = %+v", contribution)
	}
}

func ecsDiskAsset(id asset.AssetID, nativeType, nativeID string) asset.Asset {
	return asset.Asset{
		ID: id, ScopeID: "scope", Location: "cn-hangzhou",
		Identity: asset.Identity{
			Provider: asset.ProviderAliCloud, Partition: "public", ConnectionID: "connection",
			NativeType: nativeType, NativeID: nativeID, ScopeKey: "region:cn-hangzhou",
		},
	}
}
