package httptransport

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLocalAuthenticationBoundary(t *testing.T) {
	for _, addr := range []string{":8585", "0.0.0.0:8585", "[::]:8585", "192.168.1.10:8585", "steward.example:8585"} {
		if _, err := NewLocalAuthenticator(addr); err == nil {
			t.Fatalf("accepted non-loopback listener %s", addr)
		}
	}
	auth, err := NewLocalAuthenticator("127.0.0.1:8585")
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, host, remote, origin, site string
		allowed                          bool
	}{
		{"local CLI", "127.0.0.1:8585", "127.0.0.1:45000", "", "", true},
		{"local browser", "localhost:8585", "127.0.0.1:45000", "http://localhost:8585", "same-origin", true},
		{"IPv6 browser", "[::1]:8585", "[::1]:45000", "http://[::1]:8585", "same-origin", true},
		{"DNS rebinding", "attacker.example:8585", "127.0.0.1:45000", "", "", false},
		{"remote client", "127.0.0.1:8585", "192.0.2.10:45000", "", "", false},
		{"cross origin", "127.0.0.1:8585", "127.0.0.1:45000", "https://attacker.example", "", false},
		{"other local site", "127.0.0.1:8585", "127.0.0.1:45000", "http://127.0.0.1:3000", "same-site", false},
		{"opaque origin", "127.0.0.1:8585", "127.0.0.1:45000", "null", "", false},
		{"cross-site without origin", "127.0.0.1:8585", "127.0.0.1:45000", "", "cross-site", false},
		{"wrong port", "127.0.0.1:3000", "127.0.0.1:45000", "", "", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "http://"+test.host+"/api/connections", nil)
			r.RemoteAddr = test.remote
			r.Header.Set("Origin", test.origin)
			r.Header.Set("Sec-Fetch-Site", test.site)
			principal, err := auth.Authenticate(r)
			if (err == nil) != test.allowed {
				t.Fatalf("allowed=%v error=%v", test.allowed, err)
			}
			if test.allowed && (principal.Subject != "local-admin" || !principalAllows(principal, RoleAdmin)) {
				t.Fatalf("principal=%+v", principal)
			}
		})
	}
}

func TestSessionReportsServerAuthenticationMode(t *testing.T) {
	local, err := NewLocalAuthenticator("127.0.0.1:8585")
	if err != nil {
		t.Fatal(err)
	}
	token := NewStaticBearerAuthenticator([]TokenBinding{{Token: "server-secret", Principal: Principal{Subject: "alice", Roles: []Role{RoleViewer}}}})
	for _, test := range []struct {
		name, mode, bearer string
		authenticator      Authenticator
		authenticated      bool
		subject            string
	}{
		{"local without token", "local", "", local, true, "local-admin"},
		{"token anonymous", "token", "", token, false, ""},
		{"invalid token", "token", "invalid", token, false, ""},
		{"verified token", "token", "server-secret", token, true, "alice"},
	} {
		t.Run(test.name, func(t *testing.T) {
			router := NewRouter(Dependencies{Authenticator: test.authenticator, AuthMode: test.mode})
			r := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8585/api/session", nil)
			r.RemoteAddr = "127.0.0.1:45000"
			if test.bearer != "" {
				r.Header.Set("Authorization", "Bearer "+test.bearer)
			}
			w := httptest.NewRecorder()
			router.ServeHTTP(w, r)
			var body struct {
				Mode          string
				Authenticated bool
				Principal     *Principal
			}
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" || body.Mode != test.mode || body.Authenticated != test.authenticated {
				t.Fatalf("response=%d %s", w.Code, w.Body.String())
			}
			if test.authenticated && (body.Principal == nil || body.Principal.Subject != test.subject) {
				t.Fatalf("principal=%+v", body.Principal)
			}
			if !test.authenticated && body.Principal != nil {
				t.Fatal("anonymous response disclosed a principal")
			}
		})
	}
	request := httptest.NewRequest(http.MethodGet, "http://attacker.example:8585/api/session", nil)
	request.RemoteAddr = "127.0.0.1:45000"
	response := httptest.NewRecorder()
	NewRouter(Dependencies{Authenticator: local, AuthMode: "local"}).ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatal("local session accepted untrusted Host")
	}
}

func TestCloudIdentityRequiresWorkspaceServiceToken(t *testing.T) {
	auth := NewCloudAuthenticator([]TokenBinding{{Token: "workspace-secret", Principal: Principal{Subject: "gateway", Roles: []Role{RoleAdmin}}}})
	for _, test := range []struct {
		token, subject, role string
		allowed              bool
	}{
		{"", "forged-admin", "admin", false},
		{"other-workspace-secret", "alice", "admin", false},
		{"workspace-secret", "", "admin", false},
		{"workspace-secret", "alice", "owner", false},
		{"workspace-secret", "alice", "viewer", true},
		{"workspace-secret", "alice", "admin", true},
	} {
		r := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8585/api/assets", nil)
		r.Header.Set("Authorization", "Bearer "+test.token)
		r.Header.Set("X-Steward-Subject", test.subject)
		r.Header.Set("X-Steward-Role", test.role)
		principal, err := auth.Authenticate(r)
		if (err == nil) != test.allowed {
			t.Fatalf("allowed=%v err=%v", test.allowed, err)
		}
		if test.allowed && (principal.Subject != "alice" || len(principal.Roles) != 1 || principal.Roles[0] != Role(test.role)) {
			t.Fatalf("wrong gateway principal %+v", principal)
		}
	}
}
