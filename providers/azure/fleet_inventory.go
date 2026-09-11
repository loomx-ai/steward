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

const (
	fleetInventorySource    = "kubernetes-fleet"
	fleetConfigurationProof = "_fleet_configuration"
	fleetContextProof       = "_fleet_context"
	fleetReferencesProof    = "_fleet_reference_binding"
)

func fleetPath(path string) bool {
	const provider = "/providers/microsoft.containerservice/"
	path = strings.ToLower(path)
	index := strings.LastIndex(path, provider)
	return index >= 0 && armPathProvider(path) == "microsoft.containerservice" && strings.Split(path[index+len(provider):], "/")[0] == "fleets"
}

// Namespace annotations, placement expressions and future authored settings
// may contain private values. Retain them only in the configuration digest.
func fleetSafeValue(value any) any {
	switch value := value.(type) {
	case map[string]any:
		result := map[string]any{}
		for _, key := range []string{"id", "name", "type", "location", "tags", "request_id", "status_code"} {
			if entry, ok := value[key]; ok {
				result[key] = entry
			}
		}
		if props, ok := value["properties"].(map[string]any); ok {
			public := map[string]any{}
			for _, key := range []string{"provisioningState", "clusterResourceId", "group", "deletePolicy", "adoptionPolicy", "updateStrategyId", "autoUpgradeProfileId", "upgradeChannel", "disabled", "gateType", "state"} {
				switch entry := props[key].(type) {
				case string, bool:
					public[key] = entry
				}
			}
			result["properties"] = public
		}
		for _, key := range []string{"body", "value"} {
			if entry, ok := value[key]; ok {
				result[key] = fleetSafeValue(entry)
			}
		}
		return result
	case []any:
		result := make([]any, len(value))
		for i, entry := range value {
			result[i] = fleetSafeValue(entry)
		}
		return result
	default:
		return nil
	}
}

func (c *client) fleetReferenceBinding(id, kind, location, configuration, context string, refs map[string]any) string {
	return c.privateConfiguration(map[string]any{"id": id, "kind": kind, "location": location, "configuration": configuration, "context": context, "references": refs})
}

func (r *Runtime) fleetObservedInventoryItem(ctx context.Context, c *client, raw map[string]any, owners map[string]string, locks []any) (contracts.InventoryItem, error) {
	id, kind, err := fleetIdentity(text(raw["id"]))
	if err != nil {
		return contracts.InventoryItem{}, err
	}
	current, err := c.fleetRead(ctx, kind, id)
	if err != nil {
		return contracts.InventoryItem{}, contracts.DependencyReadError(err)
	}
	listed := fleetSnapshot(kind, raw)
	if kind != fleetType && kind != fleetNamespaceType {
		// The generic ARM child walker adds its parent's location to proxies.
		delete(listed, "location")
	}
	if !nativeConfigurationContains(listed, fleetSnapshot(kind, current.data)) {
		return contracts.InventoryItem{}, serviceDenied("fleet_observed_configuration_changed")
	}
	groupID := strings.Join(strings.Split(id, "/")[:5], "/")
	owner, ok := owners[groupID]
	if !ok {
		return contracts.InventoryItem{}, serviceDenied("fleet_group_missing_from_index")
	}
	group, err := c.workbookGroup(ctx, id)
	if err != nil {
		return contracts.InventoryItem{}, contracts.DependencyReadError(err)
	}
	if !strings.EqualFold(owner, text(group["managedBy"])) {
		return contracts.InventoryItem{}, serviceDenied("fleet_group_owner_changed")
	}
	var parent map[string]any
	if parentID := fleetParent(id, kind); parentID != "" {
		result, err := c.fleetRead(ctx, fleetType, parentID)
		if err != nil {
			return contracts.InventoryItem{}, contracts.DependencyReadError(err)
		}
		parent = result.data
	}
	return r.fleetInventoryItem(c, kind, current.data, parent, group, locks)
}

// Uses describe current dependencies. A run keeps its own copied strategy and
// its creating profile's ID is provenance, not a current configuration link.
// https://learn.microsoft.com/azure/kubernetes-fleet/faq
func fleetCurrentReferences(kind string, raw map[string]any) (map[string][]string, error) {
	refs, err := fleetReferences(kind, raw)
	if err != nil {
		return nil, err
	}
	if kind == fleetRunType {
		delete(refs, fleetStrategyType)
		delete(refs, fleetProfileType)
	}
	if parent := fleetParent(text(raw["id"]), kind); parent != "" {
		addReference(refs, fleetType, parent)
	}
	return refs, nil
}

func (r *Runtime) fleetInventoryItem(c *client, kind string, raw, parent, group map[string]any, locks []any) (contracts.InventoryItem, error) {
	if err := fleetValidate(kind, raw); err != nil {
		return contracts.InventoryItem{}, err
	}
	id, _, _ := fleetIdentity(text(raw["id"]))
	if !strings.HasPrefix(id, c.root()+"/") {
		return contracts.InventoryItem{}, serviceDenied("fleet_inventory_subscription_changed")
	}
	context := map[string]any{"group": insightsWorkspaceResourceSnapshot(group)}
	location := resourceRegion(raw)
	if kind != fleetType {
		if fleetValidate(fleetType, parent) != nil || !strings.EqualFold(text(parent["id"]), fleetParent(id, kind)) {
			return contracts.InventoryItem{}, serviceDenied("fleet_inventory_parent_changed")
		}
		context["parent"] = fleetSnapshot(fleetType, parent)
		if kind != fleetNamespaceType {
			location = resourceRegion(parent)
		}
	}
	safe := safePayload(object(fleetSafeValue(raw)))
	normalized := maps.Clone(object(safe["properties"]))
	normalized["name"], normalized["subscription_id"], normalized["resource_group"] = last(id), c.subscription, strings.Split(id, "/")[4]
	normalized["tags"], normalized["_inventory_source"] = safe["tags"], fleetInventorySource
	row := fleetKind(kind)
	_, parameters, err := c.resourceOperation(resourceType{NativeType: kind, ReadOperations: []string{"Azure.Microsoft.ContainerService." + row.prefix + "_Get"}}, id, "GET")
	if err != nil {
		return contracts.InventoryItem{}, err
	}
	normalized["arm_parameters"] = parameters
	normalized[fleetConfigurationProof] = c.privateConfiguration(fleetSnapshot(kind, raw))
	normalized[fleetContextProof] = c.privateConfiguration(context)
	// Registration remains inventory-only until the native lifecycle driver is
	// available. A group deletion must not bypass that missing review boundary.
	reason := "azure_fleet_lifecycle_pending"
	if kind == fleetGateType {
		reason = "azure_fleet_gate_requires_update_run"
	}
	if kind == fleetNamespaceType {
		names, dynamic, err := fleetNamespaceMembers(raw)
		if err != nil {
			return contracts.InventoryItem{}, err
		}
		normalized["placement_dynamic"], normalized["placement_member_names"] = dynamic, names
	}
	if text(group["managedBy"]) != "" {
		reason = "azure_managed_resource_group"
	}
	if protectedAzureTags(object(raw["tags"])) || protectedAzureTags(object(parent["tags"])) || protectedAzureTags(object(group["tags"])) {
		reason = "azure_protected_tag"
	}
	if locked(id, locks) {
		reason = "azure_management_lock"
	}
	normalized["cleanup_protected"], normalized["cleanup_protection_reason"] = true, reason
	refs, err := fleetCurrentReferences(kind, raw)
	if err != nil {
		return contracts.InventoryItem{}, err
	}
	var network []string
	for typ, ids := range refs {
		slices.Sort(ids)
		normalized[referenceKey(typ)] = ids
		network = append(network, ids...)
	}
	slices.Sort(network)
	recorded := monitorReferenceProjection(refs)
	normalized["_fleet_references"] = recorded
	normalized[fleetReferencesProof] = c.fleetReferenceBinding(id, kind, location, text(normalized[fleetConfigurationProof]), text(normalized[fleetContextProof]), recorded)
	if err := c.rbacIdentityInventory(id, kind, raw, normalized); err != nil {
		return contracts.InventoryItem{}, err
	}
	tags := map[string]string{}
	for key, value := range object(safe["tags"]) {
		if s, ok := value.(string); ok {
			tags[key] = s
		}
	}
	scope := contracts.InventoryScope{Kind: asset.ScopeRegion, NativeID: location, Name: location, Location: location}
	if location == "global" {
		scope = contracts.InventoryScope{Kind: asset.ScopeGlobal, NativeID: c.subscription + "/global", Name: "Global", Location: location}
	}
	actionable := false
	return contracts.InventoryItem{NativeID: id, NativeType: kind, ResourceKind: r.resourceKind(kind), Actionable: &actionable, Scope: scope, Name: last(id), State: text(object(raw["properties"])["provisioningState"]), Location: location, Tags: tags, Normalized: normalized, Raw: safe, NativeAliases: []string{text(raw["id"]), id}, NetworkReferences: network}, nil
}

func (r *Runtime) fleetInventorySnapshot(ctx context.Context, c *client, request contracts.InventoryRequest) ([]contracts.InventoryItem, []string, map[string]any, string, error) {
	kind := fleetKind(request.ResourceKind.NativeType).kind
	roots, provenance, err := c.fleetIndex(ctx, fleetType, c.root())
	if err != nil {
		return nil, nil, nil, "", err
	}
	// Recover parents omitted from LIST through the saved child's native ID.
	// A parent's 404 is never substituted for a child's own absence response.
	missingParents := map[string]bool{}
	for _, id := range request.KnownNativeIDs {
		parentID := fleetParent(id, kind)
		if parentID == "" || roots[parentID] != nil || missingParents[parentID] {
			continue
		}
		parent, err := c.fleetRead(ctx, fleetType, parentID)
		if isNotFound(err) {
			missingParents[parentID] = true
			continue
		}
		if err != nil {
			return nil, nil, nil, "", err
		}
		roots[parentID] = parent.data
	}
	rows := roots
	if kind != fleetType {
		rows = map[string]map[string]any{}
		for _, parent := range slices.Sorted(maps.Keys(roots)) {
			children, requestID, err := c.fleetIndex(ctx, kind, parent)
			if err != nil {
				return nil, nil, nil, "", err
			}
			maps.Copy(rows, children)
			if requestID != "" {
				provenance = requestID
			}
		}
	}
	var absent []string
	for _, id := range request.KnownNativeIDs {
		if rows[id] != nil {
			continue
		}
		current, err := c.fleetRead(ctx, kind, id)
		if isNotFound(err) {
			absent = append(absent, id)
			continue
		}
		if err != nil {
			return nil, nil, nil, "", err
		}
		if kind != fleetType && roots[fleetParent(id, kind)] == nil {
			return nil, nil, nil, "", serviceDenied("fleet_live_child_has_missing_parent")
		}
		rows[id] = current.data
	}
	slices.Sort(absent)
	groups, err := c.insightsGroups(ctx)
	if err != nil {
		return nil, nil, nil, "", err
	}
	locks, err := c.managementLocks(ctx)
	if err != nil {
		return nil, nil, nil, "", err
	}
	bindings := map[string]any{"roots": map[string]any{}, "items": map[string]any{}}
	for id, raw := range roots {
		object(bindings["roots"])[id] = c.privateConfiguration(fleetSnapshot(fleetType, raw))
	}
	verifiedGroups := map[string]map[string]any{}
	items := []contracts.InventoryItem{}
	for _, id := range slices.Sorted(maps.Keys(rows)) {
		groupID := strings.Join(strings.Split(id, "/")[:5], "/")
		if groups[groupID] == nil {
			return nil, nil, nil, "", serviceDenied("fleet_group_missing_from_index")
		}
		group := verifiedGroups[groupID]
		if group == nil {
			group, err = c.insightsGroup(ctx, groupID, groups[groupID])
			if err != nil {
				return nil, nil, nil, "", err
			}
			verifiedGroups[groupID] = group
		}
		item, err := r.fleetInventoryItem(c, kind, rows[id], roots[fleetParent(id, kind)], group, locks)
		if err != nil {
			return nil, nil, nil, "", err
		}
		object(bindings["items"])[id] = map[string]any{"configuration": item.Normalized[fleetConfigurationProof], "context": item.Normalized[fleetContextProof], "references": item.Normalized[fleetReferencesProof], "protection": item.Normalized["cleanup_protection_reason"]}
		if productScopeMatches(request, item) {
			items = append(items, item)
		}
	}
	return items, absent, bindings, provenance, nil
}

func (r *Runtime) listFleet(ctx context.Context, c *client, request contracts.InventoryRequest) (batch contracts.InventoryBatch, err error) {
	defer func() { err = contracts.DependencyReadError(err) }()
	if request.Source != fleetInventorySource || request.ResourceKind == nil || fleetKind(request.ResourceKind.NativeType).kind == "" || len(request.Options) != 0 {
		return batch, serviceDenied("invalid_fleet_inventory_source")
	}
	seen := map[string]bool{}
	for _, id := range request.KnownNativeIDs {
		canonical, typ, err := fleetIdentity(id)
		if err != nil || canonical != id || typ != request.ResourceKind.NativeType || !strings.HasPrefix(id, c.root()+"/") || seen[id] {
			return batch, serviceDenied("invalid_known_fleet_identity")
		}
		seen[id] = true
	}
	for id := range request.KnownNativeMetadata {
		if !seen[id] {
			return batch, serviceDenied("unrelated_fleet_known_metadata")
		}
	}
	switch request.Scope.Kind {
	case asset.ScopeSubscription:
		if !strings.EqualFold(request.Scope.NativeID, c.subscription) {
			return batch, serviceDenied("fleet_inventory_subscription_changed")
		}
	case asset.ScopeRegion:
		if request.Scope.NativeID == "" {
			return batch, serviceDenied("fleet_inventory_region_missing")
		}
	case asset.ScopeGlobal:
		if request.Scope.NativeID != "global" && !strings.EqualFold(request.Scope.NativeID, c.subscription+"/global") {
			return batch, serviceDenied("fleet_inventory_global_scope_changed")
		}
	default:
		return batch, serviceDenied("invalid_fleet_inventory_scope")
	}
	cursor := productCursor{}
	if request.Cursor != "" {
		if len(request.Cursor) > 128<<10 {
			return batch, serviceDenied("fleet_inventory_cursor_too_large")
		}
		data, err := base64.RawURLEncoding.DecodeString(request.Cursor)
		if err != nil || json.Unmarshal(data, &cursor) != nil || cursor.Target < 1 || cursor.Fingerprint == "" || cursor.Next != "" || len(cursor.Seen)+len(cursor.Resources) != 0 {
			return batch, serviceDenied("invalid_fleet_inventory_cursor")
		}
	}
	items, absent, before, provenance, err := r.fleetInventorySnapshot(ctx, c, request)
	if err != nil {
		return batch, err
	}
	_, afterAbsent, after, _, err := r.fleetInventorySnapshot(ctx, c, request)
	if err != nil {
		return batch, err
	}
	if !slices.Equal(absent, afterAbsent) || c.privateConfiguration(before) != c.privateConfiguration(after) {
		return batch, serviceDenied("fleet_collection_changed_during_scan")
	}
	boundary := request
	boundary.Cursor, boundary.Limit = "", 0
	fingerprint := c.privateConfiguration(map[string]any{"request": boundary, "revision": r.bundle.Revision, "bindings": before, "absent": absent})
	if request.Cursor != "" && (cursor.Fingerprint != fingerprint || cursor.Target >= len(items)) {
		return batch, serviceDenied("fleet_inventory_cursor_changed")
	}
	limit := request.Limit
	if limit <= 0 {
		limit = 1000
	}
	end := cursor.Target + min(limit, len(items)-cursor.Target)
	batch = contracts.InventoryBatch{Items: items[cursor.Target:end], Complete: end == len(items), RequestID: provenance}
	if batch.Complete {
		batch.AbsentNativeIDs = absent
	} else {
		cursor.Fingerprint, cursor.Target = fingerprint, end
		data, _ := json.Marshal(cursor)
		batch.NextCursor = base64.RawURLEncoding.EncodeToString(data)
	}
	return batch, nil
}
