package inventory

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	connectionapp "github.com/loomx-ai/steward/internal/app/connection"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/idgen"
	"github.com/loomx-ai/steward/internal/persistence"
	"github.com/loomx-ai/steward/internal/provider/contracts"
	"github.com/loomx-ai/steward/internal/provider/spec"
)

type RegionMode string

const (
	RegionModeAllActive RegionMode = "all_active"
	RegionModeSelected  RegionMode = "selected"
)

type ScanCreationRequest struct {
	ConnectionID    asset.ConnectionID
	RequestedBy     string
	ScopeMode       asset.ScanScopeMode
	RegionMode      RegionMode
	RegionIDs       []string
	NetworkTargets  []NetworkTargetRequest
	ResourceKindIDs []asset.ResourceKindID
}

type NetworkTargetRequest struct {
	Kind           asset.ScanTargetKind `json:"kind"`
	RegionID       string               `json:"region_id"`
	NativeID       string               `json:"native_id"`
	Name           string               `json:"name,omitempty"`
	ParentNativeID string               `json:"parent_native_id,omitempty"`
}

type ScanCreation struct {
	ScanRun asset.ScanRun     `json:"scan_run"`
	Shards  []asset.ScanShard `json:"scan_shards"`
	Jobs    []execution.Job   `json:"jobs"`
}

type ScanRequestError struct {
	Code    string
	Message string
	Details map[string]any
}

func (e *ScanRequestError) Error() string { return e.Message }

type ScanDirectory interface {
	ProviderDescriptors() []contracts.ProviderDescriptor
	Bundle(asset.Provider) (spec.Bundle, error)
}

type networkTargetDirectory interface {
	ResolveNetworkTargetDiscoverer(asset.Provider) (contracts.NetworkTargetDiscoverer, error)
}

type inventorySourceDirectory interface {
	ResolveInventorySource(asset.Provider, asset.ResourceKind, string) (string, error)
}

type CreatorOption func(*Creator)

type Creator struct {
	repositories persistence.Repositories
	directory    ScanDirectory
	clock        func() time.Time
	idGenerator  func() string
}

func NewCreator(repositories persistence.Repositories, directory ScanDirectory, options ...CreatorOption) (*Creator, error) {
	if repositories == nil || directory == nil {
		return nil, fmt.Errorf("scan creator requires repositories and provider directory")
	}
	creator := &Creator{repositories: repositories, directory: directory, clock: func() time.Time { return time.Now().UTC() }}
	for _, option := range options {
		option(creator)
	}
	return creator, nil
}

func (c *Creator) entityID(prefix string) string {
	if c.idGenerator != nil {
		return c.idGenerator()
	}
	return idgen.MustNew(prefix)
}

func WithCreatorClock(clock func() time.Time) CreatorOption {
	return func(creator *Creator) { creator.clock = clock }
}

func WithCreatorIDGenerator(generator func() string) CreatorOption {
	return func(creator *Creator) { creator.idGenerator = generator }
}

func (c *Creator) Create(ctx context.Context, request ScanCreationRequest) (ScanCreation, error) {
	if request.ConnectionID == "" || strings.TrimSpace(request.RequestedBy) == "" {
		return ScanCreation{}, scanRequestError("scan.request_invalid", "scan connection and requester are required", nil)
	}
	connection, err := c.repositories.Connections().GetConnection(ctx, request.ConnectionID)
	if err != nil {
		return ScanCreation{}, err
	}
	if connection.Status == asset.ConnectionDeleted {
		return ScanCreation{}, scanRequestError("scan.connection_inactive", "the selected connection is inactive", nil)
	}
	if connection.Status != asset.ConnectionActive {
		return ScanCreation{}, scanRequestError("scan.connection_not_validated", "the selected connection has not passed validation", nil)
	}
	scopeMode, networkTargets, err := normalizedScope(request)
	if err != nil {
		return ScanCreation{}, err
	}
	request.ScopeMode = scopeMode
	switch scopeMode {
	case asset.ScanAllActiveRegions:
		request.RegionMode = RegionModeAllActive
	case asset.ScanSelectedRegions:
		request.RegionMode = RegionModeSelected
	case asset.ScanSelectedNetworks:
		request.RegionMode = RegionModeSelected
		request.RegionIDs = networkRegionIDs(networkTargets)
	}
	regions, err := c.resolveRegions(ctx, request)
	if err != nil {
		return ScanCreation{}, err
	}
	globalSelected := scopeMode == asset.ScanAllActiveRegions ||
		(scopeMode == asset.ScanSelectedRegions && containsRegionID(request.RegionIDs, globalTargetKey()))
	globalEndpointRegion := ""
	if globalSelected {
		if len(regions) > 0 {
			globalEndpointRegion = regions[0].RegionID
		} else {
			endpointRegions, endpointErr := c.resolveRegions(ctx, ScanCreationRequest{
				ConnectionID: request.ConnectionID,
				ScopeMode:    asset.ScanAllActiveRegions,
				RegionMode:   RegionModeAllActive,
			})
			if endpointErr != nil {
				return ScanCreation{}, endpointErr
			}
			globalEndpointRegion = endpointRegions[0].RegionID
		}
	}
	if scopeMode == asset.ScanSelectedNetworks {
		networkTargets, err = c.validateNetworkTargets(ctx, connection, networkTargets)
		if err != nil {
			return ScanCreation{}, err
		}
	}
	descriptor, err := c.providerDescriptor(connection.Provider)
	if err != nil {
		return ScanCreation{}, err
	}
	sources := append([]contracts.InventorySource(nil), descriptor.InventorySources...)
	sort.Slice(sources, func(i, j int) bool { return sources[i].Name < sources[j].Name })
	if len(sources) == 0 {
		return ScanCreation{}, fmt.Errorf("provider %q has no inventory source", connection.Provider)
	}
	now := c.clock()
	rootPlan, err := c.accountRoot(ctx, connection, now)
	if err != nil {
		return ScanCreation{}, err
	}
	root := rootPlan.root
	kinds, kindSources, err := c.resolveKinds(connection.Provider, request.ResourceKindIDs, sources)
	if err != nil {
		return ScanCreation{}, err
	}
	broadScan := len(request.ResourceKindIDs) == 0

	run := asset.ScanRun{
		ID: asset.ScanRunID(c.entityID("scn")), ConnectionID: connection.ID, Status: asset.ScanPending,
		ScopeMode: scopeMode, RequestedBy: strings.TrimSpace(request.RequestedBy), CreatedAt: now,
		Targets: make([]asset.ScanTarget, 0, len(regions)+1), ResourceKindIDs: append([]asset.ResourceKindID(nil), request.ResourceKindIDs...),
	}
	shards := make([]asset.ScanShard, 0)
	jobs := make([]execution.Job, 0)
	scopes := append(make([]asset.Scope, 0, len(rootPlan.upserts)+len(regions)+1), rootPlan.upserts...)
	regionScopes := make(map[string]asset.Scope, len(regions))
	for _, region := range regions {
		if scopeMode != asset.ScanSelectedNetworks {
			run.Targets = append(run.Targets, asset.ScanTarget{Key: regionTargetKey(region.RegionID), Kind: asset.ScanTargetRegion, RegionID: region.RegionID, RegionName: region.EffectiveName(), Name: region.EffectiveName()})
		}
		scope := regionScope(connection.ID, root.ID, region, now)
		if existing, ok := rootPlan.regions[region.RegionID]; ok {
			scope.ID = existing.ID
			scope.CreatedAt = existing.CreatedAt
		} else if existing, getErr := c.repositories.Inventory().GetScopeByNaturalKey(ctx, connection.ID, asset.ScopeRegion, region.RegionID); getErr == nil {
			scope.ID = existing.ID
			scope.CreatedAt = existing.CreatedAt
		} else if !errors.Is(getErr, persistence.ErrNotFound) {
			return ScanCreation{}, getErr
		}
		scopes = append(scopes, scope)
		regionScopes[region.RegionID] = scope
		if scopeMode == asset.ScanSelectedNetworks {
			continue
		}
		if broadScan {
			for _, source := range sources {
				if source.KindSpecific || !sourceSupportsScope(source, asset.ScopeRegion) {
					continue
				}
				shards = append(shards, c.shard(run.ID, regionTargetKey(region.RegionID), connection.Provider, region.RegionID, scope.ID, source.Name, "", false, now))
			}
			for _, kind := range kinds {
				source := kindSources[kind.ID]
				if !source.KindSpecific ||
					!kindSupportsScope(kind, asset.ScopeRegion) ||
					!sourceSupportsScope(source, asset.ScopeRegion) {
					continue
				}
				shards = append(shards, c.shard(
					run.ID,
					regionTargetKey(region.RegionID),
					connection.Provider,
					region.RegionID,
					scope.ID,
					source.Name,
					kind.ID,
					source.AuthoritativeDefault,
					now,
				))
			}
			continue
		}
		for _, kind := range kinds {
			source := kindSources[kind.ID]
			if !kindSupportsScope(kind, asset.ScopeRegion) || !sourceSupportsScope(source, asset.ScopeRegion) {
				continue
			}
			shards = append(shards, c.shard(run.ID, regionTargetKey(region.RegionID), connection.Provider, region.RegionID, scope.ID, source.Name, kind.ID, source.AuthoritativeDefault, now))
		}
	}
	if scopeMode == asset.ScanSelectedNetworks {
		for _, target := range networkTargets {
			scope := regionScopes[target.RegionID]
			run.Targets = append(run.Targets, target)
			for _, source := range sources {
				if !source.KindSpecific && sourceSupportsScope(source, asset.ScopeRegion) {
					shards = append(shards, c.shard(run.ID, target.Key, connection.Provider, target.RegionID, scope.ID, source.Name, "", true, now))
				}
			}
			for _, kind := range kinds {
				source := kindSources[kind.ID]
				if !source.KindSpecific ||
					!kindSupportsScope(kind, asset.ScopeRegion) ||
					!sourceSupportsScope(source, asset.ScopeRegion) ||
					!networkSourceKindMatchesTarget(source, kind, target.Kind) {
					continue
				}
				shards = append(shards, c.shard(
					run.ID,
					target.Key,
					connection.Provider,
					target.RegionID,
					scope.ID,
					source.Name,
					kind.ID,
					true,
					now,
				))
			}
		}
	} else if globalSources := sourcesSupportingScope(sources, asset.ScopeGlobal); globalSelected && len(globalSources) > 0 {
		global := globalScope(connection.ID, root, globalEndpointRegion, now)
		if existing, getErr := c.repositories.Inventory().GetScopeByNaturalKey(ctx, connection.ID, asset.ScopeGlobal, global.NativeID); getErr == nil {
			global.ID = existing.ID
			global.CreatedAt = existing.CreatedAt
		} else if !errors.Is(getErr, persistence.ErrNotFound) {
			return ScanCreation{}, getErr
		}
		globalShardStart := len(shards)
		if broadScan {
			for _, source := range globalSources {
				if source.KindSpecific {
					continue
				}
				shards = append(shards, c.shard(run.ID, globalTargetKey(), connection.Provider, "global", global.ID, source.Name, "", false, now))
			}
			for _, kind := range kinds {
				source := kindSources[kind.ID]
				if !source.KindSpecific ||
					!kindSupportsScope(kind, asset.ScopeGlobal) ||
					!sourceSupportsScope(source, asset.ScopeGlobal) {
					continue
				}
				shards = append(shards, c.shard(
					run.ID,
					globalTargetKey(),
					connection.Provider,
					"global",
					global.ID,
					source.Name,
					kind.ID,
					source.AuthoritativeDefault,
					now,
				))
			}
		} else {
			for _, kind := range kinds {
				source := kindSources[kind.ID]
				if kindSupportsScope(kind, asset.ScopeGlobal) && sourceSupportsScope(source, asset.ScopeGlobal) {
					shards = append(shards, c.shard(run.ID, globalTargetKey(), connection.Provider, "global", global.ID, source.Name, kind.ID, source.AuthoritativeDefault, now))
				}
			}
		}
		if len(shards) > globalShardStart {
			scopes = append(scopes, global)
			run.Targets = append(run.Targets, asset.ScanTarget{Key: globalTargetKey(), Kind: asset.ScanTargetGlobal, RegionID: "global", RegionName: "Global", Name: "Global"})
		}
	}
	sortScanTargets(run.Targets)
	if len(shards) == 0 {
		return ScanCreation{}, scanRequestError("scan.scope_unsupported", "the selected resource kinds do not support regional or global scanning", nil)
	}
	shardsByTarget := make(map[string][]string, len(run.Targets))
	for _, shard := range shards {
		shardsByTarget[shard.TargetKey] = append(shardsByTarget[shard.TargetKey], string(shard.ID))
	}
	for _, target := range run.Targets {
		shardIDs := shardsByTarget[target.Key]
		if len(shardIDs) == 0 {
			continue
		}
		jobs = append(jobs, execution.Job{
			ID: execution.JobID(c.entityID("job")), ConnectionID: connection.ID, AggregateType: "scan_task", AggregateID: string(run.ID), TargetKey: target.Key, RetryGeneration: run.RetryGeneration, Type: execution.JobScan, Status: execution.JobPending,
			Payload: map[string]any{"scan_task_id": string(run.ID), "target_key": target.Key, "scan_shard_ids": shardIDs}, RunAt: now, CreatedAt: now, UpdatedAt: now,
		})
	}
	err = c.repositories.WithTx(ctx, func(repositories persistence.Repositories) error {
		if err := connectionapp.GuardActiveWork(ctx, repositories, connection.ID, now); err != nil {
			if errors.Is(err, persistence.ErrConflict) {
				return scanRequestError("scan.connection_changed", "the selected connection changed while creating the scan", nil)
			}
			if errors.Is(err, asset.ErrConnectionNotValidated) {
				return scanRequestError("scan.connection_not_validated", "the selected connection has not passed validation", nil)
			}
			return err
		}
		for _, scope := range rootPlan.upserts {
			if err := repositories.Inventory().PutScope(ctx, scope); err != nil {
				return err
			}
		}
		for _, merge := range rootPlan.merges {
			if err := repositories.Inventory().ConsolidateScopes(ctx, merge.canonicalID, merge.duplicateIDs, merge.aliasParentID); err != nil {
				return err
			}
		}
		for _, scope := range scopes {
			if err := repositories.Inventory().PutScope(ctx, scope); err != nil {
				return err
			}
		}
		for _, kind := range kinds {
			if err := repositories.Inventory().PutResourceKind(ctx, kind); err != nil {
				return err
			}
		}
		if err := repositories.Inventory().CreateScanRun(ctx, run); err != nil {
			return err
		}
		for _, shard := range shards {
			if err := repositories.Inventory().PutScanShard(ctx, shard); err != nil {
				return err
			}
		}
		for _, job := range jobs {
			if err := repositories.Jobs().Enqueue(ctx, job); err != nil {
				return err
			}
		}
		return repositories.Audits().AppendAuditEvent(ctx, execution.AuditEvent{
			ID: execution.AuditEventID(c.entityID("aud")), ConnectionID: connection.ID, Actor: run.RequestedBy,
			Action: "inventory.scan.create", TargetType: "scan_run", TargetID: string(run.ID), Result: "accepted",
			Evidence: map[string]any{"target_count": len(run.Targets), "region_count": len(regions), "shard_count": len(shards), "scope_mode": scopeMode}, CreatedAt: now,
		})
	})
	if err != nil {
		return ScanCreation{}, err
	}
	return ScanCreation{ScanRun: run, Shards: shards, Jobs: jobs}, nil
}

func (c *Creator) resolveRegions(ctx context.Context, request ScanCreationRequest) ([]asset.ConnectionRegion, error) {
	all, err := c.repositories.Regions().ListRegionsByConnection(ctx, request.ConnectionID)
	if err != nil {
		return nil, err
	}
	byID := make(map[string]asset.ConnectionRegion, len(all))
	for _, region := range all {
		byID[region.RegionID] = region
	}
	selected := make([]asset.ConnectionRegion, 0)
	switch request.RegionMode {
	case RegionModeAllActive:
		if len(request.RegionIDs) != 0 {
			return nil, scanRequestError("scan.region_ids_not_allowed", "region_ids must be empty when scanning all active regions", nil)
		}
		for _, region := range all {
			if region.Lifecycle == asset.RegionActive {
				selected = append(selected, region)
			}
		}
		if len(selected) == 0 {
			return nil, scanRequestError("scan.no_active_regions", "the connection has no active regions", nil)
		}
	case RegionModeSelected:
		if len(request.RegionIDs) == 0 {
			return nil, scanRequestError("scan.region_ids_required", "select at least one active region or global", nil)
		}
		seen := make(map[string]struct{}, len(request.RegionIDs))
		for _, requestedID := range request.RegionIDs {
			regionID := strings.TrimSpace(requestedID)
			if _, exists := seen[regionID]; exists {
				return nil, scanRequestError("scan.region_duplicate", "region_ids contains duplicate values", map[string]any{"region_ids": []string{regionID}})
			}
			seen[regionID] = struct{}{}
			if regionID == globalTargetKey() {
				continue
			}
			region, exists := byID[regionID]
			if !exists {
				return nil, scanRequestError("scan.region_not_found", "a selected region does not belong to the connection", map[string]any{"region_ids": []string{regionID}})
			}
			if region.Lifecycle != asset.RegionActive {
				return nil, scanRequestError("scan.region_inactive", "a selected region is retired or excluded", map[string]any{"region_ids": []string{regionID}})
			}
			selected = append(selected, region)
		}
	default:
		return nil, scanRequestError("scan.region_mode_invalid", "region_mode must be all_active or selected", nil)
	}
	sort.Slice(selected, func(i, j int) bool {
		return asset.RegionIDLess(selected[i].RegionID, selected[j].RegionID)
	})
	return selected, nil
}

func (c *Creator) providerDescriptor(provider asset.Provider) (contracts.ProviderDescriptor, error) {
	for _, descriptor := range c.directory.ProviderDescriptors() {
		if descriptor.Provider == provider {
			return descriptor, nil
		}
	}
	return contracts.ProviderDescriptor{}, fmt.Errorf("provider %q is not registered", provider)
}

type accountRootPlan struct {
	root    asset.Scope
	upserts []asset.Scope
	regions map[string]asset.Scope
	merges  []scopeMerge
}

type scopeMerge struct {
	canonicalID   asset.ScopeID
	duplicateIDs  []asset.ScopeID
	aliasParentID asset.ScopeID
}

func (c *Creator) accountRoot(ctx context.Context, connection asset.CloudConnection, now time.Time) (accountRootPlan, error) {
	scopes, err := c.repositories.Inventory().ListScopesByConnectionIncludingAliases(ctx, connection.ID)
	if err != nil {
		return accountRootPlan{}, err
	}
	accountScopes := make([]asset.Scope, 0, 1)
	accountAliases := make([]asset.Scope, 0)
	regionRoots := make([]asset.Scope, 0, 1)
	regionGroups := make(map[string][]asset.Scope)
	for _, scope := range scopes {
		if scope.Kind == asset.ScopeAccount {
			if scope.SupersededByID == "" {
				accountScopes = append(accountScopes, scope)
			} else {
				accountAliases = append(accountAliases, scope)
			}
		}
		if scope.Kind == asset.ScopeRegion {
			regionGroups[scope.NativeID] = append(regionGroups[scope.NativeID], scope)
			if scope.ParentID == "" && scope.SupersededByID == "" {
				regionRoots = append(regionRoots, scope)
			}
		}
	}
	plan := accountRootPlan{regions: make(map[string]asset.Scope, len(regionGroups))}
	if connection.Provider != asset.ProviderAliCloud {
		if len(accountScopes) != 1 {
			return accountRootPlan{}, fmt.Errorf("connection %q requires exactly one account root scope", connection.ID)
		}
		plan.root = accountScopes[0]
	} else {
		accountID, ok := alicloudAccountID(connection.Principal)
		if len(accountScopes) == 0 && (!ok || len(regionRoots) == 0) {
			return accountRootPlan{}, fmt.Errorf("legacy connection %q principal does not contain an Alibaba Cloud account ID", connection.ID)
		}
		if len(accountScopes) == 0 {
			plan.root = asset.Scope{
				ID: asset.ScopeID(c.entityID("scp")), ConnectionID: connection.ID, Kind: asset.ScopeAccount,
				NativeID: accountID, Name: accountID, CreatedAt: now, UpdatedAt: now,
			}
			plan.upserts = append(plan.upserts, plan.root)
		} else {
			plan.root = preferredAccountScope(accountScopes, "", accountID)
		}
		if ok && (plan.root.NativeID != accountID || plan.root.Name != accountID) {
			plan.root.NativeID = accountID
			plan.root.Name = accountID
			plan.root.UpdatedAt = now
			plan.upserts = append(plan.upserts, plan.root)
		}

		aliasIDs := make([]asset.ScopeID, 0, len(accountScopes)+len(accountAliases)+2)
		for _, candidate := range append(accountScopes, accountAliases...) {
			if candidate.ID != plan.root.ID {
				aliasIDs = appendUniqueScopeID(aliasIDs, candidate.ID)
			}
		}
		aliasIDs = removeScopeID(aliasIDs, plan.root.ID)
		if len(aliasIDs) > 0 {
			sort.Slice(aliasIDs, func(i, j int) bool { return aliasIDs[i] < aliasIDs[j] })
			plan.merges = append(plan.merges, scopeMerge{canonicalID: plan.root.ID, duplicateIDs: aliasIDs, aliasParentID: plan.root.ID})
		}
	}
	for regionID, group := range regionGroups {
		active := make([]asset.Scope, 0, len(group))
		for _, candidate := range group {
			if candidate.SupersededByID == "" {
				active = append(active, candidate)
			}
		}
		if len(active) == 0 {
			return accountRootPlan{}, fmt.Errorf("connection %q region %q has no canonical scope", connection.ID, regionID)
		}
		canonical := active[0]
		for _, candidate := range active[1:] {
			if preferRegionScope(candidate, canonical) {
				canonical = candidate
			}
		}
		duplicateIDs := make([]asset.ScopeID, 0, len(group))
		for _, candidate := range group {
			if candidate.ID != canonical.ID {
				duplicateIDs = appendUniqueScopeID(duplicateIDs, candidate.ID)
			}
		}
		wasRoot := canonical.ParentID == ""
		aliasParentID := canonical.ParentID
		if wasRoot {
			aliasParentID = plan.root.ID
		}
		if len(duplicateIDs) > 0 || wasRoot {
			sort.Slice(duplicateIDs, func(i, j int) bool { return duplicateIDs[i] < duplicateIDs[j] })
			plan.merges = append(plan.merges, scopeMerge{canonicalID: canonical.ID, duplicateIDs: duplicateIDs, aliasParentID: aliasParentID})
		}
		if wasRoot {
			canonical.ParentID = plan.root.ID
			canonical.UpdatedAt = now
			plan.upserts = append(plan.upserts, canonical)
		}
		plan.regions[regionID] = canonical
	}
	sort.Slice(plan.merges, func(i, j int) bool { return plan.merges[i].canonicalID < plan.merges[j].canonicalID })
	return plan, nil
}

func preferredAccountScope(scopes []asset.Scope, expectedID asset.ScopeID, expectedNativeID string) asset.Scope {
	preferred := scopes[0]
	for _, candidate := range scopes[1:] {
		candidateExpectedID := candidate.ID == expectedID
		preferredExpectedID := preferred.ID == expectedID
		if candidateExpectedID != preferredExpectedID {
			if candidateExpectedID {
				preferred = candidate
			}
			continue
		}
		candidateExpectedNative := candidate.NativeID == expectedNativeID
		preferredExpectedNative := preferred.NativeID == expectedNativeID
		if candidateExpectedNative != preferredExpectedNative {
			if candidateExpectedNative {
				preferred = candidate
			}
			continue
		}
		if candidate.CreatedAt.Before(preferred.CreatedAt) || (candidate.CreatedAt.Equal(preferred.CreatedAt) && candidate.ID < preferred.ID) {
			preferred = candidate
		}
	}
	return preferred
}

func appendUniqueScopeID(values []asset.ScopeID, candidate asset.ScopeID) []asset.ScopeID {
	if candidate == "" {
		return values
	}
	for _, value := range values {
		if value == candidate {
			return values
		}
	}
	return append(values, candidate)
}

func removeScopeID(values []asset.ScopeID, removed asset.ScopeID) []asset.ScopeID {
	result := values[:0]
	for _, value := range values {
		if value != removed {
			result = append(result, value)
		}
	}
	return result
}

func preferRegionScope(candidate, existing asset.Scope) bool {
	if (candidate.ParentID == "") != (existing.ParentID == "") {
		return candidate.ParentID == ""
	}
	if !candidate.CreatedAt.Equal(existing.CreatedAt) {
		return candidate.CreatedAt.Before(existing.CreatedAt)
	}
	return candidate.ID < existing.ID
}

func alicloudAccountID(principal string) (string, bool) {
	principal = strings.TrimSpace(principal)
	if decimal(principal) {
		return principal, true
	}
	parts := strings.Split(principal, ":")
	if len(parts) >= 4 && strings.EqualFold(parts[0], "acs") && decimal(parts[3]) {
		return parts[3], true
	}
	return "", false
}

func decimal(value string) bool {
	if value == "" {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

func (c *Creator) resolveKinds(provider asset.Provider, requested []asset.ResourceKindID, sources []contracts.InventorySource) ([]asset.ResourceKind, map[asset.ResourceKindID]contracts.InventorySource, error) {
	bundle, err := c.directory.Bundle(provider)
	if err != nil {
		return nil, nil, err
	}
	compiled := make(map[asset.ResourceKindID]spec.CompiledSpec, len(bundle.Specs))
	for _, item := range bundle.Specs {
		compiled[item.ResourceKind.ID] = item
	}
	sourceByName := make(map[string]contracts.InventorySource, len(sources))
	for _, source := range sources {
		sourceByName[source.Name] = source
	}
	if len(requested) == 0 {
		kinds := make([]asset.ResourceKind, 0, len(bundle.Specs))
		kindSources := make(map[asset.ResourceKindID]contracts.InventorySource, len(bundle.Specs))
		for _, item := range bundle.Specs {
			sourceName, err := c.inventorySource(provider, item.ResourceKind, item.Definition.Discovery.Source)
			if err != nil {
				return nil, nil, err
			}
			source, exists := sourceByName[sourceName]
			if !exists || !source.KindSpecific || item.ResourceKind.Provider != provider {
				continue
			}
			kinds = append(kinds, item.ResourceKind)
			kindSources[item.ResourceKind.ID] = source
		}
		sort.Slice(kinds, func(i, j int) bool { return kinds[i].ID < kinds[j].ID })
		return kinds, kindSources, nil
	}
	seen := make(map[asset.ResourceKindID]struct{}, len(requested))
	kinds := make([]asset.ResourceKind, 0, len(requested))
	kindSources := make(map[asset.ResourceKindID]contracts.InventorySource, len(requested))
	for _, id := range requested {
		if _, exists := seen[id]; exists {
			return nil, nil, scanRequestError("scan.resource_kind_duplicate", "resource_kind_ids contains duplicate values", map[string]any{"resource_kind_ids": []asset.ResourceKindID{id}})
		}
		seen[id] = struct{}{}
		item, exists := compiled[id]
		if !exists || item.ResourceKind.Provider != provider {
			return nil, nil, scanRequestError("scan.resource_kind_not_found", "a selected resource kind is not supported by the connection provider", map[string]any{"resource_kind_ids": []asset.ResourceKindID{id}})
		}
		source := sources[0]
		sourceName, err := c.inventorySource(provider, item.ResourceKind, item.Definition.Discovery.Source)
		if err != nil {
			return nil, nil, err
		}
		if named, exists := sourceByName[sourceName]; exists {
			source = named
		} else {
			return nil, nil, fmt.Errorf("resource kind %q selected unknown inventory source %q", id, sourceName)
		}
		kinds = append(kinds, item.ResourceKind)
		kindSources[id] = source
	}
	sort.Slice(kinds, func(i, j int) bool { return kinds[i].ID < kinds[j].ID })
	return kinds, kindSources, nil
}

func (c *Creator) inventorySource(provider asset.Provider, kind asset.ResourceKind, declared string) (string, error) {
	selected := strings.TrimSpace(declared)
	if directory, ok := c.directory.(inventorySourceDirectory); ok {
		resolved, err := directory.ResolveInventorySource(provider, kind, selected)
		if err != nil {
			return "", err
		}
		selected = strings.TrimSpace(resolved)
	}
	if selected == "" {
		return "", fmt.Errorf("resource kind %q has no inventory source", kind.ID)
	}
	return selected, nil
}

func networkProductKindMatchesTarget(kind asset.ResourceKind, targetKind asset.ScanTargetKind) bool {
	switch strings.TrimSpace(kind.Class) {
	case "network.vpc":
		return targetKind == asset.ScanTargetVPC
	case "network.subnet":
		return targetKind == asset.ScanTargetVPC || targetKind == asset.ScanTargetVSwitch
	case "network.vpc_attachment":
		return targetKind == asset.ScanTargetVPC || targetKind == asset.ScanTargetVSwitch
	default:
		return false
	}
}

func networkSourceKindMatchesTarget(
	source contracts.InventorySource,
	kind asset.ResourceKind,
	targetKind asset.ScanTargetKind,
) bool {
	return source.NetworkClosure || networkProductKindMatchesTarget(kind, targetKind)
}

func (c *Creator) shard(runID asset.ScanRunID, targetKey string, provider asset.Provider, regionID string, scopeID asset.ScopeID, source string, kindID asset.ResourceKindID, authoritative bool, now time.Time) asset.ScanShard {
	shard := asset.ScanShard{
		ID: asset.ScanShardID(c.entityID("shr")), ScanTaskID: runID, ScanRunID: runID, TargetKey: targetKey, Provider: provider, Source: source, RegionID: regionID,
		ScopeID: scopeID, ResourceKindID: kindID, Authoritative: authoritative, Status: asset.ShardPending, CreatedAt: now,
	}
	shard.Coverage = asset.Coverage{Source: source, TargetKey: targetKey, ScopeID: scopeID, ResourceKindID: kindID, Authoritative: authoritative}
	return shard
}

func normalizedScope(request ScanCreationRequest) (asset.ScanScopeMode, []asset.ScanTarget, error) {
	mode := request.ScopeMode
	if mode == "" {
		switch request.RegionMode {
		case RegionModeAllActive:
			mode = asset.ScanAllActiveRegions
		case RegionModeSelected:
			mode = asset.ScanSelectedRegions
		}
	}
	switch mode {
	case asset.ScanAllActiveRegions:
		if request.RegionMode != "" && request.RegionMode != RegionModeAllActive {
			return "", nil, scanRequestError("scan.scope_mode_conflict", "scope_mode conflicts with region_mode", nil)
		}
		if len(request.NetworkTargets) > 0 {
			return "", nil, scanRequestError("scan.network_targets_not_allowed", "network_targets are only allowed for selected network scanning", nil)
		}
		return mode, nil, nil
	case asset.ScanSelectedRegions:
		if request.RegionMode != "" && request.RegionMode != RegionModeSelected {
			return "", nil, scanRequestError("scan.scope_mode_conflict", "scope_mode conflicts with region_mode", nil)
		}
		if len(request.NetworkTargets) > 0 {
			return "", nil, scanRequestError("scan.network_targets_not_allowed", "network_targets are only allowed for selected network scanning", nil)
		}
		return mode, nil, nil
	case asset.ScanSelectedNetworks:
		if len(request.RegionIDs) > 0 {
			return "", nil, scanRequestError("scan.region_ids_not_allowed", "region_ids are not allowed for selected network scanning", nil)
		}
		if len(request.ResourceKindIDs) > 0 {
			return "", nil, scanRequestError("scan.resource_kinds_not_allowed", "resource_kind_ids are not allowed for selected network scanning", nil)
		}
		targets, err := canonicalNetworkTargets(request.NetworkTargets, false)
		if err != nil {
			return "", nil, err
		}
		return mode, targets, nil
	default:
		return "", nil, scanRequestError("scan.scope_mode_invalid", "scope_mode must be all_active_regions, selected_regions, or selected_networks", nil)
	}
}

func canonicalNetworkTargets(requested []NetworkTargetRequest, collapseCovered bool) ([]asset.ScanTarget, error) {
	if len(requested) == 0 {
		return nil, scanRequestError("scan.network_targets_required", "select at least one VPC or vSwitch", nil)
	}
	vpcs := make(map[string]struct{})
	if collapseCovered {
		for _, requestedTarget := range requested {
			if requestedTarget.Kind == asset.ScanTargetVPC {
				regionID := strings.TrimSpace(requestedTarget.RegionID)
				nativeID := strings.TrimSpace(requestedTarget.NativeID)
				if regionID != "" && nativeID != "" {
					vpcs[regionID+"\x00"+nativeID] = struct{}{}
				}
			}
		}
	}
	seen := make(map[string]struct{}, len(requested))
	targets := make([]asset.ScanTarget, 0, len(requested))
	for _, requestedTarget := range requested {
		regionID := strings.TrimSpace(requestedTarget.RegionID)
		nativeID := strings.TrimSpace(requestedTarget.NativeID)
		parentNativeID := strings.TrimSpace(requestedTarget.ParentNativeID)
		if requestedTarget.Kind != asset.ScanTargetVPC && requestedTarget.Kind != asset.ScanTargetVSwitch {
			return nil, scanRequestError("scan.network_target_kind_invalid", "network target kind must be vpc or vswitch", nil)
		}
		if regionID == "" || nativeID == "" {
			return nil, scanRequestError("scan.network_target_invalid", "network target region_id and native_id are required", nil)
		}
		if collapseCovered && requestedTarget.Kind == asset.ScanTargetVSwitch {
			if _, covered := vpcs[regionID+"\x00"+parentNativeID]; covered && parentNativeID != "" {
				continue
			}
		}
		key := string(requestedTarget.Kind) + ":" + regionID + ":" + nativeID
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		name := strings.TrimSpace(requestedTarget.Name)
		if name == "" {
			name = nativeID
		}
		targets = append(targets, asset.ScanTarget{Key: key, Kind: requestedTarget.Kind, RegionID: regionID, NativeID: nativeID, Name: name, ParentNativeID: parentNativeID})
	}
	if len(targets) == 0 {
		return nil, scanRequestError("scan.network_targets_required", "select at least one VPC or vSwitch", nil)
	}
	sortScanTargets(targets)
	return targets, nil
}

func (c *Creator) validateNetworkTargets(ctx context.Context, connection asset.CloudConnection, candidates []asset.ScanTarget) ([]asset.ScanTarget, error) {
	directory, ok := c.directory.(networkTargetDirectory)
	if !ok {
		return nil, scanRequestError("scan.network_discovery_unavailable", "the provider does not support network target discovery", nil)
	}
	discoverer, err := directory.ResolveNetworkTargetDiscoverer(connection.Provider)
	if err != nil {
		return nil, err
	}
	verified := make([]NetworkTargetRequest, 0, len(candidates))
	for _, candidate := range candidates {
		option, found, err := findNetworkTarget(ctx, discoverer, contracts.NetworkTargetQuery{
			ConnectionID: connection.ID, Kind: candidate.Kind, RegionID: candidate.RegionID, Query: candidate.NativeID, Limit: 100,
		})
		if err != nil {
			return nil, err
		}
		if !found {
			return nil, scanRequestError("scan.network_target_not_found", "a selected VPC or vSwitch no longer exists", map[string]any{
				"kind": candidate.Kind, "region_id": candidate.RegionID, "native_id": candidate.NativeID,
			})
		}
		verified = append(verified, NetworkTargetRequest{
			Kind: option.Kind, RegionID: option.RegionID, NativeID: option.NativeID, Name: option.Name, ParentNativeID: option.ParentNativeID,
		})
	}
	return canonicalNetworkTargets(verified, true)
}

func findNetworkTarget(ctx context.Context, discoverer contracts.NetworkTargetDiscoverer, query contracts.NetworkTargetQuery) (contracts.NetworkTargetOption, bool, error) {
	seenCursors := make(map[string]struct{})
	for pageNumber := 0; pageNumber < 100; pageNumber++ {
		page, err := discoverer.SearchNetworkTargets(ctx, query)
		if err != nil {
			return contracts.NetworkTargetOption{}, false, err
		}
		for _, option := range page.Items {
			if option.Kind == query.Kind && strings.TrimSpace(option.RegionID) == strings.TrimSpace(query.RegionID) && strings.TrimSpace(option.NativeID) == strings.TrimSpace(query.Query) {
				return option, true, nil
			}
		}
		next := strings.TrimSpace(page.NextCursor)
		if next == "" {
			return contracts.NetworkTargetOption{}, false, nil
		}
		if _, duplicate := seenCursors[next]; duplicate {
			return contracts.NetworkTargetOption{}, false, fmt.Errorf("provider returned a repeated network target cursor")
		}
		seenCursors[next] = struct{}{}
		query.Cursor = next
	}
	return contracts.NetworkTargetOption{}, false, fmt.Errorf("provider network target lookup exceeded 100 pages")
}

func networkRegionIDs(targets []asset.ScanTarget) []string {
	seen := make(map[string]struct{}, len(targets))
	result := make([]string, 0, len(targets))
	for _, target := range targets {
		if _, exists := seen[target.RegionID]; exists {
			continue
		}
		seen[target.RegionID] = struct{}{}
		result = append(result, target.RegionID)
	}
	sort.Strings(result)
	return result
}

func containsRegionID(regionIDs []string, expected string) bool {
	for _, regionID := range regionIDs {
		if strings.TrimSpace(regionID) == expected {
			return true
		}
	}
	return false
}

func sortScanTargets(targets []asset.ScanTarget) {
	sort.SliceStable(targets, func(i, j int) bool {
		left, right := targets[i], targets[j]
		if left.Kind == asset.ScanTargetGlobal || right.Kind == asset.ScanTargetGlobal {
			if left.Kind != right.Kind {
				return left.Kind == asset.ScanTargetGlobal
			}
		}
		if left.RegionID != right.RegionID {
			return asset.RegionIDLess(left.RegionID, right.RegionID)
		}
		return left.Key < right.Key
	})
}

func regionTargetKey(regionID string) string { return "region:" + strings.TrimSpace(regionID) }
func globalTargetKey() string                { return "global" }

func regionScope(connectionID asset.ConnectionID, parentID asset.ScopeID, region asset.ConnectionRegion, now time.Time) asset.Scope {
	return asset.Scope{
		ID: asset.ScopeID(idgen.MustNew("scp")), ConnectionID: connectionID, ParentID: parentID, Kind: asset.ScopeRegion,
		NativeID: region.RegionID, Name: region.EffectiveName(), Location: region.RegionID, CreatedAt: now, UpdatedAt: now,
	}
}

func globalScope(connectionID asset.ConnectionID, root asset.Scope, endpointRegion string, now time.Time) asset.Scope {
	nativeID := strings.TrimSpace(root.NativeID) + "/global"
	return asset.Scope{
		ID: asset.ScopeID(idgen.MustNew("scp")), ConnectionID: connectionID, ParentID: root.ID, Kind: asset.ScopeGlobal,
		NativeID: nativeID, Name: "Global", Location: strings.TrimSpace(endpointRegion), CreatedAt: now, UpdatedAt: now,
	}
}

func kindSupportsScope(kind asset.ResourceKind, scopeKind asset.ScopeKind) bool {
	return len(kind.ScopeKinds) == 0 || containsScopeKind(kind.ScopeKinds, scopeKind)
}

func sourceSupportsScope(source contracts.InventorySource, scopeKind asset.ScopeKind) bool {
	return len(source.RootScopeKinds) == 0 || containsScopeKind(source.RootScopeKinds, scopeKind)
}

func sourcesSupportingScope(sources []contracts.InventorySource, scopeKind asset.ScopeKind) []contracts.InventorySource {
	result := make([]contracts.InventorySource, 0, len(sources))
	for _, source := range sources {
		if sourceSupportsScope(source, scopeKind) {
			result = append(result, source)
		}
	}
	return result
}

func containsScopeKind(values []asset.ScopeKind, expected asset.ScopeKind) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func scanRequestError(code, message string, details map[string]any) error {
	return &ScanRequestError{Code: code, Message: message, Details: details}
}
