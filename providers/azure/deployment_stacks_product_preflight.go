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
	configurations, err := c.deploymentStackPreparedConfigurations(req, preparations)
	if err != nil {
		return nil, err
	}
	if _, err := deploymentStackRequestPayload(req); err != nil {
		return nil, err
	}
	members := make([]asset.Asset, 0, len(req.LifecycleImpacts))
	for _, impact := range req.LifecycleImpacts {
		members = append(members, impact.Asset)
	}
	if err := c.deploymentStackObserveMemberConfigurations(ctx, req.Asset, members, configurations); err != nil {
		return nil, err
	}

	// A verified retention write changes the ETag. Its authenticated expected
	// configuration was checked above, and is checked again after all products.
	// Keep every other identity/protection field for the real product driver.
	preparedAsset := func(value asset.Asset) asset.Asset {
		if configurations[strings.ToLower(value.Identity.NativeID)] != nil {
			value.Normalized = cloneNormalizedWithoutGeneration(value.Normalized)
		}
		return value
	}
	ordered := slices.Clone(req.LifecycleImpacts)
	slices.SortFunc(ordered, func(a, b contracts.ActionImpact) int { return strings.Compare(string(a.Asset.ID), string(b.Asset.ID)) })
	for _, impact := range ordered {
		if !impact.Delete {
			continue // Retention is verified by own reads, not deletion preflight.
		}
		member, err := c.deploymentStackMemberRequest(req, impact.Asset.ID)
		if err != nil {
			return nil, err
		}
		member.Asset = preparedAsset(member.Asset)
		for i := range member.LifecycleImpacts {
			member.LifecycleImpacts[i].Asset = preparedAsset(member.LifecycleImpacts[i].Asset)
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
	if err := c.deploymentStackObserveMemberConfigurations(ctx, req.Asset, members, configurations); err != nil {
		return nil, err
	}
	return checks, nil
}
