package httptransport

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/loomx-ai/steward/internal/app/inventory"
	"github.com/loomx-ai/steward/internal/app/scheduling"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/schedule"
	"github.com/loomx-ai/steward/internal/persistence"
)

type scheduleView struct {
	schedule.ScanSchedule
	LastRun *scheduleRunView `json:"last_run,omitempty"`
}

type scheduleRunView struct {
	schedule.Run
	Scan *inventory.ScanTaskProjection `json:"scan,omitempty"`
}

func writeScheduleError(response http.ResponseWriter, err error) {
	var invalid *scheduling.InvalidError
	if errors.As(err, &invalid) {
		writeAPIError(response, http.StatusBadRequest, APIError{Code: invalid.Code, Message: invalid.Message})
		return
	}
	repositoryError(response, err)
}

func (a *API) schedules(response http.ResponseWriter) (*scheduling.Service, bool) {
	if a.dependencies.Schedules == nil {
		writeError(response, http.StatusInternalServerError, errors.New("scheduled scans are unavailable"))
		return nil, false
	}
	return a.dependencies.Schedules, true
}

func (a *API) listSchedules(response http.ResponseWriter, request *http.Request) {
	service, ok := a.schedules(response)
	if !ok {
		return
	}
	values, err := service.List(request.Context(), selectedConnectionID(request))
	if err != nil {
		repositoryError(response, err)
		return
	}
	views, err := a.scheduleViews(request.Context(), values)
	if err != nil {
		repositoryError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, views)
}

func (a *API) scheduleViews(ctx context.Context, values []schedule.ScanSchedule) ([]scheduleView, error) {
	ids := make([]schedule.ID, 0, len(values))
	for _, value := range values {
		ids = append(ids, value.ID)
	}
	latest, err := a.dependencies.Repositories.Schedules().LatestRuns(ctx, ids)
	if err != nil {
		return nil, err
	}
	runs := make([]schedule.Run, 0, len(latest))
	for _, run := range latest {
		runs = append(runs, run)
	}
	runViews, err := a.scheduleRunViews(ctx, runs)
	if err != nil {
		return nil, err
	}
	byID := make(map[schedule.RunID]scheduleRunView, len(runViews))
	for _, view := range runViews {
		byID[view.ID] = view
	}
	views := make([]scheduleView, 0, len(values))
	for _, value := range values {
		view := scheduleView{ScanSchedule: value}
		if run, ok := latest[value.ID]; ok {
			runView := byID[run.ID]
			view.LastRun = &runView
		}
		views = append(views, view)
	}
	return views, nil
}

func (a *API) scheduleRunViews(ctx context.Context, runs []schedule.Run) ([]scheduleRunView, error) {
	scanIDs := make([]asset.ScanTaskID, 0, len(runs))
	for _, run := range runs {
		if run.ScanTaskID != "" {
			scanIDs = append(scanIDs, run.ScanTaskID)
		}
	}
	summaries, err := a.dependencies.Repositories.Schedules().ListScanSummaries(ctx, scanIDs)
	if err != nil {
		return nil, err
	}
	changes, err := a.dependencies.Repositories.Inventory().CountAssetChanges(ctx, scanIDs)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	scans := make(map[asset.ScanTaskID]inventory.ScanTaskProjection, len(summaries))
	for _, summary := range summaries {
		projection := inventory.ProjectScanTaskListItem(summary, now)
		projection.Changes = changes[summary.ScanRun.ID]
		scans[summary.ScanRun.ID] = projection
	}
	views := make([]scheduleRunView, 0, len(runs))
	for _, run := range runs {
		view := scheduleRunView{Run: run}
		if scan, ok := scans[run.ScanTaskID]; ok {
			view.Scan = &scan
		}
		views = append(views, view)
	}
	return views, nil
}

func (a *API) createSchedule(response http.ResponseWriter, request *http.Request) {
	service, ok := a.schedules(response)
	if !ok {
		return
	}
	var input scheduling.Input
	if err := decodeJSON(request, &input); err != nil {
		writeError(response, http.StatusBadRequest, err)
		return
	}
	principal, _ := principalFromContext(request.Context())
	value, err := service.Create(request.Context(), selectedConnectionID(request), input, principal.Subject)
	if err != nil {
		writeScheduleError(response, err)
		return
	}
	writeJSON(response, http.StatusCreated, scheduleView{ScanSchedule: value})
}

func (a *API) getSchedule(response http.ResponseWriter, request *http.Request) {
	service, ok := a.schedules(response)
	if !ok {
		return
	}
	value, err := service.Get(request.Context(), selectedConnectionID(request), schedule.ID(chi.URLParam(request, "id")))
	if err != nil {
		repositoryError(response, err)
		return
	}
	views, err := a.scheduleViews(request.Context(), []schedule.ScanSchedule{value})
	if err != nil {
		repositoryError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, views[0])
}

func (a *API) updateSchedule(response http.ResponseWriter, request *http.Request) {
	service, ok := a.schedules(response)
	if !ok {
		return
	}
	var input scheduling.Input
	if err := decodeJSON(request, &input); err != nil {
		writeError(response, http.StatusBadRequest, err)
		return
	}
	principal, _ := principalFromContext(request.Context())
	value, err := service.Update(request.Context(), selectedConnectionID(request), schedule.ID(chi.URLParam(request, "id")), input, principal.Subject)
	if err != nil {
		writeScheduleError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, scheduleView{ScanSchedule: value})
}

func (a *API) enableSchedule(response http.ResponseWriter, request *http.Request) {
	a.setScheduleEnabled(response, request, true)
}

func (a *API) disableSchedule(response http.ResponseWriter, request *http.Request) {
	a.setScheduleEnabled(response, request, false)
}

func (a *API) setScheduleEnabled(response http.ResponseWriter, request *http.Request, enabled bool) {
	service, ok := a.schedules(response)
	if !ok {
		return
	}
	principal, _ := principalFromContext(request.Context())
	value, err := service.SetEnabled(request.Context(), selectedConnectionID(request), schedule.ID(chi.URLParam(request, "id")), enabled, principal.Subject)
	if err != nil {
		writeScheduleError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, scheduleView{ScanSchedule: value})
}

func (a *API) deleteSchedule(response http.ResponseWriter, request *http.Request) {
	service, ok := a.schedules(response)
	if !ok {
		return
	}
	principal, _ := principalFromContext(request.Context())
	if err := service.Delete(request.Context(), selectedConnectionID(request), schedule.ID(chi.URLParam(request, "id")), principal.Subject); err != nil {
		writeScheduleError(response, err)
		return
	}
	response.WriteHeader(http.StatusNoContent)
}

func (a *API) runSchedule(response http.ResponseWriter, request *http.Request) {
	service, ok := a.schedules(response)
	if !ok {
		return
	}
	principal, _ := principalFromContext(request.Context())
	run, err := service.RunNow(request.Context(), selectedConnectionID(request), schedule.ID(chi.URLParam(request, "id")), principal.Subject)
	if err != nil {
		writeScheduleError(response, err)
		return
	}
	views, err := a.scheduleRunViews(request.Context(), []schedule.Run{run})
	if err != nil {
		repositoryError(response, err)
		return
	}
	writeJSON(response, http.StatusAccepted, views[0])
}

func (a *API) listScheduleRuns(response http.ResponseWriter, request *http.Request) {
	service, ok := a.schedules(response)
	if !ok {
		return
	}
	value, err := service.Get(request.Context(), selectedConnectionID(request), schedule.ID(chi.URLParam(request, "id")))
	if err != nil {
		repositoryError(response, err)
		return
	}
	limit, _ := strconv.Atoi(request.URL.Query().Get("limit"))
	page, err := a.dependencies.Repositories.Schedules().ListRuns(request.Context(), value.ID, persistence.ListOptions{Limit: limit, Cursor: request.URL.Query().Get("cursor")})
	if err != nil {
		repositoryError(response, err)
		return
	}
	views, err := a.scheduleRunViews(request.Context(), page.Items)
	if err != nil {
		repositoryError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, persistence.Page[scheduleRunView]{Items: views, NextCursor: page.NextCursor})
}

func (a *API) previewSchedule(response http.ResponseWriter, request *http.Request) {
	service, ok := a.schedules(response)
	if !ok {
		return
	}
	var input struct {
		Frequency schedule.Frequency `json:"frequency"`
	}
	if err := decodeJSON(request, &input); err != nil {
		writeError(response, http.StatusBadRequest, err)
		return
	}
	upcoming, err := service.Preview(input.Frequency, 3)
	if err != nil {
		writeScheduleError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, struct {
		NextRuns           []time.Time `json:"next_runs"`
		MinIntervalSeconds int64       `json:"min_interval_seconds"`
	}{NextRuns: upcoming, MinIntervalSeconds: int64(service.MinInterval().Seconds())})
}

type connectionScheduleOverview struct {
	ConnectionID       asset.ConnectionID     `json:"connection_id"`
	ConnectionName     string                 `json:"connection_name"`
	Provider           asset.Provider         `json:"provider"`
	Status             asset.ConnectionStatus `json:"status"`
	Schedules          []scheduleView         `json:"schedules"`
	LastCompleteScanAt *time.Time             `json:"last_complete_scan_at,omitempty"`
}

func (a *API) scheduleOverview(response http.ResponseWriter, request *http.Request) {
	ctx := request.Context()
	connections, err := a.dependencies.Repositories.Connections().ListConnections(ctx, persistence.ListOptions{Limit: 500})
	if err != nil {
		repositoryError(response, err)
		return
	}
	all, err := a.dependencies.Repositories.Schedules().ListSchedules(ctx, "")
	if err != nil {
		repositoryError(response, err)
		return
	}
	views, err := a.scheduleViews(ctx, all)
	if err != nil {
		repositoryError(response, err)
		return
	}
	byConnection := map[asset.ConnectionID][]scheduleView{}
	for _, view := range views {
		byConnection[view.ConnectionID] = append(byConnection[view.ConnectionID], view)
	}
	connectionIDs := make([]asset.ConnectionID, 0, len(connections.Items))
	for _, connection := range connections.Items {
		connectionIDs = append(connectionIDs, connection.ID)
	}
	completeScans, err := a.dependencies.Repositories.Schedules().LatestCompleteScans(ctx, connectionIDs)
	if err != nil {
		repositoryError(response, err)
		return
	}
	result := make([]connectionScheduleOverview, 0, len(connections.Items))
	for _, connection := range connections.Items {
		if connection.Status == asset.ConnectionDeleted {
			continue
		}
		item := connectionScheduleOverview{
			ConnectionID: connection.ID, ConnectionName: connection.Name, Provider: connection.Provider, Status: connection.Status,
			Schedules: byConnection[connection.ID],
		}
		if item.Schedules == nil {
			item.Schedules = []scheduleView{}
		}
		if latest, ok := completeScans[connection.ID]; ok {
			at := latest.CreatedAt
			if latest.FinishedAt != nil {
				at = *latest.FinishedAt
			}
			item.LastCompleteScanAt = &at
		}
		result = append(result, item)
	}
	writeJSON(response, http.StatusOK, result)
}

type scheduleSettingsView struct {
	schedule.Settings
	MinIntervalSeconds int64 `json:"min_interval_seconds"`
}

func (a *API) getScheduleSettings(response http.ResponseWriter, request *http.Request) {
	service, ok := a.schedules(response)
	if !ok {
		return
	}
	settings, err := service.Settings(request.Context())
	if err != nil {
		repositoryError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, scheduleSettingsView{Settings: settings, MinIntervalSeconds: int64(service.MinInterval().Seconds())})
}

func (a *API) updateScheduleSettings(response http.ResponseWriter, request *http.Request) {
	service, ok := a.schedules(response)
	if !ok {
		return
	}
	var input struct {
		DefaultScheduleEnabled bool   `json:"default_schedule_enabled"`
		RetentionDays          int    `json:"retention_days"`
		DefaultTimezone        string `json:"default_timezone"`
	}
	if err := decodeJSON(request, &input); err != nil {
		writeError(response, http.StatusBadRequest, err)
		return
	}
	principal, _ := principalFromContext(request.Context())
	settings, err := service.UpdateSettings(request.Context(), schedule.Settings{
		DefaultScheduleEnabled: input.DefaultScheduleEnabled, RetentionDays: input.RetentionDays, DefaultTimezone: input.DefaultTimezone,
	}, principal.Subject)
	if err != nil {
		writeScheduleError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, scheduleSettingsView{Settings: settings, MinIntervalSeconds: int64(service.MinInterval().Seconds())})
}
