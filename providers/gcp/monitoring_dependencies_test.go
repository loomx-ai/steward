package gcp

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

type monitoringDependencyScenario struct {
	r                              *Runtime
	request                        contracts.ActionRequest
	policy                         asset.Asset
	data                           map[string]any
	mode                           string
	lists, reverses, policyDeletes int
	uptimeDeletes                  *int
	uptimeMode                     *string
	uptimeData                     *map[string]any
}

func monitoringDependencyFixture(t *testing.T) *monitoringDependencyScenario {
	t.Helper()
	base, request, uptimeData, uptimeMode, deletes := uptimeScenario(t)
	s := &monitoringDependencyScenario{request: *request, data: alertPolicyFixture(), uptimeDeletes: deletes, uptimeMode: uptimeMode, uptimeData: uptimeData}
	object(object(array(s.data["conditions"])[0])["conditionThreshold"])["filter"] = uptimeMetricFilter + ` AND metric.labels.check_id="public-check"`
	s.r = protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
		if req.Method == "GET" && strings.HasPrefix(req.URL.Path, "/v3/projects/foreign-project/") {
			return dataformResponse(req, 200, map[string]any{"alertPolicies": []any{}}), nil
		}
		if req.Method == "GET" && req.URL.Path == "/v1/locations/global/metricsScopes:listMetricsScopesByMonitoredProject" {
			s.reverses++
			if req.URL.Query().Get("monitoredResourceContainer") != "projects/123456" || len(req.URL.Query()) != 1 {
				t.Fatal("incorrect reverse scope request")
			}
			switch s.mode {
			case "reverse-denied":
				return apiResponse(req, 403, `{}`), nil
			case "reverse-missing":
				return apiResponse(req, 200, `{}`), nil
			case "reverse-duplicate":
				return apiResponse(req, 200, `{"metricsScopes":[{"name":"locations/global/metricsScopes/123456"},{"name":"locations/global/metricsScopes/123456"}]}`), nil
			case "reverse-token":
				return apiResponse(req, 200, `{"metricsScopes":[{"name":"locations/global/metricsScopes/123456"}],"nextPageToken":"hidden"}`), nil
			case "reverse-foreign", "reverse-changed":
				if s.mode == "reverse-foreign" || s.reverses >= 2 {
					return apiResponse(req, 200, `{"metricsScopes":[{"name":"locations/global/metricsScopes/123456"},{"name":"locations/global/metricsScopes/987654"}]}`), nil
				}
			}
		}
		if req.Method == "GET" && req.URL.Path == "/v3/projects/sample-project/alertPolicies" {
			s.lists++
			if req.URL.Query().Get("filter") != "" || req.URL.Query().Get("pageSize") != "100" {
				t.Fatal("incomplete policy request")
			}
			switch s.mode {
			case "denied":
				return apiResponse(req, 403, `{}`), nil
			case "null":
				return apiResponse(req, 200, `{"alertPolicies":null}`), nil
			case "token-null":
				return apiResponse(req, 200, `{"nextPageToken":null}`), nil
			case "partial":
				return apiResponse(req, 200, `{"unreachable":["location"]}`), nil
			case "element":
				return apiResponse(req, 200, `{"alertPolicies":[null]}`), nil
			case "duplicate":
				return dataformResponse(req, 200, map[string]any{"alertPolicies": []any{s.data, s.data}}), nil
			case "token-loop":
				return apiResponse(req, 200, `{"nextPageToken":"same"}`), nil
			case "paged":
				if req.URL.Query().Get("pageToken") == "" {
					return apiResponse(req, 200, `{"nextPageToken":"second"}`), nil
				}
			case "changed":
				if s.lists >= 2 {
					return apiResponse(req, 200, `{}`), nil
				}
			case "late-policy":
				if s.lists < 3 {
					return apiResponse(req, 200, `{}`), nil
				}
			case "target-changed":
				if s.lists >= 2 {
					(*uptimeData)["displayName"] = "changed-during-policy-list"
				}
				return apiResponse(req, 200, `{}`), nil
			}
			if s.data == nil {
				return apiResponse(req, 200, `{}`), nil
			}
			return dataformResponse(req, 200, map[string]any{"alertPolicies": []any{s.data}, "totalSize": 999}), nil
		}
		if req.URL.Path == "/v3/"+alertPolicyName {
			if req.Method == "DELETE" {
				s.policyDeletes++
				s.data = nil
				return apiResponse(req, 200, `{}`), nil
			}
			if req.Method != "GET" {
				t.Fatal(req.Method)
			}
			if s.mode == "get-denied" {
				return apiResponse(req, 403, `{}`), nil
			}
			if s.data == nil || s.mode == "get-missing" {
				return apiResponse(req, 404, `{}`), nil
			}
			live := cloneParameters(s.data)
			if s.mode == "get-changed" {
				live["displayName"] = "changed-detail"
			}
			return dataformResponse(req, 200, live), nil
		}
		return base.transport.RoundTrip(req)
	})
	c, err := s.r.resolve(t.Context(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(s.data)
	copy := map[string]any{}
	_ = json.Unmarshal(payload, &copy)
	item, err := s.r.inventoryItem(c, map[string]any{"assetType": alertPolicyType, "name": alertPolicyID, "resource": map[string]any{"data": copy, "location": "global"}})
	if err != nil {
		t.Fatal(err)
	}
	s.request.Asset.Capabilities = asset.CapabilitySet{asset.CapabilityIndexed, asset.CapabilityActionable}
	s.policy = asset.Asset{ID: "policy", ScopeID: "global", Identity: s.request.Asset.Identity, Normalized: item.Normalized, Capabilities: s.request.Asset.Capabilities}
	s.policy.Identity.NativeType, s.policy.Identity.NativeID = alertPolicyType, alertPolicyID
	return s
}
func TestMonitoringDependencyNativeReads(t *testing.T) {
	for _, mode := range []string{"present", "paged", "empty", "denied", "null", "token-null", "partial", "element", "duplicate", "token-loop", "changed", "get-denied", "get-missing", "get-changed", "reverse-denied", "reverse-missing", "reverse-duplicate", "reverse-token", "reverse-changed"} {
		t.Run(mode, func(t *testing.T) {
			s := monitoringDependencyFixture(t)
			s.mode = mode
			if mode == "empty" {
				s.data = nil
			}
			c, _ := s.r.resolve(t.Context(), "connection")
			policies, err := c.monitoringPolicies(t.Context())
			good := mode == "present" || mode == "paged" || mode == "empty"
			if (err == nil) != good {
				t.Fatal(mode, err)
			}
			if good && ((mode == "empty") != (len(policies) == 0) || s.reverses != 2 || s.lists < 2) {
				t.Fatal("incomplete observation", s.reverses, s.lists, len(policies))
			}
			if *s.uptimeDeletes != 0 || s.policyDeletes != 0 {
				t.Fatal("read mutated cloud")
			}
		})
	}
}
func TestMonitoringDependencyGraphAndExplicitSelection(t *testing.T) {
	for _, mode := range []string{"present", "disabled", "missing", "stale", "closed", "foreign-connection", "duplicate", "unknown", "unrelated", "target-changed"} {
		t.Run(mode, func(t *testing.T) {
			s := monitoringDependencyFixture(t)
			candidate := s.policy
			values := []asset.Asset{s.request.Asset, candidate}
			switch mode {
			case "disabled":
				s.data["enabled"] = false
				candidate.Normalized[alertPolicyReview] = monitoringConfiguration(alertPolicyType, alertPolicyID, s.data)
			case "missing":
				values = values[:1]
			case "stale":
				candidate.Normalized[alertPolicyReview] = "stale"
			case "closed":
				now := time.Now()
				candidate.ClosedAt = &now
			case "foreign-connection":
				candidate.Identity.ConnectionID = "foreign"
			case "duplicate":
				values = append(values, candidate)
			case "unknown":
				object(array(s.data["conditions"])[0])["conditionThreshold"] = map[string]any{"filter": "PRIVATE_UNKNOWN"}
			case "unrelated":
				object(object(array(s.data["conditions"])[0])["conditionThreshold"])["filter"] = `metric.type="compute.googleapis.com/usage"`
			case "target-changed":
				s.mode = mode
			}
			if len(values) >= 2 {
				values[1] = candidate
			}
			contributor, err := s.r.MonitoringDependencies(t.Context(), "connection")
			if err != nil {
				t.Fatal(err)
			}
			result, err := contributor.Contribute(t.Context(), "global", values)
			if mode == "duplicate" || mode == "target-changed" {
				if err == nil {
					t.Fatal("invalid graph accepted")
				}
				return
			}
			if err != nil || len(result.Bindings) != 0 {
				t.Fatal(result, err)
			}
			encoded, _ := json.Marshal(result)
			if strings.Contains(string(encoded), "PRIVATE_") {
				t.Fatal("leaked expression")
			}
			if mode == "unrelated" {
				if len(result.Relationships)+len(result.Unresolved) != 0 {
					t.Fatal(result)
				}
				return
			}
			if mode != "present" && mode != "disabled" {
				if len(result.Unresolved) != 1 || !result.Unresolved[0].BlocksCleanup || len(result.Relationships) != 0 {
					t.Fatal(result)
				}
				return
			}
			if len(result.Relationships) != 1 || len(result.Unresolved) != 0 {
				t.Fatal(result)
			}
			relation := result.Relationships[0]
			if relation.SourceAssetID != s.request.Asset.ID || relation.TargetAssetID != candidate.ID || relation.Evidence[graph.RelationshipEvidenceAutomaticSelection] != false {
				t.Fatal(relation)
			}
			for _, selectPolicy := range []bool{false, true} {
				selected := []asset.AssetID{s.request.Asset.ID}
				if selectPolicy {
					selected = append(selected, candidate.ID)
				}
				planned, err := plan.Solve(plan.Input{Assets: values, Relationships: result.Relationships, ResolvedAssetIDs: selected})
				if err != nil {
					t.Fatal(err)
				}
				if !selectPolicy {
					if len(planned.Blockers) == 0 {
						t.Fatal("auto selected policy")
					}
					continue
				}
				if len(planned.Blockers) != 0 || len(planned.Steps) != 2 || planned.Steps[0].AssetID != candidate.ID || planned.Steps[1].AssetID != s.request.Asset.ID || !slices.Contains(planned.Steps[1].DependsOn, planned.Steps[0].ID) {
					t.Fatal(planned)
				}
				prerequisites, err := plan.RequiredDeletions(planned.Steps[1])
				if err != nil || len(prerequisites) != 1 || prerequisites[0].AssetID != candidate.ID {
					t.Fatal(prerequisites, err)
				}
			}
		})
	}
}
func TestMonitoringDependencyExecuteRechecksAndReceipt(t *testing.T) {
	for _, mode := range []string{"reference", "unknown", "log-reference", "unrelated", "absent", "late-policy", "target-changed", "denied", "prerequisite-live", "prerequisite-absent", "prerequisite-invalid", "prerequisite-duplicate"} {
		t.Run(mode, func(t *testing.T) {
			s := monitoringDependencyFixture(t)
			switch mode {
			case "unknown":
				object(object(array(s.data["conditions"])[0])["conditionThreshold"])["filter"] = "PRIVATE_UNKNOWN"
			case "log-reference":
				condition := object(array(s.data["conditions"])[0])
				delete(condition, "conditionThreshold")
				condition["conditionMatchedLog"] = map[string]any{"filter": `labels.check_id="public-check"`}
			case "unrelated":
				object(object(array(s.data["conditions"])[0])["conditionThreshold"])["filter"] = `metric.type="compute.googleapis.com/usage"`
			case "absent":
				s.data = nil
			case "late-policy", "target-changed", "denied":
				s.mode = mode
			}
			if strings.HasPrefix(mode, "prerequisite-") {
				s.request.PrerequisiteDeletions = []contracts.ActionImpact{{Asset: s.policy, ControllerID: s.request.Asset.ID, Delete: true}}
				if mode != "prerequisite-live" {
					s.data = nil
				}
				if mode == "prerequisite-invalid" {
					s.request.PrerequisiteDeletions[0].Delete = false
				}
				if mode == "prerequisite-duplicate" {
					s.request.PrerequisiteDeletions = append(s.request.PrerequisiteDeletions, s.request.PrerequisiteDeletions[0])
				}
			}
			driver, err := s.r.ResolveAction(t.Context(), "connection", s.request.Asset)
			if err != nil {
				t.Fatal(err)
			}
			result, err := driver.Execute(t.Context(), s.request)
			good := mode == "unrelated" || mode == "absent" || mode == "prerequisite-absent"
			if (err == nil) != good {
				t.Fatal(mode, err)
			}
			if !good {
				if *s.uptimeDeletes != 0 {
					t.Fatal("sent unsafe DELETE")
				}
				return
			}
			if *s.uptimeDeletes != 1 || s.policyDeletes != 0 {
				t.Fatal("incorrect mutations")
			}
			payload, _ := json.Marshal(result)
			result = contracts.ActionResult{}
			_ = json.Unmarshal(payload, &result)
			*s.uptimeMode = "gone"
			fresh := protocolRuntime(t, s.r.transport.RoundTrip)
			driver, err = fresh.ResolveAction(t.Context(), "connection", s.request.Asset)
			if err != nil {
				t.Fatal(err)
			}
			wait, err := driver.Wait(t.Context(), s.request, result)
			if err != nil || !wait.Done {
				t.Fatal(wait, err)
			}
			if mode == "prerequisite-absent" {
				s.request.PrerequisiteDeletions = nil
				if _, err := driver.Wait(t.Context(), s.request, result); err == nil {
					t.Fatal("removed prerequisite from receipt")
				}
			}
		})
	}
}

func TestMonitoringDependencyForeignScopingProject(t *testing.T) {
	for _, mode := range []string{"referenced", "empty", "denied", "wrong-project"} {
		t.Run(mode, func(t *testing.T) {
			s := monitoringDependencyFixture(t)
			payload, _ := json.Marshal(s.data)
			var foreign map[string]any
			_ = json.Unmarshal([]byte(strings.ReplaceAll(string(payload), "sample-project", "987654")), &foreign)
			s.data = nil
			foreignGets := 0
			r := protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
				if req.URL.String() == "https://cloudresourcemanager.googleapis.com/v3/projects/987654" {
					return apiResponse(req, 200, `{"name":"projects/987654","projectId":"foreign-project","state":"ACTIVE"}`), nil
				}
				if req.URL.Path == "/v1/locations/global/metricsScopes:listMetricsScopesByMonitoredProject" {
					return apiResponse(req, 200, `{"metricsScopes":[{"name":"locations/global/metricsScopes/123456"},{"name":"locations/global/metricsScopes/987654"}]}`), nil
				}
				if strings.HasPrefix(req.URL.Path, "/v3/projects/foreign-project/alertPolicies") {
					foreignGets++
					if req.Method != "GET" {
						t.Fatal("cross-project write")
					}
					if mode == "denied" {
						return apiResponse(req, 403, `{}`), nil
					}
					if mode == "wrong-project" {
						return dataformResponse(req, 200, map[string]any{"alertPolicies": []any{alertPolicyFixture()}}), nil
					}
					if req.URL.Path == "/v3/projects/foreign-project/alertPolicies" {
						values := []any{foreign}
						if mode == "empty" {
							values = nil
						}
						if values == nil {
							return apiResponse(req, 200, `{}`), nil
						}
						return dataformResponse(req, 200, map[string]any{"alertPolicies": values}), nil
					}
					return dataformResponse(req, 200, foreign), nil
				}
				return s.r.transport.RoundTrip(req)
			})
			contributor, err := r.MonitoringDependencies(t.Context(), "connection")
			if err != nil {
				t.Fatal(err)
			}
			result, err := contributor.Contribute(t.Context(), "global", []asset.Asset{s.request.Asset})
			if mode == "denied" || mode == "wrong-project" {
				if err == nil {
					t.Fatal("missing foreign authority accepted")
				}
				return
			}
			if err != nil || foreignGets < 2 {
				t.Fatal(result, err, foreignGets)
			}
			if mode == "referenced" && (len(result.Unresolved) != 1 || !result.Unresolved[0].BlocksCleanup || result.Unresolved[0].NativeID != "//monitoring.googleapis.com/projects/foreign-project/alertPolicies/1234") {
				t.Fatal(result)
			}
			if mode == "empty" && len(result.Unresolved) != 0 {
				t.Fatal(result)
			}
			driver, err := r.ResolveAction(t.Context(), "connection", s.request.Asset)
			if err != nil {
				t.Fatal(err)
			}
			_, err = driver.Execute(t.Context(), s.request)
			if (err == nil) != (mode == "empty") {
				t.Fatal(err)
			}
		})
	}
}
