package oauth

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

// driverStub stands in for one cloud. It records what the manager handed it so
// the tests can assert that PKCE, state and the loopback redirect reach the
// provider unchanged.
type driverStub struct {
	mu            sync.Mutex
	redirectURI   string
	state         string
	challenge     string
	verifier      string
	code          string
	authorizeErr  error
	exchangeErr   error
	targets       []contracts.OAuthTarget
	targetsErr    error
	credentialErr error
}

func (d *driverStub) Provider() asset.Provider { return asset.ProviderAliCloud }

func (d *driverStub) Authorize(
	_ context.Context,
	params map[string]string,
	request Request,
) (Authorization, error) {
	if d.authorizeErr != nil {
		return Authorization{}, d.authorizeErr
	}
	d.mu.Lock()
	d.redirectURI, d.state, d.challenge, d.verifier =
		request.RedirectURI, request.State, request.CodeChallenge, request.CodeVerifier
	d.mu.Unlock()
	site := params["site"]
	return Authorization{
		URL: "https://signin.example.test/authorize?state=" + url.QueryEscape(request.State),
		Exchange: func(_ context.Context, code string) (Session, error) {
			if d.exchangeErr != nil {
				return nil, d.exchangeErr
			}
			d.mu.Lock()
			d.code = code
			d.mu.Unlock()
			return &sessionStub{driver: d, site: site}, nil
		},
	}, nil
}

func (d *driverStub) request() (redirectURI, state, challenge string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.redirectURI, d.state, d.challenge
}

type sessionStub struct {
	driver *driverStub
	site   string
}

func (s *sessionStub) Targets(context.Context) ([]contracts.OAuthTarget, error) {
	if s.driver.targetsErr != nil {
		return nil, s.driver.targetsErr
	}
	return s.driver.targets, nil
}

func (s *sessionStub) Credential(_ context.Context, targetID string) (contracts.Credential, error) {
	if s.driver.credentialErr != nil {
		return contracts.Credential{}, s.driver.credentialErr
	}
	return contracts.Credential{
		Type: asset.CredentialOAuth,
		Values: map[string]string{
			"site":          s.site,
			"target_id":     targetID,
			AccessTokenKey:  "oauth-access-secret",
			RefreshTokenKey: "oauth-refresh-secret",
		},
	}, nil
}

func TestFlowAuthorizesAndConsumesCredentialOnce(t *testing.T) {
	now := time.Date(2026, 7, 27, 16, 0, 0, 0, time.UTC)
	driver := &driverStub{}
	manager := NewFlowManager(
		driver,
		WithFlowPorts(freeLoopbackPorts(t, 2)),
		WithFlowClock(func() time.Time { return now }),
	)
	defer manager.Close(context.Background())

	started, err := manager.Start(context.Background(), "admin-a", map[string]string{"site": "intl"})
	if err != nil {
		t.Fatal(err)
	}
	if started.Status != contracts.OAuthFlowPending || started.ID == "" ||
		started.AuthorizationURL == "" || !started.ExpiresAt.Equal(now.Add(5*time.Minute)) {
		t.Fatalf("started flow = %#v", started)
	}
	if len(strings.TrimPrefix(started.ID, "oauth_")) < 43 {
		t.Fatalf("flow ID does not contain 32 bytes of entropy: %q", started.ID)
	}
	redirectURI, state, challenge := driver.request()
	if !strings.HasPrefix(redirectURI, "http://127.0.0.1:") || len(state) < 43 || challenge == "" {
		t.Fatalf("redirect=%q state=%q challenge=%q", redirectURI, state, challenge)
	}

	response, err := http.Get(redirectURI + "?state=" + url.QueryEscape(state) + "&code=authorization-code")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("callback status=%d body=%s", response.StatusCode, body)
	}
	for _, secret := range []string{"oauth-access-secret", "oauth-refresh-secret", "authorization-code"} {
		if strings.Contains(string(body), secret) {
			t.Fatalf("callback response exposed %q: %s", secret, body)
		}
	}
	view, err := manager.Get(context.Background(), "admin-a", started.ID)
	if err != nil || view.Status != contracts.OAuthFlowAuthorized || view.AuthorizationURL != "" {
		t.Fatalf("authorized flow = %#v, err = %v", view, err)
	}
	if _, err := manager.Get(context.Background(), "admin-b", started.ID); flowErrorCode(err) != "oauth_flow_not_found" {
		t.Fatalf("cross-subject Get error = %v", err)
	}

	callbackErr := errors.New("database unavailable")
	if err := manager.Consume(context.Background(), "admin-a", started.ID, "", nil, func(contracts.Credential) error {
		return callbackErr
	}); !errors.Is(err, callbackErr) {
		t.Fatalf("failed Consume error = %v", err)
	}
	view, err = manager.Get(context.Background(), "admin-a", started.ID)
	if err != nil || view.Status != contracts.OAuthFlowAuthorized {
		t.Fatalf("flow after failed consume = %#v, err = %v", view, err)
	}
	var consumed contracts.Credential
	if err := manager.Consume(context.Background(), "admin-a", started.ID, "", nil, func(value contracts.Credential) error {
		consumed = value
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if consumed.Type != asset.CredentialOAuth ||
		consumed.Values["site"] != "intl" ||
		consumed.Values[AccessTokenKey] != "oauth-access-secret" ||
		consumed.ExpiresAt != nil {
		t.Fatalf("consumed credential = %#v", consumed)
	}
	view, err = manager.Get(context.Background(), "admin-a", started.ID)
	if err != nil || view.Status != contracts.OAuthFlowConsumed {
		t.Fatalf("consumed flow = %#v, err = %v", view, err)
	}
	if err := manager.Consume(context.Background(), "admin-a", started.ID, "", nil, func(contracts.Credential) error {
		t.Fatal("consume callback ran twice")
		return nil
	}); flowErrorCode(err) != "oauth_flow_consumed" {
		t.Fatalf("second Consume error = %v", err)
	}
	manager.mu.Lock()
	stored := manager.flows[started.ID]
	manager.mu.Unlock()
	if stored.session != nil || stored.exchange != nil {
		t.Fatalf("consumed session was not cleared: %#v", stored)
	}
}

func TestFlowListsTargetsAndBindsTheChosenOne(t *testing.T) {
	driver := &driverStub{targets: []contracts.OAuthTarget{
		{ID: "project-a", Name: "Project A"},
		{ID: "project-b", Name: "Project B"},
	}}
	manager := NewFlowManager(driver, WithFlowPorts(freeLoopbackPorts(t, 1)))
	defer manager.Close(context.Background())

	started, err := manager.Start(context.Background(), "admin", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Targets(context.Background(), "admin", started.ID); flowErrorCode(err) != "oauth_flow_pending" {
		t.Fatalf("Targets before authorization error = %v", err)
	}
	authorize(t, driver)

	targets, err := manager.Targets(context.Background(), "admin", started.ID)
	if err != nil || len(targets) != 2 || targets[0].ID != "project-a" {
		t.Fatalf("targets = %#v, err = %v", targets, err)
	}
	// Listing is a read: the operator may look more than once before choosing.
	if _, err := manager.Targets(context.Background(), "admin", started.ID); err != nil {
		t.Fatalf("second Targets error = %v", err)
	}
	var consumed contracts.Credential
	if err := manager.Consume(context.Background(), "admin", started.ID, "project-b", nil, func(value contracts.Credential) error {
		consumed = value
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if consumed.Values["target_id"] != "project-b" {
		t.Fatalf("consumed credential = %#v", consumed)
	}
}

func TestFlowRejectsCredentialOutsideTheExpectedScope(t *testing.T) {
	driver := &driverStub{}
	manager := NewFlowManager(driver, WithFlowPorts(freeLoopbackPorts(t, 1)))
	defer manager.Close(context.Background())

	started, err := manager.Start(context.Background(), "admin", map[string]string{"site": "cn"})
	if err != nil {
		t.Fatal(err)
	}
	authorize(t, driver)

	ran := false
	err = manager.Consume(
		context.Background(),
		"admin",
		started.ID,
		"",
		map[string]string{"site": "intl"},
		func(contracts.Credential) error { ran = true; return nil },
	)
	if flowErrorCode(err) != "oauth_target_mismatch" || ran {
		t.Fatalf("mismatched Consume error = %v, consumer ran = %v", err, ran)
	}
	// The flow survives a rejected consume so the operator can pick again.
	view, err := manager.Get(context.Background(), "admin", started.ID)
	if err != nil || view.Status != contracts.OAuthFlowAuthorized {
		t.Fatalf("flow after mismatch = %#v, err = %v", view, err)
	}
	if err := manager.Consume(
		context.Background(),
		"admin",
		started.ID,
		"",
		map[string]string{"site": "cn"},
		func(contracts.Credential) error { return nil },
	); err != nil {
		t.Fatalf("matching Consume error = %v", err)
	}
}

func TestFlowRejectsInvalidCallbackAndExpires(t *testing.T) {
	now := time.Date(2026, 7, 27, 16, 0, 0, 0, time.UTC)
	driver := &driverStub{}
	manager := NewFlowManager(
		driver,
		WithFlowPorts(freeLoopbackPorts(t, 2)),
		WithFlowClock(func() time.Time { return now }),
	)
	defer manager.Close(context.Background())

	started, err := manager.Start(context.Background(), "admin", nil)
	if err != nil {
		t.Fatal(err)
	}
	redirectURI, _, _ := driver.request()
	response, err := http.Get(redirectURI + "?state=wrong&code=code")
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("callback status = %d", response.StatusCode)
	}
	view, err := manager.Get(context.Background(), "admin", started.ID)
	if err != nil || view.Status != contracts.OAuthFlowFailed || view.ErrorCode != "oauth_state_invalid" {
		t.Fatalf("failed flow = %#v, err = %v", view, err)
	}

	expiring, err := manager.Start(context.Background(), "admin", nil)
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(6 * time.Minute)
	view, err = manager.Get(context.Background(), "admin", expiring.ID)
	if err != nil || view.Status != contracts.OAuthFlowExpired {
		t.Fatalf("expired flow = %#v, err = %v", view, err)
	}
}

// A driver that cannot build an authorization must not leave a listener behind,
// because the next attempt has to be able to take the same port.
func TestFlowReleasesThePortWhenTheDriverRefuses(t *testing.T) {
	ports := freeLoopbackPorts(t, 1)
	driver := &driverStub{authorizeErr: FlowError("invalid_connection_site", "site is invalid")}
	manager := NewFlowManager(driver, WithFlowPorts(ports))
	defer manager.Close(context.Background())

	if _, err := manager.Start(context.Background(), "admin", nil); flowErrorCode(err) != "invalid_connection_site" {
		t.Fatalf("driver refusal error = %v", err)
	}
	driver.authorizeErr = nil
	if _, err := manager.Start(context.Background(), "admin", nil); err != nil {
		t.Fatalf("retry after refusal error = %v", err)
	}
}

func TestFlowSelectsAvailablePortAndReportsExhaustion(t *testing.T) {
	ports := freeLoopbackPorts(t, 6)
	first, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(ports[0])))
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()

	driver := &driverStub{}
	manager := NewFlowManager(driver, WithFlowPorts(ports[:2]))
	defer manager.Close(context.Background())
	if _, err := manager.Start(context.Background(), "admin", nil); err != nil {
		t.Fatal(err)
	}
	redirectURI, _, _ := driver.request()
	if !strings.Contains(redirectURI, fmt.Sprintf(":%d/", ports[1])) {
		t.Fatalf("redirect URI = %q, want port %d", redirectURI, ports[1])
	}

	for _, port := range ports[2:] {
		listener, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
		if err != nil {
			t.Fatal(err)
		}
		defer listener.Close()
	}
	unavailable := NewFlowManager(driver, WithFlowPorts(ports[2:]))
	defer unavailable.Close(context.Background())
	if _, err := unavailable.Start(context.Background(), "admin", nil); flowErrorCode(err) != "oauth_loopback_unavailable" {
		t.Fatalf("exhausted ports error = %v", err)
	}
}

func TestFlowConcurrentConsumeRunsOnlyOneCallback(t *testing.T) {
	driver := &driverStub{}
	manager := NewFlowManager(driver, WithFlowPorts(freeLoopbackPorts(t, 1)))
	defer manager.Close(context.Background())
	started, err := manager.Start(context.Background(), "admin", nil)
	if err != nil {
		t.Fatal(err)
	}
	authorize(t, driver)

	release := make(chan struct{})
	startedCallback := make(chan struct{})
	var calls atomic.Int32
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- manager.Consume(context.Background(), "admin", started.ID, "", nil, func(contracts.Credential) error {
			calls.Add(1)
			close(startedCallback)
			<-release
			return nil
		})
	}()
	<-startedCallback
	secondErr := manager.Consume(context.Background(), "admin", started.ID, "", nil, func(contracts.Credential) error {
		calls.Add(1)
		return nil
	})
	close(release)
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
	if flowErrorCode(secondErr) != "oauth_flow_busy" || calls.Load() != 1 {
		t.Fatalf("second error = %v, callback calls = %d", secondErr, calls.Load())
	}
}

func authorize(t *testing.T, driver *driverStub) {
	t.Helper()
	redirectURI, state, _ := driver.request()
	response, err := http.Get(redirectURI + "?state=" + url.QueryEscape(state) + "&code=code")
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("callback status = %d", response.StatusCode)
	}
}

func freeLoopbackPorts(t *testing.T, count int) []int {
	t.Helper()
	listeners := make([]net.Listener, 0, count)
	ports := make([]int, 0, count)
	for len(ports) < count {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		listeners = append(listeners, listener)
		ports = append(ports, listener.Addr().(*net.TCPAddr).Port)
	}
	for _, listener := range listeners {
		_ = listener.Close()
	}
	return ports
}

func flowErrorCode(err error) string {
	var flowErr *contracts.OAuthFlowError
	if errors.As(err, &flowErr) {
		return flowErr.Code
	}
	return ""
}
