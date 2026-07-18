package relational

import (
	"context"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
)

const topologyGraphAssetBatchSize = 400

func (s *Store) ListRelationshipsByScope(ctx context.Context, scopeID asset.ScopeID) ([]graph.Relationship, error) {
	var rows []relationshipRow
	if err := s.db.WithContext(ctx).Table("relationships").Where("scope_id = ? AND closed_at IS NULL", string(scopeID)).Order("id ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	return decodeRows[relationshipRow, graph.Relationship](rows, func(row relationshipRow) string { return row.Payload })
}

func (s *Store) ListLifecycleBindingsByScope(ctx context.Context, scopeID asset.ScopeID) ([]graph.LifecycleBinding, error) {
	var rows []lifecycleBindingRow
	if err := s.db.WithContext(ctx).Table("lifecycle_bindings").Where("scope_id = ? AND closed_at IS NULL", string(scopeID)).Order("id ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	return decodeRows[lifecycleBindingRow, graph.LifecycleBinding](rows, func(row lifecycleBindingRow) string { return row.Payload })
}

func (s *Store) ListRelationshipsByAssetIDs(ctx context.Context, assetIDs []asset.AssetID) ([]graph.Relationship, error) {
	if len(assetIDs) == 0 {
		return []graph.Relationship{}, nil
	}
	result := make([]graph.Relationship, 0)
	seen := make(map[string]struct{})
	for start := 0; start < len(assetIDs); start += topologyGraphAssetBatchSize {
		end := min(start+topologyGraphAssetBatchSize, len(assetIDs))
		ids := make([]string, end-start)
		for index, id := range assetIDs[start:end] {
			ids[index] = string(id)
		}
		var rows []relationshipRow
		if err := s.db.WithContext(ctx).
			Table("relationships").
			Where(
				"closed_at IS NULL AND (source_asset_id IN ? OR target_asset_id IN ?)",
				ids,
				ids,
			).
			Order("id ASC").
			Find(&rows).Error; err != nil {
			return nil, err
		}
		for _, row := range rows {
			if _, ok := seen[row.ID]; ok {
				continue
			}
			value, err := decode[graph.Relationship](row.Payload)
			if err != nil {
				return nil, err
			}
			seen[row.ID] = struct{}{}
			result = append(result, value)
		}
	}
	return result, nil
}

func (s *Store) ListLifecycleBindingsByAssetIDs(ctx context.Context, assetIDs []asset.AssetID) ([]graph.LifecycleBinding, error) {
	if len(assetIDs) == 0 {
		return []graph.LifecycleBinding{}, nil
	}
	result := make([]graph.LifecycleBinding, 0)
	seen := make(map[string]struct{})
	for start := 0; start < len(assetIDs); start += topologyGraphAssetBatchSize {
		end := min(start+topologyGraphAssetBatchSize, len(assetIDs))
		ids := make([]string, end-start)
		for index, id := range assetIDs[start:end] {
			ids[index] = string(id)
		}
		var rows []lifecycleBindingRow
		if err := s.db.WithContext(ctx).
			Table("lifecycle_bindings").
			Where(
				"closed_at IS NULL AND (controller_asset_id IN ? OR managed_asset_id IN ?)",
				ids,
				ids,
			).
			Order("id ASC").
			Find(&rows).Error; err != nil {
			return nil, err
		}
		for _, row := range rows {
			if _, ok := seen[row.ID]; ok {
				continue
			}
			value, err := decode[graph.LifecycleBinding](row.Payload)
			if err != nil {
				return nil, err
			}
			seen[row.ID] = struct{}{}
			result = append(result, value)
		}
	}
	return result, nil
}

func (s *Store) ListRelationshipsByConnection(ctx context.Context, connectionID asset.ConnectionID) ([]graph.Relationship, error) {
	var rows []relationshipRow
	scopes := s.db.WithContext(ctx).Table("scopes").Select("id").Where("connection_id = ? AND (superseded_by_scope_id IS NULL OR superseded_by_scope_id = '')", string(connectionID))
	if err := s.db.WithContext(ctx).Table("relationships").Where("scope_id IN (?) AND closed_at IS NULL", scopes).Order("id ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	return decodeRows[relationshipRow, graph.Relationship](rows, func(row relationshipRow) string { return row.Payload })
}

func (s *Store) ListLifecycleBindingsByConnection(ctx context.Context, connectionID asset.ConnectionID) ([]graph.LifecycleBinding, error) {
	var rows []lifecycleBindingRow
	scopes := s.db.WithContext(ctx).Table("scopes").Select("id").Where("connection_id = ? AND (superseded_by_scope_id IS NULL OR superseded_by_scope_id = '')", string(connectionID))
	if err := s.db.WithContext(ctx).Table("lifecycle_bindings").Where("scope_id IN (?) AND closed_at IS NULL", scopes).Order("id ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	return decodeRows[lifecycleBindingRow, graph.LifecycleBinding](rows, func(row lifecycleBindingRow) string { return row.Payload })
}

func (s *Store) ListGraphRevisionsByConnection(ctx context.Context, connectionID asset.ConnectionID) (map[asset.ScopeID]string, error) {
	var rows []graphRevisionRow
	scopes := s.db.WithContext(ctx).Table("scopes").Select("id").Where("connection_id = ? AND (superseded_by_scope_id IS NULL OR superseded_by_scope_id = '')", string(connectionID))
	if err := s.db.WithContext(ctx).Table("graph_revisions").Where("scope_id IN (?)", scopes).Order("scope_id ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	result := make(map[asset.ScopeID]string, len(rows))
	for _, row := range rows {
		result[asset.ScopeID(row.ScopeID)] = row.GraphRevision
	}
	return result, nil
}
