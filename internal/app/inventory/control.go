package inventory

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	connectionapp "github.com/loomx-ai/steward/internal/app/connection"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/idgen"
	"github.com/loomx-ai/steward/internal/persistence"
)

type ControlService struct {
	repositories persistence.Repositories
	directory    ScanDirectory
	clock        func() time.Time
	newID        func(string) string
}

type ControlOption func(*ControlService)

func WithControlDirectory(directory ScanDirectory) ControlOption {
	return func(service *ControlService) {
		service.directory = directory
	}
}

func NewControlService(repositories persistence.Repositories, options ...ControlOption) (*ControlService, error) {
	if repositories == nil {
		return nil, fmt.Errorf("scan control requires repositories")
	}
	service := &ControlService{
		repositories: repositories,
		clock:        func() time.Time { return time.Now().UTC() },
		newID:        func(prefix string) string { return idgen.MustNew(prefix) },
	}
	for _, option := range options {
		option(service)
	}
	return service, nil
}

func (s *ControlService) Pause(ctx context.Context, id asset.ScanTaskID, actor string) (asset.ScanTask, error) {
	return s.mutate(ctx, id, actor, "pause", func(repositories persistence.Repositories, task *asset.ScanTask, now time.Time) error {
		if task.Status != asset.ScanPending && task.Status != asset.ScanRunning {
			return scanRequestError("scan.pause_not_allowed", "only pending or running scan tasks can be paused", map[string]any{"status": task.Status})
		}
		task.Status = asset.ScanPausing
		shards, err := repositories.Inventory().ListScanShardsByRun(ctx, task.ID)
		if err != nil {
			return err
		}
		for _, shard := range shards {
			if shard.Status != asset.ShardPending {
				continue
			}
			shard.Status = asset.ShardPaused
			if err := repositories.Inventory().PutScanShard(ctx, shard); err != nil {
				return err
			}
		}
		jobs, err := repositories.Jobs().ListJobsByAggregate(ctx, "scan_task", string(task.ID))
		if err != nil {
			return err
		}
		running := false
		for _, job := range jobs {
			switch job.Status {
			case execution.JobPending:
				job.Status = execution.JobPaused
				job.UpdatedAt = now
				if err := repositories.Jobs().UpdateJob(ctx, job); err != nil {
					return err
				}
			case execution.JobRunning:
				running = true
			}
		}
		if !running {
			task.Status = asset.ScanPaused
			task.PausedAt = &now
		}
		return nil
	})
}

func (s *ControlService) Resume(ctx context.Context, id asset.ScanTaskID, actor string) (asset.ScanTask, error) {
	return s.mutate(ctx, id, actor, "resume", func(repositories persistence.Repositories, task *asset.ScanTask, now time.Time) error {
		if task.Status != asset.ScanPaused && task.Status != asset.ScanPausing {
			return scanRequestError("scan.resume_not_allowed", "only pausing or paused scan tasks can be resumed", map[string]any{"status": task.Status})
		}
		if err := connectionapp.GuardActiveWork(ctx, repositories, task.ConnectionID, now); err != nil {
			return err
		}
		shards, err := repositories.Inventory().ListScanShardsByRun(ctx, task.ID)
		if err != nil {
			return err
		}
		for _, shard := range shards {
			if shard.Status != asset.ShardPaused {
				continue
			}
			shard.Status = asset.ShardPending
			shard.StartedAt = nil
			if err := repositories.Inventory().PutScanShard(ctx, shard); err != nil {
				return err
			}
		}
		jobs, err := repositories.Jobs().ListJobsByAggregate(ctx, "scan_task", string(task.ID))
		if err != nil {
			return err
		}
		for _, job := range jobs {
			if job.Status != execution.JobPaused {
				continue
			}
			job.Status = execution.JobPending
			job.RunAt = now
			job.UpdatedAt = now
			job.LeaseOwner = ""
			job.LeaseUntil = nil
			if err := repositories.Jobs().UpdateJob(ctx, job); err != nil {
				return err
			}
		}
		if task.StartedAt == nil {
			task.Status = asset.ScanPending
		} else {
			task.Status = asset.ScanRunning
		}
		task.PausedAt = nil
		return nil
	})
}

func (s *ControlService) Cancel(ctx context.Context, id asset.ScanTaskID, actor string) (asset.ScanTask, error) {
	return s.mutate(ctx, id, actor, "cancel", func(repositories persistence.Repositories, task *asset.ScanTask, now time.Time) error {
		if isTerminalTask(task.Status) {
			return scanRequestError("scan.cancel_not_allowed", "terminal scan tasks cannot be canceled", map[string]any{"status": task.Status})
		}
		task.Status = asset.ScanCanceling
		shards, err := repositories.Inventory().ListScanShardsByRun(ctx, task.ID)
		if err != nil {
			return err
		}
		for _, shard := range shards {
			if shard.Status != asset.ShardPending && shard.Status != asset.ShardPaused && shard.Status != asset.ShardBlocked {
				continue
			}
			shard.Status = asset.ShardCanceled
			shard.FinishedAt = &now
			if err := repositories.Inventory().PutScanShard(ctx, shard); err != nil {
				return err
			}
		}
		jobs, err := repositories.Jobs().ListJobsByAggregate(ctx, "scan_task", string(task.ID))
		if err != nil {
			return err
		}
		running := false
		for _, job := range jobs {
			switch job.Status {
			case execution.JobPending, execution.JobPaused:
				job.Status = execution.JobCanceled
				job.UpdatedAt = now
				job.FinishedAt = &now
				job.LeaseOwner = ""
				job.LeaseUntil = nil
				if err := repositories.Jobs().UpdateJob(ctx, job); err != nil {
					return err
				}
			case execution.JobRunning:
				running = true
			}
		}
		if !running {
			task.Status = asset.ScanCanceled
			task.CanceledAt = &now
			task.FinishedAt = &now
		}
		return nil
	})
}

func (s *ControlService) Retry(ctx context.Context, id asset.ScanTaskID, actor string) (asset.ScanTask, error) {
	return s.mutate(ctx, id, actor, "retry", func(repositories persistence.Repositories, task *asset.ScanTask, now time.Time) error {
		if task.Status == asset.ScanCanceled || task.Status == asset.ScanCanceling {
			return scanRequestError("scan.retry_not_allowed", "canceled scan tasks cannot be retried", map[string]any{"status": task.Status})
		}
		if task.Status != asset.ScanFailed && task.Status != asset.ScanPartial && task.Status != asset.ScanSucceeded {
			return scanRequestError("scan.retry_not_allowed", "only failed, partial, or incomplete succeeded scan tasks can be retried", map[string]any{"status": task.Status})
		}
		if err := connectionapp.GuardActiveWork(ctx, repositories, task.ConnectionID, now); err != nil {
			return err
		}
		shards, err := repositories.Inventory().ListScanShardsByRun(ctx, task.ID)
		if err != nil {
			return err
		}
		if graphReconciliationFailed(*task, shards) {
			generation := task.RetryGeneration + 1
			job := execution.Job{
				ID: execution.JobID(s.newID("job")), ConnectionID: task.ConnectionID,
				IdempotencyKey: scanGraphJobIdempotencyKey(task.ID, generation),
				AggregateType:  "scan_task", AggregateID: string(task.ID),
				RetryGeneration: generation, Type: execution.JobGraph, Status: execution.JobPending,
				Payload: map[string]any{"scan_run_id": string(task.ID)},
				RunAt:   now, CreatedAt: now, UpdatedAt: now,
			}
			if err := repositories.Jobs().Enqueue(ctx, job); err != nil {
				return err
			}
			task.Status = asset.ScanReconciling
			task.RetryGeneration = generation
			task.RetryCount++
			task.FinishedAt = nil
			return nil
		}
		desiredSignatures, managedPlan, err := s.desiredRetryShardSignatures(ctx, repositories, *task)
		if err != nil {
			return err
		}
		generation := task.RetryGeneration + 1
		byTarget := make(map[string][]string)
		for _, shard := range shards {
			if shard.Status != asset.ShardFailed && shard.Status != asset.ShardBlocked {
				continue
			}
			if _, desired := desiredSignatures[retryScanShardSignature(shard)]; managedPlan && !desired {
				shard.Status = asset.ShardSkipped
				shard.Authoritative = false
				shard.FinishedAt = &now
				shard.Coverage.Source = shard.Source
				shard.Coverage.TargetKey = shard.TargetKey
				shard.Coverage.ScopeID = shard.ScopeID
				shard.Coverage.ResourceKindID = shard.ResourceKindID
				shard.Coverage.Authoritative = false
				shard.Coverage.Complete = false
				shard.Coverage.FreshAt = now
				shard.Coverage.FailureReason = ""
				shard.Coverage.SkipReason = asset.SkipProductUnsupported
				if err := repositories.Inventory().PutScanShard(ctx, shard); err != nil {
					return err
				}
				continue
			}
			shard.Status = asset.ShardPending
			shard.RetryGeneration = generation
			shard.StartedAt = nil
			shard.FinishedAt = nil
			shard.Coverage.Complete = false
			shard.Coverage.ItemCount = 0
			shard.Coverage.FailureReason = ""
			shard.Coverage.SkipReason = ""
			if err := repositories.Inventory().PutScanShard(ctx, shard); err != nil {
				return err
			}
			byTarget[shard.TargetKey] = append(byTarget[shard.TargetKey], string(shard.ID))
		}
		added, err := s.reconcileRetryPlan(ctx, repositories, task, shards, generation, now)
		if err != nil {
			return err
		}
		for _, shard := range added {
			byTarget[shard.TargetKey] = append(byTarget[shard.TargetKey], string(shard.ID))
		}
		if len(byTarget) == 0 {
			if task.Status == asset.ScanSucceeded {
				return scanRequestError("scan.retry_not_allowed", "the succeeded scan task already covers the current provider scan plan", nil)
			}
			return scanRequestError("scan.retry_empty", "the scan task has no failed or unstarted content to retry", nil)
		}
		keys := orderedTargetKeys(task.Targets, byTarget)
		for _, targetKey := range keys {
			shardIDs := byTarget[targetKey]
			job := execution.Job{
				ID: execution.JobID(s.newID("job")), ConnectionID: task.ConnectionID, AggregateType: "scan_task", AggregateID: string(task.ID),
				TargetKey: targetKey, RetryGeneration: generation, Type: execution.JobScan, Status: execution.JobPending,
				Payload: map[string]any{"scan_task_id": string(task.ID), "target_key": targetKey, "scan_shard_ids": shardIDs},
				RunAt:   now, CreatedAt: now, UpdatedAt: now,
			}
			if err := repositories.Jobs().Enqueue(ctx, job); err != nil {
				return err
			}
		}
		task.Status = asset.ScanPending
		task.RetryGeneration = generation
		task.RetryCount++
		task.FinishedAt = nil
		return nil
	})
}

func scanGraphJobIdempotencyKey(taskID asset.ScanTaskID, retryGeneration int) string {
	key := "scan-graph:" + string(taskID)
	if retryGeneration > 0 {
		return fmt.Sprintf("%s:retry:%d", key, retryGeneration)
	}
	return key
}

func graphReconciliationFailed(task asset.ScanTask, shards []asset.ScanShard) bool {
	// A partial scan still has failed or blocked shards to retry before the
	// graph can be reconciled again.
	if task.Status != asset.ScanFailed ||
		task.CompletionStatus != asset.ScanSucceeded ||
		len(shards) == 0 {
		return false
	}
	for _, shard := range shards {
		switch shard.Status {
		case asset.ShardSucceeded, asset.ShardSkipped:
		default:
			return false
		}
	}
	return true
}

func (s *ControlService) CanRetry(
	ctx context.Context,
	task asset.ScanTask,
	shards []asset.ScanShard,
) (bool, error) {
	switch task.Status {
	case asset.ScanFailed, asset.ScanPartial:
		return true, nil
	case asset.ScanSucceeded:
		return s.retryPlanHasGaps(ctx, s.repositories, task, shards)
	default:
		return false, nil
	}
}

func (s *ControlService) mutate(ctx context.Context, id asset.ScanTaskID, actor, action string, mutation func(persistence.Repositories, *asset.ScanTask, time.Time) error) (asset.ScanTask, error) {
	if id == "" {
		return asset.ScanTask{}, scanRequestError("scan.id_required", "scan task ID is required", nil)
	}
	actor = strings.TrimSpace(actor)
	if actor == "" {
		return asset.ScanTask{}, scanRequestError("scan.actor_required", "scan task action requires an actor", nil)
	}
	var updated asset.ScanTask
	err := s.repositories.WithTx(ctx, func(repositories persistence.Repositories) error {
		task, err := repositories.Inventory().GetScanRun(ctx, id)
		if err != nil {
			return err
		}
		expected := task.ControlVersion
		now := s.clock()
		if err := mutation(repositories, &task, now); err != nil {
			return err
		}
		task.ControlVersion = expected + 1
		if err := repositories.Inventory().PutScanRunIfControlVersion(ctx, task, expected); err != nil {
			return err
		}
		if err := repositories.Audits().AppendAuditEvent(ctx, execution.AuditEvent{
			ID: execution.AuditEventID(s.newID("aud")), ConnectionID: task.ConnectionID, Actor: actor,
			Action: "inventory.scan." + action, TargetType: "scan_task", TargetID: string(task.ID), Result: string(task.Status), CreatedAt: now,
		}); err != nil {
			return err
		}
		updated = task
		return nil
	})
	return updated, err
}

func orderedTargetKeys(targets []asset.ScanTarget, values map[string][]string) []string {
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, target := range targets {
		if _, exists := values[target.Key]; !exists {
			continue
		}
		seen[target.Key] = struct{}{}
		result = append(result, target.Key)
	}
	rest := make([]string, 0)
	for key := range values {
		if _, exists := seen[key]; !exists {
			rest = append(rest, key)
		}
	}
	sort.Strings(rest)
	return append(result, rest...)
}

func isTerminalTask(status asset.ScanStatus) bool {
	switch status {
	case asset.ScanSucceeded, asset.ScanPartial, asset.ScanFailed, asset.ScanCanceled:
		return true
	default:
		return false
	}
}
