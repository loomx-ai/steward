package gcp

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

const metricsTestScope = "//monitoring.googleapis.com/locations/global/metricsScopes/123456"
const metricsTestLink = metricsTestScope + "/projects/222222"

type metricsScenario struct {
	scope, reverse, operation map[string]any
	calls, writes             []string
	handle                    func(*http.Request) (*http.Response, bool)
}

func newMetricsScenario(t *testing.T) *metricsScenario {
	t.Helper()
	s := &metricsScenario{}
	for file, target := range map[string]*map[string]any{"scope": &s.scope, "reverse": &s.reverse} {
		raw, err := os.ReadFile("fixtures/metrics-scope/" + file + ".json")
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, target); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

// Literal native URLs/fields do not depend on the catalog or production binder.
func (s *metricsScenario) transport(t *testing.T) roundTripFunc {
	return func(req *http.Request) (*http.Response, error) {
		s.calls = append(s.calls, req.Method+" "+req.URL.String())
		if s.handle != nil {
			if response, ok := s.handle(req); ok {
				return response, nil
			}
		}
		if req.URL.Host != "monitoring.googleapis.com" {
			t.Fatalf("unexpected host or project mutation: %s", req.URL)
		}
		respond := func(code int, data any) (*http.Response, error) { return dataformResponse(req, code, data), nil }
		switch req.Method + " " + req.URL.RequestURI() {
		case "GET /v1/locations/global/metricsScopes/123456":
			return respond(200, s.scope)
		case "GET /v1/locations/global/metricsScopes:listMetricsScopesByMonitoredProject?monitoredResourceContainer=projects%2F123456":
			return respond(200, s.reverse)
		case "GET /v1/operations/unlink-1":
			if s.operation == nil {
				return respond(404, map[string]any{})
			}
			return respond(200, s.operation)
		case "DELETE /v1/locations/global/metricsScopes/123456/projects/222222", "DELETE /v1/locations/global/metricsScopes/123456/projects/333333":
			body, _ := io.ReadAll(req.Body)
			if len(body) != 0 {
				t.Fatalf("native unlink body must be empty: %s", body)
			}
			s.writes = append(s.writes, req.URL.Path)
			s.operation = map[string]any{"name": "operations/unlink-1", "metadata": map[string]any{"@type": "type.googleapis.com/google.monitoring.metricsscope.v1.OperationMetadata", "state": "RUNNING"}, "done": false}
			return respond(200, s.operation)
		default:
			t.Fatalf("unexpected native method, child GET, foreign scope or paging: %s %s", req.Method, req.URL)
			return nil, nil
		}
	}
}

func (s *metricsScenario) remove(id string) {
	s.scope["monitoredProjects"] = slices.DeleteFunc(array(s.scope["monitoredProjects"]), func(value any) bool { return "//monitoring.googleapis.com/"+text(object(value)["name"]) == id })
	s.scope["updateTime"] = "2026-09-09T12:00:00Z"
}

func (s *metricsScenario) assets(t *testing.T, r *Runtime) []asset.Asset {
	t.Helper()
	var assets []asset.Asset
	for _, kind := range []string{metricsScopeType, monitoredProjectType} {
		batch, err := r.List(context.Background(), productRequest(r, kind, "project"))
		if err != nil || !batch.Complete || batch.NextCursor != "" || batch.RequestID == "" {
			t.Fatalf("native scope inventory: %+v %v", batch, err)
		}
		for _, item := range batch.Items {
			capabilities := asset.CapabilitySet{}
			if item.Actionable != nil && *item.Actionable {
				capabilities = append(capabilities, asset.CapabilityActionable)
			}
			assets = append(assets, asset.Asset{ID: asset.AssetID(item.NativeID), Identity: asset.Identity{Provider: asset.ProviderGCP, ConnectionID: "connection", Partition: "google-cloud", NativeType: kind, NativeID: item.NativeID}, Normalized: item.Normalized, Capabilities: capabilities})
		}
	}
	return assets
}

func metricsAsset(assets []asset.Asset, id string) asset.Asset {
	return batchAsset(assets, strings.TrimPrefix(id, "//monitoring.googleapis.com/"))
}

func TestMetricsScopeNativeInventoryPlanUnlinkAndRestart(t *testing.T) {
	ctx := context.Background()
	s := newMetricsScenario(t)
	r := protocolRuntime(t, s.transport(t))
	assets := s.assets(t, r)
	if len(assets) != 4 {
		t.Fatalf("scope/member inventory: %+v", assets)
	}
	root := metricsAsset(assets, metricsTestScope)
	inbound, _ := discoveryStrings(root.Normalized["monitored_by_scopes"])
	if len(inbound) != 2 || !slices.Contains(inbound, "//monitoring.googleapis.com/locations/global/metricsScopes/987654") {
		t.Fatalf("reverse scope discovery: %+v", inbound)
	}
	for _, value := range []asset.Asset{root, metricsAsset(assets, metricsTestScope+"/projects/123456")} {
		if len(value.Capabilities) != 0 {
			t.Fatalf("scope/self actionable: %+v", value)
		}
		if _, err := r.ResolveAction(ctx, "connection", value); err == nil {
			t.Fatal("resolved scope/self delete")
		}
	}
	var relations []graph.Relationship
	for _, value := range assets {
		if value.Identity.NativeType != monitoredProjectType {
			continue
		}
		refs, _ := discoveryStrings(value.Normalized[referenceKey(metricsScopeType)])
		if len(refs) != 1 || refs[0] != metricsTestScope || value.Normalized[metricsParentTime] == nil {
			t.Fatalf("missing scope dependency/proof: %+v", value)
		}
		relations = append(relations, graph.Relationship{SourceAssetID: value.ID, TargetAssetID: root.ID, Type: graph.RelationshipDependsOn, Confidence: 1})
	}
	link := metricsAsset(assets, metricsTestLink)
	input := plan.Input{Assets: assets, ResolvedAssetIDs: []asset.AssetID{link.ID}, Relationships: relations}
	planned, err := plan.Solve(input)
	if err != nil || len(planned.Blockers) != 0 || len(planned.Steps) != 1 || planned.Steps[0].AssetID != link.ID || len(planned.ImpactItems) != 0 {
		t.Fatalf("unlink plan: %+v %v", planned, err)
	}
	request := dataformRequest(t, planned, assets, link)
	driver, err := r.ResolveAction(ctx, "connection", request.Asset)
	if err != nil {
		t.Fatal(err)
	}
	result, err := driver.Execute(ctx, request)
	if err != nil || result.ProviderRequestID == "" || result.Data["phase"] != "metrics_scope_unlink" || len(s.writes) != 1 {
		t.Fatalf("unlink: %+v %v", result, err)
	}
	encoded, _ := json.Marshal(result)
	var resumed contracts.ActionResult
	if err := json.Unmarshal(encoded, &resumed); err != nil {
		t.Fatal(err)
	}
	restarted := protocolRuntime(t, s.transport(t))
	driver, err = restarted.ResolveAction(ctx, "connection", request.Asset)
	if err != nil {
		t.Fatal(err)
	}
	if wait, err := driver.Wait(ctx, request, resumed); err != nil || wait.Done {
		t.Fatalf("pending unlink: %+v %v", wait, err)
	}
	s.operation["done"] = true
	object(s.operation["metadata"])["state"] = "DONE"
	if wait, err := driver.Wait(ctx, request, resumed); err != nil || wait.Done {
		t.Fatalf("done operation with live link: %+v %v", wait, err)
	}
	s.remove(metricsTestLink)
	if wait, err := driver.Wait(ctx, request, resumed); err != nil || !wait.Done {
		t.Fatalf("unlink absence: %+v %v", wait, err)
	}
	if len(array(s.scope["monitoredProjects"])) != 2 || len(s.writes) != 1 {
		t.Fatalf("unlink altered unselected projects: %+v", s)
	}
	if result, err := driver.Execute(ctx, request); err != nil || result.ProviderOperationID != "" || len(s.writes) != 1 {
		t.Fatalf("idempotent absence: %+v %v", result, err)
	}
}

func TestMetricsScopeIdentitiesInvokeAndScanBoundaries(t *testing.T) {
	c := &client{project: "sample-project", number: "123456"}
	for _, value := range []string{metricsTestScope, "locations/global/metricsScopes/sample-project", "https://monitoring.googleapis.com/v1/locations/global/metricsScopes/123456"} {
		if id, err := c.metricsID(metricsScopeType, value, true); err != nil || id != metricsTestScope {
			t.Fatalf("alias %s: %s %v", value, id, err)
		}
	}
	for _, value := range []string{"https://user@monitoring.googleapis.com/v1/locations/global/metricsScopes/123456", metricsTestScope + "/", "locations/global/metricsScopes/foreign", "locations/us-central1/metricsScopes/123456", "projects/123456/metricsScopes/local", "https://monitoring.googleapis.com/v1/locations/global/metricsScopes/123456?x=y", "locations/global/metricsScopes/%2e%2e", "locations/global/metricsScopes/.."} {
		if _, err := c.metricsID(metricsScopeType, value, true); err == nil {
			t.Fatalf("invalid identity accepted: %s", value)
		}
	}
	s := newMetricsScenario(t)
	r := protocolRuntime(t, s.transport(t))
	for _, scope := range []string{"project", "global"} {
		request := productRequest(r, monitoredProjectType, scope)
		request.Limit = 1 // Native complete, unpaged response is never truncated.
		batch, err := r.List(context.Background(), request)
		if err != nil || len(batch.Items) != 3 || !batch.Complete {
			t.Fatalf("scope %s: %+v %v", scope, batch, err)
		}
	}
	before := len(s.calls)
	request := productRequest(r, monitoredProjectType, "us-central1")
	if batch, err := r.List(context.Background(), request); err != nil || len(batch.Items) != 0 || !batch.Complete || len(s.calls) != before {
		t.Fatalf("regional scope: %+v %v", batch, err)
	}
	request = productRequest(r, monitoredProjectType, "project")
	request.Cursor = "invented-page"
	if _, err := r.List(context.Background(), request); err == nil || len(s.calls) != before {
		t.Fatal("invented cursor reached provider")
	}
	for _, invocation := range []contracts.Invocation{
		{Operation: metricsGet, Parameters: map[string]any{"name": "locations/global/metricsScopes/sample-project"}},
		{Operation: metricsReverse, Parameters: map[string]any{"monitoredResourceContainer": "projects/123456"}},
	} {
		invocation.ConnectionID = "connection"
		if _, err := r.Invoke(context.Background(), invocation); err != nil {
			t.Fatal(err)
		}
	}
	before = len(s.calls)
	for _, invocation := range []contracts.Invocation{
		{Operation: metricsGet, Parameters: map[string]any{"name": "locations/global/metricsScopes/foreign"}},
		{Operation: metricsReverse, Parameters: map[string]any{"monitoredResourceContainer": "projects/foreign"}},
		{Operation: metricsReverse, Parameters: map[string]any{}},
		{Operation: metricsDelete, Parameters: map[string]any{"name": "locations/global/metricsScopes/123456/projects/123456"}},
		{Operation: metricsDelete, Parameters: map[string]any{"name": "locations/global/metricsScopes/foreign/projects/123456"}},
		{Operation: "monitoring.operations.get", Parameters: map[string]any{"name": "https://foreign/operations/x"}},
	} {
		invocation.ConnectionID = "connection"
		if _, err := r.Invoke(context.Background(), invocation); err == nil {
			t.Fatalf("invalid Invoke accepted: %+v", invocation)
		}
	}
	if len(s.calls) != before {
		t.Fatal("invalid invocations reached API")
	}
	if id := c.canonicalName(metricsTestScope + "/projects/123456"); id != metricsTestScope+"/projects/123456" {
		t.Fatalf("self number alias lost: %s", id)
	}
	if strings.Contains(strings.Join(s.calls, " "), "pageSize") {
		t.Fatal("invented pagination")
	}
}

func TestMetricsScopeFixturesMatchOfficialNativeSchemas(t *testing.T) {
	raw, err := os.ReadFile("fixtures/metrics-scope/native-schemas.json")
	if err != nil {
		t.Fatal(err)
	}
	var source map[string]any
	if err := json.Unmarshal(raw, &source); err != nil {
		t.Fatal(err)
	}
	// Discovery uses schema IDs for $ref. Convert only reference syntax for the
	// existing JSON Schema validator; fields/enums remain the official objects.
	var convert func(any)
	convert = func(value any) {
		switch v := value.(type) {
		case map[string]any:
			delete(v, "id")
			if ref, ok := v["$ref"].(string); ok {
				v["$ref"] = "#/definitions/" + ref
			}
			for _, child := range v {
				convert(child)
			}
		case []any:
			for _, child := range v {
				convert(child)
			}
		}
	}
	schemas := object(source["schemas"])
	convert(schemas)
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft4)
	if err := compiler.AddResource("https://fixture.test/metrics-scope.json", map[string]any{"definitions": schemas}); err != nil {
		t.Fatal(err)
	}
	s := newMetricsScenario(t)
	for name, data := range map[string]any{"MetricsScope": s.scope, "MonitoredProject": object(array(s.scope["monitoredProjects"])[2]), "OperationMetadata": map[string]any{"state": "DONE", "createTime": "2026-09-09T10:00:00Z", "updateTime": "2026-09-09T10:00:01Z"}} {
		schema, err := compiler.Compile("https://fixture.test/metrics-scope.json#/definitions/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if err := schema.Validate(data); err != nil {
			t.Fatalf("%s fixture differs from native schema: %v", name, err)
		}
	}
	if source["revision"] != "20260827" || source["source_sha256"] != "af1250b3492d3b37abc8c8e440cada94d8227aa11ddeef0f96f5c9f1b44f35c4" {
		t.Fatal("native schema provenance changed without review")
	}
}
