package gcp

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

// firstLateSecondFails answers first's request with late only after second's
// request has failed fast (bounded, so a serial walk still finishes). A serial
// walk reports first's result, so concurrent reads must too.
func firstLateSecondFails(first, second func(*http.Request) bool, late, next roundTripFunc) roundTripFunc {
	failed := make(chan struct{})
	var once sync.Once
	return func(req *http.Request) (*http.Response, error) {
		switch {
		case second(req):
			defer once.Do(func() { close(failed) })
			return apiResponse(req, 400, `{"error":{"code":400}}`), nil
		case first(req):
			after(failed)
			return late(req)
		}
		return next(req)
	}
}

// renamed answers next's response with field set to value.
func renamed(next roundTripFunc, field string, value any) roundTripFunc {
	return func(req *http.Request) (*http.Response, error) {
		response, err := next(req)
		if err != nil {
			return nil, err
		}
		var data map[string]any
		if json.NewDecoder(response.Body).Decode(&data) != nil {
			return nil, fmt.Errorf("undecodable fixture response")
		}
		data[field] = value
		return dataformResponse(req, response.StatusCode, data), nil
	}
}

func getPath(suffix string) func(*http.Request) bool {
	return func(r *http.Request) bool { return r.Method == "GET" && strings.HasSuffix(r.URL.Path, suffix) }
}

func listAll(t *testing.T, r *Runtime, request contracts.InventoryRequest) ([]contracts.InventoryItem, error) {
	t.Helper()
	var items []contracts.InventoryItem
	for range 40 {
		batch, err := r.List(t.Context(), request)
		if err != nil {
			return nil, err
		}
		items = append(items, batch.Items...)
		if batch.Complete {
			return items, nil
		}
		request.Cursor = batch.NextCursor
	}
	t.Fatal("unfinished inventory pagination")
	return nil, nil
}

func scenarioClient(t *testing.T, transport roundTripFunc) *client {
	t.Helper()
	c, err := protocolRuntime(t, transport).resolve(t.Context(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

const manyReads = groupReadConcurrency + 2

// Ten repositories, each in its own user folder.
func dataformManyRepositories(t *testing.T) *dataformScenario {
	s := newDataformScenario(t)
	warehouse := s.resources[dataformRoot]
	collection := dataformUS + "/repositories"
	s.members[collection] = nil
	for i := range manyReads {
		folder := fmt.Sprintf("%s/folders/f-%02d", dataformUS, i)
		s.resources[folder] = map[string]any{"name": folder, "displayName": "F", "createTime": "2025-01-01T00:00:00Z", "creatorIamPrincipal": "user:fixture@example.com"}
		name := fmt.Sprintf("%s/r-%02d", collection, i)
		repository := cloneParameters(warehouse)
		repository["name"], repository["containingFolder"] = name, folder
		s.resources[name] = repository
		s.members[collection] = append(s.members[collection], name)
	}
	return s
}

func TestProductInventoryReadsDataformContainersConcurrentlyInOrder(t *testing.T) {
	folderGet := func(r *http.Request) bool { return r.Method == "GET" && strings.Contains(r.URL.Path, "/folders/f-") }
	s := dataformManyRepositories(t)
	probe := newReadProbe(groupReadConcurrency, folderGet)
	r := protocolRuntime(t, probe.wrap(s.transport(t)))
	probe.start()
	items, err := listAll(t, r, productRequest(r, dataformRepositoryType, "us-central1"))
	if err != nil || len(items) != manyReads {
		t.Fatal(len(items), err)
	}
	for i, item := range items {
		if want := fmt.Sprintf("//dataform.googleapis.com/%s/folders/f-%02d", dataformUS, i); item.Normalized["_dataform_container_name"] != want || text(item.Normalized[dataformContainerChain]) == "" {
			t.Fatalf("item %d lost its container: %+v", i, item.Normalized)
		}
	}
	probe.check(t, manyReads, productReadConcurrency)

	next := s.transport(t)
	r = protocolRuntime(t, firstLateSecondFails(getPath("/folders/f-00"), getPath("/folders/f-01"), renamed(next, "name", dataformUS+"/folders/moved"), next))
	if _, err := listAll(t, r, productRequest(r, dataformRepositoryType, "us-central1")); deniedCode(err) != "dataform_identity_changed" {
		t.Fatal(err)
	}
}

func monitoringManyGroups(next roundTripFunc) roundTripFunc {
	return func(req *http.Request) (*http.Response, error) {
		path := strings.TrimPrefix(req.URL.Path, "/v3/")
		switch {
		case path == "projects/sample-project/groups":
			var groups []any
			for i := range manyReads {
				group := monitoringGroupFixture()
				group["name"] = fmt.Sprintf("projects/sample-project/groups/100%02d", i)
				groups = append(groups, group)
			}
			return dataformResponse(req, 200, map[string]any{"group": groups}), nil
		case strings.HasSuffix(path, "/members"):
			return dataformResponse(req, 200, map[string]any{"members": []any{monitoringGroupMemberFixture()}, "totalSize": 1}), nil
		case strings.HasPrefix(path, "projects/sample-project/groups/"):
			group := monitoringGroupFixture()
			group["name"] = path
			return dataformResponse(req, 200, group), nil
		}
		return next(req)
	}
}

func TestProductInventoryReadsMonitoringGroupMembersConcurrentlyInOrder(t *testing.T) {
	unexpected := func(req *http.Request) (*http.Response, error) { return nil, fmt.Errorf("unexpected %s", req.URL) }
	membersList := func(r *http.Request) bool { return strings.HasSuffix(r.URL.Path, "/members") }
	probe := newReadProbe(groupReadConcurrency, membersList)
	r := protocolRuntime(t, probe.wrap(monitoringManyGroups(unexpected)))
	probe.start()
	items, err := listAll(t, r, productRequest(r, monitoringGroupType, "global"))
	if err != nil || len(items) != manyReads {
		t.Fatal(len(items), err)
	}
	for _, item := range items {
		if item.Normalized["_monitoring_group_member_configuration"] == nil || !strings.HasSuffix(text(item.Normalized["name"]), last(item.NativeID)) {
			t.Fatalf("lost group members: %+v", item.Normalized)
		}
	}
	// Each group lists its members twice around the group read.
	probe.check(t, 2*manyReads, productReadConcurrency)

	next := monitoringManyGroups(unexpected)
	r = protocolRuntime(t, firstLateSecondFails(getPath("/groups/10000"), getPath("/groups/10001/members"), renamed(next, "filter", `resource.type = "drift"`), next))
	if _, err := listAll(t, r, productRequest(r, monitoringGroupType, "global")); deniedCode(err) != "monitoring_group_configuration_changed" {
		t.Fatal(err)
	}
}

func storageManyPools(next roundTripFunc) roundTripFunc {
	return func(req *http.Request) (*http.Response, error) {
		path := req.URL.Path
		switch {
		case strings.HasSuffix(path, "/aggregated/storagePools"):
			var pools []any
			for i := range manyReads {
				pools = append(pools, storagePoolFixture("us-central1-a", fmt.Sprintf("p-%02d", i)))
			}
			return dataformResponse(req, 200, map[string]any{"items": map[string]any{"zones/us-central1-a": map[string]any{"storagePools": pools}}}), nil
		case strings.HasSuffix(path, "/listDisks"):
			return dataformResponse(req, 200, map[string]any{"kind": "compute#storagePoolListDisks"}), nil
		case strings.Contains(path, "/storagePools/"):
			return dataformResponse(req, 200, storagePoolFixture("us-central1-a", last(path))), nil
		}
		return next(req)
	}
}

func TestProductInventoryReadsStoragePoolDisksConcurrentlyInOrder(t *testing.T) {
	unexpected := func(req *http.Request) (*http.Response, error) { return nil, fmt.Errorf("unexpected %s", req.URL) }
	listDisks := func(r *http.Request) bool { return strings.HasSuffix(r.URL.Path, "/listDisks") }
	probe := newReadProbe(groupReadConcurrency, listDisks)
	r := protocolRuntime(t, probe.wrap(storageManyPools(unexpected)))
	probe.start()
	items, err := listAll(t, r, productRequest(r, storagePoolType, "us-central1"))
	if err != nil || len(items) != manyReads {
		t.Fatal(len(items), err)
	}
	for _, item := range items {
		if text(item.Normalized[poolSnapshotKey]) == "" || item.Actionable == nil {
			t.Fatalf("lost pool disks: %+v", item.Normalized)
		}
	}
	probe.check(t, manyReads, productReadConcurrency)

	next := storageManyPools(unexpected)
	foreign := func(req *http.Request) (*http.Response, error) {
		return dataformResponse(req, 200, map[string]any{"kind": "compute#storagePoolListDisks", "items": []any{map[string]any{"name": "other", "disk": "projects/sample-project/zones/us-central1-a/disks/disk"}}}), nil
	}
	r = protocolRuntime(t, firstLateSecondFails(getPath("/p-00/listDisks"), getPath("/p-01/listDisks"), foreign, next))
	if _, err := listAll(t, r, productRequest(r, storagePoolType, "us-central1")); deniedCode(err) != "storage_pool_disk_identity_changed" {
		t.Fatal(err)
	}
}

// Ten deployments without revisions beside the fixture's European deployment.
func infraManyDeployments(t *testing.T) *infraScenario {
	s := newInfraScenario(t)
	parent := "projects/sample-project/locations/europe-west1/deployments/"
	for i := range manyReads {
		deployment := cloneParameters(s.resources[parent+"stack-a"])
		deployment["name"] = fmt.Sprintf("%sd-%02d", parent, i)
		s.resources[text(deployment["name"])] = deployment
	}
	return s
}

func TestProductInventoryReadsInfraSnapshotsConcurrentlyInOrder(t *testing.T) {
	deploymentGet := func(r *http.Request) bool {
		return r.Method == "GET" && strings.Contains(r.URL.Path, "/deployments/d-") && !strings.HasSuffix(r.URL.Path, "/revisions")
	}
	s := infraManyDeployments(t)
	probe := newReadProbe(groupReadConcurrency, deploymentGet)
	r := protocolRuntime(t, probe.wrap(s.transport(t)))
	probe.start()
	items, err := listAll(t, r, productRequest(r, infraDeployment, "europe-west1"))
	if err != nil || len(items) != manyReads+1 {
		t.Fatal(len(items), err)
	}
	for _, item := range items {
		if text(item.Normalized[infraSnapshotKey]) == "" {
			t.Fatalf("lost deployment snapshot: %+v", item.Normalized)
		}
	}
	// Each snapshot ends with one deployment read.
	probe.check(t, manyReads, productReadConcurrency)

	s = infraManyDeployments(t)
	next := s.transport(t)
	r = protocolRuntime(t, firstLateSecondFails(getPath("/deployments/d-00"), getPath("/deployments/d-01/revisions"), renamed(next, "labels", map[string]any{"drift": "true"}), next))
	if _, err := listAll(t, r, productRequest(r, infraDeployment, "europe-west1")); deniedCode(err) != "infra_configuration_changed" {
		t.Fatal(err)
	}
}

// A queued resource with ten nodes, each with its own data disk.
func tpuManyNodes(t *testing.T) *tpuScenario {
	s := newTPUScenario(t)
	object(array(object(s.resources[tpuTestQueue]["tpu"])["nodeSpec"])[0])["multisliceParams"] = map[string]any{"nodeCount": manyReads}
	template, disk := s.resources[tpuTestNode1], s.resources["projects/sample-project/zones/us-central2-b/disks/data-1"]
	for i := 2; i < manyReads; i++ {
		node := roundTripDataformJSON(t, template)
		node["name"], node["id"] = fmt.Sprintf("%s/nodes/training-%d", tpuTestParent, i), fmt.Sprint(1000+i)
		name := fmt.Sprintf("projects/sample-project/zones/us-central2-b/disks/data-%d", i)
		node["dataDisks"] = []any{map[string]any{"sourceDisk": name, "mode": "READ_WRITE"}}
		s.resources[text(node["name"])] = node
		data := roundTripDataformJSON(t, disk)
		data["name"], data["id"], data["selfLink"] = last(name), fmt.Sprint(2000+i), "https://www.googleapis.com/compute/v1/"+name
		s.resources[name] = data
	}
	return s
}

func tpuDiskGet(r *http.Request) bool {
	return r.Method == "GET" && strings.Contains(r.URL.Path, "/zones/us-central2-b/disks/data-")
}

func TestTPUInventoryReadsNodesConcurrentlyInOrder(t *testing.T) {
	s := tpuManyNodes(t)
	probe := newReadProbe(groupReadConcurrency, tpuDiskGet)
	r := protocolRuntime(t, probe.wrap(s.transport(t)))
	probe.start()
	items, err := listAll(t, r, productRequest(r, tpuNodeType, "project"))
	if err != nil || len(items) != manyReads+1 {
		t.Fatal(len(items), err)
	}
	probe.check(t, manyReads, productReadConcurrency)

	s = tpuManyNodes(t)
	next := s.transport(t)
	r = protocolRuntime(t, firstLateSecondFails(getPath("/disks/data-0"), getPath("/disks/data-1"), renamed(next, "id", ""), next))
	if _, err := listAll(t, r, productRequest(r, tpuNodeType, "project")); deniedCode(err) != "tpu_data_disk_identity_changed" {
		t.Fatal(err)
	}
}

func TestTPUNodeSnapshotReadsNodeDisksConcurrentlyInOrder(t *testing.T) {
	s := tpuManyNodes(t)
	probe := newReadProbe(groupReadConcurrency, tpuDiskGet)
	c := scenarioClient(t, probe.wrap(s.transport(t)))
	queue := "//tpu.googleapis.com/" + tpuTestQueue
	probe.start()
	nodes, proofs, err := c.tpuNodeSnapshot(t.Context(), queue, s.resources[tpuTestQueue])
	if err != nil || len(nodes) != manyReads || len(proofs) != manyReads {
		t.Fatal(len(nodes), len(proofs), err)
	}
	for i, proof := range proofs {
		if proof.ID != nodes[i].id || !strings.Contains(proof.Disks, fmt.Sprintf("disks/data-%d", i)) {
			t.Fatalf("node %d proof out of order: %+v", i, proof)
		}
	}
	probe.check(t, manyReads, groupReadConcurrency)

	next := s.transport(t)
	c = scenarioClient(t, firstLateSecondFails(getPath("/disks/data-0"), getPath("/disks/data-1"), renamed(next, "id", ""), next))
	if _, _, err := c.tpuNodeSnapshot(t.Context(), queue, s.resources[tpuTestQueue]); deniedCode(err) != "tpu_data_disk_identity_changed" {
		t.Fatal(err)
	}
}

func TestTPUQueuePreflightVerifiesPrerequisiteDisksConcurrentlyInOrder(t *testing.T) {
	setup := func(wrap func(roundTripFunc) roundTripFunc) (contracts.ActionDriver, contracts.ActionRequest) {
		s := tpuManyNodes(t)
		_, assets, result := tpuReviewed(t, s, tpuTestQueue)
		queue := batchAsset(assets, tpuTestQueue)
		request := dataformRequest(t, result, assets, queue)
		if len(request.PrerequisiteDeletions) != manyReads {
			t.Fatalf("prerequisites %d", len(request.PrerequisiteDeletions))
		}
		for name := range s.resources {
			if strings.Contains(name, "/nodes/training-") {
				delete(s.resources, name)
			}
		}
		s.resources[tpuTestQueue]["state"] = map[string]any{"state": "SUSPENDED"}
		driver, err := protocolRuntime(t, wrap(s.transport(t))).ResolveAction(t.Context(), "connection", queue)
		if err != nil {
			t.Fatal(err)
		}
		return driver, request
	}
	probe := newReadProbe(groupReadConcurrency, tpuDiskGet)
	driver, request := setup(probe.wrap)
	probe.start()
	if _, err := driver.Preflight(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	probe.check(t, manyReads, groupReadConcurrency)

	disk := func(impact contracts.ActionImpact) string {
		return "/disks/" + last(text(object(array(impact.Asset.Normalized["dataDisks"])[0])["sourceDisk"]))
	}
	var first, second string
	driver, request = setup(func(next roundTripFunc) roundTripFunc {
		return firstLateSecondFails(func(r *http.Request) bool { return getPath(first)(r) }, func(r *http.Request) bool { return getPath(second)(r) }, renamed(next, "id", "recreated"), next)
	})
	first, second = disk(request.PrerequisiteDeletions[0]), disk(request.PrerequisiteDeletions[1])
	if _, err := driver.Preflight(t.Context(), request); deniedCode(err) != "tpu_retained_disk_changed" {
		t.Fatal(err)
	}
}

// Ten folders inside the personal folder.
func dataformManyFolders(t *testing.T, prefix, container string) *dataformFolderScenario {
	s := newDataformFolderScenario(t)
	for i := range manyReads {
		name := fmt.Sprintf("%s/%s%02d", dataformUS, prefix, i)
		raw := map[string]any{"name": name, "displayName": "C", "createTime": "2025-01-01T00:00:00Z", "creatorIamPrincipal": "user:fixture@example.com"}
		if container != "" {
			raw["containingFolder"] = container
			s.contents[container] = append(s.contents[container], name)
		}
		s.resources[name] = raw
		s.contents[name] = []string{}
	}
	return s
}

func TestDataformFolderChildrenReadConcurrentlyInOrder(t *testing.T) {
	childGet := func(r *http.Request) bool { return r.Method == "GET" && strings.Contains(r.URL.Path, "/folders/c-") }
	personal := asset.Identity{Provider: asset.ProviderGCP, NativeType: dataformFolderType, NativeID: "//dataform.googleapis.com/" + dataformPersonal}
	s := dataformManyFolders(t, "folders/c-", dataformPersonal)
	probe := newReadProbe(groupReadConcurrency, childGet)
	c := scenarioClient(t, probe.wrap(s.transport(t)))
	probe.start()
	children, err := c.dataformFolderChildren(t.Context(), personal, s.resources[dataformPersonal])
	if err != nil || len(children) != manyReads {
		t.Fatal(len(children), err)
	}
	probe.check(t, manyReads, groupReadConcurrency)

	next := s.transport(t)
	c = scenarioClient(t, firstLateSecondFails(getPath("/folders/c-00"), getPath("/folders/c-01"), renamed(next, "name", dataformUS+"/folders/moved"), next))
	if _, err := c.dataformFolderChildren(t.Context(), personal, s.resources[dataformPersonal]); deniedCode(err) != "dataform_identity_changed" {
		t.Fatal(err)
	}
}

func TestDataformFolderForestSeedsReadConcurrentlyInOrder(t *testing.T) {
	teamGet := func(r *http.Request) bool {
		return r.Method == "GET" && strings.Contains(r.URL.Path, "/teamFolders/t-")
	}
	s := dataformManyFolders(t, "teamFolders/t-", "")
	search := dataformUS + "/teamFolders:search"
	for i := range manyReads {
		s.seeds[search] = append(s.seeds[search], fmt.Sprintf("%s/teamFolders/t-%02d", dataformUS, i))
	}
	probe := newReadProbe(groupReadConcurrency, teamGet)
	c := scenarioClient(t, probe.wrap(s.transport(t)))
	probe.start()
	nodes, err := c.dataformFolderForest(t.Context(), []string{dataformUS}, false)
	if err != nil || len(nodes) != manyReads+1 {
		t.Fatal(len(nodes), err)
	}
	probe.check(t, manyReads, groupReadConcurrency)

	next := s.transport(t)
	c = scenarioClient(t, firstLateSecondFails(getPath("/teamFolders/t-00"), getPath("/teamFolders/t-01"), renamed(next, "name", dataformUS+"/teamFolders/moved"), next))
	if _, err := c.dataformFolderForest(t.Context(), []string{dataformUS}, false); deniedCode(err) != "dataform_identity_changed" {
		t.Fatal(err)
	}
}

func TestDataformFolderForestQueueReadsConcurrentlyInOrder(t *testing.T) {
	contents := func(r *http.Request) bool {
		return r.Method == "GET" && strings.Contains(r.URL.Path, "/folders/c-") && strings.HasSuffix(r.URL.Path, ":queryFolderContents")
	}
	locations := []string{dataformUS, dataformEU}
	s := dataformManyFolders(t, "folders/c-", dataformPersonal)
	probe := newReadProbe(groupReadConcurrency, contents)
	c := scenarioClient(t, probe.wrap(s.transport(t)))
	probe.start()
	nodes, err := c.dataformFolderForest(t.Context(), locations, true)
	if err != nil || nodes["//dataform.googleapis.com/"+dataformUS+"/folders/c-09"].id == "" {
		t.Fatal(len(nodes), err)
	}
	// Each folder's contents are queried twice, two pages each.
	probe.check(t, 4*manyReads, groupReadConcurrency)

	next := s.transport(t)
	invalid := func(req *http.Request) (*http.Response, error) {
		return dataformResponse(req, 200, map[string]any{"entries": []any{map[string]any{"unknown": map[string]any{}}}}), nil
	}
	c = scenarioClient(t, firstLateSecondFails(getPath("/folders/c-00:queryFolderContents"), getPath("/folders/c-01:queryFolderContents"), invalid, next))
	if _, err := c.dataformFolderForest(t.Context(), locations, true); deniedCode(err) != "invalid_dataform_contents_entry" {
		t.Fatal(err)
	}
}

// inventoryPhase matches a path's GETs after the forest's own reads of it, so
// a test can tell the folder inventory's container reads from the forest's.
func inventoryPhase(t *testing.T, s *dataformFolderScenario) func(path string) func(*http.Request) bool {
	t.Helper()
	var mu sync.Mutex
	counts := map[string]int{}
	next := s.transport(t)
	c := scenarioClient(t, func(req *http.Request) (*http.Response, error) {
		mu.Lock()
		counts[req.URL.Path]++
		mu.Unlock()
		return next(req)
	})
	if _, err := c.dataformFolderForest(t.Context(), []string{dataformUS, dataformEU}, true); err != nil {
		t.Fatal(err)
	}
	return func(path string) func(*http.Request) bool {
		seen := 0
		return func(r *http.Request) bool {
			if r.Method != "GET" || r.URL.Path != "/v1/"+path {
				return false
			}
			mu.Lock()
			defer mu.Unlock()
			seen++
			return seen > counts[r.URL.Path]
		}
	}
}

func TestDataformFolderInventoryReadsContainersConcurrentlyInOrder(t *testing.T) {
	s := dataformManyFolders(t, "folders/c-", dataformPersonal)
	probe := newReadProbe(groupReadConcurrency, inventoryPhase(t, s)(dataformPersonal))
	r := protocolRuntime(t, probe.wrap(s.transport(t)))
	probe.start()
	items, err := listAll(t, r, folderRequest(r, dataformFolderType, "project"))
	if err != nil || len(items) != manyReads+4 {
		t.Fatal(len(items), err)
	}
	for _, item := range items {
		if strings.Contains(item.NativeID, "/folders/c-") && item.Normalized["_dataform_container_name"] != "//dataform.googleapis.com/"+dataformPersonal {
			t.Fatalf("lost folder container: %+v", item.Normalized)
		}
	}
	probe.check(t, manyReads, groupReadConcurrency)

	// Leaf's chain first reads Nested; Nested's chain reads Team.
	s = newDataformFolderScenario(t)
	phase := inventoryPhase(t, s)
	next := s.transport(t)
	r = protocolRuntime(t, firstLateSecondFails(phase(dataformNested), phase(dataformTeam), renamed(next, "name", dataformUS+"/folders/moved"), next))
	if _, err := listAll(t, r, folderRequest(r, dataformFolderType, "project")); deniedCode(err) != "dataform_identity_changed" {
		t.Fatal(err)
	}
}

func TestDataformFolderForestQueriesLocationsConcurrently(t *testing.T) {
	seedQuery := func(r *http.Request) bool {
		return r.Method == "GET" && (strings.HasSuffix(r.URL.Path, "/teamFolders:search") || strings.HasSuffix(r.URL.Path, ":queryUserRootContents"))
	}
	s := newDataformFolderScenario(t)
	var locations []string
	for i := range manyReads {
		location := fmt.Sprintf("projects/sample-project/locations/l-%02d", i)
		locations = append(locations, location)
		s.seeds[location+"/teamFolders:search"] = []string{}
		s.seeds[location+":queryUserRootContents"] = []string{}
	}
	probe := newReadProbe(groupReadConcurrency, seedQuery)
	c := scenarioClient(t, probe.wrap(s.transport(t)))
	probe.start()
	if _, err := c.dataformFolderForest(t.Context(), locations, true); err != nil {
		t.Fatal(err)
	}
	// Two queries per location, two pages each.
	probe.check(t, 4*manyReads, groupReadConcurrency)
}

// A child found in a folder's contents was already checked against a fresh
// read of that folder; adding it must not GET the folder again.
func TestDataformFolderForestReusesKnownParent(t *testing.T) {
	s := dataformManyFolders(t, "folders/c-", dataformPersonal)
	var gets atomic.Int32
	next := s.transport(t)
	c := scenarioClient(t, func(req *http.Request) (*http.Response, error) {
		if req.Method == "GET" && req.URL.Path == "/v1/"+dataformPersonal {
			gets.Add(1)
		}
		return next(req)
	})
	if _, err := c.dataformFolderForest(t.Context(), []string{dataformUS, dataformEU}, true); err != nil {
		t.Fatal(err)
	}
	// Seed read, then the contents walk's read before and after its queries.
	if gets.Load() != 3 {
		t.Fatalf("personal folder GETs = %d, want 3", gets.Load())
	}
}
