package azure

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func fleetTestOperation(t *testing.T, c *client) (*action, string) {
	t.Helper()
	id := strings.ToLower(resourceID(fleetType, "fleet1")) + "/updateruns/run1"
	kind, _ := findType(fleetRunType)
	a := &action{client: c, id: id, kind: kind, location: "westus", connectionID: "connection", partition: "azure", endpoint: apiURL(id, fleetVersion)}
	endpoint := "https://management.azure.com" + c.root() + "/providers/Microsoft.ContainerService/locations/westus/operations/" + rbacTestRoleName + "?api-version=2016-03-30&t=639195408663496002&c=private-certificate&s=private-signature&h=private-hash"
	return a, endpoint
}

func TestFleetNativeOperationURLAndHeaderBoundaries(t *testing.T) {
	a, endpoint := fleetTestOperation(t, directClient(nil))
	if err := a.validateOperationURL(endpoint); err != nil {
		t.Fatal("signed native operation rejected", err)
	}
	unsigned := "https://management.azure.com" + a.client.root() + "/resourceGroups/test/providers/Microsoft.ContainerService/locations/westus/operationResults/" + rbacTestRoleName + "?api-version=2022-02-01"
	if err := a.validateOperationURL(unsigned); err != nil {
		t.Fatal("published native operation rejected", err)
	}
	for _, bad := range []string{
		strings.Replace(endpoint, "management.azure.com", "foreign.example", 1),
		strings.Replace(endpoint, "https:", "http:", 1),
		strings.Replace(endpoint, testSubscription, rbacOtherSubscription, 1),
		strings.Replace(endpoint, "westus", "eastus", 1),
		strings.Replace(endpoint, "Microsoft.ContainerService", "Microsoft.Compute", 1),
		strings.Replace(unsigned, "resourceGroups/test", "resourceGroups/another", 1),
		strings.Replace(endpoint, "2016-03-30", fleetVersion, 1),
		strings.Replace(endpoint, rbacTestRoleName, "not-a-guid", 1),
		endpoint + "&api-version=2016-03-30", endpoint + "&s=other", endpoint + "&unknown=x", endpoint + "#fragment",
		strings.Replace(endpoint, "&h=private-hash", "", 1), strings.Replace(endpoint, "private-hash", "", 1),
		strings.Replace(endpoint, "/operations/", "/fleets/", 1), strings.Replace(unsigned, "2022-02-01", "2099-01-01", 1),
	} {
		if a.validateOperationURL(bad) == nil {
			t.Fatal("invalid Fleet operation URL accepted")
		}
	}
	for _, mode := range []string{"native", "unsigned", "missing", "duplicate", "foreign-location", "different-operation", "wrong-body", "partial", "failure"} {
		t.Run(mode, func(t *testing.T) {
			res := response{status: 202, header: http.Header{"Azure-Asyncoperation": {endpoint}}}
			switch mode {
			case "unsigned":
				res.header = http.Header{"Location": {unsigned}}
			case "missing":
				res.header = nil
			case "duplicate":
				res.header.Add("Azure-AsyncOperation", endpoint)
			case "foreign-location":
				res.header.Set("Location", strings.Replace(unsigned, "westus", "eastus", 1))
			case "different-operation":
				res.header.Set("Location", strings.Replace(unsigned, rbacTestRoleName, rbacTestAssignmentName, 1))
			case "wrong-body":
				res.data = fleetTestBody(t, fleetRunType, "another")
			case "partial":
				res.status = 206
			case "failure":
				res.data = map[string]any{"status": "Failed", "error": map[string]any{"code": "NativeFailure"}}
			}
			result, err := a.operationResult(res)
			if (err == nil) != (mode == "native" || mode == "unsigned") {
				t.Fatal("Fleet mutation envelope boundary changed", mode, err)
			}
			if err == nil && text(result.Data["fleet_operation_binding"]) == "" {
				t.Fatal("operation has no private receipt")
			}
		})
	}
}

func TestFleetPollingStateReceiptAndResourceReadback(t *testing.T) {
	for _, mode := range []string{"pending", "success-live", "expired-live", "success-gone", "wrong-name", "wrong-state", "missing-state", "error-string", "error-object", "partial", "forbidden", "changed-operation", "changed-mode", "changed-resource", "changed-credential"} {
		t.Run(mode, func(t *testing.T) {
			calls := 0
			var endpoint string
			var a *action
			c := directClient(func(req *http.Request) (*http.Response, error) {
				calls++
				if req.Method != "GET" {
					t.Fatal("poll mutated Fleet")
				}
				if strings.EqualFold(req.URL.Path, a.id) {
					if mode == "success-gone" {
						return jsonResponse(404, map[string]any{"error": map[string]any{"code": "ResourceNotFound"}}, nil), nil
					}
					return jsonResponse(200, fleetTestBody(t, fleetRunType, "run1"), nil), nil
				}
				if req.URL.String() != endpoint {
					t.Fatal("poll escaped reviewed operation")
				}
				body := map[string]any{"name": rbacTestRoleName, "status": "Succeeded"}
				status := 200
				switch mode {
				case "pending":
					body["status"] = "InProgress"
				case "expired-live":
					status, body = 404, map[string]any{"error": map[string]any{"code": "OperationNotFound"}}
				case "wrong-name":
					body["name"] = rbacTestAssignmentName
				case "wrong-state":
					body["status"] = "Unknown"
				case "missing-state":
					delete(body, "status")
				case "error-string":
					body["error"] = "private-error"
				case "error-object":
					body["error"] = map[string]any{"code": "NativeFailure"}
				case "partial":
					status = 206
				case "forbidden":
					status, body = 403, map[string]any{"error": map[string]any{"code": "AuthorizationFailed"}}
				}
				return jsonResponse(status, body, http.Header{"Retry-After": {"1"}}), nil
			})
			a, endpoint = fleetTestOperation(t, c)
			result, err := a.operationResult(response{status: 202, header: http.Header{"Azure-Asyncoperation": {endpoint}}})
			if err != nil {
				t.Fatal(err)
			}
			request := contracts.ActionRequest{Action: "delete", Asset: asset.Asset{ID: "run", Identity: asset.Identity{Provider: asset.ProviderAzure, ConnectionID: "connection", Partition: "azure", NativeID: a.id, NativeType: fleetRunType}, Location: a.location}}
			parent := fleetTestBody(t, fleetType, "fleet1")
			parent["location"] = a.location
			item, err := (&Runtime{}).fleetInventoryItem(c, fleetRunType, fleetTestBody(t, fleetRunType, "run1"), parent, map[string]any{"id": c.root() + "/resourcegroups/test"}, nil)
			if err != nil {
				t.Fatal(err)
			}
			request.Asset.Normalized = item.Normalized
			switch mode {
			case "changed-operation":
				result.ProviderOperationID = strings.Replace(endpoint, rbacTestRoleName, rbacTestAssignmentName, 1)
			case "changed-mode":
				result.Data["polling"] = "location"
			case "changed-resource":
				a.id = strings.Replace(a.id, "run1", "run2", 1)
			case "changed-credential":
				a.client.fingerprint[0]++
			}
			wait, err := a.Wait(t.Context(), request, result)
			want := slices.Contains([]string{"pending", "success-live", "expired-live", "success-gone"}, mode)
			if (err == nil) != want || want && wait.Done != (mode == "success-gone") {
				t.Fatal("Fleet operation/own resource boundary changed", mode, wait, err)
			}
			if strings.HasPrefix(mode, "changed-") && calls != 0 {
				t.Fatal("unbound receipt reached native API")
			}
		})
	}
}

func TestFleetSignedPollingSuccessorSurvivesSerialization(t *testing.T) {
	calls := []string{}
	var initial, next string
	c := directClient(func(req *http.Request) (*http.Response, error) {
		calls = append(calls, req.URL.String())
		if len(calls) == 1 {
			return jsonResponse(200, map[string]any{"status": "InProgress", "name": rbacTestRoleName}, http.Header{"Azure-Asyncoperation": {next}}), nil
		}
		if req.URL.String() != next {
			t.Fatal("resumed poll lost refreshed signature")
		}
		return jsonResponse(200, map[string]any{"status": "Succeeded", "name": rbacTestRoleName}, nil), nil
	})
	a, initial := fleetTestOperation(t, c)
	next = strings.Replace(initial, "private-signature", "refreshed-signature", 1)
	result, err := a.operationResult(response{status: 202, header: http.Header{"Azure-Asyncoperation": {initial}}})
	if err != nil {
		t.Fatal(err)
	}
	wait, err := a.poll(t.Context(), result)
	if err != nil || wait.Done || wait.Data["fleet_poll_operation"] != next {
		t.Fatal("native successor was not persisted", wait, err)
	}
	result.Data = wait.Data
	payload, _ := json.Marshal(result)
	var resumed contracts.ActionResult
	if err := json.Unmarshal(payload, &resumed); err != nil {
		t.Fatal(err)
	}
	if wait, err := a.poll(t.Context(), resumed); err != nil || !wait.Done {
		t.Fatal("serialized poll failed", wait, err)
	}
	resumed.Data["fleet_poll_operation"] = strings.Replace(next, "refreshed-signature", "forged-signature", 1)
	if _, err := a.poll(t.Context(), resumed); err == nil || len(calls) != 2 {
		t.Fatal("forged poll successor was followed")
	}
}

func TestFleetRecordedDeletionPollingAndLogRedaction(t *testing.T) {
	payload, err := os.ReadFile("fixtures/fleet/cli-deletion-polls.json")
	var polls struct {
		Sources   []map[string]string `json:"sources"`
		Responses []rbacRecording     `json:"responses"`
	}
	if err != nil || fmt.Sprintf("%x", sha256.Sum256(payload)) != "5121af65a0c86fa3a504acf1974d69a5f8adbe6911e7372d5d4821471007df73" || json.Unmarshal(payload, &polls) != nil || len(polls.Responses) != 13 {
		t.Fatal("native Fleet poll evidence changed", err)
	}
	deletes := fleetRecordings(t)
	var entries []execution.JobLogEntry
	ctx := execution.WithJobLogSink(t.Context(), execution.JobLogSinkFunc(func(_ context.Context, entry execution.JobLogEntry) { entries = append(entries, entry) }))
	done, pending := 0, 0
	for _, row := range polls.Responses {
		t.Run(fmt.Sprintf("%s/%d", last(row.SourceURI), row.Interaction), func(t *testing.T) {
			u, err := url.Parse(row.URL)
			if err != nil || row.Method != "GET" {
				t.Fatal("invalid native poll")
			}
			var deletion rbacRecording
			for _, candidate := range deletes {
				if candidate.SourceURI != row.SourceURI || candidate.SourceSHA256 != row.SourceSHA256 || candidate.Method != "DELETE" || candidate.Interaction >= row.Interaction {
					continue
				}
				for key, values := range candidate.Headers {
					if strings.EqualFold(key, "Azure-AsyncOperation") && len(values) == 1 {
						target, _ := url.Parse(values[0])
						if last(target.Path) == last(u.Path) {
							deletion = candidate
						}
					}
				}
			}
			if deletion.URL == "" {
				t.Fatal("poll has no preceding recorded Fleet deletion")
			}
			c := directClient(func(req *http.Request) (*http.Response, error) {
				if req.Method != "GET" || req.URL.String() != row.URL {
					t.Fatal("recorded polling path changed")
				}
				headers := http.Header{}
				for key, values := range row.Headers {
					for _, value := range values {
						headers.Add(key, value)
					}
				}
				return &http.Response{StatusCode: row.Status, Header: headers, Body: io.NopCloser(strings.NewReader(row.Body))}, nil
			})
			c.subscription = "00000000-0000-0000-0000-000000000000"
			delURL, _ := url.Parse(deletion.URL)
			id, kind, _ := fleetIdentity(delURL.Path)
			a := &action{client: c, id: id, kind: resourceType{NativeType: kind, Version: fleetVersion}, location: "westcentralus", connectionID: "connection", partition: "azure"}
			headers := http.Header{}
			for key, values := range deletion.Headers {
				for _, value := range values {
					headers.Add(key, value)
				}
			}
			if strings.Contains(strings.ToLower(u.Path), "/operationresults/") {
				headers.Del("Azure-AsyncOperation")
			} // Native Location fallback, with its original response body.
			var body map[string]any
			if deletion.Body != "" && json.Unmarshal([]byte(deletion.Body), &body) != nil {
				t.Fatal("invalid recorded DELETE body")
			}
			result, err := a.operationResult(response{status: deletion.Status, header: headers, data: body})
			if err != nil {
				t.Fatal("native DELETE envelope rejected", err)
			}
			wait, err := a.poll(ctx, result)
			if err != nil {
				t.Fatal("native poll rejected", err)
			}
			if wait.Done {
				done++
			} else {
				pending++
			}
			// No signing material from request or echoed response headers is public.
			log, _ := json.Marshal(entries)
			for _, key := range []string{"c", "h", "s", "t"} {
				if value := u.Query().Get(key); value != "" && strings.Contains(string(log), value) {
					t.Fatal("signed Fleet poll parameter entered logs", key)
				}
			}
		})
	}
	if done != 10 || pending != 3 || len(entries) == 0 {
		t.Fatal("native Fleet poll coverage changed", done, pending)
	}
}
