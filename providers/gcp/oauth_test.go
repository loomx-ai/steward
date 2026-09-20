package gcp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/credential/oauth"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

type oauthServerStub struct {
	tokenForm    url.Values
	tokenBody    string
	tokenStatus  int
	searchBodies []string
	searchTokens []string
	userinfoBody string
}

func (s *oauthServerStub) start(t *testing.T) *oauthDriver {
	t.Helper()
	if s.tokenStatus == 0 {
		s.tokenStatus = http.StatusOK
	}
	if s.tokenBody == "" {
		s.tokenBody = `{"access_token":"access-secret","refresh_token":"refresh-secret","expires_in":3600}`
	}
	if s.userinfoBody == "" {
		s.userinfoBody = `{"email":"operator@example.test"}`
	}
	if len(s.searchBodies) == 0 {
		s.searchBodies = []string{`{"projects":[{"projectId":"steward-demo","displayName":"Steward Demo","state":"ACTIVE"}]}`}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(response http.ResponseWriter, request *http.Request) {
		_ = request.ParseForm()
		s.tokenForm = request.PostForm
		response.Header().Set("Content-Type", "application/json")
		response.WriteHeader(s.tokenStatus)
		_, _ = response.Write([]byte(s.tokenBody))
	})
	mux.HandleFunc("/userinfo", func(response http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer access-secret" {
			response.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = response.Write([]byte(s.userinfoBody))
	})
	mux.HandleFunc("/projects:search", func(response http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer access-secret" {
			response.WriteHeader(http.StatusUnauthorized)
			return
		}
		index := 0
		for position, token := range s.searchTokens {
			if request.URL.Query().Get("pageToken") == token {
				index = position + 1
			}
		}
		if index >= len(s.searchBodies) {
			index = len(s.searchBodies) - 1
		}
		_, _ = response.Write([]byte(s.searchBodies[index]))
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return newOAuthDriver(server.Client(), oauthEndpoints{
		authorize: server.URL + "/auth",
		token:     server.URL + "/token",
		search:    server.URL + "/projects:search",
		userinfo:  server.URL + "/userinfo",
	})
}

func authorizeGCP(t *testing.T, driver *oauthDriver, params map[string]string) oauth.Session {
	t.Helper()
	authorization, err := driver.Authorize(context.Background(), params, oauth.Request{
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

func TestGCPOAuthAuthorizationURLCarriesPKCEAndOfflineConsent(t *testing.T) {
	driver := (&oauthServerStub{}).start(t)
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
	query := parsed.Query()
	for key, want := range map[string]string{
		"response_type":         "code",
		"client_id":             oauthClientID,
		"redirect_uri":          "http://127.0.0.1:12345/",
		"state":                 "state-value",
		"code_challenge":        "challenge-value",
		"code_challenge_method": "S256",
		// Google only re-issues a refresh token when both are present, and
		// without one the connection dies when the access token expires.
		"access_type": "offline",
		"prompt":      "consent",
	} {
		if query.Get(key) != want {
			t.Errorf("authorization %s = %q, want %q", key, query.Get(key), want)
		}
	}
	scopes := strings.Fields(query.Get("scope"))
	if len(scopes) != 3 || !strings.Contains(query.Get("scope"), "cloud-platform") {
		t.Errorf("authorization scope = %q", query.Get("scope"))
	}
	// The CLI asks for App Engine, Compute and Cloud SQL sign-in as well.
	// Steward reads none of them and must not request them.
	for _, unused := range []string{"appengine.admin", "sqlservice.login", "auth/compute"} {
		if strings.Contains(query.Get("scope"), unused) {
			t.Errorf("authorization requests unused scope %q", unused)
		}
	}
	// The verifier proves the exchange and must never reach the browser.
	if strings.Contains(authorization.URL, "verifier-value") {
		t.Errorf("authorization URL leaked the code verifier: %s", authorization.URL)
	}
}

func TestGCPOAuthExchangeSendsTheVerifierAndReadsTheIdentity(t *testing.T) {
	stub := &oauthServerStub{}
	driver := stub.start(t)
	session := authorizeGCP(t, driver, nil)

	for key, want := range map[string]string{
		"grant_type":    "authorization_code",
		"code":          "authorization-code",
		"redirect_uri":  "http://127.0.0.1:12345/",
		"code_verifier": "verifier-value",
		"client_id":     oauthClientID,
		"client_secret": oauthClientSecret,
	} {
		if stub.tokenForm.Get(key) != want {
			t.Errorf("token form %s = %q, want %q", key, stub.tokenForm.Get(key), want)
		}
	}
	credential, err := session.Credential(context.Background(), "steward-demo")
	if err != nil {
		t.Fatal(err)
	}
	if credential.Type != asset.CredentialGCPOAuth ||
		credential.Values["project_id"] != "steward-demo" ||
		credential.Values["principal_email"] != "operator@example.test" ||
		credential.Values[oauth.RefreshTokenKey] != "refresh-secret" {
		t.Fatalf("credential = %#v", credential)
	}
	// The short-lived access token is not worth storing: the refresh token
	// produces a fresh one whenever a client needs it.
	if _, exists := credential.Values[oauth.AccessTokenKey]; exists {
		t.Errorf("credential stored a short-lived access token: %#v", credential.Values)
	}
}

func TestGCPOAuthRefusesAnAuthorizationWithoutARefreshToken(t *testing.T) {
	stub := &oauthServerStub{tokenBody: `{"access_token":"access-secret","expires_in":3600}`}
	driver := stub.start(t)
	authorization, err := driver.Authorize(context.Background(), nil, oauth.Request{
		RedirectURI: "http://127.0.0.1:12345/", State: "s", CodeChallenge: "c", CodeVerifier: "v",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := authorization.Exchange(context.Background(), "code"); err == nil {
		t.Fatal("exchange accepted an authorization that cannot be refreshed")
	}
}

func TestGCPOAuthListsActiveProjectsAcrossPages(t *testing.T) {
	stub := &oauthServerStub{
		searchBodies: []string{
			`{"projects":[
				{"projectId":"steward-demo","displayName":"Steward Demo","state":"ACTIVE"},
				{"projectId":"retired-one","displayName":"Retired","state":"DELETE_REQUESTED"}
			],"nextPageToken":"page-2"}`,
			`{"projects":[{"projectId":"steward-second","state":"ACTIVE"}]}`,
		},
		searchTokens: []string{"page-2"},
	}
	driver := stub.start(t)
	session := authorizeGCP(t, driver, nil)

	targets, err := session.Targets(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 2 ||
		targets[0].ID != "steward-demo" || targets[0].Name != "Steward Demo" ||
		targets[1].ID != "steward-second" || targets[1].Name != "steward-second" {
		t.Fatalf("targets = %#v", targets)
	}
}

func TestGCPOAuthRejectsAProjectTheIdentityCannotReach(t *testing.T) {
	driver := (&oauthServerStub{}).start(t)
	session := authorizeGCP(t, driver, nil)

	_, err := session.Credential(context.Background(), "someone-elses-project")
	var flowErr *contracts.OAuthFlowError
	if err == nil || !asFlowError(err, &flowErr) || flowErr.Code != "oauth_target_invalid" {
		t.Fatalf("credential for an unreachable project error = %v", err)
	}
}

func TestGCPOAuthValidatesOptionalScopeParameters(t *testing.T) {
	driver := (&oauthServerStub{}).start(t)
	for name, params := range map[string]map[string]string{
		"firewall": {"firewall_policy_parent": "projects/not-a-container"},
		"identity": {"identity_group_parent": "groups/not-a-customer"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := driver.Authorize(context.Background(), params, oauth.Request{
				RedirectURI: "http://127.0.0.1:12345/", State: "s", CodeChallenge: "c", CodeVerifier: "v",
			})
			var flowErr *contracts.OAuthFlowError
			if err == nil || !asFlowError(err, &flowErr) || flowErr.Code != "invalid_oauth_parameters" {
				t.Fatalf("authorize error = %v", err)
			}
		})
	}
}

func TestGCPOAuthCredentialBuildsAUsableClient(t *testing.T) {
	stub := &oauthServerStub{}
	driver := stub.start(t)
	session := authorizeGCP(t, driver, map[string]string{"identity_group_parent": "customers/C01234567"})
	credential, err := session.Credential(context.Background(), "steward-demo")
	if err != nil {
		t.Fatal(err)
	}
	built, err := newClient(credential, http.DefaultTransport)
	if err != nil {
		t.Fatal(err)
	}
	if built.project != "steward-demo" || built.email != "operator@example.test" ||
		built.identityParent != "customers/C01234567" {
		t.Fatalf("client = %#v", built)
	}

	// A stored authorization missing either half of the identity is unusable.
	for _, key := range []string{"project_id", "principal_email", oauth.RefreshTokenKey} {
		broken := contracts.Credential{Type: credential.Type, Values: oauth.CloneValues(credential.Values)}
		delete(broken.Values, key)
		if _, err := newClient(broken, http.DefaultTransport); err == nil {
			t.Errorf("client built without %q", key)
		}
	}
}

func asFlowError(err error, target **contracts.OAuthFlowError) bool {
	flowErr, ok := err.(*contracts.OAuthFlowError)
	if ok {
		*target = flowErr
	}
	return ok
}
