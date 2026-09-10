package azure

import (
	"context"
	"maps"
	"slices"
	"strings"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

// Supplement generic ARM group discovery with complete native Monitor indexes,
// including extension budgets listed only at the group scope. Read twice and
// reconcile resources already visited before returning omitted group members.
func (c *client) monitorManagedGroupMembers(ctx context.Context, group string, known map[string]map[string]any) (members []map[string]any, err error) {
	defer func() { err = contracts.DependencyReadError(err) }()
	id, typ, err := parseID(group)
	if err != nil || id != group || !strings.EqualFold(typ, groupType) || !strings.HasPrefix(id, c.root()+"/") {
		return nil, serviceDenied("invalid_monitor_managed_group")
	}
	metadata, err := providerData()
	if err != nil {
		return nil, err
	}
	read := func() (map[string]map[string]any, error) {
		values := map[string]map[string]any{}
		for _, resource := range metadata.kinds {
			kind := monitorResourceKind(resource.NativeType)
			if kind == "" {
				continue
			}
			index, _, err := c.monitorResourceIndex(ctx, kind, map[string]map[string]any{group: nil})
			if err != nil {
				return nil, err
			}
			for id, raw := range index {
				if inResourceGroup(id, group) {
					values[id] = raw
				}
			}
		}
		return values, nil
	}
	snapshot := func(values map[string]map[string]any) string {
		configuration := map[string]any{}
		for id, raw := range values {
			_, _, kind, _ := monitorResourceID(id)
			configuration[id] = monitorResourceSnapshot(kind, raw)
		}
		return c.privateConfiguration(configuration)
	}
	first, err := read()
	if err != nil {
		return nil, err
	}
	second, err := read()
	if err != nil {
		return nil, err
	}
	if snapshot(first) != snapshot(second) {
		return nil, serviceDenied("managed_monitor_native_membership_changed")
	}
	for _, id := range slices.Sorted(maps.Keys(second)) {
		raw := second[id]
		if previous := known[id]; previous != nil {
			_, _, kind, _ := monitorResourceID(id)
			if c.privateConfiguration(monitorResourceSnapshot(kind, previous)) != c.privateConfiguration(monitorResourceSnapshot(kind, raw)) {
				return nil, serviceDenied("managed_monitor_native_configuration_changed")
			}
			continue
		}
		members = append(members, raw)
	}
	return members, nil
}

// A managed-group member must retain the same private configuration and native
// receiver resolution as its scan. A generic ARM GET does not establish the
// product's complete read contract. A nil group is used only for residual reads
// after the caller has established native resource-group absence.
func (c *client) monitorManagedIncarnation(ctx context.Context, planned asset.Asset, observed, group map[string]any) (err error) {
	if monitorResourceKind(planned.Identity.NativeType) == "" {
		return nil
	}
	defer func() { err = contracts.DependencyReadError(err) }()
	id, scope, kind, err := monitorResourceID(planned.Identity.NativeID)
	if err != nil || id != planned.Identity.NativeID || kind != planned.Identity.NativeType || planned.Identity.Provider != asset.ProviderAzure || !strings.HasPrefix(scope, c.root()+"/resourcegroups/") {
		return serviceDenied("invalid_managed_monitor_identity")
	}
	if err := c.servicePrivateIncarnation(planned, observed); err != nil {
		return err
	}
	if group != nil && (!strings.EqualFold(text(group["id"]), scope) || text(planned.Normalized[monitorGroupProof]) != c.privateConfiguration(insightsWorkspaceResourceSnapshot(group))) {
		return serviceDenied("managed_monitor_resource_group_changed")
	}
	current, err := c.monitorResourceRead(ctx, kind, id)
	if err != nil {
		return err
	}
	if monitorResourceRegion(kind, current.data) != planned.Location {
		return serviceDenied("managed_monitor_location_changed")
	}
	if err := c.servicePrivateIncarnation(planned, current.data); err != nil {
		return err
	}
	refs, err := c.monitorReferences(ctx, kind, id, current.data)
	if err != nil {
		return err
	}
	return c.monitorReferencesUnchanged(planned, refs)
}
