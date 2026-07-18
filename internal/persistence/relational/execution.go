package relational

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/internal/core/requestmeta"
	"github.com/loomx-ai/steward/internal/persistence"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type executionAttemptRow struct {
	ID             string    `gorm:"column:id;primaryKey"`
	ConnectionID   string    `gorm:"column:connection_id"`
	CleanupTaskID  string    `gorm:"column:cleanup_task_id"`
	Status         string    `gorm:"column:status"`
	IdempotencyKey string    `gorm:"column:idempotency_key"`
	CreatedAt      time.Time `gorm:"column:created_at"`
	Payload        string    `gorm:"column:payload"`
}

type actionAttemptRow struct {
	ID                string    `gorm:"column:id;primaryKey"`
	ExecutionID       string    `gorm:"column:execution_id"`
	CleanupTaskStepID string    `gorm:"column:cleanup_task_step_id"`
	AssetID           string    `gorm:"column:asset_id"`
	Status            string    `gorm:"column:status"`
	IdempotencyKey    string    `gorm:"column:idempotency_key"`
	CreatedAt         time.Time `gorm:"column:created_at"`
	UpdatedAt         time.Time `gorm:"column:updated_at"`
	Payload           string    `gorm:"column:payload"`
}

type cleanupTaskExecutionRow struct {
	ParentCleanupTaskID string  `gorm:"column:parent_cleanup_task_id"`
	ExecutionID         *string `gorm:"column:execution_id"`
	ExecutionPayload    *string `gorm:"column:execution_payload"`
}

type executionActionRow struct {
	ParentExecutionID string  `gorm:"column:parent_execution_id"`
	ActionID          *string `gorm:"column:action_id"`
	ActionPayload     *string `gorm:"column:action_payload"`
}

type outboxEventRow struct {
	ID          string     `gorm:"column:id;primaryKey"`
	Topic       string     `gorm:"column:topic"`
	AggregateID string     `gorm:"column:aggregate_id"`
	CreatedAt   time.Time  `gorm:"column:created_at"`
	PublishedAt *time.Time `gorm:"column:published_at"`
	Payload     string     `gorm:"column:payload"`
}

type auditEventRow struct {
	ID           string    `gorm:"column:id;primaryKey"`
	ConnectionID string    `gorm:"column:connection_id"`
	Actor        string    `gorm:"column:actor"`
	Action       string    `gorm:"column:action"`
	TargetType   string    `gorm:"column:target_type"`
	TargetID     string    `gorm:"column:target_id"`
	CreatedAt    time.Time `gorm:"column:created_at"`
	Payload      string    `gorm:"column:payload"`
}

type jobRow struct {
	ID              string     `gorm:"column:id;primaryKey"`
	ConnectionID    string     `gorm:"column:connection_id"`
	IdempotencyKey  *string    `gorm:"column:idempotency_key"`
	AggregateType   string     `gorm:"column:aggregate_type"`
	AggregateID     string     `gorm:"column:aggregate_id"`
	TargetKey       string     `gorm:"column:target_key"`
	RetryGeneration int        `gorm:"column:retry_generation"`
	JobType         string     `gorm:"column:job_type"`
	Status          string     `gorm:"column:status"`
	RunAt           time.Time  `gorm:"column:run_at"`
	LeaseOwner      string     `gorm:"column:lease_owner"`
	LeaseUntil      *time.Time `gorm:"column:lease_until"`
	Attempts        int        `gorm:"column:attempts"`
	CreatedAt       time.Time  `gorm:"column:created_at"`
	UpdatedAt       time.Time  `gorm:"column:updated_at"`
	Payload         string     `gorm:"column:payload"`
}

type jobLogRow struct {
	ID              string    `gorm:"column:id;primaryKey"`
	JobID           string    `gorm:"column:job_id"`
	AggregateType   string    `gorm:"column:aggregate_type"`
	AggregateID     string    `gorm:"column:aggregate_id"`
	TargetKey       string    `gorm:"column:target_key"`
	RetryGeneration int       `gorm:"column:retry_generation"`
	SequenceNumber  int64     `gorm:"column:sequence_number"`
	Level           string    `gorm:"column:level"`
	Message         string    `gorm:"column:message"`
	CreatedAt       time.Time `gorm:"column:created_at"`
	Payload         string    `gorm:"column:payload"`
}

type scanLogListRow struct {
	ScanPayload *string `gorm:"column:scan_payload"`
	LogID       *string `gorm:"column:log_id"`
	LogPayload  *string `gorm:"column:log_payload"`
}

type cleanupLogListRow struct {
	CleanupTaskPayload *string `gorm:"column:cleanup_task_payload"`
	LogID              *string `gorm:"column:log_id"`
	LogPayload         *string `gorm:"column:log_payload"`
}

func (s *Store) CreateExecution(ctx context.Context, attempt execution.ExecutionAttempt) error {
	payload, err := encode(attempt)
	if err != nil {
		return err
	}
	row := executionAttemptRow{ID: string(attempt.ID), ConnectionID: string(attempt.ConnectionID), CleanupTaskID: attempt.CleanupTaskID, Status: string(attempt.Status), IdempotencyKey: attempt.IdempotencyKey, CreatedAt: attempt.CreatedAt, Payload: payload}
	return mapCreateError(s.db.WithContext(ctx).Table("execution_attempts").Create(&row).Error)
}

func (s *Store) GetExecution(ctx context.Context, id execution.ExecutionID) (execution.ExecutionAttempt, error) {
	var row executionAttemptRow
	if err := s.db.WithContext(ctx).Table("execution_attempts").Where("id = ?", string(id)).Take(&row).Error; err != nil {
		return execution.ExecutionAttempt{}, mapError(err)
	}
	return decode[execution.ExecutionAttempt](row.Payload)
}

func (s *Store) GetExecutionByIdempotencyKey(ctx context.Context, idempotencyKey string) (execution.ExecutionAttempt, error) {
	var row executionAttemptRow
	if err := s.db.WithContext(ctx).Table("execution_attempts").Where("idempotency_key = ?", idempotencyKey).Take(&row).Error; err != nil {
		return execution.ExecutionAttempt{}, mapError(err)
	}
	return decode[execution.ExecutionAttempt](row.Payload)
}

func (s *Store) ListExecutions(ctx context.Context, options persistence.ListOptions) (persistence.Page[execution.ExecutionAttempt], error) {
	limit := normalizeLimit(options.Limit)
	query := s.db.WithContext(ctx).Table("execution_attempts")
	if options.ConnectionID != "" {
		query = query.Where("connection_id = ?", string(options.ConnectionID))
	}
	if options.CleanupTaskID != "" {
		query = query.Where("cleanup_task_id = ?", options.CleanupTaskID)
	}
	if options.Cursor != "" {
		createdAt, id, err := decodeCursor(options.Cursor)
		if err != nil {
			return persistence.Page[execution.ExecutionAttempt]{}, err
		}
		query = query.Where("created_at > ? OR (created_at = ? AND id > ?)", createdAt, createdAt, id)
	}
	var rows []executionAttemptRow
	if err := query.Order("created_at ASC, id ASC").Limit(limit + 1).Find(&rows).Error; err != nil {
		return persistence.Page[execution.ExecutionAttempt]{}, err
	}
	return decodePage[executionAttemptRow, execution.ExecutionAttempt](rows, limit, func(row executionAttemptRow) time.Time { return row.CreatedAt }, func(row executionAttemptRow) string { return row.ID }, func(row executionAttemptRow) string { return row.Payload })
}

func (s *Store) ListCleanupTaskExecutions(
	ctx context.Context,
	connectionID asset.ConnectionID,
	cleanupTaskID string,
	options persistence.ListOptions,
) (persistence.Page[execution.ExecutionAttempt], error) {
	limit := normalizeLimit(options.Limit)
	parentTask := s.db.WithContext(ctx).
		Table("cleanup_tasks").
		Select("id").
		Where("id = ? AND connection_id = ?", strings.TrimSpace(cleanupTaskID), string(connectionID))
	selectedExecutions := s.db.WithContext(ctx).
		Table("execution_attempts").
		Select("id, created_at, payload").
		Where("cleanup_task_id = ? AND connection_id = ?", strings.TrimSpace(cleanupTaskID), string(connectionID))
	if options.Cursor != "" {
		createdAt, id, err := decodeCursor(options.Cursor)
		if err != nil {
			return persistence.Page[execution.ExecutionAttempt]{}, err
		}
		selectedExecutions = selectedExecutions.Where("created_at > ? OR (created_at = ? AND id > ?)", createdAt, createdAt, id)
	}
	selectedExecutions = selectedExecutions.Order("created_at ASC, id ASC").Limit(limit + 1)

	var rows []cleanupTaskExecutionRow
	err := s.db.WithContext(ctx).
		Table("(?) AS parent_task", parentTask).
		Select(`
			parent_task.id AS parent_cleanup_task_id,
			selected_executions.id AS execution_id,
			selected_executions.payload AS execution_payload`).
		Joins("LEFT JOIN (?) AS selected_executions ON 1 = 1", selectedExecutions).
		Order("selected_executions.created_at ASC, selected_executions.id ASC").
		Scan(&rows).Error
	if err != nil {
		return persistence.Page[execution.ExecutionAttempt]{}, err
	}
	if len(rows) == 0 {
		return persistence.Page[execution.ExecutionAttempt]{}, persistence.ErrNotFound
	}
	page := persistence.Page[execution.ExecutionAttempt]{Items: make([]execution.ExecutionAttempt, 0, min(limit, len(rows)))}
	for _, row := range rows {
		if row.ExecutionID == nil {
			continue
		}
		if len(page.Items) == limit {
			last := page.Items[limit-1]
			page.NextCursor = encodeCursor(last.CreatedAt, string(last.ID))
			break
		}
		if row.ExecutionPayload == nil {
			return persistence.Page[execution.ExecutionAttempt]{}, errors.New("decode repository payload: execution payload is missing")
		}
		value, decodeErr := decode[execution.ExecutionAttempt](*row.ExecutionPayload)
		if decodeErr != nil {
			return persistence.Page[execution.ExecutionAttempt]{}, decodeErr
		}
		page.Items = append(page.Items, value)
	}
	return page, nil
}

func (s *Store) UpdateExecution(ctx context.Context, attempt execution.ExecutionAttempt) error {
	payload, err := encode(attempt)
	if err != nil {
		return err
	}
	result := s.db.WithContext(ctx).Table("execution_attempts").Where("id = ?", string(attempt.ID)).Updates(map[string]any{"status": string(attempt.Status), "payload": payload})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return persistence.ErrNotFound
	}
	return nil
}

func (s *Store) LockExecution(ctx context.Context, id execution.ExecutionID) error {
	var row executionAttemptRow
	err := s.db.WithContext(ctx).
		Table("execution_attempts").
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Select("id").
		Where("id = ?", string(id)).
		Take(&row).Error
	return mapError(err)
}

func (s *Store) AppendAction(ctx context.Context, attempt execution.ActionAttempt) error {
	payload, err := encode(attempt)
	if err != nil {
		return err
	}
	row := actionAttemptRow{ID: string(attempt.ID), ExecutionID: string(attempt.ExecutionID), CleanupTaskStepID: attempt.CleanupTaskStepID, AssetID: string(attempt.AssetID), Status: string(attempt.Status), IdempotencyKey: attempt.IdempotencyKey, CreatedAt: attempt.CreatedAt, UpdatedAt: attempt.UpdatedAt, Payload: payload}
	return mapCreateError(s.db.WithContext(ctx).Table("action_attempts").Create(&row).Error)
}

func (s *Store) GetAction(ctx context.Context, id execution.ActionAttemptID) (execution.ActionAttempt, error) {
	var row actionAttemptRow
	if err := s.db.WithContext(ctx).Table("action_attempts").Where("id = ?", string(id)).Take(&row).Error; err != nil {
		return execution.ActionAttempt{}, mapError(err)
	}
	return decode[execution.ActionAttempt](row.Payload)
}

func (s *Store) GetActionByExecutionStep(ctx context.Context, executionID execution.ExecutionID, cleanupTaskStepID string) (execution.ActionAttempt, error) {
	var row actionAttemptRow
	if err := s.db.WithContext(ctx).Table("action_attempts").Where("execution_id = ? AND cleanup_task_step_id = ?", string(executionID), cleanupTaskStepID).Take(&row).Error; err != nil {
		return execution.ActionAttempt{}, mapError(err)
	}
	return decode[execution.ActionAttempt](row.Payload)
}

func (s *Store) ListActions(ctx context.Context, executionID execution.ExecutionID) ([]execution.ActionAttempt, error) {
	var rows []actionAttemptRow
	if err := s.db.WithContext(ctx).Table("action_attempts").Where("execution_id = ?", string(executionID)).Order("created_at ASC, id ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	return decodeRows[actionAttemptRow, execution.ActionAttempt](rows, func(row actionAttemptRow) string { return row.Payload })
}

func (s *Store) ListExecutionActions(
	ctx context.Context,
	connectionID asset.ConnectionID,
	executionID execution.ExecutionID,
) ([]execution.ActionAttempt, error) {
	parentExecution := s.db.WithContext(ctx).
		Table("execution_attempts").
		Select("id").
		Where("id = ? AND connection_id = ?", string(executionID), string(connectionID))
	var rows []executionActionRow
	err := s.db.WithContext(ctx).
		Table("(?) AS parent_execution", parentExecution).
		Select(`
			parent_execution.id AS parent_execution_id,
			action_attempts.id AS action_id,
			action_attempts.payload AS action_payload`).
		Joins("LEFT JOIN action_attempts ON action_attempts.execution_id = parent_execution.id").
		Order("action_attempts.created_at ASC, action_attempts.id ASC").
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, persistence.ErrNotFound
	}
	result := make([]execution.ActionAttempt, 0, len(rows))
	for _, row := range rows {
		if row.ActionID == nil {
			continue
		}
		if row.ActionPayload == nil {
			return nil, errors.New("decode repository payload: action payload is missing")
		}
		value, decodeErr := decode[execution.ActionAttempt](*row.ActionPayload)
		if decodeErr != nil {
			return nil, decodeErr
		}
		result = append(result, value)
	}
	return result, nil
}

func (s *Store) CountInFlightActions(ctx context.Context, executionID execution.ExecutionID) (int, error) {
	statuses := []string{
		string(execution.ActionIntentPersisted),
		string(execution.ActionInvoking),
		string(execution.ActionWaiting),
		string(execution.ActionReadingBack),
		string(execution.ActionReconciling),
	}
	var count int64
	err := s.db.WithContext(ctx).
		Table("action_attempts").
		Where("execution_id = ? AND status IN ?", string(executionID), statuses).
		Count(&count).Error
	return int(count), err
}

func (s *Store) UpdateAction(ctx context.Context, attempt execution.ActionAttempt) error {
	payload, err := encode(attempt)
	if err != nil {
		return err
	}
	result := s.db.WithContext(ctx).Table("action_attempts").Where("id = ?", string(attempt.ID)).Updates(map[string]any{"status": string(attempt.Status), "updated_at": attempt.UpdatedAt, "payload": payload})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return persistence.ErrNotFound
	}
	return nil
}

func (s *Store) AppendOutbox(ctx context.Context, event execution.OutboxEvent) error {
	payload, err := encode(event)
	if err != nil {
		return err
	}
	row := outboxEventRow{ID: string(event.ID), Topic: event.Topic, AggregateID: event.AggregateID, CreatedAt: event.CreatedAt, PublishedAt: event.PublishedAt, Payload: payload}
	return mapCreateError(s.db.WithContext(ctx).Table("outbox_events").Create(&row).Error)
}

func (s *Store) ListPendingOutbox(ctx context.Context, limit int) ([]execution.OutboxEvent, error) {
	limit = normalizeLimit(limit)
	var rows []outboxEventRow
	if err := s.db.WithContext(ctx).Table("outbox_events").Where("published_at IS NULL").Order("created_at ASC, id ASC").Limit(limit).Find(&rows).Error; err != nil {
		return nil, err
	}
	return decodeRows[outboxEventRow, execution.OutboxEvent](rows, func(row outboxEventRow) string { return row.Payload })
}

func (s *Store) MarkOutboxPublished(ctx context.Context, id execution.OutboxEventID, publishedAt time.Time) error {
	var row outboxEventRow
	if err := s.db.WithContext(ctx).Table("outbox_events").Where("id = ? AND published_at IS NULL", string(id)).Take(&row).Error; err != nil {
		return mapError(err)
	}
	event, err := decode[execution.OutboxEvent](row.Payload)
	if err != nil {
		return err
	}
	event.PublishedAt = &publishedAt
	payload, err := encode(event)
	if err != nil {
		return err
	}
	result := s.db.WithContext(ctx).Table("outbox_events").Where("id = ? AND published_at IS NULL", string(id)).Updates(map[string]any{"published_at": publishedAt, "payload": payload})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return persistence.ErrNotFound
	}
	return nil
}

func (s *Store) AppendAuditEvent(ctx context.Context, event execution.AuditEvent) error {
	if event.RequestID == "" {
		event.RequestID = requestmeta.RequestID(ctx)
	}
	payload, err := encode(event)
	if err != nil {
		return err
	}
	row := auditEventRow{ID: string(event.ID), ConnectionID: string(event.ConnectionID), Actor: event.Actor, Action: event.Action, TargetType: event.TargetType, TargetID: event.TargetID, CreatedAt: event.CreatedAt, Payload: payload}
	return mapCreateError(s.db.WithContext(ctx).Table("audit_events").Create(&row).Error)
}

func (s *Store) ListAuditEvents(ctx context.Context, options persistence.ListOptions) (persistence.Page[execution.AuditEvent], error) {
	limit := normalizeLimit(options.Limit)
	query := s.db.WithContext(ctx).Table("audit_events")
	if options.ConnectionID != "" {
		query = query.Where("connection_id = ?", string(options.ConnectionID))
	}
	if options.Cursor != "" {
		createdAt, id, err := decodeCursor(options.Cursor)
		if err != nil {
			return persistence.Page[execution.AuditEvent]{}, err
		}
		query = query.Where("created_at < ? OR (created_at = ? AND id < ?)", createdAt, createdAt, id)
	}
	var rows []auditEventRow
	if err := query.Order("created_at DESC, id DESC").Limit(limit + 1).Find(&rows).Error; err != nil {
		return persistence.Page[execution.AuditEvent]{}, err
	}
	page := persistence.Page[execution.AuditEvent]{Items: make([]execution.AuditEvent, 0, min(limit, len(rows)))}
	for index, row := range rows {
		if index == limit {
			last := rows[index-1]
			page.NextCursor = encodeCursor(last.CreatedAt, last.ID)
			break
		}
		event, err := decode[execution.AuditEvent](row.Payload)
		if err != nil {
			return persistence.Page[execution.AuditEvent]{}, err
		}
		normalizeLegacyCleanupActionRequestID(&event)
		page.Items = append(page.Items, event)
	}
	return page, nil
}

func normalizeLegacyCleanupActionRequestID(event *execution.AuditEvent) {
	if event == nil ||
		!strings.HasPrefix(event.Action, "cleanup.action.") ||
		strings.TrimSpace(event.RequestID) == "" {
		return
	}
	if _, migrated := event.Evidence["provider_request_id"]; migrated {
		return
	}
	if event.Evidence == nil {
		event.Evidence = make(map[string]any)
	}
	event.Evidence["provider_request_id"] = event.RequestID
	event.RequestID = ""
}

func (s *Store) Enqueue(ctx context.Context, job execution.Job) error {
	payload, err := encode(job)
	if err != nil {
		return err
	}
	var idempotencyKey *string
	if value := strings.TrimSpace(job.IdempotencyKey); value != "" {
		idempotencyKey = &value
	}
	row := jobRow{ID: string(job.ID), ConnectionID: string(job.ConnectionID), IdempotencyKey: idempotencyKey, AggregateType: job.AggregateType, AggregateID: job.AggregateID, TargetKey: job.TargetKey, RetryGeneration: job.RetryGeneration, JobType: string(job.Type), Status: string(job.Status), RunAt: job.RunAt, LeaseOwner: job.LeaseOwner, LeaseUntil: job.LeaseUntil, Attempts: job.Attempts, CreatedAt: job.CreatedAt, UpdatedAt: job.UpdatedAt, Payload: payload}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Table("jobs").Create(&row).Error; err != nil {
			return err
		}
		return touchScanTaskForJob(tx, job)
	})
	return mapCreateError(err)
}

func (s *Store) HasActiveJobs(ctx context.Context, connectionID asset.ConnectionID) (bool, error) {
	var count int64
	err := s.db.WithContext(ctx).Table("jobs").Where(
		"connection_id = ? AND status IN ?",
		string(connectionID),
		[]string{string(execution.JobPending), string(execution.JobRunning), string(execution.JobPaused)},
	).Count(&count).Error
	return count > 0, err
}

func (s *Store) GetJob(ctx context.Context, id execution.JobID) (execution.Job, error) {
	var row jobRow
	if err := s.db.WithContext(ctx).Table("jobs").Where("id = ?", string(id)).Take(&row).Error; err != nil {
		return execution.Job{}, mapError(err)
	}
	return decode[execution.Job](row.Payload)
}

func (s *Store) GetJobByIdempotencyKey(ctx context.Context, idempotencyKey string) (execution.Job, error) {
	var row jobRow
	if err := s.db.WithContext(ctx).Table("jobs").Where("idempotency_key = ?", strings.TrimSpace(idempotencyKey)).Take(&row).Error; err != nil {
		return execution.Job{}, mapError(err)
	}
	return decode[execution.Job](row.Payload)
}

func (s *Store) ListJobsByAggregate(ctx context.Context, aggregateType, aggregateID string) ([]execution.Job, error) {
	var rows []jobRow
	if err := s.db.WithContext(ctx).Table("jobs").Where("aggregate_type = ? AND aggregate_id = ?", strings.TrimSpace(aggregateType), strings.TrimSpace(aggregateID)).Order("created_at ASC, id ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	return decodeRows[jobRow, execution.Job](rows, func(row jobRow) string { return row.Payload })
}

func (s *Store) ListJobsByAggregates(ctx context.Context, aggregateType string, aggregateIDs []string) (map[string][]execution.Job, error) {
	result := make(map[string][]execution.Job, len(aggregateIDs))
	if len(aggregateIDs) == 0 {
		return result, nil
	}
	var rows []jobRow
	if err := s.db.WithContext(ctx).Table("jobs").
		Where("aggregate_type = ? AND aggregate_id IN ?", strings.TrimSpace(aggregateType), aggregateIDs).
		Order("aggregate_id ASC, created_at ASC, id ASC").
		Find(&rows).Error; err != nil {
		return nil, err
	}
	for _, row := range rows {
		job, err := decode[execution.Job](row.Payload)
		if err != nil {
			return nil, err
		}
		result[row.AggregateID] = append(result[row.AggregateID], job)
	}
	return result, nil
}

func (s *Store) UpdateJob(ctx context.Context, job execution.Job) error {
	payload, err := encode(job)
	if err != nil {
		return err
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Table("jobs").Where("id = ?", string(job.ID)).Updates(map[string]any{
			"status": job.Status, "run_at": job.RunAt, "lease_owner": job.LeaseOwner, "lease_until": job.LeaseUntil,
			"attempts": job.Attempts, "updated_at": job.UpdatedAt, "aggregate_type": job.AggregateType, "aggregate_id": job.AggregateID,
			"target_key": job.TargetKey, "retry_generation": job.RetryGeneration, "payload": payload,
		})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return persistence.ErrNotFound
		}
		return refreshScanTaskTiming(tx, job.AggregateType, job.AggregateID, job.UpdatedAt)
	})
}

func (s *Store) FindActiveByType(ctx context.Context, connectionID asset.ConnectionID, jobType execution.JobType) (execution.Job, error) {
	var row jobRow
	if err := s.db.WithContext(ctx).Table("jobs").Where(
		"connection_id = ? AND job_type = ? AND status IN ?",
		string(connectionID), string(jobType), []string{string(execution.JobPending), string(execution.JobRunning)},
	).Order("created_at ASC, id ASC").Take(&row).Error; err != nil {
		return execution.Job{}, mapError(err)
	}
	return decode[execution.Job](row.Payload)
}

func (s *Store) FindLatestByType(ctx context.Context, connectionID asset.ConnectionID, jobType execution.JobType) (execution.Job, error) {
	var row jobRow
	if err := s.db.WithContext(ctx).Table("jobs").Where(
		"connection_id = ? AND job_type = ?", string(connectionID), string(jobType),
	).Order("created_at DESC, id DESC").Take(&row).Error; err != nil {
		return execution.Job{}, mapError(err)
	}
	return decode[execution.Job](row.Payload)
}

func (s *Store) ClaimNext(ctx context.Context, owner string, now time.Time, leaseDuration time.Duration, allowedTypes ...execution.JobType) (execution.Job, error) {
	var claimed execution.Job
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		condition := "run_at <= ? AND (status = ? OR (status = ? AND lease_until <= ?))"
		var row jobRow
		query := tx.Table("jobs").Where(condition, now, string(execution.JobPending), string(execution.JobRunning), now)
		if len(allowedTypes) > 0 {
			values := make([]string, 0, len(allowedTypes))
			for _, jobType := range allowedTypes {
				values = append(values, string(jobType))
			}
			query = query.Where("job_type IN ?", values)
		}
		if err := query.Order("run_at ASC, id ASC").Take(&row).Error; err != nil {
			return mapError(err)
		}
		job, err := decode[execution.Job](row.Payload)
		if err != nil {
			return err
		}
		previousLeaseUntil := job.LeaseUntil
		startJobRun(&job, now, previousLeaseUntil)
		leaseUntil := now.Add(leaseDuration)
		job.Status = execution.JobRunning
		job.LeaseOwner = owner
		job.LeaseUntil = &leaseUntil
		job.Attempts++
		job.UpdatedAt = now
		payload, err := encode(job)
		if err != nil {
			return err
		}
		result := tx.Table("jobs").Where("id = ? AND "+condition, row.ID, now, string(execution.JobPending), string(execution.JobRunning), now).Updates(map[string]any{
			"status": string(job.Status), "lease_owner": owner, "lease_until": leaseUntil, "attempts": job.Attempts, "updated_at": now, "payload": payload,
		})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return persistence.ErrNotFound
		}
		if err := refreshScanTaskTiming(tx, job.AggregateType, job.AggregateID, now); err != nil {
			return err
		}
		claimed = job
		return nil
	})
	return claimed, err
}

func (s *Store) RenewLease(ctx context.Context, id execution.JobID, owner string, leaseUntil time.Time) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row jobRow
		if err := tx.Table("jobs").Where("id = ? AND status = ? AND lease_owner = ?", string(id), string(execution.JobRunning), owner).Take(&row).Error; err != nil {
			return mapError(err)
		}
		job, err := decode[execution.Job](row.Payload)
		if err != nil {
			return err
		}
		job.LeaseUntil = &leaseUntil
		job.UpdatedAt = time.Now().UTC()
		payload, err := encode(job)
		if err != nil {
			return err
		}
		result := tx.Table("jobs").Where("id = ? AND status = ? AND lease_owner = ?", string(id), string(execution.JobRunning), owner).Updates(map[string]any{"lease_until": leaseUntil, "updated_at": job.UpdatedAt, "payload": payload})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return persistence.ErrNotFound
		}
		return refreshScanTaskTiming(tx, job.AggregateType, job.AggregateID, job.UpdatedAt)
	})
}

func (s *Store) Reschedule(ctx context.Context, id execution.JobID, owner string, runAt time.Time, lastError string, updatedAt time.Time) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row jobRow
		if err := tx.Table("jobs").Where("id = ? AND status = ? AND lease_owner = ?", string(id), string(execution.JobRunning), owner).Take(&row).Error; err != nil {
			return mapError(err)
		}
		job, err := decode[execution.Job](row.Payload)
		if err != nil {
			return err
		}
		job.Status = execution.JobPending
		finishJobRun(&job, updatedAt)
		job.RunAt = runAt
		job.LastError = lastError
		job.UpdatedAt = updatedAt
		job.LeaseOwner = ""
		job.LeaseUntil = nil
		payload, err := encode(job)
		if err != nil {
			return err
		}
		result := tx.Table("jobs").Where("id = ? AND status = ? AND lease_owner = ?", string(id), string(execution.JobRunning), owner).Updates(map[string]any{
			"status": string(execution.JobPending), "run_at": runAt, "lease_owner": nil, "lease_until": nil, "updated_at": updatedAt, "payload": payload,
		})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return persistence.ErrNotFound
		}
		return refreshScanTaskTiming(tx, job.AggregateType, job.AggregateID, updatedAt)
	})
}

func (s *Store) Complete(ctx context.Context, id execution.JobID, owner string, status execution.JobStatus, lastError string, finishedAt time.Time) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row jobRow
		if err := tx.Table("jobs").Where("id = ? AND status = ? AND lease_owner = ?", string(id), string(execution.JobRunning), owner).Take(&row).Error; err != nil {
			return mapError(err)
		}
		job, err := decode[execution.Job](row.Payload)
		if err != nil {
			return err
		}
		job.Status = status
		finishJobRun(&job, finishedAt)
		job.LastError = lastError
		job.FinishedAt = &finishedAt
		job.UpdatedAt = finishedAt
		job.LeaseOwner = ""
		job.LeaseUntil = nil
		payload, err := encode(job)
		if err != nil {
			return err
		}
		result := tx.Table("jobs").Where("id = ? AND status = ? AND lease_owner = ?", string(id), string(execution.JobRunning), owner).Updates(map[string]any{
			"status": string(status), "lease_owner": nil, "lease_until": nil, "updated_at": finishedAt, "payload": payload,
		})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return persistence.ErrNotFound
		}
		return refreshScanTaskTiming(tx, job.AggregateType, job.AggregateID, finishedAt)
	})
}

func startJobRun(job *execution.Job, startedAt time.Time, previousLeaseUntil *time.Time) {
	if job == nil {
		return
	}
	if len(job.RunIntervals) > 0 {
		last := &job.RunIntervals[len(job.RunIntervals)-1]
		if last.FinishedAt == nil {
			finishedAt := startedAt
			if previousLeaseUntil != nil && previousLeaseUntil.Before(finishedAt) {
				finishedAt = *previousLeaseUntil
			}
			if finishedAt.Before(last.StartedAt) {
				finishedAt = last.StartedAt
			}
			last.FinishedAt = &finishedAt
		}
	}
	job.RunIntervals = append(job.RunIntervals, execution.JobRunInterval{StartedAt: startedAt})
}

func finishJobRun(job *execution.Job, finishedAt time.Time) {
	if job == nil || len(job.RunIntervals) == 0 {
		return
	}
	last := &job.RunIntervals[len(job.RunIntervals)-1]
	if last.FinishedAt != nil {
		return
	}
	if finishedAt.Before(last.StartedAt) {
		finishedAt = last.StartedAt
	}
	last.FinishedAt = &finishedAt
}

func touchScanTaskForJob(db *gorm.DB, job execution.Job) error {
	if job.AggregateType != "scan_task" || strings.TrimSpace(job.AggregateID) == "" {
		return nil
	}
	return touchScanTask(db, asset.ScanTaskID(job.AggregateID), job.UpdatedAt)
}

func refreshScanTaskTiming(db *gorm.DB, aggregateType, aggregateID string, calculatedAt time.Time) error {
	if aggregateType != "scan_task" || strings.TrimSpace(aggregateID) == "" {
		return nil
	}
	if calculatedAt.IsZero() {
		calculatedAt = time.Now().UTC()
	}
	var task scanRunRow
	if err := db.Table("scan_tasks").
		Select("id", "created_at", "updated_at").
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ?", aggregateID).
		Take(&task).Error; err != nil {
		return mapError(err)
	}
	var rows []jobRow
	if err := db.Table("jobs").
		Where("aggregate_type = ? AND aggregate_id = ?", aggregateType, aggregateID).
		Order("created_at ASC, id ASC").
		Find(&rows).Error; err != nil {
		return err
	}
	jobs, err := decodeRows[jobRow, execution.Job](rows, func(row jobRow) string { return row.Payload })
	if err != nil {
		return err
	}
	duration, recorded := execution.ActiveDuration(jobs, calculatedAt)
	active := false
	for _, job := range jobs {
		if job.Status == execution.JobRunning &&
			len(job.RunIntervals) > 0 &&
			job.RunIntervals[len(job.RunIntervals)-1].FinishedAt == nil {
			active = true
			break
		}
	}
	updatedAt := execution.LatestJobUpdate(jobs, task.CreatedAt)
	if task.UpdatedAt.After(updatedAt) {
		updatedAt = task.UpdatedAt
	}
	updates := map[string]any{
		"duration_ms":       duration.Milliseconds(),
		"duration_recorded": recorded,
		"duration_active":   active,
		"updated_at":        updatedAt,
	}
	if recorded {
		updates["duration_calculated_at"] = calculatedAt
	} else {
		updates["duration_calculated_at"] = nil
	}
	result := db.Table("scan_tasks").Where("id = ?", aggregateID).Updates(updates)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return persistence.ErrNotFound
	}
	return nil
}

func (s *Store) AppendLog(ctx context.Context, event execution.JobLog) error {
	payload, err := encode(event)
	if err != nil {
		return err
	}
	row := jobLogRow{ID: event.ID, JobID: string(event.JobID), AggregateType: event.AggregateType, AggregateID: event.AggregateID, TargetKey: event.TargetKey, RetryGeneration: event.RetryGeneration, SequenceNumber: event.Sequence, Level: event.Level, Message: event.Message, CreatedAt: event.CreatedAt, Payload: payload}
	return mapCreateError(s.db.WithContext(ctx).Table("job_logs").Create(&row).Error)
}

func (s *Store) ListLogs(ctx context.Context, jobID execution.JobID, afterSequence int64, limit int) ([]execution.JobLog, error) {
	limit = normalizeLimit(limit)
	var rows []jobLogRow
	if err := s.db.WithContext(ctx).Table("job_logs").Where("job_id = ? AND sequence_number > ?", string(jobID), afterSequence).Order("sequence_number ASC").Limit(limit).Find(&rows).Error; err != nil {
		return nil, err
	}
	return decodeRows[jobLogRow, execution.JobLog](rows, func(row jobLogRow) string { return row.Payload })
}

func (s *Store) ListLogsByAggregate(ctx context.Context, aggregateType, aggregateID, targetKey string, afterCreatedAt time.Time, afterID string, limit int) ([]execution.JobLog, error) {
	limit = normalizeLimit(limit)
	query := s.db.WithContext(ctx).Table("job_logs").Where("aggregate_type = ? AND aggregate_id = ?", aggregateType, aggregateID)
	if targetKey != "" {
		query = query.Where("target_key = ?", targetKey)
	}
	if !afterCreatedAt.IsZero() || afterID != "" {
		query = query.Where("created_at > ? OR (created_at = ? AND id > ?)", afterCreatedAt, afterCreatedAt, afterID)
	}
	var rows []jobLogRow
	if err := query.Order("created_at ASC, id ASC").Limit(limit).Find(&rows).Error; err != nil {
		return nil, err
	}
	return decodeRows[jobLogRow, execution.JobLog](rows, func(row jobLogRow) string { return row.Payload })
}

func (s *Store) ListLogsByAggregateBefore(ctx context.Context, aggregateType, aggregateID, targetKey string, beforeCreatedAt time.Time, beforeID string, limit int) ([]execution.JobLog, error) {
	limit = normalizeLimit(limit)
	query := s.db.WithContext(ctx).Table("job_logs").Where("aggregate_type = ? AND aggregate_id = ?", aggregateType, aggregateID)
	if targetKey != "" {
		query = query.Where("target_key = ?", targetKey)
	}
	if !beforeCreatedAt.IsZero() || beforeID != "" {
		query = query.Where("created_at < ? OR (created_at = ? AND id < ?)", beforeCreatedAt, beforeCreatedAt, beforeID)
	}
	var rows []jobLogRow
	if err := query.Order("created_at DESC, id DESC").Limit(limit).Find(&rows).Error; err != nil {
		return nil, err
	}
	logs, err := decodeRows[jobLogRow, execution.JobLog](rows, func(row jobLogRow) string { return row.Payload })
	if err != nil {
		return nil, err
	}
	slices.Reverse(logs)
	return logs, nil
}

func (s *Store) ListScanLogsBefore(
	ctx context.Context,
	connectionID asset.ConnectionID,
	scanID asset.ScanTaskID,
	targetKey string,
	beforeCreatedAt time.Time,
	beforeID string,
	limit int,
) (asset.ScanTask, []execution.JobLog, error) {
	parentScan := s.db.WithContext(ctx).
		Table("scan_tasks").
		Select("id, payload").
		Where("id = ? AND connection_id = ?", string(scanID), string(connectionID))
	selectedLogs := aggregateLogsBeforeQuery(
		s.db.WithContext(ctx),
		"scan_task",
		string(scanID),
		strings.TrimSpace(targetKey),
		beforeCreatedAt,
		beforeID,
		normalizeLimit(limit),
	)
	var rows []scanLogListRow
	err := s.db.WithContext(ctx).
		Table("(?) AS parent_scan", parentScan).
		Select(`
			parent_scan.payload AS scan_payload,
			selected_logs.id AS log_id,
			selected_logs.payload AS log_payload`).
		Joins("LEFT JOIN (?) AS selected_logs ON 1 = 1", selectedLogs).
		Order("selected_logs.created_at ASC, selected_logs.id ASC").
		Scan(&rows).Error
	if err != nil {
		return asset.ScanTask{}, nil, err
	}
	if len(rows) == 0 || rows[0].ScanPayload == nil {
		return asset.ScanTask{}, nil, persistence.ErrNotFound
	}
	task, err := decode[asset.ScanTask](*rows[0].ScanPayload)
	if err != nil {
		return asset.ScanTask{}, nil, err
	}
	logs, err := decodeJoinedLogs(rows, func(row scanLogListRow) (*string, *string) {
		return row.LogID, row.LogPayload
	})
	return task, logs, err
}

func (s *Store) ListCleanupLogsAfter(
	ctx context.Context,
	connectionID asset.ConnectionID,
	cleanupTaskID plan.CleanupTaskID,
	filter persistence.CleanupLogFilter,
	afterCreatedAt time.Time,
	afterID string,
	limit int,
) (plan.CleanupTask, []execution.JobLog, error) {
	return s.listCleanupLogs(
		ctx,
		connectionID,
		cleanupTaskID,
		filter,
		afterCreatedAt,
		afterID,
		limit,
		false,
	)
}

func (s *Store) ListCleanupLogsBefore(
	ctx context.Context,
	connectionID asset.ConnectionID,
	cleanupTaskID plan.CleanupTaskID,
	filter persistence.CleanupLogFilter,
	beforeCreatedAt time.Time,
	beforeID string,
	limit int,
) (plan.CleanupTask, []execution.JobLog, error) {
	return s.listCleanupLogs(
		ctx,
		connectionID,
		cleanupTaskID,
		filter,
		beforeCreatedAt,
		beforeID,
		limit,
		true,
	)
}

func (s *Store) listCleanupLogs(
	ctx context.Context,
	connectionID asset.ConnectionID,
	cleanupTaskID plan.CleanupTaskID,
	filter persistence.CleanupLogFilter,
	cursorCreatedAt time.Time,
	cursorID string,
	limit int,
	before bool,
) (plan.CleanupTask, []execution.JobLog, error) {
	parentTask := s.db.WithContext(ctx).
		Table("cleanup_tasks").
		Select("id, payload").
		Where("id = ? AND connection_id = ?", string(cleanupTaskID), string(connectionID))
	selectedLogs := cleanupLogsCursorQuery(
		s.db.WithContext(ctx),
		connectionID,
		string(cleanupTaskID),
		filter,
		cursorCreatedAt,
		cursorID,
		normalizeLimit(limit),
		before,
	)
	var rows []cleanupLogListRow
	err := s.db.WithContext(ctx).
		Table("(?) AS parent_task", parentTask).
		Select(`
			parent_task.payload AS cleanup_task_payload,
			selected_logs.id AS log_id,
			selected_logs.payload AS log_payload`).
		Joins("LEFT JOIN (?) AS selected_logs ON 1 = 1", selectedLogs).
		Order("selected_logs.created_at ASC, selected_logs.id ASC").
		Scan(&rows).Error
	if err != nil {
		return plan.CleanupTask{}, nil, err
	}
	if len(rows) == 0 || rows[0].CleanupTaskPayload == nil {
		return plan.CleanupTask{}, nil, persistence.ErrNotFound
	}
	cleanupTask, err := decode[plan.CleanupTask](*rows[0].CleanupTaskPayload)
	if err != nil {
		return plan.CleanupTask{}, nil, err
	}
	logs, err := decodeJoinedLogs(rows, func(row cleanupLogListRow) (*string, *string) {
		return row.LogID, row.LogPayload
	})
	return cleanupTask, logs, err
}

func cleanupLogsCursorQuery(
	db *gorm.DB,
	connectionID asset.ConnectionID,
	cleanupTaskID string,
	filter persistence.CleanupLogFilter,
	cursorCreatedAt time.Time,
	cursorID string,
	limit int,
	before bool,
) *gorm.DB {
	query := db.
		Table("job_logs").
		Select("job_logs.id, job_logs.created_at, job_logs.payload").
		Joins(
			"LEFT JOIN assets ON assets.id = job_logs.target_key AND assets.connection_id = ?",
			string(connectionID),
		).
		Where("job_logs.aggregate_type = ? AND job_logs.aggregate_id = ?", "cleanup_task", cleanupTaskID)
	if resourceID := strings.ToLower(strings.TrimSpace(filter.ResourceID)); resourceID != "" {
		pattern := "%" + escapeLike(resourceID) + "%"
		query = query.Where(
			"(LOWER(assets.native_id) LIKE ? ESCAPE '\\' OR LOWER(job_logs.target_key) LIKE ? ESCAPE '\\')",
			pattern,
			pattern,
		)
	}
	if len(filter.ResourceKindIDs) > 0 {
		resourceKindIDs := make([]string, 0, len(filter.ResourceKindIDs))
		seen := make(map[asset.ResourceKindID]struct{}, len(filter.ResourceKindIDs))
		for _, resourceKindID := range filter.ResourceKindIDs {
			resourceKindID = asset.ResourceKindID(strings.TrimSpace(string(resourceKindID)))
			if resourceKindID == "" {
				continue
			}
			if _, exists := seen[resourceKindID]; exists {
				continue
			}
			seen[resourceKindID] = struct{}{}
			resourceKindIDs = append(resourceKindIDs, string(resourceKindID))
		}
		if len(resourceKindIDs) > 0 {
			query = query.Where("assets.resource_kind_id IN ?", resourceKindIDs)
		}
	}
	if !cursorCreatedAt.IsZero() || cursorID != "" {
		if before {
			query = query.Where(
				"job_logs.created_at < ? OR (job_logs.created_at = ? AND job_logs.id < ?)",
				cursorCreatedAt,
				cursorCreatedAt,
				cursorID,
			)
		} else {
			query = query.Where(
				"job_logs.created_at > ? OR (job_logs.created_at = ? AND job_logs.id > ?)",
				cursorCreatedAt,
				cursorCreatedAt,
				cursorID,
			)
		}
	}
	if before {
		return query.Order("job_logs.created_at DESC, job_logs.id DESC").Limit(limit)
	}
	return query.Order("job_logs.created_at ASC, job_logs.id ASC").Limit(limit)
}

func aggregateLogsBeforeQuery(
	db *gorm.DB,
	aggregateType string,
	aggregateID string,
	targetKey string,
	beforeCreatedAt time.Time,
	beforeID string,
	limit int,
) *gorm.DB {
	query := db.
		Table("job_logs").
		Select("id, created_at, payload").
		Where("aggregate_type = ? AND aggregate_id = ?", aggregateType, aggregateID)
	if targetKey != "" {
		query = query.Where("target_key = ?", targetKey)
	}
	if !beforeCreatedAt.IsZero() || beforeID != "" {
		query = query.Where("created_at < ? OR (created_at = ? AND id < ?)", beforeCreatedAt, beforeCreatedAt, beforeID)
	}
	return query.Order("created_at DESC, id DESC").Limit(limit)
}

func decodeJoinedLogs[T any](rows []T, fields func(T) (*string, *string)) ([]execution.JobLog, error) {
	logs := make([]execution.JobLog, 0, len(rows))
	for _, row := range rows {
		id, payload := fields(row)
		if id == nil {
			continue
		}
		if payload == nil {
			return nil, errors.New("decode repository payload: job log payload is missing")
		}
		value, err := decode[execution.JobLog](*payload)
		if err != nil {
			return nil, err
		}
		logs = append(logs, value)
	}
	return logs, nil
}
