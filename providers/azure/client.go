package azure

import (
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
	data   map[string]any
	header http.Header
	status int
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
	authContext := context.WithValue(context.Background(), oauth2.HTTPClient, &http.Client{Transport: transport, Timeout: 30 * time.Second, CheckRedirect: noRedirect})
	makeHTTP := func(scope string) *http.Client {
		config := clientcredentials.Config{ClientID: application, ClientSecret: secret,
			TokenURL: "https://login.microsoftonline.com/" + tenant + "/oauth2/v2.0/token",
			Scopes:   []string{scope}, AuthStyle: oauth2.AuthStyleInParams}
		return &http.Client{Transport: &oauth2.Transport{Base: transport, Source: config.TokenSource(authContext)}, Timeout: 60 * time.Second, CheckRedirect: noRedirect}
	}
	// Only the supplied service principal is used. No ambient Azure CLI, managed
	// identity, executable credential, or user-selected token endpoint is allowed.
	return &client{subscription: subscription, tenant: tenant, application: application,
		fingerprint: sha256.Sum256([]byte(subscription + "\x00" + tenant + "\x00" + application + "\x00" + secret)),
		http:        makeHTTP(armOrigin + "/.default"), storageHTTP: makeHTTP("https://storage.azure.com/.default")}, nil
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
	if err := c.validateURL(endpoint); err != nil {
		return response{}, err
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, nil)
	if err != nil {
		return response{}, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "steward/azure")
	res, err := c.http.Do(req)
	if err != nil {
		return response{}, transportError(ctx, err)
	}
	defer res.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(res.Body, (32<<20)+1))
	if err != nil || len(payload) > 32<<20 {
		return response{}, fmt.Errorf("Azure response could not be read")
	}
	out := response{data: map[string]any{}, header: res.Header, status: res.StatusCode}
	if len(payload) > 0 {
		decoder := json.NewDecoder(strings.NewReader(string(payload)))
		decoder.UseNumber()
		if decoder.Decode(&out.data) != nil {
			return response{}, apiError(res.StatusCode, "invalid_response", res.Header)
		}
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return response{}, apiError(res.StatusCode, text(object(out.data["error"])["code"]), res.Header)
	}
	if out.data == nil {
		if method != http.MethodDelete {
			return response{}, fmt.Errorf("Azure returned an invalid JSON object")
		}
		out.data = map[string]any{}
	}
	return out, nil
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
	return &contracts.ProviderCallError{Provider: execution.ProviderError{Category: category, Code: code, Message: contracts.SafeProviderValidationMessage}, RetryAfter: retryAfter(header)}
}
func retryAfter(header http.Header) time.Duration {
	seconds, err := strconv.Atoi(header.Get("Retry-After"))
	if err != nil || seconds < 1 {
		return 2 * time.Second
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
	if err := c.validateURL(endpoint); err != nil {
		return nil, "", err
	}
	u, _ := url.Parse(endpoint)
	if !strings.EqualFold(u.Path, collection) {
		return nil, "", fmt.Errorf("Azure pagination changed collection")
	}
	res, err := c.request(ctx, "GET", endpoint)
	if err != nil {
		return nil, "", err
	}
	items, ok := res.data["value"].([]any)
	if !ok {
		return nil, "", fmt.Errorf("Azure list response has no valid value array")
	}
	next := text(res.data["nextLink"])
	if next != "" {
		if err := c.validateURL(next); err != nil {
			return nil, "", err
		}
		nu, _ := url.Parse(next)
		if !strings.EqualFold(nu.Path, collection) || next == endpoint {
			return nil, "", fmt.Errorf("Azure pagination did not advance within its collection")
		}
	}
	return items, next, nil
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
func (c *client) resourceURL(kind resourceType, nativeID string) (string, error) {
	id, nativeType, err := parseID(nativeID)
	if err != nil || !strings.HasPrefix(id, c.root()+"/") || !strings.EqualFold(nativeType, kind.NativeType) {
		return "", fmt.Errorf("Azure resource identity does not match its subscription and type")
	}
	return apiURL(id, kind.Version), nil
}
