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

// Google mockconfig implements group GET/DELETE and the native operation reader,
// but not location/group/revision LIST or deprovision. The shims below supply the
// fixture location, wrap its GET in a singleton list and provide the empty
// revision set of a never-provisioned group. They do not replace native detail,
// mutation, operation or absence logic.
func TestDeploymentGroupIndependentMockGCP(t *testing.T) {
	endpoint := os.Getenv("STEWARD_DEPLOYMENT_GROUP_MOCKGCP_URL")
	if endpoint == "" {
		t.Skip("set STEWARD_DEPLOYMENT_GROUP_MOCKGCP_URL to the Config fixture harness")
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" || u.Port() == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		t.Fatal("mockgcp must use a local loopback HTTP origin")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	client := &http.Client{Timeout: 10 * time.Second}
	id := "steward-empty-group-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	name := infraTestParent + "/deploymentGroups/" + id
	seed, err := http.NewRequestWithContext(ctx, "POST", endpoint+"/v1/"+infraTestParent+"/deploymentGroups?deploymentGroupId="+id, strings.NewReader(`{"labels":{"test":"independent"},"annotations":{"private":"GROUP_PRIVATE_SEED"},"deploymentUnits":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	seed.Header.Set("Content-Type", "application/json")
	response, err := client.Do(seed)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatalf("native mockconfig create: %d %s", response.StatusCode, body)
	}
	realCalls, listShims := []string{}, 0
	forward := func(req *http.Request) (*http.Response, error) {
		realCalls = append(realCalls, req.Method+" "+req.URL.Path)
		local := req.Clone(req.Context())
		local.URL.Scheme, local.URL.Host, local.Host = u.Scheme, u.Host, u.Host
		return http.DefaultTransport.RoundTrip(local)
	}
	r := protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
		if req.URL.Host != "config.googleapis.com" {
			t.Fatalf("unexpected service: %s", req.URL)
		}
		if req.Method == "GET" && req.URL.Path == "/v1/projects/sample-project/locations" {
			listShims++
			return dataformResponse(req, 200, map[string]any{"locations": []any{map[string]any{"name": infraTestParent, "locationId": "us-central1"}}}), nil
		}
		if req.Method == "GET" && req.URL.Path == "/v1/"+infraTestParent+"/deploymentGroups" {
			listShims++
			detail := req.Clone(req.Context())
			detail.URL.Path, detail.URL.RawQuery = "/v1/"+name, ""
			response, err := forward(detail)
			if err != nil {
				return nil, err
			}
			body, _ := io.ReadAll(response.Body)
			response.Body.Close()
			rows := []any{}
			if response.StatusCode == 200 {
				var data map[string]any
				if err := json.Unmarshal(body, &data); err != nil {
					t.Fatal(err)
				}
				rows = append(rows, data)
			} else if response.StatusCode != 404 {
				t.Fatalf("native group GET in list shim: %d %s", response.StatusCode, body)
			}
			return dataformResponse(req, 200, map[string]any{"deploymentGroups": rows}), nil
		}
		if req.Method == "GET" && req.URL.Path == "/v1/"+name+"/revisions" {
			listShims++
			return dataformResponse(req, 200, map[string]any{"deploymentGroupRevisions": []any{}}), nil
		}
		if req.Method == "POST" {
			t.Fatal("never-provisioned mock group must not invoke unsupported deprovision")
		}
		return forward(req)
	})
	batch, err := r.List(ctx, productRequest(r, infraGroup, "us-central1"))
	if err != nil || !batch.Complete || len(batch.Items) != 1 {
		t.Fatalf("mockconfig group inventory: %+v %v; native calls=%v; list shims=%d", batch, err, realCalls, listShims)
	}
	item := batch.Items[0]
	root := asset.Asset{ID: "independent-group", Identity: asset.Identity{Provider: asset.ProviderGCP, ConnectionID: "connection", Partition: "google-cloud", NativeType: infraGroup, NativeID: item.NativeID}, Normalized: item.Normalized, Capabilities: asset.CapabilitySet{asset.CapabilityActionable}, Location: item.Location}
	request := contracts.ActionRequest{Action: "delete", Asset: root, IdempotencyKey: "independent-config-group"}
	driver, err := r.ResolveAction(ctx, "connection", root)
	if err != nil {
		t.Fatal(err)
	}
	result, err := driver.Execute(ctx, request)
	if err != nil || result.ProviderOperationID == "" {
		t.Fatalf("mockconfig native DELETE: %+v %v", result, err)
	}
	encoded, _ := json.Marshal(result)
	if err := json.Unmarshal(encoded, &result); err != nil {
		t.Fatal(err)
	}
	driver, err = r.ResolveAction(ctx, "connection", root)
	if err != nil {
		t.Fatal(err)
	}
	for attempts := 0; ; attempts++ {
		wait, err := driver.Wait(ctx, request, result)
		if err != nil {
			t.Fatalf("mockconfig native operation/readback: %+v %v", wait, err)
		}
		if wait.Done {
			break
		}
		if attempts >= 40 {
			t.Fatal("mockconfig operation did not settle")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if read, err := driver.Readback(ctx, request); err != nil || read.Exists {
		t.Fatalf("mockconfig native absence: %+v %v", read, err)
	}
	deletes, operations := 0, 0
	for _, call := range realCalls {
		if strings.HasPrefix(call, "DELETE ") {
			deletes++
		}
		if strings.Contains(call, "/operations/") {
			operations++
		}
	}
	if deletes != 1 || operations == 0 || listShims == 0 {
		t.Fatalf("independent native calls: %v; %d list shims", realCalls, listShims)
	}
	t.Logf("Google mockconfig GET, native DELETE, typed LRO, serialized resume and GET absence passed (%d native calls, %d explicit list shims); deprovision is not implemented upstream", len(realCalls), listShims)
}
