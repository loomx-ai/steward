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

func TestAzureLocalVMRegisteredExecutionRecovery(t *testing.T) {
	t.Run("vm", func(t *testing.T) { azureLocalWorkerRecovery(t, false) })
	t.Run("registration", func(t *testing.T) { azureLocalWorkerRecovery(t, true) })
}

func azureLocalWorkerRecovery(t *testing.T, registration bool) {
	f := newLocalVMFixture(t)
	f.holdVM, f.holdMeta, f.holdDisk = true, true, true
	logs := []execution.JobLogEntry{}
	ctx := execution.WithJobLogSink(t.Context(), execution.JobLogSinkFunc(func(_ context.Context, entry execution.JobLogEntry) { logs = append(logs, entry) }))
	repository, registry, path := azureNativeWorkerRepository(t, f.runtime)
	azureNativeWorkerScan(t, f.runtime, azureLocalSource, repository, registry, []string{azureLocalVMType, azureLocalAgentType, azureLocalIdentityType, azureLocalNICType, azureLocalDiskType, azureLocalNetworkType, azureLocalStorageType, azureLocalImageType, azureLocalMarketplaceType}, false, true)
	values := azureNativeWorkerScan(t, f.runtime, hybridComputeSource, repository, registry, []string{hybridMachineType, hybridExtensionType, hybridCommandType, hybridProfileType, hybridLicenseType}, false, true)
	var vm, metadata, machine asset.Asset
	for _, value := range values {
		if value.Identity.NativeType == hybridMachineType {
			machine = value
		}
		if value.Identity.NativeType == azureLocalVMType {
			vm = value
		}
		if value.Identity.NativeType == azureLocalIdentityType {
			metadata = value
		}
	}
	selected, steps, arcDeletes := vm, 5, 3
	if registration {
		selected, steps, arcDeletes = machine, 6, 4
	}
	planner := cleanup.NewService(repository, registry)
	task, err := planner.CreateTask(ctx, cleanup.CreateTaskRequest{ConnectionID: "connection", Selectors: []plan.CleanupSelector{{Kind: plan.SelectorAsset, AssetID: selected.ID}}, CreatedBy: "operator"})
	if err != nil || task.Task.Status != plan.StatusReady || len(task.Steps) != steps || len(task.ImpactItems) != 2 {
		t.Fatal("VM plan", err, task.Task.Status, task.Task.Blockers, len(task.Steps), task.ImpactItems)
	}
	if task.Steps[len(task.Steps)-1].AssetID != selected.ID {
		t.Fatal("VM controller ordering or metadata outcome", task.Steps, task.ImpactItems)
	}
	warned, registrationWarned := false, false
	for _, warning := range task.Task.Warnings {
		if warning.AssetID == machine.ID && warning.Code == plan.WarningArcMachineRegistrationRemoval {
			t.Fatal("Local registration warning describes an external host")
		}
		if warning.AssetID == machine.ID && warning.Code == plan.WarningAzureLocalRegistrationRemoval {
			registrationWarned = true
		}
		if warning.Code == plan.WarningAzureLocalVMRemoval && warning.AssetID == vm.ID && warning.Evidence["operation"] == "delete" && strings.Contains(warning.Message, "Arc registration, NICs and data disks have separate cleanup steps") {
			warned = true
		}
		if warning.Code == plan.WarningAzureLocalGuestRemoval && strings.Contains(warning.Message, "VM and Arc registration remain") {
			t.Fatal("guest warning contradicts planned VM deletion")
		}
	}
	if !warned || registrationWarned != registration {
		t.Fatal("VM deletion consequences missing from review")
	}
	attempt, err := planner.CreateExecution(ctx, cleanup.CreateExecutionRequest{ConnectionID: "connection", CleanupTaskID: task.Task.ID, RequestedBy: "operator", IdempotencyKey: "vm-worker", Confirmation: cleanup.ExecutionConfirmation{Acknowledged: true}})
	if err != nil {
		t.Fatal(err)
	}
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
			t.Fatal("VM execution job missing")
		}
		origin := ""
		var started *time.Time
		completed := false
		for round := 0; round < 12; round++ {
			payload, _ := json.Marshal(job)
			var restored execution.Job
			_ = json.Unmarshal(payload, &restored)
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
			if step.AssetID == vm.ID {
				if round == 3 {
					delete(f.values, f.ids[azureLocalVMType])
				}
				if round == 5 {
					delete(f.values, f.ids[azureLocalIdentityType])
				}
				if round == 7 {
					delete(f.values, f.ids[azureLocalDiskType])
				}
			}
			err = cleanup.NewExecutionHandler(cleanup.NewService(repository, registered), resolver).Handle(ctx, restored)
			var retry *cleanup.RetryError
			if err != nil && !errors.As(err, &retry) {
				t.Fatal("restored VM worker", err)
			}
			current, err := repository.Executions().GetActionByExecutionStep(ctx, attempt.ID, string(step.ID))
			if err != nil || current.ProviderError != nil {
				t.Fatal("VM persisted failure", err, current.ProviderError)
			}
			if round == 0 {
				origin = current.ProviderOperationID
			}
			if current.ProviderOperationID != origin {
				t.Fatal("VM resume replaced original operation")
			}
			if current.DeletionCheckStartedAt != nil {
				if started == nil {
					saved := *current.DeletionCheckStartedAt
					started = &saved
				}
				if !started.Equal(*current.DeletionCheckStartedAt) {
					t.Fatal("VM resume reset deadline")
				}
			}
			stored, err := repository.GetAsset(ctx, step.AssetID)
			if err != nil {
				t.Fatal(err)
			}
			if step.AssetID == vm.ID && (f.values[f.ids[azureLocalIdentityType]] != nil || f.values[f.ids[azureLocalDiskType]] != nil) {
				identity, err := repository.GetAsset(ctx, metadata.ID)
				if err != nil || identity.ClosedAt != nil || stored.ClosedAt != nil || current.Status == execution.ActionSucceeded {
					t.Fatal("VM absence closed surviving metadata", err)
				}
			}
			if current.Status == execution.ActionSucceeded {
				if stored.ClosedAt == nil || f.values[stored.Identity.NativeID] != nil {
					t.Fatal("cleanup did not verify own absence")
				}
				completed = true
				break
			}
		}
		if !completed || origin == "" || started == nil {
			t.Fatal("VM step recovery incomplete", step.AssetID)
		}
	}
	remaining, err := repository.ListActiveAssetsByConnection(ctx, "connection", "")
	if err != nil || len(remaining) != 11-arcDeletes || f.vmDeletes != 1 || f.deleted != 1 || len(f.arc.deleted) != arcDeletes {
		t.Fatal("VM recovery coverage", err, len(remaining), f.vmDeletes, f.deleted, f.arc.deleted)
	}
	for _, value := range remaining {
		if registration && value.Identity.NativeType == hybridMachineType || value.Identity.NativeType == azureLocalVMType || value.Identity.NativeType == azureLocalAgentType || value.Identity.NativeType == azureLocalIdentityType || hybridComputeChild(value.Identity.NativeType) {
			t.Fatal("unexpected remaining VM resource")
		}
	}
	encoded, _ := json.Marshal(logs)
	for _, secret := range []string{"private-local-", "private-arc-", "Write-Host Hello World"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatal("private data reached VM execution logs")
		}
	}
}
