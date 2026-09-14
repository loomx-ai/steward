package gcp

import (
	"bytes"
	"context"
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

// Native policy calls are forwarded; unsupported Dashboard LIST is an explicit fixture.
func TestAlertPolicyIndependentMockGCP(t *testing.T) {
	endpoint := os.Getenv("STEWARD_ALERT_POLICY_MOCKGCP_URL")
	if endpoint == "" {
		t.Skip("set STEWARD_ALERT_POLICY_MOCKGCP_URL to the pinned Monitoring fixture harness")
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" || u.Port() == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		t.Fatal("mockgcp must use a loopback HTTP origin")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	client := &http.Client{Timeout: 10 * time.Second}
	seed := alertPolicyFixture()
	delete(seed, "name")
	delete(seed, "futureNativeField")
	payload, _ := json.Marshal(seed)
	req, err := http.NewRequestWithContext(ctx, "POST", endpoint+"/v3/projects/sample-project/alertPolicies", bytes.NewReader(payload))
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
	dashboardFixtures := 0
	r := protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
		if req.URL.Host != "monitoring.googleapis.com" {
			t.Fatal(req.URL)
		}
		calls = append(calls, req.Method+" "+req.URL.Path)
		local := req.Clone(req.Context())
		local.URL.Scheme, local.URL.Host, local.Host = u.Scheme, u.Host, u.Host
		response, err := http.DefaultTransport.RoundTrip(local)
		if req.URL.Path == "/v1/projects/sample-project/dashboards" {
			dashboardFixtures++
			if err != nil {
				t.Fatal(err)
			}
			body, _ := io.ReadAll(response.Body)
			response.Body.Close()
			data := map[string]any{}
			if json.Unmarshal(body, &data) != nil || response.StatusCode != 500 || object(data["error"])["message"] != "method ListDashboards not implemented" {
				t.Fatal("unexpected native Dashboard LIST", response.StatusCode)
			}
			return apiResponse(req, 200, `{"dashboards":[]}`), nil
		}
		return response, err
	})
	batch, err := r.List(ctx, productRequest(r, alertPolicyType, "global"))
	if err != nil || !batch.Complete || len(batch.Items) != 1 {
		t.Fatal(batch, err)
	}
	item := batch.Items[0]
	id := item.NativeID
	encoded, _ := json.Marshal(item)
	if strings.Contains(string(encoded), "PRIVATE_ALERT") || strings.Contains(string(encoded), "UFJJVkFURV9VUFRJTUVfQk9EWQ==") {
		t.Fatal("native read secrets escaped")
	}
	target := asset.Asset{ID: "mock-alert-policy", Identity: asset.Identity{Provider: asset.ProviderGCP, Partition: "gcp", ConnectionID: "connection", NativeType: alertPolicyType, NativeID: id}, Normalized: item.Normalized}
	driver, err := r.ResolveAction(ctx, "connection", target)
	if err != nil {
		t.Fatal(err)
	}
	request := contracts.ActionRequest{Action: "delete", Asset: target, IdempotencyKey: "mock-alert-policy"}
	result, err := driver.Execute(ctx, request)
	if err != nil || result.ProviderOperationID != "" || result.Data["phase"] != "alert_policy_delete" {
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
	if deletes != 1 {
		t.Fatal("duplicate native deletion", calls)
	}
	empty, err := r.List(ctx, productRequest(r, alertPolicyType, "global"))
	if err != nil || !empty.Complete || len(empty.Items) != 0 {
		t.Fatal(empty, err)
	}
	if dashboardFixtures != 4 {
		t.Fatal("missing dashboard preflight snapshots", dashboardFixtures)
	}
	t.Logf("Native policy LIST/GET/DELETE/restart/404 passed (%d calls, %d explicit Dashboard LIST fixtures after native Unimplemented)", len(calls), dashboardFixtures)
}
