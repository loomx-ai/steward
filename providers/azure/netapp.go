package azure

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"maps"
	"net/url"
	"slices"
	"strings"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

const netappSource = "netapp"
const netappVersion = "2025-12-01"
const netappAccountType = "Microsoft.NetApp/netAppAccounts"
const netappPoolType = netappAccountType + "/capacityPools"
const netappVolumeType = netappPoolType + "/volumes"

type netappResource struct{ kind, family, list, parent string }

var netappResources = []netappResource{
	{netappAccountType, "Accounts", "ListBySubscription", ""},
	{netappPoolType, "Pools", "List", netappAccountType},
	{netappVolumeType, "Volumes", "List", netappPoolType},
	{netappVolumeType + "/snapshots", "Snapshots", "List", netappVolumeType},
	{netappAccountType + "/backupPolicies", "BackupPolicies", "List", netappAccountType},
	{netappAccountType + "/backupVaults", "BackupVaults", "ListByNetAppAccount", netappAccountType},
	{netappAccountType + "/backupVaults/backups", "Backups", "ListByVault", netappAccountType + "/backupVaults"},
	{netappAccountType + "/snapshotPolicies", "SnapshotPolicies", "List", netappAccountType},
	{netappAccountType + "/volumeGroups", "VolumeGroups", "ListByNetAppAccount", netappAccountType},
	{netappVolumeType + "/subvolumes", "Subvolumes", "ListByVolume", netappVolumeType},
	{netappVolumeType + "/volumeQuotaRules", "VolumeQuotaRules", "ListByVolume", netappVolumeType},
}

func netappKind(kind string) netappResource {
	for _, r := range netappResources {
		if strings.EqualFold(r.kind, kind) {
			return r
		}
	}
	return netappResource{}
}
func (c *client) netappIdentity(id, kind string) error {
	canonical, typ, err := parseID(id)
	if err != nil || id != canonical || !strings.EqualFold(typ, kind) || netappKind(kind).kind == "" || !strings.HasPrefix(id, c.root()+"/") || len(strings.Split(id, "/")) != 7+2*(len(strings.Split(kind, "/"))-1) {
		return serviceDenied("invalid_netapp_identity")
	}
	return nil
}
func netappMetadata(raw map[string]any, id, kind string) bool {
	parts := strings.Split(id, "/")
	names := []string{}
	for i := 8; i < len(parts); i += 2 {
		names = append(names, parts[i])
	}
	return strings.EqualFold(text(raw["id"]), id) && strings.EqualFold(text(raw["type"]), kind) && (strings.EqualFold(text(raw["name"]), last(id)) || strings.EqualFold(text(raw["name"]), strings.Join(names, "/"))) && object(raw["properties"]) != nil
}
func (c *client) netappRead(ctx context.Context, id, kind string) (response, error) {
	if err := c.netappIdentity(id, kind); err != nil {
		return response{}, err
	}
	res, err := c.request(ctx, "GET", apiURL(id, netappVersion))
	if err != nil {
		return res, err
	}
	if res.status != 200 || operationLocation(res.header) != "" || res.data["error"] != nil || !netappMetadata(res.data, id, kind) {
		return res, serviceDenied("invalid_netapp_read")
	}
	return res, nil
}
func (c *client) netappIndex(ctx context.Context, kind, parent string) ([]any, error) {
	r := netappKind(kind)
	path := parent + "/" + last(kind)
	if r.parent == "" {
		path = c.root() + "/providers/Microsoft.NetApp/netAppAccounts"
	} else if err := c.netappIdentity(parent, r.parent); err != nil {
		return nil, err
	}
	// Native list methods all share the resource collection URL. Keep POST
	// replication discovery separate: it has a different response contract.
	rows := []any{}
	next := apiURL(path, netappVersion)
	seen := map[string]bool{}
	for next != "" {
		u, err := url.Parse(next)
		if err != nil || seen[next] {
			return nil, serviceDenied("invalid_netapp_page")
		}
		seen[next] = true
		for key, values := range u.Query() {
			if len(values) != 1 || key != "api-version" && key != "$skiptoken" && key != "skipToken" {
				return nil, serviceDenied("filtered_netapp_page")
			}
		}
		if u.Query().Get("api-version") != netappVersion {
			return nil, serviceDenied("invalid_netapp_page_version")
		}
		page, cursor, res, err := c.listPageResult(ctx, next, path)
		if err != nil {
			return nil, err
		}
		if operationLocation(res.header) != "" {
			return nil, serviceDenied("incomplete_netapp_page")
		}
		rows = append(rows, page...)
		next = cursor
	}
	return rows, nil
}

func netappPublicScalar(value any) bool {
	switch value.(type) {
	case nil, string, bool, json.Number, float64, float32, int, int64:
		return true
	}
	return false
}

// Persist selected service metadata, never AD credentials or future raw fields.
func netappSafeProperties(props map[string]any) map[string]any {
	result := map[string]any{}
	for _, k := range strings.Fields("provisioningState poolId size serviceLevel qosType totalThroughputMibps utilizedThroughputMibps customThroughputMibps coolAccess encryptionType usageThreshold fileSystemId creationToken protocolTypes subnetId networkFeatures effectiveNetworkFeatures networkSiblingSetId storageToNetworkProximity throughputMibps volumeType isRestoring snapshotDirectoryVisible securityStyle kerberosEnabled created snapshotId backupId creationDate completionDate snapshotCreationDate backupType label enabled dailyBackupsToKeep weeklyBackupsToKeep monthlyBackupsToKeep volumesAssigned path quotaSizeInKiBs quotaTarget quotaType disableShowmount") {
		if v, ok := props[k]; ok && netappPublicScalar(v) {
			result[k] = v
		}
	}
	if values, ok := props["protocolTypes"].([]any); ok {
		out := []any{}
		for _, v := range values {
			if value, ok := v.(string); ok {
				out = append(out, value)
			}
		}
		result["protocolTypes"] = out
	}
	for _, entry := range []struct{ key, fields string }{{"mountTargets", "mountTargetId fileSystemId ipAddress smbServerFqdn"}} {
		if rows, ok := props[entry.key].([]any); ok {
			out := []any{}
			for _, row := range rows {
				v := map[string]any{}
				for _, k := range strings.Fields(entry.fields) {
					if x, ok := object(row)[k]; ok && netappPublicScalar(x) {
						v[k] = x
					}
				}
				out = append(out, v)
			}
			result[entry.key] = out
		}
	}
	if policy, ok := props["exportPolicy"].(map[string]any); ok {
		rules := []any{}
		for _, raw := range array(policy["rules"]) {
			rule := map[string]any{}
			for _, k := range strings.Fields("ruleIndex unixReadOnly unixReadWrite cifs nfsv3 nfsv41 allowedClients hasRootAccess kerberos5ReadOnly kerberos5ReadWrite kerberos5iReadOnly kerberos5iReadWrite kerberos5pReadOnly kerberos5pReadWrite chownMode") {
				if v, ok := object(raw)[k]; ok && netappPublicScalar(v) {
					rule[k] = v
				}
			}
			rules = append(rules, rule)
		}
		result["exportPolicy"] = map[string]any{"rules": rules}
	}
	return result
}
func netappReferences(id, kind string, raw map[string]any) (map[string][]string, error) {
	refs := map[string][]string{}
	add := func(v any, expected string) error {
		if v == nil || v == "" {
			return nil
		}
		wire, ok := v.(string)
		target, typ, err := parseID(wire)
		if !ok || wire != strings.TrimSpace(wire) || err != nil || !strings.EqualFold(typ, expected) || target == id {
			return serviceDenied("invalid_netapp_reference")
		}
		refs[expected] = append(refs[expected], target)
		return nil
	}
	if parent := netappKind(kind).parent; parent != "" {
		if err := add(redisParentID(id), parent); err != nil {
			return nil, err
		}
	}
	p := object(raw["properties"])
	entries := []struct {
		value any
		kind  string
	}{}
	if kind == netappVolumeType {
		entries = append(entries, struct {
			value any
			kind  string
		}{p["subnetId"], subnetType}, struct {
			value any
			kind  string
		}{p["snapshotId"], netappVolumeType + "/snapshots"}, struct {
			value any
			kind  string
		}{p["backupId"], netappAccountType + "/backupVaults/backups"})
		protection := object(p["dataProtection"])
		for _, e := range []struct{ section, field, kind string }{{"backup", "backupPolicyId", netappAccountType + "/backupPolicies"}, {"backup", "backupVaultId", netappAccountType + "/backupVaults"}, {"snapshot", "snapshotPolicyId", netappAccountType + "/snapshotPolicies"}, {"replication", "remoteVolumeResourceId", netappVolumeType}} {
			entries = append(entries, struct {
				value any
				kind  string
			}{object(protection[e.section])[e.field], e.kind})
		}
	}
	if kind == netappAccountType+"/backupVaults/backups" {
		entries = append(entries, struct {
			value any
			kind  string
		}{p["volumeResourceId"], netappVolumeType}, struct {
			value any
			kind  string
		}{p["backupPolicyResourceId"], netappAccountType + "/backupPolicies"})
	}
	for _, e := range entries {
		if err := add(e.value, e.kind); err != nil {
			return nil, err
		}
	}
	for id := range object(object(raw["identity"])["userAssignedIdentities"]) {
		if err := add(id, "Microsoft.ManagedIdentity/userAssignedIdentities"); err != nil {
			return nil, err
		}
	}
	for _, subnet := range refs[subnetType] {
		refs[vnetType] = append(refs[vnetType], redisParentID(subnet))
	}
	for kind, values := range refs {
		slices.Sort(values)
		refs[kind] = slices.Compact(values)
	}
	return refs, nil
}

func (r *Runtime) netappSnapshot(ctx context.Context, c *client, req contracts.InventoryRequest) ([]contracts.InventoryItem, []string, map[string]any, string, error) {
	kind := netappKind(req.ResourceKind.NativeType).kind
	known := map[string]bool{}
	hints := map[string]map[string]bool{}
	for _, id := range req.KnownNativeIDs {
		if err := c.netappIdentity(id, kind); err != nil || known[id] {
			return nil, nil, nil, "", serviceDenied("invalid_netapp_known_id")
		}
		known[id] = true
		for target, typ := id, kind; typ != ""; target, typ = redisParentID(target), netappKind(typ).parent {
			if hints[typ] == nil {
				hints[typ] = map[string]bool{}
			}
			hints[typ][target] = true
		}
	}
	for id := range req.KnownNativeMetadata {
		if !known[id] {
			return nil, nil, nil, "", serviceDenied("unrelated_netapp_metadata")
		}
	}
	raws := map[string]map[string]any{}
	regions := map[string]string{}
	absent := []string{}
	requestID := ""
	var collect func(string) ([]string, error)
	collect = func(typ string) ([]string, error) {
		definition := netappKind(typ)
		parents := []string{""}
		if definition.parent != "" {
			var err error
			parents, err = collect(definition.parent)
			if err != nil {
				return nil, err
			}
		}
		ids := map[string]bool{}
		for _, parent := range parents {
			if definition.family == "Subvolumes" {
				flag := object(raws[parent]["properties"])["enableSubvolumes"]
				if flag == nil || flag == "Disabled" {
					continue
				}
				if flag != "Enabled" {
					return nil, serviceDenied("unknown_netapp_subvolume_support")
				}
			}
			rows, err := c.netappIndex(ctx, typ, parent)
			if err != nil {
				return nil, contracts.DependencyReadError(err)
			}
			for _, v := range rows {
				raw := object(v)
				id := strings.ToLower(text(raw["id"]))
				if err := c.netappIdentity(id, typ); err != nil || !netappMetadata(raw, id, typ) || ids[id] || parent != "" && redisParentID(id) != parent {
					return nil, serviceDenied("invalid_netapp_index")
				}
				ids[id] = true
			}
		}
		for id := range hints[typ] {
			ids[id] = true
		}
		out := []string{}
		for _, id := range slices.Sorted(maps.Keys(ids)) {
			parent := redisParentID(id)
			if definition.parent != "" && raws[parent] == nil {
				return nil, serviceDenied("netapp_lookup_parent_unavailable")
			}
			own, err := c.netappRead(ctx, id, typ)
			if isNotFound(err) {
				if typ == kind && known[id] {
					absent = append(absent, id)
				}
				continue
			}
			if err != nil {
				return nil, err
			}
			region := strings.ReplaceAll(strings.ToLower(text(own.data["location"])), " ", "")
			if region == "" && (definition.family == "Backups" || definition.family == "Subvolumes") {
				region = regions[parent]
			}
			if region == "" || definition.parent != "" && regions[parent] != region {
				return nil, serviceDenied("invalid_netapp_region")
			}
			raws[id] = own.data
			regions[id] = region
			if req.Scope.Kind == asset.ScopeRegion && !strings.EqualFold(region, req.Scope.NativeID) {
				continue
			}
			out = append(out, id)
			if own.requestID != "" {
				requestID = own.requestID
			}
		}
		return out, nil
	}
	ids, err := collect(kind)
	if err != nil {
		return nil, nil, nil, "", err
	}
	contextHashes := map[string]any{}
	for _, id := range slices.Sorted(maps.Keys(raws)) {
		_, typ, _ := parseID(id)
		own, err := c.netappRead(ctx, id, typ)
		if err != nil {
			return nil, nil, nil, "", contracts.DependencyReadError(err)
		}
		fingerprint := c.privateConfiguration(raws[id])
		if fingerprint != c.privateConfiguration(own.data) {
			return nil, nil, nil, "", serviceDenied("netapp_inventory_changed")
		}
		contextHashes[id] = fingerprint
	}
	if kind == netappBackupType {
		cache := &netappRecoveryInventoryCache{backups: map[string]map[string]any{}, sources: map[string]netappSourceObservation{}}
		for id, raw := range raws {
			if strings.EqualFold(text(raw["type"]), netappBackupType) {
				cache.backups[id] = raw
			}
		}
		ctx = context.WithValue(ctx, netappRecoveryCacheKey{}, cache)
	}
	items := []contracts.InventoryItem{}
	for _, id := range ids {
		raw := raws[id]
		props := netappSafeProperties(object(raw["properties"]))
		normalized := maps.Clone(props)
		refs, err := netappReferences(id, kind, raw)
		if err != nil {
			return nil, nil, nil, "", err
		}
		network := []string{}
		for typ, values := range refs {
			normalized["refs_"+strings.NewReplacer(".", "_", "/", "_").Replace(strings.ToLower(typ))] = values
			network = append(network, values...)
		}
		slices.Sort(network)
		tags := map[string]string{}
		for key, v := range object(raw["tags"]) {
			if value, ok := v.(string); ok {
				tags[key] = value
			}
		}
		normalized["name"], normalized["state"], normalized["tags"] = raw["name"], text(props["provisioningState"]), tags
		normalized["_inventory_source"], normalized["_netapp_configuration"] = netappSource, contextHashes[id]
		context := map[string]any{}
		for target, typ := id, kind; typ != ""; target, typ = redisParentID(target), netappKind(typ).parent {
			context[target] = contextHashes[target]
		}
		normalized["_netapp_context"] = context
		normalized["cleanup_protected"], normalized["cleanup_protection_reason"] = true, "netapp_cleanup_not_implemented"
		normalized["subscriptionId"], normalized["resourceGroup"] = c.subscription, strings.Split(id, "/")[4]
		actionable := false
		region := regions[id]
		safe := map[string]any{"id": id, "type": kind, "name": raw["name"], "location": region, "tags": tags, "properties": props}
		item := contracts.InventoryItem{NativeID: id, NativeType: kind, ResourceKind: r.resourceKind(kind), Name: text(raw["name"]), State: text(props["provisioningState"]), Location: region, Scope: contracts.InventoryScope{Kind: asset.ScopeRegion, NativeID: region, Name: region, Location: region}, Tags: tags, Normalized: normalized, Raw: safe, NativeAliases: []string{id}, NetworkReferences: slices.Compact(network), Actionable: &actionable}
		if kind == netappPoolType {
			if err := r.netappPoolInventory(ctx, c, req, &item); err != nil {
				return nil, nil, nil, "", err
			}
		} else if kind == netappVolumeType {
			if err := r.netappVolumeInventory(ctx, c, req, &item); err != nil {
				return nil, nil, nil, "", err
			}
		} else if netappDirectLeaf(kind) {
			if err := r.netappRecoveryInventory(ctx, c, req, &item); err != nil {
				return nil, nil, nil, "", err
			}

		}
		items = append(items, item)
	}
	return items, absent, contextHashes, requestID, nil
}
func (r *Runtime) listNetapp(ctx context.Context, c *client, req contracts.InventoryRequest) (batch contracts.InventoryBatch, err error) {
	defer func() { err = contracts.DependencyReadError(err) }()
	if req.ResourceKind == nil || netappKind(req.ResourceKind.NativeType).kind == "" || req.Source != netappSource || len(req.Options) != 0 || req.Scope.Kind != asset.ScopeRegion && req.Scope.Kind != asset.ScopeSubscription || req.Scope.NativeID == "" {
		return batch, serviceDenied("invalid_netapp_inventory_request")
	}
	cursor := productCursor{}
	if req.Cursor != "" {
		wire, err := base64.RawURLEncoding.DecodeString(req.Cursor)
		if err != nil || len(req.Cursor) > 128<<10 || json.Unmarshal(wire, &cursor) != nil || cursor.Target < 1 || cursor.Fingerprint == "" || cursor.Next != "" || len(cursor.Resources)+len(cursor.Seen) != 0 {
			return batch, serviceDenied("invalid_netapp_cursor")
		}
	}
	first, absent, parents, provenance, err := r.netappSnapshot(ctx, c, req)
	if err != nil {
		return batch, err
	}
	second, gone, current, lastRequest, err := r.netappSnapshot(ctx, c, req)
	if err != nil {
		return batch, err
	}
	if c.privateConfiguration(map[string]any{"items": first}) != c.privateConfiguration(map[string]any{"items": second}) || c.privateConfiguration(parents) != c.privateConfiguration(current) || !slices.Equal(absent, gone) {
		return batch, serviceDenied("netapp_snapshot_changed")
	}
	if lastRequest != "" {
		provenance = lastRequest
	}
	boundary := req
	boundary.Cursor, boundary.Limit = "", 0
	fingerprint := c.privateConfiguration(map[string]any{"request": boundary, "revision": r.bundle.Revision, "items": first, "parents": parents, "absent": absent})
	if cursor.Fingerprint != "" && cursor.Fingerprint != fingerprint || cursor.Target > len(first) {
		return batch, serviceDenied("netapp_cursor_changed")
	}
	limit := req.Limit
	if limit <= 0 || limit > 500 {
		limit = 500
	}
	end := min(cursor.Target+limit, len(first))
	batch.Items = first[cursor.Target:end]
	batch.Complete = end == len(first)
	batch.RequestID = provenance
	if batch.Complete {
		batch.AbsentNativeIDs = absent
	} else {
		wire, _ := json.Marshal(productCursor{Target: end, Fingerprint: fingerprint})
		batch.NextCursor = base64.RawURLEncoding.EncodeToString(wire)
	}
	return batch, nil
}

// Retain only safe resource metadata in generic API diagnostics too.
func netappSafeValue(value any) any {
	switch v := value.(type) {
	case map[string]any:
		out := map[string]any{}
		for k, item := range v {
			if v["id"] != nil && v["type"] != nil && v["properties"] != nil && !slices.Contains([]string{"id", "type", "name", "location", "tags", "properties", "etag", "zones"}, k) {
				continue
			}
			if k == "properties" {
				out[k] = netappSafeProperties(object(item))
			} else {
				out[k] = netappSafeValue(item)
			}
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = netappSafeValue(item)
		}
		return out
	default:
		return value
	}
}
