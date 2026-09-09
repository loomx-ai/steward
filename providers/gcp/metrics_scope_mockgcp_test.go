package gcp

import (
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

// Opt-in independent protocol run. Every Monitoring response comes from the
// pinned, unmodified Google mockgcp handlers over a real local HTTP connection.
// The normal offline suite exercises delayed LROs and IAM/partial-read failures.
func TestMetricsScopeIndependentMockGCP(t *testing.T) {
	endpoint := os.Getenv("STEWARD_METRICS_SCOPE_MOCKGCP_URL")
	if endpoint == "" {
		t.Skip("set STEWARD_METRICS_SCOPE_MOCKGCP_URL to the fixture harness")
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" || u.Port() == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		t.Fatal("mockgcp must use a local loopback HTTP origin")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	client := &http.Client{Timeout: 10 * time.Second}
	// Seed through Google's CreateMonitoredProject implementation. The fixture
	// only supplies the two project identities; no Monitoring response is patched.
	seed, err := http.NewRequestWithContext(ctx, "POST", endpoint+"/v1/locations/global/metricsScopes/sample-project/projects", strings.NewReader(`{"name":"locations/global/metricsScopes/sample-project/projects/monitored-project"}`))
	if err != nil {
		t.Fatal(err)
	}
	seed.Header.Set("Content-Type", "application/json")
	response, err := client.Do(seed)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatalf("native mockgcp seed: %d %s", response.StatusCode, body)
	}
	calls := []string{}
	r := protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
		if req.URL.Host != "monitoring.googleapis.com" {
			t.Fatalf("unexpected service: %s", req.URL)
		}
		calls = append(calls, req.Method+" "+req.URL.Path)
		local := req.Clone(req.Context())
		local.URL.Scheme, local.URL.Host, local.Host = u.Scheme, u.Host, u.Host
		return http.DefaultTransport.RoundTrip(local)
	})
	batch, err := r.List(ctx, productRequest(r, monitoredProjectType, "project"))
	if err != nil || !batch.Complete || len(batch.Items) != 2 {
		t.Fatalf("mockgcp native inventory: %+v %v", batch, err)
	}
	var link asset.Asset
	for _, item := range batch.Items {
		if item.NativeID != metricsTestLink {
			continue
		}
		link = asset.Asset{ID: "mockgcp-link", Identity: asset.Identity{Provider: asset.ProviderGCP, ConnectionID: "connection", Partition: "google-cloud", NativeType: item.NativeType, NativeID: item.NativeID}, Normalized: item.Normalized}
	}
	if link.ID == "" {
		t.Fatal("native project-number link not discovered")
	}
	driver, err := r.ResolveAction(ctx, "connection", link)
	if err != nil {
		t.Fatal(err)
	}
	request := contracts.ActionRequest{Action: "delete", Asset: link, IdempotencyKey: "mockgcp-native-unlink"}
	result, err := driver.Execute(ctx, request)
	if err != nil || result.ProviderOperationID == "" {
		t.Fatalf("mockgcp native unlink: %+v %v", result, err)
	}
	encoded, _ := json.Marshal(result)
	if err := json.Unmarshal(encoded, &result); err != nil {
		t.Fatal(err)
	}
	driver, err = r.ResolveAction(ctx, "connection", link)
	if err != nil {
		t.Fatal(err)
	}
	wait, err := driver.Wait(ctx, request, result)
	if err != nil || !wait.Done {
		t.Fatalf("mockgcp native operation/readback: %+v %v", wait, err)
	}
	batch, err = r.List(ctx, productRequest(r, monitoredProjectType, "project"))
	if err != nil || len(batch.Items) != 1 || batch.Items[0].NativeID != metricsTestScope+"/projects/123456" {
		t.Fatalf("mockgcp self membership was changed: %+v %v", batch, err)
	}
	deletes := 0
	for _, call := range calls {
		if strings.HasPrefix(call, "DELETE ") {
			deletes++
		}
	}
	if deletes != 1 {
		t.Fatalf("mockgcp writes: %+v", calls)
	}
	t.Logf("Independent mockgcp GET, project-number inventory, native DELETE, LRO GET, serialized resume and complete absence passed (%d Monitoring calls)", len(calls))
}
