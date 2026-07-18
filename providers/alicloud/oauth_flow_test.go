package alicloud

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

type oauthFlowAPIStub struct {
	mu            sync.Mutex
	redirectURI   string
	state         string
	challenge     string
	code          string
	verifier      string
	authorization string
}

func (s *oauthFlowAPIStub) AuthorizationURL(_ asset.ConnectionSite, redirectURI, state, challenge string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.redirectURI = redirectURI
	s.state = state
	s.challenge = challenge
	query := url.Values{"redirect_uri": {redirectURI}, "state": {state}}
	s.authorization = "https://signin.example.test/oauth2/v1/auth?" + query.Encode()
	return s.authorization, nil
}

func (s *oauthFlowAPIStub) ExchangeAuthorizationCode(_ context.Context, _ asset.ConnectionSite, code, redirectURI, verifier string) (oauthTokens, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.code = code
	s.verifier = verifier
	if redirectURI != s.redirectURI {
		return oauthTokens{}, errors.New("redirect URI changed")
	}
	return oauthTokens{
		AccessToken: "oauth-access-secret", RefreshToken: "oauth-refresh-secret",
		ExpiresAt: time.Date(2026, 7, 27, 17, 0, 0, 0, time.UTC),
	}, nil
}

func (*oauthFlowAPIStub) Refresh(context.Context, asset.ConnectionSite, string) (oauthTokens, error) {
	return oauthTokens{}, errors.New("unexpected refresh")
}

func (*oauthFlowAPIStub) ExchangeSTS(context.Context, asset.ConnectionSite, string) (oauthSTS, error) {
	return oauthSTS{
		AccessKeyID: "sts-id", AccessKeySecret: "sts-secret", SecurityToken: "sts-token",
		ExpiresAt: time.Date(2026, 7, 27, 16, 30, 0, 0, time.UTC),
	}, nil
}

func TestOAuthFlowAuthorizesAndConsumesCredentialOnce(t *testing.T) {
	now := time.Date(2026, 7, 27, 16, 0, 0, 0, time.UTC)
	api := &oauthFlowAPIStub{}
	manager := newOAuthFlowManager(
		api,
		withOAuthFlowPorts(freeLoopbackPorts(t, 2)),
		withOAuthFlowClock(func() time.Time { return now }),
	)
	defer manager.Close(context.Background())

	started, err := manager.Start(context.Background(), "admin-a", asset.ConnectionSiteINTL)
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
	api.mu.Lock()
	redirectURI, state, challenge := api.redirectURI, api.state, api.challenge
	api.mu.Unlock()
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
	for _, secret := range []string{"oauth-access-secret", "oauth-refresh-secret", "sts-secret", "sts-token", "authorization-code"} {
		if strings.Contains(string(body), secret) {
			t.Fatalf("callback response exposed %q: %s", secret, body)
		}
	}
	view, err := manager.Get(context.Background(), "admin-a", started.ID)
	if err != nil || view.Status != contracts.OAuthFlowAuthorized || view.AuthorizationURL != "" {
		t.Fatalf("authorized flow = %#v, err = %v", view, err)
	}
	if _, err := manager.Get(context.Background(), "admin-b", started.ID); oauthFlowErrorCode(err) != "oauth_flow_not_found" {
		t.Fatalf("cross-subject Get error = %v", err)
	}

	callbackErr := errors.New("database unavailable")
	if err := manager.Consume(context.Background(), "admin-a", started.ID, asset.ConnectionSiteINTL, func(contracts.Credential) error {
		return callbackErr
	}); !errors.Is(err, callbackErr) {
		t.Fatalf("failed Consume error = %v", err)
	}
	view, err = manager.Get(context.Background(), "admin-a", started.ID)
	if err != nil || view.Status != contracts.OAuthFlowAuthorized {
		t.Fatalf("flow after failed consume = %#v, err = %v", view, err)
	}
	var consumed contracts.Credential
	if err := manager.Consume(context.Background(), "admin-a", started.ID, asset.ConnectionSiteINTL, func(value contracts.Credential) error {
		consumed = value
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if consumed.Type != asset.CredentialAliCloudOAuth ||
		consumed.Values[oauthSiteKey] != string(asset.ConnectionSiteINTL) ||
		consumed.Values[oauthAccessTokenKey] != "oauth-access-secret" ||
		consumed.Values["access_key_id"] != "sts-id" ||
		consumed.ExpiresAt != nil {
		t.Fatalf("consumed credential = %#v", consumed)
	}
	view, err = manager.Get(context.Background(), "admin-a", started.ID)
	if err != nil || view.Status != contracts.OAuthFlowConsumed {
		t.Fatalf("consumed flow = %#v, err = %v", view, err)
	}
	if err := manager.Consume(context.Background(), "admin-a", started.ID, asset.ConnectionSiteINTL, func(contracts.Credential) error {
		t.Fatal("consume callback ran twice")
		return nil
	}); oauthFlowErrorCode(err) != "oauth_flow_consumed" {
		t.Fatalf("second Consume error = %v", err)
	}
	manager.mu.Lock()
	stored := manager.flows[started.ID].credential
	manager.mu.Unlock()
	if stored.Type != "" || len(stored.Values) != 0 {
		t.Fatalf("consumed credential was not cleared: %#v", stored)
	}
}

func TestOAuthFlowRejectsInvalidCallbackAndExpires(t *testing.T) {
	now := time.Date(2026, 7, 27, 16, 0, 0, 0, time.UTC)
	api := &oauthFlowAPIStub{}
	manager := newOAuthFlowManager(
		api,
		withOAuthFlowPorts(freeLoopbackPorts(t, 2)),
		withOAuthFlowClock(func() time.Time { return now }),
	)
	defer manager.Close(context.Background())

	started, err := manager.Start(context.Background(), "admin", asset.ConnectionSiteCN)
	if err != nil {
		t.Fatal(err)
	}
	api.mu.Lock()
	redirectURI := api.redirectURI
	api.mu.Unlock()
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

	expiring, err := manager.Start(context.Background(), "admin", asset.ConnectionSiteCN)
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(6 * time.Minute)
	view, err = manager.Get(context.Background(), "admin", expiring.ID)
	if err != nil || view.Status != contracts.OAuthFlowExpired {
		t.Fatalf("expired flow = %#v, err = %v", view, err)
	}
}

func TestOAuthFlowSelectsAvailablePortAndReportsExhaustion(t *testing.T) {
	ports := freeLoopbackPorts(t, 6)
	first, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(ports[0])))
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()

	api := &oauthFlowAPIStub{}
	manager := newOAuthFlowManager(api, withOAuthFlowPorts(ports[:2]))
	defer manager.Close(context.Background())
	if _, err := manager.Start(context.Background(), "admin", asset.ConnectionSiteCN); err != nil {
		t.Fatal(err)
	}
	api.mu.Lock()
	redirectURI := api.redirectURI
	api.mu.Unlock()
	if !strings.Contains(redirectURI, fmt.Sprintf(":%d/", ports[1])) {
		t.Fatalf("redirect URI = %q, want port %d", redirectURI, ports[1])
	}

	var occupied []net.Listener
	for _, port := range ports[2:] {
		listener, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
		if err != nil {
			t.Fatal(err)
		}
		occupied = append(occupied, listener)
		defer listener.Close()
	}
	unavailable := newOAuthFlowManager(api, withOAuthFlowPorts(ports[2:]))
	defer unavailable.Close(context.Background())
	if _, err := unavailable.Start(context.Background(), "admin", asset.ConnectionSiteCN); oauthFlowErrorCode(err) != "oauth_loopback_unavailable" {
		t.Fatalf("exhausted ports error = %v", err)
	}
}

func TestOAuthFlowConcurrentConsumeRunsOnlyOneCallback(t *testing.T) {
	api := &oauthFlowAPIStub{}
	manager := newOAuthFlowManager(api, withOAuthFlowPorts(freeLoopbackPorts(t, 1)))
	defer manager.Close(context.Background())
	started, err := manager.Start(context.Background(), "admin", asset.ConnectionSiteCN)
	if err != nil {
		t.Fatal(err)
	}
	api.mu.Lock()
	redirectURI, state := api.redirectURI, api.state
	api.mu.Unlock()
	response, err := http.Get(redirectURI + "?state=" + url.QueryEscape(state) + "&code=code")
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()

	release := make(chan struct{})
	startedCallback := make(chan struct{})
	var calls atomic.Int32
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- manager.Consume(context.Background(), "admin", started.ID, asset.ConnectionSiteCN, func(contracts.Credential) error {
			calls.Add(1)
			close(startedCallback)
			<-release
			return nil
		})
	}()
	<-startedCallback
	secondErr := manager.Consume(context.Background(), "admin", started.ID, asset.ConnectionSiteCN, func(contracts.Credential) error {
		calls.Add(1)
		return nil
	})
	close(release)
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
	if oauthFlowErrorCode(secondErr) != "oauth_flow_busy" || calls.Load() != 1 {
		t.Fatalf("second error = %v, callback calls = %d", secondErr, calls.Load())
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

func oauthFlowErrorCode(err error) string {
	var flowErr *contracts.OAuthFlowError
	if errors.As(err, &flowErr) {
		return flowErr.Code
	}
	return ""
}
