package inventory

import (
	"fmt"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/persistence"
)

type ScanTargetProgress struct {
	Key            string               `json:"key"`
	Kind           asset.ScanTargetKind `json:"kind"`
	RegionID       string               `json:"region_id"`
	RegionName     string               `json:"region_name,omitempty"`
	NativeID       string               `json:"native_id,omitempty"`
	Name           string               `json:"name,omitempty"`
	ParentNativeID string               `json:"parent_native_id,omitempty"`
	Status         asset.ScanStatus     `json:"status"`
	Completed      int                  `json:"completed"`
	Total          int                  `json:"total"`
	ResourceCount  int                  `json:"resource_count"`
	ErrorCount     int                  `json:"error_count"`
	Summary        string               `json:"summary"`
}

type ScanOverallProgress struct {
	Completed     int `json:"completed"`
	Total         int `json:"total"`
	Running       int `json:"running"`
	Failed        int `json:"failed"`
	ResourceCount int `json:"resource_count"`
}

type ScanTaskProjection struct {
	asset.ScanTask
	TargetProgress []ScanTargetProgress `json:"target_progress"`
	Progress       ScanOverallProgress  `json:"progress"`
	AllowedActions []string             `json:"allowed_actions"`
	DurationMS     *int64               `json:"duration_ms,omitempty"`
	UpdatedAt      time.Time            `json:"updated_at"`
}

func ProjectScanTask(task asset.ScanTask, shards []asset.ScanShard) ScanTaskProjection {
	byTarget := make(map[string]scanTargetSummary, len(task.Targets))
	for _, shard := range shards {
		summary := byTarget[shard.TargetKey]
		summary.TargetKey = shard.TargetKey
		summary.Total++
		summary.ResourceCount += shard.Coverage.ItemCount
		switch shard.Status {
		case asset.ShardSucceeded, asset.ShardSkipped, asset.ShardFailed, asset.ShardBlocked, asset.ShardCanceled:
			summary.Completed++
		}
		switch shard.Status {
		case asset.ShardRunning:
			summary.Running++
		case asset.ShardPaused:
			summary.Paused++
		case asset.ShardSucceeded, asset.ShardSkipped:
			summary.Succeeded++
		case asset.ShardFailed, asset.ShardBlocked:
			summary.Failed++
		case asset.ShardCanceled:
			summary.Canceled++
		}
		byTarget[shard.TargetKey] = summary
	}
	return projectScanTaskFromSummaries(task, byTarget)
}

func ProjectScanTaskListItem(item persistence.ScanRunListItem, now time.Time) ScanTaskProjection {
	task := item.ScanRun
	task.Targets = append([]asset.ScanTarget(nil), task.Targets...)
	sortScanTargets(task.Targets)
	// Target-level progress belongs to the detail route. Keep the collection
	// payload compact and serve its displayed counters from the persisted row.
	projection := ScanTaskProjection{
		ScanTask:       task,
		TargetProgress: []ScanTargetProgress{},
		Progress:       ScanOverallProgress{ResourceCount: item.ResourceCount},
		AllowedActions: allowedTaskActions(task.Status),
		UpdatedAt:      item.UpdatedAt,
	}
	if projection.UpdatedAt.IsZero() {
		projection.UpdatedAt = task.CreatedAt
	}
	if item.DurationRecorded {
		durationMS := item.DurationMS
		if item.DurationActive && item.DurationCalculatedAt != nil && now.After(*item.DurationCalculatedAt) {
			durationMS += now.Sub(*item.DurationCalculatedAt).Milliseconds()
		}
		projection.DurationMS = &durationMS
	}
	return projection
}

type scanTargetSummary struct {
	TargetKey     string
	Total         int
	Completed     int
	Running       int
	Paused        int
	Succeeded     int
	Failed        int
	Canceled      int
	ResourceCount int
}

func projectScanTaskFromSummaries(task asset.ScanTask, byTarget map[string]scanTargetSummary) ScanTaskProjection {
	task.Targets = append([]asset.ScanTarget(nil), task.Targets...)
	sortScanTargets(task.Targets)
	projection := ScanTaskProjection{ScanTask: task, TargetProgress: make([]ScanTargetProgress, 0, len(task.Targets)), AllowedActions: allowedTaskActions(task.Status)}
	for _, target := range task.Targets {
		progress := projectTarget(target, byTarget[target.Key])
		projection.TargetProgress = append(projection.TargetProgress, progress)
		projection.Progress.Total++
		projection.Progress.ResourceCount += progress.ResourceCount
		switch progress.Status {
		case asset.ScanSucceeded, asset.ScanCanceled:
			projection.Progress.Completed++
		case asset.ScanFailed, asset.ScanPartial:
			projection.Progress.Completed++
			projection.Progress.Failed++
		case asset.ScanRunning:
			projection.Progress.Running++
		}
	}
	return ApplyScanTiming(projection, nil, task.CreatedAt)
}

func ApplyScanTiming(projection ScanTaskProjection, jobs []execution.Job, now time.Time) ScanTaskProjection {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if duration, recorded := execution.ActiveDuration(jobs, now); recorded {
		milliseconds := duration.Milliseconds()
		projection.DurationMS = &milliseconds
	}
	updatedAt := projection.CreatedAt
	for _, value := range []*time.Time{
		projection.StartedAt,
		projection.FinishedAt,
		projection.PausedAt,
		projection.CanceledAt,
	} {
		if value != nil && value.After(updatedAt) {
			updatedAt = *value
		}
	}
	projection.UpdatedAt = execution.LatestJobUpdate(jobs, updatedAt)
	return projection
}

func projectTarget(target asset.ScanTarget, summary scanTargetSummary) ScanTargetProgress {
	progress := ScanTargetProgress{
		Key: target.Key, Kind: target.Kind, RegionID: target.RegionID, RegionName: target.RegionName,
		NativeID: target.NativeID, Name: target.Name, ParentNativeID: target.ParentNativeID,
		Completed: summary.Completed, Total: summary.Total, ResourceCount: summary.ResourceCount, ErrorCount: summary.Failed,
	}
	switch {
	case summary.Running > 0:
		progress.Status = asset.ScanRunning
		progress.Summary = fmt.Sprintf("已完成 %d/%d", progress.Completed, progress.Total)
	case summary.Paused > 0:
		progress.Status = asset.ScanPaused
		progress.Summary = fmt.Sprintf("已暂停 · %d 个资源", progress.ResourceCount)
	case summary.Failed > 0:
		if summary.Succeeded > 0 {
			progress.Status = asset.ScanPartial
		} else {
			progress.Status = asset.ScanFailed
		}
		progress.Summary = fmt.Sprintf("%d 项失败", progress.ErrorCount)
	case summary.Canceled > 0:
		progress.Status = asset.ScanCanceled
		progress.Summary = fmt.Sprintf("已取消 · 保留 %d 个资源", progress.ResourceCount)
	case summary.Total > 0 && summary.Succeeded == summary.Total:
		progress.Status = asset.ScanSucceeded
		progress.Summary = fmt.Sprintf("%d 个资源", progress.ResourceCount)
	default:
		progress.Status = asset.ScanPending
		if progress.Completed > 0 {
			progress.Summary = fmt.Sprintf("已完成 %d/%d", progress.Completed, progress.Total)
		} else {
			progress.Summary = "等待开始"
		}
	}
	return progress
}

func allowedTaskActions(status asset.ScanStatus) []string {
	switch status {
	case asset.ScanPending, asset.ScanRunning:
		return []string{"pause", "cancel"}
	case asset.ScanPausing, asset.ScanPaused:
		return []string{"resume", "cancel"}
	case asset.ScanFailed, asset.ScanPartial:
		return []string{"retry"}
	default:
		return []string{}
	}
}
