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
func (c *client) deploymentStackObserveMembers(ctx context.Context, parent asset.Asset, members []asset.Asset) (err error) {
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
		endpoint, err := c.plannedResourceURL(member)
		if err != nil {
			return err
		}
		current, err := c.readResource(ctx, endpoint)
		if err != nil {
			return err
		}
		if !validResourceResponse(current, member.Identity.NativeID, member.Identity.NativeType) || operationLocation(current.header) != "" {
			return serviceDenied("invalid_deployment_stack_live_member")
		}
		if isCosmosType(member.Identity.NativeType) {
			wire, err := c.plannedResourceID(member)
			if err != nil {
				return err
			}
			if !cosmosSameWireID(responseID(member.Identity.NativeType, text(current.data["id"])), wire) {
				return serviceDenied("deployment_stack_live_member_identity_changed")
			}
		}
		if err := c.servicePrivateIncarnation(member, current.data); err != nil {
			return err
		}
		if err := serviceIncarnation(member, current.data); err != nil {
			return err
		}
	}
	// Detect membership/configuration changes while the member reads ran.
	return verifyStack(parent)
}
