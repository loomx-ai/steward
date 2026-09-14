package gcp

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/catalog"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

type monitoringGroupDeleteScenario struct {
	*monitoringGroupConsumerScenario
	request  contracts.ActionRequest
	state    string
	gone     bool
	ownReads int
	deletes  int
}

func newMonitoringGroupDeleteScenario(t *testing.T) *monitoringGroupDeleteScenario {
	t.Helper()
	base := newMonitoringGroupConsumerScenario(t)
	s := &monitoringGroupDeleteScenario{monitoringGroupConsumerScenario: base, request: contracts.ActionRequest{Asset: base.assets[0], Action: "delete", IdempotencyKey: "group-reviewed-delete"}}
	base.values["groups"] = base.values["groups"][:1]
	base.values["uptimeCheckConfigs"] = nil
	base.values["alertPolicies"] = nil
	transport := base.r.transport
	base.r = protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
		if req.URL.Path == "/v3/"+testMonitoringGroupName {
			if req.Method == "DELETE" {
				s.deletes++
				if req.URL.Host != "monitoring.googleapis.com" || req.URL.Query().Get("recursive") != "false" || len(req.URL.Query()) != 1 {
					t.Fatal("recursive or foreign DELETE", req.URL)
				}
				if req.Body != nil {
					b, _ := io.ReadAll(req.Body)
					if len(b) != 0 {
						t.Fatal("group DELETE body", string(b))
					}
				}
				switch s.state {
				case "delete-denied":
					return apiResponse(req, 403, `{}`), nil
				case "delete-busy":
					return apiResponse(req, 400, `{"error":{"code":400,"message":"group has descendants"}}`), nil
				case "delete-404-live":
					return apiResponse(req, 404, `{}`), nil
				case "delete-404-gone":
					s.gone = true
					return apiResponse(req, 404, `{}`), nil
				case "lost-response":
					s.gone = true
					return nil, errors.New("test response lost")
				case "delete-operation":
					return apiResponse(req, 200, `{"name":"operations/not-native"}`), nil
				case "delete-malformed":
					return apiResponse(req, 200, `not-json`), nil
				case "pending":
					return apiResponse(req, 200, `{}`), nil
				}
				s.gone = true
				response := apiResponse(req, 200, `{}`)
				response.Header.Set("X-Goog-Request-Id", "group-delete-request")
				return response, nil
			}
			if req.Method == "GET" {
				s.ownReads++
				if s.collection == "groups" && s.mode == "get-missing" && s.ownReads == 1 {
					return dataformResponse(req, 200, base.values["groups"][0]), nil
				}
				if s.gone {
					return apiResponse(req, 404, `{}`), nil
				}
				switch s.state {
				case "get-denied":
					return apiResponse(req, 403, `{}`), nil
				case "target-drift":
					if s.ownReads >= 5 {
						base.values["groups"][0]["displayName"] = "changed-before-write"
					}
				}
			}
		}
		if s.state == "late-reference" && req.URL.Path == "/v3/projects/sample-project/uptimeCheckConfigs" && base.lists["uptimeCheckConfigs"] >= 2 {
			check := uptimeFixture()
			delete(check, "monitoredResource")
			check["resourceGroup"] = map[string]any{"groupId": "9876", "resourceType": "INSTANCE"}
			base.values["uptimeCheckConfigs"] = []map[string]any{check}
		}
		return transport.RoundTrip(req)
	})
	return s
}
func TestMonitoringGroupReviewedDeleteAndRestart(t *testing.T) {
	for _, mode := range []string{"normal", "gone", "pending", "get-denied", "delete-denied", "delete-busy", "delete-404-live", "delete-404-gone", "lost-response", "delete-operation", "delete-malformed", "target-drift", "late-reference", "changed-filter", "changed-parent", "changed-cluster", "changed-unknown", "missing-proof", "empty-key", "recursive", "foreign-connection", "wrong-identity", "wrong-partition", "impacts", "prerequisite-live", "prerequisite-gone", "prerequisite-wrong-kind", "prerequisite-wrong-connection", "prerequisite-duplicate", "prerequisite-self"} {
		t.Run(mode, func(t *testing.T) {
			s := newMonitoringGroupDeleteScenario(t)
			s.state = mode
			group := s.values["groups"][0]
			switch mode {
			case "gone":
				s.gone = true
			case "changed-filter":
				group["filter"] = `resource.type="other"`
			case "changed-parent":
				group["parentName"] = "projects/sample-project/groups/parent"
			case "changed-cluster":
				group["isCluster"] = true
			case "changed-unknown":
				group["futureField"] = "changed"
			case "missing-proof":
				delete(s.request.Asset.Normalized, monitoringGroupReview)
			case "empty-key":
				s.request.IdempotencyKey = ""
			case "recursive":
				s.request.Parameters = map[string]any{"recursive": true}
			case "impacts":
				s.request.LifecycleImpacts = []contracts.ActionImpact{{Asset: s.assets[1], ControllerID: s.request.Asset.ID, Delete: true}}
			}
			if strings.HasPrefix(mode, "prerequisite-") {
				s.request.PrerequisiteDeletions = []contracts.ActionImpact{{Asset: s.assets[1], ControllerID: s.request.Asset.ID, Delete: true}}
				switch mode {
				case "prerequisite-live":
					child := monitoringGroupFixture()
					child["name"] = "projects/sample-project/groups/child"
					child["parentName"] = testMonitoringGroupName
					s.values["groups"] = append(s.values["groups"], child)
				case "prerequisite-wrong-kind":
					s.request.PrerequisiteDeletions[0].Asset.Identity.NativeType = monitoringDashboardType
				case "prerequisite-wrong-connection":
					s.request.PrerequisiteDeletions[0].Asset.Identity.ConnectionID = "other"
				case "prerequisite-duplicate":
					s.request.PrerequisiteDeletions = append(s.request.PrerequisiteDeletions, s.request.PrerequisiteDeletions[0])
				case "prerequisite-self":
					s.request.PrerequisiteDeletions[0].Asset = s.request.Asset
				}
			}
			connection := asset.ConnectionID("connection")
			if mode == "foreign-connection" {
				connection = "other"
			}
			driver, err := s.r.ResolveAction(t.Context(), connection, s.request.Asset)
			if mode == "foreign-connection" {
				if err == nil || s.deletes != 0 {
					t.Fatal("foreign action accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if mode == "wrong-identity" {
				s.request.Asset.Identity.NativeID += "-changed"
			}
			if mode == "wrong-partition" {
				s.request.Asset.Identity.Partition = "other"
			}
			result, err := driver.Execute(t.Context(), s.request)
			good := mode == "normal" || mode == "gone" || mode == "pending" || mode == "delete-404-gone" || mode == "prerequisite-gone"
			if (err == nil) != good {
				t.Fatal(mode, result, err)
			}
			if !good {
				expected := 0
				if strings.HasPrefix(mode, "delete-") || mode == "lost-response" {
					expected = 1
				}
				if s.deletes != expected {
					t.Fatal("unexpected DELETE count", s.deletes, expected)
				}
				if mode == "lost-response" {
					settled, err := driver.(contracts.MutationSettlementReader).MutationSettled(t.Context(), s.request, result)
					if err != nil || settled.Settled {
						t.Fatal("lost response released scope", settled, err)
					}
				}
				return
			}
			if mode == "gone" {
				if s.deletes != 0 || len(result.Data) != 0 {
					t.Fatal(result, s.deletes)
				}
				return
			}
			if s.deletes != 1 || result.ProviderOperationID != "" || result.Data["phase"] != "monitoring_group_delete" || s.lists["groups"] != 4 || s.ownReads < 5 {
				t.Fatal(result, s.deletes, s.lists, s.ownReads)
			}
			payload, _ := json.Marshal(result)
			if strings.Contains(string(payload), "PRIVATE_") {
				t.Fatal("private review escaped")
			}
			restored := contracts.ActionResult{}
			if err = json.Unmarshal(payload, &restored); err != nil {
				t.Fatal(err)
			}
			fresh := protocolRuntime(t, s.r.transport.RoundTrip)
			driver, err = fresh.ResolveAction(t.Context(), "connection", s.request.Asset)
			if err != nil {
				t.Fatal(err)
			}
			wait, err := driver.Wait(t.Context(), s.request, restored)
			if err != nil || wait.Done == (mode == "pending") {
				t.Fatal(wait, err)
			}
			if mode == "pending" {
				s.gone = true
				wait, err = driver.Wait(t.Context(), s.request, restored)
				if err != nil || !wait.Done {
					t.Fatal(wait, err)
				}
			}
			settled, err := driver.(contracts.MutationSettlementReader).MutationSettled(t.Context(), s.request, restored)
			if err != nil || !settled.Settled {
				t.Fatal(settled, err)
			}
			settled, err = driver.(contracts.MutationSettlementReader).MutationSettled(t.Context(), s.request, contracts.ActionResult{})
			if err != nil || settled.Settled {
				t.Fatal("empty response released scope", settled, err)
			}
			for _, changed := range []string{"key", "proof", "phase", "operation", "prerequisite"} {
				request := s.request
				receipt := restored
				receipt.Data = cloneParameters(restored.Data)
				switch changed {
				case "key":
					request.IdempotencyKey = "other"
				case "proof":
					request.Asset.Normalized = cloneParameters(request.Asset.Normalized)
					request.Asset.Normalized[monitoringGroupReview] = strings.Repeat("0", 64)
				case "phase":
					receipt.Data["phase"] = "alert_policy_delete"
				case "operation":
					receipt.ProviderOperationID = "operations/not-native"
				case "prerequisite":
					if len(request.PrerequisiteDeletions) == 0 {
						request.PrerequisiteDeletions = []contracts.ActionImpact{{Asset: s.assets[1], ControllerID: request.Asset.ID, Delete: true}}
					} else {
						request.PrerequisiteDeletions = nil
					}
				}
				if _, err = driver.Wait(t.Context(), request, receipt); err == nil {
					t.Fatal("changed receipt accepted", changed)
				}
			}
		})
	}
}
func TestMonitoringGroupDeleteConsumersAndReadFailures(t *testing.T) {
	for _, collection := range []string{"groups", "uptimeCheckConfigs", "alertPolicies", "dashboards"} {
		for _, mode := range []string{"referenced", "unresolved", "denied", "page-denied", "get-missing", "get-drift", "null", "loop", "changed"} {
			t.Run(collection+"/"+mode, func(t *testing.T) {
				s := newMonitoringGroupDeleteScenario(t)
				fresh := newMonitoringGroupConsumerScenario(t)
				s.values[collection] = fresh.values[collection]
				s.collection, s.mode = collection, mode
				if mode == "referenced" || mode == "unresolved" {
					s.mode = "normal"
					if collection == "dashboards" {
						s.values[collection][0]["dashboardFilters"] = []any{map[string]any{"filterType": "GROUP", "stringValue": "9876"}}
					}
					if mode == "unresolved" && collection == "alertPolicies" {
						object(object(array(s.values[collection][0]["conditions"])[0])["conditionThreshold"])["filter"] = "PRIVATE_UNKNOWN"
					}
				}
				driver, err := s.r.ResolveAction(t.Context(), "connection", s.request.Asset)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = driver.Execute(t.Context(), s.request); err == nil || s.deletes != 0 {
					t.Fatal("consumer/read failure allowed DELETE", err, s.deletes)
				}
			})
		}
	}
}
func TestMonitoringGroupDeleteNativeBindingAndNoBypass(t *testing.T) {
	s := newMonitoringGroupDeleteScenario(t)
	metadata, err := providerData()
	if err != nil {
		t.Fatal(err)
	}
	op, ok := metadata.catalog.Operation(monitoringGroupDelete)
	if !ok {
		t.Fatal("missing native DELETE")
	}
	bound, err := catalog.BindREST(op, map[string]any{"name": testMonitoringGroupName, "recursive": false})
	if err != nil || bound.Method != "DELETE" || !strings.Contains(bound.URL, "/v3/"+testMonitoringGroupName+"?recursive=false") || len(bound.Body) != 0 {
		t.Fatal(bound, err)
	}
	for _, recursive := range []bool{false, true} {
		if _, err = s.r.Invoke(t.Context(), contracts.Invocation{ConnectionID: "connection", Operation: monitoringGroupDelete, Parameters: map[string]any{"name": testMonitoringGroupName, "recursive": recursive}}); err == nil {
			t.Fatal("unreviewed Invoke accepted")
		}
	}
	if s.deletes != 0 {
		t.Fatal("unreviewed write")
	}
}
