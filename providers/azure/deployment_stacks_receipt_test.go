package azure

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

func stackReceiptFixture(t *testing.T) (*client, string, string, map[string]any, map[string]any) {
	t.Helper()
	id, endpoint, q := stackPollFixture()
	parameters := map[string]any{}
	for key, v := range q {
		parameters[key] = v[0]
	}
	c := directClient(nil)
	receipt, err := c.deploymentStackDeleteReceipt(id, "eastus", parameters, response{status: 202, data: map[string]any{}, header: http.Header{"Azure-Asyncoperation": {endpoint}}})
	if err != nil {
		t.Fatal(err)
	}
	return c, id, endpoint, parameters, receipt
}
func TestDeploymentStackReceiptResume(t *testing.T) {
	c, id, endpoint, _, receipt := stackReceiptFixture(t)
	calls := 0
	c.http = &http.Client{Transport: roundTripFunc(func(q *http.Request) (*http.Response, error) {
		calls++
		if q.Method != "GET" || q.URL.String() != endpoint {
			t.Fatal("unbound poll")
		}
		state := "deletingResources"
		if calls == 2 {
			state = "succeeded"
		}
		return jsonResponse(200, map[string]any{"id": q.URL.Path, "name": testTenant, "status": state}, http.Header{"Retry-After": {"7"}}), nil
	})}
	for step := 0; step < 3; step++ {
		wire, _ := json.Marshal(receipt)
		var saved map[string]any
		if json.Unmarshal(wire, &saved) != nil {
			t.Fatal("checkpoint JSON")
		}
		next, err := c.deploymentStackPoll(t.Context(), id, "eastus", saved)
		if err != nil || next.Done != (step > 0) {
			t.Fatal(step, next, err)
		}
		if step == 0 && next.RetryAfter != 7*time.Second {
			t.Fatal("lost retry timing")
		}
		receipt = next.Data
	}
	if calls != 2 {
		t.Fatal("completed receipt polled again", calls)
	}
}
func TestDeploymentStackReceiptRejectsTamperingBeforeNetwork(t *testing.T) {
	for _, fault := range []string{"nil", "binding", "owner", "region", "parameters", "url", "phase", "unknown", "management_group"} {
		t.Run(fault, func(t *testing.T) {
			c, id, _, _, receipt := stackReceiptFixture(t)
			region := "eastus"
			c.http = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				t.Fatal("invalid receipt reached network")
				return nil, nil
			})}
			switch fault {
			case "nil":
				receipt = nil
			case "binding":
				receipt["binding"] = "forged"
			case "owner":
				id += "other"
			case "region":
				region = "westus"
			case "parameters":
				object(receipt["parameters"])["unmanageAction.Resources"] = "detach"
			case "url":
				receipt["status_url"] = "https://example.com"
			case "phase":
				receipt["complete"] = true
			case "unknown":
				receipt["future"] = true
			case "management_group":
				id = "/providers/Microsoft.Management/managementGroups/group/providers/Microsoft.Resources/deploymentStacks/stack"
			}
			if _, err := c.deploymentStackPoll(t.Context(), id, region, receipt); err == nil {
				t.Fatal("unverified receipt accepted")
			}
		})
	}
}
func TestDeploymentStackNativeReceiptShapes(t *testing.T) {
	for _, fault := range []string{"no_callback", "synchronous_callback", "body", "duplicate_header", "unexpected_header", "changed_mode", "bypass", "wrong_region"} {
		t.Run(fault, func(t *testing.T) {
			c, id, endpoint, parameters, _ := stackReceiptFixture(t)
			res := response{status: 202, data: map[string]any{}, header: http.Header{"Azure-Asyncoperation": {endpoint}}}
			switch fault {
			case "no_callback":
				res.header = http.Header{}
			case "synchronous_callback":
				res.status = 200
			case "body":
				res.data["status"] = "succeeded"
			case "duplicate_header":
				res.header.Add("Azure-AsyncOperation", endpoint)
			case "unexpected_header":
				res.header.Set("Operation-Location", endpoint)
			case "changed_mode":
				parameters["unmanageAction.Resources"] = "detach"
			case "bypass":
				parameters["bypassStackOutOfSyncError"] = "true"
			case "wrong_region":
				res.header.Set("Azure-AsyncOperation", strings.Replace(endpoint, "eastus", "westus", 1))
			}
			if _, err := c.deploymentStackDeleteReceipt(id, "eastus", parameters, res); err == nil {
				t.Fatal("invalid delete receipt accepted")
			}
		})
	}
	c, id, _, parameters, _ := stackReceiptFixture(t)
	for _, status := range []int{200, 204} {
		receipt, err := c.deploymentStackDeleteReceipt(id, "eastus", parameters, response{status: status, data: map[string]any{}, header: http.Header{}})
		if err != nil {
			t.Fatal(err)
		}
		next, err := c.deploymentStackPoll(t.Context(), id, "eastus", receipt)
		if err != nil || !next.Done {
			t.Fatal(next, err)
		}
	}
}
func TestDeploymentStackLegacyResultResumeAndPrivacy(t *testing.T) {
	c, id, _, parameters, _ := stackReceiptFixture(t)
	endpoint := apiURL("/subscriptions/"+testSubscription+"/operationresults/"+testTenant, "2018-08-01")
	receipt, err := c.deploymentStackDeleteReceipt(id, "eastus", parameters, response{status: 202, data: map[string]any{}, header: http.Header{"Location": {endpoint}}})
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	c.http = &http.Client{Transport: roundTripFunc(func(q *http.Request) (*http.Response, error) {
		calls++
		if q.URL.String() != endpoint {
			t.Fatal("result endpoint changed")
		}
		status := 202
		if calls == 2 {
			status = 204
		}
		return &http.Response{StatusCode: status, Header: http.Header{"Retry-After": {"3"}}, Body: io.NopCloser(strings.NewReader(""))}, nil
	})}
	for i := 0; i < 2; i++ {
		next, err := c.deploymentStackPoll(t.Context(), id, "eastus", receipt)
		if err != nil || next.Done != (i == 1) {
			t.Fatal(next, err)
		}
		wire, _ := json.Marshal(next.Data)
		if json.Unmarshal(wire, &receipt) != nil {
			t.Fatal("result receipt persistence")
		}
	}
	safe := safeAPIPayload(map[string]any{"status": "failed", "future": "private-canary", "error": map[string]any{"message": "private-canary"}}, endpoint)
	wire, _ := json.Marshal(safe)
	if strings.Contains(string(wire), "private-canary") {
		t.Fatal("result diagnostics leaked")
	}
}

func TestDeploymentStackRecordedReceiptHTTPReplay(t *testing.T) {
	type interaction struct {
		Method, URL, Body string
		Status            int
		Headers           map[string][]string
	}
	var fixture struct {
		Recordings []struct {
			Scope    string
			Episodes [][]interaction
		}
	}
	wire, err := os.ReadFile("fixtures/deployment-stacks/delete-recordings.json")
	if err != nil || json.Unmarshal(wire, &fixture) != nil {
		t.Fatal("recorded replay fixture", err)
	}
	accepted, rejected, polls := 0, 0, 0
	for _, recording := range fixture.Recordings {
		for _, episode := range recording.Episodes {
			deletion := episode[0]
			owner, _ := url.Parse(deletion.URL)
			parts := strings.Split(owner.Path, "/")
			c := directClient(nil)
			if recording.Scope != "management_group" {
				c.subscription = parts[2]
			}
			parameters := map[string]any{}
			for key, values := range owner.Query() {
				parameters[key] = values[0]
			}
			headers := http.Header{}
			for key, values := range deletion.Headers {
				for _, value := range values {
					headers.Add(key, value)
				}
			}
			callback := headers.Get("Azure-AsyncOperation")
			parsed, _ := url.Parse(callback)
			region := strings.Split(strings.Split(parsed.Path, "/locations/")[1], "/")[0]
			receipt, err := c.deploymentStackDeleteReceipt(owner.Path, region, parameters, response{status: deletion.Status, data: map[string]any{}, header: headers})
			if recording.Scope == "management_group" || parameters["bypassStackOutOfSyncError"] == "true" {
				if err == nil {
					t.Fatal("unapproved receipt accepted")
				}
				rejected++
				continue
			}
			if err != nil {
				t.Fatal(recording.Scope, err)
			}
			accepted++
			index := 1
			c.http.Transport = roundTripFunc(func(q *http.Request) (*http.Response, error) {
				if index >= len(episode) {
					t.Fatal("extra native poll")
				}
				current := episode[index]
				index++
				polls++
				if q.Method != current.Method || q.URL.String() != current.URL {
					t.Fatal("native poll selector changed")
				}
				h := http.Header{}
				for key, values := range current.Headers {
					for _, v := range values {
						h.Add(key, v)
					}
				}
				return &http.Response{StatusCode: current.Status, Header: h, Body: io.NopCloser(strings.NewReader(current.Body))}, nil
			})
			for index < len(episode) {
				wire, _ := json.Marshal(receipt)
				var saved map[string]any
				if json.Unmarshal(wire, &saved) != nil {
					t.Fatal("saved native receipt")
				}
				result, err := c.deploymentStackPoll(t.Context(), owner.Path, region, saved)
				if err != nil || result.Done != (index == len(episode)) {
					t.Fatal(recording.Scope, index, result.Done, err)
				}
				receipt = result.Data
			}
		}
	}
	if accepted != 5 || rejected != 2 || polls != 34 {
		t.Fatal("native replay coverage", accepted, rejected, polls)
	}
}

func TestDeploymentStackDualHeaderPhases(t *testing.T) {
	c, id, statusURL, parameters, _ := stackReceiptFixture(t)
	resultURL := apiURL("/subscriptions/"+testSubscription+"/operationresults/"+testTenant, "2018-08-01")
	receipt, err := c.deploymentStackDeleteReceipt(id, "eastus", parameters, response{status: 202, data: map[string]any{}, header: http.Header{"Azure-Asyncoperation": {statusURL}, "Location": {resultURL}}})
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	c.http.Transport = roundTripFunc(func(q *http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			if q.URL.String() != statusURL {
				t.Fatal("status skipped")
			}
			return jsonResponse(200, map[string]any{"id": q.URL.Path, "name": testTenant, "status": "succeeded"}, nil), nil
		}
		if q.URL.String() != resultURL {
			t.Fatal("wrong result selector")
		}
		return &http.Response{StatusCode: 204, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(""))}, nil
	})
	first, err := c.deploymentStackPoll(t.Context(), id, "eastus", receipt)
	if err != nil || first.Done || first.Data["status_done"] != true {
		t.Fatal(first, err)
	}
	wire, _ := json.Marshal(first.Data)
	var saved map[string]any
	if json.Unmarshal(wire, &saved) != nil {
		t.Fatal("phase persistence")
	}
	next, err := c.deploymentStackPoll(t.Context(), id, "eastus", saved)
	if err != nil || !next.Done || calls != 2 {
		t.Fatal(next, err, calls)
	}
}
