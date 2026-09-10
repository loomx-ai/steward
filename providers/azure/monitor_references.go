package azure

import (
	"context"
	"strings"

	"github.com/loomx-ai/steward/internal/app/governance"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

// Only documented resource slots establish dependencies. Authored queries,
// expressions, webhook URLs and notification payloads remain private content.
func monitorResourceReferences(kind, self string, raw map[string]any) (map[string][]string, error) {
	refs := map[string][]string{}
	add := func(value any, expected string) error {
		wire, ok := value.(string)
		id, typ, err := parseID(wire)
		if !ok || wire != strings.TrimSpace(wire) || err != nil || expected != "" && !strings.EqualFold(typ, expected) {
			return serviceDenied("invalid_monitor_resource_reference")
		}
		if id == self {
			return serviceDenied("monitor_resource_self_reference")
		}
		if mapping, ok := findType(typ); ok {
			typ = mapping.NativeType
		} else if canonical := monitorResourceKind(typ); canonical != "" {
			typ = canonical
		}
		addReference(refs, typ, id)
		return nil
	}
	groups, err := monitorRuleActionGroups(kind, raw)
	if budget, _ := monitorBudgetKind(kind); budget != "" {
		groups, err = monitorBudgetActionGroups(raw)
	}
	if err != nil {
		return nil, err
	}
	for _, id := range groups {
		if err := add(id, monitorActionGroupType); err != nil {
			return nil, err
		}
	}
	if monitorRuleKind(kind).kind != "" && kind != monitorActionGroupType && kind != insightsWebTestType {
		scopes, err := monitorRuleScopes(kind, raw)
		if err != nil {
			return nil, err
		}
		for _, id := range scopes {
			// A subscription prefix scopes evaluation; it is not a deletable
			// resource or an ownership grant over everything in the subscription.
			if len(strings.Split(id, "/")) == 3 {
				continue
			}
			if err := add(id, ""); err != nil {
				return nil, err
			}
		}
	}
	props := object(raw["properties"])
	if kind == monitorMetricAlertType && text(object(props["criteria"])["odata.type"]) == "Microsoft.Azure.Monitor.WebtestLocationAvailabilityCriteria" {
		criteria := object(props["criteria"])
		if err := monitorRuleFields(criteria, "componentId", "webTestId"); err != nil {
			return nil, err
		}
		if err := add(criteria["componentId"], applicationInsightsType); err != nil {
			return nil, err
		}
		if err := add(criteria["webTestId"], insightsWebTestType); err != nil {
			return nil, err
		}
	}
	if kind == insightsWebTestType {
		for key, value := range object(raw["tags"]) {
			if strings.HasPrefix(strings.ToLower(key), "hidden-link:") {
				if value != "Resource" {
					return nil, serviceDenied("invalid_monitor_web_test_component_link")
				}
				if err := add(key[len("hidden-link:"):], applicationInsightsType); err != nil {
					return nil, err
				}
			}
		}
	}
	if kind == monitorMetricAlertType || kind == monitorScheduledRuleType {
		if err := monitorRuleFields(raw, "identity"); err != nil {
			return nil, err
		}
	}
	if value := raw["identity"]; value != nil && (kind == monitorMetricAlertType || kind == monitorScheduledRuleType) {
		identity, ok := value.(map[string]any)
		if !ok || monitorRuleFields(identity, "type", "userAssignedIdentities") != nil {
			return nil, serviceDenied("invalid_monitor_resource_identity_configuration")
		}
		if identity["type"] != "UserAssigned" && identity["type"] != "SystemAssigned" && identity["type"] != "None" {
			return nil, serviceDenied("invalid_monitor_managed_identity_type")
		}
		if value := identity["userAssignedIdentities"]; value != nil {
			identities, ok := value.(map[string]any)
			if !ok || len(identities) != 0 && identity["type"] != "UserAssigned" {
				return nil, serviceDenied("invalid_monitor_assigned_identities")
			}
			for id, value := range identities {
				if _, ok := value.(map[string]any); !ok {
					return nil, serviceDenied("invalid_monitor_assigned_identity_properties")
				}
				if err := add(id, "Microsoft.ManagedIdentity/userAssignedIdentities"); err != nil {
					return nil, err
				}
			}
		}
	}
	if kind == monitorActionGroupType {
		for _, receiver := range []struct{ collection, field, kind string }{
			{"azureFunctionReceivers", "functionAppResourceId", "Microsoft.Web/sites"},
			{"logicAppReceivers", "resourceId", "Microsoft.Logic/workflows"},
			{"automationRunbookReceivers", "automationAccountId", "Microsoft.Automation/automationAccounts"},
			{"automationRunbookReceivers", "webhookResourceId", "Microsoft.Automation/automationAccounts/webhooks"},
		} {
			if err := monitorRuleFields(props, receiver.collection); err != nil {
				return nil, err
			}
			for _, value := range array(props[receiver.collection]) {
				row := object(value)
				if err := monitorRuleFields(row, receiver.field); err != nil {
					return nil, err
				}
				if err := add(row[receiver.field], receiver.kind); err != nil {
					return nil, err
				}
			}
		}
	}
	return refs, nil
}

func (c *client) contributeMonitorReferences(ctx context.Context, parent asset.Asset, assets []asset.Asset) (contribution governance.Contribution, err error) {
	defer func() { err = contracts.DependencyReadError(err) }()
	id, scope, kind, err := monitorResourceID(parent.Identity.NativeID)
	if err != nil || id != parent.Identity.NativeID || !strings.HasPrefix(id, c.root()+"/") || kind != parent.Identity.NativeType || parent.ID == "" || parent.Identity.Provider != asset.ProviderAzure || parent.Identity.ConnectionID == "" || parent.Identity.Partition == "" || text(parent.Normalized[monitorConfigurationProof]) == "" || text(parent.Normalized[monitorGroupProof]) == "" {
		return contribution, serviceDenied("invalid_monitor_graph_identity")
	}
	current, err := c.monitorResourceRead(ctx, kind, id)
	if err != nil {
		return contribution, err
	}
	if c.privateConfiguration(monitorResourceSnapshot(kind, current.data)) != text(parent.Normalized[monitorConfigurationProof]) || monitorResourceRegion(kind, current.data) != parent.Location {
		return contribution, serviceDenied("monitor_graph_configuration_changed")
	}
	group := map[string]any{}
	if scope != c.root() {
		group, err = c.workbookGroup(ctx, id)
		if err != nil {
			return contribution, err
		}
	}
	if c.privateConfiguration(insightsWorkspaceResourceSnapshot(group)) != text(parent.Normalized[monitorGroupProof]) {
		return contribution, serviceDenied("monitor_graph_resource_group_changed")
	}
	refs, err := monitorResourceReferences(kind, id, current.data)
	if err != nil {
		return contribution, err
	}
	contribution, err = c.contributeNativeReferences(parent, assets, refs, "azure:monitor-reference")
	if err != nil {
		return contribution, err
	}
	// The referencing rule/budget remains independently managed. Require its
	// explicit selection before deleting a shared destination; never acquire
	// ownership or automatically select it through the reverse relationship.
	for _, reference := range contribution.Relationships {
		contribution.Relationships = append(contribution.Relationships, graph.Relationship{
			SourceAssetID: reference.TargetAssetID, TargetAssetID: parent.ID,
			Type: graph.RelationshipDependsOn, Source: "azure:monitor-required-cleanup", Confidence: 1,
			Evidence: map[string]any{
				graph.RelationshipEvidenceRequiredDeletion:   true,
				graph.RelationshipEvidenceAutomaticSelection: false,
				graph.RelationshipEvidenceAuthority:          graph.AuthorityAuthoritative,
				graph.RelationshipEvidenceDeletionOrder:      graph.DeletionOrderTargetBeforeSource,
				"resource_type":                              kind, "instance_id": id,
			},
		})
	}
	return contribution, nil
}
