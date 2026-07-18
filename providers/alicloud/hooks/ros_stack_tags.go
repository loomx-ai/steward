package hooks

import (
	"context"
	"sort"
	"strings"

	"github.com/loomx-ai/steward/internal/app/governance"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
	"github.com/loomx-ai/steward/providers/alicloud"
)

const (
	ROSStackIDTagKey       = "acs:ros:stackId"
	rosStackTagEvidence    = "ros:system-tag"
	globalResourceLocation = "global"
)

// ROSStackTags derives lifecycle ownership from the system tag ROS applies to
// resources created by a stack. It is intentionally read-only and requires no
// extra Provider API calls.
type ROSStackTags struct{}

func NewROSStackTags() *ROSStackTags {
	return &ROSStackTags{}
}

func (*ROSStackTags) Contribute(_ context.Context, _ asset.ScopeID, assets []asset.Asset) (governance.Contribution, error) {
	stacksByNativeID := make(map[string][]asset.Asset)
	for _, value := range assets {
		if value.Identity.Provider != asset.ProviderAliCloud || value.Identity.NativeType != alicloud.ROSStackNativeType {
			continue
		}
		stackID := strings.TrimSpace(value.Identity.NativeID)
		if stackID != "" {
			stacksByNativeID[stackID] = append(stacksByNativeID[stackID], value)
		}
	}
	for stackID := range stacksByNativeID {
		sort.Slice(stacksByNativeID[stackID], func(i, j int) bool {
			return stacksByNativeID[stackID][i].ID < stacksByNativeID[stackID][j].ID
		})
	}

	ordered := append([]asset.Asset(nil), assets...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].ID < ordered[j].ID })
	result := governance.Contribution{}
	for _, managed := range ordered {
		if managed.Identity.Provider != asset.ProviderAliCloud || managed.Identity.NativeType == alicloud.ROSStackNativeType {
			continue
		}
		stackID := strings.TrimSpace(managed.Tags[ROSStackIDTagKey])
		if stackID == "" {
			continue
		}
		controller, found := resolveTaggedStack(managed, stacksByNativeID[stackID])
		if !found {
			continue
		}
		evidence := map[string]any{
			"source": rosStackTagEvidence, "tag_key": ROSStackIDTagKey, "tag_value": stackID,
			"stack_id": controller.Identity.NativeID, "resource_type": managed.Identity.NativeType,
			"resource_id": managed.Identity.NativeID, "delete_by_default": true,
			graph.LifecycleEvidenceControllerDeleteGuaranteed: true,
		}
		result.Relationships = append(result.Relationships, graph.Relationship{
			SourceAssetID: managed.ID, TargetAssetID: controller.ID, Type: graph.RelationshipMemberOf,
			Source: rosStackTagEvidence, Evidence: evidence, Confidence: 1,
		})
		result.Bindings = append(result.Bindings, graph.LifecycleBinding{
			ControllerAssetID: controller.ID, ManagedAssetID: managed.ID,
			Authority: graph.AuthorityAuthoritative, Ownership: graph.OwnershipExclusive,
			CleanupPolicy: graph.CleanupDelegate, DirectCleanupAllowed: true,
			EvidenceSource: rosStackTagEvidence, Evidence: evidence, Confidence: 1,
		})
	}
	return result, nil
}

func resolveTaggedStack(managed asset.Asset, candidates []asset.Asset) (asset.Asset, bool) {
	if len(candidates) == 0 {
		return asset.Asset{}, false
	}
	location := strings.TrimSpace(managed.Location)
	matches := make([]asset.Asset, 0, len(candidates))
	for _, candidate := range candidates {
		if location != "" && !strings.EqualFold(location, globalResourceLocation) &&
			strings.TrimSpace(candidate.Location) == location {
			matches = append(matches, candidate)
		}
	}
	if len(matches) == 1 {
		return matches[0], true
	}
	if len(matches) > 1 {
		return asset.Asset{}, false
	}
	if len(candidates) == 1 &&
		(location == "" || strings.EqualFold(location, globalResourceLocation) || strings.TrimSpace(candidates[0].Location) == "") {
		return candidates[0], true
	}
	return asset.Asset{}, false
}
