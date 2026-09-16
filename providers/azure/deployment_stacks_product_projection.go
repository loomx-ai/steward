package azure

import (
	"slices"
	"strings"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

// Build product execution requests from the already authenticated Stack plan.
// Both flat members must be explicit native Stack members and the product must
// declare this child type and relation. Its live collection and actual preflight
// still verify the proposed cascade before any mutation; this is not ownership
// evidence and never modifies the original plan or its receipt binding.
func deploymentStackProductImpacts(req contracts.ActionRequest) ([]contracts.ActionImpact, error) {
	impacts := slices.Clone(req.LifecycleImpacts)
	native := object(object(req.Asset.Normalized[deploymentStackReviewKey])["members"])
	for i, child := range impacts {
		if !child.Delete || child.ControllerID != req.Asset.ID || native[strings.ToLower(child.Asset.Identity.NativeID)] == nil {
			continue
		}
		var controller asset.AssetID
		for _, parent := range req.LifecycleImpacts {
			if !parent.Delete || parent.Asset.ID == child.Asset.ID || native[strings.ToLower(parent.Asset.Identity.NativeID)] == nil || parent.Asset.Identity.ConnectionID != child.Asset.Identity.ConnectionID || parent.Asset.Identity.Partition != child.Asset.Identity.Partition {
				continue
			}
			// Independent prerequisites retain their separate product lifecycle. Dynamic
			// exceptions to these product rules require additional live evidence.
			if servicePrerequisiteKind(parent.Asset.Identity.NativeType, child.Asset.Identity.NativeType) || !slices.ContainsFunc(serviceChildKinds(parent.Asset.Identity.NativeType), func(kind string) bool { return strings.EqualFold(kind, child.Asset.Identity.NativeType) }) || !serviceChildRelation(parent.Asset, child.Asset) {
				continue
			}
			if controller != "" && controller != parent.Asset.ID {
				return nil, serviceDenied("ambiguous_deployment_stack_product_controller")
			}
			controller = parent.Asset.ID
		}
		if controller != "" {
			impacts[i].ControllerID = controller
		}
	}
	parents := map[asset.AssetID]asset.AssetID{}
	for _, impact := range impacts {
		parents[impact.Asset.ID] = impact.ControllerID
	}
	for id := range parents {
		seen := map[asset.AssetID]bool{}
		for current := id; current != req.Asset.ID; current = parents[current] {
			if current == "" || seen[current] {
				return nil, serviceDenied("invalid_deployment_stack_product_controller_chain")
			}
			seen[current] = true
		}
	}
	return impacts, nil
}
