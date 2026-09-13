package azure

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

func TestHybridComputeRegisteredExecutionRecovery(t *testing.T) {
	for _, machine := range []bool{false, true} {
		t.Run(fmt.Sprint("machine=", machine), func(t *testing.T) {
			testHybridComputeRegisteredExecutionRecovery(t, machine)
		})
	}
}

func testHybridComputeRegisteredExecutionRecovery(t *testing.T, machine bool) {
	logs := []execution.JobLogEntry{}
	ctx := execution.WithJobLogSink(t.Context(), execution.JobLogSinkFunc(func(_ context.Context, entry execution.JobLogEntry) { logs = append(logs, entry) }))
	f := newHybridCleanupFixture(t)
	steps := 3
	if machine {
		f.standardMachine()
		steps = 4
	}
	for _, raw := range f.values {
		if raw["type"] == hybridCommandType {
			object(object(raw["properties"])["instanceView"])["executionState"] = "Running"
		}
	}
	repository, registry, path := azureNativeWorkerRepository(t, f.runtime)
	values := azureNativeWorkerScan(t, f.runtime, hybridComputeSource, repository, registry, []string{hybridMachineType, hybridExtensionType, hybridCommandType, hybridProfileType, hybridLicenseType}, false, true)
	selectors := []plan.CleanupSelector{}
	for _, value := range values {
		if (!machine && hybridComputeChild(value.Identity.NativeType)) || (machine && value.Identity.NativeType == hybridMachineType) {
			selectors = append(selectors, plan.CleanupSelector{Kind: plan.SelectorAsset, AssetID: value.ID})
		}
	}
	planner := cleanup.NewService(repository, registry)
	task, err := planner.CreateTask(ctx, cleanup.CreateTaskRequest{ConnectionID: "connection", Selectors: selectors, CreatedBy: "operator"})
	if err != nil || task.Task.Status != plan.StatusReady || len(task.Steps) != steps {
		t.Fatal("Arc child plan", err, task.Task.Status, task.Task.Blockers, len(task.Steps))
	}
	warnings := map[asset.AssetID]bool{}
	for _, warning := range task.Task.Warnings {
		if strings.HasPrefix(string(warning.Code), "arc_") {
			warnings[warning.AssetID] = true
			if warning.Message == "" || warning.Evidence["operation"] != "delete" {
				t.Fatal("Arc impact missing")
			}
		}
	}
	if len(warnings) != steps {
		t.Fatal("review omitted Arc deletion consequences", task.Task.Warnings)
	}
	attempt, err := planner.CreateExecution(ctx, cleanup.CreateExecutionRequest{ConnectionID: "connection", CleanupTaskID: task.Task.ID, RequestedBy: "operator", IdempotencyKey: "arc-worker", Confirmation: cleanup.ExecutionConfirmation{Acknowledged: true}})
	if err != nil {
		t.Fatal(err)
	}
	restarts := 0
	for _, step := range task.Steps {
		jobs, err := repository.ListJobsByAggregate(ctx, "cleanup_task", string(task.Task.ID))
		if err != nil {
			t.Fatal(err)
		}
		var job execution.Job
		for _, candidate := range jobs {
			if text(candidate.Payload["cleanup_task_step_id"]) == string(step.ID) {
				job = candidate
			}
		}
		if job.ID == "" {
			t.Fatal("Arc execution job missing")
		}
		origin := ""
		var started *time.Time
		completed := false
		for round := 0; round < 12; round++ {
			payload, _ := json.Marshal(job)
			var restored execution.Job
			if json.Unmarshal(payload, &restored) != nil {
				t.Fatal("restore job")
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
			err = cleanup.NewExecutionHandler(cleanup.NewService(repository, registered), resolver).Handle(ctx, restored)
			var retry *cleanup.RetryError
			if err != nil && !errors.As(err, &retry) {
				t.Fatal("restored Arc worker", err)
			}
			restarts++
			current, err := repository.Executions().GetActionByExecutionStep(ctx, attempt.ID, string(step.ID))
			if err != nil || current.ProviderError != nil {
				t.Fatal("Arc persisted failure", err, current.ProviderError)
			}
			if round == 0 {
				origin = current.ProviderOperationID
			}
			if current.ProviderOperationID != origin {
				t.Fatal("Arc resume replaced original operation")
			}
			if current.DeletionCheckStartedAt != nil {
				if started == nil {
					saved := *current.DeletionCheckStartedAt
					started = &saved
				}
				if !started.Equal(*current.DeletionCheckStartedAt) {
					t.Fatal("Arc resume reset deadline")
				}
			}
			stored, err := repository.GetAsset(ctx, step.AssetID)
			if err != nil {
				t.Fatal(err)
			}
			if f.values[stored.Identity.NativeID] != nil && stored.ClosedAt != nil {
				t.Fatal("operation success closed a surviving Arc child")
			}
			if current.Status == execution.ActionSucceeded {
				if stored.ClosedAt == nil || f.values[stored.Identity.NativeID] != nil || f.deleted[stored.Identity.NativeID] != 1 {
					t.Fatal("Arc cleanup did not verify its own absence")
				}
				completed = true
				break
			}
		}
		if !completed {
			t.Fatal("Arc child never completed", step.ID)
		}
	}
	remaining, err := repository.ListActiveAssetsByConnection(ctx, "connection", "")
	if err != nil || len(remaining) != 5-steps || len(f.deleted) != steps || restarts <= steps {
		t.Fatal("Arc recovery coverage", err, len(remaining), f.deleted, restarts)
	}
	for _, value := range remaining {
		if hybridComputeChild(value.Identity.NativeType) || (machine && value.Identity.NativeType != hybridLicenseType) {
			t.Fatal("unexpected remaining Arc resource")
		}
	}
	encoded, _ := json.Marshal(logs)
	if len(logs) == 0 {
		t.Fatal("missing execution logs")
	}
	for _, secret := range []string{"private-c", "private-s", "private-h", "private-arc-configuration", "Write-Host Hello World"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatal("Arc private data reached job logs")
		}
	}
}
