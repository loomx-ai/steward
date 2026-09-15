package azure

import (
	"context"
	"maps"
	"strings"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

const synapseWorkspaceBoundaryKey = "_synapse_workspace_boundary"
const synapseWorkspaceBoundaryProof = "_synapse_workspace_boundary_proof"

func (c *synapseDataClient) workspaceBoundaryProof(id string, connection asset.ConnectionID, value map[string]any) string {
	return c.arm.privateConfiguration(map[string]any{"protocol": "synapse-workspace-boundary-1", "id": id, "connection": connection, "value": value})
}
func synapseWorkspaceMember(kind string) bool {
	return kind == synapseSparkType || kind == synapseSQLType || synapseDataKind(kind).kind != ""
}

// SQL/Spark pools are physical child resources; code and Livy records are
// workspace metadata. Neither category grants ownership of linked lake data.
func (c *synapseDataClient) workspacePools(ctx context.Context, w synapseWorkspace, known map[string]any) (map[string]map[string]any, error) {
	pools := map[string]map[string]any{}
	for _, kind := range []string{synapseSparkType, synapseSQLType} {
		collection := w.id + "/" + last(kind)
		next := apiURL(collection, synapseVersion)
		pages := map[string]bool{}
		for next != "" {
			if pages[next] {
				return nil, serviceDenied("synapse_workspace_pool_page_cycle")
			}
			pages[next] = true
			rows, following, _, err := c.arm.synapsePage(ctx, next, collection, kind)
			if err != nil {
				return nil, err
			}
			for _, value := range rows {
				raw := object(value)
				id := strings.ToLower(text(raw["id"]))
				if _, exists := pools[id]; exists {
					return nil, serviceDenied("synapse_workspace_duplicate_pool")
				}
				res, err := c.arm.request(ctx, "GET", apiURL(id, synapseVersion))
				if isNotFound(err) {
					pools[id] = nil
					continue
				}
				if err != nil {
					return nil, err
				}
				if err = c.arm.synapseReadResponse(res, id, kind); err != nil {
					return nil, err
				}
				if resourceRegion(res.data) != resourceRegion(w.raw) || !nativeConfigurationContains(synapseSnapshot(raw), synapseSnapshot(res.data)) {
					return nil, serviceDenied("synapse_workspace_pool_changed")
				}
				pools[id] = res.data
			}
			next = following
		}
	}
	for id, value := range known {
		entry := object(value)
		kind := text(entry["kind"])
		if kind != synapseSparkType && kind != synapseSQLType {
			continue
		}
		canonical, typ, err := parseID(id)
		if err != nil || canonical != id || !strings.EqualFold(typ, kind) || redisParentID(id) != w.id {
			return nil, serviceDenied("synapse_workspace_pool_scope_changed")
		}
		if pools[id] != nil {
			continue
		}
		res, err := c.arm.request(ctx, "GET", apiURL(id, synapseVersion))
		if isNotFound(err) {
			pools[id] = nil
			continue
		}
		if err != nil {
			return nil, err
		}
		if err = c.arm.synapseReadResponse(res, id, kind); err != nil {
			return nil, err
		}
		if resourceRegion(res.data) != resourceRegion(w.raw) {
			return nil, serviceDenied("synapse_workspace_pool_region_changed")
		}
		pools[id] = res.data
	}
	return pools, nil
}
func synapseBoundaryKnown(known map[string]any, pool string, spark bool) map[string]any {
	out := map[string]any{}
	for id, value := range known {
		row := object(value)
		d := synapseDataKind(text(row["kind"]))
		if d.kind == "" || d.spark != spark || spark && object(row["parameters"])["sparkPoolName"] != pool {
			continue
		}
		out[id] = value
	}
	return out
}
func (c *synapseDataClient) workspaceBoundary(ctx context.Context, w synapseWorkspace, known map[string]any) (out map[string]any, err error) {
	defer func() { err = contracts.DependencyReadError(err) }()
	members := map[string]any{}
	rawMembers := map[string]map[string]any{}
	for _, value := range known {
		if !synapseWorkspaceMember(text(object(value)["kind"])) {
			return nil, serviceDenied("unknown_synapse_workspace_member")
		}
	}
	groupID := strings.Join(strings.Split(w.id, "/")[:5], "/")
	group, err := c.arm.request(ctx, "GET", apiURL(groupID, resourcesVersion))
	if err != nil {
		return nil, err
	}
	if !validResourceResponse(group, groupID, groupType) {
		return nil, serviceDenied("invalid_synapse_workspace_group")
	}
	pools, err := c.workspacePools(ctx, w, known)
	if err != nil {
		return nil, err
	}
	code, err := c.synapseWork(ctx, synapseDataTarget{workspace: w}, synapseBoundaryKnown(known, "", false), []synapseDataDefinition{synapseDataKind(synapseNotebookType), synapseDataKind(synapseJobDefinitionType), synapseDataKind(synapsePipelineType)})
	if err != nil {
		return nil, err
	}
	maps.Copy(members, code.manifest)
	maps.Copy(rawMembers, code.raw)
	visited := map[string]bool{}
	for id, raw := range pools {
		if raw == nil {
			continue
		}
		kind := synapseKind(text(raw["type"]))
		members[id] = map[string]any{"kind": kind, "configuration": c.arm.privateConfiguration(synapseSnapshot(raw))}
		rawMembers[id] = raw
		if kind != synapseSparkType {
			continue
		}
		name := text(raw["name"])
		visited[name] = true
		work, err := c.synapseWork(ctx, synapseDataTarget{workspace: w, pool: raw}, synapseBoundaryKnown(known, name, true), []synapseDataDefinition{synapseDataKind(synapseBatchType), synapseDataKind(synapseSessionType)})
		if err != nil {
			return nil, err
		}
		maps.Copy(members, work.manifest)
		maps.Copy(rawMembers, work.raw)
	}
	// A removed pool cannot erase historical work from the reviewed boundary.
	// Only each known record's own GET can establish that record's absence.
	for id, value := range known {
		old := object(value)
		d := synapseDataKind(text(old["kind"]))
		params := object(old["parameters"])
		if !d.spark || visited[text(params["sparkPoolName"])] {
			continue
		}
		canonical, expected, e := c.arm.synapseDataIdentity(id, d.kind)
		if e != nil || canonical != id || params["endpoint"] != w.endpoint || c.arm.privateConfiguration(params) != c.arm.privateConfiguration(expected) {
			return nil, serviceDenied("synapse_workspace_known_job_changed")
		}
		res, e := c.synapseReadData(ctx, synapseDataTarget{workspace: w}, d, params)
		if isNotFound(e) {
			continue
		}
		if e != nil {
			return nil, e
		}
		if e = synapseSparkIncarnation(res.data); e != nil {
			return nil, e
		}
		members[id] = map[string]any{"kind": d.kind, "parameters": params, "configuration": c.arm.privateConfiguration(synapseDataSnapshot(d, res.data))}
		rawMembers[id] = res.data
	}
	after, err := c.workspacePools(ctx, w, known)
	if err != nil {
		return nil, err
	}
	if len(after) != len(pools) {
		return nil, serviceDenied("synapse_workspace_pool_index_changed")
	}
	for id, raw := range pools {
		other, present := after[id]
		if !present || (raw == nil) != (other == nil) {
			return nil, serviceDenied("synapse_workspace_pool_index_changed")
		}
		if raw == nil {
			continue
		}
		if c.arm.privateConfiguration(synapseSnapshot(raw)) != c.arm.privateConfiguration(synapseSnapshot(other)) {
			return nil, serviceDenied("synapse_workspace_pool_index_changed")
		}
	}
	for id, value := range members {
		entry := object(value)
		d := synapseDataKind(text(entry["kind"]))
		if d.kind == "" {
			continue
		}
		final, err := c.synapseReadData(ctx, synapseDataTarget{workspace: w}, d, object(entry["parameters"]))
		if err != nil {
			return nil, err
		}
		if entry["configuration"] != c.arm.privateConfiguration(synapseDataSnapshot(d, final.data)) {
			return nil, serviceDenied("synapse_workspace_member_changed")
		}
		rawMembers[id] = final.data
	}
	for id, value := range known {
		if members[id] == nil {
			entry := maps.Clone(object(value))
			entry["absent"] = true
			members[id] = entry
		}
	}
	locks, err := c.arm.managementLocks(ctx)
	if err != nil {
		return nil, err
	}
	protected := text(group.data["managedBy"]) != "" || protectedAzureTags(object(group.data["tags"])) || protectedAzureTags(object(w.raw["tags"])) || locked(w.id, locks)
	for id, raw := range rawMembers {
		if synapseKind(text(raw["type"])) == synapseSparkType || synapseKind(text(raw["type"])) == synapseSQLType {
			if synapseKind(text(raw["type"])) == synapseSQLType {
				links, err := c.arm.sqlReplication(ctx, id, nil)
				if err != nil {
					return nil, err
				}
				protected = protected || len(links) != 0
			}
			stamp, e := time.Parse(time.RFC3339Nano, text(object(raw["properties"])["creationDate"]))
			protected = protected || e != nil || stamp.IsZero()
		} else if !synapseDataKind(text(object(members[id])["kind"])).spark {
			protected = protected || text(raw["etag"]) == ""
		}
		protected = protected || protectedAzureTags(object(raw["tags"])) || locked(id, locks)
	}
	current, err := c.arm.synapseWorkspaceRead(ctx, w.id)
	if err != nil {
		return nil, err
	}
	if c.arm.privateConfiguration(synapseSnapshot(w.raw)) != c.arm.privateConfiguration(synapseSnapshot(current.raw)) {
		return nil, serviceDenied("synapse_workspace_boundary_changed")
	}
	groupAfter, err := c.arm.request(ctx, "GET", apiURL(groupID, resourcesVersion))
	if err != nil {
		return nil, err
	}
	groupHash := c.arm.privateConfiguration(synapseSnapshot(group.data))
	if !validResourceResponse(groupAfter, groupID, groupType) || groupHash != c.arm.privateConfiguration(synapseSnapshot(groupAfter.data)) {
		return nil, serviceDenied("synapse_workspace_group_changed")
	}
	ready := object(current.raw["properties"])["provisioningState"] == "Succeeded" && text(object(current.raw["properties"])["workspaceUID"]) != ""
	return map[string]any{"workspace": c.arm.privateConfiguration(synapseSnapshot(w.raw)), "workspace_uid": object(w.raw["properties"])["workspaceUID"], "group": groupHash, "members": members, "protected": protected, "ready": ready}, nil
}
func (r *Runtime) synapseWorkspaceInventory(ctx context.Context, c *client, req contracts.InventoryRequest, item *contracts.InventoryItem) error {
	data, err := r.synapseResolvedClient(req.ConnectionID, c)
	if err != nil {
		return err
	}
	w, err := c.synapseWorkspaceRead(ctx, item.NativeID)
	if err != nil {
		return err
	}
	if item.Normalized["_synapse_private_configuration"] != c.privateConfiguration(synapseSnapshot(w.raw)) {
		return serviceDenied("synapse_workspace_inventory_changed")
	}
	known := object(object(req.KnownNativeMetadata[item.NativeID][synapseWorkspaceBoundaryKey])["members"])
	boundary, err := data.workspaceBoundary(ctx, w, known)
	if err != nil {
		return err
	}
	item.Normalized[synapseWorkspaceBoundaryKey] = boundary
	item.Normalized[synapseWorkspaceBoundaryProof] = data.workspaceBoundaryProof(item.NativeID, req.ConnectionID, boundary)
	if item.Normalized["cleanup_protection_reason"] == "synapse_cleanup_not_implemented" && boundary["protected"] == false && boundary["ready"] == true {
		item.Normalized["cleanup_protected"] = false
		delete(item.Normalized, "cleanup_protection_reason")
	}
	actionable := item.Normalized["cleanup_protected"] == false
	item.Actionable = &actionable
	return nil
}
