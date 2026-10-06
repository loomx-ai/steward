package azure

import (
	"context"
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

// countingOverride counts GETs by lowercase path and runs change on each.
func countingOverride(change func(path string, n int)) (func(*http.Request) (*http.Response, bool), func(string) int) {
	var mu sync.Mutex
	calls := map[string]int{}
	return func(req *http.Request) (*http.Response, bool) {
			path := strings.ToLower(req.URL.Path)
			mu.Lock()
			defer mu.Unlock()
			if req.Method == "GET" {
				calls[path]++
				if change != nil {
					change(path, calls[path])
				}
			}
			return nil, false
		}, func(path string) int {
			mu.Lock()
			defer mu.Unlock()
			return calls[strings.ToLower(path)]
		}
}

// Every backup instance of a vault shares each round's retained-backup
// listing and reads; the two rounds and the two passes each read anew, so a
// retained backup added between rounds is caught.
func TestDataProtectionInstancesShareRetainedListingPerRound(t *testing.T) {
	f := newProtectionInstanceFixture(t)
	list := f.vault + "/deletedbackupinstances"
	override, calls := countingOverride(nil)
	f.override = override
	batch, err := f.runtime.List(t.Context(), protectionRequest(f.protectionFixture, dataProtectionInstance))
	if err != nil || len(batch.Items) != 2 {
		t.Fatal("instance inventory", err)
	}
	// 2 passes x 2 rounds, not once per instance and round.
	if calls(list) != 4 || calls(f.deletedInstance) != 4 {
		t.Fatal("retained backups not shared per round", calls(list), calls(f.deletedInstance))
	}
	added := f.vault + "/deletedbackupinstances/added"
	f.override, _ = countingOverride(func(path string, n int) {
		if path == list && n == 2 {
			f.objects[added] = batchClone(f.objects[f.deletedInstance])
			f.objects[added]["id"], f.objects[added]["name"] = added, "added"
		}
	})
	if _, err := f.runtime.List(t.Context(), protectionRequest(f.protectionFixture, dataProtectionInstance)); err == nil || !strings.Contains(err.Error(), "backup_instance_changed_during_review") {
		t.Fatal("retained backup added between rounds was not detected", err)
	}
}

// Containers of one vault share each round's protected-item listing within a
// pass; rounds read anew (a change between them is caught) and reviews
// without a memo read live per call.
func TestRecoveryContainersShareProtectedItemsPerRound(t *testing.T) {
	f := newRecoveryServicesFixture(t)
	other := f.container + "x"
	f.objects[other] = batchClone(f.objects[f.container])
	f.objects[other]["id"], f.objects[other]["name"] = other, last(other)
	list := f.vault + "/backupprotecteditems"
	c, err := f.runtime.resolve(t.Context(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	review := func(ctx context.Context) error {
		for _, id := range []string{f.container, other} {
			if _, _, err := c.recoveryContainerReviewFor(ctx, id, nil); err != nil {
				return err
			}
		}
		return nil
	}
	override, calls := countingOverride(nil)
	f.override = override
	if err := review(withReadMemo(t.Context())); err != nil || calls(list) != 2 {
		t.Fatal("listing not shared per round", err, calls(list))
	}
	if err := review(t.Context()); err != nil || calls(list) != 6 {
		t.Fatal("live review reused a listing", err, calls(list))
	}
	f.override, _ = countingOverride(func(path string, n int) {
		if path == list && n == 2 {
			object(f.objects[f.item]["properties"])["privateRevision"] = "changed"
		}
	})
	if err := review(withReadMemo(t.Context())); err == nil || !strings.Contains(err.Error(), "recovery_container_context_changed") {
		t.Fatal("change between rounds was not detected", err)
	}
}

// Assignments of one account share each round's pool and volume walk within
// a pass; the closing pool re-reads stay per item, and rounds read anew so a
// volume change between them is caught.
func TestNetappAssignmentsShareVolumeWalkPerRound(t *testing.T) {
	f, id, volume := netappAssignmentFixture(t, netappSnapshotPolicyType)
	other := id + "2"
	f.objects[other] = sweep4Clone(f.objects[id])
	f.objects[other]["id"], f.objects[other]["name"] = other, text(f.objects[id]["name"])+"2"
	pool := redisParentID(volume)
	pools, volumes := redisParentID(pool)+"/capacitypools", pool+"/volumes"
	inner := f.override
	override, calls := countingOverride(nil)
	f.override = func(q *http.Request) (*http.Response, bool) {
		override(q)
		return inner(q)
	}
	req := netappRequest(f.runtime, netappSnapshotPolicyType)
	req.Scope = asset.Scope{Kind: asset.ScopeRegion, NativeID: "eastus"}
	page, err := f.runtime.List(t.Context(), req)
	if err != nil || len(page.Items) != 2 {
		t.Fatal("assignment inventory", err)
	}
	// 2 passes x 2 rounds, not once per assignment and round; each pool is
	// read once per round plus once live per assignment and round.
	if calls(pools) != 4 || calls(volumes) != 4 || calls(volume) != 4 || calls(pool) != 12 {
		t.Fatal("volume walk not shared per round", calls(pools), calls(volumes), calls(volume), calls(pool))
	}
	change, _ := countingOverride(func(path string, n int) {
		if path == volumes && n == 2 {
			object(f.objects[volume]["properties"])["privateRevision"] = "changed"
		}
	})
	f.override = func(q *http.Request) (*http.Response, bool) {
		change(q)
		return inner(q)
	}
	if _, err := f.runtime.List(t.Context(), req); err == nil || !strings.Contains(err.Error(), "netapp_assignments_changed") {
		t.Fatal("volume change between rounds was not detected", err)
	}
}

// Action groups of one pass share each receiver index (itself read twice and
// compared); another pass and a memo-free cleanup read it again.
func TestMonitorReceiverIndexSharedPerPass(t *testing.T) {
	f := newMonitorReceiverFixture(t)
	collection := strings.ToLower("/subscriptions/" + testSubscription + "/providers/" + eventHubNamespaceType)
	calls := 0
	f.fault = func(req *http.Request) (*http.Response, bool) {
		if strings.ToLower(req.URL.Path) == collection {
			calls++
		}
		return nil, false
	}
	c, err := f.runtime.resolve(t.Context(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	raw := f.objects[f.sourceID]
	read := func(ctx context.Context) {
		t.Helper()
		refs, err := c.monitorReferences(ctx, monitorActionGroupType, f.sourceID, raw)
		if err != nil || !slices.Equal(refs[eventHubNamespaceType], []string{f.namespaceID}) {
			t.Fatal("receiver references", refs, err)
		}
	}
	pass := withReadMemo(t.Context())
	read(pass)
	read(pass)
	if calls != 2 {
		t.Fatal("receiver index not shared within a pass", calls)
	}
	read(withReadMemo(t.Context()))
	read(t.Context())
	if calls != 6 {
		t.Fatal("another pass or a live read reused the index", calls)
	}
	// A namespace removed before the next pass is seen by that pass.
	delete(f.receivers, f.namespaceID)
	refs, err := c.monitorReferences(withReadMemo(t.Context()), monitorActionGroupType, f.sourceID, raw)
	if err != nil || slices.Contains(refs[eventHubNamespaceType], f.namespaceID) {
		t.Fatal("next pass reused the earlier index", refs, err)
	}
}

// Domains of one pass share each round's web site walk; the two rounds each
// read it, and a memo-free (cleanup) walk reads it live per call.
func TestDomainChildrenShareSiteWalkPerRound(t *testing.T) {
	s, r, _ := domainScenario(t, true)
	sites := "/subscriptions/" + testSubscription + "/providers/microsoft.web/sites"
	var mu sync.Mutex
	calls := 0
	s.before = func(req *http.Request) {
		if req.Method == "GET" && strings.ToLower(req.URL.Path) == sites {
			mu.Lock()
			calls++
			mu.Unlock()
		}
	}
	c, err := r.resolve(t.Context(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	var domain map[string]any
	for _, raw := range s.records {
		if text(raw["type"]) == domainType {
			domain = raw
		}
	}
	parent := asset.Identity{NativeID: strings.ToLower(text(domain["id"])), NativeType: domainType}
	walk := func(ctx context.Context) {
		t.Helper()
		children, err := c.domainChildren(ctx, parent, domain, nil)
		if err != nil || len(children) != 3 {
			t.Fatal("domain children", len(children), err)
		}
	}
	pass := withReadMemo(t.Context())
	walk(pass)
	walk(pass)
	if calls != 2 {
		t.Fatal("site walk not shared per round, or a round reused the other", calls)
	}
	walk(t.Context())
	if calls != 4 {
		t.Fatal("memo-free walk reused a listing", calls)
	}
}

// A scan lists the subscription's vaults once for every API Management
// service it resolves; outside a scan each service's indexes list them.
func TestAPIMExternalIndexSharedPerScan(t *testing.T) {
	s, r, _, vault, _ := apimExternalScenario(t)
	list := "/subscriptions/" + testSubscription + "/providers/" + strings.ToLower(apimVaultType)
	var mu sync.Mutex
	calls := 0
	s.before = func(req *http.Request) {
		if req.Method == "GET" && strings.ToLower(req.URL.Path) == list {
			mu.Lock()
			calls++
			mu.Unlock()
		}
	}
	c, err := r.resolve(t.Context(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	resolve := func(ctx context.Context) {
		t.Helper()
		// Two services, each with its own per-service indexes.
		for range 2 {
			ref, err := c.apimExternalReference(ctx, apimVaultType, "https://rpbvtkeyvaultintegration.vault-int.azure-int.net", &apimIndexes{})
			if err != nil || ref != text(vault["id"]) {
				t.Fatal("vault reference", ref, err)
			}
		}
	}
	scan := map[string]*apimIndexes{}
	ctx := withAPIMScanIndexes(t.Context(), func(root string) (*apimIndexes, error) {
		if scan[root] == nil {
			scan[root] = &apimIndexes{}
		}
		return scan[root], nil
	})
	resolve(ctx)
	if calls != 1 {
		t.Fatal("scan relisted vaults per service", calls)
	}
	resolve(t.Context())
	if calls != 3 {
		t.Fatal("unscanned resolution reused the scan's index", calls)
	}
}

// Artifacts of one workspace share the opening group read, pipeline walk,
// pool index and pool work within a pass; each artifact's closing re-reads
// stay live, and a memo-free review reads everything per call.
func TestSynapseArtifactReviewsShareWorkspaceReadsPerPass(t *testing.T) {
	f, c, _ := sparkWorkFixture(t)
	group := "/subscriptions/" + testSubscription + "/resourcegroups/test"
	pools := strings.ToLower(text(f.workspace["id"])) + "/bigdatapools"
	calls := map[string]int{}
	f.intercept = func(q *http.Request) (*http.Response, bool) {
		if q.Method == "GET" {
			calls[strings.ToLower(q.URL.Path)]++
		}
		return nil, false
	}
	w, err := c.arm.synapseWorkspaceRead(t.Context(), text(f.workspace["id"]))
	if err != nil {
		t.Fatal(err)
	}
	d := synapseDataKind(synapseNotebookType)
	review := func(ctx context.Context) {
		t.Helper()
		// Two artifacts of the workspace.
		for range 2 {
			out, err := c.artifactReview(ctx, synapseDataTarget{workspace: w}, d, f.items[synapseNotebookType], nil)
			if err != nil || out["blocked"] != true || len(object(out["pools"])) != 1 {
				t.Fatal("artifact review", out, err)
			}
		}
	}
	review(withReadMemo(t.Context()))
	// One shared opening read plus one live closing read per artifact.
	if calls[group] != 3 || calls[pools] != 3 || calls["/pipelines"] != 2 {
		t.Fatal("workspace reads not shared per pass", calls[group], calls[pools], calls["/pipelines"])
	}
	clear(calls)
	review(t.Context())
	if calls[group] != 4 || calls[pools] != 4 || calls["/pipelines"] != 4 {
		t.Fatal("memo-free review reused a read", calls[group], calls[pools], calls["/pipelines"])
	}
}

// Workbooks of one resource group share its review read within a pass, and
// each of the two snapshot passes reads it itself: per pass one shared read
// plus the listing's three group checks (10 in all before sharing, 7 if the
// passes shared it).
func TestWorkbookGroupReadSharedPerPass(t *testing.T) {
	f := newWorkbookFixture(t, insightsWorkbookType)
	request := productRequest(f.runtime, insightsWorkbookType)
	request.Scope.Kind, request.Scope.NativeID = asset.ScopeRegion, "westus"
	batch, err := f.runtime.List(t.Context(), request)
	if err != nil || len(batch.Items) != 2 {
		t.Fatal("workbook inventory", err)
	}
	if f.calls["GET "+f.groupID] != 8 {
		t.Fatal("group review read not shared once per pass", f.calls["GET "+f.groupID])
	}
}

// A workspace's two managed-group walks run under the contribution's memo;
// the second walk must read the group's association indexes itself.
func TestMonitorWorkspaceSecondGroupWalkReadsAnew(t *testing.T) {
	s, r, assets := monitorWorkspaceScenario(t)
	rule := assets[2].Identity.NativeID + "/associations"
	var mu sync.Mutex
	calls := 0
	s.before = func(req *http.Request) {
		if req.Method == "GET" && strings.EqualFold(req.URL.Path, rule) {
			mu.Lock()
			calls++
			mu.Unlock()
		}
	}
	c, err := r.resolve(t.Context(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	raw := s.records[assets[0].Identity.NativeID]
	if _, _, err := c.monitorWorkspaceResources(withReadMemo(t.Context()), assets[0], raw); err != nil {
		t.Fatal(err)
	}
	// 4 if the second walk reused the first walk's reads.
	if calls != 6 {
		t.Fatal("the second walk reused the first walk's association index", calls)
	}
}
