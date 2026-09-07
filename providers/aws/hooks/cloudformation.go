package hooks

import (
	"context"
	"fmt"
	"strings"

	"github.com/loomx-ai/steward/internal/app/governance"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
	provideraws "github.com/loomx-ai/steward/providers/aws"
)

type CloudFormation struct {
	client   provideraws.CloudFormationClient
	location string
}

func NewCloudFormation(client provideraws.CloudFormationClient, location string) *CloudFormation {
	return &CloudFormation{client: client, location: strings.TrimSpace(location)}
}

func (h *CloudFormation) Contribute(ctx context.Context, _ asset.ScopeID, assets []asset.Asset) (governance.Contribution, error) {
	if h == nil || h.client == nil || h.location == "" {
		return governance.Contribution{}, fmt.Errorf("AWS CloudFormation client and location are required")
	}
	byIdentity := make(map[string]asset.Asset, len(assets))
	byPhysicalID := make(map[string][]asset.Asset, len(assets))
	byStackLogicalID := make(map[string][]asset.Asset, len(assets))
	stacks := make([]asset.Asset, 0)
	for _, value := range assets {
		byIdentity[value.Identity.Key()] = value
		// Cloud Control uses the primary identifier directly, including for global
		// resources whose scope differs from their regional stack.
		if nativeID := strings.TrimSpace(value.Identity.NativeID); nativeID != "" {
			key := value.Identity.NativeType + "\x00" + nativeID
			byPhysicalID[key] = append(byPhysicalID[key], value)
		}
		if physicalID, ok := value.Normalized["physicalId"].(string); ok && physicalID != "" && physicalID != value.Identity.NativeID {
			key := value.Identity.NativeType + "\x00" + physicalID
			byPhysicalID[key] = append(byPhysicalID[key], value)
		}
		stackID := strings.TrimSpace(value.Tags[provideraws.CloudFormationStackIDTagKey])
		logicalID := strings.TrimSpace(value.Tags[provideraws.CloudFormationLogicalIDTagKey])
		if value.Identity.Provider == asset.ProviderAWS && stackID != "" && logicalID != "" {
			key := cloudFormationTagIdentity(stackID, logicalID, value.Identity.NativeType)
			byStackLogicalID[key] = append(byStackLogicalID[key], value)
		}
		if value.Identity.Provider == asset.ProviderAWS && value.Identity.NativeType == provideraws.CloudFormationStackNativeType && value.Location == h.location {
			stacks = append(stacks, value)
		}
	}
	result := governance.Contribution{}
	for _, stack := range stacks {
		cursor := ""
		for {
			page, err := h.client.ListStackResources(ctx, provideraws.ListStackResourcesRequest{StackID: stack.Identity.NativeID, NextToken: cursor})
			if err != nil {
				return governance.Contribution{}, provideraws.NormalizeError(err)
			}
			for _, resource := range page.Resources {
				if resource.NativeType == "" || resource.PhysicalID == "" {
					continue
				}
				policy := graph.CleanupDelegate
				switch resource.DeletionPolicy {
				case "Retain", "RetainExceptOnCreate":
					policy = graph.CleanupRetain
				case "", "Delete", "Snapshot":
				default:
					policy = graph.CleanupUnknown
				}
				evidence := map[string]any{
					"request_id": page.RequestID, "stack_id": stack.Identity.NativeID, "logical_id": resource.LogicalID,
					"physical_id": resource.PhysicalID, "resource_type": resource.NativeType, "resource_status": resource.Status,
					"deletion_policy": resource.DeletionPolicy,
				}
				identity := asset.Identity{
					Provider: stack.Identity.Provider, Partition: stack.Identity.Partition, ConnectionID: stack.Identity.ConnectionID,
					NativeType: resource.NativeType, NativeID: resource.PhysicalID, ScopeKey: stack.Identity.ScopeKey,
				}
				resolution := "native_identity"
				managed, found := byIdentity[identity.Key()]
				if !found {
					resolution = "physical_id"
					managed, found = resolvePhysicalAsset(stack, byPhysicalID[resource.NativeType+"\x00"+resource.PhysicalID])
				}
				if !found && resource.LogicalID != "" {
					resolution = "cloudformation_system_tags"
					key := cloudFormationTagIdentity(stack.Identity.NativeID, resource.LogicalID, resource.NativeType)
					managed, found = resolvePhysicalAsset(stack, byStackLogicalID[key])
				}
				if !found {
					result.Unresolved = append(result.Unresolved, graph.UnresolvedReference{
						Provider: stack.Identity.Provider, ConnectionID: stack.Identity.ConnectionID,
						NativeType: resource.NativeType, NativeID: resource.PhysicalID, ControllerID: stack.ID,
						Relationship: graph.RelationshipMemberOf, Evidence: evidence,
					})
					continue
				}
				evidence["identity_resolution"] = resolution
				addCloudFormationTagEvidence(evidence, managed, stack.Identity.NativeID, resource.LogicalID)
				result.Relationships = append(result.Relationships, graph.Relationship{
					SourceAssetID: managed.ID, TargetAssetID: stack.ID, Type: graph.RelationshipMemberOf,
					Source: "cloudformation:ListStackResources", Evidence: evidence, Confidence: 1,
				})
				result.Bindings = append(result.Bindings, graph.LifecycleBinding{
					ControllerAssetID: stack.ID, ManagedAssetID: managed.ID, Authority: graph.AuthorityAuthoritative,
					Ownership: graph.OwnershipExclusive, CleanupPolicy: policy,
					DirectCleanupAllowed: true, EvidenceSource: "cloudformation:ListStackResources", Evidence: evidence, Confidence: 1,
				})
			}
			if page.NextToken == "" {
				break
			}
			cursor = page.NextToken
		}
	}
	return result, nil
}

func cloudFormationTagIdentity(stackID, logicalID, nativeType string) string {
	return strings.TrimSpace(stackID) + "\x00" + strings.TrimSpace(logicalID) + "\x00" + strings.TrimSpace(nativeType)
}

func addCloudFormationTagEvidence(evidence map[string]any, managed asset.Asset, expectedStackID, expectedLogicalID string) {
	stackID := strings.TrimSpace(managed.Tags[provideraws.CloudFormationStackIDTagKey])
	logicalID := strings.TrimSpace(managed.Tags[provideraws.CloudFormationLogicalIDTagKey])
	stackName := strings.TrimSpace(managed.Tags[provideraws.CloudFormationStackNameTagKey])
	if stackID == "" && logicalID == "" && stackName == "" {
		return
	}
	evidence["system_tag_stack_id"] = stackID
	evidence["system_tag_logical_id"] = logicalID
	evidence["system_tag_stack_name"] = stackName
	evidence["system_tag_stack_id_matches"] = stackID != "" && stackID == expectedStackID
	evidence["system_tag_logical_id_matches"] = logicalID != "" && logicalID == expectedLogicalID
}

func resolvePhysicalAsset(controller asset.Asset, candidates []asset.Asset) (asset.Asset, bool) {
	accountID, _ := controller.Normalized["accountId"].(string)
	accountID = strings.TrimSpace(accountID)
	matched := make([]asset.Asset, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.Identity.Provider != controller.Identity.Provider ||
			candidate.Identity.Partition != controller.Identity.Partition ||
			candidate.Identity.ConnectionID != controller.Identity.ConnectionID {
			continue
		}
		candidateAccountID, _ := candidate.Normalized["accountId"].(string)
		candidateAccountID = strings.TrimSpace(candidateAccountID)
		if accountID != "" && candidateAccountID != "" && candidateAccountID != accountID {
			continue
		}
		location := strings.TrimSpace(candidate.Location)
		if location != "" && !strings.EqualFold(location, "global") && strings.TrimSpace(controller.Location) != "" && location != controller.Location {
			continue
		}
		matched = append(matched, candidate)
	}
	if len(matched) != 1 {
		return asset.Asset{}, false
	}
	return matched[0], true
}
