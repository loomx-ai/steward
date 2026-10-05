package gcp

import (
	"context"
	"sync"
	"testing"
	"testing/synctest"
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
