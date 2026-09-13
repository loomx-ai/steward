package azure

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/loomx-ai/steward/internal/app/cleanup"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/internal/persistence/sqlite"
	providerruntime "github.com/loomx-ai/steward/internal/provider/runtime"
)

func dataMigrationWorkerRepository(t *testing.T, f *dataMigrationFixture) (*sqlite.Repositories, *providerruntime.Registry, string) {
	t.Helper()
	repository, registry, path := azureNativeWorkerRepository(t, f.runtime)
	previous := f.override
	f.override = func(req *http.Request) (*http.Response, bool) {
		if previous != nil {
			if res, ok := previous(req); ok {
				return res, true
			}
		}
		return dataMigrationOtherIndexes(t, req)
	}
	return repository, registry, path
}

func dataMigrationWorkerKinds() []string {
	return []string{dataMigrationServiceType, dataMigrationProjectType, dataMigrationTaskType, dataMigrationFileType, dataMigrationServiceTaskType, dataMigrationSQLServiceType, dataMigrationMongoServiceType, dataMigrationType}
}

func TestDataMigrationRegisteredInventoryAndKnownAbsence(t *testing.T) {
	f := newDataMigrationFixture(t)
	repository, registry, _ := dataMigrationWorkerRepository(t, f)
	values := azureNativeWorkerScan(t, f.runtime, dataMigrationInventorySource, repository, registry, dataMigrationWorkerKinds(), false, true)
	if len(values) != 12 {
		t.Fatal("registered DMS inventory lost native resources", len(values))
	}
	for _, value := range values {
		if string(value.ID) == value.Identity.NativeID || !slices.Contains([]string{"eastus", "westus"}, value.Location) || value.Normalized["_inventory_source"] != dataMigrationInventorySource || !value.Capabilities.Has(asset.CapabilityActionable) {
			t.Fatal("DMS inventory lost generated identity, execution region or capability", value.Identity.NativeType)
		}
		if value.Identity.NativeType == dataMigrationType && len(object(value.Normalized["_datamigration_target"])) == 0 {
			t.Fatal("migration lost independent target binding")
		}
	}
	task, migration := f.ids[dataMigrationTaskType], f.ids["SqlMi"]
	for _, id := range []string{task, migration, f.ids[dataMigrationServiceType], f.ids[dataMigrationSQLServiceType]} {
		f.omitted[id] = true
	}
	scan := func(failure bool) []asset.Asset {
		return azureNativeWorkerScan(t, f.runtime, dataMigrationInventorySource, repository, registry, []string{dataMigrationTaskType, dataMigrationType}, failure, false)
	}
	if len(scan(false)) != 12 {
		t.Fatal("native list omission erased a live known migration")
	}
	previous := f.override
	f.override = func(req *http.Request) (*http.Response, bool) {
		if strings.EqualFold(req.URL.Path, migration) {
			return jsonResponse(403, map[string]any{"error": map[string]any{"code": "AuthorizationFailed"}}, nil), true
		}
		return previous(req)
	}
	if len(scan(true)) != 12 {
		t.Fatal("incomplete native scan closed a known migration")
	}
	f.override = previous
	delete(f.resources, task)
	delete(f.resources, migration)
	values = scan(false)
	if len(values) != 10 || slices.ContainsFunc(values, func(value asset.Asset) bool {
		return value.Identity.NativeID == task || value.Identity.NativeID == migration
	}) {
		t.Fatal("own native absence did not close the requested migrations", len(values))
	}
	delete(f.resources, f.ids[dataMigrationSQLServiceType])
	if len(scan(true)) != 10 {
		t.Fatal("missing service concealed its surviving target-scoped migrations")
	}
}

func TestDataMigrationRegisteredExecutionAndRecovery(t *testing.T) {
	f := newDataMigrationFixture(t)
	f.schemaFileTask()
	for _, kind := range []string{dataMigrationTaskType, dataMigrationServiceTaskType} {
		object(f.resources[f.ids[kind]]["properties"])["state"] = "Running"
	}
	for _, node := range array(f.nodes[f.ids[dataMigrationSQLServiceType]]["nodes"]) {
		object(node)["concurrentJobsRunning"] = 0
	}
	repository, registry, path := dataMigrationWorkerRepository(t, f)
	values := azureNativeWorkerScan(t, f.runtime, dataMigrationInventorySource, repository, registry, dataMigrationWorkerKinds(), false, true)
	selectors := []plan.CleanupSelector{}
	for _, value := range values {
		if slices.Contains([]string{dataMigrationServiceType, dataMigrationSQLServiceType, dataMigrationMongoServiceType, dataMigrationType}, value.Identity.NativeType) {
			selectors = append(selectors, plan.CleanupSelector{Kind: plan.SelectorAsset, AssetID: value.ID})
		}
	}
	ctx := t.Context()
	logs := []execution.JobLogEntry{}
	ctx = execution.WithJobLogSink(ctx, execution.JobLogSinkFunc(func(_ context.Context, entry execution.JobLogEntry) { logs = append(logs, entry) }))
	planner := cleanup.NewService(repository, registry)
	task, err := planner.CreateTask(ctx, cleanup.CreateTaskRequest{ConnectionID: "connection", Selectors: selectors, CreatedBy: "operator"})
	if err != nil || task.Task.Status != plan.StatusReady || len(task.Steps) != 12 {
		t.Fatal("registered native DMS cleanup plan", err, task.Task.Status, task.Task.Blockers, len(task.Steps))
	}
	attempt, err := planner.CreateExecution(ctx, cleanup.CreateExecutionRequest{ConnectionID: "connection", CleanupTaskID: task.Task.ID, RequestedBy: "operator", IdempotencyKey: "dms-worker", Confirmation: cleanup.ExecutionConfirmation{Acknowledged: true}})
	if err != nil {
		t.Fatal(err)
	}
	counts := f.mutations(t)
	beforeTargets := map[string]string{}
	for id, raw := range f.resources {
		if dataMigrationKind(f.kinds[id]) == "" {
			beforeTargets[id] = f.client.privateConfiguration(raw)
		}
	}
	var active plan.CleanupTaskStep
	previous := f.override
	f.override = func(req *http.Request) (*http.Response, bool) {
		id := strings.ToLower(req.URL.Path)
		phase, item := "", ""
		switch {
		case req.Method == "DELETE":
			phase = "delete"
		case req.Method == "POST" && strings.HasSuffix(id, "/cancel"):
			phase = "cancel"
		case req.Method == "POST" && strings.HasSuffix(id, "/deletenode"):
			phase = "delete-node"
			payload, err := io.ReadAll(req.Body)
			body := map[string]any{}
			if err != nil || json.Unmarshal(payload, &body) != nil {
				t.Fatal("invalid native node body")
			}
			item = text(body["nodeName"])
			req.Body = io.NopCloser(bytes.NewReader(payload))
		}
		if phase != "" {
			current, err := repository.Executions().GetActionByExecutionStep(ctx, attempt.ID, string(active.ID))
			if err != nil || current.IdempotencyKey == "" || req.Header.Get("x-ms-client-request-id") != azureRequestID(current.IdempotencyKey+":"+phase+":"+item) {
				t.Fatal("DMS mutation escaped durable execution intent", phase, err)
			}
		}
		return previous(req)
	}
	completed, phases, restartedCount := map[plan.StepID]bool{}, map[string]bool{}, 0
	for range len(task.Steps) {
		active = plan.CleanupTaskStep{}
		for _, candidate := range task.Steps {
			if !completed[candidate.ID] && !slices.ContainsFunc(candidate.DependsOn, func(id plan.StepID) bool { return !completed[id] }) {
				active = candidate
				break
			}
		}
		if active.ID == "" {
			t.Fatal("DMS plan has no executable dependency order")
		}
		jobs, err := repository.ListJobsByAggregate(ctx, "cleanup_task", string(task.Task.ID))
		if err != nil {
			t.Fatal(err)
		}
		var job execution.Job
		for _, candidate := range jobs {
			if text(candidate.Payload["cleanup_task_step_id"]) == string(active.ID) {
				job = candidate
			}
		}
		if job.ID == "" {
			t.Fatal("persisted DMS job missing")
		}
		var origin string
		var started *time.Time
		for round := 0; round < 24; round++ {
			payload, _ := json.Marshal(job)
			var restored execution.Job
			if json.Unmarshal(payload, &restored) != nil {
				t.Fatal("DMS job restore failed")
			}
			repository, err = sqlite.Open(path, "../../migrations")
			if err != nil {
				t.Fatal(err)
			}
			fresh, err := NewRuntime(f.runtime.credentials)
			if err != nil {
				t.Fatal(err)
			}
			fresh.transport = f.runtime.transport
			restarted := providerruntime.NewRegistry()
			if err := restarted.Register(fresh); err != nil {
				t.Fatal(err)
			}
			if err := restarted.RegisterBundle(fresh.Bundle()); err != nil {
				t.Fatal(err)
			}
			resolver := cleanup.ActionResolverFunc(func(ctx context.Context, value asset.Asset) (cleanup.ActionDriver, error) {
				return restarted.ResolveAction(ctx, value.Identity.ConnectionID, value)
			})
			worker := cleanup.NewExecutionHandler(cleanup.NewService(repository, restarted), resolver)
			var retry *cleanup.RetryError
			err = worker.Handle(ctx, restored)
			if err != nil && !errors.As(err, &retry) {
				current, _ := repository.Executions().GetActionByExecutionStep(ctx, attempt.ID, string(active.ID))
				t.Fatal("DMS worker lost durable phase", err, current.Status, current.ProviderError)
			}
			restartedCount++
			current, err := repository.Executions().GetActionByExecutionStep(ctx, attempt.ID, string(active.ID))
			if err != nil {
				t.Fatal(err)
			}
			if round == 0 {
				origin = current.ProviderOperationID
			}
			if current.ProviderOperationID != origin {
				t.Fatal("DMS recovery replaced original operation receipt")
			}
			if current.DeletionCheckStartedAt != nil {
				if started == nil {
					saved := *current.DeletionCheckStartedAt
					started = &saved
				}
				if !current.DeletionCheckStartedAt.Equal(*started) {
					t.Fatal("DMS recovery reset deletion verification deadline")
				}
			}
			if phase := text(current.ProviderResult["phase"]); phase != "" {
				phases[phase] = true
			}
			stored, err := repository.GetAsset(ctx, active.AssetID)
			if err != nil {
				t.Fatal(err)
			}
			if f.resources[stored.Identity.NativeID] != nil && stored.ClosedAt != nil {
				t.Fatal("accepted DMS mutation closed a live asset")
			}
			if current.Status == execution.ActionSucceeded {
				if stored.ClosedAt == nil || f.resources[stored.Identity.NativeID] != nil {
					t.Fatal("DMS success lost native absence reconciliation")
				}
				completed[active.ID] = true
				break
			}
			if current.ProviderError != nil {
				t.Fatal("DMS worker failed", current.ProviderError)
			}
		}
		if !completed[active.ID] {
			t.Fatal("DMS worker never completed", active.ID)
		}
	}
	remaining, err := repository.ListActiveAssetsByConnection(ctx, "connection", "")
	if err != nil || len(remaining) != 0 {
		t.Fatal("registered DMS cleanup left active assets", len(remaining), err)
	}
	for _, phase := range []string{"cancel", "delete-node", "delete"} {
		if !phases[phase] {
			t.Fatal("DMS journal missed a durable preparation phase", phase)
		}
	}
	if restartedCount <= len(task.Steps) || len(counts) != 19 {
		t.Fatal("DMS recovery coverage changed", restartedCount, len(counts))
	}
	for _, count := range counts {
		if count != 1 {
			t.Fatal("DMS worker repeated a mutation")
		}
	}
	for id, expected := range beforeTargets {
		if f.client.privateConfiguration(f.resources[id]) != expected {
			t.Fatal("DMS worker changed a target database resource")
		}
	}
	journal, err := repository.Executions().ListActions(ctx, attempt.ID)
	if err != nil || len(journal) != 12 || len(logs) == 0 {
		t.Fatal("DMS execution evidence missing", err, len(journal))
	}
	encoded, _ := json.Marshal(map[string]any{"assets": values, "task": task, "journal": journal, "logs": logs})
	for _, secret := range []string{"private-native-dms-configuration", "ssma-test-server", "abc.mongodb.com", "SchemaInput/", "notarealsourceserver", "notarealtargetserver"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatal("private migration input reached persisted public evidence", secret)
		}
	}
	t.Logf("Verified 12 native DMS cleanup steps, %d mutations and %d worker restarts", len(counts), restartedCount)
}
