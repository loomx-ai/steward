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

const monitorConfigurationProof = "_monitor_resource_configuration"
const monitorGroupProof = "_monitor_resource_group"
const monitorReferencesProof = "_monitor_references_binding"

func monitorResourceKind(kind string) string {
	if row := monitorRuleKind(kind); row.kind != "" {
		return row.kind
	}
	kind, _ = monitorBudgetKind(kind)
	return kind
}

func monitorResourceID(wire string) (id, scope, kind string, err error) {
	if monitorBudgetPath(wire) {
		return monitorBudgetID(wire)
	}
	id, kind, err = parseID(wire)
	if err != nil || monitorRuleKind(kind).kind == "" || wire != strings.TrimSpace(wire) {
		return "", "", "", serviceDenied("invalid_monitor_resource_identity")
	}
	return id, strings.Join(strings.Split(id, "/")[:5], "/"), monitorRuleKind(kind).kind, nil
}

func monitorResourceSnapshot(kind string, raw map[string]any) map[string]any {
	if budget, _ := monitorBudgetKind(kind); budget != "" {
		return monitorBudgetSnapshot(raw)
	}
	return monitorRuleSnapshot(kind, raw)
}

func monitorResourceRegion(kind string, raw map[string]any) string {
	if budget, _ := monitorBudgetKind(kind); budget != "" {
		return "global"
	}
	return resourceRegion(raw)
}

func (c *client) monitorResourceRead(ctx context.Context, kind, id string) (response, error) {
	if budget, _ := monitorBudgetKind(kind); budget != "" {
		return c.monitorBudgetRead(ctx, id)
	}
	return c.monitorRuleRead(ctx, kind, id)
}

// A subscription budget index can contain group budgets as well. Enumerate the
// native group indexes too, and reconcile duplicate identities by their full
// private configuration. A group omitted from either index is never fabricated.
func (c *client) monitorResourceIndex(ctx context.Context, kind string, groups map[string]map[string]any) (map[string]map[string]any, string, error) {
	if monitorRuleKind(kind).kind != "" {
		return c.monitorRuleIndex(ctx, kind)
	}
	values, requestID, err := c.monitorBudgetIndex(ctx, kind, c.root())
	if err != nil {
		return nil, "", err
	}
	for _, scope := range slices.Sorted(maps.Keys(groups)) {
		rows, provenance, err := c.monitorBudgetIndex(ctx, kind, scope)
		if err != nil {
			return nil, "", err
		}
		for id, raw := range rows {
			if previous := values[id]; previous != nil && c.privateConfiguration(monitorBudgetSnapshot(previous)) != c.privateConfiguration(monitorBudgetSnapshot(raw)) {
				return nil, "", serviceDenied("monitor_budget_indexes_disagree")
			}
			values[id] = raw
		}
		if provenance != "" {
			requestID = provenance
		}
	}
	return values, requestID, nil
}

// Budgets may live directly under a subscription. Keeping their projection here
// avoids teaching the generic resource-group parser to accept new ARM scopes.
func (r *Runtime) monitorInventoryItem(ctx context.Context, c *client, raw map[string]any, owners map[string]string, locks []any) (contracts.InventoryItem, error) {
	id, scopeID, kind, err := monitorResourceID(text(raw["id"]))
	if err != nil || !strings.HasPrefix(id, c.root()+"/") || !validResponseType(kind, text(raw["type"])) {
		return contracts.InventoryItem{}, serviceDenied("monitor_inventory_identity_changed")
	}
	current, err := c.monitorResourceRead(ctx, kind, id)
	if err != nil {
		return contracts.InventoryItem{}, contracts.DependencyReadError(err)
	}
	configuration := c.privateConfiguration(monitorResourceSnapshot(kind, current.data))
	if configuration != c.privateConfiguration(monitorResourceSnapshot(kind, raw)) {
		return contracts.InventoryItem{}, serviceDenied("monitor_inventory_configuration_changed")
	}
	raw = maps.Clone(current.data)
	raw["id"], raw["type"] = id, kind
	if raw["name"] == nil {
		raw["name"] = last(id)
	}
	safe := safePayload(raw)
	normalized := maps.Clone(object(safe["properties"]))
	normalized["name"], normalized["subscription_id"], normalized["tags"] = safe["name"], c.subscription, safe["tags"]
	normalized[monitorConfigurationProof] = configuration
	normalized["_inventory_source"] = productInventorySource
	normalized["_arm_generation"] = productGeneration(raw)
	if creation := creationGeneration(raw); creation != "" {
		normalized["_arm_creation_generation"] = creation
	}
	normalized["arm_etag"] = text(raw["etag"])
	group := map[string]any{}
	if scopeID != c.root() {
		owner, indexed := owners[scopeID]
		if !indexed {
			return contracts.InventoryItem{}, serviceDenied("monitor_resource_group_missing_from_index")
		}
		group, err = c.workbookGroup(ctx, id)
		if err != nil {
			return contracts.InventoryItem{}, contracts.DependencyReadError(err)
		}
		if !strings.EqualFold(owner, text(group["managedBy"])) {
			return contracts.InventoryItem{}, serviceDenied("monitor_resource_group_owner_changed")
		}
		normalized["resource_group"] = last(scopeID)
	}
	normalized[monitorGroupProof] = c.privateConfiguration(insightsWorkspaceResourceSnapshot(group))
	mapping, known := findType(kind)
	reason := protectionReason(mapping, raw)
	if text(group["managedBy"]) != "" && (reason == "" || controllerOnlyReason(reason)) {
		reason = "azure_managed_resource_group"
	}
	if protectedAzureTags(object(group["tags"])) {
		reason = "azure_protected_tag"
	}
	if locked(id, locks) {
		reason = "azure_management_lock"
	}
	if reason != "" {
		if controllerOnlyReason(reason) {
			normalized["cleanup_controller_only"] = true
		} else {
			normalized["cleanup_protected"] = true
		}
		normalized["cleanup_protection_reason"] = reason
	}
	if budget, _ := monitorBudgetKind(kind); budget != "" {
		normalized["arm_parameters"] = map[string]any{"scope": strings.TrimPrefix(scopeID, "/"), "budgetName": last(id)}
		normalized["arm_etag"] = text(raw["eTag"])
	} else {
		row := monitorRuleKind(kind)
		_, params, err := c.resourceOperation(resourceType{NativeType: kind, ReadOperations: []string{row.prefix + row.get}}, id, "GET")
		if err != nil {
			return contracts.InventoryItem{}, err
		}
		normalized["arm_parameters"] = params
	}
	refs, err := c.monitorReferences(ctx, kind, id, current.data)
	if err != nil {
		return contracts.InventoryItem{}, err
	}
	var networkReferences []string
	recordedReferences := map[string]any{}
	for kind, ids := range refs {
		slices.Sort(ids)
		normalized[referenceKey(kind)] = ids
		recordedReferences[kind] = ids
		networkReferences = append(networkReferences, ids...)
	}
	normalized["_monitor_references"] = recordedReferences
	normalized[monitorReferencesProof] = c.monitorReferencesBinding(id, kind, configuration, text(normalized[monitorGroupProof]), recordedReferences)
	slices.Sort(networkReferences)
	tags := map[string]string{}
	for key, value := range object(safe["tags"]) {
		if value, ok := value.(string); ok {
			tags[key] = value
		}
	}
	location := monitorResourceRegion(kind, raw)
	if err := c.rbacIdentityInventory(id, kind, current.data, normalized); err != nil {
		return contracts.InventoryItem{}, err
	}
	scope := contracts.InventoryScope{Kind: asset.ScopeRegion, NativeID: location, Name: location, Location: location}
	if location == "global" {
		scope = contracts.InventoryScope{Kind: asset.ScopeGlobal, NativeID: c.subscription + "/global", Name: "Global", Location: "global"}
	}
	actionable := known && !mapping.ReadOnly
	return contracts.InventoryItem{NativeID: id, NativeType: kind, ResourceKind: r.resourceKind(kind), Actionable: &actionable, Scope: scope, Name: text(raw["name"]), Location: location, State: text(object(raw["properties"])["provisioningState"]), Tags: tags, Normalized: normalized, Raw: safe, NativeAliases: []string{text(current.data["id"]), id}, NetworkReferences: networkReferences}, nil
}

func (r *Runtime) monitorInventorySnapshot(ctx context.Context, c *client, request contracts.InventoryRequest) ([]contracts.InventoryItem, map[string]any, string, error) {
	groups, err := c.insightsGroups(ctx)
	if err != nil {
		return nil, nil, "", err
	}
	owners, groupBindings := map[string]string{}, map[string]any{}
	for _, id := range slices.Sorted(maps.Keys(groups)) {
		group, err := c.insightsGroup(ctx, id, groups[id])
		if err != nil {
			return nil, nil, "", err
		}
		groups[id], owners[id] = group, text(group["managedBy"])
		groupBindings[id] = insightsWorkspaceResourceSnapshot(group)
	}
	kind := monitorResourceKind(request.ResourceKind.NativeType)
	values, requestID, err := c.monitorResourceIndex(ctx, kind, groups)
	if err != nil {
		return nil, nil, "", err
	}
	locks, err := c.managementLocks(ctx)
	if err != nil {
		return nil, nil, "", err
	}
	lockBindings := map[string]any{}
	for _, value := range locks {
		raw := object(value)
		copy := maps.Clone(raw)
		id := strings.ToLower(text(raw["id"]))
		copy["id"] = id
		lockBindings[id] = copy
	}
	items, bindings := []contracts.InventoryItem{}, map[string]any{}
	for _, id := range slices.Sorted(maps.Keys(values)) {
		item, err := r.monitorInventoryItem(ctx, c, values[id], owners, locks)
		if err != nil {
			return nil, nil, "", err
		}
		_, scope, _, _ := monitorResourceID(id)
		if scope != c.root() && item.Normalized[monitorGroupProof] != c.privateConfiguration(object(groupBindings[scope])) {
			return nil, nil, "", serviceDenied("monitor_resource_group_changed")
		}
		bindings[id] = map[string]any{"configuration": item.Normalized[monitorConfigurationProof], "group": item.Normalized[monitorGroupProof], "references": item.Normalized[monitorReferencesProof], "protection": item.Normalized["cleanup_protection_reason"], "location": item.Location}
		if productScopeMatches(request, item) {
			items = append(items, item)
		}
	}
	return items, map[string]any{"resources": bindings, "groups": groupBindings, "locks": lockBindings}, requestID, nil
}

func (r *Runtime) listMonitorResources(ctx context.Context, c *client, request contracts.InventoryRequest) (batch contracts.InventoryBatch, err error) {
	defer func() { err = contracts.DependencyReadError(err) }()
	if request.Source != productInventorySource || len(request.Options)+len(request.KnownNativeIDs) != 0 || request.ResourceKind == nil || monitorResourceKind(request.ResourceKind.NativeType) == "" {
		return batch, serviceDenied("invalid_monitor_inventory_source")
	}
	switch request.Scope.Kind {
	case asset.ScopeSubscription:
		if !strings.EqualFold(request.Scope.NativeID, c.subscription) {
			return batch, serviceDenied("monitor_inventory_subscription_changed")
		}
	case asset.ScopeRegion:
		if request.Scope.NativeID == "" {
			return batch, serviceDenied("monitor_inventory_region_missing")
		}
	case asset.ScopeGlobal:
		if !strings.EqualFold(request.Scope.NativeID, c.subscription+"/global") && request.Scope.NativeID != "global" {
			return batch, serviceDenied("monitor_inventory_global_scope_changed")
		}
	default:
		return batch, serviceDenied("invalid_monitor_inventory_scope")
	}
	cursor := productCursor{}
	if request.Cursor != "" {
		if len(request.Cursor) > 128<<10 {
			return batch, serviceDenied("monitor_inventory_cursor_too_large")
		}
		data, err := base64.RawURLEncoding.DecodeString(request.Cursor)
		if err != nil || json.Unmarshal(data, &cursor) != nil || cursor.Target < 1 || cursor.Fingerprint == "" || cursor.Next != "" || len(cursor.Seen)+len(cursor.Resources) != 0 {
			return batch, serviceDenied("invalid_monitor_inventory_cursor")
		}
	}
	first, before, provenance, err := r.monitorInventorySnapshot(ctx, c, request)
	if err != nil {
		return batch, err
	}
	_, after, _, err := r.monitorInventorySnapshot(ctx, c, request)
	if err != nil {
		return batch, err
	}
	if c.privateConfiguration(before) != c.privateConfiguration(after) {
		return batch, serviceDenied("monitor_collection_changed_during_scan")
	}
	boundary := request
	boundary.Cursor, boundary.Limit = "", 0
	fingerprint := c.privateConfiguration(map[string]any{"request": boundary, "revision": r.bundle.Revision, "bindings": before})
	if request.Cursor != "" && (cursor.Fingerprint != fingerprint || cursor.Target >= len(first)) {
		return batch, serviceDenied("monitor_inventory_cursor_changed")
	}
	limit := request.Limit
	if limit <= 0 {
		limit = 1000
	}
	end := cursor.Target + min(limit, len(first)-cursor.Target)
	batch = contracts.InventoryBatch{Items: first[cursor.Target:end], Complete: end == len(first), RequestID: provenance}
	if !batch.Complete {
		cursor.Fingerprint, cursor.Target = fingerprint, end
		encoded, _ := json.Marshal(cursor)
		batch.NextCursor = base64.RawURLEncoding.EncodeToString(encoded)
	}
	return batch, nil
}
