package execution

import (
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
)

type JobID string

type JobType string

const (
	JobScan          JobType = "scan"
	JobGraph         JobType = "graph"
	JobExecute       JobType = "execute"
	JobWait          JobType = "wait"
	JobReconcile     JobType = "reconcile"
	JobRegionRefresh JobType = "region_refresh"
)

type JobStatus string

const (
	JobPending   JobStatus = "pending"
	JobRunning   JobStatus = "running"
	JobPaused    JobStatus = "paused"
	JobSucceeded JobStatus = "succeeded"
	JobFailed    JobStatus = "failed"
	JobCanceled  JobStatus = "canceled"
)

type Job struct {
	ID              JobID              `json:"id"`
	ConnectionID    asset.ConnectionID `json:"connection_id,omitempty"`
	IdempotencyKey  string             `json:"idempotency_key,omitempty"`
	AggregateType   string             `json:"aggregate_type,omitempty"`
	AggregateID     string             `json:"aggregate_id,omitempty"`
	TargetKey       string             `json:"target_key,omitempty"`
	RetryGeneration int                `json:"retry_generation,omitempty"`
	Type            JobType            `json:"type"`
	Status          JobStatus          `json:"status"`
	Payload         map[string]any     `json:"payload"`
	RunAt           time.Time          `json:"run_at"`
	LeaseOwner      string             `json:"lease_owner,omitempty"`
	LeaseUntil      *time.Time         `json:"lease_until,omitempty"`
	Attempts        int                `json:"attempts"`
	LastError       string             `json:"last_error,omitempty"`
	CreatedAt       time.Time          `json:"created_at"`
	UpdatedAt       time.Time          `json:"updated_at"`
	FinishedAt      *time.Time         `json:"finished_at,omitempty"`
	RunIntervals    []JobRunInterval   `json:"run_intervals,omitempty"`
}

// JobRunInterval records time during which a worker actually owned and
// processed a job. Time spent pending, paused, or waiting to retry is
// intentionally not represented.
type JobRunInterval struct {
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
}

type JobLog struct {
	ID              string         `json:"id"`
	JobID           JobID          `json:"job_id"`
	AggregateType   string         `json:"aggregate_type,omitempty"`
	AggregateID     string         `json:"aggregate_id,omitempty"`
	TargetKey       string         `json:"target_key,omitempty"`
	RetryGeneration int            `json:"retry_generation,omitempty"`
	Sequence        int64          `json:"sequence"`
	Kind            JobLogKind     `json:"kind"`
	Level           string         `json:"level"`
	Message         string         `json:"message"`
	Payload         map[string]any `json:"payload,omitempty"`
	CreatedAt       time.Time      `json:"created_at"`
}

type JobStatusError struct {
	Status JobStatus
	Cause  error
}

func (e *JobStatusError) Error() string {
	if e == nil || e.Cause == nil {
		return "job stopped by aggregate control"
	}
	return e.Cause.Error()
}

func (e *JobStatusError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}
