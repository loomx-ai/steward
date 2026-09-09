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

// Upstream exposes these methods through v1beta1 only. This explicit version
// alias and GET-backed fixture lists do not replace its GET/DELETE/error logic.
// The mock does not cascade memberships; a surviving native link must block us.
func TestIdentityGroupsIndependentMockGCP(t *testing.T) {
	endpoint := os.Getenv("STEWARD_IDENTITY_MOCKGCP_URL")
	if endpoint == "" {
		t.Skip("set STEWARD_IDENTITY_MOCKGCP_URL to the pinned Google fixture harness")
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" || u.Port() == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		t.Fatal("mockgcp must use a loopback HTTP origin")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	httpClient := &http.Client{Timeout: 10 * time.Second}
	call := func(method, path string, body any) map[string]any {
		t.Helper()
		raw := []byte{}
		if body != nil {
			raw, err = json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
		}
		req, err := http.NewRequestWithContext(ctx, method, endpoint+path, strings.NewReader(string(raw)))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		response, err := httpClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		raw, _ = io.ReadAll(response.Body)
		data := map[string]any{}
		if response.StatusCode != 200 || json.Unmarshal(raw, &data) != nil {
			t.Fatalf("native mock %s %s: %d %s", method, path, response.StatusCode, raw)
		}
		return data
	}
	created := call("POST", "/v1beta1/groups", map[string]any{"parent": "customers/C01234567", "groupKey": map[string]any{"id": "steward-" + strconv.FormatInt(time.Now().UnixNano(), 36) + "@example.test"}, "displayName": "Independent group", "labels": map[string]any{"cloudidentity.googleapis.com/groups.discussion_forum": ""}})
	groupName := text(object(created["response"])["name"])
	if !strings.HasPrefix(groupName, "groups/") {
		t.Fatalf("native create omitted group name: %+v", created)
	}
	// Native alias population is asynchronous in the upstream implementation.
	for attempt := 0; attempt < 100; attempt++ {
		data := call("GET", "/v1beta1/"+groupName, nil)
		if len(array(data["additionalGroupKeys"])) >= 2 {
			break
		}
		if attempt == 99 {
			t.Fatal("native aliases did not settle")
		}
		time.Sleep(10 * time.Millisecond)
	}
	createMember := func() string {
		result := call("POST", "/v1beta1/"+groupName+"/memberships", map[string]any{"preferredMemberKey": map[string]any{"id": "member@example.test"}, "roles": []any{map[string]any{"name": "MEMBER"}}})
		name := text(object(result["response"])["name"])
		if !strings.HasPrefix(name, groupName+"/memberships/") {
			t.Fatalf("native member create omitted name: %+v", result)
		}
		return name
	}
	memberName := createMember()
	listShims, versionAliases, nativeWrites := 0, 0, []string{}
	forward := func(req *http.Request) (*http.Response, error) {
		local := req.Clone(req.Context())
		local.URL.Scheme, local.URL.Host, local.Host = u.Scheme, u.Host, u.Host
		local.URL.Path = strings.Replace(local.URL.Path, "/v1/", "/v1beta1/", 1)
		versionAliases++
		if req.Method == "DELETE" {
			nativeWrites = append(nativeWrites, local.URL.Path)
		}
		return http.DefaultTransport.RoundTrip(local)
	}
	transport := func(req *http.Request) (*http.Response, error) {
		if req.URL.Host != identityHost {
			t.Fatalf("unexpected independent native host %s", req.URL)
		}
		if req.Method == "GET" && (req.URL.Path == "/v1/groups" || req.URL.Path == "/v1/"+groupName+"/memberships") {
			field, name := "groups", groupName
			if strings.HasSuffix(req.URL.Path, "/memberships") {
				field, name = "memberships", memberName
			} else if req.URL.Query().Get("parent") != "customers/C01234567" {
				t.Fatal("wrong directory in native shim")
			}
			if req.URL.Query().Get("view") != "FULL" {
				t.Fatal("BASIC view cannot verify native configuration")
			}
			listShims++
			detail := req.Clone(req.Context())
			detail.URL.Path, detail.URL.RawQuery = "/v1/"+name, ""
			response, err := forward(detail)
			if err != nil {
				return nil, err
			}
			raw, _ := io.ReadAll(response.Body)
			response.Body.Close()
			rows := []any{}
			if response.StatusCode == 200 {
				data := map[string]any{}
				if json.Unmarshal(raw, &data) != nil {
					t.Fatal("native GET not JSON")
				}
				rows = append(rows, data)
			} else if response.StatusCode != 404 && !(field == "groups" && response.StatusCode == 403) {
				t.Fatalf("native GET in list shim: %d %s", response.StatusCode, raw)
			}
			return dataformResponse(req, 200, map[string]any{field: rows}), nil
		}
		return forward(req)
	}
	makeRuntime := func() *Runtime {
		r := protocolRuntime(t, transport)
		source := r.credentials
		r.credentials = credentialFunc(func(ctx context.Context, id asset.ConnectionID) (contracts.Credential, error) {
			c, err := source.Resolve(ctx, id)
			c.Values["identity_group_parent"] = "customers/C01234567"
			return c, err
		})
		return r
	}
	inventoryValue := func(r *Runtime, kind string) asset.Asset {
		t.Helper()
		batch, err := r.List(ctx, identityInventoryRequest(r, kind))
		if err != nil || len(batch.Items) != 1 || !batch.Complete {
			t.Fatalf("native identity inventory: %+v %v", batch, err)
		}
		item := batch.Items[0]
		return asset.Asset{ID: asset.AssetID(item.NativeID), Identity: asset.Identity{Provider: asset.ProviderGCP, ConnectionID: "connection", Partition: "gcp", NativeType: kind, NativeID: item.NativeID}, Normalized: item.Normalized, Capabilities: asset.CapabilitySet{asset.CapabilityActionable}}
	}
	r := makeRuntime()
	member := inventoryValue(r, identityMemberType)
	request := contracts.ActionRequest{Asset: member, Action: "delete", IdempotencyKey: "native-member"}
	driver, err := r.ResolveAction(ctx, "connection", member)
	if err != nil {
		t.Fatal(err)
	}
	result, err := driver.Execute(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	wait, err := driver.Wait(ctx, request, roundTripDataformJSON(t, result))
	if err != nil || !wait.Done {
		t.Fatalf("native membership unlink: %+v %v", wait, err)
	}
	memberName = createMember()
	r = makeRuntime()
	parent := inventoryValue(r, identityGroupType)
	member = inventoryValue(r, identityMemberType)
	request = contracts.ActionRequest{Asset: parent, Action: "delete", IdempotencyKey: "native-group", LifecycleImpacts: []contracts.ActionImpact{{Asset: member, ControllerID: parent.ID, Delete: true}}}
	driver, err = r.ResolveAction(ctx, "connection", parent)
	if err != nil {
		t.Fatal(err)
	}
	result, err = driver.Execute(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	restarted := makeRuntime()
	driver, err = restarted.ResolveAction(ctx, "connection", parent)
	if err != nil {
		t.Fatal(err)
	}
	result = roundTripDataformJSON(t, result)
	wait, err = driver.Wait(ctx, request, result)
	if err != nil || wait.Done || wait.State != "memberships_deleting" {
		t.Fatalf("upstream non-cascade survivor was hidden: %+v %v", wait, err)
	}
	// Explicit native fixture cleanup: the upstream group delete does not remove
	// this record. No adapter is allowed to invent its absence or native cascade.
	call("DELETE", "/v1beta1/"+memberName, nil)
	request.ExecutionResult = &result
	read, err := driver.Readback(ctx, request)
	if err != nil || read.Exists || read.State != "delete_confirmed" {
		t.Fatalf("native group 403 plus completed receipt: %+v %v", read, err)
	}
	if len(nativeWrites) != 2 || nativeWrites[1] != "/v1beta1/"+groupName {
		t.Fatalf("native mutation replay or wrong target: %v", nativeWrites)
	}
	t.Logf("native v1beta1 aliases=%d GET-backed lists=%d adapter DELETEs=%d; upstream lingering link explicitly detected", versionAliases, listShims, len(nativeWrites))
}
