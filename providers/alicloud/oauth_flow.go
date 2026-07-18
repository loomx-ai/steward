package alicloud

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

const (
	oauthLoopbackPortStart = 12345
	oauthLoopbackPortEnd   = 12349
	oauthFlowTTL           = 5 * time.Minute
)

type oauthFlow struct {
	id               string
	subject          string
	site             asset.ConnectionSite
	state            string
	redirectURI      string
	authorizationURL string
	codeVerifier     string
	status           contracts.OAuthFlowStatus
	errorCode        string
	expiresAt        time.Time
	listener         net.Listener
	server           *http.Server
	timer            *time.Timer
	credential       contracts.Credential
	callbackRunning  bool
	consuming        bool
}

type OAuthFlowManager struct {
	mu     sync.Mutex
	api    oauthAPI
	now    func() time.Time
	ttl    time.Duration
	ports  []int
	flows  map[string]*oauthFlow
	closed bool
}

type oauthFlowOption func(*OAuthFlowManager)

func withOAuthFlowPorts(ports []int) oauthFlowOption {
	return func(manager *OAuthFlowManager) {
		manager.ports = append([]int(nil), ports...)
	}
}

func withOAuthFlowClock(now func() time.Time) oauthFlowOption {
	return func(manager *OAuthFlowManager) {
		manager.now = now
	}
}

func NewOAuthFlowManager() *OAuthFlowManager {
	now := time.Now
	return newOAuthFlowManager(
		newOAuthClient(&http.Client{Timeout: 30 * time.Second}, now),
		withOAuthFlowClock(now),
	)
}

func newOAuthFlowManager(api oauthAPI, options ...oauthFlowOption) *OAuthFlowManager {
	ports := make([]int, 0, oauthLoopbackPortEnd-oauthLoopbackPortStart+1)
	for port := oauthLoopbackPortStart; port <= oauthLoopbackPortEnd; port++ {
		ports = append(ports, port)
	}
	manager := &OAuthFlowManager{
		api:   api,
		now:   time.Now,
		ttl:   oauthFlowTTL,
		ports: ports,
		flows: make(map[string]*oauthFlow),
	}
	for _, option := range options {
		if option != nil {
			option(manager)
		}
	}
	return manager
}

var _ contracts.OAuthFlowService = (*OAuthFlowManager)(nil)

func (m *OAuthFlowManager) Start(
	ctx context.Context,
	subject string,
	site asset.ConnectionSite,
) (contracts.OAuthFlowView, error) {
	if err := ctx.Err(); err != nil {
		return contracts.OAuthFlowView{}, err
	}
	subject = strings.TrimSpace(subject)
	if subject == "" {
		return contracts.OAuthFlowView{}, oauthFlowError("oauth_subject_required", "OAuth flow subject is required")
	}
	if site != asset.ConnectionSiteCN && site != asset.ConnectionSiteINTL {
		return contracts.OAuthFlowView{}, oauthFlowError("invalid_connection_site", "Alibaba Cloud site is invalid")
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return contracts.OAuthFlowView{}, oauthFlowError("oauth_flow_unavailable", "OAuth flow service is unavailable")
	}
	m.mu.Unlock()

	listener, err := m.listen()
	if err != nil {
		return contracts.OAuthFlowView{}, err
	}
	id, err := randomOAuthString(32)
	if err != nil {
		_ = listener.Close()
		return contracts.OAuthFlowView{}, err
	}
	state, err := randomOAuthString(32)
	if err != nil {
		_ = listener.Close()
		return contracts.OAuthFlowView{}, err
	}
	verifier, err := randomOAuthString(96)
	if err != nil {
		_ = listener.Close()
		return contracts.OAuthFlowView{}, err
	}
	challengeBytes := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(challengeBytes[:])
	port := listener.Addr().(*net.TCPAddr).Port
	redirectURI := "http://127.0.0.1:" + strconv.Itoa(port) + "/cli/callback"
	authorizationURL, err := m.api.AuthorizationURL(site, redirectURI, state, challenge)
	if err != nil {
		_ = listener.Close()
		return contracts.OAuthFlowView{}, err
	}
	now := m.now().UTC()
	flow := &oauthFlow{
		id:               "oauth_" + id,
		subject:          subject,
		site:             site,
		state:            state,
		redirectURI:      redirectURI,
		authorizationURL: authorizationURL,
		codeVerifier:     verifier,
		status:           contracts.OAuthFlowPending,
		expiresAt:        now.Add(m.ttl),
		listener:         listener,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/cli/callback", func(response http.ResponseWriter, request *http.Request) {
		m.handleCallback(flow.id, response, request)
	})
	flow.server = &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		_ = listener.Close()
		return contracts.OAuthFlowView{}, oauthFlowError("oauth_flow_unavailable", "OAuth flow service is unavailable")
	}
	m.flows[flow.id] = flow
	flow.timer = time.AfterFunc(m.ttl, func() { m.expire(flow.id) })
	m.mu.Unlock()
	go func() {
		err := flow.server.Serve(listener)
		if err != nil && !errors.Is(err, http.ErrServerClosed) && !errors.Is(err, net.ErrClosed) {
			m.fail(flow.id, "oauth_loopback_failed")
		}
	}()
	return flowView(flow), nil
}

func (m *OAuthFlowManager) Get(
	_ context.Context,
	subject string,
	id string,
) (contracts.OAuthFlowView, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	flow, err := m.ownedFlowLocked(strings.TrimSpace(subject), strings.TrimSpace(id))
	if err != nil {
		return contracts.OAuthFlowView{}, err
	}
	m.expireLocked(flow)
	return flowView(flow), nil
}

func (m *OAuthFlowManager) Consume(
	ctx context.Context,
	subject string,
	id string,
	site asset.ConnectionSite,
	consume func(contracts.Credential) error,
) error {
	if consume == nil {
		return oauthFlowError("oauth_consumer_required", "OAuth credential consumer is required")
	}
	m.mu.Lock()
	flow, err := m.ownedFlowLocked(strings.TrimSpace(subject), strings.TrimSpace(id))
	if err != nil {
		m.mu.Unlock()
		return err
	}
	m.expireLocked(flow)
	switch flow.status {
	case contracts.OAuthFlowExpired:
		m.mu.Unlock()
		return oauthFlowError("oauth_flow_expired", "OAuth flow has expired")
	case contracts.OAuthFlowConsumed:
		m.mu.Unlock()
		return oauthFlowError("oauth_flow_consumed", "OAuth flow has already been consumed")
	case contracts.OAuthFlowFailed:
		m.mu.Unlock()
		return oauthFlowError("oauth_flow_failed", "OAuth flow failed")
	case contracts.OAuthFlowPending:
		m.mu.Unlock()
		return oauthFlowError("oauth_flow_pending", "OAuth flow is still pending")
	case contracts.OAuthFlowAuthorized:
	default:
		m.mu.Unlock()
		return oauthFlowError("oauth_flow_invalid", "OAuth flow is invalid")
	}
	if flow.site != site {
		m.mu.Unlock()
		return oauthFlowError("invalid_connection_site", "OAuth flow site does not match the connection site")
	}
	if flow.consuming {
		m.mu.Unlock()
		return oauthFlowError("oauth_flow_busy", "OAuth flow is already being consumed")
	}
	flow.consuming = true
	value := cloneOAuthCredential(flow.credential)
	m.mu.Unlock()

	if err := ctx.Err(); err != nil {
		m.resetConsuming(id)
		return err
	}
	if err := consume(value); err != nil {
		m.resetConsuming(id)
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	current, exists := m.flows[id]
	if !exists || current != flow {
		return oauthFlowError("oauth_flow_not_found", "OAuth flow was not found")
	}
	current.consuming = false
	current.status = contracts.OAuthFlowConsumed
	current.authorizationURL = ""
	current.credential = contracts.Credential{}
	return nil
}

func (m *OAuthFlowManager) Close(ctx context.Context) error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	servers := make([]*http.Server, 0, len(m.flows))
	for _, flow := range m.flows {
		if flow.timer != nil {
			flow.timer.Stop()
		}
		flow.status = contracts.OAuthFlowExpired
		flow.authorizationURL = ""
		flow.credential = contracts.Credential{}
		if flow.server != nil {
			servers = append(servers, flow.server)
		}
	}
	m.mu.Unlock()
	var firstErr error
	for _, server := range servers {
		if err := server.Shutdown(ctx); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (m *OAuthFlowManager) handleCallback(id string, response http.ResponseWriter, request *http.Request) {
	state := request.URL.Query().Get("state")
	code := strings.TrimSpace(request.URL.Query().Get("code"))
	m.mu.Lock()
	flow, exists := m.flows[id]
	if !exists {
		m.mu.Unlock()
		http.Error(response, "Authorization flow not found.", http.StatusNotFound)
		return
	}
	m.expireLocked(flow)
	if flow.status == contracts.OAuthFlowExpired {
		m.mu.Unlock()
		http.Error(response, "Authorization flow expired.", http.StatusGone)
		return
	}
	if flow.status != contracts.OAuthFlowPending || flow.callbackRunning {
		m.mu.Unlock()
		http.Error(response, "Authorization flow is not pending.", http.StatusConflict)
		return
	}
	if subtle.ConstantTimeCompare([]byte(state), []byte(flow.state)) != 1 {
		m.failLocked(flow, "oauth_state_invalid")
		m.mu.Unlock()
		http.Error(response, "Invalid OAuth state.", http.StatusBadRequest)
		return
	}
	if code == "" {
		m.failLocked(flow, "oauth_code_missing")
		m.mu.Unlock()
		http.Error(response, "Authorization code is missing.", http.StatusBadRequest)
		return
	}
	flow.callbackRunning = true
	site := flow.site
	redirectURI := flow.redirectURI
	verifier := flow.codeVerifier
	m.mu.Unlock()

	tokens, err := m.api.ExchangeAuthorizationCode(request.Context(), site, code, redirectURI, verifier)
	if err != nil {
		m.finishCallbackFailure(id, "oauth_token_exchange_failed")
		http.Error(response, "Authorization could not be completed.", http.StatusBadGateway)
		return
	}
	sts, err := m.api.ExchangeSTS(request.Context(), site, tokens.AccessToken)
	if err != nil {
		m.finishCallbackFailure(id, "oauth_sts_exchange_failed")
		http.Error(response, "Authorization could not be completed.", http.StatusBadGateway)
		return
	}
	value := contracts.Credential{
		Type: asset.CredentialAliCloudOAuth,
		Values: map[string]string{
			oauthSiteKey:              string(site),
			oauthAccessTokenKey:       tokens.AccessToken,
			oauthRefreshTokenKey:      tokens.RefreshToken,
			oauthAccessTokenExpireKey: formatOAuthUnix(tokens.ExpiresAt),
			"access_key_id":           sts.AccessKeyID,
			"access_key_secret":       sts.AccessKeySecret,
			"security_token":          sts.SecurityToken,
			oauthSTSExpireKey:         formatOAuthUnix(sts.ExpiresAt),
		},
	}
	m.mu.Lock()
	flow, exists = m.flows[id]
	if !exists {
		m.mu.Unlock()
		http.Error(response, "Authorization flow not found.", http.StatusNotFound)
		return
	}
	m.expireLocked(flow)
	if flow.status == contracts.OAuthFlowExpired {
		m.mu.Unlock()
		http.Error(response, "Authorization flow expired.", http.StatusGone)
		return
	}
	flow.callbackRunning = false
	flow.status = contracts.OAuthFlowAuthorized
	flow.authorizationURL = ""
	flow.credential = value
	if flow.listener != nil {
		_ = flow.listener.Close()
	}
	m.mu.Unlock()
	response.Header().Set("Content-Type", "text/plain; charset=utf-8")
	response.WriteHeader(http.StatusOK)
	_, _ = response.Write([]byte("Authorization successful. You can close this window."))
}

func (m *OAuthFlowManager) listen() (net.Listener, error) {
	for _, port := range m.ports {
		listener, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
		if err == nil {
			return listener, nil
		}
	}
	return nil, oauthFlowError("oauth_loopback_unavailable", "No Alibaba Cloud OAuth loopback port is available")
}

func (m *OAuthFlowManager) ownedFlowLocked(subject, id string) (*oauthFlow, error) {
	flow, exists := m.flows[id]
	if !exists || subject == "" || flow.subject != subject {
		return nil, oauthFlowError("oauth_flow_not_found", "OAuth flow was not found")
	}
	return flow, nil
}

func (m *OAuthFlowManager) expire(id string) {
	m.mu.Lock()
	if flow, exists := m.flows[id]; exists {
		m.expireLocked(flow)
		if flow.status == contracts.OAuthFlowExpired {
			time.AfterFunc(m.ttl, func() { m.deleteExpired(id, flow) })
		}
	}
	m.mu.Unlock()
}

func (m *OAuthFlowManager) expireLocked(flow *oauthFlow) {
	if flow.status == contracts.OAuthFlowExpired {
		return
	}
	if m.now().UTC().Before(flow.expiresAt) {
		return
	}
	flow.status = contracts.OAuthFlowExpired
	flow.errorCode = ""
	flow.authorizationURL = ""
	flow.credential = contracts.Credential{}
	flow.consuming = false
	if flow.timer != nil {
		flow.timer.Stop()
	}
	if flow.listener != nil {
		_ = flow.listener.Close()
	}
}

func (m *OAuthFlowManager) deleteExpired(id string, expected *oauthFlow) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if current, exists := m.flows[id]; exists && current == expected && current.status == contracts.OAuthFlowExpired {
		delete(m.flows, id)
	}
}

func (m *OAuthFlowManager) fail(id, code string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if flow, exists := m.flows[id]; exists {
		m.failLocked(flow, code)
	}
}

func (m *OAuthFlowManager) failLocked(flow *oauthFlow, code string) {
	flow.callbackRunning = false
	flow.status = contracts.OAuthFlowFailed
	flow.errorCode = code
	flow.authorizationURL = ""
	flow.credential = contracts.Credential{}
	if flow.listener != nil {
		_ = flow.listener.Close()
	}
}

func (m *OAuthFlowManager) finishCallbackFailure(id, code string) {
	m.fail(id, code)
}

func (m *OAuthFlowManager) resetConsuming(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if flow, exists := m.flows[id]; exists && flow.status == contracts.OAuthFlowAuthorized {
		flow.consuming = false
	}
}

func flowView(flow *oauthFlow) contracts.OAuthFlowView {
	view := contracts.OAuthFlowView{
		ID:        flow.id,
		Status:    flow.status,
		ExpiresAt: flow.expiresAt,
		ErrorCode: flow.errorCode,
	}
	if flow.status == contracts.OAuthFlowPending {
		view.AuthorizationURL = flow.authorizationURL
	}
	return view
}

func cloneOAuthCredential(value contracts.Credential) contracts.Credential {
	cloned := value
	cloned.Values = cloneOAuthValues(value.Values)
	return cloned
}

func randomOAuthString(bytes int) (string, error) {
	value := make([]byte, bytes)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("generate OAuth random value: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func oauthFlowError(code, message string) error {
	return &contracts.OAuthFlowError{Code: code, Message: message}
}
