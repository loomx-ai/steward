package azure

import (
	"context"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func (c *client) elasticSanGroupProtection(raw map[string]any, retained bool, state map[string]any) string {
	if reason := protectionReason(resourceType{NativeType: elasticSanGroupType}, raw); reason != "" {
		return reason
	}
	if retained {
		return "elastic_san_retained_group_purge_unverified"
	}
	if state["complete"] != true {
		return "elastic_san_group_members_unverified"
	}
	if _, err := time.Parse(time.RFC3339Nano, text(object(raw["systemData"])["createdAt"])); err != nil {
		return "elastic_san_group_creation_unverified"
	}
	if !slices.Contains([]string{"Succeeded", "Failed", "Canceled", "Deleting"}, text(object(raw["properties"])["provisioningState"])) {
		return "elastic_san_group_not_ready"
	}
	for _, entry := range object(state["connections"]) {
		if object(entry)["mapped"] != true {
			return "elastic_san_group_connection_unverified"
		}
	}
	for _, entry := range object(state["members"]) {
		if object(entry)["retained"] == true && object(state["policy"])["policyState"] != "Enabled" {
			return "elastic_san_group_retained_member_policy_unverified"
		}
	}
	return ""
}

// Group retention preserves the ARM ID. Both native populations and the own
// read must agree before a terminal retained outcome can replace active absence.
func (a *elasticSanChildAction) groupObservation(ctx context.Context) (map[string]any, contracts.ReadbackResult, error) {
	id := a.planned.Identity.NativeID
	selected := elasticSanObservation{}
	for _, retained := range []bool{false, true} {
		rows, _, err := a.client.elasticSanIndex(ctx, elasticSanGroupType, elasticSanRoot(id), retained)
		if err != nil {
			return nil, contracts.ReadbackResult{}, err
		}
		for _, row := range rows {
			raw := object(row)
			candidate, readErr := a.client.elasticSanRecord(raw, elasticSanGroupType)
			if readErr != nil {
				return nil, contracts.ReadbackResult{}, readErr
			}
			if candidate != id {
				continue
			}
			if selected.raw != nil {
				return nil, contracts.ReadbackResult{}, serviceDenied("elastic_san_group_duplicate_population")
			}
			selected = elasticSanObservation{raw: raw, retained: retained}
		}
	}
	own, err := a.client.elasticSanRead(ctx, id, elasticSanGroupType)
	if err != nil && !isNotFound(err) {
		return nil, contracts.ReadbackResult{}, err
	}
	if err == nil {
		if selected.raw != nil && (serviceListedIncarnation(selected.raw, own.data) != nil || !nativeConfigurationContains(object(selected.raw["properties"]), object(own.data["properties"]))) {
			return nil, contracts.ReadbackResult{}, serviceDenied("elastic_san_group_index_changed")
		}
		if selected.raw == nil {
			selected.retained = slices.Contains([]string{"Deleted", "SoftDeleting"}, text(object(own.data["properties"])["provisioningState"]))
		}
		selected.raw = own.data
	} else if selected.raw != nil && !selected.retained {
		return nil, contracts.ReadbackResult{}, serviceDenied("elastic_san_active_group_missing")
	}
	if selected.raw == nil {
		return nil, contracts.ReadbackResult{Data: map[string]any{"outcome": "absent"}}, nil
	}
	state := object(a.planned.Normalized[elasticSanSnapshotCleanup])
	if a.client.privateConfiguration(hybridComputeChildSnapshot(selected.raw)) != state["resource"] {
		return nil, contracts.ReadbackResult{}, serviceDenied("elastic_san_group_configuration_changed")
	}
	status := text(object(selected.raw["properties"])["provisioningState"])
	if selected.retained && status == "Deleted" {
		if object(object(a.planned.Normalized[elasticSanGroupContext])["policy"])["policyState"] == "Disabled" {
			return nil, contracts.ReadbackResult{}, serviceDenied("elastic_san_group_retention_changed")
		}
		return nil, contracts.ReadbackResult{State: status, Data: map[string]any{"outcome": "soft_deleted", "retained_native_id": id}}, nil
	}
	if selected.retained && status != "SoftDeleting" && status != "Deleting" {
		return nil, contracts.ReadbackResult{}, serviceDenied("elastic_san_group_retained_state_changed")
	}
	return selected.raw, contracts.ReadbackResult{Exists: true, State: status}, nil
}

// Read the complete child boundary and recover signed known IDs. An unavailable
// collection stays an error; parent disappearance cannot prove retained absence.
func (a *elasticSanChildAction) groupMembers(ctx context.Context, terminal bool) (map[string]elasticSanObservation, error) {
	id := a.planned.Identity.NativeID
	state := object(a.planned.Normalized[elasticSanGroupContext])
	known := maps.Clone(object(state["members"]))
	if known == nil {
		known = map[string]any{}
	}
	for child := range object(state["connections"]) {
		known[child] = map[string]any{"kind": elasticSanEndpointType}
	}
	nodes := map[string]elasticSanObservation{}
	read := func(child, kind string, listed map[string]any, retained bool) error {
		own, err := a.client.elasticSanRead(ctx, child, kind)
		if isNotFound(err) {
			if listed == nil {
				return nil
			}
			if !retained {
				return serviceDenied("elastic_san_group_listed_child_missing")
			}
			nodes[child] = elasticSanObservation{raw: listed, retained: true, authority: "retained-index"}
			return nil
		}
		if err != nil {
			return err
		}
		if listed != nil && (serviceListedIncarnation(listed, own.data) != nil || !nativeConfigurationContains(object(listed["properties"]), object(own.data["properties"]))) {
			return serviceDenied("elastic_san_group_child_index_changed")
		}
		status := text(object(own.data["properties"])["provisioningState"])
		if kind == elasticSanVolumeType {
			if slices.Contains([]string{"Deleted", "SoftDeleting"}, status) {
				if listed != nil && !retained {
					return serviceDenied("elastic_san_group_child_population_changed")
				}
				retained = true
			} else if listed == nil && status == "Deleting" {
				retained = object(known[child])["retained"] == true
			}
		}
		nodes[child] = elasticSanObservation{raw: own.data, retained: retained, authority: "get"}
		return nil
	}
	for _, kind := range []string{elasticSanVolumeType, elasticSanSnapshotType, elasticSanEndpointType} {
		parent := id
		if kind == elasticSanEndpointType {
			parent = elasticSanRoot(id)
		}
		modes := []bool{false}
		if kind == elasticSanVolumeType {
			modes = append(modes, true)
		}
		for _, retained := range modes {
			rows, _, err := a.client.elasticSanIndex(ctx, kind, parent, retained)
			if err != nil {
				// Only after verifying the group's terminal native outcome may
				// missing snapshot indexes fall back to every signed known own
				// GET. Volume populations still must verify retained identities.
				if terminal && kind == elasticSanSnapshotType && isNotFound(err) {
					continue
				}
				return nil, err
			}
			for _, row := range rows {
				raw := object(row)
				child, err := a.client.elasticSanRecord(raw, kind)
				if err != nil {
					return nil, err
				}
				if nodes[child].raw != nil {
					return nil, serviceDenied("elastic_san_group_child_duplicate_population")
				}
				if err := read(child, kind, raw, retained); err != nil {
					return nil, err
				}
			}
		}
	}
	for child, entry := range known {
		if nodes[child].raw == nil {
			if err := read(child, text(object(entry)["kind"]), nil, false); err != nil {
				return nil, err
			}
		}
	}
	// Connections mapped exclusively to other groups are outside this boundary.
	for child, observation := range nodes {
		_, kind, _ := parseID(child)
		if strings.EqualFold(kind, elasticSanEndpointType) {
			groups, err := a.client.elasticSanEndpointGroups(observation.raw)
			if err == nil && !slices.Contains(groups, id) && known[child] == nil {
				delete(nodes, child)
			}
		}
	}
	return nodes, nil
}

func (a *elasticSanChildAction) groupRequest(request contracts.ActionRequest) error {
	state := object(a.planned.Normalized[elasticSanGroupContext])
	members, connections := object(state["members"]), object(state["connections"])
	if len(request.Parameters) != 0 {
		return serviceDenied("elastic_san_group_options_unreviewed")
	}
	seen := map[string]bool{}
	for _, impact := range append(slices.Clone(request.PrerequisiteDeletions), request.LifecycleImpacts...) {
		child := impact.Asset
		id := child.Identity.NativeID
		member := object(members[id])
		retained := member["retained"] == true
		if seen[id] || child.ID == "" || impact.ControllerID != a.planned.ID || child.Identity.ConnectionID != a.planned.Identity.ConnectionID || impact.Delete == retained || member == nil && connections[id] == nil {
			return serviceDenied("elastic_san_group_review_changed")
		}
		if err := a.client.elasticSanChildRecord(child); err != nil {
			return err
		}
		if member != nil {
			if child.Identity.NativeType != member["kind"] || child.Normalized["retained"] != retained || object(child.Normalized[elasticSanSnapshotCleanup])["resource"] != member["configuration"] {
				return serviceDenied("elastic_san_group_member_review_changed")
			}
		} else if child.Identity.NativeType != elasticSanEndpointType || object(connections[id])["mapped"] != true {
			return serviceDenied("elastic_san_group_connection_review_changed")
		}
		seen[id] = true
	}
	for id, entry := range members {
		// A snapshot owned by a reviewed active volume is that volume's prerequisite.
		member := object(entry)
		source := object(members[text(member["source"])])
		if member["kind"] == elasticSanSnapshotType && source["kind"] == elasticSanVolumeType && source["retained"] == false {
			continue
		}
		if !seen[id] {
			return serviceDenied("elastic_san_group_member_unreviewed")
		}
	}
	for id := range connections {
		if !seen[id] {
			return serviceDenied("elastic_san_group_connection_unreviewed")
		}
	}
	return nil
}

func (a *elasticSanChildAction) groupPreflight(ctx context.Context, raw map[string]any) error {
	state := object(a.planned.Normalized[elasticSanGroupContext])
	if reason := a.client.elasticSanGroupProtection(raw, false, state); reason != "" {
		return serviceDenied(reason)
	}
	return a.groupChildrenFinished(ctx, false)
}

func (a *elasticSanChildAction) groupChildrenFinished(ctx context.Context, terminal bool) error {
	nodes, err := a.groupMembers(ctx, terminal)
	if err != nil {
		return err
	}
	expected := object(object(a.planned.Normalized[elasticSanGroupContext])["members"])
	policy := object(object(a.planned.Normalized[elasticSanGroupContext])["policy"])
	found := map[string]bool{}
	locks, err := a.client.managementLocks(ctx)
	if err != nil {
		return err
	}
	for id, observation := range nodes {
		_, typ, _ := parseID(id)
		kind := elasticSanKind(typ)
		if kind != elasticSanVolumeType {
			return serviceDenied("elastic_san_group_child_requires_cleanup")
		}
		if !observation.retained {
			return serviceDenied("elastic_san_group_active_volume_remains")
		}
		if reason := a.client.elasticSanVolumeProtection(observation.raw, true); reason != "" {
			return serviceDenied(reason)
		}
		if locked(id, locks) {
			return serviceDenied("azure_management_lock")
		}
		guid := text(object(observation.raw["properties"])["volumeId"])
		matched := ""
		for original, entry := range expected {
			member := object(entry)
			if member["kind"] == elasticSanVolumeType && member["volumeId"] == guid && guid != "" {
				if matched != "" || found[original] {
					return serviceDenied("elastic_san_group_member_identity_ambiguous")
				}
				if member["configuration"] != a.client.privateConfiguration(elasticSanVolumeSnapshot(observation.raw)) || member["retained"] == true && id != original || policy["policyState"] == "Disabled" {
					return serviceDenied("elastic_san_group_retained_member_changed")
				}
				matched = original
			}
		}
		if matched == "" {
			return serviceDenied("elastic_san_group_new_retained_member")
		}
		found[matched] = true
	}
	for id, entry := range expected {
		if object(entry)["retained"] == true && !found[id] {
			return serviceDenied("elastic_san_group_retained_member_missing")
		}
	}
	return nil
}

// Keep the signed active identity separate from a same-ID retained observation.
func elasticSanGroupCleanup(c *client, value asset.Asset, raw, context map[string]any, retained bool) (map[string]any, string) {
	reason := c.elasticSanGroupProtection(raw, retained, context)
	state := map[string]any{"resource": c.privateConfiguration(hybridComputeChildSnapshot(raw)), "etag": c.privateConfiguration(elasticSanChildVersion(elasticSanGroupType, raw)), "protected": reason != "", "group": value.Normalized[elasticSanGroupContextProof]}
	return state, reason
}
