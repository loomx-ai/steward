package azure

import (
	"context"
	"slices"
	"sort"
	"strings"

	"github.com/loomx-ai/steward/internal/core/asset"
)

func redisLinkPeer(raw map[string]any) (string, error) {
	props := object(raw["properties"])
	id, kind, err := parseID(text(props["linkedRedisCacheId"]))
	role := text(props["serverRole"])
	if err != nil || !strings.EqualFold(kind, redisType) || id == redisRootID(text(raw["id"])) || (role != "Primary" && role != "Secondary") || text(props["linkedRedisCacheLocation"]) == "" {
		return "", serviceDenied("invalid_redis_replication_link")
	}
	return id, nil
}
func redisPremium(raw map[string]any) (bool, error) {
	switch text(object(object(raw["properties"])["sku"])["name"]) {
	case "Premium":
		return true, nil
	case "Basic", "Standard":
		if value, present := object(raw["properties"])["linkedServers"]; present {
			links, ok := value.([]any)
			if !ok || len(links) > 0 {
				return false, serviceDenied("invalid_redis_replication_sku")
			}
		}
		return false, nil
	default:
		return false, serviceDenied("redis_sku_missing")
	}
}
func (c *client) redisNativeLinks(ctx context.Context, raw map[string]any) ([]serviceChild, error) {
	premium, err := redisPremium(raw)
	if err != nil {
		return nil, err
	}
	if !premium {
		return nil, nil
	}
	parent := asset.Identity{NativeType: redisType, NativeID: strings.ToLower(text(raw["id"]))}
	children, err := c.nativeServiceChildren(ctx, parent, raw, []string{redisLinkType})
	if err != nil {
		return nil, err
	}
	peers := map[string]bool{}
	for _, child := range children {
		peer, err := redisLinkPeer(child.data)
		if err != nil {
			return nil, err
		}
		if peers[peer] {
			return nil, serviceDenied("duplicate_redis_replication_link")
		}
		peers[peer] = true
	}
	if value, present := object(raw["properties"])["linkedServers"]; present {
		refs, ok := value.([]any)
		if !ok || len(refs) != len(children) {
			return nil, serviceDenied("redis_link_indexes_disagree")
		}
		seen := map[string]bool{}
		for _, value := range refs {
			id, kind, err := parseID(text(object(value)["id"]))
			if err != nil || !strings.EqualFold(kind, redisLinkType) || seen[id] || !slices.ContainsFunc(children, func(child serviceChild) bool { return child.id == id }) {
				return nil, serviceDenied("redis_link_indexes_disagree")
			}
			seen[id] = true
		}
	}
	return children, nil
}

// The subscription index finds incoming links even when the secondary cache
// exposes no reciprocal child view. The peer ID and native role determine the
// primary link; names and resource-group proximity are not evidence.
func (c *client) redisIncomingLinks(ctx context.Context, target string) ([]serviceChild, error) {
	kind, _ := findType(redisType)
	rows, err := c.listAll(ctx, c.root()+"/providers/Microsoft.Cache/redis", kind.Version)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	links := map[string]serviceChild{}
	secondary := []serviceChild{}
	found := false
	for _, value := range rows {
		row := object(value)
		id, nativeType, err := parseID(text(row["id"]))
		if err != nil || !strings.EqualFold(nativeType, redisType) || seen[id] || !validResponseType(redisType, text(row["type"])) {
			return nil, serviceDenied("invalid_redis_subscription_list")
		}
		seen[id] = true
		raw, err := c.redisResource(ctx, id)
		if err != nil {
			return nil, err
		}
		if err := serviceListedIncarnation(row, raw); err != nil {
			return nil, err
		}
		if strings.EqualFold(id, target) {
			found = true
		}
		children, err := c.redisNativeLinks(ctx, raw)
		if err != nil {
			return nil, err
		}
		for _, child := range children {
			peer, err := redisLinkPeer(child.data)
			if err != nil {
				return nil, err
			}
			if id != strings.ToLower(target) && peer != strings.ToLower(target) {
				continue
			}
			if object(child.data["properties"])["serverRole"] == "Secondary" {
				links[child.id] = child
			} else {
				secondary = append(secondary, child)
			}
		}
	}
	if !found {
		return nil, serviceDenied("redis_subscription_index_incomplete")
	}
	for _, child := range secondary {
		peer, _ := redisLinkPeer(child.data)
		if !slices.ContainsFunc(mapValuesRedisLinks(links), func(primary serviceChild) bool {
			primaryPeer, _ := redisLinkPeer(primary.data)
			return redisRootID(primary.id) == peer && primaryPeer == redisRootID(child.id)
		}) {
			return nil, serviceDenied("redis_primary_link_missing")
		}
	}
	return mapValuesRedisLinks(links), nil
}
func mapValuesRedisLinks(values map[string]serviceChild) []serviceChild {
	result := make([]serviceChild, 0, len(values))
	for _, value := range values {
		result = append(result, value)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].id < result[j].id })
	return result
}

func (c *client) redisLinkChildren(ctx context.Context, parent asset.Identity, raw map[string]any) ([]serviceChild, error) {
	peerID, err := redisLinkPeer(raw)
	if err != nil {
		return nil, err
	}
	if object(raw["properties"])["serverRole"] != "Secondary" {
		return nil, nil
	}
	peer, err := c.redisResource(ctx, peerID)
	if err != nil {
		return nil, err
	}
	children, err := c.redisNativeLinks(ctx, peer)
	if err != nil {
		return nil, err
	}
	result := []serviceChild{}
	for _, child := range children {
		target, _ := redisLinkPeer(child.data)
		if target != redisRootID(parent.NativeID) {
			continue
		}
		if object(child.data["properties"])["serverRole"] != "Primary" || len(result) > 0 {
			return nil, serviceDenied("redis_replication_roles_disagree")
		}
		result = append(result, child)
	}
	return result, nil
}
func (c *client) redisLinkInventory(ctx context.Context, id string, raw, normalized map[string]any) error {
	peerID, err := redisLinkPeer(raw)
	if err != nil {
		return err
	}
	peer, err := c.redisResource(ctx, peerID)
	if err != nil {
		return err
	}
	if premium, err := redisPremium(peer); err != nil || !premium {
		return serviceDenied("invalid_redis_replication_sku")
	}
	if resourceRegion(peer) != resourceRegion(map[string]any{"location": object(raw["properties"])["linkedRedisCacheLocation"]}) {
		return serviceDenied("redis_replication_location_changed")
	}
	normalized["_redis_link_peer_configuration"] = redisConfiguration(redisType, peer)
	normalized["_redis_link_peer_private_configuration"] = c.privateConfiguration(redisSnapshot(redisType, peer))
	children, err := c.redisLinkChildren(ctx, asset.Identity{NativeType: redisLinkType, NativeID: id}, raw)
	if err != nil {
		return err
	}
	reverse := map[string]any{}
	for _, child := range children {
		reverse[child.id] = redisConfiguration(redisLinkType, child.data)
	}
	normalized["_redis_reverse_links"] = reverse
	return nil
}
func redisLinkPeerRelation(primary, secondary asset.Asset) bool {
	if primary.Identity.NativeType != redisLinkType || secondary.Identity.NativeType != redisLinkType || text(primary.Normalized["serverRole"]) != "Secondary" || text(secondary.Normalized["serverRole"]) != "Primary" {
		return false
	}
	return strings.EqualFold(text(primary.Normalized["linkedRedisCacheId"]), redisRootID(secondary.Identity.NativeID)) && strings.EqualFold(text(secondary.Normalized["linkedRedisCacheId"]), redisRootID(primary.Identity.NativeID)) && text(object(primary.Normalized["_redis_reverse_links"])[strings.ToLower(secondary.Identity.NativeID)]) != "" && object(primary.Normalized["_redis_reverse_links"])[strings.ToLower(secondary.Identity.NativeID)] == secondary.Normalized["_redis_configuration"]
}
func (a *action) redisLinkPreflight(ctx context.Context, planned asset.Asset, raw map[string]any) error {
	if object(raw["properties"])["serverRole"] != "Secondary" {
		return serviceDenied("azure_redis_secondary_link")
	}
	peerID, err := redisLinkPeer(raw)
	if err != nil {
		return err
	}
	peer, err := a.client.redisResource(ctx, peerID)
	if err != nil {
		return err
	}
	if expected := text(planned.Normalized["_redis_link_peer_configuration"]); expected == "" || expected != redisConfiguration(redisType, peer) || text(planned.Normalized["_redis_link_peer_private_configuration"]) != a.client.privateConfiguration(redisSnapshot(redisType, peer)) {
		return serviceDenied("redis_link_peer_changed")
	}
	children, err := a.client.redisLinkChildren(ctx, planned.Identity, raw)
	if err != nil {
		return err
	}
	reverse, ok := planned.Normalized["_redis_reverse_links"].(map[string]any)
	if !ok || len(reverse) != len(children) {
		return serviceDenied("redis_link_peer_changed")
	}
	for _, child := range children {
		if reverse[child.id] != redisConfiguration(redisLinkType, child.data) {
			return serviceDenied("redis_link_peer_changed")
		}
	}
	locks, err := a.client.managementLocks(ctx)
	if err != nil {
		return err
	}
	if err := a.client.linkedResourceProtection(ctx, peerID, peer, locks); err != nil {
		return err
	}
	live, err := a.client.redisResource(ctx, peerID)
	if err != nil {
		return err
	}
	if a.client.privateConfiguration(redisSnapshot(redisType, live)) != a.client.privateConfiguration(redisSnapshot(redisType, peer)) {
		return serviceDenied("redis_link_peer_changed")
	}
	return nil
}
