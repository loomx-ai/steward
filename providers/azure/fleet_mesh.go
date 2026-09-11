package azure

import (
	"context"
	"maps"
	"slices"
	"strings"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

const (
	fleetMeshState = "_fleet_mesh"
	fleetMeshProof = "_fleet_mesh_binding"
)

func (c *client) fleetMeshBinding(id string, normalized map[string]any) string {
	return c.privateConfiguration(map[string]any{"id": id, "configuration": normalized[fleetConfigurationProof], "context": normalized[fleetContextProof], "references": normalized[fleetReferencesProof], "mesh": normalized[fleetMeshState], "protocol": "fleet-mesh-1"})
}

func (c *client) fleetRecordedMesh(id string, normalized map[string]any) (map[string]any, error) {
	state, ok := normalized[fleetMeshState].(map[string]any)
	if !ok || state == nil || object(state["members"]) == nil || text(state["applied_selector"]) == "" || text(normalized[fleetConfigurationProof]) == "" || text(normalized[fleetContextProof]) == "" || text(normalized[fleetReferencesProof]) == "" || text(normalized[fleetMeshProof]) != c.fleetMeshBinding(id, normalized) {
		return nil, serviceDenied("fleet_mesh_proof_changed")
	}
	return state, nil
}

func (c *client) fleetMeshMemberSnapshot(raw map[string]any) map[string]any {
	mesh := maps.Clone(object(object(raw["properties"])["meshProperties"]))
	delete(mesh, "status")
	cluster, _, _ := parseID(text(object(raw["properties"])["clusterResourceId"]))
	return map[string]any{"cluster": cluster, "configuration": c.privateConfiguration(fleetSnapshot(fleetMemberType, raw)), "association": c.privateConfiguration(mesh)}
}

// The association on a member is independent evidence for an omitted profile.
// Neither a missing profile collection row nor a profile's own 404 can erase a
// surviving mesh attachment from that member.
func (c *client) fleetMeshes(ctx context.Context, parent string, known []asset.Asset) (map[string]map[string]any, string, error) {
	profiles, requestID, err := c.fleetIndex(ctx, fleetMeshType, parent)
	if err != nil {
		return nil, "", err
	}
	profiles, err = c.fleetRecoverKnown(ctx, fleetMeshType, parent, profiles, known)
	if err != nil {
		return nil, "", err
	}
	members, err := c.fleetKnownIndex(ctx, fleetMemberType, parent, known)
	if err != nil {
		return nil, "", err
	}
	for _, raw := range members {
		id, err := fleetMemberMesh(raw)
		if err != nil {
			return nil, "", err
		}
		if id == "" || profiles[id] != nil {
			continue
		}
		profile, err := c.fleetRead(ctx, fleetMeshType, id)
		if isNotFound(err) {
			return nil, "", serviceDenied("fleet_mesh_attachment_has_missing_profile")
		}
		if err != nil {
			return nil, "", err
		}
		profiles[id] = profile.data
	}
	return profiles, requestID, nil
}

// Unfiltered native member lists describe applied membership. Labels and the
// profile's selector describe a proposed selection and never prove attachment.
func (c *client) readFleetMesh(ctx context.Context, id string, raw map[string]any, previous map[string]any, known ...asset.Asset) (map[string]any, map[string]map[string]any, error) {
	if fleetValidate(fleetMeshType, raw) != nil || !strings.EqualFold(text(raw["id"]), id) {
		return nil, nil, serviceDenied("invalid_fleet_mesh_observation")
	}
	hints := map[string]asset.Asset{}
	for _, value := range known {
		if value.Identity.NativeType == fleetMemberType && fleetParent(value.Identity.NativeID, fleetMemberType) == fleetParent(id, fleetMeshType) {
			hints[value.Identity.NativeID] = value
		}
	}
	for member := range object(previous["members"]) {
		canonical, kind, err := fleetIdentity(member)
		if err != nil || canonical != member || kind != fleetMemberType || fleetParent(member, kind) != fleetParent(id, fleetMeshType) {
			return nil, nil, serviceDenied("invalid_fleet_mesh_known_member")
		}
		if _, exists := hints[member]; !exists {
			hints[member] = asset.Asset{Identity: asset.Identity{Provider: asset.ProviderAzure, NativeType: fleetMemberType, NativeID: member}}
		}
	}
	members, err := c.fleetKnownIndex(ctx, fleetMemberType, fleetParent(id, fleetMeshType), slices.Collect(maps.Values(hints)))
	if err != nil {
		return nil, nil, err
	}
	state := map[string]any{"members": map[string]any{}, "applied_selector": c.fleetMeshAppliedSelector(raw)}
	selected := map[string]map[string]any{}
	for member, value := range members {
		profile, err := fleetMemberMesh(value)
		if err != nil {
			return nil, nil, err
		}
		if profile == id {
			object(state["members"])[member] = c.fleetMeshMemberSnapshot(value)
			selected[member] = value
		}
	}
	for _, member := range slices.Sorted(maps.Keys(selected)) {
		current, err := c.fleetRead(ctx, fleetMemberType, member)
		if err != nil {
			return nil, nil, err
		}
		if c.privateConfiguration(c.fleetMeshMemberSnapshot(current.data)) != c.privateConfiguration(object(object(state["members"])[member])) {
			return nil, nil, serviceDenied("fleet_mesh_member_changed")
		}
		selected[member] = current.data
	}
	current, err := c.fleetRead(ctx, fleetMeshType, id)
	if err != nil {
		return nil, nil, err
	}
	if c.privateConfiguration(fleetSnapshot(fleetMeshType, current.data)) != c.privateConfiguration(fleetSnapshot(fleetMeshType, raw)) || c.fleetMeshAppliedSelector(current.data) != state["applied_selector"] {
		return nil, nil, serviceDenied("fleet_mesh_profile_changed")
	}
	return state, selected, nil
}

func (c *client) fleetMeshAppliedSelector(raw map[string]any) string {
	return c.privateConfiguration(map[string]any{"selector": object(object(raw["properties"])["status"])["lastAppliedMemberSelector"]})
}

func (r *Runtime) fleetMeshInventory(ctx context.Context, c *client, item *contracts.InventoryItem, raw map[string]any, normalized map[string]any) error {
	var previous map[string]any
	if normalized[fleetMeshState] != nil || normalized[fleetMeshProof] != nil {
		var err error
		previous, err = c.fleetRecordedMesh(item.NativeID, normalized)
		if err != nil {
			return err
		}
	}
	state, _, err := c.readFleetMesh(ctx, item.NativeID, raw, previous)
	if err != nil {
		return err
	}
	members := slices.Sorted(maps.Keys(object(state["members"])))
	refs := object(item.Normalized["_fleet_references"])
	if len(members) != 0 {
		refs[fleetMemberType] = members
		item.Normalized[referenceKey(fleetMemberType)] = members
		item.NetworkReferences = append(item.NetworkReferences, members...)
		slices.Sort(item.NetworkReferences)
	}
	item.Normalized["mesh_member_count"] = len(members)
	item.Normalized[fleetMeshState] = state
	item.Normalized[fleetReferencesProof] = c.fleetReferenceBinding(item.NativeID, fleetMeshType, item.Location, text(item.Normalized[fleetConfigurationProof]), text(item.Normalized[fleetContextProof]), refs, nil)
	item.Normalized[fleetMeshProof] = c.fleetMeshBinding(item.NativeID, item.Normalized)
	return nil
}

func fleetMeshSelector(value any) error {
	selector, ok := value.(map[string]any)
	label, stringOK := selector["byLabel"].(string)
	if !ok || !stringOK || len(label) > 512 || monitorRuleFields(selector, "byLabel") != nil {
		return serviceDenied("invalid_fleet_mesh_selector")
	}
	return nil
}

func fleetMeshValidate(raw map[string]any) error {
	props := object(raw["properties"])
	if value, present := props["memberSelector"]; present {
		if err := fleetMeshSelector(value); err != nil {
			return err
		}
	}
	if value, present := props["status"]; present {
		status, ok := value.(map[string]any)
		if !ok || text(status["state"]) == "" || status["state"] != text(status["state"]) || monitorRuleFields(status, "state", "lastAppliedMemberSelector", "lastOperationId", "lastOperationError") != nil {
			return serviceDenied("invalid_fleet_mesh_status")
		}
		if value, present := status["lastAppliedMemberSelector"]; present {
			return fleetMeshSelector(value)
		}
	}
	return nil
}

func fleetMemberMesh(raw map[string]any) (string, error) {
	value, present := object(raw["properties"])["meshProperties"]
	if !present {
		return "", nil
	}
	mesh, ok := value.(map[string]any)
	if !ok || monitorRuleFields(mesh, "ciliumProperties", "status", "clusterMeshProfileResourceId") != nil {
		return "", serviceDenied("invalid_fleet_mesh_member")
	}
	cilium, status := object(mesh["ciliumProperties"]), object(mesh["status"])
	cluster, err := batchInteger(cilium["id"], 32)
	if err != nil || cluster < 1 || cluster > 255 || text(cilium["name"]) == "" || cilium["name"] != text(cilium["name"]) || monitorRuleFields(cilium, "id", "name") != nil || text(status["state"]) == "" || status["state"] != text(status["state"]) || monitorRuleFields(status, "state", "lastUpdatedAt", "lastOperationId", "error") != nil {
		return "", serviceDenied("invalid_fleet_mesh_member_state")
	}
	wire, ok := mesh["clusterMeshProfileResourceId"].(string)
	id, kind, err := fleetIdentity(wire)
	member, memberKind, memberErr := fleetIdentity(text(raw["id"]))
	if !ok || err != nil || kind != fleetMeshType || memberErr != nil || memberKind != fleetMemberType || !strings.EqualFold(fleetParent(id, kind), fleetParent(member, memberKind)) {
		return "", serviceDenied("invalid_fleet_mesh_member_profile")
	}
	return id, nil
}
