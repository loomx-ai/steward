package azure

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"testing"
	"time"
)

type dataFactoryRecording struct {
	SourceURI   string              `json:"source_uri"`
	SourceSHA   string              `json:"source_sha256"`
	Index       int                 `json:"interaction_index"`
	Method      string              `json:"method"`
	URL         string              `json:"url"`
	Status      int                 `json:"status"`
	Headers     map[string][]string `json:"headers"`
	Body        string              `json:"body"`
	RequestBody *string             `json:"request_body"`
}

func dataFactoryRecordings(t *testing.T) []dataFactoryRecording {
	t.Helper()
	payload, err := os.ReadFile("fixtures/datafactory/cli-recordings.json")
	var result []dataFactoryRecording
	if err != nil || json.Unmarshal(payload, &result) != nil || len(result) != 64 || fmt.Sprintf("%x", sha256.Sum256(payload)) != "3d429bb10e79a1a6acf1760dd850b387a30a28f71ca083ad2417fe99e9465b7d" {
		t.Fatal("native Data Factory recordings changed", err)
	}
	return result
}

func (r dataFactoryRecording) header() http.Header {
	header := http.Header{}
	for name, values := range r.Headers {
		for _, value := range values {
			header.Add(name, value)
		}
	}
	return header
}

func (r dataFactoryRecording) response() *http.Response {
	return &http.Response{StatusCode: r.Status, Header: r.header(), Body: io.NopCloser(strings.NewReader(r.Body))}
}

func TestDataFactoryOfficialCLIRecordingProvenance(t *testing.T) {
	methods := map[string]int{}
	files := map[string]string{"test_datafactory_main.yaml": "ead0da2ea33784c79650ea494b2fc722bc07ae14017b39d0953db0cfa45dc4c5", "test_datafactory_managedPrivateEndpoint.yaml": "c9361d97a2c0b240dcbfd874edf15d7fd07bfb7cd1ea03dab30dd7d04246dc11"}
	seen := map[string]bool{}
	for _, row := range dataFactoryRecordings(t) {
		key := fmt.Sprintf("%s:%d", row.SourceURI, row.Index)
		if seen[key] || files[last(row.SourceURI)] != row.SourceSHA || !strings.HasPrefix(row.SourceURI, "https://raw.githubusercontent.com/Azure/azure-cli-extensions/d2f60986756c939c3d6d7f85e89798cca158d935/src/datafactory/azext_datafactory/tests/latest/recordings/") {
			t.Fatal("native recording source identity changed", key)
		}
		seen[key] = true
		u, err := url.Parse(row.URL)
		if err != nil || u.Host != "management.azure.com" || u.Query().Get("api-version") != dataFactoryVersion || !strings.Contains(strings.ToLower(u.Path), "/providers/microsoft.datafactory/") {
			t.Fatal("native recording scope changed", row.Index)
		}
		if row.Body != "" && !json.Valid([]byte(row.Body)) {
			t.Fatal("native body lost its original JSON text", row.Index)
		}
		for header := range row.Headers {
			if strings.Contains(strings.ToLower(header), "authorization") || strings.Contains(strings.ToLower(header), "cookie") {
				t.Fatal("request credentials entered native response evidence")
			}
		}
		if row.Method == "POST" && (strings.Contains(strings.ToLower(row.URL), "authkey") || strings.Contains(strings.ToLower(row.URL), "dataplaneaccess")) {
			t.Fatal("credential-producing operation retained")
		}
		methods[row.Method]++
	}
	if methods["GET"] != 42 || methods["POST"] != 8 || methods["DELETE"] != 14 || len(methods) != 3 {
		t.Fatal("native recording coverage changed", methods)
	}
}

func TestDataFactoryRecordedStopStatusAndFinalResult(t *testing.T) {
	rows := map[int]dataFactoryRecording{}
	for _, row := range dataFactoryRecordings(t) {
		if strings.HasSuffix(row.SourceURI, "main.yaml") && row.Index >= 69 && row.Index <= 81 {
			rows[row.Index] = row
		}
	}
	if len(rows) != 13 {
		t.Fatal("native Stop recording lost an interaction")
	}
	next := 70
	c := directClient(func(req *http.Request) (*http.Response, error) {
		row, ok := rows[next]
		if !ok || req.Method != "GET" || req.URL.String() != row.URL {
			t.Fatal("native Stop polling order changed", next, req.Method, req.URL)
		}
		next++
		return row.response(), nil
	})
	u, _ := url.Parse(rows[69].URL)
	id := strings.ToLower(strings.TrimSuffix(u.Path, "/stop"))
	c.subscription = strings.Split(id, "/")[2]
	operation, err := c.dataFactoryStopReceipt(id, response{status: 202, data: map[string]any{}, header: rows[69].header()})
	if err != nil || operation["status_url"] == operation["result_url"] {
		t.Fatal("native status and result URLs were collapsed", operation, err)
	}
	for i := 0; i < 11; i++ {
		poll, err := c.dataFactoryPollStop(t.Context(), id, operation)
		if err != nil || poll.Done != (i == 10) {
			t.Fatal("native operation completed before its final result", i, poll, err)
		}
		operation = poll.Data
	}
	if next != 82 || operation["status_done"] != true {
		t.Fatal("native final empty-body GET was not checked", next, operation)
	}
}

func TestDataFactoryDocumentedStopExampleRejectsWrongRuntime(t *testing.T) {
	example := dataFactoryExample(t, "IntegrationRuntimes_Stop")
	params := object(example["parameters"])
	id := strings.ToLower("/subscriptions/" + text(params["subscriptionId"]) + "/resourceGroups/" + text(params["resourceGroupName"]) + "/providers/Microsoft.DataFactory/factories/" + text(params["factoryName"]) + "/integrationRuntimes/" + text(params["integrationRuntimeName"]))
	header := http.Header{}
	for name, value := range object(object(object(example["responses"])["202"])["headers"]) {
		header.Set(name, text(value))
	}
	c := directClient(nil)
	c.subscription = text(params["subscriptionId"])
	if _, err := c.dataFactoryStopReceipt(id, response{status: 202, header: header}); err == nil {
		t.Fatal("documented Stop example's different runtime name accepted")
	}
	// Keep the original fixture untouched. Only this composed receipt binds
	// the documented legacy path to the runtime actually requested.
	for _, name := range []string{"Azure-AsyncOperation", "Location"} {
		if !strings.Contains(header.Get(name), "/exampleIntegrationRuntime/IntegrationRuntimesStop/") {
			t.Fatal("native example's identity discrepancy changed")
		}
		header.Set(name, strings.Replace(header.Get(name), "/exampleIntegrationRuntime/", "/"+text(params["integrationRuntimeName"])+"/", 1))
	}
	operation, err := c.dataFactoryStopReceipt(id, response{status: 202, header: header})
	if err != nil || operation["status_url"] != operation["result_url"] {
		t.Fatal("bound documented legacy Stop operation rejected", err)
	}
	endpoint := text(operation["status_url"])
	u, _ := url.Parse(endpoint)
	for _, state := range []string{"InProgress", "Succeeded"} {
		done, err := dataFactoryStopResponse(id, endpoint, "result", response{status: 200, data: map[string]any{"name": last(u.Path), "status": state}})
		if err != nil || done != (state == "Succeeded") {
			t.Fatal("legacy Stop response changed", state, done, err)
		}
	}
}

func TestDataFactoryStopPollingRejectsForgedAndIncompleteOperations(t *testing.T) {
	for _, mode := range []string{"wrong-owner", "wrong-subscription", "foreign-host", "wrong-version", "extra-query", "duplicate-query", "encoded-path", "wrong-action", "wrong-guid", "header-mismatch", "duplicate-header", "missing-final", "synchronous-location", "status-no-state", "status-empty", "status-wrong-name", "status-wrong-resource", "status-failed", "status-canceled", "status-unknown", "result-pending", "result-wrong-body", "result-forbidden", "result-gone", "saved-state-forged", "saved-extra-field", "retry-after"} {
		t.Run(mode, func(t *testing.T) {
			id := strings.ToLower(resourceID(dataFactoryType, "factory")) + "/integrationruntimes/runtime"
			statusURL := apiURL(id+"/stop/operationstatuses/9467e81909e34a018e8dbe85042070d9", dataFactoryVersion)
			resultURL := strings.Replace(statusURL, "/operationstatuses/", "/operationresults/", 1)
			header := http.Header{}
			header.Set("Azure-AsyncOperation", statusURL)
			header.Set("Location", resultURL)
			receiptStatus := 202
			calls := 0
			c := directClient(func(req *http.Request) (*http.Response, error) {
				calls++
				if req.URL.String() == statusURL {
					body := map[string]any{"name": "9467e81909e34a018e8dbe85042070d9", "status": "Succeeded"}
					switch mode {
					case "status-no-state":
						delete(body, "status")
					case "status-empty":
						res := jsonResponse(200, nil, nil)
						res.Body = http.NoBody
						return res, nil
					case "status-wrong-name":
						body["name"] = "other-operation"
					case "status-wrong-resource":
						body["resourceId"] = strings.Replace(id, "/runtime", "/other", 1)
					case "status-failed":
						body["status"] = "Failed"
					case "status-canceled":
						body["status"] = "Canceled"
					case "status-unknown":
						body["status"] = "CompleteEventually"
					case "retry-after":
						body["status"] = "InProgress"
						return jsonResponse(200, body, http.Header{"Retry-After": []string{"37"}}), nil
					}
					return jsonResponse(200, body, nil), nil
				}
				if req.URL.String() != resultURL {
					t.Fatal("unbound operation URL reached HTTP", req.URL)
				}
				switch mode {
				case "result-wrong-body":
					return jsonResponse(200, map[string]any{"resourceId": id, "unexpected": true}, nil), nil
				case "result-forbidden":
					return jsonResponse(403, map[string]any{"error": map[string]any{"code": "Forbidden"}}, nil), nil
				case "result-gone":
					return jsonResponse(404, map[string]any{"error": map[string]any{"code": "NotFound"}}, nil), nil
				}
				res := jsonResponse(200, nil, nil)
				res.Body = http.NoBody
				if mode == "result-pending" {
					res.StatusCode = 202
				}
				return res, nil
			})
			switch mode {
			case "wrong-owner":
				header.Set("Location", strings.Replace(resultURL, "/runtime/", "/other/", 1))
			case "wrong-subscription":
				header.Set("Location", strings.Replace(resultURL, testSubscription, "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb", 1))
			case "foreign-host":
				header.Set("Location", strings.Replace(resultURL, "management.azure.com", "foreign.invalid", 1))
			case "wrong-version":
				header.Set("Location", strings.Replace(resultURL, dataFactoryVersion, "2026-01-01", 1))
			case "extra-query":
				header.Set("Location", resultURL+"&resourceId=other")
			case "duplicate-query":
				header.Set("Location", resultURL+"&api-version="+dataFactoryVersion)
			case "encoded-path":
				header.Set("Location", strings.Replace(resultURL, "/stop/", "/%73top/", 1))
			case "wrong-action":
				header.Set("Location", strings.Replace(resultURL, "/stop/", "/start/", 1))
			case "wrong-guid":
				header.Set("Location", strings.Replace(resultURL, "9467e81909e34a018e8dbe85042070d9", "not-a-guid", 1))
			case "header-mismatch":
				header.Set("Location", strings.Replace(resultURL, "9467e819", "1234e819", 1))
			case "duplicate-header":
				header.Add("Location", resultURL)
			case "missing-final":
				header.Del("Location")
			case "synchronous-location":
				receiptStatus = 200
			}
			operation, err := c.dataFactoryStopReceipt(id, response{status: receiptStatus, header: header})
			badReceipt := slices.Contains([]string{"wrong-owner", "wrong-subscription", "foreign-host", "wrong-version", "extra-query", "duplicate-query", "encoded-path", "wrong-action", "wrong-guid", "header-mismatch", "duplicate-header", "missing-final", "synchronous-location"}, mode)
			if badReceipt {
				if err == nil || calls != 0 {
					t.Fatal("untrusted receipt accepted or issued requests", mode, err)
				}
				return
			}
			if err != nil {
				t.Fatal("valid native receipt rejected before poll", err)
			}
			if mode == "saved-state-forged" {
				operation["status_done"] = "true"
			}
			if mode == "saved-extra-field" {
				operation["bypass"] = true
			}
			poll, err := c.dataFactoryPollStop(t.Context(), id, operation)
			if mode == "result-pending" || mode == "retry-after" {
				if err != nil || poll.Done || mode == "retry-after" && poll.RetryAfter != 37*time.Second {
					t.Fatal("pending result or native backoff lost", mode, err, poll)
				}
			} else if err == nil {
				t.Fatal("uncertain native operation accepted", mode, poll)
			}
		})
	}
}

func TestDataFactoryNativeUnsubscribeAndCancellationReceipts(t *testing.T) {
	c := directClient(nil)
	id := strings.ToLower(resourceID(dataFactoryType, "factory")) + "/triggers/events"
	example := dataFactoryExample(t, "Triggers_UnsubscribeFromEvents")
	for _, status := range []int{200, 202} {
		responseExample := object(object(example["responses"])[fmt.Sprint(status)])
		res := response{status: status, data: object(responseExample["body"]), header: http.Header{}}
		if status == 200 {
			res.data["triggerName"] = last(id)
		} else {
			res.header.Set("Location", apiURL(id+"/getEventSubscriptionStatus", dataFactoryVersion))
		}
		if _, err := c.dataFactoryUnsubscribeReceipt(id, res); err != nil {
			t.Fatal("native unsubscribe response rejected", status, err)
		}
	}
	for _, mode := range []string{"missing-location", "wrong-trigger", "wrong-version", "other-host", "duplicate-location", "wrong-status", "unknown-state", "error-body", "unexpected-lro"} {
		res := response{status: 202, data: map[string]any{}, header: http.Header{}}
		endpoint := apiURL(id+"/getEventSubscriptionStatus", dataFactoryVersion)
		res.header.Set("Location", endpoint)
		switch mode {
		case "missing-location":
			res.header.Del("Location")
		case "wrong-trigger":
			res.header.Set("Location", strings.Replace(endpoint, "/events/", "/other/", 1))
		case "wrong-version":
			res.header.Set("Location", strings.Replace(endpoint, dataFactoryVersion, "2026-01-01", 1))
		case "other-host":
			res.header.Set("Location", strings.Replace(endpoint, "management.azure.com", "untrusted.invalid", 1))
		case "duplicate-location":
			res.header.Add("Location", endpoint)
		case "wrong-status":
			res.status = 204
		case "unknown-state":
			res.status, res.header, res.data = 200, http.Header{}, map[string]any{"triggerName": "events", "status": "MaybeDisabled"}
		case "error-body":
			res.data["error"] = map[string]any{"code": "Failed"}
		case "unexpected-lro":
			res.header.Set("Azure-AsyncOperation", endpoint)
		}
		if _, err := c.dataFactoryUnsubscribeReceipt(id, res); err == nil {
			t.Fatal("invalid unsubscribe receipt accepted", mode)
		}
	}
	for _, row := range dataFactoryRecordings(t) {
		if row.Method != "POST" || !strings.HasSuffix(row.URL, "/cancel?api-version="+dataFactoryVersion) {
			continue
		}
		if row.Index != 91 || row.Body != `""` {
			t.Fatal("native cancellation response changed")
		}
		c := directClient(func(req *http.Request) (*http.Response, error) { return row.response(), nil })
		c.subscription = "00000000-0000-0000-0000-000000000000"
		res, err := c.request(t.Context(), row.Method, row.URL)
		if err != nil || dataFactorySynchronousReceipt(res, false) != nil {
			t.Fatal("unchanged CLI cancel response rejected", err)
		}
	}
}
