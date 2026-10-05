package azure

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
)

// One Contribute reads each RBAC parent once and shares the subscription-wide
// lock and PIM lists; outside Contribute every call reads them live.
func TestRBACGraphReadsShareSubscriptionLists(t *testing.T) {
	root := "/subscriptions/" + testSubscription
	locks := "GET " + root + "/providers/microsoft.authorization/locks"
	eligibility := "GET " + root + "/providers/" + strings.ToLower(rbacEligibilityType)
	schedules := "GET " + root + "/providers/" + strings.ToLower(rbacScheduleType)
	for _, memo := range []bool{true, false} {
		f := newRBACFixture(t)
		var parents []asset.Asset
		for id, raw := range f.resources {
			if raw["type"] == rbacAssignmentType {
				parents = append(parents, f.asset(t, rbacAssignmentType, id))
			}
		}
		c, _ := f.runtime.resolve(t.Context(), "connection")
		ctx := t.Context()
		if memo {
			ctx = withReadMemo(ctx)
		}
		clear(f.calls)
		for _, parent := range parents {
			if _, err := c.contributeRBACReferences(ctx, parent, parents); err != nil {
				t.Fatal(err)
			}
		}
		shared := 1
		if !memo {
			shared = len(parents)
		}
		if len(parents) != 2 || f.calls[locks] != shared || f.calls[eligibility] != shared || f.calls[schedules] != shared {
			t.Fatal("subscription lists were not shared exactly per Contribute", memo, f.calls)
		}
		for _, parent := range parents {
			if got := f.calls["GET "+strings.ToLower(text(parent.Normalized[rbacWireSelector]))]; got != 1 {
				t.Fatal("RBAC parent was not read exactly once", parent.Identity.NativeID, got)
			}
		}
	}
}

// Concurrent callers share one read issued after they all arrived; a read
// already running is never joined, and nothing survives the read.
func TestLiveSharedJoinsOnlyQueuedReads(t *testing.T) {
	c := &client{}
	var reads atomic.Int32
	release := make(chan struct{})
	started := make(chan struct{}, 8)
	read := func() (int32, error) {
		n := reads.Add(1)
		started <- struct{}{}
		<-release
		return n, nil
	}
	results := make(chan int32, 8)
	go func() { v, _ := liveShared(t.Context(), c, "k", read); results <- v }()
	<-started // The first read is running before the others arrive.
	var wg sync.WaitGroup
	for range 6 {
		wg.Add(1)
		go func() { defer wg.Done(); v, _ := liveShared(t.Context(), c, "k", read); results <- v }()
	}
	for {
		sharedReads.Lock()
		queued := sharedReads.queued[sharedReadKey{c, "k"}]
		sharedReads.Unlock()
		if queued != nil {
			break
		}
		time.Sleep(time.Millisecond)
	}
	time.Sleep(20 * time.Millisecond) // Let the other callers join the queued read.
	close(release)
	wg.Wait()
	seen := map[int32]int{<-results: 1}
	for range 6 {
		seen[<-results]++
	}
	if reads.Load() != 2 || seen[1] != 1 || seen[2] != 6 {
		t.Fatal("callers joined a read that predates them or were not coalesced", reads.Load(), seen)
	}
	if v, _ := liveShared(t.Context(), c, "k", func() (int32, error) { return reads.Add(1), nil }); v != 3 {
		t.Fatal("a completed read was reused", v)
	}
	if len(sharedReads.running)+len(sharedReads.queued) != 0 {
		t.Fatal("shared reads leaked")
	}
}

func TestLiveSharedFailureIsNotShared(t *testing.T) {
	c := &client{}
	release := make(chan struct{})
	go liveShared(t.Context(), c, "k", func() (int, error) { <-release; return 0, nil })
	for {
		sharedReads.Lock()
		running := sharedReads.running[sharedReadKey{c, "k"}] != nil
		sharedReads.Unlock()
		if running {
			break
		}
		time.Sleep(time.Millisecond)
	}
	failed := errors.New("throttled")
	leader := make(chan error)
	go func() {
		_, err := liveShared(t.Context(), c, "k", func() (int, error) { return 0, failed })
		leader <- err
	}()
	for {
		sharedReads.Lock()
		ready := sharedReads.queued[sharedReadKey{c, "k"}] != nil
		sharedReads.Unlock()
		if ready {
			break
		}
		time.Sleep(time.Millisecond)
	}
	joiner := make(chan int)
	go func() { v, _ := liveShared(t.Context(), c, "k", func() (int, error) { return 7, nil }); joiner <- v }()
	time.Sleep(20 * time.Millisecond)
	close(release)
	if err := <-leader; !errors.Is(err, failed) {
		t.Fatal("leader lost its read error", err)
	}
	if v := <-joiner; v != 7 {
		t.Fatal("a failed shared read was handed to a joiner", v)
	}
}

func TestRBACPrincipalIndexMatchesScan(t *testing.T) {
	f, assignment, target, _ := rbacPrincipalTarget(t, rbacUserIdentityType)
	c, _ := f.runtime.resolve(t.Context(), "connection")
	refs := map[string][]string{rbacPrincipalType: {rbacPrincipalSelector(testTenant, rbacTestPrincipal), rbacPrincipalSelector(testTenant, rbacTestClientID)}}
	duplicate := target
	duplicate.ID = "copy"
	unproven := target
	unproven.ID, unproven.Normalized = "unproven", map[string]any{}
	for _, assets := range [][]asset.Asset{{target, assignment}, {assignment}, {target, assignment, duplicate}, {unproven, target, assignment}} {
		want, wantErr := c.rbacResolvePrincipals(t.Context(), assignment, assets, refs)
		ctx := context.WithValue(withReadMemo(t.Context()), assetIndexContextKey{}, contextAssetIndex{assets, newAssetIndex(assets)})
		got, gotErr := c.rbacResolvePrincipals(ctx, assignment, assets, refs)
		if fmt.Sprint(gotErr) != fmt.Sprint(wantErr) || !reflect.DeepEqual(got, want) {
			t.Fatal("indexed principals differ from the scan", got, gotErr, want, wantErr)
		}
	}
}

func TestIncomingSourceIndexMatchesScan(t *testing.T) {
	c := &client{subscription: testSubscription}
	target := actionAsset(storageType, "target")
	source := actionAsset(diskType, "source")
	folded := source
	folded.ID, folded.Identity.NativeID = "folded", strings.ToUpper(source.Identity.NativeID)
	copy := source
	copy.ID = "copy"
	incoming := map[string][]monitorIncomingSource{target.Identity.NativeID: {{resource: serviceChild{kind: diskType, id: source.Identity.NativeID}}}}
	for _, assets := range [][]asset.Asset{{target}, {target, folded}, {target, source, copy}} {
		want, wantErr := c.contributeIncomingSources([]asset.Asset{target}, assets, incoming)
		got, gotErr := c.contributeIndexedIncomingSources([]asset.Asset{target}, assets, newAssetIndex(assets), incoming)
		if fmt.Sprint(gotErr) != fmt.Sprint(wantErr) || !reflect.DeepEqual(got, want) {
			t.Fatal("indexed incoming sources differ from the scan", got, gotErr, want, wantErr)
		}
	}
}

// Concurrent callers of one key share the read in flight; a failure reaches
// them but is not kept; reads of other keys never wait on it.
func TestMemoizedSharesInFlightReadsPerKey(t *testing.T) {
	ctx := withReadMemo(t.Context())
	var reads atomic.Int32
	release, both := make(chan struct{}), sync.WaitGroup{}
	both.Add(2)
	read := func(key string) func() (string, error) {
		return func() (string, error) {
			reads.Add(1)
			if key != "a" {
				both.Done() // "b" runs while "a" is still reading.
				return key, nil
			}
			both.Done()
			<-release
			return key, nil
		}
	}
	var wg sync.WaitGroup
	results := make([]string, 8)
	for i := range results {
		wg.Go(func() { results[i], _ = memoized(ctx, "a", read("a")) })
	}
	go func() { _, _ = memoized(ctx, "b", read("b")) }()
	done := make(chan struct{})
	go func() { both.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("a read of one key blocked another key")
	}
	close(release)
	wg.Wait()
	if reads.Load() != 2 || slices.ContainsFunc(results, func(v string) bool { return v != "a" }) {
		t.Fatal("concurrent callers did not share one read", reads.Load(), results)
	}
	fail := errors.New("transient")
	if _, err := memoized(ctx, "c", func() (string, error) { return "", fail }); err != fail {
		t.Fatal(err)
	}
	if v, err := memoized(ctx, "c", func() (string, error) { return "ok", nil }); err != nil || v != "ok" {
		t.Fatal("a failed read was kept", v, err)
	}
}

// Contribute's concurrent RBAC parents read a shared scope once, outside any
// lock held across the read: two parents cost what one does.
func TestRBACConcurrentParentsReadSharedScopeOnce(t *testing.T) {
	f := newRBACFixture(t)
	group := "/subscriptions/" + testSubscription + "/resourcegroups/test"
	second := rbacTestBody(t, rbacAssignmentType, group, "ffffffff-0000-0000-0000-000000000000")
	f.resources[strings.ToLower(text(second["id"]))] = second
	var parents []asset.Asset
	for id, raw := range f.resources {
		if raw["type"] == rbacAssignmentType && strings.HasPrefix(id, group+"/providers/microsoft.authorization/") {
			parents = append(parents, f.asset(t, rbacAssignmentType, id))
		}
	}
	c, _ := f.runtime.resolve(t.Context(), "connection")
	clear(f.calls)
	if _, err := c.contributeRBACReferences(withReadMemo(t.Context()), parents[0], parents); err != nil {
		t.Fatal(err)
	}
	one := f.calls["GET "+group]
	ctx := withReadMemo(t.Context())
	clear(f.calls)
	var wg sync.WaitGroup
	errs := make([]error, len(parents))
	for i, parent := range parents {
		wg.Go(func() { _, errs[i] = c.contributeRBACReferences(ctx, parent, parents) })
	}
	wg.Wait()
	if len(parents) != 2 || one == 0 || errors.Join(errs...) != nil || f.calls["GET "+group] != one {
		t.Fatal("shared RBAC scope was not read once", len(parents), errs, one, f.calls["GET "+group])
	}
}
