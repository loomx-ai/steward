package azure

import (
	"context"
	"maps"
	"slices"
	"strings"

	"github.com/loomx-ai/steward/internal/core/asset"
)

// Subscription indexes include unindexed scope extensions. Persisted sources
// are also read directly, so a list omission cannot erase a reviewed reference.
func (c *client) rbacIncomingObservation(ctx context.Context, targets, known []asset.Asset) (map[string][]monitorIncomingSource, error) {
	incoming := map[string][]monitorIncomingSource{}
	if len(targets) == 0 {
		return incoming, nil
	}
	kinds := []string{rbacAssignmentType}
	for _, target := range targets {
		if target.Identity.NativeType != rbacRoleType {
			kinds = append(kinds, rbacRoleType)
			break
		}
	}
	var locks []any
	var pim map[string]map[string]any
	cache := map[string]diagnosticContextState{}
	for _, kind := range kinds {
		rows, _, err := c.rbacIndex(ctx, kind, c.root())
		if err != nil {
			return nil, err
		}
		for _, value := range known {
			if value.Identity.Provider != asset.ProviderAzure || value.Identity.NativeType != kind || !strings.HasPrefix(value.Identity.NativeID, c.root()+"/") {
				continue
			}
			if _, err := c.rbacRecordedReferences(value); err != nil {
				return nil, err
			}
			if rows[value.Identity.NativeID] != nil {
				continue
			}
			current, err := c.rbacRead(ctx, kind, text(value.Normalized[rbacWireSelector]))
			if isNotFound(err) {
				continue
			}
			if err != nil {
				return nil, err
			}
			rows[value.Identity.NativeID] = current.data
		}
		if kind == rbacAssignmentType {
			principals := false
			for _, raw := range rows {
				principals = principals || object(raw["properties"])["principalType"] == "ServicePrincipal"
			}
			for _, target := range targets {
				if !rbacIdentityTarget(target) {
					continue
				}
				if target.Normalized[rbacIdentityMetadata] != nil || target.Normalized[rbacIdentityProof] != nil {
					if _, err := c.rbacRecordedIdentity(target); err != nil {
						return nil, err
					}
				}
				if principals || text(object(target.Normalized[rbacIdentityMetadata])["principal"]) != "" {
					if err := c.rbacIdentityRead(ctx, target); err != nil {
						return nil, err
					}
				}
			}
		}
		for _, id := range slices.Sorted(maps.Keys(rows)) {
			raw := rows[id]
			refs, err := c.rbacReferences(kind, id, raw)
			if err != nil {
				return nil, err
			}
			var state map[string]any
			for _, target := range targets {
				linked := slices.Contains(refs[target.Identity.NativeType], target.Identity.NativeID)
				for _, reference := range refs[rbacPrincipalType] {
					matches, err := c.rbacPrincipalMatches(target, reference)
					if err != nil {
						return nil, err
					}
					linked = linked || matches
				}
				if !linked {
					continue
				}
				if state == nil {
					if pim == nil {
						locks, err = c.managementLocks(ctx)
						if err == nil {
							pim, err = c.rbacPIM(ctx)
						}
						if err != nil {
							return nil, err
						}
					}
					state, _, err = c.rbacContext(ctx, kind, raw, locks, pim, cache)
					if err != nil {
						return nil, err
					}
				}
				incoming[target.Identity.NativeID] = append(incoming[target.Identity.NativeID], monitorIncomingSource{resource: serviceChild{id: id, kind: kind, data: raw}, references: refs, group: state})
			}
		}
	}
	return incoming, nil
}

func (c *client) rbacIncomingUnchanged(value asset.Asset, entry monitorIncomingSource) error {
	if _, err := c.rbacRecordedReferences(value); err != nil {
		return err
	}
	configuration := c.privateConfiguration(c.rbacSnapshot(entry.resource.kind, entry.resource.data))
	context := c.privateConfiguration(entry.group)
	wire, err := c.rbacWireID(text(entry.resource.data["id"]))
	if err != nil || value.Location != "global" || text(value.Normalized[rbacWireSelector]) != wire || text(value.Normalized[rbacConfigurationProof]) != configuration || text(value.Normalized[rbacContextProof]) != context || text(value.Normalized[rbacReferencesProof]) != c.rbacReferenceBinding(value.Identity.NativeID, value.Identity.NativeType, wire, configuration, context, monitorReferenceProjection(entry.references)) {
		return serviceDenied("rbac_incoming_configuration_changed")
	}
	return nil
}
