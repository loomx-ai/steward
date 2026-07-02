package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/prodesire/cloud-steward/internal/domain"
)

func TestSQLiteStorePersistsScanJobsAndResources(t *testing.T) {
	ctx := context.Background()
	db, err := OpenSQLite(filepath.Join(t.TempDir(), "cloud-steward.db"))
	if err != nil {
		t.Fatalf("OpenSQLite() error = %v", err)
	}
	if err := AutoMigrate(db); err != nil {
		t.Fatalf("AutoMigrate() error = %v", err)
	}
	repo := NewSQLStore(db)

	account, err := repo.UpsertAccount(ctx, domain.Account{Name: "sqlite-local", Provider: domain.ProviderDemo})
	if err != nil {
		t.Fatalf("UpsertAccount() error = %v", err)
	}
	job, err := repo.CreateScanJob(ctx, domain.ScanJob{
		AccountID:   account.ID,
		AccountName: account.Name,
		Provider:    domain.ProviderDemo,
		Mode:        domain.ScanModeDemo,
		Regions:     []string{"cn-hangzhou"},
	})
	if err != nil {
		t.Fatalf("CreateScanJob() error = %v", err)
	}
	claimed, ok, err := repo.ClaimNextPendingScanJob(ctx)
	if err != nil {
		t.Fatalf("ClaimNextPendingScanJob() error = %v", err)
	}
	if !ok || claimed.ID != job.ID {
		t.Fatalf("claimed = %#v ok=%v, want job %s", claimed, ok, job.ID)
	}

	now := time.Date(2026, 7, 2, 17, 0, 0, 0, time.UTC)
	resources := []domain.Resource{{
		Provider:   domain.ProviderDemo,
		AccountID:  account.ID,
		Region:     "cn-hangzhou",
		Type:       domain.ResourceTypeVPC,
		NativeID:   "vpc-sqlite",
		Name:       "sqlite-vpc",
		State:      "Available",
		Tags:       map[string]string{"team": "platform", "env": "dev"},
		CreatedAt:  now,
		LastSeenAt: now,
		Raw:        map[string]any{"VpcId": "vpc-sqlite"},
	}}
	if err := repo.UpsertResources(ctx, job.ID, resources); err != nil {
		t.Fatalf("UpsertResources() error = %v", err)
	}
	if err := repo.MarkScanJobSucceeded(ctx, job.ID, len(resources)); err != nil {
		t.Fatalf("MarkScanJobSucceeded() error = %v", err)
	}

	gotJob, err := repo.GetScanJob(ctx, job.ID)
	if err != nil {
		t.Fatalf("GetScanJob() error = %v", err)
	}
	if gotJob.Status != domain.ScanJobSucceeded || gotJob.ResourceCount != 1 {
		t.Fatalf("job = %#v, want succeeded with one resource", gotJob)
	}
	gotResources, err := repo.ListResources(ctx, domain.ResourceFilter{ScanID: job.ID})
	if err != nil {
		t.Fatalf("ListResources() error = %v", err)
	}
	if len(gotResources) != 1 || gotResources[0].NativeID != "vpc-sqlite" {
		t.Fatalf("resources = %#v, want sqlite vpc", gotResources)
	}
}

func TestSQLiteStorePersistsGovernanceObjects(t *testing.T) {
	ctx := context.Background()
	db, err := OpenSQLite(filepath.Join(t.TempDir(), "governance.db"))
	if err != nil {
		t.Fatalf("OpenSQLite() error = %v", err)
	}
	if err := AutoMigrate(db); err != nil {
		t.Fatalf("AutoMigrate() error = %v", err)
	}
	repo := NewSQLStore(db)

	resource := domain.Resource{
		Provider:   domain.ProviderDemo,
		AccountID:  "acct-1",
		Region:     "cn-hangzhou",
		Type:       domain.ResourceTypeDisk,
		NativeID:   "d-sql",
		Name:       "sql-disk",
		State:      "Available",
		Tags:       map[string]string{"team": "platform"},
		CreatedAt:  time.Now().UTC(),
		LastSeenAt: time.Now().UTC(),
		Raw:        map[string]any{"DiskId": "d-sql"},
	}
	if err := repo.UpsertResources(ctx, "scan-sql", []domain.Resource{resource}); err != nil {
		t.Fatalf("UpsertResources() error = %v", err)
	}
	resources, err := repo.ListResources(ctx, domain.ResourceFilter{ScanID: "scan-sql"})
	if err != nil {
		t.Fatalf("ListResources() error = %v", err)
	}
	if len(resources) != 1 {
		t.Fatalf("resources = %d, want 1", len(resources))
	}

	edge := domain.ResourceEdge{
		SourceResourceID: resources[0].ID,
		TargetResourceID: resources[0].ID,
		Type:             "self",
		Source:           "test",
		Confidence:       1,
		Evidence:         map[string]any{"field": "DiskId"},
	}
	if err := repo.UpsertResourceEdges(ctx, "scan-sql", []domain.ResourceEdge{edge}); err != nil {
		t.Fatalf("UpsertResourceEdges() error = %v", err)
	}
	edges, err := repo.ListResourceEdges(ctx, domain.GraphFilter{ScanID: "scan-sql"})
	if err != nil {
		t.Fatalf("ListResourceEdges() error = %v", err)
	}
	if len(edges) != 1 || edges[0].Type != "self" {
		t.Fatalf("edges = %#v, want self edge", edges)
	}

	candidate := domain.CleanupCandidate{
		ResourceID:              resources[0].ID,
		RuleID:                  "unattached_disk",
		Reason:                  "disk is unattached",
		Risk:                    domain.RiskLow,
		RecommendedAction:       "delete",
		EstimatedMonthlySavings: 8,
		Evidence:                map[string]any{"state": "Available"},
	}
	if err := repo.UpsertCandidates(ctx, "scan-sql", []domain.CleanupCandidate{candidate}); err != nil {
		t.Fatalf("UpsertCandidates() error = %v", err)
	}
	candidates, err := repo.ListCandidates(ctx, domain.CandidateFilter{ScanID: "scan-sql"})
	if err != nil {
		t.Fatalf("ListCandidates() error = %v", err)
	}
	if len(candidates) != 1 || candidates[0].Resource.NativeID != "d-sql" {
		t.Fatalf("candidates = %#v, want one candidate with resource", candidates)
	}

	plan, err := repo.CreateCleanupPlan(ctx, domain.CleanupPlan{
		Status:                  domain.PlanStatusDraft,
		DryRun:                  true,
		Risk:                    domain.RiskLow,
		EstimatedMonthlySavings: 8,
		CreatedBy:               "tester",
	}, []domain.CleanupPlanItem{{
		CandidateID:             candidates[0].ID,
		ResourceID:              resources[0].ID,
		Action:                  "delete",
		Risk:                    domain.RiskLow,
		Reason:                  "disk is unattached",
		EstimatedMonthlySavings: 8,
	}})
	if err != nil {
		t.Fatalf("CreateCleanupPlan() error = %v", err)
	}
	items, err := repo.ListPlanItems(ctx, plan.ID)
	if err != nil {
		t.Fatalf("ListPlanItems() error = %v", err)
	}
	if len(items) != 1 || items[0].Resource.NativeID != "d-sql" {
		t.Fatalf("items = %#v, want plan item with resource", items)
	}

	if _, err := repo.CreateAuditEvent(ctx, domain.AuditEvent{Actor: "tester", Action: "plan.create", TargetType: "plan", TargetID: plan.ID, Result: "succeeded"}); err != nil {
		t.Fatalf("CreateAuditEvent() error = %v", err)
	}
	audits, err := repo.ListAuditEvents(ctx, domain.AuditFilter{TargetID: plan.ID})
	if err != nil {
		t.Fatalf("ListAuditEvents() error = %v", err)
	}
	if len(audits) != 1 || audits[0].Action != "plan.create" {
		t.Fatalf("audits = %#v, want plan.create", audits)
	}
	report, err := repo.GetSavingsReport(ctx)
	if err != nil {
		t.Fatalf("GetSavingsReport() error = %v", err)
	}
	if report.EstimatedMonthlySavings != 8 {
		t.Fatalf("EstimatedMonthlySavings = %f, want 8", report.EstimatedMonthlySavings)
	}
}
