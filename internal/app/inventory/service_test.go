package inventory_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/loomx-ai/steward/internal/app/inventory"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/persistence"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

type inventoryRepository struct {
	scopes       map[asset.ScopeID]asset.Scope
	kinds        map[asset.ResourceKindID]asset.ResourceKind
	assets       map[asset.AssetID]asset.Asset
	observations map[asset.AssetID][]asset.Observation
	shards       map[asset.ScanShardID]asset.ScanShard
	runs         map[asset.ScanRunID]asset.ScanRun
	events       []string
}

func newInventoryRepository() *inventoryRepository {
	return &inventoryRepository{
		scopes:       make(map[asset.ScopeID]asset.Scope),
		kinds:        make(map[asset.ResourceKindID]asset.ResourceKind),
		assets:       make(map[asset.AssetID]asset.Asset),
		observations: make(map[asset.AssetID][]asset.Observation),
		shards:       make(map[asset.ScanShardID]asset.ScanShard),
		runs:         make(map[asset.ScanRunID]asset.ScanRun),
	}
}

func (r *inventoryRepository) WithinInventoryTx(ctx context.Context, fn func(persistence.InventoryRepository) error) error {
	return fn(r)
}

func (r *inventoryRepository) PutScope(_ context.Context, value asset.Scope) error {
	if existing, ok := r.scopes[value.ID]; ok && existing.SupersededByID != "" {
		value.SupersededByID = existing.SupersededByID
	}
	r.scopes[value.ID] = value
	return nil
}
func (r *inventoryRepository) GetScope(_ context.Context, id asset.ScopeID) (asset.Scope, error) {
	value, ok := r.scopes[id]
	if !ok {
		return asset.Scope{}, persistence.ErrNotFound
	}
	if value.SupersededByID != "" {
		value, ok = r.scopes[value.SupersededByID]
		if !ok {
			return asset.Scope{}, persistence.ErrNotFound
		}
	}
	return value, nil
}

func (r *inventoryRepository) GetScopeByNaturalKey(_ context.Context, connectionID asset.ConnectionID, kind asset.ScopeKind, nativeID string) (asset.Scope, error) {
	for _, value := range r.scopes {
		if value.ConnectionID == connectionID && value.Kind == kind && value.NativeID == nativeID && value.SupersededByID == "" {
			return value, nil
		}
	}
	return asset.Scope{}, persistence.ErrNotFound
}
func (r *inventoryRepository) ConsolidateScopes(_ context.Context, canonicalID asset.ScopeID, duplicateIDs []asset.ScopeID, aliasParentID asset.ScopeID) error {
	canonical := r.scopes[canonicalID]
	for _, duplicateID := range duplicateIDs {
		duplicate, exists := r.scopes[duplicateID]
		if !exists {
			duplicate = canonical
			duplicate.ID = duplicateID
		}
		duplicate.ParentID = aliasParentID
		duplicate.SupersededByID = canonicalID
		r.scopes[duplicateID] = duplicate
		for id, scope := range r.scopes {
			if scope.ParentID == duplicateID {
				scope.ParentID = canonicalID
				r.scopes[id] = scope
			}
		}
		for id, value := range r.assets {
			if value.ScopeID == duplicateID {
				value.ScopeID = canonicalID
				r.assets[id] = value
			}
		}
		for id, shard := range r.shards {
			if shard.ScopeID == duplicateID {
				shard.ScopeID = canonicalID
				shard.Coverage.ScopeID = canonicalID
				r.shards[id] = shard
			}
		}
	}
	return nil
}
func (r *inventoryRepository) ListScopes(context.Context, persistence.ListOptions) (persistence.Page[asset.Scope], error) {
	items := make([]asset.Scope, 0, len(r.scopes))
	for _, value := range r.scopes {
		if value.SupersededByID == "" {
			items = append(items, value)
		}
	}
	return persistence.Page[asset.Scope]{Items: items}, nil
}
func (r *inventoryRepository) ListScopesByConnection(_ context.Context, connectionID asset.ConnectionID) ([]asset.Scope, error) {
	items := make([]asset.Scope, 0, len(r.scopes))
	for _, value := range r.scopes {
		if value.ConnectionID == connectionID && value.SupersededByID == "" {
			items = append(items, value)
		}
	}
	return items, nil
}
func (r *inventoryRepository) ListScopesByConnectionIncludingAliases(_ context.Context, connectionID asset.ConnectionID) ([]asset.Scope, error) {
	items := make([]asset.Scope, 0, len(r.scopes))
	for _, value := range r.scopes {
		if value.ConnectionID == connectionID {
			items = append(items, value)
		}
	}
	return items, nil
}
func (r *inventoryRepository) PutResourceKind(_ context.Context, value asset.ResourceKind) error {
	r.kinds[value.ID] = value
	return nil
}
func (r *inventoryRepository) GetResourceKind(_ context.Context, id asset.ResourceKindID) (asset.ResourceKind, error) {
	value, ok := r.kinds[id]
	if !ok {
		return asset.ResourceKind{}, persistence.ErrNotFound
	}
	return value, nil
}
func (r *inventoryRepository) CreateScanRun(_ context.Context, run asset.ScanRun) error {
	r.runs[run.ID] = run
	return nil
}
func (r *inventoryRepository) PutScanRun(_ context.Context, run asset.ScanRun) error {
	r.runs[run.ID] = run
	return nil
}

func (r *inventoryRepository) PutScanRunIfControlVersion(_ context.Context, run asset.ScanRun, expected uint64) error {
	current, ok := r.runs[run.ID]
	if !ok {
		return persistence.ErrNotFound
	}
	if current.ControlVersion != expected {
		return persistence.ErrConflict
	}
	r.runs[run.ID] = run
	return nil
}
func (r *inventoryRepository) GetScanRun(_ context.Context, id asset.ScanRunID) (asset.ScanRun, error) {
	value, ok := r.runs[id]
	if !ok {
		return asset.ScanRun{}, persistence.ErrNotFound
	}
	return value, nil
}
func (r *inventoryRepository) ListScanRuns(context.Context, persistence.ListOptions) (persistence.Page[asset.ScanRun], error) {
	items := make([]asset.ScanRun, 0, len(r.runs))
	for _, value := range r.runs {
		items = append(items, value)
	}
	return persistence.Page[asset.ScanRun]{Items: items}, nil
}
func (r *inventoryRepository) ListScanRunListItems(_ context.Context, _ persistence.ListOptions) (persistence.Page[persistence.ScanRunListItem], error) {
	items := make([]persistence.ScanRunListItem, 0, len(r.runs))
	for _, run := range r.runs {
		item := persistence.ScanRunListItem{ScanRun: run, UpdatedAt: run.CreatedAt}
		for _, shard := range r.shards {
			if shard.ScanTaskID == run.ID || shard.ScanRunID == run.ID {
				item.ResourceCount += shard.Coverage.ItemCount
			}
		}
		items = append(items, item)
	}
	return persistence.Page[persistence.ScanRunListItem]{Items: items}, nil
}
func (r *inventoryRepository) ListScanRunsByConnection(_ context.Context, connectionID asset.ConnectionID) ([]asset.ScanRun, error) {
	items := make([]asset.ScanRun, 0, len(r.runs))
	for _, value := range r.runs {
		if value.ConnectionID == connectionID {
			items = append(items, value)
		}
	}
	return items, nil
}
func (r *inventoryRepository) PutScanShard(_ context.Context, shard asset.ScanShard) error {
	r.shards[shard.ID] = shard
	return nil
}
func (r *inventoryRepository) GetScanShard(_ context.Context, id asset.ScanShardID) (asset.ScanShard, error) {
	value, ok := r.shards[id]
	if !ok {
		return asset.ScanShard{}, persistence.ErrNotFound
	}
	return value, nil
}
func (r *inventoryRepository) ListScanShards(context.Context, persistence.ListOptions) (persistence.Page[asset.ScanShard], error) {
	items := make([]asset.ScanShard, 0, len(r.shards))
	for _, value := range r.shards {
		items = append(items, value)
	}
	return persistence.Page[asset.ScanShard]{Items: items}, nil
}
func (r *inventoryRepository) ListScanShardsByRun(_ context.Context, runID asset.ScanRunID) ([]asset.ScanShard, error) {
	items := make([]asset.ScanShard, 0, len(r.shards))
	for _, value := range r.shards {
		if value.ScanRunID == runID {
			items = append(items, value)
		}
	}
	return items, nil
}
func (r *inventoryRepository) PutAsset(_ context.Context, value asset.Asset) error {
	r.events = append(r.events, "asset:"+string(value.ID))
	if existing, ok := r.assets[value.ID]; ok {
		value.Dirty = existing.Dirty
	}
	r.assets[value.ID] = value
	return nil
}
func (r *inventoryRepository) SetAssetDirty(_ context.Context, id asset.AssetID, dirty bool) (asset.Asset, error) {
	value, ok := r.assets[id]
	if !ok {
		return asset.Asset{}, persistence.ErrNotFound
	}
	value.Dirty = dirty
	r.assets[id] = value
	return value, nil
}
func (r *inventoryRepository) GetAsset(_ context.Context, id asset.AssetID) (asset.Asset, error) {
	value, ok := r.assets[id]
	if !ok {
		return asset.Asset{}, persistence.ErrNotFound
	}
	return value, nil
}

func (r *inventoryRepository) GetAssetByIdentity(_ context.Context, identity asset.Identity) (asset.Asset, error) {
	for _, value := range r.assets {
		if value.Identity.Key() == identity.Key() {
			return value, nil
		}
	}
	return asset.Asset{}, persistence.ErrNotFound
}
func (r *inventoryRepository) ListAssetsByIDs(_ context.Context, ids []asset.AssetID) ([]asset.Asset, error) {
	wanted := make(map[asset.AssetID]struct{}, len(ids))
	for _, id := range ids {
		wanted[id] = struct{}{}
	}
	var result []asset.Asset
	for _, value := range r.assets {
		if _, ok := wanted[value.ID]; ok {
			result = append(result, value)
		}
	}
	return result, nil
}
func (r *inventoryRepository) ListAssets(context.Context, persistence.ListOptions) (persistence.Page[asset.Asset], error) {
	return persistence.Page[asset.Asset]{}, nil
}
func (r *inventoryRepository) AppendObservation(_ context.Context, observation asset.Observation) error {
	r.events = append(r.events, "observation:"+string(observation.AssetID))
	r.observations[observation.AssetID] = append(r.observations[observation.AssetID], observation)
	return nil
}
func (r *inventoryRepository) ListObservations(_ context.Context, id asset.AssetID) ([]asset.Observation, error) {
	return append([]asset.Observation(nil), r.observations[id]...), nil
}
func (r *inventoryRepository) ListActiveAssets(_ context.Context, scopeID asset.ScopeID, kindID asset.ResourceKindID) ([]asset.Asset, error) {
	var result []asset.Asset
	for _, value := range r.assets {
		if value.ClosedAt == nil && value.ScopeID == scopeID && (kindID == "" || value.ResourceKindID == kindID) {
			result = append(result, value)
		}
	}
	return result, nil
}
func (r *inventoryRepository) ListActiveAssetsByScopes(_ context.Context, connectionID asset.ConnectionID, scopeIDs []asset.ScopeID, kindID asset.ResourceKindID) ([]asset.Asset, error) {
	allowed := make(map[asset.ScopeID]bool, len(scopeIDs))
	for _, scopeID := range scopeIDs {
		allowed[scopeID] = true
	}
	var result []asset.Asset
	for _, value := range r.assets {
		if value.ClosedAt == nil &&
			value.Identity.ConnectionID == connectionID &&
			allowed[value.ScopeID] &&
			(kindID == "" || value.ResourceKindID == kindID) {
			result = append(result, value)
		}
	}
	return result, nil
}
func (r *inventoryRepository) ListActiveAssetsByConnection(_ context.Context, connectionID asset.ConnectionID, kindID asset.ResourceKindID) ([]asset.Asset, error) {
	var result []asset.Asset
	for _, value := range r.assets {
		if value.ClosedAt == nil && value.Identity.ConnectionID == connectionID && (kindID == "" || value.ResourceKindID == kindID) {
			result = append(result, value)
		}
	}
	return result, nil
}
func (r *inventoryRepository) CountActiveAssetsByScope(_ context.Context, connectionID asset.ConnectionID, kindIDs []asset.ResourceKindID) (map[asset.ScopeID]int, error) {
	allowed := make(map[asset.ResourceKindID]struct{}, len(kindIDs))
	for _, kindID := range kindIDs {
		allowed[kindID] = struct{}{}
	}
	result := make(map[asset.ScopeID]int)
	for _, value := range r.assets {
		_, kindAllowed := allowed[value.ResourceKindID]
		if value.ClosedAt == nil && value.Identity.ConnectionID == connectionID &&
			(len(allowed) == 0 || kindAllowed) {
			result[value.ScopeID]++
		}
	}
	return result, nil
}
func (r *inventoryRepository) ListAssetIDsObservedByShard(_ context.Context, shardID asset.ScanShardID) ([]asset.AssetID, error) {
	var result []asset.AssetID
	for assetID, observations := range r.observations {
		for _, observation := range observations {
			if observation.ScanShardID == shardID {
				result = append(result, assetID)
				break
			}
		}
	}
	return result, nil
}

func (r *inventoryRepository) ListAssetIDsObservedByTarget(_ context.Context, _ asset.ConnectionID, _ string, _ string, _ asset.ScopeID, _ asset.ResourceKindID) ([]asset.AssetID, error) {
	return nil, nil
}

func TestCreateScanBuildsRunAndShardsFromCoverageTuples(t *testing.T) {
	t.Parallel()

	repository := newInventoryRepository()
	createdAt := time.Date(2026, 7, 13, 0, 30, 0, 0, time.UTC)
	service := inventory.NewService(repository,
		inventory.WithClock(func() time.Time { return createdAt }),
		inventory.WithIDGenerator(sequenceIDs("run-created", "shard-ecs", "shard-vpc")),
	)
	run, shards, err := service.CreateScan(context.Background(), inventory.ScanRequest{
		ConnectionID: "connection-1",
		RequestedBy:  "tester",
		Shards: []inventory.ShardRequest{
			{Provider: asset.ProviderAliCloud, Source: "resource-center", ScopeID: "scope-1", ResourceKindID: "kind-ecs", Authoritative: true},
			{Provider: asset.ProviderAliCloud, Source: "resource-center", ScopeID: "scope-1", ResourceKindID: "kind-vpc", Authoritative: true},
		},
	})
	if err != nil {
		t.Fatalf("create scan: %v", err)
	}
	if run.ID != "run-created" || run.Status != asset.ScanPending || len(shards) != 2 || len(repository.shards) != 2 {
		t.Fatalf("run=%+v shards=%+v", run, shards)
	}
	for _, shard := range shards {
		if shard.ScanRunID != run.ID || shard.Status != asset.ShardPending || shard.Coverage.Source != shard.Source || shard.Coverage.ResourceKindID != shard.ResourceKindID {
			t.Fatalf("invalid shard tuple: %+v", shard)
		}
	}
}

func TestFinishScanDerivesPartialStatusFromTerminalShards(t *testing.T) {
	t.Parallel()

	repository := newInventoryRepository()
	finishedAt := time.Date(2026, 7, 13, 0, 45, 0, 0, time.UTC)
	service := inventory.NewService(repository, inventory.WithClock(func() time.Time { return finishedAt }))
	run := asset.ScanRun{ID: "run-finish", ConnectionID: "connection-1", Status: asset.ScanRunning}
	shards := []asset.ScanShard{{ID: "shard-success", Status: asset.ShardSucceeded}, {ID: "shard-failed", Status: asset.ShardFailed}}

	if err := service.FinishScan(context.Background(), &run, shards); err != nil {
		t.Fatalf("finish scan: %v", err)
	}
	if run.Status != asset.ScanReconciling || run.CompletionStatus != asset.ScanPartial || run.FinishedAt != nil {
		t.Fatalf("finished run = %+v", run)
	}
}

func TestFinishScanTreatsSkippedShardsAsSuccessfulCompletion(t *testing.T) {
	repository := newInventoryRepository()
	now := time.Date(2026, 7, 13, 10, 1, 0, 0, time.UTC)
	service := inventory.NewService(repository, inventory.WithClock(func() time.Time { return now }))
	run := asset.ScanRun{ID: "run-skipped", ConnectionID: "connection-1", Status: asset.ScanRunning}
	shards := []asset.ScanShard{{ID: "shard-success", Status: asset.ShardSucceeded}, {ID: "shard-skipped", Status: asset.ShardSkipped}}

	if err := service.FinishScan(context.Background(), &run, shards); err != nil {
		t.Fatal(err)
	}
	if run.Status != asset.ScanReconciling || run.CompletionStatus != asset.ScanSucceeded || run.FinishedAt != nil {
		t.Fatalf("run = %+v", run)
	}
}

func TestFinishShardSkipsWithoutClosingAssets(t *testing.T) {
	repository := newInventoryRepository()
	now := time.Date(2026, 7, 13, 10, 2, 0, 0, time.UTC)
	service := inventory.NewService(repository, inventory.WithClock(func() time.Time { return now }))
	shard := asset.ScanShard{ID: "shard-skip", ScanRunID: "run-skip", ScopeID: "scope-1", ResourceKindID: "kind-1", Authoritative: true}
	shard.Coverage.SkipReason = asset.SkipProviderRegionUnavailable

	if err := service.FinishShard(context.Background(), &shard, asset.ShardSkipped, ""); err != nil {
		t.Fatal(err)
	}
	if shard.Status != asset.ShardSkipped || shard.Authoritative || shard.Coverage.Authoritative || shard.Coverage.Complete || shard.Coverage.SkipReason != asset.SkipProviderRegionUnavailable {
		t.Fatalf("shard = %+v", shard)
	}
}

func TestL0BatchCreatesImmutableObservationBeforeProjection(t *testing.T) {
	t.Parallel()

	repository := newInventoryRepository()
	service := inventory.NewService(repository, inventory.WithIDGenerator(sequenceIDs("ast-1", "observation-1")))
	shard := asset.ScanShard{ID: "shard-1", ScanRunID: "run-1", Provider: asset.ProviderAliCloud, ScopeID: "scope-1", ResourceKindID: "kind-1", Source: "resource-index", Authoritative: false}
	connection := asset.CloudConnection{ID: "connection-1", Provider: asset.ProviderAliCloud, Partition: "aliyun"}
	kind := asset.ResourceKind{ID: "kind-1", Provider: asset.ProviderAliCloud, NativeType: "ACS::ECS::Instance", Capabilities: asset.CapabilitySet{asset.CapabilityIndexed}, BundleRevision: "bundle-1"}
	observedAt := time.Date(2026, 7, 13, 1, 0, 0, 0, time.UTC)
	batch := contracts.InventoryBatch{Items: []contracts.InventoryItem{{
		NativeType:   kind.NativeType,
		NativeID:     "i-1",
		ResourceKind: kind,
		Name:         "instance-1",
		State:        "Running",
		Location:     "cn-hangzhou",
		Normalized:   map[string]any{"chargeType": "PostPaid"},
		Raw:          map[string]any{"InstanceId": "i-1"},
	}}}

	if err := service.ProjectBatch(context.Background(), &shard, connection, batch, inventory.ProjectionOptions{ObservedAt: observedAt, Priority: 10}); err != nil {
		t.Fatalf("project L0 batch: %v", err)
	}
	if len(repository.assets) != 1 {
		t.Fatalf("assets = %d, want 1", len(repository.assets))
	}
	if len(repository.events) < 2 || !strings.HasPrefix(repository.events[0], "observation:") || !strings.HasPrefix(repository.events[1], "asset:") {
		t.Fatalf("projection write order = %v", repository.events)
	}
	for _, projected := range repository.assets {
		observations := repository.observations[projected.ID]
		if len(observations) != 1 || projected.CurrentObservationID != observations[0].ID {
			t.Fatalf("asset=%+v observations=%+v", projected, observations)
		}
		if !projected.Capabilities.Has(asset.CapabilityIndexed) || projected.Normalized["chargeType"] != "PostPaid" {
			t.Fatalf("incorrect L0 projection: %+v", projected)
		}
	}
}

func TestInventoryItemCanNarrowActionableCapabilityForOneResource(t *testing.T) {
	t.Parallel()

	repository := newInventoryRepository()
	service := inventory.NewService(
		repository,
		inventory.WithIDGenerator(sequenceIDs(
			"asset-managed", "observation-managed",
			"asset-user", "observation-user",
		)),
	)
	shard := asset.ScanShard{
		ID: "shard-actionable", ScanRunID: "run-actionable",
		Provider: asset.ProviderAliCloud, ScopeID: "scope-1",
		ResourceKindID: "kind-kms", Source: "resource-center",
		Authoritative: true,
	}
	connection := asset.CloudConnection{
		ID: "connection-1", Provider: asset.ProviderAliCloud, Partition: "aliyun",
	}
	kind := asset.ResourceKind{
		ID: "kind-kms", Provider: asset.ProviderAliCloud,
		NativeType: "ACS::KMS::Key",
		Capabilities: asset.CapabilitySet{
			asset.CapabilityIndexed,
			asset.CapabilityDetailed,
			asset.CapabilityActionable,
		},
	}
	notActionable := false
	batch := contracts.InventoryBatch{Items: []contracts.InventoryItem{
		{
			NativeType: kind.NativeType, NativeID: "key-managed",
			ResourceKind: kind, Actionable: &notActionable,
			Raw: map[string]any{"KeyId": "key-managed"},
		},
		{
			NativeType: kind.NativeType, NativeID: "key-user",
			ResourceKind: kind,
			Raw:          map[string]any{"KeyId": "key-user"},
		},
	}}
	if err := service.ProjectBatch(
		context.Background(),
		&shard,
		connection,
		batch,
		inventory.ProjectionOptions{ObservedAt: time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)},
	); err != nil {
		t.Fatalf("project item-level actionability: %v", err)
	}
	byNativeID := make(map[string]asset.Asset, len(repository.assets))
	for _, value := range repository.assets {
		byNativeID[value.Identity.NativeID] = value
	}
	if byNativeID["key-managed"].Capabilities.Has(asset.CapabilityActionable) ||
		!byNativeID["key-managed"].Capabilities.Has(asset.CapabilityIndexed) {
		t.Fatalf("managed KMS capabilities = %+v", byNativeID["key-managed"].Capabilities)
	}
	if !byNativeID["key-user"].Capabilities.Has(asset.CapabilityActionable) {
		t.Fatalf("user KMS capabilities = %+v", byNativeID["key-user"].Capabilities)
	}
	storedKind := repository.kinds[kind.ID]
	if !storedKind.Capabilities.Has(asset.CapabilityActionable) {
		t.Fatalf("resource kind capability was narrowed globally: %+v", storedKind.Capabilities)
	}
}

func TestResourceCenterDoesNotReopenCleanupDeletedAsset(t *testing.T) {
	t.Parallel()

	repository := newInventoryRepository()
	connection := asset.CloudConnection{ID: "connection-deleted", Provider: asset.ProviderAliCloud, Partition: "aliyun"}
	kind := asset.ResourceKind{
		ID: "kind-sls", Provider: asset.ProviderAliCloud, NativeType: "ACS::SLS::Project",
		Capabilities: asset.CapabilitySet{asset.CapabilityIndexed},
	}
	identity, err := asset.NewIdentity("alicloud", "aliyun", connection.ID, kind.NativeType, "deleted-project")
	if err != nil {
		t.Fatal(err)
	}
	deletedAt := time.Date(2026, 8, 5, 6, 19, 9, 0, time.UTC)
	existing := asset.Asset{
		ID: "asset-deleted", Identity: identity, ScopeID: "scope-deleted", ResourceKindID: kind.ID,
		CurrentObservationID: "observation-before-delete", Name: "deleted-project",
		FirstSeenAt: deletedAt.Add(-time.Hour), LastSeenAt: deletedAt.Add(-time.Minute),
		DeletedAt: &deletedAt,
	}
	repository.assets[existing.ID] = existing
	repository.observations[existing.ID] = []asset.Observation{{
		ID: existing.CurrentObservationID, AssetID: existing.ID, ObservedAt: existing.LastSeenAt,
		Source: "resource-center",
	}}
	service := inventory.NewService(repository)
	shard := asset.ScanShard{
		ID: "stale-resource-center-shard", ScanRunID: "stale-resource-center-scan",
		Provider: asset.ProviderAliCloud, ScopeID: existing.ScopeID,
		ResourceKindID: kind.ID, Source: "resource-center",
	}

	err = service.ProjectBatch(context.Background(), &shard, connection, contracts.InventoryBatch{
		Items: []contracts.InventoryItem{{
			NativeType: kind.NativeType, NativeID: existing.Identity.NativeID,
			ResourceKind: kind, Name: "stale-resource-center-name",
		}},
	}, inventory.ProjectionOptions{ObservedAt: deletedAt.Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}

	projected := repository.assets[existing.ID]
	if projected.ClosedAt == nil || !projected.ClosedAt.Equal(deletedAt) ||
		projected.DeletedAt == nil || !projected.DeletedAt.Equal(deletedAt) {
		t.Fatalf("cleanup deletion tombstone was not preserved: %+v", projected)
	}
	if projected.CurrentObservationID != existing.CurrentObservationID ||
		projected.Name != existing.Name ||
		len(repository.observations[existing.ID]) != 1 {
		t.Fatalf("stale Resource Center result changed the deleted asset: asset=%+v observations=%+v", projected, repository.observations[existing.ID])
	}
}

func TestLiveProductInventoryCanReopenRecreatedCleanupDeletedAsset(t *testing.T) {
	t.Parallel()

	repository := newInventoryRepository()
	connection := asset.CloudConnection{ID: "connection-recreated", Provider: asset.ProviderAliCloud, Partition: "aliyun"}
	kind := asset.ResourceKind{
		ID: "kind-sls", Provider: asset.ProviderAliCloud, NativeType: "ACS::SLS::Project",
		Capabilities: asset.CapabilitySet{asset.CapabilityIndexed},
	}
	identity, err := asset.NewIdentity("alicloud", "aliyun", connection.ID, kind.NativeType, "recreated-project")
	if err != nil {
		t.Fatal(err)
	}
	deletedAt := time.Date(2026, 8, 5, 6, 19, 9, 0, time.UTC)
	existing := asset.Asset{
		ID: "asset-recreated", Identity: identity, ScopeID: "scope-recreated", ResourceKindID: kind.ID,
		CurrentObservationID: "observation-before-delete", Name: "deleted-project",
		FirstSeenAt: deletedAt.Add(-time.Hour), LastSeenAt: deletedAt.Add(-time.Minute),
		ClosedAt: &deletedAt, DeletedAt: &deletedAt,
	}
	repository.assets[existing.ID] = existing
	repository.observations[existing.ID] = []asset.Observation{{
		ID: existing.CurrentObservationID, AssetID: existing.ID, ObservedAt: existing.LastSeenAt,
		Source: "resource-center",
	}}
	service := inventory.NewService(repository, inventory.WithIDGenerator(sequenceIDs("recreated-observation")))
	shard := asset.ScanShard{
		ID: "live-product-shard", ScanRunID: "live-product-scan",
		Provider: asset.ProviderAliCloud, ScopeID: existing.ScopeID,
		ResourceKindID: kind.ID, Source: "product-api",
	}
	observedAt := deletedAt.Add(time.Hour)

	err = service.ProjectBatch(context.Background(), &shard, connection, contracts.InventoryBatch{
		Items: []contracts.InventoryItem{{
			NativeType: kind.NativeType, NativeID: existing.Identity.NativeID,
			ResourceKind: kind, Name: "recreated-project",
		}},
	}, inventory.ProjectionOptions{ObservedAt: observedAt})
	if err != nil {
		t.Fatal(err)
	}

	projected := repository.assets[existing.ID]
	if projected.ClosedAt != nil || projected.DeletedAt != nil ||
		projected.CurrentObservationID != "recreated-observation" ||
		projected.Name != "recreated-project" {
		t.Fatalf("live product inventory did not reopen recreated asset: %+v", projected)
	}
}

func TestRegionalProjectionKeepsSameNativeIDInDifferentRegions(t *testing.T) {
	t.Parallel()

	repository := newInventoryRepository()
	connection := asset.CloudConnection{
		ID: "connection-regional-identity", Provider: asset.ProviderAliCloud, Partition: "aliyun",
	}
	repository.scopes["scope-hangzhou"] = asset.Scope{
		ID: "scope-hangzhou", ConnectionID: connection.ID, Kind: asset.ScopeRegion,
		NativeID: "cn-hangzhou", Location: "cn-hangzhou",
	}
	repository.scopes["scope-shanghai"] = asset.Scope{
		ID: "scope-shanghai", ConnectionID: connection.ID, Kind: asset.ScopeRegion,
		NativeID: "cn-shanghai", Location: "cn-shanghai",
	}
	kind := asset.ResourceKind{
		ID: "alicloud:ACS::ECS::KeyPair", Provider: asset.ProviderAliCloud,
		NativeType: "ACS::ECS::KeyPair", Capabilities: asset.CapabilitySet{asset.CapabilityIndexed},
	}
	service := inventory.NewService(
		repository,
		inventory.WithIDGenerator(sequenceIDs(
			"asset-hangzhou", "observation-hangzhou",
			"asset-shanghai", "observation-shanghai",
		)),
	)
	for _, input := range []struct {
		scopeID asset.ScopeID
		region  string
	}{
		{scopeID: "scope-hangzhou", region: "cn-hangzhou"},
		{scopeID: "scope-shanghai", region: "cn-shanghai"},
	} {
		shard := asset.ScanShard{
			ID: asset.ScanShardID("shard-" + input.region), ScanRunID: "scan-regional-identity",
			Provider: asset.ProviderAliCloud, ScopeID: input.scopeID,
			ResourceKindID: kind.ID, Source: "resource-center",
		}
		err := service.ProjectBatch(context.Background(), &shard, connection, contracts.InventoryBatch{
			Items: []contracts.InventoryItem{{
				NativeType: kind.NativeType, NativeID: "shared-name", ResourceKind: kind,
				Name: "shared-name", Location: input.region,
			}},
		}, inventory.ProjectionOptions{ObservedAt: time.Date(2026, 8, 4, 10, 0, 0, 0, time.UTC)})
		if err != nil {
			t.Fatal(err)
		}
	}

	if len(repository.assets) != 2 {
		t.Fatalf("assets = %#v, want two regional identities", repository.assets)
	}
	scopeKeys := make(map[string]struct{}, len(repository.assets))
	for _, value := range repository.assets {
		scopeKeys[value.Identity.ScopeKey] = struct{}{}
	}
	if _, ok := scopeKeys["region:cn-hangzhou"]; !ok {
		t.Fatalf("regional scope keys = %v", scopeKeys)
	}
	if _, ok := scopeKeys["region:cn-shanghai"]; !ok {
		t.Fatalf("regional scope keys = %v", scopeKeys)
	}
}

func TestRegionalProjectionMigratesLegacyGlobalIdentity(t *testing.T) {
	t.Parallel()

	repository := newInventoryRepository()
	connection := asset.CloudConnection{
		ID: "connection-oss", Provider: asset.ProviderAliCloud, Partition: "aliyun",
	}
	repository.scopes["scope-account"] = asset.Scope{
		ID: "scope-account", ConnectionID: connection.ID, Kind: asset.ScopeAccount,
	}
	repository.scopes["scope-global"] = asset.Scope{
		ID: "scope-global", ConnectionID: connection.ID, ParentID: "scope-account",
		Kind: asset.ScopeGlobal, NativeID: "account/global",
	}
	repository.scopes["scope-hangzhou"] = asset.Scope{
		ID: "scope-hangzhou", ConnectionID: connection.ID, ParentID: "scope-account",
		Kind: asset.ScopeRegion, NativeID: "cn-hangzhou", Location: "cn-hangzhou",
	}
	kind := asset.ResourceKind{
		ID: "alicloud:ACS::OSS::Bucket", Provider: asset.ProviderAliCloud,
		NativeType: "ACS::OSS::Bucket", ScopeKinds: []asset.ScopeKind{asset.ScopeRegion},
		Capabilities: asset.CapabilitySet{asset.CapabilityIndexed}, BundleRevision: "regional-oss",
	}
	legacyIdentity, err := asset.NewIdentity(
		string(connection.Provider),
		connection.Partition,
		connection.ID,
		kind.NativeType,
		"bucket-a",
	)
	if err != nil {
		t.Fatal(err)
	}
	observedAt := time.Date(2026, 8, 11, 10, 0, 0, 0, time.UTC)
	legacy := asset.Asset{
		ID: "asset-oss", Identity: legacyIdentity, ScopeID: "scope-global",
		ResourceKindID: kind.ID, CurrentObservationID: "observation-global",
		FirstSeenAt: observedAt.Add(-time.Hour), LastSeenAt: observedAt.Add(-time.Hour),
	}
	repository.assets[legacy.ID] = legacy
	repository.observations[legacy.ID] = []asset.Observation{{
		ID: legacy.CurrentObservationID, AssetID: legacy.ID, ObservedAt: legacy.LastSeenAt,
	}}
	service := inventory.NewService(
		repository,
		inventory.WithIDGenerator(sequenceIDs("observation-regional")),
	)
	shard := asset.ScanShard{
		ID: "shard-oss", ScanRunID: "scan-oss", Provider: connection.Provider,
		ScopeID: "scope-hangzhou", ResourceKindID: kind.ID, Source: "resource-center",
	}
	err = service.ProjectBatch(
		context.Background(),
		&shard,
		connection,
		contracts.InventoryBatch{Items: []contracts.InventoryItem{{
			NativeType: kind.NativeType, NativeID: legacy.Identity.NativeID,
			ResourceKind: kind, Name: "bucket-a", Location: "cn-hangzhou",
			Scope: contracts.InventoryScope{
				Kind: asset.ScopeRegion, NativeID: "cn-hangzhou", Location: "cn-hangzhou",
			},
		}}},
		inventory.ProjectionOptions{ObservedAt: observedAt},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(repository.assets) != 1 {
		t.Fatalf("assets = %#v, want the existing asset to be migrated", repository.assets)
	}
	projected := repository.assets[legacy.ID]
	if projected.ScopeID != "scope-hangzhou" ||
		projected.Identity.ScopeKey != "region:cn-hangzhou" ||
		projected.CurrentObservationID != "observation-regional" ||
		len(repository.observations[legacy.ID]) != 2 {
		t.Fatalf("migrated OSS asset = %+v, observations = %+v", projected, repository.observations[legacy.ID])
	}
}

func TestBroadBatchProjectsPerItemKindsAndHierarchicalScopes(t *testing.T) {
	t.Parallel()

	repository := newInventoryRepository()
	service := inventory.NewService(repository, inventory.WithIDGenerator(sequenceIDs("scp-global", "ast-global", "observation-global", "scp-regional", "ast-regional", "observation-regional")))
	connection := asset.CloudConnection{ID: "connection-aws", Provider: asset.ProviderAWS, Partition: "aws"}
	root := asset.Scope{ID: "scope-account", ConnectionID: connection.ID, Kind: asset.ScopeAccount, NativeID: "123456789012", Name: "123456789012"}
	repository.scopes[root.ID] = root
	shard := asset.ScanShard{
		ID: "shard-broad", ScanRunID: "run-broad", Provider: asset.ProviderAWS,
		ScopeID: root.ID, Source: "resource-explorer", Authoritative: false,
	}
	batch := contracts.InventoryBatch{Items: []contracts.InventoryItem{
		{
			NativeType: "AWS::IAM::Role", NativeID: "arn:aws:iam::123456789012:role/Admin", Name: "Admin",
			ResourceKind: asset.ResourceKind{ID: "aws:AWS::IAM::Role", Provider: asset.ProviderAWS, NativeType: "AWS::IAM::Role", DisplayName: "IAM Role", ScopeKinds: []asset.ScopeKind{asset.ScopeGlobal}, Capabilities: asset.CapabilitySet{asset.CapabilityIndexed}, BundleRevision: "catalog-aws"},
			Scope:        contracts.InventoryScope{Kind: asset.ScopeGlobal, NativeID: "123456789012/global", Name: "Global"},
			Raw:          map[string]any{"arn": "arn:aws:iam::123456789012:role/Admin"},
		},
		{
			NativeType: "AWS::EC2::Instance", NativeID: "arn:aws:ec2:us-east-1:123456789012:instance/i-1", Name: "i-1", Location: "us-east-1",
			ResourceKind: asset.ResourceKind{ID: "aws:AWS::EC2::Instance", Provider: asset.ProviderAWS, NativeType: "AWS::EC2::Instance", DisplayName: "EC2 Instance", ScopeKinds: []asset.ScopeKind{asset.ScopeRegion}, Capabilities: asset.CapabilitySet{asset.CapabilityIndexed}, BundleRevision: "catalog-aws"},
			Scope:        contracts.InventoryScope{Kind: asset.ScopeRegion, NativeID: "us-east-1", Name: "us-east-1", Location: "us-east-1"},
			Raw:          map[string]any{"arn": "arn:aws:ec2:us-east-1:123456789012:instance/i-1"},
		},
	}}

	if err := service.ProjectBatch(context.Background(), &shard, connection, batch, inventory.ProjectionOptions{ObservedAt: time.Date(2026, 7, 13, 3, 30, 0, 0, time.UTC)}); err != nil {
		t.Fatalf("project broad batch: %v", err)
	}
	if len(repository.assets) != 2 || len(repository.kinds) != 2 || len(repository.scopes) != 3 {
		t.Fatalf("assets=%d kinds=%d scopes=%d", len(repository.assets), len(repository.kinds), len(repository.scopes))
	}
	for _, projected := range repository.assets {
		if projected.Identity.NativeType == "" || projected.ResourceKindID == "" || projected.ScopeID == "" {
			t.Fatalf("incomplete per-item projection: %+v", projected)
		}
		if projected.ScopeID == root.ID {
			t.Fatalf("asset was not mapped to a child scope: %+v", projected)
		}
		mapped := repository.scopes[projected.ScopeID]
		if mapped.ParentID != root.ID || mapped.ConnectionID != connection.ID {
			t.Fatalf("mapped scope = %+v", mapped)
		}
	}
}

func TestRegionScopedProjectionReusesLegacyRegionScope(t *testing.T) {
	repository := newInventoryRepository()
	connection := asset.CloudConnection{ID: "connection-region", Provider: asset.ProviderAWS, Partition: "aws"}
	regionID := asset.ScopeID("legacy-region-scope")
	repository.scopes["account"] = asset.Scope{ID: "account", ConnectionID: connection.ID, Kind: asset.ScopeAccount, NativeID: "123456789012"}
	repository.scopes[regionID] = asset.Scope{ID: regionID, ConnectionID: connection.ID, ParentID: "account", Kind: asset.ScopeRegion, NativeID: "us-east-1", Name: "US East 1", Location: "us-east-1"}
	kind := asset.ResourceKind{ID: "aws:AWS::EC2::Instance", Provider: asset.ProviderAWS, NativeType: "AWS::EC2::Instance", Capabilities: asset.CapabilitySet{asset.CapabilityIndexed}, BundleRevision: "test"}
	shard := asset.ScanShard{ID: "region-shard", ScanRunID: "region-run", Provider: asset.ProviderAWS, Source: "resource-explorer", ScopeID: regionID}
	service := inventory.NewService(repository, inventory.WithIDGenerator(sequenceIDs("ast-region", "region-observation")))

	err := service.ProjectBatch(context.Background(), &shard, connection, contracts.InventoryBatch{Items: []contracts.InventoryItem{{
		NativeType: kind.NativeType, NativeID: "arn:aws:ec2:us-east-1:123456789012:instance/i-1", ResourceKind: kind,
		Name: "i-1", Scope: contracts.InventoryScope{Kind: asset.ScopeRegion, NativeID: "us-east-1", Name: "us-east-1", Location: "us-east-1"},
	}}}, inventory.ProjectionOptions{ObservedAt: time.Date(2026, 7, 21, 10, 0, 0, 0, time.UTC)})
	if err != nil {
		t.Fatal(err)
	}
	if stored := repository.scopes[regionID]; stored.ParentID != "account" {
		t.Fatalf("region scope parent = %q, want account", stored.ParentID)
	}
	if len(repository.scopes) != 2 {
		t.Fatalf("projection created a duplicate region scope: %+v", repository.scopes)
	}
}

func TestFailedAuthoritativeShardDoesNotCloseMissingAsset(t *testing.T) {
	t.Parallel()

	repository := newInventoryRepository()
	repository.assets["asset-1"] = activeAsset("asset-1")
	service := inventory.NewService(repository)
	shard := asset.ScanShard{ID: "shard-2", ScopeID: "scope-1", ResourceKindID: "kind-1", Authoritative: true}

	if err := service.FinishShard(context.Background(), &shard, asset.ShardFailed, "provider timeout"); err != nil {
		t.Fatalf("finish failed shard: %v", err)
	}
	if repository.assets["asset-1"].ClosedAt != nil {
		t.Fatalf("failed shard closed asset: %+v", repository.assets["asset-1"])
	}
	if shard.Coverage.Complete || shard.Coverage.FailureReason == "" {
		t.Fatalf("failed shard coverage = %+v", shard.Coverage)
	}
}

func TestOnlyCompleteAuthoritativeShardClosesMissingAssets(t *testing.T) {
	t.Parallel()

	repository := newInventoryRepository()
	repository.scopes["scope-1"] = asset.Scope{ID: "scope-1", ConnectionID: "connection-1", Kind: asset.ScopeRegion}
	repository.assets["asset-seen"] = activeAsset("asset-seen")
	repository.assets["asset-missing"] = activeAsset("asset-missing")
	repository.observations["asset-seen"] = []asset.Observation{{AssetID: "asset-seen", ScanShardID: "shard-3"}}
	repository.runs["run-3"] = asset.ScanRun{ID: "run-3", ConnectionID: "connection-1"}
	finishedAt := time.Date(2026, 7, 13, 2, 0, 0, 0, time.UTC)
	service := inventory.NewService(repository, inventory.WithClock(func() time.Time { return finishedAt }))
	shard := asset.ScanShard{ID: "shard-3", ScanRunID: "run-3", ScopeID: "scope-1", ResourceKindID: "kind-1", Authoritative: true}

	if err := service.FinishShard(context.Background(), &shard, asset.ShardSucceeded, ""); err != nil {
		t.Fatalf("finish authoritative shard: %v", err)
	}
	if repository.assets["asset-seen"].ClosedAt != nil {
		t.Fatal("seen asset was closed")
	}
	missing := repository.assets["asset-missing"]
	if missing.ClosedAt == nil || !missing.ClosedAt.Equal(finishedAt) {
		t.Fatalf("missing asset not closed at shard completion: %+v", missing)
	}
}

func TestAuthoritativeShardFailsClosedForCrossConnectionScope(t *testing.T) {
	t.Parallel()

	repository := newInventoryRepository()
	repository.scopes["scope-foreign"] = asset.Scope{ID: "scope-foreign", ConnectionID: "connection-b", Kind: asset.ScopeRegion}
	foreignScope := activeAsset("asset-foreign-scope")
	foreignScope.ScopeID = "scope-foreign"
	repository.assets[foreignScope.ID] = foreignScope
	repository.runs["run-cross-connection"] = asset.ScanRun{ID: "run-cross-connection", ConnectionID: "connection-1"}
	service := inventory.NewService(repository)
	shard := asset.ScanShard{ID: "shard-cross-connection", ScanRunID: "run-cross-connection", ScopeID: "scope-foreign", ResourceKindID: "kind-1", Authoritative: true}

	if err := service.FinishShard(context.Background(), &shard, asset.ShardSucceeded, ""); err == nil {
		t.Fatal("cross-connection scope did not fail closed")
	}
	if repository.assets[foreignScope.ID].ClosedAt != nil {
		t.Fatalf("cross-connection scope closed asset: %+v", repository.assets[foreignScope.ID])
	}
}

func TestAuthoritativeRootShardClosesMissingKindAcrossChildScopesOnlyForItsConnection(t *testing.T) {
	t.Parallel()

	repository := newInventoryRepository()
	repository.scopes["scope-account"] = asset.Scope{ID: "scope-account", ConnectionID: "connection-a", Kind: asset.ScopeAccount}
	repository.scopes["scope-region-a"] = asset.Scope{ID: "scope-region-a", ConnectionID: "connection-a", ParentID: "scope-account", Kind: asset.ScopeRegion}
	repository.scopes["scope-region-b"] = asset.Scope{ID: "scope-region-b", ConnectionID: "connection-a", ParentID: "scope-account", Kind: asset.ScopeRegion}
	seen := activeAsset("asset-seen")
	seen.Identity.ConnectionID = "connection-a"
	seen.ScopeID = "scope-region-a"
	missing := activeAsset("asset-missing")
	missing.Identity.ConnectionID = "connection-a"
	missing.ScopeID = "scope-region-b"
	other := activeAsset("asset-other")
	other.Identity.ConnectionID = "connection-b"
	other.ScopeID = "scope-region-a"
	repository.assets[seen.ID] = seen
	repository.assets[missing.ID] = missing
	repository.assets[other.ID] = other
	repository.observations[seen.ID] = []asset.Observation{{AssetID: seen.ID, ScanShardID: "shard-root"}}
	repository.runs["run-root"] = asset.ScanRun{ID: "run-root", ConnectionID: "connection-a"}
	service := inventory.NewService(repository)
	shard := asset.ScanShard{ID: "shard-root", ScanRunID: "run-root", ScopeID: "scope-account", ResourceKindID: "kind-1", Authoritative: true}

	if err := service.FinishShard(context.Background(), &shard, asset.ShardSucceeded, ""); err != nil {
		t.Fatal(err)
	}
	if repository.assets[seen.ID].ClosedAt != nil || repository.assets[missing.ID].ClosedAt == nil || repository.assets[other.ID].ClosedAt != nil {
		t.Fatalf("seen=%+v missing=%+v other=%+v", repository.assets[seen.ID], repository.assets[missing.ID], repository.assets[other.ID])
	}
}

func TestAuthoritativeRegionalShardDoesNotCloseAssetsInSiblingScope(t *testing.T) {
	t.Parallel()

	repository := newInventoryRepository()
	repository.scopes["scope-account"] = asset.Scope{ID: "scope-account", ConnectionID: "connection-a", Kind: asset.ScopeAccount}
	repository.scopes["scope-region-a"] = asset.Scope{ID: "scope-region-a", ConnectionID: "connection-a", ParentID: "scope-account", Kind: asset.ScopeRegion}
	repository.scopes["scope-region-b"] = asset.Scope{ID: "scope-region-b", ConnectionID: "connection-a", ParentID: "scope-account", Kind: asset.ScopeRegion}
	seen := activeAsset("asset-seen")
	seen.Identity.ConnectionID = "connection-a"
	seen.ScopeID = "scope-region-a"
	missing := activeAsset("asset-missing")
	missing.Identity.ConnectionID = "connection-a"
	missing.ScopeID = "scope-region-a"
	sibling := activeAsset("asset-sibling")
	sibling.Identity.ConnectionID = "connection-a"
	sibling.ScopeID = "scope-region-b"
	repository.assets[seen.ID] = seen
	repository.assets[missing.ID] = missing
	repository.assets[sibling.ID] = sibling
	repository.observations[seen.ID] = []asset.Observation{{AssetID: seen.ID, ScanShardID: "shard-region-a"}}
	repository.runs["run-region-a"] = asset.ScanRun{ID: "run-region-a", ConnectionID: "connection-a"}
	service := inventory.NewService(repository)
	shard := asset.ScanShard{ID: "shard-region-a", ScanRunID: "run-region-a", ScopeID: "scope-region-a", ResourceKindID: "kind-1", Authoritative: true}

	if err := service.FinishShard(context.Background(), &shard, asset.ShardSucceeded, ""); err != nil {
		t.Fatal(err)
	}
	if repository.assets[seen.ID].ClosedAt != nil || repository.assets[missing.ID].ClosedAt == nil || repository.assets[sibling.ID].ClosedAt != nil {
		t.Fatalf("seen=%+v missing=%+v sibling=%+v", repository.assets[seen.ID], repository.assets[missing.ID], repository.assets[sibling.ID])
	}
}

func TestLowerPriorityIndexCannotOverrideProductObservation(t *testing.T) {
	t.Parallel()

	repository := newInventoryRepository()
	connection := asset.CloudConnection{ID: "connection-1", Provider: asset.ProviderAliCloud, Partition: "aliyun"}
	kind := asset.ResourceKind{ID: "kind-1", Provider: asset.ProviderAliCloud, NativeType: "ACS::ECS::Instance", Capabilities: asset.CapabilitySet{asset.CapabilityIndexed, asset.CapabilityDetailed}}
	identity, err := asset.NewIdentity("alicloud", "aliyun", connection.ID, kind.NativeType, "i-1")
	if err != nil {
		t.Fatal(err)
	}
	assetID := asset.AssetID("ast-existing-resource")
	productTime := time.Date(2026, 7, 13, 3, 0, 0, 0, time.UTC)
	repository.assets[assetID] = asset.Asset{ID: assetID, Identity: identity, ScopeID: "scope-1", ResourceKindID: kind.ID, CurrentObservationID: "product-observation", Name: "product-name", FirstSeenAt: productTime, LastSeenAt: productTime}
	repository.observations[assetID] = []asset.Observation{{ID: "product-observation", AssetID: assetID, ScanRunID: "run-1", ScanShardID: "product-shard", ObservedAt: productTime, Source: "product-api", Priority: 100}}
	service := inventory.NewService(repository, inventory.WithIDGenerator(sequenceIDs("index-observation")))
	shard := asset.ScanShard{ID: "index-shard", ScanRunID: "run-2", Provider: asset.ProviderAliCloud, ScopeID: "scope-1", ResourceKindID: kind.ID, Source: "resource-index"}
	batch := contracts.InventoryBatch{Items: []contracts.InventoryItem{{NativeType: kind.NativeType, NativeID: "i-1", ResourceKind: kind, Name: "stale-index-name", Raw: map[string]any{"InstanceId": "i-1"}}}}

	if err := service.ProjectBatch(context.Background(), &shard, connection, batch, inventory.ProjectionOptions{ObservedAt: productTime.Add(time.Minute), Priority: 10}); err != nil {
		t.Fatalf("project lower priority index: %v", err)
	}
	projected := repository.assets[assetID]
	if projected.CurrentObservationID != "product-observation" || projected.Name != "product-name" {
		t.Fatalf("lower priority observation replaced projection: %+v", projected)
	}
	if len(repository.observations[assetID]) != 2 {
		t.Fatalf("lower priority observation was not retained: %+v", repository.observations[assetID])
	}
}

func activeAsset(id asset.AssetID) asset.Asset {
	return asset.Asset{ID: id, Identity: asset.Identity{ConnectionID: "connection-1"}, ScopeID: "scope-1", ResourceKindID: "kind-1", FirstSeenAt: time.Now(), LastSeenAt: time.Now()}
}

func sequenceIDs(ids ...string) func() string {
	index := 0
	return func() string {
		if index >= len(ids) {
			panic(errors.New("test ID sequence exhausted"))
		}
		id := ids[index]
		index++
		return id
	}
}
