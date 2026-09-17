package azure

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

// Opt-in independent ARM emulator test. The adapter supplies only OAuth,
// subscription/resource-group metadata and locks outside the emulator's scope.
// Every Microsoft.ApiManagement request reaches the unmodified local server.
func TestAPIMIndependentEmulator(t *testing.T) {
	endpoint := os.Getenv("STEWARD_APIM_EMULATOR_URL")
	if endpoint == "" {
		t.Skip("set STEWARD_APIM_EMULATOR_URL to the pinned loopback emulator")
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "http" || net.ParseIP(u.Hostname()) == nil || !net.ParseIP(u.Hostname()).IsLoopback() || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" {
		t.Fatal("emulator endpoint must be a bare loopback HTTP origin")
	}
	transport := &http.Transport{Proxy: nil}
	t.Cleanup(transport.CloseIdleConnections)
	client := &http.Client{Transport: transport, Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	group := "/subscriptions/" + testSubscription + "/resourcegroups/steward-apim-emulator"
	root := group + "/providers/microsoft.apimanagement/service/steward-" + fmt.Sprint(time.Now().UnixNano())
	call := func(method, path string, body any) (int, map[string]any) {
		t.Helper()
		var data []byte
		if body != nil {
			data, _ = json.Marshal(body)
		}
		req, _ := http.NewRequest(method, endpoint+path+"?api-version="+apimVersion, bytes.NewReader(data))
		req.Host = "management.azure.localhost"
		req.Header.Set("Content-Type", "application/json")
		res, err := client.Do(req)
		if err != nil {
			t.Fatal("emulator request", err)
		}
		defer res.Body.Close()
		payload, err := io.ReadAll(res.Body)
		if err != nil {
			t.Fatal(err)
		}
		var result map[string]any
		if len(payload) != 0 && json.Unmarshal(payload, &result) != nil {
			t.Fatal("non-JSON emulator response", res.StatusCode)
		}
		return res.StatusCode, result
	}
	status, _ := call("PUT", root, map[string]any{"location": "westus", "sku": map[string]any{"name": "Premium", "capacity": 1}, "properties": map[string]any{"publisherName": "Steward emulator fixture", "publisherEmail": "fixture@example.test"}})
	if status != 201 && status != 200 {
		t.Fatal("create isolated APIM service", status)
	}
	t.Cleanup(func() {
		status, _ := call("DELETE", root, nil)
		if status != 204 && status != 200 && status != 202 {
			t.Errorf("emulator fixture cleanup returned %d", status)
		}
	})
	for _, resource := range []struct {
		path  string
		props map[string]any
	}{
		{"/apis/customers", map[string]any{"displayName": "Customers", "path": "customers", "protocols": []string{"https"}, "serviceUrl": "https://backend.example.test", "apiRevision": "1"}},
		{"/apis/customers;rev=2", map[string]any{"displayName": "Customers", "path": "customers", "protocols": []string{"https"}, "serviceUrl": "https://backend.example.test", "apiRevision": "2"}},
		{"/namedValues/header-value", map[string]any{"displayName": "HeaderValue", "secret": true, "value": "fixture-private-value"}},
		{"/subscriptions/client", map[string]any{"displayName": "Client", "scope": root + "/apis", "state": "active"}},
	} {
		status, _ := call("PUT", root+resource.path, map[string]any{"properties": resource.props})
		if status != 201 && status != 200 {
			t.Fatal("create emulator fixture", resource.path, status)
		}
	}
	deletes := 0
	r := protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
		path := strings.ToLower(req.URL.Path)
		if req.Method == "GET" {
			switch path {
			case "/subscriptions/" + testSubscription + "/providers/microsoft.authorization/locks":
				return jsonResponse(200, map[string]any{"value": []any{}}, nil), nil
			case "/subscriptions/" + testSubscription + "/resourcegroups":
				return jsonResponse(200, map[string]any{"value": []any{map[string]any{"id": group, "name": last(group), "type": groupType, "location": "centralus"}}}, nil), nil
			case group:
				return jsonResponse(200, map[string]any{"id": group, "name": last(group), "type": groupType, "location": "centralus"}, nil), nil
			case "/subscriptions/" + testSubscription + "/resources":
				// The RBAC scope index is a read-only ARM listing; the emulator
				// owns no resources outside its own APIM service.
				return jsonResponse(200, map[string]any{"value": []any{}}, nil), nil
			}
		}
		// Monitor, RBAC and diagnostic dependency indexes are read-only ARM
		// reads outside the emulator's scope; they use empty native fixtures.
		if response, handled := emptyMonitorIndexResponse(t, req); handled {
			return response, nil
		}
		if !strings.Contains(path, "/providers/microsoft.apimanagement/") {
			t.Fatalf("unexpected non-APIM emulator adapter request %s %s", req.Method, path)
		}
		if req.Method == "DELETE" {
			deletes++
			if req.Header.Get("If-Match") == "" || req.Header.Get("If-Match") == "*" {
				t.Fatal("emulator action lost native conditional header")
			}
		}
		copy := req.Clone(req.Context())
		copy.URL.Scheme, copy.URL.Host, copy.Host = u.Scheme, u.Host, "management.azure.localhost"
		copy.Header.Del("Authorization")
		return transport.RoundTrip(copy)
	})
	var subscription, named asset.Asset
	for _, kind := range []string{apimAPIType, apimServiceType + "/namedValues", apimServiceType + "/subscriptions"} {
		var items []contracts.InventoryItem
		request := productRequest(r, kind)
		for page := 0; ; page++ {
			batch, err := r.List(context.Background(), request)
			if err != nil || page > 100 {
				t.Fatal("independent APIM inventory", kind, err)
			}
			items = append(items, batch.Items...)
			if batch.Complete {
				break
			}
			if batch.NextCursor == "" || batch.NextCursor == request.Cursor {
				t.Fatal("emulator inventory cursor did not advance")
			}
			request.Cursor = batch.NextCursor
		}
		found := 0
		for _, item := range items {
			if !strings.HasPrefix(item.NativeID, root+"/") {
				continue
			}
			found++
			if item.Location != "westus" || item.Normalized["arm_etag"] == "" {
				t.Fatal("emulator native location or ETag missing", kind)
			}
			value := asset.Asset{ID: asset.AssetID(item.NativeID), Identity: asset.Identity{Provider: asset.ProviderAzure, ConnectionID: "connection", Partition: "azure", NativeType: item.NativeType, NativeID: item.NativeID}, Location: item.Location, Capabilities: asset.CapabilitySet{asset.CapabilityActionable}, Normalized: item.Normalized}
			if strings.HasSuffix(kind, "/subscriptions") {
				subscription = value
			}
			if strings.HasSuffix(kind, "/namedValues") {
				named = value
			}
		}
		want := 1
		if kind == apimAPIType {
			want = 2
		}
		if found != want {
			t.Fatal("independent emulator hid resources", kind, found)
		}
	}
	request := contracts.ActionRequest{Asset: subscription, Action: "delete"}
	driver, err := r.ResolveAction(context.Background(), "connection", subscription)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := driver.Execute(context.Background(), request)
	if err != nil {
		t.Fatal("independent subscription deletion", err)
	}
	waited, err := driver.Wait(context.Background(), request, receipt)
	if err != nil || !waited.Done || deletes != 1 {
		t.Fatal("independent subscription readback", waited, err)
	}
	// The pinned emulator has no policy collection API. This must remain a
	// failed dependency read, never an invented empty collection or success.
	driver, err = r.ResolveAction(context.Background(), "connection", named)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := driver.Execute(context.Background(), contracts.ActionRequest{Asset: named, Action: "delete"}); err == nil || deletes != 1 {
		t.Fatal("unsupported emulator collection authorized deletion")
	}
	status, _ = call("GET", root+"/namedValues/header-value", nil)
	if status != 200 {
		t.Fatal("protected emulator named value disappeared", status)
	}
}
