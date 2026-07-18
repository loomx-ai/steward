package cleanup

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/core/graph"
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/internal/core/requestmeta"
	"github.com/loomx-ai/steward/internal/idgen"
	"github.com/loomx-ai/steward/internal/persistence"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

type Handler interface {
	Handle(context.Context, execution.Job) error
}

type JobControlChecker interface {
	CheckJobControl(context.Context, execution.Job) error
}

type JobSettlementReconciler interface {
	ReconcileJobSettlement(context.Context, execution.Job) error
}

type HandlerFunc func(context.Context, execution.Job) error

func (f HandlerFunc) Handle(ctx context.Context, job execution.Job) error {
	return f(ctx, job)
}

type WorkerOptions struct {
	WorkerID      string
	AllowedTypes  []execution.JobType
	Concurrency   int
	LeaseDuration time.Duration
	RenewInterval time.Duration
	PollInterval  time.Duration
	Now           func() time.Time
	OnError       func(error)
}

type Worker struct {
	jobs     persistence.JobRepository
	handlers map[execution.JobType]Handler
	options  WorkerOptions
}

const jobLogAttemptStride int64 = 1_000_000

// DefaultDeletionCheckTimeout bounds the complete waiter/readback cycle for
// one resource after the provider accepts its deletion request.
const DefaultDeletionCheckTimeout = 20 * time.Second

// ExecutionWorkerConcurrency is the number of durable cleanup jobs the server
// may poll or advance concurrently. The separate per-execution concurrency
// setting limits resources across their complete provider deletion lifecycle.
const ExecutionWorkerConcurrency = 20

const defaultDeletePollInterval = 2 * time.Second

const (
	defaultManagedVerificationTimeout  = 2 * time.Minute
	defaultManagedVerificationPoll     = 15 * time.Second
	providerInvokeRetryCountRequestKey = "_provider_invoke_retry_count"
	maxVerifiedProviderInvokeRetries   = 3
)

type jobLogEmitter struct {
	mu   sync.Mutex
	jobs persistence.JobRepository
	job  execution.Job
	now  func() time.Time
	next int64
}

func newJobLogEmitter(jobs persistence.JobRepository, job execution.Job, now func() time.Time) *jobLogEmitter {
	return &jobLogEmitter{
		jobs: jobs,
		job:  job,
		now:  now,
		next: int64(job.Attempts) * jobLogAttemptStride,
	}
}

func (e *jobLogEmitter) Log(ctx context.Context, entry execution.JobLogEntry) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.next++
	err := e.jobs.AppendLog(ctx, execution.JobLog{
		ID:              idgen.MustNew("log"),
		JobID:           e.job.ID,
		AggregateType:   e.job.AggregateType,
		AggregateID:     e.job.AggregateID,
		TargetKey:       e.job.TargetKey,
		RetryGeneration: e.job.RetryGeneration,
		Sequence:        e.next,
		Kind:            entry.Kind,
		Level:           entry.Level,
		Message:         entry.Message,
		Payload:         entry.Payload,
		CreatedAt:       e.now(),
	})
	if err != nil {
		slog.ErrorContext(ctx, "append durable job log", "job_id", e.job.ID, "message", entry.Message, "error", err)
	}
}

func NewWorker(jobs persistence.JobRepository, handlers map[execution.JobType]Handler, options WorkerOptions) *Worker {
	if options.Concurrency <= 0 {
		options.Concurrency = 1
	}
	if options.LeaseDuration <= 0 {
		options.LeaseDuration = time.Minute
	}
	if options.RenewInterval <= 0 {
		options.RenewInterval = options.LeaseDuration / 3
	}
	if options.PollInterval <= 0 {
		options.PollInterval = time.Second
	}
	if options.Now == nil {
		options.Now = func() time.Time { return time.Now().UTC() }
	}
	return &Worker{jobs: jobs, handlers: handlers, options: options}
}

func (w *Worker) Run(ctx context.Context) error {
	if w.options.Concurrency == 1 {
		return w.runLoop(ctx, w.options.WorkerID)
	}
	var workers sync.WaitGroup
	workers.Add(w.options.Concurrency)
	for slot := range w.options.Concurrency {
		workerID := fmt.Sprintf("%s-%02d", w.options.WorkerID, slot+1)
		go func() {
			defer workers.Done()
			_ = w.runLoop(ctx, workerID)
		}()
	}
	workers.Wait()
	return ctx.Err()
}

func (w *Worker) runLoop(ctx context.Context, workerID string) error {
	ticker := time.NewTicker(w.options.PollInterval)
	defer ticker.Stop()
	for {
		processed, err := w.processOne(ctx, workerID)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if w.options.OnError != nil {
				w.options.OnError(err)
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-ticker.C:
				continue
			}
		}
		if processed {
			continue
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (w *Worker) ProcessOne(ctx context.Context) (bool, error) {
	return w.processOne(ctx, w.options.WorkerID)
}

func (w *Worker) processOne(ctx context.Context, workerID string) (bool, error) {
	now := w.options.Now()
	job, err := w.jobs.ClaimNext(ctx, workerID, now, w.options.LeaseDuration, w.options.AllowedTypes...)
	if errors.Is(err, persistence.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	logs := newJobLogEmitter(w.jobs, job, w.options.Now)
	logs.Log(ctx, execution.JobLogEntry{
		Kind: execution.JobLogText, Level: "info",
		Message: fmt.Sprintf("job claimed by %s", workerID),
	})

	handler, ok := w.handlers[job.Type]
	if !ok {
		err := fmt.Errorf("no handler registered for job type %s", job.Type)
		logs.Log(ctx, execution.JobLogEntry{Kind: execution.JobLogText, Level: "error", Message: err.Error()})
		_ = w.jobs.Complete(ctx, job.ID, workerID, execution.JobFailed, err.Error(), w.options.Now())
		return true, err
	}

	handlerCtx, cancel := context.WithCancel(ctx)
	if requestID, ok := job.Payload["request_id"].(string); ok && strings.TrimSpace(requestID) != "" {
		handlerCtx = requestmeta.WithRequestID(handlerCtx, requestID)
	}
	handlerCtx = execution.WithJobLogSink(handlerCtx, logs)
	renewalDone := make(chan error, 1)
	go w.renewLease(handlerCtx, job.ID, workerID, cancel, renewalDone)
	handleErr := handler.Handle(handlerCtx, job)
	cancel()
	if renewalErr := <-renewalDone; renewalErr != nil {
		// Once renewal fails this worker no longer owns the right to commit a
		// terminal job result. Leave the running lease to expire so another
		// worker can safely reclaim the persisted action state.
		logs.Log(ctx, execution.JobLogEntry{
			Kind: execution.JobLogText, Level: "error",
			Message: fmt.Sprintf("job lease renewal failed: %s", renewalErr),
		})
		return true, renewalErr
	}
	if checker, ok := handler.(JobControlChecker); ok {
		if controlErr := checker.CheckJobControl(ctx, job); controlErr != nil {
			var controlled *execution.JobStatusError
			if errors.As(controlErr, &controlled) {
				handleErr = controlErr
			} else if handleErr == nil {
				handleErr = controlErr
			}
		}
	}

	completedAt := w.options.Now()
	if handleErr == nil {
		logs.Log(ctx, execution.JobLogEntry{Kind: execution.JobLogText, Level: "info", Message: "job handler succeeded"})
		if err := w.jobs.Complete(ctx, job.ID, workerID, execution.JobSucceeded, "", completedAt); err != nil {
			return true, err
		}
		if reconciler, ok := handler.(JobSettlementReconciler); ok {
			if err := reconciler.ReconcileJobSettlement(ctx, job); err != nil {
				return true, err
			}
		}
		return true, nil
	}
	var controlled *execution.JobStatusError
	if errors.As(handleErr, &controlled) && (controlled.Status == execution.JobPaused || controlled.Status == execution.JobCanceled) {
		logs.Log(ctx, execution.JobLogEntry{
			Kind: execution.JobLogText, Level: "info",
			Message: fmt.Sprintf("job stopped at aggregate control checkpoint: %s", controlled.Status),
		})
		if err := w.jobs.Complete(ctx, job.ID, workerID, controlled.Status, "", completedAt); err != nil {
			return true, err
		}
		if reconciler, ok := handler.(JobSettlementReconciler); ok {
			if err := reconciler.ReconcileJobSettlement(ctx, job); err != nil {
				return true, err
			}
		}
		return true, nil
	}
	var retry *RetryError
	if errors.As(handleErr, &retry) {
		logs.Log(ctx, execution.JobLogEntry{
			Kind: execution.JobLogText, Level: "info",
			Message: fmt.Sprintf("job rescheduled after %s: %s", retry.After, retry.Err),
		})
		if err := w.jobs.Reschedule(ctx, job.ID, workerID, completedAt.Add(retry.After), retry.Err.Error(), completedAt); err != nil {
			return true, err
		}
		if reconciler, ok := handler.(JobSettlementReconciler); ok {
			if err := reconciler.ReconcileJobSettlement(ctx, job); err != nil {
				return true, err
			}
		}
		return true, nil
	}
	logs.Log(ctx, execution.JobLogEntry{
		Kind: execution.JobLogText, Level: "error",
		Message: fmt.Sprintf("job handler failed: %s", handleErr),
	})
	if err := w.jobs.Complete(ctx, job.ID, workerID, execution.JobFailed, handleErr.Error(), completedAt); err != nil {
		return true, err
	}
	if reconciler, ok := handler.(JobSettlementReconciler); ok {
		if err := reconciler.ReconcileJobSettlement(ctx, job); err != nil {
			return true, err
		}
	}
	return true, handleErr
}

func (w *Worker) renewLease(ctx context.Context, jobID execution.JobID, workerID string, cancel context.CancelFunc, done chan<- error) {
	ticker := time.NewTicker(w.options.RenewInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			done <- nil
			return
		case <-ticker.C:
			if err := w.jobs.RenewLease(ctx, jobID, workerID, w.options.Now().Add(w.options.LeaseDuration)); err != nil {
				if ctx.Err() != nil {
					done <- nil
					return
				}
				cancel()
				done <- err
				return
			}
		}
	}
}

type RetryError struct {
	Err   error
	After time.Duration
}

func (e *RetryError) Error() string { return e.Err.Error() }
func (e *RetryError) Unwrap() error { return e.Err }

func RetryAfter(err error, after time.Duration) error {
	return &RetryError{Err: err, After: after}
}

type ActionDriver = contracts.ActionDriver

type ActionResolver interface {
	ResolveAction(context.Context, asset.Asset) (ActionDriver, error)
}

type ActionResolverFunc func(context.Context, asset.Asset) (ActionDriver, error)

func (f ActionResolverFunc) ResolveAction(ctx context.Context, value asset.Asset) (ActionDriver, error) {
	return f(ctx, value)
}

type ProviderActionRegistry interface {
	ResolveAction(context.Context, asset.ConnectionID, asset.Asset) (contracts.ActionDriver, error)
}

type RepositoryActionResolver struct {
	connections persistence.ConnectionRepository
	providers   ProviderActionRegistry
}

type activeConnectionDriver struct {
	connections  persistence.ConnectionRepository
	connectionID asset.ConnectionID
	driver       contracts.ActionDriver
}

func NewRepositoryActionResolver(connections persistence.ConnectionRepository, providers ProviderActionRegistry) *RepositoryActionResolver {
	return &RepositoryActionResolver{connections: connections, providers: providers}
}

func (r *RepositoryActionResolver) ResolveAction(ctx context.Context, value asset.Asset) (ActionDriver, error) {
	if r == nil || r.connections == nil || r.providers == nil {
		return nil, fmt.Errorf("repository action resolver requires connections and provider registry")
	}
	connection, err := r.connections.GetConnection(ctx, value.Identity.ConnectionID)
	if err != nil {
		return nil, err
	}
	if connection.Status != asset.ConnectionActive {
		return nil, fmt.Errorf("%w: connection %q status is %q", asset.ErrConnectionNotValidated, connection.ID, connection.Status)
	}
	if connection.Provider != value.Identity.Provider {
		return nil, fmt.Errorf("asset provider %q does not match connection provider %q", value.Identity.Provider, connection.Provider)
	}
	driver, err := r.providers.ResolveAction(ctx, connection.ID, value)
	if err != nil || driver == nil {
		return driver, err
	}
	return &activeConnectionDriver{
		connections: r.connections, connectionID: connection.ID, driver: driver,
	}, nil
}

func (d *activeConnectionDriver) requireActive(ctx context.Context) error {
	connection, err := d.connections.GetConnection(ctx, d.connectionID)
	if err != nil {
		return err
	}
	if connection.Status != asset.ConnectionActive {
		return fmt.Errorf("%w: connection %q status is %q", asset.ErrConnectionNotValidated, connection.ID, connection.Status)
	}
	return nil
}

func (d *activeConnectionDriver) Preflight(ctx context.Context, request contracts.ActionRequest) (contracts.PreflightResult, error) {
	if err := d.requireActive(ctx); err != nil {
		return contracts.PreflightResult{}, err
	}
	return d.driver.Preflight(ctx, request)
}

func (d *activeConnectionDriver) Execute(ctx context.Context, request contracts.ActionRequest) (contracts.ActionResult, error) {
	if err := d.requireActive(ctx); err != nil {
		return contracts.ActionResult{}, err
	}
	result, err := d.driver.Execute(ctx, request)
	if err == nil && result.RetryAfter <= 0 {
		result.RetryAfter = defaultDeletePollInterval
	}
	return result, err
}

func (d *activeConnectionDriver) Wait(ctx context.Context, request contracts.ActionRequest, result contracts.ActionResult) (contracts.WaitResult, error) {
	if err := d.requireActive(ctx); err != nil {
		return contracts.WaitResult{}, err
	}
	return d.driver.Wait(ctx, request, result)
}

func (d *activeConnectionDriver) Readback(ctx context.Context, request contracts.ActionRequest) (contracts.ReadbackResult, error) {
	if err := d.requireActive(ctx); err != nil {
		return contracts.ReadbackResult{}, err
	}
	return d.driver.Readback(ctx, request)
}

func (d *activeConnectionDriver) DeletionCheckTimeout() time.Duration {
	if provider, ok := d.driver.(contracts.DeletionCheckTimeoutProvider); ok {
		return provider.DeletionCheckTimeout()
	}
	return 0
}

type ExecutionHandlerOption func(*ExecutionHandler)

type ExecutionHandler struct {
	planner              *Service
	resolver             ActionResolver
	retryDelay           time.Duration
	deletionCheckTimeout time.Duration
	executionStateMu     sync.Mutex
	afterIntentPersisted func(execution.ActionAttempt) error
	afterProviderCall    func(execution.ActionAttempt, contracts.ActionResult) error
}

func NewExecutionHandler(planner *Service, resolver ActionResolver, options ...ExecutionHandlerOption) *ExecutionHandler {
	handler := &ExecutionHandler{
		planner: planner, resolver: resolver,
		retryDelay: 5 * time.Second, deletionCheckTimeout: DefaultDeletionCheckTimeout,
	}
	for _, option := range options {
		option(handler)
	}
	return handler
}

func WithDeletionCheckTimeout(timeout time.Duration) ExecutionHandlerOption {
	return func(handler *ExecutionHandler) {
		if timeout > 0 {
			handler.deletionCheckTimeout = timeout
		}
	}
}

func WithAfterIntentPersisted(hook func(execution.ActionAttempt) error) ExecutionHandlerOption {
	return func(handler *ExecutionHandler) { handler.afterIntentPersisted = hook }
}

func WithAfterProviderCall(hook func(execution.ActionAttempt, contracts.ActionResult) error) ExecutionHandlerOption {
	return func(handler *ExecutionHandler) { handler.afterProviderCall = hook }
}

func WithExecutionRetryDelay(delay time.Duration) ExecutionHandlerOption {
	return func(handler *ExecutionHandler) {
		if delay > 0 {
			handler.retryDelay = delay
		}
	}
}

func (h *ExecutionHandler) CheckJobControl(ctx context.Context, job execution.Job) error {
	executionID := execution.ExecutionID(payloadString(job.Payload, "execution_id"))
	cleanupTaskID := plan.CleanupTaskID(job.AggregateID)
	if cleanupTaskID == "" {
		cleanupTaskID = plan.CleanupTaskID(payloadString(job.Payload, "cleanup_task_id"))
	}
	if executionID == "" || cleanupTaskID == "" {
		return nil
	}
	return h.checkExecutionControl(ctx, executionID, cleanupTaskID)
}

func (h *ExecutionHandler) checkExecutionControl(
	ctx context.Context,
	executionID execution.ExecutionID,
	cleanupTaskID plan.CleanupTaskID,
) error {
	attempt, err := h.planner.repositories.Executions().GetExecution(ctx, executionID)
	if err != nil {
		return err
	}
	aggregate, err := h.planner.repositories.CleanupTasks().GetTask(ctx, cleanupTaskID)
	if err != nil {
		return err
	}
	if attempt.Status == execution.ExecutionPausing || attempt.Status == execution.ExecutionPaused ||
		aggregate.Task.Status == plan.StatusPausing || aggregate.Task.Status == plan.StatusPaused {
		return &execution.JobStatusError{
			Status: execution.JobPaused,
			Cause:  fmt.Errorf("cleanup task %s is paused", cleanupTaskID),
		}
	}
	return nil
}

func (h *ExecutionHandler) ReconcileJobSettlement(ctx context.Context, job execution.Job) error {
	executionID := execution.ExecutionID(payloadString(job.Payload, "execution_id"))
	cleanupTaskID := plan.CleanupTaskID(job.AggregateID)
	if cleanupTaskID == "" {
		cleanupTaskID = plan.CleanupTaskID(payloadString(job.Payload, "cleanup_task_id"))
	}
	if executionID == "" || cleanupTaskID == "" {
		return nil
	}

	h.executionStateMu.Lock()
	defer h.executionStateMu.Unlock()
	return h.planner.repositories.WithTx(ctx, func(repositories persistence.Repositories) error {
		attempt, err := repositories.Executions().GetExecution(ctx, executionID)
		if err != nil {
			return err
		}
		aggregate, err := repositories.CleanupTasks().GetTask(ctx, cleanupTaskID)
		if err != nil {
			return err
		}
		attemptControlled := attempt.Status == execution.ExecutionPausing || attempt.Status == execution.ExecutionPaused
		taskControlled := aggregate.Task.Status == plan.StatusPausing || aggregate.Task.Status == plan.StatusPaused
		jobs, err := repositories.Jobs().ListJobsByAggregate(ctx, "cleanup_task", string(cleanupTaskID))
		if err != nil {
			return err
		}
		if !attemptControlled && !taskControlled {
			if aggregate.Task.Status != plan.StatusExecuting || !pausableExecutionStatus(attempt.Status) {
				return nil
			}
			for _, current := range jobs {
				if current.ID != job.ID || current.Status != execution.JobPaused {
					continue
				}
				now := h.planner.clock()
				current.Status = execution.JobPending
				current.RunAt = now
				current.LeaseOwner = ""
				current.LeaseUntil = nil
				current.LastError = ""
				current.FinishedAt = nil
				current.UpdatedAt = now
				return repositories.Jobs().UpdateJob(ctx, current)
			}
			return nil
		}
		for _, current := range jobs {
			if current.ID != job.ID || current.Status != execution.JobPending {
				continue
			}
			current.Status = execution.JobPaused
			current.UpdatedAt = h.planner.clock()
			current.LeaseOwner = ""
			current.LeaseUntil = nil
			if err := repositories.Jobs().UpdateJob(ctx, current); err != nil {
				return err
			}
			break
		}
		for _, current := range jobs {
			if payloadString(current.Payload, "execution_id") == string(executionID) && current.Status == execution.JobRunning {
				return nil
			}
		}
		attempt.Status = execution.ExecutionPaused
		aggregate.Task.Status = plan.StatusPaused
		if err := repositories.Executions().UpdateExecution(ctx, attempt); err != nil {
			return err
		}
		return repositories.CleanupTasks().UpdateTask(ctx, aggregate.Task)
	})
}

func (h *ExecutionHandler) Handle(ctx context.Context, job execution.Job) error {
	if h == nil || h.planner == nil || h.planner.repositories == nil || h.resolver == nil {
		return fmt.Errorf("execution handler requires planner and action resolver")
	}
	executionID := execution.ExecutionID(payloadString(job.Payload, "execution_id"))
	stepID := plan.StepID(payloadString(job.Payload, "cleanup_task_step_id"))
	if executionID == "" || stepID == "" {
		return fmt.Errorf("execution job requires execution_id and cleanup_task_step_id")
	}
	if err := h.CheckJobControl(ctx, job); err != nil {
		return err
	}
	attempt, err := h.planner.repositories.Executions().GetExecution(ctx, executionID)
	if err != nil {
		return err
	}
	if attempt.Status == execution.ExecutionSucceeded || attempt.Status == execution.ExecutionCanceled {
		return nil
	}
	// Exactly one concurrent resource job performs the execution-level
	// freshness check. Other independent jobs wait here until the immutable
	// cleanup snapshot has either been accepted or rejected.
	if attempt.Status == execution.ExecutionPending {
		attempt, err = h.startExecution(ctx, attempt.ID)
		if err != nil {
			if errors.Is(err, ErrInventoryReconciliationPending) {
				execution.LogJob(ctx, "info", "waiting for inventory relationship reconciliation")
			}
			return err
		}
	}
	if attempt.Status == execution.ExecutionFailed && attempt.StartedAt == nil {
		return fmt.Errorf("cleanup execution failed before resource actions started: %s", attempt.FailureReason)
	}
	aggregate, err := h.planner.repositories.CleanupTasks().GetTask(ctx, plan.CleanupTaskID(attempt.CleanupTaskID))
	if err != nil {
		return err
	}
	relationships, err := h.planner.repositories.Graph().ListRelationshipsByAssetIDs(
		ctx,
		aggregate.Task.ResolvedAssetIDs,
	)
	if err != nil {
		return err
	}
	var inferredDependencies int
	aggregate.Steps, inferredDependencies = appendCreatedFromTaskDependencies(
		aggregate.Steps,
		relationships,
	)
	step, ok := findCleanupTaskStep(aggregate.Steps, stepID)
	if !ok {
		return fmt.Errorf("cleanup task step %q is not part of cleanup task %q", stepID, attempt.CleanupTaskID)
	}
	if inferredDependencies > 0 {
		execution.LogJob(ctx, "info", "enforcing created-from cleanup dependency")
	}
	existingAction, actionErr := h.planner.repositories.Executions().GetActionByExecutionStep(ctx, attempt.ID, string(step.ID))
	if actionErr != nil && !errors.Is(actionErr, persistence.ErrNotFound) {
		return actionErr
	}
	verificationOnly := step.Action == plan.ActionVerifyManagedAbsent ||
		actionRequestsManagedVerification(existingAction)
	execution.LogJob(ctx, "info", fmt.Sprintf("cleanup action %s started", step.Action))
	// A failed action only blocks its dependency chain. Independent steps may
	// still start and finish so one provider failure does not become a global
	// cleanup circuit breaker.
	if len(step.DependsOn) > 0 || verificationOnly {
		execution.LogJob(ctx, "info", "checking cleanup dependencies")
		if err := h.ensureDependencies(ctx, attempt.ID, step, aggregate.Steps); err != nil {
			var failed *failedDependencyError
			if errors.As(err, &failed) {
				execution.LogJob(ctx, "warn", fmt.Sprintf(
					"cleanup action is blocked by failed dependency %s",
					failed.stepID,
				))
				// A dependent resource remains in waiting state. It must not be
				// invoked after its prerequisite failed, but this job is settled so
				// the execution can converge to failed instead of remaining running.
				return h.finalizeExecution(ctx, attempt, aggregate)
			}
			var retained *retainedDependencyError
			if errors.As(err, &retained) {
				action, _, intentErr := h.ensureActionIntent(ctx, attempt, aggregate.Task, step)
				if intentErr != nil {
					return intentErr
				}
				providerError := execution.ProviderError{
					Category: execution.ErrorUnsupported,
					Code:     "CleanupSkipped.RetainedDependency",
					Message: fmt.Sprintf(
						"cleanup dependency %s was retained; retaining dependent resource",
						retained.stepID,
					),
					Summary: map[string]any{
						"dependency_step_id": retained.stepID,
						"dependency_reason":  retained.skipReason,
					},
				}
				execution.LogJob(ctx, "warn", providerError.Message)
				if skipErr := h.skipUnsupportedAction(
					ctx,
					attempt,
					aggregate,
					step,
					&action,
					providerError,
					string(asset.SkipDependencyRetained),
				); skipErr != nil {
					return skipErr
				}
				return h.finalizeExecution(ctx, attempt, aggregate)
			}
			return err
		}
	}
	if actionErr == nil && (existingAction.Status == execution.ActionSucceeded || existingAction.Status == execution.ActionSkipped || existingAction.Status == execution.ActionFailed) {
		execution.LogJob(ctx, "info", fmt.Sprintf("cleanup action already reached %s", existingAction.Status))
		return h.finalizeExecution(ctx, attempt, aggregate)
	}
	value, err := h.planner.repositories.Inventory().GetAsset(ctx, step.AssetID)
	if err != nil {
		return err
	}
	if !verificationOnly &&
		value.Dirty &&
		(errors.Is(actionErr, persistence.ErrNotFound) ||
			existingAction.Status == execution.ActionIntentPersisted) {
		action, _, err := h.ensureActionIntent(ctx, attempt, aggregate.Task, step)
		if err != nil {
			return err
		}
		execution.LogJob(ctx, "info", "cleanup action ignored because the resource is marked as dirty data")
		if err := h.skipDirtyAction(ctx, attempt, aggregate, step, &action); err != nil {
			return err
		}
		return h.finalizeExecution(ctx, attempt, aggregate)
	}
	// A dirty mark added after a provider operation has begun cannot safely
	// cancel that operation. Only actions that have not left persisted intent
	// are eligible for the dirty-data execution guard above.
	if verificationOnly && controllerDeletionImpliesAssetAbsence(step, value) {
		action, created, err := h.ensureActionIntent(ctx, attempt, aggregate.Task, step)
		if err != nil {
			return err
		}
		if created && h.afterIntentPersisted != nil {
			if err := h.afterIntentPersisted(action); err != nil {
				return err
			}
		}
		execution.LogJob(
			ctx,
			"info",
			"controller-integrated resource inherits the controller deletion result; provider readback is suppressed",
		)
		if err := h.completeControllerIntegratedVerification(
			ctx,
			attempt,
			aggregate,
			step,
			value,
			&action,
		); err != nil {
			return err
		}
		return h.finalizeExecution(ctx, attempt, aggregate)
	}
	driver, err := h.resolver.ResolveAction(ctx, value)
	if err != nil {
		return err
	}
	if driver == nil {
		return fmt.Errorf("action resolver returned no driver for asset %q", value.ID)
	}
	action, created, err := h.ensureActionIntent(ctx, attempt, aggregate.Task, step)
	if err != nil {
		return err
	}
	if created && h.afterIntentPersisted != nil {
		if err := h.afterIntentPersisted(action); err != nil {
			return err
		}
	}
	verificationOnly = step.Action == plan.ActionVerifyManagedAbsent ||
		actionRequestsManagedVerification(action)
	providerAction := step.Action
	if verificationOnly {
		providerAction = "delete"
	}
	request := contracts.ActionRequest{
		Asset: value, Action: providerAction, Parameters: cloneRequest(step.RequestOptions),
		IdempotencyKey: resumedProviderIdempotencyKey(attempt, action),
	}
	if verificationOnly {
		return h.handleManagedAbsenceVerification(
			ctx,
			attempt,
			aggregate,
			step,
			value,
			driver,
			request,
			&action,
		)
	}
	deletionCheckTimeout := h.deletionCheckTimeout
	if provider, ok := driver.(contracts.DeletionCheckTimeoutProvider); ok {
		if configured := provider.DeletionCheckTimeout(); configured > 0 {
			deletionCheckTimeout = configured
		}
	}

	for transitions := 0; transitions < 8; transitions++ {
		if err := h.checkExecutionControl(ctx, attempt.ID, aggregate.Task.ID); err != nil {
			return err
		}
		switch action.Status {
		case execution.ActionIntentPersisted:
			execution.LogJob(ctx, "info", "cleanup intent persisted")
			if err := action.Transition(execution.ActionInvoking); err != nil {
				return err
			}
			if err := h.updateAction(ctx, &action); err != nil {
				return err
			}
		case execution.ActionInvoking:
			if terminalDeterministicProviderRejection(action) {
				execution.LogJob(ctx, "error", "previous provider action error is terminal; automatic delete retry suppressed")
				return h.failAction(ctx, &attempt, aggregate, step, &action, *action.ProviderError)
			}
			execution.LogJob(ctx, "info", "checking whether the resource exists before deletion")
			preflight, err := driver.Preflight(ctx, request)
			if err != nil {
				if isProviderCategory(err, execution.ErrorNotFound) ||
					isVerifiedAbsentProviderError(err) {
					execution.LogJob(ctx, "info", "provider reports resource already absent; delete is suppressed")
					if completeErr := h.completeAbsentAction(ctx, attempt, aggregate, step, value, &action, err); completeErr != nil {
						return completeErr
					}
					execution.LogJob(ctx, "success", "cleanup action succeeded")
					return h.finalizeExecution(ctx, attempt, aggregate)
				}
				return h.handleProviderError(ctx, &attempt, aggregate, step, &action, err, true)
			}
			action.PreflightEvidence = cloneRequest(preflight.Evidence)
			if action.ProviderRequestID == "" {
				action.ProviderRequestID = preflightProviderRequestID(preflight.Evidence)
			}
			if preflight.Absent {
				execution.LogJob(ctx, "info", "provider query confirmed resource absent; delete is suppressed")
				if err := h.completeAbsentAction(ctx, attempt, aggregate, step, value, &action, nil); err != nil {
					return err
				}
				execution.LogJob(ctx, "success", "cleanup action succeeded")
				return h.finalizeExecution(ctx, attempt, aggregate)
			}
			if !preflight.Allowed {
				reason := strings.TrimSpace(preflight.Reason)
				if reason == "" {
					reason = "provider preflight rejected resource deletion"
				}
				return h.failAction(ctx, &attempt, aggregate, step, &action, execution.ProviderError{
					Category:  execution.ErrorProtected,
					Code:      "CleanupPreflightRejected",
					Message:   reason,
					RequestID: action.ProviderRequestID,
					Summary:   cloneRequest(preflight.Evidence),
				})
			}
			action.ProviderError = nil
			if err := h.updateAction(ctx, &action); err != nil {
				return err
			}
			if err := h.checkExecutionControl(ctx, attempt.ID, aggregate.Task.ID); err != nil {
				return err
			}
			execution.LogJob(ctx, "info", fmt.Sprintf("provider action %s invoking", step.Action))
			result, err := driver.Execute(ctx, request)
			if err != nil {
				if isVerifiedDeleteNotFound(err) || isVerifiedAbsentProviderError(err) {
					execution.LogJob(ctx, "info", "provider reports resource already absent; treating delete as succeeded")
					if completeErr := h.completeAbsentAction(ctx, attempt, aggregate, step, value, &action, err); completeErr != nil {
						return completeErr
					}
					execution.LogJob(ctx, "success", "cleanup action succeeded")
					return h.finalizeExecution(ctx, attempt, aggregate)
				}
				return h.handleProviderError(ctx, &attempt, aggregate, step, &action, err, false)
			}
			execution.LogJob(ctx, "info", fmt.Sprintf(
				"provider action accepted: request_id=%s operation_id=%s",
				result.ProviderRequestID, result.ProviderOperationID,
			))
			if h.afterProviderCall != nil {
				if err := h.afterProviderCall(action, result); err != nil {
					return err
				}
			}
			action.ProviderRequestID = result.ProviderRequestID
			action.ProviderOperationID = result.ProviderOperationID
			action.ProviderResult = cloneRequest(result.Data)
			if result.RetryAfter > 0 {
				action.PollIntervalSeconds = durationSeconds(result.RetryAfter)
			}
			action.ProviderError = nil
			if err := action.Transition(execution.ActionWaiting); err != nil {
				return err
			}
			checkStartedAt := h.planner.clock()
			action.DeletionCheckStartedAt = &checkStartedAt
			if err := h.updateAction(ctx, &action); err != nil {
				return err
			}
			if result.RetryAfter > 0 {
				execution.LogJob(ctx, "info", fmt.Sprintf("provider status query scheduled after %s", result.RetryAfter))
				return RetryAfter(fmt.Errorf("waiting before first provider status query"), result.RetryAfter)
			}
		case execution.ActionWaiting:
			execution.LogJob(ctx, "info", "waiting for provider action result")
			checkCtx, cancelCheck, expired, err := h.deletionCheckContext(ctx, &action, deletionCheckTimeout)
			if err != nil {
				return err
			}
			if expired {
				return h.failDeletionCheck(ctx, &attempt, aggregate, step, &action, readbackState(action.Readback), deletionCheckTimeout)
			}
			wait, err := driver.Wait(checkCtx, request, contracts.ActionResult{
				ProviderRequestID: action.ProviderRequestID, ProviderOperationID: action.ProviderOperationID, Data: cloneRequest(action.ProviderResult),
				RetryAfter: persistedPollInterval(action),
			})
			checkDeadlineExceeded := errors.Is(checkCtx.Err(), context.DeadlineExceeded)
			cancelCheck()
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			if err != nil {
				if checkDeadlineExceeded {
					return h.failDeletionCheck(ctx, &attempt, aggregate, step, &action, wait.State, deletionCheckTimeout)
				}
				if isVerifiedAbsentProviderError(err) {
					execution.LogJob(ctx, "info", "provider reports resource already absent; treating delete as succeeded")
					if completeErr := h.completeAbsentAction(ctx, attempt, aggregate, step, value, &action, err); completeErr != nil {
						return completeErr
					}
					execution.LogJob(ctx, "success", "cleanup action succeeded")
					return h.finalizeExecution(ctx, attempt, aggregate)
				}
				return h.handleProviderError(ctx, &attempt, aggregate, step, &action, err, true)
			}
			if wait.Data != nil {
				action.ProviderResult = cloneRequest(wait.Data)
			}
			if !wait.Done {
				if checkDeadlineExceeded || h.deletionCheckExpired(action, deletionCheckTimeout) {
					return h.failDeletionCheck(ctx, &attempt, aggregate, step, &action, wait.State, deletionCheckTimeout)
				}
				execution.LogJob(ctx, "info", fmt.Sprintf("provider action is still %s", wait.State))
				action.ProviderError = nil
				action.Readback = map[string]any{"state": wait.State}
				if err := h.updateAction(ctx, &action); err != nil {
					return err
				}
				delay := wait.RetryAfter
				if delay <= 0 {
					delay = persistedPollInterval(action)
				}
				if delay <= 0 {
					delay = h.retryDelay
				}
				return RetryAfter(fmt.Errorf("provider action is still %s", wait.State), delay)
			}
			execution.LogJob(ctx, "info", fmt.Sprintf("provider action completed: state=%s", wait.State))
			action.ProviderError = nil
			if err := action.Transition(execution.ActionReadingBack); err != nil {
				return err
			}
			if err := h.updateAction(ctx, &action); err != nil {
				return err
			}
			if delay := persistedPollInterval(action); delay > 0 {
				execution.LogJob(ctx, "info", fmt.Sprintf("provider readback scheduled after %s", delay))
				return RetryAfter(fmt.Errorf("waiting before provider readback"), delay)
			}
		case execution.ActionReadingBack:
			execution.LogJob(ctx, "info", "provider readback started")
			checkCtx, cancelCheck, expired, err := h.deletionCheckContext(ctx, &action, deletionCheckTimeout)
			if err != nil {
				return err
			}
			if expired {
				return h.failDeletionCheck(ctx, &attempt, aggregate, step, &action, readbackState(action.Readback), deletionCheckTimeout)
			}
			readback, err := driver.Readback(checkCtx, request)
			checkDeadlineExceeded := errors.Is(checkCtx.Err(), context.DeadlineExceeded)
			cancelCheck()
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			if err != nil {
				if isProviderCategory(err, execution.ErrorNotFound) ||
					isVerifiedAbsentProviderError(err) {
					readback = contracts.ReadbackResult{Exists: false, State: "absent"}
				} else if checkDeadlineExceeded {
					return h.failDeletionCheck(ctx, &attempt, aggregate, step, &action, readbackState(action.Readback), deletionCheckTimeout)
				} else {
					return h.handleProviderError(ctx, &attempt, aggregate, step, &action, err, true)
				}
			}
			action.Readback = map[string]any{"exists": readback.Exists, "state": readback.State, "data": cloneRequest(readback.Data)}
			if readback.Exists {
				if checkDeadlineExceeded || h.deletionCheckExpired(action, deletionCheckTimeout) {
					return h.failDeletionCheck(ctx, &attempt, aggregate, step, &action, readback.State, deletionCheckTimeout)
				}
				execution.LogJob(ctx, "warn", fmt.Sprintf("provider readback still observes resource: state=%s", readback.State))
				action.ProviderError = nil
				if err := h.updateAction(ctx, &action); err != nil {
					return err
				}
				delay := persistedPollInterval(action)
				if delay <= 0 {
					delay = h.retryDelay
				}
				return RetryAfter(fmt.Errorf("provider readback still observes asset %s in state %s", value.ID, readback.State), delay)
			}
			execution.LogJob(ctx, "info", fmt.Sprintf("provider readback confirmed resource absent: state=%s", readback.State))
			action.ProviderError = nil
			if err := action.Transition(execution.ActionSucceeded); err != nil {
				return err
			}
			if err := h.completeDirectReadback(ctx, attempt, aggregate, step, value, &action); err != nil {
				return err
			}
			execution.LogJob(ctx, "success", "cleanup action succeeded")
			return h.finalizeExecution(ctx, attempt, aggregate)
		case execution.ActionReconciling:
			// Compatibility for actions persisted by older versions while they
			// were waiting for an internal authoritative rescan. Cleanup no
			// longer creates scans; close the confirmed resource and leave
			// non-guaranteed delegated impacts unverified.
			if err := h.completeReconciliation(ctx, attempt, aggregate, step, value, &action); err != nil {
				return err
			}
			execution.LogJob(ctx, "success", "cleanup action succeeded")
			return h.finalizeExecution(ctx, attempt, aggregate)
		case execution.ActionSucceeded, execution.ActionSkipped, execution.ActionFailed:
			return h.finalizeExecution(ctx, attempt, aggregate)
		default:
			return fmt.Errorf("action attempt %q has unsupported status %q", action.ID, action.Status)
		}
	}
	return fmt.Errorf("action attempt %q exceeded local transition limit", action.ID)
}

func durationSeconds(value time.Duration) int {
	if value <= 0 {
		return 0
	}
	return int((value + time.Second - 1) / time.Second)
}

func preflightProviderRequestID(evidence map[string]any) string {
	for _, key := range []string{"provider_request_id", "request_id"} {
		if value := strings.TrimSpace(fmt.Sprint(evidence[key])); value != "" && value != "<nil>" {
			return value
		}
	}
	return ""
}

func persistedPollInterval(action execution.ActionAttempt) time.Duration {
	if action.PollIntervalSeconds <= 0 {
		return 0
	}
	return time.Duration(action.PollIntervalSeconds) * time.Second
}

func actionRequestsManagedVerification(action execution.ActionAttempt) bool {
	return requestBool(action.Request[plan.ManagedVerificationOnlyRequestKey])
}

func managedVerificationRetryOnce(action execution.ActionAttempt) bool {
	return requestBool(action.Request[plan.ManagedVerificationRetryOnceRequestKey])
}

func requestBool(value any) bool {
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		return strings.EqualFold(strings.TrimSpace(typed), "true")
	default:
		return false
	}
}

func requestInt(value any) int {
	switch typed := value.(type) {
	case int:
		return typed
	case int32:
		return int(typed)
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	default:
		return 0
	}
}

func controllerDeletionImpliesAssetAbsence(
	step plan.CleanupTaskStep,
	value asset.Asset,
) bool {
	if plan.ControllerDeletionImpliesAbsence(step.Evidence) {
		return true
	}
	switch value.Identity.NativeType {
	case "ACS::VPC::RouteTable", "ACS::CEN::TransitRouterRouteTable":
	default:
		return false
	}
	if strings.EqualFold(
		normalizedAssetString(value.Normalized, "_resource_marker"),
		"system_route_table",
	) {
		return true
	}
	return strings.EqualFold(
		normalizedAssetString(value.Normalized, "routeTableType", "route_table_type", "RouteTableType"),
		"System",
	)
}

func normalizedAssetString(values map[string]any, keys ...string) string {
	for _, key := range keys {
		for candidate, raw := range values {
			if !strings.EqualFold(strings.TrimSpace(candidate), key) {
				continue
			}
			if text, ok := raw.(string); ok {
				return strings.TrimSpace(text)
			}
		}
	}
	configuration, _ := values["configuration"].(map[string]any)
	if len(configuration) > 0 {
		return normalizedAssetString(configuration, keys...)
	}
	return ""
}

func (h *ExecutionHandler) handleManagedAbsenceVerification(
	ctx context.Context,
	attempt execution.ExecutionAttempt,
	aggregate persistence.CleanupTaskAggregate,
	step plan.CleanupTaskStep,
	value asset.Asset,
	driver contracts.ActionDriver,
	request contracts.ActionRequest,
	action *execution.ActionAttempt,
) error {
	timeout := managedVerificationDuration(
		step.Evidence[graph.LifecycleEvidenceWaitTimeoutSeconds],
		defaultManagedVerificationTimeout,
	)
	poll := managedVerificationDuration(
		step.Evidence[graph.LifecycleEvidenceWaitPollSeconds],
		defaultManagedVerificationPoll,
	)
	for transitions := 0; transitions < 4; transitions++ {
		if err := h.checkExecutionControl(ctx, attempt.ID, aggregate.Task.ID); err != nil {
			return err
		}
		switch action.Status {
		case execution.ActionIntentPersisted:
			if err := action.Transition(execution.ActionInvoking); err != nil {
				return err
			}
			if err := h.updateAction(ctx, action); err != nil {
				return err
			}
		case execution.ActionInvoking:
			execution.LogJob(ctx, "info", "managed resource verification started; provider delete is suppressed")
			if err := action.Transition(execution.ActionReadingBack); err != nil {
				return err
			}
			if action.DeletionCheckStartedAt == nil {
				startedAt := h.planner.clock()
				action.DeletionCheckStartedAt = &startedAt
			}
			action.PollIntervalSeconds = durationSeconds(poll)
			action.ProviderError = nil
			if err := h.updateAction(ctx, action); err != nil {
				return err
			}
		case execution.ActionReadingBack:
			execution.LogJob(ctx, "info", "checking whether the managed resource still exists")
			readback, err := driver.Readback(ctx, request)
			if err != nil {
				if isProviderCategory(err, execution.ErrorNotFound) {
					readback = contracts.ReadbackResult{Exists: false, State: "absent"}
				} else if managedVerificationRetryOnce(*action) {
					providerError, _ := normalizedProviderError(err, poll)
					return h.failManagedAbsenceVerification(
						ctx,
						attempt,
						aggregate,
						step,
						action,
						providerError,
						timeout,
					)
				} else {
					return h.handleProviderError(ctx, &attempt, aggregate, step, action, err, true)
				}
			}
			action.Readback = map[string]any{
				"exists": readback.Exists,
				"state":  readback.State,
				"data":   cloneRequest(readback.Data),
			}
			action.ProviderError = nil
			if !readback.Exists {
				execution.LogJob(ctx, "success", "managed resource readback confirmed absence")
				if err := action.Transition(execution.ActionSucceeded); err != nil {
					return err
				}
				if err := h.completeManagedAbsenceVerification(
					ctx,
					attempt,
					aggregate,
					step,
					value,
					action,
					timeout,
				); err != nil {
					return err
				}
				return h.finalizeExecution(ctx, attempt, aggregate)
			}

			if action.DeletionCheckStartedAt == nil {
				startedAt := h.planner.clock()
				action.DeletionCheckStartedAt = &startedAt
			}
			expired := !h.planner.clock().Before(
				action.DeletionCheckStartedAt.Add(timeout),
			)
			if managedVerificationRetryOnce(*action) || expired {
				providerError := managedResourceStillPresentError(
					aggregate.ImpactItems,
					step,
					timeout,
					readback.State,
				)
				return h.failManagedAbsenceVerification(
					ctx,
					attempt,
					aggregate,
					step,
					action,
					providerError,
					timeout,
				)
			}
			execution.LogJob(ctx, "warn", fmt.Sprintf(
				"managed resource still exists; checking again within %s",
				timeout,
			))
			if err := h.persistManagedVerificationObservation(
				ctx,
				aggregate,
				step,
				action,
				plan.ImpactStillPresent,
				timeout,
			); err != nil {
				return err
			}
			return RetryAfter(
				fmt.Errorf("managed resource %s is still present", value.ID),
				poll,
			)
		case execution.ActionSucceeded, execution.ActionFailed, execution.ActionSkipped:
			return h.finalizeExecution(ctx, attempt, aggregate)
		default:
			return fmt.Errorf(
				"managed verification action %q has unsupported status %q",
				action.ID,
				action.Status,
			)
		}
	}
	return fmt.Errorf("managed verification action %q exceeded local transition limit", action.ID)
}

func managedVerificationDuration(value any, fallback time.Duration) time.Duration {
	var seconds float64
	switch typed := value.(type) {
	case int:
		seconds = float64(typed)
	case int64:
		seconds = float64(typed)
	case float64:
		seconds = typed
	case float32:
		seconds = float64(typed)
	}
	if seconds <= 0 {
		return fallback
	}
	return time.Duration(seconds * float64(time.Second))
}

func managedResourceStillPresentError(
	impacts []plan.ImpactItem,
	step plan.CleanupTaskStep,
	timeout time.Duration,
	state string,
) execution.ProviderError {
	summary := map[string]any{
		"asset_id":        step.AssetID,
		"timeout_seconds": int64(timeout / time.Second),
	}
	for _, impact := range impacts {
		if impact.AssetID != step.AssetID {
			continue
		}
		summary["controller_id"] = impact.ControllerID
		break
	}
	if state = strings.TrimSpace(state); state != "" {
		summary["last_state"] = state
	}
	return execution.ProviderError{
		Category: execution.ErrorConflict,
		Code:     "ManagedResourceStillPresent",
		Message: fmt.Sprintf(
			"managed resource remains after its controller was deleted; automatic deletion was not confirmed within %s",
			timeout,
		),
		Summary: summary,
	}
}

func managedVerificationImpactResults(
	values []plan.ImpactItem,
	step plan.CleanupTaskStep,
	result plan.ImpactResult,
	action execution.ActionAttempt,
	timeout time.Duration,
) []plan.ImpactItem {
	updates := make([]plan.ImpactItem, 0, 1)
	for _, impact := range values {
		if impact.AssetID != step.AssetID ||
			impact.Expected != plan.ExpectedDelegatedDelete {
			continue
		}
		impact.Result = result
		impact.Evidence = cloneRequest(impact.Evidence)
		impact.Evidence["verification_timeout_seconds"] = int64(timeout / time.Second)
		impact.Evidence["verification_checked_at"] = action.UpdatedAt
		impact.Evidence["verification_read_only"] = true
		if action.DeletionCheckStartedAt != nil {
			impact.Evidence["verification_started_at"] = *action.DeletionCheckStartedAt
			impact.Evidence["verification_deadline"] = action.DeletionCheckStartedAt.Add(timeout)
		}
		if state := readbackState(action.Readback); state != "" {
			impact.Evidence["verification_state"] = state
		}
		updates = append(updates, impact)
	}
	return updates
}

func (h *ExecutionHandler) persistManagedVerificationObservation(
	ctx context.Context,
	aggregate persistence.CleanupTaskAggregate,
	step plan.CleanupTaskStep,
	action *execution.ActionAttempt,
	result plan.ImpactResult,
	timeout time.Duration,
) error {
	action.UpdatedAt = h.planner.clock()
	impacts := managedVerificationImpactResults(
		aggregate.ImpactItems,
		step,
		result,
		*action,
		timeout,
	)
	return h.planner.repositories.WithTx(ctx, func(repositories persistence.Repositories) error {
		if err := repositories.Executions().UpdateAction(ctx, *action); err != nil {
			return err
		}
		if len(impacts) > 0 {
			return repositories.CleanupTasks().UpdateImpactItems(
				ctx,
				aggregate.Task.ID,
				impacts,
			)
		}
		return nil
	})
}

func (h *ExecutionHandler) failManagedAbsenceVerification(
	ctx context.Context,
	attempt execution.ExecutionAttempt,
	aggregate persistence.CleanupTaskAggregate,
	step plan.CleanupTaskStep,
	action *execution.ActionAttempt,
	providerError execution.ProviderError,
	timeout time.Duration,
) error {
	if action.Status != execution.ActionFailed {
		action.FailedFrom = action.Status
		if err := action.Transition(execution.ActionFailed); err != nil {
			return err
		}
	}
	action.ProviderError = &providerError
	action.UpdatedAt = h.planner.clock()
	action.FinishedAt = &action.UpdatedAt
	impacts := managedVerificationImpactResults(
		aggregate.ImpactItems,
		step,
		plan.ImpactStillPresent,
		*action,
		timeout,
	)
	if err := h.planner.repositories.WithTx(ctx, func(repositories persistence.Repositories) error {
		if err := repositories.Executions().UpdateAction(ctx, *action); err != nil {
			return err
		}
		if len(impacts) > 0 {
			if err := repositories.CleanupTasks().UpdateImpactItems(
				ctx,
				aggregate.Task.ID,
				impacts,
			); err != nil {
				return err
			}
		}
		return appendActionOutcome(ctx, repositories, attempt, *action, "failed", action.UpdatedAt)
	}); err != nil {
		return err
	}
	return h.finalizeExecution(ctx, attempt, aggregate)
}

func (h *ExecutionHandler) completeManagedAbsenceVerification(
	ctx context.Context,
	attempt execution.ExecutionAttempt,
	aggregate persistence.CleanupTaskAggregate,
	step plan.CleanupTaskStep,
	value asset.Asset,
	action *execution.ActionAttempt,
	timeout time.Duration,
) error {
	action.UpdatedAt = h.planner.clock()
	action.FinishedAt = &action.UpdatedAt
	closeAssetProjection(&value, action.UpdatedAt)
	impacts := managedVerificationImpactResults(
		aggregate.ImpactItems,
		step,
		plan.ImpactDeletedByController,
		*action,
		timeout,
	)
	return h.planner.repositories.WithTx(ctx, func(repositories persistence.Repositories) error {
		if err := repositories.Inventory().PutAsset(ctx, value); err != nil {
			return err
		}
		if err := repositories.Graph().CloseAssetTopology(ctx, value.ID, action.UpdatedAt); err != nil {
			return err
		}
		if err := closeResidualFinding(ctx, repositories.Findings(), value.ID, action.UpdatedAt); err != nil {
			return err
		}
		if len(impacts) > 0 {
			if err := repositories.CleanupTasks().UpdateImpactItems(
				ctx,
				aggregate.Task.ID,
				impacts,
			); err != nil {
				return err
			}
		}
		if err := repositories.Executions().UpdateAction(ctx, *action); err != nil {
			return err
		}
		return appendActionOutcome(ctx, repositories, attempt, *action, "succeeded", action.UpdatedAt)
	})
}

func (h *ExecutionHandler) completeControllerIntegratedVerification(
	ctx context.Context,
	attempt execution.ExecutionAttempt,
	aggregate persistence.CleanupTaskAggregate,
	step plan.CleanupTaskStep,
	value asset.Asset,
	action *execution.ActionAttempt,
) error {
	switch action.Status {
	case execution.ActionIntentPersisted:
		if err := action.Transition(execution.ActionInvoking); err != nil {
			return err
		}
		if err := action.Transition(execution.ActionReadingBack); err != nil {
			return err
		}
	case execution.ActionInvoking, execution.ActionWaiting:
		if err := action.Transition(execution.ActionReadingBack); err != nil {
			return err
		}
	case execution.ActionReadingBack:
	case execution.ActionReconciling:
	default:
		return fmt.Errorf(
			"controller-integrated action %q has unsupported status %q",
			action.ID,
			action.Status,
		)
	}
	if err := action.Transition(execution.ActionSucceeded); err != nil {
		return err
	}
	action.Readback = map[string]any{
		"exists":                   false,
		"state":                    "deleted_with_controller",
		"inferred_from_controller": true,
	}
	action.ProviderError = nil
	action.UpdatedAt = h.planner.clock()
	action.FinishedAt = &action.UpdatedAt
	closeAssetProjection(&value, action.UpdatedAt)
	impacts := controllerIntegratedImpactResults(
		aggregate.ImpactItems,
		step.AssetID,
		action.UpdatedAt,
	)
	return h.planner.repositories.WithTx(ctx, func(repositories persistence.Repositories) error {
		if err := repositories.Inventory().PutAsset(ctx, value); err != nil {
			return err
		}
		if err := repositories.Graph().CloseAssetTopology(ctx, value.ID, action.UpdatedAt); err != nil {
			return err
		}
		if err := closeResidualFinding(ctx, repositories.Findings(), value.ID, action.UpdatedAt); err != nil {
			return err
		}
		if len(impacts) > 0 {
			if err := repositories.CleanupTasks().UpdateImpactItems(
				ctx,
				aggregate.Task.ID,
				impacts,
			); err != nil {
				return err
			}
		}
		if err := repositories.Executions().UpdateAction(ctx, *action); err != nil {
			return err
		}
		return appendActionOutcome(ctx, repositories, attempt, *action, "succeeded", action.UpdatedAt)
	})
}

func controllerIntegratedImpactResults(
	values []plan.ImpactItem,
	assetID asset.AssetID,
	observedAt time.Time,
) []plan.ImpactItem {
	result := make([]plan.ImpactItem, 0, 1)
	for _, impact := range values {
		if impact.AssetID != assetID ||
			impact.Expected != plan.ExpectedDelegatedDelete ||
			!plan.ControllerDeletionImpliesAbsence(impact.Evidence) {
			continue
		}
		impact.Result = plan.ImpactDeletedByController
		impact.Evidence = cloneRequest(impact.Evidence)
		impact.Evidence["controller_integrated_at"] = observedAt
		result = append(result, impact)
	}
	return result
}

func (h *ExecutionHandler) deletionCheckContext(
	ctx context.Context,
	action *execution.ActionAttempt,
	timeout time.Duration,
) (context.Context, context.CancelFunc, bool, error) {
	now := h.planner.clock()
	if action.DeletionCheckStartedAt == nil {
		startedAt := action.UpdatedAt
		if startedAt.IsZero() || startedAt.After(now) {
			startedAt = now
		}
		action.DeletionCheckStartedAt = &startedAt
		if err := h.updateAction(ctx, action); err != nil {
			return nil, nil, false, err
		}
	}
	remaining := action.DeletionCheckStartedAt.Add(timeout).Sub(now)
	if remaining <= 0 {
		return nil, nil, true, nil
	}
	checkCtx, cancel := context.WithTimeout(ctx, remaining)
	return checkCtx, cancel, false, nil
}

func (h *ExecutionHandler) deletionCheckExpired(action execution.ActionAttempt, timeout time.Duration) bool {
	return action.DeletionCheckStartedAt != nil &&
		!h.planner.clock().Before(action.DeletionCheckStartedAt.Add(timeout))
}

func (h *ExecutionHandler) failDeletionCheck(
	ctx context.Context,
	attempt *execution.ExecutionAttempt,
	aggregate persistence.CleanupTaskAggregate,
	step plan.CleanupTaskStep,
	action *execution.ActionAttempt,
	lastState string,
	timeout time.Duration,
) error {
	message := fmt.Sprintf("resource deletion was not confirmed within %s", timeout)
	summary := map[string]any{
		"asset_id":        string(action.AssetID),
		"timeout_seconds": int64(timeout / time.Second),
	}
	if lastState = strings.TrimSpace(lastState); lastState != "" {
		summary["last_state"] = lastState
		message += fmt.Sprintf("; last observed state was %s", lastState)
	}
	return h.failAction(ctx, attempt, aggregate, step, action, execution.ProviderError{
		Category: execution.ErrorProviderFailure,
		Code:     "DeletionCheckTimeout",
		Message:  message,
		Summary:  summary,
	})
}

func readbackState(readback map[string]any) string {
	state, _ := readback["state"].(string)
	return strings.TrimSpace(state)
}

func (h *ExecutionHandler) startExecution(ctx context.Context, executionID execution.ExecutionID) (execution.ExecutionAttempt, error) {
	h.executionStateMu.Lock()
	defer h.executionStateMu.Unlock()

	attempt, err := h.planner.repositories.Executions().GetExecution(ctx, executionID)
	if err != nil || attempt.Status != execution.ExecutionPending {
		return attempt, err
	}
	// Snapshot freshness is an execution-level pre-destructive guard. Revalidate
	// once when the first job starts, then freeze the task for every remaining
	// step because inventory changes can be effects of this execution itself.
	if _, err := h.planner.ValidateTask(ctx, plan.CleanupTaskID(attempt.CleanupTaskID)); err != nil {
		if errors.Is(err, ErrInventoryReconciliationPending) {
			return attempt, RetryAfter(err, h.retryDelay)
		}
		_ = h.failExecution(ctx, &attempt, err.Error())
		return attempt, err
	}
	now := h.planner.clock()
	attempt.Status = execution.ExecutionRunning
	attempt.StartedAt = &now
	if err := h.planner.repositories.Executions().UpdateExecution(ctx, attempt); err != nil {
		return execution.ExecutionAttempt{}, err
	}
	return attempt, nil
}

func (h *ExecutionHandler) completeAbsentAction(
	ctx context.Context,
	attempt execution.ExecutionAttempt,
	aggregate persistence.CleanupTaskAggregate,
	step plan.CleanupTaskStep,
	value asset.Asset,
	action *execution.ActionAttempt,
	providerErr error,
) error {
	var providerCall *contracts.ProviderCallError
	if errors.As(providerErr, &providerCall) {
		action.ProviderRequestID = providerCall.Provider.RequestID
	}
	action.ProviderError = nil
	action.Readback = map[string]any{"exists": false, "state": "absent"}
	if action.Status == execution.ActionInvoking || action.Status == execution.ActionWaiting {
		if err := action.Transition(execution.ActionReadingBack); err != nil {
			return err
		}
	}
	if err := action.Transition(execution.ActionSucceeded); err != nil {
		return err
	}
	return h.completeDirectReadback(ctx, attempt, aggregate, step, value, action)
}

var verifiedDeleteNotFoundCodes = map[string]map[string]struct{}{
	// Alibaba Cloud documents ProjectNotExist for DeleteProject, and the code
	// was confirmed against a random fake project on 2026-08-05.
	// https://help.aliyun.com/en/sls/api-deleteproject
	"AlibabaCloud.SLS.DeleteProject": {
		"ProjectNotExist": {},
	},
	// Alibaba Cloud FC 2.0 returns ServiceNotFound when DeleteService is
	// repeated after the service has already been removed. Confirmed against
	// svc-42d270g0 on 2026-08-06.
	// https://help.aliyun.com/zh/functioncompute/fc-2-0/developer-reference/api-fc-open-2021-04-06-deleteservice
	"AlibabaCloud.FC.DeleteService": {
		"ServiceNotFound": {},
	},
	// ReleaseEipAddress returns InvalidAllocationId.NotFound when the EIP has
	// already been removed, including EIPs managed and deleted with an NLB.
	"AlibabaCloud.ReleaseEipAddress": {
		"InvalidAllocationId.NotFound": {},
	},
	// DeleteSecurityGroup returns InvalidSecurityGroup.NotFound when the
	// security group has already been removed.
	"AlibabaCloud.DeleteSecurityGroup": {
		"InvalidSecurityGroup.NotFound": {},
	},
	// MaxCompute returns ODPS-0420061 when a project targeted by cleanup is
	// already absent or otherwise no longer addressable by the delete API.
	"AlibabaCloud.MaxCompute.DeleteProject": {
		"ODPS-0420061": {},
	},
	// ROS returns StackGroupNotFound when another actor has already removed
	// the group between inventory and execution.
	"AlibabaCloud.ROS.DeleteStackGroup": {
		"StackGroupNotFound": {},
	},
	// CEN peer attachments can be represented from both endpoint regions.
	// The second endpoint delete observes the attachment as already absent.
	"AlibabaCloud.CEN.DeleteTransitRouterPeerAttachment": {
		"InvalidTransitRouterAttachmentId.NotFound": {},
	},
}

func isVerifiedDeleteNotFound(err error) bool {
	var providerCall *contracts.ProviderCallError
	if !errors.As(err, &providerCall) ||
		providerCall.Provider.Category != execution.ErrorNotFound {
		return false
	}
	operation := strings.TrimSpace(fmt.Sprint(providerCall.Provider.Summary["operation"]))
	code := strings.TrimSpace(providerCall.Provider.Code)
	if operation == "AlibabaCloud.DataWorks.DeleteResourceGroup" && code == "704203" {
		message := strings.ToLower(providerCall.Provider.Message)
		return strings.Contains(message, "resource group status is deleted")
	}
	codes := verifiedDeleteNotFoundCodes[operation]
	_, verified := codes[code]
	return verified
}

func isVerifiedAbsentProviderError(err error) bool {
	var providerCall *contracts.ProviderCallError
	if !errors.As(err, &providerCall) {
		return false
	}
	operation := strings.TrimSpace(fmt.Sprint(providerCall.Provider.Summary["operation"]))
	code := strings.TrimSpace(providerCall.Provider.Code)
	// DeleteVSwitch returns IncorrectVSwitchId when the target vSwitch is no
	// longer addressable. For this exact delete operation, the requested absent
	// state has already been reached, so treat the response as idempotent success.
	if operation == "AlibabaCloud.DeleteVSwitch" && code == "IncorrectVSwitchId" {
		return true
	}
	// SLS keeps deleted projects in a recycle bin. GetProject reports them as
	// inaccessible instead of not found, but the destructive state requested
	// by cleanup has already been reached.
	return operation == "AlibabaCloud.SLS.GetProject" && code == "ProjectInRecycleBin"
}

type retainedDependencyError struct {
	stepID     plan.StepID
	skipReason string
}

func (e *retainedDependencyError) Error() string {
	return fmt.Sprintf("cleanup dependency %s was retained: %s", e.stepID, e.skipReason)
}

type failedDependencyError struct {
	stepID plan.StepID
}

func (e *failedDependencyError) Error() string {
	return fmt.Sprintf("cleanup dependency %s failed", e.stepID)
}

type dependencyTerminalState struct {
	stepID     plan.StepID
	status     execution.ActionStatus
	skipReason string
}

func (h *ExecutionHandler) ensureDependencies(
	ctx context.Context,
	executionID execution.ExecutionID,
	step plan.CleanupTaskStep,
	steps []plan.CleanupTaskStep,
) error {
	stepByID := make(map[plan.StepID]plan.CleanupTaskStep, len(steps))
	for _, candidate := range steps {
		stepByID[candidate.ID] = candidate
	}
	terminalMemo := make(map[plan.StepID]dependencyTerminalState, len(steps))
	memoized := make(map[plan.StepID]bool, len(steps))
	visiting := make(map[plan.StepID]bool, len(steps))
	var terminalAncestor func(plan.StepID) (dependencyTerminalState, error)
	terminalAncestor = func(stepID plan.StepID) (dependencyTerminalState, error) {
		if memoized[stepID] {
			return terminalMemo[stepID], nil
		}
		if visiting[stepID] {
			return dependencyTerminalState{}, nil
		}
		visiting[stepID] = true
		defer delete(visiting, stepID)

		action, err := h.planner.repositories.Executions().GetActionByExecutionStep(
			ctx,
			executionID,
			string(stepID),
		)
		if err == nil {
			terminal := dependencyTerminalState{}
			switch action.Status {
			case execution.ActionFailed:
				terminal = dependencyTerminalState{stepID: stepID, status: action.Status}
			case execution.ActionSkipped:
				terminal = dependencyTerminalState{
					stepID: stepID, status: action.Status, skipReason: action.SkipReason,
				}
			}
			if terminal.stepID != "" {
				terminalMemo[stepID] = terminal
				memoized[stepID] = true
				return terminal, nil
			}
			// A non-terminal dependency can itself be waiting behind a failed
			// ancestor. Walk its prerequisites so deeper dependency chains settle
			// without waiting for an action that can no longer run.
		}
		if err != nil && !errors.Is(err, persistence.ErrNotFound) {
			return dependencyTerminalState{}, err
		}
		candidate, exists := stepByID[stepID]
		if !exists {
			memoized[stepID] = true
			return dependencyTerminalState{}, nil
		}
		for _, dependencyID := range candidate.DependsOn {
			terminal, err := terminalAncestor(dependencyID)
			if err != nil {
				return dependencyTerminalState{}, err
			}
			if terminal.stepID != "" {
				terminalMemo[stepID] = terminal
				memoized[stepID] = true
				return terminal, nil
			}
		}
		memoized[stepID] = true
		return dependencyTerminalState{}, nil
	}

	for _, dependency := range step.DependsOn {
		action, err := h.planner.repositories.Executions().GetActionByExecutionStep(ctx, executionID, string(dependency))
		if err == nil && action.Status == execution.ActionFailed {
			return &failedDependencyError{stepID: dependency}
		}
		if err == nil && action.Status == execution.ActionSkipped {
			return &retainedDependencyError{stepID: dependency, skipReason: action.SkipReason}
		}
		if err == nil || errors.Is(err, persistence.ErrNotFound) {
			terminal, terminalErr := terminalAncestor(dependency)
			if terminalErr != nil {
				return terminalErr
			}
			switch terminal.status {
			case execution.ActionFailed:
				return &failedDependencyError{stepID: terminal.stepID}
			case execution.ActionSkipped:
				return &retainedDependencyError{
					stepID: terminal.stepID, skipReason: terminal.skipReason,
				}
			}
			if errors.Is(err, persistence.ErrNotFound) {
				return RetryAfter(fmt.Errorf("cleanup dependency %s has not started", dependency), h.retryDelay)
			}
		}
		if err != nil {
			return err
		}
		if action.Status != execution.ActionSucceeded {
			return RetryAfter(fmt.Errorf("cleanup dependency %s is %s", dependency, action.Status), h.retryDelay)
		}
	}
	return nil
}

func (h *ExecutionHandler) ensureActionIntent(ctx context.Context, attempt execution.ExecutionAttempt, cleanupTask plan.CleanupTask, step plan.CleanupTaskStep) (execution.ActionAttempt, bool, error) {
	action, err := h.planner.repositories.Executions().GetActionByExecutionStep(ctx, attempt.ID, string(step.ID))
	if err == nil && action.Status != execution.ActionPending {
		return action, false, nil
	}
	if err != nil && !errors.Is(err, persistence.ErrNotFound) {
		return execution.ActionAttempt{}, false, err
	}
	now := h.planner.clock()
	candidate := execution.ActionAttempt{
		ID:          execution.ActionAttemptID(idgen.MustNew("act")),
		ExecutionID: attempt.ID, CleanupTaskStepID: string(step.ID), AssetID: step.AssetID, Action: step.Action,
		Status: execution.ActionIntentPersisted, IdempotencyKey: "action-" + deterministicID(attempt.IdempotencyKey, string(step.ID)),
		SpecBundleRevision: cleanupTask.Revision.SpecBundleRevision, SpecHash: cleanupTask.Revision.SpecHash,
		Request: cloneRequest(step.RequestOptions), CreatedAt: now, UpdatedAt: now,
	}
	created := false
	atCapacity := false
	err = h.planner.repositories.WithTx(ctx, func(repositories persistence.Repositories) error {
		executions := repositories.Executions()
		if err := executions.LockExecution(ctx, attempt.ID); err != nil {
			return err
		}
		current, lookupErr := executions.GetActionByExecutionStep(ctx, attempt.ID, string(step.ID))
		if lookupErr == nil && current.Status != execution.ActionPending {
			action = current
			return nil
		}
		if lookupErr != nil && !errors.Is(lookupErr, persistence.ErrNotFound) {
			return lookupErr
		}
		inFlight, countErr := executions.CountInFlightActions(ctx, attempt.ID)
		if countErr != nil {
			return countErr
		}
		if inFlight >= effectiveExecutionConcurrency(attempt) {
			atCapacity = true
			return nil
		}
		if lookupErr == nil {
			resumeStatus := current.ResumeStatus
			switch resumeStatus {
			case execution.ActionInvoking, execution.ActionWaiting, execution.ActionReadingBack, execution.ActionReconciling:
			default:
				resumeStatus = execution.ActionInvoking
			}
			current.Status = resumeStatus
			current.ResumeStatus = ""
			current.UpdatedAt = now
			if err := executions.UpdateAction(ctx, current); err != nil {
				return err
			}
			action = current
			return nil
		}
		if err := executions.AppendAction(ctx, candidate); err != nil {
			return err
		}
		if err := executions.AppendOutbox(ctx, execution.OutboxEvent{
			ID: execution.OutboxEventID(idgen.MustNew("evt")), Topic: "action.intent",
			AggregateID: string(candidate.ID), Payload: map[string]any{"execution_id": attempt.ID, "cleanup_task_step_id": step.ID, "asset_id": step.AssetID}, CreatedAt: now,
		}); err != nil {
			return err
		}
		action = candidate
		created = true
		return nil
	})
	if err != nil {
		return execution.ActionAttempt{}, false, err
	}
	if atCapacity {
		execution.LogJob(ctx, "info", fmt.Sprintf(
			"waiting for cleanup concurrency slot (limit %d)",
			effectiveExecutionConcurrency(attempt),
		))
		return execution.ActionAttempt{}, false, RetryAfter(
			fmt.Errorf("cleanup concurrency limit %d reached", effectiveExecutionConcurrency(attempt)),
			h.retryDelay,
		)
	}
	return action, created, nil
}

func (h *ExecutionHandler) updateAction(ctx context.Context, action *execution.ActionAttempt) error {
	action.UpdatedAt = h.planner.clock()
	if action.Status == execution.ActionSucceeded || action.Status == execution.ActionSkipped || action.Status == execution.ActionFailed {
		finishedAt := action.UpdatedAt
		action.FinishedAt = &finishedAt
	}
	return h.planner.repositories.Executions().UpdateAction(ctx, *action)
}

func (h *ExecutionHandler) skipDirtyAction(
	ctx context.Context,
	attempt execution.ExecutionAttempt,
	aggregate persistence.CleanupTaskAggregate,
	step plan.CleanupTaskStep,
	action *execution.ActionAttempt,
) error {
	if action.Status != execution.ActionSkipped {
		if err := action.Transition(execution.ActionSkipped); err != nil {
			return err
		}
	}
	action.SkipReason = "dirty_asset"
	action.UpdatedAt = h.planner.clock()
	action.FinishedAt = &action.UpdatedAt
	impacts := ignoredDirtyImpactResults(aggregate.ImpactItems, step.ID)
	return h.planner.repositories.WithTx(ctx, func(repositories persistence.Repositories) error {
		if err := repositories.Executions().UpdateAction(ctx, *action); err != nil {
			return err
		}
		if err := repositories.CleanupTasks().UpdateImpactItems(ctx, aggregate.Task.ID, impacts); err != nil {
			return err
		}
		return appendActionOutcome(ctx, repositories, attempt, *action, "skipped", action.UpdatedAt)
	})
}

func (h *ExecutionHandler) handleProviderError(
	ctx context.Context,
	attempt *execution.ExecutionAttempt,
	aggregate persistence.CleanupTaskAggregate,
	step plan.CleanupTaskStep,
	action *execution.ActionAttempt,
	err error,
	allowRetry bool,
) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	providerError, retryAfter := normalizedProviderError(err, h.retryDelay)
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		providerError = execution.ProviderError{
			Category: execution.ErrorRetryable,
			Code:     "ProviderCheckTimeout",
			Message:  "provider deletion check timed out",
		}
		retryAfter = h.retryDelay
	}
	action.ProviderError = &providerError
	if !allowRetry &&
		action.Status == execution.ActionInvoking &&
		verifiedSafeProviderInvokeRetry(providerError) &&
		requestInt(action.Request[providerInvokeRetryCountRequestKey]) < maxVerifiedProviderInvokeRetries {
		if action.Request == nil {
			action.Request = make(map[string]any)
		}
		action.Request[providerInvokeRetryCountRequestKey] =
			requestInt(action.Request[providerInvokeRetryCountRequestKey]) + 1
		execution.LogJob(ctx, "warn", "transient provider delete error; scheduling one bounded idempotent retry")
		if updateErr := h.updateAction(ctx, action); updateErr != nil {
			return updateErr
		}
		return RetryAfter(err, retryAfter)
	}
	if !allowRetry &&
		action.Status == execution.ActionInvoking &&
		managedResourceProviderRejection(*action) {
		execution.LogJob(ctx, "warn", "provider identifies a managed resource; switching to read-only verification")
		if action.Request == nil {
			action.Request = make(map[string]any)
		}
		action.Request[plan.ManagedVerificationOnlyRequestKey] = true
		if action.DeletionCheckStartedAt == nil {
			startedAt := h.planner.clock()
			action.DeletionCheckStartedAt = &startedAt
		}
		action.ProviderError = nil
		if transitionErr := action.Transition(execution.ActionReadingBack); transitionErr != nil {
			return transitionErr
		}
		if updateErr := h.updateAction(ctx, action); updateErr != nil {
			return updateErr
		}
		return RetryAfter(err, h.retryDelay)
	}
	if skipReason, ok := cleanupProviderSkipReason(providerError); ok {
		execution.LogJob(ctx, "warn", fmt.Sprintf(
			"cleanup action skipped: reason=%s message=%s",
			skipReason,
			providerError.Message,
		))
		if skipErr := h.skipUnsupportedAction(
			ctx,
			*attempt,
			aggregate,
			step,
			action,
			providerError,
			skipReason,
		); skipErr != nil {
			return skipErr
		}
		return h.finalizeExecution(ctx, *attempt, aggregate)
	}
	execution.LogJob(ctx, "error", fmt.Sprintf("provider action error: category=%s message=%s", providerError.Category, providerError.Message))
	if providerError.Category == execution.ErrorNotFound && allowRetry && action.Status == execution.ActionWaiting {
		if transitionErr := action.Transition(execution.ActionReadingBack); transitionErr != nil {
			return transitionErr
		}
		if updateErr := h.updateAction(ctx, action); updateErr != nil {
			return updateErr
		}
		return RetryAfter(err, h.retryDelay)
	}
	if allowRetry && (providerError.Category == execution.ErrorThrottled || providerError.Category == execution.ErrorRetryable) {
		if updateErr := h.updateAction(ctx, action); updateErr != nil {
			return updateErr
		}
		return RetryAfter(err, retryAfter)
	}
	return h.failAction(ctx, attempt, aggregate, step, action, providerError)
}

func verifiedSafeProviderInvokeRetry(providerError execution.ProviderError) bool {
	operation := strings.TrimSpace(fmt.Sprint(providerError.Summary["operation"]))
	code := strings.TrimSpace(providerError.Code)
	switch operation {
	case "AlibabaCloud.CEN.DeleteTransitRouterPeerAttachment":
		return code == "Operation.Blocking"
	case "AlibabaCloud.DeleteSnapshot":
		return code == "SnapshotCreatedImage"
	case "AlibabaCloud.FC.DeleteService":
		return code == "InternalServerError" || providerError.Category == execution.ErrorRetryable
	case "AlibabaCloud.ECI.DeleteContainerGroup":
		return providerError.Category == execution.ErrorRetryable
	case "AlibabaCloud.DeleteVSwitch":
		return code == "TaskConflict"
	case "AlibabaCloud.ESS.DeleteScalingGroup":
		return code == "InstanceInUse"
	case "AlibabaCloud.DeleteNetworkInterface":
		return code == "InvalidOperation.InvalidEniState"
	default:
		return false
	}
}

func (h *ExecutionHandler) skipUnsupportedAction(
	ctx context.Context,
	attempt execution.ExecutionAttempt,
	aggregate persistence.CleanupTaskAggregate,
	step plan.CleanupTaskStep,
	action *execution.ActionAttempt,
	providerError execution.ProviderError,
	skipReason string,
) error {
	if action.Status != execution.ActionSkipped {
		if err := action.Transition(execution.ActionSkipped); err != nil {
			return err
		}
	}
	action.SkipReason = skipReason
	action.ProviderError = &providerError
	if action.ProviderRequestID == "" {
		action.ProviderRequestID = providerError.RequestID
	}
	action.UpdatedAt = h.planner.clock()
	action.FinishedAt = &action.UpdatedAt
	impacts := unsupportedImpactResults(aggregate.ImpactItems, step.ID)
	return h.planner.repositories.WithTx(ctx, func(repositories persistence.Repositories) error {
		if err := repositories.Executions().UpdateAction(ctx, *action); err != nil {
			return err
		}
		if err := repositories.CleanupTasks().UpdateImpactItems(ctx, aggregate.Task.ID, impacts); err != nil {
			return err
		}
		return appendActionOutcome(ctx, repositories, attempt, *action, "skipped", action.UpdatedAt)
	})
}

func (h *ExecutionHandler) failAction(ctx context.Context, attempt *execution.ExecutionAttempt, aggregate persistence.CleanupTaskAggregate, step plan.CleanupTaskStep, action *execution.ActionAttempt, providerError execution.ProviderError) error {
	execution.LogJob(ctx, "error", fmt.Sprintf("cleanup action failed: %s", providerError.Message))
	if action.Status != execution.ActionFailed {
		action.FailedFrom = action.Status
		if err := action.Transition(execution.ActionFailed); err != nil {
			return err
		}
	}
	action.ProviderError = &providerError
	impacts := failedImpactResults(aggregate.ImpactItems, step.ID)
	err := h.planner.repositories.WithTx(ctx, func(repositories persistence.Repositories) error {
		action.UpdatedAt = h.planner.clock()
		action.FinishedAt = &action.UpdatedAt
		if err := repositories.Executions().UpdateAction(ctx, *action); err != nil {
			return err
		}
		if err := repositories.CleanupTasks().UpdateImpactItems(ctx, aggregate.Task.ID, impacts); err != nil {
			return err
		}
		return appendActionOutcome(ctx, repositories, *attempt, *action, "failed", action.UpdatedAt)
	})
	if err != nil {
		return err
	}
	return h.finalizeExecution(ctx, *attempt, aggregate)
}

func (h *ExecutionHandler) completeDirectReadback(
	ctx context.Context,
	attempt execution.ExecutionAttempt,
	aggregate persistence.CleanupTaskAggregate,
	step plan.CleanupTaskStep,
	value asset.Asset,
	action *execution.ActionAttempt,
) error {
	impacts := initialImpactResults(aggregate.ImpactItems, step.ID)
	return h.planner.repositories.WithTx(ctx, func(repositories persistence.Repositories) error {
		action.UpdatedAt = h.planner.clock()
		action.FinishedAt = &action.UpdatedAt
		if readbackState(action.Readback) != "scheduled_deletion" {
			closeAssetProjection(&value, action.UpdatedAt)
			if err := repositories.Inventory().PutAsset(ctx, value); err != nil {
				return err
			}
			if err := repositories.Graph().CloseAssetTopology(ctx, value.ID, action.UpdatedAt); err != nil {
				return err
			}
		}
		if hasImpacts(aggregate.ImpactItems, step.ID) {
			if err := closeControllerIntegratedImpactAssets(
				ctx,
				repositories,
				aggregate.ImpactItems,
				step.ID,
				action.UpdatedAt,
			); err != nil {
				return err
			}
			if err := repositories.CleanupTasks().UpdateImpactItems(ctx, aggregate.Task.ID, impacts); err != nil {
				return err
			}
		}
		if err := repositories.Executions().UpdateAction(ctx, *action); err != nil {
			return err
		}
		return appendActionOutcome(ctx, repositories, attempt, *action, "succeeded", action.UpdatedAt)
	})
}

func (h *ExecutionHandler) completeReconciliation(
	ctx context.Context,
	attempt execution.ExecutionAttempt,
	aggregate persistence.CleanupTaskAggregate,
	step plan.CleanupTaskStep,
	value asset.Asset,
	action *execution.ActionAttempt,
) error {
	return h.planner.repositories.WithTx(ctx, func(repositories persistence.Repositories) error {
		action.UpdatedAt = h.planner.clock()
		closeAssetProjection(&value, action.UpdatedAt)
		if err := repositories.Inventory().PutAsset(ctx, value); err != nil {
			return err
		}
		if err := repositories.Graph().CloseAssetTopology(ctx, value.ID, action.UpdatedAt); err != nil {
			return err
		}
		impacts := initialImpactResults(aggregate.ImpactItems, step.ID)
		if err := closeControllerIntegratedImpactAssets(
			ctx,
			repositories,
			aggregate.ImpactItems,
			step.ID,
			action.UpdatedAt,
		); err != nil {
			return err
		}
		if err := repositories.CleanupTasks().UpdateImpactItems(ctx, aggregate.Task.ID, impacts); err != nil {
			return err
		}
		if err := action.Transition(execution.ActionSucceeded); err != nil {
			return err
		}
		action.FinishedAt = &action.UpdatedAt
		if err := repositories.Executions().UpdateAction(ctx, *action); err != nil {
			return err
		}
		return appendActionOutcome(ctx, repositories, attempt, *action, "succeeded", action.UpdatedAt)
	})
}

func (h *ExecutionHandler) finalizeExecution(ctx context.Context, attempt execution.ExecutionAttempt, aggregate persistence.CleanupTaskAggregate) error {
	h.executionStateMu.Lock()
	defer h.executionStateMu.Unlock()

	current, err := h.planner.repositories.Executions().GetExecution(ctx, attempt.ID)
	if err != nil {
		return err
	}
	if current.Status == execution.ExecutionSucceeded || current.Status == execution.ExecutionCanceled {
		return nil
	}
	attempt = current
	if currentAggregate, err := h.planner.repositories.CleanupTasks().GetTask(ctx, aggregate.Task.ID); err != nil {
		return err
	} else {
		aggregate = currentAggregate
	}
	relationships, err := h.planner.repositories.Graph().ListRelationshipsByAssetIDs(
		ctx,
		aggregate.Task.ResolvedAssetIDs,
	)
	if err != nil {
		return err
	}
	aggregate.Steps, _ = appendCreatedFromTaskDependencies(
		aggregate.Steps,
		relationships,
	)
	actions, err := h.planner.repositories.Executions().ListActions(ctx, attempt.ID)
	if err != nil {
		return err
	}
	byStep := make(map[string]execution.ActionAttempt, len(actions))
	for _, action := range actions {
		byStep[action.CleanupTaskStepID] = action
	}
	settled, failed := cleanupStepsSettled(aggregate.Steps, byStep)
	if !settled {
		return nil
	}
	if failed {
		return h.finalizeFailedExecution(
			ctx,
			&attempt,
			aggregate,
			failedCleanupReason(aggregate.Steps, byStep),
		)
	}
	for _, step := range aggregate.Steps {
		action, exists := byStep[string(step.ID)]
		if !exists || (action.Status != execution.ActionSucceeded && action.Status != execution.ActionSkipped) {
			return nil
		}
	}
	if attempt.Status == execution.ExecutionSucceeded {
		return nil
	}
	now := h.planner.clock()
	attempt.Status = execution.ExecutionSucceeded
	attempt.FailureReason = ""
	attempt.FinishedAt = &now
	aggregate.Task.Status = plan.StatusCompleted
	return h.planner.repositories.WithTx(ctx, func(repositories persistence.Repositories) error {
		if err := repositories.Executions().UpdateExecution(ctx, attempt); err != nil {
			return err
		}
		if err := repositories.CleanupTasks().UpdateTask(ctx, aggregate.Task); err != nil {
			return err
		}
		aggregateID := string(attempt.ID)
		if attempt.ContinueCount > 0 {
			aggregateID = fmt.Sprintf("%s:continue:%d", attempt.ID, attempt.ContinueCount)
		}
		return repositories.Executions().AppendOutbox(ctx, execution.OutboxEvent{
			ID: execution.OutboxEventID(idgen.MustNew("evt")), Topic: "execution.completed",
			AggregateID: aggregateID, Payload: map[string]any{"cleanup_task_id": aggregate.Task.ID, "status": attempt.Status}, CreatedAt: now,
		})
	})
}

func cleanupStepsSettled(
	steps []plan.CleanupTaskStep,
	byStep map[string]execution.ActionAttempt,
) (bool, bool) {
	stepByID := make(map[plan.StepID]plan.CleanupTaskStep, len(steps))
	hasFailure := false
	for _, step := range steps {
		stepByID[step.ID] = step
		if action, exists := byStep[string(step.ID)]; exists && action.Status == execution.ActionFailed {
			hasFailure = true
		}
	}

	blockedMemo := make(map[plan.StepID]bool, len(steps))
	visiting := make(map[plan.StepID]bool, len(steps))
	var blockedByFailure func(plan.StepID) bool
	blockedByFailure = func(stepID plan.StepID) bool {
		if blocked, exists := blockedMemo[stepID]; exists {
			return blocked
		}
		if visiting[stepID] {
			return false
		}
		visiting[stepID] = true
		defer delete(visiting, stepID)
		step, exists := stepByID[stepID]
		if !exists {
			return false
		}
		for _, dependencyID := range step.DependsOn {
			if dependency, exists := byStep[string(dependencyID)]; exists {
				if dependency.Status == execution.ActionFailed {
					blockedMemo[stepID] = true
					return true
				}
				continue
			}
			if blockedByFailure(dependencyID) {
				blockedMemo[stepID] = true
				return true
			}
		}
		blockedMemo[stepID] = false
		return false
	}

	for _, step := range steps {
		if action, exists := byStep[string(step.ID)]; exists {
			switch action.Status {
			case execution.ActionSucceeded, execution.ActionSkipped, execution.ActionFailed:
				continue
			default:
				if blockedByFailure(step.ID) {
					continue
				}
				return false, hasFailure
			}
		}
		if !blockedByFailure(step.ID) {
			return false, hasFailure
		}
	}
	return true, hasFailure
}

func failedCleanupReason(
	steps []plan.CleanupTaskStep,
	byStep map[string]execution.ActionAttempt,
) string {
	for _, step := range steps {
		action, exists := byStep[string(step.ID)]
		if !exists || action.Status != execution.ActionFailed || action.ProviderError == nil {
			continue
		}
		if reason := strings.TrimSpace(action.ProviderError.Message); reason != "" {
			return reason
		}
	}
	return "one or more cleanup actions failed"
}

func (h *ExecutionHandler) finalizeFailedExecution(
	ctx context.Context,
	attempt *execution.ExecutionAttempt,
	aggregate persistence.CleanupTaskAggregate,
	reason string,
) error {
	if attempt.Status == execution.ExecutionFailed && aggregate.Task.Status == plan.StatusFailed {
		return nil
	}
	now := h.planner.clock()
	attempt.Status = execution.ExecutionFailed
	attempt.FailureReason = reason
	attempt.FinishedAt = &now
	aggregate.Task.Status = plan.StatusFailed
	return h.planner.repositories.WithTx(ctx, func(repositories persistence.Repositories) error {
		if err := repositories.Executions().UpdateExecution(ctx, *attempt); err != nil {
			return err
		}
		return repositories.CleanupTasks().UpdateTask(ctx, aggregate.Task)
	})
}

func (h *ExecutionHandler) failExecution(ctx context.Context, attempt *execution.ExecutionAttempt, reason string) error {
	if attempt.Status == execution.ExecutionFailed {
		return nil
	}
	now := h.planner.clock()
	attempt.Status = execution.ExecutionFailed
	attempt.FailureReason = reason
	attempt.FinishedAt = &now
	return h.planner.repositories.Executions().UpdateExecution(ctx, *attempt)
}

func normalizedProviderError(err error, fallback time.Duration) (execution.ProviderError, time.Duration) {
	providerError := execution.ProviderError{Category: execution.ErrorUnknown, Message: err.Error()}
	retryAfter := fallback
	var callError *contracts.ProviderCallError
	if errors.As(err, &callError) {
		providerError = callError.Provider
		if callError.RetryAfter > 0 {
			retryAfter = callError.RetryAfter
		}
	}
	if providerError.Message == "" {
		providerError.Message = err.Error()
	}
	return providerError, retryAfter
}

func cleanupProviderSkipReason(providerError execution.ProviderError) (string, bool) {
	if providerError.Category != execution.ErrorUnsupported {
		return "", false
	}
	reason, _ := providerError.Summary["skip_reason"].(string)
	switch asset.SkipReason(strings.TrimSpace(reason)) {
	case asset.SkipProductUnsupported, asset.SkipProviderRegionUnavailable:
		return strings.TrimSpace(reason), true
	default:
		return "", false
	}
}

func managedResourceProviderRejection(action execution.ActionAttempt) bool {
	if actionRequestsManagedVerification(action) ||
		strings.HasPrefix(strings.TrimSpace(action.SkipReason), "delegated_to_") {
		return true
	}
	if action.ProviderError == nil {
		return false
	}
	code := strings.ToLower(strings.TrimSpace(action.ProviderError.Code))
	message := strings.ToLower(strings.TrimSpace(action.ProviderError.Message))
	return strings.Contains(code, "resourcemanagedbycloudproduct") ||
		strings.Contains(message, "managed by cloud product") ||
		strings.Contains(message, "has been managed by") ||
		strings.Contains(message, "is managed by")
}

func deterministicProviderRejection(providerError execution.ProviderError) bool {
	switch providerError.Category {
	case execution.ErrorInvalidRequest,
		execution.ErrorDependencyViolation,
		execution.ErrorProtected,
		execution.ErrorPermissionDenied,
		execution.ErrorConflict,
		execution.ErrorUnsupported:
		return true
	}
	code := strings.ToLower(strings.TrimSpace(providerError.Code))
	return strings.HasPrefix(code, "invalid") ||
		strings.HasPrefix(code, "illegal") ||
		strings.Contains(code, ".invalid") ||
		strings.Contains(code, ".illegal")
}

func appendCreatedFromRuntimeDependencies(
	step plan.CleanupTaskStep,
	steps []plan.CleanupTaskStep,
	relationships []graph.Relationship,
) (plan.CleanupTaskStep, int) {
	stepByAssetID := make(map[asset.AssetID]plan.StepID, len(steps))
	for _, candidate := range steps {
		stepByAssetID[candidate.AssetID] = candidate.ID
	}
	existing := make(map[plan.StepID]struct{}, len(step.DependsOn))
	for _, dependency := range step.DependsOn {
		existing[dependency] = struct{}{}
	}
	added := 0
	for _, relationship := range relationships {
		if relationship.Type != graph.RelationshipCreatedFrom ||
			relationship.TargetAssetID != step.AssetID {
			continue
		}
		dependency, exists := stepByAssetID[relationship.SourceAssetID]
		if !exists || dependency == step.ID {
			continue
		}
		if _, exists := existing[dependency]; exists {
			continue
		}
		step.DependsOn = append(step.DependsOn, dependency)
		existing[dependency] = struct{}{}
		added++
	}
	if added > 0 {
		sort.Slice(step.DependsOn, func(i, j int) bool {
			return step.DependsOn[i] < step.DependsOn[j]
		})
	}
	return step, added
}

func appendCreatedFromTaskDependencies(
	steps []plan.CleanupTaskStep,
	relationships []graph.Relationship,
) ([]plan.CleanupTaskStep, int) {
	result := append([]plan.CleanupTaskStep(nil), steps...)
	added := 0
	for index := range result {
		updated, stepAdded := appendCreatedFromRuntimeDependencies(
			result[index],
			steps,
			relationships,
		)
		result[index] = updated
		added += stepAdded
	}
	return result, added
}

func terminalDeterministicProviderRejection(action execution.ActionAttempt) bool {
	if action.ProviderError == nil || !deterministicProviderRejection(*action.ProviderError) {
		return false
	}
	retryCount := requestInt(action.Request[providerInvokeRetryCountRequestKey])
	return !verifiedSafeProviderInvokeRetry(*action.ProviderError) ||
		retryCount <= 0 || retryCount > maxVerifiedProviderInvokeRetries
}

func isProviderCategory(err error, category execution.ErrorCategory) bool {
	var callError *contracts.ProviderCallError
	return errors.As(err, &callError) && callError.Provider.Category == category
}

func findCleanupTaskStep(values []plan.CleanupTaskStep, id plan.StepID) (plan.CleanupTaskStep, bool) {
	for _, value := range values {
		if value.ID == id {
			return value, true
		}
	}
	return plan.CleanupTaskStep{}, false
}

func hasImpacts(values []plan.ImpactItem, stepID plan.StepID) bool {
	for _, value := range values {
		if value.DelegatedTo == stepID {
			return true
		}
	}
	return false
}

func payloadString(payload map[string]any, key string) string {
	value, _ := payload[key].(string)
	return strings.TrimSpace(value)
}

func cloneRequest(value map[string]any) map[string]any {
	if len(value) == 0 {
		return nil
	}
	result := make(map[string]any, len(value))
	for key, item := range value {
		result[key] = item
	}
	return result
}

func closeControllerIntegratedImpactAssets(
	ctx context.Context,
	repositories persistence.Repositories,
	impacts []plan.ImpactItem,
	stepID plan.StepID,
	closedAt time.Time,
) error {
	for _, impact := range impacts {
		if impact.DelegatedTo != stepID ||
			impact.Expected != plan.ExpectedDelegatedDelete ||
			!plan.ControllerDeletionImpliesAbsence(impact.Evidence) {
			continue
		}
		value, err := repositories.Inventory().GetAsset(ctx, impact.AssetID)
		if err != nil {
			return err
		}
		closeAssetProjection(&value, closedAt)
		if err := repositories.Inventory().PutAsset(ctx, value); err != nil {
			return err
		}
		if err := repositories.Graph().CloseAssetTopology(ctx, value.ID, closedAt); err != nil {
			return err
		}
		if err := closeResidualFinding(ctx, repositories.Findings(), value.ID, closedAt); err != nil {
			return err
		}
	}
	return nil
}

func closeAssetProjection(value *asset.Asset, closedAt time.Time) {
	if value.ClosedAt == nil {
		value.ClosedAt = &closedAt
	}
	if value.DeletedAt == nil {
		value.DeletedAt = &closedAt
	}
}

func appendActionOutcome(ctx context.Context, repositories persistence.Repositories, attempt execution.ExecutionAttempt, action execution.ActionAttempt, result string, occurredAt time.Time) error {
	evidence := map[string]any{
		"execution_id": action.ExecutionID, "cleanup_task_step_id": action.CleanupTaskStepID,
		"spec_bundle_revision": action.SpecBundleRevision, "spec_hash": action.SpecHash,
		"provider_request_id": action.ProviderRequestID, "provider_operation_id": action.ProviderOperationID,
	}
	if action.ProviderError != nil {
		evidence["provider_error"] = action.ProviderError
	}
	if action.SkipReason != "" {
		evidence["skip_reason"] = action.SkipReason
	}
	if err := repositories.Audits().AppendAuditEvent(ctx, execution.AuditEvent{
		ID: execution.AuditEventID(idgen.MustNew("aud")), ConnectionID: attempt.ConnectionID, Actor: attempt.RequestedBy,
		Action: "cleanup.action." + result, TargetType: "asset", TargetID: string(action.AssetID), Result: result,
		Evidence: evidence, CreatedAt: occurredAt,
	}); err != nil {
		return err
	}
	// An action may fail again after an operator explicitly continues the
	// execution. Each continuation needs its own outcome event; otherwise the
	// outbox's (topic, aggregate) uniqueness constraint rejects the retry and
	// leaves the task stuck in an executing state.
	aggregateID := string(action.ID)
	if attempt.ContinueCount > 0 {
		aggregateID = fmt.Sprintf("%s:continue:%d", action.ID, attempt.ContinueCount)
	}
	return repositories.Executions().AppendOutbox(ctx, execution.OutboxEvent{
		ID: execution.OutboxEventID(idgen.MustNew("evt")), Topic: "action." + result,
		AggregateID: aggregateID, Payload: map[string]any{"execution_id": action.ExecutionID, "asset_id": action.AssetID, "result": result}, CreatedAt: occurredAt,
	})
}
