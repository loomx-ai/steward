package azure

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
)

// Concurrent shards load one observation; a failure is never cached; the
// cache stays bounded.
func TestProductScanShareSingleflightFailureAndBound(t *testing.T) {
	var s productScanCache
	key := productScanKey{run: "scan", name: "a"}
	var loads atomic.Int32
	release := make(chan struct{})
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			value, err := s.share(context.Background(), key, func(any) bool { return true }, func() (any, error) {
				loads.Add(1)
				<-release
				return "value", nil
			})
			if err != nil || value != "value" {
				t.Error(value, err)
			}
		})
	}
	for loads.Load() == 0 {
	}
	close(release)
	wg.Wait()
	if loads.Load() != 1 {
		t.Fatal("shards loaded separately", loads.Load())
	}
	failing := productScanKey{run: "scan", name: "failing"}
	for range 2 {
		if _, err := s.share(context.Background(), failing, func(any) bool { return true }, func() (any, error) { loads.Add(1); return nil, errors.New("read failed") }); err == nil {
			t.Fatal("failure was served")
		}
	}
	if loads.Load() != 3 {
		t.Fatal("a failure was cached", loads.Load())
	}
	if _, err := s.share(context.Background(), key, func(any) bool { return false }, func() (any, error) { loads.Add(1); return "fresh", nil }); err != nil || loads.Load() != 4 {
		t.Fatal("a rejected observation was served", err)
	}
	for i := range 2 * productSharedLimit {
		_, _ = s.share(context.Background(), productScanKey{run: "scan", name: string(rune('A' + i))}, func(any) bool { return true }, func() (any, error) { return i, nil })
	}
	if len(s.shared) > productSharedLimit {
		t.Fatal("shared observations are unbounded", len(s.shared))
	}
}
