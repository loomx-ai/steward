package cleanup_test

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/loomx-ai/steward/internal/app/cleanup"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/internal/persistence"
	"github.com/loomx-ai/steward/internal/persistence/sqlite"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

type actionRegistrySpy struct {
	calls  int
	driver contracts.ActionDriver
}

func (r *actionRegistrySpy) ResolveAction(context.Context, asset.ConnectionID, asset.Asset) (contracts.ActionDriver, error) {
	r.calls++
	return r.driver, nil
}

type statusChangingDriver struct {
	preflight func()
	execCalls int
}

func (d *statusChangingDriver) Preflight(context.Context, contracts.ActionRequest) (contracts.PreflightResult, error) {
	if d.preflight != nil {
		d.preflight()
	}
	return contracts.PreflightResult{Allowed: true}, nil
}

func (d *statusChangingDriver) Execute(context.Context, contracts.ActionRequest) (contracts.ActionResult, error) {
	d.execCalls++
	return contracts.ActionResult{}, nil
}

func (*statusChangingDriver) Wait(context.Context, contracts.ActionRequest, contracts.ActionResult) (contracts.WaitResult, error) {
	return contracts.WaitResult{Done: true}, nil
}

func (*statusChangingDriver) Readback(context.Context, contracts.ActionRequest) (contracts.ReadbackResult, error) {
	return contracts.ReadbackResult{Exists: false}, nil
}

func TestRepositoryActionResolverRejectsConnectionThatLostValidation(t *testing.T) {
	ctx := context.Background()
	repositories, err := sqlite.Open(filepath.Join(t.TempDir(), "cleanup.db"), filepath.Join("..", "..", "..", "migrations"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	connection := asset.CloudConnection{
		ID: "connection-invalid", Name: "invalid", Provider: asset.ProviderAliCloud,
		Status: asset.ConnectionInvalid, CreatedAt: now, UpdatedAt: now,
	}
	if err := repositories.Connections().PutConnection(ctx, connection); err != nil {
		t.Fatal(err)
	}
	registry := &actionRegistrySpy{}
	resolver := cleanup.NewRepositoryActionResolver(repositories.Connections(), registry)
	value := asset.Asset{Identity: asset.Identity{
		Provider: asset.ProviderAliCloud, ConnectionID: connection.ID,
		NativeType: "ACS::ECS::Instance", NativeID: "i-1",
	}}

	if _, err := resolver.ResolveAction(ctx, value); !errors.Is(err, asset.ErrConnectionNotValidated) {
		t.Fatalf("ResolveAction() error = %v, want ErrConnectionNotValidated", err)
	}
	if registry.calls != 0 {
		t.Fatalf("provider registry calls = %d, want 0", registry.calls)
	}
}

func TestRepositoryActionResolverRechecksValidationBeforeEveryProviderCall(t *testing.T) {
	ctx := context.Background()
	repositories, err := sqlite.Open(filepath.Join(t.TempDir(), "cleanup.db"), filepath.Join("..", "..", "..", "migrations"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	connection := asset.CloudConnection{
		ID: "connection-active", Name: "active", Provider: asset.ProviderAliCloud,
		Status: asset.ConnectionActive, CreatedAt: now, UpdatedAt: now,
	}
	if err := repositories.Connections().PutConnection(ctx, connection); err != nil {
		t.Fatal(err)
	}
	driver := &statusChangingDriver{preflight: func() {
		connection.Status = asset.ConnectionInvalid
		connection.UpdatedAt = now.Add(time.Minute)
		if err := repositories.Connections().PutConnection(ctx, connection); err != nil {
			t.Fatal(err)
		}
	}}
	registry := &actionRegistrySpy{driver: driver}
	resolver := cleanup.NewRepositoryActionResolver(repositories.Connections(), registry)
	value := asset.Asset{Identity: asset.Identity{
		Provider: asset.ProviderAliCloud, ConnectionID: connection.ID,
		NativeType: "ACS::ECS::Instance", NativeID: "i-1",
	}}
	resolved, err := resolver.ResolveAction(ctx, value)
	if err != nil {
		t.Fatal(err)
	}
	request := contracts.ActionRequest{Asset: value, Action: "delete", IdempotencyKey: "action-1"}
	if _, err := resolved.Preflight(ctx, request); err != nil {
		t.Fatal(err)
	}
	if _, err := resolved.Execute(ctx, request); !errors.Is(err, asset.ErrConnectionNotValidated) {
		t.Fatalf("Execute() error = %v, want ErrConnectionNotValidated", err)
	}
	if driver.execCalls != 0 {
		t.Fatalf("provider execute calls = %d, want 0", driver.execCalls)
	}
}

func TestRepositoryActionResolverDefaultsDeletePollingToTwoSeconds(t *testing.T) {
	ctx := context.Background()
	repositories, err := sqlite.Open(filepath.Join(t.TempDir(), "cleanup.db"), filepath.Join("..", "..", "..", "migrations"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 6, 15, 0, 0, 0, time.UTC)
	connection := asset.CloudConnection{
		ID: "connection-polling", Name: "polling", Provider: asset.ProviderAliCloud,
		Status: asset.ConnectionActive, CreatedAt: now, UpdatedAt: now,
	}
	if err := repositories.Connections().PutConnection(ctx, connection); err != nil {
		t.Fatal(err)
	}
	driver := &statusChangingDriver{}
	resolver := cleanup.NewRepositoryActionResolver(
		repositories.Connections(),
		&actionRegistrySpy{driver: driver},
	)
	value := asset.Asset{Identity: asset.Identity{
		Provider: asset.ProviderAliCloud, ConnectionID: connection.ID,
		NativeType: "ACS::ECS::Instance", NativeID: "i-1",
	}}
	resolved, err := resolver.ResolveAction(ctx, value)
	if err != nil {
		t.Fatal(err)
	}
	result, err := resolved.Execute(ctx, contracts.ActionRequest{
		Asset: value, Action: "delete", IdempotencyKey: "action-polling",
	})
	if err != nil || result.RetryAfter != 2*time.Second {
		t.Fatalf("execute result=%+v err=%v", result, err)
	}
}

func TestWorkerReclaimsExpiredLeaseWithStablePayload(t *testing.T) {
	now := time.Date(2026, 7, 13, 5, 0, 0, 0, time.UTC)
	expired := now.Add(-time.Minute)
	jobs := &fakeJobs{job: execution.Job{ID: "job-1", Type: execution.JobExecute, Status: execution.JobRunning, Payload: map[string]any{"idempotency_key": "stable-key"}, RunAt: now.Add(-time.Hour), LeaseOwner: "dead-worker", LeaseUntil: &expired, CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-time.Hour)}}
	var received string
	worker := cleanup.NewWorker(jobs, map[execution.JobType]cleanup.Handler{
		execution.JobExecute: cleanup.HandlerFunc(func(_ context.Context, job execution.Job) error {
			received, _ = job.Payload["idempotency_key"].(string)
			return nil
		}),
	}, cleanup.WorkerOptions{WorkerID: "worker-2", LeaseDuration: time.Minute, RenewInterval: time.Hour, Now: func() time.Time { return now }})
	processed, err := worker.ProcessOne(context.Background())
	if err != nil || !processed || received != "stable-key" {
		t.Fatalf("processed=%v received=%q err=%v", processed, received, err)
	}
	if jobs.job.Status != execution.JobSucceeded || jobs.job.LeaseOwner != "" {
		t.Fatalf("job=%#v", jobs.job)
	}
}

func TestWorkerRenewsLeaseWhileHandlerRuns(t *testing.T) {
	now := time.Date(2026, 7, 13, 5, 1, 0, 0, time.UTC)
	jobs := &fakeJobs{job: execution.Job{ID: "job-1", Type: execution.JobExecute, Status: execution.JobPending, RunAt: now, CreatedAt: now, UpdatedAt: now}, renewed: make(chan struct{}, 1)}
	release := make(chan struct{})
	worker := cleanup.NewWorker(jobs, map[execution.JobType]cleanup.Handler{
		execution.JobExecute: cleanup.HandlerFunc(func(context.Context, execution.Job) error {
			<-release
			return nil
		}),
	}, cleanup.WorkerOptions{WorkerID: "worker-1", LeaseDuration: time.Minute, RenewInterval: time.Millisecond, Now: func() time.Time { return now }})
	done := make(chan error, 1)
	go func() {
		_, err := worker.ProcessOne(context.Background())
		done <- err
	}()
	select {
	case <-jobs.renewed:
	case <-time.After(time.Second):
		t.Fatal("worker did not renew lease")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestWorkerReschedulesRetryAfter(t *testing.T) {
	now := time.Date(2026, 7, 13, 5, 2, 0, 0, time.UTC)
	jobs := &fakeJobs{job: execution.Job{ID: "job-1", Type: execution.JobExecute, Status: execution.JobPending, RunAt: now, CreatedAt: now, UpdatedAt: now}}
	worker := cleanup.NewWorker(jobs, map[execution.JobType]cleanup.Handler{
		execution.JobExecute: cleanup.HandlerFunc(func(context.Context, execution.Job) error {
			return cleanup.RetryAfter(errors.New("throttled"), 30*time.Second)
		}),
	}, cleanup.WorkerOptions{WorkerID: "worker-1", LeaseDuration: time.Minute, RenewInterval: time.Hour, Now: func() time.Time { return now }})
	processed, err := worker.ProcessOne(context.Background())
	if err != nil || !processed {
		t.Fatalf("processed=%v err=%v", processed, err)
	}
	if jobs.job.Status != execution.JobPending || !jobs.job.RunAt.Equal(now.Add(30*time.Second)) || jobs.job.LastError != "throttled" {
		t.Fatalf("job=%#v", jobs.job)
	}
	if len(jobs.logs) != 2 ||
		jobs.logs[1].Level != "info" ||
		jobs.logs[1].Message != "job rescheduled after 30s: throttled" {
		t.Fatalf("logs=%+v", jobs.logs)
	}
}

func TestWorkerIgnoresRenewalCancellationAfterHandlerCompletes(t *testing.T) {
	now := time.Date(2026, 7, 13, 5, 3, 0, 0, time.UTC)
	renewStarted := make(chan struct{})
	jobs := &fakeJobs{
		job: execution.Job{ID: "job-1", Type: execution.JobExecute, Status: execution.JobPending, RunAt: now, CreatedAt: now, UpdatedAt: now},
		renew: func(ctx context.Context, _ execution.JobID, _ string, _ time.Time) error {
			close(renewStarted)
			<-ctx.Done()
			return ctx.Err()
		},
	}
	worker := cleanup.NewWorker(jobs, map[execution.JobType]cleanup.Handler{
		execution.JobExecute: cleanup.HandlerFunc(func(context.Context, execution.Job) error {
			<-renewStarted
			return nil
		}),
	}, cleanup.WorkerOptions{WorkerID: "worker-1", LeaseDuration: time.Minute, RenewInterval: time.Millisecond, Now: func() time.Time { return now }})
	processed, err := worker.ProcessOne(context.Background())
	if err != nil || !processed || jobs.job.Status != execution.JobSucceeded {
		t.Fatalf("processed=%v job=%#v err=%v", processed, jobs.job, err)
	}
}

func TestWorkerCancelsWaiterWhenLeaseRenewalFails(t *testing.T) {
	now := time.Date(2026, 7, 13, 5, 4, 0, 0, time.UTC)
	leaseLost := errors.New("lease lost")
	jobs := &fakeJobs{
		job: execution.Job{ID: "job-lease", Type: execution.JobWait, Status: execution.JobPending, RunAt: now, CreatedAt: now, UpdatedAt: now},
		renew: func(context.Context, execution.JobID, string, time.Time) error {
			return leaseLost
		},
	}
	waiterCanceled := make(chan struct{})
	worker := cleanup.NewWorker(jobs, map[execution.JobType]cleanup.Handler{
		execution.JobWait: cleanup.HandlerFunc(func(ctx context.Context, _ execution.Job) error {
			<-ctx.Done()
			close(waiterCanceled)
			return ctx.Err()
		}),
	}, cleanup.WorkerOptions{WorkerID: "worker-lease", LeaseDuration: time.Minute, RenewInterval: time.Millisecond, Now: func() time.Time { return now }})
	processed, err := worker.ProcessOne(context.Background())
	if !processed || !errors.Is(err, leaseLost) {
		t.Fatalf("processed=%v err=%v", processed, err)
	}
	select {
	case <-waiterCanceled:
	default:
		t.Fatal("waiter continued after lease loss")
	}
}

func TestWorkerRunReportsJobFailureAndKeepsServing(t *testing.T) {
	now := time.Date(2026, 7, 13, 5, 5, 0, 0, time.UTC)
	jobs := &fakeJobs{job: execution.Job{ID: "job-failed", Type: execution.JobScan, Status: execution.JobPending, RunAt: now, CreatedAt: now, UpdatedAt: now}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	jobFailure := errors.New("provider unavailable")
	reported := make(chan error, 1)
	worker := cleanup.NewWorker(jobs, map[execution.JobType]cleanup.Handler{
		execution.JobScan: cleanup.HandlerFunc(func(context.Context, execution.Job) error { return jobFailure }),
	}, cleanup.WorkerOptions{
		WorkerID: "worker-long-running", LeaseDuration: time.Minute, RenewInterval: time.Hour,
		PollInterval: time.Millisecond, Now: func() time.Time { return now },
		OnError: func(err error) {
			reported <- err
			cancel()
		},
	})

	err := worker.Run(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("run error = %v, want context cancellation instead of job failure", err)
	}
	if got := <-reported; !errors.Is(got, jobFailure) {
		t.Fatalf("reported error = %v", got)
	}
	if jobs.job.Status != execution.JobFailed || jobs.job.LastError != jobFailure.Error() {
		t.Fatalf("job = %#v", jobs.job)
	}
}

func TestWorkerEmitsDurableJobLifecycleLogs(t *testing.T) {
	now := time.Date(2026, 7, 13, 5, 6, 0, 0, time.UTC)
	jobs := &fakeJobs{job: execution.Job{ID: "job-logs", Type: execution.JobScan, Status: execution.JobPending, RunAt: now, CreatedAt: now, UpdatedAt: now}}
	worker := cleanup.NewWorker(jobs, map[execution.JobType]cleanup.Handler{
		execution.JobScan: cleanup.HandlerFunc(func(ctx context.Context, _ execution.Job) error {
			execution.LogCloudAPIRequest(ctx, "resource-center", "SearchResources", map[string]any{"Operation": "SearchResources"})
			execution.LogCloudAPIResponse(ctx, "resource-center", "SearchResources", map[string]any{"RequestId": "req-1"})
			return nil
		}),
	}, cleanup.WorkerOptions{WorkerID: "worker-logs", LeaseDuration: time.Minute, RenewInterval: time.Hour, Now: func() time.Time { return now }})

	processed, err := worker.ProcessOne(context.Background())
	if err != nil || !processed {
		t.Fatalf("processed=%v err=%v", processed, err)
	}
	if len(jobs.logs) != 4 ||
		jobs.logs[0].Kind != execution.JobLogText ||
		jobs.logs[0].Message != "job claimed by worker-logs" ||
		jobs.logs[0].Payload != nil ||
		jobs.logs[1].Kind != execution.JobLogCloudAPIRequest ||
		jobs.logs[2].Kind != execution.JobLogCloudAPIResponse ||
		jobs.logs[3].Kind != execution.JobLogText ||
		jobs.logs[3].Message != "job handler succeeded" ||
		jobs.logs[3].Payload != nil {
		t.Fatalf("logs=%+v", jobs.logs)
	}
	seen := map[int64]struct{}{}
	for index, event := range jobs.logs {
		if _, exists := seen[event.Sequence]; exists {
			t.Fatalf("duplicate sequence %d in logs=%+v", event.Sequence, jobs.logs)
		}
		seen[event.Sequence] = struct{}{}
		if index > 0 && jobs.logs[index-1].Sequence >= event.Sequence {
			t.Fatalf("sequences are not strictly increasing: logs=%+v", jobs.logs)
		}
	}
}

func TestWorkerFailureLogIncludesProviderRequestID(t *testing.T) {
	now := time.Date(2026, 8, 6, 17, 0, 0, 0, time.UTC)
	jobs := &fakeJobs{job: execution.Job{
		ID: "job-provider-failure", Type: execution.JobScan, Status: execution.JobPending,
		RunAt: now, CreatedAt: now, UpdatedAt: now,
	}}
	providerFailure := &contracts.ProviderCallError{Provider: execution.ProviderError{
		Category:  execution.ErrorRetryable,
		Code:      "ServiceUnavailable",
		Message:   "temporary failure",
		RequestID: "provider-request",
	}}
	worker := cleanup.NewWorker(jobs, map[execution.JobType]cleanup.Handler{
		execution.JobScan: cleanup.HandlerFunc(func(context.Context, execution.Job) error {
			return providerFailure
		}),
	}, cleanup.WorkerOptions{
		WorkerID: "worker-provider-failure", LeaseDuration: time.Minute,
		RenewInterval: time.Hour, Now: func() time.Time { return now },
	})

	processed, err := worker.ProcessOne(context.Background())
	if !processed || !errors.Is(err, providerFailure) {
		t.Fatalf("processed=%v err=%v", processed, err)
	}
	wantError := "ServiceUnavailable: temporary failure; request_id=provider-request"
	if jobs.job.Status != execution.JobFailed || jobs.job.LastError != wantError {
		t.Fatalf("job=%#v, want error %q", jobs.job, wantError)
	}
	if len(jobs.logs) != 2 ||
		jobs.logs[1].Level != "error" ||
		jobs.logs[1].Message != "job handler failed: "+wantError {
		t.Fatalf("logs=%+v", jobs.logs)
	}
}

type fakeJobs struct {
	mu      sync.Mutex
	job     execution.Job
	logs    []execution.JobLog
	renewed chan struct{}
	renew   func(context.Context, execution.JobID, string, time.Time) error
}

func (f *fakeJobs) Enqueue(_ context.Context, job execution.Job) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.job = job
	return nil
}

func (f *fakeJobs) ClaimNext(_ context.Context, owner string, now time.Time, lease time.Duration, allowedTypes ...execution.JobType) (execution.Job, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	available := f.job.Status == execution.JobPending || (f.job.Status == execution.JobRunning && f.job.LeaseUntil != nil && !f.job.LeaseUntil.After(now))
	if !available || f.job.RunAt.After(now) {
		return execution.Job{}, persistence.ErrNotFound
	}
	until := now.Add(lease)
	f.job.Status = execution.JobRunning
	f.job.LeaseOwner = owner
	f.job.LeaseUntil = &until
	f.job.Attempts++
	return f.job, nil
}

func (f *fakeJobs) ListJobsByAggregate(_ context.Context, aggregateType, aggregateID string) ([]execution.Job, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.job.AggregateType != aggregateType || f.job.AggregateID != aggregateID {
		return nil, nil
	}
	return []execution.Job{f.job}, nil
}

func (f *fakeJobs) ListJobsByAggregates(_ context.Context, aggregateType string, aggregateIDs []string) (map[string][]execution.Job, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	result := make(map[string][]execution.Job)
	if f.job.AggregateType != aggregateType {
		return result, nil
	}
	for _, aggregateID := range aggregateIDs {
		if f.job.AggregateID == aggregateID {
			result[aggregateID] = []execution.Job{f.job}
			break
		}
	}
	return result, nil
}

func (f *fakeJobs) UpdateJob(_ context.Context, job execution.Job) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.job.ID != job.ID {
		return persistence.ErrNotFound
	}
	f.job = job
	return nil
}

func (f *fakeJobs) RenewLease(ctx context.Context, id execution.JobID, owner string, until time.Time) error {
	if f.renew != nil {
		return f.renew(ctx, id, owner, until)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.job.ID != id || f.job.LeaseOwner != owner {
		return persistence.ErrNotFound
	}
	f.job.LeaseUntil = &until
	if f.renewed != nil {
		select {
		case f.renewed <- struct{}{}:
		default:
		}
	}
	return nil
}

func (f *fakeJobs) Complete(_ context.Context, id execution.JobID, owner string, status execution.JobStatus, lastError string, finishedAt time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.job.ID != id || f.job.LeaseOwner != owner {
		return persistence.ErrNotFound
	}
	f.job.Status = status
	f.job.LastError = lastError
	f.job.FinishedAt = &finishedAt
	f.job.LeaseOwner = ""
	f.job.LeaseUntil = nil
	return nil
}

func (f *fakeJobs) Reschedule(_ context.Context, id execution.JobID, owner string, runAt time.Time, lastError string, updatedAt time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.job.ID != id || f.job.LeaseOwner != owner {
		return persistence.ErrNotFound
	}
	f.job.Status = execution.JobPending
	f.job.RunAt = runAt
	f.job.LastError = lastError
	f.job.UpdatedAt = updatedAt
	f.job.LeaseOwner = ""
	f.job.LeaseUntil = nil
	return nil
}

func (f *fakeJobs) GetJob(context.Context, execution.JobID) (execution.Job, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.job, nil
}

func (f *fakeJobs) GetJobByIdempotencyKey(_ context.Context, key string) (execution.Job, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.job.IdempotencyKey != key {
		return execution.Job{}, persistence.ErrNotFound
	}
	return f.job, nil
}

func (f *fakeJobs) FindActiveByType(_ context.Context, connectionID asset.ConnectionID, jobType execution.JobType) (execution.Job, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.job.ConnectionID != connectionID || f.job.Type != jobType || (f.job.Status != execution.JobPending && f.job.Status != execution.JobRunning) {
		return execution.Job{}, persistence.ErrNotFound
	}
	return f.job, nil
}

func (f *fakeJobs) FindLatestByType(_ context.Context, connectionID asset.ConnectionID, jobType execution.JobType) (execution.Job, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.job.ConnectionID != connectionID || f.job.Type != jobType {
		return execution.Job{}, persistence.ErrNotFound
	}
	return f.job, nil
}

func (f *fakeJobs) AppendLog(_ context.Context, event execution.JobLog) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.logs = append(f.logs, event)
	return nil
}

func (f *fakeJobs) ListLogs(context.Context, execution.JobID, int64, int) ([]execution.JobLog, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]execution.JobLog(nil), f.logs...), nil
}

func (f *fakeJobs) ListLogsByAggregate(context.Context, string, string, string, time.Time, string, int) ([]execution.JobLog, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]execution.JobLog(nil), f.logs...), nil
}

func (f *fakeJobs) ListLogsByAggregateBefore(context.Context, string, string, string, time.Time, string, int) ([]execution.JobLog, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]execution.JobLog(nil), f.logs...), nil
}

func (f *fakeJobs) ListScanLogsBefore(context.Context, asset.ConnectionID, asset.ScanTaskID, string, time.Time, string, int) (asset.ScanTask, []execution.JobLog, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return asset.ScanTask{}, append([]execution.JobLog(nil), f.logs...), nil
}

func (f *fakeJobs) ListCleanupLogsAfter(context.Context, asset.ConnectionID, plan.CleanupTaskID, persistence.CleanupLogFilter, time.Time, string, int) (plan.CleanupTask, []execution.JobLog, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return plan.CleanupTask{}, append([]execution.JobLog(nil), f.logs...), nil
}

func (f *fakeJobs) ListCleanupLogsBefore(context.Context, asset.ConnectionID, plan.CleanupTaskID, persistence.CleanupLogFilter, time.Time, string, int) (plan.CleanupTask, []execution.JobLog, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return plan.CleanupTask{}, append([]execution.JobLog(nil), f.logs...), nil
}

var _ persistence.JobRepository = (*fakeJobs)(nil)
