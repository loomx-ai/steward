package checkpoint

import (
	"context"
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func answer(t *testing.T, response Response) (*httptest.Server, *[]url.Values, *atomic.Int32) {
	t.Helper()
	var queries []url.Values
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		queries = append(queries, r.URL.Query())
		if r.URL.Path != "/v1/check/steward" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(response)
	}))
	t.Cleanup(server.Close)
	return server, &queries, &calls
}

func params(t *testing.T, endpoint string, environment map[string]string) Params {
	t.Helper()
	return Params{
		Version:   "0.4.1",
		OS:        "linux",
		Arch:      "amd64",
		Directory: t.TempDir(),
		Endpoint:  endpoint,
		Getenv:    func(name string) string { return environment[name] },
	}
}

func TestCheckReportsVersionPlatformAndSignature(t *testing.T) {
	server, queries, _ := answer(t, Response{Product: "steward", CurrentVersion: "0.5.0", Outdated: true})
	p := params(t, server.URL, nil)

	response, err := Check(context.Background(), p)
	if err != nil {
		t.Fatalf("Check() error: %v", err)
	}
	if !response.Outdated || response.CurrentVersion != "0.5.0" {
		t.Fatalf("Check() = %+v, want the newer version reported as outdated", response)
	}
	query := (*queries)[0]
	for field, want := range map[string]string{"version": "0.4.1", "os": "linux", "arch": "amd64"} {
		if got := query.Get(field); got != want {
			t.Errorf("query %s = %q, want %q", field, got, want)
		}
	}
	if !signaturePattern.MatchString(query.Get("signature")) {
		t.Errorf("signature %q is not a random UUID", query.Get("signature"))
	}
	// Nothing beyond those four fields may leave the machine.
	if len(query) != 4 {
		t.Errorf("request carried %v, want only version, os, arch, and signature", query)
	}

	info, err := os.Stat(filepath.Join(p.Directory, SignatureFileName))
	if err != nil {
		t.Fatalf("stat signature: %v", err)
	}
	if permissions := info.Mode().Perm(); permissions != fs.FileMode(0o600) {
		t.Errorf("signature file mode = %v, want 0600", permissions)
	}
}

func TestCheckReusesTheSignatureAndCachesTheAnswer(t *testing.T) {
	server, queries, calls := answer(t, Response{Product: "steward", CurrentVersion: "0.5.0"})
	p := params(t, server.URL, nil)

	if _, err := Check(context.Background(), p); err != nil {
		t.Fatalf("first Check() error: %v", err)
	}
	p.Now = func() time.Time { return time.Now().Add(23 * time.Hour) }
	if _, err := Check(context.Background(), p); err != nil {
		t.Fatalf("second Check() error: %v", err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("service was asked %d times within a day, want 1", got)
	}

	// A day later the answer is asked for again, with the same signature.
	p.Now = func() time.Time { return time.Now().Add(25 * time.Hour) }
	if _, err := Check(context.Background(), p); err != nil {
		t.Fatalf("third Check() error: %v", err)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("service was asked %d times, want 2", got)
	}
	if first, second := (*queries)[0].Get("signature"), (*queries)[1].Get("signature"); first != second {
		t.Errorf("signature changed between checks: %q then %q", first, second)
	}
}

func TestCheckIgnoresTheCacheAfterAnUpgrade(t *testing.T) {
	server, _, calls := answer(t, Response{Product: "steward", CurrentVersion: "0.5.0"})
	p := params(t, server.URL, nil)

	if _, err := Check(context.Background(), p); err != nil {
		t.Fatalf("Check() error: %v", err)
	}
	p.Version = "0.5.0"
	if _, err := Check(context.Background(), p); err != nil {
		t.Fatalf("Check() after upgrade error: %v", err)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("service was asked %d times, want 2: the cached answer described the replaced build", got)
	}
}

func TestCheckStopsWhenDisabled(t *testing.T) {
	for _, name := range []string{"STEWARD_CHECKPOINT_DISABLE", "DO_NOT_TRACK"} {
		t.Run(name, func(t *testing.T) {
			server, _, calls := answer(t, Response{Product: "steward"})
			p := params(t, server.URL, map[string]string{name: "1"})
			response, err := Check(context.Background(), p)
			if err != nil || response != nil {
				t.Fatalf("Check() = %v, %v; want no answer and no error", response, err)
			}
			if got := calls.Load(); got != 0 {
				t.Errorf("service was asked %d times while disabled", got)
			}
			if _, err := os.Stat(filepath.Join(p.Directory, SignatureFileName)); !os.IsNotExist(err) {
				t.Error("a disabled check still created a signature")
			}
		})
	}
	if !Disabled(func(string) string { return "true" }) {
		t.Error("Disabled() ignored a set variable")
	}
	if Disabled(func(string) string { return "0" }) {
		t.Error("Disabled() treated 0 as disabling")
	}
}

func TestCheckOmitsTheSignatureWhenItIsDisabled(t *testing.T) {
	server, queries, _ := answer(t, Response{Product: "steward"})
	p := params(t, server.URL, map[string]string{"STEWARD_CHECKPOINT_SIGNATURE_DISABLE": "1"})

	if _, err := Check(context.Background(), p); err != nil {
		t.Fatalf("Check() error: %v", err)
	}
	if (*queries)[0].Has("signature") {
		t.Error("request carried a signature although signatures are disabled")
	}
	if _, err := os.Stat(filepath.Join(p.Directory, SignatureFileName)); !os.IsNotExist(err) {
		t.Error("a signature file was written although signatures are disabled")
	}
}

func TestCheckHonoursTheEndpointVariable(t *testing.T) {
	server, _, calls := answer(t, Response{Product: "steward"})
	p := params(t, "", map[string]string{"STEWARD_CHECKPOINT_URL": server.URL})

	if _, err := Check(context.Background(), p); err != nil {
		t.Fatalf("Check() error: %v", err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("service was asked %d times, want 1", got)
	}
}

func TestCheckRejectsAnswersItCannotTrust(t *testing.T) {
	tests := []struct {
		name     string
		response Response
	}{
		{"another product", Response{Product: "vault", CurrentVersion: "0.5.0"}},
		{"unparseable version", Response{Product: "steward", CurrentVersion: "$(rm -rf /)"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server, _, _ := answer(t, test.response)
			if _, err := Check(context.Background(), params(t, server.URL, nil)); err == nil {
				t.Fatal("Check() accepted an answer it should have rejected")
			}
		})
	}

	t.Run("server error", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer server.Close()
		if _, err := Check(context.Background(), params(t, server.URL, nil)); err == nil {
			t.Fatal("Check() accepted a failed request")
		}
	})
}

func TestCheckStripsTerminalControlAndInsecureLinks(t *testing.T) {
	server, _, _ := answer(t, Response{
		Product:        "steward",
		CurrentVersion: "0.5.0",
		ProjectWebsite: "javascript:alert(1)",
		Alerts: []Alert{
			{ID: 1, Message: "upgrade now\x1b[2K\rSteward: everything is fine", URL: "http://example.invalid/a", Level: "critical"},
			{ID: 2, Message: "   ", URL: "https://example.invalid/b"},
		},
	})

	response, err := Check(context.Background(), params(t, server.URL, nil))
	if err != nil {
		t.Fatalf("Check() error: %v", err)
	}
	if response.ProjectWebsite != "" {
		t.Errorf("project website = %q, want a non-HTTPS link dropped", response.ProjectWebsite)
	}
	if len(response.Alerts) != 1 {
		t.Fatalf("kept %d alerts, want only the one with a message", len(response.Alerts))
	}
	if strings.ContainsAny(response.Alerts[0].Message, "\x1b\r\n") {
		t.Errorf("alert message %q still carries terminal control characters", response.Alerts[0].Message)
	}
	if response.Alerts[0].URL != "" {
		t.Errorf("alert URL = %q, want a non-HTTPS link dropped", response.Alerts[0].URL)
	}
}

func TestCheckReplacesAnUnusableSignature(t *testing.T) {
	server, queries, _ := answer(t, Response{Product: "steward"})
	p := params(t, server.URL, nil)
	path := filepath.Join(p.Directory, SignatureFileName)
	if err := os.WriteFile(path, []byte("not-a-uuid\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := Check(context.Background(), p); err != nil {
		t.Fatalf("Check() error: %v", err)
	}
	if !signaturePattern.MatchString((*queries)[0].Get("signature")) {
		t.Errorf("signature %q was not replaced", (*queries)[0].Get("signature"))
	}
}

func TestCheckWorksWithoutADataDirectory(t *testing.T) {
	server, queries, _ := answer(t, Response{Product: "steward", CurrentVersion: "0.5.0"})
	p := params(t, server.URL, nil)
	p.Directory = ""

	if _, err := Check(context.Background(), p); err != nil {
		t.Fatalf("Check() error: %v", err)
	}
	if (*queries)[0].Has("signature") {
		t.Error("request carried a signature although there is nowhere to keep one")
	}
}

func TestStartAlwaysAnswers(t *testing.T) {
	p := params(t, "https://checkpoint.invalid", nil)
	p.Timeout = 50 * time.Millisecond
	select {
	case response := <-Start(context.Background(), p):
		if response != nil {
			t.Fatalf("Start() = %+v, want nothing from an unreachable service", response)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Start() never answered")
	}
}
