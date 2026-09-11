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

func TestDomainRegisteredWorkersAndDurableDelay(t *testing.T) {
	s, r, _ := domainScenario(t, true)
	var logs []execution.JobLogEntry
	ctx := execution.WithJobLogSink(t.Context(), execution.JobLogSinkFunc(func(_ context.Context, entry execution.JobLogEntry) { logs = append(logs, entry) }))
	path := filepath.Join(t.TempDir(), "registered-domains.db")
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
	if err := registry.Register(r); err != nil {
		t.Fatal(err)
	}
	if err := registry.RegisterBundle(r.Bundle()); err != nil {
		t.Fatal(err)
	}
	creator, err := inventory.NewCreator(repository, registry)
	if err != nil {
		t.Fatal(err)
	}
	var kinds []asset.ResourceKindID
	for _, kind := range []string{domainType, domainOwnershipType, publicDNSZoneType, appSiteType, appSlotType, appFunctionType, appSlotFunctionType, appCertificateType, appSiteCertificateType, appSlotCertificateType, appBindingType, appSlotBindingType} {
		kinds = append(kinds, r.resourceKind(kind).ID)
	}
	created, err := creator.Create(ctx, inventory.ScanCreationRequest{ConnectionID: connection.ID, RequestedBy: "domain-test", RegionMode: inventory.RegionModeSelected, RegionIDs: []string{"eastus", "global"}, ResourceKindIDs: kinds})
	if err != nil || len(created.Shards) != 21 {
		t.Fatal("domain scan did not combine global registration and native app child scopes", len(created.Shards), err)
	}
	handler := inventory.NewScanHandler(repository, registry, inventory.NewService(repository))
	for _, job := range created.Jobs {
		if err := handler.Handle(ctx, job); err != nil {
			t.Fatal("registered domain inventory worker", err)
		}
	}
	values, err := repository.ListActiveAssetsByConnection(ctx, connection.ID, "")
	if err != nil || len(values) != 14 {
		t.Fatal("registered native inventory lost a domain/app/DNS asset", len(values), err)
	}
	domain := cdnAsset(t, values, domainType)
	if domain.Location != "global" || !domain.Capabilities.Has(asset.CapabilityActionable) || len(object(domain.Normalized[domainDependencies])) != 3 {
		t.Fatal("registered inventory did not preserve global domain proof")
	}
	jobs, err := repository.ListJobsByAggregate(ctx, "scan_task", string(created.ScanRun.ID))
	if err != nil {
		t.Fatal(err)
	}
	graphJobs := 0
	for _, job := range jobs {
		if job.Type == execution.JobGraph {
			graphJobs++
			if err := governance.NewGraphHandler(repository, registry, fleetHubGraphContributors{r}).Handle(ctx, job); err != nil {
				t.Fatal("registered native domain graph worker", err)
			}
		}
	}
	if graphJobs != 1 {
		t.Fatal("domain inventory did not schedule graph reconciliation", graphJobs)
	}
	planner := cleanup.NewService(repository, registry)
	task, err := planner.CreateTask(ctx, cleanup.CreateTaskRequest{ConnectionID: connection.ID, Selectors: []plan.CleanupSelector{{Kind: plan.SelectorAsset, AssetID: domain.ID}}, CreatedBy: "operator"})
	if err != nil || task.Task.Status != plan.StatusReady || len(task.Steps) != 4 || len(task.ImpactItems) != 0 {
		t.Fatal("registered domain plan lost independent cleanup order", task.Task.Status, task.Task.Blockers, len(task.Steps), len(task.ImpactItems), err)
	}
	steps := map[asset.AssetID]plan.CleanupTaskStep{}
	for _, step := range task.Steps {
		steps[step.AssetID] = step
	}
	if len(steps[domain.ID].DependsOn) != 3 {
		t.Fatal("domain did not wait for all independently reviewed dependencies")
	}
	attempt, err := planner.CreateExecution(ctx, cleanup.CreateExecutionRequest{ConnectionID: connection.ID, CleanupTaskID: task.Task.ID, RequestedBy: "operator", IdempotencyKey: "domain-worker", Confirmation: cleanup.ExecutionConfirmation{Acknowledged: true}})
	if err != nil {
		t.Fatal(err)
	}
	deleted := []string{}
	s.handle = func(req *http.Request) (*http.Response, bool) {
		if req.Method != "DELETE" {
			return nil, false
		}
		id := strings.ToLower(req.URL.Path)
		var value asset.Asset
		for _, candidate := range values {
			if candidate.Identity.NativeID == id {
				value = candidate
			}
		}
		action, err := repository.Executions().GetActionByExecutionStep(ctx, attempt.ID, string(steps[value.ID].ID))
		if value.ID == "" || err != nil || action.IdempotencyKey == "" || req.Header.Get("x-ms-client-request-id") != azureRequestID(action.IdempotencyKey) {
			t.Fatal("domain mutation preceded its durable intent", action, err)
		}
		version := appServiceVersion
		if isDomainType(value.Identity.NativeType) {
			version = domainVersion
		}
		if req.URL.Query().Get("api-version") != version || req.ContentLength > 0 || req.Header.Get("If-Match") != "" {
			t.Fatal("domain workflow changed the native delete contract", req.URL)
		}
		if value.ID == domain.ID {
			if len(deleted) != 3 || req.URL.Query().Get("forceHardDeleteDomain") != "false" {
				t.Fatal("registration deleted before independent prerequisites or forced the native delay")
			}
			object(s.records[id]["properties"])["provisioningState"] = "Deleting"
		}
		deleted = append(deleted, id)
		return jsonResponse(200, nil, http.Header{"X-Ms-Request-Id": {"native-domain-workflow"}}), true
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
			t.Fatal("domain plan lost its next executable step")
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
			t.Fatal("domain step has no saved execution job")
		}
		resume := func(pending bool) {
			t.Helper()
			payload, _ := json.Marshal(job)
			var restored execution.Job
			if err := json.Unmarshal(payload, &restored); err != nil {
				t.Fatal(err)
			}
			repository, err = sqlite.Open(path, "../../migrations")
			if err != nil {
				t.Fatal(err)
			}
			fresh, err := NewRuntime(r.credentials)
			if err != nil {
				t.Fatal(err)
			}
			fresh.transport = r.transport
			registry := providerruntime.NewRegistry()
			if err := registry.Register(fresh); err != nil {
				t.Fatal(err)
			}
			if err := registry.RegisterBundle(fresh.Bundle()); err != nil {
				t.Fatal(err)
			}
			resolver := cleanup.ActionResolverFunc(func(ctx context.Context, value asset.Asset) (cleanup.ActionDriver, error) {
				return registry.ResolveAction(ctx, value.Identity.ConnectionID, value)
			})
			worker := cleanup.NewExecutionHandler(cleanup.NewService(repository, registry), resolver)
			var retry *cleanup.RetryError
			err = worker.Handle(ctx, restored)
			if pending && !errors.As(err, &retry) || !pending && err != nil {
				t.Fatal("domain worker lost durable delay/absence state", value.Identity.NativeType, pending, err)
			}
		}
		before := len(deleted)
		resume(true)
		if value.ID == domain.ID {
			resume(true)
			if len(deleted) != before {
				t.Fatal("domain deletion bypassed a delayed hostname index")
			}
			current, err := repository.Executions().GetActionByExecutionStep(ctx, attempt.ID, string(step.ID))
			if err != nil || current.ProviderResult["domain_phase"] != "hostnames" {
				t.Fatal("domain hostname wait was not durable", err)
			}
			object(s.records[domain.Identity.NativeID]["properties"])["managedHostNames"] = []any{}
			resume(true)
			current, err = repository.Executions().GetActionByExecutionStep(ctx, attempt.ID, string(step.ID))
			if err != nil || current.DeletionCheckStartedAt == nil {
				t.Fatal("domain did not persist the start of its native delay", err)
			}
			started := time.Now().Add(-25 * time.Hour)
			current.DeletionCheckStartedAt = &started
			if err := repository.Executions().UpdateAction(ctx, current); err != nil {
				t.Fatal(err)
			}
		}
		resume(true)
		if len(deleted) != before+1 || deleted[before] != value.Identity.NativeID {
			t.Fatal("restarted domain execution repeated or skipped deletion", deleted)
		}
		stored, err := repository.GetAsset(ctx, value.ID)
		if err != nil || stored.ClosedAt != nil {
			t.Fatal("native receipt closed a live domain dependency", err)
		}
		s.gone[value.Identity.NativeID] = true
		if isAppBinding(value.Identity.NativeType) {
			// Native app/slot hostname indexes change after binding removal.
			props := object(s.records[redisParentID(value.Identity.NativeID)]["properties"])
			for _, field := range []string{"hostNames", "enabledHostNames"} {
				props[field] = slices.DeleteFunc(slices.Clone(array(props[field])), func(item any) bool { return strings.EqualFold(text(item), last(value.Identity.NativeID)) })
			}
			props["hostNameSslStates"] = slices.DeleteFunc(slices.Clone(array(props["hostNameSslStates"])), func(item any) bool {
				return strings.EqualFold(text(object(item)["name"]), last(value.Identity.NativeID))
			})
		}
		if value.ID == domain.ID {
			// A known child can outlive an absent parent; its own GET gates close.
			child := cdnAsset(t, values, domainOwnershipType)
			s.gone[child.Identity.NativeID] = false
			resume(true)
			s.gone[child.Identity.NativeID] = true
		}
		resume(true)
		resume(false)
		stored, err = repository.GetAsset(ctx, value.ID)
		if err != nil || stored.ClosedAt == nil || len(deleted) != before+1 {
			t.Fatal("domain worker failed final native readback", err)
		}
		completed[step.ID] = true
	}
	if len(deleted) != 4 || deleted[3] != domain.Identity.NativeID {
		t.Fatal("registration was not the last native deletion", deleted)
	}
	remaining, err := repository.ListActiveAssetsByConnection(ctx, connection.ID, "")
	if err != nil || len(remaining) != 10 || s.gone[cdnAsset(t, values, publicDNSZoneType).Identity.NativeID] {
		t.Fatal("domain cleanup changed retained DNS/apps/certificates", len(remaining), err)
	}
	journal, err := repository.Executions().ListActions(ctx, attempt.ID)
	if err != nil || len(journal) != 4 || len(logs) == 0 {
		t.Fatal("domain execution did not preserve its journal", err)
	}
	payload, _ := json.Marshal(map[string]any{"task": task, "journal": journal, "logs": logs})
	for _, secret := range []string{"exampleAuthCode", "admin@email.com", "3400 State St", "agreementKey1", "domain-private-ownership-token", "domain-private-future-value"} {
		if strings.Contains(string(payload), secret) {
			t.Fatal("registered domain workflow exposed native private fields", secret)
		}
	}
}
