// Package schedule describes recurring scans: when they run, what they cover
// and how each planned run turned out.
package schedule

import (
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
)

type ID string

type RunID string

// Actor recorded on scans, audit events and runs that the scheduler starts
// without a person asking.
const Actor = "scheduler"

type OverlapPolicy string

const (
	// OverlapSkip records a skipped run when the connection is still scanning.
	OverlapSkip OverlapPolicy = "skip"
	// OverlapWait holds the run until the connection's scan finishes.
	OverlapWait OverlapPolicy = "wait"
)

type MissedPolicy string

const (
	// MissedCatchUp runs once when the server comes back after a missed time.
	MissedCatchUp MissedPolicy = "catch_up"
	// MissedSkip records the missed time and waits for the next one.
	MissedSkip MissedPolicy = "skip"
)

type Rules struct {
	Overlap            OverlapPolicy `json:"overlap"`
	Missed             MissedPolicy  `json:"missed"`
	RetryFailedTargets bool          `json:"retry_failed_targets"`
	// PauseAfterFailures disables the schedule after this many consecutive
	// scans that failed as a whole or could not start.
	PauseAfterFailures int `json:"pause_after_failures"`
}

func DefaultRules() Rules {
	return Rules{Overlap: OverlapSkip, Missed: MissedCatchUp, RetryFailedTargets: true, PauseAfterFailures: 3}
}

type NetworkTarget struct {
	Kind           asset.ScanTargetKind `json:"kind"`
	RegionID       string               `json:"region_id"`
	NativeID       string               `json:"native_id"`
	Name           string               `json:"name,omitempty"`
	ParentNativeID string               `json:"parent_native_id,omitempty"`
}

// Scope mirrors what a person picks when starting a scan by hand.
type Scope struct {
	ScopeMode       asset.ScanScopeMode    `json:"scope_mode"`
	RegionIDs       []string               `json:"region_ids,omitempty"`
	NetworkTargets  []NetworkTarget        `json:"network_targets,omitempty"`
	ResourceKindIDs []asset.ResourceKindID `json:"resource_kind_ids,omitempty"`
}

// Complete reports whether a scan of this scope refreshes the whole inventory:
// every enabled region plus global resources, all resource types.
func (s Scope) Complete() bool {
	return s.ScopeMode == asset.ScanAllActiveRegions && len(s.ResourceKindIDs) == 0
}

type PauseReason string

const (
	PauseConsecutiveFailures PauseReason = "consecutive_failures"
	PauseConnectionRemoved   PauseReason = "connection_removed"
)

type ScanSchedule struct {
	ID           ID                 `json:"id"`
	ConnectionID asset.ConnectionID `json:"connection_id"`
	// Name may be empty for the default schedule; clients show a localized
	// default name.
	Name      string     `json:"name"`
	Enabled   bool       `json:"enabled"`
	Scope     Scope      `json:"scope"`
	Frequency Frequency  `json:"frequency"`
	Rules     Rules      `json:"rules"`
	NextRunAt *time.Time `json:"next_run_at,omitempty"`
	// PauseReason is set when Steward, not a person, turned the schedule off.
	PauseReason         PauseReason `json:"pause_reason,omitempty"`
	PauseDetail         string      `json:"pause_detail,omitempty"`
	PausedAt            *time.Time  `json:"paused_at,omitempty"`
	ConsecutiveFailures int         `json:"consecutive_failures"`
	CreatedBy           string      `json:"created_by"`
	UpdatedBy           string      `json:"updated_by"`
	CreatedAt           time.Time   `json:"created_at"`
	UpdatedAt           time.Time   `json:"updated_at"`
	Revision            uint64      `json:"-"`
}

type Trigger string

const (
	TriggerSchedule Trigger = "schedule"
	// TriggerCatchUp runs a time the server missed while it was not running.
	TriggerCatchUp Trigger = "catch_up"
	TriggerManual  Trigger = "manual"
)

type Outcome string

const (
	OutcomeStarting Outcome = "starting"
	OutcomeStarted  Outcome = "started"
	OutcomeSkipped  Outcome = "skipped"
	OutcomeFailed   Outcome = "failed_to_start"
)

type SkipReason string

const (
	SkipOverlap SkipReason = "overlap"
	SkipMissed  SkipReason = "missed"
)

// Run is one planned time of a schedule and what happened at it, including
// times that did not start a scan.
type Run struct {
	ID           RunID              `json:"id"`
	ScheduleID   ID                 `json:"schedule_id"`
	ConnectionID asset.ConnectionID `json:"connection_id"`
	PlannedAt    time.Time          `json:"planned_at"`
	Trigger      Trigger            `json:"trigger"`
	Outcome      Outcome            `json:"outcome"`
	SkipReason   SkipReason         `json:"skip_reason,omitempty"`
	// BlockingScanID is the scan that caused an overlap skip.
	BlockingScanID asset.ScanTaskID `json:"blocking_scan_id,omitempty"`
	// Error explains why a scan could not start.
	Error       string           `json:"error,omitempty"`
	ErrorCode   string           `json:"error_code,omitempty"`
	ScanTaskID  asset.ScanTaskID `json:"scan_task_id,omitempty"`
	Actor       string           `json:"actor"`
	AutoRetried bool             `json:"auto_retried"`
	// FailedItems and FailureSummary describe what still failed when the run
	// settled.
	FailedItems    int    `json:"failed_items,omitempty"`
	FailureSummary string `json:"failure_summary,omitempty"`
	// Settled is set once the scan reached its final status and the schedule's
	// failure count and notifications were updated.
	Settled     bool             `json:"settled"`
	FinalStatus asset.ScanStatus `json:"final_status,omitempty"`
	CreatedAt   time.Time        `json:"created_at"`
	UpdatedAt   time.Time        `json:"updated_at"`
}

// Settings are workspace-wide defaults for scheduled scans.
type Settings struct {
	// DefaultScheduleEnabled creates a daily complete scan for each connection
	// the first time it passes validation.
	DefaultScheduleEnabled bool `json:"default_schedule_enabled"`
	// RetentionDays bounds how long finished scans, scheduled or manual, are
	// kept. Zero keeps them forever.
	RetentionDays   int       `json:"retention_days"`
	DefaultTimezone string    `json:"default_timezone,omitempty"`
	UpdatedBy       string    `json:"updated_by,omitempty"`
	UpdatedAt       time.Time `json:"updated_at"`
}

func DefaultSettings() Settings {
	return Settings{RetentionDays: 30}
}
