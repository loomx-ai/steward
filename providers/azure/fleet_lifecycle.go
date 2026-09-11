package azure

import (
	"context"
	"maps"
	"slices"
	"strings"

	"github.com/loomx-ai/steward/internal/app/governance"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

// Members unregister existing clusters; they never own those clusters. The
// independent ARM configurations are deleted before Fleet itself. Gates have
// no DELETE: the run referenced by target.id deletes them, irrespective of
// their sibling position in the ARM path.
// https://learn.microsoft.com/azure/kubernetes-fleet/faq
var fleetDirectKinds = []string{fleetMemberType, fleetNamespaceType, fleetRunType, fleetStrategyType, fleetProfileType, fleetMeshType}

func (c *client) fleetRecordedReferences(value asset.Asset) (map[string]any, error) {
	id, kind, err := fleetIdentity(value.Identity.NativeID)
	refs, ok := value.Normalized["_fleet_references"].(map[string]any)
	if err != nil || id != value.Identity.NativeID || kind != value.Identity.NativeType || value.Identity.Provider != asset.ProviderAzure || !strings.HasPrefix(id, c.root()+"/") || !ok || text(value.Normalized[fleetConfigurationProof]) == "" || text(value.Normalized[fleetContextProof]) == "" {
		return nil, serviceDenied("invalid_fleet_reference_proof")
	}
	if kind == fleetNamespaceType {
		if _, ok := value.Normalized["placement_dynamic"].(bool); !ok {
			return nil, serviceDenied("invalid_fleet_placement_proof")
		}
	} else if value.Normalized["placement_dynamic"] != nil {
		return nil, serviceDenied("invalid_fleet_placement_proof")
	}
	for typ, values := range refs {
		var ids []string
		switch values := values.(type) {
		case []string:
			ids = values
		case []any:
			for _, value := range values {
				id, ok := value.(string)
				if !ok {
					return nil, serviceDenied("invalid_fleet_recorded_reference")
				}
				ids = append(ids, id)
			}
		default:
			return nil, serviceDenied("invalid_fleet_recorded_reference")
		}
		seen := map[string]bool{}
		for _, id := range ids {
			canonical, actual, err := parseID(id)
			if err != nil || canonical != id || !strings.EqualFold(actual, typ) || seen[id] {
				return nil, serviceDenied("invalid_fleet_recorded_reference_identity")
			}
			seen[id] = true
		}
	}
	expected := c.fleetReferenceBinding(id, kind, value.Location, text(value.Normalized[fleetConfigurationProof]), text(value.Normalized[fleetContextProof]), refs, value.Normalized["placement_dynamic"])
	if expected != text(value.Normalized[fleetReferencesProof]) {
		return nil, serviceDenied("fleet_recorded_references_changed")
	}
	if kind == fleetMeshType {
		if _, err := c.fleetRecordedMesh(id, value.Normalized); err != nil {
			return nil, err
		}
	}
	return refs, nil
}

func (c *client) fleetIncarnation(value asset.Asset, raw map[string]any) error {
	kind := value.Identity.NativeType
	if fleetValidate(kind, raw) != nil || !strings.EqualFold(text(raw["id"]), value.Identity.NativeID) || text(value.Normalized[fleetConfigurationProof]) != c.privateConfiguration(fleetSnapshot(kind, raw)) {
		return serviceDenied("fleet_private_configuration_changed")
	}
	recorded, err := c.fleetRecordedReferences(value)
	if err != nil {
		return err
	}
	refs, err := fleetCurrentReferences(kind, raw)
	if err != nil {
		return err
	}
	if kind == fleetMeshType {
		recorded = maps.Clone(recorded)
		delete(recorded, fleetMemberType) // Native member joins are verified separately.
	}
	if c.privateConfiguration(recorded) != c.privateConfiguration(monitorReferenceProjection(refs)) {
		return serviceDenied("fleet_references_changed")
	}
	if kind == fleetNamespaceType {
		_, dynamic, err := fleetNamespaceMembers(raw)
		if err != nil || value.Normalized["placement_dynamic"] != dynamic {
			return serviceDenied("fleet_placement_changed")
		}
	}
	return nil
}

// A proxy child's region comes from its Fleet, whereas managed namespaces
// have their own region. The group and Fleet snapshots bind protection and
// ownership context without storing annotations or authored policy contents.
func (c *client) fleetContext(ctx context.Context, value asset.Asset, raw map[string]any) error {
	_, err := c.fleetVerifiedContext(ctx, value, raw)
	return err
}

func (c *client) fleetVerifiedContext(ctx context.Context, value asset.Asset, raw map[string]any) (map[string]any, error) {
	group, err := c.workbookGroup(ctx, value.Identity.NativeID)
	if err != nil {
		return nil, contracts.DependencyReadError(err)
	}
	context := map[string]any{"group": insightsWorkspaceResourceSnapshot(group)}
	location := resourceRegion(raw)
	if parent := fleetParent(value.Identity.NativeID, value.Identity.NativeType); parent != "" {
		live, err := c.fleetRead(ctx, fleetType, parent)
		if err != nil {
			return nil, contracts.DependencyReadError(err)
		}
		context["parent"] = fleetSnapshot(fleetType, live.data)
		if value.Identity.NativeType != fleetNamespaceType {
			location = resourceRegion(live.data)
		}
	}
	if value.Location != location || text(value.Normalized[fleetContextProof]) != c.privateConfiguration(context) {
		return nil, serviceDenied("fleet_context_changed")
	}
	return context, nil
}

// LIST omission is not absence. Reconcile reviewed native IDs with their own
// GETs before deciding the current child set. A child collection 404 stays an
// error; only a named child's own 404 can remove that child from this set.
func (c *client) fleetKnownIndex(ctx context.Context, kind, scope string, known []asset.Asset) (map[string]map[string]any, error) {
	values, _, err := c.fleetIndex(ctx, kind, scope)
	if err != nil {
		return nil, err
	}
	return c.fleetRecoverKnown(ctx, kind, scope, values, known)
}

func (c *client) fleetRecoverKnown(ctx context.Context, kind, scope string, values map[string]map[string]any, known []asset.Asset) (map[string]map[string]any, error) {
	seen := map[string]bool{}
	for _, value := range known {
		if value.Identity.Provider != asset.ProviderAzure || value.Identity.NativeType != kind || !strings.HasPrefix(value.Identity.NativeID, c.root()+"/") || kind != fleetType && fleetParent(value.Identity.NativeID, kind) != scope {
			continue
		}
		id, typ, err := fleetIdentity(value.Identity.NativeID)
		if err != nil || id != value.Identity.NativeID || typ != kind || seen[id] {
			return nil, serviceDenied("invalid_fleet_known_child")
		}
		seen[id] = true
		if values[id] != nil {
			continue
		}
		live, err := c.fleetRead(ctx, kind, id)
		if isNotFound(err) {
			continue
		}
		if err != nil {
			return nil, contracts.DependencyReadError(err)
		}
		values[id] = live.data
	}
	return values, nil
}

func (c *client) fleetChildren(ctx context.Context, parent asset.Identity, raw map[string]any, known ...asset.Asset) ([]serviceChild, error) {
	if parent.NativeType != fleetType && parent.NativeType != fleetRunType {
		return nil, nil
	}
	if fleetValidate(parent.NativeType, raw) != nil || !strings.EqualFold(text(raw["id"]), parent.NativeID) {
		return nil, serviceDenied("invalid_fleet_lifecycle_parent")
	}
	scope, kinds := parent.NativeID, append(slices.Clone(fleetDirectKinds), fleetGateType)
	if parent.NativeType == fleetRunType {
		scope, kinds = fleetParent(parent.NativeID, parent.NativeType), []string{fleetGateType}
	}
	observe := func() ([]serviceChild, string, error) {
		indexed := map[string]map[string]map[string]any{}
		for _, kind := range kinds {
			var values map[string]map[string]any
			var err error
			if kind == fleetMeshType {
				values, _, err = c.fleetMeshes(ctx, scope, known)
			} else {
				values, err = c.fleetKnownIndex(ctx, kind, scope, known)
			}
			if err != nil {
				return nil, "", err
			}
			indexed[kind] = values
		}
		for _, gate := range indexed[fleetGateType] {
			target, _, _ := fleetIdentity(text(object(object(gate["properties"])["target"])["id"]))
			if parent.NativeType == fleetType && indexed[fleetRunType][target] == nil {
				// A gate can reveal a run omitted by LIST, including one never
				// scanned. An orphan gate must not escape the parent review.
				run, err := c.fleetRead(ctx, fleetRunType, target)
				if err != nil {
					return nil, "", contracts.DependencyReadError(err)
				}
				indexed[fleetRunType][target] = run.data
			}
		}
		var children []serviceChild
		snapshot := map[string]any{}
		for kind, values := range indexed {
			for id, raw := range values {
				snapshot[id] = fleetSnapshot(kind, raw)
				if kind == fleetGateType {
					if parent.NativeType == fleetType || !strings.EqualFold(text(object(object(raw["properties"])["target"])["id"]), parent.NativeID) {
						continue
					}
				}
				children = append(children, serviceChild{kind: kind, id: id, data: raw, direct: parent.NativeType == fleetType})
			}
		}
		current, err := c.fleetRead(ctx, parent.NativeType, parent.NativeID)
		if err != nil {
			return nil, "", contracts.DependencyReadError(err)
		}
		if c.privateConfiguration(fleetSnapshot(parent.NativeType, raw)) != c.privateConfiguration(fleetSnapshot(parent.NativeType, current.data)) {
			return nil, "", serviceDenied("fleet_parent_configuration_changed")
		}
		slices.SortFunc(children, func(a, b serviceChild) int { return strings.Compare(a.id, b.id) })
		return children, c.privateConfiguration(snapshot), nil
	}
	_, first, err := observe()
	if err != nil {
		return nil, err
	}
	children, second, err := observe()
	if err != nil {
		return nil, err
	}
	if first != second {
		return nil, serviceDenied("fleet_children_changed")
	}
	return children, nil
}

func fleetChildRelation(parent, child asset.Asset) bool {
	if parent.Identity.NativeType == fleetType {
		return slices.Contains(fleetDirectKinds, child.Identity.NativeType) && fleetParent(child.Identity.NativeID, child.Identity.NativeType) == parent.Identity.NativeID
	}
	return parent.Identity.NativeType == fleetRunType && child.Identity.NativeType == fleetGateType && slices.Contains(stringValues(object(child.Normalized["_fleet_references"])[fleetRunType]), parent.Identity.NativeID)
}

func (c *client) contributeFleetReferences(ctx context.Context, value asset.Asset, assets []asset.Asset) (result governance.Contribution, err error) {
	defer func() { err = contracts.DependencyReadError(err) }()
	if value.ID == "" || value.Identity.ConnectionID == "" || value.Identity.Partition == "" {
		return result, serviceDenied("invalid_fleet_graph_identity")
	}
	if _, err := c.fleetRecordedReferences(value); err != nil {
		return result, err
	}
	var refs map[string][]string
	for range 2 {
		live, err := c.fleetRead(ctx, value.Identity.NativeType, value.Identity.NativeID)
		if err != nil {
			return result, err
		}
		if err := c.fleetIncarnation(value, live.data); err != nil {
			return result, err
		}
		if err := c.fleetContext(ctx, value, live.data); err != nil {
			return result, err
		}
		refs, err = fleetCurrentReferences(value.Identity.NativeType, live.data)
		if err != nil {
			return result, err
		}
		if value.Identity.NativeType == fleetMeshType {
			previous := object(value.Normalized[fleetMeshState]) // Authenticated above.
			state, _, err := c.readFleetMesh(ctx, value.Identity.NativeID, live.data, previous, assets...)
			if err != nil {
				return result, err
			}
			if c.privateConfiguration(previous) != c.privateConfiguration(state) {
				return result, serviceDenied("fleet_mesh_membership_changed")
			}
			for id := range object(state["members"]) {
				addReference(refs, fleetMemberType, id)
			}
		}
	}
	// Dynamic placement may select any member. These conservative dependency
	// edges require explicit namespace cleanup before member removal; they do
	// not claim that an empty fixed list proves no affected member workloads.
	if value.Normalized["placement_dynamic"] == true {
		for _, candidate := range assets {
			if candidate.Identity.Provider == value.Identity.Provider && candidate.Identity.ConnectionID == value.Identity.ConnectionID && candidate.Identity.Partition == value.Identity.Partition && candidate.Identity.NativeType == fleetMemberType && fleetParent(candidate.Identity.NativeID, fleetMemberType) == fleetParent(value.Identity.NativeID, fleetNamespaceType) {
				addReference(refs, fleetMemberType, candidate.Identity.NativeID)
			}
		}
	}
	result, err = c.contributeNativeReferences(value, assets, refs, "azure:fleet-reference")
	if err != nil {
		return result, err
	}
	for _, reference := range slices.Clone(result.Relationships) {
		kind := text(reference.Evidence["resource_type"])
		if kind == fleetType || value.Identity.NativeType == fleetGateType {
			continue // Native lifecycle bindings carry these deletion semantics.
		}
		evidence := maps.Clone(reference.Evidence)
		evidence[graph.RelationshipEvidenceRequiredDeletion] = true
		evidence[graph.RelationshipEvidenceAutomaticSelection] = false
		evidence[graph.RelationshipEvidenceAuthority] = graph.AuthorityAuthoritative
		evidence[graph.RelationshipEvidenceDeletionOrder] = graph.DeletionOrderTargetBeforeSource
		if value.Normalized["placement_dynamic"] == true && kind == fleetMemberType {
			evidence["placement_dynamic"] = true
		}
		result.Relationships = append(result.Relationships, graph.Relationship{SourceAssetID: reference.TargetAssetID, TargetAssetID: value.ID, Type: graph.RelationshipDependsOn, Source: "azure:fleet-required-cleanup", Confidence: 1, Evidence: evidence})
	}
	return result, nil
}
