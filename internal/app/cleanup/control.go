package cleanup

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/internal/idgen"
	"github.com/loomx-ai/steward/internal/persistence"
)

type PauseExecutionRequest struct {
	ConnectionID  asset.ConnectionID `json:"connection_id"`
	CleanupTaskID plan.CleanupTaskID `json:"cleanup_task_id"`
	RequestedBy   string             `json:"requested_by"`
}

type ResumeExecutionRequest struct {
	ConnectionID  asset.ConnectionID `json:"connection_id"`
	CleanupTaskID plan.CleanupTaskID `json:"cleanup_task_id"`
	RequestedBy   string             `json:"requested_by"`
}

var (
	ErrExecutionNotPausable  = errors.New("cleanup execution cannot be paused")
	ErrExecutionNotResumable = errors.New("cleanup execution cannot be resumed")
)

func (s *Service) PauseExecution(ctx context.Context, request PauseExecutionRequest) (execution.ExecutionAttempt, error) {
	if s == nil || s.repositories == nil {
		return execution.ExecutionAttempt{}, fmt.Errorf("planning repositories are required")
	}
	actor := strings.TrimSpace(request.RequestedBy)
	if request.CleanupTaskID == "" || actor == "" {
		return execution.ExecutionAttempt{}, fmt.Errorf("cleanup task and actor are required")
	}

	var paused execution.ExecutionAttempt
	err := s.repositories.WithTx(ctx, func(repositories persistence.Repositories) error {
		aggregate, attempt, err := latestCleanupExecution(ctx, repositories, request.ConnectionID, request.CleanupTaskID)
		if err != nil {
			return err
		}
		taskControlled := aggregate.Task.Status == plan.StatusPausing || aggregate.Task.Status == plan.StatusPaused
		attemptControlled := attempt.Status == execution.ExecutionPausing || attempt.Status == execution.ExecutionPaused
		if taskControlled || attemptControlled {
			if !attemptControlled && pausableExecutionStatus(attempt.Status) {
				attempt.PausedFrom = attempt.Status
				attempt.Status = execution.ExecutionPausing
				if aggregate.Task.Status == plan.StatusPaused {
					attempt.Status = execution.ExecutionPaused
				}
				if err := repositories.Executions().UpdateExecution(ctx, attempt); err != nil {
					return err
				}
			}
			if !taskControlled {
				aggregate.Task.Status = plan.StatusPausing
				if attempt.Status == execution.ExecutionPaused {
					aggregate.Task.Status = plan.StatusPaused
				}
				if err := repositories.CleanupTasks().UpdateTask(ctx, aggregate.Task); err != nil {
					return err
				}
			}
			paused = attempt
			return nil
		}
		if aggregate.Task.Status != plan.StatusExecuting || !pausableExecutionStatus(attempt.Status) {
			return fmt.Errorf(
				"%w: cleanup task %q is not executing: task=%s execution=%s",
				ErrExecutionNotPausable,
				request.CleanupTaskID,
				aggregate.Task.Status,
				attempt.Status,
			)
		}

		now := s.clock()
		jobs, err := repositories.Jobs().ListJobsByAggregate(ctx, "cleanup_task", string(aggregate.Task.ID))
		if err != nil {
			return err
		}
		running := false
		for _, job := range jobs {
			if payloadString(job.Payload, "execution_id") != string(attempt.ID) {
				continue
			}
			switch job.Status {
			case execution.JobPending:
				job.Status = execution.JobPaused
				job.UpdatedAt = now
				job.LeaseOwner = ""
				job.LeaseUntil = nil
				if err := repositories.Jobs().UpdateJob(ctx, job); err != nil {
					return err
				}
			case execution.JobRunning:
				running = true
			}
		}
		if running {
			attempt.PausedFrom = attempt.Status
			aggregate.Task.Status = plan.StatusPausing
			attempt.Status = execution.ExecutionPausing
		} else {
			attempt.PausedFrom = attempt.Status
			aggregate.Task.Status = plan.StatusPaused
			attempt.Status = execution.ExecutionPaused
		}
		if err := repositories.Executions().UpdateExecution(ctx, attempt); err != nil {
			return err
		}
		if err := repositories.CleanupTasks().UpdateTask(ctx, aggregate.Task); err != nil {
			return err
		}
		if err := repositories.Audits().AppendAuditEvent(ctx, execution.AuditEvent{
			ID: execution.AuditEventID(idgen.MustNew("aud")), ConnectionID: aggregate.Task.ConnectionID,
			Actor: actor, Action: "cleanup.execution.pause", TargetType: "execution_attempt", TargetID: string(attempt.ID), Result: "accepted",
			Evidence: map[string]any{"cleanup_task_id": aggregate.Task.ID, "status": attempt.Status}, CreatedAt: now,
		}); err != nil {
			return err
		}
		paused = attempt
		return nil
	})
	return paused, err
}

func (s *Service) ResumeExecution(ctx context.Context, request ResumeExecutionRequest) (execution.ExecutionAttempt, error) {
	if s == nil || s.repositories == nil {
		return execution.ExecutionAttempt{}, fmt.Errorf("planning repositories are required")
	}
	actor := strings.TrimSpace(request.RequestedBy)
	if request.CleanupTaskID == "" || actor == "" {
		return execution.ExecutionAttempt{}, fmt.Errorf("cleanup task and actor are required")
	}

	var resumed execution.ExecutionAttempt
	err := s.repositories.WithTx(ctx, func(repositories persistence.Repositories) error {
		aggregate, attempt, err := latestCleanupExecution(ctx, repositories, request.ConnectionID, request.CleanupTaskID)
		if err != nil {
			return err
		}
		if aggregate.Task.Status == plan.StatusExecuting && pausableExecutionStatus(attempt.Status) {
			resumed = attempt
			return nil
		}
		taskControlled := aggregate.Task.Status == plan.StatusPausing || aggregate.Task.Status == plan.StatusPaused
		attemptControlled := attempt.Status == execution.ExecutionPausing || attempt.Status == execution.ExecutionPaused
		if (!taskControlled && !attemptControlled) ||
			(!attemptControlled && !pausableExecutionStatus(attempt.Status)) {
			return fmt.Errorf(
				"%w: cleanup task %q is not paused: task=%s execution=%s",
				ErrExecutionNotResumable,
				request.CleanupTaskID,
				aggregate.Task.Status,
				attempt.Status,
			)
		}

		now := s.clock()
		jobs, err := repositories.Jobs().ListJobsByAggregate(ctx, "cleanup_task", string(aggregate.Task.ID))
		if err != nil {
			return err
		}
		for _, job := range jobs {
			if payloadString(job.Payload, "execution_id") != string(attempt.ID) || job.Status != execution.JobPaused {
				continue
			}
			job.Status = execution.JobPending
			job.RunAt = now
			job.LeaseOwner = ""
			job.LeaseUntil = nil
			job.LastError = ""
			job.FinishedAt = nil
			job.UpdatedAt = now
			if err := repositories.Jobs().UpdateJob(ctx, job); err != nil {
				return err
			}
		}
		aggregate.Task.Status = plan.StatusExecuting
		resumeStatus := attempt.PausedFrom
		if !pausableExecutionStatus(resumeStatus) {
			resumeStatus = execution.ExecutionRunning
		}
		attempt.Status = resumeStatus
		attempt.PausedFrom = ""
		attempt.FinishedAt = nil
		if err := repositories.Executions().UpdateExecution(ctx, attempt); err != nil {
			return err
		}
		if err := repositories.CleanupTasks().UpdateTask(ctx, aggregate.Task); err != nil {
			return err
		}
		if err := repositories.Audits().AppendAuditEvent(ctx, execution.AuditEvent{
			ID: execution.AuditEventID(idgen.MustNew("aud")), ConnectionID: aggregate.Task.ConnectionID,
			Actor: actor, Action: "cleanup.execution.resume", TargetType: "execution_attempt", TargetID: string(attempt.ID), Result: "accepted",
			Evidence: map[string]any{"cleanup_task_id": aggregate.Task.ID}, CreatedAt: now,
		}); err != nil {
			return err
		}
		resumed = attempt
		return nil
	})
	return resumed, err
}

func latestCleanupExecution(
	ctx context.Context,
	repositories persistence.Repositories,
	connectionID asset.ConnectionID,
	cleanupTaskID plan.CleanupTaskID,
) (persistence.CleanupTaskAggregate, execution.ExecutionAttempt, error) {
	aggregate, err := repositories.CleanupTasks().GetTask(ctx, cleanupTaskID)
	if err != nil {
		return persistence.CleanupTaskAggregate{}, execution.ExecutionAttempt{}, err
	}
	if connectionID != "" && aggregate.Task.ConnectionID != connectionID {
		return persistence.CleanupTaskAggregate{}, execution.ExecutionAttempt{}, persistence.ErrNotFound
	}
	page, err := repositories.Executions().ListCleanupTaskExecutions(
		ctx,
		aggregate.Task.ConnectionID,
		string(aggregate.Task.ID),
		persistence.ListOptions{Limit: 500},
	)
	if err != nil {
		return persistence.CleanupTaskAggregate{}, execution.ExecutionAttempt{}, err
	}
	if len(page.Items) == 0 {
		return persistence.CleanupTaskAggregate{}, execution.ExecutionAttempt{}, persistence.ErrNotFound
	}
	return aggregate, page.Items[len(page.Items)-1], nil
}

func pausableExecutionStatus(status execution.ExecutionStatus) bool {
	switch status {
	case execution.ExecutionPending, execution.ExecutionRunning, execution.ExecutionWaiting, execution.ExecutionReconciling:
		return true
	default:
		return false
	}
}
