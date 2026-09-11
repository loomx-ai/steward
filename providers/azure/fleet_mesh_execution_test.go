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

func testFleetMeshCleanupWorkers(t *testing.T, h *fleetMeshCleanupFixture, repository *sqlite.Repositories, registry *providerruntime.Registry, values []asset.Asset) {
	t.Helper()
	var logs []execution.JobLogEntry
	ctx := execution.WithJobLogSink(t.Context(), execution.JobLogSinkFunc(func(_ context.Context, entry execution.JobLogEntry) { logs = append(logs, entry) }))
	planner := cleanup.NewService(repository, registry)
	mesh := fleetAssetByKind(t, values, fleetMeshType)
	namespace := fleetAssetByKind(t, values, fleetNamespaceType)
	var member asset.Asset
	for _, value := range values {
		if value.Identity.NativeID == h.member {
			member = value
		}
	}
	selectors := []plan.CleanupSelector{{Kind: plan.SelectorAsset, AssetID: member.ID}, {Kind: plan.SelectorAsset, AssetID: namespace.ID}}
	blocked, err := planner.CreateTask(ctx, cleanup.CreateTaskRequest{ConnectionID: "connection", Selectors: selectors, CreatedBy: "operator"})
	if err != nil || blocked.Task.Status == plan.StatusReady || len(blocked.Task.Blockers) == 0 {
		t.Fatal("member cleanup silently selected its Mesh prerequisite", blocked, err)
	}
	selectors = append(selectors, plan.CleanupSelector{Kind: plan.SelectorAsset, AssetID: mesh.ID})
	task, err := planner.CreateTask(ctx, cleanup.CreateTaskRequest{ConnectionID: "connection", Selectors: selectors, CreatedBy: "operator"})
	if err != nil || task.Task.Status != plan.StatusReady || len(task.Steps) != 3 || len(task.ImpactItems) != 0 {
		t.Fatal("Mesh did not plan three explicit independent cleanups", task, err)
	}
	attempt, err := planner.CreateExecution(ctx, cleanup.CreateExecutionRequest{ConnectionID: "connection", CleanupTaskID: task.Task.ID, RequestedBy: "operator", IdempotencyKey: "fleet-mesh-worker", Confirmation: cleanup.ExecutionConfirmation{Acknowledged: true}})
	if err != nil {
		t.Fatal(err)
	}
	steps := map[asset.AssetID]plan.CleanupTaskStep{}
	for _, step := range task.Steps {
		steps[step.AssetID] = step
	}
	if !slices.Contains(steps[member.ID].DependsOn, steps[mesh.ID].ID) || !slices.Contains(steps[member.ID].DependsOn, steps[namespace.ID].ID) {
		t.Fatal("saved member step lost namespace or Mesh ordering", steps[member.ID])
	}
	jobs, err := repository.ListJobsByAggregate(ctx, "cleanup_task", string(task.Task.ID))
	if err != nil {
		t.Fatal(err)
	}
	fallback := h.override
	var deleted []string
	h.override = func(req *http.Request) (*http.Response, bool) {
		if req.Method == "GET" {
			return fallback(req)
		}
		if strings.EqualFold(strings.TrimSuffix(req.URL.Path, "/apply"), h.id) {
			// The intent is already durable before the transport is invoked.
			action, err := repository.Executions().GetActionByExecutionStep(ctx, attempt.ID, string(steps[mesh.ID].ID))
			if err != nil || action.IdempotencyKey == "" {
				t.Fatal("Mesh mutation preceded durable intent", action, err)
			}
			h.request.IdempotencyKey = action.IdempotencyKey
			return fallback(req)
		}
		id := strings.ToLower(req.URL.Path)
		raw := h.resources[id]
		if req.Method != "DELETE" || id != member.Identity.NativeID && id != namespace.Identity.NativeID || req.URL.Query().Get("api-version") != fleetVersion || len(req.URL.Query()) != 1 || req.ContentLength > 0 || raw == nil || req.Header.Get("If-Match") == "" || req.Header.Get("If-Match") != text(raw["eTag"]) || req.Header.Get("x-ms-client-request-id") == "" {
			t.Fatal("Mesh worker changed another resource's mutation contract", req.Method, req.URL)
		}
		if id == member.Identity.NativeID && (h.resources[h.id] != nil || h.resources[namespace.Identity.NativeID] != nil) {
			t.Fatal("member deleted before both native prerequisites disappeared")
		}
		deleted = append(deleted, id)
		object(raw["properties"])["provisioningState"] = "Deleting"
		return jsonResponse(204, nil, nil), true
	}
	resume := func(value asset.Asset, pending bool) {
		t.Helper()
		var job execution.Job
		for _, candidate := range jobs {
			if text(candidate.Payload["cleanup_task_step_id"]) == string(steps[value.ID].ID) {
				job = candidate
			}
		}
		if job.ID == "" {
			t.Fatal("Mesh cleanup lost its persisted job")
		}
		wire, _ := json.Marshal(job)
		var restored execution.Job
		if json.Unmarshal(wire, &restored) != nil {
			t.Fatal("job serialization failed")
		}
		// A fresh provider and worker must recover every phase from SQLite.
		runtime, err := NewRuntime(h.runtime.credentials)
		if err != nil {
			t.Fatal(err)
		}
		runtime.transport = h.runtime.transport
		restarted := providerruntime.NewRegistry()
		if err := restarted.Register(runtime); err != nil {
			t.Fatal(err)
		}
		if err := restarted.RegisterBundle(runtime.Bundle()); err != nil {
			t.Fatal(err)
		}
		resolver := cleanup.ActionResolverFunc(func(ctx context.Context, value asset.Asset) (cleanup.ActionDriver, error) {
			return restarted.ResolveAction(ctx, value.Identity.ConnectionID, value)
		})
		worker := cleanup.NewExecutionHandler(cleanup.NewService(repository, restarted), resolver)
		var retry *cleanup.RetryError
		err = worker.Handle(ctx, restored)
		if pending && !errors.As(err, &retry) || !pending && err != nil {
			t.Fatal("Mesh worker failed to resume its saved phase", value.Identity.NativeType, pending, err)
		}
	}
	assertMutations := func(want ...string) {
		t.Helper()
		if !slices.Equal(h.mutations, want) || len(deleted) != 0 {
			t.Fatal("Mesh worker repeated or skipped a native phase", h.mutations, deleted)
		}
	}
	resume(mesh, true)
	assertMutations("PUT")
	resume(mesh, true)
	assertMutations("PUT", "POST")
	resume(mesh, true) // Native HTTP 200 still reports Applying.
	assertMutations("PUT", "POST")
	h.disconnected()
	resume(mesh, true)
	assertMutations("PUT", "POST", "DELETE")
	resume(mesh, true)
	stored, err := repository.GetAsset(ctx, mesh.ID)
	if err != nil || stored.ClosedAt != nil {
		t.Fatal("live Mesh closed after DELETE acknowledgment", stored, err)
	}
	delete(h.resources, h.id)
	resume(mesh, true) // Save successful Wait separately from final readback.
	resume(mesh, false)
	assertMutations("PUT", "POST", "DELETE")
	for _, value := range []asset.Asset{namespace, member} {
		before := len(deleted)
		resume(value, true)
		resume(value, true)
		stored, err := repository.GetAsset(ctx, value.ID)
		if err != nil || stored.ClosedAt != nil || len(deleted) != before+1 || deleted[before] != value.Identity.NativeID {
			t.Fatal("Mesh prerequisite recovery skipped absence or repeated DELETE", stored, deleted, err)
		}
		delete(h.resources, value.Identity.NativeID)
		resume(value, true)
		resume(value, false)
	}
	for _, value := range []asset.Asset{mesh, namespace, member} {
		stored, err := repository.GetAsset(ctx, value.ID)
		if err != nil || stored.ClosedAt == nil {
			t.Fatal("Mesh worker did not close verified absence", value.Identity.NativeType, err)
		}
	}
	if !slices.Equal(h.mutations, []string{"PUT", "POST", "DELETE"}) || len(deleted) != 2 || h.resources[fleetParent(h.id, fleetMeshType)] == nil || h.resources[fleetParent(h.id, fleetMeshType)+"/members/member2"] == nil {
		t.Fatal("Mesh workflow changed unrelated resources", h.mutations, deleted)
	}
	actions, err := repository.Executions().ListActions(ctx, attempt.ID)
	if err != nil || len(actions) != 3 || len(logs) == 0 {
		t.Fatal("Mesh execution evidence missing", err)
	}
	wire, err := json.Marshal(map[string]any{"task": task, "actions": actions, "logs": logs})
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{"env=production", "cilium-private-member", "mesh-private-configuration", "futurePrivateSetting", "mesh-prepared", "mesh-applying"} {
		if strings.Contains(string(wire), private) {
			t.Fatal("private Mesh data reached persisted execution or logs", private)
		}
	}
}
