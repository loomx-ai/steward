package azure

import (
	"context"
	"slices"
	"strings"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

// Product reconciliation is distinct from native operation and ARM resource
// reconciliation. It still does not certify release of Stack deny assignments.
type deploymentStackProductObservation struct {
	Execution          deploymentStackExecutionObservation
	Products           map[asset.AssetID]contracts.ReadbackResult
	ProductsReconciled bool
}

// This read-only final context permits an absent Stack only under its original
// authenticated execution receipt. It never resumes member mutations after the
// Stack disappears. Every deleted member gets its actual product Readback, even
// when native cascading produced no independent member checkpoint.
func (r *Runtime) deploymentStackObserveProductOutcome(ctx context.Context, req contracts.ActionRequest, region string, saved map[string]any, executions ...map[string]any) (out deploymentStackProductObservation, err error) {
	defer func() {
		if err != nil {
			out = deploymentStackProductObservation{}
			err = contracts.DependencyReadError(err)
		}
	}()
	c, err := r.resolve(ctx, req.Asset.Identity.ConnectionID)
	if err != nil {
		return out, err
	}
	members := map[asset.AssetID]contracts.ActionRequest{}
	for _, impact := range req.LifecycleImpacts {
		if !impact.Delete {
			continue
		}
		member, err := c.deploymentStackMemberRequest(req, impact.Asset.ID)
		if err != nil {
			return out, err
		}
		members[impact.Asset.ID] = member
	}
	seen := map[asset.AssetID]bool{}
	// Authenticate every supplied member checkpoint before native polling or reads.
	for _, receipt := range executions {
		id := asset.AssetID(text(receipt["member"]))
		member, phase, result, err := c.deploymentStackMemberExecutionResult(req, id, receipt)
		if err != nil {
			return out, err
		}
		if phase != "complete" || seen[id] {
			return out, serviceDenied("deployment_stack_member_completion_not_unique_or_final")
		}
		seen[id] = true
		member.ExecutionResult = &result
		members[id] = member
	}
	out.Execution, err = c.deploymentStackObserveExecution(ctx, req, region, saved)
	if err != nil {
		return out, err
	}
	ordered := make([]asset.AssetID, 0, len(members))
	for id := range members {
		ordered = append(ordered, id)
	}
	slices.SortFunc(ordered, func(a, b asset.AssetID) int { return strings.Compare(string(a), string(b)) })
	out.Products = map[asset.AssetID]contracts.ReadbackResult{}
	allAbsent := true
	for _, id := range ordered {
		member := members[id]
		driver, err := r.ResolveAction(ctx, member.Asset.Identity.ConnectionID, member.Asset)
		if err != nil {
			return out, err
		}
		read, err := c.deploymentStackProductReadback(ctx, member, driver)
		if err != nil {
			return out, err
		}
		out.Products[id] = read
		allAbsent = allAbsent && !read.Exists
	}
	// Product reads must not hide a recreated Stack or a lost retained resource.
	out.Execution.Outcome, err = c.deploymentStackObserveOutcome(ctx, req)
	if err != nil {
		return out, err
	}
	out.Execution.ResourcesReconciled = deploymentStackResourcesReconciled(req, out.Execution.Operation, out.Execution.Outcome)
	out.ProductsReconciled = out.Execution.ResourcesReconciled && allAbsent
	return out, nil
}
