package store

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/prodesire/cloud-steward/internal/domain"
)

type MemoryStore struct {
	mu            sync.Mutex
	nextAccount   int
	nextScan      int
	nextResource  int
	accounts      map[string]domain.Account
	accountIndex  map[string]string
	scans         map[string]domain.ScanJob
	resources     map[string]domain.Resource
	resourceKeys  map[string]string
	edges         map[string]domain.ResourceEdge
	edgeKeys      map[string]string
	candidates    map[string]domain.CleanupCandidate
	candidateKeys map[string]string
	plans         map[string]domain.CleanupPlan
	planItems     map[string][]domain.CleanupPlanItem
	audits        map[string]domain.AuditEvent
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		accounts:      map[string]domain.Account{},
		accountIndex:  map[string]string{},
		scans:         map[string]domain.ScanJob{},
		resources:     map[string]domain.Resource{},
		resourceKeys:  map[string]string{},
		edges:         map[string]domain.ResourceEdge{},
		edgeKeys:      map[string]string{},
		candidates:    map[string]domain.CleanupCandidate{},
		candidateKeys: map[string]string{},
		plans:         map[string]domain.CleanupPlan{},
		planItems:     map[string][]domain.CleanupPlanItem{},
		audits:        map[string]domain.AuditEvent{},
	}
}

func (s *MemoryStore) UpsertAccount(_ context.Context, account domain.Account) (domain.Account, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().UTC()
	indexKey := fmt.Sprintf("%s/%s", account.Provider, strings.ToLower(strings.TrimSpace(account.Name)))
	if account.ID == "" {
		if existingID := s.accountIndex[indexKey]; existingID != "" {
			existing := s.accounts[existingID]
			existing.AccessKeyID = account.AccessKeyID
			existing.AccessKeySecret = account.AccessKeySecret
			existing.UpdatedAt = now
			s.accounts[existing.ID] = existing
			return cloneAccount(existing), nil
		}
		s.nextAccount++
		account.ID = fmt.Sprintf("acct-%d", s.nextAccount)
		account.CreatedAt = now
	}
	account.UpdatedAt = now
	s.accounts[account.ID] = account
	s.accountIndex[indexKey] = account.ID
	return cloneAccount(account), nil
}

func (s *MemoryStore) GetAccount(_ context.Context, id string) (domain.Account, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	account, ok := s.accounts[id]
	if !ok {
		return domain.Account{}, ErrNotFound
	}
	return cloneAccount(account), nil
}

func (s *MemoryStore) CreateScanJob(_ context.Context, job domain.ScanJob) (domain.ScanJob, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if job.ID == "" {
		s.nextScan++
		job.ID = fmt.Sprintf("scan-%d", s.nextScan)
	}
	if job.Status == "" {
		job.Status = domain.ScanJobPending
	}
	if job.CreatedAt.IsZero() {
		job.CreatedAt = time.Now().UTC()
	}
	job.Regions = append([]string(nil), job.Regions...)
	s.scans[job.ID] = job
	return cloneScanJob(job), nil
}

func (s *MemoryStore) ClaimNextPendingScanJob(_ context.Context) (domain.ScanJob, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	jobs := make([]domain.ScanJob, 0, len(s.scans))
	for _, job := range s.scans {
		if job.Status == domain.ScanJobPending {
			jobs = append(jobs, job)
		}
	}
	sort.Slice(jobs, func(i, j int) bool {
		return jobs[i].CreatedAt.Before(jobs[j].CreatedAt)
	})
	if len(jobs) == 0 {
		return domain.ScanJob{}, false, nil
	}
	now := time.Now().UTC()
	claimed := jobs[0]
	claimed.Status = domain.ScanJobRunning
	claimed.StartedAt = &now
	s.scans[claimed.ID] = claimed
	return cloneScanJob(claimed), true, nil
}

func (s *MemoryStore) MarkScanJobSucceeded(_ context.Context, id string, resourceCount int) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	job, ok := s.scans[id]
	if !ok {
		return ErrNotFound
	}
	now := time.Now().UTC()
	job.Status = domain.ScanJobSucceeded
	job.ResourceCount = resourceCount
	job.FailureReason = ""
	job.FinishedAt = &now
	s.scans[id] = job
	return nil
}

func (s *MemoryStore) MarkScanJobFailed(_ context.Context, id string, reason string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	job, ok := s.scans[id]
	if !ok {
		return ErrNotFound
	}
	now := time.Now().UTC()
	job.Status = domain.ScanJobFailed
	job.FailureReason = reason
	job.FinishedAt = &now
	s.scans[id] = job
	return nil
}

func (s *MemoryStore) ListScanJobs(_ context.Context) ([]domain.ScanJob, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	jobs := make([]domain.ScanJob, 0, len(s.scans))
	for _, job := range s.scans {
		jobs = append(jobs, cloneScanJob(job))
	}
	sort.Slice(jobs, func(i, j int) bool {
		return jobs[i].CreatedAt.After(jobs[j].CreatedAt)
	})
	return jobs, nil
}

func (s *MemoryStore) GetScanJob(_ context.Context, id string) (domain.ScanJob, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	job, ok := s.scans[id]
	if !ok {
		return domain.ScanJob{}, ErrNotFound
	}
	return cloneScanJob(job), nil
}

func (s *MemoryStore) UpsertResources(_ context.Context, scanID string, resources []domain.Resource) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, resource := range resources {
		resource = resource.WithDerivedFields()
		if err := resource.Validate(); err != nil {
			return err
		}
		resource.ScanID = scanID
		key := resourceKey(resource)
		if existingID := s.resourceKeys[key]; existingID != "" {
			resource.ID = existingID
		} else if resource.ID == "" {
			s.nextResource++
			resource.ID = fmt.Sprintf("res-%d", s.nextResource)
		}
		s.resourceKeys[key] = resource.ID
		s.resources[resource.ID] = cloneResource(resource)
	}
	return nil
}

func (s *MemoryStore) ListResources(_ context.Context, filter domain.ResourceFilter) ([]domain.Resource, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	resources := make([]domain.Resource, 0, len(s.resources))
	for _, resource := range s.resources {
		if !matchesResourceFilter(resource, filter) {
			continue
		}
		resources = append(resources, cloneResource(resource))
	}
	sort.Slice(resources, func(i, j int) bool {
		if resources[i].LastSeenAt.Equal(resources[j].LastSeenAt) {
			return resources[i].Name < resources[j].Name
		}
		return resources[i].LastSeenAt.After(resources[j].LastSeenAt)
	})
	return resources, nil
}

func (s *MemoryStore) GetResource(_ context.Context, id string) (domain.Resource, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	resource, ok := s.resources[id]
	if !ok {
		return domain.Resource{}, ErrNotFound
	}
	return cloneResource(resource), nil
}

func (s *MemoryStore) UpsertResourceEdges(_ context.Context, scanID string, edges []domain.ResourceEdge) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, edge := range edges {
		edge.ScanID = scanID
		if edge.Confidence == 0 {
			edge.Confidence = 1
		}
		if edge.Source == "" {
			edge.Source = "raw"
		}
		if edge.CreatedAt.IsZero() {
			edge.CreatedAt = time.Now().UTC()
		}
		key := edgeKey(edge)
		if existingID := s.edgeKeys[key]; existingID != "" {
			edge.ID = existingID
		} else if edge.ID == "" {
			edge.ID = fmt.Sprintf("edge-%d", len(s.edges)+1)
		}
		s.edgeKeys[key] = edge.ID
		s.edges[edge.ID] = cloneEdge(edge)
	}
	return nil
}

func (s *MemoryStore) ListResourceEdges(_ context.Context, filter domain.GraphFilter) ([]domain.ResourceEdge, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	edges := make([]domain.ResourceEdge, 0, len(s.edges))
	for _, edge := range s.edges {
		if filter.ScanID != "" && edge.ScanID != filter.ScanID {
			continue
		}
		if filter.ResourceID != "" && edge.SourceResourceID != filter.ResourceID && edge.TargetResourceID != filter.ResourceID {
			continue
		}
		edges = append(edges, cloneEdge(edge))
	}
	sort.Slice(edges, func(i, j int) bool {
		if edges[i].SourceResourceID == edges[j].SourceResourceID {
			return edges[i].TargetResourceID < edges[j].TargetResourceID
		}
		return edges[i].SourceResourceID < edges[j].SourceResourceID
	})
	return edges, nil
}

func (s *MemoryStore) UpsertCandidates(_ context.Context, scanID string, candidates []domain.CleanupCandidate) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, candidate := range candidates {
		candidate.ScanID = scanID
		if candidate.Status == "" {
			candidate.Status = domain.CandidateOpen
		}
		now := time.Now().UTC()
		if candidate.CreatedAt.IsZero() {
			candidate.CreatedAt = now
		}
		candidate.UpdatedAt = now
		key := candidateKey(candidate)
		if existingID := s.candidateKeys[key]; existingID != "" {
			existing := s.candidates[existingID]
			candidate.ID = existingID
			candidate.Status = existing.Status
			candidate.CreatedAt = existing.CreatedAt
		} else if candidate.ID == "" {
			candidate.ID = fmt.Sprintf("cand-%d", len(s.candidates)+1)
		}
		s.candidateKeys[key] = candidate.ID
		s.candidates[candidate.ID] = cloneCandidate(candidate)
	}
	return nil
}

func (s *MemoryStore) ListCandidates(_ context.Context, filter domain.CandidateFilter) ([]domain.CleanupCandidate, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	candidates := make([]domain.CleanupCandidate, 0, len(s.candidates))
	for _, candidate := range s.candidates {
		if !matchesCandidateFilter(candidate, s.resources[candidate.ResourceID], filter) {
			continue
		}
		candidate.Resource = cloneResource(s.resources[candidate.ResourceID])
		candidates = append(candidates, cloneCandidate(candidate))
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].Risk == candidates[j].Risk {
			return candidates[i].EstimatedMonthlySavings > candidates[j].EstimatedMonthlySavings
		}
		return riskRank(candidates[i].Risk) > riskRank(candidates[j].Risk)
	})
	return candidates, nil
}

func (s *MemoryStore) GetCandidate(_ context.Context, id string) (domain.CleanupCandidate, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	candidate, ok := s.candidates[id]
	if !ok {
		return domain.CleanupCandidate{}, ErrNotFound
	}
	candidate.Resource = cloneResource(s.resources[candidate.ResourceID])
	return cloneCandidate(candidate), nil
}

func (s *MemoryStore) UpdateCandidateStatus(_ context.Context, id string, status domain.CandidateStatus) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	candidate, ok := s.candidates[id]
	if !ok {
		return ErrNotFound
	}
	candidate.Status = status
	candidate.UpdatedAt = time.Now().UTC()
	s.candidates[id] = candidate
	return nil
}

func (s *MemoryStore) CreateCleanupPlan(_ context.Context, plan domain.CleanupPlan, items []domain.CleanupPlanItem) (domain.CleanupPlan, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if plan.ID == "" {
		plan.ID = fmt.Sprintf("plan-%d", len(s.plans)+1)
	}
	if plan.Status == "" {
		plan.Status = domain.PlanStatusDraft
	}
	if plan.CreatedAt.IsZero() {
		plan.CreatedAt = time.Now().UTC()
	}
	plan.ResourceCount = len(items)
	copiedItems := make([]domain.CleanupPlanItem, 0, len(items))
	for i, item := range items {
		if item.ID == "" {
			item.ID = fmt.Sprintf("%s-item-%d", plan.ID, i+1)
		}
		item.PlanID = plan.ID
		if item.Order == 0 {
			item.Order = i + 1
		}
		if item.CreatedAt.IsZero() {
			item.CreatedAt = plan.CreatedAt
		}
		item.Resource = cloneResource(s.resources[item.ResourceID])
		copiedItems = append(copiedItems, clonePlanItem(item))
	}
	s.plans[plan.ID] = clonePlan(plan)
	s.planItems[plan.ID] = copiedItems
	return clonePlan(plan), nil
}

func (s *MemoryStore) ListCleanupPlans(_ context.Context) ([]domain.CleanupPlan, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	plans := make([]domain.CleanupPlan, 0, len(s.plans))
	for _, plan := range s.plans {
		plans = append(plans, clonePlan(plan))
	}
	sort.Slice(plans, func(i, j int) bool {
		return plans[i].CreatedAt.After(plans[j].CreatedAt)
	})
	return plans, nil
}

func (s *MemoryStore) GetCleanupPlan(_ context.Context, id string) (domain.CleanupPlan, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	plan, ok := s.plans[id]
	if !ok {
		return domain.CleanupPlan{}, ErrNotFound
	}
	return clonePlan(plan), nil
}

func (s *MemoryStore) ListPlanItems(_ context.Context, planID string) ([]domain.CleanupPlanItem, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	items, ok := s.planItems[planID]
	if !ok {
		return nil, ErrNotFound
	}
	out := make([]domain.CleanupPlanItem, 0, len(items))
	for _, item := range items {
		item.Resource = cloneResource(s.resources[item.ResourceID])
		out = append(out, clonePlanItem(item))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Order < out[j].Order })
	return out, nil
}

func (s *MemoryStore) UpdateCleanupPlan(_ context.Context, plan domain.CleanupPlan) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.plans[plan.ID]; !ok {
		return ErrNotFound
	}
	s.plans[plan.ID] = clonePlan(plan)
	return nil
}

func (s *MemoryStore) UpdatePlanItemResult(_ context.Context, itemID string, result string, requestID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for planID, items := range s.planItems {
		for i := range items {
			if items[i].ID == itemID {
				items[i].Result = result
				items[i].RequestID = requestID
				s.planItems[planID] = items
				return nil
			}
		}
	}
	return ErrNotFound
}

func (s *MemoryStore) CreateAuditEvent(_ context.Context, event domain.AuditEvent) (domain.AuditEvent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if event.ID == "" {
		event.ID = fmt.Sprintf("audit-%d", len(s.audits)+1)
	}
	if event.CreatedAt.IsZero() {
		event.CreatedAt = time.Now().UTC()
	}
	s.audits[event.ID] = cloneAudit(event)
	return cloneAudit(event), nil
}

func (s *MemoryStore) ListAuditEvents(_ context.Context, filter domain.AuditFilter) ([]domain.AuditEvent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	events := make([]domain.AuditEvent, 0, len(s.audits))
	for _, event := range s.audits {
		if filter.Action != "" && event.Action != filter.Action {
			continue
		}
		if filter.TargetType != "" && event.TargetType != filter.TargetType {
			continue
		}
		if filter.TargetID != "" && event.TargetID != filter.TargetID {
			continue
		}
		events = append(events, cloneAudit(event))
	}
	sort.Slice(events, func(i, j int) bool { return events[i].CreatedAt.After(events[j].CreatedAt) })
	return events, nil
}

func (s *MemoryStore) GetSavingsReport(_ context.Context) (domain.SavingsReport, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	report := domain.SavingsReport{
		CandidateCount:         len(s.candidates),
		PlanCount:              len(s.plans),
		EstimatedSavingsByTeam: map[string]float64{},
		EstimatedSavingsByType: map[string]float64{},
	}
	for _, candidate := range s.candidates {
		report.EstimatedMonthlySavings += candidate.EstimatedMonthlySavings
		resource := s.resources[candidate.ResourceID]
		team := resource.Ownership.Team
		if team == "" {
			team = "unassigned"
		}
		report.EstimatedSavingsByTeam[team] += candidate.EstimatedMonthlySavings
		report.EstimatedSavingsByType[string(resource.Type)] += candidate.EstimatedMonthlySavings
	}
	for _, plan := range s.plans {
		if plan.Status == domain.PlanStatusCompleted {
			report.CompletedPlanCount++
		}
	}
	return report, nil
}

func matchesResourceFilter(resource domain.Resource, filter domain.ResourceFilter) bool {
	if filter.ScanID != "" && resource.ScanID != filter.ScanID {
		return false
	}
	if filter.Provider != "" && resource.Provider != filter.Provider {
		return false
	}
	if filter.AccountID != "" && resource.AccountID != filter.AccountID {
		return false
	}
	if filter.Region != "" && resource.Region != filter.Region {
		return false
	}
	if filter.Type != "" && resource.Type != filter.Type {
		return false
	}
	if filter.Query != "" {
		needle := strings.ToLower(strings.TrimSpace(filter.Query))
		haystack := strings.ToLower(resource.Name + " " + resource.NativeID + " " + string(resource.Type))
		if !strings.Contains(haystack, needle) {
			return false
		}
	}
	return true
}

func resourceKey(resource domain.Resource) string {
	return fmt.Sprintf("%s/%s/%s/%s/%s", resource.Provider, resource.AccountID, resource.Region, resource.Type, resource.NativeID)
}

func cloneAccount(account domain.Account) domain.Account {
	return account
}

func cloneScanJob(job domain.ScanJob) domain.ScanJob {
	job.Regions = append([]string(nil), job.Regions...)
	if job.StartedAt != nil {
		startedAt := *job.StartedAt
		job.StartedAt = &startedAt
	}
	if job.FinishedAt != nil {
		finishedAt := *job.FinishedAt
		job.FinishedAt = &finishedAt
	}
	return job
}

func cloneResource(resource domain.Resource) domain.Resource {
	resource.Tags = cloneStringMap(resource.Tags)
	resource.Raw = cloneAnyMap(resource.Raw)
	return resource
}

func cloneEdge(edge domain.ResourceEdge) domain.ResourceEdge {
	edge.Evidence = cloneAnyMap(edge.Evidence)
	return edge
}

func cloneCandidate(candidate domain.CleanupCandidate) domain.CleanupCandidate {
	candidate.Evidence = cloneAnyMap(candidate.Evidence)
	candidate.Resource = cloneResource(candidate.Resource)
	return candidate
}

func clonePlan(plan domain.CleanupPlan) domain.CleanupPlan {
	if plan.ApprovedAt != nil {
		approvedAt := *plan.ApprovedAt
		plan.ApprovedAt = &approvedAt
	}
	if plan.ExecutedAt != nil {
		executedAt := *plan.ExecutedAt
		plan.ExecutedAt = &executedAt
	}
	return plan
}

func clonePlanItem(item domain.CleanupPlanItem) domain.CleanupPlanItem {
	item.Evidence = cloneAnyMap(item.Evidence)
	item.Resource = cloneResource(item.Resource)
	return item
}

func cloneAudit(event domain.AuditEvent) domain.AuditEvent {
	return event
}

func edgeKey(edge domain.ResourceEdge) string {
	return fmt.Sprintf("%s/%s/%s/%s", edge.ScanID, edge.SourceResourceID, edge.TargetResourceID, edge.Type)
}

func candidateKey(candidate domain.CleanupCandidate) string {
	return fmt.Sprintf("%s/%s/%s", candidate.ScanID, candidate.ResourceID, candidate.RuleID)
}

func matchesCandidateFilter(candidate domain.CleanupCandidate, resource domain.Resource, filter domain.CandidateFilter) bool {
	if filter.ScanID != "" && candidate.ScanID != filter.ScanID {
		return false
	}
	if filter.RuleID != "" && candidate.RuleID != filter.RuleID {
		return false
	}
	if filter.ResourceType != "" && resource.Type != filter.ResourceType {
		return false
	}
	if filter.Team != "" && resource.Ownership.Team != filter.Team {
		return false
	}
	if filter.Region != "" && resource.Region != filter.Region {
		return false
	}
	if filter.Risk != "" && candidate.Risk != filter.Risk {
		return false
	}
	if filter.Status != "" && candidate.Status != filter.Status {
		return false
	}
	return true
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

func cloneStringMap(input map[string]string) map[string]string {
	if input == nil {
		return map[string]string{}
	}
	out := make(map[string]string, len(input))
	for key, value := range input {
		out[key] = value
	}
	return out
}

func cloneAnyMap(input map[string]any) map[string]any {
	if input == nil {
		return map[string]any{}
	}
	out := make(map[string]any, len(input))
	for key, value := range input {
		out[key] = value
	}
	return out
}
