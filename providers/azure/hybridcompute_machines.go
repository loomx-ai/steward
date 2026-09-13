package azure

import (
	"context"
	"maps"
	"slices"
	"strings"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

const hybridComputeMachinePrefix = "machine-v1:"

func hybridComputeMachineSnapshot(raw map[string]any) map[string]any {
	out := hybridComputeChildSnapshot(raw)
	props := object(out["properties"])
	// These are child projections or connection/operation observations. Retain
	// the registration identity and all other authored and unknown configuration.
	for _, key := range []string{"extensions", "licenseProfile", "status", "lastStatusChange", "errorDetails"} {
		delete(props, key)
	}
	return out
}

func hybridComputeMachineProtection(raw map[string]any) string {
	if reason := hybridComputeChildProtection(raw, raw); reason != "" {
		return reason
	}
	// Azure Local deletion can remove the underlying VM. Other VM-controller
	// variants must likewise use their controller lifecycle, not this ordinary
	// registration cleanup. AWS/GCP registrations retain their external hosts.
	if !slices.Contains([]string{"", "aws", "gcp"}, strings.ToLower(text(raw["kind"]))) || text(object(raw["properties"])["parentClusterResourceId"]) != "" {
		return "hybrid_compute_machine_controller_required"
	}
	return ""
}

func (c *client) hybridComputeMachineBinding(id string, connection asset.ConnectionID, state map[string]any) string {
	return c.privateConfiguration(map[string]any{"protocol": "hybrid-compute-machine-1", "id": id, "connection": connection, "state": state})
}

func (c *client) hybridComputeMachineRecorded(id string, connection asset.ConnectionID, normalized map[string]any) error {
	canonical, err := c.hybridComputeIdentity(id, hybridMachineType)
	state := object(normalized[hybridComputeCleanup])
	_, protected := state["protected"].(bool)
	members, memberMap := state["members"].(map[string]any)
	location := text(state["location"])
	inventory := text(state["inventory"])
	if err != nil || canonical != id || connection == "" || (len(state) != 7 && len(state) != 8) || !protected || !memberMap || location == "" || location != strings.ToLower(location) || text(state["resource"]) == "" || text(state["registration"]) == "" || text(state["etag"]) == "" || inventory != text(normalized["_hybrid_compute_configuration"]) || !strings.HasPrefix(inventory, hybridComputeMachinePrefix) || normalized["cleanup_protected"] != state["protected"] || normalized[hybridComputeCleanupProof] != c.hybridComputeMachineBinding(id, connection, state) {
		return serviceDenied("invalid_hybrid_compute_machine_record")
	}
	if (len(state) == 8) != (state["local_vm"] != nil) {
		return serviceDenied("invalid_hybrid_compute_machine_context")
	}
	if err := c.azureLocalRegistrationRecorded(id, state); err != nil {
		return err
	}
	for childID, value := range members {
		entry := object(value)
		child, typ, err := parseID(childID)
		kind := hybridComputeKind(typ)
		if err != nil || child != childID || !hybridComputeChild(kind) || hybridComputeParent(childID, kind) != id || len(entry) != 2 || entry["kind"] != kind || text(entry["configuration"]) == "" {
			return serviceDenied("invalid_hybrid_compute_machine_member")
		}
	}
	return nil
}

// Native indexes discover new children; saved IDs recover omitted children.
// When the parent is absent, only the saved children's own reads remain valid.
func (c *client) hybridComputeMachineChildren(ctx context.Context, id string, known map[string]any, list bool) (map[string]map[string]any, error) {
	canonical, err := c.hybridComputeIdentity(id, hybridMachineType)
	if err != nil || canonical != id {
		return nil, serviceDenied("invalid_hybrid_compute_machine_scope")
	}
	ids, listed := map[string]string{}, map[string]map[string]any{}
	for childID, value := range known {
		child, typ, err := parseID(childID)
		kind := hybridComputeKind(typ)
		if err != nil || child != childID || !hybridComputeChild(kind) || hybridComputeParent(childID, kind) != id || object(value)["kind"] != kind {
			return nil, serviceDenied("invalid_hybrid_compute_child_hint")
		}
		ids[childID] = kind
	}
	if list {
		for _, kind := range []string{hybridExtensionType, hybridCommandType, hybridProfileType} {
			rows, err := c.hybridComputeIndex(ctx, kind, id)
			if err != nil {
				return nil, err
			}
			for _, value := range rows {
				raw := object(value)
				child, err := c.hybridComputeIdentity(text(raw["id"]), kind)
				if err != nil || hybridComputeParent(child, kind) != id || listed[child] != nil || !strings.EqualFold(text(raw["type"]), kind) {
					return nil, serviceDenied("invalid_hybrid_compute_machine_child_index")
				}
				ids[child], listed[child] = kind, raw
			}
		}
	}
	out := map[string]map[string]any{}
	for _, child := range slices.Sorted(maps.Keys(ids)) {
		res, err := c.hybridComputeRead(ctx, child, ids[child])
		if isNotFound(err) && listed[child] == nil {
			continue
		}
		if err != nil {
			return nil, err
		}
		if listed[child] != nil && serviceListedIncarnation(listed[child], res.data) != nil {
			return nil, serviceDenied("hybrid_compute_machine_child_incarnation_changed")
		}
		out[child] = res.data
	}
	return out, nil
}

func (c *client) hybridComputeMachineMembers(values map[string]map[string]any) map[string]any {
	out := map[string]any{}
	for id, raw := range values {
		out[id] = map[string]any{"kind": hybridComputeKind(text(raw["type"])), "configuration": c.privateConfiguration(hybridComputeChildSnapshot(raw))}
	}
	return out
}

func (a *hybridComputeAction) machineRequest(request contracts.ActionRequest) error {
	state := object(request.Asset.Normalized[hybridComputeCleanup])
	seen, assets := map[string]bool{a.id: true}, map[asset.AssetID]bool{a.planned.ID: true}
	for _, prerequisite := range request.PrerequisiteDeletions {
		child := prerequisite.Asset
		if !prerequisite.Delete || prerequisite.ControllerID != a.planned.ID || seen[child.Identity.NativeID] || assets[child.ID] || (!hybridComputeChild(child.Identity.NativeType) && child.Identity.NativeType != azureLocalVMType) || child.Identity.ConnectionID != request.Asset.Identity.ConnectionID || child.Identity.Partition != request.Asset.Identity.Partition || child.Location != request.Asset.Location {
			return serviceDenied("invalid_hybrid_compute_machine_prerequisite")
		}
		if child.Identity.NativeType == azureLocalVMType {
			if err := a.client.azureLocalRegistrationVM(request.Asset, child); err != nil {
				return err
			}
			if child.Normalized["cleanup_protected"] != false {
				return serviceDenied("azure_local_registration_vm_protected")
			}
			seen[child.Identity.NativeID], assets[child.ID] = true, true
			continue
		}
		if err := a.client.hybridComputeCleanupRecord(child); err != nil {
			return err
		}
		if hybridComputeParent(child.Identity.NativeID, child.Identity.NativeType) != a.id || object(child.Normalized[hybridComputeCleanup])["parent"] != state["registration"] {
			return serviceDenied("hybrid_compute_machine_prerequisite_parent_changed")
		}
		seen[child.Identity.NativeID], assets[child.ID] = true, true
	}
	return nil
}

func (a *hybridComputeAction) machineKnown(request contracts.ActionRequest) map[string]any {
	out := maps.Clone(object(object(request.Asset.Normalized[hybridComputeCleanup])["members"]))
	for _, prerequisite := range request.PrerequisiteDeletions {
		child := prerequisite.Asset
		if child.Identity.NativeType == azureLocalVMType {
			continue
		}
		out[child.Identity.NativeID] = map[string]any{"kind": child.Identity.NativeType, "configuration": object(child.Normalized[hybridComputeCleanup])["resource"]}
	}
	return out
}

func (a *hybridComputeAction) machineObserve(ctx context.Context, request contracts.ActionRequest) (map[string]any, map[string]map[string]any, error) {
	res, err := a.client.hybridComputeRead(ctx, a.id, hybridMachineType)
	if err != nil && !isNotFound(err) {
		return nil, nil, err
	}
	var raw map[string]any
	if err == nil {
		raw = res.data
		state := object(request.Asset.Normalized[hybridComputeCleanup])
		if a.client.privateConfiguration(hybridComputeMachineSnapshot(raw)) != state["resource"] || a.client.privateConfiguration(hybridComputeParentStamp(raw)) != state["registration"] || resourceRegion(raw) != request.Asset.Location {
			return nil, nil, serviceDenied("hybrid_compute_machine_configuration_changed")
		}
	}
	known := a.machineKnown(request)
	children, err := a.client.hybridComputeMachineChildren(ctx, a.id, known, raw != nil)
	if err != nil {
		return raw, nil, err
	}
	for id, child := range children {
		expected := object(known[id])
		if expected == nil || a.client.privateConfiguration(hybridComputeChildSnapshot(child)) != expected["configuration"] {
			return raw, children, serviceDenied("hybrid_compute_machine_children_changed")
		}
	}
	local, err := a.client.azureLocalRegistrationObserve(ctx, object(object(request.Asset.Normalized[hybridComputeCleanup])["local_vm"]), request.Asset.Location)
	if err != nil {
		return raw, children, err
	}
	maps.Copy(children, local)
	return raw, children, nil
}

func (a *hybridComputeAction) machinePreflight(ctx context.Context, request contracts.ActionRequest) (contracts.PreflightResult, error) {
	for range 2 {
		raw, children, err := a.machineObserve(ctx, request)
		if err != nil {
			return contracts.PreflightResult{}, err
		}
		if raw == nil || strings.EqualFold(text(object(raw["properties"])["provisioningState"]), "Deleting") {
			return contracts.PreflightResult{Allowed: true, Absent: raw == nil && len(children) == 0, Evidence: map[string]any{"hybrid_compute_wait": true}}, nil
		}
		if len(children) != 0 {
			return contracts.PreflightResult{}, serviceDenied("hybrid_compute_machine_prerequisite_still_exists")
		}
		if reason := hybridComputeRegistrationProtection(raw, object(request.Asset.Normalized[hybridComputeCleanup])); reason != "" {
			return contracts.PreflightResult{}, serviceDenied(reason)
		}
		state := object(request.Asset.Normalized[hybridComputeCleanup])
		// Reviewed/previously known child removal can update the parent's ETag.
		// Its immutable registration and authored configuration stay bound above.
		if len(a.machineKnown(request)) == 0 && state["local_vm"] == nil && a.client.privateConfiguration(map[string]any{"etag": raw["etag"], "eTag": raw["eTag"]}) != state["etag"] {
			return contracts.PreflightResult{}, serviceDenied("hybrid_compute_machine_etag_changed")
		}
		if err := a.protection(ctx, raw, raw); err != nil {
			return contracts.PreflightResult{}, err
		}
	}
	return contracts.PreflightResult{Allowed: true}, nil
}
