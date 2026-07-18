package alicloud

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
)

type oauthTokens struct {
	AccessToken  string
	RefreshToken string
	ExpiresAt    time.Time
}

type oauthSTS struct {
	AccessKeyID     string
	AccessKeySecret string
	SecurityToken   string
	ExpiresAt       time.Time
}

type oauthAPI interface {
	AuthorizationURL(asset.ConnectionSite, string, string, string) (string, error)
	ExchangeAuthorizationCode(context.Context, asset.ConnectionSite, string, string, string) (oauthTokens, error)
	Refresh(context.Context, asset.ConnectionSite, string) (oauthTokens, error)
	ExchangeSTS(context.Context, asset.ConnectionSite, string) (oauthSTS, error)
}

type oauthSiteConfig struct {
	clientID     string
	oauthBaseURL string
	signInURL    string
}

type oauthClient struct {
	httpClient *http.Client
	now        func() time.Time
	sites      map[asset.ConnectionSite]oauthSiteConfig
}

func newOAuthClient(httpClient *http.Client, now func() time.Time) *oauthClient {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	if now == nil {
		now = time.Now
	}
	return &oauthClient{
		httpClient: httpClient,
		now:        now,
		sites: map[asset.ConnectionSite]oauthSiteConfig{
			asset.ConnectionSiteCN: {
				clientID:     "4038181954557748008",
				oauthBaseURL: "https://oauth.aliyun.com",
				signInURL:    "https://signin.aliyun.com",
			},
			asset.ConnectionSiteINTL: {
				clientID:     "4103531455503354461",
				oauthBaseURL: "https://oauth.alibabacloud.com",
				signInURL:    "https://signin.alibabacloud.com",
			},
		},
	}
}

func (c *oauthClient) AuthorizationURL(
	site asset.ConnectionSite,
	redirectURI string,
	state string,
	challenge string,
) (string, error) {
	config, err := c.site(site)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(redirectURI) == "" || strings.TrimSpace(state) == "" || strings.TrimSpace(challenge) == "" {
		return "", fmt.Errorf("OAuth redirect URI, state, and PKCE challenge are required")
	}
	authorizationURL, err := url.Parse(strings.TrimRight(config.signInURL, "/") + "/oauth2/v1/auth")
	if err != nil {
		return "", fmt.Errorf("build Alibaba Cloud OAuth authorization URL: %w", err)
	}
	query := authorizationURL.Query()
	query.Set("response_type", "code")
	query.Set("client_id", config.clientID)
	query.Set("redirect_uri", redirectURI)
	query.Set("state", state)
	query.Set("code_challenge", challenge)
	query.Set("code_challenge_method", "S256")
	authorizationURL.RawQuery = query.Encode()
	return authorizationURL.String(), nil
}

func (c *oauthClient) ExchangeAuthorizationCode(
	ctx context.Context,
	site asset.ConnectionSite,
	code string,
	redirectURI string,
	codeVerifier string,
) (oauthTokens, error) {
	if strings.TrimSpace(code) == "" || strings.TrimSpace(redirectURI) == "" || strings.TrimSpace(codeVerifier) == "" {
		return oauthTokens{}, fmt.Errorf("OAuth authorization code, redirect URI, and code verifier are required")
	}
	return c.exchangeToken(ctx, site, url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {redirectURI},
		"code_verifier": {codeVerifier},
	})
}

func (c *oauthClient) Refresh(
	ctx context.Context,
	site asset.ConnectionSite,
	refreshToken string,
) (oauthTokens, error) {
	if strings.TrimSpace(refreshToken) == "" {
		return oauthTokens{}, fmt.Errorf("OAuth refresh token is required")
	}
	return c.exchangeToken(ctx, site, url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
	})
}

func (c *oauthClient) exchangeToken(
	ctx context.Context,
	site asset.ConnectionSite,
	form url.Values,
) (oauthTokens, error) {
	config, err := c.site(site)
	if err != nil {
		return oauthTokens{}, err
	}
	form.Set("client_id", config.clientID)
	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		strings.TrimRight(config.oauthBaseURL, "/")+"/v1/token",
		strings.NewReader(form.Encode()),
	)
	if err != nil {
		return oauthTokens{}, fmt.Errorf("create Alibaba Cloud OAuth token request: %w", err)
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := c.httpClient.Do(request)
	if err != nil {
		return oauthTokens{}, fmt.Errorf("request Alibaba Cloud OAuth token: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return oauthTokens{}, fmt.Errorf("Alibaba Cloud OAuth token request failed with status %d", response.StatusCode)
	}
	var payload struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
	}
	if err := decodeOAuthJSON(response.Body, &payload); err != nil {
		return oauthTokens{}, fmt.Errorf("decode Alibaba Cloud OAuth token response: %w", err)
	}
	if strings.TrimSpace(payload.AccessToken) == "" || payload.ExpiresIn <= 0 {
		return oauthTokens{}, fmt.Errorf("Alibaba Cloud OAuth token response is incomplete")
	}
	return oauthTokens{
		AccessToken:  payload.AccessToken,
		RefreshToken: payload.RefreshToken,
		ExpiresAt:    c.now().UTC().Add(time.Duration(payload.ExpiresIn) * time.Second),
	}, nil
}

func (c *oauthClient) ExchangeSTS(
	ctx context.Context,
	site asset.ConnectionSite,
	accessToken string,
) (oauthSTS, error) {
	config, err := c.site(site)
	if err != nil {
		return oauthSTS{}, err
	}
	if strings.TrimSpace(accessToken) == "" {
		return oauthSTS{}, fmt.Errorf("OAuth access token is required")
	}
	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		strings.TrimRight(config.oauthBaseURL, "/")+"/v1/exchange",
		nil,
	)
	if err != nil {
		return oauthSTS{}, fmt.Errorf("create Alibaba Cloud OAuth STS request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+accessToken)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", "aliyun-cli")
	response, err := c.httpClient.Do(request)
	if err != nil {
		return oauthSTS{}, fmt.Errorf("request Alibaba Cloud OAuth STS: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return oauthSTS{}, fmt.Errorf("Alibaba Cloud OAuth STS request failed with status %d", response.StatusCode)
	}
	var payload struct {
		AccessKeyID     string `json:"accessKeyId"`
		AccessKeySecret string `json:"accessKeySecret"`
		SecurityToken   string `json:"securityToken"`
		Expiration      string `json:"expiration"`
	}
	if err := decodeOAuthJSON(response.Body, &payload); err != nil {
		return oauthSTS{}, fmt.Errorf("decode Alibaba Cloud OAuth STS response: %w", err)
	}
	expiresAt, err := time.Parse(time.RFC3339, payload.Expiration)
	if err != nil {
		return oauthSTS{}, fmt.Errorf("Alibaba Cloud OAuth STS expiration is invalid")
	}
	if strings.TrimSpace(payload.AccessKeyID) == "" ||
		strings.TrimSpace(payload.AccessKeySecret) == "" ||
		strings.TrimSpace(payload.SecurityToken) == "" {
		return oauthSTS{}, fmt.Errorf("Alibaba Cloud OAuth STS response is incomplete")
	}
	return oauthSTS{
		AccessKeyID:     payload.AccessKeyID,
		AccessKeySecret: payload.AccessKeySecret,
		SecurityToken:   payload.SecurityToken,
		ExpiresAt:       expiresAt,
	}, nil
}

func (c *oauthClient) site(site asset.ConnectionSite) (oauthSiteConfig, error) {
	config, ok := c.sites[site]
	if !ok {
		return oauthSiteConfig{}, fmt.Errorf("Alibaba Cloud OAuth site %q is unsupported", site)
	}
	return config, nil
}

func decodeOAuthJSON(reader io.Reader, target any) error {
	return json.NewDecoder(io.LimitReader(reader, 1<<20)).Decode(target)
}
