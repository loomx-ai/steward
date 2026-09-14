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

// Table responses come only from Google's pinned bttest via its REST/gRPC
// bridge. Instance discovery, OAuth and CRM are explicitly synthetic fixtures.
func TestBigtableIndependentEmulator(t *testing.T) {
	endpoint := os.Getenv("STEWARD_BIGTABLE_EMULATOR_URL")
	if endpoint == "" {
		t.Skip("set STEWARD_BIGTABLE_EMULATOR_URL to the pinned bttest bridge")
	}
	origin, err := url.Parse(endpoint)
	if err != nil || origin.Scheme != "http" || origin.Hostname() != "127.0.0.1" || origin.Port() == "" || origin.User != nil || origin.Path != "" || origin.RawQuery != "" || origin.Fragment != "" {
		t.Fatal("emulator must use a loopback HTTP origin")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	transport := &http.Transport{Proxy: nil}
	t.Cleanup(transport.CloseIdleConnections)
	local := &http.Client{Transport: transport, Timeout: 10 * time.Second}
	call := func(ctx context.Context, method, path string, body any) (int, map[string]any, error) {
		var raw []byte
		if body != nil {
			var err error
			raw, err = json.Marshal(body)
			if err != nil {
				return 0, nil, err
			}
		}
		req, err := http.NewRequestWithContext(ctx, method, endpoint+path, strings.NewReader(string(raw)))
		if err != nil {
			return 0, nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		res, err := local.Do(req)
		if err != nil {
			return 0, nil, err
		}
		defer res.Body.Close()
		data, err := io.ReadAll(res.Body)
		if err != nil {
			return res.StatusCode, nil, err
		}
		var decoded map[string]any
		err = json.Unmarshal(data, &decoded)
		return res.StatusCode, decoded, err
	}
	mustCall := func(method, path string, body any) map[string]any {
		t.Helper()
		code, data, err := call(ctx, method, path, body)
		if err != nil || code != 200 {
			t.Fatalf("fixture %s %s: %d %v %v", method, path, code, data, err)
		}
		return data
	}
	suffix := strconv.FormatInt(time.Now().UnixNano(), 36)
	parents := []string{"projects/sample-project/instances/steward-" + suffix + "-a", "projects/sample-project/instances/steward-" + suffix + "-b"}
	owned := map[string]bool{}
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		for path, deleted := range owned {
			if deleted {
				continue
			}
			// Only fixture-created tables are eligible. Unprotect through the native
			// fixture API, then remove; production cleanup never toggles this flag.
			code, _, err := call(cleanup, "PATCH", path+"?updateMask=deletionProtection", map[string]any{"deletionProtection": false})
			if err != nil || code != 200 && code != 404 {
				t.Errorf("fixture unprotect: %d %v", code, err)
				continue
			}
			code, _, err = call(cleanup, "DELETE", path, nil)
			if err != nil || code != 200 && code != 404 {
				t.Errorf("fixture cleanup: %d %v", code, err)
			}
		}
	})
	protectedPath := ""
	for index, parent := range parents {
		tables := []string{"events"}
		if index == 0 {
			tables = append(tables, "protected")
		}
		for _, id := range tables {
			path := "/v2/" + parent + "/tables/" + id
			data := mustCall("POST", "/v2/"+parent+"/tables", map[string]any{"tableId": id, "table": map[string]any{"granularity": "MILLIS", "deletionProtection": id == "protected", "columnFamilies": map[string]any{"events": map[string]any{"gcRule": map[string]any{"maxNumVersions": 2}}}}})
			owned[path] = false
			if data["name"] != parent+"/tables/"+id {
				t.Fatal("emulator returned wrong created table", data)
			}
			if id == "protected" {
				protectedPath = path
			}
		}
	}
	// Confirm the independent server itself enforces table protection.
	code, _, err := call(ctx, "DELETE", protectedPath, nil)
	if err != nil || code != 400 {
		t.Fatal("bttest did not reject protected native delete", code, err)
	}
	nativeCalls, deletes, parentCalls := 0, map[string]int{}, 0
	forward := func(req *http.Request) (*http.Response, error) {
		if req.URL.Host != bigtableTestHost || (req.Method != "GET" && req.Method != "DELETE") {
			t.Fatalf("unexpected provider request %s %s", req.Method, req.URL)
		}
		if req.URL.Path == "/v2/projects/sample-project/instances" {
			parentCalls++
			return dataformResponse(req, 200, map[string]any{"instances": []any{map[string]any{"name": parents[0]}, map[string]any{"name": parents[1]}}}), nil
		}
		collection := false
		for _, parent := range parents {
			collection = collection || req.URL.Path == "/v2/"+parent+"/tables"
		}
		if !collection {
			if _, ok := owned[req.URL.Path]; !ok {
				t.Fatal("request outside test-created tables", req.URL)
			}
		}
		if req.Method == "GET" {
			expected := "FULL"
			if collection {
				expected = "REPLICATION_VIEW"
			}
			if req.URL.Query().Get("view") != expected {
				t.Fatal("incorrect native view", req.URL)
			}
		} else {
			if collection || req.URL.RawQuery != "" {
				t.Fatal("invalid table mutation", req.URL)
			}
			deletes[req.URL.Path]++
		}
		nativeCalls++
		forwarded := req.Clone(req.Context())
		forwarded.URL.Scheme, forwarded.URL.Host, forwarded.Host = origin.Scheme, origin.Host, origin.Host
		return transport.RoundTrip(forwarded)
	}
	runtime := protocolRuntime(t, forward)
	scan := func(r *Runtime) []contracts.InventoryItem {
		t.Helper()
		request := productRequest(r, bigtableTestKind, "global")
		items := []contracts.InventoryItem{}
		for page := 0; ; page++ {
			if page == 10 {
				t.Fatal("inventory did not finish")
			}
			batch, err := r.List(ctx, request)
			if err != nil {
				t.Fatal(err)
			}
			items = append(items, batch.Items...)
			if batch.Complete {
				break
			}
			request.Cursor = batch.NextCursor
		}
		return items
	}
	items := scan(runtime)
	if len(items) != 3 {
		t.Fatal("native table inventory missing", items)
	}
	seen := map[string]bool{}
	for _, item := range items {
		path := "/v2/" + strings.TrimPrefix(item.NativeID, "//"+bigtableTestHost+"/")
		if _, ok := owned[path]; !ok || seen[path] {
			t.Fatal("same-name identity collision", item.NativeID)
		}
		seen[path] = true
		if item.Normalized["granularity"] != "MILLIS" || object(object(object(item.Normalized["columnFamilies"])["events"])["gcRule"])["maxNumVersions"] != float64(2) {
			t.Fatal("independent native schema lost", item.Normalized)
		}
		value := asset.Asset{ID: asset.AssetID(last(path) + "-" + strconv.Itoa(len(seen))), Identity: asset.Identity{Provider: asset.ProviderGCP, ConnectionID: "connection", Partition: "gcp", NativeType: bigtableTestKind, NativeID: item.NativeID}, Normalized: item.Normalized}
		assertGCPPropertyQuery(t, runtime, []asset.Asset{value}, bigtableTestKind, item.NativeID, `properties.granularity = "MILLIS"`)
		driver, err := runtime.ResolveAction(ctx, "connection", value)
		if err != nil {
			t.Fatal(err)
		}
		request := contracts.ActionRequest{Asset: value, Action: "delete"}
		if path == protectedPath {
			assertGCPPropertyQuery(t, runtime, []asset.Asset{value}, bigtableTestKind, item.NativeID, `properties.deletionProtection = true`)
			check, err := driver.Preflight(ctx, request)
			if err != nil || check.Allowed || check.Reason != "deletion_protection_enabled" {
				t.Fatal("provider did not honor native protection", check, err)
			}
			if _, err := driver.Execute(ctx, request); err == nil || deletes[path] != 0 {
				t.Fatal("provider sent protected delete", deletes, err)
			}
			mustCall("PATCH", path+"?updateMask=deletionProtection", map[string]any{"deletionProtection": false})
		}
		result, err := driver.Execute(ctx, request)
		if err != nil || deletes[path] != 1 {
			t.Fatal("native deletion failed", result, err, deletes)
		}
		owned[path] = true
		request, result = roundTripDataformJSON(t, request), roundTripDataformJSON(t, result)
		restarted := protocolRuntime(t, forward)
		driver, err = restarted.ResolveAction(ctx, "connection", request.Asset)
		if err != nil {
			t.Fatal(err)
		}
		wait, err := driver.Wait(ctx, request, result)
		if err != nil || !wait.Done {
			t.Fatal("restart lost native absence confirmation", wait, err)
		}
		if _, err := driver.Execute(ctx, request); err != nil || deletes[path] != 1 {
			t.Fatal("already absent table deleted twice", deletes, err)
		}
	}
	if items := scan(protocolRuntime(t, forward)); len(items) != 0 {
		t.Fatal("deleted table returned in native list", items)
	}
	if parentCalls == 0 || nativeCalls < 15 {
		t.Fatal("insufficient independent native calls", nativeCalls, parentCalls)
	}
	t.Logf("independent table calls=%d, synthetic parent lists=%d, created/deleted tables=%d", nativeCalls, parentCalls, len(owned))
}
