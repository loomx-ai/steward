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
	receipt := roundTripDataformJSON(t, result)
	wait, err := driver.Wait(context.Background(), request, receipt)
	if err != nil || !wait.Done {
		t.Fatalf("wait %+v %v", wait, err)
	}
	// Wait proves only the group absent; the worker always reads back next.
	probe.mu.Lock()
	waitCalls := probe.calls
	probe.mu.Unlock()
	if waitCalls != 0 {
		t.Fatalf("wait read %d memberships", waitCalls)
	}
	request.ExecutionResult = &receipt
	read, err := driver.Readback(context.Background(), request)
	if err != nil || read.Exists {
		t.Fatalf("readback %+v %v", read, err)
	}
	// Memberships are read once: the second pass only re-reads the group, whose
	// unique ID no membership can outlive or be re-added under.
	probe.check(t, len(request.LifecycleImpacts), identityGroupConcurrency)
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
	receipt := roundTripDataformJSON(t, result)
	request.ExecutionResult = &receipt
	read, err := driver.Readback(context.Background(), request)
	if err != nil || !read.Exists || read.State != "memberships_deleting" {
		t.Fatalf("readback %+v %v", read, err)
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

func deleteImpacts(count int, del bool) contracts.ActionRequest {
	request := contracts.ActionRequest{Asset: asset.Asset{ID: "root", Identity: asset.Identity{Provider: asset.ProviderGCP, ConnectionID: "connection", Partition: "gcp"}}}
	for i := range count {
		name := fmt.Sprintf("vm-%02d", i)
		request.LifecycleImpacts = append(request.LifecycleImpacts, contracts.ActionImpact{ControllerID: "root", Delete: del, Asset: asset.Asset{ID: asset.AssetID(name), Identity: asset.Identity{Provider: asset.ProviderGCP, ConnectionID: "connection", Partition: "gcp", NativeType: instanceType, NativeID: "//compute.googleapis.com/projects/sample-project/zones/us-central1-a/instances/" + name}, Normalized: map[string]any{"id": name}}})
	}
	return request
}

// firstSurvivesSecondFails answers vm-00 (or job-00) with 200 only after vm-01
// (job-01) failed; a serial walk reports the survivor.
func firstSurvivesSecondFails(body string) roundTripFunc {
	failed := make(chan struct{})
	return func(req *http.Request) (*http.Response, error) {
		switch {
		case strings.HasSuffix(req.URL.Path, "-01"):
			defer close(failed)
			return apiResponse(req, 400, `{"error":{"code":400}}`), nil
		case strings.HasSuffix(req.URL.Path, "-00"):
			after(failed)
			return apiResponse(req, 200, body), nil
		}
		return apiResponse(req, 404, `{"error":{"code":404}}`), nil
	}
}

func notFound(req *http.Request) (*http.Response, error) {
	return apiResponse(req, 404, `{"error":{"code":404}}`), nil
}

func TestComputeMembersReadbackReadsConcurrentlyInOrder(t *testing.T) {
	const vm = "projects/sample-project/zones/us-central1-a/instances/web"
	probe := newReadProbe(groupReadConcurrency, func(*http.Request) bool { return true })
	probe.start()
	a := protocolAction(t, instanceType, vm, probe.wrap(notFound))
	read, err := a.computeMembersReadback(t.Context(), deleteImpacts(12, true), "gke")
	if err != nil || read.Exists {
		t.Fatal(read, err)
	}
	probe.check(t, 12, groupReadConcurrency)

	a = protocolAction(t, instanceType, vm, firstSurvivesSecondFails(`{"id":"vm-00"}`))
	read, err = a.computeMembersReadback(t.Context(), deleteImpacts(12, true), "gke")
	if err != nil || !read.Exists || read.State != "waiting_for_gke_members" {
		t.Fatal(read, err)
	}
	// A retained member that vanished decides before a later read error.
	a = protocolAction(t, instanceType, vm, func(req *http.Request) (*http.Response, error) {
		if strings.HasSuffix(req.URL.Path, "-00") {
			return notFound(req)
		}
		return apiResponse(req, 200, fmt.Sprintf(`{"id":%q}`, last(req.URL.Path))), nil
	})
	if _, err := a.computeMembersReadback(t.Context(), deleteImpacts(12, false), "gke"); deniedCode(err) != "gke_retained_member_missing" {
		t.Fatal(err)
	}
}

func TestGKENodeWaitReadsConcurrentlyInOrder(t *testing.T) {
	const vm = "projects/sample-project/zones/us-central1-a/instances/web"
	probe := newReadProbe(groupReadConcurrency, func(*http.Request) bool { return true })
	probe.start()
	a := protocolAction(t, instanceType, vm, probe.wrap(notFound))
	if survives, err := a.impactsSurvive(t.Context(), deleteImpacts(12, true).LifecycleImpacts); err != nil || survives {
		t.Fatal(survives, err)
	}
	probe.check(t, 12, groupReadConcurrency)
	a = protocolAction(t, instanceType, vm, firstSurvivesSecondFails(`{}`))
	if survives, err := a.impactsSurvive(t.Context(), deleteImpacts(12, true).LifecycleImpacts); err != nil || !survives {
		t.Fatal(survives, err)
	}
}

func dataprocTestJobs(count int) []contracts.ActionImpact {
	var jobs []contracts.ActionImpact
	for i := range count {
		jobs = append(jobs, contracts.ActionImpact{Delete: true, Asset: asset.Asset{Identity: asset.Identity{NativeType: dataprocJobType, NativeID: fmt.Sprintf("//dataproc.googleapis.com/projects/sample-project/regions/us-central1/jobs/job-%02d", i)}}})
	}
	return jobs
}

func TestDataprocPrerequisiteReadsConcurrentlyInOrder(t *testing.T) {
	probe := newReadProbe(groupReadConcurrency, func(*http.Request) bool { return true })
	probe.start()
	a := protocolAction(t, dataprocClusterType, dpRoot, probe.wrap(notFound))
	request := contracts.ActionRequest{PrerequisiteDeletions: dataprocTestJobs(12)}
	if err := a.dataprocPrerequisitesAbsent(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	probe.check(t, 12, groupReadConcurrency)
	a = protocolAction(t, dataprocClusterType, dpRoot, firstSurvivesSecondFails(`{}`))
	if err := a.dataprocPrerequisitesAbsent(t.Context(), request); deniedCode(err) != "dataproc_prerequisite_still_exists" {
		t.Fatal(err)
	}
}

func TestDataprocJobsReadConcurrentlyInOrder(t *testing.T) {
	root := asset.Asset{Identity: asset.Identity{NativeType: dataprocClusterType, NativeID: "//dataproc.googleapis.com/" + dpRoot}}
	cluster := map[string]any{"clusterName": "analytics", "clusterUuid": "uuid-1"}
	job := func(id, state string) map[string]any {
		return map[string]any{"reference": map[string]any{"projectId": "sample-project", "jobId": id}, "jobUuid": "u-" + id, "placement": map[string]any{"clusterName": "analytics", "clusterUuid": "uuid-1"}, "status": map[string]any{"state": state}}
	}
	transport := func(get roundTripFunc) roundTripFunc {
		return func(req *http.Request) (*http.Response, error) {
			if strings.HasSuffix(req.URL.Path, "/jobs") {
				var rows []any
				for i := range 12 {
					rows = append(rows, job(fmt.Sprintf("job-%02d", i), "DONE"))
				}
				raw, _ := json.Marshal(map[string]any{"jobs": rows})
				return apiResponse(req, 200, string(raw)), nil
			}
			return get(req)
		}
	}
	live := func(req *http.Request) (*http.Response, error) {
		raw, _ := json.Marshal(job(last(req.URL.Path), "DONE"))
		return apiResponse(req, 200, string(raw)), nil
	}
	probe := newReadProbe(groupReadConcurrency, func(req *http.Request) bool { return strings.Contains(req.URL.Path, "/jobs/") })
	probe.start()
	a := protocolAction(t, dataprocClusterType, dpRoot, transport(probe.wrap(live)))
	jobs, err := a.client.dataprocJobs(t.Context(), root, cluster)
	if err != nil || len(jobs) != 12 || jobs[0].id >= jobs[11].id {
		t.Fatal(len(jobs), err)
	}
	probe.check(t, 12, groupReadConcurrency)
	// job-00 has an unknown state but answers last; job-01 fails first.
	raw, _ := json.Marshal(job("job-00", "UNKNOWN"))
	ordered := firstSurvivesSecondFails(string(raw))
	a = protocolAction(t, dataprocClusterType, dpRoot, transport(func(req *http.Request) (*http.Response, error) {
		if strings.HasSuffix(req.URL.Path, "-00") || strings.HasSuffix(req.URL.Path, "-01") {
			return ordered(req)
		}
		return live(req)
	}))
	if _, err := a.client.dataprocJobs(t.Context(), root, cluster); deniedCode(err) != "dataproc_job_state_unknown" {
		t.Fatal(err)
	}
}

func TestDataprocUnlistedMembersReadConcurrentlyInOrder(t *testing.T) {
	impacts, err := groupImpacts(deleteImpacts(12, true))
	if err != nil {
		t.Fatal(err)
	}
	keys := sortedImpactKeys(impacts)
	probe := newReadProbe(groupReadConcurrency, func(*http.Request) bool { return true })
	probe.start()
	a := protocolAction(t, dataprocClusterType, dpRoot, probe.wrap(notFound))
	if err := a.dataprocUnlistedMembers(t.Context(), impacts, keys); err != nil {
		t.Fatal(err)
	}
	probe.check(t, 12, groupReadConcurrency)
	a = protocolAction(t, dataprocClusterType, dpRoot, firstSurvivesSecondFails(`{}`))
	if err := a.dataprocUnlistedMembers(t.Context(), impacts, keys); deniedCode(err) != "dataproc_member_membership_changed" {
		t.Fatal(err)
	}
}

func TestDataprocReadbackReadsMembersConcurrently(t *testing.T) {
	s := newDataprocScenario(t)
	_, _, _, request := dataprocReviewed(t, s)
	paths := map[string]bool{}
	for _, impact := range request.LifecycleImpacts {
		paths[impactPath(impact.Asset.Identity.NativeID)] = true
	}
	probe := newReadProbe(groupReadConcurrency, func(r *http.Request) bool {
		return r.Method == "GET" && paths[strings.TrimPrefix(strings.TrimPrefix(r.URL.Path, "/compute/v1"), "/v1")]
	})
	r := protocolRuntime(t, probe.wrap(s.transport(t)))
	driver, err := r.ResolveAction(context.Background(), "connection", request.Asset)
	if err != nil {
		t.Fatal(err)
	}
	delete(s.resources, dpRoot)
	probe.start()
	read, err := driver.Readback(context.Background(), request)
	if err != nil || !read.Exists {
		t.Fatal(read, err)
	}
	// Every member once, plus the retained job's re-read by the job listing.
	probe.check(t, len(request.LifecycleImpacts)+1, groupReadConcurrency)
}

func TestIdentityMembershipPreflightReadsOnlyItsGroupAndMembership(t *testing.T) {
	s := identityManyMembers()
	r, values, _, _ := identityReviewed(t, s)
	name := identityTestGroup + "/memberships/m-03"
	value := batchAsset(values, name)
	driver, err := r.ResolveAction(context.Background(), "connection", value)
	if err != nil {
		t.Fatal(err)
	}
	request := contracts.ActionRequest{Asset: value, Action: "delete", IdempotencyKey: name}
	for _, test := range []struct {
		name   string
		change func()
		check  func(contracts.PreflightResult, error) bool
	}{
		{"unchanged", func() {}, func(c contracts.PreflightResult, err error) bool { return err == nil && c.Allowed && !c.Absent }},
		// Another member's change does not concern this unlink.
		{"other_member_changed", func() { s.members[identityTestGroup+"/memberships/m-04"]["deliverySetting"] = "NONE" }, func(c contracts.PreflightResult, err error) bool { return err == nil && c.Allowed }},
		{"member_changed", func() { s.members[name]["deliverySetting"] = "NONE" }, func(_ contracts.PreflightResult, err error) bool {
			return deniedCode(err) == "identity_membership_configuration_changed"
		}},
		{"member_removed", func() { delete(s.members, name) }, func(c contracts.PreflightResult, err error) bool { return err == nil && c.Allowed && c.Absent }},
		// Changes accumulate; the group change goes last.
		{"group_changed", func() { s.groups[identityTestGroup]["description"] = "changed" }, func(_ contracts.PreflightResult, err error) bool {
			return deniedCode(err) == "identity_membership_parent_changed"
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			test.change()
			s.calls = nil
			check, err := driver.Preflight(context.Background(), request)
			if !test.check(check, err) {
				t.Fatal(check, err)
			}
			want := []string{"GET https://cloudidentity.googleapis.com/v1/" + identityTestGroup, "GET https://cloudidentity.googleapis.com/v1/" + name, "GET https://cloudidentity.googleapis.com/v1/" + identityTestGroup}
			if strings.Join(s.calls, "\n") != strings.Join(want, "\n") {
				t.Fatalf("calls %q", s.calls)
			}
		})
	}
}

func TestBatchPreflightReusesAutoDeleteDiskReads(t *testing.T) {
	s, _, _, _, request := batchReviewed(t)
	var mu sync.Mutex
	reads := 0
	inner := s.transport(t)
	r := protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
		if req.Method == "GET" && strings.HasSuffix(req.URL.Path, "/"+batchBoot) {
			mu.Lock()
			reads++
			mu.Unlock()
		}
		return inner(req)
	})
	driver, err := r.ResolveAction(context.Background(), "connection", request.Asset)
	if err != nil {
		t.Fatal(err)
	}
	check, err := driver.Preflight(context.Background(), request)
	if err != nil || !check.Allowed {
		t.Fatal(check, err)
	}
	if reads != 1 {
		t.Fatalf("auto-delete boot disk read %d times", reads)
	}
}

func TestStoragePoolReadbackSkipsDisksWhilePoolExists(t *testing.T) {
	disks := 0
	_, driver, request, _ := poolManyDisks(t, func(next roundTripFunc) roundTripFunc {
		return func(req *http.Request) (*http.Response, error) {
			if poolDiskGet(req) {
				disks++
				return apiResponse(req, 500, `{"error":{"code":500}}`), nil
			}
			return next(req)
		}
	})
	read, err := driver.Readback(t.Context(), request)
	if err != nil || !read.Exists || disks != 0 {
		t.Fatal(read, err, disks)
	}
}

// serviceManyChildren reviews a Service Directory namespace with twelve
// services as lifecycle impacts and a Discovery collection with twelve engines
// as prerequisites.
func serviceManyChildren(t *testing.T, transport roundTripFunc) (*action, contracts.ActionRequest, *action, contracts.ActionRequest) {
	t.Helper()
	identity := func(kind, name string) asset.Identity {
		return asset.Identity{Provider: asset.ProviderGCP, ConnectionID: "gcp-connection", Partition: "google-cloud", NativeType: kind, NativeID: "//" + strings.Split(kind, "/")[0] + "/" + name}
	}
	namespace := "projects/sample-project/locations/us-central1/namespaces/apps"
	cascade := contracts.ActionRequest{Action: "delete", Asset: asset.Asset{ID: "namespace", Identity: identity("servicedirectory.googleapis.com/Namespace", namespace)}}
	collection := "projects/sample-project/locations/global/collections/docs"
	prerequisites := contracts.ActionRequest{Action: "delete", Asset: asset.Asset{ID: "collection", Identity: identity(discoveryHost+"/Collection", collection)}}
	for i := range 12 {
		service := asset.Asset{ID: asset.AssetID(fmt.Sprintf("service-%02d", i)), Identity: identity("servicedirectory.googleapis.com/Service", fmt.Sprintf("%s/services/s-%02d", namespace, i))}
		cascade.LifecycleImpacts = append(cascade.LifecycleImpacts, contracts.ActionImpact{ControllerID: "namespace", Asset: service, Delete: true})
		engine := asset.Asset{ID: asset.AssetID(fmt.Sprintf("engine-%02d", i)), Identity: identity(discoveryHost+"/Engine", fmt.Sprintf("%s/engines/e-%02d", collection, i))}
		prerequisites.PrerequisiteDeletions = append(prerequisites.PrerequisiteDeletions, contracts.ActionImpact{ControllerID: "collection", Asset: engine, Delete: true})
	}
	return protocolAction(t, cascade.Asset.Identity.NativeType, namespace, transport), cascade, protocolAction(t, prerequisites.Asset.Identity.NativeType, collection, transport), prerequisites
}

func serviceChildGet(r *http.Request) bool {
	return r.Method == "GET" && (strings.Contains(r.URL.Path, "/services/s-") || strings.Contains(r.URL.Path, "/engines/e-"))
}

func TestServiceChildReadsAreConcurrent(t *testing.T) {
	for _, phase := range []string{"cascade", "prerequisites"} {
		t.Run(phase, func(t *testing.T) {
			probe := newReadProbe(groupReadConcurrency, serviceChildGet)
			cascadeAction, cascade, prerequisiteAction, prerequisites := serviceManyChildren(t, probe.wrap(func(req *http.Request) (*http.Response, error) {
				return apiResponse(req, 404, `{"error":{"code":404}}`), nil
			}))
			probe.start()
			if phase == "cascade" {
				read, err := cascadeAction.serviceCascadeReadback(t.Context(), cascade)
				if err != nil || read.Exists {
					t.Fatal(read, err)
				}
				probe.check(t, len(cascade.LifecycleImpacts), groupReadConcurrency)
				return
			}
			if err := prerequisiteAction.servicePrerequisitesAbsent(t.Context(), prerequisites); err != nil {
				t.Fatal(err)
			}
			probe.check(t, len(prerequisites.PrerequisiteDeletions), groupReadConcurrency)
		})
	}
}

func TestServiceChildReadsKeepOrder(t *testing.T) {
	// The first child survives but answers last; the second fails first. A
	// serial walk reports the survivor, so ordered evaluation must too.
	failed := make(chan struct{})
	var once sync.Once
	cascadeAction, cascade, prerequisiteAction, prerequisites := serviceManyChildren(t, func(req *http.Request) (*http.Response, error) {
		switch {
		case strings.HasSuffix(req.URL.Path, "/s-01") || strings.HasSuffix(req.URL.Path, "/e-01"):
			defer once.Do(func() { close(failed) })
			return apiResponse(req, 400, `{"error":{"code":400}}`), nil
		case strings.HasSuffix(req.URL.Path, "/s-00") || strings.HasSuffix(req.URL.Path, "/e-00"):
			after(failed)
			return apiResponse(req, 200, `{}`), nil
		}
		return apiResponse(req, 404, `{"error":{"code":404}}`), nil
	})
	read, err := cascadeAction.serviceCascadeReadback(t.Context(), cascade)
	if err != nil || !read.Exists || read.State != "service_children_deleting" {
		t.Fatal(read, err)
	}
	failed, once = make(chan struct{}), sync.Once{}
	if err := prerequisiteAction.servicePrerequisitesAbsent(t.Context(), prerequisites); deniedCode(err) != "service_prerequisite_still_exists" {
		t.Fatal(err)
	}
	// Every prerequisite is validated before any is read.
	reads := 0
	_, _, prerequisiteAction, prerequisites = serviceManyChildren(t, func(req *http.Request) (*http.Response, error) {
		reads++
		return apiResponse(req, 404, `{"error":{"code":404}}`), nil
	})
	prerequisites.PrerequisiteDeletions[11].Delete = false
	if err := prerequisiteAction.servicePrerequisitesAbsent(t.Context(), prerequisites); deniedCode(err) != "invalid_service_prerequisite" || reads != 0 {
		t.Fatal(err, reads)
	}
}

func TestGKEEnrichReadsClustersConcurrently(t *testing.T) {
	const prefix = "projects/sample-project/locations/us-central1/clusters/c-"
	clusterGet := func(r *http.Request) bool {
		return r.Method == "GET" && strings.Contains(r.URL.Path, "/clusters/c-")
	}
	items := func() []contracts.InventoryItem {
		var result []contracts.InventoryItem
		for i := range groupReadConcurrency + 2 {
			result = append(result, contracts.InventoryItem{NativeType: clusterType, NativeID: fmt.Sprintf("//container.googleapis.com/%s%02d", prefix, i), Normalized: map[string]any{"id": "planned"}})
		}
		return result
	}
	t.Run("concurrent", func(t *testing.T) {
		// Every cluster changed identity, so each stops after its cluster GET.
		probe := newReadProbe(groupReadConcurrency, clusterGet)
		r := protocolRuntime(t, probe.wrap(func(req *http.Request) (*http.Response, error) {
			return apiResponse(req, 200, `{"id":"other"}`), nil
		}))
		probe.start()
		if _, err := r.EnrichInventoryBatch(t.Context(), contracts.InventoryRequest{ConnectionID: "connection"}, items()); deniedCode(err) != "gke_cluster_identity_changed" {
			t.Fatal(err)
		}
		probe.check(t, groupReadConcurrency, groupReadConcurrency)
	})
	t.Run("order", func(t *testing.T) {
		// The first cluster changed identity but answers last; the second fails first.
		failed := make(chan struct{})
		r := protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
			switch {
			case strings.HasSuffix(req.URL.Path, "/c-01"):
				defer close(failed)
				return apiResponse(req, 400, `{"error":{"code":400}}`), nil
			case strings.HasSuffix(req.URL.Path, "/c-00"):
				after(failed)
			}
			return apiResponse(req, 200, `{"id":"other"}`), nil
		})
		if _, err := r.EnrichInventoryBatch(t.Context(), contracts.InventoryRequest{ConnectionID: "connection"}, items()); deniedCode(err) != "gke_cluster_identity_changed" {
			t.Fatal(err)
		}
	})
}

func TestStoragePoolContributionReadsPoolsConcurrently(t *testing.T) {
	poolGet := func(r *http.Request) bool {
		return r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/storagePools/pool")
	}
	probe := newReadProbe(groupReadConcurrency, poolGet)
	s := newPoolCleanupScenario()
	_, values := poolCleanupReviewed(t, s)
	r := protocolRuntime(t, probe.wrap(s.transport(t)))
	// Ten reviews of the one native pool, merged in asset order.
	var assets []asset.Asset
	for i := range groupReadConcurrency + 2 {
		pool := values[0]
		pool.ID = asset.AssetID(fmt.Sprintf("pool-%02d", i))
		assets = append(assets, pool)
	}
	assets = append(assets, values[1:]...)
	contributor, err := r.ComputeLifecycle(t.Context(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	probe.start()
	c, err := contributor.(*computeGroups).contributeStoragePools(t.Context(), assets, nil)
	if err != nil || len(c.Unresolved) != 0 || len(c.Relationships) != groupReadConcurrency+2 {
		t.Fatal(c, err)
	}
	for i, relationship := range c.Relationships {
		if relationship.SourceAssetID != assets[i].ID {
			t.Fatalf("relationship %d from %s", i, relationship.SourceAssetID)
		}
	}
	// Each pool is read twice around its two member listings.
	probe.check(t, 2*(groupReadConcurrency+2), groupReadConcurrency)
}

// TestMonitoringDependencyValidationReadsConcurrentlyInOrder covers the
// alert policy, notification channel and group validation passes of the
// Monitoring dependency contribution. Every review is stale, so each run stops
// at the first pass.
func TestMonitoringDependencyValidationReadsConcurrentlyInOrder(t *testing.T) {
	for _, entry := range []struct {
		kind, review, fixtureName, denied string
		fixture                           func() map[string]any
		contribute                        func(*monitoringDependencies, context.Context, []asset.Asset) error
	}{
		{alertPolicyType, alertPolicyReview, alertPolicyName, "monitoring_configuration_changed", alertPolicyFixture, func(h *monitoringDependencies, ctx context.Context, values []asset.Asset) error {
			_, err := h.monitoringDashboardPolicyDependencies(ctx, values)
			return err
		}},
		{notificationChannelType, notificationChannelReview, notificationChannelName, "notification_channel_configuration_changed", notificationChannelFixture, func(h *monitoringDependencies, ctx context.Context, values []asset.Asset) error {
			_, err := h.notificationChannelDependencies(ctx, values)
			return err
		}},
		{monitoringGroupType, monitoringGroupReview, testMonitoringGroupName, "monitoring_group_configuration_changed", monitoringGroupFixture, func(h *monitoringDependencies, ctx context.Context, values []asset.Asset) error {
			_, err := h.monitoringGroupDependencies(ctx, values)
			return err
		}},
	} {
		t.Run(entry.kind, func(t *testing.T) {
			prefix := entry.fixtureName[:strings.LastIndex(entry.fixtureName, "/")+1]
			fixture, _ := json.Marshal(entry.fixture())
			live := func(req *http.Request) (*http.Response, error) {
				name := prefix + last(req.URL.Path)
				return apiResponse(req, 200, strings.ReplaceAll(string(fixture), entry.fixtureName, name)), nil
			}
			var values []asset.Asset
			for i := range groupReadConcurrency + 4 {
				values = append(values, asset.Asset{ID: asset.AssetID(fmt.Sprintf("a-%02d", i)), Identity: asset.Identity{ConnectionID: "connection", Provider: asset.ProviderGCP, Partition: "gcp", NativeType: entry.kind, NativeID: fmt.Sprintf("//monitoring.googleapis.com/%s100%02d", prefix, i)}, Normalized: map[string]any{entry.review: "stale"}})
			}
			run := func(transport roundTripFunc) error {
				c, err := protocolRuntime(t, transport).resolve(t.Context(), "connection")
				if err != nil {
					t.Fatal(err)
				}
				return entry.contribute(&monitoringDependencies{client: c, connection: "connection"}, t.Context(), values)
			}
			probe := newReadProbe(groupReadConcurrency, func(r *http.Request) bool { return r.Method == "GET" && strings.Contains(r.URL.Path, prefix+"100") })
			probe.start()
			if err := run(probe.wrap(live)); deniedCode(err) != entry.denied {
				t.Fatal(err)
			}
			probe.check(t, len(values), groupReadConcurrency)
			// The first asset is stale but answers last; the second fails first.
			// A serial walk reports the stale first asset, so ordered evaluation must too.
			failed := make(chan struct{})
			err := run(func(req *http.Request) (*http.Response, error) {
				switch {
				case strings.HasSuffix(req.URL.Path, "/10001"):
					defer close(failed)
					return apiResponse(req, 400, `{"error":{"code":400}}`), nil
				case strings.HasSuffix(req.URL.Path, "/10000"):
					after(failed)
				}
				return live(req)
			})
			if deniedCode(err) != entry.denied {
				t.Fatal(err)
			}
		})
	}
}

func TestComputeReadsListAndGetConcurrentlyInOrder(t *testing.T) {
	// One VM per zone: each zone is its own list, and every list fails so each
	// VM falls back to its own GET.
	var ids []string
	for i := range groupReadConcurrency + 4 {
		ids = append(ids, fmt.Sprintf("//compute.googleapis.com/projects/sample-project/zones/z-%02d/instances/vm-%02d", i, i))
	}
	isList := func(r *http.Request) bool { return strings.HasSuffix(r.URL.Path, "/instances") }
	isGet := func(r *http.Request) bool { return strings.Contains(r.URL.Path, "/instances/vm-") }
	lists := newReadProbe(groupReadConcurrency, isList)
	gets := newReadProbe(groupReadConcurrency, isGet)
	r := protocolRuntime(t, lists.wrap(gets.wrap(func(req *http.Request) (*http.Response, error) {
		if isList(req) {
			return apiResponse(req, 400, `{"error":{"code":400}}`), nil
		}
		return apiResponse(req, 200, fmt.Sprintf(`{"name":%q}`, last(req.URL.Path))), nil
	})))
	c, err := r.resolve(t.Context(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	lists.start()
	gets.start()
	reads, err := c.computeReads(t.Context(), instanceType, append(ids, ids[0]))
	if err != nil || len(reads) != len(ids) {
		t.Fatal(reads, err)
	}
	for _, id := range ids {
		if reads[id]["name"] != last(id) {
			t.Fatalf("%s read %v", id, reads[id])
		}
	}
	lists.check(t, len(ids), groupReadConcurrency)
	gets.check(t, len(ids), groupReadConcurrency)

	// The first GET is denied but answers last; the second fails first. A
	// serial walk reports the first VM's error, so ordered evaluation must too.
	failed := make(chan struct{})
	r = protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
		switch {
		case isList(req):
			return apiResponse(req, 400, `{"error":{"code":400}}`), nil
		case strings.HasSuffix(req.URL.Path, "/vm-01"):
			defer close(failed)
			return apiResponse(req, 400, `{"error":{"code":400}}`), nil
		case strings.HasSuffix(req.URL.Path, "/vm-00"):
			after(failed)
			return apiResponse(req, 403, `{"error":{"code":403}}`), nil
		}
		return apiResponse(req, 200, `{}`), nil
	})
	if c, err = r.resolve(t.Context(), "connection"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.computeReads(t.Context(), instanceType, ids); deniedCode(err) != "403" {
		t.Fatal(err)
	}
}
