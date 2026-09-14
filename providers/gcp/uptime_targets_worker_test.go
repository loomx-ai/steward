package gcp

import (
	"context"
	"slices"
	"testing"

	"github.com/loomx-ai/steward/internal/app/cleanup"
	"github.com/loomx-ai/steward/internal/app/governance"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/core/graph"
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/internal/persistence"
)

type uptimeTargetContributors struct{}

func (uptimeTargetContributors) ResolveContributors(_ context.Context, _ asset.CloudConnection, _ []asset.Asset) ([]governance.Contributor, error) {
	return []governance.Contributor{NewUptimeTargets()}, nil
}

func assertUptimeSQLiteTargetGraph(t *testing.T, repos persistence.Repositories, r *Runtime, source asset.Asset) {
	t.Helper()
	ctx := t.Context()
	kind := r.resourceKind(instanceType)
	target := asset.Asset{ID: "uptime-target", ScopeID: "uptime-target-region", ResourceKindID: kind.ID, Identity: source.Identity, Name: "server", Normalized: map[string]any{"id": "87654321", "name": "server"}, Capabilities: source.Capabilities, FirstSeenAt: source.FirstSeenAt, LastSeenAt: source.LastSeenAt}
	target.Identity.NativeType = instanceType
	target.Identity.NativeID = uptimeVM
	target.Identity.ScopeKey = "region:us-central1"
	for _, write := range []func() error{
		func() error {
			return repos.Inventory().PutScope(ctx, asset.Scope{ID: target.ScopeID, ConnectionID: source.Identity.ConnectionID, Kind: asset.ScopeRegion, NativeID: "us-central1", CreatedAt: source.FirstSeenAt, UpdatedAt: source.LastSeenAt})
		},
		func() error { return repos.Inventory().PutResourceKind(ctx, kind) },
		func() error { return repos.Inventory().PutAsset(ctx, target) },
	} {
		if err := write(); err != nil {
			t.Fatal(err)
		}
	}
	handler := governance.NewGraphHandler(repos, identityRegistry(t, r), uptimeTargetContributors{})
	if err := handler.Handle(ctx, execution.Job{Type: execution.JobGraph, Payload: map[string]any{"scan_run_id": "recovered"}}); err != nil {
		t.Fatal(err)
	}
	relations, err := repos.Graph().ListRelationshipsForAsset(ctx, source.Identity.ConnectionID, source.ID)
	if err != nil || len(relations) != 1 || relations[0].TargetAssetID != target.ID || relations[0].Type != graph.RelationshipDependsOn || relations[0].Source != "gcp:uptime-targets" {
		t.Fatal(relations, err)
	}
	planner := cleanup.NewService(repos, identityRegistry(t, r))
	task, err := planner.CreateTask(ctx, cleanup.CreateTaskRequest{ConnectionID: source.Identity.ConnectionID, CreatedBy: "test", Selectors: []plan.CleanupSelector{{Kind: plan.SelectorAsset, AssetID: source.ID}, {Kind: plan.SelectorAsset, AssetID: target.ID}}})
	if err != nil || len(task.Task.Blockers) != 0 || len(task.Steps) != 2 || len(task.ImpactItems) != 0 || task.Steps[0].AssetID != source.ID || task.Steps[1].AssetID != target.ID || !slices.Contains(task.Steps[1].DependsOn, task.Steps[0].ID) {
		t.Fatal(task, err)
	}
	persisted, err := repos.CleanupTasks().GetTask(ctx, task.Task.ID)
	if err != nil || len(persisted.Steps) != 2 || !slices.Contains(persisted.Steps[1].DependsOn, persisted.Steps[0].ID) {
		t.Fatal(persisted, err)
	}
}
