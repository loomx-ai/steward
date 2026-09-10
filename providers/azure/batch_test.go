package azure

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func batchExampleBody(t *testing.T, name string) map[string]any {
	t.Helper()
	payload, err := os.ReadFile("fixtures/batch/" + name + ".json")
	var value map[string]any
	if err != nil || json.Unmarshal(payload, &value) != nil {
		t.Fatal("invalid Batch example", name, err)
	}
	return object(object(object(value["responses"])["200"])["body"])
}

type batchScenario struct {
	arm             *dnsScenario
	account, origin string
	records         map[string]map[string]any
	lists           map[string][]any
	gone            map[string]bool
	writes          []string
	dataCalls       int
	handle          func(*http.Request) (*http.Response, bool)
}

// The original examples come from unrelated accounts. Compose copies into one
// account, fixing the documented Task id/URL and ARM pool package-id mismatches.
// Their unmodified originals remain available for schema/provenance tests.
func newBatchScenario(t *testing.T) (*batchScenario, *Runtime, []asset.Asset) {
	t.Helper()
	s := &batchScenario{arm: newDNSScenario(), records: map[string]map[string]any{}, lists: map[string][]any{}, gone: map[string]bool{}}
	root := "/subscriptions/" + testSubscription
	group := root + "/resourcegroups/batch-rg"
	s.account = group + "/providers/microsoft.batch/batchaccounts/sampleacct"
	s.origin = "https://sampleacct.japaneast.batch.azure.com"
	s.arm.lists[root+"/resourcegroups"] = []any{map[string]any{"id": group, "type": groupType}}
	// Supporting indexes for external references in the composed examples.
	s.arm.lists[root+"/providers/microsoft.storage/storageaccounts"] = []any{}
	s.arm.version[root+"/providers/microsoft.storage/storageaccounts"] = "2023-05-01"
	s.arm.lists[root+"/providers/microsoft.keyvault/vaults"] = []any{}
	s.arm.version[root+"/providers/microsoft.keyvault/vaults"] = "2023-07-01"
	var raws []map[string]any
	addARM := func(file, id, kind string) map[string]any {
		raw := batchExampleBody(t, file)
		raw["id"], raw["type"], raw["name"] = id, kind, last(id)
		s.arm.add(raw, batchVersion)
		collection := redisParentID(id) + "/" + strings.ToLower(last(kind))
		if kind == batchAccountType {
			collection = root + "/providers/microsoft.batch/batchaccounts"
		}
		s.arm.lists[collection] = append(s.arm.lists[collection], raw)
		s.arm.version[collection] = batchVersion
		raws = append(raws, raw)
		return raw
	}
	account := addARM("BatchAccountGet", s.account, batchAccountType)
	object(account["properties"])["autoStorage"] = map[string]any{"storageAccountId": group + "/providers/microsoft.storage/storageaccounts/batchstorage"}
	app := addARM("ApplicationGet", s.account+"/applications/app1", batchApplicationType)
	object(app["properties"])["defaultVersion"] = "1"
	addARM("ApplicationPackageGet", s.account+"/applications/app1/versions/1", batchPackageType)
	pool := addARM("PoolGet", s.account+"/pools/poolid", batchPoolType)
	object(pool["properties"])["applicationPackages"] = []any{map[string]any{"id": s.account + "/applications/app1", "version": "1"}}
	addARM("PrivateEndpointConnectionGet", s.account+"/privateendpointconnections/connection", batchPECType)
	addARM("NspConfigurationGet", s.account+"/networksecurityperimeterconfigurations/configuration", batchPerimeterType)
	addData := func(file, path string) map[string]any {
		raw := batchExampleBody(t, file)
		raw["id"], raw["url"] = last(path), s.origin+path
		s.records[path] = raw
		collection := path[:strings.LastIndex(path, "/")]
		s.lists[collection] = append(s.lists[collection], raw)
		return raw
	}
	job := addData("Jobs_GetJob", "/jobs/jobid")
	object(job["poolInfo"])["poolId"] = "poolid"
	object(job["executionInfo"])["poolId"] = "poolid"
	schedule := addData("JobSchedules_GetJobSchedule", "/jobschedules/schedule")
	object(object(schedule["jobSpecification"])["poolInfo"])["poolId"] = "poolid"
	delete(schedule, "executionInfo")
	s.lists["/jobschedules/schedule/jobs"] = []any{}
	task := addData("Tasks_GetTask", "/jobs/jobid/tasks/taskid")
	delete(task, "multiInstanceSettings")
	addData("Nodes_GetNode", "/pools/poolid/nodes/nodeid")
	s.lists["/jobs/jobid/tasks/taskid/subtasksinfo"] = []any{}
	dataPool := batchExampleBody(t, "Pools_GetPool_Basic")
	dataPool["id"], dataPool["url"] = "poolid", s.origin+"/pools/poolid"
	s.records["/pools/poolid"] = dataPool
	s.lists["/pools"] = []any{dataPool}
	s.arm.handle = func(req *http.Request) (*http.Response, bool) {
		if s.handle != nil {
			if response, handled := s.handle(req); handled {
				return response, true
			}
		}
		if req.URL.Scheme+"://"+req.URL.Host != s.origin {
			return nil, false
		}
		s.dataCalls++
		if req.URL.Query().Get("api-version") != batchVersion || req.Header.Get("Ocp-Date") == "" {
			t.Fatal("Batch data request lost its version/date", req.URL)
		}
		path := strings.ToLower(req.URL.Path)
		if req.Method != "GET" {
			s.writes = append(s.writes, req.Method+" "+path)
			s.gone[path] = true
			status := 202
			if strings.Contains(path, "/tasks/") {
				status = 200
			}
			return jsonResponse(status, nil, http.Header{"Request-Id": {"batch-delete"}}), true
		}
		if s.gone[path] {
			return jsonResponse(404, map[string]any{"code": "ResourceNotFound"}, nil), true
		}
		if raw := s.records[path]; raw != nil {
			headers := http.Header{"Request-Id": {"batch-get"}}
			if etag := text(raw["eTag"]); etag != "" {
				headers.Set("ETag", etag)
			}
			return jsonResponse(200, raw, headers), true
		}
		if records, ok := s.lists[path]; ok {
			values := []any{}
			for _, raw := range records {
				id := strings.TrimPrefix(strings.ToLower(text(object(raw)["url"])), s.origin)
				if !s.gone[id] {
					values = append(values, raw)
				}
			}
			return jsonResponse(200, map[string]any{"value": values}, http.Header{"Request-Id": {"batch-list"}}), true
		}
		t.Fatal("unexpected Batch data operation", req.URL)
		return nil, false
	}
	r := s.arm.runtime(t)
	var assets []asset.Asset
	for _, raw := range raws {
		value := dnsAsset(t, r, raw)
		if value.Identity.NativeType == batchPerimeterType {
			value.Capabilities = nil
		}
		assets = append(assets, value)
	}
	c, err := r.resolve(context.Background(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	context, err := c.batchAccount(context.Background(), s.account)
	if err != nil {
		t.Fatal(err)
	}
	for path, raw := range s.records {
		_, kind, _, _, err := batchDataIdentity(s.origin + path)
		if err != nil {
			continue // The pool's asset has its real ARM ID.
		}
		item, err := r.batchDataItem(t.Context(), c, context, kind, raw, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		assets = append(assets, asset.Asset{ID: asset.AssetID(item.NativeID), Identity: asset.Identity{Provider: asset.ProviderAzure, ConnectionID: "connection", Partition: "azure", NativeID: item.NativeID, NativeType: kind}, Location: item.Location, Normalized: item.Normalized, Capabilities: asset.CapabilitySet{asset.CapabilityActionable}})
	}
	return s, r, assets
}

func TestBatchNativeInventory(t *testing.T) {
	for _, kind := range []string{batchAccountType, batchPoolType, batchApplicationType, batchPackageType, batchPECType, batchPerimeterType, batchScheduleType, batchJobType, batchTaskType, batchNodeType} {
		t.Run(kind, func(t *testing.T) {
			s, r, assets := newBatchScenario(t)
			request := productRequest(r, kind)
			page, err := r.List(t.Context(), request)
			if err != nil || !page.Complete || len(page.Items) != 1 {
				t.Fatal("Batch inventory", kind, len(page.Items), err)
			}
			expected := cdnAsset(t, assets, kind)
			item := page.Items[0]
			if item.NativeID != expected.Identity.NativeID || item.Location != "japaneast" || item.Normalized["_batch_account"] != s.account || item.Normalized["_batch_account_binding"] == "" || item.Normalized["_batch_private_configuration"] == "" {
				t.Fatal("Batch inventory lost its account or incarnation", item.NativeID)
			}
			if isBatchDataType(kind) {
				if !strings.HasPrefix(item.NativeID, s.origin+"/") || page.RequestID != "batch-list" {
					t.Fatal("Batch native URL or request provenance lost", page.RequestID)
				}
				request.Source = ""
				if page, err := r.List(t.Context(), request); err != nil || len(page.Items) != 1 {
					t.Fatal("typed Batch data inventory did not use its native source", err)
				}
			}
			if kind == batchPerimeterType && (item.Actionable == nil || *item.Actionable) {
				t.Fatal("NSP configuration invented a delete")
			}
		})
	}
}

func TestBatchPrivateConfigurationAndRedaction(t *testing.T) {
	s, r, assets := newBatchScenario(t)
	task := s.records["/jobs/jobid/tasks/taskid"]
	task["commandLine"] = "DO_NOT_LOG_COMMAND"
	task["environmentSettings"] = []any{map[string]any{"name": "KEY", "value": "DO_NOT_LOG_ENV"}}
	task["resourceFiles"] = []any{map[string]any{"httpUrl": "https://data.blob.core.windows.net/container/file?sig=DO_NOT_LOG_SAS", "filePath": "input"}}
	page, err := r.List(t.Context(), productRequest(r, batchTaskType))
	if err != nil || len(page.Items) != 1 {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(page.Items[0])
	if strings.Contains(string(payload), "DO_NOT_LOG") || !strings.Contains(string(payload), "data.blob.core.windows.net/container/file") {
		t.Fatal("Batch diagnostics exposed private configuration or lost safe URL")
	}
	c, _ := r.resolve(t.Context(), "connection")
	if err := batchIncarnation(c, cdnAsset(t, assets, batchTaskType), task); err == nil {
		t.Fatal("redacted command change did not invalidate the plan")
	}
	if len(s.writes)+len(s.arm.deletes) != 0 {
		t.Fatal("inventory mutated Batch")
	}
}

func TestBatchNativeDataBoundaries(t *testing.T) {
	for _, mode := range []string{"foreign_origin", "wrong_kind", "wrong_task_id", "duplicate", "filtered_page", "cross_collection", "repeated_page", "malformed_page", "wrong_etag", "partial_success", "aad_disabled"} {
		t.Run(mode, func(t *testing.T) {
			s, r, _ := newBatchScenario(t)
			task := s.records["/jobs/jobid/tasks/taskid"]
			switch mode {
			case "foreign_origin":
				task["url"] = "https://foreign.japaneast.batch.azure.com/jobs/jobid/tasks/taskid"
			case "wrong_kind":
				task["url"] = s.origin + "/jobschedules/taskid"
			case "wrong_task_id":
				task["id"] = "testTask" // The known unmodified native example mismatch.
			case "duplicate":
				s.lists["/jobs/jobid/tasks"] = append(s.lists["/jobs/jobid/tasks"], task)
			case "aad_disabled":
				object(s.arm.records[s.account]["properties"])["allowedAuthenticationModes"] = []any{"SharedKey"}
			default:
				s.handle = func(req *http.Request) (*http.Response, bool) {
					if req.URL.Host != strings.TrimPrefix(s.origin, "https://") {
						return nil, false
					}
					if mode == "wrong_etag" && req.URL.Path == "/jobs/jobid/tasks/taskid" {
						return jsonResponse(200, task, http.Header{"Etag": {"other"}}), true
					}
					if req.URL.Path != "/jobs/jobid/tasks" {
						return nil, false
					}
					body := map[string]any{"value": []any{task}}
					switch mode {
					case "filtered_page":
						body["odata.nextLink"] = s.origin + "/jobs/jobid/tasks?api-version=" + batchVersion + "&$filter=state%20eq%20'completed'"
					case "cross_collection":
						body["odata.nextLink"] = s.origin + "/jobs/other/tasks?api-version=" + batchVersion
					case "repeated_page":
						body["odata.nextLink"] = req.URL.String()
					case "malformed_page":
						body["value"] = nil
					case "partial_success":
						return jsonResponse(206, body, nil), true
					}
					return jsonResponse(200, body, nil), true
				}
			}
			if _, err := r.List(t.Context(), productRequest(r, batchTaskType)); err == nil {
				t.Fatal("incomplete/foreign Batch inventory succeeded", mode)
			}
			if len(s.writes)+len(s.arm.deletes) != 0 {
				t.Fatal("Batch inventory boundary mutated resources")
			}
		})
	}
}

func TestBatchDataCursorAndNativeContinuation(t *testing.T) {
	s, r, _ := newBatchScenario(t)
	first := s.records["/jobs/jobid/tasks/taskid"]
	second := batchClone(first)
	second["id"], second["url"] = "tasktwo", s.origin+"/jobs/jobid/tasks/tasktwo"
	s.records["/jobs/jobid/tasks/tasktwo"] = second
	s.handle = func(req *http.Request) (*http.Response, bool) {
		if req.URL.Scheme+"://"+req.URL.Host == s.origin && req.URL.Path == "/jobs/jobid/tasks" {
			body := map[string]any{"value": []any{second}}
			if req.URL.Query().Get("$skiptoken") == "" {
				body = map[string]any{"value": []any{first}, "odata.nextLink": s.origin + "/jobs/jobid/tasks?api-version=" + batchVersion + "&$skiptoken=page2"}
			}
			return jsonResponse(200, body, http.Header{"Request-Id": {"batch-page"}}), true
		}
		return nil, false
	}
	request := productRequest(r, batchTaskType)
	request.Limit = 1
	page, err := r.List(t.Context(), request)
	if err != nil || page.Complete || len(page.Items) != 1 || page.NextCursor == "" || page.RequestID != "batch-page" {
		t.Fatal("Batch native continuation", page.Complete, err)
	}
	request.Cursor = page.NextCursor
	last, err := r.List(t.Context(), request)
	if err != nil || !last.Complete || len(last.Items) != 1 || last.Items[0].NativeID == page.Items[0].NativeID {
		t.Fatal("Batch client continuation lost a task", err)
	}
	second["commandLine"] = "changed"
	if _, err := r.List(t.Context(), request); err == nil {
		t.Fatal("Batch cursor survived a private configuration change")
	}
}

func TestBatchInvocationResolvesAccountBeforeDataOAuth(t *testing.T) {
	for _, endpoint := range []string{"https://sampleacct.japaneast.batch.azure.com", "https://foreign.japaneast.batch.azure.com", "https://sampleacct.japaneast.batch.azure.com:443", "https://other.example.com"} {
		t.Run(endpoint, func(t *testing.T) {
			s, r, _ := newBatchScenario(t)
			before := s.dataCalls
			result, err := r.Invoke(t.Context(), contracts.Invocation{ConnectionID: "connection", Operation: batchDataPrefix + "Jobs_ListJobs", Parameters: map[string]any{"endpoint": endpoint}})
			if endpoint != s.origin {
				if err == nil || s.dataCalls != before {
					t.Fatal("unowned Batch endpoint reached the data transport", err)
				}
			} else if err != nil || len(array(result.Data["value"])) != 1 || result.RequestID != "batch-list" {
				t.Fatal("native Batch invocation", err)
			}
		})
	}
}

func TestBatchDataIdentityKeepsNativeURLs(t *testing.T) {
	for _, valid := range []string{"https://account.region.batch.azure.com/jobs/Job", "https://account.region.batch.azure.com/jobs/schedule:job-1", "https://account.region.batch.azure.com/jobs/JOB/tasks/TASK", "https://account.region.batch.azure.com/pools/pool/nodes/node_1"} {
		id, kind, _, _, err := batchDataIdentity(valid)
		if err != nil || id != strings.ToLower(valid) || !isBatchDataType(kind) {
			t.Fatal("native Batch identity", valid, err)
		}
	}
	for _, invalid := range []string{"https://account.region.batch.azure.com/jobs/job?sig=secret", "https://account.region.batch.azure.com/jobs/%2Fjob", "https://user@account.region.batch.azure.com/jobs/job", "https://account.region.batch.azure.com:443/jobs/job", "https://account.region.batch.azure.com/jobs/job/", "https://account.region.batch.azure.com/prefix/jobs/job", "https://account.region.batch.azure.com/jobs/../jobs/job"} {
		if _, _, _, _, err := batchDataIdentity(invalid); err == nil {
			t.Fatal("unsafe Batch identity accepted", invalid)
		}
	}
	if !slices.Contains([]string{batchTaskType, batchJobType}, batchKind(strings.ToLower(batchTaskType))) {
		t.Fatal("case-insensitive native kind lookup failed")
	}
}
