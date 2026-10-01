package scheduling_test

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/loomx-ai/steward/internal/app/inventory"
	"github.com/loomx-ai/steward/internal/app/scheduling"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/schedule"
	"github.com/loomx-ai/steward/internal/idgen"
	"github.com/loomx-ai/steward/internal/persistence"
	"github.com/loomx-ai/steward/internal/persistence/sqlite"
)

type fakeScans struct {
	repositories persistence.Repositories
	clock        func() time.Time
	requests     []inventory.ScanCreationRequest
	fail         error
	retried      []asset.ScanTaskID
}

func (f *fakeScans) Create(ctx context.Context, request inventory.ScanCreationRequest) (inventory.ScanCreation, error) {
	f.requests = append(f.requests, request)
	if f.fail != nil {
		return inventory.ScanCreation{}, f.fail
	}
	run := asset.ScanRun{
		ID: asset.ScanRunID(idgen.MustNew("scn")), ConnectionID: request.ConnectionID, Status: asset.ScanPending,
		ScopeMode: request.ScopeMode, RequestedBy: request.RequestedBy, ScheduleID: request.ScheduleID, CreatedAt: f.clock(),
	}
	if err := f.repositories.Inventory().CreateScanRun(ctx, run); err != nil {
		return inventory.ScanCreation{}, err
	}
	return inventory.ScanCreation{ScanRun: run}, nil
}

func (f *fakeScans) Retry(ctx context.Context, id asset.ScanTaskID, actor string) (asset.ScanTask, error) {
	f.retried = append(f.retried, id)
	run, err := f.repositories.Inventory().GetScanRun(ctx, id)
	if err != nil {
		return asset.ScanTask{}, err
	}
	run.Status = asset.ScanRunning
	return run, f.repositories.Inventory().PutScanRun(ctx, run)
}

type recordingNotifier struct {
	mu     sync.Mutex
	events []scheduling.Event
}

func (n *recordingNotifier) Notify(_ context.Context, event scheduling.Event) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.events = append(n.events, event)
}

func (n *recordingNotifier) kinds() []scheduling.EventKind {
	n.mu.Lock()
	defer n.mu.Unlock()
	kinds := make([]scheduling.EventKind, 0, len(n.events))
	for _, event := range n.events {
		kinds = append(kinds, event.Kind)
	}
	return kinds
}

type harness struct {
	t            *testing.T
	repositories persistence.Repositories
	scans        *fakeScans
	notifier     *recordingNotifier
	now          time.Time
	service      *scheduling.Service
}

func newHarness(t *testing.T, minInterval time.Duration) *harness {
	t.Helper()
	repositories, err := sqlite.Open(filepath.Join(t.TempDir(), "steward.db"), filepath.Join("..", "..", "..", "migrations"))
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{t: t, repositories: repositories, notifier: &recordingNotifier{}, now: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)}
	h.scans = &fakeScans{repositories: repositories, clock: h.clock}
	if err := repositories.Connections().PutConnection(context.Background(), asset.CloudConnection{
		ID: "con-1", Name: "production", Provider: asset.ProviderAliCloud, Status: asset.ConnectionActive, CreatedAt: h.now, UpdatedAt: h.now,
	}); err != nil {
		t.Fatal(err)
	}
	h.restart(minInterval)
	return h
}

func (h *harness) clock() time.Time { return h.now }

// restart simulates the server starting at the current clock.
func (h *harness) restart(minInterval time.Duration) {
	service, err := scheduling.NewService(h.repositories, h.scans, h.scans, scheduling.Options{MinInterval: minInterval, Clock: h.clock, Notifier: h.notifier})
	if err != nil {
		h.t.Fatal(err)
	}
	h.service = service
}

func (h *harness) create(input scheduling.Input) schedule.ScanSchedule {
	h.t.Helper()
	value, err := h.service.Create(context.Background(), "con-1", input, "alice")
	if err != nil {
		h.t.Fatal(err)
	}
	return value
}

func (h *harness) runs(id schedule.ID) []schedule.Run {
	h.t.Helper()
	page, err := h.repositories.Schedules().ListRuns(context.Background(), id, persistence.ListOptions{})
	if err != nil {
		h.t.Fatal(err)
	}
	return page.Items
}

func (h *harness) schedule(id schedule.ID) schedule.ScanSchedule {
	h.t.Helper()
	value, err := h.repositories.Schedules().GetSchedule(context.Background(), id)
	if err != nil {
		h.t.Fatal(err)
	}
	return value
}

func (h *harness) finishScan(id asset.ScanTaskID, status asset.ScanStatus) {
	h.t.Helper()
	run, err := h.repositories.Inventory().GetScanRun(context.Background(), id)
	if err != nil {
		h.t.Fatal(err)
	}
	run.Status = status
	finished := h.now
	run.FinishedAt = &finished
	if err := h.repositories.Inventory().PutScanRun(context.Background(), run); err != nil {
		h.t.Fatal(err)
	}
}

func everyTwoHours() scheduling.Input {
	return scheduling.Input{
		Name:      "core",
		Scope:     schedule.Scope{ScopeMode: asset.ScanAllActiveRegions},
		Frequency: schedule.Frequency{Kind: schedule.FrequencyHourly, EveryHours: 2, Time: "00:30", Timezone: "UTC"},
	}
}

func TestCreateValidatesAndPlansTheNextRun(t *testing.T) {
	h := newHarness(t, time.Hour)
	value := h.create(everyTwoHours())
	if !value.Enabled || value.NextRunAt == nil || !value.NextRunAt.Equal(time.Date(2026, 10, 1, 12, 30, 0, 0, time.UTC)) {
		t.Fatalf("schedule = %+v", value)
	}
	if value.Rules != schedule.DefaultRules() {
		t.Fatalf("rules = %+v", value.Rules)
	}

	cloud := newHarness(t, scheduling.CloudMinInterval)
	_, err := cloud.service.Create(context.Background(), "con-1", everyTwoHours(), "alice")
	var invalid *scheduling.InvalidError
	if !errors.As(err, &invalid) || invalid.Code != "schedule.interval_too_short" {
		t.Fatalf("cloud minimum not enforced: %v", err)
	}
	input := everyTwoHours()
	input.Scope = schedule.Scope{ScopeMode: asset.ScanSelectedRegions}
	if _, err := h.service.Create(context.Background(), "con-1", input, "alice"); !errors.As(err, &invalid) || invalid.Code != "schedule.scope_invalid" {
		t.Fatalf("empty region scope accepted: %v", err)
	}
}

func TestDueScheduleStartsOneScanAndMovesToTheNextTime(t *testing.T) {
	h := newHarness(t, time.Hour)
	value := h.create(everyTwoHours())
	h.now = time.Date(2026, 10, 1, 12, 30, 5, 0, time.UTC)
	h.service.Tick(context.Background())
	h.service.Tick(context.Background())

	if len(h.scans.requests) != 1 {
		t.Fatalf("scan requests = %d", len(h.scans.requests))
	}
	request := h.scans.requests[0]
	if request.ScheduleID != string(value.ID) || request.RequestedBy != schedule.Actor || request.ScopeMode != asset.ScanAllActiveRegions {
		t.Fatalf("request = %+v", request)
	}
	runs := h.runs(value.ID)
	if len(runs) != 1 || runs[0].Outcome != schedule.OutcomeStarted || runs[0].Trigger != schedule.TriggerSchedule || runs[0].ScanTaskID == "" {
		t.Fatalf("runs = %+v", runs)
	}
	if next := h.schedule(value.ID).NextRunAt; next == nil || !next.Equal(time.Date(2026, 10, 1, 14, 30, 0, 0, time.UTC)) {
		t.Fatalf("next run = %v", next)
	}

	// A second server holding the old revision cannot claim the same time.
	stale := value
	if _, err := h.repositories.Schedules().UpdateSchedule(context.Background(), stale, stale.Revision); !errors.Is(err, persistence.ErrConflict) {
		t.Fatalf("stale claim error = %v", err)
	}
}

func TestOverlapSkipsOrWaits(t *testing.T) {
	h := newHarness(t, time.Hour)
	if err := h.repositories.Inventory().CreateScanRun(context.Background(), asset.ScanRun{ID: "scn-manual", ConnectionID: "con-1", Status: asset.ScanRunning, ScopeMode: asset.ScanSelectedRegions, RequestedBy: "bob", CreatedAt: h.now}); err != nil {
		t.Fatal(err)
	}
	skip := h.create(everyTwoHours())
	waitInput := everyTwoHours()
	waitInput.Rules = &schedule.Rules{Overlap: schedule.OverlapWait, Missed: schedule.MissedCatchUp, RetryFailedTargets: true, PauseAfterFailures: 3}
	wait := h.create(waitInput)

	h.now = time.Date(2026, 10, 1, 12, 31, 0, 0, time.UTC)
	h.service.Tick(context.Background())
	runs := h.runs(skip.ID)
	if len(runs) != 1 || runs[0].Outcome != schedule.OutcomeSkipped || runs[0].SkipReason != schedule.SkipOverlap || runs[0].BlockingScanID != "scn-manual" || !runs[0].Settled {
		t.Fatalf("skip runs = %+v", runs)
	}
	if len(h.runs(wait.ID)) != 0 || !h.schedule(wait.ID).NextRunAt.Equal(time.Date(2026, 10, 1, 12, 30, 0, 0, time.UTC)) {
		t.Fatal("waiting schedule must hold its planned time")
	}

	h.finishScan("scn-manual", asset.ScanSucceeded)
	h.now = time.Date(2026, 10, 1, 12, 40, 0, 0, time.UTC)
	h.service.Tick(context.Background())
	runs = h.runs(wait.ID)
	if len(runs) != 1 || runs[0].Outcome != schedule.OutcomeStarted || !runs[0].PlannedAt.Equal(time.Date(2026, 10, 1, 12, 30, 0, 0, time.UTC)) {
		t.Fatalf("wait runs = %+v", runs)
	}
}

func TestMissedTimesCatchUpOnceOrAreSkipped(t *testing.T) {
	h := newHarness(t, time.Hour)
	catchUp := h.create(everyTwoHours())
	skipInput := everyTwoHours()
	skipInput.Rules = &schedule.Rules{Overlap: schedule.OverlapSkip, Missed: schedule.MissedSkip, RetryFailedTargets: true, PauseAfterFailures: 3}
	skip := h.create(skipInput)

	// The server was down from 12:00 to 17:10, across three planned times.
	h.now = time.Date(2026, 10, 1, 17, 10, 0, 0, time.UTC)
	h.restart(time.Hour)
	h.service.Tick(context.Background())

	runs := h.runs(catchUp.ID)
	if len(runs) != 1 || runs[0].Trigger != schedule.TriggerCatchUp || runs[0].Outcome != schedule.OutcomeStarted {
		t.Fatalf("catch-up runs = %+v", runs)
	}
	if next := h.schedule(catchUp.ID).NextRunAt; !next.Equal(time.Date(2026, 10, 1, 18, 30, 0, 0, time.UTC)) {
		t.Fatalf("catch-up next = %v", next)
	}
	runs = h.runs(skip.ID)
	if len(runs) != 1 || runs[0].Outcome != schedule.OutcomeSkipped || runs[0].SkipReason != schedule.SkipMissed {
		t.Fatalf("skip runs = %+v", runs)
	}
}

func TestFailuresRetryOncePauseAfterThresholdAndResumeOnValidation(t *testing.T) {
	h := newHarness(t, time.Hour)
	value := h.create(everyTwoHours())
	ctx := context.Background()

	// Run 1 finishes partially: retried once, then settles as partial.
	h.now = time.Date(2026, 10, 1, 12, 30, 0, 0, time.UTC)
	h.service.Tick(ctx)
	scanID := h.runs(value.ID)[0].ScanTaskID
	h.finishScan(scanID, asset.ScanPartial)
	h.service.Tick(ctx)
	if len(h.scans.retried) != 1 || h.scans.retried[0] != scanID {
		t.Fatalf("retried = %v", h.scans.retried)
	}
	h.finishScan(scanID, asset.ScanPartial)
	h.service.Tick(ctx)
	if run := h.runs(value.ID)[0]; !run.Settled || run.FinalStatus != asset.ScanPartial || !run.AutoRetried || len(h.scans.retried) != 1 {
		t.Fatalf("partial run = %+v", run)
	}

	// Three scans that cannot start pause the schedule.
	h.scans.fail = &inventory.ScanRequestError{Code: "scan.connection_not_validated", Message: "the selected connection has not passed validation"}
	for _, at := range []time.Time{
		time.Date(2026, 10, 1, 14, 30, 0, 0, time.UTC),
		time.Date(2026, 10, 1, 16, 30, 0, 0, time.UTC),
		time.Date(2026, 10, 1, 18, 30, 0, 0, time.UTC),
	} {
		h.now = at
		h.service.Tick(ctx)
	}
	paused := h.schedule(value.ID)
	if paused.Enabled || paused.PauseReason != schedule.PauseConsecutiveFailures || paused.NextRunAt != nil || paused.ConsecutiveFailures != 3 || paused.PauseDetail == "" {
		t.Fatalf("paused schedule = %+v", paused)
	}
	if runs := h.runs(value.ID); runs[0].Outcome != schedule.OutcomeFailed || runs[0].ErrorCode != "scan.connection_not_validated" {
		t.Fatalf("failed run = %+v", runs[0])
	}
	kinds := h.notifier.kinds()
	if len(kinds) != 5 || kinds[0] != scheduling.EventScanPartial || kinds[3] != scheduling.EventScanFailed || kinds[4] != scheduling.EventSchedulePaused {
		t.Fatalf("events = %v", kinds)
	}
	h.now = time.Date(2026, 10, 1, 22, 30, 0, 0, time.UTC)
	h.service.Tick(ctx)
	if len(h.runs(value.ID)) != 4 {
		t.Fatal("a paused schedule must not run")
	}

	h.scans.fail = nil
	if err := h.service.ConnectionValidated(ctx, "con-1", false); err != nil {
		t.Fatal(err)
	}
	resumed := h.schedule(value.ID)
	if !resumed.Enabled || resumed.PauseReason != "" || resumed.ConsecutiveFailures != 0 || !resumed.NextRunAt.Equal(time.Date(2026, 10, 2, 0, 30, 0, 0, time.UTC)) {
		t.Fatalf("resumed schedule = %+v", resumed)
	}
}

func TestFirstValidationCreatesDefaultScheduleOnlyWhenEnabled(t *testing.T) {
	h := newHarness(t, time.Hour)
	ctx := context.Background()
	if err := h.service.ConnectionValidated(ctx, "con-1", true); err != nil {
		t.Fatal(err)
	}
	if values, _ := h.service.List(ctx, "con-1"); len(values) != 0 {
		t.Fatalf("default schedule created while the setting is off: %+v", values)
	}
	if _, err := h.service.UpdateSettings(ctx, schedule.Settings{DefaultScheduleEnabled: true, RetentionDays: 30, DefaultTimezone: "Asia/Shanghai"}, "admin"); err != nil {
		t.Fatal(err)
	}
	if err := h.service.ConnectionValidated(ctx, "con-1", true); err != nil {
		t.Fatal(err)
	}
	values, _ := h.service.List(ctx, "con-1")
	if len(values) != 1 || !values[0].Scope.Complete() || values[0].Frequency.Kind != schedule.FrequencyDaily || values[0].Frequency.Timezone != "Asia/Shanghai" || values[0].CreatedBy != schedule.Actor {
		t.Fatalf("default schedule = %+v", values)
	}
	hour := values[0].Frequency.Time[:2]
	if hour < "01" || hour > "05" {
		t.Fatalf("default time %s is outside 01:00–06:00", values[0].Frequency.Time)
	}
}

func TestRetentionKeepsTheLatestSucceededScanOfEachSchedule(t *testing.T) {
	h := newHarness(t, time.Hour)
	ctx := context.Background()
	old := h.now.AddDate(0, 0, -40)
	for _, scan := range []asset.ScanRun{
		{ID: "scn-old-ok", Status: asset.ScanSucceeded, CreatedAt: old},
		{ID: "scn-old-partial", Status: asset.ScanPartial, CreatedAt: old.Add(time.Hour)},
		{ID: "scn-latest-ok", Status: asset.ScanSucceeded, CreatedAt: old.Add(2 * time.Hour)},
		{ID: "scn-recent", Status: asset.ScanFailed, CreatedAt: h.now.Add(-time.Hour)},
	} {
		scan.ConnectionID = "con-1"
		scan.ScheduleID = "sch-1"
		scan.ScopeMode = asset.ScanAllActiveRegions
		if err := h.repositories.Inventory().CreateScanRun(ctx, scan); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.repositories.Inventory().CreateScanRun(ctx, asset.ScanRun{ID: "scn-manual-old", ConnectionID: "con-1", Status: asset.ScanSucceeded, ScopeMode: asset.ScanAllActiveRegions, CreatedAt: old}); err != nil {
		t.Fatal(err)
	}
	h.service.Tick(ctx)
	for id, kept := range map[asset.ScanTaskID]bool{"scn-old-ok": false, "scn-old-partial": false, "scn-latest-ok": true, "scn-recent": true, "scn-manual-old": true} {
		_, err := h.repositories.Inventory().GetScanRun(ctx, id)
		if kept != (err == nil) {
			t.Fatalf("scan %s kept=%v err=%v", id, kept, err)
		}
	}
}
