package azure

import (
	"encoding/json"
	"maps"
	"net/http"
	"strings"
	"testing"
)

func TestAzureLocalPollingReceiptsAndBoundaries(t *testing.T) {
	for _, kind := range []string{azureLocalVMType, azureLocalAgentType, azureLocalDiskType, azureLocalNICType, azureLocalImageType, azureLocalMarketplaceType, azureLocalStorageType, azureLocalNetworkType} {
		t.Run(kind, func(t *testing.T) { testAzureLocalPollingReceiptsAndBoundaries(t, kind) })
	}
}

func testAzureLocalPollingReceiptsAndBoundaries(t *testing.T, kind string) {
	f := newLocalCleanupFixture(t)
	id := f.ids[kind]
	status, result := localCleanupPollURL("Azure-AsyncOperation"), localCleanupPollURL("Location")
	version := azureLocalVersion
	if kind == azureLocalNetworkType {
		version = "2025-06-01-preview"
		status = strings.ReplaceAll(status, azureLocalVersion, version)
		result = strings.ReplaceAll(result, azureLocalVersion, version)
	}
	header := http.Header{"Azure-Asyncoperation": {status}, "Location": {result}}
	receipt, err := f.client.azureLocalDeleteReceipt(id, response{status: 202, header: header})
	if err != nil || receipt["mode"] != "Azure-AsyncOperation" || receipt["url"] != status {
		t.Fatal("ARM header precedence", receipt, err)
	}
	for _, bad := range []string{
		"http://azure.async.operation/status", strings.Replace(status, "management.azure.com", "attacker.test", 1), strings.Replace(status, testSubscription, testTenant, 1), strings.Replace(status, "Microsoft.AzureStackHCI", "Microsoft.Compute", 1), strings.Replace(status, version, "2025-01-01", 1), status + "&api-version=2024-01-01", status + "#fragment", status + "&force=true", strings.Replace(status, "/operationStatuses/", "/virtualMachines/", 1), strings.Replace(status, "11111111-2222-3333-4444-555555555555", "not-an-operation", 1), strings.Replace(status, "/locations/", "/%6cocations/", 1), status + "&t=one&c=two&s=three", strings.Replace(status, "/eastus/", "/../", 1),
	} {
		if _, err := f.client.azureLocalDeleteReceipt(id, response{status: 202, header: http.Header{"Location": {bad}}}); err == nil {
			t.Fatal("unsafe callback accepted", bad)
		}
	}
	for _, res := range []response{{status: 202}, {status: 200}, {status: 202, data: map[string]any{"status": "Succeeded"}, header: header}, {status: 202, header: http.Header{"Location": {result, result}}}, {status: 202, header: http.Header{"Location": {""}}}, {status: 202, header: http.Header{"Operation-Location": {status}}}, {status: 202, header: http.Header{"Azure-Asyncoperation": {status}, "Location": {strings.Replace(result, "11111111-2222-3333-4444-555555555555", testTenant, 1)}}}} {
		if _, err := f.client.azureLocalDeleteReceipt(id, res); err == nil {
			t.Fatal("invalid initial receipt", res.status)
		}
	}
	for _, endpoint := range []string{status, "http://azure.async.operation/status", "https://untrusted.test/ignored"} {
		synchronous, err := f.client.azureLocalDeleteReceipt(id, response{status: 204, header: http.Header{"Azure-Asyncoperation": {endpoint}}})
		if err != nil || len(synchronous) != 1 {
			t.Fatal("terminal response retained a callback", synchronous, err)
		}
		if wait, err := f.client.azureLocalPoll(t.Context(), id, synchronous); err != nil || !wait.Done {
			t.Fatal("terminal response attempted polling", wait, err)
		}
	}
	for _, field := range []string{"url", "mode", "complete", "binding", "unknown"} {
		altered := maps.Clone(receipt)
		altered[field] = "forged"
		if _, err := f.client.azureLocalPoll(t.Context(), id, altered); err == nil {
			t.Fatal("tampered persisted receipt", field)
		}
	}
	if f.polls != 0 {
		t.Fatal("tampering reached network")
	}
	signed := status + "&t=one&c=two&s=three&h=four"
	receipt, err = f.client.azureLocalDeleteReceipt(id, response{status: 202, header: http.Header{"Azure-Asyncoperation": {signed}}})
	if err != nil {
		t.Fatal(err)
	}
	rotated := strings.Replace(signed, "t=one", "t=rotated", 1)
	calls := 0
	f.override = func(req *http.Request) (*http.Response, bool) {
		if req.URL.String() != signed && req.URL.String() != rotated {
			t.Fatal("lost complete signed callback", req.URL)
		}
		calls++
		if calls == 1 {
			return jsonResponse(200, map[string]any{"status": "InProgress"}, http.Header{"Azure-Asyncoperation": {rotated}, "Retry-After": {"7"}}), true
		}
		if req.URL.String() != rotated {
			t.Fatal("did not restore rotated URL")
		}
		return jsonResponse(200, map[string]any{"status": "Succeeded"}, nil), true
	}
	wait, err := f.client.azureLocalPoll(t.Context(), id, receipt)
	if err != nil || wait.Done || wait.Data["url"] != rotated || wait.RetryAfter.Seconds() != 7 {
		t.Fatal("rotation", wait, err)
	}
	data, _ := json.Marshal(wait.Data)
	var saved map[string]any
	_ = json.Unmarshal(data, &saved)
	fresh, err := NewRuntime(f.runtime.credentials)
	if err != nil {
		t.Fatal(err)
	}
	fresh.transport = f.runtime.transport
	c, err := fresh.resolve(t.Context(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	wait, err = c.azureLocalPoll(t.Context(), id, saved)
	if err != nil || !wait.Done || calls != 2 {
		t.Fatal("poll resume", wait, err)
	}
	if _, err := c.azureLocalPoll(t.Context(), id, wait.Data); err != nil || calls != 2 {
		t.Fatal("completed poll replay", err)
	}
}

func TestAzureLocalPollingFailureDoesNotMeanAbsence(t *testing.T) {
	for _, kind := range []string{azureLocalVMType, azureLocalAgentType, azureLocalDiskType, azureLocalNICType, azureLocalImageType, azureLocalMarketplaceType, azureLocalStorageType} {
		t.Run(kind, func(t *testing.T) { testAzureLocalPollingFailureDoesNotMeanAbsence(t, kind) })
	}
}

func testAzureLocalPollingFailureDoesNotMeanAbsence(t *testing.T, kind string) {
	for _, failure := range []string{"404", "403", "500", "failed", "canceled", "missing-status", "wrong-id", "wrong-name", "unknown-state", "wrong-type", "error-body", "rotation", "redirect"} {
		t.Run(failure, func(t *testing.T) {
			f := newLocalCleanupFixture(t)
			id := f.ids[kind]
			endpoint := localCleanupPollURL("Azure-AsyncOperation")
			receipt, err := f.client.azureLocalDeleteReceipt(id, response{status: 202, header: http.Header{"Azure-Asyncoperation": {endpoint}}})
			if err != nil {
				t.Fatal(err)
			}
			f.override = func(req *http.Request) (*http.Response, bool) {
				body := map[string]any{"status": "Succeeded"}
				header := http.Header{}
				code := 200
				switch failure {
				case "404":
					code = 404
				case "403":
					code = 403
				case "500":
					code = 500
				case "failed":
					body["status"] = "Failed"
				case "canceled":
					body["status"] = "Canceled"
				case "missing-status":
					delete(body, "status")
				case "wrong-id":
					body["resourceId"] = f.ids[azureLocalVMType]
					if kind == azureLocalVMType {
						body["resourceId"] = f.ids[azureLocalAgentType]
					}
				case "wrong-name":
					body["name"] = "other"
				case "unknown-state":
					body["status"] = "Invalid"
				case "wrong-type":
					body["status"] = true
				case "error-body":
					body["error"] = map[string]any{"code": "InternalError", "message": "private-local-error"}
				case "rotation":
					header.Set("Azure-AsyncOperation", strings.Replace(endpoint, "11111111-2222-3333-4444-555555555555", testTenant, 1))
				case "redirect":
					code = 302
					header.Set("Location", "https://attacker.test/private")
				}
				return jsonResponse(code, body, header), true
			}
			if wait, err := f.client.azureLocalPoll(t.Context(), id, receipt); err == nil || wait.Done {
				t.Fatal("failed poll became success", failure, wait, err)
			}
		})
	}
	for _, mode := range []string{"Azure-AsyncOperation", "Location"} {
		t.Run("pending-"+mode, func(t *testing.T) {
			f := newLocalCleanupFixture(t)
			id := f.ids[kind]
			receipt, err := f.client.azureLocalDeleteReceipt(id, response{status: 202, header: http.Header{http.CanonicalHeaderKey(mode): {localCleanupPollURL(mode)}}})
			if err != nil {
				t.Fatal(err)
			}
			f.override = func(*http.Request) (*http.Response, bool) {
				if mode == "Location" {
					return &http.Response{StatusCode: 202, Header: http.Header{}, Body: http.NoBody}, true
				}
				return jsonResponse(200, map[string]any{"status": "Running"}, nil), true
			}
			if wait, err := f.client.azureLocalPoll(t.Context(), id, receipt); err != nil || wait.Done {
				t.Fatal("pending", wait, err)
			}
		})
	}
}

func TestAzureLocalPollingOwnerIsolation(t *testing.T) {
	f := newLocalCleanupFixture(t)
	endpoint := localCleanupPollURL("Azure-AsyncOperation")
	initial := response{status: 202, header: http.Header{"Azure-Asyncoperation": {endpoint}}}
	calls := 0
	f.override = func(*http.Request) (*http.Response, bool) {
		calls++
		return jsonResponse(200, map[string]any{"status": "Succeeded"}, nil), true
	}
	vm, guest := f.ids[azureLocalVMType], f.ids[azureLocalAgentType]
	owners := []string{vm, guest, f.ids[azureLocalDiskType], f.ids[azureLocalNICType], f.ids[azureLocalImageType], f.ids[azureLocalMarketplaceType], f.ids[azureLocalStorageType]}
	for _, id := range owners {
		receipt, err := f.client.azureLocalDeleteReceipt(id, initial)
		if err != nil {
			t.Fatal("reviewed owner rejected", id, err)
		}
		for _, other := range append(append([]string{}, owners...), strings.Replace(id, azureLocalMachine(id), azureLocalMachine(id)+"-other", 1)) {
			if other == id {
				continue
			}
			if wait, err := f.client.azureLocalPoll(t.Context(), other, receipt); err == nil || wait.Done {
				t.Fatal("receipt crossed owner", id, other, wait, err)
			}
		}
		foreign := *f.client
		foreign.subscription = testTenant
		if _, err := foreign.azureLocalPoll(t.Context(), id, receipt); err == nil {
			t.Fatal("receipt crossed connection subscription")
		}
	}
	invalid := []string{
		"", azureLocalMachine(vm), strings.ToUpper(vm), " " + vm,
		strings.Replace(vm, "/default", "/other", 1), vm + "/default",
		strings.Replace(vm, testSubscription, testTenant, 1),
		strings.Replace(vm, "/providers/microsoft.hybridcompute/", "/microsoft.hybridcompute/", 1),
	}
	for kind, id := range f.ids {
		if kind != azureLocalVMType && kind != azureLocalAgentType && !azureLocalIndependent(kind) {
			invalid = append(invalid, id)
		}
	}
	for _, id := range invalid {
		if _, err := f.client.azureLocalDeleteReceipt(id, initial); err == nil {
			t.Fatal("unreviewed receipt owner", id)
		}
		if _, err := f.client.azureLocalDeleteReceipt(id, response{status: 204}); err == nil {
			t.Fatal("synchronous receipt bypassed owner validation", id)
		}
		if _, err := f.client.azureLocalPollURL(id, endpoint); err == nil {
			t.Fatal("invalid poll owner", id)
		}
		if _, err := f.client.azureLocalPoll(t.Context(), id, f.client.azureLocalSignReceipt(id, nil)); err == nil {
			t.Fatal("signed synchronous receipt bypassed owner validation", id)
		}
	}
	if calls != 0 {
		t.Fatal("invalid receipt reached the network", calls)
	}
}
