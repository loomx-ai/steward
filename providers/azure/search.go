package azure

import (
	"context"
	"encoding/json"
	"slices"
	"strings"

	"github.com/loomx-ai/steward/internal/core/asset"
)

const (
	searchType           = "Microsoft.Search/searchServices"
	searchConnectionType = searchType + "/privateEndpointConnections"
	searchLinkType       = searchType + "/sharedPrivateLinkResources"
	searchPerimeterType  = searchType + "/networkSecurityPerimeterConfigurations"
)

func isSearchType(kind string) bool {
	return strings.EqualFold(kind, searchType) || strings.EqualFold(kind, searchConnectionType) || strings.EqualFold(kind, searchLinkType) || strings.EqualFold(kind, searchPerimeterType)
}

// Search owns these proxy views. Its network perimeter, data sources and
// manually created private endpoints remain independently managed resources.
func searchSnapshot(kind string, raw map[string]any) map[string]any {
	payload, _ := json.Marshal(raw)
	var snapshot map[string]any
	json.Unmarshal(payload, &snapshot)
	snapshot["id"] = strings.ToLower(text(raw["id"]))
	snapshot["name"] = last(text(snapshot["id"]))
	delete(snapshot, "type")
	delete(snapshot, "etag")
	for _, field := range []string{"lastModifiedAt", "lastModifiedBy", "lastModifiedByType"} {
		delete(object(snapshot["systemData"]), field)
	}
	props := object(snapshot["properties"])
	delete(props, "provisioningState")
	if kind == searchType {
		for _, field := range []string{"eTag", "status", "statusDetails", "upgradeAvailable", "privateEndpointConnections", "sharedPrivateLinkResources"} {
			delete(props, field)
		}
	} else {
		delete(snapshot, "location") // Proxy resources inherit the service region.
	}
	return snapshot
}
func searchConfiguration(kind string, raw map[string]any) string {
	return serviceParentConfiguration(kind, searchSnapshot(kind, raw))
}
func searchIncarnation(planned asset.Asset, live map[string]any) error {
	if !isSearchType(planned.Identity.NativeType) {
		return nil
	}
	if expected := text(planned.Normalized["_search_configuration"]); expected == "" || expected != searchConfiguration(planned.Identity.NativeType, live) {
		return serviceDenied("search_configuration_changed")
	}
	return nil
}

// Bind the target's actual native API. An unknown or out-of-subscription
// resource cannot borrow the Search API version or the selected credential.
func (c *client) linkedResource(ctx context.Context, value string) (map[string]any, error) {
	id, kind, err := parseID(value)
	mapping, known := findType(kind)
	if err != nil || !known {
		return nil, serviceDenied("linked_target_api_unavailable")
	}
	if isCosmosType(kind) {
		return c.cosmosResource(ctx, value)
	}
	endpoint, err := c.resourceURL(mapping, id)
	if err != nil {
		return nil, err
	}
	response, err := c.request(ctx, "GET", endpoint)
	if err != nil {
		return nil, err
	}
	if !validResourceResponse(response, id, mapping.NativeType) {
		return nil, serviceDenied("linked_resource_identity_changed")
	}
	return response.data, nil
}
func searchLinkTarget(raw map[string]any) (string, error) {
	props := object(raw["properties"])
	id, _, err := parseID(text(props["privateLinkResourceId"]))
	group := text(props["groupId"])
	if err != nil || group == "" || strings.ContainsAny(group, "/\\%?#\x00\r\n") || strings.TrimSpace(group) != group {
		return "", serviceDenied("invalid_search_link_target")
	}
	return id, nil
}
func searchTargetRegion(link, target map[string]any) error {
	region := object(link["properties"])["resourceRegion"]
	if region == nil || region == "" {
		return nil
	}
	if value, ok := region.(string); !ok || strings.TrimSpace(value) != value || resourceRegion(map[string]any{"location": value}) != resourceRegion(target) {
		return serviceDenied("search_link_target_region_changed")
	}
	return nil
}
func searchTargetSnapshot(raw map[string]any) map[string]any {
	if _, kind, err := parseID(text(raw["id"])); err == nil && isStreamAnalyticsType(kind) {
		return streamAnalyticsSnapshot(kind, raw)
	}
	if _, kind, err := parseID(text(raw["id"])); err == nil && isKustoType(kind) {
		return kustoSnapshot(kind, raw)
	}
	if _, kind, err := parseID(text(raw["id"])); err == nil && isMongoClusterType(kind) {
		return mongoClusterSnapshot(kind, raw)
	}
	if _, kind, err := parseID(text(raw["id"])); err == nil && isCosmosType(kind) {
		return cosmosSnapshot(kind, raw)
	}
	// Target connection metadata changes as individually reviewed links depart.
	// Keep the target's identity, other configuration, ownership and protection.
	snapshot := searchSnapshot(searchType, raw)
	delete(object(snapshot["properties"]), "eTag")
	return snapshot
}
func (c *client) searchInventory(ctx context.Context, id, kind string, raw, normalized map[string]any) error {
	if !isSearchType(kind) {
		return nil
	}
	normalized["_search_configuration"] = searchConfiguration(kind, raw)
	normalized["_search_private_configuration"] = c.privateConfiguration(searchSnapshot(kind, raw))
	if kind != searchType {
		parent, err := c.linkedResource(ctx, redisParentID(id))
		if err != nil {
			return err
		}
		normalized["_search_parent_configuration"] = searchConfiguration(searchType, parent)
		normalized["_search_parent_private_configuration"] = c.privateConfiguration(searchSnapshot(searchType, parent))
	}
	if kind == searchLinkType {
		targetID, err := searchLinkTarget(raw)
		if err != nil {
			return err
		}
		target, err := c.linkedResource(ctx, targetID)
		if err != nil {
			return err
		}
		if err := searchTargetRegion(raw, target); err != nil {
			return err
		}
		normalized["_search_target_configuration"] = c.privateConfiguration(searchTargetSnapshot(target))
	}
	return nil
}

func (a *action) searchPreflight(ctx context.Context, planned asset.Asset, raw map[string]any) error {
	kind := a.kind.NativeType
	if !isSearchType(kind) {
		return nil
	}
	if err := searchIncarnation(planned, raw); err != nil {
		return err
	}
	if kind != searchType {
		parent, err := a.client.linkedResource(ctx, redisParentID(a.id))
		if err != nil {
			return err
		}
		if expected := text(planned.Normalized["_search_parent_configuration"]); expected == "" || expected != searchConfiguration(searchType, parent) || text(planned.Normalized["_search_parent_private_configuration"]) != a.client.privateConfiguration(searchSnapshot(searchType, parent)) {
			return serviceDenied("search_parent_changed")
		}
		mapping, _ := findType(searchType)
		if reason := protectionReason(mapping, parent); reason != "" {
			return serviceDenied(reason)
		}
	}
	if kind != searchLinkType {
		return nil
	}
	// The documented delete contract permits only terminal provisioning states.
	state := text(object(raw["properties"])["provisioningState"])
	if state != "Succeeded" && state != "Failed" {
		return serviceDenied("search_link_not_terminal")
	}
	targetID, err := searchLinkTarget(raw)
	if err != nil {
		return err
	}
	target, err := a.client.linkedResource(ctx, targetID)
	if err != nil {
		return err
	}
	if err := searchTargetRegion(raw, target); err != nil {
		return err
	}
	expected := text(planned.Normalized["_search_target_configuration"])
	if expected == "" || expected != a.client.privateConfiguration(searchTargetSnapshot(target)) {
		return serviceDenied("search_link_target_changed")
	}
	locks, err := a.client.managementLocks(ctx)
	if err != nil {
		return err
	}
	if err := a.client.linkedResourceProtection(ctx, targetID, target, locks); err != nil {
		return err
	}
	current, err := a.client.linkedResource(ctx, targetID)
	if err != nil {
		return err
	}
	if expected != a.client.privateConfiguration(searchTargetSnapshot(current)) {
		return serviceDenied("search_link_target_changed")
	}
	return nil
}

func (c *client) searchChildren(ctx context.Context, parent asset.Identity, raw map[string]any) ([]serviceChild, error) {
	collect := func() ([]serviceChild, error) {
		children, err := c.nativeServiceChildren(ctx, parent, raw, serviceChildKinds(searchType))
		if err != nil {
			return nil, err
		}
		for field, kind := range map[string]string{"privateEndpointConnections": searchConnectionType, "sharedPrivateLinkResources": searchLinkType} {
			value, present := object(raw["properties"])[field]
			if !present {
				continue
			}
			refs, ok := value.([]any)
			if !ok {
				return nil, serviceDenied("search_child_indexes_disagree")
			}
			actual := map[string]bool{}
			for _, child := range children {
				if child.kind == kind {
					actual[child.id] = true
				}
			}
			for _, ref := range refs {
				id := strings.ToLower(text(object(ref)["id"]))
				if !actual[id] {
					return nil, serviceDenied("search_child_indexes_disagree")
				}
				delete(actual, id)
			}
			if len(actual) != 0 {
				return nil, serviceDenied("search_child_indexes_disagree")
			}
		}
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
		return a.id == b.id && c.privateConfiguration(searchSnapshot(a.kind, a.data)) == c.privateConfiguration(searchSnapshot(b.kind, b.data))
	}) {
		return nil, serviceDenied("search_children_changed")
	}
	return second, nil
}
