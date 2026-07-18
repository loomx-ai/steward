package cleanup_test

import (
	"context"
	"errors"
	"testing"

	"github.com/loomx-ai/steward/internal/app/cleanup"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func TestContinueExecutionRejectsRebuiltDependencyGraph(t *testing.T) {
	ctx := context.Background()
	repositories, planner, created, now := directExecutionFixture(t, "rebuilt-graph-continuation")
	providerError := &contracts.ProviderCallError{Provider: execution.ProviderError{
		Category: execution.ErrorInvalidRequest,
		Code:     "DependencyViolation",
		Message:  "dependent resource still exists",
	}}
	driver := &scriptedActionDriver{executeErrors: []error{providerError}}
	handler := cleanup.NewExecutionHandler(
		planner,
		cleanup.ActionResolverFunc(func(context.Context, asset.Asset) (cleanup.ActionDriver, error) {
			return driver, nil
		}),
	)

	if err := handler.Handle(ctx, claimExecutionJob(t, repositories, now)); err != nil {
		t.Fatalf("initial failing action: %v", err)
	}
	if err := repositories.Graph().ReplaceGraph(ctx, "scope-a", "graph-rebuilt", nil, nil); err != nil {
		t.Fatal(err)
	}

	_, err := planner.ContinueExecution(ctx, cleanup.ContinueExecutionRequest{
		CleanupTaskID: "cln-direct", RequestedBy: "operator", IdempotencyKey: "stale-graph-continue",
	})
	if !errors.Is(err, cleanup.ErrExecutionNotContinuable) {
		t.Fatalf("continue error = %v, want ErrExecutionNotContinuable", err)
	}
	stored, lookupErr := repositories.Executions().GetExecution(ctx, created.ID)
	if lookupErr != nil || stored.Status != execution.ExecutionFailed || stored.ContinueCount != 0 {
		t.Fatalf("execution=%+v err=%v", stored, lookupErr)
	}
	if driver.executeCalls != 1 {
		t.Fatalf("provider calls=%d, want 1", driver.executeCalls)
	}
}

func TestContinuedFailureRecordsAnotherActionOutcome(t *testing.T) {
	ctx := context.Background()
	repositories, planner, created, now := directExecutionFixture(t, "continued-failure-outcome")
	providerError := &contracts.ProviderCallError{Provider: execution.ProviderError{
		Category: execution.ErrorInvalidRequest,
		Code:     "InvalidParameter",
		Message:  "invalid delete parameter",
	}}
	driver := &scriptedActionDriver{executeErrors: []error{providerError, providerError}}
	handler := cleanup.NewExecutionHandler(
		planner,
		cleanup.ActionResolverFunc(func(context.Context, asset.Asset) (cleanup.ActionDriver, error) {
			return driver, nil
		}),
	)

	if err := handler.Handle(ctx, claimExecutionJob(t, repositories, now)); err != nil {
		t.Fatalf("initial failing action: %v", err)
	}
	if _, err := planner.ContinueExecution(ctx, cleanup.ContinueExecutionRequest{
		CleanupTaskID: "cln-direct", RequestedBy: "operator", IdempotencyKey: "continue-after-failure",
	}); err != nil {
		t.Fatalf("continue failed action: %v", err)
	}
	if err := handler.Handle(ctx, claimExecutionJob(t, repositories, now)); err != nil {
		t.Fatalf("continued failing action: %v", err)
	}
	stored, err := repositories.Executions().GetExecution(ctx, created.ID)
	if err != nil || stored.Status != execution.ExecutionFailed || driver.executeCalls != 2 {
		t.Fatalf("execution=%+v driver_calls=%d err=%v", stored, driver.executeCalls, err)
	}
	continued, err := planner.ContinueExecution(ctx, cleanup.ContinueExecutionRequest{
		CleanupTaskID: "cln-direct", RequestedBy: "operator", IdempotencyKey: "continue-after-second-failure",
	})
	if err != nil || continued.ContinueCount != 2 {
		t.Fatalf("second continuation=%+v err=%v", continued, err)
	}
}
