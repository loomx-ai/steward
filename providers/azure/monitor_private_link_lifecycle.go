package azure

import (
	"context"
	"maps"
	"net/url"
	"slices"
	"strings"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func monitorPrivateLinkSnapshot(kind string, raw map[string]any) map[string]any {
	snapshot := maps.Clone(raw)
	if raw["systemData"] != nil {
		snapshot["systemData"] = maps.Clone(object(raw["systemData"]))
	}
	if raw["properties"] != nil {
		snapshot["properties"] = maps.Clone(object(raw["properties"]))
	}
	snapshot["id"] = strings.ToLower(text(raw["id"]))
	snapshot["name"] = last(text(snapshot["id"]))
	delete(snapshot, "type")
	delete(snapshot, "etag")
	for _, field := range []string{"lastModifiedAt", "lastModifiedBy", "lastModifiedByType"} {
		delete(object(snapshot["systemData"]), field)
	}
	delete(object(snapshot["properties"]), "provisioningState")
	if strings.EqualFold(kind, monitorPrivateLinkType) {
		delete(object(snapshot["properties"]), "privateEndpointConnections")
	} else {
		delete(snapshot, "location") // Both child APIs return ProxyResources.
	}
	return snapshot
}

func monitorPrivateLinkConfiguration(kind string, raw map[string]any) string {
	return serviceParentConfiguration(monitorPrivateLinkKind(kind), monitorPrivateLinkSnapshot(kind, raw))
}

func monitorPrivateLinkReference(raw map[string]any) (string, error) {
	id, kind, err := parseID(text(object(raw["properties"])["linkedResourceId"]))
	if err != nil || (!strings.EqualFold(kind, applicationInsightsType) && !strings.EqualFold(kind, "Microsoft.OperationalInsights/workspaces") && !strings.EqualFold(kind, dataCollectionEndpointType)) {
		return "", serviceDenied("invalid_monitor_private_link_target")
	}
	return id, nil // Native examples include links across subscriptions.
}

func validateMonitorPrivateLink(kind string, raw map[string]any) error {
	id, actual, err := parseID(text(raw["id"]))
	if err != nil || !strings.EqualFold(actual, kind) || !validResponseType(kind, text(raw["type"])) || !strings.EqualFold(text(raw["name"]), last(id)) {
		return serviceDenied("invalid_monitor_private_link_identity")
	}
	properties, ok := raw["properties"].(map[string]any)
	if !ok || text(properties["provisioningState"]) == "" {
		return serviceDenied("invalid_monitor_private_link_properties")
	}
	switch monitorPrivateLinkKind(kind) {
	case monitorPrivateLinkType:
		if !strings.EqualFold(text(raw["location"]), "global") {
			return serviceDenied("invalid_monitor_private_link_location")
		}
		settings, ok := properties["accessModeSettings"].(map[string]any)
		if !ok {
			return serviceDenied("invalid_monitor_private_link_access_modes")
		}
		validMode := func(value any) bool { return value == "Open" || value == "PrivateOnly" }
		if !validMode(settings["queryAccessMode"]) || !validMode(settings["ingestionAccessMode"]) {
			return serviceDenied("invalid_monitor_private_link_access_modes")
		}
		if value, present := settings["exclusions"]; present {
			rows, ok := value.([]any)
			if !ok {
				return serviceDenied("invalid_monitor_private_link_exclusions")
			}
			seen := map[string]bool{}
			for _, row := range rows {
				exclusion, ok := row.(map[string]any)
				name := text(exclusion["privateEndpointConnectionName"])
				_, _, err := parseID(id + "/privateEndpointConnections/" + name)
				if !ok || err != nil || strings.Contains(name, "/") || seen[strings.ToLower(name)] {
					return serviceDenied("invalid_monitor_private_link_exclusion")
				}
				seen[strings.ToLower(name)] = true
				for _, field := range []string{"queryAccessMode", "ingestionAccessMode"} {
					if value, present := exclusion[field]; present && !validMode(value) {
						return serviceDenied("invalid_monitor_private_link_exclusion_mode")
					}
				}
			}
		}
	case monitorScopedResourceType:
		_, err := monitorPrivateLinkReference(raw)
		return err
	case monitorPrivateConnectionType:
		_, target, err := parseID(text(object(properties["privateEndpoint"])["id"]))
		if err != nil || !strings.EqualFold(target, privateEndpointType) || text(object(properties["privateLinkServiceConnectionState"])["status"]) == "" {
			return serviceDenied("invalid_monitor_private_link_endpoint")
		}
	default:
		return serviceDenied("unknown_monitor_private_link_kind")
	}
	return nil
}

func monitorPrivateLinkListed(kind string, listed, live map[string]any) error {
	if monitorPrivateLinkKind(kind) == "" {
		return nil
	}
	id, actual, err := parseID(text(listed["id"]))
	if err != nil || !strings.EqualFold(actual, kind) || !validResponseType(kind, text(listed["type"])) {
		return serviceDenied("invalid_monitor_private_link_listed_identity")
	}
	if value, present := listed["name"]; present && !strings.EqualFold(text(value), last(id)) {
		return serviceDenied("invalid_monitor_private_link_listed_name")
	}
	if err := validateMonitorPrivateLink(kind, live); err != nil {
		return err
	}
	if !nativeConfigurationContains(monitorPrivateLinkSnapshot(kind, listed), monitorPrivateLinkSnapshot(kind, live)) {
		return serviceDenied("monitor_private_link_listed_configuration_changed")
	}
	return serviceListedIncarnation(listed, live)
}

func monitorPrivateLinkIncarnation(planned asset.Asset, live map[string]any) error {
	kind := monitorPrivateLinkKind(planned.Identity.NativeType)
	if kind == "" {
		return nil
	}
	if err := validateMonitorPrivateLink(kind, live); err != nil {
		return err
	}
	if expected := text(planned.Normalized["_monitor_private_link_configuration"]); expected == "" || expected != monitorPrivateLinkConfiguration(kind, live) {
		return serviceDenied("monitor_private_link_configuration_changed")
	}
	return nil
}

// These are provider capability descriptions, not independently deletable
// resources. Read their native list and detail APIs and retain the complete
// group/member/zone contract in the reviewed scope's snapshot.
func (c *client) monitorPrivateLinkCapabilities(ctx context.Context, parent string) ([]any, error) {
	kind, _ := findType(monitorPrivateLinkType)
	_, parameters, err := c.resourceOperation(kind, parent, "GET")
	if err != nil {
		return nil, err
	}
	metadata, err := providerData()
	if err != nil {
		return nil, err
	}
	operation, _ := metadata.catalog.Operation("Azure.Microsoft.Insights.PrivateLinkResources_ListByPrivateLinkScope")
	bound, err := bindAzureREST(operation, parameters)
	if err != nil {
		return nil, err
	}
	u, _ := url.Parse(bound.URL)
	rows, err := c.listAllURL(ctx, bound.URL, u.Path)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	result := []any{}
	for _, row := range rows {
		raw := object(row)
		id, actual, err := parseID(text(raw["id"]))
		name := text(raw["name"])
		if err != nil || !strings.EqualFold(actual, monitorPrivateLinkType+"/privateLinkResources") || !strings.EqualFold(id, parent+"/privateLinkResources/"+name) || seen[id] || !validResponseType(monitorPrivateLinkType+"/privateLinkResources", text(raw["type"])) {
			return nil, serviceDenied("invalid_monitor_private_link_capability")
		}
		seen[id] = true
		parameters["groupName"] = name
		operation, _ := metadata.catalog.Operation("Azure.Microsoft.Insights.PrivateLinkResources_Get")
		bound, err := bindAzureREST(operation, parameters)
		if err != nil {
			return nil, err
		}
		live, err := c.request(ctx, "GET", bound.URL)
		if err != nil {
			return nil, err
		}
		if !validResourceResponse(live, id, actual) || !nativeConfigurationContains(monitorPrivateLinkSnapshot(actual, raw), monitorPrivateLinkSnapshot(actual, live.data)) || !strings.EqualFold(text(live.data["name"]), name) {
			return nil, serviceDenied("monitor_private_link_capabilities_changed")
		}
		properties := object(live.data["properties"])
		if text(properties["groupId"]) != name {
			return nil, serviceDenied("invalid_monitor_private_link_capability_group")
		}
		for _, field := range []string{"requiredMembers", "requiredZoneNames"} {
			values, ok := properties[field].([]any)
			if !ok || len(values) == 0 {
				return nil, serviceDenied("incomplete_monitor_private_link_capability")
			}
			seen := map[string]bool{}
			for _, value := range values {
				name := text(value)
				if name == "" || seen[name] {
					return nil, serviceDenied("invalid_monitor_private_link_capability_member")
				}
				seen[name] = true
			}
		}
		capability := safePayload(live.data)
		capability["id"], capability["name"], capability["type"] = id, last(id), monitorPrivateLinkType+"/privateLinkResources"
		result = append(result, capability)
	}
	if len(result) == 0 {
		return nil, serviceDenied("monitor_private_link_capabilities_missing")
	}
	slices.SortFunc(result, func(a, b any) int { return strings.Compare(text(object(a)["id"]), text(object(b)["id"])) })
	return result, nil
}

func (c *client) monitorPrivateLinkInventory(ctx context.Context, kind string, raw, normalized map[string]any) error {
	if monitorPrivateLinkKind(kind) == "" {
		return nil
	}
	if err := validateMonitorPrivateLink(kind, raw); err != nil {
		return err
	}
	normalized["_monitor_private_link_configuration"] = monitorPrivateLinkConfiguration(kind, raw)
	normalized["_monitor_private_link_private_configuration"] = c.privateConfiguration(monitorPrivateLinkSnapshot(kind, raw))
	id := text(raw["id"])
	if kind == monitorPrivateLinkType {
		capabilities, err := c.monitorPrivateLinkCapabilities(ctx, id)
		if err != nil {
			return err
		}
		normalized["privateLinkCapabilities"] = capabilities
		return c.verifyProductParent(ctx, productTarget{ParentID: id, ParentType: kind, Generation: productGeneration(raw)})
	}
	parent, err := c.monitorPrivateLinkParent(ctx, id)
	if err != nil {
		return err
	}
	normalized["_monitor_private_link_parent_configuration"] = c.privateConfiguration(monitorPrivateLinkSnapshot(monitorPrivateLinkType, parent))
	return nil
}

func (c *client) monitorPrivateLinkParent(ctx context.Context, child string) (map[string]any, error) {
	id := redisParentID(child)
	kind, _ := findType(monitorPrivateLinkType)
	endpoint, err := c.resourceURL(kind, id)
	if err != nil {
		return nil, err
	}
	live, err := c.request(ctx, "GET", endpoint)
	if err != nil {
		return nil, err
	}
	if !validResourceResponse(live, id, monitorPrivateLinkType) {
		return nil, serviceDenied("monitor_private_link_parent_changed")
	}
	if err := validateMonitorPrivateLink(monitorPrivateLinkType, live.data); err != nil {
		return nil, err
	}
	return live.data, nil
}

func (c *client) monitorPrivateLinkChildren(ctx context.Context, parent asset.Identity, raw map[string]any) ([]serviceChild, error) {
	first, err := c.nativeServiceChildren(ctx, parent, raw, serviceChildKinds(monitorPrivateLinkType))
	if err != nil {
		return nil, err
	}
	second, err := c.nativeServiceChildren(ctx, parent, raw, serviceChildKinds(monitorPrivateLinkType))
	if err != nil {
		return nil, err
	}
	if !slices.EqualFunc(first, second, func(a, b serviceChild) bool {
		return a.id == b.id && c.privateConfiguration(monitorPrivateLinkSnapshot(a.kind, a.data)) == c.privateConfiguration(monitorPrivateLinkSnapshot(b.kind, b.data))
	}) {
		return nil, serviceDenied("monitor_private_link_children_changed")
	}
	if value, present := object(raw["properties"])["privateEndpointConnections"]; present {
		rows, ok := value.([]any)
		if !ok {
			return nil, serviceDenied("invalid_monitor_private_link_embedded_connections")
		}
		connections := map[string]map[string]any{}
		for _, child := range second {
			if child.kind == monitorPrivateConnectionType {
				connections[child.id] = child.data
			}
		}
		for _, row := range rows {
			id := strings.ToLower(text(object(row)["id"]))
			if connections[id] == nil || monitorPrivateLinkListed(monitorPrivateConnectionType, object(row), connections[id]) != nil {
				return nil, serviceDenied("monitor_private_link_embedded_connections_changed")
			}
			delete(connections, id)
		}
		if len(connections) != 0 {
			return nil, serviceDenied("monitor_private_link_embedded_connections_changed")
		}
	}
	return second, nil
}

func (a *action) monitorPrivateLinkPreflight(ctx context.Context, planned asset.Asset, raw map[string]any) error {
	if monitorPrivateLinkKind(a.kind.NativeType) == "" {
		return nil
	}
	if err := monitorPrivateLinkIncarnation(planned, raw); err != nil {
		return err
	}
	if a.kind.NativeType == monitorPrivateLinkType {
		capabilities, err := a.client.monitorPrivateLinkCapabilities(ctx, a.id)
		if err != nil {
			return err
		}
		if a.client.privateConfiguration(map[string]any{"value": planned.Normalized["privateLinkCapabilities"]}) != a.client.privateConfiguration(map[string]any{"value": capabilities}) {
			return serviceDenied("monitor_private_link_capabilities_changed")
		}
		return nil
	}
	parent, err := a.client.monitorPrivateLinkParent(ctx, a.id)
	if err != nil {
		return err
	}
	if expected := text(planned.Normalized["_monitor_private_link_parent_configuration"]); expected == "" || expected != a.client.privateConfiguration(monitorPrivateLinkSnapshot(monitorPrivateLinkType, parent)) {
		return serviceDenied("monitor_private_link_parent_changed")
	}
	return nil
}

func (a *action) monitorPrivateLinkRequestIdentity(value asset.Asset) error {
	if (monitorPrivateLinkKind(a.kind.NativeType) != "" || monitorPrivateLinkTarget(a.kind.NativeType)) && (value.Identity.Provider != asset.ProviderAzure || value.Identity.ConnectionID != a.connectionID || value.Identity.Partition != a.partition || !strings.EqualFold(value.Identity.NativeType, a.kind.NativeType) || !strings.EqualFold(value.Identity.NativeID, a.id)) {
		return serviceDenied("monitor_private_link_action_identity_changed")
	}
	return nil
}

func (a *action) monitorPrivateLinkOperationBinding(operation string) string {
	return a.client.privateConfiguration(map[string]any{"subscription": a.client.subscription, "tenant": a.client.tenant, "connection": string(a.connectionID), "partition": a.partition, "resource": a.id, "kind": a.kind.NativeType, "operation": operation, "polling": "status"})
}

func (a *action) monitorPrivateLinkPollReceipt(result contracts.ActionResult) error {
	if monitorPrivateLinkKind(a.kind.NativeType) != "" && (text(result.Data["polling"]) != "status" || text(result.Data["monitor_private_link_operation_binding"]) != a.monitorPrivateLinkOperationBinding(result.ProviderOperationID)) {
		return serviceDenied("monitor_private_link_poll_receipt_changed")
	}
	return nil
}
