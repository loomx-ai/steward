package hooks

import (
	"context"
	"strconv"
	"strings"

	"github.com/loomx-ai/steward/internal/app/governance"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
)

const (
	ecsDiskAttachmentEvidence = "ecs:disk-attachment"
	ecsDiskLifecycleKind      = "ecs_disk_delete_with_instance"
	ecsDiskNativeType         = "ACS::ECS::Disk"
	ecsInstanceNativeType     = "ACS::ECS::Instance"
)

// ECSDisks derives cleanup ordering and lifecycle ownership from the disk's
// DeleteWithInstance attachment attribute.
type ECSDisks struct{}

func NewECSDisks() *ECSDisks {
	return &ECSDisks{}
}

func (*ECSDisks) Contribute(_ context.Context, _ asset.ScopeID, assets []asset.Asset) (governance.Contribution, error) {
	byIdentity := make(map[string]asset.Asset, len(assets))
	for _, value := range assets {
		byIdentity[value.Identity.Key()] = value
	}

	result := governance.Contribution{}
	for _, disk := range assets {
		if disk.Identity.Provider != asset.ProviderAliCloud || disk.Identity.NativeType != ecsDiskNativeType {
			continue
		}
		instanceID := diskStringValue(disk, "attached_instance_id", "instanceId", "InstanceId")
		diskType := diskStringValue(disk, "disk_type", "type", "Type")
		deleteWithInstance, known := diskBoolValue(disk, "delete_with_instance", "deleteWithInstance", "DeleteWithInstance")
		if instanceID == "" || !strings.EqualFold(diskType, "system") || !known {
			continue
		}
		instanceIdentity := asset.Identity{
			Provider: disk.Identity.Provider, Partition: disk.Identity.Partition,
			ConnectionID: disk.Identity.ConnectionID, NativeType: ecsInstanceNativeType,
			NativeID: instanceID, ScopeKey: disk.Identity.ScopeKey,
		}
		instance, found := byIdentity[instanceIdentity.Key()]
		if !found {
			continue
		}
		evidence := map[string]any{
			"source": ecsDiskAttachmentEvidence, "disk_id": disk.Identity.NativeID,
			"instance_id": instanceID, "delete_with_instance": deleteWithInstance,
		}
		if deleteWithInstance {
			evidence["lifecycle_kind"] = ecsDiskLifecycleKind
			evidence["delete_by_default"] = true
			evidence[graph.LifecycleEvidenceControllerDeleteGuaranteed] = true
			result.Bindings = append(result.Bindings, graph.LifecycleBinding{
				ControllerAssetID: instance.ID, ManagedAssetID: disk.ID,
				Authority: graph.AuthorityAuthoritative, Ownership: graph.OwnershipExclusive,
				CleanupPolicy: graph.CleanupDelegate, DirectCleanupAllowed: false,
				EvidenceSource: ecsDiskAttachmentEvidence, Evidence: evidence, Confidence: 1,
			})
		} else {
			evidence[graph.RelationshipEvidenceDeletionOrder] = graph.DeletionOrderTargetBeforeSource
		}
		result.Relationships = append(result.Relationships, graph.Relationship{
			SourceAssetID: disk.ID, TargetAssetID: instance.ID,
			Type: graph.RelationshipAttachedTo, Source: ecsDiskAttachmentEvidence,
			Evidence: evidence, Confidence: 1,
		})
	}
	return result, nil
}

func diskStringValue(value asset.Asset, normalizedKeys ...string) string {
	for _, values := range []map[string]any{value.Normalized, nestedMap(value.Normalized, "configuration")} {
		for _, key := range normalizedKeys {
			if text, found := caseInsensitiveValue(values, key); found {
				if result, ok := text.(string); ok && strings.TrimSpace(result) != "" {
					return strings.TrimSpace(result)
				}
			}
		}
	}
	return ""
}

func diskBoolValue(value asset.Asset, normalizedKeys ...string) (bool, bool) {
	for _, values := range []map[string]any{value.Normalized, nestedMap(value.Normalized, "configuration")} {
		for _, key := range normalizedKeys {
			raw, found := caseInsensitiveValue(values, key)
			if !found {
				continue
			}
			switch typed := raw.(type) {
			case bool:
				return typed, true
			case string:
				parsed, err := strconv.ParseBool(strings.TrimSpace(typed))
				if err == nil {
					return parsed, true
				}
			}
		}
	}
	return false, false
}

func nestedMap(values map[string]any, key string) map[string]any {
	raw, found := caseInsensitiveValue(values, key)
	if !found {
		return nil
	}
	result, _ := raw.(map[string]any)
	return result
}

func caseInsensitiveValue(values map[string]any, want string) (any, bool) {
	for key, value := range values {
		if strings.EqualFold(strings.TrimSpace(key), want) {
			return value, true
		}
	}
	return nil, false
}
