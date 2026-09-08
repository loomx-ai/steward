package azure

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
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
	"golang.org/x/oauth2/clientcredentials"
)

const armOrigin = "https://management.azure.com"
const resourcesVersion = "2021-04-01"
const locksVersion = "2016-09-01"

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
var storageNamePattern = regexp.MustCompile(`^[a-z0-9]{3,24}$`)

type client struct {
	http, storageHTTP                 *http.Client
	subscription, tenant, application string
	fingerprint                       [32]byte
}
type response struct {
	data      map[string]any
	header    http.Header
	status    int
	requestID string
}

// ARM child responses may omit type. The full ID has already been bound to the
// expected native operation; a present type must still agree with that binding.
func validResourceResponse(res response, nativeID, nativeType string) bool {
	return res.status == http.StatusOK && res.data["error"] == nil &&
		strings.EqualFold(text(res.data["id"]), nativeID) &&
		(text(res.data["type"]) == "" || strings.EqualFold(text(res.data["type"]), nativeType))
}

func newClient(credential contracts.Credential, transport http.RoundTripper) (*client, error) {
	subscription := strings.ToLower(strings.TrimSpace(credential.Values["subscription_id"]))
	tenant := strings.ToLower(strings.TrimSpace(credential.Values["tenant_id"]))
	application := strings.ToLower(strings.TrimSpace(credential.Values["client_id"]))
	secret := credential.Values["client_secret"]
	if credential.Type != asset.CredentialAzureServicePrincipal ||
		!uuidPattern.MatchString(subscription) || !uuidPattern.MatchString(tenant) || !uuidPattern.MatchString(application) ||
		strings.TrimSpace(secret) == "" || len(secret) > 16<<10 ||
		(credential.ExpiresAt != nil && !credential.ExpiresAt.After(time.Now())) {
		return nil, contracts.NewCredentialValidationError("credential_fields_invalid", "A subscription ID, tenant ID, application ID, and client secret are required.", nil)
	}
	makeHTTP := func(scope string) *http.Client {
		config := clientcredentials.Config{ClientID: application, ClientSecret: secret,
			TokenURL: "https://login.microsoftonline.com/" + tenant + "/oauth2/v2.0/token",
			Scopes:   []string{scope}, AuthStyle: oauth2.AuthStyleInParams}
		return &http.Client{Transport: &tokenTransport{base: transport, config: config}, Timeout: 60 * time.Second, CheckRedirect: noRedirect}
	}
	// Only the supplied service principal is used. No ambient Azure CLI, managed
	// identity, executable credential, or user-selected token endpoint is allowed.
	return &client{subscription: subscription, tenant: tenant, application: application,
		fingerprint: sha256.Sum256([]byte(subscription + "\x00" + tenant + "\x00" + application + "\x00" + secret)),
		http:        makeHTTP(armOrigin + "/.default"), storageHTTP: makeHTTP("https://storage.azure.com/.default")}, nil
}

// Cache the token, while binding every refresh to the active request context.
// ARM and Storage use separate transports so tokens never cross audiences.
type tokenTransport struct {
	base   http.RoundTripper
	config clientcredentials.Config
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
		ctx = context.WithValue(ctx, oauth2.HTTPClient, &http.Client{Transport: t.base, CheckRedirect: noRedirect})
		var err error
		token, err = t.config.TokenSource(ctx).Token()
		if err != nil {
			return nil, err
		}
		if !token.Valid() {
			return nil, fmt.Errorf("Azure OAuth response has no usable access token")
		}
		t.mu.Lock()
		t.token = token
		t.mu.Unlock()
	}
	clone := request.Clone(request.Context())
	token.SetAuthHeader(clone)
	return t.base.RoundTrip(clone)
}
func noRedirect(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
func (c *client) root() string                        { return "/subscriptions/" + c.subscription }
func apiURL(path, version string) string {
	return armOrigin + path + "?api-version=" + url.QueryEscape(version)
}

func (c *client) validateURL(endpoint string) error {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "https" || u.Host != "management.azure.com" || u.User != nil || u.Fragment != "" ||
		(strings.ToLower(u.Path) != c.root() && !strings.HasPrefix(strings.ToLower(u.Path), c.root()+"/")) || strings.ContainsAny(u.Path, "\\\x00\r\n") {
		return fmt.Errorf("invalid Azure management endpoint")
	}
	for _, part := range strings.Split(u.Path, "/") {
		if part == "." || part == ".." {
			return fmt.Errorf("invalid Azure management path")
		}
	}
	return nil
}
func (c *client) request(ctx context.Context, method, endpoint string) (response, error) {
	return c.requestBody(ctx, method, endpoint, nil, nil)
}

func (c *client) requestBody(ctx context.Context, method, endpoint string, body []byte, headers map[string]string) (out response, failure error) {
	if err := c.validateURL(endpoint); err != nil {
		return response{}, err
	}
	u, _ := url.Parse(endpoint)
	requestLog := map[string]any{"method": method, "path": u.Path, "query": u.Query()}
	if len(body) > 0 {
		var value any
		if json.Unmarshal(body, &value) != nil {
			return out, fmt.Errorf("invalid Azure API request body")
		}
		requestLog["body"] = value
	}
	execution.LogCloudAPIRequest(ctx, u.Host, method, safePayload(requestLog))
	defer func() {
		if failure != nil {
			execution.LogCloudAPIFailure(ctx, u.Host, method, failure)
		}
	}()
	req, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(body))
	if err != nil {
		return response{}, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "steward/azure")
	if len(body) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	res, err := c.http.Do(req)
	if err != nil {
		return response{}, transportError(ctx, err)
	}
	defer res.Body.Close()
	out = response{data: map[string]any{}, header: res.Header, status: res.StatusCode, requestID: requestID(res.Header)}
	payload, err := io.ReadAll(io.LimitReader(res.Body, (32<<20)+1))
	if err != nil || len(payload) > 32<<20 {
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
		return out, apiError(res.StatusCode, "response_unreadable", res.Header)
	}
	if len(bytes.TrimSpace(payload)) > 0 {
		decoder := json.NewDecoder(bytes.NewReader(payload))
		decoder.UseNumber()
		if decoder.Decode(&out.data) != nil {
			return out, apiError(res.StatusCode, "invalid_response", res.Header)
		}
		var trailing any
		if decoder.Decode(&trailing) != io.EOF {
			return out, apiError(res.StatusCode, "invalid_response", res.Header)
		}
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		execution.LogCloudAPIResponse(ctx, u.Host, method, map[string]any{"request_id": out.requestID, "status_code": out.status})
		return out, apiError(res.StatusCode, text(object(out.data["error"])["code"]), res.Header)
	}
	if out.data == nil || len(bytes.TrimSpace(payload)) == 0 {
		// LRO Location polling may finish with 204, and an accepted operation
		// may return 202 without a body. An empty ordinary resource GET fails.
		if method == http.MethodGet && res.StatusCode != http.StatusAccepted && res.StatusCode != http.StatusNoContent {
			return out, apiError(res.StatusCode, "invalid_response", res.Header)
		}
		out.data = map[string]any{}
	}
	execution.LogCloudAPIResponse(ctx, u.Host, method, safePayload(map[string]any{"request_id": out.requestID, "status_code": out.status, "body": out.data}))
	return out, nil
}

func requestID(header http.Header) string {
	for _, name := range []string{"x-ms-request-id", "x-ms-correlation-request-id", "request-id"} {
		if value := header.Get(name); value != "" {
			return value
		}
	}
	return ""
}

func operationLocation(header http.Header) string {
	for _, name := range []string{"Azure-AsyncOperation", "Operation-Location", "Location"} {
		if value := header.Get(name); value != "" {
			return value
		}
	}
	return ""
}
func transportError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	var tokenError *oauth2.RetrieveError
	if errors.As(err, &tokenError) && tokenError.Response != nil {
		status := tokenError.Response.StatusCode
		if status == 400 {
			status = http.StatusUnauthorized
		}
		return apiError(status, "token_exchange_failed", tokenError.Response.Header)
	}
	return &contracts.ProviderCallError{Provider: execution.ProviderError{Category: execution.ErrorRetryable, Code: "transport_error", Message: contracts.SafeProviderTransportMessage}}
}
func apiError(status int, code string, header http.Header) error {
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
	if code == "ScopeLocked" {
		category = execution.ErrorProtected
	}
	return &contracts.ProviderCallError{Provider: execution.ProviderError{Category: category, Code: code, Message: contracts.SafeProviderValidationMessage, RequestID: requestID(header)}, RetryAfter: retryAfter(header)}
}
func retryAfter(header http.Header) time.Duration {
	seconds, err := strconv.Atoi(header.Get("Retry-After"))
	if err != nil {
		if deadline, parseErr := http.ParseTime(header.Get("Retry-After")); parseErr == nil {
			seconds = int(time.Until(deadline).Seconds())
		} else {
			return 2 * time.Second
		}
	}
	if seconds < 0 {
		seconds = 0
	}
	if seconds > 300 {
		seconds = 300
	}
	return time.Duration(seconds) * time.Second
}
func isNotFound(err error) bool {
	var call *contracts.ProviderCallError
	return errors.As(err, &call) && call.Provider.Category == execution.ErrorNotFound
}
func (c *client) listPage(ctx context.Context, endpoint, collection string) ([]any, string, error) {
	items, next, _, err := c.listPageResult(ctx, endpoint, collection)
	return items, next, err
}
func (c *client) listPageResult(ctx context.Context, endpoint, collection string) ([]any, string, response, error) {
	if err := c.validateURL(endpoint); err != nil {
		return nil, "", response{}, err
	}
	u, _ := url.Parse(endpoint)
	if !strings.EqualFold(u.Path, collection) {
		return nil, "", response{}, fmt.Errorf("Azure pagination changed collection")
	}
	res, err := c.request(ctx, "GET", endpoint)
	if err != nil {
		return nil, "", response{}, err
	}
	if res.status != http.StatusOK || res.data["error"] != nil {
		return nil, "", response{}, fmt.Errorf("Azure list response is incomplete")
	}
	items, ok := res.data["value"].([]any)
	if !ok {
		return nil, "", response{}, fmt.Errorf("Azure list response has no valid value array")
	}
	next := ""
	if value, present := res.data["nextLink"]; present && value != nil {
		var valid bool
		next, valid = value.(string)
		if !valid {
			return nil, "", response{}, fmt.Errorf("Azure list nextLink is not a string")
		}
	}
	if next != "" {
		if err := c.validateURL(next); err != nil {
			return nil, "", response{}, err
		}
		nu, _ := url.Parse(next)
		if !strings.EqualFold(nu.Path, collection) || next == endpoint || nu.Query().Get("api-version") != u.Query().Get("api-version") {
			return nil, "", response{}, fmt.Errorf("Azure pagination did not advance within its collection")
		}
	}
	return items, next, res, nil
}
func (c *client) listAll(ctx context.Context, path, version string) ([]any, error) {
	var items []any
	next := apiURL(path, version)
	seen := map[string]bool{}
	for next != "" {
		if seen[next] {
			return nil, fmt.Errorf("Azure pagination repeated a page")
		}
		seen[next] = true
		page, cursor, err := c.listPage(ctx, next, path)
		if err != nil {
			return nil, err
		}
		items = append(items, page...)
		next = cursor
	}
	return items, nil
}
func text(value any) string           { s, _ := value.(string); return strings.TrimSpace(s) }
func object(value any) map[string]any { result, _ := value.(map[string]any); return result }
func array(value any) []any           { result, _ := value.([]any); return result }
func last(value string) string {
	parts := strings.Split(strings.TrimRight(value, "/"), "/")
	return parts[len(parts)-1]
}

// ARM identities are case-insensitive; the full ID retains subscription and
// resource-group boundaries. Resource URLs never come from an arbitrary host.
func parseID(value string) (string, string, error) {
	id := strings.ToLower(strings.TrimSpace(value))
	if !strings.HasPrefix(id, "/subscriptions/") || strings.ContainsAny(id, "%?#\\\x00\r\n") {
		return "", "", fmt.Errorf("invalid Azure resource ID")
	}
	parts := strings.Split(strings.TrimPrefix(id, "/"), "/")
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return "", "", fmt.Errorf("invalid Azure resource path")
		}
	}
	if len(parts) < 4 || !uuidPattern.MatchString(parts[1]) || parts[2] != "resourcegroups" {
		return "", "", fmt.Errorf("invalid Azure resource scope")
	}
	if len(parts) == 4 {
		return id, "microsoft.resources/resourcegroups", nil
	}
	if len(parts) < 8 || parts[4] != "providers" || len(parts)%2 != 0 {
		return "", "", fmt.Errorf("invalid Azure resource type")
	}
	providerIndex := 4
	for i := 4; i+3 < len(parts); i += 2 {
		if parts[i] == "providers" {
			providerIndex = i
		}
	}
	kind := parts[providerIndex+1]
	for i := providerIndex + 2; i < len(parts); i += 2 {
		kind += "/" + parts[i]
	}
	return id, kind, nil
}
