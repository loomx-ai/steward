package azure

import (
	"github.com/loomx-ai/steward/internal/core/asset"
)

const elasticSanBoundary = "_elastic_san_boundary"
const elasticSanBoundaryProof = "_elastic_san_boundary_proof"

func (c *client) elasticSanBoundaryBinding(value asset.Asset, state map[string]any) string {
	return c.privateConfiguration(map[string]any{"protocol": "elastic-san-boundary-1", "id": value.Identity.NativeID, "connection": value.Identity.ConnectionID, "location": value.Location, "inventory": value.Normalized[elasticSanInventoryProof], "boundary": state})
}

// The signed native population is a review boundary, not permission to delete
// the SAN or evidence that retained children can be permanently removed.
func (c *client) elasticSanBoundaryRecorded(value asset.Asset) (map[string]any, error) {
	if _, err := c.elasticSanRecorded(value); err != nil {
		return nil, err
	}
	state := object(value.Normalized[elasticSanBoundary])
	complete, ok := state["complete"].(bool)
	members, unavailable := object(state["members"]), object(state["unavailable_snapshot_groups"])
	if value.Identity.NativeType != elasticSanType || len(state) != 3 || !ok || members == nil || unavailable == nil || complete != (len(unavailable) == 0) || value.Normalized[elasticSanBoundaryProof] != c.elasticSanBoundaryBinding(value, state) {
		return nil, serviceDenied("elastic_san_boundary_changed")
	}
	for id, entry := range members {
		member := object(entry)
		kind := text(member["kind"])
		canonical, err := c.elasticSanIdentity(id, kind)
		retained, isBool := member["retained"].(bool)
		if err != nil || canonical != id || kind == elasticSanType || elasticSanKind(kind) == "" || elasticSanRoot(id) != value.Identity.NativeID || len(member) != 3 || !isBool || retained && kind != elasticSanGroupType && kind != elasticSanVolumeType || text(member["configuration"]) == "" {
			return nil, serviceDenied("elastic_san_boundary_member_changed")
		}
	}
	for group, reason := range unavailable {
		member := object(members[group])
		if member["kind"] != elasticSanGroupType || member["retained"] != true || reason != "snapshot_index_not_found" {
			return nil, serviceDenied("elastic_san_boundary_availability_changed")
		}
	}
	return state, nil
}

func (c *client) elasticSanBoundaryState(id string, nodes map[string]elasticSanObservation, snapshotUnavailable map[string]bool) map[string]any {
	members, unavailable := map[string]any{}, map[string]any{}
	for child, observation := range nodes {
		if child == id || elasticSanRoot(child) != id {
			continue
		}
		_, typ, _ := parseID(child)
		kind := elasticSanKind(typ)
		// Bind the full private native read, including incarnation, retention policy,
		// network mappings and mutable configuration, without exposing those fields.
		members[child] = map[string]any{"kind": kind, "retained": observation.retained, "configuration": c.privateConfiguration(observation.raw)}
		if kind == elasticSanGroupType && snapshotUnavailable[child] {
			unavailable[child] = "snapshot_index_not_found"
		}
	}
	return map[string]any{"complete": len(unavailable) == 0, "members": members, "unavailable_snapshot_groups": unavailable}
}
