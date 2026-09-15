package azure

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
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

type netappVaultFixture struct {
	*netappBackupPolicyFixture
	vault         string
	backups       []string
	backupDeletes map[string]int
	vaultDeletes  int
}

func newNetappVaultFixture(t *testing.T) *netappVaultFixture {
	base := newNetappBackupPolicyFixture(t)
	vault := redisParentID(base.id) + "/backupvaults/item"
	first := vault + "/backups/item"
	second := vault + "/backups/second"
	f := &netappVaultFixture{netappBackupPolicyFixture: base, vault: vault, backups: []string{first, second}, backupDeletes: map[string]int{}}
	raw := batchClone(f.objects[first])
	raw["id"], raw["name"] = second, "second"
	object(raw["properties"])["backupId"] = testApplication
	object(raw["properties"])["volumeResourceId"] = f.volumes[1]
	f.objects[second] = raw
	previous := f.override
	f.override = func(q *http.Request) (*http.Response, bool) {
		id := strings.ToLower(q.URL.Path)
		if q.Method == "PATCH" {
			wire, err := io.ReadAll(q.Body)
			if err != nil {
				t.Fatal(err)
			}
			q.Body = io.NopCloser(strings.NewReader(string(wire)))
			var body map[string]any
			if json.Unmarshal(wire, &body) != nil {
				t.Fatal("PATCH body")
			}
			backup := object(object(object(body["properties"])["dataProtection"])["backup"])
			if _, ok := backup["backupVaultId"]; ok {
				want := `{"properties":{"dataProtection":{"backup":{"backupVaultId":""}}}}`
				canonical, _ := json.Marshal(body)
				if string(canonical) != want || !slices.Contains(f.volumes, id) || len(q.URL.Query()) != 1 {
					t.Fatal("unreviewed vault PATCH", string(canonical))
				}
				for _, child := range f.backups {
					if !f.missing[child] {
						t.Fatal("vault detached before backups removed")
					}
				}
				assignments, _ := netappAssignments(f.objects[id])
				if assignments[netappBackupPolicyType] != "" {
					t.Fatal("vault detached before policy")
				}
				if f.pending[id] != "" {
					t.Fatal("replayed vault update")
				}
				f.pending[id] = "backupVaultId"
				f.patches[id]++
				if f.failAfterMutation {
					f.readFault[id] = 503
				}
				updated := batchClone(f.objects[id])
				object(updated["properties"])["provisioningState"] = "Updating"
				return jsonResponse(200, updated, nil), true
			}
		}
		if q.Method == "DELETE" && (id == vault || slices.Contains(f.backups, id)) {
			if len(q.URL.Query()) != 1 || q.URL.Query().Get("api-version") != netappVersion || q.ContentLength != 0 {
				t.Fatal("unreviewed vault deletion")
			}
			for _, volume := range f.volumes {
				assignments, _ := netappAssignments(f.objects[volume])
				if assignments[netappBackupPolicyType] != "" {
					t.Fatal("backup deletion before policy unassignment")
				}
				if id == vault && assignments[netappVaultType] != "" {
					t.Fatal("vault deletion before unassignment")
				}
			}
			if id == vault {
				for _, child := range f.backups {
					if !f.missing[child] {
						t.Fatal("vault deletion before backup absence")
					}
				}
				f.vaultDeletes++
			} else {
				f.backupDeletes[id]++
			}
			if f.failAfterMutation {
				f.readFault[id] = 503
			}
			return &http.Response{StatusCode: 204, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(""))}, true
		}
		return previous(q)
	}
	return f
}

func (f *netappVaultFixture) finishPatch(id string) {
	if f.pending[id] != "backupVaultId" {
		f.netappBackupPolicyFixture.finishPatch(id)
		return
	}
	p := object(f.objects[id]["properties"])
	object(object(p["dataProtection"])["backup"])["backupVaultId"] = ""
	p["provisioningState"] = "Succeeded"
	f.objects[id]["etag"] = "vault-detached"
	delete(f.pending, id)
	delete(f.readFault, id)
}

func (f *netappVaultFixture) request(t *testing.T) contracts.ActionRequest {
	values := f.assets(t)
	req := contracts.ActionRequest{Action: "delete", IdempotencyKey: "vault"}
	for _, v := range values {
		if v.Identity.NativeID == f.vault {
			req.Asset = v
		}
	}
	for _, v := range values {
		if slices.Contains(f.volumes, v.Identity.NativeID) {
			req.LifecycleImpacts = append(req.LifecycleImpacts, contracts.ActionImpact{Asset: v, ControllerID: req.Asset.ID, Delete: false})
		}
		if slices.Contains(f.backups, v.Identity.NativeID) {
			req.LifecycleImpacts = append(req.LifecycleImpacts, contracts.ActionImpact{Asset: v, ControllerID: req.Asset.ID, Delete: true})
		}
		if v.Identity.NativeType == netappSnapshotType || v.Identity.NativeType == netappSubvolumeType || v.Identity.NativeType == netappQuotaType {
			for _, parent := range values {
				if slices.Contains(f.volumes, parent.Identity.NativeID) && redisParentID(v.Identity.NativeID) == parent.Identity.NativeID {
					req.LifecycleImpacts = append(req.LifecycleImpacts, contracts.ActionImpact{Asset: v, ControllerID: parent.ID, Delete: false})
				}
			}
		}
	}
	return req
}

func TestNetappVaultWorkerRestartsAndRetainsVolumes(t *testing.T) {
	f := newNetappVaultFixture(t)
	repo, registry, path := azureNativeWorkerRepository(t, f.runtime)
	kinds := []string{}
	for _, k := range netappResources {
		kinds = append(kinds, k.kind)
	}
	values := azureNativeWorkerScan(t, f.runtime, netappSource, repo, registry, kinds, false, true)
	var vault asset.Asset
	for _, v := range values {
		if v.Identity.NativeID == f.vault {
			vault = v
		}
	}
	planner := cleanup.NewService(repo, registry)
	task, err := planner.CreateTask(t.Context(), cleanup.CreateTaskRequest{ConnectionID: "connection", CreatedBy: "operator", Selectors: []plan.CleanupSelector{{Kind: plan.SelectorAsset, AssetID: vault.ID}}})
	if err != nil || task.Task.Status != plan.StatusReady || len(task.Steps) != 1 || len(task.ImpactItems) != 10 {
		t.Fatal("vault plan", task.Task.Status, len(task.Steps), len(task.ImpactItems), task.Task.Blockers, err)
	}
	retained, deleted := 0, 0
	for _, impact := range task.ImpactItems {
		if impact.Expected == plan.ExpectedRetainShared {
			retained++
		}
		if impact.Expected == plan.ExpectedDelegatedDelete {
			deleted++
		}
	}
	if retained != 8 || deleted != 2 {
		t.Fatal("vault plan deletion boundary", retained, deleted)
	}
	warned := false
	for _, w := range task.Task.Warnings {
		if w.Code == plan.WarningNetappVaultDelete {
			warned = true
		}
	}
	if !warned {
		t.Fatal("missing backup-loss warning")
	}
	attempt, err := planner.CreateExecution(t.Context(), cleanup.CreateExecutionRequest{ConnectionID: "connection", CleanupTaskID: task.Task.ID, RequestedBy: "operator", IdempotencyKey: "vault-worker", Confirmation: cleanup.ExecutionConfirmation{Acknowledged: true}})
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
		if err := registry.Register(fresh); err != nil {
			t.Fatal(err)
		}
		if err := registry.RegisterBundle(fresh.Bundle()); err != nil {
			t.Fatal(err)
		}
		worker := cleanup.NewExecutionHandler(cleanup.NewService(repo, registry), cleanup.ActionResolverFunc(func(ctx context.Context, v asset.Asset) (cleanup.ActionDriver, error) {
			return registry.ResolveAction(ctx, v.Identity.ConnectionID, v)
		}))
		err = worker.Handle(t.Context(), job)
		var retry *cleanup.RetryError
		action, readErr := repo.Executions().GetActionByExecutionStep(t.Context(), attempt.ID, string(task.Steps[0].ID))
		if pending && !errors.As(err, &retry) || !pending && err != nil || readErr != nil {
			t.Fatal("vault worker", err, readErr, action.Status, action.ProviderError)
		}
		return action
	}
	run(true)
	f.failAfterMutation = true
	for _, id := range f.volumes {
		for stage := 0; stage < 2; stage++ {
			run(true)
			run(true)
			if f.patches[id] != stage+1 {
				t.Fatal("update replay", f.patches)
			}
			f.finishPatch(id)
			run(true)
		}
	}
	run(true) // policies -> backups
	for _, id := range f.backups {
		run(true)
		if f.backupDeletes[id] != 1 {
			t.Fatal("missing backup delete")
		}
		run(true)
		run(true)
		delete(f.readFault, id)
		f.missing[id] = true
		run(true)
	}
	run(true) // backups -> vault unassignment
	for _, id := range f.volumes {
		run(true)
		run(true)
		if f.patches[id] != 3 {
			t.Fatal("vault PATCH replay")
		}
		f.finishPatch(id)
		run(true)
	}
	run(true)
	if f.vaultDeletes != 1 {
		t.Fatal("missing vault delete")
	}
	run(true)
	delete(f.readFault, f.vault)
	run(true)
	run(true)
	f.missing[f.vault] = true
	run(false)
	after, err := repo.ListActiveAssetsByConnection(t.Context(), "connection", "")
	if err != nil || len(after) != len(values)-3 {
		t.Fatal("vault retained scope", len(after), len(values), err)
	}
	for _, v := range after {
		if v.Identity.NativeID == f.vault || slices.Contains(f.backups, v.Identity.NativeID) {
			t.Fatal("deleted vault member left active")
		}
	}
	if f.policyDeletes != 0 || f.missing[f.policy] || f.vaultDeletes != 1 {
		t.Fatal("global policy deleted or vault replayed")
	}
}

func TestNetappVaultChangesPreventMutation(t *testing.T) {
	for _, fault := range []string{"new backup", "new volume", "policy uuid", "volume uuid", "snapshot binding", "backup protection", "vault protection", "backup unavailable", "missing backup impact", "retained backup", "deleted volume", "forged review", "receipt", "force"} {
		t.Run(fault, func(t *testing.T) {
			f := newNetappVaultFixture(t)
			req := f.request(t)
			driver, err := f.runtime.ResolveAction(t.Context(), "connection", req.Asset)
			if err != nil {
				t.Fatal(err)
			}
			result, err := driver.Execute(t.Context(), req)
			if err != nil {
				t.Fatal(err)
			}
			switch fault {
			case "new backup":
				raw := batchClone(f.objects[f.backups[0]])
				id := f.vault + "/backups/new"
				raw["id"], raw["name"] = id, "new"
				f.objects[id] = raw
			case "new volume":
				raw := batchClone(f.objects[f.volumes[0]])
				id := f.id + "/volumes/new"
				raw["id"], raw["name"] = id, "new"
				f.objects[id] = raw
			case "policy uuid":
				object(f.objects[f.policy]["properties"])["backupPolicyId"] = testApplication
			case "volume uuid":
				object(f.objects[f.volumes[0]]["properties"])["fileSystemId"] = testTenant
			case "snapshot binding":
				object(object(object(f.objects[f.volumes[0]]["properties"])["dataProtection"])["snapshot"])["snapshotPolicyId"] = ""
			case "backup protection":
				f.objects[f.backups[0]]["tags"] = map[string]any{"steward:protected": "true"}
			case "vault protection":
				f.objects[f.vault]["tags"] = map[string]any{"steward:protected": "true"}
			case "backup unavailable":
				f.readFault[f.backups[0]] = 403
			case "missing backup impact":
				for i, v := range req.LifecycleImpacts {
					if v.Asset.Identity.NativeType == netappBackupType {
						req.LifecycleImpacts = append(req.LifecycleImpacts[:i], req.LifecycleImpacts[i+1:]...)
						break
					}
				}
			case "retained backup":
				for i, v := range req.LifecycleImpacts {
					if v.Asset.Identity.NativeType == netappBackupType {
						req.LifecycleImpacts[i].Delete = false
						break
					}
				}
			case "deleted volume":
				for i, v := range req.LifecycleImpacts {
					if v.Asset.Identity.NativeType == netappVolumeType {
						req.LifecycleImpacts[i].Delete = true
						break
					}
				}
			case "forged review":
				object(req.Asset.Normalized[netappVaultReview])["members"] = map[string]any{}
			case "receipt":
				result.Data["index"] = 1
			case "force":
				req.Parameters = map[string]any{"force": true}
			}
			if _, err := driver.Wait(t.Context(), req, result); err == nil || len(f.patches) != 0 || len(f.backupDeletes) != 0 || f.vaultDeletes != 0 {
				t.Fatal("changed vault boundary mutated", fault, err)
			}
		})
	}
}

func TestNetappVaultTransferAndResumedPolicy(t *testing.T) {
	f := newNetappVaultFixture(t)
	req := f.request(t)
	driver, err := f.runtime.ResolveAction(t.Context(), "connection", req.Asset)
	if err != nil {
		t.Fatal(err)
	}
	result, err := driver.Execute(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	id := f.volumes[0]
	f.status[id] = "Transferring"
	out, err := driver.Wait(t.Context(), req, result)
	if err != nil || out.Done || len(f.patches) != 0 {
		t.Fatal("transfer not awaited", err)
	}
	result.Data = out.Data
	f.status[id] = "Unknown"
	if _, err := driver.Wait(t.Context(), req, result); err == nil {
		t.Fatal("unknown transfer authorized update")
	}
	f.status[id] = "Idle"
	out, err = driver.Wait(t.Context(), req, result)
	if err != nil {
		t.Fatal(err)
	}
	result.Data = out.Data
	out, err = driver.Wait(t.Context(), req, result)
	if err != nil || out.Data["step"] != "pause" {
		t.Fatal("callback replaced own suspension read", err)
	}
	result.Data = out.Data
	f.finishPatch(id)
	out, err = driver.Wait(t.Context(), req, result)
	if err != nil {
		t.Fatal(err)
	}
	result.Data = out.Data
	object(object(object(f.objects[id]["properties"])["dataProtection"])["backup"])["policyEnforced"] = true
	if _, err := driver.Wait(t.Context(), req, result); err == nil || f.patches[id] != 1 {
		t.Fatal("resumed policy detached")
	}
}

func TestNetappVaultDoesNotAuthorizeIndependentLatestBackup(t *testing.T) {
	f := newNetappVaultFixture(t)
	req := f.request(t)
	for _, impact := range req.LifecycleImpacts {
		if impact.Asset.Identity.NativeType == netappBackupType {
			if _, err := f.runtime.ResolveAction(t.Context(), "connection", impact.Asset); err == nil {
				t.Fatal("direct latest backup deletion authorized")
			}
		}
	}
}

func TestNetappVaultEmptyUnassignedDeletion(t *testing.T) {
	f := newNetappVaultFixture(t)
	for _, id := range f.backups {
		f.missing[id] = true
	}
	for _, id := range f.volumes {
		object(object(f.objects[id]["properties"])["dataProtection"])["backup"] = map[string]any{}
	}
	object(f.objects[f.policy]["properties"])["volumesAssigned"] = 0
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
	for range 3 {
		out, err := driver.Wait(t.Context(), req, result)
		if err != nil {
			t.Fatal(err)
		}
		result.Data = out.Data
	}
	if f.vaultDeletes != 1 || len(f.patches) != 0 || len(f.backupDeletes) != 0 {
		t.Fatal("empty vault cleanup scope")
	}
	f.missing[f.vault] = true
	out, err := driver.Wait(t.Context(), req, result)
	if err != nil || !out.Done {
		t.Fatal("vault absence", out, err)
	}
}

func TestNetappVaultAsyncWorkflow(t *testing.T) {
	for _, expired := range []bool{false, true} {
		t.Run(fmt.Sprintf("expired=%v", expired), func(t *testing.T) {
			f := newNetappVaultFixture(t)
			req := f.request(t)
			driver, err := f.runtime.ResolveAction(t.Context(), "connection", req.Asset)
			if err != nil {
				t.Fatal(err)
			}
			result, err := driver.Execute(t.Context(), req)
			if err != nil {
				t.Fatal(err)
			}
			type operation struct{ id, method string }
			ops := map[string]operation{}
			previous := f.override
			f.override = func(q *http.Request) (*http.Response, bool) {
				if q.Method == "PATCH" || q.Method == "DELETE" {
					_, ok := previous(q)
					if !ok {
						t.Fatal("unvalidated mutation")
					}
					uid := fmt.Sprintf("00000000-0000-4000-8000-%012d", len(ops)+1)
					ops[uid] = operation{strings.ToLower(q.URL.Path), q.Method}
					h := http.Header{}
					h.Set("Azure-AsyncOperation", strings.Replace(netappTestPollURL("status_url"), testTenant, uid, 1))
					h.Set("Location", strings.Replace(netappTestPollURL("result_url"), testTenant, uid, 1))
					return &http.Response{StatusCode: 202, Header: h, Body: io.NopCloser(strings.NewReader(""))}, true
				}
				if strings.Contains(strings.ToLower(q.URL.Path), "/operationresults/") {
					uid := last(q.URL.Path)
					op, ok := ops[uid]
					if !ok {
						t.Fatal("unknown operation")
					}
					if expired && op.id != f.vault {
						if op.method == "PATCH" {
							f.finishPatch(op.id)
						} else {
							f.missing[op.id] = true
						}
						return jsonResponse(404, map[string]any{"error": map[string]any{"code": "OperationNotFound"}}, nil), true
					}
					if q.URL.Query().Get("operationResultResponseType") == "Location" {
						if op.method == "PATCH" {
							return jsonResponse(200, f.objects[op.id], nil), true
						}
						return &http.Response{StatusCode: 204, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(""))}, true
					}
					raw := netappTestStatus(op.id, "Succeeded")
					raw["id"], raw["name"] = q.URL.Path, uid
					object(raw["properties"])["action"] = op.method
					return jsonResponse(200, raw, nil), true
				}
				return previous(q)
			}
			done := false
			for range 100 {
				wire, _ := json.Marshal(result)
				if json.Unmarshal(wire, &result) != nil {
					t.Fatal("receipt")
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
				out, err := driver.Wait(t.Context(), req, result)
				if err != nil {
					t.Fatal("async vault", result.Data["phase"], err)
				}
				result.Data = out.Data
				if out.Done {
					done = true
					break
				}
				if object(result.Data["operation"])["complete"] == true {
					for id := range f.pending {
						f.finishPatch(id)
					}
					if result.Data["phase"] == "backups" && result.Data["operation_done"] == true {
						for id := range f.backupDeletes {
							f.missing[id] = true
						}
					}
					if result.Data["phase"] == "deleting" && result.Data["operation_done"] == true {
						f.missing[f.vault] = true
					}
				}
			}
			if !done || len(ops) != 9 || f.vaultDeletes != 1 || f.policyDeletes != 0 {
				t.Fatal("async vault incomplete or replayed", done, len(ops))
			}
			for _, id := range f.volumes {
				if f.patches[id] != 3 || f.missing[id] {
					t.Fatal("volume not retained")
				}
			}

		})
	}
}

func TestNetappVaultLateBackupStopsPartialCleanup(t *testing.T) {
	f := newNetappVaultFixture(t)
	req := f.request(t)
	driver, err := f.runtime.ResolveAction(t.Context(), "connection", req.Asset)
	if err != nil {
		t.Fatal(err)
	}
	result, err := driver.Execute(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	for range 30 {
		out, err := driver.Wait(t.Context(), req, result)
		if err != nil {
			t.Fatal(err)
		}
		result.Data = out.Data
		for id := range f.pending {
			f.finishPatch(id)
		}
		if result.Data["phase"] == "backups" {
			break
		}
	}
	if result.Data["phase"] != "backups" {
		t.Fatal("policy unassignment incomplete")
	}
	raw := batchClone(f.objects[f.backups[0]])
	id := f.vault + "/backups/late"
	raw["id"], raw["name"] = id, "late"
	object(raw["properties"])["backupId"] = testTenant
	f.objects[id] = raw
	if _, err := driver.Wait(t.Context(), req, result); err == nil {
		t.Fatal("late backup accepted")
	}
	if len(f.backupDeletes) != 0 || f.vaultDeletes != 0 {
		t.Fatal("unreviewed backup deletion")
	}
	for _, id := range f.volumes {
		assignments, _ := netappAssignments(f.objects[id])
		if assignments[netappBackupPolicyType] != "" || assignments[netappVaultType] != f.vault || f.patches[id] != 2 {
			t.Fatal("partial cleanup boundary")
		}
	}
}
