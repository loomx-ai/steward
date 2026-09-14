package gcp

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
)

type loggingRoutingScenario struct {
	*monitoringDependencyScenario
	hierarchy    *organizationScenario
	sinks        map[string][]map[string]any
	foreign      map[string]any
	mode         string
	calls        []string
	lists        map[string]int
	foreignLists int
}

func loggingSinkFixture(parent, name, destination string) map[string]any {
	return map[string]any{"name": name, "resourceName": parent + "/sinks/" + name, "destination": destination, "filter": `labels.check_id="PUBLIC-CHECK" AND NOT jsonPayload.PRIVATE_STATE="up"`, "includeChildren": strings.HasPrefix(parent, "folders/") || strings.HasPrefix(parent, "organizations/"), "writerIdentity": "serviceAccount:writer@example.test"}
}
func newLoggingRoutingScenario(t *testing.T) *loggingRoutingScenario {
	t.Helper()
	s := &loggingRoutingScenario{monitoringDependencyScenario: monitoringDependencyFixture(t), hierarchy: newOrganizationScenario(), sinks: map[string][]map[string]any{}, lists: map[string]int{}}
	s.data = nil
	payload, _ := json.Marshal(alertPolicyFixture())
	if err := json.Unmarshal([]byte(strings.ReplaceAll(string(payload), "sample-project", "foreign-project")), &s.foreign); err != nil {
		t.Fatal(err)
	}
	condition := object(array(s.foreign["conditions"])[0])
	delete(condition, "conditionThreshold")
	condition["conditionMatchedLog"] = map[string]any{"filter": `labels.check_id="public-check"`}
	s.sinks["projects/sample-project"] = []map[string]any{loggingSinkFixture("projects/sample-project", "forward", "logging.googleapis.com/projects/foreign-project")}
	base := s.r.transport
	s.r = protocolRuntime(t, base.RoundTrip)
	s.r.transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.Method != "GET" {
			return base.RoundTrip(req)
		}
		s.calls = append(s.calls, req.URL.Host+req.URL.RequestURI())
		if req.URL.Host == resourceManagerHost {
			switch req.URL.Path {
			case "/v3/projects/sample-project":
				data := cloneParameters(s.hierarchy.project)
				if s.mode == "ancestry-change" && s.lists["projects/sample-project"] >= 2 {
					data["createTime"] = "2025-02-01T00:00:00Z"
				}
				return dataformResponse(req, 200, data), nil
			case "/v3/projects/foreign-project", "/v3/projects/987654":
				if s.mode == "foreign-denied" {
					return apiResponse(req, 403, `{}`), nil
				}
				id := "foreign-project"
				if s.mode == "foreign-wrong" {
					id = "wrong-project"
				}
				return dataformResponse(req, 200, map[string]any{"name": "projects/987654", "projectId": id, "state": "ACTIVE"}), nil
			case "/v3/folders/456":
				if s.mode == "ancestor-denied" {
					return apiResponse(req, 403, `{}`), nil
				}
				return dataformResponse(req, 200, s.hierarchy.folder), nil
			case "/v3/organizations/123":
				return dataformResponse(req, 200, s.hierarchy.organization), nil
			}
		}
		if req.URL.Host == loggingHost {
			if !strings.HasPrefix(req.URL.Path, "/v2/") {
				t.Fatal(req.URL)
			}
			parts := strings.Split(strings.TrimPrefix(req.URL.Path, "/v2/"), "/")
			if len(parts) < 3 || parts[2] != "sinks" {
				t.Fatal(req.URL)
			}
			parent := strings.Join(parts[:2], "/")
			if parent != "projects/sample-project" && parent != "folders/456" && parent != "organizations/123" {
				t.Fatal("unexpected sink scope", parent)
			}
			rows := s.sinks[parent]
			if len(parts) == 3 {
				s.lists[parent]++
				if req.URL.Query().Get("filter") != `in_scope("DEFAULT")` || req.URL.Query().Get("pageSize") != "1000" {
					t.Fatal("incomplete sink query", req.URL)
				}
				if parent == "projects/sample-project" {
					switch s.mode {
					case "sink-denied":
						return apiResponse(req, 403, `{}`), nil
					case "sink-null":
						return apiResponse(req, 200, `{"sinks":null}`), nil
					case "sink-item":
						return apiResponse(req, 200, `{"sinks":[null]}`), nil
					case "sink-token-null":
						return apiResponse(req, 200, `{"nextPageToken":null}`), nil
					case "sink-loop":
						return apiResponse(req, 200, `{"nextPageToken":"loop"}`), nil
					case "sink-partial":
						return apiResponse(req, 200, `{"unreachable":["scope"]}`), nil
					case "sink-duplicate":
						rows = append(rows, rows[0])
					case "late-route":
						if s.lists[parent] < 3 {
							rows = nil
						}
					case "late-execute":
						if s.lists[parent] < 5 {
							rows = nil
						}
					case "sink-paged":
						if req.URL.Query().Get("pageToken") == "" {
							return apiResponse(req, 200, `{"nextPageToken":"second"}`), nil
						}
					case "sink-set-change":
						if s.lists[parent] >= 2 {
							rows = nil
						}
					}
				}
				values := []any{}
				for _, row := range rows {
					values = append(values, row)
				}
				return dataformResponse(req, 200, map[string]any{"sinks": values}), nil
			}
			if len(parts) != 4 || req.URL.RawQuery != "" {
				t.Fatal(req.URL)
			}
			if s.mode == "sink-get-denied" {
				return apiResponse(req, 403, `{}`), nil
			}
			for _, row := range rows {
				if row["name"] == parts[3] {
					copy := cloneParameters(row)
					if s.mode == "sink-get-change" {
						copy["filter"] = "PRIVATE_CHANGED"
					}
					if s.mode == "sink-get-missing" {
						return apiResponse(req, 404, `{}`), nil
					}
					return dataformResponse(req, 200, copy), nil
				}
			}
			return apiResponse(req, 404, `{}`), nil
		}
		if req.URL.Host == "monitoring.googleapis.com" && strings.HasPrefix(req.URL.Path, "/v3/projects/foreign-project/alertPolicies") {
			if s.mode == "policy-denied" {
				return apiResponse(req, 403, `{}`), nil
			}
			if req.URL.Path == "/v3/projects/foreign-project/alertPolicies" {
				s.foreignLists++
				if req.URL.Query().Get("filter") != "" || req.URL.Query().Get("pageSize") != "100" {
					t.Fatal(req.URL)
				}
				return dataformResponse(req, 200, map[string]any{"alertPolicies": []any{s.foreign}}), nil
			}
			if req.URL.Path != "/v3/"+text(s.foreign["name"]) {
				t.Fatal(req.URL)
			}
			return dataformResponse(req, 200, s.foreign), nil
		}
		return base.RoundTrip(req)
	})
	return s
}
func TestLoggingRoutingNativeScopeAndReads(t *testing.T) {
	for _, mode := range []string{"project", "folder", "organization", "system-buckets", "intercepting", "disabled", "aliases", "sink-paged", "ancestor-denied", "sink-denied", "sink-null", "sink-item", "sink-token-null", "sink-loop", "sink-partial", "sink-duplicate", "sink-get-denied", "sink-get-missing", "sink-get-change", "sink-set-change", "ancestry-change", "late-route", "foreign-denied", "foreign-wrong", "policy-denied"} {
		t.Run(mode, func(t *testing.T) {
			s := newLoggingRoutingScenario(t)
			s.mode = mode
			if mode == "folder" || mode == "organization" || mode == "intercepting" {
				s.sinks["projects/sample-project"] = nil
				parent := "folders/456"
				if mode == "organization" || mode == "intercepting" {
					parent = "organizations/123"
				}
				s.sinks[parent] = []map[string]any{loggingSinkFixture(parent, "aggregate", "logging.googleapis.com/projects/foreign-project")}
				if mode == "intercepting" {
					s.sinks[parent][0]["interceptChildren"] = true
				}
			}
			if mode == "system-buckets" {
				for _, parent := range []string{"folders/456", "organizations/123"} {
					row := loggingSinkFixture(parent, "_Required", "logging.googleapis.com/"+parent+"/locations/global/buckets/_Required")
					row["includeChildren"] = false
					s.sinks[parent] = []map[string]any{row}
				}
			}
			if mode == "disabled" {
				s.sinks["projects/sample-project"][0]["disabled"] = true
			}
			if mode == "aliases" {
				s.sinks["projects/sample-project"] = append(s.sinks["projects/sample-project"], loggingSinkFixture("projects/sample-project", "number", "logging.googleapis.com/projects/987654"))
			}
			c, err := s.r.resolve(t.Context(), "connection")
			if err != nil {
				t.Fatal(err)
			}
			policies, err := c.monitoringPolicies(t.Context())
			good := mode == "project" || mode == "folder" || mode == "organization" || mode == "system-buckets" || mode == "intercepting" || mode == "disabled" || mode == "aliases" || mode == "sink-paged"
			if (err == nil) != good {
				t.Fatal(mode, err)
			}
			if good {
				if len(policies) != 1 || s.foreignLists != 2 {
					t.Fatal("incomplete or duplicate foreign observations", len(policies), s.foreignLists)
				}
				for _, p := range policies {
					if p.Metrics || p.Local || p.reference("public-check") == monitoringNoReference || p.reference("other") != monitoringNoReference {
						t.Fatal("wrong project scope", p.Metrics, p.Local)
					}
				}
				for _, parent := range []string{"projects/sample-project", "folders/456", "organizations/123"} {
					if s.lists[parent] < 4 {
						t.Fatal("missing repeated ancestor read", parent, s.lists)
					}
				}
				if c.project != "sample-project" || c.number != "123456" {
					t.Fatal("mutated bound client")
				}
			}
			if *s.uptimeDeletes != 0 || s.policyDeletes != 0 {
				t.Fatal("discovery wrote to cloud")
			}
		})
	}
}
func TestLoggingRoutingGraphAndExecute(t *testing.T) {
	for _, mode := range []string{"reference", "metric-only", "route-excluded", "excluded-foreign-denied", "exclusion-unknown", "bucket", "ancestor-local-only", "late-execute", "sink-denied"} {
		t.Run(mode, func(t *testing.T) {
			s := newLoggingRoutingScenario(t)
			sink := s.sinks["projects/sample-project"][0]
			switch mode {
			case "metric-only":
				condition := object(array(s.foreign["conditions"])[0])
				delete(condition, "conditionMatchedLog")
				condition["conditionThreshold"] = map[string]any{"filter": uptimeMetricFilter + ` AND metric.labels.check_id="public-check"`}
			case "excluded-foreign-denied":
				s.mode = "foreign-denied"
				sink["filter"] = `labels.check_id="other"`
			case "route-excluded":
				sink["filter"] = `labels.check_id="other"`
			case "exclusion-unknown":
				sink["exclusions"] = []any{map[string]any{"name": "state", "filter": `labels.check_id="public-check" AND jsonPayload.PRIVATE_STATE="up"`}}
			case "bucket":
				sink["destination"] = "logging.googleapis.com/projects/foreign-project/locations/global/buckets/logs"
			case "ancestor-local-only":
				s.sinks["projects/sample-project"] = nil
				sink["resourceName"] = "folders/456/sinks/forward"
				sink["includeChildren"] = false
				s.sinks["folders/456"] = []map[string]any{sink}
			case "late-execute", "sink-denied":
				s.mode = mode
			}
			contributor, err := s.r.MonitoringDependencies(t.Context(), "connection")
			if err != nil {
				t.Fatal(err)
			}
			result, graphErr := contributor.Contribute(t.Context(), "global", []asset.Asset{s.request.Asset})
			if mode == "sink-denied" {
				if graphErr == nil {
					t.Fatal("lost scope became complete graph")
				}
			} else if graphErr != nil {
				t.Fatal(graphErr)
			}
			encoded, _ := json.Marshal(result)
			if strings.Contains(string(encoded), "PRIVATE_") || strings.Contains(string(encoded), "writer@example") {
				t.Fatal("private routing evidence leaked")
			}
			good := mode == "metric-only" || mode == "route-excluded" || mode == "excluded-foreign-denied" || mode == "bucket" || mode == "ancestor-local-only"
			if !good && mode != "late-execute" && mode != "sink-denied" && (len(result.Unresolved) != 1 || !result.Unresolved[0].BlocksCleanup || len(result.Relationships) != 0) {
				t.Fatal(result)
			}
			if good && (len(result.Unresolved) != 0 || len(result.Relationships) != 0) {
				t.Fatal(result)
			}
			// Start fresh counters so late-execute inserts a route only after preflight.
			s.lists = map[string]int{}
			driver, err := s.r.ResolveAction(t.Context(), "connection", s.request.Asset)
			if err != nil {
				t.Fatal(err)
			}
			_, err = driver.Execute(t.Context(), s.request)
			if (err == nil) != good {
				t.Fatal(mode, err)
			}
			if good && *s.uptimeDeletes != 1 || !good && *s.uptimeDeletes != 0 || s.policyDeletes != 0 {
				t.Fatal("incorrect mutations", *s.uptimeDeletes, s.policyDeletes)
			}
		})
	}
}
func TestLoggingRoutingFilterExclusions(t *testing.T) {
	for _, tt := range []struct {
		filter, exclusion string
		disabled          bool
		want              monitoringReference
	}{
		{"", "", false, monitoringHasReference},
		{`labels.check_id="public-check"`, `labels.check_id="public-check"`, false, monitoringNoReference},
		{`labels.check_id="public-check"`, `labels.check_id="public-check"`, true, monitoringHasReference},
		{`labels.check_id="public-check"`, `labels.check_id="public-check" AND jsonPayload.state="up"`, false, monitoringUnresolvedReference},
		{`labels.check_id="other"`, `PRIVATE_UNKNOWN`, false, monitoringNoReference},
	} {
		sink := map[string]any{"filter": tt.filter}
		if tt.exclusion != "" {
			sink["exclusions"] = []any{map[string]any{"filter": tt.exclusion, "disabled": tt.disabled}}
		}
		if got := loggingRouteReference(sink, "public-check"); got != tt.want {
			t.Fatal(tt, got)
		}
	}
}

func TestLoggingRoutingNativeSchemasAndDestinations(t *testing.T) {
	compiler := infraFixtureSchemas(t, "fixtures/logging-routing/native-schemas.json", "20260818", "da8b8dafbac40c8b0c4bf0568a7b6ccd9654045ddac92a94040164e0accf9191")
	schema, err := compiler.Compile("https://fixture.test/infra-manager.json#/definitions/LogSink")
	if err != nil {
		t.Fatal(err)
	}
	sink := loggingSinkFixture("folders/456", "aggregate", "logging.googleapis.com/projects/foreign-project")
	if err := schema.Validate(sink); err != nil {
		t.Fatal(err)
	}
	invalid := cloneParameters(sink)
	invalid["destination"] = 42
	if schema.Validate(invalid) == nil {
		t.Fatal("native schema accepted numeric destination")
	}
	for _, dest := range []string{"logging.googleapis.com/projects/foreign-project", "logging.googleapis.com/projects/987654", "logging.googleapis.com/projects/foreign-project/locations/global/buckets/logs", "logging.googleapis.com/organizations/123/locations/global/buckets/_Default", "logging.googleapis.com/folders/456/locations/global/buckets/_Required", "logging.googleapis.com/billingAccounts/012345-ABCDEF-123456/locations/global/buckets/_Default", "storage.googleapis.com/logs-bucket", "bigquery.googleapis.com/projects/foreign-project/datasets/logs", "pubsub.googleapis.com/projects/foreign-project/topics/logs"} {
		if _, err := loggingDestinationProject(dest); err != nil {
			t.Fatal(dest, err)
		}
	}
	for _, dest := range []string{"", "evil.example/projects/foreign-project", "https://logging.googleapis.com/projects/foreign-project", "logging.googleapis.com:443/projects/foreign-project", "logging.googleapis.com/projects/foreign-project?x=1", "logging.googleapis.com/projects/foreign-project#fragment", "logging.googleapis.com/projects/%2e%2e", "logging.googleapis.com/projects/../locations/global/buckets/logs", "logging.googleapis.com/projects/foreign-project/extra", "logging.googleapis.com/projects/foreign-project/", "user@logging.googleapis.com/projects/foreign-project", "storage.googleapis.com/../logs"} {
		if _, err := loggingDestinationProject(dest); err == nil {
			t.Fatal("unsafe destination accepted", dest)
		}
	}
	c := &client{project: "sample-project", number: "123456"}
	for _, change := range []func(map[string]any){
		func(v map[string]any) { v["resourceName"] = "folders/999/sinks/aggregate" },
		func(v map[string]any) { v["name"] = "../escape" },
		func(v map[string]any) { v["includeChildren"] = nil },
		func(v map[string]any) { v["interceptChildren"] = true; v["includeChildren"] = false },
		func(v map[string]any) { v["exclusions"] = []any{nil} },
		func(v map[string]any) { v["exclusions"] = []any{map[string]any{"name": "bad", "filter": ""}} },
	} {
		copy := cloneParameters(sink)
		change(copy)
		if _, err := c.loggingSinkData("folders/456", copy); err == nil {
			t.Fatal("malformed sink accepted")
		}
	}
}
func FuzzLoggingDestination(f *testing.F) {
	for _, seed := range []string{"logging.googleapis.com/projects/sample-project", "storage.googleapis.com/logs", "logging.googleapis.com/projects/%2e%2e"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, value string) { _, _ = loggingDestinationProject(value) })
}

func TestLoggingRoutingDoesNotBorrowMetricsScope(t *testing.T) {
	logPolicy := map[string]any{"conditions": []any{map[string]any{"conditionMatchedLog": map[string]any{"filter": `labels.check_id="public-check"`}}}}
	metricPolicy := map[string]any{"conditions": []any{map[string]any{"conditionThreshold": map[string]any{"filter": uptimeMetricFilter + ` AND metric.labels.check_id="public-check"`}}}}
	route := []map[string]any{{"filter": `labels.check_id="public-check"`}}
	for _, tt := range []struct {
		policy monitoringConsumer
		want   monitoringReference
	}{
		{monitoringConsumer{Data: logPolicy, Metrics: true}, monitoringNoReference},
		{monitoringConsumer{Data: metricPolicy, LogRoutes: route}, monitoringNoReference},
		{monitoringConsumer{Data: logPolicy, LogRoutes: route}, monitoringHasReference},
		{monitoringConsumer{Data: metricPolicy, Metrics: true}, monitoringHasReference},
		{monitoringConsumer{Data: logPolicy, Metrics: true, Local: true}, monitoringHasReference},
	} {
		if got := tt.policy.reference("public-check"); got != tt.want {
			t.Fatal(got, tt.want)
		}
	}
}
