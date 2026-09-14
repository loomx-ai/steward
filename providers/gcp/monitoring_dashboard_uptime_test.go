package gcp

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func dashboardUptimeFixture(filter string) map[string]any {
	data := monitoringDashboardFixture()
	data["gridLayout"] = map[string]any{"widgets": []any{map[string]any{"xyChart": map[string]any{"dataSets": []any{map[string]any{"timeSeriesQuery": map[string]any{"timeSeriesFilter": map[string]any{"filter": filter}}}}}}}}
	return data
}
func TestMonitoringDashboardUptimeQueryReferences(t *testing.T) {
	for _, mode := range []string{"metric", "other-check", "other-metric", "ratio", "template", "mql", "promql", "sql", "trace", "text", "unknown", "log", "log-other", "log-template", "log-own", "log-number", "log-foreign", "log-view", "log-route", "log-excluded", "metric-no-scope", "log-no-route", "log-explicit-source"} {
		t.Run(mode, func(t *testing.T) {
			data := dashboardUptimeFixture(uptimeMetricFilter + ` AND metric.labels.check_id="public-check"`)
			c := monitoringConsumer{NativeType: monitoringDashboardType, Project: "sample-project", ProjectNumber: "123456", MonitoredProject: "sample-project", MonitoredNumber: "123456", Data: data, Metrics: true, Local: true}
			want := monitoringHasReference
			switch mode {
			case "metric":
			case "other-check":
				c.Data = dashboardUptimeFixture(uptimeMetricFilter + ` AND metric.labels.check_id="other"`)
				want = monitoringNoReference
			case "other-metric":
				c.Data = dashboardUptimeFixture(`metric.type="compute.googleapis.com/usage"`)
				want = monitoringNoReference
			case "template":
				c.Data = dashboardUptimeFixture(uptimeMetricFilter + ` AND metric.labels.check_id="${check}"`)
				want = monitoringUnresolvedReference
			case "text":
				c.Data = monitoringDashboardFixture()
				want = monitoringNoReference
			case "unknown":
				c.Data = monitoringDashboardFixture()
				c.Data["futureWidget"] = map[string]any{}
				want = monitoringUnresolvedReference
			case "metric-no-scope":
				c.Metrics = false
				want = monitoringNoReference
			case "ratio", "mql", "promql", "sql", "trace":
				q := map[string]any{}
				switch mode {
				case "ratio":
					q["timeSeriesFilterRatio"] = map[string]any{"numerator": map[string]any{"filter": `metric.type="other"`}, "denominator": map[string]any{"filter": uptimeMetricFilter}}
				case "mql":
					q["timeSeriesQueryLanguage"] = "PRIVATE_QUERY"
					want = monitoringUnresolvedReference
				case "promql":
					q["prometheusQuery"] = "PRIVATE_QUERY"
					want = monitoringUnresolvedReference
				case "sql":
					q["opsAnalyticsQuery"] = map[string]any{"sql": "PRIVATE_QUERY"}
					want = monitoringUnresolvedReference
				case "trace":
					q["traceQuery"] = map[string]any{}
					want = monitoringUnresolvedReference
				}
				data["gridLayout"] = map[string]any{"widgets": []any{map[string]any{"xyChart": map[string]any{"dataSets": []any{map[string]any{"timeSeriesQuery": q}}}}}}
			default:
				panel := map[string]any{"filter": `labels.check_id="public-check"`}
				switch mode {
				case "log-other":
					panel["filter"] = `labels.check_id="other"`
					want = monitoringNoReference
				case "log-template":
					panel["filter"] = `labels.check_id="${check}"`
					want = monitoringUnresolvedReference
				case "log-own":
					panel["resourceNames"] = []any{"projects/sample-project"}
				case "log-number":
					panel["resourceNames"] = []any{"projects/123456"}
				case "log-foreign":
					panel["resourceNames"] = []any{"projects/foreign-project"}
					want = monitoringUnresolvedReference
				case "log-view":
					panel["resourceNames"] = []any{"projects/sample-project/locations/global/buckets/archive/views/all"}
					want = monitoringUnresolvedReference
				case "log-no-route":
					c.Local = false
					want = monitoringNoReference
				case "log-explicit-source":
					c.Local = false
					c.Project = "foreign-project"
					c.ProjectNumber = "987654"
					panel["resourceNames"] = []any{"projects/123456"}
				case "log-route", "log-excluded":
					c.Local = false
					c.LogRoutes = []map[string]any{loggingSinkFixture("projects/sample-project", "forward", "logging.googleapis.com/projects/foreign-project")}
					c.LogRoutes[0]["filter"] = `labels.check_id="public-check"`
					if mode == "log-excluded" {
						c.LogRoutes[0]["filter"] = `labels.check_id="other"`
						want = monitoringNoReference
					}
				}
				data["gridLayout"] = map[string]any{"widgets": []any{map[string]any{"logsPanel": panel}}}
			}
			if got := c.reference("public-check"); got != want {
				t.Fatal(got, want)
			}
		})
	}
}

func TestMonitoringDashboardUptimeGraphAndDelete(t *testing.T) {
	for _, mode := range []string{"live", "missing", "stale", "closed", "foreign", "unknown", "denied", "page-denied", "get-missing", "get-drift", "changed", "absent", "late-reference", "prerequisite-live", "prerequisite-gone"} {
		t.Run(mode, func(t *testing.T) {
			s := newMonitoringGroupConsumerScenario(t)
			s.values["groups"] = nil
			s.values["alertPolicies"] = nil
			s.values["dashboards"][0] = dashboardUptimeFixture(uptimeMetricFilter + ` AND metric.labels.check_id="public-check"`)
			d := monitoringDashboardAsset(t, s)
			fallback, _, _, _, _ := uptimeScenario(t)
			base := s.r.transport
			deletes := 0
			gone := false
			s.r = protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
				if req.URL.Path == "/v1/locations/global/metricsScopes:listMetricsScopesByMonitoredProject" || req.URL.Host == loggingHost {
					return fallback.transport.RoundTrip(req)
				}
				if req.URL.Path == "/v3/"+uptimeName {
					if req.Method == "DELETE" {
						deletes++
						gone = true
						return apiResponse(req, 200, `{}`), nil
					}
					if gone {
						return apiResponse(req, 404, `{}`), nil
					}
				}
				if mode == "late-reference" && req.URL.Path == "/v1/projects/sample-project/dashboards" && s.lists["dashboards"] < 2 {
					s.lists["dashboards"]++
					return apiResponse(req, 200, `{"dashboards":[]}`), nil
				}
				return base.RoundTrip(req)
			})
			target := s.assets[2]
			request := contracts.ActionRequest{Asset: target, Action: "delete", IdempotencyKey: "dashboard-uptime"}
			switch mode {
			case "stale":
				d.Normalized[monitoringDashboardReview] = "stale"
			case "closed":
				now := d.LastSeenAt
				d.ClosedAt = &now
			case "foreign":
				d.Identity.ConnectionID = "other"
			case "unknown":
				s.values["dashboards"][0] = monitoringDashboardFixture()
				s.values["dashboards"][0]["unknownQuery"] = map[string]any{}
			case "absent", "prerequisite-gone":
				s.values["dashboards"] = nil
			}
			s.collection, s.mode = "dashboards", mode
			s.lists["dashboards"] = 0
			assets := []asset.Asset{target}
			if mode != "missing" {
				assets = append(assets, d)
			}
			if strings.HasPrefix(mode, "prerequisite-") {
				request.PrerequisiteDeletions = []contracts.ActionImpact{{Asset: d, ControllerID: target.ID, Delete: true}}
			}
			if mode != "late-reference" {
				contributor, err := s.r.MonitoringDependencies(t.Context(), "connection")
				if err != nil {
					t.Fatal(err)
				}
				result, err := contributor.Contribute(t.Context(), "global", assets)
				failed := mode == "denied" || mode == "page-denied" || mode == "get-missing" || mode == "get-drift" || mode == "changed"
				if (err != nil) != failed {
					t.Fatal(result, err)
				}
				if !failed {
					if mode == "live" || mode == "prerequisite-live" {
						if len(result.Relationships) != 1 || len(result.Unresolved) != 0 {
							t.Fatal(result)
						}
					} else if mode == "absent" || mode == "prerequisite-gone" {
						if len(result.Relationships)+len(result.Unresolved) != 0 {
							t.Fatal(result)
						}
					} else if len(result.Unresolved) != 1 {
						t.Fatal(result)
					}
					b, _ := json.Marshal(result)
					if strings.Contains(string(b), "PRIVATE_") {
						t.Fatal("private graph content")
					}
				}
			}
			s.lists["dashboards"] = 0
			driver, err := s.r.ResolveAction(t.Context(), "connection", target)
			if err != nil {
				t.Fatal(err)
			}
			result, err := driver.Execute(t.Context(), request)
			good := mode == "absent" || mode == "prerequisite-gone"
			if (err == nil) != good || deletes != map[bool]int{true: 1, false: 0}[good] {
				t.Fatal(result, err, deletes)
			}
			if good {
				fresh := protocolRuntime(t, s.r.transport.RoundTrip)
				driver, err = fresh.ResolveAction(t.Context(), "connection", target)
				if err != nil {
					t.Fatal(err)
				}
				restored := contracts.ActionResult{}
				if err = json.Unmarshal(mustDashboardJSON(t, result), &restored); err != nil {
					t.Fatal(err)
				}
				settled, err := driver.(contracts.MutationSettlementReader).MutationSettled(t.Context(), request, restored)
				if err != nil || !settled.Settled {
					t.Fatal(settled, err)
				}
				settled, err = driver.(contracts.MutationSettlementReader).MutationSettled(t.Context(), request, contracts.ActionResult{})
				if err != nil || settled.Settled {
					t.Fatal("empty receipt released scope", settled, err)
				}
			}
		})
	}
}

func TestMonitoringDashboardUptimeForeignScopes(t *testing.T) {
	for _, mode := range []string{"referenced", "unrelated", "empty", "denied", "page-denied", "get-missing", "get-drift", "wrong-project", "reverse-changed"} {
		t.Run(mode, func(t *testing.T) {
			s := monitoringDependencyFixture(t)
			s.data = nil
			data := dashboardUptimeFixture(uptimeMetricFilter)
			data["name"] = "projects/987654/dashboards/foreign"
			if mode == "unrelated" {
				data = dashboardUptimeFixture(`metric.type="compute.googleapis.com/usage"`)
				data["name"] = "projects/987654/dashboards/foreign"
			}
			reverse, lists, gets := 0, 0, 0
			r := protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
				if req.URL.Host == resourceManagerHost && req.URL.Path == "/v3/projects/987654" {
					return apiResponse(req, 200, `{"name":"projects/987654","projectId":"foreign-project","state":"ACTIVE"}`), nil
				}
				if req.URL.Path == "/v1/locations/global/metricsScopes:listMetricsScopesByMonitoredProject" {
					reverse++
					if mode == "reverse-changed" && reverse > 1 {
						return apiResponse(req, 200, `{"metricsScopes":[{"name":"locations/global/metricsScopes/123456"}]}`), nil
					}
					return apiResponse(req, 200, `{"metricsScopes":[{"name":"locations/global/metricsScopes/123456"},{"name":"locations/global/metricsScopes/987654"}]}`), nil
				}
				if strings.HasPrefix(req.URL.Path, "/v1/projects/foreign-project/dashboards") {
					if req.Method != "GET" || req.URL.Host != "monitoring.googleapis.com" {
						t.Fatal("foreign write", req.Method, req.URL)
					}
					if req.URL.Path == "/v1/projects/foreign-project/dashboards" {
						lists++
						if mode == "denied" {
							return apiResponse(req, 403, `{}`), nil
						}
						if mode == "page-denied" {
							if req.URL.Query().Get("pageToken") == "" {
								return apiResponse(req, 200, `{"nextPageToken":"next"}`), nil
							}
							return apiResponse(req, 403, `{}`), nil
						}
						if mode == "empty" {
							return apiResponse(req, 200, `{"dashboards":[]}`), nil
						}
						if mode == "wrong-project" {
							data["name"] = "projects/sample-project/dashboards/foreign"
						}
						return dataformResponse(req, 200, map[string]any{"dashboards": []any{data}}), nil
					}
					gets++
					if mode == "get-missing" {
						return apiResponse(req, 404, `{}`), nil
					}
					live := cloneParameters(data)
					if mode == "get-drift" {
						live["etag"] = "changed"
					}
					return dataformResponse(req, 200, live), nil
				}
				return s.r.transport.RoundTrip(req)
			})
			contributor, err := r.MonitoringDependencies(t.Context(), "connection")
			if err != nil {
				t.Fatal(err)
			}
			result, err := contributor.Contribute(t.Context(), "global", []asset.Asset{s.request.Asset})
			good := mode == "referenced" || mode == "unrelated" || mode == "empty"
			if (err == nil) != good {
				t.Fatal(result, err)
			}
			if !good {
				return
			}
			if lists != 2 || reverse != 2 {
				t.Fatal("incomplete foreign review", lists, gets, reverse)
			}
			if mode == "referenced" {
				if len(result.Relationships) != 0 || len(result.Unresolved) != 1 || result.Unresolved[0].NativeType != monitoringDashboardType || !result.Unresolved[0].BlocksCleanup {
					t.Fatal(result)
				}
			} else if len(result.Relationships)+len(result.Unresolved) != 0 {
				t.Fatal(result)
			}
			if *s.uptimeDeletes != 0 {
				t.Fatal("discovery deleted uptime")
			}
		})
	}
}

func TestMonitoringDashboardUptimeLoggingRoutes(t *testing.T) {
	for _, mode := range []string{"routed-log", "metric-outside-scope", "route-excluded", "explicit-source", "unknown-view"} {
		t.Run(mode, func(t *testing.T) {
			s := newLoggingRoutingScenario(t)
			object(object(array(s.foreign["conditions"])[0])["conditionMatchedLog"])["filter"] = `labels.check_id="other"`
			s.sinks["projects/sample-project"][0]["filter"] = `labels.check_id="public-check"`
			if mode == "route-excluded" {
				s.sinks["projects/sample-project"][0]["filter"] = `labels.check_id="other"`
			}
			data := monitoringDashboardFixture()
			data["name"] = "projects/987654/dashboards/foreign"
			panel := map[string]any{"filter": `labels.check_id="public-check"`}
			if mode == "explicit-source" {
				panel["resourceNames"] = []any{"projects/123456"}
			}
			if mode == "unknown-view" {
				panel["resourceNames"] = []any{"projects/foreign-project/locations/global/buckets/archive/views/all"}
			}
			data["gridLayout"] = map[string]any{"widgets": []any{map[string]any{"logsPanel": panel}}}
			if mode == "metric-outside-scope" {
				data = dashboardUptimeFixture(uptimeMetricFilter)
				data["name"] = "projects/987654/dashboards/foreign"
			}
			count := 0
			base := s.r.transport
			s.r = protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
				if strings.HasPrefix(req.URL.Path, "/v1/projects/foreign-project/dashboards") {
					if req.Method != "GET" {
						t.Fatal(req.Method)
					}
					count++
					if req.URL.Path == "/v1/projects/foreign-project/dashboards" {
						return dataformResponse(req, 200, map[string]any{"dashboards": []any{data}}), nil
					}
					return dataformResponse(req, 200, data), nil
				}
				return base.RoundTrip(req)
			})
			contributor, err := s.r.MonitoringDependencies(t.Context(), "connection")
			if err != nil {
				t.Fatal(err)
			}
			result, err := contributor.Contribute(t.Context(), "global", []asset.Asset{s.request.Asset})
			if err != nil {
				t.Fatal(err)
			}
			if mode == "metric-outside-scope" || mode == "route-excluded" {
				if len(result.Relationships)+len(result.Unresolved) != 0 {
					t.Fatal(result)
				}
			} else if len(result.Unresolved) != 1 || result.Unresolved[0].NativeType != monitoringDashboardType {
				t.Fatal(result)
			}
			if (count == 0) != (mode == "route-excluded") {
				t.Fatal("wrong routed scope", count)
			}
		})
	}
}
