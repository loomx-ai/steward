package httptransport

import (
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
	"github.com/loomx-ai/steward/internal/workloadidentity"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type oidcProviders struct{}

func (oidcProviders) ProviderDescriptors() []contracts.ProviderDescriptor {
	return []contracts.ProviderDescriptor{{Provider: asset.ProviderAWS}}
}

func TestOIDCDiscoveryRequiresConfigurationAndTrustRequiresAdmin(t *testing.T) {
	auth := NewStaticBearerAuthenticator([]TokenBinding{{Token: "viewer", Principal: Principal{Subject: "viewer", Roles: []Role{RoleViewer}}}})
	for _, enabled := range []bool{false, true} {
		deps := Dependencies{Authenticator: auth, Providers: oidcProviders{}}
		if enabled {
			deps.WorkloadIdentity = &workloadidentity.Broker{}
		}
		router := NewRouter(deps)
		req := httptest.NewRequest("GET", "/api/providers", nil)
		req.Header.Set("Authorization", "Bearer viewer")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != 200 || strings.Contains(w.Body.String(), `"type":"oidc"`) != enabled {
			t.Fatalf("OIDC availability mismatch: %d %s", w.Code, w.Body.String())
		}
		for _, token := range []string{"", "viewer"} {
			req = httptest.NewRequest("GET", "/api/connections/con_test/oidc", nil)
			if token != "" {
				req.Header.Set("Authorization", "Bearer "+token)
			}
			w = httptest.NewRecorder()
			router.ServeHTTP(w, req)
			want := http.StatusUnauthorized
			if token != "" {
				want = http.StatusForbidden
			}
			if w.Code != want {
				t.Fatalf("trust configuration bypassed authorization: %d", w.Code)
			}
		}
	}
}
