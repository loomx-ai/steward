package cleanup_test

import (
	"context"
	"testing"
	"time"

	"github.com/loomx-ai/steward/internal/app/cleanup"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/core/requestmeta"
)

func TestWorkerRestoresInitiatingRequestIDForJobHandler(t *testing.T) {
	now := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	jobs := &fakeJobs{job: execution.Job{
		ID: "job-request-id", Type: execution.JobExecute, Status: execution.JobPending,
		Payload: map[string]any{"request_id": "application-request-id"},
		RunAt:   now, CreatedAt: now, UpdatedAt: now,
	}}
	var received string
	worker := cleanup.NewWorker(jobs, map[execution.JobType]cleanup.Handler{
		execution.JobExecute: cleanup.HandlerFunc(func(ctx context.Context, _ execution.Job) error {
			received = requestmeta.RequestID(ctx)
			return nil
		}),
	}, cleanup.WorkerOptions{
		WorkerID: "worker-request-id", LeaseDuration: time.Minute,
		RenewInterval: time.Hour, Now: func() time.Time { return now },
	})

	processed, err := worker.ProcessOne(context.Background())
	if err != nil || !processed || received != "application-request-id" {
		t.Fatalf("processed=%v request_id=%q err=%v", processed, received, err)
	}
}
