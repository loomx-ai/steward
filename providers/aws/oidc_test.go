package aws

import (
	"context"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
	"testing"
	"time"
)

func TestOIDCCredentialsUseBoundSourceInsteadOfEnvironment(t *testing.T) {
	t.Setenv("AWS_ACCESS_KEY_ID", "ambient")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "ambient-secret")
	calls := 0
	c := contracts.Credential{Type: asset.CredentialOIDC, Dynamic: &contracts.DynamicCredential{Resolve: func(ctx context.Context, scope string) (contracts.TemporaryCredential, error) {
		calls++
		return contracts.TemporaryCredential{AccessKeyID: "oidc-id", SecretAccessKey: "oidc-secret", SessionToken: "oidc-session", ExpiresAt: time.Now().Add(time.Hour)}, ctx.Err()
	}}}
	cfg, err := loadSDKConfig(context.Background(), c, "us-east-1")
	if err != nil {
		t.Fatal(err)
	}
	value, err := cfg.Credentials.Retrieve(context.Background())
	if err != nil || value.AccessKeyID != "oidc-id" || value.SessionToken != "oidc-session" || !value.CanExpire || calls != 1 {
		t.Fatalf("wrong credentials: err=%v calls=%d", err, calls)
	}
	c.Dynamic = nil
	if _, err = loadSDKConfig(context.Background(), c, "us-east-1"); err == nil {
		t.Fatal("unbound OIDC fell back to ambient credentials")
	}
}
