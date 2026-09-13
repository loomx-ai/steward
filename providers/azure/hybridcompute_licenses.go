package azure

import (
	"context"
	"maps"
	"slices"
	"strings"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

const hybridComputeLicensePrefix = "license-v1:"

func hybridComputeLicenseSnapshot(raw map[string]any) map[string]any {
	out := hybridComputeChildSnapshot(raw)
	// Assignment counts change when separately reviewed consumers are removed.
	delete(object(object(out["properties"])["licenseDetails"]), "assignedLicenses")
	return out
}

func (c *client) hybridComputeLicenseProtection(raw map[string]any) string {
	if reason := protectionReason(resourceType{NativeType: hybridLicenseType}, raw); reason != "" {
		return reason
	}
	props := object(raw["properties"])
	details := object(props["licenseDetails"])
	count, err := batchInteger(details["assignedLicenses"], 32)
	if text(props["licenseType"]) != "ESU" || !strings.EqualFold(text(props["tenantId"]), c.tenant) || text(details["immutableId"]) == "" || err != nil || count < 0 {
		return "hybrid_compute_license_identity_unverified"
	}
	return ""
}

func (c *client) hybridComputeLicenseBinding(id string, connection asset.ConnectionID, state map[string]any) string {
	return c.privateConfiguration(map[string]any{"protocol": "hybrid-compute-license-1", "id": id, "connection": connection, "state": state})
}

func (c *client) hybridComputeLicenseRecorded(id string, connection asset.ConnectionID, normalized map[string]any) error {
	canonical, err := c.hybridComputeIdentity(id, hybridLicenseType)
	state := object(normalized[hybridComputeCleanup])
	_, protected := state["protected"].(bool)
	assignments, valid := state["assignments"].(map[string]any)
	if err != nil || canonical != id || connection == "" || len(state) != 6 || !protected || !valid || text(state["resource"]) == "" || text(state["etag"]) == "" || text(state["location"]) == "" || state["location"] != strings.ToLower(text(state["location"])) || text(state["inventory"]) != text(normalized["_hybrid_compute_configuration"]) || !strings.HasPrefix(text(state["inventory"]), hybridComputeLicensePrefix) || normalized["cleanup_protected"] != state["protected"] || normalized[hybridComputeCleanupProof] != c.hybridComputeLicenseBinding(id, connection, state) {
		return serviceDenied("invalid_hybrid_compute_license_record")
	}
	for profile, value := range assignments {
		canonical, err := c.hybridComputeIdentity(profile, hybridProfileType)
		if err != nil || canonical != profile || text(value) == "" {
			return serviceDenied("invalid_hybrid_compute_license_assignment")
		}
	}
	return nil
}

// Licenses may serve other subscriptions in the same tenant. These indexes
// discover local consumers; the license's native assignedLicenses count is a
// separate, mandatory pre-delete guard for assignments outside this connection.
func (c *client) hybridComputeLicenseAssignments(ctx context.Context, id string, known map[string]any, list bool) (map[string]map[string]any, error) {
	profiles, listed := map[string]bool{}, map[string]map[string]any{}
	for profile := range known {
		canonical, err := c.hybridComputeIdentity(profile, hybridProfileType)
		if err != nil || canonical != profile {
			return nil, serviceDenied("invalid_hybrid_compute_license_profile_hint")
		}
		profiles[profile] = true
	}
	if list {
		machines, err := c.hybridComputeIndex(ctx, hybridMachineType, "")
		if err != nil {
			return nil, err
		}
		seen := map[string]bool{}
		for _, value := range machines {
			raw := object(value)
			machine, err := c.hybridComputeIdentity(text(raw["id"]), hybridMachineType)
			if err != nil || seen[machine] {
				return nil, serviceDenied("invalid_hybrid_compute_license_machine_index")
			}
			seen[machine] = true
			res, err := c.hybridComputeRead(ctx, machine, hybridMachineType)
			if err != nil {
				return nil, err
			}
			if !strings.EqualFold(text(raw["type"]), hybridMachineType) || serviceListedIncarnation(raw, res.data) != nil {
				return nil, serviceDenied("hybrid_compute_license_machine_changed")
			}
			rows, err := c.hybridComputeIndex(ctx, hybridProfileType, machine)
			if err != nil {
				return nil, err
			}
			for _, value := range rows {
				raw := object(value)
				profile, err := c.hybridComputeIdentity(text(raw["id"]), hybridProfileType)
				if err != nil || hybridComputeParent(profile, hybridProfileType) != machine || listed[profile] != nil || !strings.EqualFold(text(raw["type"]), hybridProfileType) {
					return nil, serviceDenied("invalid_hybrid_compute_license_profile_index")
				}
				profiles[profile], listed[profile] = true, raw
			}
		}
	}
	result := map[string]map[string]any{}
	for _, profile := range slices.Sorted(maps.Keys(profiles)) {
		res, err := c.hybridComputeRead(ctx, profile, hybridProfileType)
		if isNotFound(err) && listed[profile] == nil {
			continue
		}
		if err != nil {
			return nil, err
		}
		if listed[profile] != nil && serviceListedIncarnation(listed[profile], res.data) != nil {
			return nil, serviceDenied("hybrid_compute_license_profile_changed")
		}
		refs, err := hybridComputeReferences(profile, hybridProfileType, res.data)
		if err != nil {
			return nil, err
		}
		if slices.Contains(refs[hybridLicenseType], id) {
			result[profile] = res.data
		}
	}
	return result, nil
}

func (c *client) hybridComputeAssignmentRecords(values map[string]map[string]any) map[string]any {
	out := map[string]any{}
	for id, raw := range values {
		out[id] = c.privateConfiguration(hybridComputeChildSnapshot(raw))
	}
	return out
}

func (a *hybridComputeAction) licenseRequest(request contracts.ActionRequest) error {
	seen, assets := map[string]bool{}, map[asset.AssetID]bool{request.Asset.ID: true}
	for _, prerequisite := range request.PrerequisiteDeletions {
		child := prerequisite.Asset
		if !prerequisite.Delete || prerequisite.ControllerID != request.Asset.ID || child.Identity.NativeType != hybridProfileType || child.Identity.ConnectionID != request.Asset.Identity.ConnectionID || child.Identity.Partition != request.Asset.Identity.Partition || seen[child.Identity.NativeID] || assets[child.ID] {
			return serviceDenied("invalid_hybrid_compute_license_prerequisite")
		}
		if err := a.client.hybridComputeCleanupRecord(child); err != nil {
			return err
		}
		refs, err := a.client.hybridComputeRecordedReferences(child)
		if err != nil || !slices.Contains(refs[hybridLicenseType], a.id) {
			return serviceDenied("hybrid_compute_license_prerequisite_changed")
		}
		seen[child.Identity.NativeID], assets[child.ID] = true, true
	}
	return nil
}

func (a *hybridComputeAction) licenseKnown(request contracts.ActionRequest) map[string]any {
	known := maps.Clone(object(object(request.Asset.Normalized[hybridComputeCleanup])["assignments"]))
	for _, prerequisite := range request.PrerequisiteDeletions {
		known[prerequisite.Asset.Identity.NativeID] = object(prerequisite.Asset.Normalized[hybridComputeCleanup])["resource"]
	}
	return known
}

func (a *hybridComputeAction) licenseObserve(ctx context.Context, request contracts.ActionRequest) (map[string]any, map[string]map[string]any, error) {
	res, err := a.client.hybridComputeRead(ctx, a.id, hybridLicenseType)
	if err != nil && !isNotFound(err) {
		return nil, nil, err
	}
	var raw map[string]any
	if err == nil {
		raw = res.data
		if a.client.privateConfiguration(hybridComputeLicenseSnapshot(raw)) != object(request.Asset.Normalized[hybridComputeCleanup])["resource"] || resourceRegion(raw) != request.Asset.Location {
			return nil, nil, serviceDenied("hybrid_compute_license_configuration_changed")
		}
	}
	assignments, err := a.client.hybridComputeLicenseAssignments(ctx, a.id, a.licenseKnown(request), raw != nil)
	return raw, assignments, err
}

func (a *hybridComputeAction) licensePreflight(ctx context.Context, request contracts.ActionRequest) (contracts.PreflightResult, error) {
	for range 2 {
		raw, assignments, err := a.licenseObserve(ctx, request)
		if err != nil {
			return contracts.PreflightResult{}, err
		}
		if raw == nil || strings.EqualFold(text(object(raw["properties"])["provisioningState"]), "Deleting") {
			return contracts.PreflightResult{Allowed: true, Absent: raw == nil && len(assignments) == 0, Evidence: map[string]any{"hybrid_compute_wait": true}}, nil
		}
		if len(assignments) != 0 {
			return contracts.PreflightResult{}, serviceDenied("hybrid_compute_license_still_assigned")
		}
		if reason := a.client.hybridComputeLicenseProtection(raw); reason != "" {
			return contracts.PreflightResult{}, serviceDenied(reason)
		}
		count, _ := batchInteger(object(object(raw["properties"])["licenseDetails"])["assignedLicenses"], 32)
		if count != 0 {
			return contracts.PreflightResult{}, serviceDenied("hybrid_compute_license_external_assignments")
		}
		state := object(request.Asset.Normalized[hybridComputeCleanup])
		if len(a.licenseKnown(request)) == 0 && a.client.privateConfiguration(map[string]any{"etag": raw["etag"], "eTag": raw["eTag"]}) != state["etag"] {
			return contracts.PreflightResult{}, serviceDenied("hybrid_compute_license_etag_changed")
		}
		if err := a.protection(ctx, raw, nil); err != nil {
			return contracts.PreflightResult{}, err
		}
	}
	return contracts.PreflightResult{Allowed: true}, nil
}
