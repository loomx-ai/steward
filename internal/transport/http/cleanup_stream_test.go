package httptransport_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/internal/persistence"
)

type streamEvent struct {
	name string
	data string
}

func parseStreamEvents(body string) []streamEvent {
	var events []streamEvent
	for _, frame := range strings.Split(body, "\n\n") {
		var event streamEvent
		for _, line := range strings.Split(frame, "\n") {
			if value, ok := strings.CutPrefix(line, "event: "); ok {
				event.name = value
			}
			if value, ok := strings.CutPrefix(line, "data: "); ok {
				event.data = value
			}
		}
		if event.name != "" {
			events = append(events, event)
		}
	}
	return events
}

func TestCleanupProgressStreamPushesOnlyChangedStateUntilTheTaskSettles(t *testing.T) {
	repositories, router := terminalRouter(t)
	ctx := context.Background()
	taskRequest := httptest.NewRequest(http.MethodPost, "/api/cleanup?connection_id=connection-a", bytes.NewBufferString(`{"selectors":[{"kind":"asset","asset_id":"asset-a"}]}`))
	taskRequest.Header.Set("Authorization", "Bearer operator-token")
	taskResponse := httptest.NewRecorder()
	router.ServeHTTP(taskResponse, taskRequest)
	var aggregate persistence.CleanupTaskAggregate
	if err := json.Unmarshal(taskResponse.Body.Bytes(), &aggregate); err != nil || taskResponse.Code != http.StatusCreated {
		t.Fatalf("cleanup task status=%d body=%s err=%v", taskResponse.Code, taskResponse.Body.String(), err)
	}
	executeRequest := httptest.NewRequest(http.MethodPost, "/api/cleanup/"+string(aggregate.Task.ID)+"/executions?connection_id=connection-a", bytes.NewBufferString(`{"idempotency_key":"progress-stream","confirmation":{"acknowledged":true}}`))
	executeRequest.Header.Set("Authorization", "Bearer operator-token")
	executeResponse := httptest.NewRecorder()
	router.ServeHTTP(executeResponse, executeRequest)
	var attempt execution.ExecutionAttempt
	if err := json.Unmarshal(executeResponse.Body.Bytes(), &attempt); err != nil || executeResponse.Code != http.StatusAccepted {
		t.Fatalf("execution status=%d body=%s err=%v", executeResponse.Code, executeResponse.Body.String(), err)
	}
	stored, err := repositories.CleanupTasks().GetTask(ctx, aggregate.Task.ID)
	if err != nil {
		t.Fatal(err)
	}
	impact := plan.ImpactItem{ID: "impact-progress", CleanupTaskID: aggregate.Task.ID, AssetID: "asset-b", ControllerID: "asset-a", Expected: plan.ExpectedDelegatedDelete}
	if err := repositories.CleanupTasks().ReplaceTask(ctx, stored.Task, append(stored.Steps, plan.CleanupTaskStep{ID: "stp-progress-b", CleanupTaskID: aggregate.Task.ID, AssetID: "asset-b", Kind: plan.StepDirect, Action: "delete"}), []plan.ImpactItem{impact}); err != nil {
		t.Fatal(err)
	}
	actions := make([]execution.ActionAttempt, 2)
	for index, stepID := range []string{string(aggregate.Steps[0].ID), "stp-progress-b"} {
		id := execution.ActionAttemptID(fmt.Sprintf("action-progress-%d", index+1))
		actions[index] = execution.ActionAttempt{
			ID: id, ExecutionID: attempt.ID, CleanupTaskStepID: stepID,
			AssetID: "asset-a", Action: "delete", Status: execution.ActionPending,
			IdempotencyKey: string(id), SpecBundleRevision: "bundle", SpecHash: "hash",
			CreatedAt: attempt.CreatedAt, UpdatedAt: attempt.CreatedAt,
		}
		if err := repositories.Executions().AppendAction(ctx, actions[index]); err != nil {
			t.Fatal(err)
		}
	}

	server := httptest.NewServer(router)
	defer server.Close()
	streamCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	streamRequest, err := http.NewRequestWithContext(streamCtx, http.MethodGet, server.URL+"/api/cleanup/"+string(aggregate.Task.ID)+"/progress?connection_id=connection-a", nil)
	if err != nil {
		t.Fatal(err)
	}
	streamRequest.Header.Set("Authorization", "Bearer viewer-token")
	streamResponse, err := http.DefaultClient.Do(streamRequest)
	if err != nil {
		t.Fatal(err)
	}
	defer streamResponse.Body.Close()
	if streamResponse.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", streamResponse.StatusCode)
	}
	reader := bufio.NewReader(streamResponse.Body)
	var seen []streamEvent
	// next reads events until one named name arrives and returns it.
	next := func(name string) streamEvent {
		t.Helper()
		var frame strings.Builder
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				t.Fatalf("stream ended before %s: %v; events=%+v", name, err, seen)
			}
			if line != "\n" {
				frame.WriteString(line)
				continue
			}
			events := parseStreamEvents(frame.String())
			frame.Reset()
			if len(events) == 0 {
				continue
			}
			seen = append(seen, events[0])
			if events[0].name == "aggregate" && len(seen) > 1 {
				t.Fatalf("aggregate resent although its steps did not change: %+v", seen)
			}
			if events[0].name == name {
				return events[0]
			}
		}
	}

	if first := next("aggregate"); len(seen) != 1 || !strings.Contains(first.data, `"impact-progress"`) {
		t.Fatalf("stream must open with the full aggregate: %+v", seen)
	}
	if event := next("actions"); strings.Count(event.data, `"status":"pending"`) != 2 {
		t.Fatalf("first actions event must carry every action: %s", event.data)
	}

	actions[0].Status = execution.ActionSucceeded
	if err := repositories.Executions().UpdateAction(ctx, actions[0]); err != nil {
		t.Fatal(err)
	}
	if event := next("actions"); !strings.Contains(event.data, `"id":"action-progress-1"`) || strings.Contains(event.data, "action-progress-2") {
		t.Fatalf("actions event must carry only the changed action: %s", event.data)
	}
	impact.Result = plan.ImpactDeletedByController
	if err := repositories.CleanupTasks().UpdateImpactItems(ctx, aggregate.Task.ID, []plan.ImpactItem{impact}); err != nil {
		t.Fatal(err)
	}
	if event := next("impacts"); !strings.Contains(event.data, `"result":"deleted_by_controller"`) {
		t.Fatalf("impacts event must carry the changed item: %s", event.data)
	}

	actions[1].Status = execution.ActionSucceeded
	if err := repositories.Executions().UpdateAction(ctx, actions[1]); err != nil {
		t.Fatal(err)
	}
	finishedAt := attempt.CreatedAt.Add(time.Minute)
	attempt, err = repositories.Executions().GetExecution(ctx, attempt.ID)
	if err != nil {
		t.Fatal(err)
	}
	attempt.Status = execution.ExecutionSucceeded
	attempt.FinishedAt = &finishedAt
	if err := repositories.Executions().UpdateExecution(ctx, attempt); err != nil {
		t.Fatal(err)
	}
	stored.Task.Status = plan.StatusCompleted
	if err := repositories.CleanupTasks().UpdateTask(ctx, stored.Task); err != nil {
		t.Fatal(err)
	}
	if event := next("end"); !strings.Contains(event.data, `"status":"completed"`) {
		t.Fatalf("end event must carry the settled task: %s", event.data)
	}
	if _, err := reader.ReadString('\n'); err == nil {
		t.Fatal("stream stayed open after the task settled")
	}
	executionSucceeded := false
	for _, event := range seen {
		executionSucceeded = executionSucceeded || event.name == "execution" && strings.Contains(event.data, `"status":"succeeded"`)
	}
	if !executionSucceeded {
		t.Fatalf("stream missed the execution change: %+v", seen)
	}
}

func TestCleanupProgressStreamHidesOtherConnectionsTasks(t *testing.T) {
	_, router := terminalRouter(t)
	taskRequest := httptest.NewRequest(http.MethodPost, "/api/cleanup?connection_id=connection-a", bytes.NewBufferString(`{"selectors":[{"kind":"asset","asset_id":"asset-a"}]}`))
	taskRequest.Header.Set("Authorization", "Bearer operator-token")
	taskResponse := httptest.NewRecorder()
	router.ServeHTTP(taskResponse, taskRequest)
	var aggregate persistence.CleanupTaskAggregate
	if err := json.Unmarshal(taskResponse.Body.Bytes(), &aggregate); err != nil {
		t.Fatal(err)
	}
	streamRequest := httptest.NewRequest(http.MethodGet, "/api/cleanup/"+string(aggregate.Task.ID)+"/progress?connection_id=connection-b", nil)
	streamRequest.Header.Set("Authorization", "Bearer viewer-token")
	streamResponse := httptest.NewRecorder()
	router.ServeHTTP(streamResponse, streamRequest)
	if streamResponse.Code != http.StatusNotFound || strings.Contains(streamResponse.Body.String(), "event:") {
		t.Fatalf("status=%d body=%s", streamResponse.Code, streamResponse.Body.String())
	}
}

func TestCleanupTaskListIncludesLatestExecution(t *testing.T) {
	_, router := terminalRouter(t)
	serve := func(method, path, body string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(method, path, bytes.NewBufferString(body))
		request.Header.Set("Authorization", "Bearer operator-token")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		return response
	}
	list := func() []map[string]any {
		response := serve(http.MethodGet, "/api/cleanup?connection_id=connection-a", "")
		var page struct {
			Items []map[string]any `json:"items"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil || response.Code != http.StatusOK || len(page.Items) != 1 {
			t.Fatalf("list status=%d body=%s err=%v", response.Code, response.Body.String(), err)
		}
		return page.Items
	}
	taskResponse := serve(http.MethodPost, "/api/cleanup?connection_id=connection-a", `{"selectors":[{"kind":"asset","asset_id":"asset-a"}]}`)
	var aggregate persistence.CleanupTaskAggregate
	if err := json.Unmarshal(taskResponse.Body.Bytes(), &aggregate); err != nil || taskResponse.Code != http.StatusCreated {
		t.Fatalf("cleanup task status=%d body=%s err=%v", taskResponse.Code, taskResponse.Body.String(), err)
	}
	if item := list()[0]; item["id"] != string(aggregate.Task.ID) || item["latest_execution"] != nil {
		t.Fatalf("unexecuted task item = %v", item)
	}
	executeResponse := serve(http.MethodPost, "/api/cleanup/"+string(aggregate.Task.ID)+"/executions?connection_id=connection-a", `{"idempotency_key":"list-latest","confirmation":{"acknowledged":true}}`)
	var attempt execution.ExecutionAttempt
	if err := json.Unmarshal(executeResponse.Body.Bytes(), &attempt); err != nil || executeResponse.Code != http.StatusAccepted {
		t.Fatalf("execution status=%d body=%s err=%v", executeResponse.Code, executeResponse.Body.String(), err)
	}
	latest, _ := list()[0]["latest_execution"].(map[string]any)
	if latest["id"] != string(attempt.ID) || latest["requested_by"] != attempt.RequestedBy {
		t.Fatalf("latest execution = %v, want %s", latest, attempt.ID)
	}
}
