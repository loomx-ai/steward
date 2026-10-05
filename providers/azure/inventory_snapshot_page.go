package azure

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

// inventorySnapshot is one stability-checked (observed twice, unchanged)
// listing of a snapshot source across the whole subscription, with the known
// IDs it proved absent and the fingerprint that binds its continuation cursors.
type inventorySnapshot struct {
	items       []contracts.InventoryItem
	absent      []string
	provenance  string
	fingerprint string
}

// inventorySnapshotTTL bounds how long the shards of one scan reuse a
// snapshot; without it every region shard and page re-observes the whole
// subscription twice.
const inventorySnapshotTTL = 10 * time.Minute

// productSharedLimit bounds the shared observations one runtime holds. The
// entry closest to expiry is dropped first; a dropped one is observed again.
const productSharedLimit = 64

type productShared struct {
	done  chan struct{}
	value any
	err   error
	// expires is zero while the observation runs.
	expires time.Time
}

// inventorySnapshotPage serves one page of the snapshot observe builds for the
// whole subscription, filtered to the request's scope. The shards of one scan
// share an observation (one observes while the others wait; a failure is never
// cached). A later page whose cursor no longer matches the cached observation
// observes again, so a changed collection still fails the cursor with changed.
func (r *Runtime) inventorySnapshotPage(ctx context.Context, c *client, request contracts.InventoryRequest, cursor productCursor, changed string, observe func(contracts.InventoryRequest) (inventorySnapshot, error)) (contracts.InventoryBatch, error) {
	return r.snapshotPage(ctx, c, request, cursor, changed, false, nil, observe)
}

// scopedSnapshotPage serves a source whose observation, absence proof
// included, depends on the request's scope: the observation is cached per
// scope and its items are served unfiltered. bind is whatever else the
// observation depends on.
func (r *Runtime) scopedSnapshotPage(ctx context.Context, c *client, request contracts.InventoryRequest, cursor productCursor, changed string, bind any, observe func() (inventorySnapshot, error)) (contracts.InventoryBatch, error) {
	return r.snapshotPage(ctx, c, request, cursor, changed, true, bind, func(contracts.InventoryRequest) (inventorySnapshot, error) { return observe() })
}

func (r *Runtime) snapshotPage(ctx context.Context, c *client, request contracts.InventoryRequest, cursor productCursor, changed string, scoped bool, bind any, observe func(contracts.InventoryRequest) (inventorySnapshot, error)) (batch contracts.InventoryBatch, err error) {
	observed := request
	if !scoped {
		observed.Scope = asset.Scope{Kind: asset.ScopeSubscription, NativeID: c.subscription}
	}
	observed.Cursor, observed.Limit = "", 0
	// Known metadata can be large. A shard's pages carry the same metadata, so
	// its first page digests it once and the cursor carries the digest.
	known := cursor.Known
	if request.Cursor == "" {
		known = c.privateConfiguration(map[string]any{"metadata": request.KnownNativeMetadata})
	}
	boundary := observed
	boundary.KnownNativeMetadata = nil
	// The key binds source, kind, network target, known IDs and metadata, and
	// bundle revision; a scope only when the observation depends on it.
	key := productScanKey{run: request.ScanRunID, connection: request.ConnectionID, credential: c.fingerprint, name: "snapshot\x00" + c.privateConfiguration(map[string]any{"request": boundary, "known": known, "revision": r.bundle.Revision, "bind": bind})}
	page := func(snapshot inventorySnapshot) string {
		bound, _ := json.Marshal([]string{key.name, snapshot.fingerprint, string(request.Scope.Kind), request.Scope.NativeID})
		return fmt.Sprintf("%x", sha256.Sum256(bound))
	}
	var snapshot inventorySnapshot
	if request.ScanRunID == "" {
		snapshot, err = observe(observed)
	} else {
		var shared any
		shared, err = r.productScan.share(ctx, key, func(cached any) bool {
			return request.Cursor == "" || page(cached.(inventorySnapshot)) == cursor.Fingerprint
		}, func() (any, error) { return observe(observed) })
		snapshot, _ = shared.(inventorySnapshot)
	}
	if err != nil {
		return batch, err
	}
	// ponytail: every page filters the whole observation; cache per-scope
	// slices if snapshot sources grow past ~100k items per kind.
	items := snapshot.items
	if !scoped {
		items = []contracts.InventoryItem{}
		for _, item := range snapshot.items {
			if productScopeMatches(request, item) {
				items = append(items, item)
			}
		}
	}
	fingerprint := page(snapshot)
	if request.Cursor != "" && (cursor.Fingerprint != fingerprint || cursor.Target >= len(items)) {
		return batch, serviceDenied(changed)
	}
	limit := request.Limit
	if limit <= 0 {
		limit = 1000
	}
	end := cursor.Target + min(limit, len(items)-cursor.Target)
	batch = contracts.InventoryBatch{Items: items[cursor.Target:end], Complete: end == len(items), RequestID: snapshot.provenance}
	if batch.Complete {
		batch.AbsentNativeIDs = snapshot.absent
		return batch, nil
	}
	cursor.Fingerprint, cursor.Target, cursor.Known = fingerprint, end, known
	encoded, _ := json.Marshal(cursor)
	batch.NextCursor = base64.RawURLEncoding.EncodeToString(encoded)
	return batch, nil
}

// share returns the scan's cached observation for key when reuse accepts it,
// and otherwise loads once for every caller waiting on key. A failure is never
// cached; a caller whose wait ends in another's failure loads again.
func (s *productScanCache) share(ctx context.Context, key productScanKey, reuse func(any) bool, load func() (any, error)) (any, error) {
	for {
		s.mu.Lock()
		now := time.Now()
		entry := s.shared[key]
		if entry != nil && !entry.expires.IsZero() && (!now.Before(entry.expires) || !reuse(entry.value)) {
			entry = nil
		}
		if entry == nil {
			if s.shared == nil {
				s.shared = map[productScanKey]*productShared{}
			}
			var oldest productScanKey
			var oldestExpires time.Time
			for cached, old := range s.shared {
				if old.expires.IsZero() {
					continue
				}
				if !now.Before(old.expires) {
					delete(s.shared, cached)
				} else if oldestExpires.IsZero() || old.expires.Before(oldestExpires) {
					oldest, oldestExpires = cached, old.expires
				}
			}
			if !oldestExpires.IsZero() && len(s.shared) >= productSharedLimit {
				delete(s.shared, oldest)
			}
			entry = &productShared{done: make(chan struct{})}
			s.shared[key] = entry
			s.mu.Unlock()
			func() {
				defer func() {
					s.mu.Lock()
					if entry.err != nil {
						if s.shared[key] == entry {
							delete(s.shared, key)
						}
					} else {
						entry.expires = time.Now().Add(inventorySnapshotTTL)
					}
					s.mu.Unlock()
					close(entry.done)
				}()
				entry.err = errors.New("Azure shared inventory observation did not finish")
				entry.value, entry.err = load()
			}()
			return entry.value, entry.err
		}
		s.mu.Unlock()
		select {
		case <-entry.done:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		// A just-finished observation is served as is.
		if entry.err == nil {
			return entry.value, nil
		}
	}
}

// forget drops key's finished observation, so the next caller loads afresh.
func (s *productScanCache) forget(key productScanKey) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if entry := s.shared[key]; entry != nil && !entry.expires.IsZero() {
		delete(s.shared, key)
	}
}
