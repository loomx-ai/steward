package httptransport

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"net/http"
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
