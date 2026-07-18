package hooks_test

import (
	"context"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
	"github.com/loomx-ai/steward/providers/alicloud"
	"github.com/loomx-ai/steward/providers/alicloud/hooks"
)

func TestROSStackTagsContributeDelegatedLifecycleWithDirectCleanupFallback(t *testing.T) {
	t.Parallel()

	stackHangzhou := rosAsset("stack-hangzhou", alicloud.ROSStackNativeType, "stack-1", "cn-hangzhou")
	stackBeijing := rosAsset("stack-beijing", alicloud.ROSStackNativeType, "stack-1", "cn-beijing")
	instance := rosAsset("instance", "ACS::ECS::Instance", "i-1", "cn-hangzhou")
	instance.Tags = map[string]string{hooks.ROSStackIDTagKey: " stack-1 "}
	global := rosAsset("bucket", "ACS::OSS::Bucket", "bucket-1", "global")
	global.Tags = map[string]string{hooks.ROSStackIDTagKey: "stack-global"}
	globalStack := rosAsset("stack-global", alicloud.ROSStackNativeType, "stack-global", "cn-shanghai")
	lookalike := rosAsset("lookalike", "ACS::ECS::Instance", "i-lookalike", "cn-hangzhou")
	lookalike.Tags = map[string]string{"acs:ros:stackid": "stack-1"}

	contribution, err := hooks.NewROSStackTags().Contribute(context.Background(), "scope", []asset.Asset{
		stackBeijing, lookalike, instance, global, stackHangzhou, globalStack,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(contribution.Bindings) != 2 || len(contribution.Relationships) != 2 || len(contribution.Unresolved) != 0 {
		t.Fatalf("contribution = %+v", contribution)
	}
	bindings := make(map[asset.AssetID]graph.LifecycleBinding, len(contribution.Bindings))
	for _, binding := range contribution.Bindings {
		bindings[binding.ManagedAssetID] = binding
		if binding.Authority != graph.AuthorityAuthoritative ||
			binding.Ownership != graph.OwnershipExclusive ||
			binding.CleanupPolicy != graph.CleanupDelegate ||
			!binding.DirectCleanupAllowed ||
			binding.EvidenceSource != "ros:system-tag" ||
			binding.Evidence[graph.LifecycleEvidenceControllerDeleteGuaranteed] != true ||
			binding.Confidence != 1 {
			t.Fatalf("binding = %+v", binding)
		}
	}
	if bindings[instance.ID].ControllerAssetID != stackHangzhou.ID {
		t.Fatalf("regional binding = %+v", bindings[instance.ID])
	}
	if bindings[global.ID].ControllerAssetID != globalStack.ID {
		t.Fatalf("global binding = %+v", bindings[global.ID])
	}
	if _, exists := bindings[lookalike.ID]; exists {
		t.Fatalf("case-mismatched system tag created lifecycle binding: %+v", bindings[lookalike.ID])
	}
}

func TestROSStackTagsIgnoreAmbiguousOrCrossRegionControllers(t *testing.T) {
	t.Parallel()

	tagged := rosAsset("tagged", "ACS::ECS::Instance", "i-1", "cn-shanghai")
	tagged.Tags = map[string]string{hooks.ROSStackIDTagKey: "stack-1"}
	contribution, err := hooks.NewROSStackTags().Contribute(context.Background(), "scope", []asset.Asset{
		tagged,
		rosAsset("stack-hangzhou", alicloud.ROSStackNativeType, "stack-1", "cn-hangzhou"),
		rosAsset("stack-beijing", alicloud.ROSStackNativeType, "stack-1", "cn-beijing"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(contribution.Bindings) != 0 || len(contribution.Relationships) != 0 {
		t.Fatalf("ambiguous cross-region stack match = %+v", contribution)
	}
}

func rosAsset(id asset.AssetID, nativeType, nativeID, location string) asset.Asset {
	return asset.Asset{
		ID: id, ScopeID: "scope", Location: location,
		Identity: asset.Identity{
			Provider: asset.ProviderAliCloud, Partition: "aliyun", ConnectionID: "connection",
			NativeType: nativeType, NativeID: nativeID,
		},
	}
}
