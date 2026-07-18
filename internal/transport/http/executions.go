package httptransport

import (
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/loomx-ai/steward/internal/app/cleanup"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/internal/persistence"
)

type executionAttemptProjection struct {
	execution.ExecutionAttempt
	DurationMS *int64    `json:"duration_ms,omitempty"`
	UpdatedAt  time.Time `json:"updated_at"`
}

func (a *API) createExecution(response http.ResponseWriter, request *http.Request) {
	var input struct {
		IdempotencyKey string                        `json:"idempotency_key"`
		Concurrency    *int                          `json:"concurrency"`
		Confirmation   cleanup.ExecutionConfirmation `json:"confirmation"`
	}
	if err := decodeJSON(request, &input); err != nil {
		writeError(response, http.StatusBadRequest, err)
		return
	}
	principal, _ := principalFromContext(request.Context())
	attempt, err := a.dependencies.CleanupTasks.CreateExecution(request.Context(), cleanup.CreateExecutionRequest{ConnectionID: selectedConnectionID(request), CleanupTaskID: plan.CleanupTaskID(chi.URLParam(request, "id")), RequestedBy: principal.Subject, IdempotencyKey: input.IdempotencyKey, Concurrency: input.Concurrency, Confirmation: input.Confirmation})
	if err != nil {
		writeExecutionError(response, err)
		return
	}
	writeJSON(response, http.StatusAccepted, projectExecutionTiming(attempt, nil, time.Now().UTC()))
}

func (a *API) continueExecution(response http.ResponseWriter, request *http.Request) {
	var input struct {
		IdempotencyKey string `json:"idempotency_key"`
		Concurrency    *int   `json:"concurrency"`
	}
	if err := decodeJSON(request, &input); err != nil {
		writeError(response, http.StatusBadRequest, err)
		return
	}
	principal, _ := principalFromContext(request.Context())
	attempt, err := a.dependencies.CleanupTasks.ContinueExecution(request.Context(), cleanup.ContinueExecutionRequest{
		ConnectionID: selectedConnectionID(request), CleanupTaskID: plan.CleanupTaskID(chi.URLParam(request, "id")),
		RequestedBy: principal.Subject, IdempotencyKey: input.IdempotencyKey, Concurrency: input.Concurrency,
	})
	if err != nil {
		writeExecutionError(response, err)
		return
	}
	writeJSON(response, http.StatusAccepted, projectExecutionTiming(attempt, nil, time.Now().UTC()))
}

func (a *API) pauseExecution(response http.ResponseWriter, request *http.Request) {
	principal, _ := principalFromContext(request.Context())
	attempt, err := a.dependencies.CleanupTasks.PauseExecution(request.Context(), cleanup.PauseExecutionRequest{
		ConnectionID: selectedConnectionID(request), CleanupTaskID: plan.CleanupTaskID(chi.URLParam(request, "id")),
		RequestedBy: principal.Subject,
	})
	if err != nil {
		writeExecutionError(response, err)
		return
	}
	writeJSON(response, http.StatusAccepted, projectExecutionTiming(attempt, nil, time.Now().UTC()))
}

func (a *API) resumeExecution(response http.ResponseWriter, request *http.Request) {
	principal, _ := principalFromContext(request.Context())
	attempt, err := a.dependencies.CleanupTasks.ResumeExecution(request.Context(), cleanup.ResumeExecutionRequest{
		ConnectionID: selectedConnectionID(request), CleanupTaskID: plan.CleanupTaskID(chi.URLParam(request, "id")),
		RequestedBy: principal.Subject,
	})
	if err != nil {
		writeExecutionError(response, err)
		return
	}
	writeJSON(response, http.StatusAccepted, projectExecutionTiming(attempt, nil, time.Now().UTC()))
}

func writeExecutionError(response http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, cleanup.ErrExecutionConcurrency):
		writeAPIError(response, http.StatusBadRequest, APIError{Code: "cleanup.concurrency_invalid", Message: err.Error()})
	case errors.Is(err, cleanup.ErrExecutionConfirmation):
		writeAPIError(response, http.StatusBadRequest, APIError{Code: "cleanup.confirmation_invalid", Message: err.Error()})
	case errors.Is(err, cleanup.ErrExecutionNotContinuable):
		writeAPIError(response, http.StatusConflict, APIError{Code: "cleanup.not_continuable", Message: err.Error()})
	case errors.Is(err, cleanup.ErrExecutionNotPausable):
		writeAPIError(response, http.StatusConflict, APIError{Code: "cleanup.not_pausable", Message: err.Error()})
	case errors.Is(err, cleanup.ErrExecutionNotResumable):
		writeAPIError(response, http.StatusConflict, APIError{Code: "cleanup.not_resumable", Message: err.Error()})
	case errors.Is(err, plan.ErrCleanupTaskInvalidated):
		writeAPIError(response, http.StatusConflict, APIError{Code: "cleanup.invalidated", Message: err.Error()})
	case errors.Is(err, cleanup.ErrInventoryReconciliationPending):
		writeAPIError(response, http.StatusConflict, APIError{Code: "cleanup.inventory_reconciling", Message: err.Error()})
	case errors.Is(err, asset.ErrConnectionNotValidated):
		writeAPIError(response, http.StatusConflict, APIError{Code: "connection_not_validated", Message: err.Error()})
	default:
		repositoryError(response, err)
	}
}

func (a *API) getExecution(response http.ResponseWriter, request *http.Request) {
	attempt, err := a.dependencies.Repositories.Executions().GetExecution(request.Context(), execution.ExecutionID(chi.URLParam(request, "id")))
	if err == nil && attempt.ConnectionID != selectedConnectionID(request) {
		err = persistence.ErrNotFound
	}
	if err != nil {
		repositoryError(response, err)
		return
	}
	projection, err := a.executionProjection(request, attempt)
	if err != nil {
		repositoryError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, projection)
}

func (a *API) listExecutions(response http.ResponseWriter, request *http.Request) {
	page, err := a.dependencies.Repositories.Executions().ListExecutions(request.Context(), pageOptions(request))
	if err != nil {
		repositoryError(response, err)
		return
	}
	projected, err := a.projectExecutionPage(request, page)
	if err != nil {
		repositoryError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, projected)
}

func (a *API) listCleanupTaskExecutions(response http.ResponseWriter, request *http.Request) {
	options := pageOptions(request)
	page, err := a.dependencies.Repositories.Executions().ListCleanupTaskExecutions(
		request.Context(),
		selectedConnectionID(request),
		chi.URLParam(request, "id"),
		options,
	)
	if err != nil {
		repositoryError(response, err)
		return
	}
	projected, err := a.projectExecutionPage(request, page)
	if err != nil {
		repositoryError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, projected)
}

func (a *API) executionProjection(request *http.Request, attempt execution.ExecutionAttempt) (executionAttemptProjection, error) {
	jobs, err := a.dependencies.Repositories.Jobs().ListJobsByAggregate(request.Context(), "cleanup_task", attempt.CleanupTaskID)
	if err != nil {
		return executionAttemptProjection{}, err
	}
	return projectExecutionTiming(attempt, jobs, time.Now().UTC()), nil
}

func (a *API) projectExecutionPage(
	request *http.Request,
	page persistence.Page[execution.ExecutionAttempt],
) (persistence.Page[executionAttemptProjection], error) {
	cleanupTaskIDs := make([]string, 0, len(page.Items))
	seen := make(map[string]struct{}, len(page.Items))
	for _, attempt := range page.Items {
		if _, exists := seen[attempt.CleanupTaskID]; exists {
			continue
		}
		seen[attempt.CleanupTaskID] = struct{}{}
		cleanupTaskIDs = append(cleanupTaskIDs, attempt.CleanupTaskID)
	}
	jobsByCleanupTask, err := a.dependencies.Repositories.Jobs().ListJobsByAggregates(request.Context(), "cleanup_task", cleanupTaskIDs)
	if err != nil {
		return persistence.Page[executionAttemptProjection]{}, err
	}
	now := time.Now().UTC()
	items := make([]executionAttemptProjection, 0, len(page.Items))
	for _, attempt := range page.Items {
		items = append(items, projectExecutionTiming(attempt, jobsByCleanupTask[attempt.CleanupTaskID], now))
	}
	return persistence.Page[executionAttemptProjection]{Items: items, NextCursor: page.NextCursor}, nil
}

func projectExecutionTiming(attempt execution.ExecutionAttempt, jobs []execution.Job, now time.Time) executionAttemptProjection {
	matching := make([]execution.Job, 0, len(jobs))
	for _, job := range jobs {
		if executionID, _ := job.Payload["execution_id"].(string); executionID == string(attempt.ID) {
			matching = append(matching, job)
		}
	}
	projection := executionAttemptProjection{ExecutionAttempt: attempt, UpdatedAt: attempt.CreatedAt}
	for _, value := range []*time.Time{attempt.StartedAt, attempt.FinishedAt} {
		if value != nil && value.After(projection.UpdatedAt) {
			projection.UpdatedAt = *value
		}
	}
	projection.UpdatedAt = execution.LatestJobUpdate(matching, projection.UpdatedAt)
	if duration, recorded := execution.ActiveDuration(matching, now); recorded {
		milliseconds := duration.Milliseconds()
		projection.DurationMS = &milliseconds
	}
	return projection
}

func (a *API) listExecutionActions(response http.ResponseWriter, request *http.Request) {
	actions, err := a.dependencies.Repositories.Executions().ListExecutionActions(
		request.Context(),
		selectedConnectionID(request),
		execution.ExecutionID(chi.URLParam(request, "id")),
	)
	if err != nil {
		repositoryError(response, err)
		return
	}
	if actions == nil {
		actions = []execution.ActionAttempt{}
	}
	writeJSON(response, http.StatusOK, struct {
		Items []execution.ActionAttempt `json:"items"`
	}{Items: actions})
}
