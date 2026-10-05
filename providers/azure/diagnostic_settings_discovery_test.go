package azure

import (
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// diagnosticDiscoveryFixture lists vaults and a network whose subnet is also
// listed in the resource index, so the merge must keep the walk's first visit.
func diagnosticDiscoveryFixture(t *testing.T, vaults int) (*diagnosticFixture, []string, string, string) {
	t.Helper()
	f := newDiagnosticFixture(t)
	template := f.sources[slices.Sorted(maps.Keys(f.sources))[0]]
	clear(f.sources)
	var ids []string
	for i := range vaults {
		raw := batchClone(template)
		name := fmt.Sprintf("vault-%02d", i)
		raw["id"], raw["name"] = resourceID("Microsoft.KeyVault/vaults", name), name
		id := strings.ToLower(text(raw["id"]))
		f.sources[id], ids = raw, append(ids, id)
	}
	network := nativeResource(vnetType, "net", "westus", map[string]any{"provisioningState": "Succeeded"})
	subnet := map[string]any{"id": text(network["id"]) + "/subnets/a", "name": "a", "type": subnetType, "properties": map[string]any{"provisioningState": "Succeeded"}}
	networkID, subnetID := strings.ToLower(text(network["id"])), strings.ToLower(text(subnet["id"]))
	f.sources[networkID], f.sources[subnetID] = network, subnet
	f.override = func(req *http.Request) (*http.Response, bool) {
		if req.Method == "GET" && strings.ToLower(req.URL.Path) == networkID+"/subnets" {
			return jsonResponse(200, map[string]any{"value": []any{subnet}}, nil), true
		}
		return nil, false
	}
	return f, ids, networkID, subnetID
}

// Each source is read once per walk, concurrently within the bound, and the
// walk keeps the serial visit's sources.
func TestDiagnosticSourceWalkReadsEachSourceOnce(t *testing.T) {
	f, vaults, network, subnet := diagnosticDiscoveryFixture(t, 30)
	var mu sync.Mutex
	inFlight, peak := 0, 0
	c, err := f.runtime.resolve(t.Context(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	transport := c.http.Transport // Outside the fixture's lock.
	c.http.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if slices.Contains(vaults, strings.ToLower(req.URL.Path)) {
			mu.Lock()
			inFlight++
			peak = max(peak, inFlight)
			mu.Unlock()
			time.Sleep(5 * time.Millisecond)
			defer func() { mu.Lock(); inFlight--; mu.Unlock() }()
		}
		return transport.RoundTrip(req)
	})
	clear(f.calls)
	sources, err := c.diagnosticSourceCandidates(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range append(slices.Clone(vaults), network, subnet) {
		if sources[id] == nil || f.calls["GET "+id] != 1 {
			t.Fatal("source not discovered by exactly one read", id, sources[id] != nil, f.calls["GET "+id])
		}
	}
	if f.calls["GET "+network+"/subnets"] != 1 || f.calls["GET /subscriptions/"+testSubscription+"/resources"] != 1 {
		t.Fatal("child or index list repeated", f.calls)
	}
	if peak < 2 || peak > diagnosticSourceConcurrency {
		t.Fatal("source reads were not concurrent within the bound", peak)
	}
}

// diagnosticSubtreeFixture indexes one network whose subnets are listed only as
// its children, served by serve.
func diagnosticSubtreeFixture(t *testing.T, subnets int, serve func(i int) *http.Response) (*diagnosticFixture, []string) {
	t.Helper()
	f := newDiagnosticFixture(t)
	clear(f.sources)
	network := nativeResource(vnetType, "net", "westus", map[string]any{"provisioningState": "Succeeded"})
	networkID := strings.ToLower(text(network["id"]))
	f.sources[networkID] = network
	var ids []string
	var values []any
	for i := range subnets {
		name := fmt.Sprintf("s%02d", i)
		values = append(values, map[string]any{"id": text(network["id"]) + "/subnets/" + name, "name": name, "type": subnetType, "properties": map[string]any{"provisioningState": "Succeeded"}})
		ids = append(ids, networkID+"/subnets/"+name)
	}
	f.override = func(req *http.Request) (*http.Response, bool) {
		path := strings.ToLower(req.URL.Path)
		if req.Method == "GET" && path == networkID+"/subnets" {
			return jsonResponse(200, map[string]any{"value": values}, nil), true
		}
		if i := slices.Index(ids, path); req.Method == "GET" && i >= 0 {
			if response := serve(i); response != nil {
				return response, true
			}
			return jsonResponse(200, values[i], nil), true
		}
		return nil, false
	}
	return f, ids
}

// Children are expanded concurrently under the walk's one bound, each read
// once, and the walk keeps every child.
func TestDiagnosticSourceWalkExpandsChildrenConcurrently(t *testing.T) {
	f, subnets := diagnosticSubtreeFixture(t, 40, func(int) *http.Response { return nil })
	c, err := f.runtime.resolve(t.Context(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	inFlight, peak := 0, 0
	transport := c.http.Transport
	c.http.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		mu.Lock()
		inFlight++
		peak = max(peak, inFlight)
		mu.Unlock()
		defer func() { mu.Lock(); inFlight--; mu.Unlock() }()
		time.Sleep(5 * time.Millisecond)
		return transport.RoundTrip(req)
	})
	clear(f.calls)
	sources, err := c.diagnosticSourceCandidates(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range subnets {
		if sources[id] == nil || f.calls["GET "+id] != 1 {
			t.Fatal("child not discovered by exactly one read", id, sources[id] != nil, f.calls["GET "+id])
		}
	}
	if peak < 2 || peak > diagnosticSourceConcurrency {
		t.Fatal("child reads were not concurrent within the walk's bound", peak)
	}
}

// Within a subtree the first failing child in order is reported; once the
// merge reaches it, reads still outstanding are canceled.
func TestDiagnosticSourceWalkCancelsAfterFirstChildFailure(t *testing.T) {
	var timedOut atomic.Int32
	f, _ := diagnosticSubtreeFixture(t, 10, func(i int) *http.Response {
		switch i {
		case 1:
			return jsonResponse(403, map[string]any{"error": map[string]any{"code": "FirstDenied", "message": "first"}}, nil)
		case 6:
			return jsonResponse(403, map[string]any{"error": map[string]any{"code": "LaterDenied", "message": "later"}}, nil)
		}
		return nil
	})
	c, err := f.runtime.resolve(t.Context(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	transport := c.http.Transport
	c.http.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		// Children after the failure hang (outside the fixture's lock) until canceled.
		if name := last(req.URL.Path); strings.Contains(req.URL.Path, "/subnets/") && name > "s01" && name != "s06" {
			select {
			case <-req.Context().Done():
				return nil, req.Context().Err()
			case <-time.After(5 * time.Second):
				timedOut.Add(1)
			}
		}
		return transport.RoundTrip(req)
	})
	for range 5 {
		if sources, err := c.diagnosticSourceCandidates(t.Context()); err == nil || !strings.Contains(err.Error(), "FirstDenied") || sources != nil {
			t.Fatal("first failing child was not the reported error", err)
		}
	}
	if timedOut.Load() != 0 {
		t.Fatal("outstanding reads were not canceled after the first failure", timedOut.Load())
	}
}

// The first failure in index order is returned, as the serial visit did.
func TestDiagnosticSourceWalkReturnsFirstFailureInOrder(t *testing.T) {
	f, vaults, _, _ := diagnosticDiscoveryFixture(t, 20)
	base := f.override
	f.override = func(req *http.Request) (*http.Response, bool) {
		switch strings.ToLower(req.URL.Path) {
		case vaults[4]:
			return jsonResponse(403, map[string]any{"error": map[string]any{"code": "FirstDenied", "message": "first"}}, nil), true
		case vaults[15]:
			return jsonResponse(403, map[string]any{"error": map[string]any{"code": "LaterDenied", "message": "later"}}, nil), true
		}
		return base(req)
	}
	c, err := f.runtime.resolve(t.Context(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	for range 5 {
		if sources, err := c.diagnosticSourceCandidates(t.Context()); err == nil || !strings.Contains(err.Error(), "FirstDenied") || sources != nil {
			t.Fatal("first failing source was not the reported error", err)
		}
	}
}

// Delete checks arriving while a walk runs queue behind it and share the next
// one; nothing outlives that walk.
func TestDiagnosticSourceWalkSharedByQueuedCallers(t *testing.T) {
	f, vaults, _, _ := diagnosticDiscoveryFixture(t, 10)
	c, err := f.runtime.resolve(t.Context(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	index := "/subscriptions/" + testSubscription + "/resources"
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	transport := c.http.Transport
	c.http.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if strings.ToLower(req.URL.Path) == index {
			once.Do(func() { close(started); <-release })
		}
		return transport.RoundTrip(req)
	})
	clear(f.calls)
	errs := make(chan error, 6)
	walk := func() { _, err := c.diagnosticSourceCandidates(t.Context()); errs <- err }
	go walk()
	<-started
	for range 5 {
		go walk()
	}
	for {
		sharedReads.Lock()
		queued := sharedReads.queued[sharedReadKey{c, "diagnostic-sources"}]
		sharedReads.Unlock()
		if queued != nil {
			break
		}
		time.Sleep(time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond) // Let the other callers join the queued walk.
	close(release)
	for range 6 {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	if f.calls["GET "+index] != 2 {
		t.Fatal("queued callers did not share one walk", f.calls["GET "+index])
	}
	for _, id := range vaults {
		if f.calls["GET "+id] != 2 {
			t.Fatal("queued callers did not share source reads", id, f.calls["GET "+id])
		}
	}
	if _, err := c.diagnosticSourceCandidates(t.Context()); err != nil || f.calls["GET "+index] != 3 {
		t.Fatal("a finished walk was reused", f.calls["GET "+index], err)
	}
}

// Persisted settings and scope lists are read concurrently; the first failing
// scope in order is reported.
func TestDiagnosticCollectionScopeListsFailInOrder(t *testing.T) {
	f, vaults, _, _ := diagnosticDiscoveryFixture(t, 12)
	base := f.override
	f.override = func(req *http.Request) (*http.Response, bool) {
		switch strings.ToLower(req.URL.Path) {
		case vaults[2] + "/providers/microsoft.insights/diagnosticsettings":
			return jsonResponse(403, map[string]any{"error": map[string]any{"code": "FirstDenied", "message": "first"}}, nil), true
		case vaults[9] + "/providers/microsoft.insights/diagnosticsettings":
			return jsonResponse(403, map[string]any{"error": map[string]any{"code": "LaterDenied", "message": "later"}}, nil), true
		}
		return base(req)
	}
	c, err := f.runtime.resolve(t.Context(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := c.diagnosticCollection(t.Context(), nil, nil); err == nil || !strings.Contains(err.Error(), "FirstDenied") {
		t.Fatal("first failing scope list was not the reported error", err)
	}
	f.override = base
	clear(f.calls)
	if _, _, _, err := c.diagnosticCollection(t.Context(), nil, nil); err != nil {
		t.Fatal(err)
	}
	for _, id := range vaults {
		if f.calls["GET "+id+"/providers/microsoft.insights/diagnosticsettings"] != 1 {
			t.Fatal("scope not listed exactly once", id)
		}
	}
}
