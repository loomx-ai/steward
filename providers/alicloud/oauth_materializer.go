package alicloud

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/persistence"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

const (
	oauthSiteKey              = "site"
	oauthAccessTokenKey       = "oauth_access_token"
	oauthRefreshTokenKey      = "oauth_refresh_token"
	oauthAccessTokenExpireKey = "oauth_access_token_expire"
	oauthSTSExpireKey         = "sts_expiration"
	// Leave enough validity for client construction, retries, and one cloud API
	// request. Alibaba Cloud OAuth rotates tokens during renewal, so refreshes
	// must also be serialized per connection.
	oauthRefreshBeforeExpiry = 5 * time.Minute
)

type oauthMaterializer struct {
	api     oauthAPI
	source  contracts.CredentialSource
	updater contracts.CredentialUpdater
	now     func() time.Time
	locks   sync.Map
}

func newOAuthMaterializer(
	api oauthAPI,
	source contracts.CredentialSource,
	updater contracts.CredentialUpdater,
	now func() time.Time,
) *oauthMaterializer {
	if now == nil {
		now = time.Now
	}
	return &oauthMaterializer{api: api, source: source, updater: updater, now: now}
}

func (m *oauthMaterializer) materialize(
	ctx context.Context,
	expected contracts.Credential,
) (contracts.Credential, error) {
	if expected.Type != asset.CredentialAliCloudOAuth {
		return expected, nil
	}
	now := m.now().UTC()
	reusable, ok, err := reusableOAuthSTSCredential(expected, now)
	if err != nil || ok {
		return reusable, err
	}

	if expected.ConnectionID == "" {
		return m.renew(ctx, expected, now)
	}
	lockValue, _ := m.locks.LoadOrStore(expected.ConnectionID, &sync.Mutex{})
	lock := lockValue.(*sync.Mutex)
	lock.Lock()
	defer lock.Unlock()

	current, err := m.currentCredential(ctx, expected)
	if err != nil {
		return contracts.Credential{}, err
	}
	now = m.now().UTC()
	reusable, ok, err = reusableOAuthSTSCredential(current, now)
	if err != nil || ok {
		return reusable, err
	}
	return m.renew(ctx, current, now)
}

func reusableOAuthSTSCredential(
	snapshot contracts.Credential,
	now time.Time,
) (contracts.Credential, bool, error) {
	if err := validateOAuthSite(snapshot); err != nil {
		return contracts.Credential{}, false, err
	}
	stsExpiresAt, err := parseOptionalOAuthUnix(snapshot.Values, oauthSTSExpireKey)
	if err != nil {
		return contracts.Credential{}, false, invalidOAuthCredential(err)
	}
	if validOAuthSTS(snapshot.Values, stsExpiresAt, now) {
		return oauthSTSCredential(snapshot, stsExpiresAt), true, nil
	}
	return contracts.Credential{}, false, nil
}

func (m *oauthMaterializer) currentCredential(
	ctx context.Context,
	expected contracts.Credential,
) (contracts.Credential, error) {
	if m.source == nil {
		return contracts.Credential{}, oauthRefreshUnavailable(nil)
	}
	current, err := m.source.Resolve(ctx, expected.ConnectionID)
	if err != nil {
		return contracts.Credential{}, oauthRefreshUnavailable(err)
	}
	if current.Type != asset.CredentialAliCloudOAuth {
		return contracts.Credential{}, credentialRefreshConflict(nil)
	}
	return current, nil
}

func (m *oauthMaterializer) renew(
	ctx context.Context,
	expected contracts.Credential,
	now time.Time,
) (contracts.Credential, error) {
	if err := validateOAuthSite(expected); err != nil {
		return contracts.Credential{}, err
	}
	stsExpiresAt, err := parseOptionalOAuthUnix(expected.Values, oauthSTSExpireKey)
	if err != nil {
		return contracts.Credential{}, invalidOAuthCredential(err)
	}
	if validOAuthSTS(expected.Values, stsExpiresAt, now) {
		return oauthSTSCredential(expected, stsExpiresAt), nil
	}

	values := cloneOAuthValues(expected.Values)
	accessExpiresAt, err := parseOptionalOAuthUnix(values, oauthAccessTokenExpireKey)
	if err != nil {
		return contracts.Credential{}, invalidOAuthCredential(err)
	}
	accessToken := strings.TrimSpace(values[oauthAccessTokenKey])
	if accessToken == "" || expiresSoon(accessExpiresAt, now) {
		refreshToken := strings.TrimSpace(values[oauthRefreshTokenKey])
		if refreshToken == "" {
			return contracts.Credential{}, contracts.NewCredentialValidationError(
				"oauth_reauthentication_required",
				"The Alibaba Cloud OAuth authorization must be completed again.",
				nil,
			)
		}
		if m.api == nil {
			return contracts.Credential{}, oauthRefreshUnavailable(nil)
		}
		refreshed, err := m.api.Refresh(ctx, expected.Site, refreshToken)
		if err != nil {
			if winner, ok := m.concurrentCredentialAfterFailure(ctx, expected.ConnectionID, now); ok {
				return winner, nil
			}
			return contracts.Credential{}, contracts.NewCredentialValidationError(
				"oauth_token_refresh_failed",
				"Alibaba Cloud OAuth could not refresh the access token.",
				err,
			)
		}
		accessToken = strings.TrimSpace(refreshed.AccessToken)
		values[oauthAccessTokenKey] = accessToken
		values[oauthAccessTokenExpireKey] = formatOAuthUnix(refreshed.ExpiresAt)
		if rotated := strings.TrimSpace(refreshed.RefreshToken); rotated != "" {
			values[oauthRefreshTokenKey] = rotated
		}
	}
	if m.api == nil {
		return contracts.Credential{}, oauthRefreshUnavailable(nil)
	}
	sts, err := m.api.ExchangeSTS(ctx, expected.Site, accessToken)
	if err != nil {
		if winner, ok := m.concurrentCredentialAfterFailure(ctx, expected.ConnectionID, now); ok {
			return winner, nil
		}
		return contracts.Credential{}, contracts.NewCredentialValidationError(
			"oauth_sts_exchange_failed",
			"Alibaba Cloud OAuth could not exchange the access token for a temporary credential.",
			err,
		)
	}
	values["access_key_id"] = sts.AccessKeyID
	values["access_key_secret"] = sts.AccessKeySecret
	values["security_token"] = sts.SecurityToken
	values[oauthSTSExpireKey] = formatOAuthUnix(sts.ExpiresAt)
	replacement := contracts.Credential{
		Type:   asset.CredentialAliCloudOAuth,
		Values: values,
	}
	if m.updater == nil {
		return contracts.Credential{}, oauthRefreshUnavailable(nil)
	}
	updated, err := m.updater.CompareAndSwap(ctx, expected, replacement)
	if err == nil {
		return oauthSTSCredential(updated, sts.ExpiresAt), nil
	}
	if !errors.Is(err, persistence.ErrConflict) {
		return contracts.Credential{}, oauthRefreshUnavailable(err)
	}
	return m.concurrentWinner(ctx, expected.ConnectionID, now)
}

func (m *oauthMaterializer) concurrentCredentialAfterFailure(
	ctx context.Context,
	connectionID asset.ConnectionID,
	now time.Time,
) (contracts.Credential, bool) {
	if connectionID == "" {
		return contracts.Credential{}, false
	}
	winner, err := m.concurrentWinner(ctx, connectionID, now)
	return winner, err == nil
}

func (m *oauthMaterializer) concurrentWinner(
	ctx context.Context,
	connectionID asset.ConnectionID,
	now time.Time,
) (contracts.Credential, error) {
	if m.source == nil {
		return contracts.Credential{}, credentialRefreshConflict(nil)
	}
	winner, err := m.source.Resolve(ctx, connectionID)
	if err != nil || winner.Type != asset.CredentialAliCloudOAuth {
		return contracts.Credential{}, credentialRefreshConflict(err)
	}
	if err := validateOAuthSite(winner); err != nil {
		return contracts.Credential{}, credentialRefreshConflict(err)
	}
	expiresAt, err := parseOptionalOAuthUnix(winner.Values, oauthSTSExpireKey)
	if err != nil || !validOAuthSTS(winner.Values, expiresAt, now) {
		return contracts.Credential{}, credentialRefreshConflict(err)
	}
	return oauthSTSCredential(winner, expiresAt), nil
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
	return !expiresSoon(expiresAt, now) &&
		strings.TrimSpace(values["access_key_id"]) != "" &&
		strings.TrimSpace(values["access_key_secret"]) != "" &&
		strings.TrimSpace(values["security_token"]) != ""
}

func expiresSoon(expiresAt, now time.Time) bool {
	return !expiresAt.After(now.Add(oauthRefreshBeforeExpiry))
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

func parseOptionalOAuthUnix(values map[string]string, key string) (time.Time, error) {
	raw := strings.TrimSpace(values[key])
	if raw == "" {
		return time.Time{}, nil
	}
	seconds, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return time.Time{}, fmt.Errorf("%s is not a Unix timestamp", key)
	}
	return time.Unix(seconds, 0).UTC(), nil
}

func formatOAuthUnix(value time.Time) string {
	return strconv.FormatInt(value.Unix(), 10)
}

func cloneOAuthValues(values map[string]string) map[string]string {
	cloned := make(map[string]string, len(values))
	for key, value := range values {
		cloned[key] = value
	}
	return cloned
}

func invalidOAuthCredential(cause error) error {
	return contracts.NewCredentialValidationError(
		"credential_fields_invalid",
		"The Alibaba Cloud OAuth credential fields are incomplete or invalid.",
		cause,
	)
}

func oauthRefreshUnavailable(cause error) error {
	return contracts.NewCredentialValidationError(
		"credential_refresh_unavailable",
		"The Alibaba Cloud OAuth credential could not be refreshed.",
		cause,
	)
}

func credentialRefreshConflict(cause error) error {
	return contracts.NewCredentialValidationError(
		"credential_refresh_conflict",
		"The Alibaba Cloud credential changed while OAuth was refreshing.",
		cause,
	)
}
