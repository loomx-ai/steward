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

type recoveryContainerActionFixture struct {
	*recoveryServicesFixture
	exists, complete                  bool
	deletes, polls, status, readFault int
	pollURL, statusURL, location      string
}

func newRecoveryContainerActionFixture(t *testing.T) *recoveryContainerActionFixture {
	t.Helper()
	f := &recoveryContainerActionFixture{recoveryServicesFixture: recoveryContainerFixture(t), exists: true, status: 202}
	delete(f.objects, f.item)
	f.pollURL = apiURL(f.container+"/operationResults/Operation-AbC==", recoveryServicesBackupVersion)
	f.statusURL = apiURL(f.vault+"/backupFabrics/azure/operationsStatus/Operation-AbC==", recoveryServicesBackupVersion)
	f.location = "https://management.azure.com" + f.vault + "/backupOperationResults/Operation-AbC==?fabricName=Azure"
	base := f.runtime.transport
	f.runtime.transport = roundTripFunc(func(q *http.Request) (*http.Response, error) {
		if q.Method == "DELETE" {
			if !strings.EqualFold(q.URL.Path, f.container) || q.URL.Query().Get("api-version") != recoveryServicesBackupVersion || len(q.URL.Query()) != 1 || q.Header.Get("If-Match") != "" || !uuidPattern.MatchString(q.Header.Get("x-ms-client-request-id")) {
				t.Fatal("unregister changed native request")
			}
			if q.Body != nil {
				b, err := io.ReadAll(q.Body)
				if err != nil || len(b) != 0 {
					t.Fatal("unexpected unregister body")
				}
			}
			f.deletes++
			h := http.Header{}
			if f.status == 202 {
				h.Set("Azure-AsyncOperation", f.statusURL)
				h.Set("Location", f.location)
			}
			return &http.Response{StatusCode: f.status, Header: h, Body: io.NopCloser(strings.NewReader(""))}, nil
		}
		if q.URL.String() == f.pollURL {
			f.polls++
			status := 202
			if f.complete {
				status = 204
			}
			return &http.Response{StatusCode: status, Header: http.Header{"Retry-After": []string{"60"}}, Body: io.NopCloser(strings.NewReader(""))}, nil
		}
		if strings.EqualFold(q.URL.Path, f.container) && f.deletes > 0 && f.readFault != 0 {
			return jsonResponse(f.readFault, map[string]any{"error": map[string]any{"code": "ServiceUnavailable"}}, nil), nil
		}
		if strings.EqualFold(q.URL.Path, f.container) && !f.exists {
			return jsonResponse(404, map[string]any{"error": map[string]any{"code": "ResourceNotFound"}}, nil), nil
		}
		if armPathProvider(q.URL.Path) != "microsoft.recoveryservices" && q.Method == "GET" {
			if res, ok := fleetGraphEmptyIndexes(t, q); ok {
				return res, nil
			}
		}
		return base.RoundTrip(q)
	})
	return f
}

func (f *recoveryContainerActionFixture) request(t *testing.T) contracts.ActionRequest {
	t.Helper()
	batch, err := f.runtime.List(t.Context(), recoveryServicesRequest(f.recoveryServicesFixture, recoveryServicesContainer))
	if err != nil || len(batch.Items) != 1 {
		t.Fatal("missing container inventory", err)
	}
	item := batch.Items[0]
	if item.Actionable == nil || !*item.Actionable {
		t.Fatal("empty registered container not actionable")
	}
	value := asset.Asset{ID: "container", Identity: asset.Identity{Provider: asset.ProviderAzure, Partition: "azure", ConnectionID: "connection", NativeID: item.NativeID, NativeType: item.NativeType}, Location: item.Location, Normalized: item.Normalized}
	return contracts.ActionRequest{Asset: value, Action: "delete", IdempotencyKey: "unregister-container"}
}

func TestRecoveryContainerRegisteredUnregisterRecovery(t *testing.T) {
	for _, status := range []int{200, 202, 204} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			f := newRecoveryContainerActionFixture(t)
			f.status = status
			req := f.request(t)
			driver, err := f.runtime.ResolveAction(t.Context(), "connection", req.Asset)
			if err != nil {
				t.Fatal(err)
			}
			result, err := driver.Execute(t.Context(), req)
			if err != nil || f.deletes != 1 {
				t.Fatal("native unregister failed", err)
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
			resumed := req
			resumed.ExecutionResult = &result
			if _, err = driver.Execute(t.Context(), resumed); err != nil || f.deletes != 1 {
				t.Fatal("resumed unregister repeated mutation", err)
			}
			poll, err := driver.Wait(t.Context(), req, result)
			if err != nil || poll.Done {
				t.Fatal("accepted operation treated as absence", err)
			}
			if status == 202 && poll.RetryAfter.Seconds() != 60 {
				t.Fatal("Retry-After ignored", poll.RetryAfter)
			}
			result.Data = poll.Data
			f.complete = true
			poll, err = driver.Wait(t.Context(), req, result)
			if err != nil || poll.Done {
				t.Fatal("operation completion erased live container", err)
			}
			result.Data = poll.Data
			f.exists = false
			poll, err = driver.Wait(t.Context(), req, result)
			if err != nil || !poll.Done || poll.State != "unregistered" {
				t.Fatal("own absence did not close unregister", err)
			}
			if f.deletes != 1 || (status != 202 && f.polls != 0) || (status == 202 && f.polls != 2) {
				t.Fatal("unexpected mutation/poll count", f.deletes, f.polls)
			}
		})
	}
}

func TestRecoveryContainerLateConsumersAndChanges(t *testing.T) {
	for _, mode := range []string{"active", "retained", "container-config", "vault-config", "proof", "parameters", "receipt", "poll-scope"} {
		t.Run(mode, func(t *testing.T) {
			f := newRecoveryContainerActionFixture(t)
			req := f.request(t)
			driver, err := f.runtime.ResolveAction(t.Context(), "connection", req.Asset)
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "active", "retained":
				_, raw := recoveryServicesTestItem()
				object(raw["properties"])["isScheduledForDeferredDelete"] = mode == "retained"
				f.objects[f.item] = raw
			case "container-config":
				object(f.objects[f.container]["properties"])["unknownConfiguration"] = "changed"
			case "vault-config":
				object(f.objects[f.vault]["properties"])["unknownConfiguration"] = "changed"
			case "proof":
				req.Asset.Normalized[recoveryContainerProof] = "forged"
			case "parameters":
				req.Parameters = map[string]any{"force": true}
			case "receipt":
				req.ExecutionResult = &contracts.ActionResult{Data: map[string]any{"binding": "forged"}}
			case "poll-scope":
				f.location = strings.Replace(f.location, "/vaults/vault/", "/vaults/foreign/", 1)
			}
			if _, err = driver.Execute(t.Context(), req); err == nil {
				t.Fatal("unsafe unregister accepted")
			}
			want := 0
			if mode == "poll-scope" {
				want = 1
			}
			if f.deletes != want {
				t.Fatal("mutation preceded safety checks", f.deletes)
			}
		})
	}
}

func TestRecoveryContainerCleanupWorkerResumesUnregister(t *testing.T) {
	f := newRecoveryContainerActionFixture(t)
	repo, registry, path := azureNativeWorkerRepository(t, f.runtime)
	values := azureNativeWorkerScan(t, f.runtime, recoveryServicesSource, repo, registry, []string{recoveryServicesVault, recoveryServicesContainer}, false, true)
	var instance asset.Asset
	for _, v := range values {
		if v.Identity.NativeID == f.container {
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
	attempt, err := planner.CreateExecution(t.Context(), cleanup.CreateExecutionRequest{ConnectionID: "connection", CleanupTaskID: task.Task.ID, RequestedBy: "operator", IdempotencyKey: "container-worker", Confirmation: cleanup.ExecutionConfirmation{Acknowledged: true}})
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
	again := resume(true)
	if first.ProviderResult["binding"] != again.ProviderResult["binding"] || f.deletes != 1 {
		t.Fatal("pending receipt changed across restart")
	}
	f.complete = true
	f.readFault = 503
	resume(true)
	failedRead := resume(true)
	repeatRead := resume(true)
	if failedRead.ProviderResult["binding"] != repeatRead.ProviderResult["binding"] || f.deletes != 1 {
		t.Fatal("transient read changed durable receipt or repeated unregister")
	}
	f.readFault = 0
	resume(true)
	f.exists = false
	resume(true)
	resume(false)

	active, err := repo.ListActiveAssetsByConnection(t.Context(), "connection", "")
	if err != nil || len(active) != len(values)-1 || f.deletes != 1 {
		t.Fatal("cleanup scope", len(active), len(values), err, f.deletes)
	}
	for _, v := range active {
		if v.Identity.NativeID == f.container {
			t.Fatal("deleted instance remains active")
		}
	}
}

func TestRecoveryContainerCompletionRechecksConsumers(t *testing.T) {
	for _, retained := range []bool{false, true} {
		t.Run(map[bool]string{false: "active", true: "retained"}[retained], func(t *testing.T) {
			f := newRecoveryContainerActionFixture(t)
			req := f.request(t)
			driver, err := f.runtime.ResolveAction(t.Context(), "connection", req.Asset)
			if err != nil {
				t.Fatal(err)
			}
			result, err := driver.Execute(t.Context(), req)
			if err != nil {
				t.Fatal(err)
			}
			f.complete, f.exists = true, false
			_, raw := recoveryServicesTestItem()
			object(raw["properties"])["isScheduledForDeferredDelete"] = retained
			f.objects[f.item] = raw
			if poll, err := driver.Wait(t.Context(), req, result); err == nil || poll.Done {
				t.Fatal("container absence hid a remaining protected item", err)
			}
			if f.deletes != 1 {
				t.Fatal("completion repeated unregister")
			}
		})
	}
}
