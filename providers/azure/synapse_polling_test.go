package azure

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func synapseTestPollURL(id, role string) string {
	collection := "operationResults"
	if role == "status_url" {
		collection = "operationStatuses"
	}
	workspace := strings.Join(strings.Split(id, "/")[:9], "/")
	return apiURL(workspace+"/"+collection+"/"+testTenant, synapseVersion)
}
func TestSynapseOperationNativeExamples(t *testing.T) {
	payload, err := os.ReadFile("fixtures/synapse/polling-sources.json")
	var manifest []map[string]string
	if err != nil || json.Unmarshal(payload, &manifest) != nil || len(manifest) != 2 {
		t.Fatal("native polling manifest", err)
	}
	metadata, err := providerData()
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range manifest {
		payload, err = os.ReadFile("fixtures/synapse/" + entry["file"])
		if err != nil || fmt.Sprintf("%x", sha256.Sum256(payload)) != entry["source_sha256"] || entry["source_uri"] != synapseARMSource+"examples/"+entry["file"] {
			t.Fatal("native example changed")
		}
		var raw map[string]any
		if json.Unmarshal(payload, &raw) != nil {
			t.Fatal("bad example")
		}
		op, ok := metadata.catalog.Operation("Azure.Microsoft.Synapse." + entry["operation"])
		if !ok || op.SourceURI != synapseARMSource+"operations.json" || op.Call.Path != entry["path"] {
			t.Fatal("native operation changed")
		}
		params := object(raw["parameters"])
		bound, err := bindAzureREST(op, params)
		if err != nil {
			t.Fatal(err)
		}
		u, _ := url.Parse(bound.URL)
		owner := strings.ToLower(strings.Join(strings.Split(u.Path, "/")[:9], "/"))
		c := &client{subscription: text(params["subscriptionId"])}
		role := synapseARMOperationRole(op.ID)
		for code, value := range object(raw["responses"]) {
			if code == "default" || code == "404" {
				continue
			}
			status := map[string]int{"200": 200, "201": 201, "202": 202, "204": 204}[code]
			body := object(object(value)["body"])
			done, state, err := c.synapsePollObservation(owner, bound.URL, role, response{status: status, data: body})
			if err != nil || done != (role == "result_url" && status != 202) || role == "status_url" && state != "InProgress" {
				t.Fatal("native response", entry["file"], code, done, state, err)
			}
		}
	}
}
func TestSynapseDeleteReceiptsAndPollingBoundaries(t *testing.T) {
	c := &client{subscription: testSubscription}
	for _, kind := range []string{synapseType, synapseSparkType, synapseSQLType} {
		id := strings.ToLower(resourceID(synapseType, "first"))
		if kind != synapseType {
			collection := "bigdatapools"
			if kind == synapseSQLType {
				collection = "sqlpools"
			}
			id += "/" + collection + "/pool"
		}
		for _, status := range []int{200, 202, 204} {
			h := http.Header{}
			if status != 204 {
				h.Set("Location", synapseTestPollURL(id, "result_url"))
				h.Set("Azure-AsyncOperation", synapseTestPollURL(id, "status_url"))
			}
			receipt, err := c.synapseDeleteReceipt(id, response{status: status, header: h})
			if err != nil || c.synapseVerifyReceipt(id, receipt) != nil {
				t.Fatal(kind, status, err)
			}
		}
		endpoint := synapseTestPollURL(id, "result_url")
		for _, bad := range []string{strings.Replace(endpoint, "https:", "http:", 1), strings.Replace(endpoint, testSubscription, testTenant, 1), strings.Replace(endpoint, "/first/", "/another/", 1), strings.Replace(endpoint, "operationResults", "bigDataPools", 1), strings.Replace(endpoint, synapseVersion, "2021-06-01-preview", 1), endpoint + "&api-version=" + synapseVersion, endpoint + "&sig=secret", endpoint + "#fragment", strings.Replace(endpoint, "operationResults", "%6fperationResults", 1), strings.Replace(endpoint, testTenant, "..", 1)} {
			if _, err := c.synapsePollURL(id, bad, "result_url"); err == nil {
				t.Fatal("foreign polling URL accepted", bad)
			}
		}
		for _, fault := range []string{"missing location", "empty location", "duplicate", "mismatch", "unknown header", "204 location", "202 malformed body"} {
			h := http.Header{"Location": {endpoint}}
			status := 202
			body := map[string]any{}
			switch fault {
			case "missing location":
				h = nil
			case "empty location":
				h.Set("Location", "")
			case "duplicate":
				h.Add("Location", endpoint)
			case "mismatch":
				h.Set("Azure-AsyncOperation", strings.Replace(synapseTestPollURL(id, "status_url"), testTenant, testApplication, 1))
			case "unknown header":
				h.Set("Operation-Location", endpoint)
			case "204 location":
				status = 204
			case "202 malformed body":
				body = map[string]any{"id": id + "wrong"}
			}
			if _, err := c.synapseDeleteReceipt(id, response{status: status, header: h, data: body}); err == nil {
				t.Fatal("invalid receipt", fault)
			}
		}
	}
}
func TestSynapsePollingResumeAndReadbackSeparation(t *testing.T) {
	f := newSynapseTransportFixture(t)
	id := strings.ToLower(text(f.pool["id"]))
	statusURL, resultURL := synapseTestPollURL(id, "status_url"), synapseTestPollURL(id, "result_url")
	polls := 0
	f.override = func(q *http.Request) (*http.Response, bool) {
		if q.URL.String() != statusURL && q.URL.String() != resultURL {
			return nil, false
		}
		polls++
		switch polls {
		case 1:
			return jsonResponse(200, map[string]any{"status": "InProgress", "properties": map[string]any{"secret": "poll-secret-canary"}}, http.Header{"Retry-After": {"7"}}), true
		case 2:
			if q.URL.String() != statusURL {
				t.Fatal("skipped status")
			}
			return jsonResponse(200, map[string]any{"status": "Succeeded"}, nil), true
		case 3:
			if q.URL.String() != resultURL {
				t.Fatal("skipped result")
			}
			return jsonResponse(202, nil, http.Header{"Location": {resultURL}}), true
		case 4:
			return jsonResponse(204, nil, nil), true
		}
		t.Fatal("completed operation polled again")
		return nil, false
	}
	c, err := f.runtime.resolve(t.Context(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	h := http.Header{"Location": {resultURL}}
	h.Set("Azure-AsyncOperation", statusURL)
	receipt, err := c.synapseDeleteReceipt(id, response{status: 202, header: h, data: f.pool})
	if err != nil {
		t.Fatal(err)
	}
	var logs []execution.JobLogEntry
	ctx := execution.WithJobLogSink(t.Context(), execution.JobLogSinkFunc(func(_ context.Context, e execution.JobLogEntry) { logs = append(logs, e) }))
	for i := 1; i <= 4; i++ {
		encoded, _ := json.Marshal(receipt)
		var restored map[string]any
		if json.Unmarshal(encoded, &restored) != nil {
			t.Fatal("receipt serialization")
		}
		fresh, err := NewRuntime(f.runtime.credentials)
		if err != nil {
			t.Fatal(err)
		}
		fresh.transport = f.runtime.transport
		c, err = fresh.resolve(ctx, "connection")
		if err != nil {
			t.Fatal(err)
		}
		wait, err := c.synapsePoll(ctx, id, restored)
		if err != nil || wait.Done != (i == 4) || i == 1 && wait.RetryAfter.Seconds() != 7 {
			t.Fatal("resume", i, wait, err)
		}
		receipt = wait.Data
	}
	if wait, err := c.synapsePoll(ctx, id, receipt); err != nil || !wait.Done || polls != 4 {
		t.Fatal("completed receipt not stable", wait, err)
	}
	// Operation success must not close a resource that its own GET still returns.
	if res, err := c.request(ctx, "GET", apiURL(id, synapseVersion)); err != nil || res.status != 200 {
		t.Fatal("resource readback lost", err)
	}
	encoded, _ := json.Marshal([]any{receipt, logs})
	if strings.Contains(string(encoded), "poll-secret-canary") {
		t.Fatal("operation data leaked")
	}
	for _, fault := range []string{"missing binding", "complete", "url", "unknown", "owner", "connection"} {
		bad := maps.Clone(receipt)
		owner := id
		switch fault {
		case "missing binding":
			delete(bad, "binding")
		case "complete":
			delete(bad, "complete")
		case "url":
			bad["result_url"] = strings.Replace(resultURL, testTenant, testApplication, 1)
		case "unknown":
			bad["new"] = true
		case "owner":
			owner = id + "-other"
		case "connection":
			copy := *c
			copy.fingerprint = sha256.Sum256([]byte("different"))
			if _, err := copy.synapsePoll(ctx, id, bad); err == nil {
				t.Fatal("connection changed accepted")
			}
			continue
		}
		if _, err := c.synapsePoll(ctx, owner, bad); err == nil || polls != 4 {
			t.Fatal("tampered receipt accepted", fault, err)
		}
	}
}
func TestSynapsePollingRejectsUncertainCompletion(t *testing.T) {
	for _, role := range []string{"status_url", "result_url"} {
		for _, fault := range []string{"404", "403", "500", "206", "empty", "unknown state", "failed", "canceled", "wrong ID", "changed URL", "private error"} {
			t.Run(role+"/"+fault, func(t *testing.T) {
				f := newSynapseTransportFixture(t)
				id := strings.ToLower(text(f.pool["id"]))
				endpoint := synapseTestPollURL(id, role)
				status := 200
				body := map[string]any{"status": "Succeeded"}
				h := http.Header{}
				switch fault {
				case "404":
					status = 404
				case "403":
					status = 403
				case "500":
					status = 500
				case "206":
					status = 206
				case "empty":
					if role == "result_url" {
						status = 206
					}
					body = nil
				case "unknown state":
					body["status"] = "Future"
				case "failed":
					body["status"] = "Failed"
				case "canceled":
					body["status"] = "Canceled"
				case "wrong ID":
					body["id"] = "different"
				case "changed URL":
					h.Set("Location", strings.Replace(synapseTestPollURL(id, "result_url"), testTenant, testApplication, 1))
				case "private error":
					body["error"] = map[string]any{"message": "poll-secret-canary"}
				}
				f.override = func(q *http.Request) (*http.Response, bool) {
					if strings.EqualFold(q.URL.String(), endpoint) {
						return jsonResponse(status, body, h), true
					}
					return nil, false
				}
				c, err := f.runtime.resolve(t.Context(), "connection")
				if err != nil {
					t.Fatal(err)
				}
				receipt := c.synapseSignReceipt(id, map[string]any{role: endpoint})
				wait, err := c.synapsePoll(t.Context(), id, receipt)
				if err == nil || wait.Done || isNotFound(err) || strings.Contains(err.Error(), "poll-secret-canary") {
					t.Fatal("unverified completion escaped", wait, err)
				}
			})
		}
	}
}
func TestSynapseOperationInvoke(t *testing.T) {
	for _, role := range []string{"status_url", "result_url"} {
		f := newSynapseTransportFixture(t)
		id := strings.ToLower(text(f.workspace["id"]))
		endpoint := synapseTestPollURL(id, role)
		f.override = func(q *http.Request) (*http.Response, bool) {
			if strings.EqualFold(q.URL.String(), endpoint) {
				body := map[string]any{"status": "Succeeded", "properties": map[string]any{"payload": "poll-secret-canary"}}
				if role == "result_url" {
					return &http.Response{StatusCode: 200, Header: http.Header{"X-Ms-Request-Id": {"poll-id"}}, Body: io.NopCloser(strings.NewReader(""))}, true
				}
				return jsonResponse(200, body, http.Header{"X-Ms-Request-Id": {"poll-id"}}), true
			}
			return nil, false
		}
		operation := "Operations_GetLocationHeaderResult"
		if role == "status_url" {
			operation = "Operations_GetAzureAsyncHeaderResult"
		}
		inv := contracts.Invocation{ConnectionID: "connection", Operation: "Azure.Microsoft.Synapse." + operation, Parameters: map[string]any{"resourceGroupName": strings.Split(id, "/")[4], "workspaceName": "first", "operationId": testTenant}}
		result, err := f.runtime.Invoke(t.Context(), inv)
		encoded, _ := json.Marshal(result)
		if err != nil || result.Data["_synapse_operation_done"] != true || result.RequestID != "poll-id" || strings.Contains(string(encoded), "poll-secret-canary") {
			t.Fatal("native operation invoke", result, err)
		}
	}
	f := newSynapseTransportFixture(t)
	id := strings.ToLower(text(f.pool["id"]))
	endpoint := synapseTestPollURL(id, "result_url")
	f.override = func(q *http.Request) (*http.Response, bool) {
		if q.Method == "DELETE" {
			return jsonResponse(202, f.pool, http.Header{"Location": {endpoint}, "X-Ms-Request-Id": {"delete-id"}}), true
		}
		return nil, false
	}
	inv := contracts.Invocation{ConnectionID: "connection", Operation: "Azure.Microsoft.Synapse.BigDataPools_Delete", Parameters: map[string]any{"resourceGroupName": strings.Split(id, "/")[4], "workspaceName": "first", "bigDataPoolName": "pool"}}
	result, err := f.runtime.Invoke(t.Context(), inv)
	if err != nil || result.OperationID != endpoint || result.RequestID != "delete-id" {
		t.Fatal(result, err)
	}
	c, err := f.runtime.resolve(t.Context(), "connection")
	if err != nil || c.synapseVerifyReceipt(id, object(result.Data["_synapse_operation"])) != nil {
		t.Fatal("native receipt not retained", err)
	}
}

func TestSynapseLocationEmptyWireStatuses(t *testing.T) {
	for _, status := range []int{200, 201, 202, 204} {
		f := newSynapseTransportFixture(t)
		id := strings.ToLower(text(f.pool["id"]))
		endpoint := synapseTestPollURL(id, "result_url")
		f.override = func(q *http.Request) (*http.Response, bool) {
			if q.URL.String() == endpoint {
				return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(""))}, true
			}
			return nil, false
		}
		c, err := f.runtime.resolve(t.Context(), "connection")
		if err != nil {
			t.Fatal(err)
		}
		receipt := c.synapseSignReceipt(id, map[string]any{"result_url": endpoint})
		wait, err := c.synapsePoll(t.Context(), id, receipt)
		if err != nil || wait.Done != (status != 202) {
			t.Fatal(status, wait, err)
		}
	}
}

func TestSynapseLocationPreservesNonemptyWireBodies(t *testing.T) {
	for _, shape := range []string{"resource", "wrong resource", "null", "array", "malformed"} {
		t.Run(shape, func(t *testing.T) {
			f := newSynapseTransportFixture(t)
			id := strings.ToLower(text(f.pool["id"]))
			endpoint := synapseTestPollURL(id, "result_url")
			raw := batchClone(f.pool)
			object(raw["properties"])["provisioningState"] = "Succeeded"
			if shape == "wrong resource" {
				raw["id"] = id + "-other"
			}
			payload, _ := json.Marshal(raw)
			switch shape {
			case "null":
				payload = []byte("null")
			case "array":
				payload = []byte("[]")
			case "malformed":
				payload = []byte("{")
			}
			f.override = func(q *http.Request) (*http.Response, bool) {
				if q.URL.String() == endpoint {
					return &http.Response{StatusCode: 201, Header: http.Header{"X-Ms-Request-Id": {"result-201"}}, Body: io.NopCloser(strings.NewReader(string(payload)))}, true
				}
				return nil, false
			}
			c, err := f.runtime.resolve(t.Context(), "connection")
			if err != nil {
				t.Fatal(err)
			}
			wait, err := c.synapsePoll(t.Context(), id, c.synapseSignReceipt(id, map[string]any{"result_url": endpoint}))
			if shape == "resource" {
				if err != nil || !wait.Done {
					t.Fatal("valid 201 body changed", wait, err)
				}
			} else if err == nil || wait.Done {
				t.Fatal("nonempty body bypassed validation", shape, wait, err)
			}
		})
	}
}
