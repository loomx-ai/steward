package azure

import (
	"context"
	"maps"
	"slices"
	"strings"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

// Guard proxies are read-only security dependencies, not cleanup targets.
const dataProtectionGuardProxy = dataProtectionVault + "/backupResourceGuardProxies"

func (c *client) dataProtectionGuardProxies(ctx context.Context, vault string, known map[string]any) (map[string]any, error) {
	canonical, err := c.dataProtectionIdentity(vault, dataProtectionVault)
	if err != nil || vault != canonical {
		return nil, serviceDenied("invalid_guard_proxy_vault")
	}
	ids := map[string]bool{}
	for id := range known {
		canonical, err := c.dataProtectionIdentity(id, dataProtectionGuardProxy)
		if err != nil || id != canonical || redisParentID(id) != vault {
			return nil, serviceDenied("invalid_guard_proxy_hint")
		}
		ids[id] = true
	}
	rows, err := c.dataProtectionCollection(ctx, vault+"/backupresourceguardproxies", dataProtectionGuardProxy)
	if err != nil {
		return nil, err
	}
	for id := range rows {
		ids[id] = true
	}
	result := map[string]any{}
	for _, id := range slices.Sorted(maps.Keys(ids)) {
		own, err := c.dataProtectionRead(ctx, id, dataProtectionGuardProxy)
		if isNotFound(err) && rows[id] == nil {
			continue
		}
		if err != nil {
			return nil, contracts.DependencyReadError(err)
		}
		if rows[id] != nil && !nativeConfigurationContains(rows[id], own.data) {
			return nil, serviceDenied("guard_proxy_changed_during_read")
		}
		result[id] = c.privateConfiguration(hybridComputeChildSnapshot(own.data))
	}
	return result, nil
}

// Keep policies, active instances and retained instances distinct. A retained
// instance is neither an active deletion prerequisite nor proof of purge.
func (c *client) dataProtectionVaultChildren(ctx context.Context, vault string, known map[string]any) (map[string]any, error) {
	canonical, err := c.dataProtectionIdentity(vault, dataProtectionVault)
	if err != nil || canonical != vault {
		return nil, serviceDenied("invalid_backup_vault_child_scope")
	}
	kinds := []string{dataProtectionPolicy, dataProtectionInstance, dataProtectionDeletedInstance}
	ids := map[string]string{}
	listed := map[string]map[string]any{}
	for id, value := range known {
		kind := text(object(value)["kind"])
		canonical, err := c.dataProtectionIdentity(id, kind)
		if err != nil || canonical != id || redisParentID(id) != vault || !slices.Contains(kinds, kind) {
			return nil, serviceDenied("invalid_backup_vault_child_hint")
		}
		ids[id] = kind
	}
	for _, kind := range kinds {
		rows, err := c.dataProtectionCollection(ctx, vault+"/"+strings.ToLower(last(kind)), kind)
		if err != nil {
			return nil, err
		}
		for id, raw := range rows {
			ids[id] = kind
			listed[id] = raw
		}
	}
	children := map[string]any{}
	for _, id := range slices.Sorted(maps.Keys(ids)) {
		own, err := c.dataProtectionRead(ctx, id, ids[id])
		if isNotFound(err) && listed[id] == nil {
			continue
		}
		if err != nil {
			return nil, contracts.DependencyReadError(err)
		}
		if listed[id] != nil && !nativeConfigurationContains(listed[id], own.data) {
			return nil, serviceDenied("backup_vault_child_changed_during_read")
		}
		children[id] = map[string]any{"kind": ids[id], "configuration": c.privateConfiguration(hybridComputeChildSnapshot(own.data))}
	}
	return children, nil
}

func (c *client) dataProtectionVaultDependencies(ctx context.Context, vault string, known map[string]any) (map[string]any, error) {
	before, err := c.dataProtectionRead(ctx, vault, dataProtectionVault)
	if err != nil {
		return nil, contracts.DependencyReadError(err)
	}
	children, err := c.dataProtectionVaultChildren(ctx, vault, object(known["children"]))
	if err != nil {
		return nil, err
	}
	guards, err := c.dataProtectionGuardProxies(ctx, vault, object(known["guards"]))
	if err != nil {
		return nil, err
	}
	childHints := batchClone(object(known["children"]))
	if childHints == nil {
		childHints = map[string]any{}
	}
	maps.Copy(childHints, children)
	guardHints := batchClone(object(known["guards"]))
	if guardHints == nil {
		guardHints = map[string]any{}
	}
	maps.Copy(guardHints, guards)
	laterChildren, err := c.dataProtectionVaultChildren(ctx, vault, childHints)
	if err != nil {
		return nil, err
	}
	laterGuards, err := c.dataProtectionGuardProxies(ctx, vault, guardHints)
	if err != nil {
		return nil, err
	}
	after, err := c.dataProtectionRead(ctx, vault, dataProtectionVault)
	if err != nil {
		return nil, contracts.DependencyReadError(err)
	}
	if c.privateConfiguration(before.data) != c.privateConfiguration(after.data) || c.privateConfiguration(children) != c.privateConfiguration(laterChildren) || c.privateConfiguration(guards) != c.privateConfiguration(laterGuards) {
		return nil, serviceDenied("backup_vault_dependencies_changed_during_read")
	}
	return map[string]any{"observed": c.privateConfiguration(before.data), "configuration": c.privateConfiguration(hybridComputeChildSnapshot(before.data)), "children": children, "guards": guards, "region": resourceRegion(before.data)}, nil
}

func (c *client) dataProtectionGroup(ctx context.Context, id string) (map[string]any, error) {
	canonical, _, err := parseID(id)
	if err != nil || canonical != id || !strings.HasPrefix(id, c.root()+"/") {
		return nil, serviceDenied("invalid_backup_group_scope")
	}
	groupID := strings.Join(strings.Split(id, "/")[:5], "/")
	group, err := c.request(ctx, "GET", apiURL(groupID, resourcesVersion))
	if err != nil {
		return nil, contracts.DependencyReadError(err)
	}
	if !validResourceResponse(group, groupID, groupType) || operationLocation(group.header) != "" {
		return nil, serviceDenied("invalid_backup_resource_group")
	}
	locks, err := c.managementLocks(ctx)
	if err != nil {
		return nil, contracts.DependencyReadError(err)
	}
	snapshot := hybridComputeChildSnapshot(group.data)
	snapshot["type"] = strings.ToLower(groupType)
	protected := locked(id, locks) || protectedAzureTags(object(group.data["tags"])) || text(group.data["managedBy"]) != ""
	return map[string]any{"configuration": c.privateConfiguration(snapshot), "protected": protected}, nil
}

// Original vault IDs describe retained history, not ownership of a currently
// active same-name vault. Keep every distinct deletion identity independently.
func (c *client) dataProtectionRetainedVaults(ctx context.Context, vault, region string, known map[string]any) (map[string]any, error) {
	canonical, err := c.dataProtectionIdentity(vault, dataProtectionVault)
	if err != nil || canonical != vault || !cosmosOperationRegion.MatchString(region) || region != strings.ToLower(region) {
		return nil, serviceDenied("invalid_retained_vault_scope")
	}
	ids := map[string]bool{}
	for id := range known {
		canonical, err := c.dataProtectionIdentity(id, dataProtectionDeletedVault)
		if err != nil || id != canonical || strings.Split(id, "/")[6] != region {
			return nil, serviceDenied("invalid_retained_vault_hint")
		}
		ids[id] = true
	}
	path := c.root() + "/providers/microsoft.dataprotection/locations/" + region + "/deletedvaults"
	rows, err := c.dataProtectionCollection(ctx, path, dataProtectionDeletedVault)
	if err != nil {
		return nil, err
	}
	for id := range rows {
		ids[id] = true
	}
	result := map[string]any{}
	for _, id := range slices.Sorted(maps.Keys(ids)) {
		own, err := c.dataProtectionRead(ctx, id, dataProtectionDeletedVault)
		if isNotFound(err) && rows[id] == nil {
			continue
		}
		if err != nil {
			return nil, contracts.DependencyReadError(err)
		}
		if rows[id] != nil && !nativeConfigurationContains(rows[id], own.data) {
			return nil, serviceDenied("retained_vault_changed_during_read")
		}
		props := object(own.data["properties"])
		if !strings.EqualFold(text(props["originalBackupVaultId"]), vault) {
			if known[id] != nil {
				return nil, serviceDenied("retained_vault_origin_changed")
			}
			continue
		}
		deletion := object(props["resourceDeletionInfo"])
		result[id] = map[string]any{"configuration": c.privateConfiguration(own.data), "original_vault_id": vault, "deletion_time": deletion["deletionTime"], "scheduled_purge_time": deletion["scheduledPurgeTime"]}
	}
	return result, nil
}

const dataProtectionVaultReview = "_data_protection_vault_review"
const dataProtectionVaultProof = "_data_protection_vault_proof"

func (c *client) dataProtectionVaultProofFor(id string, connection asset.ConnectionID, review map[string]any) string {
	return c.privateConfiguration(map[string]any{"protocol": "data-protection-vault-review-1", "id": id, "connection": connection, "review": review})
}
func (c *client) dataProtectionVaultReviewFor(ctx context.Context, id string, known map[string]any) (map[string]any, error) {
	group, err := c.dataProtectionGroup(ctx, id)
	if err != nil {
		return nil, err
	}
	review, err := c.dataProtectionVaultDependencies(ctx, id, known)
	if err != nil {
		return nil, err
	}
	retained, err := c.dataProtectionRetainedVaults(ctx, id, text(review["region"]), object(known["retained"]))
	if err != nil {
		return nil, err
	}
	after, err := c.dataProtectionGroup(ctx, id)
	if err != nil {
		return nil, err
	}
	own, err := c.dataProtectionRead(ctx, id, dataProtectionVault)
	if err != nil {
		return nil, contracts.DependencyReadError(err)
	}
	if c.privateConfiguration(group) != c.privateConfiguration(after) || c.privateConfiguration(own.data) != review["observed"] {
		return nil, serviceDenied("backup_vault_context_changed_during_review")
	}
	review["group"] = group["configuration"]
	review["protected"] = group["protected"] == true || protectedAzureTags(object(own.data["tags"])) || text(own.data["managedBy"]) != ""
	review["ready"] = object(own.data["properties"])["provisioningState"] == "Succeeded"
	for _, value := range object(review["children"]) {
		if object(value)["kind"] != dataProtectionDeletedInstance {
			review["ready"] = false
		}
	}
	review["retained"] = retained
	return review, nil
}
func (r *Runtime) dataProtectionVaultInventory(ctx context.Context, c *client, req contracts.InventoryRequest, item *contracts.InventoryItem) error {
	known := object(req.KnownNativeMetadata[item.NativeID][dataProtectionVaultReview])
	review, err := c.dataProtectionVaultReviewFor(ctx, item.NativeID, known)
	if err != nil {
		return err
	}
	if review["observed"] != item.Normalized["_data_protection_configuration"] || review["region"] != item.Location {
		return serviceDenied("backup_vault_inventory_changed")
	}
	item.Normalized[dataProtectionVaultReview] = review
	item.Normalized[dataProtectionVaultProof] = c.dataProtectionVaultProofFor(item.NativeID, req.ConnectionID, review)
	allowed := review["ready"] == true && review["protected"] == false
	item.Actionable = &allowed
	item.Normalized["cleanup_protected"] = !allowed
	if allowed {
		delete(item.Normalized, "cleanup_protection_reason")
	} else {
		item.Normalized["cleanup_protection_reason"] = "backup_vault_protected_or_has_dependencies"
	}
	return nil
}
