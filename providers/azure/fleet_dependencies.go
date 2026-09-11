package azure

import (
	"context"
	"maps"
	"slices"
	"strings"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func fleetIncomingKinds(kind string) []string {
	if fleetClusterKind(kind) != "" {
		return []string{fleetMemberType}
	}
	if registered, ok := findType(kind); ok {
		kind = registered.NativeType
	}
	switch kind {
	case fleetType:
		return append(slices.Clone(fleetDirectKinds), fleetGateType)
	case fleetRunType:
		return []string{fleetGateType}
	case fleetStrategyType:
		return []string{fleetProfileType}
	case fleetMemberType:
		return []string{fleetNamespaceType}
	case subnetType, "Microsoft.ManagedIdentity/userAssignedIdentities":
		return []string{fleetType}
	}
	return nil
}

func fleetReferenceMatches(target, source asset.Asset, refs map[string]any) bool {
	for kind, ids := range refs {
		if strings.EqualFold(kind, target.Identity.NativeType) && slices.Contains(stringValues(ids), target.Identity.NativeID) {
			return true
		}
	}
	return strings.EqualFold(target.Identity.NativeType, fleetMemberType) && source.Identity.NativeType == fleetNamespaceType && source.Normalized["placement_dynamic"] == true && fleetParent(target.Identity.NativeID, fleetMemberType) == fleetParent(source.Identity.NativeID, fleetNamespaceType)
}

// The native Fleet index is independent of the asset database. Reconcile
// known sources AND their parents so LIST omission or parent disappearance
// cannot authorize deleting an AKS/Arc cluster, subnet or identity still in use.
func (c *client) fleetIncomingObservation(ctx context.Context, targets, known []asset.Asset) (incoming map[string][]monitorIncomingSource, err error) {
	defer func() { err = contracts.DependencyReadError(err) }()
	incoming = map[string][]monitorIncomingSource{}
	kinds := map[string]bool{}
	var selected []asset.Asset
	for _, target := range targets {
		needed := fleetIncomingKinds(target.Identity.NativeType)
		if len(needed) == 0 {
			continue
		}
		id, kind, err := parseID(target.Identity.NativeID)
		if err != nil || id != target.Identity.NativeID || !strings.EqualFold(kind, target.Identity.NativeType) || target.Identity.Provider != asset.ProviderAzure || !strings.HasPrefix(id, c.root()+"/") || target.Identity.ConnectionID == "" || target.Identity.Partition == "" {
			return nil, serviceDenied("invalid_fleet_dependency_target")
		}
		if _, exists := incoming[id]; exists {
			return nil, serviceDenied("ambiguous_fleet_dependency_target")
		}
		if len(selected) != 0 && (target.Identity.ConnectionID != selected[0].Identity.ConnectionID || target.Identity.Partition != selected[0].Identity.Partition) {
			return nil, serviceDenied("fleet_dependency_connection_changed")
		}
		incoming[id] = nil
		selected = append(selected, target)
		for _, kind := range needed {
			kinds[kind] = true
		}
	}
	if len(selected) == 0 {
		return incoming, nil
	}
	hints := map[string]asset.Asset{}
	for _, value := range append(slices.Clone(known), selected...) {
		if value.Identity.Provider != asset.ProviderAzure || fleetKind(value.Identity.NativeType).kind == "" || value.Identity.ConnectionID != selected[0].Identity.ConnectionID || value.Identity.Partition != selected[0].Identity.Partition || !strings.HasPrefix(value.Identity.NativeID, c.root()+"/") {
			continue
		}
		id, kind, err := fleetIdentity(value.Identity.NativeID)
		if err != nil || id != value.Identity.NativeID || kind != value.Identity.NativeType {
			return nil, serviceDenied("invalid_fleet_dependency_source")
		}
		if previous, ok := hints[id]; ok && (previous.ID != value.ID || previous.Identity != value.Identity) {
			return nil, serviceDenied("ambiguous_fleet_dependency_source")
		}
		hints[id] = value
	}
	known = slices.Collect(maps.Values(hints))
	parents, err := c.fleetKnownIndex(ctx, fleetType, c.root(), known)
	if err != nil {
		return nil, err
	}
	missing := map[string]bool{}
	for _, id := range slices.Sorted(maps.Keys(hints)) {
		value := hints[id]
		parent := fleetParent(id, value.Identity.NativeType)
		if parent == "" || parents[parent] != nil || missing[parent] {
			continue
		}
		live, err := c.fleetRead(ctx, fleetType, parent)
		if isNotFound(err) {
			missing[parent] = true
			continue
		}
		if err != nil {
			return nil, err
		}
		parents[parent] = live.data
	}
	for _, id := range slices.Sorted(maps.Keys(hints)) {
		value := hints[id]
		if !kinds[value.Identity.NativeType] || !missing[fleetParent(id, value.Identity.NativeType)] {
			continue
		}
		if _, err := c.fleetRead(ctx, value.Identity.NativeType, id); !isNotFound(err) {
			if err != nil {
				return nil, err
			}
			return nil, serviceDenied("fleet_live_source_has_missing_parent")
		}
	}
	groups := map[string]map[string]any{}
	for _, parent := range slices.Sorted(maps.Keys(parents)) {
		for _, kind := range slices.Sorted(maps.Keys(kinds)) {
			values := map[string]map[string]any{parent: parents[parent]}
			if kind != fleetType {
				values, err = c.fleetKnownIndex(ctx, kind, parent, known)
				if err != nil {
					return nil, err
				}
			}
			for _, id := range slices.Sorted(maps.Keys(values)) {
				raw := values[id]
				refs, err := fleetCurrentReferences(kind, raw)
				if err != nil {
					return nil, err
				}
				source := asset.Asset{Identity: asset.Identity{NativeType: kind, NativeID: id}, Normalized: map[string]any{}}
				if kind == fleetNamespaceType {
					_, dynamic, err := fleetNamespaceMembers(raw)
					if err != nil {
						return nil, err
					}
					source.Normalized["placement_dynamic"] = dynamic
				}
				for _, target := range selected {
					if !fleetReferenceMatches(target, source, monitorReferenceProjection(refs)) {
						continue
					}
					groupID := strings.Join(strings.Split(id, "/")[:5], "/")
					if groups[groupID] == nil {
						group, err := c.workbookGroup(ctx, id)
						if err != nil {
							return nil, err
						}
						groups[groupID] = group
					}
					context := map[string]any{"group": insightsWorkspaceResourceSnapshot(groups[groupID])}
					if kind != fleetType {
						context["parent"] = fleetSnapshot(fleetType, parents[parent])
					}
					incoming[target.Identity.NativeID] = append(incoming[target.Identity.NativeID], monitorIncomingSource{resource: serviceChild{id: id, kind: kind, data: raw}, references: refs, group: context})
				}
			}
		}
		// Children may change the hub context while their collections are read.
		current, err := c.fleetRead(ctx, fleetType, parent)
		if err != nil {
			return nil, err
		}
		if c.privateConfiguration(fleetSnapshot(fleetType, parents[parent])) != c.privateConfiguration(fleetSnapshot(fleetType, current.data)) {
			return nil, serviceDenied("fleet_dependency_parent_changed")
		}
	}
	return incoming, nil
}

func (c *client) fleetIncomingUnchanged(value asset.Asset, entry monitorIncomingSource) error {
	if err := c.fleetIncarnation(value, entry.resource.data); err != nil {
		return err
	}
	location := resourceRegion(entry.resource.data)
	if value.Identity.NativeType != fleetType && value.Identity.NativeType != fleetNamespaceType {
		location = resourceRegion(object(entry.group["parent"]))
	}
	if value.Location != location || text(value.Normalized[fleetContextProof]) != c.privateConfiguration(entry.group) {
		return serviceDenied("fleet_dependency_context_changed")
	}
	return nil
}
