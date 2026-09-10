package azure

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
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

type diagnosticContributors struct{ runtime *Runtime }

func (r diagnosticContributors) ResolveContributors(ctx context.Context, connection asset.CloudConnection, _ []asset.Asset) ([]governance.Contributor, error) {
	lifecycle, err := r.runtime.ServiceLifecycle(ctx, connection.ID)
	return []governance.Contributor{NewResourceAttachments(), lifecycle}, err
}

func TestDiagnosticRegisteredScanWorkerAndCleanupRecovery(t *testing.T) {
	for _, nativeCase := range []bool{false, true} {
		t.Run(map[bool]string{false: "arm-source", true: "native-source-selector"}[nativeCase], func(t *testing.T) {
			testDiagnosticRegisteredScanWorkerAndCleanupRecovery(t, nativeCase)
		})
	}
}

func testDiagnosticRegisteredScanWorkerAndCleanupRecovery(t *testing.T, nativeCase bool) {
	ctx := t.Context()
	f := newDiagnosticFixture(t)
	if nativeCase {
		f = newDiagnosticCaseFixture(t, true)
	}
	r := f.runtime
	repository, err := sqlite.Open(filepath.Join(t.TempDir(), "diagnostic.db"), "../../migrations")
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
	root := asset.Scope{ID: "root", ConnectionID: connection.ID, Kind: asset.ScopeSubscription, NativeID: testSubscription, CreatedAt: now, UpdatedAt: now}
	if err := repository.PutScope(ctx, root); err != nil {
		t.Fatal(err)
	}
	if err := repository.PutRegion(ctx, asset.ConnectionRegion{ID: "west", ConnectionID: connection.ID, RegionID: "westus", Origin: asset.RegionOriginAPI, Lifecycle: asset.RegionActive, CreatedAt: now, UpdatedAt: now}); err != nil {
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
	handler := inventory.NewScanHandler(repository, registry, inventory.NewService(repository))
	scan := func(failure bool) []asset.Asset {
		t.Helper()
		created, err := creator.Create(ctx, inventory.ScanCreationRequest{ConnectionID: connection.ID, RequestedBy: "diagnostic-native-worker", RegionMode: inventory.RegionModeSelected, RegionIDs: []string{"westus", "global"}, ResourceKindIDs: []asset.ResourceKindID{r.resourceKind(diagnosticSettingsType).ID}})
		if err != nil || len(created.Shards) != 1 {
			t.Fatal("registered diagnostic source was not a global shard", created, err)
		}
		shard := created.Shards[0]
		if shard.Source != diagnosticInventorySource || shard.Authoritative {
			t.Fatal("diagnostic scope enumeration acquired blanket absence authority", shard)
		}
		scope, err := repository.GetScope(ctx, shard.ScopeID)
		if err != nil || scope.Kind != asset.ScopeGlobal {
			t.Fatal("diagnostic source became regional", scope, err)
		}
		shard.Authoritative = true // A stale persisted flag must not widen source authority.
		if err := repository.PutScanShard(ctx, shard); err != nil {
			t.Fatal(err)
		}
		for _, job := range created.Jobs {
			if err := handler.Handle(ctx, job); !failure && err != nil {
				t.Fatal("registered diagnostic worker failed", err)
			}
		}
		stored, err := repository.GetScanShard(ctx, shard.ID)
		if err != nil || !failure && (stored.Status != asset.ShardSucceeded || stored.Authoritative || stored.Coverage.Authoritative) || failure && stored.Status == asset.ShardSucceeded {
			t.Fatal("diagnostic worker changed collection authority", stored, err)
		}
		if !failure {
			jobs, err := repository.ListJobsByAggregate(ctx, "scan_task", string(created.ScanRun.ID))
			if err != nil {
				t.Fatal(err)
			}
			count := 0
			for _, job := range jobs {
				if job.Type == execution.JobGraph {
					count++
					if err := governance.NewGraphHandler(repository, registry, diagnosticContributors{r}).Handle(ctx, job); err != nil {
						t.Fatal("diagnostic graph reconciliation worker failed", err)
					}
				}
			}
			if count != 1 {
				t.Fatal("diagnostic scan did not schedule graph reconciliation", count)
			}
		}
		values, err := repository.ListActiveAssetsByConnection(ctx, connection.ID, "")
		if err != nil {
			t.Fatal(err)
		}
		return values
	}
	values := scan(false)
	if len(values) != 3 {
		t.Fatal("registered inventory omitted a native diagnostic scope", len(values))
	}
	first := values[0]
	for _, value := range values {
		if strings.Contains(value.Identity.NativeID, "/resourcegroups/") {
			first = value
			break
		}
	}
	before := first.Normalized[diagnosticConfigurationProof]
	object(f.settings[first.Identity.NativeID]["properties"])["ordinaryFutureField"] = "PRIVATE_DIAGNOSTIC_WORKER_CONTENT"
	clear(f.sources) // Known IDs must recover settings after their source is gone.
	clear(f.groups)
	values = scan(false)
	if len(values) != 3 {
		t.Fatal("worker lost diagnostic settings whose source disappeared")
	}
	for _, value := range values {
		if value.ID == first.ID {
			first = value
		}
	}
	if first.Normalized[diagnosticConfigurationProof] == before || first.Location != "global" || !first.Capabilities.Has(asset.CapabilityActionable) {
		t.Fatal("known orphan did not retain its independent native action")
	}
	wire, _ := json.Marshal(values)
	if strings.Contains(string(wire), "PRIVATE_DIAGNOSTIC_WORKER_CONTENT") || strings.Contains(string(wire), "ordinaryFutureField") {
		t.Fatal("private diagnostic configuration entered persisted inventory")
	}
	lifecycle, err := r.ServiceLifecycle(ctx, connection.ID)
	if err != nil {
		t.Fatal(err)
	}
	built, err := governance.NewService(repository, repository).RebuildGraph(ctx, root.ID, connection.ID, "diagnostic-worker", r.bundle, []governance.Contributor{lifecycle})
	if err != nil || len(built.Bindings) != 0 {
		t.Fatal("diagnostic settings acquired ownership or lost native graph validation", built, err)
	}
	planner := cleanup.NewService(repository, registry)
	task, err := planner.CreateTask(ctx, cleanup.CreateTaskRequest{ConnectionID: connection.ID, Selectors: []plan.CleanupSelector{{Kind: plan.SelectorAsset, AssetID: first.ID}}, CreatedBy: "operator"})
	if err != nil || task.Task.Status != plan.StatusReady || len(task.Steps) != 1 || task.Steps[0].AssetID != first.ID || len(task.ImpactItems) != 0 {
		t.Fatal("native diagnostic setting could not be planned independently", task, err)
	}
	if _, err := planner.CreateExecution(ctx, cleanup.CreateExecutionRequest{ConnectionID: connection.ID, CleanupTaskID: task.Task.ID, RequestedBy: "operator", IdempotencyKey: "diagnostic-worker-delete", Confirmation: cleanup.ExecutionConfirmation{Acknowledged: true}}); err != nil {
		t.Fatal("native diagnostic execution creation failed", err)
	}
	jobs, err := repository.ListJobsByAggregate(ctx, "cleanup_task", string(task.Task.ID))
	if err != nil || len(jobs) != 1 {
		t.Fatal("diagnostic cleanup did not create exactly one native action job", jobs, err)
	}
	resolver := cleanup.ActionResolverFunc(func(ctx context.Context, value asset.Asset) (cleanup.ActionDriver, error) {
		return registry.ResolveAction(ctx, value.Identity.ConnectionID, value)
	})
	f.hold = true
	worker := cleanup.NewExecutionHandler(planner, resolver)
	var retry *cleanup.RetryError
	if err := worker.Handle(ctx, jobs[0]); !errors.As(err, &retry) || !slices.Equal(f.deleted, []string{first.Identity.NativeID}) {
		t.Fatal("diagnostic worker did not persist native deletion waiting", err, f.deleted)
	}
	stored, err := repository.GetAsset(ctx, first.ID)
	if err != nil || stored.ClosedAt != nil {
		t.Fatal("source/group absence closed a still-live diagnostic setting", stored, err)
	}
	delete(f.settings, first.Identity.NativeID)
	// Recreate the application service and driver after persistence, so only
	// stored request/receipt evidence can authorize final native GET absence.
	planner = cleanup.NewService(repository, registry)
	worker = cleanup.NewExecutionHandler(planner, resolver)
	jobWire, _ := json.Marshal(jobs[0])
	var restored execution.Job
	if err := json.Unmarshal(jobWire, &restored); err != nil {
		t.Fatal(err)
	}
	if err := worker.Handle(ctx, restored); !errors.As(err, &retry) {
		t.Fatal("recovered diagnostic waiter did not persist final readback", err)
	}
	worker = cleanup.NewExecutionHandler(cleanup.NewService(repository, registry), resolver)
	if err := worker.Handle(ctx, restored); err != nil {
		t.Fatal("recovered final diagnostic readback failed", err)
	}
	stored, err = repository.GetAsset(ctx, first.ID)
	if err != nil || stored.ClosedAt == nil || len(f.deleted) != 1 {
		t.Fatal("recovered native receipt did not close exactly one setting", stored, err, f.deleted)
	}
	if len(scan(false)) != 2 {
		t.Fatal("subsequent scan lost surviving settings")
	}
	denied := slices.Sorted(maps.Keys(f.settings))[0]
	previousOverride := f.override
	f.override = func(req *http.Request) (*http.Response, bool) {
		if req.Method == "GET" && strings.EqualFold(req.URL.Path, denied) {
			return jsonResponse(403, map[string]any{"error": map[string]any{"code": "AuthorizationFailed"}}, nil), true
		}
		if previousOverride != nil {
			return previousOverride(req)
		}
		return nil, false
	}
	if len(scan(true)) != 2 {
		t.Fatal("unreadable diagnostic discovery closed saved resources")
	}
}
