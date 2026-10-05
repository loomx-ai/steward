package httptransport

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/loomx-ai/steward/internal/app/inventory"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/core/schedule"
	"github.com/loomx-ai/steward/internal/persistence"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func (a *API) createScan(response http.ResponseWriter, request *http.Request) {
	var input struct {
		ScopeMode       asset.ScanScopeMode              `json:"scope_mode"`
		RegionIDs       []string                         `json:"region_ids"`
		NetworkTargets  []inventory.NetworkTargetRequest `json:"network_targets"`
		ResourceKindIDs []asset.ResourceKindID           `json:"resource_kind_ids"`
	}
	if err := decodeJSON(request, &input); err != nil {
		writeError(response, http.StatusBadRequest, err)
		return
	}
	if a.dependencies.Scans == nil {
		writeError(response, http.StatusInternalServerError, errors.New("scan creator is unavailable"))
		return
	}
	principal, _ := principalFromContext(request.Context())
	created, err := a.dependencies.Scans.Create(request.Context(), inventory.ScanCreationRequest{
		ConnectionID: selectedConnectionID(request), RequestedBy: principal.Subject, ScopeMode: input.ScopeMode,
		RegionIDs: input.RegionIDs, NetworkTargets: input.NetworkTargets, ResourceKindIDs: input.ResourceKindIDs,
	})
	if err != nil {
		writeScanRequestError(response, scanRequestStatus(err), err)
		return
	}
	writeJSON(response, http.StatusAccepted, inventory.ProjectScanTask(created.ScanRun, created.Shards))
}

func (a *API) listScans(response http.ResponseWriter, request *http.Request) {
	page, err := a.dependencies.Repositories.Inventory().ListScanRunListItems(request.Context(), pageOptions(request))
	if err != nil {
		repositoryError(response, err)
		return
	}
	now := time.Now().UTC()
	ids := make([]asset.ScanTaskID, 0, len(page.Items))
	for _, item := range page.Items {
		ids = append(ids, item.ScanRun.ID)
	}
	changes, err := a.dependencies.Repositories.Inventory().CountAssetChanges(request.Context(), ids)
	if err != nil {
		repositoryError(response, err)
		return
	}
	names, err := a.scheduleNames(request.Context(), selectedConnectionID(request))
	if err != nil {
		repositoryError(response, err)
		return
	}
	items := make([]inventory.ScanTaskProjection, 0, len(page.Items))
	for _, item := range page.Items {
		// A succeeded task's dynamic retry eligibility depends on the current
		// provider plan. Resolve that on the detail route, not once per list row.
		projection := inventory.ProjectScanTaskListItem(item, now)
		projection.Changes = changes[item.ScanRun.ID]
		if name, ok := names[schedule.ID(item.ScanRun.ScheduleID)]; ok {
			projection.ScheduleName = &name
		}
		items = append(items, projection)
	}
	writeJSON(response, http.StatusOK, persistence.Page[inventory.ScanTaskProjection]{Items: items, NextCursor: page.NextCursor})
}

func (a *API) getScan(response http.ResponseWriter, request *http.Request) {
	projection, err := a.scanProjection(request, asset.ScanTaskID(chi.URLParam(request, "id")))
	if err != nil {
		repositoryError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, projection)
}

func (a *API) pauseScan(response http.ResponseWriter, request *http.Request) {
	a.controlScan(response, request, "pause")
}
func (a *API) resumeScan(response http.ResponseWriter, request *http.Request) {
	a.controlScan(response, request, "resume")
}
func (a *API) cancelScan(response http.ResponseWriter, request *http.Request) {
	a.controlScan(response, request, "cancel")
}
func (a *API) retryScan(response http.ResponseWriter, request *http.Request) {
	a.controlScan(response, request, "retry")
}

func (a *API) controlScan(response http.ResponseWriter, request *http.Request, action string) {
	if a.dependencies.ScanControls == nil {
		writeError(response, http.StatusInternalServerError, errors.New("scan control is unavailable"))
		return
	}
	id := asset.ScanTaskID(chi.URLParam(request, "id"))
	current, err := a.dependencies.Repositories.Inventory().GetScanRun(request.Context(), id)
	if err == nil && current.ConnectionID != selectedConnectionID(request) {
		err = persistence.ErrNotFound
	}
	if err != nil {
		repositoryError(response, err)
		return
	}
	principal, _ := principalFromContext(request.Context())
	switch action {
	case "pause":
		_, err = a.dependencies.ScanControls.Pause(request.Context(), id, principal.Subject)
	case "resume":
		_, err = a.dependencies.ScanControls.Resume(request.Context(), id, principal.Subject)
	case "cancel":
		_, err = a.dependencies.ScanControls.Cancel(request.Context(), id, principal.Subject)
	case "retry":
		_, err = a.dependencies.ScanControls.Retry(request.Context(), id, principal.Subject)
	}
	if err != nil {
		writeScanError(response, err)
		return
	}
	projection, err := a.scanProjection(request, id)
	if err != nil {
		repositoryError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, projection)
}

func (a *API) listVPCTargets(response http.ResponseWriter, request *http.Request) {
	a.listNetworkTargets(response, request, asset.ScanTargetVPC)
}

func (a *API) listVSwitchTargets(response http.ResponseWriter, request *http.Request) {
	a.listNetworkTargets(response, request, asset.ScanTargetVSwitch)
}

func (a *API) listNetworkTargets(response http.ResponseWriter, request *http.Request, kind asset.ScanTargetKind) {
	if a.dependencies.NetworkTargets == nil {
		writeError(response, http.StatusInternalServerError, errors.New("network target discovery is unavailable"))
		return
	}
	connectionID := selectedConnectionID(request)
	connection, ok := selectedConnection(request)
	if !ok {
		var err error
		connection, err = a.dependencies.Repositories.Connections().GetConnection(request.Context(), connectionID)
		if err != nil {
			repositoryError(response, err)
			return
		}
	}
	discoverer, err := a.dependencies.NetworkTargets.ResolveNetworkTargetDiscoverer(connection.Provider)
	if err != nil {
		writeError(response, http.StatusServiceUnavailable, err)
		return
	}
	limit, _ := strconv.Atoi(request.URL.Query().Get("limit"))
	page, err := discoverer.SearchNetworkTargets(request.Context(), contracts.NetworkTargetQuery{
		ConnectionID: connectionID, Kind: kind, RegionID: request.URL.Query().Get("region_id"),
		ParentNativeID: request.URL.Query().Get("vpc_id"), Query: request.URL.Query().Get("query"), Cursor: request.URL.Query().Get("cursor"), Limit: limit,
	})
	if err != nil {
		if writeProviderNetworkError(response, err) {
			return
		}
		writeError(response, http.StatusBadRequest, err)
		return
	}
	if page.Items == nil {
		page.Items = []contracts.NetworkTargetOption{}
	}
	writeJSON(response, http.StatusOK, page)
}

func (a *API) scanProjection(request *http.Request, id asset.ScanTaskID) (inventory.ScanTaskProjection, error) {
	task, err := a.dependencies.Repositories.Inventory().GetScanRun(request.Context(), id)
	if err != nil {
		return inventory.ScanTaskProjection{}, err
	}
	if task.ConnectionID != selectedConnectionID(request) {
		return inventory.ScanTaskProjection{}, persistence.ErrNotFound
	}
	shards, err := a.dependencies.Repositories.Inventory().ListScanShardsByRun(request.Context(), task.ID)
	if err != nil {
		return inventory.ScanTaskProjection{}, err
	}
	return a.projectScanTask(request.Context(), task, shards)
}

func (a *API) projectScanTask(
	ctx context.Context,
	task asset.ScanTask,
	shards []asset.ScanShard,
) (inventory.ScanTaskProjection, error) {
	projection := inventory.ProjectScanTask(task, shards)
	if a.dependencies.ScanControls != nil {
		canRetry, err := a.dependencies.ScanControls.CanRetry(ctx, task, shards)
		if err != nil {
			return inventory.ScanTaskProjection{}, err
		}
		if canRetry && !containsScanAction(projection.AllowedActions, "retry") {
			projection.AllowedActions = append(projection.AllowedActions, "retry")
		}
	}
	return a.refreshScanProjection(ctx, projection)
}

// refreshScanProjection applies the parts of a scan projection that change
// without its task or shards changing: job timing, change counts and the
// schedule name.
func (a *API) refreshScanProjection(ctx context.Context, projection inventory.ScanTaskProjection) (inventory.ScanTaskProjection, error) {
	jobs, err := a.dependencies.Repositories.Jobs().ListJobsByAggregate(ctx, "scan_task", string(projection.ID))
	if err != nil {
		return inventory.ScanTaskProjection{}, err
	}
	projection = inventory.ApplyScanTiming(projection, jobs, time.Now().UTC())
	changes, err := a.dependencies.Repositories.Inventory().CountAssetChanges(ctx, []asset.ScanTaskID{projection.ID})
	if err != nil {
		return inventory.ScanTaskProjection{}, err
	}
	projection.Changes = changes[projection.ID]
	projection.ScheduleName = nil
	if projection.ScheduleID != "" {
		value, err := a.dependencies.Repositories.Schedules().GetSchedule(ctx, schedule.ID(projection.ScheduleID))
		if err == nil {
			projection.ScheduleName = &value.Name
		}
	}
	return projection, nil
}

// scanProgressMarker is what a scan projection's task and shard parts are
// derived from, read without loading every shard.
type scanProgressMarker struct {
	task   asset.ScanTask
	shards []persistence.ScanShardProgress
}

func (a *API) scanProgressMarker(ctx context.Context, id asset.ScanTaskID) (scanProgressMarker, error) {
	task, err := a.dependencies.Repositories.Inventory().GetScanRun(ctx, id)
	if err != nil {
		return scanProgressMarker{}, err
	}
	shards, err := a.dependencies.Repositories.Inventory().ScanShardProgress(ctx, id)
	if err != nil {
		return scanProgressMarker{}, err
	}
	return scanProgressMarker{task: task, shards: shards}, nil
}

func containsScanAction(actions []string, expected string) bool {
	for _, action := range actions {
		if action == expected {
			return true
		}
	}
	return false
}

func (a *API) scheduleNames(ctx context.Context, connectionID asset.ConnectionID) (map[schedule.ID]string, error) {
	values, err := a.dependencies.Repositories.Schedules().ListSchedules(ctx, connectionID)
	if err != nil {
		return nil, err
	}
	names := make(map[schedule.ID]string, len(values))
	for _, value := range values {
		names[value.ID] = value.Name
	}
	return names, nil
}

func (a *API) scanChanges(response http.ResponseWriter, request *http.Request) {
	task, err := a.dependencies.Repositories.Inventory().GetScanRun(request.Context(), asset.ScanTaskID(chi.URLParam(request, "id")))
	if err == nil && task.ConnectionID != selectedConnectionID(request) {
		err = persistence.ErrNotFound
	}
	if err != nil {
		repositoryError(response, err)
		return
	}
	changeType := asset.ChangeType(strings.TrimSpace(request.URL.Query().Get("change_type")))
	if changeType != "" && !changeType.Valid() {
		writeAPIError(response, http.StatusBadRequest, APIError{Code: "scan.change_type_invalid", Message: "change_type must be added, removed or modified"})
		return
	}
	limit, _ := strconv.Atoi(request.URL.Query().Get("limit"))
	page, err := a.dependencies.Repositories.Inventory().ListAssetChanges(request.Context(), persistence.AssetChangeListOptions{
		ScanTaskID: task.ID, Type: changeType, Query: request.URL.Query().Get("q"),
		Limit: limit, Cursor: request.URL.Query().Get("cursor"),
	})
	if err != nil {
		repositoryError(response, err)
		return
	}
	if page.Items == nil {
		page.Items = []asset.AssetChange{}
	}
	writeJSON(response, http.StatusOK, page)
}

func (a *API) scanLogs(response http.ResponseWriter, request *http.Request) {
	id := asset.ScanTaskID(chi.URLParam(request, "id"))
	before := strings.TrimSpace(request.URL.Query().Get("before"))
	beforeTime, beforeID, err := parseScanEventCursor(before)
	if err != nil {
		writeAPIError(response, http.StatusBadRequest, APIError{Code: "scan.log_cursor_invalid", Message: err.Error()})
		return
	}
	limit, _ := strconv.Atoi(request.URL.Query().Get("limit"))
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	task, logs, err := a.dependencies.Repositories.Jobs().ListScanLogsBefore(
		request.Context(),
		selectedConnectionID(request),
		id,
		request.URL.Query().Get("target_key"),
		beforeTime,
		beforeID,
		limit+1,
	)
	if err != nil {
		repositoryError(response, err)
		return
	}
	projection := inventory.ProjectScanTask(task, nil)
	_, err = scanLogTargetKey(request, projection)
	if err != nil {
		writeAPIError(response, http.StatusBadRequest, APIError{Code: "scan.log_target_invalid", Message: err.Error()})
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

func (a *API) scanEvents(response http.ResponseWriter, request *http.Request) {
	id := asset.ScanTaskID(chi.URLParam(request, "id"))
	// A marker is read before the projection it describes, so a change racing
	// a rebuild only causes one more rebuild.
	lastMarker, err := a.scanProgressMarker(request.Context(), id)
	if err != nil {
		repositoryError(response, err)
		return
	}
	projection, err := a.scanProjection(request, id)
	if err != nil {
		repositoryError(response, err)
		return
	}
	targetKey, err := scanLogTargetKey(request, projection)
	if err != nil {
		writeAPIError(response, http.StatusBadRequest, APIError{Code: "scan.log_target_invalid", Message: err.Error()})
		return
	}
	afterTime, afterID, err := parseScanEventCursor(request.Header.Get("Last-Event-ID"))
	if err != nil {
		writeAPIError(response, http.StatusBadRequest, APIError{Code: "scan.event_cursor_invalid", Message: err.Error()})
		return
	}
	response.Header().Set("Content-Type", "text/event-stream")
	response.Header().Set("Cache-Control", "no-cache")
	flusher, _ := response.(http.Flusher)
	writeScanEvent(response, "snapshot", "", projection)
	lastProjection := projection
	if flusher != nil {
		flusher.Flush()
	}
	poll := a.dependencies.SSEPollInterval
	if poll <= 0 {
		poll = time.Second
	}
	for {
		logs, err := a.dependencies.Repositories.Jobs().ListLogsByAggregate(request.Context(), "scan_task", string(id), targetKey, afterTime, afterID, 100)
		if err != nil {
			return
		}
		for _, log := range logs {
			afterTime, afterID = log.CreatedAt, log.ID
			writeScanEvent(response, "log", scanEventCursor(afterTime, afterID), log)
		}
		marker, err := a.scanProgressMarker(request.Context(), id)
		if err != nil {
			return
		}
		if reflect.DeepEqual(marker, lastMarker) {
			// Only timing, change counts or the schedule name can differ.
			projection, err = a.refreshScanProjection(request.Context(), lastProjection)
		} else {
			projection, err = a.scanProjection(request, id)
		}
		if err != nil {
			return
		}
		lastMarker = marker
		changed := scanProjectionChanged(lastProjection, projection)
		if isTerminalScanStatus(projection.Status) {
			writeScanEvent(response, "end", "", projection)
			if flusher != nil {
				flusher.Flush()
			}
			return
		} else if changed {
			writeScanEvent(response, "snapshot", "", projection)
			lastProjection = projection
		} else if len(logs) == 0 {
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

func scanLogTargetKey(request *http.Request, projection inventory.ScanTaskProjection) (string, error) {
	targetKey := strings.TrimSpace(request.URL.Query().Get("target_key"))
	if targetKey == "" {
		return "", nil
	}
	for _, target := range projection.TargetProgress {
		if target.Key == targetKey {
			return targetKey, nil
		}
	}
	return "", fmt.Errorf("scan task %q has no target %q", projection.ID, targetKey)
}

// scanProjectionChanged ignores a running duration that only grew with the
// clock: the console ticks it locally, and status changes and the terminal
// "end" event still carry the current value.
func scanProjectionChanged(previous, current inventory.ScanTaskProjection) bool {
	if (previous.DurationMS == nil) == (current.DurationMS == nil) {
		previous.DurationMS, current.DurationMS = nil, nil
	}
	return !reflect.DeepEqual(previous, current)
}

func writeScanEvent(response http.ResponseWriter, event, id string, value any) {
	payload, _ := json.Marshal(value)
	if id != "" {
		_, _ = fmt.Fprintf(response, "id: %s\n", id)
	}
	_, _ = fmt.Fprintf(response, "event: %s\ndata: %s\n\n", event, payload)
}

func scanEventCursor(createdAt time.Time, id string) string {
	return strconv.FormatInt(createdAt.UnixNano(), 10) + ":" + id
}

func parseScanEventCursor(value string) (time.Time, string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, "", nil
	}
	parts := strings.SplitN(value, ":", 2)
	if len(parts) != 2 || parts[1] == "" {
		return time.Time{}, "", errors.New("invalid Last-Event-ID")
	}
	nanos, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return time.Time{}, "", errors.New("invalid Last-Event-ID")
	}
	return time.Unix(0, nanos).UTC(), parts[1], nil
}

func isTerminalScanStatus(status asset.ScanStatus) bool {
	switch status {
	case asset.ScanSucceeded, asset.ScanPartial, asset.ScanFailed, asset.ScanCanceled:
		return true
	default:
		return false
	}
}

func writeScanError(response http.ResponseWriter, err error) {
	writeScanRequestError(response, http.StatusConflict, err)
}

func scanRequestStatus(err error) int {
	var requestError *inventory.ScanRequestError
	if errors.As(err, &requestError) {
		if requestError.Code == "scan.connection_changed" || requestError.Code == "scan.connection_not_validated" {
			return http.StatusConflict
		}
		return http.StatusBadRequest
	}
	if errors.Is(err, asset.ErrConnectionNotValidated) || errors.Is(err, persistence.ErrConflict) {
		return http.StatusConflict
	}
	return http.StatusInternalServerError
}

func writeScanRequestError(response http.ResponseWriter, status int, err error) {
	var requestError *inventory.ScanRequestError
	if errors.As(err, &requestError) {
		writeAPIError(response, status, APIError{Code: requestError.Code, Message: requestError.Message, Details: requestError.Details})
		return
	}
	if errors.Is(err, asset.ErrConnectionNotValidated) {
		writeAPIError(response, http.StatusConflict, APIError{Code: "connection_not_validated", Message: err.Error()})
		return
	}
	if writeProviderNetworkError(response, err) {
		return
	}
	repositoryError(response, err)
}

func writeProviderNetworkError(response http.ResponseWriter, err error) bool {
	var providerError *contracts.ProviderCallError
	if !errors.As(err, &providerError) {
		return false
	}
	writeAPIError(response, http.StatusBadGateway, APIError{
		Code: "provider.network_query_failed", Message: providerError.Provider.Message, RequestID: providerError.Provider.RequestID,
	})
	return true
}
