package gcp

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

type credentialFunc func(context.Context, asset.ConnectionID) (contracts.Credential, error)

func (f credentialFunc) Resolve(ctx context.Context, id asset.ConnectionID) (contracts.Credential, error) {
	return f(ctx, id)
}

func protocolRuntime(t *testing.T, product roundTripFunc) *Runtime {
	t.Helper()
	credential, _ := testCredential(t)
	r, err := NewRuntime(credentialFunc(func(_ context.Context, id asset.ConnectionID) (contracts.Credential, error) {
		if id != "connection" {
			t.Fatalf("wrong credential connection: %q", id)
		}
		return credential, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	r.transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.String() == tokenURL {
			return apiResponse(request, 200, `{"access_token":"token", "expires_in":3600, "token_type":"Bearer"}`), nil
		}
		if request.Header.Get("Authorization") != "Bearer token" {
			t.Fatal("product request was not authenticated")
		}
		if request.URL.String() == "https://cloudresourcemanager.googleapis.com/v3/projects/sample-project" {
			return apiResponse(request, 200, `{"name":"projects/123456", "projectId":"sample-project", "state":"ACTIVE", "displayName":"Sample project"}`), nil
		}
		return product(request)
	})
	return r
}

func TestRuntimeInvokeUsesCatalogAndSelectedProject(t *testing.T) {
	var calls int
	r := protocolRuntime(t, func(request *http.Request) (*http.Response, error) {
		calls++
		if request.Method != "GET" || request.URL.String() != "https://compute.googleapis.com/compute/v1/projects/sample-project/zones/us-central1-a/instances/web" {
			t.Fatalf("wrong catalog request: %s %s", request.Method, request.URL)
		}
		return apiResponse(request, 200, `{"name":"web", "metadata":{"items":[{"key":"startup-script", "value":"PRIVATE_VALUE"}]}}`), nil
	})
	invocation := contracts.Invocation{ConnectionID: "connection", Operation: "compute.instances.get", Parameters: map[string]any{"zone": "us-central1-a", "instance": "web"}}
	result, err := r.Invoke(context.Background(), invocation)
	if err != nil || result.RequestID != "request-123" || result.Data["name"] != "web" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	payload, _ := json.Marshal(result)
	if strings.Contains(string(payload), "PRIVATE_VALUE") {
		t.Fatal("Invoke returned secret metadata")
	}
	invocation.Parameters["project"] = "foreign-project"
	if _, err := r.Invoke(context.Background(), invocation); err == nil {
		t.Fatal("cross-project invocation accepted")
	}
	invocation.Operation = "gcp.resources.delete"
	if _, err := r.Invoke(context.Background(), invocation); err == nil {
		t.Fatal("invented operation accepted")
	}
	invocation.Operation = "run.projects.locations.services.delete"
	invocation.Parameters = map[string]any{"name": "projects/foreign-project/locations/us-central1/services/web"}
	if _, err := r.Invoke(context.Background(), invocation); err == nil {
		t.Fatal("cross-project full name accepted")
	}
	if calls != 1 {
		t.Fatalf("invalid calls reached product API: %d", calls)
	}
}

func TestInventorySnapshotPagingProjectScopeAndSanitization(t *testing.T) {
	first, err := os.ReadFile("fixtures/inventory-page-1.json")
	if err != nil {
		t.Fatal(err)
	}
	second, err := os.ReadFile("fixtures/inventory-page-2.json")
	if err != nil {
		t.Fatal(err)
	}
	r := protocolRuntime(t, func(request *http.Request) (*http.Response, error) {
		if request.URL.Host != "cloudasset.googleapis.com" || request.URL.Path != "/v1/projects/sample-project/assets" {
			t.Fatalf("wrong inventory endpoint %s", request.URL)
		}
		query := request.URL.Query()
		if query.Get("contentType") != "RESOURCE" || query.Get("pageSize") != "100" {
			t.Fatalf("wrong inventory parameters: %v", query)
		}
		body := first
		if query.Get("pageToken") == "page-2" {
			if query.Get("readTime") != "2026-09-08T10:00:00Z" {
				t.Fatal("pagination lost its snapshot")
			}
			body = second
		}
		return apiResponse(request, 200, string(body)), nil
	})
	request := contracts.InventoryRequest{ConnectionID: "connection", Scope: asset.Scope{Kind: asset.ScopeProject, NativeID: "sample-project"}, Limit: 100}
	page, err := r.List(context.Background(), request)
	if err != nil || len(page.Items) != 2 || page.Complete || page.NextCursor == "" || page.RequestID != "request-123" {
		t.Fatalf("first page=%+v err=%v", page, err)
	}
	if page.Items[0].Scope.Kind != asset.ScopeGlobal || page.Items[1].Scope.Location != "us-central1" {
		t.Fatal("project scan did not preserve native resource scopes")
	}
	vm := page.Items[1]
	if vm.NativeID != "//compute.googleapis.com/projects/sample-project/zones/us-central1-a/instances/web" || vm.Normalized["vpc_id"] != "//compute.googleapis.com/projects/sample-project/global/networks/main" || len(vm.NetworkReferences) != 3 {
		t.Fatalf("identity/network references lost: %+v", vm)
	}
	payload, _ := json.Marshal(page)
	if strings.Contains(string(payload), "PRIVATE_SCRIPT_VALUE") {
		t.Fatal("inventory persisted metadata secret")
	}
	request.Cursor = page.NextCursor
	page, err = r.List(context.Background(), request)
	if err != nil || !page.Complete || page.NextCursor != "" || len(page.Items) != 1 || page.RequestID == "" {
		t.Fatalf("terminal page=%+v err=%v", page, err)
	}
}

func TestInventoryRejectsPaginationSnapshotDrift(t *testing.T) {
	c := &client{project: "sample-project", http: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Query().Get("pageToken") == "" {
			return apiResponse(request, 200, `{"readTime":"2026-09-08T10:00:00Z", "nextPageToken":"second"}`), nil
		}
		return apiResponse(request, 200, `{"readTime":"2026-09-08T10:01:00Z"}`), nil
	})}}
	first, err := c.assetPageResult(context.Background(), "", "", 100)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.assetPageResult(context.Background(), first.NextToken, "", 100); err == nil {
		t.Fatal("changed snapshot accepted as authoritative pagination")
	}
}

func TestRegionalBackendReferenceUsesCAIListType(t *testing.T) {
	c := &client{project: "sample-project", number: "123456"}
	refs := references(c, map[string]any{"backendService": "https://www.googleapis.com/compute/v1/projects/sample-project/regions/us-central1/backendServices/backend"})
	if len(refs["compute.googleapis.com/RegionBackendService"]) != 1 || len(refs["compute.googleapis.com/BackendService"]) != 0 {
		t.Fatalf("regional backend would not resolve against CAI inventory: %+v", refs)
	}
}
