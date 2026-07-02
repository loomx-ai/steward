package governance

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/prodesire/cloud-steward/internal/domain"
	"github.com/prodesire/cloud-steward/internal/store"
)

type Service struct {
	repo     store.Repository
	executor PlanExecutor
}

type ReconcileSummary struct {
	EdgeCount      int `json:"edge_count"`
	CandidateCount int `json:"candidate_count"`
}

type PlanLimits struct {
	MaxResourceCount int
	MaxRegionCount   int
	MaxHighRiskCount int
}

type ExecutionRequest struct {
	Plan     domain.CleanupPlan
	Item     domain.CleanupPlanItem
	Resource domain.Resource
	Actor    string
}

type ExecutionResult struct {
	Result    string
	RequestID string
}

type PlanExecutor interface {
	ExecutePlanItem(context.Context, ExecutionRequest) (ExecutionResult, error)
}

type executionMode interface {
	DryRun() bool
}

type DryRunExecutor struct{}

func (DryRunExecutor) DryRun() bool {
	return true
}

func (DryRunExecutor) ExecutePlanItem(_ context.Context, _ ExecutionRequest) (ExecutionResult, error) {
	return ExecutionResult{Result: "dry_run_skipped", RequestID: requestID()}, nil
}

func NewService(repo store.Repository) *Service {
	return NewServiceWithExecutor(repo, DryRunExecutor{})
}

func NewServiceWithExecutor(repo store.Repository, executor PlanExecutor) *Service {
	if executor == nil {
		executor = DryRunExecutor{}
	}
	return &Service{repo: repo, executor: executor}
}

func (s *Service) ReconcileScan(ctx context.Context, scanID string, actor string) (ReconcileSummary, error) {
	resources, err := s.repo.ListResources(ctx, domain.ResourceFilter{ScanID: scanID})
	if err != nil {
		return ReconcileSummary{}, err
	}
	edges := buildEdges(scanID, resources)
	candidates := buildCandidates(scanID, resources)
	if err := s.repo.UpsertResourceEdges(ctx, scanID, edges); err != nil {
		return ReconcileSummary{}, err
	}
	if err := s.repo.UpsertCandidates(ctx, scanID, candidates); err != nil {
		return ReconcileSummary{}, err
	}
	_, _ = s.repo.CreateAuditEvent(ctx, domain.AuditEvent{
		Actor:      actorOrSystem(actor),
		Action:     "scan.reconcile",
		TargetType: "scan",
		TargetID:   scanID,
		Result:     "succeeded",
		Message:    fmt.Sprintf("derived %d graph edges and %d cleanup candidates", len(edges), len(candidates)),
		RequestID:  requestID(),
	})
	return ReconcileSummary{EdgeCount: len(edges), CandidateCount: len(candidates)}, nil
}

func (s *Service) CreatePlan(ctx context.Context, candidateIDs []string, actor string) (domain.CleanupPlan, error) {
	return s.CreatePlanWithLimits(ctx, candidateIDs, actor, PlanLimits{})
}

func (s *Service) CreatePlanWithLimits(ctx context.Context, candidateIDs []string, actor string, limits PlanLimits) (domain.CleanupPlan, error) {
	if len(candidateIDs) == 0 {
		return domain.CleanupPlan{}, errors.New("at least one candidate is required")
	}
	items := make([]domain.CleanupPlanItem, 0, len(candidateIDs))
	var total float64
	risk := domain.RiskLow
	for _, id := range candidateIDs {
		candidate, err := s.repo.GetCandidate(ctx, id)
		if err != nil {
			return domain.CleanupPlan{}, err
		}
		total += candidate.EstimatedMonthlySavings
		risk = maxRisk(risk, candidate.Risk)
		blocked := candidate.Resource.Protected
		blockReason := ""
		if blocked {
			blockReason = "resource is protected by tags or production environment"
		}
		items = append(items, domain.CleanupPlanItem{
			CandidateID:             candidate.ID,
			ResourceID:              candidate.ResourceID,
			Resource:                candidate.Resource,
			Action:                  candidate.RecommendedAction,
			Risk:                    candidate.Risk,
			Blocked:                 blocked,
			BlockReason:             blockReason,
			Reason:                  candidate.Reason,
			Evidence:                candidate.Evidence,
			EstimatedMonthlySavings: candidate.EstimatedMonthlySavings,
		})
	}
	if err := enforcePlanLimits(items, limits); err != nil {
		return domain.CleanupPlan{}, err
	}
	sort.SliceStable(items, func(i, j int) bool {
		return actionOrder(items[i].Action) < actionOrder(items[j].Action)
	})
	for i := range items {
		items[i].Order = i + 1
	}
	plan := domain.CleanupPlan{
		Status:                  domain.PlanStatusDraft,
		DryRun:                  s.planDryRun(),
		Risk:                    risk,
		EstimatedMonthlySavings: total,
		CreatedBy:               actorOrSystem(actor),
	}
	created, err := s.repo.CreateCleanupPlan(ctx, plan, items)
	if err != nil {
		return domain.CleanupPlan{}, err
	}
	for _, id := range candidateIDs {
		_ = s.repo.UpdateCandidateStatus(ctx, id, domain.CandidateAccepted)
	}
	_, _ = s.repo.CreateAuditEvent(ctx, domain.AuditEvent{
		Actor:      actorOrSystem(actor),
		Action:     "plan.create",
		TargetType: "plan",
		TargetID:   created.ID,
		Result:     "succeeded",
		Message:    fmt.Sprintf("created %s plan with %d resources", planModeName(created.DryRun), created.ResourceCount),
		RequestID:  requestID(),
	})
	return created, nil
}

func enforcePlanLimits(items []domain.CleanupPlanItem, limits PlanLimits) error {
	if limits.MaxResourceCount > 0 && len(items) > limits.MaxResourceCount {
		return fmt.Errorf("max_resource_count exceeded: selected %d resources, limit is %d", len(items), limits.MaxResourceCount)
	}
	if limits.MaxRegionCount > 0 {
		regions := map[string]bool{}
		for _, item := range items {
			regions[item.Resource.Region] = true
		}
		if len(regions) > limits.MaxRegionCount {
			return fmt.Errorf("max_region_count exceeded: selected %d regions, limit is %d", len(regions), limits.MaxRegionCount)
		}
	}
	if limits.MaxHighRiskCount >= 0 {
		var highRisk int
		for _, item := range items {
			if item.Risk == domain.RiskHigh {
				highRisk++
			}
		}
		if highRisk > limits.MaxHighRiskCount {
			return fmt.Errorf("max_high_risk_count exceeded: selected %d high-risk resources, limit is %d", highRisk, limits.MaxHighRiskCount)
		}
	}
	return nil
}

func (s *Service) ApprovePlan(ctx context.Context, planID string, actor string, comment string) (domain.CleanupPlan, error) {
	plan, err := s.repo.GetCleanupPlan(ctx, planID)
	if err != nil {
		return domain.CleanupPlan{}, err
	}
	if plan.Status != domain.PlanStatusDraft && plan.Status != domain.PlanStatusPendingApproval {
		return domain.CleanupPlan{}, fmt.Errorf("plan status %q cannot be approved", plan.Status)
	}
	now := time.Now().UTC()
	plan.Status = domain.PlanStatusApproved
	plan.ApprovedBy = actorOrSystem(actor)
	plan.ApprovalComment = comment
	plan.ApprovedAt = &now
	if err := s.repo.UpdateCleanupPlan(ctx, plan); err != nil {
		return domain.CleanupPlan{}, err
	}
	_, _ = s.repo.CreateAuditEvent(ctx, domain.AuditEvent{
		Actor:      actorOrSystem(actor),
		Action:     "plan.approve",
		TargetType: "plan",
		TargetID:   plan.ID,
		Result:     "succeeded",
		Message:    comment,
		RequestID:  requestID(),
	})
	return plan, nil
}

func (s *Service) ExecutePlan(ctx context.Context, planID string, actor string) (domain.CleanupPlan, error) {
	plan, err := s.repo.GetCleanupPlan(ctx, planID)
	if err != nil {
		return domain.CleanupPlan{}, err
	}
	if plan.Status != domain.PlanStatusApproved {
		return domain.CleanupPlan{}, fmt.Errorf("plan status %q cannot be executed", plan.Status)
	}
	items, err := s.repo.ListPlanItems(ctx, plan.ID)
	if err != nil {
		return domain.CleanupPlan{}, err
	}
	plan.Status = domain.PlanStatusRunning
	if err := s.repo.UpdateCleanupPlan(ctx, plan); err != nil {
		return domain.CleanupPlan{}, err
	}
	for _, item := range items {
		result := s.executePlanItem(ctx, plan, item, actorOrSystem(actor))
		_ = s.repo.UpdatePlanItemResult(ctx, item.ID, result.Result, result.RequestID)
	}
	now := time.Now().UTC()
	plan.Status = domain.PlanStatusCompleted
	plan.ExecutedAt = &now
	if err := s.repo.UpdateCleanupPlan(ctx, plan); err != nil {
		return domain.CleanupPlan{}, err
	}
	_, _ = s.repo.CreateAuditEvent(ctx, domain.AuditEvent{
		Actor:      actorOrSystem(actor),
		Action:     "plan.execute",
		TargetType: "plan",
		TargetID:   plan.ID,
		Result:     "succeeded",
		Message:    executionMessage(plan.DryRun),
		RequestID:  requestID(),
	})
	return plan, nil
}

func (s *Service) planDryRun() bool {
	mode, ok := s.executor.(executionMode)
	if !ok {
		return true
	}
	return mode.DryRun()
}

func (s *Service) executePlanItem(ctx context.Context, plan domain.CleanupPlan, item domain.CleanupPlanItem, actor string) ExecutionResult {
	if item.Blocked {
		return ExecutionResult{Result: "blocked", RequestID: requestID()}
	}
	live, err := s.repo.GetResource(ctx, item.ResourceID)
	if err != nil {
		return ExecutionResult{Result: "blocked", RequestID: requestID()}
	}
	if live.Protected {
		return ExecutionResult{Result: "blocked", RequestID: requestID()}
	}
	result, err := s.executor.ExecutePlanItem(ctx, ExecutionRequest{
		Plan:     plan,
		Item:     item,
		Resource: live,
		Actor:    actor,
	})
	if err != nil {
		return ExecutionResult{Result: "failed", RequestID: requestID()}
	}
	if strings.TrimSpace(result.Result) == "" {
		result.Result = "succeeded"
	}
	if strings.TrimSpace(result.RequestID) == "" {
		result.RequestID = requestID()
	}
	return result
}

func buildEdges(scanID string, resources []domain.Resource) []domain.ResourceEdge {
	byNativeID := map[string]domain.Resource{}
	for _, resource := range resources {
		byNativeID[resource.NativeID] = resource
	}
	var edges []domain.ResourceEdge
	add := func(source domain.Resource, targetNativeID string, edgeType string, field string) {
		target, ok := byNativeID[strings.TrimSpace(targetNativeID)]
		if !ok || source.ID == "" || target.ID == "" {
			return
		}
		edges = append(edges, domain.ResourceEdge{
			ScanID:           scanID,
			SourceResourceID: source.ID,
			TargetResourceID: target.ID,
			Type:             edgeType,
			Source:           "raw",
			Confidence:       1,
			Evidence:         map[string]any{"field": field, "value": targetNativeID},
		})
	}
	for _, resource := range resources {
		add(resource, rawString(resource.Raw, "VpcId"), "member_of", "VpcId")
		add(resource, rawString(resource.Raw, "VSwitchId"), "member_of", "VSwitchId")
		add(resource, rawString(resource.Raw, "InstanceId"), "attached_to", "InstanceId")
		add(resource, rawString(resource.Raw, "SourceDiskId"), "created_from", "SourceDiskId")
	}
	return edges
}

func buildCandidates(scanID string, resources []domain.Resource) []domain.CleanupCandidate {
	referencedSecurityGroups := map[string]bool{}
	for _, resource := range resources {
		if resource.Type == domain.ResourceTypeECSInstance {
			for _, id := range rawStringSlice(resource.Raw, "SecurityGroupIds") {
				referencedSecurityGroups[id] = true
			}
		}
	}
	now := time.Now().UTC()
	var candidates []domain.CleanupCandidate
	add := func(resource domain.Resource, ruleID string, reason string, action string, risk domain.RiskLevel, savings float64, evidence map[string]any) {
		candidates = append(candidates, domain.CleanupCandidate{
			ScanID:                  scanID,
			ResourceID:              resource.ID,
			Resource:                resource,
			RuleID:                  ruleID,
			Reason:                  reason,
			Evidence:                evidence,
			Confidence:              0.9,
			Risk:                    risk,
			RecommendedAction:       action,
			EstimatedMonthlySavings: savings,
			Status:                  domain.CandidateOpen,
		})
	}
	for _, resource := range resources {
		switch resource.Type {
		case domain.ResourceTypeDisk:
			if strings.EqualFold(resource.State, "Available") || rawString(resource.Raw, "InstanceId") == "" {
				add(resource, "unattached_disk", "disk is not attached to an ECS instance", "delete", domain.RiskLow, 8, map[string]any{"state": resource.State})
			}
		case domain.ResourceTypeEIP:
			if strings.EqualFold(resource.State, "Available") || rawString(resource.Raw, "InstanceId") == "" {
				add(resource, "unused_eip", "EIP is not associated with a cloud resource", "release", domain.RiskLow, 3, map[string]any{"state": resource.State})
			}
		case domain.ResourceTypeECSInstance:
			if strings.EqualFold(resource.State, "Stopped") {
				add(resource, "stopped_instance", "ECS instance is stopped", "tag", domain.RiskMedium, 20, map[string]any{"state": resource.State})
			}
		case domain.ResourceTypeSnapshot:
			if resource.CreatedAt.Before(now.AddDate(0, -3, 0)) {
				add(resource, "old_snapshot", "snapshot is older than 90 days", "delete", domain.RiskLow, 2, map[string]any{"created_at": resource.CreatedAt})
			}
		case domain.ResourceTypeSecurityGroup:
			if !referencedSecurityGroups[resource.NativeID] {
				add(resource, "unreferenced_security_group", "security group has no observed ECS references", "tag", domain.RiskMedium, 0.5, map[string]any{"reference_count": 0})
			}
		}
	}
	return candidates
}

func rawString(raw map[string]any, key string) string {
	if raw == nil {
		return ""
	}
	value, ok := raw[key]
	if !ok {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case fmt.Stringer:
		return strings.TrimSpace(typed.String())
	default:
		return strings.TrimSpace(fmt.Sprintf("%v", typed))
	}
}

func rawStringSlice(raw map[string]any, key string) []string {
	value, ok := raw[key]
	if !ok {
		return nil
	}
	switch typed := value.(type) {
	case []string:
		return typed
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			out = append(out, strings.TrimSpace(fmt.Sprintf("%v", item)))
		}
		return out
	default:
		return nil
	}
}

func maxRisk(left domain.RiskLevel, right domain.RiskLevel) domain.RiskLevel {
	if riskRank(right) > riskRank(left) {
		return right
	}
	return left
}

func riskRank(risk domain.RiskLevel) int {
	switch risk {
	case domain.RiskHigh:
		return 3
	case domain.RiskMedium:
		return 2
	case domain.RiskLow:
		return 1
	default:
		return 0
	}
}

func actionOrder(action string) int {
	switch action {
	case "tag":
		return 10
	case "stop":
		return 20
	case "detach":
		return 30
	case "release":
		return 40
	case "delete":
		return 50
	default:
		return 100
	}
}

func actorOrSystem(actor string) string {
	actor = strings.TrimSpace(actor)
	if actor == "" {
		return "system"
	}
	return actor
}

func planModeName(dryRun bool) string {
	if dryRun {
		return "dry-run"
	}
	return "live"
}

func executionMessage(dryRun bool) string {
	if dryRun {
		return "dry-run execution completed; no destructive cloud action was performed"
	}
	return "live execution completed; unsupported or destructive actions may be skipped by the configured executor"
}

func requestID() string {
	return fmt.Sprintf("req-%d", time.Now().UnixNano())
}
