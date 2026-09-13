package azure

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/loomx-ai/steward/internal/app/cleanup"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/internal/persistence/sqlite"
	"github.com/loomx-ai/steward/internal/provider/contracts"
	providerruntime "github.com/loomx-ai/steward/internal/provider/runtime"
)

func elasticSanTestPollURL() string {
	return apiURL("/subscriptions/"+testSubscription+"/providers/Microsoft.ElasticSan/locations/eastus/asyncoperations/"+testTenant, elasticSanVersion) + "&monitor=true&t=private-elastic-signature&c=opaque&s=opaque&h=opaque"
}

type elasticSanCleanupFixture struct {
	*elasticSanFixture
	deletes, polls int
	pollStatus     int
	locks          []any
	hook           func(*http.Request) (*http.Response, bool)
}

func newElasticSanCleanupFixture(t *testing.T) *elasticSanCleanupFixture {
	t.Helper()
	f := &elasticSanCleanupFixture{elasticSanFixture: newElasticSanFixture(t), pollStatus: 200, locks: []any{}}
	f.override = func(req *http.Request) (*http.Response, bool) {
		if f.hook != nil {
			if response, handled := f.hook(req); handled {
				return response, true
			}
		}
		path := strings.ToLower(req.URL.Path)
		if req.URL.String() == elasticSanTestPollURL() {
			if req.Method != "GET" {
				t.Fatal("unexpected polling mutation")
			}
			f.polls++
			return jsonResponse(f.pollStatus, map[string]any{}, nil), true
		}
		if req.Method == "DELETE" {
			if path != f.ids[elasticSanSnapshotType] || req.URL.Query().Get("api-version") != elasticSanVersion || len(req.URL.Query()) != 1 || req.Header.Get("x-ms-force-delete") != "" || req.Header.Get("x-ms-delete-snapshots") != "" || req.ContentLength != 0 {
				t.Fatal("snapshot cleanup broadened its native mutation")
			}
			f.deletes++
			raw := f.values[path]
			object(raw["properties"])["provisioningState"] = "Deleting"
			return jsonResponse(202, raw, http.Header{"Location": {elasticSanTestPollURL()}, "X-Ms-Request-Id": {"elastic-delete"}}), true
		}
		groupID := "/subscriptions/" + testSubscription + "/resourcegroups/test"
		if req.Method == "GET" && path == groupID {
			return jsonResponse(200, map[string]any{"id": groupID, "name": "test", "type": groupType, "location": "eastus", "properties": map[string]any{}}, nil), true
		}
		if req.Method == "GET" && path == "/subscriptions/"+testSubscription+"/providers/microsoft.authorization/locks" {
			return jsonResponse(200, map[string]any{"value": f.locks}, nil), true
		}
		return nil, false
	}
	return f
}

func (f *elasticSanCleanupFixture) action(t *testing.T) (contracts.ActionDriver, contracts.ActionRequest) {
	t.Helper()
	batch, err := f.runtime.List(t.Context(), f.request(elasticSanSnapshotType))
	if err != nil || len(batch.Items) != 1 {
		t.Fatal("snapshot inventory", batch, err)
	}
	value := elasticSanTestAsset(batch.Items[0])
	driver, err := f.runtime.ResolveAction(t.Context(), "connection", value)
	if err != nil {
		t.Fatal(err)
	}
	guard, ok := driver.(*monitorTargetAction)
	if !ok {
		t.Fatal("missing monitor prerequisite guard")
	}
	if _, ok := guard.inner.(*elasticSanSnapshotAction); !ok {
		t.Fatal("missing native snapshot driver")
	}
	return driver, contracts.ActionRequest{Asset: value, Action: "delete", IdempotencyKey: "elastic-snapshot-delete"}
}

func TestElasticSanSnapshotDeleteAndIndependentAbsence(t *testing.T) {
	f := newElasticSanCleanupFixture(t)
	a, request := f.action(t)
	result, err := a.Execute(t.Context(), request)
	if err != nil || f.deletes != 1 || result.ProviderOperationID != "eastus/"+testTenant || strings.Contains(result.ProviderOperationID, "private-elastic-") {
		t.Fatal("native delete receipt", result.ProviderOperationID, f.deletes, err)
	}
	wait, err := a.Wait(t.Context(), request, result)
	if err != nil || wait.Done || f.polls != 1 {
		t.Fatal("operation success closed a live snapshot", wait, err)
	}
	result.Data = wait.Data
	request.ExecutionResult = &result
	if _, err := a.Execute(t.Context(), request); err != nil || f.deletes != 1 {
		t.Fatal("restored execution repeated DELETE", err)
	}
	delete(f.values, f.ids[elasticSanSnapshotType])
	wait, err = a.Wait(t.Context(), request, result)
	if err != nil || !wait.Done || f.polls != 1 {
		t.Fatal("completed receipt or independent own absence lost", wait, err)
	}
	if f.values[f.ids[elasticSanVolumeType]] == nil || f.values[f.ids[elasticSanGroupType]] == nil {
		t.Fatal("snapshot cleanup removed its source or parent")
	}
}

func TestElasticSanSnapshotPreflightProtectionAndRecovery(t *testing.T) {
	for _, change := range []string{"created", "configuration", "etag", "parent-region", "parent-tag", "lock", "forbidden", "parameter", "proof", "missing-parent", "already-deleting", "already-absent"} {
		t.Run(change, func(t *testing.T) {
			f := newElasticSanCleanupFixture(t)
			a, request := f.action(t)
			raw := f.values[f.ids[elasticSanSnapshotType]]
			waiting, absent := false, false
			switch change {
			case "created":
				object(raw["systemData"])["createdAt"] = "2026-03-11T09:51:01Z"
			case "configuration":
				object(raw["properties"])["futureSetting"] = "changed"
			case "etag":
				raw["etag"] = "new-generation"
			case "parent-region":
				f.values[f.ids[elasticSanType]]["location"] = "westus"
			case "parent-tag":
				f.values[f.ids[elasticSanType]]["tags"] = map[string]any{"steward:protected": "true"}
			case "lock":
				f.locks = []any{map[string]any{"id": f.ids[elasticSanType] + "/providers/Microsoft.Authorization/locks/protect", "properties": map[string]any{"level": "CanNotDelete"}}}
			case "forbidden":
				f.hook = func(req *http.Request) (*http.Response, bool) {
					if strings.EqualFold(req.URL.Path, f.ids[elasticSanSnapshotType]) {
						return jsonResponse(403, map[string]any{"error": map[string]any{"code": "AuthorizationFailed"}}, nil), true
					}
					return nil, false
				}
			case "parameter":
				request.Parameters = map[string]any{"x-ms-delete-snapshots": "true"}
			case "proof":
				request.Asset.Normalized = maps.Clone(request.Asset.Normalized)
				request.Asset.Normalized[elasticSanSnapshotCleanupProof] = "forged"
			case "missing-parent":
				delete(f.values, f.ids[elasticSanGroupType])
				waiting = true
			case "already-deleting":
				object(raw["properties"])["provisioningState"] = "Deleting"
				waiting = true
			case "already-absent":
				delete(f.values, f.ids[elasticSanSnapshotType])
				absent = true
			}
			check, err := a.Preflight(t.Context(), request)
			if waiting || absent {
				if err != nil || !check.Allowed || check.Absent != absent || waiting && check.Evidence["elastic_san_wait"] != true {
					t.Fatal("native recovery state", check, err)
				}
				result, err := a.Execute(t.Context(), request)
				if err != nil {
					t.Fatal(err)
				}
				wait, err := a.Wait(t.Context(), request, result)
				if err != nil || wait.Done != absent {
					t.Fatal("missing parent became snapshot absence", wait, err)
				}
			} else if err == nil {
				t.Fatal("unsafe preflight accepted", change)
			}
			if f.deletes != 0 {
				t.Fatal("preflight recovery or failure mutated snapshot")
			}
		})
	}
}

func TestElasticSanSnapshotExpiredCallbackNeedsOwnAbsence(t *testing.T) {
	f := newElasticSanCleanupFixture(t)
	a, request := f.action(t)
	result, err := a.Execute(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	f.pollStatus = 404
	if wait, err := a.Wait(t.Context(), request, result); err == nil || wait.Done {
		t.Fatal("operation 404 erased a live snapshot")
	}
	delete(f.values, f.ids[elasticSanType])
	delete(f.values, f.ids[elasticSanGroupType])
	if wait, err := a.Wait(t.Context(), request, result); err == nil || wait.Done {
		t.Fatal("missing parents erased a live snapshot")
	}
	delete(f.values, f.ids[elasticSanSnapshotType])
	if wait, err := a.Wait(t.Context(), request, result); err != nil || !wait.Done {
		t.Fatal("independent snapshot absence could not finish expired operation", wait, err)
	}
}

func TestElasticSanSnapshotRecreatedDuringPollingIsNotClosed(t *testing.T) {
	f := newElasticSanCleanupFixture(t)
	a, request := f.action(t)
	result, err := a.Execute(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	object(f.values[f.ids[elasticSanSnapshotType]]["systemData"])["createdAt"] = "2026-04-11T09:51:01Z"
	object(f.values[f.ids[elasticSanSnapshotType]]["properties"])["provisioningState"] = "Succeeded"
	if wait, err := a.Wait(t.Context(), request, result); err == nil || wait.Done || f.deletes != 1 {
		t.Fatal("same-name replacement closed or received another DELETE")
	}
}

func TestElasticSanSnapshotUnverifiedCreationAndReceiptTampering(t *testing.T) {
	f := newElasticSanCleanupFixture(t)
	delete(f.values[f.ids[elasticSanSnapshotType]], "systemData")
	batch, err := f.runtime.List(t.Context(), f.request(elasticSanSnapshotType))
	if err != nil || len(batch.Items) != 1 || batch.Items[0].Actionable == nil || *batch.Items[0].Actionable {
		t.Fatal("snapshot without creation identity became actionable", err)
	}
	if _, err := f.runtime.ResolveAction(t.Context(), "connection", elasticSanTestAsset(batch.Items[0])); err == nil {
		t.Fatal("unverified snapshot gained cleanup authority")
	}
	f = newElasticSanCleanupFixture(t)
	a, request := f.action(t)
	result, err := a.Execute(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"operation", "binding", "phase", "origin"} {
		changed := result
		changed.Data = maps.Clone(result.Data)
		if field == "origin" {
			changed.ProviderOperationID = "other-operation"
		} else {
			changed.Data[field] = "forged"
		}
		if wait, err := a.Wait(t.Context(), request, changed); err == nil || wait.Done {
			t.Fatal("tampered action receipt", field)
		}
	}
	if f.polls != 0 || f.deletes != 1 {
		t.Fatal("tampering reached polling or repeated mutation")
	}
}

func TestElasticSanSnapshotSQLiteCleanupRestart(t *testing.T) {
	f := newElasticSanCleanupFixture(t)
	logs := []execution.JobLogEntry{}
	ctx := execution.WithJobLogSink(t.Context(), execution.JobLogSinkFunc(func(_ context.Context, entry execution.JobLogEntry) { logs = append(logs, entry) }))
	repo, registry, path := azureNativeWorkerRepository(t, f.runtime)
	values := azureNativeWorkerScan(t, f.runtime, elasticSanSource, repo, registry, []string{elasticSanSnapshotType, elasticSanVolumeType, elasticSanGroupType, elasticSanType}, false, true)
	var snapshot asset.Asset
	for _, value := range values {
		if value.Identity.NativeType == elasticSanSnapshotType {
			snapshot = value
		}
	}
	service := cleanup.NewService(repo, registry)
	task, err := service.CreateTask(ctx, cleanup.CreateTaskRequest{ConnectionID: "connection", Selectors: []plan.CleanupSelector{{Kind: plan.SelectorAsset, AssetID: snapshot.ID}}, CreatedBy: "operator"})
	if err != nil || task.Task.Status != plan.StatusReady || len(task.Steps) != 1 {
		t.Fatal("snapshot plan", task.Task.Status, task.Task.Blockers, err)
	}
	attempt, err := service.CreateExecution(ctx, cleanup.CreateExecutionRequest{ConnectionID: "connection", CleanupTaskID: task.Task.ID, RequestedBy: "operator", IdempotencyKey: "elastic-snapshot-worker", Confirmation: cleanup.ExecutionConfirmation{Acknowledged: true}})
	if err != nil {
		t.Fatal(err)
	}
	jobs, err := repo.ListJobsByAggregate(ctx, "cleanup_task", string(task.Task.ID))
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
		t.Fatal("missing snapshot cleanup job")
	}
	var deadline *time.Time
	origin, completed := "", false
	for round := 0; round < 8; round++ {
		encoded, _ := json.Marshal(job)
		var restored execution.Job
		_ = json.Unmarshal(encoded, &restored)
		repo, err = sqlite.Open(path, "../../migrations")
		if err != nil {
			t.Fatal(err)
		}
		fresh, err := NewRuntime(f.runtime.credentials)
		if err != nil {
			t.Fatal(err)
		}
		fresh.transport = f.runtime.transport
		registered := providerruntime.NewRegistry()
		if err := registered.Register(fresh); err != nil {
			t.Fatal(err)
		}
		if err := registered.RegisterBundle(fresh.Bundle()); err != nil {
			t.Fatal(err)
		}
		resolver := cleanup.ActionResolverFunc(func(ctx context.Context, value asset.Asset) (cleanup.ActionDriver, error) {
			return registered.ResolveAction(ctx, value.Identity.ConnectionID, value)
		})
		if round == 3 {
			delete(f.values, f.ids[elasticSanSnapshotType])
		}
		err = cleanup.NewExecutionHandler(cleanup.NewService(repo, registered), resolver).Handle(ctx, restored)
		var retry *cleanup.RetryError
		if err != nil && !errors.As(err, &retry) {
			t.Fatal("restored snapshot worker", err)
		}
		current, err := repo.Executions().GetActionByExecutionStep(ctx, attempt.ID, string(task.Steps[0].ID))
		if err != nil || current.ProviderError != nil {
			t.Fatal("persisted snapshot failure", err, current.ProviderError)
		}
		if round == 0 {
			origin = current.ProviderOperationID
		}
		if current.ProviderOperationID != origin || strings.Contains(current.ProviderOperationID, "private-elastic-") {
			t.Fatal("operation origin changed or exposed signing material")
		}
		if current.DeletionCheckStartedAt != nil {
			if deadline == nil {
				saved := *current.DeletionCheckStartedAt
				deadline = &saved
			}
			if !deadline.Equal(*current.DeletionCheckStartedAt) {
				t.Fatal("deletion deadline reset after restart")
			}
		}
		stored, err := repo.GetAsset(ctx, snapshot.ID)
		if err != nil || f.values[f.ids[elasticSanSnapshotType]] != nil && stored.ClosedAt != nil {
			t.Fatal("live snapshot closed", err)
		}
		if current.Status == execution.ActionSucceeded {
			completed = stored.ClosedAt != nil && f.deletes == 1 && f.values[f.ids[elasticSanSnapshotType]] == nil
			break
		}
	}
	if !completed || deadline == nil || origin == "" {
		t.Fatal("snapshot recovery incomplete", completed, deadline, origin)
	}
	for _, value := range values {
		if value.ID != snapshot.ID {
			stored, err := repo.GetAsset(ctx, value.ID)
			if err != nil || stored.ClosedAt != nil {
				t.Fatal("snapshot cleanup closed a parent or volume", err)
			}
		}
	}
	encoded, _ := json.Marshal(logs)
	if strings.Contains(string(encoded), "private-elastic-") {
		t.Fatal("private snapshot or signed callback leaked into logs")
	}
}
