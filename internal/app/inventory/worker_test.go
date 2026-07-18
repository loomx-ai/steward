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
	pages    []contracts.InventoryBatch
	requests []contracts.InventoryRequest
	err      error
	errors   []error
	onList   func()
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
