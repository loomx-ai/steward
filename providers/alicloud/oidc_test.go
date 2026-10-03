package alicloud

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	resourcecenterclient "github.com/alibabacloud-go/resourcecenter-20221201/client"
	"github.com/alibabacloud-go/tea/tea"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func oidcTestCredential(version string, resolves *atomic.Int32) contracts.Credential {
	return contracts.Credential{
		Type: asset.CredentialOIDC, ConnectionID: "connection-a", Site: asset.ConnectionSiteCN, Version: version,
		Values: map[string]string{"role_arn": "acs:ram::1:role/steward"},
		Dynamic: &contracts.DynamicCredential{Key: "key-" + version, Resolve: func(ctx context.Context, _ string) (contracts.TemporaryCredential, error) {
			resolves.Add(1)
			if err := ctx.Err(); err != nil {
				return contracts.TemporaryCredential{}, err
			}
			return contracts.TemporaryCredential{
				AccessKeyID: "id-" + version, SecretAccessKey: "secret", SessionToken: "token-" + version,
				ExpiresAt: time.Now().Add(time.Hour),
			}, nil
		}},
	}
}

func TestOIDCCredentialRefreshesOnEverySignature(t *testing.T) {
	t.Parallel()

	var resolves atomic.Int32
	c := oidcTestCredential("v1", &resolves)
	sdk, err := cloudCredential(c)
	if err != nil {
		t.Fatal(err)
	}
	v, err := sdk.GetCredential()
	if err != nil || tea.StringValue(v.SecurityToken) != "token-v1" || resolves.Load() != 1 {
		t.Fatal("dynamic source not used", err)
	}
	if _, err = sdk.GetCredential(); err != nil || resolves.Load() != 2 {
		t.Fatal("SDK froze temporary credentials", err)
	}
	c.Dynamic = nil
	if _, err = cloudCredential(c); err == nil {
		t.Fatal("accepted unbound identity")
	}
}

func TestSDKClientsAreCachedPerCredentialAndOutliveTheirRequest(t *testing.T) {
	t.Parallel()

	var resolves atomic.Int32
	factory := &sdkClientFactory{}
	token := func(client ResourceCenterClient) string {
		model, err := client.(*sdkResourceCenter).client.(*resourcecenterclient.Client).Credential.GetCredential()
		if err != nil {
			t.Errorf("sign with cached client: %v", err)
			return ""
		}
		return tea.StringValue(model.SecurityToken)
	}

	// The client is built under a request that is cancelled before it signs;
	// later calls sharing the client must still sign.
	cancelled, cancel := context.WithCancel(context.Background())
	first, err := factory.ResourceCenter(cancelled, oidcTestCredential("v1", &resolves), "cn-hangzhou")
	cancel()
	if err != nil {
		t.Fatal(err)
	}
	if got := token(first); got != "token-v1" {
		t.Fatalf("token after the building request was cancelled = %q", got)
	}
	again, err := factory.ResourceCenter(context.Background(), oidcTestCredential("v1", &resolves), "cn-hangzhou")
	if err != nil || again != first {
		t.Fatalf("same credential and region rebuilt the client: err = %v", err)
	}
	if other, err := factory.ResourceCenter(context.Background(), oidcTestCredential("v1", &resolves), "cn-shanghai"); err != nil || other == first {
		t.Fatalf("another region reused the client: err = %v", err)
	}

	// A rotated credential gets its own client and retires the old version's.
	rotated, err := factory.ResourceCenter(context.Background(), oidcTestCredential("v2", &resolves), "cn-hangzhou")
	if err != nil || rotated == first || token(rotated) != "token-v2" {
		t.Fatalf("rotated client reused the previous credential: err = %v", err)
	}
	factory.clients.mu.Lock()
	for _, entry := range factory.clients.entries {
		if entry.version != "v2" {
			t.Errorf("client for retired credential version %q is still cached", entry.version)
		}
	}
	factory.clients.mu.Unlock()

	var wait sync.WaitGroup
	for range 16 {
		wait.Go(func() {
			client, err := factory.ResourceCenter(context.Background(), oidcTestCredential("v2", &resolves), "cn-hangzhou")
			if err != nil || client != rotated || token(client) != "token-v2" {
				t.Errorf("concurrent lookup = %p, err = %v", client, err)
			}
			if _, err := factory.ACK(context.Background(), oidcTestCredential("v2", &resolves), "cn-hangzhou"); err != nil {
				t.Error(err)
			}
		})
	}
	wait.Wait()
}
