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
	dataMigrationConfiguration = "_datamigration_configuration"
	dataMigrationMembers       = "_datamigration_members"
	dataMigrationProof         = "_datamigration_binding"
)

func (c *client) dataMigrationBinding(id, kind string, normalized map[string]any) string {
	value := map[string]any{"id": id, "kind": kind, "protocol": "datamigration-native-1"}
	for _, key := range []string{dataMigrationConfiguration, dataMigrationMembers, "_datamigration_connection", "_datamigration_service", "_datamigration_parent", "_datamigration_ancestors", "_datamigration_groups", "_datamigration_target", "_datamigration_dependents", "_datamigration_nodes", "_datamigration_location", "_datamigration_references", "arm_parameters", "arm_etag", "cleanup_protected", "cleanup_protection_reason"} {
		value[key] = normalized[key]
	}
	return c.privateConfiguration(value)
}

func (c *client) dataMigrationRecorded(id, kind string, normalized map[string]any) error {
	if c.dataMigrationIdentity(id, kind) != nil || text(normalized[dataMigrationConfiguration]) == "" || normalized["_inventory_source"] != dataMigrationInventorySource || normalized[dataMigrationProof] != c.dataMigrationBinding(id, kind, normalized) || text(normalized["_datamigration_location"]) == "" || normalized["_datamigration_parent"] != dataMigrationParent(id, kind) {
		return serviceDenied("invalid_datamigration_recorded_binding")
	}
	for _, key := range []string{dataMigrationMembers, "_datamigration_ancestors", "_datamigration_groups", "_datamigration_target", "_datamigration_dependents", "_datamigration_nodes", "_datamigration_references", "arm_parameters"} {
		if value, ok := normalized[key].(map[string]any); !ok || value == nil {
			return serviceDenied("datamigration_recorded_context_missing")
		}
	}
	service := text(normalized["_datamigration_service"])
	_, typ, _ := parseID(service)
	if c.dataMigrationIdentity(service, dataMigrationKind(typ)) != nil {
		return serviceDenied("invalid_datamigration_recorded_service")
	}
	return nil
}

func (c *client) dataMigrationKnown(request contracts.InventoryRequest) (map[string]dataMigrationMember, error) {
	hints, seen := map[string]dataMigrationMember{}, map[string]bool{}
	add := func(id, kind string) error {
		if c.dataMigrationIdentity(id, kind) != nil || dataMigrationClassic(kind) != dataMigrationClassic(request.ResourceKind.NativeType) {
			return serviceDenied("invalid_datamigration_known_identity")
		}
		if previous := hints[id]; previous.id != "" && previous.kind != kind {
			return serviceDenied("ambiguous_datamigration_known_identity")
		}
		hints[id] = dataMigrationMember{id: id, kind: kind, parent: dataMigrationParent(id, kind)}
		return nil
	}
	for _, id := range request.KnownNativeIDs {
		if seen[id] {
			return nil, serviceDenied("duplicate_datamigration_known_id")
		}
		seen[id] = true
		normalized := request.KnownNativeMetadata[id]
		kind := request.ResourceKind.NativeType
		if normalized["_datamigration_connection"] != string(request.ConnectionID) {
			return nil, serviceDenied("datamigration_known_connection_changed")
		}
		if err := c.dataMigrationRecorded(id, kind, normalized); err != nil {
			return nil, err
		}
		if err := add(id, kind); err != nil {
			return nil, err
		}
		for _, key := range []string{dataMigrationMembers, "_datamigration_dependents"} {
			for childID, value := range object(normalized[key]) {
				entry := object(value)
				childKind := text(entry["kind"])
				if text(entry["configuration"]) == "" || key == dataMigrationMembers && (!strings.HasPrefix(childID, id+"/") || entry["parent"] != dataMigrationParent(childID, childKind)) || key == "_datamigration_dependents" && childKind != dataMigrationType && childKind != dataMigrationTaskType {
					return nil, serviceDenied("invalid_datamigration_known_member")
				}
				if err := add(childID, childKind); err != nil {
					return nil, err
				}
			}
		}
		for ancestor := range object(normalized["_datamigration_ancestors"]) {
			_, typ, _ := parseID(ancestor)
			if err := add(ancestor, dataMigrationKind(typ)); err != nil {
				return nil, err
			}
		}
		service := text(normalized["_datamigration_service"])
		_, typ, _ := parseID(service)
		if err := add(service, dataMigrationKind(typ)); err != nil {
			return nil, err
		}
	}
	for id := range request.KnownNativeMetadata {
		if !seen[id] {
			return nil, serviceDenied("unrequested_datamigration_known_metadata")
		}
	}
	for _, hint := range maps.Clone(hints) {
		for parent := hint.parent; parent != ""; {
			_, typ, _ := parseID(parent)
			kind := dataMigrationKind(typ)
			if err := add(parent, kind); err != nil {
				return nil, err
			}
			parent = dataMigrationParent(parent, kind)
		}
	}
	return hints, nil
}

func dataMigrationProtection(member dataMigrationMember) string {
	if text(member.raw["managedBy"]) != "" {
		return "azure_managed_resource"
	}
	if protectedAzureTags(object(member.raw["tags"])) {
		return "azure_protected_tag"
	}
	props := object(member.raw["properties"])
	switch member.kind {
	case dataMigrationServiceType:
		if !slices.Contains([]string{"Succeeded", "Failed", "Stopped", "FailedToStart", "FailedToStop"}, member.state) {
			return "azure_datamigration_service_busy"
		}
	case dataMigrationProjectType, dataMigrationMongoServiceType, dataMigrationSQLServiceType:
		if !slices.Contains([]string{"Succeeded", "Failed", "Canceled"}, member.state) {
			return "azure_datamigration_resource_busy"
		}
	case dataMigrationTaskType, dataMigrationServiceTaskType:
		if !slices.Contains([]string{"Queued", "Running", "Canceled", "Succeeded", "Failed", "FailedInputValidation", "Faulted"}, member.state) {
			return "azure_datamigration_task_state_unverified"
		}
	case dataMigrationType:
		if text(props["migrationOperationId"]) == "" || !uuidPattern.MatchString(text(props["migrationOperationId"])) {
			return "azure_datamigration_operation_unverified"
		}
		if !slices.Contains([]string{"InProgress", "Succeeded", "Failed", "Canceled", "Canceling"}, member.state) {
			return "azure_datamigration_migration_state_unverified"
		}
		if !slices.Contains([]string{"Succeeded", "Failed", "Canceled", "Canceling"}, text(props["provisioningState"])) {
			return "azure_datamigration_migration_busy"
		}
	}
	return ""
}

func (c *client) dataMigrationNodeManifest(raw map[string]any) map[string]any {
	out := map[string]any{}
	for _, value := range array(dataMigrationNodesSnapshot(raw)["nodes"]) {
		node := object(value)
		out[text(node["nodeName"])] = map[string]any{"runtime": raw["name"], "configuration": c.privateConfiguration(node)}
	}
	return out
}

func (r *Runtime) dataMigrationItems(ctx context.Context, c *client, connection asset.ConnectionID, forest dataMigrationForest) (map[string]contracts.InventoryItem, map[string]any, error) {
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
	for id, member := range forest.members {
		targetID, _ := dataMigrationTarget(id)
		member.refs, err = dataMigrationReferences(member, forest.targets[targetID])
		if err != nil {
			return nil, nil, err
		}
		forest.members[id] = member
	}
	for _, id := range slices.Sorted(maps.Keys(forest.members)) {
		member := forest.members[id]
		root := forest.members[member.service]
		if root.id == "" {
			return nil, nil, serviceDenied("datamigration_inventory_service_missing")
		}
		location := resourceRegion(root.raw)
		if location == "global" {
			return nil, nil, serviceDenied("datamigration_inventory_region_unverified")
		}
		safe := safePayload(object(dataMigrationSafeValue(member.raw)))
		normalized := map[string]any{"name": last(id), "state": member.state, "tags": safe["tags"], "subscription_id": c.subscription, "resource_group": strings.Split(id, "/")[4], "_inventory_source": dataMigrationInventorySource, "_datamigration_connection": string(connection), "_datamigration_service": root.id, "_datamigration_parent": member.parent, "_datamigration_location": location, dataMigrationConfiguration: c.privateConfiguration(dataMigrationSnapshot(member.kind, member.raw))}
		maps.Copy(normalized, object(safe["properties"]))
		normalized["state"] = member.state
		ancestors, members, dependents, groupBindings, target := map[string]any{}, map[string]any{}, map[string]any{}, map[string]any{}, map[string]any{}
		reason := dataMigrationProtection(member)
		for parent := member.parent; parent != ""; parent = forest.members[parent].parent {
			ancestors[parent] = c.privateConfiguration(dataMigrationSnapshot(forest.members[parent].kind, forest.members[parent].raw))
		}
		if member.service != id {
			ancestors[member.service] = c.privateConfiguration(dataMigrationSnapshot(root.kind, root.raw))
		}
		for ancestor := range ancestors {
			if protection := dataMigrationProtection(forest.members[ancestor]); protection != "" {
				reason = protection
			}
			if locked(ancestor, locks) {
				reason = "azure_management_lock"
			}
		}
		for childID, child := range forest.members {
			entry := map[string]any{"kind": child.kind, "parent": child.parent, "configuration": c.privateConfiguration(dataMigrationSnapshot(child.kind, child.raw))}
			if dataMigrationClassic(member.kind) && strings.HasPrefix(childID, id+"/") {
				members[childID] = entry
			}
			if child.kind == dataMigrationType && child.service == id {
				dependents[childID] = entry
			}
			if member.kind == dataMigrationFileType && slices.Contains(child.refs[dataMigrationFileType], id) {
				dependents[childID] = entry
			}
		}
		if member.kind == dataMigrationType {
			targetID, _ := dataMigrationTarget(id)
			raw := forest.targets[targetID]
			if raw == nil {
				return nil, nil, serviceDenied("datamigration_inventory_target_missing")
			}
			target = map[string]any{"id": targetID, "configuration": c.privateConfiguration(insightsWorkspaceResourceSnapshot(raw))}
			if protectedAzureTags(object(raw["tags"])) {
				reason = "azure_protected_tag"
			}
			if text(raw["managedBy"]) != "" {
				reason = "azure_managed_resource"
			}
		}
		for _, resource := range []string{id, root.id} {
			groupID := strings.Join(strings.Split(resource, "/")[:5], "/")
			if verified[groupID] == nil {
				if groups[groupID] == nil {
					return nil, nil, serviceDenied("datamigration_group_missing_from_index")
				}
				verified[groupID], err = c.insightsGroup(ctx, groupID, groups[groupID])
				if err != nil {
					return nil, nil, err
				}
			}
			group := verified[groupID]
			groupBindings[groupID] = c.privateConfiguration(insightsWorkspaceResourceSnapshot(group))
			if text(group["managedBy"]) != "" {
				reason = "azure_managed_resource_group"
			}
			if protectedAzureTags(object(group["tags"])) {
				reason = "azure_protected_tag"
			}
		}
		normalized["_datamigration_ancestors"], normalized[dataMigrationMembers], normalized["_datamigration_dependents"], normalized["_datamigration_groups"], normalized["_datamigration_target"] = ancestors, members, dependents, groupBindings, target
		normalized["_datamigration_nodes"] = c.dataMigrationNodeManifest(forest.nodes[id])
		normalized["_datamigration_references"] = monitorReferenceProjection(member.refs)
		network := []string{}
		for kind, ids := range member.refs {
			normalized[referenceKey(kind)] = ids
			network = append(network, ids...)
		}
		slices.Sort(network)
		mapping, _ := findType(member.kind)
		_, parameters, err := c.resourceOperation(mapping, id, "GET")
		if err != nil {
			return nil, nil, err
		}
		normalized["arm_parameters"], normalized["arm_etag"] = parameters, text(member.raw["etag"])
		if locked(id, locks) {
			reason = "azure_management_lock"
		}
		normalized["cleanup_protected"], normalized["cleanup_protection_reason"] = reason != "", reason
		normalized[dataMigrationProof] = c.dataMigrationBinding(id, member.kind, normalized)
		if err := c.rbacIdentityInventory(id, member.kind, member.raw, normalized); err != nil {
			return nil, nil, err
		}
		tags := map[string]string{}
		for key, value := range object(safe["tags"]) {
			if str, ok := value.(string); ok {
				tags[key] = str
			}
		}
		actionable := reason == ""
		items[id] = contracts.InventoryItem{NativeID: id, NativeType: member.kind, ResourceKind: r.resourceKind(member.kind), Actionable: &actionable, Scope: contracts.InventoryScope{Kind: asset.ScopeRegion, NativeID: location, Name: location, Location: location}, Name: last(id), State: member.state, Location: location, Tags: tags, Normalized: normalized, Raw: safe, NetworkReferences: network, NativeAliases: []string{id}}
		bindings[id] = map[string]any{"proof": normalized[dataMigrationProof], "state": member.state}
	}
	return items, bindings, nil
}

func (r *Runtime) dataMigrationInventorySnapshot(ctx context.Context, c *client, request contracts.InventoryRequest) ([]contracts.InventoryItem, []string, map[string]any, error) {
	hints, err := c.dataMigrationKnown(request)
	if err != nil {
		return nil, nil, nil, err
	}
	var forest dataMigrationForest
	if dataMigrationClassic(request.ResourceKind.NativeType) {
		forest, err = c.dataMigrationClassicForest(ctx, hints)
	} else {
		forest, err = c.dataMigrationModernForest(ctx, hints)
	}
	if err != nil {
		return nil, nil, nil, err
	}
	all, bindings, err := r.dataMigrationItems(ctx, c, request.ConnectionID, forest)
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
		if forest.missing[id] {
			absent = append(absent, id)
		}
	}
	slices.Sort(absent)
	return items, absent, bindings, nil
}

func (r *Runtime) listDataMigration(ctx context.Context, c *client, request contracts.InventoryRequest) (batch contracts.InventoryBatch, err error) {
	defer func() { err = contracts.DependencyReadError(err) }()
	if request.Source != dataMigrationInventorySource || request.ResourceKind == nil || dataMigrationKind(request.ResourceKind.NativeType) == "" || dataMigrationKind(request.ResourceKind.NativeType) != request.ResourceKind.NativeType || len(request.Options) != 0 {
		return batch, serviceDenied("invalid_datamigration_inventory_source")
	}
	switch request.Scope.Kind {
	case asset.ScopeSubscription:
		if !strings.EqualFold(request.Scope.NativeID, c.subscription) {
			return batch, serviceDenied("datamigration_inventory_subscription_changed")
		}
	case asset.ScopeRegion:
		if request.Scope.NativeID == "" || request.Scope.NativeID != strings.TrimSpace(request.Scope.NativeID) {
			return batch, serviceDenied("invalid_datamigration_inventory_region")
		}
	default:
		return batch, serviceDenied("invalid_datamigration_inventory_scope")
	}
	cursor := productCursor{}
	if request.Cursor != "" {
		if len(request.Cursor) > 128<<10 {
			return batch, serviceDenied("datamigration_inventory_cursor_too_large")
		}
		payload, e := base64.RawURLEncoding.DecodeString(request.Cursor)
		if e != nil || json.Unmarshal(payload, &cursor) != nil || cursor.Target < 1 || cursor.Fingerprint == "" || cursor.Next != "" || len(cursor.Seen)+len(cursor.Resources) != 0 {
			return batch, serviceDenied("invalid_datamigration_inventory_cursor")
		}
	}
	items, absent, before, err := r.dataMigrationInventorySnapshot(ctx, c, request)
	if err != nil {
		return batch, err
	}
	_, afterAbsent, after, err := r.dataMigrationInventorySnapshot(ctx, c, request)
	if err != nil {
		return batch, err
	}
	if !slices.Equal(absent, afterAbsent) || c.privateConfiguration(before) != c.privateConfiguration(after) {
		return batch, serviceDenied("datamigration_collection_changed_during_scan")
	}
	boundary := request
	boundary.Cursor = ""
	boundary.Limit = 0
	fingerprint := c.privateConfiguration(map[string]any{"request": boundary, "revision": r.bundle.Revision, "bindings": before, "absent": absent})
	if request.Cursor != "" && (cursor.Fingerprint != fingerprint || cursor.Target >= len(items)) {
		return batch, serviceDenied("datamigration_inventory_cursor_changed")
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
