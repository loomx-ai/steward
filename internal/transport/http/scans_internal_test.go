package httptransport

import (
	"bytes"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/app/inventory"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/persistence"
)

func TestScanRequestStatusTreatsConnectionChangeAsConflict(t *testing.T) {
	err := scanRequestErrorForTest("scan.connection_changed")
	if status := scanRequestStatus(err); status != http.StatusConflict {
		t.Fatalf("scanRequestStatus() = %d, want %d", status, http.StatusConflict)
	}
}

func TestWriteScanRequestErrorPreservesConnectionNotValidatedCode(t *testing.T) {
	response := httptest.NewRecorder()
	writeScanRequestError(response, scanRequestStatus(asset.ErrConnectionNotValidated), asset.ErrConnectionNotValidated)
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), `"code":"connection_not_validated"`) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestWriteScanRequestErrorHidesAndLogsInternalFailure(t *testing.T) {
	var logs bytes.Buffer
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previousLogger) })

	response := httptest.NewRecorder()
	response.Header().Set(requestIDHeader, "request-123")
	err := errors.New("table scan_tasks has no column named resource_count")
	writeScanRequestError(response, scanRequestStatus(err), err)

	body := response.Body.String()
	if response.Code != http.StatusInternalServerError ||
		!strings.Contains(body, `"code":"internal_error"`) ||
		!strings.Contains(body, `"message":"an internal server error occurred"`) ||
		strings.Contains(body, "scan_tasks") ||
		strings.Contains(body, "resource_count") {
		t.Fatalf("status=%d body=%s", response.Code, body)
	}
	if output := logs.String(); !strings.Contains(output, err.Error()) ||
		!strings.Contains(output, `"request_id":"request-123"`) {
		t.Fatalf("internal error log = %s", output)
	}
}

func TestRepositoryErrorKeepsInvalidCursorAsBadRequest(t *testing.T) {
	response := httptest.NewRecorder()
	repositoryError(response, persistence.ErrInvalidCursor)
	if response.Code != http.StatusBadRequest ||
		!strings.Contains(response.Body.String(), `"code":"request.invalid"`) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func scanRequestErrorForTest(code string) error {
	return &inventory.ScanRequestError{Code: code, Message: "test"}
}

func TestScanProjectionChangedDetectsEveryVisibleProjectionChange(t *testing.T) {
	base := inventory.ScanTaskProjection{
		ScanTask:       asset.ScanTask{Status: asset.ScanRunning},
		Progress:       inventory.ScanOverallProgress{Completed: 1, Total: 2, ResourceCount: 3},
		AllowedActions: []string{"pause", "cancel"},
		TargetProgress: []inventory.ScanTargetProgress{{Key: "region:cn-hangzhou", Status: asset.ScanRunning, ResourceCount: 3}},
	}
	if scanProjectionChanged(base, base) {
		t.Fatal("identical projections must not be reported as changed")
	}

	tests := map[string]inventory.ScanTaskProjection{
		"task status": {
			ScanTask:       asset.ScanTask{Status: asset.ScanPausing},
			Progress:       base.Progress,
			AllowedActions: base.AllowedActions,
			TargetProgress: base.TargetProgress,
		},
		"resource count": {
			ScanTask:       base.ScanTask,
			Progress:       inventory.ScanOverallProgress{Completed: 1, Total: 2, ResourceCount: 4},
			AllowedActions: base.AllowedActions,
			TargetProgress: base.TargetProgress,
		},
		"allowed actions": {
			ScanTask:       base.ScanTask,
			Progress:       base.Progress,
			AllowedActions: []string{"resume", "cancel"},
			TargetProgress: base.TargetProgress,
		},
		"regional status": {
			ScanTask:       base.ScanTask,
			Progress:       base.Progress,
			AllowedActions: base.AllowedActions,
			TargetProgress: []inventory.ScanTargetProgress{{Key: "region:cn-hangzhou", Status: asset.ScanSucceeded, ResourceCount: 3}},
		},
	}
	for name, current := range tests {
		t.Run(name, func(t *testing.T) {
			if !scanProjectionChanged(base, current) {
				t.Fatalf("change %q was not detected", name)
			}
		})
	}
}
