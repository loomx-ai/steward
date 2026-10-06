package azure

import (
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

// sweep1Fail makes GETs of the given paths fail with a distinct error code.
func sweep1Fail(s *dnsScenario, codes map[string]string) {
	base := s.handle
	s.handle = func(req *http.Request) (*http.Response, bool) {
		if code, ok := codes[strings.ToLower(req.URL.Path)]; ok && req.Method == "GET" {
			return jsonResponse(403, map[string]any{"error": map[string]any{"code": code}}, nil), true
		}
		if base != nil {
			return base(req)
		}
		return nil, false
	}
}

func sweep1Concurrent(t *testing.T, peak *peakTracker) {
	t.Helper()
	if peak.peak < 2 || peak.peak > detailReadConcurrency {
		t.Fatal("reads were not concurrent within the bound", peak.peak)
	}
}

func sweep1FirstError(t *testing.T, err error) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), "FirstDenied") {
		t.Fatal("first failure in order lost", err)
	}
}

// Each service-level issue's projection and canonical GETs run concurrently in
// both passes; the first failure in list order wins.
func TestSweep1APIMIssuesReadConcurrentlyInOrder(t *testing.T) {
	s, r, assets := apimScenario(t)
	issue := cdnAsset(t, assets, apimIssueType)
	root, api := apimRootID(issue.Identity.NativeID), redisParentID(issue.Identity.NativeID)
	ids := []string{issue.Identity.NativeID}
	for i := range 11 {
		raw := maps.Clone(s.records[issue.Identity.NativeID])
		raw["id"], raw["name"] = fmt.Sprintf("%s/issues/sweep%02d", api, i), fmt.Sprintf("sweep%02d", i)
		raw["_apim_header_etag"] = fmt.Sprintf(`"sweep-%d"`, i)
		s.add(raw, apimVersion)
		apimIssueProjectionScenario(s, raw)
		ids = append(ids, strings.ToLower(text(raw["id"])))
	}
	projection := func(id string) string { return root + "/issues/" + last(id) }
	calls, peak := countCalls(s, func(path string) bool { return strings.Contains(path, "/issues/") }, 5*time.Millisecond)
	c, _ := r.resolve(t.Context(), "connection")
	issues, err := c.apimIssues(t.Context(), root)
	if err != nil || len(issues) != len(ids) {
		t.Fatal("issues changed", len(issues), err)
	}
	for _, id := range ids {
		if issues[id].id != id || calls["GET "+projection(id)] != 2 || calls["GET "+id] != 2 {
			t.Fatal("issue not read once per pass", id, calls["GET "+projection(id)], calls["GET "+id])
		}
	}
	sweep1Concurrent(t, peak)
	sweep1Fail(s, map[string]string{projection(ids[3]): "FirstDenied", ids[9]: "LaterDenied"})
	for range 5 {
		_, err := c.apimIssues(t.Context(), root)
		sweep1FirstError(t, err)
	}
}

func sweep1LocalStorage(t *testing.T, count int) (*dnsScenario, *client, []string, []string) {
	t.Helper()
	s := newDNSScenario()
	root := "/subscriptions/" + testSubscription
	for _, kind := range []string{azureLocalDiskType, azureLocalImageType, azureLocalMarketplaceType} {
		path := strings.ToLower(root + "/providers/" + kind)
		s.lists[path], s.version[path] = []any{}, azureLocalVersion
	}
	var listed, known []string
	for i := range count {
		raw := nativeResource(azureLocalDiskType, fmt.Sprintf("disk%02d", i), "eastus", map[string]any{"provisioningState": "Succeeded"})
		s.add(raw, azureLocalVersion)
		id := strings.ToLower(text(raw["id"]))
		if i%2 == 0 {
			path := strings.ToLower(root + "/providers/" + azureLocalDiskType)
			s.lists[path] = append(s.lists[path], raw)
			listed = append(listed, id)
		} else {
			known = append(known, id) // Omitted by the index, found by its own GET.
		}
	}
	gone := strings.ToLower(resourceID(azureLocalDiskType, "gone"))
	s.gone[gone] = true
	known = append(known, gone)
	c, err := s.runtime(t).resolve(t.Context(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	return s, c, listed, known
}

// Listed storage resources and known IDs omitted by the index are read
// concurrently; failures are taken in row order, then in known order.
func TestSweep1AzureLocalStorageResourcesReadConcurrentlyInOrder(t *testing.T) {
	s, c, listed, known := sweep1LocalStorage(t, 24)
	calls, peak := countCalls(s, func(path string) bool { return strings.Contains(path, "/virtualharddisks/") }, 5*time.Millisecond)
	resources, err := c.azureLocalStorageResources(t.Context(), known)
	if err != nil || len(resources) != len(listed)+len(known)-1 {
		t.Fatal("storage resources changed", len(resources), err)
	}
	for _, id := range append(append([]string{}, listed...), known...) {
		if calls["GET "+id] != 1 {
			t.Fatal("resource not read exactly once", id, calls["GET "+id])
		}
	}
	sweep1Concurrent(t, peak)
	sweep1Fail(s, map[string]string{listed[3]: "FirstDenied", listed[9]: "LaterDenied", known[1]: "LaterDenied"})
	for range 5 {
		_, err := c.azureLocalStorageResources(t.Context(), known)
		sweep1FirstError(t, err)
	}
	s.handle = nil
	sweep1Fail(s, map[string]string{known[2]: "FirstDenied", known[7]: "LaterDenied"})
	for range 5 {
		_, err := c.azureLocalStorageResources(t.Context(), known)
		sweep1FirstError(t, err)
	}
	// An invalid row fails after the rows before it and stops later reads.
	s.handle = nil
	path := strings.ToLower("/subscriptions/" + testSubscription + "/providers/" + azureLocalDiskType)
	rows := s.lists[path]
	rows[5] = rows[4]
	clear(calls)
	if _, err := c.azureLocalStorageResources(t.Context(), known); err == nil || !strings.Contains(err.Error(), "invalid_azure_local_storage_resource_index") {
		t.Fatal("duplicate row accepted", err)
	}
	for i, id := range listed {
		if want := map[bool]int{true: 1, false: 0}[i < 5]; calls["GET "+id] != want {
			t.Fatal("wrong reads around the invalid row", id, calls["GET "+id])
		}
	}
}

// sweep1Watch counts requests and tracks concurrent matching requests outside
// a fixture's own lock. Call it before resolving the runtime's client.
func sweep1Watch(r *Runtime, match func(string) bool) (map[string]int, *peakTracker) {
	var mu sync.Mutex
	calls, peak := map[string]int{}, &peakTracker{}
	inner := r.transport
	r.transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		path := strings.ToLower(req.URL.Path)
		mu.Lock()
		calls[req.Method+" "+path]++
		mu.Unlock()
		if match(path) {
			peak.hold(5 * time.Millisecond)
		}
		return inner.RoundTrip(req)
	})
	return calls, peak
}

// Hybrid machines and their VM instances are read concurrently and checked in order.
func TestSweep1AzureLocalVMResourcesReadConcurrentlyInOrder(t *testing.T) {
	s := newDNSScenario()
	index := strings.ToLower("/subscriptions/" + testSubscription + "/providers/" + hybridMachineType)
	s.lists[index], s.version[index] = []any{}, hybridComputeVersion
	var machines, instances []string
	for i := range 12 {
		raw := nativeResource(hybridMachineType, fmt.Sprintf("machine%02d", i), "eastus", map[string]any{"status": "Connected"})
		s.add(raw, hybridComputeVersion)
		s.lists[index] = append(s.lists[index], raw)
		id := strings.ToLower(text(raw["id"]))
		instance := id + "/providers/microsoft.azurestackhci/virtualmachineinstances/default"
		if i%3 == 0 {
			s.gone[instance] = true
		} else {
			s.add(map[string]any{"id": instance, "name": "default", "type": azureLocalVMType, "properties": map[string]any{"provisioningState": "Succeeded"}}, azureLocalVersion)
		}
		machines, instances = append(machines, id), append(instances, instance)
	}
	calls, peak := countCalls(s, func(path string) bool { return strings.Contains(path, "/machines/") }, 5*time.Millisecond)
	c, _ := s.runtime(t).resolve(t.Context(), "connection")
	resources, err := c.azureLocalVMResources(t.Context(), nil)
	if err != nil || len(resources) != 8 {
		t.Fatal("VM resources changed", len(resources), err)
	}
	for i := range machines {
		if calls["GET "+machines[i]] != 1 || calls["GET "+instances[i]] != 1 {
			t.Fatal("not read exactly once", machines[i], calls["GET "+machines[i]], calls["GET "+instances[i]])
		}
	}
	sweep1Concurrent(t, peak)
	sweep1Fail(s, map[string]string{machines[3]: "FirstDenied", machines[9]: "LaterDenied"})
	for range 5 {
		_, err := c.azureLocalVMResources(t.Context(), nil)
		sweep1FirstError(t, err)
	}
	s.handle = nil
	sweep1Fail(s, map[string]string{instances[4]: "FirstDenied", instances[10]: "LaterDenied"})
	for range 5 {
		_, err := c.azureLocalVMResources(t.Context(), nil)
		sweep1FirstError(t, err)
	}
}

// Connected clusters and their provisioned instances are read concurrently and
// checked in order; an absent unlisted instance is skipped.
func TestSweep1AzureLocalAKSResourcesReadConcurrentlyInOrder(t *testing.T) {
	s := newDNSScenario()
	request := func(kind, id string, list bool) string {
		c := &client{subscription: testSubscription}
		bound, err := c.azureLocalAKSRequest(kind, id, list)
		if err != nil {
			t.Fatal(err)
		}
		u, _ := url.Parse(bound.URL)
		return strings.ToLower(u.Path)
	}
	index := request(fleetArcClusterType, "", true)
	s.lists[index] = []any{}
	var clusters, instances []string
	for i := range 12 {
		raw := nativeResource(fleetArcClusterType, fmt.Sprintf("cluster%02d", i), "eastus", map[string]any{"provisioningState": "Succeeded"})
		s.add(raw, azureLocalVersion)
		s.lists[index] = append(s.lists[index], raw)
		id := strings.ToLower(text(raw["id"]))
		instance := id + azureLocalAKSSuffix
		s.lists[request(azureLocalAKSType, instance, true)] = []any{}
		s.gone[instance] = true
		clusters, instances = append(clusters, id), append(instances, instance)
	}
	calls, peak := countCalls(s, func(path string) bool { return strings.Contains(path, "/connectedclusters/") }, 5*time.Millisecond)
	c, _ := s.runtime(t).resolve(t.Context(), "connection")
	resources, err := c.azureLocalAKSResources(t.Context(), nil)
	if err != nil || len(resources) != 0 {
		t.Fatal("AKS resources changed", len(resources), err)
	}
	for i := range clusters {
		if calls["GET "+clusters[i]] != 1 || calls["GET "+instances[i]] != 1 || calls["GET "+request(azureLocalAKSType, instances[i], true)] != 1 {
			t.Fatal("not read exactly once", clusters[i], calls["GET "+clusters[i]], calls["GET "+instances[i]])
		}
	}
	sweep1Concurrent(t, peak)
	sweep1Fail(s, map[string]string{clusters[3]: "FirstDenied", clusters[9]: "LaterDenied"})
	for range 5 {
		_, err := c.azureLocalAKSResources(t.Context(), nil)
		sweep1FirstError(t, err)
	}
	s.handle = nil
	sweep1Fail(s, map[string]string{instances[4]: "FirstDenied", request(azureLocalAKSType, instances[10], true): "LaterDenied"})
	for range 5 {
		_, err := c.azureLocalAKSResources(t.Context(), nil)
		sweep1FirstError(t, err)
	}
}

// Listed legacy children are read concurrently between the component's
// before/after reads and checked in list order.
func TestSweep1InsightsLegacyChildrenReadConcurrentlyInOrder(t *testing.T) {
	f := newInsightsInventoryFixture(t)
	var ids []string
	for i := range 12 {
		id, err := insightsLegacyURL(f.parentID, insightsExportType, fmt.Sprintf("sweep%02d", i))
		if err != nil {
			t.Fatal(err)
		}
		f.children[id] = map[string]any{"ExportId": fmt.Sprintf("sweep%02d", i), "Name": "display name"}
		u, _ := url.Parse(id)
		ids = append(ids, strings.ToLower(u.Path))
	}
	calls, peak := sweep1Watch(f.runtime, func(path string) bool { return strings.Contains(path, "/exportconfiguration/") })
	c, _ := f.runtime.resolve(t.Context(), "connection")
	children, err := c.insightsLegacyChildren(t.Context(), f.parentID, insightsExportType, insightsAnnotationWindow{})
	if err != nil || len(children) != len(ids)+2 {
		t.Fatal("legacy children changed", len(children), err)
	}
	for _, id := range ids {
		if calls["GET "+id] != 1 {
			t.Fatal("child not read exactly once", id, calls["GET "+id])
		}
	}
	if calls["GET "+strings.ToLower(f.parentID)] != 2 {
		t.Fatal("component before/after reads changed", calls["GET "+strings.ToLower(f.parentID)])
	}
	sweep1Concurrent(t, peak)
	f.override = func(req *http.Request) (*http.Response, bool) {
		switch strings.ToLower(req.URL.Path) {
		case ids[3]:
			return jsonResponse(403, map[string]any{"error": map[string]any{"code": "FirstDenied"}}, nil), true
		case ids[9]:
			return jsonResponse(403, map[string]any{"error": map[string]any{"code": "LaterDenied"}}, nil), true
		}
		return nil, false
	}
	for range 5 {
		_, err := c.insightsLegacyChildren(t.Context(), f.parentID, insightsExportType, insightsAnnotationWindow{})
		sweep1FirstError(t, err)
	}
}

// Saved annotations omitted by the window are read concurrently; absent ones
// are reported, the first failure in saved order wins.
func TestSweep1InsightsOlderAnnotationsReadConcurrentlyInOrder(t *testing.T) {
	f := newInsightsInventoryFixture(t)
	example := object(array(insightsScopedExample(t, "stable/2015-05-01/examples/AnnotationsList.json", f.parentID)["value"])[0])
	var known, paths []string
	for i := range 12 {
		raw := maps.Clone(example)
		raw["Id"] = fmt.Sprintf("sweep-%02d", i)
		id, err := insightsLegacyURL(f.parentID, insightsAnnotationType, text(raw["Id"]))
		if err != nil {
			t.Fatal(err)
		}
		if i != 5 {
			f.children[id] = raw // The sixth is gone.
		}
		u, _ := url.Parse(id)
		known, paths = append(known, id), append(paths, strings.ToLower(u.Path))
	}
	f.annotationVisible = map[string]bool{}
	calls, peak := sweep1Watch(f.runtime, func(path string) bool { return strings.Contains(path, "/annotations/") })
	c, _ := f.runtime.resolve(t.Context(), "connection")
	window := insightsLegacyTestWindow()
	children, _, absent, err := c.insightsAnnotationInventoryChildren(t.Context(), f.parentID, window, known)
	if err != nil || len(children) != 11 || len(absent) != 1 || absent[0] != known[5] {
		t.Fatal("older annotations changed", len(children), absent, err)
	}
	for i, child := range children {
		if child.id != known[i+map[bool]int{true: 1, false: 0}[i >= 5]] || calls["GET "+paths[i]] != 1 {
			t.Fatal("annotation order or reads changed", child.id, calls["GET "+paths[i]])
		}
	}
	sweep1Concurrent(t, peak)
	f.override = func(req *http.Request) (*http.Response, bool) {
		switch strings.ToLower(req.URL.Path) {
		case paths[3]:
			return jsonResponse(403, map[string]any{"error": map[string]any{"code": "FirstDenied"}}, nil), true
		case paths[9]:
			return jsonResponse(403, map[string]any{"error": map[string]any{"code": "LaterDenied"}}, nil), true
		}
		return nil, false
	}
	for range 5 {
		_, _, _, err := c.insightsAnnotationInventoryChildren(t.Context(), f.parentID, window, known)
		sweep1FirstError(t, err)
	}
}

// A workbook's revisions are read concurrently between its before/after reads.
func TestSweep1WorkbookRevisionsReadConcurrentlyInOrder(t *testing.T) {
	f := newWorkbookFixture(t, insightsWorkbookType)
	id := slices.Sorted(maps.Keys(f.objects))[0]
	template := f.revisions[id][slices.Sorted(maps.Keys(f.revisions[id]))[0]]
	var paths []string
	for i := range 12 {
		revision := fmt.Sprintf("sweep%02d", i)
		raw := maps.Clone(template)
		raw["properties"] = maps.Clone(object(template["properties"]))
		object(raw["properties"])["revision"] = revision
		f.revisions[id][revision] = raw
		paths = append(paths, strings.ToLower(id+"/revisions/"+revision))
	}
	calls, peak := sweep1Watch(f.runtime, func(path string) bool { return strings.Contains(path, "/revisions/") })
	c, _ := f.runtime.resolve(t.Context(), "connection")
	record, err := c.workbookRecord(t.Context(), insightsWorkbookType, id)
	if err != nil || len(record.revisions) != len(f.revisions[id]) {
		t.Fatal("revisions changed", len(record.revisions), err)
	}
	for _, path := range paths {
		if calls["GET "+path] != 1 {
			t.Fatal("revision not read exactly once", path, calls["GET "+path])
		}
	}
	if calls["GET "+strings.ToLower(id)] != 2 {
		t.Fatal("workbook before/after reads changed", calls["GET "+strings.ToLower(id)])
	}
	sweep1Concurrent(t, peak)
	f.override = func(req *http.Request) (*http.Response, bool) {
		switch strings.ToLower(req.URL.Path) {
		case paths[3]:
			return jsonResponse(403, map[string]any{"error": map[string]any{"code": "FirstDenied"}}, nil), true
		case paths[9]:
			return jsonResponse(403, map[string]any{"error": map[string]any{"code": "LaterDenied"}}, nil), true
		}
		return nil, false
	}
	for range 5 {
		_, err := c.workbookRecord(t.Context(), insightsWorkbookType, id)
		sweep1FirstError(t, err)
	}
}

// Every site is read and checked concurrently in both passes; the first
// failure in list order wins.
func TestSweep1AppCertificateSitesReadConcurrentlyInOrder(t *testing.T) {
	s, r, assets := appServiceScenario(t)
	target := cdnAsset(t, assets, appCertificateType)
	site := cdnAsset(t, assets, appSiteType)
	original := s.records[site.Identity.NativeID]
	collection := strings.ToLower("/subscriptions/" + testSubscription + "/providers/" + appSiteType)
	var sites []string
	for i := range 11 {
		raw := maps.Clone(original)
		raw["name"] = fmt.Sprintf("sweep%02d", i)
		raw["id"] = strings.TrimSuffix(site.Identity.NativeID, last(site.Identity.NativeID)) + text(raw["name"])
		id := strings.ToLower(text(raw["id"]))
		s.add(raw, appServiceVersion)
		s.lists[collection] = append(s.lists[collection], raw)
		for _, children := range []string{"/hostnamebindings", "/slots"} {
			s.lists[id+children], s.version[id+children] = []any{}, appServiceVersion
		}
		sites = append(sites, id)
	}
	calls, peak := countCalls(s, func(path string) bool { return strings.Contains(path, "/sites/sweep") }, 5*time.Millisecond)
	c, _ := r.resolve(t.Context(), "connection")
	if err := c.appCertificateUnused(t.Context(), target, s.records[target.Identity.NativeID]); err != nil {
		t.Fatal(err)
	}
	for _, id := range sites {
		if calls["GET "+id] != 4 { // The site and its children walk, in each pass.
			t.Fatal("site reads changed", id, calls["GET "+id])
		}
	}
	sweep1Concurrent(t, peak)
	sweep1Fail(s, map[string]string{sites[3]: "FirstDenied", sites[9]: "LaterDenied"})
	for range 5 {
		sweep1FirstError(t, c.appCertificateUnused(t.Context(), target, s.records[target.Identity.NativeID]))
	}
}

// Batch impact readback and prerequisite absence read every member
// concurrently; the first failure (or survivor) in ID order decides.
func TestSweep1BatchImpactsAndPrerequisitesReadConcurrentlyInOrder(t *testing.T) {
	s, r, assets := newBatchScenario(t)
	job := cdnAsset(t, assets, batchJobType)
	c, _ := r.resolve(t.Context(), "connection")
	account, err := c.batchAccount(t.Context(), s.account)
	if err != nil {
		t.Fatal(err)
	}
	var impacts []contracts.ActionImpact
	var paths []string
	for i := range 12 {
		path := fmt.Sprintf("/jobs/jobid/tasks/sweep%02d", i)
		raw := maps.Clone(s.records["/jobs/jobid/tasks/taskid"])
		raw["id"], raw["url"] = last(path), s.origin+path
		s.records[path] = raw
		item, err := r.batchDataItem(t.Context(), c, account, batchTaskType, raw, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		value := asset.Asset{ID: asset.AssetID(item.NativeID), Identity: asset.Identity{Provider: asset.ProviderAzure, ConnectionID: "connection", Partition: "azure", NativeID: item.NativeID, NativeType: batchTaskType}, Location: item.Location, Normalized: item.Normalized}
		impacts = append(impacts, contracts.ActionImpact{Asset: value, ControllerID: job.ID, Delete: true})
		paths = append(paths, path)
	}
	driver, err := r.ResolveAction(t.Context(), "connection", job)
	if err != nil {
		t.Fatal(err)
	}
	a := monitorTargetInner(driver).(*batchAction)
	calls, peak := countCalls(s.arm, func(path string) bool { return strings.Contains(path, "/tasks/sweep") }, 5*time.Millisecond)
	readback := contracts.ActionRequest{Action: "delete", Asset: job, LifecycleImpacts: impacts}
	prerequisites := contracts.ActionRequest{Action: "delete", Asset: job, PrerequisiteDeletions: impacts}
	if read, err := a.readImpacts(t.Context(), readback, account); err != nil || !read.Exists {
		t.Fatal("surviving impacts hidden", read, err)
	}
	for _, path := range paths {
		if calls["GET "+path] != 1 {
			t.Fatal("impact not read exactly once", path, calls["GET "+path])
		}
	}
	sweep1Concurrent(t, peak)
	if err := a.prerequisitesAbsent(t.Context(), prerequisites, account); err == nil || !strings.Contains(err.Error(), "batch_prerequisite_still_exists") {
		t.Fatal("surviving prerequisite accepted", err)
	}
	for _, path := range paths {
		s.gone[path] = true
	}
	clear(calls)
	*peak = peakTracker{}
	if err := a.prerequisitesAbsent(t.Context(), prerequisites, account); err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		if calls["GET "+path] != 1 {
			t.Fatal("prerequisite not read exactly once", path, calls["GET "+path])
		}
	}
	sweep1Concurrent(t, peak)
	s.handle = func(req *http.Request) (*http.Response, bool) {
		switch req.URL.Path {
		case paths[3]:
			return jsonResponse(403, map[string]any{"code": "FirstDenied"}, nil), true
		case paths[9]:
			return jsonResponse(403, map[string]any{"code": "LaterDenied"}, nil), true
		}
		return nil, false
	}
	for range 5 {
		_, err := a.readImpacts(t.Context(), readback, account)
		sweep1FirstError(t, err)
		sweep1FirstError(t, a.prerequisitesAbsent(t.Context(), prerequisites, account))
	}
}

// Clusters are read and their node groups walked concurrently; contributions
// keep asset order and the first failure in asset order wins.
func TestSweep1AKSContributeReadsClustersConcurrentlyInOrder(t *testing.T) {
	s := newDNSScenario()
	kind, _ := findType(aksType)
	var raws []map[string]any
	var clusters, groups []string
	for i := range 12 {
		raw := nativeResource(aksType, fmt.Sprintf("cluster%02d", i), "eastus", map[string]any{"nodeResourceGroup": fmt.Sprintf("nodes%02d", i), "provisioningState": "Succeeded"})
		s.add(raw, kind.Version)
		group := strings.ToLower("/subscriptions/" + testSubscription + "/resourceGroups/" + fmt.Sprintf("nodes%02d", i))
		s.add(map[string]any{"id": group, "name": fmt.Sprintf("nodes%02d", i), "type": groupType, "location": "eastus", "managedBy": raw["id"]}, resourcesVersion)
		s.lists[group+"/resources"], s.version[group+"/resources"] = []any{}, resourcesVersion
		raws, clusters, groups = append(raws, raw), append(clusters, strings.ToLower(text(raw["id"]))), append(groups, group)
	}
	r := s.runtime(t)
	var assets []asset.Asset
	for _, raw := range raws {
		assets = append(assets, dnsAsset(t, r, raw))
	}
	calls, peak := countCalls(s, func(path string) bool { return strings.Contains(path, "/managedclusters/") }, 5*time.Millisecond)
	c, _ := r.resolve(t.Context(), "connection")
	lifecycle := &aksLifecycle{client: c}
	result, err := lifecycle.Contribute(t.Context(), "scope", assets)
	if err != nil || len(result.Unresolved) != len(clusters) {
		t.Fatal("contribution changed", len(result.Unresolved), err)
	}
	for i := range clusters {
		if result.Unresolved[i].ControllerID != assets[i].ID || calls["GET "+clusters[i]] != 1 || calls["GET "+groups[i]] != 2 {
			t.Fatal("cluster order or reads changed", i, calls["GET "+clusters[i]], calls["GET "+groups[i]])
		}
	}
	sweep1Concurrent(t, peak)
	sweep1Fail(s, map[string]string{clusters[3]: "FirstDenied", groups[9]: "LaterDenied"})
	for range 5 {
		_, err := lifecycle.Contribute(t.Context(), "scope", assets)
		sweep1FirstError(t, err)
	}
}

// Saved annotations whose component left the index are probed concurrently:
// the component and then the annotation must both be absent.
func TestSweep1InsightsAnnotationParentsReadConcurrentlyInOrder(t *testing.T) {
	f := newInsightsInventoryFixture(t)
	var known, components, paths []string
	for i := range 12 {
		component := strings.ToLower(resourceID(applicationInsightsType, fmt.Sprintf("gone%02d", i)))
		id, err := insightsLegacyURL(component, insightsAnnotationType, fmt.Sprintf("sweep-%02d", i))
		if err != nil {
			t.Fatal(err)
		}
		u, _ := url.Parse(id)
		known, components, paths = append(known, id), append(components, component), append(paths, strings.ToLower(u.Path))
	}
	fail := map[string]string{}
	f.override = func(req *http.Request) (*http.Response, bool) {
		path := strings.ToLower(req.URL.Path)
		if code := fail[path]; code != "" {
			return jsonResponse(403, map[string]any{"error": map[string]any{"code": code}}, nil), true
		}
		if slices.Contains(components, path) {
			return jsonResponse(404, map[string]any{"error": map[string]any{"code": "ResourceNotFound"}}, nil), true
		}
		return nil, false
	}
	calls, peak := sweep1Watch(f.runtime, func(path string) bool { return strings.Contains(path, "/components/gone") })
	c, _ := f.runtime.resolve(t.Context(), "connection")
	absent, err := c.insightsAnnotationParents(t.Context(), nil, known)
	if err != nil || !slices.Equal(absent, known) {
		t.Fatal("absent annotations changed", absent, err)
	}
	for i := range known {
		if calls["GET "+components[i]] != 1 || calls["GET "+paths[i]] != 1 {
			t.Fatal("not read exactly once", known[i], calls["GET "+components[i]], calls["GET "+paths[i]])
		}
	}
	sweep1Concurrent(t, peak)
	fail[components[3]], fail[paths[9]] = "FirstDenied", "LaterDenied"
	for range 5 {
		_, err := c.insightsAnnotationParents(t.Context(), nil, known)
		sweep1FirstError(t, err)
	}
}

// Saved annotations omitted by the component's window are read concurrently
// during contribution; found ones bind in ID order, absent ones are skipped.
func TestSweep1InsightsContributeOlderAnnotationsConcurrentlyInOrder(t *testing.T) {
	f := newInsightsInventoryFixture(t)
	f.annotationVisible = map[string]bool{}
	example := object(array(insightsScopedExample(t, "stable/2015-05-01/examples/AnnotationsList.json", f.parentID)["value"])[0])
	calls, peak := sweep1Watch(f.runtime, func(path string) bool { return strings.Contains(path, "/annotations/") })
	c, _ := f.runtime.resolve(t.Context(), "connection")
	parent := insightsLifecycleAssets(t, f)[0]
	assets := []asset.Asset{parent}
	mapping, _ := findType(insightsAnnotationType)
	var paths []string
	for i := range 12 {
		raw := maps.Clone(example)
		raw["Id"] = fmt.Sprintf("sweep-%02d", i)
		id, err := insightsLegacyURL(f.parentID, insightsAnnotationType, text(raw["Id"]))
		if err != nil {
			t.Fatal(err)
		}
		f.children[id] = raw
		current, err := c.insightsChildRead(t.Context(), mapping, id)
		if err != nil {
			t.Fatal(err)
		}
		if i == 5 {
			delete(f.children, id)
		}
		normalized := map[string]any{"_insights_component": parent.Identity.NativeID, "_insights_component_configuration": parent.Normalized["_monitor_private_link_target_configuration"], insightsChildProofKey(insightsAnnotationType): c.insightsChildConfiguration(id, insightsAnnotationType, current.data)}
		assets = append(assets, asset.Asset{ID: asset.AssetID(id), Identity: asset.Identity{Provider: asset.ProviderAzure, ConnectionID: "connection", Partition: "azure", NativeType: insightsAnnotationType, NativeID: id}, Location: parent.Location, Normalized: normalized})
		u, _ := url.Parse(id)
		paths = append(paths, strings.ToLower(u.Path))
	}
	clear(calls)
	*peak = peakTracker{}
	result, err := c.contributeInsightsChildren(t.Context(), parent, assets)
	if err != nil {
		t.Fatal(err)
	}
	var bound []asset.AssetID
	for _, binding := range result.Bindings {
		bound = append(bound, binding.ManagedAssetID)
	}
	var want []asset.AssetID
	for i, value := range assets[1:] {
		if i != 5 {
			want = append(want, value.ID)
		}
	}
	if !slices.Equal(bound, want) {
		t.Fatal("bound annotations changed", bound)
	}
	for _, path := range paths {
		if calls["GET "+path] != 1 {
			t.Fatal("annotation not read exactly once", path, calls["GET "+path])
		}
	}
	sweep1Concurrent(t, peak)
	f.override = func(req *http.Request) (*http.Response, bool) {
		switch strings.ToLower(req.URL.Path) {
		case paths[3]:
			return jsonResponse(403, map[string]any{"error": map[string]any{"code": "FirstDenied"}}, nil), true
		case paths[9]:
			return jsonResponse(403, map[string]any{"error": map[string]any{"code": "LaterDenied"}}, nil), true
		}
		return nil, false
	}
	for range 5 {
		_, err := c.contributeInsightsChildren(t.Context(), parent, assets)
		sweep1FirstError(t, err)
	}
}

// Listed network resources and known IDs omitted by the index are read
// concurrently; failures are taken in row order, then in known order.
func TestSweep1AzureLocalNetworkResourcesReadConcurrentlyInOrder(t *testing.T) {
	s := newDNSScenario()
	root := "/subscriptions/" + testSubscription
	for _, kind := range []string{azureLocalNICType, azureLocalNetworkType} {
		mapping, _ := findType(kind)
		path := strings.ToLower(root + "/providers/" + kind)
		s.lists[path], s.version[path] = []any{}, mapping.Version
	}
	c0 := &client{subscription: testSubscription}
	clusters, _ := c0.azureLocalAKSRequest(fleetArcClusterType, "", true)
	u, _ := url.Parse(clusters.URL)
	s.lists[strings.ToLower(u.Path)] = []any{}
	nics := strings.ToLower(root + "/providers/" + azureLocalNICType)
	mapping, _ := findType(azureLocalNICType)
	var listed, known []string
	for i := range 24 {
		raw := nativeResource(azureLocalNICType, fmt.Sprintf("nic%02d", i), "eastus", map[string]any{"provisioningState": "Succeeded"})
		s.add(raw, mapping.Version)
		id := strings.ToLower(text(raw["id"]))
		if i%2 == 0 {
			s.lists[nics] = append(s.lists[nics], raw)
			listed = append(listed, id)
		} else {
			known = append(known, id)
		}
	}
	gone := strings.ToLower(resourceID(azureLocalNICType, "gone"))
	s.gone[gone] = true
	known = append(known, gone)
	calls, peak := countCalls(s, func(path string) bool { return strings.Contains(path, "/networkinterfaces/") }, 5*time.Millisecond)
	c, _ := s.runtime(t).resolve(t.Context(), "connection")
	resources, err := c.azureLocalNetworkResources(t.Context(), known)
	if err != nil || len(resources) != len(listed)+len(known)-1 {
		t.Fatal("network resources changed", len(resources), err)
	}
	for _, id := range append(append([]string{}, listed...), known...) {
		if calls["GET "+id] != 1 {
			t.Fatal("resource not read exactly once", id, calls["GET "+id])
		}
	}
	sweep1Concurrent(t, peak)
	sweep1Fail(s, map[string]string{listed[3]: "FirstDenied", listed[9]: "LaterDenied", known[1]: "LaterDenied"})
	for range 5 {
		_, err := c.azureLocalNetworkResources(t.Context(), known)
		sweep1FirstError(t, err)
	}
	s.handle = nil
	sweep1Fail(s, map[string]string{known[2]: "FirstDenied", known[7]: "LaterDenied"})
	for range 5 {
		_, err := c.azureLocalNetworkResources(t.Context(), known)
		sweep1FirstError(t, err)
	}
}

// Component child prerequisites are checked in order, then probed for
// absence concurrently; the first survivor or failure in order decides.
func TestSweep1InsightsPrerequisitesAbsentReadConcurrentlyInOrder(t *testing.T) {
	f := newInsightsInventoryFixture(t)
	calls, peak := sweep1Watch(f.runtime, func(path string) bool { return strings.Contains(path, "/annotations/") })
	c, _ := f.runtime.resolve(t.Context(), "connection")
	a := &insightsComponentAction{action: action{client: c, id: f.parentID, location: "eastus", connectionID: "connection", partition: "azure"}, assetID: "component", configuration: "component-configuration"}
	request := contracts.ActionRequest{Action: "delete"}
	var paths []string
	for i := range 12 {
		id, err := insightsLegacyURL(f.parentID, insightsAnnotationType, fmt.Sprintf("sweep-%02d", i))
		if err != nil {
			t.Fatal(err)
		}
		value := asset.Asset{ID: asset.AssetID(id), Identity: asset.Identity{Provider: asset.ProviderAzure, ConnectionID: "connection", Partition: "azure", NativeType: insightsAnnotationType, NativeID: id}, Location: "eastus", Normalized: map[string]any{"_insights_component": f.parentID, "_insights_component_configuration": "component-configuration", insightsChildProofKey(insightsAnnotationType): "proof"}}
		request.PrerequisiteDeletions = append(request.PrerequisiteDeletions, contracts.ActionImpact{Asset: value, ControllerID: "component", Delete: true})
		u, _ := url.Parse(id)
		paths = append(paths, strings.ToLower(u.Path))
	}
	if err := a.prerequisitesAbsent(t.Context(), request, map[string]any{}); err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		if calls["GET "+path] != 1 {
			t.Fatal("prerequisite not read exactly once", path, calls["GET "+path])
		}
	}
	sweep1Concurrent(t, peak)
	f.override = func(req *http.Request) (*http.Response, bool) {
		switch strings.ToLower(req.URL.Path) {
		case paths[3]:
			return jsonResponse(403, map[string]any{"error": map[string]any{"code": "FirstDenied"}}, nil), true
		case paths[9]:
			return jsonResponse(403, map[string]any{"error": map[string]any{"code": "LaterDenied"}}, nil), true
		}
		return nil, false
	}
	for range 5 {
		sweep1FirstError(t, a.prerequisitesAbsent(t.Context(), request, map[string]any{}))
	}
	// An invalid prerequisite fails after the earlier probes; later ones are not read.
	f.override = nil
	request.PrerequisiteDeletions[6].Delete = false
	clear(calls)
	if err := a.prerequisitesAbsent(t.Context(), request, map[string]any{}); err == nil || !strings.Contains(err.Error(), "invalid_insights_component_prerequisite") {
		t.Fatal("invalid prerequisite accepted", err)
	}
	for i, path := range paths {
		if want := map[bool]int{true: 1, false: 0}[i < 6]; calls["GET "+path] != want {
			t.Fatal("wrong reads around the invalid prerequisite", path, calls["GET "+path])
		}
	}
}
