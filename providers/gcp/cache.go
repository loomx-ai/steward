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
	locations   ttlCache[[]string]
	reads       sharedReads
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
}

type sharedRead struct {
	done      chan struct{}
	body      []byte
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

func (s *sharedReads) get(ctx context.Context, key string, fetch func(context.Context) (contracts.InvocationResult, error)) (contracts.InvocationResult, error) {
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
		result, err := fetch(context.WithoutCancel(ctx))
		if err == nil {
			entry.body, err = json.Marshal(result.Data)
		}
		entry.requestID, entry.err, entry.expires = result.RequestID, err, time.Now().Add(sharedReadTTL)
		s.mu.Lock()
		if s.entries[key] == entry {
			if err != nil || s.bytes+len(entry.body) > sharedReadBudget {
				// Waiting shards still receive this page; later ones read their own.
				delete(s.entries, key)
			} else {
				s.bytes += len(entry.body)
			}
		}
		s.mu.Unlock()
		close(entry.done)
	}
	select {
	case <-entry.done:
	case <-ctx.Done():
		return contracts.InvocationResult{}, ctx.Err()
	}
	if entry.err != nil {
		return contracts.InvocationResult{}, entry.err
	}
	data := map[string]any{}
	decoder := json.NewDecoder(bytes.NewReader(entry.body))
	decoder.UseNumber()
	if err := decoder.Decode(&data); err != nil {
		return contracts.InvocationResult{}, err
	}
	return contracts.InvocationResult{Data: data, RequestID: entry.requestID, NextToken: text(data["nextPageToken"])}, nil
}

// drop removes an entry; the caller holds s.mu.
func (s *sharedReads) drop(key string, entry *sharedRead) {
	if s.entries[key] == entry {
		delete(s.entries, key)
		s.bytes -= len(entry.body)
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
