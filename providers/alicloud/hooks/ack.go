package hooks

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/loomx-ai/steward/internal/app/governance"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/core/graph"
	"github.com/loomx-ai/steward/internal/provider/contracts"
	"github.com/loomx-ai/steward/providers/alicloud"
)

type ACK struct {
	client   alicloud.ACKClient
	location string
}

func NewACK(client alicloud.ACKClient, location string) *ACK {
	return &ACK{client: client, location: strings.TrimSpace(location)}
}

func (h *ACK) Contribute(ctx context.Context, _ asset.ScopeID, assets []asset.Asset) (governance.Contribution, error) {
	if h.client == nil || h.location == "" {
		return governance.Contribution{}, fmt.Errorf("ACK client and location are required")
	}
	byIdentity := make(map[string]asset.Asset, len(assets))
	var clusters []asset.Asset
	for _, value := range assets {
		byIdentity[value.Identity.Key()] = value
		if value.Identity.Provider == asset.ProviderAliCloud && value.Identity.NativeType == alicloud.ACKClusterNativeType && value.Location == h.location {
			clusters = append(clusters, value)
		}
	}
	result := governance.Contribution{}
	for _, cluster := range clusters {
		resources, requestID, err := h.client.DescribeClusterResources(ctx, cluster.Identity.NativeID, true)
		if err != nil {
			normalized := alicloud.NormalizeError(err)
			if providerResourceGone(normalized) {
				continue
			}
			return governance.Contribution{}, normalized
		}
		seen := make(map[string]struct{}, len(resources))
		for _, resource := range resources {
			nativeType := NormalizeResourceType(resource.ResourceType)
			key := nativeType + "\x00" + resource.InstanceID
			if _, duplicate := seen[key]; duplicate || nativeType == "" || resource.InstanceID == "" {
				continue
			}
			seen[key] = struct{}{}
			ownership, policy, confidence := classifyResource(resource)
			evidence := map[string]any{
				"request_id": requestID, "cluster_id": cluster.Identity.NativeID, "auto_create": resource.AutoCreate,
				"resource_type": nativeType, "instance_id": resource.InstanceID,
				"creator_type": resource.CreatorType, "delete_by_default": resource.DeleteBehavior.DeleteByDefault,
				"delete_behavior_changeable": resource.DeleteBehavior.Changeable, "resource_state": resource.State,
			}
			appendLifecycle(&result, cluster, byIdentity, nativeType, resource.InstanceID, "ack:DescribeClusterResources", ownership, policy, confidence, evidence)
		}
		nodes, nodeRequestID, err := h.client.DescribeClusterNodes(ctx, cluster.Identity.NativeID)
		if err != nil {
			normalized := alicloud.NormalizeError(err)
			if providerResourceGone(normalized) {
				continue
			}
			return governance.Contribution{}, normalized
		}
		for _, node := range nodes {
			key := "ACS::ECS::Instance\x00" + node.InstanceID
			if _, duplicate := seen[key]; duplicate || node.InstanceID == "" {
				continue
			}
			seen[key] = struct{}{}
			ownership, policy, confidence := classifyNode(node)
			evidence := map[string]any{
				"request_id": nodeRequestID, "cluster_id": cluster.Identity.NativeID,
				"resource_type": "ACS::ECS::Instance", "instance_id": node.InstanceID,
				"nodepool_id": node.NodePoolID, "source": node.Source, "auto_created": node.AutoCreated,
			}
			appendLifecycle(&result, cluster, byIdentity, "ACS::ECS::Instance", node.InstanceID, "ack:DescribeClusterNodes", ownership, policy, confidence, evidence)
		}
	}
	return result, nil
}

func providerResourceGone(err error) bool {
	var providerError *contracts.ProviderCallError
	return errors.As(err, &providerError) && providerError.Provider.Category == execution.ErrorNotFound
}

func appendLifecycle(result *governance.Contribution, cluster asset.Asset, byIdentity map[string]asset.Asset, nativeType, nativeID, evidenceSource string, ownership graph.Ownership, policy graph.CleanupPolicy, confidence float64, evidence map[string]any) {
	targetIdentity := asset.Identity{
		Provider: cluster.Identity.Provider, Partition: cluster.Identity.Partition, ConnectionID: cluster.Identity.ConnectionID,
		NativeType: nativeType, NativeID: nativeID, ScopeKey: cluster.Identity.ScopeKey,
	}
	managed, ok := byIdentity[targetIdentity.Key()]
	if !ok {
		result.Unresolved = append(result.Unresolved, graph.UnresolvedReference{
			Provider: cluster.Identity.Provider, ConnectionID: cluster.Identity.ConnectionID,
			NativeType: nativeType, NativeID: nativeID, ControllerID: cluster.ID,
			Relationship: graph.RelationshipMemberOf, Evidence: evidence,
		})
		return
	}
	result.Relationships = append(result.Relationships, graph.Relationship{
		SourceAssetID: managed.ID, TargetAssetID: cluster.ID, Type: graph.RelationshipMemberOf,
		Source: evidenceSource, Evidence: evidence, Confidence: confidence,
	})
	result.Bindings = append(result.Bindings, graph.LifecycleBinding{
		ControllerAssetID: cluster.ID, ManagedAssetID: managed.ID, Authority: graph.AuthorityAuthoritative,
		Ownership: ownership, CleanupPolicy: policy, EvidenceSource: evidenceSource, Evidence: evidence, Confidence: confidence,
	})
}

func classifyResource(resource alicloud.ClusterResource) (graph.Ownership, graph.CleanupPolicy, float64) {
	if resource.AutoCreate == nil {
		return graph.OwnershipUnknown, graph.CleanupRetain, 0.7
	}
	if *resource.AutoCreate == 1 && !strings.EqualFold(resource.CreatorType, "user") {
		return graph.OwnershipExclusive, graph.CleanupDelegate, 1
	}
	if *resource.AutoCreate == 0 && (resource.CreatorType == "" || strings.EqualFold(resource.CreatorType, "user")) {
		return graph.OwnershipShared, graph.CleanupRetain, 1
	}
	return graph.OwnershipUnknown, graph.CleanupRetain, 0.7
}

func classifyNode(node alicloud.ClusterNode) (graph.Ownership, graph.CleanupPolicy, float64) {
	if node.AutoCreated == nil {
		return graph.OwnershipUnknown, graph.CleanupRetain, 0.7
	}
	if *node.AutoCreated && (node.Source == "" || strings.EqualFold(node.Source, "ess") || strings.EqualFold(node.Source, "system")) {
		return graph.OwnershipExclusive, graph.CleanupDelegate, 1
	}
	if !*node.AutoCreated {
		return graph.OwnershipShared, graph.CleanupRetain, 1
	}
	return graph.OwnershipUnknown, graph.CleanupRetain, 0.7
}

func NormalizeResourceType(resourceType string) string {
	resourceType = strings.TrimSpace(resourceType)
	return strings.Replace(resourceType, "ALIYUN::", "ACS::", 1)
}
