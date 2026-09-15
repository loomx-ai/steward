package azure

import (
	"context"
	"encoding/json"
	"errors"
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

type sparkActionFixture struct {
	*synapseDataInventoryFixture
	mutations            map[string]int
	poolAbsent, pollDone bool
	readFault            int
	failAfterCancel      bool
}

func newSparkActionFixture(t *testing.T) *sparkActionFixture {
	f, _, _ := sparkWorkFixture(t)
	f.items[synapseNotebookType], f.items[synapseJobDefinitionType] = nil, nil
	a := &sparkActionFixture{synapseDataInventoryFixture: f, mutations: map[string]int{}}
	data := f.data
	f.data = func(q *http.Request) *http.Response {
		if q.Method == "DELETE" {
			a.mutations[q.URL.Path]++
			if a.failAfterCancel {
				a.readFault = 503
			}
			return jsonResponse(200, nil, nil)
		}
		if a.readFault != 0 {
			return jsonResponse(a.readFault, nil, nil)
		}
		return data(q)
	}
	previous := f.override
	f.override = func(q *http.Request) (*http.Response, bool) {
		if q.URL.Host == "management.azure.com" {
			if strings.EqualFold(q.URL.Path, text(f.pool["id"])) {
				if q.Method == "DELETE" {
					a.mutations[q.URL.Path]++
					h := http.Header{}
					h.Set("Azure-AsyncOperation", armOrigin+text(f.workspace["id"])+"/operationStatuses/operation?api-version="+synapseVersion)
					return jsonResponse(202, nil, h), true
				}
				if a.poolAbsent {
					return jsonResponse(404, nil, nil), true
				}
			}
			if strings.Contains(q.URL.Path, "/operationStatuses/") {
				status := "InProgress"
				if a.pollDone {
					status = "Succeeded"
				}
				return jsonResponse(200, map[string]any{"status": status}, nil), true
			}
		}
		if res, ok := previous(q); ok {
			return res, true
		}
		return fleetGraphEmptyIndexes(t, q)
	}
	return a
}

func (f *sparkActionFixture) request(t *testing.T) contracts.ActionRequest {
	t.Helper()
	batch, err := f.runtime.List(t.Context(), productRequest(f.runtime, synapseSparkType))
	if err != nil || !batch.Complete || len(batch.Items) != 1 || batch.Items[0].Actionable == nil || !*batch.Items[0].Actionable {
		t.Fatal("pool inventory not actionable", batch, err)
	}
	item := batch.Items[0]
	value := asset.Asset{ID: "pool-asset", Identity: asset.Identity{Provider: asset.ProviderAzure, Partition: "azure", ConnectionID: "connection", NativeType: synapseSparkType, NativeID: item.NativeID}, Normalized: item.Normalized, Location: item.Location}
	return contracts.ActionRequest{Asset: value, Action: "delete", IdempotencyKey: "spark-cleanup"}
}

func TestSynapseSparkActionDurablePhases(t *testing.T) {
	f := newSparkActionFixture(t)
	req := f.request(t)
	resolve := func() contracts.ActionDriver {
		t.Helper()
		fresh, err := NewRuntime(f.runtime.credentials)
		if err != nil {
			t.Fatal(err)
		}
		fresh.transport = f.runtime.transport
		driver, err := fresh.ResolveAction(t.Context(), "connection", req.Asset)
		if err != nil {
			t.Fatal(err)
		}
		return driver
	}
	driver := resolve()
	f.failAfterCancel = true
	result, err := driver.Execute(t.Context(), req)
	if err != nil || result.Data["phase"] != "cancel" || len(f.mutations) != 1 {
		t.Fatal(result, err, f.mutations)
	}
	batchID := text(result.Data["item"])
	f.failAfterCancel = false
	f.readFault = 403
	if _, err = driver.Wait(t.Context(), req, result); err == nil {
		t.Fatal("post-ack forbidden read accepted")
	}
	f.readFault = 0
	resume := func(done bool) {
		t.Helper()
		wire, _ := json.Marshal(result)
		if json.Unmarshal(wire, &result) != nil {
			t.Fatal("receipt serialization")
		}
		driver = resolve()
		saved := req
		saved.ExecutionResult = &result
		again, err := driver.Execute(t.Context(), saved)
		if err != nil || again.Data["binding"] != result.Data["binding"] {
			t.Fatal("ack replay", again, err)
		}
		wait, err := driver.Wait(t.Context(), req, result)
		if err != nil || wait.Done != done {
			t.Fatal(wait, err)
		}
		result.Data = wait.Data
	}
	resume(false)
	if text(result.Data["item"]) != batchID {
		t.Fatal("pending cancellation advanced")
	}
	endSynapseSpark(f.items[synapseBatchType])
	resume(false)
	if !strings.Contains(text(result.Data["item"]), "/sessions/") || len(f.mutations) != 2 {
		t.Fatal("session phase missing", result, f.mutations)
	}
	endSynapseSpark(f.items[synapseSessionType])
	resume(false)
	if result.Data["phase"] != "delete" || len(f.mutations) != 3 {
		t.Fatal("pool delete phase missing", result, f.mutations)
	}
	resume(false)
	f.pollDone = true
	resume(false)
	if result.Data["operation_done"] != true {
		t.Fatal("poll completion not persisted")
	}
	f.poolAbsent = true
	resume(true)
	for id, count := range f.mutations {
		if count != 1 {
			t.Fatal("repeated native mutation", id, count)
		}
	}
	if len(f.items) != 4 || !synapseSparkQuiesced(f.items[synapseBatchType]) {
		t.Fatal("historical record was erased")
	}
}

func TestSynapseSparkActionRejectsChangedReview(t *testing.T) {
	for _, fault := range []string{"pool", "workspace", "new work", "incarnation", "tag", "consumer", "proof", "receipt", "state"} {
		t.Run(fault, func(t *testing.T) {
			f := newSparkActionFixture(t)
			req := f.request(t)
			driver, err := f.runtime.ResolveAction(t.Context(), "connection", req.Asset)
			if err != nil {
				t.Fatal(err)
			}
			switch fault {
			case "pool":
				object(f.pool["properties"])["nodeCount"] = 99
			case "workspace":
				object(f.workspace["properties"])["managedResourceGroupName"] = "changed"
			case "new work":
				f.items[synapseBatchType]["id"] = 1
			case "incarnation":
				object(f.items[synapseBatchType]["schedulerInfo"])["submittedAt"] = "2026-02-24T09:47:41Z"
			case "tag":
				f.pool["tags"] = map[string]any{"steward:protected": "true"}
			case "consumer":
				f.items[synapseNotebookType] = f.item("Notebook_GetNotebook")
			case "proof":
				req.Asset.Normalized[synapseSparkProof] = "forged"
			case "receipt":
				req.ExecutionResult = &contracts.ActionResult{Data: map[string]any{"phase": "delete", "operation_done": true}}
			case "state":
				object(f.pool["properties"])["provisioningState"] = "Unknown"
			}
			if _, err := driver.Execute(t.Context(), req); err == nil || len(f.mutations) != 0 {
				t.Fatal("changed review mutated", fault, err, f.mutations)
			}
		})
	}
}

func TestSynapseSparkCleanupWorkerRestart(t *testing.T) {
	for _, completeGraph := range []bool{false, true} {
		name := "partial-inventory"
		if completeGraph {
			name = "complete-workspace-graph"
		}
		t.Run(name, func(t *testing.T) { synapseSparkCleanupWorkerRestart(t, completeGraph) })
	}
}
func synapseSparkCleanupWorkerRestart(t *testing.T, completeGraph bool) {
	f := newSparkActionFixture(t)
	repository, registry, path := azureNativeWorkerRepository(t, f.runtime)
	values := azureNativeWorkerScan(t, f.runtime, synapseSource, repository, registry, []string{synapseType, synapseSparkType}, false, false)
	var pool asset.Asset
	for _, value := range values {
		if value.Identity.NativeType == synapseSparkType {
			pool = value
		}
	}
	if pool.ID == "" {
		t.Fatal("missing persisted pool")
	}
	kinds := []string{synapseBatchType, synapseSessionType}
	if completeGraph {
		kinds = append(kinds, synapseNotebookType, synapseJobDefinitionType, synapsePipelineType)
	}
	values = azureNativeWorkerScan(t, f.runtime, synapseDataInventorySource, repository, registry, kinds, false, completeGraph)
	planner := cleanup.NewService(repository, registry)
	task, err := planner.CreateTask(t.Context(), cleanup.CreateTaskRequest{ConnectionID: "connection", Selectors: []plan.CleanupSelector{{Kind: plan.SelectorAsset, AssetID: pool.ID}}, CreatedBy: "operator"})
	if err != nil || task.Task.Status != plan.StatusReady || len(task.Steps) != 1 || task.Steps[0].AssetID != pool.ID || len(task.ImpactItems) != 0 {
		t.Fatal("pool plan", err, task.Task.Status, task.Task.Blockers, len(task.Steps))
	}
	attempt, err := planner.CreateExecution(t.Context(), cleanup.CreateExecutionRequest{ConnectionID: "connection", CleanupTaskID: task.Task.ID, RequestedBy: "operator", IdempotencyKey: "spark-worker", Confirmation: cleanup.ExecutionConfirmation{Acknowledged: true}})
	if err != nil {
		t.Fatal(err)
	}
	jobs, err := repository.ListJobsByAggregate(t.Context(), "cleanup_task", string(task.Task.ID))
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
		t.Fatal("missing step job")
	}
	resume := func(pending bool) execution.ActionAttempt {
		t.Helper()
		repository, err = sqlite.Open(path, "../../migrations")
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
		worker := cleanup.NewExecutionHandler(cleanup.NewService(repository, registry), cleanup.ActionResolverFunc(func(ctx context.Context, value asset.Asset) (cleanup.ActionDriver, error) {
			return registry.ResolveAction(ctx, value.Identity.ConnectionID, value)
		}))
		err = worker.Handle(t.Context(), job)
		var retry *cleanup.RetryError
		if pending && !errors.As(err, &retry) || !pending && err != nil {
			t.Fatal("durable worker", err)
		}
		current, err := repository.Executions().GetActionByExecutionStep(t.Context(), attempt.ID, string(task.Steps[0].ID))
		if err != nil {
			t.Fatal(err)
		}
		return current
	}
	f.failAfterCancel = true
	current := resume(true)
	if current.ProviderResult["phase"] != "cancel" {
		t.Fatal("cancel acknowledgement not persisted", current)
	}
	f.failAfterCancel = false
	f.readFault = 503
	failedRead := resume(true)
	if failedRead.ProviderResult["binding"] != current.ProviderResult["binding"] {
		t.Fatal("read failure discarded saved acknowledgement")
	}
	f.readFault = 0
	resume(true)
	endSynapseSpark(f.items[synapseBatchType])
	resume(true)
	endSynapseSpark(f.items[synapseSessionType])
	resume(true)
	f.pollDone = true
	current = resume(true)
	if current.ProviderResult["phase"] != "delete" {
		t.Fatal("pool deletion phase not persisted")
	}
	f.poolAbsent = true
	resume(true)
	resume(false)
	for id, count := range f.mutations {
		if count != 1 {
			t.Fatal("worker replayed mutation", id, count)
		}
	}
	active, err := repository.ListActiveAssetsByConnection(t.Context(), "connection", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 3 {
		t.Fatal("independent pool cleanup must retain workspace and both histories", len(active))
	}
	workspaceRetained := false
	for _, row := range active {
		workspaceRetained = workspaceRetained || row.Identity.NativeType == synapseType
	}
	if !workspaceRetained {
		t.Fatal("independent pool cleanup removed workspace")
	}
	for _, value := range values {
		if value.Identity.NativeType == synapseBatchType || value.Identity.NativeType == synapseSessionType {
			found := false
			for _, row := range active {
				found = found || row.ID == value.ID
			}
			if !found {
				t.Fatal("history closed without own absence", value.ID)
			}
		}
	}
	for _, row := range active {
		if row.ID == pool.ID {
			t.Fatal("pool own absence not reconciled")
		}
	}
}

func TestSynapseSparkInventoryReviewRotationAndCursor(t *testing.T) {
	f := newSparkActionFixture(t)
	req := productRequest(f.runtime, synapseSparkType)
	before, err := f.runtime.List(t.Context(), req)
	if err != nil || len(before.Items) != 1 {
		t.Fatal(before, err)
	}
	old := before.Items[0]
	req.KnownNativeIDs = []string{old.NativeID}
	req.KnownNativeMetadata = map[string]map[string]any{old.NativeID: old.Normalized}
	f.secret = "rotated-work-credential"
	f.hidden[synapseBatchType] = true
	after, err := f.runtime.List(t.Context(), req)
	if err != nil || len(after.Items) != 1 || after.Items[0].Normalized[synapseSparkProof] == old.Normalized[synapseSparkProof] || len(object(object(after.Items[0].Normalized[synapseSparkReview])["work"])) != 2 {
		t.Fatal("fresh credential/omitted work reconciliation", after, err)
	}
	base := newSynapseInventoryFixture(t)
	paged := productRequest(base.runtime, synapseSparkType)
	paged.Limit = 1
	page, err := base.runtime.List(t.Context(), paged)
	if err != nil || page.NextCursor == "" {
		t.Fatal(page, err)
	}
	parent := strings.ToLower(resourceID(synapseType, "first"))
	base.objects[parent]["tags"] = map[string]any{"changed": "true"}
	paged.Cursor = page.NextCursor
	if _, err := base.runtime.List(t.Context(), paged); err == nil {
		t.Fatal("review context drift accepted across cursor")
	}
}

func TestSynapseSparkActionRechecksAfterCancel(t *testing.T) {
	for _, fault := range []string{"new consumer", "changed job", "new lock", "changed proof"} {
		t.Run(fault, func(t *testing.T) {
			f := newSparkActionFixture(t)
			req := f.request(t)
			driver, err := f.runtime.ResolveAction(t.Context(), "connection", req.Asset)
			if err != nil {
				t.Fatal(err)
			}
			result, err := driver.Execute(t.Context(), req)
			if err != nil {
				t.Fatal(err)
			}
			endSynapseSpark(f.items[synapseBatchType])
			switch fault {
			case "new consumer":
				f.items[synapseNotebookType] = f.item("Notebook_GetNotebook")
			case "changed job":
				object(f.items[synapseSessionType]["schedulerInfo"])["submittedAt"] = "2026-02-24T09:47:41Z"
			case "changed proof":
				result.Data["operation_done"] = true
			case "new lock":
				previous := f.override
				f.override = func(q *http.Request) (*http.Response, bool) {
					if strings.HasSuffix(strings.ToLower(q.URL.Path), "/providers/microsoft.authorization/locks") {
						return jsonResponse(200, map[string]any{"value": []any{map[string]any{"id": text(f.workspace["id"]) + "/providers/Microsoft.Authorization/locks/lock", "name": "lock", "properties": map[string]any{"level": "CanNotDelete"}}}}, nil), true
					}
					return previous(q)
				}
			}
			if _, err := driver.Wait(t.Context(), req, result); err == nil || len(f.mutations) != 1 {
				t.Fatal("unsafe phase advance", err, f.mutations)
			}
		})
	}
}
