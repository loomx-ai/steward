package gcp

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

const uptimeName = "projects/sample-project/uptimeCheckConfigs/public-check"
const uptimeID = "//monitoring.googleapis.com/" + uptimeName

func uptimeFixture() map[string]any {
	return map[string]any{"name": uptimeName, "displayName": "Public HTTPS", "period": "60s", "timeout": "10s", "disabled": false, "checkerType": "STATIC_IP_CHECKERS", "selectedRegions": []any{"USA", "EUROPE"}, "userLabels": map[string]any{"environment": "test"}, "monitoredResource": map[string]any{"type": "uptime_url", "labels": map[string]any{"project_id": "sample-project", "host": "example.com"}}, "httpCheck": map[string]any{"requestMethod": "POST", "useSsl": true, "path": "/health", "port": 443, "contentType": "URL_ENCODED", "body": base64.StdEncoding.EncodeToString([]byte("PRIVATE_UPTIME_BODY")), "headers": map[string]any{"ordinary": "PRIVATE_UPTIME_HEADER"}, "authInfo": map[string]any{"username": "PRIVATE_UPTIME_USER", "password": "PRIVATE_UPTIME_PASSWORD"}}, "futureNativeField": map[string]any{"enabled": true}}
}

func uptimeScenario(t *testing.T) (*Runtime, *contracts.ActionRequest, *map[string]any, *string, *int) {
	return monitoringScenario(t, uptimeType)
}

func monitoringScenario(t *testing.T, kind string) (*Runtime, *contracts.ActionRequest, *map[string]any, *string, *int) {
	t.Helper()
	data := uptimeFixture()
	name, id, collection := uptimeName, uptimeID, "uptimeCheckConfigs"
	if kind == alertPolicyType {
		data = alertPolicyFixture()
		name = alertPolicyName
		id = alertPolicyID
		collection = "alertPolicies"
	}
	mode := ""
	deletes := 0
	reads := 0
	r := protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
		if req.URL.Host != "monitoring.googleapis.com" {
			t.Fatal(req.URL)
		}
		if req.URL.Path == "/v3/projects/sample-project/"+collection && req.Method == "GET" {
			switch mode {
			case "list-empty":
				return apiResponse(req, 200, `{}`), nil
			case "list-denied":
				return apiResponse(req, 403, `{}`), nil
			case "list-null":
				return apiResponse(req, 200, `{"`+collection+`":null}`), nil
			case "list-token":
				return apiResponse(req, 200, `{"nextPageToken":null}`), nil
			case "list-partial":
				return apiResponse(req, 200, `{"unreachable":["region"]}`), nil
			case "list-duplicate":
				return dataformResponse(req, 200, map[string]any{collection: []any{data, data}}), nil
			case "list-paged":
				if req.URL.Query().Get("pageToken") == "" {
					return apiResponse(req, 200, `{"nextPageToken":"next"}`), nil
				}
			}
			return dataformResponse(req, 200, map[string]any{collection: []any{data}}), nil
		}
		if req.URL.Path != "/v3/"+name || len(req.URL.Query()) != 0 {
			t.Fatal(req.Method, req.URL)
		}
		if req.Method == "DELETE" {
			deletes++
			if req.Body != nil && req.ContentLength != 0 {
				t.Fatal("invented delete body")
			}
			switch mode {
			case "delete-denied":
				return apiResponse(req, 403, `{}`), nil
			case "referenced":
				return apiResponse(req, 400, `{"error":{"code":400,"message":"PRIVATE_UPTIME_ERROR","status":"FAILED_PRECONDITION"}}`), nil
			case "delete-missing":
				return apiResponse(req, 404, `{}`), nil
			case "delete-error":
				return apiResponse(req, 200, `{"error":{"code":500}}`), nil
			case "delete-operation":
				return apiResponse(req, 200, `{"name":"operations/not-native"}`), nil
			}
			return apiResponse(req, 200, `{}`), nil
		}
		if req.Method != "GET" {
			t.Fatal(req.Method)
		}
		reads++
		switch mode {
		case "gone":
			return apiResponse(req, 404, `{}`), nil
		case "get-denied":
			return apiResponse(req, 403, `{}`), nil
		case "get-partial":
			return apiResponse(req, 200, `{"unreachable":["location"]}`), nil
		case "detail-enabled-missing":
			live := cloneParameters(data)
			delete(live, "enabled")
			return dataformResponse(req, 200, live), nil
		case "detail-target-invalid":
			live := cloneParameters(data)
			live["monitoredResource"] = map[string]any{"type": "gce_instance", "labels": map[string]any{"project_id": "sample-project", "zone": "us-central1-a", "instance_id": "a-name"}}
			return dataformResponse(req, 200, live), nil
		case "detail-drift":
			live := cloneParameters(data)
			live["displayName"] = "changed-detail"
			return dataformResponse(req, 200, live), nil
		case "race":
			if reads >= 2 {
				data["displayName"] = "changed-before-delete"
			}
		}
		return dataformResponse(req, 200, data), nil
	})
	batch, err := r.List(t.Context(), productRequest(r, kind, "global"))
	if err != nil || len(batch.Items) != 1 || batch.Items[0].NativeID != id {
		t.Fatal(batch, err)
	}
	item := batch.Items[0]
	request := contracts.ActionRequest{Action: "delete", IdempotencyKey: "uptime-delete", Asset: asset.Asset{ID: "uptime", Identity: asset.Identity{Provider: asset.ProviderGCP, ConnectionID: "connection", Partition: "gcp", NativeType: kind, NativeID: item.NativeID}, Normalized: item.Normalized}}
	reads = 0
	return r, &request, &data, &mode, &deletes
}

func TestUptimeNativeInventoryReviewAndRedaction(t *testing.T) {
	r, request, data, _, _ := uptimeScenario(t)
	if request.Asset.Normalized[uptimeReview] != uptimeConfiguration(uptimeID, *data) {
		t.Fatal("lost native review")
	}
	encoded, _ := json.Marshal(request.Asset)
	if strings.Contains(string(encoded), "PRIVATE_UPTIME") {
		t.Fatal("native credentials escaped inventory")
	}
	if object(request.Asset.Normalized["labels"])["environment"] != "test" {
		t.Fatal("labels missing")
	}
	result, err := r.Invoke(t.Context(), contracts.Invocation{ConnectionID: "connection", Operation: "monitoring.projects.uptimeCheckConfigs.get", Parameters: map[string]any{"name": uptimeName}})
	if err != nil || result.RequestID == "" {
		t.Fatal(result, err)
	}
	encoded, _ = json.Marshal(result)
	if strings.Contains(string(encoded), "PRIVATE_UPTIME") {
		t.Fatal("native credentials escaped Invoke")
	}
	assertGCPPropertyQuery(t, r, []asset.Asset{request.Asset}, uptimeType, "public-check", `properties.disabled = false AND properties.checkerType = "STATIC_IP_CHECKERS"`)
	for _, scope := range []string{"project", "us-central1"} {
		batch, err := r.List(t.Context(), productRequest(r, uptimeType, scope))
		if err != nil || scope == "project" && len(batch.Items) != 1 || scope == "us-central1" && len(batch.Items) != 0 {
			t.Fatal(scope, batch, err)
		}
	}
}

func TestUptimeReadAndWriteFailures(t *testing.T) {
	for _, mode := range []string{"get-denied", "get-partial", "race", "delete-denied", "delete-missing", "delete-error", "delete-operation", "referenced", "changed-target", "changed-hidden-header", "changed-unknown", "protected", "identity", "missing-review", "foreign-connection"} {
		t.Run(mode, func(t *testing.T) {
			r, request, data, state, deletes := uptimeScenario(t)
			*state = mode
			switch mode {
			case "changed-target":
				object((*data)["monitoredResource"])["labels"] = map[string]any{"host": "other.example"}
			case "changed-hidden-header":
				object(object((*data)["httpCheck"])["headers"])["ordinary"] = "new-value"
			case "changed-unknown":
				(*data)["futureNativeField"] = map[string]any{"enabled": false}
			case "protected":
				object((*data)["userLabels"])["steward_protected"] = "true"
			case "identity":
				(*data)["name"] = "projects/foreign-project/uptimeCheckConfigs/public-check"
			case "missing-review":
				delete(request.Asset.Normalized, uptimeReview)
			}
			connection := "connection"
			if mode == "foreign-connection" {
				connection = "other"
			}
			driver, err := r.ResolveAction(t.Context(), asset.ConnectionID(connection), request.Asset)
			if mode == "foreign-connection" {
				if err == nil {
					t.Fatal("foreign credential accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			result, err := driver.Execute(t.Context(), *request)
			if err == nil {
				t.Fatal("unreviewed write accepted", result)
			}
			expected := 0
			if strings.HasPrefix(mode, "delete-") || mode == "referenced" {
				expected = 1
			}
			if *deletes != expected {
				t.Fatal("unexpected writes", *deletes, expected)
			}
		})
	}
}

func TestUptimeSynchronousDeleteRestartAndReceipt(t *testing.T) {
	r, request, data, state, deletes := uptimeScenario(t)
	driver, err := r.ResolveAction(t.Context(), "connection", request.Asset)
	if err != nil {
		t.Fatal(err)
	}
	result, err := driver.Execute(t.Context(), *request)
	if err != nil || *deletes != 1 || result.ProviderOperationID != "" || result.ProviderRequestID == "" {
		t.Fatal(result, err, *deletes)
	}
	encoded, _ := json.Marshal(result)
	if err := json.Unmarshal(encoded, &result); err != nil {
		t.Fatal(err)
	}
	driver, err = r.ResolveAction(t.Context(), "connection", request.Asset)
	if err != nil {
		t.Fatal(err)
	}
	wait, err := driver.Wait(t.Context(), *request, result)
	if err != nil || wait.Done {
		t.Fatal("DELETE response proved absence", wait, err)
	}
	for _, mode := range []string{"operation", "review", "phase", "extra", "request"} {
		t.Run(mode, func(t *testing.T) {
			changed := result
			changed.Data = cloneParameters(result.Data)
			bound := *request
			switch mode {
			case "operation":
				changed.ProviderOperationID = "https://evil.example/operation"
			case "review":
				changed.Data["review"] = "changed"
			case "phase":
				changed.Data["phase"] = "other"
			case "extra":
				changed.Data["unknown"] = true
			case "request":
				bound.IdempotencyKey = "other"
			}
			if _, err := driver.Wait(t.Context(), bound, changed); err == nil {
				t.Fatal("changed receipt accepted")
			}
		})
	}
	(*data)["displayName"] = "new-config"
	if _, err := driver.Wait(t.Context(), *request, result); err == nil {
		t.Fatal("new configuration accepted as old")
	}
	*state = "gone"
	wait, err = driver.Wait(t.Context(), *request, result)
	if err != nil || !wait.Done {
		t.Fatal(wait, err)
	}
	read, err := driver.Readback(t.Context(), *request)
	if err != nil || read.Exists || *deletes != 1 {
		t.Fatal(read, err, *deletes)
	}
	if _, err := driver.Execute(t.Context(), *request); err != nil || *deletes != 1 {
		t.Fatal("absent retry wrote again", err, *deletes)
	}
}

func TestUptimeNativeConfigurationShapes(t *testing.T) {
	c := &client{project: "sample-project", number: "123456"}
	for _, mode := range []string{"http", "tcp", "synthetic", "group", "masked", "null-target", "two-targets", "no-target", "no-check", "two-checks", "null-http", "labels", "bool", "timeout", "auth", "matchers", "headers", "null-array"} {
		t.Run(mode, func(t *testing.T) {
			data := uptimeFixture()
			valid := false
			switch mode {
			case "http":
				valid = true
			case "tcp":
				delete(data, "httpCheck")
				data["tcpCheck"] = map[string]any{"port": 80}
				valid = true
			case "synthetic":
				delete(data, "httpCheck")
				delete(data, "monitoredResource")
				data["syntheticMonitor"] = map[string]any{"cloudFunctionV2": map[string]any{"name": "projects/sample-project/locations/us-central1/functions/check", "cloudRunRevision": map[string]any{"type": "cloud_run_revision", "labels": map[string]any{"revision_name": "runtime"}}}}
				valid = true
			case "group":
				delete(data, "monitoredResource")
				data["resourceGroup"] = map[string]any{"groupId": "1234", "resourceType": "INSTANCE"}
				valid = true
			case "masked":
				object(data["httpCheck"])["maskHeaders"] = true
				object(data["httpCheck"])["headers"] = map[string]any{"ordinary": "******"}
				valid = true
			case "null-target":
				data["monitoredResource"] = nil
			case "two-targets":
				data["resourceGroup"] = map[string]any{"groupId": "1234", "resourceType": "INSTANCE"}
			case "no-target":
				delete(data, "monitoredResource")
			case "no-check":
				delete(data, "httpCheck")
			case "two-checks":
				data["tcpCheck"] = map[string]any{"port": 80}
			case "null-http":
				data["httpCheck"] = nil
			case "labels":
				data["userLabels"] = map[string]any{"key": 123}
			case "bool":
				data["disabled"] = "false"
			case "timeout":
				data["timeout"] = "not-a-duration"
			case "auth":
				object(data["httpCheck"])["authInfo"] = false
			case "matchers":
				data["contentMatchers"] = []any{false}
			case "headers":
				object(data["httpCheck"])["headers"] = map[string]any{"key": false}
			case "null-array":
				data["selectedRegions"] = nil
			}
			before := firewallDigest(data)
			if err := c.uptimeData(uptimeID, data); (err == nil) != valid {
				t.Fatal(valid, err)
			}
			_ = uptimeConfiguration(uptimeID, data)
			if before != firewallDigest(data) {
				t.Fatal("configuration digest mutated native response")
			}
		})
	}
}

func TestUptimeListCompletenessAndPagination(t *testing.T) {
	for _, mode := range []string{"list-empty", "list-paged", "list-denied", "list-null", "list-token", "list-partial", "list-duplicate", "detail-drift"} {
		t.Run(mode, func(t *testing.T) {
			r, _, _, state, _ := uptimeScenario(t)
			*state = mode
			request := productRequest(r, uptimeType, "global")
			total := 0
			var err error
			for page := 0; page < 3; page++ {
				var batch contracts.InventoryBatch
				batch, err = r.List(t.Context(), request)
				if err != nil {
					break
				}
				total += len(batch.Items)
				if batch.Complete {
					break
				}
				request.Cursor = batch.NextCursor
				if page == 2 {
					t.Fatal("pagination did not complete")
				}
			}
			valid := mode == "list-empty" || mode == "list-paged"
			if valid && err != nil || !valid && err == nil {
				t.Fatal(mode, total, err)
			}
			if mode == "list-empty" && total != 0 || mode == "list-paged" && total != 1 {
				t.Fatal(total)
			}
		})
	}
}

func TestUptimeNativeSchemaAndProjectAliases(t *testing.T) {
	raw, err := os.ReadFile("catalog/source/discovery.json")
	if err != nil {
		t.Fatal(err)
	}
	var source map[string]any
	if json.Unmarshal(raw, &source) != nil {
		t.Fatal("invalid source")
	}
	var schemas map[string]any
	for _, raw := range array(source["documents"]) {
		candidate := object(object(object(raw)["document"])["schemas"])
		if candidate["UptimeCheckConfig"] != nil {
			schemas = candidate
			break
		}
	}
	if schemas == nil {
		t.Fatal("native schema missing")
	}
	compiler := discoveryFixtureSchemaCompiler(t, schemas)
	schema, err := compiler.Compile("https://fixture.test/infra-manager.json#/definitions/UptimeCheckConfig")
	if err != nil {
		t.Fatal(err)
	}
	data := uptimeFixture()
	delete(data, "futureNativeField")
	if err := schema.Validate(roundTripDataformJSON(t, data)); err != nil {
		t.Fatal(err)
	}
	c := &client{project: "sample-project", number: "123456"}
	review := uptimeConfiguration(uptimeID, data)
	data["name"] = "projects/123456/uptimeCheckConfigs/public-check"
	if err := c.uptimeData(uptimeID, data); err != nil || uptimeConfiguration(uptimeID, data) != review {
		t.Fatal("project alias changed identity or review", err)
	}
}
