package cleanup_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/loomx-ai/steward/internal/app/cleanup"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/core/graph"
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/internal/persistence"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func requiredDeletionExecutionFixture(t *testing.T, authorizer cleanup.ExecutionAuthorizer, managed ...bool) (persistence.Repositories, *cleanup.Service) {
	t.Helper()
	repositories := openPlanningRepositories(t)
	now := time.Date(2026, 7, 13, 11, 0, 0, 0, time.UTC)
	var assets []asset.Asset
	for _, id := range []asset.AssetID{"first", "second", "configuration", "retained-owner", "view"} {
		assets = append(assets, planningAsset(id, "c-1", "ACS::ECS::Instance", string(id)+"-native", now))
	}
	owner := planningBinding("owner-binding", "retained-owner", "configuration", graph.OwnershipExclusive, graph.CleanupDirect, "required-graph", now)
	owner.DirectCleanupAllowed = true
	view := planningBinding("view-binding", "configuration", "view", graph.OwnershipExclusive, graph.CleanupDelegate, "required-graph", now)
	view.Evidence[graph.LifecycleEvidenceControllerVerifiesManagedAbsence] = true
	var relationships []graph.Relationship
	for _, source := range []asset.AssetID{"first", "second"} {
		relationships = append(relationships, graph.Relationship{ID: graph.RelationshipID(source + "-requires-configuration"), SourceAssetID: source, TargetAssetID: "configuration", Type: graph.RelationshipDependsOn, Source: "provider:native-lifecycle", Confidence: 1, GraphRevision: "required-graph", ObservedAt: now, Evidence: map[string]any{
			graph.RelationshipEvidenceRequiredDeletion: true, graph.RelationshipEvidenceAuthority: graph.AuthorityAuthoritative, graph.RelationshipEvidenceDeletionOrder: graph.DeletionOrderTargetBeforeSource,
		}})
	}
	selectors := []plan.CleanupSelector{assetSelector("first"), assetSelector("second")}
	if len(managed) != 0 && managed[0] {
		selectors = append(selectors, assetSelector("configuration"))
		for i := range relationships {
			relationships[i].TargetAssetID = "view"
			relationships[i].Evidence[graph.RelationshipEvidenceAutomaticSelection] = false
			relationships[i].Evidence[graph.RelationshipEvidenceDeletionCascadeControllers] = map[string]any{"configuration": true}
		}
	}
	bindings := []graph.LifecycleBinding{owner, view}
	wantImpacts := 1
	if len(managed) > 1 && managed[1] {
		node := planningAsset("node", "c-1", "ACS::ECS::Instance", "node-native", now)
		assets = append(assets, node)
		bindings[1].ControllerAssetID = node.ID
		parent := planningBinding("node-binding", "configuration", node.ID, graph.OwnershipExclusive, graph.CleanupDelegate, "required-graph", now)
		parent.Evidence[graph.LifecycleEvidenceControllerVerifiesManagedAbsence] = true
		bindings = append(bindings, parent)
		wantImpacts = 2
	}
	seedPlanningGraph(t, repositories, "scope-a", "required-graph", assets, relationships, bindings)
	planner := cleanup.NewService(repositories, bundleResolver{asset.ProviderAliCloud: {Provider: asset.ProviderAliCloud, Revision: "bundle-a", Hash: "spec-a"}}, cleanup.WithClock(func() time.Time { return now }), cleanup.WithTaskIDGenerator(func() string { return "cln-required" }), cleanup.WithExecutionIDGenerator(func() string { return "execution-required" }), cleanup.WithExecutionAuthorizer(authorizer))
	aggregate, err := planner.CreateTask(context.Background(), cleanup.CreateTaskRequest{Selectors: selectors, CreatedBy: "operator"})
	if err != nil || aggregate.Task.Status != plan.StatusReady || len(aggregate.Steps) != 3 || len(aggregate.ImpactItems) != wantImpacts {
		t.Fatalf("required cleanup planning failed %+v %v", aggregate, err)
	}
	if len(managed) == 0 && !slices.Equal(aggregate.Task.ResolvedAssetIDs, []asset.AssetID{"first", "second"}) || cleanupTaskStepForAsset(aggregate.Steps, "retained-owner").ID != "" {
		t.Fatal("prerequisite changed the user's selected roots or deleted its owner")
	}
	return repositories, planner
}

func TestSharedRequiredDeletionExecutesOnceAndRestoresFrozenPrerequisite(t *testing.T) {
	ctx := context.Background()
	var authorized []asset.AssetID
	repositories, planner := requiredDeletionExecutionFixture(t, cleanup.ExecutionAuthorizerFunc(func(_ context.Context, _ string, ids []asset.AssetID) error {
		authorized = slices.Clone(ids)
		return nil
	}))
	if _, err := planner.CreateExecution(ctx, cleanup.CreateExecutionRequest{CleanupTaskID: "cln-required", RequestedBy: "operator", IdempotencyKey: "required-key", Confirmation: cleanup.ExecutionConfirmation{Acknowledged: true}}); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(authorized, []asset.AssetID{"configuration", "first", "second", "view"}) {
		t.Fatalf("execution authorization omitted implicit deletes: %v", authorized)
	}
	configuration := &scriptedActionDriver{readback: contracts.ReadbackResult{Exists: false}}
	first := &scriptedActionDriver{pollInterval: time.Second, readback: contracts.ReadbackResult{Exists: false}}
	second := &scriptedActionDriver{readback: contracts.ReadbackResult{Exists: false}}
	resolver := cleanup.ActionResolverFunc(func(_ context.Context, value asset.Asset) (cleanup.ActionDriver, error) {
		switch value.ID {
		case "first":
			return first, nil
		case "second":
			return second, nil
		case "configuration":
			return configuration, nil
		default:
			return nil, fmt.Errorf("unexpected cleanup of %s", value.ID)
		}
	})
	handler := cleanup.NewExecutionHandler(planner, resolver)
	firstJob := cleanupExecutionJobForAsset(t, repositories, "cln-required", "first")
	var retry *cleanup.RetryError
	if err := handler.Handle(ctx, firstJob); err != nil && !errors.As(err, &retry) {
		t.Fatal(err)
	}
	if first.executeCalls != 0 {
		t.Fatal("source executed before required deletion")
	}
	if err := handler.Handle(ctx, cleanupExecutionJobForAsset(t, repositories, "cln-required", "configuration")); err != nil {
		t.Fatal(err)
	}
	closed, err := repositories.Inventory().GetAsset(ctx, "configuration")
	if err != nil || closed.ClosedAt == nil {
		t.Fatalf("prerequisite not closed %+v %v", closed, err)
	}
	closed.Normalized = map[string]any{"changed_after_plan": true}
	if err := repositories.Inventory().PutAsset(ctx, closed); err != nil {
		t.Fatal(err)
	}
	if err := handler.Handle(ctx, firstJob); !errors.As(err, &retry) {
		t.Fatalf("source did not persist wait: %v", err)
	}
	if err := handler.Handle(ctx, cleanupExecutionJobForAsset(t, repositories, "cln-required", "second")); err != nil {
		t.Fatal(err)
	}
	handler = cleanup.NewExecutionHandler(planner, resolver)
	if err := handler.Handle(ctx, firstJob); err != nil && !errors.As(err, &retry) {
		t.Fatal(err)
	}
	if configuration.executeCalls != 1 || first.executeCalls != 1 || second.executeCalls != 1 || len(first.waitRequests) != 1 {
		t.Fatal("shared prerequisite or source executed more than once")
	}
	requests := []contracts.ActionRequest{first.executeRequests[0], second.executeRequests[0], first.waitRequests[0]}
	for _, request := range requests {
		if len(request.PrerequisiteDeletions) != 1 {
			t.Fatalf("missing required deletion %+v", request)
		}
		prerequisite := request.PrerequisiteDeletions[0]
		if prerequisite.Asset.ID != "configuration" || prerequisite.ControllerID != request.Asset.ID || !prerequisite.Delete || prerequisite.Asset.ClosedAt != nil || prerequisite.Asset.Normalized["changed_after_plan"] != nil || prerequisite.Asset.Normalized["id"] != "configuration-native" {
			t.Fatalf("prerequisite snapshot was replaced %+v", prerequisite)
		}
	}
	retained, err := repositories.Inventory().GetAsset(ctx, "retained-owner")
	if err != nil || retained.ClosedAt != nil {
		t.Fatal("prerequisite deleted its unselected owner")
	}
}

func TestExecutionAuthorizerCanDenyRequiredResource(t *testing.T) {
	ctx := context.Background()
	denied := errors.New("required configuration permission denied")
	repositories, planner := requiredDeletionExecutionFixture(t, cleanup.ExecutionAuthorizerFunc(func(_ context.Context, _ string, ids []asset.AssetID) error {
		if slices.Contains(ids, asset.AssetID("configuration")) {
			return denied
		}
		return nil
	}))
	if _, err := planner.CreateExecution(ctx, cleanup.CreateExecutionRequest{CleanupTaskID: "cln-required", RequestedBy: "operator", IdempotencyKey: "denied-required", Confirmation: cleanup.ExecutionConfirmation{Acknowledged: true}}); !errors.Is(err, denied) {
		t.Fatalf("implicit resource authorization bypassed %v", err)
	}
	if _, err := repositories.Executions().GetExecutionByIdempotencyKey(ctx, "denied-required"); !errors.Is(err, persistence.ErrNotFound) {
		t.Fatal("unauthorized execution persisted")
	}
}

func TestContinuationReauthorizesRequiredResources(t *testing.T) {
	ctx := context.Background()
	deny := false
	denied := errors.New("required configuration permission revoked")
	var authorized []asset.AssetID
	repositories, planner := requiredDeletionExecutionFixture(t, cleanup.ExecutionAuthorizerFunc(func(_ context.Context, _ string, ids []asset.AssetID) error {
		authorized = slices.Clone(ids)
		if deny && slices.Contains(ids, asset.AssetID("configuration")) {
			return denied
		}
		return nil
	}))
	created, err := planner.CreateExecution(ctx, cleanup.CreateExecutionRequest{CleanupTaskID: "cln-required", RequestedBy: "operator", IdempotencyKey: "required-key", Confirmation: cleanup.ExecutionConfirmation{Acknowledged: true}})
	if err != nil {
		t.Fatal(err)
	}
	created.Status = execution.ExecutionFailed
	if err := repositories.Executions().UpdateExecution(ctx, created); err != nil {
		t.Fatal(err)
	}
	aggregate, err := repositories.CleanupTasks().GetTask(ctx, "cln-required")
	if err != nil {
		t.Fatal(err)
	}
	aggregate.Task.Status = plan.StatusFailed
	if err := repositories.CleanupTasks().UpdateTask(ctx, aggregate.Task); err != nil {
		t.Fatal(err)
	}
	deny, authorized = true, nil
	if _, err := planner.ContinueExecution(ctx, cleanup.ContinueExecutionRequest{CleanupTaskID: "cln-required", RequestedBy: "operator", IdempotencyKey: "required-continue"}); !errors.Is(err, denied) || !slices.Equal(authorized, []asset.AssetID{"configuration", "first", "second", "view"}) {
		t.Fatalf("continuation omitted required resource permission ids=%v err=%v", authorized, err)
	}
	stored, err := repositories.Executions().GetExecution(ctx, created.ID)
	if err != nil || stored.ContinueCount != 0 || stored.Status != execution.ExecutionFailed {
		t.Fatalf("unauthorized continuation persisted %+v %v", stored, err)
	}
}

func TestRequiredDeletionWorkerRejectsIncompleteFrozenContract(t *testing.T) {
	for _, mode := range []string{"missing-snapshot", "wrong-step", "foreign-snapshot"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			repositories, planner := requiredDeletionExecutionFixture(t, cleanup.ExecutionAuthorizerFunc(func(context.Context, string, []asset.AssetID) error { return nil }))
			if _, err := planner.CreateExecution(ctx, cleanup.CreateExecutionRequest{CleanupTaskID: "cln-required", RequestedBy: "operator", IdempotencyKey: "malformed-required", Confirmation: cleanup.ExecutionConfirmation{Acknowledged: true}}); err != nil {
				t.Fatal(err)
			}
			driver := &scriptedActionDriver{readback: contracts.ReadbackResult{Exists: false}}
			handler := cleanup.NewExecutionHandler(planner, cleanup.ActionResolverFunc(func(context.Context, asset.Asset) (cleanup.ActionDriver, error) { return driver, nil }))
			if err := handler.Handle(ctx, cleanupExecutionJobForAsset(t, repositories, "cln-required", "configuration")); err != nil {
				t.Fatal(err)
			}
			aggregate, err := repositories.CleanupTasks().GetTask(ctx, "cln-required")
			if err != nil {
				t.Fatal(err)
			}
			for i := range aggregate.Steps {
				step := &aggregate.Steps[i]
				if step.AssetID == "configuration" {
					if mode == "missing-snapshot" {
						delete(step.Evidence, plan.EvidencePlannedAsset)
					}
					if mode == "foreign-snapshot" {
						value := planningAsset("configuration", "other", "ACS::ECS::Instance", "configuration-native", time.Now())
						step.Evidence[plan.EvidencePlannedAsset] = value
					}
				}
				if step.AssetID == "first" && mode == "wrong-step" {
					step.Evidence[plan.EvidenceRequiredDeletions] = []plan.RequiredDeletion{{AssetID: "second", StepID: cleanupTaskStepForAsset(aggregate.Steps, "configuration").ID}}
				}
			}
			if err := repositories.CleanupTasks().ReplaceTask(ctx, aggregate.Task, aggregate.Steps, aggregate.ImpactItems); err != nil {
				t.Fatal(err)
			}
			before := driver.executeCalls
			if err := handler.Handle(ctx, cleanupExecutionJobForAsset(t, repositories, "cln-required", "first")); err == nil || driver.executeCalls != before {
				t.Fatalf("malformed required contract executed: %v", err)
			}
		})
	}
}

func TestIndependentRequiredDeletionSurvivesPlanningAndExecutionPersistence(t *testing.T) {
	for _, selected := range []bool{false, true} {
		t.Run(fmt.Sprint(selected), func(t *testing.T) {
			ctx := context.Background()
			repositories := openPlanningRepositories(t)
			now := time.Date(2026, 9, 10, 11, 0, 0, 0, time.UTC)
			var assets []asset.Asset
			for _, id := range []asset.AssetID{"cluster", "job", "query"} {
				assets = append(assets, planningAsset(id, "c-1", "ACS::ECS::Instance", string(id)+"-native", now))
			}
			binding := planningBinding("job-query", "job", "query", graph.OwnershipExclusive, graph.CleanupDelegate, "independent-graph", now)
			binding.Evidence[graph.LifecycleEvidenceControllerVerifiesManagedAbsence] = true
			relation := graph.Relationship{ID: "cluster-requires-job", SourceAssetID: "cluster", TargetAssetID: "job", Type: graph.RelationshipDependsOn, Source: "provider:native-lifecycle", Confidence: 1, GraphRevision: "independent-graph", ObservedAt: now, Evidence: map[string]any{
				graph.RelationshipEvidenceRequiredDeletion: true, graph.RelationshipEvidenceAutomaticSelection: false, graph.RelationshipEvidenceAuthority: graph.AuthorityAuthoritative, graph.RelationshipEvidenceDeletionOrder: graph.DeletionOrderTargetBeforeSource,
			}}
			seedPlanningGraph(t, repositories, "scope-a", "independent-graph", assets, []graph.Relationship{relation}, []graph.LifecycleBinding{binding})
			var authorized []asset.AssetID
			planner := cleanup.NewService(repositories, bundleResolver{asset.ProviderAliCloud: {Provider: asset.ProviderAliCloud, Revision: "bundle-a", Hash: "spec-a"}}, cleanup.WithClock(func() time.Time { return now }), cleanup.WithTaskIDGenerator(func() string { return "cln-independent" }), cleanup.WithExecutionIDGenerator(func() string { return "execution-independent" }), cleanup.WithExecutionAuthorizer(cleanup.ExecutionAuthorizerFunc(func(_ context.Context, _ string, ids []asset.AssetID) error {
				authorized = slices.Clone(ids)
				return nil
			})))
			selectors := []plan.CleanupSelector{assetSelector("cluster")}
			if selected {
				selectors = append(selectors, assetSelector("job"))
			}
			aggregate, err := planner.CreateTask(ctx, cleanup.CreateTaskRequest{Selectors: selectors, CreatedBy: "operator"})
			if err != nil {
				t.Fatal(err)
			}
			aggregate, err = repositories.CleanupTasks().GetTask(ctx, aggregate.Task.ID)
			if err != nil {
				t.Fatal(err)
			}
			executionRequest := cleanup.CreateExecutionRequest{CleanupTaskID: "cln-independent", RequestedBy: "operator", IdempotencyKey: "independent-key", Confirmation: cleanup.ExecutionConfirmation{Acknowledged: true}}
			if !selected {
				if aggregate.Task.Status != plan.StatusDraft || len(aggregate.Task.Blockers) == 0 || cleanupTaskStepForAsset(aggregate.Steps, "job").ID != "" || len(aggregate.ImpactItems) != 0 {
					t.Fatal("persisted graph silently selected independent job", aggregate)
				}
				if _, err := planner.CreateExecution(ctx, executionRequest); err == nil {
					t.Fatal("blocked independent cleanup executed")
				}
				return
			}
			if aggregate.Task.Status != plan.StatusReady || len(aggregate.Steps) != 2 || len(aggregate.ImpactItems) != 1 {
				t.Fatal("explicit selection lost", aggregate)
			}
			required, err := plan.RequiredDeletions(cleanupTaskStepForAsset(aggregate.Steps, "cluster"))
			if err != nil || len(required) != 1 || required[0].AssetID != "job" {
				t.Fatal("independent prerequisite lost after persistence", required, err)
			}
			if _, err := planner.CreateExecution(ctx, executionRequest); err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(authorized, []asset.AssetID{"cluster", "job", "query"}) {
				t.Fatal("authorization missed independent selection or its query", authorized)
			}
			job := &scriptedActionDriver{readback: contracts.ReadbackResult{Exists: false}}
			cluster := &scriptedActionDriver{pollInterval: time.Second, readback: contracts.ReadbackResult{Exists: false}}
			resolver := cleanup.ActionResolverFunc(func(_ context.Context, value asset.Asset) (cleanup.ActionDriver, error) {
				if value.ID == "job" {
					return job, nil
				}
				if value.ID == "cluster" {
					return cluster, nil
				}
				return nil, fmt.Errorf("unexpected independent deletion %s", value.ID)
			})
			handler := cleanup.NewExecutionHandler(planner, resolver)
			clusterJob := cleanupExecutionJobForAsset(t, repositories, "cln-independent", "cluster")
			var retry *cleanup.RetryError
			if err := handler.Handle(ctx, clusterJob); err != nil && !errors.As(err, &retry) {
				t.Fatal(err)
			}
			if cluster.executeCalls != 0 {
				t.Fatal("cluster executed before selected job")
			}
			if err := handler.Handle(ctx, cleanupExecutionJobForAsset(t, repositories, "cln-independent", "job")); err != nil {
				t.Fatal(err)
			}
			if err := handler.Handle(ctx, clusterJob); !errors.As(err, &retry) {
				t.Fatal("cluster did not persist pending execution", err)
			}
			handler = cleanup.NewExecutionHandler(planner, resolver)
			if err := handler.Handle(ctx, clusterJob); err != nil && !errors.As(err, &retry) {
				t.Fatal(err)
			}
			if job.executeCalls != 1 || cluster.executeCalls != 1 || len(cluster.waitRequests) != 1 {
				t.Fatal("independent cleanup was not resumable")
			}
			for _, request := range []contracts.ActionRequest{cluster.executeRequests[0], cluster.waitRequests[0]} {
				if len(request.PrerequisiteDeletions) != 1 || request.PrerequisiteDeletions[0].Asset.ID != "job" || request.PrerequisiteDeletions[0].Asset.ClosedAt != nil || request.PrerequisiteDeletions[0].ControllerID != "cluster" {
					t.Fatal("frozen independent prerequisite changed", request)
				}
			}
		})
	}
}

func TestRequiredManagedDeletionRestoresFrozenImpactAndRejectsTampering(t *testing.T) {
	for _, mode := range []string{"restore", "nested-restore", "nested-wrong-owner", "nested-unverified-parent", "nested-missing-parent", "nested-duplicate-parent", "missing-snapshot", "wrong-controller", "unverified", "retained", "foreign-snapshot", "duplicate-impact"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			repositories, planner := requiredDeletionExecutionFixture(t, cleanup.ExecutionAuthorizerFunc(func(context.Context, string, []asset.AssetID) error { return nil }), true, strings.HasPrefix(mode, "nested-"))
			if _, err := planner.CreateExecution(ctx, cleanup.CreateExecutionRequest{CleanupTaskID: "cln-required", RequestedBy: "operator", IdempotencyKey: "managed-required", Confirmation: cleanup.ExecutionConfirmation{Acknowledged: true}}); err != nil {
				t.Fatal(err)
			}
			owner := &scriptedActionDriver{readback: contracts.ReadbackResult{Exists: false}}
			source := &scriptedActionDriver{readback: contracts.ReadbackResult{Exists: false}}
			resolver := cleanup.ActionResolverFunc(func(_ context.Context, value asset.Asset) (cleanup.ActionDriver, error) {
				if value.ID == "configuration" {
					return owner, nil
				}
				if value.ID == "first" || value.ID == "second" {
					return source, nil
				}
				return nil, fmt.Errorf("unexpected independent action for %s", value.ID)
			})
			handler := cleanup.NewExecutionHandler(planner, resolver)
			if err := handler.Handle(ctx, cleanupExecutionJobForAsset(t, repositories, "cln-required", "configuration")); err != nil {
				t.Fatal(err)
			}
			value, err := repositories.Inventory().GetAsset(ctx, "view")
			if err != nil || value.ClosedAt == nil {
				t.Fatal("owner did not close managed prerequisite", err)
			}
			value.Normalized = map[string]any{"changed_after_plan": true}
			if err := repositories.Inventory().PutAsset(ctx, value); err != nil {
				t.Fatal(err)
			}
			aggregate, err := repositories.CleanupTasks().GetTask(ctx, "cln-required")
			if err != nil {
				t.Fatal(err)
			}
			for i := range aggregate.ImpactItems {
				impact := &aggregate.ImpactItems[i]
				if impact.AssetID != "view" {
					continue
				}
				switch mode {
				case "missing-snapshot":
					delete(impact.Evidence, plan.EvidencePlannedAsset)
				case "wrong-controller", "nested-wrong-owner":
					impact.ControllerID = "first"
				case "unverified":
					delete(impact.Evidence, graph.LifecycleEvidenceControllerVerifiesManagedAbsence)
				case "retained":
					impact.Expected = plan.ExpectedRetainExplicit
				case "foreign-snapshot":
					v := planningAsset("view", "other", "ACS::ECS::Instance", "view-native", time.Now())
					impact.Evidence[plan.EvidencePlannedAsset] = v
				}
			}
			for i := range aggregate.ImpactItems {
				impact := &aggregate.ImpactItems[i]
				if impact.AssetID != "node" {
					continue
				}
				switch mode {
				case "nested-unverified-parent":
					delete(impact.Evidence, graph.LifecycleEvidenceControllerVerifiesManagedAbsence)
				case "nested-missing-parent":
					impact.AssetID = "missing-node"
				case "nested-duplicate-parent":
					copy := *impact
					copy.ID = "duplicate-node"
					aggregate.ImpactItems = append(aggregate.ImpactItems, copy)
				}
			}
			if mode == "duplicate-impact" {
				v := aggregate.ImpactItems[0]
				v.ID = "duplicate-impact"
				aggregate.ImpactItems = append(aggregate.ImpactItems, v)
			}
			if err := repositories.CleanupTasks().ReplaceTask(ctx, aggregate.Task, aggregate.Steps, aggregate.ImpactItems); err != nil {
				t.Fatal(err)
			}
			handler = cleanup.NewExecutionHandler(planner, resolver)
			err = handler.Handle(ctx, cleanupExecutionJobForAsset(t, repositories, "cln-required", "first"))
			if mode != "restore" && mode != "nested-restore" {
				if err == nil || source.executeCalls != 0 {
					t.Fatal("altered managed prerequisite executed", mode, err)
				}
				return
			}
			if err != nil || source.executeCalls != 1 || owner.executeCalls != 1 {
				t.Fatal("managed prerequisite recovery", err)
			}
			request := source.executeRequests[0]
			if len(request.PrerequisiteDeletions) != 1 {
				t.Fatal("missing managed prerequisite", request)
			}
			restored := request.PrerequisiteDeletions[0]
			if restored.Asset.ID != "view" || restored.ControllerID != "first" || !restored.Delete || restored.Asset.ClosedAt != nil || restored.Asset.Normalized["changed_after_plan"] != nil || restored.Asset.Normalized["id"] != "view-native" {
				t.Fatal("managed prerequisite did not use its frozen impact", restored)
			}
		})
	}
}
