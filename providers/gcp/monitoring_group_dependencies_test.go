package gcp

import (
	"encoding/json"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
	"github.com/loomx-ai/steward/internal/core/plan"
)

func TestMonitoringGroupQueryReferences(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  monitoringReference
	}{
		{`group.id="9876"`, monitoringHasReference}, {`group.id=9876`, monitoringHasReference},
		{`group.id="98\u00376"`, monitoringHasReference}, {`group.id="other"`, monitoringNoReference},
		{`metric.type="x" AND metric.type="y" AND group.id="9876"`, monitoringHasReference},
		{`group.id="other" OR group.id="9876"`, monitoringHasReference},
		{`metric.labels.x="group.id=9876"`, monitoringNoReference},
		{`metric.type="x"`, monitoringNoReference},
		{`group.id=one_of("9876")`, monitoringUnresolvedReference},
		{`group.id!="other"`, monitoringUnresolvedReference}, {`group.id="${group}"`, monitoringUnresolvedReference},
		{`group.id="98\x376"`, monitoringUnresolvedReference}, {`group.id=9.876`, monitoringUnresolvedReference},
		{`group.id=0x2694`, monitoringUnresolvedReference}, {`group.id="9876" TRAILING`, monitoringUnresolvedReference},
		{`future.selector="other"`, monitoringUnresolvedReference}, {``, monitoringUnresolvedReference},
	} {
		if got := monitoringGroupFilterReference(tc.value, "9876"); got != tc.want {
			t.Errorf("%q: got %v want %v", tc.value, got, tc.want)
		}
	}
	policy := alertPolicyFixture()
	body := object(object(array(policy["conditions"])[0])["conditionThreshold"])
	body["denominatorFilter"] = `group.id="9876"`
	if alertPolicyGroupReference(policy, "9876") != monitoringHasReference {
		t.Fatal("missed denominator")
	}
	condition := object(array(policy["conditions"])[0])
	delete(condition, "conditionThreshold")
	condition["conditionMatchedLog"] = map[string]any{"filter": `labels.x="group.id=9876"`}
	if alertPolicyGroupReference(policy, "9876") != monitoringNoReference {
		t.Fatal("logging literal became Monitoring reference")
	}
	condition["futureCondition"] = map[string]any{"query": "future"}
	if alertPolicyGroupReference(policy, "9876") != monitoringUnresolvedReference {
		t.Fatal("unknown condition cleared")
	}
}
func monitoringDashboardFixture() map[string]any {
	return map[string]any{"name": "projects/sample-project/dashboards/example", "displayName": "Dashboard", "etag": "version-1", "gridLayout": map[string]any{"widgets": []any{map[string]any{"text": map[string]any{"content": "PRIVATE_GROUP group.id=9876", "format": "MARKDOWN"}}}}}
}
func TestMonitoringGroupDashboardNativeQueries(t *testing.T) {
	for _, mode := range []string{"text", "filter", "ratio", "other", "mql", "promql", "sql", "trace", "template", "group-default", "group-other", "future-widget", "future-query", "future-layout", "null-query", "null-layout", "invalid-union", "label-literal", "missing-filter", "deep"} {
		t.Run(mode, func(t *testing.T) {
			data := monitoringDashboardFixture()
			query := map[string]any{"timeSeriesFilter": map[string]any{"filter": `metric.type="compute.googleapis.com/usage" AND group.id="9876"`}}
			want := monitoringUnresolvedReference
			switch mode {
			case "text":
				want = monitoringNoReference
			case "filter":
				want = monitoringHasReference
			case "ratio":
				query = map[string]any{"timeSeriesFilterRatio": map[string]any{"numerator": map[string]any{"filter": `metric.type="x"`}, "denominator": map[string]any{"filter": `group.id="9876"`}}}
				want = monitoringHasReference
			case "other":
				object(query["timeSeriesFilter"])["filter"] = `group.id="other"`
				want = monitoringNoReference
			case "mql":
				query = map[string]any{"timeSeriesQueryLanguage": "PRIVATE_QUERY"}
			case "promql":
				query = map[string]any{"prometheusQuery": "PRIVATE_QUERY"}
			case "sql":
				query = map[string]any{"opsAnalyticsQuery": map[string]any{"sql": "PRIVATE_QUERY"}}
			case "trace":
				query = map[string]any{"traceQuery": map[string]any{}}
			case "template":
				object(query["timeSeriesFilter"])["filter"] = `group.id="${selected_group}"`
			case "group-default", "group-other":
				value := "other"
				if mode == "group-default" {
					value = "9876"
					want = monitoringHasReference
				}
				data["dashboardFilters"] = []any{map[string]any{"filterType": "GROUP", "stringValue": value, "labelKey": "group.id"}}
			case "future-widget":
				data["gridLayout"] = map[string]any{"widgets": []any{map[string]any{"futureWidget": map[string]any{"query": "PRIVATE_QUERY"}}}}
			case "future-query":
				query = map[string]any{"futureQuery": "PRIVATE_QUERY"}
			case "future-layout":
				data["futureLayout"] = map[string]any{"query": "PRIVATE_QUERY"}
			case "null-query":
				query = nil
			case "null-layout":
				data["gridLayout"] = nil
			case "invalid-union":
				query = map[string]any{"timeSeriesFilter": map[string]any{"filter": `metric.type="x"`}, "timeSeriesQueryLanguage": "PRIVATE_QUERY"}
			case "label-literal":
				data["labels"] = map[string]any{"note": "group.id=9876"}
				want = monitoringNoReference
			case "missing-filter":
				query = map[string]any{"timeSeriesFilter": map[string]any{}}
			case "deep":
				nested := map[string]any{}
				data["futureLayout"] = nested
				for i := 0; i < 100; i++ {
					next := map[string]any{}
					nested["next"] = next
					nested = next
				}
			}
			if !slices.Contains([]string{"text", "group-default", "group-other", "future-widget", "future-layout", "null-layout", "label-literal", "deep"}, mode) {
				data["gridLayout"] = map[string]any{"widgets": []any{map[string]any{"scorecard": map[string]any{"timeSeriesQuery": query}}}}
			}
			if got := monitoringDashboardGroupReference(data, "9876"); got != want {
				t.Fatalf("got %v want %v", got, want)
			}
		})
	}
	// Every retained schema is byte-equivalent as decoded JSON to the pinned
	// native source already used to generate the Dashboard catalog operations.
	b, err := os.ReadFile("catalog/source/discovery.json")
	if err != nil {
		t.Fatal(err)
	}
	var source map[string]any
	if err = json.Unmarshal(b, &source); err != nil {
		t.Fatal(err)
	}
	schemas, err := monitoringDashboardSchemas()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, raw := range array(source["documents"]) {
		fragment := object(raw)
		doc := object(fragment["document"])
		if object(doc["methods"])["monitoring.projects.dashboards.list"] == nil {
			continue
		}
		found = true
		for name, schema := range schemas {
			if firewallDigest(schema) != firewallDigest(object(doc["schemas"])[name]) {
				t.Fatal("native schema changed", name)
			}
		}
	}
	if !found || len(schemas) != 61 {
		t.Fatal("missing native Dashboard contract")
	}
}

type monitoringGroupConsumerScenario struct {
	r                *Runtime
	values           map[string][]map[string]any
	assets           []asset.Asset
	mode, collection string
	lists, gets      map[string]int
}

func newMonitoringGroupConsumerScenario(t *testing.T) *monitoringGroupConsumerScenario {
	t.Helper()
	group := monitoringGroupFixture()
	child := monitoringGroupFixture()
	child["name"] = "projects/sample-project/groups/child"
	child["parentName"] = testMonitoringGroupName
	check := uptimeFixture()
	delete(check, "monitoredResource")
	check["resourceGroup"] = map[string]any{"groupId": "9876", "resourceType": "INSTANCE"}
	policy := alertPolicyFixture()
	object(object(array(policy["conditions"])[0])["conditionThreshold"])["filter"] = `metric.type="x" AND group.id="9876"`
	s := &monitoringGroupConsumerScenario{values: map[string][]map[string]any{"groups": {group, child}, "uptimeCheckConfigs": {check}, "alertPolicies": {policy}, "dashboards": {monitoringDashboardFixture()}}, lists: map[string]int{}, gets: map[string]int{}}
	s.r = protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
		if req.Method != "GET" || req.URL.Host != "monitoring.googleapis.com" {
			t.Fatal("unexpected mutation or host", req.Method, req.URL)
		}
		parts := strings.Split(strings.Trim(req.URL.Path, "/"), "/")
		if len(parts) < 4 || parts[1] != "projects" || parts[2] != "sample-project" {
			t.Fatal("unexpected scope", req.URL)
		}
		collection := parts[3]
		rows, ok := s.values[collection]
		if !ok {
			t.Fatal("unexpected collection", req.URL)
		}
		version := "v3"
		if collection == "dashboards" {
			version = "v1"
		}
		if parts[0] != version {
			t.Fatal(req.URL)
		}
		active := s.collection == collection
		if len(parts) == 4 {
			s.lists[collection]++
			n := s.lists[collection]
			q := req.URL.Query()
			if q.Get("pageSize") != "100" {
				t.Fatal(req.URL)
			}
			for k := range q {
				if k != "pageSize" && k != "pageToken" {
					t.Fatal("filtered collection", req.URL)
				}
			}
			field := collection
			if collection == "groups" {
				field = "group"
			}
			data := map[string]any{}
			items := []any{}
			for _, row := range rows {
				items = append(items, row)
			}
			data[field] = items
			if active {
				switch s.mode {
				case "denied":
					return apiResponse(req, 403, `{}`), nil
				case "list-404":
					return apiResponse(req, 404, `{}`), nil
				case "null":
					data[field] = nil
				case "element":
					data[field] = []any{nil}
				case "duplicate":
					data[field] = append(items, items[0])
				case "partial":
					data["unreachable"] = []any{"unknown"}
				case "token-null":
					data["nextPageToken"] = nil
				case "loop":
					data[field] = []any{}
					data["nextPageToken"] = "next"
				case "changed":
					if n > 1 {
						data[field] = []any{}
					}
				case "paged", "page-denied":
					if q.Get("pageToken") == "" {
						data[field] = []any{}
						data["nextPageToken"] = "next"
					} else if s.mode == "page-denied" {
						return apiResponse(req, 403, `{}`), nil
					}
				case "target-omitted":
					data[field] = items[1:]
				case "target-changed":
					group["displayName"] = "changed"
				}
			}
			return dataformResponse(req, 200, data), nil
		}
		if len(parts) != 5 || req.URL.RawQuery != "" {
			t.Fatal(req.URL)
		}
		s.gets[collection]++
		for _, row := range rows {
			if last(text(row["name"])) == parts[4] {
				if active {
					switch s.mode {
					case "get-denied":
						return apiResponse(req, 403, `{}`), nil
					case "get-missing":
						return apiResponse(req, 404, `{}`), nil
					case "get-drift":
						copy := cloneParameters(row)
						copy["displayName"] = "different"
						return dataformResponse(req, 200, copy), nil
					}
				}
				return dataformResponse(req, 200, row), nil
			}
		}
		return apiResponse(req, 404, `{}`), nil
	})
	c, err := s.r.resolve(t.Context(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range []struct{ collection, kind, key string }{{"groups", monitoringGroupType, monitoringGroupReview}, {"uptimeCheckConfigs", uptimeType, uptimeReview}, {"alertPolicies", alertPolicyType, alertPolicyReview}} {
		for _, row := range s.values[entry.collection] {
			id := c.canonicalName("//monitoring.googleapis.com/" + text(row["name"]))
			k := s.r.resourceKind(entry.kind)
			s.assets = append(s.assets, asset.Asset{ID: asset.AssetID(entry.collection + "-" + last(id)), ResourceKindID: k.ID, Identity: asset.Identity{ConnectionID: "connection", Provider: asset.ProviderGCP, Partition: "gcp", NativeType: entry.kind, NativeID: id}, Normalized: map[string]any{entry.key: c.monitoringGroupConsumerConfiguration(entry.kind, id, row), "project_id": "sample-project", "parentName": row["parentName"], "resourceGroup": row["resourceGroup"]}, Capabilities: k.Capabilities})
		}
	}
	return s
}
func TestMonitoringGroupConsumerSnapshotFailures(t *testing.T) {
	for _, collection := range []string{"groups", "uptimeCheckConfigs", "alertPolicies", "dashboards"} {
		for _, mode := range []string{"normal", "paged", "denied", "list-404", "null", "element", "duplicate", "partial", "token-null", "loop", "changed", "page-denied", "get-denied", "get-missing", "get-drift", "target-changed"} {
			t.Run(collection+"/"+mode, func(t *testing.T) {
				s := newMonitoringGroupConsumerScenario(t)
				s.collection, s.mode = collection, mode
				c, err := s.r.resolve(t.Context(), "connection")
				if err != nil {
					t.Fatal(err)
				}
				h := &monitoringDependencies{client: c, connection: "connection"}
				result, err := h.monitoringGroupDependencies(t.Context(), s.assets)
				good := mode == "normal" || mode == "paged"
				if (err == nil) != good {
					t.Fatalf("result %+v, error %v", result, err)
				}
				if good && (len(result.Relationships) != 3 || len(result.Unresolved) != 0) {
					t.Fatal(result)
				}
			})
		}
	}
}
func TestMonitoringGroupConsumerGraphBoundaries(t *testing.T) {
	for _, mode := range []string{"normal", "missing", "stale", "foreign-connection", "closed", "dashboard", "unknown-dashboard", "unknown-policy", "disabled", "target-stale", "target-omitted", "unrelated"} {
		t.Run(mode, func(t *testing.T) {
			s := newMonitoringGroupConsumerScenario(t)
			assets := s.assets
			switch mode {
			case "missing":
				assets = assets[:2]
			case "stale":
				assets[2].Normalized[uptimeReview] = "stale"
			case "foreign-connection":
				assets[2].Identity.ConnectionID = "other"
			case "closed":
				now := assets[2].LastSeenAt
				assets[2].ClosedAt = &now
			case "target-stale":
				assets[0].Normalized[monitoringGroupReview] = "stale"
			case "target-omitted":
				s.collection, s.mode = "groups", mode
			case "dashboard":
				s.values["dashboards"][0]["dashboardFilters"] = []any{map[string]any{"filterType": "GROUP", "stringValue": "9876"}}
			case "unknown-dashboard":
				s.values["dashboards"][0]["futureWidget"] = map[string]any{"query": "PRIVATE_QUERY"}
			case "unknown-policy":
				object(object(array(s.values["alertPolicies"][0]["conditions"])[0])["conditionThreshold"])["filter"] = "PRIVATE_UNKNOWN"
			case "disabled":
				s.values["alertPolicies"][0]["enabled"] = false
			case "unrelated":
				s.values["uptimeCheckConfigs"] = nil
				s.values["alertPolicies"] = nil
				s.values["groups"] = s.values["groups"][:1]
				assets = assets[:1]
			}
			c, _ := s.r.resolve(t.Context(), "connection")
			if mode == "disabled" {
				assets[3].Normalized[alertPolicyReview] = monitoringConfiguration(alertPolicyType, assets[3].Identity.NativeID, s.values["alertPolicies"][0])
			}
			result, err := (&monitoringDependencies{client: c, connection: "connection"}).monitoringGroupDependencies(t.Context(), assets)
			if mode == "target-stale" || mode == "target-omitted" {
				if err == nil {
					t.Fatal("stale target accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			wantRelations, wantUnresolved := 3, 0
			switch mode {
			case "missing":
				wantRelations, wantUnresolved = 1, 2
			case "stale", "foreign-connection", "closed":
				wantRelations, wantUnresolved = 2, 1
			case "dashboard":
				wantUnresolved = 2
			case "unknown-dashboard":
				wantUnresolved = 2
			case "unknown-policy":
				wantRelations, wantUnresolved = 2, 2
			case "unrelated":
				wantRelations = 0
			}
			if len(result.Relationships) != wantRelations || len(result.Unresolved) != wantUnresolved {
				t.Fatalf("%+v", result)
			}
			for _, r := range result.Relationships {
				if r.SourceAssetID != assets[0].ID || r.Evidence[graph.RelationshipEvidenceAutomaticSelection] != false || r.Evidence[graph.RelationshipEvidenceRequiredDeletion] != true {
					t.Fatal(r)
				}
			}
			for _, r := range result.Unresolved {
				if !r.BlocksCleanup {
					t.Fatal(r)
				}
			}
			payload, _ := json.Marshal(result)
			if strings.Contains(string(payload), "PRIVATE_") {
				t.Fatal("private query in graph")
			}
			if _, err := s.r.ResolveAction(t.Context(), "connection", assets[0]); err != nil {
				t.Fatal("reviewed group action unavailable", err)
			}
			if mode == "normal" {
				// Informational child->parent plus reviewed parent->child must yield the
				// same deletion ordering, not a cycle, using native cleanup capabilities.
				values := append([]asset.Asset{}, assets...)
				relationships := append([]graph.Relationship{}, result.Relationships...)
				relationships = append(relationships, graph.Relationship{SourceAssetID: assets[1].ID, TargetAssetID: assets[0].ID, Type: graph.RelationshipDependsOn, Source: "gcp:monitoring-group-targets", Confidence: 1})
				selected := []asset.AssetID{}
				for _, v := range values {
					selected = append(selected, v.ID)
				}
				p, err := plan.Solve(plan.Input{Assets: values, Relationships: relationships, ResolvedAssetIDs: selected})
				if err != nil || len(p.Blockers) != 0 || len(p.Steps) != 4 || p.Steps[3].AssetID != assets[0].ID {
					t.Fatal(p, err)
				}
				p, err = plan.Solve(plan.Input{Assets: values, Relationships: relationships, ResolvedAssetIDs: []asset.AssetID{assets[0].ID}})
				if err != nil || len(p.Blockers) == 0 {
					t.Fatal("auto selected consumers", p, err)
				}
				p, err = plan.Solve(plan.Input{Assets: values, Relationships: relationships, ResolvedAssetIDs: []asset.AssetID{assets[1].ID}})
				if err != nil || len(p.Blockers) != 0 || len(p.Steps) != 1 {
					t.Fatal("child forced parent", p, err)
				}
			}
		})
	}
}

func TestMonitoringGroupDashboardLayouts(t *testing.T) {
	for _, layout := range []string{"gridLayout", "mosaicLayout", "rowLayout", "columnLayout"} {
		for _, chart := range []string{"xyChart", "timeSeriesTable", "pieChart", "treemap"} {
			t.Run(layout+"/"+chart, func(t *testing.T) {
				for _, group := range []string{"9876", "other"} {
					query := map[string]any{"timeSeriesFilter": map[string]any{"filter": `group.id="` + group + `" AND metric.type="x"`}}
					widget := map[string]any{chart: map[string]any{"dataSets": []any{map[string]any{"timeSeriesQuery": query}}}}
					data := monitoringDashboardFixture()
					delete(data, "gridLayout")
					switch layout {
					case "gridLayout":
						data[layout] = map[string]any{"widgets": []any{widget}}
					case "mosaicLayout":
						data[layout] = map[string]any{"tiles": []any{map[string]any{"widget": widget}}}
					case "rowLayout":
						data[layout] = map[string]any{"rows": []any{map[string]any{"widgets": []any{widget}}}}
					case "columnLayout":
						data[layout] = map[string]any{"columns": []any{map[string]any{"widgets": []any{widget}}}}
					}
					if got := monitoringDashboardGroupReference(data, "9876"); got != monitoringBool(group == "9876") {
						t.Fatal(group, got)
					}
				}
			})
		}
	}
}
