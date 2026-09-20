package azure

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/credential/oauth"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

const (
	// The Azure CLI's own public client. A public client holds no secret; the
	// loopback redirect and PKCE are what bind the authorization to this host.
	oauthClientID = "04b07795-8ddb-461a-bbee-02f9e1bf7b46"
	// Personal Microsoft accounts cannot hold an Azure subscription, so the
	// authorization is restricted to work and school accounts, as az login does.
	oauthAuthority   = "https://login.microsoftonline.com"
	oauthCommonRealm = "organizations"
	oauthLabel       = "Microsoft Azure"
	subscriptionsAPI = "2022-12-01"
)

// oauthAudience is one token audience the provider reads. Azure issues a
// separate access token per audience, and the CLI client is not guaranteed to
// reach every one of them in every tenant, so only ARM is required: losing a
// data-plane audience degrades that inventory source rather than the connection.
type oauthAudience struct {
	key      string
	scope    string
	required bool
}

var oauthAudiences = []oauthAudience{
	{key: "arm", scope: armOrigin + "/.default", required: true},
	{key: "graph", scope: graphOrigin + "/.default"},
	{key: "storage", scope: "https://storage.azure.com/.default"},
	{key: "batch", scope: "https://batch.core.windows.net//.default"},
	{key: "communication", scope: "https://communication.azure.com/.default"},
	{key: "vault", scope: "https://vault.azure.net/.default"},
}

func oauthTokenKey(audience string) string  { return "oauth_token_" + audience }
func oauthExpireKey(audience string) string { return "oauth_token_" + audience + "_expire" }

type oauthDriver struct {
	httpClient *http.Client
	authority  string
	armOrigin  string
}

// NewOAuthDriver builds the Microsoft Azure half of a browser authorization.
func NewOAuthDriver() oauth.Driver {
	return newOAuthDriver(&http.Client{Timeout: 30 * time.Second, CheckRedirect: noRedirect}, "", "")
}

func newOAuthDriver(httpClient *http.Client, authority, arm string) *oauthDriver {
	if strings.TrimSpace(authority) == "" {
		authority = oauthAuthority
	}
	if strings.TrimSpace(arm) == "" {
		arm = armOrigin
	}
	return &oauthDriver{httpClient: httpClient, authority: strings.TrimRight(authority, "/"), armOrigin: strings.TrimRight(arm, "/")}
}

func (d *oauthDriver) Provider() asset.Provider { return asset.ProviderAzure }

// The Azure CLI's public client is registered for "http://localhost" exactly.
// Entra ID lets only the port vary, so anything else — the 127.0.0.1 literal or
// a trailing path — is refused with AADSTS50011. MSAL advertises the same name
// while listening on the IPv4 loopback.
func (d *oauthDriver) Callback() oauth.Callback { return oauth.Callback{Host: "localhost"} }

func (d *oauthDriver) Authorize(
	_ context.Context,
	_ map[string]string,
	request oauth.Request,
) (oauth.Authorization, error) {
	authorizationURL, err := url.Parse(d.authority + "/" + oauthCommonRealm + "/oauth2/v2.0/authorize")
	if err != nil {
		return oauth.Authorization{}, fmt.Errorf("build Azure OAuth authorization URL: %w", err)
	}
	query := authorizationURL.Query()
	query.Set("response_type", "code")
	query.Set("response_mode", "query")
	query.Set("client_id", oauthClientID)
	query.Set("redirect_uri", request.RedirectURI)
	query.Set("state", request.State)
	query.Set("code_challenge", request.CodeChallenge)
	query.Set("code_challenge_method", "S256")
	query.Set("scope", d.armOrigin+"/.default offline_access openid profile")
	authorizationURL.RawQuery = query.Encode()
	return oauth.Authorization{
		URL: authorizationURL.String(),
		Exchange: func(ctx context.Context, code string) (oauth.Session, error) {
			tokens, err := d.token(ctx, oauthCommonRealm, url.Values{
				"grant_type":    {"authorization_code"},
				"code":          {code},
				"redirect_uri":  {request.RedirectURI},
				"code_verifier": {request.CodeVerifier},
				"scope":         {d.armOrigin + "/.default offline_access openid profile"},
			})
			if err != nil {
				return nil, err
			}
			if strings.TrimSpace(tokens.RefreshToken) == "" {
				return nil, fmt.Errorf("Azure did not return a refresh token")
			}
			return &oauthSession{driver: d, tokens: tokens}, nil
		},
	}, nil
}

type oauthTokens struct {
	AccessToken  string
	RefreshToken string
	IDToken      string
	ExpiresAt    time.Time
}

func (d *oauthDriver) token(ctx context.Context, realm string, form url.Values) (oauthTokens, error) {
	form.Set("client_id", oauthClientID)
	endpoint := d.authority + "/" + url.PathEscape(realm) + "/oauth2/v2.0/token"
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return oauthTokens{}, fmt.Errorf("create Azure OAuth token request: %w", err)
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := d.httpClient.Do(request)
	if err != nil {
		return oauthTokens{}, fmt.Errorf("request Azure OAuth token: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return oauthTokens{}, fmt.Errorf("Azure OAuth token request failed with status %d", response.StatusCode)
	}
	var payload struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		IDToken      string `json:"id_token"`
		ExpiresIn    int64  `json:"expires_in"`
	}
	if err := decodeOAuthJSON(response.Body, &payload); err != nil {
		return oauthTokens{}, fmt.Errorf("decode Azure OAuth token response: %w", err)
	}
	if strings.TrimSpace(payload.AccessToken) == "" || payload.ExpiresIn <= 0 {
		return oauthTokens{}, fmt.Errorf("Azure OAuth token response is incomplete")
	}
	return oauthTokens{
		AccessToken:  payload.AccessToken,
		RefreshToken: payload.RefreshToken,
		IDToken:      payload.IDToken,
		ExpiresAt:    time.Now().UTC().Add(time.Duration(payload.ExpiresIn) * time.Second),
	}, nil
}

// armGet reads one ARM collection with an explicit bearer token, which the
// authorization needs before any client exists.
func (d *oauthDriver) armGet(ctx context.Context, accessToken, path string) ([]map[string]any, error) {
	endpoint := d.armOrigin + path + "?api-version=" + subscriptionsAPI
	values := []map[string]any{}
	for page := 0; page < 20 && endpoint != ""; page++ {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return nil, err
		}
		request.Header.Set("Authorization", "Bearer "+accessToken)
		response, err := d.httpClient.Do(request)
		if err != nil {
			return nil, fmt.Errorf("read Azure %s: %w", path, err)
		}
		var payload struct {
			Value    []map[string]any `json:"value"`
			NextLink string           `json:"nextLink"`
		}
		status := response.StatusCode
		err = decodeOAuthJSON(response.Body, &payload)
		_ = response.Body.Close()
		if status != http.StatusOK {
			return nil, fmt.Errorf("Azure %s request failed with status %d", path, status)
		}
		if err != nil {
			return nil, fmt.Errorf("decode Azure %s response: %w", path, err)
		}
		values = append(values, payload.Value...)
		endpoint = ""
		if strings.HasPrefix(payload.NextLink, d.armOrigin+"/") {
			endpoint = payload.NextLink
		}
	}
	return values, nil
}

// oauthSession holds one completed Azure authorization. The refresh token it
// received from the common realm can be redeemed against any tenant the
// identity belongs to, which is how the subscriptions are enumerated.
type oauthSession struct {
	driver  *oauthDriver
	tokens  oauthTokens
	targets []contracts.OAuthTarget
}

func (s *oauthSession) Targets(ctx context.Context) ([]contracts.OAuthTarget, error) {
	if s.targets != nil {
		return s.targets, nil
	}
	tenants, err := s.driver.armGet(ctx, s.tokens.AccessToken, "/tenants")
	if err != nil {
		return nil, err
	}
	targets := []contracts.OAuthTarget{}
	for _, tenant := range tenants {
		tenantID := strings.TrimSpace(text(tenant["tenantId"]))
		if !uuidPattern.MatchString(tenantID) {
			continue
		}
		// A tenant the identity cannot currently sign in to is skipped rather
		// than failing the whole listing: the others are still connectable.
		tenantTokens, err := s.driver.token(ctx, tenantID, url.Values{
			"grant_type":    {"refresh_token"},
			"refresh_token": {s.tokens.RefreshToken},
			"scope":         {s.driver.armOrigin + "/.default offline_access"},
		})
		if err != nil {
			continue
		}
		subscriptions, err := s.driver.armGet(ctx, tenantTokens.AccessToken, "/subscriptions")
		if err != nil {
			continue
		}
		tenantName := strings.TrimSpace(text(tenant["displayName"]))
		if tenantName == "" {
			tenantName = tenantID
		}
		for _, subscription := range subscriptions {
			id := strings.TrimSpace(text(subscription["subscriptionId"]))
			if !uuidPattern.MatchString(id) || !strings.EqualFold(text(subscription["state"]), "Enabled") {
				continue
			}
			name := strings.TrimSpace(text(subscription["displayName"]))
			if name == "" {
				name = id
			}
			targets = append(targets, contracts.OAuthTarget{
				ID: tenantID + "/" + id, Name: name, Description: tenantName,
			})
		}
	}
	s.targets = targets
	return targets, nil
}

func (s *oauthSession) Credential(ctx context.Context, targetID string) (contracts.Credential, error) {
	targets, err := s.Targets(ctx)
	if err != nil {
		return contracts.Credential{}, err
	}
	found := false
	for _, target := range targets {
		if target.ID == targetID {
			found = true
			break
		}
	}
	if !found {
		return contracts.Credential{}, oauth.FlowError(
			"oauth_target_invalid",
			"The selected Azure subscription is not available to this identity",
		)
	}
	tenantID, subscriptionID, _ := strings.Cut(targetID, "/")
	// The stored authorization is tenant scoped from here on: the connection
	// only ever reads one subscription, in one tenant.
	tenantTokens, err := s.driver.token(ctx, tenantID, url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {s.tokens.RefreshToken},
		"scope":         {s.driver.armOrigin + "/.default offline_access openid profile"},
	})
	if err != nil {
		return contracts.Credential{}, err
	}
	// A guest identity has a different object ID in each tenant, so the
	// connection's principal is read from the tenant-scoped token.
	principal := oauthPrincipal(tenantTokens.IDToken)
	if !uuidPattern.MatchString(principal) {
		return contracts.Credential{}, fmt.Errorf("Azure did not identify the authorized account")
	}
	values := map[string]string{
		"tenant_id":           tenantID,
		"subscription_id":     subscriptionID,
		"principal_id":        principal,
		oauth.RefreshTokenKey: tenantTokens.RefreshToken,
	}
	if strings.TrimSpace(tenantTokens.RefreshToken) == "" {
		values[oauth.RefreshTokenKey] = s.tokens.RefreshToken
	}
	refreshed, err := (&oauthRefresher{driver: s.driver}).audienceTokens(
		ctx, tenantID, values[oauth.RefreshTokenKey], tenantTokens,
	)
	if err != nil {
		return contracts.Credential{}, err
	}
	for key, value := range refreshed {
		values[key] = value
	}
	return contracts.Credential{Type: asset.CredentialAzureOAuth, Values: values}, nil
}

// oauthPrincipal reads the object ID from an ID token. The token came straight
// from the Entra ID token endpoint over TLS, so its claims are read, not
// trusted as an authorization decision.
func oauthPrincipal(idToken string) string {
	parts := strings.Split(idToken, ".")
	if len(parts) != 3 {
		return ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	var claims struct {
		ObjectID string `json:"oid"`
	}
	if json.Unmarshal(payload, &claims) != nil {
		return ""
	}
	return strings.TrimSpace(claims.ObjectID)
}

func decodeOAuthJSON(reader io.Reader, target any) error {
	return json.NewDecoder(io.LimitReader(reader, 4<<20)).Decode(target)
}
