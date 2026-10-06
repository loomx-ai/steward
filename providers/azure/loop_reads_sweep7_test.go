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
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func sweep7Denied(code string) *http.Response {
	return jsonResponse(403, map[string]any{"error": map[string]any{"code": code}}, nil)
}

// sweep7Fail wraps the scenario's handler so the given paths fail with codes.
func sweep7Fail(s *dnsScenario, failures map[string]string) {
	previous := s.handle
	s.handle = func(req *http.Request) (*http.Response, bool) {
		if code := failures[strings.ToLower(req.URL.Path)]; code != "" && req.Method == "GET" {
			return sweep7Denied(code), true
		}
		if previous != nil {
			return previous(req)
		}
		return nil, false
	}
}

func sweep7ClusterPrerequisites(t *testing.T, count int) (*dnsScenario, *action, contracts.ActionRequest, []string) {
	t.Helper()
	s, r, assets := streamAnalyticsScenario(t, true)
	c, err := r.resolve(t.Context(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	cluster := cdnAsset(t, assets, streamAnalyticsClusterType)
	kind, _ := findType(streamAnalyticsClusterType)
	a := &action{client: c, kind: kind, id: cluster.Identity.NativeID}
	request := contracts.ActionRequest{Asset: cluster}
	var ids []string
	for i := range count {
		id := fmt.Sprintf("/subscriptions/%s/resourcegroups/testgroup/providers/microsoft.streamanalytics/streamingjobs/sweep%02d", testSubscription, i)
		s.gone[id] = true
		job := asset.Asset{ID: asset.AssetID(fmt.Sprint("sweep-job-", i)), Identity: asset.Identity{Provider: asset.ProviderAzure, ConnectionID: cluster.Identity.ConnectionID, Partition: cluster.Identity.Partition, NativeID: id, NativeType: streamAnalyticsJobType}, Normalized: map[string]any{"cluster": map[string]any{"id": cluster.Identity.NativeID}}}
		request.PrerequisiteDeletions = append(request.PrerequisiteDeletions, contracts.ActionImpact{Asset: job, ControllerID: cluster.ID, Delete: true})
		ids = append(ids, id)
	}
	return s, a, request, ids
}

// Prerequisite absence GETs run concurrently, once each, and the first failure
// in prerequisite order wins over a later failure or a later live resource.
func TestSweep7ServicePrerequisitesAbsentConcurrentInOrder(t *testing.T) {
	s, a, request, ids := sweep7ClusterPrerequisites(t, 12)
	isJob := func(path string) bool { return strings.Contains(path, "/streamingjobs/sweep") }
	calls, peak := countCalls(s, isJob, 5*time.Millisecond)
	if err := a.servicePrerequisitesAbsent(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		if calls["GET "+id] != 1 {
			t.Fatal("prerequisite not read exactly once", id, calls["GET "+id])
		}
	}
	if peak.peak < 2 || peak.peak > detailReadConcurrency {
		t.Fatal("prerequisite reads were not concurrent within the bound", peak.peak)
	}
	delete(s.gone, ids[9])
	s.records[ids[9]] = map[string]any{"id": ids[9], "name": last(ids[9]), "type": streamAnalyticsJobType}
	base := s.handle
	sweep7Fail(s, map[string]string{ids[3]: "FirstDenied", ids[10]: "LaterDenied"})
	for range 5 {
		if err := a.servicePrerequisitesAbsent(t.Context(), request); err == nil || !strings.Contains(err.Error(), "FirstDenied") {
			t.Fatal("first failure in prerequisite order lost", err)
		}
	}
	s.handle = base
	if err := a.servicePrerequisitesAbsent(t.Context(), request); err == nil || !strings.Contains(err.Error(), "service_prerequisite_still_exists") {
		t.Fatal("live prerequisite accepted", err)
	}
	// A duplicate row fails after the rows before it and stops later reads.
	request.PrerequisiteDeletions[6] = request.PrerequisiteDeletions[5]
	clear(calls)
	if err := a.servicePrerequisitesAbsent(t.Context(), request); err == nil || !strings.Contains(err.Error(), "invalid_service_prerequisite") {
		t.Fatal("duplicate prerequisite accepted", err)
	}
	for i, id := range ids {
		if want := map[bool]int{true: 1, false: 0}[i < 6]; calls["GET "+id] != want {
			t.Fatal("wrong reads around the invalid row", id, calls["GET "+id])
		}
	}
}

func sweep7ClusterJobs(t *testing.T, count int) (*dnsScenario, *client, asset.Identity, map[string]any, []any, []string) {
	t.Helper()
	s, r, assets := streamAnalyticsScenario(t, true)
	c, err := r.resolve(t.Context(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	cluster := cdnAsset(t, assets, streamAnalyticsClusterType)
	template := cdnAsset(t, assets, streamAnalyticsJobType)
	var rows []any
	var ids []string
	for i := range count {
		payload, _ := json.Marshal(s.records[template.Identity.NativeID])
		var raw map[string]any
		json.Unmarshal(payload, &raw)
		props := object(raw["properties"])
		for _, key := range []string{"inputs", "outputs", "functions", "transformation"} {
			delete(props, key)
		}
		id := fmt.Sprintf("/subscriptions/%s/resourcegroups/testgroup/providers/microsoft.streamanalytics/streamingjobs/sweep%02d", testSubscription, i)
		raw["id"], raw["name"] = id, last(id)
		props["cluster"] = map[string]any{"id": cluster.Identity.NativeID}
		s.add(raw, streamAnalyticsVersion)
		rows = append(rows, map[string]any{"id": id, "type": streamAnalyticsJobType, "jobState": props["jobState"]})
		ids = append(ids, id)
	}
	list := strings.ToLower(cluster.Identity.NativeID) + "/liststreamingjobs"
	previous := s.handle
	s.handle = func(req *http.Request) (*http.Response, bool) {
		if req.Method == "POST" && strings.ToLower(req.URL.Path) == list {
			return jsonResponse(200, map[string]any{"value": rows, "nextLink": nil}, nil), true
		}
		return previous(req)
	}
	return s, c, cluster.Identity, s.records[cluster.Identity.NativeID], rows, ids
}

// A cluster's job GETs run concurrently and are checked in list order.
func TestSweep7StreamAnalyticsClusterJobsConcurrentInOrder(t *testing.T) {
	s, c, cluster, raw, rows, ids := sweep7ClusterJobs(t, 12)
	isJob := func(path string) bool { return strings.Contains(path, "/streamingjobs/sweep") }
	calls, peak := countCalls(s, isJob, 5*time.Millisecond)
	children, err := c.streamAnalyticsClusterJobs(t.Context(), cluster, raw)
	if err != nil || len(children) != len(ids) {
		t.Fatal("cluster jobs changed", len(children), err)
	}
	for i, id := range ids {
		if children[i].id != id || children[i].kind != streamAnalyticsJobType || calls["GET "+id] != 1 {
			t.Fatal("job out of order or not read exactly once", i, children[i].id, calls["GET "+id])
		}
	}
	if peak.peak < 2 || peak.peak > detailReadConcurrency {
		t.Fatal("job reads were not concurrent within the bound", peak.peak)
	}
	base := s.handle
	sweep7Fail(s, map[string]string{ids[3]: "FirstDenied", ids[9]: "LaterDenied"})
	for range 5 {
		if _, err := c.streamAnalyticsClusterJobs(t.Context(), cluster, raw); err == nil || !strings.Contains(err.Error(), "FirstDenied") {
			t.Fatal("first failure in list order lost", err)
		}
	}
	object(rows[7])["type"] = "Microsoft.Compute/disks"
	for range 3 {
		if _, err := c.streamAnalyticsClusterJobs(t.Context(), cluster, raw); err == nil || !strings.Contains(err.Error(), "FirstDenied") {
			t.Fatal("later invalid row hid the earlier read failure", err)
		}
	}
	object(rows[7])["type"] = streamAnalyticsJobType
	object(rows[5])["type"] = "Microsoft.Compute/disks"
	s.handle = base
	clear(calls)
	if _, err := c.streamAnalyticsClusterJobs(t.Context(), cluster, raw); err == nil || !strings.Contains(err.Error(), "invalid_stream_analytics_cluster_job") {
		t.Fatal("invalid job row accepted", err)
	}
	for i, id := range ids {
		if want := map[bool]int{true: 1, false: 0}[i < 5]; calls["GET "+id] != want {
			t.Fatal("wrong reads around the invalid row", id, calls["GET "+id])
		}
	}
}

func sweep7WAFPolicies(t *testing.T, count int) (*dnsScenario, *serviceCascades, []asset.Asset, []string) {
	t.Helper()
	s, r, assets := wafScenario(t)
	template := cdnAsset(t, assets, cdnWAFType)
	var ids []string
	for i := range count {
		payload, _ := json.Marshal(s.records[template.Identity.NativeID])
		var raw map[string]any
		json.Unmarshal(payload, &raw)
		for field := range wafLinkFields(cdnWAFType) {
			object(raw["properties"])[field] = []any{}
		}
		id := fmt.Sprintf("/subscriptions/%s/resourcegroups/test/providers/microsoft.cdn/cdnwebapplicationfirewallpolicies/sweep%02d", testSubscription, i)
		raw["id"], raw["name"] = id, last(id)
		s.add(raw, "2025-12-01")
		assets = append(assets, dnsAsset(t, r, raw))
		ids = append(ids, id)
	}
	c, err := r.resolve(t.Context(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	return s, &serviceCascades{client: c, connectionID: "connection"}, assets, ids
}

// WAF policies are reviewed concurrently: each policy keeps its own before and
// after GET, results merge in asset order, and the first failing policy wins.
func TestSweep7WAFReferencesConcurrentInOrder(t *testing.T) {
	s, cascades, assets, ids := sweep7WAFPolicies(t, 12)
	isPolicy := func(path string) bool { return strings.Contains(path, "firewallpolicies/sweep") }
	calls, peak := countCalls(s, isPolicy, 5*time.Millisecond)
	var result governance.Contribution
	if err := cascades.contributeWAFReferences(t.Context(), assets, &result); err != nil {
		t.Fatal(err)
	}
	// Only the scenario's two linked policies contribute, in asset order.
	cdn, frontDoor := cdnAsset(t, assets, cdnWAFType), cdnAsset(t, assets, frontDoorWAFType)
	if len(result.Relationships) != 2 || result.Relationships[0].SourceAssetID != cdn.ID || result.Relationships[1].SourceAssetID != frontDoor.ID || len(result.Unresolved) != 0 {
		t.Fatalf("WAF contribution changed %+v", result)
	}
	for _, id := range ids {
		if calls["GET "+id] != 2 {
			t.Fatal("policy not read before and after exactly once", id, calls["GET "+id])
		}
	}
	if peak.peak < 2 || peak.peak > detailReadConcurrency {
		t.Fatal("policy reads were not concurrent within the bound", peak.peak)
	}
	sweep7Fail(s, map[string]string{ids[3]: "FirstDenied", ids[9]: "LaterDenied"})
	for range 5 {
		result = governance.Contribution{}
		if err := cascades.contributeWAFReferences(t.Context(), assets, &result); err == nil || !strings.Contains(err.Error(), "FirstDenied") {
			t.Fatal("first failure in asset order lost", err)
		}
	}
}

// Listed Synapse artifacts are read, re-read and reviewed concurrently, and
// the items and the first failure follow list order.
func TestSweep7SynapseDataListConcurrentInOrder(t *testing.T) {
	f := newSynapseDataInventoryFixture(t)
	template := f.items[synapseNotebookType]
	var rows []any
	byName := map[string]map[string]any{}
	var names []string
	for i := range 12 {
		raw := batchClone(template)
		name := fmt.Sprintf("nb%02d", i)
		raw["id"], raw["name"] = text(f.workspace["id"])+"/notebooks/"+name, name
		rows = append(rows, raw)
		byName[name] = raw
		names = append(names, name)
	}
	var mu sync.Mutex
	failures := map[string]string{}
	f.intercept = func(q *http.Request) (*http.Response, bool) {
		if q.URL.Host != "first.dev.azuresynapse.net" {
			return nil, false
		}
		if q.URL.Path == "/notebooks" {
			return jsonResponse(200, map[string]any{"value": rows}, nil), true
		}
		if name, ok := strings.CutPrefix(q.URL.Path, "/notebooks/"); ok && byName[name] != nil {
			mu.Lock()
			code := failures[name]
			mu.Unlock()
			if code != "" {
				return sweep7Denied(code), true
			}
			return jsonResponse(200, byName[name], nil), true
		}
		return nil, false
	}
	peak := &peakTracker{}
	inner := f.runtime.transport
	f.runtime.transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Host == "first.dev.azuresynapse.net" && strings.HasPrefix(req.URL.Path, "/notebooks/") {
			peak.hold(5 * time.Millisecond)
		}
		return inner.RoundTrip(req)
	})
	request := synapseDataInventoryRequest(f.runtime, synapseNotebookType)
	batch, err := f.runtime.List(t.Context(), request)
	if err != nil || !batch.Complete || len(batch.Items) != len(names) {
		t.Fatal("notebook inventory changed", len(batch.Items), err)
	}
	reads := f.reads["/notebooks/"+names[0]]
	for i, name := range names {
		if batch.Items[i].NativeID != strings.ToLower(text(byName[name]["id"])) || f.reads["/notebooks/"+name] != reads {
			t.Fatal("notebook out of order or read a different number of times", name, f.reads["/notebooks/"+name], reads)
		}
	}
	// The list detail read, the post-reference re-read and the review re-read.
	if reads != 3 {
		t.Fatal("unexpected per-notebook reads", reads)
	}
	if peak.peak < 2 || peak.peak > detailReadConcurrency {
		t.Fatal("notebook reads were not concurrent within the bound", peak.peak)
	}
	mu.Lock()
	failures["nb03"], failures["nb09"] = "FirstDenied", "LaterDenied"
	mu.Unlock()
	for range 5 {
		if _, err := f.runtime.List(t.Context(), request); err == nil || !strings.Contains(err.Error(), "FirstDenied") {
			t.Fatal("first failure in list order lost", err)
		}
	}
}

// Contribute prefetches monitor parents' reference reads concurrently and
// consumes them in parent order, so the first failing parent still wins.
func TestSweep7ContributeMonitorParentsConcurrentInOrder(t *testing.T) {
	f := newMonitorInventoryFixture(t, monitorActionGroupType)
	peak := &peakTracker{}
	var tracking sync.Map
	inner := f.runtime.transport
	f.runtime.transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if _, ok := tracking.Load(strings.ToLower(req.URL.Path)); ok && req.Method == "GET" {
			peak.hold(5 * time.Millisecond)
		}
		return inner.RoundTrip(req)
	})
	base := slices.Sorted(maps.Keys(f.objects))[0]
	var ids []string
	for i := range 12 {
		raw := batchClone(f.objects[base])
		original := text(raw["id"])
		raw["id"], raw["name"] = strings.TrimSuffix(original, last(original))+fmt.Sprintf("sweep%02d", i), fmt.Sprintf("sweep%02d", i)
		id, _, _, err := monitorResourceID(text(raw["id"]))
		if err != nil {
			t.Fatal(err)
		}
		f.objects[id] = raw
		ids = append(ids, id)
	}
	var values []asset.Asset
	for _, id := range ids {
		values = append(values, f.asset(t, id))
	}
	contributor, err := f.runtime.ServiceLifecycle(t.Context(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		tracking.Store(strings.ToLower(id), true)
	}
	clear(f.calls)
	if _, err := contributor.Contribute(t.Context(), "scope", values); err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		if f.calls["GET "+strings.ToLower(id)] != 1 {
			t.Fatal("monitor parent not read exactly once", id, f.calls["GET "+strings.ToLower(id)])
		}
	}
	if peak.peak < 2 || peak.peak > detailReadConcurrency {
		t.Fatal("monitor parent reads were not concurrent within the bound", peak.peak)
	}
	f.override = func(req *http.Request) (*http.Response, bool) {
		switch strings.ToLower(req.URL.Path) {
		case strings.ToLower(ids[3]):
			return sweep7Denied("FirstDenied"), true
		case strings.ToLower(ids[9]):
			return sweep7Denied("LaterDenied"), true
		}
		return nil, false
	}
	for range 5 {
		if _, err := contributor.Contribute(t.Context(), "scope", values); err == nil || !strings.Contains(err.Error(), "FirstDenied") {
			t.Fatal("first failing monitor parent lost", err)
		}
	}
}

// A cascade preflight lists its children's own children concurrently up front
// and still reports the first failing child in list order.
func TestSweep7CascadePreflightListsChildrenConcurrentInOrder(t *testing.T) {
	s, r, assets := messagingScenario(t, serviceBusNamespaceType)
	namespace := assets[0]
	var lists []string
	for i := range 12 {
		id := strings.ToLower(namespace.Identity.NativeID) + fmt.Sprintf("/queues/sweep%02d", i)
		raw := map[string]any{"id": id, "name": last(id), "type": serviceBusQueueType, "properties": map[string]any{"provisioningState": "Succeeded", "createdAt": "2026-01-01T00:00:00Z"}}
		s.records[id], s.version[id] = raw, "2024-01-01"
		collection := strings.ToLower(namespace.Identity.NativeID) + "/queues"
		s.lists[collection] = append(s.lists[collection], raw)
		s.lists[id+"/authorizationrules"], s.version[id+"/authorizationrules"] = []any{}, "2024-01-01"
		assets = append(assets, dnsAsset(t, r, raw))
		lists = append(lists, id+"/authorizationrules")
	}
	request, _ := dnsRequest(t, r, assets, namespace)
	isList := func(path string) bool {
		return strings.Contains(path, "/queues/sweep") && strings.HasSuffix(path, "/authorizationrules")
	}
	failing := func(failures map[string]string) contracts.ActionDriver {
		s.handle = nil
		if failures != nil {
			sweep7Fail(s, failures)
		}
		driver, err := r.ResolveAction(t.Context(), "connection", request.Asset)
		if err != nil {
			t.Fatal(err)
		}
		return driver
	}
	calls, peak := countCalls(s, isList, 5*time.Millisecond)
	driver := failing(map[string]string{lists[3]: "FirstDenied", lists[9]: "LaterDenied"})
	for range 5 {
		if _, err := driver.Execute(t.Context(), request); err == nil || !strings.Contains(err.Error(), "FirstDenied") || len(s.deletes) != 0 {
			t.Fatal("first failing child list lost", err, s.deletes)
		}
	}
	driver = failing(nil)
	clear(calls)
	peak.peak = 0
	if _, err := driver.Execute(t.Context(), request); err != nil || len(s.deletes) != 1 {
		t.Fatal("namespace cascade preflight failed", err, s.deletes)
	}
	for _, list := range lists {
		if calls["GET "+list] != 1 {
			t.Fatal("child list not read exactly once", list, calls["GET "+list])
		}
	}
	if peak.peak < 2 || peak.peak > detailReadConcurrency {
		t.Fatal("child lists were not concurrent within the bound", peak.peak)
	}
}

// A workspace's final member reads (boundary) and its member absence reads
// (readback) run concurrently, once per member, and the first failing member
// in id order wins.
func TestSweep7SynapseWorkspaceMembersConcurrentInOrder(t *testing.T) {
	f := newWorkspaceActionFixture(t)
	template := f.items[synapseNotebookType]
	rows := []any{template}
	byName := map[string]map[string]any{"item": template}
	var paths []string
	for i := range 12 {
		raw := batchClone(template)
		name := fmt.Sprintf("sweep%02d", i)
		raw["id"], raw["name"] = text(f.workspace["id"])+"/notebooks/"+name, name
		rows = append(rows, raw)
		byName[name] = raw
		paths = append(paths, "/notebooks/"+name)
	}
	var mu sync.Mutex
	failures, gone := map[string]string{}, false
	f.intercept = func(q *http.Request) (*http.Response, bool) {
		if q.URL.Host != "first.dev.azuresynapse.net" {
			return nil, false
		}
		mu.Lock()
		code, missing := failures[q.URL.Path], gone
		mu.Unlock()
		if code != "" {
			return sweep7Denied(code), true
		}
		if q.URL.Path == "/notebooks" {
			return jsonResponse(200, map[string]any{"value": rows}, nil), true
		}
		if name, ok := strings.CutPrefix(q.URL.Path, "/notebooks/"); ok && byName[name] != nil {
			if missing {
				return jsonResponse(404, map[string]any{"error": map[string]any{"code": "NotFound"}}, nil), true
			}
			return jsonResponse(200, byName[name], nil), true
		}
		return nil, false
	}
	peak := &peakTracker{}
	var tracking sync.Map
	inner := f.runtime.transport
	f.runtime.transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if _, ok := tracking.Load(req.URL.Path); ok && req.URL.Host == "first.dev.azuresynapse.net" {
			peak.hold(5 * time.Millisecond)
		}
		return inner.RoundTrip(req)
	})
	var values []asset.Asset
	for _, kind := range []string{synapseType, synapseSparkType, synapseSQLType, synapseBatchType, synapseSessionType, synapseNotebookType, synapseJobDefinitionType, synapsePipelineType} {
		req := productRequest(f.runtime, kind)
		if synapseDataKind(kind).kind != "" {
			req.Source = synapseDataInventorySource
		}
		batch, err := f.runtime.List(t.Context(), req)
		if err != nil {
			t.Fatal("native fixture inventory", kind, err)
		}
		for _, item := range batch.Items {
			values = append(values, asset.Asset{ID: asset.AssetID(fmt.Sprint("asset-", len(values))), Identity: asset.Identity{Provider: asset.ProviderAzure, Partition: "azure", ConnectionID: "connection", NativeType: kind, NativeID: item.NativeID}, Normalized: item.Normalized, Location: item.Location})
		}
	}
	if len(values) != 8+len(paths) {
		t.Fatal("notebook members missing from inventory", len(values))
	}
	req := workspaceActionRequest(values)
	driver, err := f.runtime.ResolveAction(t.Context(), "connection", req.Asset)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		tracking.Store(path, true)
	}
	mu.Lock()
	failures[paths[3]], failures[paths[9]] = "FirstDenied", "LaterDenied"
	mu.Unlock()
	for range 3 {
		if _, err := driver.Execute(t.Context(), req); err == nil || !strings.Contains(err.Error(), "FirstDenied") || f.deletes != 0 {
			t.Fatal("first failing member lost", err, f.deletes)
		}
	}
	mu.Lock()
	clear(failures)
	mu.Unlock()
	if _, err := driver.Execute(t.Context(), req); err != nil || f.deletes != 1 {
		t.Fatal("workspace delete failed", err, f.deletes)
	}
	if peak.peak < 2 || peak.peak > detailReadConcurrency {
		t.Fatal("boundary member reads were not concurrent within the bound", peak.peak)
	}
	// Readback after the workspace is gone, without a terminal receipt.
	f.gone, f.poolsGone, f.metadataGone = true, true, true
	mu.Lock()
	gone = true
	mu.Unlock()
	clear(f.reads)
	peak.peak = 0
	readback, err := driver.Readback(t.Context(), req)
	if err != nil || readback.Exists {
		t.Fatal("absent members not reconciled", readback, err)
	}
	for _, path := range paths {
		if f.reads[path] != 1 {
			t.Fatal("member not read exactly once", path, f.reads[path])
		}
	}
	if peak.peak < 2 || peak.peak > detailReadConcurrency {
		t.Fatal("readback member reads were not concurrent within the bound", peak.peak)
	}
	mu.Lock()
	failures[paths[3]], failures[paths[9]] = "FirstDenied", "LaterDenied"
	mu.Unlock()
	for range 5 {
		if _, err := driver.Readback(t.Context(), req); err == nil || !strings.Contains(err.Error(), "FirstDenied") {
			t.Fatal("first failing member lost in readback", err)
		}
	}
}

// Restore point reviews run concurrently (each with its own parent reads
// before and after its own GET) and the first failing review in item order wins.
func TestSweep7SynapseRestorePointReviewsConcurrentInOrder(t *testing.T) {
	f := newSynapseBackupFixture(t)
	pool := strings.ToLower(resourceID(synapseType, "first")) + "/sqlpools/pool"
	var template map[string]any
	for id, raw := range f.backups {
		if strings.HasPrefix(id, pool+"/restorepoints/") {
			template = raw
		}
	}
	var ids []string
	for i := range 12 {
		raw := batchClone(template)
		id := pool + fmt.Sprintf("/restorepoints/sweep%02d", i)
		raw["id"], raw["name"] = id, last(id)
		f.backups[id] = raw
		ids = append(ids, id)
	}
	var mu sync.Mutex
	seen, failures := map[string]int{}, map[string]string{}
	f.intercepted = func(q *http.Request) (*http.Response, bool) {
		path := strings.ToLower(q.URL.Path)
		mu.Lock()
		defer mu.Unlock()
		seen[path]++
		// The two snapshot reads come first; the third read is the review's own.
		if code := failures[path]; code != "" && seen[path] == 3 {
			return sweep7Denied(code), true
		}
		return nil, false
	}
	peak := &peakTracker{}
	inner := f.runtime.transport
	f.runtime.transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if strings.ToLower(req.URL.Path) == pool {
			peak.hold(5 * time.Millisecond)
		}
		return inner.RoundTrip(req)
	})
	req := backupRequest(f.runtime, synapseRestorePointType)
	batch, err := f.runtime.List(t.Context(), req)
	if err != nil || !batch.Complete || len(batch.Items) != 2+len(ids) {
		t.Fatal("restore point inventory changed", len(batch.Items), err)
	}
	for _, item := range batch.Items {
		if item.Normalized[synapseRestoreReview] == nil || item.Actionable == nil {
			t.Fatal("restore point review missing", item.NativeID)
		}
	}
	for _, id := range ids {
		if seen[id] != 3 {
			t.Fatal("restore point not read twice by snapshots and once by its review", id, seen[id])
		}
	}
	if peak.peak < 2 || peak.peak > detailReadConcurrency {
		t.Fatal("restore point reviews were not concurrent within the bound", peak.peak)
	}
	for range 5 {
		mu.Lock()
		clear(seen)
		failures[ids[3]], failures[ids[9]] = "FirstDenied", "LaterDenied"
		mu.Unlock()
		if _, err := f.runtime.List(t.Context(), req); err == nil || !strings.Contains(err.Error(), "FirstDenied") {
			t.Fatal("first failing review lost", err)
		}
	}
}
