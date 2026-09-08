package gcp

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func apiResponse(request *http.Request, status int, payload string) *http.Response {
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {"application/json"}, "X-Goog-Request-Id": {"request-123"}}, Body: io.NopCloser(strings.NewReader(payload)), Request: request}
}

func testCredential(t *testing.T) (contracts.Credential, *rsa.PrivateKey) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(map[string]any{"type": "service_account", "project_id": "sample-project", "client_email": "steward@sample-project.iam.gserviceaccount.com", "private_key_id": "key-123", "private_key": string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: encoded})), "token_uri": tokenURL})
	if err != nil {
		t.Fatal(err)
	}
	return contracts.Credential{Type: asset.CredentialGCPServiceAccount, Values: map[string]string{"project_id": "sample-project", "service_account_json": string(payload)}}, key
}

func TestClientSignsExplicitCredentialAndCachesToken(t *testing.T) {
	credential, key := testCredential(t)
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", "/not-the-selected-credential.json")
	var tokens, products atomic.Int32
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.String() == tokenURL {
			tokens.Add(1)
			body, _ := io.ReadAll(request.Body)
			form, _ := url.ParseQuery(string(body))
			if form.Get("grant_type") != "urn:ietf:params:oauth:grant-type:jwt-bearer" {
				t.Fatal("wrong OAuth grant")
			}
			parts := strings.Split(form.Get("assertion"), ".")
			if len(parts) != 3 {
				t.Fatal("missing signed assertion")
			}
			signature, _ := base64.RawURLEncoding.DecodeString(parts[2])
			digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
			if rsa.VerifyPKCS1v15(&key.PublicKey, crypto.SHA256, digest[:], signature) != nil {
				t.Fatal("assertion did not use the explicit private key")
			}
			claimsRaw, _ := base64.RawURLEncoding.DecodeString(parts[1])
			var claims map[string]any
			if json.Unmarshal(claimsRaw, &claims) != nil || claims["aud"] != tokenURL || claims["iss"] != "steward@sample-project.iam.gserviceaccount.com" || claims["scope"] != "https://www.googleapis.com/auth/cloud-platform" {
				t.Fatalf("incorrect assertion claims: %s", claimsRaw)
			}
			return apiResponse(request, 200, `{"access_token":"test-token", "token_type":"Bearer", "expires_in":3600}`), nil
		}
		products.Add(1)
		if request.Header.Get("Authorization") != "Bearer test-token" {
			t.Fatal("product call lacks cached OAuth token")
		}
		return apiResponse(request, 200, `{"name":"test"}`), nil
	})
	c, err := newClient(credential, transport)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err := c.request(context.Background(), "GET", "https://compute.googleapis.com/compute/v1/projects/sample-project/regions", nil); err != nil {
			t.Fatal(err)
		}
	}
	if tokens.Load() != 1 || products.Load() != 2 {
		t.Fatalf("calls: token=%d product=%d", tokens.Load(), products.Load())
	}
}

func TestTokenExchangeCancellationAndCredentialValidation(t *testing.T) {
	credential, _ := testCredential(t)
	started := make(chan struct{})
	c, err := newClient(credential, roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.String() != tokenURL {
			t.Error("product API called before authentication")
		}
		close(started)
		<-request.Context().Done()
		return nil, request.Context().Err()
	}))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		_, err := c.request(ctx, "GET", "https://compute.googleapis.com/compute/v1/projects/sample-project/regions", nil)
		result <- err
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("token request did not start")
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation lost: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("token request ignored cancellation")
	}
	for _, mutation := range []func(*contracts.Credential){
		func(c *contracts.Credential) { c.Type = asset.CredentialAWSAccessKey },
		func(c *contracts.Credential) { past := time.Now().Add(-time.Minute); c.ExpiresAt = &past },
		func(c *contracts.Credential) { c.Values["project_id"] = "../../other" },
		func(c *contracts.Credential) {
			c.Values["service_account_json"] = strings.ReplaceAll(c.Values["service_account_json"], tokenURL, "https://untrusted.example/token")
		},
	} {
		candidate := credential
		candidate.Values = map[string]string{"project_id": credential.Values["project_id"], "service_account_json": credential.Values["service_account_json"]}
		mutation(&candidate)
		if _, err := newClient(candidate, http.DefaultTransport); err == nil {
			t.Fatal("invalid credential accepted")
		}
	}
}

func TestClientResponseProvenanceErrorsAndSecrets(t *testing.T) {
	var logs []execution.JobLogEntry
	ctx := execution.WithJobLogSink(context.Background(), execution.JobLogSinkFunc(func(_ context.Context, entry execution.JobLogEntry) { logs = append(logs, entry) }))
	payload := `{"name":"vm", "metadata":{"items":[{"key":"startup-script", "value":"PRIVATE_SCRIPT"}]}, "template":{"containers":[{"env":[{"name":"API_KEY", "value":"PRIVATE_ENV"}]}]}, "error":{"code":500,"message":"PRIVATE_ERROR"}}`
	c := &client{http: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) { return apiResponse(request, 200, payload), nil })}}
	result, err := c.requestResult(ctx, "GET", "https://compute.googleapis.com/compute/v1/projects/sample-project/regions", nil, nil)
	if err != nil || result.RequestID != "request-123" || result.Data["name"] != "vm" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	encoded, _ := json.Marshal(logs)
	for _, secret := range []string{"PRIVATE_SCRIPT", "PRIVATE_ENV", "PRIVATE_ERROR"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("secret leaked into logs: %s", secret)
		}
	}
	if len(logs) != 2 || logs[0].Kind != execution.JobLogCloudAPIRequest || logs[1].Kind != execution.JobLogCloudAPIResponse {
		t.Fatalf("missing API logs: %s", encoded)
	}
	if object(array(object(result.Data["metadata"])["items"])[0])["value"] != "PRIVATE_SCRIPT" {
		t.Fatal("log sanitizer mutated the live response")
	}
	for _, test := range []struct {
		status   int
		category execution.ErrorCategory
	}{
		{401, execution.ErrorPermissionDenied}, {403, execution.ErrorPermissionDenied}, {404, execution.ErrorNotFound},
		{409, execution.ErrorConflict}, {412, execution.ErrorConflict}, {429, execution.ErrorThrottled}, {503, execution.ErrorRetryable},
	} {
		t.Run(fmt.Sprint(test.status), func(t *testing.T) {
			logs = nil
			c.http.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
				response := apiResponse(request, test.status, `{"error":{"status":"API_FAILURE", "message":"PRIVATE_ERROR", "details":{"password":"PRIVATE_PASSWORD"}}}`)
				response.Header.Set("Retry-After", "7")
				return response, nil
			})
			_, err := c.request(ctx, "GET", "https://compute.googleapis.com/compute/v1/projects/sample-project/regions", nil)
			var call *contracts.ProviderCallError
			if !errors.As(err, &call) || call.Provider.Category != test.category || call.Provider.RequestID != "request-123" || call.RetryAfter != 7*time.Second {
				t.Fatalf("wrong error: %#v", err)
			}
			encoded, _ := json.Marshal(logs)
			if strings.Contains(string(encoded), "PRIVATE_") || strings.Contains(err.Error(), "PRIVATE_") {
				t.Fatal("provider error details leaked")
			}
		})
	}
}

func TestClientRejectsMalformedResponsesAndUntrustedEndpoints(t *testing.T) {
	var calls int
	c := &client{http: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls++
		return apiResponse(request, 200, `{}`), nil
	})}}
	for _, endpoint := range []string{"http://compute.googleapis.com/", "https://compute.googleapis.com.evil.example/", "https://compute.googleapis.com:8443/", "https://user:pass@compute.googleapis.com/", "https://compute.googleapis.com/#fragment"} {
		if _, err := c.request(context.Background(), "GET", endpoint, nil); err == nil {
			t.Fatalf("accepted %s", endpoint)
		}
	}
	if calls != 0 {
		t.Fatal("invalid endpoints reached transport")
	}
	for _, payload := range []string{"", "null", "[]", "{", `{} {"trailing":true}`} {
		c.http.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) { return apiResponse(request, 200, payload), nil })
		if _, err := c.request(context.Background(), "GET", "https://compute.googleapis.com/", nil); err == nil {
			t.Errorf("accepted GET body %q", payload)
		}
	}
	for _, payload := range []string{"", "null", `{}`} {
		c.http.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) { return apiResponse(request, 204, payload), nil })
		if _, err := c.request(context.Background(), "DELETE", "https://compute.googleapis.com/", nil); err != nil {
			t.Errorf("rejected empty deletion response: %v", err)
		}
	}
}
