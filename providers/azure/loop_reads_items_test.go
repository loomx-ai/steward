package azure

import (
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

// insightsComponentsScenario serves count components, each with its fixed
// configuration reads and detections detection rules, concurrently.
type insightsComponentsScenario struct {
	mu       sync.Mutex
	calls    map[string]int
	peak     *peakTracker
	parents  map[string]map[string]any
	ids      []string
	configs  map[string]map[string]map[string]any
	rules    map[string]map[string]any
	fail     map[string]string
	holdPath func(string) bool
}

func newInsightsComponentsScenario(t *testing.T, count, detections int) (*insightsComponentsScenario, *Runtime) {
	t.Helper()
	s := &insightsComponentsScenario{calls: map[string]int{}, peak: &peakTracker{}, parents: map[string]map[string]any{}, configs: map[string]map[string]map[string]any{}, rules: map[string]map[string]any{}, fail: map[string]string{}}
	root := "/subscriptions/" + testSubscription
	group := map[string]any{"id": root + "/resourceGroups/test", "name": "test", "type": groupType, "location": "eastus"}
	for i := range count {
		parent := nativeResource(applicationInsightsType, fmt.Sprintf("app%02d", i), "eastus", map[string]any{"AppId": fmt.Sprint("app-", i), "CreationDate": "2026-09-01T00:00:00Z"})
		id, _, _ := parseID(text(parent["id"]))
		s.parents[id], s.ids = parent, append(s.ids, id)
		s.configs[id] = map[string]map[string]any{}
		for path, file := range map[string]string{
			"currentbillingfeatures":      "stable/2015-05-01/examples/CurrentBillingFeaturesGet.json",
			"pricingplans/current":        "stable/2017-10-01/examples/CurrentPricingPlanGet.json",
			"featurecapabilities":         "stable/2015-05-01/examples/FeatureCapabilitiesGet.json",
			"getavailablebillingfeatures": "stable/2015-05-01/examples/AvailableBillingFeaturesGet.json",
			"quotastatus":                 "stable/2015-05-01/examples/QuotaStatusGet.json",
		} {
			s.configs[id][path] = insightsScopedExample(t, file, id)
		}
		s.configs[id]["quotastatus"]["AppId"] = fmt.Sprint("app-", i)
	}
	detection := insightsScopedExample(t, "stable/2015-05-01/examples/ProactiveDetectionConfigurationGet.json", s.ids[0])
	for i := range detections {
		rule := maps.Clone(detection)
		rule["name"] = fmt.Sprintf("rule%02d", i)
		s.rules[text(rule["name"])] = rule
	}
	r := concurrentProtocolRuntime(t, func(req *http.Request) (*http.Response, error) {
		path := strings.ToLower(req.URL.Path)
		s.mu.Lock()
		s.calls[req.Method+" "+path]++
		code := s.fail[path]
		s.mu.Unlock()
		if s.holdPath != nil && s.holdPath(path) {
			s.peak.hold(2 * time.Millisecond)
		}
		if code != "" {
			return jsonResponse(403, map[string]any{"error": map[string]any{"code": code}}, nil), nil
		}
		if req.Method != "GET" {
			t.Error("unexpected write", req.Method, path)
		}
		if response, handled := emptyMonitorIndexResponse(t, req); handled {
			return response, nil
		}
		if response, handled := emptyDiagnosticSourceIndexResponse(t, req); handled {
			return response, nil
		}
		switch path {
		case root + "/providers/microsoft.insights/components":
			rows := []any{}
			for _, id := range s.ids {
				rows = append(rows, s.parents[id])
			}
			return jsonResponse(200, map[string]any{"value": rows}, nil), nil
		case root + "/resourcegroups":
			return jsonResponse(200, map[string]any{"value": []any{group}}, nil), nil
		case strings.ToLower(text(group["id"])):
			return jsonResponse(200, group, nil), nil
		case root + "/providers/microsoft.authorization/locks":
			return jsonResponse(200, map[string]any{"value": []any{}}, nil), nil
		case root + "/providers/microsoft.insights/privatelinkscopes":
			return jsonResponse(200, map[string]any{"value": []any{}}, nil), nil
		}
		for _, id := range s.ids {
			if path == id {
				return jsonResponse(200, s.parents[id], nil), nil
			}
			rest, ok := strings.CutPrefix(path, id+"/")
			if !ok {
				continue
			}
			if value := s.configs[id][rest]; value != nil {
				return jsonResponse(200, value, nil), nil
			}
			if rest == "proactivedetectionconfigs" {
				rows := []any{}
				for _, name := range slices.Sorted(maps.Keys(s.rules)) {
					rows = append(rows, s.rules[name])
				}
				return jsonResponse(200, rows, nil), nil
			}
			if name, ok := strings.CutPrefix(rest, "proactivedetectionconfigs/"); ok && s.rules[name] != nil {
				return jsonResponse(200, s.rules[name], nil), nil
			}
		}
		t.Error("unexpected endpoint", req.Method, req.URL)
		return jsonResponse(404, map[string]any{}, nil), nil
	})
	return s, r
}

// Components, their fixed configuration reads and their detection reads run
// concurrently; both inventory snapshots still read every component live.
func TestInsightsInventoryReadsComponentsConcurrently(t *testing.T) {
	s, r := newInsightsComponentsScenario(t, 12, 6)
	s.holdPath = func(path string) bool { return strings.Contains(path, "/components/") }
	page, err := r.List(t.Context(), productRequest(r, applicationInsightsType))
	if err != nil || !page.Complete || len(page.Items) != 12 {
		t.Fatal("component inventory changed", len(page.Items), err)
	}
	for i, item := range page.Items {
		if item.NativeID != s.ids[i] || len(object(item.Normalized["proactive_detection"])) != 6 {
			t.Fatal("component inventory out of order or incomplete", i, item.NativeID)
		}
	}
	for _, id := range s.ids {
		// One snapshot: list GET, workspace re-read, configuration re-read.
		if s.calls["GET "+id] != 6 || s.calls["GET "+id+"/quotastatus"] != 2 || s.calls["GET "+id+"/proactivedetectionconfigs/rule03"] != 2 {
			t.Fatal("component reads changed", id, s.calls["GET "+id], s.calls["GET "+id+"/quotastatus"], s.calls["GET "+id+"/proactivedetectionconfigs/rule03"])
		}
	}
	if s.peak.peak < 2 || s.peak.peak > clientRoundTrips {
		t.Fatal("component reads were not concurrent within the client cap", s.peak.peak)
	}
	// The first failure in component order wins, as in a serial walk.
	s.fail[s.ids[2]+"/proactivedetectionconfigs/rule04"] = "FirstDenied"
	s.fail[s.ids[9]+"/currentbillingfeatures"] = "LaterDenied"
	for range 3 {
		if _, err := r.List(t.Context(), productRequest(r, applicationInsightsType)); err == nil || !strings.Contains(err.Error(), "FirstDenied") {
			t.Fatal("first failure in component order lost", err)
		}
	}
}

// Within one component, a detection read failure precedes a later invalid
// row, and rows after an invalid row are not read.
func TestInsightsConfigurationsFirstErrorInOrder(t *testing.T) {
	s, r := newInsightsComponentsScenario(t, 1, 10)
	c, err := r.resolve(t.Context(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	id := s.ids[0]
	s.holdPath = func(path string) bool { return strings.Contains(path, "/proactivedetectionconfigs/") }
	if _, _, err := c.insightsConfigurations(t.Context(), id, s.parents[id], true); err != nil {
		t.Fatal(err)
	}
	if s.peak.peak < 2 || s.peak.peak > detailReadConcurrency {
		t.Fatal("detection reads were not concurrent within the bound", s.peak.peak)
	}
	s.rules["rule06"]["enabled"] = "yes" // Invalid listed row.
	s.fail[id+"/proactivedetectionconfigs/rule02"] = "FirstDenied"
	for range 3 {
		if _, _, err := c.insightsConfigurations(t.Context(), id, s.parents[id], true); err == nil || !strings.Contains(err.Error(), "FirstDenied") {
			t.Fatal("later invalid row hid the earlier read failure", err)
		}
	}
	delete(s.fail, id+"/proactivedetectionconfigs/rule02")
	clear(s.calls)
	if _, _, err := c.insightsConfigurations(t.Context(), id, s.parents[id], true); err == nil || !strings.Contains(err.Error(), "invalid_insights_detection_list_identity") {
		t.Fatal("invalid detection row accepted", err)
	}
	for i := range 10 {
		if want := map[bool]int{true: 1, false: 0}[i < 6]; s.calls[fmt.Sprintf("GET %s/proactivedetectionconfigs/rule%02d", id, i)] != want {
			t.Fatal("wrong reads around the invalid row", i)
		}
	}
	// A fixed read failure wins over the detection list, as before.
	s.rules["rule06"]["enabled"] = true
	s.fail[id+"/featurecapabilities"] = "FirstDenied"
	s.fail[id+"/proactivedetectionconfigs"] = "LaterDenied"
	if _, _, err := c.insightsConfigurations(t.Context(), id, s.parents[id], true); err == nil || !strings.Contains(err.Error(), "FirstDenied") {
		t.Fatal("detection list failure hid the fixed read failure", err)
	}
}

// One memo scope scans the AMPLS set (both passes) once for every target;
// each target's own before/after GETs stay live, and a call without a memo
// (a delete check) rescans.
func TestMonitorPrivateLinkIncomingScanMemoized(t *testing.T) {
	s, r, values := monitorPrivateLinkTargetScenario(t, "Microsoft.OperationalInsights/workspaces")
	c, _ := r.resolve(t.Context(), "connection")
	targets := []asset.Asset{values[0]}
	for i := range 11 {
		raw := nativeResource("Microsoft.OperationalInsights/workspaces", fmt.Sprintf("workspace%02d", i), "eastus", map[string]any{"provisioningState": "Succeeded", "customerId": "00000000-1111-2222-3333-444444444444"})
		metadata, _ := findType("Microsoft.OperationalInsights/workspaces")
		s.add(raw, metadata.Version)
		targets = append(targets, dnsAsset(t, r, raw))
	}
	isTarget := func(path string) bool { return strings.Contains(path, "/workspaces/") }
	calls, peak := countCalls(s, isTarget, 2*time.Millisecond)
	index := "GET /subscriptions/" + testSubscription + "/providers/microsoft.insights/privatelinkscopes"
	scope := "GET " + strings.ToLower(redisParentID(values[1].Identity.NativeID))
	contribution := governance.Contribution{}
	assets := append(slices.Clone(targets), values[1])
	if err := (&serviceCascades{client: c}).contributeMonitorPrivateLinkReferences(withReadMemo(t.Context()), assets, &contribution); err != nil || len(contribution.Relationships) != 1 || len(contribution.Unresolved) != 0 {
		t.Fatal("incoming references changed", contribution, err)
	}
	if calls[index] != 2 || calls[scope] != 4 { // Two passes, each reading the scope and its children.
		t.Fatal("memo scope repeated the AMPLS scan", calls[index], calls[scope])
	}
	for _, target := range targets {
		if calls["GET "+strings.ToLower(target.Identity.NativeID)] != 2 {
			t.Fatal("target before/after reads changed", target.Identity.NativeID, calls["GET "+strings.ToLower(target.Identity.NativeID)])
		}
	}
	if peak.peak < 2 || peak.peak > detailReadConcurrency {
		t.Fatal("target reads were not concurrent within the bound", peak.peak)
	}
	clear(calls)
	for range 2 {
		if _, err := c.monitorPrivateLinkIncoming(t.Context(), targets[1].Identity); err != nil {
			t.Fatal(err)
		}
	}
	if calls[index] != 4 {
		t.Fatal("delete check reused the AMPLS scan", calls[index])
	}
	s.handle = func(req *http.Request) (*http.Response, bool) {
		switch strings.ToLower(req.URL.Path) {
		case strings.ToLower(targets[2].Identity.NativeID):
			return jsonResponse(403, map[string]any{"error": map[string]any{"code": "FirstDenied"}}, nil), true
		case strings.ToLower(targets[9].Identity.NativeID):
			return jsonResponse(403, map[string]any{"error": map[string]any{"code": "LaterDenied"}}, nil), true
		}
		return nil, false
	}
	for range 3 {
		err := (&serviceCascades{client: c}).contributeMonitorPrivateLinkReferences(withReadMemo(t.Context()), assets, &governance.Contribution{})
		if err == nil || !strings.Contains(err.Error(), "FirstDenied") {
			t.Fatal("first failure in target order lost", err)
		}
	}
}

func apimAPIBasesScenario(t *testing.T, bases, revisions int) (*dnsScenario, *client, string, []string) {
	t.Helper()
	s := newDNSScenario()
	service, _, _ := parseID(resourceID(apimServiceType, "apim"))
	s.add(map[string]any{"id": service, "name": "apim", "type": apimServiceType, "location": "eastus", "properties": map[string]any{}}, apimVersion)
	collection := strings.ToLower(service + "/apis")
	s.version[collection] = apimVersion
	var ids []string
	for b := range bases {
		base := fmt.Sprintf("%s/apis/api%02d", service, b)
		revisionList := strings.ToLower(base + "/revisions")
		s.version[revisionList] = apimVersion
		for n := 1; n <= revisions; n++ {
			id := base
			if n > 1 {
				id = fmt.Sprintf("%s;rev=%d", base, n)
			}
			raw := map[string]any{"id": id, "name": last(id), "type": apimServiceType + "/apis", "properties": map[string]any{"apiRevision": fmt.Sprint(n), "isCurrent": n == 1, "path": fmt.Sprintf("api%02d", b), "displayName": "api"}}
			s.add(raw, apimVersion)
			s.lists[collection] = append(s.lists[collection], raw)
			s.lists[revisionList] = append(s.lists[revisionList], map[string]any{"apiId": fmt.Sprintf("/apis/api%02d;rev=%d", b, n), "apiRevision": fmt.Sprint(n), "isCurrent": n == 1, "isOnline": true})
			ids = append(ids, strings.ToLower(id))
		}
	}
	c, err := s.runtime(t).resolve(t.Context(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	return s, c, service, ids
}

// API bases and their revisions are read concurrently and merged in order;
// each base still re-lists its revisions after its reads.
func TestAPIMAPIBasesReadConcurrently(t *testing.T) {
	s, c, service, ids := apimAPIBasesScenario(t, 12, 2)
	isAPI := func(path string) bool {
		return strings.Contains(path, "/apis/") && !strings.HasSuffix(path, "/revisions")
	}
	calls, peak := countCalls(s, isAPI, 2*time.Millisecond)
	children, err := c.apimAPIs(t.Context(), service)
	if err != nil || len(children) != len(ids) {
		t.Fatal("API children changed", len(children), err)
	}
	for i := range 12 {
		base := strings.ToLower(fmt.Sprintf("%s/apis/api%02d", service, i))
		if calls["GET "+base+"/revisions"] != 2 || calls["GET "+base] != 1 || calls["GET "+base+";rev=2"] != 1 {
			t.Fatal("API reads changed", base, calls["GET "+base+"/revisions"], calls["GET "+base])
		}
	}
	if peak.peak < 2 || peak.peak > clientRoundTrips {
		t.Fatal("API reads were not concurrent within the client cap", peak.peak)
	}
	s.handle = func(req *http.Request) (*http.Response, bool) {
		switch strings.ToLower(req.URL.Path) {
		case ids[5]:
			return jsonResponse(403, map[string]any{"error": map[string]any{"code": "FirstDenied"}}, nil), true
		case ids[18]:
			return jsonResponse(403, map[string]any{"error": map[string]any{"code": "LaterDenied"}}, nil), true
		}
		return nil, false
	}
	for range 3 {
		if _, err := c.apimAPIs(t.Context(), service); err == nil || !strings.Contains(err.Error(), "FirstDenied") {
			t.Fatal("first failure in base order lost", err)
		}
	}
}

// Template probe lists run concurrently; only groups holding templates are
// read before and after their second listing.
func TestWorkbookTemplateProbesConcurrent(t *testing.T) {
	s := newDNSScenario()
	root := "/subscriptions/" + testSubscription
	version := insightsWorkbookVersion(insightsWorkbookTemplateType)
	template := object(array(object(workbookExample(t, "2020-11-20/examples/WorkbookTemplatesList.json"))["value"])[0])
	groups := []any{}
	var lists []string
	for i := range 20 {
		name := fmt.Sprintf("group%02d", i)
		group := map[string]any{"id": root + "/resourcegroups/" + name, "name": name, "type": groupType, "location": "westus", "tags": map[string]any{}, "properties": map[string]any{"provisioningState": "Succeeded"}}
		s.add(group, resourcesVersion)
		groups = append(groups, group)
		list := root + "/resourcegroups/" + name + "/providers/microsoft.insights/workbooktemplates"
		s.lists[list], s.version[list] = []any{}, version
		lists = append(lists, "GET "+list)
		if i == 3 || i == 11 {
			raw := maps.Clone(template)
			id, _, _ := parseID(text(raw["id"]))
			raw["id"] = strings.Replace(id, "/resourcegroups/my-resource-group/", "/resourcegroups/"+name+"/", 1)
			s.add(raw, version)
			s.lists[list] = []any{raw}
		}
	}
	s.lists[root+"/resourcegroups"] = groups
	s.lists[root+"/providers/microsoft.authorization/locks"] = []any{}
	r := s.runtime(t)
	calls, peak := countCalls(s, func(path string) bool { return strings.HasSuffix(path, "/workbooktemplates") }, 2*time.Millisecond)
	page, err := r.List(t.Context(), productRequest(r, insightsWorkbookTemplateType))
	if err != nil || len(page.Items) != 2 {
		t.Fatal("template inventory changed", len(page.Items), err)
	}
	for i, list := range lists {
		group := "GET " + root + fmt.Sprintf("/resourcegroups/group%02d", i)
		// Each scan has two snapshots; a group with templates lists twice per snapshot.
		if want := map[bool]int{true: 4, false: 2}[i == 3 || i == 11]; calls[list] != want || (want == 2) != (calls[group] == 0) {
			t.Fatal("template probe reads changed", list, calls[list], calls[group])
		}
	}
	if peak.peak < 2 || peak.peak > detailReadConcurrency {
		t.Fatal("template probes were not concurrent within the bound", peak.peak)
	}
	s.status[strings.TrimPrefix(lists[5], "GET ")] = 403
	s.handle = func(req *http.Request) (*http.Response, bool) {
		if "GET "+strings.ToLower(req.URL.Path) == lists[14] {
			return jsonResponse(500, map[string]any{"error": map[string]any{"code": "LaterFailed"}}, nil), true
		}
		return nil, false
	}
	for range 3 {
		if _, err := r.List(t.Context(), productRequest(r, insightsWorkbookTemplateType)); err == nil || strings.Contains(err.Error(), "LaterFailed") {
			t.Fatal("first failure in group order lost", err)
		}
	}
}
