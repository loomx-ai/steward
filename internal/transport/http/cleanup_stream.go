package httptransport

import (
	"fmt"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/internal/persistence"
)

func (a *API) loadCleanupTask(request *http.Request) (plan.CleanupTask, error) {
	aggregate, err := a.dependencies.Repositories.CleanupTasks().GetTask(request.Context(), plan.CleanupTaskID(chi.URLParam(request, "id")))
	if err == nil && aggregate.Task.ConnectionID != selectedConnectionID(request) {
		err = persistence.ErrNotFound
	}
	return aggregate.Task, err
}

func (a *API) cleanupTaskLogs(response http.ResponseWriter, request *http.Request) {
	filter := cleanupLogFilter(request)
	before := strings.TrimSpace(request.URL.Query().Get("before"))
	beforeTime, beforeID, err := parseScanEventCursor(before)
	if err != nil {
		writeAPIError(response, http.StatusBadRequest, APIError{Code: "cleanup.log_cursor_invalid", Message: err.Error()})
		return
	}
	limit, _ := strconv.Atoi(request.URL.Query().Get("limit"))
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	_, logs, err := a.dependencies.Repositories.Jobs().ListCleanupLogsBefore(
		request.Context(),
		selectedConnectionID(request),
		plan.CleanupTaskID(chi.URLParam(request, "id")),
		filter,
		beforeTime,
		beforeID,
		limit+1,
	)
	if err != nil {
		repositoryError(response, err)
		return
	}
	hasMore := len(logs) > limit
	if hasMore {
		logs = logs[1:]
	}
	page := struct {
		Items      []execution.JobLog `json:"items"`
		NextCursor string             `json:"next_cursor,omitempty"`
		LiveCursor string             `json:"live_cursor,omitempty"`
	}{Items: logs}
	if page.Items == nil {
		page.Items = []execution.JobLog{}
	}
	if hasMore && len(logs) > 0 {
		page.NextCursor = scanEventCursor(logs[0].CreatedAt, logs[0].ID)
	}
	if before == "" && len(logs) > 0 {
		latest := logs[len(logs)-1]
		page.LiveCursor = scanEventCursor(latest.CreatedAt, latest.ID)
	}
	writeJSON(response, http.StatusOK, page)
}

func (a *API) cleanupTaskEvents(response http.ResponseWriter, request *http.Request) {
	filter := cleanupLogFilter(request)
	cleanupTask, err := a.loadCleanupTask(request)
	if err != nil {
		repositoryError(response, err)
		return
	}
	afterTime, afterID, err := parseScanEventCursor(request.Header.Get("Last-Event-ID"))
	if err != nil {
		writeAPIError(response, http.StatusBadRequest, APIError{Code: "cleanup.event_cursor_invalid", Message: err.Error()})
		return
	}
	response.Header().Set("Content-Type", "text/event-stream")
	response.Header().Set("Cache-Control", "no-cache")
	flusher, _ := response.(http.Flusher)
	writeScanEvent(response, "snapshot", "", cleanupTask)
	lastTask := cleanupTask
	if flusher != nil {
		flusher.Flush()
	}
	poll := a.dependencies.SSEPollInterval
	if poll <= 0 {
		poll = time.Second
	}
	for {
		// The log query returns the task header with the connection check, so
		// the poll reads no steps or impact rows.
		latestTask, logs, err := a.dependencies.Repositories.Jobs().ListCleanupLogsAfter(
			request.Context(),
			selectedConnectionID(request),
			cleanupTask.ID,
			filter,
			afterTime,
			afterID,
			100,
		)
		if err != nil {
			return
		}
		for _, log := range logs {
			afterTime, afterID = log.CreatedAt, log.ID
			writeScanEvent(response, "log", scanEventCursor(afterTime, afterID), log)
		}
		cleanupTask = latestTask
		switch {
		case isTerminalCleanupStatus(cleanupTask.Status):
			writeScanEvent(response, "end", "", cleanupTask)
			if flusher != nil {
				flusher.Flush()
			}
			return
		case !reflect.DeepEqual(lastTask, cleanupTask):
			writeScanEvent(response, "snapshot", "", cleanupTask)
			lastTask = cleanupTask
		case len(logs) == 0:
			_, _ = fmt.Fprint(response, ": heartbeat\n\n")
		}
		if flusher != nil {
			flusher.Flush()
		}
		select {
		case <-request.Context().Done():
			return
		case <-time.After(poll):
		}
	}
}

// cleanupTaskProgress streams what the cleanup detail page renders while a
// task runs, so the page does not poll: the full aggregate first (and again if
// the steps change), then the task header, the impact items and the latest
// execution attempt's actions that changed since the last event, and that
// attempt whenever it changes. Each connection starts from the full state, so
// a reconnect needs no cursor.
func (a *API) cleanupTaskProgress(response http.ResponseWriter, request *http.Request) {
	if _, err := a.loadCleanupTask(request); err != nil {
		repositoryError(response, err)
		return
	}
	response.Header().Set("Content-Type", "text/event-stream")
	response.Header().Set("Cache-Control", "no-cache")
	flusher, _ := response.(http.Flusher)
	poll := a.dependencies.SSEPollInterval
	if poll <= 0 {
		poll = time.Second
	}
	progress := cleanupProgress{
		connectionID: selectedConnectionID(request),
		taskID:       plan.CleanupTaskID(chi.URLParam(request, "id")),
	}
	for {
		wrote, ended, err := a.writeCleanupProgress(response, request, &progress)
		if err != nil {
			return
		}
		switch {
		case ended:
			writeScanEvent(response, "end", "", progress.task)
		case !wrote:
			_, _ = fmt.Fprint(response, ": heartbeat\n\n")
		}
		if flusher != nil {
			flusher.Flush()
		}
		if ended {
			return
		}
		select {
		case <-request.Context().Done():
			return
		case <-time.After(poll):
		}
	}
}

// cleanupProgress is what one progress stream has sent so far.
type cleanupProgress struct {
	connectionID asset.ConnectionID
	taskID       plan.CleanupTaskID
	started      bool
	version      string
	steps        []plan.CleanupTaskStep
	task         plan.CleanupTask
	impacts      map[plan.ImpactItemID]plan.ImpactItem
	execution    *executionAttemptProjection
	actions      map[execution.ActionAttemptID]execution.ActionAttempt
	actionsDone  bool
}

// writeCleanupProgress writes an event for each part of the task that changed
// since the last call and reports whether it wrote any and whether the task has
// settled: terminal, with its latest attempt and every action finished. The
// task, impact and action rows are read only when the task's version moved;
// the attempt is projected every call because its timing follows its jobs.
func (a *API) writeCleanupProgress(response http.ResponseWriter, request *http.Request, progress *cleanupProgress) (wrote, ended bool, err error) {
	ctx := request.Context()
	version, err := a.dependencies.Repositories.CleanupTasks().CleanupTaskVersion(ctx, progress.connectionID, progress.taskID)
	if err != nil {
		return false, false, err
	}
	changed := !progress.started || version != progress.version
	if changed {
		if wrote, err = a.writeCleanupTaskChanges(response, request, progress); err != nil {
			return wrote, false, err
		}
		progress.version = version
	}

	latest, err := a.latestCleanupExecution(request, progress.connectionID, progress.taskID)
	if err != nil {
		return wrote, false, err
	}
	settled := isTerminalCleanupStatus(progress.task.Status)
	if latest == nil {
		return wrote, settled, nil
	}
	if !reflect.DeepEqual(progress.execution, latest) {
		writeScanEvent(response, "execution", "", latest)
		wrote = true
	}
	if progress.execution == nil || progress.execution.ID != latest.ID {
		progress.actions = map[execution.ActionAttemptID]execution.ActionAttempt{}
	}
	progress.execution = latest
	if changed {
		actions, err := a.dependencies.Repositories.Executions().ListExecutionActions(ctx, progress.connectionID, latest.ID)
		if err != nil {
			return wrote, false, err
		}
		changedActions := []execution.ActionAttempt{}
		progress.actionsDone = true
		for _, action := range actions {
			action = execution.PublicActionAttempt(action)
			if previous, seen := progress.actions[action.ID]; !seen || !reflect.DeepEqual(previous, action) {
				changedActions = append(changedActions, action)
				progress.actions[action.ID] = action
			}
			progress.actionsDone = progress.actionsDone && isTerminalActionStatus(action.Status)
		}
		if len(changedActions) > 0 {
			writeScanEvent(response, "actions", "", struct {
				ExecutionID execution.ExecutionID     `json:"execution_id"`
				Items       []execution.ActionAttempt `json:"items"`
			}{ExecutionID: latest.ID, Items: changedActions})
			wrote = true
		}
	}
	return wrote, settled && progress.actionsDone && isTerminalExecutionStatus(latest.Status), nil
}

// writeCleanupTaskChanges writes the full aggregate on the first call or when
// the steps changed, and otherwise the task header and the impact items that
// changed since the last call.
func (a *API) writeCleanupTaskChanges(response http.ResponseWriter, request *http.Request, progress *cleanupProgress) (wrote bool, err error) {
	ctx := request.Context()
	stored, err := a.dependencies.Repositories.CleanupTasks().GetTask(ctx, progress.taskID)
	if err == nil && stored.Task.ConnectionID != progress.connectionID {
		err = persistence.ErrNotFound
	}
	if err != nil {
		return false, err
	}
	if !progress.started || !reflect.DeepEqual(progress.steps, stored.Steps) {
		// The page shows the steps the service derives, so the full aggregate
		// comes from it rather than from the stored rows.
		aggregate, err := a.dependencies.CleanupTasks.GetTask(ctx, progress.taskID, progress.connectionID)
		if err != nil {
			return false, err
		}
		writeScanEvent(response, "aggregate", "", aggregate)
		progress.started = true
		progress.steps = stored.Steps
		progress.task = aggregate.Task
		progress.impacts = make(map[plan.ImpactItemID]plan.ImpactItem, len(aggregate.ImpactItems))
		for _, impact := range aggregate.ImpactItems {
			progress.impacts[impact.ID] = impact
		}
		return true, nil
	}
	if !reflect.DeepEqual(progress.task, stored.Task) {
		writeScanEvent(response, "task", "", stored.Task)
		progress.task = stored.Task
		wrote = true
	}
	changed := []plan.ImpactItem{}
	for _, impact := range stored.ImpactItems {
		if previous, seen := progress.impacts[impact.ID]; !seen || !reflect.DeepEqual(previous, impact) {
			changed = append(changed, impact)
			progress.impacts[impact.ID] = impact
		}
	}
	if len(changed) > 0 {
		writeScanEvent(response, "impacts", "", struct {
			Items []plan.ImpactItem `json:"items"`
		}{Items: changed})
		wrote = true
	}
	return wrote, nil
}

// latestCleanupExecution returns the task's newest execution attempt with
// its timing, or nil before the first one. Attempts list oldest first.
func (a *API) latestCleanupExecution(request *http.Request, connectionID asset.ConnectionID, taskID plan.CleanupTaskID) (*executionAttemptProjection, error) {
	var latest persistence.Page[execution.ExecutionAttempt]
	options := persistence.ListOptions{Limit: 100}
	for {
		page, err := a.dependencies.Repositories.Executions().ListCleanupTaskExecutions(request.Context(), connectionID, string(taskID), options)
		if err != nil {
			return nil, err
		}
		if len(page.Items) > 0 {
			latest.Items = page.Items[len(page.Items)-1:]
		}
		if page.NextCursor == "" {
			break
		}
		options.Cursor = page.NextCursor
	}
	if len(latest.Items) == 0 {
		return nil, nil
	}
	projected, err := a.projectExecutionPage(request, latest)
	if err != nil {
		return nil, err
	}
	return &projected.Items[0], nil
}

func cleanupLogFilter(request *http.Request) persistence.CleanupLogFilter {
	values := request.URL.Query()
	resourceKindIDs := make([]asset.ResourceKindID, 0, len(values["resource_kind_id"]))
	seen := make(map[asset.ResourceKindID]struct{}, len(values["resource_kind_id"]))
	for _, value := range values["resource_kind_id"] {
		resourceKindID := asset.ResourceKindID(strings.TrimSpace(value))
		if resourceKindID == "" {
			continue
		}
		if _, exists := seen[resourceKindID]; exists {
			continue
		}
		seen[resourceKindID] = struct{}{}
		resourceKindIDs = append(resourceKindIDs, resourceKindID)
	}
	return persistence.CleanupLogFilter{
		ResourceID:      strings.TrimSpace(values.Get("resource_id")),
		ResourceKindIDs: resourceKindIDs,
	}
}

func isTerminalCleanupStatus(status plan.Status) bool {
	switch status {
	case plan.StatusCompleted, plan.StatusFailed, plan.StatusCanceled, plan.StatusInvalidated:
		return true
	default:
		return false
	}
}

func isTerminalExecutionStatus(status execution.ExecutionStatus) bool {
	switch status {
	case execution.ExecutionSucceeded, execution.ExecutionFailed, execution.ExecutionCanceled:
		return true
	default:
		return false
	}
}

func isTerminalActionStatus(status execution.ActionStatus) bool {
	switch status {
	case execution.ActionSucceeded, execution.ActionSkipped, execution.ActionFailed:
		return true
	default:
		return false
	}
}
