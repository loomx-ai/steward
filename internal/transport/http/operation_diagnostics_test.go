package httptransport_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/persistence"
)

func TestExecutionActionDetailsHideSignedReceipts(t *testing.T) {
	repositories, router := terminalRouter(t)
	call := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
		req.Header.Set("Authorization", "Bearer operator-token")
		res := httptest.NewRecorder()
		router.ServeHTTP(res, req)
		return res
	}
	res := call("POST", "/api/cleanup?connection_id=connection-a", `{"selectors":[{"kind":"asset","asset_id":"asset-a"}]}`)
	var aggregate persistence.CleanupTaskAggregate
	if res.Code != http.StatusCreated || json.Unmarshal(res.Body.Bytes(), &aggregate) != nil {
		t.Fatal("create cleanup", res.Code, res.Body.String())
	}
	res = call("POST", "/api/cleanup/"+string(aggregate.Task.ID)+"/executions?connection_id=connection-a", `{"idempotency_key":"signed-details","confirmation":{"acknowledged":true}}`)
	var attempt execution.ExecutionAttempt
	if res.Code != http.StatusAccepted || json.Unmarshal(res.Body.Bytes(), &attempt) != nil {
		t.Fatal("create execution", res.Code, res.Body.String())
	}
	signed := "https://management.azure.com/resource/OperationResults/op?api-version=2020-03-01&s=private-signed-value"
	action := execution.ActionAttempt{ID: "signed-action", ExecutionID: attempt.ID, CleanupTaskStepID: string(aggregate.Steps[0].ID), AssetID: "asset-a", Action: "delete", Status: execution.ActionWaiting, IdempotencyKey: "signed-action-key", SpecBundleRevision: "bundle", SpecHash: "hash", ProviderOperationID: signed, ProviderResult: map[string]any{"stream_analytics_poll_operation": signed}, CreatedAt: attempt.CreatedAt, UpdatedAt: attempt.CreatedAt}
	if err := repositories.Executions().AppendAction(context.Background(), action); err != nil {
		t.Fatal(err)
	}
	res = call("GET", "/api/execution-attempts/"+string(attempt.ID)+"/actions?connection_id=connection-a", "")
	if res.Code != http.StatusOK || strings.Contains(res.Body.String(), "private-signed-value") || !strings.Contains(res.Body.String(), "api-version") {
		t.Fatal("signed execution details exposed", res.Code, res.Body.String())
	}
	stored, err := repositories.Executions().ListActions(context.Background(), attempt.ID)
	if err != nil || len(stored) != 1 || stored[0].ProviderOperationID != signed || stored[0].ProviderResult["stream_analytics_poll_operation"] != signed {
		t.Fatal("display projection corrupted persisted receipt", err)
	}
}
