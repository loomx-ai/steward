package azure

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/loomx-ai/steward/internal/app/cleanup"
	"github.com/loomx-ai/steward/internal/app/governance"
	"github.com/loomx-ai/steward/internal/app/inventory"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/internal/persistence/sqlite"
	providerruntime "github.com/loomx-ai/steward/internal/provider/runtime"
)

func TestFleetRootRegisteredWorkersAndResidualRecovery(t *testing.T) {
	var logs []execution.JobLogEntry
	ctx := execution.WithJobLogSink(t.Context(), execution.JobLogSinkFunc(func(_ context.Context, entry execution.JobLogEntry) { logs = append(logs, entry) }))
	h := newFleetHubMembersFixture(t)
	mesh := fleetTestBody(t, fleetMeshType, "mesh1")
	object(mesh["properties"])["memberSelector"] = map[string]any{"byLabel": ""}
	object(object(mesh["properties"])["status"])["state"] = "NotConnected"
	h.fleetFixture.resources[text(mesh["id"])] = mesh
	for _, raw := range h.fleetFixture.resources {
		if raw["type"] == fleetRunType {
			object(object(object(raw["properties"])["status"])["status"])["state"] = "Completed"
		}
	}
	hubAssets := h.graphAssets(t)
	path := filepath.Join(t.TempDir(), "fleet-root.db")
	repository, err := sqlite.Open(path, "../../migrations")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	connection := asset.CloudConnection{ID: "connection", Provider: asset.ProviderAzure, Partition: "azure", Status: asset.ConnectionActive, CreatedAt: now, UpdatedAt: now}
	if err := repository.PutConnection(ctx, connection); err != nil {
		t.Fatal(err)
	}
	if err := repository.PutCredential(ctx, asset.ConnectionCredential{ConnectionID: connection.ID, Provider: asset.ProviderAzure, Type: asset.CredentialAzureServicePrincipal, EnvelopeVersion: 1, Nonce: "test", Ciphertext: "test", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	scope := asset.Scope{ID: "root", ConnectionID: connection.ID, Kind: asset.ScopeSubscription, NativeID: testSubscription, CreatedAt: now, UpdatedAt: now}
	if err := repository.PutScope(ctx, scope); err != nil {
		t.Fatal(err)
	}
	for _, region := range []string{"westus", "eastus"} {
		if err := repository.PutRegion(ctx, asset.ConnectionRegion{ID: region, ConnectionID: connection.ID, RegionID: region, Origin: asset.RegionOriginAPI, Lifecycle: asset.RegionActive, CreatedAt: now, UpdatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	registry := providerruntime.NewRegistry()
	if err := registry.Register(h.runtime); err != nil {
		t.Fatal(err)
	}
	if err := registry.RegisterBundle(h.runtime.Bundle()); err != nil {
		t.Fatal(err)
	}
	creator, err := inventory.NewCreator(repository, registry)
	if err != nil {
		t.Fatal(err)
	}
	var kinds []asset.ResourceKindID
	for _, kind := range append(slices.Clone(fleetTestKinds), fleetMeshType) {
		kinds = append(kinds, h.runtime.resourceKind(kind).ID)
	}
	created, err := creator.Create(ctx, inventory.ScanCreationRequest{ConnectionID: connection.ID, RequestedBy: "fleet-root-worker", RegionMode: inventory.RegionModeSelected, RegionIDs: []string{"westus", "eastus"}, ResourceKindIDs: kinds})
	if err != nil || len(created.Shards) != 16 {
		t.Fatal("root scan did not use all registered native Fleet collections", created, err)
	}
	handler := inventory.NewScanHandler(repository, registry, inventory.NewService(repository))
	for _, job := range created.Jobs {
		if err := handler.Handle(ctx, job); err != nil {
			t.Fatal(err)
		}
	}
	for _, shard := range created.Shards {
		stored, err := repository.GetScanShard(ctx, shard.ID)
		if err != nil || stored.Status != asset.ShardSucceeded || stored.Authoritative || stored.Coverage.Authoritative {
			t.Fatal("root source lost native scan coverage boundaries", stored, err)
		}
	}
	values, err := repository.ListActiveAssetsByConnection(ctx, connection.ID, "")
	if err != nil || len(values) != 8 {
		t.Fatal("Fleet registered inventory lost a native resource", err, len(values))
	}
	root := fleetAssetByKind(t, values, fleetType)
	if !root.Capabilities.Has(asset.CapabilityActionable) || len(object(object(root.Normalized[fleetHubState])["members"])) != 17 || len(object(object(root.Normalized[fleetHubState])["children"])) != 7 {
		t.Fatal("root inventory lost its reviewed Hub and independent children")
	}
	// Hub resources use their real product normalizers; the Fleet scan above
	// supplies the root's authoritative ownership observation and private proof.
	for _, value := range hubAssets {
		if fleetKind(value.Identity.NativeType).kind != "" {
			continue
		}
		value.ScopeID, value.ResourceKindID = root.ScopeID, h.runtime.resourceKind(value.Identity.NativeType).ID
		value.FirstSeenAt, value.LastSeenAt = now, now
		if err := repository.PutAsset(ctx, value); err != nil {
			t.Fatal(err)
		}
	}
	jobs, err := repository.ListJobsByAggregate(ctx, "scan_task", string(created.ScanRun.ID))
	if err != nil {
		t.Fatal(err)
	}
	graphJobs := 0
	for _, job := range jobs {
		if job.Type == execution.JobGraph {
			graphJobs++
			if err := governance.NewGraphHandler(repository, registry, fleetHubGraphContributors{h.runtime}).Handle(ctx, job); err != nil {
				t.Fatal("root graph worker rejected the complete native ownership tree", err)
			}
		}
	}
	if graphJobs != 1 {
		t.Fatal("root inventory did not schedule native graph reconciliation", graphJobs)
	}
	planner := cleanup.NewService(repository, registry)
	selectors := []plan.CleanupSelector{{Kind: plan.SelectorAsset, AssetID: root.ID}}
	blocked, err := planner.CreateTask(ctx, cleanup.CreateTaskRequest{ConnectionID: "connection", Selectors: selectors, CreatedBy: "operator", RequestOptions: map[asset.AssetID]map[string]any{root.ID: {"retain_resources": []string{h.cluster}}}})
	if err != nil || blocked.Task.Status == plan.StatusReady || len(blocked.Task.Blockers) == 0 {
		t.Fatal("root selection ignored retained Hub ownership", blocked.Task.Status, blocked.Task.Blockers, err)
	}
	task, err := planner.CreateTask(ctx, cleanup.CreateTaskRequest{ConnectionID: "connection", Selectors: selectors, CreatedBy: "operator"})
	if err != nil || task.Task.Status != plan.StatusReady || len(task.Steps) != 7 || len(task.ImpactItems) != 18 {
		t.Fatal("root did not plan six independent prerequisites and 17 native Hub impacts", task.Task.Status, task.Task.Blockers, len(task.Steps), len(task.ImpactItems), err)
	}
	attempt, err := planner.CreateExecution(ctx, cleanup.CreateExecutionRequest{ConnectionID: "connection", CleanupTaskID: task.Task.ID, RequestedBy: "operator", IdempotencyKey: "fleet-root-worker", Confirmation: cleanup.ExecutionConfirmation{Acknowledged: true}})
	if err != nil {
		t.Fatal(err)
	}
	steps := map[asset.AssetID]plan.CleanupTaskStep{}
	for _, step := range task.Steps {
		steps[step.AssetID] = step
	}
	rootStep := steps[root.ID]
	if len(rootStep.DependsOn) != 6 {
		t.Fatal("root lost independently reviewed prerequisites", rootStep)
	}
	for _, impact := range task.ImpactItems {
		want := rootStep.ID
		if impact.AssetID == fleetAssetByKind(t, values, fleetGateType).ID {
			want = steps[fleetAssetByKind(t, values, fleetRunType).ID].ID
		}
		if impact.DelegatedTo != want {
			t.Fatal("Hub or Gate was delegated to the wrong deletion", impact)
		}
	}
	fallback := h.override
	deleted := []string{}
	h.override = func(req *http.Request) (*http.Response, bool) {
		if req.Method != "DELETE" {
			return fallback(req)
		}
		id := strings.ToLower(req.URL.Path)
		var value asset.Asset
		for _, candidate := range values {
			if candidate.Identity.NativeID == id {
				value = candidate
			}
		}
		raw := h.fleetFixture.resources[id]
		if value.ID == "" || raw == nil || req.URL.Query().Get("api-version") != fleetAPIVersion(value.Identity.NativeType, "DELETE") || len(req.URL.Query()) != 1 || req.ContentLength > 0 || req.Header.Get("If-Match") == "" || req.Header.Get("If-Match") != text(raw["eTag"]) {
			t.Fatal("root workflow changed a native mutation contract", req.Method, req.URL)
		}
		action, err := repository.Executions().GetActionByExecutionStep(ctx, attempt.ID, string(steps[value.ID].ID))
		if err != nil || action.IdempotencyKey == "" || req.Header.Get("x-ms-client-request-id") != azureRequestID(action.IdempotencyKey+":fleet:delete") {
			t.Fatal("root mutation preceded its durable intent", action, err)
		}
		if value.ID == root.ID && len(h.fleetFixture.resources) != 1 {
			t.Fatal("root DELETE preceded native child absence")
		}
		deleted = append(deleted, id)
		object(raw["properties"])["provisioningState"] = "Deleting"
		return jsonResponse(204, nil, nil), true
	}
	completed := map[plan.StepID]bool{}
	for range len(task.Steps) {
		var step plan.CleanupTaskStep
		for _, candidate := range task.Steps {
			if !completed[candidate.ID] && !slices.ContainsFunc(candidate.DependsOn, func(id plan.StepID) bool { return !completed[id] }) {
				step = candidate
				break
			}
		}
		if step.ID == "" {
			t.Fatal("root plan had no next executable step")
		}
		value, err := repository.GetAsset(ctx, step.AssetID)
		if err != nil {
			t.Fatal(err)
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
		if job.ID == "" {
			t.Fatal("root execution lost its saved step job")
		}
		resume := func(pending bool) {
			t.Helper()
			wire, _ := json.Marshal(job)
			var restored execution.Job
			if json.Unmarshal(wire, &restored) != nil {
				t.Fatal("root job could not be restored")
			}
			// Reopen SQLite and recreate the provider, registry and worker on
			// every retry. Only the simulated native API retains mutable state.
			repository, err = sqlite.Open(path, "../../migrations")
			if err != nil {
				t.Fatal(err)
			}
			r, err := NewRuntime(h.runtime.credentials)
			if err != nil {
				t.Fatal(err)
			}
			r.transport = h.runtime.transport
			restarted := providerruntime.NewRegistry()
			if err := restarted.Register(r); err != nil {
				t.Fatal(err)
			}
			if err := restarted.RegisterBundle(r.Bundle()); err != nil {
				t.Fatal(err)
			}
			resolver := cleanup.ActionResolverFunc(func(ctx context.Context, value asset.Asset) (cleanup.ActionDriver, error) {
				return restarted.ResolveAction(ctx, value.Identity.ConnectionID, value)
			})
			worker := cleanup.NewExecutionHandler(cleanup.NewService(repository, restarted), resolver)
			var retry *cleanup.RetryError
			err = worker.Handle(ctx, restored)
			if pending && !errors.As(err, &retry) || !pending && err != nil {
				t.Fatal("root worker lost durable recovery state", value.Identity.NativeType, pending, err)
			}
		}
		before := len(deleted)
		resume(true)
		resume(true)
		if len(deleted) != before+1 || deleted[before] != value.Identity.NativeID {
			t.Fatal("root workflow repeated or skipped DELETE", deleted, value.Identity.NativeID)
		}
		stored, err := repository.GetAsset(ctx, value.ID)
		if err != nil || stored.ClosedAt != nil {
			t.Fatal("DELETE acknowledgment closed a live resource", err)
		}
		delete(h.fleetFixture.resources, value.Identity.NativeID)
		if value.Identity.NativeType == fleetRunType {
			resume(true)
			delete(h.fleetFixture.resources, fleetAssetByKind(t, values, fleetGateType).Identity.NativeID)
		}
		if value.ID == root.ID {
			resume(true)
			// Resource-group absence still cannot hide a typed descendant.
			h.gone[h.hub], h.gone[h.nodes] = true, true
			resume(true)
			members := object(object(root.Normalized[fleetHubState])["members"])
			external := "/subscriptions/" + testSubscription + "/resourcegroups/shared/providers/microsoft.compute/disks/scale-data"
			for id := range members {
				h.gone[id] = id != external
			}
			resume(true)
			for id := range members {
				stored, err := repository.GetAsset(ctx, asset.AssetID(id))
				if err != nil || stored.ClosedAt != nil {
					t.Fatal("unfinished root cascade closed a managed impact", id, err)
				}
			}
			h.gone[external] = true
		}
		clear(h.calls)
		resume(true)
		resume(false)
		stored, err = repository.GetAsset(ctx, value.ID)
		if err != nil || stored.ClosedAt == nil || len(deleted) != before+1 {
			t.Fatal("root worker failed final independent native absence", value.Identity.NativeType, err, deleted)
		}
		completed[step.ID] = true
	}
	if len(deleted) != 7 || deleted[len(deleted)-1] != root.Identity.NativeID {
		t.Fatal("root deletion was not the final independent mutation", deleted)
	}
	for id := range object(object(root.Normalized[fleetHubState])["members"]) {
		stored, err := repository.GetAsset(ctx, asset.AssetID(id))
		if err != nil || stored.ClosedAt == nil {
			t.Fatal("verified root cascade did not close its managed impact", id, err)
		}
	}
	remaining, err := repository.ListActiveAssetsByConnection(ctx, connection.ID, "")
	if err != nil || len(remaining) != 4 {
		t.Fatal("root cascade changed shared zones, manual records or unrelated groups", remaining, err)
	}
	actions, err := repository.Executions().ListActions(ctx, attempt.ID)
	if err != nil || len(actions) != 7 || len(logs) == 0 {
		t.Fatal("root execution journal is incomplete", err)
	}
	// Other assets retain their normal product inventory projections. The
	// Fleet proof, operation journal and API logs must not copy full Hub bodies.
	data, _ := json.Marshal(map[string]any{"root": rootStep.Evidence, "actions": actions, "logs": logs})
	for _, private := range []string{"fleet-private-configuration", "hub-authored-secret", "hub-private-proof", "hub-etag", "unknown-private-secret", "futurePrivateSetting"} {
		if strings.Contains(string(data), private) {
			t.Fatal("root proof, operation journal or logs leaked native Hub configuration", private)
		}
	}
}
