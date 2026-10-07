package contract

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/core/workspace"
	"github.com/loomx-ai/steward/internal/persistence"
)

// RunWorkspaceIsolation checks that repositories in a shared pool keep each
// workspace's rows to itself. The factory's repositories must require a
// workspace in every context.
func RunWorkspaceIsolation(t *testing.T, factory Factory) {
	t.Helper()
	repositories := factory(t)
	if strict, ok := repositories.(interface{ RequireWorkspace() }); ok {
		strict.RequireWorkspace()
	}
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	first := workspace.With(context.Background(), "ws_first")
	second := workspace.With(context.Background(), "ws_second")

	connection := asset.CloudConnection{ID: "con-shared", Name: "first", Provider: asset.ProviderAliCloud, Partition: "public", Principal: "first", CreatedAt: now, UpdatedAt: now}
	if err := repositories.Connections().PutConnection(first, connection); err != nil {
		t.Fatal(err)
	}
	job := execution.Job{ID: "job-first", ConnectionID: connection.ID, IdempotencyKey: "same-key", Type: execution.JobScan, Status: execution.JobPending, RunAt: now, CreatedAt: now, UpdatedAt: now}
	if err := repositories.Jobs().Enqueue(first, job); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Schedules().PutSetting(first, "shared-setting", `"first"`, now); err != nil {
		t.Fatal(err)
	}

	t.Run("another workspace cannot read, list or change the rows", func(t *testing.T) {
		if _, err := repositories.Connections().GetConnection(second, connection.ID); !errors.Is(err, persistence.ErrNotFound) {
			t.Fatalf("second workspace read the connection: %v", err)
		}
		if page, err := repositories.Connections().ListConnections(second, persistence.ListOptions{Limit: 10}); err != nil || len(page.Items) != 0 {
			t.Fatalf("second workspace listed %#v, %v", page.Items, err)
		}
		if _, err := repositories.Jobs().GetJobByIdempotencyKey(second, "same-key"); !errors.Is(err, persistence.ErrNotFound) {
			t.Fatalf("second workspace found the job by key: %v", err)
		}
		changed := job
		changed.Status = execution.JobFailed
		if err := repositories.Jobs().UpdateJob(second, changed); err == nil {
			t.Fatal("second workspace updated the job")
		}
		// An upsert sharing the ID must not overwrite the first workspace's row.
		takeover := connection
		takeover.Name = "second"
		_ = repositories.Connections().PutConnection(second, takeover)
		if stored, err := repositories.Connections().GetConnection(first, connection.ID); err != nil || stored.Name != "first" {
			t.Fatalf("first workspace connection = %#v, %v", stored, err)
		}
	})

	t.Run("keys are per workspace", func(t *testing.T) {
		if _, err := repositories.Schedules().GetSetting(second, "shared-setting"); !errors.Is(err, persistence.ErrNotFound) {
			t.Fatalf("second workspace read the setting: %v", err)
		}
		if err := repositories.Schedules().PutSetting(second, "shared-setting", `"second"`, now); err != nil {
			t.Fatal(err)
		}
		if value, err := repositories.Schedules().GetSetting(first, "shared-setting"); err != nil || value != `"first"` {
			t.Fatalf("first workspace setting = %q, %v", value, err)
		}
		other := job
		other.ID = "job-second"
		if err := repositories.Jobs().Enqueue(second, other); err != nil {
			t.Fatalf("second workspace could not reuse the idempotency key: %v", err)
		}
	})

	t.Run("access without a workspace fails", func(t *testing.T) {
		if _, err := repositories.Connections().GetConnection(context.Background(), connection.ID); err == nil {
			t.Fatal("read without a workspace succeeded")
		}
		if page, err := repositories.Connections().ListConnections(context.Background(), persistence.ListOptions{Limit: 10}); err == nil {
			t.Fatalf("list without a workspace returned %#v", page.Items)
		}
	})

	t.Run("claims across workspaces name the job's workspace", func(t *testing.T) {
		claimed := map[workspace.ID]execution.JobID{}
		for range 2 {
			job, err := repositories.Jobs().ClaimNext(workspace.AcrossAll(context.Background()), "worker", now, time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			claimed[job.WorkspaceID] = job.ID
		}
		if claimed["ws_first"] != "job-first" || claimed["ws_second"] != "job-second" {
			t.Fatalf("claimed = %#v", claimed)
		}
	})

	t.Run("claims go round workspaces however deep one backlog is", func(t *testing.T) {
		for name, count := range map[workspace.ID]int{"ws_deep": 10, "ws_one": 1, "ws_two": 1} {
			ctx := workspace.With(context.Background(), name)
			for index := range count {
				id := execution.JobID(fmt.Sprintf("job-%s-%d", name, index))
				// Deep backlog jobs are the oldest, so oldest-first alone would
				// serve them all before the others.
				runAt := now.Add(-time.Hour + time.Duration(index)*time.Second)
				if name != "ws_deep" {
					runAt = now
				}
				if err := repositories.Jobs().Enqueue(ctx, execution.Job{ID: id, Type: execution.JobGraph, Status: execution.JobPending, RunAt: runAt, CreatedAt: now, UpdatedAt: now}); err != nil {
					t.Fatal(err)
				}
			}
		}
		served := map[workspace.ID]bool{}
		for range 3 {
			job, err := repositories.Jobs().ClaimNext(workspace.AcrossAll(context.Background()), "worker", now, time.Minute, execution.JobGraph)
			if err != nil {
				t.Fatal(err)
			}
			served[job.WorkspaceID] = true
		}
		if len(served) != 3 {
			t.Fatalf("three claims served only %v", served)
		}
	})
}
