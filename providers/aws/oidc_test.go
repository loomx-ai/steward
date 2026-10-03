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

func TestSDKConfigCacheReusesConfigUntilCredentialChanges(t *testing.T) {
	factory := &sdkClientFactory{}
	ctx := context.Background()
	static := contracts.Credential{ConnectionID: "conn-a", Type: asset.CredentialAWSAccessKey, Values: map[string]string{"access_key_id": "AKIA1", "secret_access_key": "secret-1"}}
	first, err := factory.config(ctx, static, "us-east-1")
	if err != nil {
		t.Fatal(err)
	}
	again, _ := factory.config(ctx, static, "us-east-1")
	if first.Credentials != again.Credentials || first.HTTPClient != sdkHTTPClient {
		t.Fatal("same connection, credential and region did not reuse the cached config")
	}
	otherRegion, _ := factory.config(ctx, static, "eu-west-1")
	if otherRegion.Credentials == first.Credentials || otherRegion.Region != "eu-west-1" {
		t.Fatal("a different region reused another region's config")
	}
	rotated := static
	rotated.Values = map[string]string{"access_key_id": "AKIA2", "secret_access_key": "secret-2"}
	renewed, _ := factory.config(ctx, rotated, "us-east-1")
	value, err := renewed.Credentials.Retrieve(ctx)
	if renewed.Credentials == first.Credentials || err != nil || value.AccessKeyID != "AKIA2" {
		t.Fatalf("rotated credential reused the old config: %v %q", err, value.AccessKeyID)
	}

	calls := 0
	oidc := contracts.Credential{ConnectionID: "conn-b", Type: asset.CredentialOIDC, Values: map[string]string{"role_arn": "arn"}, Dynamic: &contracts.DynamicCredential{Key: "run-1", Resolve: func(context.Context, string) (contracts.TemporaryCredential, error) {
		calls++
		return contracts.TemporaryCredential{AccessKeyID: "oidc-id", SecretAccessKey: "s", SessionToken: "t", ExpiresAt: time.Now().Add(time.Hour)}, nil
	}}}
	for range 2 {
		config, err := factory.config(ctx, oidc, "us-east-1")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := config.Credentials.Retrieve(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 2 {
		t.Fatalf("OIDC authorization re-check ran %d times for two clients, want 2", calls)
	}
}
