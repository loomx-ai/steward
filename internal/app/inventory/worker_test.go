package inventory_test

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/loomx-ai/steward/internal/app/inventory"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/persistence"
	"github.com/loomx-ai/steward/internal/persistence/sqlite"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func TestScanHandlerProjectsAllPagesAndFinishesRun(t *testing.T) {
	ctx := context.Background()
	repositories := openInventoryWorkerRepositories(t)
	now := time.Date(2026, 7, 13, 8, 0, 0, 0, time.UTC)
	seedScanWorker(t, repositories, now)
	adapter := &pagedInventoryAdapter{pages: []contracts.InventoryBatch{
		{Items: []contracts.InventoryItem{{NativeType: workerKind().NativeType, NativeID: "i-1", ResourceKind: workerKind(), Name: "one", Raw: map[string]any{"InstanceId": "i-1"}}}, NextCursor: "page-2"},
		{Items: []contracts.InventoryItem{{NativeType: workerKind().NativeType, NativeID: "i-2", ResourceKind: workerKind(), Name: "two", Raw: map[string]any{"InstanceId": "i-2"}}}, Complete: true},
	}}
	service := inventory.NewService(repositories.Inventory(), inventory.WithClock(func() time.Time { return now }))
	handler := inventory.NewScanHandler(repositories, inventoryRuntime{adapter: adapter}, service)

	err := handler.Handle(ctx, execution.Job{ID: "job-scan", Type: execution.JobScan, Payload: map[string]any{"scan_shard_id": "shard-worker"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(adapter.requests) != 2 || adapter.requests[0].ConnectionID != "connection-worker" || adapter.requests[1].Cursor != "page-2" {
		t.Fatalf("requests = %+v", adapter.requests)
	}
	page, err := repositories.Inventory().ListAssets(ctx, persistence.ListOptions{Limit: 10})
	if err != nil || len(page.Items) != 2 {
		t.Fatalf("assets = %+v, err = %v", page, err)
	}
	shard, err := repositories.Inventory().GetScanShard(ctx, "shard-worker")
	if err != nil || shard.Status != asset.ShardSucceeded || !shard.Coverage.Complete || shard.Coverage.ItemCount != 2 {
		t.Fatalf("shard = %+v, err = %v", shard, err)
	}
	run, err := repositories.Inventory().GetScanRun(ctx, "run-worker")
	if err != nil || run.Status != asset.ScanReconciling || run.CompletionStatus != asset.ScanSucceeded || run.FinishedAt != nil {
		t.Fatalf("run = %+v, err = %v", run, err)
	}
	graphJob, err := repositories.Jobs().GetJobByIdempotencyKey(ctx, "scan-graph:run-worker")
	if err != nil || graphJob.Type != execution.JobGraph || graphJob.Payload["scan_run_id"] != "run-worker" {
		t.Fatalf("graph job = %+v, err = %v", graphJob, err)
	}
}

func TestScanHandlerRejectsConnectionThatLostValidationBeforeProviderCall(t *testing.T) {
	ctx := context.Background()
	repositories := openInventoryWorkerRepositories(t)
	now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	seedScanWorker(t, repositories, now)
	connection, err := repositories.Connections().GetConnection(ctx, "connection-worker")
	if err != nil {
		t.Fatal(err)
	}
	connection.Status = asset.ConnectionUnverified
	connection.UpdatedAt = now.Add(time.Minute)
	if err := repositories.Connections().PutConnection(ctx, connection); err != nil {
		t.Fatal(err)
	}
	adapter := &pagedInventoryAdapter{pages: []contracts.InventoryBatch{{Complete: true}}}
	handler := inventory.NewScanHandler(
		repositories,
		inventoryRuntime{adapter: adapter},
		inventory.NewService(repositories.Inventory(), inventory.WithClock(func() time.Time { return now })),
	)

	err = handler.Handle(ctx, execution.Job{ID: "job-stale-credential", Type: execution.JobScan, Payload: map[string]any{"scan_shard_id": "shard-worker"}})
	if !errors.Is(err, asset.ErrConnectionNotValidated) {
		t.Fatalf("Handle() error = %v, want ErrConnectionNotValidated", err)
	}
	if len(adapter.requests) != 0 {
		t.Fatalf("provider calls = %#v, want none", adapter.requests)
	}
}

func TestScanHandlerRechecksValidationBeforeProviderEnrichment(t *testing.T) {
	ctx := context.Background()
	repositories := openInventoryWorkerRepositories(t)
	now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	seedScanWorker(t, repositories, now)
	enrichCalls := 0
	adapter := &enrichingInventoryAdapter{
		pagedInventoryAdapter: pagedInventoryAdapter{
			pages: []contracts.InventoryBatch{{
				Items: []contracts.InventoryItem{{
					NativeType: workerKind().NativeType, NativeID: "i-1",
					ResourceKind: workerKind(), Raw: map[string]any{"InstanceId": "i-1"},
				}},
				Complete: true,
			}},
			onList: func() {
				connection, err := repositories.Connections().GetConnection(ctx, "connection-worker")
				if err != nil {
					t.Fatal(err)
				}
				connection.Status = asset.ConnectionInvalid
				connection.UpdatedAt = now.Add(time.Minute)
				if err := repositories.Connections().PutConnection(ctx, connection); err != nil {
					t.Fatal(err)
				}
			},
		},
		enrich: func(_ context.Context, _ contracts.InventoryRequest, items []contracts.InventoryItem) ([]contracts.InventoryItem, error) {
			enrichCalls++
			return items, nil
		},
	}
	handler := inventory.NewScanHandler(
		repositories,
		inventoryRuntime{adapter: adapter},
		inventory.NewService(repositories.Inventory(), inventory.WithClock(func() time.Time { return now })),
	)

	err := handler.Handle(ctx, execution.Job{ID: "job-enrichment-gate", Type: execution.JobScan, Payload: map[string]any{"scan_shard_id": "shard-worker"}})
	if !errors.Is(err, asset.ErrConnectionNotValidated) {
		t.Fatalf("Handle() error = %v, want ErrConnectionNotValidated", err)
	}
	if enrichCalls != 0 {
		t.Fatalf("provider enrichment calls = %d, want 0", enrichCalls)
	}
}

func TestScanHandlerEnrichesInventoryBatchBeforeProjection(t *testing.T) {
	ctx := context.Background()
	repositories := openInventoryWorkerRepositories(t)
	now := time.Date(2026, 7, 24, 8, 0, 0, 0, time.UTC)
	seedScanWorker(t, repositories, now)
	wantNetwork := map[string]any{
		"vpc_id":             "vpc-a",
		"vswitch_id":         "vsw-a",
		"security_group_ids": []any{"sg-a", "sg-b"},
	}
	adapter := &enrichingInventoryAdapter{
		pagedInventoryAdapter: pagedInventoryAdapter{pages: []contracts.InventoryBatch{{
			Items:    []contracts.InventoryItem{{NativeType: workerKind().NativeType, NativeID: "i-enriched", ResourceKind: workerKind(), Raw: map[string]any{"InstanceId": "i-enriched"}}},
			Complete: true,
		}}},
		enrich: func(_ context.Context, _ contracts.InventoryRequest, items []contracts.InventoryItem) ([]contracts.InventoryItem, error) {
			items[0].Normalized = wantNetwork
			return items, nil
		},
	}
	handler := inventory.NewScanHandler(repositories, inventoryRuntime{adapter: adapter}, inventory.NewService(repositories.Inventory(), inventory.WithClock(func() time.Time { return now })))

	if err := handler.Handle(ctx, execution.Job{ID: "job-enriched", Type: execution.JobScan, Payload: map[string]any{"scan_shard_id": "shard-worker"}}); err != nil {
		t.Fatal(err)
	}
	identity, err := asset.NewIdentity(string(asset.ProviderAliCloud), "public", "connection-worker", workerKind().NativeType, "i-enriched")
	if err != nil {
		t.Fatal(err)
	}
	identity.ScopeKey = "region:cn-hangzhou"
	stored, err := repositories.Inventory().GetAssetByIdentity(ctx, identity)
	if err != nil || !reflect.DeepEqual(stored.Normalized, wantNetwork) {
		t.Fatalf("stored asset=%+v err=%v want normalized=%+v", stored, err, wantNetwork)
	}
}

func TestScanHandlerFailsShardWhenBatchEnrichmentFails(t *testing.T) {
	ctx := context.Background()
	repositories := openInventoryWorkerRepositories(t)
	now := time.Date(2026, 7, 24, 8, 1, 0, 0, time.UTC)
	seedScanWorker(t, repositories, now)
	detailFailure := errors.New("detail failed")
	adapter := &enrichingInventoryAdapter{
		pagedInventoryAdapter: pagedInventoryAdapter{pages: []contracts.InventoryBatch{{
			Items:    []contracts.InventoryItem{{NativeType: workerKind().NativeType, NativeID: "i-unenriched", ResourceKind: workerKind(), Raw: map[string]any{"InstanceId": "i-unenriched"}}},
			Complete: true,
		}}},
		enrich: func(context.Context, contracts.InventoryRequest, []contracts.InventoryItem) ([]contracts.InventoryItem, error) {
			return nil, detailFailure
		},
	}
	handler := inventory.NewScanHandler(repositories, inventoryRuntime{adapter: adapter}, inventory.NewService(repositories.Inventory(), inventory.WithClock(func() time.Time { return now })))

	err := handler.Handle(ctx, execution.Job{ID: "job-enrichment-failed", Type: execution.JobScan, Payload: map[string]any{"scan_shard_id": "shard-worker"}})
	if !errors.Is(err, detailFailure) {
		t.Fatalf("Handle() error=%v", err)
	}
	shard, err := repositories.Inventory().GetScanShard(ctx, "shard-worker")
	if err != nil || shard.Status != asset.ShardFailed || shard.Coverage.FailureReason != detailFailure.Error() {
		t.Fatalf("shard=%+v err=%v", shard, err)
	}
	page, err := repositories.Inventory().ListAssets(ctx, persistence.ListOptions{Limit: 10})
	if err != nil || len(page.Items) != 1 || page.Items[0].Identity.NativeID != "i-unenriched" {
		t.Fatalf("assets=%+v err=%v", page.Items, err)
	}
}

func TestScanHandlerEmitsOrderedSanitizedLifecycleLogs(t *testing.T) {
	repositories := openInventoryWorkerRepositories(t)
	now := time.Date(2026, 7, 13, 8, 0, 15, 0, time.UTC)
	seedScanWorker(t, repositories, now)
	adapter := &pagedInventoryAdapter{pages: []contracts.InventoryBatch{
		{Items: []contracts.InventoryItem{{NativeType: workerKind().NativeType, NativeID: "i-1", ResourceKind: workerKind(), Raw: map[string]any{"credential": "must-not-appear"}}}, NextCursor: "page-2", RequestID: "req-1"},
		{Items: []contracts.InventoryItem{{NativeType: workerKind().NativeType, NativeID: "i-2", ResourceKind: workerKind(), Raw: map[string]any{"credential": "must-not-appear"}}}, Complete: true, RequestID: "req-2"},
	}}
	var logs []capturedJobLog
	ctx := execution.WithJobLogSink(context.Background(), execution.JobLogSinkFunc(
		func(_ context.Context, entry execution.JobLogEntry) {
			logs = append(logs, capturedJobLog{kind: entry.Kind, level: entry.Level, message: entry.Message, payload: entry.Payload})
		},
	))
	handler := inventory.NewScanHandler(
		repositories,
		inventoryRuntime{adapter: adapter},
		inventory.NewService(repositories.Inventory(), inventory.WithClock(func() time.Time { return now })),
	)

	if err := handler.Handle(ctx, execution.Job{
		ID: "job-logs", Type: execution.JobScan, TargetKey: "region:cn-hangzhou",
		Payload: map[string]any{"scan_shard_id": "shard-worker"},
	}); err != nil {
		t.Fatal(err)
	}

	wantMessages := []string{
		"scan target started with 1 source",
		"projected 1 resource from resource-center for ACS::ECS::Instance; 1 accumulated; request req-1",
		"projected 1 resource from resource-center for ACS::ECS::Instance; 2 accumulated; request req-2",
		"scan target completed with 1 source",
	}
	if len(logs) != len(wantMessages) {
		t.Fatalf("logs=%#v", logs)
	}
	for index, want := range wantMessages {
		if logs[index].kind != execution.JobLogText || logs[index].message != want || logs[index].payload != nil {
			t.Fatalf("log[%d]=%#v want message %q", index, logs[index], want)
		}
	}
}

func TestScanHandlerProjectsOnlySelectedNetworkDependencyClosure(t *testing.T) {
	ctx := context.Background()
	repositories := openInventoryWorkerRepositories(t)
	now := time.Date(2026, 7, 13, 8, 0, 30, 0, time.UTC)
	seedScanWorker(t, repositories, now)
	task, err := repositories.Inventory().GetScanRun(ctx, "run-worker")
	if err != nil {
		t.Fatal(err)
	}
	task.ScopeMode = asset.ScanSelectedNetworks
	task.Targets = []asset.ScanTarget{{Key: "vpc:cn-hangzhou:vpc-a", Kind: asset.ScanTargetVPC, RegionID: "cn-hangzhou", NativeID: "vpc-a", Name: "production"}}
	if err := repositories.Inventory().PutScanRun(ctx, task); err != nil {
		t.Fatal(err)
	}
	shard, err := repositories.Inventory().GetScanShard(ctx, "shard-worker")
	if err != nil {
		t.Fatal(err)
	}
	shard.TargetKey = task.Targets[0].Key
	if err := repositories.Inventory().PutScanShard(ctx, shard); err != nil {
		t.Fatal(err)
	}
	adapter := &pagedInventoryAdapter{pages: []contracts.InventoryBatch{{Items: []contracts.InventoryItem{
		{NativeType: workerKind().NativeType, NativeID: "vpc-a", ResourceKind: workerKind(), Raw: map[string]any{"VpcId": "vpc-a"}},
		{NativeType: workerKind().NativeType, NativeID: "i-a", ResourceKind: workerKind(), Raw: map[string]any{"VpcId": "vpc-a"}},
		{NativeType: workerKind().NativeType, NativeID: "i-other", ResourceKind: workerKind(), Raw: map[string]any{"VpcId": "vpc-other"}},
	}, Complete: true}}}
	handler := inventory.NewScanHandler(repositories, inventoryRuntime{adapter: adapter}, inventory.NewService(repositories.Inventory(), inventory.WithClock(func() time.Time { return now })))
	if err := handler.Handle(ctx, execution.Job{ID: "job-network", Type: execution.JobScan, TargetKey: task.Targets[0].Key, Payload: map[string]any{"scan_shard_id": "shard-worker"}}); err != nil {
		t.Fatal(err)
	}
	page, err := repositories.Inventory().ListAssets(ctx, persistence.ListOptions{Limit: 10})
	if err != nil || len(page.Items) != 2 {
		t.Fatalf("assets=%+v err=%v", page.Items, err)
	}
	if len(adapter.requests) != 1 || adapter.requests[0].NetworkTarget == nil || adapter.requests[0].NetworkTarget.NativeID != "vpc-a" {
		t.Fatalf("requests=%+v", adapter.requests)
	}
}

func TestScanHandlerPersistsFailedShardAndTerminalRun(t *testing.T) {
	ctx := context.Background()
	repositories := openInventoryWorkerRepositories(t)
	now := time.Date(2026, 7, 13, 8, 1, 0, 0, time.UTC)
	seedScanWorker(t, repositories, now)
	providerFailure := errors.New("inventory API unavailable")
	service := inventory.NewService(repositories.Inventory(), inventory.WithClock(func() time.Time { return now }))
	handler := inventory.NewScanHandler(repositories, inventoryRuntime{adapter: &pagedInventoryAdapter{err: providerFailure}}, service)

	err := handler.Handle(ctx, execution.Job{ID: "job-scan", Type: execution.JobScan, Payload: map[string]any{"scan_shard_id": "shard-worker"}})
	if !errors.Is(err, providerFailure) {
		t.Fatalf("handle error = %v", err)
	}
	shard, err := repositories.Inventory().GetScanShard(ctx, "shard-worker")
	if err != nil || shard.Status != asset.ShardFailed || shard.Coverage.FailureReason != providerFailure.Error() {
		t.Fatalf("shard = %+v, err = %v", shard, err)
	}
	run, err := repositories.Inventory().GetScanRun(ctx, "run-worker")
	if err != nil || run.Status != asset.ScanFailed || run.FinishedAt == nil {
		t.Fatalf("run = %+v, err = %v", run, err)
	}
	if _, err := repositories.Jobs().GetJobByIdempotencyKey(ctx, "scan-graph:run-worker"); !errors.Is(err, persistence.ErrNotFound) {
		t.Fatalf("failed scan enqueued graph rebuild: %v", err)
	}
}

func TestScanHandlerContinuesRemainingSourcesAfterOneShardFails(t *testing.T) {
	ctx := context.Background()
	repositories := openInventoryWorkerRepositories(t)
	now := time.Date(2026, 8, 3, 13, 30, 0, 0, time.UTC)
	seedScanWorker(t, repositories, now)
	run, err := repositories.Inventory().GetScanRun(ctx, "run-worker")
	if err != nil {
		t.Fatal(err)
	}
	run.RetryGeneration = 1
	if err := repositories.Inventory().PutScanRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	first, err := repositories.Inventory().GetScanShard(ctx, "shard-worker")
	if err != nil {
		t.Fatal(err)
	}
	first.RetryGeneration = 1
	if err := repositories.Inventory().PutScanShard(ctx, first); err != nil {
		t.Fatal(err)
	}
	second := asset.ScanShard{
		ID: "shard-worker-second", ScanRunID: "run-worker", Provider: asset.ProviderAliCloud,
		Source: "product-api", ScopeID: "scope-worker", ResourceKindID: "kind-worker",
		Authoritative: true, Status: asset.ShardPending, RetryGeneration: 1, CreatedAt: now,
	}
	if err := repositories.Inventory().PutScanShard(ctx, second); err != nil {
		t.Fatal(err)
	}
	originalGraphFinishedAt := now.Add(-time.Minute)
	if err := repositories.Jobs().Enqueue(ctx, execution.Job{
		ID: "job-original-graph", ConnectionID: "connection-worker",
		IdempotencyKey: "scan-graph:run-worker", AggregateType: "scan_task", AggregateID: "run-worker",
		Type: execution.JobGraph, Status: execution.JobSucceeded,
		Payload: map[string]any{"scan_run_id": "run-worker"}, RunAt: now.Add(-time.Minute),
		CreatedAt: now.Add(-time.Minute), UpdatedAt: originalGraphFinishedAt, FinishedAt: &originalGraphFinishedAt,
	}); err != nil {
		t.Fatal(err)
	}
	providerFailure := &contracts.ProviderCallError{Provider: execution.ProviderError{
		Category:  execution.ErrorRetryable,
		Code:      "ServiceUnavailable",
		Message:   "one product API is temporarily unavailable",
		RequestID: "provider-request",
	}}
	adapter := &pagedInventoryAdapter{
		errors: []error{providerFailure, nil},
		pages:  []contracts.InventoryBatch{{}, {Complete: true}},
	}
	handler := inventory.NewScanHandler(
		repositories,
		inventoryRuntime{adapter: adapter},
		inventory.NewService(repositories.Inventory(), inventory.WithClock(func() time.Time { return now })),
	)
	var logs []capturedJobLog
	ctx = execution.WithJobLogSink(ctx, execution.JobLogSinkFunc(
		func(_ context.Context, entry execution.JobLogEntry) {
			logs = append(logs, capturedJobLog{
				kind: entry.Kind, level: entry.Level, message: entry.Message, payload: entry.Payload,
			})
		},
	))

	err = handler.Handle(ctx, execution.Job{
		ID: "job-multi-source", Type: execution.JobScan,
		Payload: map[string]any{"scan_shard_ids": []any{"shard-worker", "shard-worker-second"}},
	})
	if !errors.Is(err, providerFailure) {
		t.Fatalf("Handle() error=%v, want provider failure", err)
	}
	if !strings.Contains(err.Error(), "request_id=provider-request") {
		t.Fatalf("Handle() error omitted provider request ID: %v", err)
	}
	foundFailureLog := false
	for _, entry := range logs {
		if entry.level == "warn" &&
			strings.Contains(entry.message, "scan source shard-worker failed") &&
			strings.Contains(entry.message, "request_id=provider-request") {
			foundFailureLog = true
			break
		}
	}
	if !foundFailureLog {
		t.Fatalf("scan source failure log omitted provider request ID: %#v", logs)
	}
	if len(adapter.requests) != 2 {
		t.Fatalf("provider calls=%d, want both sources attempted", len(adapter.requests))
	}
	first, err = repositories.Inventory().GetScanShard(ctx, "shard-worker")
	if err != nil || first.Status != asset.ShardFailed {
		t.Fatalf("first shard=%+v err=%v", first, err)
	}
	second, err = repositories.Inventory().GetScanShard(ctx, "shard-worker-second")
	if err != nil || second.Status != asset.ShardSucceeded {
		t.Fatalf("second shard=%+v err=%v", second, err)
	}
	run, err = repositories.Inventory().GetScanRun(ctx, "run-worker")
	if err != nil || run.Status != asset.ScanReconciling || run.CompletionStatus != asset.ScanPartial || run.FinishedAt != nil {
		t.Fatalf("run=%+v err=%v", run, err)
	}
	graphJob, err := repositories.Jobs().GetJobByIdempotencyKey(ctx, "scan-graph:run-worker:retry:1")
	if err != nil || graphJob.Type != execution.JobGraph ||
		graphJob.Status != execution.JobPending || graphJob.RetryGeneration != 1 {
		t.Fatalf("partial retry did not enqueue generation-specific graph rebuild: job=%+v err=%v", graphJob, err)
	}
}

func TestScanHandlerSkipsExplicitlyUnsupportedProductRegion(t *testing.T) {
	ctx := context.Background()
	repositories := openInventoryWorkerRepositories(t)
	now := time.Date(2026, 7, 13, 8, 1, 30, 0, time.UTC)
	seedScanWorker(t, repositories, now)
	unsupported := &contracts.ProviderCallError{Provider: execution.ProviderError{
		Category: execution.ErrorUnsupported,
		Code:     "UnsupportedOperation",
		Message:  "product is not available in this region",
		Summary:  map[string]any{"skip_reason": string(asset.SkipProviderRegionUnavailable)},
	}}
	service := inventory.NewService(repositories.Inventory(), inventory.WithClock(func() time.Time { return now }))
	handler := inventory.NewScanHandler(repositories, inventoryRuntime{adapter: &pagedInventoryAdapter{err: unsupported}}, service)

	if err := handler.Handle(ctx, execution.Job{ID: "job-scan", Type: execution.JobScan, Payload: map[string]any{"scan_shard_id": "shard-worker"}}); err != nil {
		t.Fatal(err)
	}
	shard, err := repositories.Inventory().GetScanShard(ctx, "shard-worker")
	if err != nil || shard.Status != asset.ShardSkipped || shard.Coverage.SkipReason != asset.SkipProviderRegionUnavailable || shard.Coverage.Complete || shard.Authoritative {
		t.Fatalf("shard = %+v, err = %v", shard, err)
	}
	run, err := repositories.Inventory().GetScanRun(ctx, "run-worker")
	if err != nil || run.Status != asset.ScanReconciling || run.CompletionStatus != asset.ScanSucceeded || run.FinishedAt != nil {
		t.Fatalf("run = %+v, err = %v", run, err)
	}
	if _, err := repositories.Jobs().GetJobByIdempotencyKey(ctx, "scan-graph:run-worker"); err != nil {
		t.Fatalf("skipped scan did not enqueue graph rebuild: %v", err)
	}
}

func TestScanHandlerDoesNotRepeatProviderCallForTerminalShard(t *testing.T) {
	ctx := context.Background()
	repositories := openInventoryWorkerRepositories(t)
	now := time.Date(2026, 7, 13, 8, 2, 0, 0, time.UTC)
	seedScanWorker(t, repositories, now)
	shard, err := repositories.Inventory().GetScanShard(ctx, "shard-worker")
	if err != nil {
		t.Fatal(err)
	}
	shard.Status = asset.ShardSucceeded
	shard.Coverage.Complete = true
	if err := repositories.Inventory().PutScanShard(ctx, shard); err != nil {
		t.Fatal(err)
	}
	adapter := &pagedInventoryAdapter{pages: []contracts.InventoryBatch{{Complete: true}}}
	service := inventory.NewService(repositories.Inventory(), inventory.WithClock(func() time.Time { return now }))
	handler := inventory.NewScanHandler(repositories, inventoryRuntime{adapter: adapter}, service)

	if err := handler.Handle(ctx, execution.Job{ID: "job-reclaimed", Type: execution.JobScan, Payload: map[string]any{"scan_shard_id": "shard-worker"}}); err != nil {
		t.Fatal(err)
	}
	if len(adapter.requests) != 0 {
		t.Fatalf("provider was called again after terminal shard: %+v", adapter.requests)
	}
	run, err := repositories.Inventory().GetScanRun(ctx, "run-worker")
	if err != nil || run.Status != asset.ScanReconciling || run.CompletionStatus != asset.ScanSucceeded || run.FinishedAt != nil {
		t.Fatalf("run=%+v err=%v", run, err)
	}
	if _, err := repositories.Jobs().GetJobByIdempotencyKey(ctx, "scan-graph:run-worker"); err != nil {
		t.Fatalf("terminal shard did not recover graph job enqueue: %v", err)
	}
}

func TestScanHandlerRecoversGraphEnqueueAfterRunAlreadyFinished(t *testing.T) {
	ctx := context.Background()
	repositories := openInventoryWorkerRepositories(t)
	now := time.Date(2026, 7, 13, 8, 3, 0, 0, time.UTC)
	seedScanWorker(t, repositories, now)
	shard, err := repositories.Inventory().GetScanShard(ctx, "shard-worker")
	if err != nil {
		t.Fatal(err)
	}
	shard.Status = asset.ShardSucceeded
	shard.Coverage.Complete = true
	if err := repositories.Inventory().PutScanShard(ctx, shard); err != nil {
		t.Fatal(err)
	}
	run, err := repositories.Inventory().GetScanRun(ctx, "run-worker")
	if err != nil {
		t.Fatal(err)
	}
	run.Status = asset.ScanSucceeded
	run.FinishedAt = &now
	if err := repositories.Inventory().PutScanRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	adapter := &pagedInventoryAdapter{pages: []contracts.InventoryBatch{{Complete: true}}}
	service := inventory.NewService(repositories.Inventory(), inventory.WithClock(func() time.Time { return now }))
	handler := inventory.NewScanHandler(repositories, inventoryRuntime{adapter: adapter}, service)

	if err := handler.Handle(ctx, execution.Job{ID: "job-retry", Type: execution.JobScan, Payload: map[string]any{"scan_shard_id": "shard-worker"}}); err != nil {
		t.Fatal(err)
	}
	if len(adapter.requests) != 0 {
		t.Fatalf("provider was called again after terminal run: %+v", adapter.requests)
	}
	if _, err := repositories.Jobs().GetJobByIdempotencyKey(ctx, "scan-graph:run-worker"); err != nil {
		t.Fatalf("finished run did not recover graph job enqueue: %v", err)
	}
}

func TestScanHandlerPausesAtBatchCheckpoint(t *testing.T) {
	ctx := context.Background()
	repositories := openInventoryWorkerRepositories(t)
	now := time.Date(2026, 7, 13, 8, 4, 0, 0, time.UTC)
	seedScanWorker(t, repositories, now)
	adapter := &pagedInventoryAdapter{pages: []contracts.InventoryBatch{{Items: []contracts.InventoryItem{{NativeType: workerKind().NativeType, NativeID: "i-checkpoint", ResourceKind: workerKind(), Raw: map[string]any{"InstanceId": "i-checkpoint"}}}, NextCursor: "next"}}}
	adapter.onList = func() {
		task, err := repositories.Inventory().GetScanRun(ctx, "run-worker")
		if err != nil {
			t.Fatal(err)
		}
		task.Status = asset.ScanPausing
		task.ControlVersion++
		if err := repositories.Inventory().PutScanRun(ctx, task); err != nil {
			t.Fatal(err)
		}
	}
	handler := inventory.NewScanHandler(repositories, inventoryRuntime{adapter: adapter}, inventory.NewService(repositories.Inventory(), inventory.WithClock(func() time.Time { return now })))
	err := handler.Handle(ctx, execution.Job{ID: "job-checkpoint", Type: execution.JobScan, Payload: map[string]any{"scan_shard_id": "shard-worker"}})
	var controlled *execution.JobStatusError
	if !errors.As(err, &controlled) || controlled.Status != execution.JobPaused {
		t.Fatalf("Handle() error = %v", err)
	}
	shard, err := repositories.Inventory().GetScanShard(ctx, "shard-worker")
	if err != nil || shard.Status != asset.ShardPaused || shard.Coverage.ItemCount != 1 {
		t.Fatalf("paused shard = %+v, err = %v", shard, err)
	}
	task, err := repositories.Inventory().GetScanRun(ctx, "run-worker")
	if err != nil || task.Status != asset.ScanPaused {
		t.Fatalf("paused task = %+v, err = %v", task, err)
	}
}

type pagedInventoryAdapter struct {
	sources  []contracts.InventorySource
	pages    []contracts.InventoryBatch
	requests []contracts.InventoryRequest
	err      error
	errors   []error
	onList   func()
}

func (a *pagedInventoryAdapter) InventorySources() []contracts.InventorySource { return a.sources }

func TestScanHandlerKnownIDsAreScopedAndFixedAcrossPages(t *testing.T) {
	for _, reconcile := range []bool{false, true} {
		t.Run(map[bool]string{false: "ordinary-source", true: "bounded-source"}[reconcile], func(t *testing.T) {
			ctx, now := t.Context(), time.Now().UTC()
			repositories := openInventoryWorkerRepositories(t)
			seedScanWorker(t, repositories, now)
			put := func(name string, modify func(*asset.Asset)) {
				t.Helper()
				value := asset.Asset{ID: asset.AssetID(name), ResourceKindID: workerKind().ID, ScopeID: "scope-worker", Identity: asset.Identity{Provider: asset.ProviderAliCloud, ConnectionID: "connection-worker", Partition: "public", NativeType: workerKind().NativeType, NativeID: name}, FirstSeenAt: now, LastSeenAt: now}
				if modify != nil {
					modify(&value)
				}
				if err := repositories.Inventory().PutAsset(ctx, value); err != nil {
					t.Fatal(err)
				}
			}
			put("i-z", func(v *asset.Asset) {
				v.Normalized = map[string]any{"selector": map[string]any{"name": "CaseSensitive", "proof": "original"}}
			})
			put("i-a", nil)
			put("other-region", func(v *asset.Asset) { v.Location = "cn-beijing" })
			put("other-provider", func(v *asset.Asset) { v.Identity.Provider = asset.ProviderAzure })
			put("other-connection", func(v *asset.Asset) { v.Identity.ConnectionID = "another" })
			put("other-partition", func(v *asset.Asset) { v.Identity.Partition = "another" })
			put("other-type", func(v *asset.Asset) { v.Identity.NativeType = "another" })
			put("other-kind", func(v *asset.Asset) { v.ResourceKindID = "another" })
			put("closed", func(v *asset.Asset) { v.ClosedAt = &now })
			put("deleted", func(v *asset.Asset) { v.DeletedAt, v.ClosedAt = &now, &now })
			adapter := &pagedInventoryAdapter{
				sources: []contracts.InventorySource{{Name: "resource-center", ReconcileKnownIDs: reconcile}},
				pages:   []contracts.InventoryBatch{{NextCursor: "next"}, {Complete: true}},
			}
			adapter.onList = func() {
				put("created-between-pages", nil)
				if reconcile {
					if want := []string{"i-a", "i-z", "other-region"}; !reflect.DeepEqual(adapter.requests[0].KnownNativeIDs, want) {
						t.Fatalf("known IDs = %v, want %v", adapter.requests[0].KnownNativeIDs, want)
					}
					// A provider must not mutate the worker's next-page baseline.
					adapter.requests[0].KnownNativeIDs[0] = "mutated-provider-copy"
					if len(adapter.requests[0].KnownNativeMetadata) != len(wantKnownMetadata()) {
						t.Fatal("known metadata included an unrelated resource", adapter.requests[0].KnownNativeMetadata)
					}
					adapter.requests[0].KnownNativeMetadata["i-z"]["selector"].(map[string]any)["name"] = "mutated-provider-copy"
					delete(adapter.requests[0].KnownNativeMetadata, "i-a")
				}
			}
			handler := inventory.NewScanHandler(repositories, inventoryRuntime{adapter}, inventory.NewService(repositories.Inventory()))
			if err := handler.Handle(ctx, execution.Job{Payload: map[string]any{"scan_shard_id": "shard-worker"}}); err != nil {
				t.Fatal(err)
			}
			var want []string
			if reconcile {
				want = []string{"i-a", "i-z", "other-region"}
			}
			if len(adapter.requests) != 2 || !reflect.DeepEqual(adapter.requests[1].KnownNativeIDs, want) || !reconcile && adapter.requests[0].KnownNativeIDs != nil {
				t.Fatal("known ID set changed across pages or ordinary source received IDs", adapter.requests)
			}
			if reconcile && !reflect.DeepEqual(adapter.requests[1].KnownNativeMetadata, wantKnownMetadata()) || !reconcile && (adapter.requests[0].KnownNativeMetadata != nil || adapter.requests[1].KnownNativeMetadata != nil) {
				t.Fatal("known native selectors changed across pages or leaked to an ordinary source", adapter.requests[1].KnownNativeMetadata)
			}
			shard, err := repositories.Inventory().GetScanShard(ctx, "shard-worker")
			if err != nil || shard.Authoritative == reconcile || shard.Status != asset.ShardSucceeded {
				t.Fatal("source authority did not override stale bounded-source state", shard, err)
			}
			if reconcile {
				value, err := repositories.Inventory().GetAsset(ctx, "i-a")
				if err != nil || value.ClosedAt != nil || !value.LastSeenAt.Equal(now) {
					t.Fatal("empty bounded scan closed/refreshed an unobserved asset", value, err)
				}
			}
		})
	}
}

func wantKnownMetadata() map[string]map[string]any {
	return map[string]map[string]any{"i-a": {}, "i-z": {"selector": map[string]any{"name": "CaseSensitive", "proof": "original"}}, "other-region": {}}
}

func TestScanHandlerClosesOnlyConfirmedUnchangedKnownAssets(t *testing.T) {
	for _, mode := range []string{"native-absence", "network-native-absence", "omission", "unknown", "ordinary-source", "duplicate", "partial-page", "final-cursor", "observed-page", "observed-earlier-page", "failed-page", "canceled", "refreshed", "newer-observation", "new-record", "new-record-as-absent", "outside-coverage", "ambiguous-id", "other-connection", "other-provider", "other-partition", "other-kind"} {
		t.Run(mode, func(t *testing.T) {
			ctx, now := t.Context(), time.Now().UTC()
			repositories := openInventoryWorkerRepositories(t)
			seedScanWorker(t, repositories, now)
			put := func(id string, change func(*asset.Asset)) asset.Asset {
				t.Helper()
				value := asset.Asset{ID: asset.AssetID(id), Identity: asset.Identity{Provider: asset.ProviderAliCloud, ConnectionID: "connection-worker", Partition: "public", NativeType: workerKind().NativeType, NativeID: id, ScopeKey: "region:cn-hangzhou"}, ResourceKindID: workerKind().ID, ScopeID: "scope-worker", FirstSeenAt: now, LastSeenAt: now}
				if change != nil {
					change(&value)
				}
				if err := repositories.Inventory().PutAsset(ctx, value); err != nil {
					t.Fatal(err)
				}
				return value
			}
			if err := repositories.Inventory().PutScope(ctx, asset.Scope{ID: "outside", ConnectionID: "connection-worker", Kind: asset.ScopeRegion, NativeID: "cn-beijing", CreatedAt: now, UpdatedAt: now}); err != nil {
				t.Fatal(err)
			}
			put("target", func(v *asset.Asset) {
				if mode == "outside-coverage" {
					v.ScopeID = "outside"
				}
			})
			put("unobserved", nil)
			if mode == "ambiguous-id" {
				put("same-id-another-region", func(v *asset.Asset) {
					v.Identity.NativeID, v.Identity.ScopeKey, v.ScopeID = "target", "region:cn-beijing", "outside"
				})
			}
			if strings.HasPrefix(mode, "other-") {
				put("foreign", func(v *asset.Asset) {
					switch mode {
					case "other-connection":
						v.Identity.ConnectionID = "another"
					case "other-provider":
						v.Identity.Provider = asset.ProviderAzure
					case "other-partition":
						v.Identity.Partition = "another"
					case "other-kind":
						v.ResourceKindID = "another"
					}
				})
			}
			if mode == "network-native-absence" {
				run, _ := repositories.Inventory().GetScanRun(ctx, "run-worker")
				run.ScopeMode = asset.ScanSelectedNetworks
				target := asset.ScanTarget{Key: "vpc:cn-hangzhou:network", Kind: asset.ScanTargetVPC, NativeID: "network", RegionID: "cn-hangzhou"}
				run.Targets = []asset.ScanTarget{target}
				if err := repositories.Inventory().PutScanRun(ctx, run); err != nil {
					t.Fatal(err)
				}
				shard, _ := repositories.Inventory().GetScanShard(ctx, "shard-worker")
				shard.TargetKey = target.Key
				if err := repositories.Inventory().PutScanShard(ctx, shard); err != nil {
					t.Fatal(err)
				}
			}
			final := contracts.InventoryBatch{Complete: true, AbsentNativeIDs: []string{"target"}}
			adapter := &pagedInventoryAdapter{sources: []contracts.InventorySource{{Name: "resource-center", ReconcileKnownIDs: mode != "ordinary-source"}}, pages: []contracts.InventoryBatch{final}}
			item := contracts.InventoryItem{NativeType: workerKind().NativeType, NativeID: "target", ResourceKind: workerKind()}
			switch mode {
			case "omission":
				adapter.pages[0].AbsentNativeIDs = nil
			case "unknown", "new-record-as-absent":
				adapter.pages[0].AbsentNativeIDs = []string{"late"}
			case "duplicate":
				adapter.pages[0].AbsentNativeIDs = []string{"target", "target"}
			case "partial-page":
				adapter.pages[0].Complete, adapter.pages[0].NextCursor = false, "next"
			case "final-cursor":
				adapter.pages[0].NextCursor = "next"
			case "observed-page":
				adapter.pages[0].Items = []contracts.InventoryItem{item}
			case "observed-earlier-page":
				adapter.pages = []contracts.InventoryBatch{{NextCursor: "next", Items: []contracts.InventoryItem{item}}, final}
			case "failed-page":
				adapter.pages = []contracts.InventoryBatch{{NextCursor: "next"}, final}
				adapter.errors = []error{nil, errors.New("native read denied")}
			}
			if strings.HasPrefix(mode, "other-") {
				adapter.pages[0].AbsentNativeIDs = []string{"foreign"}
			}
			adapter.onList = func() {
				switch mode {
				case "new-record", "new-record-as-absent":
					put("late", nil)
				case "refreshed":
					current, _ := repositories.Inventory().GetAsset(ctx, "target")
					current.LastSeenAt = now.Add(time.Hour)
					if err := repositories.Inventory().PutAsset(ctx, current); err != nil {
						t.Fatal(err)
					}
				case "newer-observation":
					connection, err := repositories.Connections().GetConnection(ctx, "connection-worker")
					if err != nil {
						t.Fatal(err)
					}
					shard := asset.ScanShard{ID: "concurrent-observation", ScanRunID: "run-worker", Provider: asset.ProviderAliCloud, Source: "product-api", ScopeID: "scope-worker", ResourceKindID: workerKind().ID, Status: asset.ShardRunning, CreatedAt: now}
					if err := inventory.NewService(repositories.Inventory()).ProjectBatch(ctx, &shard, connection, contracts.InventoryBatch{Items: []contracts.InventoryItem{item}, Complete: true}, inventory.ProjectionOptions{}); err != nil {
						t.Fatal(err)
					}
					current, err := repositories.Inventory().GetAsset(ctx, "target")
					if err != nil || current.CurrentObservationID == "" {
						t.Fatal("concurrent scan did not refresh the same asset", current, err)
					}
				case "canceled":
					run, _ := repositories.Inventory().GetScanRun(ctx, "run-worker")
					run.Status = asset.ScanCanceling
					if err := repositories.Inventory().PutScanRun(ctx, run); err != nil {
						t.Fatal(err)
					}
				}
			}
			handler := inventory.NewScanHandler(repositories, inventoryRuntime{adapter}, inventory.NewService(repositories.Inventory()))
			err := handler.Handle(ctx, execution.Job{Payload: map[string]any{"scan_shard_id": "shard-worker"}})
			wantSuccess := mode == "native-absence" || mode == "network-native-absence" || mode == "omission" || mode == "refreshed" || mode == "newer-observation" || mode == "new-record" || mode == "outside-coverage"
			if (err == nil) != wantSuccess {
				t.Fatal("unexpected native absence outcome", err)
			}
			closed := mode == "native-absence" || mode == "network-native-absence" || mode == "new-record"
			value, err := repositories.Inventory().GetAsset(ctx, "target")
			if err != nil || (value.ClosedAt != nil) != closed || value.DeletedAt != nil {
				t.Fatal("native absence closed the wrong observation or invented a cleanup tombstone", value, err)
			}
			for _, id := range []asset.AssetID{"unobserved", "late", "foreign", "same-id-another-region"} {
				value, err := repositories.Inventory().GetAsset(ctx, id)
				if err == nil && value.ClosedAt != nil {
					t.Fatal("native absence swept an unrelated record", id)
				}
			}
		})
	}
}

func TestConfirmedNativeAbsenceCommitsAtomicallyWithShard(t *testing.T) {
	ctx, now := t.Context(), time.Now().UTC()
	repositories := openInventoryWorkerRepositories(t)
	seedScanWorker(t, repositories, now)
	var baseline []asset.Asset
	for _, id := range []asset.AssetID{"first", "second"} {
		value := asset.Asset{ID: id, Identity: asset.Identity{Provider: asset.ProviderAliCloud, ConnectionID: "connection-worker", Partition: "public", NativeType: workerKind().NativeType, NativeID: string(id)}, ResourceKindID: workerKind().ID, ScopeID: "scope-worker", FirstSeenAt: now, LastSeenAt: now}
		if err := repositories.Inventory().PutAsset(ctx, value); err != nil {
			t.Fatal(err)
		}
		baseline = append(baseline, value)
	}
	shard, err := repositories.Inventory().GetScanShard(ctx, "shard-worker")
	if err != nil {
		t.Fatal(err)
	}
	shard.Authoritative = false
	service := inventory.NewService(repositories.Inventory())
	if err := service.FinishShard(ctx, &shard, asset.ShardFailed, "native query failed", baseline[0]); err == nil {
		t.Fatal("failed shard accepted native absence")
	}
	for _, mode := range []string{"foreign", "duplicate"} {
		second := baseline[1]
		if mode == "foreign" {
			second.Identity.ConnectionID = "another"
		} else {
			second = baseline[0]
		}
		if err := service.FinishShard(ctx, &shard, asset.ShardSucceeded, "", baseline[0], second); err == nil {
			t.Fatal("invalid native absence transaction succeeded", mode)
		}
		for _, original := range baseline {
			value, err := repositories.Inventory().GetAsset(ctx, original.ID)
			if err != nil || value.ClosedAt != nil {
				t.Fatal("failed native absence transaction partially closed assets", value, err)
			}
		}
		stored, err := repositories.Inventory().GetScanShard(ctx, shard.ID)
		if err != nil || stored.Status != asset.ShardPending {
			t.Fatal("failed native absence transaction marked shard complete", stored, err)
		}
	}
	if err := service.FinishShard(ctx, &shard, asset.ShardSucceeded, "", baseline[0]); err != nil {
		t.Fatal(err)
	}
	first, err := repositories.Inventory().GetAsset(ctx, baseline[0].ID)
	if err != nil || first.ClosedAt == nil || first.DeletedAt != nil || shard.Status != asset.ShardSucceeded || shard.Authoritative {
		t.Fatal("native absence did not commit with its non-authoritative shard", first, shard, err)
	}
}

type enrichingInventoryAdapter struct {
	pagedInventoryAdapter
	enrich func(context.Context, contracts.InventoryRequest, []contracts.InventoryItem) ([]contracts.InventoryItem, error)
}

func (a *enrichingInventoryAdapter) EnrichInventoryBatch(ctx context.Context, request contracts.InventoryRequest, items []contracts.InventoryItem) ([]contracts.InventoryItem, error) {
	return a.enrich(ctx, request, items)
}

func (a *pagedInventoryAdapter) List(_ context.Context, request contracts.InventoryRequest) (contracts.InventoryBatch, error) {
	a.requests = append(a.requests, request)
	if a.onList != nil {
		onList := a.onList
		a.onList = nil
		onList()
	}
	if a.err != nil {
		return contracts.InventoryBatch{}, a.err
	}
	index := len(a.requests) - 1
	if index < len(a.errors) && a.errors[index] != nil {
		return contracts.InventoryBatch{}, a.errors[index]
	}
	if index >= len(a.pages) {
		return contracts.InventoryBatch{Complete: true}, nil
	}
	return a.pages[index], nil
}

type inventoryRuntime struct{ adapter contracts.InventoryAdapter }

func (r inventoryRuntime) ResolveInventory(asset.Provider) (contracts.InventoryAdapter, error) {
	return r.adapter, nil
}

func openInventoryWorkerRepositories(t *testing.T) persistence.Repositories {
	t.Helper()
	repositories, err := sqlite.Open(filepath.Join(t.TempDir(), "inventory-worker.db"), filepath.Join("..", "..", "..", "migrations"))
	if err != nil {
		t.Fatal(err)
	}
	return repositories
}

func seedScanWorker(t *testing.T, repositories persistence.Repositories, now time.Time) {
	t.Helper()
	ctx := context.Background()
	values := []func() error{
		func() error {
			return repositories.Connections().PutConnection(ctx, asset.CloudConnection{ID: "connection-worker", Name: "scanner", Provider: asset.ProviderAliCloud, Partition: "public", Principal: "scanner", Status: asset.ConnectionActive, CreatedAt: now, UpdatedAt: now})
		},
		func() error {
			return repositories.Inventory().PutScope(ctx, asset.Scope{ID: "scope-worker", ConnectionID: "connection-worker", Kind: asset.ScopeRegion, NativeID: "cn-hangzhou", Name: "cn-hangzhou", CreatedAt: now, UpdatedAt: now})
		},
		func() error {
			return repositories.Inventory().PutResourceKind(ctx, workerKind())
		},
		func() error {
			return repositories.Inventory().CreateScanRun(ctx, asset.ScanRun{ID: "run-worker", ConnectionID: "connection-worker", Status: asset.ScanPending, RequestedBy: "tester", CreatedAt: now})
		},
		func() error {
			return repositories.Inventory().PutScanShard(ctx, asset.ScanShard{ID: "shard-worker", ScanRunID: "run-worker", Provider: asset.ProviderAliCloud, Source: "resource-center", ScopeID: "scope-worker", ResourceKindID: "kind-worker", Authoritative: true, Status: asset.ShardPending, CreatedAt: now})
		},
	}
	for _, write := range values {
		if err := write(); err != nil {
			t.Fatal(err)
		}
	}
}

func workerKind() asset.ResourceKind {
	return asset.ResourceKind{ID: "kind-worker", Provider: asset.ProviderAliCloud, NativeType: "ACS::ECS::Instance", DisplayName: "ECS", BundleRevision: "bundle-worker", Capabilities: asset.CapabilitySet{asset.CapabilityIndexed}}
}

type capturedJobLog struct {
	kind    execution.JobLogKind
	level   string
	message string
	payload map[string]any
}

type networkProductAdapter struct {
	kinds       map[asset.ResourceKindID]asset.ResourceKind
	failBackend bool
}

func (a *networkProductAdapter) InventorySources() []contracts.InventorySource {
	return []contracts.InventorySource{{Name: "product-api", AuthoritativeDefault: true}}
}
func (a *networkProductAdapter) List(_ context.Context, request contracts.InventoryRequest) (contracts.InventoryBatch, error) {
	if request.NetworkTarget == nil || request.ResourceKind == nil {
		return contracts.InventoryBatch{}, errors.New("missing network/kind selection")
	}
	kind := a.kinds[request.ResourceKind.ID]
	name, dependency := "backend", "network"
	switch kind.ID {
	case "kind-worker":
		if a.failBackend {
			return contracts.InventoryBatch{}, errors.New("backend API unavailable")
		}
	case "kind-map":
		name, dependency = "map", "backend"
	case "kind-proxy":
		name, dependency = "proxy", "map"
	}
	return contracts.InventoryBatch{Items: []contracts.InventoryItem{{NativeType: kind.NativeType, NativeID: name, ResourceKind: kind, Name: name, NetworkReferences: []string{dependency}, Normalized: map[string]any{"dependency": dependency}}}, Complete: true}, nil
}

func TestNetworkScanClosesAcrossProductShardsAndPreservesAssetsOnPartialFailure(t *testing.T) {
	ctx := context.Background()
	repositories := openInventoryWorkerRepositories(t)
	now := time.Date(2026, 9, 8, 1, 0, 0, 0, time.UTC)
	seedScanWorker(t, repositories, now)
	run, err := repositories.Inventory().GetScanRun(ctx, "run-worker")
	if err != nil {
		t.Fatal(err)
	}
	run.ScopeMode = asset.ScanSelectedNetworks
	target := asset.ScanTarget{Key: "vpc:cn-hangzhou:network", Kind: asset.ScanTargetVPC, NativeID: "network", RegionID: "cn-hangzhou"}
	run.Targets = []asset.ScanTarget{target}
	if err = repositories.Inventory().PutScanRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	kinds := map[asset.ResourceKindID]asset.ResourceKind{"kind-worker": workerKind()}
	for _, suffix := range []string{"map", "proxy"} {
		kind := workerKind()
		kind.ID = asset.ResourceKindID("kind-" + suffix)
		kind.NativeType = "TEST::" + suffix
		kinds[kind.ID] = kind
		if err = repositories.Inventory().PutResourceKind(ctx, kind); err != nil {
			t.Fatal(err)
		}
	}
	// Place the proxy first: no shard order can substitute for target closure.
	ids := []string{"shard-proxy", "shard-map", "shard-worker"}
	for i, kindID := range []asset.ResourceKindID{"kind-proxy", "kind-map", "kind-worker"} {
		shard := asset.ScanShard{ID: asset.ScanShardID(ids[i]), ScanRunID: run.ID, Provider: asset.ProviderAliCloud, ScopeID: "scope-worker", TargetKey: target.Key, ResourceKindID: kindID, Source: "product-api", Authoritative: true, Status: asset.ShardPending, CreatedAt: now}
		if err = repositories.Inventory().PutScanShard(ctx, shard); err != nil {
			t.Fatal(err)
		}
	}
	adapter := &networkProductAdapter{kinds: kinds}
	service := inventory.NewService(repositories.Inventory(), inventory.WithClock(func() time.Time { return now }))
	handler := inventory.NewScanHandler(repositories, inventoryRuntime{adapter: adapter}, service)
	job := execution.Job{ID: "network-job", Type: execution.JobScan, Payload: map[string]any{"scan_shard_ids": ids}}
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			now = now.Add(time.Hour)
			current, err := repositories.Inventory().GetScanRun(ctx, run.ID)
			if err != nil {
				t.Fatal(err)
			}
			current.Status = asset.ScanPending
			current.FinishedAt = nil
			current.RetryGeneration++
			if err = repositories.Inventory().PutScanRun(ctx, current); err != nil {
				t.Fatal(err)
			}
			for _, id := range ids {
				shard, err := repositories.Inventory().GetScanShard(ctx, asset.ScanShardID(id))
				if err != nil {
					t.Fatal(err)
				}
				// Keep successful statuses to exercise the target-resume path. Only the
				// failed source must be explicitly reset before a retry is authorized.
				if shard.Status == asset.ShardFailed {
					shard.Status = asset.ShardPending
				}
				shard.RetryGeneration = current.RetryGeneration
				if err = repositories.Inventory().PutScanShard(ctx, shard); err != nil {
					t.Fatal(err)
				}
			}
		}
		adapter.failBackend = attempt == 1
		err = handler.Handle(ctx, job)
		if (err != nil) != (attempt == 1) {
			t.Fatalf("attempt %d err=%v", attempt, err)
		}
		page, err := repositories.Inventory().ListAssets(ctx, persistence.ListOptions{Limit: 100})
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Items) != 3 {
			t.Fatalf("attempt %d lost a cross-kind dependent: %+v", attempt, page.Items)
		}
		for _, item := range page.Items {
			if item.ClosedAt != nil {
				t.Fatalf("attempt %d falsely closed %s", attempt, item.Name)
			}
		}
		proxy, err := repositories.Inventory().GetScanShard(ctx, "shard-proxy")
		if err != nil {
			t.Fatal(err)
		}
		if proxy.Status != asset.ShardSucceeded || proxy.Authoritative != (attempt != 1) {
			t.Fatalf("attempt %d proxy coverage=%+v", attempt, proxy)
		}
	}
}

func TestNonAuthoritativeNetworkSourceCannotClosePreviousObservations(t *testing.T) {
	ctx := context.Background()
	repositories := openInventoryWorkerRepositories(t)
	now := time.Date(2026, 9, 8, 1, 0, 0, 0, time.UTC)
	seedScanWorker(t, repositories, now)
	run, err := repositories.Inventory().GetScanRun(ctx, "run-worker")
	if err != nil {
		t.Fatal(err)
	}
	run.ScopeMode = asset.ScanSelectedNetworks
	target := asset.ScanTarget{Key: "vpc:cn-hangzhou:network", Kind: asset.ScanTargetVPC, NativeID: "network", RegionID: "cn-hangzhou"}
	run.Targets = []asset.ScanTarget{target}
	if err = repositories.Inventory().PutScanRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	shard, err := repositories.Inventory().GetScanShard(ctx, "shard-worker")
	if err != nil {
		t.Fatal(err)
	}
	shard.TargetKey = target.Key
	shard.Authoritative = false
	if err = repositories.Inventory().PutScanShard(ctx, shard); err != nil {
		t.Fatal(err)
	}
	connection, err := repositories.Connections().GetConnection(ctx, run.ConnectionID)
	if err != nil {
		t.Fatal(err)
	}
	service := inventory.NewService(repositories.Inventory(), inventory.WithClock(func() time.Time { return now }))
	item := contracts.InventoryItem{NativeType: workerKind().NativeType, NativeID: "old", ResourceKind: workerKind(), NetworkReferences: []string{"network"}}
	if err = service.ProjectBatch(ctx, &shard, connection, contracts.InventoryBatch{Items: []contracts.InventoryItem{item}}, inventory.ProjectionOptions{ObservedAt: now}); err != nil {
		t.Fatal(err)
	}
	// A new shard sees none of those resources after ownership moves to product
	// inventory. Broad inventory is not evidence that they disappeared.
	next := shard
	next.ID = "new-shard"
	next.Coverage = asset.Coverage{}
	next.Status = asset.ShardRunning
	if err = repositories.Inventory().PutScanShard(ctx, next); err != nil {
		t.Fatal(err)
	}
	if err = service.FinishShard(ctx, &next, asset.ShardSucceeded, ""); err != nil {
		t.Fatal(err)
	}
	page, err := repositories.Inventory().ListAssets(ctx, persistence.ListOptions{Limit: 10})
	if err != nil || len(page.Items) != 1 || page.Items[0].ClosedAt != nil {
		t.Fatalf("nonauthoritative source closed old observation: %+v err=%v", page, err)
	}
}
