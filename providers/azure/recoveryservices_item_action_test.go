package azure

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/loomx-ai/steward/internal/app/cleanup"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/internal/persistence/sqlite"
	providerruntime "github.com/loomx-ai/steward/internal/provider/runtime"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

type recoveryItemActionFixture struct {
	*recoveryServicesFixture
	status, deletes, polls, jobReads, readFault int
	operationDone, jobDone                      bool
	statusURL, resultURL, jobID                 string
}

func newRecoveryItemActionFixture(t *testing.T) *recoveryItemActionFixture {
	t.Helper()
	f := &recoveryItemActionFixture{recoveryServicesFixture: recoveryItemReviewFixture(t), status: 202, jobID: "Delete-Job-AbC"}
	token := "Delete-Operation-AbC"
	f.statusURL = apiURL(f.vault+"/backupOperations/"+token, recoveryServicesBackupVersion) + "&t=1&c=2&s=3&h=4"
	f.resultURL = strings.Replace(f.statusURL, "/backupOperations/", "/backupOperationResults/", 1)
	base := f.runtime.transport
	f.runtime.transport = roundTripFunc(func(q *http.Request) (*http.Response, error) {
		if q.Method == "DELETE" {
			if !strings.EqualFold(q.URL.Path, f.item) || q.URL.Query().Get("api-version") != recoveryServicesBackupVersion || len(q.URL.Query()) != 1 || q.Header.Get("If-Match") != "" || !uuidPattern.MatchString(q.Header.Get("x-ms-client-request-id")) {
				t.Fatal("item delete changed native request")
			}
			if q.Body != nil {
				wire, err := io.ReadAll(q.Body)
				if err != nil || len(wire) != 0 {
					t.Fatal("unexpected delete body")
				}
			}
			f.deletes++
			h := http.Header{}
			if f.status == 202 {
				h.Set("Location", f.resultURL)
				h.Set("Azure-AsyncOperation", f.statusURL)
			}
			return &http.Response{StatusCode: f.status, Header: h, Body: io.NopCloser(strings.NewReader(""))}, nil
		}
		if q.URL.String() == f.statusURL {
			f.polls++
			state := "InProgress"
			if f.operationDone {
				state = "Succeeded"
			}
			raw := map[string]any{"id": token, "name": token, "status": state}
			if f.operationDone {
				raw["properties"] = map[string]any{"objectType": "OperationStatusJobExtendedInfo", "jobId": f.jobID}
			}
			return jsonResponse(200, raw, http.Header{"Retry-After": []string{"30"}}), nil
		}
		job := f.vault + "/backupJobs/" + f.jobID
		if strings.EqualFold(q.URL.Path, job) {
			f.jobReads++
			state := "InProgress"
			if f.jobDone {
				state = "Completed"
			}
			return jsonResponse(200, map[string]any{"id": job, "name": f.jobID, "type": "Microsoft.RecoveryServices/vaults/backupJobs", "properties": map[string]any{"jobType": "AzureWorkloadJob", "operation": "DeleteBackupData", "status": state}}, nil), nil
		}
		if strings.EqualFold(q.URL.Path, f.item) && f.deletes > 0 && f.readFault != 0 {
			return jsonResponse(f.readFault, map[string]any{"error": map[string]any{"code": "ServiceUnavailable"}}, nil), nil
		}
		if q.Method == "GET" && armPathProvider(q.URL.Path) != "microsoft.recoveryservices" {
			if res, ok := fleetGraphEmptyIndexes(t, q); ok {
				return res, nil
			}
		}
		return base.RoundTrip(q)
	})
	return f
}

func (f *recoveryItemActionFixture) request(t *testing.T) contracts.ActionRequest {
	t.Helper()
	batch, err := f.runtime.List(t.Context(), recoveryServicesRequest(f.recoveryServicesFixture, recoveryServicesItem))
	if err != nil || len(batch.Items) == 0 {
		t.Fatal("native item inventory", err)
	}
	var item contracts.InventoryItem
	for _, candidate := range batch.Items {
		if candidate.NativeID == f.item {
			item = candidate
		}
	}
	if item.NativeID == "" {
		t.Fatal("target item missing")
	}
	if item.Actionable == nil || !*item.Actionable {
		t.Fatal("reviewed active item not actionable")
	}
	value := asset.Asset{ID: "backup-item", Identity: asset.Identity{Provider: asset.ProviderAzure, Partition: "azure", ConnectionID: "connection", NativeType: recoveryServicesItem, NativeID: item.NativeID}, Location: item.Location, Normalized: item.Normalized}
	return contracts.ActionRequest{Asset: value, Action: "delete", IdempotencyKey: "delete-recovery-item"}
}

func (f *recoveryItemActionFixture) retain() {
	p := object(f.objects[f.item]["properties"])
	p["isScheduledForDeferredDelete"], p["protectionState"] = true, "ProtectionStopped"
	p["policyId"], p["policyName"] = "", ""
	p["deferredDeleteTimeInUTC"] = "2026-09-17T00:00:00Z"
	p["softDeleteRetentionPeriod"] = 14
}

func TestRecoveryItemRegisteredDeleteRecovery(t *testing.T) {
	for _, status := range []int{200, 202, 204} {
		for _, retained := range []bool{false, true} {
			t.Run(http.StatusText(status)+map[bool]string{false: "/absent", true: "/retained"}[retained], func(t *testing.T) {
				f := newRecoveryItemActionFixture(t)
				f.status = status
				req := f.request(t)
				driver, err := f.runtime.ResolveAction(t.Context(), "connection", req.Asset)
				if err != nil {
					t.Fatal(err)
				}
				result, err := driver.Execute(t.Context(), req)
				if err != nil || f.deletes != 1 {
					t.Fatal("native delete", err)
				}
				wire, _ := json.Marshal(result)
				result = contracts.ActionResult{}
				if err = json.Unmarshal(wire, &result); err != nil {
					t.Fatal(err)
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
				saved := req
				saved.ExecutionResult = &result
				if _, err = driver.Execute(t.Context(), saved); err != nil || f.deletes != 1 {
					t.Fatal("restart repeated deletion", err)
				}
				pending := func() {
					t.Helper()
					poll, err := driver.Wait(t.Context(), req, result)
					if err != nil || poll.Done {
						t.Fatal("operation/job completed before own state", err)
					}
					result.Data = poll.Data
				}
				pending()
				if status == 202 {
					f.operationDone = true
					pending()
					if f.jobReads != 0 {
						t.Fatal("job phase was not checkpointed")
					}
					pending()
					f.jobDone = true
				}
				pending()
				object(f.objects[f.item]["properties"])["protectionState"] = "ProtectionStopped"
				pending() // Stop-with-retain is not a deleted backup item.
				if retained {
					f.retain()
				} else {
					delete(f.objects, f.item)
				}
				poll, err := driver.Wait(t.Context(), req, result)
				expected := "absent"
				if retained {
					expected = "soft_deleted"
				}
				if err != nil || !poll.Done || poll.State != expected || object(poll.Data["outcome"])["outcome"] != expected || f.deletes != 1 {
					t.Fatal("native final outcome", err)
				}
				if retained && object(poll.Data["outcome"])["retained_native_id"] != f.item {
					t.Fatal("retained identity lost")
				}
				if status != 202 && (f.polls != 0 || f.jobReads != 0) {
					t.Fatal("synchronous delete invented async requests")
				}
			})
		}
	}
}

func TestRecoveryItemCleanupWorkerPreservesSameIDRetention(t *testing.T) {
	f := newRecoveryItemActionFixture(t)
	repo, registry, path := azureNativeWorkerRepository(t, f.runtime)
	values := azureNativeWorkerScan(t, f.runtime, recoveryServicesSource, repo, registry, []string{recoveryServicesVault, recoveryServicesContainer, recoveryServicesItem}, false, true)
	var instance asset.Asset
	for _, v := range values {
		if v.Identity.NativeID == f.item {
			instance = v
		}
	}
	if instance.ID == "" {
		t.Fatal("native instance not persisted")
	}
	planner := cleanup.NewService(repo, registry)
	task, err := planner.CreateTask(t.Context(), cleanup.CreateTaskRequest{ConnectionID: "connection", Selectors: []plan.CleanupSelector{{Kind: plan.SelectorAsset, AssetID: instance.ID}}, CreatedBy: "operator"})
	if err != nil || task.Task.Status != plan.StatusReady || len(task.Steps) != 1 || len(task.ImpactItems) != 0 {
		t.Fatal("instance-only plan", task, err)
	}
	warned := false
	for _, warning := range task.Task.Warnings {
		if warning.Code == plan.WarningRecoveryServicesItemDelete {
			warned = strings.Contains(warning.Message, "completion does not mean permanent purge") && strings.Contains(warning.Message, "source workload")
		}
	}
	if !warned {
		t.Fatal("missing backup deletion consequence warning")
	}
	attempt, err := planner.CreateExecution(t.Context(), cleanup.CreateExecutionRequest{ConnectionID: "connection", CleanupTaskID: task.Task.ID, RequestedBy: "operator", IdempotencyKey: "item-worker", Confirmation: cleanup.ExecutionConfirmation{Acknowledged: true}})
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
	if job.ID == "" {
		t.Fatal("missing cleanup job")
	}
	resume := func(pending bool) execution.ActionAttempt {
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
			t.Fatal("worker recovery", err)
		}
		current, err := repo.Executions().GetActionByExecutionStep(t.Context(), attempt.ID, string(task.Steps[0].ID))
		if err != nil {
			t.Fatal(err)
		}
		return current
	}
	first := resume(true)
	if first.ProviderResult["binding"] == nil || f.deletes != 1 {
		t.Fatal("native acknowledgement not durable", first)
	}
	resume(true)
	f.operationDone = true
	phase := resume(true)
	if object(phase.ProviderResult["operation"])["status_done"] != true {
		t.Fatal("operation success did not persist job IDs")
	}
	f.jobDone = true
	f.readFault = 503
	resume(true)
	resume(true)
	f.readFault = 0
	f.retain()
	resume(true)
	resume(false)

	active, err := repo.ListActiveAssetsByConnection(t.Context(), "connection", "")
	if err != nil || len(active) != len(values)-1 || f.deletes != 1 {
		t.Fatal("cleanup scope", len(active), len(values), err, f.deletes)
	}
	for _, v := range active {
		if v.Identity.NativeID == f.item {
			t.Fatal("deleted instance remains active")
		}
	}
	rediscovered := azureNativeWorkerScan(t, f.runtime, recoveryServicesSource, repo, registry, []string{recoveryServicesVault, recoveryServicesContainer, recoveryServicesItem}, false, true)
	found := false
	for _, value := range rediscovered {
		if value.ID == instance.ID {
			found = value.ClosedAt == nil && value.Normalized["retained"] == true && value.Normalized["state"] == "soft_deleted" && !value.Capabilities.Has(asset.CapabilityActionable)
		}
	}
	if !found {
		t.Fatal("same-ID retained backup disappeared after rescan")
	}
	final, err := repo.Executions().GetActionByExecutionStep(t.Context(), attempt.ID, string(task.Steps[0].ID))
	if err != nil || object(final.ProviderResult["outcome"])["retained_native_id"] != f.item {
		t.Fatal("persisted outcome lost retained identity", err)
	}

}

// Revalidate the approved configuration both before mutation and after a native
// acknowledgement. Neither a successful DELETE nor soft deletion authorizes drift.
func TestRecoveryItemActionRejectsLateChanges(t *testing.T) {
	changes := map[string]func(*recoveryItemActionFixture){
		"source": func(f *recoveryItemActionFixture) {
			object(f.objects[f.item]["properties"])["sourceResourceId"] = f.vault + "/replacement"
		},
		"policy": func(f *recoveryItemActionFixture) {
			object(f.objects[f.vault+"/backuppolicies/policy"]["properties"])["privatePolicySetting"] = "changed"
		},
		"item-protection": func(f *recoveryItemActionFixture) {
			f.objects[f.item]["tags"] = map[string]any{"steward:protected": "true"}
		},
		"vault-immutability": func(f *recoveryItemActionFixture) {
			object(f.objects[f.vault]["properties"])["securitySettings"] = map[string]any{"immutabilitySettings": map[string]any{"state": "Locked"}}
		},
		"container-registration": func(f *recoveryItemActionFixture) {
			object(f.objects[f.container]["properties"])["registrationStatus"] = "NotRegistered"
		},
	}
	for name, change := range changes {
		for _, acknowledged := range []bool{false, true} {
			phase := "before-delete"
			if acknowledged {
				phase = "after-acknowledgement"
			}
			t.Run(name+"/"+phase, func(t *testing.T) {
				f := newRecoveryItemActionFixture(t)
				f.status = 204
				req := f.request(t)
				driver, err := f.runtime.ResolveAction(t.Context(), "connection", req.Asset)
				if err != nil {
					t.Fatal(err)
				}
				var result contracts.ActionResult
				if acknowledged {
					result, err = driver.Execute(t.Context(), req)
					if err != nil || f.deletes != 1 {
						t.Fatal("initial delete", err)
					}
					f.retain()
				}
				change(f)
				if acknowledged {
					poll, err := driver.Wait(t.Context(), req, result)
					if err == nil || poll.Done || f.deletes != 1 {
						t.Fatal("configuration drift accepted after acknowledgement", err)
					}
				} else {
					if _, err := driver.Preflight(t.Context(), req); err == nil {
						t.Fatal("preflight accepted drift")
					}
					if _, err := driver.Execute(t.Context(), req); err == nil || f.deletes != 0 {
						t.Fatal("changed resource mutated", err)
					}
				}
			})
		}
	}
}

func TestRecoveryHanaCleanupWorkerOrdersExplicitSelections(t *testing.T) {
	f := newRecoveryItemActionFixture(t)
	snapshotID := recoveryHanaSnapshot(f)
	f.status = 204
	var mutations []string
	base := f.runtime.transport
	f.runtime.transport = roundTripFunc(func(q *http.Request) (*http.Response, error) {
		if q.Method == "DELETE" {
			id := strings.ToLower(q.URL.Path)
			if id != snapshotID && id != f.item {
				t.Fatal("unexpected mutation", id)
			}
			if id == f.item && f.objects[snapshotID] != nil {
				t.Fatal("database deleted before snapshot disappearance")
			}
			if q.URL.Query().Get("api-version") != recoveryServicesBackupVersion || !uuidPattern.MatchString(q.Header.Get("x-ms-client-request-id")) {
				t.Fatal("invalid native deletion")
			}
			mutations = append(mutations, id)
			delete(f.objects, id)
			return &http.Response{StatusCode: 204, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(""))}, nil
		}
		return base.RoundTrip(q)
	})
	repo, registry, path := azureNativeWorkerRepository(t, f.runtime)
	values := azureNativeWorkerScan(t, f.runtime, recoveryServicesSource, repo, registry, []string{recoveryServicesVault, recoveryServicesContainer, recoveryServicesItem}, false, true)
	var selectors []plan.CleanupSelector
	for _, v := range values {
		if v.Identity.NativeID == f.item || v.Identity.NativeID == snapshotID {
			selectors = append(selectors, plan.CleanupSelector{Kind: plan.SelectorAsset, AssetID: v.ID})
		}
	}
	if len(selectors) != 2 {
		t.Fatal("missing HANA inventory")
	}
	planner := cleanup.NewService(repo, registry)
	task, err := planner.CreateTask(t.Context(), cleanup.CreateTaskRequest{ConnectionID: "connection", Selectors: selectors, CreatedBy: "operator"})
	if err != nil || task.Task.Status != plan.StatusReady || len(task.Steps) != 2 || len(task.ImpactItems) != 0 {
		t.Fatal("joint HANA plan", task, err)
	}
	attempt, err := planner.CreateExecution(t.Context(), cleanup.CreateExecutionRequest{ConnectionID: "connection", CleanupTaskID: task.Task.ID, RequestedBy: "operator", IdempotencyKey: "hana-worker", Confirmation: cleanup.ExecutionConfirmation{Acknowledged: true}})
	if err != nil {
		t.Fatal(err)
	}
	jobs, err := repo.ListJobsByAggregate(t.Context(), "cleanup_task", string(task.Task.ID))
	if err != nil {
		t.Fatal(err)
	}
	// Reopen both persistence and the provider for every retry, including an
	// out-of-order delivery of the database job before its snapshot prerequisite.
	run := func(step plan.CleanupTaskStep) bool {
		t.Helper()
		var job execution.Job
		for _, candidate := range jobs {
			if text(candidate.Payload["cleanup_task_step_id"]) == string(step.ID) {
				job = candidate
			}
		}
		if job.ID == "" {
			t.Fatal("missing step job")
		}
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
		if err != nil && !errors.As(err, &retry) {
			t.Fatal("joint HANA execution", err)
		}
		return err == nil
	}
	var first, second plan.CleanupTaskStep
	for _, step := range task.Steps {
		if len(step.DependsOn) == 0 {
			first = step
		} else {
			second = step
		}
	}
	if first.ID == "" || second.ID == "" {
		t.Fatal("missing persisted ordering")
	}
	if run(second) || len(mutations) != 0 {
		t.Fatal("out-of-order job bypassed prerequisite")
	}
	for _, step := range []plan.CleanupTaskStep{first, second} {
		done := false
		for i := 0; i < 5 && !done; i++ {
			done = run(step)
		}
		if !done {
			t.Fatal("HANA step did not finish")
		}
		action, err := repo.Executions().GetActionByExecutionStep(t.Context(), attempt.ID, string(step.ID))
		if err != nil || object(action.ProviderResult["outcome"])["outcome"] != "absent" {
			t.Fatal("missing persisted own-read outcome", err)
		}
	}
	if len(mutations) != 2 || mutations[0] != snapshotID || mutations[1] != f.item {
		t.Fatal("wrong native deletion order", mutations)
	}
	active, err := repo.ListActiveAssetsByConnection(t.Context(), "connection", "")
	if err != nil || len(active) != len(values)-2 {
		t.Fatal("parent/source cleanup escaped selection", err)
	}
}
