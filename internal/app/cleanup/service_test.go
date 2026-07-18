package cleanup_test

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/loomx-ai/steward/internal/app/cleanup"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/internal/core/topology"
	"github.com/loomx-ai/steward/internal/persistence"
	"github.com/loomx-ai/steward/internal/persistence/sqlite"
	"github.com/loomx-ai/steward/internal/provider/contracts"
	"github.com/loomx-ai/steward/internal/provider/spec"
)

func TestCleanupServiceStoresCompleteControllerTaskSnapshot(t *testing.T) {
	ctx := context.Background()
	repositories := openPlanningRepositories(t)
	now := time.Date(2026, 7, 13, 10, 0, 0, 0, time.UTC)
	ack := planningAsset("ack", "c-1", "ACS::CS::Cluster", "cluster-1", now)
	ecs := planningAsset("ecs", "c-1", "ACS::ECS::Instance", "i-1", now)
	seedPlanningSnapshot(t, repositories, "scope-a", "graph-a", []asset.Asset{ack, ecs}, []graph.LifecycleBinding{
		planningBinding("binding-a", "ack", "ecs", graph.OwnershipExclusive, graph.CleanupDelegate, "graph-a", now),
	})

	service := cleanup.NewService(repositories, bundleResolver{
		asset.ProviderAliCloud: {Provider: asset.ProviderAliCloud, Revision: "bundle-a", Hash: "spec-a"},
	}, cleanup.WithClock(func() time.Time { return now }), cleanup.WithTaskIDGenerator(func() string { return "cln-a" }))
	aggregate, err := service.CreateTask(ctx, cleanup.CreateTaskRequest{
		Selectors: []plan.CleanupSelector{assetSelector("ack"), assetSelector("ecs")}, CreatedBy: "operator",
		RequestOptions: map[asset.AssetID]map[string]any{"ack": {"retain_resources": []string{"disk-1"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if aggregate.Task.ID != "cln-a" || aggregate.Task.Status != plan.StatusReady || aggregate.Task.SnapshotHash == "" || len(aggregate.Task.Blockers) != 0 {
		t.Fatalf("plan = %+v", aggregate.Task)
	}
	if len(aggregate.Steps) != 2 || len(aggregate.ImpactItems) != 1 || aggregate.ImpactItems[0].AssetID != "ecs" {
		t.Fatalf("aggregate = %+v", aggregate)
	}
	controller := cleanupStepForAsset(aggregate.Steps, "ack")
	verification := cleanupStepForAsset(aggregate.Steps, "ecs")
	if controller.Kind != plan.StepController ||
		verification.Kind != plan.StepVerification ||
		len(verification.DependsOn) != 1 ||
		verification.DependsOn[0] != controller.ID {
		t.Fatalf("steps = %+v", aggregate.Steps)
	}
	if aggregate.Task.Revision != (plan.RevisionBinding{InventoryRevision: aggregate.Task.Revision.InventoryRevision, GraphRevision: "graph-a", SpecBundleRevision: "bundle-a", SpecHash: "spec-a"}) || aggregate.Task.Revision.InventoryRevision == "" {
		t.Fatalf("revision = %+v", aggregate.Task.Revision)
	}
	if aggregate.Task.RequestOptions["ack"]["retain_resources"] == nil {
		t.Fatalf("request options were not bound: %+v", aggregate.Task.RequestOptions)
	}
	if want := [][]asset.AssetID{{"ack"}, {"ecs"}}; !reflect.DeepEqual(aggregate.Task.SelectorAssetIDs, want) {
		t.Fatalf("selector asset IDs = %v, want %v", aggregate.Task.SelectorAssetIDs, want)
	}
	stored, err := repositories.CleanupTasks().GetTask(ctx, "cln-a")
	if err != nil || stored.Task.SnapshotHash != aggregate.Task.SnapshotHash ||
		!reflect.DeepEqual(stored.Task.SelectorAssetIDs, aggregate.Task.SelectorAssetIDs) ||
		len(stored.Steps) != 2 || len(stored.ImpactItems) != 1 {
		t.Fatalf("stored = %+v, err = %v", stored, err)
	}
}

func TestCleanupServiceReadsLegacyCreatedFromDependencyWithoutMutatingSnapshot(t *testing.T) {
	ctx := context.Background()
	repositories := openPlanningRepositories(t)
	now := time.Date(2026, 8, 10, 6, 45, 0, 0, time.UTC)
	image := planningAsset("image", "c-1", "ACS::ECS::Image", "m-1", now)
	snapshot := planningAsset("snapshot", "c-1", "ACS::ECS::Snapshot", "s-1", now)
	seedPlanningSnapshot(
		t,
		repositories,
		"scope-a",
		"graph-a",
		[]asset.Asset{image, snapshot},
		nil,
	)
	if err := repositories.Graph().ReplaceGraph(
		ctx,
		"scope-a",
		"graph-a",
		[]graph.Relationship{{
			ID: "image-snapshot", SourceAssetID: image.ID, TargetAssetID: snapshot.ID,
			Type: graph.RelationshipCreatedFrom, Source: "test", Confidence: 1,
			GraphRevision: "graph-a", ObservedAt: now,
		}},
		nil,
	); err != nil {
		t.Fatal(err)
	}
	task := plan.CleanupTask{
		ID: "cln-legacy-created-from", ConnectionID: "c-1", Status: plan.StatusExecuting,
		ResolvedAssetIDs: []asset.AssetID{image.ID, snapshot.ID},
		CreatedBy:        "operator", CreatedAt: now,
	}
	steps := []plan.CleanupTaskStep{
		{ID: "step-image", CleanupTaskID: task.ID, AssetID: image.ID, Action: "delete"},
		{ID: "step-snapshot", CleanupTaskID: task.ID, AssetID: snapshot.ID, Action: "delete"},
	}
	if err := repositories.CleanupTasks().CreateTask(ctx, task, steps, nil); err != nil {
		t.Fatal(err)
	}

	service := cleanup.NewService(repositories, nil)
	aggregate, err := service.GetTask(ctx, task.ID, task.ConnectionID)
	if err != nil {
		t.Fatal(err)
	}
	snapshotStep := cleanupStepForAsset(aggregate.Steps, snapshot.ID)
	if len(snapshotStep.DependsOn) != 1 || snapshotStep.DependsOn[0] != "step-image" {
		t.Fatalf("read-time compatibility steps=%+v", aggregate.Steps)
	}
	stored, err := repositories.CleanupTasks().GetTask(ctx, task.ID)
	if err != nil || len(cleanupStepForAsset(stored.Steps, snapshot.ID).DependsOn) != 0 {
		t.Fatalf("stored immutable snapshot=%+v err=%v", stored.Steps, err)
	}
}

func TestCleanupServiceRejectsTaskWhileInventoryRelationshipsAreReconciling(t *testing.T) {
	ctx := context.Background()
	repositories := openPlanningRepositories(t)
	now := time.Date(2026, 8, 5, 9, 59, 0, 0, time.UTC)
	value := planningAsset("reconciling-asset", "reconciling-connection", "ACS::ESS::ScalingGroup", "asg-1", now)
	seedPlanningSnapshot(t, repositories, "reconciling-scope", "graph-before-scan", []asset.Asset{value}, nil)
	if err := repositories.Inventory().CreateScanRun(ctx, asset.ScanRun{
		ID: "scan-reconciling", ConnectionID: value.Identity.ConnectionID,
		Status: asset.ScanReconciling, CompletionStatus: asset.ScanSucceeded,
		CreatedAt: now, StartedAt: &now,
	}); err != nil {
		t.Fatal(err)
	}
	service := cleanup.NewService(
		repositories,
		bundleResolver{asset.ProviderAliCloud: {Provider: asset.ProviderAliCloud, Revision: "bundle-a", Hash: "spec-a"}},
		cleanup.WithTaskIDGenerator(func() string { return "cln-must-not-persist" }),
	)

	_, err := service.CreateTask(ctx, cleanup.CreateTaskRequest{
		Selectors: []plan.CleanupSelector{assetSelector(value.ID)},
		CreatedBy: "operator",
	})
	if !errors.Is(err, cleanup.ErrInventoryReconciliationPending) {
		t.Fatalf("CreateTask() error = %v, want ErrInventoryReconciliationPending", err)
	}
	if _, lookupErr := repositories.CleanupTasks().GetTask(ctx, "cln-must-not-persist"); !errors.Is(lookupErr, persistence.ErrNotFound) {
		t.Fatalf("cleanup task was persisted during graph reconciliation: %v", lookupErr)
	}
}

func TestCleanupServiceBindsChildScopeAssetsToRootGraphRevision(t *testing.T) {
	ctx := context.Background()
	repositories := openPlanningRepositories(t)
	now := time.Date(2026, 7, 13, 10, 0, 0, 0, time.UTC)
	if err := repositories.Inventory().PutScope(ctx, asset.Scope{
		ID: "scope-root", ConnectionID: "c-1", Kind: asset.ScopeAccount,
		NativeID: "account-1", Name: "account-1", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Inventory().PutScope(ctx, asset.Scope{
		ID: "scope-region", ConnectionID: "c-1", ParentID: "scope-root", Kind: asset.ScopeRegion,
		NativeID: "cn-hangzhou", Name: "cn-hangzhou", Location: "cn-hangzhou", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	cluster := planningAsset("ack", "c-1", "ACS::CS::Cluster", "cluster-1", now)
	cluster.ScopeID = "scope-region"
	seedPlanningSnapshot(t, repositories, "scope-root", "graph-root-a", []asset.Asset{cluster}, nil)

	service := cleanup.NewService(repositories, bundleResolver{
		asset.ProviderAliCloud: {Provider: asset.ProviderAliCloud, Revision: "bundle-a", Hash: "spec-a"},
	}, cleanup.WithTaskIDGenerator(func() string { return "cln-root-revision" }))
	created, err := service.CreateTask(ctx, cleanup.CreateTaskRequest{Selectors: []plan.CleanupSelector{assetSelector("ack")}, CreatedBy: "operator"})
	if err != nil {
		t.Fatal(err)
	}
	if created.Task.Revision.GraphRevision != "graph-root-a" {
		t.Fatalf("graph revision = %q, want root revision", created.Task.Revision.GraphRevision)
	}

	if err := repositories.Graph().ReplaceGraph(ctx, "scope-root", "graph-root-b", nil, nil); err != nil {
		t.Fatal(err)
	}
	validated, err := service.ValidateTask(ctx, created.Task.ID)
	if !errors.Is(err, plan.ErrCleanupTaskInvalidated) || validated.Task.Status != plan.StatusInvalidated {
		t.Fatalf("validated = %+v, err = %v", validated, err)
	}
}

func TestCleanupServicePersistsManagedChildBlocker(t *testing.T) {
	ctx := context.Background()
	repositories := openPlanningRepositories(t)
	now := time.Date(2026, 7, 13, 10, 0, 0, 0, time.UTC)
	ack := planningAsset("ack", "c-1", "ACS::CS::Cluster", "cluster-1", now)
	ecs := planningAsset("ecs", "c-1", "ACS::ECS::Instance", "i-1", now)
	seedPlanningSnapshot(t, repositories, "scope-a", "graph-a", []asset.Asset{ack, ecs}, []graph.LifecycleBinding{
		planningBinding("binding-a", "ack", "ecs", graph.OwnershipExclusive, graph.CleanupDelegate, "graph-a", now),
	})
	service := cleanup.NewService(repositories, bundleResolver{asset.ProviderAliCloud: {Provider: asset.ProviderAliCloud, Revision: "bundle-a", Hash: "spec-a"}}, cleanup.WithTaskIDGenerator(func() string { return "cln-child" }))

	aggregate, err := service.CreateTask(ctx, cleanup.CreateTaskRequest{Selectors: []plan.CleanupSelector{assetSelector("ecs")}, CreatedBy: "operator"})
	if err != nil {
		t.Fatal(err)
	}
	if aggregate.Task.Status != plan.StatusDraft || len(aggregate.Steps) != 0 || !containsTaskBlocker(aggregate.Task.Blockers, plan.BlockManagedByController, "ecs") {
		t.Fatalf("aggregate = %+v", aggregate)
	}
	stored, err := repositories.CleanupTasks().GetTask(ctx, "cln-child")
	if err != nil || !containsTaskBlocker(stored.Task.Blockers, plan.BlockManagedByController, "ecs") {
		t.Fatalf("stored = %+v, err = %v", stored, err)
	}
}

func TestCleanupServiceAddsAssetsToCurrentTaskAndReplacesPlan(t *testing.T) {
	ctx := context.Background()
	repositories := openPlanningRepositories(t)
	now := time.Date(2026, 7, 13, 10, 0, 0, 0, time.UTC)
	ack := planningAsset("ack", "c-1", "ACS::CS::Cluster", "cluster-1", now)
	ecs := planningAsset("ecs", "c-1", "ACS::ECS::Instance", "i-1", now)
	seedPlanningSnapshot(t, repositories, "scope-a", "graph-a", []asset.Asset{ack, ecs}, []graph.LifecycleBinding{
		planningBinding("binding-a", "ack", "ecs", graph.OwnershipExclusive, graph.CleanupDelegate, "graph-a", now),
	})
	service := cleanup.NewService(
		repositories,
		bundleResolver{asset.ProviderAliCloud: {Provider: asset.ProviderAliCloud, Revision: "bundle-a", Hash: "spec-a"}},
		cleanup.WithClock(func() time.Time { return now }),
		cleanup.WithTaskIDGenerator(func() string { return "cln-update" }),
	)

	created, err := service.CreateTask(ctx, cleanup.CreateTaskRequest{
		Selectors: []plan.CleanupSelector{assetSelector("ecs")}, CreatedBy: "creator",
		RequestOptions: map[asset.AssetID]map[string]any{"ack": {"retain_resources": []string{"disk-1"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.Task.Status != plan.StatusDraft || len(created.Steps) != 0 {
		t.Fatalf("created = %+v", created)
	}
	createdAt := created.Task.CreatedAt
	now = now.Add(time.Minute)

	updated, err := service.AddTaskAssets(ctx, cleanup.AddTaskAssetsRequest{
		ConnectionID: "c-1", CleanupTaskID: created.Task.ID, AssetIDs: []asset.AssetID{"ack", "ack"}, UpdatedBy: "operator",
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Task.ID != created.Task.ID || updated.Task.CreatedBy != "creator" || !updated.Task.CreatedAt.Equal(createdAt) {
		t.Fatalf("task identity changed: %+v", updated.Task)
	}
	if updated.Task.UpdatedAt == nil || !updated.Task.UpdatedAt.Equal(now) {
		t.Fatalf("updated at = %v, want %v", updated.Task.UpdatedAt, now)
	}
	if updated.Task.Status != plan.StatusReady || len(updated.Task.Blockers) != 0 || len(updated.Steps) != 2 || len(updated.ImpactItems) != 1 {
		t.Fatalf("updated aggregate = %+v", updated)
	}
	if len(updated.Task.Selectors) != 2 || updated.Task.Selectors[0].AssetID != "ecs" || updated.Task.Selectors[1].AssetID != "ack" {
		t.Fatalf("selectors = %+v", updated.Task.Selectors)
	}
	if updated.Task.RequestOptions["ack"]["retain_resources"] == nil {
		t.Fatalf("request options were not preserved: %+v", updated.Task.RequestOptions)
	}
	stored, err := repositories.CleanupTasks().GetTask(ctx, created.Task.ID)
	if err != nil || stored.Task.SnapshotHash != updated.Task.SnapshotHash || len(stored.Steps) != len(updated.Steps) || len(stored.ImpactItems) != len(updated.ImpactItems) {
		t.Fatalf("stored = %+v, updated = %+v, err = %v", stored, updated, err)
	}
	audits, err := repositories.Audits().ListAuditEvents(ctx, persistence.ListOptions{Limit: 10})
	if err != nil || len(audits.Items) < 2 || audits.Items[0].Action != "cleanup.task.update" || audits.Items[0].Actor != "operator" {
		t.Fatalf("audits = %+v, err = %v", audits.Items, err)
	}
}

func TestCleanupServiceReplansWhenDependencySelectorAlreadyExists(t *testing.T) {
	ctx := context.Background()
	repositories := openPlanningRepositories(t)
	now := time.Date(2026, 8, 10, 8, 0, 0, 0, time.UTC)
	vpc := planningAsset("vpc", "c-1", "ACS::VPC::VPC", "vpc-a", now)
	attachment := planningAsset(
		"cen-child",
		"c-1",
		"ACS::CEN::ChildInstanceAttachment",
		"cen-a/vpc-a",
		now,
	)
	attachment.Capabilities = asset.CapabilitySet{asset.CapabilityIndexed}
	seedPlanningSnapshot(t, repositories, "scope-a", "graph-a", []asset.Asset{
		vpc, attachment,
	}, nil)
	if err := repositories.Graph().ReplaceGraph(
		ctx,
		"scope-a",
		"graph-a",
		[]graph.Relationship{{
			ID: "cen-child-vpc", SourceAssetID: attachment.ID, TargetAssetID: vpc.ID,
			Type: graph.RelationshipAttachedTo, Source: "test", Confidence: 1,
			GraphRevision: "graph-a", ObservedAt: now,
		}},
		nil,
	); err != nil {
		t.Fatal(err)
	}
	legacyService := cleanup.NewService(
		repositories,
		bundleResolver{asset.ProviderAliCloud: {
			Provider: asset.ProviderAliCloud, Revision: "legacy-bundle", Hash: "legacy-spec",
		}},
		cleanup.WithClock(func() time.Time { return now }),
		cleanup.WithTaskIDGenerator(func() string { return "cln-existing-dependency" }),
	)
	created, err := legacyService.CreateTask(ctx, cleanup.CreateTaskRequest{
		Selectors: []plan.CleanupSelector{
			assetSelector(vpc.ID), assetSelector(attachment.ID),
		},
		CreatedBy: "creator",
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.Task.Status != plan.StatusDraft ||
		!containsTaskBlocker(created.Task.Blockers, plan.BlockCrossScopeDependency, vpc.ID) {
		t.Fatalf("initial task=%+v", created.Task)
	}

	// The current provider spec now supports this dependency, while its stored
	// asset projection still reflects the older scan-only capability set.
	// Repeating the add operation must use the current spec and rebuild the
	// snapshot instead of returning the stale task unchanged.
	now = now.Add(time.Minute)
	upgradedService := cleanup.NewService(
		repositories,
		bundleResolver{asset.ProviderAliCloud: {
			Provider: asset.ProviderAliCloud, Revision: "bundle-a", Hash: "spec-a",
			Specs: []spec.CompiledSpec{{ResourceKind: asset.ResourceKind{
				Provider:   asset.ProviderAliCloud,
				NativeType: "ACS::CEN::ChildInstanceAttachment",
				Capabilities: asset.CapabilitySet{
					asset.CapabilityIndexed, asset.CapabilityActionable,
				},
			}}},
		}},
		cleanup.WithClock(func() time.Time { return now }),
	)
	updated, err := upgradedService.AddTaskAssets(ctx, cleanup.AddTaskAssetsRequest{
		ConnectionID: "c-1", CleanupTaskID: created.Task.ID,
		AssetIDs: []asset.AssetID{attachment.ID}, UpdatedBy: "operator",
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Task.Status != plan.StatusReady || len(updated.Task.Blockers) != 0 ||
		len(updated.Steps) != 2 || updated.Task.UpdatedAt == nil ||
		!updated.Task.UpdatedAt.Equal(now) {
		t.Fatalf("replanned task=%+v steps=%+v", updated.Task, updated.Steps)
	}
}

func TestCleanupServiceRejectsAddingAssetsAfterTaskStarts(t *testing.T) {
	ctx := context.Background()
	repositories := openPlanningRepositories(t)
	now := time.Date(2026, 7, 13, 10, 0, 0, 0, time.UTC)
	value := planningAsset("asset-a", "c-1", "ACS::ECS::Instance", "i-1", now)
	seedPlanningSnapshot(t, repositories, "scope-a", "graph-a", []asset.Asset{value}, nil)
	service := cleanup.NewService(
		repositories,
		bundleResolver{asset.ProviderAliCloud: {Provider: asset.ProviderAliCloud, Revision: "bundle-a", Hash: "spec-a"}},
		cleanup.WithTaskIDGenerator(func() string { return "cln-started" }),
	)
	created, err := service.CreateTask(ctx, cleanup.CreateTaskRequest{Selectors: []plan.CleanupSelector{assetSelector(value.ID)}, CreatedBy: "creator"})
	if err != nil {
		t.Fatal(err)
	}
	created.Task.Status = plan.StatusExecuting
	if err := repositories.CleanupTasks().UpdateTask(ctx, created.Task); err != nil {
		t.Fatal(err)
	}
	_, err = service.AddTaskAssets(ctx, cleanup.AddTaskAssetsRequest{
		ConnectionID: "c-1", CleanupTaskID: created.Task.ID, AssetIDs: []asset.AssetID{value.ID}, UpdatedBy: "operator",
	})
	if !errors.Is(err, cleanup.ErrTaskNotEditable) {
		t.Fatalf("AddTaskAssets() error = %v, want ErrTaskNotEditable", err)
	}
}

func TestCleanupServiceInvalidatesTaskWhenLifecycleSnapshotChanges(t *testing.T) {
	ctx := context.Background()
	repositories := openPlanningRepositories(t)
	now := time.Date(2026, 7, 13, 10, 0, 0, 0, time.UTC)
	ack := planningAsset("ack", "c-1", "ACS::CS::Cluster", "cluster-1", now)
	ecs := planningAsset("ecs", "c-1", "ACS::ECS::Instance", "i-1", now)
	seedPlanningSnapshot(t, repositories, "scope-a", "graph-a", []asset.Asset{ack, ecs}, []graph.LifecycleBinding{
		planningBinding("binding-a", "ack", "ecs", graph.OwnershipExclusive, graph.CleanupDelegate, "graph-a", now),
	})
	service := cleanup.NewService(repositories, bundleResolver{asset.ProviderAliCloud: {Provider: asset.ProviderAliCloud, Revision: "bundle-a", Hash: "spec-a"}}, cleanup.WithTaskIDGenerator(func() string { return "cln-stale" }))
	created, err := service.CreateTask(ctx, cleanup.CreateTaskRequest{Selectors: []plan.CleanupSelector{assetSelector("ack")}, CreatedBy: "operator"})
	if err != nil || created.Task.Status != plan.StatusReady {
		t.Fatalf("created = %+v, err = %v", created, err)
	}

	if err := repositories.Graph().ReplaceGraph(ctx, "scope-a", "graph-b", nil, []graph.LifecycleBinding{
		planningBinding("binding-b", "ack", "ecs", graph.OwnershipShared, graph.CleanupRetain, "graph-b", now.Add(time.Minute)),
	}); err != nil {
		t.Fatal(err)
	}
	validated, err := service.ValidateTask(ctx, "cln-stale")
	if !errors.Is(err, plan.ErrCleanupTaskInvalidated) || validated.Task.Status != plan.StatusInvalidated || validated.Task.InvalidationReason == "" {
		t.Fatalf("validated = %+v, err = %v", validated, err)
	}
	stored, getErr := repositories.CleanupTasks().GetTask(ctx, "cln-stale")
	if getErr != nil || stored.Task.Status != plan.StatusInvalidated || stored.Task.InvalidationReason == "" {
		t.Fatalf("stored = %+v, err = %v", stored, getErr)
	}
}

func TestCleanupServiceExpandsConnectionAcrossRegionalAndGlobalScopes(t *testing.T) {
	ctx := context.Background()
	repositories := openPlanningRepositories(t)
	now := time.Date(2026, 7, 13, 10, 30, 0, 0, time.UTC)
	connection := asset.CloudConnection{ID: "connection-range", Name: "production account", Provider: asset.ProviderAliCloud, Partition: "public", Principal: "production account", Status: asset.ConnectionActive, CreatedAt: now, UpdatedAt: now}
	if err := repositories.Connections().PutConnection(ctx, connection); err != nil {
		t.Fatal(err)
	}
	for _, scope := range []asset.Scope{
		{ID: "account-range", ConnectionID: connection.ID, Kind: asset.ScopeAccount, NativeID: "123", Name: "production account", CreatedAt: now, UpdatedAt: now},
		{ID: "region-range", ConnectionID: connection.ID, ParentID: "account-range", Kind: asset.ScopeRegion, NativeID: "cn-hangzhou", Name: "cn-hangzhou", CreatedAt: now, UpdatedAt: now},
		{ID: "global-range", ConnectionID: connection.ID, ParentID: "account-range", Kind: asset.ScopeGlobal, NativeID: "global", Name: "Global", CreatedAt: now, UpdatedAt: now},
	} {
		if err := repositories.Inventory().PutScope(ctx, scope); err != nil {
			t.Fatal(err)
		}
	}
	ack := planningAsset("ack-range", connection.ID, "ACS::CS::Cluster", "cluster-1", now)
	ack.ScopeID = "region-range"
	ecs := planningAsset("ecs-range", connection.ID, "ACS::ECS::Instance", "i-1", now)
	ecs.ScopeID = "region-range"
	global := planningAsset("global-range", connection.ID, "ACS::RAM::Role", "role-1", now)
	global.ScopeID = "global-range"
	for _, value := range []asset.Asset{ack, ecs, global} {
		if err := repositories.Inventory().PutAsset(ctx, value); err != nil {
			t.Fatal(err)
		}
	}
	binding := planningBinding("binding-range", ack.ID, ecs.ID, graph.OwnershipExclusive, graph.CleanupDelegate, "graph-range", now)
	if err := repositories.Graph().ReplaceGraph(ctx, "account-range", "graph-range", nil, []graph.LifecycleBinding{binding}); err != nil {
		t.Fatal(err)
	}
	seedCompleteScan(t, repositories, connection.ID, "account-range", now)
	service := cleanup.NewService(repositories, bundleResolver{asset.ProviderAliCloud: {Provider: asset.ProviderAliCloud, Revision: "bundle-a", Hash: "spec-a"}}, cleanup.WithTaskIDGenerator(func() string { return "cln-range" }))

	aggregate, err := service.CreateTask(ctx, cleanup.CreateTaskRequest{Selectors: []plan.CleanupSelector{{Kind: plan.SelectorConnection, ConnectionID: connection.ID}}, CreatedBy: "operator"})
	if err != nil {
		t.Fatal(err)
	}
	if aggregate.Task.Status != plan.StatusReady || aggregate.Task.Coverage.Status != "complete" || !reflect.DeepEqual(aggregate.Task.ResolvedAssetIDs, []asset.AssetID{"ack-range", "ecs-range", "global-range"}) {
		t.Fatalf("plan = %+v", aggregate.Task)
	}
	if len(aggregate.Task.Selectors) != 1 || aggregate.Task.Selectors[0].DisplayName != connection.Principal || len(aggregate.Steps) != 3 || len(aggregate.ImpactItems) != 1 || aggregate.ImpactItems[0].AssetID != ecs.ID {
		t.Fatalf("aggregate = %+v", aggregate)
	}

	newAsset := planningAsset("late-range", connection.ID, "ACS::OSS::Bucket", "bucket-1", now.Add(time.Minute))
	newAsset.ScopeID = "global-range"
	if err := repositories.Inventory().PutAsset(ctx, newAsset); err != nil {
		t.Fatal(err)
	}
	validated, err := service.ValidateTask(ctx, aggregate.Task.ID)
	if !errors.Is(err, plan.ErrCleanupTaskInvalidated) || validated.Task.Status != plan.StatusInvalidated {
		t.Fatalf("validated=%+v err=%v", validated, err)
	}
}

func TestCleanupServiceInvalidatesWhenStoredRangeReexpandsToEmpty(t *testing.T) {
	ctx := context.Background()
	repositories := openPlanningRepositories(t)
	now := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	value := planningAsset("range-last", "connection-empty-range", "ACS::ECS::Instance", "i-last", now)
	value.ScopeID = "scope-empty-range"
	seedPlanningSnapshot(t, repositories, "scope-empty-range", "graph-empty-range", []asset.Asset{value}, nil)
	seedCompleteScan(t, repositories, value.Identity.ConnectionID, value.ScopeID, now)
	service := cleanup.NewService(repositories, bundleResolver{
		asset.ProviderAliCloud: {Provider: asset.ProviderAliCloud, Revision: "bundle-a", Hash: "spec-a"},
	}, cleanup.WithTaskIDGenerator(func() string { return "cln-empty-range" }))
	created, err := service.CreateTask(ctx, cleanup.CreateTaskRequest{
		Selectors: []plan.CleanupSelector{{
			Kind: plan.SelectorScope, ConnectionID: value.Identity.ConnectionID,
			ScopeID: value.ScopeID, Descendants: true,
		}},
		CreatedBy: "operator",
	})
	if err != nil {
		t.Fatalf("create range task: %v", err)
	}

	closedAt := now.Add(time.Minute)
	value.ClosedAt = &closedAt
	if err := repositories.Inventory().PutAsset(ctx, value); err != nil {
		t.Fatal(err)
	}
	validated, err := service.ValidateTask(ctx, created.Task.ID)
	if !errors.Is(err, plan.ErrCleanupTaskInvalidated) || validated.Task.Status != plan.StatusInvalidated {
		t.Fatalf("validated = %+v, err = %v", validated, err)
	}
	stored, getErr := repositories.CleanupTasks().GetTask(ctx, created.Task.ID)
	if getErr != nil || stored.Task.Status != plan.StatusInvalidated || stored.Task.InvalidationReason == "" {
		t.Fatalf("stored = %+v, err = %v", stored, getErr)
	}
}

func TestCleanupServiceLastCompleteScanSkipsIncompleteShardCoverage(t *testing.T) {
	ctx := context.Background()
	repositories := openPlanningRepositories(t)
	now := time.Date(2026, 7, 24, 12, 30, 0, 0, time.UTC)
	value := planningAsset("coverage-range", "connection-coverage-range", "ACS::ECS::Instance", "i-coverage", now)
	value.ScopeID = "scope-coverage-range"
	seedPlanningSnapshot(t, repositories, value.ScopeID, "graph-coverage-range", []asset.Asset{value}, nil)
	seedCompleteScan(t, repositories, value.Identity.ConnectionID, value.ScopeID, now)
	oldComplete := now.Add(time.Minute)

	newFinished := now.Add(3 * time.Minute)
	newRun := asset.ScanRun{
		ID: "scan-new-incomplete", ConnectionID: value.Identity.ConnectionID,
		ScopeMode: asset.ScanAllActiveRegions,
		Status:    asset.ScanSucceeded, CreatedAt: now.Add(2 * time.Minute), FinishedAt: &newFinished,
	}
	if err := repositories.Inventory().CreateScanRun(ctx, newRun); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Inventory().PutScanShard(ctx, asset.ScanShard{
		ID: "shard-new-incomplete", ScanRunID: newRun.ID, Provider: asset.ProviderAliCloud,
		Source: "resource-center", ScopeID: value.ScopeID, Status: asset.ShardSkipped,
		Coverage: asset.Coverage{Complete: false}, CreatedAt: now.Add(2 * time.Minute), FinishedAt: &newFinished,
	}); err != nil {
		t.Fatal(err)
	}
	service := cleanup.NewService(repositories, bundleResolver{
		asset.ProviderAliCloud: {Provider: asset.ProviderAliCloud, Revision: "bundle-a", Hash: "spec-a"},
	}, cleanup.WithTaskIDGenerator(func() string { return "cln-coverage-range" }))
	created, err := service.CreateTask(ctx, cleanup.CreateTaskRequest{
		Selectors: []plan.CleanupSelector{{
			Kind: plan.SelectorScope, ConnectionID: value.Identity.ConnectionID,
			ScopeID: value.ScopeID, Descendants: true,
		}},
		CreatedBy: "operator",
	})
	if err != nil {
		t.Fatalf("create range task: %v", err)
	}
	if created.Task.Coverage.Status != "complete" || len(created.Task.Coverage.Connections) != 1 {
		t.Fatalf("coverage = %+v", created.Task.Coverage)
	}
	got := created.Task.Coverage.Connections[0].LastCompleteScanAt
	if got == nil || !got.Equal(oldComplete) {
		t.Fatalf("last complete scan = %v, want %v", got, oldComplete)
	}
}

func TestRangeCleanupWarnsForTargetedOrFilteredCoverage(t *testing.T) {
	for _, test := range []struct {
		name string
		run  asset.ScanRun
	}{
		{
			name: "selected network",
			run: asset.ScanRun{
				ID: "scan-targeted", Status: asset.ScanSucceeded, ScopeMode: asset.ScanSelectedNetworks,
				Targets: []asset.ScanTarget{{
					Key: topology.VPCFocusKey("cn-hangzhou", "vpc-a"), Kind: asset.ScanTargetVPC,
					RegionID: "cn-hangzhou", NativeID: "vpc-a",
				}},
			},
		},
		{
			name: "resource kind filtered",
			run: asset.ScanRun{
				ID: "scan-filtered", Status: asset.ScanSucceeded, ScopeMode: asset.ScanAllActiveRegions,
				Targets:         []asset.ScanTarget{{Key: "region:cn-hangzhou", Kind: asset.ScanTargetRegion, RegionID: "cn-hangzhou"}},
				ResourceKindIDs: []asset.ResourceKindID{"kind-instance"},
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			repositories, service, selectors := rangeCoverageFixture(t, test.run)
			for index, selector := range selectors {
				created, err := service.CreateTask(context.Background(), cleanup.CreateTaskRequest{
					Selectors: []plan.CleanupSelector{selector}, CreatedBy: "operator",
				})
				if err != nil {
					t.Fatalf("selector %d create task: %v", index, err)
				}
				if created.Task.Status != plan.StatusReady || created.Task.Coverage.Status != "incomplete" ||
					!containsTaskWarning(created.Task.Warnings, plan.WarningScanCoverageIncomplete, "") ||
					containsTaskBlocker(created.Task.Blockers, plan.BlockScanCoverageIncomplete, "") {
					t.Fatalf("selector %d task = %+v", index, created.Task)
				}
			}
			_ = repositories
		})
	}
}

func TestCleanupTaskValidationInvalidatesCoverageAfterActiveRegionAddition(t *testing.T) {
	now := time.Date(2026, 7, 24, 15, 0, 0, 0, time.UTC)
	run := asset.ScanRun{
		ID: "scan-full", Status: asset.ScanSucceeded, ScopeMode: asset.ScanAllActiveRegions,
		Targets: []asset.ScanTarget{{Key: "region:cn-hangzhou", Kind: asset.ScanTargetRegion, RegionID: "cn-hangzhou"}},
	}
	repositories, service, selectors := rangeCoverageFixture(t, run)
	created, err := service.CreateTask(context.Background(), cleanup.CreateTaskRequest{
		Selectors: []plan.CleanupSelector{selectors[1]}, CreatedBy: "operator",
	})
	if err != nil || created.Task.Status != plan.StatusReady {
		t.Fatalf("created = %+v, err = %v", created, err)
	}
	if err := repositories.Regions().PutRegion(context.Background(), asset.ConnectionRegion{
		ID: "region-new", ConnectionID: created.Task.ConnectionID, RegionID: "cn-beijing",
		Lifecycle: asset.RegionActive, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}

	validated, err := service.ValidateTask(context.Background(), created.Task.ID)
	if !errors.Is(err, plan.ErrCleanupTaskInvalidated) || validated.Task.Status != plan.StatusInvalidated {
		t.Fatalf("validated = %+v, err = %v", validated, err)
	}
}

func TestConnectionCoverageRequiresProviderDeclaredGlobalScope(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                string
		provider            asset.Provider
		rootScopeKinds      []asset.ScopeKind
		descriptorPresent   bool
		sourcePresent       bool
		duplicateDescriptor bool
		includeGlobalTarget bool
		wantComplete        bool
	}{
		{
			name: "AWS old Region-only scan", provider: asset.ProviderAWS,
			rootScopeKinds:    []asset.ScopeKind{asset.ScopeRegion, asset.ScopeGlobal},
			descriptorPresent: true, sourcePresent: true,
		},
		{
			name: "AWS Region and Global scan", provider: asset.ProviderAWS,
			rootScopeKinds:      []asset.ScopeKind{asset.ScopeRegion, asset.ScopeGlobal},
			descriptorPresent:   true,
			sourcePresent:       true,
			includeGlobalTarget: true, wantComplete: true,
		},
		{
			name: "AliCloud Region-only scan", provider: asset.ProviderAliCloud,
			rootScopeKinds:    []asset.ScopeKind{asset.ScopeRegion},
			descriptorPresent: true, sourcePresent: true, wantComplete: true,
		},
		{
			name: "provider descriptor missing", provider: "provider-missing",
		},
		{
			name: "provider inventory sources missing", provider: "provider-no-sources",
			descriptorPresent: true,
		},
		{
			name: "empty source scope list supports Global", provider: "provider-all-scopes",
			descriptorPresent: true, sourcePresent: true,
		},
		{
			name: "duplicate provider descriptors", provider: "provider-duplicate",
			rootScopeKinds:    []asset.ScopeKind{asset.ScopeRegion},
			descriptorPresent: true, sourcePresent: true, duplicateDescriptor: true,
		},
	}
	for index, test := range tests {
		index, test := index, test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			repositories := openPlanningRepositories(t)
			now := time.Date(2026, 7, 24, 19, index, 0, 0, time.UTC)
			connectionID := asset.ConnectionID(fmt.Sprintf("provider-coverage-%d", index))
			connection := asset.CloudConnection{
				ID: connectionID, Name: test.name, Provider: test.provider,
				Principal: test.name, Status: asset.ConnectionActive, CreatedAt: now, UpdatedAt: now,
			}
			if err := repositories.Connections().PutConnection(ctx, connection); err != nil {
				t.Fatal(err)
			}
			if err := repositories.Regions().PutRegion(ctx, asset.ConnectionRegion{
				ID:           fmt.Sprintf("provider-region-%d", index),
				ConnectionID: connectionID, RegionID: "region-a", Lifecycle: asset.RegionActive,
				CreatedAt: now, UpdatedAt: now,
			}); err != nil {
				t.Fatal(err)
			}
			for _, scope := range []asset.Scope{
				{
					ID: asset.ScopeID(fmt.Sprintf("provider-account-%d", index)), ConnectionID: connectionID,
					Kind: asset.ScopeAccount, NativeID: "account", CreatedAt: now, UpdatedAt: now,
				},
				{
					ID: asset.ScopeID(fmt.Sprintf("provider-region-scope-%d", index)), ConnectionID: connectionID,
					ParentID: asset.ScopeID(fmt.Sprintf("provider-account-%d", index)),
					Kind:     asset.ScopeRegion, NativeID: "region-a", CreatedAt: now, UpdatedAt: now,
				},
			} {
				if err := repositories.Inventory().PutScope(ctx, scope); err != nil {
					t.Fatal(err)
				}
			}
			value := planningAsset(
				asset.AssetID(fmt.Sprintf("provider-asset-%d", index)), connectionID,
				"example::instance", fmt.Sprintf("instance-%d", index), now,
			)
			value.Identity.Provider = test.provider
			value.ScopeID = asset.ScopeID(fmt.Sprintf("provider-region-scope-%d", index))
			if err := repositories.Inventory().PutAsset(ctx, value); err != nil {
				t.Fatal(err)
			}
			if err := repositories.Graph().ReplaceGraph(
				ctx, asset.ScopeID(fmt.Sprintf("provider-account-%d", index)),
				fmt.Sprintf("provider-graph-%d", index), nil, nil,
			); err != nil {
				t.Fatal(err)
			}
			finished := now.Add(time.Minute)
			targets := []asset.ScanTarget{{
				Key: "region:region-a", Kind: asset.ScanTargetRegion, RegionID: "region-a",
			}}
			if test.includeGlobalTarget {
				targets = append(targets, asset.ScanTarget{
					Key: "global", Kind: asset.ScanTargetGlobal, RegionID: "global",
				})
			}
			runID := asset.ScanRunID(fmt.Sprintf("provider-scan-%d", index))
			if err := repositories.Inventory().CreateScanRun(ctx, asset.ScanRun{
				ID: runID, ConnectionID: connectionID, Status: asset.ScanSucceeded,
				ScopeMode: asset.ScanAllActiveRegions, Targets: targets,
				CreatedAt: now, FinishedAt: &finished,
			}); err != nil {
				t.Fatal(err)
			}
			for targetIndex, target := range targets {
				if err := repositories.Inventory().PutScanShard(ctx, asset.ScanShard{
					ID:        asset.ScanShardID(fmt.Sprintf("provider-shard-%d-%d", index, targetIndex)),
					ScanRunID: runID, Provider: test.provider, Source: "provider-index",
					TargetKey: target.Key, RegionID: target.RegionID,
					ScopeID: asset.ScopeID(fmt.Sprintf("provider-account-%d", index)),
					Status:  asset.ShardSucceeded, Coverage: asset.Coverage{Complete: true, FreshAt: finished},
					CreatedAt: now, FinishedAt: &finished,
				}); err != nil {
					t.Fatal(err)
				}
			}
			descriptors := []contracts.ProviderDescriptor{{
				Provider: "known-other-provider",
				InventorySources: []contracts.InventorySource{{
					Name: "known-other-provider", RootScopeKinds: []asset.ScopeKind{asset.ScopeRegion},
				}},
			}}
			if test.duplicateDescriptor {
				descriptors = append(descriptors, contracts.ProviderDescriptor{Provider: test.provider})
			}
			if test.descriptorPresent {
				var sources []contracts.InventorySource
				if test.sourcePresent {
					sources = []contracts.InventorySource{{
						Name: "provider-index", RootScopeKinds: test.rootScopeKinds,
					}}
				}
				descriptors = append(descriptors, contracts.ProviderDescriptor{
					Provider: test.provider, InventorySources: sources,
				})
			}
			resolver := descriptorBundleResolver{
				bundleResolver: bundleResolver{
					test.provider: {Provider: test.provider, Revision: "bundle-provider", Hash: "spec-provider"},
				},
				descriptors: descriptors,
			}
			service := cleanup.NewService(
				repositories, resolver,
				cleanup.WithTaskIDGenerator(func() string { return fmt.Sprintf("provider-cln-%d", index) }),
			)
			created, err := service.CreateTask(ctx, cleanup.CreateTaskRequest{
				Selectors: []plan.CleanupSelector{{Kind: plan.SelectorConnection, ConnectionID: connectionID}},
				CreatedBy: "operator",
			})
			if err != nil {
				t.Fatal(err)
			}
			if test.wantComplete {
				if created.Task.Status != plan.StatusReady || created.Task.Coverage.Status != "complete" {
					t.Fatalf("provider-complete coverage = %+v", created.Task)
				}
				return
			}
			if created.Task.Status != plan.StatusReady || created.Task.Coverage.Status != "incomplete" ||
				!containsTaskWarning(created.Task.Warnings, plan.WarningScanCoverageIncomplete, "") ||
				containsTaskBlocker(created.Task.Blockers, plan.BlockScanCoverageIncomplete, "") {
				t.Fatalf("provider-incomplete coverage warning = %+v", created.Task)
			}
		})
	}
}

func TestRangeCleanupWarnsForExpandedRetiredAndGlobalScopes(t *testing.T) {
	repositories, service, selectors := retiredAndGlobalCoverageFixture(t)
	_ = repositories
	for name, selector := range selectors {
		t.Run(name, func(t *testing.T) {
			created, err := service.CreateTask(context.Background(), cleanup.CreateTaskRequest{
				Selectors: []plan.CleanupSelector{selector}, CreatedBy: "operator",
			})
			if err != nil {
				t.Fatal(err)
			}
			if created.Task.Status != plan.StatusReady || created.Task.Coverage.Status != "incomplete" ||
				!containsTaskWarning(created.Task.Warnings, plan.WarningScanCoverageIncomplete, "") ||
				containsTaskBlocker(created.Task.Blockers, plan.BlockScanCoverageIncomplete, "") {
				t.Fatalf("incomplete coverage warning missing: %+v", created.Task)
			}
		})
	}
}

func TestRangeCleanupWarnsForDeclaredTargetWithoutShard(t *testing.T) {
	ctx := context.Background()
	repositories := openPlanningRepositories(t)
	now := time.Date(2026, 7, 24, 17, 0, 0, 0, time.UTC)
	connection := asset.CloudConnection{
		ID: "connection-missing-shard", Name: "missing shard", Provider: asset.ProviderAliCloud,
		Principal: "missing shard", Status: asset.ConnectionActive, CreatedAt: now, UpdatedAt: now,
	}
	if err := repositories.Connections().PutConnection(ctx, connection); err != nil {
		t.Fatal(err)
	}
	for _, region := range []asset.ConnectionRegion{
		{ID: "region-h", ConnectionID: connection.ID, RegionID: "cn-hangzhou", Lifecycle: asset.RegionActive, CreatedAt: now, UpdatedAt: now},
		{ID: "region-b", ConnectionID: connection.ID, RegionID: "cn-beijing", Lifecycle: asset.RegionActive, CreatedAt: now, UpdatedAt: now},
	} {
		if err := repositories.Regions().PutRegion(ctx, region); err != nil {
			t.Fatal(err)
		}
	}
	for _, scope := range []asset.Scope{
		{ID: "account-missing-shard", ConnectionID: connection.ID, Kind: asset.ScopeAccount, NativeID: "account", CreatedAt: now, UpdatedAt: now},
		{ID: "region-h-missing-shard", ConnectionID: connection.ID, ParentID: "account-missing-shard", Kind: asset.ScopeRegion, NativeID: "cn-hangzhou", CreatedAt: now, UpdatedAt: now},
	} {
		if err := repositories.Inventory().PutScope(ctx, scope); err != nil {
			t.Fatal(err)
		}
	}
	value := planningAsset("instance-missing-shard", connection.ID, "ACS::ECS::Instance", "i-a", now)
	value.ScopeID = "region-h-missing-shard"
	if err := repositories.Inventory().PutAsset(ctx, value); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Graph().ReplaceGraph(ctx, "account-missing-shard", "graph-missing-shard", nil, nil); err != nil {
		t.Fatal(err)
	}
	finished := now.Add(time.Minute)
	run := asset.ScanRun{
		ID: "scan-missing-shard", ConnectionID: connection.ID, Status: asset.ScanSucceeded,
		ScopeMode: asset.ScanAllActiveRegions,
		Targets: []asset.ScanTarget{
			{Key: "region:cn-hangzhou", Kind: asset.ScanTargetRegion, RegionID: "cn-hangzhou"},
			{Key: "region:cn-beijing", Kind: asset.ScanTargetRegion, RegionID: "cn-beijing"},
		},
		CreatedAt: now, FinishedAt: &finished,
	}
	if err := repositories.Inventory().CreateScanRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Inventory().PutScanShard(ctx, asset.ScanShard{
		ID: "shard-h-missing-shard", ScanRunID: run.ID, Provider: asset.ProviderAliCloud,
		TargetKey: "region:cn-hangzhou", RegionID: "cn-hangzhou", ScopeID: "region-h-missing-shard",
		Status: asset.ShardSucceeded, Coverage: asset.Coverage{Complete: true},
		CreatedAt: now, FinishedAt: &finished,
	}); err != nil {
		t.Fatal(err)
	}
	service := cleanup.NewService(repositories, bundleResolver{
		asset.ProviderAliCloud: {Provider: asset.ProviderAliCloud, Revision: "bundle-a", Hash: "spec-a"},
	}, cleanup.WithTaskIDGenerator(func() string { return "cln-missing-shard" }))

	created, err := service.CreateTask(ctx, cleanup.CreateTaskRequest{
		Selectors: []plan.CleanupSelector{{Kind: plan.SelectorConnection, ConnectionID: connection.ID}},
		CreatedBy: "operator",
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.Task.Status != plan.StatusReady || created.Task.Coverage.Status != "incomplete" ||
		!containsTaskWarning(created.Task.Warnings, plan.WarningScanCoverageIncomplete, "") ||
		containsTaskBlocker(created.Task.Blockers, plan.BlockScanCoverageIncomplete, "") {
		t.Fatalf("declared target without shard warning missing: %+v", created.Task)
	}
}

func retiredAndGlobalCoverageFixture(t *testing.T) (persistence.Repositories, *cleanup.Service, map[string]plan.CleanupSelector) {
	t.Helper()
	ctx := context.Background()
	repositories := openPlanningRepositories(t)
	now := time.Date(2026, 7, 24, 16, 45, 0, 0, time.UTC)
	connection := asset.CloudConnection{
		ID: "connection-retired-global", Name: "retired and global", Provider: asset.ProviderAliCloud,
		Principal: "retired and global", Status: asset.ConnectionActive, CreatedAt: now, UpdatedAt: now,
	}
	if err := repositories.Connections().PutConnection(ctx, connection); err != nil {
		t.Fatal(err)
	}
	for _, region := range []asset.ConnectionRegion{
		{ID: "region-active", ConnectionID: connection.ID, RegionID: "cn-hangzhou", Lifecycle: asset.RegionActive, CreatedAt: now, UpdatedAt: now},
		{ID: "region-retired", ConnectionID: connection.ID, RegionID: "cn-shanghai", Lifecycle: asset.RegionRetired, CreatedAt: now, UpdatedAt: now},
	} {
		if err := repositories.Regions().PutRegion(ctx, region); err != nil {
			t.Fatal(err)
		}
	}
	for _, scope := range []asset.Scope{
		{ID: "account-retired-global", ConnectionID: connection.ID, Kind: asset.ScopeAccount, NativeID: "account", CreatedAt: now, UpdatedAt: now},
		{ID: "scope-active", ConnectionID: connection.ID, ParentID: "account-retired-global", Kind: asset.ScopeRegion, NativeID: "cn-hangzhou", CreatedAt: now, UpdatedAt: now},
		{ID: "scope-retired", ConnectionID: connection.ID, ParentID: "account-retired-global", Kind: asset.ScopeRegion, NativeID: "cn-shanghai", CreatedAt: now, UpdatedAt: now},
		{ID: "scope-global", ConnectionID: connection.ID, ParentID: "account-retired-global", Kind: asset.ScopeGlobal, NativeID: "global", CreatedAt: now, UpdatedAt: now},
	} {
		if err := repositories.Inventory().PutScope(ctx, scope); err != nil {
			t.Fatal(err)
		}
	}
	active := planningAsset("active-instance", connection.ID, "ACS::ECS::Instance", "i-active", now)
	active.ScopeID = "scope-active"
	retiredVPC := planningAsset("retired-vpc", connection.ID, "ACS::VPC::VPC", "vpc-retired", now)
	retiredVPC.ScopeID, retiredVPC.ResourceKindID = "scope-retired", "kind-vpc"
	retiredChild := planningAsset("retired-child", connection.ID, "ACS::ECS::Instance", "i-retired", now)
	retiredChild.ScopeID = "scope-retired"
	retiredChild.Normalized[topology.NormalizedVPCID] = "vpc-retired"
	global := planningAsset("global-role", connection.ID, "ACS::RAM::Role", "role-a", now)
	global.ScopeID = "scope-global"
	for _, value := range []asset.Asset{active, retiredVPC, retiredChild, global} {
		if err := repositories.Inventory().PutAsset(ctx, value); err != nil {
			t.Fatal(err)
		}
	}
	if err := repositories.Inventory().PutResourceKind(ctx, asset.ResourceKind{
		ID: "kind-vpc", Provider: asset.ProviderAliCloud, NativeType: "ACS::VPC::VPC", Class: "network.vpc",
	}); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Graph().ReplaceGraph(ctx, "account-retired-global", "graph-retired-global", nil, nil); err != nil {
		t.Fatal(err)
	}
	finished := now.Add(time.Minute)
	run := asset.ScanRun{
		ID: "scan-active-only", ConnectionID: connection.ID, Status: asset.ScanSucceeded,
		ScopeMode: asset.ScanAllActiveRegions,
		Targets:   []asset.ScanTarget{{Key: "region:cn-hangzhou", Kind: asset.ScanTargetRegion, RegionID: "cn-hangzhou"}},
		CreatedAt: now, FinishedAt: &finished,
	}
	if err := repositories.Inventory().CreateScanRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Inventory().PutScanShard(ctx, asset.ScanShard{
		ID: "shard-active-only", ScanRunID: run.ID, Provider: asset.ProviderAliCloud,
		TargetKey: "region:cn-hangzhou", RegionID: "cn-hangzhou", ScopeID: "scope-active",
		Status: asset.ShardSucceeded, Coverage: asset.Coverage{Complete: true},
		CreatedAt: now, FinishedAt: &finished,
	}); err != nil {
		t.Fatal(err)
	}
	planIndex := 0
	service := cleanup.NewService(repositories, bundleResolver{
		asset.ProviderAliCloud: {
			Provider: asset.ProviderAliCloud, Revision: "bundle-a", Hash: "spec-a",
			Specs: []spec.CompiledSpec{{ResourceKind: asset.ResourceKind{
				ID: "kind-vpc", Provider: asset.ProviderAliCloud, NativeType: "ACS::VPC::VPC", Class: "network.vpc",
			}}},
		},
	}, cleanup.WithTaskIDGenerator(func() string {
		planIndex++
		return fmt.Sprintf("cln-retired-global-%d", planIndex)
	}))
	return repositories, service, map[string]plan.CleanupSelector{
		"retired Region scope": {
			Kind: plan.SelectorScope, ConnectionID: connection.ID, ScopeID: "scope-retired", Descendants: true,
		},
		"retired Region VPC": {
			Kind: plan.SelectorGroup, ConnectionID: connection.ID,
			GroupKey: topology.VPCFocusKey("cn-shanghai", "vpc-retired"),
		},
		"connection includes retired": {
			Kind: plan.SelectorConnection, ConnectionID: connection.ID,
		},
		"global scope": {
			Kind: plan.SelectorScope, ConnectionID: connection.ID, ScopeID: "scope-global", Descendants: true,
		},
	}
}

func rangeCoverageFixture(t *testing.T, run asset.ScanRun) (persistence.Repositories, *cleanup.Service, []plan.CleanupSelector) {
	t.Helper()
	ctx := context.Background()
	repositories := openPlanningRepositories(t)
	now := time.Date(2026, 7, 24, 14, 0, 0, 0, time.UTC)
	connection := asset.CloudConnection{
		ID: "connection-range-coverage", Name: "production", Provider: asset.ProviderAliCloud,
		Principal: "production", Status: asset.ConnectionActive, CreatedAt: now, UpdatedAt: now,
	}
	if err := repositories.Connections().PutConnection(ctx, connection); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Regions().PutRegion(ctx, asset.ConnectionRegion{
		ID: "region-record", ConnectionID: connection.ID, RegionID: "cn-hangzhou",
		Lifecycle: asset.RegionActive, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	for _, scope := range []asset.Scope{
		{ID: "account-range-coverage", ConnectionID: connection.ID, Kind: asset.ScopeAccount, NativeID: "account", Name: "production", CreatedAt: now, UpdatedAt: now},
		{ID: "region-range-coverage", ConnectionID: connection.ID, ParentID: "account-range-coverage", Kind: asset.ScopeRegion, NativeID: "cn-hangzhou", Name: "Hangzhou", Location: "cn-hangzhou", CreatedAt: now, UpdatedAt: now},
	} {
		if err := repositories.Inventory().PutScope(ctx, scope); err != nil {
			t.Fatal(err)
		}
	}
	vpc := planningAsset("vpc-range-coverage", connection.ID, "ACS::VPC::VPC", "vpc-a", now)
	vpc.ScopeID, vpc.Location = "region-range-coverage", "cn-hangzhou"
	vpc.ResourceKindID = "kind-vpc"
	vpc.Normalized[topology.NormalizedVPCID] = "vpc-a"
	instance := planningAsset("instance-range-coverage", connection.ID, "ACS::ECS::Instance", "i-a", now)
	instance.ScopeID, instance.Location = "region-range-coverage", "cn-hangzhou"
	instance.ResourceKindID = "kind-instance"
	instance.Normalized[topology.NormalizedVPCID] = "vpc-a"
	for _, value := range []asset.Asset{vpc, instance} {
		if err := repositories.Inventory().PutAsset(ctx, value); err != nil {
			t.Fatal(err)
		}
	}
	if err := repositories.Graph().ReplaceGraph(ctx, "account-range-coverage", "graph-range-coverage", nil, nil); err != nil {
		t.Fatal(err)
	}
	finished := now.Add(time.Minute)
	run.ConnectionID, run.CreatedAt, run.FinishedAt = connection.ID, now, &finished
	if err := repositories.Inventory().CreateScanRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Inventory().PutScanShard(ctx, asset.ScanShard{
		ID: "shard-range-coverage", ScanRunID: run.ID, Provider: asset.ProviderAliCloud,
		TargetKey: run.Targets[0].Key, RegionID: "cn-hangzhou", ScopeID: "region-range-coverage",
		Status: asset.ShardSucceeded, Coverage: asset.Coverage{Complete: true},
		CreatedAt: now, FinishedAt: &finished,
	}); err != nil {
		t.Fatal(err)
	}
	planIndex := 0
	service := cleanup.NewService(repositories, bundleResolver{
		asset.ProviderAliCloud: {
			Provider: asset.ProviderAliCloud, Revision: "bundle-a", Hash: "spec-a",
			Specs: []spec.CompiledSpec{
				{ResourceKind: asset.ResourceKind{ID: "kind-vpc", Class: "network.vpc"}},
				{ResourceKind: asset.ResourceKind{ID: "kind-instance", Class: "compute.instance"}},
			},
		},
	}, cleanup.WithTaskIDGenerator(func() string {
		planIndex++
		return fmt.Sprintf("cln-range-coverage-%d", planIndex)
	}))
	return repositories, service, []plan.CleanupSelector{
		{Kind: plan.SelectorGroup, ConnectionID: connection.ID, GroupKey: topology.VPCFocusKey("cn-hangzhou", "vpc-a")},
		{Kind: plan.SelectorScope, ConnectionID: connection.ID, ScopeID: "region-range-coverage", Descendants: true},
		{Kind: plan.SelectorConnection, ConnectionID: connection.ID},
	}
}

func TestCreateTaskExpandsVPCFocusWithoutClientAssetList(t *testing.T) {
	ctx := context.Background()
	repositories := openPlanningRepositories(t)
	now := time.Date(2026, 7, 24, 10, 30, 0, 0, time.UTC)
	connection := asset.CloudConnection{ID: "connection-vpc", Name: "production", Provider: asset.ProviderAliCloud, Principal: "production", Status: asset.ConnectionActive, CreatedAt: now, UpdatedAt: now}
	if err := repositories.Connections().PutConnection(ctx, connection); err != nil {
		t.Fatal(err)
	}
	for _, scope := range []asset.Scope{
		{ID: "account-vpc", ConnectionID: connection.ID, Kind: asset.ScopeAccount, NativeID: "account", Name: "account", CreatedAt: now, UpdatedAt: now},
		{ID: "region-vpc", ConnectionID: connection.ID, ParentID: "account-vpc", Kind: asset.ScopeRegion, NativeID: "cn-hangzhou", Name: "Hangzhou", Location: "cn-hangzhou", CreatedAt: now, UpdatedAt: now},
	} {
		if err := repositories.Inventory().PutScope(ctx, scope); err != nil {
			t.Fatal(err)
		}
	}
	vpc := planningAsset("vpc-boundary", connection.ID, "ACS::VPC::VPC", "vpc-a", now)
	vpc.Name, vpc.ScopeID, vpc.Location = "Production VPC", "region-vpc", "cn-hangzhou"
	vpc.ResourceKindID = "kind-vpc"
	instance := planningAsset("instance-vpc", connection.ID, "ACS::ECS::Instance", "i-a", now)
	instance.ScopeID, instance.Location = "region-vpc", "cn-hangzhou"
	instance.ResourceKindID = "kind-instance"
	instance.Normalized[topology.NormalizedVPCID] = "vpc-a"
	disk := planningAsset("disk-vpc", connection.ID, "ACS::ECS::Disk", "d-a", now)
	disk.ScopeID, disk.Location = "region-vpc", "cn-hangzhou"
	disk.ResourceKindID = "kind-disk"
	other := planningAsset("instance-other", connection.ID, "ACS::ECS::Instance", "i-b", now)
	other.ScopeID, other.Location = "region-vpc", "cn-hangzhou"
	other.ResourceKindID = "kind-instance"
	other.Normalized[topology.NormalizedVPCID] = "vpc-b"
	emptyVPC := planningAsset("vpc-empty", connection.ID, "ACS::VPC::VPC", "vpc-empty", now)
	emptyVPC.Name, emptyVPC.ScopeID, emptyVPC.Location = "Empty VPC", "region-vpc", "cn-hangzhou"
	emptyVPC.ResourceKindID = "kind-vpc"
	for _, kind := range []asset.ResourceKind{
		{ID: "kind-vpc", Provider: asset.ProviderAliCloud, NativeType: "ACS::VPC::VPC", Class: "network.vpc"},
		{ID: "kind-instance", Provider: asset.ProviderAliCloud, NativeType: "ACS::ECS::Instance", Class: "compute.instance"},
		{ID: "kind-disk", Provider: asset.ProviderAliCloud, NativeType: "ACS::ECS::Disk", Class: "storage.block"},
	} {
		if err := repositories.Inventory().PutResourceKind(ctx, kind); err != nil {
			t.Fatal(err)
		}
	}
	for _, value := range []asset.Asset{vpc, instance, disk, other, emptyVPC} {
		if err := repositories.Inventory().PutAsset(ctx, value); err != nil {
			t.Fatal(err)
		}
	}
	if err := repositories.Graph().ReplaceGraph(ctx, "account-vpc", "graph-vpc", []graph.Relationship{{
		ID: "disk-attached-to-instance", SourceAssetID: disk.ID, TargetAssetID: instance.ID,
		Type: graph.RelationshipAttachedTo, Source: "spec", Confidence: 1,
		GraphRevision: "graph-vpc", ObservedAt: now,
	}}, nil); err != nil {
		t.Fatal(err)
	}
	seedCompleteScan(t, repositories, connection.ID, "account-vpc", now)
	planIndex := 0
	service := cleanup.NewService(repositories, bundleResolver{
		asset.ProviderAliCloud: {
			Provider: asset.ProviderAliCloud, Revision: "bundle-vpc", Hash: "spec-vpc",
			Specs: []spec.CompiledSpec{
				{ResourceKind: asset.ResourceKind{ID: "kind-vpc", Provider: asset.ProviderAliCloud, NativeType: "ACS::VPC::VPC", Class: "network.vpc"}},
				{ResourceKind: asset.ResourceKind{ID: "kind-instance", Provider: asset.ProviderAliCloud, NativeType: "ACS::ECS::Instance", Class: "compute.instance"}},
				{ResourceKind: asset.ResourceKind{ID: "kind-disk", Provider: asset.ProviderAliCloud, NativeType: "ACS::ECS::Disk", Class: "storage.block"}},
			},
		},
	}, cleanup.WithTaskIDGenerator(func() string {
		planIndex++
		return fmt.Sprintf("cln-vpc-%d", planIndex)
	}))
	selector := plan.CleanupSelector{
		Kind: plan.SelectorGroup, ConnectionID: connection.ID,
		GroupKey: topology.VPCFocusKey("cn-hangzhou", "vpc-a"),
	}
	created, err := service.CreateTask(ctx, cleanup.CreateTaskRequest{Selectors: []plan.CleanupSelector{selector}, CreatedBy: "operator"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(created.Task.ResolvedAssetIDs, []asset.AssetID{"disk-vpc", "instance-vpc", "vpc-boundary"}) {
		t.Fatalf("resolved IDs = %v", created.Task.ResolvedAssetIDs)
	}
	if containsTaskBlocker(created.Task.Blockers, plan.BlockCrossScopeDependency, instance.ID) {
		t.Fatalf("internal disk attachment produced cross-scope blocker: %+v", created.Task.Blockers)
	}
	if created.Task.Status != plan.StatusReady {
		t.Fatalf("VPC aggregate task stayed %s: blockers=%+v", created.Task.Status, created.Task.Blockers)
	}
	if len(created.Task.Selectors) != 1 || created.Task.Selectors[0].GroupKey != selector.GroupKey || created.Task.Selectors[0].DisplayName != "Production VPC" {
		t.Fatalf("selectors = %+v", created.Task.Selectors)
	}

	emptySelector := plan.CleanupSelector{
		Kind: plan.SelectorGroup, ConnectionID: connection.ID,
		GroupKey: topology.VPCFocusKey("cn-hangzhou", "vpc-empty"),
	}
	emptyCreated, err := service.CreateTask(ctx, cleanup.CreateTaskRequest{
		Selectors: []plan.CleanupSelector{emptySelector}, CreatedBy: "operator",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(emptyCreated.Task.ResolvedAssetIDs, []asset.AssetID{"vpc-empty"}) {
		t.Fatalf("empty VPC resolved IDs = %v", emptyCreated.Task.ResolvedAssetIDs)
	}
}

func TestCleanupServiceWarnsForIncompleteScanAndBlocksCrossScopeDependency(t *testing.T) {
	ctx := context.Background()
	repositories := openPlanningRepositories(t)
	now := time.Date(2026, 7, 13, 10, 45, 0, 0, time.UTC)
	connection := asset.CloudConnection{ID: "connection-blocked", Name: "blocked account", Provider: asset.ProviderAliCloud, Partition: "public", Principal: "blocked account", Status: asset.ConnectionActive, CreatedAt: now, UpdatedAt: now}
	if err := repositories.Connections().PutConnection(ctx, connection); err != nil {
		t.Fatal(err)
	}
	for _, scope := range []asset.Scope{
		{ID: "account-blocked", ConnectionID: connection.ID, Kind: asset.ScopeAccount, NativeID: "123", Name: "account", CreatedAt: now, UpdatedAt: now},
		{ID: "selected-region", ConnectionID: connection.ID, ParentID: "account-blocked", Kind: asset.ScopeRegion, NativeID: "cn-a", Name: "cn-a", CreatedAt: now, UpdatedAt: now},
		{ID: "other-region", ConnectionID: connection.ID, ParentID: "account-blocked", Kind: asset.ScopeRegion, NativeID: "cn-b", Name: "cn-b", CreatedAt: now, UpdatedAt: now},
	} {
		if err := repositories.Inventory().PutScope(ctx, scope); err != nil {
			t.Fatal(err)
		}
	}
	network := planningAsset("network-blocked", connection.ID, "ACS::VPC::VPC", "vpc-1", now)
	network.ScopeID = "selected-region"
	external := planningAsset("external-blocked", connection.ID, "ACS::ECS::Instance", "i-outside", now)
	external.ScopeID = "other-region"
	for _, value := range []asset.Asset{network, external} {
		if err := repositories.Inventory().PutAsset(ctx, value); err != nil {
			t.Fatal(err)
		}
	}
	relationship := graph.Relationship{ID: "outside-dependency", SourceAssetID: external.ID, TargetAssetID: network.ID, Type: graph.RelationshipDependsOn, Source: "spec", Confidence: 1, GraphRevision: "graph-blocked", ObservedAt: now}
	if err := repositories.Graph().ReplaceGraph(ctx, "account-blocked", "graph-blocked", []graph.Relationship{relationship}, nil); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Inventory().CreateScanRun(ctx, asset.ScanRun{ID: "failed-scan", ConnectionID: connection.ID, Status: asset.ScanFailed, CreatedAt: now, FinishedAt: &now}); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Inventory().PutScanShard(ctx, asset.ScanShard{ID: "failed-shard", ScanRunID: "failed-scan", Provider: asset.ProviderAliCloud, Source: "resource-center", ScopeID: "account-blocked", Status: asset.ShardFailed, Coverage: asset.Coverage{Complete: false, FailureReason: "denied"}, CreatedAt: now, FinishedAt: &now}); err != nil {
		t.Fatal(err)
	}
	service := cleanup.NewService(repositories, bundleResolver{asset.ProviderAliCloud: {Provider: asset.ProviderAliCloud, Revision: "bundle-a", Hash: "spec-a"}}, cleanup.WithTaskIDGenerator(func() string { return "cln-blocked" }))
	aggregate, err := service.CreateTask(ctx, cleanup.CreateTaskRequest{Selectors: []plan.CleanupSelector{{Kind: plan.SelectorScope, ConnectionID: connection.ID, ScopeID: "selected-region", Descendants: true}}, CreatedBy: "operator"})
	if err != nil {
		t.Fatal(err)
	}
	if aggregate.Task.Status != plan.StatusDraft ||
		!containsTaskWarning(aggregate.Task.Warnings, plan.WarningScanCoverageIncomplete, "") ||
		containsTaskBlocker(aggregate.Task.Blockers, plan.BlockScanCoverageIncomplete, "") ||
		!containsTaskBlocker(aggregate.Task.Blockers, plan.BlockCrossScopeDependency, network.ID) {
		t.Fatalf("plan = %+v", aggregate.Task)
	}
}

func TestCleanupServiceRequiresVpnConnectionBeforeCustomerGateway(t *testing.T) {
	ctx := context.Background()
	repositories := openPlanningRepositories(t)
	now := time.Date(2026, 8, 6, 11, 0, 0, 0, time.UTC)
	connectionID := asset.ConnectionID("connection-vpn")
	customerGateway := planningAsset(
		"customer-gateway",
		connectionID,
		"ACS::VPN::CustomerGateway",
		"cgw-a",
		now,
	)
	vpnConnection := planningAsset(
		"vpn-connection",
		connectionID,
		"ACS::VPN::VpnConnection",
		"vco-a",
		now,
	)
	relationship := graph.Relationship{
		ID:            "vpn-uses-customer-gateway",
		SourceAssetID: vpnConnection.ID,
		TargetAssetID: customerGateway.ID,
		Type:          graph.RelationshipUses,
		Source:        "spec",
		Confidence:    1,
		GraphRevision: "graph-vpn",
		ObservedAt:    now,
	}
	seedPlanningGraph(
		t,
		repositories,
		"scope-a",
		"graph-vpn",
		[]asset.Asset{customerGateway, vpnConnection},
		[]graph.Relationship{relationship},
		nil,
	)

	taskNumber := 0
	service := cleanup.NewService(
		repositories,
		bundleResolver{
			asset.ProviderAliCloud: {
				Provider: asset.ProviderAliCloud,
				Revision: "bundle-vpn",
				Hash:     "spec-vpn",
			},
		},
		cleanup.WithTaskIDGenerator(func() string {
			taskNumber++
			return fmt.Sprintf("cln-vpn-%d", taskNumber)
		}),
	)
	blocked, err := service.CreateTask(ctx, cleanup.CreateTaskRequest{
		Selectors: []plan.CleanupSelector{assetSelector(customerGateway.ID)},
		CreatedBy: "operator",
	})
	if err != nil {
		t.Fatal(err)
	}
	if blocked.Task.Status != plan.StatusDraft ||
		len(blocked.Task.Blockers) != 1 ||
		blocked.Task.Blockers[0].Code != plan.BlockCrossScopeDependency ||
		blocked.Task.Blockers[0].AssetID != customerGateway.ID ||
		blocked.Task.Blockers[0].Evidence["dependent_asset_id"] != vpnConnection.ID {
		t.Fatalf("customer-gateway-only task=%+v", blocked)
	}

	ready, err := service.CreateTask(ctx, cleanup.CreateTaskRequest{
		Selectors: []plan.CleanupSelector{
			assetSelector(customerGateway.ID),
			assetSelector(vpnConnection.ID),
		},
		CreatedBy: "operator",
	})
	if err != nil {
		t.Fatal(err)
	}
	if ready.Task.Status != plan.StatusReady ||
		len(ready.Task.Blockers) != 0 ||
		len(ready.Steps) != 2 ||
		ready.Steps[0].AssetID != vpnConnection.ID ||
		ready.Steps[1].AssetID != customerGateway.ID ||
		len(ready.Steps[1].DependsOn) != 1 ||
		ready.Steps[1].DependsOn[0] != ready.Steps[0].ID {
		t.Fatalf("customer gateway with VPN task=%+v", ready)
	}
}

func TestCleanupServiceRequiresEndpointServiceBeforeLoadBalancers(t *testing.T) {
	ctx := context.Background()
	repositories := openPlanningRepositories(t)
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	connectionID := asset.ConnectionID("connection-privatelink")
	endpointService := planningAsset(
		"endpoint-service",
		connectionID,
		"ACS::PrivateLink::VpcEndpointService",
		"epsrv-a",
		now,
	)
	alb := planningAsset(
		"load-balancer-alb",
		connectionID,
		"ACS::ALB::LoadBalancer",
		"alb-a",
		now,
	)
	nlb := planningAsset(
		"load-balancer-nlb",
		connectionID,
		"ACS::NLB::LoadBalancer",
		"nlb-a",
		now,
	)
	relationships := []graph.Relationship{
		{
			ID:            "endpoint-service-alb",
			SourceAssetID: endpointService.ID,
			TargetAssetID: alb.ID,
			Type:          graph.RelationshipDependsOn,
			Source:        "product_api",
			Confidence:    1,
			GraphRevision: "graph-privatelink",
			ObservedAt:    now,
		},
		{
			ID:            "endpoint-service-nlb",
			SourceAssetID: endpointService.ID,
			TargetAssetID: nlb.ID,
			Type:          graph.RelationshipDependsOn,
			Source:        "product_api",
			Confidence:    1,
			GraphRevision: "graph-privatelink",
			ObservedAt:    now,
		},
	}
	seedPlanningGraph(
		t,
		repositories,
		"scope-a",
		"graph-privatelink",
		[]asset.Asset{endpointService, alb, nlb},
		relationships,
		nil,
	)

	taskNumber := 0
	service := cleanup.NewService(
		repositories,
		bundleResolver{
			asset.ProviderAliCloud: {
				Provider: asset.ProviderAliCloud,
				Revision: "bundle-privatelink",
				Hash:     "spec-privatelink",
			},
		},
		cleanup.WithTaskIDGenerator(func() string {
			taskNumber++
			return fmt.Sprintf("cln-privatelink-%d", taskNumber)
		}),
	)

	for _, loadBalancer := range []asset.Asset{alb, nlb} {
		blocked, err := service.CreateTask(ctx, cleanup.CreateTaskRequest{
			Selectors: []plan.CleanupSelector{assetSelector(loadBalancer.ID)},
			CreatedBy: "operator",
		})
		if err != nil {
			t.Fatal(err)
		}
		if blocked.Task.Status != plan.StatusDraft ||
			len(blocked.Task.Blockers) != 1 ||
			blocked.Task.Blockers[0].Code != plan.BlockCrossScopeDependency ||
			blocked.Task.Blockers[0].AssetID != loadBalancer.ID ||
			blocked.Task.Blockers[0].Evidence["dependent_asset_id"] != endpointService.ID {
			t.Fatalf("%s-only task=%+v", loadBalancer.Identity.NativeType, blocked)
		}
	}

	ready, err := service.CreateTask(ctx, cleanup.CreateTaskRequest{
		Selectors: []plan.CleanupSelector{
			assetSelector(endpointService.ID),
			assetSelector(alb.ID),
			assetSelector(nlb.ID),
		},
		CreatedBy: "operator",
	})
	if err != nil {
		t.Fatal(err)
	}
	if ready.Task.Status != plan.StatusReady ||
		len(ready.Task.Blockers) != 0 ||
		len(ready.Steps) != 3 ||
		ready.Steps[0].AssetID != endpointService.ID {
		t.Fatalf("endpoint service with load balancers task=%+v", ready)
	}
	for _, step := range ready.Steps[1:] {
		if len(step.DependsOn) != 1 || step.DependsOn[0] != ready.Steps[0].ID {
			t.Fatalf("load balancer step does not wait for endpoint service: %+v", ready.Steps)
		}
	}
}

func TestCleanupServiceAppliesROSManagementAndNetworkDependencyClosure(t *testing.T) {
	ctx := context.Background()
	repositories := openPlanningRepositories(t)
	now := time.Date(2026, 8, 3, 9, 30, 0, 0, time.UTC)
	connectionID := asset.ConnectionID("connection-ros")
	stack := planningAsset("stack", connectionID, "ACS::ROS::Stack", "stack-1", now)
	stackInstance := planningAsset("stack-instance", connectionID, "ACS::ECS::Instance", "i-001", now)
	vswitch := planningAsset("stack-vswitch", connectionID, "ACS::VPC::VSwitch", "vsw-001", now)
	vpc := planningAsset("stack-vpc", connectionID, "ACS::VPC::VPC", "vpc-001", now)
	externalInstance := planningAsset("external-instance", connectionID, "ACS::ECS::Instance", "i-002", now)
	assets := []asset.Asset{stack, stackInstance, vswitch, vpc, externalInstance}
	bindings := []graph.LifecycleBinding{
		planningBinding("ros-instance", stack.ID, stackInstance.ID, graph.OwnershipExclusive, graph.CleanupDelegate, "graph-ros", now),
		planningBinding("ros-vswitch", stack.ID, vswitch.ID, graph.OwnershipExclusive, graph.CleanupDelegate, "graph-ros", now),
		planningBinding("ros-vpc", stack.ID, vpc.ID, graph.OwnershipExclusive, graph.CleanupDelegate, "graph-ros", now),
	}
	for index := range bindings {
		bindings[index].DirectCleanupAllowed = true
		bindings[index].EvidenceSource = "ros:system-tag"
	}
	relationships := []graph.Relationship{
		{ID: "stack-instance-vswitch", SourceAssetID: stackInstance.ID, TargetAssetID: vswitch.ID, Type: graph.RelationshipDependsOn, Source: "product_api", Confidence: 1, GraphRevision: "graph-ros", ObservedAt: now},
		{ID: "stack-instance-vpc", SourceAssetID: stackInstance.ID, TargetAssetID: vpc.ID, Type: graph.RelationshipDependsOn, Source: "product_api", Confidence: 1, GraphRevision: "graph-ros", ObservedAt: now},
		{ID: "external-instance-vswitch", SourceAssetID: externalInstance.ID, TargetAssetID: vswitch.ID, Type: graph.RelationshipDependsOn, Source: "product_api", Confidence: 1, GraphRevision: "graph-ros", ObservedAt: now},
		{ID: "external-instance-vpc", SourceAssetID: externalInstance.ID, TargetAssetID: vpc.ID, Type: graph.RelationshipDependsOn, Source: "product_api", Confidence: 1, GraphRevision: "graph-ros", ObservedAt: now},
		{ID: "vswitch-vpc", SourceAssetID: vswitch.ID, TargetAssetID: vpc.ID, Type: graph.RelationshipMemberOf, Source: "product_api", Confidence: 1, GraphRevision: "graph-ros", ObservedAt: now},
	}
	seedPlanningSnapshot(t, repositories, "scope-a", "graph-ros", assets, nil)
	if err := repositories.Graph().ReplaceGraph(ctx, "scope-a", "graph-ros", relationships, bindings); err != nil {
		t.Fatal(err)
	}
	planNumber := 0
	service := cleanup.NewService(repositories, bundleResolver{
		asset.ProviderAliCloud: {Provider: asset.ProviderAliCloud, Revision: "bundle-ros", Hash: "spec-ros"},
	}, cleanup.WithTaskIDGenerator(func() string {
		planNumber++
		return fmt.Sprintf("cln-ros-%d", planNumber)
	}))

	direct, err := service.CreateTask(ctx, cleanup.CreateTaskRequest{
		Selectors: []plan.CleanupSelector{assetSelector(stackInstance.ID)}, CreatedBy: "operator",
	})
	if err != nil {
		t.Fatal(err)
	}
	if direct.Task.Status != plan.StatusReady || len(direct.Task.Blockers) != 0 ||
		len(direct.Task.Warnings) != 1 ||
		direct.Task.Warnings[0].Code != plan.WarningManagedResourceDirectCleanup ||
		len(direct.Steps) != 1 || direct.Steps[0].AssetID != stackInstance.ID {
		t.Fatalf("direct managed-resource task = %+v", direct)
	}

	blocked, err := service.CreateTask(ctx, cleanup.CreateTaskRequest{
		Selectors: []plan.CleanupSelector{assetSelector(stack.ID)}, CreatedBy: "operator",
	})
	if err != nil {
		t.Fatal(err)
	}
	dependencyBlockers := 0
	for _, blocker := range blocked.Task.Blockers {
		if blocker.Code != plan.BlockCrossScopeDependency {
			continue
		}
		if blocker.Evidence["dependent_asset_id"] != externalInstance.ID {
			t.Fatalf("dependency blocker = %+v", blocker)
		}
		dependencyBlockers++
	}
	if blocked.Task.Status != plan.StatusDraft || dependencyBlockers != 2 ||
		len(blocked.Steps) != 4 ||
		cleanupStepForAsset(blocked.Steps, stack.ID).Kind != plan.StepController {
		t.Fatalf("stack-only task = %+v", blocked)
	}

	ready, err := service.CreateTask(ctx, cleanup.CreateTaskRequest{
		Selectors: []plan.CleanupSelector{assetSelector(stack.ID), assetSelector(externalInstance.ID)},
		CreatedBy: "operator",
	})
	if err != nil {
		t.Fatal(err)
	}
	if ready.Task.Status != plan.StatusReady || len(ready.Task.Blockers) != 0 || len(ready.Steps) != 5 {
		t.Fatalf("complete dependency task = %+v", ready)
	}
	externalStep := cleanupStepForAsset(ready.Steps, externalInstance.ID)
	stackStep := cleanupStepForAsset(ready.Steps, stack.ID)
	if len(stackStep.DependsOn) != 1 ||
		stackStep.DependsOn[0] != externalStep.ID {
		t.Fatalf("complete dependency steps = %+v", ready.Steps)
	}
	for _, managedID := range []asset.AssetID{
		stackInstance.ID,
		vpc.ID,
		vswitch.ID,
	} {
		verificationStep := cleanupStepForAsset(ready.Steps, managedID)
		if verificationStep.Kind != plan.StepVerification ||
			len(verificationStep.DependsOn) != 1 ||
			verificationStep.DependsOn[0] != stackStep.ID {
			t.Fatalf("managed verification step = %+v", verificationStep)
		}
	}
}

func cleanupStepForAsset(
	steps []plan.CleanupTaskStep,
	id asset.AssetID,
) plan.CleanupTaskStep {
	for _, step := range steps {
		if step.AssetID == id {
			return step
		}
	}
	return plan.CleanupTaskStep{}
}

func seedCompleteScan(t *testing.T, repositories persistence.Repositories, connectionID asset.ConnectionID, scopeID asset.ScopeID, now time.Time) {
	t.Helper()
	finished := now.Add(time.Minute)
	scopes, err := repositories.Inventory().ListScopesByConnection(context.Background(), connectionID)
	if err != nil {
		t.Fatal(err)
	}
	regions, err := repositories.Regions().ListRegionsByConnection(context.Background(), connectionID)
	if err != nil {
		t.Fatal(err)
	}
	type targetScope struct {
		target asset.ScanTarget
		scope  asset.ScopeID
	}
	byIdentity := make(map[string]targetScope)
	for _, scope := range scopes {
		switch scope.Kind {
		case asset.ScopeRegion:
			regionID := scope.NativeID
			if regionID == "" {
				regionID = scope.Location
			}
			if regionID != "" {
				byIdentity["region:"+regionID] = targetScope{
					target: asset.ScanTarget{Key: "region:" + regionID, Kind: asset.ScanTargetRegion, RegionID: regionID},
					scope:  scope.ID,
				}
			}
		case asset.ScopeGlobal:
			byIdentity["global"] = targetScope{
				target: asset.ScanTarget{Key: "global", Kind: asset.ScanTargetGlobal, RegionID: "global"},
				scope:  scope.ID,
			}
		}
	}
	for _, region := range regions {
		if region.Lifecycle != asset.RegionActive || region.RegionID == "" {
			continue
		}
		identity := "region:" + region.RegionID
		if _, exists := byIdentity[identity]; !exists {
			byIdentity[identity] = targetScope{
				target: asset.ScanTarget{Key: identity, Kind: asset.ScanTargetRegion, RegionID: region.RegionID},
				scope:  scopeID,
			}
		}
	}
	identities := make([]string, 0, len(byIdentity))
	for identity := range byIdentity {
		identities = append(identities, identity)
	}
	sort.Strings(identities)
	if len(identities) == 0 {
		t.Fatal("complete scan fixture requires at least one authoritative Region or global target")
	}
	run := asset.ScanRun{
		ID: asset.ScanRunID("scan-" + string(connectionID)), ConnectionID: connectionID,
		Status: asset.ScanSucceeded, ScopeMode: asset.ScanAllActiveRegions,
		CreatedAt: now, FinishedAt: &finished,
	}
	for _, identity := range identities {
		run.Targets = append(run.Targets, byIdentity[identity].target)
	}
	if err := repositories.Inventory().CreateScanRun(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	for index, identity := range identities {
		value := byIdentity[identity]
		shard := asset.ScanShard{
			ID:        asset.ScanShardID(fmt.Sprintf("shard-%s-%d", connectionID, index)),
			ScanRunID: run.ID, Provider: asset.ProviderAliCloud, Source: "resource-center",
			TargetKey: value.target.Key, RegionID: value.target.RegionID, ScopeID: value.scope,
			Status: asset.ShardSucceeded, Coverage: asset.Coverage{Complete: true, FreshAt: finished},
			CreatedAt: now, FinishedAt: &finished,
		}
		if err := repositories.Inventory().PutScanShard(context.Background(), shard); err != nil {
			t.Fatal(err)
		}
	}
}

type bundleResolver map[asset.Provider]spec.Bundle

func (r bundleResolver) Bundle(provider asset.Provider) (spec.Bundle, error) {
	bundle, ok := r[provider]
	if !ok {
		return spec.Bundle{}, fmt.Errorf("bundle for %s not found", provider)
	}
	return bundle, nil
}

func (r bundleResolver) ProviderDescriptors() []contracts.ProviderDescriptor {
	result := make([]contracts.ProviderDescriptor, 0, len(r))
	for provider := range r {
		result = append(result, contracts.ProviderDescriptor{
			Provider: provider,
			InventorySources: []contracts.InventorySource{{
				Name: "test-index", RootScopeKinds: []asset.ScopeKind{asset.ScopeRegion},
			}},
		})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Provider < result[j].Provider })
	return result
}

type descriptorBundleResolver struct {
	bundleResolver
	descriptors []contracts.ProviderDescriptor
}

func (r descriptorBundleResolver) ProviderDescriptors() []contracts.ProviderDescriptor {
	return append([]contracts.ProviderDescriptor(nil), r.descriptors...)
}

func openPlanningRepositories(t *testing.T) persistence.Repositories {
	t.Helper()
	repositories, err := sqlite.Open(filepath.Join(t.TempDir(), "planning.db"), filepath.Join("..", "..", "..", "migrations"))
	if err != nil {
		t.Fatal(err)
	}
	return repositories
}

func seedPlanningSnapshot(t *testing.T, repositories persistence.Repositories, scopeID asset.ScopeID, revision string, assets []asset.Asset, bindings []graph.LifecycleBinding) {
	seedPlanningGraph(t, repositories, scopeID, revision, assets, nil, bindings)
}

func seedPlanningGraph(
	t *testing.T,
	repositories persistence.Repositories,
	scopeID asset.ScopeID,
	revision string,
	assets []asset.Asset,
	relationships []graph.Relationship,
	bindings []graph.LifecycleBinding,
) {
	t.Helper()
	ctx := context.Background()
	connectionID := asset.ConnectionID("planning-connection")
	provider := asset.ProviderAliCloud
	if len(assets) > 0 {
		connectionID = assets[0].Identity.ConnectionID
		provider = assets[0].Identity.Provider
	}
	if _, err := repositories.Connections().GetConnection(ctx, connectionID); errors.Is(err, persistence.ErrNotFound) {
		if err := repositories.Connections().PutConnection(ctx, asset.CloudConnection{ID: connectionID, Name: string(connectionID), Provider: provider, Partition: "public", Principal: string(connectionID), Status: asset.ConnectionActive, CreatedAt: time.Now(), UpdatedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
	} else if err != nil {
		t.Fatal(err)
	}
	if _, err := repositories.Inventory().GetScope(ctx, scopeID); errors.Is(err, persistence.ErrNotFound) {
		if err := repositories.Inventory().PutScope(ctx, asset.Scope{
			ID: scopeID, ConnectionID: connectionID, Kind: asset.ScopeRegion,
			NativeID: string(scopeID), Name: string(scopeID), CreatedAt: time.Now(), UpdatedAt: time.Now(),
		}); err != nil {
			t.Fatal(err)
		}
	} else if err != nil {
		t.Fatal(err)
	}
	for _, value := range assets {
		if err := repositories.Inventory().PutAsset(ctx, value); err != nil {
			t.Fatal(err)
		}
	}
	if err := repositories.Graph().ReplaceGraph(ctx, scopeID, revision, relationships, bindings); err != nil {
		t.Fatal(err)
	}
}

func planningAsset(id asset.AssetID, connection asset.ConnectionID, nativeType, nativeID string, observedAt time.Time) asset.Asset {
	return asset.Asset{
		ID: id, Identity: asset.Identity{Provider: asset.ProviderAliCloud, Partition: "public", ConnectionID: connection, NativeType: nativeType, NativeID: nativeID},
		ScopeID: "scope-a", ResourceKindID: asset.ResourceKindID("kind-" + string(id)), CurrentObservationID: asset.ObservationID("observation-" + string(id)),
		Name: nativeID, State: "running", Capabilities: asset.CapabilitySet{asset.CapabilityIndexed, asset.CapabilityActionable},
		Normalized: map[string]any{"id": nativeID}, FirstSeenAt: observedAt, LastSeenAt: observedAt,
	}
}

func planningBinding(id graph.LifecycleBindingID, controller, managed asset.AssetID, ownership graph.Ownership, cleanupPolicy graph.CleanupPolicy, revision string, observedAt time.Time) graph.LifecycleBinding {
	return graph.LifecycleBinding{
		ID: id, ControllerAssetID: controller, ManagedAssetID: managed, Authority: graph.AuthorityAuthoritative,
		Ownership: ownership, CleanupPolicy: cleanupPolicy, EvidenceSource: "provider", Evidence: map[string]any{"delete_by_default": true},
		Confidence: 1, GraphRevision: revision, ObservedAt: observedAt,
	}
}

func containsTaskBlocker(values []plan.Blocker, code plan.BlockCode, assetID asset.AssetID) bool {
	for _, value := range values {
		if value.Code == code && value.AssetID == assetID {
			return true
		}
	}
	return false
}

func containsTaskWarning(values []plan.Warning, code plan.WarningCode, assetID asset.AssetID) bool {
	for _, value := range values {
		if value.Code == code && value.AssetID == assetID {
			return true
		}
	}
	return false
}

func assetSelector(id asset.AssetID) plan.CleanupSelector {
	return plan.CleanupSelector{Kind: plan.SelectorAsset, AssetID: id}
}
