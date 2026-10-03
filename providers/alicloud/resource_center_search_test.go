package alicloud

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

type sharedSearchClient struct {
	mu       sync.Mutex
	searches []SearchRequest
	err      error
	records  []ResourceRecord
}

func (c *sharedSearchClient) SearchResources(_ context.Context, request SearchRequest) (ResourcePage, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.searches = append(c.searches, request)
	if c.err != nil {
		return ResourcePage{}, c.err
	}
	var page ResourcePage
	for _, record := range c.records {
		if record.RegionID == request.RegionID && slices.Contains(request.ResourceTypes, record.ResourceType) {
			page.Resources = append(page.Resources, record)
		}
	}
	return page, nil
}

func (c *sharedSearchClient) BatchGetResourceConfigurations(_ context.Context, request ResourceConfigurationRequest) (ResourceConfigurationPage, error) {
	var page ResourceConfigurationPage
	for _, resource := range request.Resources {
		page.Resources = append(page.Resources, ResourceRecord{
			RegionID: resource.RegionID, ResourceID: resource.ResourceID, ResourceType: resource.ResourceType,
			Configuration: map[string]any{},
		})
	}
	return page, nil
}

// sharedSearchFactory and staticCredentials are safe for concurrent shards.
type sharedSearchFactory struct {
	*runtimeFactory
}

func (f sharedSearchFactory) ResourceCenter(context.Context, contracts.Credential, string) (ResourceCenterClient, error) {
	return f.client, nil
}

type staticCredentials struct{ value contracts.Credential }

func (s staticCredentials) Resolve(context.Context, asset.ConnectionID) (contracts.Credential, error) {
	return s.value, nil
}

func (c *sharedSearchClient) searchCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.searches)
}

func TestResourceCenterKindShardsShareOneSearchPerScanAndRegion(t *testing.T) {
	t.Parallel()

	client := &sharedSearchClient{}
	runtime, err := newRuntime(staticCredentials{
		value: contracts.Credential{Type: asset.CredentialAliCloudAccessKey, Values: map[string]string{"access_key_id": "id", "access_key_secret": "secret"}},
	}, sharedSearchFactory{&runtimeFactory{client: client}})
	if err != nil {
		t.Fatal(err)
	}
	types := NewInventory(nil, runtime.resourceCenterInventoryNativeTypes()).resourceTypes[:resourceCenterTypeChunk]
	client.records = []ResourceRecord{
		{RegionID: "cn-hangzhou", ResourceType: types[0], ResourceID: "first"},
		{RegionID: "cn-hangzhou", ResourceType: types[1], ResourceID: "second"},
		{RegionID: "cn-shanghai", ResourceType: types[0], ResourceID: "elsewhere"},
	}
	list := func(run asset.ScanRunID, nativeType string) (contracts.InventoryBatch, error) {
		return runtime.List(context.Background(), contracts.InventoryRequest{
			ConnectionID: "connection-a", ScanRunID: run, Source: "resource-center",
			Scope:        asset.Scope{Kind: asset.ScopeRegion, NativeID: "cn-hangzhou", Location: "cn-hangzhou"},
			ResourceKind: &asset.ResourceKind{NativeType: nativeType},
		})
	}

	batches := make([]contracts.InventoryBatch, len(types))
	errs := make([]error, len(types))
	var wait sync.WaitGroup
	for index, nativeType := range types {
		wait.Go(func() { batches[index], errs[index] = list("run-1", nativeType) })
	}
	wait.Wait()
	if err := errors.Join(errs...); err != nil {
		t.Fatal(err)
	}
	if client.searchCount() != 1 || len(client.searches[0].ResourceTypes) != resourceCenterTypeChunk || client.searches[0].RegionID != "cn-hangzhou" {
		t.Fatalf("searches = %+v", client.searches)
	}
	for index, batch := range batches {
		want := map[int]string{0: "first", 1: "second"}[index]
		if !batch.Complete || batch.NextCursor != "" || (want == "") != (len(batch.Items) == 0) ||
			(want != "" && (len(batch.Items) != 1 || batch.Items[0].NativeID != want || batch.Items[0].NativeType != types[index])) {
			t.Fatalf("%s batch = %+v", types[index], batch)
		}
	}

	// A rescan never reuses the previous scan's search.
	if _, err := list("run-2", types[0]); err != nil || client.searchCount() != 2 {
		t.Fatalf("rescan searches = %d, err = %v", client.searchCount(), err)
	}

	// A failed shared search fails every shard instead of reporting it empty,
	// and is searched again by the next shard.
	client.mu.Lock()
	client.err = errors.New("throttled")
	client.mu.Unlock()
	for _, nativeType := range types[:2] {
		if batch, err := list("run-3", nativeType); err == nil {
			t.Fatalf("%s listed %+v after the shared search failed", nativeType, batch)
		}
	}
	client.mu.Lock()
	client.err = nil
	client.mu.Unlock()
	if batch, err := list("run-3", types[0]); err != nil || len(batch.Items) != 1 {
		t.Fatalf("retried shard batch = %+v, err = %v", batch, err)
	}
}

func TestResourceCenterKindShardPagesItsTypeFromTheSharedSearch(t *testing.T) {
	t.Parallel()

	client := &sharedSearchClient{}
	runtime, err := newRuntime(staticCredentials{
		value: contracts.Credential{Type: asset.CredentialAliCloudAccessKey, Values: map[string]string{"access_key_id": "id", "access_key_secret": "secret"}},
	}, sharedSearchFactory{&runtimeFactory{client: client}})
	if err != nil {
		t.Fatal(err)
	}
	nativeType := NewInventory(nil, runtime.resourceCenterInventoryNativeTypes()).resourceTypes[0]
	total := ResourceCenterPageLimit*2 + 7
	for index := range total {
		client.records = append(client.records, ResourceRecord{
			RegionID: "cn-hangzhou", ResourceType: nativeType, ResourceID: fmt.Sprintf("r-%04d", total-index),
		})
	}
	list := func(cursor string) (contracts.InventoryBatch, error) {
		return runtime.List(context.Background(), contracts.InventoryRequest{
			ConnectionID: "connection-a", ScanRunID: "run-1", Source: "resource-center", Cursor: cursor,
			Scope:        asset.Scope{Kind: asset.ScopeRegion, NativeID: "cn-hangzhou", Location: "cn-hangzhou"},
			ResourceKind: &asset.ResourceKind{NativeType: nativeType},
		})
	}
	readAll := func(cursor string) []string {
		t.Helper()
		var ids []string
		for {
			batch, err := list(cursor)
			if err != nil {
				t.Fatal(err)
			}
			for _, item := range batch.Items {
				ids = append(ids, item.NativeID)
			}
			if batch.Complete {
				return ids
			}
			cursor = batch.NextCursor
		}
	}

	ids := readAll("")
	if len(ids) != total || !slices.IsSorted(ids) || client.searchCount() != 1 {
		t.Fatalf("listed %d of %d records (sorted %t) in %d searches", len(ids), total, slices.IsSorted(ids), client.searchCount())
	}

	// The finished shard released its records; listing the type again is a
	// fresh search, never an empty listing that would read as deletions.
	if again := readAll(""); len(again) != total || client.searchCount() != 2 {
		t.Fatalf("relisted %d records in %d searches", len(again), client.searchCount())
	}

	// A shard resumed after its shared search was dropped continues after its
	// last record; a record it already read being deleted shifts nothing.
	first, err := list("")
	if err != nil || first.Complete {
		t.Fatalf("first page = %+v, err = %v", first, err)
	}
	client.mu.Lock()
	client.records = client.records[:len(client.records)-1] // deletes r-0001
	client.mu.Unlock()
	runtime.resourceCenterSearches = resourceCenterSearchCache{}
	rest := readAll(first.NextCursor)
	if len(first.Items)+len(rest) != total || rest[0] != fmt.Sprintf("r-%04d", ResourceCenterPageLimit+1) {
		t.Fatalf("resumed listing = %d records starting at %q", len(rest), rest[0])
	}

	if _, err := list(`{"parent_index":1}`); err == nil {
		t.Fatal("accepted a cursor that names no record")
	}
}
