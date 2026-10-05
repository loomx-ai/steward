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
// Listed rows come without their GETs (see rbacList): an assignment whose
// validated list fields reference no target cannot block one. One that does,
// one whose persisted references did, and every custom role definition is
// read and must agree before it is used.
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
		// Concurrent delete checks coalesce these lists; see liveShared.
		rows, err := liveShared(ctx, c, "rbac-index:"+kind, func() (map[string]map[string]any, error) {
			rows, _, err := c.rbacList(ctx, kind, c.root())
			return rows, err
		})
		if err != nil {
			return nil, err
		}
		rows = maps.Clone(rows) // Shared: add known sources to a copy.
		read, recorded := map[string]bool{}, map[string]bool{}
		for _, value := range known {
			if value.Identity.Provider != asset.ProviderAzure || value.Identity.NativeType != kind || !strings.HasPrefix(value.Identity.NativeID, c.root()+"/") {
				continue
			}
			refs, err := c.rbacRecordedReferences(value)
			if err != nil {
				return nil, err
			}
			for _, target := range targets {
				recorded[value.Identity.NativeID] = recorded[value.Identity.NativeID] || slices.Contains(stringValues(refs[target.Identity.NativeType]), target.Identity.NativeID)
				for _, reference := range stringValues(refs[rbacPrincipalType]) {
					matches, err := c.rbacPrincipalMatches(target, reference)
					if err != nil {
						return nil, err
					}
					recorded[value.Identity.NativeID] = recorded[value.Identity.NativeID] || matches
				}
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
			rows[value.Identity.NativeID], read[value.Identity.NativeID] = current.data, true
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
		linked := func(target asset.Asset, refs map[string][]string) (bool, error) {
			linked := slices.Contains(refs[target.Identity.NativeType], target.Identity.NativeID)
			for _, reference := range refs[rbacPrincipalType] {
				matches, err := c.rbacPrincipalMatches(target, reference)
				if err != nil {
					return false, err
				}
				linked = linked || matches
			}
			return linked, nil
		}
		for _, id := range slices.Sorted(maps.Keys(rows)) {
			raw := rows[id]
			refs, err := c.rbacReferences(kind, id, raw)
			if err != nil {
				return nil, err
			}
			if !read[id] {
				// A custom role's assignableScopes are mutable: a lagging list
				// must not hide a scope just added, so every custom role is
				// read and must agree. Built-in roles are Microsoft-managed.
				relevant := recorded[id] || kind == rbacRoleType && text(object(raw["properties"])["type"]) != "BuiltInRole"
				for _, target := range targets {
					matches, err := linked(target, refs)
					if err != nil {
						return nil, err
					}
					relevant = relevant || matches
				}
				if !relevant {
					continue
				}
				if raw, err = c.rbacDetail(ctx, kind, raw); err != nil {
					return nil, err
				}
				if refs, err = c.rbacReferences(kind, id, raw); err != nil {
					return nil, err
				}
			}
			var state map[string]any
			for _, target := range targets {
				matches, err := linked(target, refs)
				if err != nil {
					return nil, err
				}
				if !matches {
					continue
				}
				if state == nil {
					if pim == nil {
						locks, err = c.managementLocks(ctx)
						if err == nil {
							pim, err = liveShared(ctx, c, "rbac-pim", func() (map[string]map[string]any, error) { return c.rbacPIM(ctx, false) })
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
