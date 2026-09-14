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
	testNotificationChannelIndependentMockGCP(t, "email")
}
func TestNotificationChannelDeleteIndependentMockGCP(t *testing.T) {
	testNotificationChannelIndependentMockGCP(t, "pubsub")
}
func TestBillingBudgetIndependentMockGCP(t *testing.T) {
	testNotificationChannelIndependentMockGCP(t, "email", true)
}
func TestBillingBudgetInventoryIndependentMockGCP(t *testing.T) {
	testNotificationChannelIndependentMockGCP(t, "email", true, true)
}
func TestBillingBudgetDeleteIndependentMockGCP(t *testing.T) {
	testNotificationChannelIndependentMockGCP(t, "email", true, true, true)
}
func testNotificationChannelIndependentMockGCP(t *testing.T, delivery string, billing ...bool) {
	cleanupBudget := len(billing) > 2 && billing[2]
	inventoryAccount := "billingAccounts/ABCDEF-012345-678901"
	if cleanupBudget {
		inventoryAccount = "billingAccounts/ABCDEF-ABCDEF-ABCDEF"
	}
	inventoryBudget := len(billing) > 1 && billing[1]
	withBudget := len(billing) != 0 && billing[0]
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
	seed["type"] = delivery
	if delivery == "pubsub" {
		seed["labels"] = map[string]any{"topic": "projects/sample-project/topics/PRIVATE_CHANNEL_TOPIC"}
	}
	for _, field := range []string{"name", "futureNativeField", "verificationStatus", "creationRecord", "mutationRecords"} {
		delete(seed, field)
	}
	created := native("POST", "/v3/projects/sample-project/notificationChannels", seed)
	name := text(created["name"])
	if !strings.HasPrefix(name, "projects/sample-project/notificationChannels/") {
		t.Fatal("unexpected native identity")
	}
	channelExists := true
	t.Cleanup(func() {
		if channelExists {
			native("DELETE", "/v3/"+name, nil)
		}
	})
	budgetName := ""
	budgetExists := false
	if withBudget {
		account := map[string]any{"name": testBillingAccount}
		if !inventoryBudget {
			account = native("POST", "/v1/billingAccounts", billingAccountFixture())
		} else {
			// This case may run alone. The mock has no account DELETE; create through
			// a dedicated account identity when sharing the harness with the prior case.
			account = native("POST", "/v1/billingAccounts", map[string]any{"name": inventoryAccount, "displayName": "Inventory account"})
		}
		if (!inventoryBudget && account["name"] != testBillingAccount) || (inventoryBudget && account["name"] != inventoryAccount) {
			t.Fatal("wrong native billing account")
		}
		budget := billingBudgetFixture()
		delete(budget, "name")
		delete(budget, "etag")
		object(budget["notificationsRule"])["monitoringNotificationChannels"] = []any{name}
		createdBudget := native("POST", "/v1/"+text(account["name"])+"/budgets", budget)
		budgetName = text(createdBudget["name"])
		if !strings.HasPrefix(budgetName, text(account["name"])+"/budgets/") {
			t.Fatal("wrong native budget identity")
		}
		budgetExists = true
		t.Cleanup(func() {
			if budgetExists {
				native("DELETE", "/v1/"+budgetName, nil)
			}
		})
	}
	calls := []string{}
	billingFixtureCalls := 0
	budgetRuntimeDeletes := 0
	r := protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
		if req.URL.Host == "cloudbilling.googleapis.com" && delivery == "email" && !withBudget {
			billingFixtureCalls++
			return emptyBillingAccountsFixture(t, req), nil
		}
		billingRead := withBudget && (req.URL.Host == "cloudbilling.googleapis.com" || req.URL.Host == "billingbudgets.googleapis.com") && req.Method == "GET"
		billingDelete := cleanupBudget && req.URL.Host == "billingbudgets.googleapis.com" && req.Method == "DELETE" && req.URL.Path == "/v1/"+budgetName
		monitoringCall := req.URL.Host == "monitoring.googleapis.com" && (req.Method == "GET" || delivery != "email" && req.Method == "DELETE")
		if !billingRead && !billingDelete && !monitoringCall {
			t.Fatal("unexpected runtime request", req.Method, req.URL)
		}
		if req.Method == "DELETE" && strings.Contains(req.URL.Path, "/notificationChannels/") && req.URL.RawQuery != "force=false" {
			t.Fatal("forced native channel delete")
		}
		if billingDelete {
			budgetRuntimeDeletes++
			if req.URL.RawQuery != "" {
				t.Fatal("unexpected native budget delete query")
			}
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
			labels := map[string]any{"email_address": "PRIVATE_CHANNEL_NEW"}
			if delivery == "pubsub" {
				labels = map[string]any{"topic": "projects/sample-project/topics/PRIVATE_CHANNEL_NEW"}
			}
			native("PATCH", "/v3/"+name+"?updateMask=labels", map[string]any{"name": name, "labels": labels})
		}
		batch, err := r.List(t.Context(), productRequest(r, notificationChannelType, "global"))
		if err != nil || !batch.Complete || len(batch.Items) != 1 {
			t.Fatal(batch, err)
		}
		item := batch.Items[0]
		b, _ := json.Marshal(item)
		if item.NativeID != "//monitoring.googleapis.com/"+name || strings.Contains(string(b), "PRIVATE_CHANNEL") || item.Actionable == nil || *item.Actionable != (delivery != "email") {
			t.Fatal("invalid native inventory")
		}
		proof := text(item.Normalized[notificationChannelReview])
		if proof == "" || phase == "changed" && proof == previous {
			t.Fatal("changed native label not bound")
		}
		previous = proof
		target := asset.Asset{ID: "native-channel", Identity: asset.Identity{Provider: asset.ProviderGCP, ConnectionID: "connection", Partition: "gcp", NativeType: notificationChannelType, NativeID: item.NativeID}, Normalized: item.Normalized}
		channel = target
		if _, err := r.ResolveAction(t.Context(), "connection", target); (err == nil) != (delivery != "email") {
			t.Fatal("read-only channel exposes cleanup")
		}
	}
	result, err := r.Invoke(t.Context(), contracts.Invocation{ConnectionID: "connection", Operation: "monitoring.projects.notificationChannels.get", Parameters: map[string]any{"name": name}})
	b, _ := json.Marshal(result)
	if err != nil || strings.Contains(string(b), "PRIVATE_CHANNEL") {
		t.Fatal("private native Invoke response", err)
	}
	if inventoryBudget {
		req := billingInventoryRequest(r)
		batch, err := r.List(t.Context(), req)
		if err != nil || !batch.Complete || len(batch.Items) != 1 || batch.Items[0].NativeID != "//billingbudgets.googleapis.com/"+budgetName {
			t.Fatal(batch, err)
		}
		before := batch.Items[0].Normalized[billingBudgetReview]
		req.KnownNativeIDs = []string{batch.Items[0].NativeID}
		native("PATCH", "/v1/"+budgetName+"?updateMask=displayName", map[string]any{"name": budgetName, "displayName": "Updated native budget"})
		batch, err = r.List(t.Context(), req)
		if err != nil || len(batch.Items) != 1 || batch.Items[0].Name != "Updated native budget" || batch.Items[0].Normalized[billingBudgetReview] == before {
			t.Fatal(batch, err)
		}
		if cleanupBudget {
			item := batch.Items[0]
			value := asset.Asset{ID: "native-budget", Identity: channel.Identity, Normalized: item.Normalized}
			value.Identity.NativeType, value.Identity.NativeID = billingBudgetType, item.NativeID
			request := contracts.ActionRequest{Asset: value, Action: "delete", IdempotencyKey: "native-budget-delete"}
			driver, err := r.ResolveAction(t.Context(), "connection", value)
			if err != nil {
				t.Fatal(err)
			}
			result, err := driver.Execute(t.Context(), request)
			if err != nil {
				t.Fatal(err)
			}
			budgetExists = false
			encoded, _ := json.Marshal(result)
			var restored contracts.ActionResult
			if err := json.Unmarshal(encoded, &restored); err != nil {
				t.Fatal(err)
			}
			fresh := protocolRuntime(t, r.transport.RoundTrip)
			driver, err = fresh.ResolveAction(t.Context(), "connection", value)
			if err != nil {
				t.Fatal(err)
			}
			wait, err := driver.Wait(t.Context(), request, restored)
			if err != nil || !wait.Done {
				t.Fatal(wait, err)
			}
			settled, err := driver.(contracts.MutationSettlementReader).MutationSettled(t.Context(), request, restored)
			if err != nil || !settled.Settled || budgetRuntimeDeletes != 1 {
				t.Fatal(settled, err, budgetRuntimeDeletes)
			}
			if native("GET", "/v3/"+name, nil)["name"] != name {
				t.Fatal("budget cleanup removed channel")
			}
		} else {
			native("DELETE", "/v1/"+budgetName, nil)
		}
		budgetExists = false
		batch, err = r.List(t.Context(), req)
		if err != nil || !batch.Complete || len(batch.Items) != 0 || len(batch.AbsentNativeIDs) != 1 || batch.AbsentNativeIDs[0] != req.KnownNativeIDs[0] {
			t.Fatal(batch, err)
		}
		if cleanupBudget {
			t.Logf("Native reviewed budget DELETE, JSON restart, own-404 settlement, retained channel and inventory reconciliation passed (%d forwarded calls; %d runtime DELETE)", len(calls), budgetRuntimeDeletes)
		} else {
			t.Logf("Native budget inventory, changed configuration and known-budget DELETE/GET-404 reconciliation passed (%d forwarded GETs)", len(calls))
		}
		return
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
	policyExists := true
	t.Cleanup(func() {
		if policyExists {
			native("DELETE", "/v3/"+policyName, nil)
		}
	})
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
	if err == nil && delivery == "email" {
		contribution.Unresolved = assertBudgetCoverage(t, contribution.Unresolved, channel.ID)
	}
	if err == nil && withBudget {
		if len(contribution.Unresolved) != 1 || contribution.Unresolved[0].NativeType != billingBudgetType || contribution.Unresolved[0].NativeID != "//billingbudgets.googleapis.com/"+budgetName || !contribution.Unresolved[0].BlocksCleanup {
			t.Fatal("native budget reference missing", contribution)
		}
		contribution.Unresolved = nil
	}
	if (delivery == "email" && !withBudget && billingFixtureCalls != 2) || (withBudget && billingFixtureCalls != 0) || (delivery != "email" && billingFixtureCalls != 0) {
		t.Fatal("unexpected explicit billing fixture calls", billingFixtureCalls)
	}
	if err != nil || len(contribution.Unresolved) != 0 || len(contribution.Relationships) != 1 || contribution.Relationships[0].SourceAssetID != channel.ID || contribution.Relationships[0].TargetAssetID != policyAsset.ID {
		t.Fatal(contribution, err)
	}
	wantCalls := 12
	if withBudget {
		wantCalls += 7
	}
	if len(calls) != wantCalls {
		t.Fatal("unexpected native reads", calls)
	}
	if withBudget {
		native("PATCH", "/v1/"+budgetName+"?updateMask=notificationsRule", map[string]any{"name": budgetName, "notificationsRule": map[string]any{"monitoringNotificationChannels": []any{}}})
		updated, err := contributor.Contribute(t.Context(), "global", []asset.Asset{channel, policyAsset})
		if err != nil {
			t.Fatal(err)
		}
		if refs := assertBudgetCoverage(t, updated.Unresolved, channel.ID); len(refs) != 0 {
			t.Fatal("removed native budget reference retained", refs)
		}
		if len(updated.Relationships) != 1 {
			t.Fatal("budget removal lost policy reference")
		}
		t.Logf("Native Billing Account/Budget LIST/GET and channel graph, then native budget PATCH/reference removal passed (%d forwarded GETs; no Billing fixture)", len(calls))
		return
	}
	if delivery != "email" {
		driver, err := r.ResolveAction(t.Context(), "connection", channel)
		if err != nil {
			t.Fatal(err)
		}
		request := contracts.ActionRequest{Asset: channel, Action: "delete", IdempotencyKey: "native-channel", PrerequisiteDeletions: []contracts.ActionImpact{{Asset: policyAsset, ControllerID: channel.ID, Delete: true}}}
		if _, err := driver.Execute(t.Context(), request); err == nil {
			t.Fatal("native live policy was ignored")
		}
		policyDriver, err := r.ResolveAction(t.Context(), "connection", policyAsset)
		if err != nil {
			t.Fatal(err)
		}
		policyRequest := contracts.ActionRequest{Asset: policyAsset, Action: "delete", IdempotencyKey: "native-channel-policy"}
		policyResult, err := policyDriver.Execute(t.Context(), policyRequest)
		if err != nil {
			t.Fatal(err)
		}
		policyWait, err := policyDriver.Wait(t.Context(), policyRequest, policyResult)
		if err != nil || !policyWait.Done {
			t.Fatal(policyWait, err)
		}
		policyExists = false
		result, err := driver.Execute(t.Context(), request)
		if err != nil {
			t.Fatal(err)
		}
		encoded, _ := json.Marshal(result)
		if err := json.Unmarshal(encoded, &result); err != nil {
			t.Fatal(err)
		}
		fresh := protocolRuntime(t, r.transport.RoundTrip)
		driver, err = fresh.ResolveAction(t.Context(), "connection", channel)
		if err != nil {
			t.Fatal(err)
		}
		settled, err := driver.(contracts.MutationSettlementReader).MutationSettled(t.Context(), request, result)
		if err != nil || !settled.Settled {
			t.Fatal(settled, err)
		}
		wait, err := driver.Wait(t.Context(), request, result)
		if err != nil || !wait.Done {
			t.Fatal(wait, err)
		}
		channelExists = false
		channelDeletes, policyDeletes := 0, 0
		for _, call := range calls {
			if call == "DELETE /v3/"+name {
				channelDeletes++
			}
			if call == "DELETE /v3/"+policyName {
				policyDeletes++
			}
		}
		if channelDeletes != 1 || policyDeletes != 1 {
			t.Fatal("duplicate native deletion", calls)
		}
		t.Logf("Independent native policy/channel ordered delete, force=false, JSON restart, settlement and 404 passed (%d forwarded calls)", len(calls))
		return
	}
	t.Logf("Independent native LIST/GET, configuration refresh, redacted Invoke, native policy dependency and unavailable cleanup passed (%d forwarded GETs)", len(calls))
}
