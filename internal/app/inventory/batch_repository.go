package inventory

import (
	"context"
	"reflect"
	"strings"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/persistence"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

// batchRepository serves the reads of one ProjectBatch transaction from
// memory and holds its observation, asset and change writes until flush
// writes each kind together. Scopes, natural scope keys and observations do
// not change inside it except by inserts of new rows, so successful reads are
// remembered. Assets and their current observations are read for the whole
// batch up front, and later reads of those native keys see the held writes.
type batchRepository struct {
	persistence.InventoryRepository
	kinds        map[asset.ResourceKindID]asset.ResourceKind
	scopes       map[asset.ScopeID]asset.Scope
	naturalKeys  map[naturalScopeKey]asset.Scope
	prefetched   map[asset.Identity]bool
	assets       map[asset.Identity]asset.Asset
	assetKeys    map[asset.AssetID]asset.Identity
	observations map[asset.ObservationID]asset.Observation

	pendingObservations []asset.Observation
	pendingAssets       []asset.Asset
	pendingChanges      []asset.AssetChange
}

type naturalScopeKey struct {
	connectionID asset.ConnectionID
	kind         asset.ScopeKind
	nativeID     string
}

func newBatchRepository(ctx context.Context, repository persistence.InventoryRepository, connection asset.CloudConnection, items []contracts.InventoryItem) (*batchRepository, error) {
	batch := &batchRepository{
		InventoryRepository: repository,
		kinds:               make(map[asset.ResourceKindID]asset.ResourceKind),
		scopes:              make(map[asset.ScopeID]asset.Scope),
		naturalKeys:         make(map[naturalScopeKey]asset.Scope),
		prefetched:          make(map[asset.Identity]bool, len(items)),
		assets:              make(map[asset.Identity]asset.Asset, len(items)),
		assetKeys:           make(map[asset.AssetID]asset.Identity, len(items)),
		observations:        make(map[asset.ObservationID]asset.Observation, len(items)),
	}
	identities := make([]asset.Identity, 0, len(items))
	for _, item := range items {
		// Items projectItem rejects are simply not prefetched.
		identity, err := asset.NewIdentity(string(connection.Provider), connection.Partition, connection.ID, item.ResourceKind.NativeType, item.NativeID)
		if err != nil {
			continue
		}
		identities = append(identities, identity)
	}
	values, err := repository.ListAssetsByNativeIdentities(ctx, identities)
	if err != nil {
		return nil, err
	}
	for _, identity := range identities {
		batch.prefetched[nativeKey(identity)] = true
	}
	observationIDs := make([]asset.ObservationID, 0, len(values))
	for _, value := range values {
		batch.assets[identityKey(value.Identity)] = value
		batch.assetKeys[value.ID] = identityKey(value.Identity)
		observationIDs = append(observationIDs, value.CurrentObservationID)
	}
	observations, err := repository.ListObservationsByIDs(ctx, observationIDs)
	if err != nil {
		return nil, err
	}
	for _, observation := range observations {
		batch.observations[observation.ID] = observation
	}
	return batch, nil
}

func nativeKey(identity asset.Identity) asset.Identity {
	identity.ScopeKey = ""
	return identity
}

func identityKey(identity asset.Identity) asset.Identity {
	identity.ScopeKey = strings.TrimSpace(identity.ScopeKey)
	return identity
}

func (r *batchRepository) PutResourceKind(ctx context.Context, kind asset.ResourceKind) error {
	if previous, ok := r.kinds[kind.ID]; ok && reflect.DeepEqual(previous, kind) {
		return nil
	}
	if err := r.InventoryRepository.PutResourceKind(ctx, kind); err != nil {
		return err
	}
	r.kinds[kind.ID] = kind
	return nil
}

func (r *batchRepository) GetScope(ctx context.Context, id asset.ScopeID) (asset.Scope, error) {
	if value, ok := r.scopes[id]; ok {
		return value, nil
	}
	value, err := r.InventoryRepository.GetScope(ctx, id)
	if err == nil {
		r.scopes[id] = value
	}
	return value, err
}

func (r *batchRepository) GetScopeByNaturalKey(ctx context.Context, connectionID asset.ConnectionID, kind asset.ScopeKind, nativeID string) (asset.Scope, error) {
	key := naturalScopeKey{connectionID, kind, strings.TrimSpace(nativeID)}
	if value, ok := r.naturalKeys[key]; ok {
		return value, nil
	}
	value, err := r.InventoryRepository.GetScopeByNaturalKey(ctx, connectionID, kind, nativeID)
	if err == nil {
		r.naturalKeys[key] = value
	}
	return value, err
}

func (r *batchRepository) PutScope(ctx context.Context, scope asset.Scope) error {
	delete(r.scopes, scope.ID)
	return r.InventoryRepository.PutScope(ctx, scope)
}

func (r *batchRepository) GetAssetByIdentity(ctx context.Context, identity asset.Identity) (asset.Asset, error) {
	if !r.prefetched[nativeKey(identity)] {
		return r.InventoryRepository.GetAssetByIdentity(ctx, identity)
	}
	value, ok := r.assets[identityKey(identity)]
	if !ok {
		return asset.Asset{}, persistence.ErrNotFound
	}
	return value, nil
}

// PutAsset holds the write when later reads of the asset's native key are
// served from memory. Any other write goes out at once, after the held ones.
func (r *batchRepository) PutAsset(ctx context.Context, value asset.Asset) error {
	if !r.prefetched[nativeKey(value.Identity)] {
		if err := r.flush(ctx); err != nil {
			return err
		}
		return r.InventoryRepository.PutAsset(ctx, value)
	}
	if previous, ok := r.assetKeys[value.ID]; ok {
		delete(r.assets, previous)
	}
	r.assets[identityKey(value.Identity)] = value
	r.assetKeys[value.ID] = identityKey(value.Identity)
	r.pendingAssets = append(r.pendingAssets, value)
	return nil
}

func (r *batchRepository) PutAssets(ctx context.Context, values []asset.Asset) error {
	for _, value := range values {
		if err := r.PutAsset(ctx, value); err != nil {
			return err
		}
	}
	return nil
}

func (r *batchRepository) AppendObservations(_ context.Context, observations []asset.Observation) error {
	for _, observation := range observations {
		r.observations[observation.ID] = observation
	}
	r.pendingObservations = append(r.pendingObservations, observations...)
	return nil
}

func (r *batchRepository) RecordAssetChanges(_ context.Context, changes []asset.AssetChange) error {
	r.pendingChanges = append(r.pendingChanges, changes...)
	return nil
}

func (r *batchRepository) GetObservation(ctx context.Context, id asset.ObservationID) (asset.Observation, error) {
	if value, ok := r.observations[id]; ok {
		return value, nil
	}
	return r.InventoryRepository.GetObservation(ctx, id)
}

// flush writes the held observations, assets and changes, in that order and
// one statement batch each.
func (r *batchRepository) flush(ctx context.Context) error {
	if err := r.InventoryRepository.AppendObservations(ctx, r.pendingObservations); err != nil {
		return err
	}
	if err := r.InventoryRepository.PutAssets(ctx, r.pendingAssets); err != nil {
		return err
	}
	if err := r.InventoryRepository.RecordAssetChanges(ctx, r.pendingChanges); err != nil {
		return err
	}
	r.pendingObservations, r.pendingAssets, r.pendingChanges = nil, nil, nil
	return nil
}
