package azure

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

// sweep2BatchJobs adds count jobs, each with one task, to the Batch scenario.
func sweep2BatchJobs(t *testing.T, count int) (*batchScenario, *Runtime, []string) {
	t.Helper()
	s, r, _ := newBatchScenario(t)
	job, task := s.records["/jobs/jobid"], s.records["/jobs/jobid/tasks/taskid"]
	var jobs []string
	for i := range count {
		path := fmt.Sprintf("/jobs/j%02d", i)
		j := batchClone(job)
		j["id"], j["url"] = last(path), s.origin+path
		s.records[path] = j
		s.lists["/jobs"] = append(s.lists["/jobs"], j)
		k := batchClone(task)
		k["url"] = s.origin + path + "/tasks/taskid"
		s.records[path+"/tasks/taskid"] = k
		s.lists[path+"/tasks"] = []any{k}
		jobs = append(jobs, path)
	}
	return s, r, jobs
}

func sweep2Deny(paths map[string]string) func(*http.Request) (*http.Response, bool) {
	return func(req *http.Request) (*http.Response, bool) {
		if code, ok := paths[strings.ToLower(req.URL.Path)]; ok && req.Method == "GET" {
			return jsonResponse(403, map[string]any{"code": code, "error": map[string]any{"code": code, "message": code}}, nil), true
		}
		return nil, false
	}
}

// Task inventory lists each job's tasks concurrently (re-reading the job after
// its list), reads listed tasks concurrently, and builds items concurrently;
// results and the first error follow job order.
func TestSweep2BatchTaskInventoryConcurrentInOrder(t *testing.T) {
	s, r, jobs := sweep2BatchJobs(t, 12)
	calls, peak := countCalls(s.arm, func(path string) bool { return strings.HasSuffix(path, "/tasks") }, 5*time.Millisecond)
	page, err := r.List(t.Context(), productRequest(r, batchTaskType))
	if err != nil || len(page.Items) != len(jobs)+1 {
		t.Fatal("Batch task inventory changed", len(page.Items), err)
	}
	for i, job := range jobs {
		if page.Items[i].NativeID != s.origin+job+"/tasks/taskid" {
			t.Fatal("task order changed", i, page.Items[i].NativeID)
		}
		if calls["GET "+job] != 3 || calls["GET "+job+"/tasks"] != 1 || calls["GET "+job+"/tasks/taskid"] != 1 {
			t.Fatal("wrong per-job reads", job, calls["GET "+job], calls["GET "+job+"/tasks"], calls["GET "+job+"/tasks/taskid"])
		}
	}
	if peak.peak < 2 || peak.peak > detailReadConcurrency {
		t.Fatal("task lists were not concurrent within the bound", peak.peak)
	}
	s.handle = sweep2Deny(map[string]string{jobs[3] + "/tasks": "FirstDenied", jobs[9] + "/tasks": "LaterDenied"})
	for range 5 {
		if _, err := r.List(t.Context(), productRequest(r, batchTaskType)); err == nil || !strings.Contains(err.Error(), "FirstDenied") {
			t.Fatal("first failure in job order lost", err)
		}
	}
}

// The topology walk lists each job's tasks concurrently, in both passes.
func TestSweep2BatchTopologyConcurrentInOrder(t *testing.T) {
	s, r, jobs := sweep2BatchJobs(t, 12)
	c, err := r.resolve(t.Context(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	account, err := c.batchAccount(t.Context(), s.account)
	if err != nil {
		t.Fatal(err)
	}
	calls, peak := countCalls(s.arm, func(path string) bool { return strings.HasSuffix(path, "/tasks") }, 5*time.Millisecond)
	topology, err := c.batchTopology(t.Context(), account)
	if err != nil {
		t.Fatal(err)
	}
	for _, job := range jobs {
		task := topology.members[s.origin+job+"/tasks/taskid"]
		if topology.members[s.origin+job].kind != batchJobType || task.kind != batchTaskType || task.parent != s.origin+job {
			t.Fatal("topology lost a job or task", job)
		}
		if calls["GET "+job+"/tasks"] != 2 || calls["GET "+job+"/tasks/taskid"] != 2 {
			t.Fatal("wrong per-job topology reads", job, calls["GET "+job+"/tasks"], calls["GET "+job+"/tasks/taskid"])
		}
	}
	if peak.peak < 2 || peak.peak > detailReadConcurrency {
		t.Fatal("task lists were not concurrent within the bound", peak.peak)
	}
	s.handle = sweep2Deny(map[string]string{jobs[3] + "/tasks": "FirstDenied", jobs[9] + "/tasks": "LaterDenied"})
	for range 5 {
		if _, err := c.batchTopology(t.Context(), account); err == nil || !strings.Contains(err.Error(), "FirstDenied") {
			t.Fatal("first failure in job order lost", err)
		}
	}
}

// The subscription's account index is read concurrently for an endpoint
// lookup and the root index; checks, the match and errors follow list order.
func TestSweep2CommunicationAccountsConcurrentInOrder(t *testing.T) {
	s := newDNSScenario()
	collection := "/subscriptions/" + testSubscription + "/providers/microsoft.communication/communicationservices"
	s.version[collection] = communicationARMVersion
	var ids []string
	for i := range 12 {
		raw := communicationTestAccount(t)
		name := fmt.Sprintf("comms%02d", i)
		raw["id"], raw["name"] = strings.ToLower(resourceID(communicationType, name)), name
		object(raw["properties"])["hostName"] = name + ".unitedstates.communication.azure.com"
		s.add(raw, communicationARMVersion)
		s.lists[collection] = append(s.lists[collection], raw)
		ids = append(ids, text(raw["id"]))
	}
	c, err := s.runtime(t).resolve(t.Context(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	calls, peak := countCalls(s, func(path string) bool { return strings.Contains(path, "/communicationservices/") }, 5*time.Millisecond)
	account, err := c.communicationAccountForEndpoint(t.Context(), "https://comms05.unitedstates.communication.azure.com")
	if err != nil || account.id != ids[5] {
		t.Fatal("endpoint bound another account", account.id, err)
	}
	index, err := c.communicationARMIndex(t.Context(), communicationType, nil)
	if err != nil || len(index) != len(ids) {
		t.Fatal("account index changed", len(index), err)
	}
	for _, id := range ids {
		if calls["GET "+id] != 2 || index[id] == nil {
			t.Fatal("account not read once per walk", id, calls["GET "+id])
		}
	}
	if peak.peak < 2 || peak.peak > detailReadConcurrency {
		t.Fatal("account reads were not concurrent within the bound", peak.peak)
	}
	s.handle = sweep2Deny(map[string]string{ids[3]: "FirstDenied", ids[9]: "LaterDenied"})
	for range 5 {
		if _, err := c.communicationAccountForEndpoint(t.Context(), "https://comms05.unitedstates.communication.azure.com"); err == nil || !strings.Contains(err.Error(), "FirstDenied") {
			t.Fatal("first failure in list order lost", err)
		}
		if _, err := c.communicationARMIndex(t.Context(), communicationType, nil); err == nil || !strings.Contains(err.Error(), "FirstDenied") {
			t.Fatal("first failure in index order lost", err)
		}
	}
	s.handle = nil
	object(s.lists[collection][6].(map[string]any))["type"] = "Microsoft.Compute/disks"
	clear(calls)
	if _, err := c.communicationARMIndex(t.Context(), communicationType, nil); err == nil || !strings.Contains(err.Error(), "invalid_communication_root_index") {
		t.Fatal("invalid row accepted", err)
	}
	for i, id := range ids {
		if want := map[bool]int{true: 1, false: 0}[i < 6]; calls["GET "+id] != want {
			t.Fatal("wrong reads around the invalid row", id, calls["GET "+id])
		}
	}
}

// A factory's child collection is read concurrently and checked in list order.
func TestSweep2DataFactoryIndexConcurrentInOrder(t *testing.T) {
	s := newDNSScenario()
	factory := strings.ToLower(resourceID(dataFactoryType, "factory"))
	collection := factory + "/pipelines"
	s.version[collection] = dataFactoryVersion
	var ids []string
	for i := range 12 {
		id := fmt.Sprintf("%s/pipelines/p%02d", factory, i)
		raw := map[string]any{"id": id, "name": last(id), "type": dataFactoryPipelineType, "etag": fmt.Sprint("etag", i), "properties": map[string]any{"activities": []any{}}}
		s.add(raw, dataFactoryVersion)
		s.lists[collection] = append(s.lists[collection], raw)
		ids = append(ids, id)
	}
	c, err := s.runtime(t).resolve(t.Context(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	parent := dataFactoryMember{id: factory, kind: dataFactoryType}
	calls, peak := countCalls(s, func(path string) bool { return strings.Contains(path, "/pipelines/") }, 5*time.Millisecond)
	children, err := c.dataFactoryIndex(t.Context(), dataFactoryPipelineType, parent)
	if err != nil || len(children) != len(ids) {
		t.Fatal("pipelines changed", len(children), err)
	}
	for _, id := range ids {
		if calls["GET "+id] != 1 || children[id].parent != factory || text(children[id].raw["name"]) != last(id) {
			t.Fatal("pipeline not read exactly once", id, calls["GET "+id])
		}
	}
	if peak.peak < 2 || peak.peak > detailReadConcurrency {
		t.Fatal("pipeline reads were not concurrent within the bound", peak.peak)
	}
	s.handle = sweep2Deny(map[string]string{ids[3]: "FirstDenied", ids[9]: "LaterDenied"})
	for range 5 {
		if _, err := c.dataFactoryIndex(t.Context(), dataFactoryPipelineType, parent); err == nil || !strings.Contains(err.Error(), "FirstDenied") {
			t.Fatal("first failure in list order lost", err)
		}
	}
	s.handle = nil
	s.lists[collection][5] = s.lists[collection][4] // A duplicate row fails after the rows before it.
	clear(calls)
	if _, err := c.dataFactoryIndex(t.Context(), dataFactoryPipelineType, parent); err == nil || !strings.Contains(err.Error(), "invalid_datafactory_index_identity") {
		t.Fatal("duplicate row accepted", err)
	}
	for i, id := range ids {
		if want := map[bool]int{true: 1, false: 0}[i < 5]; calls["GET "+id] != want {
			t.Fatal("wrong reads around the invalid row", id, calls["GET "+id])
		}
	}
}
