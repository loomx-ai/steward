package azure

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

const testSubscription = "11111111-2222-4333-8444-555555555555"
const testTenant = "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
const testApplication = "12345678-1234-4234-8234-123456789012"

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func jsonResponse(status int, value any, headers http.Header) *http.Response {
	payload, _ := json.Marshal(value)
	if headers == nil {
		headers = http.Header{}
	}
	headers.Set("Content-Type", "application/json")
	return &http.Response{StatusCode: status, Header: headers, Body: io.NopCloser(strings.NewReader(string(payload)))}
}
func testCredential() contracts.Credential {
	return contracts.Credential{Type: asset.CredentialAzureServicePrincipal, Values: map[string]string{"subscription_id": testSubscription, "tenant_id": testTenant, "client_id": testApplication, "client_secret": "explicit-secret"}}
}
func directClient(handler roundTripFunc) *client {
	return &client{subscription: testSubscription, tenant: testTenant, application: testApplication, http: &http.Client{Transport: handler, CheckRedirect: noRedirect}, storageHTTP: &http.Client{Transport: handler, CheckRedirect: noRedirect}}
}

func TestExplicitOAuthAudienceSelectionAndTokenCache(t *testing.T) {
	t.Setenv("AZURE_CLIENT_SECRET", "ambient-must-not-be-used")
	var mu sync.Mutex
	counts := map[string]int{}
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host == "login.microsoftonline.com" {
			if r.URL.Path != "/"+testTenant+"/oauth2/v2.0/token" {
				t.Errorf("wrong tenant endpoint %s", r.URL)
			}
			if err := r.ParseForm(); err != nil {
				t.Fatal(err)
			}
			if r.Form.Get("client_id") != testApplication || r.Form.Get("client_secret") != "explicit-secret" || r.Form.Get("grant_type") != "client_credentials" {
				t.Error("wrong explicit credential selection")
			}
			scope := r.Form.Get("scope")
			mu.Lock()
			counts[scope]++
			mu.Unlock()
			return jsonResponse(200, map[string]any{"access_token": scope, "token_type": "Bearer", "expires_in": 3600}, nil), nil
		}
		audience := armOrigin + "/.default"
		if r.URL.Host == "storage.blob.core.windows.net" {
			audience = "https://storage.azure.com/.default"
		}
		if r.Header.Get("Authorization") != "Bearer "+audience {
			t.Error("token audience mismatch")
		}
		return jsonResponse(200, map[string]any{"id": "ok"}, nil), nil
	})
	c, err := newClient(testCredential(), transport)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err := c.request(context.Background(), "GET", apiURL(c.root(), "2022-12-01")); err != nil {
			t.Fatal(err)
		}
	}
	req, _ := http.NewRequestWithContext(context.Background(), "GET", "https://storage.blob.core.windows.net/test", nil)
	res, err := c.storageHTTP.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if counts[armOrigin+"/.default"] != 1 || counts["https://storage.azure.com/.default"] != 1 {
		t.Fatalf("exchanges=%v", counts)
	}
}

func TestOAuthRefreshUsesCurrentRequestCancellation(t *testing.T) {
	started := make(chan struct{})
	var calls atomic.Int32
	c, err := newClient(testCredential(), roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		close(started)
		<-r.Context().Done()
		return nil, r.Context().Err()
	}))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := c.request(ctx, "GET", apiURL(c.root(), "2022-12-01")); done <- err }()
	<-started
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("OAuth request ignored cancellation")
	}
	if calls.Load() != 1 {
		t.Fatal("unexpected token fallback")
	}
}

func TestCredentialValidationRejectsMissingExpiredAndForeignFields(t *testing.T) {
	for _, field := range []string{"subscription_id", "tenant_id", "client_id", "client_secret"} {
		cred := testCredential()
		cred.Values[field] = ""
		if _, err := newClient(cred, http.DefaultTransport); err == nil {
			t.Errorf("missing %s accepted", field)
		}
	}
	cred := testCredential()
	expired := time.Now().Add(-time.Minute)
	cred.ExpiresAt = &expired
	if _, err := newClient(cred, http.DefaultTransport); err == nil {
		t.Error("expired credential accepted")
	}
	cred = testCredential()
	cred.Type = asset.CredentialGCPServiceAccount
	if _, err := newClient(cred, http.DefaultTransport); err == nil {
		t.Error("wrong credential type accepted")
	}
}

func TestAPIErrorCategoriesRequestIDsAndRetry(t *testing.T) {
	for _, test := range []struct {
		status   int
		category execution.ErrorCategory
	}{{401, execution.ErrorPermissionDenied}, {403, execution.ErrorPermissionDenied}, {404, execution.ErrorNotFound}, {409, execution.ErrorConflict}, {412, execution.ErrorConflict}, {429, execution.ErrorThrottled}, {503, execution.ErrorRetryable}} {
		c := directClient(func(r *http.Request) (*http.Response, error) {
			return jsonResponse(test.status, map[string]any{"error": map[string]any{"code": "provider_code", "message": "do-not-leak-secret"}}, http.Header{"X-Ms-Request-Id": {"request-7"}, "Retry-After": {"17"}}), nil
		})
		_, err := c.request(context.Background(), "GET", apiURL(c.root(), resourcesVersion))
		var call *contracts.ProviderCallError
		if !errors.As(err, &call) || call.Provider.Category != test.category || call.Provider.RequestID != "request-7" || call.RetryAfter != 17*time.Second || strings.Contains(err.Error(), "do-not-leak") {
			t.Fatalf("status %d: %v", test.status, err)
		}
	}
	if retryAfter(http.Header{"Retry-After": {"9999"}}) != 300*time.Second || retryAfter(http.Header{"Retry-After": {"0"}}) != 0 {
		t.Fatal("retry bounds incorrect")
	}
}

func TestTransportRejectsMalformedObjectsAndForeignURLs(t *testing.T) {
	for _, body := range []string{"", "null", "[]", "{} {}", "not-json"} {
		c := directClient(func(r *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Header: http.Header{"X-Ms-Request-Id": {"invalid"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
		})
		if _, err := c.request(context.Background(), "GET", apiURL(c.root(), resourcesVersion)); err == nil {
			t.Errorf("accepted %q", body)
		}
	}
	c := directClient(func(r *http.Request) (*http.Response, error) {
		t.Error("foreign URL reached transport")
		return nil, errors.New("unexpected")
	})
	for _, endpoint := range []string{"http://management.azure.com" + c.root(), "https://management.azure.com.evil.invalid" + c.root(), armOrigin + "/subscriptions/" + testTenant, armOrigin + c.root() + "/../other", armOrigin + c.root() + "/%2e%2e/other"} {
		if _, err := c.request(context.Background(), "GET", endpoint); err == nil {
			t.Errorf("accepted %s", endpoint)
		}
	}
}

func TestCloudLogsAndResponsesRemoveValueSecrets(t *testing.T) {
	entries := []execution.JobLogEntry{}
	ctx := execution.WithJobLogSink(context.Background(), execution.JobLogSinkFunc(func(_ context.Context, e execution.JobLogEntry) { entries = append(entries, e) }))
	data := map[string]any{"id": "resource", "properties": map[string]any{"env": []any{map[string]any{"name": "ORDINARY", "value": "environment-secret"}}, "customData": "startup-secret", "provisioningState": "Succeeded"}}
	c := directClient(func(r *http.Request) (*http.Response, error) {
		return jsonResponse(200, data, http.Header{"X-Ms-Request-Id": {"request-safe"}}), nil
	})
	res, err := c.requestBody(ctx, "PATCH", apiURL(c.root(), resourcesVersion), []byte(`{"properties":{"clientSecret":"request-secret"}}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || res.requestID != "request-safe" {
		t.Fatalf("entries=%d, request=%s", len(entries), res.requestID)
	}
	encoded, _ := json.Marshal(entries)
	for _, secret := range []string{"environment-secret", "startup-secret", "request-secret"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("logs leaked %s", secret)
		}
	}
	if object(res.data["properties"])["customData"] != "startup-secret" {
		t.Fatal("live provider data mutated")
	}
	cleaned, _ := json.Marshal(safePayload(res.data))
	if strings.Contains(string(cleaned), "startup-secret") {
		t.Fatal("sanitized response leaked secret")
	}
}
