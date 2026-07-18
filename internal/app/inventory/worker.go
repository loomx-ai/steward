package inventory

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/idgen"
	"github.com/loomx-ai/steward/internal/persistence"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

type RuntimeRegistry interface {
	ResolveInventory(asset.Provider) (contracts.InventoryAdapter, error)
}

type ScanHandler struct {
	repositories persistence.Repositories
	runtimes     RuntimeRegistry
	service      *Service
	clock        func() time.Time
}

func NewScanHandler(repositories persistence.Repositories, runtimes RuntimeRegistry, service *Service) *ScanHandler {
	return &ScanHandler{repositories: repositories, runtimes: runtimes, service: service, clock: func() time.Time { return time.Now().UTC() }}
}

func (h *ScanHandler) Handle(ctx context.Context, job execution.Job) error {
	shardIDs := scanShardIDs(job.Payload)
	if len(shardIDs) == 0 || h == nil || h.repositories == nil || h.runtimes == nil || h.service == nil {
		return fmt.Errorf("scan handler requires target shards, repositories, runtime, and inventory service")
	}
	execution.LogJob(ctx, "info", fmt.Sprintf("scan target started with %s", countNoun(len(shardIDs), "source")))
	var run *asset.ScanRun
	shardErrors := make([]error, 0)
	for index, shardID := range shardIDs {
		current, err := h.handleShard(ctx, shardID)
		if current != nil {
			run = current
		}
		if err == nil {
			continue
		}
		var controlled *execution.JobStatusError
		if errors.As(err, &controlled) {
			if controlled.Status == execution.JobCanceled {
				if cancelErr := h.cancelShards(ctx, shardIDs[index+1:]); cancelErr != nil {
					err = errors.Join(err, cancelErr)
				}
			}
			if run != nil {
				err = errors.Join(err, h.settleControlledTask(ctx, run))
			}
			return err
		}
		failed, statusErr := h.shardFailedTerminally(ctx, shardID)
		if statusErr != nil {
			err = errors.Join(err, statusErr)
		}
		if failed && statusErr == nil {
			shardErrors = append(shardErrors, err)
			execution.LogJob(ctx, "warn", fmt.Sprintf(
				"scan source %s failed; continuing with the remaining sources: %v",
				shardID,
				err,
			))
			continue
		}
		if blockErr := h.blockShards(ctx, shardIDs[index+1:], err.Error()); blockErr != nil {
			err = errors.Join(err, blockErr)
		}
		if run != nil {
			err = errors.Join(err, h.finishRunIfTerminal(ctx, run))
		}
		return err
	}
	if run == nil {
		return fmt.Errorf("scan target resolved no task")
	}
	if err := h.checkTaskControl(ctx, run, nil); err != nil {
		return err
	}
	if err := h.finishRunIfTerminal(ctx, run); err != nil {
		return err
	}
	if len(shardErrors) > 0 {
		execution.LogJob(ctx, "warn", fmt.Sprintf(
			"scan target completed with %s and %s",
			countNoun(len(shardIDs)-len(shardErrors), "successful source"),
			countNoun(len(shardErrors), "failed source"),
		))
		return fmt.Errorf(
			"scan target completed with %s: %w",
			countNoun(len(shardErrors), "failed source"),
			errors.Join(shardErrors...),
		)
	}
	execution.LogJob(ctx, "info", fmt.Sprintf("scan target completed with %s", countNoun(len(shardIDs), "source")))
	return nil
}

func (h *ScanHandler) shardFailedTerminally(ctx context.Context, shardID asset.ScanShardID) (bool, error) {
	shard, err := h.repositories.Inventory().GetScanShard(ctx, shardID)
	if err != nil {
		return false, err
	}
	return shard.Status == asset.ShardFailed, nil
}

func (h *ScanHandler) handleShard(ctx context.Context, shardID asset.ScanShardID) (*asset.ScanRun, error) {
	shard, err := h.repositories.Inventory().GetScanShard(ctx, shardID)
	if err != nil {
		return nil, err
	}
	run, err := h.repositories.Inventory().GetScanRun(ctx, shard.ScanRunID)
	if err != nil {
		return nil, err
	}
	if err := h.checkTaskControl(ctx, &run, &shard); err != nil {
		return &run, err
	}
	if shard.Status == asset.ShardSucceeded || shard.Status == asset.ShardSkipped || shard.Status == asset.ShardFailed {
		if shard.Status == asset.ShardFailed {
			return &run, fmt.Errorf("scan shard %q already failed: %s", shard.ID, shard.Coverage.FailureReason)
		}
		return &run, nil
	}
	connection, err := h.repositories.Connections().GetConnection(ctx, run.ConnectionID)
	if err != nil {
		return &run, err
	}
	if connection.Status != asset.ConnectionActive {
		return &run, fmt.Errorf("%w: connection %q status is %q", asset.ErrConnectionNotValidated, connection.ID, connection.Status)
	}
	scope, err := h.repositories.Inventory().GetScope(ctx, shard.ScopeID)
	if err != nil {
		return &run, err
	}
	var kind *asset.ResourceKind
	nativeType := "all"
	if shard.ResourceKindID != "" {
		resolved, err := h.repositories.Inventory().GetResourceKind(ctx, shard.ResourceKindID)
		if err != nil {
			return &run, err
		}
		kind = &resolved
		nativeType = resolved.NativeType
	}
	adapter, err := h.runtimes.ResolveInventory(shard.Provider)
	if err != nil {
		return &run, err
	}
	now := h.clock()
	if shard.StartedAt == nil {
		shard.StartedAt = &now
	}
	shard.Status = asset.ShardRunning
	if run.StartedAt == nil {
		run.StartedAt = &now
		run.Status = asset.ScanRunning
		if err := h.repositories.Inventory().PutScanRun(ctx, run); err != nil {
			return &run, err
		}
	}
	if err := h.repositories.Inventory().PutScanShard(ctx, shard); err != nil {
		return &run, err
	}
	var networkTarget *asset.ScanTarget
	if run.ScopeMode == asset.ScanSelectedNetworks {
		networkTarget, err = networkTargetForTask(run, shard.TargetKey)
		if err != nil {
			return &run, err
		}
	}
	cursor := ""
	collected := make([]contracts.InventoryItem, 0)
	for {
		if err := h.checkTaskControl(ctx, &run, &shard); err != nil {
			return &run, err
		}
		currentConnection, err := h.repositories.Connections().GetConnection(ctx, connection.ID)
		if err != nil {
			return &run, err
		}
		if currentConnection.Status != asset.ConnectionActive {
			return &run, fmt.Errorf("%w: connection %q status is %q", asset.ErrConnectionNotValidated, currentConnection.ID, currentConnection.Status)
		}
		request := contracts.InventoryRequest{
			ConnectionID: connection.ID, Scope: scope, Source: shard.Source, ResourceKind: kind,
			Cursor: cursor, Limit: MaxBatchSize, NetworkTarget: networkTarget,
		}
		batch, err := adapter.List(ctx, request)
		if err != nil {
			if reason, ok := unsupportedSkipReason(err); ok {
				shard.Coverage.SkipReason = reason
				if finishErr := h.service.FinishShard(ctx, &shard, asset.ShardSkipped, ""); finishErr != nil {
					return &run, errors.Join(err, finishErr)
				}
				return &run, nil
			}
			finishErr := h.service.FinishShard(ctx, &shard, asset.ShardFailed, err.Error())
			if finishErr == nil {
				finishErr = h.finishRunIfTerminal(ctx, &run)
			}
			return &run, errors.Join(err, finishErr)
		}
		currentConnection, err = h.repositories.Connections().GetConnection(ctx, connection.ID)
		if err != nil {
			return &run, err
		}
		if currentConnection.Status != asset.ConnectionActive {
			return &run, fmt.Errorf("%w: connection %q status is %q", asset.ErrConnectionNotValidated, currentConnection.ID, currentConnection.Status)
		}
		baseBatch := batch
		batch, err = enrichInventoryBatch(ctx, adapter, request, batch)
		if err != nil {
			fallbackErr := h.projectFallbackInventoryBatch(
				ctx, &shard, connection, nativeType, networkTarget, collected, baseBatch,
			)
			if fallbackErr == nil {
				execution.LogJob(ctx, "warn", fmt.Sprintf(
					"preserved %s from %s without product detail after enrichment failed: %v",
					countNoun(len(baseBatch.Items), "resource"), shard.Source, err,
				))
			} else {
				err = errors.Join(err, fallbackErr)
			}
			if reason, ok := unsupportedSkipReason(err); ok {
				shard.Coverage.SkipReason = reason
				if finishErr := h.service.FinishShard(ctx, &shard, asset.ShardSkipped, ""); finishErr != nil {
					return &run, errors.Join(err, finishErr)
				}
				return &run, nil
			}
			finishErr := h.service.FinishShard(ctx, &shard, asset.ShardFailed, err.Error())
			if finishErr == nil {
				finishErr = h.finishRunIfTerminal(ctx, &run)
			}
			return &run, errors.Join(err, finishErr)
		}
		if networkTarget != nil {
			collected = append(collected, batch.Items...)
		} else {
			if err := h.service.ProjectBatch(ctx, &shard, connection, batch, ProjectionOptions{ObservedAt: h.clock()}); err != nil {
				finishErr := h.service.FinishShard(ctx, &shard, asset.ShardFailed, err.Error())
				if finishErr == nil {
					finishErr = h.finishRunIfTerminal(ctx, &run)
				}
				return &run, errors.Join(err, finishErr)
			}
			logProjectedInventoryBatch(ctx, shard, nativeType, batch)
		}
		if err := h.checkTaskControl(ctx, &run, &shard); err != nil {
			return &run, err
		}
		if batch.Complete || batch.NextCursor == "" {
			break
		}
		cursor = batch.NextCursor
	}
	if networkTarget != nil {
		filtered := FilterNetworkClosure(*networkTarget, collected)
		for start := 0; start < len(filtered); start += MaxBatchSize {
			end := start + MaxBatchSize
			if end > len(filtered) {
				end = len(filtered)
			}
			batch := contracts.InventoryBatch{Items: filtered[start:end], Complete: end == len(filtered)}
			if err := h.service.ProjectBatch(ctx, &shard, connection, batch, ProjectionOptions{ObservedAt: h.clock()}); err != nil {
				finishErr := h.service.FinishShard(ctx, &shard, asset.ShardFailed, err.Error())
				if finishErr == nil {
					finishErr = h.finishRunIfTerminal(ctx, &run)
				}
				return &run, errors.Join(err, finishErr)
			}
			logProjectedInventoryBatch(ctx, shard, nativeType, batch)
			if err := h.checkTaskControl(ctx, &run, &shard); err != nil {
				return &run, err
			}
		}
	}
	if err := h.service.FinishShard(ctx, &shard, asset.ShardSucceeded, ""); err != nil {
		return &run, err
	}
	if err := h.checkTaskControl(ctx, &run, nil); err != nil {
		return &run, err
	}
	return &run, nil
}

func (h *ScanHandler) projectFallbackInventoryBatch(
	ctx context.Context,
	shard *asset.ScanShard,
	connection asset.CloudConnection,
	nativeType string,
	networkTarget *asset.ScanTarget,
	collected []contracts.InventoryItem,
	batch contracts.InventoryBatch,
) error {
	if networkTarget == nil {
		if err := h.service.ProjectBatch(ctx, shard, connection, batch, ProjectionOptions{ObservedAt: h.clock()}); err != nil {
			return err
		}
		logProjectedInventoryBatch(ctx, *shard, nativeType, batch)
		return nil
	}
	candidates := make([]contracts.InventoryItem, 0, len(collected)+len(batch.Items))
	candidates = append(candidates, collected...)
	candidates = append(candidates, batch.Items...)
	filtered := FilterNetworkClosure(*networkTarget, candidates)
	for start := 0; start < len(filtered); start += MaxBatchSize {
		end := start + MaxBatchSize
		if end > len(filtered) {
			end = len(filtered)
		}
		fallback := contracts.InventoryBatch{
			Items: filtered[start:end], Complete: end == len(filtered), RequestID: batch.RequestID,
		}
		if err := h.service.ProjectBatch(ctx, shard, connection, fallback, ProjectionOptions{ObservedAt: h.clock()}); err != nil {
			return err
		}
		logProjectedInventoryBatch(ctx, *shard, nativeType, fallback)
	}
	return nil
}

func enrichInventoryBatch(
	ctx context.Context,
	adapter contracts.InventoryAdapter,
	request contracts.InventoryRequest,
	batch contracts.InventoryBatch,
) (contracts.InventoryBatch, error) {
	enricher, ok := adapter.(contracts.InventoryBatchEnricher)
	if !ok || len(batch.Items) == 0 {
		return batch, nil
	}
	items, err := enricher.EnrichInventoryBatch(ctx, request, batch.Items)
	if err != nil {
		return contracts.InventoryBatch{}, err
	}
	batch.Items = items
	return batch, nil
}

func logProjectedInventoryBatch(ctx context.Context, shard asset.ScanShard, nativeType string, batch contracts.InventoryBatch) {
	message := fmt.Sprintf(
		"projected %s from %s for %s; %d accumulated",
		countNoun(len(batch.Items), "resource"),
		shard.Source,
		nativeType,
		shard.Coverage.ItemCount,
	)
	if batch.RequestID != "" {
		message += "; request " + batch.RequestID
	}
	execution.LogJob(ctx, "info", message)
}

func countNoun(count int, noun string) string {
	if count == 1 {
		return fmt.Sprintf("%d %s", count, noun)
	}
	return fmt.Sprintf("%d %ss", count, noun)
}

func scanShardIDs(payload map[string]any) []asset.ScanShardID {
	result := make([]asset.ScanShardID, 0)
	appendID := func(value string) {
		value = strings.TrimSpace(value)
		if value != "" {
			result = append(result, asset.ScanShardID(value))
		}
	}
	switch values := payload["scan_shard_ids"].(type) {
	case []string:
		for _, value := range values {
			appendID(value)
		}
	case []any:
		for _, value := range values {
			text, _ := value.(string)
			appendID(text)
		}
	}
	if len(result) == 0 {
		value, _ := payload["scan_shard_id"].(string)
		appendID(value)
	}
	return result
}

func (h *ScanHandler) blockShards(ctx context.Context, shardIDs []asset.ScanShardID, reason string) error {
	now := h.clock()
	for _, shardID := range shardIDs {
		shard, err := h.repositories.Inventory().GetScanShard(ctx, shardID)
		if err != nil {
			return err
		}
		if shard.Status != asset.ShardPending && shard.Status != asset.ShardPaused {
			continue
		}
		shard.Status = asset.ShardBlocked
		shard.FinishedAt = &now
		shard.Coverage.FailureReason = strings.TrimSpace(reason)
		if err := h.repositories.Inventory().PutScanShard(ctx, shard); err != nil {
			return err
		}
	}
	return nil
}

func (h *ScanHandler) cancelShards(ctx context.Context, shardIDs []asset.ScanShardID) error {
	now := h.clock()
	for _, shardID := range shardIDs {
		shard, err := h.repositories.Inventory().GetScanShard(ctx, shardID)
		if err != nil {
			return err
		}
		if shard.Status != asset.ShardPending && shard.Status != asset.ShardPaused && shard.Status != asset.ShardBlocked {
			continue
		}
		shard.Status = asset.ShardCanceled
		shard.FinishedAt = &now
		if err := h.repositories.Inventory().PutScanShard(ctx, shard); err != nil {
			return err
		}
	}
	return nil
}

func (h *ScanHandler) checkTaskControl(ctx context.Context, task *asset.ScanRun, shard *asset.ScanShard) error {
	current, err := h.repositories.Inventory().GetScanRun(ctx, task.ID)
	if err != nil {
		return err
	}
	*task = current
	now := h.clock()
	switch current.Status {
	case asset.ScanPausing, asset.ScanPaused:
		if shard != nil && shard.Status != asset.ShardSucceeded && shard.Status != asset.ShardSkipped && shard.Status != asset.ShardFailed && shard.Status != asset.ShardBlocked {
			shard.Status = asset.ShardPaused
			shard.FinishedAt = nil
			if err := h.repositories.Inventory().PutScanShard(ctx, *shard); err != nil {
				return err
			}
		}
		return &execution.JobStatusError{Status: execution.JobPaused, Cause: fmt.Errorf("scan task %s is paused", task.ID)}
	case asset.ScanCanceling, asset.ScanCanceled:
		if shard != nil && shard.Status != asset.ShardSucceeded && shard.Status != asset.ShardSkipped && shard.Status != asset.ShardFailed && shard.Status != asset.ShardBlocked {
			shard.Status = asset.ShardCanceled
			shard.FinishedAt = &now
			if err := h.repositories.Inventory().PutScanShard(ctx, *shard); err != nil {
				return err
			}
		}
		return &execution.JobStatusError{Status: execution.JobCanceled, Cause: fmt.Errorf("scan task %s is canceled", task.ID)}
	default:
		return nil
	}
}

func (h *ScanHandler) settleControlledTask(ctx context.Context, task *asset.ScanRun) error {
	current, err := h.repositories.Inventory().GetScanRun(ctx, task.ID)
	if err != nil {
		return err
	}
	if current.Status != asset.ScanPausing && current.Status != asset.ScanPaused && current.Status != asset.ScanCanceling && current.Status != asset.ScanCanceled {
		*task = current
		return nil
	}
	shards, err := h.repositories.Inventory().ListScanShardsByRun(ctx, current.ID)
	if err != nil {
		return err
	}
	for _, shard := range shards {
		if shard.Status == asset.ShardRunning {
			*task = current
			return nil
		}
	}
	now := h.clock()
	expected := current.ControlVersion
	if current.Status == asset.ScanPausing || current.Status == asset.ScanPaused {
		current.Status = asset.ScanPaused
		current.PausedAt = &now
	} else {
		current.Status = asset.ScanCanceled
		current.CanceledAt = &now
		current.FinishedAt = &now
	}
	current.ControlVersion++
	if err := h.repositories.Inventory().PutScanRunIfControlVersion(ctx, current, expected); err != nil {
		return err
	}
	*task = current
	return nil
}

func (h *ScanHandler) finishRunIfTerminal(ctx context.Context, run *asset.ScanRun) error {
	runShards, err := h.repositories.Inventory().ListScanShardsByRun(ctx, run.ID)
	if err != nil {
		return err
	}
	for _, candidate := range runShards {
		if candidate.Status != asset.ShardSucceeded && candidate.Status != asset.ShardSkipped && candidate.Status != asset.ShardFailed && candidate.Status != asset.ShardBlocked && candidate.Status != asset.ShardCanceled {
			return nil
		}
	}
	if run.Status != asset.ScanReconciling && run.Status != asset.ScanSucceeded && run.Status != asset.ScanPartial && run.Status != asset.ScanFailed && run.Status != asset.ScanCanceled {
		if err := h.service.FinishScan(ctx, run, runShards); err != nil {
			return err
		}
	}
	if run.Status == asset.ScanFailed || run.Status == asset.ScanCanceled {
		return nil
	}
	now := h.clock()
	err = h.repositories.Jobs().Enqueue(ctx, execution.Job{
		ID: execution.JobID(idgen.MustNew("job")), ConnectionID: run.ConnectionID,
		IdempotencyKey: scanGraphJobIdempotencyKey(run.ID, run.RetryGeneration),
		AggregateType:  "scan_task", AggregateID: string(run.ID), RetryGeneration: run.RetryGeneration,
		Type: execution.JobGraph, Status: execution.JobPending,
		Payload: map[string]any{"scan_run_id": string(run.ID)}, RunAt: now, CreatedAt: now, UpdatedAt: now,
	})
	if errors.Is(err, persistence.ErrConflict) {
		return nil
	}
	return err
}

func unsupportedSkipReason(err error) (asset.SkipReason, bool) {
	var providerCall *contracts.ProviderCallError
	if !errors.As(err, &providerCall) || providerCall.Provider.Category != execution.ErrorUnsupported {
		return "", false
	}
	reason, _ := providerCall.Provider.Summary["skip_reason"].(string)
	switch asset.SkipReason(reason) {
	case asset.SkipProductUnsupported, asset.SkipProviderRegionUnavailable:
		return asset.SkipReason(reason), true
	default:
		return asset.SkipProductUnsupported, true
	}
}
