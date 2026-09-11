package azure

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"maps"
	"slices"
	"strings"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func insightsWorkbookCategory(value string) string {
	for _, known := range []string{"workbook", "TSG", "performance", "retention"} {
		if strings.EqualFold(value, known) {
			return known
		}
	}
	return value
}

// Workbooks expose required category filters rather than an unfiltered native
// index. Enumerate every documented category and those learned through ARM and
// saved IDs. Unseen custom categories cannot authorize an absence sweep.
func (c *client) workbookIndex(ctx context.Context, kind string, known []string, groups map[string]map[string]any) (map[string]map[string]any, []string, string, error) {
	values := map[string]map[string]any{}
	categories := map[string]bool{"workbook": true, "TSG": true, "performance": true, "retention": true}
	if kind != insightsWorkbookTemplateType {
		seedIDs := map[string]bool{}
		for _, id := range known {
			canonical, typ, err := parseID(id)
			if err != nil || canonical != id || insightsWorkbookKind(typ) != kind || !strings.HasPrefix(id, c.root()+"/") || seedIDs[id] {
				return nil, nil, "", serviceDenied("invalid_known_workbook_identity")
			}
			seedIDs[id] = true
		}
		rows, err := c.insightsARMIndex(ctx, c.root()+"/resources")
		if err != nil {
			return nil, nil, "", err
		}
		seen := map[string]bool{}
		for _, value := range rows {
			raw := object(value)
			id, typ, err := parseID(text(raw["id"]))
			if err != nil || !strings.HasPrefix(id, c.root()+"/") || seen[id] {
				return nil, nil, "", serviceDenied("invalid_workbook_arm_index")
			}
			seen[id] = true
			if insightsWorkbookKind(typ) != kind {
				continue
			}
			if !validResponseType(kind, text(raw["type"])) {
				return nil, nil, "", serviceDenied("workbook_arm_index_type_changed")
			}
			seedIDs[id] = true
		}
		for _, id := range slices.Sorted(maps.Keys(seedIDs)) {
			current, err := c.workbookRead(ctx, kind, id)
			if isNotFound(err) {
				if seen[id] {
					return nil, nil, "", serviceDenied("workbook_arm_index_changed")
				}
				continue // Only this known identity can receive an explicit absence result.
			}
			if err != nil {
				return nil, nil, "", err
			}
			values[id] = current.data
			categories[insightsWorkbookCategory(text(object(current.data["properties"])["category"]))] = true
		}
	}
	provenance := ""
	read := func(operation string, params map[string]any, group, category string) error {
		rows, requestID, err := c.workbookList(ctx, operation, kind, params)
		if err != nil {
			return err
		}
		if requestID != "" {
			provenance = requestID
		}
		seen := map[string]bool{}
		for _, value := range rows {
			raw := object(value)
			id, typ, err := parseID(text(raw["id"]))
			if err != nil || insightsWorkbookKind(typ) != kind || !strings.HasPrefix(id, c.root()+"/") || seen[id] || group != "" && strings.Join(strings.Split(id, "/")[:5], "/") != group || insightsWorkbookIdentity(raw, id, kind, false) != nil {
				return serviceDenied("invalid_workbook_list_identity")
			}
			seen[id] = true
			if category != "" && insightsWorkbookCategory(text(object(raw["properties"])["category"])) != category {
				return serviceDenied("workbook_category_changed")
			}
			current, err := c.workbookRead(ctx, kind, id)
			if err != nil {
				return err
			}
			if !nativeConfigurationContains(insightsWorkbookSnapshot(raw, true), insightsWorkbookSnapshot(current.data, false)) {
				return serviceDenied("workbook_list_configuration_changed")
			}
			if previous := values[id]; previous != nil && c.privateConfiguration(insightsWorkbookSnapshot(previous, false)) != c.privateConfiguration(insightsWorkbookSnapshot(current.data, false)) {
				return serviceDenied("workbook_indexes_disagree")
			}
			values[id] = current.data
		}
		return nil
	}
	if kind == insightsWorkbookTemplateType {
		for _, id := range slices.Sorted(maps.Keys(groups)) {
			before, err := c.insightsGroup(ctx, id, groups[id])
			if err != nil {
				return nil, nil, "", err
			}
			if err := read("WorkbookTemplates_ListByResourceGroup", map[string]any{"subscriptionId": c.subscription, "resourceGroupName": strings.Split(id, "/")[4]}, id, ""); err != nil {
				return nil, nil, "", err
			}
			if _, err := c.insightsGroup(ctx, id, before); err != nil {
				return nil, nil, "", err
			}
		}
	} else {
		operation := "Workbooks_ListBySubscription"
		if kind == insightsMyWorkbookType {
			operation = "MyWorkbooks_ListBySubscription"
		}
		for _, category := range slices.Sorted(maps.Keys(categories)) {
			if err := read(operation, map[string]any{"subscriptionId": c.subscription, "category": category, "canFetchContent": true}, "", category); err != nil {
				return nil, nil, "", err
			}
		}
	}
	var absent []string
	for _, id := range known {
		if values[id] == nil {
			absent = append(absent, id)
		}
	}
	slices.Sort(absent)
	return values, absent, provenance, nil
}

func (r *Runtime) workbookInventorySnapshot(ctx context.Context, c *client, request contracts.InventoryRequest) ([]contracts.InventoryItem, []string, string, error) {
	kind := insightsWorkbookKind(request.ResourceKind.NativeType)
	groups, err := c.insightsGroups(ctx)
	if err != nil {
		return nil, nil, "", err
	}
	values, absent, requestID, err := c.workbookIndex(ctx, kind, request.KnownNativeIDs, groups)
	if err != nil {
		return nil, nil, "", err
	}
	locks, err := c.managementLocks(ctx)
	if err != nil {
		return nil, nil, "", err
	}
	owners := map[string]string{}
	for id, raw := range groups {
		owners[id] = text(raw["managedBy"])
	}
	var items []contracts.InventoryItem
	for _, id := range slices.Sorted(maps.Keys(values)) {
		groupID := strings.Join(strings.Split(id, "/")[:5], "/")
		if groups[groupID] == nil {
			return nil, nil, "", serviceDenied("workbook_resource_group_missing_from_index")
		}
		group, err := c.insightsGroup(ctx, groupID, groups[groupID])
		if err != nil {
			return nil, nil, "", err
		}
		raw := maps.Clone(values[id])
		raw["type"] = kind // The template API also publishes its singular type alias.
		item, err := r.inventoryItem(ctx, c, raw, owners, locks)
		if err != nil {
			return nil, nil, "", err
		}
		if !productScopeMatches(request, item) {
			continue
		}
		if _, err := c.insightsGroup(ctx, groupID, group); err != nil {
			return nil, nil, "", err
		}
		item.Normalized["_inventory_source"] = insightsInventorySource(kind)
		if item.Normalized["_insights_workbook_group"] != c.privateConfiguration(insightsWorkspaceResourceSnapshot(group)) {
			return nil, nil, "", serviceDenied("workbook_group_changed")
		}
		if kind != insightsWorkbookTemplateType {
			item.Name = text(object(raw["properties"])["displayName"])
			item.Normalized["name"] = item.Name
		}
		if protectedAzureTags(object(group["tags"])) {
			item.Normalized["cleanup_protected"], item.Normalized["cleanup_protection_reason"] = true, "azure_protected_tag"
		}
		items = append(items, item)
	}
	return items, absent, requestID, nil
}

func (r *Runtime) listInsightsWorkbooks(ctx context.Context, c *client, request contracts.InventoryRequest) (batch contracts.InventoryBatch, err error) {
	defer func() { err = contracts.DependencyReadError(err) }()
	kind := insightsWorkbookKind(request.ResourceKind.NativeType)
	if request.Source != insightsInventorySource(kind) || len(request.Options) != 0 || kind == insightsWorkbookTemplateType && len(request.KnownNativeIDs) != 0 {
		return batch, serviceDenied("invalid_workbook_inventory_source")
	}
	switch request.Scope.Kind {
	case asset.ScopeSubscription:
		if !strings.EqualFold(request.Scope.NativeID, c.subscription) {
			return batch, serviceDenied("workbook_subscription_changed")
		}
	case asset.ScopeRegion:
		if request.Scope.NativeID == "" {
			return batch, serviceDenied("workbook_region_missing")
		}
	case asset.ScopeGlobal:
		if !strings.EqualFold(request.Scope.NativeID, c.subscription+"/global") && request.Scope.NativeID != "global" {
			return batch, serviceDenied("workbook_global_scope_changed")
		}
	default:
		return batch, serviceDenied("invalid_workbook_inventory_scope")
	}
	cursor := productCursor{}
	if request.Cursor != "" {
		if len(request.Cursor) > 128<<10 {
			return batch, serviceDenied("workbook_cursor_too_large")
		}
		data, err := base64.RawURLEncoding.DecodeString(request.Cursor)
		if err != nil || json.Unmarshal(data, &cursor) != nil || cursor.Target < 1 || cursor.Fingerprint == "" || cursor.Next != "" || len(cursor.Seen)+len(cursor.Resources) != 0 {
			return batch, serviceDenied("invalid_workbook_cursor")
		}
	}
	first, absent, provenance, err := r.workbookInventorySnapshot(ctx, c, request)
	if err != nil {
		return batch, err
	}
	second, afterAbsent, _, err := r.workbookInventorySnapshot(ctx, c, request)
	if err != nil {
		return batch, err
	}
	bindings := func(items []contracts.InventoryItem) map[string]any {
		result := map[string]any{}
		for _, item := range items {
			result[item.NativeID] = map[string]any{"configuration": item.Normalized[insightsWorkbookProof], "group": item.Normalized["_insights_workbook_group"], "protected": item.Normalized["cleanup_protection_reason"]}
		}
		return result
	}
	if !slices.Equal(absent, afterAbsent) || c.privateConfiguration(bindings(first)) != c.privateConfiguration(bindings(second)) {
		return batch, serviceDenied("workbook_collection_changed_during_scan")
	}
	boundary := request
	boundary.Cursor, boundary.Limit = "", 0
	fingerprint := c.privateConfiguration(map[string]any{"request": boundary, "revision": r.bundle.Revision, "bindings": bindings(first), "absent": absent})
	if request.Cursor != "" && (cursor.Fingerprint != fingerprint || cursor.Target >= len(first)) {
		return batch, serviceDenied("workbook_cursor_changed")
	}
	limit := request.Limit
	if limit <= 0 {
		limit = 1000
	}
	end := cursor.Target + min(limit, len(first)-cursor.Target)
	batch = contracts.InventoryBatch{Items: first[cursor.Target:end], Complete: end == len(first), RequestID: provenance}
	if batch.Complete {
		batch.AbsentNativeIDs = absent
	} else {
		cursor.Fingerprint, cursor.Target = fingerprint, end
		encoded, _ := json.Marshal(cursor)
		batch.NextCursor = base64.RawURLEncoding.EncodeToString(encoded)
	}
	return batch, nil
}
