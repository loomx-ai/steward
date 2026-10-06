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
	contexts := func(wire string) (diagnosticContextState, error) {
		if state, ok := cache[wire]; ok {
			return state, nil
		}
		state, err := c.diagnosticContext(ctx, wire)
		if err == nil {
			cache[wire] = state
		}
		return state, err
	}
	index := c.rbacTargetIndex(targets)
	for _, kind := range kinds {
		// Concurrent delete checks coalesce these lists; see liveShared.
		rows, err := liveShared(ctx, c, "rbac-index:"+kind, func() (map[string]map[string]any, error) {
			rows, _, err := c.rbacList(ctx, kind, c.root())
			if err != nil || kind != rbacRoleType {
				return rows, err
			}
			// A custom role's assignableScopes are mutable: a lagging list
			// must not hide a scope just added, so every custom role is read
			// and must agree. Built-in roles are Microsoft-managed.
			var custom []string
			for _, id := range slices.Sorted(maps.Keys(rows)) {
				if rbacCustomRole(rows[id]) {
					custom = append(custom, id)
				}
			}
			details, errs := readConcurrently(len(custom), func(i int) (map[string]any, error) { return c.rbacDetail(ctx, kind, rows[custom[i]]) })
			for i, id := range custom {
				if errs[i] != nil {
					return nil, errs[i]
				}
				rows[id] = details[i]
			}
			return rows, nil
		})
		if err != nil {
			return nil, err
		}
		rows = maps.Clone(rows) // Shared: add known sources to a copy.
		read, recorded := map[string]bool{}, map[string]bool{}
		for id, raw := range rows {
			read[id] = kind == rbacRoleType && rbacCustomRole(raw) // Read above.
		}
		for _, value := range known {
			if value.Identity.Provider != asset.ProviderAzure || value.Identity.NativeType != kind || !strings.HasPrefix(value.Identity.NativeID, c.root()+"/") {
				continue
			}
			refs, err := c.rbacRecordedReferences(value)
			if err != nil {
				return nil, err
			}
			values := map[string][]string{}
			for k, v := range refs {
				values[k] = stringValues(v)
			}
			linked, err := index.linked(values)
			if err != nil {
				return nil, err
			}
			recorded[value.Identity.NativeID] = recorded[value.Identity.NativeID] || len(linked) > 0
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
			// Targets are checked in order before any read; targets at or after
			// the first invalid proof are not read, and its error follows
			// earlier read results.
			var invalid error
			var reads []asset.Asset
			for _, target := range targets {
				if !rbacIdentityTarget(target) {
					continue
				}
				if target.Normalized[rbacIdentityMetadata] != nil || target.Normalized[rbacIdentityProof] != nil {
					if _, err := c.rbacRecordedIdentity(target); err != nil {
						invalid = err
						break
					}
				}
				if principals || text(object(target.Normalized[rbacIdentityMetadata])["principal"]) != "" {
					reads = append(reads, target)
				}
			}
			_, errs := readConcurrently(len(reads), func(i int) (struct{}, error) { return struct{}{}, c.rbacIdentityRead(ctx, reads[i]) })
			for _, err := range errs {
				if err != nil {
					return nil, err
				}
			}
			if invalid != nil {
				return nil, invalid
			}
		}
		// Match every row's list fields in order, up to the first that fails;
		// read the rows that need detail concurrently; then process in order,
		// so results and the first error are the serial walk's.
		ids := slices.Sorted(maps.Keys(rows))
		type listedRow struct {
			refs   map[string][]string
			linked []int
		}
		var listed []listedRow
		var listErr error
		var detail []string
		for _, id := range ids {
			refs, err := c.rbacReferences(kind, id, rows[id])
			var linked []int
			if err == nil {
				linked, err = index.linked(refs)
			}
			if err != nil {
				listErr = err
				break
			}
			listed = append(listed, listedRow{refs, linked})
			if !read[id] && (recorded[id] || len(linked) > 0) {
				detail = append(detail, id)
			}
		}
		details, detailErrs := readConcurrently(len(detail), func(i int) (map[string]any, error) { return c.rbacDetail(ctx, kind, rows[detail[i]]) })
		next := 0
		for n, row := range listed {
			id, raw, refs, linked := ids[n], rows[ids[n]], row.refs, row.linked
			var err error
			if !read[id] {
				if !recorded[id] && len(linked) == 0 {
					continue
				}
				if raw, err = details[next], detailErrs[next]; err != nil {
					return nil, err
				}
				next++
				if refs, err = c.rbacReferences(kind, id, raw); err != nil {
					return nil, err
				}
				if linked, err = index.linked(refs); err != nil {
					return nil, err
				}
			}
			var state map[string]any
			for _, i := range linked {
				target := targets[i]
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
					state, _, err = c.rbacContext(ctx, kind, raw, locks, pim, contexts)
					if err != nil {
						return nil, err
					}
				}
				incoming[target.Identity.NativeID] = append(incoming[target.Identity.NativeID], monitorIncomingSource{resource: serviceChild{id: id, kind: kind, data: raw}, references: refs, group: state})
			}
		}
		if listErr != nil {
			return nil, listErr
		}
	}
	return incoming, nil
}

func rbacCustomRole(raw map[string]any) bool {
	return text(object(raw["properties"])["type"]) != "BuiltInRole"
}

// rbacTargets answers which of one observation's targets a row's references
// link, proving each target's identity once rather than once per row.
type rbacTargets struct {
	count             int
	byID, byPrincipal map[string][]int
	badAt             int   // the first identity target without a valid proof
	bad               error // its proof error; nil if every proof is valid
}

func (c *client) rbacTargetIndex(targets []asset.Asset) *rbacTargets {
	index := &rbacTargets{count: len(targets), byID: map[string][]int{}, byPrincipal: map[string][]int{}}
	for i, target := range targets {
		key := target.Identity.NativeType + "\x00" + target.Identity.NativeID
		index.byID[key] = append(index.byID[key], i)
		if !rbacIdentityTarget(target) {
			continue
		}
		metadata, err := c.rbacRecordedIdentity(target)
		if err != nil {
			if index.bad == nil {
				index.badAt, index.bad = i, err
			}
			continue
		}
		principal := text(metadata["principal"])
		index.byPrincipal[principal] = append(index.byPrincipal[principal], i)
	}
	return index
}

// linked returns, in target order, the targets refs name directly or by
// principal. Its error is the one the per-target walk "for each target, for
// each principal reference, rbacPrincipalMatches" stops at first.
func (x *rbacTargets) linked(refs map[string][]string) ([]int, error) {
	if principals := refs[rbacPrincipalType]; len(principals) > 0 && x.count > 0 {
		invalid := slices.IndexFunc(principals, func(reference string) bool { return !validRBACPrincipalSelector(rbacPrincipalType, reference) })
		if x.bad != nil && x.badAt == 0 && invalid != 0 {
			return nil, x.bad
		}
		if invalid >= 0 {
			return nil, serviceDenied("invalid_rbac_principal_reference")
		}
		if x.bad != nil {
			return nil, x.bad
		}
	}
	seen := map[int]bool{}
	for kind, ids := range refs {
		for _, id := range ids {
			for _, i := range x.byID[kind+"\x00"+id] {
				seen[i] = true
			}
		}
	}
	for _, reference := range refs[rbacPrincipalType] {
		for _, i := range x.byPrincipal[reference] {
			seen[i] = true
		}
	}
	return slices.Sorted(maps.Keys(seen)), nil
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
