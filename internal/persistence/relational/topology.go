package relational

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

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

// Unresolved diagnostics share the graph revision's atomic replacement. Closed
// controllers and superseded scopes cannot revive old cleanup constraints.
func (s *Store) ListUnresolvedByConnection(ctx context.Context, connectionID asset.ConnectionID) ([]graph.UnresolvedReference, error) {
	var rows []graphRevisionRow
	scopes := s.db.WithContext(ctx).Table("scopes").Select("id").Where("connection_id = ? AND (superseded_by_scope_id IS NULL OR superseded_by_scope_id = '')", string(connectionID))
	if err := s.db.WithContext(ctx).Table("graph_revisions").Where("scope_id IN (?)", scopes).Order("scope_id ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	decoded := make([][]graph.UnresolvedReference, len(rows))
	var controllerIDs []string
	for index, row := range rows {
		if err := json.Unmarshal([]byte(row.UnresolvedPayload), &decoded[index]); err != nil {
			return nil, fmt.Errorf("decode unresolved graph references: %w", err)
		}
		for _, reference := range decoded[index] {
			if reference.ConnectionID == connectionID {
				controllerIDs = append(controllerIDs, string(reference.ControllerID))
			}
		}
	}
	slices.Sort(controllerIDs)
	controllerIDs = slices.Compact(controllerIDs)
	active := make(map[asset.AssetID]bool, len(controllerIDs))
	for start := 0; start < len(controllerIDs); start += topologyGraphAssetBatchSize {
		var ids []string
		batch := controllerIDs[start:min(start+topologyGraphAssetBatchSize, len(controllerIDs))]
		if err := s.db.WithContext(ctx).Table("assets").Where("id IN ? AND connection_id = ? AND closed_at IS NULL", batch, string(connectionID)).Pluck("id", &ids).Error; err != nil {
			return nil, err
		}
		for _, id := range ids {
			active[asset.AssetID(id)] = true
		}
	}
	result := []graph.UnresolvedReference{}
	for index, row := range rows {
		for _, reference := range decoded[index] {
			if reference.ConnectionID == connectionID && active[reference.ControllerID] {
				reference.GraphRevision = row.GraphRevision
				result = append(result, reference)
			}
		}
	}
	return result, nil
}

// revisionTotals is a table's row count and revision sum. Where every update
// bumps a row's revision, the pair changes whenever the rows do.
type revisionTotals struct {
	Rows      int64 `gorm:"column:row_count"`
	Revisions int64 `gorm:"column:revision_sum"`
}

const selectRevisionTotals = "COUNT(*) AS row_count, CAST(COALESCE(SUM(revision), 0) AS BIGINT) AS revision_sum"

// ConnectionInventoryVersion changes whenever the connection's assets,
// findings, graph revisions or open graph rows do: asset and finding writes
// bump a row revision and neither table deletes rows, so count plus revision
// sum never repeats.
func (s *Store) ConnectionInventoryVersion(ctx context.Context, connectionID asset.ConnectionID) (string, error) {
	db := s.db.WithContext(ctx)
	var assets, findings revisionTotals
	if err := db.Table("assets").Select(selectRevisionTotals).Where("connection_id = ?", string(connectionID)).Scan(&assets).Error; err != nil {
		return "", err
	}
	connectionAssets := db.Table("assets").Select("id").Where("connection_id = ?", string(connectionID))
	if err := db.Table("findings").Select(selectRevisionTotals).Where("asset_id IN (?)", connectionAssets).Scan(&findings).Error; err != nil {
		return "", err
	}
	scopes := db.Table("scopes").Select("id").Where("connection_id = ?", string(connectionID))
	var revisions []graphRevisionRow
	if err := db.Table("graph_revisions").Select("scope_id, graph_revision, observed_at").Where("scope_id IN (?)", scopes).Order("scope_id ASC").Find(&revisions).Error; err != nil {
		return "", err
	}
	var relationships, bindings int64
	if err := db.Table("relationships").Where("scope_id IN (?) AND closed_at IS NULL", scopes).Count(&relationships).Error; err != nil {
		return "", err
	}
	if err := db.Table("lifecycle_bindings").Where("scope_id IN (?) AND closed_at IS NULL", scopes).Count(&bindings).Error; err != nil {
		return "", err
	}
	var version strings.Builder
	fmt.Fprintf(&version, "a%d.%d:f%d.%d:r%d:b%d", assets.Rows, assets.Revisions, findings.Rows, findings.Revisions, relationships, bindings)
	for _, row := range revisions {
		fmt.Fprintf(&version, ":%s=%s@%d", row.ScopeID, row.GraphRevision, row.ObservedAt.UnixNano())
	}
	return version.String(), nil
}
