package azure

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/loomx-ai/steward/internal/core/execution"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
)

func TestDataProtectionRecordedVaultDeletionPolling(t *testing.T) {
	wire, err := os.ReadFile("fixtures/dataprotection/vault-delete-recording.json")
	sum := sha256.Sum256(wire)
	if err != nil || hex.EncodeToString(sum[:]) != "446d74b12414cddd77e82045d4b70136eee6ea78cafd3a5253e763875aa31173" {
		t.Fatal("recording extraction changed", err)
	}
	var fixture struct {
		APIVersion   string `json:"api_version"`
		Source       struct{ File, Source, SHA256 string }
		Interactions []struct {
			SourceIndex       int `json:"source_index"`
			Method, URL, Body string
			Status            int
			Headers           map[string][]string
		}
	}
	if err = json.Unmarshal(wire, &fixture); err != nil || fixture.APIVersion != dataProtectionVaultVersion || len(fixture.Interactions) != 4 || fixture.Source.SHA256 != "06f305cbc3de340f31d23bbbcdb87609302728f9d7a3b673f19f33afec9a2387" || !strings.Contains(fixture.Source.Source, "/a57501bbefc0a7e65e7d4762fda57807566fb5e0/") {
		t.Fatal("unverified native recording", err)
	}
	first := fixture.Interactions[0]
	u, err := url.Parse(first.URL)
	if err != nil || first.Method != "DELETE" || first.Status != 202 || first.SourceIndex != 5 || first.Body != "" {
		t.Fatal("invalid delete evidence")
	}
	id := strings.ToLower(u.Path)
	subscription := strings.Split(id, "/")[2]
	h := http.Header{}
	for key, values := range first.Headers {
		for _, value := range values {
			h.Add(key, value)
		}
	}
	calls := 0
	runtime := protocolRuntime(t, func(q *http.Request) (*http.Response, error) {
		calls++
		if calls >= len(fixture.Interactions) {
			t.Fatal("unexpected extra operation request")
		}
		expected := fixture.Interactions[calls]
		if q.Method != expected.Method || q.URL.String() != expected.URL {
			t.Fatal("native polling protocol changed", calls)
		}
		headers := http.Header{}
		for key, values := range expected.Headers {
			for _, value := range values {
				headers.Add(key, value)
			}
		}
		return &http.Response{StatusCode: expected.Status, Header: headers, Body: io.NopCloser(strings.NewReader(expected.Body))}, nil
	})
	c, err := runtime.resolve(t.Context(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	// Azure sanitizes this recording's subscription; preserve all original wires.
	c.subscription = subscription
	receipt, err := c.dataProtectionDeleteReceipt(id, "centraluseuap", response{status: first.Status, header: h}, true)
	if err != nil {
		t.Fatal(err)
	}
	var logs []execution.JobLogEntry
	ctx := execution.WithJobLogSink(t.Context(), execution.JobLogSinkFunc(func(_ context.Context, entry execution.JobLogEntry) { logs = append(logs, entry) }))
	for i := 1; i < len(fixture.Interactions); i++ {
		encoded, _ := json.Marshal(receipt)
		receipt = nil
		if err = json.Unmarshal(encoded, &receipt); err != nil {
			t.Fatal(err)
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
		poll, err := c.dataProtectionPoll(ctx, id, "centraluseuap", receipt)
		if err != nil || poll.Done != (i == 3) {
			t.Fatal("native poll recovery", i, err, poll.Done)
		}
		receipt = poll.Data
	}
	if poll, err := c.dataProtectionPoll(ctx, id, "centraluseuap", receipt); err != nil || !poll.Done || calls != 3 {
		t.Fatal("completed receipt repeated IO", err, calls)
	}
	// Query signatures remain opaque capabilities and never authorize other scopes.
	original, _ := url.Parse(h.Get("Azure-AsyncOperation"))
	encodedLogs, _ := json.Marshal(logs)
	if len(logs) == 0 {
		t.Fatal("missing native operation diagnostics")
	}
	for _, key := range []string{"t", "c", "s", "h"} {
		if strings.Contains(string(encodedLogs), original.Query().Get(key)) {
			t.Fatal("operation signing material leaked", key)
		}
	}
	for _, mode := range []string{"missing-signature", "extra-query", "wrong-group", "wrong-region", "wrong-version", "wrong-subscription"} {
		candidate := *original
		q := candidate.Query()
		switch mode {
		case "missing-signature":
			q.Del("s")
		case "extra-query":
			q.Set("scope", "all")
		case "wrong-group":
			candidate.Path = strings.Replace(candidate.Path, "dataprotectionclitest-rg", "other", 1)
		case "wrong-region":
			candidate.Path = strings.Replace(candidate.Path, "centraluseuap", "eastus", 1)
		case "wrong-version":
			q.Set("api-version", dataProtectionVersion)
		case "wrong-subscription":
			candidate.Path = strings.Replace(candidate.Path, subscription, testSubscription, 1)
		}
		candidate.RawQuery = q.Encode()
		if _, err := c.dataProtectionPollURL(id, "centraluseuap", candidate.String(), "status_url"); err == nil {
			t.Fatal("unreviewed callback accepted", mode)
		}
	}
}
