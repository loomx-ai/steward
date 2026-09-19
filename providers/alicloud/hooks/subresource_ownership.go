package hooks

import (
	"context"
	"strings"

	"github.com/loomx-ai/steward/internal/app/governance"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
)

// ownedSubresource describes a child resource that exists only inside its
// parent and can be deleted on its own. The parent's native delete removes it
// too, so the child is listed as an exclusive, directly cleaned child: a plan
// that deletes the parent shows and deletes the child first, with its own
// readback.
type ownedSubresource struct {
	parentType  string
	parentField string
	source      string
}

var ownedSubresources = map[string]ownedSubresource{
	"ACS::ALB::Listener":     {parentType: "ACS::ALB::LoadBalancer", parentField: "loadBalancerId", source: "alb:ListListeners"},
	"ACS::NLB::Listener":     {parentType: "ACS::NLB::LoadBalancer", parentField: "loadBalancerId", source: "nlb:ListListeners"},
	"ACS::SLB::VServerGroup": {parentType: "ACS::SLB::LoadBalancer", parentField: "loadBalancerId", source: "slb:DescribeVServerGroups"},
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
		parentID := strings.TrimSpace(normalizedScalar(child.Normalized[owned.parentField]))
		if parentID == "" {
			continue
		}
		parent, found := resolveScopedAsset(child, owned.parentType, parentID, assets)
		if !found {
			continue
		}
		result.Bindings = append(result.Bindings, graph.LifecycleBinding{
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
		})
	}
	return result, nil
}
