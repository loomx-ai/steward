package scancoverage_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/loomx-ai/steward/internal/app/scancoverage"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/persistence"
	"github.com/loomx-ai/steward/internal/persistence/sqlite"
)

func TestEvaluateConnectionBoundsReadsAtFirstVerifiedCompleteRun(t *testing.T) {
	t.Parallel()

	repositories := openCoverageRepositories(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 24, 14, 0, 0, 0, time.UTC)
	active := []asset.ConnectionRegion{{
		ID: "region-a", ConnectionID: "connection-a", RegionID: "cn-hangzhou",
		Lifecycle: asset.RegionActive,
	}}
	putCompleteCoverageRun(t, repositories.Inventory(), asset.ScanRun{
		ID: "scan-new", ConnectionID: "connection-a", Status: asset.ScanSucceeded,
		ScopeMode: asset.ScanAllActiveRegions,
		Targets:   []asset.ScanTarget{{Key: "region:cn-hangzhou", Kind: asset.ScanTargetRegion, RegionID: "cn-hangzhou"}},
		CreatedAt: now, FinishedAt: timePointer(now),
	}, "shard-new")
	older := now.Add(-time.Hour)
	putCompleteCoverageRun(t, repositories.Inventory(), asset.ScanRun{
		ID: "scan-old", ConnectionID: "connection-a", Status: asset.ScanSucceeded,
		ScopeMode: asset.ScanAllActiveRegions,
		Targets:   []asset.ScanTarget{{Key: "region:cn-hangzhou", Kind: asset.ScanTargetRegion, RegionID: "cn-hangzhou"}},
		CreatedAt: older, FinishedAt: timePointer(older),
	}, "shard-old")
	spy := &coverageInventorySpy{InventoryRepository: repositories.Inventory()}

	summary, err := scancoverage.EvaluateConnection(
		ctx, spy, "connection-a", scancoverage.ActiveRegionRequirement(active, "connection-a"),
	)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Status != "complete" || summary.LastCompleteScanAt == nil || !summary.LastCompleteScanAt.Equal(now) {
		t.Fatalf("summary = %+v", summary)
	}
	if len(spy.pageOptions) != 1 || spy.pageOptions[0].ConnectionID != "connection-a" || spy.pageOptions[0].Limit != scancoverage.CandidateLimit ||
		spy.byConnectionCalls != 0 || len(spy.shardRunIDs) != 1 || spy.shardRunIDs[0] != "scan-new" {
		t.Fatalf("coverage reads: pages=%+v by_connection=%d shards=%v", spy.pageOptions, spy.byConnectionCalls, spy.shardRunIDs)
	}
}

func TestEvaluateConnectionSkipsShardReadsForMetadataDisqualifiedRuns(t *testing.T) {
	t.Parallel()

	repositories := openCoverageRepositories(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 24, 14, 0, 0, 0, time.UTC)
	for _, run := range []asset.ScanRun{
		{
			ID: "scan-network", ConnectionID: "connection-a", Status: asset.ScanSucceeded,
			ScopeMode: asset.ScanSelectedNetworks,
			Targets:   []asset.ScanTarget{{Key: "vpc:a", Kind: asset.ScanTargetVPC, RegionID: "cn-hangzhou", NativeID: "vpc-a"}},
			CreatedAt: now, FinishedAt: timePointer(now),
		},
		{
			ID: "scan-filtered", ConnectionID: "connection-a", Status: asset.ScanSucceeded,
			ScopeMode:       asset.ScanAllActiveRegions,
			Targets:         []asset.ScanTarget{{Key: "region:cn-hangzhou", Kind: asset.ScanTargetRegion, RegionID: "cn-hangzhou"}},
			ResourceKindIDs: []asset.ResourceKindID{"alicloud:ACS::ECS::Instance"},
			CreatedAt:       now.Add(-time.Minute), FinishedAt: timePointer(now.Add(-time.Minute)),
		},
		{
			ID: "scan-missing-active-region", ConnectionID: "connection-a", Status: asset.ScanSucceeded,
			ScopeMode: asset.ScanAllActiveRegions,
			Targets:   []asset.ScanTarget{{Key: "region:cn-hangzhou", Kind: asset.ScanTargetRegion, RegionID: "cn-hangzhou"}},
			CreatedAt: now.Add(-2 * time.Minute), FinishedAt: timePointer(now.Add(-2 * time.Minute)),
		},
	} {
		if err := repositories.Inventory().CreateScanRun(ctx, run); err != nil {
			t.Fatal(err)
		}
	}
	spy := &coverageInventorySpy{InventoryRepository: repositories.Inventory()}
	active := []asset.ConnectionRegion{
		{ID: "region-a", ConnectionID: "connection-a", RegionID: "cn-hangzhou", Lifecycle: asset.RegionActive},
		{ID: "region-b", ConnectionID: "connection-a", RegionID: "cn-beijing", Lifecycle: asset.RegionActive},
	}

	summary, err := scancoverage.EvaluateConnection(
		ctx, spy, "connection-a", scancoverage.ActiveRegionRequirement(active, "connection-a"),
	)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Status != "incomplete" || summary.LastCompleteScanAt != nil {
		t.Fatalf("summary = %+v", summary)
	}
	if len(spy.pageOptions) != 1 || spy.pageOptions[0].ConnectionID != "connection-a" || spy.pageOptions[0].Limit != scancoverage.CandidateLimit ||
		spy.byConnectionCalls != 0 || len(spy.shardRunIDs) != 0 {
		t.Fatalf("coverage reads: pages=%+v by_connection=%d shards=%v", spy.pageOptions, spy.byConnectionCalls, spy.shardRunIDs)
	}
}

func TestEvaluateConnectionRejectsIncompleteTargetShardClosure(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 24, 16, 0, 0, 0, time.UTC)
	tests := []struct {
		name    string
		active  []asset.ConnectionRegion
		targets []asset.ScanTarget
		shards  []asset.ScanShard
	}{
		{
			name: "declared target without shard",
			active: []asset.ConnectionRegion{
				activeCoverageRegion("cn-hangzhou"),
				activeCoverageRegion("cn-beijing"),
			},
			targets: []asset.ScanTarget{
				{Key: "region:cn-hangzhou", Kind: asset.ScanTargetRegion, RegionID: "cn-hangzhou"},
				{Key: "region:cn-beijing", Kind: asset.ScanTargetRegion, RegionID: "cn-beijing"},
			},
			shards: []asset.ScanShard{completeCoverageShard("shard-hangzhou", "region:cn-hangzhou", "cn-hangzhou", now)},
		},
		{
			name:    "extra undeclared shard target",
			active:  []asset.ConnectionRegion{activeCoverageRegion("cn-hangzhou")},
			targets: []asset.ScanTarget{{Key: "region:cn-hangzhou", Kind: asset.ScanTargetRegion, RegionID: "cn-hangzhou"}},
			shards: []asset.ScanShard{
				completeCoverageShard("shard-hangzhou", "region:cn-hangzhou", "cn-hangzhou", now),
				completeCoverageShard("shard-extra", "region:cn-beijing", "cn-beijing", now),
			},
		},
		{
			name:   "duplicate declared target",
			active: []asset.ConnectionRegion{activeCoverageRegion("cn-hangzhou")},
			targets: []asset.ScanTarget{
				{Key: "region:cn-hangzhou", Kind: asset.ScanTargetRegion, RegionID: "cn-hangzhou"},
				{Key: "region:cn-hangzhou-copy", Kind: asset.ScanTargetRegion, RegionID: "cn-hangzhou"},
			},
			shards: []asset.ScanShard{completeCoverageShard("shard-hangzhou", "region:cn-hangzhou", "cn-hangzhou", now)},
		},
		{
			name:    "failed shard",
			active:  []asset.ConnectionRegion{activeCoverageRegion("cn-hangzhou")},
			targets: []asset.ScanTarget{{Key: "region:cn-hangzhou", Kind: asset.ScanTargetRegion, RegionID: "cn-hangzhou"}},
			shards: []asset.ScanShard{{
				ID: "shard-failed", TargetKey: "region:cn-hangzhou", RegionID: "cn-hangzhou",
				Status: asset.ShardFailed, Coverage: asset.Coverage{Complete: false},
			}},
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			repositories := openCoverageRepositories(t)
			run := asset.ScanRun{
				ID: "scan", ConnectionID: "connection-a", Status: asset.ScanSucceeded,
				ScopeMode: asset.ScanAllActiveRegions, Targets: test.targets,
				CreatedAt: now, FinishedAt: timePointer(now),
			}
			if err := repositories.Inventory().CreateScanRun(context.Background(), run); err != nil {
				t.Fatal(err)
			}
			for _, shard := range test.shards {
				shard.ScanRunID = run.ID
				shard.Provider = asset.ProviderAliCloud
				shard.CreatedAt = now
				shard.FinishedAt = timePointer(now)
				if err := repositories.Inventory().PutScanShard(context.Background(), shard); err != nil {
					t.Fatal(err)
				}
			}

			summary, err := scancoverage.EvaluateConnection(
				context.Background(), repositories.Inventory(), "connection-a",
				scancoverage.ActiveRegionRequirement(test.active, "connection-a"),
			)
			if err != nil {
				t.Fatal(err)
			}
			if summary.Status == "complete" || summary.LastCompleteScanAt != nil {
				t.Fatalf("unsafe target/shard closure accepted: %+v", summary)
			}
		})
	}
}

func TestEvaluateConnectionBoundsTotalHistoryReads(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 24, 16, 30, 0, 0, time.UTC)
	const candidateLimit = 100
	runs := make([]asset.ScanRun, 0, candidateLimit)
	shards := make(map[asset.ScanRunID][]asset.ScanShard, candidateLimit)
	for index := 0; index < candidateLimit; index++ {
		runID := asset.ScanRunID("scan-" + time.Unix(int64(index), 0).UTC().Format("150405"))
		runs = append(runs, asset.ScanRun{
			ID: runID, ConnectionID: "connection-a", Status: asset.ScanSucceeded,
			ScopeMode: asset.ScanAllActiveRegions,
			Targets:   []asset.ScanTarget{{Key: "region:cn-hangzhou", Kind: asset.ScanTargetRegion, RegionID: "cn-hangzhou"}},
			CreatedAt: now.Add(-time.Duration(index) * time.Minute), FinishedAt: timePointer(now),
		})
		shards[runID] = []asset.ScanShard{{
			ID: asset.ScanShardID("shard-" + string(runID)), ScanRunID: runID,
			TargetKey: "region:cn-hangzhou", RegionID: "cn-hangzhou",
			Status: asset.ShardFailed, Coverage: asset.Coverage{Complete: false},
		}}
	}
	overflowID := asset.ScanRunID("scan-overflow-must-not-read")
	runs = append(runs, asset.ScanRun{
		ID: overflowID, ConnectionID: "connection-a", Status: asset.ScanSucceeded,
		ScopeMode: asset.ScanAllActiveRegions,
		Targets:   []asset.ScanTarget{{Key: "region:cn-hangzhou", Kind: asset.ScanTargetRegion, RegionID: "cn-hangzhou"}},
		CreatedAt: now.Add(-24 * time.Hour), FinishedAt: timePointer(now),
	})
	shards[overflowID] = []asset.ScanShard{{
		ID: "shard-overflow", ScanRunID: overflowID,
		TargetKey: "region:cn-hangzhou", RegionID: "cn-hangzhou",
		Status: asset.ShardFailed, Coverage: asset.Coverage{Complete: false},
	}}
	spy := &coverageInventorySpy{
		pages: []persistence.Page[asset.ScanRun]{
			{Items: runs, NextCursor: "more-history"},
			{Items: []asset.ScanRun{{ID: "must-not-read"}}, NextCursor: ""},
		},
		shards: shards,
	}

	summary, err := scancoverage.EvaluateConnection(
		context.Background(), spy, "connection-a",
		scancoverage.ActiveRegionRequirement([]asset.ConnectionRegion{activeCoverageRegion("cn-hangzhou")}, "connection-a"),
	)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Status != "incomplete" {
		t.Fatalf("summary = %+v", summary)
	}
	if len(spy.pageOptions) != 1 || spy.pageOptions[0].Limit != candidateLimit ||
		len(spy.shardRunIDs) > candidateLimit {
		t.Fatalf("unbounded coverage reads: pages=%+v shards=%d", spy.pageOptions, len(spy.shardRunIDs))
	}
}

type coverageInventorySpy struct {
	persistence.InventoryRepository
	pageOptions       []persistence.ListOptions
	byConnectionCalls int
	shardRunIDs       []asset.ScanRunID
	pages             []persistence.Page[asset.ScanRun]
	shards            map[asset.ScanRunID][]asset.ScanShard
}

func (s *coverageInventorySpy) ListScanRuns(ctx context.Context, options persistence.ListOptions) (persistence.Page[asset.ScanRun], error) {
	s.pageOptions = append(s.pageOptions, options)
	if len(s.pages) > 0 {
		page := s.pages[0]
		s.pages = s.pages[1:]
		return page, nil
	}
	return s.InventoryRepository.ListScanRuns(ctx, options)
}

func (s *coverageInventorySpy) ListScanRunsByConnection(ctx context.Context, connectionID asset.ConnectionID) ([]asset.ScanRun, error) {
	s.byConnectionCalls++
	return s.InventoryRepository.ListScanRunsByConnection(ctx, connectionID)
}

func (s *coverageInventorySpy) ListScanShardsByRun(ctx context.Context, runID asset.ScanRunID) ([]asset.ScanShard, error) {
	s.shardRunIDs = append(s.shardRunIDs, runID)
	if s.shards != nil {
		return append([]asset.ScanShard(nil), s.shards[runID]...), nil
	}
	return s.InventoryRepository.ListScanShardsByRun(ctx, runID)
}

func putCompleteCoverageRun(t *testing.T, repository persistence.InventoryRepository, run asset.ScanRun, shardID asset.ScanShardID) {
	t.Helper()
	if err := repository.CreateScanRun(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	if err := repository.PutScanShard(context.Background(), asset.ScanShard{
		ID: shardID, ScanRunID: run.ID, Provider: asset.ProviderAliCloud,
		TargetKey: run.Targets[0].Key, RegionID: run.Targets[0].RegionID,
		Status: asset.ShardSucceeded, Coverage: asset.Coverage{Complete: true},
		CreatedAt: run.CreatedAt, FinishedAt: run.FinishedAt,
	}); err != nil {
		t.Fatal(err)
	}
}

func openCoverageRepositories(t *testing.T) persistence.Repositories {
	t.Helper()
	repositories, err := sqlite.Open(filepath.Join(t.TempDir(), "coverage.db"), filepath.Join("..", "..", "..", "migrations"))
	if err != nil {
		t.Fatal(err)
	}
	return repositories
}

func timePointer(value time.Time) *time.Time { return &value }

func activeCoverageRegion(regionID string) asset.ConnectionRegion {
	return asset.ConnectionRegion{
		ID: "region-" + regionID, ConnectionID: "connection-a",
		RegionID: regionID, Lifecycle: asset.RegionActive,
	}
}

func completeCoverageShard(id asset.ScanShardID, targetKey, regionID string, finished time.Time) asset.ScanShard {
	return asset.ScanShard{
		ID: id, TargetKey: targetKey, RegionID: regionID,
		Status: asset.ShardSucceeded, Coverage: asset.Coverage{Complete: true},
		FinishedAt: timePointer(finished),
	}
}
