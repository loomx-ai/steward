package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/prodesire/cloud-steward/internal/domain"
	"github.com/prodesire/cloud-steward/internal/governance"
	"github.com/prodesire/cloud-steward/internal/store"
)

func TestCreateScanCreatesAccountAndPendingJob(t *testing.T) {
	repo := store.NewMemoryStore()
	router := NewRouter(repo)
	body := strings.NewReader(`{
		"accountName": "dev",
		"provider": "alicloud",
		"mode": "demo",
		"regions": ["cn-hangzhou"],
		"accessKeyId": "ak",
		"accessKeySecret": "secret"
	}`)
	req := httptest.NewRequest(http.MethodPost, "/api/scans", body)
	rr := httptest.NewRecorder()

	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", rr.Code, rr.Body.String())
	}
	if strings.Contains(rr.Body.String(), "secret") {
		t.Fatalf("response leaked access key secret: %s", rr.Body.String())
	}
	var got domain.ScanJob
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode scan job: %v", err)
	}
	if got.Status != domain.ScanJobPending {
		t.Fatalf("status = %s, want pending", got.Status)
	}
	if got.AccountName != "dev" {
		t.Fatalf("account name = %q, want dev", got.AccountName)
	}
	if len(got.Regions) != 1 || got.Regions[0] != "cn-hangzhou" {
		t.Fatalf("regions = %#v, want cn-hangzhou", got.Regions)
	}
}

func TestCreateScanValidatesRegions(t *testing.T) {
	repo := store.NewMemoryStore()
	router := NewRouter(repo)
	req := httptest.NewRequest(http.MethodPost, "/api/scans", strings.NewReader(`{"accountName":"dev","mode":"demo","regions":[]}`))
	rr := httptest.NewRecorder()

	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rr.Code, rr.Body.String())
	}
	var got domain.APIErrorResponse
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	if got.Error.Code != "validation_error" {
		t.Fatalf("error code = %q, want validation_error", got.Error.Code)
	}
	if got.Error.RequestID == "" {
		t.Fatal("request id is empty")
	}
}

func TestListAndGetResources(t *testing.T) {
	ctx := context.Background()
	repo := store.NewMemoryStore()
	now := time.Date(2026, 7, 2, 15, 30, 0, 0, time.UTC)
	resources := []domain.Resource{{
		Provider:   domain.ProviderDemo,
		AccountID:  "acct-1",
		Region:     "cn-hangzhou",
		Type:       domain.ResourceTypeDisk,
		NativeID:   "d-1",
		Name:       "orphan-disk",
		State:      "Available",
		Tags:       map[string]string{"team": "platform"},
		CreatedAt:  now,
		LastSeenAt: now,
		Raw:        map[string]any{"DiskId": "d-1"},
	}}
	if err := repo.UpsertResources(ctx, "scan-1", resources); err != nil {
		t.Fatalf("UpsertResources() error = %v", err)
	}
	router := NewRouter(repo)

	listReq := httptest.NewRequest(http.MethodGet, "/api/resources?type=disk&q=orphan", nil)
	listRR := httptest.NewRecorder()
	router.ServeHTTP(listRR, listReq)
	if listRR.Code != http.StatusOK {
		t.Fatalf("list status = %d, want 200; body=%s", listRR.Code, listRR.Body.String())
	}
	var listed []domain.Resource
	if err := json.NewDecoder(listRR.Body).Decode(&listed); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(listed) != 1 {
		t.Fatalf("listed resources = %d, want 1", len(listed))
	}

	detailReq := httptest.NewRequest(http.MethodGet, "/api/resources/"+listed[0].ID, nil)
	detailRR := httptest.NewRecorder()
	router.ServeHTTP(detailRR, detailReq)
	if detailRR.Code != http.StatusOK {
		t.Fatalf("detail status = %d, want 200; body=%s", detailRR.Code, detailRR.Body.String())
	}
	var detail domain.Resource
	if err := json.NewDecoder(detailRR.Body).Decode(&detail); err != nil {
		t.Fatalf("decode detail: %v", err)
	}
	if detail.Raw["DiskId"] != "d-1" {
		t.Fatalf("raw DiskId = %#v, want d-1", detail.Raw["DiskId"])
	}
}

func TestGetMissingScanReturnsErrorEnvelope(t *testing.T) {
	repo := store.NewMemoryStore()
	router := NewRouter(repo)
	req := httptest.NewRequest(http.MethodGet, "/api/scans/missing", bytes.NewReader(nil))
	rr := httptest.NewRecorder()

	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rr.Code)
	}
	var got domain.APIErrorResponse
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	if got.Error.Code != "not_found" {
		t.Fatalf("error code = %q, want not_found", got.Error.Code)
	}
}

func TestGovernanceEndpoints(t *testing.T) {
	ctx := context.Background()
	repo := store.NewMemoryStore()
	now := time.Now().UTC().AddDate(0, -4, 0)
	resources := []domain.Resource{
		{
			Provider:   domain.ProviderDemo,
			AccountID:  "acct-1",
			Region:     "cn-hangzhou",
			Type:       domain.ResourceTypeVPC,
			NativeID:   "vpc-1",
			Name:       "vpc",
			State:      "Available",
			Tags:       map[string]string{"team": "platform", "env": "dev"},
			CreatedAt:  now,
			LastSeenAt: now,
			Raw:        map[string]any{"VpcId": "vpc-1"},
		},
		{
			Provider:   domain.ProviderDemo,
			AccountID:  "acct-1",
			Region:     "cn-hangzhou",
			Type:       domain.ResourceTypeDisk,
			NativeID:   "d-1",
			Name:       "orphan-disk",
			State:      "Available",
			Tags:       map[string]string{"team": "platform", "env": "dev"},
			CreatedAt:  now,
			LastSeenAt: now,
			Raw:        map[string]any{"DiskId": "d-1", "VpcId": "vpc-1"},
		},
	}
	if err := repo.UpsertResources(ctx, "scan-1", resources); err != nil {
		t.Fatalf("UpsertResources() error = %v", err)
	}
	router := NewRouter(repo)

	reconcileReq := httptest.NewRequest(http.MethodPost, "/api/scans/scan-1/reconcile", strings.NewReader(`{"actor":"tester"}`))
	reconcileRR := httptest.NewRecorder()
	router.ServeHTTP(reconcileRR, reconcileReq)
	if reconcileRR.Code != http.StatusOK {
		t.Fatalf("reconcile status = %d, want 200; body=%s", reconcileRR.Code, reconcileRR.Body.String())
	}

	graphRR := httptest.NewRecorder()
	router.ServeHTTP(graphRR, httptest.NewRequest(http.MethodGet, "/api/graph?scan_id=scan-1", nil))
	if graphRR.Code != http.StatusOK {
		t.Fatalf("graph status = %d, want 200; body=%s", graphRR.Code, graphRR.Body.String())
	}
	var graph []domain.ResourceEdge
	if err := json.NewDecoder(graphRR.Body).Decode(&graph); err != nil {
		t.Fatalf("decode graph: %v", err)
	}
	if len(graph) == 0 {
		t.Fatal("graph edges = 0, want edges")
	}

	candidatesRR := httptest.NewRecorder()
	router.ServeHTTP(candidatesRR, httptest.NewRequest(http.MethodGet, "/api/candidates?scan_id=scan-1", nil))
	if candidatesRR.Code != http.StatusOK {
		t.Fatalf("candidates status = %d, want 200; body=%s", candidatesRR.Code, candidatesRR.Body.String())
	}
	var candidates []domain.CleanupCandidate
	if err := json.NewDecoder(candidatesRR.Body).Decode(&candidates); err != nil {
		t.Fatalf("decode candidates: %v", err)
	}
	if len(candidates) == 0 {
		t.Fatal("candidates = 0, want cleanup candidates")
	}

	planBody, _ := json.Marshal(map[string]any{"candidate_ids": []string{candidates[0].ID}, "actor": "tester"})
	planRR := httptest.NewRecorder()
	router.ServeHTTP(planRR, httptest.NewRequest(http.MethodPost, "/api/plans", bytes.NewReader(planBody)))
	if planRR.Code != http.StatusCreated {
		t.Fatalf("plan status = %d, want 201; body=%s", planRR.Code, planRR.Body.String())
	}
	var plan domain.CleanupPlan
	if err := json.NewDecoder(planRR.Body).Decode(&plan); err != nil {
		t.Fatalf("decode plan: %v", err)
	}
	if plan.Status != domain.PlanStatusDraft {
		t.Fatalf("plan status = %s, want draft", plan.Status)
	}

	approveRR := httptest.NewRecorder()
	router.ServeHTTP(approveRR, httptest.NewRequest(http.MethodPost, "/api/plans/"+plan.ID+"/approve", strings.NewReader(`{"actor":"approver","comment":"ok"}`)))
	if approveRR.Code != http.StatusOK {
		t.Fatalf("approve status = %d, want 200; body=%s", approveRR.Code, approveRR.Body.String())
	}
	executeRR := httptest.NewRecorder()
	router.ServeHTTP(executeRR, httptest.NewRequest(http.MethodPost, "/api/plans/"+plan.ID+"/execute", strings.NewReader(`{"actor":"operator"}`)))
	if executeRR.Code != http.StatusOK {
		t.Fatalf("execute status = %d, want 200; body=%s", executeRR.Code, executeRR.Body.String())
	}
	var executed domain.CleanupPlan
	if err := json.NewDecoder(executeRR.Body).Decode(&executed); err != nil {
		t.Fatalf("decode executed plan: %v", err)
	}
	if executed.Status != domain.PlanStatusCompleted {
		t.Fatalf("executed status = %s, want completed", executed.Status)
	}

	auditsRR := httptest.NewRecorder()
	router.ServeHTTP(auditsRR, httptest.NewRequest(http.MethodGet, "/api/audits", nil))
	if auditsRR.Code != http.StatusOK {
		t.Fatalf("audits status = %d, want 200; body=%s", auditsRR.Code, auditsRR.Body.String())
	}
	var audits []domain.AuditEvent
	if err := json.NewDecoder(auditsRR.Body).Decode(&audits); err != nil {
		t.Fatalf("decode audits: %v", err)
	}
	if len(audits) == 0 {
		t.Fatal("audits = 0, want events")
	}

	reportRR := httptest.NewRecorder()
	router.ServeHTTP(reportRR, httptest.NewRequest(http.MethodGet, "/api/reports/savings", nil))
	if reportRR.Code != http.StatusOK {
		t.Fatalf("report status = %d, want 200; body=%s", reportRR.Code, reportRR.Body.String())
	}
	var report domain.SavingsReport
	if err := json.NewDecoder(reportRR.Body).Decode(&report); err != nil {
		t.Fatalf("decode report: %v", err)
	}
	if report.CandidateCount == 0 || report.EstimatedMonthlySavings <= 0 {
		t.Fatalf("report = %#v, want candidates and savings", report)
	}
}

func TestRouterCanUseInjectedExecutor(t *testing.T) {
	ctx := context.Background()
	repo := store.NewMemoryStore()
	now := time.Now().UTC()
	resource := domain.Resource{
		Provider:   domain.ProviderDemo,
		AccountID:  "acct-1",
		Region:     "cn-hangzhou",
		Type:       domain.ResourceTypeECSInstance,
		NativeID:   "i-stopped",
		Name:       "stopped-instance",
		State:      "Stopped",
		Tags:       map[string]string{"team": "platform", "env": "dev"},
		CreatedAt:  now,
		LastSeenAt: now,
		Raw:        map[string]any{"InstanceId": "i-stopped"},
	}
	if err := repo.UpsertResources(ctx, "scan-1", []domain.Resource{resource}); err != nil {
		t.Fatalf("UpsertResources() error = %v", err)
	}
	executor := &apiRecordingExecutor{result: governance.ExecutionResult{Result: "tagged", RequestID: "req-tagged"}}
	router := NewRouterWithExecutor(repo, executor)

	reconcileRR := httptest.NewRecorder()
	router.ServeHTTP(reconcileRR, httptest.NewRequest(http.MethodPost, "/api/scans/scan-1/reconcile", strings.NewReader(`{"actor":"tester"}`)))
	if reconcileRR.Code != http.StatusOK {
		t.Fatalf("reconcile status = %d, want 200; body=%s", reconcileRR.Code, reconcileRR.Body.String())
	}

	candidatesRR := httptest.NewRecorder()
	router.ServeHTTP(candidatesRR, httptest.NewRequest(http.MethodGet, "/api/candidates?scan_id=scan-1", nil))
	if candidatesRR.Code != http.StatusOK {
		t.Fatalf("candidates status = %d, want 200; body=%s", candidatesRR.Code, candidatesRR.Body.String())
	}
	var candidates []domain.CleanupCandidate
	if err := json.NewDecoder(candidatesRR.Body).Decode(&candidates); err != nil {
		t.Fatalf("decode candidates: %v", err)
	}
	if len(candidates) != 1 {
		t.Fatalf("candidates = %d, want 1", len(candidates))
	}

	planBody, _ := json.Marshal(map[string]any{"candidate_ids": []string{candidates[0].ID}, "actor": "tester"})
	planRR := httptest.NewRecorder()
	router.ServeHTTP(planRR, httptest.NewRequest(http.MethodPost, "/api/plans", bytes.NewReader(planBody)))
	if planRR.Code != http.StatusCreated {
		t.Fatalf("plan status = %d, want 201; body=%s", planRR.Code, planRR.Body.String())
	}
	var plan domain.CleanupPlan
	if err := json.NewDecoder(planRR.Body).Decode(&plan); err != nil {
		t.Fatalf("decode plan: %v", err)
	}

	approveRR := httptest.NewRecorder()
	router.ServeHTTP(approveRR, httptest.NewRequest(http.MethodPost, "/api/plans/"+plan.ID+"/approve", strings.NewReader(`{"actor":"approver","comment":"ok"}`)))
	if approveRR.Code != http.StatusOK {
		t.Fatalf("approve status = %d, want 200; body=%s", approveRR.Code, approveRR.Body.String())
	}
	executeRR := httptest.NewRecorder()
	router.ServeHTTP(executeRR, httptest.NewRequest(http.MethodPost, "/api/plans/"+plan.ID+"/execute", strings.NewReader(`{"actor":"operator"}`)))
	if executeRR.Code != http.StatusOK {
		t.Fatalf("execute status = %d, want 200; body=%s", executeRR.Code, executeRR.Body.String())
	}
	if executor.called != 1 {
		t.Fatalf("executor calls = %d, want 1", executor.called)
	}
	items, err := repo.ListPlanItems(ctx, plan.ID)
	if err != nil {
		t.Fatalf("ListPlanItems() error = %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("items = %d, want 1", len(items))
	}
	if items[0].Result != "tagged" {
		t.Fatalf("item result = %q, want tagged", items[0].Result)
	}
	if items[0].RequestID != "req-tagged" {
		t.Fatalf("item request id = %q, want req-tagged", items[0].RequestID)
	}
}

func TestExportPlanEndpointReturnsMarkdown(t *testing.T) {
	router, plan := seedPlanForAPI(t)

	req := httptest.NewRequest(http.MethodGet, "/api/plans/"+plan.ID+"/export?format=markdown", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	if contentType := rr.Header().Get("Content-Type"); !strings.Contains(contentType, "text/markdown") {
		t.Fatalf("content type = %q, want text/markdown", contentType)
	}
	body := rr.Body.String()
	for _, want := range []string{"# Cleanup Plan", plan.ID, "orphan-disk", "delete"} {
		if !strings.Contains(body, want) {
			t.Fatalf("markdown body missing %q:\n%s", want, body)
		}
	}

	jsonRR := httptest.NewRecorder()
	router.ServeHTTP(jsonRR, httptest.NewRequest(http.MethodGet, "/api/plans/"+plan.ID+"/export", nil))
	if jsonRR.Code != http.StatusOK {
		t.Fatalf("json status = %d, want 200; body=%s", jsonRR.Code, jsonRR.Body.String())
	}
	if contentType := jsonRR.Header().Get("Content-Type"); !strings.Contains(contentType, "application/json") {
		t.Fatalf("json content type = %q, want application/json", contentType)
	}
	var exported planDetail
	if err := json.NewDecoder(jsonRR.Body).Decode(&exported); err != nil {
		t.Fatalf("decode exported plan: %v", err)
	}
	if exported.Plan.ID != plan.ID || len(exported.Items) == 0 {
		t.Fatalf("exported plan = %#v, want plan detail with items", exported)
	}

	badRR := httptest.NewRecorder()
	router.ServeHTTP(badRR, httptest.NewRequest(http.MethodGet, "/api/plans/"+plan.ID+"/export?format=pdf", nil))
	if badRR.Code != http.StatusBadRequest {
		t.Fatalf("bad format status = %d, want 400; body=%s", badRR.Code, badRR.Body.String())
	}
}

func TestExportAuditsEndpointReturnsCSV(t *testing.T) {
	router, _ := seedPlanForAPI(t)

	req := httptest.NewRequest(http.MethodGet, "/api/audits/export?format=csv", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	if contentType := rr.Header().Get("Content-Type"); !strings.Contains(contentType, "text/csv") {
		t.Fatalf("content type = %q, want text/csv", contentType)
	}
	body := rr.Body.String()
	for _, want := range []string{"created_at,actor,action,target_type,target_id,result,request_id,message", "scan.reconcile", "plan.create"} {
		if !strings.Contains(body, want) {
			t.Fatalf("csv body missing %q:\n%s", want, body)
		}
	}
}

func TestCreatePlanEnforcesBlastRadiusLimits(t *testing.T) {
	ctx := context.Background()
	repo := store.NewMemoryStore()
	now := time.Now().UTC().AddDate(0, -4, 0)
	resources := []domain.Resource{
		{
			Provider:   domain.ProviderDemo,
			AccountID:  "acct-1",
			Region:     "cn-hangzhou",
			Type:       domain.ResourceTypeDisk,
			NativeID:   "d-1",
			Name:       "orphan-disk-1",
			State:      "Available",
			Tags:       map[string]string{"team": "platform", "env": "dev"},
			CreatedAt:  now,
			LastSeenAt: now,
			Raw:        map[string]any{"DiskId": "d-1"},
		},
		{
			Provider:   domain.ProviderDemo,
			AccountID:  "acct-1",
			Region:     "cn-shanghai",
			Type:       domain.ResourceTypeEIP,
			NativeID:   "eip-1",
			Name:       "unused-eip-1",
			State:      "Available",
			Tags:       map[string]string{"team": "platform", "env": "dev"},
			CreatedAt:  now,
			LastSeenAt: now,
			Raw:        map[string]any{"AllocationId": "eip-1", "InstanceId": ""},
		},
	}
	if err := repo.UpsertResources(ctx, "scan-1", resources); err != nil {
		t.Fatalf("UpsertResources() error = %v", err)
	}
	router := NewRouter(repo)
	reconcileRR := httptest.NewRecorder()
	router.ServeHTTP(reconcileRR, httptest.NewRequest(http.MethodPost, "/api/scans/scan-1/reconcile", strings.NewReader(`{"actor":"tester"}`)))
	if reconcileRR.Code != http.StatusOK {
		t.Fatalf("reconcile status = %d, want 200; body=%s", reconcileRR.Code, reconcileRR.Body.String())
	}
	candidatesRR := httptest.NewRecorder()
	router.ServeHTTP(candidatesRR, httptest.NewRequest(http.MethodGet, "/api/candidates?scan_id=scan-1", nil))
	if candidatesRR.Code != http.StatusOK {
		t.Fatalf("candidates status = %d, want 200; body=%s", candidatesRR.Code, candidatesRR.Body.String())
	}
	var candidates []domain.CleanupCandidate
	if err := json.NewDecoder(candidatesRR.Body).Decode(&candidates); err != nil {
		t.Fatalf("decode candidates: %v", err)
	}
	if len(candidates) != 2 {
		t.Fatalf("candidates = %d, want 2", len(candidates))
	}
	ids := []string{candidates[0].ID, candidates[1].ID}
	planBody, _ := json.Marshal(map[string]any{
		"candidate_ids":       ids,
		"actor":               "tester",
		"max_resource_count":  1,
		"max_region_count":    1,
		"max_high_risk_count": 0,
	})
	planRR := httptest.NewRecorder()
	router.ServeHTTP(planRR, httptest.NewRequest(http.MethodPost, "/api/plans", bytes.NewReader(planBody)))
	if planRR.Code != http.StatusBadRequest {
		t.Fatalf("plan status = %d, want 400; body=%s", planRR.Code, planRR.Body.String())
	}
	if !strings.Contains(planRR.Body.String(), "max_resource_count") {
		t.Fatalf("plan error body = %s, want max_resource_count message", planRR.Body.String())
	}

	negativeBody, _ := json.Marshal(map[string]any{
		"candidate_ids":      ids[:1],
		"actor":              "tester",
		"max_resource_count": -1,
	})
	negativeRR := httptest.NewRecorder()
	router.ServeHTTP(negativeRR, httptest.NewRequest(http.MethodPost, "/api/plans", bytes.NewReader(negativeBody)))
	if negativeRR.Code != http.StatusBadRequest {
		t.Fatalf("negative limit status = %d, want 400; body=%s", negativeRR.Code, negativeRR.Body.String())
	}
	if !strings.Contains(negativeRR.Body.String(), "must be non-negative") {
		t.Fatalf("negative limit body = %s, want non-negative message", negativeRR.Body.String())
	}
}

func seedPlanForAPI(t *testing.T) (http.Handler, domain.CleanupPlan) {
	t.Helper()
	ctx := context.Background()
	repo := store.NewMemoryStore()
	now := time.Now().UTC().AddDate(0, -4, 0)
	resources := []domain.Resource{
		{
			Provider:   domain.ProviderDemo,
			AccountID:  "acct-1",
			Region:     "cn-hangzhou",
			Type:       domain.ResourceTypeVPC,
			NativeID:   "vpc-1",
			Name:       "vpc",
			State:      "Available",
			Tags:       map[string]string{"team": "platform", "env": "dev"},
			CreatedAt:  now,
			LastSeenAt: now,
			Raw:        map[string]any{"VpcId": "vpc-1"},
		},
		{
			Provider:   domain.ProviderDemo,
			AccountID:  "acct-1",
			Region:     "cn-hangzhou",
			Type:       domain.ResourceTypeDisk,
			NativeID:   "d-1",
			Name:       "orphan-disk",
			State:      "Available",
			Tags:       map[string]string{"team": "platform", "env": "dev"},
			CreatedAt:  now,
			LastSeenAt: now,
			Raw:        map[string]any{"DiskId": "d-1", "VpcId": "vpc-1"},
		},
	}
	if err := repo.UpsertResources(ctx, "scan-1", resources); err != nil {
		t.Fatalf("UpsertResources() error = %v", err)
	}
	router := NewRouter(repo)

	reconcileRR := httptest.NewRecorder()
	router.ServeHTTP(reconcileRR, httptest.NewRequest(http.MethodPost, "/api/scans/scan-1/reconcile", strings.NewReader(`{"actor":"tester"}`)))
	if reconcileRR.Code != http.StatusOK {
		t.Fatalf("reconcile status = %d, want 200; body=%s", reconcileRR.Code, reconcileRR.Body.String())
	}
	candidatesRR := httptest.NewRecorder()
	router.ServeHTTP(candidatesRR, httptest.NewRequest(http.MethodGet, "/api/candidates?scan_id=scan-1", nil))
	if candidatesRR.Code != http.StatusOK {
		t.Fatalf("candidates status = %d, want 200; body=%s", candidatesRR.Code, candidatesRR.Body.String())
	}
	var candidates []domain.CleanupCandidate
	if err := json.NewDecoder(candidatesRR.Body).Decode(&candidates); err != nil {
		t.Fatalf("decode candidates: %v", err)
	}
	if len(candidates) == 0 {
		t.Fatal("candidates = 0, want cleanup candidates")
	}
	planBody, _ := json.Marshal(map[string]any{"candidate_ids": []string{candidates[0].ID}, "actor": "tester"})
	planRR := httptest.NewRecorder()
	router.ServeHTTP(planRR, httptest.NewRequest(http.MethodPost, "/api/plans", bytes.NewReader(planBody)))
	if planRR.Code != http.StatusCreated {
		t.Fatalf("plan status = %d, want 201; body=%s", planRR.Code, planRR.Body.String())
	}
	var plan domain.CleanupPlan
	if err := json.NewDecoder(planRR.Body).Decode(&plan); err != nil {
		t.Fatalf("decode plan: %v", err)
	}
	return router, plan
}

type apiRecordingExecutor struct {
	called int
	result governance.ExecutionResult
}

func (e *apiRecordingExecutor) ExecutePlanItem(_ context.Context, _ governance.ExecutionRequest) (governance.ExecutionResult, error) {
	e.called++
	return e.result, nil
}
