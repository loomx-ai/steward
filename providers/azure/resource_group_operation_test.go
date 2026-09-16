package azure

import (
	"encoding/json"
	"io"
	"maps"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func groupOperationRequest() contracts.ActionRequest {
	return contracts.ActionRequest{Action: "delete", IdempotencyKey: "group-job", Asset: asset.Asset{Identity: asset.Identity{NativeType: groupType, NativeID: "/subscriptions/" + testSubscription + "/resourcegroups/reviewed"}}}
}
func groupOperationEndpoint() string {
	return "https://management.azure.com/subscriptions/" + testSubscription + "/operationresults/opaque_CASE-sensitive_id?api-version=" + resourcesVersion
}
func groupOperationJSON(t *testing.T, v map[string]any) map[string]any {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out
}
func TestResourceGroupOperationOfficialRecording(t *testing.T) {
	var fixture struct {
		Source       string
		SourceSHA256 string `json:"source_sha256"`
		Interactions []struct {
			Method, URL, Body string
			Status            int
			Headers           map[string][]string
		}
	}
	b, err := os.ReadFile("fixtures/resource-groups/delete-recording.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &fixture); err != nil {
		t.Fatal(err)
	}
	if len(fixture.Interactions) != 6 || fixture.Source == "" || len(fixture.SourceSHA256) != 64 {
		t.Fatal("missing native evidence")
	}
	c := directClient(nil)
	c.subscription = "00000000-0000-0000-0000-000000000000"
	req := groupOperationRequest()
	owner, _ := url.Parse(fixture.Interactions[0].URL)
	req.Asset.Identity.NativeID = owner.Path
	headers := func(h map[string][]string) http.Header {
		out := http.Header{}
		for k, values := range h {
			for _, v := range values {
				out.Add(k, v)
			}
		}
		return out
	}
	first := fixture.Interactions[0]
	receipt, err := c.resourceGroupOperationReceipt(req, response{status: first.Status, header: headers(first.Headers)})
	if err != nil {
		t.Fatal(err)
	}
	step := 1
	c.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		expected := fixture.Interactions[step]
		step++
		if r.Method != expected.Method || r.URL.String() != expected.URL {
			t.Fatal("request differs from native recording")
		}
		return &http.Response{StatusCode: expected.Status, Header: headers(expected.Headers), Body: io.NopCloser(strings.NewReader(expected.Body))}, nil
	})
	for step < len(fixture.Interactions) {
		saved := groupOperationJSON(t, receipt)
		before, _ := json.Marshal(saved)
		result, err := c.resourceGroupPollOperation(t.Context(), req, saved)
		if err != nil || result.Done != (step == len(fixture.Interactions)) {
			t.Fatal(step, result, err)
		}
		after, _ := json.Marshal(saved)
		if string(before) != string(after) {
			t.Fatal("mutated checkpoint")
		}
		receipt = result.Data
	}
	result, err := c.resourceGroupPollOperation(t.Context(), req, groupOperationJSON(t, receipt))
	if err != nil || !result.Done || step != len(fixture.Interactions) {
		t.Fatal(result, err)
	}
}
func TestResourceGroupOperationResumeAndRotation(t *testing.T) {
	c, req := directClient(nil), groupOperationRequest()
	endpoint := groupOperationEndpoint() + "&t=1&c=certificate&s=signature&h=hash"
	rotated := strings.Replace(endpoint, "t=1", "t=2", 1)
	receipt, err := c.resourceGroupOperationReceipt(req, response{status: 202, header: http.Header{"Location": {endpoint}}})
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	c.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		want, status, h := endpoint, 202, http.Header{"Location": {rotated}, "Retry-After": {"7"}}
		if calls == 2 {
			want, status, h = rotated, 200, http.Header{}
		}
		if r.Method != "GET" || r.URL.String() != want {
			t.Fatal("wrong poll", r.URL)
		}
		return &http.Response{StatusCode: status, Header: h, Body: io.NopCloser(strings.NewReader(""))}, nil
	})
	for step := 0; step < 3; step++ {
		next, err := c.resourceGroupPollOperation(t.Context(), req, groupOperationJSON(t, receipt))
		if err != nil || next.Done != (step > 0) {
			t.Fatal(step, next, err)
		}
		if step == 0 && next.RetryAfter != 7*time.Second {
			t.Fatal("retry timing lost")
		}
		receipt = next.Data
	}
	if calls != 2 {
		t.Fatal("repeated completed operation", calls)
	}
}
func TestResourceGroupOperationReceiptAuthentication(t *testing.T) {
	for _, fault := range []string{"missing", "binding", "job", "owner", "member", "phase", "url", "extra", "parameters", "type"} {
		t.Run(fault, func(t *testing.T) {
			c, req := directClient(nil), groupOperationRequest()
			saved, err := c.resourceGroupOperationReceipt(req, response{status: 202, header: http.Header{"Location": {groupOperationEndpoint()}}})
			if err != nil {
				t.Fatal(err)
			}
			c.http.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
				t.Fatal("untrusted checkpoint reached HTTP")
				return nil, nil
			})
			switch fault {
			case "missing":
				saved = nil
			case "binding":
				saved["binding"] = "forged"
			case "job":
				req.IdempotencyKey = "another"
			case "owner":
				req.Asset.Identity.NativeID += "other"
			case "member":
				req.LifecycleImpacts = []contracts.ActionImpact{{Delete: true}}
			case "phase":
				saved["done"] = true
			case "url":
				saved["url"] = groupOperationEndpoint() + "changed"
			case "extra":
				saved["extra"] = true
			case "parameters":
				req.Parameters = map[string]any{"forceDeletionTypes": "Microsoft.Compute/virtualMachines"}
			case "type":
				req.Asset.Identity.NativeType = vmType
			}
			if out, err := c.resourceGroupPollOperation(t.Context(), req, saved); err == nil || out.Done {
				t.Fatal(out, err)
			}
		})
	}
}
func TestResourceGroupOperationRejectsInvalidCallback(t *testing.T) {
	endpoint := groupOperationEndpoint()
	cases := []string{
		strings.Replace(endpoint, "https:", "http:", 1), strings.Replace(endpoint, "management.azure.com", "example.com", 1),
		strings.Replace(endpoint, testSubscription, testTenant, 1), strings.Replace(endpoint, "operationresults", "providers/Microsoft.Compute/operationresults", 1),
		strings.Replace(endpoint, "opaque_CASE-sensitive_id", "..", 1), strings.Replace(endpoint, "opaque_CASE-sensitive_id", "%2Fescape", 1),
		endpoint + "#fragment", endpoint + "&api-version=" + resourcesVersion, endpoint + "&forceDeletionTypes=all", endpoint + "&t=1",
		strings.Replace(endpoint, resourcesVersion, "2024-11-01", 1), strings.Replace(endpoint, "https://", "https://user@", 1), endpoint + "&t=1&c=x&s=x&h=x&h=y",
	}
	for _, u := range cases {
		if _, err := resourceGroupOperationURL(testSubscription, u); err == nil {
			t.Fatal("accepted invalid callback", u)
		}
	}
}
func TestResourceGroupOperationResponseFailures(t *testing.T) {
	for _, fault := range []string{"other_operation", "foreign", "ambiguous", "async_header", "body", "forbidden", "not_found", "redirect", "server_error"} {
		t.Run(fault, func(t *testing.T) {
			c, req := directClient(nil), groupOperationRequest()
			receipt, err := c.resourceGroupOperationReceipt(req, response{status: 202, header: http.Header{"Location": {groupOperationEndpoint()}}})
			if err != nil {
				t.Fatal(err)
			}
			original := maps.Clone(receipt)
			c.http.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
				status, body, h := 202, "", http.Header{}
				switch fault {
				case "other_operation":
					h.Set("Location", strings.Replace(groupOperationEndpoint(), "opaque_CASE", "opaque_case", 1))
				case "foreign":
					h.Set("Location", strings.Replace(groupOperationEndpoint(), testSubscription, testTenant, 1))
				case "ambiguous":
					h["Location"] = []string{groupOperationEndpoint(), groupOperationEndpoint()}
				case "async_header":
					h.Set("Azure-AsyncOperation", groupOperationEndpoint())
				case "body":
					status, body = 200, `{"status":"Succeeded"}`
				case "forbidden":
					status, body = 403, `{"error":{"code":"AuthorizationFailed"}}`
				case "not_found":
					status = 404
				case "redirect":
					status = 302
					h.Set("Location", "https://example.com")
				case "server_error":
					status = 500
				}
				return &http.Response{StatusCode: status, Header: h, Body: io.NopCloser(strings.NewReader(body))}, nil
			})
			if out, err := c.resourceGroupPollOperation(t.Context(), req, receipt); err == nil || out.Done || out.Data != nil {
				t.Fatal(out, err)
			}
			if !maps.Equal(original, receipt) {
				t.Fatal("failure mutated receipt")
			}
		})
	}
}
func TestResourceGroupOperationAcceptance(t *testing.T) {
	c, req := directClient(nil), groupOperationRequest()
	for _, status := range []int{200, 204} {
		receipt, err := c.resourceGroupOperationReceipt(req, response{status: status})
		if err != nil {
			t.Fatal(err)
		}
		out, err := c.resourceGroupPollOperation(t.Context(), req, groupOperationJSON(t, receipt))
		if err != nil || !out.Done {
			t.Fatal(out, err)
		}
	}
	for _, res := range []response{
		{status: 202}, {status: 404}, {status: 200, data: map[string]any{"error": "failure"}},
		{status: 200, header: http.Header{"Location": {groupOperationEndpoint()}}},
		{status: 202, header: http.Header{"Operation-Location": {groupOperationEndpoint()}}},
	} {
		if _, err := c.resourceGroupOperationReceipt(req, res); err == nil {
			t.Fatal("accepted ambiguous deletion", res)
		}
	}
}
