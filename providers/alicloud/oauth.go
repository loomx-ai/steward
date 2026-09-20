package alicloud

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/credential/oauth"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

const (
	oauthSiteKey      = "site"
	oauthSTSExpireKey = "sts_expiration"
	oauthLabel        = "Alibaba Cloud"
)

// oauthDriver authorizes an Alibaba Cloud account through the browser. The site
// is chosen before the browser opens because the China and international
// consoles are separate OAuth issuers with their own client registrations.
type oauthDriver struct {
	api oauthAPI
}

// NewOAuthDriver builds the Alibaba Cloud half of a browser authorization.
func NewOAuthDriver() oauth.Driver {
	return &oauthDriver{api: newOAuthClient(&http.Client{Timeout: 30 * time.Second}, time.Now)}
}

func (d *oauthDriver) Provider() asset.Provider { return asset.ProviderAliCloud }

// The Alibaba Cloud CLI's registered client redirects to this path.
func (d *oauthDriver) Callback() oauth.Callback { return oauth.Callback{Path: "/cli/callback"} }

func (d *oauthDriver) Authorize(
	_ context.Context,
	params map[string]string,
	request oauth.Request,
) (oauth.Authorization, error) {
	site := asset.ConnectionSite(strings.TrimSpace(params[oauthSiteKey]))
	if site != asset.ConnectionSiteCN && site != asset.ConnectionSiteINTL {
		return oauth.Authorization{}, oauth.FlowError("invalid_connection_site", "Alibaba Cloud site is invalid")
	}
	authorizationURL, err := d.api.AuthorizationURL(site, request.RedirectURI, request.State, request.CodeChallenge)
	if err != nil {
		return oauth.Authorization{}, err
	}
	return oauth.Authorization{
		URL: authorizationURL,
		Exchange: func(ctx context.Context, code string) (oauth.Session, error) {
			tokens, err := d.api.ExchangeAuthorizationCode(ctx, site, code, request.RedirectURI, request.CodeVerifier)
			if err != nil {
				return nil, err
			}
			sts, err := d.api.ExchangeSTS(ctx, site, tokens.AccessToken)
			if err != nil {
				return nil, err
			}
			return &oauthSession{site: site, tokens: tokens, sts: sts}, nil
		},
	}, nil
}

// oauthSession holds one completed Alibaba Cloud authorization. The site the
// operator picked already names the only scope this authorization reaches, so
// it offers no targets.
type oauthSession struct {
	site   asset.ConnectionSite
	tokens oauthTokens
	sts    oauthSTS
}

func (s *oauthSession) Targets(context.Context) ([]contracts.OAuthTarget, error) {
	return nil, nil
}

func (s *oauthSession) Credential(_ context.Context, targetID string) (contracts.Credential, error) {
	if strings.TrimSpace(targetID) != "" {
		return contracts.Credential{}, oauth.FlowError(
			"oauth_target_invalid",
			"An Alibaba Cloud authorization does not select a cloud scope",
		)
	}
	return contracts.Credential{
		Type: asset.CredentialAliCloudOAuth,
		Values: map[string]string{
			oauthSiteKey:               string(s.site),
			oauth.AccessTokenKey:       s.tokens.AccessToken,
			oauth.RefreshTokenKey:      s.tokens.RefreshToken,
			oauth.AccessTokenExpireKey: oauth.FormatUnix(s.tokens.ExpiresAt),
			"access_key_id":            s.sts.AccessKeyID,
			"access_key_secret":        s.sts.AccessKeySecret,
			"security_token":           s.sts.SecurityToken,
			oauthSTSExpireKey:          oauth.FormatUnix(s.sts.ExpiresAt),
		},
	}, nil
}

// oauthRefresher renews a stored Alibaba Cloud authorization. Alibaba Cloud
// rotates the refresh token on every renewal, so the materializer's
// compare-and-swap is what keeps two workers from losing each other's token.
type oauthRefresher struct {
	api oauthAPI
}

func (r *oauthRefresher) CredentialType() asset.CredentialType {
	return asset.CredentialAliCloudOAuth
}

func (r *oauthRefresher) Usable(
	snapshot contracts.Credential,
	now time.Time,
) (contracts.Credential, bool, error) {
	if err := validateOAuthSite(snapshot); err != nil {
		return contracts.Credential{}, false, err
	}
	expiresAt, err := oauth.ParseUnix(snapshot.Values, oauthSTSExpireKey)
	if err != nil {
		return contracts.Credential{}, false, oauth.InvalidCredential(oauthLabel, err)
	}
	if !validOAuthSTS(snapshot.Values, expiresAt, now) {
		return contracts.Credential{}, false, nil
	}
	return oauthSTSCredential(snapshot, expiresAt), true, nil
}

func (r *oauthRefresher) Renew(
	ctx context.Context,
	snapshot contracts.Credential,
	now time.Time,
) (oauth.Renewal, error) {
	if err := validateOAuthSite(snapshot); err != nil {
		return oauth.Renewal{}, err
	}
	if r.api == nil {
		return oauth.Renewal{}, oauth.RefreshFailed(oauthLabel, nil)
	}
	values := oauth.CloneValues(snapshot.Values)
	accessExpiresAt, err := oauth.ParseUnix(values, oauth.AccessTokenExpireKey)
	if err != nil {
		return oauth.Renewal{}, oauth.InvalidCredential(oauthLabel, err)
	}
	accessToken := strings.TrimSpace(values[oauth.AccessTokenKey])
	if accessToken == "" || oauth.ExpiresSoon(accessExpiresAt, now) {
		refreshToken := strings.TrimSpace(values[oauth.RefreshTokenKey])
		if refreshToken == "" {
			return oauth.Renewal{}, oauth.ReauthenticationRequired(oauthLabel, nil)
		}
		refreshed, err := r.api.Refresh(ctx, snapshot.Site, refreshToken)
		if err != nil {
			return oauth.Renewal{}, oauth.RefreshFailed(oauthLabel, err)
		}
		accessToken = strings.TrimSpace(refreshed.AccessToken)
		values[oauth.AccessTokenKey] = accessToken
		values[oauth.AccessTokenExpireKey] = oauth.FormatUnix(refreshed.ExpiresAt)
		if rotated := strings.TrimSpace(refreshed.RefreshToken); rotated != "" {
			values[oauth.RefreshTokenKey] = rotated
		}
	}
	sts, err := r.api.ExchangeSTS(ctx, snapshot.Site, accessToken)
	if err != nil {
		return oauth.Renewal{}, contracts.NewCredentialValidationError(
			"oauth_sts_exchange_failed",
			"Alibaba Cloud OAuth could not exchange the access token for a temporary credential.",
			err,
		)
	}
	values["access_key_id"] = sts.AccessKeyID
	values["access_key_secret"] = sts.AccessKeySecret
	values["security_token"] = sts.SecurityToken
	values[oauthSTSExpireKey] = oauth.FormatUnix(sts.ExpiresAt)
	return oauth.Renewal{
		Values: values,
		Usable: func(stored contracts.Credential) contracts.Credential {
			return oauthSTSCredential(stored, sts.ExpiresAt)
		},
	}, nil
}

func validateOAuthSite(value contracts.Credential) error {
	storedSite := asset.ConnectionSite(strings.TrimSpace(value.Values[oauthSiteKey]))
	if value.Site == "" || storedSite == "" || storedSite != value.Site {
		return contracts.NewCredentialValidationError(
			"oauth_site_mismatch",
			"The Alibaba Cloud OAuth authorization belongs to a different site.",
			nil,
		)
	}
	return nil
}

func validOAuthSTS(values map[string]string, expiresAt, now time.Time) bool {
	return !oauth.ExpiresSoon(expiresAt, now) &&
		oauth.Present(values, "access_key_id", "access_key_secret", "security_token")
}

func oauthSTSCredential(snapshot contracts.Credential, expiresAt time.Time) contracts.Credential {
	return contracts.Credential{
		Type: asset.CredentialAliCloudSTS,
		Values: map[string]string{
			"access_key_id":     snapshot.Values["access_key_id"],
			"access_key_secret": snapshot.Values["access_key_secret"],
			"security_token":    snapshot.Values["security_token"],
		},
		ExpiresAt:    &expiresAt,
		ConnectionID: snapshot.ConnectionID,
		Site:         snapshot.Site,
		Version:      snapshot.Version,
	}
}
