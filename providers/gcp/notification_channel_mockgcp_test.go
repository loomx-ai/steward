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

func TestNotificationChannelIndependentMockGCP(t *testing.T) {
	endpoint := os.Getenv("STEWARD_NOTIFICATION_CHANNEL_MOCKGCP_URL")
	if endpoint == "" {
		t.Skip("set STEWARD_NOTIFICATION_CHANNEL_MOCKGCP_URL to the pinned Monitoring harness")
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" || u.Port() == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		t.Fatal("mockgcp must use a loopback HTTP origin")
	}
	httpClient := &http.Client{Timeout: 10 * time.Second}
	// Native fixture setup and teardown are outside the runtime's read-only calls.
	native := func(method, path string, body map[string]any) map[string]any {
		t.Helper()
		var input io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			input = bytes.NewReader(b)
		}
		req, err := http.NewRequest(method, endpoint+path, input)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		res, err := httpClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, err := io.ReadAll(res.Body)
		if err != nil || res.StatusCode != 200 {
			t.Fatalf("native fixture %s failed: %d, %v", method, res.StatusCode, err)
		}
		var data map[string]any
		if json.Unmarshal(b, &data) != nil {
			t.Fatal("invalid native response")
		}
		return data
	}
	seed := notificationChannelFixture()
	for _, field := range []string{"name", "futureNativeField", "verificationStatus", "creationRecord", "mutationRecords"} {
		delete(seed, field)
	}
	created := native("POST", "/v3/projects/sample-project/notificationChannels", seed)
	name := text(created["name"])
	if !strings.HasPrefix(name, "projects/sample-project/notificationChannels/") {
		t.Fatal("unexpected native identity")
	}
	t.Cleanup(func() { native("DELETE", "/v3/"+name, nil) })
	calls := []string{}
	r := protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
		if req.URL.Host != "monitoring.googleapis.com" || req.Method != "GET" {
			t.Fatal("unexpected runtime request", req.Method, req.URL)
		}
		calls = append(calls, req.Method+" "+req.URL.Path)
		local := req.Clone(req.Context())
		local.URL.Scheme, local.URL.Host, local.Host = u.Scheme, u.Host, u.Host
		return http.DefaultTransport.RoundTrip(local)
	})
	var previous string
	var channel asset.Asset
	for _, phase := range []string{"initial", "changed"} {
		if phase == "changed" {
			native("PATCH", "/v3/"+name+"?updateMask=labels", map[string]any{"name": name, "labels": map[string]any{"email_address": "PRIVATE_CHANNEL_NEW"}})
		}
		batch, err := r.List(t.Context(), productRequest(r, notificationChannelType, "global"))
		if err != nil || !batch.Complete || len(batch.Items) != 1 {
			t.Fatal(batch, err)
		}
		item := batch.Items[0]
		b, _ := json.Marshal(item)
		if item.NativeID != "//monitoring.googleapis.com/"+name || strings.Contains(string(b), "PRIVATE_CHANNEL") || item.Actionable == nil || *item.Actionable {
			t.Fatal("invalid native inventory")
		}
		proof := text(item.Normalized[notificationChannelReview])
		if proof == "" || phase == "changed" && proof == previous {
			t.Fatal("changed native label not bound")
		}
		previous = proof
		target := asset.Asset{ID: "native-channel", Identity: asset.Identity{Provider: asset.ProviderGCP, ConnectionID: "connection", Partition: "gcp", NativeType: notificationChannelType, NativeID: item.NativeID}, Normalized: item.Normalized}
		channel = target
		if _, err := r.ResolveAction(t.Context(), "connection", target); err == nil {
			t.Fatal("read-only channel exposes cleanup")
		}
	}
	result, err := r.Invoke(t.Context(), contracts.Invocation{ConnectionID: "connection", Operation: "monitoring.projects.notificationChannels.get", Parameters: map[string]any{"name": name}})
	b, _ := json.Marshal(result)
	if err != nil || strings.Contains(string(b), "PRIVATE_CHANNEL") {
		t.Fatal("private native Invoke response", err)
	}
	policySeed := alertPolicyFixture()
	delete(policySeed, "name")
	delete(policySeed, "futureNativeField")
	policySeed["notificationChannels"] = []any{name}
	policy := native("POST", "/v3/projects/sample-project/alertPolicies", policySeed)
	policyName := text(policy["name"])
	if !strings.HasPrefix(policyName, "projects/sample-project/alertPolicies/") {
		t.Fatal("unexpected native policy identity")
	}
	t.Cleanup(func() { native("DELETE", "/v3/"+policyName, nil) })
	batch, err := r.List(t.Context(), productRequest(r, alertPolicyType, "global"))
	if err != nil || len(batch.Items) != 1 {
		t.Fatal(batch, err)
	}
	policyAsset := asset.Asset{ID: "native-policy", Identity: channel.Identity, Normalized: batch.Items[0].Normalized}
	policyAsset.Identity.NativeType, policyAsset.Identity.NativeID = alertPolicyType, batch.Items[0].NativeID
	contributor, err := r.MonitoringDependencies(t.Context(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	contribution, err := contributor.Contribute(t.Context(), "global", []asset.Asset{channel, policyAsset})
	if err != nil || len(contribution.Unresolved) != 0 || len(contribution.Relationships) != 1 || contribution.Relationships[0].SourceAssetID != channel.ID || contribution.Relationships[0].TargetAssetID != policyAsset.ID {
		t.Fatal(contribution, err)
	}
	if len(calls) != 12 {
		t.Fatal("unexpected native reads", calls)
	}
	t.Logf("Independent native LIST/GET, configuration refresh, redacted Invoke, native policy dependency and unavailable cleanup passed (%d forwarded GETs)", len(calls))
}
