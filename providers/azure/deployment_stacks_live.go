package azure

import (
	"context"
	"strings"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

// Own native reads keep observed membership from being promoted after a stale
// scan. They do not establish deletion permission or complete native child
// closure; the graph remains observed membership without cleanup delegation.
func (c *client) deploymentStackObserveMembers(ctx context.Context, parent asset.Asset, members []asset.Asset) error {
	return c.deploymentStackObserveMemberConfigurations(ctx, parent, members, nil)
}

func (c *client) deploymentStackObserveMemberConfigurations(ctx context.Context, parent asset.Asset, members []asset.Asset, configurations map[string]any) (err error) {
	defer func() { err = contracts.DependencyReadError(err) }()
	verifyStack := func(value asset.Asset) error {
		current, err := c.deploymentStackRead(ctx, value.Identity.NativeID)
		if err != nil {
			return err
		}
		birth, err := c.deploymentStackBirth(current.data)
		if err != nil {
			return err
		}
		review := object(value.Normalized[deploymentStackReviewKey])
		if review["incarnation"] != birth {
			return serviceDenied("deployment_stack_live_incarnation_changed")
		}
		if text(review["configuration"]) == "" || value.Normalized[deploymentStackProofKey] != c.deploymentStackProof(value.Identity.NativeID, value.Identity.ConnectionID, review) || review["configuration"] != c.privateConfiguration(current.data) {
			return serviceDenied("deployment_stack_live_configuration_changed")
		}
		return nil
	}
	if err := verifyStack(parent); err != nil {
		return err
	}
	for _, member := range members {
		if strings.EqualFold(member.Identity.NativeType, deploymentStackType) {
			if err := verifyStack(member); err != nil {
				return err
			}
			continue
		}
		current, err := c.deploymentStackMemberRead(ctx, member)
		if err != nil {
			return err
		}
		if err := c.deploymentStackPreparedMember(member, current.data, object(configurations[strings.ToLower(member.Identity.NativeID)])); err != nil {
			return err
		}
	}
	// Detect membership/configuration changes while the member reads ran.
	return verifyStack(parent)
}

// Read the member's own native endpoint, including case-sensitive Cosmos selectors.
func (c *client) deploymentStackMemberRead(ctx context.Context, member asset.Asset) (response, error) {
	if strings.EqualFold(member.Identity.NativeType, deploymentStackType) {
		return c.deploymentStackRead(ctx, member.Identity.NativeID)
	}
	endpoint, err := c.plannedResourceURL(member)
	if err != nil {
		return response{}, err
	}
	current, err := c.readResource(ctx, endpoint)
	if err != nil {
		return current, err
	}
	if !validResourceResponse(current, member.Identity.NativeID, member.Identity.NativeType) || operationLocation(current.header) != "" {
		return response{}, serviceDenied("invalid_deployment_stack_live_member")
	}
	if isCosmosType(member.Identity.NativeType) {
		wire, err := c.plannedResourceID(member)
		if err != nil {
			return response{}, err
		}
		if !cosmosSameWireID(responseID(member.Identity.NativeType, text(current.data["id"])), wire) {
			return response{}, serviceDenied("deployment_stack_live_member_identity_changed")
		}
	}
	return current, nil
}
