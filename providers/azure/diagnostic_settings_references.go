package azure

import (
	"context"
	"strings"

	"github.com/loomx-ai/steward/internal/app/governance"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

// Diagnostic destinations are references, not owned resources. This parser
// performs no destination reads, including for cross-subscription settings.
func diagnosticReferences(self string, raw map[string]any) (map[string][]string, error) {
	id, scope, kind, err := diagnosticResourceID(self)
	if err != nil || id != self || kind != diagnosticSettingsType {
		return nil, serviceDenied("invalid_diagnostic_reference_source")
	}
	props, ok := raw["properties"].(map[string]any)
	if !ok || monitorRuleFields(props, "storageAccountId", "workspaceId", "marketplacePartnerId", "eventHubAuthorizationRuleId", "eventHubName", "serviceBusRuleId") != nil {
		return nil, serviceDenied("invalid_diagnostic_reference_properties")
	}
	refs := map[string][]string{}
	add := func(wire string, kinds ...string) (string, string, error) {
		id, kind, err := parseID(wire)
		if err != nil || wire != strings.TrimSpace(wire) || strings.ContainsAny(wire, "\t") || id == self {
			return "", "", serviceDenied("invalid_diagnostic_destination")
		}
		if len(kinds) != 0 {
			matched := false
			for _, expected := range kinds {
				if strings.EqualFold(expected, kind) {
					kind, matched = expected, true
					break
				}
			}
			if !matched {
				return "", "", serviceDenied("invalid_diagnostic_destination_type")
			}
		} else if mapping, ok := findType(kind); ok {
			kind = mapping.NativeType
		}
		addReference(refs, kind, id)
		// Removing an ancestor also removes the source or destination resource.
		// These edges require the setting's explicit prior deletion; they grant
		// no ownership of either ancestor or any shared destination.
		parts := strings.Split(id, "/")
		for end := len(parts) - 2; end >= 5; end -= 2 {
			if parts[end-2] == "providers" {
				continue // A provider namespace alone is not a resource.
			}
			ancestor, typ, err := parseID(strings.Join(parts[:end], "/"))
			if err != nil {
				return "", "", serviceDenied("invalid_diagnostic_reference_ancestor")
			}
			if mapping, known := findType(typ); known {
				typ = mapping.NativeType
			}
			addReference(refs, typ, ancestor)
		}
		return id, kind, nil
	}
	// The source must also have its setting removed before deletion. A parent
	// resource's absence is not evidence that its diagnostic setting vanished.
	if len(strings.Split(scope, "/")) > 3 {
		if _, _, err := add(scope); err != nil {
			return nil, err
		}
	}
	stringsByField := map[string]string{}
	for _, field := range []string{"storageAccountId", "workspaceId", "marketplacePartnerId", "eventHubAuthorizationRuleId", "eventHubName", "serviceBusRuleId"} {
		if value := props[field]; value != nil {
			wire, ok := value.(string)
			if !ok || wire != strings.TrimSpace(wire) {
				return nil, serviceDenied("invalid_diagnostic_destination_field")
			}
			stringsByField[field] = wire
		}
	}
	for _, row := range []struct{ field, kind string }{
		{"storageAccountId", "Microsoft.Storage/storageAccounts"},
		{"workspaceId", insightsWorkspaceType},
		{"marketplacePartnerId", ""},
	} {
		if wire := stringsByField[row.field]; wire != "" {
			var kinds []string
			if row.kind != "" {
				kinds = []string{row.kind}
			}
			if _, _, err := add(wire, kinds...); err != nil {
				return nil, err
			}
		}
	}
	for _, field := range []string{"eventHubAuthorizationRuleId", "serviceBusRuleId"} {
		wire := stringsByField[field]
		if wire == "" {
			continue
		}
		id, kind, err := add(wire, "Microsoft.EventHub/namespaces/authorizationRules", "Microsoft.ServiceBus/namespaces/authorizationRules")
		if err != nil {
			return nil, err
		}
		namespace := strings.Join(strings.Split(id, "/")[:len(strings.Split(id, "/"))-2], "/")
		// Without eventHubName, the service selects its default. Do not invent
		// a hub name; the namespace and authorization rule remain dependencies.
		if name := stringsByField["eventHubName"]; name != "" && field == "eventHubAuthorizationRuleId" {
			if !monitorReceiverName(name) {
				return nil, serviceDenied("invalid_diagnostic_event_hub_name")
			}
			if _, _, err := add(namespace+"/eventhubs/"+name, strings.TrimSuffix(kind, "/authorizationRules")+"/eventhubs"); err != nil {
				return nil, err
			}
		}
	}
	if stringsByField["eventHubName"] != "" && stringsByField["eventHubAuthorizationRuleId"] == "" {
		return nil, serviceDenied("diagnostic_event_hub_rule_missing")
	}
	return refs, nil
}

func (c *client) diagnosticRecordedReferences(value asset.Asset) (map[string]any, error) {
	id, _, kind, err := diagnosticResourceID(value.Identity.NativeID)
	refs, ok := value.Normalized["_diagnostic_references"].(map[string]any)
	configuration, context := text(value.Normalized[diagnosticConfigurationProof]), text(value.Normalized[diagnosticContextProof])
	if err != nil || id != value.Identity.NativeID || kind != diagnosticSettingsType || kind != value.Identity.NativeType || !strings.HasPrefix(id, c.root()+"/") || !ok || configuration == "" || context == "" {
		return nil, serviceDenied("diagnostic_recorded_reference_proof_missing")
	}
	for kind, value := range refs {
		values := stringValues(value)
		switch raw := value.(type) {
		case []string:
		case []any:
			if len(raw) != len(values) {
				return nil, serviceDenied("invalid_diagnostic_recorded_references")
			}
		default:
			return nil, serviceDenied("invalid_diagnostic_recorded_references")
		}
		if len(values) == 0 {
			return nil, serviceDenied("empty_diagnostic_recorded_reference")
		}
		seen := map[string]bool{}
		for _, wire := range values {
			id, typ, err := parseID(wire)
			if err != nil || wire != id || seen[id] || !strings.EqualFold(typ, kind) {
				return nil, serviceDenied("invalid_diagnostic_recorded_reference_identity")
			}
			seen[id] = true
		}
	}
	if text(value.Normalized[diagnosticReferencesProof]) != c.diagnosticReferenceBinding(id, configuration, context, refs) {
		return nil, serviceDenied("diagnostic_recorded_references_changed")
	}
	if _, err := c.diagnosticPlannedWire(id, value.Normalized); err != nil {
		return nil, err
	}
	return refs, nil
}

func (c *client) contributeDiagnosticReferences(ctx context.Context, parent asset.Asset, assets []asset.Asset) (contribution governance.Contribution, err error) {
	defer func() { err = contracts.DependencyReadError(err) }()
	driver, err := newDiagnosticAction(c, parent.Identity.ConnectionID, parent)
	if err != nil {
		return contribution, err
	}
	var current response
	for range 2 {
		current, _, err = driver.current(ctx)
		if err != nil {
			return contribution, err
		}
	}
	refs, err := diagnosticReferences(parent.Identity.NativeID, current.data)
	if err != nil {
		return contribution, err
	}
	contribution, err = c.contributeNativeReferences(parent, assets, refs, "azure:diagnostic-reference")
	if err != nil {
		return contribution, err
	}
	for _, reference := range contribution.Relationships {
		contribution.Relationships = append(contribution.Relationships, graph.Relationship{
			SourceAssetID: reference.TargetAssetID, TargetAssetID: parent.ID,
			Type: graph.RelationshipDependsOn, Source: "azure:diagnostic-required-cleanup", Confidence: 1,
			Evidence: map[string]any{
				graph.RelationshipEvidenceRequiredDeletion: true, graph.RelationshipEvidenceAutomaticSelection: false,
				graph.RelationshipEvidenceAuthority: graph.AuthorityAuthoritative, graph.RelationshipEvidenceDeletionOrder: graph.DeletionOrderTargetBeforeSource,
				"resource_type": diagnosticSettingsType, "instance_id": parent.Identity.NativeID,
			},
		})
	}
	return contribution, nil
}
