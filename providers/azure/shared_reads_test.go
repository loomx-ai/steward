package azure

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/loomx-ai/steward/internal/app/governance"
	"github.com/loomx-ai/steward/internal/core/asset"
)

// sharedReadsTrack counts every request by method and lowercase path, and
// holds matching ones so a peak tracker sees overlap behind a serializing
// fixture.
func sharedReadsTrack(r *Runtime, match func(path string, n int) bool) (func(string) int, *peakTracker) {
	var mu sync.Mutex
	calls, peak := map[string]int{}, &peakTracker{}
	inner := r.transport
	r.transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		path := strings.ToLower(req.URL.Path)
		mu.Lock()
		calls[req.Method+" "+path]++
		n := calls[req.Method+" "+path]
		mu.Unlock()
		if match(path, n) {
			peak.hold(2 * time.Millisecond)
		}
		return inner.RoundTrip(req)
	})
	return func(key string) int {
		mu.Lock()
		defer mu.Unlock()
		return calls[key]
	}, peak
}

// One scan's pages share each service's reference indexes; a failed load is
// not kept, and a listing outside a scan reads them afresh.
func TestAPIMPolicyInventorySharesReferenceIndexesAcrossScanPages(t *testing.T) {
	s, r, _, policy, _ := apimPolicyScenario(t, apimAPIType+"/operations/policies")
	named := strings.ToLower(apimNamespaceID(policy.Identity.NativeID) + "/namedValues")
	var mu sync.Mutex
	lists, fail := 0, false
	base := s.handle
	s.handle = func(req *http.Request) (*http.Response, bool) {
		if req.Method == "GET" && strings.ToLower(req.URL.Path) == named {
			mu.Lock()
			lists++
			failing := fail
			fail = false
			mu.Unlock()
			if failing {
				return sweep4Denied("NamedValuesDenied"), true
			}
		}
		if base != nil {
			return base(req)
		}
		return nil, false
	}
	request := productRequest(r, policy.Identity.NativeType)
	unscanned, err := listAllProduct(t, r, request)
	if err != nil || len(unscanned) != 1 || lists != 1 {
		t.Fatal("unscanned policy inventory", len(unscanned), lists, err)
	}
	request.ScanRunID = "scan"
	for range 3 {
		items, err := listAllProduct(t, r, request)
		if err != nil || len(items) != 1 || !slices.Equal(stringValues(items[0].Normalized["_apim_references"]), stringValues(unscanned[0].Normalized["_apim_references"])) {
			t.Fatal("scanned policy inventory changed its references", err)
		}
	}
	if lists != 2 {
		t.Fatal("scan pages relisted the service's named values", lists)
	}
	request.ScanRunID, fail = "retry", true
	if _, err := listAllProduct(t, r, request); err == nil || !strings.Contains(err.Error(), "NamedValuesDenied") {
		t.Fatal("named value failure was not surfaced", err)
	}
	if _, err := listAllProduct(t, r, request); err != nil || lists != 4 {
		t.Fatal("a failed index load was kept", lists, err)
	}
	request.ScanRunID = ""
	if _, err := listAllProduct(t, r, request); err != nil || lists != 5 {
		t.Fatal("an unscanned listing reused a scan index", lists, err)
	}
}

// VNet checks list each zone's links once without reading them, then read
// only the links naming the VNet (or naming none).
func TestVNetDNSLinksReadOnlyMatchingLinks(t *testing.T) {
	s, c, _, vnets := sweep4RegistrationScenario(t, 12)
	zoneID := strings.ToLower(resourceID(privateDNSZoneType, "internal.example.com"))
	root := "/subscriptions/" + testSubscription
	s.lists[root+"/providers/microsoft.network/privatednszones"] = []any{s.records[zoneID]}
	collection := zoneID + "/virtualnetworklinks"
	link := func(i int) string { return zoneID + fmt.Sprintf("/virtualnetworklinks/link%02d", i) }
	// A sparse list row naming no VNet is read for every VNet.
	sparse := sweep4Clone(object(s.lists[collection][5]))
	delete(object(sparse["properties"]), "virtualNetwork")
	s.lists[collection][5] = sparse
	calls, _ := countCalls(s, func(string) bool { return false }, 0)

	got, err := c.virtualNetworkDNSLinks(t.Context(), vnets[3])
	if err != nil || len(got) != 1 || got[0].id != link(3) {
		t.Fatal("VNet link membership", got, err)
	}
	for i := range 12 {
		if want := btoi(i == 3 || i == 5); calls["GET "+link(i)] != want {
			t.Fatal("link read count", i, calls["GET "+link(i)], want)
		}
	}
	if got, err := c.virtualNetworkDNSLinks(t.Context(), vnets[5]); err != nil || len(got) != 1 || got[0].id != link(5) {
		t.Fatal("sparse link row hid its VNet's link", got, err)
	}
	if calls["GET "+collection] != 2 {
		t.Fatal("delete-time checks reused a link index", calls["GET "+collection])
	}

	// An inventory page or contribution lists each zone once for all VNets.
	clear(calls)
	ctx := withReadMemo(t.Context())
	for i, vnet := range vnets {
		got, err := c.virtualNetworkDNSLinks(ctx, vnet)
		if err != nil || len(got) != 1 || got[0].id != link(i) {
			t.Fatal("memoized VNet link membership", i, got, err)
		}
	}
	if calls["GET "+collection] != 1 || calls["GET "+link(0)] != 1 || calls["GET "+link(5)] != 12 {
		t.Fatal("memoized link reads", calls["GET "+collection], calls["GET "+link(0)], calls["GET "+link(5)])
	}

	// A failed zone list fails closed and is not kept.
	ctx, denied := withReadMemo(t.Context()), true
	s.handle = func(req *http.Request) (*http.Response, bool) {
		if strings.ToLower(req.URL.Path) == collection && denied {
			denied = false
			return sweep4Denied("LinksDenied"), true
		}
		return nil, false
	}
	if got, err := c.virtualNetworkDNSLinks(ctx, vnets[3]); err == nil || !strings.Contains(err.Error(), "LinksDenied") || got != nil {
		t.Fatal("failed zone list read as no links", got, err)
	}
	if got, err := c.virtualNetworkDNSLinks(ctx, vnets[3]); err != nil || len(got) != 1 {
		t.Fatal("failed link index was kept", got, err)
	}
	// A zone deleted while its links were listed proves nothing.
	listed := false // Guarded by the fixture's lock, under which handle runs.
	s.handle = func(req *http.Request) (*http.Response, bool) {
		switch strings.ToLower(req.URL.Path) {
		case collection:
			listed = true
		case zoneID:
			if listed {
				return sweep4Denied("ZoneGone"), true
			}
		}
		return nil, false
	}
	if _, err := c.virtualNetworkDNSLinks(t.Context(), vnets[3]); err == nil || !strings.Contains(err.Error(), "ZoneGone") {
		t.Fatal("unfenced zone link list", err)
	}
}

// Each inventory pass reads the machine/profile index once for all licenses.
func TestHybridLicenseInventoryReadsMachineIndexOncePerPass(t *testing.T) {
	f := newHybridInventoryFixture(t)
	license := strings.ToLower(resourceID(hybridLicenseType, "license"))
	for i := range 4 {
		raw := sweep4Clone(f.values[license])
		raw["id"] = license + fmt.Sprint("extra", i)
		raw["name"] = last(text(raw["id"]))
		f.values[text(raw["id"])] = raw
	}
	machine := strings.ToLower(resourceID(hybridMachineType, "machine"))
	profile := machine + "/licenseprofiles/default"
	var mu sync.Mutex
	calls, fail := map[string]int{}, false
	f.override = func(req *http.Request) (*http.Response, bool) {
		path := strings.ToLower(req.URL.Path)
		mu.Lock()
		defer mu.Unlock()
		calls[path]++
		if fail && path == machine {
			fail = false
			return sweep4Denied("MachineDenied"), true
		}
		return nil, false
	}
	first, err := f.runtime.List(t.Context(), f.request(hybridLicenseType))
	if err != nil || len(first.Items) != 5 {
		t.Fatal("license inventory", err)
	}
	for _, item := range first.Items {
		if want := btoi(item.NativeID == license); len(object(object(item.Normalized[hybridComputeCleanup])["assignments"])) != want {
			t.Fatal("license assignments changed", item.NativeID)
		}
	}
	if calls[machine] != 2 || calls[machine+"/licenseprofiles"] != 2 || calls[profile] != 2 {
		t.Fatal("each pass did not read the index once", calls[machine], calls[machine+"/licenseprofiles"], calls[profile])
	}
	fail = true
	if _, err := f.runtime.List(t.Context(), f.request(hybridLicenseType)); err == nil || !strings.Contains(err.Error(), "MachineDenied") {
		t.Fatal("machine failure was not surfaced", err)
	}
	again, err := f.runtime.List(t.Context(), f.request(hybridLicenseType))
	a, _ := json.Marshal(first.Items)
	b, _ := json.Marshal(again.Items)
	if err != nil || string(a) != string(b) {
		t.Fatal("license inventory changed after a failed read", err)
	}
}

// Snapshot reviews run concurrently, apply in ID order, and keep the first
// failure in that order.
func TestNetappSnapshotInventoryReviewsConcurrentlyInOrder(t *testing.T) {
	f := newNetappFixture(t)
	base := strings.ToLower(resourceID(netappAccountType, "first")) + "/capacitypools/item/volumes/item/snapshots/item"
	ids := []string{base}
	for i := range 11 {
		raw := sweep4Clone(f.objects[base])
		id := base + fmt.Sprintf("%02d", i)
		raw["id"], raw["name"] = id, text(raw["name"])+fmt.Sprintf("%02d", i)
		object(raw["properties"])["snapshotId"] = fmt.Sprintf("00000000-0000-4000-8000-%012d", i)
		f.objects[id] = raw
		ids = append(ids, id)
	}
	// Hold each snapshot's third read in the first pass: its review's own read.
	counted, peak := sharedReadsTrack(f.runtime, func(path string, n int) bool { return n == 3 && slices.Contains(ids, path) })
	req := netappRequest(f.runtime, netappSnapshotType)
	req.Scope = asset.Scope{Kind: asset.ScopeRegion, NativeID: "eastus"}
	first, err := f.runtime.List(t.Context(), req)
	if err != nil || len(first.Items) != 12 {
		t.Fatal("snapshot inventory", len(first.Items), err)
	}
	for _, item := range first.Items {
		if item.Normalized[netappRecoveryReview] == nil || counted("GET "+item.NativeID) != 6 {
			t.Fatal("snapshot review missing or reread", item.NativeID, counted("GET "+item.NativeID))
		}
	}
	if peak.peak < 2 {
		t.Fatal("snapshot reviews were not concurrent", peak.peak)
	}
	second, err := f.runtime.List(t.Context(), req)
	a, _ := json.Marshal(first)
	b, _ := json.Marshal(second)
	if err != nil || string(a) != string(b) {
		t.Fatal("concurrent reviews are not deterministic", err)
	}
	// The third read of a snapshot in a pass is its review's own read.
	var mu sync.Mutex
	seen := map[string]int{}
	failing := map[string]string{ids[3]: "FirstDenied", ids[9]: "LaterDenied"}
	f.override = func(q *http.Request) (*http.Response, bool) {
		path := strings.ToLower(q.URL.Path)
		mu.Lock()
		defer mu.Unlock()
		if code := failing[path]; code != "" {
			if seen[path]++; seen[path] == 3 {
				return sweep4Denied(code), true
			}
		}
		return nil, false
	}
	for range 5 {
		clear(seen)
		if _, err := f.runtime.List(t.Context(), req); err == nil || !strings.Contains(err.Error(), "FirstDenied") {
			t.Fatal("first review failure in order lost", err)
		}
	}
}

// A contribution reads each volume group's snapshot index once for all its
// volumes, with the same result as reading it per volume.
func TestElasticSanVolumeSnapshotsShareGroupIndex(t *testing.T) {
	f := newElasticSanVolumeFixture(t)
	volume := f.ids[elasticSanVolumeType]
	snapshot := f.ids[elasticSanSnapshotType]
	var volumes []string
	for i := range 6 {
		raw := sweep4Clone(f.values[volume])
		id := volume + fmt.Sprintf("-share%02d", i)
		raw["id"], raw["name"] = id, last(id)
		object(raw["properties"])["volumeId"] = fmt.Sprintf("00000000-0000-4000-8000-%012d", i)
		f.values[id] = raw
		volumes = append(volumes, id)
		child := sweep4Clone(f.values[snapshot])
		childID := snapshot + fmt.Sprintf("-share%02d", i)
		child["id"], child["name"] = childID, last(childID)
		object(object(child["properties"])["creationData"])["sourceId"] = id
		f.values[childID] = child
	}
	var assets []asset.Asset
	for _, kind := range []string{elasticSanVolumeType, elasticSanSnapshotType} {
		batch, err := f.runtime.List(t.Context(), f.request(kind))
		if err != nil {
			t.Fatal(err)
		}
		for _, item := range batch.Items {
			assets = append(assets, elasticSanTestAsset(item))
		}
	}
	cascades := serviceCascades{client: f.client, connectionID: "connection"}
	calls, _ := sweep4Track(f.client, func(string) bool { return false }, 0)
	var live governance.Contribution
	if err := cascades.contributeElasticSanVolumes(t.Context(), assets, &live); err != nil {
		t.Fatal(err)
	}
	// The base volume group holds the base volume, its clones, and one more.
	group := "GET " + strings.ToLower(elasticSanParent(volume, elasticSanVolumeType)) + "/snapshots"
	if calls[group] != 8 || len(live.Bindings) != 7 {
		t.Fatal("fixture lost its volumes or snapshot bindings", calls[group], len(live.Bindings))
	}
	clear(calls)
	var shared governance.Contribution
	if err := cascades.contributeElasticSanVolumes(withReadMemo(t.Context()), assets, &shared); err != nil {
		t.Fatal(err)
	}
	a, _ := json.Marshal(live)
	b, _ := json.Marshal(shared)
	if string(a) != string(b) || calls[group] != 1 || calls["GET "+strings.ToLower(snapshot)] != 1 {
		t.Fatal("shared group index changed the contribution or reread it", calls[group], calls["GET "+strings.ToLower(snapshot)])
	}
}
