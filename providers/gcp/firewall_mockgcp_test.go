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

// The upstream Google mock has hierarchical policy GET/INSERT/DELETE and native
// organization LROs. It does not implement LIST or association operations. Only
// a GET-wrapped singleton list and the fixture's explicit CRM root are supplied
// here. Native details, DELETE, LRO status and absence remain unmodified upstream.
func TestFirewallIndependentMockGCP(t *testing.T) {
	endpoint := os.Getenv("STEWARD_FIREWALL_MOCKGCP_URL")
	if endpoint == "" {
		t.Skip("set STEWARD_FIREWALL_MOCKGCP_URL to the Compute fixture harness")
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" || u.Port() == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		t.Fatal("mockgcp must use a local loopback HTTP origin")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	localClient := &http.Client{Timeout: 10 * time.Second}
	call := func(method, path, body string) map[string]any {
		t.Helper()
		req, err := http.NewRequestWithContext(ctx, method, endpoint+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		response, err := localClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		raw, _ := io.ReadAll(response.Body)
		var data map[string]any
		if response.StatusCode != 200 || json.Unmarshal(raw, &data) != nil {
			t.Fatalf("mockgcp %s %s: %d %s", method, path, response.StatusCode, raw)
		}
		return data
	}
	shortName := "steward-policy-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	create := call("POST", "/compute/v1/locations/global/firewallPolicies?parentId=organizations%2F123", `{"shortName":"`+shortName+`","description":"Independent native firewall fixture"}`)
	operation := text(create["name"])
	if !segmentPattern.MatchString(operation) {
		t.Fatal("native create omitted operation name")
	}
	created := call("GET", "/compute/v1/locations/global/operations/"+operation, "")
	if created["status"] != "DONE" {
		t.Fatal("mock create operation not finished")
	}
	name := strings.TrimPrefix(text(created["targetLink"]), "https://www.googleapis.com/compute/v1/")
	if !strings.HasPrefix(name, "locations/global/firewallPolicies/") || !firewallNumericID(last(name)) {
		t.Fatal("native create omitted policy numeric identity")
	}
	realCalls := []string{}
	listShims, crmShims := 0, 0
	forward := func(req *http.Request) (*http.Response, error) {
		realCalls = append(realCalls, req.Method+" "+req.URL.Path)
		local := req.Clone(req.Context())
		local.URL.Scheme, local.URL.Host, local.Host = u.Scheme, u.Host, u.Host
		return http.DefaultTransport.RoundTrip(local)
	}
	transport := func(req *http.Request) (*http.Response, error) {
		if req.URL.Host == "cloudresourcemanager.googleapis.com" && req.Method == "GET" {
			crmShims++
			switch req.URL.Path {
			case "/v3/organizations/123":
				return dataformResponse(req, 200, map[string]any{"name": "organizations/123", "state": "ACTIVE", "createTime": "2026-01-01T00:00:00Z", "displayName": "Fixture organization"}), nil
			case "/v3/folders":
				if req.URL.Query().Get("parent") != "organizations/123" {
					t.Fatal("unexpected fixture folder parent")
				}
				return dataformResponse(req, 200, map[string]any{"folders": []any{}}), nil
			}
		}
		if req.URL.Host != "compute.googleapis.com" {
			t.Fatalf("unexpected service %s", req.URL)
		}
		if req.Method == "GET" && req.URL.Path == "/compute/v1/locations/global/firewallPolicies" {
			if req.URL.Query().Get("parentId") != "organizations/123" {
				t.Fatal("unexpected policy list parent")
			}
			listShims++
			detail := req.Clone(req.Context())
			detail.URL.Path, detail.URL.RawQuery = "/compute/v1/"+name, ""
			response, err := forward(detail)
			if err != nil {
				return nil, err
			}
			raw, _ := io.ReadAll(response.Body)
			response.Body.Close()
			rows := []any{}
			if response.StatusCode == 200 {
				var data map[string]any
				if err := json.Unmarshal(raw, &data); err != nil {
					t.Fatal(err)
				}
				rows = append(rows, data)
			} else if response.StatusCode != 404 {
				t.Fatalf("native policy GET in list shim: %d %s", response.StatusCode, raw)
			}
			return dataformResponse(req, 200, map[string]any{"items": rows}), nil
		}
		if req.Method == "POST" || strings.Contains(req.URL.Path, "Association") {
			t.Fatal("independent mock does not implement associations")
		}
		return forward(req)
	}
	makeRuntime := func() *Runtime {
		r := protocolRuntime(t, transport)
		credentials := r.credentials
		r.credentials = credentialFunc(func(ctx context.Context, id asset.ConnectionID) (contracts.Credential, error) {
			value, err := credentials.Resolve(ctx, id)
			value.Values["firewall_policy_parent"] = "organizations/123"
			return value, err
		})
		return r
	}
	r := makeRuntime()
	inventoryRequest := productRequest(r, firewallPolicyType, "global")
	inventoryRequest.Source = firewallInventorySource
	batch, err := r.List(ctx, inventoryRequest)
	if err != nil || !batch.Complete || len(batch.Items) != 1 {
		t.Fatalf("native mock firewall inventory: %+v %v", batch, err)
	}
	item := batch.Items[0]
	value := asset.Asset{ID: "independent-firewall", Identity: asset.Identity{Provider: asset.ProviderGCP, ConnectionID: "connection", Partition: "gcp", NativeType: firewallPolicyType, NativeID: item.NativeID}, Normalized: item.Normalized, Capabilities: asset.CapabilitySet{asset.CapabilityActionable}}
	request := contracts.ActionRequest{Action: "delete", Asset: value, IdempotencyKey: "independent-native-firewall"}
	driver, err := r.ResolveAction(ctx, "connection", value)
	if err != nil {
		t.Fatal(err)
	}
	result, err := driver.Execute(ctx, request)
	if err != nil || result.ProviderOperationID == "" {
		t.Fatalf("native mock policy DELETE: %+v %v", result, err)
	}
	result, request = roundTripDataformJSON(t, result), roundTripDataformJSON(t, request)
	restarted := makeRuntime()
	driver, err = restarted.ResolveAction(ctx, "connection", request.Asset)
	if err != nil {
		t.Fatal(err)
	}
	if wait, err := driver.Wait(ctx, request, result); err != nil || !wait.Done {
		t.Fatalf("native mock policy operation and absence: %+v %v", wait, err)
	}
	if read, err := driver.Readback(ctx, request); err != nil || read.Exists {
		t.Fatalf("native mock policy readback: %+v %v", read, err)
	}
	deletes := 0
	for _, call := range realCalls {
		if strings.HasPrefix(call, "DELETE ") {
			deletes++
		}
	}
	if deletes != 1 || listShims != 2 || crmShims == 0 {
		t.Fatal("missing native mutation or explicit scope/list shims", realCalls, listShims, crmShims)
	}
	t.Logf("Google mockcompute GET/DELETE, global organization LRO, JSON restart and GET absence passed (%d native calls, %d GET-wrapped list shims, %d CRM fixture reads); association removal is not implemented upstream", len(realCalls), listShims, crmShims)
}
