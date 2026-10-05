package azure

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
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
			value, err := s.share(context.Background(), &s.shared, key, func(any) bool { return true }, func() (any, error) {
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
		if _, err := s.share(context.Background(), &s.shared, failing, func(any) bool { return true }, func() (any, error) { loads.Add(1); return nil, errors.New("read failed") }); err == nil {
			t.Fatal("failure was served")
		}
	}
	if loads.Load() != 3 {
		t.Fatal("a failure was cached", loads.Load())
	}
	if _, err := s.share(context.Background(), &s.shared, key, func(any) bool { return false }, func() (any, error) { loads.Add(1); return "fresh", nil }); err != nil || loads.Load() != 4 {
		t.Fatal("a rejected observation was served", err)
	}
	for i := range 2 * productSharedLimit {
		_, _ = s.share(context.Background(), &s.shared, productScanKey{run: "scan", name: string(rune('A' + i))}, func(any) bool { return true }, func() (any, error) { return i, nil })
	}
	if len(s.shared) > productSharedLimit {
		t.Fatal("shared observations are unbounded", len(s.shared))
	}
}

// Callers waiting on a failed load get its failure instead of loading again,
// unless the loading caller was cancelled; the failure is never cached.
func TestProductScanShareHandsFailureToWaiters(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var s productScanCache
		key := productScanKey{run: "scan", name: "a"}
		for _, cancelled := range []bool{false, true} {
			var loads atomic.Int32
			started, release := make(chan struct{}), make(chan struct{})
			leader, cancel := context.WithCancel(context.Background())
			go func() {
				_, _ = s.share(leader, &s.shared, key, func(any) bool { return true }, func() (any, error) {
					loads.Add(1)
					close(started)
					<-release
					if cancelled {
						cancel()
						return nil, leader.Err()
					}
					return nil, errors.New("read failed")
				})
			}()
			<-started
			done := make(chan error)
			go func() {
				_, err := s.share(context.Background(), &s.shared, key, func(any) bool { return true }, func() (any, error) { loads.Add(1); return "value", nil })
				done <- err
			}()
			synctest.Wait()
			close(release)
			err := <-done
			if cancelled && (err != nil || loads.Load() != 2) || !cancelled && (err == nil || err.Error() != "read failed" || loads.Load() != 1) {
				t.Fatal("waiter outcome", cancelled, err, loads.Load())
			}
			cancel()
			s.forget(&s.shared, key)
		}
	})
}

// Region shards of a scan observe each subscription-wide source once although
// every region also walks scoped sources and API Management issue indexes.
func TestProductScanShareKeepsSubscriptionObservationsAcrossRegions(t *testing.T) {
	r, c := &Runtime{}, &client{subscription: testSubscription}
	observed := map[string]int{}
	for region := range 60 {
		scope := asset.Scope{Kind: asset.ScopeRegion, NativeID: fmt.Sprintf("region%d", region)}
		for kind := range 40 {
			request := contracts.InventoryRequest{ScanRunID: "scan", ConnectionID: "connection", Source: fmt.Sprintf("subscription%d", kind), Scope: scope}
			if _, err := r.inventorySnapshotPage(t.Context(), c, request, productCursor{}, "changed", func(contracts.InventoryRequest) (inventorySnapshot, error) {
				observed[request.Source]++
				return inventorySnapshot{}, nil
			}); err != nil {
				t.Fatal(err)
			}
		}
		for kind := range 27 {
			request := contracts.InventoryRequest{ScanRunID: "scan", ConnectionID: "connection", Source: fmt.Sprintf("scoped%d", kind), Scope: scope, Limit: 1}
			observe := func() (inventorySnapshot, error) {
				observed[scope.NativeID+request.Source]++
				return inventorySnapshot{items: []contracts.InventoryItem{{NativeID: "a"}, {NativeID: "b"}}, fingerprint: "f"}, nil
			}
			batch, err := r.scopedSnapshotPage(t.Context(), c, request, productCursor{}, "changed", nil, observe)
			for err == nil && !batch.Complete {
				var cursor productCursor
				request.Cursor = batch.NextCursor
				wire, _ := base64.RawURLEncoding.DecodeString(batch.NextCursor)
				_ = json.Unmarshal(wire, &cursor)
				batch, err = r.scopedSnapshotPage(t.Context(), c, request, cursor, "changed", nil, observe)
			}
			if err != nil || observed[scope.NativeID+request.Source] != 1 {
				t.Fatal("scoped shard", err, observed[scope.NativeID+request.Source])
			}
		}
		for service := range 50 {
			key := productScanKey{run: "scan", connection: "connection", name: fmt.Sprintf("apim-issues\x00%d", service)}
			if _, err := r.productScan.share(t.Context(), &r.productScan.apimIssues, key, func(any) bool { return true }, func() (any, error) { return map[string]serviceChild{}, nil }); err != nil {
				t.Fatal(err)
			}
		}
	}
	for kind := range 40 {
		if got := observed[fmt.Sprintf("subscription%d", kind)]; got != 1 {
			t.Fatal("subscription-wide observation repeated", kind, got)
		}
	}
	if len(r.productScan.shared) != 40 || len(r.productScan.apimIssues) != 50 {
		t.Fatal("shared observations", len(r.productScan.shared), len(r.productScan.apimIssues))
	}
}

// An audited source's shards share an observation across known-metadata
// changes it never reads, and observe again when a read field changes.
func TestSnapshotShareBindsOnlyReadKnownMetadata(t *testing.T) {
	r, c := &Runtime{}, &client{subscription: testSubscription}
	observed := 0
	for i, metadata := range []map[string]any{
		{"_hybrid_compute_configuration": "a", "status": "Connected"},
		{"_hybrid_compute_configuration": "a", "status": "Disconnected"},
		{"_hybrid_compute_configuration": "b", "status": "Disconnected"},
	} {
		request := contracts.InventoryRequest{ScanRunID: "scan", ConnectionID: "connection", Source: hybridComputeSource, Scope: asset.Scope{Kind: asset.ScopeRegion, NativeID: fmt.Sprint("region", i)}, KnownNativeIDs: []string{"id"}, KnownNativeMetadata: map[string]map[string]any{"id": metadata}}
		if _, err := r.inventorySnapshotPage(t.Context(), c, request, productCursor{}, "changed", func(contracts.InventoryRequest) (inventorySnapshot, error) {
			observed++
			return inventorySnapshot{}, nil
		}); err != nil {
			t.Fatal(err)
		}
		if want := []int{1, 1, 2}[i]; observed != want {
			t.Fatal("observations", i, observed)
		}
	}
}
