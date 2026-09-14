package gcp

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func dashboardPolicyWidget(kind, name string) map[string]any {
	if kind == "alertChart" {
		return map[string]any{kind: map[string]any{"name": name}}
	}
	return map[string]any{kind: map[string]any{"policyNames": []any{name}}}
}
func TestMonitoringDashboardPolicyNativeReferences(t *testing.T) {
	s := newMonitoringGroupConsumerScenario(t)
	c, err := s.r.resolve(t.Context(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	for _, layout := range []string{"gridLayout", "mosaicLayout", "rowLayout", "columnLayout"} {
		for _, mode := range []string{"chart", "number", "other", "foreign", "incident", "incident-other", "incident-full", "all-incidents", "resource-incidents", "text", "log", "query", "unknown", "bad-name", "template", "nil-names"} {
			t.Run(layout+"/"+mode, func(t *testing.T) {
				d := monitoringDashboardFixture()
				delete(d, "gridLayout")
				w := dashboardPolicyWidget("alertChart", alertPolicyName)
				want := monitoringHasReference
				switch mode {
				case "number":
					w = dashboardPolicyWidget("alertChart", strings.Replace(alertPolicyName, "sample-project", "123456", 1))
				case "other":
					w = dashboardPolicyWidget("alertChart", alertPolicyName+"-other")
					want = monitoringNoReference
				case "foreign":
					w = dashboardPolicyWidget("alertChart", strings.Replace(alertPolicyName, "sample-project", "foreign-project", 1))
					want = monitoringNoReference
				case "incident":
					w = dashboardPolicyWidget("incidentList", "alertPolicies/"+last(alertPolicyName))
				case "incident-other":
					w = dashboardPolicyWidget("incidentList", "alertPolicies/other")
					want = monitoringNoReference
				case "incident-full":
					w = dashboardPolicyWidget("incidentList", alertPolicyName)
					want = monitoringUnresolvedReference
				case "all-incidents":
					w = map[string]any{"incidentList": map[string]any{}}
				case "resource-incidents":
					w = map[string]any{"incidentList": map[string]any{"policyNames": []any{}, "monitoredResources": []any{map[string]any{"type": "gce_instance"}}}}
				case "text":
					w = map[string]any{"text": map[string]any{"content": alertPolicyName, "format": "MARKDOWN"}}
					want = monitoringNoReference
				case "log":
					w = map[string]any{"logsPanel": map[string]any{"filter": alertPolicyName}}
					want = monitoringNoReference
				case "query":
					w = map[string]any{"xyChart": map[string]any{"dataSets": []any{map[string]any{"timeSeriesQuery": map[string]any{"prometheusQuery": alertPolicyName}}}}}
					want = monitoringNoReference
				case "unknown":
					w = map[string]any{"futureWidget": map[string]any{"name": alertPolicyName}}
					want = monitoringUnresolvedReference
				case "bad-name":
					w = dashboardPolicyWidget("alertChart", "projects/sample-project/alertPolicies/../policy")
					want = monitoringUnresolvedReference
				case "template":
					w = dashboardPolicyWidget("alertChart", "projects/sample-project/alertPolicies/${policy}")
					want = monitoringUnresolvedReference
				case "nil-names":
					w = map[string]any{"incidentList": map[string]any{"policyNames": nil}}
					want = monitoringUnresolvedReference
				}
				switch layout {
				case "gridLayout":
					d[layout] = map[string]any{"widgets": []any{w}}
				case "mosaicLayout":
					d[layout] = map[string]any{"tiles": []any{map[string]any{"widget": w}}}
				case "rowLayout":
					d[layout] = map[string]any{"rows": []any{map[string]any{"widgets": []any{w}}}}
				case "columnLayout":
					d[layout] = map[string]any{"columns": []any{map[string]any{"widgets": []any{w}}}}
				}
				if got := c.monitoringDashboardPolicyReference(d, alertPolicyID); got != want {
					t.Fatal(got, want)
				}
			})
		}
	}
}
func TestMonitoringDashboardPolicyGraphReview(t *testing.T) {
	for _, mode := range []string{"normal", "missing", "stale", "foreign", "closed", "unknown", "denied", "get-missing", "get-drift", "page-denied", "changed", "target-changed"} {
		t.Run(mode, func(t *testing.T) {
			s := newMonitoringGroupConsumerScenario(t)
			s.values["dashboards"][0]["gridLayout"] = map[string]any{"widgets": []any{dashboardPolicyWidget("alertChart", alertPolicyName)}}
			if mode == "unknown" {
				s.values["dashboards"][0]["futureWidget"] = map[string]any{"policy": "PRIVATE_POLICY"}
			}
			a := monitoringDashboardAsset(t, s)
			assets := s.assets[3:4]
			switch mode {
			case "stale":
				a.Normalized[monitoringDashboardReview] = "stale"
			case "foreign":
				a.Identity.ConnectionID = "other"
			case "closed":
				now := a.LastSeenAt
				a.ClosedAt = &now
			case "target-changed":
				s.values["alertPolicies"][0]["displayName"] = "changed"
			}
			if mode != "missing" {
				assets = append(assets, a)
			}
			s.collection, s.mode = "dashboards", mode
			s.lists["dashboards"] = 0
			c, err := s.r.resolve(t.Context(), "connection")
			if err != nil {
				t.Fatal(err)
			}
			result, err := (&monitoringDependencies{client: c, connection: "connection"}).monitoringDashboardPolicyDependencies(t.Context(), assets)
			failed := mode == "denied" || mode == "get-missing" || mode == "get-drift" || mode == "page-denied" || mode == "changed" || mode == "target-changed"
			if (err != nil) != failed {
				t.Fatal(result, err)
			}
			if failed {
				return
			}
			good := mode == "normal" || mode == "unknown" // A known reference survives unrelated unknown content.
			if good {
				if len(result.Relationships) != 1 || len(result.Unresolved) != 0 {
					t.Fatal(result)
				}
			} else if len(result.Relationships) != 0 || len(result.Unresolved) != 1 {
				t.Fatal(result)
			}
			b, _ := json.Marshal(result)
			if strings.Contains(string(b), "PRIVATE_") {
				t.Fatal("private graph evidence")
			}
		})
	}
}
func TestMonitoringDashboardPolicyDeleteGuards(t *testing.T) {
	for _, mode := range []string{"live", "absent", "unknown", "denied", "page-denied", "get-missing", "get-drift", "changed", "late-reference", "prerequisite-live", "prerequisite-gone", "prerequisite-stale", "prerequisite-foreign"} {
		t.Run(mode, func(t *testing.T) {
			s := newMonitoringGroupConsumerScenario(t)
			s.values["dashboards"][0]["gridLayout"] = map[string]any{"widgets": []any{dashboardPolicyWidget("alertChart", alertPolicyName)}}
			d := monitoringDashboardAsset(t, s)
			request := contracts.ActionRequest{Asset: s.assets[3], Action: "delete", IdempotencyKey: "policy-dashboard"}
			if strings.HasPrefix(mode, "prerequisite-") {
				request.PrerequisiteDeletions = []contracts.ActionImpact{{Asset: d, ControllerID: request.Asset.ID, Delete: true}}
				if mode == "prerequisite-stale" {
					request.PrerequisiteDeletions[0].Asset.Normalized[monitoringDashboardReview] = "stale"
				}
				if mode == "prerequisite-foreign" {
					request.PrerequisiteDeletions[0].Asset.Identity.ConnectionID = "other"
				}
			}
			if mode == "absent" || mode == "prerequisite-gone" {
				s.values["dashboards"] = nil
			}
			if mode == "unknown" {
				s.values["dashboards"][0]["gridLayout"] = map[string]any{"widgets": []any{map[string]any{"futureWidget": map[string]any{}}}}
			}
			s.collection, s.mode = "dashboards", mode
			s.lists["dashboards"] = 0
			transport := s.r.transport
			deleted := false
			deletes := 0
			s.r = protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
				if req.URL.Path == "/v3/"+alertPolicyName {
					if req.Method == "DELETE" {
						deletes++
						if req.URL.RawQuery != "" {
							t.Fatal(req.URL)
						}
						deleted = true
						return apiResponse(req, 200, `{}`), nil
					}
					if deleted {
						return apiResponse(req, 404, `{}`), nil
					}
				}
				if mode == "late-reference" && req.URL.Path == "/v1/projects/sample-project/dashboards" && s.lists["dashboards"] < 2 {
					s.lists["dashboards"]++
					return apiResponse(req, 200, `{"dashboards":[]}`), nil
				}
				return transport.RoundTrip(req)
			})
			driver, err := s.r.ResolveAction(t.Context(), "connection", request.Asset)
			if err != nil {
				t.Fatal(err)
			}
			result, err := driver.Execute(t.Context(), request)
			good := mode == "absent" || mode == "prerequisite-gone"
			if (err == nil) != good || deletes != map[bool]int{true: 1, false: 0}[good] {
				t.Fatal(result, err, deletes)
			}
			if !good {
				return
			}
			fresh := protocolRuntime(t, s.r.transport.RoundTrip)
			driver, err = fresh.ResolveAction(t.Context(), "connection", request.Asset)
			if err != nil {
				t.Fatal(err)
			}
			restored := contracts.ActionResult{}
			if err = json.Unmarshal(mustDashboardJSON(t, result), &restored); err != nil {
				t.Fatal(err)
			}
			wait, err := driver.Wait(t.Context(), request, restored)
			if err != nil || !wait.Done {
				t.Fatal(wait, err)
			}
			if mode == "prerequisite-gone" {
				request.PrerequisiteDeletions = nil
				if _, err = driver.Wait(t.Context(), request, restored); err == nil {
					t.Fatal("prerequisite substitution accepted")
				}
			}
		})
	}
}
