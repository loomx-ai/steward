package gcp

import (
	"bytes"
	"encoding/json"
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

func TestMonitoringDashboardIndependentMockGCP(t *testing.T) {
	testMonitoringDashboardIndependentMockGCP(t, false)
}
func TestMonitoringDashboardPolicyIndependentMockGCP(t *testing.T) {
	testMonitoringDashboardIndependentMockGCP(t, true)
}
func testMonitoringDashboardIndependentMockGCP(t *testing.T, policy bool) {
	endpoint := os.Getenv("STEWARD_NOTIFICATION_CHANNEL_MOCKGCP_URL")
	if endpoint == "" {
		t.Skip("set STEWARD_NOTIFICATION_CHANNEL_MOCKGCP_URL to the pinned Monitoring harness")
	}
	origin, err := url.Parse(endpoint)
	if err != nil || origin.Scheme != "http" || origin.Hostname() != "127.0.0.1" || origin.Port() == "" || origin.User != nil || origin.Path != "" || origin.RawQuery != "" || origin.Fragment != "" {
		t.Fatal("mock must use a loopback origin")
	}
	client := &http.Client{Timeout: 10 * time.Second}
	native := func(method, path string, data map[string]any, want int) map[string]any {
		t.Helper()
		var body io.Reader
		if data != nil {
			body = bytes.NewReader(mustDashboardJSON(t, data))
		}
		req, e := http.NewRequest(method, endpoint+path, body)
		if e != nil {
			t.Fatal(e)
		}
		req.Header.Set("Content-Type", "application/json")
		res, e := client.Do(req)
		if e != nil {
			t.Fatal(e)
		}
		defer res.Body.Close()
		b, e := io.ReadAll(res.Body)
		if e != nil || res.StatusCode != want {
			t.Fatal(method, path, res.StatusCode, e, string(b))
		}
		result := map[string]any{}
		if e = json.Unmarshal(b, &result); e != nil {
			t.Fatal(e)
		}
		return result
	}
	policyName := ""
	policyExists := false
	if policy {
		seed := alertPolicyFixture()
		delete(seed, "name")
		delete(seed, "futureNativeField")
		created := native("POST", "/v3/projects/sample-project/alertPolicies", seed, 200)
		policyName = text(created["name"])
		policyExists = true
		t.Cleanup(func() {
			if policyExists {
				native("DELETE", "/v3/"+policyName, nil, 200)
			}
		})
	}
	seed := monitoringDashboardFixture()
	if policy {
		seed["gridLayout"] = map[string]any{"widgets": []any{dashboardPolicyWidget("alertChart", policyName)}}
	}
	delete(seed, "name")
	delete(seed, "etag")
	current := native("POST", "/v1/projects/sample-project/dashboards", seed, 200)
	name := text(current["name"])
	if !strings.HasPrefix(name, "projects/123456/dashboards/") {
		t.Fatal(current)
	}
	exists := true
	t.Cleanup(func() {
		if exists {
			native("DELETE", "/v1/"+name, nil, 200)
		}
	})
	unsupported := native("GET", "/v1/projects/sample-project/dashboards", nil, 500)
	if object(unsupported["error"])["message"] != "method ListDashboards not implemented" {
		t.Fatal(unsupported)
	}
	hybrid := false
	clearList := false
	forwarded, fixtures, deletes := 0, 0, 0
	r := protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
		if req.URL.Host != "monitoring.googleapis.com" || (req.Method != "GET" && req.Method != "DELETE") {
			t.Fatal(req.Method, req.URL)
		}
		if hybrid && req.URL.Path == "/v1/projects/sample-project/dashboards" {
			if req.URL.RawQuery != "pageSize=100" {
				t.Fatal(req.URL)
			}
			fixtures++
			if clearList {
				return apiResponse(req, 200, `{"dashboards":[]}`), nil
			}
			return dataformResponse(req, 200, map[string]any{"dashboards": []any{current}}), nil
		}
		if req.Method == "DELETE" {
			deletes++
			if (req.URL.Path != "/v1/"+strings.Replace(name, "projects/123456/", "projects/sample-project/", 1) && !(policy && req.URL.Path == "/v3/"+strings.Replace(policyName, "projects/123456/", "projects/sample-project/", 1))) || req.URL.RawQuery != "" {
				t.Fatal(req.URL)
			}
			if req.Body != nil {
				b, _ := io.ReadAll(req.Body)
				if len(b) != 0 {
					t.Fatal("DELETE body")
				}
			}
		}
		forwarded++
		local := req.Clone(req.Context())
		local.URL.Scheme, local.URL.Host, local.Host = origin.Scheme, origin.Host, origin.Host
		return client.Do(local)
	})
	req := productRequest(r, monitoringDashboardType, "global")
	if batch, e := r.List(t.Context(), req); e == nil || batch.Complete || len(batch.Items) != 0 {
		t.Fatal("native unsupported LIST cleared inventory", batch, e)
	}
	hybrid = true
	var saved asset.Asset
	previous := ""
	for i := 0; i < 2; i++ {
		if i == 1 {
			current["displayName"] = "Updated Dashboard"
			current = native("PATCH", "/v1/"+name, current, 200)
		}
		batch, e := r.List(t.Context(), req)
		if e != nil || !batch.Complete || len(batch.Items) != 1 {
			t.Fatal(batch, e)
		}
		item := batch.Items[0]
		proof := text(item.Normalized[monitoringDashboardReview])
		if len(proof) != 64 || proof == previous {
			t.Fatal("native update not bound")
		}
		previous = proof
		if strings.Contains(string(mustDashboardJSON(t, batch)), "PRIVATE_") {
			t.Fatal("native private content persisted")
		}
		k := r.resourceKind(monitoringDashboardType)
		saved = asset.Asset{ID: "native-dashboard", ResourceKindID: k.ID, Identity: asset.Identity{Provider: asset.ProviderGCP, Partition: "gcp", ConnectionID: "connection", NativeType: monitoringDashboardType, NativeID: item.NativeID}, Normalized: item.Normalized, Capabilities: k.Capabilities}
	}
	policyRequest := contracts.ActionRequest{}
	if policy {
		batch, e := r.List(t.Context(), productRequest(r, alertPolicyType, "global"))
		if e != nil || len(batch.Items) != 1 {
			t.Fatal(batch, e)
		}
		item := batch.Items[0]
		k := r.resourceKind(alertPolicyType)
		value := asset.Asset{ID: "native-policy", ResourceKindID: k.ID, Identity: asset.Identity{Provider: asset.ProviderGCP, Partition: "gcp", ConnectionID: "connection", NativeType: alertPolicyType, NativeID: item.NativeID}, Normalized: item.Normalized, Capabilities: k.Capabilities}
		policyRequest = contracts.ActionRequest{Asset: value, Action: "delete", IdempotencyKey: "native-policy-delete"}
		driver, e := r.ResolveAction(t.Context(), "connection", value)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = driver.Execute(t.Context(), policyRequest); e == nil || deletes != 0 {
			t.Fatal("live native dashboard did not block policy", e, deletes)
		}
		policyRequest.PrerequisiteDeletions = []contracts.ActionImpact{{Asset: saved, ControllerID: value.ID, Delete: true}}
	}
	driver, err := r.ResolveAction(t.Context(), "connection", saved)
	if err != nil {
		t.Fatal(err)
	}
	action := contracts.ActionRequest{Asset: saved, Action: "delete", IdempotencyKey: "native-dashboard-delete"}
	result, err := driver.Execute(t.Context(), action)
	if err != nil || result.Data["phase"] != "monitoring_dashboard_delete" || deletes != 1 {
		t.Fatal(result, err, deletes)
	}
	exists = false
	restored := contracts.ActionResult{}
	if err = json.Unmarshal(mustDashboardJSON(t, result), &restored); err != nil {
		t.Fatal(err)
	}
	fresh := protocolRuntime(t, r.transport.RoundTrip)
	driver, err = fresh.ResolveAction(t.Context(), "connection", saved)
	if err != nil {
		t.Fatal(err)
	}
	wait, err := driver.Wait(t.Context(), action, restored)
	if err != nil || !wait.Done {
		t.Fatal(wait, err)
	}
	settled, err := driver.(contracts.MutationSettlementReader).MutationSettled(t.Context(), action, restored)
	if err != nil || !settled.Settled {
		t.Fatal(settled, err)
	}
	if batch, e := r.List(t.Context(), req); e == nil || batch.Complete || len(batch.Items) != 0 {
		t.Fatal("stale list accepted after native deletion", batch, e)
	}
	if policy {
		clearList = true
		fresh := protocolRuntime(t, r.transport.RoundTrip)
		driver, e := fresh.ResolveAction(t.Context(), "connection", policyRequest.Asset)
		if e != nil {
			t.Fatal(e)
		}
		result, e := driver.Execute(t.Context(), policyRequest)
		if e != nil || deletes != 2 {
			t.Fatal(result, e, deletes)
		}
		policyExists = false
		restored := contracts.ActionResult{}
		if e = json.Unmarshal(mustDashboardJSON(t, result), &restored); e != nil {
			t.Fatal(e)
		}
		fresh = protocolRuntime(t, r.transport.RoundTrip)
		driver, e = fresh.ResolveAction(t.Context(), "connection", policyRequest.Asset)
		if e != nil {
			t.Fatal(e)
		}
		wait, e := driver.Wait(t.Context(), policyRequest, restored)
		if e != nil || !wait.Done {
			t.Fatal(wait, e)
		}
		settled, e := driver.(contracts.MutationSettlementReader).MutationSettled(t.Context(), policyRequest, restored)
		if e != nil || !settled.Settled {
			t.Fatal(settled, e)
		}
	}
	t.Logf("%d native forwarded requests (%d DELETE), %d explicit LIST fixtures", forwarded, deletes, fixtures)
}
