package azure

import (
	"context"
	"strings"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func (c *client) deploymentStackPreparedMember(member asset.Asset, live, prepared map[string]any) error {
	if err := c.servicePrivateIncarnation(member, live); err != nil {
		return err
	}
	if prepared == nil {
		if monitorPrivateLinkTarget(member.Identity.NativeType) {
			// Reviewed association unlinking changes the target ETag. Its keyed
			// native snapshot still binds creation and all non-association settings;
			// the product preflight separately verifies actual association absence.
			proof := text(member.Normalized["_monitor_private_link_target_configuration"])
			if proof == "" || proof != c.privateConfiguration(monitorPrivateLinkTargetSnapshot(live)) {
				return serviceDenied("monitor_private_link_target_configuration_changed")
			}
			member.Normalized = cloneNormalizedWithoutGeneration(member.Normalized)
		}
		return serviceIncarnation(member, live)
	}
	if member.Identity.NativeType != vmType && member.Identity.NativeType != nicType || len(prepared) != 2 || text(prepared["configuration"]) == "" {
		return serviceDenied("invalid_deployment_stack_prepared_member")
	}
	if state := text(object(live["properties"])["provisioningState"]); state != "" && !strings.EqualFold(state, "Succeeded") {
		return serviceDenied("deployment_stack_prepared_member_not_ready")
	}
	expectedBirth, valid := prepared["creation"].(string)
	if !valid || expectedBirth != creationGeneration(live) {
		return serviceDenied("deployment_stack_prepared_member_recreated")
	}
	if err := serviceCreationIdentity(member, live); err != nil {
		return err
	}
	current, err := attachmentPreparedConfiguration(member.Identity.NativeType, live, nil)
	if err != nil {
		return err
	}
	if prepared["configuration"] != c.privateConfiguration(current) {
		return serviceDenied("deployment_stack_prepared_configuration_changed")
	}
	return nil
}

// Only complete, request-bound preparation checkpoints can explain a changed
// member generation. Plain member flags or an unsigned configuration hash cannot.
func (c *client) deploymentStackPreparedConfigurations(req contracts.ActionRequest, checkpoints []map[string]any) (map[string]any, error) {
	return c.deploymentStackPreparationConfigurations(req, checkpoints, false)
}

func (c *client) deploymentStackPreparationConfigurations(req contracts.ActionRequest, checkpoints []map[string]any, pending bool) (map[string]any, error) {
	if _, _, err := c.deploymentStackDeletePlan(req); err != nil {
		return nil, err
	}
	out := map[string]any{}
	for _, saved := range checkpoints {
		id := asset.AssetID(text(saved["member"]))
		member, err := c.deploymentStackMemberRequest(req, id)
		if err != nil {
			return nil, err
		}
		if member.Asset.Identity.NativeType != vmType && member.Asset.Identity.NativeType != nicType {
			return nil, serviceDenied("invalid_deployment_stack_preparation_member")
		}
		binding, err := c.deploymentStackPreparationBinding(req, saved)
		if err != nil {
			return nil, err
		}
		if len(saved) != 5 || (saved["phase"] != "attachments_prepared" && !(pending && saved["phase"] == "prepare_attachments")) || saved["binding"] != binding || object(saved["configurations"]) == nil {
			return nil, serviceDenied("deployment_stack_preparation_not_complete")
		}
		targets := map[string]bool{strings.ToLower(member.Asset.Identity.NativeID): true}
		for _, impact := range member.LifecycleImpacts {
			if impact.Delete && impact.Asset.Identity.NativeType == nicType {
				targets[strings.ToLower(impact.Asset.Identity.NativeID)] = true
			}
		}
		for target, value := range object(saved["configurations"]) {
			entry := object(value)
			if !targets[target] || len(entry) != 2 || text(entry["configuration"]) == "" {
				return nil, serviceDenied("invalid_deployment_stack_prepared_member")
			}
			if _, valid := entry["creation"].(string); !valid {
				return nil, serviceDenied("invalid_deployment_stack_prepared_member")
			}
			if previous := out[target]; previous != nil && c.privateConfiguration(object(previous)) != c.privateConfiguration(entry) {
				return nil, serviceDenied("deployment_stack_preparation_checkpoints_disagree")
			}
			out[target] = value
		}
	}
	return out, nil
}

func (c *client) deploymentStackObservePreparedMembers(ctx context.Context, req contracts.ActionRequest, checkpoints ...map[string]any) error {
	configurations, err := c.deploymentStackPreparedConfigurations(req, checkpoints)
	if err != nil {
		return err
	}
	members := make([]asset.Asset, 0, len(req.LifecycleImpacts))
	for _, impact := range req.LifecycleImpacts {
		members = append(members, impact.Asset)
	}
	return c.deploymentStackObserveMemberConfigurations(ctx, req.Asset, members, configurations)
}
