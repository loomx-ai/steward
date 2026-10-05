package azure

import (
	"context"
	"maps"
	"slices"
	"strings"

	"github.com/loomx-ai/steward/internal/core/asset"
)

// The resource index supplies new sources; persisted IDs additionally recover
// settings whose source has disappeared. Neither index can replace the setting's
// own GET. Caller-owned target scopes also expose unindexed attached settings.
func (c *client) diagnosticIncomingObservation(ctx context.Context, targets, known []asset.Asset) (map[string][]monitorIncomingSource, error) {
	incoming := map[string][]monitorIncomingSource{}
	if len(targets) == 0 {
		return incoming, nil
	}
	hints, scopes := map[string]string{}, map[string]bool{}
	for _, value := range append(slices.Clone(known), targets...) {
		if value.Identity.Provider != asset.ProviderAzure || !strings.HasPrefix(value.Identity.NativeID, c.root()+"/") {
			continue
		}
		if value.Identity.NativeType == diagnosticSettingsType {
			wire, err := c.diagnosticPlannedWire(value.Identity.NativeID, value.Normalized)
			if err != nil {
				return nil, err
			}
			hints[value.Identity.NativeID] = wire
		}
	}
	for _, target := range targets {
		if id, err := diagnosticScope(target.Identity.NativeID); err == nil {
			if isCosmosType(target.Identity.NativeType) {
				wire, err := c.plannedResourceID(target)
				if err != nil {
					return nil, err
				}
				id, err = diagnosticSourceWire(wire)
				if err != nil {
					return nil, err
				}
			}
			scopes[id] = true
		}
	}
	settings, _, _, err := c.diagnosticCollection(ctx, slices.Sorted(maps.Values(hints)), slices.Sorted(maps.Keys(scopes)))
	if err != nil {
		return nil, err
	}
	byID := map[string][]int{} // Target positions by native ID; kinds compare below.
	for i, target := range targets {
		byID[target.Identity.NativeID] = append(byID[target.Identity.NativeID], i)
	}
	// Linked settings' contexts are read up front, concurrently and once per
	// scope; the loop below still takes results and the first error in order.
	ids, linkedScopes := slices.Sorted(maps.Keys(settings)), []string{}
	for _, id := range ids {
		refs, err := diagnosticReferences(id, settings[id])
		if err != nil {
			break // Reported in order below.
		}
		linked := false
		for kind, references := range refs {
			for _, reference := range references {
				for _, i := range byID[reference] {
					linked = linked || strings.EqualFold(kind, targets[i].Identity.NativeType)
				}
			}
		}
		if wire, err := diagnosticWireID(text(settings[id]["id"])); linked && err == nil {
			linkedScopes = append(linkedScopes, diagnosticWireScope(wire))
		}
	}
	contexts := c.diagnosticContexts(ctx, linkedScopes)
	for _, id := range ids {
		raw := settings[id]
		refs, err := diagnosticReferences(id, raw)
		if err != nil {
			return nil, err
		}
		linked := map[int]bool{}
		for kind, ids := range refs {
			for _, reference := range ids {
				for _, i := range byID[reference] {
					linked[i] = linked[i] || strings.EqualFold(kind, targets[i].Identity.NativeType)
				}
			}
		}
		var state *diagnosticContextState
		for i, target := range targets {
			if !linked[i] {
				continue
			}
			if state == nil {
				wire, err := diagnosticWireID(text(raw["id"]))
				if err != nil {
					return nil, err
				}
				current, err := contexts(diagnosticWireScope(wire))
				if err != nil {
					return nil, err
				}
				state = &current
			}
			incoming[target.Identity.NativeID] = append(incoming[target.Identity.NativeID], monitorIncomingSource{resource: serviceChild{id: id, kind: diagnosticSettingsType, data: raw}, references: refs, group: state.state})
		}
	}
	return incoming, nil
}

func (c *client) diagnosticIncomingUnchanged(value asset.Asset, entry monitorIncomingSource) error {
	if _, err := c.diagnosticRecordedReferences(value); err != nil {
		return err
	}
	configuration := c.privateConfiguration(diagnosticSnapshot(entry.resource.data))
	context := c.privateConfiguration(entry.group)
	if value.Location != "global" || text(value.Normalized[diagnosticConfigurationProof]) != configuration || text(value.Normalized[diagnosticContextProof]) != context || text(value.Normalized[diagnosticReferencesProof]) != c.diagnosticReferenceBinding(value.Identity.NativeID, configuration, context, monitorReferenceProjection(entry.references)) {
		return serviceDenied("diagnostic_incoming_configuration_changed")
	}
	return nil
}
