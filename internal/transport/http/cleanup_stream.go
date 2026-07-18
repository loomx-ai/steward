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
		_, logs, err := a.dependencies.Repositories.Jobs().ListCleanupLogsAfter(
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
		cleanupTask, err = a.loadCleanupTask(request)
		if err != nil {
			return
		}
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
