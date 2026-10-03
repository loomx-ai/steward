package events_test

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/transport/events"
)

func TestJobEventStreamResumesAfterLastEventID(t *testing.T) {
	store := terminalEventStore{
		job: execution.Job{ID: "job-1", Status: execution.JobSucceeded},
		logs: []execution.JobLog{
			{ID: "log-1", JobID: "job-1", Sequence: 1, Kind: execution.JobLogText, Level: "info", Message: "first"},
			{ID: "log-2", JobID: "job-1", Sequence: 2, Kind: execution.JobLogText, Level: "info", Message: "second"},
		},
	}
	handler := events.NewJobEventHandler(store, time.Millisecond)
	request := httptest.NewRequest("GET", "/jobs/job-1/events", nil)
	request.SetPathValue("id", "job-1")
	request.Header.Set("Last-Event-ID", "1")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	body := response.Body.String()
	if response.Code != 200 || !strings.Contains(body, "id: 2\n") || !strings.Contains(body, `"message":"second"`) || strings.Contains(body, `"message":"first"`) {
		t.Fatalf("status=%d body=%q", response.Code, body)
	}
}

func TestJobEventStreamEndsForCanceledJobs(t *testing.T) {
	handler := events.NewJobEventHandler(terminalEventStore{job: execution.Job{ID: "job-1", Status: execution.JobCanceled}}, time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	request := httptest.NewRequestWithContext(ctx, "GET", "/jobs/job-1/events", nil)
	request.SetPathValue("id", "job-1")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if ctx.Err() != nil || response.Code != 200 {
		t.Fatalf("canceled job stream kept polling: status=%d err=%v", response.Code, ctx.Err())
	}
}

type terminalEventStore struct {
	job  execution.Job
	logs []execution.JobLog
}

func (s terminalEventStore) GetJob(context.Context, execution.JobID) (execution.Job, error) {
	return s.job, nil
}

func (s terminalEventStore) ListLogs(_ context.Context, _ execution.JobID, after int64, limit int) ([]execution.JobLog, error) {
	result := make([]execution.JobLog, 0, len(s.logs))
	for _, log := range s.logs {
		if log.Sequence > after && len(result) < limit {
			result = append(result, log)
		}
	}
	return result, nil
}
