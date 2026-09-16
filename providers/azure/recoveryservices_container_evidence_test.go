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

// Preserve historical wire evidence. Only the outgoing result request is bound
// to today's selected catalog version, following the official CLI algorithm.
// This is protocol adaptation coverage, not a current-version wire recording.
func TestRecoveryContainerRecordedUnregisterProtocol(t *testing.T) {
	wire, err := os.ReadFile("fixtures/recoveryservices/container-unregister-recording.json")
	if err != nil {
		t.Fatal(err)
	}
	var rows []struct {
		Index       int
		Method, URL string
		SourceURL   string `json:"source_url"`
		SourceSHA   string `json:"source_sha256"`
		Response    struct {
			Body    struct{ String string }
			Headers map[string][]string
			Status  struct{ Code int }
		}
	}
	if err = json.Unmarshal(wire, &rows); err != nil || len(rows) != 18 {
		t.Fatal("missing recording", err)
	}
	first, err := url.Parse(rows[0].URL)
	if err != nil {
		t.Fatal(err)
	}
	id := strings.ToLower(first.Path)
	subscription := strings.Split(id, "/")[2]
	next := 1
	endpoint := ""
	runtime := protocolRuntime(t, func(q *http.Request) (*http.Response, error) {
		if next >= len(rows) || q.Method != "GET" || q.URL.String() != endpoint {
			t.Fatal("unexpected native poll")
		}
		row := rows[next]
		next++
		h := http.Header{}
		for k, values := range row.Response.Headers {
			for _, value := range values {
				h.Add(k, value)
			}
		}
		return &http.Response{StatusCode: row.Response.Status.Code, Header: h, Body: io.NopCloser(strings.NewReader(row.Response.Body.String))}, nil
	})
	c, err := runtime.resolve(t.Context(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	c.subscription = subscription
	h := http.Header{}
	for k, values := range rows[0].Response.Headers {
		for _, value := range values {
			h.Add(k, value)
		}
	}
	saved, err := c.recoveryContainerDeleteReceipt(id, response{status: rows[0].Response.Status.Code, header: h}, rows[0].Response.Body.String == "")
	if err != nil {
		t.Fatal("recorded acknowledgement rejected", err)
	}
	endpoint = text(saved["poll_url"])
	for i, row := range rows {
		if row.Index != 133+i || !strings.Contains(row.SourceURL, "79fa7556f80f3edce83676e10a2d0952776b7022") || len(row.SourceSHA) != 64 {
			t.Fatal("lost provenance")
		}
		parsed, err := url.Parse(row.URL)
		if err != nil || parsed.Query().Get("api-version") != "2023-04-01" {
			t.Fatal("historical version changed", err)
		}
		if i == 0 {
			if row.Method != "DELETE" {
				t.Fatal("missing unregister")
			}
			continue
		}
		if row.Method != "GET" {
			t.Fatal("unexpected recorded mutation")
		}
		if !strings.EqualFold(parsed.Path[:strings.LastIndex(parsed.Path, "/")], id+"/operationResults") {
			t.Fatal("recorded result belongs to another container")
		}
		expected := apiURL(id+"/operationResults/"+last(parsed.Path), recoveryServicesBackupVersion)
		if expected != endpoint {
			t.Fatal("recorded poll owner or operation changed")
		}
		saved, _, err = c.recoveryContainerPoll(t.Context(), id, saved)
		if err != nil || saved["operation_done"] != (i == len(rows)-1) {
			t.Fatal("recorded protocol failed", i, err)
		}
	}
	if next != len(rows) {
		t.Fatal("recording not consumed")
	}
}

func TestRecoveryContainerOfficialContracts(t *testing.T) {
	metadata, err := providerData()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"ProtectionContainers_Unregister", "ProtectionContainerOperationResults_Get"} {
		wire, err := os.ReadFile("fixtures/recoveryservices/" + name + ".json")
		if err != nil {
			t.Fatal(err)
		}
		var example map[string]any
		if err = json.Unmarshal(wire, &example); err != nil {
			t.Fatal(err)
		}
		op, ok := metadata.catalog.Operation("Azure.Microsoft.RecoveryServices." + name)
		if !ok {
			t.Fatal("missing native operation")
		}
		params := object(example["parameters"])
		bound, err := bindAzureREST(op, params)
		if err != nil {
			t.Fatal("official parameters rejected", err)
		}
		u, err := url.Parse(bound.URL)
		if err != nil || u.Query().Get("api-version") != recoveryServicesBackupVersion || len(bound.Body) != 0 {
			t.Fatal("native contract changed", err)
		}
		if name == "ProtectionContainerOperationResults_Get" {
			if bound.Method != "GET" || !strings.HasSuffix(u.Path, "/operationResults/"+text(params["operationId"])) {
				t.Fatal("wrong result operation")
			}
			continue
		}
		if bound.Method != "DELETE" {
			t.Fatal("unregister is not native DELETE")
		}
		h := http.Header{}
		values := object(object(object(example["responses"])["202"])["headers"])
		h.Set("Location", text(values["Location"]))
		h.Set("Azure-AsyncOperation", text(values["Azure-AsyncOperation"]))
		c := &client{subscription: text(params["subscriptionId"])}
		// The unchanged current-version example uses testRg in the request and
		// test-rg in its callback. They are different groups, not case variants.
		if _, err = c.recoveryContainerPollHeaders(strings.ToLower(u.Path), h); err == nil {
			t.Fatal("example's foreign resource group accepted")
		}
	}
}
