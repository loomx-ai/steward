package gcp

import (
	"bytes"
	"context"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/provider/contracts"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/jwt"
)

const tokenURL = "https://oauth2.googleapis.com/token"

var projectPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{4,61}[a-z0-9]$|^[0-9]+$`)
var segmentPattern = regexp.MustCompile(`^[A-Za-z0-9_.:@-]+$`)

type client struct {
	http        *http.Client
	project     string
	number      string
	email       string
	fingerprint [32]byte
}

func newClient(credential contracts.Credential, transport http.RoundTripper) (*client, error) {
	invalid := func() (*client, error) {
		return nil, contracts.NewCredentialValidationError("credential_fields_invalid", "A valid Google service account JSON key and project ID are required.", nil)
	}
	if credential.Type != asset.CredentialGCPServiceAccount || (credential.ExpiresAt != nil && !credential.ExpiresAt.After(time.Now())) {
		return invalid()
	}
	raw := credential.Values["service_account_json"]
	if len(raw) > 64<<10 {
		return invalid()
	}
	var key struct {
		Type         string `json:"type"`
		Project      string `json:"project_id"`
		Email        string `json:"client_email"`
		PrivateKey   string `json:"private_key"`
		PrivateKeyID string `json:"private_key_id"`
		TokenURI     string `json:"token_uri"`
		Universe     string `json:"universe_domain"`
	}
	if json.Unmarshal([]byte(raw), &key) != nil || key.Type != "service_account" ||
		!strings.HasSuffix(key.Email, ".iam.gserviceaccount.com") || strings.Count(key.Email, "@") != 1 ||
		(key.TokenURI != "" && key.TokenURI != tokenURL && key.TokenURI != "https://accounts.google.com/o/oauth2/token") ||
		(key.Universe != "" && key.Universe != "googleapis.com") {
		return invalid()
	}
	project := strings.TrimSpace(credential.Values["project_id"])
	if project == "" {
		project = key.Project
	}
	if !projectPattern.MatchString(project) {
		return invalid()
	}
	block, rest := pem.Decode([]byte(key.PrivateKey))
	if block == nil || len(strings.TrimSpace(string(rest))) != 0 {
		return invalid()
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		parsed, err = x509.ParsePKCS1PrivateKey(block.Bytes)
	}
	rsaKey, rsaOK := parsed.(*rsa.PrivateKey)
	if err != nil || !rsaOK || rsaKey.N.BitLen() < 2048 {
		return invalid()
	}
	// The JWT package owns signing and token refresh. Credentials cannot choose a
	// token endpoint, credential file, executable, impersonation URL, or universe.
	config := jwt.Config{Email: key.Email, PrivateKey: []byte(key.PrivateKey), PrivateKeyID: key.PrivateKeyID, TokenURL: tokenURL, Scopes: []string{"https://www.googleapis.com/auth/cloud-platform"}}
	return &client{project: project, email: key.Email, fingerprint: sha256.Sum256([]byte(project + "\x00" + raw)), http: &http.Client{
		Transport: &tokenTransport{base: transport, config: config}, Timeout: 60 * time.Second, CheckRedirect: noRedirect,
	}}, nil
}

func noRedirect(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

// Token exchange uses the active request context, including cancellation. A
// cached client must never retain the context of its initial validation request.
type tokenTransport struct {
	base   http.RoundTripper
	config jwt.Config
	mu     sync.Mutex
	token  *oauth2.Token
}

func (t *tokenTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if err := request.Context().Err(); err != nil {
		return nil, err
	}
	t.mu.Lock()
	token := t.token
	t.mu.Unlock()
	if !token.Valid() {
		ctx, cancel := context.WithTimeout(request.Context(), 30*time.Second)
		defer cancel()
		// jwt.TokenSource currently calls Client.PostForm without a context.
		// Bind the actual exchange request at the transport boundary as well.
		httpClient := &http.Client{Transport: contextTransport{context: ctx, base: t.base}, CheckRedirect: noRedirect}
		ctx = context.WithValue(ctx, oauth2.HTTPClient, httpClient)
		var err error
		token, err = t.config.TokenSource(ctx).Token()
		if err != nil {
			return nil, err
		}
		if !token.Valid() {
			return nil, fmt.Errorf("Google OAuth response has no usable access token")
		}
		t.mu.Lock()
		t.token = token
		t.mu.Unlock()
	}
	clone := request.Clone(request.Context())
	token.SetAuthHeader(clone)
	return t.base.RoundTrip(clone)
}

type contextTransport struct {
	context context.Context
	base    http.RoundTripper
}

func (t contextTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return t.base.RoundTrip(request.Clone(t.context))
}

func (c *client) request(ctx context.Context, method, endpoint string, query url.Values) (map[string]any, error) {
	result, err := c.requestResult(ctx, method, endpoint, query, nil)
	return result.Data, err
}

func (c *client) requestResult(ctx context.Context, method, endpoint string, query url.Values, body []byte) (result contracts.InvocationResult, failure error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Port() != "" || u.Fragment != "" || !allowedHost(u.Hostname()) {
		return result, fmt.Errorf("invalid Google API endpoint")
	}
	if len(query) > 0 {
		merged := u.Query()
		for key, values := range query {
			merged[key] = values
		}
		u.RawQuery = merged.Encode()
	}
	requestLog := map[string]any{"method": method, "path": u.Path, "query": u.Query()}
	if len(body) > 0 {
		var value any
		if json.Unmarshal(body, &value) != nil {
			return result, fmt.Errorf("invalid Google API request body")
		}
		requestLog["body"] = value
	}
	execution.LogCloudAPIRequest(ctx, u.Host, method, safePayload(requestLog))
	defer func() {
		if failure != nil {
			execution.LogCloudAPIFailure(ctx, u.Host, method, failure)
		}
	}()
	req, err := http.NewRequestWithContext(ctx, method, u.String(), bytes.NewReader(body))
	if err != nil {
		return result, fmt.Errorf("invalid Google API request")
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "steward/gcp")
	if len(body) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}
	response, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		var tokenError *oauth2.RetrieveError
		if errors.As(err, &tokenError) && tokenError.Response != nil {
			status := tokenError.Response.StatusCode
			if status == 400 {
				status = http.StatusUnauthorized
			}
			return result, apiError(status, "token_exchange_failed", nil, tokenError.Response.Header.Get("Retry-After"))
		}
		return result, &contracts.ProviderCallError{Provider: execution.ProviderError{Category: execution.ErrorRetryable, Code: "transport_error", Message: contracts.SafeProviderTransportMessage}}
	}
	defer response.Body.Close()
	for _, header := range []string{"X-Goog-Request-Id", "X-Request-Id", "X-GUploader-UploadID"} {
		if value := response.Header.Get(header); value != "" {
			result.RequestID = value
			break
		}
	}
	// Attach provenance to every structured error, including malformed payloads.
	defer func() {
		var call *contracts.ProviderCallError
		if errors.As(failure, &call) {
			call.Provider.RequestID = result.RequestID
		}
	}()
	payload, err := io.ReadAll(io.LimitReader(response.Body, (32<<20)+1))
	if err != nil || len(payload) > 32<<20 {
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		return result, apiError(response.StatusCode, "response_unreadable", nil, response.Header.Get("Retry-After"))
	}
	data := map[string]any{}
	if len(bytes.TrimSpace(payload)) > 0 {
		decoder := json.NewDecoder(bytes.NewReader(payload))
		decoder.UseNumber()
		if decoder.Decode(&data) != nil {
			return result, apiError(response.StatusCode, "invalid_response", nil, response.Header.Get("Retry-After"))
		}
		var trailing any
		if decoder.Decode(&trailing) != io.EOF {
			return result, apiError(response.StatusCode, "invalid_response", nil, response.Header.Get("Retry-After"))
		}
	}
	if data == nil || (len(bytes.TrimSpace(payload)) == 0 && method == http.MethodGet && response.StatusCode >= 200 && response.StatusCode < 300) {
		if method != http.MethodDelete && response.StatusCode >= 200 && response.StatusCode < 300 {
			return result, apiError(response.StatusCode, "invalid_response", nil, "")
		}
		data = map[string]any{}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		detail, _ := data["error"].(map[string]any)
		execution.LogCloudAPIResponse(ctx, u.Host, method, map[string]any{"request_id": result.RequestID, "status_code": response.StatusCode})
		return result, apiError(response.StatusCode, text(detail["status"]), detail, response.Header.Get("Retry-After"))
	}
	execution.LogCloudAPIResponse(ctx, u.Host, method, safePayload(map[string]any{"request_id": result.RequestID, "status_code": response.StatusCode, "body": data}))
	result.Data = data
	result.NextToken = text(data["nextPageToken"])
	return result, nil
}

func allowedHost(host string) bool {
	switch host {
	case "cloudresourcemanager.googleapis.com", "cloudasset.googleapis.com", "compute.googleapis.com", "storage.googleapis.com", "pubsub.googleapis.com", "sqladmin.googleapis.com", "container.googleapis.com", "run.googleapis.com", "artifactregistry.googleapis.com", "secretmanager.googleapis.com":
		return true
	}
	return false
}

func apiError(status int, code string, detail map[string]any, retry string) error {
	category := execution.ErrorProviderFailure
	switch {
	case status == 404:
		category = execution.ErrorNotFound
	case status == 401 || status == 403:
		category = execution.ErrorPermissionDenied
	case status == 429:
		category = execution.ErrorThrottled
	case status == 409 || status == 412:
		category = execution.ErrorConflict
	case status >= 500:
		category = execution.ErrorRetryable
	case status >= 400:
		category = execution.ErrorInvalidRequest
	}
	if code == "" {
		code = strconv.Itoa(status)
	}
	seconds, err := strconv.Atoi(retry)
	if err != nil {
		if deadline, err := http.ParseTime(retry); err == nil {
			seconds = int(time.Until(deadline).Seconds())
		}
	}
	if seconds < 0 {
		seconds = 0
	}
	if seconds > 300 {
		seconds = 300
	}
	return &contracts.ProviderCallError{Provider: execution.ProviderError{Category: category, Code: code, Message: contracts.SafeProviderValidationMessage}, RetryAfter: time.Duration(seconds) * time.Second}
}

func isNotFound(err error) bool {
	var call *contracts.ProviderCallError
	return errors.As(err, &call) && call.Provider.Category == execution.ErrorNotFound
}
func text(value any) string           { s, _ := value.(string); return strings.TrimSpace(s) }
func object(value any) map[string]any { result, _ := value.(map[string]any); return result }
func array(value any) []any           { result, _ := value.([]any); return result }
func last(value string) string {
	parts := strings.Split(strings.TrimRight(value, "/"), "/")
	return parts[len(parts)-1]
}
