package store

import (
	"context"
	"testing"
	"time"

	"github.com/prodesire/cloud-steward/internal/domain"
)

func TestMemoryStoreClaimsPendingScanJobAndMarksSuccess(t *testing.T) {
	ctx := context.Background()
	repo := NewMemoryStore()
	account := domain.Account{Name: "dev", Provider: domain.ProviderAliCloud}
	account, err := repo.UpsertAccount(ctx, account)
	if err != nil {
		t.Fatalf("UpsertAccount() error = %v", err)
	}

	job, err := repo.CreateScanJob(ctx, domain.ScanJob{
		AccountID:   account.ID,
		AccountName: account.Name,
		Provider:    domain.ProviderAliCloud,
		Mode:        domain.ScanModeDemo,
		Regions:     []string{"cn-hangzhou"},
	})
	if err != nil {
		t.Fatalf("CreateScanJob() error = %v", err)
	}
	if job.Status != domain.ScanJobPending {
		t.Fatalf("status = %s, want pending", job.Status)
	}

	claimed, ok, err := repo.ClaimNextPendingScanJob(ctx)
	if err != nil {
		t.Fatalf("ClaimNextPendingScanJob() error = %v", err)
	}
	if !ok {
		t.Fatal("ClaimNextPendingScanJob() ok = false, want true")
	}
	if claimed.ID != job.ID {
		t.Fatalf("claimed id = %s, want %s", claimed.ID, job.ID)
	}
	if claimed.Status != domain.ScanJobRunning {
		t.Fatalf("claimed status = %s, want running", claimed.Status)
	}
	if claimed.StartedAt == nil {
		t.Fatal("claimed StartedAt = nil, want timestamp")
	}

	if err := repo.MarkScanJobSucceeded(ctx, job.ID, 7); err != nil {
		t.Fatalf("MarkScanJobSucceeded() error = %v", err)
	}
	got, err := repo.GetScanJob(ctx, job.ID)
	if err != nil {
		t.Fatalf("GetScanJob() error = %v", err)
	}
	if got.Status != domain.ScanJobSucceeded {
		t.Fatalf("status = %s, want succeeded", got.Status)
	}
	if got.ResourceCount != 7 {
		t.Fatalf("resource count = %d, want 7", got.ResourceCount)
	}
	if got.FinishedAt == nil {
		t.Fatal("FinishedAt = nil, want timestamp")
	}
}

func TestMemoryStoreMarksScanJobFailure(t *testing.T) {
	ctx := context.Background()
	repo := NewMemoryStore()
	job, err := repo.CreateScanJob(ctx, domain.ScanJob{
		AccountID:   "acct-1",
		AccountName: "dev",
		Provider:    domain.ProviderAliCloud,
		Mode:        domain.ScanModeAliCloud,
		Regions:     []string{"cn-hangzhou"},
	})
	if err != nil {
		t.Fatalf("CreateScanJob() error = %v", err)
	}
	if _, _, err := repo.ClaimNextPendingScanJob(ctx); err != nil {
		t.Fatalf("ClaimNextPendingScanJob() error = %v", err)
	}

	if err := repo.MarkScanJobFailed(ctx, job.ID, "missing credentials"); err != nil {
		t.Fatalf("MarkScanJobFailed() error = %v", err)
	}
	got, err := repo.GetScanJob(ctx, job.ID)
	if err != nil {
		t.Fatalf("GetScanJob() error = %v", err)
	}
	if got.Status != domain.ScanJobFailed {
		t.Fatalf("status = %s, want failed", got.Status)
	}
	if got.FailureReason != "missing credentials" {
		t.Fatalf("failure reason = %q, want missing credentials", got.FailureReason)
	}
}

func TestMemoryStoreUpsertsAndFiltersResources(t *testing.T) {
	ctx := context.Background()
	repo := NewMemoryStore()
	created := time.Date(2026, 7, 1, 9, 0, 0, 0, time.UTC)
	resources := []domain.Resource{
		{
			Provider:   domain.ProviderAliCloud,
			AccountID:  "acct-1",
			Region:     "cn-hangzhou",
			Type:       domain.ResourceTypeECSInstance,
			NativeID:   "i-1",
			Name:       "api-dev",
			State:      "Running",
			Tags:       map[string]string{"team": "platform", "env": "dev"},
			CreatedAt:  created,
			LastSeenAt: created,
			Raw:        map[string]any{"InstanceId": "i-1"},
		},
		{
			Provider:   domain.ProviderAliCloud,
			AccountID:  "acct-1",
			Region:     "cn-hangzhou",
			Type:       domain.ResourceTypeDisk,
			NativeID:   "d-1",
			Name:       "orphan-disk",
			State:      "Available",
			Tags:       map[string]string{"team": "platform", "env": "prod"},
			CreatedAt:  created,
			LastSeenAt: created,
			Raw:        map[string]any{"DiskId": "d-1"},
		},
	}

	if err := repo.UpsertResources(ctx, "scan-1", resources); err != nil {
		t.Fatalf("UpsertResources() error = %v", err)
	}
	got, err := repo.ListResources(ctx, domain.ResourceFilter{Type: domain.ResourceTypeDisk})
	if err != nil {
		t.Fatalf("ListResources() error = %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("disk resource count = %d, want 1", len(got))
	}
	if !got[0].Protected {
		t.Fatal("prod disk Protected = false, want true")
	}
	if got[0].Ownership.Team != "platform" {
		t.Fatalf("team = %q, want platform", got[0].Ownership.Team)
	}

	matches, err := repo.ListResources(ctx, domain.ResourceFilter{Query: "api"})
	if err != nil {
		t.Fatalf("ListResources(query) error = %v", err)
	}
	if len(matches) != 1 || matches[0].NativeID != "i-1" {
		t.Fatalf("query matches = %#v, want only i-1", matches)
	}

	detail, err := repo.GetResource(ctx, got[0].ID)
	if err != nil {
		t.Fatalf("GetResource() error = %v", err)
	}
	if detail.Raw["DiskId"] != "d-1" {
		t.Fatalf("raw DiskId = %#v, want d-1", detail.Raw["DiskId"])
	}
}
