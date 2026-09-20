package aws

import (
	"context"
	"strings"
	"time"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	awssso "github.com/aws/aws-sdk-go-v2/service/sso"
	awsssooidc "github.com/aws/aws-sdk-go-v2/service/ssooidc"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/credential/oauth"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

// oauthRefresher renews a stored IAM Identity Center sign-in. The renewal has
// two levels: the sign-in token is refreshed from the refresh token, and the
// sign-in token is then exchanged for the role's temporary credentials, which
// are what every AWS API call actually uses.
type oauthRefresher struct {
	driver *oauthDriver
}

func (r *oauthRefresher) CredentialType() asset.CredentialType {
	return asset.CredentialAWSOAuth
}

func (r *oauthRefresher) Usable(
	snapshot contracts.Credential,
	now time.Time,
) (contracts.Credential, bool, error) {
	if !oauth.Present(snapshot.Values, ssoRegionKey, accountIDKey, roleNameKey) {
		return contracts.Credential{}, false, oauth.InvalidCredential(oauthLabel, nil)
	}
	expiresAt, err := oauth.ParseUnix(snapshot.Values, stsExpireKey)
	if err != nil {
		return contracts.Credential{}, false, oauth.InvalidCredential(oauthLabel, err)
	}
	if oauth.ExpiresSoon(expiresAt, now) ||
		!oauth.Present(snapshot.Values, "access_key_id", "secret_access_key", "session_token") {
		return contracts.Credential{}, false, nil
	}
	return sessionCredential(snapshot, expiresAt), true, nil
}

func (r *oauthRefresher) Renew(
	ctx context.Context,
	snapshot contracts.Credential,
	now time.Time,
) (oauth.Renewal, error) {
	values := oauth.CloneValues(snapshot.Values)
	region := strings.TrimSpace(values[ssoRegionKey])
	accessToken := strings.TrimSpace(values[oauth.AccessTokenKey])
	accessExpiresAt, err := oauth.ParseUnix(values, oauth.AccessTokenExpireKey)
	if err != nil {
		return oauth.Renewal{}, oauth.InvalidCredential(oauthLabel, err)
	}
	if accessToken == "" || oauth.ExpiresSoon(accessExpiresAt, now) {
		refreshed, err := r.signIn(ctx, values, now)
		if err != nil {
			return oauth.Renewal{}, err
		}
		for key, value := range refreshed {
			values[key] = value
		}
		accessToken = values[oauth.AccessTokenKey]
	}
	role, err := r.roleCredentials(ctx, region, accessToken, values[accountIDKey], values[roleNameKey])
	if err != nil {
		return oauth.Renewal{}, oauth.RefreshFailed(oauthLabel, err)
	}
	for key, value := range role {
		values[key] = value
	}
	expiresAt, err := oauth.ParseUnix(values, stsExpireKey)
	if err != nil {
		return oauth.Renewal{}, oauth.InvalidCredential(oauthLabel, err)
	}
	return oauth.Renewal{
		Values: values,
		Usable: func(stored contracts.Credential) contracts.Credential {
			return sessionCredential(stored, expiresAt)
		},
	}, nil
}

// signIn refreshes the Identity Center sign-in token. The registered client
// itself expires — around ninety days — and once it has, no stored material can
// renew the sign-in and the operator has to authorize again.
func (r *oauthRefresher) signIn(
	ctx context.Context,
	values map[string]string,
	now time.Time,
) (map[string]string, error) {
	refreshToken := strings.TrimSpace(values[oauth.RefreshTokenKey])
	if refreshToken == "" {
		return nil, oauth.ReauthenticationRequired(oauthLabel, nil)
	}
	registrationExpiresAt, err := oauth.ParseUnix(values, ssoClientExpire)
	if err != nil {
		return nil, oauth.InvalidCredential(oauthLabel, err)
	}
	if !registrationExpiresAt.IsZero() && !registrationExpiresAt.After(now) {
		return nil, oauth.ReauthenticationRequired(oauthLabel, nil)
	}
	oidc := r.driver.newOIDC(strings.TrimSpace(values[ssoRegionKey]), r.driver.endpoint, r.driver.httpClient)
	tokens, err := oidc.CreateToken(ctx, &awsssooidc.CreateTokenInput{
		ClientId:     awssdk.String(values[ssoClientIDKey]),
		ClientSecret: awssdk.String(values[ssoClientSecret]),
		GrantType:    awssdk.String("refresh_token"),
		RefreshToken: awssdk.String(refreshToken),
	})
	if err != nil {
		return nil, oauth.RefreshFailed(oauthLabel, err)
	}
	renewed := map[string]string{
		oauth.AccessTokenKey:       awssdk.ToString(tokens.AccessToken),
		oauth.AccessTokenExpireKey: oauth.FormatUnix(now.Add(time.Duration(tokens.ExpiresIn) * time.Second)),
	}
	if rotated := strings.TrimSpace(awssdk.ToString(tokens.RefreshToken)); rotated != "" {
		renewed[oauth.RefreshTokenKey] = rotated
	}
	if renewed[oauth.AccessTokenKey] == "" {
		return nil, oauth.RefreshFailed(oauthLabel, nil)
	}
	return renewed, nil
}

// roleCredentials exchanges an Identity Center sign-in for the account and
// role's temporary credentials, which expire much sooner than the sign-in does.
func (r *oauthRefresher) roleCredentials(
	ctx context.Context,
	region string,
	accessToken string,
	accountID string,
	roleName string,
) (map[string]string, error) {
	portal := r.driver.newPortal(region, r.driver.endpoint, r.driver.httpClient)
	issued, err := portal.GetRoleCredentials(ctx, &awssso.GetRoleCredentialsInput{
		AccessToken: awssdk.String(accessToken),
		AccountId:   awssdk.String(accountID),
		RoleName:    awssdk.String(roleName),
	})
	if err != nil {
		return nil, err
	}
	role := issued.RoleCredentials
	if role == nil || awssdk.ToString(role.AccessKeyId) == "" ||
		awssdk.ToString(role.SecretAccessKey) == "" || awssdk.ToString(role.SessionToken) == "" {
		return nil, oauth.RefreshFailed(oauthLabel, nil)
	}
	return map[string]string{
		"access_key_id":     awssdk.ToString(role.AccessKeyId),
		"secret_access_key": awssdk.ToString(role.SecretAccessKey),
		"session_token":     awssdk.ToString(role.SessionToken),
		// IAM Identity Center reports the expiry in milliseconds.
		stsExpireKey: oauth.FormatUnix(time.UnixMilli(role.Expiration).UTC()),
	}, nil
}

// sessionCredential is what the rest of the provider sees: an ordinary AWS
// session credential. Nothing downstream has to know it came from a browser.
func sessionCredential(snapshot contracts.Credential, expiresAt time.Time) contracts.Credential {
	return contracts.Credential{
		Type: asset.CredentialAWSSession,
		Values: map[string]string{
			"access_key_id":     snapshot.Values["access_key_id"],
			"secret_access_key": snapshot.Values["secret_access_key"],
			"session_token":     snapshot.Values["session_token"],
		},
		ExpiresAt:    &expiresAt,
		ConnectionID: snapshot.ConnectionID,
		Site:         snapshot.Site,
		Version:      snapshot.Version,
	}
}
