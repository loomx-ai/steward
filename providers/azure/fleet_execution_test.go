package azure

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/app/cleanup"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/internal/persistence/sqlite"
	providerruntime "github.com/loomx-ai/steward/internal/provider/runtime"
)

func testFleetCleanupWorkers(t *testing.T, f *fleetFixture, repository *sqlite.Repositories, registry *providerruntime.Registry, values []asset.Asset) {
	t.Helper()
	ctx := t.Context()
	planner := cleanup.NewService(repository, registry)
	for _, kind := range []string{fleetMemberType, fleetStrategyType} {
		value := fleetAssetByKind(t, values, kind)
		blocked, err := planner.CreateTask(ctx, cleanup.CreateTaskRequest{ConnectionID: "connection", Selectors: []plan.CleanupSelector{{Kind: plan.SelectorAsset, AssetID: value.ID}}, CreatedBy: "operator"})
		if err != nil || blocked.Task.Status == plan.StatusReady || len(blocked.Task.Blockers) == 0 {
			t.Fatal("native Fleet prerequisite disappeared from persisted plan", kind, blocked, err)
		}
	}
	var selectors []plan.CleanupSelector
	for _, value := range values {
		if slices.Contains(fleetDirectKinds, value.Identity.NativeType) {
			selectors = append(selectors, plan.CleanupSelector{Kind: plan.SelectorAsset, AssetID: value.ID})
		}
	}
	task, err := planner.CreateTask(ctx, cleanup.CreateTaskRequest{ConnectionID: "connection", Selectors: selectors, CreatedBy: "operator"})
	if err != nil || task.Task.Status != plan.StatusReady || len(task.Steps) != 5 || len(task.ImpactItems) != 1 {
		t.Fatal("Fleet did not plan five independent deletes and the Run-owned Gate", task, err)
	}
	if _, err := planner.CreateExecution(ctx, cleanup.CreateExecutionRequest{ConnectionID: "connection", CleanupTaskID: task.Task.ID, RequestedBy: "operator", IdempotencyKey: "fleet-worker-delete", Confirmation: cleanup.ExecutionConfirmation{Acknowledged: true}}); err != nil {
		t.Fatal(err)
	}
	resolver := cleanup.ActionResolverFunc(func(ctx context.Context, value asset.Asset) (cleanup.ActionDriver, error) {
		return registry.ResolveAction(ctx, value.Identity.ConnectionID, value)
	})
	run := fleetAssetByKind(t, values, fleetRunType)
	gate := fleetAssetByKind(t, values, fleetGateType)
	progress := object(object(object(f.resources[run.Identity.NativeID]["properties"])["status"])["status"])
	progress["state"] = "Running"
	var deleted []string
	stops := 0
	f.override = func(req *http.Request) (*http.Response, bool) {
		if req.Method != "GET" {
			id := strings.TrimSuffix(strings.ToLower(req.URL.Path), "/stop")
			raw := f.resources[id]
			if raw == nil || !slices.Contains(fleetDirectKinds, text(raw["type"])) || req.URL.Query().Get("api-version") != fleetVersion || len(req.URL.Query()) != 1 || req.ContentLength > 0 || req.Header.Get("If-Match") == "" || req.Header.Get("If-Match") != text(raw["eTag"]) || req.Header.Get("x-ms-client-request-id") == "" {
				t.Fatal("worker changed the Fleet mutation contract", req.Method, req.URL, req.Header)
			}
			if req.Method == "POST" {
				if id != run.Identity.NativeID || !strings.HasSuffix(req.URL.Path, "/stop") || progress["state"] != "Running" {
					t.Fatal("worker issued a repeated or unrelated Fleet POST")
				}
				stops++
				progress["state"], raw["eTag"] = "Stopping", `"worker-stopping"`
				return jsonResponse(200, raw, http.Header{"Retry-After": {"1"}}), true
			}
			if req.Method != "DELETE" || id == run.Identity.NativeID && progress["state"] != "Stopped" {
				t.Fatal("worker deleted an active Fleet run")
			}
			deleted = append(deleted, id)
			object(raw["properties"])["provisioningState"] = "Deleting"
			return jsonResponse(204, nil, http.Header{"Retry-After": {"1"}}), true
		}
		return fleetGraphEmptyIndexes(t, req)
	}
	completed := map[plan.StepID]bool{}
	for range len(task.Steps) {
		var step plan.CleanupTaskStep
		for _, candidate := range task.Steps {
			ready := !completed[candidate.ID]
			for _, required := range candidate.DependsOn {
				ready = ready && completed[required]
			}
			if ready {
				step = candidate
				break
			}
		}
		if step.ID == "" {
			t.Fatal("Fleet persisted plan has no ready step")
		}
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
		value, err := repository.GetAsset(ctx, step.AssetID)
		if err != nil || job.ID == "" {
			t.Fatal("Fleet step lost its native asset or durable job", err)
		}
		resume := func(pending bool) {
			t.Helper()
			wire, _ := json.Marshal(job)
			var restored execution.Job
			if err := json.Unmarshal(wire, &restored); err != nil {
				t.Fatal(err)
			}
			f.runtime.clients = map[asset.ConnectionID]*client{}
			worker := cleanup.NewExecutionHandler(cleanup.NewService(repository, registry), resolver)
			var retry *cleanup.RetryError
			err := worker.Handle(ctx, restored)
			if pending && !errors.As(err, &retry) || !pending && err != nil {
				t.Fatal("restarted Fleet worker lost its saved phase", value.Identity.NativeType, pending, err)
			}
		}
		before := len(deleted)
		resume(true)
		if value.ID == run.ID {
			if stops != 1 || len(deleted) != before {
				t.Fatal("Fleet worker did not save Stop separately from Delete", stops, deleted)
			}
			resume(true) // The synchronous Stop receipt still reports Stopping.
			if len(deleted) != before {
				t.Fatal("worker confused Stop completion with run termination")
			}
			progress["state"] = "Stopped"
			f.resources[run.Identity.NativeID]["eTag"] = `"worker-stopped"`
			object(f.resources[gate.Identity.NativeID]["properties"])["state"] = "Skipped"
			resume(true)
		}
		if len(deleted) != before+1 || deleted[before] != value.Identity.NativeID {
			t.Fatal("Fleet worker changed dependency order or repeated deletion", deleted)
		}
		resume(true) // Native DELETE acknowledgment cannot close a live resource.
		stored, err := repository.GetAsset(ctx, value.ID)
		if err != nil || stored.ClosedAt != nil {
			t.Fatal("Fleet worker closed a live native resource", stored, err)
		}
		delete(f.resources, value.Identity.NativeID)
		if value.ID == run.ID {
			resume(true) // A live Gate remains after its run disappears.
			stored, err := repository.GetAsset(ctx, gate.ID)
			if err != nil || stored.ClosedAt != nil {
				t.Fatal("Fleet worker closed a residual Gate", stored, err)
			}
			delete(f.resources, gate.Identity.NativeID)
		}
		resume(true) // Save successful Wait before the separate final readback.
		resume(false)
		stored, err = repository.GetAsset(ctx, value.ID)
		if err != nil || stored.ClosedAt == nil || len(deleted) != before+1 {
			t.Fatal("Fleet worker did not close exactly one verified deletion", stored, err, deleted)
		}
		completed[step.ID] = true
	}
	if stops != 1 || len(deleted) != 5 {
		t.Fatal("Fleet worker repeated native mutations", stops, deleted)
	}
	stored, err := repository.GetAsset(ctx, gate.ID)
	if err != nil || stored.ClosedAt == nil {
		t.Fatal("owning run failed to verify and close its Gate", stored, err)
	}
	for _, pair := range [][2]string{{fleetNamespaceType, fleetMemberType}, {fleetProfileType, fleetStrategyType}} {
		first, second := fleetAssetByKind(t, values, pair[0]), fleetAssetByKind(t, values, pair[1])
		if slices.Index(deleted, first.Identity.NativeID) >= slices.Index(deleted, second.Identity.NativeID) {
			t.Fatal("Fleet worker lost native prerequisite ordering", pair, deleted)
		}
	}
}
