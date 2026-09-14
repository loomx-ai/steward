package gcp

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

// All Compute responses, including aggregated inventory and state defaults, come
// from the unmodified pinned Google server. protocolRuntime supplies only OAuth
// and the connected project's CRM record; it does not substitute Compute calls.
func TestFutureReservationIndependentMockGCP(t *testing.T) {
	endpoint := os.Getenv("STEWARD_FUTURE_RESERVATION_MOCKGCP_URL")
	if endpoint == "" {
		t.Skip("set STEWARD_FUTURE_RESERVATION_MOCKGCP_URL to the pinned Compute harness")
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" || u.Port() == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		t.Fatal("mockgcp must use a local loopback HTTP origin")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	transport := &http.Transport{Proxy: nil}
	t.Cleanup(transport.CloseIdleConnections)
	localClient := &http.Client{Timeout: 10 * time.Second, Transport: transport}
	fixtureCalls := 0
	call := func(method, path string, body any) map[string]any {
		t.Helper()
		payload := ""
		if body != nil {
			encoded, err := json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
			payload = string(encoded)
		}
		req, err := http.NewRequestWithContext(ctx, method, endpoint+path, strings.NewReader(payload))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		response, err := localClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		raw, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		var data map[string]any
		if response.StatusCode != 200 || json.Unmarshal(raw, &data) != nil {
			t.Fatalf("mockgcp %s %s: %d %s", method, path, response.StatusCode, raw)
		}
		fixtureCalls++
		return data
	}
	operationDone := func(zone string, operation map[string]any) {
		t.Helper()
		name := text(operation["name"])
		if !segmentPattern.MatchString(name) {
			t.Fatal("native mutation omitted operation identity", operation)
		}
		result := call("GET", "/compute/v1/projects/sample-project/zones/"+zone+"/operations/"+name, nil)
		if result["status"] != "DONE" || result["error"] != nil {
			t.Fatal("native fixture operation incomplete", result)
		}
	}
	createdPaths := map[string]bool{}
	t.Cleanup(func() {
		// Only resources created by this invocation are eligible for fixture cleanup.
		cleanupCtx, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		for path, deleted := range createdPaths {
			if deleted {
				continue
			}
			req, err := http.NewRequestWithContext(cleanupCtx, "DELETE", endpoint+path, nil)
			if err != nil {
				t.Error(err)
				continue
			}
			response, err := localClient.Do(req)
			if err != nil {
				t.Error(err)
				continue
			}
			response.Body.Close()
			if response.StatusCode != 200 && response.StatusCode != 404 {
				t.Errorf("native fixture cleanup %s: %d", path, response.StatusCode)
			}
		}
	})
	suffix := strconv.FormatInt(time.Now().UnixNano(), 36)
	start := time.Now().UTC().Truncate(time.Second).AddDate(1, 0, 0)
	primaryPath := ""
	for _, zone := range []string{"us-central1-a", "europe-west1-b"} {
		name := "steward-fr-" + suffix + "-" + zone
		path := "/compute/v1/projects/sample-project/zones/" + zone + "/futureReservations/" + name
		// Both durations are converted into timestamps by the independent server.
		body := map[string]any{"name": name, "timeWindow": map[string]any{"startTime": start.Format(time.RFC3339), "duration": map[string]any{"seconds": "86400"}}, "specificSkuProperties": map[string]any{"totalCount": "2", "instanceProperties": map[string]any{"machineType": "n2-standard-2"}}, "autoDeleteAutoCreatedReservations": true, "autoCreatedReservationsDuration": map[string]any{"seconds": "3600"}}
		operation := call("POST", strings.TrimSuffix(path, "/"+name), body)
		createdPaths[path] = false
		operationDone(zone, operation)
		if zone == "us-central1-a" {
			primaryPath = path
		}
	}
	nativeCalls := []string{}
	deletes := map[string]int{}
	forward := func(req *http.Request) (*http.Response, error) {
		if req.URL.Host != "compute.googleapis.com" || (req.Method != "GET" && req.Method != "DELETE") {
			t.Fatalf("unexpected native runtime API %s %s", req.Method, req.URL)
		}
		if req.Method == "DELETE" {
			if _, owned := createdPaths[req.URL.Path]; !owned {
				t.Fatal("mutation outside test-created resources", req.URL)
			}
			deletes[req.URL.Path]++
			if req.URL.Query().Get("requestId") != googleRequestID("future-reservation-independent-"+last(req.URL.Path)) {
				t.Fatal("native idempotency key lost", req.URL)
			}
		}
		nativeCalls = append(nativeCalls, req.Method+" "+req.URL.Path)
		local := req.Clone(req.Context())
		local.URL.Scheme, local.URL.Host, local.Host = u.Scheme, u.Host, u.Host
		return transport.RoundTrip(local)
	}
	runtime := protocolRuntime(t, forward)
	scan := func(r *Runtime, region string) []contracts.InventoryItem {
		t.Helper()
		batch, err := r.List(ctx, productRequest(r, futureReservationTestType, region))
		if err != nil || !batch.Complete || batch.NextCursor != "" {
			t.Fatal("native aggregate list", batch, err)
		}
		items := []contracts.InventoryItem{}
		for _, item := range batch.Items {
			// A shared loopback server may also host another invocation's fixtures.
			if strings.Contains(item.NativeID, "/steward-fr-"+suffix+"-") {
				items = append(items, item)
			}
		}
		return items
	}
	items := scan(runtime, "project")
	if len(items) != 2 {
		t.Fatal("native aggregate omitted a zone", items)
	}
	for _, item := range items {
		n := item.Normalized
		if item.State != "DRAFTING" || n["state"] != "DRAFTING" || n["planningStatus"] != "DRAFT" || n["requestedInstanceCount"] != "2" || text(n["resourceId"]) == "" || n["resourceId"] != n["id"] || object(n["existingMatchingUsageInfo"])["count"] != "0" {
			t.Fatal("independent native defaults/projection lost", item)
		}
		if object(n["timeWindow"])["endTime"] != start.Add(24*time.Hour).Format(time.RFC3339) || n["autoCreatedReservationsDeleteTime"] != start.Add(time.Hour).Format(time.RFC3339) || n["autoCreatedReservationsDuration"] != nil || n["autoDeleteAutoCreatedReservations"] != nil {
			t.Fatal("server-derived deletion/window timing lost", n)
		}
	}
	regional := scan(runtime, "us-central1")
	if len(regional) != 1 || !strings.HasSuffix(primaryPath, strings.TrimPrefix(regional[0].NativeID, "//compute.googleapis.com")) {
		t.Fatal("native regional selection", regional)
	}
	// Native CANCEL is a fixture-side state change, not a Steward delete action.
	operationDone("us-central1-a", call("POST", primaryPath+"/cancel", map[string]any{}))
	regional = scan(runtime, "us-central1")
	if len(regional) != 1 || regional[0].State != "CANCELLED" || regional[0].Normalized["planningStatus"] != "DRAFT" {
		t.Fatal("cancelled native state not refreshed", regional)
	}
	items = []contracts.InventoryItem{regional[0]}
	other := scan(runtime, "europe-west1")
	if len(other) != 1 {
		t.Fatal("unrelated region changed", other)
	}
	items = append(items, other[0])
	for index, item := range items {
		path := "/compute/v1" + strings.TrimPrefix(item.NativeID, "//compute.googleapis.com")
		value := asset.Asset{ID: asset.AssetID("independent-fr-" + strconv.Itoa(index)), Identity: asset.Identity{Provider: asset.ProviderGCP, ConnectionID: "connection", Partition: "gcp", NativeType: futureReservationTestType, NativeID: item.NativeID}, Normalized: item.Normalized, Capabilities: asset.CapabilitySet{asset.CapabilityActionable}}
		request := contracts.ActionRequest{Action: "delete", Asset: value, IdempotencyKey: "future-reservation-independent-" + last(path)}
		driver, err := runtime.ResolveAction(ctx, "connection", value)
		if err != nil {
			t.Fatal(err)
		}
		before, err := driver.Readback(ctx, request)
		if err != nil || !before.Exists || before.State != item.State {
			t.Fatal("native own readback state", before, err)
		}
		result, err := driver.Execute(ctx, request)
		if err != nil || result.ProviderOperationID == "" {
			t.Fatal("independent native delete", result, err)
		}
		request, result = roundTripDataformJSON(t, request), roundTripDataformJSON(t, result)
		// Restore only serialized review/receipt into a fresh runtime/client.
		restarted := protocolRuntime(t, forward)
		driver, err = restarted.ResolveAction(ctx, "connection", request.Asset)
		if err != nil {
			t.Fatal(err)
		}
		wait, err := driver.Wait(ctx, request, result)
		if err != nil || !wait.Done {
			t.Fatal("native operation/restart/absence", wait, err)
		}
		read, err := driver.Readback(ctx, request)
		if err != nil || read.Exists {
			t.Fatal("native own resource survived", read, err)
		}
		if deletes[path] != 1 {
			t.Fatal("acknowledged native mutation replayed", path, deletes)
		}
		createdPaths[path] = true
		runtime = restarted
		remaining := scan(runtime, "project")
		if len(remaining) != 1-index {
			t.Fatal("independent native reconciliation", remaining)
		}
		if index == 0 && (remaining[0].NativeID != other[0].NativeID || remaining[0].State != "DRAFTING") {
			t.Fatal("unselected native reservation changed", remaining)
		}
	}
	lists, polls, reads := 0, 0, 0
	for _, call := range nativeCalls {
		if strings.Contains(call, "/aggregated/futureReservations") {
			lists++
		} else if strings.Contains(call, "/operations/") {
			polls++
		} else if strings.HasPrefix(call, "GET ") {
			reads++
		}
	}
	if lists < 6 || polls < 2 || reads < 6 || len(deletes) != 2 {
		t.Fatal("missing independent native lifecycle calls", nativeCalls)
	}
	t.Logf("unmodified Google mockcompute: %d forwarded Compute calls (%d aggregate lists, %d operation polls, %d own reads, 2 deletes), %d fixture setup calls; no Compute response substitutions; OAuth/CRM use fixtures", len(nativeCalls), lists, polls, reads, fixtureCalls)
}
