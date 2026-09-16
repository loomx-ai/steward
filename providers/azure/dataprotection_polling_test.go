package azure

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
)

func TestDataProtectionOperationCallbackBoundaries(t *testing.T) {
	c := &client{subscription: testSubscription}
	group := c.root() + "/resourcegroups/test"
	vault := group + "/providers/microsoft.dataprotection/backupvaults/vault"
	id := vault + "/backupinstances/instance"
	regional := c.root() + "/providers/microsoft.dataprotection/locations/eastus"
	token := "MGQ0NDgwMzEtZTZmYi00YzVjLWIzY2QtMDFlMGE2NjVkY2E1Ozk2YmI4YjM0LTg4MjUtNGYxYS05MTE5LWM1OGI2ODZjNTZlMA=="
	endpoint := func(path string) string {
		return "https://management.azure.com" + path + "/" + token + "?api-version=" + dataProtectionVersion
	}
	for _, row := range []struct{ path, role string }{
		{regional + "/operationstatus", "status_url"},
		{vault + "/operationstatus", "status_url"},
		{group + "/providers/microsoft.dataprotection/operationstatus", "status_url"},
		{regional + "/operationresults", "result_url"},
		{vault + "/operationresults", "result_url"},
		{id + "/operationresults", "result_url"},
	} {
		if got, err := c.dataProtectionPollURL(id, "eastus", endpoint(row.path), row.role); err != nil || got != token {
			t.Fatal(row, got, err)
		}
	}
	if _, err := c.dataProtectionPollURL(id, "eastus/other", endpoint(regional+"/operationstatus"), "status_url"); err == nil {
		t.Fatal("accepted malformed owner region")
	}
	status := endpoint(vault + "/operationstatus")
	result := endpoint(regional + "/operationresults")
	h := http.Header{"Azure-Asyncoperation": {status}, "Location": {result}}
	if saved, err := c.dataProtectionOperationHeaders(id, "eastus", h); err != nil || len(saved) != 2 {
		t.Fatal(saved, err)
	}
	for _, bad := range []string{
		strings.Replace(status, "management.azure.com", "example.com", 1),
		strings.Replace(status, testSubscription, "00000000-0000-0000-0000-000000000001", 1),
		strings.Replace(status, "/resourcegroups/test/", "/resourcegroups/other/", 1),
		strings.Replace(status, "/backupvaults/vault/", "/backupvaults/other/", 1),
		strings.Replace(status, "/operationstatus/", "/operationresults/", 1),
		status + "&api-version=" + dataProtectionVersion,
		status + "&sig=unreviewed",
		strings.Replace(status, dataProtectionVersion, "2023-01-01", 1),
		strings.Replace(status, token, "%2e%2e", 1),
		status + "#fragment",
		strings.Replace(status, token, "", 1),
	} {
		if _, err := c.dataProtectionPollURL(id, "eastus", bad, "status_url"); err == nil {
			t.Fatal("accepted foreign callback", bad)
		}
	}
	if _, err := c.dataProtectionPollURL(id, "eastus", strings.Replace(result, "/eastus/", "/westus/", 1), "result_url"); err == nil {
		t.Fatal("accepted foreign region")
	}
	// The published delete example references a different instance without a vault.
	malformed := endpoint(group + "/providers/microsoft.dataprotection/backupinstances/harshitbi1/operationresults")
	if _, err := c.dataProtectionPollURL(id, "eastus", malformed, "result_url"); err == nil {
		t.Fatal("accepted malformed official example callback")
	}
	for _, bad := range []http.Header{
		{"Azure-Asyncoperation": {status, status}},
		{"Azure-Asyncoperation": {status}, "Location": {strings.Replace(result, token, strings.ToLower(token), 1)}},
		{"Azure-Asyncoperation": {""}},
		{"Operation-Location": {status}},
	} {
		if _, err := c.dataProtectionOperationHeaders(id, "eastus", bad); err == nil {
			t.Fatal("accepted ambiguous callback headers", bad)
		}
	}
}

func TestDataProtectionOperationReceiptRestart(t *testing.T) {
	group := "/subscriptions/" + testSubscription + "/resourcegroups/test"
	vault := group + "/providers/microsoft.dataprotection/backupvaults/vault"
	id := vault + "/backupinstances/instance"
	token := "Operation-AbC=="
	status := "https://management.azure.com" + vault + "/operationstatus/" + token + "?api-version=" + dataProtectionVersion
	result := "https://management.azure.com/subscriptions/" + testSubscription + "/providers/microsoft.dataprotection/locations/eastus/operationresults/" + token + "?api-version=" + dataProtectionVersion
	calls := 0
	runtime := protocolRuntime(t, func(q *http.Request) (*http.Response, error) {
		calls++
		if q.Method != "GET" || calls <= 2 && q.URL.String() != status || calls > 2 && q.URL.String() != result {
			t.Fatal("unexpected polling request", q.Method, q.URL)
		}
		if calls <= 2 {
			state := "Inprogress"
			if calls == 2 {
				state = "Succeeded"
			}
			return jsonResponse(200, map[string]any{"id": q.URL.Path, "name": token, "status": state}, nil), nil
		}
		if calls == 3 {
			return &http.Response{StatusCode: 202, Header: http.Header{"Retry-After": {"3"}}, Body: io.NopCloser(strings.NewReader(""))}, nil
		}
		return jsonResponse(200, map[string]any{"objectType": "OperationJobExtendedInfo"}, nil), nil
	})
	c, err := runtime.resolve(t.Context(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := c.dataProtectionDeleteReceipt(id, "eastus", response{status: 202, header: http.Header{"Azure-Asyncoperation": {status}, "Location": {result}}}, true)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		wire, err := json.Marshal(receipt)
		if err != nil {
			t.Fatal(err)
		}
		receipt = nil
		if err = json.Unmarshal(wire, &receipt); err != nil {
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
		poll, err := c.dataProtectionPoll(t.Context(), id, "eastus", receipt)
		if err != nil || poll.Done != (i == 3) {
			t.Fatal("poll restart", i, poll, err)
		}
		receipt = poll.Data
	}
	if poll, err := c.dataProtectionPoll(t.Context(), id, "eastus", receipt); err != nil || !poll.Done || calls != 4 {
		t.Fatal("completed receipt issued another request", poll, err, calls)
	}
	receipt["result_url"] = strings.Replace(result, "eastus", "westus", 1)
	if _, err := c.dataProtectionPoll(t.Context(), id, "eastus", receipt); err == nil || calls != 4 {
		t.Fatal("tampered receipt performed IO", err, calls)
	}
}

func TestDataProtectionDeleteAcknowledgementBoundaries(t *testing.T) {
	c := &client{subscription: testSubscription}
	id := c.root() + "/resourcegroups/test/providers/microsoft.dataprotection/backupvaults/vault/backupinstances/instance"
	for _, status := range []int{200, 204} {
		receipt, err := c.dataProtectionDeleteReceipt(id, "eastus", response{status: status}, true)
		if err != nil {
			t.Fatal(status, err)
		}
		if err = c.dataProtectionVerifyReceipt(id, "eastus", receipt); err != nil {
			t.Fatal(err)
		}
	}
	for _, res := range []response{{status: 202}, {status: 201}, {status: 404}, {status: 200, data: map[string]any{"unexpected": true}}, {status: 204, header: http.Header{"Location": {"https://example.com/"}}}} {
		if _, err := c.dataProtectionDeleteReceipt(id, "eastus", res, true); err == nil {
			t.Fatal("invalid acknowledgement accepted", res)
		}
	}
	if _, err := c.dataProtectionDeleteReceipt(id, "eastus", response{status: 200}, false); err == nil {
		t.Fatal("nonempty acknowledgement accepted")
	}
	if _, err := c.dataProtectionDeleteReceipt("invalid", "eastus", response{status: 204}, true); err == nil {
		t.Fatal("invalid receipt owner accepted")
	}
}

func TestDataProtectionPollingRejectsUncertainCompletion(t *testing.T) {
	for _, mode := range []string{"forbidden", "missing-operation", "failed", "cancelled", "unknown-state", "empty-status", "wrong-id", "wrong-name", "foreign-successor", "changed-token", "result-empty", "result-null", "result-wrong-type", "pending-body", "result-204"} {
		t.Run(mode, func(t *testing.T) {
			id := "/subscriptions/" + testSubscription + "/resourcegroups/test/providers/microsoft.dataprotection/backupvaults/vault/backupinstances/instance"
			token := "Operation-AbC=="
			status := "https://management.azure.com" + redisParentID(id) + "/operationstatus/" + token + "?api-version=" + dataProtectionVersion
			result := "https://management.azure.com/subscriptions/" + testSubscription + "/providers/microsoft.dataprotection/locations/eastus/operationresults/" + token + "?api-version=" + dataProtectionVersion
			runtime := protocolRuntime(t, func(q *http.Request) (*http.Response, error) {
				body := map[string]any{"id": q.URL.Path, "name": token, "status": "Succeeded"}
				headers := http.Header{}
				switch mode {
				case "forbidden":
					return jsonResponse(403, map[string]any{}, nil), nil
				case "missing-operation":
					return jsonResponse(404, map[string]any{}, nil), nil
				case "failed":
					body["status"] = "Failed"
				case "cancelled":
					body["status"] = "Canceled"
				case "unknown-state":
					body["status"] = "FutureState"
				case "empty-status":
					delete(body, "status")
				case "wrong-id":
					body["id"] = strings.Replace(q.URL.Path, "/vault/", "/other/", 1)
				case "wrong-name":
					body["name"] = strings.ToLower(token)
				case "foreign-successor":
					headers.Set("Location", strings.Replace(result, "eastus", "westus", 1))
				case "changed-token":
					headers.Set("Location", strings.Replace(result, token, "OtherOperation", 1))
				case "result-empty":
					return &http.Response{StatusCode: 200, Header: headers, Body: io.NopCloser(strings.NewReader(""))}, nil
				case "result-null":
					return &http.Response{StatusCode: 200, Header: headers, Body: io.NopCloser(strings.NewReader("null"))}, nil
				case "result-wrong-type":
					body = map[string]any{"objectType": "FutureResult"}
				case "pending-body":
					return jsonResponse(202, map[string]any{}, nil), nil
				case "result-204":
					return &http.Response{StatusCode: 204, Header: headers, Body: io.NopCloser(strings.NewReader(""))}, nil
				}
				return jsonResponse(200, body, headers), nil
			})
			c, err := runtime.resolve(t.Context(), "connection")
			if err != nil {
				t.Fatal(err)
			}
			headers := http.Header{"Azure-Asyncoperation": {status}}
			if strings.HasPrefix(mode, "result-") || mode == "pending-body" {
				headers = http.Header{"Location": {result}}
			}
			receipt, err := c.dataProtectionDeleteReceipt(id, "eastus", response{status: 202, header: headers}, true)
			if err != nil {
				t.Fatal(err)
			}
			poll, err := c.dataProtectionPoll(t.Context(), id, "eastus", receipt)
			if err == nil || poll.Done || isNotFound(err) {
				t.Fatal("uncertain operation treated as completion or own absence", poll, err)
			}
		})
	}
}

func TestDataProtectionOfficialOperationExamples(t *testing.T) {
	load := func(file string) map[string]any {
		t.Helper()
		wire, err := os.ReadFile("fixtures/dataprotection/" + file)
		if err != nil {
			t.Fatal(err)
		}
		var value map[string]any
		if err = json.Unmarshal(wire, &value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	sample := load("DeleteBackupInstance.json")
	params := object(sample["parameters"])
	c := &client{subscription: text(params["subscriptionId"])}
	id := strings.ToLower(c.root() + "/resourceGroups/" + text(params["resourceGroupName"]) + "/providers/Microsoft.DataProtection/backupVaults/" + text(params["vaultName"]) + "/backupInstances/" + text(params["backupInstanceName"]))
	headers := object(object(object(sample["responses"])["202"])["headers"])
	status, result := text(headers["Azure-AsyncOperation"]), text(headers["Location"])
	if _, err := c.dataProtectionPollURL(id, "eastus", status, "status_url"); err != nil {
		t.Fatal("native status route rejected", err)
	}
	if _, err := c.dataProtectionPollURL(id, "eastus", result, "result_url"); err == nil {
		t.Fatal("inconsistent sample Location accepted")
	}
	sample = load("GetOperationStatusVaultContext.json")
	params = object(sample["parameters"])
	c.subscription = text(params["subscriptionId"])
	id = strings.ToLower(c.root() + "/resourceGroups/" + text(params["resourceGroupName"]) + "/providers/Microsoft.DataProtection/backupVaults/" + text(params["vaultName"]) + "/backupInstances/instance")
	body := object(object(object(sample["responses"])["200"])["body"])
	status = "https://management.azure.com" + text(body["id"]) + "?api-version=" + dataProtectionVersion
	if token, err := c.dataProtectionPollURL(id, "westus", status, "status_url"); err != nil || token != body["name"] {
		t.Fatal("unmodified native operation identity rejected", token, err)
	}
	sample = load("GetOperationResult.json")
	headers = object(object(object(sample["responses"])["202"])["headers"])
	if _, err := c.dataProtectionOperationHeaders(id, "westus", http.Header{"Azure-Asyncoperation": {text(headers["Azure-AsyncOperation"])}, "Location": {text(headers["Location"])}}); err == nil {
		t.Fatal("foreign/version-inconsistent native sample accepted")
	}
}
