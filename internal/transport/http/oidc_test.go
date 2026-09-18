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

type oauthFlowsStub struct{ contracts.OAuthFlowService }

type oauthProviders struct{}

func (oauthProviders) ProviderDescriptors() []contracts.ProviderDescriptor {
	return []contracts.ProviderDescriptor{{Provider: asset.ProviderAliCloud, CredentialSchemas: []contracts.CredentialSchema{
		{Type: asset.CredentialAliCloudAccessKey},
		{Type: asset.CredentialAliCloudOAuth, Flow: "browser_oauth"},
	}}}
}

func TestProvidersHideBrowserOAuthWithoutFlowService(t *testing.T) {
	auth := NewStaticBearerAuthenticator([]TokenBinding{{Token: "viewer", Principal: Principal{Subject: "viewer", Roles: []Role{RoleViewer}}}})
	for _, enabled := range []bool{false, true} {
		deps := Dependencies{Authenticator: auth, Providers: oauthProviders{}}
		if enabled {
			deps.OAuthFlows = oauthFlowsStub{}
		}
		req := httptest.NewRequest("GET", "/api/providers", nil)
		req.Header.Set("Authorization", "Bearer viewer")
		w := httptest.NewRecorder()
		NewRouter(deps).ServeHTTP(w, req)
		body := w.Body.String()
		if w.Code != 200 || !strings.Contains(body, `"type":"access_key"`) || strings.Contains(body, `"flow":"browser_oauth"`) != enabled {
			t.Fatalf("OAuth availability mismatch (enabled=%v): %d %s", enabled, w.Code, body)
		}
	}
}
