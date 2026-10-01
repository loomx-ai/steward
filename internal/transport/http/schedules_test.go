package httptransport_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/schedule"
)

func TestScheduleRoutesCreateRunAndReport(t *testing.T) {
	repositories, router := terminalRouter(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	if err := repositories.Regions().PutRegion(ctx, asset.ConnectionRegion{ID: "region-schedule", ConnectionID: "connection-a", RegionID: "cn-hangzhou", Origin: asset.RegionOriginAPI, Lifecycle: asset.RegionActive, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	call := func(method, path, token, body string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(method, path, bytes.NewBufferString(body))
		request.Header.Set("Authorization", "Bearer "+token)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		return response
	}
	body := `{"name":"杭州","scope":{"scope_mode":"selected_regions","region_ids":["cn-hangzhou"]},"frequency":{"kind":"daily","time":"03:12","timezone":"Asia/Shanghai"}}`

	if response := call(http.MethodPost, "/api/scan-schedules?connection_id=connection-a", "viewer-token", body); response.Code != http.StatusForbidden {
		t.Fatalf("viewer create status=%d", response.Code)
	}
	response := call(http.MethodPost, "/api/scan-schedules?connection_id=connection-a", "operator-token", body)
	var created schedule.ScanSchedule
	if err := json.Unmarshal(response.Body.Bytes(), &created); err != nil || response.Code != http.StatusCreated || created.NextRunAt == nil || created.CreatedBy != "alice" {
		t.Fatalf("create status=%d body=%s err=%v", response.Code, response.Body.String(), err)
	}
	bad := `{"scope":{"scope_mode":"all_active_regions"},"frequency":{"kind":"cron","cron":"* * * * *","timezone":"UTC"}}`
	response = call(http.MethodPost, "/api/scan-schedules?connection_id=connection-a", "operator-token", bad)
	if response.Code != http.StatusBadRequest || !bytes.Contains(response.Body.Bytes(), []byte("schedule.interval_too_short")) {
		t.Fatalf("too frequent status=%d body=%s", response.Code, response.Body.String())
	}
	response = call(http.MethodPost, "/api/scan-schedules/preview?connection_id=connection-a", "viewer-token", `{"frequency":{"kind":"hourly","every_hours":6,"time":"00:00","timezone":"UTC"}}`)
	var preview struct {
		NextRuns           []time.Time `json:"next_runs"`
		MinIntervalSeconds int64       `json:"min_interval_seconds"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &preview); err != nil || len(preview.NextRuns) != 3 || !preview.NextRuns[0].Equal(time.Date(2026, 7, 13, 18, 0, 0, 0, time.UTC)) || preview.MinIntervalSeconds != 3600 {
		t.Fatalf("preview status=%d body=%s err=%v", response.Code, response.Body.String(), err)
	}

	response = call(http.MethodPost, "/api/scan-schedules/"+string(created.ID)+"/run?connection_id=connection-a", "operator-token", "")
	var run struct {
		schedule.Run
		Scan *struct {
			ID           string `json:"id"`
			ScheduleID   string `json:"schedule_id"`
			ScheduleName string `json:"schedule_name"`
		} `json:"scan"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &run); err != nil || response.Code != http.StatusAccepted || run.Outcome != schedule.OutcomeStarted || run.Trigger != schedule.TriggerManual || run.Actor != "alice" || run.Scan == nil || run.Scan.ScheduleID != string(created.ID) {
		t.Fatalf("run status=%d body=%s err=%v", response.Code, response.Body.String(), err)
	}

	response = call(http.MethodGet, "/api/scans?connection_id=connection-a&source=scheduled", "viewer-token", "")
	if !bytes.Contains(response.Body.Bytes(), []byte(`"schedule_name":"杭州"`)) {
		t.Fatalf("scheduled scans body=%s", response.Body.String())
	}
	response = call(http.MethodGet, "/api/scans?connection_id=connection-a&source=manual", "viewer-token", "")
	if !bytes.Contains(response.Body.Bytes(), []byte(`"items":[]`)) {
		t.Fatalf("manual scans body=%s", response.Body.String())
	}
	response = call(http.MethodGet, "/api/scan-schedules/"+string(created.ID)+"/runs?connection_id=connection-a", "viewer-token", "")
	if response.Code != http.StatusOK || !bytes.Contains(response.Body.Bytes(), []byte(`"trigger":"manual"`)) {
		t.Fatalf("runs status=%d body=%s", response.Code, response.Body.String())
	}
	response = call(http.MethodGet, "/api/scan-schedules?connection_id=connection-a", "viewer-token", "")
	if response.Code != http.StatusOK || !bytes.Contains(response.Body.Bytes(), []byte(`"last_run"`)) {
		t.Fatalf("list status=%d body=%s", response.Code, response.Body.String())
	}

	response = call(http.MethodPost, "/api/scan-schedules/"+string(created.ID)+"/disable?connection_id=connection-a", "operator-token", "")
	if response.Code != http.StatusOK || !bytes.Contains(response.Body.Bytes(), []byte(`"enabled":false`)) {
		t.Fatalf("disable status=%d body=%s", response.Code, response.Body.String())
	}
	response = call(http.MethodGet, "/api/scan-schedule-overview", "viewer-token", "")
	if response.Code != http.StatusOK || !bytes.Contains(response.Body.Bytes(), []byte(`"connection_id":"connection-a"`)) {
		t.Fatalf("overview status=%d body=%s", response.Code, response.Body.String())
	}

	if response := call(http.MethodPut, "/api/scan-schedule-settings", "operator-token", `{"default_schedule_enabled":true,"retention_days":30}`); response.Code != http.StatusForbidden {
		t.Fatalf("operator settings status=%d", response.Code)
	}
	response = call(http.MethodPut, "/api/scan-schedule-settings", "admin-token", `{"default_schedule_enabled":true,"retention_days":7,"default_timezone":"Asia/Shanghai"}`)
	if response.Code != http.StatusOK || !bytes.Contains(response.Body.Bytes(), []byte(`"retention_days":7`)) {
		t.Fatalf("settings status=%d body=%s", response.Code, response.Body.String())
	}
	if response := call(http.MethodDelete, "/api/scan-schedules/"+string(created.ID)+"?connection_id=connection-a", "operator-token", ""); response.Code != http.StatusNoContent {
		t.Fatalf("delete status=%d", response.Code)
	}
	if response := call(http.MethodGet, "/api/scan-schedules/"+string(created.ID)+"?connection_id=connection-a", "viewer-token", ""); response.Code != http.StatusNotFound {
		t.Fatalf("deleted get status=%d", response.Code)
	}
}

func TestNotificationChannelRoutesAreAdminOnlyAndHideAddresses(t *testing.T) {
	_, router := terminalRouter(t)
	call := func(method, path, token, body string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(method, path, bytes.NewBufferString(body))
		request.Header.Set("Authorization", "Bearer "+token)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		return response
	}
	body := `{"name":"ops","type":"slack","language":"en","url":"https://hooks.slack.com/services/T000/B000/secretvalue","events":["scan_failed"]}`
	if response := call(http.MethodPost, "/api/notification-channels", "operator-token", body); response.Code != http.StatusForbidden {
		t.Fatalf("operator create status=%d", response.Code)
	}
	response := call(http.MethodPost, "/api/notification-channels", "admin-token", body)
	if response.Code != http.StatusCreated || bytes.Contains(response.Body.Bytes(), []byte("secretvalue")) || !bytes.Contains(response.Body.Bytes(), []byte(`"target":"https://hooks.slack.com/…alue"`)) {
		t.Fatalf("create status=%d body=%s", response.Code, response.Body.String())
	}
	response = call(http.MethodGet, "/api/notification-channels", "viewer-token", "")
	if response.Code != http.StatusOK || bytes.Contains(response.Body.Bytes(), []byte("secretvalue")) || bytes.Contains(response.Body.Bytes(), []byte("sealed")) {
		t.Fatalf("list status=%d body=%s", response.Code, response.Body.String())
	}
	if response := call(http.MethodPost, "/api/notification-channels", "admin-token", `{"name":"x","type":"pager","url":"https://example.com","events":["scan_failed"]}`); response.Code != http.StatusBadRequest {
		t.Fatalf("invalid type status=%d", response.Code)
	}
}
