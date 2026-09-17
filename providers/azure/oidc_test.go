package azure

import (
	"context"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
	"net/http"
	"testing"
	"time"
)

func TestOIDCClientUsesFederatedIdentityForEachAudience(t *testing.T) {
	c := testCredential()
	c.Type = asset.CredentialOIDC
	delete(c.Values, "client_secret")
	seen := map[string]bool{}
	c.Dynamic = &contracts.DynamicCredential{Key: "read", Resolve: func(ctx context.Context, scope string) (contracts.TemporaryCredential, error) {
		seen[scope] = true
		return contracts.TemporaryCredential{AccessToken: scope, ExpiresAt: time.Now().Add(time.Hour)}, ctx.Err()
	}}
	client, err := newClient(c, roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Authorization") == "" {
			t.Fatal("missing token")
		}
		return jsonResponse(200, map[string]any{}, nil), nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	for _, hc := range []*http.Client{client.http, client.storageHTTP, client.batchHTTP, client.communicationHTTP} {
		req, _ := http.NewRequest("GET", "https://management.azure.com/test", nil)
		res, err := hc.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
	}
	for _, scope := range []string{armOrigin + "/.default", "https://storage.azure.com/.default", "https://batch.core.windows.net//.default", "https://communication.azure.com/.default"} {
		if !seen[scope] {
			t.Fatalf("missing audience %s", scope)
		}
	}
	c.Dynamic = nil
	if _, err = newClient(c, http.DefaultTransport); err == nil {
		t.Fatal("accepted unbound identity")
	}
}
