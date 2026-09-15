package azure

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"maps"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/execution"
)

func netappTestOwner() string {
	return strings.ToLower(resourceID(netappAccountType, "account")) + "/capacitypools/pool/volumes/volume"
}
func netappTestPollURL(role string) string {
	endpoint := apiURL("/subscriptions/"+testSubscription+"/providers/Microsoft.NetApp/locations/eastus/operationResults/"+testTenant, netappVersion)
	if role == "result_url" {
		endpoint += "&operationResultResponseType=Location"
	}
	return endpoint
}
func netappTestStatus(id, state string) map[string]any {
	u, _ := url.Parse(netappTestPollURL("status_url"))
	return map[string]any{"id": u.Path, "name": testTenant, "status": state, "properties": map[string]any{"resourceName": id, "action": "DELETE"}}
}
func TestNetappRecordedNativeDeletionPolling(t *testing.T) {
	wire, err := os.ReadFile("fixtures/netapp/delete-recordings.json")
	sum := sha256.Sum256(wire)
	if err != nil || hex.EncodeToString(sum[:]) != "791fb4ac16f4c6a4628d1268b9b3bd162677c5c2bb12d5722660c457ceb88823" {
		t.Fatal("recorded extraction changed", err)
	}
	var fixture struct {
		Version    string `json:"api_version"`
		Ref        string `json:"source_ref"`
		Recordings []struct {
			URI          string `json:"source_uri"`
			SHA          string `json:"source_sha256"`
			Interactions []struct {
				Index             int
				Method, URL, Body string
				Status            int
				Headers           map[string][]string
			}
		}
	}
	if json.Unmarshal(wire, &fixture) != nil || fixture.Version != netappVersion || fixture.Ref != "ea185727729efc032ad9d4eef9ec355ee74ebaae" || len(fixture.Recordings) != 4 {
		t.Fatal("native manifest")
	}
	for _, record := range fixture.Recordings {
		t.Run(last(record.URI), func(t *testing.T) {
			if !strings.Contains(record.URI, "/"+fixture.Ref+"/") || len(record.SHA) != 64 {
				t.Fatal("source provenance")
			}
			first := record.Interactions[0]
			u, _ := url.Parse(first.URL)
			id := strings.ToLower(u.Path)
			subscription := strings.Split(id, "/")[2]
			headers := http.Header{}
			for k, vs := range first.Headers {
				for _, v := range vs {
					headers.Add(k, v)
				}
			}
			if first.Method != "DELETE" || first.Status != 202 || first.Body != "" {
				t.Fatal("native acknowledgement")
			}
			calls := 0
			runtime := protocolRuntime(t, func(q *http.Request) (*http.Response, error) {
				calls++
				if calls >= len(record.Interactions) {
					t.Fatal("extra poll")
				}
				next := record.Interactions[calls]
				if q.Method != next.Method || q.URL.String() != next.URL {
					t.Fatal("poll protocol changed", calls)
				}
				h := http.Header{}
				for k, vs := range next.Headers {
					for _, v := range vs {
						h.Add(k, v)
					}
				}
				return &http.Response{StatusCode: next.Status, Header: h, Body: io.NopCloser(strings.NewReader(next.Body))}, nil
			})
			c, err := runtime.resolve(t.Context(), "connection")
			if err != nil {
				t.Fatal(err)
			}
			// The upstream recordings replace their subscription with all-zero UUIDs.
			// Match that owner without modifying the retained response bodies or URLs.
			c.subscription = subscription
			receipt, err := c.netappDeleteReceipt(id, "eastus", response{status: first.Status, header: headers})
			if err != nil {
				t.Fatal(err)
			}
			for i := 1; i < len(record.Interactions); i++ {
				encoded, _ := json.Marshal(receipt)
				var restored map[string]any
				if json.Unmarshal(encoded, &restored) != nil {
					t.Fatal("saved receipt")
				}
				fresh, err := NewRuntime(runtime.credentials)
				if err != nil {
					t.Fatal(err)
				}
				fresh.transport = runtime.transport
				c, err = fresh.resolve(t.Context(), "connection")
				if err != nil {
					t.Fatal(err)
				}
				c.subscription = subscription
				poll, err := c.netappPoll(t.Context(), id, "eastus", restored)
				if err != nil {
					t.Fatal(i, err)
				}
				// The subvolume recording ends after status success, before its Location
				// result. Never manufacture the missing observation or report completion.
				wantDone := i == len(record.Interactions)-1 && strings.Contains(record.Interactions[i].URL, "operationResultResponseType=Location")
				if poll.Done != wantDone {
					t.Fatal("native completion phases", i, poll.Done, wantDone)
				}
				receipt = poll.Data
			}
			if receipt["complete"] == true {
				if poll, err := c.netappPoll(t.Context(), id, "eastus", receipt); err != nil || !poll.Done || calls != len(record.Interactions)-1 {
					t.Fatal("completed operation repeated", err)
				}
			}
		})
	}
}
func TestNetappDeleteReceiptScopeAndStatus(t *testing.T) {
	c := &client{subscription: testSubscription}
	owner := netappTestOwner()
	for _, kind := range netappResources {
		id := strings.ToLower(resourceID(netappAccountType, "account"))
		for _, collection := range strings.Split(kind.kind, "/")[2:] {
			id += "/" + strings.ToLower(collection) + "/item"
		}
		for _, status := range []int{200, 201, 202, 204, 206} {
			h := http.Header{}
			if status == 202 {
				h.Set("Azure-AsyncOperation", netappTestPollURL("status_url"))
				h.Set("Location", netappTestPollURL("result_url"))
			}
			receipt, err := c.netappDeleteReceipt(id, "eastus", response{status: status, header: h})
			want := status == 202 || status == 204 || status == 200 && (kind.family == "Snapshots" || kind.family == "Subvolumes" || kind.family == "VolumeQuotaRules" || kind.family == "VolumeGroups")
			if (err == nil) != want {
				t.Fatal(kind.family, status, err)
			}
			if want && c.netappVerifyReceipt(id, "eastus", receipt) != nil {
				t.Fatal("receipt rejected")
			}
		}
	}
	for _, role := range []string{"status_url", "result_url"} {
		endpoint := netappTestPollURL(role)
		for _, bad := range []string{strings.Replace(endpoint, "https:", "http:", 1), strings.Replace(endpoint, "management.azure.com", "evil.example", 1), strings.Replace(endpoint, testSubscription, testApplication, 1), strings.Replace(endpoint, "eastus", "westus", 1), strings.Replace(endpoint, "Microsoft.NetApp", "Microsoft.Compute", 1), strings.Replace(endpoint, "operationResults", "volumes", 1), strings.Replace(endpoint, testTenant, "not-a-uuid", 1), endpoint + "&api-version=" + netappVersion, endpoint + "&unknown=true", endpoint + "&t=partial", endpoint + "#fragment", strings.Replace(endpoint, "operationResults", "%6fperationResults", 1), strings.Replace(endpoint, netappVersion, "2025-01-01", 1)} {
			if _, err := c.netappPollURL(owner, "eastus", bad, role); err == nil {
				t.Fatal("invalid polling scope accepted", role, bad)
			}
		}
		if _, err := c.netappPollURL(owner, "eastus", endpoint+"&t=one&c=two&s=three&h=four", role); err != nil {
			t.Fatal("native signed query", err)
		}
	}
	for _, fault := range []string{"missing", "empty", "duplicate", "disagree", "wrong role", "unknown header", "body", "204 location", "owner", "region"} {
		h := http.Header{}
		h.Set("Location", netappTestPollURL("result_url"))
		status := 202
		id, region := owner, "eastus"
		var body map[string]any
		switch fault {
		case "missing":
			h = nil
		case "empty":
			h.Set("Location", "")
		case "duplicate":
			h.Add("Location", h.Get("Location"))
		case "disagree":
			h.Set("Azure-AsyncOperation", strings.Replace(netappTestPollURL("status_url"), testTenant, testApplication, 1))
		case "wrong role":
			h.Set("Location", netappTestPollURL("status_url"))
		case "unknown header":
			h.Set("Operation-Location", h.Get("Location"))
		case "body":
			body = map[string]any{"status": "Succeeded"}
		case "204 location":
			status = 204
		case "owner":
			id = resourceID(vmType, "vm")
		case "region":
			region = "EastUS"
		}
		if _, err := c.netappDeleteReceipt(id, region, response{status: status, header: h, data: body}); err == nil {
			t.Fatal("invalid acknowledgement", fault)
		}
	}
}
func TestNetappPollingRejectsUncertainCompletion(t *testing.T) {
	for _, role := range []string{"status_url", "result_url"} {
		for _, fault := range []string{"404", "403", "503", "206", "201", "missing body", "failed", "canceled", "unknown state", "wrong operation", "wrong resource", "wrong action", "changed header", "error"} {
			t.Run(role+"/"+fault, func(t *testing.T) {
				id := netappTestOwner()
				status := 200
				body := netappTestStatus(id, "Succeeded")
				h := http.Header{}
				switch fault {
				case "404":
					status = 404
				case "403":
					status = 403
				case "503":
					status = 503
				case "206":
					status = 206
				case "201":
					status = 201
				case "missing body":
					body = nil
					if role == "result_url" {
						status = 206
					}
				case "failed":
					body["status"] = "Failed"
				case "canceled":
					body["status"] = "Canceled"
				case "unknown state":
					body["status"] = "Future"
				case "wrong operation":
					body["name"] = testApplication
				case "wrong resource":
					object(body["properties"])["resourceName"] = id + "-other"
				case "wrong action":
					object(body["properties"])["action"] = "PUT"
				case "changed header":
					h.Set("Location", strings.Replace(netappTestPollURL("result_url"), testTenant, testApplication, 1))
				case "error":
					body["error"] = map[string]any{"message": "netapp-poll-private-canary"}
				}
				r := protocolRuntime(t, func(q *http.Request) (*http.Response, error) {
					if q.Method != "GET" || q.URL.String() != netappTestPollURL(role) {
						t.Fatal("unexpected request")
					}
					return jsonResponse(status, body, h), nil
				})
				c, err := r.resolve(t.Context(), "connection")
				if err != nil {
					t.Fatal(err)
				}
				poll, err := c.netappPoll(t.Context(), id, "eastus", c.netappSignReceipt(id, "eastus", map[string]any{role: netappTestPollURL(role)}))
				if err == nil || poll.Done || isNotFound(err) || strings.Contains(err.Error(), "netapp-poll-private-canary") {
					t.Fatal("uncertain completion escaped", poll, err)
				}
			})
		}
	}
}
func TestNetappPollingSavedReceiptTampering(t *testing.T) {
	calls := 0
	r := protocolRuntime(t, func(q *http.Request) (*http.Response, error) { calls++; return nil, nil })
	c, err := r.resolve(t.Context(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	id := netappTestOwner()
	receipt := c.netappSignReceipt(id, "eastus", map[string]any{"complete": true, "result_url": netappTestPollURL("result_url")})
	for _, fault := range []string{"binding", "url", "phase", "field", "owner", "region", "credential"} {
		bad := maps.Clone(receipt)
		owner, region := id, "eastus"
		copy := *c
		switch fault {
		case "binding":
			delete(bad, "binding")
		case "url":
			bad["result_url"] = netappTestPollURL("status_url")
		case "phase":
			delete(bad, "complete")
		case "field":
			bad["unexpected"] = true
		case "owner":
			owner += "-other"
		case "region":
			region = "westus"
		case "credential":
			copy.fingerprint[0]++
		}
		if _, err := copy.netappPoll(t.Context(), owner, region, bad); err == nil || calls != 0 {
			t.Fatal("modified receipt accepted", fault, err)
		}
	}
	for _, invalid := range []map[string]any{{"unknown": true}, {"complete": false}, {"status_done": true}, {"result_url": true}} {
		if _, err := c.netappPoll(t.Context(), id, "eastus", c.netappSignReceipt(id, "eastus", invalid)); err == nil || calls != 0 {
			t.Fatal("invalid saved phase")
		}
	}
	for _, region := range []string{"eastus", ""} {
		receipt := c.netappSignReceipt(id, region, nil)
		poll, err := c.netappPoll(t.Context(), id, region, receipt)
		if region == "eastus" && (err != nil || !poll.Done) || region == "" && err == nil || calls != 0 {
			t.Fatal("synchronous acknowledgement", err)
		}
	}
}

func TestNetappPollingResultWireAndPrivateLogs(t *testing.T) {
	for _, status := range []int{200, 202, 204} {
		id := netappTestOwner()
		endpoint := netappTestPollURL("result_url") + "&t=private-time&c=private-client&s=private-signature&h=private-hash"
		runtime := protocolRuntime(t, func(q *http.Request) (*http.Response, error) {
			if q.Method != "GET" || q.URL.String() != endpoint {
				t.Fatal("result endpoint changed")
			}
			return &http.Response{StatusCode: status, Header: http.Header{"Retry-After": {"9"}}, Body: io.NopCloser(strings.NewReader(""))}, nil
		})
		c, err := runtime.resolve(t.Context(), "connection")
		if err != nil {
			t.Fatal(err)
		}
		var logs []execution.JobLogEntry
		ctx := execution.WithJobLogSink(t.Context(), execution.JobLogSinkFunc(func(_ context.Context, e execution.JobLogEntry) { logs = append(logs, e) }))
		receipt := c.netappSignReceipt(id, "eastus", map[string]any{"result_url": endpoint})
		result, err := c.netappPoll(ctx, id, "eastus", receipt)
		if err != nil || result.Done != (status != 202) || result.RetryAfter.Seconds() != 9 {
			t.Fatal("empty native result", status, result, err)
		}
		encoded, _ := json.Marshal(logs)
		if strings.Contains(string(encoded), "private-") {
			t.Fatal("operation signature in diagnostics")
		}
	}
}
