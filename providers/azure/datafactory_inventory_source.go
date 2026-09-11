package azure

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

const (
	dataFactoryConfiguration = "_datafactory_configuration"
	dataFactoryMembers       = "_datafactory_members"
	dataFactoryProof         = "_datafactory_binding"
)

func (c *client) dataFactoryBinding(id, kind string, normalized map[string]any) string {
	result := map[string]any{"id": id, "kind": kind, "protocol": "datafactory-native-1"}
	for _, key := range []string{dataFactoryConfiguration, dataFactoryMembers, "_datafactory_connection", "_datafactory_root", "_datafactory_parent", "_datafactory_node_name", "_datafactory_ancestors", "_datafactory_group", "_datafactory_references", "_datafactory_location", "_datafactory_work", "_datafactory_incoming", "_datafactory_links", "arm_parameters", "arm_etag", "cleanup_protected", "cleanup_protection_reason"} {
		result[key] = normalized[key]
	}
	return c.privateConfiguration(result)
}

func (c *client) dataFactoryRecorded(id, kind string, normalized map[string]any) (map[string]any, error) {
	if c.dataFactoryIdentity(id, kind) != nil || text(normalized[dataFactoryConfiguration]) == "" || text(normalized["_datafactory_group"]) == "" || normalized["_inventory_source"] != dataFactoryInventorySource || normalized[dataFactoryProof] != c.dataFactoryBinding(id, kind, normalized) {
		return nil, serviceDenied("invalid_datafactory_recorded_binding")
	}
	for _, key := range []string{dataFactoryMembers, "_datafactory_ancestors", "_datafactory_references", "_datafactory_work", "_datafactory_incoming", "_datafactory_links", "arm_parameters"} {
		if value, ok := normalized[key].(map[string]any); !ok || value == nil {
			return nil, serviceDenied("datafactory_recorded_context_missing")
		}
	}
	if normalized["_datafactory_root"] != dataFactoryRoot(id) || normalized["_datafactory_parent"] != dataFactoryParent(id, kind) || text(normalized["_datafactory_location"]) == "" {
		return nil, serviceDenied("datafactory_recorded_scope_changed")
	}
	if _, err := dataFactoryWireID(id, kind, text(normalized["_datafactory_node_name"])); err != nil {
		return nil, err
	}
	return object(normalized[dataFactoryMembers]), nil
}

func (c *client) dataFactoryKnown(request contracts.InventoryRequest) (map[string]dataFactoryMember, error) {
	hints, seen := map[string]dataFactoryMember{}, map[string]bool{}
	add := func(id, kind, nodeName string) error {
		if c.dataFactoryIdentity(id, kind) != nil {
			return serviceDenied("invalid_datafactory_known_identity")
		}
		if _, err := dataFactoryWireID(id, kind, nodeName); err != nil {
			return err
		}
		if previous, ok := hints[id]; ok && (previous.kind != kind || previous.nodeName != nodeName) {
			return serviceDenied("ambiguous_datafactory_known_identity")
		}
		hints[id] = dataFactoryMember{id: id, kind: kind, parent: dataFactoryParent(id, kind), root: dataFactoryRoot(id), nodeName: nodeName}
		return nil
	}
	for _, id := range request.KnownNativeIDs {
		if seen[id] {
			return nil, serviceDenied("duplicate_datafactory_known_id")
		}
		seen[id] = true
		kind := request.ResourceKind.NativeType
		normalized := request.KnownNativeMetadata[id]
		if normalized["_datafactory_connection"] != string(request.ConnectionID) {
			return nil, serviceDenied("datafactory_known_connection_changed")
		}
		members, err := c.dataFactoryRecorded(id, kind, normalized)
		if err != nil {
			return nil, err
		}
		if err := add(id, kind, text(normalized["_datafactory_node_name"])); err != nil {
			return nil, err
		}
		for childID, value := range members {
			entry := object(value)
			childKind := text(entry["kind"])
			if !strings.HasPrefix(childID, id+"/") || entry["parent"] != dataFactoryParent(childID, childKind) || text(entry["configuration"]) == "" {
				return nil, serviceDenied("invalid_datafactory_known_member")
			}
			if err := add(childID, childKind, text(entry["nodeName"])); err != nil {
				return nil, err
			}
		}
		// Reverse dependencies are authenticated discovery hints too. A
		// consumer omitted from LIST cannot silently stop protecting its target.
		for target, values := range object(normalized["_datafactory_incoming"]) {
			_, typ, err := parseID(target)
			if err != nil || c.dataFactoryIdentity(target, dataFactoryKind(typ)) != nil || dataFactoryRoot(target) != dataFactoryRoot(id) || object(values) == nil {
				return nil, serviceDenied("invalid_datafactory_known_dependency_target")
			}
			for source, value := range object(values) {
				entry := object(value)
				if text(entry["configuration"]) == "" {
					return nil, serviceDenied("invalid_datafactory_known_dependency")
				}
				if err := add(source, text(entry["kind"]), ""); err != nil {
					return nil, err
				}
			}
		}
		for owner, values := range object(normalized["_datafactory_links"]) {
			if c.dataFactoryIdentity(owner, dataFactoryIRType) != nil || dataFactoryRoot(owner) != dataFactoryRoot(id) || object(values) == nil {
				return nil, serviceDenied("invalid_datafactory_known_sharing_owner")
			}
			for _, value := range object(values) {
				if source := text(object(value)["source"]); source != "" {
					if err := add(source, dataFactoryIRType, ""); err != nil {
						return nil, err
					}
				}
			}
		}
	}
	for id := range request.KnownNativeMetadata {
		if !seen[id] {
			return nil, serviceDenied("unrequested_datafactory_known_metadata")
		}
	}
	for _, hint := range maps.Clone(hints) {
		for parent := hint.parent; parent != ""; {
			_, typ, err := parseID(parent)
			kind := dataFactoryKind(typ)
			if err != nil || kind == "" {
				return nil, serviceDenied("invalid_datafactory_known_ancestor")
			}
			if hints[parent].id == "" {
				if err := add(parent, kind, ""); err != nil {
					return nil, err
				}
			}
			parent = dataFactoryParent(parent, kind)
		}
	}
	return hints, nil
}

func dataFactoryProtection(member dataFactoryMember) string {
	if protectedAzureTags(object(member.raw["tags"])) {
		return "azure_protected_tag"
	}
	props := object(member.raw["properties"])
	switch member.kind {
	case dataFactoryType:
		if _, err := time.Parse(time.RFC3339Nano, text(props["createTime"])); err != nil {
			return "azure_datafactory_creation_unverified"
		}
		if !slices.Contains([]string{"Succeeded", "Failed", "Canceled"}, member.state) {
			return "azure_datafactory_resource_busy"
		}
	case dataFactoryIRType:
		if props["type"] == "SelfHosted" {
			if _, err := time.Parse(time.RFC3339Nano, text(object(object(member.status["properties"])["typeProperties"])["createTime"])); err != nil {
				return "azure_datafactory_runtime_creation_unverified"
			}
		}
		if !slices.Contains([]string{"Initial", "Stopped", "Started", "Starting", "Stopping", "NeedRegistration", "Online", "Limited", "Offline"}, member.state) {
			return "azure_datafactory_runtime_state_unverified"
		}
	case dataFactoryNodeType:
		if _, err := time.Parse(time.RFC3339Nano, text(member.raw["registerTime"])); err != nil {
			return "azure_datafactory_node_creation_unverified"
		}
	case dataFactoryCDCType:
		if !slices.Contains([]string{"Running", "Stopped"}, member.state) {
			return "azure_datafactory_cdc_state_unverified"
		}
	case dataFactoryTriggerType:
		if !slices.Contains([]string{"Started", "Stopped", "Disabled"}, member.state) {
			return "azure_datafactory_trigger_state_unverified"
		}
		if member.eventState == "Unknown" {
			return "azure_datafactory_event_subscription_unverified"
		}
	case dataFactoryEndpointType:
		if props["isReserved"] == true {
			return "azure_datafactory_reserved_endpoint"
		}
	}
	return ""
}

func (r *Runtime) dataFactoryInventoryItem(c *client, connection asset.ConnectionID, tree dataFactoryTree, member dataFactoryMember, group map[string]any, locks []any) (contracts.InventoryItem, error) {
	root := tree.members[tree.root]
	location := resourceRegion(root.raw)
	if root.kind != dataFactoryType || location == "global" {
		return contracts.InventoryItem{}, serviceDenied("datafactory_location_unverified")
	}
	snapshot, err := dataFactoryMemberSnapshot(member)
	if err != nil {
		return contracts.InventoryItem{}, err
	}
	safe := safePayload(object(dataFactorySafeValue(member.raw)))
	normalized := map[string]any{"name": last(member.id), "state": member.state, "tags": safe["tags"], "subscription_id": c.subscription, "resource_group": strings.Split(tree.root, "/")[4], "_inventory_source": dataFactoryInventorySource, "_datafactory_connection": string(connection), "_datafactory_root": tree.root, "_datafactory_parent": member.parent, "_datafactory_node_name": member.nodeName, "_datafactory_location": location, dataFactoryConfiguration: c.privateConfiguration(snapshot), "_datafactory_group": c.privateConfiguration(insightsWorkspaceResourceSnapshot(group)), "_datafactory_references": monitorReferenceProjection(member.refs)}
	maps.Copy(normalized, object(safe["properties"]))
	normalized["state"] = member.state
	ancestors, members := map[string]any{}, map[string]any{}
	direct := dataFactoryDirectMembers(tree)
	reason := dataFactoryProtection(member)
	for id := member.parent; id != ""; id = tree.members[id].parent {
		parent, ok := tree.members[id]
		if !ok {
			return contracts.InventoryItem{}, serviceDenied("datafactory_ancestor_missing")
		}
		config, err := dataFactoryMemberSnapshot(parent)
		if err != nil {
			return contracts.InventoryItem{}, err
		}
		ancestors[id] = c.privateConfiguration(config)
		if protection := dataFactoryProtection(parent); protection != "" {
			reason = protection
		}
	}
	for id, child := range tree.members {
		if !strings.HasPrefix(id, member.id+"/") {
			continue
		}
		config, err := dataFactoryMemberSnapshot(child)
		if err != nil {
			return contracts.InventoryItem{}, err
		}
		members[id] = map[string]any{"kind": child.kind, "parent": child.parent, "controller": dataFactoryController(child), "nodeName": child.nodeName, "configuration": c.privateConfiguration(config), "direct": direct[id]}
	}
	normalized["_datafactory_ancestors"], normalized[dataFactoryMembers] = ancestors, members
	normalized["_datafactory_work"] = c.dataFactoryWorkManifest(tree.work)
	normalized["_datafactory_incoming"], normalized["_datafactory_links"] = tree.incoming, tree.links
	if protection := dataFactorySharingProtection(member.id, member.kind, tree.links); protection != "" {
		reason = protection
	}
	mapping, _ := findType(member.kind)
	wire, err := dataFactoryWireID(member.id, member.kind, member.nodeName)
	if err != nil {
		return contracts.InventoryItem{}, err
	}
	_, params, err := c.resourceOperation(mapping, wire, "GET")
	if err != nil {
		return contracts.InventoryItem{}, err
	}
	normalized["arm_parameters"] = params
	normalized["arm_etag"] = text(member.raw["_datafactory_header_etag"])
	if normalized["arm_etag"] == "" {
		stamp, err := dataFactoryETag(response{data: member.raw})
		if err != nil {
			return contracts.InventoryItem{}, err
		}
		normalized["arm_etag"] = stamp
	}
	network := []string{}
	for kind, refs := range member.refs {
		normalized[referenceKey(kind)] = refs
		network = append(network, refs...)
	}
	slices.Sort(network)
	if text(group["managedBy"]) != "" {
		reason = "azure_managed_resource_group"
	}
	if protectedAzureTags(object(group["tags"])) {
		reason = "azure_protected_tag"
	}
	if locked(member.id, locks) {
		reason = "azure_management_lock"
	}
	normalized["cleanup_protected"], normalized["cleanup_protection_reason"] = reason != "", reason
	normalized[dataFactoryProof] = c.dataFactoryBinding(member.id, member.kind, normalized)
	if err := c.rbacIdentityInventory(member.id, member.kind, member.raw, normalized); err != nil {
		return contracts.InventoryItem{}, err
	}
	tags := map[string]string{}
	for key, value := range object(safe["tags"]) {
		if str, ok := value.(string); ok {
			tags[key] = str
		}
	}
	actionable := reason == "" && !mapping.ReadOnly
	return contracts.InventoryItem{NativeID: member.id, NativeType: member.kind, ResourceKind: r.resourceKind(member.kind), Actionable: &actionable, Scope: contracts.InventoryScope{Kind: asset.ScopeRegion, NativeID: location, Name: location, Location: location}, Name: last(member.id), State: member.state, Location: location, Tags: tags, Normalized: normalized, Raw: safe, NetworkReferences: network, NativeAliases: []string{member.id}}, nil
}

func (c *client) dataFactoryContext(ctx context.Context, hints map[string]dataFactoryMember, metadata map[string]map[string]any) (map[string]dataFactoryTree, map[string]bool, error) {
	trees, missing, err := c.dataFactoryForest(ctx, hints)
	if err != nil {
		return nil, nil, err
	}
	knownWork := map[string]map[string]any{}
	for _, normalized := range metadata {
		root := text(normalized["_datafactory_root"])
		if knownWork[root] == nil {
			knownWork[root] = map[string]any{"runs": map[string]any{}, "debug": map[string]any{}}
		}
		for _, part := range []string{"runs", "debug"} {
			for id, entry := range object(object(normalized["_datafactory_work"])[part]) {
				previous := object(knownWork[root][part])[id]
				if previous != nil && c.privateConfiguration(object(previous)) != c.privateConfiguration(object(entry)) {
					return nil, nil, serviceDenied("conflicting_datafactory_known_work")
				}
				object(knownWork[root][part])[id] = entry
			}
		}
	}
	for _, rootID := range slices.Sorted(maps.Keys(trees)) {
		tree := trees[rootID]
		tree.work, err = c.dataFactoryWork(ctx, tree, knownWork[rootID])
		if err != nil {
			return nil, nil, err
		}
		trees[rootID] = tree
	}
	if err := c.dataFactoryRelations(ctx, trees, metadata); err != nil {
		return nil, nil, err
	}
	return trees, missing, nil
}

func (r *Runtime) dataFactoryItems(ctx context.Context, c *client, connection asset.ConnectionID, trees map[string]dataFactoryTree) (map[string]contracts.InventoryItem, map[string]any, error) {
	groups, err := c.insightsGroups(ctx)
	if err != nil {
		return nil, nil, err
	}
	locks, err := c.managementLocks(ctx)
	if err != nil {
		return nil, nil, err
	}
	verified := map[string]map[string]any{}
	items, bindings := map[string]contracts.InventoryItem{}, map[string]any{}
	for _, rootID := range slices.Sorted(maps.Keys(trees)) {
		tree := trees[rootID]
		groupID := strings.Join(strings.Split(rootID, "/")[:5], "/")
		if verified[groupID] == nil {
			if groups[groupID] == nil {
				return nil, nil, serviceDenied("datafactory_group_missing_from_index")
			}
			verified[groupID], err = c.insightsGroup(ctx, groupID, groups[groupID])
			if err != nil {
				return nil, nil, err
			}
		}
		for _, id := range slices.Sorted(maps.Keys(tree.members)) {
			member := tree.members[id]
			item, err := r.dataFactoryInventoryItem(c, connection, tree, member, verified[groupID], locks)
			if err != nil {
				return nil, nil, err
			}
			bindings[id] = map[string]any{"proof": item.Normalized[dataFactoryProof], "protection": item.Normalized["cleanup_protection_reason"], "state": member.state, "event_state": member.eventState}
			items[id] = item
		}
	}
	return items, bindings, nil
}

func (r *Runtime) dataFactoryInventorySnapshot(ctx context.Context, c *client, request contracts.InventoryRequest) ([]contracts.InventoryItem, []string, map[string]any, error) {
	hints, err := c.dataFactoryKnown(request)
	if err != nil {
		return nil, nil, nil, err
	}
	trees, missing, err := c.dataFactoryContext(ctx, hints, request.KnownNativeMetadata)
	if err != nil {
		return nil, nil, nil, err
	}
	all, bindings, err := r.dataFactoryItems(ctx, c, request.ConnectionID, trees)
	if err != nil {
		return nil, nil, nil, err
	}
	items := []contracts.InventoryItem{}
	for _, id := range slices.Sorted(maps.Keys(all)) {
		item := all[id]
		if item.NativeType == request.ResourceKind.NativeType && productScopeMatches(request, item) {
			items = append(items, item)
		}
	}
	absent := []string{}
	for _, id := range request.KnownNativeIDs {
		if missing[id] {
			absent = append(absent, id)
		}
	}
	slices.Sort(absent)
	return items, absent, bindings, nil
}

func (r *Runtime) listDataFactory(ctx context.Context, c *client, request contracts.InventoryRequest) (batch contracts.InventoryBatch, err error) {
	defer func() { err = contracts.DependencyReadError(err) }()
	if request.Source != dataFactoryInventorySource || request.ResourceKind == nil || dataFactoryKind(request.ResourceKind.NativeType) == "" || dataFactoryKind(request.ResourceKind.NativeType) != request.ResourceKind.NativeType || len(request.Options) != 0 {
		return batch, serviceDenied("invalid_datafactory_inventory_source")
	}
	switch request.Scope.Kind {
	case asset.ScopeSubscription:
		if !strings.EqualFold(request.Scope.NativeID, c.subscription) {
			return batch, serviceDenied("datafactory_inventory_subscription_changed")
		}
	case asset.ScopeRegion:
		if request.Scope.NativeID == "" || request.Scope.NativeID != strings.TrimSpace(request.Scope.NativeID) {
			return batch, serviceDenied("invalid_datafactory_inventory_region")
		}
	default:
		return batch, serviceDenied("invalid_datafactory_inventory_scope")
	}
	cursor := productCursor{}
	if request.Cursor != "" {
		if len(request.Cursor) > 128<<10 {
			return batch, serviceDenied("datafactory_inventory_cursor_too_large")
		}
		payload, e := base64.RawURLEncoding.DecodeString(request.Cursor)
		if e != nil || json.Unmarshal(payload, &cursor) != nil || cursor.Target < 1 || cursor.Fingerprint == "" || cursor.Next != "" || len(cursor.Seen)+len(cursor.Resources) != 0 {
			return batch, serviceDenied("invalid_datafactory_inventory_cursor")
		}
	}
	items, absent, before, err := r.dataFactoryInventorySnapshot(ctx, c, request)
	if err != nil {
		return batch, err
	}
	_, afterAbsent, after, err := r.dataFactoryInventorySnapshot(ctx, c, request)
	if err != nil {
		return batch, err
	}
	if !slices.Equal(absent, afterAbsent) || c.privateConfiguration(before) != c.privateConfiguration(after) {
		return batch, serviceDenied("datafactory_collection_changed_during_scan")
	}
	boundary := request
	boundary.Cursor = ""
	boundary.Limit = 0
	fingerprint := c.privateConfiguration(map[string]any{"request": boundary, "revision": r.bundle.Revision, "bindings": before, "absent": absent})
	if request.Cursor != "" && (cursor.Fingerprint != fingerprint || cursor.Target >= len(items)) {
		return batch, serviceDenied("datafactory_inventory_cursor_changed")
	}
	limit := request.Limit
	if limit <= 0 {
		limit = 1000
	}
	end := cursor.Target + min(limit, len(items)-cursor.Target)
	batch = contracts.InventoryBatch{Items: items[cursor.Target:end], Complete: end == len(items)}
	if batch.Complete {
		batch.AbsentNativeIDs = absent
	} else {
		cursor.Fingerprint, cursor.Target = fingerprint, end
		payload, _ := json.Marshal(cursor)
		batch.NextCursor = base64.RawURLEncoding.EncodeToString(payload)
	}
	return batch, nil
}
