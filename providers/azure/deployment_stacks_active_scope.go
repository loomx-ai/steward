package azure

import (
	"context"
	"slices"
	"strings"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

// Wait can advance a product from preparation to another native mutation. Check
// live protection without requiring an in-flight deletion target to still exist.
// Missing active members are NOT marked complete: the actual product wait and
// readback retain that responsibility. This does not replace full scope preflight
// between operations or the product's own phase-specific checks.
func (r *Runtime) deploymentStackGuardActiveScope(ctx context.Context, req contracts.ActionRequest, state deploymentStackPrerequisiteState) error {
	c, err := r.resolve(ctx, req.Asset.Identity.ConnectionID)
	if err != nil {
		return err
	}
	// Authenticate every nested receipt before reading any member.
	if _, err := c.deploymentStackPrerequisiteBinding(req, state); err != nil {
		return err
	}
	if state.Active == nil {
		return serviceDenied("deployment_stack_active_scope_missing_execution")
	}
	member, phase, _, err := c.deploymentStackMemberExecutionResult(req, asset.AssetID(text(state.Active["member"])), state.Active)
	if err != nil || phase != "wait" {
		return err // Readback does not start another product phase.
	}
	active, completed := map[asset.AssetID]bool{}, map[asset.AssetID]bool{}
	add := func(ids map[asset.AssetID]bool, request contracts.ActionRequest) {
		ids[request.Asset.ID] = true
		for _, impact := range request.LifecycleImpacts {
			if impact.Delete {
				ids[impact.Asset.ID] = true
			}
		}
	}
	add(active, member)
	for _, saved := range state.Progress.Executions {
		prior, _, _, err := c.deploymentStackMemberExecutionResult(req, asset.AssetID(text(saved["member"])), saved)
		if err != nil {
			return err
		}
		add(completed, prior)
	}
	for id := range active {
		if completed[id] {
			return serviceDenied("deployment_stack_active_scope_overlaps_completion")
		}
	}
	if _, err := c.deploymentStackProtectedRead(ctx, req); err != nil {
		return err
	}
	locks, err := c.managementLocks(ctx)
	if err != nil {
		return err
	}
	ordered := slices.Clone(req.LifecycleImpacts)
	slices.SortFunc(ordered, func(a, b contracts.ActionImpact) int { return strings.Compare(string(a.Asset.ID), string(b.Asset.ID)) })
	for _, impact := range ordered {
		if impact.Delete && locked(impact.Asset.Identity.NativeID, locks) {
			return serviceDenied("azure_management_lock")
		}
		live, err := c.deploymentStackMemberRead(ctx, impact.Asset)
		if isNotFound(err) && impact.Delete && (active[impact.Asset.ID] || completed[impact.Asset.ID]) {
			continue
		}
		if err != nil {
			return err
		}
		if completed[impact.Asset.ID] {
			return serviceDenied("deployment_stack_completed_member_reappeared")
		}
		// Operational generations can change during the authenticated operation.
		// Native creation identity still cannot change, including retained assets.
		if err := serviceCreationIdentity(impact.Asset, live.data); err != nil {
			return err
		}
		if impact.Delete {
			if protectedAzureTags(object(live.data["tags"])) || isDNSRecordType(impact.Asset.Identity.NativeType) && protectedAzureTags(object(object(live.data["properties"])["metadata"])) {
				return serviceDenied("azure_protected_tag")
			}
		}
	}
	_, err = c.deploymentStackProtectedRead(ctx, req)
	return err
}
