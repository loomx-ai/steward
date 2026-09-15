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
	// A resource-group GET has no creation token in the native contract. Keep
	// ID/location observations distinct from verified creation identity.
	RetentionEvidence map[asset.AssetID]string
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
	_, parameters, err := c.deploymentStackDeletePlan(req)
	if err != nil {
		return out, err
	}
	out.MembersAbsent = map[asset.AssetID]bool{}
	out.RetentionEvidence = map[asset.AssetID]string{}
	var retainedGroups []asset.Asset
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
			if !impact.Delete && err == nil && !absent {
				out.RetentionEvidence[value.ID] = "creation_identity"
			}
		} else {
			var current response
			current, err = c.deploymentStackMemberRead(ctx, value)
			if isNotFound(err) {
				absent, err = true, nil
			} else if err == nil {
				if !impact.Delete {
					evidence := "creation_identity"
					if text(value.Normalized["_arm_creation_generation"]) == "" {
						if !strings.EqualFold(value.Identity.NativeType, groupType) || parameters["unmanageAction.ResourceGroups"] != "detach" || object(object(req.Asset.Normalized[deploymentStackReviewKey])["members"])[strings.ToLower(value.Identity.NativeID)] == nil {
							return out, serviceDenied("deployment_stack_retained_incarnation_requires_refresh")
						}
						// The compiler already requires this group's native category to detach
						// and rejects retained descendants of deleted groups. This observation
						// does not claim a creation identity the ResourceGroups_Get API lacks.
						evidence = "resource_id_and_location"
					}
					if strings.EqualFold(value.Identity.NativeType, groupType) {
						if err := deploymentStackRetainedGroup(value, current.data); err != nil {
							return out, err
						}
						retainedGroups = append(retainedGroups, value)
					}
					out.RetentionEvidence[value.ID] = evidence
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
	// Re-read retained groups after the member pass: an unavailable group must
	// not be hidden behind successful reads of its deleted members.
	for _, group := range retainedGroups {
		current, failure := c.deploymentStackMemberRead(ctx, group)
		if isNotFound(failure) {
			return out, serviceDenied("deployment_stack_retained_member_missing")
		}
		if failure != nil {
			return out, failure
		}
		if failure := deploymentStackRetainedGroup(group, current.data); failure != nil {
			return out, failure
		}
		if failure := serviceCreationIdentity(group, current.data); failure != nil {
			return out, failure
		}
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

// ResourceGroups_Get 2021-04-01 exposes no creation identity. Its location is
// immutable. Validate what the API can prove without inventing a birth token.
func deploymentStackRetainedGroup(group asset.Asset, raw map[string]any) error {
	if group.Location == "" || text(raw["location"]) == "" || !strings.EqualFold(group.Location, text(raw["location"])) {
		return serviceDenied("deployment_stack_retained_group_location_changed")
	}
	if state := text(object(raw["properties"])["provisioningState"]); state != "" && !strings.EqualFold(state, "Succeeded") {
		return serviceDenied("deployment_stack_retained_group_not_ready")
	}
	return nil
}
