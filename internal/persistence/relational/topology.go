package relational

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
)

const topologyGraphAssetBatchSize = 400

func (s *Store) ListRelationshipsByScope(ctx context.Context, scopeID asset.ScopeID) ([]graph.Relationship, error) {
	var rows []relationshipRow
	if err := s.db.WithContext(ctx).Table("relationships").Select(relationshipColumns).Where("scope_id = ? AND closed_at IS NULL", string(scopeID)).Order("id ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	return decodeRelationshipRows(rows)
}

func (s *Store) ListLifecycleBindingsByScope(ctx context.Context, scopeID asset.ScopeID) ([]graph.LifecycleBinding, error) {
	var rows []lifecycleBindingRow
	if err := s.db.WithContext(ctx).Table("lifecycle_bindings").Select(bindingColumns).Where("scope_id = ? AND closed_at IS NULL", string(scopeID)).Order("id ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	return decodeBindingRows(rows)
}

func (s *Store) ListRelationshipsByAssetIDs(ctx context.Context, assetIDs []asset.AssetID) ([]graph.Relationship, error) {
	if len(assetIDs) == 0 {
		return []graph.Relationship{}, nil
	}
	result := make([]graph.Relationship, 0)
	seen := make(map[string]struct{})
	decoder := graphItemDecoder{}
	for start := 0; start < len(assetIDs); start += topologyGraphAssetBatchSize {
		end := min(start+topologyGraphAssetBatchSize, len(assetIDs))
		ids := make([]string, end-start)
		for index, id := range assetIDs[start:end] {
			ids[index] = string(id)
		}
		var rows []relationshipRow
		if err := s.db.WithContext(ctx).
			Table("relationships").
			Select(relationshipColumns).
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
			value, err := decoder.relationship(row.Payload, row.ItemDefaults)
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
	decoder := graphItemDecoder{}
	for start := 0; start < len(assetIDs); start += topologyGraphAssetBatchSize {
		end := min(start+topologyGraphAssetBatchSize, len(assetIDs))
		ids := make([]string, end-start)
		for index, id := range assetIDs[start:end] {
			ids[index] = string(id)
		}
		var rows []lifecycleBindingRow
		if err := s.db.WithContext(ctx).
			Table("lifecycle_bindings").
			Select(bindingColumns).
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
			value, err := decoder.binding(row.Payload, row.ItemDefaults)
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
	if err := s.db.WithContext(ctx).Table("relationships").Select(relationshipColumns).Where("scope_id IN (?) AND closed_at IS NULL", scopes).Order("id ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	return decodeRelationshipRows(rows)
}

func (s *Store) ListLifecycleBindingsByConnection(ctx context.Context, connectionID asset.ConnectionID) ([]graph.LifecycleBinding, error) {
	var rows []lifecycleBindingRow
	scopes := s.db.WithContext(ctx).Table("scopes").Select("id").Where("connection_id = ? AND (superseded_by_scope_id IS NULL OR superseded_by_scope_id = '')", string(connectionID))
	if err := s.db.WithContext(ctx).Table("lifecycle_bindings").Select(bindingColumns).Where("scope_id IN (?) AND closed_at IS NULL", scopes).Order("id ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	return decodeBindingRows(rows)
}

func (s *Store) ListGraphRevisionsByConnection(ctx context.Context, connectionID asset.ConnectionID) (map[asset.ScopeID]string, error) {
	var rows []graphRevisionRow
	scopes := s.db.WithContext(ctx).Table("scopes").Select("id").Where("connection_id = ? AND (superseded_by_scope_id IS NULL OR superseded_by_scope_id = '')", string(connectionID))
	if err := s.db.WithContext(ctx).Table("graph_revisions").Select("scope_id, graph_revision").Where("scope_id IN (?)", scopes).Order("scope_id ASC").Find(&rows).Error; err != nil {
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

// ConnectionInventoryVersion reads the counter every transaction that changes
// the connection's assets, findings or graph bumps (see Store.touch).
func (s *Store) ConnectionInventoryVersion(ctx context.Context, connectionID asset.ConnectionID) (string, error) {
	var versions []int64
	if err := s.db.WithContext(ctx).Table("inventory_versions").Where("connection_id = ?", string(connectionID)).Pluck("version", &versions).Error; err != nil {
		return "", err
	}
	if len(versions) == 0 {
		return "v0", nil
	}
	return fmt.Sprintf("v%d", versions[0]), nil
}
