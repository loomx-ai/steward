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
// memory. Scopes, natural scope keys and observations do not change inside it
// except by inserts of new rows, so successful reads are remembered. Assets and
// their current observations are read for the whole batch up front; once an
// asset is written, every later read of its native key goes to the
// transaction again.
type batchRepository struct {
	persistence.InventoryRepository
	kinds        map[asset.ResourceKindID]asset.ResourceKind
	scopes       map[asset.ScopeID]asset.Scope
	naturalKeys  map[naturalScopeKey]asset.Scope
	prefetched   map[asset.Identity]bool
	assets       map[asset.Identity]asset.Asset
	observations map[asset.ObservationID]asset.Observation
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

func (r *batchRepository) PutAsset(ctx context.Context, value asset.Asset) error {
	delete(r.prefetched, nativeKey(value.Identity))
	return r.InventoryRepository.PutAsset(ctx, value)
}

func (r *batchRepository) GetObservation(ctx context.Context, id asset.ObservationID) (asset.Observation, error) {
	if value, ok := r.observations[id]; ok {
		return value, nil
	}
	return r.InventoryRepository.GetObservation(ctx, id)
}
