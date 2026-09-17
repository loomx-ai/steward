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

const recoveryServicesVaultVersion = "2026-07-01"
const recoveryServicesBackupVersion = "2026-08-01"
const recoveryServicesSource = "recovery-services"
const recoveryServicesVault = "Microsoft.RecoveryServices/vaults"
const recoveryServicesContainer = recoveryServicesVault + "/backupFabrics/protectionContainers"
const recoveryServicesItem = recoveryServicesContainer + "/protectedItems"
const recoveryServicesDeletedVault = "Microsoft.RecoveryServices/locations/deletedVaults"

// Site Recovery replication is inventoried read-only. Disabling replication
// removes the replica disks at the recovery location, and purge only removes
// the vault record; neither is exposed as cleanup.
const siteRecoveryVersion = "2026-07-01"
const siteRecoveryItem = recoveryServicesVault + "/replicationFabrics/replicationProtectionContainers/replicationProtectedItems"

func recoveryServicesKind(kind string) string {
	for _, candidate := range []string{recoveryServicesVault, recoveryServicesContainer, recoveryServicesItem, recoveryServicesDeletedVault, siteRecoveryItem} {
		if strings.EqualFold(kind, candidate) {
			return candidate
		}
	}
	return ""
}

func recoveryServicesVersion(kind string) string {
	if kind == recoveryServicesVault || kind == recoveryServicesDeletedVault {
		return recoveryServicesVaultVersion
	}
	if kind == siteRecoveryItem {
		return siteRecoveryVersion
	}
	return recoveryServicesBackupVersion
}

// Containers and items retain the complete native fabric hierarchy, including
// semicolon-bearing workload names. Never infer a fabric from a workload label.
func (c *client) recoveryServicesIdentity(value, kind string) (string, error) {
	if value != strings.TrimSpace(value) || recoveryServicesKind(kind) == "" || strings.ContainsAny(value, "\t\x00\r\n") {
		return "", serviceDenied("invalid_recovery_services_identity")
	}
	id := strings.ToLower(value)
	if kind == recoveryServicesDeletedVault {
		parts := strings.Split(id, "/")
		if len(parts) != 9 || strings.Join(parts[:6], "/") != c.root()+"/providers/microsoft.recoveryservices/locations" || !cosmosOperationRegion.MatchString(parts[6]) || parts[7] != "deletedvaults" || parts[8] == "" || parts[8] == "." || parts[8] == ".." || strings.ContainsAny(id, "%?#\\") {
			return "", serviceDenied("invalid_recovery_services_deleted_identity")
		}
		return id, nil
	}
	canonical, typ, err := parseID(value)
	size := map[string]int{recoveryServicesVault: 9, recoveryServicesContainer: 13, recoveryServicesItem: 15, siteRecoveryItem: 15}[kind]
	if err != nil || typ != strings.ToLower(kind) || len(strings.Split(canonical, "/")) != size || !strings.HasPrefix(canonical, c.root()+"/") {
		return "", serviceDenied("invalid_recovery_services_identity")
	}
	return canonical, nil
}

func recoveryServicesVaultID(id string) string {
	parts := strings.Split(id, "/")
	if len(parts) < 9 {
		return ""
	}
	return strings.Join(parts[:9], "/")
}

func (c *client) recoveryServicesMetadata(raw map[string]any, id, kind string) error {
	actual, err := c.recoveryServicesIdentity(text(raw["id"]), kind)
	props := object(raw["properties"])
	if err != nil || actual != id || !strings.EqualFold(text(raw["type"]), kind) || !strings.EqualFold(text(raw["name"]), last(id)) || props == nil {
		return serviceDenied("invalid_recovery_services_metadata")
	}
	switch kind {
	case recoveryServicesVault:
		if !cosmosOperationRegion.MatchString(resourceRegion(raw)) {
			return serviceDenied("invalid_recovery_services_location")
		}
	case recoveryServicesContainer:
		if text(props["containerType"]) == "" {
			return serviceDenied("invalid_recovery_services_container")
		}
	case recoveryServicesItem:
		if value, exists := props["isScheduledForDeferredDelete"]; exists {
			if _, ok := value.(bool); !ok {
				return serviceDenied("invalid_recovery_services_retention_flag")
			}
		}
		if text(props["protectedItemType"]) == "" {
			return serviceDenied("invalid_recovery_services_item")
		}
		// Native CLI responses also represent vaultId as an absolute ARM URL.
		// Validate its origin and path without following this descriptive link.
		if value, exists := props["vaultId"]; exists {
			reference := text(value)
			if strings.HasPrefix(reference, "https://") {
				u, err := url.Parse(reference)
				if err != nil || !strings.EqualFold(u.Host, "management.azure.com") || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawPath != "" {
					return serviceDenied("invalid_recovery_services_item_vault")
				}
				reference = u.Path
			}
			vault, err := c.recoveryServicesIdentity(reference, recoveryServicesVault)
			if err != nil || vault != recoveryServicesVaultID(id) {
				return serviceDenied("invalid_recovery_services_item_vault")
			}
		}
	case siteRecoveryItem:
		if text(props["protectedItemType"]) == "" || text(props["protectionState"]) == "" {
			return serviceDenied("invalid_site_recovery_item")
		}
		for _, key := range []string{"policyId", "recoveryFabricId", "recoveryContainerId"} {
			if value, exists := props[key]; exists && value != nil {
				if _, ok := value.(string); !ok {
					return serviceDenied("invalid_site_recovery_reference")
				}
			}
		}
	case recoveryServicesDeletedVault:
		if _, err := c.recoveryServicesIdentity(text(props["vaultId"]), recoveryServicesVault); err != nil {
			return serviceDenied("invalid_recovery_services_deleted_origin")
		}
		deleted, err := time.Parse(time.RFC3339Nano, text(props["vaultDeletionTime"]))
		purge, e := time.Parse(time.RFC3339Nano, text(props["purgeAt"]))
		if err != nil || e != nil || deleted.IsZero() || purge.IsZero() {
			return serviceDenied("invalid_recovery_services_retention_time")
		}
	}
	return nil
}

func (c *client) recoveryServicesRead(ctx context.Context, id, kind string) (response, error) {
	canonical, err := c.recoveryServicesIdentity(id, kind)
	if err != nil {
		return response{}, err
	}
	res, err := c.request(ctx, "GET", apiURL(canonical, recoveryServicesVersion(kind)))
	if err != nil {
		var call *contracts.ProviderCallError
		if isNotFound(err) && errors.As(err, &call) && slices.Contains([]string{"ParentResourceNotFound", "ResourceGroupNotFound", "SubscriptionNotFound"}, call.Provider.Code) {
			err = contracts.DependencyReadError(err)
		}
		return res, err
	}
	if res.status != 200 || res.data["error"] != nil || operationLocation(res.header) != "" {
		return res, serviceDenied("incomplete_recovery_services_read")
	}
	return res, c.recoveryServicesMetadata(res.data, canonical, kind)
}

func (c *client) recoveryServicesCollection(ctx context.Context, path, kind string) (map[string]map[string]any, error) {
	validPath := false
	switch kind {
	case recoveryServicesVault:
		validPath = strings.EqualFold(path, c.root()+"/providers/microsoft.recoveryservices/vaults")
	case recoveryServicesContainer, recoveryServicesItem, siteRecoveryItem:
		suffix := "/backupprotectioncontainers"
		if kind == recoveryServicesItem {
			suffix = "/backupprotecteditems"
		} else if kind == siteRecoveryItem {
			suffix = "/replicationprotecteditems"
		}
		if strings.HasSuffix(strings.ToLower(path), suffix) {
			_, err := c.recoveryServicesIdentity(path[:len(path)-len(suffix)], recoveryServicesVault)
			validPath = err == nil
		}
	case recoveryServicesDeletedVault:
		_, err := c.recoveryServicesIdentity(path+"/validation-only", kind)
		validPath = err == nil
	}
	if !validPath {
		return nil, serviceDenied("invalid_recovery_services_collection_scope")
	}
	rows := map[string]map[string]any{}
	seen := map[string]bool{}
	version := recoveryServicesVersion(kind)
	next := apiURL(path, version)
	for next != "" {
		u, err := url.Parse(next)
		if err != nil || seen[next] {
			return nil, serviceDenied("invalid_recovery_services_page")
		}
		query, err := url.ParseQuery(u.RawQuery)
		if err != nil || query.Get("api-version") != version {
			return nil, serviceDenied("invalid_recovery_services_page_version")
		}
		for key, values := range query {
			if len(values) != 1 || key != "api-version" && key != "$skiptoken" && key != "$skipToken" && key != "skipToken" {
				return nil, serviceDenied("filtered_recovery_services_page")
			}
		}
		seen[next] = true
		values, following, res, err := c.listPageResult(ctx, next, path)
		if err != nil {
			return nil, contracts.DependencyReadError(err)
		}
		if operationLocation(res.header) != "" {
			return nil, serviceDenied("incomplete_recovery_services_page")
		}
		for _, value := range values {
			raw := object(value)
			id, err := c.recoveryServicesIdentity(text(raw["id"]), kind)
			if err != nil {
				return nil, err
			}
			if err = c.recoveryServicesMetadata(raw, id, kind); err != nil {
				return nil, err
			}
			member := false
			switch kind {
			case recoveryServicesVault:
				member = strings.EqualFold(path, c.root()+"/providers/microsoft.recoveryservices/vaults")
			case recoveryServicesContainer:
				member = strings.EqualFold(path, recoveryServicesVaultID(id)+"/backupprotectioncontainers")
			case recoveryServicesItem:
				member = strings.EqualFold(path, recoveryServicesVaultID(id)+"/backupprotecteditems")
			case siteRecoveryItem:
				member = strings.EqualFold(path, recoveryServicesVaultID(id)+"/replicationprotecteditems")
			case recoveryServicesDeletedVault:
				member = strings.EqualFold(path, redisParentID(id)+"/deletedvaults")
			}
			if !member || rows[id] != nil {
				return nil, serviceDenied("invalid_recovery_services_member")
			}
			rows[id] = raw
		}
		next = following
	}
	return rows, nil
}

func (r *Runtime) recoveryServicesSnapshot(ctx context.Context, c *client, req contracts.InventoryRequest, kind string) ([]contracts.InventoryItem, []string, error) {
	known := map[string]bool{}
	for _, value := range req.KnownNativeIDs {
		id, err := c.recoveryServicesIdentity(value, kind)
		if err != nil {
			return nil, nil, err
		}
		// The scan worker supplies known IDs across all regions. A regional
		// deleted-vault shard must not reconcile another region's tombstones.
		if kind == recoveryServicesDeletedVault && req.Scope.Kind == asset.ScopeRegion && strings.Split(id, "/")[6] != strings.ToLower(req.Scope.NativeID) {
			continue
		}
		known[id] = true
	}
	targets := map[string]string{}
	parents := map[string]map[string]any{}
	if kind == recoveryServicesDeletedVault {
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
			targets[c.root()+"/providers/microsoft.recoveryservices/locations/"+location+"/deletedvaults"] = location
		}
	} else if kind == recoveryServicesVault {
		targets[c.root()+"/providers/microsoft.recoveryservices/vaults"] = ""
	} else {
		vaults, err := c.recoveryServicesCollection(ctx, c.root()+"/providers/microsoft.recoveryservices/vaults", recoveryServicesVault)
		if err != nil {
			return nil, nil, err
		}
		for id := range known {
			parent := recoveryServicesVaultID(id)
			if vaults[parent] == nil {
				vaults[parent] = map[string]any{}
			}
		}
		for _, id := range slices.Sorted(maps.Keys(vaults)) {
			own, err := c.recoveryServicesRead(ctx, id, recoveryServicesVault)
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
			collection := "backupprotectioncontainers"
			if kind == recoveryServicesItem {
				collection = "backupprotecteditems"
			} else if kind == siteRecoveryItem {
				collection = "replicationprotecteditems"
			}
			targets[id+"/"+collection] = location
		}
	}
	rows := map[string]map[string]any{}
	locations := map[string]string{}
	for _, path := range slices.Sorted(maps.Keys(targets)) {
		listed, err := c.recoveryServicesCollection(ctx, path, kind)
		if err != nil {
			return nil, nil, err
		}
		for id, raw := range listed {
			if rows[id] != nil {
				return nil, nil, serviceDenied("duplicate_recovery_services_resource")
			}
			rows[id] = raw
			locations[id] = targets[path]
		}
	}
	for id := range known {
		if kind == recoveryServicesDeletedVault && req.Scope.Kind == asset.ScopeRegion && strings.Split(id, "/")[6] != strings.ToLower(req.Scope.NativeID) {
			return nil, nil, serviceDenied("foreign_deleted_vault_hint")
		}
		if kind != recoveryServicesVault && kind != recoveryServicesDeletedVault && parents[recoveryServicesVaultID(id)] == nil {
			continue
		}
		if rows[id] == nil {
			rows[id] = map[string]any{}
		}
		if kind == recoveryServicesDeletedVault {
			locations[id] = strings.Split(id, "/")[6]
		} else if kind != recoveryServicesVault {
			locations[id] = resourceRegion(parents[recoveryServicesVaultID(id)])
		}
	}
	containers := map[string]map[string]any{}
	items := []contracts.InventoryItem{}
	absent := []string{}
	for _, id := range slices.Sorted(maps.Keys(rows)) {
		own, err := c.recoveryServicesRead(ctx, id, kind)
		if isNotFound(err) && len(rows[id]) == 0 {
			absent = append(absent, id)
			continue
		}
		if err != nil {
			return nil, nil, contracts.DependencyReadError(err)
		}
		// Compare native representations, not the canonical URL alias.
		if len(rows[id]) != 0 && !nativeConfigurationContains(rows[id], own.data) {
			return nil, nil, serviceDenied("recovery_services_listed_configuration_changed")
		}
		location := locations[id]
		if kind == recoveryServicesVault {
			location = resourceRegion(own.data)
		}
		if req.Scope.Kind == asset.ScopeRegion && !strings.EqualFold(location, req.Scope.NativeID) {
			continue
		}
		props := object(own.data["properties"])
		state := text(props["provisioningState"])
		if kind == recoveryServicesDeletedVault {
			state = "soft_deleted"
		} else if kind == recoveryServicesItem {
			state = text(props["protectionState"])
			if props["isScheduledForDeferredDelete"] == true {
				state = "soft_deleted"
			}
		} else if kind == recoveryServicesContainer {
			state = text(props["registrationStatus"])
		} else if kind == siteRecoveryItem {
			state = text(props["protectionState"])
		}
		normalized := map[string]any{"name": last(id), "state": state, "subscriptionId": c.subscription, "_inventory_source": recoveryServicesSource, "_recovery_services_configuration": c.privateConfiguration(own.data), "cleanup_protected": true, "cleanup_protection_reason": "recovery_services_cleanup_not_implemented"}
		if kind != recoveryServicesDeletedVault {
			normalized["resourceGroup"] = strings.Split(id, "/")[4]
		}
		if kind != recoveryServicesVault && kind != recoveryServicesDeletedVault {
			parent := recoveryServicesVaultID(id)
			normalized["vaultId"] = parent
			normalized[referenceKey(recoveryServicesVault)] = []string{parent}
			normalized["_recovery_services_parent_configuration"] = c.privateConfiguration(parents[parent])
		}
		if kind == recoveryServicesItem {
			container := redisParentID(id)
			parent, err := c.recoveryServicesRead(ctx, container, recoveryServicesContainer)
			if err != nil {
				return nil, nil, contracts.DependencyReadError(err)
			}
			if previous := containers[container]; previous != nil && c.privateConfiguration(previous) != c.privateConfiguration(parent.data) {
				return nil, nil, serviceDenied("recovery_services_container_changed")
			}
			containers[container] = parent.data
			normalized["_recovery_services_container_configuration"] = c.privateConfiguration(parent.data)
			normalized["containerId"] = container
			normalized[referenceKey(recoveryServicesContainer)] = []string{container}
			normalized["retained"] = props["isScheduledForDeferredDelete"] == true
		}
		if kind == siteRecoveryItem {
			normalized["replicationFabricId"] = strings.Join(strings.Split(id, "/")[:11], "/")
			normalized["protectionContainerId"] = redisParentID(id)
			normalized["protectedItemType"] = props["protectedItemType"]
			normalized["replicationHealth"] = props["replicationHealth"]
			normalized["activeLocation"] = props["activeLocation"]
			normalized["cleanup_protection_reason"] = "site_recovery_replication_read_only"
		}
		if kind == recoveryServicesDeletedVault {
			normalized["originalVaultId"] = strings.ToLower(text(props["vaultId"]))
			normalized["deletionTime"] = props["vaultDeletionTime"]
			normalized["scheduledPurgeTime"] = props["purgeAt"]
		}
		actionable := false
		items = append(items, contracts.InventoryItem{NativeID: id, NativeType: kind, ResourceKind: r.resourceKind(kind), Name: last(id), State: state, Location: location, Scope: contracts.InventoryScope{Kind: asset.ScopeRegion, NativeID: location, Name: location, Location: location}, Normalized: normalized, Raw: object(recoveryServicesSafeValue(own.data)), NativeAliases: []string{id}, Actionable: &actionable})
	}
	if kind == recoveryServicesContainer {
		for i := range items {
			if err := r.recoveryContainerInventory(ctx, c, req, &items[i]); err != nil {
				return nil, nil, err
			}
		}
	}

	if kind == recoveryServicesItem {
		for i := range items {
			if err := r.recoveryItemInventory(ctx, c, req, &items[i]); err != nil {
				return nil, nil, err
			}
		}
	}

	for id, raw := range containers {
		own, err := c.recoveryServicesRead(ctx, id, recoveryServicesContainer)
		if err != nil {
			return nil, nil, contracts.DependencyReadError(err)
		}
		if c.privateConfiguration(raw) != c.privateConfiguration(own.data) {
			return nil, nil, serviceDenied("recovery_services_container_changed")
		}
	}

	for id, raw := range parents {
		own, err := c.recoveryServicesRead(ctx, id, recoveryServicesVault)
		if err != nil {
			return nil, nil, contracts.DependencyReadError(err)
		}
		if c.privateConfiguration(raw) != c.privateConfiguration(own.data) {
			return nil, nil, serviceDenied("backup_vault_changed_during_inventory")
		}
	}
	return items, absent, nil
}

func (r *Runtime) listRecoveryServices(ctx context.Context, c *client, req contracts.InventoryRequest) (batch contracts.InventoryBatch, err error) {
	defer func() { err = contracts.DependencyReadError(err) }()
	kind := ""
	if req.ResourceKind != nil {
		kind = recoveryServicesKind(req.ResourceKind.NativeType)
	}
	if kind == "" || req.Source != recoveryServicesSource || len(req.Options) != 0 || req.NetworkTarget != nil || !(req.Scope.Kind == asset.ScopeSubscription && strings.EqualFold(req.Scope.NativeID, c.subscription) || req.Scope.Kind == asset.ScopeRegion && cosmosOperationRegion.MatchString(req.Scope.NativeID)) {
		return batch, serviceDenied("invalid_recovery_services_inventory_request")
	}
	cursor := productCursor{}
	if req.Cursor != "" {
		wire, e := base64.RawURLEncoding.DecodeString(req.Cursor)
		if e != nil || len(req.Cursor) > 128<<10 || json.Unmarshal(wire, &cursor) != nil || cursor.Target < 1 || cursor.Fingerprint == "" || cursor.Next != "" || len(cursor.Seen)+len(cursor.Resources) != 0 {
			return batch, serviceDenied("invalid_recovery_services_cursor")
		}
	}
	first, absent, err := r.recoveryServicesSnapshot(ctx, c, req, kind)
	if err != nil {
		return batch, err
	}
	second, gone, err := r.recoveryServicesSnapshot(ctx, c, req, kind)
	if err != nil {
		return batch, err
	}
	observation := map[string]any{"items": first, "absent": absent}
	if c.privateConfiguration(observation) != c.privateConfiguration(map[string]any{"items": second, "absent": gone}) {
		return batch, serviceDenied("recovery_services_snapshot_changed")
	}
	boundary := req
	boundary.Cursor = ""
	boundary.Limit = 0
	fingerprint := c.privateConfiguration(map[string]any{"request": boundary, "revision": r.bundle.Revision, "observation": observation})
	if cursor.Fingerprint != "" && cursor.Fingerprint != fingerprint || cursor.Target > len(first) {
		return batch, serviceDenied("recovery_services_cursor_changed")
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

func recoveryServicesSafeValue(value any) any {
	switch v := value.(type) {
	case map[string]any:
		out := map[string]any{}
		for key, child := range v {
			switch key {
			case "id", "name", "type", "location", "state", "provisioningState", "registrationStatus", "protectionState", "protectionStatus", "protectedItemType", "replicationHealth", "activeLocation", "containerType", "backupManagementType", "workloadType", "vaultId", "policyId", "sourceResourceId", "isScheduledForDeferredDelete", "softDeleteRetentionPeriod", "deferredDeleteTimeInUTC", "vaultDeletionTime", "purgeAt", "request_id", "status_code", "method", "path", "api-version", "code":
				switch child.(type) {
				case string, bool, float64, int, json.Number, nil:
					out[key] = child
				default:
					out[key] = recoveryServicesSafeValue(child)
				}
			case "body", "properties", "value", "query", "error":
				out[key] = recoveryServicesSafeValue(child)
			default:
				out[key] = "[REDACTED]"
			}
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, child := range v {
			out[i] = recoveryServicesSafeValue(child)
		}
		return out
	default:
		return "[REDACTED]"
	}
}
