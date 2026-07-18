package inventory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/idgen"
	"github.com/loomx-ai/steward/internal/persistence"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

const MaxBatchSize = 1000

type ProjectionOptions struct {
	ObservedAt     time.Time
	Priority       int
	SchemaRevision string
}

type ShardRequest struct {
	Provider       asset.Provider
	Source         string
	ScopeID        asset.ScopeID
	ResourceKindID asset.ResourceKindID
	Authoritative  bool
}

type ScanRequest struct {
	ConnectionID asset.ConnectionID
	RequestedBy  string
	Shards       []ShardRequest
}

type Option func(*Service)

type Service struct {
	repository  persistence.InventoryRepository
	clock       func() time.Time
	idGenerator func() string
}

func NewService(repository persistence.InventoryRepository, options ...Option) *Service {
	service := &Service{
		repository: repository,
		clock:      func() time.Time { return time.Now().UTC() },
	}
	for _, option := range options {
		option(service)
	}
	return service
}

func (s *Service) entityID(prefix string) string {
	if s.idGenerator != nil {
		return s.idGenerator()
	}
	return idgen.MustNew(prefix)
}

func WithClock(clock func() time.Time) Option {
	return func(service *Service) { service.clock = clock }
}

func WithIDGenerator(generator func() string) Option {
	return func(service *Service) { service.idGenerator = generator }
}

func (s *Service) CreateScan(ctx context.Context, request ScanRequest) (asset.ScanRun, []asset.ScanShard, error) {
	if request.ConnectionID == "" || strings.TrimSpace(request.RequestedBy) == "" {
		return asset.ScanRun{}, nil, fmt.Errorf("scan connection and requester are required")
	}
	if len(request.Shards) == 0 {
		return asset.ScanRun{}, nil, fmt.Errorf("scan requires at least one shard")
	}
	createdAt := s.clock()
	run := asset.ScanRun{
		ID: asset.ScanRunID(s.entityID("scn")), ConnectionID: request.ConnectionID, Status: asset.ScanPending,
		RequestedBy: request.RequestedBy, CreatedAt: createdAt,
	}
	shards := make([]asset.ScanShard, 0, len(request.Shards))
	for _, tuple := range request.Shards {
		if tuple.Provider == "" || strings.TrimSpace(tuple.Source) == "" || tuple.ScopeID == "" {
			return asset.ScanRun{}, nil, fmt.Errorf("scan shard provider, source, and scope are required")
		}
		if tuple.Authoritative && tuple.ResourceKindID == "" {
			return asset.ScanRun{}, nil, fmt.Errorf("broad inventory shards cannot be authoritative")
		}
		shard := asset.ScanShard{
			ID: asset.ScanShardID(s.entityID("shr")), ScanRunID: run.ID, Provider: tuple.Provider, Source: tuple.Source,
			ScopeID: tuple.ScopeID, ResourceKindID: tuple.ResourceKindID, Authoritative: tuple.Authoritative,
			Status: asset.ShardPending, CreatedAt: createdAt,
			Coverage: asset.Coverage{
				Source: tuple.Source, ScopeID: tuple.ScopeID, ResourceKindID: tuple.ResourceKindID, Authoritative: tuple.Authoritative,
			},
		}
		shards = append(shards, shard)
	}
	if err := s.StartScan(ctx, run, shards); err != nil {
		return asset.ScanRun{}, nil, err
	}
	return run, shards, nil
}

func (s *Service) StartScan(ctx context.Context, run asset.ScanRun, shards []asset.ScanShard) error {
	if run.ID == "" || run.ConnectionID == "" {
		return fmt.Errorf("scan run ID and connection are required")
	}
	return s.repository.WithinInventoryTx(ctx, func(repository persistence.InventoryRepository) error {
		if err := repository.CreateScanRun(ctx, run); err != nil {
			return err
		}
		for _, shard := range shards {
			if shard.ScanRunID != run.ID {
				return fmt.Errorf("scan shard %q does not belong to run %q", shard.ID, run.ID)
			}
			if err := repository.PutScanShard(ctx, shard); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Service) FinishScan(ctx context.Context, run *asset.ScanRun, shards []asset.ScanShard) error {
	if run == nil || run.ID == "" {
		return fmt.Errorf("scan run is required")
	}
	if len(shards) == 0 {
		return fmt.Errorf("scan run requires shards")
	}
	succeeded := 0
	failed := 0
	skipped := 0
	canceled := 0
	for _, shard := range shards {
		switch shard.Status {
		case asset.ShardSucceeded:
			succeeded++
		case asset.ShardFailed, asset.ShardBlocked:
			failed++
		case asset.ShardCanceled:
			canceled++
		case asset.ShardSkipped:
			skipped++
		default:
			return fmt.Errorf("scan shard %q is not terminal", shard.ID)
		}
	}
	updated := *run
	var completionStatus asset.ScanStatus
	switch {
	case canceled > 0:
		updated.Status = asset.ScanCanceled
	case succeeded+skipped == len(shards):
		updated.Status = asset.ScanReconciling
		completionStatus = asset.ScanSucceeded
	case failed == len(shards):
		updated.Status = asset.ScanFailed
	default:
		updated.Status = asset.ScanReconciling
		completionStatus = asset.ScanPartial
	}
	updated.CompletionStatus = completionStatus
	if completionStatus == "" {
		finishedAt := s.clock()
		updated.FinishedAt = &finishedAt
	} else {
		updated.FinishedAt = nil
	}
	err := s.repository.WithinInventoryTx(ctx, func(repository persistence.InventoryRepository) error {
		return repository.PutScanRun(ctx, updated)
	})
	if err != nil {
		return err
	}
	*run = updated
	return nil
}

func (s *Service) ProjectBatch(ctx context.Context, shard *asset.ScanShard, connection asset.CloudConnection, batch contracts.InventoryBatch, options ProjectionOptions) error {
	if shard == nil {
		return fmt.Errorf("scan shard is required")
	}
	if len(batch.Items) > MaxBatchSize {
		return fmt.Errorf("inventory batch has %d items, maximum is %d", len(batch.Items), MaxBatchSize)
	}
	if options.ObservedAt.IsZero() {
		options.ObservedAt = s.clock()
	}
	if shard.Provider != connection.Provider || shard.ScanRunID == "" || strings.TrimSpace(shard.Source) == "" {
		return fmt.Errorf("scan shard does not match connection or run")
	}
	updatedShard := *shard
	err := s.repository.WithinInventoryTx(ctx, func(repository persistence.InventoryRepository) error {
		for _, item := range batch.Items {
			if err := s.projectItem(ctx, repository, updatedShard, connection, item, options); err != nil {
				return err
			}
		}
		updatedShard.Coverage.Source = updatedShard.Source
		updatedShard.Coverage.TargetKey = updatedShard.TargetKey
		updatedShard.Coverage.ScopeID = updatedShard.ScopeID
		updatedShard.Coverage.ResourceKindID = updatedShard.ResourceKindID
		updatedShard.Coverage.Authoritative = updatedShard.Authoritative
		updatedShard.Coverage.ItemCount += len(batch.Items)
		updatedShard.Coverage.FreshAt = options.ObservedAt
		return repository.PutScanShard(ctx, updatedShard)
	})
	if err != nil {
		return err
	}
	*shard = updatedShard
	return nil
}

func (s *Service) projectItem(ctx context.Context, repository persistence.InventoryRepository, shard asset.ScanShard, connection asset.CloudConnection, item contracts.InventoryItem, options ProjectionOptions) error {
	kind := item.ResourceKind
	if kind.ID == "" || kind.Provider != connection.Provider || strings.TrimSpace(kind.NativeType) == "" || !kind.Capabilities.Has(asset.CapabilityIndexed) {
		return fmt.Errorf("inventory item requires an indexed resource kind for provider %q", connection.Provider)
	}
	if strings.TrimSpace(item.NativeType) == "" || strings.TrimSpace(item.NativeType) != kind.NativeType {
		return fmt.Errorf("inventory item native type %q does not match resource kind %q", item.NativeType, kind.NativeType)
	}
	if shard.ResourceKindID != "" && shard.ResourceKindID != kind.ID {
		return fmt.Errorf("inventory item resource kind %q does not match shard filter %q", kind.ID, shard.ResourceKindID)
	}
	if err := repository.PutResourceKind(ctx, kind); err != nil {
		return err
	}
	scopeID, err := s.projectScope(ctx, repository, shard, connection, item.Scope, options.ObservedAt)
	if err != nil {
		return err
	}
	identity, err := asset.NewIdentity(string(connection.Provider), connection.Partition, connection.ID, kind.NativeType, item.NativeID)
	if err != nil {
		return fmt.Errorf("build inventory identity: %w", err)
	}
	identity.ScopeKey, err = inventoryIdentityScopeKey(ctx, repository, scopeID)
	if err != nil {
		return fmt.Errorf("build inventory identity scope: %w", err)
	}
	projected, err := repository.GetAssetByIdentity(ctx, identity)
	if err != nil && !errors.Is(err, persistence.ErrNotFound) {
		return err
	}
	isNew := errors.Is(err, persistence.ErrNotFound)
	if isNew {
		projected, isNew, err = migrateUnscopedAliCloudOSSBucket(
			ctx,
			repository,
			connection.ID,
			kind,
			identity,
		)
		if err != nil {
			return err
		}
	}
	if !isNew && projected.DeletedAt != nil &&
		strings.EqualFold(strings.TrimSpace(shard.Source), "resource-center") {
		if projected.ClosedAt == nil {
			projected.ClosedAt = projected.DeletedAt
			if err := repository.PutAsset(ctx, projected); err != nil {
				return err
			}
		}
		return nil
	}
	if isNew {
		projected = asset.Asset{
			ID: asset.AssetID(s.entityID("ast")), Identity: identity, ScopeID: scopeID, ResourceKindID: kind.ID,
			FirstSeenAt: options.ObservedAt,
		}
	}
	normalized, err := normalizedSnapshot(item)
	if err != nil {
		return err
	}
	raw, err := snapshotMap(item.Raw)
	if err != nil {
		return err
	}
	contentHash, err := observationHash(normalized, raw)
	if err != nil {
		return err
	}
	observation := asset.Observation{
		ID: asset.ObservationID(s.entityID("obs")), AssetID: projected.ID, ScanRunID: shard.ScanRunID, ScanShardID: shard.ID,
		ObservedAt: options.ObservedAt, Source: shard.Source, SchemaRevision: schemaRevision(options.SchemaRevision, kind.BundleRevision),
		Normalized: normalized, Raw: raw, ContentHash: contentHash, Authoritative: shard.Authoritative, Priority: options.Priority,
	}
	observations, err := repository.ListObservations(ctx, projected.ID)
	if err != nil {
		return err
	}
	var current *asset.Observation
	for index := range observations {
		if observations[index].ID == projected.CurrentObservationID {
			current = &observations[index]
			break
		}
	}
	// Observation append deliberately precedes current-projection mutation in
	// the same transaction. A projection can therefore never reference a row
	// that was not durably written.
	if err := repository.AppendObservation(ctx, observation); err != nil {
		return err
	}
	if isNew || shouldProject(observation, current) {
		projected.Identity = identity
		projected.ScopeID = scopeID
		projected.ResourceKindID = kind.ID
		projected.CurrentObservationID = observation.ID
		projected.Name = item.Name
		projected.State = item.State
		projected.Location = item.Location
		projected.Tags = cloneTags(item.Tags)
		projected.Capabilities = inventoryItemCapabilities(kind.Capabilities, item.Actionable)
		projected.Normalized = normalized
		projected.LastSeenAt = observation.ObservedAt
		projected.ClosedAt = nil
		projected.DeletedAt = nil
		if projected.FirstSeenAt.IsZero() {
			projected.FirstSeenAt = observation.ObservedAt
		}
		if err := repository.PutAsset(ctx, projected); err != nil {
			return err
		}
	}
	return nil
}

func migrateUnscopedAliCloudOSSBucket(
	ctx context.Context,
	repository persistence.InventoryRepository,
	connectionID asset.ConnectionID,
	kind asset.ResourceKind,
	identity asset.Identity,
) (asset.Asset, bool, error) {
	// OSS Bucket was previously modeled as global. Reuse that asset when the
	// regional observation first arrives so its identity and history stay intact.
	if kind.Provider != asset.ProviderAliCloud ||
		kind.NativeType != "ACS::OSS::Bucket" ||
		strings.TrimSpace(identity.ScopeKey) == "" ||
		!containsScopeKind(kind.ScopeKinds, asset.ScopeRegion) ||
		containsScopeKind(kind.ScopeKinds, asset.ScopeGlobal) {
		return asset.Asset{}, true, nil
	}
	legacyIdentity := identity
	legacyIdentity.ScopeKey = ""
	legacy, err := repository.GetAssetByIdentity(ctx, legacyIdentity)
	if errors.Is(err, persistence.ErrNotFound) {
		return asset.Asset{}, true, nil
	}
	if err != nil {
		return asset.Asset{}, true, err
	}
	boundary, err := authoritativeScopeKind(ctx, repository, legacy.ScopeID, connectionID)
	if err != nil {
		return asset.Asset{}, true, err
	}
	if boundary != asset.ScopeGlobal {
		return asset.Asset{}, true, nil
	}
	return legacy, false, nil
}

func authoritativeScopeKind(
	ctx context.Context,
	repository persistence.InventoryRepository,
	scopeID asset.ScopeID,
	connectionID asset.ConnectionID,
) (asset.ScopeKind, error) {
	visited := make(map[asset.ScopeID]struct{})
	for scopeID != "" {
		if _, exists := visited[scopeID]; exists {
			return "", fmt.Errorf("scope hierarchy contains a cycle at %s", scopeID)
		}
		visited[scopeID] = struct{}{}
		scope, err := repository.GetScope(ctx, scopeID)
		if err != nil {
			return "", fmt.Errorf("resolve authoritative scope for %s: %w", scopeID, err)
		}
		if scope.ConnectionID != connectionID {
			return "", fmt.Errorf(
				"scope %s belongs to connection %s, expected %s",
				scope.ID,
				scope.ConnectionID,
				connectionID,
			)
		}
		if scope.Kind == asset.ScopeRegion || scope.Kind == asset.ScopeGlobal {
			return scope.Kind, nil
		}
		scopeID = scope.ParentID
	}
	return "", nil
}

func inventoryItemCapabilities(
	declared asset.CapabilitySet,
	actionable *bool,
) asset.CapabilitySet {
	result := make(asset.CapabilitySet, 0, len(declared))
	for _, capability := range declared {
		if actionable != nil && !*actionable &&
			capability == asset.CapabilityActionable {
			continue
		}
		result = append(result, capability)
	}
	return result
}

func inventoryIdentityScopeKey(
	ctx context.Context,
	repository persistence.InventoryRepository,
	scopeID asset.ScopeID,
) (string, error) {
	seen := make(map[asset.ScopeID]struct{})
	for depth := 0; scopeID != "" && depth < 64; depth++ {
		if _, duplicate := seen[scopeID]; duplicate {
			return "", fmt.Errorf("scope ancestry contains a cycle at %q", scopeID)
		}
		seen[scopeID] = struct{}{}
		scope, err := repository.GetScope(ctx, scopeID)
		if err != nil {
			if errors.Is(err, persistence.ErrNotFound) {
				return "", nil
			}
			return "", err
		}
		switch scope.Kind {
		case asset.ScopeRegion:
			regionID := strings.TrimSpace(scope.NativeID)
			if regionID == "" {
				regionID = strings.TrimSpace(scope.Location)
			}
			if regionID == "" {
				return "", fmt.Errorf("region scope %q has no stable native identity", scope.ID)
			}
			return "region:" + regionID, nil
		case asset.ScopeGlobal:
			return "", nil
		}
		scopeID = scope.ParentID
	}
	if scopeID != "" {
		return "", fmt.Errorf("scope ancestry exceeds 64 levels")
	}
	return "", nil
}

func (s *Service) projectScope(ctx context.Context, repository persistence.InventoryRepository, shard asset.ScanShard, connection asset.CloudConnection, ref contracts.InventoryScope, observedAt time.Time) (asset.ScopeID, error) {
	if ref.Kind == "" && strings.TrimSpace(ref.NativeID) == "" {
		return shard.ScopeID, nil
	}
	if ref.Kind == "" || strings.TrimSpace(ref.NativeID) == "" {
		return "", fmt.Errorf("inventory scope kind and native ID must be supplied together")
	}
	nativeID := strings.TrimSpace(ref.NativeID)
	if shardScope, err := repository.GetScope(ctx, shard.ScopeID); err == nil {
		if shardScope.Kind == ref.Kind && strings.TrimSpace(shardScope.NativeID) == nativeID {
			return shard.ScopeID, nil
		}
	} else if !errors.Is(err, persistence.ErrNotFound) {
		return "", err
	}
	existing, err := repository.GetScopeByNaturalKey(ctx, connection.ID, ref.Kind, nativeID)
	if err == nil {
		return existing.ID, nil
	}
	if !errors.Is(err, persistence.ErrNotFound) {
		return "", err
	}
	id := asset.ScopeID(s.entityID("scp"))
	name := strings.TrimSpace(ref.Name)
	if name == "" {
		name = nativeID
	}
	value := asset.Scope{
		ID: id, ConnectionID: connection.ID, ParentID: shard.ScopeID, Kind: ref.Kind,
		NativeID: nativeID, Name: name, Location: strings.TrimSpace(ref.Location),
		CreatedAt: observedAt, UpdatedAt: observedAt,
	}
	if err := repository.PutScope(ctx, value); err != nil {
		return "", err
	}
	return id, nil
}

func schemaRevision(override, kindRevision string) string {
	if strings.TrimSpace(override) != "" {
		return strings.TrimSpace(override)
	}
	return kindRevision
}

func (s *Service) FinishShard(ctx context.Context, shard *asset.ScanShard, status asset.ShardStatus, failureReason string) error {
	if shard == nil {
		return fmt.Errorf("scan shard is required")
	}
	if status != asset.ShardSucceeded && status != asset.ShardSkipped && status != asset.ShardFailed {
		return fmt.Errorf("scan shard can only finish as succeeded, skipped, or failed")
	}
	finishedAt := s.clock()
	updatedShard := *shard
	err := s.repository.WithinInventoryTx(ctx, func(repository persistence.InventoryRepository) error {
		updatedShard.Status = status
		updatedShard.FinishedAt = &finishedAt
		updatedShard.Coverage.Source = updatedShard.Source
		updatedShard.Coverage.TargetKey = updatedShard.TargetKey
		updatedShard.Coverage.ScopeID = updatedShard.ScopeID
		updatedShard.Coverage.ResourceKindID = updatedShard.ResourceKindID
		updatedShard.Coverage.Authoritative = updatedShard.Authoritative
		updatedShard.Coverage.Complete = status == asset.ShardSucceeded
		updatedShard.Coverage.FreshAt = finishedAt
		updatedShard.Coverage.FailureReason = strings.TrimSpace(failureReason)
		if status == asset.ShardFailed && updatedShard.Coverage.FailureReason == "" {
			updatedShard.Coverage.FailureReason = "scan shard failed"
		}
		if status == asset.ShardSucceeded {
			updatedShard.Coverage.FailureReason = ""
			updatedShard.Coverage.SkipReason = ""
		}
		if status == asset.ShardSkipped {
			updatedShard.Authoritative = false
			updatedShard.Coverage.Authoritative = false
			updatedShard.Coverage.FailureReason = ""
			if updatedShard.Coverage.SkipReason == "" {
				updatedShard.Coverage.SkipReason = asset.SkipProductUnsupported
			}
		}
		if updatedShard.Coverage.Complete {
			seenIDs, err := repository.ListAssetIDsObservedByShard(ctx, updatedShard.ID)
			if err != nil {
				return err
			}
			seen := make(map[asset.AssetID]struct{}, len(seenIDs))
			for _, id := range seenIDs {
				seen[id] = struct{}{}
			}
			run, err := repository.GetScanRun(ctx, updatedShard.ScanRunID)
			if err != nil {
				return err
			}
			var active []asset.Asset
			if run.ScopeMode == asset.ScanSelectedNetworks {
				coveredIDs, err := repository.ListAssetIDsObservedByTarget(ctx, run.ConnectionID, updatedShard.TargetKey, updatedShard.Source, updatedShard.ScopeID, updatedShard.ResourceKindID)
				if err != nil {
					return err
				}
				for _, id := range coveredIDs {
					value, err := repository.GetAsset(ctx, id)
					if err != nil {
						return err
					}
					if value.ClosedAt == nil {
						active = append(active, value)
					}
				}
			} else if updatedShard.Authoritative {
				active, err = repository.ListActiveAssetsByConnection(ctx, run.ConnectionID, updatedShard.ResourceKindID)
				if err != nil {
					return err
				}
			}
			for _, projected := range active {
				if run.ScopeMode != asset.ScanSelectedNetworks {
					covered, err := scopeWithinCoverage(ctx, repository, projected.ScopeID, updatedShard.ScopeID, run.ConnectionID)
					if err != nil {
						return err
					}
					if !covered {
						continue
					}
				}
				if _, ok := seen[projected.ID]; ok {
					continue
				}
				projected.ClosedAt = &finishedAt
				if err := repository.PutAsset(ctx, projected); err != nil {
					return err
				}
			}
		}
		return repository.PutScanShard(ctx, updatedShard)
	})
	if err != nil {
		return err
	}
	*shard = updatedShard
	return nil
}

func scopeWithinCoverage(ctx context.Context, repository persistence.InventoryRepository, scopeID, coverageScopeID asset.ScopeID, connectionID asset.ConnectionID) (bool, error) {
	if scopeID == "" || coverageScopeID == "" {
		return false, nil
	}
	coverageScope, err := repository.GetScope(ctx, coverageScopeID)
	if err != nil {
		return false, fmt.Errorf("resolve inventory coverage scope %s: %w", coverageScopeID, err)
	}
	if coverageScope.ConnectionID != connectionID {
		return false, fmt.Errorf("coverage scope %s belongs to connection %s, expected %s", coverageScope.ID, coverageScope.ConnectionID, connectionID)
	}
	if scopeID == coverageScopeID {
		return true, nil
	}
	visited := make(map[asset.ScopeID]struct{})
	currentID := scopeID
	for currentID != "" {
		if _, exists := visited[currentID]; exists {
			return false, fmt.Errorf("scope hierarchy contains a cycle at %s", currentID)
		}
		visited[currentID] = struct{}{}
		current, err := repository.GetScope(ctx, currentID)
		if err != nil {
			return false, fmt.Errorf("resolve inventory coverage for scope %s: %w", scopeID, err)
		}
		if current.ConnectionID != connectionID {
			return false, fmt.Errorf("scope %s belongs to connection %s, expected %s", current.ID, current.ConnectionID, connectionID)
		}
		if current.ParentID == coverageScopeID {
			return true, nil
		}
		currentID = current.ParentID
	}
	return false, nil
}

func shouldProject(candidate asset.Observation, current *asset.Observation) bool {
	if current == nil {
		return true
	}
	if candidate.ObservedAt.Before(current.ObservedAt) || candidate.Priority < current.Priority {
		return false
	}
	return candidate.ObservedAt.After(current.ObservedAt) || candidate.Priority > current.Priority || candidate.SchemaRevision >= current.SchemaRevision
}

func normalizedSnapshot(item contracts.InventoryItem) (map[string]any, error) {
	normalized, err := snapshotMap(item.Normalized)
	if err != nil {
		return nil, err
	}
	if normalized == nil {
		normalized = make(map[string]any)
	}
	if item.Name != "" {
		normalized["name"] = item.Name
	}
	if item.State != "" {
		normalized["state"] = item.State
	}
	if item.Location != "" {
		normalized["location"] = item.Location
	}
	if item.Tags != nil {
		normalized["tags"] = cloneTags(item.Tags)
	}
	return normalized, nil
}

func snapshotMap(value map[string]any) (map[string]any, error) {
	if value == nil {
		return map[string]any{}, nil
	}
	payload, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("snapshot inventory data: %w", err)
	}
	var result map[string]any
	decoder := json.NewDecoder(strings.NewReader(string(payload)))
	decoder.UseNumber()
	if err := decoder.Decode(&result); err != nil {
		return nil, fmt.Errorf("snapshot inventory data: %w", err)
	}
	return result, nil
}

func observationHash(normalized, raw map[string]any) (string, error) {
	payload, err := json.Marshal(struct {
		Normalized map[string]any `json:"normalized"`
		Raw        map[string]any `json:"raw"`
	}{normalized, raw})
	if err != nil {
		return "", fmt.Errorf("hash observation: %w", err)
	}
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:]), nil
}

func cloneTags(tags map[string]string) map[string]string {
	if tags == nil {
		return map[string]string{}
	}
	result := make(map[string]string, len(tags))
	for key, value := range tags {
		result[key] = value
	}
	return result
}
