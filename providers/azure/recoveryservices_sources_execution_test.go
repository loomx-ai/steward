package azure

import (
	"context"
	"errors"
	"github.com/loomx-ai/steward/internal/app/cleanup"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/internal/persistence/sqlite"
	providerruntime "github.com/loomx-ai/steward/internal/provider/runtime"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestRecoverySourceCleanupWorkerOrdersExplicitSelections(t *testing.T) {
	f := newRecoveryItemActionFixture(t)
	sourceID := strings.ToLower(resourceID(vmType, "source"))
	object(f.objects[f.item]["properties"])["sourceResourceId"] = sourceID
	sourceRaw := map[string]any{"id": sourceID, "name": "source", "type": vmType, "location": "eastus", "properties": map[string]any{"provisioningState": "Succeeded"}}
	sourceExists := true
	f.status = 204
	var mutations []string
	base := f.runtime.transport
	f.runtime.transport = roundTripFunc(func(q *http.Request) (*http.Response, error) {
		if q.Method == "DELETE" {
			id := strings.ToLower(q.URL.Path)
			if id != sourceID && id != f.item {
				t.Fatal("unexpected mutation", id)
			}
			if id == sourceID && f.objects[f.item] != nil {
				t.Fatal("source deleted before backup disappearance")
			}
			if q.URL.Query().Get("api-version") == "" {
				t.Fatal("invalid native deletion")
			}
			mutations = append(mutations, id)
			delete(f.objects, id)
			if id == sourceID {
				sourceExists = false
			}
			return &http.Response{StatusCode: 204, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(""))}, nil
		}
		if q.Method == "GET" && strings.EqualFold(q.URL.Path, sourceID) {
			if sourceExists {
				return jsonResponse(200, sourceRaw, nil), nil
			}
			return jsonResponse(404, map[string]any{"error": map[string]any{"code": "ResourceNotFound"}}, nil), nil
		}
		if q.Method == "GET" && strings.EqualFold(q.URL.Path, sourceID+"/extensions") {
			return jsonResponse(200, map[string]any{"value": []any{}}, nil), nil
		}
		return base.RoundTrip(q)
	})
	repo, registry, path := azureNativeWorkerRepository(t, f.runtime)
	// A separately discovered VM is already persisted when the native backup
	// scan contributes references. This test covers execution, not VM discovery.
	now := time.Now().UTC()
	source := asset.Asset{ID: "source", Identity: asset.Identity{Provider: asset.ProviderAzure, Partition: "azure", ConnectionID: "connection", NativeType: vmType, NativeID: sourceID}, Location: "eastus", ScopeID: "root", ResourceKindID: f.runtime.resourceKind(vmType).ID, FirstSeenAt: now, LastSeenAt: now, Normalized: object(sourceRaw["properties"]), Capabilities: asset.CapabilitySet{asset.CapabilityIndexed, asset.CapabilityActionable}}
	if err := repo.PutAsset(t.Context(), source); err != nil {
		t.Fatal(err)
	}
	values := azureNativeWorkerScan(t, f.runtime, recoveryServicesSource, repo, registry, []string{recoveryServicesVault, recoveryServicesContainer, recoveryServicesItem}, false, true)
	var selectors []plan.CleanupSelector
	for _, v := range values {
		if v.Identity.NativeID == f.item || v.Identity.NativeID == sourceID {
			selectors = append(selectors, plan.CleanupSelector{Kind: plan.SelectorAsset, AssetID: v.ID})
		}
	}
	if len(selectors) != 2 {
		t.Fatal("missing backup/source inventory")
	}
	planner := cleanup.NewService(repo, registry)
	task, err := planner.CreateTask(t.Context(), cleanup.CreateTaskRequest{ConnectionID: "connection", Selectors: selectors, CreatedBy: "operator"})
	if err != nil || task.Task.Status != plan.StatusReady || len(task.Steps) != 2 || len(task.ImpactItems) != 0 {
		t.Fatal("joint backup/source plan", task, err)
	}
	attempt, err := planner.CreateExecution(t.Context(), cleanup.CreateExecutionRequest{ConnectionID: "connection", CleanupTaskID: task.Task.ID, RequestedBy: "operator", IdempotencyKey: "recovery-source-worker", Confirmation: cleanup.ExecutionConfirmation{Acknowledged: true}})
	if err != nil {
		t.Fatal(err)
	}
	jobs, err := repo.ListJobsByAggregate(t.Context(), "cleanup_task", string(task.Task.ID))
	if err != nil {
		t.Fatal(err)
	}
	// Reopen both persistence and the provider for every retry, including an
	// out-of-order delivery of the source job before its backup prerequisite.
	run := func(step plan.CleanupTaskStep) bool {
		t.Helper()
		var job execution.Job
		for _, candidate := range jobs {
			if text(candidate.Payload["cleanup_task_step_id"]) == string(step.ID) {
				job = candidate
			}
		}
		if job.ID == "" {
			t.Fatal("missing step job")
		}
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
		if err != nil && !errors.As(err, &retry) {
			t.Fatal("joint backup/source execution", err)
		}
		return err == nil
	}
	var first, second plan.CleanupTaskStep
	for _, step := range task.Steps {
		if len(step.DependsOn) == 0 {
			first = step
		} else {
			second = step
		}
	}
	if first.ID == "" || second.ID == "" {
		t.Fatal("missing persisted ordering")
	}
	if run(second) || len(mutations) != 0 {
		t.Fatal("out-of-order job bypassed prerequisite")
	}
	for _, step := range []plan.CleanupTaskStep{first, second} {
		done := false
		for i := 0; i < 5 && !done; i++ {
			done = run(step)
		}
		if !done {
			t.Fatal("backup/source step did not finish")
		}
		action, err := repo.Executions().GetActionByExecutionStep(t.Context(), attempt.ID, string(step.ID))
		if err != nil || action.Status != execution.ActionSucceeded {
			t.Fatal("missing persisted own-read outcome", err)
		}
	}
	if len(mutations) != 2 || mutations[0] != f.item || mutations[1] != sourceID {
		t.Fatal("wrong native deletion order", mutations)
	}
	active, err := repo.ListActiveAssetsByConnection(t.Context(), "connection", "")
	if err != nil || len(active) != len(values)-2 {
		t.Fatal("parent/source cleanup escaped selection", err)
	}
}
