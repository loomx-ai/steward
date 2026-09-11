package azure

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
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

func TestCommunicationRegisteredWorkersAndDurableRelease(t *testing.T) {
	f := newCommunicationFixture(t)
	f.override = func(req *http.Request) (*http.Response, bool) { return fleetGraphEmptyIndexes(t, req) }
	var logs []execution.JobLogEntry
	ctx := execution.WithJobLogSink(t.Context(), execution.JobLogSinkFunc(func(_ context.Context, entry execution.JobLogEntry) { logs = append(logs, entry) }))
	path := filepath.Join(t.TempDir(), "communication.db")
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
	if err := repository.PutScope(ctx, asset.Scope{ID: "root", ConnectionID: connection.ID, Kind: asset.ScopeSubscription, NativeID: testSubscription, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := repository.PutRegion(ctx, asset.ConnectionRegion{ID: "eastus", ConnectionID: connection.ID, RegionID: "eastus", Origin: asset.RegionOriginAPI, Lifecycle: asset.RegionActive, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	registry := providerruntime.NewRegistry()
	if err := registry.Register(f.runtime); err != nil {
		t.Fatal(err)
	}
	if err := registry.RegisterBundle(f.runtime.Bundle()); err != nil {
		t.Fatal(err)
	}
	creator, err := inventory.NewCreator(repository, registry)
	if err != nil {
		t.Fatal(err)
	}
	var kinds []asset.ResourceKindID
	for _, kind := range communicationTestKinds {
		kinds = append(kinds, f.runtime.resourceKind(kind).ID)
	}
	created, err := creator.Create(ctx, inventory.ScanCreationRequest{ConnectionID: connection.ID, RequestedBy: "communication-worker", RegionMode: inventory.RegionModeSelected, RegionIDs: []string{"eastus", "global"}, ResourceKindIDs: kinds})
	if err != nil || len(created.Shards) != 10 {
		t.Fatal("Communication scan lost its ten global native collections", len(created.Shards), err)
	}
	handler := inventory.NewScanHandler(repository, registry, inventory.NewService(repository))
	for _, job := range created.Jobs {
		if err := handler.Handle(ctx, job); err != nil {
			t.Fatal("registered native Communication inventory", err)
		}
	}
	values, err := repository.ListActiveAssetsByConnection(ctx, connection.ID, "")
	if err != nil || len(values) != 10 {
		t.Fatal("registered Communication inventory lost native assets", len(values), err)
	}
	for _, value := range values {
		if value.Location != "global" || !value.Capabilities.Has(asset.CapabilityActionable) || value.Normalized["_inventory_source"] != communicationInventorySource {
			t.Fatal("registered Communication metadata lost actionability or source", value.Identity.NativeType)
		}
	}
	jobs, err := repository.ListJobsByAggregate(ctx, "scan_task", string(created.ScanRun.ID))
	if err != nil {
		t.Fatal(err)
	}
	graphs := 0
	for _, job := range jobs {
		if job.Type == execution.JobGraph {
			graphs++
			if err := governance.NewGraphHandler(repository, registry, fleetHubGraphContributors{f.runtime}).Handle(ctx, job); err != nil {
				t.Fatal("registered Communication graph", err)
			}
		}
	}
	if graphs != 1 {
		t.Fatal("Communication scan lost graph reconciliation", graphs)
	}
	comm, email, phone := cdnAsset(t, values, communicationType), cdnAsset(t, values, communicationEmailType), cdnAsset(t, values, communicationPhoneType)
	planner := cleanup.NewService(repository, registry)
	blocked, err := planner.CreateTask(ctx, cleanup.CreateTaskRequest{ConnectionID: connection.ID, Selectors: []plan.CleanupSelector{{Kind: plan.SelectorAsset, AssetID: email.ID}}, CreatedBy: "operator"})
	if err != nil || blocked.Task.Status == plan.StatusReady || len(blocked.Task.Blockers) == 0 {
		t.Fatal("email deletion bypassed its shared native account", blocked.Task.Status, err)
	}
	task, err := planner.CreateTask(ctx, cleanup.CreateTaskRequest{ConnectionID: connection.ID, Selectors: []plan.CleanupSelector{{Kind: plan.SelectorAsset, AssetID: email.ID}, {Kind: plan.SelectorAsset, AssetID: comm.ID}}, CreatedBy: "operator"})
	if err != nil || task.Task.Status != plan.StatusReady || len(task.Steps) != 9 || len(task.ImpactItems) != 1 {
		t.Fatal("registered Communication plan changed native lifecycles", task.Task.Status, task.Task.Blockers, len(task.Steps), len(task.ImpactItems), err)
	}
	attempt, err := planner.CreateExecution(ctx, cleanup.CreateExecutionRequest{ConnectionID: connection.ID, CleanupTaskID: task.Task.ID, RequestedBy: "operator", IdempotencyKey: "communication-worker-delete", Confirmation: cleanup.ExecutionConfirmation{Acknowledged: true}})
	if err != nil {
		t.Fatal(err)
	}
	steps := map[asset.AssetID]plan.CleanupTaskStep{}
	for _, step := range task.Steps {
		steps[step.AssetID] = step
	}
	deleted := []string{}
	polls := map[string]int{}
	terminal := map[string]bool{}
	operations := map[string]string{}
	original := f.override
	f.override = func(req *http.Request) (*http.Response, bool) {
		id := strings.ToLower(req.URL.Path)
		if req.URL.Host != "management.azure.com" {
			id = "https://" + req.URL.Host + req.URL.Path
		}
		if resourceID := operations[req.URL.Path]; resourceID != "" {
			polls[resourceID]++
			if req.Method != "GET" || req.URL.Query().Get("api-version") != communicationARMVersion {
				t.Fatal("worker changed the native polling contract")
			}
			status, state := 202, "Deleting"
			headers := http.Header{"Azure-Asyncoperation": {communicationSignedPollURL(resourceID, "communication-private-rotated-signature")}, "Location": {communicationSignedPollURL(resourceID, "communication-private-secondary-signature")}}
			if terminal[resourceID] {
				status, state, headers = 200, "Succeeded", nil
			}
			return jsonResponse(status, map[string]any{"id": req.URL.Path, "name": last(req.URL.Path), "resourceId": resourceID, "status": state, "properties": map[string]any{"privateCredential": "communication-private-poll-value"}}, headers), true
		}
		if req.Method != "DELETE" {
			return original(req)
		}
		var value asset.Asset
		for _, candidate := range values {
			if candidate.Identity.NativeID == id {
				value = candidate
			}
		}
		action, err := repository.Executions().GetActionByExecutionStep(ctx, attempt.ID, string(steps[value.ID].ID))
		if value.ID == "" || value.ID == phone.ID || err != nil || action.IdempotencyKey == "" || req.Header.Get("x-ms-client-request-id") != azureRequestID(action.IdempotencyKey) || slices.Contains(deleted, id) {
			t.Fatal("Communication mutation lacked durable intent or repeated a delete", value.Identity.NativeType, err)
		}
		version := communicationARMVersion
		if isCommunicationDataType(value.Identity.NativeType) {
			version = communicationVersion(req.URL.Path)
		}
		if req.URL.Query().Get("api-version") != version || len(req.URL.Query()) != 1 || req.ContentLength > 0 {
			t.Fatal("worker changed native Communication DELETE")
		}
		deleted = append(deleted, id)
		if value.Identity.NativeType == communicationType || value.Identity.NativeType == communicationEmailType || value.Identity.NativeType == communicationDomainType {
			object(f.resources[id]["properties"])["provisioningState"] = "Deleting"
			operation := communicationSignedPollURL(id, "communication-private-original-signature")
			u, _ := url.Parse(operation)
			operations[u.Path] = id
			return jsonResponse(202, nil, http.Header{"Azure-Asyncoperation": {operation}, "Location": {communicationSignedPollURL(id, "communication-private-initial-secondary")}}), true
		}
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
			t.Fatal("Communication task lost executable dependency order")
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
			t.Fatal("Communication task lost persisted job")
		}
		resume := func(pending bool) {
			t.Helper()
			wire, _ := json.Marshal(job)
			var restored execution.Job
			if json.Unmarshal(wire, &restored) != nil {
				t.Fatal("job serialization failed")
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
			if pending && !errors.As(err, &retry) || !pending && err != nil {
				t.Fatal("Communication worker lost its persisted deletion phase", value.Identity.NativeType, pending, err)
			}
		}
		before := len(deleted)
		resume(true)
		resume(true)
		if len(deleted) != before+1 || deleted[before] != value.Identity.NativeID {
			t.Fatal("restarted Communication worker repeated or skipped native delete", deleted)
		}
		if _, exists := polls[value.Identity.NativeID]; exists {
			current, err := repository.Executions().GetActionByExecutionStep(ctx, attempt.ID, string(step.ID))
			if err != nil || current.ProviderResult["communication_phase"] != "poll" || !strings.Contains(text(current.ProviderResult["communication_operation"]), "communication-private-rotated-signature") {
				t.Fatal("native rotated poll receipt was not durable", err)
			}
			terminal[value.Identity.NativeID] = true
			resume(true)
			current, err = repository.Executions().GetActionByExecutionStep(ctx, attempt.ID, string(step.ID))
			if err != nil || current.ProviderResult["communication_phase"] != "absence" {
				t.Fatal("successful native operation did not persist absence phase", err)
			}
		}
		stored, err := repository.GetAsset(ctx, value.ID)
		if err != nil || stored.ClosedAt != nil {
			t.Fatal("native receipt closed a live Communication resource", err)
		}
		beforePolls := polls[value.Identity.NativeID]
		delete(f.resources, value.Identity.NativeID)
		if value.ID == comm.ID {
			current, err := repository.Executions().GetActionByExecutionStep(ctx, attempt.ID, string(step.ID))
			if err != nil || current.DeletionCheckStartedAt == nil {
				t.Fatal("phone release window was not durable", err)
			}
			started := time.Now().Add(-32 * 24 * time.Hour)
			current.DeletionCheckStartedAt = &started
			if err := repository.Executions().UpdateAction(ctx, current); err != nil {
				t.Fatal(err)
			}
			resume(true)
			for _, id := range []asset.AssetID{comm.ID, phone.ID} {
				stored, err := repository.GetAsset(ctx, id)
				if err != nil || stored.ClosedAt != nil {
					t.Fatal("account 404 closed an unverified released phone", err)
				}
			}
			delete(f.resources, phone.Identity.NativeID)
		}
		resume(true)
		resume(false)
		stored, err = repository.GetAsset(ctx, value.ID)
		if err != nil || stored.ClosedAt == nil || len(deleted) != before+1 || polls[value.Identity.NativeID] != beforePolls {
			t.Fatal("worker did not finish own-resource and residual 404 verification", value.Identity.NativeType, err)
		}
		completed[step.ID] = true
	}
	remaining, err := repository.ListActiveAssetsByConnection(ctx, connection.ID, "")
	if err != nil || len(remaining) != 0 || len(deleted) != 9 {
		t.Fatal("registered Communication cleanup left active assets", len(remaining), len(deleted), err)
	}
	journal, err := repository.Executions().ListActions(ctx, attempt.ID)
	if err != nil || len(journal) != 9 || len(logs) == 0 {
		t.Fatal("Communication execution journal missing", len(journal), err)
	}
	payload, _ := json.Marshal(map[string]any{"task": task, "journal": journal, "logs": logs})
	for _, secret := range []string{"private-native-configuration", "futurePrivateSetting", "communication-private-poll-value"} {
		if strings.Contains(string(payload), secret) {
			t.Fatal("private native Communication data reached public execution evidence", secret)
		}
	}
	logged, _ := json.Marshal(logs)
	for _, signature := range []string{"communication-private-original-signature", "communication-private-initial-secondary", "communication-private-rotated-signature", "communication-private-secondary-signature"} {
		if strings.Contains(string(logged), signature) {
			t.Fatal("native poll signing parameters entered job logs")
		}
	}
}
