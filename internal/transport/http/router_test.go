package httptransport_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/loomx-ai/steward/internal/app/cleanup"
	connectionapp "github.com/loomx-ai/steward/internal/app/connection"
	"github.com/loomx-ai/steward/internal/app/inventory"
	regionapp "github.com/loomx-ai/steward/internal/app/region"
	topologyapp "github.com/loomx-ai/steward/internal/app/topology"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/core/plan"
	coretopology "github.com/loomx-ai/steward/internal/core/topology"
	credentialstore "github.com/loomx-ai/steward/internal/credential"
	"github.com/loomx-ai/steward/internal/persistence"
	"github.com/loomx-ai/steward/internal/persistence/sqlite"
	"github.com/loomx-ai/steward/internal/provider/contracts"
	"github.com/loomx-ai/steward/internal/provider/spec"
	httptransport "github.com/loomx-ai/steward/internal/transport/http"
)

func TestRouterRequiresAuthenticationAndBackendRoles(t *testing.T) {
	repositories, router := terminalRouter(t)
	_ = repositories

	missing := httptest.NewRecorder()
	router.ServeHTTP(missing, httptest.NewRequest(http.MethodGet, "/api/assets", nil))
	if missing.Code != http.StatusUnauthorized {
		t.Fatalf("missing token status = %d", missing.Code)
	}

	forbidden := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/cleanup?connection_id=connection-a", bytes.NewBufferString(`{"selectors":[{"kind":"asset","asset_id":"asset-a"}]}`))
	request.Header.Set("Authorization", "Bearer viewer-token")
	router.ServeHTTP(forbidden, request)
	if forbidden.Code != http.StatusForbidden {
		t.Fatalf("viewer create cleanup task status = %d body=%s", forbidden.Code, forbidden.Body.String())
	}
}

func TestAuthenticatedResponseExposesOnlyVerifiedPrincipalMetadata(t *testing.T) {
	_, router := terminalRouter(t)
	request := httptest.NewRequest(http.MethodGet, "/api/providers/catalog", nil)
	request.Header.Set("Authorization", "Bearer viewer-token")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Header().Get("X-Steward-Subject") != "bob" || response.Header().Get("X-Steward-Roles") != "viewer" {
		t.Fatalf("status=%d subject=%q roles=%q", response.Code, response.Header().Get("X-Steward-Subject"), response.Header().Get("X-Steward-Roles"))
	}
}

func TestOperatorCanMarkAndUnmarkDirtyAsset(t *testing.T) {
	repositories, router := terminalRouter(t)

	viewerRequest := httptest.NewRequest(http.MethodPatch, "/api/assets/asset-a?connection_id=connection-a", bytes.NewBufferString(`{"dirty":true}`))
	viewerRequest.Header.Set("Authorization", "Bearer viewer-token")
	viewerResponse := httptest.NewRecorder()
	router.ServeHTTP(viewerResponse, viewerRequest)
	if viewerResponse.Code != http.StatusForbidden {
		t.Fatalf("viewer patch status=%d body=%s", viewerResponse.Code, viewerResponse.Body.String())
	}

	for _, dirty := range []bool{true, false} {
		request := httptest.NewRequest(
			http.MethodPatch,
			"/api/assets/asset-a?connection_id=connection-a",
			bytes.NewBufferString(fmt.Sprintf(`{"dirty":%t}`, dirty)),
		)
		request.Header.Set("Authorization", "Bearer operator-token")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("dirty=%t status=%d body=%s", dirty, response.Code, response.Body.String())
		}
		var updated asset.Asset
		if err := json.Unmarshal(response.Body.Bytes(), &updated); err != nil {
			t.Fatal(err)
		}
		if updated.Dirty != dirty {
			t.Fatalf("dirty=%t response=%+v", dirty, updated)
		}
	}

	audits, err := repositories.Audits().ListAuditEvents(context.Background(), persistence.ListOptions{Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	actions := map[string]bool{}
	for _, event := range audits.Items {
		actions[event.Action] = event.Actor == "alice" && event.TargetID == "asset-a"
	}
	if !actions["asset.dirty.mark"] || !actions["asset.dirty.unmark"] {
		t.Fatalf("dirty asset audits = %+v", audits.Items)
	}
}

func TestBusinessRoutesRequireAnExistingActiveConnection(t *testing.T) {
	_, router := terminalRouter(t)
	request := httptest.NewRequest(http.MethodGet, "/api/assets?connection_id=missing", nil)
	request.Header.Set("Authorization", "Bearer viewer-token")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusNotFound || !strings.Contains(response.Body.String(), `"code":"connection_not_found"`) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestCatalogSerializesEmptySpecsAsArray(t *testing.T) {
	_, router := terminalRouter(t)
	request := httptest.NewRequest(http.MethodGet, "/api/providers/catalog", nil)
	request.Header.Set("Authorization", "Bearer viewer-token")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var bundles []struct {
		Specs []json.RawMessage `json:"specs"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &bundles); err != nil {
		t.Fatal(err)
	}
	if len(bundles) != 1 || bundles[0].Specs == nil {
		t.Fatalf("catalog specs must be a JSON array: %s", response.Body.String())
	}
}

func TestClientCannotDeclareAuditActor(t *testing.T) {
	repositories, router := terminalRouter(t)
	forged := httptest.NewRequest(http.MethodPost, "/api/cleanup?connection_id=connection-a", bytes.NewBufferString(`{"actor":"forged","selectors":[{"kind":"asset","asset_id":"asset-a"}]}`))
	forged.Header.Set("Authorization", "Bearer operator-token")
	forgedResponse := httptest.NewRecorder()
	router.ServeHTTP(forgedResponse, forged)
	if forgedResponse.Code != http.StatusBadRequest {
		t.Fatalf("forged actor status=%d body=%s", forgedResponse.Code, forgedResponse.Body.String())
	}

	request := httptest.NewRequest(http.MethodPost, "/api/cleanup?connection_id=connection-a", bytes.NewBufferString(`{"selectors":[{"kind":"asset","asset_id":"asset-a"}]}`))
	request.Header.Set("Authorization", "Bearer operator-token")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var aggregate persistence.CleanupTaskAggregate
	if err := json.Unmarshal(response.Body.Bytes(), &aggregate); err != nil {
		t.Fatal(err)
	}
	if aggregate.Task.CreatedBy != "alice" {
		t.Fatalf("cleanup task actor = %q", aggregate.Task.CreatedBy)
	}
	audits, err := repositories.Audits().ListAuditEvents(context.Background(), persistence.ListOptions{Limit: 10})
	if err != nil || len(audits.Items) == 0 || audits.Items[0].Actor != "alice" {
		t.Fatalf("audits=%+v err=%v", audits, err)
	}
}

func TestCleanupTaskRejectsAssetIDRequestContract(t *testing.T) {
	_, router := terminalRouter(t)
	request := httptest.NewRequest(http.MethodPost, "/api/cleanup?connection_id=connection-a", bytes.NewBufferString(`{"asset_ids":["asset-a"]}`))
	request.Header.Set("Authorization", "Bearer operator-token")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "unknown field") {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestOperatorAddsAssetsToCurrentCleanupTask(t *testing.T) {
	repositories, router := terminalRouter(t)
	ctx := context.Background()
	dependency, err := repositories.Inventory().GetAsset(ctx, "asset-a")
	if err != nil {
		t.Fatal(err)
	}
	dependency.ID = "asset-b"
	dependency.Identity.NativeID = "i-b"
	dependency.Name = "instance-b"
	dependency.CurrentObservationID = "observation-b"
	if err := repositories.Inventory().PutAsset(ctx, dependency); err != nil {
		t.Fatal(err)
	}

	createRequest := httptest.NewRequest(http.MethodPost, "/api/cleanup?connection_id=connection-a", bytes.NewBufferString(`{"selectors":[{"kind":"asset","asset_id":"asset-a"}]}`))
	createRequest.Header.Set("Authorization", "Bearer operator-token")
	createResponse := httptest.NewRecorder()
	router.ServeHTTP(createResponse, createRequest)
	if createResponse.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", createResponse.Code, createResponse.Body.String())
	}
	var created persistence.CleanupTaskAggregate
	if err := json.Unmarshal(createResponse.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}

	updateRequest := httptest.NewRequest(
		http.MethodPost,
		"/api/cleanup/"+string(created.Task.ID)+"/assets?connection_id=connection-a",
		bytes.NewBufferString(`{"selectors":[{"kind":"asset","asset_id":"asset-b"}]}`),
	)
	updateRequest.Header.Set("Authorization", "Bearer operator-token")
	updateResponse := httptest.NewRecorder()
	router.ServeHTTP(updateResponse, updateRequest)
	if updateResponse.Code != http.StatusOK {
		t.Fatalf("update status=%d body=%s", updateResponse.Code, updateResponse.Body.String())
	}
	var updated persistence.CleanupTaskAggregate
	if err := json.Unmarshal(updateResponse.Body.Bytes(), &updated); err != nil {
		t.Fatal(err)
	}
	if updated.Task.ID != created.Task.ID || len(updated.Task.Selectors) != 2 || len(updated.Steps) != 2 || updated.Task.UpdatedAt == nil {
		t.Fatalf("updated task = %+v", updated)
	}
	stored, err := repositories.CleanupTasks().GetTask(ctx, created.Task.ID)
	if err != nil || len(stored.Steps) != 2 || stored.Task.ID != created.Task.ID {
		t.Fatalf("stored task = %+v, err = %v", stored, err)
	}
	audits, err := repositories.Audits().ListAuditEvents(ctx, persistence.ListOptions{Limit: 10})
	updateAuditFound := false
	for _, event := range audits.Items {
		if event.Action == "cleanup.task.update" && event.Actor == "alice" {
			updateAuditFound = true
			break
		}
	}
	if err != nil || len(audits.Items) < 2 || !updateAuditFound {
		t.Fatalf("audits = %+v, err = %v", audits.Items, err)
	}
}

func TestExecutionCreationReturnsAcceptedWithoutProviderCall(t *testing.T) {
	repositories, router := terminalRouter(t)
	taskRequest := httptest.NewRequest(http.MethodPost, "/api/cleanup?connection_id=connection-a", bytes.NewBufferString(`{"selectors":[{"kind":"asset","asset_id":"asset-a"}]}`))
	taskRequest.Header.Set("Authorization", "Bearer operator-token")
	taskResponse := httptest.NewRecorder()
	router.ServeHTTP(taskResponse, taskRequest)
	if taskResponse.Code != http.StatusCreated {
		t.Fatalf("cleanup task status=%d body=%s", taskResponse.Code, taskResponse.Body.String())
	}
	var aggregate persistence.CleanupTaskAggregate
	if err := json.Unmarshal(taskResponse.Body.Bytes(), &aggregate); err != nil {
		t.Fatal(err)
	}

	executeRequest := httptest.NewRequest(http.MethodPost, "/api/cleanup/"+string(aggregate.Task.ID)+"/executions?connection_id=connection-a", bytes.NewBufferString(`{"idempotency_key":"http-request","confirmation":{"acknowledged":true}}`))
	executeRequest.Header.Set("Authorization", "Bearer operator-token")
	executeResponse := httptest.NewRecorder()
	router.ServeHTTP(executeResponse, executeRequest)
	if executeResponse.Code != http.StatusAccepted {
		t.Fatalf("execution status=%d body=%s", executeResponse.Code, executeResponse.Body.String())
	}
	var attempt execution.ExecutionAttempt
	if err := json.Unmarshal(executeResponse.Body.Bytes(), &attempt); err != nil || attempt.Status != execution.ExecutionPending || attempt.RequestedBy != "alice" {
		t.Fatalf("attempt=%+v err=%v", attempt, err)
	}
	jobs, err := repositories.Jobs().ListJobsByAggregate(context.Background(), "cleanup_task", string(aggregate.Task.ID))
	if err != nil || len(jobs) != 1 || jobs[0].TargetKey != "asset-a" || jobs[0].Payload["request_id"] != executeResponse.Header().Get(requestIDHeader) {
		t.Fatalf("cleanup jobs=%+v err=%v", jobs, err)
	}
	if err := repositories.Jobs().AppendLog(context.Background(), execution.JobLog{
		ID:            "cleanup-log-1",
		JobID:         jobs[0].ID,
		AggregateType: "cleanup_task",
		AggregateID:   string(aggregate.Task.ID),
		TargetKey:     "asset-a",
		Sequence:      1,
		Kind:          execution.JobLogText,
		Level:         "info",
		Message:       "provider preflight started",
		CreatedAt:     attempt.CreatedAt.Add(time.Second),
	}); err != nil {
		t.Fatal(err)
	}
	logRequest := httptest.NewRequest(http.MethodGet, "/api/cleanup/"+string(aggregate.Task.ID)+"/logs?connection_id=connection-a", nil)
	logRequest.Header.Set("Authorization", "Bearer viewer-token")
	logResponse := httptest.NewRecorder()
	router.ServeHTTP(logResponse, logRequest)
	if logResponse.Code != http.StatusOK || !strings.Contains(logResponse.Body.String(), `"target_key":"asset-a"`) || !strings.Contains(logResponse.Body.String(), `"message":"provider preflight started"`) {
		t.Fatalf("cleanup logs status=%d body=%s", logResponse.Code, logResponse.Body.String())
	}
	filteredLogRequest := httptest.NewRequest(http.MethodGet, "/api/cleanup/"+string(aggregate.Task.ID)+"/logs?connection_id=connection-a&resource_id=i-a&resource_kind_id=kind-a", nil)
	filteredLogRequest.Header.Set("Authorization", "Bearer viewer-token")
	filteredLogResponse := httptest.NewRecorder()
	router.ServeHTTP(filteredLogResponse, filteredLogRequest)
	if filteredLogResponse.Code != http.StatusOK || !strings.Contains(filteredLogResponse.Body.String(), `"target_key":"asset-a"`) {
		t.Fatalf("filtered cleanup logs status=%d body=%s", filteredLogResponse.Code, filteredLogResponse.Body.String())
	}
	missingLogRequest := httptest.NewRequest(http.MethodGet, "/api/cleanup/"+string(aggregate.Task.ID)+"/logs?connection_id=connection-a&resource_id=missing", nil)
	missingLogRequest.Header.Set("Authorization", "Bearer viewer-token")
	missingLogResponse := httptest.NewRecorder()
	router.ServeHTTP(missingLogResponse, missingLogRequest)
	if missingLogResponse.Code != http.StatusOK || !strings.Contains(missingLogResponse.Body.String(), `"items":[]`) {
		t.Fatalf("missing cleanup logs status=%d body=%s", missingLogResponse.Code, missingLogResponse.Body.String())
	}
	action := execution.ActionAttempt{
		ID: "action-http-1", ExecutionID: attempt.ID, CleanupTaskStepID: string(aggregate.Steps[0].ID),
		AssetID: "asset-a", Action: "delete", Status: execution.ActionSucceeded,
		IdempotencyKey: "action-http-key", SpecBundleRevision: "bundle", SpecHash: "hash",
		CreatedAt: attempt.CreatedAt, UpdatedAt: attempt.CreatedAt,
	}
	if err := repositories.Executions().AppendAction(context.Background(), action); err != nil {
		t.Fatal(err)
	}
	actionRequest := httptest.NewRequest(http.MethodGet, "/api/execution-attempts/"+string(attempt.ID)+"/actions?connection_id=connection-a", nil)
	actionRequest.Header.Set("Authorization", "Bearer viewer-token")
	actionResponse := httptest.NewRecorder()
	router.ServeHTTP(actionResponse, actionRequest)
	if actionResponse.Code != http.StatusOK || !strings.Contains(actionResponse.Body.String(), `"asset_id":"asset-a"`) {
		t.Fatalf("cleanup actions status=%d body=%s", actionResponse.Code, actionResponse.Body.String())
	}
	aggregate.Task.Status = plan.StatusCompleted
	if err := repositories.CleanupTasks().UpdateTask(context.Background(), aggregate.Task); err != nil {
		t.Fatal(err)
	}
	eventRequest := httptest.NewRequest(http.MethodGet, "/api/cleanup/"+string(aggregate.Task.ID)+"/events?connection_id=connection-a&resource_id=i-a&resource_kind_id=kind-a", nil)
	eventRequest.Header.Set("Authorization", "Bearer viewer-token")
	eventResponse := httptest.NewRecorder()
	router.ServeHTTP(eventResponse, eventRequest)
	body := eventResponse.Body.String()
	if eventResponse.Code != http.StatusOK ||
		strings.Index(body, "event: snapshot") < 0 ||
		strings.Index(body, "event: log") <= strings.Index(body, "event: snapshot") ||
		strings.Index(body, "event: end") <= strings.Index(body, "event: log") {
		t.Fatalf("cleanup events status=%d body=%s", eventResponse.Code, body)
	}
}

func TestFailedCleanupExecutionCanContinue(t *testing.T) {
	repositories, router := terminalRouter(t)
	taskRequest := httptest.NewRequest(http.MethodPost, "/api/cleanup?connection_id=connection-a", bytes.NewBufferString(`{"selectors":[{"kind":"asset","asset_id":"asset-a"}]}`))
	taskRequest.Header.Set("Authorization", "Bearer operator-token")
	taskResponse := httptest.NewRecorder()
	router.ServeHTTP(taskResponse, taskRequest)
	if taskResponse.Code != http.StatusCreated {
		t.Fatalf("cleanup task status=%d body=%s", taskResponse.Code, taskResponse.Body.String())
	}
	var aggregate persistence.CleanupTaskAggregate
	if err := json.Unmarshal(taskResponse.Body.Bytes(), &aggregate); err != nil {
		t.Fatal(err)
	}

	executeRequest := httptest.NewRequest(http.MethodPost, "/api/cleanup/"+string(aggregate.Task.ID)+"/executions?connection_id=connection-a", bytes.NewBufferString(`{"idempotency_key":"initial-execution","concurrency":42,"confirmation":{"acknowledged":true}}`))
	executeRequest.Header.Set("Authorization", "Bearer operator-token")
	executeResponse := httptest.NewRecorder()
	router.ServeHTTP(executeResponse, executeRequest)
	if executeResponse.Code != http.StatusAccepted {
		t.Fatalf("execution status=%d body=%s", executeResponse.Code, executeResponse.Body.String())
	}
	var attempt execution.ExecutionAttempt
	if err := json.Unmarshal(executeResponse.Body.Bytes(), &attempt); err != nil {
		t.Fatal(err)
	}
	if attempt.Concurrency != 42 {
		t.Fatalf("execution concurrency=%d, want 42", attempt.Concurrency)
	}

	failedAt := attempt.CreatedAt.Add(time.Second)
	attempt.Status = execution.ExecutionFailed
	attempt.StartedAt = &attempt.CreatedAt
	attempt.FinishedAt = &failedAt
	attempt.FailureReason = "provider rejected the request"
	if err := repositories.Executions().UpdateExecution(context.Background(), attempt); err != nil {
		t.Fatal(err)
	}
	aggregate.Task.Status = plan.StatusFailed
	if err := repositories.CleanupTasks().UpdateTask(context.Background(), aggregate.Task); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Executions().AppendAction(context.Background(), execution.ActionAttempt{
		ID: "action-http-failed", ExecutionID: attempt.ID, CleanupTaskStepID: string(aggregate.Steps[0].ID),
		AssetID: "asset-a", Action: "delete", Status: execution.ActionFailed, FailedFrom: execution.ActionInvoking,
		IdempotencyKey: "action-http-failed-key", SpecBundleRevision: "bundle", SpecHash: "hash",
		ProviderError: &execution.ProviderError{Category: execution.ErrorUnsupported, Message: "provider rejected the request"},
		CreatedAt:     attempt.CreatedAt, UpdatedAt: failedAt, FinishedAt: &failedAt,
	}); err != nil {
		t.Fatal(err)
	}

	continueRequest := httptest.NewRequest(http.MethodPost, "/api/cleanup/"+string(aggregate.Task.ID)+"/continue?connection_id=connection-a", bytes.NewBufferString(`{"idempotency_key":"continue-execution","concurrency":37}`))
	continueRequest.Header.Set("Authorization", "Bearer operator-token")
	continueResponse := httptest.NewRecorder()
	router.ServeHTTP(continueResponse, continueRequest)
	if continueResponse.Code != http.StatusAccepted {
		t.Fatalf("continue status=%d body=%s", continueResponse.Code, continueResponse.Body.String())
	}
	var continued execution.ExecutionAttempt
	if err := json.Unmarshal(continueResponse.Body.Bytes(), &continued); err != nil ||
		continued.ID != attempt.ID ||
		continued.Status != execution.ExecutionRunning ||
		continued.Concurrency != 37 ||
		continued.ContinueCount != 1 {
		t.Fatalf("continued=%+v err=%v", continued, err)
	}
	actions, err := repositories.Executions().ListActions(context.Background(), attempt.ID)
	if err != nil || len(actions) != 1 ||
		actions[0].Status != execution.ActionPending ||
		actions[0].ResumeStatus != execution.ActionInvoking ||
		actions[0].ProviderError != nil {
		t.Fatalf("continued actions=%+v err=%v", actions, err)
	}
	jobs, err := repositories.Jobs().ListJobsByAggregate(context.Background(), "cleanup_task", string(aggregate.Task.ID))
	if err != nil || len(jobs) != 2 {
		t.Fatalf("continued jobs=%+v err=%v", jobs, err)
	}
}

func TestCleanupExecutionCanPauseAndResume(t *testing.T) {
	repositories, router := terminalRouter(t)
	taskRequest := httptest.NewRequest(http.MethodPost, "/api/cleanup?connection_id=connection-a", bytes.NewBufferString(`{"selectors":[{"kind":"asset","asset_id":"asset-a"}]}`))
	taskRequest.Header.Set("Authorization", "Bearer operator-token")
	taskResponse := httptest.NewRecorder()
	router.ServeHTTP(taskResponse, taskRequest)
	if taskResponse.Code != http.StatusCreated {
		t.Fatalf("cleanup task status=%d body=%s", taskResponse.Code, taskResponse.Body.String())
	}
	var aggregate persistence.CleanupTaskAggregate
	if err := json.Unmarshal(taskResponse.Body.Bytes(), &aggregate); err != nil {
		t.Fatal(err)
	}

	executeRequest := httptest.NewRequest(http.MethodPost, "/api/cleanup/"+string(aggregate.Task.ID)+"/executions?connection_id=connection-a", bytes.NewBufferString(`{"idempotency_key":"pause-execution","confirmation":{"acknowledged":true}}`))
	executeRequest.Header.Set("Authorization", "Bearer operator-token")
	executeResponse := httptest.NewRecorder()
	router.ServeHTTP(executeResponse, executeRequest)
	if executeResponse.Code != http.StatusAccepted {
		t.Fatalf("execution status=%d body=%s", executeResponse.Code, executeResponse.Body.String())
	}

	pauseRequest := httptest.NewRequest(http.MethodPost, "/api/cleanup/"+string(aggregate.Task.ID)+"/pause?connection_id=connection-a", nil)
	pauseRequest.Header.Set("Authorization", "Bearer operator-token")
	pauseResponse := httptest.NewRecorder()
	router.ServeHTTP(pauseResponse, pauseRequest)
	var paused execution.ExecutionAttempt
	if err := json.Unmarshal(pauseResponse.Body.Bytes(), &paused); err != nil ||
		pauseResponse.Code != http.StatusAccepted ||
		paused.Status != execution.ExecutionPaused {
		t.Fatalf("paused=%+v status=%d body=%s err=%v", paused, pauseResponse.Code, pauseResponse.Body.String(), err)
	}
	jobs, err := repositories.Jobs().ListJobsByAggregate(context.Background(), "cleanup_task", string(aggregate.Task.ID))
	if err != nil || len(jobs) != 1 || jobs[0].Status != execution.JobPaused {
		t.Fatalf("paused jobs=%+v err=%v", jobs, err)
	}

	resumeRequest := httptest.NewRequest(http.MethodPost, "/api/cleanup/"+string(aggregate.Task.ID)+"/resume?connection_id=connection-a", nil)
	resumeRequest.Header.Set("Authorization", "Bearer operator-token")
	resumeResponse := httptest.NewRecorder()
	router.ServeHTTP(resumeResponse, resumeRequest)
	var resumed execution.ExecutionAttempt
	if err := json.Unmarshal(resumeResponse.Body.Bytes(), &resumed); err != nil ||
		resumeResponse.Code != http.StatusAccepted ||
		resumed.Status != execution.ExecutionPending {
		t.Fatalf("resumed=%+v status=%d body=%s err=%v", resumed, resumeResponse.Code, resumeResponse.Body.String(), err)
	}
	jobs, err = repositories.Jobs().ListJobsByAggregate(context.Background(), "cleanup_task", string(aggregate.Task.ID))
	if err != nil || len(jobs) != 1 || jobs[0].Status != execution.JobPending {
		t.Fatalf("resumed jobs=%+v err=%v", jobs, err)
	}
}

func TestExecutionCreationRejectsMissingBackendConfirmation(t *testing.T) {
	repositories, router := terminalRouter(t)
	taskRequest := httptest.NewRequest(http.MethodPost, "/api/cleanup?connection_id=connection-a", bytes.NewBufferString(`{"selectors":[{"kind":"asset","asset_id":"asset-a"}]}`))
	taskRequest.Header.Set("Authorization", "Bearer operator-token")
	taskResponse := httptest.NewRecorder()
	router.ServeHTTP(taskResponse, taskRequest)
	if taskResponse.Code != http.StatusCreated {
		t.Fatalf("cleanup task status=%d body=%s", taskResponse.Code, taskResponse.Body.String())
	}
	var aggregate persistence.CleanupTaskAggregate
	if err := json.Unmarshal(taskResponse.Body.Bytes(), &aggregate); err != nil {
		t.Fatal(err)
	}

	executeRequest := httptest.NewRequest(http.MethodPost, "/api/cleanup/"+string(aggregate.Task.ID)+"/executions?connection_id=connection-a", bytes.NewBufferString(`{"idempotency_key":"missing-confirmation"}`))
	executeRequest.Header.Set("Authorization", "Bearer operator-token")
	executeResponse := httptest.NewRecorder()
	router.ServeHTTP(executeResponse, executeRequest)
	if executeResponse.Code != http.StatusBadRequest || !strings.Contains(executeResponse.Body.String(), `"code":"cleanup.confirmation_invalid"`) {
		t.Fatalf("execution status=%d body=%s", executeResponse.Code, executeResponse.Body.String())
	}
	if _, err := repositories.Executions().GetExecutionByIdempotencyKey(context.Background(), "missing-confirmation"); !errors.Is(err, persistence.ErrNotFound) {
		t.Fatalf("unconfirmed execution was persisted: %v", err)
	}
}

func TestScanCreationReturnsAcceptedWithDurableJobs(t *testing.T) {
	repositories, router := terminalRouter(t)
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	if err := repositories.Regions().PutRegion(context.Background(), asset.ConnectionRegion{ID: "region-http", ConnectionID: "connection-a", RegionID: "cn-hangzhou", DiscoveredName: "华东 1（杭州）", Origin: asset.RegionOriginAPI, Lifecycle: asset.RegionActive, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/scans?connection_id=connection-a", bytes.NewBufferString(`{
		"scope_mode":"selected_regions",
		"region_ids":["cn-hangzhou"]
	}`))
	request.Header.Set("Authorization", "Bearer operator-token")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var result inventory.ScanTaskProjection
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Status != asset.ScanPending || result.RequestedBy != "alice" || len(result.Targets) != 1 || result.Targets[0].RegionName != "华东 1（杭州）" || len(result.TargetProgress) != 1 || result.TargetProgress[0].Status != asset.ScanPending || strings.Contains(response.Body.String(), "scan_shards") {
		t.Fatalf("result=%+v", result)
	}
	jobs, err := repositories.Jobs().ListJobsByAggregate(context.Background(), "scan_task", string(result.ID))
	if err != nil || len(jobs) != 1 || jobs[0].Type != execution.JobScan || jobs[0].TargetKey != "region:cn-hangzhou" || jobs[0].Payload["scan_shard_ids"] == nil {
		t.Fatalf("stored jobs=%+v err=%v", jobs, err)
	}
	audits, err := repositories.Audits().ListAuditEvents(context.Background(), persistence.ListOptions{Limit: 10})
	if err != nil || len(audits.Items) != 1 || audits.Items[0].Actor != "alice" || audits.Items[0].Action != "inventory.scan.create" {
		t.Fatalf("audits=%+v err=%v", audits, err)
	}
}

func TestNetworkTargetRoutesUseProviderDataAndLegacyScanRoutesAreGone(t *testing.T) {
	_, router := terminalRouter(t)
	request := httptest.NewRequest(http.MethodGet, "/api/scan-targets/vpcs?connection_id=connection-a&region_id=cn-hangzhou&query=vpc-live", nil)
	request.Header.Set("Authorization", "Bearer viewer-token")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"native_id":"vpc-live"`) || !strings.Contains(response.Body.String(), `"request_id":"provider-request"`) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	for _, path := range []string{"/api/scan-runs?connection_id=connection-a", "/api/scan-shards?connection_id=connection-a"} {
		legacyRequest := httptest.NewRequest(http.MethodGet, path, nil)
		legacyRequest.Header.Set("Authorization", "Bearer viewer-token")
		legacyResponse := httptest.NewRecorder()
		router.ServeHTTP(legacyResponse, legacyRequest)
		if legacyResponse.Code != http.StatusNotFound {
			t.Fatalf("legacy path %s status=%d body=%s", path, legacyResponse.Code, legacyResponse.Body.String())
		}
	}
}

func TestNetworkScanCreationSnapshotsProviderTarget(t *testing.T) {
	repositories, router := terminalRouter(t)
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	if err := repositories.Regions().PutRegion(context.Background(), asset.ConnectionRegion{ID: "region-network", ConnectionID: "connection-a", RegionID: "cn-hangzhou", DiscoveredName: "杭州", Origin: asset.RegionOriginAPI, Lifecycle: asset.RegionActive, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/scans?connection_id=connection-a", bytes.NewBufferString(`{
		"scope_mode":"selected_networks",
		"network_targets":[{"kind":"vpc","region_id":"cn-hangzhou","native_id":"vpc-live","name":"forged"}]
	}`))
	request.Header.Set("Authorization", "Bearer operator-token")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	var task inventory.ScanTaskProjection
	if err := json.Unmarshal(response.Body.Bytes(), &task); err != nil || response.Code != http.StatusAccepted || len(task.Targets) != 1 || task.Targets[0].Name != "Provider VPC" {
		t.Fatalf("status=%d task=%+v body=%s err=%v", response.Code, task, response.Body.String(), err)
	}
}

func TestScanEventStreamOrdersSnapshotAggregateLogsAndTerminalEnd(t *testing.T) {
	repositories, router := terminalRouter(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	if err := repositories.Regions().PutRegion(ctx, asset.ConnectionRegion{ID: "region-events", ConnectionID: "connection-a", RegionID: "cn-hangzhou", DiscoveredName: "杭州", Origin: asset.RegionOriginAPI, Lifecycle: asset.RegionActive, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	createRequest := httptest.NewRequest(http.MethodPost, "/api/scans?connection_id=connection-a", bytes.NewBufferString(`{"scope_mode":"selected_regions","region_ids":["cn-hangzhou"]}`))
	createRequest.Header.Set("Authorization", "Bearer operator-token")
	createResponse := httptest.NewRecorder()
	router.ServeHTTP(createResponse, createRequest)
	var task inventory.ScanTaskProjection
	if err := json.Unmarshal(createResponse.Body.Bytes(), &task); err != nil || createResponse.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s err=%v", createResponse.Code, createResponse.Body.String(), err)
	}
	stored, err := repositories.Inventory().GetScanRun(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	finishedAt := now.Add(time.Minute)
	stored.Status = asset.ScanSucceeded
	stored.FinishedAt = &finishedAt
	if err := repositories.Inventory().PutScanRun(ctx, stored); err != nil {
		t.Fatal(err)
	}
	shards, err := repositories.Inventory().ListScanShardsByRun(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, shard := range shards {
		shard.Status = asset.ShardSucceeded
		shard.Coverage.Complete = true
		shard.FinishedAt = &finishedAt
		if err := repositories.Inventory().PutScanShard(ctx, shard); err != nil {
			t.Fatal(err)
		}
	}
	if err := repositories.Jobs().AppendLog(ctx, execution.JobLog{
		ID: "log-scan-events", JobID: createdJobID(t, repositories, task.ID), AggregateType: "scan_task", AggregateID: string(task.ID),
		TargetKey: "region:cn-hangzhou", Sequence: 1, Kind: execution.JobLogText, Level: "info", Message: "region completed", CreatedAt: finishedAt,
	}); err != nil {
		t.Fatal(err)
	}
	eventRequest := httptest.NewRequest(http.MethodGet, "/api/scans/"+string(task.ID)+"/events?connection_id=connection-a", nil)
	eventRequest.Header.Set("Authorization", "Bearer viewer-token")
	eventResponse := httptest.NewRecorder()
	router.ServeHTTP(eventResponse, eventRequest)
	body := eventResponse.Body.String()
	snapshotIndex := strings.Index(body, "event: snapshot")
	logIndex := strings.Index(body, "event: log")
	endIndex := strings.Index(body, "event: end")
	if eventResponse.Code != http.StatusOK || snapshotIndex < 0 || logIndex <= snapshotIndex || endIndex <= logIndex || !strings.Contains(body, `"message":"region completed"`) {
		t.Fatalf("status=%d body=%q", eventResponse.Code, body)
	}
}

func TestScanLogHistoryReturnsLatestPageAndOlderCursor(t *testing.T) {
	repositories, router := terminalRouter(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 13, 12, 2, 0, 0, time.UTC)
	if err := repositories.Regions().PutRegion(ctx, asset.ConnectionRegion{ID: "region-log-history", ConnectionID: "connection-a", RegionID: "cn-hangzhou", DiscoveredName: "杭州", Origin: asset.RegionOriginAPI, Lifecycle: asset.RegionActive, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	createRequest := httptest.NewRequest(http.MethodPost, "/api/scans?connection_id=connection-a", bytes.NewBufferString(`{"scope_mode":"selected_regions","region_ids":["cn-hangzhou"]}`))
	createRequest.Header.Set("Authorization", "Bearer operator-token")
	createResponse := httptest.NewRecorder()
	router.ServeHTTP(createResponse, createRequest)
	var task inventory.ScanTaskProjection
	if err := json.Unmarshal(createResponse.Body.Bytes(), &task); err != nil || createResponse.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s err=%v", createResponse.Code, createResponse.Body.String(), err)
	}
	jobID := createdJobID(t, repositories, task.ID)
	for index := 1; index <= 3; index++ {
		if err := repositories.Jobs().AppendLog(ctx, execution.JobLog{
			ID: fmt.Sprintf("scan-history-%d", index), JobID: jobID,
			AggregateType: "scan_task", AggregateID: string(task.ID),
			Sequence: int64(index), Kind: execution.JobLogText, Level: "info", Message: fmt.Sprintf("log %d", index),
			CreatedAt: now.Add(time.Duration(index) * time.Second),
		}); err != nil {
			t.Fatal(err)
		}
	}

	firstRequest := httptest.NewRequest(http.MethodGet, "/api/scans/"+string(task.ID)+"/logs?connection_id=connection-a&limit=2", nil)
	firstRequest.Header.Set("Authorization", "Bearer viewer-token")
	firstResponse := httptest.NewRecorder()
	router.ServeHTTP(firstResponse, firstRequest)
	var first struct {
		Items      []execution.JobLog `json:"items"`
		NextCursor string             `json:"next_cursor"`
		LiveCursor string             `json:"live_cursor"`
	}
	if err := json.Unmarshal(firstResponse.Body.Bytes(), &first); err != nil ||
		firstResponse.Code != http.StatusOK || len(first.Items) != 2 ||
		first.Items[0].Message != "log 2" || first.Items[1].Message != "log 3" ||
		first.NextCursor == "" || first.LiveCursor == "" {
		t.Fatalf("status=%d page=%+v body=%s err=%v", firstResponse.Code, first, firstResponse.Body.String(), err)
	}

	secondRequest := httptest.NewRequest(http.MethodGet, "/api/scans/"+string(task.ID)+"/logs?connection_id=connection-a&limit=2&before="+first.NextCursor, nil)
	secondRequest.Header.Set("Authorization", "Bearer viewer-token")
	secondResponse := httptest.NewRecorder()
	router.ServeHTTP(secondResponse, secondRequest)
	var second struct {
		Items      []execution.JobLog `json:"items"`
		NextCursor string             `json:"next_cursor"`
		LiveCursor string             `json:"live_cursor"`
	}
	if err := json.Unmarshal(secondResponse.Body.Bytes(), &second); err != nil ||
		secondResponse.Code != http.StatusOK || len(second.Items) != 1 ||
		second.Items[0].Message != "log 1" || second.NextCursor != "" || second.LiveCursor != "" {
		t.Fatalf("status=%d page=%+v body=%s err=%v", secondResponse.Code, second, secondResponse.Body.String(), err)
	}
}

func TestScanLogHistoryAndEventsFilterByTarget(t *testing.T) {
	repositories, router := terminalRouter(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 13, 12, 4, 0, 0, time.UTC)
	for _, region := range []asset.ConnectionRegion{
		{ID: "region-filter-hangzhou", ConnectionID: "connection-a", RegionID: "cn-hangzhou", DiscoveredName: "杭州", Origin: asset.RegionOriginAPI, Lifecycle: asset.RegionActive, CreatedAt: now, UpdatedAt: now},
		{ID: "region-filter-west", ConnectionID: "connection-a", RegionID: "us-west-1", DiscoveredName: "硅谷", Origin: asset.RegionOriginAPI, Lifecycle: asset.RegionActive, CreatedAt: now, UpdatedAt: now},
	} {
		if err := repositories.Regions().PutRegion(ctx, region); err != nil {
			t.Fatal(err)
		}
	}
	createRequest := httptest.NewRequest(http.MethodPost, "/api/scans?connection_id=connection-a", bytes.NewBufferString(`{"scope_mode":"selected_regions","region_ids":["cn-hangzhou","us-west-1"]}`))
	createRequest.Header.Set("Authorization", "Bearer operator-token")
	createResponse := httptest.NewRecorder()
	router.ServeHTTP(createResponse, createRequest)
	var task inventory.ScanTaskProjection
	if err := json.Unmarshal(createResponse.Body.Bytes(), &task); err != nil || createResponse.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s err=%v", createResponse.Code, createResponse.Body.String(), err)
	}
	jobs, err := repositories.Jobs().ListJobsByAggregate(ctx, "scan_task", string(task.ID))
	if err != nil || len(jobs) != 2 {
		t.Fatalf("jobs=%+v err=%v", jobs, err)
	}
	jobIDs := map[string]execution.JobID{}
	for _, job := range jobs {
		jobIDs[job.TargetKey] = job.ID
	}
	for index, event := range []execution.JobLog{
		{ID: "filter-hangzhou", JobID: jobIDs["region:cn-hangzhou"], TargetKey: "region:cn-hangzhou", Message: "hangzhou completed"},
		{ID: "filter-west", JobID: jobIDs["region:us-west-1"], TargetKey: "region:us-west-1", Message: "west completed"},
	} {
		event.AggregateType = "scan_task"
		event.AggregateID = string(task.ID)
		event.Sequence = 1
		event.Kind = execution.JobLogText
		event.Level = "info"
		event.CreatedAt = now.Add(time.Duration(index+1) * time.Second)
		if err := repositories.Jobs().AppendLog(ctx, event); err != nil {
			t.Fatal(err)
		}
	}

	historyRequest := httptest.NewRequest(http.MethodGet, "/api/scans/"+string(task.ID)+"/logs?connection_id=connection-a&target_key=region:us-west-1", nil)
	historyRequest.Header.Set("Authorization", "Bearer viewer-token")
	historyResponse := httptest.NewRecorder()
	router.ServeHTTP(historyResponse, historyRequest)
	var history struct {
		Items []execution.JobLog `json:"items"`
	}
	if err := json.Unmarshal(historyResponse.Body.Bytes(), &history); err != nil ||
		historyResponse.Code != http.StatusOK ||
		len(history.Items) != 1 ||
		history.Items[0].TargetKey != "region:us-west-1" {
		t.Fatalf("status=%d history=%+v body=%s err=%v", historyResponse.Code, history, historyResponse.Body.String(), err)
	}

	unknownRequest := httptest.NewRequest(http.MethodGet, "/api/scans/"+string(task.ID)+"/logs?connection_id=connection-a&target_key=region:missing", nil)
	unknownRequest.Header.Set("Authorization", "Bearer viewer-token")
	unknownResponse := httptest.NewRecorder()
	router.ServeHTTP(unknownResponse, unknownRequest)
	if unknownResponse.Code != http.StatusBadRequest {
		t.Fatalf("unknown target status=%d body=%s", unknownResponse.Code, unknownResponse.Body.String())
	}

	stored, err := repositories.Inventory().GetScanRun(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	finishedAt := now.Add(time.Minute)
	stored.Status = asset.ScanSucceeded
	stored.FinishedAt = &finishedAt
	if err := repositories.Inventory().PutScanRun(ctx, stored); err != nil {
		t.Fatal(err)
	}
	shards, err := repositories.Inventory().ListScanShardsByRun(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, shard := range shards {
		shard.Status = asset.ShardSucceeded
		shard.Coverage.Complete = true
		shard.FinishedAt = &finishedAt
		if err := repositories.Inventory().PutScanShard(ctx, shard); err != nil {
			t.Fatal(err)
		}
	}

	eventRequest := httptest.NewRequest(http.MethodGet, "/api/scans/"+string(task.ID)+"/events?connection_id=connection-a&target_key=region:us-west-1", nil)
	eventRequest.Header.Set("Authorization", "Bearer viewer-token")
	eventResponse := httptest.NewRecorder()
	router.ServeHTTP(eventResponse, eventRequest)
	if eventResponse.Code != http.StatusOK ||
		!strings.Contains(eventResponse.Body.String(), `"message":"west completed"`) ||
		strings.Contains(eventResponse.Body.String(), `"message":"hangzhou completed"`) {
		t.Fatalf("status=%d body=%s", eventResponse.Code, eventResponse.Body.String())
	}
}

func TestScanEventStreamEmitsChangedRunningSnapshotBeforeTerminalEnd(t *testing.T) {
	repositories, router := terminalRouter(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 13, 12, 5, 0, 0, time.UTC)
	if err := repositories.Regions().PutRegion(ctx, asset.ConnectionRegion{ID: "region-live-events", ConnectionID: "connection-a", RegionID: "cn-hangzhou", DiscoveredName: "杭州", Origin: asset.RegionOriginAPI, Lifecycle: asset.RegionActive, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	createRequest := httptest.NewRequest(http.MethodPost, "/api/scans?connection_id=connection-a", bytes.NewBufferString(`{"scope_mode":"selected_regions","region_ids":["cn-hangzhou"]}`))
	createRequest.Header.Set("Authorization", "Bearer operator-token")
	createResponse := httptest.NewRecorder()
	router.ServeHTTP(createResponse, createRequest)
	var task inventory.ScanTaskProjection
	if err := json.Unmarshal(createResponse.Body.Bytes(), &task); err != nil || createResponse.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s err=%v", createResponse.Code, createResponse.Body.String(), err)
	}
	stored, err := repositories.Inventory().GetScanRun(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	stored.Status = asset.ScanRunning
	if err := repositories.Inventory().PutScanRun(ctx, stored); err != nil {
		t.Fatal(err)
	}
	shards, err := repositories.Inventory().ListScanShardsByRun(ctx, task.ID)
	if err != nil || len(shards) == 0 {
		t.Fatalf("shards=%+v err=%v", shards, err)
	}
	for _, shard := range shards {
		shard.Status = asset.ShardRunning
		if err := repositories.Inventory().PutScanShard(ctx, shard); err != nil {
			t.Fatal(err)
		}
	}

	go func() {
		time.Sleep(10 * time.Millisecond)
		currentShards, _ := repositories.Inventory().ListScanShardsByRun(ctx, task.ID)
		currentShards[0].Coverage.ItemCount = 1
		_ = repositories.Inventory().PutScanShard(ctx, currentShards[0])
		time.Sleep(10 * time.Millisecond)
		finishedAt := now.Add(time.Minute)
		for _, shard := range currentShards {
			shard.Status = asset.ShardSucceeded
			shard.Coverage.Complete = true
			shard.FinishedAt = &finishedAt
			_ = repositories.Inventory().PutScanShard(ctx, shard)
		}
		current, _ := repositories.Inventory().GetScanRun(ctx, task.ID)
		current.Status = asset.ScanSucceeded
		current.FinishedAt = &finishedAt
		_ = repositories.Inventory().PutScanRun(ctx, current)
	}()

	eventRequest := httptest.NewRequest(http.MethodGet, "/api/scans/"+string(task.ID)+"/events?connection_id=connection-a", nil)
	eventRequest.Header.Set("Authorization", "Bearer viewer-token")
	eventResponse := httptest.NewRecorder()
	router.ServeHTTP(eventResponse, eventRequest)
	body := eventResponse.Body.String()
	endIndex := strings.Index(body, "event: end")
	if endIndex < 0 {
		t.Fatalf("stream has no terminal end: status=%d body=%q", eventResponse.Code, body)
	}
	if eventResponse.Code != http.StatusOK || strings.Count(body[:endIndex], "event: snapshot") < 2 || !strings.Contains(body[:endIndex], `"resource_count":1`) {
		t.Fatalf("status=%d body=%q", eventResponse.Code, body)
	}
}

func createdJobID(t *testing.T, repositories persistence.Repositories, taskID asset.ScanTaskID) execution.JobID {
	t.Helper()
	jobs, err := repositories.Jobs().ListJobsByAggregate(context.Background(), "scan_task", string(taskID))
	if err != nil || len(jobs) != 1 {
		t.Fatalf("jobs=%+v err=%v", jobs, err)
	}
	return jobs[0].ID
}

func TestConnectionCreationUsesAuthenticatedAdminAsAuditActor(t *testing.T) {
	repositories, router := terminalRouter(t)
	request := httptest.NewRequest(http.MethodPost, "/api/connections", bytes.NewBufferString(`{
		"name":"production","provider":"alicloud","site":"cn",
		"credential":{"type":"access_key","values":{"access_key_id":"id","access_key_secret":"do-not-return"}}
	}`))
	request.Header.Set("Authorization", "Bearer admin-token")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "do-not-return") || strings.Contains(response.Body.String(), "access_key_secret") {
		t.Fatalf("connection response exposed credential material: %s", response.Body.String())
	}
	audits, err := repositories.Audits().ListAuditEvents(context.Background(), persistence.ListOptions{Limit: 10})
	if err != nil || len(audits.Items) != 1 || audits.Items[0].Actor != "carol" || audits.Items[0].Action != "connection.create" || audits.Items[0].RequestID != response.Header().Get(requestIDHeader) {
		t.Fatalf("audits=%+v err=%v", audits, err)
	}
	scopes, err := repositories.Inventory().ListScopes(context.Background(), persistence.ListOptions{Limit: 10})
	createdScope := false
	for _, scope := range scopes.Items {
		if scope.Kind == asset.ScopeAccount && scope.NativeID == "1234567890123456" && scope.ConnectionID != "" {
			createdScope = true
		}
	}
	if err != nil || !createdScope {
		t.Fatalf("root scopes=%+v err=%v", scopes, err)
	}
}

func TestConnectionCreationEnforcesAndReturnsProviderSite(t *testing.T) {
	for _, test := range []struct {
		name       string
		body       string
		wantStatus int
		wantCode   string
		wantSite   asset.ConnectionSite
	}{
		{
			name:       "alicloud missing site",
			body:       `{"name":"missing","provider":"alicloud","credential":{"type":"access_key","values":{"access_key_id":"id","access_key_secret":"secret"}}}`,
			wantStatus: http.StatusBadRequest,
			wantCode:   "invalid_connection_site",
		},
		{
			name:       "alicloud international",
			body:       `{"name":"international","provider":"alicloud","site":"intl","credential":{"type":"access_key","values":{"access_key_id":"id","access_key_secret":"secret"}}}`,
			wantStatus: http.StatusCreated,
			wantSite:   asset.ConnectionSiteINTL,
		},
		{
			name:       "aws rejects site",
			body:       `{"name":"aws","provider":"aws","site":"cn","credential":{"type":"access_key","values":{"access_key_id":"id","access_key_secret":"secret"}}}`,
			wantStatus: http.StatusBadRequest,
			wantCode:   "invalid_connection_site",
		},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			repositories, router := terminalRouter(t)
			request := httptest.NewRequest(http.MethodPost, "/api/connections", bytes.NewBufferString(test.body))
			request.Header.Set("Authorization", "Bearer admin-token")
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != test.wantStatus {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			if test.wantCode != "" {
				if !strings.Contains(response.Body.String(), `"code":"`+test.wantCode+`"`) {
					t.Fatalf("body=%s", response.Body.String())
				}
				return
			}
			var created asset.CloudConnection
			if err := json.Unmarshal(response.Body.Bytes(), &created); err != nil {
				t.Fatal(err)
			}
			if created.Site != test.wantSite {
				t.Fatalf("created.Site = %q, want %q", created.Site, test.wantSite)
			}
			getRequest := httptest.NewRequest(http.MethodGet, "/api/connections?limit=100", nil)
			getRequest.Header.Set("Authorization", "Bearer viewer-token")
			getResponse := httptest.NewRecorder()
			router.ServeHTTP(getResponse, getRequest)
			if getResponse.Code != http.StatusOK || !strings.Contains(getResponse.Body.String(), `"site":"intl"`) {
				t.Fatalf("GET status=%d body=%s", getResponse.Code, getResponse.Body.String())
			}
			stored, err := repositories.Connections().GetConnection(context.Background(), created.ID)
			if err != nil || stored.Site != test.wantSite {
				t.Fatalf("stored = %#v, err = %v", stored, err)
			}
		})
	}
}

func TestConnectionValidationRequiresAdminAndActivatesStoredCredential(t *testing.T) {
	_, router := terminalRouter(t)
	createRequest := httptest.NewRequest(http.MethodPost, "/api/connections", bytes.NewBufferString(`{
		"name":"offline","provider":"alicloud","site":"cn",
		"credential":{"type":"access_key","values":{"access_key_id":"id","access_key_secret":"do-not-return"}}
	}`))
	createRequest.Header.Set("Authorization", "Bearer admin-token")
	createResponse := httptest.NewRecorder()
	router.ServeHTTP(createResponse, createRequest)
	if createResponse.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", createResponse.Code, createResponse.Body.String())
	}
	var created asset.CloudConnection
	if err := json.Unmarshal(createResponse.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.Status != asset.ConnectionUnverified {
		t.Fatalf("created connection = %+v", created)
	}

	viewerRequest := httptest.NewRequest(http.MethodPost, "/api/connections/"+string(created.ID)+"/validate", nil)
	viewerRequest.Header.Set("Authorization", "Bearer viewer-token")
	viewerResponse := httptest.NewRecorder()
	router.ServeHTTP(viewerResponse, viewerRequest)
	if viewerResponse.Code != http.StatusForbidden {
		t.Fatalf("viewer validation status=%d body=%s", viewerResponse.Code, viewerResponse.Body.String())
	}

	adminRequest := httptest.NewRequest(http.MethodPost, "/api/connections/"+string(created.ID)+"/validate", nil)
	adminRequest.Header.Set("Authorization", "Bearer admin-token")
	adminResponse := httptest.NewRecorder()
	router.ServeHTTP(adminResponse, adminRequest)
	if adminResponse.Code != http.StatusOK || !strings.Contains(adminResponse.Body.String(), `"status":"active"`) {
		t.Fatalf("admin validation status=%d body=%s", adminResponse.Code, adminResponse.Body.String())
	}
	if strings.Contains(adminResponse.Body.String(), "do-not-return") || strings.Contains(adminResponse.Body.String(), "access_key_secret") {
		t.Fatalf("validation response exposed credential material: %s", adminResponse.Body.String())
	}
}

func TestConnectionValidationReturnsRawProviderDiagnosticsAndSanitizesAudit(t *testing.T) {
	providerErr := &contracts.ProviderCallError{Provider: execution.ProviderError{
		Category: execution.ErrorInvalidRequest,
		Code:     "InvalidAccessKeyId.NotFound", Message: "The Access Key ID is inactive.",
		RequestID: "provider-request",
	}}
	repositories, router := terminalRouterWithValidator(t, testConnectionValidator{err: providerErr})
	createRequest := httptest.NewRequest(http.MethodPost, "/api/connections", bytes.NewBufferString(`{
		"name":"invalid","provider":"alicloud","site":"cn",
		"credential":{"type":"access_key","values":{"access_key_id":"id","access_key_secret":"do-not-return"}}
	}`))
	createRequest.Header.Set("Authorization", "Bearer admin-token")
	createResponse := httptest.NewRecorder()
	router.ServeHTTP(createResponse, createRequest)
	var created asset.CloudConnection
	if err := json.Unmarshal(createResponse.Body.Bytes(), &created); err != nil || createResponse.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s err=%v", createResponse.Code, createResponse.Body.String(), err)
	}

	request := httptest.NewRequest(http.MethodPost, "/api/connections/"+string(created.ID)+"/validate", nil)
	request.Header.Set("Authorization", "Bearer admin-token")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("validation status=%d body=%s", response.Code, response.Body.String())
	}
	var body struct {
		Error httptransport.APIError `json:"error"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Error.Code != "credential_validation_failed" ||
		body.Error.Details["category"] != "invalid_request" ||
		body.Error.Details["provider_code"] != "InvalidAccessKeyId.NotFound" ||
		body.Error.Details["provider_message"] != "The Access Key ID is inactive." ||
		body.Error.Details["provider_request_id"] != "provider-request" ||
		body.Error.RequestID == "" {
		t.Fatalf("validation error = %#v", body.Error)
	}
	if strings.Contains(response.Body.String(), "do-not-return") || strings.Contains(response.Body.String(), "access_key_secret") {
		t.Fatalf("validation error exposed credential material: %s", response.Body.String())
	}
	audits, err := repositories.Audits().ListAuditEvents(context.Background(), persistence.ListOptions{
		ConnectionID: created.ID,
		Limit:        10,
	})
	encoded, _ := json.Marshal(audits.Items)
	if err != nil || !strings.Contains(string(encoded), `"error_message":"`+contracts.SafeProviderValidationMessage+`"`) {
		t.Fatalf("validation audits were not sanitized: %s, err=%v", encoded, err)
	}
}

func TestConnectionValidationPreservesSignedTransportErrorOnlyInResponse(t *testing.T) {
	providerErr := &contracts.ProviderCallError{Provider: execution.ProviderError{
		Category: execution.ErrorProviderFailure,
		Message:  `Post "https://sts.example.test/?AccessKeyId=sensitive-key&Signature=sensitive-signature": dial tcp: connection refused`,
	}}
	repositories, router := terminalRouterWithValidator(t, testConnectionValidator{err: providerErr})
	createRequest := httptest.NewRequest(http.MethodPost, "/api/connections", bytes.NewBufferString(`{
		"name":"transport-error","provider":"alicloud","site":"cn",
		"credential":{"type":"access_key","values":{"access_key_id":"id","access_key_secret":"do-not-return"}}
	}`))
	createRequest.Header.Set("Authorization", "Bearer admin-token")
	createResponse := httptest.NewRecorder()
	router.ServeHTTP(createResponse, createRequest)
	var created asset.CloudConnection
	if err := json.Unmarshal(createResponse.Body.Bytes(), &created); err != nil || createResponse.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s err=%v", createResponse.Code, createResponse.Body.String(), err)
	}

	request := httptest.NewRequest(http.MethodPost, "/api/connections/"+string(created.ID)+"/validate", nil)
	request.Header.Set("Authorization", "Bearer admin-token")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("validation status=%d body=%s", response.Code, response.Body.String())
	}
	var body struct {
		Error httptransport.APIError `json:"error"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Error.Details["provider_message"] != `Post "https://sts.example.test/?AccessKeyId=sensitive-key&Signature=sensitive-signature": dial tcp: connection refused` {
		t.Fatalf("validation response did not preserve the provider message: %#v", body.Error)
	}
	audits, err := repositories.Audits().ListAuditEvents(context.Background(), persistence.ListOptions{
		ConnectionID: created.ID,
		Limit:        10,
	})
	encoded, _ := json.Marshal(audits.Items)
	if err != nil ||
		!strings.Contains(string(encoded), `"error_message":"`+contracts.SafeProviderValidationMessage+`"`) ||
		strings.Contains(string(encoded), "sensitive-key") ||
		strings.Contains(string(encoded), "sensitive-signature") {
		t.Fatalf("validation audits were not sanitized: %s, err=%v", encoded, err)
	}
}

func TestConnectionValidationReportsUnavailableStoredCredential(t *testing.T) {
	repositories, router := terminalRouter(t)
	sealed, err := repositories.Credentials().GetCredential(context.Background(), "connection-a")
	if err != nil {
		t.Fatal(err)
	}
	sealed.Ciphertext = "not-valid-ciphertext"
	if err := repositories.Credentials().PutCredential(context.Background(), sealed); err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodPost, "/api/connections/connection-a/validate", nil)
	request.Header.Set("Authorization", "Bearer admin-token")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusUnprocessableEntity ||
		!strings.Contains(response.Body.String(), `"code":"credential_unavailable"`) ||
		strings.Contains(response.Body.String(), "not-valid-ciphertext") {
		t.Fatalf("validation status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestConnectionValidationRejectsInvalidLocalCredentialInput(t *testing.T) {
	_, router := terminalRouter(t)
	for _, test := range []struct {
		name string
		path string
		body string
	}{
		{name: "create", path: "/api/connections", body: `{"name":"","provider":"alicloud","credential":{"type":"access_key","values":{"access_key_id":"id"}}}`},
		{name: "replace", path: "/api/connections/connection-a/credential", body: `{}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			method := http.MethodPost
			if test.name == "replace" {
				method = http.MethodPut
			}
			request := httptest.NewRequest(method, test.path, bytes.NewBufferString(test.body))
			request.Header.Set("Authorization", "Bearer admin-token")
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), `"code":"invalid_credential_fields"`) {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
}

func TestConnectionValidationReturnsTypedLocalCredentialError(t *testing.T) {
	_, router := terminalRouterWithValidator(t, testConnectionValidator{err: contracts.NewCredentialValidationError(
		"credential_expired",
		"The cloud credential has expired.",
		nil,
	)})
	createRequest := httptest.NewRequest(http.MethodPost, "/api/connections", bytes.NewBufferString(`{
		"name":"expired","provider":"alicloud","site":"cn",
		"credential":{"type":"access_key","values":{"access_key_id":"id","access_key_secret":"secret"}}
	}`))
	createRequest.Header.Set("Authorization", "Bearer admin-token")
	createResponse := httptest.NewRecorder()
	router.ServeHTTP(createResponse, createRequest)
	var created asset.CloudConnection
	if err := json.Unmarshal(createResponse.Body.Bytes(), &created); err != nil || createResponse.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s err=%v", createResponse.Code, createResponse.Body.String(), err)
	}

	request := httptest.NewRequest(http.MethodPost, "/api/connections/"+string(created.ID)+"/validate", nil)
	request.Header.Set("Authorization", "Bearer admin-token")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusUnprocessableEntity ||
		!strings.Contains(response.Body.String(), `"code":"credential_expired"`) ||
		strings.Contains(response.Body.String(), `"code":"server.unavailable"`) {
		t.Fatalf("validation status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestUnverifiedConnectionContextReturnsConflict(t *testing.T) {
	for _, status := range []asset.ConnectionStatus{asset.ConnectionUnverified, asset.ConnectionInvalid} {
		t.Run(string(status), func(t *testing.T) {
			repositories, router := terminalRouter(t)
			connection, err := repositories.Connections().GetConnection(context.Background(), "connection-a")
			if err != nil {
				t.Fatal(err)
			}
			connection.Status = status
			if err := repositories.Connections().PutConnection(context.Background(), connection); err != nil {
				t.Fatal(err)
			}
			request := httptest.NewRequest(http.MethodGet, "/api/assets?connection_id=connection-a", nil)
			request.Header.Set("Authorization", "Bearer viewer-token")
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), `"code":"connection_not_validated"`) {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
}

func TestScanCreationAcceptsServerOwnedBroadScanAndRejectsRawShards(t *testing.T) {
	repositories, router := terminalRouter(t)
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	if err := repositories.Regions().PutRegion(context.Background(), asset.ConnectionRegion{ID: "region-broad", ConnectionID: "connection-a", RegionID: "cn-hangzhou", Origin: asset.RegionOriginManual, Lifecycle: asset.RegionActive, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name       string
		payload    string
		wantStatus int
	}{
		{name: "server owned broad scan", payload: `{"scope_mode":"all_active_regions"}`, wantStatus: http.StatusAccepted},
		{name: "raw shard contract", payload: `{"scope_mode":"all_active_regions","shards":[{"provider":"alicloud","scope_id":"scope-a","authoritative":true}]}`, wantStatus: http.StatusBadRequest},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/api/scans?connection_id=connection-a", bytes.NewBufferString(test.payload))
			request.Header.Set("Authorization", "Bearer operator-token")
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != test.wantStatus {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
}

func TestJobEventRouteResumesAfterLastEventID(t *testing.T) {
	repositories, router := terminalRouter(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 13, 12, 30, 0, 0, time.UTC)
	job := execution.Job{ID: "job-events", ConnectionID: "connection-a", Type: execution.JobScan, Status: execution.JobSucceeded, RunAt: now, CreatedAt: now, UpdatedAt: now, FinishedAt: &now}
	if err := repositories.Jobs().Enqueue(ctx, job); err != nil {
		t.Fatal(err)
	}
	for sequence, message := range []string{"first", "second"} {
		if err := repositories.Jobs().AppendLog(ctx, execution.JobLog{ID: "log-" + message, JobID: job.ID, Sequence: int64(sequence + 1), Kind: execution.JobLogText, Level: "info", Message: message, CreatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	request := httptest.NewRequest(http.MethodGet, "/api/jobs/job-events/events?connection_id=connection-a", nil)
	request.Header.Set("Authorization", "Bearer viewer-token")
	request.Header.Set("Last-Event-ID", "1")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	body := response.Body.String()
	if response.Code != http.StatusOK || !strings.Contains(body, "id: 2\n") || strings.Contains(body, `"message":"first"`) || !strings.Contains(body, `"message":"second"`) {
		t.Fatalf("status=%d body=%q", response.Code, body)
	}
}

func TestDomainResourcesUseOpaqueCursorPagination(t *testing.T) {
	repositories, router := terminalRouter(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	for index, id := range []asset.ConnectionID{"connection-a", "connection-b"} {
		if err := repositories.Connections().PutConnection(ctx, asset.CloudConnection{ID: id, Name: string(id), Provider: asset.ProviderAliCloud, Partition: "public", Principal: string(id), Status: asset.ConnectionActive, CreatedAt: now.Add(time.Duration(index) * time.Second), UpdatedAt: now}); err != nil {
			t.Fatal(err)
		}
		if err := repositories.Credentials().PutCredential(ctx, asset.ConnectionCredential{ConnectionID: id, Provider: asset.ProviderAliCloud, Type: asset.CredentialAliCloudAccessKey, EnvelopeVersion: 1, Nonce: "test", Ciphertext: "test", CreatedAt: now, UpdatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	request := httptest.NewRequest(http.MethodGet, "/api/connections?limit=1", nil)
	request.Header.Set("Authorization", "Bearer viewer-token")
	first := httptest.NewRecorder()
	router.ServeHTTP(first, request)
	if first.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", first.Code, first.Body.String())
	}
	var page persistence.Page[asset.CloudConnection]
	if err := json.Unmarshal(first.Body.Bytes(), &page); err != nil || len(page.Items) != 1 || page.NextCursor == "" {
		t.Fatalf("page=%+v err=%v", page, err)
	}
	secondRequest := httptest.NewRequest(http.MethodGet, "/api/connections?limit=1&cursor="+page.NextCursor, nil)
	secondRequest.Header.Set("Authorization", "Bearer viewer-token")
	second := httptest.NewRecorder()
	router.ServeHTTP(second, secondRequest)
	if second.Code != http.StatusOK {
		t.Fatalf("second status=%d body=%s", second.Code, second.Body.String())
	}
}

func TestAssetAndConnectionListsApplyPageFilters(t *testing.T) {
	repositories, router := terminalRouter(t)
	closedAsset, err := repositories.Inventory().GetAsset(context.Background(), "asset-a")
	if err != nil {
		t.Fatal(err)
	}
	activeNativeID := closedAsset.Identity.NativeID
	closedAt := time.Now().UTC()
	closedAsset.ID = "asset-closed"
	closedAsset.Identity.NativeID = "native-closed"
	closedAsset.Name = "closed resource"
	closedAsset.ClosedAt = &closedAt
	if err := repositories.Inventory().PutAsset(context.Background(), closedAsset); err != nil {
		t.Fatal(err)
	}

	assetsRequest := httptest.NewRequest(http.MethodGet, "/api/assets?connection_id=connection-a&resource_kind_id=missing-kind&limit=50", nil)
	assetsRequest.Header.Set("Authorization", "Bearer viewer-token")
	assetsResponse := httptest.NewRecorder()
	router.ServeHTTP(assetsResponse, assetsRequest)
	if assetsResponse.Code != http.StatusOK || !strings.Contains(assetsResponse.Body.String(), `"items":[]`) {
		t.Fatalf("filtered assets status=%d body=%s", assetsResponse.Code, assetsResponse.Body.String())
	}
	matchingAssetsRequest := httptest.NewRequest(http.MethodGet, "/api/assets?connection_id=connection-a&resource_kind_id=kind-a&limit=50", nil)
	matchingAssetsRequest.Header.Set("Authorization", "Bearer viewer-token")
	matchingAssetsResponse := httptest.NewRecorder()
	router.ServeHTTP(matchingAssetsResponse, matchingAssetsRequest)
	if matchingAssetsResponse.Code != http.StatusOK ||
		!strings.Contains(matchingAssetsResponse.Body.String(), `"id":"asset-a"`) ||
		strings.Contains(matchingAssetsResponse.Body.String(), `"id":"asset-closed"`) {
		t.Fatalf(
			"matching assets status=%d body=%s",
			matchingAssetsResponse.Code,
			matchingAssetsResponse.Body.String(),
		)
	}
	nativeIDAssetsRequest := httptest.NewRequest(
		http.MethodGet,
		"/api/assets?connection_id=connection-a&native_id=missing&native_id="+activeNativeID+"&limit=50",
		nil,
	)
	nativeIDAssetsRequest.Header.Set("Authorization", "Bearer viewer-token")
	nativeIDAssetsResponse := httptest.NewRecorder()
	router.ServeHTTP(nativeIDAssetsResponse, nativeIDAssetsRequest)
	if nativeIDAssetsResponse.Code != http.StatusOK ||
		!strings.Contains(nativeIDAssetsResponse.Body.String(), `"id":"asset-a"`) ||
		strings.Contains(nativeIDAssetsResponse.Body.String(), `"id":"asset-closed"`) {
		t.Fatalf(
			"native ID assets status=%d body=%s",
			nativeIDAssetsResponse.Code,
			nativeIDAssetsResponse.Body.String(),
		)
	}
	assetIDAssetsRequest := httptest.NewRequest(
		http.MethodGet,
		"/api/assets?connection_id=connection-a&asset_id=missing&asset_id=asset-a&limit=50",
		nil,
	)
	assetIDAssetsRequest.Header.Set("Authorization", "Bearer viewer-token")
	assetIDAssetsResponse := httptest.NewRecorder()
	router.ServeHTTP(assetIDAssetsResponse, assetIDAssetsRequest)
	if assetIDAssetsResponse.Code != http.StatusOK ||
		!strings.Contains(assetIDAssetsResponse.Body.String(), `"id":"asset-a"`) ||
		strings.Contains(assetIDAssetsResponse.Body.String(), `"id":"asset-closed"`) {
		t.Fatalf(
			"asset ID assets status=%d body=%s",
			assetIDAssetsResponse.Code,
			assetIDAssetsResponse.Body.String(),
		)
	}
	closedAssetsRequest := httptest.NewRequest(
		http.MethodGet,
		"/api/assets?connection_id=connection-a&resource_kind_id=kind-a&include_closed=true&limit=50",
		nil,
	)
	closedAssetsRequest.Header.Set("Authorization", "Bearer viewer-token")
	closedAssetsResponse := httptest.NewRecorder()
	router.ServeHTTP(closedAssetsResponse, closedAssetsRequest)
	if closedAssetsResponse.Code != http.StatusOK ||
		!strings.Contains(closedAssetsResponse.Body.String(), `"id":"asset-closed"`) {
		t.Fatalf(
			"closed assets status=%d body=%s",
			closedAssetsResponse.Code,
			closedAssetsResponse.Body.String(),
		)
	}
	multiKindAssetsRequest := httptest.NewRequest(
		http.MethodGet,
		"/api/assets?connection_id=connection-a&resource_kind_id=missing-kind&resource_kind_id=kind-a&limit=50",
		nil,
	)
	multiKindAssetsRequest.Header.Set("Authorization", "Bearer viewer-token")
	multiKindAssetsResponse := httptest.NewRecorder()
	router.ServeHTTP(multiKindAssetsResponse, multiKindAssetsRequest)
	if multiKindAssetsResponse.Code != http.StatusOK ||
		!strings.Contains(multiKindAssetsResponse.Body.String(), `"id":"asset-a"`) {
		t.Fatalf(
			"multi-kind assets status=%d body=%s",
			multiKindAssetsResponse.Code,
			multiKindAssetsResponse.Body.String(),
		)
	}

	connectionsRequest := httptest.NewRequest(http.MethodGet, "/api/connections?provider=not-present&limit=50", nil)
	connectionsRequest.Header.Set("Authorization", "Bearer viewer-token")
	connectionsResponse := httptest.NewRecorder()
	router.ServeHTTP(connectionsResponse, connectionsRequest)
	if connectionsResponse.Code != http.StatusOK || !strings.Contains(connectionsResponse.Body.String(), `"items":[]`) {
		t.Fatalf("filtered connections status=%d body=%s", connectionsResponse.Code, connectionsResponse.Body.String())
	}
}

func TestPanoramaAssetSearchScopesAndOrdersBeforeLimit(t *testing.T) {
	repositories, router := terminalRouter(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 31, 9, 0, 0, 0, time.UTC)

	for _, scope := range []asset.Scope{
		{ID: "scope-beijing", ConnectionID: "connection-a", ParentID: "scope-root", Kind: asset.ScopeRegion, NativeID: "cn-beijing", Name: "Beijing", CreatedAt: now, UpdatedAt: now},
		{ID: "scope-shanghai", ConnectionID: "connection-a", ParentID: "scope-root", Kind: asset.ScopeRegion, NativeID: "cn-shanghai", Name: "Shanghai", CreatedAt: now, UpdatedAt: now},
	} {
		if err := repositories.Inventory().PutScope(ctx, scope); err != nil {
			t.Fatal(err)
		}
	}

	kinds := []asset.ResourceKind{
		{ID: "search-vpc", Provider: asset.ProviderAliCloud, NativeType: "ACS::VPC::VPC", Class: "network.vpc", DisplayName: "VPC", BundleRevision: "search"},
		{ID: "search-vswitch", Provider: asset.ProviderAliCloud, NativeType: "ACS::VPC::VSwitch", Class: "network.subnet", DisplayName: "vSwitch", BundleRevision: "search"},
		{ID: "search-ecs", Provider: asset.ProviderAliCloud, NativeType: "ACS::ECS::Instance", Class: "compute.instance", DisplayName: "ECS", BundleRevision: "search"},
		{ID: "search-sg", Provider: asset.ProviderAliCloud, NativeType: "ACS::ECS::SecurityGroup", Class: "network.security_group", DisplayName: "Security group", BundleRevision: "search"},
		{ID: "search-disk", Provider: asset.ProviderAliCloud, NativeType: "ACS::ECS::Disk", Class: "storage.block", DisplayName: "Disk", BundleRevision: "search"},
		{ID: "search-bucket", Provider: asset.ProviderAliCloud, NativeType: "ACS::OSS::Bucket", Class: "storage.bucket", DisplayName: "Bucket", BundleRevision: "search"},
		{ID: "search-route", Provider: asset.ProviderAliCloud, NativeType: "ACS::VPC::RouteTable", Class: "network.route_table", DisplayName: "Route table", BundleRevision: "search"},
	}
	for _, kind := range kinds {
		if err := repositories.Inventory().PutResourceKind(ctx, kind); err != nil {
			t.Fatal(err)
		}
	}

	type searchAsset struct {
		id, nativeType, nativeID, scopeID, kindID, name, vpcID string
		closedAt                                               *time.Time
	}
	values := []searchAsset{
		{id: "order-route", nativeType: "ACS::VPC::RouteTable", nativeID: "rtb-order", scopeID: "scope-beijing", kindID: "search-route", name: "ros-order-beijing", vpcID: "vpc-a"},
		{id: "order-bucket", nativeType: "ACS::OSS::Bucket", nativeID: "ros-order-beijing", scopeID: "scope-beijing", kindID: "search-bucket", name: "ros-order-beijing", vpcID: "vpc-a"},
		{id: "order-disk", nativeType: "ACS::ECS::Disk", nativeID: "d-order", scopeID: "scope-beijing", kindID: "search-disk", name: "ros-order-beijing", vpcID: "vpc-a"},
		{id: "order-sg", nativeType: "ACS::ECS::SecurityGroup", nativeID: "sg-order", scopeID: "scope-beijing", kindID: "search-sg", name: "ros-order-beijing", vpcID: "vpc-a"},
		{id: "order-ecs", nativeType: "ACS::ECS::Instance", nativeID: "i-order", scopeID: "scope-beijing", kindID: "search-ecs", name: "ros-order-beijing", vpcID: "vpc-a"},
		{id: "order-vswitch", nativeType: "ACS::VPC::VSwitch", nativeID: "vsw-order", scopeID: "scope-beijing", kindID: "search-vswitch", name: "ros-order-beijing", vpcID: "vpc-a"},
		{id: "order-vpc", nativeType: "ACS::VPC::VPC", nativeID: "vpc-a", scopeID: "scope-beijing", kindID: "search-vpc", name: "ros-order-beijing"},
		{id: "order-foreign", nativeType: "ACS::VPC::VPC", nativeID: "vpc-shanghai", scopeID: "scope-shanghai", kindID: "search-vpc", name: "ros-order-beijing"},
		{id: "order-closed", nativeType: "ACS::VPC::VPC", nativeID: "vpc-closed", scopeID: "scope-beijing", kindID: "search-vpc", name: "ros-order-beijing", closedAt: &now},
		{id: "filter-vpc-a", nativeType: "ACS::ECS::Instance", nativeID: "i-vpc-a", scopeID: "scope-beijing", kindID: "search-ecs", name: "ros-vpc-filter", vpcID: "vpc-a"},
		{id: "filter-vpc-b", nativeType: "ACS::ECS::Instance", nativeID: "i-vpc-b", scopeID: "scope-beijing", kindID: "search-ecs", name: "ros-vpc-filter", vpcID: "vpc-b"},
		{id: "filter-public", nativeType: "ACS::ECS::Instance", nativeID: "i-public", scopeID: "scope-beijing", kindID: "search-ecs", name: "ros-public-beijing"},
		{id: "filter-global", nativeType: "ACS::OSS::Bucket", nativeID: "bucket-global", scopeID: "scope-root", kindID: "search-bucket", name: "ros-global-resource"},
	}
	for index, value := range values {
		normalized := map[string]any{}
		if value.vpcID != "" {
			normalized["vpc_id"] = value.vpcID
		}
		stored := asset.Asset{
			ID: asset.AssetID(value.id),
			Identity: asset.Identity{
				Provider: asset.ProviderAliCloud, Partition: "public", ConnectionID: "connection-a",
				NativeType: value.nativeType, NativeID: value.nativeID,
			},
			ScopeID: asset.ScopeID(value.scopeID), ResourceKindID: asset.ResourceKindID(value.kindID), Name: value.name,
			Normalized: normalized, FirstSeenAt: now.Add(time.Duration(index) * time.Second),
			LastSeenAt: now, ClosedAt: value.closedAt,
		}
		if err := repositories.Inventory().PutAsset(ctx, stored); err != nil {
			t.Fatal(err)
		}
	}

	search := func(path string) persistence.Page[asset.Asset] {
		t.Helper()
		request := httptest.NewRequest(http.MethodGet, path, nil)
		request.Header.Set("Authorization", "Bearer viewer-token")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("search status=%d body=%s", response.Code, response.Body.String())
		}
		var page persistence.Page[asset.Asset]
		if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		return page
	}
	ids := func(page persistence.Page[asset.Asset]) []asset.AssetID {
		result := make([]asset.AssetID, 0, len(page.Items))
		for _, item := range page.Items {
			result = append(result, item.ID)
		}
		return result
	}

	ordered := search("/api/assets?connection_id=connection-a&q=ros-order-beijing&canvas=region&region_id=cn-beijing&order=panorama-search&limit=7")
	wantOrder := []asset.AssetID{"order-vpc", "order-vswitch", "order-ecs", "order-sg", "order-disk", "order-bucket", "order-route"}
	if got := ids(ordered); fmt.Sprint(got) != fmt.Sprint(wantOrder) || ordered.NextCursor != "" {
		t.Fatalf("ordered region search = %v cursor=%q, want %v", got, ordered.NextCursor, wantOrder)
	}

	vpc := search("/api/assets?connection_id=connection-a&q=ros-vpc-filter&canvas=vpc&region_id=cn-beijing&vpc_id=vpc-a&order=panorama-search&limit=10")
	if got := ids(vpc); fmt.Sprint(got) != fmt.Sprint([]asset.AssetID{"filter-vpc-a"}) {
		t.Fatalf("VPC search = %v", got)
	}
	public := search("/api/assets?connection_id=connection-a&q=ros-public-beijing&canvas=region-public&region_id=cn-beijing&order=panorama-search&limit=10")
	if got := ids(public); fmt.Sprint(got) != fmt.Sprint([]asset.AssetID{"filter-public"}) {
		t.Fatalf("public search = %v", got)
	}
	global := search("/api/assets?connection_id=connection-a&q=ros-global-resource&canvas=global&order=panorama-search&limit=10")
	if got := ids(global); fmt.Sprint(got) != fmt.Sprint([]asset.AssetID{"filter-global"}) {
		t.Fatalf("global search = %v", got)
	}
}

func TestPanoramaAssetSearchRejectsIncompleteCanvas(t *testing.T) {
	_, router := terminalRouter(t)
	request := httptest.NewRequest(http.MethodGet, "/api/assets?connection_id=connection-a&canvas=vpc&region_id=cn-beijing", nil)
	request.Header.Set("Authorization", "Bearer viewer-token")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), `"code":"assets.canvas_invalid"`) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestTopologyRouteReturnsVPCViewWithNewQueryProtocol(t *testing.T) {
	_, router := terminalRouter(t)
	request := httptest.NewRequest(http.MethodGet, "/api/topology?connection_id=connection-a&focus_key="+coretopology.VPCFocusKey("cn-hangzhou", "vpc-a")+"&limit=50", nil)
	request.Header.Set("Authorization", "Bearer viewer-token")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var result struct {
		Revision struct {
			Inventory string `json:"inventory"`
		} `json:"revision"`
		View struct {
			Kind string `json:"kind"`
		} `json:"view"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil || result.Revision.Inventory == "" || result.View.Kind != "vpc" {
		t.Fatalf("result=%+v err=%v", result, err)
	}

	accountRequest := httptest.NewRequest(http.MethodGet, "/api/topology?connection_id=connection-a", nil)
	accountRequest.Header.Set("Authorization", "Bearer viewer-token")
	accountResponse := httptest.NewRecorder()
	router.ServeHTTP(accountResponse, accountRequest)
	if accountResponse.Code != http.StatusOK {
		t.Fatalf("account status=%d body=%s", accountResponse.Code, accountResponse.Body.String())
	}
}

func TestTopologyRouteRejectsOversizedLimitWithStableError(t *testing.T) {
	_, router := terminalRouter(t)
	request := httptest.NewRequest(http.MethodGet, "/api/topology?connection_id=connection-a&limit=10001", nil)
	request.Header.Set("Authorization", "Bearer viewer-token")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || response.Code != http.StatusBadRequest || body.Error.Code != "topology.limit_invalid" {
		t.Fatalf("status=%d body=%s err=%v", response.Code, response.Body.String(), err)
	}
}

func TestTopologyRouteRejectsLegacyQueryParameters(t *testing.T) {
	_, router := terminalRouter(t)
	for _, parameter := range []string{"parent_key=x", "depth=1", "node_limit=50"} {
		request := httptest.NewRequest(http.MethodGet, "/api/topology?connection_id=connection-a&"+parameter, nil)
		request.Header.Set("Authorization", "Bearer viewer-token")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		var body struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || response.Code != http.StatusBadRequest || body.Error.Code != "topology.query_invalid" {
			t.Fatalf("%s status=%d body=%s err=%v", parameter, response.Code, response.Body.String(), err)
		}
	}
}

type testBundles map[asset.Provider]spec.Bundle

func (b testBundles) Bundle(provider asset.Provider) (spec.Bundle, error) { return b[provider], nil }
func (b testBundles) Bundles() []spec.Bundle {
	result := make([]spec.Bundle, 0, len(b))
	for _, bundle := range b {
		result = append(result, bundle)
	}
	return result
}

func (b testBundles) ProviderDescriptors() []contracts.ProviderDescriptor {
	return []contracts.ProviderDescriptor{{Provider: asset.ProviderAliCloud, InventorySources: []contracts.InventorySource{{Name: "resource-center", RootScopeKinds: []asset.ScopeKind{asset.ScopeRegion}}}}}
}

func (b testBundles) ResolveNetworkTargetDiscoverer(asset.Provider) (contracts.NetworkTargetDiscoverer, error) {
	return b, nil
}

func (b testBundles) SearchNetworkTargets(_ context.Context, query contracts.NetworkTargetQuery) (contracts.NetworkTargetPage, error) {
	items := []contracts.NetworkTargetOption{
		{Kind: asset.ScanTargetVPC, RegionID: "cn-hangzhou", NativeID: "vpc-live", Name: "Provider VPC"},
		{Kind: asset.ScanTargetVSwitch, RegionID: "cn-hangzhou", NativeID: "vsw-live", Name: "Provider vSwitch", ParentNativeID: "vpc-live"},
	}
	page := contracts.NetworkTargetPage{RequestID: "provider-request"}
	for _, item := range items {
		if item.Kind == query.Kind && item.RegionID == query.RegionID && (query.Query == "" || strings.Contains(item.NativeID, query.Query) || strings.Contains(item.Name, query.Query)) {
			page.Items = append(page.Items, item)
		}
	}
	return page, nil
}

type testConnectionValidator struct {
	err error
}

func (validator testConnectionValidator) ValidateConnection(_ context.Context, provider asset.Provider, credential contracts.Credential) (contracts.ConnectionIdentity, error) {
	if validator.err != nil {
		return contracts.ConnectionIdentity{}, validator.err
	}
	if provider == "" || credential.Type == "" || len(credential.Values) == 0 {
		return contracts.ConnectionIdentity{}, errors.New("invalid credential")
	}
	return contracts.ConnectionIdentity{
		Partition:  "public",
		TenantID:   "1234567890123456",
		Principal:  "cloud-user",
		RootScopes: []contracts.RootScopeCandidate{{Kind: asset.ScopeAccount, NativeID: "1234567890123456", Name: "production account"}},
	}, nil
}

func (testConnectionValidator) ValidateConnectionSite(provider asset.Provider, site asset.ConnectionSite) error {
	switch provider {
	case asset.ProviderAliCloud:
		if site == asset.ConnectionSiteCN || site == asset.ConnectionSiteINTL {
			return nil
		}
	case asset.ProviderAWS:
		if site == "" {
			return nil
		}
	}
	return errors.New("site rejected")
}

func terminalRouter(t *testing.T) (persistence.Repositories, http.Handler) {
	return terminalRouterWithValidator(t, testConnectionValidator{})
}

func terminalRouterWithValidator(t *testing.T, validator connectionapp.Validator) (persistence.Repositories, http.Handler) {
	return terminalRouterWithOAuthFlows(t, validator, nil)
}

func terminalRouterWithOAuthFlows(t *testing.T, validator connectionapp.Validator, oauthFlows contracts.OAuthFlowService) (persistence.Repositories, http.Handler) {
	t.Helper()
	repositories, err := sqlite.Open(filepath.Join(t.TempDir(), "http.db"), filepath.Join("..", "..", "..", "migrations"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	if err := repositories.Connections().PutConnection(context.Background(), asset.CloudConnection{
		ID: "connection-a", Name: "test account", Provider: asset.ProviderAliCloud, Partition: "public", Principal: "test account", Status: asset.ConnectionActive,
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	vault, err := credentialstore.NewVault("AQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQE=", repositories.Credentials())
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := vault.Seal("connection-a", asset.ProviderAliCloud, contracts.Credential{
		Type:   asset.CredentialAliCloudAccessKey,
		Values: map[string]string{"access_key_id": "id", "access_key_secret": "secret"},
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := repositories.Credentials().PutCredential(context.Background(), sealed); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Inventory().PutScope(context.Background(), asset.Scope{
		ID: "scope-root", ConnectionID: "connection-a", Kind: asset.ScopeAccount,
		NativeID: "1234567890123456", Name: "test account", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Inventory().PutScope(context.Background(), asset.Scope{
		ID: "scope-a", ConnectionID: "connection-a", ParentID: "scope-root", Kind: asset.ScopeRegion,
		NativeID: "cn-hangzhou", Name: "cn-hangzhou", Location: "cn-hangzhou", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	value := asset.Asset{
		ID: "asset-a", Identity: asset.Identity{Provider: asset.ProviderAliCloud, Partition: "public", ConnectionID: "connection-a", NativeType: "ACS::ECS::Instance", NativeID: "i-a"},
		ScopeID: "scope-a", ResourceKindID: "kind-a", CurrentObservationID: "observation-a", Name: "instance-a", Location: "cn-hangzhou",
		Capabilities: asset.CapabilitySet{asset.CapabilityIndexed, asset.CapabilityActionable}, FirstSeenAt: now, LastSeenAt: now,
	}
	if err := repositories.Inventory().PutAsset(context.Background(), value); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Graph().ReplaceGraph(context.Background(), "scope-a", "graph-a", nil, nil); err != nil {
		t.Fatal(err)
	}
	bundles := testBundles{asset.ProviderAliCloud: {Provider: asset.ProviderAliCloud, Revision: "bundle-a", Hash: "spec-a"}}
	planner := cleanup.NewService(repositories, bundles,
		cleanup.WithClock(func() time.Time { return now }),
		cleanup.WithTaskIDGenerator(func() string { return "cln-http" }),
		cleanup.WithExecutionIDGenerator(func() string { return "execution-http" }),
	)
	regionQueue, err := regionapp.NewRefreshQueue(repositories)
	if err != nil {
		t.Fatal(err)
	}
	connections, err := connectionapp.NewService(repositories, vault, validator, regionQueue)
	if err != nil {
		t.Fatal(err)
	}
	authenticator := httptransport.NewStaticBearerAuthenticator([]httptransport.TokenBinding{
		{Token: "viewer-token", Principal: httptransport.Principal{Subject: "bob", Roles: []httptransport.Role{httptransport.RoleViewer}}},
		{Token: "operator-token", Principal: httptransport.Principal{Subject: "alice", Roles: []httptransport.Role{httptransport.RoleOperator}}},
		{Token: "admin-token", Principal: httptransport.Principal{Subject: "carol", Roles: []httptransport.Role{httptransport.RoleAdmin}}},
	})
	scanCreator, err := inventory.NewCreator(repositories, bundles, inventory.WithCreatorClock(func() time.Time { return now }))
	if err != nil {
		t.Fatal(err)
	}
	scanControls, err := inventory.NewControlService(
		repositories,
		inventory.WithControlDirectory(bundles),
	)
	if err != nil {
		t.Fatal(err)
	}
	return repositories, httptransport.NewRouter(httptransport.Dependencies{
		Repositories: repositories, CleanupTasks: planner, Connections: connections, Regions: mustRegionService(t, repositories), RegionRefreshes: regionQueue, Scans: scanCreator, ScanControls: scanControls, NetworkTargets: bundles, Topology: topologyapp.NewService(repositories, bundles, topologyapp.WithClock(func() time.Time { return now })), Bundles: bundles, Providers: bundles, OAuthFlows: oauthFlows, Authenticator: authenticator, SSEPollInterval: time.Millisecond,
	})
}

func mustRegionService(t *testing.T, repositories persistence.Repositories) *regionapp.Service {
	t.Helper()
	service, err := regionapp.NewService(repositories)
	if err != nil {
		t.Fatal(err)
	}
	return service
}
