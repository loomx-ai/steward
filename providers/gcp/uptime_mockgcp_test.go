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
	r := protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
		if req.URL.Host != "monitoring.googleapis.com" {
			t.Fatal(req.URL)
		}
		calls = append(calls, req.Method+" "+req.URL.Path)
		local := req.Clone(req.Context())
		local.URL.Scheme, local.URL.Host, local.Host = u.Scheme, u.Host, u.Host
		return http.DefaultTransport.RoundTrip(local)
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
	if deletes != 1 {
		t.Fatal("duplicate native deletion", calls)
	}
	t.Logf("Independent Google mockgcp native GET, configuration review, synchronous DELETE, JSON resume and 404 passed (%d calls); LIST remains unimplemented", len(calls))
}
