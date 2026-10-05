// Package scheduling runs recurring scans: it starts scans at their planned
// times, applies each schedule's rules and keeps scan history bounded.
package scheduling

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"strings"
	"time"

	"github.com/loomx-ai/steward/internal/app/inventory"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/core/schedule"
	"github.com/loomx-ai/steward/internal/idgen"
	"github.com/loomx-ai/steward/internal/persistence"
)

const (
	settingsKey = "scan_schedule_settings"
	// missedGrace separates a run the server missed while it was stopped from
	// one it is merely picking up on its next tick.
	missedGrace = 2 * time.Minute
	// startTimeout settles runs whose scan never started because the server
	// stopped between claiming the time and creating the scan.
	startTimeout      = 10 * time.Minute
	tickInterval      = 30 * time.Second
	retentionInterval = time.Hour
	maxNameLength     = 100
	maxFailureDetail  = 300
)

// CloudMinInterval is the shortest schedule Steward Cloud allows; hosted
// workspaces share capacity. Self-hosted servers allow hourly schedules.
const (
	CloudMinInterval      = 6 * time.Hour
	SelfHostedMinInterval = time.Hour
)

type ScanCreator interface {
	Create(context.Context, inventory.ScanCreationRequest) (inventory.ScanCreation, error)
}

type ScanRetrier interface {
	Retry(context.Context, asset.ScanTaskID, string) (asset.ScanTask, error)
}

type EventKind string

const (
	EventScanFailed     EventKind = "scan_failed"
	EventScanPartial    EventKind = "scan_partial"
	EventSchedulePaused EventKind = "schedule_paused"
)

// Event is something about a scheduled scan that people may want to hear
// about without opening Steward.
type Event struct {
	Kind       EventKind             `json:"kind"`
	Schedule   schedule.ScanSchedule `json:"schedule"`
	Connection asset.CloudConnection `json:"connection"`
	ScanTaskID asset.ScanTaskID      `json:"scan_task_id,omitempty"`
	Detail     string                `json:"detail,omitempty"`
	OccurredAt time.Time             `json:"occurred_at"`
}

type Notifier interface {
	Notify(context.Context, Event)
}

type Options struct {
	MinInterval time.Duration
	Clock       func() time.Time
	Notifier    Notifier
}

type Service struct {
	repositories  persistence.Repositories
	scans         ScanCreator
	retrier       ScanRetrier
	notifier      Notifier
	minInterval   time.Duration
	clock         func() time.Time
	startedAt     time.Time
	lastRetention time.Time
}

func NewService(repositories persistence.Repositories, scans ScanCreator, retrier ScanRetrier, options Options) (*Service, error) {
	if repositories == nil || scans == nil || retrier == nil {
		return nil, fmt.Errorf("scheduling requires repositories, a scan creator and a scan retrier")
	}
	clock := options.Clock
	if clock == nil {
		clock = func() time.Time { return time.Now().UTC() }
	}
	minInterval := options.MinInterval
	if minInterval <= 0 {
		minInterval = SelfHostedMinInterval
	}
	return &Service{
		repositories: repositories, scans: scans, retrier: retrier, notifier: options.Notifier,
		minInterval: minInterval, clock: clock, startedAt: clock(),
	}, nil
}

func (s *Service) MinInterval() time.Duration { return s.minInterval }

// InvalidError is a request the caller can fix.
type InvalidError struct {
	Code    string
	Message string
}

func (e *InvalidError) Error() string { return e.Message }

func invalid(code, format string, values ...any) error {
	return &InvalidError{Code: code, Message: fmt.Sprintf(format, values...)}
}

type Input struct {
	Name      string             `json:"name"`
	Enabled   *bool              `json:"enabled,omitempty"`
	Scope     schedule.Scope     `json:"scope"`
	Frequency schedule.Frequency `json:"frequency"`
	Rules     *schedule.Rules    `json:"rules,omitempty"`
}

func (s *Service) compile(frequency schedule.Frequency, now time.Time) (schedule.Compiled, error) {
	compiled, err := frequency.Compile(s.minInterval, now)
	switch {
	case errors.Is(err, schedule.ErrIntervalTooShort):
		return schedule.Compiled{}, invalid("schedule.interval_too_short", "%s", err.Error())
	case err != nil:
		return schedule.Compiled{}, invalid("schedule.frequency_invalid", "%s", err.Error())
	}
	return compiled, nil
}

// Preview lists the next run times of a frequency without saving anything.
func (s *Service) Preview(frequency schedule.Frequency, count int) ([]time.Time, error) {
	now := s.clock()
	compiled, err := s.compile(frequency, now)
	if err != nil {
		return nil, err
	}
	return compiled.Upcoming(now, count), nil
}

func validateInput(input Input) (Input, error) {
	input.Name = strings.TrimSpace(input.Name)
	if len([]rune(input.Name)) > maxNameLength {
		return Input{}, invalid("schedule.name_too_long", "the name can have at most %d characters", maxNameLength)
	}
	scope := input.Scope
	switch scope.ScopeMode {
	case asset.ScanAllActiveRegions:
		if len(scope.RegionIDs) > 0 || len(scope.NetworkTargets) > 0 {
			return Input{}, invalid("schedule.scope_invalid", "a scan of all enabled regions does not take regions or networks")
		}
	case asset.ScanSelectedRegions:
		if len(scope.RegionIDs) == 0 || len(scope.NetworkTargets) > 0 {
			return Input{}, invalid("schedule.scope_invalid", "pick at least one region")
		}
	case asset.ScanSelectedNetworks:
		if len(scope.NetworkTargets) == 0 || len(scope.RegionIDs) > 0 || len(scope.ResourceKindIDs) > 0 {
			return Input{}, invalid("schedule.scope_invalid", "pick at least one network; resource types follow the networks")
		}
	default:
		return Input{}, invalid("schedule.scope_invalid", "unknown scope mode %q", scope.ScopeMode)
	}
	rules := schedule.DefaultRules()
	if input.Rules != nil {
		rules = *input.Rules
	}
	if rules.Overlap != schedule.OverlapSkip && rules.Overlap != schedule.OverlapWait {
		return Input{}, invalid("schedule.rules_invalid", "overlap must be skip or wait")
	}
	if rules.Missed != schedule.MissedCatchUp && rules.Missed != schedule.MissedSkip {
		return Input{}, invalid("schedule.rules_invalid", "missed must be catch_up or skip")
	}
	if rules.PauseAfterFailures < 1 || rules.PauseAfterFailures > 10 {
		return Input{}, invalid("schedule.rules_invalid", "pause_after_failures must be 1 to 10")
	}
	input.Rules = &rules
	return input, nil
}

func (s *Service) Create(ctx context.Context, connectionID asset.ConnectionID, input Input, actor string) (schedule.ScanSchedule, error) {
	input, err := validateInput(input)
	if err != nil {
		return schedule.ScanSchedule{}, err
	}
	now := s.clock()
	compiled, err := s.compile(input.Frequency, now)
	if err != nil {
		return schedule.ScanSchedule{}, err
	}
	enabled := input.Enabled == nil || *input.Enabled
	value := schedule.ScanSchedule{
		ID: schedule.ID(idgen.MustNew("sch")), ConnectionID: connectionID, Name: input.Name, Enabled: enabled,
		Scope: input.Scope, Frequency: input.Frequency, Rules: *input.Rules,
		CreatedBy: actor, UpdatedBy: actor, CreatedAt: now, UpdatedAt: now,
	}
	if enabled {
		next := compiled.Next(now)
		value.NextRunAt = &next
	}
	err = s.repositories.WithTx(ctx, func(repositories persistence.Repositories) error {
		if err := repositories.Schedules().CreateSchedule(ctx, value); err != nil {
			return err
		}
		return audit(ctx, repositories, value, actor, "schedule.create", "created", scheduleEvidence(value), now)
	})
	if err != nil {
		return schedule.ScanSchedule{}, err
	}
	value.Revision = 1
	return value, nil
}

func (s *Service) Get(ctx context.Context, connectionID asset.ConnectionID, id schedule.ID) (schedule.ScanSchedule, error) {
	value, err := s.repositories.Schedules().GetSchedule(ctx, id)
	if err == nil && value.ConnectionID != connectionID {
		err = persistence.ErrNotFound
	}
	return value, err
}

func (s *Service) Update(ctx context.Context, connectionID asset.ConnectionID, id schedule.ID, input Input, actor string) (schedule.ScanSchedule, error) {
	input, err := validateInput(input)
	if err != nil {
		return schedule.ScanSchedule{}, err
	}
	now := s.clock()
	compiled, err := s.compile(input.Frequency, now)
	if err != nil {
		return schedule.ScanSchedule{}, err
	}
	return s.mutate(ctx, connectionID, id, actor, "schedule.update", func(value *schedule.ScanSchedule) error {
		value.Name = input.Name
		value.Scope = input.Scope
		value.Frequency = input.Frequency
		value.Rules = *input.Rules
		if input.Enabled != nil {
			setEnabled(value, *input.Enabled)
		}
		if value.Enabled {
			next := compiled.Next(now)
			value.NextRunAt = &next
		}
		return nil
	})
}

func setEnabled(value *schedule.ScanSchedule, enabled bool) {
	value.Enabled = enabled
	value.PauseReason = ""
	value.PauseDetail = ""
	value.PausedAt = nil
	if enabled {
		value.ConsecutiveFailures = 0
	} else {
		value.NextRunAt = nil
	}
}

// SetEnabled turns a schedule on or off. Turning it on starts from the next
// planned time; times that passed while it was off are not caught up.
func (s *Service) SetEnabled(ctx context.Context, connectionID asset.ConnectionID, id schedule.ID, enabled bool, actor string) (schedule.ScanSchedule, error) {
	action := "schedule.disable"
	if enabled {
		action = "schedule.enable"
	}
	now := s.clock()
	return s.mutate(ctx, connectionID, id, actor, action, func(value *schedule.ScanSchedule) error {
		setEnabled(value, enabled)
		if enabled {
			compiled, err := value.Frequency.Compile(0, now)
			if err != nil {
				return invalid("schedule.frequency_invalid", "%s", err.Error())
			}
			next := compiled.Next(now)
			value.NextRunAt = &next
		}
		return nil
	})
}

func (s *Service) mutate(ctx context.Context, connectionID asset.ConnectionID, id schedule.ID, actor, action string, change func(*schedule.ScanSchedule) error) (schedule.ScanSchedule, error) {
	for attempt := 0; ; attempt++ {
		current, err := s.Get(ctx, connectionID, id)
		if err != nil {
			return schedule.ScanSchedule{}, err
		}
		updated := current
		if err := change(&updated); err != nil {
			return schedule.ScanSchedule{}, err
		}
		now := s.clock()
		updated.UpdatedAt = now
		updated.UpdatedBy = actor
		err = s.repositories.WithTx(ctx, func(repositories persistence.Repositories) error {
			saved, err := repositories.Schedules().UpdateSchedule(ctx, updated, current.Revision)
			if err != nil {
				return err
			}
			updated = saved
			return audit(ctx, repositories, updated, actor, action, "updated", scheduleEvidence(updated), now)
		})
		// The scheduler may have claimed a planned time in between; retry on
		// the fresh revision rather than failing the person's request.
		if errors.Is(err, persistence.ErrConflict) && attempt < 3 {
			continue
		}
		return updated, err
	}
}

func (s *Service) Delete(ctx context.Context, connectionID asset.ConnectionID, id schedule.ID, actor string) error {
	value, err := s.Get(ctx, connectionID, id)
	if err != nil {
		return err
	}
	now := s.clock()
	return s.repositories.WithTx(ctx, func(repositories persistence.Repositories) error {
		if err := repositories.Schedules().DeleteSchedule(ctx, id); err != nil {
			return err
		}
		return audit(ctx, repositories, value, actor, "schedule.delete", "deleted", scheduleEvidence(value), now)
	})
}

func (s *Service) List(ctx context.Context, connectionID asset.ConnectionID) ([]schedule.ScanSchedule, error) {
	return s.repositories.Schedules().ListSchedules(ctx, connectionID)
}

// RunNow starts the schedule's scan immediately on a person's request. It does
// not move the next planned time.
func (s *Service) RunNow(ctx context.Context, connectionID asset.ConnectionID, id schedule.ID, actor string) (schedule.Run, error) {
	value, err := s.Get(ctx, connectionID, id)
	if err != nil {
		return schedule.Run{}, err
	}
	return s.start(ctx, value, s.clock(), schedule.TriggerManual, actor)
}

// Tick does one round of scheduler work. Run calls it on an interval; tests
// call it directly with a controlled clock.
func (s *Service) Tick(ctx context.Context) {
	// Every server sharing the database runs the scheduler; one tick at a
	// time keeps settlement, failure counters and notifications single.
	err := s.repositories.WithLock(ctx, "scan-scheduler", func(ctx context.Context) error {
		s.tick(ctx)
		return nil
	})
	if err != nil && ctx.Err() == nil {
		slog.Error("scan scheduler lock could not be taken", "error", err)
	}
}

func (s *Service) tick(ctx context.Context) {
	now := s.clock()
	if err := s.triggerDue(ctx, now); err != nil {
		slog.Error("scheduled scans could not be triggered", "error", err)
	}
	if err := s.settleRuns(ctx, now); err != nil {
		slog.Error("scheduled scan results could not be settled", "error", err)
	}
	if now.Sub(s.lastRetention) >= retentionInterval {
		s.lastRetention = now
		if err := s.applyRetention(ctx, now); err != nil {
			slog.Error("scheduled scan history could not be trimmed", "error", err)
		}
	}
}

func (s *Service) Run(ctx context.Context) error {
	ticker := time.NewTicker(tickInterval)
	defer ticker.Stop()
	for {
		s.Tick(ctx)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (s *Service) triggerDue(ctx context.Context, now time.Time) error {
	due, err := s.repositories.Schedules().ListDueSchedules(ctx, now, 100)
	if err != nil {
		return err
	}
	for _, value := range due {
		if err := s.triggerOne(ctx, value, now); err != nil {
			slog.Error("scheduled scan could not be triggered", "schedule_id", value.ID, "error", err)
		}
	}
	return nil
}

func (s *Service) triggerOne(ctx context.Context, value schedule.ScanSchedule, now time.Time) error {
	planned := *value.NextRunAt
	connection, err := s.repositories.Connections().GetConnection(ctx, value.ConnectionID)
	if errors.Is(err, persistence.ErrNotFound) || (err == nil && connection.Status == asset.ConnectionDeleted) {
		return s.pause(ctx, value, schedule.PauseConnectionRemoved, "", now)
	}
	if err != nil {
		return err
	}
	compiled, err := value.Frequency.Compile(0, now)
	if err != nil {
		return s.pause(ctx, value, schedule.PauseConsecutiveFailures, err.Error(), now)
	}
	next := compiled.Next(now)
	trigger := schedule.TriggerSchedule
	if planned.Before(s.startedAt.Add(-missedGrace)) {
		if value.Rules.Missed == schedule.MissedSkip {
			return s.claimAndRecord(ctx, value, next, schedule.Run{PlannedAt: planned, Trigger: schedule.TriggerSchedule, Outcome: schedule.OutcomeSkipped, SkipReason: schedule.SkipMissed}, now)
		}
		trigger = schedule.TriggerCatchUp
	}
	blocking, err := s.repositories.Schedules().FindBlockingScan(ctx, value.ConnectionID)
	switch {
	case err == nil && value.Rules.Overlap == schedule.OverlapWait:
		return nil
	case err == nil:
		return s.claimAndRecord(ctx, value, next, schedule.Run{PlannedAt: planned, Trigger: trigger, Outcome: schedule.OutcomeSkipped, SkipReason: schedule.SkipOverlap, BlockingScanID: blocking.ID}, now)
	case !errors.Is(err, persistence.ErrNotFound):
		return err
	}
	claimed, err := s.claim(ctx, value, next)
	if errors.Is(err, persistence.ErrConflict) {
		return nil
	}
	if err != nil {
		return err
	}
	_, err = s.start(ctx, claimed, planned, trigger, schedule.Actor)
	return err
}

// claim moves the schedule to its next planned time. Only the server whose
// update succeeds goes on to act on the current time.
func (s *Service) claim(ctx context.Context, value schedule.ScanSchedule, next time.Time) (schedule.ScanSchedule, error) {
	updated := value
	updated.NextRunAt = &next
	return s.repositories.Schedules().UpdateSchedule(ctx, updated, value.Revision)
}

func (s *Service) claimAndRecord(ctx context.Context, value schedule.ScanSchedule, next time.Time, run schedule.Run, now time.Time) error {
	if _, err := s.claim(ctx, value, next); err != nil {
		if errors.Is(err, persistence.ErrConflict) {
			return nil
		}
		return err
	}
	run.ID = schedule.RunID(idgen.MustNew("srn"))
	run.ScheduleID = value.ID
	run.ConnectionID = value.ConnectionID
	run.Actor = schedule.Actor
	run.Settled = true
	run.CreatedAt = now
	run.UpdatedAt = now
	return s.repositories.Schedules().CreateRun(ctx, run)
}

func (s *Service) start(ctx context.Context, value schedule.ScanSchedule, planned time.Time, trigger schedule.Trigger, actor string) (schedule.Run, error) {
	now := s.clock()
	run := schedule.Run{
		ID: schedule.RunID(idgen.MustNew("srn")), ScheduleID: value.ID, ConnectionID: value.ConnectionID,
		PlannedAt: planned, Trigger: trigger, Outcome: schedule.OutcomeStarting, Actor: actor, CreatedAt: now, UpdatedAt: now,
	}
	if err := s.repositories.Schedules().CreateRun(ctx, run); err != nil {
		return schedule.Run{}, err
	}
	networkTargets := make([]inventory.NetworkTargetRequest, 0, len(value.Scope.NetworkTargets))
	for _, target := range value.Scope.NetworkTargets {
		networkTargets = append(networkTargets, inventory.NetworkTargetRequest{
			Kind: target.Kind, RegionID: target.RegionID, NativeID: target.NativeID, Name: target.Name, ParentNativeID: target.ParentNativeID,
		})
	}
	created, err := s.scans.Create(ctx, inventory.ScanCreationRequest{
		ConnectionID: value.ConnectionID, RequestedBy: actor, ScheduleID: string(value.ID),
		ScopeMode: value.Scope.ScopeMode, RegionIDs: value.Scope.RegionIDs, NetworkTargets: networkTargets,
		ResourceKindIDs: value.Scope.ResourceKindIDs,
	})
	run.UpdatedAt = s.clock()
	if err != nil {
		run.Outcome = schedule.OutcomeFailed
		run.Error = truncate(err.Error())
		var requestErr *inventory.ScanRequestError
		if errors.As(err, &requestErr) {
			run.ErrorCode = requestErr.Code
		}
		run.Settled = true
		if updateErr := s.repositories.Schedules().UpdateRun(ctx, run); updateErr != nil {
			return schedule.Run{}, updateErr
		}
		if failureErr := s.recordFailure(ctx, value.ID, run.Error, EventScanFailed, ""); failureErr != nil {
			return run, failureErr
		}
		return run, nil
	}
	run.Outcome = schedule.OutcomeStarted
	run.ScanTaskID = created.ScanRun.ID
	return run, s.repositories.Schedules().UpdateRun(ctx, run)
}

func (s *Service) settleRuns(ctx context.Context, now time.Time) error {
	runs, err := s.repositories.Schedules().ListUnsettledRuns(ctx, 200)
	if err != nil {
		return err
	}
	for _, run := range runs {
		if err := s.settleOne(ctx, run, now); err != nil {
			slog.Error("scheduled scan run could not be settled", "run_id", run.ID, "error", err)
		}
	}
	return nil
}

func (s *Service) settleOne(ctx context.Context, run schedule.Run, now time.Time) error {
	switch run.Outcome {
	case schedule.OutcomeStarting:
		if now.Sub(run.CreatedAt) < startTimeout {
			return nil
		}
		run.Outcome = schedule.OutcomeFailed
		run.ErrorCode = "schedule.start_interrupted"
		run.Error = "Steward stopped before the scan started."
		run.Settled = true
		run.UpdatedAt = now
		return s.repositories.Schedules().UpdateRun(ctx, run)
	case schedule.OutcomeStarted:
	default:
		run.Settled = true
		run.UpdatedAt = now
		return s.repositories.Schedules().UpdateRun(ctx, run)
	}
	scan, err := s.repositories.Inventory().GetScanRun(ctx, run.ScanTaskID)
	if errors.Is(err, persistence.ErrNotFound) {
		run.Settled = true
		run.UpdatedAt = now
		return s.repositories.Schedules().UpdateRun(ctx, run)
	}
	if err != nil {
		return err
	}
	if !scan.Status.Terminal() {
		return nil
	}
	value, err := s.repositories.Schedules().GetSchedule(ctx, run.ScheduleID)
	if errors.Is(err, persistence.ErrNotFound) {
		run.Settled = true
		run.FinalStatus = scan.Status
		run.UpdatedAt = now
		return s.repositories.Schedules().UpdateRun(ctx, run)
	}
	if err != nil {
		return err
	}
	if (scan.Status == asset.ScanPartial || scan.Status == asset.ScanFailed) && value.Rules.RetryFailedTargets && !run.AutoRetried {
		run.AutoRetried = true
		run.UpdatedAt = now
		if err := s.repositories.Schedules().UpdateRun(ctx, run); err != nil {
			return err
		}
		if _, err := s.retrier.Retry(ctx, scan.ID, schedule.Actor); err == nil {
			return nil
		} else {
			slog.Info("scheduled scan could not be retried automatically", "scan_task_id", scan.ID, "error", err)
		}
	}
	shards, err := s.repositories.Inventory().ListScanShardsByRun(ctx, scan.ID)
	if err != nil {
		return err
	}
	run.FailedItems, run.FailureSummary = failureSummary(shards)
	run.Settled = true
	run.FinalStatus = scan.Status
	run.UpdatedAt = now
	if err := s.repositories.Schedules().UpdateRun(ctx, run); err != nil {
		return err
	}
	switch scan.Status {
	case asset.ScanFailed:
		return s.recordFailure(ctx, value.ID, run.FailureSummary, EventScanFailed, scan.ID)
	case asset.ScanPartial:
		s.notify(ctx, EventScanPartial, value, scan.ID, run.FailureSummary, now)
		return s.recordSuccess(ctx, value.ID)
	case asset.ScanSucceeded:
		return s.recordSuccess(ctx, value.ID)
	}
	return nil
}

func failureSummary(shards []asset.ScanShard) (int, string) {
	failed := 0
	summary := ""
	for _, shard := range shards {
		if shard.Status != asset.ShardFailed && shard.Status != asset.ShardBlocked {
			continue
		}
		failed++
		if summary == "" {
			parts := []string{}
			for _, part := range []string{shard.RegionID, string(shard.ResourceKindID), shard.Coverage.FailureReason} {
				if strings.TrimSpace(part) != "" {
					parts = append(parts, strings.TrimSpace(part))
				}
			}
			summary = truncate(strings.Join(parts, " · "))
		}
	}
	return failed, summary
}

func (s *Service) recordSuccess(ctx context.Context, id schedule.ID) error {
	return s.updateCounters(ctx, id, func(value *schedule.ScanSchedule) bool {
		if value.ConsecutiveFailures == 0 {
			return false
		}
		value.ConsecutiveFailures = 0
		return true
	})
}

func (s *Service) recordFailure(ctx context.Context, id schedule.ID, detail string, kind EventKind, scanID asset.ScanTaskID) error {
	now := s.clock()
	paused := false
	var saved schedule.ScanSchedule
	err := s.updateCounters(ctx, id, func(value *schedule.ScanSchedule) bool {
		value.ConsecutiveFailures++
		paused = value.Enabled && value.ConsecutiveFailures >= value.Rules.PauseAfterFailures
		if paused {
			value.Enabled = false
			value.NextRunAt = nil
			value.PauseReason = schedule.PauseConsecutiveFailures
			value.PauseDetail = detail
			value.PausedAt = &now
		}
		saved = *value
		return true
	})
	if err != nil {
		return err
	}
	s.notify(ctx, kind, saved, scanID, detail, now)
	if paused {
		if err := audit(ctx, s.repositories, saved, schedule.Actor, "schedule.pause", "paused", map[string]any{"reason": saved.PauseReason, "consecutive_failures": saved.ConsecutiveFailures}, now); err != nil {
			return err
		}
		s.notify(ctx, EventSchedulePaused, saved, scanID, detail, now)
	}
	return nil
}

func (s *Service) updateCounters(ctx context.Context, id schedule.ID, change func(*schedule.ScanSchedule) bool) error {
	for attempt := 0; ; attempt++ {
		current, err := s.repositories.Schedules().GetSchedule(ctx, id)
		if errors.Is(err, persistence.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		updated := current
		if !change(&updated) {
			return nil
		}
		_, err = s.repositories.Schedules().UpdateSchedule(ctx, updated, current.Revision)
		if errors.Is(err, persistence.ErrConflict) && attempt < 5 {
			continue
		}
		return err
	}
}

func (s *Service) pause(ctx context.Context, value schedule.ScanSchedule, reason schedule.PauseReason, detail string, now time.Time) error {
	updated := value
	updated.Enabled = false
	updated.NextRunAt = nil
	updated.PauseReason = reason
	updated.PauseDetail = truncate(detail)
	updated.PausedAt = &now
	saved, err := s.repositories.Schedules().UpdateSchedule(ctx, updated, value.Revision)
	if errors.Is(err, persistence.ErrConflict) {
		return nil
	}
	if err != nil {
		return err
	}
	return audit(ctx, s.repositories, saved, schedule.Actor, "schedule.pause", "paused", map[string]any{"reason": reason}, now)
}

// ConnectionValidated resumes schedules that Steward paused after repeated
// failures, because the usual cause — a broken credential — was just fixed.
// On a connection's first validation it also creates the default schedule
// when the workspace asks for one.
func (s *Service) ConnectionValidated(ctx context.Context, connectionID asset.ConnectionID, first bool) error {
	values, err := s.repositories.Schedules().ListSchedules(ctx, connectionID)
	if err != nil {
		return err
	}
	now := s.clock()
	for _, value := range values {
		if value.Enabled || value.PauseReason != schedule.PauseConsecutiveFailures {
			continue
		}
		compiled, err := value.Frequency.Compile(0, now)
		if err != nil {
			continue
		}
		updated := value
		setEnabled(&updated, true)
		next := compiled.Next(now)
		updated.NextRunAt = &next
		saved, err := s.repositories.Schedules().UpdateSchedule(ctx, updated, value.Revision)
		if errors.Is(err, persistence.ErrConflict) {
			continue
		}
		if err != nil {
			return err
		}
		if err := audit(ctx, s.repositories, saved, schedule.Actor, "schedule.resume", "resumed", map[string]any{"reason": "connection_validated"}, now); err != nil {
			return err
		}
	}
	if !first || len(values) > 0 {
		return nil
	}
	settings, err := s.Settings(ctx)
	if err != nil || !settings.DefaultScheduleEnabled {
		return err
	}
	_, err = s.Create(ctx, connectionID, DefaultInput(settings.DefaultTimezone), schedule.Actor)
	return err
}

// DefaultInput is a daily scan of everything at a random time between 01:00
// and 06:00, so connections added together do not all scan at once.
func DefaultInput(timezone string) Input {
	if _, err := time.LoadLocation(timezone); err != nil || timezone == "" {
		timezone = "UTC"
	}
	minutes := 60 + rand.IntN(300)
	return Input{
		Scope: schedule.Scope{ScopeMode: asset.ScanAllActiveRegions},
		Frequency: schedule.Frequency{
			Kind: schedule.FrequencyDaily, Time: fmt.Sprintf("%02d:%02d", minutes/60, minutes%60), Timezone: timezone,
		},
	}
}

func (s *Service) Settings(ctx context.Context) (schedule.Settings, error) {
	payload, err := s.repositories.Schedules().GetSetting(ctx, settingsKey)
	if errors.Is(err, persistence.ErrNotFound) {
		return schedule.DefaultSettings(), nil
	}
	if err != nil {
		return schedule.Settings{}, err
	}
	settings := schedule.DefaultSettings()
	if err := json.Unmarshal([]byte(payload), &settings); err != nil {
		return schedule.Settings{}, err
	}
	return settings, nil
}

func (s *Service) UpdateSettings(ctx context.Context, settings schedule.Settings, actor string) (schedule.Settings, error) {
	if settings.RetentionDays < 0 || settings.RetentionDays > 3650 {
		return schedule.Settings{}, invalid("schedule.settings_invalid", "retention_days must be 0 (keep forever) to 3650")
	}
	settings.DefaultTimezone = strings.TrimSpace(settings.DefaultTimezone)
	if settings.DefaultTimezone != "" {
		if _, err := time.LoadLocation(settings.DefaultTimezone); err != nil {
			return schedule.Settings{}, invalid("schedule.settings_invalid", "unknown time zone %q", settings.DefaultTimezone)
		}
	}
	now := s.clock()
	settings.UpdatedAt = now
	settings.UpdatedBy = actor
	payload, err := json.Marshal(settings)
	if err != nil {
		return schedule.Settings{}, err
	}
	err = s.repositories.WithTx(ctx, func(repositories persistence.Repositories) error {
		if err := repositories.Schedules().PutSetting(ctx, settingsKey, string(payload), now); err != nil {
			return err
		}
		return repositories.Audits().AppendAuditEvent(ctx, execution.AuditEvent{
			ID: execution.AuditEventID(idgen.MustNew("aud")), Actor: actor, Action: "schedule.settings.update",
			TargetType: "workspace_settings", TargetID: settingsKey, Result: "updated",
			Evidence:  map[string]any{"default_schedule_enabled": settings.DefaultScheduleEnabled, "retention_days": settings.RetentionDays, "default_timezone": settings.DefaultTimezone},
			CreatedAt: now,
		})
	})
	return settings, err
}

func (s *Service) applyRetention(ctx context.Context, now time.Time) error {
	settings, err := s.Settings(ctx)
	if err != nil || settings.RetentionDays <= 0 {
		return err
	}
	cutoff := now.AddDate(0, 0, -settings.RetentionDays)
	schedules := s.repositories.Schedules()
	for _, listExpired := range []func(context.Context, time.Time, int) ([]asset.ScanTaskID, error){
		schedules.ListExpiredScheduledScans, schedules.ListExpiredManualScans,
	} {
		for batch := 0; batch < 20; batch++ {
			ids, err := listExpired(ctx, cutoff, 50)
			if err != nil {
				return err
			}
			for _, id := range ids {
				if err := schedules.DeleteScan(ctx, id); err != nil && !errors.Is(err, persistence.ErrNotFound) && !errors.Is(err, persistence.ErrConflict) {
					return err
				}
			}
			if len(ids) < 50 {
				break
			}
		}
	}
	return s.repositories.Schedules().DeleteRunsBefore(ctx, cutoff)
}

func (s *Service) notify(ctx context.Context, kind EventKind, value schedule.ScanSchedule, scanID asset.ScanTaskID, detail string, now time.Time) {
	if s.notifier == nil {
		return
	}
	connection, err := s.repositories.Connections().GetConnection(ctx, value.ConnectionID)
	if err != nil {
		return
	}
	s.notifier.Notify(ctx, Event{Kind: kind, Schedule: value, Connection: connection, ScanTaskID: scanID, Detail: detail, OccurredAt: now})
}

func audit(ctx context.Context, repositories persistence.Repositories, value schedule.ScanSchedule, actor, action, result string, evidence map[string]any, now time.Time) error {
	return repositories.Audits().AppendAuditEvent(ctx, execution.AuditEvent{
		ID: execution.AuditEventID(idgen.MustNew("aud")), ConnectionID: value.ConnectionID, Actor: actor, Action: action,
		TargetType: "scan_schedule", TargetID: string(value.ID), Result: result, Evidence: evidence, CreatedAt: now,
	})
}

func scheduleEvidence(value schedule.ScanSchedule) map[string]any {
	return map[string]any{
		"name": value.Name, "enabled": value.Enabled, "scope_mode": value.Scope.ScopeMode,
		"frequency": value.Frequency.Kind, "timezone": value.Frequency.Timezone,
	}
}

func truncate(value string) string {
	value = strings.TrimSpace(value)
	if len([]rune(value)) <= maxFailureDetail {
		return value
	}
	return string([]rune(value)[:maxFailureDetail]) + "…"
}
