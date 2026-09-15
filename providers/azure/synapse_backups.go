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

const synapseBackupSource = "synapse-backups"
const synapseDroppedType = synapseType + "/restorableDroppedSqlPools"
const synapseRestorePointType = synapseSQLType + "/restorePoints"

func synapseBackupKind(kind string) string {
	for _, candidate := range []string{synapseDroppedType, synapseRestorePointType} {
		if strings.EqualFold(kind, candidate) {
			return candidate
		}
	}
	return ""
}
func (c *client) backupIdentity(id, kind string) error {
	canonical, typ, err := parseID(id)
	n := 11
	if kind == synapseRestorePointType {
		n = 13
	}
	if err != nil || canonical != id || typ != strings.ToLower(kind) || synapseBackupKind(kind) == "" || len(strings.Split(id, "/")) != n || !strings.HasPrefix(id, c.root()+"/") {
		return serviceDenied("invalid_synapse_backup_identity")
	}
	return nil
}
func (c *client) backupMetadata(raw map[string]any, id, kind string) error {
	if err := c.backupIdentity(id, kind); err != nil {
		return err
	}
	if !strings.EqualFold(text(raw["id"]), id) || !strings.EqualFold(text(raw["type"]), kind) || !strings.EqualFold(text(raw["name"]), last(id)) || resourceRegion(raw) == "" || object(raw["properties"]) == nil {
		return serviceDenied("invalid_synapse_backup_metadata")
	}
	props := object(raw["properties"])
	for _, key := range []string{"databaseName", "restorePointLabel", "edition", "serviceLevelObjective", "maxSizeBytes"} {
		if v, present := props[key]; present && v != nil {
			if _, ok := v.(string); !ok {
				return serviceDenied("invalid_synapse_backup_property")
			}
		}
	}

	dates := []string{"creationDate", "deletionDate", "earliestRestoreDate"}
	if kind == synapseRestorePointType {
		dates = []string{"restorePointCreationDate", "earliestRestoreDate"}
		if value, present := props["restorePointType"]; present && value != nil && value != "DISCRETE" && value != "CONTINUOUS" {
			return serviceDenied("unknown_synapse_restore_point_type")
		}
	}
	for _, key := range dates {
		if value, present := props[key]; !present || value == nil {
			continue
		}
		stamp, err := time.Parse(time.RFC3339Nano, text(props[key]))
		if err != nil || stamp.IsZero() {
			return serviceDenied("invalid_synapse_backup_time")
		}
	}
	return nil
}
func (c *client) backupRead(ctx context.Context, id, kind string) (response, error) {
	if err := c.backupIdentity(id, kind); err != nil {
		return response{}, err
	}
	res, err := c.request(ctx, "GET", apiURL(id, synapseVersion))
	if err != nil {
		return res, err
	}
	if res.status != 200 || res.data["error"] != nil || operationLocation(res.header) != "" {
		return res, serviceDenied("invalid_synapse_backup_read")
	}
	return res, c.backupMetadata(res.data, id, kind)
}
func (c *client) backupCollection(ctx context.Context, parent, kind string) (map[string]map[string]any, string, error) {
	collection := parent + "/" + last(kind)
	next := apiURL(collection, synapseVersion)
	seen := map[string]bool{}
	rows := map[string]map[string]any{}
	requestID := ""
	for next != "" {
		if seen[next] {
			return nil, "", serviceDenied("synapse_backup_page_cycle")
		}
		seen[next] = true
		if err := c.synapseListQuery(next, collection); err != nil {
			return nil, "", err
		}
		values, following, res, err := c.listPageResult(ctx, next, collection)
		if err != nil {
			return nil, "", contracts.DependencyReadError(err)
		}
		if operationLocation(res.header) != "" {
			return nil, "", serviceDenied("synapse_backup_index_incomplete")
		}
		if following != "" {
			if err := c.synapseListQuery(following, collection); err != nil {
				return nil, "", err
			}
		}
		if res.requestID != "" {
			requestID = res.requestID
		}
		for _, v := range values {
			raw := object(v)
			id := strings.ToLower(text(raw["id"]))
			if err := c.backupMetadata(raw, id, kind); err != nil {
				return nil, "", err
			}
			if redisParentID(id) != parent || rows[id] != nil {
				return nil, "", serviceDenied("synapse_backup_index_identity_changed")
			}
			rows[id] = raw
		}
		next = following
	}
	return rows, requestID, nil
}

// A disappeared workspace/pool may make retained backups temporarily
// inaccessible. Only own absence under a currently readable parent is used;
// missing parents/collections fail the shard and preserve prior observations.
func (c *client) backupSnapshot(ctx context.Context, req contracts.InventoryRequest, kind string) (map[string]map[string]any, map[string]any, []string, string, error) {
	workspaces := map[string]map[string]any{}
	parents := map[string]map[string]any{}
	collection := c.root() + "/providers/Microsoft.Synapse/workspaces"
	next := apiURL(collection, synapseVersion)
	pages := map[string]bool{}
	for next != "" {
		if pages[next] {
			return nil, nil, nil, "", serviceDenied("synapse_backup_workspace_page_cycle")
		}
		pages[next] = true
		rows, following, _, err := c.synapsePage(ctx, next, collection, synapseType)
		if err != nil {
			return nil, nil, nil, "", err
		}
		for _, v := range rows {
			raw := object(v)
			id := strings.ToLower(text(raw["id"]))
			if workspaces[id] != nil {
				return nil, nil, nil, "", serviceDenied("synapse_backup_workspace_duplicate")
			}
			workspaces[id] = raw
		}
		next = following
	}
	for _, id := range req.KnownNativeIDs {
		workspace := strings.Join(strings.Split(id, "/")[:9], "/")
		if workspaces[workspace] == nil {
			workspaces[workspace] = map[string]any{}
		}
	}
	contextHashes := map[string]any{}
	for id, listed := range workspaces {
		own, err := c.synapseWorkspaceRead(ctx, id)
		if err != nil {
			return nil, nil, nil, "", contracts.DependencyReadError(err)
		}
		if len(listed) != 0 && !nativeConfigurationContains(synapseSnapshot(listed), synapseSnapshot(own.raw)) {
			return nil, nil, nil, "", serviceDenied("synapse_backup_workspace_changed")
		}
		workspaces[id] = own.raw
		if req.Scope.Kind == asset.ScopeRegion && !strings.EqualFold(resourceRegion(own.raw), req.Scope.NativeID) {
			continue
		}
		contextHashes[id] = c.privateConfiguration(synapseSnapshot(own.raw))
		if kind == synapseDroppedType {
			parents[id] = own.raw
			continue
		}
		pools := id + "/sqlPools"
		next = apiURL(pools, synapseVersion)
		seen := map[string]bool{}
		for next != "" {
			if seen[next] {
				return nil, nil, nil, "", serviceDenied("synapse_backup_pool_page_cycle")
			}
			seen[next] = true
			rows, following, _, err := c.synapsePage(ctx, next, pools, synapseSQLType)
			if err != nil {
				return nil, nil, nil, "", contracts.DependencyReadError(err)
			}
			for _, v := range rows {
				raw := object(v)
				pool := strings.ToLower(text(raw["id"]))
				if parents[pool] != nil {
					return nil, nil, nil, "", serviceDenied("synapse_backup_pool_duplicate")
				}
				parents[pool] = raw
			}
			next = following
		}
	}
	for _, id := range req.KnownNativeIDs {
		parent := redisParentID(id)
		workspace := strings.Join(strings.Split(id, "/")[:9], "/")
		if contextHashes[workspace] != nil && parents[parent] == nil {
			parents[parent] = map[string]any{}
		}
	}
	result := map[string]map[string]any{}
	absent := []string{}
	requestID := ""
	for parent, listed := range parents {
		parentKind := synapseType
		if kind == synapseRestorePointType {
			parentKind = synapseSQLType
		}
		own, err := c.request(ctx, "GET", apiURL(parent, synapseVersion))
		if err != nil {
			return nil, nil, nil, "", contracts.DependencyReadError(err)
		}
		if err = c.synapseReadResponse(own, parent, parentKind); err != nil {
			return nil, nil, nil, "", err
		}
		workspace := strings.Join(strings.Split(parent, "/")[:9], "/")
		if resourceRegion(own.data) != resourceRegion(workspaces[workspace]) || len(listed) != 0 && !nativeConfigurationContains(synapseSnapshot(listed), synapseSnapshot(own.data)) {
			return nil, nil, nil, "", serviceDenied("synapse_backup_parent_changed")
		}
		parents[parent] = own.data
		contextHashes[parent] = c.privateConfiguration(synapseSnapshot(own.data))
		index, rid, err := c.backupCollection(ctx, parent, kind)
		if err != nil {
			return nil, nil, nil, "", err
		}
		if rid != "" {
			requestID = rid
		}
		for _, id := range req.KnownNativeIDs {
			if redisParentID(id) == parent && index[id] == nil {
				index[id] = map[string]any{}
			}
		}
		for id, raw := range index {
			res, err := c.backupRead(ctx, id, kind)
			if isNotFound(err) {
				if slices.Contains(req.KnownNativeIDs, id) {
					absent = append(absent, id)
				}
				continue
			}
			if err != nil {
				return nil, nil, nil, "", err
			}
			if resourceRegion(res.data) != resourceRegion(own.data) || len(raw) != 0 && !nativeConfigurationContains(synapseSnapshot(raw), synapseSnapshot(res.data)) {
				return nil, nil, nil, "", serviceDenied("synapse_backup_detail_changed")
			}
			result[id] = res.data
			if res.requestID != "" {
				requestID = res.requestID
			}
		}
	}
	for id, hash := range contextHashes {
		typ := synapseType
		if len(strings.Split(id, "/")) == 11 {
			typ = synapseSQLType
		}
		own, err := c.request(ctx, "GET", apiURL(id, synapseVersion))
		if err != nil {
			return nil, nil, nil, "", contracts.DependencyReadError(err)
		}
		if err = c.synapseReadResponse(own, id, typ); err != nil {
			return nil, nil, nil, "", err
		}
		if hash != c.privateConfiguration(synapseSnapshot(own.data)) {
			return nil, nil, nil, "", serviceDenied("synapse_backup_parent_changed")
		}
	}
	slices.Sort(absent)
	return result, contextHashes, absent, requestID, nil
}
func (r *Runtime) listSynapseBackups(ctx context.Context, c *client, req contracts.InventoryRequest) (batch contracts.InventoryBatch, err error) {
	defer func() { err = contracts.DependencyReadError(err) }()
	if req.ResourceKind == nil || synapseBackupKind(req.ResourceKind.NativeType) == "" || req.Source != synapseBackupSource || len(req.Options) != 0 || req.Scope.Kind != asset.ScopeRegion && req.Scope.Kind != asset.ScopeSubscription {
		return batch, serviceDenied("invalid_synapse_backup_request")
	}
	kind := synapseBackupKind(req.ResourceKind.NativeType)
	known := map[string]bool{}
	for _, id := range req.KnownNativeIDs {
		if err = c.backupIdentity(id, kind); err != nil {
			return batch, err
		}
		if known[id] {
			return batch, serviceDenied("duplicate_synapse_backup_hint")
		}
		known[id] = true
	}
	for id := range req.KnownNativeMetadata {
		if !known[id] {
			return batch, serviceDenied("unrelated_synapse_backup_hint")
		}
	}
	cursor := productCursor{}
	if req.Cursor != "" {
		wire, e := base64.RawURLEncoding.DecodeString(req.Cursor)
		if e != nil || len(req.Cursor) > 128<<10 || json.Unmarshal(wire, &cursor) != nil || cursor.Target < 1 || cursor.Fingerprint == "" || cursor.Next != "" || len(cursor.Seen)+len(cursor.Resources) != 0 {
			return batch, serviceDenied("invalid_synapse_backup_cursor")
		}
	}
	first, parents, absent, rid, err := c.backupSnapshot(ctx, req, kind)
	if err != nil {
		return batch, err
	}
	second, again, missing, _, err := c.backupSnapshot(ctx, req, kind)
	if err != nil {
		return batch, err
	}
	if c.privateConfiguration(map[string]any{"resources": first}) != c.privateConfiguration(map[string]any{"resources": second}) || c.privateConfiguration(parents) != c.privateConfiguration(again) || !slices.Equal(absent, missing) {
		return batch, serviceDenied("synapse_backup_snapshot_changed")
	}
	items := []contracts.InventoryItem{}
	for _, id := range slices.Sorted(maps.Keys(first)) {
		raw := first[id]
		props := object(raw["properties"])
		workspace := strings.Join(strings.Split(id, "/")[:9], "/")
		region := resourceRegion(raw)
		normalized := map[string]any{"name": raw["name"], "state": "Retained", "subscription_id": c.subscription, "resource_group": strings.Split(id, "/")[4], "_synapse_workspace": workspace, "_synapse_backup_parent": redisParentID(id), "_synapse_backup_configuration": c.privateConfiguration(synapseSnapshot(raw)), "cleanup_protected": true, "cleanup_protection_reason": "synapse_backup_retained"}
		safeProps := map[string]any{}
		for _, key := range []string{"databaseName", "creationDate", "deletionDate", "earliestRestoreDate", "restorePointType", "restorePointCreationDate", "restorePointLabel", "edition", "serviceLevelObjective", "maxSizeBytes"} {
			if v, ok := props[key]; ok {
				normalized[key] = v
				safeProps[key] = v
			}
		}
		normalized[referenceKey(synapseType)] = []string{workspace}
		network := []string{workspace}
		if kind == synapseRestorePointType {
			normalized[referenceKey(synapseSQLType)] = []string{redisParentID(id)}
			network = append(network, redisParentID(id))
		}
		actionable := false
		safe := map[string]any{"id": id, "type": kind, "name": raw["name"], "location": region, "properties": safeProps}
		items = append(items, contracts.InventoryItem{NativeID: id, NativeType: kind, ResourceKind: r.resourceKind(kind), Scope: contracts.InventoryScope{Kind: asset.ScopeRegion, NativeID: region, Name: region, Location: region}, Name: text(raw["name"]), State: "Retained", Location: region, Tags: map[string]string{}, Raw: safe, Normalized: normalized, NativeAliases: []string{id}, NetworkReferences: network, Actionable: &actionable})
	}
	boundary := req
	boundary.Cursor, boundary.Limit = "", 0
	fingerprint := c.privateConfiguration(map[string]any{"request": boundary, "revision": r.bundle.Revision, "items": items, "parents": parents, "absent": absent})
	if req.Cursor != "" && (cursor.Fingerprint != fingerprint || cursor.Target >= len(items)) {
		return batch, serviceDenied("synapse_backup_cursor_changed")
	}
	limit := req.Limit
	if limit <= 0 {
		limit = 1000
	}
	end := cursor.Target + min(limit, len(items)-cursor.Target)
	batch = contracts.InventoryBatch{Items: items[cursor.Target:end], Complete: end == len(items), RequestID: rid}
	if batch.Complete {
		batch.AbsentNativeIDs = absent
	} else {
		cursor.Target, cursor.Fingerprint = end, fingerprint
		wire, _ := json.Marshal(cursor)
		batch.NextCursor = base64.RawURLEncoding.EncodeToString(wire)
	}
	return batch, nil
}
