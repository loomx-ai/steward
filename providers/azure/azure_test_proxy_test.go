package azure

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const azureTestProxyVersion = "20260521.2"

// Optional, independent Microsoft SDK playback process. This helper accepts
// only public, already-sanitized fixtures; it never starts a recording session.
// Production clients keep their origin validation and OAuth transport. Only
// the final test transport redirects requests onto the owned loopback listener.
type azureTestProxy struct {
	base    *url.URL
	session string
	http    *http.Client
	calls   atomic.Int64
}

type azureTestProxyEntry struct {
	RequestUri      string
	RequestMethod   string
	RequestHeaders  map[string][]string
	RequestBody     any
	StatusCode      int
	ResponseHeaders map[string][]string
	ResponseBody    any
}

func startAzureTestProxy(t *testing.T, entries []azureTestProxyEntry) *azureTestProxy {
	t.Helper()
	binary := os.Getenv("STEWARD_AZURE_TEST_PROXY")
	if binary == "" {
		t.Skip("set STEWARD_AZURE_TEST_PROXY to the pinned standalone Azure SDK Test Proxy executable")
	}
	version, err := exec.CommandContext(t.Context(), binary, "--version").Output()
	if err != nil || strings.TrimSpace(string(version)) != azureTestProxyVersion {
		t.Fatal("unexpected Azure SDK Test Proxy version; see fixtures/test-proxy/README.md")
	}
	directory := t.TempDir()
	recording := filepath.Join(directory, "public-recording.json")
	wire, err := json.Marshal(map[string]any{"Entries": entries, "Variables": map[string]string{}})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(recording, wire, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	command := exec.CommandContext(ctx, binary, "start", "-l", directory, "--", "--urls", "http://127.0.0.1:0")
	// Public fixture only: default substitutions would hide host/subscription
	// regressions. Never use this unsanitized configuration for live recording.
	for _, value := range os.Environ() {
		if !strings.HasPrefix(value, "TEST_PROXY_DISABLE_DEFAULT_SANITIZERS=") {
			command.Env = append(command.Env, value)
		}
	}
	command.Env = append(command.Env, "TEST_PROXY_DISABLE_DEFAULT_SANITIZERS=true")
	output, err := command.StdoutPipe()
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	command.Stderr = command.Stdout
	if err = command.Start(); err != nil {
		cancel()
		t.Fatal(err)
	}
	ready := make(chan *url.URL, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		scanner := bufio.NewScanner(output)
		scanner.Buffer(make([]byte, 4096), 1<<20)
		announced := false
		for scanner.Scan() {
			line := scanner.Text()
			_, address, found := strings.Cut(line, "Now listening on: ")
			if found && !announced {
				u, err := url.Parse(strings.TrimSpace(address))
				if err == nil && u.Scheme == "http" && u.Hostname() == "127.0.0.1" && u.Port() != "" {
					ready <- u
					announced = true
				}
			}
			// Do not publish the server's mismatch diagnostics or signed query values.
		}
		if scanner.Err() != nil {
			_ = command.Process.Kill()
		}
		_ = command.Wait()
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("owned test proxy did not terminate")
		}
	})
	var base *url.URL
	select {
	case base = <-ready:
	case <-done:
		t.Fatal("test proxy exited before opening its loopback listener")
	case <-time.After(20 * time.Second):
		t.Fatal("test proxy startup timed out")
	}
	transport := &http.Transport{Proxy: nil}
	p := &azureTestProxy{base: base, http: &http.Client{Transport: transport, Timeout: 10 * time.Second, CheckRedirect: noRedirect}}
	t.Cleanup(transport.CloseIdleConnections)
	// The extracted upstream fixture did not retain request headers. Exclude
	// only the client's standard headers; URL, method and body remain strict.
	p.control(t, "/Admin/SetMatcher", map[string]any{"compareBodies": true, "excludedHeaders": "Accept,Accept-Encoding,Connection,User-Agent,Content-Length,x-ms-client-request-id", "ignoredQueryOrdering": false}, map[string]string{"x-abstraction-identifier": "CustomDefaultMatcher"})
	response := p.control(t, "/playback/start", map[string]any{"x-recording-file": recording}, nil)
	p.session = response.Get("x-recording-id")
	if p.session == "" {
		t.Fatal("test proxy did not return a playback session")
	}
	t.Cleanup(func() { p.control(t, "/playback/stop", nil, map[string]string{"x-recording-id": p.session}) })
	return p
}

func (p *azureTestProxy) control(t *testing.T, path string, body any, headers map[string]string) http.Header {
	t.Helper()
	var reader io.Reader
	if body != nil {
		wire, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = strings.NewReader(string(wire))
	}
	request, err := http.NewRequest("POST", p.base.String()+path, reader)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	response, err := p.http.Do(request)
	if err != nil {
		t.Fatal("test proxy control request failed")
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, response.Body)
	if response.StatusCode != 200 {
		t.Fatalf("test proxy control %s returned %d", path, response.StatusCode)
	}
	return response.Header
}

func (p *azureTestProxy) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.URL.Scheme != "https" || request.URL.User != nil || request.URL.Host == "" {
		return nil, fmt.Errorf("invalid test playback origin")
	}
	clone := request.Clone(request.Context())
	target := *request.URL
	target.Scheme = p.base.Scheme
	target.Host = p.base.Host
	clone.URL = &target
	clone.Host = ""
	clone.Header.Del("Authorization") // Even the synthetic OAuth token stays in-process.
	clone.Header.Set("x-recording-id", p.session)
	clone.Header.Set("x-recording-mode", "playback")
	clone.Header.Set("x-recording-upstream-base-uri", request.URL.Scheme+"://"+request.URL.Host)
	p.calls.Add(1)
	return p.http.Do(clone)
}
