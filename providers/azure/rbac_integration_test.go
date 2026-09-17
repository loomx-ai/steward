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

func TestRBACRegisteredScanGraphPlanAndWorkerRecovery(t *testing.T) {
	for _, principal := range []bool{false, true} {
		t.Run(map[bool]string{false: "role-and-scopes", true: "managed-identity-principal"}[principal], func(t *testing.T) {
			testRBACRegisteredScanGraphPlanAndWorkerRecovery(t, principal)
		})
	}
}

func testRBACRegisteredScanGraphPlanAndWorkerRecovery(t *testing.T, principal bool) {
	ctx := t.Context()
	f := newRBACFixture(t)
	identityID := ""
	if principal {
		var identity asset.Asset
		f, _, identity, _ = rbacPrincipalTarget(t, rbacUserIdentityType)
		identityID = identity.Identity.NativeID
		base := f.override
		f.override = func(req *http.Request) (*http.Response, bool) {
			if req.Method == "DELETE" && strings.EqualFold(req.URL.Path, identityID) {
				if req.URL.Query().Get("api-version") != "2023-01-31" || !f.hold {
					t.Fatal("identity worker lost the native pending-deletion protocol")
				}
				f.deleted = append(f.deleted, identityID)
				return jsonResponse(204, nil, nil), true
			}
			return base(req)
		}
	}
	r := f.runtime
	repository, err := sqlite.Open(filepath.Join(t.TempDir(), "rbac.db"), "../../migrations")
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
		regions := []string{"global"}
		kinds := []asset.ResourceKindID{r.resourceKind(rbacRoleType).ID, r.resourceKind(rbacAssignmentType).ID}
		shards := 2
		if principal {
			regions = append(regions, "westus")
			kinds = append(kinds, r.resourceKind(rbacUserIdentityType).ID)
			shards = 4
		}
		created, err := creator.Create(ctx, inventory.ScanCreationRequest{ConnectionID: connection.ID, RequestedBy: "rbac-native-worker", RegionMode: inventory.RegionModeSelected, RegionIDs: regions, ResourceKindIDs: kinds})
		if err != nil || len(created.Shards) != shards {
			t.Fatal("RBAC/identity native sources were not registered", len(created.Shards), err)
		}
		for _, shard := range created.Shards {
			scope, err := repository.GetScope(ctx, shard.ScopeID)
			regionalIdentity := principal && shard.ResourceKindID == r.resourceKind(rbacUserIdentityType).ID && scope.Kind == asset.ScopeRegion && scope.NativeID == "westus"
			if err != nil || scope.Kind != asset.ScopeGlobal && !regionalIdentity || shard.Source != productInventorySource || !shard.Authoritative {
				t.Fatal("RBAC source lost subscription-native coverage", shard, scope, err)
			}
		}
		for _, job := range created.Jobs {
			if err := handler.Handle(ctx, job); !failure && err != nil {
				t.Fatal("registered RBAC scan worker failed", err)
			}
		}
		if !failure {
			jobs, err := repository.ListJobsByAggregate(ctx, "scan_task", string(created.ScanRun.ID))
			if err != nil {
				t.Fatal(err)
			}
			graphs := 0
			for _, job := range jobs {
				if job.Type == execution.JobGraph {
					graphs++
					if err := governance.NewGraphHandler(repository, registry, diagnosticContributors{r}).Handle(ctx, job); err != nil {
						t.Fatal("RBAC graph reconciliation worker failed", err)
					}
				}
			}
			if graphs != 1 {
				t.Fatal("RBAC scan did not enqueue one completed graph", graphs)
			}
		}
		values, err := repository.ListActiveAssetsByConnection(ctx, connection.ID, "")
		if err != nil {
			t.Fatal(err)
		}
		return values
	}
	values := scan(false)
	if len(values) != 4 {
		t.Fatal("registered RBAC inventory omitted a native resource", len(values))
	}
	object(f.resources[rbacTestRoleID()]["properties"])["futurePrivateCondition"] = "PRIVATE_RBAC_DATABASE_CONTENT"
	values = scan(false)
	wire, _ := json.Marshal(values)
	if strings.Contains(string(wire), "PRIVATE_RBAC_DATABASE_CONTENT") || strings.Contains(string(wire), "private-assignment-condition") {
		t.Fatal("private RBAC configuration entered persisted inventory")
	}
	planner := cleanup.NewService(repository, registry)
	var role asset.Asset
	selectors := []plan.CleanupSelector{}
	for _, value := range values {
		if value.Identity.NativeID == rbacTestRoleID() {
			role = value
		}
		if value.Normalized["cleanup_protected"] != true {
			selectors = append(selectors, plan.CleanupSelector{Kind: plan.SelectorAsset, AssetID: value.ID})
		}
	}
	blocked, err := planner.CreateTask(ctx, cleanup.CreateTaskRequest{ConnectionID: connection.ID, Selectors: []plan.CleanupSelector{{Kind: plan.SelectorAsset, AssetID: role.ID}}, CreatedBy: "operator"})
	if err != nil || blocked.Task.Status == plan.StatusReady || len(blocked.Task.Blockers) == 0 {
		t.Fatal("retained assignments did not block a persisted role-only plan", blocked, err)
	}
	if principal {
		identity := cdnAsset(t, values, rbacUserIdentityType)
		blocked, err := planner.CreateTask(ctx, cleanup.CreateTaskRequest{ConnectionID: connection.ID, Selectors: []plan.CleanupSelector{{Kind: plan.SelectorAsset, AssetID: identity.ID}}, CreatedBy: "operator"})
		if err != nil || blocked.Task.Status == plan.StatusReady || len(blocked.Task.Blockers) == 0 {
			t.Fatal("a role assignment outside the identity's group did not block its persisted plan", blocked, err)
		}
	}
	if principal {
		// Deleting an identity also requires a complete scan of the connection:
		// the kind-limited scans above cannot rule out other identity users.
		finished := time.Now().UTC()
		complete := asset.ScanRun{ID: "complete-all-regions", ConnectionID: connection.ID, Status: asset.ScanSucceeded, ScopeMode: asset.ScanAllActiveRegions, CreatedAt: finished, FinishedAt: &finished, Targets: []asset.ScanTarget{{Key: "region:westus", Kind: asset.ScanTargetRegion, RegionID: "westus"}, {Key: "global", Kind: asset.ScanTargetGlobal, RegionID: "global"}}}
		if err := repository.CreateScanRun(ctx, complete); err != nil {
			t.Fatal(err)
		}
		for _, target := range complete.Targets {
			if err := repository.PutScanShard(ctx, asset.ScanShard{ID: asset.ScanShardID("complete-" + target.RegionID), ScanRunID: complete.ID, Provider: asset.ProviderAzure, Source: productInventorySource, TargetKey: target.Key, RegionID: target.RegionID, ScopeID: root.ID, Status: asset.ShardSucceeded, Coverage: asset.Coverage{Complete: true, FreshAt: finished}, CreatedAt: finished, FinishedAt: &finished}); err != nil {
				t.Fatal(err)
			}
		}
	}
	task, err := planner.CreateTask(ctx, cleanup.CreateTaskRequest{ConnectionID: connection.ID, Selectors: selectors, CreatedBy: "operator"})
	if err != nil || task.Task.Status != plan.StatusReady || len(task.Steps) != 3 || len(task.ImpactItems) != 0 {
		t.Fatal("RBAC did not produce three independent native deletion steps", task, err)
	}
	if _, err := planner.CreateExecution(ctx, cleanup.CreateExecutionRequest{ConnectionID: connection.ID, CleanupTaskID: task.Task.ID, RequestedBy: "operator", IdempotencyKey: "rbac-worker-delete", Confirmation: cleanup.ExecutionConfirmation{Acknowledged: true}}); err != nil {
		t.Fatal("RBAC execution creation failed", err)
	}
	resolver := cleanup.ActionResolverFunc(func(ctx context.Context, value asset.Asset) (cleanup.ActionDriver, error) {
		return registry.ResolveAction(ctx, value.Identity.ConnectionID, value)
	})
	f.hold = true
	completed := map[plan.StepID]bool{}
	for range len(task.Steps) {
		var step plan.CleanupTaskStep
		for _, candidate := range task.Steps {
			ready := !completed[candidate.ID]
			for _, requirement := range candidate.DependsOn {
				ready = ready && completed[requirement]
			}
			if ready {
				step = candidate
				break
			}
		}
		if step.ID == "" {
			t.Fatal("persisted RBAC graph has no ready step")
		}
		jobs, err := repository.ListJobsByAggregate(ctx, "cleanup_task", string(task.Task.ID))
		if err != nil {
			t.Fatal(err)
		}
		var job execution.Job
		for _, candidate := range jobs {
			if text(candidate.Payload["cleanup_task_step_id"]) == string(step.ID) {
				job = candidate
				break
			}
		}
		if job.ID == "" {
			t.Fatal("RBAC ready step did not acquire its durable job", step.ID)
		}
		value, err := repository.GetAsset(ctx, step.AssetID)
		if err != nil {
			t.Fatal(err)
		}
		worker := cleanup.NewExecutionHandler(cleanup.NewService(repository, registry), resolver)
		var retry *cleanup.RetryError
		before := len(f.deleted)
		if err := worker.Handle(ctx, job); !errors.As(err, &retry) || len(f.deleted) != before+1 || f.deleted[before] != value.Identity.NativeID {
			t.Fatal("RBAC worker did not persist pending native deletion", err, f.deleted)
		}
		stored, err := repository.GetAsset(ctx, value.ID)
		if err != nil || stored.ClosedAt != nil {
			t.Fatal("RBAC DELETE acknowledgement closed a live resource", stored, err)
		}
		delete(f.resources, value.Identity.NativeID)
		if value.Identity.NativeID == identityID {
			delete(f.scopes, identityID)
		}
		wire, _ := json.Marshal(job)
		if err := json.Unmarshal(wire, &job); err != nil {
			t.Fatal(err)
		}
		f.runtime.clients = map[asset.ConnectionID]*client{}
		worker = cleanup.NewExecutionHandler(cleanup.NewService(repository, registry), resolver)
		if err := worker.Handle(ctx, job); !errors.As(err, &retry) {
			t.Fatal("recovered RBAC waiter did not persist readback", err)
		}
		worker = cleanup.NewExecutionHandler(cleanup.NewService(repository, registry), resolver)
		if err := worker.Handle(ctx, job); err != nil {
			t.Fatal("recovered RBAC final readback failed", err)
		}
		stored, err = repository.GetAsset(ctx, value.ID)
		if err != nil || stored.ClosedAt == nil || len(f.deleted) != before+1 {
			t.Fatal("RBAC recovery repeated deletion or lost closure", stored, err, f.deleted)
		}
		completed[step.ID] = true
	}
	expectedScopes := 2
	validOrder := f.deleted[2] == rbacTestRoleID()
	if principal {
		expectedScopes = 3
		validOrder = f.deleted[0] == rbacTestAssignmentID() && slices.Contains(f.deleted, identityID) && slices.Contains(f.deleted, rbacTestRoleID())
	}
	if !validOrder || len(f.scopes) != expectedScopes || len(scan(false)) != 1 {
		t.Fatal("RBAC cleanup lost ordering, removed independent scopes or lost the built-in role", f.deleted)
	}
	base := f.override
	f.override = func(req *http.Request) (*http.Response, bool) {
		if strings.HasSuffix(strings.ToLower(req.URL.Path), "/roledefinitions") {
			return jsonResponse(403, map[string]any{"error": map[string]any{"code": "AuthorizationFailed"}}, nil), true
		}
		if base != nil {
			return base(req)
		}
		return nil, false
	}
	if len(scan(true)) != 1 {
		t.Fatal("unreadable native RBAC source closed the retained built-in role")
	}
}
