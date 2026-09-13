package azure

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/loomx-ai/steward/internal/provider/contracts"
)

// This is a cross-version protocol adaptation, not same-version cloud evidence:
// change only the subscription and API version in memory. Follow newly returned
// signatures instead of the original CLI's initial URL; retain response bodies.
func TestHybridComputeRecordedPollingResume(t *testing.T) {
	payload, err := os.ReadFile("fixtures/hybridcompute/cli-recordings.json")
	if err != nil {
		t.Fatal(err)
	}
	payload = bytes.ReplaceAll(payload, []byte("00000000-0000-0000-0000-000000000000"), []byte(testSubscription))
	payload = bytes.ReplaceAll(payload, []byte("api-version=2026-07-15"), []byte("api-version="+hybridComputeVersion))
	var sources []struct {
		File string                  `json:"file"`
		Rows []redisRecordedResponse `json:"recordings"`
	}
	if json.Unmarshal(payload, &sources) != nil || len(sources) != 3 {
		t.Fatal("invalid native recordings")
	}
	deletes, polls, completed, rotations := 0, 0, 0, 0
	for _, source := range sources {
		t.Run(source.File, func(t *testing.T) {
			var id string
			var saved map[string]any
			for _, row := range source.Rows {
				if row.Status == 429 { // Classification is covered by the unmodified transport replay.
					continue
				}
				u, err := url.Parse(row.URI)
				if err != nil {
					t.Fatal(err)
				}
				endpoint := row.URI
				role := "status_url"
				if strings.Contains(u.Path, "/operationresults/") {
					role = "result_url"
				}
				if row.Method != "DELETE" {
					endpoint = text(saved[role])
					selected, _ := url.Parse(endpoint)
					if selected == nil || selected.Path != u.Path {
						t.Fatal("resume selected another operation")
					}
				}
				calls := 0
				// Recreate the runtime and OAuth client at every recorded response.
				r := protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
					calls++
					if req.Method != row.Method || req.URL.String() != endpoint {
						return nil, fmt.Errorf("resume did not use the exact saved native URL")
					}
					return streamAnalyticsRecordedHTTP(row), nil
				})
				c, err := r.resolve(t.Context(), "connection")
				if err != nil {
					t.Fatal(err)
				}
				if row.Method == "DELETE" {
					if saved != nil && saved["complete"] != true {
						t.Fatal("previous operation never completed")
					}
					id, _, err = parseID(u.Path)
					if err != nil {
						t.Fatal(err)
					}
					res, err := c.request(t.Context(), row.Method, row.URI)
					if err != nil {
						t.Fatal(err)
					}
					saved, err = c.hybridComputeDeleteReceipt(id, res)
					if err != nil {
						t.Fatal("recorded receipt rejected", row.Index, err)
					}
					deletes++
				} else {
					before, _ := json.Marshal(saved)
					wait, err := c.hybridComputePoll(t.Context(), id, saved)
					if err != nil || wait.Done != (role == "result_url") {
						t.Fatal("recorded poll failed or completed before final result", row.Index, err)
					}
					after, _ := json.Marshal(saved)
					if !bytes.Equal(before, after) {
						t.Fatal("poll mutated the previous durable receipt")
					}
					if text(wait.Data["status_url"]) != text(saved["status_url"]) {
						rotations++
					}
					if wait.RetryAfter != retryAfter(streamAnalyticsRecordedHTTP(row).Header) {
						t.Fatal("Retry-After lost")
					}
					saved = wait.Data
					polls++
					if wait.Done {
						completed++
					}
				}
				if calls != 1 {
					t.Fatal("unexpected native calls", calls)
				}
				encoded, err := json.Marshal(saved)
				if err != nil || json.Unmarshal(encoded, &saved) != nil || c.hybridComputeVerifyReceipt(id, saved) != nil {
					t.Fatal("receipt did not survive persistence", err)
				}
			}
		})
	}
	if deletes != 5 || polls != 19 || completed != 5 || rotations == 0 {
		t.Fatal("incomplete native polling coverage", deletes, polls, completed, rotations)
	}
}

func hybridComputeTestURLs() (string, string) {
	root := "https://management.azure.com/subscriptions/" + testSubscription + "/providers/Microsoft.HybridCompute/locations/eastus/"
	suffix := "/11111111-2222-3333-4444-555555555555?api-version=" + hybridComputeVersion + "&t=123&c=private-c&s=private-s&h=private-h"
	return root + "operationstatus" + suffix, root + "operationresults" + suffix
}

func TestHybridComputeReceiptBoundaries(t *testing.T) {
	id := strings.ToLower(resourceID(hybridMachineType, "machine") + "/extensions/extension")
	status, result := hybridComputeTestURLs()
	calls := 0
	c := directClient(func(*http.Request) (*http.Response, error) {
		calls++
		return nil, fmt.Errorf("invalid receipt reached transport")
	})
	for _, endpoint := range []string{
		strings.Replace(status, "management.azure.com", "example.com", 1),
		strings.Replace(status, "https:", "http:", 1),
		strings.Replace(status, testSubscription, testTenant, 1),
		strings.Replace(status, "Microsoft.HybridCompute", "Microsoft.Compute", 1),
		strings.Replace(status, "operationstatus", "operationstatuses", 1),
		strings.Replace(status, "eastus/", "../", 1),
		strings.Replace(status, "/eastus/", "/%65astus/", 1),
		strings.Replace(status, "11111111-2222-3333-4444-555555555555", "not-an-operation", 1),
		strings.Replace(status, hybridComputeVersion, "2026-07-15", 1),
		status + "&api-version=" + hybridComputeVersion,
		status + "&h=duplicate", status + "&filter=x", status + "#fragment",
		strings.Replace(status, "&h=private-h", "", 1),
		strings.Replace(status, "private-h", "", 1),
		strings.Replace(status, "private-h", "%20", 1),
		strings.Replace(status, "private-h", "%ZZ", 1), result,
	} {
		if _, err := c.hybridComputeDeleteReceipt(id, response{status: 202, header: http.Header{"Azure-Asyncoperation": {endpoint}}}); err == nil {
			t.Error("invalid status URL accepted")
		}
	}
	for _, headers := range []http.Header{
		{"Azure-Asyncoperation": {status, status}}, {"Location": {result, result}},
		{"Operation-Location": {status}}, {"Azure-Asyncoperation": {""}},
		{"Azure-Asyncoperation": {status}, "Location": {strings.Replace(result, "eastus", "westus", 1)}},
		{"Azure-Asyncoperation": {status}, "Location": {strings.Replace(result, "11111111", "aaaaaaaa", 1)}},
	} {
		if _, err := c.hybridComputeDeleteReceipt(id, response{status: 202, header: headers}); err == nil {
			t.Error("ambiguous receipt accepted")
		}
	}
	valid, err := c.hybridComputeDeleteReceipt(id, response{status: 202, header: http.Header{"Azure-Asyncoperation": {status}, "Location": {result}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, receipt := range []map[string]any{
		nil, {}, {"complete": true}, {"binding": valid["binding"]}, maps.Clone(valid),
	} {
		if len(receipt) > 1 {
			receipt["complete"] = true
		}
		if _, err := c.hybridComputePoll(t.Context(), id, receipt); err == nil {
			t.Error("unauthenticated phase accepted")
		}
	}
	for _, field := range []string{"status_url", "result_url", "binding"} {
		changed := maps.Clone(valid)
		changed[field] = text(changed[field]) + "changed"
		if _, err := c.hybridComputePoll(t.Context(), id, changed); err == nil {
			t.Error("changed receipt accepted", field)
		}
	}
	if _, err := c.hybridComputePoll(t.Context(), id+"other", valid); err == nil {
		t.Error("receipt reused for a different resource")
	}
	for _, extra := range []map[string]any{
		{"status_done": false}, {"complete": "true"}, {"status_url": 42}, {"unknown": "value"},
	} {
		changed := maps.Clone(valid)
		maps.Copy(changed, extra)
		if _, err := c.hybridComputePoll(t.Context(), id, c.hybridComputeSignReceipt(id, changed)); err == nil {
			t.Error("invalid saved field accepted", extra)
		}
	}
	for _, owner := range []string{"", resourceID(hybridMachineType, "machine"), strings.ToLower(resourceID("Microsoft.HybridCompute/privateLinkScopes", "scope")), strings.Replace(id, testSubscription, testTenant, 1)} {
		if _, err := c.hybridComputeDeleteReceipt(owner, response{status: 204}); err == nil {
			t.Error("invalid deletion owner accepted")
		}
	}
	for _, res := range []response{{status: 201}, {status: 204, data: map[string]any{"unexpected": true}}, {status: 200, data: map[string]any{"error": map[string]any{"code": "Failed"}}}} {
		if _, err := c.hybridComputeDeleteReceipt(id, res); err == nil {
			t.Error("invalid delete body or status accepted")
		}
	}
	synchronous, err := c.hybridComputeDeleteReceipt(id, response{status: 204})
	if err != nil {
		t.Fatal(err)
	}
	if wait, err := c.hybridComputePoll(t.Context(), id, synchronous); err != nil || !wait.Done {
		t.Fatal("synchronous operation did not complete", err)
	}
	unsigned, _, _ := strings.Cut(status, "&t=")
	if _, err := c.hybridComputeDeleteReceipt(id, response{status: 202, header: http.Header{"Azure-Asyncoperation": {unsigned}}}); err != nil {
		t.Fatal("scoped native URL without optional signature rejected", err)
	}
	c.fingerprint[0]++
	if _, err := c.hybridComputePoll(t.Context(), id, valid); err == nil {
		t.Error("receipt reused under another credential")
	}
	if calls != 0 {
		t.Fatal("invalid receipt performed HTTP", calls)
	}
}

func TestHybridComputePollFailureAndCompletion(t *testing.T) {
	id := strings.ToLower(resourceID(hybridMachineType, "machine") + "/runCommands/command")
	status, result := hybridComputeTestURLs()
	for _, tc := range []struct {
		name, role  string
		status      int
		body        string
		headers     http.Header
		valid, done bool
	}{
		{"queued", "status_url", 200, `{"name":"11111111-2222-3333-4444-555555555555","status":"Queued"}`, nil, true, false},
		{"status complete", "status_url", 200, `{"name":"11111111-2222-3333-4444-555555555555","status":"Succeeded"}`, nil, true, true},
		{"empty status", "status_url", 200, "", nil, false, false},
		{"missing identity", "status_url", 200, `{"status":"Succeeded"}`, nil, false, false},
		{"status missing", "status_url", 404, `{"error":{"code":"NotFound"}}`, nil, false, false},
		{"wrong identity", "status_url", 200, `{"name":"other","status":"Succeeded"}`, nil, false, false},
		{"failure", "status_url", 200, `{"status":"Failed","error":{"code":"InternalError"}}`, nil, false, false},
		{"cancelled", "status_url", 200, `{"status":"Canceled"}`, nil, false, false},
		{"unrecognized state", "status_url", 200, `{"name":"11111111-2222-3333-4444-555555555555","status":"Completed"}`, nil, false, false},
		{"accepted success", "status_url", 202, `{"name":"11111111-2222-3333-4444-555555555555","status":"Succeeded"}`, nil, false, false},
		{"empty result", "result_url", 200, "", nil, true, true},
		{"no content result", "result_url", 204, "", nil, true, true},
		{"null result", "result_url", 200, "null", nil, false, false},
		{"accepted empty result", "result_url", 202, "", nil, false, false},
		{"result error envelope", "result_url", 200, `{"code":"InternalError"}`, nil, false, false},
		{"result wrong resource", "result_url", 200, `{"status":"Succeeded","resourceId":"other"}`, nil, false, false},
		{"result redirect", "result_url", 302, "", http.Header{"Location": {result}}, false, false},
		{"result forbidden", "result_url", 403, `{"error":{"code":"AuthorizationFailed"}}`, nil, false, false},
		{"result missing", "result_url", 404, `{"error":{"code":"NotFound"}}`, nil, false, false},
		{"result throttled", "result_url", 429, `{"error":{"code":"TooManyRequests"}}`, nil, false, false},
		{"partial result", "result_url", 206, "{}", nil, false, false},
		{"different operation", "result_url", 200, "", http.Header{"Location": {strings.Replace(result, "11111111", "aaaaaaaa", 1)}}, false, false},
		{"new role", "result_url", 200, "", http.Header{"Azure-Asyncoperation": {status}}, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			endpoint := result
			if tc.role == "status_url" {
				endpoint = status
			}
			calls := 0
			c := directClient(func(req *http.Request) (*http.Response, error) {
				calls++
				if req.Method != "GET" || req.URL.String() != endpoint {
					t.Fatal("unexpected poll request")
				}
				headers := tc.headers.Clone()
				if headers == nil {
					headers = http.Header{}
				}
				headers.Set("Retry-After", "7")
				return &http.Response{StatusCode: tc.status, Header: headers, Body: io.NopCloser(strings.NewReader(tc.body))}, nil
			})
			receipt := c.hybridComputeSignReceipt(id, map[string]any{tc.role: endpoint})
			wait, err := c.hybridComputePoll(t.Context(), id, receipt)
			if (err == nil) != tc.valid || wait.Done != tc.done || calls != 1 {
				t.Fatal("unexpected polling outcome", wait.Done, calls, err)
			}
			if tc.valid && (wait.RetryAfter != 7*time.Second || c.hybridComputeVerifyReceipt(id, wait.Data) != nil) {
				t.Fatal("invalid saved progress")
			}
			if wait.Done {
				resumed, err := c.hybridComputePoll(t.Context(), id, wait.Data)
				if err != nil || !resumed.Done || calls != 1 {
					t.Fatal("completed receipt was not resumable", err)
				}
			}
		})
	}
}

func TestHybridComputeInvokeDeleteContracts(t *testing.T) {
	status, result := hybridComputeTestURLs()
	for _, operation := range []string{"Machines_Delete", "MachineExtensions_Delete", "MachineRunCommands_Delete", "LicenseProfiles_Delete"} {
		for _, tc := range []struct {
			name    string
			status  int
			headers http.Header
		}{
			{"async", 202, http.Header{"Azure-Asyncoperation": {status}, "Location": {result}}},
			{"location", 202, http.Header{"Location": {result}}},
			{"status only", 202, http.Header{"Azure-Asyncoperation": {status}}},
			{"missing callback", 202, nil}, {"sync", 204, nil}, {"extension sync", 200, nil},
			{"contradictory sync", 204, http.Header{"Location": {result}}},
			{"foreign callback", 202, http.Header{"Location": {strings.Replace(result, "Microsoft.HybridCompute", "Microsoft.Compute", 1)}}},
		} {
			t.Run(operation+"/"+tc.name, func(t *testing.T) {
				calls := 0
				r := protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
					calls++
					if req.Method != "DELETE" || req.URL.Query().Get("api-version") != hybridComputeVersion || req.Header.Get("X-Ms-Client-Request-Id") != azureRequestID("test-key") {
						t.Fatal("invalid native deletion request")
					}
					return &http.Response{StatusCode: tc.status, Header: tc.headers, Body: io.NopCloser(strings.NewReader(""))}, nil
				})
				params := map[string]any{"resourceGroupName": "test", "machineName": "machine"}
				switch operation {
				case "MachineExtensions_Delete":
					params["extensionName"] = "extension"
				case "MachineRunCommands_Delete":
					params["runCommandName"] = "command"
				case "LicenseProfiles_Delete":
					params["licenseProfileName"] = "default"
				}
				valid := tc.name == "async" || tc.name == "location" || tc.name == "sync" || tc.name == "status only" && operation != "MachineRunCommands_Delete" || tc.name == "extension sync" && operation == "MachineExtensions_Delete"
				invoked, err := r.Invoke(t.Context(), contracts.Invocation{ConnectionID: "connection", Operation: "Azure.Microsoft.HybridCompute." + operation, Parameters: params, IdempotencyKey: "test-key"})
				if (err == nil) != valid || calls != 1 {
					t.Fatal("unexpected Invoke outcome", calls, err)
				}
				if valid {
					expected := tc.headers.Get("Azure-AsyncOperation")
					if expected == "" {
						expected = tc.headers.Get("Location")
					}
					if invoked.OperationID != expected {
						t.Fatal("wrong polling URL returned")
					}
				}
			})
		}
	}
}
