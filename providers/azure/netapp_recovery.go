package azure

import (
	"context"
	"encoding/json"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

type netappRecoveryCacheKey struct{}
type netappSourceObservation struct {
	res response
	err error
}
type netappRecoveryInventoryCache struct {
	backups map[string]map[string]any
	sources map[string]netappSourceObservation
}

const netappSnapshotType = netappVolumeType + "/snapshots"
const netappBackupType = netappAccountType + "/backupVaults/backups"
const netappRecoveryReview = "_netapp_recovery_review"
const netappRecoveryProof = "_netapp_recovery_proof"

func netappRecoveryKind(kind string) bool {
	return kind == netappSnapshotType || kind == netappBackupType
}
func (c *client) netappRecoveryProofFor(id string, connection asset.ConnectionID, review map[string]any) string {
	return c.privateConfiguration(map[string]any{"protocol": "netapp-recovery-review-1", "id": id, "connection": connection, "review": review})
}
func netappRecoveryTime(value any) (time.Time, bool) {
	stamp, err := time.Parse(time.RFC3339Nano, text(value))
	return stamp, err == nil && !stamp.IsZero()
}
func netappRecoveryIncarnation(kind string, raw map[string]any) string {
	p := object(raw["properties"])
	switch kind {
	case netappVolumeType:
		return text(p["fileSystemId"])
	case netappPoolType:
		return text(p["poolId"])
	case netappSnapshotType:
		return text(p["snapshotId"])
	case netappBackupType:
		return text(p["backupId"])
	}
	return ""
}

// Retained backups belong to their vault, not to their source volume. The
// source is read only to evaluate the native latest-backup restriction.
func (c *client) netappBackupRetention(ctx context.Context, id string, raw map[string]any, known map[string]any, region string) (map[string]any, map[string]any, error) {
	props := object(raw["properties"])
	source := strings.ToLower(text(props["volumeResourceId"]))
	if c.netappIdentity(source, netappVolumeType) != nil {
		return nil, nil, serviceDenied("invalid_netapp_backup_source")
	}
	account := strings.Join(strings.Split(id, "/")[:9], "/")
	vaultKind := netappAccountType + "/backupVaults"
	cache, _ := ctx.Value(netappRecoveryCacheKey{}).(*netappRecoveryInventoryCache)
	for target := range known {
		if c.netappIdentity(target, netappBackupType) != nil || strings.Join(strings.Split(target, "/")[:9], "/") != account {
			return nil, nil, serviceDenied("invalid_netapp_backup_hint")
		}
		if cache != nil && cache.backups[target] == nil {
			cache = nil
		}
	}
	ids := map[string]bool{id: true}
	if cache != nil {
		// The surrounding native inventory pass has already performed own GETs,
		// parent/region checks and full rereads. A fresh cache is used for each
		// of its two passes. Mutation review never receives this cache.
		for target := range cache.backups {
			if strings.Join(strings.Split(target, "/")[:9], "/") == account {
				ids[target] = true
			}
		}
	} else {
		vaults := map[string]bool{redisParentID(id): true}
		rows, err := c.netappIndex(ctx, vaultKind, account)
		if err != nil {
			return nil, nil, contracts.DependencyReadError(err)
		}
		for _, row := range rows {
			v := object(row)
			target := strings.ToLower(text(v["id"]))
			if c.netappIdentity(target, vaultKind) != nil || redisParentID(target) != account || !netappMetadata(v, target, vaultKind) {
				return nil, nil, serviceDenied("invalid_netapp_backup_vault_index")
			}
			vaults[target] = true
		}
		for target := range known {
			if c.netappIdentity(target, netappBackupType) != nil || strings.Join(strings.Split(target, "/")[:9], "/") != account {
				return nil, nil, serviceDenied("invalid_netapp_backup_hint")
			}
			vaults[redisParentID(target)] = true
		}
		for target := range known {
			ids[target] = true
		}
		for _, vault := range slices.Sorted(maps.Keys(vaults)) {
			vaultRead, err := c.netappRead(ctx, vault, vaultKind)
			if err != nil {
				return nil, nil, contracts.DependencyReadError(err)
			}
			if resourceRegion(vaultRead.data) != region {
				return nil, nil, serviceDenied("netapp_backup_peer_vault_region_changed")
			}
			rows, err := c.netappIndex(ctx, netappBackupType, vault)
			if err != nil {
				return nil, nil, contracts.DependencyReadError(err)
			}
			seen := map[string]bool{}
			for _, row := range rows {
				v := object(row)
				target := strings.ToLower(text(v["id"]))
				if c.netappIdentity(target, netappBackupType) != nil || redisParentID(target) != vault || !netappMetadata(v, target, netappBackupType) || seen[target] {
					return nil, nil, serviceDenied("invalid_netapp_backup_index")
				}
				seen[target] = true
				ids[target] = true
			}
		}
	}
	peers := map[string]any{}
	latest := true
	orderKnown := true
	count := 0
	ownTime, validOwnTime := netappRecoveryTime(props["snapshotCreationDate"])
	for _, target := range slices.Sorted(maps.Keys(ids)) {
		var res response
		var err error
		if cache != nil {
			res = response{data: cache.backups[target], status: 200}
		} else {
			res, err = c.netappRead(ctx, target, netappBackupType)
		}
		if isNotFound(err) {
			if target == id {
				return nil, nil, serviceDenied("netapp_backup_disappeared_during_review")
			}
			continue
		}
		if err != nil {
			return nil, nil, contracts.DependencyReadError(err)
		}
		if target == id && c.privateConfiguration(raw) != c.privateConfiguration(res.data) {
			return nil, nil, serviceDenied("netapp_backup_changed_during_review")
		}
		p := object(res.data["properties"])
		peerSource := strings.ToLower(text(p["volumeResourceId"]))
		if c.netappIdentity(peerSource, netappVolumeType) != nil {
			return nil, nil, serviceDenied("invalid_netapp_backup_peer_source")
		}
		if peerSource != source {
			continue
		}
		peers[target] = c.privateConfiguration(res.data)
		count++
		stamp, valid := netappRecoveryTime(p["snapshotCreationDate"])
		if p["provisioningState"] == "Succeeded" {
			orderKnown = orderKnown && valid && validOwnTime && uuidPattern.MatchString(text(p["backupId"]))
		}
		if valid && validOwnTime && stamp.After(ownTime) && p["provisioningState"] == "Succeeded" && uuidPattern.MatchString(text(p["backupId"])) && p["backupId"] != props["backupId"] {
			latest = false
		}
	}
	var volume response
	var err error
	observed, found := netappSourceObservation{}, false
	if cache != nil {
		observed, found = cache.sources[source]
	}
	if found {
		volume, err = observed.res, observed.err
	} else {
		volume, err = c.netappRead(ctx, source, netappVolumeType)
		if cache != nil {
			cache.sources[source] = netappSourceObservation{res: volume, err: err}
		}
	}
	absent := isNotFound(err)
	if err != nil && !absent {
		return nil, nil, contracts.DependencyReadError(err)
	}
	policy := ""
	fingerprint := ""
	sourceReady := true
	if !absent {
		p := object(volume.data["properties"])
		fingerprint = c.privateConfiguration(volume.data)
		sourceReady = p["provisioningState"] == "Succeeded"
		protection := object(p["dataProtection"])
		if p["dataProtection"] != nil && protection == nil {
			return nil, nil, serviceDenied("invalid_netapp_source_data_protection")
		}
		backup := object(protection["backup"])
		if protection["backup"] != nil && backup == nil {
			return nil, nil, serviceDenied("invalid_netapp_source_backup_assignment")
		}
		if v, exists := backup["backupPolicyId"]; exists && v != nil {
			var ok bool
			policy, ok = v.(string)
			if !ok || policy != "" && c.netappIdentity(strings.ToLower(policy), netappAccountType+"/backupPolicies") != nil {
				return nil, nil, serviceDenied("invalid_netapp_current_backup_policy")
			}
			policy = strings.ToLower(policy)
		}
	}
	// Same snapshot timestamp means a tie for latest, even when creationDate
	// differs. The historical policy on a backup is not the volume's assignment.
	// Nullable snapshot timestamps do not prove that a backup is latest.
	// With otherwise complete scope evidence, leave that opaque restriction to
	// native DELETE, which rejects protected latest backups without force.
	allowed := absent || policy == "" || !latest || !orderKnown
	return map[string]any{"source": source, "source_configuration": fingerprint, "source_absent": absent, "policy": policy, "source_ready": sourceReady, "latest": latest, "last": count == 1, "order_known": orderKnown, "allowed": allowed}, peers, nil
}
func (c *client) netappRecoveryParents(ctx context.Context, id, kind string) (map[string]any, map[string]any, string, bool, bool, error) {
	hashes := map[string]any{}
	incarnations := map[string]any{}
	region := ""
	protected, ready := false, true
	for parent, typ := redisParentID(id), netappKind(kind).parent; typ != ""; parent, typ = redisParentID(parent), netappKind(typ).parent {
		res, err := c.netappRead(ctx, parent, typ)
		if err != nil {
			return nil, nil, "", false, false, contracts.DependencyReadError(err)
		}
		location := resourceRegion(res.data)
		if region != "" && region != location {
			return nil, nil, "", false, false, serviceDenied("netapp_recovery_parent_region_changed")
		}
		region = location
		hashes[parent] = c.privateConfiguration(res.data)
		protected = protected || protectedAzureTags(object(res.data["tags"])) || text(res.data["managedBy"]) != ""
		props := object(res.data["properties"])
		ready = ready && props["provisioningState"] == "Succeeded"
		if typ == netappVolumeType || typ == netappPoolType {
			uid := netappRecoveryIncarnation(typ, res.data)
			incarnations[parent] = uid
			ready = ready && uuidPattern.MatchString(uid)
		}
		if typ == netappVolumeType {
			ready = ready && (props["isRestoring"] == nil || props["isRestoring"] == false) && (props["cloneProgress"] == nil || props["cloneProgress"] == json.Number("100"))
			peers, err := c.netappActiveReplications(ctx, parent)
			if err != nil {
				return nil, nil, "", false, false, err
			}
			hashes["replications"] = c.privateConfiguration(map[string]any{"peers": peers})
			if props["volumeType"] == "DataProtection" && len(peers) != 0 {
				ready = false
			}
			if netappIndependentChild(kind) {
				// Quota changes on a replication source propagate to its destination.
				// This action has no reviewed remote impact ledger and never terminates
				// replication or mutates destination rules implicitly.
				ready = ready && len(peers) == 0
				if kind == netappSubvolumeType {
					ready = ready && props["enableSubvolumes"] == "Enabled" && (props["volumeType"] == nil || props["volumeType"] == "Regular")
				}
			}
		}
	}
	group := strings.Join(strings.Split(id, "/")[:5], "/")
	res, err := c.request(ctx, "GET", apiURL(group, resourcesVersion))
	if err != nil {
		return nil, nil, "", false, false, contracts.DependencyReadError(err)
	}
	if !validResourceResponse(res, group, groupType) || operationLocation(res.header) != "" {
		return nil, nil, "", false, false, serviceDenied("invalid_netapp_recovery_group")
	}
	hashes[group] = c.privateConfiguration(res.data)
	protected = protected || protectedAzureTags(object(res.data["tags"])) || text(res.data["managedBy"]) != ""
	locks, err := c.managementLocks(ctx)
	if err != nil {
		return nil, nil, "", false, false, contracts.DependencyReadError(err)
	}
	return hashes, incarnations, region, protected || locked(id, locks), ready, nil
}
func (c *client) netappRecoveryBoundary(ctx context.Context, id, kind string, known map[string]any) (map[string]any, bool, error) {
	if !netappDirectLeaf(kind) || c.netappIdentity(id, kind) != nil {
		return nil, false, serviceDenied("invalid_netapp_recovery_owner")
	}
	parents, incarnations, region, protected, ready, err := c.netappRecoveryParents(ctx, id, kind)
	if err != nil {
		return nil, false, err
	}
	own, err := c.netappRead(ctx, id, kind)
	absent := isNotFound(err)
	if err != nil && !absent {
		return nil, false, err
	}
	review := map[string]any{"parents": parents, "incarnations": incarnations, "region": region, "protected": protected, "ready": ready, "resource": "", "uid": "", "created": "", "retention": map[string]any{}, "siblings": map[string]any{}}
	if !absent {
		props := object(own.data["properties"])
		uid, created := c.netappLeafIdentity(kind, own.data)
		review["resource"], review["uid"], review["created"] = c.privateConfiguration(own.data), uid, created
		if netappIndependentChild(kind) {
			ready = ready && netappIndependentChildReady(kind, own.data)
		} else {
			_, validDate := netappRecoveryTime(created)
			ready = ready && validDate && uuidPattern.MatchString(uid) && props["provisioningState"] == "Succeeded"
		}
		if loc := text(own.data["location"]); loc != "" && resourceRegion(own.data) != region {
			return nil, false, serviceDenied("netapp_recovery_region_changed")
		}
		protected = protected || protectedAzureTags(object(own.data["tags"])) || text(own.data["managedBy"]) != ""
		if kind == netappBackupType {
			retention, peers, err := c.netappBackupRetention(ctx, id, own.data, known, region)
			if err != nil {
				return nil, false, err
			}
			review["retention"], review["siblings"] = retention, peers
			ready = ready && retention["allowed"] == true && retention["source_ready"] == true
			after, later, err := c.netappBackupRetention(ctx, id, own.data, peers, region)
			if err != nil {
				return nil, false, err
			}
			if c.privateConfiguration(retention) != c.privateConfiguration(after) || c.privateConfiguration(peers) != c.privateConfiguration(later) {
				return nil, false, serviceDenied("netapp_backup_retention_changed")
			}
		}
	}
	after, other, location, guard, currentReady, err := c.netappRecoveryParents(ctx, id, kind)
	if err != nil {
		return nil, false, err
	}
	if c.privateConfiguration(parents) != c.privateConfiguration(after) || c.privateConfiguration(incarnations) != c.privateConfiguration(other) || region != location {
		return nil, false, serviceDenied("netapp_recovery_parents_changed")
	}
	if absent && (!ready || !currentReady) {
		return nil, false, serviceDenied("netapp_recovery_lookup_unavailable")
	}
	review["protected"], review["ready"] = protected || guard, ready && currentReady
	return review, absent, nil
}
func (r *Runtime) netappRecoveryInventory(ctx context.Context, c *client, req contracts.InventoryRequest, item *contracts.InventoryItem) error {
	// Siblings are review evidence, not owned children. Inventory's complete
	// collection already reconciles req.KnownNativeIDs; carrying closed sibling
	// IDs forward would strand a vault after another vault was removed.
	var known map[string]any
	review, absent, err := c.netappRecoveryBoundary(ctx, item.NativeID, item.NativeType, known)
	if err != nil {
		return err
	}
	if absent || review["resource"] != item.Normalized["_netapp_configuration"] || review["region"] != item.Location {
		return serviceDenied("netapp_recovery_inventory_changed")
	}
	item.Normalized[netappRecoveryReview], item.Normalized[netappRecoveryProof] = review, c.netappRecoveryProofFor(item.NativeID, req.ConnectionID, review)
	allowed := review["ready"] == true && review["protected"] == false
	item.Actionable = &allowed
	item.Normalized["cleanup_protected"] = !allowed
	delete(item.Normalized, "controller_only")
	if allowed {
		delete(item.Normalized, "cleanup_protection_reason")
	}
	return nil
}
