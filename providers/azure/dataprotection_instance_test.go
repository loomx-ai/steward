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

type protectionInstanceFixture struct {
	*protectionFixture
	deletes, status, readFault    int
	statusURL, resultURL, sibling string
}

func newProtectionInstanceFixture(t *testing.T) *protectionInstanceFixture {
	t.Helper()
	f := &protectionInstanceFixture{protectionFixture: newProtectionFixture(t), status: 200}
	f.statusURL = "https://management.azure.com" + f.vault + "/operationstatus/Operation-AbC==?api-version=" + dataProtectionVersion
	f.resultURL = "https://management.azure.com/subscriptions/" + testSubscription + "/providers/microsoft.dataprotection/locations/eastus/operationresults/Operation-AbC==?api-version=" + dataProtectionVersion
	f.sibling = f.instance + "-other"
	sibling := batchClone(f.objects[f.instance])
	sibling["id"], sibling["name"] = f.sibling, last(f.sibling)
	f.objects[f.sibling] = sibling
	base := f.runtime.transport
	f.runtime.transport = roundTripFunc(func(q *http.Request) (*http.Response, error) {
		if q.Method == "DELETE" {
			if !strings.EqualFold(q.URL.Path, f.instance) || q.URL.Query().Get("api-version") != dataProtectionVersion || len(q.URL.Query()) != 1 || q.Header.Get("If-Match") != "" || !uuidPattern.MatchString(q.Header.Get("x-ms-client-request-id")) {
				t.Fatal("unexpected instance mutation", q.Method, q.URL)
			}
			if q.Body != nil {
				body, err := io.ReadAll(q.Body)
				if err != nil || len(body) > 0 {
					t.Fatal("unexpected instance delete body", err)
				}
			}
			f.deletes++
			h := http.Header{}
			if f.status == 202 {
				h.Set("Azure-AsyncOperation", f.statusURL)
				h.Set("Location", f.resultURL)
			}
			return &http.Response{StatusCode: f.status, Header: h, Body: io.NopCloser(strings.NewReader(""))}, nil
		}
		if q.URL.String() == f.statusURL {
			return jsonResponse(200, map[string]any{"id": q.URL.Path, "name": "Operation-AbC==", "status": "Succeeded"}, nil), nil
		}
		if q.URL.String() == f.resultURL {
			return jsonResponse(200, map[string]any{"objectType": "OperationJobExtendedInfo"}, nil), nil
		}
		if f.deletes > 0 && f.readFault != 0 && strings.EqualFold(q.URL.Path, f.instance) {
			return jsonResponse(f.readFault, map[string]any{}, nil), nil
		}
		if armPathProvider(q.URL.Path) != "microsoft.dataprotection" && !strings.EqualFold(q.URL.Path, "/subscriptions/"+testSubscription+"/resourcegroups/test") {
			if res, ok := fleetGraphEmptyIndexes(t, q); ok {
				return res, nil
			}
		}
		return base.RoundTrip(q)
	})
	return f
}
func (f *protectionInstanceFixture) request(t *testing.T) contracts.ActionRequest {
	t.Helper()
	batch, err := f.runtime.List(t.Context(), protectionRequest(f.protectionFixture, dataProtectionInstance))
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range batch.Items {
		if item.NativeID == f.instance {
			return contracts.ActionRequest{Asset: asset.Asset{ID: "instance", Identity: asset.Identity{Provider: asset.ProviderAzure, Partition: "azure", ConnectionID: "connection", NativeType: dataProtectionInstance, NativeID: item.NativeID}, Location: item.Location, Normalized: item.Normalized}, Action: "delete", IdempotencyKey: "delete-instance"}
		}
	}
	t.Fatal("missing instance")
	return contracts.ActionRequest{}
}
func TestDataProtectionInstanceNativeDeleteRestartAndRetention(t *testing.T) {
	for _, status := range []int{200, 202, 204} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			f := newProtectionInstanceFixture(t)
			f.status = status
			req := f.request(t)
			driver, err := f.runtime.ResolveAction(t.Context(), "connection", req.Asset)
			if err != nil {
				t.Fatal(err)
			}
			before := map[string]string{}
			for _, id := range []string{f.vault, f.policy, f.sibling} {
				wire, _ := json.Marshal(f.objects[id])
				before[id] = string(wire)
			}
			result, err := driver.Execute(t.Context(), req)
			if err != nil || f.deletes != 1 || result.Data["binding"] == nil {
				t.Fatal("native delete receipt", result, err, f.deletes)
			}
			f.readFault = 503
			failed := false
			for i := 0; i < 5; i++ {
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
				poll, err := driver.Wait(t.Context(), req, result)
				if err != nil {
					failed = true
					break
				}
				if poll.Done {
					t.Fatal("live instance reported deleted")
				}
				result.Data = poll.Data
			}
			if !failed {
				t.Fatal("own-read service failure ignored")
			}
			req.ExecutionResult = &result
			if _, err = driver.Execute(t.Context(), req); err != nil || f.deletes != 1 {
				t.Fatal("reissued delete after restart", err, f.deletes)
			}
			req.ExecutionResult = nil
			f.readFault = 0
			if poll, err := driver.Wait(t.Context(), req, result); err != nil || poll.Done {
				t.Fatal("live instance lost", poll, err)
			}
			retained := batchClone(f.objects[f.instance])
			retained["id"], retained["type"] = f.deletedInstance, dataProtectionDeletedInstance
			object(retained["properties"])["currentProtectionState"] = "SoftDeleted"
			object(retained["properties"])["deletionInfo"] = map[string]any{"deletionTime": "2026-09-17T00:00:00Z"}
			f.objects[f.deletedInstance] = retained
			delete(f.objects, f.instance)
			poll, err := driver.Wait(t.Context(), req, result)
			if err != nil || !poll.Done || poll.State != "soft_deleted" || object(poll.Data["retention"])["permanent_purge_verified"] != false {
				t.Fatal("retention result", poll, err)
			}
			for id, want := range before {
				wire, _ := json.Marshal(f.objects[id])
				if string(wire) != want {
					t.Fatal("other resource changed", id)
				}
			}
			if f.deletes != 1 {
				t.Fatal("unexpected mutation count", f.deletes)
			}
		})
	}
}

func TestDataProtectionInstanceCleanupWorkerKeepsOtherBackups(t *testing.T) {
	f := newProtectionInstanceFixture(t)
	repo, registry, path := azureNativeWorkerRepository(t, f.runtime)
	values := azureNativeWorkerScan(t, f.runtime, dataProtectionSource, repo, registry, []string{dataProtectionVault, dataProtectionPolicy, dataProtectionInstance, dataProtectionDeletedInstance, dataProtectionDeletedVault}, false, true)
	var instance asset.Asset
	for _, v := range values {
		if v.Identity.NativeID == f.instance {
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
		if warning.Code == plan.WarningDataProtectionInstanceDelete {
			warned = strings.Contains(warning.Message, "does not mean permanent purge") && strings.Contains(warning.Message, "source workload")
		}
	}
	if !warned {
		t.Fatal("backup retention warning missing")
	}
	attempt, err := planner.CreateExecution(t.Context(), cleanup.CreateExecutionRequest{ConnectionID: "connection", CleanupTaskID: task.Task.ID, RequestedBy: "operator", IdempotencyKey: "instance-worker", Confirmation: cleanup.ExecutionConfirmation{Acknowledged: true}})
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
	// Transient service failures must resume from the durable acknowledgement.
	// Permission failures are covered separately and intentionally terminate jobs.
	f.readFault = 503
	first := resume(true)
	if first.ProviderResult["binding"] == nil || f.deletes != 1 {
		t.Fatal("native acknowledgement not durable", first)
	}
	again := resume(true)
	if again.ProviderResult["operation_done"] != true || object(first.ProviderResult["operation"])["binding"] != object(again.ProviderResult["operation"])["binding"] || f.deletes != 1 {
		t.Fatal("native acknowledgement changed across restart")
	}
	failing := resume(true)
	if failing.ProviderResult["binding"] != again.ProviderResult["binding"] {
		t.Fatal("read failure changed receipt")
	}
	f.readFault = 0
	resume(true)
	delete(f.objects, f.instance)
	resume(true)
	resume(false)
	active, err := repo.ListActiveAssetsByConnection(t.Context(), "connection", "")
	if err != nil || len(active) != len(values)-1 || f.deletes != 1 {
		t.Fatal("cleanup scope", len(active), len(values), err, f.deletes)
	}
	for _, v := range active {
		if v.Identity.NativeID == f.instance {
			t.Fatal("deleted instance remains active")
		}
	}
	wire, _ := json.Marshal(first.ProviderResult)
	if strings.Contains(string(wire), "secret-config") {
		t.Fatal("receipt exposed private configuration")
	}
}

func TestDataProtectionInstanceRejectsChangedReview(t *testing.T) {
	for _, mode := range []string{"source", "policy", "vault", "protected-tag", "updating", "lock", "retained-forbidden", "policy-forbidden", "proof", "parameters", "receipt"} {
		t.Run(mode, func(t *testing.T) {
			f := newProtectionInstanceFixture(t)
			req := f.request(t)
			driver, err := f.runtime.ResolveAction(t.Context(), "connection", req.Asset)
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "source":
				object(object(f.objects[f.instance]["properties"])["dataSourceInfo"])["resourceID"] = "/changed"
			case "policy":
				object(f.objects[f.policy]["properties"])["privateFutureSetting"] = "changed"
			case "vault":
				object(f.objects[f.vault]["properties"])["securitySettings"] = map[string]any{"softDeleteSettings": map[string]any{"state": "Off"}}
			case "protected-tag":
				f.objects[f.instance]["tags"] = map[string]any{"steward:protected": "true"}
			case "updating":
				object(f.objects[f.instance]["properties"])["currentProtectionState"] = "UpdatingProtection"
			case "proof":
				req.Asset.Normalized[dataProtectionInstanceProof] = "forged"
			case "parameters":
				req.Parameters = map[string]any{"disableSoftDelete": true}
			case "receipt":
				req.ExecutionResult = &contracts.ActionResult{Data: map[string]any{"already_absent": true, "binding": "forged"}}
			}
			f.override = func(q *http.Request) (*http.Response, bool) {
				path := strings.ToLower(q.URL.Path)
				if mode == "lock" && strings.HasSuffix(path, "/providers/microsoft.authorization/locks") {
					return jsonResponse(200, map[string]any{"value": []any{map[string]any{"id": f.vault + "/providers/Microsoft.Authorization/locks/keep", "properties": map[string]any{"level": "CanNotDelete"}}}}, nil), true
				}
				if mode == "retained-forbidden" && path == f.vault+"/deletedbackupinstances" || mode == "policy-forbidden" && path == f.policy {
					return jsonResponse(403, map[string]any{}, nil), true
				}
				return nil, false
			}
			if _, err := driver.Preflight(t.Context(), req); err == nil {
				t.Fatal("changed review passed preflight", mode)
			}
			if _, err := driver.Execute(t.Context(), req); err == nil || f.deletes != 0 {
				t.Fatal("changed review mutated resource", mode, err, f.deletes)
			}
		})
	}
}

func TestDataProtectionInstanceAbsenceRequiresReadableRetentionAndParents(t *testing.T) {
	f := newProtectionInstanceFixture(t)
	req := f.request(t)
	driver, err := f.runtime.ResolveAction(t.Context(), "connection", req.Asset)
	if err != nil {
		t.Fatal(err)
	}
	delete(f.objects, f.instance)
	f.override = func(q *http.Request) (*http.Response, bool) {
		if strings.EqualFold(q.URL.Path, f.vault+"/deletedbackupinstances") {
			return jsonResponse(403, map[string]any{}, nil), true
		}
		return nil, false
	}
	if _, err := driver.Readback(t.Context(), req); err == nil {
		t.Fatal("unreadable retained backups treated as absence")
	}
	f.override = nil
	delete(f.objects, f.deletedInstance)
	read, err := driver.Readback(t.Context(), req)
	if err != nil || read.Exists || read.State != "active_absent" || read.Data["permanent_purge_verified"] != false {
		t.Fatal("overstated purge", read, err)
	}
	delete(f.objects, f.vault)
	if _, err := driver.Readback(t.Context(), req); err == nil {
		t.Fatal("missing parent treated as own absence")
	}
}
