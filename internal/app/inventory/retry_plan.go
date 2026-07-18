package inventory

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/persistence"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

type retryShardTemplate struct {
	source        string
	resourceKind  *asset.ResourceKind
	authoritative bool
}

type retryPlanDefinition struct {
	connection      asset.CloudConnection
	regions         []asset.ConnectionRegion
	regionTemplates []retryShardTemplate
	globalTemplates []retryShardTemplate
}

func (s *ControlService) retryPlanDefinition(
	ctx context.Context,
	repositories persistence.Repositories,
	task asset.ScanTask,
) (retryPlanDefinition, error) {
	if s.directory == nil || task.ScopeMode == asset.ScanSelectedNetworks {
		return retryPlanDefinition{}, nil
	}
	connection, err := repositories.Connections().GetConnection(ctx, task.ConnectionID)
	if err != nil {
		return retryPlanDefinition{}, err
	}
	planner := &Creator{
		repositories: repositories,
		directory:    s.directory,
		clock:        s.clock,
		idGenerator:  func() string { return s.newID("shr") },
	}
	descriptor, err := planner.providerDescriptor(connection.Provider)
	if err != nil {
		return retryPlanDefinition{}, err
	}
	sources := append([]contracts.InventorySource(nil), descriptor.InventorySources...)
	sort.Slice(sources, func(i, j int) bool { return sources[i].Name < sources[j].Name })
	kinds, kindSources, err := planner.resolveKinds(connection.Provider, task.ResourceKindIDs, sources)
	if err != nil {
		return retryPlanDefinition{}, err
	}
	definition := retryPlanDefinition{
		connection:      connection,
		regionTemplates: retryTemplates(asset.ScopeRegion, len(task.ResourceKindIDs) == 0, sources, kinds, kindSources),
	}
	if task.ScopeMode == asset.ScanAllActiveRegions || hasGlobalScanTarget(task.Targets) {
		definition.globalTemplates = retryTemplates(asset.ScopeGlobal, len(task.ResourceKindIDs) == 0, sources, kinds, kindSources)
	}
	switch task.ScopeMode {
	case asset.ScanAllActiveRegions:
		definition.regions, err = planner.resolveRegions(ctx, ScanCreationRequest{
			ConnectionID: task.ConnectionID,
			ScopeMode:    asset.ScanAllActiveRegions,
			RegionMode:   RegionModeAllActive,
		})
	case asset.ScanSelectedRegions:
		known, listErr := repositories.Regions().ListRegionsByConnection(ctx, task.ConnectionID)
		if listErr != nil {
			return retryPlanDefinition{}, listErr
		}
		byID := make(map[string]asset.ConnectionRegion, len(known))
		for _, region := range known {
			byID[region.RegionID] = region
		}
		for _, target := range task.Targets {
			if target.Kind != asset.ScanTargetRegion {
				continue
			}
			region, ok := byID[target.RegionID]
			if !ok {
				return retryPlanDefinition{}, fmt.Errorf("scan target region %q no longer exists", target.RegionID)
			}
			definition.regions = append(definition.regions, region)
		}
	default:
		return retryPlanDefinition{}, nil
	}
	if err != nil {
		return retryPlanDefinition{}, err
	}
	return definition, nil
}

func hasGlobalScanTarget(targets []asset.ScanTarget) bool {
	for _, target := range targets {
		if target.Kind == asset.ScanTargetGlobal || strings.TrimSpace(target.RegionID) == globalTargetKey() {
			return true
		}
	}
	return false
}

func retryTemplates(
	scopeKind asset.ScopeKind,
	broad bool,
	sources []contracts.InventorySource,
	kinds []asset.ResourceKind,
	kindSources map[asset.ResourceKindID]contracts.InventorySource,
) []retryShardTemplate {
	result := make([]retryShardTemplate, 0)
	if broad {
		for _, source := range sources {
			if !source.KindSpecific && sourceSupportsScope(source, scopeKind) {
				result = append(result, retryShardTemplate{
					source: source.Name, authoritative: false,
				})
			}
		}
	}
	for index := range kinds {
		kind := &kinds[index]
		source, ok := kindSources[kind.ID]
		if !ok || !kindSupportsScope(*kind, scopeKind) || !sourceSupportsScope(source, scopeKind) {
			continue
		}
		if broad && !source.KindSpecific {
			continue
		}
		result = append(result, retryShardTemplate{
			source: source.Name, resourceKind: kind, authoritative: source.AuthoritativeDefault,
		})
	}
	sort.Slice(result, func(i, j int) bool {
		leftKind, rightKind := "", ""
		if result[i].resourceKind != nil {
			leftKind = string(result[i].resourceKind.ID)
		}
		if result[j].resourceKind != nil {
			rightKind = string(result[j].resourceKind.ID)
		}
		if result[i].source != result[j].source {
			return result[i].source < result[j].source
		}
		return leftKind < rightKind
	})
	return result
}

func (s *ControlService) retryPlanHasGaps(
	ctx context.Context,
	repositories persistence.Repositories,
	task asset.ScanTask,
	shards []asset.ScanShard,
) (bool, error) {
	definition, err := s.retryPlanDefinition(ctx, repositories, task)
	if err != nil {
		return false, err
	}
	if len(definition.regionTemplates) == 0 && len(definition.globalTemplates) == 0 {
		return false, nil
	}
	targetsByRegion := make(map[string]string, len(task.Targets))
	globalKey := ""
	for _, target := range task.Targets {
		switch target.Kind {
		case asset.ScanTargetRegion:
			targetsByRegion[strings.TrimSpace(target.RegionID)] = strings.TrimSpace(target.Key)
		case asset.ScanTargetGlobal:
			globalKey = strings.TrimSpace(target.Key)
		}
	}
	existing := retryShardSignatures(shards)
	for _, region := range definition.regions {
		targetKey := targetsByRegion[strings.TrimSpace(region.RegionID)]
		if targetKey == "" {
			return true, nil
		}
		for _, template := range definition.regionTemplates {
			if _, ok := existing[retryShardSignature(targetKey, template)]; !ok {
				return true, nil
			}
		}
	}
	if len(definition.globalTemplates) > 0 {
		if globalKey == "" {
			return true, nil
		}
		for _, template := range definition.globalTemplates {
			if _, ok := existing[retryShardSignature(globalKey, template)]; !ok {
				return true, nil
			}
		}
	}
	return false, nil
}

func (s *ControlService) desiredRetryShardSignatures(
	ctx context.Context,
	repositories persistence.Repositories,
	task asset.ScanTask,
) (map[string]struct{}, bool, error) {
	if s.directory == nil || task.ScopeMode == asset.ScanSelectedNetworks {
		return nil, false, nil
	}
	definition, err := s.retryPlanDefinition(ctx, repositories, task)
	if err != nil {
		return nil, false, err
	}
	targetsByRegion := make(map[string]string, len(task.Targets))
	globalKey := globalTargetKey()
	for _, target := range task.Targets {
		switch target.Kind {
		case asset.ScanTargetRegion:
			targetsByRegion[strings.TrimSpace(target.RegionID)] = strings.TrimSpace(target.Key)
		case asset.ScanTargetGlobal:
			if key := strings.TrimSpace(target.Key); key != "" {
				globalKey = key
			}
		}
	}
	result := make(map[string]struct{})
	for _, region := range definition.regions {
		regionID := strings.TrimSpace(region.RegionID)
		targetKey := targetsByRegion[regionID]
		if targetKey == "" {
			targetKey = regionTargetKey(regionID)
		}
		for _, template := range definition.regionTemplates {
			result[retryShardSignature(targetKey, template)] = struct{}{}
		}
	}
	for _, template := range definition.globalTemplates {
		result[retryShardSignature(globalKey, template)] = struct{}{}
	}
	return result, true, nil
}

func (s *ControlService) reconcileRetryPlan(
	ctx context.Context,
	repositories persistence.Repositories,
	task *asset.ScanTask,
	existingShards []asset.ScanShard,
	generation int,
	now time.Time,
) ([]asset.ScanShard, error) {
	definition, err := s.retryPlanDefinition(ctx, repositories, *task)
	if err != nil {
		return nil, err
	}
	if len(definition.regionTemplates) == 0 && len(definition.globalTemplates) == 0 {
		return nil, nil
	}
	planner := &Creator{
		repositories: repositories,
		directory:    s.directory,
		clock:        s.clock,
		idGenerator:  func() string { return s.newID("shr") },
	}
	rootPlan, err := planner.accountRoot(ctx, definition.connection, now)
	if err != nil {
		return nil, err
	}
	for _, scope := range rootPlan.upserts {
		if err := repositories.Inventory().PutScope(ctx, scope); err != nil {
			return nil, err
		}
	}
	for _, merge := range rootPlan.merges {
		if err := repositories.Inventory().ConsolidateScopes(
			ctx, merge.canonicalID, merge.duplicateIDs, merge.aliasParentID,
		); err != nil {
			return nil, err
		}
	}

	targetsByRegion := make(map[string]asset.ScanTarget, len(task.Targets))
	var globalTarget asset.ScanTarget
	for _, target := range task.Targets {
		switch target.Kind {
		case asset.ScanTargetRegion:
			targetsByRegion[strings.TrimSpace(target.RegionID)] = target
		case asset.ScanTargetGlobal:
			globalTarget = target
		}
	}
	scopeByTarget := make(map[string]asset.ScopeID, len(existingShards))
	for _, shard := range existingShards {
		if shard.ScopeID != "" && scopeByTarget[shard.TargetKey] == "" {
			scopeByTarget[shard.TargetKey] = shard.ScopeID
		}
	}
	existing := retryShardSignatures(existingShards)
	added := make([]asset.ScanShard, 0)
	persistedKinds := make(map[asset.ResourceKindID]struct{})
	putShard := func(targetKey, regionID string, scopeID asset.ScopeID, template retryShardTemplate) error {
		signature := retryShardSignature(targetKey, template)
		if _, ok := existing[signature]; ok {
			return nil
		}
		kindID := asset.ResourceKindID("")
		if template.resourceKind != nil {
			kindID = template.resourceKind.ID
			if _, persisted := persistedKinds[kindID]; !persisted {
				if err := repositories.Inventory().PutResourceKind(ctx, *template.resourceKind); err != nil {
					return err
				}
				persistedKinds[kindID] = struct{}{}
			}
		}
		shard := planner.shard(
			task.ID, targetKey, definition.connection.Provider, regionID, scopeID,
			template.source, kindID, template.authoritative, now,
		)
		shard.RetryGeneration = generation
		if err := repositories.Inventory().PutScanShard(ctx, shard); err != nil {
			return err
		}
		existing[signature] = struct{}{}
		added = append(added, shard)
		return nil
	}

	for _, region := range definition.regions {
		regionID := strings.TrimSpace(region.RegionID)
		target, ok := targetsByRegion[regionID]
		if !ok {
			target = asset.ScanTarget{
				Key: regionTargetKey(regionID), Kind: asset.ScanTargetRegion,
				RegionID: regionID, RegionName: region.EffectiveName(), Name: region.EffectiveName(),
			}
			task.Targets = append(task.Targets, target)
			targetsByRegion[regionID] = target
		}
		scopeID := scopeByTarget[target.Key]
		if scopeID == "" {
			scope, found := rootPlan.regions[regionID]
			if !found {
				scope = regionScope(definition.connection.ID, rootPlan.root.ID, region, now)
				if existingScope, getErr := repositories.Inventory().GetScopeByNaturalKey(
					ctx, definition.connection.ID, asset.ScopeRegion, regionID,
				); getErr == nil {
					scope = existingScope
				} else if !errors.Is(getErr, persistence.ErrNotFound) {
					return nil, getErr
				}
				if err := repositories.Inventory().PutScope(ctx, scope); err != nil {
					return nil, err
				}
			}
			scopeID = scope.ID
			scopeByTarget[target.Key] = scopeID
		}
		for _, template := range definition.regionTemplates {
			if err := putShard(target.Key, regionID, scopeID, template); err != nil {
				return nil, err
			}
		}
	}

	if len(definition.globalTemplates) > 0 {
		if globalTarget.Key == "" {
			globalTarget = asset.ScanTarget{
				Key: globalTargetKey(), Kind: asset.ScanTargetGlobal,
				RegionID: "global", RegionName: "Global", Name: "Global",
			}
			task.Targets = append(task.Targets, globalTarget)
		}
		scopeID := scopeByTarget[globalTarget.Key]
		if scopeID == "" {
			endpointRegion := ""
			if len(definition.regions) > 0 {
				endpointRegion = definition.regions[0].RegionID
			}
			scope := globalScope(definition.connection.ID, rootPlan.root, endpointRegion, now)
			if existingScope, getErr := repositories.Inventory().GetScopeByNaturalKey(
				ctx, definition.connection.ID, asset.ScopeGlobal, scope.NativeID,
			); getErr == nil {
				scope = existingScope
			} else if !errors.Is(getErr, persistence.ErrNotFound) {
				return nil, getErr
			}
			if err := repositories.Inventory().PutScope(ctx, scope); err != nil {
				return nil, err
			}
			scopeID = scope.ID
			scopeByTarget[globalTarget.Key] = scopeID
		}
		for _, template := range definition.globalTemplates {
			if err := putShard(globalTarget.Key, "global", scopeID, template); err != nil {
				return nil, err
			}
		}
	}
	sortScanTargets(task.Targets)
	return added, nil
}

func retryShardSignatures(shards []asset.ScanShard) map[string]struct{} {
	result := make(map[string]struct{}, len(shards))
	for _, shard := range shards {
		result[retryScanShardSignature(shard)] = struct{}{}
	}
	return result
}

func retryScanShardSignature(shard asset.ScanShard) string {
	return strings.Join([]string{
		strings.TrimSpace(shard.TargetKey),
		strings.TrimSpace(shard.Source),
		string(shard.ResourceKindID),
	}, "\x00")
}

func retryShardSignature(targetKey string, template retryShardTemplate) string {
	kindID := ""
	if template.resourceKind != nil {
		kindID = string(template.resourceKind.ID)
	}
	return strings.Join([]string{
		strings.TrimSpace(targetKey),
		strings.TrimSpace(template.source),
		kindID,
	}, "\x00")
}
