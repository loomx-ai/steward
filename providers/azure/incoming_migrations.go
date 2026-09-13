package azure

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"

	"github.com/loomx-ai/steward/internal/app/governance"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/core/graph"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

type incomingMigration struct {
	configuration  serviceChild
	source, target map[string]any
}

func messagingNamespaceSetChanged() error {
	return &contracts.ProviderCallError{Provider: execution.ProviderError{Category: execution.ErrorRetryable, Code: "messaging_namespace_set_changed", Message: contracts.SafeProviderValidationMessage}}
}

// Migration configurations live only under the source. The target's own list
// cannot establish that no migration is copying entities into it.
// https://learn.microsoft.com/rest/api/servicebus/controlplane/migration-configs/list
func (c *client) incomingMigrations(ctx context.Context) (map[string][]incomingMigration, error) {
	metadata, err := providerData()
	if err != nil {
		return nil, err
	}
	runtime := &Runtime{bundle: metadata.bundle}
	definition, ok := runtime.productDefinition(serviceBusNamespaceType)
	if !ok || definition.Discovery.List == nil {
		return nil, fmt.Errorf("Azure namespace discovery is unavailable")
	}
	bound, err := c.bindProductList(definition.Discovery.List, "", contracts.InventoryItem{})
	if err != nil {
		return nil, err
	}
	u, _ := url.Parse(bound.URL)
	list := func() (map[string]map[string]any, error) {
		records, err := c.listAllURL(ctx, bound.URL, u.Path)
		if err != nil {
			return nil, err
		}
		namespaces := map[string]map[string]any{}
		for _, record := range records {
			raw := object(record)
			id, kind, err := parseID(text(raw["id"]))
			if err != nil || !strings.EqualFold(kind, serviceBusNamespaceType) || !strings.HasPrefix(id, c.root()+"/") || !validResponseType(serviceBusNamespaceType, text(raw["type"])) || namespaces[id] != nil {
				return nil, fmt.Errorf("invalid or duplicate Azure messaging namespace")
			}
			namespaces[id] = raw
		}
		return namespaces, nil
	}
	listed, err := list()
	if err != nil {
		return nil, err
	}
	namespaces := map[string]map[string]any{}
	var configurations []serviceChild
	var ids []string
	for id := range listed {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		record := listed[id]
		kind, _ := findType(serviceBusNamespaceType)
		endpoint, err := c.resourceURL(kind, id)
		if err != nil {
			return nil, err
		}
		live, err := c.request(ctx, "GET", endpoint)
		if isNotFound(err) {
			return nil, messagingNamespaceSetChanged()
		}
		if err != nil {
			return nil, err
		}
		if !validResourceResponse(live, id, serviceBusNamespaceType) {
			return nil, fmt.Errorf("Azure messaging namespace identity mismatch")
		}
		if err := serviceListedIncarnation(record, live.data); err != nil {
			return nil, messagingNamespaceSetChanged()
		}
		children, err := c.nativeServiceChildren(ctx, asset.Identity{NativeID: id, NativeType: serviceBusNamespaceType}, live.data, []string{serviceBusMigrationType})
		if isNotFound(err) || errors.Is(err, errProductParentGenerationChanged) {
			return nil, messagingNamespaceSetChanged()
		}
		if err != nil {
			return nil, err
		}
		namespaces[id] = live.data
		configurations = append(configurations, children...)
	}
	// Detect a namespace appearing or disappearing during this subscription
	// read, including another selected namespace being deleted by a sibling step.
	after, err := list()
	if err != nil {
		return nil, err
	}
	if len(after) != len(namespaces) {
		return nil, messagingNamespaceSetChanged()
	}
	for id, raw := range after {
		if namespaces[id] == nil || serviceListedIncarnation(raw, namespaces[id]) != nil {
			return nil, messagingNamespaceSetChanged()
		}
	}
	result := map[string][]incomingMigration{}
	for _, child := range configurations {
		value := object(child.data["properties"])["targetNamespace"]
		if value == nil || value == "" {
			continue
		}
		targetID, kind, err := parseID(text(value))
		sourceID := strings.Join(strings.Split(child.id, "/")[:9], "/")
		if err != nil || !strings.EqualFold(kind, serviceBusNamespaceType) || sourceID == targetID || !strings.HasPrefix(targetID, c.root()+"/") {
			return nil, serviceDenied("invalid_migration_namespace")
		}
		if namespaces[targetID] == nil {
			return nil, messagingNamespaceSetChanged()
		}
		result[targetID] = append(result[targetID], incomingMigration{configuration: child, source: namespaces[sourceID], target: namespaces[targetID]})
	}
	return result, nil
}

func (s *serviceCascades) contributeIncomingMigrations(ctx context.Context, assets []asset.Asset, result *governance.Contribution) error {
	var incoming map[string][]incomingMigration
	for _, parent := range assets {
		if parent.Identity.Provider != asset.ProviderAzure || parent.Identity.NativeType != serviceBusNamespaceType {
			continue
		}
		if incoming == nil {
			var err error
			incoming, err = s.client.incomingMigrations(ctx)
			if err != nil {
				return err
			}
		}
		for _, migration := range incoming[strings.ToLower(parent.Identity.NativeID)] {
			child := migration.configuration
			evidence := map[string]any{graph.RelationshipEvidenceRequiredDeletion: true, graph.RelationshipEvidenceAuthority: graph.AuthorityAuthoritative, graph.RelationshipEvidenceDeletionOrder: graph.DeletionOrderTargetBeforeSource, "resource_type": child.kind, "instance_id": child.id}
			var configuration *asset.Asset
			for i := range assets {
				candidate := &assets[i]
				if candidate.Identity.Provider == parent.Identity.Provider && candidate.Identity.ConnectionID == parent.Identity.ConnectionID && candidate.Identity.Partition == parent.Identity.Partition && strings.EqualFold(candidate.Identity.NativeType, child.kind) && strings.EqualFold(candidate.Identity.NativeID, child.id) {
					if configuration != nil {
						return fmt.Errorf("ambiguous Azure incoming migration")
					}
					configuration = candidate
				}
			}
			if configuration == nil {
				result.Unresolved = append(result.Unresolved, graph.UnresolvedReference{BlocksCleanup: true, Provider: parent.Identity.Provider, ConnectionID: parent.Identity.ConnectionID, NativeType: child.kind, NativeID: child.id, ControllerID: parent.ID, Relationship: graph.RelationshipDependsOn, Evidence: evidence})
				continue
			}
			if err := serviceIncarnation(*configuration, child.data); err != nil {
				return err
			}
			if err := serviceIncarnation(parent, migration.target); err != nil {
				return err
			}
			if !incomingMigrationPrerequisite(parent, *configuration) || text(configuration.Normalized["_migration_configuration"]) != migrationConfiguration(child.data) || text(configuration.Normalized["_migration_source_creation"]) != creationGeneration(migration.source) || text(configuration.Normalized["_migration_target_creation"]) != creationGeneration(migration.target) {
				return serviceDenied("incoming_migration_changed")
			}
			result.Relationships = append(result.Relationships, graph.Relationship{SourceAssetID: parent.ID, TargetAssetID: configuration.ID, Type: graph.RelationshipDependsOn, Source: "azure:incoming-migration", Evidence: evidence, Confidence: 1})
		}
	}
	return nil
}

func incomingMigrationPrerequisite(parent, configuration asset.Asset) bool {
	return parent.Identity.NativeType == serviceBusNamespaceType && configuration.Identity.NativeType == serviceBusMigrationType &&
		strings.EqualFold(text(configuration.Normalized["targetNamespace"]), parent.Identity.NativeID) &&
		text(configuration.Normalized["_migration_configuration"]) != "" && text(configuration.Normalized["_migration_source_creation"]) != "" &&
		text(configuration.Normalized["_migration_target_creation"]) != "" && text(configuration.Normalized["_migration_target_creation"]) == text(parent.Normalized["_arm_creation_generation"])
}
