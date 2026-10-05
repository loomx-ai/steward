package gcp

import (
	"context"
	"maps"
	"slices"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

// In-flight reads share one fetch; a finished read is never reused.
func TestInflightReadsShareOnlyWhileInFlight(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		reads := sharedReads{inflightOnly: true}
		fetches := 0
		release := make(chan struct{})
		fetch := func(context.Context) (map[string]any, string, error) {
			fetches++
			<-release
			return map[string]any{"": "page"}, "", nil
		}
		var wg sync.WaitGroup
		for range 4 {
			wg.Go(func() {
				if _, err := reads.load(t.Context(), "key", fetch); err != nil {
					t.Error(err)
				}
			})
		}
		synctest.Wait() // every caller is waiting on the one fetch
		close(release)
		wg.Wait()
		if fetches != 1 {
			t.Fatalf("concurrent callers fetched %d times", fetches)
		}
		if _, err := reads.load(t.Context(), "key", fetch); err != nil || fetches != 2 || len(reads.entries) != 0 {
			t.Fatalf("finished read was reused: fetches=%d entries=%d err=%v", fetches, len(reads.entries), err)
		}
	})
}

// A full budget evicts the oldest finished pages for a new one; an in-flight
// read is never evicted and a failed read is never kept.
func TestSharedReadsEvictOldestFinishedPages(t *testing.T) {
	defer func(budget int) { sharedReadBudget = budget }(sharedReadBudget)
	sharedReadBudget = 20 // three 6-byte pages
	synctest.Test(t, func(t *testing.T) {
		var reads sharedReads
		page := func(context.Context) (map[string]any, string, error) {
			return map[string]any{"": "pg-0"}, "", nil // 6 encoded bytes
		}
		release := make(chan struct{})
		var wg sync.WaitGroup
		defer func() { close(release); wg.Wait() }()
		wg.Go(func() {
			if _, err := reads.load(t.Context(), "inflight", func(context.Context) (map[string]any, string, error) {
				<-release
				return map[string]any{"": "pg-9"}, "", nil
			}); err != nil {
				t.Error(err)
			}
		})
		synctest.Wait()
		for _, key := range []string{"a", "b", "c", "d", "e"} {
			if _, err := reads.load(t.Context(), key, page); err != nil {
				t.Fatal(err)
			}
			time.Sleep(time.Second) // distinct completion times
		}
		if reads.entries["a"] != nil || reads.entries["b"] != nil || reads.entries["c"] == nil || reads.entries["inflight"] == nil || reads.entries["e"] == nil || reads.bytes > sharedReadBudget {
			t.Fatalf("eviction kept wrong entries: %v bytes=%d", slices.Sorted(maps.Keys(reads.entries)), reads.bytes)
		}
		if _, err := reads.load(t.Context(), "failed", func(context.Context) (map[string]any, string, error) {
			return nil, "", context.DeadlineExceeded
		}); err == nil || reads.entries["failed"] != nil {
			t.Fatalf("failed read cached: %v", err)
		}
	})
}

// Recent shared reads reuse a page only while it is young; ordinary shared
// reads keep it for the scan TTL.
func TestRecentSharedReadsRelistAfterMaxAge(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var reads sharedReads
		fetches := 0
		fetch := func(context.Context) (map[string]any, string, error) {
			fetches++
			return map[string]any{"": fetches}, "", nil
		}
		recent := withRecentSharedReads(t.Context(), "scan")
		load := func(ctx context.Context) {
			if _, err := reads.load(ctx, "key", fetch); err != nil {
				t.Fatal(err)
			}
		}
		load(recent)
		time.Sleep(sharedListMaxAge - time.Second)
		load(recent)
		if fetches != 1 {
			t.Fatalf("young page relisted: %d", fetches)
		}
		time.Sleep(2 * time.Second)
		load(t.Context()) // an ordinary shared read still reuses it
		if fetches != 1 {
			t.Fatalf("ordinary shared read relisted: %d", fetches)
		}
		load(recent)
		if fetches != 2 || reads.bytes != len(`2`) {
			t.Fatalf("old page reused or its bytes kept: fetches=%d bytes=%d", fetches, reads.bytes)
		}
	})
}
