package azure

import (
	"context"
	"encoding/json"
	"maps"
	"net/url"
	"slices"
	"strings"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

const netappVolumeReview = "_netapp_volume_review"
const netappVolumeProof = "_netapp_volume_proof"

func netappVolumeChild(kind string) bool {
	return slices.Contains([]string{netappVolumeType + "/snapshots", netappVolumeType + "/subvolumes", netappVolumeType + "/volumeQuotaRules"}, kind)
}
func (c *client) netappVolumeProofFor(id string, connection asset.ConnectionID, review map[string]any) string {
	return c.privateConfiguration(map[string]any{"protocol": "netapp-volume-review-1", "id": id, "connection": connection, "review": review})
}

// Azure's pageable POST uses GET for continuation. The native Deleted filter
// excludes terminated replications; all active peers, including remote ones,
// must remain visible to the deletion boundary.
func (c *client) netappActiveReplications(ctx context.Context, id string) ([]any, error) {
	if err := c.netappIdentity(id, netappVolumeType); err != nil {
		return nil, err
	}
	next := apiURL(id+"/listReplications", netappVersion)
	method := "POST"
	body := []byte(`{"exclude":"Deleted"}`)
	rows := []any{}
	seen := map[string]bool{}
	for next != "" {
		u, err := url.Parse(next)
		if err != nil || c.validateURL(next) != nil || !strings.EqualFold(u.Path, id+"/listReplications") || seen[next] || u.RawPath != "" || u.ForceQuery {
			return nil, serviceDenied("invalid_netapp_replication_page")
		}
		seen[next] = true
		query, err := url.ParseQuery(u.RawQuery)
		if err != nil {
			return nil, serviceDenied("invalid_netapp_replication_query")
		}
		for key, values := range query {
			if len(values) != 1 || key != "api-version" && key != "$skiptoken" && key != "skipToken" && key != "exclude" || key == "exclude" && values[0] != "Deleted" {
				return nil, serviceDenied("invalid_netapp_replication_filter")
			}
		}
		if u.Query().Get("api-version") != netappVersion {
			return nil, serviceDenied("invalid_netapp_replication_version")
		}
		res, err := c.requestBody(ctx, method, next, body, nil)
		if err != nil {
			return nil, contracts.DependencyReadError(err)
		}
		page, ok := res.data["value"].([]any)
		if !ok || res.status != 200 || res.data["error"] != nil || operationLocation(res.header) != "" {
			return nil, serviceDenied("incomplete_netapp_replication_page")
		}
		for _, v := range page {
			raw := object(v)
			target, kind, err := parseID(text(raw["remoteVolumeResourceId"]))
			if err != nil || kind != strings.ToLower(netappVolumeType) || target == id || text(raw["remoteVolumeRegion"]) == "" {
				return nil, serviceDenied("invalid_netapp_replication_peer")
			}
			rows = append(rows, raw)
		}
		next = ""
		if v, ok := res.data["nextLink"]; ok && v != nil {
			var valid bool
			next, valid = v.(string)
			if !valid {
				return nil, serviceDenied("invalid_netapp_replication_cursor")
			}
		}
		method, body = "GET", nil
	}
	return rows, nil
}
func (c *client) netappVolumeBoundary(ctx context.Context, id string, known map[string]any) (map[string]any, error) {
	if err := c.netappIdentity(id, netappVolumeType); err != nil {
		return nil, err
	}
	for target, value := range known {
		entry := object(value)
		kind := text(entry["kind"])
		if !netappVolumeChild(kind) || c.netappIdentity(target, kind) != nil || redisParentID(target) != id || text(entry["configuration"]) == "" || len(entry) < 2 || len(entry) > 3 {
			return nil, serviceDenied("invalid_netapp_volume_member_hint")
		}
		if flag, ok := entry["absent"]; ok && flag != true {
			return nil, serviceDenied("invalid_netapp_volume_member_hint")
		}
	}
	parts := strings.Split(id, "/")
	pool := redisParentID(id)
	account := strings.Join(parts[:9], "/")
	group := strings.Join(parts[:5], "/")
	raws := map[string]map[string]any{}
	review := map[string]any{}
	protected := false
	ready := true
	region := ""
	for _, entry := range []struct{ key, id, kind string }{{"volume", id, netappVolumeType}, {"pool", pool, netappPoolType}, {"account", account, netappAccountType}, {"group", group, groupType}} {
		var res response
		var err error
		if entry.kind == groupType {
			res, err = c.request(ctx, "GET", apiURL(group, resourcesVersion))
			if err == nil && (!validResourceResponse(res, group, groupType) || operationLocation(res.header) != "") {
				return nil, serviceDenied("invalid_netapp_resource_group")
			}
		} else {
			res, err = c.netappRead(ctx, entry.id, entry.kind)
		}
		if err != nil {
			return nil, contracts.DependencyReadError(err)
		}
		raws[entry.id] = res.data
		review[entry.key] = c.privateConfiguration(res.data)
		protected = protected || protectedAzureTags(object(res.data["tags"])) || text(res.data["managedBy"]) != ""
		if entry.kind != groupType {
			current := resourceRegion(res.data)
			if region != "" && current != region {
				return nil, serviceDenied("netapp_volume_parent_region_changed")
			}
			region = current
			ready = ready && object(res.data["properties"])["provisioningState"] == "Succeeded"
		}
	}
	locks, err := c.managementLocks(ctx)
	if err != nil {
		return nil, contracts.DependencyReadError(err)
	}
	protected = protected || locked(id, locks)
	props := object(raws[id]["properties"])
	poolProps := object(raws[pool]["properties"])
	ready = ready && (props["enableSubvolumes"] == nil || props["enableSubvolumes"] == "Enabled" || props["enableSubvolumes"] == "Disabled") && uuidPattern.MatchString(text(props["fileSystemId"])) && uuidPattern.MatchString(text(poolProps["poolId"])) && (props["isRestoring"] == nil || props["isRestoring"] == false) && (props["cloneProgress"] == nil || props["cloneProgress"] == json.Number("100")) && (props["volumeType"] == nil || props["volumeType"] == "Regular")
	review["file_system_id"], review["pool_id"], review["region"] = props["fileSystemId"], poolProps["poolId"], region
	peers, err := c.netappActiveReplications(ctx, id)
	if err != nil {
		return nil, err
	}
	review["replications"] = c.privateConfiguration(map[string]any{"peers": peers})
	ready = ready && len(peers) == 0
	members := map[string]any{}
	for _, child := range netappResources {
		if !netappVolumeChild(child.kind) {
			continue
		}
		ids := map[string]bool{}
		// The native flag defaults to Disabled. Disabled subvolume APIs are not
		// queried speculatively, but known child IDs still need independent reads.
		if child.family != "Subvolumes" || props["enableSubvolumes"] == "Enabled" {
			rows, err := c.netappIndex(ctx, child.kind, id)
			if err != nil {
				return nil, contracts.DependencyReadError(err)
			}
			for _, v := range rows {
				raw := object(v)
				target := strings.ToLower(text(raw["id"]))
				if c.netappIdentity(target, child.kind) != nil || redisParentID(target) != id || !netappMetadata(raw, target, child.kind) || ids[target] {
					return nil, serviceDenied("invalid_netapp_volume_member_index")
				}
				ids[target] = true
			}
		}
		for target, v := range known {
			if object(v)["kind"] != child.kind {
				continue
			}
			if c.netappIdentity(target, child.kind) != nil || redisParentID(target) != id {
				return nil, serviceDenied("invalid_netapp_volume_member_hint")
			}
			ids[target] = true
		}
		for _, target := range slices.Sorted(maps.Keys(ids)) {
			own, err := c.netappRead(ctx, target, child.kind)
			if isNotFound(err) {
				if old := object(known[target]); old != nil {
					entry := maps.Clone(old)
					entry["absent"] = true
					members[target] = entry
				}
				continue
			}
			if err != nil {
				return nil, contracts.DependencyReadError(err)
			}
			if loc := text(own.data["location"]); loc != "" && resourceRegion(own.data) != region {
				return nil, serviceDenied("netapp_member_region_changed")
			}
			protected = protected || protectedAzureTags(object(own.data["tags"])) || text(own.data["managedBy"]) != "" || locked(target, locks)
			members[target] = map[string]any{"kind": child.kind, "configuration": c.privateConfiguration(own.data)}
			raws[target] = own.data
		}
	}
	for target, raw := range raws {
		_, kind, _ := parseID(target)
		var res response
		if kind == strings.ToLower(groupType) {
			res, err = c.request(ctx, "GET", apiURL(target, resourcesVersion))
			if err == nil && (!validResourceResponse(res, target, groupType) || operationLocation(res.header) != "") {
				return nil, serviceDenied("invalid_netapp_resource_group_recheck")
			}
		} else {
			res, err = c.netappRead(ctx, target, kind)
		}
		if err != nil {
			return nil, contracts.DependencyReadError(err)
		}
		if c.privateConfiguration(raw) != c.privateConfiguration(res.data) {
			return nil, serviceDenied("netapp_volume_boundary_changed")
		}
	}
	after, err := c.netappActiveReplications(ctx, id)
	if err != nil {
		return nil, err
	}
	if review["replications"] != c.privateConfiguration(map[string]any{"peers": after}) {
		return nil, serviceDenied("netapp_volume_replications_changed")
	}
	review["members"], review["protected"], review["ready"] = members, protected, ready
	return review, nil
}
func (r *Runtime) netappVolumeInventory(ctx context.Context, c *client, req contracts.InventoryRequest, item *contracts.InventoryItem) error {
	previous := object(req.KnownNativeMetadata[item.NativeID][netappVolumeReview])
	// Previous metadata supplies only scoped read hints, never deletion
	// authority. Re-read every member and sign the new review with the current
	// credential context so credential rotation does not strand inventory.
	known := object(previous["members"])
	review, err := c.netappVolumeBoundary(ctx, item.NativeID, known)
	if err != nil {
		return err
	}
	if review["volume"] != item.Normalized["_netapp_configuration"] || review["region"] != item.Location {
		return serviceDenied("netapp_volume_inventory_changed")
	}
	item.Normalized[netappVolumeReview], item.Normalized[netappVolumeProof] = review, c.netappVolumeProofFor(item.NativeID, req.ConnectionID, review)
	actionable := review["ready"] == true && review["protected"] == false
	item.Actionable = &actionable
	item.Normalized["cleanup_protected"] = !actionable
	if actionable {
		delete(item.Normalized, "cleanup_protection_reason")
	}
	return nil
}
