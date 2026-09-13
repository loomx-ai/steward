package catalog

import (
	"encoding/json"
	"net/url"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
)

func TestBindOfficialRESTRequests(t *testing.T) {
	google, err := ImportOfficial("google-discovery", asset.ProviderGCP, "fixture", restFixture(t, "google-compute"))
	if err != nil {
		t.Fatal(err)
	}
	op := requireRESTOperation(t, google, "compute.instances.delete")
	parameters := map[string]any{"project": "test-project", "zone": "us-central1-a", "instance": "vm-1", "requestId": "8e2cc7d7-7b18-4f6d-b803-7ad832718a66"}
	bound, err := BindREST(op, parameters)
	if err != nil {
		t.Fatal(err)
	}
	if bound.Method != "DELETE" || bound.URL != "https://compute.googleapis.com/compute/v1/projects/test-project/zones/us-central1-a/instances/vm-1?requestId=8e2cc7d7-7b18-4f6d-b803-7ad832718a66" {
		t.Fatalf("wrong request: %+v", bound)
	}
	if len(parameters) != 4 {
		t.Fatal("caller parameters mutated")
	}
	azure, err := ImportOfficial("azure-openapi", asset.ProviderAzure, "fixture", restFixture(t, "azure-public-ip"))
	if err != nil {
		t.Fatal(err)
	}
	op = requireRESTOperation(t, azure, "Azure.Microsoft.Network.PublicIPAddresses_Get")
	parameters = map[string]any{"subscriptionId": "sub", "resourceGroupName": "my group", "publicIpAddressName": "my-ip"}
	bound, err = BindREST(op, parameters)
	if err != nil {
		t.Fatal(err)
	}
	parsed, _ := url.Parse(bound.URL)
	if parsed.Query().Get("api-version") != "2024-05-01" || parsed.EscapedPath() != "/subscriptions/sub/resourceGroups/my%20group/providers/Microsoft.Network/publicIPAddresses/my-ip" {
		t.Fatalf("ARM request: %+v", bound)
	}
	parameters["api-version"] = "2099-01-01"
	if _, err := BindREST(op, parameters); err == nil {
		t.Fatal("API version override accepted")
	}
}

func TestRESTRedisEnterpriseNativeNameBoundaries(t *testing.T) {
	pattern := "^(?=.{1,60}$)[A-Za-z0-9]+(-[A-Za-z0-9]+)*$"
	property := map[string]any{"in": "path", "pattern": pattern}
	op := Operation{ID: "Azure.Microsoft.Cache.RedisEnterprise_Get", Call: &OperationCall{Style: "azure-rest", Endpoint: "https://management.azure.com", Method: "GET", Path: "/clusters/{name}", Version: "2025-07-01"}, InputSchema: map[string]any{"properties": map[string]any{"name": property}}}
	for name, valid := range map[string]bool{"a": true, "A1-cache": true, strings.Repeat("a", 60): true, "": false, strings.Repeat("a", 61): false, "-cache": false, "cache-": false, "two--hyphens": false, "under_score": false, "中文": false, "a\n": false, "a/b": false} {
		_, err := BindREST(op, map[string]any{"name": name})
		if (err == nil) != valid || property["pattern"] != pattern {
			t.Fatalf("Redis Enterprise name %q: %v", name, err)
		}
	}
}

func TestRESTSearchNativeNameBoundaries(t *testing.T) {
	pattern := "^(?=.{2,60}$)[a-z0-9][a-z0-9]+(-[a-z0-9]+)*$"
	property := map[string]any{"type": "string", "in": "path", "pattern": pattern}
	op := Operation{ID: "Azure.Microsoft.Search.Services_Get", Call: &OperationCall{Style: "azure-rest", Endpoint: "https://management.azure.com", Method: "GET", Path: "/searchServices/{name}", Version: "2025-05-01"}, InputSchema: map[string]any{"properties": map[string]any{"name": property}}}
	for _, name := range []string{"ab", "ab-c", "22", strings.Repeat("a", 60)} {
		if _, err := BindREST(op, map[string]any{"name": name}); err != nil {
			t.Fatalf("valid Search name %q: %v", name, err)
		}
	}
	for _, name := range []string{"a", "A1", "-ab", "ab-", "ab--c", "a-b", "ab_c", "中文", strings.Repeat("a", 61)} {
		if _, err := BindREST(op, map[string]any{"name": name}); err == nil {
			t.Fatalf("invalid Search name accepted: %q", name)
		}
	}
	if property["pattern"] != pattern {
		t.Fatal("binding changed the native schema")
	}
}

func TestRESTBindingEscapesNamesAndRejectsMalformedInput(t *testing.T) {
	op := Operation{ID: "service.resources.delete", Call: &OperationCall{Style: "google-rest", Endpoint: "https://run.googleapis.com", Method: "DELETE", Path: "/v2/{name}", RawPathParameters: []string{"name"}}, InputSchema: map[string]any{"properties": map[string]any{
		"name": map[string]any{"type": "string", "location": "path", "required": true, "pattern": "^projects/[^/]+/locations/[^/]+/services/[^/]+$"},
		"etag": map[string]any{"type": "string", "location": "query"},
		"body": map[string]any{"type": "object"},
	}}}
	bound, err := BindREST(op, map[string]any{"name": "projects/p/locations/us/services/service?x=1", "etag": "quoted & tag", "body": map[string]any{"enabled": true}})
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(bound.URL)
	if err != nil || u.Query().Get("x") != "" || u.Query().Get("etag") != "quoted & tag" || u.Host != "run.googleapis.com" {
		t.Fatalf("parameter escaped its boundary: %s", bound.URL)
	}
	var body map[string]any
	if json.Unmarshal(bound.Body, &body) != nil || body["enabled"] != true {
		t.Fatalf("body lost: %s", bound.Body)
	}
	for _, params := range []map[string]any{
		{}, {"name": 23}, {"name": "projects/p/locations/us/services/.."},
		{"name": "projects/p/locations/us/services/../else"}, {"name": "projects/p/locations/us/services/"},
		{"name": "projects/p/locations/us/jobs/job"}, {"name": "projects/p/locations/us/services/s", "unknown": true},
		{"name": "projects/p/locations/us/services/s", "etag": map[string]any{"x": "y"}},
	} {
		if _, err := BindREST(op, params); err == nil {
			t.Errorf("accepted malformed parameters: %#v", params)
		}
	}
	for _, endpoint := range []string{"http://run.googleapis.com", "https://run.googleapis.com:8443", "https://run.googleapis.com.evil.example", "https://user:pass@run.googleapis.com", "https://run.googleapis.com?host=evil"} {
		op.Call.Endpoint = endpoint
		if _, err := BindREST(op, map[string]any{"name": "projects/p/locations/us/services/s"}); err == nil {
			t.Errorf("accepted %s", endpoint)
		}
	}
}

func TestRESTNativeHeaderAndAzureNameValidation(t *testing.T) {
	operation := Operation{ID: "Azure.fixture", Call: &OperationCall{Style: "azure-rest", Endpoint: "https://management.azure.com", Method: "DELETE", Path: "/subscriptions/{subscriptionId}/servers/{name}", Version: "2024-01-01"}, InputSchema: map[string]any{"properties": map[string]any{
		"subscriptionId": map[string]any{"in": "path"}, "name": map[string]any{"in": "path", "pattern": "^[a-z0-9][-a-z0-9]*(?<!-)$"}, "If-Match": map[string]any{"in": "header"}, "api-version": map[string]any{"in": "query"},
	}}}
	params := map[string]any{"subscriptionId": "sub", "name": "valid-name", "If-Match": "etag-1"}
	request, err := BindREST(operation, params)
	if err != nil || request.Headers["If-Match"] != "etag-1" {
		t.Fatalf("native header=%+v %v", request, err)
	}
	params["name"] = "trailing-"
	if _, err := BindREST(operation, params); err == nil {
		t.Error("Azure MySQL trailing hyphen accepted")
	}
	params["name"] = "valid"
	params["If-Match"] = "unsafe\r\nAuthorization: injected"
	if _, err := BindREST(operation, params); err == nil {
		t.Error("header injection accepted")
	}
}

func TestRESTMonitorWorkspaceNativeNamePattern(t *testing.T) {
	op := Operation{ID: "Azure.Microsoft.Monitor.AzureMonitorWorkspaces_Get", Call: &OperationCall{Style: "azure-rest", Endpoint: "https://management.azure.com", Method: "GET", Path: "/accounts/{name}", Version: "2023-04-03"}, InputSchema: map[string]any{"properties": map[string]any{"name": map[string]any{"in": "path", "pattern": "^(?!-)[a-zA-Z0-9-]+[^-]$"}}}}
	for name, valid := range map[string]bool{"workspace": true, "Workspace-2": true, "-workspace": false, "workspace-": false, "": false} {
		_, err := BindREST(op, map[string]any{"name": name})
		if (err == nil) != valid {
			t.Fatalf("native name %q: %v", name, err)
		}
	}
}

func TestRESTSQLVMNativeNamePattern(t *testing.T) {
	operation := Operation{ID: "Azure.Microsoft.SqlVirtualMachine.SqlVirtualMachines_Get", Call: &OperationCall{Style: "azure-rest", Method: "GET", Endpoint: "https://management.azure.com", Path: "/sqlVirtualMachines/{name}", Version: "2023-10-01"}, InputSchema: map[string]any{"properties": map[string]any{"name": map[string]any{"type": "string", "in": "path", "required": true, "pattern": `^((?!_)[^\\/"'\[\]:|<>+=;,?*@&]{1,64}(?<![.-]))$`}}}}
	for _, name := range []string{"vm", "v_m", "vm.1", "vm-1", strings.Repeat("a", 64)} {
		if _, err := BindREST(operation, map[string]any{"name": name}); err != nil {
			t.Errorf("valid SQL VM name %q: %v", name, err)
		}
	}
	for _, name := range []string{"", "_vm", "vm.", "vm-", "vm[name]", "vm@name", strings.Repeat("a", 65)} {
		if _, err := BindREST(operation, map[string]any{"name": name}); err == nil {
			t.Errorf("invalid SQL VM name accepted: %q", name)
		}
	}
}
