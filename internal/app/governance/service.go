package governance

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
	"github.com/loomx-ai/steward/internal/idgen"
	"github.com/loomx-ai/steward/internal/provider/spec"
)

type AssetReader interface {
	ListActiveAssetsByConnection(context.Context, asset.ConnectionID, asset.ResourceKindID) ([]asset.Asset, error)
}

type GraphRepository interface {
	ReplaceGraph(context.Context, asset.ScopeID, string, []graph.Relationship, []graph.LifecycleBinding) error
}

type Contribution struct {
	Relationships []graph.Relationship
	Bindings      []graph.LifecycleBinding
	Unresolved    []graph.UnresolvedReference
}

// Contributor is a read-only graph extension point. It can contribute
// evidence-backed facts but has no access to cleanup actions or the executor.
type Contributor interface {
	Contribute(context.Context, asset.ScopeID, []asset.Asset) (Contribution, error)
}

type GraphResult struct {
	Revision      string
	Relationships []graph.Relationship
	Bindings      []graph.LifecycleBinding
	Unresolved    []graph.UnresolvedReference
}

type ServiceOption func(*Service)

type Service struct {
	assets AssetReader
	graphs GraphRepository
	clock  func() time.Time
}

func NewService(assets AssetReader, graphs GraphRepository, options ...ServiceOption) *Service {
	service := &Service{assets: assets, graphs: graphs, clock: func() time.Time { return time.Now().UTC() }}
	for _, option := range options {
		option(service)
	}
	return service
}

func WithClock(clock func() time.Time) ServiceOption {
	return func(service *Service) { service.clock = clock }
}

func (s *Service) RebuildGraph(ctx context.Context, scopeID asset.ScopeID, connectionID asset.ConnectionID, revision string, bundle spec.Bundle, contributors []Contributor) (GraphResult, error) {
	if scopeID == "" || connectionID == "" || strings.TrimSpace(revision) == "" {
		return GraphResult{}, fmt.Errorf("scope, connection, and graph revision are required")
	}
	assets, err := s.assets.ListActiveAssetsByConnection(ctx, connectionID, "")
	if err != nil {
		return GraphResult{}, err
	}
	for _, value := range assets {
		if value.Identity.ConnectionID != connectionID {
			return GraphResult{}, fmt.Errorf("asset %q does not belong to graph connection %q", value.ID, connectionID)
		}
	}
	sort.Slice(assets, func(i, j int) bool { return assets[i].ID < assets[j].ID })
	byIdentity := make(map[string]asset.Asset, len(assets))
	byID := make(map[asset.AssetID]asset.Asset, len(assets))
	for _, value := range assets {
		byIdentity[value.Identity.Key()] = value
		byID[value.ID] = value
	}
	specs := make(map[string]spec.CompiledSpec, len(bundle.Specs))
	for _, compiled := range bundle.Specs {
		nativeType := compiled.ResourceKind.NativeType
		if nativeType == "" {
			nativeType = compiled.Definition.Metadata.NativeType
		}
		specs[nativeType] = compiled
	}
	result := GraphResult{Revision: revision}
	observedAt := s.clock()
	for _, source := range assets {
		compiled, ok := specs[source.Identity.NativeType]
		if !ok {
			continue
		}
		for _, mapping := range compiled.Definition.Relationships {
			values, ok := pathStrings(source.Normalized, mapping.TargetIDPath)
			if !ok {
				continue
			}
			for _, nativeID := range values {
				targetIdentity := asset.Identity{
					Provider: source.Identity.Provider, Partition: source.Identity.Partition, ConnectionID: source.Identity.ConnectionID,
					NativeType: mapping.TargetType, NativeID: nativeID, ScopeKey: source.Identity.ScopeKey,
				}
				target, resolved := byIdentity[targetIdentity.Key()]
				if !resolved {
					result.Unresolved = append(result.Unresolved, graph.UnresolvedReference{
						Provider: source.Identity.Provider, ConnectionID: source.Identity.ConnectionID,
						NativeType: mapping.TargetType, NativeID: nativeID, ControllerID: source.ID,
						Relationship: graph.RelationshipType(mapping.Type), GraphRevision: revision,
						Evidence: map[string]any{"source": "spec", "target_id_path": mapping.TargetIDPath, "spec_bundle_revision": bundle.Revision},
					})
					continue
				}
				if target.ID == source.ID {
					continue
				}
				relationshipType := graph.RelationshipType(mapping.Type)
				relationshipSource := "spec"
				if source.Identity.Provider == asset.ProviderAliCloud {
					switch strings.TrimSpace(fmt.Sprint(source.Normalized["_inventory_source"])) {
					case "resource-center":
						relationshipSource = "resource_center_configuration"
					default:
						relationshipSource = "product_api"
					}
				}
				result.Relationships = append(result.Relationships, graph.Relationship{
					ID: relationshipID(revision, source.ID, target.ID, relationshipType, relationshipSource), SourceAssetID: source.ID, TargetAssetID: target.ID,
					Type: relationshipType, Source: relationshipSource, Confidence: 1, GraphRevision: revision, ObservedAt: observedAt,
					Evidence: map[string]any{"target_id_path": mapping.TargetIDPath, "target_native_id": nativeID, "spec_bundle_revision": bundle.Revision},
				})
			}
		}
	}
	for _, contributor := range contributors {
		if contributor == nil {
			continue
		}
		contributorAssets, err := cloneAssets(assets)
		if err != nil {
			return GraphResult{}, err
		}
		contribution, err := contributor.Contribute(ctx, scopeID, contributorAssets)
		if err != nil {
			return GraphResult{}, err
		}
		if err := validateContribution(contribution, connectionID, byID); err != nil {
			return GraphResult{}, err
		}
		for _, relationship := range contribution.Relationships {
			if relationship.Confidence < 0 || relationship.Confidence > 1 {
				return GraphResult{}, fmt.Errorf("relationship confidence %.3f is outside [0,1]", relationship.Confidence)
			}
			relationship.GraphRevision = revision
			if relationship.ObservedAt.IsZero() {
				relationship.ObservedAt = observedAt
			}
			if relationship.ID == "" {
				relationship.ID = relationshipID(revision, relationship.SourceAssetID, relationship.TargetAssetID, relationship.Type, relationship.Source)
			}
			result.Relationships = append(result.Relationships, relationship)
		}
		for _, binding := range contribution.Bindings {
			if binding.Confidence < 0 || binding.Confidence > 1 {
				return GraphResult{}, fmt.Errorf("lifecycle binding confidence %.3f is outside [0,1]", binding.Confidence)
			}
			binding.GraphRevision = revision
			if binding.ObservedAt.IsZero() {
				binding.ObservedAt = observedAt
			}
			if binding.ID == "" {
				binding.ID = lifecycleBindingID(revision, binding.ControllerAssetID, binding.ManagedAssetID, binding.EvidenceSource)
			}
			result.Bindings = append(result.Bindings, binding)
		}
		for _, unresolved := range contribution.Unresolved {
			unresolved.GraphRevision = revision
			result.Unresolved = append(result.Unresolved, unresolved)
		}
	}
	result.Relationships = mergeRelationships(result.Relationships)
	sort.Slice(result.Relationships, func(i, j int) bool { return result.Relationships[i].ID < result.Relationships[j].ID })
	sort.Slice(result.Bindings, func(i, j int) bool { return result.Bindings[i].ID < result.Bindings[j].ID })
	sort.Slice(result.Unresolved, func(i, j int) bool {
		left := string(result.Unresolved[i].ControllerID) + "\x00" + result.Unresolved[i].NativeType + "\x00" + result.Unresolved[i].NativeID
		right := string(result.Unresolved[j].ControllerID) + "\x00" + result.Unresolved[j].NativeType + "\x00" + result.Unresolved[j].NativeID
		return left < right
	})
	if err := s.graphs.ReplaceGraph(ctx, scopeID, revision, result.Relationships, result.Bindings); err != nil {
		return GraphResult{}, err
	}
	return result, nil
}

type relationshipMergeKey struct {
	source asset.AssetID
	target asset.AssetID
	kind   graph.RelationshipType
}

func mergeRelationships(values []graph.Relationship) []graph.Relationship {
	merged := make(map[relationshipMergeKey]graph.Relationship, len(values))
	order := make([]relationshipMergeKey, 0, len(values))
	for _, candidate := range values {
		key := relationshipMergeKey{source: candidate.SourceAssetID, target: candidate.TargetAssetID, kind: candidate.Type}
		current, exists := merged[key]
		if !exists {
			merged[key] = candidate
			order = append(order, key)
			continue
		}
		winner, loser := current, candidate
		if relationshipSourcePriority(candidate.Source) > relationshipSourcePriority(current.Source) {
			winner, loser = candidate, current
		}
		winner.Evidence = mergedRelationshipEvidence(winner, loser)
		merged[key] = winner
	}
	result := make([]graph.Relationship, 0, len(order))
	for _, key := range order {
		result = append(result, merged[key])
	}
	return result
}

func mergedRelationshipEvidence(winner, loser graph.Relationship) map[string]any {
	evidence := make(map[string]any, len(winner.Evidence)+len(loser.Evidence)+2)
	for key, value := range winner.Evidence {
		evidence[key] = value
	}
	// Lower-priority contributors may add non-conflicting semantic evidence to
	// a relationship discovered by the provider inventory. Keep the winning
	// source authoritative when both sources report the same field.
	for key, value := range loser.Evidence {
		if key == "evidence_sources" || key == "evidence_by_source" {
			continue
		}
		if _, exists := evidence[key]; !exists {
			evidence[key] = value
		}
	}
	bySource := map[string]any{
		winner.Source: cloneEvidence(winner.Evidence),
		loser.Source:  cloneEvidence(loser.Evidence),
	}
	if existing, ok := winner.Evidence["evidence_by_source"].(map[string]any); ok {
		for source, value := range existing {
			bySource[source] = value
		}
	}
	if existing, ok := loser.Evidence["evidence_by_source"].(map[string]any); ok {
		for source, value := range existing {
			bySource[source] = value
		}
	}
	sources := make([]string, 0, len(bySource))
	for source := range bySource {
		sources = append(sources, source)
	}
	sort.Slice(sources, func(i, j int) bool {
		left, right := relationshipSourcePriority(sources[i]), relationshipSourcePriority(sources[j])
		if left != right {
			return left > right
		}
		return sources[i] < sources[j]
	})
	evidence["evidence_sources"] = sources
	evidence["evidence_by_source"] = bySource
	return evidence
}

func cloneEvidence(values map[string]any) map[string]any {
	if values == nil {
		return map[string]any{}
	}
	cloned := make(map[string]any, len(values))
	for key, value := range values {
		if key != "evidence_sources" && key != "evidence_by_source" {
			cloned[key] = value
		}
	}
	return cloned
}

func relationshipSourcePriority(source string) int {
	switch strings.TrimSpace(source) {
	case "product_api", "resource_center_configuration", "spec":
		return 3
	default:
		return 2
	}
}

func validateContribution(contribution Contribution, connectionID asset.ConnectionID, assets map[asset.AssetID]asset.Asset) error {
	for _, relationship := range contribution.Relationships {
		if relationship.SourceAssetID == relationship.TargetAssetID || relationship.Type == "" || strings.TrimSpace(relationship.Source) == "" {
			return fmt.Errorf("contributor returned an invalid relationship from %q to %q", relationship.SourceAssetID, relationship.TargetAssetID)
		}
		if _, exists := assets[relationship.SourceAssetID]; !exists {
			return fmt.Errorf("contributor relationship source %q is outside connection %q", relationship.SourceAssetID, connectionID)
		}
		if _, exists := assets[relationship.TargetAssetID]; !exists {
			return fmt.Errorf("contributor relationship target %q is outside connection %q", relationship.TargetAssetID, connectionID)
		}
	}
	for _, binding := range contribution.Bindings {
		if binding.ControllerAssetID == binding.ManagedAssetID || strings.TrimSpace(binding.EvidenceSource) == "" {
			return fmt.Errorf("contributor returned an invalid lifecycle binding from %q to %q", binding.ControllerAssetID, binding.ManagedAssetID)
		}
		if _, exists := assets[binding.ControllerAssetID]; !exists {
			return fmt.Errorf("lifecycle controller %q is outside connection %q", binding.ControllerAssetID, connectionID)
		}
		if _, exists := assets[binding.ManagedAssetID]; !exists {
			return fmt.Errorf("managed asset %q is outside connection %q", binding.ManagedAssetID, connectionID)
		}
		if binding.Authority != graph.AuthorityAuthoritative && binding.Authority != graph.AuthorityInferred {
			return fmt.Errorf("lifecycle binding has invalid authority %q", binding.Authority)
		}
		if binding.Ownership != graph.OwnershipExclusive && binding.Ownership != graph.OwnershipShared && binding.Ownership != graph.OwnershipReferenced && binding.Ownership != graph.OwnershipUnknown {
			return fmt.Errorf("lifecycle binding has invalid ownership %q", binding.Ownership)
		}
		if binding.CleanupPolicy != graph.CleanupDelegate && binding.CleanupPolicy != graph.CleanupRetain && binding.CleanupPolicy != graph.CleanupDirect && binding.CleanupPolicy != graph.CleanupUnknown {
			return fmt.Errorf("lifecycle binding has invalid cleanup policy %q", binding.CleanupPolicy)
		}
	}
	for _, unresolved := range contribution.Unresolved {
		controller, exists := assets[unresolved.ControllerID]
		if !exists || controller.Identity.ConnectionID != connectionID {
			return fmt.Errorf("unresolved reference controller %q is outside connection %q", unresolved.ControllerID, connectionID)
		}
		if unresolved.Provider != controller.Identity.Provider || unresolved.ConnectionID != connectionID || strings.TrimSpace(unresolved.NativeType) == "" || strings.TrimSpace(unresolved.NativeID) == "" || unresolved.Relationship == "" {
			return fmt.Errorf("contributor returned an invalid unresolved reference for controller %q", unresolved.ControllerID)
		}
	}
	return nil
}

func pathStrings(document map[string]any, path string) ([]string, bool) {
	var current any = document
	for _, segment := range strings.Split(path, ".") {
		object, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		current, ok = object[segment]
		if !ok {
			return nil, false
		}
	}
	switch value := current.(type) {
	case string:
		if value == "" {
			return nil, false
		}
		return []string{value}, true
	case []string:
		return append([]string(nil), value...), len(value) > 0
	case []any:
		result := make([]string, 0, len(value))
		for _, item := range value {
			text, ok := item.(string)
			if !ok || text == "" {
				continue
			}
			result = append(result, text)
		}
		return result, len(result) > 0
	default:
		return nil, false
	}
}

func relationshipID(revision string, sourceID, targetID asset.AssetID, relationshipType graph.RelationshipType, source string) graph.RelationshipID {
	return graph.RelationshipID(idgen.MustNew("rel"))
}

func lifecycleBindingID(revision string, controllerID, managedID asset.AssetID, evidenceSource string) graph.LifecycleBindingID {
	return graph.LifecycleBindingID(idgen.MustNew("lcb"))
}

func cloneAssets(values []asset.Asset) ([]asset.Asset, error) {
	payload, err := json.Marshal(values)
	if err != nil {
		return nil, fmt.Errorf("copy assets for graph contributor: %w", err)
	}
	var result []asset.Asset
	if err := json.Unmarshal(payload, &result); err != nil {
		return nil, fmt.Errorf("copy assets for graph contributor: %w", err)
	}
	return result, nil
}
