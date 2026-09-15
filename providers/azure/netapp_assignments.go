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

const netappAssignmentReview = "_netapp_assignment_review"
const netappAssignmentProof = "_netapp_assignment_proof"

var netappAssignmentFields = []struct{ kind, section, field string }{
	{netappAccountType + "/backupPolicies", "backup", "backupPolicyId"},
	{netappAccountType + "/backupVaults", "backup", "backupVaultId"},
	{netappAccountType + "/snapshotPolicies", "snapshot", "snapshotPolicyId"},
}

func netappAssignmentKind(kind string) bool {
	for _, field := range netappAssignmentFields {
		if field.kind == kind {
			return true
		}
	}
	return false
}

// Only current volume dataProtection fields describe live assignments. Disabled
// enforcement and historical backupPolicyResourceId do not remove an assignment.
func netappAssignments(raw map[string]any) (map[string]string, error) {
	p := object(raw["properties"])
	protection := object(p["dataProtection"])
	if p["dataProtection"] != nil && protection == nil {
		return nil, serviceDenied("invalid_netapp_data_protection")
	}
	out := map[string]string{}
	for _, field := range netappAssignmentFields {
		section := object(protection[field.section])
		if protection[field.section] != nil && section == nil {
			return nil, serviceDenied("invalid_netapp_assignment_section")
		}
		if v := section[field.field]; v != nil {
			wire, ok := v.(string)
			if !ok || wire != strings.TrimSpace(wire) {
				return nil, serviceDenied("invalid_netapp_assignment")
			}
			if wire != "" {
				id, kind, err := parseID(wire)
				if err != nil || !strings.EqualFold(kind, field.kind) {
					return nil, serviceDenied("invalid_netapp_assignment")
				}
				out[field.kind] = id
			}
		}
	}
	if v := object(protection["backup"])["policyEnforced"]; v != nil {
		if _, ok := v.(bool); !ok {
			return nil, serviceDenied("invalid_netapp_policy_enforcement")
		}
	}
	return out, nil
}
func (c *client) netappAssignmentProofFor(id string, connection asset.ConnectionID, review map[string]any) string {
	return c.privateConfiguration(map[string]any{"protocol": "netapp-assignments-1", "id": id, "connection": connection, "review": review})
}

func (c *client) netappAssignmentConsumers(ctx context.Context, id, kind, region string, raw map[string]any, known map[string]any) (map[string]any, bool, error) {
	account := redisParentID(id)
	pools := map[string]bool{}
	volumes := map[string]bool{}
	required := map[string]bool{}
	groupUUIDs := map[string]string{}
	// Previous observations are read hints only; every live assignment is read anew.
	for volume := range known {
		if c.netappIdentity(volume, netappVolumeType) != nil || kind == netappGroupType && redisParentID(redisParentID(volume)) != account {
			return nil, false, serviceDenied("invalid_netapp_assignment_hint")
		}
		pools[redisParentID(volume)] = false
		volumes[volume] = false
	}
	nativeComplete := true
	if kind == netappGroupType {
		rows, ok := object(raw["properties"])["volumes"].([]any)
		count, err := batchInteger(object(object(raw["properties"])["groupMetaData"])["volumesCount"], 32)
		if !ok || err != nil || count != int64(len(rows)) {
			return nil, false, serviceDenied("incomplete_netapp_group_members")
		}
		for _, value := range rows {
			v := object(value)
			target := strings.ToLower(text(v["id"]))
			if c.netappIdentity(target, netappVolumeType) != nil || redisParentID(redisParentID(target)) != account || !netappMetadata(v, target, netappVolumeType) || required[target] {
				return nil, false, serviceDenied("invalid_netapp_group_member")
			}
			if value := object(v["properties"])["fileSystemId"]; value != nil {
				uid, ok := value.(string)
				if !ok || !uuidPattern.MatchString(uid) {
					return nil, false, serviceDenied("invalid_netapp_group_volume_uuid")
				}
				groupUUIDs[target] = uid
			}
			required[target], volumes[target] = true, true
			pools[redisParentID(target)] = false
		}
	}
	if kind == netappAccountType+"/snapshotPolicies" {
		rows, err := c.netappIndexPath(ctx, id+"/volumes")
		if err != nil {
			return nil, false, contracts.DependencyReadError(err)
		}
		for _, value := range rows {
			v := object(value)
			target := strings.ToLower(text(v["id"]))
			if c.netappIdentity(target, netappVolumeType) != nil || !netappMetadata(v, target, netappVolumeType) || required[target] {
				return nil, false, serviceDenied("invalid_netapp_snapshot_policy_volumes")
			}
			required[target] = true
			volumes[target] = true
			pools[redisParentID(target)] = false
		}
	}
	if kind == netappAccountType+"/backupPolicies" {
		p := object(raw["properties"])
		if v := p["volumeBackups"]; v != nil {
			rows, ok := v.([]any)
			if !ok {
				return nil, false, serviceDenied("invalid_netapp_backup_policy_volumes")
			}
			for _, value := range rows {
				row := object(value)
				if row == nil {
					return nil, false, serviceDenied("invalid_netapp_backup_policy_volume")
				}
				// Native examples contain names without resource IDs; a name is not an ARM identity.
				if row["volumeResourceId"] == nil || row["volumeResourceId"] == "" {
					nativeComplete = false
					continue
				}
				wire, ok := row["volumeResourceId"].(string)
				target := strings.ToLower(wire)
				if !ok || c.netappIdentity(target, netappVolumeType) != nil || required[target] {
					return nil, false, serviceDenied("invalid_netapp_backup_policy_volume")
				}
				required[target] = true
				volumes[target] = true
				pools[redisParentID(target)] = false
			}
		}
	}
	rows, err := c.netappIndex(ctx, netappPoolType, account)
	if err != nil {
		return nil, false, contracts.DependencyReadError(err)
	}
	for _, value := range rows {
		v := object(value)
		target := strings.ToLower(text(v["id"]))
		if c.netappIdentity(target, netappPoolType) != nil || redisParentID(target) != account || !netappMetadata(v, target, netappPoolType) || pools[target] {
			return nil, false, serviceDenied("invalid_netapp_assignment_pool_index")
		}
		pools[target] = true
	}
	parents := map[string]any{}
	stablePools := map[string]string{}
	for _, pool := range slices.Sorted(maps.Keys(pools)) {
		own, err := c.netappRead(ctx, pool, netappPoolType)
		if isNotFound(err) && !pools[pool] {
			// A removed known pool does not make a volume record absent. Confirm
			// each former consumer's own 404 solely to reconcile its association.
			for volume, listed := range volumes {
				if redisParentID(volume) != pool {
					continue
				}
				if listed {
					return nil, false, serviceDenied("netapp_assignment_parent_missing")
				}
				_, ownErr := c.netappRead(ctx, volume, netappVolumeType)
				if !isNotFound(ownErr) {
					return nil, false, serviceDenied("netapp_assignment_parent_missing")
				}
				delete(volumes, volume)
			}
			continue
		}
		if err != nil {
			return nil, false, contracts.DependencyReadError(err)
		}
		if resourceRegion(own.data) != region {
			return nil, false, serviceDenied("netapp_assignment_pool_region_changed")
		}
		parents[pool] = c.privateConfiguration(own.data)
		stablePools[pool] = c.privateConfiguration(netappPoolSnapshot(own.data))
		rows, err := c.netappIndex(ctx, netappVolumeType, pool)
		if err != nil {
			return nil, false, contracts.DependencyReadError(err)
		}
		listed := map[string]bool{}
		for _, value := range rows {
			v := object(value)
			target := strings.ToLower(text(v["id"]))
			if c.netappIdentity(target, netappVolumeType) != nil || redisParentID(target) != pool || !netappMetadata(v, target, netappVolumeType) || listed[target] {
				return nil, false, serviceDenied("invalid_netapp_assignment_volume_index")
			}
			listed[target] = true
			volumes[target] = true
		}
	}
	consumers := map[string]any{}
	for _, volume := range slices.Sorted(maps.Keys(volumes)) {
		own, err := c.netappRead(ctx, volume, netappVolumeType)
		if isNotFound(err) && !volumes[volume] {
			continue
		}
		if err != nil {
			return nil, false, contracts.DependencyReadError(err)
		}
		if resourceRegion(own.data) != region || !uuidPattern.MatchString(text(object(own.data["properties"])["fileSystemId"])) {
			return nil, false, serviceDenied("invalid_netapp_assignment_volume_identity")
		}
		if kind == netappGroupType {
			if uid := groupUUIDs[volume]; uid != "" && uid != object(own.data["properties"])["fileSystemId"] {
				return nil, false, serviceDenied("netapp_group_volume_recreated")
			}
			name, err := netappVolumeGroupName(own.data)
			if err != nil {
				return nil, false, err
			}
			if required[volume] && name != "" && name != last(id) || !required[volume] && (name == last(id) || name == "" && known[volume] != nil) {
				return nil, false, serviceDenied("netapp_group_membership_changed")
			}
			if required[volume] {
				consumers[volume] = map[string]any{"configuration": c.privateConfiguration(own.data), "uid": object(own.data["properties"])["fileSystemId"], "pool": stablePools[redisParentID(volume)]}
			}
			continue
		}
		assignments, err := netappAssignments(own.data)
		if err != nil {
			return nil, false, err
		}
		if assignments[kind] != id {
			if required[volume] {
				return nil, false, serviceDenied("netapp_assignment_index_changed")
			}
			continue
		}
		consumers[volume] = map[string]any{"configuration": c.privateConfiguration(own.data), "uid": object(own.data["properties"])["fileSystemId"], "pool": stablePools[redisParentID(volume)], "detached_configuration": c.privateConfiguration(netappDetachedPolicyVolume(own.data, kind))}
		if kind == netappVaultType {
			entry := object(consumers[volume])
			policy := assignments[netappBackupPolicyType]
			entry["policy"], entry["policy_configuration"], entry["policy_ready"] = policy, "", true
			if policy != "" {
				ownPolicy, err := c.netappRead(ctx, policy, netappBackupPolicyType)
				if err != nil {
					return nil, false, contracts.DependencyReadError(err)
				}
				if resourceRegion(ownPolicy.data) != region {
					return nil, false, serviceDenied("netapp_vault_policy_identity_unavailable")
				}
				entry["policy_configuration"] = c.privateConfiguration(netappPolicySnapshot(ownPolicy.data, netappBackupPolicyType))
				entry["policy_ready"] = uuidPattern.MatchString(text(object(ownPolicy.data["properties"])["backupPolicyId"])) && object(ownPolicy.data["properties"])["provisioningState"] == "Succeeded"
			}
		}
	}
	if kind == netappAccountType+"/backupPolicies" {
		if value := object(raw["properties"])["volumesAssigned"]; value != nil {
			count, err := batchInteger(value, 32)
			if err != nil || count < 0 {
				return nil, false, serviceDenied("invalid_netapp_assignment_count")
			}
			nativeComplete = nativeComplete && count == int64(len(consumers))
		}
	}
	// A concurrent parent or pool change invalidates the collected observations.
	for pool, hash := range parents {
		own, err := c.netappRead(ctx, pool, netappPoolType)
		if err != nil {
			return nil, false, contracts.DependencyReadError(err)
		}
		if c.privateConfiguration(own.data) != hash {
			return nil, false, serviceDenied("netapp_assignment_pool_changed")
		}
	}
	return consumers, nativeComplete, nil
}
func (r *Runtime) netappAssignmentInventory(ctx context.Context, c *client, req contracts.InventoryRequest, item *contracts.InventoryItem, raw map[string]any) error {
	id, kind := item.NativeID, item.NativeType
	known := object(object(req.KnownNativeMetadata[id][netappAssignmentReview])["consumers"])
	parents, _, region, protected, ready, err := c.netappRecoveryParents(ctx, id, kind)
	if err != nil {
		return err
	}
	if region != item.Location {
		return serviceDenied("netapp_assignment_region_changed")
	}
	first, complete, err := c.netappAssignmentConsumers(ctx, id, kind, region, raw, known)
	if err != nil {
		return err
	}
	second, laterComplete, err := c.netappAssignmentConsumers(ctx, id, kind, region, raw, first)
	if err != nil {
		return err
	}
	own, err := c.netappRead(ctx, id, kind)
	if err != nil {
		return err
	}
	after, _, location, _, _, err := c.netappRecoveryParents(ctx, id, kind)
	if err != nil {
		return err
	}
	if complete != laterComplete || c.privateConfiguration(first) != c.privateConfiguration(second) || c.privateConfiguration(own.data) != item.Normalized["_netapp_configuration"] || c.privateConfiguration(parents) != c.privateConfiguration(after) || location != region {
		return serviceDenied("netapp_assignments_changed")
	}
	review := map[string]any{"consumers": first, "native_index_complete": complete, "parents": parents, "region": region, "configuration": item.Normalized["_netapp_configuration"], "policy_configuration": c.privateConfiguration(netappPolicySnapshot(raw, kind))}
	if netappPolicyKind(kind) {
		allowed := ready && !protected && object(raw["properties"])["provisioningState"] == "Succeeded" && !protectedAzureTags(object(raw["tags"])) && text(raw["managedBy"]) == "" && complete && (kind != netappBackupPolicyType || uuidPattern.MatchString(text(object(raw["properties"])["backupPolicyId"])))
		item.Actionable = &allowed
		item.Normalized["cleanup_protected"] = !allowed
		if allowed {
			delete(item.Normalized, "cleanup_protection_reason")
		}
	}
	item.Normalized[netappAssignmentReview] = review
	item.Normalized[netappAssignmentProof] = c.netappAssignmentProofFor(id, req.ConnectionID, review)
	return nil
}

func (c *client) netappAssignmentContribution(parent asset.Asset, assets []asset.Asset) (governance.Contribution, error) {
	result := governance.Contribution{}
	review := object(parent.Normalized[netappAssignmentReview])
	unresolved := func(id, reason string) {
		kind := netappVolumeType
		if id == parent.Identity.NativeID {
			kind = parent.Identity.NativeType
		}
		result.Unresolved = append(result.Unresolved, graph.UnresolvedReference{BlocksCleanup: true, Provider: parent.Identity.Provider, ConnectionID: parent.Identity.ConnectionID, NativeType: kind, NativeID: id, ControllerID: parent.ID, Relationship: graph.RelationshipUses, Evidence: map[string]any{"reason": reason}})
	}
	if len(review) != 6 || review["region"] != parent.Location || review["configuration"] != parent.Normalized["_netapp_configuration"] || parent.Normalized[netappAssignmentProof] != c.netappAssignmentProofFor(parent.Identity.NativeID, parent.Identity.ConnectionID, review) {
		unresolved(parent.Identity.NativeID, "netapp_assignment_review_required")
		return result, nil
	}
	if review["native_index_complete"] != true {
		unresolved(parent.Identity.NativeID, "netapp_assignment_native_index_incomplete")
	}
	byID := map[string]asset.Asset{}
	for _, value := range assets {
		if value.Identity.Provider == parent.Identity.Provider && value.Identity.ConnectionID == parent.Identity.ConnectionID && value.Identity.Partition == parent.Identity.Partition && value.Identity.NativeType == netappVolumeType {
			if byID[value.Identity.NativeID].ID != "" {
				return result, serviceDenied("ambiguous_netapp_assignment_volume")
			}
			byID[value.Identity.NativeID] = value
		}
	}
	for _, id := range slices.Sorted(maps.Keys(object(review["consumers"]))) {
		entry := object(object(review["consumers"])[id])
		value := byID[id]
		if value.ID == "" || value.Location != parent.Location || value.Normalized["_netapp_configuration"] != entry["configuration"] || value.Normalized["fileSystemId"] != entry["uid"] {
			unresolved(id, "netapp_assignment_volume_requires_refresh")
			continue
		}
		if netappPolicyKind(parent.Identity.NativeType) {
			result.Bindings = append(result.Bindings, graph.LifecycleBinding{ControllerAssetID: parent.ID, ManagedAssetID: value.ID, Authority: graph.AuthorityAuthoritative, Ownership: graph.OwnershipReferenced, CleanupPolicy: graph.CleanupRetain, DirectCleanupAllowed: true, EvidenceSource: "azure:netapp-assignments", Evidence: map[string]any{"resource_type": netappVolumeType, "instance_id": id, "delete_by_default": false, "retention_supported": true}, Confidence: 1})
		}
		result.Relationships = append(result.Relationships, graph.Relationship{SourceAssetID: value.ID, TargetAssetID: parent.ID, Type: graph.RelationshipUses, Source: "azure:netapp-assignments", Evidence: map[string]any{"current_assignment": true}, Confidence: 1})
	}
	return result, nil
}
