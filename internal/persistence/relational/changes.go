package relational

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/persistence"
)

type assetChangeRow struct {
	ID             string    `gorm:"column:id;primaryKey"`
	ConnectionID   string    `gorm:"column:connection_id"`
	ScanTaskID     string    `gorm:"column:scan_task_id"`
	AssetID        string    `gorm:"column:asset_id"`
	ChangeType     string    `gorm:"column:change_type"`
	ResourceKindID string    `gorm:"column:resource_kind_id"`
	ChangedAt      time.Time `gorm:"column:changed_at"`
	Payload        string    `gorm:"column:payload"`
}

func (s *Store) GetObservation(ctx context.Context, id asset.ObservationID) (asset.Observation, error) {
	var row observationRow
	if err := s.db.WithContext(ctx).Table("asset_observations").Where("id = ?", string(id)).Take(&row).Error; err != nil {
		return asset.Observation{}, mapError(err)
	}
	return row.observation(), nil
}

func (s *Store) ListObservationsByIDs(ctx context.Context, ids []asset.ObservationID) ([]asset.Observation, error) {
	values := make([]string, 0, len(ids))
	seen := make(map[asset.ObservationID]struct{}, len(ids))
	for _, id := range ids {
		if _, ok := seen[id]; ok || id == "" {
			continue
		}
		seen[id] = struct{}{}
		values = append(values, string(id))
	}
	const batchSize = 400
	result := make([]asset.Observation, 0, len(values))
	for start := 0; start < len(values); start += batchSize {
		var rows []observationRow
		if err := s.db.WithContext(ctx).Table("asset_observations").Where("id IN ?", values[start:min(start+batchSize, len(values))]).Order("id ASC").Find(&rows).Error; err != nil {
			return nil, err
		}
		for _, row := range rows {
			result = append(result, row.observation())
		}
	}
	return result, nil
}

func (s *Store) RecordAssetChanges(ctx context.Context, changes []asset.AssetChange) error {
	type changeKey struct{ scanTaskID, assetID string }
	pending := make(map[changeKey][]asset.AssetChange, len(changes))
	keys := make([]changeKey, 0, len(changes))
	assetIDsByScan := make(map[string][]string)
	for _, change := range changes {
		if change.ID == "" || change.ScanTaskID == "" || change.AssetID == "" || !change.Type.Valid() {
			return errors.New("asset change requires an id, scan, asset and change type")
		}
		key := changeKey{string(change.ScanTaskID), string(change.AssetID)}
		if _, ok := pending[key]; !ok {
			keys = append(keys, key)
			assetIDsByScan[key.scanTaskID] = append(assetIDsByScan[key.scanTaskID], key.assetID)
		}
		pending[key] = append(pending[key], change)
	}
	if len(keys) == 0 {
		return nil
	}
	db := s.db.WithContext(ctx)
	existing := make(map[changeKey]assetChangeRow, len(keys))
	const batchSize = 400
	for scanTaskID, assetIDs := range assetIDsByScan {
		for start := 0; start < len(assetIDs); start += batchSize {
			var rows []assetChangeRow
			if err := db.Table("asset_changes").
				Where("scan_task_id = ? AND asset_id IN ?", scanTaskID, assetIDs[start:min(start+batchSize, len(assetIDs))]).
				Find(&rows).Error; err != nil {
				return err
			}
			for _, row := range rows {
				existing[changeKey{row.ScanTaskID, row.AssetID}] = row
			}
		}
	}
	// Fold each asset's changes as one write per change would have: a merge
	// that cancels out removes the recorded change, and a later change then
	// starts a new one.
	var deletes []string
	var upserts []assetChangeRow
	for _, key := range keys {
		var current *asset.AssetChange
		row, recorded := existing[key]
		if recorded {
			previous, err := decode[asset.AssetChange](row.Payload)
			if err != nil {
				return err
			}
			current = &previous
		}
		for _, change := range pending[key] {
			if current == nil {
				current = &change
				continue
			}
			merged, keep := asset.MergeAssetChange(*current, change)
			if !keep {
				current = nil
				continue
			}
			current = &merged
		}
		if recorded && (current == nil || current.ID != row.ID) {
			deletes = append(deletes, row.ID)
		}
		if current == nil {
			continue
		}
		payload, err := encode(*current)
		if err != nil {
			return err
		}
		upserts = append(upserts, assetChangeRow{
			ID: current.ID, ConnectionID: string(current.ConnectionID), ScanTaskID: string(current.ScanTaskID), AssetID: string(current.AssetID),
			ChangeType: string(current.Type), ResourceKindID: string(current.ResourceKindID), ChangedAt: current.ChangedAt, Payload: payload,
		})
	}
	for start := 0; start < len(deletes); start += batchSize {
		if err := db.Table("asset_changes").Where("id IN ?", deletes[start:min(start+batchSize, len(deletes))]).Delete(nil).Error; err != nil {
			return err
		}
	}
	return upsertInBatches(db, "asset_changes", upserts, []string{"change_type", "resource_kind_id", "changed_at", "payload"})
}

func (s *Store) ListAssetChanges(ctx context.Context, options persistence.AssetChangeListOptions) (persistence.Page[asset.AssetChange], error) {
	limit := normalizeLimit(options.Limit)
	query := s.db.WithContext(ctx).Table("asset_changes").Where("scan_task_id = ?", string(options.ScanTaskID))
	if options.Type != "" {
		query = query.Where("change_type = ?", string(options.Type))
	}
	if term := strings.ToLower(strings.TrimSpace(options.Query)); term != "" {
		query = query.Where("LOWER(payload) LIKE ? ESCAPE '\\'", "%"+escapeLike(term)+"%")
	}
	if options.Cursor != "" {
		changedAt, id, err := decodeCursor(options.Cursor)
		if err != nil {
			return persistence.Page[asset.AssetChange]{}, err
		}
		query = query.Where("changed_at < ? OR (changed_at = ? AND id < ?)", changedAt, changedAt, id)
	}
	var rows []assetChangeRow
	if err := query.Order("changed_at DESC, id DESC").Limit(limit + 1).Find(&rows).Error; err != nil {
		return persistence.Page[asset.AssetChange]{}, err
	}
	return decodePage[assetChangeRow, asset.AssetChange](rows, limit, func(row assetChangeRow) time.Time { return row.ChangedAt }, func(row assetChangeRow) string { return row.ID }, func(row assetChangeRow) string { return row.Payload })
}

func (s *Store) CountAssetChanges(ctx context.Context, scanTaskIDs []asset.ScanTaskID) (map[asset.ScanTaskID]asset.ChangeCounts, error) {
	result := make(map[asset.ScanTaskID]asset.ChangeCounts, len(scanTaskIDs))
	if len(scanTaskIDs) == 0 {
		return result, nil
	}
	ids := make([]string, len(scanTaskIDs))
	for index, id := range scanTaskIDs {
		ids[index] = string(id)
	}
	var rows []struct {
		ScanTaskID string `gorm:"column:scan_task_id"`
		ChangeType string `gorm:"column:change_type"`
		Count      int    `gorm:"column:change_count"`
	}
	if err := s.db.WithContext(ctx).Table("asset_changes").
		Select("scan_task_id, change_type, COUNT(*) AS change_count").
		Where("scan_task_id IN ?", ids).
		Group("scan_task_id, change_type").
		Find(&rows).Error; err != nil {
		return nil, err
	}
	for _, row := range rows {
		counts := result[asset.ScanTaskID(row.ScanTaskID)]
		counts.Add(asset.ChangeType(row.ChangeType), row.Count)
		result[asset.ScanTaskID(row.ScanTaskID)] = counts
	}
	return result, nil
}
