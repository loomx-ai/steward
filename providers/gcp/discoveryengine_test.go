package gcp

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/core/graph"
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

const (
	deCollection   = "projects/sample-project/locations/global/collections/default_collection"
	deStore        = deCollection + "/dataStores/articles"
	deEngine       = deCollection + "/engines/search"
	deUSCollection = "projects/sample-project/locations/us/collections/connector-real"
	deUSStore      = deUSCollection + "/dataStores/articles"
)

type discoveryScenario struct {
	resources  map[string]map[string]any
	operations map[string]map[string]any
	mutations  []string
	calls      []string
	emptyPage  bool
	handle     func(*http.Request) (*http.Response, bool)
}

func newDiscoveryScenario(t *testing.T) *discoveryScenario {
	t.Helper()
	raw, err := os.ReadFile("fixtures/discoveryengine/resources.json")
	if err != nil {
		t.Fatal(err)
	}
	s := &discoveryScenario{operations: map[string]map[string]any{}}
	if err := json.Unmarshal(raw, &s.resources); err != nil {
		t.Fatal(err)
	}
	return s
}

// These paths, versions, parent collections and response fields are literal
// native fixtures, independent of the catalog and request binder being tested.
func (s *discoveryScenario) transport(t *testing.T) roundTripFunc {
	t.Helper()
	return func(req *http.Request) (*http.Response, error) {
		s.calls = append(s.calls, req.Method+" "+req.URL.String())
		if s.handle != nil {
			if response, ok := s.handle(req); ok {
				return response, nil
			}
		}
		respond := func(code int, data any) (*http.Response, error) {
			raw, _ := json.Marshal(data)
			return apiResponse(req, code, string(raw)), nil
		}
		parts := strings.Split(strings.TrimPrefix(req.URL.Path, "/"), "/")
		if len(parts) < 6 || parts[1] != "projects" || parts[2] != "sample-project" || parts[3] != "locations" {
			t.Fatalf("unexpected Discovery Engine endpoint %s", req.URL)
		}
		host := "discoveryengine.googleapis.com"
		if parts[4] != "global" {
			host = parts[4] + "-" + host
		}
		if req.URL.Host != host || !slices.Contains([]string{"global", "us", "eu"}, parts[4]) {
			t.Fatalf("wrong native multi-region endpoint %s", req.URL)
		}
		name := strings.Join(parts[1:], "/")
		collection := last(name)
		if req.Method == "GET" && strings.HasSuffix(name, "/siteSearchEngine/sitemaps:fetch") {
			return respond(200, map[string]any{})
		}
		version := "v1"
		if len(parts) == 6 || len(parts) == 7 || (len(parts) > 7 && parts[7] == "operations") || collection == "branches" || (len(parts) >= 10 && parts[len(parts)-2] == "branches") {
			version = "v1alpha"
		}
		if parts[0] != version {
			t.Fatalf("wrong API version for %s; want %s", req.URL, version)
		}
		if req.Method == "DELETE" {
			if len(req.URL.Query()) > 0 {
				t.Fatalf("invented delete parameters %s", req.URL)
			}
			if req.Body != nil {
				raw, _ := io.ReadAll(req.Body)
				if len(raw) > 0 {
					t.Fatalf("invented native delete body %s", raw)
				}
			}
			s.mutations = append(s.mutations, "DELETE "+name)
			if s.resources[name] == nil {
				return respond(404, map[string]any{})
			}
			kind := parts[len(parts)-2]
			if slices.Contains([]string{"collections", "dataStores", "engines", "schemas", "targetSites"}, kind) {
				op := name + "/operations/delete-1"
				if kind == "targetSites" {
					op = name[:strings.LastIndex(name, "/")] + "/operations/delete-1"
				}
				title := map[string]string{"collections": "Collection", "dataStores": "DataStore", "engines": "Engine", "schemas": "Schema", "targetSites": "TargetSite"}[kind]
				s.operations[op] = map[string]any{"name": op, "metadata": map[string]any{"@type": "type.googleapis.com/google.cloud.discoveryengine." + version + ".Delete" + title + "Metadata"}}
				return respond(200, s.operations[op])
			}
			if !slices.Contains([]string{"controls", "servingConfigs", "sessions", "conversations", "assistants", "documents"}, kind) {
				t.Fatalf("invented DELETE %s", req.URL)
			}
			delete(s.resources, name)
			return respond(200, map[string]any{})
		}
		if req.Method != "GET" {
			t.Fatalf("invented method %s %s", req.Method, req.URL)
		}
		if strings.Contains(name, "/operations/") {
			if operation := s.operations[name]; operation != nil {
				return respond(200, operation)
			}
			return respond(404, map[string]any{})
		}
		lists := []string{"collections", "dataStores", "engines", "schemas", "controls", "servingConfigs", "sessions", "conversations", "assistants", "branches", "documents", "targetSites"}
		if slices.Contains(lists, collection) {
			if collection == "engines" || collection == "branches" {
				if len(req.URL.Query()) > 0 {
					t.Fatalf("unsupported list pagination %s", req.URL)
				}
			}
			if s.emptyPage && collection != "engines" && collection != "branches" && req.URL.Query().Get("pageToken") == "" {
				return respond(200, map[string]any{"nextPageToken": "second-page"})
			}
			if token := req.URL.Query().Get("pageToken"); token != "" && token != "second-page" {
				t.Fatalf("unexpected token %s", req.URL)
			}
			var records []any
			var names []string
			for id := range s.resources {
				names = append(names, id)
			}
			sort.Strings(names)
			for _, id := range names {
				if strings.HasPrefix(id, name+"/") && !strings.Contains(strings.TrimPrefix(id, name+"/"), "/") {
					records = append(records, s.resources[id])
				}
			}
			return respond(200, map[string]any{collection: records})
		}
		if data := s.resources[name]; data != nil {
			return respond(200, data)
		}
		return respond(404, map[string]any{})
	}
}

func (s *discoveryScenario) inventory(t *testing.T, r *Runtime) []asset.Asset {
	t.Helper()
	var result []asset.Asset
	for _, suffix := range []string{"Collection", "DataStore", "Engine", "Schema", "Control", "ServingConfig", "Session", "Conversation", "Assistant", "Branch", "Document", "SiteSearchEngine", "TargetSite"} {
		req := productRequest(r, "discoveryengine.googleapis.com/"+suffix, "project")
		for attempts := 0; ; attempts++ {
			batch, err := r.List(context.Background(), req)
			if err != nil || attempts > 100 {
				t.Fatalf("%s inventory: %+v %v", suffix, batch, err)
			}
			for _, item := range batch.Items {
				capabilities := asset.CapabilitySet{}
				if item.Actionable != nil && *item.Actionable {
					capabilities = append(capabilities, asset.CapabilityActionable)
				}
				result = append(result, asset.Asset{ID: asset.AssetID(item.NativeID), Identity: asset.Identity{Provider: asset.ProviderGCP, ConnectionID: "connection", Partition: "google-cloud", NativeType: item.NativeType, NativeID: item.NativeID}, Normalized: item.Normalized, Capabilities: capabilities, Location: item.Location})
			}
			if batch.Complete {
				break
			}
			req.Cursor = batch.NextCursor
		}
	}
	return result
}

func discoveryReviewed(t *testing.T, s *discoveryScenario, root string) (*Runtime, []asset.Asset, plan.Input, contracts.ActionRequest) {
	t.Helper()
	r := protocolRuntime(t, s.transport(t))
	assets := s.inventory(t, r)
	hook, err := r.ServiceLifecycle(context.Background(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	contribution, err := hook.Contribute(context.Background(), "scope", assets)
	if err != nil || len(contribution.Unresolved) > 0 {
		t.Fatalf("Discovery Engine contribution %+v %v", contribution, err)
	}
	var relations []graph.Relationship
	for _, a := range assets {
		for _, target := range []string{discoveryDataStoreType, discoveryHost + "/Control"} {
			refs, _ := discoveryStrings(a.Normalized[referenceKey(target)])
			for _, id := range refs {
				relations = append(relations, graph.Relationship{SourceAssetID: a.ID, TargetAssetID: asset.AssetID(id), Type: graph.RelationshipDependsOn, Confidence: 1})
			}
		}
	}
	value := batchAsset(assets, root)
	input := plan.Input{Assets: assets, ResolvedAssetIDs: []asset.AssetID{value.ID}, LifecycleBindings: contribution.Bindings, Relationships: append(contribution.Relationships, relations...)}
	result, err := plan.Solve(input)
	if err != nil || len(result.Blockers) > 0 {
		t.Fatalf("Discovery Engine plan %+v %v", result, err)
	}
	return r, assets, input, dataformRequest(t, result, assets, value)
}

func TestDiscoveryEngineInventoryPlanDeleteAndRestart(t *testing.T) {
	s := newDiscoveryScenario(t)
	s.emptyPage = true
	r, assets, input, request := discoveryReviewed(t, s, deEngine)
	if len(assets) != 21 || len(request.LifecycleImpacts) != 5 {
		t.Fatalf("wrong inventory/impacts %d/%d", len(assets), len(request.LifecycleImpacts))
	}
	raw, _ := json.Marshal(assets)
	if strings.Contains(string(raw), "DISCOVERY_PRIVATE_") {
		t.Fatalf("customer data escaped %s", raw)
	}
	refs, _ := discoveryStrings(batchAsset(assets, deEngine).Normalized[referenceKey(discoveryDataStoreType)])
	if !slices.Contains(refs, "//"+discoveryHost+"/"+deStore) {
		t.Fatal("missing data store dependency")
	}
	refs, _ = discoveryStrings(batchAsset(assets, deStore+"/branches/0/documents/document-1").Normalized[referenceKey(discoveryHost+"/Schema")])
	if !slices.Contains(refs, "//"+discoveryHost+"/"+deStore+"/schemas/default_schema") {
		t.Fatal("missing document schema dependency")
	}
	// Existing native control/serving configuration kinds remain independently actionable.
	input.ResolvedAssetIDs = []asset.AssetID{batchAsset(assets, deEngine+"/sessions/session-1").ID}
	standalone, err := plan.Solve(input)
	if err != nil || len(standalone.Blockers) > 0 {
		t.Fatalf("standalone session plan %+v %v", standalone, err)
	}
	driver, err := r.ResolveAction(context.Background(), "connection", request.Asset)
	if err != nil {
		t.Fatal(err)
	}
	result, err := driver.Execute(context.Background(), request)
	if err != nil || result.ProviderOperationID == "" || len(s.mutations) != 1 {
		t.Fatalf("execute %+v %v %v", result, err, s.mutations)
	}
	raw, _ = json.Marshal(result)
	var restored contracts.ActionResult
	if err := json.Unmarshal(raw, &restored); err != nil {
		t.Fatal(err)
	}
	r = protocolRuntime(t, s.transport(t))
	driver, err = r.ResolveAction(context.Background(), "connection", request.Asset)
	if err != nil {
		t.Fatal(err)
	}
	wait, err := driver.Wait(context.Background(), request, restored)
	if err != nil || wait.Done {
		t.Fatalf("premature completion %+v %v", wait, err)
	}
	s.operations[deEngine+"/operations/delete-1"]["done"] = true
	delete(s.resources, deEngine)
	wait, err = driver.Wait(context.Background(), request, restored)
	if err != nil || wait.Done {
		t.Fatalf("root absence hid children %+v %v", wait, err)
	}
	for _, impact := range request.LifecycleImpacts {
		delete(s.resources, strings.TrimPrefix(impact.Asset.Identity.NativeID, "//"+discoveryHost+"/"))
	}
	wait, err = driver.Wait(context.Background(), request, restored)
	if err != nil || !wait.Done || s.resources[deStore] == nil || len(s.mutations) != 1 {
		t.Fatalf("final readback %+v %v", wait, err)
	}
}

func TestDiscoveryEngineDataStoreConstraintsAndNativeDeletion(t *testing.T) {
	s := newDiscoveryScenario(t)
	delete(s.resources, deEngine)
	// Remove its ordinary API-owned child records as the service would.
	for name := range s.resources {
		if strings.HasPrefix(name, deEngine+"/") {
			delete(s.resources, name)
		}
	}
	r, _, _, request := discoveryReviewed(t, s, deStore)
	driver, err := r.ResolveAction(context.Background(), "connection", request.Asset)
	if err != nil {
		t.Fatal(err)
	}
	if driver.(*action).DeletionCheckTimeout() < 72*time.Hour {
		t.Fatal("data store deletion can take days")
	}
	result, err := driver.Execute(context.Background(), request)
	if err != nil || result.ProviderOperationID == "" {
		t.Fatalf("delete data store %+v %v", result, err)
	}
	delete(s.operations, deStore+"/operations/delete-1")
	wait, err := driver.Wait(context.Background(), request, result)
	if err != nil || wait.Done {
		t.Fatalf("expired operation hid pending store %+v %v", wait, err)
	}
	for name := range s.resources {
		if name == deStore || strings.HasPrefix(name, deStore+"/") {
			delete(s.resources, name)
		}
	}
	wait, err = driver.Wait(context.Background(), request, result)
	if err != nil || !wait.Done {
		t.Fatalf("final store readback %+v %v", wait, err)
	}
}

func TestDiscoveryEngineRegionalScopesAndAliases(t *testing.T) {
	s := newDiscoveryScenario(t)
	r := protocolRuntime(t, s.transport(t))
	req := productRequest(r, discoveryDataStoreType, "us")
	page, err := r.List(context.Background(), req)
	if err != nil || len(page.Items) != 1 || page.Items[0].NativeID != "//"+discoveryHost+"/"+deUSStore || page.Items[0].Scope.NativeID != "us" {
		t.Fatalf("regional inventory %+v %v", page, err)
	}
	c, err := r.resolve(context.Background(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"projects/123456/locations/global/dataStores/articles", "https://discoveryengine.googleapis.com/v1/projects/123456/locations/global/dataStores/articles"} {
		id, err := c.discoveryID(discoveryDataStoreType, value)
		if err != nil || id != "//"+discoveryHost+"/"+deStore {
			t.Fatalf("native alias %s = %s %v", value, id, err)
		}
	}
	s.resources[deStore]["name"] = "projects/123456/locations/global/dataStores/articles"
	req = productRequest(r, discoveryDataStoreType, "global")
	page, err = r.List(context.Background(), req)
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("legacy data store name %+v %v", page, err)
	}
	raw, _ := json.Marshal(page)
	if strings.Contains(string(raw), "DISCOVERY_PRIVATE_") {
		t.Fatal("legacy name skipped redaction")
	}
	for _, operation := range []string{"discoveryengine.projects.locations.collections.list", "discoveryengine.projects.locations.collections.engines.list"} {
		parent := "projects/sample-project/locations/us"
		if strings.Contains(operation, ".engines.") {
			parent += "/collections/connector-real"
		}
		if _, err := r.Invoke(context.Background(), contracts.Invocation{ConnectionID: "connection", Operation: operation, Parameters: map[string]any{"parent": parent}}); err != nil {
			t.Fatalf("regional Invoke %v", err)
		}
	}
}

func TestDiscoveryEngineSelectedCollectionDeletesApplicationsBeforeDataStores(t *testing.T) {
	s := newDiscoveryScenario(t)
	r, assets, input, request := discoveryReviewed(t, s, deCollection)
	if len(request.PrerequisiteDeletions) != 2 {
		t.Fatalf("collection prerequisites %+v", request)
	}
	result, err := plan.Solve(input)
	if err != nil {
		t.Fatal(err)
	}
	order := map[asset.AssetID]int{}
	for i, step := range result.Steps {
		order[step.AssetID] = i
	}
	if order[batchAsset(assets, deEngine).ID] >= order[batchAsset(assets, deStore).ID] || order[batchAsset(assets, deStore).ID] >= order[request.Asset.ID] {
		t.Fatalf("native dependency order %+v", result.Steps)
	}
	driver, err := r.ResolveAction(context.Background(), "connection", request.Asset)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := driver.Execute(context.Background(), request); err == nil || len(s.mutations) > 0 {
		t.Fatal("collection bypassed remaining children")
	}
	for _, value := range []string{deEngine, deStore} {
		selected := batchAsset(assets, value)
		childRequest := dataformRequest(t, result, assets, selected)
		childDriver, err := r.ResolveAction(context.Background(), "connection", selected)
		if err != nil {
			t.Fatal(err)
		}
		action, err := childDriver.Execute(context.Background(), childRequest)
		if err != nil {
			t.Fatal(err)
		}
		s.operations[value+"/operations/delete-1"]["done"] = true
		for name := range s.resources {
			if name == value || strings.HasPrefix(name, value+"/") {
				delete(s.resources, name)
			}
		}
		wait, err := childDriver.Wait(context.Background(), childRequest, action)
		if err != nil || !wait.Done {
			t.Fatalf("child readback %+v %v", wait, err)
		}
	}
	action, err := driver.Execute(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	s.operations[deCollection+"/operations/delete-1"]["done"] = true
	delete(s.resources, deCollection)
	wait, err := driver.Wait(context.Background(), request, action)
	if err != nil || !wait.Done || len(s.mutations) != 3 {
		t.Fatalf("collection readback %+v %v %v", wait, err, s.mutations)
	}
}

func TestDiscoveryEngineNativeDataStoreInUseAndChildActions(t *testing.T) {
	for _, name := range []string{deStore, deEngine + "/sessions/session-1", deStore + "/branches/0/documents/document-1", deUSStore + "/siteSearchEngine/targetSites/site-1"} {
		t.Run(name, func(t *testing.T) {
			s := newDiscoveryScenario(t)
			r := protocolRuntime(t, s.transport(t))
			assets := s.inventory(t, r)
			value := batchAsset(assets, name)
			driver, err := r.ResolveAction(context.Background(), "connection", value)
			if err != nil {
				t.Fatal(err)
			}
			request := contracts.ActionRequest{Asset: value, Action: "delete"}
			result, err := driver.Execute(context.Background(), request)
			if name == deStore {
				if err == nil || len(s.mutations) > 0 {
					t.Fatal("deleted linked data store")
				}
				return
			}
			if err != nil || len(s.mutations) != 1 {
				t.Fatalf("native child deletion %+v %v", result, err)
			}
			if strings.Contains(name, "/targetSites/") {
				s.operations[name[:strings.LastIndex(name, "/")]+"/operations/delete-1"]["done"] = true
				delete(s.resources, name)
			}
			wait, err := driver.Wait(context.Background(), request, result)
			if err != nil || !wait.Done {
				t.Fatalf("native child readback %+v %v", wait, err)
			}
		})
	}
}

func TestDiscoveryEngineCollectionConnectorAndSitemapProofs(t *testing.T) {
	for _, mode := range []string{"connector_secret", "connector_entity", "connector_shape", "connector_error", "connector_scope", "sitemap_uri", "sitemap_scope", "sitemap_duplicate", "sitemap_permission"} {
		t.Run(mode, func(t *testing.T) {
			s := newDiscoveryScenario(t)
			s.resources[deUSCollection]["dataConnector"] = map[string]any{"name": deUSCollection + "/dataConnector", "dataSource": "sharepoint"}
			connector := map[string]any{"name": deUSCollection + "/dataConnector", "dataSource": "sharepoint", "params": map[string]any{"ordinary_key": "DISCOVERY_PRIVATE_CONNECTOR"}, "entities": []any{map[string]any{"entityName": "Content", "params": map[string]any{"ordinary": "DISCOVERY_PRIVATE_ENTITY"}}}}
			sitemap := map[string]any{"name": deUSStore + "/siteSearchEngine/sitemaps/map-1", "uri": "https://private.example/sitemap.xml?key=DISCOVERY_PRIVATE_SITEMAP", "createTime": "2026-08-01T12:00:00Z"}
			broken := false
			s.handle = func(req *http.Request) (*http.Response, bool) {
				if req.URL.Path == "/v1/"+deUSCollection+"/dataConnector" {
					data := cloneParameters(connector)
					if broken && mode == "connector_scope" {
						data["name"] = deCollection + "/dataConnector"
					}
					if broken && mode == "connector_shape" {
						data["entities"] = map[string]any{}
					}
					if broken && mode == "connector_error" {
						data["error"] = map[string]any{"code": 7}
					}
					return deJSON(req, 200, data), true
				}
				if req.URL.Path == "/v1/"+deUSStore+"/siteSearchEngine/sitemaps:fetch" {
					if broken && mode == "sitemap_permission" {
						return deJSON(req, 403, map[string]any{}), true
					}
					data := cloneParameters(sitemap)
					if broken && mode == "sitemap_scope" {
						data["name"] = deStore + "/siteSearchEngine/sitemaps/map-1"
					}
					items := []any{map[string]any{"sitemap": data}}
					if broken && mode == "sitemap_duplicate" {
						items = append(items, items[0])
					}
					return deJSON(req, 200, map[string]any{"sitemapsMetadata": items}), true
				}
				return nil, false
			}
			r := protocolRuntime(t, s.transport(t))
			assets := s.inventory(t, r)
			encoded, _ := json.Marshal(assets)
			if strings.Contains(string(encoded), "DISCOVERY_PRIVATE_") {
				t.Fatal("connector or sitemap escaped inventory")
			}
			value := batchAsset(assets, deUSStore+"/siteSearchEngine/targetSites/site-1")
			driver, err := r.ResolveAction(context.Background(), "connection", value)
			if err != nil {
				t.Fatal(err)
			}
			broken = true
			if mode == "connector_secret" {
				connector["params"] = map[string]any{"ordinary_key": "changed"}
			}
			if mode == "connector_entity" {
				object(array(connector["entities"])[0])["params"] = map[string]any{"ordinary": "changed"}
			}
			if mode == "sitemap_uri" {
				sitemap["uri"] = "https://changed.example/map.xml"
			}
			if _, err := driver.Execute(context.Background(), contracts.ActionRequest{Asset: value, Action: "delete"}); err == nil || len(s.mutations) > 0 {
				t.Fatalf("accepted changed %s", mode)
			}
		})
	}
}

func TestDiscoveryEngineRegionsAvailableBeforeCAIAndDoNotBecomeComputeRegions(t *testing.T) {
	r := protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
		switch req.URL.Host {
		case "compute.googleapis.com":
			if strings.HasSuffix(req.URL.Path, "/regions") {
				return apiResponse(req, 200, `{"items":[{"name":"us-central1","status":"UP"}]}`), nil
			}
		case "cloudasset.googleapis.com":
			return apiResponse(req, 200, `{"readTime":"2026-09-09T00:00:00Z"}`), nil
		case "dlp.googleapis.com":
			if strings.Contains(req.URL.Path, "/locations/us/") {
				return apiResponse(req, 200, `{}`), nil
			}
		case "logging.googleapis.com":
			if strings.Contains(req.URL.Path, "/locations/-/") {
				return apiResponse(req, 200, `{}`), nil
			}
		}
		t.Fatalf("unsupported regional request %s", req.URL)
		return nil, nil
	})
	regions, err := r.DiscoverRegions(context.Background(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, region := range regions {
		names = append(names, region.RegionID)
	}
	if !slices.Equal(names, []string{"eu", "us", "us-central1"}) {
		t.Fatalf("missing Discovery Engine locations %v", names)
	}
	for _, entry := range []struct{ kind, region string }{{"compute.googleapis.com/RegionDisk", "us"}, {"run.googleapis.com/Service", "eu"}, {"dataproc.googleapis.com/Cluster", "us"}, {"artifactregistry.googleapis.com/Repository", "eu"}, {"dlp.googleapis.com/InspectTemplate", "us"}, {"logging.googleapis.com/LogBucket", "eu"}} {
		page, err := r.List(context.Background(), productRequest(r, entry.kind, entry.region))
		if err != nil || !page.Complete || len(page.Items) > 0 {
			t.Fatalf("multi-region support %s %+v %v", entry.kind, page, err)
		}
	}
}

func TestDiscoveryEngineLogsRedactMalformedCustomerRecords(t *testing.T) {
	var logs []execution.JobLogEntry
	ctx := execution.WithJobLogSink(context.Background(), execution.JobLogSinkFunc(func(_ context.Context, entry execution.JobLogEntry) { logs = append(logs, entry) }))
	r := protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
		return apiResponse(req, 200, `{"documents":[{"structData":{"title":"DISCOVERY_PRIVATE_TITLE"},"unexpected":"DISCOVERY_PRIVATE_VALUE","data":"DISCOVERY_PRIVATE_SCALAR"},"DISCOVERY_PRIVATE_INVALID_ITEM"]}`), nil
	})
	c, err := r.resolve(ctx, "connection")
	if err != nil {
		t.Fatal(err)
	}
	data, err := c.request(ctx, "GET", "https://discoveryengine.googleapis.com/v1/"+deStore+"/branches/0/documents", nil)
	if err != nil || len(array(data["documents"])) != 2 {
		t.Fatalf("native response changed %v", err)
	}
	raw, _ := json.Marshal(logs)
	if strings.Contains(string(raw), "DISCOVERY_PRIVATE_") {
		t.Fatalf("malformed response leaked %s", raw)
	}
	raw, _ = json.Marshal(data)
	if !strings.Contains(string(raw), "DISCOVERY_PRIVATE_") {
		t.Fatal("redaction mutated private validation input")
	}
	other := safePayload(map[string]any{"name": "projects/sample-project/locations/us/collections/unrelated", "configuration": map[string]any{"ordinary": true}})
	if object(other["configuration"])["ordinary"] != true {
		t.Fatal("Discovery Engine filtering changed another service")
	}
}
