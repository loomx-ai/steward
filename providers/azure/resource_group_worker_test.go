package azure

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/loomx-ai/steward/internal/app/cleanup"
	"github.com/loomx-ai/steward/internal/app/governance"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/core/graph"
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/internal/persistence/sqlite"
	providerruntime "github.com/loomx-ai/steward/internal/provider/runtime"
)

func TestResourceGroupPersistedWorkerRecovery(t *testing.T) {
	testResourceGroupMonitorReferences(t, monitorActivityAlertType, "public-worker")
}

func testResourceGroupWorkerRecovery(t *testing.T, runtime *Runtime, values []asset.Asset, built governance.Contribution, counts func() (int, int), removeResidual func()) {
	t.Helper()
	ctx := t.Context()
	repo, registry, path := azureNativeWorkerRepository(t, runtime)
	now := time.Now().UTC()
	for _, value := range values {
		value.ScopeID = "root"
		value.ResourceKindID = runtime.resourceKind(value.Identity.NativeType).ID
		value.FirstSeenAt, value.LastSeenAt = now, now
		if err := repo.PutAsset(ctx, value); err != nil {
			t.Fatal(err)
		}
	}
	for i := range built.Bindings {
		built.Bindings[i].ID = graph.LifecycleBindingID(fmt.Sprintf("group-binding-%d", i))
		built.Bindings[i].ObservedAt = now
	}
	for i := range built.Relationships {
		built.Relationships[i].ID = graph.RelationshipID(fmt.Sprintf("group-relation-%d", i))
		built.Relationships[i].ObservedAt = now
	}
	if err := repo.ReplaceGraph(ctx, "root", "group-worker", built.Relationships, built.Bindings, built.Unresolved...); err != nil {
		t.Fatal(err)
	}
	service := cleanup.NewService(repo, registry)
	task, err := service.CreateTask(ctx, cleanup.CreateTaskRequest{ConnectionID: "connection", Selectors: []plan.CleanupSelector{{Kind: plan.SelectorAsset, AssetID: values[0].ID}}, CreatedBy: "operator"})
	if err != nil || task.Task.Status != plan.StatusReady || len(task.Steps) != 1 {
		t.Fatal("persisted group plan", task, err)
	}
	attempt, err := service.CreateExecution(ctx, cleanup.CreateExecutionRequest{ConnectionID: "connection", CleanupTaskID: task.Task.ID, RequestedBy: "operator", IdempotencyKey: "group-worker", Confirmation: cleanup.ExecutionConfirmation{Acknowledged: true}})
	if err != nil {
		t.Fatal(err)
	}
	jobs, err := repo.ListJobsByAggregate(ctx, "cleanup_task", string(task.Task.ID))
	if err != nil {
		t.Fatal(err)
	}
	checkedResidual, completed := false, false
	for round := 0; round < 12; round++ {
		repo, err = sqlite.Open(path, "../../migrations")
		if err != nil {
			t.Fatal(err)
		}
		fresh, err := NewRuntime(runtime.credentials)
		if err != nil {
			t.Fatal(err)
		}
		fresh.transport = runtime.transport
		registered := providerruntime.NewRegistry()
		if err = registered.Register(fresh); err != nil {
			t.Fatal(err)
		}
		if err = registered.RegisterBundle(fresh.Bundle()); err != nil {
			t.Fatal(err)
		}
		resolver := cleanup.ActionResolverFunc(func(ctx context.Context, value asset.Asset) (cleanup.ActionDriver, error) {
			return registered.ResolveAction(ctx, value.Identity.ConnectionID, value)
		})
		handled := 0
		for _, job := range jobs {
			if job.Payload["cleanup_task_step_id"] == nil {
				continue
			}
			handled++
			raw, err := json.Marshal(job)
			if err != nil {
				t.Fatal(err)
			}
			var restored execution.Job
			if err = json.Unmarshal(raw, &restored); err != nil {
				t.Fatal(err)
			}
			err = cleanup.NewExecutionHandler(cleanup.NewService(repo, registered), resolver).Handle(ctx, restored)
			var retry *cleanup.RetryError
			if err != nil && !errors.As(err, &retry) {
				t.Fatal("restarted group worker", err)
			}
		}
		if handled != 1 {
			t.Fatal("missing root job", handled)
		}
		deletes, polls := counts()
		if deletes > 1 {
			t.Fatal("repeated native DELETE", deletes)
		}
		root, err := repo.GetAsset(ctx, values[0].ID)
		if err != nil {
			t.Fatal(err)
		}
		action, err := repo.Executions().GetActionByExecutionStep(ctx, attempt.ID, string(task.Steps[0].ID))
		if err != nil {
			t.Fatal(err)
		}
		if action.Status == execution.ActionFailed {
			t.Fatal("recoverable native cascade became a failed action", action.ProviderError)
		}
		if !checkedResidual {
			for _, value := range values {
				stored, err := repo.GetAsset(ctx, value.ID)
				if err != nil || stored.ClosedAt != nil {
					t.Fatal("member closed before residual verification", value.ID, err)
				}
			}
		}
		if !checkedResidual && polls >= 2 {
			if root.ClosedAt != nil || action.Status == execution.ActionSucceeded {
				t.Fatal("native operation completion hid surviving member")
			}
			checkedResidual = true
			removeResidual()
		}
		if root.ClosedAt != nil {
			completed = true
			break
		}
	}
	deletes, _ := counts()
	if !completed || !checkedResidual || deletes != 1 {
		t.Fatal("group recovery incomplete", completed, checkedResidual, deletes)
	}
	action, err := repo.Executions().GetActionByExecutionStep(ctx, attempt.ID, string(task.Steps[0].ID))
	if err != nil || action.Status != execution.ActionSucceeded || action.DeletionCheckStartedAt == nil {
		t.Fatal("persisted group outcome", action, err)
	}
	for _, value := range values {
		stored, err := repo.GetAsset(ctx, value.ID)
		if err != nil || stored.ClosedAt == nil {
			t.Fatal("verified group member not closed", value.ID, err)
		}
	}
}
