package azure

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
)

func TestNetappBackupStatusRecordedResponses(t *testing.T) {
	wire, err := os.ReadFile("fixtures/netapp/backup-status-recordings.json")
	if err != nil {
		t.Fatal(err)
	}
	var recording struct {
		Interactions []struct {
			Method, URL, Body string
			Status            int
		}
	}
	if err := json.Unmarshal(wire, &recording); err != nil {
		t.Fatal(err)
	}
	if len(recording.Interactions) != 3 {
		t.Fatal("missing recorded status reads")
	}
	for _, row := range recording.Interactions {
		f := newNetappFixture(t)
		u, err := url.Parse(strings.Replace(row.URL, "00000000-0000-0000-0000-000000000000", testSubscription, 1))
		if err != nil {
			t.Fatal(err)
		}
		id := strings.TrimSuffix(strings.ToLower(u.Path), "/latestbackupstatus/current")
		calls := 0
		f.override = func(q *http.Request) (*http.Response, bool) {
			calls++
			if q.Method != row.Method || !strings.EqualFold(q.URL.Path, u.Path) || q.URL.RawQuery != u.RawQuery || q.ContentLength != 0 {
				t.Fatal("recorded request mismatch", q.URL)
			}
			return &http.Response{StatusCode: row.Status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(row.Body))}, true
		}
		c, err := f.runtime.resolve(t.Context(), "connection")
		if err != nil {
			t.Fatal(err)
		}
		idle, err := c.netappBackupIdle(t.Context(), id)
		if err != nil || !idle || calls != 1 {
			t.Fatal("recorded idle rejected", idle, calls, err)
		}
	}
}

func TestNetappBackupStatusNativeEvidence(t *testing.T) {
	wire, err := os.ReadFile("fixtures/netapp/Volumes_LatestBackupStatus.json")
	if err != nil {
		t.Fatal(err)
	}
	var example struct {
		Responses map[string]struct{ Body map[string]any } `json:"responses"`
	}
	if err := json.Unmarshal(wire, &example); err != nil {
		t.Fatal(err)
	}
	for _, fault := range []string{"native", "transferring", "failed", "unknown", "future", "missing", "null", "numeric", "object", "wrapped", "empty", "accepted", "no content", "not found", "forbidden", "server error", "async", "operation location", "location", "error body", "foreign subscription", "wrong kind"} {
		t.Run(fault, func(t *testing.T) {
			f := newNetappFixture(t)
			id := strings.ToLower(resourceID(netappAccountType, "first")) + "/capacitypools/item/volumes/item"
			calls := 0
			f.override = func(q *http.Request) (*http.Response, bool) {
				calls++
				if q.Method != "GET" || q.URL.Path != id+"/latestBackupStatus/current" || q.URL.Query().Get("api-version") != netappVersion || len(q.URL.Query()) != 1 || q.ContentLength != 0 {
					t.Fatal("wrong backup status request", q.Method, q.URL)
				}
				body := batchClone(example.Responses["200"].Body)
				status := 200
				header := http.Header{}
				switch fault {
				case "transferring":
					body["relationshipStatus"] = "Transferring"
				case "failed":
					body["relationshipStatus"] = "Failed"
				case "unknown":
					body["relationshipStatus"] = "Unknown"
				case "future":
					body["relationshipStatus"] = "NewState"
				case "missing":
					delete(body, "relationshipStatus")
				case "null":
					body["relationshipStatus"] = nil
				case "numeric":
					body["relationshipStatus"] = 204
				case "object":
					body["relationshipStatus"] = map[string]any{"status": "Idle"}
				case "wrapped":
					body = map[string]any{"properties": body}
				case "empty":
					return &http.Response{StatusCode: 200, Header: header, Body: io.NopCloser(strings.NewReader(""))}, true
				case "accepted":
					status = 202
				case "no content":
					status = 204
				case "not found":
					status = 404
				case "forbidden":
					status = 403
				case "server error":
					status = 503
				case "async":
					header.Set("Azure-AsyncOperation", netappTestPollURL("status_url"))
				case "operation location":
					header.Set("Operation-Location", netappTestPollURL("status_url"))
				case "location":
					header.Set("Location", netappTestPollURL("result_url"))
				case "error body":
					body["error"] = map[string]any{"code": "Failed"}
				}
				return jsonResponse(status, body, header), true
			}
			c, err := f.runtime.resolve(t.Context(), "connection")
			if err != nil {
				t.Fatal(err)
			}
			if fault == "foreign subscription" {
				id = strings.Replace(id, testSubscription, testTenant, 1)
			}
			if fault == "wrong kind" {
				id = redisParentID(id)
			}
			idle, err := c.netappBackupIdle(t.Context(), id)
			if fault == "native" {
				if err != nil || !idle {
					t.Fatal("native Idle rejected", idle, err)
				}
			} else if fault == "transferring" {
				if err != nil || idle {
					t.Fatal("transfer must wait", idle, err)
				}
			} else if err == nil || idle {
				t.Fatal("ambiguous evidence accepted", idle, err)
			}
			if fault == "foreign subscription" || fault == "wrong kind" {
				if calls != 0 {
					t.Fatal("invalid scope sent to Azure")
				}
			} else if calls != 1 {
				t.Fatal("unexpected requests", calls)
			}
		})
	}
}
