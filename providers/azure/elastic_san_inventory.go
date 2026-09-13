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

const elasticSanSource = "elastic-san"
const elasticSanInventoryRecord = "_elastic_san_inventory"
const elasticSanInventoryProof = "_elastic_san_inventory_proof"

type elasticSanObservation struct {
	raw       map[string]any
	retained  bool
	authority string
}

func elasticSanRoot(id string) string { return strings.Join(strings.Split(id, "/")[:9], "/") }

func (c *client) elasticSanInventoryBinding(id, kind string, connection asset.ConnectionID, record map[string]any) string {
	return c.privateConfiguration(map[string]any{"protocol": "elastic-san-inventory-v1", "id": id, "kind": kind, "connection": connection, "record": record})
}

func (c *client) elasticSanRecorded(value asset.Asset) (map[string]any, error) {
	kind := elasticSanKind(value.Identity.NativeType)
	id, err := c.elasticSanIdentity(value.Identity.NativeID, kind)
	if err != nil || id != value.Identity.NativeID || value.Identity.Provider != asset.ProviderAzure || value.Identity.ConnectionID == "" || value.Identity.Partition != "azure" || value.Normalized["_inventory_source"] != elasticSanSource {
		return nil, serviceDenied("invalid_elastic_san_recorded_identity")
	}
	record := object(value.Normalized[elasticSanInventoryRecord])
	retained, ok := record["retained"].(bool)
	if len(record) != 7 || !ok || value.Normalized["retained"] != retained || record["root"] != elasticSanRoot(id) || record["parent"] != elasticSanParent(id, kind) || record["location"] != value.Location || value.Location == "" || text(record["configuration"]) == "" || object(record["references"]) == nil || value.Normalized[elasticSanInventoryProof] != c.elasticSanInventoryBinding(id, kind, value.Identity.ConnectionID, record) {
		return nil, serviceDenied("invalid_elastic_san_inventory_proof")
	}
	return record, nil
}

func (c *client) elasticSanRecordedReferences(value asset.Asset) (map[string][]string, error) {
	record, err := c.elasticSanRecorded(value)
	if err != nil {
		return nil, err
	}
	refs := map[string][]string{}
	for kind, values := range object(record["references"]) {
		refs[kind] = stringValues(values)
	}
	return refs, nil
}

func (r *Runtime) elasticSanSnapshot(ctx context.Context, c *client, request contracts.InventoryRequest) ([]contracts.InventoryItem, map[string]any, string, error) {
	kind := elasticSanKind(request.ResourceKind.NativeType)
	nodes, reads := map[string]elasticSanObservation{}, map[string]map[string]any{}
	prior, roots, groups := map[string]map[string]any{}, map[string]bool{}, map[string]bool{}
	known, requestID := map[string]bool{}, ""
	read := func(id, typ string) (map[string]any, error) {
		if raw, exists := reads[id]; exists {
			return raw, nil
		}
		res, err := c.elasticSanRead(ctx, id, typ)
		if isNotFound(err) {
			reads[id] = nil
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		if res.requestID != "" {
			requestID = res.requestID
		}
		reads[id] = res.data
		return res.data, nil
	}
	for _, id := range request.KnownNativeIDs {
		canonical, err := c.elasticSanIdentity(id, kind)
		if err != nil || id != canonical || known[id] {
			return nil, nil, "", serviceDenied("invalid_elastic_san_known_id")
		}
		known[id] = true
		if metadata := request.KnownNativeMetadata[id]; len(metadata) != 0 {
			value := asset.Asset{Identity: asset.Identity{Provider: asset.ProviderAzure, ConnectionID: request.ConnectionID, Partition: "azure", NativeID: id, NativeType: kind}, Location: text(object(metadata[elasticSanInventoryRecord])["location"]), Normalized: metadata}
			prior[id], err = c.elasticSanRecorded(value)
			if err != nil {
				return nil, nil, "", err
			}
		}
		if kind != elasticSanType {
			roots[elasticSanRoot(id)] = true
		}
		if kind == elasticSanVolumeType || kind == elasticSanSnapshotType {
			groups[elasticSanParent(id, kind)] = true
		}
	}
	for id := range request.KnownNativeMetadata {
		if !known[id] {
			return nil, nil, "", serviceDenied("unrelated_elastic_san_known_metadata")
		}
	}
	collect := func(typ, parent string) error {
		modes := []bool{false}
		if typ == elasticSanGroupType || typ == elasticSanVolumeType {
			modes = append(modes, true)
		}
		for _, retained := range modes {
			rows, provenance, err := c.elasticSanIndex(ctx, typ, parent, retained)
			if err != nil {
				return err
			}
			if provenance != "" {
				requestID = provenance
			}
			for _, value := range rows {
				listed := object(value)
				id, err := c.elasticSanRecord(listed, typ)
				if err != nil || nodes[id].raw != nil {
					return serviceDenied("duplicate_elastic_san_population")
				}
				live, err := read(id, typ)
				if err != nil {
					return err
				}
				authority := "get"
				if live == nil {
					if !retained {
						return serviceDenied("listed_elastic_san_resource_missing")
					}
					// The selected retained index remains authoritative when GET
					// cannot address it. Do not manufacture a GET selector.
					live, authority = listed, "retained-index"
				} else if serviceListedIncarnation(listed, live) != nil || !nativeConfigurationContains(object(listed["properties"]), object(live["properties"])) {
					return serviceDenied("elastic_san_index_resource_changed")
				}
				state := text(object(live["properties"])["provisioningState"])
				if !retained && (typ == elasticSanGroupType || typ == elasticSanVolumeType) && (state == "Deleted" || state == "SoftDeleting") {
					return serviceDenied("elastic_san_active_population_changed")
				}
				nodes[id] = elasticSanObservation{raw: live, retained: retained, authority: authority}
			}
		}
		return nil
	}
	// Recover explicitly known IDs even when native list indexes omit them.
	recoverKnown := func(id, typ string) error {
		if nodes[id].raw != nil {
			return nil
		}
		raw, err := read(id, typ)
		if err != nil || raw == nil {
			return err
		}
		retained := false
		if typ == elasticSanVolumeType || typ == elasticSanGroupType {
			switch text(object(raw["properties"])["provisioningState"]) {
			case "Deleted", "SoftDeleting":
				retained = true
			case "Deleting", "Restoring":
				retained = prior[id]["retained"] == true
			}
		}
		nodes[id] = elasticSanObservation{raw: raw, retained: retained, authority: "get"}
		return nil
	}
	if err := collect(elasticSanType, ""); err != nil {
		return nil, nil, "", err
	}
	if kind == elasticSanType {
		for id := range known {
			if err := recoverKnown(id, kind); err != nil {
				return nil, nil, "", err
			}
		}
	}
	for id := range nodes {
		roots[id] = true
	}
	for _, root := range slices.Sorted(maps.Keys(roots)) {
		if err := recoverKnown(root, elasticSanType); err != nil {
			return nil, nil, "", err
		}
		// Groups carry SAN network membership and retained child identities.
		if err := collect(elasticSanGroupType, root); err != nil {
			return nil, nil, "", err
		}
	}
	if kind == elasticSanGroupType {
		for id := range known {
			if err := recoverKnown(id, kind); err != nil {
				return nil, nil, "", err
			}
		}
	}
	for id := range nodes {
		if _, typ, _ := parseID(id); strings.EqualFold(typ, elasticSanGroupType) {
			groups[id] = true
		}
	}
	if kind == elasticSanVolumeType || kind == elasticSanSnapshotType {
		for _, group := range slices.Sorted(maps.Keys(groups)) {
			if err := recoverKnown(group, elasticSanGroupType); err != nil {
				return nil, nil, "", err
			}
			if err := collect(kind, group); err != nil {
				return nil, nil, "", err
			}
		}
	} else if kind == elasticSanEndpointType {
		for _, root := range slices.Sorted(maps.Keys(roots)) {
			if err := collect(kind, root); err != nil {
				return nil, nil, "", err
			}
		}
	}
	for id := range known {
		if err := recoverKnown(id, kind); err != nil {
			return nil, nil, "", err
		}
	}
	refsByID, allBindings := map[string]map[string][]string{}, map[string]any{}
	for id, observation := range nodes {
		_, typ, _ := parseID(id)
		if root := nodes[elasticSanRoot(id)].raw; root != nil && text(observation.raw["location"]) != "" && !strings.EqualFold(text(observation.raw["location"]), resourceRegion(root)) {
			return nil, nil, "", serviceDenied("elastic_san_ancestor_location_changed")
		}
		refs, err := elasticSanReferences(id, elasticSanKind(typ), observation.raw)
		if err != nil {
			return nil, nil, "", err
		}
		refsByID[id] = refs
		allBindings[id] = map[string]any{"raw": observation.raw, "retained": observation.retained, "authority": observation.authority}
	}
	items, bindings := []contracts.InventoryItem{}, map[string]any{"parents": c.privateConfiguration(allBindings)}
	for _, id := range slices.Sorted(maps.Keys(nodes)) {
		_, typ, _ := parseID(id)
		if !strings.EqualFold(typ, kind) {
			continue
		}
		observation := nodes[id]
		raw, root, parent := observation.raw, elasticSanRoot(id), elasticSanParent(id, kind)
		location := resourceRegion(nodes[root].raw)
		if nodes[root].raw == nil {
			location = text(prior[id]["location"])
		}
		if location == "" || location == "global" || text(raw["location"]) != "" && !strings.EqualFold(text(raw["location"]), location) {
			return nil, nil, "", serviceDenied("unverified_elastic_san_location")
		}
		if parent != "" && text(nodes[parent].raw["location"]) != "" && !strings.EqualFold(text(nodes[parent].raw["location"]), location) {
			return nil, nil, "", serviceDenied("elastic_san_parent_location_changed")
		}
		network := []string{}
		addNetwork := func(refs map[string][]string) {
			for _, ids := range refs {
				network = append(network, ids...)
			}
		}
		addNetwork(refsByID[id])
		addNetwork(refsByID[root])
		addNetwork(refsByID[parent])
		if kind == elasticSanType {
			for group := range groups {
				if elasticSanRoot(group) == root {
					addNetwork(refsByID[group])
				}
			}
		}
		if parent != "" && nodes[parent].raw == nil {
			if prior[id] == nil {
				return nil, nil, "", serviceDenied("unverified_elastic_san_parent")
			}
			network = append(network, stringValues(prior[id]["network"])...)
		}
		slices.Sort(network)
		network = slices.Compact(network)
		safe := safePayload(object(elasticSanSafeValue(raw)))
		normalized := maps.Clone(object(safe["properties"]))
		normalized["name"], normalized["tags"], normalized["state"] = safe["name"], safe["tags"], object(safe["properties"])["provisioningState"]
		normalized["subscription_id"], normalized["resource_group"] = c.subscription, strings.Split(id, "/")[4]
		normalized["_inventory_source"], normalized["retained"] = elasticSanSource, observation.retained
		references := map[string]any{}
		for typ, ids := range refsByID[id] {
			references[typ], normalized[referenceKey(typ)] = ids, ids
		}
		record := map[string]any{"configuration": c.privateConfiguration(map[string]any{"resource": raw, "root": nodes[root].raw, "parent": nodes[parent].raw, "authority": observation.authority}), "root": root, "parent": parent, "location": location, "retained": observation.retained, "references": references, "network": network}
		normalized[elasticSanInventoryRecord], normalized[elasticSanInventoryProof] = record, c.elasticSanInventoryBinding(id, kind, request.ConnectionID, record)
		bindings[id] = c.privateConfiguration(record)
		tags := map[string]string{}
		for key, value := range object(raw["tags"]) {
			tags[key] = value.(string)
		}
		actionable := false
		item := contracts.InventoryItem{NativeID: id, NativeType: kind, ResourceKind: r.resourceKind(kind), Name: text(raw["name"]), State: text(normalized["state"]), Tags: tags, Location: location, Scope: contracts.InventoryScope{Kind: asset.ScopeRegion, NativeID: location, Name: location, Location: location}, Raw: safe, Normalized: normalized, Actionable: &actionable, NativeAliases: []string{id, text(raw["id"])}, NetworkReferences: network}
		if productScopeMatches(request, item) {
			items = append(items, item)
		}
	}
	return items, bindings, requestID, nil
}

func (r *Runtime) listElasticSan(ctx context.Context, c *client, request contracts.InventoryRequest) (batch contracts.InventoryBatch, err error) {
	defer func() { err = contracts.DependencyReadError(err) }()
	if request.Source != elasticSanSource || request.ResourceKind == nil || elasticSanKind(request.ResourceKind.NativeType) == "" || len(request.Options) != 0 || request.Scope.Kind != asset.ScopeSubscription && request.Scope.Kind != asset.ScopeRegion || request.Scope.Kind == asset.ScopeRegion && request.Scope.NativeID == "" {
		return batch, serviceDenied("invalid_elastic_san_inventory_request")
	}
	cursor := productCursor{}
	if request.Cursor != "" {
		if len(request.Cursor) > 128<<10 {
			return batch, serviceDenied("elastic_san_cursor_too_large")
		}
		encoded, err := base64.RawURLEncoding.DecodeString(request.Cursor)
		if err != nil || json.Unmarshal(encoded, &cursor) != nil || cursor.Target < 1 || cursor.Fingerprint == "" || cursor.Next != "" || len(cursor.Seen)+len(cursor.Resources) != 0 {
			return batch, serviceDenied("invalid_elastic_san_cursor")
		}
	}
	items, before, provenance, err := r.elasticSanSnapshot(ctx, c, request)
	if err != nil {
		return batch, err
	}
	_, after, _, err := r.elasticSanSnapshot(ctx, c, request)
	if err != nil {
		return batch, err
	}
	if c.privateConfiguration(before) != c.privateConfiguration(after) {
		return batch, serviceDenied("elastic_san_inventory_changed_during_scan")
	}
	boundary := request
	boundary.Cursor, boundary.Limit = "", 0
	fingerprint := c.privateConfiguration(map[string]any{"request": boundary, "revision": r.bundle.Revision, "bindings": before})
	if request.Cursor != "" && (cursor.Fingerprint != fingerprint || cursor.Target >= len(items)) {
		return batch, serviceDenied("elastic_san_cursor_changed")
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
