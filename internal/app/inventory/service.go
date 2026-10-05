package inventory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
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
		}
		return repository.CreateScanShards(ctx, shards)
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
	err := s.repository.WithinInventoryTx(ctx, func(transaction persistence.InventoryRepository) error {
		repository, err := newBatchRepository(ctx, transaction, connection, batch.Items)
		if err != nil {
			return err
		}
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
		if err := repository.flush(ctx); err != nil {
			return err
		}
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
	raw = contracts.RedactCloudSecrets(raw)
	contentHash, err := observationHash(normalized, raw)
	if err != nil {
		return err
	}
	observation := asset.Observation{
		ID: asset.ObservationID(s.entityID("obs")), AssetID: projected.ID, ScanRunID: shard.ScanRunID, ScanShardID: shard.ID,
		ObservedAt: options.ObservedAt, Source: shard.Source, SchemaRevision: schemaRevision(options.SchemaRevision, kind.BundleRevision),
		ContentHash: contentHash, Authoritative: shard.Authoritative, Priority: options.Priority,
	}
	var current *asset.Observation
	if !isNew && projected.CurrentObservationID != "" {
		value, err := repository.GetObservation(ctx, projected.CurrentObservationID)
		if err != nil && !errors.Is(err, persistence.ErrNotFound) {
			return err
		}
		if err == nil {
			current = &value
		}
	}
	// Observation append deliberately precedes current-projection mutation in
	// the same transaction. A projection can therefore never reference a row
	// that was not durably written.
	if err := repository.AppendObservations(ctx, []asset.Observation{observation}); err != nil {
		return err
	}
	if isNew || shouldProject(observation, current) {
		before := projected
		reappeared := !isNew && projected.ClosedAt != nil
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
		switch {
		case isNew || reappeared:
			return s.recordChange(ctx, repository, asset.ChangeAdded, projected, shard.ScanRunID, observation.ObservedAt, nil)
		default:
			if fields := asset.DiffAssets(before, projected); len(fields) > 0 {
				return s.recordChange(ctx, repository, asset.ChangeModified, projected, shard.ScanRunID, observation.ObservedAt, fields)
			}
		}
	}
	return nil
}

func (s *Service) recordChange(ctx context.Context, repository persistence.InventoryRepository, changeType asset.ChangeType, value asset.Asset, scanTaskID asset.ScanTaskID, at time.Time, fields []asset.FieldChange) error {
	return repository.RecordAssetChanges(ctx, []asset.AssetChange{newChange(changeType, value, scanTaskID, at, fields)})
}

func newChange(changeType asset.ChangeType, value asset.Asset, scanTaskID asset.ScanTaskID, at time.Time, fields []asset.FieldChange) asset.AssetChange {
	change := asset.NewAssetChange(value)
	// Change rows are keyed by scan and asset; their own ID never needs the
	// injected sequence that tests use to pin asset and observation IDs.
	change.ID = idgen.MustNew("chg")
	change.Type = changeType
	change.ScanTaskID = scanTaskID
	change.ChangedAt = at
	change.Fields = fields
	return change
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

func (s *Service) FinishShard(ctx context.Context, shard *asset.ScanShard, status asset.ShardStatus, failureReason string, confirmedAbsent ...asset.Asset) error {
	if shard == nil {
		return fmt.Errorf("scan shard is required")
	}
	if status != asset.ShardSucceeded && status != asset.ShardSkipped && status != asset.ShardFailed {
		return fmt.Errorf("scan shard can only finish as succeeded, skipped, or failed")
	}
	if len(confirmedAbsent) != 0 && status != asset.ShardSucceeded {
		return fmt.Errorf("native absence requires a successful shard")
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
		if updatedShard.Coverage.Complete && updatedShard.Authoritative {
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
				covered, err := repository.ListAssetsByIDs(ctx, coveredIDs)
				if err != nil {
					return err
				}
				if len(covered) != len(coveredIDs) {
					return fmt.Errorf("observed asset of shard %s: %w", updatedShard.ID, persistence.ErrNotFound)
				}
				for _, value := range covered {
					if value.ClosedAt == nil {
						active = append(active, value)
					}
				}
			} else if updatedShard.Authoritative && updatedShard.ScopeID != "" {
				scopeIDs, err := coveredScopeIDs(ctx, repository, updatedShard.ScopeID, run.ConnectionID)
				if err != nil {
					return err
				}
				// A kind-less source closes only the kinds it lists, never the
				// region's other kinds that their own sources report.
				kindIDs := []asset.ResourceKindID{updatedShard.ResourceKindID}
				if updatedShard.ResourceKindID == "" {
					kindIDs = updatedShard.DeclaredKindIDs
				}
				// Only the unseen few are read, not every covered asset.
				var activeIDs []asset.AssetID
				for _, kindID := range kindIDs {
					ids, err := repository.ListActiveAssetIDsByScopes(ctx, run.ConnectionID, scopeIDs, kindID)
					if err != nil {
						return err
					}
					activeIDs = append(activeIDs, ids...)
				}
				slices.Sort(activeIDs)
				unseen := slices.DeleteFunc(activeIDs, func(id asset.AssetID) bool {
					_, ok := seen[id]
					return ok
				})
				candidates, err := repository.ListAssetsByIDs(ctx, unseen)
				if err != nil {
					return err
				}
				for _, value := range candidates {
					if value.ClosedAt == nil {
						active = append(active, value)
					}
				}
			}
			var closed []asset.Asset
			var removals []asset.AssetChange
			for _, projected := range active {
				if _, ok := seen[projected.ID]; ok {
					continue
				}
				projected.ClosedAt = &finishedAt
				closed = append(closed, projected)
				removals = append(removals, newChange(asset.ChangeRemoved, projected, updatedShard.ScanRunID, finishedAt, nil))
			}
			if err := repository.PutAssets(ctx, closed); err != nil {
				return err
			}
			if err := repository.RecordAssetChanges(ctx, removals); err != nil {
				return err
			}
		}
		if err := s.closeConfirmedAbsentAssets(ctx, repository, updatedShard, confirmedAbsent, finishedAt); err != nil {
			return err
		}
		return repository.PutScanShard(ctx, updatedShard)
	})
	if err != nil {
		return err
	}
	*shard = updatedShard
	return nil
}

// coveredScopeIDs lists the coverage scope and every scope below it, the same
// scopes scopeWithinCoverage accepts, so reconciliation reads only the assets a
// shard covers instead of walking every asset of the connection upwards.
func coveredScopeIDs(ctx context.Context, repository persistence.InventoryRepository, coverageScopeID asset.ScopeID, connectionID asset.ConnectionID) ([]asset.ScopeID, error) {
	coverageScope, err := repository.GetScope(ctx, coverageScopeID)
	if err != nil {
		return nil, fmt.Errorf("resolve inventory coverage scope %s: %w", coverageScopeID, err)
	}
	if coverageScope.ConnectionID != connectionID {
		return nil, fmt.Errorf("coverage scope %s belongs to connection %s, expected %s", coverageScope.ID, coverageScope.ConnectionID, connectionID)
	}
	scopes, err := repository.ListScopesByConnection(ctx, connectionID)
	if err != nil {
		return nil, err
	}
	children := make(map[asset.ScopeID][]asset.ScopeID, len(scopes))
	for _, scope := range scopes {
		children[scope.ParentID] = append(children[scope.ParentID], scope.ID)
	}
	result := []asset.ScopeID{coverageScopeID}
	visited := map[asset.ScopeID]struct{}{coverageScopeID: {}}
	for index := 0; index < len(result); index++ {
		for _, child := range children[result[index]] {
			if _, ok := visited[child]; !ok {
				visited[child] = struct{}{}
				result = append(result, child)
			}
		}
	}
	return result, nil
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
	// Provider-normalized configuration is an authoritative payload. Adding or
	// replacing common display fields changes native names, structured tags and
	// configuration fingerprints. Asset already stores those display fields.
	if normalized != nil {
		return normalized, nil
	}
	normalized = make(map[string]any)
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

// cloneSnapshot deep-copies a snapshotMap value, whose only containers are
// JSON objects and arrays.
func cloneSnapshot(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		result := make(map[string]any, len(typed))
		for key, item := range typed {
			result[key] = cloneSnapshot(item)
		}
		return result
	case []any:
		result := make([]any, len(typed))
		for index, item := range typed {
			result[index] = cloneSnapshot(item)
		}
		return result
	}
	return value
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
