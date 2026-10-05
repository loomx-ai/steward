package gcp

import (
	"bytes"
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

// clientCache is shared by value copies of a client that read another project,
// so every key includes the project. A client without one reads live.
type clientCache struct {
	fingerprint [32]byte
	parents     ttlCache[[]contracts.InventoryItem]
	targets     ttlCache[productTargetSet]
	locations   ttlCache[[]string]
	reads       sharedReads
	// inflight shares an action-time GET only while it is in flight.
	inflight sharedReads
}

// ttlCache keeps successful reads for a bounded time; failures are never kept.
type ttlCache[V any] struct {
	mu      sync.Mutex
	entries map[string]ttlEntry[V]
}

type ttlEntry[V any] struct {
	value   V
	expires time.Time
}

// get returns a live cached value unless refresh is set, and otherwise loads
// and keeps it for ttl. Callers must not mutate a returned value.
func (c *ttlCache[V]) get(key string, ttl time.Duration, refresh bool, load func() (V, error)) (V, error) {
	now := time.Now()
	c.mu.Lock()
	entry, ok := c.entries[key]
	c.mu.Unlock()
	if ok && !refresh && now.Before(entry.expires) {
		return entry.value, nil
	}
	value, err := load()
	if err != nil {
		return value, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = map[string]ttlEntry[V]{}
	}
	for cached, entry := range c.entries {
		if !now.Before(entry.expires) {
			delete(c.entries, cached)
		}
	}
	c.entries[key] = ttlEntry[V]{value: value, expires: now.Add(ttl)}
	return value, nil
}

// Project-wide listings (Cloud Asset Inventory, aggregatedList and
// locations/- methods) return the same pages to every region shard of a scan,
// which each keep only their own scope. sharedReads lets the shards of one scan
// share those pages: concurrent shards wait for one fetch, a failed fetch fails
// every waiting shard and is never kept, and pages are never shared across
// scans. Bodies are kept encoded so every shard decodes its own copy.
const (
	sharedReadTTL    = 15 * time.Minute
	sharedReadBudget = 128 << 20
)

type sharedReads struct {
	mu      sync.Mutex
	entries map[string]*sharedRead
	bytes   int
	// inflightOnly drops a read once it finishes, so callers arriving while it
	// is in flight share it and every later caller reads live.
	inflightOnly bool
}

type sharedRead struct {
	done chan struct{}
	// parts holds the encoded page, or for a split page one entry per part.
	parts     map[string][]byte
	size      int
	requestID string
	err       error
	expires   time.Time
}

type sharedReadScan struct{}

// withSharedReads marks GETs made with ctx as shareable by the shards of scan.
func withSharedReads(ctx context.Context, scan asset.ScanRunID) context.Context {
	if scan == "" {
		return ctx
	}
	return context.WithValue(ctx, sharedReadScan{}, scan)
}

type inflightReads struct{}

// withInflightReads marks GETs made with ctx as joinable while an identical
// GET of the same client is in flight. Results are never reused once finished,
// so action-time preflight reads stay live.
func withInflightReads(ctx context.Context) context.Context {
	return context.WithValue(ctx, inflightReads{}, true)
}

func (s *sharedReads) get(ctx context.Context, key string, fetch func(context.Context) (contracts.InvocationResult, error)) (contracts.InvocationResult, error) {
	entry, err := s.load(ctx, key, func(ctx context.Context) (map[string]any, string, error) {
		result, err := fetch(ctx)
		return map[string]any{"": result.Data}, result.RequestID, err
	})
	if err != nil {
		return contracts.InvocationResult{}, err
	}
	data := map[string]any{}
	if err := decodeShared(entry.parts[""], &data); err != nil {
		return contracts.InvocationResult{}, err
	}
	return contracts.InvocationResult{Data: data, RequestID: entry.requestID, NextToken: text(data["nextPageToken"])}, nil
}

// getParts shares a page that fetch splits into named parts, so each shard
// decodes only the parts it keeps. A part the page does not have is nil.
func (s *sharedReads) getParts(ctx context.Context, key string, fetch func(context.Context) (map[string]any, string, error), names ...string) ([]any, string, error) {
	entry, err := s.load(ctx, key, fetch)
	if err != nil {
		return nil, "", err
	}
	values := make([]any, len(names))
	for index, name := range names {
		if body, ok := entry.parts[name]; ok {
			if err := decodeShared(body, &values[index]); err != nil {
				return nil, "", err
			}
		}
	}
	return values, entry.requestID, nil
}

// load returns the finished shared entry for key, fetching it when no live one
// exists. fetch returns the page parts and its request ID.
func (s *sharedReads) load(ctx context.Context, key string, fetch func(context.Context) (map[string]any, string, error)) (*sharedRead, error) {
	now := time.Now()
	s.mu.Lock()
	entry := s.entries[key]
	owner := entry == nil || (entry.finished() && !now.Before(entry.expires))
	if owner {
		for cached, value := range s.entries {
			if value.finished() && !now.Before(value.expires) {
				s.drop(cached, value)
			}
		}
		if s.entries == nil {
			s.entries = map[string]*sharedRead{}
		}
		entry = &sharedRead{done: make(chan struct{})}
		s.entries[key] = entry
	}
	s.mu.Unlock()
	if owner {
		// The page serves other shards too, so one shard's cancellation must
		// not fail it for them.
		parts, requestID, err := fetch(context.WithoutCancel(ctx))
		if err == nil {
			entry.parts = make(map[string][]byte, len(parts))
			for name, value := range parts {
				var body []byte
				if body, err = json.Marshal(value); err != nil {
					break
				}
				entry.parts[name] = body
				entry.size += len(body)
			}
		}
		entry.requestID, entry.err, entry.expires = requestID, err, time.Now().Add(sharedReadTTL)
		s.mu.Lock()
		if s.entries[key] == entry {
			if err != nil || s.inflightOnly || s.bytes+entry.size > sharedReadBudget {
				// Waiting shards still receive this page; later ones read their own.
				delete(s.entries, key)
			} else {
				s.bytes += entry.size
			}
		}
		s.mu.Unlock()
		close(entry.done)
	}
	select {
	case <-entry.done:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return entry, entry.err
}

func decodeShared(body []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	return decoder.Decode(target)
}

// drop removes an entry; the caller holds s.mu.
func (s *sharedReads) drop(key string, entry *sharedRead) {
	if s.entries[key] == entry {
		delete(s.entries, key)
		s.bytes -= entry.size
	}
}

func (r *sharedRead) finished() bool {
	select {
	case <-r.done:
		return true
	default:
		return false
	}
}
