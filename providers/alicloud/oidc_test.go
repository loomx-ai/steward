package alicloud

import (
	"context"
	"github.com/alibabacloud-go/tea/tea"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
	"testing"
	"time"
)

func TestOIDCCredentialRefreshAndCancellation(t *testing.T) {
	calls := 0
	c := contracts.Credential{Type: asset.CredentialOIDC, Dynamic: &contracts.DynamicCredential{Resolve: func(ctx context.Context, scope string) (contracts.TemporaryCredential, error) {
		calls++
		return contracts.TemporaryCredential{AccessKeyID: "oidc-id", SecretAccessKey: "oidc-secret", SessionToken: "oidc-session", ExpiresAt: time.Now().Add(time.Hour)}, ctx.Err()
	}}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sdk, err := cloudCredential(c, ctx)
	if err != nil {
		t.Fatal(err)
	}
	v, err := sdk.GetCredential()
	if err != nil || tea.StringValue(v.SecurityToken) != "oidc-session" || calls != 1 {
		t.Fatal("dynamic source not used", err)
	}
	if _, err = sdk.GetCredential(); err != nil || calls != 2 {
		t.Fatal("SDK froze temporary credentials", err)
	}
	cancel()
	if _, err = sdk.GetCredential(); err == nil {
		t.Fatal("SDK lost cancellation")
	}
	c.Dynamic = nil
	if _, err = cloudCredential(c); err == nil {
		t.Fatal("accepted unbound identity")
	}
}
