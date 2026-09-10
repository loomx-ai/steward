package azure

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func TestMonitorPrivateLinkNativeOperationLocations(t *testing.T) {
	owner := resourceID(monitorPrivateLinkType, "scope")
	path := "/subscriptions/" + testSubscription + "/resourceGroups/test/providers/Microsoft.Insights/privateLinkScopeOperationStatuses/713192d7-503f-477a-9cfe-4efc3ee2bd11"
	want := apiURL(path, monitorPrivateLinkVersion)
	for _, value := range []string{path, armOrigin + path, want, path + "?api-version=" + monitorPrivateLinkVersion} {
		for _, resource := range []string{owner, owner + "/scopedResources/link", owner + "/privateEndpointConnections/connection"} {
			actual, err := monitorPrivateLinkOperationURL(testSubscription, resource, value)
			if err != nil || actual != want {
				t.Fatal("native operation location rejected", actual, err)
			}
		}
	}
	for _, value := range []string{"", "//evil.invalid" + path, "http://management.azure.com" + path, "https://user@management.azure.com" + path, "https://management.azure.com.evil.invalid" + path, path + "#fragment", path + "?api-version=2020-01-01", path + "?api-version=" + monitorPrivateLinkVersion + "&api-version=" + monitorPrivateLinkVersion, path + "?token=secret", path + "?api-version", strings.Replace(path, "/test/", "/another/", 1), strings.Replace(path, testSubscription, testTenant, 1), strings.Replace(path, "privateLinkScopeOperationStatuses", "components", 1), strings.Replace(path, "713192d7", "invalid", 1), path + "/child", strings.Replace(path, "Microsoft.Insights", "Microsoft.Network", 1), strings.Replace(path, "/test/", "/test%2fother/", 1), path + "?bad;query"} {
		if _, err := monitorPrivateLinkOperationURL(testSubscription, owner, value); err == nil {
			t.Fatal("invalid operation location accepted", value)
		}
	}
	if _, err := monitorPrivateLinkOperationURL(testSubscription, resourceID(vmType, "vm"), path); err == nil {
		t.Fatal("foreign resource type accepted as an operation owner")
	}
}

func TestMonitorPrivateLinkInvokeAndPollingResponses(t *testing.T) {
	owner := resourceID(monitorPrivateLinkType, "scope")
	operationPath := "/subscriptions/" + testSubscription + "/resourceGroups/test/providers/Microsoft.Insights/privateLinkScopeOperationStatuses/713192d7-503f-477a-9cfe-4efc3ee2bd11"
	r := protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
		if req.Method != "DELETE" || req.URL.Path != owner || req.URL.Query().Get("api-version") != monitorPrivateLinkVersion {
			t.Fatal("unexpected private link deletion", req.Method, req.URL)
		}
		return &http.Response{StatusCode: 202, Header: http.Header{"Location": {operationPath}}, Body: http.NoBody}, nil
	})
	result, err := r.Invoke(context.Background(), contracts.Invocation{ConnectionID: "connection", Operation: "Azure.Microsoft.Insights.PrivateLinkScopes_Delete", Parameters: map[string]any{"resourceGroupName": "test", "scopeName": "scope"}})
	if err != nil || result.OperationID != apiURL(operationPath, monitorPrivateLinkVersion) {
		t.Fatal("relative native operation location failed", result, err)
	}
	for _, test := range []struct {
		body  map[string]any
		valid bool
	}{
		{map[string]any{"id": operationPath, "name": last(operationPath), "status": "Succeeded"}, true},
		{map[string]any{"status": "InProgress"}, true},
		{map[string]any{"status": "Failed", "error": map[string]any{"code": "Failure"}}, true},
		{map[string]any{}, false},
		{map[string]any{"status": false}, false},
		{map[string]any{"id": owner, "status": "Succeeded"}, false},
		{map[string]any{"name": "another", "status": "Succeeded"}, false},
	} {
		c := directClient(func(*http.Request) (*http.Response, error) { return jsonResponse(200, test.body, nil), nil })
		_, err := c.request(context.Background(), "GET", result.OperationID)
		if test.valid != (err == nil) {
			t.Fatal("operation response validation disagreed", test.body, err)
		}
	}
}

func TestMonitorPrivateLinkDeleteResponseBoundaries(t *testing.T) {
	owner := resourceID(monitorPrivateLinkType, "scope")
	operationPath := "/subscriptions/" + testSubscription + "/resourceGroups/test/providers/Microsoft.Insights/privateLinkScopeOperationStatuses/713192d7-503f-477a-9cfe-4efc3ee2bd11"
	endpoint, _ := url.Parse(apiURL(owner, monitorPrivateLinkVersion))
	for _, test := range []struct {
		name    string
		status  int
		headers http.Header
		body    map[string]any
		valid   bool
	}{
		{"native relative", 202, http.Header{"Location": {operationPath}}, nil, true},
		{"consistent headers", 202, http.Header{"Location": {operationPath}, "Azure-Asyncoperation": {apiURL(operationPath, monitorPrivateLinkVersion)}}, nil, true},
		{"absent scope", 204, http.Header{}, nil, true},
		{"missing operation", 202, http.Header{}, nil, false},
		{"empty operation", 202, http.Header{"Location": {""}}, nil, false},
		{"duplicate location", 202, http.Header{"Location": {operationPath, operationPath}}, nil, false},
		{"different operation", 202, http.Header{"Location": {operationPath}, "Azure-Asyncoperation": {strings.Replace(operationPath, "713192d7", "813192d7", 1)}}, nil, false},
		{"unexpected root status", 200, http.Header{}, nil, false},
		{"unexpected body", 204, http.Header{}, map[string]any{"error": map[string]any{"code": "Failed"}}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			result := response{status: test.status, header: test.headers, data: test.body}
			err := monitorPrivateLinkResponse("DELETE", endpoint, &result)
			if test.valid != (err == nil) {
				t.Fatal("delete response validation disagreed", err)
			}
			if test.valid && test.status == 202 && (result.header.Get("Azure-AsyncOperation") != apiURL(operationPath, monitorPrivateLinkVersion) || result.header.Get("Location") != "") {
				t.Fatal("operation-status endpoint was not normalized for status polling")
			}
		})
	}
	endpoint, _ = url.Parse(apiURL(owner+"/scopedResources/link", monitorPrivateLinkVersion))
	if err := monitorPrivateLinkResponse("DELETE", endpoint, &response{status: 200, header: http.Header{}}); err != nil {
		t.Fatal("native synchronous scoped-resource unlink rejected", err)
	}
}
