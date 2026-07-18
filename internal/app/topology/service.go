package topology

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"sort"
	"strings"
	"time"

	"github.com/loomx-ai/steward/internal/app/scancoverage"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
	"github.com/loomx-ai/steward/internal/core/resourcequery"
	core "github.com/loomx-ai/steward/internal/core/topology"
	"github.com/loomx-ai/steward/internal/persistence"
	"github.com/loomx-ai/steward/internal/provider/contracts"
	"github.com/loomx-ai/steward/internal/provider/spec"
)

type BundleCatalog interface {
	Bundles() []spec.Bundle
	ProviderDescriptors() []contracts.ProviderDescriptor
}

type ResourceKindCatalog interface {
	ResourceKinds(asset.Provider) ([]asset.ResourceKind, string, bool)
}

type Query struct {
	ConnectionID    asset.ConnectionID
	FocusKey        string
	Cursor          string
	Limit           int
	ResourceClass   string
	ResourceKindIDs []asset.ResourceKindID
	Risk            string
	ResourceQuery   string
	resourceFilter  *resourcequery.Expression
}

type QueryError struct {
	Code    string
	Message string
	Details map[string]any
}

func (e *QueryError) Error() string { return e.Message }

func IsQueryError(err error, code string) bool {
	var queryError *QueryError
	return errors.As(err, &queryError) && queryError.Code == code
}

type Option func(*Service)

type Service struct {
	repositories persistence.Repositories
	bundles      BundleCatalog
	clock        func() time.Time
}

func NewService(repositories persistence.Repositories, bundles BundleCatalog, options ...Option) *Service {
	service := &Service{
		repositories: repositories,
		bundles:      bundles,
		clock:        func() time.Time { return time.Now().UTC() },
	}
	for _, option := range options {
		option(service)
	}
	return service
}

func WithClock(clock func() time.Time) Option {
	return func(service *Service) {
		if clock != nil {
			service.clock = clock
		}
	}
}

func (s *Service) Query(ctx context.Context, query Query) (core.Response, error) {
	query.ResourceKindIDs = normalizedResourceKindIDs(query.ResourceKindIDs)
	resourceFilter, err := resourcequery.Parse(query.ResourceQuery)
	if err != nil {
		return core.Response{}, resourceQueryFailure(err)
	}
	query.ResourceQuery = strings.TrimSpace(query.ResourceQuery)
	query.resourceFilter = resourceFilter
	if query.Limit == 0 {
		query.Limit = 200
	}
	if query.Limit < 1 || query.Limit > core.MaxResourceLimit {
		return core.Response{}, queryFailure(
			"topology.limit_invalid",
			"topology limit is outside the allowed range",
			map[string]any{"maximum": core.MaxResourceLimit},
		)
	}
	if query.Risk != "" && query.Risk != "actionable" && query.Risk != "findings" && query.Risk != "blockers" {
		return core.Response{}, queryFailure(
			"topology.risk_invalid",
			"topology risk filter is not supported",
			map[string]any{"risk": query.Risk, "supported": []string{"actionable", "findings", "blockers"}},
		)
	}
	focus := core.Focus{Kind: core.FocusAccount}
	if query.FocusKey != "" {
		parsed, err := core.ParseFocusKey(query.FocusKey)
		if err != nil {
			return core.Response{}, queryFailure("topology.focus_invalid", "topology focus key is invalid", nil)
		}
		focus = parsed
	}
	if s == nil || s.repositories == nil || s.bundles == nil {
		return core.Response{}, fmt.Errorf("topology repositories and bundle catalog are required")
	}

	input, err := s.loadInput(ctx, query, focus)
	if err != nil {
		return core.Response{}, err
	}
	revision := revisionKey(input, query)
	if query.Cursor != "" {
		cursor, decodeErr := decodeCursor(query.Cursor)
		if decodeErr != nil {
			return core.Response{}, queryFailure("topology.cursor_invalid", "topology cursor is invalid", nil)
		}
		if cursor.Revision != revision || cursor.FocusKey != query.FocusKey {
			return core.Response{}, queryFailure("topology.cursor_stale", "topology cursor belongs to another revision or focus", nil)
		}
		input.Cursor = cursor.Inner
	}
	response, err := core.Project(input)
	if err != nil {
		if strings.Contains(err.Error(), "cursor") {
			return core.Response{}, queryFailure("topology.cursor_stale", "topology cursor belongs to an older revision", nil)
		}
		return core.Response{}, queryFailure("topology.query_invalid", err.Error(), nil)
	}
	if response.NextCursor != "" {
		response.NextCursor = encodeCursor(cursorEnvelope{
			Revision: revision,
			FocusKey: query.FocusKey,
			Inner:    response.NextCursor,
		})
	}
	return response, nil
}

func (s *Service) loadInput(ctx context.Context, query Query, focus core.Focus) (core.Input, error) {
	connection, err := s.repositories.Connections().GetConnection(ctx, query.ConnectionID)
	if err != nil {
		return core.Input{}, err
	}
	regions, err := s.repositories.Regions().ListRegionsByConnection(ctx, query.ConnectionID)
	if err != nil {
		return core.Input{}, err
	}
	scopes, err := s.repositories.Inventory().ListScopesByConnection(ctx, query.ConnectionID)
	if err != nil {
		return core.Input{}, err
	}
	kinds, bundleRevision, allowedKindIDs, constrained := providerCatalogProjection(
		s.bundles,
		connection.Provider,
	)
	if query.resourceFilter != nil {
		queryKinds := make([]asset.ResourceKind, 0, len(kinds))
		for _, kind := range kinds {
			queryKinds = append(queryKinds, kind)
		}
		if err := query.resourceFilter.Validate(queryKinds); err != nil {
			return core.Input{}, resourceQueryFailure(err)
		}
	}
	for _, kindID := range query.ResourceKindIDs {
		if !kindExists(kinds, kindID) {
			return core.Input{}, queryFailure(
				"topology.resource_kind_invalid",
				"topology resource kind is not supported by this connection",
				map[string]any{"resource_kind_id": kindID},
			)
		}
	}
	if focus.Kind == core.FocusAccount {
		return s.loadAccountInput(
			ctx,
			query,
			focus,
			connection,
			regions,
			scopes,
			kinds,
			bundleRevision,
			allowedKindIDs,
			constrained,
		)
	}
	focusScopeIDs := scopeIDsForFocus(scopes, query.ConnectionID, focus)
	assets, err := s.repositories.Inventory().ListActiveAssetsByScopes(
		ctx,
		query.ConnectionID,
		focusScopeIDs,
		"",
	)
	if err != nil {
		return core.Input{}, err
	}
	if constrained {
		assets = filterAssetsByKinds(assets, kinds)
	}
	if focus.Kind == core.FocusAccountGlobal {
		assets, err = s.loadMemberOfDescendants(
			ctx,
			query.ConnectionID,
			assets,
			kinds,
			constrained,
		)
		if err != nil {
			return core.Input{}, err
		}
	}
	if query.resourceFilter != nil {
		assets = filterAssetsByResourceQuery(assets, query.resourceFilter)
	}
	projectedAssetCount := len(assets)
	assetIDs := make([]asset.AssetID, len(assets))
	for index, value := range assets {
		assetIDs[index] = value.ID
	}
	relationships, err := s.repositories.Graph().ListRelationshipsByAssetIDs(
		ctx,
		assetIDs,
	)
	if err != nil {
		return core.Input{}, err
	}
	bindings, err := s.repositories.Graph().ListLifecycleBindingsByAssetIDs(
		ctx,
		assetIDs,
	)
	if err != nil {
		return core.Input{}, err
	}
	relatedAssetIDs := relatedEndpointIDs(assetIDs, relationships, bindings)
	if len(relatedAssetIDs) > 0 {
		relatedAssets, relatedErr := s.repositories.Inventory().ListAssetsByIDs(ctx, relatedAssetIDs)
		if relatedErr != nil {
			return core.Input{}, relatedErr
		}
		for _, related := range relatedAssets {
			if related.Identity.ConnectionID == query.ConnectionID &&
				(!constrained || kindExists(kinds, related.ResourceKindID)) {
				assets = append(assets, related)
			}
		}
	}
	visibleAssetIDs := make(map[asset.AssetID]struct{}, len(assets))
	for _, value := range assets {
		visibleAssetIDs[value.ID] = struct{}{}
	}
	relationships = filterRelationshipsByAssets(relationships, visibleAssetIDs)
	bindings = filterBindingsByAssets(bindings, visibleAssetIDs)
	graphRevisions, err := s.repositories.Graph().ListGraphRevisionsByConnection(ctx, query.ConnectionID)
	if err != nil {
		return core.Input{}, err
	}
	counts := map[asset.AssetID]int{}
	if focus.Kind != core.FocusRegion || query.Risk == "findings" {
		counts, err = s.findingCounts(ctx, assets[:projectedAssetCount])
		if err != nil {
			return core.Input{}, err
		}
	}
	requirement := scancoverage.ActiveRegionRequirement(regions, query.ConnectionID)
	global, provable := contracts.ProviderSupportsRootScope(
		s.bundles.ProviderDescriptors(), connection.Provider, asset.ScopeGlobal,
	)
	requirement.Global = global
	if !provable {
		requirement.Unprovable = true
	}
	coverage, err := s.coverage(ctx, query.ConnectionID, requirement)
	if err != nil {
		return core.Input{}, err
	}
	return core.Input{
		Focus: focus, Connection: connection, Regions: regions, Scopes: scopes, Assets: assets,
		Kinds: kinds, Relationships: relationships, LifecycleBindings: bindings, FindingCounts: counts,
		ResourceClass: query.ResourceClass, ResourceKindIDs: query.ResourceKindIDs,
		ResourceQueryApplied: query.resourceFilter != nil,
		Risk:                 query.Risk, Limit: query.Limit,
		Revision: core.Revision{
			Inventory: inventoryRevision(regions, scopes, assets), Graph: graphRevision(graphRevisions),
			SpecBundle: bundleRevision, ProjectedAt: s.clock(),
		},
		Coverage: coverage,
	}, nil
}

func (s *Service) loadMemberOfDescendants(
	ctx context.Context,
	connectionID asset.ConnectionID,
	roots []asset.Asset,
	kinds map[asset.ResourceKindID]asset.ResourceKind,
	constrained bool,
) ([]asset.Asset, error) {
	result := append([]asset.Asset(nil), roots...)
	known := make(map[asset.AssetID]struct{}, len(result))
	frontier := make([]asset.AssetID, 0, len(result))
	for _, value := range result {
		if value.ID == "" {
			continue
		}
		known[value.ID] = struct{}{}
		frontier = append(frontier, value.ID)
	}
	for len(frontier) > 0 {
		relationships, err := s.repositories.Graph().ListRelationshipsByAssetIDs(
			ctx,
			frontier,
		)
		if err != nil {
			return nil, err
		}
		descendantIDs := make([]asset.AssetID, 0)
		queued := make(map[asset.AssetID]struct{})
		for _, relationship := range relationships {
			if relationship.ClosedAt != nil ||
				relationship.Type != graph.RelationshipMemberOf {
				continue
			}
			if _, parentKnown := known[relationship.TargetAssetID]; !parentKnown {
				continue
			}
			childID := relationship.SourceAssetID
			if childID == "" {
				continue
			}
			if _, childKnown := known[childID]; childKnown {
				continue
			}
			if _, alreadyQueued := queued[childID]; alreadyQueued {
				continue
			}
			queued[childID] = struct{}{}
			descendantIDs = append(descendantIDs, childID)
		}
		if len(descendantIDs) == 0 {
			break
		}
		sort.Slice(descendantIDs, func(left, right int) bool {
			return descendantIDs[left] < descendantIDs[right]
		})
		descendants, err := s.repositories.Inventory().ListAssetsByIDs(
			ctx,
			descendantIDs,
		)
		if err != nil {
			return nil, err
		}
		frontier = frontier[:0]
		for _, descendant := range descendants {
			if descendant.ID == "" ||
				descendant.ClosedAt != nil ||
				descendant.Identity.ConnectionID != connectionID ||
				(constrained && !kindExists(kinds, descendant.ResourceKindID)) {
				continue
			}
			if _, exists := known[descendant.ID]; exists {
				continue
			}
			known[descendant.ID] = struct{}{}
			result = append(result, descendant)
			frontier = append(frontier, descendant.ID)
		}
	}
	return result, nil
}

func relatedEndpointIDs(
	focused []asset.AssetID,
	relationships []graph.Relationship,
	bindings []graph.LifecycleBinding,
) []asset.AssetID {
	known := make(map[asset.AssetID]struct{}, len(focused))
	for _, id := range focused {
		known[id] = struct{}{}
	}
	result := make([]asset.AssetID, 0)
	add := func(id asset.AssetID) {
		if id == "" {
			return
		}
		if _, ok := known[id]; ok {
			return
		}
		known[id] = struct{}{}
		result = append(result, id)
	}
	for _, relationship := range relationships {
		add(relationship.SourceAssetID)
		add(relationship.TargetAssetID)
	}
	for _, binding := range bindings {
		add(binding.ControllerAssetID)
		add(binding.ManagedAssetID)
	}
	return result
}

func (s *Service) loadAccountInput(
	ctx context.Context,
	query Query,
	focus core.Focus,
	connection asset.CloudConnection,
	regions []asset.ConnectionRegion,
	scopes []asset.Scope,
	kinds map[asset.ResourceKindID]asset.ResourceKind,
	bundleRevision string,
	allowedKindIDs []asset.ResourceKindID,
	constrained bool,
) (core.Input, error) {
	countKindIDs := topologyCountKindIDs(query, kinds, allowedKindIDs, constrained)
	counts, err := s.accountAssetCounts(ctx, query, kinds, countKindIDs, constrained)
	if err != nil {
		return core.Input{}, err
	}
	graphRevisions, err := s.repositories.Graph().ListGraphRevisionsByConnection(
		ctx,
		query.ConnectionID,
	)
	if err != nil {
		return core.Input{}, err
	}
	requirement := scancoverage.ActiveRegionRequirement(
		regions,
		query.ConnectionID,
	)
	global, provable := contracts.ProviderSupportsRootScope(
		s.bundles.ProviderDescriptors(),
		connection.Provider,
		asset.ScopeGlobal,
	)
	requirement.Global = global
	if !provable {
		requirement.Unprovable = true
	}
	coverage, err := s.coverage(ctx, query.ConnectionID, requirement)
	if err != nil {
		return core.Input{}, err
	}
	return core.Input{
		Focus:                focus,
		Connection:           connection,
		Regions:              regions,
		Scopes:               scopes,
		AssetCountsByScope:   counts,
		Kinds:                kinds,
		ResourceClass:        query.ResourceClass,
		ResourceKindIDs:      query.ResourceKindIDs,
		ResourceQueryApplied: query.resourceFilter != nil,
		Risk:                 query.Risk,
		Limit:                query.Limit,
		Revision: core.Revision{
			Inventory:   inventorySummaryRevision(regions, scopes, counts),
			Graph:       graphRevision(graphRevisions),
			SpecBundle:  bundleRevision,
			ProjectedAt: s.clock(),
		},
		Coverage: coverage,
	}, nil
}

func (s *Service) accountAssetCounts(
	ctx context.Context,
	query Query,
	kinds map[asset.ResourceKindID]asset.ResourceKind,
	countKindIDs []asset.ResourceKindID,
	constrained bool,
) (map[asset.ScopeID]int, error) {
	if query.resourceFilter == nil {
		return s.repositories.Inventory().CountActiveAssetsByScope(
			ctx,
			query.ConnectionID,
			countKindIDs,
		)
	}
	assets, err := s.repositories.Inventory().ListActiveAssetsByConnection(
		ctx,
		query.ConnectionID,
		"",
	)
	if err != nil {
		return nil, err
	}
	allowedKinds := make(map[asset.ResourceKindID]struct{}, len(countKindIDs))
	for _, kindID := range countKindIDs {
		allowedKinds[kindID] = struct{}{}
	}
	counts := make(map[asset.ScopeID]int)
	for _, value := range assets {
		if constrained && !kindExists(kinds, value.ResourceKindID) {
			continue
		}
		if len(allowedKinds) > 0 {
			if _, allowed := allowedKinds[value.ResourceKindID]; !allowed {
				continue
			}
		}
		if query.resourceFilter.Match(value) {
			counts[value.ScopeID]++
		}
	}
	return counts, nil
}

func filterAssetsByResourceQuery(
	values []asset.Asset,
	expression *resourcequery.Expression,
) []asset.Asset {
	result := make([]asset.Asset, 0, len(values))
	for _, value := range values {
		if expression.Match(value) {
			result = append(result, value)
		}
	}
	return result
}

func filterAssetsByKinds(
	values []asset.Asset,
	kinds map[asset.ResourceKindID]asset.ResourceKind,
) []asset.Asset {
	result := make([]asset.Asset, 0, len(values))
	for _, value := range values {
		if kindExists(kinds, value.ResourceKindID) {
			result = append(result, value)
		}
	}
	return result
}

func kindExists(
	kinds map[asset.ResourceKindID]asset.ResourceKind,
	kindID asset.ResourceKindID,
) bool {
	_, exists := kinds[kindID]
	return exists
}

func normalizedResourceKindIDs(values []asset.ResourceKindID) []asset.ResourceKindID {
	result := make([]asset.ResourceKindID, 0, len(values))
	seen := make(map[asset.ResourceKindID]struct{}, len(values))
	for _, value := range values {
		value = asset.ResourceKindID(strings.TrimSpace(string(value)))
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result
}

func topologyCountKindIDs(
	query Query,
	kinds map[asset.ResourceKindID]asset.ResourceKind,
	allowedKindIDs []asset.ResourceKindID,
	constrained bool,
) []asset.ResourceKindID {
	if len(query.ResourceKindIDs) > 0 {
		return append([]asset.ResourceKindID(nil), query.ResourceKindIDs...)
	}
	if query.ResourceClass != "" {
		result := make([]asset.ResourceKindID, 0)
		for kindID, kind := range kinds {
			if kind.Class == query.ResourceClass {
				result = append(result, kindID)
			}
		}
		sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
		return result
	}
	if constrained {
		return allowedKindIDs
	}
	return nil
}

func filterRelationshipsByAssets(
	values []graph.Relationship,
	visible map[asset.AssetID]struct{},
) []graph.Relationship {
	result := make([]graph.Relationship, 0, len(values))
	for _, value := range values {
		_, sourceVisible := visible[value.SourceAssetID]
		_, targetVisible := visible[value.TargetAssetID]
		if sourceVisible && targetVisible {
			result = append(result, value)
		}
	}
	return result
}

func filterBindingsByAssets(
	values []graph.LifecycleBinding,
	visible map[asset.AssetID]struct{},
) []graph.LifecycleBinding {
	result := make([]graph.LifecycleBinding, 0, len(values))
	for _, value := range values {
		_, controllerVisible := visible[value.ControllerAssetID]
		_, managedVisible := visible[value.ManagedAssetID]
		if controllerVisible && managedVisible {
			result = append(result, value)
		}
	}
	return result
}

func scopeIDsForFocus(
	scopes []asset.Scope,
	connectionID asset.ConnectionID,
	focus core.Focus,
) []asset.ScopeID {
	wantedKind := asset.ScopeRegion
	wantedNativeID := focus.RegionID
	if focus.Kind == core.FocusAccountGlobal {
		wantedKind = asset.ScopeGlobal
		wantedNativeID = ""
	}
	allowed := make(map[asset.ScopeID]bool)
	for _, scope := range scopes {
		if scope.ConnectionID != connectionID || scope.Kind != wantedKind {
			continue
		}
		if wantedKind == asset.ScopeRegion &&
			scope.NativeID != wantedNativeID &&
			scope.Location != wantedNativeID {
			continue
		}
		allowed[scope.ID] = true
	}
	for changed := true; changed; {
		changed = false
		for _, scope := range scopes {
			if scope.ConnectionID == connectionID &&
				!allowed[scope.ID] &&
				allowed[scope.ParentID] {
				allowed[scope.ID] = true
				changed = true
			}
		}
	}
	result := make([]asset.ScopeID, 0, len(allowed))
	for scopeID := range allowed {
		result = append(result, scopeID)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result
}

func (s *Service) findingCounts(ctx context.Context, assets []asset.Asset) (map[asset.AssetID]int, error) {
	if len(assets) == 0 {
		return map[asset.AssetID]int{}, nil
	}
	ids := make([]asset.AssetID, 0, len(assets))
	for _, value := range assets {
		ids = append(ids, value.ID)
	}
	return s.repositories.Findings().CountOpenFindingsByAssetIDs(ctx, ids)
}

func (s *Service) coverage(
	ctx context.Context,
	connectionID asset.ConnectionID,
	requirement scancoverage.Requirement,
) (core.Coverage, error) {
	summary, err := scancoverage.EvaluateConnection(
		ctx, s.repositories.Inventory(), connectionID, requirement,
	)
	if err != nil {
		return core.Coverage{}, err
	}
	return core.Coverage{
		Status: summary.Status, FailedShards: summary.FailedShards,
		LastCompleteScanAt: summary.LastCompleteScanAt,
	}, nil
}

func catalogProjection(bundles []spec.Bundle, provider asset.Provider) (map[asset.ResourceKindID]asset.ResourceKind, string) {
	kinds := make(map[asset.ResourceKindID]asset.ResourceKind)
	revisions := []string{}
	for _, bundle := range bundles {
		if bundle.Provider != provider {
			continue
		}
		revisions = append(revisions, bundle.Revision)
		for _, compiled := range bundle.Specs {
			kind := compiled.ResourceKind
			kind.FieldDisplayNames = cloneLocalizedFields(kind.FieldDisplayNames)
			kind.Properties = asset.CloneResourceProperties(kind.Properties)
			kinds[kind.ID] = kind
		}
	}
	sort.Strings(revisions)
	if len(revisions) == 1 {
		return kinds, revisions[0]
	}
	return kinds, digestStrings(revisions)
}

func providerCatalogProjection(
	catalog BundleCatalog,
	provider asset.Provider,
) (
	map[asset.ResourceKindID]asset.ResourceKind,
	string,
	[]asset.ResourceKindID,
	bool,
) {
	bundleKinds, bundleRevision := catalogProjection(catalog.Bundles(), provider)
	metadata, ok := catalog.(ResourceKindCatalog)
	if !ok {
		return bundleKinds, bundleRevision, nil, false
	}
	providerKinds, metadataRevision, constrained := metadata.ResourceKinds(provider)
	if !constrained {
		return bundleKinds, bundleRevision, nil, false
	}
	revision := digestStrings([]string{bundleRevision, metadataRevision})
	kinds := make(map[asset.ResourceKindID]asset.ResourceKind, len(providerKinds))
	allowed := make([]asset.ResourceKindID, 0, len(providerKinds))
	for _, metadataKind := range providerKinds {
		kind := metadataKind
		kind.FieldDisplayNames = cloneLocalizedFields(kind.FieldDisplayNames)
		kind.Properties = asset.CloneResourceProperties(kind.Properties)
		if compiled, exists := bundleKinds[metadataKind.ID]; exists {
			kind = compiled
			kind.Icon = metadataKind.Icon
			if strings.TrimSpace(metadataKind.DisplayName) != "" {
				kind.DisplayName = metadataKind.DisplayName
			}
			if len(metadataKind.DisplayNames) > 0 {
				kind.DisplayNames = maps.Clone(metadataKind.DisplayNames)
			}
		}
		kind.BundleRevision = revision
		kinds[kind.ID] = kind
		allowed = append(allowed, kind.ID)
	}
	sort.Slice(allowed, func(left, right int) bool {
		return allowed[left] < allowed[right]
	})
	return kinds, revision, allowed, true
}

func cloneLocalizedFields(values map[string]map[string]string) map[string]map[string]string {
	if values == nil {
		return nil
	}
	cloned := make(map[string]map[string]string, len(values))
	for field, labels := range values {
		cloned[field] = maps.Clone(labels)
	}
	return cloned
}

func inventoryRevision(regions []asset.ConnectionRegion, scopes []asset.Scope, assets []asset.Asset) string {
	values := make([]string, 0, len(regions)+len(scopes)+len(assets))
	for _, region := range regions {
		values = append(values, "region:"+region.ID+":"+region.RegionID+":"+string(region.Lifecycle)+":"+fmt.Sprint(region.Revision))
	}
	for _, scope := range scopes {
		values = append(values, "scope:"+string(scope.ID)+":"+scope.UpdatedAt.UTC().Format(time.RFC3339Nano))
	}
	for _, value := range assets {
		values = append(values, "asset:"+string(value.ID)+":"+string(value.CurrentObservationID)+":"+value.LastSeenAt.UTC().Format(time.RFC3339Nano))
	}
	sort.Strings(values)
	return digestStrings(values)
}

func inventorySummaryRevision(
	regions []asset.ConnectionRegion,
	scopes []asset.Scope,
	counts map[asset.ScopeID]int,
) string {
	values := make([]string, 0, len(regions)+len(scopes)+len(counts))
	for _, region := range regions {
		values = append(values, "region:"+region.ID+":"+region.RegionID+":"+string(region.Lifecycle)+":"+fmt.Sprint(region.Revision))
	}
	for _, scope := range scopes {
		values = append(values, "scope:"+string(scope.ID)+":"+scope.UpdatedAt.UTC().Format(time.RFC3339Nano))
	}
	for scopeID, count := range counts {
		values = append(values, "count:"+string(scopeID)+":"+fmt.Sprint(count))
	}
	sort.Strings(values)
	return digestStrings(values)
}

func graphRevision(revisions map[asset.ScopeID]string) string {
	values := make([]string, 0, len(revisions))
	for scopeID, revision := range revisions {
		values = append(values, string(scopeID)+":"+revision)
	}
	sort.Strings(values)
	return digestStrings(values)
}

func revisionKey(input core.Input, query Query) string {
	findingValues := make([]string, 0, len(input.FindingCounts))
	for assetID, count := range input.FindingCounts {
		findingValues = append(findingValues, string(assetID)+":"+fmt.Sprint(count))
	}
	sort.Strings(findingValues)
	lastComplete := ""
	if input.Coverage.LastCompleteScanAt != nil {
		lastComplete = input.Coverage.LastCompleteScanAt.UTC().Format(time.RFC3339Nano)
	}
	return digestStrings([]string{
		input.Revision.Inventory,
		input.Revision.Graph,
		input.Revision.SpecBundle,
		digestStrings(findingValues),
		input.Coverage.Status,
		fmt.Sprint(input.Coverage.FailedShards),
		lastComplete,
		string(query.ConnectionID),
		query.ResourceClass,
		digestResourceKindIDs(query.ResourceKindIDs),
		query.Risk,
		query.ResourceQuery,
		fmt.Sprint(query.Limit),
	})
}

func digestResourceKindIDs(values []asset.ResourceKindID) string {
	normalized := normalizedResourceKindIDs(values)
	parts := make([]string, len(normalized))
	for index, value := range normalized {
		parts[index] = string(value)
	}
	return digestStrings(parts)
}

func digestStrings(values []string) string {
	digest := sha256.Sum256([]byte(strings.Join(values, "\x00")))
	return hex.EncodeToString(digest[:])
}

type cursorEnvelope struct {
	Revision string `json:"revision"`
	FocusKey string `json:"focus_key"`
	Inner    string `json:"inner"`
}

func encodeCursor(value cursorEnvelope) string {
	payload, _ := json.Marshal(value)
	return base64.RawURLEncoding.EncodeToString(payload)
}

func decodeCursor(value string) (cursorEnvelope, error) {
	payload, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return cursorEnvelope{}, err
	}
	var result cursorEnvelope
	if err := json.Unmarshal(payload, &result); err != nil || result.Revision == "" || result.Inner == "" {
		return cursorEnvelope{}, fmt.Errorf("invalid topology cursor")
	}
	return result, nil
}

func queryFailure(code, message string, details map[string]any) error {
	return &QueryError{Code: code, Message: message, Details: details}
}

func resourceQueryFailure(err error) error {
	details := map[string]any{}
	var parseError *resourcequery.ParseError
	if errors.As(err, &parseError) {
		details["position"] = parseError.Position
	}
	var validationError *resourcequery.ValidationError
	if errors.As(err, &validationError) {
		details["position"] = validationError.Position
	}
	return queryFailure("topology.resource_query_invalid", err.Error(), details)
}
