package aws

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	awssso "github.com/aws/aws-sdk-go-v2/service/sso"
	ssotypes "github.com/aws/aws-sdk-go-v2/service/sso/types"
	awsssooidc "github.com/aws/aws-sdk-go-v2/service/ssooidc"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/credential/oauth"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

var oauthNow = time.Date(2026, 7, 27, 16, 0, 0, 0, time.UTC)

type ssoOIDCStub struct {
	registerInput   *awsssooidc.RegisterClientInput
	registerErr     error
	tokenInputs     []*awsssooidc.CreateTokenInput
	accessToken     string
	refreshToken    string
	secretExpiresAt int64
}

func (s *ssoOIDCStub) RegisterClient(
	_ context.Context,
	input *awsssooidc.RegisterClientInput,
	_ ...func(*awsssooidc.Options),
) (*awsssooidc.RegisterClientOutput, error) {
	s.registerInput = input
	if s.registerErr != nil {
		return nil, s.registerErr
	}
	expires := s.secretExpiresAt
	if expires == 0 {
		expires = oauthNow.Add(90 * 24 * time.Hour).Unix()
	}
	return &awsssooidc.RegisterClientOutput{
		ClientId:              awssdk.String("registered-client"),
		ClientSecret:          awssdk.String("registered-secret"),
		ClientSecretExpiresAt: expires,
		AuthorizationEndpoint: awssdk.String("https://oidc.us-east-1.amazonaws.com/authorize"),
		TokenEndpoint:         awssdk.String("https://oidc.us-east-1.amazonaws.com/token"),
	}, nil
}

func (s *ssoOIDCStub) CreateToken(
	_ context.Context,
	input *awsssooidc.CreateTokenInput,
	_ ...func(*awsssooidc.Options),
) (*awsssooidc.CreateTokenOutput, error) {
	s.tokenInputs = append(s.tokenInputs, input)
	access := s.accessToken
	if access == "" {
		access = "sso-access-token"
	}
	refresh := s.refreshToken
	if refresh == "" {
		refresh = "sso-refresh-token"
	}
	return &awsssooidc.CreateTokenOutput{
		AccessToken: awssdk.String(access), RefreshToken: awssdk.String(refresh), ExpiresIn: 28800,
	}, nil
}

type ssoPortalStub struct {
	accounts   []ssotypes.AccountInfo
	roles      map[string][]string
	roleInput  *awssso.GetRoleCredentialsInput
	expiration int64
}

func (s *ssoPortalStub) ListAccounts(
	_ context.Context,
	_ *awssso.ListAccountsInput,
	_ ...func(*awssso.Options),
) (*awssso.ListAccountsOutput, error) {
	return &awssso.ListAccountsOutput{AccountList: s.accounts}, nil
}

func (s *ssoPortalStub) ListAccountRoles(
	_ context.Context,
	input *awssso.ListAccountRolesInput,
	_ ...func(*awssso.Options),
) (*awssso.ListAccountRolesOutput, error) {
	roles := []ssotypes.RoleInfo{}
	for _, role := range s.roles[awssdk.ToString(input.AccountId)] {
		roles = append(roles, ssotypes.RoleInfo{AccountId: input.AccountId, RoleName: awssdk.String(role)})
	}
	return &awssso.ListAccountRolesOutput{RoleList: roles}, nil
}

func (s *ssoPortalStub) GetRoleCredentials(
	_ context.Context,
	input *awssso.GetRoleCredentialsInput,
	_ ...func(*awssso.Options),
) (*awssso.GetRoleCredentialsOutput, error) {
	s.roleInput = input
	expiration := s.expiration
	if expiration == 0 {
		expiration = oauthNow.Add(time.Hour).UnixMilli()
	}
	return &awssso.GetRoleCredentialsOutput{RoleCredentials: &ssotypes.RoleCredentials{
		AccessKeyId:     awssdk.String("ASIAEXAMPLE"),
		SecretAccessKey: awssdk.String("role-secret"),
		SessionToken:    awssdk.String("role-session-token"),
		Expiration:      expiration,
	}}, nil
}

func oauthDriverStub(oidc *ssoOIDCStub, portal *ssoPortalStub) *oauthDriver {
	driver := newOAuthDriver(nil, "")
	driver.now = func() time.Time { return oauthNow }
	driver.newOIDC = func(string, string, awssdk.HTTPClient) ssoOIDCAPI { return oidc }
	driver.newPortal = func(string, string, awssdk.HTTPClient) ssoPortalAPI { return portal }
	return driver
}

func defaultPortalStub() *ssoPortalStub {
	return &ssoPortalStub{
		accounts: []ssotypes.AccountInfo{
			{AccountId: awssdk.String("123456789012"), AccountName: awssdk.String("Production")},
			{AccountId: awssdk.String("210987654321"), AccountName: awssdk.String("Sandbox")},
		},
		roles: map[string][]string{
			"123456789012": {"AdministratorAccess", "ReadOnlyAccess"},
			"210987654321": {"ReadOnlyAccess"},
		},
	}
}

func oauthParams() map[string]string {
	return map[string]string{
		startURLKey:  "https://d-0123456789.awsapps.com/start",
		ssoRegionKey: "us-east-1",
	}
}

func oauthRequest() oauth.Request {
	return oauth.Request{
		RedirectURI:   "http://127.0.0.1:12345/oauth/callback",
		State:         "state-value",
		CodeChallenge: "challenge-value",
		CodeVerifier:  "verifier-value",
	}
}

func authorizeAWS(t *testing.T, driver *oauthDriver) oauth.Session {
	t.Helper()
	authorization, err := driver.Authorize(context.Background(), oauthParams(), oauthRequest())
	if err != nil {
		t.Fatal(err)
	}
	session, err := authorization.Exchange(context.Background(), "authorization-code")
	if err != nil {
		t.Fatal(err)
	}
	return session
}

func TestAWSOAuthRegistersAPublicClientBeforeOpeningTheBrowser(t *testing.T) {
	oidc := &ssoOIDCStub{}
	driver := oauthDriverStub(oidc, defaultPortalStub())

	authorization, err := driver.Authorize(context.Background(), oauthParams(), oauthRequest())
	if err != nil {
		t.Fatal(err)
	}
	input := oidc.registerInput
	if awssdk.ToString(input.ClientType) != "public" ||
		awssdk.ToString(input.IssuerUrl) != "https://d-0123456789.awsapps.com/start" ||
		len(input.RedirectUris) != 1 || input.RedirectUris[0] != "http://127.0.0.1:12345/oauth/callback" {
		t.Fatalf("register input = %#v", input)
	}
	if strings.Join(input.GrantTypes, ",") != "authorization_code,refresh_token" ||
		strings.Join(input.Scopes, ",") != oauthScope {
		t.Fatalf("register grants=%v scopes=%v", input.GrantTypes, input.Scopes)
	}
	parsed, err := url.Parse(authorization.URL)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	for key, want := range map[string]string{
		"response_type":         "code",
		"client_id":             "registered-client",
		"redirect_uri":          "http://127.0.0.1:12345/oauth/callback",
		"state":                 "state-value",
		"code_challenge":        "challenge-value",
		"code_challenge_method": "S256",
		"scopes":                oauthScope,
	} {
		if query.Get(key) != want {
			t.Errorf("authorization %s = %q, want %q", key, query.Get(key), want)
		}
	}
	// Neither the verifier nor the registration secret may reach the browser.
	for _, secret := range []string{"verifier-value", "registered-secret"} {
		if strings.Contains(authorization.URL, secret) {
			t.Errorf("authorization URL leaked %q: %s", secret, authorization.URL)
		}
	}
}

// A directory that refuses the registration must fail the start, so the
// operator is told rather than left with a browser tab that cannot work.
func TestAWSOAuthReportsARefusedRegistration(t *testing.T) {
	driver := oauthDriverStub(&ssoOIDCStub{registerErr: errors.New("invalid_client_metadata")}, defaultPortalStub())

	_, err := driver.Authorize(context.Background(), oauthParams(), oauthRequest())
	flowErr, ok := err.(*contracts.OAuthFlowError)
	if !ok || flowErr.Code != "oauth_client_registration_failed" {
		t.Fatalf("authorize error = %v", err)
	}
}

func TestAWSOAuthRejectsAnIncompleteIdentityCenterAddress(t *testing.T) {
	driver := oauthDriverStub(&ssoOIDCStub{}, defaultPortalStub())
	for name, params := range map[string]map[string]string{
		"no start URL":   {ssoRegionKey: "us-east-1"},
		"insecure start": {startURLKey: "http://d-0123456789.awsapps.com/start", ssoRegionKey: "us-east-1"},
		"no region":      {startURLKey: "https://d-0123456789.awsapps.com/start"},
		"bad region":     {startURLKey: "https://d-0123456789.awsapps.com/start", ssoRegionKey: "not a region"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := driver.Authorize(context.Background(), params, oauthRequest())
			flowErr, ok := err.(*contracts.OAuthFlowError)
			if !ok || flowErr.Code != "invalid_oauth_parameters" {
				t.Fatalf("authorize error = %v", err)
			}
		})
	}
}

func TestAWSOAuthExchangeSendsTheVerifierAndListsEveryAssignedRole(t *testing.T) {
	oidc := &ssoOIDCStub{}
	session := authorizeAWS(t, oauthDriverStub(oidc, defaultPortalStub()))

	input := oidc.tokenInputs[0]
	if awssdk.ToString(input.GrantType) != "authorization_code" ||
		awssdk.ToString(input.Code) != "authorization-code" ||
		awssdk.ToString(input.CodeVerifier) != "verifier-value" ||
		awssdk.ToString(input.RedirectUri) != "http://127.0.0.1:12345/oauth/callback" ||
		awssdk.ToString(input.ClientId) != "registered-client" {
		t.Fatalf("create token input = %#v", input)
	}
	targets, err := session.Targets(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// One target per account and role pair, because a connection is one role.
	if len(targets) != 3 ||
		targets[0].ID != "123456789012/AdministratorAccess" ||
		targets[1].ID != "123456789012/ReadOnlyAccess" ||
		targets[2].ID != "210987654321/ReadOnlyAccess" {
		t.Fatalf("targets = %#v", targets)
	}
	if targets[0].Name != "Production · AdministratorAccess" || targets[0].Description != "123456789012" {
		t.Errorf("first target = %#v", targets[0])
	}
}

func TestAWSOAuthCredentialStoresTheSignInAndTheRoleCredentials(t *testing.T) {
	portal := defaultPortalStub()
	session := authorizeAWS(t, oauthDriverStub(&ssoOIDCStub{}, portal))

	credential, err := session.Credential(context.Background(), "123456789012/ReadOnlyAccess")
	if err != nil {
		t.Fatal(err)
	}
	if credential.Type != asset.CredentialAWSOAuth {
		t.Fatalf("credential type = %q", credential.Type)
	}
	for key, want := range map[string]string{
		startURLKey:           "https://d-0123456789.awsapps.com/start",
		ssoRegionKey:          "us-east-1",
		ssoClientIDKey:        "registered-client",
		ssoClientSecret:       "registered-secret",
		accountIDKey:          "123456789012",
		roleNameKey:           "ReadOnlyAccess",
		oauth.AccessTokenKey:  "sso-access-token",
		oauth.RefreshTokenKey: "sso-refresh-token",
		"access_key_id":       "ASIAEXAMPLE",
		"secret_access_key":   "role-secret",
		"session_token":       "role-session-token",
	} {
		if credential.Values[key] != want {
			t.Errorf("credential %s = %q, want %q", key, credential.Values[key], want)
		}
	}
	if awssdk.ToString(portal.roleInput.RoleName) != "ReadOnlyAccess" {
		t.Errorf("role credentials asked for %q", awssdk.ToString(portal.roleInput.RoleName))
	}
}

func TestAWSOAuthRejectsARoleTheIdentityIsNotAssigned(t *testing.T) {
	session := authorizeAWS(t, oauthDriverStub(&ssoOIDCStub{}, defaultPortalStub()))

	_, err := session.Credential(context.Background(), "210987654321/AdministratorAccess")
	flowErr, ok := err.(*contracts.OAuthFlowError)
	if !ok || flowErr.Code != "oauth_target_invalid" {
		t.Fatalf("credential for an unassigned role error = %v", err)
	}
}

func TestAWSOAuthRefresherHandsOutAnOrdinarySessionCredential(t *testing.T) {
	portal := defaultPortalStub()
	driver := oauthDriverStub(&ssoOIDCStub{}, portal)
	session := authorizeAWS(t, driver)
	credential, err := session.Credential(context.Background(), "123456789012/ReadOnlyAccess")
	if err != nil {
		t.Fatal(err)
	}
	refresher := &oauthRefresher{driver: driver}

	usable, ok, err := refresher.Usable(credential, oauthNow)
	if err != nil || !ok {
		t.Fatalf("fresh credential usable=%v err=%v", ok, err)
	}
	// Everything downstream of this point is ordinary AWS session material.
	if usable.Type != asset.CredentialAWSSession ||
		usable.Values["access_key_id"] != "ASIAEXAMPLE" ||
		usable.Values["session_token"] != "role-session-token" ||
		usable.ExpiresAt == nil || !usable.ExpiresAt.Equal(oauthNow.Add(time.Hour)) {
		t.Fatalf("usable credential = %#v", usable)
	}
	// The stored sign-in must not travel with it.
	for _, key := range []string{oauth.RefreshTokenKey, oauth.AccessTokenKey, ssoClientSecret} {
		if _, exists := usable.Values[key]; exists {
			t.Errorf("session credential carried %q", key)
		}
	}
}

func TestAWSOAuthRefresherExchangesTheSignInAgainWhenRoleCredentialsExpire(t *testing.T) {
	oidc := &ssoOIDCStub{}
	portal := defaultPortalStub()
	driver := oauthDriverStub(oidc, portal)
	session := authorizeAWS(t, driver)
	credential, err := session.Credential(context.Background(), "123456789012/ReadOnlyAccess")
	if err != nil {
		t.Fatal(err)
	}
	refresher := &oauthRefresher{driver: driver}
	later := oauthNow.Add(2 * time.Hour)

	if _, ok, err := refresher.Usable(credential, later); ok || err != nil {
		t.Fatalf("expired credential usable=%v err=%v", ok, err)
	}
	portal.expiration = later.Add(time.Hour).UnixMilli()
	renewal, err := refresher.Renew(context.Background(), credential, later)
	if err != nil {
		t.Fatal(err)
	}
	// The sign-in token is still valid for eight hours, so only the role
	// credentials are exchanged again.
	if len(oidc.tokenInputs) != 1 {
		t.Errorf("sign-in was refreshed %d times, want 0 beyond the authorization", len(oidc.tokenInputs)-1)
	}
	if renewal.Values[stsExpireKey] != oauth.FormatUnix(later.Add(time.Hour)) {
		t.Errorf("renewed expiry = %q", renewal.Values[stsExpireKey])
	}
}

func TestAWSOAuthRefresherRenewsTheSignInWhenItsTokenExpires(t *testing.T) {
	oidc := &ssoOIDCStub{}
	driver := oauthDriverStub(oidc, defaultPortalStub())
	session := authorizeAWS(t, driver)
	credential, err := session.Credential(context.Background(), "123456789012/ReadOnlyAccess")
	if err != nil {
		t.Fatal(err)
	}
	refresher := &oauthRefresher{driver: driver}
	later := oauthNow.Add(9 * time.Hour)
	oidc.accessToken = "renewed-access-token"
	oidc.refreshToken = "rotated-refresh-token"

	renewal, err := refresher.Renew(context.Background(), credential, later)
	if err != nil {
		t.Fatal(err)
	}
	refresh := oidc.tokenInputs[len(oidc.tokenInputs)-1]
	if awssdk.ToString(refresh.GrantType) != "refresh_token" ||
		awssdk.ToString(refresh.RefreshToken) != "sso-refresh-token" ||
		awssdk.ToString(refresh.ClientSecret) != "registered-secret" {
		t.Fatalf("refresh input = %#v", refresh)
	}
	if renewal.Values[oauth.AccessTokenKey] != "renewed-access-token" ||
		renewal.Values[oauth.RefreshTokenKey] != "rotated-refresh-token" {
		t.Fatalf("renewal did not store the rotated sign-in: %#v", renewal.Values)
	}
}

// The registered client expires long before the refresh token would, and once
// it has, no stored material can renew the sign-in.
func TestAWSOAuthRefresherRequiresReauthorizationWhenTheClientRegistrationExpires(t *testing.T) {
	oidc := &ssoOIDCStub{secretExpiresAt: oauthNow.Add(time.Hour).Unix()}
	driver := oauthDriverStub(oidc, defaultPortalStub())
	session := authorizeAWS(t, driver)
	credential, err := session.Credential(context.Background(), "123456789012/ReadOnlyAccess")
	if err != nil {
		t.Fatal(err)
	}
	refresher := &oauthRefresher{driver: driver}

	_, err = refresher.Renew(context.Background(), credential, oauthNow.Add(10*time.Hour))
	if credentialValidationCode(err) != "oauth_reauthentication_required" {
		t.Fatalf("renew past the registration error = %v", err)
	}
}

func credentialValidationCode(err error) string {
	var validationErr *contracts.CredentialValidationError
	if errors.As(err, &validationErr) {
		return validationErr.Code
	}
	return ""
}
