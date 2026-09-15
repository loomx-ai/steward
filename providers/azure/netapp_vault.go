package azure

import (
	"context"
	"maps"
	"slices"
	"strings"

	"github.com/loomx-ai/steward/internal/app/governance"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

const netappVaultType = netappAccountType + "/backupVaults"
const netappVaultReview = "_netapp_vault_review"
const netappVaultProof = "_netapp_vault_proof"
const netappVaultSource = "azure:netapp-vault-backups"

func (c *client) netappVaultProofFor(id string, connection asset.ConnectionID, review map[string]any) string {
	return c.privateConfiguration(map[string]any{"protocol": "netapp-vault-review-1", "id": id, "connection": connection, "review": review})
}

// Previous members are read hints, including backups omitted from a later list.
// Only each backup's own 404 can retire its association, never a missing page or
// missing parent. This method does not close any asset records.
func (c *client) netappVaultMembers(ctx context.Context, id, region string, known map[string]any) (map[string]any, error) {
	if err := c.netappIdentity(id, netappVaultType); err != nil {
		return nil, err
	}
	vault, err := c.netappRead(ctx, id, netappVaultType)
	if err != nil {
		return nil, contracts.DependencyReadError(err)
	}
	if resourceRegion(vault.data) != region {
		return nil, serviceDenied("netapp_vault_region_changed")
	}
	ids := map[string]bool{}
	for child := range known {
		if c.netappIdentity(child, netappBackupType) != nil || redisParentID(child) != id {
			return nil, serviceDenied("invalid_netapp_vault_member_hint")
		}
		ids[child] = false
	}
	rows, err := c.netappIndex(ctx, netappBackupType, id)
	if err != nil {
		return nil, contracts.DependencyReadError(err)
	}
	for _, value := range rows {
		raw := object(value)
		child := strings.ToLower(text(raw["id"]))
		if c.netappIdentity(child, netappBackupType) != nil || redisParentID(child) != id || !netappMetadata(raw, child, netappBackupType) || ids[child] {
			return nil, serviceDenied("invalid_netapp_vault_backup_index")
		}
		ids[child] = true
	}
	members := map[string]any{}
	for _, child := range slices.Sorted(maps.Keys(ids)) {
		own, err := c.netappRead(ctx, child, netappBackupType)
		if isNotFound(err) && !ids[child] {
			continue
		}
		if err != nil {
			return nil, contracts.DependencyReadError(err)
		}
		p := object(own.data["properties"])
		source := strings.ToLower(text(p["volumeResourceId"]))
		if c.netappIdentity(source, netappVolumeType) != nil || text(own.data["location"]) != "" && resourceRegion(own.data) != region {
			return nil, serviceDenied("netapp_vault_backup_context_changed")
		}
		uid, created := c.netappLeafIdentity(netappBackupType, own.data)
		_, validTime := netappRecoveryTime(created)
		ready := uuidPattern.MatchString(uid) && validTime && p["provisioningState"] == "Succeeded"
		members[child] = map[string]any{"configuration": c.privateConfiguration(own.data), "uid": uid, "created": created, "source": source, "ready": ready, "protected": protectedAzureTags(object(own.data["tags"])) || text(own.data["managedBy"]) != ""}
	}
	later, err := c.netappRead(ctx, id, netappVaultType)
	if err != nil {
		return nil, contracts.DependencyReadError(err)
	}
	if c.privateConfiguration(vault.data) != c.privateConfiguration(later.data) {
		return nil, serviceDenied("netapp_vault_changed_during_member_read")
	}
	return members, nil
}

func (r *Runtime) netappVaultInventory(ctx context.Context, c *client, req contracts.InventoryRequest, item *contracts.InventoryItem) error {
	assignment := object(item.Normalized[netappAssignmentReview])
	known := object(object(req.KnownNativeMetadata[item.NativeID][netappVaultReview])["members"])
	members, err := c.netappVaultMembers(ctx, item.NativeID, item.Location, known)
	if err != nil {
		return err
	}
	hints := maps.Clone(known)
	if hints == nil {
		hints = map[string]any{}
	}
	maps.Copy(hints, members)
	after, err := c.netappVaultMembers(ctx, item.NativeID, item.Location, hints)
	if err != nil {
		return err
	}
	if c.privateConfiguration(members) != c.privateConfiguration(after) {
		return serviceDenied("netapp_vault_members_changed")
	}
	root, err := c.netappRead(ctx, item.NativeID, netappVaultType)
	if err != nil {
		return err
	}
	parents, _, region, _, _, err := c.netappRecoveryParents(ctx, item.NativeID, netappVaultType)
	if err != nil {
		return err
	}
	consumers, complete, err := c.netappAssignmentConsumers(ctx, item.NativeID, netappVaultType, region, root.data, object(assignment["consumers"]))
	if err != nil {
		return err
	}
	if len(assignment) != 6 || !complete || region != item.Location || c.privateConfiguration(root.data) != item.Normalized["_netapp_configuration"] || c.privateConfiguration(parents) != c.privateConfiguration(object(assignment["parents"])) || c.privateConfiguration(consumers) != c.privateConfiguration(object(assignment["consumers"])) {
		return serviceDenied("netapp_vault_context_changed")
	}
	review := map[string]any{"members": members, "region": region, "configuration": item.Normalized["_netapp_configuration"], "assignments": c.privateConfiguration(assignment)}
	item.Normalized[netappVaultReview] = review
	item.Normalized[netappVaultProof] = c.netappVaultProofFor(item.NativeID, req.ConnectionID, review)
	// Membership evidence is not authorization to delete a vault or its backups.
	return nil
}

func (c *client) netappVaultContribution(parent asset.Asset, assets []asset.Asset) (governance.Contribution, error) {
	out := governance.Contribution{}
	unresolved := func(id, reason string) {
		kind := netappBackupType
		if id == parent.Identity.NativeID {
			kind = netappVaultType
		}
		out.Unresolved = append(out.Unresolved, graph.UnresolvedReference{BlocksCleanup: true, Provider: parent.Identity.Provider, ConnectionID: parent.Identity.ConnectionID, NativeType: kind, NativeID: id, ControllerID: parent.ID, Relationship: graph.RelationshipAttachedTo, Evidence: map[string]any{"reason": reason}})
	}
	review := object(parent.Normalized[netappVaultReview])
	assignment := object(parent.Normalized[netappAssignmentReview])
	if parent.Identity.Provider != asset.ProviderAzure || parent.Identity.Partition != "azure" || parent.Identity.ConnectionID == "" || parent.Identity.NativeType != netappVaultType || c.netappIdentity(parent.Identity.NativeID, netappVaultType) != nil || len(review) != 4 || review["region"] != parent.Location || review["configuration"] != parent.Normalized["_netapp_configuration"] || review["assignments"] != c.privateConfiguration(assignment) || parent.Normalized[netappVaultProof] != c.netappVaultProofFor(parent.Identity.NativeID, parent.Identity.ConnectionID, review) {
		unresolved(parent.Identity.NativeID, "netapp_vault_review_required")
		return out, nil
	}
	members := object(review["members"])
	byID := map[string]asset.Asset{}
	for _, v := range assets {
		if v.Identity.Provider != parent.Identity.Provider || v.Identity.Partition != parent.Identity.Partition || v.Identity.ConnectionID != parent.Identity.ConnectionID || v.Identity.NativeType != netappBackupType || redisParentID(v.Identity.NativeID) != parent.Identity.NativeID {
			continue
		}
		if byID[v.Identity.NativeID].ID != "" {
			return out, serviceDenied("ambiguous_netapp_vault_backup")
		}
		byID[v.Identity.NativeID] = v
		if members[v.Identity.NativeID] == nil {
			unresolved(v.Identity.NativeID, "netapp_vault_membership_requires_refresh")
		}
	}
	for _, id := range slices.Sorted(maps.Keys(members)) {
		entry := object(members[id])
		v := byID[id]
		if v.ID == "" || v.Location != parent.Location || v.Normalized["_netapp_configuration"] != entry["configuration"] {
			unresolved(id, "netapp_vault_membership_requires_refresh")
			continue
		}
		boundary, err := c.netappRecoveryRecorded(v)
		if err != nil || boundary["uid"] != entry["uid"] || boundary["created"] != entry["created"] {
			unresolved(id, "netapp_vault_backup_requires_refresh")
			continue
		}
		out.Relationships = append(out.Relationships, graph.Relationship{SourceAssetID: v.ID, TargetAssetID: parent.ID, Type: graph.RelationshipAttachedTo, Source: netappVaultSource, Evidence: map[string]any{"native_membership": true}, Confidence: 1})
	}
	return out, nil
}
