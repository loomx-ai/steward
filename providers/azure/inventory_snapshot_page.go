package azure

import (
	"encoding/base64"
	"encoding/json"
	"sync"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

// inventorySnapshot is one stability-checked (observed twice, unchanged)
// listing of a snapshot source, with the known IDs it proved absent and the
// fingerprint that binds its continuation cursors.
type inventorySnapshot struct {
	items       []contracts.InventoryItem
	absent      []string
	provenance  string
	fingerprint string
}

// inventorySnapshotTTL bounds how long later pages of one scan reuse the
// snapshot its first page checked; without it every page re-observes the
// whole collection twice.
const inventorySnapshotTTL = 10 * time.Minute

type inventorySnapshotKey struct {
	run        asset.ScanRunID
	connection asset.ConnectionID
	credential [32]byte
	// request binds source, kind, scope, known IDs and bundle revision.
	request     string
	fingerprint string
}

type cachedInventorySnapshot struct {
	snapshot inventorySnapshot
	expires  time.Time
}

var inventorySnapshots struct {
	sync.Mutex
	entries map[inventorySnapshotKey]cachedInventorySnapshot
}

// inventorySnapshotPage serves one page of the snapshot observe builds. The
// first page always observes; a later page of the same scan run slices the
// snapshot its first page observed when it is still cached under the
// cursor's fingerprint, and otherwise observes again so a changed collection
// still fails the cursor with changed.
func (r *Runtime) inventorySnapshotPage(c *client, request contracts.InventoryRequest, cursor productCursor, changed string, observe func() (inventorySnapshot, error)) (batch contracts.InventoryBatch, err error) {
	boundary := request
	boundary.Cursor, boundary.Limit = "", 0
	key := inventorySnapshotKey{run: request.ScanRunID, connection: request.ConnectionID, credential: c.fingerprint, request: c.privateConfiguration(map[string]any{"request": boundary, "revision": r.bundle.Revision}), fingerprint: cursor.Fingerprint}
	now := time.Now()
	inventorySnapshots.Lock()
	entry, cached := inventorySnapshots.entries[key]
	inventorySnapshots.Unlock()
	snapshot := entry.snapshot
	if request.Cursor == "" || request.ScanRunID == "" || !cached || !now.Before(entry.expires) {
		if snapshot, err = observe(); err != nil {
			return batch, err
		}
	}
	if request.Cursor != "" && (cursor.Fingerprint != snapshot.fingerprint || cursor.Target >= len(snapshot.items)) {
		return batch, serviceDenied(changed)
	}
	limit := request.Limit
	if limit <= 0 {
		limit = 1000
	}
	end := cursor.Target + min(limit, len(snapshot.items)-cursor.Target)
	batch = contracts.InventoryBatch{Items: snapshot.items[cursor.Target:end], Complete: end == len(snapshot.items), RequestID: snapshot.provenance}
	key.fingerprint = snapshot.fingerprint
	inventorySnapshots.Lock()
	defer inventorySnapshots.Unlock()
	for cached, entry := range inventorySnapshots.entries {
		if !now.Before(entry.expires) {
			delete(inventorySnapshots.entries, cached)
		}
	}
	if batch.Complete {
		batch.AbsentNativeIDs = snapshot.absent
		delete(inventorySnapshots.entries, key)
		return batch, nil
	}
	if request.ScanRunID != "" {
		if inventorySnapshots.entries == nil {
			inventorySnapshots.entries = map[inventorySnapshotKey]cachedInventorySnapshot{}
		}
		inventorySnapshots.entries[key] = cachedInventorySnapshot{snapshot: snapshot, expires: now.Add(inventorySnapshotTTL)}
	}
	cursor.Fingerprint, cursor.Target = snapshot.fingerprint, end
	encoded, _ := json.Marshal(cursor)
	batch.NextCursor = base64.RawURLEncoding.EncodeToString(encoded)
	return batch, nil
}
