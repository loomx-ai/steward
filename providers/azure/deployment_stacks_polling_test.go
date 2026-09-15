package azure

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
)

func TestDeploymentStackRecordedDeletePolling(t *testing.T) {
	wire, err := os.ReadFile("fixtures/deployment-stacks/delete-recordings.json")
	if err != nil || fmt.Sprintf("%x", sha256.Sum256(wire)) != "2c5ba0d3fe9a1240b6c2ae7886ce9ee98c6d5bef27b8903822e6920848152517" {
		t.Fatal("recording extraction changed", err)
	}
	type interaction struct {
		Index             int
		Method, URL, Body string
		Status            int
		Headers           map[string][]string
	}
	var fixture struct {
		Ref        string `json:"source_ref"`
		Version    string `json:"api_version"`
		Recordings []struct {
			URI      string `json:"source_uri"`
			SHA      string `json:"source_sha256"`
			Scope    string
			Episodes [][]interaction
		}
	}
	if json.Unmarshal(wire, &fixture) != nil || fixture.Ref != "ea185727729efc032ad9d4eef9ec355ee74ebaae" || fixture.Version != deploymentStackVersion || len(fixture.Recordings) != 3 {
		t.Fatal("recording manifest")
	}
	expected := map[string]string{"subscription": "d4d8a606d97f862a5b3300dcb4b82f8bf3ad2f1469aeff13dea5811214636957", "resource_group": "865cee143cc019e3f92d257d6d91f8b06d41b27580d0105776c415f4177ceb8f", "management_group": "7a0dbf0c4f65a162fb51976db85f71110ed4649bb1ecc1e958189469dd7e2dc1"}
	episodes, polls := 0, 0
	for _, recording := range fixture.Recordings {
		if recording.SHA != expected[recording.Scope] || !strings.Contains(recording.URI, "/"+fixture.Ref+"/") {
			t.Fatal("source fingerprint")
		}
		for _, episode := range recording.Episodes {
			episodes++
			deletion := episode[0]
			owner, err := url.Parse(deletion.URL)
			if err != nil {
				t.Fatal(err)
			}
			headers := http.Header{}
			for key, values := range deletion.Headers {
				for _, value := range values {
					headers.Add(key, value)
				}
			}
			callback := headers.Get("Azure-AsyncOperation")
			if deletion.Status != 202 || deletion.Body != "" || callback == "" || headers.Get("Location") != "" {
				t.Fatal("native deletion receipt")
			}
			endpoint, _ := url.Parse(callback)
			parts := strings.Split(endpoint.Path, "/locations/")
			region := strings.Split(parts[1], "/")[0]
			if _, err := deploymentStackPollURL(owner.Path, region, callback, "status_url", owner.Query()); err != nil {
				t.Fatal(recording.Scope, deletion.Index, err)
			}
			for index, poll := range episode[1:] {
				polls++
				if poll.Method != "GET" || poll.URL != callback {
					t.Fatal("recorded polling selector changed")
				}
				data := map[string]any{}
				if json.Unmarshal([]byte(poll.Body), &data) != nil {
					t.Fatal("native poll body")
				}
				done, err := deploymentStackPollState(callback, response{status: poll.Status, data: data, header: http.Header{}})
				if err != nil || done != (index == len(episode)-2) {
					t.Fatal(recording.Scope, poll.Index, done, err)
				}
			}
		}
	}
	if episodes != 7 || polls != 48 {
		t.Fatal("incomplete native evidence", episodes, polls)
	}
}

func stackPollFixture() (string, string, url.Values) {
	id := "/subscriptions/" + testSubscription + "/providers/Microsoft.Resources/deploymentStacks/stack"
	q := url.Values{"api-version": {deploymentStackVersion}, "unmanageAction.Resources": {"delete"}, "unmanageAction.ResourceGroups": {"detach"}, "unmanageAction.ManagementGroups": {"detach"}, "bypassStackOutOfSyncError": {"false"}}
	return id, armOrigin + "/subscriptions/" + testSubscription + "/providers/Microsoft.Resources/locations/eastus/deploymentStackOperationStatus/" + testTenant + "?" + q.Encode(), q
}
func TestDeploymentStackPollBoundaries(t *testing.T) {
	for _, fault := range []string{"host", "subscription", "region", "operation", "path_escape", "version", "unknown_query", "duplicate", "missing_mode", "changed_mode", "partial_signature", "userinfo", "fragment", "role"} {
		t.Run(fault, func(t *testing.T) {
			id, endpoint, parameters := stackPollFixture()
			u, _ := url.Parse(endpoint)
			q := u.Query()
			role := "status_url"
			switch fault {
			case "host":
				u.Host = "example.com"
			case "subscription":
				u.Path = strings.Replace(u.Path, testSubscription, testApplication, 1)
			case "region":
				u.Path = strings.Replace(u.Path, "eastus", "westus", 1)
			case "operation":
				u.Path = strings.Replace(u.Path, testTenant, "invalid", 1)
			case "path_escape":
				u.RawPath = strings.Replace(u.Path, "eastus", "%65astus", 1)
			case "version":
				q.Set("api-version", "2024-03-01")
			case "unknown_query":
				q.Set("filter", "hidden")
			case "duplicate":
				q.Add("api-version", deploymentStackVersion)
			case "missing_mode":
				q.Del("unmanageAction.Resources")
			case "changed_mode":
				q.Set("unmanageAction.Resources", "detach")
			case "partial_signature":
				q.Set("t", "signed")
			case "userinfo":
				u.User = url.User("user")
			case "fragment":
				u.Fragment = "fragment"
			case "role":
				role = "result_url"
			}
			u.RawQuery = q.Encode()
			if _, err := deploymentStackPollURL(id, "eastus", u.String(), role, parameters); err == nil {
				t.Fatal("invalid callback accepted")
			}
		})
	}
	id, _, parameters := stackPollFixture()
	endpoint := apiURL("/subscriptions/"+testSubscription+"/operationresults/"+testTenant, "2018-08-01")
	if _, err := deploymentStackPollURL(id, "eastus", endpoint, "result_url", parameters); err != nil {
		t.Fatal("declared Location callback rejected", err)
	}
}
func TestDeploymentStackPollResponseAndPrivacy(t *testing.T) {
	_, endpoint, _ := stackPollFixture()
	u, _ := url.Parse(endpoint)
	for _, fault := range []string{"id", "name", "status", "http", "async", "error", "failed", "canceled"} {
		t.Run(fault, func(t *testing.T) {
			data := map[string]any{"id": u.Path, "name": testTenant, "status": "succeeded"}
			res := response{status: 200, data: data, header: http.Header{}}
			switch fault {
			case "id":
				data["id"] = u.Path + "other"
			case "name":
				data["name"] = testApplication
			case "status":
				data["status"] = "succeededWithFailures"
			case "http":
				res.status = 202
			case "async":
				res.header.Set("Location", endpoint)
			case "error":
				data["error"] = map[string]any{"code": "failure"}
			case "failed", "canceled":
				data["status"] = fault
			}
			if done, err := deploymentStackPollState(endpoint, res); err == nil || done {
				t.Fatal("invalid success accepted", done, err)
			}
		})
	}
	raw := map[string]any{"id": u.Path, "name": testTenant, "status": "failed", "error": map[string]any{"message": "private-canary", "details": []any{map[string]any{"message": "private-canary"}}}, "properties": map[string]any{"parameters": map[string]any{"value": "private-canary"}}, "debug": "private-canary"}
	safe := safeAPIPayload(raw, endpoint)
	wire, _ := json.Marshal(safe)
	if strings.Contains(string(wire), "private-canary") || safe["status"] != "failed" {
		t.Fatal("unsafe polling diagnostics", string(wire))
	}
}
