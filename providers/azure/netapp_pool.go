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

const netappPoolReview = "_netapp_pool_review"
const netappPoolProof = "_netapp_pool_proof"
const netappPoolLifecycleSource = "azure:netapp-pool-volumes"

func netappPoolSnapshot(raw map[string]any) map[string]any {
	out := hybridComputeChildSnapshot(raw)
	// Both fields are readOnly in the pinned PoolProperties schema. Utilization
	// changes when a reviewed volume prerequisite is deleted. All writable and
	// unknown properties, native poolId and creation metadata remain bound.
	delete(object(out["properties"]), "totalThroughputMibps")
	delete(object(out["properties"]), "utilizedThroughputMibps")
	return out
}
func (c *client) netappPoolProofFor(id string, connection asset.ConnectionID, review map[string]any) string {
	return c.privateConfiguration(map[string]any{"protocol": "netapp-pool-review-1", "id": id, "connection": connection, "review": review})
}
func (c *client) netappPoolRecorded(value asset.Asset) (map[string]any, error) {
	id := value.Identity.NativeID
	review := object(value.Normalized[netappPoolReview])
	if value.ID == "" || value.Identity.Provider != asset.ProviderAzure || value.Identity.Partition != "azure" || value.Identity.ConnectionID == "" || value.Identity.NativeType != netappPoolType || c.netappIdentity(id, netappPoolType) != nil || len(review) != 8 || review["region"] != value.Location || review["raw"] != value.Normalized["_netapp_configuration"] || object(review["members"]) == nil || value.Normalized[netappPoolProof] != c.netappPoolProofFor(id, value.Identity.ConnectionID, review) {
		return nil, serviceDenied("invalid_netapp_pool_review")
	}
	return review, nil
}
func (c *client) netappPoolMembers(ctx context.Context, id string, region string, known map[string]any) (map[string]any, error) {
	ids := map[string]bool{}
	for child, v := range known {
		entry := object(v)
		if c.netappIdentity(child, netappVolumeType) != nil || redisParentID(child) != id || len(entry) != 3 || text(entry["configuration"]) == "" || !uuidPattern.MatchString(text(entry["uid"])) {
			return nil, serviceDenied("invalid_netapp_pool_member_hint")
		}
		ids[child] = false
	}
	rows, err := c.netappIndex(ctx, netappVolumeType, id)
	if err != nil {
		return nil, contracts.DependencyReadError(err)
	}
	for _, v := range rows {
		raw := object(v)
		child := strings.ToLower(text(raw["id"]))
		if c.netappIdentity(child, netappVolumeType) != nil || redisParentID(child) != id || !netappMetadata(raw, child, netappVolumeType) || ids[child] {
			return nil, serviceDenied("invalid_netapp_pool_volume_index")
		}
		ids[child] = true
	}
	members := map[string]any{}
	for _, child := range slices.Sorted(maps.Keys(ids)) {
		own, err := c.netappRead(ctx, child, netappVolumeType)
		if isNotFound(err) {
			if ids[child] {
				return nil, serviceDenied("netapp_pool_listed_volume_missing")
			}
			old := object(known[child])
			members[child] = map[string]any{"configuration": old["configuration"], "uid": old["uid"], "absent": true}
			continue
		}
		if err != nil {
			return nil, contracts.DependencyReadError(err)
		}
		if resourceRegion(own.data) != region {
			return nil, serviceDenied("netapp_pool_volume_region_changed")
		}
		uid := text(object(own.data["properties"])["fileSystemId"])
		if !uuidPattern.MatchString(uid) {
			return nil, serviceDenied("netapp_pool_volume_identity_unavailable")
		}
		members[child] = map[string]any{"configuration": c.privateConfiguration(own.data), "uid": uid, "absent": false}
	}
	return members, nil
}
func (c *client) netappPoolBoundary(ctx context.Context, id string, known map[string]any) (map[string]any, error) {
	if c.netappIdentity(id, netappPoolType) != nil {
		return nil, serviceDenied("invalid_netapp_pool_identity")
	}
	parents, _, region, protected, ready, err := c.netappRecoveryParents(ctx, id, netappPoolType)
	if err != nil {
		return nil, err
	}
	own, err := c.netappRead(ctx, id, netappPoolType)
	if err != nil {
		return nil, err
	}
	if resourceRegion(own.data) != region {
		return nil, serviceDenied("netapp_pool_region_changed")
	}
	p := object(own.data["properties"])
	uid := text(p["poolId"])
	ready = ready && p["provisioningState"] == "Succeeded" && uuidPattern.MatchString(uid)
	protected = protected || protectedAzureTags(object(own.data["tags"])) || text(own.data["managedBy"]) != ""
	members, err := c.netappPoolMembers(ctx, id, region, known)
	if err != nil {
		return nil, err
	}
	after, err := c.netappPoolMembers(ctx, id, region, members)
	if err != nil {
		return nil, err
	}
	if c.privateConfiguration(members) != c.privateConfiguration(after) {
		return nil, serviceDenied("netapp_pool_members_changed")
	}
	later, err := c.netappRead(ctx, id, netappPoolType)
	if err != nil {
		return nil, err
	}
	other, _, location, guard, currentReady, err := c.netappRecoveryParents(ctx, id, netappPoolType)
	if err != nil {
		return nil, err
	}
	if c.privateConfiguration(own.data) != c.privateConfiguration(later.data) || c.privateConfiguration(parents) != c.privateConfiguration(other) || region != location {
		return nil, serviceDenied("netapp_pool_context_changed")
	}
	return map[string]any{"parents": parents, "region": region, "raw": c.privateConfiguration(own.data), "configuration": c.privateConfiguration(netappPoolSnapshot(own.data)), "pool_id": uid, "members": members, "protected": protected || guard, "ready": ready && currentReady}, nil
}
func (r *Runtime) netappPoolInventory(ctx context.Context, c *client, req contracts.InventoryRequest, item *contracts.InventoryItem) error {
	known := object(object(req.KnownNativeMetadata[item.NativeID][netappPoolReview])["members"])
	review, err := c.netappPoolBoundary(ctx, item.NativeID, known)
	if err != nil {
		return err
	}
	if review["raw"] != item.Normalized["_netapp_configuration"] || review["region"] != item.Location {
		return serviceDenied("netapp_pool_inventory_changed")
	}
	item.Normalized[netappPoolReview], item.Normalized[netappPoolProof] = review, c.netappPoolProofFor(item.NativeID, req.ConnectionID, review)
	allowed := review["ready"] == true && review["protected"] == false
	item.Actionable = &allowed
	item.Normalized["cleanup_protected"] = !allowed
	if allowed {
		delete(item.Normalized, "cleanup_protection_reason")
	}
	return nil
}
func (c *client) netappPoolContribution(parent asset.Asset, assets []asset.Asset) (governance.Contribution, error) {
	result := governance.Contribution{}
	review, err := c.netappPoolRecorded(parent)
	if err != nil {
		return result, err
	}
	members := object(review["members"])
	byID := map[string]asset.Asset{}
	for _, v := range assets {
		if v.Identity.Provider != parent.Identity.Provider || v.Identity.ConnectionID != parent.Identity.ConnectionID || v.Identity.Partition != parent.Identity.Partition || v.Identity.NativeType != netappVolumeType || redisParentID(v.Identity.NativeID) != parent.Identity.NativeID {
			continue
		}
		if _, exists := byID[v.Identity.NativeID]; exists {
			return result, serviceDenied("ambiguous_netapp_pool_volume")
		}
		byID[v.Identity.NativeID] = v
		if members[v.Identity.NativeID] == nil || object(members[v.Identity.NativeID])["absent"] == true {
			result.Unresolved = append(result.Unresolved, graph.UnresolvedReference{BlocksCleanup: true, Provider: parent.Identity.Provider, ConnectionID: parent.Identity.ConnectionID, NativeType: netappVolumeType, NativeID: v.Identity.NativeID, ControllerID: parent.ID, Relationship: graph.RelationshipAttachedTo, Evidence: map[string]any{"reason": "netapp_pool_membership_requires_refresh"}})
		}
	}
	for _, id := range slices.Sorted(maps.Keys(members)) {
		entry := object(members[id])
		target := byID[id]
		evidence := map[string]any{"resource_type": netappVolumeType, "instance_id": id, "delete_by_default": true, "retention_supported": false}
		if target.ID == "" {
			if entry["absent"] == true {
				continue
			}
			result.Unresolved = append(result.Unresolved, graph.UnresolvedReference{BlocksCleanup: true, Provider: parent.Identity.Provider, ConnectionID: parent.Identity.ConnectionID, NativeType: netappVolumeType, NativeID: id, ControllerID: parent.ID, Relationship: graph.RelationshipAttachedTo, Evidence: evidence})
			continue
		}
		boundary, err := c.netappVolumeRecorded(target)
		if err != nil || target.Location != parent.Location || entry["configuration"] != target.Normalized["_netapp_configuration"] || entry["uid"] != boundary["file_system_id"] {
			result.Unresolved = append(result.Unresolved, graph.UnresolvedReference{BlocksCleanup: true, Provider: parent.Identity.Provider, ConnectionID: parent.Identity.ConnectionID, NativeType: netappVolumeType, NativeID: id, ControllerID: parent.ID, Relationship: graph.RelationshipAttachedTo, Evidence: map[string]any{"reason": "netapp_pool_membership_requires_refresh"}})
			continue
		}
		result.Bindings = append(result.Bindings, graph.LifecycleBinding{ControllerAssetID: parent.ID, ManagedAssetID: target.ID, Authority: graph.AuthorityAuthoritative, Ownership: graph.OwnershipExclusive, CleanupPolicy: graph.CleanupDirect, DirectCleanupAllowed: true, EvidenceSource: netappPoolLifecycleSource, Evidence: evidence, Confidence: 1})
		result.Relationships = append(result.Relationships, graph.Relationship{SourceAssetID: target.ID, TargetAssetID: parent.ID, Type: graph.RelationshipAttachedTo, Source: netappPoolLifecycleSource, Evidence: evidence, Confidence: 1})
	}
	return result, nil
}
