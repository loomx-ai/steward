package azure

import (
	"context"
	"maps"
	"slices"
	"strings"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

const recoveryItemReviewKey = "_recovery_item_review"
const recoveryItemProofKey = "_recovery_item_proof"

func (c *client) recoveryItemProof(id string, connection asset.ConnectionID, review map[string]any) string {
	return c.privateConfiguration(map[string]any{"protocol": "recovery-item-review-1", "owner": id, "connection": connection, "review": review})
}

func (c *client) recoveryItemObservation(ctx context.Context, id string, known map[string]any) (map[string]any, map[string]any, error) {
	owner, err := c.recoveryServicesIdentity(id, recoveryServicesItem)
	if err != nil || owner != id {
		return nil, nil, serviceDenied("invalid_recovery_item_review_owner")
	}
	containerID := redisParentID(id)
	vaultID := recoveryServicesVaultID(id)
	parents, err := c.recoveryContainerParents(ctx, containerID)
	if err != nil {
		return nil, nil, err
	}
	container, err := c.recoveryServicesRead(ctx, containerID, recoveryServicesContainer)
	if err != nil {
		return nil, nil, contracts.DependencyReadError(err)
	}
	vault, err := c.recoveryServicesRead(ctx, vaultID, recoveryServicesVault)
	if err != nil {
		return nil, nil, contracts.DependencyReadError(err)
	}
	if parents["parent_observed"] != c.privateConfiguration(vault.data) {
		return nil, nil, serviceDenied("recovery_item_vault_changed")
	}
	own, err := c.recoveryServicesRead(ctx, id, recoveryServicesItem)
	absent := isNotFound(err)
	if err != nil && !absent {
		return nil, nil, err
	}
	var raw map[string]any
	state := map[string]any{"observed": "", "configuration": "", "policy_id": "", "policy_name": "", "state": "", "protected_type": text(known["protected_type"]), "retained": false}
	if !absent {
		raw = own.data
		p := object(raw["properties"])
		for _, key := range []string{"policyId", "policyName"} {
			if value, exists := p[key]; exists && value != nil {
				if _, ok := value.(string); !ok {
					return nil, nil, serviceDenied("invalid_recovery_item_policy")
				}
			}
		}
		state = c.recoveryItemState(raw)
		state["observed"] = c.privateConfiguration(raw)
	}
	policyID := strings.ToLower(text(state["policy_id"]))
	if policyID == "" {
		policyID = text(known["reference_policy"])
	}
	policyConfiguration := ""
	if policyID != "" {
		policy, err := c.recoveryPolicyRead(ctx, vaultID, policyID)
		if err != nil {
			return nil, nil, err
		}
		policyConfiguration = c.privateConfiguration(hybridComputeChildSnapshot(policy))
	}
	prerequisites, snapshots, err := c.recoveryItemPrerequisites(ctx, id, raw, object(known["snapshots"]))
	if err != nil {
		return nil, nil, err
	}
	state["prerequisites"], state["snapshots"] = prerequisites, snapshots
	guards, err := c.recoveryGuardProxies(ctx, vaultID, object(known["guards"]))
	if err != nil {
		return nil, nil, err
	}
	for _, key := range []string{"securitySettings", "immutabilitySettings"} {
		var value any
		if key == "securitySettings" {
			value = object(vault.data["properties"])[key]
		} else {
			value = object(object(vault.data["properties"])["securitySettings"])[key]
		}
		if value != nil && object(value) == nil {
			return nil, nil, serviceDenied("invalid_recovery_item_security_settings")
		}
	}
	immutableSettings := object(object(object(vault.data["properties"])["securitySettings"])["immutabilitySettings"])
	if value, exists := immutableSettings["state"]; exists && value != nil {
		state, ok := value.(string)
		if !ok || state == "" {
			return nil, nil, serviceDenied("invalid_recovery_item_immutability")
		}
	}
	immutable := text(immutableSettings["state"])
	if immutable != "" && !slices.Contains([]string{"Disabled", "Unlocked", "Locked"}, immutable) {
		return nil, nil, serviceDenied("unknown_recovery_item_immutability")
	}
	protected := parents["protected"] == true || protectedAzureTags(object(container.data["tags"])) || text(container.data["managedBy"]) != "" || immutable == "Unlocked" || immutable == "Locked"
	if raw != nil {
		protected = protected || protectedAzureTags(object(raw["tags"])) || text(raw["managedBy"]) != ""
	}
	ready := parents["ready"] == true && object(container.data["properties"])["registrationStatus"] == "Registered"
	if raw != nil {
		ready = ready && slices.Contains([]string{"IRPending", "Protected", "ProtectionError", "ProtectionStopped", "ProtectionPaused", "BackupsSuspended"}, text(state["state"]))
	}
	maps.Copy(state, parents)
	state["protected"], state["ready"] = protected, ready
	state["container"] = c.privateConfiguration(recoveryContainerConfiguration(container.data))
	state["container_observed"] = c.privateConfiguration(container.data)
	state["guards"], state["reference_policy"], state["policy_configuration"], state["immutability"] = guards, policyID, policyConfiguration, immutable
	return state, raw, nil
}

func (c *client) recoveryItemReview(ctx context.Context, id string, known map[string]any) (map[string]any, map[string]any, error) {
	before, own, err := c.recoveryItemObservation(ctx, id, known)
	if err != nil {
		return nil, nil, err
	}
	// A second observation includes newly discovered guard IDs as own-read hints,
	// so omission from the following list cannot silently remove a dependency.
	after, current, err := c.recoveryItemObservation(ctx, id, before)
	if err != nil {
		return nil, nil, err
	}
	if (own == nil) != (current == nil) || c.privateConfiguration(before) != c.privateConfiguration(after) {
		return nil, nil, serviceDenied("recovery_item_changed_during_review")
	}
	return after, current, nil
}

func (r *Runtime) recoveryItemInventory(ctx context.Context, c *client, req contracts.InventoryRequest, item *contracts.InventoryItem) error {
	if item.Normalized["retained"] == true {
		item.Normalized["cleanup_protected"], item.Normalized["cleanup_protection_reason"] = true, "recovery_item_data_retained"
		return nil
	}
	previous := object(object(req.KnownNativeMetadata[item.NativeID])[recoveryItemReviewKey])
	known := maps.Clone(previous)
	if known == nil {
		known = map[string]any{}
	}
	hints := maps.Clone(object(known["snapshots"]))
	if hints == nil {
		hints = map[string]any{}
	}
	for _, id := range req.KnownNativeIDs {
		canonical, err := c.recoveryServicesIdentity(id, recoveryServicesItem)
		if err != nil {
			return err
		}
		if redisParentID(canonical) == redisParentID(item.NativeID) && canonical != item.NativeID {
			hints[canonical] = map[string]any{}
		}
	}
	known["snapshots"] = hints
	review, raw, err := c.recoveryItemReview(ctx, item.NativeID, known)
	if err != nil {
		return err
	}
	if raw == nil || review["observed"] != item.Normalized["_recovery_services_configuration"] || review["parent_observed"] != item.Normalized["_recovery_services_parent_configuration"] || review["container_observed"] != item.Normalized["_recovery_services_container_configuration"] || review["region"] != item.Location {
		return serviceDenied("recovery_item_inventory_changed")
	}
	allowed := review["ready"] == true && review["protected"] == false && review["retained"] == false
	item.Actionable = &allowed
	item.Normalized[recoveryItemReviewKey], item.Normalized[recoveryItemProofKey] = review, c.recoveryItemProof(item.NativeID, req.ConnectionID, review)
	item.Normalized["cleanup_protected"] = !allowed
	delete(item.Normalized, "cleanup_protection_reason")
	if !allowed {
		item.Normalized["cleanup_protection_reason"] = "recovery_item_not_ready_or_protected"
	}
	return nil
}
