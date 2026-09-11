package azure

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
)

type rbacRecording struct {
	SourceURI    string              `json:"source_uri"`
	SourceSHA256 string              `json:"source_sha256"`
	Interaction  int                 `json:"interaction"`
	Method       string              `json:"method"`
	URL          string              `json:"url"`
	Status       int                 `json:"status"`
	Headers      map[string][]string `json:"headers"`
	Body         string              `json:"body"`
}

func rbacRecordings(t *testing.T) []rbacRecording {
	t.Helper()
	payload, err := os.ReadFile("fixtures/rbac/cli-recordings.json")
	var data struct {
		Sources   []map[string]string `json:"sources"`
		Responses []rbacRecording     `json:"responses"`
	}
	if err != nil || fmt.Sprintf("%x", sha256.Sum256(payload)) != "7eb59a1aa1885a818a46b0da830eb05b5477a6d7e21d59ee683848ff6a810a57" || json.Unmarshal(payload, &data) != nil {
		t.Fatal("invalid RBAC recordings", err)
	}
	if len(data.Sources) != 3 || len(data.Responses) != 14 {
		t.Fatal("incomplete RBAC recordings", len(data.Responses))
	}
	sources := map[string]string{}
	for _, source := range data.Sources {
		if !strings.Contains(source["source_uri"], "/5e912a20420a11f53b0b7bc9a2c1f03f98de6238/") || len(source["source_sha256"]) != 64 {
			t.Fatal("invalid RBAC recording provenance")
		}
		sources[source["source_uri"]] = source["source_sha256"]
	}
	for _, row := range data.Responses {
		if sources[row.SourceURI] != row.SourceSHA256 {
			t.Fatal("unbound RBAC response")
		}
	}
	return data.Responses
}

func TestRBACRecordedResponseCompatibility(t *testing.T) {
	c := directClient(nil)
	c.subscription = "00000000-0000-0000-0000-000000000000"
	roles, assignments, inherited, nativeDeletes := 0, 0, 0, 0
	for _, row := range rbacRecordings(t) {
		t.Run(fmt.Sprintf("%s/%d", last(row.SourceURI), row.Interaction), func(t *testing.T) {
			var body map[string]any
			if json.Unmarshal([]byte(row.Body), &body) != nil {
				t.Fatal("invalid retained response")
			}
			values, listed := body["value"].([]any)
			if !listed {
				values = []any{body}
			}
			u, err := url.Parse(row.URL)
			if err != nil {
				t.Fatal(err)
			}
			for _, value := range values {
				raw := object(value)
				_, scope, kind, err := rbacResourceID(text(raw["id"]))
				if err != nil {
					t.Fatal("native identity", err)
				}
				canonical, err := c.rbacValidate(kind, raw)
				if err != nil {
					t.Fatal("native RBAC response rejected", text(raw["id"]), err)
				}
				if kind == rbacRoleType {
					roles++
					if u.Query().Get("api-version") != "2022-05-01-preview" || !strings.HasPrefix(canonical, c.root()+"/") {
						t.Fatal("role recording version/identity changed")
					}
				} else {
					assignments++
					if u.Query().Get("api-version") != "2022-04-01" || canonical != strings.ToLower(text(raw["id"])) {
						t.Fatal("assignment recording version/identity changed")
					}
					if !c.rbacLocalScope(scope) {
						inherited++
					}
				}
			}
			if row.Method == "DELETE" {
				if row.Status != 200 || u.Query().Get("api-version") != "2022-04-01" || len(values) != 1 {
					t.Fatal("native assignment delete protocol changed")
				}
				nativeDeletes++
			}
		})
	}
	if roles != 645 || assignments != 167 || inherited == 0 || nativeDeletes != 1 {
		t.Fatal("incomplete recorded RBAC checks", roles, assignments, inherited, nativeDeletes)
	}
}
