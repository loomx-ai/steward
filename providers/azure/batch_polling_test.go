package azure

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func TestBatchNodeRemovalConditionsThePool(t *testing.T) {
	s, r, assets := newBatchScenario(t)
	node := cdnAsset(t, assets, batchNodeType)
	solved := batchPlan(t, r, assets, node)
	if len(solved.Blockers) != 0 {
		t.Fatal(solved.Blockers)
	}
	request := servicePlanRequest(solved, assets, node)
	seen := false
	s.handle = func(req *http.Request) (*http.Response, bool) {
		if req.Method == "POST" && req.URL.Scheme+"://"+req.URL.Host == s.origin && req.URL.Path == "/pools/poolid/removenodes" {
			seen = true
			var body map[string]any
			if json.NewDecoder(req.Body).Decode(&body) != nil || len(body) != 2 || body["nodeDeallocationOption"] != "requeue" || len(array(body["nodeList"])) != 1 || array(body["nodeList"])[0] != "nodeid" || req.Header.Get("If-Match") != text(s.records["/pools/poolid"]["eTag"]) {
				t.Fatal("RemoveNodes did not condition the actual pool and exact node")
			}
			s.gone["/pools/poolid/nodes/nodeid"] = true
			return jsonResponse(202, nil, http.Header{"Request-Id": {"remove-node"}}), true
		}
		return nil, false
	}
	driver, _ := r.ResolveAction(t.Context(), "connection", node)
	receipt, err := driver.Execute(t.Context(), request)
	if err != nil || !seen || receipt.ProviderRequestID != "remove-node" {
		t.Fatal("Batch RemoveNodes", err)
	}
	if wait, err := driver.Wait(t.Context(), request, receipt); err != nil || !wait.Done {
		t.Fatal("Batch node readback", wait, err)
	}
	if s.arm.gone[s.account+"/pools/poolid"] || s.gone["/jobs/jobid"] || s.gone["/jobs/jobid/tasks/taskid"] {
		t.Fatal("RemoveNodes deleted its pool, job or requeued task")
	}
}

func TestBatchActiveScheduleDisablesBeforeDelete(t *testing.T) {
	for _, createJob := range []bool{false, true} {
		t.Run(map[bool]string{false: "stable", true: "new_job_before_disable"}[createJob], func(t *testing.T) {
			s, r, assets := newBatchScenario(t)
			schedule := cdnAsset(t, assets, batchScheduleType)
			raw := s.records["/jobschedules/schedule"]
			raw["state"] = "active"
			solved := batchPlan(t, r, assets, schedule)
			request := servicePlanRequest(solved, assets, schedule)
			sequence := []string{}
			s.handle = func(req *http.Request) (*http.Response, bool) {
				if req.URL.Scheme+"://"+req.URL.Host != s.origin {
					return nil, false
				}
				if req.Method == "POST" && req.URL.Path == "/jobschedules/schedule/disable" {
					if req.Header.Get("If-Match") != text(raw["eTag"]) || req.ContentLength > 0 {
						t.Fatal("schedule disable lost its native ETag or invented a body")
					}
					sequence = append(sequence, "disable")
					raw["state"], raw["eTag"] = "disabled", "disabled-etag"
					if createJob {
						s.lists["/jobschedules/schedule/jobs"] = []any{s.records["/jobs/jobid"]}
					}
					return jsonResponse(204, nil, nil), true
				}
				if req.Method == "DELETE" && req.URL.Path == "/jobschedules/schedule" {
					sequence = append(sequence, "delete")
					if req.Header.Get("If-Match") != "disabled-etag" {
						t.Fatal("schedule DELETE did not use its post-disable ETag")
					}
				}
				return nil, false
			}
			driver, _ := r.ResolveAction(t.Context(), "connection", schedule)
			receipt, err := driver.Execute(t.Context(), request)
			if createJob {
				if err == nil || len(sequence) != 1 || sequence[0] != "disable" {
					t.Fatal("schedule deleted an unreviewed newly created job", sequence, err)
				}
				return
			}
			if err != nil || len(sequence) != 2 || sequence[0] != "disable" || sequence[1] != "delete" {
				t.Fatal("schedule disable/delete sequence", sequence, err)
			}
			if wait, err := driver.Wait(t.Context(), request, receipt); err != nil || !wait.Done {
				t.Fatal("schedule readback", wait, err)
			}
		})
	}
}

func batchPollingScenario(t *testing.T, kind string) (*batchScenario, *Runtime, contracts.ActionRequest, *batchAction, string) {
	t.Helper()
	s, r, assets := newBatchScenario(t)
	if kind == batchAccountType {
		for id := range s.arm.records {
			if id != s.account {
				s.arm.gone[id] = true
			}
		}
		for id := range s.records {
			s.gone[id] = true
		}
	}
	target := cdnAsset(t, assets, kind)
	if kind == batchAccountType {
		assets = []asset.Asset{target}
	}
	solved := batchPlan(t, r, assets, target)
	if len(solved.Blockers) != 0 {
		t.Fatal(solved.Blockers)
	}
	request := servicePlanRequest(solved, assets, target)
	driver, err := r.ResolveAction(t.Context(), "connection", target)
	if err != nil {
		t.Fatal(err)
	}
	path := target.Identity.NativeID + "/accountOperationResults/sampleacct-30a022cb-a64f-4fd5-9289-8b38b342e9de"
	if kind == batchAccountType {
		path = "/subscriptions/" + testSubscription + "/providers/Microsoft.Batch/locations/japaneast/accountOperationResults/sampleacct-30a022cb-a64f-4fd5-9289-8b38b342e9de"
	}
	return s, r, request, driver.(*batchAction), apiURL(path, batchVersion)
}

func TestBatchARMLocationPollingResumesSignedRotation(t *testing.T) {
	for _, kind := range []string{batchAccountType, batchPECType} {
		t.Run(kind, func(t *testing.T) {
			s, r, request, driver, endpoint := batchPollingScenario(t, kind)
			initial := endpoint + "&t=one&c=one&s=one&h=one"
			next := endpoint + "&t=two&c=two&s=two&h=two"
			polls := 0
			s.handle = func(req *http.Request) (*http.Response, bool) {
				if req.Method == "DELETE" && strings.EqualFold(req.URL.Path, request.Asset.Identity.NativeID) {
					return jsonResponse(202, nil, http.Header{"Location": {initial}, "X-Ms-Request-Id": {"batch-arm-delete"}}), true
				}
				if strings.Contains(strings.ToLower(req.URL.Path), "/accountoperationresults/") {
					polls++
					if polls == 1 {
						if req.URL.String() != initial {
							t.Fatal("initial Batch operation changed")
						}
						return jsonResponse(202, nil, http.Header{"Location": {next}, "Retry-After": {"1"}}), true
					}
					if req.URL.String() != next {
						t.Fatal("resumed Batch operation ignored native Location rotation")
					}
					s.arm.gone[request.Asset.Identity.NativeID] = true
					return &http.Response{StatusCode: 200, Header: http.Header{}, Body: http.NoBody}, true
				}
				return nil, false
			}
			receipt, err := driver.Execute(t.Context(), request)
			if err != nil || receipt.ProviderOperationID != initial {
				t.Fatal("Batch native ARM LRO", err)
			}
			wait, err := driver.Wait(t.Context(), request, receipt)
			if err != nil || wait.Done || wait.Data["batch_poll_operation"] != next {
				t.Fatal("Batch signed poll continuation", wait, err)
			}
			receipt.Data = wait.Data
			payload, _ := json.Marshal(receipt)
			json.Unmarshal(payload, &receipt)
			resumed, _ := r.ResolveAction(t.Context(), "connection", request.Asset)
			if wait, err = resumed.Wait(t.Context(), request, receipt); err != nil || !wait.Done {
				t.Fatal("Batch resumed ARM readback", wait, err)
			}
		})
	}
}

func TestBatchPollingRejectsForeignAndTamperedReceipts(t *testing.T) {
	_, _, request, driver, endpoint := batchPollingScenario(t, batchPECType)
	for _, invalid := range []string{
		strings.Replace(endpoint, "management.azure.com", "foreign.example", 1),
		strings.Replace(endpoint, "/sampleacct/", "/foreign/", 1),
		strings.Replace(endpoint, "/connection/", "/other/", 1),
		endpoint + "&api-version=" + batchVersion,
		endpoint + "&unexpected=1",
		endpoint + "&t=one",
	} {
		if err := driver.validateOperationURL(invalid); err == nil {
			t.Fatal("foreign/ambiguous Batch LRO accepted", invalid)
		}
	}
	response, err := driver.operationResult(request, response{status: 202, header: http.Header{"Location": {endpoint}}})
	if err != nil {
		t.Fatal(err)
	}
	response.Data["batch_operation_binding"] = "tampered"
	if _, err := driver.Wait(t.Context(), request, response); err == nil {
		t.Fatal("Batch followed a tampered operation receipt")
	}
	poolMapping, _ := findType(batchPoolType)
	pool := &batchAction{client: driver.client, kind: poolMapping, accountID: driver.accountID, id: driver.accountID + "/pools/poolid", location: driver.location}
	validPool := apiURL(pool.accountID+"/poolOperationResults/delete-poolid-8D4EDFF164A11C9", batchVersion)
	if err := pool.validateOperationURL(validPool); err != nil {
		t.Fatal("native pool operation path", err)
	}
	u, _ := url.Parse(validPool)
	u.Path = strings.Replace(u.Path, "delete-poolid", "delete-other", 1)
	if err := pool.validateOperationURL(u.String()); err == nil {
		t.Fatal("pool LRO accepted a different pool")
	}
}
