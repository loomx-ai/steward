package gcp

import (
	"context"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
	"net/http"
	"testing"
	"time"
)

func TestOIDCClientUsesImpersonatedTokenWithoutPrivateKey(t *testing.T) {
	c := contracts.Credential{Type: asset.CredentialOIDC, Values: map[string]string{"project_id": "test-project", "workload_provider": "projects/123/locations/global/workloadIdentityPools/steward/providers/test", "service_account_email": "test@test-project.iam.gserviceaccount.com"}, Dynamic: &contracts.DynamicCredential{Key: "read", Resolve: func(ctx context.Context, scope string) (contracts.TemporaryCredential, error) {
		return contracts.TemporaryCredential{AccessToken: "federated", ExpiresAt: time.Now().Add(time.Hour)}, ctx.Err()
	}}}
	client, err := newClient(c, roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Authorization") != "Bearer federated" {
			t.Fatal("wrong identity")
		}
		return apiResponse(r, 200, `{"projectId":"test-project"}`), nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = client.request(context.Background(), "GET", "https://cloudresourcemanager.googleapis.com/v3/projects/test-project", nil); err != nil {
		t.Fatal(err)
	}
	if client.email != c.Values["service_account_email"] {
		t.Fatal("wrong principal")
	}
	c.Dynamic = nil
	if _, err = newClient(c, http.DefaultTransport); err == nil {
		t.Fatal("accepted unbound identity")
	}
}
