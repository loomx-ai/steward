package inventory

import (
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
