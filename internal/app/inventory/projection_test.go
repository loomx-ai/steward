package inventory

import (
	"fmt"
	"testing"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/persistence"
)

func TestApplyScanTimingUsesMergedJobRunsAndLatestUpdate(t *testing.T) {
	base := time.Date(2026, 8, 4, 8, 0, 0, 0, time.UTC)
	finished := base.Add(10 * time.Second)
	updated := base.Add(time.Minute)
	projection := ProjectScanTask(asset.ScanTask{
		ID: "scan-1", CreatedAt: base,
	}, nil)
	projection = ApplyScanTiming(projection, []execution.Job{{
		UpdatedAt: updated,
		RunIntervals: []execution.JobRunInterval{{
			StartedAt:  base,
			FinishedAt: &finished,
		}},
	}}, updated)

	if projection.DurationMS == nil || *projection.DurationMS != 10_000 {
		t.Fatalf("duration_ms = %v, want 10000", projection.DurationMS)
	}
	if !projection.UpdatedAt.Equal(updated) {
		t.Fatalf("updated_at = %s, want %s", projection.UpdatedAt, updated)
	}
}

func TestProjectScanTaskListItemUsesPersistedReadModel(t *testing.T) {
	base := time.Date(2026, 8, 4, 8, 0, 0, 0, time.UTC)
	calculatedAt := base.Add(10 * time.Second)
	updatedAt := base.Add(11 * time.Second)
	got := ProjectScanTaskListItem(persistence.ScanRunListItem{
		ScanRun: asset.ScanRun{
			ID: "scan-summary", Status: asset.ScanRunning, CreatedAt: base,
		},
		ResourceCount: 7, DurationMS: 10_000, DurationRecorded: true,
		DurationActive: true, DurationCalculatedAt: &calculatedAt, UpdatedAt: updatedAt,
	}, base.Add(12*time.Second))

	if got.Progress.ResourceCount != 7 || len(got.TargetProgress) != 0 {
		t.Fatalf("list progress = %#v, target progress = %#v", got.Progress, got.TargetProgress)
	}
	if got.DurationMS == nil || *got.DurationMS != 12_000 {
		t.Fatalf("duration_ms = %v, want 12000", got.DurationMS)
	}
	if !got.UpdatedAt.Equal(updatedAt) {
		t.Fatalf("updated_at = %s, want %s", got.UpdatedAt, updatedAt)
	}
}

// The live scan stream projects item-count changes from progress rows; they
// must match the projection from the shards themselves.
func TestProjectScanTaskProgressMatchesShards(t *testing.T) {
	task := asset.ScanTask{ID: "scan-1", Status: asset.ScanRunning, Targets: []asset.ScanTarget{
		{Key: "region:a", Kind: asset.ScanTargetRegion, RegionID: "a"},
		{Key: "region:b", Kind: asset.ScanTargetRegion, RegionID: "b"},
		{Key: "region:c", Kind: asset.ScanTargetRegion, RegionID: "c"},
	}}
	var shards []asset.ScanShard
	rows := map[[2]string]persistence.ScanShardProgress{}
	for _, value := range []struct {
		target string
		status asset.ShardStatus
		items  int
	}{
		{"region:a", asset.ShardSucceeded, 3}, {"region:a", asset.ShardRunning, 4}, {"region:a", asset.ShardSucceeded, 5},
		{"region:b", asset.ShardFailed, 1}, {"region:b", asset.ShardSkipped, 0}, {"region:b", asset.ShardCanceled, 2},
		{"region:c", asset.ShardPaused, 6}, {"region:c", asset.ShardBlocked, 0}, {"region:c", asset.ShardPending, 0},
	} {
		shard := asset.ScanShard{TargetKey: value.target, Status: value.status}
		shard.Coverage.ItemCount = value.items
		shards = append(shards, shard)
		key := [2]string{value.target, string(value.status)}
		row := rows[key]
		row.TargetKey, row.Status = value.target, value.status
		row.Shards++
		row.ItemCount += value.items
		rows[key] = row
	}
	progress := make([]persistence.ScanShardProgress, 0, len(rows))
	for _, row := range rows {
		progress = append(progress, row)
	}
	want, got := ProjectScanTask(task, shards), ProjectScanTaskProgress(task, progress)
	if fmt.Sprintf("%+v", want) != fmt.Sprintf("%+v", got) {
		t.Fatalf("progress projection =\n%+v\nwant\n%+v", got, want)
	}
}
