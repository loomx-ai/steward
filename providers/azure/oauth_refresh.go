package azure

import (
	"context"
	"net/url"
	"strings"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/credential/oauth"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

// oauthRefresher renews a stored Azure authorization. Entra ID issues one
// access token per audience, so a renewal redeems the refresh token once for
// each audience the provider reads and stores them together, under the
// materializer's per-connection lock and compare-and-swap.
type oauthRefresher struct {
	driver *oauthDriver
}

func (r *oauthRefresher) CredentialType() asset.CredentialType {
	return asset.CredentialAzureOAuth
}

func (r *oauthRefresher) Usable(
	snapshot contracts.Credential,
	now time.Time,
) (contracts.Credential, bool, error) {
	if !oauth.Present(snapshot.Values, "tenant_id", "subscription_id", "principal_id", oauth.RefreshTokenKey) {
		return contracts.Credential{}, false, oauth.InvalidCredential(oauthLabel, nil)
	}
	// Only ARM decides usability. The data-plane audiences are renewed with it
	// and an audience this identity cannot reach stays empty on purpose.
	expiresAt, err := oauth.ParseUnix(snapshot.Values, oauthExpireKey("arm"))
	if err != nil {
		return contracts.Credential{}, false, oauth.InvalidCredential(oauthLabel, err)
	}
	if strings.TrimSpace(snapshot.Values[oauthTokenKey("arm")]) == "" || oauth.ExpiresSoon(expiresAt, now) {
		return contracts.Credential{}, false, nil
	}
	usable := snapshot
	usable.ExpiresAt = &expiresAt
	return usable, true, nil
}

func (r *oauthRefresher) Renew(
	ctx context.Context,
	snapshot contracts.Credential,
	_ time.Time,
) (oauth.Renewal, error) {
	refreshToken := strings.TrimSpace(snapshot.Values[oauth.RefreshTokenKey])
	if refreshToken == "" {
		return oauth.Renewal{}, oauth.ReauthenticationRequired(oauthLabel, nil)
	}
	tenant := strings.TrimSpace(snapshot.Values["tenant_id"])
	values := oauth.CloneValues(snapshot.Values)
	refreshed, err := r.audienceTokens(ctx, tenant, refreshToken, oauthTokens{})
	if err != nil {
		return oauth.Renewal{}, oauth.RefreshFailed(oauthLabel, err)
	}
	for key, value := range refreshed {
		values[key] = value
	}
	expiresAt, err := oauth.ParseUnix(values, oauthExpireKey("arm"))
	if err != nil {
		return oauth.Renewal{}, oauth.InvalidCredential(oauthLabel, err)
	}
	return oauth.Renewal{
		Values: values,
		Usable: func(stored contracts.Credential) contracts.Credential {
			stored.ExpiresAt = &expiresAt
			return stored
		},
	}, nil
}

// audienceTokens redeems the refresh token once per audience. The ARM audience
// is mandatory; any other audience the identity cannot reach is recorded as
// empty so the connection keeps working without that data plane.
//
// `seed` is an already-issued ARM token from the same tenant, which the
// authorization has in hand and should not pay to fetch a second time.
func (r *oauthRefresher) audienceTokens(
	ctx context.Context,
	tenant string,
	refreshToken string,
	seed oauthTokens,
) (map[string]string, error) {
	values := map[string]string{}
	current := refreshToken
	for _, audience := range oauthAudiences {
		tokens := oauthTokens{}
		if audience.key == "arm" && strings.TrimSpace(seed.AccessToken) != "" {
			tokens = seed
		} else {
			issued, err := r.driver.token(ctx, tenant, url.Values{
				"grant_type":    {"refresh_token"},
				"refresh_token": {current},
				"scope":         {audience.scope + " offline_access"},
			})
			if err != nil {
				if audience.required {
					return nil, err
				}
				values[oauthTokenKey(audience.key)] = ""
				values[oauthExpireKey(audience.key)] = ""
				continue
			}
			tokens = issued
		}
		values[oauthTokenKey(audience.key)] = tokens.AccessToken
		values[oauthExpireKey(audience.key)] = oauth.FormatUnix(tokens.ExpiresAt)
		// Entra ID returns a fresh refresh token with each redemption. Carrying
		// the newest one forward keeps the stored token from ageing out.
		if rotated := strings.TrimSpace(tokens.RefreshToken); rotated != "" {
			current = rotated
		}
	}
	values[oauth.RefreshTokenKey] = current
	return values, nil
}
