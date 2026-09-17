package gcp

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func TestLoggingRoutingIndependentMockGCP(t *testing.T) {
	endpoint := os.Getenv("STEWARD_UPTIME_MOCKGCP_URL")
	if endpoint == "" {
		t.Skip("set STEWARD_UPTIME_MOCKGCP_URL to the pinned Monitoring/Logging harness")
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" || u.Port() == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		t.Fatal("mockgcp must use a loopback HTTP origin")
	}
	httpClient := &http.Client{Timeout: 10 * time.Second}
	seed := func(path string, value map[string]any) map[string]any {
		t.Helper()
		payload, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		req, err := http.NewRequestWithContext(t.Context(), "POST", endpoint+path, bytes.NewReader(payload))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		response, err := httpClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		body, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != 200 {
			t.Fatalf("native seed %s: %d %s", path, response.StatusCode, body)
		}
		var data map[string]any
		if err := json.Unmarshal(body, &data); err != nil {
			t.Fatal(err)
		}
		return data
	}
	checkSeed := uptimeFixture()
	delete(checkSeed, "name")
	delete(checkSeed, "futureNativeField")
	check := seed("/v3/projects/sample-project/uptimeCheckConfigs", checkSeed)
	checkID := last(text(check["name"]))
	if checkID == "" {
		t.Fatal("missing native check identity")
	}
	encodedID, _ := json.Marshal(checkID)
	filter := "labels.check_id=" + string(encodedID)
	parents := []string{"projects/sample-project", "folders/456", "organizations/123"}
	sinks := map[string][]map[string]any{}
	cleanupPaths := []string{"/v3/" + text(check["name"])}
	for _, parent := range parents {
		value := loggingSinkFixture(parent, "steward-route", "logging.googleapis.com/projects/foreign-project")
		delete(value, "resourceName")
		delete(value, "writerIdentity")
		value["filter"] = filter
		created := seed("/v2/"+parent+"/sinks", value)
		sinks[parent] = []map[string]any{created}
		cleanupPaths = append(cleanupPaths, "/v2/"+parent+"/sinks/steward-route")
	}
	policySeed := alertPolicyFixture()
	delete(policySeed, "name")
	delete(policySeed, "futureNativeField")
	policySeed["conditions"] = []any{map[string]any{"displayName": "Routed check logs", "conditionMatchedLog": map[string]any{"filter": filter}}}
	policySeed["alertStrategy"] = map[string]any{"notificationRateLimit": map[string]any{"period": "300s"}}
	policySeed["notificationChannels"] = []any{}
	policy := seed("/v3/projects/foreign-project/alertPolicies", policySeed)
	cleanupPaths = append(cleanupPaths, "/v3/"+text(policy["name"]))
	t.Cleanup(func() {
		for _, path := range cleanupPaths {
			req, err := http.NewRequest("DELETE", endpoint+path, nil)
			if err != nil {
				t.Error(err)
				continue
			}
			response, err := httpClient.Do(req)
			if err != nil {
				t.Error(err)
				continue
			}
			response.Body.Close()
			if response.StatusCode != 200 {
				t.Errorf("native seed cleanup %s: %d", path, response.StatusCode)
			}
		}
	})
	listFixture, nativeListFailed := false, false
	listFixtureCalls, reverseFixtureCalls, nativeCalls, deletes := 0, 0, 0, 0
	org := newOrganizationScenario()
	dashboardFixtureCalls := 0
	r := protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
		if response, ok := emptyDashboardListFixture(t, req); ok {
			dashboardFixtureCalls++
			return response, nil
		}
		if req.URL.Host == resourceManagerHost && req.URL.Path == "/v3/projects/foreign-project" {
			return apiResponse(req, 200, `{"name":"projects/987654","projectId":"foreign-project","state":"ACTIVE"}`), nil
		}
		if req.URL.Host != "monitoring.googleapis.com" && req.URL.Host != loggingHost {
			t.Fatal(req.URL)
		}
		if req.Method == "GET" && req.URL.Path == "/v1/locations/global/metricsScopes:listMetricsScopesByMonitoredProject" {
			reverseFixtureCalls++
			return apiResponse(req, 200, `{"metricsScopes":[{"name":"locations/global/metricsScopes/123456"}]}`), nil
		}
		isList := req.URL.Host == loggingHost && req.Method == "GET" && strings.HasSuffix(req.URL.Path, "/sinks")
		if isList && listFixture {
			if req.URL.Query().Get("filter") != `in_scope("DEFAULT")` || req.URL.Query().Get("pageSize") != "1000" {
				t.Fatal(req.URL)
			}
			parent := strings.TrimSuffix(strings.TrimPrefix(req.URL.Path, "/v2/"), "/sinks")
			rows, ok := sinks[parent]
			if !ok {
				t.Fatal("unmodeled routing scope", parent)
			}
			listFixtureCalls++
			values := []any{}
			for _, row := range rows {
				values = append(values, row)
			}
			return dataformResponse(req, 200, map[string]any{"sinks": values}), nil
		}
		if req.Method == "DELETE" {
			deletes++
		}
		nativeCalls++
		local := req.Clone(req.Context())
		local.URL.Scheme, local.URL.Host, local.Host = u.Scheme, u.Host, u.Host
		response, err := http.DefaultTransport.RoundTrip(local)
		if isList && err == nil && response.StatusCode >= 400 {
			nativeListFailed = true
		}
		return response, err
	})
	base := r.transport
	r.transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Host == resourceManagerHost && req.URL.Path != "/v3/projects/foreign-project" {
			return org.transport(t, req)
		}
		return base.RoundTrip(req)
	})
	c, err := r.resolve(t.Context(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.loggingRouting(t.Context()); err == nil || !nativeListFailed {
		t.Fatal("native unsupported LIST did not fail closed", err)
	}
	listFixture = true
	observations, err := c.monitoringPolicies(t.Context(), checkID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, p := range observations {
		if p.Data["name"] == policy["name"] {
			found = true
			if p.Metrics || p.Local || p.reference(checkID) != monitoringHasReference {
				t.Fatal("routed log policy lost native dependency")
			}
		}
	}
	if !found {
		t.Fatal("foreign native policy missing")
	}
	id := c.canonicalName("//monitoring.googleapis.com/" + text(check["name"]))
	live, err := c.uptimeRead(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	item, err := r.inventoryItem(c, map[string]any{"name": id, "assetType": uptimeType, "resource": map[string]any{"data": live, "location": "global"}})
	if err != nil {
		t.Fatal(err)
	}
	value := asset.Asset{ID: "native-routing-check", Identity: asset.Identity{Provider: asset.ProviderGCP, Partition: "gcp", ConnectionID: "connection", NativeType: uptimeType, NativeID: id}, Normalized: item.Normalized}
	driver, err := r.ResolveAction(t.Context(), "connection", value)
	if err != nil {
		t.Fatal(err)
	}
	_, err = driver.Execute(t.Context(), contracts.ActionRequest{Action: "delete", Asset: value, IdempotencyKey: "native-routing-block"})
	var blocked *contracts.ProviderCallError
	if !errors.As(err, &blocked) || blocked.Provider.Code != "uptime_referenced_by_monitoring_consumer" || deletes != 0 {
		t.Fatal("routed native policy failed to block DELETE", err, deletes)
	}
	if dashboardFixtureCalls == 0 {
		t.Fatal("dashboard consumer discovery did not run")
	}
	if listFixtureCalls < 12 || nativeCalls < 10 {
		t.Fatal("missing repeated native sink/policy reads", listFixtureCalls, nativeCalls)
	}
	t.Logf("%d native Monitoring/Logging requests; %d explicit sink LIST fixture calls and %d reverse-scope fixtures; zero runtime DELETEs", nativeCalls, listFixtureCalls, reverseFixtureCalls)
}
