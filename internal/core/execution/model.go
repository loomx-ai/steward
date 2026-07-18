package execution

import (
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
)

type ExecutionID string
type ActionAttemptID string
type AuditEventID string
type OutboxEventID string

type ExecutionStatus string

const (
	ExecutionPending     ExecutionStatus = "pending"
	ExecutionRunning     ExecutionStatus = "running"
	ExecutionWaiting     ExecutionStatus = "waiting"
	ExecutionReconciling ExecutionStatus = "reconciling"
	ExecutionPausing     ExecutionStatus = "pausing"
	ExecutionPaused      ExecutionStatus = "paused"
	ExecutionSucceeded   ExecutionStatus = "succeeded"
	ExecutionFailed      ExecutionStatus = "failed"
	ExecutionCanceled    ExecutionStatus = "canceled"
)

type ActionStatus string

const (
	ActionPending         ActionStatus = "pending"
	ActionIntentPersisted ActionStatus = "intent_persisted"
	ActionInvoking        ActionStatus = "invoking"
	ActionWaiting         ActionStatus = "waiting"
	ActionReadingBack     ActionStatus = "reading_back"
	ActionReconciling     ActionStatus = "reconciling"
	ActionSucceeded       ActionStatus = "succeeded"
	ActionSkipped         ActionStatus = "skipped"
	ActionFailed          ActionStatus = "failed"
)

type ErrorCategory string

const (
	ErrorNotFound            ErrorCategory = "not_found"
	ErrorConflict            ErrorCategory = "conflict"
	ErrorDependencyViolation ErrorCategory = "dependency_violation"
	ErrorProtected           ErrorCategory = "protected"
	ErrorPermissionDenied    ErrorCategory = "permission_denied"
	ErrorThrottled           ErrorCategory = "throttled"
	ErrorRetryable           ErrorCategory = "retryable"
	ErrorInvalidRequest      ErrorCategory = "invalid_request"
	ErrorProviderFailure     ErrorCategory = "provider_failure"
	ErrorUnsupported         ErrorCategory = "unsupported"
	ErrorUnknown             ErrorCategory = "unknown"
)

type ProviderError struct {
	Category  ErrorCategory  `json:"category"`
	Code      string         `json:"code,omitempty"`
	Message   string         `json:"message"`
	RequestID string         `json:"request_id,omitempty"`
	Summary   map[string]any `json:"summary,omitempty"`
}

type ExecutionAttempt struct {
	ID                     ExecutionID        `json:"id"`
	ConnectionID           asset.ConnectionID `json:"connection_id"`
	CleanupTaskID          string             `json:"cleanup_task_id"`
	Status                 ExecutionStatus    `json:"status"`
	PausedFrom             ExecutionStatus    `json:"paused_from,omitempty"`
	Concurrency            int                `json:"concurrency"`
	RequestedBy            string             `json:"requested_by"`
	IdempotencyKey         string             `json:"idempotency_key"`
	ContinueIdempotencyKey string             `json:"continue_idempotency_key,omitempty"`
	ContinueCount          int                `json:"continue_count,omitempty"`
	CreatedAt              time.Time          `json:"created_at"`
	StartedAt              *time.Time         `json:"started_at,omitempty"`
	FinishedAt             *time.Time         `json:"finished_at,omitempty"`
	FailureReason          string             `json:"failure_reason,omitempty"`
}

type ActionAttempt struct {
	ID                     ActionAttemptID `json:"id"`
	ExecutionID            ExecutionID     `json:"execution_id"`
	CleanupTaskStepID      string          `json:"cleanup_task_step_id"`
	AssetID                asset.AssetID   `json:"asset_id"`
	Action                 string          `json:"action"`
	Status                 ActionStatus    `json:"status"`
	IdempotencyKey         string          `json:"idempotency_key"`
	SpecBundleRevision     string          `json:"spec_bundle_revision"`
	SpecHash               string          `json:"spec_hash"`
	Request                map[string]any  `json:"request"`
	PreflightEvidence      map[string]any  `json:"preflight_evidence,omitempty"`
	ProviderRequestID      string          `json:"provider_request_id,omitempty"`
	ProviderOperationID    string          `json:"provider_operation_id,omitempty"`
	ProviderResult         map[string]any  `json:"provider_result,omitempty"`
	PollIntervalSeconds    int             `json:"poll_interval_seconds,omitempty"`
	Readback               map[string]any  `json:"readback,omitempty"`
	ProviderError          *ProviderError  `json:"provider_error,omitempty"`
	DeletionCheckStartedAt *time.Time      `json:"deletion_check_started_at,omitempty"`
	SkipReason             string          `json:"skip_reason,omitempty"`
	FailedFrom             ActionStatus    `json:"failed_from,omitempty"`
	ResumeStatus           ActionStatus    `json:"resume_status,omitempty"`
	CreatedAt              time.Time       `json:"created_at"`
	UpdatedAt              time.Time       `json:"updated_at"`
	FinishedAt             *time.Time      `json:"finished_at,omitempty"`
}

type AuditEvent struct {
	ID           AuditEventID       `json:"id"`
	ConnectionID asset.ConnectionID `json:"connection_id,omitempty"`
	Actor        string             `json:"actor"`
	Action       string             `json:"action"`
	TargetType   string             `json:"target_type"`
	TargetID     string             `json:"target_id"`
	Result       string             `json:"result"`
	RequestID    string             `json:"request_id,omitempty"`
	Evidence     map[string]any     `json:"evidence,omitempty"`
	CreatedAt    time.Time          `json:"created_at"`
}

type OutboxEvent struct {
	ID          OutboxEventID  `json:"id"`
	Topic       string         `json:"topic"`
	AggregateID string         `json:"aggregate_id"`
	Payload     map[string]any `json:"payload"`
	CreatedAt   time.Time      `json:"created_at"`
	PublishedAt *time.Time     `json:"published_at,omitempty"`
}
