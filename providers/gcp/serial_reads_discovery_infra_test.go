package gcp

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

// infraLateFirst answers the request matching first (with status and body) only
// after the request matching second has failed fast with a 400. A serial walk
// reports first's outcome, so ordered evaluation must too.
func infraLateFirst(first, second func(*http.Request) bool, status int, body string, next roundTripFunc) roundTripFunc {
	failed := make(chan struct{})
	var once sync.Once
	return func(req *http.Request) (*http.Response, error) {
		switch {
		case second(req):
			defer once.Do(func() { close(failed) })
			return apiResponse(req, 400, `{"error":{"code":400}}`), nil
		case first(req):
			after(failed)
			return apiResponse(req, status, body), nil
		}
		return next(req)
	}
}

func infraGetSuffix(suffix string) func(*http.Request) bool {
	return func(r *http.Request) bool { return r.Method == "GET" && strings.HasSuffix(r.URL.Path, suffix) }
}

func infraGetContains(part string) func(*http.Request) bool {
	return func(r *http.Request) bool { return r.Method == "GET" && strings.Contains(r.URL.Path, part) }
}

// infraFake serves Config rows by name, lists the direct children of existing
// parents, and serves other hosts' rows keyed by full resource name.
type infraFake map[string]map[string]any

func (f infraFake) serve(req *http.Request) (*http.Response, error) {
	if req.URL.Host != infraHost {
		path := strings.TrimPrefix(strings.TrimPrefix(req.URL.Path, "/compute"), "/v1/")
		if row := f["//"+req.URL.Host+"/"+path]; row != nil {
			return dataformResponse(req, 200, row), nil
		}
		return notFound(req)
	}
	name := strings.TrimPrefix(req.URL.Path, "/v1/")
	if row := f[name]; row != nil {
		return dataformResponse(req, 200, row), nil
	}
	collection := last(name)
	parent := strings.TrimSuffix(name, "/"+collection)
	if f[parent] == nil {
		return notFound(req)
	}
	field := collection
	if strings.Contains(parent, "/deploymentGroups/") {
		field = "deploymentGroupRevisions"
	}
	var keys []string
	for key := range f {
		if strings.HasPrefix(key, name+"/") && !strings.Contains(strings.TrimPrefix(key, name+"/"), "/") {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	rows := []any{}
	for _, key := range keys {
		rows = append(rows, f[key])
	}
	return dataformResponse(req, 200, map[string]any{field: rows}), nil
}

const infraTestGroup = infraTestParent + "/deploymentGroups/application"

func infraTestID(name string) string { return "//" + infraHost + "/" + name }

func infraTestNet(i int) string {
	return fmt.Sprintf("//compute.googleapis.com/projects/sample-project/global/networks/net-%02d", i)
}

func infraTestNetRow(i int) map[string]any {
	return map[string]any{"name": fmt.Sprintf("net-%02d", i), "id": fmt.Sprint(1000 + i), "creationTimestamp": "2026-08-01T00:00:00Z", "selfLink": fmt.Sprintf("https://www.googleapis.com/compute/v1/projects/sample-project/global/networks/net-%02d", i)}
}

// infraManyFake holds a deployment whose latest revision r-00 owns twelve
// networks, eleven more empty revisions, and a group of twelve deployments
// d-00..d-11 with twelve group revisions g-00..g-11.
func infraManyFake() infraFake {
	f := infraFake{}
	f[infraTestDeployment] = map[string]any{"name": infraTestDeployment, "createTime": "2026-08-01T00:00:00Z", "state": "ACTIVE", "lockState": "UNLOCKED", "latestRevision": infraTestDeployment + "/revisions/r-00"}
	units := []any{}
	for i := range 12 {
		revision := fmt.Sprintf("%s/revisions/r-%02d", infraTestDeployment, i)
		f[revision] = map[string]any{"name": revision, "createTime": "2026-08-02T00:00:00Z", "state": "APPLIED"}
		resource := fmt.Sprintf("%s/revisions/r-00/resources/n-%02d", infraTestDeployment, i)
		f[resource] = map[string]any{"name": resource, "intent": "CREATE", "state": "RECONCILED", "terraformInfo": map[string]any{"address": fmt.Sprintf("google_compute_network.n%02d", i), "type": "google_compute_network", "id": strings.TrimPrefix(infraTestNet(i), "//compute.googleapis.com/")}, "caiAssets": map[string]any{"compute.googleapis.com/Network": map[string]any{"fullResourceName": infraTestNet(i)}}}
		f[infraTestNet(i)] = infraTestNetRow(i)
		deployment := fmt.Sprintf("%s/deployments/d-%02d", infraTestParent, i)
		f[deployment] = map[string]any{"name": deployment, "createTime": "2026-08-01T00:00:00Z", "state": "ACTIVE", "lockState": "UNLOCKED"}
		units = append(units, map[string]any{"id": fmt.Sprintf("u-%02d", i), "deployment": deployment})
		groupRevision := fmt.Sprintf("%s/revisions/g-%02d", infraTestGroup, i)
		f[groupRevision] = map[string]any{"name": groupRevision, "createTime": "2026-08-02T00:00:00Z", "snapshot": map[string]any{"name": infraTestGroup, "createTime": "2026-08-01T00:00:00Z", "provisioningState": "FAILED_TO_PROVISION"}}
	}
	f[infraTestGroup] = map[string]any{"name": infraTestGroup, "createTime": "2026-08-01T00:00:00Z", "state": "ACTIVE", "deploymentUnits": units}
	return f
}

// infraManyAction binds an action of kind to a transport over a fresh fake.
func infraManyAction(t *testing.T, kind, name string, wrap func(roundTripFunc) roundTripFunc) (*action, infraFake) {
	f := infraManyFake()
	a := protocolAction(t, kind, name, wrap(f.serve))
	a.identity = asset.Identity{Provider: asset.ProviderGCP, ConnectionID: "connection", Partition: "google-cloud", NativeType: kind, NativeID: infraTestID(name)}
	return a, f
}

// checkInfraLoop runs one loop under a probe, then again with item 0
// answering late and item 1 failing fast, and expects item 0's denial.
func checkInfraLoop(t *testing.T, match func(*http.Request) bool, calls int, first, second string, denial string, run func(*action, infraFake) error) {
	t.Helper()
	probe := newReadProbe(groupReadConcurrency, match)
	a, f := infraManyAction(t, infraDeployment, infraTestDeployment, probe.wrap)
	probe.start()
	if err := run(a, f); err != nil {
		t.Fatal(err)
	}
	probe.check(t, calls, groupReadConcurrency)
	a, f = infraManyAction(t, infraDeployment, infraTestDeployment, func(next roundTripFunc) roundTripFunc {
		return infraLateFirst(infraGetSuffix(first), infraGetSuffix(second), 200, `{}`, next)
	})
	if err := run(a, f); deniedCode(err) != denial {
		t.Fatalf("got %v, want %s", err, denial)
	}
}

func TestInfraListReadsDetailsConcurrentlyInOrder(t *testing.T) {
	checkInfraLoop(t, infraGetContains("/revisions/r-"), 12, "/r-00", "/r-01", "infra_resource_identity_changed", func(a *action, _ infraFake) error {
		records, err := a.client.infraList(t.Context(), infraRevision, infraTestID(infraTestDeployment), true)
		if err == nil && len(records) != 12 {
			t.Fatal(records)
		}
		return err
	})
}

func TestInfraRecordsListsSiblingSubtreesConcurrentlyInOrder(t *testing.T) {
	// Each revision's resource list is a subtree read. r-00's list answers late
	// with an invalid row; r-01's fails fast.
	probe := newReadProbe(groupReadConcurrency, infraGetSuffix("/resources"))
	a, _ := infraManyAction(t, infraDeployment, infraTestDeployment, probe.wrap)
	probe.start()
	records, err := a.client.infraRecords(t.Context(), infraDeployment, infraTestID(infraTestDeployment), false)
	if err != nil || len(records) != 24 {
		t.Fatal(len(records), err)
	}
	probe.check(t, 12, groupReadConcurrency)
	a, _ = infraManyAction(t, infraDeployment, infraTestDeployment, func(next roundTripFunc) roundTripFunc {
		return infraLateFirst(infraGetSuffix("r-00/resources"), infraGetSuffix("r-01/resources"), 200, `{"resources":[{"name":"invalid"}]}`, next)
	})
	if _, err := a.client.infraRecords(t.Context(), infraDeployment, infraTestID(infraTestDeployment), false); deniedCode(err) != "infra_child_list_invalid" {
		t.Fatal(err)
	}
}

func TestInfraSnapshotReadsPhysicalMembersConcurrentlyInOrder(t *testing.T) {
	compute := func(r *http.Request) bool { return r.Method == "GET" && r.URL.Host == "compute.googleapis.com" }
	checkInfraLoop(t, compute, 12, "/net-00", "/net-01", "infra_physical_identity_changed", func(a *action, f infraFake) error {
		members, err := a.client.infraSnapshot(t.Context(), infraDeployment, infraTestID(infraTestDeployment), cloneParameters(f[infraTestDeployment]))
		if err == nil && len(members) != 36 {
			t.Fatal(members)
		}
		return err
	})
}

// Each group deployment is read, snapshotted and re-read inside one task.
var groupDeploymentGet = func(r *http.Request) bool {
	return r.Method == "GET" && strings.Contains(r.URL.Path, "/deployments/d-") && !strings.HasSuffix(r.URL.Path, "/revisions")
}

func TestInfraGroupSnapshotReadsDeploymentsConcurrentlyInOrder(t *testing.T) {
	checkInfraLoop(t, groupDeploymentGet, 24, "/d-00", "/d-01", "infra_resource_identity_changed", func(a *action, f infraFake) error {
		members, err := a.client.infraGroupSnapshot(t.Context(), infraTestID(infraTestGroup), cloneParameters(f[infraTestGroup]))
		if err == nil && len(members) != 24 {
			t.Fatal(members)
		}
		return err
	})
}

func TestInfraGroupDetachedDeploymentsObservedConcurrentlyInOrder(t *testing.T) {
	checkInfraLoop(t, groupDeploymentGet, 24, "/d-00", "/d-01", "infra_resource_identity_changed", func(a *action, f infraFake) error {
		request := contracts.ActionRequest{Action: "delete", Asset: asset.Asset{ID: "group", Identity: asset.Identity{Provider: asset.ProviderGCP, ConnectionID: "connection", Partition: "google-cloud", NativeType: infraGroup, NativeID: infraTestID(infraTestGroup)}}}
		for i := range 12 {
			name := fmt.Sprintf("%s/deployments/d-%02d", infraTestParent, i)
			normalized := cloneParameters(f[name])
			normalized[infraSnapshotKey] = infraManifestHash(infraConfiguration(f[name]), "null")
			request.LifecycleImpacts = append(request.LifecycleImpacts, contracts.ActionImpact{ControllerID: "group", Delete: false, Asset: asset.Asset{ID: asset.AssetID(name), Identity: asset.Identity{Provider: asset.ProviderGCP, ConnectionID: "connection", Partition: "google-cloud", NativeType: infraDeployment, NativeID: infraTestID(name)}, Normalized: normalized}})
		}
		exists, err := a.infraGroupDeploymentsObserved(t.Context(), request, "DETACH")
		if exists {
			t.Fatal("detached deployments reported as deleting")
		}
		return err
	})
}

func TestInfraGroupMetadataReadsConcurrentlyInOrder(t *testing.T) {
	checkInfraLoop(t, infraGetContains("/revisions/g-"), 12, "/g-00", "/g-01", "infra_resource_identity_changed", func(a *action, f infraFake) error {
		var members []infraMember
		for i := range 12 {
			name := fmt.Sprintf("%s/revisions/g-%02d", infraTestGroup, i)
			members = append(members, infraMember{Kind: infraGroupRevision, ID: infraTestID(name), Proof: infraConfiguration(f[name])})
		}
		found, err := a.infraGroupMetadataExists(t.Context(), members)
		if err == nil && !found {
			t.Fatal("revisions not found")
		}
		return err
	})
}

func TestInfraReadbackReadsMembersConcurrentlyInOrder(t *testing.T) {
	compute := func(r *http.Request) bool { return r.Method == "GET" && r.URL.Host == "compute.googleapis.com" }
	checkInfraLoop(t, compute, 12, "/net-00", "/net-01", "infra_physical_identity_changed", func(a *action, f infraFake) error {
		var members []infraMember
		for i := range 12 {
			members = append(members, infraMember{Kind: "compute.googleapis.com/Network", ID: infraTestNet(i), Proof: infraPhysicalConfiguration(f[infraTestNet(i)])})
		}
		exists, err := a.infraMembersExist(t.Context(), members, "DELETE")
		if err == nil && !exists {
			t.Fatal("members not found")
		}
		return err
	})
}

const infraTestTopic = "projects/sample-project/topics/t-%02d"

// infraTopicRequest has twelve managed topics t-00..t-11 under the deployment.
func infraTopicRequest(f infraFake) contracts.ActionRequest {
	request := contracts.ActionRequest{Action: "delete", Asset: asset.Asset{ID: "root", Identity: asset.Identity{Provider: asset.ProviderGCP, ConnectionID: "connection", Partition: "google-cloud", NativeType: infraDeployment, NativeID: infraTestID(infraTestDeployment)}}}
	for i := range 12 {
		name := fmt.Sprintf(infraTestTopic, i)
		row := map[string]any{"name": name}
		f["//pubsub.googleapis.com/"+name] = row
		request.LifecycleImpacts = append(request.LifecycleImpacts, contracts.ActionImpact{ControllerID: "root", Delete: true, Asset: asset.Asset{ID: asset.AssetID(name), Identity: asset.Identity{Provider: asset.ProviderGCP, ConnectionID: "connection", Partition: "google-cloud", NativeType: "pubsub.googleapis.com/Topic", NativeID: "//pubsub.googleapis.com/" + name}, Normalized: row}})
	}
	return request
}

func topicGet(r *http.Request) bool {
	return r.Method == "GET" && r.URL.Host == "pubsub.googleapis.com"
}

func TestInfraManagedPreflightRunsConcurrentlyInOrder(t *testing.T) {
	probe := newReadProbe(groupReadConcurrency, topicGet)
	a, f := infraManyAction(t, infraDeployment, infraTestDeployment, probe.wrap)
	request := infraTopicRequest(f)
	probe.start()
	if err := a.infraManagedPreflight(t.Context(), request, "DELETE"); err != nil {
		t.Fatal(err)
	}
	probe.check(t, 12, groupReadConcurrency)
	// t-00 is gone (not ready), answered late; t-01 fails fast.
	a, f = infraManyAction(t, infraDeployment, infraTestDeployment, func(next roundTripFunc) roundTripFunc {
		return infraLateFirst(infraGetSuffix("/t-00"), infraGetSuffix("/t-01"), 404, `{"error":{"code":404}}`, next)
	})
	if err := a.infraManagedPreflight(t.Context(), infraTopicRequest(f), "DELETE"); deniedCode(err) != "infra_managed_resource_not_ready" {
		t.Fatal(err)
	}
	if err := a.infraManagedPreflight(t.Context(), infraTopicRequest(f), "ABANDON"); err != nil {
		t.Fatal(err)
	}
}

func TestInfraManagedReadbackRunsConcurrentlyInOrder(t *testing.T) {
	probe := newReadProbe(groupReadConcurrency, topicGet)
	a, f := infraManyAction(t, infraDeployment, infraTestDeployment, probe.wrap)
	request := infraTopicRequest(f)
	probe.start()
	exists, err := a.infraManagedReadback(t.Context(), request, "DELETE")
	if err != nil || !exists {
		t.Fatal(exists, err)
	}
	probe.check(t, 12, groupReadConcurrency)
	// t-00 is denied permission late; t-01 fails fast with another error.
	a, f = infraManyAction(t, infraDeployment, infraTestDeployment, func(next roundTripFunc) roundTripFunc {
		return infraLateFirst(infraGetSuffix("/t-00"), infraGetSuffix("/t-01"), 403, `{"error":{"code":403}}`, next)
	})
	if _, err := a.infraManagedReadback(t.Context(), infraTopicRequest(f), "DELETE"); deniedCode(err) != "403" {
		t.Fatal(err)
	}
}

const deSessions = "/sessions/s-"

// discoveryManySessions adds twelve sessions s-00..s-11 under the engine.
func discoveryManySessions(t *testing.T) *discoveryScenario {
	s := newDiscoveryScenario(t)
	session := s.resources[deEngine+"/sessions/session-1"]
	for i := range 12 {
		name := fmt.Sprintf("%s%s%02d", deEngine, deSessions, i)
		row := cloneParameters(session)
		row["name"] = name
		s.resources[name] = row
	}
	return s
}

// discoveryManyEngines replaces the collection's engine with twelve engines
// that use another data store.
func discoveryManyEngines(t *testing.T) *discoveryScenario {
	s := newDiscoveryScenario(t)
	engine := s.resources[deEngine]
	delete(s.resources, deEngine)
	for i := range 12 {
		name := fmt.Sprintf("%s/engines/e-%02d", deCollection, i)
		row := cloneParameters(engine)
		row["name"], row["dataStoreIds"] = name, []any{"other"}
		s.resources[name] = row
	}
	return s
}

func discoveryDenial(t *testing.T, kind, name string) string {
	t.Helper()
	c := &client{project: "sample-project", number: "123456"}
	code := deniedCode(c.discoveryIdentity(kind, "//"+discoveryHost+"/"+name, map[string]any{}))
	if code == "" {
		t.Fatal("empty response is not denied")
	}
	return code
}

func TestDiscoveryChildrenReadConcurrentlyInOrder(t *testing.T) {
	s := discoveryManySessions(t)
	parent := asset.Identity{NativeType: discoveryEngineType, NativeID: "//" + discoveryHost + "/" + deEngine}
	probe := newReadProbe(groupReadConcurrency, infraGetContains(deSessions))
	c := protocolAction(t, discoveryEngineType, deEngine, probe.wrap(s.transport(t))).client
	probe.start()
	children, err := c.discoveryChildren(t.Context(), parent, s.resources[deEngine])
	if err != nil || len(children) < 12 {
		t.Fatal(children, err)
	}
	probe.check(t, 12, groupReadConcurrency)
	c = protocolAction(t, discoveryEngineType, deEngine, infraLateFirst(infraGetSuffix("/s-00"), infraGetSuffix("/s-01"), 200, `{}`, s.transport(t))).client
	if _, err := c.discoveryChildren(t.Context(), parent, s.resources[deEngine]); deniedCode(err) != discoveryDenial(t, discoveryHost+"/Session", deEngine+deSessions+"00") {
		t.Fatal(err)
	}
}

func TestDiscoveryDataStoreEngineReadsConcurrentlyInOrder(t *testing.T) {
	s := discoveryManyEngines(t)
	store := "//" + discoveryHost + "/" + deStore
	probe := newReadProbe(groupReadConcurrency, infraGetContains("/engines/e-"))
	c := protocolAction(t, discoveryEngineType, deEngine, probe.wrap(s.transport(t))).client
	probe.start()
	if err := c.discoveryDataStoreUnused(t.Context(), store); err != nil {
		t.Fatal(err)
	}
	probe.check(t, 12, groupReadConcurrency)
	c = protocolAction(t, discoveryEngineType, deEngine, infraLateFirst(infraGetSuffix("/e-00"), infraGetSuffix("/e-01"), 200, `{}`, s.transport(t))).client
	if err := c.discoveryDataStoreUnused(t.Context(), store); deniedCode(err) != discoveryDenial(t, discoveryEngineType, deCollection+"/engines/e-00") {
		t.Fatal(err)
	}
}

func TestDiscoveryReadbackReadsImpactsConcurrentlyInOrder(t *testing.T) {
	s := discoveryManySessions(t)
	_, _, _, request := discoveryReviewed(t, s, deEngine)
	// Members are read back once the engine itself is gone.
	delete(s.resources, deEngine)
	probe := newReadProbe(groupReadConcurrency, infraGetContains(deSessions))
	driver, err := protocolRuntime(t, probe.wrap(s.transport(t))).ResolveAction(context.Background(), "connection", request.Asset)
	if err != nil {
		t.Fatal(err)
	}
	probe.start()
	read, err := driver.Readback(context.Background(), request)
	if err != nil || !read.Exists {
		t.Fatal(read, err)
	}
	probe.check(t, 12, groupReadConcurrency)
	// Whichever of s-00 and s-01 comes first in impact order answers late with
	// a denial; the other fails fast.
	ids := make([]string, len(request.LifecycleImpacts))
	for i, impact := range request.LifecycleImpacts {
		ids[i] = impact.Asset.Identity.NativeID
	}
	first, second := "/s-00", "/s-01"
	if slices.Index(ids, "//"+discoveryHost+"/"+deEngine+deSessions+"01") < slices.Index(ids, "//"+discoveryHost+"/"+deEngine+deSessions+"00") {
		first, second = second, first
	}
	driver, err = protocolRuntime(t, infraLateFirst(infraGetSuffix(first), infraGetSuffix(second), 200, `{}`, s.transport(t))).ResolveAction(context.Background(), "connection", request.Asset)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := driver.Readback(context.Background(), request); deniedCode(err) != discoveryDenial(t, discoveryHost+"/Session", deEngine+"/sessions"+first) {
		t.Fatal(err)
	}
}
