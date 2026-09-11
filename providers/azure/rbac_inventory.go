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
	rbacConfigurationProof = "_rbac_configuration"
	rbacContextProof       = "_rbac_context"
	rbacReferencesProof    = "_rbac_reference_binding"
	rbacWireSelector       = "_rbac_wire_id"
)

func rbacResourceKind(kind string) string {
	if kind = rbacKind(kind); kind == rbacRoleType || kind == rbacAssignmentType {
		return kind
	}
	return ""
}

func (c *client) rbacScopes(kind string, raw map[string]any) ([]string, error) {
	if kind == rbacRoleType {
		return rbacStrings(object(raw["properties"])["assignableScopes"], true)
	}
	return []string{text(object(raw["properties"])["scope"])}, nil
}

func (c *client) rbacPIM(ctx context.Context) (map[string]map[string]any, error) {
	result := map[string]map[string]any{}
	for _, kind := range []string{rbacEligibilityType, rbacScheduleType} {
		values, _, err := c.rbacIndex(ctx, kind, c.root())
		if err != nil {
			return nil, err
		}
		for id, raw := range values {
			result[id] = raw
		}
	}
	return result, nil
}

func (c *client) rbacContext(ctx context.Context, kind string, raw map[string]any, locks []any, pim map[string]map[string]any, cache map[string]diagnosticContextState) (map[string]any, string, error) {
	state := map[string]any{}
	scopes, err := c.rbacScopes(kind, raw)
	if err != nil {
		return nil, "", err
	}
	reason := ""
	if kind == rbacRoleType && text(object(raw["properties"])["type"]) == "BuiltInRole" {
		reason = "azure_rbac_builtin_role"
	}
	for _, candidate := range scopes {
		scope, err := rbacScope(candidate)
		if err != nil {
			return nil, "", err
		}
		if !c.rbacLocalScope(scope) {
			state[scope] = map[string]any{"outside_connection": true}
			if reason == "" {
				reason = "azure_rbac_shared_assignable_scope"
			}
			continue
		}
		wire, err := rbacWireScope(candidate)
		if err != nil {
			return nil, "", err
		}
		current, ok := cache[wire]
		if !ok {
			current, err = c.diagnosticContext(ctx, wire)
			if err != nil {
				return nil, "", contracts.DependencyReadError(err)
			}
			if cache != nil {
				cache[wire] = current
			}
		}
		state[scope] = current.state
		if !current.verified {
			reason = "azure_rbac_unverified_scope"
		}
		if protectedAzureTags(object(current.source["tags"])) || protectedAzureTags(object(current.group["tags"])) {
			reason = "azure_protected_tag"
		}
		if locked(scope, locks) {
			reason = "azure_management_lock"
		}
	}
	matching := map[string]any{}
	roleID := text(raw["id"])
	props := object(raw["properties"])
	if kind == rbacAssignmentType {
		roleID = text(props["roleDefinitionId"])
	}
	roleID, err = c.rbacRoleID(roleID)
	if err != nil {
		return nil, "", err
	}
	for id, schedule := range pim {
		p := object(schedule["properties"])
		referenced, err := c.rbacRoleID(text(p["roleDefinitionId"]))
		if err != nil {
			return nil, "", err
		}
		if referenced != roleID {
			continue
		}
		if kind == rbacAssignmentType && (!strings.EqualFold(text(p["principalId"]), text(props["principalId"])) || !strings.EqualFold(text(p["scope"]), text(props["scope"]))) {
			continue
		}
		_, _, scheduleKind, _ := rbacResourceID(id)
		matching[id] = c.rbacSnapshot(scheduleKind, schedule)
	}
	if len(matching) != 0 && reason == "" {
		reason = "azure_rbac_pim_assignment"
	}
	return map[string]any{"scopes": state, "pim": matching}, reason, nil
}

func (c *client) rbacReferenceBinding(id, kind, wire, configuration, context string, refs map[string]any) string {
	return c.privateConfiguration(map[string]any{"id": id, "kind": kind, "wire": wire, "configuration": configuration, "context": context, "references": refs})
}

func (r *Runtime) rbacInventoryItem(ctx context.Context, c *client, kind string, raw map[string]any, locks []any, pim map[string]map[string]any, cache map[string]diagnosticContextState) (contracts.InventoryItem, error) {
	id, err := c.rbacValidate(kind, raw)
	if err != nil {
		return contracts.InventoryItem{}, err
	}
	wire, err := c.rbacWireID(text(raw["id"]))
	if err != nil {
		return contracts.InventoryItem{}, err
	}
	state, reason, err := c.rbacContext(ctx, kind, raw, locks, pim, cache)
	if err != nil {
		return contracts.InventoryItem{}, err
	}
	configuration, context := c.privateConfiguration(c.rbacSnapshot(kind, raw)), c.privateConfiguration(state)
	props := object(raw["properties"])
	normalized := map[string]any{"name": last(id), "subscription_id": c.subscription, "_inventory_source": productInventorySource, rbacConfigurationProof: configuration, rbacContextProof: context, rbacWireSelector: wire}
	for _, field := range []string{"roleName", "type", "assignableScopes", "scope", "roleDefinitionId", "principalId", "principalType", "createdOn", "updatedOn"} {
		if value, ok := props[field]; ok {
			normalized[field] = value
		}
	}
	if kind == rbacRoleType {
		normalized["roleType"] = props["type"]
		delete(normalized, "type")
	}
	if reason != "" {
		normalized["cleanup_protected"], normalized["cleanup_protection_reason"] = true, reason
	}
	refs, err := c.rbacReferences(kind, id, raw)
	if err != nil {
		return contracts.InventoryItem{}, err
	}
	var network []string
	for typ, ids := range refs {
		slices.Sort(ids)
		normalized[referenceKey(typ)] = ids
		network = append(network, ids...)
	}
	recorded := monitorReferenceProjection(refs)
	normalized["_rbac_references"] = recorded
	normalized[rbacReferencesProof] = c.rbacReferenceBinding(id, kind, wire, configuration, context, recorded)
	name := last(id)
	if kind == rbacRoleType {
		name = text(props["roleName"])
	}
	actionable := true
	return contracts.InventoryItem{NativeID: id, NativeType: kind, ResourceKind: r.resourceKind(kind), Actionable: &actionable, Scope: contracts.InventoryScope{Kind: asset.ScopeGlobal, NativeID: c.subscription + "/global", Name: "Global", Location: "global"}, Name: name, Location: "global", State: "available", Normalized: normalized, Raw: safePayload(raw), NativeAliases: []string{text(raw["id"]), id}, NetworkReferences: network}, nil
}

func (r *Runtime) rbacInventorySnapshot(ctx context.Context, c *client, request contracts.InventoryRequest) ([]contracts.InventoryItem, map[string]any, string, error) {
	kind := rbacResourceKind(request.ResourceKind.NativeType)
	rows, requestID, err := c.rbacIndex(ctx, kind, c.root())
	if err != nil {
		return nil, nil, "", err
	}
	locks, err := c.managementLocks(ctx)
	if err != nil {
		return nil, nil, "", err
	}
	pim := map[string]map[string]any{}
	if len(rows) != 0 {
		pim, err = c.rbacPIM(ctx)
		if err != nil {
			return nil, nil, "", err
		}
	}
	cache := map[string]diagnosticContextState{}
	items, bindings := []contracts.InventoryItem{}, map[string]any{}
	for _, id := range slices.Sorted(maps.Keys(rows)) {
		item, err := r.rbacInventoryItem(ctx, c, kind, rows[id], locks, pim, cache)
		if err != nil {
			return nil, nil, "", err
		}
		bindings[id] = map[string]any{"configuration": item.Normalized[rbacConfigurationProof], "context": item.Normalized[rbacContextProof], "references": item.Normalized[rbacReferencesProof], "protection": item.Normalized["cleanup_protection_reason"]}
		if productScopeMatches(request, item) {
			items = append(items, item)
		}
	}
	return items, bindings, requestID, nil
}

func (r *Runtime) listRBAC(ctx context.Context, c *client, request contracts.InventoryRequest) (batch contracts.InventoryBatch, err error) {
	defer func() { err = contracts.DependencyReadError(err) }()
	if request.Source != productInventorySource || request.ResourceKind == nil || rbacResourceKind(request.ResourceKind.NativeType) == "" || len(request.Options)+len(request.KnownNativeIDs)+len(request.KnownNativeMetadata) != 0 {
		return batch, serviceDenied("invalid_rbac_inventory_source")
	}
	switch request.Scope.Kind {
	case asset.ScopeSubscription:
		if !strings.EqualFold(request.Scope.NativeID, c.subscription) {
			return batch, serviceDenied("rbac_inventory_subscription_changed")
		}
	case asset.ScopeGlobal:
		if request.Scope.NativeID != "global" && !strings.EqualFold(request.Scope.NativeID, c.subscription+"/global") {
			return batch, serviceDenied("rbac_inventory_global_scope_changed")
		}
	case asset.ScopeRegion:
		if request.Scope.NativeID == "" {
			return batch, serviceDenied("rbac_inventory_region_missing")
		}
	default:
		return batch, serviceDenied("invalid_rbac_inventory_scope")
	}
	cursor := productCursor{}
	if request.Cursor != "" {
		if len(request.Cursor) > 128<<10 {
			return batch, serviceDenied("rbac_inventory_cursor_too_large")
		}
		data, err := base64.RawURLEncoding.DecodeString(request.Cursor)
		if err != nil || json.Unmarshal(data, &cursor) != nil || cursor.Target < 1 || cursor.Fingerprint == "" || cursor.Next != "" || len(cursor.Seen)+len(cursor.Resources) != 0 {
			return batch, serviceDenied("invalid_rbac_inventory_cursor")
		}
	}
	items, before, provenance, err := r.rbacInventorySnapshot(ctx, c, request)
	if err != nil {
		return batch, err
	}
	_, after, _, err := r.rbacInventorySnapshot(ctx, c, request)
	if err != nil {
		return batch, err
	}
	if c.privateConfiguration(before) != c.privateConfiguration(after) {
		return batch, serviceDenied("rbac_collection_changed_during_scan")
	}
	boundary := request
	boundary.Cursor, boundary.Limit = "", 0
	fingerprint := c.privateConfiguration(map[string]any{"request": boundary, "revision": r.bundle.Revision, "bindings": before})
	if request.Cursor != "" && (cursor.Fingerprint != fingerprint || cursor.Target >= len(items)) {
		return batch, serviceDenied("rbac_inventory_cursor_changed")
	}
	limit := request.Limit
	if limit <= 0 {
		limit = 1000
	}
	end := cursor.Target + min(limit, len(items)-cursor.Target)
	batch = contracts.InventoryBatch{Items: items[cursor.Target:end], Complete: end == len(items), RequestID: provenance}
	if !batch.Complete {
		cursor.Fingerprint, cursor.Target = fingerprint, end
		data, _ := json.Marshal(cursor)
		batch.NextCursor = base64.RawURLEncoding.EncodeToString(data)
	}
	return batch, nil
}
