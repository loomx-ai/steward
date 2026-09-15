package azure

import (
	"context"
	"slices"
	"strings"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

type deploymentStackProductCheck struct {
	Member asset.AssetID
	Check  contracts.PreflightResult
}

// Use the registered product drivers, including their dependency wrappers. A
// successful product preflight does not prove that Stack DELETE performs that
// product's preparation, purge or readback; those remain separate lifecycle work.
// In particular, do not waive a prerequisite merely because Stack lists it.
func (r *Runtime) deploymentStackPreflightProducts(ctx context.Context, req contracts.ActionRequest, preparations ...map[string]any) (checks []deploymentStackProductCheck, err error) {
	return r.deploymentStackPreflightProductsWithProgress(ctx, req, deploymentStackProgress{Preparations: preparations})
}

func (r *Runtime) deploymentStackPreflightProductsWithProgress(ctx context.Context, req contracts.ActionRequest, progress deploymentStackProgress) (checks []deploymentStackProductCheck, err error) {
	defer func() {
		if err != nil {
			checks = nil
			err = contracts.DependencyReadError(err)
		}
	}()
	c, err := r.resolve(ctx, req.Asset.Identity.ConnectionID)
	if err != nil {
		return nil, err
	}
	observed, err := r.deploymentStackObserveProgress(ctx, req, progress)
	if err != nil {
		return nil, err
	}
	ordered := slices.Clone(req.LifecycleImpacts)
	slices.SortFunc(ordered, func(a, b contracts.ActionImpact) int { return strings.Compare(string(a.Asset.ID), string(b.Asset.ID)) })
	for _, impact := range ordered {
		if !impact.Delete || observed.Completed[impact.Asset.ID] {
			continue // Retention is verified by own reads, not deletion preflight.
		}
		member, err := c.deploymentStackProductRequest(req, impact.Asset.ID, observed.Completed)
		if err != nil {
			return nil, err
		}
		member.Asset = observed.Members[member.Asset.ID]
		for i := range member.LifecycleImpacts {
			if current, exists := observed.Members[member.LifecycleImpacts[i].Asset.ID]; exists {
				member.LifecycleImpacts[i].Asset = current
			}
		}
		driver, err := r.ResolveAction(ctx, req.Asset.Identity.ConnectionID, member.Asset)
		if err != nil {
			return nil, err
		}
		check, err := driver.Preflight(ctx, member)
		if err != nil {
			return nil, err
		}
		if check.Absent {
			return nil, serviceDenied("deployment_stack_member_disappeared_during_preflight")
		}
		checks = append(checks, deploymentStackProductCheck{Member: member.Asset.ID, Check: check})
	}
	if _, err := r.deploymentStackObserveProgress(ctx, req, progress); err != nil {
		return nil, err
	}
	return checks, nil
}
