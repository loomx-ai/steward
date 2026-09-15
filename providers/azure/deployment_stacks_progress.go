package azure

import (
	"context"
	"maps"
	"slices"
	"strings"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

type deploymentStackProgress struct {
	Preparations []map[string]any
	Executions   []map[string]any
}

type deploymentStackObservedProgress struct {
	Members   map[asset.AssetID]asset.Asset
	Completed map[asset.AssetID]bool
}

// Reconcile recorded execution with native state before using an absence as a
// prerequisite. Neither a raw 404 nor a completed parent's receipt covers an
// unrecorded child. Keep the frozen Stack request unchanged.
func (r *Runtime) deploymentStackObserveProgress(ctx context.Context, req contracts.ActionRequest, progress deploymentStackProgress) (out deploymentStackObservedProgress, err error) {
	defer func() {
		if err != nil {
			out = deploymentStackObservedProgress{}
			err = contracts.DependencyReadError(err)
		}
	}()
	c, err := r.resolve(ctx, req.Asset.Identity.ConnectionID)
	if err != nil {
		return out, err
	}
	configurations, err := c.deploymentStackPreparedConfigurations(req, progress.Preparations)
	if err != nil {
		return out, err
	}
	if _, err := deploymentStackRequestPayload(req); err != nil {
		return out, err
	}
	type completedMember struct {
		request contracts.ActionRequest
		result  contracts.ActionResult
		driver  contracts.ActionDriver
	}
	completed := map[asset.AssetID]completedMember{}
	out = deploymentStackObservedProgress{Members: map[asset.AssetID]asset.Asset{}, Completed: map[asset.AssetID]bool{}}
	for _, saved := range progress.Executions {
		id := asset.AssetID(text(saved["member"]))
		member, phase, result, err := c.deploymentStackMemberExecutionResult(req, id, saved)
		if err != nil {
			return out, err
		}
		if phase != "complete" || out.Completed[id] {
			return out, serviceDenied("deployment_stack_member_completion_not_unique_or_final")
		}
		completed[id] = completedMember{request: member, result: result}
		out.Completed[id] = true
	}
	members := []asset.Asset{}
	for _, impact := range req.LifecycleImpacts {
		if !out.Completed[impact.Asset.ID] {
			members = append(members, impact.Asset)
		}
	}
	slices.SortFunc(members, func(a, b asset.Asset) int { return strings.Compare(string(a.ID), string(b.ID)) })
	if len(completed) == 0 {
		if err := c.deploymentStackObserveMemberConfigurations(ctx, req.Asset, members, configurations); err != nil {
			return out, err
		}
		for _, member := range members {
			if configurations[strings.ToLower(member.Identity.NativeID)] != nil {
				member.Normalized = cloneNormalizedWithoutGeneration(member.Normalized)
			}
			out.Members[member.ID] = member
		}
		return out, nil
	}
	parentsWithCompletedPrerequisites := map[asset.AssetID]bool{}
	for _, parent := range req.LifecycleImpacts {
		for _, child := range req.LifecycleImpacts {
			if out.Completed[child.Asset.ID] && deploymentStackProductPrerequisite(req, parent, child) {
				parentsWithCompletedPrerequisites[parent.Asset.ID] = true
			}
		}
	}
	// All receipt authentication above finishes before any product readback.
	ordered := slices.Sorted(maps.Keys(completed))
	for _, id := range ordered {
		value := completed[id]
		value.driver, err = r.ResolveAction(ctx, value.request.Asset.Identity.ConnectionID, value.request.Asset)
		if err != nil {
			return out, err
		}
		completed[id] = value
	}
	if err := c.deploymentStackObserveMembers(ctx, req.Asset, nil); err != nil {
		return out, err
	}
	verifyCompleted := func() error {
		for _, id := range ordered {
			value := completed[id]
			read, err := c.deploymentStackMemberReadback(ctx, value.request, value.driver, value.result)
			if err != nil {
				return err
			}
			if read.Exists {
				return serviceDenied("deployment_stack_completed_member_has_residual_state")
			}
		}
		return nil
	}
	if err := verifyCompleted(); err != nil {
		return out, err
	}
	for pass := 0; pass < 2; pass++ {
		for _, value := range members {
			member := value
			if strings.EqualFold(member.Identity.NativeType, deploymentStackType) {
				if err := c.deploymentStackObserveMembers(ctx, member, nil); err != nil {
					return out, err
				}
				out.Members[member.ID] = member
				continue
			}
			live, err := c.deploymentStackMemberRead(ctx, member)
			if err != nil {
				return out, err
			}
			if parentsWithCompletedPrerequisites[member.ID] && text(member.Normalized["_arm_parent_configuration"]) != "" {
				if !serviceParentConfigurationMatches(member, live.data) {
					return out, serviceDenied("deployment_stack_parent_configuration_changed")
				}
				member.Normalized = cloneNormalizedWithoutGeneration(member.Normalized)
			}
			configuration := object(configurations[strings.ToLower(member.Identity.NativeID)])
			if err := c.deploymentStackPreparedMember(member, live.data, configuration); err != nil {
				return out, err
			}
			if configuration != nil {
				member.Normalized = cloneNormalizedWithoutGeneration(member.Normalized)
			}
			out.Members[member.ID] = member
		}
	}
	if err := verifyCompleted(); err != nil {
		return out, err
	}
	if err := c.deploymentStackObserveMembers(ctx, req.Asset, nil); err != nil {
		return out, err
	}
	return out, nil
}

// Move only completed native service prerequisites out of the product's current
// cascade. Other products retain their own missing-child handling and checks.
func (c *client) deploymentStackProductRequest(req contracts.ActionRequest, id asset.AssetID, completed map[asset.AssetID]bool) (contracts.ActionRequest, error) {
	member, err := c.deploymentStackMemberRequest(req, id)
	if err != nil {
		return member, err
	}
	prerequisites := map[asset.AssetID]bool{}
	parents := map[asset.AssetID]asset.AssetID{}
	var parent contracts.ActionImpact
	for _, impact := range req.LifecycleImpacts {
		if impact.Asset.ID == id {
			parent = impact
		}
		parents[impact.Asset.ID] = impact.ControllerID
	}
	for _, impact := range req.LifecycleImpacts {
		if completed[impact.Asset.ID] && deploymentStackProductPrerequisite(req, parent, impact) {
			for _, existing := range member.PrerequisiteDeletions {
				if existing.Asset.ID == impact.Asset.ID {
					return contracts.ActionRequest{}, serviceDenied("ambiguous_deployment_stack_completed_prerequisite")
				}
			}
			// This is a product execution prerequisite, not an ownership rewrite.
			// The original Stack impact and its authenticated receipt stay intact.
			impact.ControllerID = id
			member.PrerequisiteDeletions = append(member.PrerequisiteDeletions, impact)
			prerequisites[impact.Asset.ID] = true
		}
	}
	remaining := member.LifecycleImpacts[:0]
	for _, impact := range member.LifecycleImpacts {
		removed := false
		for ancestor := impact.Asset.ID; ancestor != id && ancestor != ""; ancestor = parents[ancestor] {
			if prerequisites[ancestor] {
				removed = true
				break
			}
		}
		if !removed {
			remaining = append(remaining, impact)
		}
	}
	member.LifecycleImpacts = remaining
	slices.SortFunc(member.PrerequisiteDeletions, func(a, b contracts.ActionImpact) int { return strings.Compare(string(a.Asset.ID), string(b.Asset.ID)) })
	return member, nil
}

// A flat Stack member may be a prerequisite of another member without being
// exclusively owned by it. Require the product's typed native relation and both
// explicit Stack memberships before projecting that execution-only relationship.
func deploymentStackProductPrerequisite(req contracts.ActionRequest, parent, child contracts.ActionImpact) bool {
	if !parent.Delete || !child.Delete || parent.Asset.ID == child.Asset.ID || parent.Asset.ID == "" || child.Asset.ID == "" || parent.Asset.Identity.ConnectionID != child.Asset.Identity.ConnectionID || parent.Asset.Identity.Partition != child.Asset.Identity.Partition || !servicePrerequisiteKind(parent.Asset.Identity.NativeType, child.Asset.Identity.NativeType) || !serviceChildRelation(parent.Asset, child.Asset) {
		return false
	}
	if child.ControllerID == parent.Asset.ID {
		return true
	}
	if parent.ControllerID != req.Asset.ID || child.ControllerID != req.Asset.ID {
		return false
	}
	native := object(object(req.Asset.Normalized[deploymentStackReviewKey])["members"])
	return native[strings.ToLower(parent.Asset.Identity.NativeID)] != nil && native[strings.ToLower(child.Asset.Identity.NativeID)] != nil
}
