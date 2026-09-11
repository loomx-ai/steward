package inventory

import (
	"context"
	"fmt"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/persistence"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func confirmedAbsentAssets(batch contracts.InventoryBatch, reconcileKnown bool, known map[string][]asset.Asset, observed map[string]bool) ([]asset.Asset, error) {
	if len(batch.AbsentNativeIDs) == 0 {
		return nil, nil
	}
	if !reconcileKnown || !batch.Complete || batch.NextCursor != "" {
		return nil, fmt.Errorf("native absence requires a complete known-resource reconciliation batch")
	}
	result := make([]asset.Asset, 0, len(batch.AbsentNativeIDs))
	seen := map[string]bool{}
	for _, id := range batch.AbsentNativeIDs {
		if id == "" || seen[id] || observed[id] || len(known[id]) != 1 {
			return nil, fmt.Errorf("native absence has an unknown, ambiguous or observed identity")
		}
		seen[id] = true
		result = append(result, known[id][0])
	}
	return result, nil
}

// Called within FinishShard's transaction. A later scan may have refreshed or
// recreated an asset while this worker was reading: that newer observation must
// survive even when the worker's original native read found nothing.
func closeConfirmedAbsentAssets(ctx context.Context, repository persistence.InventoryRepository, shard asset.ScanShard, known []asset.Asset, finishedAt time.Time) error {
	if len(known) == 0 {
		return nil
	}
	run, err := repository.GetScanRun(ctx, shard.ScanRunID)
	if err != nil {
		return err
	}
	kind, err := repository.GetResourceKind(ctx, shard.ResourceKindID)
	if err != nil {
		return err
	}
	seen := map[asset.AssetID]bool{}
	for _, baseline := range known {
		if baseline.ID == "" || seen[baseline.ID] || baseline.Identity.Provider != shard.Provider || baseline.Identity.ConnectionID != run.ConnectionID || baseline.ResourceKindID != shard.ResourceKindID || baseline.Identity.NativeType != kind.NativeType {
			return fmt.Errorf("confirmed native absence belongs to another scan identity")
		}
		seen[baseline.ID] = true
		current, err := repository.GetAsset(ctx, baseline.ID)
		if err != nil {
			return err
		}
		if current.Identity != baseline.Identity || current.ResourceKindID != baseline.ResourceKindID || current.ScopeID != baseline.ScopeID || current.ClosedAt != nil || current.DeletedAt != nil || current.CurrentObservationID != baseline.CurrentObservationID || !current.LastSeenAt.Equal(baseline.LastSeenAt) {
			continue
		}
		covered, err := scopeWithinCoverage(ctx, repository, current.ScopeID, shard.ScopeID, run.ConnectionID)
		if err != nil {
			return err
		}
		if !covered {
			continue
		}
		current.ClosedAt = &finishedAt
		if err := repository.PutAsset(ctx, current); err != nil {
			return err
		}
	}
	return nil
}
