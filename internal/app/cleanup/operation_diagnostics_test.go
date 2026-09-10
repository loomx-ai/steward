package cleanup_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/loomx-ai/steward/internal/app/cleanup"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/persistence"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

type rotatingSignedReceiptDriver struct {
	scriptedActionDriver
	t             *testing.T
	initial, next string
	waits         int
}

func (d *rotatingSignedReceiptDriver) Execute(context.Context, contracts.ActionRequest) (contracts.ActionResult, error) {
	return contracts.ActionResult{ProviderOperationID: d.initial, Data: map[string]any{"polling": "location"}, RetryAfter: time.Second}, nil
}
func (d *rotatingSignedReceiptDriver) Wait(_ context.Context, _ contracts.ActionRequest, result contracts.ActionResult) (contracts.WaitResult, error) {
	d.waits++
	if result.ProviderOperationID != d.initial {
		d.t.Fatal("execution journal stripped the initial signed receipt")
	}
	if d.waits == 1 {
		return contracts.WaitResult{RetryAfter: time.Second, State: "InProgress", Data: map[string]any{"polling": "location", "stream_analytics_poll_operation": d.next}}, nil
	}
	if result.Data["stream_analytics_poll_operation"] != d.next {
		d.t.Fatal("execution journal stripped the rotated signed receipt")
	}
	return contracts.WaitResult{Done: true}, nil
}

func TestExecutionPersistsSignedReceiptsWithoutPublishingCredentials(t *testing.T) {
	var logs []execution.JobLogEntry
	ctx := execution.WithJobLogSink(context.Background(), execution.JobLogSinkFunc(func(_ context.Context, entry execution.JobLogEntry) { logs = append(logs, entry) }))
	repositories, planner, created, now := directExecutionFixture(t, "execution-signed-receipt")
	initial := "https://management.azure.com/resource/OperationResults/op?api-version=2020-03-01&s=private-initial"
	next := "https://management.azure.com/resource/OperationResults/op?api-version=2020-03-01&s=private-next"
	driver := &rotatingSignedReceiptDriver{t: t, initial: initial, next: next, scriptedActionDriver: scriptedActionDriver{readback: contracts.ReadbackResult{Exists: false}}}
	resolver := cleanup.ActionResolverFunc(func(context.Context, asset.Asset) (cleanup.ActionDriver, error) { return driver, nil })
	job := claimExecutionJob(t, repositories, now)
	for range 4 {
		// Each poll uses a fresh handler and restores its receipt from repositories.
		handler := cleanup.NewExecutionHandler(planner, resolver)
		var retry *cleanup.RetryError
		if err := handler.Handle(ctx, job); err != nil && !errors.As(err, &retry) {
			t.Fatal(err)
		}
	}
	actions, err := repositories.Executions().ListActions(ctx, created.ID)
	if err != nil || len(actions) != 1 || actions[0].Status != execution.ActionSucceeded || actions[0].ProviderOperationID != initial || actions[0].ProviderResult["stream_analytics_poll_operation"] != next {
		t.Fatal("native execution receipt lost", actions, err)
	}
	audits, err := repositories.Audits().ListAuditEvents(ctx, persistence.ListOptions{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(struct {
		Logs   []execution.JobLogEntry
		Audits any
	}{logs, audits})
	if len(logs) == 0 || !strings.Contains(string(payload), "api-version") || strings.Contains(string(payload), "private-") {
		t.Fatal("durable diagnostics expose polling credentials")
	}
}
