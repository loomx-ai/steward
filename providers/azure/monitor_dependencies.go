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

func (c *client) monitorReferencesBinding(id, kind, configuration, group string, refs map[string]any) string {
	return c.privateConfiguration(map[string]any{"id": id, "kind": kind, "configuration": configuration, "group": group, "references": refs})
}

// The native source may already be gone by the time its destination executes.
// Bind the public reference projection to the private scan proof so a stored
// prerequisite cannot acquire a different destination after source deletion.
func (c *client) monitorRecordedReferences(value asset.Asset) (map[string]any, error) {
	refs, ok := value.Normalized["_monitor_references"].(map[string]any)
	if !ok || text(value.Normalized[monitorConfigurationProof]) == "" || text(value.Normalized[monitorGroupProof]) == "" {
		return nil, serviceDenied("monitor_reference_proof_missing")
	}
	for kind, values := range refs {
		var ids []string
		switch values := values.(type) {
		case []string:
			ids = values
		case []any:
			for _, value := range values {
				id, ok := value.(string)
				if !ok {
					return nil, serviceDenied("invalid_monitor_recorded_reference")
				}
				ids = append(ids, id)
			}
		default:
			return nil, serviceDenied("invalid_monitor_recorded_reference")
		}
		seen := map[string]bool{}
		for _, id := range ids {
			canonical, typ, err := parseID(id)
			if seen[id] || err == nil && (id != canonical || !strings.EqualFold(kind, typ)) || err != nil && !monitorReceiverSelector(kind, id) {
				return nil, serviceDenied("invalid_monitor_recorded_reference_identity")
			}
			seen[id] = true
		}
	}
	expected := c.monitorReferencesBinding(value.Identity.NativeID, value.Identity.NativeType, text(value.Normalized[monitorConfigurationProof]), text(value.Normalized[monitorGroupProof]), refs)
	if text(value.Normalized[monitorReferencesProof]) != expected {
		return nil, serviceDenied("monitor_recorded_references_changed")
	}
	return refs, nil
}

func monitorIncomingKinds(target string) []string {
	kinds := []string{monitorMetricAlertType, monitorActivityAlertType, monitorScheduledRuleType, monitorSmartAlertType, monitorPrometheusType, monitorProcessingType}
	if strings.EqualFold(target, monitorActionGroupType) {
		kinds = append(kinds, monitorConsumptionBudgetType, monitorCostBudgetType)
	}
	if strings.EqualFold(target, applicationInsightsType) {
		kinds = append(kinds, insightsWebTestType)
	}
	for _, kind := range []string{appSiteType, appFunctionType, "Microsoft.Logic/workflows", "Microsoft.Automation/automationAccounts", "Microsoft.Automation/automationAccounts/webhooks", "Microsoft.Automation/automationAccounts/runbooks", insightsWorkspaceType, eventHubNamespaceType, eventHubType} {
		if strings.EqualFold(target, kind) {
			kinds = append(kinds, monitorActionGroupType)
			break
		}
	}
	return kinds
}

type monitorIncomingSource struct {
	resource   serviceChild
	references map[string][]string
	group      map[string]any
}

// Read each native source family once for the whole set of targets, including
// unindexed sources. Every collection reconciles List with full private GETs.
// Receiver indexes are reused only within this observation and rechecked before
// returning, so the walk cannot preserve a cached absence across graph passes.
func (c *client) monitorIncomingObservation(ctx context.Context, targets []asset.Asset) (incoming map[string][]monitorIncomingSource, err error) {
	defer func() { err = contracts.DependencyReadError(err) }()
	incoming = map[string][]monitorIncomingSource{}
	kinds := map[string]bool{}
	for _, target := range targets {
		id, kind, err := parseID(target.Identity.NativeID)
		if monitorBudgetPath(target.Identity.NativeID) {
			id, _, kind, err = monitorResourceID(target.Identity.NativeID)
		}
		if target.Identity.NativeType == diagnosticSettingsType {
			id, _, kind, err = diagnosticResourceID(target.Identity.NativeID)
		}
		if err != nil || id != target.Identity.NativeID || !strings.EqualFold(kind, target.Identity.NativeType) || target.Identity.Provider != asset.ProviderAzure || !strings.HasPrefix(id, c.root()+"/") {
			return nil, serviceDenied("invalid_monitor_incoming_target")
		}
		if _, seen := incoming[id]; seen {
			return nil, serviceDenied("ambiguous_monitor_incoming_target")
		}
		incoming[id] = nil
		for _, kind := range monitorIncomingKinds(target.Identity.NativeType) {
			kinds[kind] = true
		}
	}
	if len(kinds) == 0 {
		return incoming, nil
	}
	var groups map[string]map[string]any
	loadGroups := func() error {
		if groups == nil {
			var err error
			groups, err = c.insightsGroups(ctx)
			return err
		}
		return nil
	}
	checkedGroups := map[string]bool{}
	indexes := map[string]map[string]map[string]any{}
	for _, kind := range slices.Sorted(maps.Keys(kinds)) {
		if budget, _ := monitorBudgetKind(kind); budget != "" {
			if err := loadGroups(); err != nil {
				return nil, err
			}
		}
		values, _, err := c.monitorResourceIndex(ctx, kind, groups)
		if err != nil {
			return nil, err
		}
		for _, id := range slices.Sorted(maps.Keys(values)) {
			_, scope, _, err := monitorResourceID(id)
			if err != nil {
				return nil, serviceDenied("monitor_incoming_group_missing")
			}
			refs, err := c.monitorReferencesWithIndexes(ctx, kind, id, values[id], indexes)
			if err != nil {
				return nil, err
			}
			for _, target := range targets {
				linked := false
				for typ, ids := range refs {
					for _, reference := range ids {
						matches, err := c.monitorReferenceMatches(target, typ, reference)
						if err != nil {
							return nil, err
						}
						linked = linked || matches
					}
				}
				if linked {
					if scope != c.root() {
						if err := loadGroups(); err != nil {
							return nil, err
						}
						if groups[scope] != nil && !checkedGroups[scope] {
							group, err := c.insightsGroup(ctx, scope, groups[scope])
							if err != nil && !isNotFound(err) {
								return nil, err
							}
							// A native source can outlive its group during deletion.
							// Keep its references as blockers; group absence alone
							// cannot grant ownership to a newly indexed source.
							groups[scope], checkedGroups[scope] = group, true
						}
					}
					group := groups[scope]
					if scope == c.root() {
						group = map[string]any{}
					}
					incoming[target.Identity.NativeID] = append(incoming[target.Identity.NativeID], monitorIncomingSource{resource: serviceChild{id: id, kind: kind, data: values[id]}, references: refs, group: group})
				}
			}
		}
	}
	for _, kind := range slices.Sorted(maps.Keys(indexes)) {
		current, err := c.monitorReceiverIndex(ctx, kind)
		if err != nil {
			return nil, err
		}
		if c.privateConfiguration(map[string]any{"index": current}) != c.privateConfiguration(map[string]any{"index": indexes[kind]}) {
			return nil, serviceDenied("monitor_incoming_receiver_index_changed")
		}
	}
	return incoming, nil
}

func (c *client) monitorIncomingTargets(ctx context.Context, targets []asset.Asset, known ...asset.Asset) (map[string][]monitorIncomingSource, error) {
	observe := func() (map[string][]monitorIncomingSource, error) {
		incoming, err := c.monitorIncomingObservation(ctx, targets)
		if err != nil {
			return nil, err
		}
		diagnostics, err := c.diagnosticIncomingObservation(ctx, targets, known)
		if err != nil {
			return nil, contracts.DependencyReadError(err)
		}
		for target, sources := range diagnostics {
			incoming[target] = append(incoming[target], sources...)
		}
		rbac, err := c.rbacIncomingObservation(ctx, targets, known)
		if err != nil {
			return nil, contracts.DependencyReadError(err)
		}
		for target, sources := range rbac {
			incoming[target] = append(incoming[target], sources...)
		}
		fleet, err := c.fleetIncomingObservation(ctx, targets, known)
		if err != nil {
			return nil, err
		}
		for target, sources := range fleet {
			incoming[target] = append(incoming[target], sources...)
		}
		return incoming, nil
	}
	return c.verifiedIncoming(observe)
}

func (c *client) verifiedIncoming(observe func() (map[string][]monitorIncomingSource, error)) (map[string][]monitorIncomingSource, error) {
	snapshot := func(incoming map[string][]monitorIncomingSource) string {
		values := map[string]any{}
		for target, sources := range incoming {
			rows := map[string]any{}
			for _, source := range sources {
				var group map[string]any
				configuration := monitorResourceSnapshot(source.resource.kind, source.resource.data)
				if source.group != nil {
					group = insightsWorkspaceResourceSnapshot(source.group)
				}
				if source.resource.kind == diagnosticSettingsType {
					configuration, group = diagnosticSnapshot(source.resource.data), source.group
				}
				if rbacResourceKind(source.resource.kind) != "" {
					configuration, group = c.rbacSnapshot(source.resource.kind, source.resource.data), source.group
				}
				if fleetKind(source.resource.kind).kind != "" {
					configuration, group = fleetSnapshot(source.resource.kind, source.resource.data), source.group
				}
				rows[source.resource.id] = map[string]any{"kind": source.resource.kind, "configuration": configuration, "group": group, "references": source.references}
			}
			values[target] = rows
		}
		return c.privateConfiguration(values)
	}
	first, err := observe()
	if err != nil {
		return nil, err
	}
	second, err := observe()
	if err != nil {
		return nil, err
	}
	if snapshot(first) != snapshot(second) {
		return nil, serviceDenied("monitor_incoming_references_changed")
	}
	return second, nil
}

func (c *client) monitorIncoming(ctx context.Context, target asset.Asset, known ...asset.Asset) ([]serviceChild, error) {
	incoming, err := c.monitorIncomingTargets(ctx, []asset.Asset{target}, known...)
	if err != nil {
		return nil, err
	}
	var values []serviceChild
	for _, source := range incoming[target.Identity.NativeID] {
		values = append(values, source.resource)
	}
	return values, nil
}

func (c *client) contributeMonitorIncoming(ctx context.Context, targets, assets []asset.Asset) (contribution governance.Contribution, err error) {
	defer func() { err = contracts.DependencyReadError(err) }()
	incoming, err := c.monitorIncomingTargets(ctx, targets, assets...)
	if err != nil {
		return contribution, err
	}
	return c.contributeIncomingSources(targets, assets, incoming)
}

func (c *client) contributeIncomingSources(targets, assets []asset.Asset, incoming map[string][]monitorIncomingSource) (contribution governance.Contribution, err error) {
	for _, target := range targets {
		for _, entry := range incoming[target.Identity.NativeID] {
			source := entry.resource
			var indexed *asset.Asset
			for i := range assets {
				candidate := &assets[i]
				if candidate.Identity.Provider != target.Identity.Provider || candidate.Identity.ConnectionID != target.Identity.ConnectionID || candidate.Identity.Partition != target.Identity.Partition || candidate.Identity.NativeID != source.id || candidate.Identity.NativeType != source.kind {
					continue
				}
				if indexed != nil || candidate.ID == "" || candidate.ID == target.ID {
					return contribution, serviceDenied("ambiguous_monitor_incoming_asset")
				}
				indexed = candidate
			}
			if indexed == nil {
				contribution.Unresolved = append(contribution.Unresolved, graph.UnresolvedReference{Provider: target.Identity.Provider, ConnectionID: target.Identity.ConnectionID, NativeType: source.kind, NativeID: source.id, ControllerID: target.ID, Relationship: graph.RelationshipDependsOn, Evidence: map[string]any{
					graph.RelationshipEvidenceRequiredDeletion: true, graph.RelationshipEvidenceAutomaticSelection: false, graph.RelationshipEvidenceAuthority: graph.AuthorityAuthoritative, graph.RelationshipEvidenceDeletionOrder: graph.DeletionOrderTargetBeforeSource, "resource_type": source.kind, "instance_id": source.id,
				}})
				continue
			}
			if fleetKind(source.kind).kind != "" {
				if err := c.fleetIncomingUnchanged(*indexed, entry); err != nil {
					return contribution, err
				}
				continue
			}
			if rbacResourceKind(source.kind) != "" {
				if err := c.rbacIncomingUnchanged(*indexed, entry); err != nil {
					return contribution, err
				}
				continue
			}
			if source.kind == diagnosticSettingsType {
				if err := c.diagnosticIncomingUnchanged(*indexed, entry); err != nil {
					return contribution, err
				}
				continue
			}
			if err := c.monitorReferencesUnchanged(*indexed, entry.references); err != nil {
				return contribution, err
			}
			if entry.group == nil || text(indexed.Normalized[monitorConfigurationProof]) != c.privateConfiguration(monitorResourceSnapshot(source.kind, source.data)) || text(indexed.Normalized[monitorGroupProof]) != c.privateConfiguration(insightsWorkspaceResourceSnapshot(entry.group)) {
				return contribution, serviceDenied("monitor_incoming_configuration_changed")
			}
			// Indexed sources contribute their own verified forward/reverse edges
			// in the same native graph pass. Do not duplicate their relationships.
		}
	}
	return contribution, nil
}
