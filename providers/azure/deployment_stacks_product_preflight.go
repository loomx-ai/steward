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
	return r.deploymentStackCheckProducts(ctx, req, progress, false)
}

func (r *Runtime) deploymentStackCheckProducts(ctx context.Context, req contracts.ActionRequest, progress deploymentStackProgress, staging bool) (checks []deploymentStackProductCheck, err error) {
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
	closure := deploymentStackServiceClosure{}
	for _, impact := range req.LifecycleImpacts {
		if impact.Delete && !observed.Completed[impact.Asset.ID] && HasServiceCascade(impact.Asset.Identity.NativeType) {
			closure, err = r.deploymentStackObserveServiceClosureWithProgress(ctx, req, progress)
			if err != nil {
				return nil, err
			}
			break
		}
	}
	ordered := slices.Clone(req.LifecycleImpacts)
	slices.SortFunc(ordered, func(a, b contracts.ActionImpact) int { return strings.Compare(string(a.Asset.ID), string(b.Asset.ID)) })
	byID := map[asset.AssetID]contracts.ActionImpact{}
	for _, impact := range ordered {
		byID[impact.Asset.ID] = impact
	}
	verified := map[asset.AssetID]contracts.PreflightResult{}
	visiting := map[asset.AssetID]bool{}
	var checkMember func(asset.AssetID) (contracts.PreflightResult, error)
	checkMember = func(id asset.AssetID) (contracts.PreflightResult, error) {
		if check, found := verified[id]; found {
			return check, nil
		}
		if visiting[id] {
			return contracts.PreflightResult{}, serviceDenied("deployment_stack_preflight_controller_cycle")
		}
		visiting[id] = true
		parentKind := ""
		if parentID, covered := closure.CascadeParents[id]; covered {
			parent, found := byID[parentID]
			if !found || !parent.Delete || observed.Completed[parentID] {
				return contracts.PreflightResult{}, serviceDenied("deployment_stack_preflight_parent_unavailable")
			}
			check, err := checkMember(parentID)
			if err != nil {
				return contracts.PreflightResult{}, err
			}
			if check.Allowed && !check.Absent {
				parentKind = parent.Asset.Identity.NativeType
			}
		}
		member, err := c.deploymentStackProductRequest(req, id, observed.Completed)
		if err != nil {
			return contracts.PreflightResult{}, err
		}
		var pending map[asset.AssetID][]asset.AssetID
		if staging {
			member, err = c.deploymentStackStagedProductRequest(req, member, closure)
			if err != nil {
				return contracts.PreflightResult{}, err
			}
			pending = closure.Prerequisites
		}
		member.Asset = observed.Members[member.Asset.ID]
		for i := range member.LifecycleImpacts {
			if current, exists := observed.Members[member.LifecycleImpacts[i].Asset.ID]; exists {
				member.LifecycleImpacts[i].Asset = current
			}
		}
		driver, err := r.ResolveAction(ctx, req.Asset.Identity.ConnectionID, member.Asset)
		if err != nil {
			return contracts.PreflightResult{}, err
		}
		check, err := deploymentStackPreflightWithPending(ctx, driver, member, parentKind, pending)
		if err != nil {
			return contracts.PreflightResult{}, err
		}
		if check.Absent {
			return contracts.PreflightResult{}, serviceDenied("deployment_stack_member_disappeared_during_preflight")
		}
		verified[id] = check
		delete(visiting, id)
		return check, nil
	}
	for _, impact := range ordered {
		if !impact.Delete || observed.Completed[impact.Asset.ID] {
			continue
		}
		check, err := checkMember(impact.Asset.ID)
		if err != nil {
			return nil, err
		}
		checks = append(checks, deploymentStackProductCheck{Member: impact.Asset.ID, Check: check})
	}
	if _, err := r.deploymentStackObserveProgress(ctx, req, progress); err != nil {
		return nil, err
	}
	return checks, nil
}

// Preserve every product and monitoring check. Only the documented intrinsic
// protection reason can be interpreted in the already-verified parent context;
// this never modifies a driver or makes standalone child Execute permissible.
func deploymentStackPreflightInParent(ctx context.Context, driver contracts.ActionDriver, req contracts.ActionRequest, parentKind string) (contracts.PreflightResult, error) {
	return deploymentStackPreflightWithPending(ctx, driver, req, parentKind, nil)
}

func deploymentStackPreflightWithPending(ctx context.Context, driver contracts.ActionDriver, req contracts.ActionRequest, parentKind string, pending map[asset.AssetID][]asset.AssetID) (contracts.PreflightResult, error) {
	if parentKind == "" && len(pending) == 0 {
		return driver.Preflight(ctx, req)
	}
	switch action := driver.(type) {
	case *monitorTargetAction:
		filtered, targets, err := action.request(ctx, req)
		if err != nil {
			return contracts.PreflightResult{}, err
		}
		check, err := deploymentStackPreflightWithPending(ctx, action.inner, filtered, parentKind, pending)
		if err == nil && check.Allowed {
			err = action.dependencies(ctx, req, targets)
		}
		return check, err
	case *action:
		return action.preflightWithPending(ctx, req, parentKind, pending)
	default:
		return driver.Preflight(ctx, req)
	}
}
