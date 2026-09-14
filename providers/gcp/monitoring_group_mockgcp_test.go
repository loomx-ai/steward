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

func TestMonitoringGroupIndependentMockGCP(t *testing.T) {
	testMonitoringGroupIndependentMockGCP(t, false)
}
func TestMonitoringGroupDeleteIndependentMockGCP(t *testing.T) {
	testMonitoringGroupIndependentMockGCP(t, true)
}
func testMonitoringGroupIndependentMockGCP(t *testing.T, cleanup bool) {
	endpoint := os.Getenv("STEWARD_NOTIFICATION_CHANNEL_MOCKGCP_URL")
	if endpoint == "" {
		t.Skip("set STEWARD_NOTIFICATION_CHANNEL_MOCKGCP_URL to the pinned Monitoring harness")
	}
	origin, err := url.Parse(endpoint)
	if err != nil || origin.Scheme != "http" || origin.Hostname() != "127.0.0.1" || origin.Port() == "" || origin.User != nil || origin.Path != "" || origin.RawQuery != "" || origin.Fragment != "" {
		t.Fatal("mock must use a loopback origin")
	}
	client := &http.Client{Timeout: 10 * time.Second}
	native := func(method, path string, data map[string]any, want int) map[string]any {
		t.Helper()
		var body io.Reader
		if data != nil {
			b, _ := json.Marshal(data)
			body = bytes.NewReader(b)
		}
		req, err := http.NewRequest(method, endpoint+path, body)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, err := io.ReadAll(res.Body)
		if err != nil || res.StatusCode != want {
			t.Fatal(method, path, res.StatusCode, err)
		}
		var result map[string]any
		if err := json.Unmarshal(b, &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	seed := monitoringGroupFixture()
	delete(seed, "name")
	delete(seed, "futureField")
	current := native("POST", "/v3/projects/sample-project/groups", seed, 200)
	name := text(current["name"])
	if !strings.HasPrefix(name, "projects/sample-project/groups/") {
		t.Fatal(current)
	}
	exists := true
	t.Cleanup(func() {
		if exists {
			native("DELETE", "/v3/"+name+"?recursive=false", nil, 200)
		}
	})
	// Upstream implements Get/Create/Update/Delete, but neither list endpoint.
	unsupported := native("GET", "/v3/"+name+"/members", nil, 500)
	if object(unsupported["error"])["message"] != "method ListGroupMembers not implemented" {
		t.Fatal(unsupported)
	}
	fixtures, forwarded, deletes := 0, 0, 0
	if cleanup {
		for path, method := range map[string]string{"/v3/projects/sample-project/uptimeCheckConfigs": "ListUptimeCheckConfigs", "/v1/projects/sample-project/dashboards": "ListDashboards"} {
			unsupported := native("GET", path, nil, 500)
			if object(unsupported["error"])["message"] != "method "+method+" not implemented" {
				t.Fatal(unsupported)
			}
		}
	}
	hybrid := false
	r := protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
		if (req.Method != "GET" && !(cleanup && req.Method == "DELETE")) || req.URL.Host != "monitoring.googleapis.com" {
			t.Fatal("runtime group mutation", req.Method, req.URL)
		}
		if hybrid && req.URL.Path == "/v3/projects/sample-project/groups" {
			if req.URL.RawQuery != "pageSize=100" {
				t.Fatal(req.URL)
			}
			fixtures++
			b, _ := json.Marshal(map[string]any{"group": []any{current}})
			return apiResponse(req, 200, string(b)), nil
		}
		if hybrid && req.URL.Path == "/v3/"+name+"/members" {
			q := req.URL.Query()
			if q.Get("pageSize") != "100" || q.Get("filter") != "" || q.Get("interval.startTime") == "" || q.Get("interval.endTime") == "" {
				t.Fatal(req.URL)
			}
			fixtures++
			b, _ := json.Marshal(map[string]any{"members": []any{monitoringGroupMemberFixture()}, "totalSize": 1})
			return apiResponse(req, 200, string(b)), nil
		}
		if cleanup && (req.URL.Path == "/v3/projects/sample-project/uptimeCheckConfigs" || req.URL.Path == "/v1/projects/sample-project/dashboards") {
			if req.Method != "GET" || req.URL.RawQuery != "pageSize=100" {
				t.Fatal(req.Method, req.URL)
			}
			fixtures++
			return apiResponse(req, 200, `{}`), nil
		}
		if req.Method == "DELETE" {
			deletes++
			if req.URL.Path != "/v3/"+name || req.URL.RawQuery != "recursive=false" {
				t.Fatal(req.URL)
			}
			if req.Body != nil {
				body, _ := io.ReadAll(req.Body)
				if len(body) != 0 {
					t.Fatal("native DELETE body")
				}
			}
		}
		forwarded++
		local := req.Clone(req.Context())
		local.URL.Scheme, local.URL.Host, local.Host = origin.Scheme, origin.Host, origin.Host
		return client.Do(local)
	})
	req := productRequest(r, monitoringGroupType, "global")
	if batch, err := r.List(t.Context(), req); err == nil || batch.Complete || len(batch.Items) != 0 {
		t.Fatal("unimplemented native LIST became empty inventory", batch, err)
	}
	invoke, err := r.Invoke(t.Context(), contracts.Invocation{ConnectionID: "connection", Operation: "monitoring.projects.groups.get", Parameters: map[string]any{"name": name}})
	if err != nil || invoke.Data["name"] != name {
		t.Fatal(invoke, err)
	}
	b, _ := json.Marshal(invoke)
	if strings.Contains(string(b), "PRIVATE_GROUP") {
		t.Fatal("native filter escaped")
	}
	hybrid = true
	var previous string
	var observed contracts.InventoryItem
	for _, phase := range []string{"initial", "updated"} {
		if phase == "updated" {
			seed["name"] = name
			seed["displayName"] = "Changed group"
			current = native("PUT", "/v3/"+name, seed, 200)
		}
		batch, err := r.List(t.Context(), req)
		if err != nil || !batch.Complete || len(batch.Items) != 1 {
			t.Fatal(batch, err)
		}
		proof := text(batch.Items[0].Normalized[monitoringGroupReview])
		if proof == "" || phase == "updated" && proof == previous {
			t.Fatal("native update not bound")
		}
		previous = proof
		observed = batch.Items[0]
	}
	if cleanup {
		value := asset.Asset{ID: "native-group", Identity: asset.Identity{ConnectionID: "connection", Provider: asset.ProviderGCP, Partition: "gcp", NativeType: monitoringGroupType, NativeID: observed.NativeID}, Normalized: observed.Normalized}
		request := contracts.ActionRequest{Asset: value, Action: "delete", IdempotencyKey: "native-group-delete"}
		driver, err := r.ResolveAction(t.Context(), "connection", value)
		if err != nil {
			t.Fatal(err)
		}
		result, err := driver.Execute(t.Context(), request)
		if err != nil || deletes != 1 || result.Data["phase"] != "monitoring_group_delete" || result.ProviderOperationID != "" {
			t.Fatal(result, err, deletes)
		}
		exists = false
		encoded, _ := json.Marshal(result)
		restored := contracts.ActionResult{}
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
		if err != nil || !settled.Settled {
			t.Fatal(settled, err)
		}
		settled, err = driver.(contracts.MutationSettlementReader).MutationSettled(t.Context(), request, contracts.ActionResult{})
		if err != nil || settled.Settled {
			t.Fatal("empty receipt released scope", settled, err)
		}
	} else {
		native("DELETE", "/v3/"+name+"?recursive=false", nil, 200)
		exists = false
	}
	batch, err := r.List(t.Context(), req)
	if err == nil || batch.Complete || len(batch.Items) != 0 {
		t.Fatal("listed group's own native 404 became absence", batch, err)
	}
	wantFixtures, wantForwarded := 7, 7
	if cleanup {
		wantFixtures, wantForwarded = 19, 19
	}
	if fixtures != wantFixtures || forwarded != wantForwarded {
		t.Fatal("unexpected hybrid evidence", fixtures, forwarded)
	}
	t.Logf("Native Group GET/update/404 and optional reviewed deletion verified: %d forwarded calls (%d DELETE), %d explicit LIST/member fixtures; no native paging, membership, descendant-guard or IAM claim", forwarded, deletes, fixtures)
}
