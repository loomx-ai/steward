package azure

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
)

func fleetRecordings(t *testing.T) []rbacRecording {
	t.Helper()
	// Reuse the retained response envelope; neither request bodies nor
	// authentication headers are part of this common recording format.
	raw, err := os.ReadFile("fixtures/fleet/cli-recordings.json")
	var data struct {
		Sources   []map[string]string `json:"sources"`
		Responses []rbacRecording     `json:"responses"`
	}
	if err != nil || fmt.Sprintf("%x", sha256.Sum256(raw)) != "98f569f8ab3d0fc03d373fba42c73d35955f241f1fda9b54d2e09f059f153d07" || json.Unmarshal(raw, &data) != nil || len(data.Sources) != 2 || len(data.Responses) != 44 {
		t.Fatal("Fleet recording evidence changed", err)
	}
	sources := map[string]string{}
	for _, source := range data.Sources {
		if !strings.Contains(source["source_uri"], "/a20385bffcbb7403846af8a65dbfcaf96c717e4d/") || len(source["source_sha256"]) != 64 {
			t.Fatal("invalid Fleet recording provenance")
		}
		sources[source["source_uri"]] = source["source_sha256"]
	}
	for _, row := range data.Responses {
		if sources[row.SourceURI] != row.SourceSHA256 {
			t.Fatal("unbound Fleet recording")
		}
	}
	return data.Responses
}

func TestFleetRecordedResponseCompatibility(t *testing.T) {
	kinds := map[string]int{}
	deletions, transient, fixed := 0, 0, 0
	for _, row := range fleetRecordings(t) {
		t.Run(fmt.Sprintf("%s/%d", last(row.SourceURI), row.Interaction), func(t *testing.T) {
			u, err := url.Parse(row.URL)
			if err != nil || u.Query().Get("api-version") != "2026-06-02-preview" {
				t.Fatal("recording API changed")
			}
			// A real transient response is HTML, not an ARM error envelope.
			// Exercise the real transport parser and keep it non-absence.
			if row.Status == 503 {
				c := directClient(func(r *http.Request) (*http.Response, error) {
					return &http.Response{StatusCode: row.Status, Header: http.Header(row.Headers), Body: io.NopCloser(strings.NewReader(row.Body))}, nil
				})
				c.subscription = "00000000-0000-0000-0000-000000000000"
				if _, err := c.request(t.Context(), row.Method, row.URL); err == nil || isNotFound(err) {
					t.Fatal("recorded HTML service failure became absence", err)
				}
				transient++
				return
			}
			if row.Method == "DELETE" {
				deletions++
			}
			if strings.TrimSpace(row.Body) == "" {
				if row.Method != "DELETE" {
					t.Fatal("missing recorded GET body")
				}
				return
			}
			var body map[string]any
			if json.Unmarshal([]byte(row.Body), &body) != nil {
				t.Fatal("invalid recorded Fleet JSON")
			}
			values, listed := body["value"].([]any)
			if !listed {
				values = []any{body}
			}
			for _, value := range values {
				raw := object(value)
				id, kind, err := fleetIdentity(text(raw["id"]))
				if err != nil || fleetValidate(kind, raw) != nil {
					t.Fatal("real Fleet metadata rejected", kind, err)
				}
				kinds[kind]++
				if !listed && !strings.EqualFold(id, u.Path) {
					t.Fatal("recorded Fleet route differs from resource")
				}
				if kind == fleetNamespaceType {
					names, dynamic, err := fleetNamespaceMembers(raw)
					if err != nil || dynamic || len(names) != 1 {
						t.Fatal("native fixed namespace placement changed", names, dynamic, err)
					}
					fixed++
					// The preview recording includes rolloutStrategy, which is
					// absent from the selected stable schema. It must stay bound.
					placement := object(object(object(object(raw["properties"])["propagationPolicy"])["placementProfile"])["defaultClusterResourcePlacement"])
					before := directClient(nil).privateConfiguration(fleetSnapshot(kind, raw))
					delete(placement, "rolloutStrategy")
					if before == directClient(nil).privateConfiguration(fleetSnapshot(kind, raw)) {
						t.Fatal("additional recorded configuration lost")
					}
				}
				if kind == fleetGateType {
					props := object(raw["properties"])
					if props["gateType"] != "ScheduledStart" || object(props["scheduledStartProperties"]) == nil {
						t.Fatal("recorded gate subtype changed")
					}
					before := directClient(nil).privateConfiguration(fleetSnapshot(kind, raw))
					delete(props, "scheduledStartProperties")
					if before == directClient(nil).privateConfiguration(fleetSnapshot(kind, raw)) {
						t.Fatal("opaque read-only gate configuration was discarded")
					}
				}
			}
		})
	}
	if len(kinds) != 7 || deletions != 9 || transient != 1 || fixed != 5 {
		t.Fatal("incomplete Fleet recorded coverage", kinds, deletions, transient, fixed)
	}
}
