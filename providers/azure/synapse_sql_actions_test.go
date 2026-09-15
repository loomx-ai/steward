package azure

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
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

type sqlActionFixture struct {
	*workspaceActionFixture
	sqlGone      bool
	link         map[string]any
	linkFault    int
	linksPaged   bool
	hideLinkList bool
}

func newSQLActionFixture(t *testing.T) *sqlActionFixture {
	f := &sqlActionFixture{workspaceActionFixture: newWorkspaceActionFixture(t)}
	previous := f.override
	f.override = func(q *http.Request) (*http.Response, bool) {
		id := strings.ToLower(text(f.sql["id"]))
		path := strings.ToLower(q.URL.Path)
		if q.URL.Host == "management.azure.com" {
			if q.Method == "DELETE" {
				if path != id {
					t.Fatal("SQL action mutated another resource", q.URL)
				}
				f.deletes++
				if f.failAfterDelete {
					f.readFault = 503
				}
				h := http.Header{}
				h.Set("Azure-AsyncOperation", apiURL(text(f.workspace["id"])+"/operationStatuses/operation", synapseVersion))
				return jsonResponse(202, nil, h), true
			}
			if path == id && f.sqlGone {
				return jsonResponse(404, nil, nil), true
			}
			if path == id+"/replicationlinks" || strings.HasPrefix(path, id+"/replicationlinks/") {
				if f.linkFault != 0 {
					return jsonResponse(f.linkFault, nil, nil), true
				}
				if path == id+"/replicationlinks" {
					if f.linksPaged && q.URL.Query().Get("$skiptoken") == "" {
						return jsonResponse(200, map[string]any{"value": []any{}, "nextLink": apiURL(id+"/replicationLinks", synapseVersion) + "&$skiptoken=second"}, nil), true
					}
					values := []any{}
					if f.link != nil && !f.hideLinkList {
						values = append(values, f.link)
					}
					return jsonResponse(200, map[string]any{"value": values}, nil), true
				}
				if f.link != nil {
					return jsonResponse(200, f.link, nil), true
				}
				return jsonResponse(404, nil, nil), true
			}
		}
		return previous(q)
	}
	return f
}
func (f *sqlActionFixture) replication(t *testing.T) {
	t.Helper()
	payload, err := os.ReadFile("fixtures/synapse/ListSqlPoolReplicationLinks.json")
	var native map[string]any
	if err != nil || json.Unmarshal(payload, &native) != nil {
		t.Fatal(err)
	}
	f.link = object(object(object(object(native["responses"])["200"])["body"])["value"].([]any)[0])
	f.link["id"] = text(f.sql["id"]) + "/replicationLinks/" + text(f.link["name"])
}
func (f *sqlActionFixture) request(t *testing.T) contracts.ActionRequest {
	return contracts.ActionRequest{Asset: f.assets(t)[2], Action: "delete", IdempotencyKey: "sql-delete"}
}
func TestSynapseSQLDurableAction(t *testing.T) {
	f := newSQLActionFixture(t)
	req := f.request(t)
	driver, err := f.runtime.ResolveAction(t.Context(), "connection", req.Asset)
	if err != nil {
		t.Fatal(err)
	}
	f.failAfterDelete = true
	result, err := driver.Execute(t.Context(), req)
	if err != nil || f.deletes != 1 || result.Data["binding"] == nil {
		t.Fatal("ack lost", result, err)
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
	if _, err = driver.Wait(t.Context(), req, result); err == nil {
		t.Fatal("failure hidden")
	}
	f.readFault = 0
	if _, err = driver.Execute(t.Context(), req); err != nil || f.deletes != 1 {
		t.Fatal("delete replay", err)
	}
	f.pollDone = true
	wait, err := driver.Wait(t.Context(), req, result)
	if err != nil || wait.Done || wait.Data["operation_done"] != true {
		t.Fatal("terminal checkpoint", wait, err)
	}
	result.Data = wait.Data
	req.ExecutionResult = &result
	wait, err = driver.Wait(t.Context(), req, result)
	if err != nil || wait.Done {
		t.Fatal("live pool erased", wait, err)
	}
	f.sqlGone = true
	wait, err = driver.Wait(t.Context(), req, result)
	if err != nil || !wait.Done {
		t.Fatal(wait, err)
	}
	f.sqlGone = false
	object(f.sql["properties"])["creationDate"] = "2026-09-15T00:00:00Z"
	if _, err = driver.Readback(t.Context(), req); err == nil {
		t.Fatal("recreated pool accepted")
	}
}
func TestSynapseSQLRejectsChangedScope(t *testing.T) {
	for _, fault := range []string{"replication", "later page replication", "permission", "dependency missing", "workspace missing", "group missing", "recreated", "configuration", "protected", "workspace protected", "lock", "state", "review", "receipt", "impact", "parameter", "foreign identity"} {
		t.Run(fault, func(t *testing.T) {
			f := newSQLActionFixture(t)
			req := f.request(t)
			driver, err := f.runtime.ResolveAction(t.Context(), "connection", req.Asset)
			if err != nil {
				t.Fatal(err)
			}
			switch fault {
			case "replication", "later page replication":
				f.replication(t)
				f.linksPaged = fault == "later page replication"
			case "permission":
				f.linkFault = 403
			case "dependency missing":
				f.linkFault = 404
			case "workspace missing":
				f.gone = true
			case "group missing":
				previous := f.override
				f.override = func(q *http.Request) (*http.Response, bool) {
					if strings.EqualFold(q.URL.Path, "/subscriptions/"+testSubscription+"/resourcegroups/test") {
						return jsonResponse(404, nil, nil), true
					}
					return previous(q)
				}
			case "recreated":
				object(f.sql["properties"])["creationDate"] = "2026-09-15T00:00:00Z"
			case "configuration":
				object(f.sql["properties"])["collation"] = "different"
			case "protected":
				f.sql["tags"] = map[string]any{"steward:protected": "true"}
			case "workspace protected":
				f.workspace["tags"] = map[string]any{"steward:protected": "true"}
			case "lock":
				previous := f.override
				f.override = func(q *http.Request) (*http.Response, bool) {
					if strings.Contains(strings.ToLower(q.URL.Path), "/microsoft.authorization/locks") {
						return jsonResponse(200, map[string]any{"value": []any{map[string]any{"id": text(f.sql["id"]) + "/providers/Microsoft.Authorization/locks/keep", "properties": map[string]any{"level": "CanNotDelete"}}}}, nil), true
					}
					return previous(q)
				}
			case "state":
				object(f.sql["properties"])["status"] = "Scaling"
			case "review":
				req.Asset.Normalized[synapseSQLProof] = "tampered"
			case "receipt":
				req.ExecutionResult = &contracts.ActionResult{Data: map[string]any{"binding": "tampered"}}
			case "impact":
				req.LifecycleImpacts = []contracts.ActionImpact{{Asset: req.Asset, Delete: true}}
			case "parameter":
				req.Parameters = map[string]any{"force_delete": true}
			case "foreign identity":
				req.Asset.Identity.NativeID = strings.Replace(req.Asset.Identity.NativeID, testSubscription, "different", 1)
			}
			if _, err = driver.Execute(t.Context(), req); err == nil || f.deletes != 0 {
				t.Fatal("changed scope allowed", err, f.deletes)
			}
		})
	}
}
func TestSynapseSQLExpiredCallbackOwnAbsence(t *testing.T) {
	f := newSQLActionFixture(t)
	req := f.request(t)
	driver, err := f.runtime.ResolveAction(t.Context(), "connection", req.Asset)
	if err != nil {
		t.Fatal(err)
	}
	result, err := driver.Execute(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	req.ExecutionResult = &result
	f.pollFault = 404
	if wait, err := driver.Wait(t.Context(), req, result); err == nil || wait.Done {
		t.Fatal("callback 404 erased live pool", wait, err)
	}
	f.sqlGone = true
	if wait, err := driver.Wait(t.Context(), req, result); err != nil || !wait.Done {
		t.Fatal(wait, err)
	}
}
func TestSynapseSQLInventoryProtection(t *testing.T) {
	for _, mode := range []string{"paused", "replication", "missing creation"} {
		t.Run(mode, func(t *testing.T) {
			f := newSQLActionFixture(t)
			switch mode {
			case "paused":
				object(f.sql["properties"])["status"] = "Paused"
			case "replication":
				f.replication(t)
				f.linksPaged = true
			case "missing creation":
				delete(object(f.sql["properties"]), "creationDate")
			}
			batch, err := f.runtime.List(t.Context(), productRequest(f.runtime, synapseSQLType))
			if err != nil || len(batch.Items) != 1 {
				t.Fatal(err, len(batch.Items))
			}
			if batch.Items[0].Actionable == nil || *batch.Items[0].Actionable != (mode == "paused") {
				t.Fatal("readiness", batch.Items[0])
			}
		})
	}
}

func TestSynapseSQLCleanupWorkerKeepsWorkspace(t *testing.T) {
	for _, completeGraph := range []bool{false, true} {
		name := "partial-inventory"
		if completeGraph {
			name = "complete-workspace-graph"
		}
		t.Run(name, func(t *testing.T) { synapseSQLCleanupWorkerKeepsWorkspace(t, completeGraph) })
	}
}
func synapseSQLCleanupWorkerKeepsWorkspace(t *testing.T, completeGraph bool) {
	f := newSQLActionFixture(t)
	repo, registry, path := azureNativeWorkerRepository(t, f.runtime)
	azureNativeWorkerScan(t, f.runtime, synapseSource, repo, registry, []string{synapseType, synapseSparkType, synapseSQLType}, false, false)
	expected := 3
	if completeGraph {
		azureNativeWorkerScan(t, f.runtime, synapseDataInventorySource, repo, registry, []string{synapseBatchType, synapseSessionType, synapseNotebookType, synapseJobDefinitionType, synapsePipelineType}, false, true)
		expected = 8
	}
	// A direct SQL selection must retain its workspace and siblings, whether
	// workspace lifecycle membership has been contributed or is still unscanned.
	values, err := repo.ListActiveAssetsByConnection(t.Context(), "connection", "")
	if err != nil || len(values) != expected {
		t.Fatal(err, len(values))
	}
	var sql asset.Asset
	for _, v := range values {
		if v.Identity.NativeType == synapseSQLType {
			sql = v
		}
	}
	planner := cleanup.NewService(repo, registry)
	task, err := planner.CreateTask(t.Context(), cleanup.CreateTaskRequest{ConnectionID: "connection", Selectors: []plan.CleanupSelector{{Kind: plan.SelectorAsset, AssetID: sql.ID}}, CreatedBy: "operator"})
	if err != nil || task.Task.Status != plan.StatusReady || len(task.Steps) != 1 || task.Steps[0].AssetID != sql.ID || len(task.ImpactItems) != 0 {
		t.Fatal("pool-only plan", task, err)
	}
	warning := false
	for _, v := range task.Task.Warnings {
		if v.Code == plan.WarningSynapseSQLDelete {
			warning = strings.Contains(v.Message, "does not purge") && strings.Contains(v.Message, "other pools are retained")
		}
	}
	if !warning {
		t.Fatal("SQL scope warning missing")
	}
	attempt, err := planner.CreateExecution(t.Context(), cleanup.CreateExecutionRequest{ConnectionID: "connection", CleanupTaskID: task.Task.ID, RequestedBy: "operator", IdempotencyKey: "sql-worker", Confirmation: cleanup.ExecutionConfirmation{Acknowledged: true}})
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
		t.Fatal("missing job", jobs)
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
		t.Fatal("ack not saved", first)
	}
	again := resume(true)
	if first.ProviderResult["binding"] != again.ProviderResult["binding"] {
		t.Fatal("receipt lost")
	}
	f.readFault = 0
	f.pollDone = true
	terminal := resume(true)
	if terminal.ProviderResult["operation_done"] != true {
		t.Fatal("terminal checkpoint", terminal)
	}
	resume(true)
	f.sqlGone = true
	resume(true)
	resume(false)
	active, err := repo.ListActiveAssetsByConnection(t.Context(), "connection", "")
	if err != nil || len(active) != expected-1 || f.deletes != 1 {
		t.Fatal("scope", active, err, f.deletes)
	}
	for _, v := range active {
		if v.Identity.NativeType == synapseSQLType {
			t.Fatal("SQL still active")
		}
	}
}

func TestSynapseSQLKnownReplicationOwnReads(t *testing.T) {
	f := newSQLActionFixture(t)
	f.replication(t)
	req := productRequest(f.runtime, synapseSQLType)
	first, err := f.runtime.List(t.Context(), req)
	if err != nil || len(first.Items) != 1 {
		t.Fatal(err)
	}
	item := first.Items[0]
	req.KnownNativeIDs = []string{item.NativeID}
	req.KnownNativeMetadata = map[string]map[string]any{item.NativeID: item.Normalized}
	f.hideLinkList = true
	observed, err := f.runtime.List(t.Context(), req)
	if err != nil || *observed.Items[0].Actionable {
		t.Fatal("omitted live link forgotten", err)
	}
	f.link = nil
	observed, err = f.runtime.List(t.Context(), req)
	if err != nil || !*observed.Items[0].Actionable {
		t.Fatal("own absence did not release link", err)
	}
	review := batchClone(object(item.Normalized[synapseSQLReview]))
	review["links"] = map[string]any{"/subscriptions/foreign/resourcegroups/group/providers/microsoft.synapse/workspaces/foreign/sqlpools/pool/replicationlinks/link": "hint"}
	req.KnownNativeMetadata[item.NativeID] = map[string]any{synapseSQLReview: review}
	if _, err = f.runtime.List(t.Context(), req); err == nil {
		t.Fatal("foreign replication hint accepted")
	}
}

func TestSynapseSQLReplicationBlocksWorkspace(t *testing.T) {
	f := newSQLActionFixture(t)
	values := f.assets(t)
	req := workspaceActionRequest(values)
	driver, err := f.runtime.ResolveAction(t.Context(), "connection", req.Asset)
	if err != nil {
		t.Fatal(err)
	}
	f.replication(t)
	f.linksPaged = true
	if _, err = driver.Execute(t.Context(), req); err == nil || f.deletes != 0 {
		t.Fatal("new SQL peer ignored by workspace cascade", err)
	}
	batch, err := f.runtime.List(t.Context(), productRequest(f.runtime, synapseType))
	if err != nil || len(batch.Items) != 1 || *batch.Items[0].Actionable {
		t.Fatal("replicated workspace actionable", err)
	}
}

func TestSynapseSQLReplicationNativeIdentityCase(t *testing.T) {
	f := newSQLActionFixture(t)
	f.replication(t)
	previous := f.override
	f.override = func(q *http.Request) (*http.Response, bool) {
		if strings.Contains(strings.ToLower(q.URL.Path), "/replicationlinks/") {
			raw := batchClone(f.link)
			for _, key := range []string{"id", "name", "type"} {
				raw[key] = strings.ToUpper(text(raw[key]))
			}
			return jsonResponse(200, raw, nil), true
		}
		return previous(q)
	}
	batch, err := f.runtime.List(t.Context(), productRequest(f.runtime, synapseSQLType))
	if err != nil || len(batch.Items) != 1 || *batch.Items[0].Actionable {
		t.Fatal("native identity case lost protection", err)
	}
}
