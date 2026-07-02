package store

import (
	"context"
	"errors"

	"github.com/prodesire/cloud-steward/internal/domain"
)

var ErrNotFound = errors.New("not found")

type Repository interface {
	UpsertAccount(context.Context, domain.Account) (domain.Account, error)
	GetAccount(context.Context, string) (domain.Account, error)
	CreateScanJob(context.Context, domain.ScanJob) (domain.ScanJob, error)
	ClaimNextPendingScanJob(context.Context) (domain.ScanJob, bool, error)
	MarkScanJobSucceeded(context.Context, string, int) error
	MarkScanJobFailed(context.Context, string, string) error
	ListScanJobs(context.Context) ([]domain.ScanJob, error)
	GetScanJob(context.Context, string) (domain.ScanJob, error)
	UpsertResources(context.Context, string, []domain.Resource) error
	ListResources(context.Context, domain.ResourceFilter) ([]domain.Resource, error)
	GetResource(context.Context, string) (domain.Resource, error)
	UpsertResourceEdges(context.Context, string, []domain.ResourceEdge) error
	ListResourceEdges(context.Context, domain.GraphFilter) ([]domain.ResourceEdge, error)
	UpsertCandidates(context.Context, string, []domain.CleanupCandidate) error
	ListCandidates(context.Context, domain.CandidateFilter) ([]domain.CleanupCandidate, error)
	GetCandidate(context.Context, string) (domain.CleanupCandidate, error)
	UpdateCandidateStatus(context.Context, string, domain.CandidateStatus) error
	CreateCleanupPlan(context.Context, domain.CleanupPlan, []domain.CleanupPlanItem) (domain.CleanupPlan, error)
	ListCleanupPlans(context.Context) ([]domain.CleanupPlan, error)
	GetCleanupPlan(context.Context, string) (domain.CleanupPlan, error)
	ListPlanItems(context.Context, string) ([]domain.CleanupPlanItem, error)
	UpdateCleanupPlan(context.Context, domain.CleanupPlan) error
	UpdatePlanItemResult(context.Context, string, string, string) error
	CreateAuditEvent(context.Context, domain.AuditEvent) (domain.AuditEvent, error)
	ListAuditEvents(context.Context, domain.AuditFilter) ([]domain.AuditEvent, error)
	GetSavingsReport(context.Context) (domain.SavingsReport, error)
}
