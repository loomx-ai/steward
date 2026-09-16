package azure

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"maps"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

const dataProtectionVersion = "2026-03-01"
const dataProtectionVaultVersion = "2026-06-01"

func dataProtectionReadVersion(kind string) string {
	if kind == dataProtectionVault || kind == dataProtectionGuardProxy {
		return dataProtectionVaultVersion
	}
	return dataProtectionVersion
}

const dataProtectionSource = "data-protection"
const dataProtectionVault = "Microsoft.DataProtection/backupVaults"
const dataProtectionPolicy = dataProtectionVault + "/backupPolicies"
const dataProtectionInstance = dataProtectionVault + "/backupInstances"
const dataProtectionDeletedInstance = dataProtectionVault + "/deletedBackupInstances"
const dataProtectionDeletedVault = "Microsoft.DataProtection/locations/deletedVaults"

func dataProtectionKind(kind string) string {
	for _, candidate := range []string{dataProtectionVault, dataProtectionPolicy, dataProtectionInstance, dataProtectionDeletedInstance, dataProtectionDeletedVault} {
		if strings.EqualFold(kind, candidate) {
			return candidate
		}
	}
	return ""
}

// The upstream deleted-vault examples use deletedBackupVaults in response IDs,
// while the operation path is deletedVaults. Only that exact leaf alias is
// normalized; the subscription, location and deletion identity stay unchanged.
func (c *client) dataProtectionIdentity(value, kind string) (string, error) {
	id := strings.ToLower(value)
	if value != strings.TrimSpace(value) || dataProtectionKind(kind) == "" && kind != dataProtectionGuardProxy {
		return "", serviceDenied("invalid_data_protection_identity")
	}
	if kind == dataProtectionDeletedVault {
		parts := strings.Split(id, "/")
		if len(parts) != 9 || strings.Join(parts[:6], "/") != c.root()+"/providers/microsoft.dataprotection/locations" || !cosmosOperationRegion.MatchString(parts[6]) || (parts[7] != "deletedvaults" && parts[7] != "deletedbackupvaults") || parts[8] == "" || parts[8] == "." || parts[8] == ".." || strings.ContainsAny(id, "%?#\\\x00\r\n") {
			return "", serviceDenied("invalid_deleted_backup_vault_identity")
		}
		parts[7] = "deletedvaults"
		return strings.Join(parts, "/"), nil
	}
	canonical, typ, err := parseID(value)
	n := 11
	if kind == dataProtectionVault {
		n = 9
	}
	if err != nil || typ != strings.ToLower(kind) || len(strings.Split(canonical, "/")) != n || !strings.HasPrefix(canonical, c.root()+"/") {
		return "", serviceDenied("invalid_data_protection_identity")
	}
	return canonical, nil
}

func (c *client) dataProtectionMetadata(raw map[string]any, id, kind string) error {
	actual, err := c.dataProtectionIdentity(text(raw["id"]), kind)
	typ := text(raw["type"])
	validType := strings.EqualFold(typ, kind)
	if kind == dataProtectionGuardProxy {
		// The native example uses vaults in type, while its ID uses backupVaults.
		validType = validType || strings.EqualFold(typ, "Microsoft.DataProtection/vaults/backupResourceGuardProxies")
	}
	if kind == dataProtectionDeletedVault {
		validType = validType || strings.EqualFold(typ, "Microsoft.DataProtection/deletedBackupVaults")
	}
	props := object(raw["properties"])
	if err != nil || actual != id || !validType || !strings.EqualFold(text(raw["name"]), last(id)) || props == nil {
		return serviceDenied("invalid_data_protection_metadata")
	}
	switch kind {
	case dataProtectionVault:
		if !cosmosOperationRegion.MatchString(resourceRegion(raw)) {
			return serviceDenied("invalid_backup_vault_location")
		}
	case dataProtectionPolicy:
		if text(props["objectType"]) == "" {
			return serviceDenied("invalid_backup_policy")
		}
	case dataProtectionInstance, dataProtectionDeletedInstance:
		if text(props["objectType"]) == "" || object(props["dataSourceInfo"]) == nil || object(props["policyInfo"]) == nil {
			return serviceDenied("invalid_backup_instance")
		}
		policy, err := c.dataProtectionIdentity(text(object(props["policyInfo"])["policyId"]), dataProtectionPolicy)
		if err != nil || redisParentID(policy) != redisParentID(id) {
			return serviceDenied("invalid_backup_instance_policy")
		}
	case dataProtectionGuardProxy:
		guard := text(props["resourceGuardResourceId"])
		_, guardKind, err := parseID(guard)
		if err != nil || guard != strings.TrimSpace(guard) || guardKind != "microsoft.dataprotection/resourceguards" || len(strings.Split(guard, "/")) != 9 {
			return serviceDenied("invalid_backup_resource_guard_reference")
		}
	case dataProtectionDeletedVault:
		original, err := c.dataProtectionIdentity(text(props["originalBackupVaultId"]), dataProtectionVault)
		if err != nil || !strings.EqualFold(text(props["originalBackupVaultResourcePath"]), original) || !strings.EqualFold(text(props["originalBackupVaultName"]), last(original)) {
			return serviceDenied("invalid_deleted_vault_origin")
		}
		info := object(props["resourceDeletionInfo"])
		for _, key := range []string{"deletionTime", "scheduledPurgeTime"} {
			stamp, err := time.Parse(time.RFC3339Nano, text(info[key]))
			if err != nil || stamp.IsZero() {
				return serviceDenied("invalid_deleted_vault_time")
			}
		}
	}
	return nil
}

func (c *client) dataProtectionRead(ctx context.Context, id, kind string) (response, error) {
	canonical, err := c.dataProtectionIdentity(id, kind)
	if err != nil {
		return response{}, err
	}
	res, err := c.request(ctx, "GET", apiURL(canonical, dataProtectionReadVersion(kind)))
	if err != nil {
		var call *contracts.ProviderCallError
		if isNotFound(err) && errors.As(err, &call) && slices.Contains([]string{"ParentResourceNotFound", "ResourceGroupNotFound", "SubscriptionNotFound"}, call.Provider.Code) {
			err = contracts.DependencyReadError(err)
		}
		return res, err
	}
	if res.status != 200 || res.data["error"] != nil || operationLocation(res.header) != "" {
		return res, serviceDenied("incomplete_data_protection_read")
	}
	return res, c.dataProtectionMetadata(res.data, canonical, kind)
}

func (c *client) dataProtectionCollection(ctx context.Context, path, kind string) (map[string]map[string]any, error) {
	rows := map[string]map[string]any{}
	seen := map[string]bool{}
	version := dataProtectionReadVersion(kind)
	next := apiURL(path, version)
	for next != "" {
		u, err := url.Parse(next)
		if err != nil || seen[next] {
			return nil, serviceDenied("invalid_data_protection_page")
		}
		query, err := url.ParseQuery(u.RawQuery)
		if err != nil || query.Get("api-version") != version {
			return nil, serviceDenied("invalid_data_protection_page_version")
		}
		for key, values := range query {
			if len(values) != 1 || key != "api-version" && key != "$skiptoken" && key != "$skipToken" && key != "skipToken" {
				return nil, serviceDenied("filtered_data_protection_page")
			}
		}
		seen[next] = true
		values, following, res, err := c.listPageResult(ctx, next, path)
		if err != nil {
			return nil, contracts.DependencyReadError(err)
		}
		if operationLocation(res.header) != "" {
			return nil, serviceDenied("incomplete_data_protection_page")
		}
		for _, value := range values {
			raw := object(value)
			id, err := c.dataProtectionIdentity(text(raw["id"]), kind)
			if err != nil {
				return nil, err
			}
			if err = c.dataProtectionMetadata(raw, id, kind); err != nil {
				return nil, err
			}
			if rows[id] != nil || kind != dataProtectionVault && !strings.EqualFold(redisParentID(id)+"/"+last(kind), path) {
				return nil, serviceDenied("invalid_data_protection_member")
			}
			rows[id] = raw
		}
		next = following
	}
	return rows, nil
}

// Use an allowlist because native instance details may contain data-source
// credentials and arbitrary workload configuration, including future fields.
func dataProtectionSafeValue(value any) any {
	switch v := value.(type) {
	case map[string]any:
		out := map[string]any{}
		for key, child := range v {
			switch key {
			case "id", "name", "type", "location", "state", "provisioningState", "currentProtectionState", "objectType", "policyId", "resourceID", "originalBackupVaultId", "originalBackupVaultName", "originalBackupVaultResourcePath", "deletionTime", "scheduledPurgeTime", "deleteActivityId", "request_id", "status_code", "method", "path", "api-version", "code":
				switch child.(type) {
				case string, bool, float64, int, json.Number, nil:
					out[key] = child
				default:
					out[key] = dataProtectionSafeValue(child)
				}
			case "body", "properties", "value", "query", "error", "policyInfo", "dataSourceInfo", "resourceDeletionInfo", "deletionInfo", "securitySettings", "softDeleteSettings", "immutabilitySettings":
				out[key] = dataProtectionSafeValue(child)
			default:
				out[key] = "[REDACTED]"
			}
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, child := range v {
			out[i] = dataProtectionSafeValue(child)
		}
		return out
	default:
		return "[REDACTED]"
	}
}

func (r *Runtime) dataProtectionSnapshot(ctx context.Context, c *client, req contracts.InventoryRequest, kind string) ([]contracts.InventoryItem, []string, error) {
	known := map[string]bool{}
	for _, value := range req.KnownNativeIDs {
		id, err := c.dataProtectionIdentity(value, kind)
		if err != nil {
			return nil, nil, err
		}
		// The scan worker supplies known IDs across all regions. A regional
		// deleted-vault shard must not reconcile another region's tombstones.
		if kind == dataProtectionDeletedVault && req.Scope.Kind == asset.ScopeRegion && strings.Split(id, "/")[6] != strings.ToLower(req.Scope.NativeID) {
			continue
		}
		known[id] = true
	}
	targets := map[string]string{}
	parents := map[string]map[string]any{}
	if kind == dataProtectionDeletedVault {
		locations := []string{strings.ToLower(req.Scope.NativeID)}
		if req.Scope.Kind == asset.ScopeSubscription {
			discovered, err := r.DiscoverRegions(ctx, req.ConnectionID)
			if err != nil {
				return nil, nil, err
			}
			locations = nil
			for _, region := range discovered {
				locations = append(locations, region.RegionID)
			}
			for id := range known {
				locations = append(locations, strings.Split(id, "/")[6])
			}
		}
		for _, location := range locations {
			if !cosmosOperationRegion.MatchString(location) {
				return nil, nil, serviceDenied("invalid_deleted_vault_location")
			}
			targets[c.root()+"/providers/microsoft.dataprotection/locations/"+location+"/deletedvaults"] = location
		}
	} else if kind == dataProtectionVault {
		targets[c.root()+"/providers/microsoft.dataprotection/backupvaults"] = ""
	} else {
		vaults, err := c.dataProtectionCollection(ctx, c.root()+"/providers/microsoft.dataprotection/backupvaults", dataProtectionVault)
		if err != nil {
			return nil, nil, err
		}
		for id := range known {
			parent := redisParentID(id)
			if vaults[parent] == nil {
				vaults[parent] = map[string]any{}
			}
		}
		for _, id := range slices.Sorted(maps.Keys(vaults)) {
			own, err := c.dataProtectionRead(ctx, id, dataProtectionVault)
			if err != nil {
				return nil, nil, contracts.DependencyReadError(err)
			}
			if len(vaults[id]) != 0 && !nativeConfigurationContains(vaults[id], own.data) {
				return nil, nil, serviceDenied("backup_vault_index_changed")
			}
			location := resourceRegion(own.data)
			if req.Scope.Kind == asset.ScopeRegion && !strings.EqualFold(location, req.Scope.NativeID) {
				continue
			}
			parents[id] = own.data
			targets[id+"/"+strings.ToLower(last(kind))] = location
		}
	}
	rows := map[string]map[string]any{}
	locations := map[string]string{}
	for _, path := range slices.Sorted(maps.Keys(targets)) {
		listed, err := c.dataProtectionCollection(ctx, path, kind)
		if err != nil {
			return nil, nil, err
		}
		for id, raw := range listed {
			if rows[id] != nil {
				return nil, nil, serviceDenied("duplicate_data_protection_resource")
			}
			rows[id] = raw
			locations[id] = targets[path]
		}
	}
	for id := range known {
		if kind == dataProtectionDeletedVault && req.Scope.Kind == asset.ScopeRegion && strings.Split(id, "/")[6] != strings.ToLower(req.Scope.NativeID) {
			return nil, nil, serviceDenied("foreign_deleted_vault_hint")
		}
		if kind != dataProtectionVault && kind != dataProtectionDeletedVault && parents[redisParentID(id)] == nil {
			continue
		}
		if rows[id] == nil {
			rows[id] = map[string]any{}
		}
		if kind == dataProtectionDeletedVault {
			locations[id] = strings.Split(id, "/")[6]
		} else if kind != dataProtectionVault {
			locations[id] = resourceRegion(parents[redisParentID(id)])
		}
	}
	items := []contracts.InventoryItem{}
	absent := []string{}
	for _, id := range slices.Sorted(maps.Keys(rows)) {
		own, err := c.dataProtectionRead(ctx, id, kind)
		if isNotFound(err) && len(rows[id]) == 0 {
			absent = append(absent, id)
			continue
		}
		if err != nil {
			return nil, nil, contracts.DependencyReadError(err)
		}
		// Compare native representations, not the canonical URL alias.
		if len(rows[id]) != 0 && !nativeConfigurationContains(rows[id], own.data) {
			return nil, nil, serviceDenied("data_protection_listed_configuration_changed")
		}
		location := locations[id]
		if kind == dataProtectionVault {
			location = resourceRegion(own.data)
		}
		if req.Scope.Kind == asset.ScopeRegion && !strings.EqualFold(location, req.Scope.NativeID) {
			continue
		}
		props := object(own.data["properties"])
		state := text(props["provisioningState"])
		if kind == dataProtectionDeletedVault || kind == dataProtectionDeletedInstance {
			state = "soft_deleted"
		} else if kind == dataProtectionInstance {
			state = text(props["currentProtectionState"])
		}
		normalized := map[string]any{"name": last(id), "state": state, "subscriptionId": c.subscription, "_inventory_source": dataProtectionSource, "_data_protection_configuration": c.privateConfiguration(own.data), "cleanup_protected": true, "cleanup_protection_reason": "data_protection_cleanup_not_implemented"}
		if kind != dataProtectionDeletedVault {
			normalized["resourceGroup"] = strings.Split(id, "/")[4]
		}
		if kind != dataProtectionVault && kind != dataProtectionDeletedVault {
			parent := redisParentID(id)
			normalized["vaultId"] = parent
			normalized[referenceKey(dataProtectionVault)] = []string{parent}
			normalized["_data_protection_parent_configuration"] = c.privateConfiguration(parents[parent])
		}
		if kind == dataProtectionInstance || kind == dataProtectionDeletedInstance {
			policy, _ := c.dataProtectionIdentity(text(object(props["policyInfo"])["policyId"]), dataProtectionPolicy)
			normalized["policyId"] = policy
			normalized[referenceKey(dataProtectionPolicy)] = []string{policy}
		}
		if kind == dataProtectionDeletedVault {
			normalized["originalVaultId"] = strings.ToLower(text(props["originalBackupVaultId"]))
			info := object(props["resourceDeletionInfo"])
			normalized["deletionTime"] = info["deletionTime"]
			normalized["scheduledPurgeTime"] = info["scheduledPurgeTime"]
		}
		actionable := false
		items = append(items, contracts.InventoryItem{NativeID: id, NativeType: kind, ResourceKind: r.resourceKind(kind), Name: last(id), State: state, Location: location, Scope: contracts.InventoryScope{Kind: asset.ScopeRegion, NativeID: location, Name: location, Location: location}, Normalized: normalized, Raw: object(dataProtectionSafeValue(own.data)), NativeAliases: []string{id}, Actionable: &actionable})
	}
	if kind == dataProtectionVault {
		for i := range items {
			if err := r.dataProtectionVaultInventory(ctx, c, req, &items[i]); err != nil {
				return nil, nil, err
			}
		}
	}
	if kind == dataProtectionInstance {
		for i := range items {
			if err := r.protectionInstanceInventory(ctx, c, req, &items[i]); err != nil {
				return nil, nil, err
			}
		}
	}
	if kind == dataProtectionPolicy {
		for i := range items {
			if err := r.protectionPolicyInventory(ctx, c, req, &items[i]); err != nil {
				return nil, nil, err
			}
		}
	}
	for id, raw := range parents {
		own, err := c.dataProtectionRead(ctx, id, dataProtectionVault)
		if err != nil {
			return nil, nil, contracts.DependencyReadError(err)
		}
		if c.privateConfiguration(raw) != c.privateConfiguration(own.data) {
			return nil, nil, serviceDenied("backup_vault_changed_during_inventory")
		}
	}
	return items, absent, nil
}

func (r *Runtime) listDataProtection(ctx context.Context, c *client, req contracts.InventoryRequest) (batch contracts.InventoryBatch, err error) {
	defer func() { err = contracts.DependencyReadError(err) }()
	kind := ""
	if req.ResourceKind != nil {
		kind = dataProtectionKind(req.ResourceKind.NativeType)
	}
	if kind == "" || req.Source != dataProtectionSource || len(req.Options) != 0 || req.NetworkTarget != nil || !(req.Scope.Kind == asset.ScopeSubscription && strings.EqualFold(req.Scope.NativeID, c.subscription) || req.Scope.Kind == asset.ScopeRegion && cosmosOperationRegion.MatchString(req.Scope.NativeID)) {
		return batch, serviceDenied("invalid_data_protection_inventory_request")
	}
	cursor := productCursor{}
	if req.Cursor != "" {
		wire, e := base64.RawURLEncoding.DecodeString(req.Cursor)
		if e != nil || len(req.Cursor) > 128<<10 || json.Unmarshal(wire, &cursor) != nil || cursor.Target < 1 || cursor.Fingerprint == "" || cursor.Next != "" || len(cursor.Seen)+len(cursor.Resources) != 0 {
			return batch, serviceDenied("invalid_data_protection_cursor")
		}
	}
	first, absent, err := r.dataProtectionSnapshot(ctx, c, req, kind)
	if err != nil {
		return batch, err
	}
	second, gone, err := r.dataProtectionSnapshot(ctx, c, req, kind)
	if err != nil {
		return batch, err
	}
	observation := map[string]any{"items": first, "absent": absent}
	if c.privateConfiguration(observation) != c.privateConfiguration(map[string]any{"items": second, "absent": gone}) {
		return batch, serviceDenied("data_protection_snapshot_changed")
	}
	boundary := req
	boundary.Cursor = ""
	boundary.Limit = 0
	fingerprint := c.privateConfiguration(map[string]any{"request": boundary, "revision": r.bundle.Revision, "observation": observation})
	if cursor.Fingerprint != "" && cursor.Fingerprint != fingerprint || cursor.Target > len(first) {
		return batch, serviceDenied("data_protection_cursor_changed")
	}
	limit := req.Limit
	if limit <= 0 || limit > 500 {
		limit = 500
	}
	end := min(cursor.Target+limit, len(first))
	batch.Items = first[cursor.Target:end]
	batch.Complete = end == len(first)
	if batch.Complete {
		batch.AbsentNativeIDs = absent
	} else {
		wire, _ := json.Marshal(productCursor{Target: end, Fingerprint: fingerprint})
		batch.NextCursor = base64.RawURLEncoding.EncodeToString(wire)
	}
	return batch, nil
}
