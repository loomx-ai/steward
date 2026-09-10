package azure

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"sort"
	"strings"

	"github.com/loomx-ai/steward/internal/core/asset"
)

const (
	redisType                     = "Microsoft.Cache/redis"
	redisPolicyType               = redisType + "/accessPolicies"
	redisAssignmentType           = redisType + "/accessPolicyAssignments"
	redisFirewallType             = redisType + "/firewallRules"
	redisLinkType                 = redisType + "/linkedServers"
	redisPatchType                = redisType + "/patchSchedules"
	redisConnectionType           = redisType + "/privateEndpointConnections"
	redisEnterpriseType           = "Microsoft.Cache/redisEnterprise"
	redisDatabaseType             = redisEnterpriseType + "/databases"
	redisDatabaseAssignmentType   = redisDatabaseType + "/accessPolicyAssignments"
	redisEnterpriseConnectionType = redisEnterpriseType + "/privateEndpointConnections"
)

func redisKind(kind string) string {
	for _, candidate := range []string{redisType, redisPolicyType, redisAssignmentType, redisFirewallType, redisLinkType, redisPatchType, redisConnectionType, redisEnterpriseType, redisDatabaseType, redisDatabaseAssignmentType, redisEnterpriseConnectionType} {
		if strings.EqualFold(candidate, kind) {
			return candidate
		}
	}
	return ""
}
func isRedisType(kind string) bool { return redisKind(kind) != "" }

// Child collections are verified with native List/Get operations. Keep all
// other public and private configuration, excluding documented runtime fields.
func redisSnapshot(kind string, raw map[string]any) map[string]any {
	payload, _ := json.Marshal(raw)
	var snapshot map[string]any
	json.Unmarshal(payload, &snapshot)
	snapshot["id"] = strings.ToLower(text(raw["id"]))
	snapshot["name"] = last(text(snapshot["id"]))
	delete(snapshot, "type")
	delete(snapshot, "etag")
	kind = redisKind(kind)
	if kind != redisType && kind != redisEnterpriseType {
		delete(snapshot, "location")
	}
	for _, field := range []string{"lastModifiedAt", "lastModifiedBy", "lastModifiedByType"} {
		delete(object(snapshot["systemData"]), field)
	}
	props := object(snapshot["properties"])
	delete(props, "provisioningState")
	if kind == redisType || kind == redisEnterpriseType {
		delete(props, "privateEndpointConnections")
	}
	if kind == redisType {
		delete(props, "linkedServers")
		delete(props, "instances")
	}
	if kind == redisEnterpriseType || kind == redisDatabaseType {
		delete(props, "resourceState")
	}
	if kind == redisDatabaseType {
		delete(object(props["geoReplication"]), "linkedDatabases")
	}
	return snapshot
}
func redisConfiguration(kind string, raw map[string]any) string {
	return serviceParentConfiguration(kind, redisSnapshot(kind, raw))
}
func redisIncarnation(planned asset.Asset, live map[string]any) error {
	if !isRedisType(planned.Identity.NativeType) {
		return nil
	}
	if expected := text(planned.Normalized["_redis_configuration"]); expected == "" || expected != redisConfiguration(planned.Identity.NativeType, live) {
		return serviceDenied("redis_configuration_changed")
	}
	if planned.Identity.NativeType == redisDatabaseType {
		ids, err := redisGeoIDs(live)
		if err != nil {
			return err
		}
		frozen := object(planned.Normalized["_redis_geo_peers"])
		for _, id := range ids {
			if frozen[id] == nil {
				return serviceDenied("redis_geo_replication_changed")
			}
		}
	}
	return nil
}
func (c *client) redisResource(ctx context.Context, id string) (map[string]any, error) {
	canonical, kind, err := parseID(id)
	if err != nil || !isRedisType(kind) {
		return nil, serviceDenied("invalid_redis_resource")
	}
	mapping, _ := findType(kind)
	endpoint, err := c.resourceURL(mapping, canonical)
	if err != nil {
		return nil, err
	}
	live, err := c.request(ctx, "GET", endpoint)
	if err != nil {
		return nil, err
	}
	if !validResourceResponse(live, canonical, mapping.NativeType) {
		return nil, serviceDenied("invalid_redis_resource")
	}
	return live.data, nil
}
func redisParentID(id string) string {
	parts := strings.Split(strings.ToLower(id), "/")
	return strings.Join(parts[:len(parts)-2], "/")
}
func redisRootID(id string) string {
	parts := strings.Split(strings.ToLower(id), "/")
	return strings.Join(parts[:9], "/")
}

func (c *client) redisInventory(ctx context.Context, id, kind string, raw, normalized map[string]any) error {
	if !isRedisType(kind) {
		return nil
	}
	normalized["_redis_configuration"] = redisConfiguration(kind, raw)
	normalized["_redis_private_configuration"] = c.privateConfiguration(redisSnapshot(kind, raw))
	if kind != redisType && kind != redisEnterpriseType {
		parent, err := c.redisResource(ctx, redisParentID(id))
		if err != nil {
			return err
		}
		_, parentKind, _ := parseID(text(parent["id"]))
		normalized["_redis_parent_configuration"] = redisConfiguration(parentKind, parent)
		normalized["_redis_parent_private_configuration"] = c.privateConfiguration(redisSnapshot(parentKind, parent))
		if kind == redisDatabaseAssignmentType {
			root, err := c.redisResource(ctx, redisRootID(id))
			if err != nil {
				return err
			}
			normalized["_redis_root_configuration"] = redisConfiguration(redisEnterpriseType, root)
			normalized["_redis_root_private_configuration"] = c.privateConfiguration(redisSnapshot(redisEnterpriseType, root))
		}
	}
	if kind == redisLinkType {
		return c.redisLinkInventory(ctx, id, raw, normalized)
	}
	if kind == redisDatabaseType {
		return c.redisGeoInventory(ctx, raw, normalized)
	}
	return nil
}

func (a *action) redisPreflight(ctx context.Context, planned asset.Asset, raw map[string]any) error {
	kind := a.kind.NativeType
	if !isRedisType(kind) {
		return nil
	}
	if err := redisIncarnation(planned, raw); err != nil {
		return err
	}
	if kind != redisType && kind != redisEnterpriseType {
		parent, err := a.client.redisResource(ctx, redisParentID(a.id))
		if err != nil {
			return err
		}
		_, parentKind, _ := parseID(text(parent["id"]))
		if expected := text(planned.Normalized["_redis_parent_configuration"]); expected == "" || expected != redisConfiguration(parentKind, parent) || text(planned.Normalized["_redis_parent_private_configuration"]) != a.client.privateConfiguration(redisSnapshot(parentKind, parent)) {
			return serviceDenied("redis_parent_changed")
		}
		mapping, _ := findType(parentKind)
		if kind == redisLinkType {
			if premium, err := redisPremium(parent); err != nil || !premium {
				return serviceDenied("invalid_redis_replication_sku")
			}
		}
		if reason := protectionReason(mapping, parent); reason != "" {
			return serviceDenied(reason)
		}
		if kind == redisDatabaseAssignmentType {
			root, err := a.client.redisResource(ctx, redisRootID(a.id))
			if err != nil {
				return err
			}
			if expected := text(planned.Normalized["_redis_root_configuration"]); expected == "" || expected != redisConfiguration(redisEnterpriseType, root) || text(planned.Normalized["_redis_root_private_configuration"]) != a.client.privateConfiguration(redisSnapshot(redisEnterpriseType, root)) {
				return serviceDenied("redis_root_changed")
			}
			rootKind, _ := findType(redisEnterpriseType)
			if reason := protectionReason(rootKind, root); reason != "" {
				return serviceDenied(reason)
			}
		}
	}
	if kind == redisPatchType && last(a.id) != "default" {
		return serviceDenied("invalid_redis_patch_schedule")
	}
	if kind == redisLinkType {
		return a.redisLinkPreflight(ctx, planned, raw)
	}
	if kind == redisDatabaseType {
		return a.redisGeoPreflight(ctx, planned, raw)
	}
	return nil
}

func redisPolicyReference(id string, raw map[string]any) (string, error) {
	name := text(object(raw["properties"])["accessPolicyName"])
	if name == "" || strings.TrimSpace(name) != name || strings.ContainsAny(name, "/\\%?#\x00\r\n") {
		return "", serviceDenied("invalid_redis_access_policy")
	}
	return redisRootID(id) + "/accesspolicies/" + strings.ToLower(name), nil
}

// Independently deletable children precede the parent. Built-in policies are
// controller-only; Azure's cache DELETE removes their parent-scoped views.
func (c *client) redisChildren(ctx context.Context, parent asset.Identity, raw map[string]any) ([]serviceChild, error) {
	if parent.NativeType == redisLinkType {
		return c.redisLinkChildren(ctx, parent, raw)
	}
	collect := func() ([]serviceChild, error) {
		nativeParent, nativeRaw := parent, raw
		kinds := slices.Clone(serviceChildKinds(parent.NativeType))
		if parent.NativeType == redisPolicyType {
			var err error
			nativeRaw, err = c.redisResource(ctx, redisRootID(parent.NativeID))
			if err != nil {
				return nil, err
			}
			nativeParent = asset.Identity{NativeType: redisType, NativeID: redisRootID(parent.NativeID)}
		}
		if parent.NativeType == redisType {
			kinds = slices.DeleteFunc(kinds, func(kind string) bool { return kind == redisLinkType })
		}
		children, err := c.nativeServiceChildren(ctx, nativeParent, nativeRaw, kinds)
		if err != nil {
			return nil, err
		}
		if parent.NativeType == redisType || parent.NativeType == redisEnterpriseType {
			if value, present := object(raw["properties"])["privateEndpointConnections"]; present {
				refs, ok := value.([]any)
				if !ok {
					return nil, serviceDenied("redis_connection_indexes_disagree")
				}
				actual := map[string]bool{}
				for _, child := range children {
					if child.kind == redisConnectionType || child.kind == redisEnterpriseConnectionType {
						actual[child.id] = true
					}
				}
				for _, ref := range refs {
					id := strings.ToLower(text(object(ref)["id"]))
					if !actual[id] {
						return nil, serviceDenied("redis_connection_indexes_disagree")
					}
					delete(actual, id)
				}
				if len(actual) != 0 {
					return nil, serviceDenied("redis_connection_indexes_disagree")
				}
			}
		}
		if parent.NativeType == redisPolicyType {
			matched := children[:0]
			for _, child := range children {
				ref, err := redisPolicyReference(child.id, child.data)
				if err != nil {
					return nil, err
				}
				if strings.EqualFold(ref, parent.NativeID) {
					matched = append(matched, child)
				}
			}
			children = matched
		}
		if parent.NativeType == redisType {
			links, err := c.redisIncomingLinks(ctx, parent.NativeID)
			if err != nil {
				return nil, err
			}
			children = append(children, links...)
		}
		sort.Slice(children, func(i, j int) bool { return children[i].id < children[j].id })
		return children, nil
	}
	first, err := collect()
	if err != nil {
		return nil, err
	}
	second, err := collect()
	if err != nil {
		return nil, err
	}
	if !slices.EqualFunc(first, second, func(a, b serviceChild) bool {
		return a.id == b.id && c.privateConfiguration(redisSnapshot(a.kind, a.data)) == c.privateConfiguration(redisSnapshot(b.kind, b.data))
	}) {
		return nil, serviceDenied("redis_children_changed")
	}
	return second, nil
}

func redisSharedPrerequisite(parent, child asset.Asset) bool {
	if parent.Identity.NativeType == redisType && child.Identity.NativeType == redisLinkType {
		return text(child.Normalized["serverRole"]) == "Secondary" && (redisRootID(child.Identity.NativeID) == strings.ToLower(parent.Identity.NativeID) || strings.EqualFold(text(child.Normalized["linkedRedisCacheId"]), parent.Identity.NativeID))
	}
	if parent.Identity.NativeType == redisPolicyType && child.Identity.NativeType == redisAssignmentType {
		ref, err := redisPolicyReference(child.Identity.NativeID, map[string]any{"properties": child.Normalized})
		return err == nil && strings.EqualFold(ref, parent.Identity.NativeID)
	}
	return false
}

func (a *action) resourceOperationResponse(endpoint string, res response) error {
	if !isRedisType(a.kind.NativeType) && !isSearchType(a.kind.NativeType) && !isCognitiveType(a.kind.NativeType) && !isCosmosType(a.kind.NativeType) && !isMongoClusterType(a.kind.NativeType) && !isKustoType(a.kind.NativeType) && !isStreamAnalyticsType(a.kind.NativeType) {
		return nil
	}
	if res.status != 200 && res.status != 202 && res.status != 204 {
		return fmt.Errorf("incomplete Azure operation response")
	}
	u, _ := url.Parse(endpoint)
	for field, expected := range map[string]string{"id": u.Path, "name": last(u.Path), "resourceId": a.id} {
		if isCosmosType(a.kind.NativeType) && field == "resourceId" {
			if value, present := res.data[field]; present && !cosmosSameWireID(responseID(a.kind.NativeType, text(value)), a.wireID) {
				return fmt.Errorf("Cosmos DB polling response belongs to another resource")
			}
			continue
		}
		if value, present := res.data[field]; present && !strings.EqualFold(text(value), expected) {
			return fmt.Errorf("Azure polling response belongs to another resource or operation")
		}
	}
	return nil
}
