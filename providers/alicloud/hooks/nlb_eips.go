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

const nlbEIPEvidenceSource = "nlb:allocation"

// NLBEIPs links every EIP currently associated with an NLB and delegates only
// provider-managed CREATE_BY_NLB EIPs to the NLB deletion lifecycle.
type NLBEIPs struct{}

func NewNLBEIPs() *NLBEIPs {
	return &NLBEIPs{}
}

func (*NLBEIPs) Contribute(
	_ context.Context,
	_ asset.ScopeID,
	assets []asset.Asset,
) (governance.Contribution, error) {
	ordered := append([]asset.Asset(nil), assets...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].ID < ordered[j].ID })

	result := governance.Contribution{}
	for _, loadBalancer := range ordered {
		if loadBalancer.Identity.Provider != asset.ProviderAliCloud ||
			loadBalancer.Identity.NativeType != "ACS::NLB::LoadBalancer" {
			continue
		}
		for _, allocationID := range normalizedStrings(
			loadBalancer.Normalized[alicloud.NormalizedNLBEIPIDsField],
		) {
			eip, found := resolveNLBEIP(loadBalancer, allocationID, ordered)
			if !found {
				continue
			}
			managed := alicloud.IsNLBManagedEIP(eip, loadBalancer.Identity.NativeID)
			evidence := map[string]any{
				"source": nlbEIPEvidenceSource, "nlb_id": loadBalancer.Identity.NativeID,
				"allocation_id": allocationID, "service_managed": managed,
				graph.RelationshipEvidenceDeletionOrder: graph.DeletionOrderTargetBeforeSource,
			}
			result.Relationships = append(result.Relationships, graph.Relationship{
				SourceAssetID: eip.ID, TargetAssetID: loadBalancer.ID,
				Type: graph.RelationshipAttachedTo, Source: nlbEIPEvidenceSource,
				Evidence: evidence, Confidence: 1,
			})
			if !managed {
				continue
			}
			evidence["lifecycle_kind"] = "nlb_managed_eip"
			evidence["delete_by_default"] = true
			// The NLB action readback checks DescribeEipAddresses after the NLB
			// disappears and does not complete until these EIPs are absent.
			evidence[graph.LifecycleEvidenceControllerDeleteGuaranteed] = true
			evidence[graph.LifecycleEvidenceWaitUntilAbsentBeforeDependents] = true
			result.Bindings = append(result.Bindings, graph.LifecycleBinding{
				ControllerAssetID: loadBalancer.ID, ManagedAssetID: eip.ID,
				Authority: graph.AuthorityAuthoritative, Ownership: graph.OwnershipExclusive,
				CleanupPolicy: graph.CleanupDelegate, DirectCleanupAllowed: false,
				EvidenceSource: nlbEIPEvidenceSource, Evidence: evidence, Confidence: 1,
			})
		}
	}
	return result, nil
}

func resolveNLBEIP(
	loadBalancer asset.Asset,
	allocationID string,
	assets []asset.Asset,
) (asset.Asset, bool) {
	var result asset.Asset
	for _, candidate := range assets {
		if candidate.Identity.Provider != loadBalancer.Identity.Provider ||
			candidate.Identity.ConnectionID != loadBalancer.Identity.ConnectionID ||
			candidate.Identity.Partition != loadBalancer.Identity.Partition ||
			candidate.Identity.NativeType != "ACS::EIP::EipAddress" ||
			strings.TrimSpace(candidate.Identity.NativeID) != strings.TrimSpace(allocationID) ||
			!sameLifecycleScope(loadBalancer, candidate) {
			continue
		}
		if result.ID != "" {
			return asset.Asset{}, false
		}
		result = candidate
	}
	return result, result.ID != ""
}
