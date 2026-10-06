package azure

import (
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

// sweep6Deny fails GETs of the given paths with a distinct error code each.
func sweep6Deny(s *dnsScenario, codes map[string]string) {
	s.handle = func(req *http.Request) (*http.Response, bool) {
		if code := codes[strings.ToLower(req.URL.Path)]; code != "" && req.Method == "GET" {
			return jsonResponse(403, map[string]any{"error": map[string]any{"code": code}}, nil), true
		}
		return nil, false
	}
}

func sweep6Client(t *testing.T, s *dnsScenario) *client {
	t.Helper()
	c, err := s.runtime(t).resolve(t.Context(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func sweep6Concurrent(t *testing.T, peak *peakTracker) {
	t.Helper()
	if peak.peak < 2 || peak.peak > detailReadConcurrency {
		t.Fatal("reads were not concurrent within the bound", peak.peak)
	}
}

func flexibleScaleSetSweepScenario(t *testing.T, count int) (*dnsScenario, string, []string, []any) {
	t.Helper()
	s := newDNSScenario()
	scaleID := strings.ToLower(resourceID(scaleSetType, "flexible"))
	vmList := strings.ToLower("/subscriptions/" + testSubscription + "/providers/" + vmType)
	s.version[vmList] = "2024-07-01"
	var ids []string
	for i := range count {
		id := strings.ToLower(resourceID(vmType, fmt.Sprintf("vm%02d", i)))
		vm := map[string]any{"id": id, "type": vmType, "name": fmt.Sprintf("vm%02d", i), "location": "eastus", "properties": map[string]any{"vmId": fmt.Sprintf("uid-%d", i)}}
		if i%2 == 0 {
			object(vm["properties"])["virtualMachineScaleSet"] = map[string]any{"id": scaleID}
		}
		s.add(vm, "2024-07-01")
		s.lists[vmList] = append(s.lists[vmList], batchClone(vm))
		ids = append(ids, id)
	}
	return s, scaleID, ids, s.lists[vmList]
}

func TestSweep6FlexibleScaleSetVMsConcurrentInOrder(t *testing.T) {
	s, scaleID, ids, _ := flexibleScaleSetSweepScenario(t, 12)
	calls, peak := countCalls(s, func(path string) bool { return strings.Contains(path, "/virtualmachines/vm") }, 5*time.Millisecond)
	c := sweep6Client(t, s)
	children, err := c.flexibleScaleSetVMs(t.Context(), scaleID)
	if err != nil || len(children) != 6 {
		t.Fatal("members changed", len(children), err)
	}
	for i, child := range children {
		if child.id != ids[2*i] || !child.direct || child.kind != vmType {
			t.Fatal("members out of order", i, child.id)
		}
	}
	for _, id := range ids {
		if calls["GET "+id] != 1 {
			t.Fatal("member read count", id, calls["GET "+id])
		}
	}
	sweep6Concurrent(t, peak)
}

func TestSweep6FlexibleScaleSetVMsFirstErrorInOrder(t *testing.T) {
	s, scaleID, ids, listed := flexibleScaleSetSweepScenario(t, 12)
	calls, _ := countCalls(s, func(string) bool { return false }, 0)
	c := sweep6Client(t, s)
	sweep6Deny(s, map[string]string{ids[2]: "FirstDenied", ids[6]: "LaterDenied"})
	object(listed[7])["type"] = "Microsoft.Compute/disks"
	for range 5 {
		if _, err := c.flexibleScaleSetVMs(t.Context(), scaleID); err == nil || !strings.Contains(err.Error(), "FirstDenied") {
			t.Fatal("earlier read failure lost", err)
		}
	}
	s.handle = nil
	clear(calls)
	if _, err := c.flexibleScaleSetVMs(t.Context(), scaleID); err == nil || !strings.Contains(err.Error(), "invalid or duplicate") {
		t.Fatal("invalid row accepted", err)
	}
	for i, id := range ids {
		if want := map[bool]int{true: 1, false: 0}[i < 7]; calls["GET "+id] != want {
			t.Fatal("read rows at or after the invalid row", id, calls["GET "+id])
		}
	}
}

func redisIncomingSweepScenario(t *testing.T, count int) (*dnsScenario, []string, []any) {
	t.Helper()
	s := newDNSScenario()
	kind, _ := findType(redisType)
	list := strings.ToLower("/subscriptions/" + testSubscription + "/providers/" + redisType)
	s.version[list] = kind.Version
	s.lists[list] = []any{}
	var ids []string
	for i := range count {
		id := strings.ToLower(resourceID(redisType, fmt.Sprintf("cache%02d", i)))
		raw := map[string]any{"id": id, "type": redisType, "name": fmt.Sprintf("cache%02d", i), "location": "eastus", "properties": map[string]any{"sku": map[string]any{"name": "Basic"}}}
		s.add(raw, kind.Version)
		s.lists[list] = append(s.lists[list], batchClone(raw))
		ids = append(ids, id)
	}
	return s, ids, s.lists[list]
}

func TestSweep6RedisIncomingLinksConcurrentInOrder(t *testing.T) {
	s, ids, _ := redisIncomingSweepScenario(t, 12)
	calls, peak := countCalls(s, func(path string) bool { return strings.Contains(path, "/redis/cache") }, 5*time.Millisecond)
	c := sweep6Client(t, s)
	links, err := c.redisIncomingLinks(t.Context(), ids[3])
	if err != nil || len(links) != 0 {
		t.Fatal("incoming links changed", links, err)
	}
	for _, id := range ids {
		if calls["GET "+id] != 1 {
			t.Fatal("cache read count", id, calls["GET "+id])
		}
	}
	sweep6Concurrent(t, peak)
	if _, err := c.redisIncomingLinks(t.Context(), strings.ToLower(resourceID(redisType, "missing"))); err == nil || !strings.Contains(err.Error(), "redis_subscription_index_incomplete") {
		t.Fatal("missing target accepted", err)
	}
}

func TestSweep6RedisIncomingLinksFirstErrorInOrder(t *testing.T) {
	s, ids, listed := redisIncomingSweepScenario(t, 12)
	calls, _ := countCalls(s, func(string) bool { return false }, 0)
	c := sweep6Client(t, s)
	sweep6Deny(s, map[string]string{ids[2]: "FirstDenied", ids[6]: "LaterDenied"})
	object(listed[7])["type"] = "Microsoft.Cache/other"
	for range 5 {
		if _, err := c.redisIncomingLinks(t.Context(), ids[0]); err == nil || !strings.Contains(err.Error(), "FirstDenied") {
			t.Fatal("earlier read failure lost", err)
		}
	}
	s.handle = nil
	clear(calls)
	// The invalid row's error follows the earlier rows, even before the target check.
	if _, err := c.redisIncomingLinks(t.Context(), ids[0]); err == nil || !strings.Contains(err.Error(), "invalid_redis_subscription_list") {
		t.Fatal("invalid row accepted", err)
	}
	for i, id := range ids {
		if want := map[bool]int{true: 1, false: 0}[i < 7]; calls["GET "+id] != want {
			t.Fatal("read rows at or after the invalid row", id, calls["GET "+id])
		}
	}
}

var sweep6DefenderRequest = contracts.InventoryRequest{ConnectionID: "connection", Scope: asset.Scope{Kind: asset.ScopeGlobal, NativeID: testSubscription + "/global"}}

// defenderSweepScenario has count VMs with one own plan each, and count
// subscription plans.
func defenderSweepScenario(t *testing.T, count int) (*dnsScenario, []string, []string, []any) {
	t.Helper()
	s := newDNSScenario()
	root := "/subscriptions/" + testSubscription
	pricings := func(scope string) string { return strings.ToLower(scope + "/providers/" + defenderPricingType) }
	plan := func(scope, name string) map[string]any {
		raw := map[string]any{"id": pricings(scope) + "/" + name, "name": name, "type": defenderPricingType, "properties": map[string]any{"pricingTier": "Standard"}}
		s.add(raw, defenderVersion)
		s.lists[pricings(scope)] = append(s.lists[pricings(scope)], batchClone(raw))
		s.version[pricings(scope)] = defenderVersion
		return raw
	}
	for _, kind := range defenderScopeKinds {
		path := strings.ToLower(root + "/providers/" + kind)
		s.lists[path], s.version[path] = []any{}, defenderParentKind(kind).Version
	}
	vmList := strings.ToLower(root + "/providers/" + vmType)
	var vms, plans []string
	for i := range count {
		id := strings.ToLower(resourceID(vmType, fmt.Sprintf("vm%02d", i)))
		vm := map[string]any{"id": id, "type": vmType, "name": fmt.Sprintf("vm%02d", i), "location": "eastus", "properties": map[string]any{"vmId": fmt.Sprintf("uid-%d", i)}}
		s.add(vm, defenderParentKind(vmType).Version)
		s.lists[vmList] = append(s.lists[vmList], batchClone(vm))
		vms = append(vms, id)
		plans = append(plans, text(plan(id, "virtualmachines")["id"]))
		plan(root, fmt.Sprintf("plan%02d", i))
	}
	return s, vms, plans, s.lists[pricings(root)]
}

func TestSweep6DefenderSnapshotConcurrentInOrder(t *testing.T) {
	s, vms, plans, _ := defenderSweepScenario(t, 12)
	calls, peak := countCalls(s, func(path string) bool { return strings.Contains(path, "/virtualmachines/vm") }, 5*time.Millisecond)
	r := s.runtime(t)
	c, err := r.resolve(t.Context(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	items, _, _, err := r.defenderSnapshot(t.Context(), c, sweep6DefenderRequest)
	if err != nil || len(items) != 24 {
		t.Fatal("plans changed", len(items), err)
	}
	for i := 1; i < len(items); i++ {
		if items[i-1].NativeID >= items[i].NativeID {
			t.Fatal("plans out of order", items[i-1].NativeID, items[i].NativeID)
		}
	}
	for i, vm := range vms {
		if calls["GET "+vm] != 1 || calls["GET "+strings.ToLower(vm+"/providers/"+defenderPricingType)] != 1 || calls["GET "+plans[i]] != 1 {
			t.Fatal("VM scope read counts", vm, calls["GET "+vm], calls["GET "+plans[i]])
		}
	}
	sweep6Concurrent(t, peak)
}

func TestSweep6DefenderFirstErrorInOrder(t *testing.T) {
	s, vms, plans, subscriptionPlans := defenderSweepScenario(t, 12)
	r := s.runtime(t)
	c, err := r.resolve(t.Context(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	// Parent reads: the earlier VM's failure wins.
	sweep6Deny(s, map[string]string{vms[2]: "FirstDenied", vms[6]: "LaterDenied"})
	for range 5 {
		if _, _, _, err := r.defenderSnapshot(t.Context(), c, sweep6DefenderRequest); err == nil || !strings.Contains(err.Error(), "FirstDenied") {
			t.Fatal("earlier parent failure lost", err)
		}
	}
	// Per-scope plan indexes: the earlier scope's failure wins.
	sweep6Deny(s, map[string]string{plans[3]: "FirstDenied", plans[9]: "LaterDenied"})
	for range 5 {
		if _, _, _, err := r.defenderSnapshot(t.Context(), c, sweep6DefenderRequest); err == nil || !strings.Contains(err.Error(), "FirstDenied") {
			t.Fatal("earlier plan index failure lost", err)
		}
	}
	// One index: an earlier read failure precedes a later invalid row, and rows
	// at or after an invalid row are not read.
	root := "/subscriptions/" + testSubscription
	first, later := text(object(subscriptionPlans[2])["id"]), text(object(subscriptionPlans[6])["id"])
	sweep6Deny(s, map[string]string{first: "FirstDenied", later: "LaterDenied"})
	object(subscriptionPlans[7])["type"] = "Microsoft.Security/other"
	for range 5 {
		if _, _, err := c.defenderPricingIndex(t.Context(), root); err == nil || !strings.Contains(err.Error(), "FirstDenied") {
			t.Fatal("earlier plan failure lost", err)
		}
	}
	s.handle = nil
	calls, _ := countCalls(s, func(string) bool { return false }, 0)
	if _, _, err := c.defenderPricingIndex(t.Context(), root); err == nil || !strings.Contains(err.Error(), "invalid_defender_index_member") {
		t.Fatal("invalid row accepted", err)
	}
	for i, raw := range subscriptionPlans {
		if want := map[bool]int{true: 1, false: 0}[i < 7]; calls["GET "+text(object(raw)["id"])] != want {
			t.Fatal("read rows at or after the invalid row", i, calls["GET "+text(object(raw)["id"])])
		}
	}
}

// sweep6Track counts a runtime's requests and holds matching ones, outside any
// fixture lock, so overlapping reads are observable. Call before resolving.
func sweep6Track(r *Runtime, match func(string) bool) (map[string]int, *peakTracker) {
	var mu sync.Mutex
	calls, peak := map[string]int{}, &peakTracker{}
	base := r.transport
	r.transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		path := strings.ToLower(req.URL.Path)
		mu.Lock()
		calls[req.Method+" "+path]++
		mu.Unlock()
		if match(path) {
			peak.hold(5 * time.Millisecond)
		}
		return base.RoundTrip(req)
	})
	return calls, peak
}

// recoveryServicesSweepFixture has count protected items in one container.
func recoveryServicesSweepFixture(t *testing.T, count int) (*recoveryServicesFixture, []string) {
	t.Helper()
	f := newRecoveryServicesFixture(t)
	ids := []string{f.item}
	for i := 1; i < count; i++ {
		raw := batchClone(f.objects[f.item])
		id := f.container + fmt.Sprintf("/protecteditems/saphanadatabase;hdb;db%02d", i)
		raw["id"], raw["name"] = id, last(id)
		f.objects[id] = raw
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return f, ids
}

func TestSweep6RecoveryServicesSnapshotConcurrentInOrder(t *testing.T) {
	isItem := func(path string) bool { return strings.Contains(path, "/protecteditems/") }
	baseline, single := recoveryServicesSweepFixture(t, 1)
	once, _ := sweep6Track(baseline.runtime, func(string) bool { return false })
	for _, kind := range []string{recoveryServicesItem, recoveryServicesContainer} {
		if batch, err := baseline.runtime.List(t.Context(), recoveryServicesRequest(baseline, kind)); err != nil || len(batch.Items) != 1 {
			t.Fatal("baseline inventory", kind, len(batch.Items), err)
		}
	}
	f, ids := recoveryServicesSweepFixture(t, 12)
	calls, peak := sweep6Track(f.runtime, isItem)
	batch, err := f.runtime.List(t.Context(), recoveryServicesRequest(f, recoveryServicesItem))
	if err != nil || len(batch.Items) != len(ids) {
		t.Fatal("items changed", len(batch.Items), err)
	}
	for i, item := range batch.Items {
		if item.NativeID != ids[i] || item.Normalized["containerId"] != f.container {
			t.Fatal("items out of order", i, item.NativeID)
		}
	}
	if batch, err := f.runtime.List(t.Context(), recoveryServicesRequest(f, recoveryServicesContainer)); err != nil || len(batch.Items) != 1 || len(object(object(batch.Items[0].Normalized[recoveryContainerReview])["consumers"])) != len(ids) {
		t.Fatal("container consumers changed", len(batch.Items), err)
	}
	// Each item is read as often as the only item of a container is.
	for _, id := range ids {
		if calls["GET "+id] != once["GET "+single[0]] {
			t.Fatal("item read count", id, calls["GET "+id], once["GET "+single[0]])
		}
	}
	sweep6Concurrent(t, peak)
}

func TestSweep6RecoveryServicesSnapshotFirstErrorInOrder(t *testing.T) {
	for _, kind := range []string{recoveryServicesItem, recoveryServicesContainer} {
		f, ids := recoveryServicesSweepFixture(t, 12)
		f.override = func(q *http.Request) (*http.Response, bool) {
			code := map[string]string{ids[2]: "FirstDenied", ids[6]: "LaterDenied"}[strings.ToLower(q.URL.Path)]
			if code == "" {
				return nil, false
			}
			return jsonResponse(403, map[string]any{"error": map[string]any{"code": code}}, nil), true
		}
		for range 5 {
			if _, err := f.runtime.List(t.Context(), recoveryServicesRequest(f, kind)); err == nil || !strings.Contains(err.Error(), "FirstDenied") {
				t.Fatal("earlier item failure lost", kind, err)
			}
		}
	}
}
