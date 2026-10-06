package azure

import (
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/loomx-ai/steward/internal/app/governance"
	"github.com/loomx-ai/steward/internal/core/asset"
)

func sweep4Denied(code string) *http.Response {
	return jsonResponse(403, map[string]any{"error": map[string]any{"code": code}}, nil)
}

// sweep4Track wraps a client's transport so concurrent requests for matching
// paths are visible to a peak tracker, even behind a serializing fixture.
func sweep4Track(c *client, match func(string) bool, wait time.Duration) (map[string]int, *peakTracker) {
	var mu sync.Mutex
	calls, peak := map[string]int{}, &peakTracker{}
	inner := c.http.Transport
	c.http.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		path := strings.ToLower(req.URL.Path)
		mu.Lock()
		calls[req.Method+" "+path]++
		mu.Unlock()
		if match(path) {
			peak.hold(wait)
		}
		return inner.RoundTrip(req)
	})
	return calls, peak
}

func sweep4Peak(t *testing.T, peak *peakTracker) {
	t.Helper()
	if peak.peak < 2 || peak.peak > detailReadConcurrency {
		t.Fatal("reads were not concurrent within the bound", peak.peak)
	}
}

func TestSweep4FleetIndexReadsConcurrentlyInOrder(t *testing.T) {
	var rows []any
	var ids []string
	for i := range 12 {
		raw := fleetTestBody(t, fleetMemberType, fmt.Sprintf("member%02d", i))
		rows = append(rows, raw)
		ids = append(ids, text(raw["id"]))
	}
	scope := fleetParent(ids[0], fleetMemberType)
	var mu sync.Mutex
	calls, peak := map[string]int{}, &peakTracker{}
	var fail func(string) *http.Response
	c := directClient(func(r *http.Request) (*http.Response, error) {
		path := strings.ToLower(r.URL.Path)
		if path == scope+"/members" {
			mu.Lock()
			defer mu.Unlock()
			return jsonResponse(200, map[string]any{"value": rows}, nil), nil
		}
		mu.Lock()
		calls[path]++
		mu.Unlock()
		peak.hold(5 * time.Millisecond)
		if fail != nil {
			if res := fail(path); res != nil {
				return res, nil
			}
		}
		i := slices.Index(ids, path)
		if i < 0 {
			t.Fatal("unexpected fleet request", r.URL)
		}
		return jsonResponse(200, rows[i], nil), nil
	})
	items, _, err := c.fleetIndex(t.Context(), fleetMemberType, scope)
	if err != nil || len(items) != len(ids) {
		t.Fatal("members changed", len(items), err)
	}
	for _, id := range ids {
		if calls[id] != 1 || text(items[id]["id"]) != id {
			t.Fatal("member not read exactly once", id, calls[id])
		}
	}
	sweep4Peak(t, peak)
	fail = func(path string) *http.Response {
		switch path {
		case ids[3]:
			return sweep4Denied("FirstDenied")
		case ids[9]:
			return sweep4Denied("LaterDenied")
		}
		return nil
	}
	for range 5 {
		if _, _, err := c.fleetIndex(t.Context(), fleetMemberType, scope); err == nil || !strings.Contains(err.Error(), "FirstDenied") {
			t.Fatal("first failure in list order lost", err)
		}
	}
	fail = nil
	rows[5] = rows[4] // A duplicate row fails after the rows before it.
	clear(calls)
	if _, _, err := c.fleetIndex(t.Context(), fleetMemberType, scope); err == nil || !strings.Contains(err.Error(), "invalid_fleet_list_identity") {
		t.Fatal("duplicate member accepted", err)
	}
	for i, id := range ids {
		if want := map[bool]int{true: 1, false: 0}[i < 5]; calls[id] != want {
			t.Fatal("wrong reads around the invalid row", id, calls[id])
		}
	}
}

func TestSweep4EventHubClusterNamespacesConcurrentInOrder(t *testing.T) {
	s, r, assets := eventHubClusterScenario(t)
	root := assets[0]
	listPath := root.Identity.NativeID + "/namespaces"
	first := text(object(s.lists[listPath][0])["id"])
	for i := range 12 {
		id := first[:strings.LastIndex(first, "/")] + fmt.Sprintf("/sweepns-%02d", i)
		ns := map[string]any{"id": id, "name": last(id), "type": eventHubNamespaceType, "location": "South Central US", "properties": map[string]any{"createdAt": "2020-01-01T00:00:00Z", "provisioningState": "Succeeded", "clusterArmId": root.Identity.NativeID}}
		s.add(ns, "2024-01-01")
		s.lists[listPath] = append(s.lists[listPath], map[string]any{"id": id, "type": eventHubNamespaceType})
	}
	var ids []string
	for _, row := range s.lists[listPath] {
		id, _, _ := parseID(text(object(row)["id"]))
		ids = append(ids, strings.ToLower(id))
	}
	slices.Sort(ids)
	c, _ := r.resolve(t.Context(), "connection")
	calls, peak := countCalls(s, func(path string) bool { return strings.Contains(path, "/namespaces/sweepns-") }, 5*time.Millisecond)
	children, err := c.eventHubClusterNamespaces(t.Context(), root.Identity)
	if err != nil || len(children) != len(ids) {
		t.Fatal("namespaces changed", len(children), err)
	}
	for i, child := range children {
		if strings.ToLower(child.id) != ids[i] || calls["GET "+ids[i]] != 2 {
			t.Fatal("namespace order or read count changed", child.id, calls["GET "+ids[i]])
		}
	}
	if calls["GET "+strings.ToLower(listPath)] != 2 {
		t.Fatal("member list not read twice", calls["GET "+strings.ToLower(listPath)])
	}
	sweep4Peak(t, peak)
	s.handle = func(req *http.Request) (*http.Response, bool) {
		switch strings.ToLower(req.URL.Path) {
		case ids[3]:
			return sweep4Denied("FirstDenied"), true
		case ids[9]:
			return sweep4Denied("LaterDenied"), true
		}
		return nil, false
	}
	for range 5 {
		if _, err := c.eventHubClusterNamespaces(t.Context(), root.Identity); err == nil || !strings.Contains(err.Error(), "FirstDenied") {
			t.Fatal("first failure in order lost", err)
		}
	}
}

func TestSweep4DomainZoneUnusedConcurrentInOrder(t *testing.T) {
	s, r, _ := domainScenario(t, false)
	collection := "/subscriptions/" + testSubscription + "/providers/microsoft.domainregistration/domains"
	base := object(s.lists[collection][0])
	var ids []string
	for i := range 12 {
		payload, _ := json.Marshal(base)
		var raw map[string]any
		json.Unmarshal(payload, &raw)
		id := strings.Join(strings.Split(text(base["id"]), "/")[:8], "/") + fmt.Sprintf("/sweep%02d.com", i)
		raw["id"], raw["name"] = id, last(id)
		s.add(raw, domainVersion)
		s.lists[collection] = append(s.lists[collection], raw)
		ids = append(ids, strings.ToLower(id))
	}
	c, _ := r.resolve(t.Context(), "connection")
	calls, peak := countCalls(s, func(path string) bool { return strings.Contains(path, "/domains/sweep") }, 5*time.Millisecond)
	other := resourceID(publicDNSZoneType, "unused.example")
	if err := c.domainZoneUnused(t.Context(), other); err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		if calls["GET "+id] != 2 {
			t.Fatal("domain not read once per pass", id, calls["GET "+id])
		}
	}
	sweep4Peak(t, peak)
	if err := c.domainZoneUnused(t.Context(), text(object(base["properties"])["dnsZoneId"])); err == nil || !strings.Contains(err.Error(), "dns_zone_has_registered_domain") {
		t.Fatal("registered domain not found", err)
	}
	s.handle = func(req *http.Request) (*http.Response, bool) {
		switch strings.ToLower(req.URL.Path) {
		case ids[3]:
			return sweep4Denied("FirstDenied"), true
		case ids[9]:
			return sweep4Denied("LaterDenied"), true
		}
		return nil, false
	}
	for range 5 {
		if err := c.domainZoneUnused(t.Context(), other); err == nil || !strings.Contains(err.Error(), "FirstDenied") {
			t.Fatal("first failure in order lost", err)
		}
	}
	s.handle = func(req *http.Request) (*http.Response, bool) {
		if strings.ToLower(req.URL.Path) == ids[2] {
			return jsonResponse(404, map[string]any{"error": map[string]any{"code": "ResourceNotFound"}}, nil), true
		}
		return nil, false
	}
	if err := c.domainZoneUnused(t.Context(), other); err != nil {
		t.Fatal("own 404 of a listed domain is skipped", err)
	}
}

func TestSweep4GraphKnownObjectsConcurrentInOrder(t *testing.T) {
	f := newGraphFixture(t)
	var ids, known []string
	for i := range 12 {
		id := fmt.Sprintf("00000000-0000-4000-8000-%012d", i)
		f.users[id] = map[string]any{"id": id, "displayName": id, "accountEnabled": true}
		f.omitted[id] = true
		ids, known = append(ids, id), append(known, rbacPrincipalSelector(testTenant, id))
	}
	for _, i := range []int{2, 7} {
		delete(f.users, ids[i])
	}
	var mu sync.Mutex
	calls, peak := map[string]int{}, &peakTracker{}
	var fail map[string]string
	f.override = func(q *http.Request) (*http.Response, bool) {
		parts := strings.Split(strings.TrimPrefix(q.URL.Path, "/v1.0/users/"), "/")
		if q.URL.Host != "graph.microsoft.com" || !strings.HasPrefix(q.URL.Path, "/v1.0/users/") || len(parts) != 1 {
			return nil, false
		}
		mu.Lock()
		calls[parts[0]]++
		mu.Unlock()
		peak.hold(5 * time.Millisecond)
		if code := fail[parts[0]]; code != "" {
			return jsonResponse(403, map[string]any{"error": map[string]any{"code": code}}, nil), true
		}
		return nil, false
	}
	c, err := f.runtime.resolve(t.Context(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	batch, err := f.runtime.listGraphDirectory(t.Context(), c, graphRequest(f, graphUserType, known...))
	if err != nil || len(batch.Items) != 2+10 || !slices.Equal(batch.AbsentNativeIDs, []string{known[2], known[7]}) {
		t.Fatal("known reconciliation changed", len(batch.Items), batch.AbsentNativeIDs, err)
	}
	for i, item := range batch.Items[2:] {
		if want := slices.DeleteFunc(slices.Clone(ids), func(id string) bool { return id == ids[2] || id == ids[7] })[i]; item.Normalized["object_id"] != want {
			t.Fatal("known items out of order", i, item.Normalized["object_id"])
		}
	}
	for _, id := range ids {
		if calls[id] != 1 {
			t.Fatal("known object not read exactly once", id, calls[id])
		}
	}
	sweep4Peak(t, peak)
	fail = map[string]string{ids[3]: "FirstDenied", ids[9]: "LaterDenied"}
	for range 5 {
		if _, err := f.runtime.listGraphDirectory(t.Context(), c, graphRequest(f, graphUserType, known...)); err == nil || !strings.Contains(err.Error(), "FirstDenied") {
			t.Fatal("first failure in order lost", err)
		}
	}
}

func TestSweep4HybridComputeSnapshotConcurrentInOrder(t *testing.T) {
	f := newHybridInventoryFixture(t)
	base := strings.ToLower(resourceID(hybridMachineType, "machine"))
	ids := []string{base}
	for i := range 11 {
		payload, _ := json.Marshal(f.values[base])
		var raw map[string]any
		json.Unmarshal(payload, &raw)
		id := base + fmt.Sprintf("%02d", i)
		raw["id"], raw["name"] = id, last(id)
		f.values[id] = raw
		ids = append(ids, id)
	}
	slices.Sort(ids)
	var fail map[string]string
	f.override = func(req *http.Request) (*http.Response, bool) {
		if code := fail[strings.ToLower(req.URL.Path)]; code != "" {
			return sweep4Denied(code), true
		}
		return nil, false
	}
	calls, peak := sweep4Track(f.client, func(path string) bool { return strings.HasSuffix(path, "/extensions") }, 5*time.Millisecond)
	items, bindings, _, err := f.runtime.hybridComputeSnapshot(t.Context(), f.client, f.request(hybridMachineType))
	if err != nil || len(items) != len(ids) {
		t.Fatal("machines changed", len(items), err)
	}
	for i, item := range items {
		if item.NativeID != ids[i] || bindings[ids[i]] == nil || calls["GET "+ids[i]+"/extensions"] != 1 {
			t.Fatal("machine order, binding or child list count changed", item.NativeID, calls["GET "+ids[i]+"/extensions"])
		}
	}
	sweep4Peak(t, peak)
	fail = map[string]string{ids[3] + "/extensions": "FirstDenied", ids[9] + "/extensions": "LaterDenied"}
	for range 5 {
		if _, _, _, err := f.runtime.hybridComputeSnapshot(t.Context(), f.client, f.request(hybridMachineType)); err == nil || !strings.Contains(err.Error(), "FirstDenied") {
			t.Fatal("first failure in order lost", err)
		}
	}
}

func sweep4Clone(raw map[string]any) map[string]any {
	payload, _ := json.Marshal(raw)
	var out map[string]any
	json.Unmarshal(payload, &out)
	return out
}

// sweep4FailNth fails the nth GET of each listed path (counted in calls).
func sweep4FailNth(calls map[string]int, n int, codes map[string]string) func(*http.Request) (*http.Response, bool) {
	return func(req *http.Request) (*http.Response, bool) {
		path := strings.ToLower(req.URL.Path)
		if code := codes[path]; code != "" && calls["GET "+path] == n {
			return sweep4Denied(code), true
		}
		return nil, false
	}
}

func sweep4FleetMembers(t *testing.T, f *fleetFixture, parent string, mesh string) []string {
	t.Helper()
	base := f.resources[parent+"/members/member1"]
	ids := []string{parent + "/members/member1"}
	for i := range 11 {
		raw := sweep4Clone(base)
		id := parent + fmt.Sprintf("/members/sweep%02d", i)
		raw["id"], raw["name"] = id, last(id)
		props := object(raw["properties"])
		props["clusterResourceId"] = resourceID(aksType, fmt.Sprintf("sweepcluster%02d", i))
		if mesh != "" {
			object(object(props["meshProperties"])["ciliumProperties"])["id"] = float64(i + 2)
		}
		f.resources[id] = raw
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}

func TestSweep4FleetMeshMemberRereadsInOrder(t *testing.T) {
	f, meshID := newFleetMeshFixture(t)
	ids := sweep4FleetMembers(t, f, fleetParent(meshID, fleetMeshType), meshID)
	c, err := f.runtime.resolve(t.Context(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	_, peak := sweep4Track(c, func(path string) bool { return strings.Contains(path, "/members/sweep") }, 5*time.Millisecond)
	clear(f.calls)
	state, selected, err := c.readFleetMesh(t.Context(), meshID, f.resources[meshID], nil)
	if err != nil || len(object(state["members"])) != len(ids) || len(selected) != len(ids) {
		t.Fatal("mesh members changed", len(selected), err)
	}
	for _, id := range ids {
		if f.calls["GET "+id] != 2 || text(selected[id]["id"]) != id {
			t.Fatal("member not indexed and re-read once", id, f.calls["GET "+id])
		}
	}
	sweep4Peak(t, peak)
	f.override = sweep4FailNth(f.calls, 2, map[string]string{ids[3]: "FirstDenied", ids[9]: "LaterDenied"})
	for range 5 {
		clear(f.calls)
		if _, _, err := c.readFleetMesh(t.Context(), meshID, f.resources[meshID], nil); err == nil || !strings.Contains(err.Error(), "FirstDenied") {
			t.Fatal("first re-read failure in order lost", err)
		}
	}
}

func TestSweep4FleetRootChildRereadsInOrder(t *testing.T) {
	f := newFleetFixture(t)
	fleet := strings.ToLower(resourceID(fleetType, "fleet1"))
	ids := sweep4FleetMembers(t, f, fleet, "")
	c, err := f.runtime.resolve(t.Context(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	_, peak := sweep4Track(c, func(path string) bool { return strings.Contains(path, "/members/sweep") }, 5*time.Millisecond)
	clear(f.calls)
	children, err := c.fleetRootChildren(t.Context(), fleet, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		// Members index, re-read, then the mesh scan's member index.
		if object(children[id])["kind"] != fleetMemberType || f.calls["GET "+id] != 3 {
			t.Fatal("member child not re-read once", id, f.calls["GET "+id])
		}
	}
	sweep4Peak(t, peak)
	f.override = sweep4FailNth(f.calls, 2, map[string]string{ids[3]: "FirstDenied", ids[9]: "LaterDenied"})
	for range 5 {
		clear(f.calls)
		if _, err := c.fleetRootChildren(t.Context(), fleet, nil); err == nil || !strings.Contains(err.Error(), "FirstDenied") {
			t.Fatal("first re-read failure in order lost", err)
		}
	}
}

func sweep4HubMemberIDs(state map[string]any) []string {
	var ids []string
	for _, id := range slices.Sorted(maps.Keys(object(state["members"]))) {
		if _, ok := findType(text(object(object(state["members"])[id])["kind"])); ok {
			ids = append(ids, strings.ToLower(id))
		}
	}
	return ids
}

func TestSweep4FleetRootResidualReadsInOrder(t *testing.T) {
	h := newFleetHubMembersFixture(t)
	request := fleetRootCleanupRequest(t, h)
	a := fleetRootInner(t, h, request)
	state := object(request.Asset.Normalized[fleetHubState])
	members := sweep4HubMemberIDs(state)
	var children []string
	for id := range object(state["children"]) {
		children = append(children, strings.ToLower(id))
	}
	if len(members) < 4 || len(children) == 0 {
		t.Fatal("fixture lost its residual set", len(members), len(children))
	}
	tracked := map[string]bool{}
	for _, id := range append(slices.Clone(members), children...) {
		tracked[id] = true
	}
	_, peak := sweep4Track(a.client, func(path string) bool { return tracked[path] }, 5*time.Millisecond)
	clear(h.calls)
	read, err := a.rootResidualReadback(t.Context(), request)
	if err != nil || !read.Exists {
		t.Fatal("residual resources not observed", read, err)
	}
	for id := range tracked {
		if h.calls["GET "+id] != 1 {
			t.Fatal("residual resource not read exactly once", id, h.calls["GET "+id])
		}
	}
	sweep4Peak(t, peak)
	fallback := h.override
	failing := map[string]string{members[1]: "FirstDenied", members[len(members)-1]: "LaterDenied"}
	h.override = func(req *http.Request) (*http.Response, bool) {
		if code := failing[strings.ToLower(req.URL.Path)]; code != "" {
			return sweep4Denied(code), true
		}
		if fallback != nil {
			return fallback(req)
		}
		return nil, false
	}
	for range 5 {
		if _, err := a.rootResidualReadback(t.Context(), request); err == nil || !strings.Contains(err.Error(), "FirstDenied") {
			t.Fatal("first failure in order lost", err)
		}
	}
}

func TestSweep4FleetHubMemberRereadsInOrder(t *testing.T) {
	h := newFleetHubMembersFixture(t)
	request := fleetRootCleanupRequest(t, h)
	a := fleetRootInner(t, h, request)
	state := object(request.Asset.Normalized[fleetHubState])
	members := sweep4HubMemberIDs(state)
	tracked := map[string]bool{}
	for _, id := range members {
		tracked[id] = true
	}
	read := func() (map[string]any, error) {
		groups, err := a.client.insightsGroups(t.Context())
		if err != nil {
			return nil, err
		}
		live, _, err := a.client.readFleetHub(t.Context(), h.fleet, h.fleetFixture.resources[h.fleet], groups, state)
		return live, err
	}
	_, peak := sweep4Track(a.client, func(path string) bool { return tracked[path] }, 5*time.Millisecond)
	clear(h.calls)
	live, err := read()
	if err != nil || a.client.privateConfiguration(fleetHubLifecycleProjection(live)) != a.client.privateConfiguration(fleetHubLifecycleProjection(state)) {
		t.Fatal("hub observation changed", err)
	}
	counts := map[string]int{}
	for _, id := range members {
		counts[id] = h.calls["GET "+id]
		if counts[id] < 1 {
			t.Fatal("hub member not re-read", id)
		}
	}
	sweep4Peak(t, peak)
	// Fail only each member's final read: the recheck after the group walk.
	fallback := h.override
	failing := map[string]string{members[1]: "FirstDenied", members[len(members)-1]: "LaterDenied"}
	h.override = func(req *http.Request) (*http.Response, bool) {
		path := strings.ToLower(req.URL.Path)
		if code := failing[path]; code != "" && h.calls["GET "+path] == counts[path] {
			return sweep4Denied(code), true
		}
		if fallback != nil {
			return fallback(req)
		}
		return nil, false
	}
	for range 5 {
		clear(h.calls)
		if _, err := read(); err == nil || !strings.Contains(err.Error(), "FirstDenied") {
			t.Fatal("first recheck failure in order lost", err)
		}
	}
}

func TestSweep4ElasticSanVolumeContributionsInOrder(t *testing.T) {
	f := newElasticSanVolumeFixture(t)
	base := f.ids[elasticSanVolumeType]
	for i := range 11 {
		raw := sweep4Clone(f.values[base])
		id := base + fmt.Sprintf("-sweep%02d", i)
		raw["id"], raw["name"] = id, last(id)
		object(raw["properties"])["volumeId"] = fmt.Sprintf("00000000-0000-4000-8000-%012d", i)
		f.values[id] = raw
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
	// Volumes are independent: the serial result is each volume's own
	// contribution, appended in asset order.
	var want governance.Contribution
	volumes := 0
	for _, value := range assets {
		if value.Identity.NativeType != elasticSanVolumeType {
			continue
		}
		volumes++
		var one governance.Contribution
		if err := cascades.contributeElasticSanVolumes(t.Context(), append([]asset.Asset{value}, assets[len(assets)-1]), &one); err != nil {
			t.Fatal(err)
		}
		want.Bindings = append(want.Bindings, one.Bindings...)
		want.Relationships = append(want.Relationships, one.Relationships...)
		want.Unresolved = append(want.Unresolved, one.Unresolved...)
	}
	if volumes < 12 || len(want.Bindings) == 0 {
		t.Fatal("fixture lost its volumes or snapshot binding", volumes, len(want.Bindings))
	}
	calls, peak := sweep4Track(f.client, func(path string) bool { return strings.HasSuffix(path, "/snapshots") }, 5*time.Millisecond)
	var got governance.Contribution
	if err := cascades.contributeElasticSanVolumes(t.Context(), assets, &got); err != nil {
		t.Fatal(err)
	}
	wantJSON, _ := json.Marshal(want)
	gotJSON, _ := json.Marshal(got)
	if string(wantJSON) != string(gotJSON) {
		t.Fatal("concurrent contribution differs from the per-volume serial one")
	}
	reads := 0
	for call, n := range calls {
		if strings.HasSuffix(call, "/snapshots") {
			reads += n
		}
	}
	if reads > volumes {
		t.Fatal("a volume listed snapshots more than once", reads, volumes)
	}
	sweep4Peak(t, peak)
	// An earlier volume's read failure wins over a later forged volume; a forged
	// first volume fails before any snapshot read.
	forged := slices.Clone(assets)
	forged[5].Normalized = maps.Clone(forged[5].Normalized)
	forged[5].Normalized[elasticSanSnapshotCleanupProof] = "forged"
	f.override = func(req *http.Request) (*http.Response, bool) {
		if strings.HasSuffix(strings.ToLower(req.URL.Path), "/snapshots") {
			return sweep4Denied("FirstDenied"), true
		}
		return nil, false
	}
	for range 5 {
		if err := cascades.contributeElasticSanVolumes(t.Context(), forged, &governance.Contribution{}); err == nil || !strings.Contains(err.Error(), "FirstDenied") {
			t.Fatal("later forged volume hid the earlier read failure", err)
		}
	}
	forged[0], forged[5] = forged[5], forged[0]
	clear(calls)
	if err := cascades.contributeElasticSanVolumes(t.Context(), forged, &governance.Contribution{}); err == nil || !strings.Contains(err.Error(), "elastic_san_volume_graph_changed") {
		t.Fatal("forged volume accepted", err)
	}
	for call := range calls {
		if strings.HasSuffix(call, "/snapshots") {
			t.Fatal("read a volume at or after the forged one", call)
		}
	}
}

func sweep4RegistrationScenario(t *testing.T, count int) (*dnsScenario, *client, map[string]any, []string) {
	t.Helper()
	s := newDNSScenario()
	zoneID := resourceID(privateDNSZoneType, "internal.example.com")
	zone := map[string]any{"id": zoneID, "name": "internal.example.com", "location": "global", "etag": "zone-etag"}
	s.add(zone, "2024-06-01")
	var links, records []any
	var vnets []string
	for i := range count {
		vnetID := resourceID(vnetType, fmt.Sprintf("vnet%02d", i))
		vnet := map[string]any{"id": vnetID, "name": last(vnetID), "etag": "network-etag", "properties": map[string]any{"addressSpace": map[string]any{"addressPrefixes": []any{fmt.Sprintf("10.%d.0.0/16", i+1)}}}}
		link := map[string]any{"id": zoneID + fmt.Sprintf("/virtualNetworkLinks/link%02d", i), "name": fmt.Sprintf("link%02d", i), "location": "global", "etag": fmt.Sprint("link-etag", i), "properties": map[string]any{"registrationEnabled": true, "virtualNetwork": map[string]any{"id": vnetID}}}
		record := map[string]any{"id": zoneID + fmt.Sprintf("/A/vm%02d", i), "name": fmt.Sprintf("vm%02d", i), "etag": fmt.Sprint("record-etag", i), "properties": map[string]any{"isAutoRegistered": true, "ttl": 10, "aRecords": []any{map[string]any{"ipv4Address": fmt.Sprintf("10.%d.0.4", i+1)}}}}
		s.add(vnet, "2024-05-01")
		s.add(link, "2024-06-01")
		s.add(record, "2024-06-01")
		links, records, vnets = append(links, link), append(records, record), append(vnets, strings.ToLower(vnetID))
	}
	for _, kind := range dnsChildTypes(privateDNSZoneType) {
		s.lists[strings.ToLower(zoneID+"/"+last(kind))] = []any{}
	}
	s.lists[strings.ToLower(zoneID+"/virtualNetworkLinks")] = links
	s.lists[strings.ToLower(zoneID+"/A")] = records
	c, err := s.runtime(t).resolve(t.Context(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	return s, c, object(links[0]), vnets
}

func TestSweep4PrivateDNSRegistrationVNetReadsInOrder(t *testing.T) {
	s, c, link, vnets := sweep4RegistrationScenario(t, 12)
	id, _, _ := parseID(text(link["id"]))
	parent := asset.Identity{NativeID: id, NativeType: privateDNSLinkType}
	calls, peak := countCalls(s, func(path string) bool { return strings.Contains(path, "/virtualnetworks/vnet") }, 5*time.Millisecond)
	children, err := c.privateDNSRegistrationChildren(t.Context(), parent, link)
	if err != nil || len(children) != 1 || !strings.HasSuffix(strings.ToLower(children[0].id), "/a/vm00") {
		t.Fatal("registration ownership changed", children, err)
	}
	for _, vnet := range vnets {
		if calls["GET "+vnet] != 1 {
			t.Fatal("VNet not read exactly once", vnet, calls["GET "+vnet])
		}
	}
	sweep4Peak(t, peak)
	s.handle = func(req *http.Request) (*http.Response, bool) {
		switch strings.ToLower(req.URL.Path) {
		case vnets[3]:
			return sweep4Denied("FirstDenied"), true
		case vnets[9]:
			return sweep4Denied("LaterDenied"), true
		}
		return nil, false
	}
	for range 5 {
		if _, err := c.privateDNSRegistrationChildren(t.Context(), parent, link); err == nil || !strings.Contains(err.Error(), "FirstDenied") {
			t.Fatal("first failure in link order lost", err)
		}
	}
}

func TestSweep4DomainChildrenSiteWalksInOrder(t *testing.T) {
	s, r, _ := domainScenario(t, true)
	var sitesPath string
	for path := range s.lists {
		if strings.HasSuffix(strings.ToLower(path), "/providers/microsoft.web/sites") {
			sitesPath = path
		}
	}
	base := object(s.lists[sitesPath][0])
	ids := []string{strings.ToLower(text(base["id"]))}
	for i := range 11 {
		raw := sweep4Clone(base)
		id := text(base["id"])[:strings.LastIndex(text(base["id"]), "/")] + fmt.Sprintf("/sweepsite%02d", i)
		raw["id"], raw["name"] = id, last(id)
		s.add(raw, appServiceVersion)
		s.lists[sitesPath] = append(s.lists[sitesPath], raw)
		for _, collection := range []string{"/hostnamebindings", "/slots"} {
			s.lists[strings.ToLower(id)+collection], s.version[strings.ToLower(id)+collection] = []any{}, appServiceVersion
		}
		ids = append(ids, strings.ToLower(id))
	}
	c, err := r.resolve(t.Context(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	var domain map[string]any
	for _, raw := range s.records {
		if strings.EqualFold(text(raw["type"]), domainType) {
			domain = raw
		}
	}
	parent := asset.Identity{NativeID: text(domain["id"]), NativeType: domainType}
	calls, peak := countCalls(s, func(path string) bool { return strings.Contains(path, "/sites/sweepsite") }, 5*time.Millisecond)
	children, err := c.domainChildren(t.Context(), parent, domain, nil)
	if err != nil {
		t.Fatal(err)
	}
	bindings := 0
	for _, child := range children {
		if isAppBinding(child.kind) {
			bindings++
		}
	}
	if bindings != 2 {
		t.Fatal("linked bindings changed", bindings)
	}
	for _, id := range ids {
		// Per pass: the site's own GET and its binding walk's parent re-read.
		if calls["GET "+id] != 4 {
			t.Fatal("site read count changed", id, calls["GET "+id])
		}
	}
	sweep4Peak(t, peak)
	s.handle = func(req *http.Request) (*http.Response, bool) {
		switch strings.ToLower(req.URL.Path) {
		case ids[3]:
			return sweep4Denied("FirstDenied"), true
		case ids[9]:
			return sweep4Denied("LaterDenied"), true
		}
		return nil, false
	}
	for range 5 {
		if _, err := c.domainChildren(t.Context(), parent, domain, nil); err == nil || !strings.Contains(err.Error(), "FirstDenied") {
			t.Fatal("first failure in site order lost", err)
		}
	}
}
