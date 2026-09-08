package gcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/app/governance"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

type gkeFixture struct {
	*managedGroupFixture
	cluster, pool  asset.Asset
	gke            map[string]map[string]any
	polls, deletes int
	fault          func(*http.Request) (*http.Response, bool)
}

func newGKEFixture(t *testing.T) *gkeFixture {
	f := &gkeFixture{managedGroupFixture: newManagedGroupFixture(t, false), gke: map[string]map[string]any{}}
	f.live("vm")["deletionProtection"] = false
	// GKE boot disks are disposable; an attached PersistentVolume is retained.
	delete(f.members[0], "preservedStateFromPolicy")
	f.configs = nil
	f.pool = diskAsset("pool", nodePoolType, "unused")
	f.pool.Identity.NativeID = "//container.googleapis.com/projects/sample-project/locations/us-central1-a/clusters/test/nodePools/workers"
	f.pool.Normalized = map[string]any{"name": "workers", "status": "RUNNING", "etag": "pool-etag", "_gke_cluster_uid": "cluster-uid", "instanceGroupUrls": []any{f.live("mig")["selfLink"]}}
	f.cluster = diskAsset("cluster", clusterType, "unused")
	f.cluster.Identity.NativeID = strings.Split(f.pool.Identity.NativeID, "/nodePools/")[0]
	f.cluster.Normalized = map[string]any{"id": "cluster-uid", "name": "test", "location": "us-central1-a", "status": "RUNNING", "nodePools": []any{groupCopy(f.pool.Normalized)}}
	f.gke[f.cluster.Identity.NativeID] = groupCopy(f.cluster.Normalized)
	f.gke[f.pool.Identity.NativeID] = groupCopy(f.pool.Normalized)
	f.assets = append(f.assets, f.cluster, f.pool)
	return f
}

func (f *gkeFixture) roundTrip(r *http.Request) (*http.Response, error) {
	if f.fault != nil {
		if response, handled := f.fault(r); handled {
			return response, nil
		}
	}
	if r.URL.Host != "container.googleapis.com" {
		return f.managedGroupFixture.roundTrip(r)
	}
	reply := func(data any) (*http.Response, error) {
		body, _ := json.Marshal(data)
		return apiResponse(r, 200, string(body)), nil
	}
	if len(r.URL.Query()) != 0 {
		f.t.Fatalf("invented GKE query parameter: %s", r.URL)
	}
	if strings.HasSuffix(r.URL.Path, "/locations/-/clusters") {
		return reply(map[string]any{"clusters": []any{f.gke[f.cluster.Identity.NativeID]}})
	}
	if strings.HasSuffix(r.URL.Path, "/nodePools") {
		return reply(map[string]any{"nodePools": []any{f.gke[f.pool.Identity.NativeID]}})
	}
	if strings.Contains(r.URL.Path, "/operations/") {
		f.polls++
		status := "RUNNING"
		if f.polls >= 2 {
			status = "DONE"
			delete(f.gke, f.pool.Identity.NativeID)
		}
		if f.polls >= 3 {
			for id := range f.resources {
				// PV and unattached reservation must survive node pool cleanup.
				if id != f.value("data").Identity.NativeID && id != f.value("ip").Identity.NativeID {
					delete(f.resources, id)
				}
			}
		}
		return reply(map[string]any{"name": "operation-gke", "status": status, "location": "us-central1-a"})
	}
	if r.Method == "DELETE" {
		f.deletes++
		if !strings.HasSuffix(r.URL.Path, "/nodePools/workers") {
			f.t.Fatalf("unexpected GKE deletion %s", r.URL)
		}
		return reply(map[string]any{"name": "operation-gke", "status": "PENDING", "location": "us-central1-a"})
	}
	id := "//container.googleapis.com/" + strings.TrimPrefix(r.URL.Path, "/v1/")
	if data := f.gke[id]; data != nil {
		return reply(data)
	}
	return apiResponse(r, 404, `{}`), nil
}

func (f *gkeFixture) driver() *action {
	return protocolAction(f.t, nodePoolType, strings.TrimPrefix(f.pool.Identity.NativeID, "//container.googleapis.com/"), f.roundTrip)
}

func (f *gkeFixture) plan(root string, retain ...string) (plan.Result, governance.Contribution) {
	f.t.Helper()
	contribution, err := (&computeGroups{client: f.driver().client}).Contribute(context.Background(), "scope", f.assets)
	if err != nil {
		f.t.Fatal(err)
	}
	result, err := plan.Solve(plan.Input{Assets: f.assets, ResolvedAssetIDs: []asset.AssetID{asset.AssetID(root)}, LifecycleBindings: contribution.Bindings, Relationships: contribution.Relationships, RequestOptions: map[asset.AssetID]map[string]any{asset.AssetID(root): {"retain_resources": retain}}})
	if err != nil {
		f.t.Fatal(err)
	}
	return result, contribution
}

func (f *gkeFixture) request() contracts.ActionRequest {
	f.t.Helper()
	result, _ := f.plan("pool")
	if len(result.Blockers) != 0 {
		f.t.Fatalf("node pool blocked: %+v", result.Blockers)
	}
	request := contracts.ActionRequest{Asset: f.pool, Action: "delete", IdempotencyKey: "gke-cleanup"}
	for _, impact := range result.ImpactItems {
		request.LifecycleImpacts = append(request.LifecycleImpacts, contracts.ActionImpact{Asset: f.value(string(impact.AssetID)), ControllerID: impact.ControllerID, Delete: impact.Expected == plan.ExpectedDelegatedDelete})
	}
	return request
}

func TestGKEProductDiscoveryIncludesNodePoolsAndClusterIdentity(t *testing.T) {
	f := newGKEFixture(t)
	r := protocolRuntime(t, f.roundTrip)
	page, err := r.List(context.Background(), productRequest(r, nodePoolType, "us-central1"))
	if err != nil || !page.Complete || len(page.Items) != 1 {
		t.Fatalf("node pool inventory=%+v err=%v", page, err)
	}
	pool := page.Items[0]
	if pool.NativeID != f.pool.Identity.NativeID || pool.Normalized["_gke_cluster_uid"] != "cluster-uid" || len(pool.NetworkReferences) != 1 || pool.NetworkReferences[0] != f.cluster.Identity.NativeID {
		t.Fatalf("node pool has no authoritative parent: %+v", pool)
	}
	f.fault = func(req *http.Request) (*http.Response, bool) {
		if strings.HasSuffix(req.URL.Path, "/clusters") {
			return apiResponse(req, 200, `{"missingZones":["us-central1-a"]}`), true
		}
		return nil, false
	}
	if _, err := r.List(context.Background(), productRequest(r, nodePoolType, "us-central1")); err == nil {
		t.Fatal("partial parent inventory falsely authorized node pool absence")
	}
}

func TestGKEPlanUsesNativePoolControllerAndRetainsPersistentVolume(t *testing.T) {
	f := newGKEFixture(t)
	result, contribution := f.plan("pool")
	if len(result.Blockers) != 0 || len(result.Steps) != 1 || result.Steps[0].AssetID != "pool" || len(result.ImpactItems) != 7 {
		t.Fatalf("node pool plan=%+v", result)
	}
	if len(contribution.Bindings) != 8 || len(contribution.Unresolved) != 0 {
		t.Fatalf("duplicate/missing GKE ownership: %+v", contribution)
	}
	for _, impact := range result.ImpactItems {
		if impact.AssetID == "data" && impact.Expected != plan.ExpectedProviderDefaultRetain {
			t.Fatalf("persistent volume scheduled for deletion: %+v", impact)
		}
		if impact.AssetID != "data" && impact.Expected != plan.ExpectedDelegatedDelete {
			t.Fatalf("node component not delegated: %+v", impact)
		}
	}
	for _, retained := range []string{"vm", "boot", "mig", "ig", f.value("other-disk").Identity.NativeID} {
		blocked, _ := f.plan("pool", retained)
		if len(blocked.Blockers) == 0 {
			t.Fatalf("unsupported node pool retention accepted for %s", retained)
		}
	}
	cluster, _ := f.plan("cluster")
	if len(cluster.Blockers) != 0 || len(cluster.Steps) != 1 || len(cluster.ImpactItems) != 8 {
		t.Fatalf("cluster must retain node controllers until workload finalizers finish: %+v", cluster)
	}
	retained, _ := f.plan("cluster", "pool")
	if len(retained.Blockers) == 0 {
		t.Fatal("cluster plan ignored unsupported direct node pool retention")
	}
}

func TestGKENodePoolWaitsForNativeOperationAndEveryDeletedMember(t *testing.T) {
	f := newGKEFixture(t)
	request := f.request()
	result, err := f.driver().Execute(context.Background(), request)
	if err != nil || result.ProviderRequestID != "request-123" || result.ProviderOperationID != "https://container.googleapis.com/v1/projects/sample-project/locations/us-central1-a/operations/operation-gke" {
		t.Fatalf("GKE execution=%+v err=%v", result, err)
	}
	for poll := 1; poll <= 3; poll++ {
		// Persist native operation state and resume with a new driver each time.
		raw, _ := json.Marshal(result)
		var restored contracts.ActionResult
		_ = json.Unmarshal(raw, &restored)
		wait, err := f.driver().Wait(context.Background(), request, restored)
		if err != nil || wait.Done != (poll == 3) {
			t.Fatalf("poll %d prematurely closes descendants: %+v err=%v", poll, wait, err)
		}
		if poll == 2 {
			check, err := f.driver().Preflight(context.Background(), request)
			if err != nil || !check.Allowed || check.Absent {
				t.Fatalf("root 404 discarded pending members: %+v err=%v", check, err)
			}
		}
	}
	if f.deletes != 1 || f.resources[f.value("data").Identity.NativeID] == nil || f.resources[f.value("ip").Identity.NativeID] == nil {
		t.Fatal("GKE deletion lost retained/unattached resources or repeated mutation")
	}
}

func TestGKENodePoolRejectsLiveDriftProtectionAndForgedImpacts(t *testing.T) {
	cases := map[string]func(*gkeFixture, *contracts.ActionRequest){
		"new cluster": func(f *gkeFixture, _ *contracts.ActionRequest) {
			f.gke[f.cluster.Identity.NativeID]["id"] = "new-cluster"
		},
		"pool changed": func(f *gkeFixture, _ *contracts.ActionRequest) { f.gke[f.pool.Identity.NativeID]["etag"] = "new-pool" },
		"new group":    func(f *gkeFixture, _ *contracts.ActionRequest) { f.live("mig")["id"] = "new-group" },
		"new disk":     func(f *gkeFixture, _ *contracts.ActionRequest) { f.live("data")["id"] = "new-disk" },
		"protected VM": func(f *gkeFixture, _ *contracts.ActionRequest) { f.live("vm")["deletionProtection"] = true },
		"protected disk": func(f *gkeFixture, _ *contracts.ActionRequest) {
			f.live("boot")["labels"] = map[string]any{"steward-protected": "true"}
		},
		"protected pool": func(f *gkeFixture, _ *contracts.ActionRequest) {
			f.gke[f.pool.Identity.NativeID]["config"] = map[string]any{"resourceLabels": map[string]any{"steward_protected": "true"}}
		},
		"autopilot cluster": func(f *gkeFixture, _ *contracts.ActionRequest) {
			f.gke[f.cluster.Identity.NativeID]["autopilot"] = map[string]any{"enabled": true}
		},
		"autopilot pool": func(f *gkeFixture, _ *contracts.ActionRequest) {
			f.gke[f.pool.Identity.NativeID]["autopilotConfig"] = map[string]any{"enabled": true}
		},
		"upgrading": func(f *gkeFixture, _ *contracts.ActionRequest) {
			f.gke[f.pool.Identity.NativeID]["status"] = "RECONCILING"
		},
		"missing group":       func(f *gkeFixture, _ *contracts.ActionRequest) { delete(f.resources, f.value("mig").Identity.NativeID) },
		"missing plan member": func(_ *gkeFixture, r *contracts.ActionRequest) { r.LifecycleImpacts = r.LifecycleImpacts[1:] },
		"retain boot": func(_ *gkeFixture, r *contracts.ActionRequest) {
			for i := range r.LifecycleImpacts {
				if r.LifecycleImpacts[i].Asset.ID == "boot" {
					r.LifecycleImpacts[i].Delete = false
				}
			}
		},
		"delete PV": func(_ *gkeFixture, r *contracts.ActionRequest) {
			for i := range r.LifecycleImpacts {
				if r.LifecycleImpacts[i].Asset.ID == "data" {
					r.LifecycleImpacts[i].Delete = true
				}
			}
		},
		"foreign parent": func(_ *gkeFixture, r *contracts.ActionRequest) { r.LifecycleImpacts[0].ControllerID = "foreign" },
		"foreign connection": func(_ *gkeFixture, r *contracts.ActionRequest) {
			r.LifecycleImpacts[0].Asset.Identity.ConnectionID = "foreign"
		},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			f := newGKEFixture(t)
			request := f.request()
			change(f, &request)
			check, err := f.driver().Preflight(context.Background(), request)
			if err == nil && check.Allowed {
				t.Fatalf("unsafe GKE preflight accepted: %+v", check)
			}
			if f.deletes != 0 {
				t.Fatal("preflight mutated GKE")
			}
		})
	}
}

func TestGKEOperationIdentityAndFailures(t *testing.T) {
	for _, bad := range []map[string]any{
		{"name": "../escape"}, {"name": "operation-gke", "location": "europe-west1"},
		{"name": "operation-gke", "targetLink": "https://container.googleapis.com/v1/projects/foreign/locations/us-central1-a/clusters/test/nodePools/workers"},
		{"name": "operation-gke", "selfLink": "https://foreign.example/operation-gke"},
	} {
		f := newGKEFixture(t)
		if _, err := f.driver().gkeOperationURL(bad); err == nil {
			t.Fatalf("accepted forged operation %+v", bad)
		}
	}
	for _, body := range []string{`{"name":"other-operation","status":"DONE"}`, `{"name":"operation-gke","status":"DONE","error":{"code":13,"message":"private-error"}}`, `{"name":"operation-gke","status":"DONE","statusMessage":"private-error"}`} {
		t.Run(fmt.Sprint(body), func(t *testing.T) {
			f := newGKEFixture(t)
			request := f.request()
			result, err := f.driver().Execute(context.Background(), request)
			if err != nil {
				t.Fatal(err)
			}
			f.fault = func(r *http.Request) (*http.Response, bool) {
				if strings.Contains(r.URL.Path, "/operations/") {
					return apiResponse(r, 200, body), true
				}
				return nil, false
			}
			if _, err := f.driver().Wait(context.Background(), request, result); err == nil || strings.Contains(err.Error(), "private-error") {
				t.Fatalf("GKE error lost or exposed: %v", err)
			}
		})
	}
}

func TestGKEAutopilotOwnershipCannotDeleteIndividualPool(t *testing.T) {
	f := newGKEFixture(t)
	f.gke[f.cluster.Identity.NativeID]["autopilot"] = map[string]any{"enabled": true}
	result, contribution := f.plan("pool")
	if len(result.Blockers) == 0 {
		t.Fatal("Autopilot pool got an independent delete step")
	}
	for _, binding := range contribution.Bindings {
		if binding.ManagedAssetID == "pool" && binding.CleanupPolicy != graph.CleanupDelegate {
			t.Fatal("Autopilot pool bypasses cluster")
		}
	}
}

func TestGKEManagedGroupCannotBypassNodePoolController(t *testing.T) {
	f := newGKEFixture(t)
	driver := protocolAction(t, managerType, "projects/sample-project/"+f.groupPath, f.roundTrip)
	check, err := driver.Preflight(context.Background(), contracts.ActionRequest{Asset: f.value("mig"), Action: "delete"})
	if err != nil || check.Allowed || check.Reason != "managed_group_requires_gke_cleanup" {
		t.Fatalf("GKE owned MIG bypassed its native controller: %+v %v", check, err)
	}
	f.fault = func(r *http.Request) (*http.Response, bool) {
		if r.URL.Host == "container.googleapis.com" {
			return apiResponse(r, 403, `{}`), true
		}
		return nil, false
	}
	if _, err := driver.Preflight(context.Background(), contracts.ActionRequest{Asset: f.value("mig"), Action: "delete"}); err == nil {
		t.Fatal("unavailable GKE ownership lookup authorized direct MIG deletion")
	}
}

func TestGKEBootDiskPolicyOverridesComputeAutoDelete(t *testing.T) {
	f := newGKEFixture(t)
	boot := object(array(f.live("vm")["disks"])[0])
	boot["boot"], boot["autoDelete"] = true, false
	result, _ := f.plan("pool")
	for _, impact := range result.ImpactItems {
		if impact.AssetID == "boot" && impact.Expected != plan.ExpectedDelegatedDelete {
			t.Fatal("GKE boot disk incorrectly treated as a retained persistent volume")
		}
	}
}
