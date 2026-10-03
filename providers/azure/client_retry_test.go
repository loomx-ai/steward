package azure

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

// Fixtures that answer inventory reads with 5xx or 429 should not wait out
// real backoff.
func init() { readRetryBase = time.Millisecond }

func TestIdempotentReadsRetryThrottlingAndTransientFailures(t *testing.T) {
	type reply struct {
		status     int
		retryAfter string
	}
	var mu sync.Mutex
	var replies []reply
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		calls++
		next := reply{status: 200}
		if len(replies) > 0 {
			next, replies = replies[0], replies[1:]
		}
		if next.status == 0 {
			// Drop the connection without a response.
			conn, _, _ := w.(http.Hijacker).Hijack()
			conn.Close()
			return
		}
		if next.retryAfter != "" {
			w.Header().Set("Retry-After", next.retryAfter)
		}
		w.WriteHeader(next.status)
		_, _ = w.Write([]byte(`{"id":"x"}`))
	}))
	defer server.Close()
	target, _ := url.Parse(server.URL)
	// Fresh connections keep net/http from replaying a dropped request itself.
	transport := &http.Transport{DisableKeepAlives: true}
	c := directClient(roundTripFunc(func(r *http.Request) (*http.Response, error) {
		r = r.Clone(r.Context())
		r.URL.Scheme, r.URL.Host, r.Host = target.Scheme, target.Host, ""
		return transport.RoundTrip(r)
	}))
	endpoint := apiURL(c.root()+"/resourcegroups/test", resourcesVersion)
	inventory := context.WithValue(context.Background(), inventoryReadContextKey{}, true)
	runAt := func(ctx context.Context, method string, queued ...reply) (int, error) {
		mu.Lock()
		replies, calls = queued, 0
		mu.Unlock()
		_, err := c.request(ctx, method, endpoint)
		mu.Lock()
		defer mu.Unlock()
		return calls, err
	}
	run := func(method string, queued ...reply) (int, error) { return runAt(inventory, method, queued...) }

	if calls, err := run("GET", reply{429, "0"}, reply{503, ""}, reply{0, ""}, reply{504, ""}); err != nil || calls != 5 {
		t.Fatalf("throttled read calls=%d error=%v", calls, err)
	}
	calls, err := run("GET", reply{502, ""}, reply{502, ""}, reply{502, ""}, reply{502, ""}, reply{502, ""}, reply{502, ""})
	var failure *contracts.ProviderCallError
	if calls != 5 || !errors.As(err, &failure) || failure.Provider.Category != execution.ErrorRetryable {
		t.Fatalf("exhausted retries calls=%d error=%v", calls, err)
	}
	// A Retry-After beyond the per-call budget fails now instead of waiting.
	if calls, err := run("GET", reply{429, "120"}); calls != 1 || !errors.As(err, &failure) || failure.Provider.Category != execution.ErrorThrottled {
		t.Fatalf("over-budget retry calls=%d error=%v", calls, err)
	}
	for _, method := range []string{"PUT", "POST", "PATCH", "DELETE"} {
		if calls, err := run(method, reply{429, "0"}); calls != 1 || err == nil {
			t.Fatalf("%s was retried: calls=%d error=%v", method, calls, err)
		}
	}
	// Cleanup checks and operation polls surface throttling to their callers.
	if calls, err := runAt(context.Background(), "GET", reply{429, "0"}); err == nil || calls != 1 {
		t.Fatalf("read outside inventory was retried: calls=%d error=%v", calls, err)
	}

	ctx, cancel := context.WithTimeout(inventory, 50*time.Millisecond)
	defer cancel()
	started := time.Now()
	if _, err := runAt(ctx, "GET", reply{429, "30"}); !errors.Is(err, context.DeadlineExceeded) || time.Since(started) > 5*time.Second {
		t.Fatalf("canceled retry wait error=%v after %s", err, time.Since(started))
	}
}
