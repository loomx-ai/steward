package cleanup_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
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

func requiredDeletionExecutionFixture(t *testing.T, authorizer cleanup.ExecutionAuthorizer) (persistence.Repositories, *cleanup.Service) {
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
	seedPlanningGraph(t, repositories, "scope-a", "required-graph", assets, relationships, []graph.LifecycleBinding{owner, view})
	planner := cleanup.NewService(repositories, bundleResolver{asset.ProviderAliCloud: {Provider: asset.ProviderAliCloud, Revision: "bundle-a", Hash: "spec-a"}}, cleanup.WithClock(func() time.Time { return now }), cleanup.WithTaskIDGenerator(func() string { return "cln-required" }), cleanup.WithExecutionIDGenerator(func() string { return "execution-required" }), cleanup.WithExecutionAuthorizer(authorizer))
	aggregate, err := planner.CreateTask(context.Background(), cleanup.CreateTaskRequest{Selectors: []plan.CleanupSelector{assetSelector("first"), assetSelector("second")}, CreatedBy: "operator"})
	if err != nil || aggregate.Task.Status != plan.StatusReady || len(aggregate.Steps) != 3 || len(aggregate.ImpactItems) != 1 {
		t.Fatalf("required cleanup planning failed %+v %v", aggregate, err)
	}
	if !slices.Equal(aggregate.Task.ResolvedAssetIDs, []asset.AssetID{"first", "second"}) || cleanupTaskStepForAsset(aggregate.Steps, "retained-owner").ID != "" {
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
