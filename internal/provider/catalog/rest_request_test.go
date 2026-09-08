package catalog

import (
	"encoding/json"
	"net/url"
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
