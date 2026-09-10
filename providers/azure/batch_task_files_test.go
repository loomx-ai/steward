package azure

import (
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func TestBatchNativeFileOperationBinding(t *testing.T) {
	data, err := providerData()
	if err != nil {
		t.Fatal(err)
	}
	op, ok := data.catalog.Operation(batchDataPrefix + "Nodes_GetNodeFileProperties")
	if !ok || op.Call.Method != "HEAD" || op.Destructive {
		t.Fatal("missing native file HEAD")
	}
	payload, err := os.ReadFile("fixtures/batch/Nodes_GetNodeFileProperties.json")
	var example map[string]any
	if err != nil || json.Unmarshal(payload, &example) != nil {
		t.Fatal(err)
	}
	params := object(example["parameters"])
	// The pinned example still calls its host batchUrl. The selected 2025
	// Swagger names the parameter endpoint; preserve the original fixture.
	delete(params, "batchUrl")
	params["endpoint"] = "https://account.region.batch.azure.com"
	bound, err := bindAzureREST(op, params)
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(bound.URL)
	if err := batchFileURL(u); err != nil || u.Path != "/pools/poolId/nodes/nodeId/files/workitems/jobId/job-1/task1/wd/testFile.txt" {
		t.Fatal("native Windows file path binding", bound.URL, err)
	}
	for _, path := range []string{"/workitems/job/task/0", "workitems/job/路径/1", "workitems/job/some file"} {
		params["filePath"] = path
		bound, err := bindAzureREST(op, params)
		if err != nil {
			t.Fatal(err)
		}
		u, _ := url.Parse(bound.URL)
		if err := batchFileURL(u); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{"/", "//root", "x//y", "../private", "x/../private", "x/%2e%2e/private", "x/.", "x\nInjected"} {
		params["filePath"] = path
		if _, err := bindAzureREST(op, params); err == nil {
			t.Fatal("unsafe native file parameter", path)
		}
	}
	ordinary, _ := data.catalog.Operation(batchDataPrefix + "Nodes_GetNode")
	if _, err := bindAzureREST(ordinary, map[string]any{"endpoint": params["endpoint"], "poolId": "pool", "nodeId": "node/files/other"}); err == nil {
		t.Fatal("file-path support weakened ordinary node identities")
	}
}

func batchMPI(t *testing.T, s *batchScenario, r *Runtime, assets []asset.Asset) []asset.Asset {
	t.Helper()
	task := s.records["/jobs/jobid/tasks/taskid"]
	task["multiInstanceSettings"] = map[string]any{"numberOfInstances": 3, "coordinationCommandLine": "MPI_PRIVATE_COMMAND"}
	task["state"] = "running"
	values := array(batchExampleBody(t, "Tasks_ListSubTasks")["value"])
	for i := 0; i < 3; i++ {
		name := []string{"nodeid", "second", "third"}[i]
		if i > 0 {
			node := batchClone(s.records["/pools/poolid/nodes/nodeid"])
			node["id"], node["url"] = name, s.origin+"/pools/poolid/nodes/"+name
			s.records["/pools/poolid/nodes/"+name] = node
			s.lists["/pools/poolid/nodes"] = append(s.lists["/pools/poolid/nodes"], node)
		}
		directory := "/workitems/jobid/job-1/taskid/" + []string{"0", "1", "2"}[i]
		info := map[string]any{"nodeUrl": s.origin + "/pools/poolid/nodes/" + name, "poolId": "poolid", "nodeId": name, "taskRootDirectory": strings.ReplaceAll(directory, "/", "\\"), "taskRootDirectoryUrl": s.origin + "/pools/poolid/nodes/" + name + "/files/" + directory}
		if i == 0 {
			task["nodeInfo"] = info
		} else {
			object(values[i-1])["nodeInfo"] = info // Correct the native example's mpiPool/poolId mismatch in a copy.
		}
	}
	s.lists["/jobs/jobid/tasks/taskid/subtasksinfo"] = values
	c, _ := r.resolve(t.Context(), "connection")
	account, _ := c.batchAccount(t.Context(), s.account)
	for path, raw := range s.records {
		_, kind, _, _, err := batchDataIdentity(s.origin + path)
		if err != nil {
			continue
		}
		item, err := r.batchDataItem(t.Context(), c, account, kind, raw, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		value := asset.Asset{ID: asset.AssetID(item.NativeID), Identity: asset.Identity{Provider: asset.ProviderAzure, ConnectionID: "connection", Partition: "azure", NativeID: item.NativeID, NativeType: kind}, Location: item.Location, Normalized: item.Normalized, Capabilities: asset.CapabilitySet{asset.CapabilityActionable}}
		index := slices.IndexFunc(assets, func(a asset.Asset) bool { return a.ID == value.ID })
		if index < 0 {
			assets = append(assets, value)
		} else {
			assets[index] = value
		}
	}
	return assets
}

func TestBatchMultiInstanceTaskCleanupResumesAndWaitsForEveryDirectory(t *testing.T) {
	for _, kind := range []string{batchTaskType, batchJobType, batchScheduleType} {
		t.Run(kind, func(t *testing.T) {
			s, r, assets := newBatchScenario(t)
			assets = batchMPI(t, s, r, assets)
			if kind == batchScheduleType {
				s.lists["/jobschedules/schedule/jobs"] = []any{s.records["/jobs/jobid"]}
				s.records["/jobschedules/schedule"]["state"] = "active"
			}
			target := cdnAsset(t, assets, kind)
			solved := batchPlan(t, r, assets, target)
			request := servicePlanRequest(solved, assets, target)
			task := s.records["/jobs/jobid/tasks/taskid"]
			terminations, disables, heads := 0, 0, map[string]int{}
			filesGone := map[string]bool{}
			s.handle = func(req *http.Request) (*http.Response, bool) {
				if req.Method == "POST" && req.URL.Path == "/jobschedules/schedule/disable" {
					disables++
					s.records["/jobschedules/schedule"]["state"] = "disabled"
					s.records["/jobschedules/schedule"]["eTag"] = "disabled-tag"
					return &http.Response{StatusCode: 204, Header: http.Header{}, Body: http.NoBody}, true
				}
				if req.Method == "POST" && req.URL.Path == "/jobs/jobid/tasks/taskid/terminate" {
					terminations++
					if req.Header.Get("If-Match") != task["eTag"] || req.Header.Get("client-request-id") == "" {
						t.Fatal("task termination lost its native condition")
					}
					task["state"], task["eTag"] = "completed", "terminated-tag"
					return &http.Response{StatusCode: 204, Header: http.Header{}, Body: http.NoBody}, true
				}
				if req.Method == "HEAD" {
					if !strings.Contains(req.URL.EscapedPath(), "%2Fworkitems%2F") || req.URL.Query().Get("api-version") != batchVersion {
						t.Fatal("directory path was not bound as the native file parameter")
					}
					heads[req.URL.Path]++
					status := 200
					if filesGone[req.URL.Path] {
						status = 404
					}
					return &http.Response{StatusCode: status, Header: http.Header{"Ocp-Batch-File-Isdirectory": {"true"}}, Body: http.NoBody}, true
				}
				return nil, false
			}
			driver, _ := r.ResolveAction(t.Context(), "connection", target)
			receipt, err := driver.Execute(t.Context(), request)
			if err != nil || terminations != 1 || len(s.writes) != 0 || receipt.Data["batch_task_phase"] != "terminating" {
				t.Fatal("MPI primary termination", terminations, receipt.Data["batch_task_phase"], err)
			}
			if kind == batchScheduleType && disables != 1 {
				t.Fatal("schedule was not disabled before MPI cleanup")
			}
			resume := func() contracts.WaitResult {
				t.Helper()
				payload, _ := json.Marshal(receipt)
				json.Unmarshal(payload, &receipt)
				driver, err = r.ResolveAction(t.Context(), "connection", target)
				if err != nil {
					t.Fatal(err)
				}
				waiting, err := driver.Wait(t.Context(), request, receipt)
				if err != nil {
					t.Fatal("MPI resumed wait", err)
				}
				receipt.Data = waiting.Data
				return waiting
			}
			if wait := resume(); wait.Done || len(s.writes) != 0 || terminations != 1 {
				t.Fatal("primary completion concealed a running subtask")
			}
			for _, subtask := range s.lists["/jobs/jobid/tasks/taskid/subtasksinfo"] {
				object(subtask)["state"] = "completed"
			}
			if wait := resume(); wait.Done || len(s.writes) != 1 || receipt.Data["batch_task_phase"] != "deleting" {
				t.Fatal("MPI DELETE did not follow subtask termination", s.writes, wait)
			}
			batchCascadeGone(s)
			if kind == batchScheduleType {
				s.gone["/jobs/jobid"], s.gone["/jobs/jobid/tasks/taskid"] = true, true
			}
			if wait := resume(); wait.Done || wait.State != "batch_subtask_files_deleting" {
				t.Fatal("primary absence concealed remaining files", wait)
			}
			for _, info := range []map[string]any{object(task["nodeInfo"]), object(object(s.lists["/jobs/jobid/tasks/taskid/subtasksinfo"][0])["nodeInfo"]), object(object(s.lists["/jobs/jobid/tasks/taskid/subtasksinfo"][1])["nodeInfo"])} {
				u, _ := url.Parse(text(info["taskRootDirectoryUrl"]))
				filesGone[u.Path] = true
			}
			if wait := resume(); !wait.Done || len(heads) != 3 {
				t.Fatal("not all MPI directories were independently verified", len(heads), wait)
			}
			request.ExecutionResult = &receipt
			before := len(s.writes)
			if _, err := driver.Execute(t.Context(), request); err != nil || len(s.writes) != before {
				t.Fatal("MPI execution retry repeated deletion", err)
			}
		})
	}
}

func TestBatchMultiInstanceBoundaries(t *testing.T) {
	for _, mode := range []string{"duplicate", "duplicate_node", "missing_subtask", "foreign_node", "wrong_pool", "directory_url", "traversal", "encoded_traversal", "unknown_state", "bad_count", "missing_primary", "changed_during_walk", "bad_termination", "changed_task_before_delete"} {
		t.Run(mode, func(t *testing.T) {
			s, r, assets := newBatchScenario(t)
			assets = batchMPI(t, s, r, assets)
			target := cdnAsset(t, assets, batchTaskType)
			subtasks := s.lists["/jobs/jobid/tasks/taskid/subtasksinfo"]
			info := object(object(subtasks[0])["nodeInfo"])
			switch mode {
			case "duplicate":
				object(subtasks[1])["id"] = 1
			case "duplicate_node":
				object(subtasks[1])["nodeInfo"] = info
			case "missing_subtask":
				s.lists["/jobs/jobid/tasks/taskid/subtasksinfo"] = subtasks[:1]
			case "foreign_node":
				info["nodeUrl"] = "https://foreign.japaneast.batch.azure.com/pools/poolid/nodes/second"
			case "wrong_pool":
				info["poolId"] = "different"
			case "directory_url":
				info["taskRootDirectoryUrl"] = text(info["taskRootDirectoryUrl"]) + "/different"
			case "traversal", "encoded_traversal":
				value := "/workitems/../shared"
				if mode == "encoded_traversal" {
					value = "/workitems/%2e%2e/shared"
				}
				info["taskRootDirectory"], info["taskRootDirectoryUrl"] = value, text(info["nodeUrl"])+"/files/"+value
			case "unknown_state":
				object(subtasks[1])["state"] = "futureState"
			case "bad_count":
				object(s.records["/jobs/jobid/tasks/taskid"]["multiInstanceSettings"])["numberOfInstances"] = 0
			case "missing_primary":
				delete(s.records["/jobs/jobid/tasks/taskid"], "nodeInfo")
			case "changed_during_walk":
				reads := 0
				s.handle = func(req *http.Request) (*http.Response, bool) {
					if strings.HasSuffix(req.URL.Path, "/subtasksinfo") {
						reads++
						if reads == 2 {
							info["taskRootDirectory"] = "/workitems/moved"
							info["taskRootDirectoryUrl"] = text(info["nodeUrl"]) + "/files//workitems/moved"
						}
					}
					return nil, false
				}
			case "bad_termination":
				s.handle = func(req *http.Request) (*http.Response, bool) {
					if strings.HasSuffix(req.URL.Path, "/terminate") {
						return jsonResponse(202, nil, nil), true
					}
					return nil, false
				}
			case "changed_task_before_delete":
				s.records["/jobs/jobid/tasks/taskid"]["state"] = "completed"
				object(subtasks[1])["state"] = "completed"
				reads := 0
				s.handle = func(req *http.Request) (*http.Response, bool) {
					if strings.HasSuffix(req.URL.Path, "/subtasksinfo") {
						reads++
						if reads == 4 {
							s.records["/jobs/jobid/tasks/taskid"]["commandLine"] = "changed"
						}
					}
					return nil, false
				}
			}
			driver, _ := r.ResolveAction(t.Context(), "connection", target)
			if _, err := driver.Execute(t.Context(), contracts.ActionRequest{Asset: target, Action: "delete"}); err == nil || len(s.writes) != 0 {
				t.Fatal("MPI boundary accepted", mode, err)
			}
		})
	}
}

func TestBatchMultiInstanceReadbackRequiresBoundFiles(t *testing.T) {
	for _, mode := range []string{"tampered_path", "removed_files", "tampered_phase", "node_recreated", "head_403", "head_partial", "head_file", "no_receipt"} {
		t.Run(mode, func(t *testing.T) {
			s, r, assets := newBatchScenario(t)
			assets = batchMPI(t, s, r, assets)
			s.records["/jobs/jobid/tasks/taskid"]["state"] = "completed"
			object(s.lists["/jobs/jobid/tasks/taskid/subtasksinfo"][1])["state"] = "completed"
			target := cdnAsset(t, assets, batchTaskType)
			request := contracts.ActionRequest{Asset: target, Action: "delete"}
			driver, _ := r.ResolveAction(t.Context(), "connection", target)
			receipt, err := driver.Execute(t.Context(), request)
			if err != nil {
				t.Fatal(err)
			}
			payload, _ := json.Marshal(receipt)
			json.Unmarshal(payload, &receipt)
			headCalls := 0
			s.handle = func(req *http.Request) (*http.Response, bool) {
				if req.Method != "HEAD" {
					return nil, false
				}
				headCalls++
				status, directory := 404, "true"
				if mode == "head_403" {
					status = 403
				}
				if mode == "head_partial" {
					status = 206
				}
				if mode == "head_file" {
					status, directory = 200, "false"
				}
				return &http.Response{StatusCode: status, Header: http.Header{"Ocp-Batch-File-Isdirectory": {directory}}, Body: http.NoBody}, true
			}
			switch mode {
			case "tampered_path":
				object(array(receipt.Data["batch_task_files"])[0])["directory"] = "/shared"
			case "removed_files":
				receipt.Data["batch_task_files"] = []any{}
			case "tampered_phase":
				receipt.Data["batch_task_phase"] = "terminating"
			case "node_recreated":
				s.records["/pools/poolid/nodes/nodeid"]["allocationTime"] = "2026-09-10T12:00:00Z"
			}
			if mode == "no_receipt" {
				if _, err := driver.Readback(t.Context(), request); err == nil {
					t.Fatal("MPI primary absence accepted without file receipt")
				}
			} else if wait, err := driver.Wait(t.Context(), request, receipt); err == nil || wait.Done {
				t.Fatal("invalid MPI cleanup receipt or file response accepted", mode, wait, err)
			}
			if strings.HasPrefix(mode, "tampered") || mode == "removed_files" || mode == "no_receipt" {
				if headCalls != 0 {
					t.Fatal("untrusted receipt used node-file authority")
				}
			}
		})
	}
}
