package gcp

import (
	"bytes"
	"context"
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

// The pinned Google implementation provides GET/CREATE/UPDATE/DELETE but leaves
// LIST unimplemented. Do not substitute a fixture list and claim full discovery.
func TestUptimeIndependentMockGCP(t *testing.T) {
	endpoint := os.Getenv("STEWARD_UPTIME_MOCKGCP_URL")
	if endpoint == "" {
		t.Skip("set STEWARD_UPTIME_MOCKGCP_URL to the pinned Monitoring fixture harness")
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" || u.Port() == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		t.Fatal("mockgcp must use a loopback HTTP origin")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	client := &http.Client{Timeout: 10 * time.Second}
	seed := uptimeFixture()
	delete(seed, "name")
	delete(seed, "futureNativeField")
	payload, _ := json.Marshal(seed)
	req, err := http.NewRequestWithContext(ctx, "POST", endpoint+"/v3/projects/sample-project/uptimeCheckConfigs", bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	response, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatalf("native create failed: %d", response.StatusCode)
	}
	var created map[string]any
	if json.Unmarshal(body, &created) != nil || text(created["name"]) == "" {
		t.Fatal("native create returned no identity")
	}
	calls := []string{}
	// The independent backend lacks reverse Metrics Scope lookup. First prove
	// that gap blocks all writes; only then enable this explicit protocol fixture.
	reverseFixture, nativeReverseFailed := false, false
	fixtureCalls := 0
	loggingFixture, nativeLoggingFailed := false, false
	loggingFixtureCalls := 0
	dashboardFixture, nativeDashboardFailed := false, false
	dashboardFixtureCalls := 0
	r := protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
		if req.URL.Host != "monitoring.googleapis.com" && req.URL.Host != loggingHost {
			t.Fatal(req.URL)
		}
		dashboardList := req.Method == "GET" && req.URL.Host == "monitoring.googleapis.com" && req.URL.Path == "/v1/projects/sample-project/dashboards"
		if dashboardList && dashboardFixture {
			if req.URL.RawQuery != "pageSize=100" {
				t.Fatal(req.URL)
			}
			dashboardFixtureCalls++
			return apiResponse(req, 200, `{"dashboards":[]}`), nil
		}
		loggingList := req.URL.Host == loggingHost && req.Method == "GET" && req.URL.Path == "/v2/projects/sample-project/sinks"
		if loggingList && loggingFixture {
			if req.URL.Query().Get("filter") != `in_scope("DEFAULT")` || req.URL.Query().Get("pageSize") != "1000" {
				t.Fatal("wrong modeled logging request")
			}
			loggingFixtureCalls++
			return apiResponse(req, 200, `{}`), nil
		}
		reverse := req.Method == "GET" && req.URL.Path == "/v1/locations/global/metricsScopes:listMetricsScopesByMonitoredProject"
		if reverse && reverseFixture {
			if req.URL.Query().Get("monitoredResourceContainer") != "projects/123456" {
				t.Fatal("wrong modeled scope request")
			}
			fixtureCalls++
			return apiResponse(req, 200, `{"metricsScopes":[{"name":"locations/global/metricsScopes/123456"}]}`), nil
		}
		calls = append(calls, req.Method+" "+req.URL.Path)
		local := req.Clone(req.Context())
		local.URL.Scheme, local.URL.Host, local.Host = u.Scheme, u.Host, u.Host
		response, err := http.DefaultTransport.RoundTrip(local)
		if dashboardList && err == nil && response.StatusCode >= 400 {
			nativeDashboardFailed = true
		}
		if loggingList && err == nil && response.StatusCode >= 400 {
			nativeLoggingFailed = true
		}
		if reverse && err == nil && response.StatusCode >= 400 {
			nativeReverseFailed = true
		}
		return response, err
	})
	if batch, err := r.List(ctx, productRequest(r, uptimeType, "global")); err == nil || len(batch.Items) != 0 {
		t.Fatal("unimplemented native LIST became complete inventory", batch, err)
	}
	c, err := r.resolve(ctx, "connection")
	if err != nil {
		t.Fatal(err)
	}
	id := c.canonicalName("//monitoring.googleapis.com/" + text(created["name"]))
	live, err := c.uptimeRead(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	item, err := r.inventoryItem(c, map[string]any{"name": id, "assetType": uptimeType, "resource": map[string]any{"data": live, "location": "global"}})
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(item)
	if strings.Contains(string(encoded), "PRIVATE_UPTIME") || strings.Contains(string(encoded), "UFJJVkFURV9VUFRJTUVfQk9EWQ==") {
		t.Fatal("native read secrets escaped")
	}
	target := asset.Asset{ID: "mock-uptime", Identity: asset.Identity{Provider: asset.ProviderGCP, Partition: "gcp", ConnectionID: "connection", NativeType: uptimeType, NativeID: id}, Normalized: item.Normalized}
	driver, err := r.ResolveAction(ctx, "connection", target)
	if err != nil {
		t.Fatal(err)
	}
	request := contracts.ActionRequest{Action: "delete", Asset: target, IdempotencyKey: "mock-uptime"}
	if _, err := driver.Execute(ctx, request); err == nil || !nativeReverseFailed {
		t.Fatal("unsupported reverse discovery did not block native mutation", err)
	}
	for _, call := range calls {
		if strings.HasPrefix(call, "DELETE ") {
			t.Fatal("unsupported reverse lookup allowed a write")
		}
	}
	reverseFixture = true
	if _, err := driver.Execute(ctx, request); err == nil || !nativeLoggingFailed {
		t.Fatal("unimplemented sink LIST did not block cleanup", err)
	}
	for _, call := range calls {
		if strings.HasPrefix(call, "DELETE ") {
			t.Fatal("incomplete routing allowed DELETE")
		}
	}
	loggingFixture = true
	if _, err := driver.Execute(ctx, request); err == nil || !nativeDashboardFailed {
		t.Fatal("unsupported Dashboard LIST did not block cleanup", err)
	}
	for _, call := range calls {
		if strings.HasPrefix(call, "DELETE ") {
			t.Fatal("incomplete dashboard discovery allowed DELETE")
		}
	}
	dashboardFixture = true
	t.Cleanup(func() { t.Logf("explicit Dashboard LIST fixture calls: %d", dashboardFixtureCalls) })

	t.Cleanup(func() { t.Logf("explicit Logging empty LIST fixture calls: %d", loggingFixtureCalls) })
	policySeed := alertPolicyFixture()
	delete(policySeed, "name")
	delete(policySeed, "futureNativeField")
	checkID, _ := json.Marshal(last(id))
	object(object(array(policySeed["conditions"])[0])["conditionThreshold"])["filter"] = uptimeMetricFilter + " AND metric.labels.check_id=" + string(checkID)
	payload, _ = json.Marshal(policySeed)
	req, err = http.NewRequestWithContext(ctx, "POST", endpoint+"/v3/projects/sample-project/alertPolicies", bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	response, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatal("native policy seed failed", response.StatusCode)
	}
	policyBatch, err := r.List(ctx, productRequest(r, alertPolicyType, "global"))
	if err != nil || !policyBatch.Complete || len(policyBatch.Items) != 1 {
		t.Fatal("native policy discovery failed", err)
	}
	policyItem := policyBatch.Items[0]
	policy := asset.Asset{ID: "mock-policy", Identity: target.Identity, Normalized: policyItem.Normalized}
	policy.Identity.NativeID, policy.Identity.NativeType = policyItem.NativeID, alertPolicyType
	_, err = driver.Execute(ctx, request)
	var blocked *contracts.ProviderCallError
	if !errors.As(err, &blocked) || blocked.Provider.Code != "uptime_referenced_by_monitoring_consumer" {
		t.Fatal("native policy reference was not enforced", err)
	}
	for _, call := range calls {
		if strings.HasPrefix(call, "DELETE ") {
			t.Fatal("referenced check sent a mutation")
		}
	}
	policyDriver, err := r.ResolveAction(ctx, "connection", policy)
	if err != nil {
		t.Fatal(err)
	}
	policyRequest := contracts.ActionRequest{Action: "delete", Asset: policy, IdempotencyKey: "mock-policy"}
	policyResult, err := policyDriver.Execute(ctx, policyRequest)
	if err != nil {
		t.Fatal(err)
	}
	policyWait, err := policyDriver.Wait(ctx, policyRequest, policyResult)
	if err != nil || !policyWait.Done {
		t.Fatal("native prerequisite still exists", policyWait, err)
	}
	request.PrerequisiteDeletions = []contracts.ActionImpact{{Asset: policy, ControllerID: target.ID, Delete: true}}
	result, err := driver.Execute(ctx, request)
	if err != nil || result.ProviderOperationID != "" || result.Data["phase"] != "uptime_delete" {
		t.Fatal(result, err)
	}
	encoded, _ = json.Marshal(result)
	if err := json.Unmarshal(encoded, &result); err != nil {
		t.Fatal(err)
	}
	driver, err = r.ResolveAction(ctx, "connection", target)
	if err != nil {
		t.Fatal(err)
	}
	wait, err := driver.Wait(ctx, request, result)
	if err != nil || !wait.Done {
		t.Fatal(wait, err)
	}
	read, err := driver.Readback(ctx, request)
	if err != nil || read.Exists {
		t.Fatal(read, err)
	}
	deletes := 0
	for _, call := range calls {
		if strings.HasPrefix(call, "DELETE ") {
			deletes++
		}
	}
	if deletes != 2 || fixtureCalls == 0 {
		t.Fatal("duplicate native deletion", calls)
	}
	t.Logf("Google mockgcp: %d forwarded Monitoring/Logging requests, %d explicitly modeled reverse-scope calls; native Uptime/Dashboard LIST and reverse/sink query bindings remain unsupported by this backend. Verified unsupported-discovery write blocking, native AlertPolicy LIST/GET, reference blocking, policy DELETE/404, check DELETE, prerequisite-bound JSON resume and 404. No native IAM or reference-lock claim.", len(calls)-fixtureCalls, fixtureCalls)
}
