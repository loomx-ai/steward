package gcp

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
	"github.com/loomx-ai/steward/internal/core/graph"
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/internal/persistence"
	"github.com/loomx-ai/steward/internal/persistence/sqlite"
	providerruntime "github.com/loomx-ai/steward/internal/provider/runtime"
)

func TestRoutePolicySQLiteScanCleanupRestartAndReconciliation(t *testing.T) {
	for _, bgp := range []bool{false, true} {
		t.Run(map[bool]string{false: "unattached", true: "bgp-attached"}[bgp], func(t *testing.T) { routePolicySQLiteCleanup(t, bgp, false) })
	}
}

func TestRoutePolicyNamedSetDependencyGraphAndRetainedSet(t *testing.T) {
	routePolicySQLiteCleanup(t, true, true)
}

func routePolicySQLiteCleanup(t *testing.T, bgp, setReferences bool) {
	routerComponentSQLiteCleanup(t, routePolicyType, bgp, setReferences, false)
}
func TestNamedSetSQLiteScanCleanupRestartAndReconciliation(t *testing.T) {
	routerComponentSQLiteCleanup(t, namedSetType, false, false, false)
}
func TestNamedSetSQLitePolicyBeforeSetDeletion(t *testing.T) {
	routerComponentSQLiteCleanup(t, routePolicyType, true, true, true)
}

type routerCleanupCheckpoint struct {
	Repositories    persistence.Repositories
	Planner         *cleanup.Service
	Runtime         *Runtime
	Fixture         *routePolicyActionFixture
	Task, Competing persistence.CleanupTaskAggregate
	Attempt         execution.ExecutionAttempt
	Job             execution.Job
}

func routerComponentSQLiteCleanup(t *testing.T, nativeType string, bgp, setReferences, deleteSet bool, checkpoints ...func(routerCleanupCheckpoint)) {
	ctx := t.Context()
	r, request, fixture := routerComponentActionRuntime(t, nativeType)
	if bgp {
		routePolicyAttachFixture(t, &request, fixture)
	}
	kinds := []asset.ResourceKindID{r.resourceKind(routerType).ID, r.resourceKind(nativeType).ID}
	setExists, setDeletes, setStatus := true, 0, "RUNNING"
	var setOperation map[string]any
	if setReferences {
		object(array(fixture.policy["terms"])[0])["match"] = map[string]any{"expression": "destination.inAnyRange(prefixSets('local'))"}
		kinds = append(kinds, r.resourceKind(namedSetType).ID)
		original := r.transport
		r = protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
			if req.Method == "GET" && strings.HasSuffix(req.URL.Path, "/listNamedSets") {
				if !setExists {
					return apiResponse(req, 200, `{}`), nil
				}
				return apiResponse(req, 200, `{"result":[{"name":"local"}]}`), nil
			}
			if req.Method == "GET" && strings.HasSuffix(req.URL.Path, "/getNamedSet") {
				if req.URL.Query().Get("namedSet") != "local" {
					t.Fatal("foreign set request", req.URL)
				}
				if !setExists {
					return apiResponse(req, 404, `{}`), nil
				}
				return dataformResponse(req, 200, map[string]any{"resource": namedSetFixture("local")}), nil
			}
			if req.Method == "POST" && strings.HasSuffix(req.URL.Path, "/deleteNamedSet") {
				if !deleteSet || fixture.exists || fixture.deletes != 1 || req.URL.Query().Get("namedSet") != "local" || req.URL.Query().Get("requestId") == "" {
					t.Fatal("set deleted before policy or wrong request", req.URL)
				}
				setDeletes++
				setOperation = map[string]any{"name": "set-operation", "status": "PENDING", "operationType": "deleteNamedSet", "targetLink": fixture.parent["selfLink"], "targetId": "1001", "clientOperationId": req.URL.Query().Get("requestId")}
				return dataformResponse(req, 200, setOperation), nil
			}
			if req.Method == "GET" && strings.HasSuffix(req.URL.Path, "/operations/set-operation") {
				result := cloneParameters(setOperation)
				result["status"] = setStatus
				return dataformResponse(req, 200, result), nil
			}
			return original.RoundTrip(req)
		})
	}
	transport := r.transport
	registry := identityRegistry(t, r)
	db := filepath.Join(t.TempDir(), "route-policy-cleanup.db")
	repositories, err := sqlite.Open(db, "../../migrations")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := repositories.Connections().PutConnection(ctx, asset.CloudConnection{ID: "connection", Provider: asset.ProviderGCP, Partition: "gcp", Status: asset.ConnectionActive, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Inventory().PutScope(ctx, asset.Scope{ID: "project", ConnectionID: "connection", Kind: asset.ScopeProject, NativeID: "sample-project", Name: "Project", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Regions().PutRegion(ctx, asset.ConnectionRegion{ID: "region", ConnectionID: "connection", RegionID: "us-central1", DiscoveredName: "US Central", Origin: asset.RegionOriginAPI, Lifecycle: asset.RegionActive, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	scan := func() { now = scanRouterComponentAssets(t, repositories, registry, now, kinds) }
	scan()
	page, err := repositories.Inventory().ListAssets(ctx, persistence.ListOptions{Limit: 10})
	if err != nil || len(page.Items) != len(kinds) {
		t.Fatal(page, err)
	}
	var policy, parent, set asset.Asset
	for _, value := range page.Items {
		if value.Identity.NativeType == nativeType {
			policy = value
		} else if value.Identity.NativeType == namedSetType {
			set = value
		} else {
			parent = value
		}
	}
	if policy.ID == "" || !policy.Capabilities.Has(asset.CapabilityActionable) || policy.Normalized[map[string]string{routePolicyType: routePolicyRouterID, namedSetType: namedSetRouterID, cloudNatType: cloudNatRouterID}[nativeType]] != "1001" {
		t.Fatal("scan did not capture deletable policy review", policy)
	}
	if bgp {
		encoded, _ := json.Marshal(policy.Normalized)
		if strings.Contains(string(encoded), "router-only-secret") || len(array(policy.Normalized["bgpReferences"])) != 2 {
			t.Fatal("missing references or leaked key", policy.Normalized["bgpReferences"])
		}
	}
	if setReferences {
		relationships, err := repositories.Graph().ListRelationshipsForAsset(ctx, "connection", policy.ID)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, relation := range relationships {
			if relation.SourceAssetID == policy.ID && relation.TargetAssetID == set.ID && relation.Type == graph.RelationshipDependsOn {
				found = true
			}
		}
		if !found {
			t.Fatal("missing persisted native set dependency", relationships)
		}
	}
	planner := cleanup.NewService(repositories, registry, cleanup.WithClock(func() time.Time { return now }))
	selectors := []plan.CleanupSelector{{Kind: plan.SelectorAsset, AssetID: policy.ID}}
	expectedSteps := 1
	if deleteSet {
		setOnly, err := planner.CreateTask(ctx, cleanup.CreateTaskRequest{ConnectionID: "connection", Selectors: []plan.CleanupSelector{{Kind: plan.SelectorAsset, AssetID: set.ID}}, CreatedBy: "test"})
		foundBlocker := false
		for _, blocker := range setOnly.Task.Blockers {
			if blocker.Code == plan.BlockCrossScopeDependency && blocker.AssetID == set.ID {
				foundBlocker = true
			}
		}
		if err != nil || !foundBlocker {
			t.Fatal("referenced set-only plan was not blocked", setOnly, err)
		}
		selectors = append(selectors, plan.CleanupSelector{Kind: plan.SelectorAsset, AssetID: set.ID})
		expectedSteps = 2
	}
	task, err := planner.CreateTask(ctx, cleanup.CreateTaskRequest{ConnectionID: "connection", Selectors: selectors, CreatedBy: "test"})
	if err != nil || len(task.Steps) != expectedSteps || task.Steps[0].AssetID != policy.ID {
		t.Fatal("independent policy plan", task, err)
	}
	if deleteSet && (task.Steps[1].AssetID != set.ID || !slices.Contains(task.Steps[1].DependsOn, task.Steps[0].ID)) {
		t.Fatal("policy-before-set dependency absent", task.Steps)
	}
	var competing persistence.CleanupTaskAggregate
	if nativeType == cloudNatType {
		competing, err = planner.CreateTask(ctx, cleanup.CreateTaskRequest{ConnectionID: "connection", Selectors: selectors, CreatedBy: "test"})
		if err != nil {
			t.Fatal(err)
		}
	}
	attempt, err := planner.CreateExecution(ctx, cleanup.CreateExecutionRequest{CleanupTaskID: task.Task.ID, ConnectionID: "connection", RequestedBy: "test", IdempotencyKey: "policy-sqlite", Confirmation: cleanup.ExecutionConfirmation{Acknowledged: true}})
	if err != nil {
		t.Fatal(err)
	}
	if nativeType == cloudNatType {
		_, err := planner.CreateExecution(ctx, cleanup.CreateExecutionRequest{CleanupTaskID: competing.Task.ID, ConnectionID: "connection", RequestedBy: "test", IdempotencyKey: "competing-nat", Confirmation: cleanup.ExecutionConfirmation{Acknowledged: true}})
		if !errors.Is(err, persistence.ErrConflict) {
			t.Fatal("overlapping native NAT execution accepted", err)
		}
	}
	job, err := repositories.Jobs().ClaimNext(ctx, "policy-worker", now, time.Minute, execution.JobExecute)
	if err != nil {
		t.Fatal(err)
	}
	var setJob execution.Job
	if deleteSet {
		second, err := repositories.Jobs().ClaimNext(ctx, "policy-worker", now, time.Minute, execution.JobExecute)
		if err != nil {
			t.Fatal(err)
		}
		if job.TargetKey == string(set.ID) {
			setJob = job
			job = second
		} else {
			setJob = second
		}
		if job.TargetKey != string(policy.ID) || setJob.TargetKey != string(set.ID) {
			t.Fatal("wrong jobs", job, setJob)
		}
		handler := cleanup.NewExecutionHandler(planner, cleanup.ActionResolverFunc(func(ctx context.Context, value asset.Asset) (cleanup.ActionDriver, error) {
			return r.ResolveAction(ctx, value.Identity.ConnectionID, value)
		}))
		var retry *cleanup.RetryError
		if err := handler.Handle(ctx, setJob); !errors.As(err, &retry) || setDeletes != 0 || fixture.deletes != 0 {
			t.Fatal("set ran before policy", err)
		}
	}
	states := []execution.ActionStatus{execution.ActionWaiting, execution.ActionWaiting, execution.ActionReadingBack, execution.ActionSucceeded}
	if bgp {
		states = []execution.ActionStatus{execution.ActionWaiting, execution.ActionWaiting, execution.ActionWaiting, execution.ActionWaiting, execution.ActionWaiting, execution.ActionReadingBack, execution.ActionSucceeded}
	}
	for round, want := range states {
		repositories, err = sqlite.Open(db, "../../migrations")
		if err != nil {
			t.Fatal(err)
		}
		fresh := protocolRuntime(t, transport.RoundTrip)
		planner = cleanup.NewService(repositories, identityRegistry(t, fresh), cleanup.WithClock(func() time.Time { return now }))
		handler := cleanup.NewExecutionHandler(planner, cleanup.ActionResolverFunc(func(ctx context.Context, value asset.Asset) (cleanup.ActionDriver, error) {
			return fresh.ResolveAction(ctx, value.Identity.ConnectionID, value)
		}))
		if bgp {
			if round == 1 {
				fixture.patchStatus = "DONE"
			}
			if round == 2 {
				applyRoutePolicyPatch(t, fixture)
			}
			if round == 4 {
				fixture.status = "DONE"
			}
			if round == 5 {
				fixture.exists = false
			}
		} else {
			if round == 1 {
				fixture.status = "DONE"
			}
			if round == 2 {
				fixture.exists = false
			}
		}
		err = handler.Handle(ctx, job)
		var retry *cleanup.RetryError
		if want == execution.ActionSucceeded && err != nil || want != execution.ActionSucceeded && !errors.As(err, &retry) {
			t.Fatal("restarted worker", round, err)
		}
		phase := map[string]string{routePolicyType: "route_policy_delete", namedSetType: "named_set_delete", cloudNatType: "cloud_nat_delete"}[nativeType]
		deletes := 1
		if bgp && round < 2 {
			phase = routePolicyDetach
			deletes = 0
		}
		actions, err := repositories.Executions().ListActions(ctx, attempt.ID)
		var current execution.ActionAttempt
		for _, action := range actions {
			if action.AssetID == policy.ID {
				current = action
			}
		}
		if err != nil || current.Status != want || current.ProviderResult["phase"] != phase {
			t.Fatal("persisted native phase", round, actions, err)
		}
		if fixture.deletes != deletes || bgp && fixture.patches != 1 {
			t.Fatal("restart repeated mutation", round, fixture.deletes, fixture.patches)
		}
		if round == 0 && len(checkpoints) > 0 {
			checkpoints[0](routerCleanupCheckpoint{repositories, planner, fresh, fixture, task, competing, attempt, job})
			return
		}
		now = now.Add(3 * time.Second)
	}
	if deleteSet {
		for round, want := range []execution.ActionStatus{execution.ActionWaiting, execution.ActionWaiting, execution.ActionReadingBack, execution.ActionSucceeded} {
			repositories, err = sqlite.Open(db, "../../migrations")
			if err != nil {
				t.Fatal(err)
			}
			fresh := protocolRuntime(t, transport.RoundTrip)
			planner = cleanup.NewService(repositories, identityRegistry(t, fresh), cleanup.WithClock(func() time.Time { return now }))
			handler := cleanup.NewExecutionHandler(planner, cleanup.ActionResolverFunc(func(ctx context.Context, value asset.Asset) (cleanup.ActionDriver, error) {
				return fresh.ResolveAction(ctx, value.Identity.ConnectionID, value)
			}))
			if round == 1 {
				setStatus = "DONE"
			}
			if round == 2 {
				setExists = false
			}
			err = handler.Handle(ctx, setJob)
			var retry *cleanup.RetryError
			if want == execution.ActionSucceeded && err != nil || want != execution.ActionSucceeded && !errors.As(err, &retry) {
				t.Fatal("set worker restart", round, err)
			}
			actions, err := repositories.Executions().ListActions(ctx, attempt.ID)
			if err != nil {
				t.Fatal(err)
			}
			var current execution.ActionAttempt
			for _, action := range actions {
				if action.AssetID == set.ID {
					current = action
				}
			}
			if current.Status != want || current.ProviderResult["phase"] != "named_set_delete" || setDeletes != 1 || fixture.deletes != 1 || fixture.patches != 1 {
				t.Fatal("set checkpoint or mutation count", round, current, setDeletes)
			}
			now = now.Add(3 * time.Second)
		}
		deletedSet, err := repositories.Inventory().GetAsset(ctx, set.ID)
		if err != nil || deletedSet.DeletedAt == nil {
			t.Fatal("missing set tombstone", deletedSet, err)
		}
		if err := repositories.Jobs().Complete(ctx, setJob.ID, "policy-worker", execution.JobSucceeded, "", now); err != nil {
			t.Fatal(err)
		}
	}
	finished, err := repositories.Executions().GetExecution(ctx, attempt.ID)
	if err != nil || finished.Status != execution.ExecutionSucceeded {
		t.Fatal(finished, err)
	}
	deleted, err := repositories.Inventory().GetAsset(ctx, policy.ID)
	if err != nil || deleted.DeletedAt == nil {
		t.Fatal("missing tombstone", deleted, err)
	}
	retained, err := repositories.Inventory().GetAsset(ctx, parent.ID)
	if err != nil || retained.DeletedAt != nil || retained.ClosedAt != nil {
		t.Fatal("router was deleted", retained, err)
	}
	if err := repositories.Jobs().Complete(ctx, job.ID, "policy-worker", execution.JobSucceeded, "", now); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Minute)
	scan()
	page, err = repositories.Inventory().ListAssets(ctx, persistence.ListOptions{Limit: 10})
	if err != nil || len(page.Items) != len(kinds)-expectedSteps || fixture.deletes != 1 {
		t.Fatal("reconciliation changed parent or restored deleted policy", page, err)
	}
	if setReferences && !deleteSet {
		retainedSet, err := repositories.Inventory().GetAsset(ctx, set.ID)
		if err != nil || retainedSet.ClosedAt != nil || retainedSet.DeletedAt != nil {
			t.Fatal("referenced set was deleted", retainedSet, err)
		}
	}
}

func scanRouterComponentAssets(t *testing.T, repositories persistence.Repositories, registry *providerruntime.Registry, now time.Time, kinds []asset.ResourceKindID, contributors ...governance.ContributorResolver) time.Time {
	t.Helper()
	ctx := t.Context()
	creator, err := inventory.NewCreator(repositories, registry, inventory.WithCreatorClock(func() time.Time { return now }))
	if err != nil {
		t.Fatal(err)
	}
	created, err := creator.Create(ctx, inventory.ScanCreationRequest{ConnectionID: "connection", RequestedBy: "test", RegionMode: inventory.RegionModeSelected, RegionIDs: []string{"us-central1"}, ResourceKindIDs: kinds})
	if err != nil || len(created.Shards) != len(kinds) || len(created.Jobs) != 1 {
		t.Fatal("scan creation", created, err)
	}
	job, err := repositories.Jobs().ClaimNext(ctx, "policy-scan", now, time.Minute, execution.JobScan)
	if err != nil {
		t.Fatal(err)
	}
	if err := inventory.NewScanHandler(repositories, registry, inventory.NewService(repositories.Inventory())).Handle(ctx, job); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Jobs().Complete(ctx, job.ID, "policy-scan", execution.JobSucceeded, "", now); err != nil {
		t.Fatal(err)
	}
	if wall := time.Now().UTC(); wall.After(now) {
		now = wall
	}
	now = now.Add(time.Second)
	job, err = repositories.Jobs().ClaimNext(ctx, "policy-graph", now, time.Minute, execution.JobGraph)
	if err != nil {
		t.Fatal(err)
	}
	var resolver governance.ContributorResolver
	if len(contributors) > 0 {
		resolver = contributors[0]
	}
	if err := governance.NewGraphHandler(repositories, registry, resolver).Handle(ctx, job); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Jobs().Complete(ctx, job.ID, "policy-graph", execution.JobSucceeded, "", now); err != nil {
		t.Fatal(err)
	}
	return now
}
