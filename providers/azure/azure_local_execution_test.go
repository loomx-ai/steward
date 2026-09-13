package azure

import (
	"context"
	"encoding/json"
	"errors"
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

func TestAzureLocalRegisteredGuestCleanupRecovery(t *testing.T) {
	f := newLocalCleanupFixture(t)
	f.hold = true
	logs := []execution.JobLogEntry{}
	ctx := execution.WithJobLogSink(t.Context(), execution.JobLogSinkFunc(func(_ context.Context, e execution.JobLogEntry) { logs = append(logs, e) }))
	repo, registry, path := azureNativeWorkerRepository(t, f.runtime)
	values := azureNativeWorkerScan(t, f.runtime, azureLocalSource, repo, registry, []string{azureLocalAgentType, azureLocalVMType, azureLocalIdentityType}, false, true)
	var guest asset.Asset
	for _, value := range values {
		if value.Identity.NativeType == azureLocalAgentType {
			guest = value
		}
	}
	if guest.ID == "" {
		t.Fatal("guest scan missing")
	}
	service := cleanup.NewService(repo, registry)
	task, err := service.CreateTask(ctx, cleanup.CreateTaskRequest{ConnectionID: "connection", Selectors: []plan.CleanupSelector{{Kind: plan.SelectorAsset, AssetID: guest.ID}}, CreatedBy: "operator"})
	if err != nil || task.Task.Status != plan.StatusReady || len(task.Steps) != 1 {
		t.Fatal("guest plan", err, task.Task.Status, task.Task.Blockers)
	}
	found := false
	for _, warning := range task.Task.Warnings {
		if warning.Code == plan.WarningAzureLocalGuestRemoval && warning.AssetID == guest.ID && warning.Message != "" && warning.Evidence["operation"] == "delete" {
			found = true
		}
	}
	if !found {
		t.Fatal("guest-management effect missing", task.Task.Warnings)
	}
	attempt, err := service.CreateExecution(ctx, cleanup.CreateExecutionRequest{ConnectionID: "connection", CleanupTaskID: task.Task.ID, RequestedBy: "operator", IdempotencyKey: "local-worker", Confirmation: cleanup.ExecutionConfirmation{Acknowledged: true}})
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
		t.Fatal("execution job missing")
	}
	origin := ""
	var deadline *time.Time
	completed := false
	for round := 0; round < 10; round++ {
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
			delete(f.values, f.ids[azureLocalAgentType])
		}
		err = cleanup.NewExecutionHandler(cleanup.NewService(repo, registered), resolver).Handle(ctx, restored)
		var retry *cleanup.RetryError
		if err != nil && !errors.As(err, &retry) {
			t.Fatal("restored guest worker", err)
		}
		current, err := repo.Executions().GetActionByExecutionStep(ctx, attempt.ID, string(task.Steps[0].ID))
		if err != nil || current.ProviderError != nil {
			t.Fatal("persisted failure", err, current.ProviderError)
		}
		if round == 0 {
			origin = current.ProviderOperationID
		}
		if current.ProviderOperationID != origin {
			t.Fatal("original operation lost")
		}
		if current.DeletionCheckStartedAt != nil {
			if deadline == nil {
				saved := *current.DeletionCheckStartedAt
				deadline = &saved
			}
			if !deadline.Equal(*current.DeletionCheckStartedAt) {
				t.Fatal("readback deadline reset")
			}
		}
		stored, err := repo.GetAsset(ctx, guest.ID)
		if err != nil {
			t.Fatal(err)
		}
		if f.values[f.ids[azureLocalAgentType]] != nil && stored.ClosedAt != nil {
			t.Fatal("successful poll closed live guest")
		}
		if current.Status == execution.ActionSucceeded {
			if stored.ClosedAt == nil || f.deleted != 1 || f.values[f.ids[azureLocalAgentType]] != nil {
				t.Fatal("guest cleanup did not verify absence")
			}
			completed = true
			break
		}
	}
	if !completed || deadline == nil || origin == "" {
		t.Fatal("cleanup recovery incomplete")
	}
	for _, value := range values {
		if value.ID != guest.ID {
			stored, err := repo.GetAsset(ctx, value.ID)
			if err != nil || stored.ClosedAt != nil {
				t.Fatal("guest cleanup closed VM/identity", err)
			}
		}
	}
	encoded, _ := json.Marshal(logs)
	if strings.Contains(string(encoded), "private-local-") {
		t.Fatal("private guest/VM data in logs")
	}
}
