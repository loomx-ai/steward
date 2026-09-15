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

func TestNetappChildOriginalRecordings(t *testing.T) {
	wire, err := os.ReadFile("fixtures/netapp/child-recordings.json")
	sum := sha256.Sum256(wire)
	if err != nil || hex.EncodeToString(sum[:]) != "af5b581292500abb1ab39762e6dc325ea59d92cd4eacf0dff9958a1ffe64ef37" {
		t.Fatal("native child evidence changed", err)
	}
	type interaction struct {
		Index             int
		Method, URL, Body string
		Status            int
		Headers           map[string][]string
	}
	type recording struct {
		URI                string `json:"source_uri"`
		SHA                string `json:"source_sha256"`
		Metadata, Deletion []interaction
	}
	var fixture struct {
		Version    string `json:"api_version"`
		Ref        string `json:"source_ref"`
		Recordings []recording
	}
	if json.Unmarshal(wire, &fixture) != nil || fixture.Version != netappVersion || fixture.Ref != "ea185727729efc032ad9d4eef9ec355ee74ebaae" || len(fixture.Recordings) != 2 {
		t.Fatal("native child manifest")
	}
	expected := []string{"0ab3382b790841777ad6cab0b3d7a7c5348f6611296f6aa9d565d339005893e7", "60d98b1270aa160e1ba02d96a7c78a9a07e443292513ca8790298fe8e682d1d7"}
	r := protocolRuntime(t, func(q *http.Request) (*http.Response, error) {
		t.Fatal("unexpected network request", q.Method)
		return nil, nil
	})
	c, err := r.resolve(t.Context(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	c.subscription = "00000000-0000-0000-0000-000000000000"
	for i, record := range fixture.Recordings {
		if record.SHA != expected[i] || !strings.Contains(record.URI, "/"+fixture.Ref+"/") || len(record.Metadata) != 2 {
			t.Fatal("child recording provenance")
		}
		var raws []map[string]any
		for _, entry := range record.Metadata {
			var raw map[string]any
			u, _ := url.Parse(entry.URL)
			if entry.Method != "GET" || entry.Status != 200 || json.Unmarshal([]byte(entry.Body), &raw) != nil {
				t.Fatal("invalid own GET")
			}
			kind := text(raw["type"])
			if !netappIndependentChild(kind) || c.netappIdentity(strings.ToLower(u.Path), kind) != nil || !netappMetadata(raw, strings.ToLower(u.Path), kind) || !netappIndependentChildReady(kind, raw) {
				t.Fatal("original child metadata rejected")
			}
			uid, created := c.netappLeafIdentity(kind, raw)
			if uid == "" || created != object(raw["systemData"])["createdAt"] {
				t.Fatal("native creation observation lost")
			}
			raws = append(raws, raw)
		}
		if i == 0 {
			if object(raws[0]["systemData"])["createdAt"] != object(raws[1]["systemData"])["createdAt"] || object(raws[0]["properties"])["path"] == object(raws[1]["properties"])["path"] {
				t.Fatal("subvolume recorded update lost")
			}
		} else {
			// This real service changes createdAt on PUT. Treat it as context evidence,
			// never claim it is an immutable child UUID or ignore the updated boundary.
			if object(raws[0]["systemData"])["createdAt"] == object(raws[1]["systemData"])["createdAt"] {
				t.Fatal("quota update creation discrepancy lost")
			}
		}
	}
	record := fixture.Recordings[1]
	if len(record.Deletion) != 4 {
		t.Fatal("incomplete quota deletion recording")
	}
	first := record.Deletion[0]
	u, _ := url.Parse(first.URL)
	id := strings.ToLower(u.Path)
	if first.Index != 49 || first.Method != "DELETE" || first.Status != 202 || first.Body != "" || len(u.Query()) != 1 || u.Query().Get("api-version") != netappVersion {
		t.Fatal("quota delete wire contract")
	}
	headers := func(values map[string][]string) http.Header {
		h := http.Header{}
		for k, vs := range values {
			for _, v := range vs {
				h.Add(k, v)
			}
		}
		return h
	}
	calls := 0
	runtime := protocolRuntime(t, func(q *http.Request) (*http.Response, error) {
		calls++
		if calls >= len(record.Deletion) {
			t.Fatal("extra quota callback")
		}
		next := record.Deletion[calls]
		if q.Method != next.Method || q.URL.String() != next.URL {
			t.Fatal("native quota callback changed")
		}
		return &http.Response{StatusCode: next.Status, Header: headers(next.Headers), Body: io.NopCloser(strings.NewReader(next.Body))}, nil
	})
	c, err = runtime.resolve(t.Context(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	c.subscription = strings.Split(id, "/")[2]
	receipt, err := c.netappDeleteReceipt(id, "westcentralus", response{status: first.Status, header: headers(first.Headers)})
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i < 4; i++ {
		saved, _ := json.Marshal(receipt)
		var restored map[string]any
		if json.Unmarshal(saved, &restored) != nil {
			t.Fatal("receipt serialization")
		}
		poll, err := c.netappPoll(t.Context(), id, "westcentralus", restored)
		if err != nil || poll.Done != (i == 3) {
			t.Fatal("native quota poll", i, poll, err)
		}
		receipt = poll.Data
	}
}
