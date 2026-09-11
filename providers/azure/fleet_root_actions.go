package azure

import (
	"context"
	"maps"
	"slices"
	"strings"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func (a *fleetAction) rootState(value asset.Asset) (map[string]any, error) {
	state, err := a.client.fleetRecordedHub(a.id, value.Normalized)
	if err != nil {
		return nil, err
	}
	if text(value.Normalized[fleetHubProof]) != text(a.planned.Normalized[fleetHubProof]) || state["mode"] != "none" && state["mode"] != "managed" || state["children"] == nil || state["members"] == nil {
		return nil, serviceDenied("fleet_root_ownership_requires_refresh")
	}
	if _, err := fleetRootChildHints(a.id, state); err != nil {
		return nil, err
	}
	return state, nil
}

// Only the exact authenticated Hub manifest can be delegated to Fleet. The
// independent Fleet children and ARM extensions must be deleted beforehand.
func (a *fleetAction) rootImpacts(request contracts.ActionRequest) (map[string]contracts.ActionImpact, error) {
	state, err := a.rootState(request.Asset)
	if err != nil {
		return nil, err
	}
	members := object(state["members"])
	impacts := map[string]contracts.ActionImpact{}
	seen := map[asset.AssetID]bool{request.Asset.ID: true}
	for _, impact := range request.LifecycleImpacts {
		value := impact.Asset
		id, kind, err := parseID(value.Identity.NativeID)
		member := object(members[id])
		group := text(member["group"])
		if err != nil || id != value.Identity.NativeID || value.ID == "" || seen[value.ID] || impacts[id].Asset.ID != "" || !strings.EqualFold(kind, value.Identity.NativeType) || !strings.EqualFold(kind, text(member["kind"])) || value.Identity.Provider != request.Asset.Identity.Provider || value.Identity.ConnectionID != a.connectionID || value.Identity.Partition != a.partition || impact.ControllerID != request.Asset.ID || !impact.Delete || text(member["lifecycle_configuration"]) == "" || group != text(state["group"]) && group != text(state["node_group"]) || fleetKind(kind).kind != "" || rbacResourceKind(kind) != "" || strings.EqualFold(kind, diagnosticSettingsType) {
			return nil, serviceDenied("invalid_fleet_root_lifecycle_impact")
		}
		if _, known := findType(kind); !known && !inResourceGroup(id, group) {
			return nil, serviceDenied("fleet_unknown_external_hub_member")
		}
		seen[value.ID], impacts[id] = true, impact
	}
	if len(impacts) != len(members) {
		return nil, serviceDenied("fleet_hub_member_missing_from_plan")
	}
	return impacts, nil
}

func fleetHubLifecycleProjection(state map[string]any) map[string]any {
	result := map[string]any{}
	for _, key := range []string{"mode", "location", "group", "cluster", "node_group"} {
		result[key] = state[key]
	}
	members := map[string]any{}
	for id, value := range object(state["members"]) {
		member := object(value)
		members[id] = map[string]any{"kind": member["kind"], "group": member["group"], "configuration": member["lifecycle_configuration"]}
	}
	result["members"] = members
	return result
}

func (a *fleetAction) rootPreflight(ctx context.Context, request contracts.ActionRequest, raw response, locks []any) (response, contracts.PreflightResult, error) {
	ctx = context.WithValue(ctx, fleetHubReadContextKey{}, true)
	state, err := a.rootState(request.Asset)
	if err != nil {
		return raw, contracts.PreflightResult{}, err
	}
	if object(raw.data["properties"])["provisioningState"] == "Deleting" {
		_, err := a.rootResidualReadback(ctx, request)
		return raw, contracts.PreflightResult{Allowed: err == nil}, err
	}
	var resources map[string]map[string]any
	for range 2 {
		groups, err := a.client.insightsGroups(ctx)
		if err != nil {
			return raw, contracts.PreflightResult{}, err
		}
		live, current, err := a.client.readFleetHub(ctx, a.id, raw.data, groups, state)
		if err != nil {
			return raw, contracts.PreflightResult{}, err
		}
		if len(object(live["children"])) != 0 {
			return raw, contracts.PreflightResult{}, serviceDenied("service_child_requires_prior_deletion")
		}
		if a.client.privateConfiguration(fleetHubLifecycleProjection(state)) != a.client.privateConfiguration(fleetHubLifecycleProjection(live)) {
			return raw, contracts.PreflightResult{}, serviceDenied("fleet_hub_lifecycle_changed")
		}
		resources = current
	}
	if state["mode"] == "managed" {
		for _, group := range []string{text(state["group"]), text(state["node_group"])} {
			part := request
			part.LifecycleImpacts = nil
			var members []map[string]any
			for _, impact := range request.LifecycleImpacts {
				id := impact.Asset.Identity.NativeID
				if text(object(object(state["members"])[id])["group"]) == group {
					part.LifecycleImpacts = append(part.LifecycleImpacts, impact)
					members = append(members, resources[id])
				}
			}
			reason, err := a.managedGroupResourcesPreflight(ctx, part, group, members, locks)
			if err != nil {
				return raw, contracts.PreflightResult{}, err
			}
			if reason != "" {
				return raw, contracts.PreflightResult{}, serviceDenied(reason)
			}
		}
	}
	return raw, contracts.PreflightResult{Allowed: true}, nil
}

// Root absence cannot stand in for any known typed resource's own GET. Native
// group absence is sufficient only for unknown kinds contained by that group.
func (a *fleetAction) rootResidualReadback(ctx context.Context, request contracts.ActionRequest) (contracts.ReadbackResult, error) {
	ctx = context.WithValue(ctx, fleetHubReadContextKey{}, true)
	state, err := a.rootState(request.Asset)
	if err != nil {
		return contracts.ReadbackResult{}, err
	}
	exists := false
	children := object(state["children"])
	for _, id := range slices.Sorted(maps.Keys(children)) {
		_, err := a.client.fleetRead(ctx, text(object(children[id])["kind"]), id)
		if isNotFound(err) {
			continue
		}
		if err != nil {
			return contracts.ReadbackResult{}, err
		}
		exists = true
	}
	members := object(state["members"])
	absentGroups := map[string]bool{}
	for _, id := range slices.Sorted(maps.Keys(members)) {
		member := object(members[id])
		kind := text(member["kind"])
		rule, known := findType(kind)
		if !known {
			continue
		}
		endpoint, err := a.client.resourceURL(rule, id)
		if err != nil {
			return contracts.ReadbackResult{}, err
		}
		live, err := a.client.request(ctx, "GET", endpoint)
		if isNotFound(err) {
			if strings.EqualFold(kind, groupType) {
				absentGroups[id] = true
			}
			continue
		}
		if err != nil {
			return contracts.ReadbackResult{}, err
		}
		if !insightsARMReadValid(live, id, kind) || text(member["lifecycle_configuration"]) != a.client.privateConfiguration(fleetHubLifecycleSnapshot(live.data)) {
			return contracts.ReadbackResult{}, serviceDenied("fleet_hub_residual_configuration_changed")
		}
		exists = true
	}
	for _, value := range members {
		member := object(value)
		if _, known := findType(text(member["kind"])); !known && !absentGroups[text(member["group"])] {
			exists = true
		}
	}
	if exists {
		return contracts.ReadbackResult{Exists: true, State: "deleting_fleet_resources"}, nil
	}
	return contracts.ReadbackResult{Exists: false}, nil
}
