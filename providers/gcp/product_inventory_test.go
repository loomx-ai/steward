package gcp

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func productRequest(r *Runtime, nativeType, region string) contracts.InventoryRequest {
	kind := r.resourceKind(nativeType)
	scope := asset.Scope{Kind: asset.ScopeRegion, NativeID: region}
	if region == "global" {
		scope.Kind = asset.ScopeGlobal
	}
	if region == "project" {
		scope.Kind = asset.ScopeProject
		scope.NativeID = "sample-project"
	}
	return contracts.InventoryRequest{ConnectionID: "connection", Source: productInventorySource, ResourceKind: &kind, Scope: scope, Limit: 100}
}

// Explicit service responses exercise distinct API shapes and identity rules.
// Expected URLs and names are independent of the generated catalog.
func TestProductListsUseNativeAPIsAndCanonicalIdentities(t *testing.T) {
	tests := []struct{ kind, region, path, body, id string }{
		{"compute.googleapis.com/InstanceGroupManager", "us-central1", "/compute/v1/projects/sample-project/aggregated/instanceGroupManagers", `{"items":{"zones/us-central1-a":{"instanceGroupManagers":[{"name":"workers"}]}}}`, "projects/sample-project/zones/us-central1-a/instanceGroupManagers/workers"},
		{"compute.googleapis.com/InstanceGroupManager", "europe-west1", "/compute/v1/projects/sample-project/aggregated/instanceGroupManagers", `{"items":{"regions/europe-west1":{"instanceGroupManagers":[{"name":"regional"}]},"zones/us-central1-a":{"instanceGroupManagers":[{"name":"other"}]}}}`, "projects/sample-project/regions/europe-west1/instanceGroupManagers/regional"},
		{"compute.googleapis.com/InstanceGroup", "us-central1", "/compute/v1/projects/sample-project/aggregated/instanceGroups", `{"items":{"regions/us-central1":{"instanceGroups":[{"name":"workers"}]}}}`, "projects/sample-project/regions/us-central1/instanceGroups/workers"},
		{"compute.googleapis.com/Autoscaler", "us-central1", "/compute/v1/projects/sample-project/aggregated/autoscalers", `{"items":{"regions/us-central1":{"autoscalers":[{"name":"workers"}]}}}`, "projects/sample-project/regions/us-central1/autoscalers/workers"},
		{"compute.googleapis.com/Instance", "us-central1", "/compute/v1/projects/sample-project/aggregated/instances", `{"items":{"zones/us-central1-a":{"instances":[{"name":"web","zone":"https://www.googleapis.com/compute/v1/projects/sample-project/zones/us-central1-a"}]},"zones/europe-west1-b":{"instances":[{"name":"other"}]}}}`, "projects/sample-project/zones/us-central1-a/instances/web"},
		{"compute.googleapis.com/Disk", "us-central1", "/compute/v1/projects/sample-project/aggregated/disks", `{"items":{"zones/us-central1-a":{"disks":[{"name":"disk"}]},"regions/us-central1":{"disks":[{"name":"regional-disk"}]}}}`, "projects/sample-project/zones/us-central1-a/disks/disk"},
		{"compute.googleapis.com/RegionDisk", "us-central1", "/compute/v1/projects/sample-project/regions/us-central1/disks", `{"items":[{"name":"disk"}]}`, "projects/sample-project/regions/us-central1/disks/disk"},
		{"compute.googleapis.com/Snapshot", "global", "/compute/v1/projects/sample-project/global/snapshots", `{"items":[{"name":"snapshot"}]}`, "projects/sample-project/global/snapshots/snapshot"},
		{"compute.googleapis.com/Image", "global", "/compute/v1/projects/sample-project/global/images", `{"items":[{"name":"image"}]}`, "projects/sample-project/global/images/image"},
		{"compute.googleapis.com/InstanceTemplate", "global", "/compute/v1/projects/sample-project/aggregated/instanceTemplates", `{"items":{"global":{"instanceTemplates":[{"name":"template"}]},"regions/us-central1":{"instanceTemplates":[{"name":"regional"}]}}}`, "projects/sample-project/global/instanceTemplates/template"},
		{"compute.googleapis.com/Network", "global", "/compute/v1/projects/sample-project/global/networks", `{"items":[{"name":"main"}]}`, "projects/sample-project/global/networks/main"},
		{"compute.googleapis.com/Subnetwork", "us-central1", "/compute/v1/projects/sample-project/regions/us-central1/subnetworks", `{"items":[{"name":"private","network":"https://www.googleapis.com/compute/v1/projects/sample-project/global/networks/main"}]}`, "projects/sample-project/regions/us-central1/subnetworks/private"},
		{"compute.googleapis.com/Firewall", "global", "/compute/v1/projects/sample-project/global/firewalls", `{"items":[{"name":"firewall"}]}`, "projects/sample-project/global/firewalls/firewall"},
		{"compute.googleapis.com/Route", "global", "/compute/v1/projects/sample-project/global/routes", `{"items":[{"name":"route"}]}`, "projects/sample-project/global/routes/route"},
		{"compute.googleapis.com/Router", "us-central1", "/compute/v1/projects/sample-project/regions/us-central1/routers", `{"items":[{"name":"router"}]}`, "projects/sample-project/regions/us-central1/routers/router"},
		{"compute.googleapis.com/Address", "us-central1", "/compute/v1/projects/sample-project/regions/us-central1/addresses", `{"items":[{"name":"ip"}]}`, "projects/sample-project/regions/us-central1/addresses/ip"},
		{"compute.googleapis.com/GlobalAddress", "global", "/compute/v1/projects/sample-project/global/addresses", `{"items":[{"name":"ip"}]}`, "projects/sample-project/global/addresses/ip"},
		{"compute.googleapis.com/ForwardingRule", "us-central1", "/compute/v1/projects/sample-project/regions/us-central1/forwardingRules", `{"items":[{"name":"rule"}]}`, "projects/sample-project/regions/us-central1/forwardingRules/rule"},
		{"compute.googleapis.com/GlobalForwardingRule", "global", "/compute/v1/projects/sample-project/global/forwardingRules", `{"items":[{"name":"rule"}]}`, "projects/sample-project/global/forwardingRules/rule"},
		{"compute.googleapis.com/BackendService", "global", "/compute/v1/projects/sample-project/global/backendServices", `{"items":[{"name":"backend"}]}`, "projects/sample-project/global/backendServices/backend"},
		{"compute.googleapis.com/RegionBackendService", "us-central1", "/compute/v1/projects/sample-project/regions/us-central1/backendServices", `{"items":[{"name":"backend"}]}`, "projects/sample-project/regions/us-central1/backendServices/backend"},
		{"compute.googleapis.com/HealthCheck", "us-central1", "/compute/v1/projects/sample-project/aggregated/healthChecks", `{"items":{"regions/us-central1":{"healthChecks":[{"name":"check"}]},"global":{"healthChecks":[{"name":"global"}]}}}`, "projects/sample-project/regions/us-central1/healthChecks/check"},
		{"compute.googleapis.com/UrlMap", "global", "/compute/v1/projects/sample-project/aggregated/urlMaps", `{"items":{"global":{"urlMaps":[{"name":"map"}]}}}`, "projects/sample-project/global/urlMaps/map"},
		{"compute.googleapis.com/TargetHttpProxy", "global", "/compute/v1/projects/sample-project/aggregated/targetHttpProxies", `{"items":{"global":{"targetHttpProxies":[{"name":"proxy"}]}}}`, "projects/sample-project/global/targetHttpProxies/proxy"},
		{"compute.googleapis.com/TargetHttpsProxy", "us-central1", "/compute/v1/projects/sample-project/aggregated/targetHttpsProxies", `{"items":{"regions/us-central1":{"targetHttpsProxies":[{"name":"proxy"}]}}}`, "projects/sample-project/regions/us-central1/targetHttpsProxies/proxy"},
		{"compute.googleapis.com/SslCertificate", "global", "/compute/v1/projects/sample-project/aggregated/sslCertificates", `{"items":{"global":{"sslCertificates":[{"name":"cert","privateKey":"PRIVATE_KEY"}]}}}`, "projects/sample-project/global/sslCertificates/cert"},
		{"storage.googleapis.com/Bucket", "global", "/storage/v1/b", `{"items":[{"name":"sample-bucket","projectNumber":"123456","location":"US"}]}`, "sample-bucket"},
		{"pubsub.googleapis.com/Topic", "global", "/v1/projects/sample-project/topics", `{"topics":[{"name":"projects/sample-project/topics/events"}]}`, "projects/sample-project/topics/events"},
		{"pubsub.googleapis.com/Subscription", "global", "/v1/projects/sample-project/subscriptions", `{"subscriptions":[{"name":"projects/sample-project/subscriptions/worker","topic":"projects/sample-project/topics/events"}]}`, "projects/sample-project/subscriptions/worker"},
		{"sqladmin.googleapis.com/Instance", "us-central1", "/sql/v1beta4/projects/sample-project/instances", `{"items":[{"name":"db","region":"us-central1"}]}`, "projects/sample-project/instances/db"},
		{"container.googleapis.com/Cluster", "us-central1", "/v1/projects/sample-project/locations/-/clusters", `{"clusters":[{"name":"cluster","location":"us-central1-a","masterAuth":{"password":"PRIVATE_KEY"}}]}`, "projects/sample-project/locations/us-central1-a/clusters/cluster"},
		{"run.googleapis.com/Service", "us-central1", "/v2/projects/sample-project/locations/us-central1/services", `{"services":[{"name":"projects/123456/locations/us-central1/services/web"}]}`, "projects/sample-project/locations/us-central1/services/web"},
		{"artifactregistry.googleapis.com/Repository", "us-central1", "/v1/projects/sample-project/locations/us-central1/repositories", `{"repositories":[{"name":"projects/sample-project/locations/us-central1/repositories/repo"}]}`, "projects/sample-project/locations/us-central1/repositories/repo"},
		{"secretmanager.googleapis.com/Secret", "global", "/v1/projects/sample-project/secrets", `{"secrets":[{"name":"projects/123456/secrets/key"}]}`, "projects/sample-project/secrets/key"},
		{"secretmanager.googleapis.com/Secret", "us-central1", "/v1/projects/sample-project/locations/us-central1/secrets", `{"secrets":[{"name":"projects/sample-project/locations/us-central1/secrets/key"}]}`, "projects/sample-project/locations/us-central1/secrets/key"},
	}
	for _, test := range tests {
		t.Run(test.kind+"/"+test.region, func(t *testing.T) {
			calls := 0
			r := protocolRuntime(t, func(request *http.Request) (*http.Response, error) {
				calls++
				if request.Method != "GET" || request.URL.Path != test.path || request.URL.Host == "cloudasset.googleapis.com" {
					t.Fatalf("wrong native list request %s %s", request.Method, request.URL)
				}
				return apiResponse(request, 200, test.body), nil
			})
			page, err := r.List(context.Background(), productRequest(r, test.kind, test.region))
			if err != nil || !page.Complete || page.RequestID != "request-123" || len(page.Items) != 1 || calls != 1 {
				t.Fatalf("page=%+v calls=%d err=%v", page, calls, err)
			}
			item := page.Items[0]
			if item.NativeID != "//"+strings.Split(test.kind, "/")[0]+"/"+test.id || item.Normalized["_inventory_source"] != productInventorySource {
				t.Fatalf("wrong identity/source: %+v", item)
			}
			raw, _ := json.Marshal(page)
			if strings.Contains(string(raw), "PRIVATE_KEY") {
				t.Fatal("product list persisted a secret")
			}
		})
	}
}

func TestProductPaginationBindsScanAndRejectsCycles(t *testing.T) {
	calls := 0
	r := protocolRuntime(t, func(request *http.Request) (*http.Response, error) {
		calls++
		if request.URL.Query().Get("pageSize") != "100" {
			t.Fatalf("wrong page size %s", request.URL)
		}
		token := request.URL.Query().Get("pageToken")
		next := "second"
		if token == "second" {
			next = "third"
		}
		return apiResponse(request, 200, `{"nextPageToken":"`+next+`"}`), nil
	})
	request := productRequest(r, "pubsub.googleapis.com/Topic", "global")
	first, err := r.List(context.Background(), request)
	if err != nil || first.Complete || first.NextCursor == "" {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	request.Cursor = first.NextCursor
	second, err := r.List(context.Background(), request)
	if err != nil || second.NextCursor == "" {
		t.Fatalf("second=%+v err=%v", second, err)
	}
	request.Cursor = second.NextCursor
	if _, err = r.List(context.Background(), request); err == nil {
		t.Fatal("multi-page token cycle accepted")
	}
	request = productRequest(r, "pubsub.googleapis.com/Subscription", "global")
	request.Cursor = first.NextCursor
	if _, err = r.List(context.Background(), request); err == nil {
		t.Fatal("cross-kind cursor accepted")
	}
	request = productRequest(r, "pubsub.googleapis.com/Topic", "project")
	request.Cursor = first.NextCursor
	if _, err = r.List(context.Background(), request); err == nil {
		t.Fatal("cross-scope cursor accepted")
	}
	if calls != 3 {
		t.Fatalf("invalid cursor reached API: %d calls", calls)
	}
}

func TestProductIncompleteAndMalformedResponsesCannotComplete(t *testing.T) {
	for _, body := range []string{
		`{"items":{"zones/us-central1-a":{"instances":[]}},"unreachables":["us-east1"]}`,
		`{"items":{"zones/us-central1-a":{"warning":{"code":"UNREACHABLE"}}}}`,
		`{"warning":{"code":"PARTIAL_SUCCESS"}}`,
		`{"missingZones":["us-central1-a"]}`, `{"unreachable":"bad"}`,
		`{"items":[]}`, `{"items":{"zones/us-central1-a":{"instances":{}}}}`,
		`{"items":{"zones/us-central1-a":{"instances":[null]}}}`,
		`{"items":{"zones/us-central1-a":{"instances":[{"status":"RUNNING"}]}}}`,
		`{"items":{"zones/us-central1-a":{"instances":[{"name":"vm","selfLink":"https://compute.googleapis.com/compute/v1/projects/foreign-project/zones/us-central1-a/instances/vm"}]}}}`,
		`{"items":{"zones/us-central1-a":{"instances":[{"name":"vm"},{"name":"vm"}]}}}`,
		`{"nextPageToken":7}`,
	} {
		t.Run(body, func(t *testing.T) {
			r := protocolRuntime(t, func(request *http.Request) (*http.Response, error) { return apiResponse(request, 200, body), nil })
			if page, err := r.List(context.Background(), productRequest(r, "compute.googleapis.com/Instance", "us-central1")); err == nil {
				t.Fatalf("invalid response marked complete: %+v", page)
			}
		})
	}
	for _, body := range []string{`{}`, `{"items":{}}`, `{"warning":{"code":"NO_RESULTS_ON_PAGE"}}`, `{"items":{"zones/us-central1-a":{"warning":{"code":"NO_RESULTS_ON_PAGE"}}}}`} {
		r := protocolRuntime(t, func(request *http.Request) (*http.Response, error) { return apiResponse(request, 200, body), nil })
		if page, err := r.List(context.Background(), productRequest(r, "compute.googleapis.com/Instance", "us-central1")); err != nil || !page.Complete || len(page.Items) != 0 {
			t.Fatalf("valid empty page=%+v err=%v", page, err)
		}
	}
}

func TestBroadCAIDoesNotOverwriteKnownProductKinds(t *testing.T) {
	r := protocolRuntime(t, func(request *http.Request) (*http.Response, error) {
		return apiResponse(request, 200, `{"readTime":"2026-09-08T10:00:00Z","assets":[{"name":"//compute.googleapis.com/projects/sample-project/global/networks/stale","assetType":"compute.googleapis.com/Network"},{"name":"//dataform.googleapis.com/projects/sample-project/locations/us-central1/folders/stale","assetType":"dataform.googleapis.com/Folder"},{"name":"//dataform.googleapis.com/projects/sample-project/locations/us-central1/teamFolders/stale","assetType":"dataform.googleapis.com/TeamFolder"},{"name":"//example.googleapis.com/projects/sample-project/things/unknown","assetType":"example.googleapis.com/Thing","resource":{"data":{"name":"unknown"}}}]}`), nil
	})
	for _, source := range r.InventorySources() {
		if source.Name == inventorySource && source.AuthoritativeDefault {
			t.Fatal("broad CAI coverage could close resources owned by product shards")
		}
	}
	page, err := r.List(context.Background(), contracts.InventoryRequest{ConnectionID: "connection", Source: inventorySource, Scope: asset.Scope{Kind: asset.ScopeProject, NativeID: "sample-project"}})
	if err != nil || len(page.Items) != 1 || page.Items[0].NativeType != "example.googleapis.com/Thing" {
		t.Fatalf("broad inventory=%+v err=%v", page, err)
	}
}

func TestNetworkSearchUsesFreshComputeAndBindsFilters(t *testing.T) {
	calls := 0
	r := protocolRuntime(t, func(request *http.Request) (*http.Response, error) {
		calls++
		if request.URL.Path != "/compute/v1/projects/sample-project/regions/us-central1/subnetworks" {
			t.Fatalf("network search did not use native regional list: %s", request.URL)
		}
		return apiResponse(request, 200, `{"items":[{"name":"private","network":"https://www.googleapis.com/compute/v1/projects/sample-project/global/networks/main"},{"name":"public","network":"https://www.googleapis.com/compute/v1/projects/sample-project/global/networks/other"}],"nextPageToken":"next"}`), nil
	})
	query := contracts.NetworkTargetQuery{ConnectionID: "connection", Kind: asset.ScanTargetVSwitch, RegionID: "us-central1", ParentNativeID: "//compute.googleapis.com/projects/sample-project/global/networks/main", Query: "PRIVATE"}
	page, err := r.SearchNetworkTargets(context.Background(), query)
	if err != nil || len(page.Items) != 1 || page.Items[0].Name != "private" || page.NextCursor == "" {
		t.Fatalf("page=%+v err=%v", page, err)
	}
	query.Cursor = page.NextCursor
	query.Query = "public"
	if _, err = r.SearchNetworkTargets(context.Background(), query); err == nil {
		t.Fatal("changed search filter reused cursor")
	}
	if calls != 1 {
		t.Fatal("invalid cursor reached provider")
	}
}
