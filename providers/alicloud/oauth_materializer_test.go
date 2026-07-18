package alicloud

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/persistence"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

type oauthAPIStub struct {
	mu          sync.Mutex
	calls       []string
	refresh     oauthTokens
	refreshErr  error
	exchange    oauthSTS
	exchangeErr error
	refreshWait chan struct{}
	refreshSeen chan struct{}
}

func (*oauthAPIStub) AuthorizationURL(asset.ConnectionSite, string, string, string) (string, error) {
	return "", errors.New("unexpected AuthorizationURL")
}

func (*oauthAPIStub) ExchangeAuthorizationCode(context.Context, asset.ConnectionSite, string, string, string) (oauthTokens, error) {
	return oauthTokens{}, errors.New("unexpected ExchangeAuthorizationCode")
}

func (s *oauthAPIStub) Refresh(_ context.Context, _ asset.ConnectionSite, refreshToken string) (oauthTokens, error) {
	s.mu.Lock()
	s.calls = append(s.calls, "refresh:"+refreshToken)
	result, err := s.refresh, s.refreshErr
	seen, wait := s.refreshSeen, s.refreshWait
	s.mu.Unlock()
	if seen != nil {
		select {
		case seen <- struct{}{}:
		default:
		}
	}
	if wait != nil {
		<-wait
	}
	return result, err
}

func (s *oauthAPIStub) ExchangeSTS(_ context.Context, _ asset.ConnectionSite, accessToken string) (oauthSTS, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, "exchange:"+accessToken)
	return s.exchange, s.exchangeErr
}

func (s *oauthAPIStub) recordedCalls() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.calls...)
}

type credentialUpdaterStub struct {
	expected    contracts.Credential
	replacement contracts.Credential
	err         error
}

func (s *credentialUpdaterStub) CompareAndSwap(_ context.Context, expected, replacement contracts.Credential) (contracts.Credential, error) {
	s.expected = expected
	s.replacement = replacement
	if s.err != nil {
		return contracts.Credential{}, s.err
	}
	replacement.ConnectionID = expected.ConnectionID
	replacement.Site = expected.Site
	replacement.Version = "version-refreshed"
	return replacement, nil
}

type credentialSourceStub struct {
	value contracts.Credential
	err   error
	calls int
}

func (s *credentialSourceStub) Resolve(context.Context, asset.ConnectionID) (contracts.Credential, error) {
	s.calls++
	return s.value, s.err
}

type credentialSequenceSourceStub struct {
	values []contracts.Credential
	calls  int
}

func (s *credentialSequenceSourceStub) Resolve(
	context.Context,
	asset.ConnectionID,
) (contracts.Credential, error) {
	index := s.calls
	s.calls++
	if index >= len(s.values) {
		index = len(s.values) - 1
	}
	return cloneTestOAuthCredential(s.values[index]), nil
}

type oauthCredentialStoreStub struct {
	mu      sync.Mutex
	value   contracts.Credential
	updates int
}

func (s *oauthCredentialStoreStub) Resolve(
	_ context.Context,
	connectionID asset.ConnectionID,
) (contracts.Credential, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if connectionID != s.value.ConnectionID {
		return contracts.Credential{}, errors.New("unexpected connection ID")
	}
	return cloneTestOAuthCredential(s.value), nil
}

func (s *oauthCredentialStoreStub) CompareAndSwap(
	_ context.Context,
	expected contracts.Credential,
	replacement contracts.Credential,
) (contracts.Credential, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if expected.Version != s.value.Version {
		return contracts.Credential{}, persistence.ErrConflict
	}
	s.updates++
	replacement.ConnectionID = expected.ConnectionID
	replacement.Site = expected.Site
	replacement.Version = "version-refreshed"
	s.value = cloneTestOAuthCredential(replacement)
	return cloneTestOAuthCredential(replacement), nil
}

func (s *oauthCredentialStoreStub) updateCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.updates
}

func cloneTestOAuthCredential(value contracts.Credential) contracts.Credential {
	value.Values = cloneOAuthValues(value.Values)
	return value
}

func oauthCredentialFixture(now time.Time) contracts.Credential {
	return contracts.Credential{
		Type:         asset.CredentialAliCloudOAuth,
		ConnectionID: "connection-oauth",
		Site:         asset.ConnectionSiteINTL,
		Version:      "version-original",
		Values: map[string]string{
			oauthSiteKey:              string(asset.ConnectionSiteINTL),
			oauthAccessTokenKey:       "access-old",
			oauthRefreshTokenKey:      "refresh-old",
			oauthAccessTokenExpireKey: formatOAuthUnix(now.Add(time.Hour)),
			"access_key_id":           "sts-old-id",
			"access_key_secret":       "sts-old-secret",
			"security_token":          "sts-old-token",
			oauthSTSExpireKey:         formatOAuthUnix(now.Add(time.Hour)),
		},
	}
}

func TestOAuthMaterializerReusesValidSTSWithoutNetworkOrPersistence(t *testing.T) {
	now := time.Date(2026, 7, 27, 16, 0, 0, 0, time.UTC)
	api := &oauthAPIStub{}
	updater := &credentialUpdaterStub{err: errors.New("updater must not be called")}
	materializer := newOAuthMaterializer(api, &credentialSourceStub{}, updater, func() time.Time { return now })

	got, err := materializer.materialize(context.Background(), oauthCredentialFixture(now))
	if err != nil {
		t.Fatal(err)
	}
	if got.Type != asset.CredentialAliCloudSTS ||
		got.Values["access_key_id"] != "sts-old-id" ||
		got.ExpiresAt == nil || got.ExpiresAt.Unix() != now.Add(time.Hour).Unix() ||
		got.ConnectionID != "connection-oauth" || got.Site != asset.ConnectionSiteINTL || got.Version != "version-original" {
		t.Fatalf("materialized credential = %#v", got)
	}
	if len(api.calls) != 0 {
		t.Fatalf("OAuth calls = %#v", api.calls)
	}
}

func TestOAuthMaterializerRefreshesTokensAndSTSWithRotation(t *testing.T) {
	now := time.Date(2026, 7, 27, 16, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name             string
		rotated          string
		wantRefreshToken string
	}{
		{name: "preserves refresh token", wantRefreshToken: "refresh-old"},
		{name: "stores rotated refresh token", rotated: "refresh-rotated", wantRefreshToken: "refresh-rotated"},
	} {
		t.Run(test.name, func(t *testing.T) {
			expected := oauthCredentialFixture(now)
			expected.Values[oauthAccessTokenExpireKey] = formatOAuthUnix(now.Add(-time.Minute))
			expected.Values[oauthSTSExpireKey] = formatOAuthUnix(now.Add(-time.Minute))
			api := &oauthAPIStub{
				refresh: oauthTokens{
					AccessToken: "access-new", RefreshToken: test.rotated, ExpiresAt: now.Add(30 * time.Minute),
				},
				exchange: oauthSTS{
					AccessKeyID: "sts-new-id", AccessKeySecret: "sts-new-secret", SecurityToken: "sts-new-token",
					ExpiresAt: now.Add(15 * time.Minute),
				},
			}
			updater := &credentialUpdaterStub{}
			materializer := newOAuthMaterializer(api, &credentialSourceStub{value: expected}, updater, func() time.Time { return now })

			got, err := materializer.materialize(context.Background(), expected)
			if err != nil {
				t.Fatal(err)
			}
			if len(api.calls) != 2 || api.calls[0] != "refresh:refresh-old" || api.calls[1] != "exchange:access-new" {
				t.Fatalf("OAuth calls = %#v", api.calls)
			}
			if updater.replacement.Type != asset.CredentialAliCloudOAuth ||
				updater.replacement.ExpiresAt != nil ||
				updater.replacement.Values[oauthRefreshTokenKey] != test.wantRefreshToken ||
				updater.replacement.Values[oauthAccessTokenKey] != "access-new" ||
				updater.replacement.Values["access_key_id"] != "sts-new-id" {
				t.Fatalf("persisted replacement = %#v", updater.replacement)
			}
			if got.Type != asset.CredentialAliCloudSTS ||
				got.Values["security_token"] != "sts-new-token" ||
				got.Version != "version-refreshed" ||
				got.ExpiresAt == nil || got.ExpiresAt.Unix() != now.Add(15*time.Minute).Unix() {
				t.Fatalf("materialized credential = %#v", got)
			}
		})
	}
}

func TestOAuthMaterializerExchangesSTSWithoutRefreshingValidAccessToken(t *testing.T) {
	now := time.Date(2026, 7, 27, 16, 0, 0, 0, time.UTC)
	expected := oauthCredentialFixture(now)
	expected.Values[oauthSTSExpireKey] = formatOAuthUnix(now.Add(-time.Minute))
	api := &oauthAPIStub{exchange: oauthSTS{
		AccessKeyID: "sts-new-id", AccessKeySecret: "sts-new-secret", SecurityToken: "sts-new-token",
		ExpiresAt: now.Add(15 * time.Minute),
	}}
	materializer := newOAuthMaterializer(api, &credentialSourceStub{value: expected}, &credentialUpdaterStub{}, func() time.Time { return now })

	if _, err := materializer.materialize(context.Background(), expected); err != nil {
		t.Fatal(err)
	}
	if len(api.calls) != 1 || api.calls[0] != "exchange:access-old" {
		t.Fatalf("OAuth calls = %#v", api.calls)
	}
}

func TestOAuthMaterializerReusesConcurrentWinnerAndRejectsUserReplacement(t *testing.T) {
	now := time.Date(2026, 7, 27, 16, 0, 0, 0, time.UTC)
	expected := oauthCredentialFixture(now)
	expected.Values[oauthSTSExpireKey] = formatOAuthUnix(now.Add(-time.Minute))
	api := &oauthAPIStub{exchange: oauthSTS{
		AccessKeyID: "loser-id", AccessKeySecret: "loser-secret", SecurityToken: "loser-token",
		ExpiresAt: now.Add(15 * time.Minute),
	}}

	winner := oauthCredentialFixture(now)
	winner.Version = "version-winner"
	winner.Values["access_key_id"] = "winner-id"
	source := &credentialSourceStub{value: winner}
	materializer := newOAuthMaterializer(api, source, &credentialUpdaterStub{err: persistence.ErrConflict}, func() time.Time { return now })
	got, err := materializer.materialize(context.Background(), expected)
	if err != nil {
		t.Fatal(err)
	}
	if got.Values["access_key_id"] != "winner-id" || got.Version != "version-winner" || source.calls != 1 {
		t.Fatalf("winner credential = %#v, source calls = %d", got, source.calls)
	}

	source.value = contracts.Credential{
		Type: asset.CredentialAliCloudAccessKey,
		Values: map[string]string{
			"access_key_id": "user-id", "access_key_secret": "user-secret",
		},
	}
	if _, err := materializer.materialize(context.Background(), expected); credentialValidationCode(err) != "credential_refresh_conflict" {
		t.Fatalf("user replacement error = %v", err)
	}
}

func TestOAuthMaterializerRequiresMatchingSiteAndReauthenticationToken(t *testing.T) {
	now := time.Date(2026, 7, 27, 16, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name     string
		mutate   func(contracts.Credential) contracts.Credential
		wantCode string
	}{
		{
			name: "site mismatch",
			mutate: func(value contracts.Credential) contracts.Credential {
				value.Values[oauthSiteKey] = string(asset.ConnectionSiteCN)
				return value
			},
			wantCode: "oauth_site_mismatch",
		},
		{
			name: "missing refresh token",
			mutate: func(value contracts.Credential) contracts.Credential {
				value.Values[oauthAccessTokenExpireKey] = formatOAuthUnix(now.Add(-time.Minute))
				value.Values[oauthSTSExpireKey] = formatOAuthUnix(now.Add(-time.Minute))
				delete(value.Values, oauthRefreshTokenKey)
				return value
			},
			wantCode: "oauth_reauthentication_required",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			value := test.mutate(oauthCredentialFixture(now))
			materializer := newOAuthMaterializer(&oauthAPIStub{}, &credentialSourceStub{value: value}, &credentialUpdaterStub{}, func() time.Time { return now })
			if _, err := materializer.materialize(context.Background(), value); credentialValidationCode(err) != test.wantCode {
				t.Fatalf("materialize() error = %v, want %q", err, test.wantCode)
			}
		})
	}
}

func TestOAuthMaterializerRefreshesBeforeExpiryAndSerializesConnection(t *testing.T) {
	now := time.Date(2026, 8, 4, 16, 20, 0, 0, time.UTC)
	expected := oauthCredentialFixture(now)
	expected.Values[oauthAccessTokenExpireKey] = formatOAuthUnix(now.Add(4 * time.Minute))
	expected.Values[oauthSTSExpireKey] = formatOAuthUnix(now.Add(4 * time.Minute))
	refreshSeen := make(chan struct{}, 1)
	refreshWait := make(chan struct{})
	api := &oauthAPIStub{
		refresh: oauthTokens{
			AccessToken: "access-new", RefreshToken: "refresh-rotated",
			ExpiresAt: now.Add(time.Hour),
		},
		exchange: oauthSTS{
			AccessKeyID: "sts-new-id", AccessKeySecret: "sts-new-secret",
			SecurityToken: "sts-new-token", ExpiresAt: now.Add(30 * time.Minute),
		},
		refreshSeen: refreshSeen,
		refreshWait: refreshWait,
	}
	store := &oauthCredentialStoreStub{value: cloneTestOAuthCredential(expected)}
	materializer := newOAuthMaterializer(api, store, store, func() time.Time { return now })

	const callers = 8
	results := make(chan contracts.Credential, callers)
	errs := make(chan error, callers)
	var wait sync.WaitGroup
	wait.Add(callers)
	for range callers {
		go func() {
			defer wait.Done()
			result, err := materializer.materialize(context.Background(), cloneTestOAuthCredential(expected))
			results <- result
			errs <- err
		}()
	}
	select {
	case <-refreshSeen:
	case <-time.After(time.Second):
		t.Fatal("OAuth refresh did not start")
	}
	close(refreshWait)
	wait.Wait()
	close(results)
	close(errs)

	for err := range errs {
		if err != nil {
			t.Fatalf("materialize concurrent credential: %v", err)
		}
	}
	for result := range results {
		if result.Type != asset.CredentialAliCloudSTS ||
			result.Values["access_key_id"] != "sts-new-id" ||
			result.Version != "version-refreshed" {
			t.Fatalf("materialized credential = %#v", result)
		}
	}
	calls := api.recordedCalls()
	if len(calls) != 2 || calls[0] != "refresh:refresh-old" || calls[1] != "exchange:access-new" {
		t.Fatalf("OAuth calls = %#v", calls)
	}
	if store.updateCount() != 1 {
		t.Fatalf("credential updates = %d, want 1", store.updateCount())
	}
}

func TestOAuthMaterializerUsesPersistedWinnerWhenRotatedTokenRefreshFails(t *testing.T) {
	now := time.Date(2026, 8, 4, 16, 20, 0, 0, time.UTC)
	expected := oauthCredentialFixture(now)
	expected.Values[oauthAccessTokenExpireKey] = formatOAuthUnix(now.Add(-time.Minute))
	expected.Values[oauthSTSExpireKey] = formatOAuthUnix(now.Add(-time.Minute))
	winner := oauthCredentialFixture(now)
	winner.Version = "version-winner"
	winner.Values["access_key_id"] = "winner-id"
	source := &credentialSequenceSourceStub{
		values: []contracts.Credential{expected, winner},
	}
	materializer := newOAuthMaterializer(
		&oauthAPIStub{refreshErr: errors.New("rotated refresh token is no longer valid")},
		source,
		&credentialUpdaterStub{},
		func() time.Time { return now },
	)

	got, err := materializer.materialize(context.Background(), expected)
	if err != nil {
		t.Fatal(err)
	}
	if got.Values["access_key_id"] != "winner-id" ||
		got.Version != "version-winner" ||
		source.calls != 2 {
		t.Fatalf("winner credential = %#v, source calls = %d", got, source.calls)
	}
}

func credentialValidationCode(err error) string {
	var validationErr *contracts.CredentialValidationError
	if errors.As(err, &validationErr) {
		return validationErr.Code
	}
	return ""
}
