package gcp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func metricsFixtureAction(t *testing.T) (*metricsScenario, *Runtime, contracts.ActionDriver, contracts.ActionRequest) {
	t.Helper()
	s := newMetricsScenario(t)
	r := protocolRuntime(t, s.transport(t))
	value := metricsAsset(s.assets(t, r), metricsTestLink)
	request := contracts.ActionRequest{Action: "delete", Asset: value, IdempotencyKey: "unlink-reviewed-project"}
	driver, err := r.ResolveAction(context.Background(), "connection", value)
	if err != nil {
		t.Fatal(err)
	}
	return s, r, driver, request
}

func TestMetricsScopeRejectsIncompleteInventory(t *testing.T) {
	for _, mode := range []string{"permission", "missing-scope", "embedded-error", "partial", "unexpected-page", "typed-page", "missing-name", "foreign-scope", "missing-time", "bad-update-time", "missing-self", "missing-members", "null-members", "wrong-members", "null-member", "duplicate", "foreign-member", "wrong-collection", "member-query", "bad-member-time", "wrong-tombstone", "empty-reverse", "foreign-first-reverse", "duplicate-reverse", "malformed-reverse", "paged-reverse", "scope-recreated", "member-recreated", "member-added", "member-removed"} {
		t.Run(mode, func(t *testing.T) {
			s := newMetricsScenario(t)
			scopeReads := 0
			s.handle = func(req *http.Request) (*http.Response, bool) {
				if strings.Contains(req.URL.Path, ":listMetricsScopesByMonitoredProject") {
					data := roundTripDataformJSON(t, s.reverse)
					switch mode {
					case "empty-reverse":
						data["metricsScopes"] = []any{}
					case "foreign-first-reverse":
						data["metricsScopes"] = []any{object(array(data["metricsScopes"])[1])}
					case "duplicate-reverse":
						data["metricsScopes"] = []any{object(array(data["metricsScopes"])[0]), object(array(data["metricsScopes"])[0])}
					case "malformed-reverse":
						data["metricsScopes"] = "hidden"
					case "paged-reverse":
						data["nextPageToken"] = "more"
					default:
						return nil, false
					}
					return dataformResponse(req, 200, data), true
				}
				if req.URL.Path != "/v1/locations/global/metricsScopes/123456" {
					return nil, false
				}
				scopeReads++
				data := roundTripDataformJSON(t, s.scope)
				member := object(array(data["monitoredProjects"])[1])
				switch mode {
				case "permission":
					return dataformResponse(req, 403, map[string]any{}), true
				case "missing-scope":
					return dataformResponse(req, 404, map[string]any{}), true
				case "embedded-error":
					data["error"] = map[string]any{"code": 7}
				case "partial":
					data["unreachable"] = []any{"project"}
				case "unexpected-page":
					data["nextPageToken"] = "next"
				case "typed-page":
					data["nextPageToken"] = true
				case "missing-name":
					delete(data, "name")
				case "foreign-scope":
					data["name"] = "locations/global/metricsScopes/987654"
				case "missing-time":
					delete(data, "createTime")
				case "bad-update-time":
					data["updateTime"] = 17
				case "missing-self":
					data["monitoredProjects"] = array(data["monitoredProjects"])[1:]
				case "missing-members":
					delete(data, "monitoredProjects")
				case "null-members":
					data["monitoredProjects"] = nil
				case "wrong-members":
					data["monitoredProjects"] = map[string]any{}
				case "null-member":
					data["monitoredProjects"] = append(array(data["monitoredProjects"]), nil)
				case "duplicate":
					data["monitoredProjects"] = append(array(data["monitoredProjects"]), member)
				case "foreign-member":
					member["name"] = "locations/global/metricsScopes/987654/projects/222222"
				case "wrong-collection":
					member["name"] = "locations/global/metricsScopes/123456/dashboards/222222"
				case "member-query":
					member["name"] = "https://monitoring.googleapis.com/v1/locations/global/metricsScopes/123456/projects/222222?query=wrong"
				case "bad-member-time":
					member["createTime"] = "unknown"
				case "wrong-tombstone":
					member["isTombstoned"] = "false"
				case "scope-recreated":
					if scopeReads == 2 {
						data["createTime"] = "2026-09-09T12:00:00Z"
					}
				case "member-recreated":
					if scopeReads == 2 {
						member["createTime"] = "2026-09-09T12:00:00Z"
					}
				case "member-added":
					if scopeReads == 2 {
						data["monitoredProjects"] = append(array(data["monitoredProjects"]), map[string]any{"name": "locations/global/metricsScopes/123456/projects/444444", "createTime": "2026-09-09T12:00:00Z"})
					}
				case "member-removed":
					if scopeReads == 2 {
						data["monitoredProjects"] = array(data["monitoredProjects"])[:1]
					}
				default:
					return nil, false
				}
				return dataformResponse(req, 200, data), true
			}
			r := protocolRuntime(t, s.transport(t))
			batch, err := r.List(context.Background(), productRequest(r, metricsScopeType, "project"))
			if err == nil || batch.Complete || len(s.writes) > 0 {
				t.Fatalf("incomplete snapshot accepted: %+v %v", batch, err)
			}
		})
	}
}

func TestMetricsScopePreflightRejectsChangedReviewedLink(t *testing.T) {
	for _, mode := range []string{"connection", "partition", "provider", "native-id", "asset-id", "wrong-kind", "missing-time", "missing-parent-time", "plan-tombstone-type", "impacts", "prerequisites", "scope-recreated", "link-recreated", "tombstone-changed", "scope-missing", "scope-denied", "self-missing", "replaced-after-first-read"} {
		t.Run(mode, func(t *testing.T) {
			s, _, driver, request := metricsFixtureAction(t)
			switch mode {
			case "connection":
				request.Asset.Identity.ConnectionID = "different"
			case "partition":
				request.Asset.Identity.Partition = "different"
			case "provider":
				request.Asset.Identity.Provider = asset.ProviderAzure
			case "native-id":
				request.Asset.Identity.NativeID = metricsTestScope + "/projects/333333"
			case "asset-id":
				request.Asset.ID = ""
			case "wrong-kind":
				request.Asset.Identity.NativeType = metricsScopeType
			case "missing-time":
				delete(request.Asset.Normalized, "createTime")
			case "missing-parent-time":
				delete(request.Asset.Normalized, metricsParentTime)
			case "plan-tombstone-type":
				request.Asset.Normalized["isTombstoned"] = "false"
			case "impacts":
				request.LifecycleImpacts = []contracts.ActionImpact{{Asset: request.Asset}}
			case "prerequisites":
				request.PrerequisiteDeletions = []contracts.ActionImpact{{Asset: request.Asset}}
			case "scope-recreated":
				s.scope["createTime"] = "2026-09-09T12:00:00Z"
			case "link-recreated":
				object(array(s.scope["monitoredProjects"])[1])["createTime"] = "2026-09-09T12:00:00Z"
			case "tombstone-changed":
				object(array(s.scope["monitoredProjects"])[1])["isTombstoned"] = true
			case "self-missing":
				s.scope["monitoredProjects"] = array(s.scope["monitoredProjects"])[1:]
			case "scope-missing", "scope-denied":
				status := 404
				if mode == "scope-denied" {
					status = 403
				}
				s.handle = func(req *http.Request) (*http.Response, bool) {
					return dataformResponse(req, status, map[string]any{}), true
				}
			case "replaced-after-first-read":
				reads := 0
				s.handle = func(req *http.Request) (*http.Response, bool) {
					if req.Method != "GET" {
						return nil, false
					}
					reads++
					response := dataformResponse(req, 200, s.scope)
					if reads == 1 {
						object(array(s.scope["monitoredProjects"])[1])["createTime"] = "2026-09-09T12:00:00Z"
					}
					return response, true
				}
			}
			if _, err := driver.Execute(context.Background(), request); err == nil || len(s.writes) != 0 {
				t.Fatalf("changed action reached DELETE: %v %+v", err, s.writes)
			}
		})
	}
}

func TestMetricsScopeWaitBindsOperationAndRequiresAuthoritativeAbsence(t *testing.T) {
	for _, mode := range []string{"phase", "resource", "create-time", "parent-time", "tombstone", "operation", "host", "query", "userinfo", "foreign-version", "nested-operation", "wrong-operation", "wrong-done", "empty-error", "operation-error", "bad-metadata", "wrong-type", "unknown-state", "cancelled", "wrong-response", "recreated-link", "recreated-scope", "scope-404", "expired-live", "expired-absent", "pending-absent", "done-live", "done-absent"} {
		t.Run(mode, func(t *testing.T) {
			s, _, driver, request := metricsFixtureAction(t)
			result, err := driver.Execute(context.Background(), request)
			if err != nil {
				t.Fatal(err)
			}
			raw, _ := json.Marshal(result)
			if err := json.Unmarshal(raw, &result); err != nil {
				t.Fatal(err)
			}
			shouldError := true
			done := false
			switch mode {
			case "phase":
				result.Data["phase"] = "delete"
			case "resource":
				result.Data["resource"] = metricsTestScope + "/projects/333333"
			case "create-time":
				result.Data["create_time"] = "2025-01-01T00:00:00Z"
			case "parent-time":
				result.Data["parent_create_time"] = "2025-01-01T00:00:00Z"
			case "tombstone":
				result.Data["tombstoned"] = "false"
			case "operation":
				result.Data["operation"] = "https://monitoring.googleapis.com/v1/operations/different"
			case "host", "query", "userinfo", "foreign-version", "nested-operation":
				urls := map[string]string{"host": "https://foreign.example/v1/operations/unlink-1", "query": result.ProviderOperationID + "?project=foreign", "userinfo": "https://user@monitoring.googleapis.com/v1/operations/unlink-1", "foreign-version": "https://monitoring.googleapis.com/v3/operations/unlink-1", "nested-operation": "https://monitoring.googleapis.com/v1/projects/other/operations/unlink-1"}
				result.ProviderOperationID = urls[mode]
				result.Data["operation"] = urls[mode]
			case "wrong-operation":
				s.operation["name"] = "operations/other"
			case "wrong-done":
				s.operation["done"] = "true"
			case "empty-error":
				s.operation["error"] = map[string]any{}
			case "operation-error":
				s.operation["error"] = map[string]any{"code": 7, "message": "SCOPE_PRIVATE_OPERATION_FAILURE"}
			case "bad-metadata":
				s.operation["metadata"] = false
			case "wrong-type":
				object(s.operation["metadata"])["@type"] = "type.googleapis.com/foreign.OperationMetadata"
			case "unknown-state":
				object(s.operation["metadata"])["state"] = "STATE_UNSPECIFIED"
			case "cancelled":
				object(s.operation["metadata"])["state"] = "CANCELLED"
			case "wrong-response":
				s.operation["response"] = []any{}
			case "recreated-link":
				object(array(s.scope["monitoredProjects"])[1])["createTime"] = "2026-09-09T12:00:00Z"
			case "recreated-scope":
				s.scope["createTime"] = "2026-09-09T12:00:00Z"
			case "scope-404":
				s.handle = func(req *http.Request) (*http.Response, bool) {
					if strings.Contains(req.URL.Path, "/metricsScopes/") {
						return dataformResponse(req, 404, map[string]any{}), true
					}
					return nil, false
				}
			default:
				shouldError = false
				if strings.HasSuffix(mode, "-absent") {
					s.remove(metricsTestLink)
				}
				if strings.HasPrefix(mode, "expired") {
					s.operation = nil
				}
				if strings.HasPrefix(mode, "done") {
					s.operation["done"] = true
					object(s.operation["metadata"])["state"] = "DONE"
				}
				done = mode == "expired-absent" || mode == "done-absent"
			}
			wait, err := driver.Wait(context.Background(), request, result)
			if (err != nil) != shouldError || !shouldError && wait.Done != done || len(s.writes) != 1 {
				t.Fatalf("wait %+v, error %v, writes %v", wait, err, s.writes)
			}
			if err != nil && strings.Contains(err.Error(), "SCOPE_PRIVATE_") {
				t.Fatal("operation diagnostic leaked provider message")
			}
			var call *contracts.ProviderCallError
			if mode == "scope-404" && (!errors.As(err, &call) || call.Provider.Category != execution.ErrorDependencyViolation) {
				t.Fatalf("scope 404 treated as deleted link: %v", err)
			}
		})
	}
}

func TestMetricsScopeDelete404RequiresLinkAbsence(t *testing.T) {
	for _, absent := range []bool{false, true} {
		t.Run(map[bool]string{false: "still-linked", true: "removed"}[absent], func(t *testing.T) {
			s, _, driver, request := metricsFixtureAction(t)
			s.handle = func(req *http.Request) (*http.Response, bool) {
				if req.Method != "DELETE" {
					return nil, false
				}
				if absent {
					s.remove(metricsTestLink)
				}
				return dataformResponse(req, 404, map[string]any{}), true
			}
			result, err := driver.Execute(context.Background(), request)
			if absent {
				if err != nil {
					t.Fatal(err)
				}
				if wait, err := driver.Wait(context.Background(), request, result); err != nil || !wait.Done {
					t.Fatalf("verified 404 absence: %+v %v", wait, err)
				}
			} else {
				var call *contracts.ProviderCallError
				if !errors.As(err, &call) || call.Provider.Category == execution.ErrorNotFound {
					t.Fatalf("unverified DELETE 404 could close a live link: %v", err)
				}
			}
		})
	}
}

func TestMetricsScopeTombstonedProjectLinkRemainsIndependentlyRemovable(t *testing.T) {
	s := newMetricsScenario(t)
	r := protocolRuntime(t, s.transport(t))
	link := metricsAsset(s.assets(t, r), metricsTestScope+"/projects/333333")
	if link.Normalized["isTombstoned"] != true || len(link.Capabilities) == 0 {
		t.Fatal("tombstoned link was hidden or confused with absence")
	}
	driver, err := r.ResolveAction(context.Background(), "connection", link)
	if err != nil {
		t.Fatal(err)
	}
	request := contracts.ActionRequest{Action: "delete", Asset: link}
	result, err := driver.Execute(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	s.remove(link.Identity.NativeID)
	s.operation["done"] = true
	object(s.operation["metadata"])["state"] = "DONE"
	if wait, err := driver.Wait(context.Background(), request, result); err != nil || !wait.Done {
		t.Fatalf("tombstoned link cleanup: %+v %v", wait, err)
	}
	if len(s.writes) != 1 || !strings.HasSuffix(s.writes[0], "/projects/333333") {
		t.Fatalf("wrong tombstoned target: %v", s.writes)
	}
}
