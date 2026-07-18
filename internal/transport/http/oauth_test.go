package httptransport_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/persistence"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

type testOAuthFlowService struct {
	mu           sync.Mutex
	startView    contracts.OAuthFlowView
	getView      contracts.OAuthFlowView
	credential   contracts.Credential
	consumeErr   error
	startSubject string
	startSite    asset.ConnectionSite
	getSubject   string
	getID        string
	consumeCount int
	consumeSite  asset.ConnectionSite
	consumeID    string
}

func (service *testOAuthFlowService) Start(_ context.Context, subject string, site asset.ConnectionSite) (contracts.OAuthFlowView, error) {
	service.mu.Lock()
	defer service.mu.Unlock()
	service.startSubject = subject
	service.startSite = site
	return service.startView, nil
}

func (service *testOAuthFlowService) Get(_ context.Context, subject, id string) (contracts.OAuthFlowView, error) {
	service.mu.Lock()
	defer service.mu.Unlock()
	service.getSubject = subject
	service.getID = id
	return service.getView, nil
}

func (service *testOAuthFlowService) Consume(
	_ context.Context,
	_ string,
	id string,
	site asset.ConnectionSite,
	consume func(contracts.Credential) error,
) error {
	service.mu.Lock()
	service.consumeCount++
	service.consumeID = id
	service.consumeSite = site
	credential := service.credential
	err := service.consumeErr
	service.mu.Unlock()
	if err != nil {
		return err
	}
	return consume(credential)
}

func TestOAuthFlowRoutesRequireAdminAndReturnOnlyPublicState(t *testing.T) {
	expiresAt := time.Date(2026, 7, 27, 12, 5, 0, 0, time.UTC)
	flows := &testOAuthFlowService{
		startView: contracts.OAuthFlowView{
			ID: "oauth-flow-a", Status: contracts.OAuthFlowPending,
			AuthorizationURL: "https://signin.alibabacloud.com/oauth2/v1/auth?state=opaque",
			ExpiresAt:        expiresAt,
		},
		getView: contracts.OAuthFlowView{
			ID: "oauth-flow-a", Status: contracts.OAuthFlowAuthorized, ExpiresAt: expiresAt,
		},
	}
	_, router := terminalRouterWithOAuthFlows(t, testConnectionValidator{}, flows)

	viewerRequest := httptest.NewRequest(http.MethodPost, "/api/providers/alicloud/oauth/flows", bytes.NewBufferString(`{"site":"intl"}`))
	viewerRequest.Header.Set("Authorization", "Bearer viewer-token")
	viewerResponse := httptest.NewRecorder()
	router.ServeHTTP(viewerResponse, viewerRequest)
	if viewerResponse.Code != http.StatusForbidden {
		t.Fatalf("viewer status=%d body=%s", viewerResponse.Code, viewerResponse.Body.String())
	}

	startRequest := httptest.NewRequest(http.MethodPost, "/api/providers/alicloud/oauth/flows", bytes.NewBufferString(`{"site":"intl"}`))
	startRequest.Header.Set("Authorization", "Bearer admin-token")
	startResponse := httptest.NewRecorder()
	router.ServeHTTP(startResponse, startRequest)
	if startResponse.Code != http.StatusCreated ||
		!strings.Contains(startResponse.Body.String(), `"id":"oauth-flow-a"`) ||
		!strings.Contains(startResponse.Body.String(), `"authorization_url":"https://signin.alibabacloud.com/`) ||
		strings.Contains(startResponse.Body.String(), "access_token") {
		t.Fatalf("start status=%d body=%s", startResponse.Code, startResponse.Body.String())
	}
	if flows.startSubject != "carol" || flows.startSite != asset.ConnectionSiteINTL {
		t.Fatalf("start subject=%q site=%q", flows.startSubject, flows.startSite)
	}

	getRequest := httptest.NewRequest(http.MethodGet, "/api/providers/alicloud/oauth/flows/oauth-flow-a", nil)
	getRequest.Header.Set("Authorization", "Bearer admin-token")
	getResponse := httptest.NewRecorder()
	router.ServeHTTP(getResponse, getRequest)
	if getResponse.Code != http.StatusOK || !strings.Contains(getResponse.Body.String(), `"status":"authorized"`) {
		t.Fatalf("get status=%d body=%s", getResponse.Code, getResponse.Body.String())
	}
	if flows.getSubject != "carol" || flows.getID != "oauth-flow-a" {
		t.Fatalf("get subject=%q id=%q", flows.getSubject, flows.getID)
	}
}

func TestOAuthConnectionCreationConsumesServerOwnedCredential(t *testing.T) {
	flows := &testOAuthFlowService{credential: oauthCredentialForHTTPTest()}
	repositories, router := terminalRouterWithOAuthFlows(t, testConnectionValidator{}, flows)
	request := httptest.NewRequest(http.MethodPost, "/api/connections", bytes.NewBufferString(`{
		"name":"oauth account","provider":"alicloud","site":"cn",
		"credential":{"type":"oauth","values":{"flow_id":"oauth-flow-a"}}
	}`))
	request.Header.Set("Authorization", "Bearer admin-token")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if flows.consumeCount != 1 || flows.consumeID != "oauth-flow-a" || flows.consumeSite != asset.ConnectionSiteCN {
		t.Fatalf("consume count=%d id=%q site=%q", flows.consumeCount, flows.consumeID, flows.consumeSite)
	}
	for _, secret := range []string{"oauth-flow-a", "access-token-secret", "refresh-token-secret", "sts-secret"} {
		if strings.Contains(response.Body.String(), secret) {
			t.Fatalf("connection response exposed %q: %s", secret, response.Body.String())
		}
	}
	audits, err := repositories.Audits().ListAuditEvents(context.Background(), persistence.ListOptions{Limit: 10})
	if err != nil || len(audits.Items) != 1 {
		t.Fatalf("audits=%+v err=%v", audits.Items, err)
	}
	auditJSON, err := json.Marshal(audits.Items[0])
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"oauth-flow-a", "access-token-secret", "refresh-token-secret", "sts-secret"} {
		if strings.Contains(string(auditJSON), secret) {
			t.Fatalf("audit exposed %q: %+v", secret, audits.Items[0])
		}
	}
}

func TestOAuthCredentialInputRejectsDirectSecretsAndDoesNotConsumeFlow(t *testing.T) {
	flows := &testOAuthFlowService{credential: oauthCredentialForHTTPTest()}
	_, router := terminalRouterWithOAuthFlows(t, testConnectionValidator{}, flows)
	for _, body := range []string{
		`{"name":"direct","provider":"alicloud","site":"cn","credential":{"type":"oauth","values":{"access_token":"secret"}}}`,
		`{"name":"extra","provider":"alicloud","site":"cn","credential":{"type":"oauth","values":{"flow_id":"oauth-flow-a","refresh_token":"secret"}}}`,
		`{"name":"wrong provider","provider":"aws","credential":{"type":"oauth","values":{"flow_id":"oauth-flow-a"}}}`,
	} {
		request := httptest.NewRequest(http.MethodPost, "/api/connections", bytes.NewBufferString(body))
		request.Header.Set("Authorization", "Bearer admin-token")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), `"code":"invalid_credential_fields"`) {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
	}
	if flows.consumeCount != 0 {
		t.Fatalf("consume count=%d", flows.consumeCount)
	}
}

func TestOAuthCredentialReplacementUsesExistingConnectionSite(t *testing.T) {
	flows := &testOAuthFlowService{credential: oauthCredentialForHTTPTest()}
	repositories, router := terminalRouterWithOAuthFlows(t, testConnectionValidator{}, flows)
	connection, err := repositories.Connections().GetConnection(context.Background(), "connection-a")
	if err != nil {
		t.Fatal(err)
	}
	if connection.Site != asset.ConnectionSiteCN {
		t.Fatalf("legacy connection site=%q", connection.Site)
	}
	request := httptest.NewRequest(http.MethodPut, "/api/connections/connection-a/credential", bytes.NewBufferString(
		`{"type":"oauth","values":{"flow_id":"oauth-flow-a"}}`,
	))
	request.Header.Set("Authorization", "Bearer admin-token")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if flows.consumeCount != 1 || flows.consumeSite != asset.ConnectionSiteCN {
		t.Fatalf("consume count=%d site=%q", flows.consumeCount, flows.consumeSite)
	}
}

func TestOAuthFlowErrorUsesStableAPIError(t *testing.T) {
	flows := &testOAuthFlowService{
		credential: oauthCredentialForHTTPTest(),
		consumeErr: &contracts.OAuthFlowError{Code: "oauth_flow_pending", Message: "OAuth flow is still pending"},
	}
	_, router := terminalRouterWithOAuthFlows(t, testConnectionValidator{}, flows)
	request := httptest.NewRequest(http.MethodPost, "/api/connections", bytes.NewBufferString(`{
		"name":"pending","provider":"alicloud","site":"cn",
		"credential":{"type":"oauth","values":{"flow_id":"oauth-flow-a"}}
	}`))
	request.Header.Set("Authorization", "Bearer admin-token")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), `"code":"oauth_flow_pending"`) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestOAuthConnectionCreationFailureKeepsFlowRetryable(t *testing.T) {
	flows := &testOAuthFlowService{credential: oauthCredentialForHTTPTest()}
	_, router := terminalRouterWithOAuthFlows(t, testConnectionValidator{}, flows)
	flows.consumeErr = errors.New("temporary persistence failure")
	failed := httptest.NewRequest(http.MethodPost, "/api/connections", bytes.NewBufferString(`{
		"name":"retry","provider":"alicloud","site":"cn",
		"credential":{"type":"oauth","values":{"flow_id":"oauth-flow-a"}}
	}`))
	failed.Header.Set("Authorization", "Bearer admin-token")
	failedResponse := httptest.NewRecorder()
	router.ServeHTTP(failedResponse, failed)
	if failedResponse.Code != http.StatusInternalServerError {
		t.Fatalf("failed status=%d body=%s", failedResponse.Code, failedResponse.Body.String())
	}

	flows.consumeErr = nil
	retry := httptest.NewRequest(http.MethodPost, "/api/connections", bytes.NewBufferString(`{
		"name":"retry","provider":"alicloud","site":"cn",
		"credential":{"type":"oauth","values":{"flow_id":"oauth-flow-a"}}
	}`))
	retry.Header.Set("Authorization", "Bearer admin-token")
	retryResponse := httptest.NewRecorder()
	router.ServeHTTP(retryResponse, retry)
	if retryResponse.Code != http.StatusCreated || flows.consumeCount != 2 {
		t.Fatalf("retry status=%d consume=%d body=%s", retryResponse.Code, flows.consumeCount, retryResponse.Body.String())
	}
}

func oauthCredentialForHTTPTest() contracts.Credential {
	return contracts.Credential{
		Type: asset.CredentialAliCloudOAuth,
		Values: map[string]string{
			"site":                      "cn",
			"oauth_access_token":        "access-token-secret",
			"oauth_refresh_token":       "refresh-token-secret",
			"oauth_access_token_expire": "1785157200",
			"access_key_id":             "sts-id",
			"access_key_secret":         "sts-secret",
			"security_token":            "sts-token-secret",
			"sts_expiration":            "1785157200",
		},
	}
}
