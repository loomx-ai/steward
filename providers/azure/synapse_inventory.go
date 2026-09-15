package azure

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"slices"
	"strings"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func (r *Runtime) listSynapse(ctx context.Context, c *client, request contracts.InventoryRequest) (batch contracts.InventoryBatch, err error) {
	defer func() { err = contracts.DependencyReadError(err) }()
	if request.ResourceKind == nil || synapseKind(request.ResourceKind.NativeType) == "" || request.Source != synapseSource || len(request.Options) != 0 || request.Scope.Kind != asset.ScopeSubscription && request.Scope.Kind != asset.ScopeRegion {
		return batch, serviceDenied("invalid_synapse_inventory_request")
	}
	if len(request.KnownNativeIDs) == 0 {
		if len(request.KnownNativeMetadata) != 0 {
			return batch, serviceDenied("unrelated_synapse_known_metadata")
		}
		if request.ResourceKind.NativeType != synapseSparkType {
			return r.listProduct(ctx, c, request, nil)
		}
	}
	// Lists discover new resources. A known resource's own GET establishes
	// continued existence or absence, even when its parent is omitted from LIST.
	kind := synapseKind(request.ResourceKind.NativeType)
	known := map[string]bool{}
	for _, id := range request.KnownNativeIDs {
		canonical, typ, parseErr := parseID(id)
		segments := 9
		if kind != synapseType {
			segments = 11
		}
		if parseErr != nil || id != canonical || !strings.EqualFold(typ, kind) || !strings.HasPrefix(id, c.root()+"/") || len(strings.Split(id, "/")) != segments || known[id] {
			return batch, serviceDenied("invalid_synapse_known_identity")
		}
		known[id] = true
	}
	for id := range request.KnownNativeMetadata {
		if !known[id] {
			return batch, serviceDenied("unrelated_synapse_known_metadata")
		}
	}
	cursor := productCursor{}
	if request.Cursor != "" {
		encoded, decodeErr := base64.RawURLEncoding.DecodeString(request.Cursor)
		if len(request.Cursor) > 128<<10 || decodeErr != nil || json.Unmarshal(encoded, &cursor) != nil || cursor.Target < 1 || cursor.Fingerprint == "" || cursor.Next != "" || len(cursor.Seen)+len(cursor.Resources) != 0 {
			return batch, serviceDenied("invalid_synapse_known_cursor")
		}
	}
	native := request
	native.KnownNativeIDs, native.KnownNativeMetadata, native.Cursor = nil, nil, ""
	items, seen := []contracts.InventoryItem{}, map[string]bool{}
	provenance := ""
	for {
		page, readErr := r.listProduct(ctx, c, native, nil)
		if readErr != nil {
			return batch, readErr
		}
		if page.RequestID != "" {
			provenance = page.RequestID
		}
		for _, item := range page.Items {
			if seen[item.NativeID] {
				return batch, serviceDenied("duplicate_synapse_inventory_identity")
			}
			seen[item.NativeID] = true
			items = append(items, item)
		}
		if page.Complete {
			break
		}
		native.Cursor = page.NextCursor
	}
	owners, locks, err := c.inventoryProtection(ctx)
	if err != nil {
		return batch, err
	}
	absent := []string{}
	for _, id := range request.KnownNativeIDs {
		if seen[id] {
			continue
		}
		res, readErr := c.request(ctx, "GET", apiURL(id, synapseVersion))
		if isNotFound(readErr) {
			absent = append(absent, id)
			continue
		}
		if readErr != nil {
			return batch, readErr
		}
		if err := c.synapseReadResponse(res, id, kind); err != nil {
			return batch, err
		}
		// The worker supplies all connection-wide known IDs to every region.
		// A live sibling-region resource remains active without becoming an
		// observation in this shard; this source has no list-absence authority.
		if request.Scope.Kind == asset.ScopeRegion && !strings.EqualFold(resourceRegion(res.data), request.Scope.NativeID) {
			continue
		}
		if res.requestID != "" {
			provenance = res.requestID
		}
		parent := strings.Join(strings.Split(id, "/")[:9], "/")
		var before response
		if kind != synapseType {
			before, err = c.request(ctx, "GET", apiURL(parent, synapseVersion))
			if err != nil {
				return batch, err
			}
			if err := c.synapseReadResponse(before, parent, synapseType); err != nil {
				return batch, err
			}
			if resourceRegion(before.data) != resourceRegion(res.data) {
				return batch, serviceDenied("synapse_known_parent_region_changed")
			}
		}
		item, err := r.inventoryItem(ctx, c, res.data, owners, locks)
		if err != nil {
			return batch, err
		}
		if !productScopeMatches(request, item) {
			return batch, serviceDenied("synapse_known_scope_changed")
		}
		item.Normalized["_inventory_source"] = synapseSource
		if kind != synapseType {
			current, readErr := c.request(ctx, "GET", apiURL(id, synapseVersion))
			if readErr != nil {
				return batch, readErr
			}
			if err := c.synapseReadResponse(current, id, kind); err != nil {
				return batch, err
			}
			after, readErr := c.request(ctx, "GET", apiURL(parent, synapseVersion))
			if readErr != nil {
				return batch, readErr
			}
			if err := c.synapseReadResponse(after, parent, synapseType); err != nil {
				return batch, err
			}
			if c.privateConfiguration(synapseSnapshot(res.data)) != c.privateConfiguration(synapseSnapshot(current.data)) || c.privateConfiguration(synapseSnapshot(before.data)) != c.privateConfiguration(synapseSnapshot(after.data)) {
				return batch, serviceDenied("synapse_known_configuration_changed")
			}
		}
		items = append(items, item)
	}
	if kind == synapseSparkType {
		for i := range items {
			if err := r.synapseSparkInventory(ctx, c, request, &items[i]); err != nil {
				return batch, err
			}
		}
	}
	slices.SortFunc(items, func(a, b contracts.InventoryItem) int { return strings.Compare(a.NativeID, b.NativeID) })
	slices.Sort(absent)
	boundary := request
	boundary.Cursor, boundary.Limit = "", 0
	fingerprint := c.privateConfiguration(map[string]any{"request": boundary, "revision": r.bundle.Revision, "items": items, "absent": absent})
	if request.Cursor != "" && (cursor.Fingerprint != fingerprint || cursor.Target >= len(items)) {
		return batch, serviceDenied("synapse_known_cursor_changed")
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
		encoded, _ := json.Marshal(cursor)
		batch.NextCursor = base64.RawURLEncoding.EncodeToString(encoded)
	}
	return batch, nil
}
