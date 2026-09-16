package azure

import (
	"strings"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

// Call only after the parent's complete execution receipt and exact request are
// authenticated. This identifies candidates, not completed resources: callers
// must perform every child's actual product readback and own ARM read twice.
func (c *client) deploymentStackAttachmentConsequences(req, parent contracts.ActionRequest) (map[asset.AssetID]contracts.ActionRequest, error) {
	out := map[asset.AssetID]contracts.ActionRequest{}
	var walk func(asset.Asset) error
	walk = func(controller asset.Asset) error {
		if controller.Identity.NativeType != vmType && controller.Identity.NativeType != nicType {
			return nil
		}
		attachments, err := resourceAttachments(c.subscription, controller.Identity.NativeType, controller.Normalized)
		if err != nil {
			return err
		}
		for _, attachment := range attachments {
			if !attachment.delete {
				continue
			}
			var child *contracts.ActionImpact
			for i := range parent.LifecycleImpacts {
				candidate := &parent.LifecycleImpacts[i]
				if candidate.ControllerID == controller.ID && strings.EqualFold(candidate.Asset.Identity.NativeID, attachment.id) && strings.EqualFold(candidate.Asset.Identity.NativeType, attachment.kind) {
					if child != nil {
						return serviceDenied("ambiguous_deployment_stack_attachment_consequence")
					}
					child = candidate
				}
			}
			if child == nil {
				return serviceDenied("deployment_stack_attachment_consequence_not_reviewed")
			}
			if !child.Delete {
				continue
			}
			if _, seen := out[child.Asset.ID]; seen {
				return serviceDenied("ambiguous_deployment_stack_attachment_consequence")
			}
			member, err := c.deploymentStackMemberRequest(req, child.Asset.ID)
			if err != nil {
				return err
			}
			// Native cascading did not return an independent child operation result.
			member.ExecutionResult = nil
			out[child.Asset.ID] = member
			if err = walk(child.Asset); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(parent.Asset); err != nil {
		return nil, err
	}
	return out, nil
}
