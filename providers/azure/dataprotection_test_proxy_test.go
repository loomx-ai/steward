package azure

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

// The four mutation/poll interactions are unchanged upstream CLI evidence.
// Preconditions and own-resource reconciliation below are explicitly synthetic:
// the official recording contains neither inventory nor a final vault GET.
func TestDataProtectionVaultIndependentProxy(t *testing.T) {
	if os.Getenv("STEWARD_AZURE_TEST_PROXY") == "" {
		t.Skip("optional independent Azure SDK Test Proxy integration")
	}
	wire, err := os.ReadFile("fixtures/dataprotection/vault-delete-recording.json")
	sum := sha256.Sum256(wire)
	if err != nil || hex.EncodeToString(sum[:]) != "446d74b12414cddd77e82045d4b70136eee6ea78cafd3a5253e763875aa31173" {
		t.Fatal("public CLI recording changed", err)
	}
	var fixture struct {
		Interactions []struct {
			Method, URL, Body string
			Status            int
			Headers           map[string][]string
		}
	}
	if err = json.Unmarshal(wire, &fixture); err != nil || len(fixture.Interactions) != 4 {
		t.Fatal("invalid public recording", err)
	}
	var entries []azureTestProxyEntry
	for _, row := range fixture.Interactions {
		headers := http.Header(row.Headers).Clone()
		headers.Set("Content-Type", "application/json")
		entries = append(entries, azureTestProxyEntry{RequestUri: row.URL, RequestMethod: row.Method, RequestHeaders: map[string][]string{}, RequestBody: nil, StatusCode: row.Status, ResponseHeaders: headers, ResponseBody: row.Body})
	}
	proxy := startAzureTestProxy(t, entries)
	// An independent matcher must reject these changes without consuming the
	// original interaction, including changes default SDK sanitizers can hide.
	for _, mode := range []string{"host", "version", "subscription", "signature"} {
		row := fixture.Interactions[0]
		if mode == "signature" {
			row = fixture.Interactions[1]
		}
		candidate := row.URL
		switch mode {
		case "host":
			candidate = strings.Replace(candidate, "management.azure.com", "untrusted.invalid", 1)
		case "version":
			candidate = strings.Replace(candidate, dataProtectionVaultVersion, "2025-01-01", 1)
		case "subscription":
			candidate = strings.Replace(candidate, "00000000-0000-0000-0000-000000000000", testSubscription, 1)
		case "signature":
			candidate = strings.Split(candidate, "&")[0]
		}
		request, err := http.NewRequestWithContext(t.Context(), row.Method, candidate, nil)
		if err != nil {
			t.Fatal(err)
		}
		response, err := proxy.RoundTrip(request)
		if err != nil {
			t.Fatal("negative replay failed", mode)
		}
		_, _ = io.Copy(io.Discard, response.Body)
		_ = response.Body.Close()
		if response.StatusCode != 404 {
			t.Fatal("independent matcher accepted changed recording", mode, response.StatusCode)
		}
	}
	original, err := url.Parse(fixture.Interactions[0].URL)
	if err != nil {
		t.Fatal(err)
	}
	id := strings.ToLower(original.Path)
	subscription := strings.Split(id, "/")[2]
	group := strings.Join(strings.Split(id, "/")[:5], "/")
	region := "centraluseuap"
	vault := map[string]any{"id": id, "name": last(id), "type": dataProtectionVault, "location": region, "properties": map[string]any{"provisioningState": "Succeeded", "securitySettings": map[string]any{"softDeleteSettings": map[string]any{"state": "AlwaysOn"}}}}
	exists := true
	deletes := 0
	runtime := protocolRuntime(t, func(request *http.Request) (*http.Response, error) {
		path := strings.ToLower(request.URL.Path)
		if request.Method == "DELETE" || strings.Contains(path, "/operationstatus/") || strings.Contains(path, "/operationresults/") {
			if request.Method == "DELETE" {
				deletes++
				if request.Header.Get("If-Match") != "" || !uuidPattern.MatchString(request.Header.Get("x-ms-client-request-id")) {
					return nil, fmt.Errorf("native delete headers changed")
				}
			}
			return proxy.RoundTrip(request)
		}
		if request.Method != "GET" {
			return nil, fmt.Errorf("unexpected synthetic context mutation")
		}
		switch path {
		case "/subscriptions/" + subscription:
			return jsonResponse(200, map[string]any{"subscriptionId": subscription, "tenantId": testTenant, "state": "Enabled"}, nil), nil
		case id:
			if request.URL.Query().Get("api-version") != dataProtectionVaultVersion {
				return nil, fmt.Errorf("synthetic vault read version changed")
			}
			if !exists {
				return jsonResponse(404, map[string]any{"error": map[string]any{"code": "ResourceNotFound"}}, nil), nil
			}
			return jsonResponse(200, vault, nil), nil
		case group:
			return jsonResponse(200, map[string]any{"id": group, "name": last(group), "type": groupType, "location": region, "properties": map[string]any{}}, nil), nil
		case "/subscriptions/" + subscription + "/providers/microsoft.authorization/locks", id + "/backuppolicies", id + "/backupinstances", id + "/deletedbackupinstances", id + "/backupresourceguardproxies", "/subscriptions/" + subscription + "/providers/microsoft.dataprotection/locations/" + region + "/deletedvaults":
			return jsonResponse(200, map[string]any{"value": []any{}}, nil), nil
		}
		if armPathProvider(request.URL.Path) != "microsoft.dataprotection" {
			// Existing synthetic index helpers use the shared fixture subscription.
			// Adapt only these context reads; recorded DELETE/poll URLs stay exact.
			contextRequest := request.Clone(request.Context())
			contextRequest.URL.Path = strings.Replace(request.URL.Path, "/subscriptions/"+subscription, "/subscriptions/"+testSubscription, 1)
			if response, ok := fleetGraphEmptyIndexes(t, contextRequest); ok {
				return response, nil
			}
		}
		return nil, fmt.Errorf("unexpected synthetic context read")
	})
	c, err := runtime.resolve(t.Context(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	c.subscription = subscription
	review, err := c.dataProtectionVaultReviewFor(t.Context(), id, nil)
	if err != nil {
		t.Fatal(err)
	}
	planned := asset.Asset{ID: "vault", Identity: asset.Identity{Provider: asset.ProviderAzure, Partition: "azure", ConnectionID: "connection", NativeID: id, NativeType: dataProtectionVault}, Location: region, Normalized: map[string]any{dataProtectionVaultReview: review, dataProtectionVaultProof: c.dataProtectionVaultProofFor(id, "connection", review), "_data_protection_configuration": review["observed"], "cleanup_protected": false}}
	req := contracts.ActionRequest{Asset: planned, Action: "delete", IdempotencyKey: "independent-proxy-vault"}
	driver, err := runtime.ResolveAction(t.Context(), "connection", planned)
	if err != nil {
		t.Fatal(err)
	}
	var logs []execution.JobLogEntry
	ctx := execution.WithJobLogSink(t.Context(), execution.JobLogSinkFunc(func(_ context.Context, entry execution.JobLogEntry) { logs = append(logs, entry) }))
	result, err := driver.Execute(ctx, req)
	if err != nil || deletes != 1 {
		t.Fatal("catalog-bound native DELETE replay failed", err, deletes)
	}
	// Recreate the provider before every phase and round-trip the saved receipt.
	for i := 0; i < 3; i++ {
		wire, _ := json.Marshal(result)
		result = contracts.ActionResult{}
		if err = json.Unmarshal(wire, &result); err != nil {
			t.Fatal(err)
		}
		fresh, err := NewRuntime(runtime.credentials)
		if err != nil {
			t.Fatal(err)
		}
		fresh.transport = runtime.transport
		c, err = fresh.resolve(t.Context(), "connection")
		if err != nil {
			t.Fatal(err)
		}
		c.subscription = subscription
		driver, err = fresh.ResolveAction(t.Context(), "connection", planned)
		if err != nil {
			t.Fatal(err)
		}
		resumed := req
		resumed.ExecutionResult = &result
		if _, err = driver.Execute(ctx, resumed); err != nil || deletes != 1 {
			t.Fatal("restarted action repeated DELETE", err)
		}
		poll, err := driver.Wait(ctx, req, result)
		if err != nil || poll.Done {
			t.Fatal("recorded polling skipped own-resource reconciliation", i, err)
		}
		result.Data = poll.Data
	}
	if proxy.calls.Load() != 8 || deletes != 1 || result.Data["operation_done"] != true {
		t.Fatal("recorded operation did not finish exactly once", proxy.calls.Load(), deletes)
	}
	if poll, err := driver.Wait(ctx, req, result); err != nil || poll.Done {
		t.Fatal("live synthetic vault incorrectly closed", err)
	}
	exists = false
	poll, err := driver.Wait(ctx, req, result)
	if err != nil || !poll.Done || poll.State != "active_absent" || object(poll.Data["retention"])["permanent_purge_verified"] != false {
		t.Fatal("synthetic absence overstated retention outcome", err)
	}
	if proxy.calls.Load() != 8 || deletes != 1 {
		t.Fatal("completed native receipt repeated network mutation/poll")
	}
	diagnostic, _ := json.Marshal(logs)
	signed, _ := url.Parse(fixture.Interactions[1].URL)
	for _, key := range []string{"t", "c", "s", "h"} {
		if strings.Contains(string(diagnostic), signed.Query().Get(key)) {
			t.Fatal("native diagnostics leaked signing material", key)
		}
	}
	// Production URL validation still runs before the loopback adapter.
	before := proxy.calls.Load()
	if _, err = c.request(ctx, "GET", "https://untrusted.invalid/subscriptions/"+subscription); err == nil || proxy.calls.Load() != before {
		t.Fatal("test adapter bypassed native origin validation")
	}
}
