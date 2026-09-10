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
			if err != nil || id != canonical || !strings.EqualFold(kind, typ) || seen[id] {
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
	for _, kind := range []string{"Microsoft.Web/sites", "Microsoft.Logic/workflows", "Microsoft.Automation/automationAccounts", "Microsoft.Automation/automationAccounts/webhooks", "Microsoft.OperationalInsights/workspaces"} {
		if strings.EqualFold(target, kind) {
			kinds = append(kinds, monitorActionGroupType)
			break
		}
	}
	return kinds
}

// Incoming dependencies must come from native collections even when no saved
// asset or graph edge exists. Each List is reconciled with full private GETs.
func (c *client) monitorIncoming(ctx context.Context, target asset.Identity) (incoming []serviceChild, err error) {
	defer func() { err = contracts.DependencyReadError(err) }()
	groups, err := c.insightsGroups(ctx)
	if err != nil {
		return nil, err
	}
	for _, id := range slices.Sorted(maps.Keys(groups)) {
		group, err := c.insightsGroup(ctx, id, groups[id])
		if err != nil {
			return nil, err
		}
		groups[id] = group
	}
	for _, kind := range monitorIncomingKinds(target.NativeType) {
		values, _, err := c.monitorResourceIndex(ctx, kind, groups)
		if err != nil {
			return nil, err
		}
		for _, id := range slices.Sorted(maps.Keys(values)) {
			_, scope, _, err := monitorResourceID(id)
			if err != nil || scope != c.root() && groups[scope] == nil {
				return nil, serviceDenied("monitor_incoming_group_missing")
			}
			refs, err := monitorResourceReferences(kind, id, values[id])
			if err != nil {
				return nil, err
			}
			for typ, ids := range refs {
				if strings.EqualFold(typ, target.NativeType) && slices.Contains(ids, target.NativeID) {
					incoming = append(incoming, serviceChild{id: id, kind: kind, data: values[id]})
				}
			}
		}
	}
	return incoming, nil
}

func (c *client) contributeMonitorIncoming(ctx context.Context, target asset.Asset, assets []asset.Asset) (contribution governance.Contribution, err error) {
	defer func() { err = contracts.DependencyReadError(err) }()
	incoming, err := c.monitorIncoming(ctx, target.Identity)
	if err != nil {
		return contribution, err
	}
	for _, source := range incoming {
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
		if _, err := c.monitorRecordedReferences(*indexed); err != nil {
			return contribution, err
		}
		if text(indexed.Normalized[monitorConfigurationProof]) != c.privateConfiguration(monitorResourceSnapshot(source.kind, source.data)) {
			return contribution, serviceDenied("monitor_incoming_configuration_changed")
		}
		// Indexed sources contribute their own verified forward/reverse edges
		// in the same native graph pass. Do not duplicate their relationships.
	}
	return contribution, nil
}
