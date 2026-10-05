package alicloud

import (
	"context"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func TestCENTopologyCacheReleasesWaitersWhenCollectionPanics(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		var cache cenTopologyCache
		key := cenTopologyKey{run: "scan-a", region: "cn-hangzhou"}
		release := make(chan struct{})
		errs := make(chan error, 2)
		go func() {
			_, err := cache.get(t.Context(), key, func(context.Context) (*cenTopologyCollector, error) {
				<-release
				panic("boom")
			})
			errs <- err
		}()
		synctest.Wait() // the leader is collecting
		go func() {
			_, err := cache.get(t.Context(), key, func(context.Context) (*cenTopologyCollector, error) {
				t.Error("waiter collected while the leader was running")
				return nil, nil
			})
			errs <- err
		}()
		synctest.Wait() // the waiter is waiting on the leader's collection
		close(release)
		for range 2 {
			if err := <-errs; err == nil || !strings.Contains(err.Error(), "panicked") {
				t.Fatalf("err = %v, want the panic as an error", err)
			}
		}
		// The failed collection is not kept.
		collector, err := cache.get(t.Context(), key, func(context.Context) (*cenTopologyCollector, error) {
			return &cenTopologyCollector{}, nil
		})
		if err != nil || collector == nil {
			t.Fatalf("fresh collection = %v, %v", collector, err)
		}
	})
}

func TestResourceCenterSearchCacheStopsWithLeaderAndWaiterSearchesAgain(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		var cache resourceCenterSearchCache
		key := resourceCenterSearchKey{run: "scan-a"}
		leaderCtx, cancelLeader := context.WithCancel(t.Context())
		leader := make(chan error, 1)
		go func() {
			_, err := cache.get(leaderCtx, key, func(ctx context.Context) (map[string][]ResourceRecord, error) {
				<-ctx.Done() // the search sees the leader's cancellation
				return nil, ctx.Err()
			})
			leader <- err
		}()
		synctest.Wait() // the leader is searching
		waiter := make(chan *resourceCenterSearch, 1)
		go func() {
			search, err := cache.get(t.Context(), key, func(context.Context) (map[string][]ResourceRecord, error) {
				return map[string][]ResourceRecord{"ACS::ECS::Instance": {{ResourceID: "i-a"}}}, nil
			})
			if err != nil {
				t.Error(err)
			}
			waiter <- search
		}()
		synctest.Wait() // the waiter joined the leader's search
		cancelLeader()
		if err := <-leader; err == nil {
			t.Fatal("canceled leader returned no error")
		}
		if search := <-waiter; search == nil || len(search.records["ACS::ECS::Instance"]) != 1 {
			t.Fatalf("waiter search = %+v, want a fresh search after the leader was canceled", search)
		}
	})
}

func TestInvokeResolvesAPinnedCredentialNearExpiry(t *testing.T) {
	t.Parallel()
	fresh := time.Now().Add(time.Hour)
	source := &credentialSource{wantConnection: "connection-a", value: contracts.Credential{
		Type: asset.CredentialAliCloudAccessKey, Values: map[string]string{"access_key_id": "fresh"}, ExpiresAt: &fresh,
	}}
	factory := &runtimeFactory{invokeResult: contracts.InvocationResult{Data: map[string]any{}}}
	runtime, err := newRuntime(source, factory)
	if err != nil {
		t.Fatal(err)
	}
	invocation := contracts.Invocation{
		ConnectionID: "connection-a", Operation: "AlibabaCloud.CEN.DescribeCens",
		Scope: map[string]string{"region": "cn-hangzhou"},
	}
	for _, test := range []struct {
		remaining time.Duration
		wantKey   string
		wantCalls int
	}{
		{remaining: 10 * time.Minute, wantKey: "pinned", wantCalls: 0},
		{remaining: 30 * time.Second, wantKey: "fresh", wantCalls: 1},
	} {
		source.calls = 0
		expires := time.Now().Add(test.remaining)
		pinned := contracts.Credential{
			Type: asset.CredentialAliCloudAccessKey, Values: map[string]string{"access_key_id": "pinned"}, ExpiresAt: &expires,
		}
		if _, err := runtime.invoke(context.Background(), &pinned, invocation); err != nil {
			t.Fatal(err)
		}
		if factory.credential.Values["access_key_id"] != test.wantKey || source.calls != test.wantCalls {
			t.Fatalf("remaining %s: used %q after %d resolves", test.remaining, factory.credential.Values["access_key_id"], source.calls)
		}
	}
}
