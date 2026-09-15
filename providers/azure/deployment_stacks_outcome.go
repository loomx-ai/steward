package azure

import (
	"context"
	"strings"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

// These are active ARM observations, not proof of permanent purge or release of
// deny assignments. The action must also verify its receipt and product-specific
// consequences before reporting cleanup completion.
type deploymentStackOutcome struct {
	StackAbsent   bool
	MembersAbsent map[asset.AssetID]bool
}

func (c *client) deploymentStackObserveOutcome(ctx context.Context, req contracts.ActionRequest) (out deploymentStackOutcome, err error) {
	defer func() {
		if err != nil {
			out = deploymentStackOutcome{}
			err = contracts.DependencyReadError(err)
		}
	}()
	// Authenticate the frozen membership and all nested delete/retain consequences
	// before even issuing a GET. This constructs a DELETE but never sends it.
	if _, _, err = c.deploymentStackDeletePlan(req); err != nil {
		return out, err
	}
	out.MembersAbsent = map[asset.AssetID]bool{}
	checkStack := func(value asset.Asset) (bool, error) {
		current, err := c.deploymentStackRead(ctx, value.Identity.NativeID)
		if isNotFound(err) {
			return true, nil
		}
		if err != nil {
			return false, err
		}
		review := object(value.Normalized[deploymentStackReviewKey])
		birth, err := c.deploymentStackBirth(current.data)
		if err != nil {
			return false, err
		}
		if birth == "" || review["incarnation"] != birth || value.Normalized[deploymentStackProofKey] != c.deploymentStackProof(value.Identity.NativeID, value.Identity.ConnectionID, review) {
			return false, serviceDenied("deployment_stack_outcome_incarnation_changed")
		}
		return false, nil
	}
	if out.StackAbsent, err = checkStack(req.Asset); err != nil {
		return out, err
	}
	// Do not stop at the first remaining deleted member: a later retained member
	// might already be missing and must not be hidden as normal deletion progress.
	for _, impact := range req.LifecycleImpacts {
		value := impact.Asset
		absent := false
		if strings.EqualFold(value.Identity.NativeType, deploymentStackType) {
			absent, err = checkStack(value)
		} else {
			var current response
			current, err = c.deploymentStackMemberRead(ctx, value)
			if isNotFound(err) {
				absent, err = true, nil
			} else if err == nil {
				if !impact.Delete && text(value.Normalized["_arm_creation_generation"]) == "" {
					return out, serviceDenied("deployment_stack_retained_incarnation_requires_refresh")
				}
				// Delete/detach may change configuration and ETags. Retention identity is
				// the recorded creation identity, not equality of mutable configuration.
				err = serviceCreationIdentity(value, current.data)
			}
		}
		if err != nil {
			return out, err
		}
		if absent && !impact.Delete {
			return out, serviceDenied("deployment_stack_retained_member_missing")
		}
		out.MembersAbsent[value.ID] = absent
	}
	// Detect a same-name Stack recreation during member readback.
	var finalAbsent bool
	finalAbsent, err = checkStack(req.Asset)
	if err != nil {
		return out, err
	}
	if finalAbsent != out.StackAbsent {
		return out, serviceDenied("deployment_stack_outcome_changed_during_read")
	}
	return out, nil
}
