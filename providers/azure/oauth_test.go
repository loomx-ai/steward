package azure

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/credential/oauth"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

const (
	oauthTenant       = "11111111-1111-1111-1111-111111111111"
	oauthOtherTenant  = "22222222-2222-2222-2222-222222222222"
	oauthSubscription = "33333333-3333-3333-3333-333333333333"
	oauthObjectID    = "44444444-4444-4444-4444-444444444444"
)

type azureOAuthStub struct {
	mu sync.Mutex
	// scopeFailures names the audiences this identity cannot reach.
	scopeFailures map[string]bool
	tenantFailure string
	tokenForms    []url.Values
	refreshSerial int
	subscriptions map[string]string
}

func (s *azureOAuthStub) start(t *testing.T) *oauthDriver {
	t.Helper()
	if s.subscriptions == nil {
		s.subscriptions = map[string]string{oauthTenant: oauthSubscription}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(response http.ResponseWriter, request *http.Request) {
		switch {
		case strings.HasSuffix(request.URL.Path, "/oauth2/v2.0/token"):
			s.serveToken(response, request)
		case request.URL.Path == "/tenants":
			s.serveTenants(response, request)
		case request.URL.Path == "/subscriptions":
			s.serveSubscriptions(response, request)
		default:
			response.WriteHeader(http.StatusNotFound)
		}
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return newOAuthDriver(server.Client(), server.URL, server.URL)
}

func (s *azureOAuthStub) serveToken(response http.ResponseWriter, request *http.Request) {
	_ = request.ParseForm()
	realm := strings.TrimSuffix(strings.TrimPrefix(request.URL.Path, "/"), "/oauth2/v2.0/token")
	scope := request.PostForm.Get("scope")
	s.mu.Lock()
	s.tokenForms = append(s.tokenForms, request.PostForm)
	s.refreshSerial++
	serial := s.refreshSerial
	failTenant := s.tenantFailure == realm
	failScope := s.scopeFailures[scopeKey(scope)]
	s.mu.Unlock()
	if failTenant || failScope {
		response.WriteHeader(http.StatusBadRequest)
		_, _ = response.Write([]byte(`{"error":"invalid_grant"}`))
		return
	}
	payload := map[string]any{
		"access_token":  "access~" + realm + "~" + scopeKey(scope),
		"refresh_token": "refresh-" + itoa(serial),
		"expires_in":    3600,
		"id_token":      testIDToken(oauthObjectID),
	}
	response.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(response).Encode(payload)
}

func (s *azureOAuthStub) serveTenants(response http.ResponseWriter, _ *http.Request) {
	tenants := []map[string]any{}
	for tenant := range s.subscriptions {
		tenants = append(tenants, map[string]any{"tenantId": tenant, "displayName": "Tenant " + tenant[:4]})
	}
	// A stable order keeps the assertions readable.
	if len(tenants) == 2 && text(tenants[0]["tenantId"]) > text(tenants[1]["tenantId"]) {
		tenants[0], tenants[1] = tenants[1], tenants[0]
	}
	_ = json.NewEncoder(response).Encode(map[string]any{"value": tenants})
}

func (s *azureOAuthStub) serveSubscriptions(response http.ResponseWriter, request *http.Request) {
	token := strings.TrimPrefix(request.Header.Get("Authorization"), "Bearer access~")
	tenant, _, _ := strings.Cut(token, "~")
	subscription, ok := s.subscriptions[tenant]
	if !ok {
		_ = json.NewEncoder(response).Encode(map[string]any{"value": []any{}})
		return
	}
	_ = json.NewEncoder(response).Encode(map[string]any{"value": []map[string]any{
		{"subscriptionId": subscription, "displayName": "Production", "state": "Enabled"},
		{"subscriptionId": "55555555-5555-5555-5555-555555555555", "displayName": "Retired", "state": "Disabled"},
	}})
}

// scopeKey names the audience a requested scope belongs to. The test driver
// serves ARM from its own origin, so ARM is the fallback rather than a match.
func scopeKey(scope string) string {
	for _, audience := range oauthAudiences {
		if audience.key == "arm" {
			continue
		}
		if strings.Contains(scope, strings.TrimSuffix(audience.scope, "/.default")) {
			return audience.key
		}
	}
	return "arm"
}

func itoa(value int) string {
	return string(rune('0' + value%10))
}

func testIDToken(objectID string) string {
	claims, _ := json.Marshal(map[string]string{"oid": objectID})
	return "header." + base64.RawURLEncoding.EncodeToString(claims) + ".signature"
}

func authorizeAzure(t *testing.T, driver *oauthDriver) oauth.Session {
	t.Helper()
	authorization, err := driver.Authorize(context.Background(), nil, oauth.Request{
		RedirectURI:   "http://127.0.0.1:12345/",
		State:         "state-value",
		CodeChallenge: "challenge-value",
		CodeVerifier:  "verifier-value",
	})
	if err != nil {
		t.Fatal(err)
	}
	session, err := authorization.Exchange(context.Background(), "authorization-code")
	if err != nil {
		t.Fatal(err)
	}
	return session
}

func TestAzureOAuthAuthorizationURLTargetsWorkAccountsWithPKCE(t *testing.T) {
	driver := (&azureOAuthStub{}).start(t)
	authorization, err := driver.Authorize(context.Background(), nil, oauth.Request{
		RedirectURI:   "http://127.0.0.1:12345/",
		State:         "state-value",
		CodeChallenge: "challenge-value",
		CodeVerifier:  "verifier-value",
	})
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(authorization.URL)
	if err != nil {
		t.Fatal(err)
	}
	// A personal Microsoft account cannot hold a subscription, so the
	// authorization asks the work-and-school realm rather than "common".
	if !strings.Contains(parsed.Path, "/"+oauthCommonRealm+"/oauth2/v2.0/authorize") {
		t.Errorf("authorization path = %q", parsed.Path)
	}
	query := parsed.Query()
	for key, want := range map[string]string{
		"response_type":         "code",
		"client_id":             oauthClientID,
		"code_challenge_method": "S256",
		"code_challenge":        "challenge-value",
		"state":                 "state-value",
	} {
		if query.Get(key) != want {
			t.Errorf("authorization %s = %q, want %q", key, query.Get(key), want)
		}
	}
	if !strings.Contains(query.Get("scope"), "offline_access") {
		t.Errorf("authorization scope = %q, want offline_access", query.Get("scope"))
	}
	if strings.Contains(authorization.URL, "verifier-value") {
		t.Errorf("authorization URL leaked the code verifier: %s", authorization.URL)
	}
}

func TestAzureOAuthListsEnabledSubscriptionsPerTenant(t *testing.T) {
	stub := &azureOAuthStub{subscriptions: map[string]string{
		oauthTenant:      oauthSubscription,
		oauthOtherTenant: "66666666-6666-6666-6666-666666666666",
	}}
	session := authorizeAzure(t, stub.start(t))

	targets, err := session.Targets(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 2 {
		t.Fatalf("targets = %#v", targets)
	}
	if targets[0].ID != oauthTenant+"/"+oauthSubscription || targets[0].Name != "Production" {
		t.Errorf("first target = %#v", targets[0])
	}
	// A disabled subscription cannot be inventoried and must not be offered.
	for _, target := range targets {
		if strings.Contains(target.ID, "55555555") {
			t.Errorf("a disabled subscription was offered: %#v", target)
		}
	}
}

// A tenant the identity can see but cannot currently sign in to must not fail
// the whole listing; the tenants that do work are still connectable.
func TestAzureOAuthSkipsATenantItCannotRedeem(t *testing.T) {
	stub := &azureOAuthStub{
		subscriptions: map[string]string{
			oauthTenant:      oauthSubscription,
			oauthOtherTenant: "66666666-6666-6666-6666-666666666666",
		},
		tenantFailure: oauthOtherTenant,
	}
	session := authorizeAzure(t, stub.start(t))

	targets, err := session.Targets(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 1 || targets[0].ID != oauthTenant+"/"+oauthSubscription {
		t.Fatalf("targets = %#v", targets)
	}
}

func TestAzureOAuthCredentialStoresEveryAudienceAndTheTenantPrincipal(t *testing.T) {
	stub := &azureOAuthStub{}
	session := authorizeAzure(t, stub.start(t))

	credential, err := session.Credential(context.Background(), oauthTenant+"/"+oauthSubscription)
	if err != nil {
		t.Fatal(err)
	}
	if credential.Type != asset.CredentialAzureOAuth ||
		credential.Values["tenant_id"] != oauthTenant ||
		credential.Values["subscription_id"] != oauthSubscription ||
		credential.Values["principal_id"] != oauthObjectID {
		t.Fatalf("credential = %#v", credential.Values)
	}
	for _, audience := range oauthAudiences {
		if strings.TrimSpace(credential.Values[oauthTokenKey(audience.key)]) == "" {
			t.Errorf("credential has no %s token", audience.key)
		}
	}
	if strings.TrimSpace(credential.Values[oauth.RefreshTokenKey]) == "" {
		t.Error("credential has no refresh token")
	}
}

func TestAzureOAuthRejectsASubscriptionTheIdentityCannotReach(t *testing.T) {
	session := authorizeAzure(t, (&azureOAuthStub{}).start(t))

	_, err := session.Credential(context.Background(), oauthOtherTenant+"/"+oauthSubscription)
	flowErr, ok := err.(*contracts.OAuthFlowError)
	if !ok || flowErr.Code != "oauth_target_invalid" {
		t.Fatalf("credential for an unreachable subscription error = %v", err)
	}
}

// Losing one data plane must degrade that data plane, not the connection: ARM
// is the only audience a connection cannot work without.
func TestAzureOAuthKeepsTheConnectionWhenADataPlaneAudienceIsRefused(t *testing.T) {
	stub := &azureOAuthStub{scopeFailures: map[string]bool{"vault": true}}
	session := authorizeAzure(t, stub.start(t))

	credential, err := session.Credential(context.Background(), oauthTenant+"/"+oauthSubscription)
	if err != nil {
		t.Fatal(err)
	}
	if credential.Values[oauthTokenKey("vault")] != "" {
		t.Errorf("a refused audience produced a token: %q", credential.Values[oauthTokenKey("vault")])
	}
	if strings.TrimSpace(credential.Values[oauthTokenKey("arm")]) == "" {
		t.Error("the required ARM audience was lost with the refused one")
	}

	built, err := newClient(credential, http.DefaultTransport)
	if err != nil {
		t.Fatal(err)
	}
	request, _ := http.NewRequest(http.MethodGet, "https://vault.azure.net/certificates", nil)
	if _, err := built.keyVaultHTTP.Transport.RoundTrip(request); err == nil ||
		!strings.Contains(err.Error(), "vault.azure.net") {
		t.Fatalf("key vault request error = %v, want a named audience failure", err)
	}
}

func TestAzureOAuthRefusesTheRequiredAudience(t *testing.T) {
	stub := &azureOAuthStub{scopeFailures: map[string]bool{"arm": true}}
	driver := stub.start(t)
	authorization, err := driver.Authorize(context.Background(), nil, oauth.Request{
		RedirectURI: "http://127.0.0.1:12345/", State: "s", CodeChallenge: "c", CodeVerifier: "v",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := authorization.Exchange(context.Background(), "code"); err == nil {
		t.Fatal("exchange accepted an authorization without an ARM token")
	}
}

func TestAzureOAuthRefresherReusesFreshTokensAndRenewsExpiredOnes(t *testing.T) {
	stub := &azureOAuthStub{}
	driver := stub.start(t)
	session := authorizeAzure(t, driver)
	credential, err := session.Credential(context.Background(), oauthTenant+"/"+oauthSubscription)
	if err != nil {
		t.Fatal(err)
	}
	refresher := &oauthRefresher{driver: driver}
	now := time.Now().UTC()

	usable, ok, err := refresher.Usable(credential, now)
	if err != nil || !ok || usable.ExpiresAt == nil {
		t.Fatalf("fresh credential usable=%v err=%v expires=%v", ok, err, usable.ExpiresAt)
	}
	stub.mu.Lock()
	callsBefore := len(stub.tokenForms)
	stub.mu.Unlock()

	// Past the ARM token's expiry the whole audience set is renewed at once.
	expired := contracts.Credential{Type: credential.Type, Values: oauth.CloneValues(credential.Values)}
	expired.Values[oauthExpireKey("arm")] = oauth.FormatUnix(now.Add(-time.Minute))
	if _, ok, err := refresher.Usable(expired, now); ok || err != nil {
		t.Fatalf("expired credential usable=%v err=%v", ok, err)
	}
	renewal, err := refresher.Renew(context.Background(), expired, now)
	if err != nil {
		t.Fatal(err)
	}
	stub.mu.Lock()
	callsAfter := len(stub.tokenForms)
	lastForm := stub.tokenForms[len(stub.tokenForms)-1]
	stub.mu.Unlock()
	if callsAfter-callsBefore != len(oauthAudiences) {
		t.Errorf("renewal made %d token calls, want %d", callsAfter-callsBefore, len(oauthAudiences))
	}
	if lastForm.Get("grant_type") != "refresh_token" {
		t.Errorf("renewal grant type = %q", lastForm.Get("grant_type"))
	}
	// The newest refresh token is carried forward so the stored one cannot age out.
	if renewal.Values[oauth.RefreshTokenKey] == expired.Values[oauth.RefreshTokenKey] {
		t.Error("renewal did not store the rotated refresh token")
	}
	for _, audience := range oauthAudiences {
		if strings.TrimSpace(renewal.Values[oauthTokenKey(audience.key)]) == "" {
			t.Errorf("renewal has no %s token", audience.key)
		}
	}
}

func TestAzureOAuthRefresherRequiresAStoredRefreshToken(t *testing.T) {
	driver := (&azureOAuthStub{}).start(t)
	refresher := &oauthRefresher{driver: driver}
	credential := contracts.Credential{Type: asset.CredentialAzureOAuth, Values: map[string]string{
		"tenant_id": oauthTenant, "subscription_id": oauthSubscription, "principal_id": oauthObjectID,
	}}
	if _, _, err := refresher.Usable(credential, time.Now()); err == nil {
		t.Fatal("a credential with no refresh token was accepted")
	}
}
