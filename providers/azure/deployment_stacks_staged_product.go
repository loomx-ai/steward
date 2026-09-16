package azure

import (
	"slices"
	"strings"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

// Add independently planned prerequisites to a temporary preflight projection.
// The live service closure must already be verified. This projection never enters
// Execute or a completion receipt, and does not change the frozen Stack graph.
func (c *client) deploymentStackStagedProductRequest(req, member contracts.ActionRequest, closure deploymentStackServiceClosure) (contracts.ActionRequest, error) {
	byID := map[asset.AssetID]contracts.ActionImpact{}
	for _, impact := range req.LifecycleImpacts {
		byID[impact.Asset.ID] = impact
	}
	impacts := map[asset.AssetID]contracts.ActionImpact{}
	queue := []asset.AssetID{member.Asset.ID}
	for _, impact := range member.LifecycleImpacts {
		impacts[impact.Asset.ID] = impact
		queue = append(queue, impact.Asset.ID)
	}
	visited := map[asset.AssetID]bool{}
	for len(queue) > 0 {
		parent := queue[0]
		queue = queue[1:]
		if visited[parent] {
			continue
		}
		visited[parent] = true
		for _, id := range closure.Prerequisites[parent] {
			impact, found := byID[id]
			if !found || !impact.Delete || id == member.Asset.ID {
				return contracts.ActionRequest{}, serviceDenied("invalid_deployment_stack_staged_prerequisite")
			}
			if existing, found := impacts[id]; found {
				if existing.ControllerID != parent {
					return contracts.ActionRequest{}, serviceDenied("ambiguous_deployment_stack_staged_prerequisite")
				}
				continue
			}
			impact.ControllerID = parent
			impacts[id] = impact
			queue = append(queue, id)
			child, err := c.deploymentStackMemberRequest(req, id)
			if err != nil {
				return contracts.ActionRequest{}, err
			}
			for _, descendant := range child.LifecycleImpacts {
				if existing, found := impacts[descendant.Asset.ID]; found && existing.ControllerID != descendant.ControllerID {
					return contracts.ActionRequest{}, serviceDenied("ambiguous_deployment_stack_staged_prerequisite")
				}
				impacts[descendant.Asset.ID] = descendant
				queue = append(queue, descendant.Asset.ID)
			}
		}
	}
	member.LifecycleImpacts = make([]contracts.ActionImpact, 0, len(impacts))
	for _, impact := range impacts {
		member.LifecycleImpacts = append(member.LifecycleImpacts, impact)
	}
	slices.SortFunc(member.LifecycleImpacts, func(a, b contracts.ActionImpact) int { return strings.Compare(string(a.Asset.ID), string(b.Asset.ID)) })
	return member, nil
}
