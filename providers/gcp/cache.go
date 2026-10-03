package gcp

import (
	"sync"
	"time"

	"github.com/loomx-ai/steward/internal/provider/contracts"
)

// clientCache is shared by value copies of a client that read another project,
// so every key includes the project. A client without one reads live.
type clientCache struct {
	parents   ttlCache[[]contracts.InventoryItem]
	locations ttlCache[[]string]
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
