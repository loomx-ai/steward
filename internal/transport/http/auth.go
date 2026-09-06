package httptransport

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
)

type Role string

const (
	RoleViewer   Role = "viewer"
	RoleOperator Role = "operator"
	RoleAdmin    Role = "admin"
)

var (
	ErrUnauthenticated = errors.New("authentication required")
	ErrForbidden       = errors.New("permission denied")
)

type Principal struct {
	Subject string `json:"subject"`
	Roles   []Role `json:"roles"`
}

// LocalAuthenticator is only valid for a loopback listener. It also rejects
// cross-origin browser requests and DNS rebinding to the local API.
type LocalAuthenticator struct {
	port string
}

func NewLocalAuthenticator(addr string) (*LocalAuthenticator, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil || !isLoopbackHost(host) || port == "" {
		return nil, fmt.Errorf("local authentication requires a loopback listen address; use token or cloud authentication for network access")
	}
	return &LocalAuthenticator{port: port}, nil
}

func isLoopbackHost(host string) bool {
	return host == "localhost" || net.ParseIP(host).IsLoopback()
}

func (a *LocalAuthenticator) Authenticate(request *http.Request) (Principal, error) {
	host, port, err := net.SplitHostPort(request.Host)
	if err != nil || !isLoopbackHost(host) || port != a.port {
		return Principal{}, ErrUnauthenticated
	}
	remote, _, err := net.SplitHostPort(request.RemoteAddr)
	if err != nil || !net.ParseIP(remote).IsLoopback() {
		return Principal{}, ErrUnauthenticated
	}
	if site := request.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
		return Principal{}, ErrUnauthenticated
	}
	if origin := request.Header.Get("Origin"); origin != "" {
		parsed, err := url.Parse(origin)
		if err != nil || parsed.Scheme != "http" || parsed.Host != request.Host || parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
			return Principal{}, ErrUnauthenticated
		}
	}
	return Principal{Subject: "local-admin", Roles: []Role{RoleAdmin}}, nil
}

// CloudAuthenticator accepts identity only from a gateway that presents the
// workspace-specific service token. Client-supplied identity headers alone
// never grant access.
type CloudAuthenticator struct {
	service *StaticBearerAuthenticator
}

func NewCloudAuthenticator(bindings []TokenBinding) *CloudAuthenticator {
	return &CloudAuthenticator{service: NewStaticBearerAuthenticator(bindings)}
}

func (a *CloudAuthenticator) Authenticate(request *http.Request) (Principal, error) {
	if _, err := a.service.Authenticate(request); err != nil {
		return Principal{}, err
	}
	subject := strings.TrimSpace(request.Header.Get("X-Steward-Subject"))
	role := Role(request.Header.Get("X-Steward-Role"))
	if subject == "" || len(subject) > 256 || (role != RoleAdmin && role != RoleOperator && role != RoleViewer) {
		return Principal{}, ErrUnauthenticated
	}
	return Principal{Subject: subject, Roles: []Role{role}}, nil
}

type Authenticator interface {
	Authenticate(*http.Request) (Principal, error)
}

type TokenBinding struct {
	Token     string
	Principal Principal
}

type staticToken struct {
	digest    [sha256.Size]byte
	principal Principal
}

type StaticBearerAuthenticator struct {
	tokens []staticToken
}

func NewStaticBearerAuthenticator(bindings []TokenBinding) *StaticBearerAuthenticator {
	authenticator := &StaticBearerAuthenticator{tokens: make([]staticToken, 0, len(bindings))}
	for _, binding := range bindings {
		if strings.TrimSpace(binding.Token) == "" || strings.TrimSpace(binding.Principal.Subject) == "" {
			continue
		}
		authenticator.tokens = append(authenticator.tokens, staticToken{digest: sha256.Sum256([]byte(binding.Token)), principal: binding.Principal})
	}
	return authenticator
}

func (a *StaticBearerAuthenticator) Authenticate(request *http.Request) (Principal, error) {
	header := strings.TrimSpace(request.Header.Get("Authorization"))
	if len(header) < len("Bearer ") || !strings.EqualFold(header[:len("Bearer ")], "Bearer ") {
		return Principal{}, ErrUnauthenticated
	}
	digest := sha256.Sum256([]byte(strings.TrimSpace(header[len("Bearer "):])))
	for _, configured := range a.tokens {
		if subtle.ConstantTimeCompare(digest[:], configured.digest[:]) == 1 {
			return configured.principal, nil
		}
	}
	return Principal{}, ErrUnauthenticated
}

type principalContextKey struct{}

func principalFromContext(ctx context.Context) (Principal, bool) {
	principal, ok := ctx.Value(principalContextKey{}).(Principal)
	return principal, ok
}

func authenticate(authenticator Authenticator) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
			if authenticator == nil {
				writeError(response, http.StatusUnauthorized, ErrUnauthenticated)
				return
			}
			principal, err := authenticator.Authenticate(request)
			if err != nil || strings.TrimSpace(principal.Subject) == "" {
				writeError(response, http.StatusUnauthorized, ErrUnauthenticated)
				return
			}
			roles := make([]string, 0, len(principal.Roles))
			for _, role := range principal.Roles {
				roles = append(roles, string(role))
			}
			response.Header().Set("X-Steward-Subject", principal.Subject)
			response.Header().Set("X-Steward-Roles", strings.Join(roles, ","))
			next.ServeHTTP(response, request.WithContext(context.WithValue(request.Context(), principalContextKey{}, principal)))
		})
	}
}

func requireRole(role Role, next http.HandlerFunc) http.HandlerFunc {
	return func(response http.ResponseWriter, request *http.Request) {
		principal, ok := principalFromContext(request.Context())
		if !ok || !principalAllows(principal, role) {
			writeError(response, http.StatusForbidden, ErrForbidden)
			return
		}
		next(response, request)
	}
}

func principalAllows(principal Principal, want Role) bool {
	for _, role := range principal.Roles {
		if role == RoleAdmin || role == want || (role == RoleOperator && want == RoleViewer) {
			return true
		}
	}
	return false
}
