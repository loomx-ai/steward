package gcp

import (
	"context"
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
	// The Google Cloud CLI's own installed-application client. Google publishes
	// the secret with the CLI; an installed application cannot keep one, and the
	// loopback redirect plus PKCE are what actually protect the exchange.
	oauthClientID     = "32555940559.apps.googleusercontent.com"
	oauthClientSecret = "ZmssLNjJy2998hD4CTg2ejr2"
	oauthAuthorizeURL = "https://accounts.google.com/o/oauth2/auth"
	projectSearchURL  = "https://cloudresourcemanager.googleapis.com/v3/projects:search"
	userinfoURL       = "https://openidconnect.googleapis.com/v1/userinfo"
)

// oauthScopes is narrowed to what this provider reads. The CLI also asks for
// App Engine, Compute and Cloud SQL sign-in scopes that Steward never uses.
var oauthScopes = []string{
	"openid",
	"https://www.googleapis.com/auth/userinfo.email",
	"https://www.googleapis.com/auth/cloud-platform",
}

type oauthDriver struct {
	httpClient   *http.Client
	authorizeURL string
	tokenURL     string
	searchURL    string
	userinfoURL  string
}

// NewOAuthDriver builds the Google Cloud half of a browser authorization.
func NewOAuthDriver() oauth.Driver {
	return newOAuthDriver(&http.Client{Timeout: 30 * time.Second, CheckRedirect: noRedirect}, oauthEndpoints{})
}

// oauthEndpoints lets a test point the driver at its own server. An empty field
// keeps the real Google endpoint.
type oauthEndpoints struct {
	authorize string
	token     string
	search    string
	userinfo  string
}

func newOAuthDriver(httpClient *http.Client, endpoints oauthEndpoints) *oauthDriver {
	pick := func(override, fallback string) string {
		if strings.TrimSpace(override) != "" {
			return override
		}
		return fallback
	}
	return &oauthDriver{
		httpClient:   httpClient,
		authorizeURL: pick(endpoints.authorize, oauthAuthorizeURL),
		tokenURL:     pick(endpoints.token, tokenURL),
		searchURL:    pick(endpoints.search, projectSearchURL),
		userinfoURL:  pick(endpoints.userinfo, userinfoURL),
	}
}

func (d *oauthDriver) Provider() asset.Provider { return asset.ProviderGCP }

// Google matches a loopback redirect on everything but the port, so the
// authorization returns to the listener's root.
func (d *oauthDriver) Callback() oauth.Callback { return oauth.Callback{Path: "/"} }

func (d *oauthDriver) Authorize(
	_ context.Context,
	params map[string]string,
	request oauth.Request,
) (oauth.Authorization, error) {
	firewallParent := strings.TrimSpace(params["firewall_policy_parent"])
	if firewallParent != "" && !firewallContainerName(firewallParent) {
		return oauth.Authorization{}, oauth.FlowError(
			"invalid_oauth_parameters",
			"Firewall policy scope must be organizations/ID or folders/ID",
		)
	}
	identityParent := strings.TrimSpace(params["identity_group_parent"])
	if identityParent != "" && !identityParentValid(identityParent) {
		return oauth.Authorization{}, oauth.FlowError(
			"invalid_oauth_parameters",
			"Identity group scope must be customers/CUSTOMER_ID or identitysources/ID",
		)
	}
	authorizationURL, err := url.Parse(d.authorizeURL)
	if err != nil {
		return oauth.Authorization{}, fmt.Errorf("build Google Cloud OAuth authorization URL: %w", err)
	}
	query := authorizationURL.Query()
	query.Set("response_type", "code")
	query.Set("client_id", oauthClientID)
	query.Set("redirect_uri", request.RedirectURI)
	query.Set("state", request.State)
	query.Set("code_challenge", request.CodeChallenge)
	query.Set("code_challenge_method", "S256")
	query.Set("scope", strings.Join(oauthScopes, " "))
	// Without both of these Google issues no refresh token on a repeat
	// authorization, and the connection would stop working within the hour.
	query.Set("access_type", "offline")
	query.Set("prompt", "consent")
	authorizationURL.RawQuery = query.Encode()
	return oauth.Authorization{
		URL: authorizationURL.String(),
		Exchange: func(ctx context.Context, code string) (oauth.Session, error) {
			tokens, err := d.exchange(ctx, url.Values{
				"grant_type":    {"authorization_code"},
				"code":          {code},
				"redirect_uri":  {request.RedirectURI},
				"code_verifier": {request.CodeVerifier},
			})
			if err != nil {
				return nil, err
			}
			if strings.TrimSpace(tokens.RefreshToken) == "" {
				return nil, fmt.Errorf("Google Cloud did not return a refresh token")
			}
			// The connection's principal is the person who authorized it. A
			// service account key carries its own address; a browser
			// authorization has to be asked who it belongs to.
			email, err := d.userinfoEmail(ctx, tokens.AccessToken)
			if err != nil {
				return nil, err
			}
			return &oauthSession{
				driver:         d,
				tokens:         tokens,
				email:          email,
				firewallParent: firewallParent,
				identityParent: identityParent,
			}, nil
		},
	}, nil
}

func (d *oauthDriver) userinfoEmail(ctx context.Context, accessToken string) (string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, d.userinfoURL, nil)
	if err != nil {
		return "", err
	}
	request.Header.Set("Authorization", "Bearer "+accessToken)
	response, err := d.httpClient.Do(request)
	if err != nil {
		return "", fmt.Errorf("read Google Cloud account identity: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("Google Cloud account identity request failed with status %d", response.StatusCode)
	}
	var payload struct {
		Email string `json:"email"`
	}
	if err := decodeOAuthJSON(response.Body, &payload); err != nil {
		return "", fmt.Errorf("decode Google Cloud account identity: %w", err)
	}
	email := strings.TrimSpace(payload.Email)
	if email == "" || strings.Count(email, "@") != 1 {
		return "", fmt.Errorf("Google Cloud did not return an account address")
	}
	return email, nil
}

type oauthTokens struct {
	AccessToken  string
	RefreshToken string
	ExpiresAt    time.Time
}

func (d *oauthDriver) exchange(ctx context.Context, form url.Values) (oauthTokens, error) {
	form.Set("client_id", oauthClientID)
	form.Set("client_secret", oauthClientSecret)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, d.tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return oauthTokens{}, fmt.Errorf("create Google Cloud OAuth token request: %w", err)
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := d.httpClient.Do(request)
	if err != nil {
		return oauthTokens{}, fmt.Errorf("request Google Cloud OAuth token: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return oauthTokens{}, fmt.Errorf("Google Cloud OAuth token request failed with status %d", response.StatusCode)
	}
	var payload struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
	}
	if err := decodeOAuthJSON(response.Body, &payload); err != nil {
		return oauthTokens{}, fmt.Errorf("decode Google Cloud OAuth token response: %w", err)
	}
	if strings.TrimSpace(payload.AccessToken) == "" || payload.ExpiresIn <= 0 {
		return oauthTokens{}, fmt.Errorf("Google Cloud OAuth token response is incomplete")
	}
	return oauthTokens{
		AccessToken:  payload.AccessToken,
		RefreshToken: payload.RefreshToken,
		ExpiresAt:    time.Now().UTC().Add(time.Duration(payload.ExpiresIn) * time.Second),
	}, nil
}

// oauthSession holds one completed Google Cloud authorization. A Google
// identity usually reaches many projects, so the connection is not decided
// until the operator picks one.
type oauthSession struct {
	driver         *oauthDriver
	tokens         oauthTokens
	email          string
	firewallParent string
	identityParent string
	projects       []contracts.OAuthTarget
}

func (s *oauthSession) Targets(ctx context.Context) ([]contracts.OAuthTarget, error) {
	if s.projects != nil {
		return s.projects, nil
	}
	targets := []contracts.OAuthTarget{}
	pageToken := ""
	for page := 0; page < 20; page++ {
		search, err := url.Parse(s.driver.searchURL)
		if err != nil {
			return nil, err
		}
		query := search.Query()
		query.Set("pageSize", "1000")
		if pageToken != "" {
			query.Set("pageToken", pageToken)
		}
		search.RawQuery = query.Encode()
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, search.String(), nil)
		if err != nil {
			return nil, err
		}
		request.Header.Set("Authorization", "Bearer "+s.tokens.AccessToken)
		response, err := s.driver.httpClient.Do(request)
		if err != nil {
			return nil, fmt.Errorf("list Google Cloud projects: %w", err)
		}
		var payload struct {
			Projects []struct {
				ProjectID   string `json:"projectId"`
				DisplayName string `json:"displayName"`
				State       string `json:"state"`
			} `json:"projects"`
			NextPageToken string `json:"nextPageToken"`
		}
		status := response.StatusCode
		err = decodeOAuthJSON(response.Body, &payload)
		_ = response.Body.Close()
		if status != http.StatusOK {
			return nil, fmt.Errorf("Google Cloud project search failed with status %d", status)
		}
		if err != nil {
			return nil, fmt.Errorf("decode Google Cloud project search response: %w", err)
		}
		for _, project := range payload.Projects {
			if project.State != "ACTIVE" || !projectPattern.MatchString(project.ProjectID) {
				continue
			}
			name := project.DisplayName
			if strings.TrimSpace(name) == "" {
				name = project.ProjectID
			}
			targets = append(targets, contracts.OAuthTarget{
				ID: project.ProjectID, Name: name, Description: project.ProjectID,
			})
		}
		pageToken = payload.NextPageToken
		if pageToken == "" {
			break
		}
	}
	s.projects = targets
	return targets, nil
}

func (s *oauthSession) Credential(ctx context.Context, targetID string) (contracts.Credential, error) {
	targets, err := s.Targets(ctx)
	if err != nil {
		return contracts.Credential{}, err
	}
	// The project must be one this authorization actually reaches; a connection
	// pinned to a project the identity cannot see would only fail later.
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
			"The selected Google Cloud project is not available to this identity",
		)
	}
	values := map[string]string{
		"project_id":          targetID,
		"principal_email":     s.email,
		oauth.RefreshTokenKey: s.tokens.RefreshToken,
	}
	if s.firewallParent != "" {
		values["firewall_policy_parent"] = s.firewallParent
	}
	if s.identityParent != "" {
		values["identity_group_parent"] = s.identityParent
	}
	return contracts.Credential{Type: asset.CredentialGCPOAuth, Values: values}, nil
}

func decodeOAuthJSON(reader io.Reader, target any) error {
	return json.NewDecoder(io.LimitReader(reader, 4<<20)).Decode(target)
}
