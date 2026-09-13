package azure

import (
	"context"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func elasticSanVolumeSnapshot(raw map[string]any) map[string]any {
	out := hybridComputeChildSnapshot(raw)
	// Soft deletion changes the ARM name while preserving volumeId and createdAt.
	// The selected ARM path is independently bound by the inventory/action proof.
	delete(out, "id")
	delete(out, "name")
	target := object(object(out["properties"])["storageTarget"])
	delete(target, "status")
	delete(target, "provisioningState")
	return out
}

func elasticSanVolumePolicy(group map[string]any) (map[string]any, error) {
	policy := object(object(group["properties"])["deleteRetentionPolicy"])
	if policy == nil {
		return map[string]any{"policyState": "Unspecified"}, nil
	}
	switch policy["policyState"] {
	case "Disabled":
		return maps.Clone(policy), nil
	case "Enabled":
		days, err := batchInteger(policy["retentionPeriodDays"], 32)
		if err == nil && days > 0 {
			return maps.Clone(policy), nil
		}
	}
	return nil, serviceDenied("elastic_san_volume_retention_unverified")
}

func (c *client) elasticSanVolumeProtection(raw map[string]any, retained bool) string {
	if reason := protectionReason(resourceType{NativeType: elasticSanVolumeType}, raw); reason != "" {
		return reason
	}
	props := object(raw["properties"])
	if props["managedBy"] != nil {
		return "azure_elastic_san_volume_managed"
	}
	if !uuidPattern.MatchString(text(props["volumeId"])) {
		return "azure_elastic_san_volume_identity_unverified"
	}
	if _, err := time.Parse(time.RFC3339Nano, text(object(raw["systemData"])["createdAt"])); err != nil {
		return "azure_elastic_san_volume_creation_unverified"
	}
	state := text(props["provisioningState"])
	if retained {
		if !slices.Contains([]string{"Deleted", "SoftDeleting", "Deleting"}, state) {
			return "azure_elastic_san_volume_retained_state_changed"
		}
	} else if !slices.Contains([]string{"Succeeded", "Failed", "Canceled", "Deleting"}, state) {
		return "azure_elastic_san_volume_not_ready"
	}
	return ""
}

// Complete native snapshot indexes plus known own reads prevent omissions from
// hiding a previously reviewed restore point. Snapshot deletion stays separate.
func (c *client) elasticSanVolumeSnapshots(ctx context.Context, id string, known map[string]any) (map[string]map[string]any, error) {
	group := elasticSanParent(id, elasticSanVolumeType)
	rows, _, err := c.elasticSanIndex(ctx, elasticSanSnapshotType, group, false)
	if err != nil {
		return nil, err
	}
	result, seen := map[string]map[string]any{}, map[string]bool{}
	read := func(child string, listed map[string]any) error {
		res, err := c.elasticSanRead(ctx, child, elasticSanSnapshotType)
		if isNotFound(err) && listed == nil {
			return nil
		}
		if err != nil {
			return err
		}
		if listed != nil && (serviceListedIncarnation(listed, res.data) != nil || !nativeConfigurationContains(object(listed["properties"]), object(res.data["properties"]))) {
			return serviceDenied("elastic_san_snapshot_index_changed")
		}
		source, kind, err := parseID(text(object(object(res.data["properties"])["creationData"])["sourceId"]))
		if err != nil || !strings.EqualFold(kind, elasticSanVolumeType) {
			return serviceDenied("elastic_san_snapshot_source_unverified")
		}
		if source == id {
			result[child] = res.data
		} else if known[child] != nil {
			return serviceDenied("elastic_san_snapshot_source_changed")
		}
		return nil
	}
	for _, row := range rows {
		raw := object(row)
		child, err := c.elasticSanRecord(raw, elasticSanSnapshotType)
		if err != nil {
			return nil, err
		}
		seen[child] = true
		if err := read(child, raw); err != nil {
			return nil, err
		}
	}
	for child := range known {
		canonical, err := c.elasticSanIdentity(child, elasticSanSnapshotType)
		if err != nil || canonical != child || elasticSanParent(child, elasticSanSnapshotType) != group {
			return nil, serviceDenied("elastic_san_snapshot_history_changed")
		}
		if !seen[child] {
			if err := read(child, nil); err != nil {
				return nil, err
			}
		}
	}
	return result, nil
}

func (c *client) elasticSanVolumeCleanup(raw, group, prior map[string]any, snapshots map[string]map[string]any, retained bool) (map[string]any, string, error) {
	policy, err := elasticSanVolumePolicy(group)
	if group == nil {
		policy = object(object(prior["volume"])["policy"])
		if policy == nil {
			return nil, "", serviceDenied("elastic_san_volume_parent_unverified")
		}
	}
	if err != nil {
		return nil, "", err
	}
	children := map[string]any{}
	for child, raw := range snapshots {
		children[child] = c.privateConfiguration(hybridComputeChildSnapshot(raw))
	}
	reason := c.elasticSanVolumeProtection(raw, retained)
	volume := map[string]any{"retained": retained, "policy": policy, "snapshots": children, "volumeId": object(raw["properties"])["volumeId"]}
	state := map[string]any{"resource": c.privateConfiguration(elasticSanVolumeSnapshot(raw)), "etag": c.privateConfiguration(elasticSanChildVersion(elasticSanVolumeType, raw)), "protected": reason != "", "volume": volume}
	return state, reason, nil
}

// An own 404 cannot distinguish soft deletion from permanent removal. Inspect
// both populations and the immutable volume GUID before describing the outcome.
func (a *elasticSanChildAction) volumeObservation(ctx context.Context) (map[string]any, contracts.ReadbackResult, error) {
	id := a.planned.Identity.NativeID
	state := object(a.planned.Normalized[elasticSanSnapshotCleanup])
	volume := object(state["volume"])
	guid := text(volume["volumeId"])
	retainedPlan := volume["retained"] == true
	group := elasticSanParent(id, elasticSanVolumeType)
	candidates := map[string]elasticSanObservation{}
	for _, retained := range []bool{false, true} {
		rows, _, err := a.client.elasticSanIndex(ctx, elasticSanVolumeType, group, retained)
		if err != nil {
			return nil, contracts.ReadbackResult{}, err
		}
		for _, row := range rows {
			raw := object(row)
			candidate, err := a.client.elasticSanRecord(raw, elasticSanVolumeType)
			if err != nil {
				return nil, contracts.ReadbackResult{}, err
			}
			if !uuidPattern.MatchString(text(object(raw["properties"])["volumeId"])) {
				return nil, contracts.ReadbackResult{}, serviceDenied("elastic_san_volume_index_identity_unverified")
			}
			if candidates[candidate].raw != nil {
				return nil, contracts.ReadbackResult{}, serviceDenied("elastic_san_volume_duplicate_population")
			}
			candidates[candidate] = elasticSanObservation{raw: raw, retained: retained}
		}
	}
	own, err := a.client.elasticSanRead(ctx, id, elasticSanVolumeType)
	if err != nil && !isNotFound(err) {
		return nil, contracts.ReadbackResult{}, err
	}
	if err == nil {
		existing := candidates[id]
		if existing.raw != nil && (serviceListedIncarnation(existing.raw, own.data) != nil || !nativeConfigurationContains(object(existing.raw["properties"]), object(own.data["properties"]))) {
			return nil, contracts.ReadbackResult{}, serviceDenied("elastic_san_volume_index_changed")
		}
		retained := existing.retained
		if existing.raw == nil {
			switch object(own.data["properties"])["provisioningState"] {
			case "Deleted", "SoftDeleting":
				retained = true
			default:
				retained = retainedPlan
			}
		}
		candidates[id] = elasticSanObservation{raw: own.data, retained: retained}
	} else if candidate := candidates[id]; candidate.raw != nil && !candidate.retained {
		return nil, contracts.ReadbackResult{}, serviceDenied("elastic_san_active_volume_missing")
	}
	var match elasticSanObservation
	matchID := ""
	for candidate, value := range candidates {
		if candidate == id && text(object(value.raw["properties"])["volumeId"]) != guid {
			return nil, contracts.ReadbackResult{}, serviceDenied("elastic_san_volume_recreated")
		}
		if text(object(value.raw["properties"])["volumeId"]) == guid {
			if matchID != "" {
				return nil, contracts.ReadbackResult{}, serviceDenied("elastic_san_volume_ambiguous_identity")
			}
			matchID, match = candidate, value
		}
	}
	if matchID == "" {
		return nil, contracts.ReadbackResult{Data: map[string]any{"outcome": "absent"}}, nil
	}
	if match.retained && matchID != id {
		current, readErr := a.client.elasticSanRead(ctx, matchID, elasticSanVolumeType)
		if readErr != nil && !isNotFound(readErr) {
			return nil, contracts.ReadbackResult{}, readErr
		}
		if readErr == nil {
			if serviceListedIncarnation(match.raw, current.data) != nil || !nativeConfigurationContains(object(match.raw["properties"]), object(current.data["properties"])) {
				return nil, contracts.ReadbackResult{}, serviceDenied("elastic_san_retained_volume_changed")
			}
			match.raw = current.data
		}
	}
	if a.client.privateConfiguration(elasticSanVolumeSnapshot(match.raw)) != state["resource"] {
		return nil, contracts.ReadbackResult{}, serviceDenied("elastic_san_volume_configuration_changed")
	}
	if matchID != id {
		if retainedPlan || !match.retained || object(volume["policy"])["policyState"] == "Disabled" || err == nil {
			return nil, contracts.ReadbackResult{}, serviceDenied("elastic_san_volume_identity_moved")
		}
		return nil, contracts.ReadbackResult{State: "Deleted", Data: map[string]any{"outcome": "soft_deleted", "retained_native_id": matchID, "volumeId": guid}}, nil
	}
	if match.retained != retainedPlan {
		// Some native versions retain the same ARM ID. Do not purge it implicitly.
		if !retainedPlan && match.retained && object(volume["policy"])["policyState"] != "Disabled" && err != nil {
			return nil, contracts.ReadbackResult{State: "Deleted", Data: map[string]any{"outcome": "soft_deleted", "retained_native_id": matchID, "volumeId": guid}}, nil
		}
		return nil, contracts.ReadbackResult{}, serviceDenied("elastic_san_volume_population_changed")
	}
	return match.raw, contracts.ReadbackResult{Exists: true, State: text(object(match.raw["properties"])["provisioningState"])}, nil
}

func (a *elasticSanChildAction) volumeRequest(request contracts.ActionRequest) error {
	state := object(a.planned.Normalized[elasticSanSnapshotCleanup])
	volume := object(state["volume"])
	children := object(volume["snapshots"])
	if len(request.LifecycleImpacts) != 0 || len(request.PrerequisiteDeletions) != len(children) {
		return serviceDenied("elastic_san_volume_snapshot_review_changed")
	}
	for key, value := range request.Parameters {
		if key != "force_delete" || value != true && value != false || volume["retained"] == true && value == true {
			return serviceDenied("elastic_san_volume_options_unreviewed")
		}
	}
	seen := map[string]bool{}
	for _, impact := range request.PrerequisiteDeletions {
		child := impact.Asset
		id := child.Identity.NativeID
		if !impact.Delete || impact.ControllerID != a.planned.ID || child.Identity.ConnectionID != a.planned.Identity.ConnectionID || child.Identity.NativeType != elasticSanSnapshotType || seen[id] || children[id] == nil || a.client.elasticSanChildRecord(child) != nil || object(child.Normalized[elasticSanSnapshotCleanup])["resource"] != children[id] {
			return serviceDenied("elastic_san_volume_snapshot_review_changed")
		}
		seen[id] = true
	}
	return nil
}

func (a *elasticSanChildAction) volumePreflight(ctx context.Context, raw map[string]any) error {
	state := object(a.planned.Normalized[elasticSanSnapshotCleanup])
	volume := object(state["volume"])
	if reason := a.client.elasticSanVolumeProtection(raw, volume["retained"] == true); reason != "" {
		return serviceDenied(reason)
	}
	group, err := a.client.elasticSanRead(ctx, elasticSanParent(a.planned.Identity.NativeID, elasticSanVolumeType), elasticSanGroupType)
	if err != nil {
		return err
	}
	policy, err := elasticSanVolumePolicy(group.data)
	if err != nil {
		return err
	}
	if a.client.privateConfiguration(policy) != a.client.privateConfiguration(object(volume["policy"])) {
		return serviceDenied("elastic_san_volume_retention_changed")
	}
	children, err := a.client.elasticSanVolumeSnapshots(ctx, a.planned.Identity.NativeID, object(volume["snapshots"]))
	if err != nil {
		return err
	}
	if len(children) != 0 {
		return serviceDenied("elastic_san_volume_snapshots_require_cleanup")
	}
	return nil
}

func elasticSanVolumeMode(state map[string]any) string {
	volume := object(state["volume"])
	if volume["retained"] == true {
		return "permanent"
	}
	if object(volume["policy"])["policyState"] == "Enabled" {
		return "soft_delete"
	}
	if object(volume["policy"])["policyState"] != "Disabled" {
		return "native"
	}
	return "delete"
}
