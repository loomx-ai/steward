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

func (r *Runtime) hybridComputeSnapshot(ctx context.Context, c *client, request contracts.InventoryRequest) ([]contracts.InventoryItem, map[string]any, string, error) {
	kind := hybridComputeKind(request.ResourceKind.NativeType)
	values, parents := map[string]map[string]any{}, map[string]map[string]any{}
	known := map[string]bool{}
	provenance := ""
	read := func(id, typ string) (map[string]any, error) {
		res, err := c.hybridComputeRead(ctx, id, typ)
		if res.requestID != "" {
			provenance = res.requestID
		}
		return res.data, err
	}
	for _, id := range request.KnownNativeIDs {
		canonical, err := c.hybridComputeIdentity(id, kind)
		if err != nil || id != canonical || known[id] {
			return nil, nil, "", serviceDenied("invalid_hybrid_compute_known_identity")
		}
		known[id] = true
		raw, err := read(id, kind)
		if isNotFound(err) {
			continue
		}
		if err != nil {
			return nil, nil, "", err
		}
		values[id] = raw
		if parent := hybridComputeParent(id, kind); parent != "" && parents[parent] == nil {
			parents[parent], err = read(parent, hybridMachineType)
			if err != nil {
				return nil, nil, "", err
			}
		}
	}
	for id := range request.KnownNativeMetadata {
		if !known[id] {
			return nil, nil, "", serviceDenied("unrelated_hybrid_compute_known_metadata")
		}
	}
	collect := func(typ, parent string, target map[string]map[string]any) error {
		rows, err := c.hybridComputeIndex(ctx, typ, parent)
		if err != nil {
			return err
		}
		seen := map[string]bool{}
		for _, row := range rows {
			raw := object(row)
			id, err := c.hybridComputeIdentity(text(raw["id"]), typ)
			if err != nil || seen[id] || hybridComputeParent(id, typ) != parent || !strings.EqualFold(text(raw["type"]), typ) {
				return serviceDenied("invalid_hybrid_compute_index_member")
			}
			seen[id] = true
			live, err := read(id, typ)
			if err != nil {
				return err
			}
			if serviceListedIncarnation(raw, live) != nil {
				return serviceDenied("hybrid_compute_index_incarnation_changed")
			}
			if prior := target[id]; prior != nil && c.privateConfiguration(prior) != c.privateConfiguration(live) {
				return serviceDenied("hybrid_compute_known_resource_changed")
			}
			target[id] = live
		}
		return nil
	}
	if kind == hybridMachineType || kind == hybridLicenseType {
		if err := collect(kind, "", values); err != nil {
			return nil, nil, "", err
		}
	} else {
		if err := collect(hybridMachineType, "", parents); err != nil {
			return nil, nil, "", err
		}
		for _, parent := range slices.Sorted(maps.Keys(parents)) {
			if err := collect(kind, parent, values); err != nil {
				return nil, nil, "", err
			}
		}
	}
	items, bindings := []contracts.InventoryItem{}, map[string]any{}
	for _, id := range slices.Sorted(maps.Keys(values)) {
		raw := values[id]
		parent := hybridComputeParent(id, kind)
		if parent != "" && (parents[parent] == nil || resourceRegion(parents[parent]) != resourceRegion(raw)) {
			return nil, nil, "", serviceDenied("hybrid_compute_child_location_changed")
		}
		refs, err := hybridComputeReferences(id, kind, raw)
		if err != nil {
			return nil, nil, "", err
		}
		safe := safePayload(object(hybridComputeSafeValue(raw)))
		normalized := maps.Clone(object(safe["properties"]))
		normalized["name"], normalized["tags"], normalized["kind"], normalized["managedBy"] = safe["name"], safe["tags"], safe["kind"], safe["managedBy"]
		normalized["state"] = object(safe["properties"])["provisioningState"]
		normalized["subscription_id"], normalized["resource_group"] = c.subscription, strings.Split(id, "/")[4]
		normalized["_inventory_source"] = hybridComputeSource
		configuration := c.privateConfiguration(map[string]any{"resource": raw, "parent": parents[parent]})
		bindings[id] = configuration
		normalized["_hybrid_compute_configuration"] = configuration
		recorded, network := map[string]any{}, []string{}
		for typ, ids := range refs {
			recorded[typ], normalized[referenceKey(typ)] = ids, ids
			network = append(network, ids...)
		}
		slices.Sort(network)
		normalized["_hybrid_compute_references"] = recorded
		normalized["_hybrid_compute_reference_binding"] = c.privateConfiguration(map[string]any{"id": id, "connection": request.ConnectionID, "configuration": configuration, "references": refs})
		location, actionable := resourceRegion(raw), false // Cleanup requires the native Arc lifecycle driver.
		item := contracts.InventoryItem{NativeID: id, NativeType: kind, ResourceKind: r.resourceKind(kind), Name: text(raw["name"]), Location: location, Scope: contracts.InventoryScope{Kind: asset.ScopeRegion, NativeID: location, Name: location, Location: location}, Raw: safe, Normalized: normalized, Actionable: &actionable, NativeAliases: []string{id, text(raw["id"])}, NetworkReferences: slices.Compact(network)}
		if productScopeMatches(request, item) {
			items = append(items, item)
		}
	}
	// Include even empty parents so their replacement or appearance invalidates
	// a child scan continuation instead of silently changing its discovery scope.
	bindings["parents"] = c.privateConfiguration(map[string]any{"parents": parents})
	return items, bindings, provenance, nil
}

func (r *Runtime) listHybridCompute(ctx context.Context, c *client, request contracts.InventoryRequest) (batch contracts.InventoryBatch, err error) {
	defer func() { err = contracts.DependencyReadError(err) }()
	if request.Source != hybridComputeSource || request.ResourceKind == nil || hybridComputeKind(request.ResourceKind.NativeType) == "" || len(request.Options) != 0 || request.Scope.Kind != asset.ScopeSubscription && request.Scope.Kind != asset.ScopeRegion || request.Scope.Kind == asset.ScopeRegion && request.Scope.NativeID == "" {
		return batch, serviceDenied("invalid_hybrid_compute_inventory_request")
	}
	cursor := productCursor{}
	if request.Cursor != "" {
		if len(request.Cursor) > 128<<10 {
			return batch, serviceDenied("hybrid_compute_cursor_too_large")
		}
		encoded, err := base64.RawURLEncoding.DecodeString(request.Cursor)
		if err != nil || json.Unmarshal(encoded, &cursor) != nil || cursor.Target < 1 || cursor.Fingerprint == "" || cursor.Next != "" || len(cursor.Seen)+len(cursor.Resources) != 0 {
			return batch, serviceDenied("invalid_hybrid_compute_cursor")
		}
	}
	items, before, provenance, err := r.hybridComputeSnapshot(ctx, c, request)
	if err != nil {
		return batch, err
	}
	_, after, _, err := r.hybridComputeSnapshot(ctx, c, request)
	if err != nil {
		return batch, err
	}
	if c.privateConfiguration(before) != c.privateConfiguration(after) {
		return batch, serviceDenied("hybrid_compute_inventory_changed_during_scan")
	}
	boundary := request
	boundary.Cursor, boundary.Limit = "", 0
	fingerprint := c.privateConfiguration(map[string]any{"request": boundary, "revision": r.bundle.Revision, "bindings": before})
	if request.Cursor != "" && (cursor.Fingerprint != fingerprint || cursor.Target >= len(items)) {
		return batch, serviceDenied("hybrid_compute_cursor_changed")
	}
	limit := request.Limit
	if limit <= 0 {
		limit = 1000
	}
	end := cursor.Target + min(limit, len(items)-cursor.Target)
	batch = contracts.InventoryBatch{Items: items[cursor.Target:end], Complete: end == len(items), RequestID: provenance}
	if batch.Complete {
		for _, id := range request.KnownNativeIDs {
			if before[id] == nil {
				batch.AbsentNativeIDs = append(batch.AbsentNativeIDs, id)
			}
		}
	} else {
		cursor.Fingerprint, cursor.Target = fingerprint, end
		encoded, _ := json.Marshal(cursor)
		batch.NextCursor = base64.RawURLEncoding.EncodeToString(encoded)
	}
	return batch, nil
}
