package gcp

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func monitoringDashboardAsset(t *testing.T, s *monitoringGroupConsumerScenario) asset.Asset {
	t.Helper()
	batch, err := s.r.List(t.Context(), productRequest(s.r, monitoringDashboardType, "global"))
	if err != nil || !batch.Complete || len(batch.Items) != 1 {
		t.Fatal(batch, err)
	}
	item := batch.Items[0]
	k := s.r.resourceKind(monitoringDashboardType)
	return asset.Asset{ID: "dashboard", ResourceKindID: k.ID, Identity: asset.Identity{Provider: asset.ProviderGCP, Partition: "gcp", ConnectionID: "connection", NativeType: monitoringDashboardType, NativeID: item.NativeID}, Normalized: item.Normalized, Capabilities: k.Capabilities}
}

func TestMonitoringDashboardInventoryAndPrivacy(t *testing.T) {
	for _, mode := range []string{"normal", "denied", "list-404", "null", "element", "partial", "token-null", "get-denied", "get-missing", "get-drift", "no-etag", "system", "wrong-project", "two-layouts", "null-layout", "bad-labels", "bad-filters"} {
		t.Run(mode, func(t *testing.T) {
			s := newMonitoringGroupConsumerScenario(t)
			s.collection, s.mode = "dashboards", mode
			row := s.values["dashboards"][0]
			row["futureField"] = map[string]any{"network": "PRIVATE_NETWORK"}
			switch mode {
			case "no-etag":
				delete(row, "etag")
			case "system":
				row["name"] = "dashboards/system"
			case "wrong-project":
				row["name"] = "projects/foreign/dashboards/example"
			case "two-layouts":
				row["rowLayout"] = map[string]any{}
			case "null-layout":
				row["gridLayout"] = nil
			case "bad-labels":
				row["labels"] = map[string]any{"x": false}
			case "bad-filters":
				row["dashboardFilters"] = []any{nil}
			}
			batch, err := s.r.List(t.Context(), productRequest(s.r, monitoringDashboardType, "global"))
			if mode != "normal" {
				if err == nil || batch.Complete || len(batch.Items) != 0 {
					t.Fatal("unsafe inventory", batch, err)
				}
				return
			}
			if err != nil || !batch.Complete || len(batch.Items) != 1 {
				t.Fatal(batch, err)
			}
			b, _ := json.Marshal(batch)
			if strings.Contains(string(b), "PRIVATE_") {
				t.Fatal("private content persisted")
			}
			if len(text(batch.Items[0].Normalized[monitoringDashboardReview])) != 64 {
				t.Fatal("missing review")
			}
			result, err := s.r.Invoke(t.Context(), contracts.Invocation{ConnectionID: "connection", Operation: "monitoring.projects.dashboards.get", Parameters: map[string]any{"name": row["name"]}})
			b, _ = json.Marshal(result)
			if err != nil || strings.Contains(string(b), "PRIVATE_") {
				t.Fatal("private Invoke content", err)
			}
			if !strings.Contains(string(mustDashboardJSON(t, row)), "PRIVATE_") {
				t.Fatal("redaction mutated native state")
			}
		})
	}
}
func mustDashboardJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestMonitoringDashboardReviewedDeleteAndRestart(t *testing.T) {
	for _, mode := range []string{"normal", "gone", "pending", "get-denied", "delete-denied", "delete-404-live", "delete-404-gone", "lost-response", "delete-operation", "delete-malformed", "changed-etag", "changed-layout", "changed-unknown", "protected", "missing-proof", "empty-key", "parameters", "wrong-identity", "wrong-partition", "impacts", "prerequisite", "late-drift"} {
		t.Run(mode, func(t *testing.T) {
			s := newMonitoringGroupConsumerScenario(t)
			row := s.values["dashboards"][0]
			if mode == "protected" {
				row["labels"] = map[string]any{"steward-protected": "true"}
			}
			a := monitoringDashboardAsset(t, s)
			request := contracts.ActionRequest{Asset: a, Action: "delete", IdempotencyKey: "dashboard-delete"}
			switch mode {
			case "changed-etag":
				row["etag"] = "changed"
			case "changed-layout":
				row["gridLayout"] = map[string]any{}
			case "changed-unknown":
				row["futureField"] = "PRIVATE_CHANGED"
			case "missing-proof":
				delete(request.Asset.Normalized, monitoringDashboardReview)
			case "empty-key":
				request.IdempotencyKey = ""
			case "parameters":
				request.Parameters = map[string]any{"etag": "version-1"}
			case "impacts":
				request.LifecycleImpacts = []contracts.ActionImpact{{Asset: s.assets[0], ControllerID: a.ID, Delete: true}}
			case "prerequisite":
				request.PrerequisiteDeletions = []contracts.ActionImpact{{Asset: s.assets[0], ControllerID: a.ID, Delete: true}}
			}
			gone := mode == "gone"
			deletes, reads := 0, 0
			transport := s.r.transport
			r := protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
				if req.URL.Path != "/v1/projects/sample-project/dashboards/example" {
					t.Fatal(req.URL)
				}
				if req.Method == "GET" {
					reads++
					if gone {
						return apiResponse(req, 404, `{}`), nil
					}
					if mode == "get-denied" {
						return apiResponse(req, 403, `{}`), nil
					}
					if mode == "late-drift" && reads >= 2 {
						row["etag"] = "late"
					}
					return transport.RoundTrip(req)
				}
				if req.Method != "DELETE" || req.URL.Host != "monitoring.googleapis.com" || req.URL.RawQuery != "" {
					t.Fatal(req.Method, req.URL)
				}
				if req.Body != nil {
					b, _ := io.ReadAll(req.Body)
					if len(b) != 0 {
						t.Fatal("DELETE body")
					}
				}
				deletes++
				switch mode {
				case "delete-denied":
					return apiResponse(req, 403, `{}`), nil
				case "delete-404-live":
					return apiResponse(req, 404, `{}`), nil
				case "delete-404-gone":
					gone = true
					return apiResponse(req, 404, `{}`), nil
				case "lost-response":
					gone = true
					return nil, errors.New("response lost")
				case "delete-operation":
					return apiResponse(req, 200, `{"name":"operations/invalid"}`), nil
				case "delete-malformed":
					return apiResponse(req, 200, `broken`), nil
				case "pending":
					return apiResponse(req, 200, `{}`), nil
				}
				gone = true
				return apiResponse(req, 200, `{}`), nil
			})
			driver, err := r.ResolveAction(t.Context(), "connection", a)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "wrong-identity" {
				request.Asset.Identity.NativeID += "-other"
			}
			if mode == "wrong-partition" {
				request.Asset.Identity.Partition = "other"
			}
			result, err := driver.Execute(t.Context(), request)
			good := mode == "normal" || mode == "gone" || mode == "pending" || mode == "delete-404-gone"
			if (err == nil) != good {
				t.Fatal(mode, result, err)
			}
			if !good {
				want := 0
				if strings.HasPrefix(mode, "delete-") || mode == "lost-response" {
					want = 1
				}
				if deletes != want {
					t.Fatal(deletes, want)
				}
				if mode == "lost-response" {
					settled, e := driver.(contracts.MutationSettlementReader).MutationSettled(t.Context(), request, result)
					if e != nil || settled.Settled {
						t.Fatal(settled, e)
					}
				}
				return
			}
			if mode == "gone" {
				if deletes != 0 {
					t.Fatal(deletes)
				}
				return
			}
			if deletes != 1 || reads < 2 || result.Data["phase"] != "monitoring_dashboard_delete" {
				t.Fatal(result, deletes, reads)
			}
			raw := mustDashboardJSON(t, result)
			if strings.Contains(string(raw), "PRIVATE_") {
				t.Fatal("private receipt")
			}
			restored := contracts.ActionResult{}
			if err = json.Unmarshal(raw, &restored); err != nil {
				t.Fatal(err)
			}
			fresh := protocolRuntime(t, r.transport.RoundTrip)
			driver, err = fresh.ResolveAction(t.Context(), "connection", a)
			if err != nil {
				t.Fatal(err)
			}
			wait, err := driver.Wait(t.Context(), request, restored)
			if err != nil || wait.Done == (mode == "pending") {
				t.Fatal(wait, err)
			}
			gone = true
			settled, err := driver.(contracts.MutationSettlementReader).MutationSettled(t.Context(), request, restored)
			if err != nil || !settled.Settled {
				t.Fatal(settled, err)
			}
			settled, err = driver.(contracts.MutationSettlementReader).MutationSettled(t.Context(), request, contracts.ActionResult{})
			if err != nil || settled.Settled {
				t.Fatal("empty receipt released scope", settled, err)
			}
			for _, change := range []string{"key", "proof", "phase", "operation", "prerequisite"} {
				changed := request
				receipt := restored
				receipt.Data = cloneParameters(restored.Data)
				switch change {
				case "key":
					changed.IdempotencyKey = "other"
				case "proof":
					changed.Asset.Normalized = cloneParameters(a.Normalized)
					changed.Asset.Normalized[monitoringDashboardReview] = strings.Repeat("0", 64)
				case "phase":
					receipt.Data["phase"] = "uptime_delete"
				case "operation":
					receipt.ProviderOperationID = "operations/invalid"
				case "prerequisite":
					changed.PrerequisiteDeletions = []contracts.ActionImpact{{Asset: s.assets[0], ControllerID: a.ID, Delete: true}}
				}
				if _, err = driver.Wait(t.Context(), changed, receipt); err == nil {
					t.Fatal("changed receipt accepted", change)
				}
			}
		})
	}
	s := newMonitoringGroupConsumerScenario(t)
	if _, err := s.r.Invoke(t.Context(), contracts.Invocation{ConnectionID: "connection", Operation: monitoringDashboardDelete, Parameters: map[string]any{"name": "projects/sample-project/dashboards/example"}}); err == nil {
		t.Fatal("unreviewed delete accepted")
	}
}

func TestMonitoringDashboardGroupConsumerReview(t *testing.T) {
	for _, mode := range []string{"normal", "missing", "stale", "foreign", "closed", "unknown"} {
		t.Run(mode, func(t *testing.T) {
			s := newMonitoringGroupConsumerScenario(t)
			s.values["dashboards"][0]["gridLayout"] = map[string]any{"widgets": []any{map[string]any{"xyChart": map[string]any{"dataSets": []any{map[string]any{"timeSeriesQuery": map[string]any{"timeSeriesFilter": map[string]any{"filter": `group.id="9876"`}}}}}}}}
			if mode == "unknown" {
				s.values["dashboards"][0]["gridLayout"] = map[string]any{"widgets": []any{map[string]any{"futureWidget": map[string]any{"query": "PRIVATE_QUERY"}}}}
			}
			a := monitoringDashboardAsset(t, s)
			switch mode {
			case "stale":
				a.Normalized[monitoringDashboardReview] = strings.Repeat("0", 64)
			case "foreign":
				a.Identity.ConnectionID = "other"
			case "closed":
				now := a.LastSeenAt
				a.ClosedAt = &now
			}
			if mode != "missing" {
				s.assets = append(s.assets, a)
			}
			c, err := s.r.resolve(t.Context(), "connection")
			if err != nil {
				t.Fatal(err)
			}
			result, err := (&monitoringDependencies{client: c, connection: "connection"}).monitoringGroupDependencies(t.Context(), s.assets)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "normal" {
				if len(result.Relationships) != 4 || len(result.Unresolved) != 0 {
					t.Fatal(result)
				}
			} else if len(result.Relationships) != 3 || len(result.Unresolved) == 0 {
				t.Fatal("unsafe consumer review", result)
			}
		})
	}
}
