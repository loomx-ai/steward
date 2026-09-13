package azure

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"os"
	"strings"
	"testing"
)

func TestElasticSanOriginalSnapshotDeleteRecording(t *testing.T) {
	var manifest struct {
		SourceURI  string `json:"source_uri"`
		SourceHash string `json:"source_sha256"`
		Responses  []struct {
			Interaction, Status int
			Method, Path        string
			Version             string `json:"api_version"`
			File                string `json:"body_file"`
			Hash                string `json:"body_sha256"`
			Location            struct {
				Path, Monitor string
				Version       string   `json:"api_version"`
				Keys          []string `json:"query_keys"`
				Hash          string   `json:"original_url_sha256"`
			} `json:"location"`
		} `json:"responses"`
	}
	raw, err := os.ReadFile("fixtures/elastic-san/cli-snapshot-sources.json")
	if err != nil || json.Unmarshal(raw, &manifest) != nil || manifest.SourceHash != "08f109e11c530c2116e9cb6ef797a10dc4406f00ac9fd8da8c511bcb8c0e2dd3" || !strings.Contains(manifest.SourceURI, "/2aa1d8fc6417d0d5055acd88e9491e29734ad62b/") || len(manifest.Responses) != 6 {
		t.Fatal("original snapshot source provenance", err)
	}
	values, bodies := map[int]map[string]any{}, map[int][]byte{}
	status := map[int]int{}
	endpoint, location := "", ""
	for _, entry := range manifest.Responses {
		body, err := os.ReadFile("fixtures/elastic-san/" + entry.File)
		value := map[string]any{}
		if err != nil || fmt.Sprintf("%x", sha256.Sum256(body)) != entry.Hash || len(body) != 0 && json.Unmarshal(body, &value) != nil || entry.Version != "2025-09-01" {
			t.Fatal("original response bytes changed", entry.File, err)
		}
		values[entry.Interaction], bodies[entry.Interaction], status[entry.Interaction] = value, body, entry.Status
		if entry.Interaction == 24 {
			if entry.Method != "DELETE" || entry.Status != 202 || entry.Location.Monitor != "true" || entry.Location.Version != entry.Version || len(entry.Location.Keys) != 6 || len(entry.Location.Hash) != 64 {
				t.Fatal("native Location contract evidence changed")
			}
			location = strings.Split(entry.Location.Path, "/")[6]
			// Only the composed request changes version/signature values. The
			// source metadata and six original bodies remain byte-for-byte intact.
			endpoint = apiURL(entry.Location.Path, elasticSanVersion) + "&monitor=true&t=private-elastic-recording&c=opaque&s=opaque&h=opaque"
		}
	}
	id := strings.ToLower(text(values[22]["id"]))
	if (&client{}).elasticSanChildProtection(elasticSanSnapshotType, values[22]) != "" || object(values[24]["properties"])["provisioningState"] != "Deleting" || len(array(values[23]["value"])) != 1 || len(array(values[27]["value"])) != 0 || len(bodies[25])+len(bodies[26]) != 0 || status[25] != 202 || status[26] != 200 {
		t.Fatal("recorded snapshot lifecycle changed")
	}
	calls := 0
	c := &client{subscription: strings.Split(id, "/")[2], http: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		if req.URL.String() != endpoint || req.Method != "GET" || calls > 2 {
			t.Fatal("recording replay escaped its callback")
		}
		i := 24 + calls
		return &http.Response{StatusCode: status[i], Header: http.Header{}, Body: io.NopCloser(strings.NewReader(string(bodies[i])))}, nil
	})}}
	if c.privateConfiguration(hybridComputeChildSnapshot(values[22])) != c.privateConfiguration(hybridComputeChildSnapshot(values[24])) {
		t.Fatal("native DELETE changed immutable snapshot configuration")
	}
	receipt, err := c.elasticSanDeleteReceipt(id, location, response{status: 202, data: values[24], header: http.Header{"Location": {endpoint}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, done := range []bool{false, true} {
		wait, err := c.elasticSanPoll(t.Context(), id, location, receipt)
		if err != nil || wait.Done != done {
			t.Fatal("native Location sequence", wait.Done, err)
		}
		receipt = wait.Data
	}
}

func TestElasticSanPollingReceiptsAndScope(t *testing.T) {
	f := newElasticSanCleanupFixture(t)
	endpoint := elasticSanTestPollURL()
	for _, kind := range []string{elasticSanType, elasticSanGroupType, elasticSanVolumeType, elasticSanSnapshotType, elasticSanEndpointType} {
		id := f.ids[kind]
		for _, status := range []int{200, 202, 204} {
			res := response{status: status, header: http.Header{"Location": {endpoint}}}
			if status != 204 {
				res.data = f.values[id]
			}
			receipt, err := f.client.elasticSanDeleteReceipt(id, "eastus", res)
			if err != nil || status == 204 && receipt["url"] != nil || status != 204 && receipt["url"] != endpoint {
				t.Fatal("native DELETE status/body", kind, status, err)
			}
			if status == 204 {
				if wait, err := f.client.elasticSanPoll(t.Context(), id, "eastus", receipt); err != nil || !wait.Done {
					t.Fatal("terminal receipt polled", wait, err)
				}
			}
		}
	}
	id := f.ids[elasticSanSnapshotType]
	for _, bad := range []string{
		"https://contoso.com/operationstatus", strings.Replace(endpoint, "https:", "http:", 1), strings.Replace(endpoint, testSubscription, "00000000-0000-0000-0000-000000000000", 1), strings.Replace(endpoint, "Microsoft.ElasticSan", "Microsoft.Compute", 1), strings.Replace(endpoint, "/eastus/", "/westus/", 1), strings.Replace(endpoint, "/asyncoperations/", "/snapshots/", 1), strings.Replace(endpoint, testTenant, "not-a-uuid", 1), strings.Replace(endpoint, "/locations/", "/%6cocations/", 1), strings.Replace(endpoint, elasticSanVersion, "2024-07-01-preview", 1), endpoint + "&api-version=" + elasticSanVersion, endpoint + "&monitor=true", strings.Replace(endpoint, "monitor=true", "monitor=false", 1), strings.Replace(endpoint, "monitor=true", "monitor=TRUE", 1), strings.Replace(endpoint, "&monitor=true", "", 1), endpoint + "&filter=one", strings.Replace(endpoint, "&h=opaque", "", 1), strings.Replace(endpoint, "c=opaque", "c=", 1), strings.Replace(endpoint, "c=opaque", "c=%20secret", 1), endpoint + "#fragment", " " + endpoint, endpoint + "&c=again",
	} {
		if _, err := f.client.elasticSanDeleteReceipt(id, "eastus", response{status: 202, header: http.Header{"Location": {bad}}}); err == nil {
			t.Fatal("unsafe callback accepted")
		}
	}
	for _, res := range []response{
		{status: 201}, {status: 202}, {status: 202, header: http.Header{"Location": {""}}}, {status: 202, header: http.Header{"Location": {endpoint, endpoint}}}, {status: 202, header: http.Header{"Location": {endpoint}, "Azure-Asyncoperation": {endpoint}}}, {status: 202, header: http.Header{"Operation-Location": {endpoint}}}, {status: 202, header: http.Header{"Location": {endpoint}}, data: f.values[f.ids[elasticSanVolumeType]]}, {status: 204, data: f.values[id]}, {status: 200, data: map[string]any{"status": "Succeeded"}},
	} {
		if _, err := f.client.elasticSanDeleteReceipt(id, "eastus", res); err == nil {
			t.Fatal("incomplete or ambiguous DELETE accepted", res.status)
		}
	}
	synchronous, err := f.client.elasticSanDeleteReceipt(id, "eastus", response{status: 204, header: http.Header{"Location": {"https://contoso.com/operationstatus"}}})
	if err != nil || len(synchronous) != 1 {
		t.Fatal("diagnostic URL retained after terminal 204")
	}
	receipt, err := f.client.elasticSanDeleteReceipt(id, "eastus", response{status: 202, header: http.Header{"Location": {endpoint}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"url", "complete", "binding", "unknown"} {
		changed := maps.Clone(receipt)
		changed[field] = "forged"
		if _, err := f.client.elasticSanPoll(t.Context(), id, "eastus", changed); err == nil {
			t.Fatal("tampered receipt accepted", field)
		}
	}
	if _, err := f.client.elasticSanPoll(t.Context(), id+"-other", "eastus", receipt); err == nil {
		t.Fatal("receipt moved to another snapshot")
	}
	if _, err := f.client.elasticSanPoll(t.Context(), id, "westus", receipt); err == nil {
		t.Fatal("receipt moved to another region")
	}
	for _, changed := range []map[string]any{{"complete": false}, {"url": ""}, {"unknown": true}, {"url": endpoint, "complete": "true"}} {
		if _, err := f.client.elasticSanPoll(t.Context(), id, "eastus", f.client.elasticSanSignReceipt(id, "eastus", changed)); err == nil {
			t.Fatal("malformed saved phase accepted")
		}
	}
	if f.polls != 0 {
		t.Fatal("unsafe receipts reached transport")
	}
}

func TestElasticSanPollingRotationAndRestart(t *testing.T) {
	f := newElasticSanCleanupFixture(t)
	id, endpoint := f.ids[elasticSanSnapshotType], elasticSanTestPollURL()
	rotated := strings.Replace(endpoint, "t=private-elastic-signature", "t=private-elastic-rotated", 1)
	calls := 0
	f.hook = func(req *http.Request) (*http.Response, bool) {
		if req.URL.String() != endpoint && req.URL.String() != rotated {
			return nil, false
		}
		calls++
		if calls == 1 {
			return jsonResponse(202, map[string]any{}, http.Header{"Location": {rotated}, "Retry-After": {"7"}}), true
		}
		if req.URL.String() != rotated {
			t.Fatal("signed query rotation was lost")
		}
		return jsonResponse(200, map[string]any{}, nil), true
	}
	receipt, err := f.client.elasticSanDeleteReceipt(id, "eastus", response{status: 202, header: http.Header{"Location": {endpoint}}})
	if err != nil {
		t.Fatal(err)
	}
	wait, err := f.client.elasticSanPoll(t.Context(), id, "eastus", receipt)
	if err != nil || wait.Done || wait.Data["url"] != rotated || wait.RetryAfter.Seconds() != 7 {
		t.Fatal("pending Location response", wait.Done, err)
	}
	encoded, _ := json.Marshal(wait.Data)
	var restored map[string]any
	_ = json.Unmarshal(encoded, &restored)
	fresh, err := NewRuntime(f.runtime.credentials)
	if err != nil {
		t.Fatal(err)
	}
	fresh.transport = f.runtime.transport
	c, err := fresh.resolve(t.Context(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	wait, err = c.elasticSanPoll(t.Context(), id, "eastus", restored)
	if err != nil || !wait.Done || wait.Data["complete"] != true || calls != 2 {
		t.Fatal("restored native callback", wait.Done, calls, err)
	}
	if next, err := c.elasticSanPoll(t.Context(), id, "eastus", wait.Data); err != nil || !next.Done || calls != 2 {
		t.Fatal("completed receipt polled again", err)
	}
}

func TestElasticSanPollingRejectsUnverifiedCompletion(t *testing.T) {
	for _, failure := range []string{"missing", "forbidden", "redirect", "failure", "canceled", "empty-status", "case-status", "early-success", "resource-body", "wrong-resource", "wrong-operation", "changed-rotation", "unknown-header"} {
		t.Run(failure, func(t *testing.T) {
			f := newElasticSanCleanupFixture(t)
			id, endpoint := f.ids[elasticSanSnapshotType], elasticSanTestPollURL()
			calls := 0
			f.hook = func(req *http.Request) (*http.Response, bool) {
				calls++
				if req.URL.String() != endpoint {
					t.Fatal("followed unverified callback")
				}
				status, body, headers := 200, map[string]any{"status": "Succeeded"}, http.Header{}
				switch failure {
				case "missing":
					status = 404
				case "forbidden":
					status = 403
				case "redirect":
					status = 302
					headers.Set("Location", "https://untrusted.invalid/operation")
				case "failure":
					body["status"] = "Failed"
				case "canceled":
					body["status"] = "Canceled"
				case "empty-status":
					body["status"] = ""
				case "case-status":
					body["status"] = "succeeded"
				case "early-success":
					status = 202
				case "resource-body":
					body = f.values[id]
				case "wrong-resource":
					body["resourceId"] = id + "-other"
				case "wrong-operation":
					body["name"] = "other-operation"
				case "changed-rotation":
					headers.Set("Location", strings.Replace(endpoint, testTenant, "00000000-0000-0000-0000-000000000000", 1))
				case "unknown-header":
					headers.Set("Azure-AsyncOperation", endpoint)
				}
				return jsonResponse(status, body, headers), true
			}
			receipt, err := f.client.elasticSanDeleteReceipt(id, "eastus", response{status: 202, header: http.Header{"Location": {endpoint}}})
			if err != nil {
				t.Fatal(err)
			}
			wait, err := f.client.elasticSanPoll(t.Context(), id, "eastus", receipt)
			if err == nil || wait.Done || calls != 1 {
				t.Fatal("unverified callback marked complete", failure, wait.Done, calls, err)
			}
		})
	}
}
