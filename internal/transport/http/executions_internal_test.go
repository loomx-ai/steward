package httptransport

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/core/plan"
)

func TestWriteExecutionErrorMapsInvalidatedCleanupToConflict(t *testing.T) {
	t.Parallel()

	response := httptest.NewRecorder()
	writeExecutionError(response, fmt.Errorf("%w: resource is already absent", plan.ErrCleanupTaskInvalidated))
	if response.Code != http.StatusConflict ||
		!strings.Contains(response.Body.String(), `"code":"cleanup.invalidated"`) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestProjectExecutionTimingOnlyIncludesMatchingExecutionJobs(t *testing.T) {
	base := time.Date(2026, 8, 4, 8, 0, 0, 0, time.UTC)
	firstEnd := base.Add(8 * time.Second)
	otherEnd := base.Add(30 * time.Second)
	attempt := execution.ExecutionAttempt{
		ID: "execution-1", CleanupTaskID: "cleanup-1", CreatedAt: base,
	}
	projection := projectExecutionTiming(attempt, []execution.Job{
		{
			Payload:   map[string]any{"execution_id": "execution-1"},
			UpdatedAt: firstEnd,
			RunIntervals: []execution.JobRunInterval{{
				StartedAt: base, FinishedAt: &firstEnd,
			}},
		},
		{
			Payload:   map[string]any{"execution_id": "execution-2"},
			UpdatedAt: otherEnd,
			RunIntervals: []execution.JobRunInterval{{
				StartedAt: base, FinishedAt: &otherEnd,
			}},
		},
	}, otherEnd)

	if projection.DurationMS == nil || *projection.DurationMS != 8_000 {
		t.Fatalf("duration_ms = %v, want 8000", projection.DurationMS)
	}
	if !projection.UpdatedAt.Equal(firstEnd) {
		t.Fatalf("updated_at = %s, want %s", projection.UpdatedAt, firstEnd)
	}
}
