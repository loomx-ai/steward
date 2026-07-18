package governance_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/loomx-ai/steward/internal/app/governance"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/core/finding"
	"github.com/loomx-ai/steward/internal/persistence"
	"github.com/loomx-ai/steward/internal/persistence/sqlite"
	"github.com/loomx-ai/steward/internal/provider/spec"
)

type bundleDirectory struct{ bundle spec.Bundle }

func (d bundleDirectory) Bundle(asset.Provider) (spec.Bundle, error) { return d.bundle, nil }

type contributorResolver struct {
	assets []asset.Asset
	err    error
}

func (r *contributorResolver) ResolveContributors(_ context.Context, _ asset.CloudConnection, values []asset.Asset) ([]governance.Contributor, error) {
	r.assets = append([]asset.Asset(nil), values...)
	return nil, r.err
}

func TestGraphHandlerRebuildsOneConnectionGraphAcrossScopes(t *testing.T) {
	ctx := context.Background()
	repositories, err := sqlite.Open(filepath.Join(t.TempDir(), "graph-worker.db"), filepath.Join("..", "..", "..", "migrations"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 13, 9, 0, 0, 0, time.UTC)
	seedGraphWorker(t, repositories, now)
	bundle := spec.Bundle{Provider: asset.ProviderAliCloud, Revision: "bundle-graph", Specs: []spec.CompiledSpec{{
		Definition: spec.ResourceKindSpec{
			Metadata:      assetMetadata(asset.ProviderAliCloud, "ACS::ECS::Instance"),
			Relationships: []spec.RelationshipSpec{{Type: "member_of", TargetType: "ACS::VPC::VPC", TargetIDPath: "vpcId"}},
		},
		ResourceKind: asset.ResourceKind{ID: "kind-instance", Provider: asset.ProviderAliCloud, NativeType: "ACS::ECS::Instance", BundleRevision: "bundle-graph"},
		Rules:        []spec.CompiledRule{{ID: "stopped-instance", Title: "Stopped instance", Severity: finding.SeverityHigh, FieldPath: []string{"state"}, Operator: spec.RuleEqual, Expected: "Stopped"}},
	}}}
	resolver := &contributorResolver{}
	handler := governance.NewGraphHandler(repositories, bundleDirectory{bundle: bundle}, resolver)
	if err := handler.Handle(ctx, execution.Job{ID: "graph-run-graph", Type: execution.JobGraph, Payload: map[string]any{"scan_run_id": "run-graph"}}); err != nil {
		t.Fatal(err)
	}
	run, err := repositories.Inventory().GetScanRun(ctx, "run-graph")
	if err != nil || run.Status != asset.ScanSucceeded || run.CompletionStatus != "" || run.FinishedAt == nil {
		t.Fatalf("scan run was not finalized after graph rebuild: run=%+v err=%v", run, err)
	}
	if len(resolver.assets) != 3 {
		t.Fatalf("contributor resolver received assets = %+v", resolver.assets)
	}
	revision, err := repositories.Graph().GetGraphRevision(ctx, "scope-root")
	if err != nil || revision != "run-graph" {
		t.Fatalf("graph revision = %q, err = %v", revision, err)
	}
	relationships, err := repositories.Graph().ListRelationships(ctx, "asset-instance")
	if err != nil || len(relationships) != 1 || relationships[0].TargetAssetID != "asset-vpc" {
		t.Fatalf("relationships = %+v, err = %v", relationships, err)
	}
	findings, err := repositories.Findings().ListFindingsByAsset(ctx, "asset-outside-scan")
	if err != nil || len(findings) != 1 || findings[0].Status != finding.StatusOpen || !findings[0].LastSeenAt.Equal(now) {
		t.Fatalf("out-of-scope finding was closed by regional coverage: %+v, err = %v", findings, err)
	}
	findings, err = repositories.Findings().ListFindingsByAsset(ctx, "asset-instance")
	if err != nil || len(findings) != 1 || findings[0].Status != finding.StatusClosed || findings[0].ClosedAt == nil {
		t.Fatalf("broad authoritative coverage did not close in-scope finding: %+v, err = %v", findings, err)
	}
}

func TestGraphHandlerKeepsCompletionStatusWhenReconciliationFails(t *testing.T) {
	ctx := context.Background()
	repositories, err := sqlite.Open(filepath.Join(t.TempDir(), "graph-worker-failure.db"), filepath.Join("..", "..", "..", "migrations"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 5, 10, 0, 0, 0, time.UTC)
	seedGraphWorker(t, repositories, now)
	reconciliationErr := errors.New("relationship contributor failed")
	handler := governance.NewGraphHandler(
		repositories,
		bundleDirectory{bundle: spec.Bundle{Provider: asset.ProviderAliCloud, Revision: "bundle-graph"}},
		&contributorResolver{err: reconciliationErr},
	)

	err = handler.Handle(ctx, execution.Job{
		ID: "graph-run-graph", Type: execution.JobGraph,
		Payload: map[string]any{"scan_run_id": "run-graph"},
	})
	if !errors.Is(err, reconciliationErr) {
		t.Fatalf("Handle() error = %v, want reconciliation failure", err)
	}
	run, getErr := repositories.Inventory().GetScanRun(ctx, "run-graph")
	if getErr != nil || run.Status != asset.ScanFailed ||
		run.CompletionStatus != asset.ScanSucceeded || run.FinishedAt == nil {
		t.Fatalf("failed scan run = %+v, err = %v", run, getErr)
	}
}

func assetMetadata(provider asset.Provider, nativeType string) spec.Metadata {
	return spec.Metadata{Provider: provider, NativeType: nativeType}
}

func seedGraphWorker(t *testing.T, repositories persistence.Repositories, now time.Time) {
	t.Helper()
	ctx := context.Background()
	finishedAt := now.Add(time.Minute)
	writes := []func() error{
		func() error {
			return repositories.Connections().PutConnection(ctx, asset.CloudConnection{ID: "connection-graph", Name: "scanner", Provider: asset.ProviderAliCloud, Partition: "public", Principal: "scanner", Status: asset.ConnectionActive, CreatedAt: now, UpdatedAt: now})
		},
		func() error {
			return repositories.Inventory().PutScope(ctx, asset.Scope{ID: "scope-root", ConnectionID: "connection-graph", Kind: asset.ScopeAccount, NativeID: "account", Name: "account", CreatedAt: now, UpdatedAt: now})
		},
		func() error {
			return repositories.Inventory().PutScope(ctx, asset.Scope{ID: "scope-region", ConnectionID: "connection-graph", ParentID: "scope-root", Kind: asset.ScopeRegion, NativeID: "cn-hangzhou", Name: "cn-hangzhou", Location: "cn-hangzhou", CreatedAt: now, UpdatedAt: now})
		},
		func() error {
			return repositories.Inventory().PutScope(ctx, asset.Scope{ID: "scope-outside", ConnectionID: "connection-graph", ParentID: "scope-root", Kind: asset.ScopeRegion, NativeID: "cn-shanghai", Name: "cn-shanghai", Location: "cn-shanghai", CreatedAt: now, UpdatedAt: now})
		},
		func() error {
			return repositories.Inventory().CreateScanRun(ctx, asset.ScanRun{
				ID: "run-graph", ConnectionID: "connection-graph",
				Status: asset.ScanReconciling, CompletionStatus: asset.ScanSucceeded,
				RequestedBy: "tester", CreatedAt: now, StartedAt: &now, FinishedAt: &finishedAt,
			})
		},
		func() error {
			return repositories.Inventory().PutScanShard(ctx, asset.ScanShard{ID: "shard-graph", ScanRunID: "run-graph", Provider: asset.ProviderAliCloud, Source: "resource-center", ScopeID: "scope-region", Authoritative: true, Status: asset.ShardSucceeded, Coverage: asset.Coverage{ScopeID: "scope-region", Authoritative: true, Complete: true, FreshAt: finishedAt}, CreatedAt: now, StartedAt: &now, FinishedAt: &finishedAt})
		},
		func() error {
			return repositories.Inventory().PutAsset(ctx, asset.Asset{ID: "asset-instance", Identity: asset.Identity{Provider: asset.ProviderAliCloud, Partition: "public", ConnectionID: "connection-graph", NativeType: "ACS::ECS::Instance", NativeID: "i-1"}, ScopeID: "scope-region", ResourceKindID: "kind-instance", CurrentObservationID: "observation-instance", Normalized: map[string]any{"vpcId": "vpc-1", "state": "Running"}, FirstSeenAt: now, LastSeenAt: now})
		},
		func() error {
			return repositories.Inventory().AppendObservation(ctx, asset.Observation{ID: "observation-instance", AssetID: "asset-instance", ScanRunID: "run-graph", ScanShardID: "shard-graph", ObservedAt: finishedAt, Source: "resource-center", SchemaRevision: "bundle-graph", Normalized: map[string]any{"state": "Running"}, Authoritative: true})
		},
		func() error {
			return repositories.Inventory().PutAsset(ctx, asset.Asset{ID: "asset-vpc", Identity: asset.Identity{Provider: asset.ProviderAliCloud, Partition: "public", ConnectionID: "connection-graph", NativeType: "ACS::VPC::VPC", NativeID: "vpc-1"}, ScopeID: "scope-root", ResourceKindID: "kind-vpc", FirstSeenAt: now, LastSeenAt: now})
		},
		func() error {
			return repositories.Inventory().PutAsset(ctx, asset.Asset{ID: "asset-outside-scan", Identity: asset.Identity{Provider: asset.ProviderAliCloud, Partition: "public", ConnectionID: "connection-graph", NativeType: "ACS::ECS::Instance", NativeID: "i-outside"}, ScopeID: "scope-outside", ResourceKindID: "kind-instance", Normalized: map[string]any{"state": "Stopped"}, FirstSeenAt: now, LastSeenAt: now})
		},
		func() error {
			return repositories.Findings().PutFinding(ctx, finding.Finding{ID: "finding-instance", AssetID: "asset-instance", RuleID: "stopped-instance", Status: finding.StatusOpen, Severity: finding.SeverityHigh, FirstSeenAt: now, LastSeenAt: now, Evidence: map[string]any{"engine": "spec-governance"}})
		},
		func() error {
			return repositories.Findings().PutFinding(ctx, finding.Finding{ID: "finding-outside", AssetID: "asset-outside-scan", RuleID: "stopped-instance", Status: finding.StatusOpen, Severity: finding.SeverityHigh, FirstSeenAt: now, LastSeenAt: now, Evidence: map[string]any{"engine": "spec-governance"}})
		},
	}
	for _, write := range writes {
		if err := write(); err != nil {
			t.Fatal(err)
		}
	}
}
