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

func elasticSanRootSnapshot(raw map[string]any) map[string]any {
	out := hybridComputeChildSnapshot(raw)
	// Native readOnly aggregate fields change as reviewed children are removed.
	for _, key := range []string{"totalVolumeSizeGiB", "volumeGroupCount", "totalIops", "totalMBps", "totalSizeTiB", "privateEndpointConnections"} {
		delete(object(out["properties"]), key)
	}
	return out
}

func (c *client) elasticSanRootCleanup(value asset.Asset, raw map[string]any, nodes map[string]elasticSanObservation) (map[string]any, string) {
	boundary := object(value.Normalized[elasticSanBoundary])
	reason := protectionReason(resourceType{NativeType: elasticSanType}, raw)
	if reason == "" && (raw["managedBy"] != nil && text(raw["managedBy"]) != "" || object(raw["properties"])["managedBy"] != nil) {
		reason = "elastic_san_managed"
	}
	if reason == "" && boundary["complete"] != true {
		reason = "elastic_san_boundary_incomplete"
	}
	if _, err := time.Parse(time.RFC3339Nano, text(object(raw["systemData"])["createdAt"])); reason == "" && err != nil {
		reason = "elastic_san_creation_unverified"
	}
	if reason == "" && !slices.Contains([]string{"Succeeded", "Failed", "Canceled", "Deleting"}, text(object(raw["properties"])["provisioningState"])) {
		reason = "elastic_san_not_ready"
	}
	children := map[string]any{}
	for id, entry := range object(boundary["members"]) {
		member := object(entry)
		kind := text(member["kind"])
		child := nodes[id].raw
		snapshot := elasticSanChildSnapshot(kind, child)
		if kind == elasticSanVolumeType {
			snapshot = elasticSanVolumeSnapshot(child)
		}
		children[id] = map[string]any{"kind": kind, "configuration": c.privateConfiguration(snapshot)}
		if reason == "" && member["retained"] == true {
			reason = "elastic_san_retained_children_require_resolution"
		}
		if kind == elasticSanGroupType {
			policy, err := elasticSanVolumePolicy(child)
			if reason == "" && (err != nil || policy["policyState"] == "Enabled") {
				reason = "elastic_san_group_retention_requires_resolution"
			}
		}
	}
	return map[string]any{"resource": c.privateConfiguration(elasticSanRootSnapshot(raw)), "etag": c.privateConfiguration(elasticSanChildVersion(elasticSanType, raw)), "protected": reason != "", "boundary": value.Normalized[elasticSanBoundaryProof], "children": children}, reason
}

func (a *elasticSanChildAction) rootRequest(request contracts.ActionRequest) error {
	if len(request.Parameters)+len(request.LifecycleImpacts) != 0 {
		return serviceDenied("elastic_san_root_options_unreviewed")
	}
	children := object(object(a.planned.Normalized[elasticSanSnapshotCleanup])["children"])
	seen := map[string]bool{}
	for _, impact := range request.PrerequisiteDeletions {
		child := impact.Asset
		id := child.Identity.NativeID
		entry := object(children[id])
		if seen[id] || !impact.Delete || impact.ControllerID != a.planned.ID || child.Identity.ConnectionID != a.planned.Identity.ConnectionID || entry == nil || child.Identity.NativeType != entry["kind"] || (child.Identity.NativeType != elasticSanGroupType && child.Identity.NativeType != elasticSanEndpointType) || a.client.elasticSanChildRecord(child) != nil || object(child.Normalized[elasticSanSnapshotCleanup])["resource"] != entry["configuration"] {
			return serviceDenied("elastic_san_root_prerequisite_changed")
		}
		seen[id] = true
	}
	for id, entry := range children {
		kind := object(entry)["kind"]
		if (kind == elasticSanGroupType || kind == elasticSanEndpointType) && !seen[id] {
			return serviceDenied("elastic_san_root_prerequisite_unreviewed")
		}
	}
	return nil
}

// Check both group populations and each known child's own address. Collections
// may disappear only after their parent own read confirms absence; a surviving
// retained population or known child always prevents root completion.
func (a *elasticSanChildAction) rootMembers(ctx context.Context, terminal bool) (map[string]elasticSanObservation, error) {
	id := a.planned.Identity.NativeID
	known := object(object(a.planned.Normalized[elasticSanSnapshotCleanup])["children"])
	nodes := map[string]elasticSanObservation{}
	read := func(child, kind string, listed map[string]any, retained bool) error {
		res, err := a.client.elasticSanRead(ctx, child, kind)
		if isNotFound(err) {
			if listed == nil {
				return nil
			}
			if !retained {
				return serviceDenied("elastic_san_root_listed_child_missing")
			}
			nodes[child] = elasticSanObservation{raw: listed, retained: true}
			return nil
		}
		if err != nil {
			return err
		}
		if listed != nil && (serviceListedIncarnation(listed, res.data) != nil || !nativeConfigurationContains(object(listed["properties"]), object(res.data["properties"]))) {
			return serviceDenied("elastic_san_root_child_changed")
		}
		nodes[child] = elasticSanObservation{raw: res.data, retained: retained}
		return nil
	}
	collect := func(kind, parent string, missingParent bool) error {
		modes := []bool{false}
		if kind == elasticSanGroupType || kind == elasticSanVolumeType {
			modes = append(modes, true)
		}
		for _, retained := range modes {
			rows, _, err := a.client.elasticSanIndex(ctx, kind, parent, retained)
			if isNotFound(err) && missingParent {
				continue
			}
			if err != nil {
				return err
			}
			for _, row := range rows {
				raw := object(row)
				child, err := a.client.elasticSanRecord(raw, kind)
				if err != nil {
					return err
				}
				if nodes[child].raw != nil {
					return serviceDenied("elastic_san_root_duplicate_population")
				}
				if err := read(child, kind, raw, retained); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := collect(elasticSanGroupType, id, terminal); err != nil {
		return nil, err
	}
	groups := map[string]bool{}
	for child := range nodes {
		groups[child] = true
	}
	for child, entry := range known {
		if object(entry)["kind"] == elasticSanGroupType {
			groups[child] = true
			if nodes[child].raw == nil {
				if err := read(child, elasticSanGroupType, nil, false); err != nil {
					return nil, err
				}
			}
		}
	}
	for _, group := range slices.Sorted(maps.Keys(groups)) {
		for _, kind := range []string{elasticSanVolumeType, elasticSanSnapshotType} {
			if err := collect(kind, group, nodes[group].raw == nil); err != nil {
				return nil, err
			}
		}
	}
	if err := collect(elasticSanEndpointType, id, terminal); err != nil {
		return nil, err
	}
	for child, entry := range known {
		if nodes[child].raw == nil {
			if err := read(child, text(object(entry)["kind"]), nil, false); err != nil {
				return nil, err
			}
		}
	}
	return nodes, nil
}

func (a *elasticSanChildAction) rootPreflight(ctx context.Context, raw map[string]any) error {
	if reason := protectionReason(resourceType{NativeType: elasticSanType}, raw); reason != "" {
		return serviceDenied(reason)
	}
	if raw["managedBy"] != nil && text(raw["managedBy"]) != "" || object(raw["properties"])["managedBy"] != nil {
		return serviceDenied("elastic_san_managed")
	}
	if !strings.EqualFold(resourceRegion(raw), a.planned.Location) {
		return serviceDenied("elastic_san_region_changed")
	}
	return a.rootChildrenFinished(ctx, false)
}

func (a *elasticSanChildAction) rootChildrenFinished(ctx context.Context, terminal bool) error {
	nodes, err := a.rootMembers(ctx, terminal)
	if err != nil {
		return err
	}
	if len(nodes) != 0 {
		return serviceDenied("elastic_san_children_require_cleanup")
	}
	return nil
}
