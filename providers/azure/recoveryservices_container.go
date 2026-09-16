package azure

import (
	"context"
	"maps"
	"slices"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

const recoveryContainerReview = "_recovery_container_review"
const recoveryContainerProof = "_recovery_container_proof"

func (c *client) recoveryContainerProofFor(id string, connection asset.ConnectionID, review map[string]any) string {
	return c.privateConfiguration(map[string]any{"protocol": "recovery-container-review-1", "resource": id, "connection": connection, "review": review})
}

func recoveryContainerConfiguration(raw map[string]any) map[string]any {
	snapshot := hybridComputeChildSnapshot(raw)
	properties := object(snapshot["properties"])
	// Registration changes as a consequence of unregistering. Health/update
	// observations are not authored configuration or a replacement identifier.
	for _, key := range []string{"registrationStatus", "healthStatus", "lastUpdatedTime"} {
		delete(properties, key)
	}
	return snapshot
}

// Soft-deleted items remain consumers. Unregistering a source must not become
// an implicit purge or a way to hide retained backups from inventory.
func (c *client) recoveryContainerConsumers(ctx context.Context, id string, known map[string]any) (map[string]any, error) {
	owner, err := c.recoveryServicesIdentity(id, recoveryServicesContainer)
	if err != nil || owner != id {
		return nil, serviceDenied("invalid_recovery_container_owner")
	}
	candidates := map[string]bool{}
	for hint := range known {
		canonical, err := c.recoveryServicesIdentity(hint, recoveryServicesItem)
		if err != nil || canonical != hint || redisParentID(hint) != id {
			return nil, serviceDenied("invalid_recovery_container_consumer_hint")
		}
		candidates[hint] = true
	}
	listed, err := c.recoveryServicesCollection(ctx, recoveryServicesVaultID(id)+"/backupprotecteditems", recoveryServicesItem)
	if err != nil {
		return nil, err
	}
	for item := range listed {
		if redisParentID(item) == id {
			candidates[item] = true
		}
	}
	consumers := map[string]any{}
	for _, item := range slices.Sorted(maps.Keys(candidates)) {
		own, err := c.recoveryServicesRead(ctx, item, recoveryServicesItem)
		if isNotFound(err) && listed[item] == nil {
			continue
		}
		if err != nil {
			return nil, contracts.DependencyReadError(err)
		}
		if listed[item] != nil && !nativeConfigurationContains(listed[item], own.data) {
			return nil, serviceDenied("recovery_container_consumer_changed")
		}
		consumers[item] = map[string]any{"configuration": c.privateConfiguration(hybridComputeChildSnapshot(own.data)), "retained": object(own.data["properties"])["isScheduledForDeferredDelete"] == true}
	}
	return consumers, nil
}

func (c *client) recoveryContainerParents(ctx context.Context, id string) (map[string]any, error) {
	canonical, err := c.recoveryServicesIdentity(id, recoveryServicesContainer)
	if err != nil || canonical != id {
		return nil, serviceDenied("invalid_recovery_container_identity")
	}
	vault, err := c.recoveryServicesRead(ctx, recoveryServicesVaultID(id), recoveryServicesVault)
	if err != nil {
		return nil, contracts.DependencyReadError(err)
	}
	group, err := c.dataProtectionGroup(ctx, id)
	if err != nil {
		return nil, err
	}
	protected := group["protected"] == true || protectedAzureTags(object(vault.data["tags"])) || text(vault.data["managedBy"]) != ""
	return map[string]any{"vault": c.privateConfiguration(hybridComputeChildSnapshot(vault.data)), "parent_observed": c.privateConfiguration(vault.data), "group": group["configuration"], "region": resourceRegion(vault.data), "protected": protected, "ready": object(vault.data["properties"])["provisioningState"] == "Succeeded"}, nil
}

func (c *client) recoveryContainerReviewFor(ctx context.Context, id string, known map[string]any) (map[string]any, bool, error) {
	parents, err := c.recoveryContainerParents(ctx, id)
	if err != nil {
		return nil, false, err
	}
	own, err := c.recoveryServicesRead(ctx, id, recoveryServicesContainer)
	absent := isNotFound(err)
	if err != nil && !absent {
		return nil, false, err
	}
	consumers, err := c.recoveryContainerConsumers(ctx, id, known)
	if err != nil {
		return nil, false, err
	}
	hints := maps.Clone(known)
	if hints == nil {
		hints = map[string]any{}
	}
	maps.Copy(hints, consumers)
	later, err := c.recoveryContainerConsumers(ctx, id, hints)
	if err != nil {
		return nil, false, err
	}
	after, err := c.recoveryContainerParents(ctx, id)
	if err != nil {
		return nil, false, err
	}
	current, err := c.recoveryServicesRead(ctx, id, recoveryServicesContainer)
	if err != nil && !isNotFound(err) {
		return nil, false, err
	}
	if absent != isNotFound(err) || c.privateConfiguration(parents) != c.privateConfiguration(after) || c.privateConfiguration(consumers) != c.privateConfiguration(later) {
		return nil, false, serviceDenied("recovery_container_context_changed")
	}
	parents["consumers"] = consumers
	parents["observed"], parents["configuration"], parents["state"] = "", "", ""
	if !absent {
		if c.privateConfiguration(own.data) != c.privateConfiguration(current.data) {
			return nil, false, serviceDenied("recovery_container_changed_during_review")
		}
		parents["observed"] = c.privateConfiguration(own.data)
		parents["configuration"] = c.privateConfiguration(recoveryContainerConfiguration(own.data))
		parents["state"] = text(object(own.data["properties"])["registrationStatus"])
		parents["protected"] = parents["protected"] == true || protectedAzureTags(object(own.data["tags"])) || text(own.data["managedBy"]) != ""
	}
	return parents, absent, nil
}

func (r *Runtime) recoveryContainerInventory(ctx context.Context, c *client, req contracts.InventoryRequest, item *contracts.InventoryItem) error {
	known := object(object(req.KnownNativeMetadata[item.NativeID][recoveryContainerReview])["consumers"])

	review, absent, err := c.recoveryContainerReviewFor(ctx, item.NativeID, known)
	if err != nil {
		return err
	}
	if absent || review["observed"] != item.Normalized["_recovery_services_configuration"] || review["parent_observed"] != item.Normalized["_recovery_services_parent_configuration"] || review["region"] != item.Location {
		return serviceDenied("recovery_container_inventory_changed")
	}
	item.Normalized[recoveryContainerReview] = review
	item.Normalized[recoveryContainerProof] = c.recoveryContainerProofFor(item.NativeID, req.ConnectionID, review)
	allowed := review["ready"] == true && review["protected"] == false && review["state"] == "Registered" && len(object(review["consumers"])) == 0
	item.Actionable = &allowed
	item.Normalized["cleanup_protected"] = !allowed
	reason := "recovery_container_not_ready"
	if len(object(review["consumers"])) != 0 {
		reason = "recovery_container_has_backup_items"
	}
	if review["protected"] == true {
		reason = "recovery_container_protected"
	}
	item.Normalized["cleanup_protection_reason"] = reason
	if allowed {
		delete(item.Normalized, "cleanup_protection_reason")
	}
	return nil
}
