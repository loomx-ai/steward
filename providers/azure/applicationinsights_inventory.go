package azure

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"maps"
	"net/url"
	"slices"
	"strings"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func (c *client) insightsComponents(ctx context.Context) ([]serviceChild, string, error) {
	data, err := providerData()
	if err != nil {
		return nil, "", err
	}
	op, ok := data.catalog.Operation(insightsOperationPrefix + "Components_List")
	if !ok || op.Call == nil || op.Call.Method != "GET" || op.Call.Version != insightsComponentVersion {
		return nil, "", serviceDenied("invalid_insights_component_list_binding")
	}
	request, err := bindAzureREST(op, map[string]any{"subscriptionId": c.subscription})
	if err != nil {
		return nil, "", err
	}
	u, _ := url.Parse(request.URL)
	var values []any
	provenance := ""
	pages := map[string]bool{}
	for endpoint := request.URL; endpoint != ""; {
		parsed, err := url.Parse(endpoint)
		if err != nil || pages[endpoint] {
			return nil, "", serviceDenied("insights_component_pagination_failed")
		}
		query, err := url.ParseQuery(parsed.RawQuery)
		if err != nil || len(query["api-version"]) != 1 || query.Get("api-version") != insightsComponentVersion || len(query["$skiptoken"])+len(query["skiptoken"]) > 1 {
			return nil, "", serviceDenied("invalid_insights_component_list_query")
		}
		for key, entries := range query {
			if key != "api-version" && key != "$skiptoken" && key != "skiptoken" || len(entries) != 1 || entries[0] == "" {
				return nil, "", serviceDenied("invalid_insights_component_list_query")
			}
		}
		pages[endpoint] = true
		page, next, result, err := c.listPageResult(ctx, endpoint, u.Path)
		if err != nil {
			return nil, "", err
		}
		if operationLocation(result.header) != "" || result.data["code"] != nil {
			return nil, "", serviceDenied("incomplete_insights_component_list")
		}
		if result.requestID != "" {
			provenance = result.requestID
		}
		values = append(values, page...)
		endpoint = next
	}
	seen := map[string]bool{}
	var components []serviceChild
	for _, value := range values {
		raw, ok := value.(map[string]any)
		id, kind, err := parseID(text(raw["id"]))
		if !ok || err != nil || !strings.EqualFold(kind, applicationInsightsType) || !strings.EqualFold(text(raw["type"]), applicationInsightsType) || !strings.HasPrefix(id, c.root()+"/") || seen[id] {
			return nil, "", serviceDenied("invalid_insights_component_list_identity")
		}
		seen[id] = true
		current, err := c.insightsComponent(ctx, id)
		if err != nil {
			return nil, "", err // A vanished parent cannot prove an empty child index.
		}
		if !nativeConfigurationContains(monitorPrivateLinkTargetSnapshot(raw), monitorPrivateLinkTargetSnapshot(current)) {
			return nil, "", serviceDenied("insights_component_list_configuration_changed")
		}
		components = append(components, serviceChild{id: id, kind: applicationInsightsType, data: current})
	}
	slices.SortFunc(components, func(a, b serviceChild) int { return strings.Compare(a.id, b.id) })
	return components, provenance, nil
}

func (r *Runtime) insightsChildItem(ctx context.Context, c *client, parent contracts.InventoryItem, child serviceChild) (contracts.InventoryItem, error) {
	id, component, kind, selector, err := insightsChildIdentity(child.id)
	if err != nil || id != child.id || kind != child.kind || component != parent.NativeID || !strings.HasPrefix(component, c.root()+"/") {
		return contracts.InventoryItem{}, serviceDenied("invalid_insights_legacy_inventory_identity")
	}
	if insightsARMChildKind(kind) != "" {
		if err := insightsARMChildResponseIdentity(id, kind, child.data); err != nil {
			return contracts.InventoryItem{}, err
		}
	} else if err := insightsLegacyResponseIdentity(insightsLegacyKind(kind), selector, child.data); err != nil {
		return contracts.InventoryItem{}, err
	}
	safe := safePayload(object(applicationInsightsSafeValue(child.data)))
	normalized := maps.Clone(safe)
	name := text(child.data["Name"])
	if kind == insightsAnnotationType {
		name = text(child.data["AnnotationName"])
	}
	if insightsARMChildKind(kind) != "" {
		name = text(child.data["name"])
	}
	if name == "" {
		name = selector
	}
	normalized["name"], normalized["subscription_id"], normalized["resource_group"] = name, c.subscription, strings.Split(component, "/")[4]
	normalized["_inventory_source"] = insightsInventorySource(kind)
	normalized["_insights_component"] = component
	normalized["_insights_component_configuration"] = parent.Normalized["_monitor_private_link_target_configuration"]
	normalized["_insights_group_configuration"] = parent.Normalized["_insights_group_configuration"]
	normalized[insightsChildProofKey(kind)] = c.insightsChildConfiguration(id, kind, child.data)
	for _, key := range []string{"cleanup_protection_reason", "cleanup_protected", "cleanup_controller_only"} {
		if value, exists := parent.Normalized[key]; exists {
			normalized[key] = value
		}
	}
	refs := map[string][]string{applicationInsightsType: {component}}
	if kind == insightsExportType {
		if err := c.insightsExportReferences(ctx, child.data, refs); err != nil {
			return contracts.InventoryItem{}, err
		}
	}
	if kind == insightsLinkedStorageType {
		target, _, _ := parseID(text(object(child.data["properties"])["linkedStorageAccount"]))
		addReference(refs, storageType, target)
	}
	var network []string
	for typ, values := range refs {
		normalized[referenceKey(typ)] = values
		network = append(network, values...)
	}
	slices.Sort(network)
	actionable := true
	return contracts.InventoryItem{NativeID: id, NativeType: kind, ResourceKind: r.resourceKind(kind), Actionable: &actionable,
		Scope: parent.Scope, Name: name, Location: parent.Location, Tags: map[string]string{}, Normalized: normalized,
		Raw: safe, NativeAliases: []string{id}, NetworkReferences: network}, nil
}

func (r *Runtime) insightsInventorySnapshot(ctx context.Context, c *client, request contracts.InventoryRequest, window insightsAnnotationWindow) ([]contracts.InventoryItem, []string, string, error) {
	components, provenance, err := c.insightsComponents(ctx)
	if err != nil {
		return nil, nil, "", err
	}
	var absent []string
	if request.ResourceKind.NativeType == insightsAnnotationType {
		absent, err = c.insightsAnnotationParents(ctx, components, request.KnownNativeIDs)
		if err != nil {
			return nil, nil, "", err
		}
	}
	indexedGroups, err := c.insightsGroups(ctx)
	if err != nil {
		return nil, nil, "", err
	}
	owners := map[string]string{}
	for id, group := range indexedGroups {
		owners[id] = text(group["managedBy"])
	}
	locks, err := c.managementLocks(ctx)
	if err != nil {
		return nil, nil, "", err
	}
	var items []contracts.InventoryItem
	groups := map[string]map[string]any{}
	for _, component := range components {
		parent, err := r.inventoryItem(ctx, c, component.data, owners, locks)
		if err != nil {
			return nil, nil, "", err
		}
		if !productScopeMatches(request, parent) {
			continue
		}
		groupID := strings.Join(strings.Split(component.id, "/")[:5], "/")
		group, exists := groups[groupID]
		if !exists {
			if indexedGroups[groupID] == nil {
				return nil, nil, "", serviceDenied("insights_inventory_group_disagrees")
			}
			group, err = c.insightsGroup(ctx, groupID, indexedGroups[groupID])
			if err != nil {
				return nil, nil, "", err
			}
			groups[groupID] = group
		}
		if protectedAzureTags(object(group["tags"])) {
			parent.Normalized["cleanup_protected"] = true
			parent.Normalized["cleanup_protection_reason"] = "azure_protected_tag"
		}
		parent.Normalized["_insights_group_configuration"] = c.privateConfiguration(map[string]any{"id": groupID, "tags": group["tags"], "managedBy": group["managedBy"]})
		parent.Normalized["_inventory_source"] = productInventorySource
		if strings.EqualFold(request.ResourceKind.NativeType, applicationInsightsType) {
			if err := c.insightsWorkspaceInventory(ctx, &parent, component.data, indexedGroups); err != nil {
				return nil, nil, "", err
			}
			if err := c.insightsConfigurationInventory(ctx, &parent, component.data); err != nil {
				return nil, nil, "", err
			}
			items = append(items, parent)
			continue
		}
		var children []serviceChild
		var windowIDs map[string]bool
		var childAbsent []string
		if request.ResourceKind.NativeType == insightsAnnotationType {
			children, windowIDs, childAbsent, err = c.insightsAnnotationInventoryChildren(ctx, component.id, window, request.KnownNativeIDs)
		} else {
			children, err = c.insightsChildren(ctx, component.id, request.ResourceKind.NativeType)
		}
		if err != nil {
			return nil, nil, "", err
		}
		absent = append(absent, childAbsent...)
		current, err := c.insightsComponent(ctx, component.id)
		if err != nil {
			return nil, nil, "", err
		}
		if c.privateConfiguration(monitorPrivateLinkTargetSnapshot(current)) != parent.Normalized["_monitor_private_link_target_configuration"] {
			return nil, nil, "", serviceDenied("insights_inventory_component_changed")
		}
		for _, child := range children {
			item, err := r.insightsChildItem(ctx, c, parent, child)
			if err != nil {
				return nil, nil, "", err
			}
			if item.NativeType == insightsAnnotationType {
				item.Normalized["_insights_annotation_window"] = window
				item.Normalized["_insights_annotation_discovery"] = "known-id"
				if windowIDs[item.NativeID] {
					item.Normalized["_insights_annotation_discovery"] = "time-window"
				}
			}
			items = append(items, item)
		}
	}
	slices.SortFunc(items, func(a, b contracts.InventoryItem) int { return strings.Compare(a.NativeID, b.NativeID) })
	slices.Sort(absent)
	return items, absent, provenance, nil
}

func insightsInventoryBindings(items []contracts.InventoryItem) map[string]any {
	bindings := map[string]any{}
	for _, item := range items {
		value := map[string]any{"kind": item.NativeType, "location": item.Location, "scope": item.Scope, "references": item.NetworkReferences}
		for _, key := range []string{insightsSettingsProof, "_monitor_private_link_target_configuration", "_insights_component_configuration", "_insights_legacy_private_configuration", "_insights_child_private_configuration", "_insights_group_configuration", "_insights_workspace_configuration", "cleanup_protection_reason", "cleanup_protected", "cleanup_controller_only"} {
			value[key] = item.Normalized[key]
		}
		bindings[item.NativeID] = value
	}
	return bindings
}

func (c *client) insightsExportReferences(ctx context.Context, raw map[string]any, refs map[string][]string) error {
	fields := map[string]string{}
	for _, key := range []string{"DestinationAccountId", "StorageName", "DestinationStorageSubscriptionId", "ContainerName", "DestinationType"} {
		if value := raw[key]; value != nil {
			text, valid := value.(string)
			if !valid || text != strings.TrimSpace(text) {
				return serviceDenied("invalid_insights_export_destination")
			}
			fields[key] = text
		}
	}
	destination, name, subscription := fields["DestinationAccountId"], fields["StorageName"], fields["DestinationStorageSubscriptionId"]
	if subscription != "" && !uuidPattern.MatchString(subscription) {
		return serviceDenied("invalid_insights_export_destination_subscription")
	}
	if strings.HasPrefix(destination, "/") {
		id, kind, err := parseID(destination)
		if err != nil || !strings.EqualFold(kind, storageType) || name != "" && !strings.EqualFold(name, last(id)) || subscription != "" && !strings.EqualFold(subscription, strings.Split(id, "/")[2]) {
			return serviceDenied("insights_export_destination_disagrees")
		}
		destination = id
	} else {
		if destination != "" && name != "" && !strings.EqualFold(destination, name) {
			return serviceDenied("insights_export_destination_disagrees")
		}
		if name == "" {
			name = destination
		}
		name = strings.ToLower(name)
		if name == "" {
			if fields["ContainerName"] != "" {
				return serviceDenied("insights_export_container_account_missing")
			}
			return nil
		}
		if !storageNamePattern.MatchString(name) {
			return serviceDenied("invalid_insights_export_storage_name")
		}
		// Resolve native account names through the complete selected-subscription
		// ARM index. A foreign or unresolvable name remains a graph reference;
		// it never supplies permission to read another subscription.
		destination = name
		if subscription == "" || strings.EqualFold(subscription, c.subscription) {
			values, err := c.subscriptionReferenceIndex(ctx, storageType)
			if err != nil {
				return err
			}
			for _, value := range values {
				if !strings.EqualFold(text(value["name"]), name) {
					continue
				}
				id, _, _ := parseID(text(value["id"]))
				current, err := c.linkedResource(ctx, id)
				if err != nil {
					return err
				}
				if destination != name || !strings.EqualFold(text(current["name"]), name) || serviceListedIncarnation(value, current) != nil {
					return serviceDenied("insights_export_storage_changed_or_ambiguous")
				}
				destination = id
			}
		}
	}
	addReference(refs, storageType, destination)
	if container := fields["ContainerName"]; container != "" && strings.HasPrefix(destination, "/") {
		if fields["DestinationType"] != "" && !strings.EqualFold(fields["DestinationType"], "Blob") {
			return serviceDenied("unsupported_insights_export_container_type")
		}
		id, err := cognitiveNameID(destination, "blobServices/default/containers", container)
		if err != nil {
			return err
		}
		addReference(refs, containerType, id)
	}
	return nil
}

func (r *Runtime) listInsights(ctx context.Context, c *client, request contracts.InventoryRequest) (batch contracts.InventoryBatch, err error) {
	defer func() { err = contracts.DependencyReadError(err) }()
	definition, known := r.productDefinition(request.ResourceKind.NativeType)
	if !known || definition.Discovery.List == nil {
		return batch, serviceDenied("insights_inventory_coverage_unavailable")
	}
	switch request.Scope.Kind {
	case asset.ScopeSubscription:
		if !strings.EqualFold(request.Scope.NativeID, c.subscription) {
			return batch, serviceDenied("insights_inventory_subscription_changed")
		}
	case asset.ScopeRegion:
		if request.Scope.NativeID == "" {
			return batch, serviceDenied("insights_inventory_region_missing")
		}
	case asset.ScopeGlobal:
		if !strings.EqualFold(request.Scope.NativeID, c.subscription+"/global") && request.Scope.NativeID != "global" {
			return batch, serviceDenied("insights_inventory_global_scope_changed")
		}
	default:
		return batch, serviceDenied("invalid_insights_inventory_scope")
	}
	cursor := insightsInventoryCursor{}
	if request.Cursor != "" {
		if len(request.Cursor) > 128<<10 {
			return batch, serviceDenied("insights_inventory_cursor_too_large")
		}
		raw, err := base64.RawURLEncoding.DecodeString(request.Cursor)
		if err != nil || json.Unmarshal(raw, &cursor) != nil || cursor.Target < 1 || cursor.Next != "" || len(cursor.Seen) != 0 || len(cursor.Resources) != 0 || cursor.Fingerprint == "" {
			return batch, serviceDenied("invalid_insights_inventory_cursor")
		}
	}
	annotation := request.ResourceKind.NativeType == insightsAnnotationType
	if annotation {
		if request.Source != insightsAnnotationSource || len(request.Options) != 0 {
			return batch, serviceDenied("invalid_insights_annotation_inventory_source")
		}
		if request.Cursor == "" {
			cursor.Window = insightsRecentAnnotationWindow()
		}
		if _, err := insightsLegacyListParameters(insightsLegacyKind(insightsAnnotationType), cursor.Window); err != nil {
			return batch, err
		}
	} else if cursor.Window != (insightsAnnotationWindow{}) || len(request.KnownNativeIDs) != 0 {
		return batch, serviceDenied("unexpected_insights_annotation_window")
	}
	items, absent, provenance, err := r.insightsInventorySnapshot(ctx, c, request, cursor.Window)
	if err != nil {
		return batch, err
	}
	// As with Batch's native inventory, materialize the current collection
	// before slicing. Two independently read snapshots catch membership and
	// private-configuration drift; an old cursor must not skip new resources.
	current, afterAbsent, _, err := r.insightsInventorySnapshot(ctx, c, request, cursor.Window)
	if err != nil {
		return batch, err
	}
	bindings := insightsInventoryBindings(items)
	if !slices.Equal(absent, afterAbsent) || c.privateConfiguration(bindings) != c.privateConfiguration(insightsInventoryBindings(current)) {
		return batch, serviceDenied("insights_inventory_changed_during_scan")
	}
	boundary := request
	boundary.Cursor, boundary.Limit = "", 0
	fingerprint := c.privateConfiguration(map[string]any{"request": boundary, "revision": r.bundle.Revision, "bindings": bindings, "absent": absent, "window": cursor.Window})
	if request.Cursor != "" && (cursor.Fingerprint != fingerprint || cursor.Target >= len(items)) {
		return batch, serviceDenied("insights_inventory_cursor_changed")
	}
	cursor.Fingerprint = fingerprint
	limit := request.Limit
	if limit <= 0 {
		limit = 1000
	}
	end := cursor.Target + min(limit, len(items)-cursor.Target)
	batch = contracts.InventoryBatch{Items: items[cursor.Target:end], Complete: end == len(items), RequestID: provenance}
	if batch.Complete {
		batch.AbsentNativeIDs = absent
	} else {
		cursor.Target = end
		raw, _ := json.Marshal(cursor)
		batch.NextCursor = base64.RawURLEncoding.EncodeToString(raw)
	}
	return batch, nil
}
