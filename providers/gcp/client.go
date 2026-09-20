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
	"github.com/loomx-ai/steward/internal/credential/oauth"
	"github.com/loomx-ai/steward/internal/provider/contracts"
	"github.com/loomx-ai/steward/internal/workloadidentity"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/jwt"
)

const tokenURL = "https://oauth2.googleapis.com/token"

var projectPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{4,61}[a-z0-9]$|^[0-9]+$`)
var segmentPattern = regexp.MustCompile(`^[A-Za-z0-9_.:@()-]+$`)

type client struct {
	http           *http.Client
	project        string
	number         string
	email          string
	firewallParent string
	identityParent string
	fingerprint    [32]byte
}

func newClient(credential contracts.Credential, transport http.RoundTripper) (*client, error) {
	invalid := func() (*client, error) {
		return nil, contracts.NewCredentialValidationError("credential_fields_invalid", "A valid Google service account JSON key and project ID are required.", nil)
	}
	if credential.Type == asset.CredentialOIDC {
		if credential.Dynamic == nil {
			return invalid()
		}
		if err := workloadidentity.ValidateConfig(asset.ProviderGCP, credential); err != nil {
			return nil, err
		}
		v := credential.Values
		if !projectPattern.MatchString(v["project_id"]) || (v["firewall_policy_parent"] != "" && !firewallContainerName(v["firewall_policy_parent"])) || (v["identity_group_parent"] != "" && !identityParentValid(v["identity_group_parent"])) {
			return invalid()
		}
		return &client{project: v["project_id"], email: v["service_account_email"], firewallParent: v["firewall_policy_parent"], identityParent: v["identity_group_parent"], fingerprint: sha256.Sum256([]byte(credential.Dynamic.Key)), http: &http.Client{Transport: &workloadidentity.Transport{Base: transport, Credential: credential.Dynamic}, Timeout: 60 * time.Second, CheckRedirect: noRedirect}}, nil
	}
	if credential.ExpiresAt != nil && !credential.ExpiresAt.After(time.Now()) {
		return invalid()
	}
	if credential.Type == asset.CredentialGCPOAuth {
		return oauthClient(credential, transport)
	}
	if credential.Type != asset.CredentialGCPServiceAccount {
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
	firewallParent := strings.TrimSpace(credential.Values["firewall_policy_parent"])
	if firewallParent != "" && !firewallContainerName(firewallParent) {
		return nil, contracts.NewCredentialValidationError("credential_fields_invalid", "Firewall policy scope must be organizations/ID or folders/ID.", nil)
	}
	identityParent := strings.TrimSpace(credential.Values["identity_group_parent"])
	if identityParent != "" && !identityParentValid(identityParent) {
		return nil, contracts.NewCredentialValidationError("credential_fields_invalid", "Identity group scope must be customers/CUSTOMER_ID or identitysources/ID.", nil)
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
	config := jwt.Config{Email: key.Email, PrivateKey: []byte(key.PrivateKey), PrivateKeyID: key.PrivateKeyID, TokenURL: tokenURL, Scopes: []string{"https://www.googleapis.com/auth/cloud-platform", "https://www.googleapis.com/auth/userinfo.email"}}
	return &client{project: project, email: key.Email, firewallParent: firewallParent, identityParent: identityParent, fingerprint: sha256.Sum256([]byte(project + "\x00" + raw + "\x00" + firewallParent + "\x00" + identityParent)), http: &http.Client{
		Transport: &tokenTransport{base: transport, source: config.TokenSource}, Timeout: 60 * time.Second, CheckRedirect: noRedirect,
	}}, nil
}

// oauthClient builds a client for a browser authorization. Google does not
// rotate an installed application's refresh token, so nothing has to be written
// back: the token source exchanges it for a fresh access token whenever the
// cached one expires, exactly as the service account path does.
func oauthClient(credential contracts.Credential, transport http.RoundTripper) (*client, error) {
	invalid := func() (*client, error) {
		return nil, contracts.NewCredentialValidationError(
			"credential_fields_invalid",
			"A Google Cloud browser authorization and project ID are required.",
			nil,
		)
	}
	values := credential.Values
	refreshToken := strings.TrimSpace(values[oauth.RefreshTokenKey])
	project := strings.TrimSpace(values["project_id"])
	email := strings.TrimSpace(values["principal_email"])
	if refreshToken == "" || len(refreshToken) > 8<<10 ||
		!projectPattern.MatchString(project) || strings.Count(email, "@") != 1 {
		return invalid()
	}
	firewallParent := strings.TrimSpace(values["firewall_policy_parent"])
	if firewallParent != "" && !firewallContainerName(firewallParent) {
		return nil, contracts.NewCredentialValidationError("credential_fields_invalid", "Firewall policy scope must be organizations/ID or folders/ID.", nil)
	}
	identityParent := strings.TrimSpace(values["identity_group_parent"])
	if identityParent != "" && !identityParentValid(identityParent) {
		return nil, contracts.NewCredentialValidationError("credential_fields_invalid", "Identity group scope must be customers/CUSTOMER_ID or identitysources/ID.", nil)
	}
	config := oauth2.Config{
		ClientID:     oauthClientID,
		ClientSecret: oauthClientSecret,
		Endpoint:     oauth2.Endpoint{TokenURL: tokenURL, AuthStyle: oauth2.AuthStyleInParams},
		Scopes:       oauthScopes,
	}
	source := func(ctx context.Context) oauth2.TokenSource {
		return config.TokenSource(ctx, &oauth2.Token{RefreshToken: refreshToken})
	}
	return &client{
		project:        project,
		email:          email,
		firewallParent: firewallParent,
		identityParent: identityParent,
		fingerprint:    sha256.Sum256([]byte(project + "\x00" + email + "\x00" + refreshToken + "\x00" + firewallParent + "\x00" + identityParent)),
		http: &http.Client{
			Transport: &tokenTransport{base: transport, source: source}, Timeout: 60 * time.Second, CheckRedirect: noRedirect,
		},
	}, nil
}

func noRedirect(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

// Token exchange uses the active request context, including cancellation. A
// cached client must never retain the context of its initial validation request.
type tokenTransport struct {
	base   http.RoundTripper
	source func(context.Context) oauth2.TokenSource
	mu     sync.Mutex
	token  *oauth2.Token
}

func (t *tokenTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	token, err := t.accessToken(request.Context())
	if err != nil {
		return nil, err
	}
	clone := request.Clone(request.Context())
	token.SetAuthHeader(clone)
	return t.base.RoundTrip(clone)
}

func (t *tokenTransport) accessToken(ctx context.Context) (*oauth2.Token, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	t.mu.Lock()
	token := t.token
	t.mu.Unlock()
	if !token.Valid() {
		ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		// The oauth2 token sources call Client.PostForm without a context.
		// Bind the actual exchange request at the transport boundary as well.
		httpClient := &http.Client{Transport: contextTransport{context: ctx, base: t.base}, CheckRedirect: noRedirect}
		ctx = context.WithValue(ctx, oauth2.HTTPClient, httpClient)
		var err error
		token, err = t.source(ctx).Token()
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
	return token, nil
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
	if err != nil || u.Scheme != "https" || u.User != nil || u.Port() != "" || u.Fragment != "" || (!allowedHost(u.Hostname()) && !(discoveryAPIHost(u.Hostname()) && allowedHost(discoveryHost))) {
		return result, fmt.Errorf("invalid Google API endpoint")
	}
	if err := c.discoveryEndpoint(u); err != nil {
		return result, err
	}
	if len(query) > 0 {
		merged := u.Query()
		for key, values := range query {
			merged[key] = values
		}
		u.RawQuery = merged.Encode()
	}
	sanitize := safePayload
	if u.Host == infraHost {
		sanitize = safeInfraPayload
	}
	if u.Host == fusionHost {
		sanitize = safeFusionPayload
	}
	if u.Host == tpuHost {
		sanitize = safeTPUPayload
	}
	if discoveryAPIHost(u.Host) {
		sanitize = safeDiscoveryPayload
	}
	return requestJSON(ctx, c.http, method, u, body, sanitize)
}

func requestJSON(ctx context.Context, httpClient *http.Client, method string, u *url.URL, body []byte, sanitize func(map[string]any) map[string]any) (result contracts.InvocationResult, failure error) {
	requestLog := map[string]any{"method": method, "path": u.Path, "query": u.Query()}
	if len(body) > 0 {
		var value any
		if json.Unmarshal(body, &value) != nil {
			return result, fmt.Errorf("invalid Google API request body")
		}
		requestLog["body"] = value
	}
	execution.LogCloudAPIRequest(ctx, u.Host, method, sanitize(requestLog))
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
	response, err := httpClient.Do(req)
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
	for _, header := range []string{"X-Goog-Request-Id", "X-Request-Id", "X-GUploader-UploadID", "Audit-Id"} {
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
			call.Cause = googleResponseStatus(response.StatusCode)
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
		code := text(detail["status"])
		if data["kind"] == "Status" {
			code = text(data["reason"])
		}
		execution.LogCloudAPIResponse(ctx, u.Host, method, map[string]any{"request_id": result.RequestID, "status_code": response.StatusCode})
		return result, apiError(response.StatusCode, code, detail, response.Header.Get("Retry-After"))
	}
	if method == http.MethodGet && response.StatusCode != http.StatusOK {
		return result, apiError(response.StatusCode, "incomplete_response", nil, "")
	}
	if (u.Host == "dataform.googleapis.com" || u.Host == "batch.googleapis.com" || u.Host == "dataproc.googleapis.com" || discoveryAPIHost(u.Host) || u.Host == identityHost) && response.StatusCode != http.StatusOK {
		return result, apiError(response.StatusCode, "unexpected_native_response", nil, "")
	}
	if _, present := data["error"]; (u.Host == "dataform.googleapis.com" || u.Host == "batch.googleapis.com" || u.Host == "dataproc.googleapis.com") && present {
		return result, apiError(response.StatusCode, "native_error_response", nil, "")
	}
	execution.LogCloudAPIResponse(ctx, u.Host, method, sanitize(map[string]any{"request_id": result.RequestID, "status_code": response.StatusCode, "body": data}))
	result.Data = data
	result.NextToken = text(data["nextPageToken"])
	return result, nil
}

func allowedHost(host string) bool {
	metadata, err := providerData()
	return err == nil && metadata.hosts[host]
}

// googleResponseStatus distinguishes an actual resource response from OAuth errors.
type googleResponseStatus int

func (s googleResponseStatus) Error() string { return "Google HTTP status " + strconv.Itoa(int(s)) }

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
	// Compute reports a deletion blocked by a user of the resource as a 400
	// with this reason; retrying cannot succeed until that user is removed.
	for _, item := range array(detail["errors"]) {
		if reason := text(object(item)["reason"]); reason == "resourceInUseByAnotherResource" {
			category, code = execution.ErrorDependencyViolation, reason
		}
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
