package hooks

import (
	"context"
	"strings"

	"github.com/loomx-ai/steward/internal/app/governance"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
)

// ownedSubresource describes a child resource that exists only inside its
// parent and can be deleted on its own. The child is deleted as its own step
// before the parent, and its readback runs while the parent still answers.
// RocketMQ 4.0 instances cannot be deleted until their topics and groups are
// gone; Kafka and RocketMQ 5.0 releases would remove them, but confirming a
// child's absence after its instance is gone depends on error codes the
// products do not document, so they are deleted first as well.
type ownedSubresource struct {
	parentType  string
	parentField string
	source      string
	// parentMatch identifies a parent that the child names only by
	// attributes, as a repository names its namespace by instance and name:
	// each child field must equal the parent field. The relationship is then
	// contributed here because a specification path cannot express it.
	parentMatch map[string]string
	// delegated children are removed by the parent's own delete: the plan
	// shows them as deleted with the parent and then confirms each is gone.
	// The child's readback must report absence once the parent is gone.
	delegated bool
}

var ownedSubresources = map[string]ownedSubresource{
	"ACS::ALB::Listener":           {parentType: "ACS::ALB::LoadBalancer", parentField: "loadBalancerId", source: "alb:ListListeners"},
	"ACS::NLB::Listener":           {parentType: "ACS::NLB::LoadBalancer", parentField: "loadBalancerId", source: "nlb:ListListeners"},
	"ACS::SLB::VServerGroup":       {parentType: "ACS::SLB::LoadBalancer", parentField: "loadBalancerId", source: "slb:DescribeVServerGroups"},
	"ACS::Ons::Topic":              {parentType: "ACS::Ons::Instance", parentField: "instanceId", source: "ons:OnsTopicList"},
	"ACS::Ons::Group":              {parentType: "ACS::Ons::Instance", parentField: "instanceId", source: "ons:OnsGroupList"},
	"ACS::AliKafka::Topic":         {parentType: "ACS::AliKafka::Instance", parentField: "instanceId", source: "alikafka:GetTopicList"},
	"ACS::AliKafka::ConsumerGroup": {parentType: "ACS::AliKafka::Instance", parentField: "instanceId", source: "alikafka:GetConsumerList"},
	"ACS::RocketMQ::Topic":         {parentType: "ACS::RocketMQ::Instance", parentField: "instanceId", source: "rocketmq:ListTopics"},
	"ACS::RocketMQ::ConsumerGroup": {parentType: "ACS::RocketMQ::Instance", parentField: "instanceId", source: "rocketmq:ListConsumerGroups"},
	"ACS::VPN::SslVpnServer":       {parentType: "ACS::VPN::VpnGateway", parentField: "vpnGatewayId", source: "vpc:DescribeSslVpnServers"},
	"ACS::VPN::SslVpnClientCert":   {parentType: "ACS::VPN::SslVpnServer", parentField: "sslVpnServerId", source: "vpc:DescribeSslVpnClientCerts"},
	"ACS::VPN::IpsecServer":        {parentType: "ACS::VPN::VpnGateway", parentField: "vpnGatewayId", source: "vpc:ListIpsecServers"},
	"ACS::CR::Namespace":           {parentType: "ACS::CR::Instance", parentField: "instanceId", source: "cr:ListNamespace"},
	// DeleteProject removes every logstore, including service-created
	// internal ones, and GetLogStore answers 404 once the project is gone.
	"ACS::SLS::LogStore": {parentType: "ACS::SLS::Project", parentField: "project", source: "sls:ListLogStores", delegated: true},
	"ACS::CR::Repository": {parentType: "ACS::CR::Namespace", source: "cr:ListRepository", parentMatch: map[string]string{
		"instanceId": "instanceId", "namespaceName": "namespaceName",
	}},
}

// SubresourceOwnership binds owned subresources to their parents.
type SubresourceOwnership struct{}

func NewSubresourceOwnership() *SubresourceOwnership {
	return &SubresourceOwnership{}
}

func (*SubresourceOwnership) Contribute(_ context.Context, _ asset.ScopeID, assets []asset.Asset) (governance.Contribution, error) {
	result := governance.Contribution{}
	for _, child := range assets {
		owned, ok := ownedSubresources[child.Identity.NativeType]
		if !ok || child.Identity.Provider != asset.ProviderAliCloud {
			continue
		}
		var parent asset.Asset
		var found bool
		if owned.parentMatch != nil {
			parent, found = matchParentByFields(child, owned, assets)
		} else if parentID := strings.TrimSpace(normalizedScalar(child.Normalized[owned.parentField])); parentID != "" {
			parent, found = resolveScopedAsset(child, owned.parentType, parentID, assets)
		}
		if !found {
			continue
		}
		parentID := parent.Identity.NativeID
		if owned.parentMatch != nil {
			result.Relationships = append(result.Relationships, graph.Relationship{
				SourceAssetID: child.ID, TargetAssetID: parent.ID,
				Type: graph.RelationshipMemberOf, Source: owned.source,
				Evidence: map[string]any{"source": owned.source, "parent_id": parentID}, Confidence: 1,
			})
		}
		binding := graph.LifecycleBinding{
			ControllerAssetID: parent.ID,
			ManagedAssetID:    child.ID,
			Authority:         graph.AuthorityAuthoritative,
			Ownership:         graph.OwnershipExclusive,
			CleanupPolicy:     graph.CleanupDirect,
			EvidenceSource:    owned.source,
			Evidence: map[string]any{
				"source": owned.source, "lifecycle_kind": "owned_subresource",
				"parent_id": parentID, "child_id": child.Identity.NativeID,
			},
			Confidence: 1,
		}
		if owned.delegated {
			binding.CleanupPolicy = graph.CleanupDelegate
			binding.DirectCleanupAllowed = true
			binding.Evidence["delete_by_default"] = true
			binding.Evidence[graph.LifecycleEvidenceControllerDeleteGuaranteed] = true
		}
		result.Bindings = append(result.Bindings, binding)
	}
	return result, nil
}

// matchParentByFields finds the one parent in the child's scope whose fields
// equal the child's; an ambiguous or missing match binds nothing.
func matchParentByFields(child asset.Asset, owned ownedSubresource, assets []asset.Asset) (asset.Asset, bool) {
	var result asset.Asset
	for _, candidate := range assets {
		if candidate.Identity.Provider != child.Identity.Provider || candidate.Identity.NativeType != owned.parentType ||
			!sameLifecycleScope(child, candidate) {
			continue
		}
		matches := true
		for childField, parentField := range owned.parentMatch {
			want := strings.TrimSpace(normalizedScalar(child.Normalized[childField]))
			if want == "" || want != strings.TrimSpace(normalizedScalar(candidate.Normalized[parentField])) {
				matches = false
				break
			}
		}
		if !matches {
			continue
		}
		if result.ID != "" {
			return asset.Asset{}, false
		}
		result = candidate
	}
	return result, result.ID != ""
}
