package gcp

import (
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

// The region shards of one scan share Cloud Asset Inventory pages and keep only
// their own scope; another scan, or a failed fetch, never reuses a page.
func TestScanShardsShareProjectWidePagesWithinOneScan(t *testing.T) {
	var reads atomic.Int32
	var failing atomic.Bool
	release := make(chan struct{})
	r := protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
		if req.URL.Host != "cloudasset.googleapis.com" {
			t.Fatalf("unexpected request %s", req.URL)
		}
		reads.Add(1)
		<-release
		if failing.Load() {
			return apiResponse(req, 403, `{}`), nil
		}
		if req.URL.Query().Get("pageToken") == "" {
			return apiResponse(req, 200, `{"readTime":"2026-10-03T00:00:00Z","nextPageToken":"second","assets":[{"name":"//example.googleapis.com/projects/sample-project/locations/us-central1/things/a","assetType":"example.googleapis.com/Thing","resource":{"data":{"name":"a"},"location":"us-central1"}},{"name":"//example.googleapis.com/projects/sample-project/locations/global/things/g","assetType":"example.googleapis.com/Thing","resource":{"data":{"name":"g"},"location":"global"}}]}`), nil
		}
		return apiResponse(req, 200, `{"readTime":"2026-10-03T00:00:00Z","assets":[{"name":"//example.googleapis.com/projects/sample-project/locations/europe-west1/things/b","assetType":"example.googleapis.com/Thing","resource":{"data":{"name":"b"},"location":"europe-west1"}}]}`), nil
	})
	close(release)
	scan := func(run asset.ScanRunID, region string) []string {
		request := contracts.InventoryRequest{ConnectionID: "connection", ScanRunID: run, Source: inventorySource, Scope: asset.Scope{Kind: asset.ScopeRegion, NativeID: region}, Limit: 100}
		if region == "global" {
			request.Scope = asset.Scope{Kind: asset.ScopeGlobal, NativeID: "sample-project/global"}
		}
		var names []string
		for {
			batch, err := r.List(t.Context(), request)
			if err != nil {
				t.Fatal(err)
			}
			for _, item := range batch.Items {
				names = append(names, last(item.NativeID))
			}
			if batch.Complete {
				return names
			}
			request.Cursor = batch.NextCursor
		}
	}
	var wg sync.WaitGroup
	results := make([][]string, 5)
	for i, region := range []string{"us-central1", "europe-west1", "us-central1", "asia-east1", "global"} {
		wg.Go(func() { results[i] = scan("scan-1", region) })
	}
	wg.Wait()
	if strings.Join(results[0], ",") != "a" || strings.Join(results[1], ",") != "b" || strings.Join(results[2], ",") != "a" || len(results[3]) != 0 || strings.Join(results[4], ",") != "g" {
		t.Fatalf("shards reported another scope: %v", results)
	}
	if reads.Load() != 2 {
		t.Fatalf("asset pages read %d times, want each page once per scan", reads.Load())
	}
	scan("scan-2", "us-central1")
	if reads.Load() != 4 {
		t.Fatalf("another scan reused pages: %d reads", reads.Load())
	}
	failing.Store(true)
	request := contracts.InventoryRequest{ConnectionID: "connection", ScanRunID: "scan-3", Source: inventorySource, Scope: asset.Scope{Kind: asset.ScopeRegion, NativeID: "us-central1"}, Limit: 100}
	if _, err := r.List(t.Context(), request); err == nil {
		t.Fatal("failed page accepted")
	}
	failing.Store(false)
	if batch, err := r.List(t.Context(), request); err != nil || len(batch.Items) != 1 {
		t.Fatalf("failed page was kept: %+v %v", batch, err)
	}
}
