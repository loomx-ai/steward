package azure

import (
	"context"
	"slices"
	"sort"
	"strings"

	"github.com/loomx-ai/steward/internal/core/asset"
)

func redisGeoIDs(raw map[string]any) ([]string, error) {
	value := object(raw["properties"])["geoReplication"]
	if value == nil {
		return nil, nil
	}
	geo, ok := value.(map[string]any)
	if !ok || text(geo["groupNickname"]) == "" {
		return nil, serviceDenied("invalid_redis_geo_replication")
	}
	rows, ok := geo["linkedDatabases"].([]any)
	if !ok {
		return nil, serviceDenied("invalid_redis_geo_replication")
	}
	ids := []string{}
	for _, row := range rows {
		id, kind, err := parseID(text(object(row)["id"]))
		if err != nil || !strings.EqualFold(kind, redisDatabaseType) || slices.Contains(ids, id) {
			return nil, serviceDenied("invalid_redis_geo_replication")
		}
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids, nil
}
func redisGeoHealthy(raw map[string]any) bool {
	for _, row := range array(object(object(raw["properties"])["geoReplication"])["linkedDatabases"]) {
		if text(object(row)["state"]) != "Linked" {
			return false
		}
	}
	return true
}
func (c *client) redisGeoInventory(ctx context.Context, raw, normalized map[string]any) error {
	ids, err := redisGeoIDs(raw)
	if err != nil {
		return err
	}
	peers := map[string]any{}
	for _, id := range ids {
		peer := raw
		if !strings.EqualFold(id, text(raw["id"])) {
			peer, err = c.redisResource(ctx, id)
			if err != nil {
				return err
			}
		}
		root, err := c.redisResource(ctx, redisRootID(id))
		if err != nil {
			return err
		}
		peers[id] = map[string]any{"configuration": redisConfiguration(redisDatabaseType, peer), "private_configuration": c.privateConfiguration(redisSnapshot(redisDatabaseType, peer)), "root_configuration": redisConfiguration(redisEnterpriseType, root), "root_private_configuration": c.privateConfiguration(redisSnapshot(redisEnterpriseType, root))}
	}
	normalized["_redis_geo_peers"] = peers
	return nil
}

// Native database deletion removes that member from a healthy replication
// group. Subsequent reviewed deletions may see a smaller group; each missing
// member must independently return 404. Joins, live unlinking, changed peers,
// failed links and missing permissions require a fresh review.
func (a *action) redisGeoPreflight(ctx context.Context, planned asset.Asset, raw map[string]any) error {
	ids, err := redisGeoIDs(raw)
	if err != nil {
		return err
	}
	frozen, ok := planned.Normalized["_redis_geo_peers"].(map[string]any)
	if !ok || !redisGeoHealthy(raw) {
		return serviceDenied("redis_geo_replication_changed")
	}
	if len(ids) > 0 && !slices.Contains(ids, a.id) {
		return serviceDenied("redis_geo_replication_changed")
	}
	locks, err := a.client.managementLocks(ctx)
	if err != nil {
		return err
	}
	current := map[string]map[string]any{}
	roots := map[string]map[string]any{}
	for id, value := range frozen {
		peer, err := a.client.redisResource(ctx, id)
		if isNotFound(err) && !slices.Contains(ids, id) {
			continue
		}
		if err != nil {
			return err
		}
		if !slices.Contains(ids, id) {
			return serviceDenied("redis_geo_replication_changed")
		}
		expected := object(value)
		if text(expected["configuration"]) == "" || text(expected["configuration"]) != redisConfiguration(redisDatabaseType, peer) || text(expected["private_configuration"]) != a.client.privateConfiguration(redisSnapshot(redisDatabaseType, peer)) {
			return serviceDenied("redis_geo_peer_changed")
		}
		peerIDs, err := redisGeoIDs(peer)
		if err != nil {
			return err
		}
		if !slices.Equal(peerIDs, ids) || !redisGeoHealthy(peer) {
			return serviceDenied("redis_geo_replication_changed")
		}
		root, err := a.client.redisResource(ctx, redisRootID(id))
		if err != nil {
			return err
		}
		if text(expected["root_configuration"]) == "" || text(expected["root_configuration"]) != redisConfiguration(redisEnterpriseType, root) || text(expected["root_private_configuration"]) != a.client.privateConfiguration(redisSnapshot(redisEnterpriseType, root)) {
			return serviceDenied("redis_geo_peer_changed")
		}
		if err := a.client.redisPeerProtection(ctx, id, peer, locks); err != nil {
			return err
		}
		if err := a.client.redisPeerProtection(ctx, redisRootID(id), root, locks); err != nil {
			return err
		}
		current[id] = peer
		roots[redisRootID(id)] = root
	}
	for _, id := range ids {
		if current[id] == nil {
			return serviceDenied("redis_geo_replication_changed")
		}
	}
	// Re-read every participant after completing the topology walk.
	for id, peer := range current {
		live, err := a.client.redisResource(ctx, id)
		if err != nil {
			return err
		}
		if a.client.privateConfiguration(live) != a.client.privateConfiguration(peer) {
			return serviceDenied("redis_geo_replication_changed")
		}
	}
	for id, root := range roots {
		live, err := a.client.redisResource(ctx, id)
		if err != nil {
			return err
		}
		if a.client.privateConfiguration(redisSnapshot(redisEnterpriseType, live)) != a.client.privateConfiguration(redisSnapshot(redisEnterpriseType, root)) {
			return serviceDenied("redis_geo_peer_changed")
		}
	}
	return nil
}
func (c *client) redisPeerProtection(ctx context.Context, id string, raw map[string]any, locks []any) error {
	_, nativeType, _ := parseID(id)
	kind, _ := findType(nativeType)
	if locked(id, locks) {
		return serviceDenied("azure_management_lock")
	}
	if reason := protectionReason(kind, raw); reason != "" {
		return serviceDenied(reason)
	}
	groupID := strings.Join(strings.Split(id, "/")[:5], "/")
	group, err := c.request(ctx, "GET", apiURL(groupID, resourcesVersion))
	if err != nil {
		return err
	}
	if !validResourceResponse(group, groupID, groupType) {
		return serviceDenied("invalid_redis_peer_group")
	}
	if text(group.data["managedBy"]) != "" {
		return serviceDenied("azure_managed_resource_group")
	}
	return nil
}

func (a *action) redisReplicationReadback(ctx context.Context, planned asset.Asset) (bool, error) {
	if a.kind.NativeType == redisLinkType {
		peerID := strings.ToLower(text(planned.Normalized["linkedRedisCacheId"]))
		if peerID == "" {
			return false, serviceDenied("redis_link_peer_missing")
		}
		peer, err := a.client.redisResource(ctx, peerID)
		if isNotFound(err) {
			return true, nil
		}
		if err != nil {
			return false, err
		}
		if text(planned.Normalized["_redis_link_peer_configuration"]) != redisConfiguration(redisType, peer) || text(planned.Normalized["_redis_link_peer_private_configuration"]) != a.client.privateConfiguration(redisSnapshot(redisType, peer)) {
			return false, serviceDenied("redis_link_peer_changed")
		}
		links, err := a.client.redisNativeLinks(ctx, peer)
		if err != nil {
			return false, err
		}
		for _, link := range links {
			target, _ := redisLinkPeer(link.data)
			if target == redisRootID(a.id) {
				return false, nil
			}
		}
	}
	if a.kind.NativeType == redisDatabaseType {
		frozen, ok := planned.Normalized["_redis_geo_peers"].(map[string]any)
		if !ok {
			return false, serviceDenied("redis_geo_replication_changed")
		}
		for id, value := range frozen {
			if id == a.id {
				continue
			}
			peer, err := a.client.redisResource(ctx, id)
			if isNotFound(err) {
				continue
			}
			if err != nil {
				return false, err
			}
			expected := object(value)
			if text(expected["configuration"]) != redisConfiguration(redisDatabaseType, peer) || text(expected["private_configuration"]) != a.client.privateConfiguration(redisSnapshot(redisDatabaseType, peer)) {
				return false, serviceDenied("redis_geo_peer_changed")
			}
			root, err := a.client.redisResource(ctx, redisRootID(id))
			if err != nil {
				return false, err
			}
			if text(expected["root_configuration"]) != redisConfiguration(redisEnterpriseType, root) || text(expected["root_private_configuration"]) != a.client.privateConfiguration(redisSnapshot(redisEnterpriseType, root)) {
				return false, serviceDenied("redis_geo_peer_changed")
			}
			ids, err := redisGeoIDs(peer)
			if err != nil {
				return false, err
			}
			if slices.Contains(ids, a.id) {
				return false, nil
			}
			for _, member := range ids {
				if frozen[member] == nil {
					return false, serviceDenied("redis_geo_replication_changed")
				}
			}
		}
	}
	return true, nil
}
