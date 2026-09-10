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

const diagnosticInventorySource = "monitor-diagnostic-settings"
const diagnosticConfigurationProof = "_diagnostic_settings_configuration"
const diagnosticContextProof = "_diagnostic_settings_context"
const diagnosticReferencesProof = "_diagnostic_settings_references_binding"
const diagnosticWireSelector = "_diagnostic_settings_wire_id"
const diagnosticWireProof = "_diagnostic_settings_wire_binding"

type diagnosticContextState struct {
	state         map[string]any
	source, group map[string]any
	verified      bool
}

func diagnosticSourceStamp(raw map[string]any) map[string]any {
	id, kind, _ := parseID(text(raw["id"]))
	wire, _ := diagnosticSourceWire(text(raw["id"]))
	return map[string]any{"id": id, "wire_id": wire, "type": kind, "generation": productGeneration(raw), "creation": creationGeneration(raw), "location": resourceRegion(raw), "tags": raw["tags"], "managedBy": raw["managedBy"]}
}

func (c *client) diagnosticContext(ctx context.Context, scope string) (diagnosticContextState, error) {
	canonical, err := diagnosticScope(scope)
	wire, wireErr := diagnosticSourceWire(scope)
	if err != nil || wireErr != nil || wire != scope || canonical != c.root() && !strings.HasPrefix(canonical, c.root()+"/") {
		return diagnosticContextState{}, serviceDenied("invalid_diagnostic_context_scope")
	}
	result := diagnosticContextState{state: map[string]any{"scope": canonical, "source_wire_id": wire}, source: map[string]any{}, group: map[string]any{}, verified: true}
	if scope == c.root() {
		subscription, err := c.subscriptionIdentity(ctx)
		if err != nil {
			return result, err
		}
		result.state["subscription"] = map[string]any{"id": c.root(), "tenantId": strings.ToLower(text(subscription["tenantId"])), "state": subscription["state"]}
		return result, nil
	}
	id, kind, _ := parseID(scope)
	parts := strings.Split(id, "/")
	// Service scopes share their native storage account's incarnation. The
	// diagnostic-setting GET establishes the exact existing extension scope.
	if len(parts) == 11 && parts[10] == "default" && slices.Contains([]string{"blobservices", "fileservices", "queueservices", "tableservices"}, parts[9]) && strings.HasPrefix(kind, "microsoft.storage/storageaccounts/") {
		id, kind = strings.Join(parts[:9], "/"), storageType
		wire = id
	}
	if mapping, known := findType(kind); known {
		endpoint, err := c.resourceURL(mapping, wire)
		if err != nil {
			return result, err
		}
		current, err := c.request(ctx, "GET", endpoint)
		if isNotFound(err) {
			result.state["source"] = map[string]any{"id": id, "absent": true}
		} else if err != nil {
			return result, err
		} else {
			if !insightsARMReadValid(current, id, kind) || diagnosticSourceMetadata(current.data) != nil {
				return result, serviceDenied("invalid_diagnostic_native_source")
			}
			currentWire, err := diagnosticSourceWire(responseID(mapping.NativeType, text(current.data["id"])))
			if err != nil || wire != currentWire {
				return result, serviceDenied("diagnostic_native_source_name_changed")
			}
			result.source = maps.Clone(current.data)
			result.source["id"] = responseID(mapping.NativeType, text(current.data["id"]))
			result.state["source"] = diagnosticSourceStamp(result.source)
		}
	} else {
		// Enumerate settings on unregistered source kinds, but do not authorize
		// deletion without a native source-incarnation reader. ARM-index absence
		// cannot identify an unknown or omitted child resource as deleted.
		result.verified = false
		result.state["source"] = map[string]any{"id": id, "unverified": true}
	}
	groupID := strings.Join(parts[:5], "/")
	group, err := c.request(ctx, "GET", apiURL(groupID, resourcesVersion))
	if isNotFound(err) {
		result.state["group"] = map[string]any{"id": groupID, "absent": true}
		return result, nil
	}
	if err != nil {
		return result, err
	}
	if !insightsARMReadValid(group, groupID, groupType) || diagnosticSourceMetadata(group.data) != nil {
		return result, serviceDenied("invalid_diagnostic_source_group")
	}
	if _, err := insightsManagedBy(group.data); err != nil {
		return result, err
	}
	result.group, result.state["group"] = group.data, insightsWorkspaceResourceSnapshot(group.data)
	return result, nil
}

func (c *client) diagnosticReferenceBinding(id, configuration, context string, refs map[string]any) string {
	return c.privateConfiguration(map[string]any{"id": id, "kind": diagnosticSettingsType, "configuration": configuration, "context": context, "references": refs})
}

func (c *client) diagnosticWireBinding(id, wire, configuration, context string) string {
	return c.privateConfiguration(map[string]any{"id": id, "wire_id": wire, "configuration": configuration, "context": context})
}

func (c *client) diagnosticPlannedWire(id string, normalized map[string]any) (string, error) {
	wire := text(normalized[diagnosticWireSelector])
	canonical, _, kind, err := diagnosticResourceID(wire)
	selector, wireErr := diagnosticWireID(wire)
	configuration, context := text(normalized[diagnosticConfigurationProof]), text(normalized[diagnosticContextProof])
	if err != nil || wireErr != nil || selector != wire || canonical != id || kind != diagnosticSettingsType || !strings.HasPrefix(id, c.root()+"/") || configuration == "" || context == "" || text(normalized[diagnosticWireProof]) != c.diagnosticWireBinding(id, wire, configuration, context) {
		return "", serviceDenied("diagnostic_native_selector_changed")
	}
	return wire, nil
}

func (r *Runtime) diagnosticInventoryItem(ctx context.Context, c *client, raw map[string]any, locks []any) (contracts.InventoryItem, error) {
	id, scope, kind, err := diagnosticResourceID(text(raw["id"]))
	if err != nil || kind != diagnosticSettingsType || !strings.HasPrefix(id, c.root()+"/") || diagnosticIdentity(raw, id, kind) != nil {
		return contracts.InventoryItem{}, serviceDenied("invalid_diagnostic_inventory_identity")
	}
	wire, err := diagnosticWireID(text(raw["id"]))
	if err != nil {
		return contracts.InventoryItem{}, err
	}
	state, err := c.diagnosticContext(ctx, diagnosticWireScope(wire))
	if err != nil {
		return contracts.InventoryItem{}, contracts.DependencyReadError(err)
	}
	configuration := c.privateConfiguration(diagnosticSnapshot(raw))
	context := c.privateConfiguration(state.state)
	refs, err := diagnosticReferences(id, raw)
	if err != nil {
		return contracts.InventoryItem{}, err
	}
	recorded, network := map[string]any{}, []string{}
	normalized := map[string]any{"name": last(id), "subscription_id": c.subscription, "diagnostic_source_id": scope, "_inventory_source": diagnosticInventorySource, diagnosticConfigurationProof: configuration, diagnosticContextProof: context}
	normalized[diagnosticWireSelector], normalized[diagnosticWireProof] = wire, c.diagnosticWireBinding(id, wire, configuration, context)
	for typ, ids := range refs {
		slices.Sort(ids)
		recorded[typ], normalized[referenceKey(typ)] = ids, ids
		network = append(network, ids...)
	}
	slices.Sort(network)
	normalized["_diagnostic_references"] = recorded
	normalized[diagnosticReferencesProof] = c.diagnosticReferenceBinding(id, configuration, context, recorded)
	reason := ""
	if !state.verified {
		reason = "diagnostic_source_identity_unverified"
	}
	if protectedAzureTags(object(raw["tags"])) || protectedAzureTags(object(state.source["tags"])) || protectedAzureTags(object(state.group["tags"])) {
		reason = "azure_protected_tag"
	}
	if locked(id, locks) {
		reason = "azure_management_lock"
	}
	if reason != "" {
		normalized["cleanup_protected"], normalized["cleanup_protection_reason"] = true, reason
	}
	// No authored or future properties leave the private response. The public
	// reference projection is separately authenticated, including after deletion.
	safe := safePayload(map[string]any{"id": id, "name": text(raw["name"]), "type": kind, "tags": raw["tags"]})
	normalized["tags"] = safe["tags"]
	tags := map[string]string{}
	for key, value := range object(safe["tags"]) {
		tags[key] = text(value)
	}
	actionable := true
	return contracts.InventoryItem{NativeID: id, NativeType: kind, ResourceKind: r.resourceKind(kind), Actionable: &actionable, Scope: contracts.InventoryScope{Kind: asset.ScopeGlobal, NativeID: c.subscription + "/global", Name: "Global", Location: "global"}, Location: "global", Name: text(raw["name"]), Tags: tags, Normalized: normalized, Raw: safe, NativeAliases: []string{id, text(raw["id"])}, NetworkReferences: network}, nil
}

func (r *Runtime) diagnosticInventorySnapshot(ctx context.Context, c *client, request contracts.InventoryRequest) ([]contracts.InventoryItem, map[string]any, string, error) {
	known := slices.Clone(request.KnownNativeIDs)
	for id := range request.KnownNativeMetadata {
		if !slices.Contains(known, id) {
			return nil, nil, "", serviceDenied("unrelated_diagnostic_known_metadata")
		}
	}
	for index, id := range known {
		if normalized, exists := request.KnownNativeMetadata[id]; exists {
			wire, err := c.diagnosticPlannedWire(id, normalized)
			if err != nil {
				return nil, nil, "", err
			}
			known[index] = wire
		} else if _, scope, _, err := diagnosticResourceID(id); err == nil {
			if _, typ, err := parseID(scope); err == nil && isCosmosType(typ) && len(strings.Split(scope, "/")) > 9 {
				return nil, nil, "", serviceDenied("diagnostic_known_native_selector_missing")
			}
		}
	}
	settings, sources, requestID, err := c.diagnosticCollection(ctx, known, nil)
	if err != nil {
		return nil, nil, "", err
	}
	locks, err := c.managementLocks(ctx)
	if err != nil {
		return nil, nil, "", err
	}
	items, bindings, sourceBindings := []contracts.InventoryItem{}, map[string]any{}, map[string]any{}
	for id, raw := range sources {
		sourceBindings[id] = diagnosticSourceStamp(raw)
	}
	for _, id := range slices.Sorted(maps.Keys(settings)) {
		item, err := r.diagnosticInventoryItem(ctx, c, settings[id], locks)
		if err != nil {
			return nil, nil, "", err
		}
		bindings[id] = map[string]any{"configuration": item.Normalized[diagnosticConfigurationProof], "context": item.Normalized[diagnosticContextProof], "references": item.Normalized[diagnosticReferencesProof], "protection": item.Normalized["cleanup_protection_reason"]}
		if productScopeMatches(request, item) {
			items = append(items, item)
		}
	}
	return items, map[string]any{"settings": bindings, "sources": sourceBindings, "locks": locks}, requestID, nil
}

func (r *Runtime) listDiagnosticSettings(ctx context.Context, c *client, request contracts.InventoryRequest) (batch contracts.InventoryBatch, err error) {
	defer func() { err = contracts.DependencyReadError(err) }()
	if request.Source != diagnosticInventorySource || request.ResourceKind == nil || request.ResourceKind.NativeType != diagnosticSettingsType || len(request.Options) != 0 {
		return batch, serviceDenied("invalid_diagnostic_inventory_source")
	}
	switch request.Scope.Kind {
	case asset.ScopeSubscription:
		if !strings.EqualFold(request.Scope.NativeID, c.subscription) {
			return batch, serviceDenied("diagnostic_inventory_subscription_changed")
		}
	case asset.ScopeGlobal:
		if request.Scope.NativeID != "global" && !strings.EqualFold(request.Scope.NativeID, c.subscription+"/global") {
			return batch, serviceDenied("diagnostic_inventory_global_scope_changed")
		}
	case asset.ScopeRegion:
		if request.Scope.NativeID == "" {
			return batch, serviceDenied("diagnostic_inventory_region_missing")
		}
	default:
		return batch, serviceDenied("invalid_diagnostic_inventory_scope")
	}
	cursor := productCursor{}
	if request.Cursor != "" {
		data, err := base64.RawURLEncoding.DecodeString(request.Cursor)
		if len(request.Cursor) > 128<<10 || err != nil || json.Unmarshal(data, &cursor) != nil || cursor.Target < 1 || cursor.Fingerprint == "" || cursor.Next != "" || len(cursor.Seen)+len(cursor.Resources) != 0 {
			return batch, serviceDenied("invalid_diagnostic_inventory_cursor")
		}
	}
	first, before, provenance, err := r.diagnosticInventorySnapshot(ctx, c, request)
	if err != nil {
		return batch, err
	}
	_, after, _, err := r.diagnosticInventorySnapshot(ctx, c, request)
	if err != nil {
		return batch, err
	}
	if c.privateConfiguration(before) != c.privateConfiguration(after) {
		return batch, serviceDenied("diagnostic_collection_changed_during_scan")
	}
	boundary := request
	boundary.Cursor, boundary.Limit = "", 0
	fingerprint := c.privateConfiguration(map[string]any{"request": boundary, "revision": r.bundle.Revision, "bindings": before})
	if request.Cursor != "" && (cursor.Fingerprint != fingerprint || cursor.Target >= len(first)) {
		return batch, serviceDenied("diagnostic_inventory_cursor_changed")
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
