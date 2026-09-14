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

// These are unchanged preview responses, not stable-version wire replays. They
// establish that cancel acknowledgement and continued record existence coexist.
func TestSynapseCancellationRecordingSemantics(t *testing.T) {
	payload, err := os.ReadFile("fixtures/synapse/data-plane/cli-cancellation-recordings.json")
	if err != nil || fmt.Sprintf("%x", sha256.Sum256(payload)) != "c3bb0fcbcec3d392cd00a3161106e3fb619deba6e72ed38e31452bede1cd2a34" {
		t.Fatal("cancellation recording changed", err)
	}
	var sources []struct {
		File string                  `json:"file"`
		URI  string                  `json:"source_uri"`
		SHA  string                  `json:"source_sha256"`
		Rows []redisRecordedResponse `json:"recordings"`
	}
	if json.Unmarshal(payload, &sources) != nil || len(sources) != 2 {
		t.Fatal("invalid recordings")
	}
	for _, source := range sources {
		expected := map[string]string{"test_spark_job.yaml": "431c4ca2e96f5e5da72d397bda257033ef8179f14e2c175f51164369c2909d94", "test_spark_session_and_statements.yaml": "b64405d9ea86a3a0ac1bca1d8d4abdc550d19098f1964d3fa44c680cdf02076f"}[source.File]
		if expected == "" || source.SHA != expected || !strings.Contains(source.URI, "/c683a64f397974bae397d77a204e2ae86a908fa0/") || len(source.Rows) != 2 {
			t.Fatal("unverified provenance")
		}
		for i, row := range source.Rows {
			u, err := url.Parse(row.URI)
			if err != nil || !strings.Contains(u.Path, "/versions/2019-11-01-preview/") || row.Status != 200 {
				t.Fatal("rewritten native recording")
			}
			h := http.Header{}
			for k, v := range row.Headers {
				for _, value := range v {
					h.Add(k, value)
				}
			}
			if i == 0 {
				if row.Method != "DELETE" || synapseCancelReceipt(response{status: row.Status, data: row.Body, header: h}) != nil {
					t.Fatal("native acknowledgement rejected")
				}
			} else {
				if row.Method != "GET" || row.Body["id"] == nil || !synapseSparkQuiesced(row.Body) || synapseSparkIncarnation(row.Body) != nil {
					t.Fatal("native completed record misclassified")
				}
				// Removing either service completion marker must prevent cleanup evidence.
				raw := batchClone(row.Body)
				object(raw["pluginInfo"])["currentState"] = "Cleanup"
				if synapseSparkQuiesced(raw) {
					t.Fatal("cleanup in progress treated as stopped")
				}
			}
		}
	}
}
