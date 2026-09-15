package azure

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/app/cleanup"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/internal/persistence/sqlite"
	"github.com/loomx-ai/steward/internal/provider/contracts"
	providerruntime "github.com/loomx-ai/steward/internal/provider/runtime"
)

type netappPolicyFixture struct {
	*netappPoolFixture
	policy            string
	patches           map[string]int
	policyDeletes     int
	failAfterMutation bool
}

func newNetappPolicyFixture(t *testing.T) *netappPolicyFixture {
	base := newNetappPoolFixture(t)
	f := &netappPolicyFixture{netappPoolFixture: base, policy: redisParentID(base.id) + "/snapshotpolicies/item", patches: map[string]int{}}
	for _, id := range f.volumes {
		object(f.objects[id]["properties"])["dataProtection"] = map[string]any{"snapshot": map[string]any{"snapshotPolicyId": f.policy}, "backup": map[string]any{"policyEnforced": false}}
	}
	previous := f.override
	f.override = func(q *http.Request) (*http.Response, bool) {
		id := strings.ToLower(q.URL.Path)
		if q.Method == "GET" && id == f.policy+"/volumes" {
			rows := []any{}
			for _, id := range f.volumes {
				assignments, _ := netappAssignments(f.objects[id])
				if assignments[netappSnapshotPolicyType] == f.policy {
					rows = append(rows, f.objects[id])
				}
			}
			return jsonResponse(200, map[string]any{"value": rows}, nil), true
		}
		if q.Method == "PATCH" {
			found := false
			for _, v := range f.volumes {
				found = found || v == id
			}
			if !found || q.URL.Query().Get("api-version") != netappVersion || len(q.URL.Query()) != 1 {
				t.Fatal("unreviewed volume patch", q.URL)
			}
			body, err := io.ReadAll(q.Body)
			if err != nil {
				t.Fatal(err)
			}
			var value map[string]any
			if json.Unmarshal(body, &value) != nil {
				t.Fatal("invalid patch")
			}
			expected := map[string]any{"properties": map[string]any{"dataProtection": map[string]any{"snapshot": map[string]any{"snapshotPolicyId": ""}}}}
			got, _ := json.Marshal(value)
			want, _ := json.Marshal(expected)
			if string(got) != string(want) {
				t.Fatal("patch changed unrelated volume settings", string(got))
			}
			f.patches[id]++
			if f.failAfterMutation {
				f.readFault[id] = 503
			}
			bodyValue := batchClone(f.objects[id])
			object(bodyValue["properties"])["provisioningState"] = "Updating"
			return jsonResponse(200, bodyValue, nil), true
		}
		if q.Method == "DELETE" && id == f.policy {
			for _, volume := range f.volumes {
				assignments, _ := netappAssignments(f.objects[volume])
				if assignments[netappSnapshotPolicyType] != "" {
					t.Fatal("policy delete before unassignment")
				}
			}
			if len(q.URL.Query()) != 1 || q.ContentLength != 0 {
				t.Fatal("unreviewed policy delete")
			}
			f.policyDeletes++
			if f.failAfterMutation {
				f.readFault[id] = 503
			}
			return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(""))}, true
		}
		return previous(q)
	}
	return f
}
func (f *netappPolicyFixture) finishPatch(id string) {
	p := object(f.objects[id]["properties"])
	object(object(p["dataProtection"])["snapshot"])["snapshotPolicyId"] = ""
	p["provisioningState"] = "Succeeded"
	f.objects[id]["etag"] = "updated"
	f.objects[f.policy]["etag"] = "membership-changed"
	delete(f.readFault, id)
}
func (f *netappPolicyFixture) request(t *testing.T) contracts.ActionRequest {
	values := f.assets(t)
	req := contracts.ActionRequest{Action: "delete", IdempotencyKey: "snapshot-policy"}
	for _, v := range values {
		if v.Identity.NativeID == f.policy {
			req.Asset = v
		}
	}
	for _, v := range values {
		if v.Identity.NativeType == netappVolumeType {
			req.LifecycleImpacts = append(req.LifecycleImpacts, contracts.ActionImpact{Asset: v, ControllerID: req.Asset.ID, Delete: false})
		}
	}
	for _, v := range values {
		if v.Identity.NativeType != netappSnapshotType && v.Identity.NativeType != netappSubvolumeType && v.Identity.NativeType != netappQuotaType {
			continue
		}
		for _, parent := range values {
			if parent.Identity.NativeType == netappVolumeType && redisParentID(v.Identity.NativeID) == parent.Identity.NativeID {
				req.LifecycleImpacts = append(req.LifecycleImpacts, contracts.ActionImpact{Asset: v, ControllerID: parent.ID, Delete: false})
			}
		}
	}
	return req
}
func TestNetappSnapshotPolicyPlanPreservesVolumes(t *testing.T) {
	testNetappPolicyPlanPreservesVolumes(t, newNetappPolicyFixture(t), plan.WarningNetappSnapshotPolicyDelete)
}
func testNetappPolicyPlanPreservesVolumes(t *testing.T, f *netappPolicyFixture, warningCode plan.WarningCode) {
	repo, registry, _ := azureNativeWorkerRepository(t, f.runtime)
	kinds := []string{}
	for _, k := range netappResources {
		kinds = append(kinds, k.kind)
	}
	values := azureNativeWorkerScan(t, f.runtime, netappSource, repo, registry, kinds, false, true)
	var policy asset.Asset
	for _, v := range values {
		if v.Identity.NativeID == f.policy {
			policy = v
		}
	}
	task, err := cleanup.NewService(repo, registry).CreateTask(t.Context(), cleanup.CreateTaskRequest{ConnectionID: "connection", CreatedBy: "operator", Selectors: []plan.CleanupSelector{{Kind: plan.SelectorAsset, AssetID: policy.ID}}})
	if err != nil || task.Task.Status != plan.StatusReady || len(task.Steps) != 1 || len(task.ImpactItems) != 8 {
		t.Fatal("policy retention plan", task.Task.Status, len(task.Steps), len(task.ImpactItems), task.Task.Blockers, err)
	}
	warned := false
	for _, warning := range task.Task.Warnings {
		if warning.Code == warningCode && warning.AssetID == policy.ID {
			warned = true
		}
	}
	if !warned {
		t.Fatal("missing snapshot policy consequence warning")
	}
	var volume asset.Asset
	for _, v := range values {
		if v.Identity.NativeID == f.volumes[0] {
			volume = v
		}
	}
	independent, err := cleanup.NewService(repo, registry).CreateTask(t.Context(), cleanup.CreateTaskRequest{ConnectionID: "connection", CreatedBy: "operator", Selectors: []plan.CleanupSelector{{Kind: plan.SelectorAsset, AssetID: volume.ID}}})
	if err != nil || independent.Task.Status != plan.StatusReady || len(independent.Steps) != 1 || independent.Steps[0].AssetID != volume.ID || len(independent.ImpactItems) != 3 {
		t.Fatal("independent volume plan changed", independent.Task.Status, len(independent.Steps), len(independent.ImpactItems), err)
	}
	for _, impact := range task.ImpactItems {
		if impact.Expected != plan.ExpectedRetainShared {
			t.Fatal("volume deletion substituted for unassignment", impact)
		}
	}
}
func TestNetappSnapshotPolicyDurableUnassignment(t *testing.T) {
	f := newNetappPolicyFixture(t)
	req := f.request(t)
	driver, err := f.runtime.ResolveAction(t.Context(), "connection", req.Asset)
	if err != nil {
		t.Fatal(err)
	}
	result, err := driver.Execute(t.Context(), req)
	if err != nil || len(f.patches) != 0 || f.policyDeletes != 0 {
		t.Fatal("preparation", result, err)
	}
	checkpoint := func() {
		wire, _ := json.Marshal(result)
		if json.Unmarshal(wire, &result) != nil {
			t.Fatal("receipt JSON")
		}
		fresh, err := NewRuntime(f.runtime.credentials)
		if err != nil {
			t.Fatal(err)
		}
		fresh.transport = f.runtime.transport
		driver, err = fresh.ResolveAction(t.Context(), "connection", req.Asset)
		if err != nil {
			t.Fatal(err)
		}
		req.ExecutionResult = &result
	}
	wait := func(wantError bool) contracts.WaitResult {
		t.Helper()
		checkpoint()
		out, err := driver.Wait(t.Context(), req, result)
		if (err != nil) != wantError {
			t.Fatal("wait", out, err)
		}
		if err == nil {
			result.Data = out.Data
		}
		return out
	}
	f.failAfterMutation = true
	for _, id := range f.volumes {
		wait(false)
		if f.patches[id] != 1 || result.Data["operation"] == nil {
			t.Fatal("update ack not checkpointed", f.patches, result)
		}
		wait(true)
		if f.patches[id] != 1 {
			t.Fatal("update repeated")
		}
		f.finishPatch(id)
		wait(false)
	}
	wait(false)
	if f.policyDeletes != 1 || result.Data["phase"] != "delete" {
		t.Fatal("policy acknowledgement not saved", result)
	}
	wait(false) // synchronous DELETE receipt completion
	wait(true)  // missing own read must not close the policy
	delete(f.readFault, f.policy)
	if out := wait(false); out.Done {
		t.Fatal("live policy considered absent")
	}
	f.missing[f.policy] = true
	if out := wait(false); !out.Done {
		t.Fatal("policy own absence not recognized", out)
	}
	if f.policyDeletes != 1 {
		t.Fatal("delete replayed")
	}
	for _, id := range f.volumes {
		if f.missing[id] || f.patches[id] != 1 {
			t.Fatal("volume not retained", id)
		}
	}
}
func TestNetappSnapshotPolicyWorkerRestarts(t *testing.T) {
	f := newNetappPolicyFixture(t)
	testNetappPolicyWorkerRestarts(t, f, 1, f.finishPatch)
}
func testNetappPolicyWorkerRestarts(t *testing.T, f *netappPolicyFixture, stages int, finish func(string)) {
	repo, registry, path := azureNativeWorkerRepository(t, f.runtime)
	kinds := []string{}
	for _, k := range netappResources {
		kinds = append(kinds, k.kind)
	}
	values := azureNativeWorkerScan(t, f.runtime, netappSource, repo, registry, kinds, false, true)
	var policy asset.Asset
	for _, v := range values {
		if v.Identity.NativeID == f.policy {
			policy = v
		}
	}
	planner := cleanup.NewService(repo, registry)
	task, err := planner.CreateTask(t.Context(), cleanup.CreateTaskRequest{ConnectionID: "connection", CreatedBy: "operator", Selectors: []plan.CleanupSelector{{Kind: plan.SelectorAsset, AssetID: policy.ID}}})
	if err != nil || len(task.Steps) != 1 || len(task.ImpactItems) != 8 {
		t.Fatal("worker plan", task.Task.Status, len(task.Steps), len(task.ImpactItems), task.Task.Blockers, err)
	}
	attempt, err := planner.CreateExecution(t.Context(), cleanup.CreateExecutionRequest{ConnectionID: "connection", CleanupTaskID: task.Task.ID, RequestedBy: "operator", IdempotencyKey: "policy-worker", Confirmation: cleanup.ExecutionConfirmation{Acknowledged: true}})
	if err != nil {
		t.Fatal(err)
	}
	jobs, err := repo.ListJobsByAggregate(t.Context(), "cleanup_task", string(task.Task.ID))
	if err != nil {
		t.Fatal(err)
	}
	var job execution.Job
	for _, j := range jobs {
		if text(j.Payload["cleanup_task_step_id"]) == string(task.Steps[0].ID) {
			job = j
		}
	}
	run := func(pending bool) execution.ActionAttempt {
		t.Helper()
		repo, err = sqlite.Open(path, "../../migrations")
		if err != nil {
			t.Fatal(err)
		}
		fresh, err := NewRuntime(f.runtime.credentials)
		if err != nil {
			t.Fatal(err)
		}
		fresh.transport = f.runtime.transport
		registry = providerruntime.NewRegistry()
		if err = registry.Register(fresh); err != nil {
			t.Fatal(err)
		}
		if err = registry.RegisterBundle(fresh.Bundle()); err != nil {
			t.Fatal(err)
		}
		worker := cleanup.NewExecutionHandler(cleanup.NewService(repo, registry), cleanup.ActionResolverFunc(func(ctx context.Context, v asset.Asset) (cleanup.ActionDriver, error) {
			return registry.ResolveAction(ctx, v.Identity.ConnectionID, v)
		}))
		err = worker.Handle(t.Context(), job)
		var retry *cleanup.RetryError
		if pending && !errors.As(err, &retry) || !pending && err != nil {
			action, _ := repo.Executions().GetActionByExecutionStep(t.Context(), attempt.ID, string(task.Steps[0].ID))
			t.Fatal("worker", err, action.Status, action.ProviderError)
		}
		action, err := repo.Executions().GetActionByExecutionStep(t.Context(), attempt.ID, string(task.Steps[0].ID))
		if err != nil {
			t.Fatal(err)
		}
		return action
	}
	run(true) // saves preparation before any mutation
	f.failAfterMutation = true
	for _, id := range f.volumes {
		for stage := 0; stage < stages; stage++ {
			run(true)
			run(true)
			if f.patches[id] != stage+1 {
				t.Fatal("worker repeated update", f.patches)
			}
			finish(id)
			run(true)
		}
	}
	run(true)
	if f.policyDeletes != 1 {
		t.Fatal("missing policy DELETE")
	}
	run(true)
	run(true)
	delete(f.readFault, f.policy)
	f.missing[f.policy] = true
	run(false)
	after, err := repo.ListActiveAssetsByConnection(t.Context(), "connection", "")
	if err != nil || len(after) != len(values)-1 {
		t.Fatal("retained scope", len(after), len(values), err)
	}
	for _, v := range after {
		if v.Identity.NativeID == f.policy {
			t.Fatal("policy not closed")
		}
	}
	if f.policyDeletes != 1 {
		t.Fatal("worker repeated DELETE")
	}
}

func TestNetappSnapshotPolicyChangesBlockMutation(t *testing.T) {
	for _, fault := range []string{"missing volume", "missing retained child", "delete volume", "delete child", "parameter", "proof", "new consumer", "volume uuid", "pool uuid", "volume protected", "policy protected", "parent changed", "backup changed", "other policy", "missing parent", "volume absent"} {
		t.Run(fault, func(t *testing.T) {
			f := newNetappPolicyFixture(t)
			req := f.request(t)
			driver, err := f.runtime.ResolveAction(t.Context(), "connection", req.Asset)
			if err != nil {
				t.Fatal(err)
			}
			volume := f.volumes[0]
			p := object(f.objects[volume]["properties"])
			switch fault {
			case "missing volume":
				req.LifecycleImpacts = req.LifecycleImpacts[1:]
			case "missing retained child":
				req.LifecycleImpacts = req.LifecycleImpacts[:len(req.LifecycleImpacts)-1]
			case "delete volume":
				req.LifecycleImpacts[0].Delete = true
			case "delete child":
				req.LifecycleImpacts[len(req.LifecycleImpacts)-1].Delete = true
			case "parameter":
				req.Parameters = map[string]any{"force": true}
			case "proof":
				req.Asset.Normalized[netappAssignmentProof] = "forged"
			case "new consumer":
				v := batchClone(f.objects[volume])
				id := redisParentID(volume) + "/volumes/new"
				v["id"] = id
				v["name"] = "new"
				f.objects[id] = v
				f.volumes = append(f.volumes, id)
			case "volume uuid":
				p["fileSystemId"] = testTenant
			case "pool uuid":
				object(f.objects[redisParentID(volume)]["properties"])["poolId"] = testTenant
			case "volume protected":
				f.objects[volume]["tags"] = map[string]any{"steward:protected": "true"}
			case "policy protected":
				f.objects[f.policy]["tags"] = map[string]any{"steward:protected": "true"}
			case "parent changed":
				object(f.objects[redisParentID(f.policy)]["properties"])["unknown"] = true
			case "backup changed":
				object(object(p["dataProtection"])["backup"])["policyEnforced"] = true
			case "other policy":
				object(object(p["dataProtection"])["snapshot"])["snapshotPolicyId"] = f.policy + "2"
			case "missing parent":
				f.missing[redisParentID(f.policy)] = true
			case "volume absent":
				f.missing[volume] = true
			}
			if _, err := driver.Execute(t.Context(), req); err == nil || len(f.patches) != 0 || f.policyDeletes != 0 {
				t.Fatal("changed scope mutated", fault, err)
			}
		})
	}
}

func TestNetappSnapshotPolicyWithoutAssignments(t *testing.T) {
	f := newNetappPolicyFixture(t)
	for _, id := range f.volumes {
		f.finishPatch(id)
	}
	req := f.request(t)
	req.LifecycleImpacts = nil
	driver, err := f.runtime.ResolveAction(t.Context(), "connection", req.Asset)
	if err != nil {
		t.Fatal(err)
	}
	result, err := driver.Execute(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	req.ExecutionResult = &result
	out, err := driver.Wait(t.Context(), req, result)
	if err != nil || f.policyDeletes != 1 || len(f.patches) != 0 {
		t.Fatal("empty policy delete", out, err)
	}
	result.Data = out.Data
	f.missing[f.policy] = true
	out, err = driver.Wait(t.Context(), req, result)
	if err != nil {
		t.Fatal(err)
	}
	result.Data = out.Data
	out, err = driver.Wait(t.Context(), req, result)
	if err != nil || !out.Done {
		t.Fatal("empty policy completion", out, err)
	}
}
