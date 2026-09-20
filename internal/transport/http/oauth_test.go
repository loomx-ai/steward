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
	mu              sync.Mutex
	startView       contracts.OAuthFlowView
	getView         contracts.OAuthFlowView
	targets         []contracts.OAuthTarget
	credential      contracts.Credential
	consumeErr      error
	startSubject    string
	startParams     map[string]string
	getSubject      string
	getID           string
	targetsSubject  string
	targetsID       string
	consumeCount    int
	consumeExpect   map[string]string
	consumeTargetID string
	consumeID       string
}

func (service *testOAuthFlowService) Start(_ context.Context, subject string, params map[string]string) (contracts.OAuthFlowView, error) {
	service.mu.Lock()
	defer service.mu.Unlock()
	service.startSubject = subject
	service.startParams = params
	return service.startView, nil
}

func (service *testOAuthFlowService) Get(_ context.Context, subject, id string) (contracts.OAuthFlowView, error) {
	service.mu.Lock()
	defer service.mu.Unlock()
	service.getSubject = subject
	service.getID = id
	return service.getView, nil
}

func (service *testOAuthFlowService) Targets(_ context.Context, subject, id string) ([]contracts.OAuthTarget, error) {
	service.mu.Lock()
	defer service.mu.Unlock()
	service.targetsSubject = subject
	service.targetsID = id
	return service.targets, nil
}

func (service *testOAuthFlowService) Consume(
	_ context.Context,
	_ string,
	id string,
	targetID string,
	expect map[string]string,
	consume func(contracts.Credential) error,
) error {
	service.mu.Lock()
	service.consumeCount++
	service.consumeID = id
	service.consumeTargetID = targetID
	service.consumeExpect = expect
	credential := service.credential
	err := service.consumeErr
	service.mu.Unlock()
	if err != nil {
		return err
	}
	return consume(credential)
}

// aliCloudOAuthFlows registers the stub for Alibaba Cloud only, so the tests
// also cover a provider that has no browser authorization at all.
func aliCloudOAuthFlows(flows contracts.OAuthFlowService) map[asset.Provider]contracts.OAuthFlowService {
	return map[asset.Provider]contracts.OAuthFlowService{asset.ProviderAliCloud: flows}
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
	_, router := terminalRouterWithOAuthFlows(t, testConnectionValidator{}, aliCloudOAuthFlows(flows))

	viewerRequest := httptest.NewRequest(http.MethodPost, "/api/providers/alicloud/oauth/flows", bytes.NewBufferString(`{"params":{"site":"intl"}}`))
	viewerRequest.Header.Set("Authorization", "Bearer viewer-token")
	viewerResponse := httptest.NewRecorder()
	router.ServeHTTP(viewerResponse, viewerRequest)
	if viewerResponse.Code != http.StatusForbidden {
		t.Fatalf("viewer status=%d body=%s", viewerResponse.Code, viewerResponse.Body.String())
	}

	startRequest := httptest.NewRequest(http.MethodPost, "/api/providers/alicloud/oauth/flows", bytes.NewBufferString(`{"params":{"site":"intl"}}`))
	startRequest.Header.Set("Authorization", "Bearer admin-token")
	startResponse := httptest.NewRecorder()
	router.ServeHTTP(startResponse, startRequest)
	if startResponse.Code != http.StatusCreated ||
		!strings.Contains(startResponse.Body.String(), `"id":"oauth-flow-a"`) ||
		!strings.Contains(startResponse.Body.String(), `"authorization_url":"https://signin.alibabacloud.com/`) ||
		strings.Contains(startResponse.Body.String(), "access_token") {
		t.Fatalf("start status=%d body=%s", startResponse.Code, startResponse.Body.String())
	}
	if flows.startSubject != "carol" || flows.startParams["site"] != string(asset.ConnectionSiteINTL) {
		t.Fatalf("start subject=%q params=%v", flows.startSubject, flows.startParams)
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
	repositories, router := terminalRouterWithOAuthFlows(t, testConnectionValidator{}, aliCloudOAuthFlows(flows))
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
	if flows.consumeCount != 1 || flows.consumeID != "oauth-flow-a" ||
		flows.consumeExpect["site"] != string(asset.ConnectionSiteCN) {
		t.Fatalf("consume count=%d id=%q expect=%v", flows.consumeCount, flows.consumeID, flows.consumeExpect)
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
	_, router := terminalRouterWithOAuthFlows(t, testConnectionValidator{}, aliCloudOAuthFlows(flows))
	for _, body := range []string{
		`{"name":"direct","provider":"alicloud","site":"cn","credential":{"type":"oauth","values":{"access_token":"secret"}}}`,
		`{"name":"extra","provider":"alicloud","site":"cn","credential":{"type":"oauth","values":{"flow_id":"oauth-flow-a","refresh_token":"secret"}}}`,
		`{"name":"no flow","provider":"alicloud","site":"cn","credential":{"type":"oauth","values":{"target_id":"only"}}}`,
		`{"name":"selection on static","provider":"alicloud","site":"cn","credential":{"type":"access_key","values":{"access_key_id":"a","access_key_secret":"b","flow_id":"oauth-flow-a"}}}`,
	} {
		request := httptest.NewRequest(http.MethodPost, "/api/connections", bytes.NewBufferString(body))
		request.Header.Set("Authorization", "Bearer admin-token")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), `"code":"invalid_credential_fields"`) {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
	}
	// A provider with no registered flow service cannot be authorized at all,
	// which is an availability answer rather than a malformed request.
	unavailable := httptest.NewRequest(http.MethodPost, "/api/connections", bytes.NewBufferString(
		`{"name":"wrong provider","provider":"aws","credential":{"type":"oauth","values":{"flow_id":"oauth-flow-a"}}}`,
	))
	unavailable.Header.Set("Authorization", "Bearer admin-token")
	unavailableResponse := httptest.NewRecorder()
	router.ServeHTTP(unavailableResponse, unavailable)
	if unavailableResponse.Code != http.StatusServiceUnavailable ||
		!strings.Contains(unavailableResponse.Body.String(), `"code":"oauth_flow_unavailable"`) {
		t.Fatalf("status=%d body=%s", unavailableResponse.Code, unavailableResponse.Body.String())
	}
	if flows.consumeCount != 0 {
		t.Fatalf("consume count=%d", flows.consumeCount)
	}
}

func TestOAuthFlowTargetsAreAdminOnlyAndBindTheChosenScope(t *testing.T) {
	flows := &testOAuthFlowService{
		credential: oauthCredentialForHTTPTest(),
		targets: []contracts.OAuthTarget{
			{ID: "sub-a", Name: "Production", Description: "tenant-a"},
			{ID: "sub-b", Name: "Staging"},
		},
	}
	_, router := terminalRouterWithOAuthFlows(t, testConnectionValidator{}, aliCloudOAuthFlows(flows))

	viewer := httptest.NewRequest(http.MethodGet, "/api/providers/alicloud/oauth/flows/oauth-flow-a/targets", nil)
	viewer.Header.Set("Authorization", "Bearer viewer-token")
	viewerResponse := httptest.NewRecorder()
	router.ServeHTTP(viewerResponse, viewer)
	if viewerResponse.Code != http.StatusForbidden {
		t.Fatalf("viewer status=%d body=%s", viewerResponse.Code, viewerResponse.Body.String())
	}

	request := httptest.NewRequest(http.MethodGet, "/api/providers/alicloud/oauth/flows/oauth-flow-a/targets", nil)
	request.Header.Set("Authorization", "Bearer admin-token")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK ||
		!strings.Contains(response.Body.String(), `"id":"sub-a"`) ||
		!strings.Contains(response.Body.String(), `"name":"Production"`) ||
		response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("targets status=%d body=%s", response.Code, response.Body.String())
	}
	if flows.targetsSubject != "carol" || flows.targetsID != "oauth-flow-a" {
		t.Fatalf("targets subject=%q id=%q", flows.targetsSubject, flows.targetsID)
	}

	create := httptest.NewRequest(http.MethodPost, "/api/connections", bytes.NewBufferString(`{
		"name":"scoped","provider":"alicloud","site":"cn",
		"credential":{"type":"oauth","values":{"flow_id":"oauth-flow-a","target_id":"sub-b"}}
	}`))
	create.Header.Set("Authorization", "Bearer admin-token")
	createResponse := httptest.NewRecorder()
	router.ServeHTTP(createResponse, create)
	if createResponse.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", createResponse.Code, createResponse.Body.String())
	}
	if flows.consumeTargetID != "sub-b" {
		t.Fatalf("consume target=%q", flows.consumeTargetID)
	}
}

func TestOAuthCredentialReplacementUsesExistingConnectionSite(t *testing.T) {
	flows := &testOAuthFlowService{credential: oauthCredentialForHTTPTest()}
	repositories, router := terminalRouterWithOAuthFlows(t, testConnectionValidator{}, aliCloudOAuthFlows(flows))
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
	if flows.consumeCount != 1 || flows.consumeExpect["site"] != string(asset.ConnectionSiteCN) {
		t.Fatalf("consume count=%d expect=%v", flows.consumeCount, flows.consumeExpect)
	}
}

func TestOAuthFlowErrorUsesStableAPIError(t *testing.T) {
	flows := &testOAuthFlowService{
		credential: oauthCredentialForHTTPTest(),
		consumeErr: &contracts.OAuthFlowError{Code: "oauth_flow_pending", Message: "OAuth flow is still pending"},
	}
	_, router := terminalRouterWithOAuthFlows(t, testConnectionValidator{}, aliCloudOAuthFlows(flows))
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
	_, router := terminalRouterWithOAuthFlows(t, testConnectionValidator{}, aliCloudOAuthFlows(flows))
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
