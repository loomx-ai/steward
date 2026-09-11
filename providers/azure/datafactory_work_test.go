package azure

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

func dataFactoryWorkFixture(t *testing.T, f *dataFactoryFixture) (map[string]any, map[string]any) {
	t.Helper()
	run := dataFactoryBody(t, "PipelineRuns_Get")
	run["pipelineName"] = last(f.ids[dataFactoryPipelineType])
	run["runStart"], run["lastUpdated"], run["status"] = "2026-09-01T00:00:00Z", "2026-09-01T00:00:01Z", "InProgress"
	delete(run, "runEnd")
	delete(run, "durationInMs")
	run["futurePrivateWork"] = "sensitive-work-definition"
	debug := object(array(dataFactoryBody(t, "DataFlowDebugSession_QueryByFactory")["value"])[0])
	debug["futurePrivateDebug"] = "sensitive-debug-definition"
	return run, debug
}

func TestDataFactoryNativeWorkCensus(t *testing.T) {
	f := newDataFactoryFixture(t)
	run, debug := dataFactoryWorkFixture(t, f)
	root := f.ids[dataFactoryType]
	queryCount, debugPost, debugGet, runGet := 0, 0, 0, 0
	var before string
	f.override = func(req *http.Request) (*http.Response, bool) {
		path := strings.ToLower(req.URL.Path)
		switch path {
		case root + "/querypipelineruns":
			if req.Method != "POST" || len(req.URL.Query()) != 1 {
				t.Fatal("run query must use its native POST body", req.Method, req.URL)
			}
			var body map[string]any
			if json.NewDecoder(req.Body).Decode(&body) != nil || len(body) < 2 || body["filters"] != nil || body["orderBy"] != nil || body["lastUpdatedAfter"] != object(f.resources[root]["properties"])["createTime"] {
				t.Fatal("filtered or truncated work window", body)
			}
			queryCount++
			if queryCount%2 == 1 {
				before = text(body["lastUpdatedBefore"])
				if stamp, err := time.Parse(time.RFC3339Nano, before); err != nil || time.Since(stamp) > time.Minute || len(body) != 2 {
					t.Fatal("query time not bound to this census", body)
				}
				return jsonResponse(200, map[string]any{"value": []any{run}, "continuationToken": "opaque +/&=token"}, nil), true
			}
			if body["continuationToken"] != "opaque +/&=token" || body["lastUpdatedBefore"] != before || len(body) != 3 {
				t.Fatal("run continuation or window changed", body)
			}
			return jsonResponse(200, map[string]any{"value": []any{}, "continuationToken": nil}, nil), true
		case root + "/pipelineruns/" + text(run["runId"]):
			runGet++
			if req.Method != "GET" {
				t.Fatal("census mutated work")
			}
			return jsonResponse(200, run, nil), true
		case root + "/querydataflowdebugsessions":
			if req.Method == "POST" {
				debugPost++
				return jsonResponse(200, map[string]any{"value": []any{debug}, "nextLink": apiURL(root+"/queryDataFlowDebugSessions", dataFactoryVersion) + "&$skiptoken=opaque%2Btoken"}, nil), true
			}
			debugGet++
			if req.Method != "GET" || req.URL.Query().Get("$skiptoken") != "opaque+token" {
				t.Fatal("debug continuation must use native GET", req.Method, req.URL)
			}
			return jsonResponse(200, map[string]any{"value": []any{}}, nil), true
		}
		return nil, false
	}
	batch, err := f.runtime.List(t.Context(), f.request(dataFactoryType))
	if err != nil || len(batch.Items) != 1 {
		t.Fatal("work census failed", err)
	}
	work := object(batch.Items[0].Normalized["_datafactory_work"])
	if work["verified"] != true || len(object(work["runs"])) != 1 || len(object(work["debug"])) != 1 || queryCount != 4 || runGet != 2 || debugPost != 2 || debugGet != 2 {
		t.Fatal("work census lost pages or named reads", work, queryCount, runGet, debugPost, debugGet)
	}
	payload, _ := json.Marshal(batch)
	for _, secret := range []string{"sensitive-work-definition", "sensitive-debug-definition", "OutputBlobNameList", "userObjectId"} {
		if strings.Contains(string(payload), secret) {
			t.Fatal("private work details escaped inventory", secret)
		}
	}
}

func TestDataFactoryKnownWorkRequiresNamedStateAndSameIncarnation(t *testing.T) {
	for _, mode := range []string{"omitted-active", "completed", "canceling", "gone", "forbidden", "replaced", "listed-replaced", "debug-replaced", "debug-expired", "liveness"} {
		t.Run(mode, func(t *testing.T) {
			f := newDataFactoryFixture(t)
			run, debug := dataFactoryWorkFixture(t, f)
			root := f.ids[dataFactoryType]
			refresh := false
			runReads := 0
			f.override = func(req *http.Request) (*http.Response, bool) {
				switch strings.ToLower(req.URL.Path) {
				case root + "/querypipelineruns":
					rows := []any{run}
					if refresh && mode != "listed-replaced" {
						rows = []any{}
					}
					return jsonResponse(200, map[string]any{"value": rows}, nil), true
				case root + "/pipelineruns/" + text(run["runId"]):
					runReads++
					if refresh && (mode == "gone" || mode == "forbidden") {
						code := 404
						if mode == "forbidden" {
							code = 403
						}
						return jsonResponse(code, map[string]any{"error": map[string]any{"code": "NotAvailable"}}, nil), true
					}
					return jsonResponse(200, run, nil), true
				case root + "/querydataflowdebugsessions":
					rows := []any{debug}
					if refresh && mode == "debug-expired" {
						rows = []any{}
					}
					return jsonResponse(200, map[string]any{"value": rows}, nil), true
				}
				return nil, false
			}
			request := f.request(dataFactoryPipelineType)
			batch, err := f.runtime.List(t.Context(), request)
			if err != nil || len(batch.Items) != 1 {
				t.Fatal(err)
			}
			item := batch.Items[0]
			request.KnownNativeIDs, request.KnownNativeMetadata = []string{item.NativeID}, map[string]map[string]any{item.NativeID: item.Normalized}
			refresh = true
			switch mode {
			case "completed":
				run["status"], run["runEnd"] = "Succeeded", "2026-09-01T00:01:00Z"
			case "canceling":
				run["status"] = "Canceling"
			case "replaced", "listed-replaced":
				run["futurePrivateWork"] = "changed-definition"
			case "debug-replaced":
				debug["startTime"] = "2026-09-01T00:01:00Z"
			case "liveness":
				run["lastUpdated"], run["durationInMs"], debug["lastActivityTime"] = "2026-09-01T00:01:00Z", 300, "2026-09-01T00:01:00Z"
			}
			batch, err = f.runtime.List(t.Context(), request)
			fails := mode == "gone" || mode == "forbidden" || mode == "replaced" || mode == "listed-replaced" || mode == "debug-replaced"
			if fails {
				if err == nil {
					t.Fatal("uncertain or replaced work accepted", mode)
				}
				return
			}
			if err != nil || len(batch.Items) != 1 || runReads != 4 {
				t.Fatal("known run was not read independently of index", err, runReads)
			}
			work := object(batch.Items[0].Normalized["_datafactory_work"])
			if (len(object(work["runs"])) == 0) != (mode == "completed") || (len(object(work["debug"])) == 0) != (mode == "debug-expired") {
				t.Fatal("work state confused with absence", work)
			}
		})
	}
}

func TestDataFactoryRejectsUncertainWorkCensus(t *testing.T) {
	for _, mode := range []string{"missing-runs", "filtered-pagination", "token-number", "token-cycle", "duplicate-run", "run-forbidden", "run-gone", "run-other-id", "run-other-factory", "run-path-pipeline", "run-unknown-state", "run-no-creation", "run-list-drift", "debug-no-creation", "debug-duplicate", "debug-next-foreign", "debug-next-other-factory", "debug-next-version", "debug-next-filter", "debug-next-cycle", "debug-next-number", "debug-lro", "changed-between-scans"} {
		t.Run(mode, func(t *testing.T) {
			f := newDataFactoryFixture(t)
			run, debug := dataFactoryWorkFixture(t, f)
			root := f.ids[dataFactoryType]
			queryCalls := 0
			f.override = func(req *http.Request) (*http.Response, bool) {
				switch strings.ToLower(req.URL.Path) {
				case root + "/querypipelineruns":
					queryCalls++
					body := map[string]any{"value": []any{run}}
					switch mode {
					case "missing-runs":
						delete(body, "value")
					case "filtered-pagination":
						body["nextLink"] = apiURL(root+"/queryPipelineRuns", dataFactoryVersion)
					case "token-number":
						body["continuationToken"] = 2
					case "token-cycle":
						body["value"], body["continuationToken"] = []any{}, "same-token"
					case "duplicate-run":
						body["value"] = []any{run, run}
					case "run-unknown-state":
						run["status"] = "Unknown"
					case "run-no-creation":
						delete(run, "runStart")
					case "run-path-pipeline":
						run["pipelineName"] = "../pipelines/other"
					case "changed-between-scans":
						if queryCalls > 1 {
							run["futurePrivateWork"] = "new-definition"
						}
					}
					return jsonResponse(200, body, nil), true
				case root + "/pipelineruns/" + text(run["runId"]):
					if mode == "run-forbidden" || mode == "run-gone" {
						code := 403
						if mode == "run-gone" {
							code = 404
						}
						return jsonResponse(code, map[string]any{"error": map[string]any{"code": "NoAccess"}}, nil), true
					}
					live := batchClone(run)
					switch mode {
					case "run-other-id":
						live["runId"] = "2f7fdb90-5df1-4b8e-ac2f-064cfa582022"
					case "run-other-factory":
						live["id"] = strings.Replace(root, "/factory", "/foreign", 1) + "/pipelineruns/" + text(run["runId"])
					case "run-list-drift":
						live["pipelineName"] = "other-pipeline"
					}
					return jsonResponse(200, live, nil), true
				case root + "/querydataflowdebugsessions":
					body := map[string]any{"value": []any{debug}}
					next := apiURL(root+"/queryDataFlowDebugSessions", dataFactoryVersion)
					switch mode {
					case "debug-no-creation":
						delete(debug, "startTime")
					case "debug-duplicate":
						body["value"] = []any{debug, debug}
					case "debug-next-foreign":
						body["nextLink"] = strings.Replace(next, "management.azure.com", "foreign.invalid", 1)
					case "debug-next-other-factory":
						body["nextLink"] = strings.Replace(next, "/factory/", "/foreign/", 1)
					case "debug-next-version":
						body["nextLink"] = strings.Replace(next, dataFactoryVersion, "2026-01-01", 1)
					case "debug-next-filter":
						body["nextLink"] = next + "&$filter=mine"
					case "debug-next-cycle":
						body["value"], body["nextLink"] = []any{}, next
					case "debug-next-number":
						body["nextLink"] = 123
					case "debug-lro":
						return jsonResponse(202, body, nil), true
					}
					return jsonResponse(200, body, nil), true
				}
				return nil, false
			}
			if _, err := f.runtime.List(t.Context(), f.request(dataFactoryType)); err == nil {
				t.Fatal("uncertain native work census accepted", mode)
			}
		})
	}
}

func TestDataFactoryCancelEmptyStringHasNarrowNativeContract(t *testing.T) {
	root := strings.ToLower(resourceID(dataFactoryType, "factory"))
	endpoint := apiURL(root+"/pipelineruns/2f7fdb90-5df1-4b8e-ac2f-064cfa58202b/cancel", dataFactoryVersion)
	for _, mode := range []string{"empty", "empty-object", "empty-body", "string", "null", "array", "number", "boolean", "wrong-method", "wrong-version", "wrong-run", "extra-query", "duplicate-query"} {
		t.Run(mode, func(t *testing.T) {
			method, target := "POST", endpoint
			var body any = ""
			switch mode {
			case "empty-object":
				body = map[string]any{}
			case "string":
				body = "Succeeded"
			case "null":
				body = nil
			case "array":
				body = []any{}
			case "number":
				body = 1
			case "boolean":
				body = true
			case "wrong-method":
				method = "GET"
			case "wrong-version":
				target = strings.Replace(target, dataFactoryVersion, "2026-01-01", 1)
			case "wrong-run":
				target = strings.Replace(target, "2f7fdb90-5df1-4b8e-ac2f-064cfa58202b", "any-operation", 1)
			case "extra-query":
				target += "&unsafe=true"
			case "duplicate-query":
				target += "&api-version=" + dataFactoryVersion
			}
			runtime := protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
				if req.Method != method || req.URL.String() != target {
					t.Fatal("cancel request changed", req.Method, req.URL)
				}
				res := jsonResponse(200, body, nil)
				if mode == "empty-body" {
					res.Body = http.NoBody
				}
				return res, nil
			})
			c, err := runtime.resolve(t.Context(), "connection")
			if err != nil {
				t.Fatal(err)
			}
			res, err := c.request(t.Context(), method, target)
			ok := mode == "empty" || mode == "empty-object" || mode == "empty-body"
			if (err == nil) != ok || err == nil && len(res.data) != 0 {
				t.Fatal("unexpected primitive response accepted", mode, err, res.data)
			}
		})
	}
	for _, query := range []string{"&isRecursive=false", "&isRecursive=true"} {
		u, _ := url.Parse(endpoint + query)
		if !dataFactoryCancelStringResponse("POST", u) {
			t.Fatal("native optional query rejected")
		}
	}
}
