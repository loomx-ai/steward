package azure

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func TestStreamAnalyticsIndependentClusterIndexBoundaries(t *testing.T) {
	for _, mode := range []string{"duplicate", "foreign-subscription", "wrong-type", "missing-state", "wrong-state", "wrong-cluster", "wrong-region", "job-forbidden", "job-absent", "job-partial", "job-pending", "index-forbidden", "index-absent", "index-partial", "missing-array", "malformed-next", "filtered-next", "foreign-next", "changed-job", "changed-parent"} {
		t.Run(mode, func(t *testing.T) {
			s, r, assets := streamAnalyticsScenario(t, true)
			cluster, job := cdnAsset(t, assets, streamAnalyticsClusterType), cdnAsset(t, assets, streamAnalyticsJobType)
			index := cluster.Identity.NativeID + "/liststreamingjobs"
			row := map[string]any{"id": job.Identity.NativeID, "jobState": "Stopped"}
			switch mode {
			case "foreign-subscription":
				row["id"] = strings.Replace(job.Identity.NativeID, testSubscription, testTenant, 1)
			case "wrong-type":
				row["type"] = streamAnalyticsClusterType
			case "missing-state":
				delete(row, "jobState")
			case "wrong-state":
				row["jobState"] = "Running"
			case "wrong-cluster":
				object(s.records[job.Identity.NativeID]["properties"])["cluster"] = map[string]any{"id": cluster.Identity.NativeID + "other"}
			case "wrong-region":
				s.records[job.Identity.NativeID]["location"] = "eastus"
			case "job-forbidden":
				s.status[job.Identity.NativeID] = 403
			case "job-absent":
				s.gone[job.Identity.NativeID] = true
			case "job-partial":
				s.status[job.Identity.NativeID] = 206
			case "job-pending":
				object(s.records[job.Identity.NativeID]["properties"])["jobState"] = "Starting"
				row["jobState"] = "Starting"
			}
			original := s.handle
			walks := 0
			s.handle = func(req *http.Request) (*http.Response, bool) {
				if strings.ToLower(req.URL.Path) != index {
					return original(req)
				}
				walks++
				if mode == "changed-job" && walks == 2 {
					object(s.records[job.Identity.NativeID]["properties"])["compatibilityLevel"] = "1.2"
				}
				if mode == "changed-parent" && walks == 1 {
					object(s.records[cluster.Identity.NativeID]["sku"])["capacity"] = 99
				}
				values := []any{row}
				if mode == "duplicate" {
					values = append(values, row)
				}
				body := map[string]any{"value": values}
				status := 200
				switch mode {
				case "index-forbidden":
					status = 403
				case "index-absent":
					status = 404
				case "index-partial":
					status = 206
				case "missing-array":
					delete(body, "value")
				case "malformed-next":
					body["nextLink"] = 17
				case "filtered-next":
					body["nextLink"] = apiURL(index, streamAnalyticsVersion) + "&$filter=jobState%20eq%20Stopped"
				case "foreign-next":
					body["nextLink"] = strings.Replace(apiURL(index, streamAnalyticsVersion), testSubscription, testTenant, 1)
				}
				return jsonResponse(status, body, nil), true
			}
			initial := streamAnalyticsSnapshot(streamAnalyticsClusterType, s.records[cluster.Identity.NativeID])
			// Snapshot omits runtime counters; restore the native type and state.
			initial["type"] = streamAnalyticsClusterType
			c, _ := r.resolve(context.Background(), "connection")
			if _, err := c.streamAnalyticsChildren(context.Background(), cluster.Identity, initial); err == nil {
				t.Fatal("incomplete or changing associated-job inventory accepted")
			}
		})
	}
}

func TestStreamAnalyticsClusterPaginationUsesPostThenGet(t *testing.T) {
	s, r, assets := streamAnalyticsScenario(t, true)
	cluster, job := cdnAsset(t, assets, streamAnalyticsClusterType), cdnAsset(t, assets, streamAnalyticsJobType)
	index := cluster.Identity.NativeID + "/liststreamingjobs"
	original := s.handle
	var methods []string
	s.handle = func(req *http.Request) (*http.Response, bool) {
		if strings.ToLower(req.URL.Path) != index {
			return original(req)
		}
		methods = append(methods, req.Method)
		if req.URL.Query().Get("$skiptoken") == "" {
			return jsonResponse(200, map[string]any{"value": []any{}, "nextLink": apiURL(index, streamAnalyticsVersion) + "&$skiptoken=page2"}, nil), true
		}
		return jsonResponse(200, map[string]any{"value": []any{map[string]any{"id": job.Identity.NativeID, "jobState": "Stopped"}}, "nextLink": nil}, nil), true
	}
	c, _ := r.resolve(context.Background(), "connection")
	children, err := c.streamAnalyticsChildren(context.Background(), cluster.Identity, s.records[cluster.Identity.NativeID])
	if err != nil || len(children) != 2 || !slices.Equal(methods, []string{"POST", "GET", "POST", "GET"}) {
		t.Fatal("native paginated membership walk", len(children), methods, err)
	}
}

func TestStreamAnalyticsLeavesRequireUnchangedUnprotectedStoppedParents(t *testing.T) {
	for _, mode := range []string{"own-config", "own-secret", "own-pending", "wrong-id", "wrong-type", "parent-config", "parent-secret", "parent-running", "parent-starting", "parent-protected", "parent-lock", "parent-forbidden", "parent-absent", "parent-partial", "last-parent-secret", "last-parent-running", "request-id", "request-type", "request-provider"} {
		t.Run(mode, func(t *testing.T) {
			s, r, assets := streamAnalyticsScenario(t, false)
			target, parent := cdnAsset(t, assets, streamAnalyticsInputType), cdnAsset(t, assets, streamAnalyticsJobType)
			driver, _ := r.ResolveAction(context.Background(), "connection", target)
			request := contracts.ActionRequest{Action: "delete", Asset: target}
			if checked, err := driver.Preflight(context.Background(), request); err != nil || !checked.Allowed {
				t.Fatal("invalid baseline", checked, err)
			}
			raw, root := s.records[target.Identity.NativeID], s.records[parent.Identity.NativeID]
			switch mode {
			case "own-config":
				object(raw["properties"])["compression"] = map[string]any{"type": "GZip"}
			case "own-secret":
				object(raw["properties"])["accountKey"] = "new-secret"
			case "own-pending":
				object(raw["properties"])["provisioningState"] = "Updating"
			case "wrong-id":
				raw["id"] = target.Identity.NativeID + "other"
			case "wrong-type":
				raw["type"] = streamAnalyticsOutputType
			case "parent-config":
				object(root["properties"])["compatibilityLevel"] = "new"
			case "parent-secret":
				object(root["properties"])["accountKey"] = "new-secret"
			case "parent-running":
				object(root["properties"])["jobState"] = "Running"
			case "parent-starting":
				object(root["properties"])["jobState"] = "Starting"
			case "parent-protected":
				root["tags"] = map[string]any{"steward:protected": "true"}
			case "parent-lock":
				s.lists["/subscriptions/"+testSubscription+"/providers/microsoft.authorization/locks"] = []any{map[string]any{"id": parent.Identity.NativeID + "/providers/Microsoft.Authorization/locks/keep", "properties": map[string]any{"level": "CanNotDelete"}}}
			case "parent-forbidden":
				s.status[parent.Identity.NativeID] = 403
			case "parent-absent":
				s.gone[parent.Identity.NativeID] = true
			case "parent-partial":
				s.status[parent.Identity.NativeID] = 206
			case "request-id":
				request.Asset.Identity.NativeID += "other"
			case "request-type":
				request.Asset.Identity.NativeType = streamAnalyticsOutputType
			case "request-provider":
				request.Asset.Identity.Provider = asset.ProviderGCP
			}
			previous, reads := s.handle, 0
			s.handle = func(req *http.Request) (*http.Response, bool) {
				if strings.EqualFold(req.URL.Path, parent.Identity.NativeID) && req.Method == "GET" {
					reads++
					if reads == 2 && mode == "last-parent-secret" {
						object(root["properties"])["accountKey"] = "new-secret"
					}
					if reads == 2 && mode == "last-parent-running" {
						object(root["properties"])["jobState"] = "Running"
					}
				}
				return previous(req)
			}
			if _, err := driver.Execute(context.Background(), request); err == nil || len(s.deletes) != 0 {
				t.Fatal("unsafe job definition deletion", mode, err)
			}
		})
	}
}

func TestStreamAnalyticsEndpointRetainsAndChecksTarget(t *testing.T) {
	for _, mode := range []string{"configuration", "secret", "protected", "lock", "forbidden", "absent", "partial", "missing-binding", "last-change"} {
		t.Run(mode, func(t *testing.T) {
			s, r, assets := streamAnalyticsScenario(t, false)
			target := cdnAsset(t, assets, streamAnalyticsEndpointType)
			targets, _ := streamAnalyticsEndpointTargets(s.records[target.Identity.NativeID])
			storageID := targets[0]
			storage := s.records[storageID]
			driver, _ := r.ResolveAction(context.Background(), "connection", target)
			request := contracts.ActionRequest{Action: "delete", Asset: target}
			switch mode {
			case "configuration":
				object(storage["properties"])["publicNetworkAccess"] = "Disabled"
			case "secret":
				object(storage["properties"])["password"] = "new-secret"
			case "protected":
				storage["tags"] = map[string]any{"steward:protected": "true"}
			case "lock":
				s.lists["/subscriptions/"+testSubscription+"/providers/microsoft.authorization/locks"] = []any{map[string]any{"id": storageID + "/providers/Microsoft.Authorization/locks/keep", "properties": map[string]any{"level": "CanNotDelete"}}}
			case "forbidden":
				s.status[storageID] = 403
			case "absent":
				s.gone[storageID] = true
			case "partial":
				s.status[storageID] = 206
			case "missing-binding":
				delete(request.Asset.Normalized, "_stream_analytics_target_configurations")
			}
			previous, reads := s.handle, 0
			s.handle = func(req *http.Request) (*http.Response, bool) {
				if strings.EqualFold(req.URL.Path, storageID) && req.Method == "GET" {
					reads++
					if reads == 2 && mode == "last-change" {
						object(storage["properties"])["password"] = "changed"
					}
				}
				return previous(req)
			}
			if _, err := driver.Execute(context.Background(), request); err == nil || len(s.deletes) != 0 {
				t.Fatal("endpoint changed a protected or recreated target", err)
			}
		})
	}
}

func TestStreamAnalyticsTransformationAndJobCascadeBoundaries(t *testing.T) {
	for _, mode := range []string{"missing-review", "query-change", "function-secret", "protected-child", "retained-child", "wrong-name", "wrong-id", "wrong-type", "malformed", "partial-parent", "filtered-query"} {
		t.Run(mode, func(t *testing.T) {
			s, r, assets := streamAnalyticsScenario(t, false)
			job, query := cdnAsset(t, assets, streamAnalyticsJobType), cdnAsset(t, assets, streamAnalyticsTransformationType)
			request, input := dnsRequest(t, r, assets, job)
			original := s.handle
			switch mode {
			case "missing-review":
				request.LifecycleImpacts = nil
			case "query-change":
				object(s.records[query.Identity.NativeID]["properties"])["query"] = "SELECT private_new_query"
			case "function-secret":
				function := cdnAsset(t, assets, streamAnalyticsFunctionType)
				object(object(object(s.records[function.Identity.NativeID]["properties"])["properties"])["binding"])["privateKey"] = "new-key"
			case "protected-child":
				s.records[query.Identity.NativeID]["tags"] = map[string]any{"steward:protected": "true"}
			case "retained-child":
				input.RequestOptions = map[asset.AssetID]map[string]any{job.ID: {"retain_resources": []string{query.Identity.NativeID}}}
				solved, err := plan.Solve(input)
				if err != nil || len(solved.Blockers) == 0 {
					t.Fatal("retained query allowed job deletion", err)
				}
				return
			case "wrong-name":
				s.records[query.Identity.NativeID]["name"] = "other"
			case "wrong-id":
				s.records[query.Identity.NativeID]["id"] = query.Identity.NativeID + "other"
			case "wrong-type":
				s.records[query.Identity.NativeID]["type"] = streamAnalyticsFunctionType
			case "malformed":
				object(s.records[job.Identity.NativeID]["properties"])["transformation"] = []any{}
			case "partial-parent":
				s.status[job.Identity.NativeID] = 206
			case "filtered-query":
				c, _ := r.resolve(context.Background(), "connection")
				_, _, _, err := c.streamAnalyticsTransformationPage(context.Background(), apiURL(job.Identity.NativeID, streamAnalyticsVersion)+"&$expand=transformation&$filter=hidden", job.Identity.NativeID)
				if err == nil {
					t.Fatal("filtered transformation inventory accepted")
				}
				return
			}
			s.handle = original
			driver, _ := r.ResolveAction(context.Background(), "connection", job)
			if _, err := driver.Execute(context.Background(), request); err == nil || len(s.deletes) != 0 {
				t.Fatal("unreviewed or malformed native query deleted", mode, err)
			}
		})
	}
	for _, state := range []string{"Running", "Degraded"} {
		s, r, assets := streamAnalyticsScenario(t, false)
		job := cdnAsset(t, assets, streamAnalyticsJobType)
		object(s.records[job.Identity.NativeID]["properties"])["jobState"] = state
		request, _ := dnsRequest(t, r, assets, job)
		driver, _ := r.ResolveAction(context.Background(), "connection", job)
		if _, err := driver.Execute(context.Background(), request); err != nil {
			t.Fatal("native running-job cascade rejected", state, err)
		}
	}
}

func TestStreamAnalyticsRotatingPollBoundaries(t *testing.T) {
	for _, mode := range []string{"foreign-host", "foreign-subscription", "foreign-resource", "other-operation", "duplicate-query", "missing-signature", "wrong-version", "fragment", "encoded-path", "duplicate-header", "status-header", "receipt-binding", "receipt-operation", "orphan-binding"} {
		t.Run(mode, func(t *testing.T) {
			s, r, assets := streamAnalyticsScenario(t, false)
			target := cdnAsset(t, assets, streamAnalyticsEndpointType)
			driver, _ := r.ResolveAction(context.Background(), "connection", target)
			a := driver.(*action)
			native := streamAnalyticsRecordings(t, "test_private_endpoint_crud")[69]
			u, _ := url.Parse(native.URI)
			u.Path = target.Identity.NativeID + "/OperationResults/" + last(u.Path)
			operation := u.String()
			next := operation
			switch mode {
			case "foreign-host":
				next = strings.Replace(next, "management.azure.com", "evil.invalid", 1)
			case "foreign-subscription":
				next = strings.Replace(next, testSubscription, testTenant, 1)
			case "foreign-resource":
				next = strings.Replace(next, "/privateendpoints/storage/", "/privateendpoints/other/", 1)
			case "other-operation":
				next = strings.Replace(next, last(u.Path), "0638938368000000000.11111111-2222-3333-4444-555555555555", 1)
			case "duplicate-query":
				next += "&t=1"
			case "missing-signature":
				q := u.Query()
				q.Del("s")
				u.RawQuery = q.Encode()
				next = u.String()
			case "wrong-version":
				next = strings.Replace(next, streamAnalyticsVersion, "2021-10-01-preview", 1)
			case "fragment":
				next += "#secret"
			case "encoded-path":
				next = strings.Replace(next, "/providers/", "/%70roviders/", 1)
			}
			result := contracts.ActionResult{ProviderOperationID: operation, Data: map[string]any{"polling": "location", "stream_analytics_operation_binding": a.operationBinding(operation)}}
			switch mode {
			case "receipt-binding":
				result.Data["stream_analytics_poll_operation"] = next
				result.Data["stream_analytics_poll_binding"] = "wrong"
			case "receipt-operation":
				result.Data["stream_analytics_poll_operation"] = strings.Replace(next, last(u.Path), "0638938368000000000.11111111-2222-3333-4444-555555555555", 1)
				result.Data["stream_analytics_poll_binding"] = a.operationBinding(text(result.Data["stream_analytics_poll_operation"]))
			case "orphan-binding":
				result.Data["stream_analytics_poll_binding"] = "orphan"
			}
			native.Headers = map[string][]string{"Location": {next}}
			if mode == "duplicate-header" {
				native.Headers["Location"] = []string{next, next}
			}
			if mode == "status-header" {
				native.Headers["Azure-AsyncOperation"] = []string{next}
			}
			calls := 0
			s.handle = func(req *http.Request) (*http.Response, bool) {
				if strings.Contains(strings.ToLower(req.URL.Path), "/operationresults/") {
					calls++
					if req.URL.String() != operation {
						t.Fatal("followed untrusted successor")
					}
					return streamAnalyticsRecordedHTTP(native), true
				}
				return nil, false
			}
			if _, err := driver.Wait(context.Background(), contracts.ActionRequest{Action: "delete", Asset: target}, result); err == nil {
				t.Fatal("invalid rotated poll accepted", mode)
			}
			if strings.Contains(mode, "receipt") || mode == "orphan-binding" {
				if calls != 0 {
					t.Fatal("unbound resumed receipt reached HTTP")
				}
			}
		})
	}
}

func TestStreamAnalyticsPrivateQuerySnapshot(t *testing.T) {
	s, r, assets := streamAnalyticsScenario(t, false)
	target := cdnAsset(t, assets, streamAnalyticsInputType)
	raw := s.records[target.Identity.NativeID]
	props := object(raw["properties"])
	fields := []string{"query", "fullSnapshotQuery", "deltaSnapshotQuery", "script", "accountKey", "sharedAccessPolicyKey", "refreshToken", "accessToken", "apiKey", "functionKey", "clientSecret", "certificate", "privateKey", "password"}
	for _, key := range fields {
		props[key] = "private-" + key
	}
	props["endpoint"] = "https://private-user:private-password@example.invalid/path?sig=private-sig#private-fragment"
	props["counter"] = json.Number("9007199254740993")
	snapshot := streamAnalyticsSnapshot(target.Identity.NativeType, raw)
	if object(snapshot["properties"])["counter"] != json.Number("9007199254740993") {
		t.Fatal("private integer precision lost")
	}
	c, _ := r.resolve(context.Background(), "connection")
	before := c.privateConfiguration(snapshot)
	sanitized := dnsAsset(t, r, raw)
	payload, _ := json.Marshal(sanitized)
	if strings.Contains(string(payload), "private-") {
		t.Fatal("authored content or credentials leaked into inventory")
	}
	for _, key := range fields {
		old := props[key]
		props[key] = "changed-private-value"
		if c.privateConfiguration(streamAnalyticsSnapshot(target.Identity.NativeType, raw)) == before {
			t.Fatal("private field omitted from drift check", key)
		}
		props[key] = old
	}
	props["etag"], props["diagnostics"], props["provisioningState"] = "changed", map[string]any{"conditions": []any{1}}, "Updating"
	if c.privateConfiguration(streamAnalyticsSnapshot(target.Identity.NativeType, raw)) != before {
		t.Fatal("volatile diagnostics changed authored configuration")
	}
}

func TestStreamAnalyticsInventoryPaginationBoundaries(t *testing.T) {
	for _, mode := range []string{"complete", "duplicate", "cycle", "foreign-host", "foreign-subscription", "foreign-collection", "wrong-version", "partial", "forbidden", "missing-array", "parent-config", "parent-private", "filtered", "duplicate-version", "encoded-path"} {
		t.Run(mode, func(t *testing.T) {
			s, r, assets := streamAnalyticsScenario(t, false)
			parent := cdnAsset(t, assets, streamAnalyticsJobType).Identity.NativeID
			path := parent + "/functions"
			first := s.records[cdnAsset(t, assets, streamAnalyticsFunctionType).Identity.NativeID]
			payload, _ := json.Marshal(first)
			var second map[string]any
			json.Unmarshal(payload, &second)
			second["id"], second["name"] = path+"/other-function", "other-function"
			s.add(second, streamAnalyticsVersion)
			next := apiURL(path, streamAnalyticsVersion) + "&$skiptoken=native-page-2"
			switch mode {
			case "foreign-host":
				next = strings.Replace(next, "management.azure.com", "evil.invalid", 1)
			case "foreign-subscription":
				next = strings.Replace(next, testSubscription, testTenant, 1)
			case "foreign-collection":
				next = strings.Replace(next, "/functions?", "/outputs?", 1)
			case "wrong-version":
				next = strings.Replace(next, streamAnalyticsVersion, "2025-09-01", 1)
			}
			switch mode {
			case "filtered":
				next += "&$filter=hidden"
			case "duplicate-version":
				next += "&api-version=" + streamAnalyticsVersion
			case "encoded-path":
				next = strings.Replace(next, "/functions?", "/%66unctions?", 1)
			}
			original := s.handle
			s.handle = func(req *http.Request) (*http.Response, bool) {
				if strings.ToLower(req.URL.Path) != path {
					return original(req)
				}
				if req.URL.Query().Get("$skiptoken") == "" {
					return jsonResponse(200, map[string]any{"value": []any{first}, "nextLink": next}, nil), true
				}
				switch mode {
				case "partial":
					return jsonResponse(206, map[string]any{"value": []any{second}}, nil), true
				case "forbidden":
					return jsonResponse(403, nil, nil), true
				case "missing-array":
					return jsonResponse(200, map[string]any{}, nil), true
				case "cycle":
					return jsonResponse(200, map[string]any{"value": []any{second}, "nextLink": next}, nil), true
				case "duplicate":
					return jsonResponse(200, map[string]any{"value": []any{first}}, nil), true
				case "parent-config":
					object(s.records[parent]["properties"])["compatibilityLevel"] = "1.2"
				case "parent-private":
					object(s.records[parent]["properties"])["password"] = "changed-private-value"
				}
				return jsonResponse(200, map[string]any{"value": []any{second}}, nil), true
			}
			request := productRequest(r, streamAnalyticsFunctionType)
			var err error
			count, complete := 0, false
			for range 5 {
				var batch contracts.InventoryBatch
				batch, err = r.List(context.Background(), request)
				if err != nil {
					break
				}
				count += len(batch.Items)
				if batch.Complete {
					complete = true
					break
				}
				request.Cursor = batch.NextCursor
			}
			if mode == "complete" {
				if err != nil || !complete || count != 2 {
					t.Fatal("StreamAnalytics complete pagination", err, count, complete)
				}
			} else if err == nil || complete {
				t.Fatal("incomplete StreamAnalytics collection accepted", count)
			}
		})
	}
}

func TestStreamAnalyticsOperationIdentityStateAndReadback(t *testing.T) {
	for _, mode := range []string{"pending", "unknown", "success-live", "success-absent", "failed", "canceled", "partial", "forbidden", "wrong-id", "wrong-name", "wrong-resource", "wrong-binding", "wrong-request", "expired-live", "expired-absent", "readback-partial", "readback-forbidden", "readback-wrong-id"} {
		t.Run(mode, func(t *testing.T) {
			s, r, assets := streamAnalyticsScenario(t, false)
			target := cdnAsset(t, assets, streamAnalyticsEndpointType)
			driver, _ := r.ResolveAction(context.Background(), "connection", target)
			a := driver.(*action)
			row := streamAnalyticsRecordings(t, "test_private_endpoint_crud")[85]
			u, _ := url.Parse(row.URI)
			u.Path = target.Identity.NativeID + "/OperationResults/" + last(u.Path)
			endpoint := u.String()
			result := contracts.ActionResult{ProviderOperationID: endpoint, Data: map[string]any{"polling": "location", "stream_analytics_operation_binding": a.operationBinding(endpoint)}}
			request := contracts.ActionRequest{Action: "delete", Asset: target}
			status := 200
			body := map[string]any{"status": "Succeeded", "id": u.Path, "name": last(u.Path), "resourceId": target.Identity.NativeID}
			switch mode {
			case "pending":
				body["status"] = "InProgress"
			case "unknown":
				body["status"] = "FutureState"
			case "success-absent", "expired-absent":
				s.gone[target.Identity.NativeID] = true
			case "failed":
				body["status"] = "Failed"
			case "canceled":
				body["status"] = "Canceled"
			case "partial":
				status = 206
			case "forbidden":
				status = 403
			case "wrong-id":
				body["id"] = u.Path + "other"
			case "wrong-name":
				body["name"] = "other"
			case "wrong-resource":
				body["resourceId"] = target.Identity.NativeID + "other"
			case "wrong-binding":
				result.Data["stream_analytics_operation_binding"] = "forged"
			case "wrong-request":
				request.Asset.Identity.NativeID += "other"
			case "readback-partial":
				s.status[target.Identity.NativeID] = 206
			case "readback-forbidden":
				s.status[target.Identity.NativeID] = 403
			case "readback-wrong-id":
				s.records[target.Identity.NativeID]["id"] = target.Identity.NativeID + "other"
			}
			if strings.HasPrefix(mode, "expired-") {
				status = 404
			}
			calls := 0
			previous := s.handle
			s.handle = func(req *http.Request) (*http.Response, bool) {
				if req.URL.String() == endpoint {
					calls++
					return jsonResponse(status, body, nil), true
				}
				return previous(req)
			}
			waited, err := driver.Wait(context.Background(), request, result)
			if mode == "success-absent" || mode == "expired-absent" {
				if err != nil || !waited.Done {
					t.Fatal("confirmed native absence rejected", waited, err)
				}
			} else if waited.Done {
				t.Fatal("unproven Stream Analytics deletion completed", mode)
			}
			if slices.Contains([]string{"pending", "unknown", "success-live", "expired-live"}, mode) {
				if err != nil {
					t.Fatal("pending or live readback should remain retryable", err)
				}
			} else if mode != "success-absent" && mode != "expired-absent" && err == nil {
				t.Fatal("invalid operation response accepted", mode)
			}
			if (mode == "wrong-binding" || mode == "wrong-request") && calls != 0 {
				t.Fatal("forged receipt reached HTTP")
			}
		})
	}
}

func TestStreamAnalyticsStopInvocationBindsItsActualJob(t *testing.T) {
	for _, mode := range []string{"native", "foreign-job", "foreign-host", "duplicate-version", "missing-signature"} {
		t.Run(mode, func(t *testing.T) {
			s, r, assets := streamAnalyticsScenario(t, false)
			job := cdnAsset(t, assets, streamAnalyticsJobType)
			row := streamAnalyticsRecordings(t, "test_job_scale")[14]
			u, _ := url.Parse(streamAnalyticsRecordedHTTP(row).Header.Get("Location"))
			u.Path = job.Identity.NativeID + "/OperationResults/" + last(u.Path)
			switch mode {
			case "foreign-job":
				u.Path = strings.Replace(u.Path, "/streamingjobs/job/", "/streamingjobs/other/", 1)
			case "foreign-host":
				u.Host = "evil.invalid"
			case "duplicate-version":
				u.RawQuery += "&api-version=" + streamAnalyticsVersion
			case "missing-signature":
				q := u.Query()
				q.Del("s")
				u.RawQuery = q.Encode()
			}
			s.handle = func(req *http.Request) (*http.Response, bool) {
				if req.Method != "POST" {
					return nil, false
				}
				if !strings.EqualFold(req.URL.Path, job.Identity.NativeID+"/stop") || req.URL.Query().Get("api-version") != streamAnalyticsVersion {
					t.Fatal("wrong Stop operation binding")
				}
				return jsonResponse(202, nil, http.Header{"Location": {u.String()}}), true
			}
			result, err := r.Invoke(context.Background(), contracts.Invocation{ConnectionID: "connection", Operation: "Azure.Microsoft.StreamAnalytics.StreamingJobs_Stop", Parameters: map[string]any{"resourceGroupName": "testgroup", "jobName": "job"}})
			if mode == "native" {
				if err != nil || result.OperationID != u.String() {
					t.Fatal("native Stop invocation rejected", err)
				}
			} else if err == nil {
				t.Fatal("Stop accepted an unrelated operation", mode)
			}
		})
	}
}
