package azure

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/app/cleanup"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/core/graph"
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/internal/persistence/sqlite"
	"github.com/loomx-ai/steward/internal/provider/contracts"
	providerruntime "github.com/loomx-ai/steward/internal/provider/runtime"
)

type workspaceActionFixture struct {
	*synapseDataInventoryFixture
	sql, pipeline                                            map[string]any
	deletes, polls, readFault, pollFault                     int
	gone, poolsGone, metadataGone, pollDone, failAfterDelete bool
}

func newWorkspaceActionFixture(t *testing.T) *workspaceActionFixture {
	f := newSynapseDataInventoryFixture(t)
	native := newSynapseInventoryFixture(t)
	sql := batchClone(native.objects[strings.ToLower(text(f.workspace["id"]))+"/sqlpools/pool"])
	p := f.item("Pipeline_GetPipeline")
	p["properties"] = map[string]any{"activities": []any{pipelineReferenceActivity("SynapseNotebook", "notebook", "NotebookReference", "item")}}
	a := &workspaceActionFixture{synapseDataInventoryFixture: f, sql: sql, pipeline: p}
	previous := f.override
	f.override = func(q *http.Request) (*http.Response, bool) {
		if q.URL.Host == "management.azure.com" {
			path := strings.ToLower(q.URL.Path)
			if q.Method == "DELETE" {
				if path != strings.ToLower(text(f.workspace["id"])) {
					t.Fatal("cascade issued child/external delete", q.URL)
				}
				a.deletes++
				if a.failAfterDelete {
					a.readFault = 503
				}
				h := http.Header{}
				h.Set("Azure-AsyncOperation", apiURL(text(f.workspace["id"])+"/operationStatuses/operation", synapseVersion))
				return jsonResponse(202, nil, h), true
			}
			if a.readFault != 0 && strings.HasPrefix(path, strings.ToLower(text(f.workspace["id"]))) {
				return jsonResponse(a.readFault, nil, nil), true
			}
			if strings.Contains(path, "/operationstatuses/") {
				a.polls++
				if a.pollFault != 0 {
					return jsonResponse(a.pollFault, nil, nil), true
				}
				status := "InProgress"
				if a.pollDone {
					status = "Succeeded"
				}
				return jsonResponse(200, map[string]any{"status": status}, nil), true
			}
			if path == strings.ToLower(text(f.workspace["id"])) && a.gone {
				return jsonResponse(404, nil, nil), true
			}
			if path == strings.ToLower(text(a.sql["id"])) || path == strings.ToLower(text(f.pool["id"])) {
				if a.poolsGone {
					return jsonResponse(404, nil, nil), true
				}
				if path == strings.ToLower(text(a.sql["id"])) {
					return jsonResponse(200, a.sql, nil), true
				}
			}
			if path == strings.ToLower(text(f.workspace["id"]))+"/sqlpools" {
				return jsonResponse(200, map[string]any{"value": []any{a.sql}}, nil), true
			}
		}
		if res, ok := previous(q); ok {
			return res, true
		}
		if q.URL.Host == "management.azure.com" {
			return fleetGraphEmptyIndexes(t, q)
		}
		return nil, false
	}
	data := f.data
	f.data = func(q *http.Request) *http.Response {
		if q.Method != "GET" {
			t.Fatal("unexpected data mutation", q.Method, q.URL)
		}
		if a.metadataGone {
			return jsonResponse(404, map[string]any{"error": map[string]any{"code": "WorkspaceNotFound"}}, nil)
		}
		if q.URL.Path == "/pipelines" {
			return jsonResponse(200, map[string]any{"value": []any{a.pipeline}}, nil)
		}
		if q.URL.Path == "/pipelines/"+text(a.pipeline["name"]) {
			return jsonResponse(200, a.pipeline, nil)
		}
		if strings.HasPrefix(q.URL.Path, "/pipelines/") {
			return jsonResponse(404, nil, nil)
		}
		return data(q)
	}
	return a
}
func (f *workspaceActionFixture) assets(t *testing.T) []asset.Asset {
	t.Helper()
	out := []asset.Asset{}
	for _, kind := range []string{synapseType, synapseSparkType, synapseSQLType, synapseBatchType, synapseSessionType, synapseNotebookType, synapseJobDefinitionType, synapsePipelineType} {
		req := productRequest(f.runtime, kind)
		if synapseDataKind(kind).kind != "" {
			req.Source = synapseDataInventorySource
		}
		batch, err := f.runtime.List(t.Context(), req)
		if err != nil || len(batch.Items) != 1 {
			t.Fatal("native fixture inventory", kind, err, len(batch.Items))
		}
		item := batch.Items[0]
		value := asset.Asset{ID: asset.AssetID(fmt.Sprint("asset-", len(out))), Identity: asset.Identity{Provider: asset.ProviderAzure, Partition: "azure", ConnectionID: "connection", NativeType: kind, NativeID: item.NativeID}, Normalized: item.Normalized, Location: item.Location}
		out = append(out, value)
	}
	return out
}
func workspaceActionRequest(values []asset.Asset) contracts.ActionRequest {
	req := contracts.ActionRequest{Asset: values[0], Action: "delete", IdempotencyKey: "workspace-delete"}
	for _, value := range values[1:] {
		req.LifecycleImpacts = append(req.LifecycleImpacts, contracts.ActionImpact{Asset: value, ControllerID: req.Asset.ID, Delete: true})
	}
	return req
}
func TestSynapseWorkspaceActionDurableCascade(t *testing.T) {
	f := newWorkspaceActionFixture(t)
	values := f.assets(t)
	req := workspaceActionRequest(values)
	c, err := f.runtime.resolve(t.Context(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	contribution, err := c.synapseWorkspaceContribution(req.Asset, values)
	if err != nil || len(contribution.Bindings) != 7 || len(contribution.Unresolved) != 0 {
		t.Fatal(contribution, err)
	}
	missing, err := c.synapseWorkspaceContribution(req.Asset, values[:len(values)-1])
	if err != nil || len(missing.Unresolved) != 1 || !missing.Unresolved[0].BlocksCleanup {
		t.Fatal("unscanned member did not block cascade", missing, err)
	}
	for _, binding := range contribution.Bindings {
		if binding.ControllerAssetID != req.Asset.ID || binding.Ownership != graph.OwnershipExclusive || binding.CleanupPolicy != graph.CleanupDelegate {
			t.Fatal(binding)
		}
	}
	driver, err := f.runtime.ResolveAction(t.Context(), "connection", req.Asset)
	if err != nil {
		t.Fatal(err)
	}
	f.failAfterDelete = true
	result, err := driver.Execute(t.Context(), req)
	if err != nil || f.deletes != 1 {
		t.Fatal("ack not returned before reads", result, err)
	}
	wire, _ := json.Marshal(result)
	if json.Unmarshal(wire, &result) != nil {
		t.Fatal("receipt restore")
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
	if _, err = driver.Execute(t.Context(), req); f.deletes != 1 {
		t.Fatal("replayed root DELETE", err)
	}
	if _, err = driver.Wait(t.Context(), req, result); err == nil {
		t.Fatal("read failure ignored")
	}
	f.readFault = 0
	f.pollDone = true
	wait, err := driver.Wait(t.Context(), req, result)
	if err != nil || wait.Done || wait.Data["operation_done"] != true {
		t.Fatal("terminal operation not checkpointed", wait, err)
	}
	result.Data = wait.Data
	req.ExecutionResult = &result
	f.gone = true
	wait, err = driver.Wait(t.Context(), req, result)
	if err != nil || wait.Done {
		t.Fatal("root disappearance erased live pools", wait, err)
	}
	f.poolsGone = true
	wait, err = driver.Wait(t.Context(), req, result)
	if err != nil || !wait.Done || f.deletes != 1 {
		t.Fatal(wait, err)
	}
	f.gone = false
	object(f.workspace["properties"])["workspaceUID"] = "new-workspace-incarnation"
	if _, err = driver.Readback(t.Context(), req); err == nil {
		t.Fatal("new workspace closed by old receipt")
	}
}
func TestSynapseWorkspaceActionRejectsUnreviewedChanges(t *testing.T) {
	for _, fault := range []string{"retained member", "missing impact", "changed impact", "new pipeline", "changed job", "protected member", "changed workspace", "missing creation", "lock", "receipt"} {
		t.Run(fault, func(t *testing.T) {
			f := newWorkspaceActionFixture(t)
			req := workspaceActionRequest(f.assets(t))
			driver, err := f.runtime.ResolveAction(t.Context(), "connection", req.Asset)
			if err != nil {
				t.Fatal(err)
			}
			switch fault {
			case "retained member":
				req.LifecycleImpacts[0].Delete = false
			case "missing impact":
				req.LifecycleImpacts = req.LifecycleImpacts[1:]
			case "changed impact":
				req.LifecycleImpacts[0].Asset.Normalized["_synapse_private_configuration"] = "changed"
			case "new pipeline":
				f.pipeline["name"] = "different"
				f.pipeline["id"] = strings.TrimSuffix(text(f.pipeline["id"]), "item") + "different"
			case "changed job":
				object(f.items[synapseBatchType]["livyInfo"])["jobCreationRequest"] = map[string]any{"args": []any{"changed"}}
			case "protected member":
				f.items[synapseNotebookType]["tags"] = map[string]any{"steward:protected": "true"}
			case "changed workspace":
				object(f.workspace["properties"])["workspaceUID"] = "recreated"
			case "missing creation":
				delete(object(f.sql["properties"]), "creationDate")
			case "lock":
				f.intercept = func(q *http.Request) (*http.Response, bool) {
					if strings.Contains(strings.ToLower(q.URL.Path), "/microsoft.authorization/locks") {
						return jsonResponse(200, map[string]any{"value": []any{map[string]any{"id": text(f.sql["id"]) + "/providers/Microsoft.Authorization/locks/keep", "properties": map[string]any{"level": "CanNotDelete"}}}}, nil), true
					}
					return nil, false
				}
			case "receipt":
				req.ExecutionResult = &contracts.ActionResult{Data: map[string]any{"binding": "forged"}}
			}
			if _, err = driver.Execute(t.Context(), req); err == nil || f.deletes != 0 {
				t.Fatal("unsafe root mutation", fault, err, f.deletes)
			}
		})
	}
}
func TestSynapseWorkspaceExpiredCallbackNeedsIndependentScopeAbsence(t *testing.T) {
	f := newWorkspaceActionFixture(t)
	req := workspaceActionRequest(f.assets(t))
	driver, err := f.runtime.ResolveAction(t.Context(), "connection", req.Asset)
	if err != nil {
		t.Fatal(err)
	}
	result, err := driver.Execute(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	f.pollFault = 404
	f.gone = true
	f.poolsGone = true
	if out, err := driver.Wait(t.Context(), req, result); err == nil || out.Done {
		t.Fatal("expired callback erased surviving metadata", out, err)
	}
	f.metadataGone = true
	out, err := driver.Wait(t.Context(), req, result)
	if err != nil || !out.Done || out.Data["operation_done"] == true {
		t.Fatal("independent scope reads not honored", out, err)
	}
}

func TestSynapseWorkspaceCleanupWorkerAndRetention(t *testing.T) {
	f := newWorkspaceActionFixture(t)
	repo, registry, path := azureNativeWorkerRepository(t, f.runtime)
	azureNativeWorkerScan(t, f.runtime, synapseSource, repo, registry, []string{synapseType, synapseSparkType, synapseSQLType}, false, false)
	values := azureNativeWorkerScan(t, f.runtime, synapseDataInventorySource, repo, registry, []string{synapseBatchType, synapseSessionType, synapseNotebookType, synapseJobDefinitionType, synapsePipelineType}, false, true)
	if len(values) != 8 {
		t.Fatal("incomplete persisted boundary", len(values))
	}
	var root, notebook asset.Asset
	for _, v := range values {
		if v.Identity.NativeType == synapseType {
			root = v
		}
		if v.Identity.NativeType == synapseNotebookType {
			notebook = v
		}
	}
	if root.ID == "" || notebook.ID == "" {
		t.Fatal("missing persisted identities")
	}
	planner := cleanup.NewService(repo, registry)
	onlyNotebook, err := planner.CreateTask(t.Context(), cleanup.CreateTaskRequest{ConnectionID: "connection", Selectors: []plan.CleanupSelector{{Kind: plan.SelectorAsset, AssetID: notebook.ID}}, CreatedBy: "operator"})
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range onlyNotebook.Steps {
		if step.AssetID == root.ID {
			t.Fatal("child selection silently selected workspace")
		}
	}
	selector := []plan.CleanupSelector{{Kind: plan.SelectorAsset, AssetID: root.ID}}
	for _, options := range []map[string]any{{"retain_all_resources": true}, {"retain_resources": []string{string(notebook.ID)}}} {
		retained, err := planner.CreateTask(t.Context(), cleanup.CreateTaskRequest{ConnectionID: "connection", Selectors: selector, CreatedBy: "operator", RequestOptions: map[asset.AssetID]map[string]any{root.ID: options}})
		if err != nil || len(retained.Task.Blockers) == 0 {
			t.Fatal("unsupported retention permitted workspace cascade", retained, err)
		}
	}
	task, err := planner.CreateTask(t.Context(), cleanup.CreateTaskRequest{ConnectionID: "connection", Selectors: selector, CreatedBy: "operator"})
	if err != nil || task.Task.Status != plan.StatusReady || len(task.Steps) != 1 || len(task.ImpactItems) != 7 {
		t.Fatal("workspace cascade plan", task, err)
	}
	warning := false
	for _, v := range task.Task.Warnings {
		if v.Code == plan.WarningSynapseWorkspaceDelete {
			warning = true
			if !strings.Contains(v.Message, "this operation does not purge them") || strings.Contains(v.Message, "permanently") {
				t.Fatal("workspace removal falsely promises backup purge", v.Message)
			}
		}
	}
	if !warning {
		t.Fatal("destructive scope warning missing")
	}
	attempt, err := planner.CreateExecution(t.Context(), cleanup.CreateExecutionRequest{ConnectionID: "connection", CleanupTaskID: task.Task.ID, RequestedBy: "operator", IdempotencyKey: "workspace-worker", Confirmation: cleanup.ExecutionConfirmation{Acknowledged: true}})
	if err != nil {
		t.Fatal(err)
	}
	jobs, err := repo.ListJobsByAggregate(t.Context(), "cleanup_task", string(task.Task.ID))
	if err != nil {
		t.Fatal(err)
	}
	var job execution.Job
	for _, candidate := range jobs {
		if text(candidate.Payload["cleanup_task_step_id"]) == string(task.Steps[0].ID) {
			job = candidate
		}
	}
	if job.ID == "" {
		t.Fatal("missing controller job")
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
		worker := cleanup.NewExecutionHandler(cleanup.NewService(repo, registry), cleanup.ActionResolverFunc(func(ctx context.Context, value asset.Asset) (cleanup.ActionDriver, error) {
			return registry.ResolveAction(ctx, value.Identity.ConnectionID, value)
		}))
		err = worker.Handle(t.Context(), job)
		var retry *cleanup.RetryError
		if pending && !errors.As(err, &retry) || !pending && err != nil {
			t.Fatal("restart", err)
		}
		current, err := repo.Executions().GetActionByExecutionStep(t.Context(), attempt.ID, string(task.Steps[0].ID))
		if err != nil {
			t.Fatal(err)
		}
		return current
	}
	f.failAfterDelete = true
	first := resume(true)
	if first.ProviderResult["binding"] == nil || f.deletes != 1 {
		t.Fatal("native acknowledgement not saved", first)
	}
	again := resume(true)
	if again.ProviderResult["binding"] != first.ProviderResult["binding"] {
		t.Fatal("failed read lost receipt")
	}
	f.readFault = 0
	f.pollDone = true
	terminal := resume(true)
	if terminal.ProviderResult["operation_done"] != true {
		t.Fatal("terminal result not checkpointed", terminal)
	}
	f.gone = true
	resume(true)
	f.poolsGone = true
	resume(true)
	resume(false)
	if f.deletes != 1 {
		t.Fatal("workspace delete replayed")
	}
	active, err := repo.ListActiveAssetsByConnection(t.Context(), "connection", "")
	if err != nil || len(active) != 0 {
		t.Fatal("verified scope not reconciled", len(active), err)
	}
}
