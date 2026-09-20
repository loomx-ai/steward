package oauth

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

	"github.com/loomx-ai/steward/internal/provider/contracts"
)

const (
	loopbackPortStart = 12345
	loopbackPortEnd   = 12349
	flowTTL           = 5 * time.Minute
)

type flow struct {
	id               string
	subject          string
	state            string
	redirectURI      string
	authorizationURL string
	exchange         func(ctx context.Context, code string) (Session, error)
	status           contracts.OAuthFlowStatus
	errorCode        string
	expiresAt        time.Time
	listener         net.Listener
	server           *http.Server
	timer            *time.Timer
	session          Session
	callbackRunning  bool
	consuming        bool
}

// FlowManager runs one provider's browser authorizations. Each flow owns a
// loopback listener that is closed the moment the flow leaves the pending
// state, so an abandoned authorization cannot hold a port.
type FlowManager struct {
	mu     sync.Mutex
	driver Driver
	now    func() time.Time
	ttl    time.Duration
	ports  []int
	flows  map[string]*flow
	closed bool
}

type FlowOption func(*FlowManager)

// WithFlowPorts replaces the loopback port range. Tests use it to avoid the
// fixed range a developer's own CLI session may already hold.
func WithFlowPorts(ports []int) FlowOption {
	return func(manager *FlowManager) {
		manager.ports = append([]int(nil), ports...)
	}
}

// WithFlowClock replaces the clock used for expiry.
func WithFlowClock(now func() time.Time) FlowOption {
	return func(manager *FlowManager) {
		manager.now = now
	}
}

func NewFlowManager(driver Driver, options ...FlowOption) *FlowManager {
	ports := make([]int, 0, loopbackPortEnd-loopbackPortStart+1)
	for port := loopbackPortStart; port <= loopbackPortEnd; port++ {
		ports = append(ports, port)
	}
	manager := &FlowManager{
		driver: driver,
		now:    time.Now,
		ttl:    flowTTL,
		ports:  ports,
		flows:  make(map[string]*flow),
	}
	for _, option := range options {
		if option != nil {
			option(manager)
		}
	}
	if manager.now == nil {
		manager.now = time.Now
	}
	return manager
}

var _ contracts.OAuthFlowService = (*FlowManager)(nil)

func (m *FlowManager) Start(
	ctx context.Context,
	subject string,
	params map[string]string,
) (contracts.OAuthFlowView, error) {
	if err := ctx.Err(); err != nil {
		return contracts.OAuthFlowView{}, err
	}
	subject = strings.TrimSpace(subject)
	if subject == "" {
		return contracts.OAuthFlowView{}, FlowError("oauth_subject_required", "OAuth flow subject is required")
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return contracts.OAuthFlowView{}, FlowError("oauth_flow_unavailable", "OAuth flow service is unavailable")
	}
	m.mu.Unlock()

	listener, err := m.listen()
	if err != nil {
		return contracts.OAuthFlowView{}, err
	}
	id, state, verifier, err := flowSecrets()
	if err != nil {
		_ = listener.Close()
		return contracts.OAuthFlowView{}, err
	}
	challengeBytes := sha256.Sum256([]byte(verifier))
	port := listener.Addr().(*net.TCPAddr).Port
	callbackPath := m.driver.CallbackPath()
	if !strings.HasPrefix(callbackPath, "/") {
		callbackPath = "/" + callbackPath
	}
	redirectURI := "http://127.0.0.1:" + strconv.Itoa(port) + callbackPath
	authorization, err := m.driver.Authorize(ctx, params, Request{
		RedirectURI:   redirectURI,
		State:         state,
		CodeChallenge: base64.RawURLEncoding.EncodeToString(challengeBytes[:]),
		CodeVerifier:  verifier,
	})
	if err != nil {
		_ = listener.Close()
		return contracts.OAuthFlowView{}, err
	}
	if strings.TrimSpace(authorization.URL) == "" || authorization.Exchange == nil {
		_ = listener.Close()
		return contracts.OAuthFlowView{}, FlowError(
			"oauth_flow_unavailable",
			"OAuth driver did not produce an authorization request",
		)
	}
	now := m.now().UTC()
	current := &flow{
		id:               "oauth_" + id,
		subject:          subject,
		state:            state,
		redirectURI:      redirectURI,
		authorizationURL: authorization.URL,
		exchange:         authorization.Exchange,
		status:           contracts.OAuthFlowPending,
		expiresAt:        now.Add(m.ttl),
		listener:         listener,
	}
	mux := http.NewServeMux()
	mux.HandleFunc(callbackPath, func(response http.ResponseWriter, request *http.Request) {
		m.handleCallback(current.id, response, request)
	})
	current.server = &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		_ = listener.Close()
		return contracts.OAuthFlowView{}, FlowError("oauth_flow_unavailable", "OAuth flow service is unavailable")
	}
	m.flows[current.id] = current
	current.timer = time.AfterFunc(m.ttl, func() { m.expire(current.id) })
	m.mu.Unlock()
	go func() {
		err := current.server.Serve(listener)
		if err != nil && !errors.Is(err, http.ErrServerClosed) && !errors.Is(err, net.ErrClosed) {
			m.fail(current.id, "oauth_loopback_failed")
		}
	}()
	return view(current), nil
}

func (m *FlowManager) Get(
	_ context.Context,
	subject string,
	id string,
) (contracts.OAuthFlowView, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	current, err := m.ownedFlowLocked(strings.TrimSpace(subject), strings.TrimSpace(id))
	if err != nil {
		return contracts.OAuthFlowView{}, err
	}
	m.expireLocked(current)
	return view(current), nil
}

// Targets reads the cloud scopes an authorized flow can reach. It leaves the
// flow authorized: listing targets is a read, and the operator may look more
// than once before choosing.
func (m *FlowManager) Targets(
	ctx context.Context,
	subject string,
	id string,
) ([]contracts.OAuthTarget, error) {
	m.mu.Lock()
	current, err := m.ownedFlowLocked(strings.TrimSpace(subject), strings.TrimSpace(id))
	if err != nil {
		m.mu.Unlock()
		return nil, err
	}
	m.expireLocked(current)
	if err := authorizedLocked(current); err != nil {
		m.mu.Unlock()
		return nil, err
	}
	session := current.session
	m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	targets, err := session.Targets(ctx)
	if err != nil {
		return nil, FlowError("oauth_targets_unavailable", "The authorized identity's cloud scopes could not be listed")
	}
	return targets, nil
}

func (m *FlowManager) Consume(
	ctx context.Context,
	subject string,
	id string,
	targetID string,
	expect map[string]string,
	consume func(contracts.Credential) error,
) error {
	if consume == nil {
		return FlowError("oauth_consumer_required", "OAuth credential consumer is required")
	}
	id = strings.TrimSpace(id)
	m.mu.Lock()
	current, err := m.ownedFlowLocked(strings.TrimSpace(subject), id)
	if err != nil {
		m.mu.Unlock()
		return err
	}
	m.expireLocked(current)
	if err := authorizedLocked(current); err != nil {
		m.mu.Unlock()
		return err
	}
	if current.consuming {
		m.mu.Unlock()
		return FlowError("oauth_flow_busy", "OAuth flow is already being consumed")
	}
	current.consuming = true
	session := current.session
	m.mu.Unlock()

	if err := ctx.Err(); err != nil {
		m.resetConsuming(id)
		return err
	}
	value, err := session.Credential(ctx, strings.TrimSpace(targetID))
	if err != nil {
		m.resetConsuming(id)
		var flowErr *contracts.OAuthFlowError
		if errors.As(err, &flowErr) {
			return err
		}
		return FlowError("oauth_target_invalid", "The selected cloud scope could not be used")
	}
	if err := matches(value, expect); err != nil {
		m.resetConsuming(id)
		return err
	}
	if err := consume(cloneCredential(value)); err != nil {
		m.resetConsuming(id)
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	stored, exists := m.flows[id]
	if !exists || stored != current {
		return FlowError("oauth_flow_not_found", "OAuth flow was not found")
	}
	stored.consuming = false
	stored.status = contracts.OAuthFlowConsumed
	stored.authorizationURL = ""
	stored.session = nil
	stored.exchange = nil
	return nil
}

func (m *FlowManager) Close(ctx context.Context) error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	servers := make([]*http.Server, 0, len(m.flows))
	for _, current := range m.flows {
		if current.timer != nil {
			current.timer.Stop()
		}
		current.status = contracts.OAuthFlowExpired
		current.authorizationURL = ""
		current.session = nil
		current.exchange = nil
		if current.server != nil {
			servers = append(servers, current.server)
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

func (m *FlowManager) handleCallback(id string, response http.ResponseWriter, request *http.Request) {
	state := request.URL.Query().Get("state")
	code := strings.TrimSpace(request.URL.Query().Get("code"))
	m.mu.Lock()
	current, exists := m.flows[id]
	if !exists {
		m.mu.Unlock()
		http.Error(response, "Authorization flow not found.", http.StatusNotFound)
		return
	}
	m.expireLocked(current)
	if current.status == contracts.OAuthFlowExpired {
		m.mu.Unlock()
		http.Error(response, "Authorization flow expired.", http.StatusGone)
		return
	}
	if current.status != contracts.OAuthFlowPending || current.callbackRunning {
		m.mu.Unlock()
		http.Error(response, "Authorization flow is not pending.", http.StatusConflict)
		return
	}
	if subtle.ConstantTimeCompare([]byte(state), []byte(current.state)) != 1 {
		m.failLocked(current, "oauth_state_invalid")
		m.mu.Unlock()
		http.Error(response, "Invalid OAuth state.", http.StatusBadRequest)
		return
	}
	if code == "" {
		m.failLocked(current, "oauth_code_missing")
		m.mu.Unlock()
		http.Error(response, "Authorization code is missing.", http.StatusBadRequest)
		return
	}
	current.callbackRunning = true
	exchange := current.exchange
	m.mu.Unlock()

	session, err := exchange(request.Context(), code)
	if err != nil || session == nil {
		m.fail(id, "oauth_token_exchange_failed")
		http.Error(response, "Authorization could not be completed.", http.StatusBadGateway)
		return
	}
	m.mu.Lock()
	current, exists = m.flows[id]
	if !exists {
		m.mu.Unlock()
		http.Error(response, "Authorization flow not found.", http.StatusNotFound)
		return
	}
	m.expireLocked(current)
	if current.status == contracts.OAuthFlowExpired {
		m.mu.Unlock()
		http.Error(response, "Authorization flow expired.", http.StatusGone)
		return
	}
	current.callbackRunning = false
	current.status = contracts.OAuthFlowAuthorized
	current.authorizationURL = ""
	current.session = session
	if current.listener != nil {
		_ = current.listener.Close()
	}
	m.mu.Unlock()
	response.Header().Set("Content-Type", "text/plain; charset=utf-8")
	response.WriteHeader(http.StatusOK)
	_, _ = response.Write([]byte("Authorization successful. You can close this window."))
}

func (m *FlowManager) listen() (net.Listener, error) {
	for _, port := range m.ports {
		listener, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
		if err == nil {
			return listener, nil
		}
	}
	return nil, FlowError("oauth_loopback_unavailable", "No OAuth loopback port is available")
}

func (m *FlowManager) ownedFlowLocked(subject, id string) (*flow, error) {
	current, exists := m.flows[id]
	if !exists || subject == "" || current.subject != subject {
		return nil, FlowError("oauth_flow_not_found", "OAuth flow was not found")
	}
	return current, nil
}

func (m *FlowManager) expire(id string) {
	m.mu.Lock()
	if current, exists := m.flows[id]; exists {
		m.expireLocked(current)
		if current.status == contracts.OAuthFlowExpired {
			time.AfterFunc(m.ttl, func() { m.deleteExpired(id, current) })
		}
	}
	m.mu.Unlock()
}

func (m *FlowManager) expireLocked(current *flow) {
	if current.status == contracts.OAuthFlowExpired {
		return
	}
	if m.now().UTC().Before(current.expiresAt) {
		return
	}
	current.status = contracts.OAuthFlowExpired
	current.errorCode = ""
	current.authorizationURL = ""
	current.session = nil
	current.exchange = nil
	current.consuming = false
	if current.timer != nil {
		current.timer.Stop()
	}
	if current.listener != nil {
		_ = current.listener.Close()
	}
}

func (m *FlowManager) deleteExpired(id string, expected *flow) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if current, exists := m.flows[id]; exists && current == expected && current.status == contracts.OAuthFlowExpired {
		delete(m.flows, id)
	}
}

func (m *FlowManager) fail(id, code string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if current, exists := m.flows[id]; exists {
		m.failLocked(current, code)
	}
}

func (m *FlowManager) failLocked(current *flow, code string) {
	current.callbackRunning = false
	current.status = contracts.OAuthFlowFailed
	current.errorCode = code
	current.authorizationURL = ""
	current.session = nil
	current.exchange = nil
	if current.listener != nil {
		_ = current.listener.Close()
	}
}

func (m *FlowManager) resetConsuming(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if current, exists := m.flows[id]; exists && current.status == contracts.OAuthFlowAuthorized {
		current.consuming = false
	}
}

func authorizedLocked(current *flow) error {
	switch current.status {
	case contracts.OAuthFlowExpired:
		return FlowError("oauth_flow_expired", "OAuth flow has expired")
	case contracts.OAuthFlowConsumed:
		return FlowError("oauth_flow_consumed", "OAuth flow has already been consumed")
	case contracts.OAuthFlowFailed:
		return FlowError("oauth_flow_failed", "OAuth flow failed")
	case contracts.OAuthFlowPending:
		return FlowError("oauth_flow_pending", "OAuth flow is still pending")
	case contracts.OAuthFlowAuthorized:
		if current.session == nil {
			return FlowError("oauth_flow_invalid", "OAuth flow is invalid")
		}
		return nil
	default:
		return FlowError("oauth_flow_invalid", "OAuth flow is invalid")
	}
}

// matches rejects a credential that does not describe the cloud scope the
// caller expected. Replacing a connection's credential must not move that
// connection to another site, subscription, project or account.
func matches(value contracts.Credential, expect map[string]string) error {
	for key, expected := range expect {
		if strings.TrimSpace(expected) == "" {
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(value.Values[key]), strings.TrimSpace(expected)) {
			return FlowError(
				"oauth_target_mismatch",
				"The authorization does not belong to this connection's cloud scope",
			)
		}
	}
	return nil
}

func view(current *flow) contracts.OAuthFlowView {
	value := contracts.OAuthFlowView{
		ID:        current.id,
		Status:    current.status,
		ExpiresAt: current.expiresAt,
		ErrorCode: current.errorCode,
	}
	if current.status == contracts.OAuthFlowPending {
		value.AuthorizationURL = current.authorizationURL
	}
	return value
}

func flowSecrets() (id string, state string, verifier string, err error) {
	if id, err = RandomString(32); err != nil {
		return "", "", "", err
	}
	if state, err = RandomString(32); err != nil {
		return "", "", "", err
	}
	if verifier, err = RandomString(96); err != nil {
		return "", "", "", err
	}
	return id, state, verifier, nil
}

// RandomString returns a URL-safe random string drawn from the given number of
// bytes of entropy.
func RandomString(bytes int) (string, error) {
	value := make([]byte, bytes)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("generate OAuth random value: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

// FlowError builds the transport-mapped error every driver and the manager
// share, so one error code table serves every provider.
func FlowError(code, message string) error {
	return &contracts.OAuthFlowError{Code: code, Message: message}
}

func cloneCredential(value contracts.Credential) contracts.Credential {
	cloned := value
	cloned.Values = CloneValues(value.Values)
	return cloned
}
