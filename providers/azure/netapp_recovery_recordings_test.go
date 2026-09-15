package azure

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
)

func TestNetappRecoveryOriginalRecordings(t *testing.T) {
	wire, err := os.ReadFile("fixtures/netapp/recovery-recordings.json")
	sum := sha256.Sum256(wire)
	if err != nil || hex.EncodeToString(sum[:]) != "6e31508855c6df63ecddf297ac13a11667f254b8e35f3ea93fda680d1c98952a" {
		t.Fatal("native recovery extraction changed", err)
	}
	type interaction struct {
		Index             int
		Method, URL, Body string
		Status            int
		Headers           map[string][]string
	}
	type recording struct {
		URI          string `json:"source_uri"`
		SHA          string `json:"source_sha256"`
		Interactions []interaction
	}
	var fixture struct {
		Version               string `json:"api_version"`
		Ref                   string `json:"source_ref"`
		Recordings, Resources []recording
	}
	if json.Unmarshal(wire, &fixture) != nil || fixture.Version != netappVersion || fixture.Ref != "ea185727729efc032ad9d4eef9ec355ee74ebaae" || len(fixture.Recordings) != 1 || len(fixture.Resources) != 1 {
		t.Fatal("native recovery manifest")
	}
	record := fixture.Recordings[0]
	if record.SHA != "e005b63c13550051185d49fb5a6c6aa7590655f7103191a3c558f81d0e4bfe88" || !strings.Contains(record.URI, "/"+fixture.Ref+"/") || len(record.Interactions) != 4 {
		t.Fatal("snapshot recording provenance")
	}
	first := record.Interactions[0]
	u, _ := url.Parse(first.URL)
	id := strings.ToLower(u.Path)
	if first.Index != 65 || first.Method != "DELETE" || first.Status != 202 || first.Body != "" || u.Query().Get("api-version") != netappVersion || len(u.Query()) != 1 {
		t.Fatal("snapshot native acknowledgement")
	}
	calls := 0
	r := protocolRuntime(t, func(q *http.Request) (*http.Response, error) {
		calls++
		if calls >= len(record.Interactions) {
			t.Fatal("extra native poll")
		}
		next := record.Interactions[calls]
		if q.Method != next.Method || q.URL.String() != next.URL {
			t.Fatal("native poll changed")
		}
		h := http.Header{}
		for k, vs := range next.Headers {
			for _, v := range vs {
				h.Add(k, v)
			}
		}
		return &http.Response{StatusCode: next.Status, Header: h, Body: io.NopCloser(strings.NewReader(next.Body))}, nil
	})
	c, err := r.resolve(t.Context(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	c.subscription = strings.Split(id, "/")[2]
	h := http.Header{}
	for k, vs := range first.Headers {
		for _, v := range vs {
			h.Add(k, v)
		}
	}
	receipt, err := c.netappDeleteReceipt(id, "eastus", response{status: first.Status, header: h})
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i < len(record.Interactions); i++ {
		wire, _ := json.Marshal(receipt)
		var saved map[string]any
		if json.Unmarshal(wire, &saved) != nil {
			t.Fatal("receipt round trip")
		}
		poll, err := c.netappPoll(t.Context(), id, "eastus", saved)
		if err != nil || poll.Done != (i == 3) {
			t.Fatal("native snapshot phase", i, poll, err)
		}
		receipt = poll.Data
	}
	metadata := fixture.Resources[0]
	if metadata.SHA != "0e3fdbedf9da3e0cd9651753bcea87d1b54bade67a0910c158babf2d5f0c4160" || !strings.Contains(metadata.URI, "/"+fixture.Ref+"/") || len(metadata.Interactions) != 2 {
		t.Fatal("backup metadata provenance")
	}
	var backups []map[string]any
	for _, entry := range metadata.Interactions {
		if entry.Method != "GET" || entry.Status != 200 {
			t.Fatal("backup evidence is not an own GET")
		}
		u, _ := url.Parse(entry.URL)
		var raw map[string]any
		if json.Unmarshal([]byte(entry.Body), &raw) != nil {
			t.Fatal("native backup JSON")
		}
		owner := strings.ToLower(u.Path)
		if c.netappIdentity(owner, netappBackupType) != nil || !netappMetadata(raw, owner, netappBackupType) || !uuidPattern.MatchString(netappRecoveryIncarnation(netappBackupType, raw)) {
			t.Fatal("native backup identity rejected")
		}
		p := object(raw["properties"])
		if _, ok := netappRecoveryTime(p["creationDate"]); !ok {
			t.Fatal("native creation time")
		}
		if _, ok := netappRecoveryTime(p["snapshotCreationDate"]); !ok {
			t.Fatal("native snapshot time")
		}
		backups = append(backups, p)
	}
	if backups[0]["backupId"] == backups[1]["backupId"] || backups[0]["volumeResourceId"] != backups[1]["volumeResourceId"] {
		t.Fatal("native independent backup identities lost")
	}
	firstTime, _ := netappRecoveryTime(backups[0]["snapshotCreationDate"])
	later, _ := netappRecoveryTime(backups[1]["snapshotCreationDate"])
	if !later.After(firstTime) {
		t.Fatal("recorded backup ordering")
	}
}
