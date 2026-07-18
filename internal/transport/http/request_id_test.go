package httptransport_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/loomx-ai/steward/internal/core/execution"
)

const requestIDHeader = "X-Request-ID"

var uuidV4Pattern = regexp.MustCompile(
	`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`,
)

func TestSuccessfulAPIResponseIncludesServerGeneratedRequestID(t *testing.T) {
	_, router := terminalRouter(t)
	request := httptest.NewRequest(http.MethodGet, "/api/providers/catalog", nil)
	request.Header.Set("Authorization", "Bearer viewer-token")
	request.Header.Set(requestIDHeader, "client-controlled")
	response := httptest.NewRecorder()

	router.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	assertUUIDV4(t, response.Header().Get(requestIDHeader))
	if response.Header().Get(requestIDHeader) == "client-controlled" {
		t.Fatal("server reused the client-controlled request ID")
	}
	var body []json.RawMessage
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("successful response is no longer a top-level array: %v body=%s", err, response.Body.String())
	}
}

func TestAPIErrorResponsesIncludeMatchingRequestID(t *testing.T) {
	tests := []struct {
		name       string
		path       string
		token      string
		wantStatus int
	}{
		{name: "authentication error", path: "/api/providers/catalog", wantStatus: http.StatusUnauthorized},
		{name: "business error", path: "/api/assets?connection_id=missing", token: "viewer-token", wantStatus: http.StatusNotFound},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, router := terminalRouter(t)
			request := httptest.NewRequest(http.MethodGet, test.path, nil)
			request.Header.Set(requestIDHeader, "client-controlled")
			if test.token != "" {
				request.Header.Set("Authorization", "Bearer "+test.token)
			}
			response := httptest.NewRecorder()

			router.ServeHTTP(response, request)

			if response.Code != test.wantStatus {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			headerID := response.Header().Get(requestIDHeader)
			assertUUIDV4(t, headerID)
			if headerID == "client-controlled" {
				t.Fatal("server reused the client-controlled request ID")
			}
			var body struct {
				Error struct {
					RequestID string `json:"request_id"`
				} `json:"error"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode error response: %v body=%s", err, response.Body.String())
			}
			if body.Error.RequestID != headerID {
				t.Fatalf("body request_id=%q header=%q", body.Error.RequestID, headerID)
			}
		})
	}
}

func TestConsecutiveAPIRequestsUseDifferentRequestIDs(t *testing.T) {
	_, router := terminalRouter(t)
	requestIDs := make(map[string]struct{}, 2)
	for range 2 {
		request := httptest.NewRequest(http.MethodGet, "/api/providers", nil)
		request.Header.Set("Authorization", "Bearer viewer-token")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
		requestID := response.Header().Get(requestIDHeader)
		assertUUIDV4(t, requestID)
		requestIDs[requestID] = struct{}{}
	}
	if len(requestIDs) != 2 {
		t.Fatalf("consecutive requests reused an ID: %+v", requestIDs)
	}
}

func TestNoContentAPIResponseIncludesRequestID(t *testing.T) {
	_, router := terminalRouter(t)
	request := httptest.NewRequest(
		http.MethodDelete,
		"/api/connections/connection-a",
		bytes.NewBufferString(`{"confirmation":"test account"}`),
	)
	request.Header.Set("Authorization", "Bearer admin-token")
	response := httptest.NewRecorder()

	router.ServeHTTP(response, request)

	if response.Code != http.StatusNoContent || response.Body.Len() != 0 {
		t.Fatalf("status=%d body=%q", response.Code, response.Body.String())
	}
	assertUUIDV4(t, response.Header().Get(requestIDHeader))
}

func TestSSEAPIResponseIncludesRequestID(t *testing.T) {
	repositories, router := terminalRouter(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)
	job := execution.Job{
		ID: "job-request-id", ConnectionID: "connection-a", Type: execution.JobScan,
		Status: execution.JobSucceeded, RunAt: now, CreatedAt: now, UpdatedAt: now, FinishedAt: &now,
	}
	if err := repositories.Jobs().Enqueue(ctx, job); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Jobs().AppendLog(ctx, execution.JobLog{
		ID: "log-request-id", JobID: job.ID, Sequence: 1, Kind: execution.JobLogText, Level: "info", Message: "done", CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/api/jobs/job-request-id/events?connection_id=connection-a", nil)
	request.Header.Set("Authorization", "Bearer viewer-token")
	response := httptest.NewRecorder()

	router.ServeHTTP(response, request)

	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "event: log") {
		t.Fatalf("status=%d body=%q", response.Code, response.Body.String())
	}
	assertUUIDV4(t, response.Header().Get(requestIDHeader))
}

func TestUnknownAPIRouteIncludesRequestID(t *testing.T) {
	_, router := terminalRouter(t)
	request := httptest.NewRequest(http.MethodGet, "/api/does-not-exist", nil)
	request.Header.Set("Authorization", "Bearer viewer-token")
	response := httptest.NewRecorder()

	router.ServeHTTP(response, request)

	if response.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	assertUUIDV4(t, response.Header().Get(requestIDHeader))
}

func TestNonAPIResponseDoesNotIncludeRequestID(t *testing.T) {
	_, router := terminalRouter(t)
	response := httptest.NewRecorder()

	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))

	if value := response.Header().Get(requestIDHeader); value != "" {
		t.Fatalf("non-API response request ID=%q", value)
	}
}

func assertUUIDV4(t *testing.T, value string) {
	t.Helper()
	if !uuidV4Pattern.MatchString(value) {
		t.Fatalf("request ID %q is not a lowercase UUID v4", value)
	}
}
