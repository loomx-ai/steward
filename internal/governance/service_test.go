package governance

import (
	"context"
	"testing"

	"github.com/prodesire/cloud-steward/internal/domain"
	"github.com/prodesire/cloud-steward/internal/scanner"
	"github.com/prodesire/cloud-steward/internal/store"
)

func TestServiceReconcilesGraphCandidatesAndAudit(t *testing.T) {
	ctx := context.Background()
	repo, scanID := seedDemoScan(t, ctx)
	service := NewService(repo)

	summary, err := service.ReconcileScan(ctx, scanID, "tester")
	if err != nil {
		t.Fatalf("ReconcileScan() error = %v", err)
	}
	if summary.EdgeCount < 4 {
		t.Fatalf("edge count = %d, want at least 4", summary.EdgeCount)
	}
	if summary.CandidateCount < 4 {
		t.Fatalf("candidate count = %d, want at least 4", summary.CandidateCount)
	}

	graph, err := repo.ListResourceEdges(ctx, domain.GraphFilter{ScanID: scanID})
	if err != nil {
		t.Fatalf("ListResourceEdges() error = %v", err)
	}
	if !hasEdgeType(graph, "member_of") {
		t.Fatalf("graph edges = %#v, want member_of edge", graph)
	}
	if !hasEdgeType(graph, "created_from") {
		t.Fatalf("graph edges = %#v, want created_from edge", graph)
	}

	candidates, err := repo.ListCandidates(ctx, domain.CandidateFilter{ScanID: scanID})
	if err != nil {
		t.Fatalf("ListCandidates() error = %v", err)
	}
	if !hasCandidateRule(candidates, "unattached_disk") {
		t.Fatalf("candidates = %#v, want unattached_disk", candidates)
	}
	if !hasCandidateRule(candidates, "unused_eip") {
		t.Fatalf("candidates = %#v, want unused_eip", candidates)
	}
	if !hasCandidateRule(candidates, "old_snapshot") {
		t.Fatalf("candidates = %#v, want old_snapshot", candidates)
	}

	audits, err := repo.ListAuditEvents(ctx, domain.AuditFilter{})
	if err != nil {
		t.Fatalf("ListAuditEvents() error = %v", err)
	}
	if len(audits) == 0 || audits[0].Action != "scan.reconcile" {
		t.Fatalf("audits = %#v, want scan.reconcile event", audits)
	}
}

func TestServiceCreatesApprovesAndExecutesDryRunPlan(t *testing.T) {
	ctx := context.Background()
	repo, scanID := seedDemoScan(t, ctx)
	service := NewService(repo)
	if _, err := service.ReconcileScan(ctx, scanID, "tester"); err != nil {
		t.Fatalf("ReconcileScan() error = %v", err)
	}
	candidates, err := repo.ListCandidates(ctx, domain.CandidateFilter{ScanID: scanID})
	if err != nil {
		t.Fatalf("ListCandidates() error = %v", err)
	}
	ids := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		ids = append(ids, candidate.ID)
	}

	plan, err := service.CreatePlan(ctx, ids, "tester")
	if err != nil {
		t.Fatalf("CreatePlan() error = %v", err)
	}
	if plan.Status != domain.PlanStatusDraft {
		t.Fatalf("plan status = %s, want draft", plan.Status)
	}
	if !plan.DryRun {
		t.Fatal("plan DryRun = false, want true")
	}
	if plan.ResourceCount == 0 {
		t.Fatal("plan ResourceCount = 0, want items")
	}
	items, err := repo.ListPlanItems(ctx, plan.ID)
	if err != nil {
		t.Fatalf("ListPlanItems() error = %v", err)
	}
	if len(items) != plan.ResourceCount {
		t.Fatalf("plan items = %d, want %d", len(items), plan.ResourceCount)
	}

	approved, err := service.ApprovePlan(ctx, plan.ID, "approver", "ship it")
	if err != nil {
		t.Fatalf("ApprovePlan() error = %v", err)
	}
	if approved.Status != domain.PlanStatusApproved {
		t.Fatalf("approved status = %s, want approved", approved.Status)
	}
	executed, err := service.ExecutePlan(ctx, plan.ID, "operator")
	if err != nil {
		t.Fatalf("ExecutePlan() error = %v", err)
	}
	if executed.Status != domain.PlanStatusCompleted {
		t.Fatalf("executed status = %s, want completed", executed.Status)
	}

	report, err := repo.GetSavingsReport(ctx)
	if err != nil {
		t.Fatalf("GetSavingsReport() error = %v", err)
	}
	if report.CandidateCount == 0 {
		t.Fatalf("report CandidateCount = 0, want candidates")
	}
	if report.EstimatedMonthlySavings <= 0 {
		t.Fatalf("report EstimatedMonthlySavings = %f, want positive", report.EstimatedMonthlySavings)
	}
}

func TestServiceRechecksResourceProtectionBeforeExecution(t *testing.T) {
	ctx := context.Background()
	repo, scanID := seedDemoScan(t, ctx)
	service := NewService(repo)
	if _, err := service.ReconcileScan(ctx, scanID, "tester"); err != nil {
		t.Fatalf("ReconcileScan() error = %v", err)
	}
	candidates, err := repo.ListCandidates(ctx, domain.CandidateFilter{ScanID: scanID, RuleID: "unattached_disk"})
	if err != nil {
		t.Fatalf("ListCandidates() error = %v", err)
	}
	if len(candidates) == 0 {
		t.Fatal("unattached disk candidate not found")
	}
	plan, err := service.CreatePlan(ctx, []string{candidates[0].ID}, "tester")
	if err != nil {
		t.Fatalf("CreatePlan() error = %v", err)
	}
	items, err := repo.ListPlanItems(ctx, plan.ID)
	if err != nil {
		t.Fatalf("ListPlanItems() error = %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("plan items = %d, want 1", len(items))
	}
	live := items[0].Resource
	live.Tags["env"] = "prod"
	if err := repo.UpsertResources(ctx, live.ScanID, []domain.Resource{live}); err != nil {
		t.Fatalf("UpsertResources() error = %v", err)
	}
	if _, err := service.ApprovePlan(ctx, plan.ID, "approver", "ok"); err != nil {
		t.Fatalf("ApprovePlan() error = %v", err)
	}

	if _, err := service.ExecutePlan(ctx, plan.ID, "operator"); err != nil {
		t.Fatalf("ExecutePlan() error = %v", err)
	}

	executedItems, err := repo.ListPlanItems(ctx, plan.ID)
	if err != nil {
		t.Fatalf("ListPlanItems() after execute error = %v", err)
	}
	if executedItems[0].Result != "blocked" {
		t.Fatalf("item result = %q, want blocked", executedItems[0].Result)
	}
	if executedItems[0].RequestID == "" {
		t.Fatal("item request id is empty")
	}
}

func TestServiceUsesExecutorWithLiveResource(t *testing.T) {
	ctx := context.Background()
	repo, scanID := seedDemoScan(t, ctx)
	executor := &recordingExecutor{result: ExecutionResult{Result: "tagged", RequestID: "req-custom"}}
	service := NewServiceWithExecutor(repo, executor)
	if _, err := service.ReconcileScan(ctx, scanID, "tester"); err != nil {
		t.Fatalf("ReconcileScan() error = %v", err)
	}
	candidates, err := repo.ListCandidates(ctx, domain.CandidateFilter{ScanID: scanID, RuleID: "unattached_disk"})
	if err != nil {
		t.Fatalf("ListCandidates() error = %v", err)
	}
	if len(candidates) == 0 {
		t.Fatal("unattached disk candidate not found")
	}
	plan, err := service.CreatePlan(ctx, []string{candidates[0].ID}, "tester")
	if err != nil {
		t.Fatalf("CreatePlan() error = %v", err)
	}
	items, err := repo.ListPlanItems(ctx, plan.ID)
	if err != nil {
		t.Fatalf("ListPlanItems() error = %v", err)
	}
	live := items[0].Resource
	live.Tags["owner"] = "current-owner"
	if err := repo.UpsertResources(ctx, live.ScanID, []domain.Resource{live}); err != nil {
		t.Fatalf("UpsertResources() error = %v", err)
	}
	if _, err := service.ApprovePlan(ctx, plan.ID, "approver", "ok"); err != nil {
		t.Fatalf("ApprovePlan() error = %v", err)
	}

	if _, err := service.ExecutePlan(ctx, plan.ID, "operator"); err != nil {
		t.Fatalf("ExecutePlan() error = %v", err)
	}

	if executor.called != 1 {
		t.Fatalf("executor calls = %d, want 1", executor.called)
	}
	if got := executor.request.Resource.Tags["owner"]; got != "current-owner" {
		t.Fatalf("executor resource owner = %q, want current-owner", got)
	}
	executedItems, err := repo.ListPlanItems(ctx, plan.ID)
	if err != nil {
		t.Fatalf("ListPlanItems() after execute error = %v", err)
	}
	if executedItems[0].Result != "tagged" {
		t.Fatalf("item result = %q, want tagged", executedItems[0].Result)
	}
	if executedItems[0].RequestID != "req-custom" {
		t.Fatalf("item request id = %q, want req-custom", executedItems[0].RequestID)
	}
}

func TestServiceMarksPlanLiveForTagExecutor(t *testing.T) {
	ctx := context.Background()
	repo, scanID := seedDemoScan(t, ctx)
	service := NewServiceWithExecutor(repo, TagExecutor{Client: &recordingTagClient{requestID: "req-tag"}})
	if _, err := service.ReconcileScan(ctx, scanID, "tester"); err != nil {
		t.Fatalf("ReconcileScan() error = %v", err)
	}
	candidates, err := repo.ListCandidates(ctx, domain.CandidateFilter{ScanID: scanID, RuleID: "stopped_instance"})
	if err != nil {
		t.Fatalf("ListCandidates() error = %v", err)
	}
	if len(candidates) == 0 {
		t.Fatal("stopped instance candidate not found")
	}

	plan, err := service.CreatePlan(ctx, []string{candidates[0].ID}, "tester")
	if err != nil {
		t.Fatalf("CreatePlan() error = %v", err)
	}
	if plan.DryRun {
		t.Fatal("plan DryRun = true, want false for live tag executor")
	}
}

type recordingExecutor struct {
	called  int
	request ExecutionRequest
	result  ExecutionResult
}

func (e *recordingExecutor) ExecutePlanItem(_ context.Context, request ExecutionRequest) (ExecutionResult, error) {
	e.called++
	e.request = request
	return e.result, nil
}

func seedDemoScan(t *testing.T, ctx context.Context) (*store.MemoryStore, string) {
	t.Helper()
	repo := store.NewMemoryStore()
	account, err := repo.UpsertAccount(ctx, domain.Account{Name: "demo", Provider: domain.ProviderDemo})
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
	scanService := scanner.NewService(repo, scanner.Registry{domain.ScanModeDemo: scanner.DemoConnector{}})
	if processed, err := scanService.ProcessNext(ctx); err != nil || !processed {
		t.Fatalf("ProcessNext() processed=%v err=%v, want processed without error", processed, err)
	}
	return repo, job.ID
}

func hasEdgeType(edges []domain.ResourceEdge, edgeType string) bool {
	for _, edge := range edges {
		if edge.Type == edgeType {
			return true
		}
	}
	return false
}

func hasCandidateRule(candidates []domain.CleanupCandidate, ruleID string) bool {
	for _, candidate := range candidates {
		if candidate.RuleID == ruleID {
			return true
		}
	}
	return false
}
