package gcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

// readProbe holds matching requests until want of them are in flight together
// (a serial walk only gets there by timing out), and records calls and the
// highest overlap.
type readProbe struct {
	mu                          sync.Mutex
	active, reached             bool
	want, inFlight, peak, calls int
	release                     chan struct{}
	match                       func(*http.Request) bool
}

func newReadProbe(want int, match func(*http.Request) bool) *readProbe {
	return &readProbe{want: want, match: match, release: make(chan struct{})}
}

func (p *readProbe) wrap(next roundTripFunc) roundTripFunc {
	return func(req *http.Request) (*http.Response, error) {
		p.mu.Lock()
		if !p.active || !p.match(req) {
			p.mu.Unlock()
			return next(req)
		}
		p.calls++
		p.inFlight++
		p.peak = max(p.peak, p.inFlight)
		if p.inFlight >= p.want && !p.reached {
			p.reached = true
			close(p.release)
		}
		p.mu.Unlock()
		select {
		case <-p.release:
		case <-time.After(time.Second):
		}
		defer func() { p.mu.Lock(); p.inFlight--; p.mu.Unlock() }()
		return next(req)
	}
}

func (p *readProbe) start() { p.mu.Lock(); p.active = true; p.mu.Unlock() }

func (p *readProbe) check(t *testing.T, calls, limit int) {
	t.Helper()
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.calls != calls || p.peak < p.want || p.peak > limit {
		t.Fatalf("calls=%d (want %d) peak=%d (want %d..%d)", p.calls, calls, p.peak, p.want, limit)
	}
}

func deniedCode(err error) string {
	var call *contracts.ProviderCallError
	if errors.As(err, &call) {
		return call.Provider.Code
	}
	return ""
}

// after makes a request wait (bounded, so a serial walk fails instead of
// hanging) until another request has been answered.
func after(done <-chan struct{}) {
	select {
	case <-done:
	case <-time.After(2 * time.Second):
	}
}

func identityProbeRuntime(t *testing.T, s *identityScenario, wrap func(roundTripFunc) roundTripFunc) *Runtime {
	r := protocolRuntime(t, wrap(s.transport(t)))
	credentials := r.credentials
	r.credentials = credentialFunc(func(ctx context.Context, id asset.ConnectionID) (contracts.Credential, error) {
		c, err := credentials.Resolve(ctx, id)
		c.Values["identity_group_parent"] = s.root
		return c, err
	})
	return r
}

func identityManyMembers() *identityScenario {
	s := newIdentityScenario()
	for i := range 10 {
		name := fmt.Sprintf("%s/memberships/m-%02d", identityTestGroup, i)
		s.members[name] = map[string]any{"name": name, "preferredMemberKey": map[string]any{"id": fmt.Sprintf("user%02d@example.test", i)}, "type": "USER", "roles": []any{map[string]any{"name": "MEMBER"}}, "createTime": "2026-01-03T00:00:00Z", "updateTime": "2026-01-04T00:00:00Z", "deliverySetting": "ALL_MAIL"}
	}
	return s
}

func TestIdentityGroupReadbackReadsMembershipsConcurrently(t *testing.T) {
	s := identityManyMembers()
	_, _, _, request := identityReviewed(t, s)
	probe := newReadProbe(identityGroupConcurrency, func(r *http.Request) bool {
		return r.Method == "GET" && strings.Contains(r.URL.Path, "/memberships/")
	})
	r := identityProbeRuntime(t, s, probe.wrap)
	driver, err := r.ResolveAction(context.Background(), "connection", request.Asset)
	if err != nil {
		t.Fatal(err)
	}
	result, err := driver.Execute(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	probe.start()
	wait, err := driver.Wait(context.Background(), request, roundTripDataformJSON(t, result))
	if err != nil || !wait.Done {
		t.Fatalf("wait %+v %v", wait, err)
	}
	// Two passes over every reviewed membership, never more than the bound.
	probe.check(t, 2*len(request.LifecycleImpacts), identityGroupConcurrency)
}

func TestIdentityGroupReadbackKeepsMembershipOrder(t *testing.T) {
	s := identityManyMembers()
	s.linger = true
	_, _, _, request := identityReviewed(t, s)
	var proofs []identityMemberProof
	if err := json.Unmarshal([]byte(text(request.Asset.Normalized[identityMembers])), &proofs); err != nil || len(proofs) < 2 {
		t.Fatal(proofs, err)
	}
	// The first membership survives but answers last; the second fails first.
	// A serial walk reports the survivor, so ordered evaluation must too.
	var active atomic.Bool
	failed := make(chan struct{})
	r := identityProbeRuntime(t, s, func(next roundTripFunc) roundTripFunc {
		return func(req *http.Request) (*http.Response, error) {
			if active.Load() && req.Method == "GET" && strings.HasSuffix(req.URL.Path, "/"+proofs[1].ID) {
				defer close(failed)
				return apiResponse(req, 400, `{"error":{"code":400,"status":"INVALID_ARGUMENT"}}`), nil
			}
			if active.Load() && req.Method == "GET" && strings.HasSuffix(req.URL.Path, "/"+proofs[0].ID) {
				after(failed)
			}
			return next(req)
		}
	})
	driver, err := r.ResolveAction(context.Background(), "connection", request.Asset)
	if err != nil {
		t.Fatal(err)
	}
	result, err := driver.Execute(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	active.Store(true)
	wait, err := driver.Wait(context.Background(), request, roundTripDataformJSON(t, result))
	if err != nil || wait.Done || wait.State != "memberships_deleting" {
		t.Fatalf("wait %+v %v", wait, err)
	}
}

const poolTestZoneDisks = "/zones/us-central1-a/disks/"

// poolManyDisks reviews the pool with eleven more local disks than the scenario
// lists; the probe answers every disk GET itself.
func poolManyDisks(t *testing.T, wrap func(roundTripFunc) roundTripFunc) (*poolCleanupScenario, contracts.ActionDriver, contracts.ActionRequest, []poolMember) {
	t.Helper()
	s := newPoolCleanupScenario()
	_, values := poolCleanupReviewed(t, s)
	pool := values[0]
	var members []poolMember
	if err := json.Unmarshal([]byte(text(pool.Normalized[poolMembersKey])), &members); err != nil || len(members) != 1 {
		t.Fatal(members, err)
	}
	for i := 1; i <= 11; i++ {
		members = append(members, poolMember{ID: fmt.Sprintf("%s-%02d", poolDiskID, i), Created: members[0].Created})
	}
	encoded, _ := json.Marshal(members)
	pool.Normalized[poolMembersKey] = string(encoded)
	pool.Normalized[poolSnapshotKey] = infraManifestHash(text(pool.Normalized[poolConfigurationKey]), string(encoded))
	s.disk = nil
	r := protocolRuntime(t, wrap(s.transport(t)))
	driver, err := r.ResolveAction(t.Context(), "connection", pool)
	if err != nil {
		t.Fatal(err)
	}
	return s, driver, poolRequest(values), members
}

func poolDiskGet(r *http.Request) bool {
	return r.Method == "GET" && strings.Contains(r.URL.Path, poolTestZoneDisks)
}

func TestStoragePoolDiskReadsAreConcurrent(t *testing.T) {
	for _, phase := range []string{"preflight", "readback"} {
		t.Run(phase, func(t *testing.T) {
			probe := newReadProbe(groupReadConcurrency, poolDiskGet)
			s, driver, request, members := poolManyDisks(t, func(next roundTripFunc) roundTripFunc {
				return probe.wrap(func(req *http.Request) (*http.Response, error) {
					if poolDiskGet(req) {
						return apiResponse(req, 404, `{"error":{"code":404}}`), nil
					}
					return next(req)
				})
			})
			probe.start()
			if phase == "preflight" {
				check, err := driver.Preflight(t.Context(), request)
				if err != nil || !check.Allowed {
					t.Fatal(check, err)
				}
				probe.check(t, 2*len(members), groupReadConcurrency)
				return
			}
			s.pool = nil
			read, err := driver.Readback(t.Context(), request)
			if err != nil || read.Exists {
				t.Fatal(read, err)
			}
			probe.check(t, len(members), groupReadConcurrency)
		})
	}
}

func TestStoragePoolDiskReadsKeepOrder(t *testing.T) {
	// The first disk survives but answers last; the second fails first.
	for _, phase := range []string{"preflight", "readback"} {
		t.Run(phase, func(t *testing.T) {
			failed := make(chan struct{})
			var members []poolMember
			s, driver, request, reviewed := poolManyDisks(t, func(next roundTripFunc) roundTripFunc {
				return func(req *http.Request) (*http.Response, error) {
					if !poolDiskGet(req) {
						return next(req)
					}
					switch {
					case strings.HasSuffix(req.URL.Path, last(members[1].ID)):
						defer close(failed)
						return apiResponse(req, 400, `{"error":{"code":400}}`), nil
					case strings.HasSuffix(req.URL.Path, poolTestZoneDisks+last(members[0].ID)):
						after(failed)
						return apiResponse(req, 200, `{}`), nil
					}
					return apiResponse(req, 404, `{"error":{"code":404}}`), nil
				}
			})
			members = reviewed
			if phase == "preflight" {
				// Serial: the surviving first disk decides before the second's error.
				check, err := driver.Preflight(t.Context(), request)
				if err != nil || check.Allowed || check.Reason != "storage_pool_disks_still_exist" {
					t.Fatal(check, err)
				}
				return
			}
			// Serial: a survivor does not stop the walk, so the later error wins.
			s.pool = nil
			if read, err := driver.Readback(t.Context(), request); err == nil {
				t.Fatal("disk read error hidden by an earlier survivor", read)
			}
		})
	}
}

func infraDescendantRequest(count int) contracts.ActionRequest {
	request := contracts.ActionRequest{Asset: asset.Asset{ID: "deployment"}}
	for i := range count {
		name := fmt.Sprintf("vm-%02d", i)
		request.LifecycleImpacts = append(request.LifecycleImpacts, contracts.ActionImpact{ControllerID: "other", Asset: asset.Asset{ID: asset.AssetID(name), Identity: asset.Identity{NativeType: instanceType, NativeID: "//compute.googleapis.com/projects/sample-project/zones/us-central1-a/instances/" + name}, Normalized: map[string]any{"name": name}}})
	}
	return request
}

func TestInfraRetainedDescendantsReadConcurrentlyInOrder(t *testing.T) {
	live := func(req *http.Request) (*http.Response, error) {
		return apiResponse(req, 200, fmt.Sprintf(`{"name":%q}`, last(req.URL.Path))), nil
	}
	probe := newReadProbe(groupReadConcurrency, func(*http.Request) bool { return true })
	probe.start()
	request := infraDescendantRequest(12)
	a := protocolAction(t, instanceType, "projects/sample-project/zones/us-central1-a/instances/web", probe.wrap(live))
	if err := a.infraRetainedDescendants(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	probe.check(t, 12, groupReadConcurrency)

	// The first descendant changed but answers last; the second is missing and
	// answers first. A serial walk reports the change.
	missing := make(chan struct{})
	a = protocolAction(t, instanceType, "projects/sample-project/zones/us-central1-a/instances/web", func(req *http.Request) (*http.Response, error) {
		switch last(req.URL.Path) {
		case "vm-01":
			defer close(missing)
			return apiResponse(req, 404, `{"error":{"code":404}}`), nil
		case "vm-00":
			after(missing)
			return apiResponse(req, 200, `{"name":"vm-00","labels":{"changed":"yes"}}`), nil
		}
		return live(req)
	})
	if err := a.infraRetainedDescendants(t.Context(), request); deniedCode(err) != "infra_retained_descendant_changed" {
		t.Fatal(err)
	}
}

func impactPath(id string) string {
	_, rest, _ := strings.Cut(strings.TrimPrefix(id, "//"), "/")
	return "/" + rest
}

func TestBatchReadbackAndChildrenReadConcurrently(t *testing.T) {
	s, _, _, _, request := batchReviewed(t)
	paths := map[string]bool{}
	for _, impact := range request.LifecycleImpacts {
		paths[impactPath(impact.Asset.Identity.NativeID)] = true
	}
	isImpact := func(r *http.Request) bool {
		path := strings.TrimPrefix(strings.TrimPrefix(r.URL.Path, "/compute/v1"), "/v1")
		return r.Method == "GET" && paths[path]
	}
	children := newReadProbe(2, func(r *http.Request) bool {
		return r.Method == "GET" && r.URL.Host == "compute.googleapis.com" && !strings.Contains(r.URL.Path, "/aggregated/") && !strings.HasSuffix(r.URL.Path, "/disks")
	})
	readback := newReadProbe(len(request.LifecycleImpacts), isImpact)
	r := protocolRuntime(t, readback.wrap(children.wrap(s.transport(t))))
	driver, err := r.ResolveAction(context.Background(), "connection", request.Asset)
	if err != nil {
		t.Fatal(err)
	}
	children.start()
	check, err := driver.Preflight(context.Background(), request)
	if err != nil || !check.Allowed {
		t.Fatalf("preflight %+v %v", check, err)
	}
	children.mu.Lock()
	children.active = false
	if children.peak < 2 {
		t.Fatalf("Batch compute members read serially: peak=%d", children.peak)
	}
	children.mu.Unlock()
	result, err := driver.Execute(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	s.operation["done"] = true
	delete(s.resources, batchRoot)
	s.mu.Unlock()
	readback.start()
	wait, err := driver.Wait(context.Background(), request, result)
	if err != nil || wait.Done {
		t.Fatalf("live members ignored %+v %v", wait, err)
	}
	// Every member is read once even though the first already keeps it pending.
	readback.check(t, len(request.LifecycleImpacts), groupReadConcurrency)
}
