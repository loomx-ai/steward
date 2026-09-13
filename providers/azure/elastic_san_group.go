package azure

import (
	"maps"
	"strings"

	"github.com/loomx-ai/steward/internal/core/asset"
)

const elasticSanGroupContext = "_elastic_san_group_context"
const elasticSanGroupContextProof = "_elastic_san_group_context_proof"

func (c *client) elasticSanGroupBinding(value asset.Asset, state map[string]any) string {
	return c.privateConfiguration(map[string]any{"protocol": "elastic-san-group-1", "id": value.Identity.NativeID, "connection": value.Identity.ConnectionID, "location": value.Location, "inventory": value.Normalized[elasticSanInventoryProof], "context": state})
}

// This context records the native cascade boundary; it does not authorize an
// unregistered parent DELETE or infer ownership of the consumer Network endpoint.
func (c *client) elasticSanGroupRecorded(value asset.Asset) (map[string]any, error) {
	if _, err := c.elasticSanRecorded(value); err != nil {
		return nil, err
	}
	state := object(value.Normalized[elasticSanGroupContext])
	complete, ok := state["complete"].(bool)
	if value.Identity.NativeType != elasticSanGroupType || len(state) != 4 || !ok || object(state["members"]) == nil || object(state["connections"]) == nil || object(state["policy"]) == nil || value.Normalized[elasticSanGroupContextProof] != c.elasticSanGroupBinding(value, state) {
		return nil, serviceDenied("elastic_san_group_context_changed")
	}
	if value.Normalized["retained"] == true && complete {
		return nil, serviceDenied("elastic_san_retained_group_context_unverified")
	}
	for id, entry := range object(state["members"]) {
		member := object(entry)
		kind := text(member["kind"])
		canonical, err := c.elasticSanIdentity(id, kind)
		retained, isBool := member["retained"].(bool)
		if err != nil || canonical != id || kind != elasticSanVolumeType && kind != elasticSanSnapshotType || elasticSanParent(id, kind) != value.Identity.NativeID || len(member) != 5 || !isBool || kind == elasticSanSnapshotType && retained || text(member["configuration"]) == "" {
			return nil, serviceDenied("elastic_san_group_member_changed")
		}
		if kind == elasticSanVolumeType && (complete || text(member["volumeId"]) != "") && !uuidPattern.MatchString(text(member["volumeId"])) {
			return nil, serviceDenied("elastic_san_group_volume_identity_unverified")
		}
	}
	for id, entry := range object(state["connections"]) {
		canonical, err := c.elasticSanIdentity(id, elasticSanEndpointType)
		record := object(entry)
		_, ok := record["mapped"].(bool)
		if err != nil || canonical != id || elasticSanRoot(id) != elasticSanRoot(value.Identity.NativeID) || len(record) != 2 || !ok || text(record["configuration"]) == "" {
			return nil, serviceDenied("elastic_san_group_connection_changed")
		}
	}
	return state, nil
}

func (c *client) elasticSanGroupState(id string, raw map[string]any, retained bool, nodes map[string]elasticSanObservation, prior map[string]any) (map[string]any, error) {
	policy, err := elasticSanVolumePolicy(raw)
	if err != nil {
		return nil, err
	}
	members, connections := map[string]any{}, map[string]any{}
	if retained {
		// The retained group LIST is authoritative for the group, not its children.
		// Preserve signed historical membership as history, never as current absence.
		members = maps.Clone(object(prior["members"]))
		if members == nil {
			members = map[string]any{}
		}
		connections = maps.Clone(object(prior["connections"]))
		if connections == nil {
			connections = map[string]any{}
		}
		return map[string]any{"complete": false, "members": members, "connections": connections, "policy": policy}, nil
	}
	complete := true
	for child, observation := range nodes {
		_, typ, _ := parseID(child)
		kind := elasticSanKind(typ)
		if kind == elasticSanEndpointType && elasticSanRoot(child) == elasticSanRoot(id) {
			groups, err := c.elasticSanEndpointGroups(observation.raw)
			mapped := false
			for _, group := range groups {
				mapped = mapped || group == id
			}
			if mapped || err != nil {
				connections[child] = map[string]any{"mapped": mapped, "configuration": c.privateConfiguration(observation.raw)}
			}
			continue
		}
		if kind != elasticSanVolumeType && kind != elasticSanSnapshotType || elasticSanParent(child, kind) != id {
			continue
		}
		configuration := hybridComputeChildSnapshot(observation.raw)
		volumeID, source := "", ""
		if kind == elasticSanVolumeType {
			configuration = elasticSanVolumeSnapshot(observation.raw)
			volumeID = text(object(observation.raw["properties"])["volumeId"])
			if volumeID == "" {
				complete = false
			} else if !uuidPattern.MatchString(volumeID) {
				return nil, serviceDenied("elastic_san_group_volume_identity_unverified")
			}
		} else {
			source, typ, err = parseID(text(object(object(observation.raw["properties"])["creationData"])["sourceId"]))
			if err != nil || !strings.EqualFold(typ, elasticSanVolumeType) {
				return nil, serviceDenied("elastic_san_group_snapshot_source_unverified")
			}
		}
		members[child] = map[string]any{"kind": kind, "retained": observation.retained, "configuration": c.privateConfiguration(configuration), "volumeId": volumeID, "source": source}
	}
	return map[string]any{"complete": complete, "members": members, "connections": connections, "policy": policy}, nil
}
