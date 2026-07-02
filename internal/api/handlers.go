package api

import (
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/prodesire/cloud-steward/internal/domain"
	"github.com/prodesire/cloud-steward/internal/governance"
	"github.com/prodesire/cloud-steward/internal/store"
)

type handler struct {
	repo       store.Repository
	governance *governance.Service
}

type createScanRequest struct {
	AccountName     string          `json:"accountName"`
	Provider        domain.Provider `json:"provider"`
	Mode            domain.ScanMode `json:"mode"`
	Regions         []string        `json:"regions"`
	AccessKeyID     string          `json:"accessKeyId"`
	AccessKeySecret string          `json:"accessKeySecret"`
}

type actorRequest struct {
	Actor   string `json:"actor"`
	Comment string `json:"comment"`
}

type createPlanRequest struct {
	CandidateIDs     []string `json:"candidate_ids"`
	Actor            string   `json:"actor"`
	MaxResourceCount int      `json:"max_resource_count"`
	MaxRegionCount   int      `json:"max_region_count"`
	MaxHighRiskCount int      `json:"max_high_risk_count"`
}

type updateCandidateRequest struct {
	Status domain.CandidateStatus `json:"status"`
}

func (h *handler) createScan(w http.ResponseWriter, r *http.Request) {
	var req createScanRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "validation_error", "request body must be valid JSON")
		return
	}
	req.AccountName = strings.TrimSpace(req.AccountName)
	if req.AccountName == "" {
		writeError(w, http.StatusBadRequest, "validation_error", "accountName is required")
		return
	}
	if len(req.Regions) == 0 {
		writeError(w, http.StatusBadRequest, "validation_error", "at least one region is required")
		return
	}
	for i, region := range req.Regions {
		req.Regions[i] = strings.TrimSpace(region)
		if req.Regions[i] == "" {
			writeError(w, http.StatusBadRequest, "validation_error", "regions cannot contain empty values")
			return
		}
	}
	if req.Mode == "" {
		req.Mode = domain.ScanModeDemo
	}
	if req.Provider == "" {
		req.Provider = domain.ProviderAliCloud
		if req.Mode == domain.ScanModeDemo {
			req.Provider = domain.ProviderDemo
		}
	}

	account, err := h.repo.UpsertAccount(r.Context(), domain.Account{
		Name:            req.AccountName,
		Provider:        req.Provider,
		AccessKeyID:     strings.TrimSpace(req.AccessKeyID),
		AccessKeySecret: req.AccessKeySecret,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", "failed to save account")
		return
	}
	job, err := h.repo.CreateScanJob(r.Context(), domain.ScanJob{
		AccountID:   account.ID,
		AccountName: account.Name,
		Provider:    req.Provider,
		Mode:        req.Mode,
		Regions:     req.Regions,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", "failed to create scan job")
		return
	}
	writeJSON(w, http.StatusCreated, job)
}

func (h *handler) listScans(w http.ResponseWriter, r *http.Request) {
	jobs, err := h.repo.ListScanJobs(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", "failed to list scans")
		return
	}
	writeJSON(w, http.StatusOK, jobs)
}

func (h *handler) getScan(w http.ResponseWriter, r *http.Request) {
	job, err := h.repo.GetScanJob(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeStoreError(w, err, "scan")
		return
	}
	writeJSON(w, http.StatusOK, job)
}

func (h *handler) listResources(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	filter := domain.ResourceFilter{
		ScanID:    query.Get("scan_id"),
		AccountID: query.Get("account_id"),
		Region:    query.Get("region"),
		Query:     query.Get("q"),
	}
	if provider := query.Get("provider"); provider != "" {
		filter.Provider = domain.Provider(provider)
	}
	if resourceType := query.Get("type"); resourceType != "" {
		filter.Type = domain.ResourceType(resourceType)
	}
	resources, err := h.repo.ListResources(r.Context(), filter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", "failed to list resources")
		return
	}
	writeJSON(w, http.StatusOK, resources)
}

func (h *handler) getResource(w http.ResponseWriter, r *http.Request) {
	resource, err := h.repo.GetResource(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeStoreError(w, err, "resource")
		return
	}
	writeJSON(w, http.StatusOK, resource)
}

func (h *handler) reconcileScan(w http.ResponseWriter, r *http.Request) {
	var req actorRequest
	_ = json.NewDecoder(r.Body).Decode(&req)
	summary, err := h.governance.ReconcileScan(r.Context(), chi.URLParam(r, "id"), req.Actor)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "reconcile_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, summary)
}

func (h *handler) listGraph(w http.ResponseWriter, r *http.Request) {
	filter := domain.GraphFilter{
		ScanID:     r.URL.Query().Get("scan_id"),
		ResourceID: r.URL.Query().Get("resource_id"),
		VPCID:      r.URL.Query().Get("vpc_id"),
	}
	edges, err := h.repo.ListResourceEdges(r.Context(), filter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", "failed to list graph")
		return
	}
	writeJSON(w, http.StatusOK, edges)
}

func (h *handler) listCandidates(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	filter := domain.CandidateFilter{
		ScanID: query.Get("scan_id"),
		RuleID: query.Get("rule_id"),
		Team:   query.Get("team"),
		Region: query.Get("region"),
	}
	if resourceType := query.Get("type"); resourceType != "" {
		filter.ResourceType = domain.ResourceType(resourceType)
	}
	if risk := query.Get("risk"); risk != "" {
		filter.Risk = domain.RiskLevel(risk)
	}
	if status := query.Get("status"); status != "" {
		filter.Status = domain.CandidateStatus(status)
	}
	candidates, err := h.repo.ListCandidates(r.Context(), filter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", "failed to list candidates")
		return
	}
	writeJSON(w, http.StatusOK, candidates)
}

func (h *handler) updateCandidate(w http.ResponseWriter, r *http.Request) {
	var req updateCandidateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "validation_error", "request body must be valid JSON")
		return
	}
	if req.Status == "" {
		writeError(w, http.StatusBadRequest, "validation_error", "status is required")
		return
	}
	id := chi.URLParam(r, "id")
	if err := h.repo.UpdateCandidateStatus(r.Context(), id, req.Status); err != nil {
		writeStoreError(w, err, "candidate")
		return
	}
	candidate, err := h.repo.GetCandidate(r.Context(), id)
	if err != nil {
		writeStoreError(w, err, "candidate")
		return
	}
	writeJSON(w, http.StatusOK, candidate)
}

func (h *handler) createPlan(w http.ResponseWriter, r *http.Request) {
	var req createPlanRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "validation_error", "request body must be valid JSON")
		return
	}
	if req.MaxResourceCount < 0 || req.MaxRegionCount < 0 || req.MaxHighRiskCount < 0 {
		writeError(w, http.StatusBadRequest, "validation_error", "plan limits must be non-negative")
		return
	}
	plan, err := h.governance.CreatePlanWithLimits(r.Context(), req.CandidateIDs, req.Actor, governance.PlanLimits{
		MaxResourceCount: req.MaxResourceCount,
		MaxRegionCount:   req.MaxRegionCount,
		MaxHighRiskCount: req.MaxHighRiskCount,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, "plan_error", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, plan)
}

func (h *handler) listPlans(w http.ResponseWriter, r *http.Request) {
	plans, err := h.repo.ListCleanupPlans(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", "failed to list plans")
		return
	}
	writeJSON(w, http.StatusOK, plans)
}

type planDetail struct {
	Plan  domain.CleanupPlan       `json:"plan"`
	Items []domain.CleanupPlanItem `json:"items"`
}

func (h *handler) getPlan(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	plan, err := h.repo.GetCleanupPlan(r.Context(), id)
	if err != nil {
		writeStoreError(w, err, "plan")
		return
	}
	items, err := h.repo.ListPlanItems(r.Context(), id)
	if err != nil {
		writeStoreError(w, err, "plan")
		return
	}
	writeJSON(w, http.StatusOK, planDetail{Plan: plan, Items: items})
}

func (h *handler) exportPlan(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	plan, err := h.repo.GetCleanupPlan(r.Context(), id)
	if err != nil {
		writeStoreError(w, err, "plan")
		return
	}
	items, err := h.repo.ListPlanItems(r.Context(), id)
	if err != nil {
		writeStoreError(w, err, "plan")
		return
	}
	detail := planDetail{Plan: plan, Items: items}
	switch strings.ToLower(strings.TrimSpace(r.URL.Query().Get("format"))) {
	case "", "json":
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="cleanup-plan-%s.json"`, plan.ID))
		writeJSON(w, http.StatusOK, detail)
	case "markdown", "md":
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="cleanup-plan-%s.md"`, plan.ID))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(formatPlanMarkdown(detail)))
	default:
		writeError(w, http.StatusBadRequest, "validation_error", "format must be json or markdown")
	}
}

func (h *handler) approvePlan(w http.ResponseWriter, r *http.Request) {
	var req actorRequest
	_ = json.NewDecoder(r.Body).Decode(&req)
	plan, err := h.governance.ApprovePlan(r.Context(), chi.URLParam(r, "id"), req.Actor, req.Comment)
	if err != nil {
		writeError(w, http.StatusBadRequest, "plan_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, plan)
}

func (h *handler) executePlan(w http.ResponseWriter, r *http.Request) {
	var req actorRequest
	_ = json.NewDecoder(r.Body).Decode(&req)
	plan, err := h.governance.ExecutePlan(r.Context(), chi.URLParam(r, "id"), req.Actor)
	if err != nil {
		writeError(w, http.StatusBadRequest, "plan_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, plan)
}

func (h *handler) listAudits(w http.ResponseWriter, r *http.Request) {
	filter := auditFilterFromQuery(r)
	events, err := h.repo.ListAuditEvents(r.Context(), filter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", "failed to list audit events")
		return
	}
	writeJSON(w, http.StatusOK, events)
}

func (h *handler) exportAudits(w http.ResponseWriter, r *http.Request) {
	events, err := h.repo.ListAuditEvents(r.Context(), auditFilterFromQuery(r))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", "failed to list audit events")
		return
	}
	switch strings.ToLower(strings.TrimSpace(r.URL.Query().Get("format"))) {
	case "", "json":
		w.Header().Set("Content-Disposition", `attachment; filename="cloud-steward-audits.json"`)
		writeJSON(w, http.StatusOK, events)
	case "csv":
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="cloud-steward-audits.csv"`)
		w.WriteHeader(http.StatusOK)
		writer := csv.NewWriter(w)
		_ = writer.Write([]string{"created_at", "actor", "action", "target_type", "target_id", "result", "request_id", "message"})
		for _, event := range events {
			_ = writer.Write([]string{
				event.CreatedAt.Format(time.RFC3339),
				event.Actor,
				event.Action,
				event.TargetType,
				event.TargetID,
				event.Result,
				event.RequestID,
				event.Message,
			})
		}
		writer.Flush()
	default:
		writeError(w, http.StatusBadRequest, "validation_error", "format must be json or csv")
	}
}

func (h *handler) savingsReport(w http.ResponseWriter, r *http.Request) {
	report, err := h.repo.GetSavingsReport(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", "failed to build savings report")
		return
	}
	writeJSON(w, http.StatusOK, report)
}

func writeStoreError(w http.ResponseWriter, err error, noun string) {
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", fmt.Sprintf("%s not found", noun))
		return
	}
	writeError(w, http.StatusInternalServerError, "store_error", "storage operation failed")
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, code string, message string) {
	writeJSON(w, status, domain.APIErrorResponse{
		Error: domain.APIError{
			Code:      code,
			Message:   message,
			RequestID: requestID(),
		},
	})
}

func requestID() string {
	return fmt.Sprintf("req-%d", time.Now().UnixNano())
}

func auditFilterFromQuery(r *http.Request) domain.AuditFilter {
	return domain.AuditFilter{
		Action:     r.URL.Query().Get("action"),
		TargetType: r.URL.Query().Get("target_type"),
		TargetID:   r.URL.Query().Get("target_id"),
	}
}

func formatPlanMarkdown(detail planDetail) string {
	var b strings.Builder
	plan := detail.Plan
	fmt.Fprintf(&b, "# Cleanup Plan %s\n\n", plan.ID)
	fmt.Fprintf(&b, "- Status: %s\n", plan.Status)
	fmt.Fprintf(&b, "- Dry run: %t\n", plan.DryRun)
	fmt.Fprintf(&b, "- Risk: %s\n", plan.Risk)
	fmt.Fprintf(&b, "- Resources: %d\n", plan.ResourceCount)
	fmt.Fprintf(&b, "- Estimated monthly savings: %.2f\n", plan.EstimatedMonthlySavings)
	fmt.Fprintf(&b, "- Created by: %s\n", markdownCell(plan.CreatedBy))
	if plan.ApprovedBy != "" {
		fmt.Fprintf(&b, "- Approved by: %s\n", markdownCell(plan.ApprovedBy))
	}
	if plan.ApprovalComment != "" {
		fmt.Fprintf(&b, "- Approval comment: %s\n", markdownCell(plan.ApprovalComment))
	}
	b.WriteString("\n## Items\n\n")
	b.WriteString("| Order | Resource | Type | Region | Action | Risk | Blocked | Result | Reason |\n")
	b.WriteString("| --- | --- | --- | --- | --- | --- | --- | --- | --- |\n")
	for _, item := range detail.Items {
		blocked := "no"
		if item.Blocked {
			blocked = "yes: " + item.BlockReason
		}
		result := item.Result
		if result == "" {
			result = "pending"
		}
		fmt.Fprintf(
			&b,
			"| %d | %s | %s | %s | %s | %s | %s | %s | %s |\n",
			item.Order,
			markdownCell(item.Resource.Name),
			markdownCell(string(item.Resource.Type)),
			markdownCell(item.Resource.Region),
			markdownCell(item.Action),
			markdownCell(string(item.Risk)),
			markdownCell(blocked),
			markdownCell(result),
			markdownCell(item.Reason),
		)
	}
	return b.String()
}

func markdownCell(value string) string {
	value = strings.ReplaceAll(value, "\n", " ")
	value = strings.ReplaceAll(value, "\r", " ")
	value = strings.ReplaceAll(value, "|", "\\|")
	value = strings.TrimSpace(value)
	if value == "" {
		return "-"
	}
	return value
}
