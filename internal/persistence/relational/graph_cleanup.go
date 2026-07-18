package relational

import (
	"context"
	"errors"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/finding"
	"github.com/loomx-ai/steward/internal/core/graph"
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/internal/persistence"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type relationshipRow struct {
	ID            string     `gorm:"column:id;primaryKey"`
	ScopeID       string     `gorm:"column:scope_id"`
	SourceAssetID string     `gorm:"column:source_asset_id"`
	TargetAssetID string     `gorm:"column:target_asset_id"`
	GraphRevision string     `gorm:"column:graph_revision"`
	ObservedAt    time.Time  `gorm:"column:observed_at"`
	ClosedAt      *time.Time `gorm:"column:closed_at"`
	Payload       string     `gorm:"column:payload"`
}

type lifecycleBindingRow struct {
	ID                string     `gorm:"column:id;primaryKey"`
	ScopeID           string     `gorm:"column:scope_id"`
	ControllerAssetID string     `gorm:"column:controller_asset_id"`
	ManagedAssetID    string     `gorm:"column:managed_asset_id"`
	GraphRevision     string     `gorm:"column:graph_revision"`
	ObservedAt        time.Time  `gorm:"column:observed_at"`
	ClosedAt          *time.Time `gorm:"column:closed_at"`
	Payload           string     `gorm:"column:payload"`
}

type assetRelationshipRow struct {
	ParentAssetID       string  `gorm:"column:parent_asset_id"`
	RelationshipID      *string `gorm:"column:relationship_id"`
	RelationshipPayload *string `gorm:"column:relationship_payload"`
}

type assetLifecycleBindingRow struct {
	ParentAssetID  string  `gorm:"column:parent_asset_id"`
	BindingID      *string `gorm:"column:binding_id"`
	BindingPayload *string `gorm:"column:binding_payload"`
}

type graphRevisionRow struct {
	ScopeID       string    `gorm:"column:scope_id;primaryKey"`
	GraphRevision string    `gorm:"column:graph_revision"`
	ObservedAt    time.Time `gorm:"column:observed_at"`
}

type findingRow struct {
	ID         string     `gorm:"column:id;primaryKey"`
	AssetID    string     `gorm:"column:asset_id"`
	RuleID     string     `gorm:"column:rule_id"`
	Status     string     `gorm:"column:status"`
	Severity   string     `gorm:"column:severity"`
	LastSeenAt time.Time  `gorm:"column:last_seen_at"`
	ClosedAt   *time.Time `gorm:"column:closed_at"`
	Payload    string     `gorm:"column:payload"`
}

type assetFindingRow struct {
	ParentAssetID  string  `gorm:"column:parent_asset_id"`
	FindingID      *string `gorm:"column:finding_id"`
	FindingPayload *string `gorm:"column:finding_payload"`
}

type cleanupTaskRecord struct {
	ID           string    `gorm:"column:id;primaryKey"`
	ConnectionID string    `gorm:"column:connection_id"`
	Status       string    `gorm:"column:status"`
	CreatedBy    string    `gorm:"column:created_by"`
	CreatedAt    time.Time `gorm:"column:created_at"`
	Payload      string    `gorm:"column:payload"`
}

type cleanupTaskRow struct {
	ID            string `gorm:"column:id;primaryKey"`
	CleanupTaskID string `gorm:"column:cleanup_task_id"`
	RowKind       string `gorm:"column:row_kind"`
	Position      int    `gorm:"column:position"`
	Payload       string `gorm:"column:payload"`
}

func (s *Store) WithinFindingTx(ctx context.Context, fn func(persistence.FindingRepository) error) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return fn(New(tx))
	})
}

func (s *Store) ReplaceGraph(ctx context.Context, scopeID asset.ScopeID, revision string, relationships []graph.Relationship, bindings []graph.LifecycleBinding) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		closedAt := time.Now().UTC()
		revisionRow := graphRevisionRow{ScopeID: string(scopeID), GraphRevision: revision, ObservedAt: closedAt}
		if err := tx.Table("graph_revisions").Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "scope_id"}},
			DoUpdates: clause.AssignmentColumns([]string{"graph_revision", "observed_at"}),
		}).Create(&revisionRow).Error; err != nil {
			return err
		}
		if err := tx.Table("relationships").Where("scope_id = ? AND closed_at IS NULL", string(scopeID)).Update("closed_at", closedAt).Error; err != nil {
			return err
		}
		if err := tx.Table("lifecycle_bindings").Where("scope_id = ? AND closed_at IS NULL", string(scopeID)).Update("closed_at", closedAt).Error; err != nil {
			return err
		}
		for _, relationship := range relationships {
			payload, err := encode(relationship)
			if err != nil {
				return err
			}
			row := relationshipRow{ID: string(relationship.ID), ScopeID: string(scopeID), SourceAssetID: string(relationship.SourceAssetID), TargetAssetID: string(relationship.TargetAssetID), GraphRevision: revision, ObservedAt: relationship.ObservedAt, Payload: payload}
			if err := upsert(tx, "relationships", row, []string{"scope_id", "source_asset_id", "target_asset_id", "graph_revision", "observed_at", "closed_at", "payload"}); err != nil {
				return err
			}
		}
		for _, binding := range bindings {
			payload, err := encode(binding)
			if err != nil {
				return err
			}
			row := lifecycleBindingRow{ID: string(binding.ID), ScopeID: string(scopeID), ControllerAssetID: string(binding.ControllerAssetID), ManagedAssetID: string(binding.ManagedAssetID), GraphRevision: revision, ObservedAt: binding.ObservedAt, Payload: payload}
			if err := upsert(tx, "lifecycle_bindings", row, []string{"scope_id", "controller_asset_id", "managed_asset_id", "graph_revision", "observed_at", "closed_at", "payload"}); err != nil {
				return err
			}
		}
		return closeGraphRowsForClosedAssets(tx, scopeID, closedAt)
	})
}

func closeGraphRowsForClosedAssets(tx *gorm.DB, scopeID asset.ScopeID, closedAt time.Time) error {
	if err := tx.
		Table("relationships").
		Where(
			`scope_id = ? AND closed_at IS NULL AND (
				EXISTS (
					SELECT 1 FROM assets
					WHERE assets.id = relationships.source_asset_id
					  AND assets.closed_at IS NOT NULL
				)
				OR EXISTS (
					SELECT 1 FROM assets
					WHERE assets.id = relationships.target_asset_id
					  AND assets.closed_at IS NOT NULL
				)
			)`,
			string(scopeID),
		).
		Update("closed_at", closedAt).Error; err != nil {
		return err
	}
	return tx.
		Table("lifecycle_bindings").
		Where(
			`scope_id = ? AND closed_at IS NULL AND (
				EXISTS (
					SELECT 1 FROM assets
					WHERE assets.id = lifecycle_bindings.controller_asset_id
					  AND assets.closed_at IS NOT NULL
				)
				OR EXISTS (
					SELECT 1 FROM assets
					WHERE assets.id = lifecycle_bindings.managed_asset_id
					  AND assets.closed_at IS NOT NULL
				)
			)`,
			string(scopeID),
		).
		Update("closed_at", closedAt).Error
}

func (s *Store) CloseAssetTopology(ctx context.Context, assetID asset.AssetID, closedAt time.Time) error {
	if assetID == "" || closedAt.IsZero() {
		return errors.New("asset topology closure requires asset ID and closure time")
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.
			Table("relationships").
			Where(
				"closed_at IS NULL AND (source_asset_id = ? OR target_asset_id = ?)",
				string(assetID),
				string(assetID),
			).
			Update("closed_at", closedAt).Error; err != nil {
			return err
		}
		return tx.
			Table("lifecycle_bindings").
			Where(
				"closed_at IS NULL AND (controller_asset_id = ? OR managed_asset_id = ?)",
				string(assetID),
				string(assetID),
			).
			Update("closed_at", closedAt).Error
	})
}

func (s *Store) GetGraphRevision(ctx context.Context, scopeID asset.ScopeID) (string, error) {
	var row graphRevisionRow
	if err := s.db.WithContext(ctx).Table("graph_revisions").Where("scope_id = ?", string(scopeID)).Take(&row).Error; err != nil {
		return "", mapError(err)
	}
	return row.GraphRevision, nil
}

func (s *Store) ListRelationships(ctx context.Context, assetID asset.AssetID) ([]graph.Relationship, error) {
	var rows []relationshipRow
	canonicalScopes := s.db.WithContext(ctx).Table("scopes").Select("id").Where("superseded_by_scope_id IS NULL OR superseded_by_scope_id = ''")
	if err := s.db.WithContext(ctx).Table("relationships").Where("scope_id IN (?) AND closed_at IS NULL AND (source_asset_id = ? OR target_asset_id = ?)", canonicalScopes, string(assetID), string(assetID)).Order("id ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	return decodeRows[relationshipRow, graph.Relationship](rows, func(row relationshipRow) string { return row.Payload })
}

func (s *Store) ListRelationshipsForAsset(
	ctx context.Context,
	connectionID asset.ConnectionID,
	assetID asset.AssetID,
) ([]graph.Relationship, error) {
	parentAsset := s.db.WithContext(ctx).
		Table("assets").
		Select("id").
		Where("id = ? AND connection_id = ?", string(assetID), string(connectionID))
	canonicalScopes := s.db.WithContext(ctx).
		Table("scopes").
		Select("id").
		Where("superseded_by_scope_id IS NULL OR superseded_by_scope_id = ''")
	activeRelationships := s.db.WithContext(ctx).
		Table("relationships").
		Select("id, source_asset_id, target_asset_id, payload").
		Where("closed_at IS NULL AND scope_id IN (?)", canonicalScopes)
	var rows []assetRelationshipRow
	err := s.db.WithContext(ctx).
		Table("(?) AS parent_asset", parentAsset).
		Select(
			"parent_asset.id AS parent_asset_id, relationship.id AS relationship_id, "+
				"relationship.payload AS relationship_payload",
		).
		Joins(
			"LEFT JOIN (?) AS relationship ON relationship.source_asset_id = parent_asset.id "+
				"OR relationship.target_asset_id = parent_asset.id",
			activeRelationships,
		).
		Order("relationship.id ASC").
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, persistence.ErrNotFound
	}
	values := make([]graph.Relationship, 0, len(rows))
	for _, row := range rows {
		if row.RelationshipID == nil || row.RelationshipPayload == nil {
			continue
		}
		value, err := decode[graph.Relationship](*row.RelationshipPayload)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, nil
}

func (s *Store) ListLifecycleBindings(ctx context.Context, assetID asset.AssetID) ([]graph.LifecycleBinding, error) {
	var rows []lifecycleBindingRow
	canonicalScopes := s.db.WithContext(ctx).Table("scopes").Select("id").Where("superseded_by_scope_id IS NULL OR superseded_by_scope_id = ''")
	if err := s.db.WithContext(ctx).Table("lifecycle_bindings").Where("scope_id IN (?) AND closed_at IS NULL AND (managed_asset_id = ? OR controller_asset_id = ?)", canonicalScopes, string(assetID), string(assetID)).Order("id ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	return decodeRows[lifecycleBindingRow, graph.LifecycleBinding](rows, func(row lifecycleBindingRow) string { return row.Payload })
}

func (s *Store) ListLifecycleBindingsForAsset(
	ctx context.Context,
	connectionID asset.ConnectionID,
	assetID asset.AssetID,
) ([]graph.LifecycleBinding, error) {
	parentAsset := s.db.WithContext(ctx).
		Table("assets").
		Select("id").
		Where("id = ? AND connection_id = ?", string(assetID), string(connectionID))
	canonicalScopes := s.db.WithContext(ctx).
		Table("scopes").
		Select("id").
		Where("superseded_by_scope_id IS NULL OR superseded_by_scope_id = ''")
	activeBindings := s.db.WithContext(ctx).
		Table("lifecycle_bindings").
		Select("id, controller_asset_id, managed_asset_id, payload").
		Where("closed_at IS NULL AND scope_id IN (?)", canonicalScopes)
	var rows []assetLifecycleBindingRow
	err := s.db.WithContext(ctx).
		Table("(?) AS parent_asset", parentAsset).
		Select(
			"parent_asset.id AS parent_asset_id, binding.id AS binding_id, "+
				"binding.payload AS binding_payload",
		).
		Joins(
			"LEFT JOIN (?) AS binding ON binding.controller_asset_id = parent_asset.id "+
				"OR binding.managed_asset_id = parent_asset.id",
			activeBindings,
		).
		Order("binding.id ASC").
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, persistence.ErrNotFound
	}
	values := make([]graph.LifecycleBinding, 0, len(rows))
	for _, row := range rows {
		if row.BindingID == nil || row.BindingPayload == nil {
			continue
		}
		value, err := decode[graph.LifecycleBinding](*row.BindingPayload)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, nil
}

func (s *Store) PutFinding(ctx context.Context, value finding.Finding) error {
	payload, err := encode(value)
	if err != nil {
		return err
	}
	row := findingRow{ID: string(value.ID), AssetID: string(value.AssetID), RuleID: value.RuleID, Status: string(value.Status), Severity: string(value.Severity), LastSeenAt: value.LastSeenAt, ClosedAt: value.ClosedAt, Payload: payload}
	return upsert(s.db.WithContext(ctx), "findings", row, []string{"status", "severity", "last_seen_at", "closed_at", "payload"})
}

func (s *Store) ListFindingsByAsset(ctx context.Context, assetID asset.AssetID) ([]finding.Finding, error) {
	var rows []findingRow
	if err := s.db.WithContext(ctx).Table("findings").Where("asset_id = ?", string(assetID)).Order("last_seen_at DESC, id ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	return decodeRows[findingRow, finding.Finding](rows, func(row findingRow) string { return row.Payload })
}

func (s *Store) ListFindingsForAsset(ctx context.Context, connectionID asset.ConnectionID, assetID asset.AssetID) ([]finding.Finding, error) {
	parentAsset := s.db.WithContext(ctx).
		Table("assets").
		Select("id").
		Where("id = ? AND connection_id = ?", string(assetID), string(connectionID))
	var rows []assetFindingRow
	err := s.db.WithContext(ctx).
		Table("(?) AS parent_asset", parentAsset).
		Select(`
			parent_asset.id AS parent_asset_id,
			findings.id AS finding_id,
			findings.payload AS finding_payload`).
		Joins("LEFT JOIN findings ON findings.asset_id = parent_asset.id").
		Order("findings.last_seen_at DESC, findings.id ASC").
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, persistence.ErrNotFound
	}
	result := make([]finding.Finding, 0, len(rows))
	for _, row := range rows {
		if row.FindingID == nil {
			continue
		}
		if row.FindingPayload == nil {
			return nil, errors.New("decode repository payload: finding payload is missing")
		}
		value, decodeErr := decode[finding.Finding](*row.FindingPayload)
		if decodeErr != nil {
			return nil, decodeErr
		}
		result = append(result, value)
	}
	return result, nil
}

func (s *Store) CountOpenFindingsByAssetIDs(ctx context.Context, assetIDs []asset.AssetID) (map[asset.AssetID]int, error) {
	const batchSize = 400
	result := make(map[asset.AssetID]int)
	seen := make(map[asset.AssetID]struct{}, len(assetIDs))
	for start := 0; start < len(assetIDs); start += batchSize {
		end := min(start+batchSize, len(assetIDs))
		ids := make([]string, 0, end-start)
		for _, id := range assetIDs[start:end] {
			if id == "" {
				continue
			}
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			ids = append(ids, string(id))
		}
		if len(ids) == 0 {
			continue
		}
		var rows []struct {
			AssetID string `gorm:"column:asset_id"`
			Count   int    `gorm:"column:finding_count"`
		}
		if err := s.db.WithContext(ctx).
			Table("findings").
			Select("asset_id, COUNT(*) AS finding_count").
			Where(
				"asset_id IN ? AND status = ? AND closed_at IS NULL",
				ids,
				string(finding.StatusOpen),
			).
			Group("asset_id").
			Scan(&rows).Error; err != nil {
			return nil, err
		}
		for _, row := range rows {
			result[asset.AssetID(row.AssetID)] += row.Count
		}
	}
	return result, nil
}

func (s *Store) ListFindings(ctx context.Context, options persistence.ListOptions) (persistence.Page[finding.Finding], error) {
	limit := normalizeLimit(options.Limit)
	query := s.db.WithContext(ctx).Table("findings")
	if options.ConnectionID != "" {
		assets := s.db.WithContext(ctx).Table("assets").Select("id").Where("connection_id = ?", string(options.ConnectionID))
		query = query.Where("asset_id IN (?)", assets)
	}
	if options.Cursor != "" {
		lastSeenAt, id, err := decodeCursor(options.Cursor)
		if err != nil {
			return persistence.Page[finding.Finding]{}, err
		}
		query = query.Where("last_seen_at > ? OR (last_seen_at = ? AND id > ?)", lastSeenAt, lastSeenAt, id)
	}
	var rows []findingRow
	if err := query.Order("last_seen_at ASC, id ASC").Limit(limit + 1).Find(&rows).Error; err != nil {
		return persistence.Page[finding.Finding]{}, err
	}
	return decodePage[findingRow, finding.Finding](rows, limit, func(row findingRow) time.Time { return row.LastSeenAt }, func(row findingRow) string { return row.ID }, func(row findingRow) string { return row.Payload })
}

func (s *Store) CreateTask(ctx context.Context, value plan.CleanupTask, steps []plan.CleanupTaskStep, impacts []plan.ImpactItem) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		payload, err := encode(value)
		if err != nil {
			return err
		}
		row := cleanupTaskRecord{ID: string(value.ID), ConnectionID: string(value.ConnectionID), Status: string(value.Status), CreatedBy: value.CreatedBy, CreatedAt: value.CreatedAt, Payload: payload}
		if err := mapCreateError(tx.Table("cleanup_tasks").Create(&row).Error); err != nil {
			return err
		}
		for index, step := range steps {
			payload, err := encode(step)
			if err != nil {
				return err
			}
			stepRow := cleanupTaskRow{ID: string(step.ID), CleanupTaskID: string(value.ID), RowKind: "step", Position: index, Payload: payload}
			if err := mapCreateError(tx.Table("cleanup_task_rows").Create(&stepRow).Error); err != nil {
				return err
			}
		}
		for index, impact := range impacts {
			payload, err := encode(impact)
			if err != nil {
				return err
			}
			impactRow := cleanupTaskRow{ID: string(impact.ID), CleanupTaskID: string(value.ID), RowKind: "impact", Position: index, Payload: payload}
			if err := mapCreateError(tx.Table("cleanup_task_rows").Create(&impactRow).Error); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Store) GetTask(ctx context.Context, id plan.CleanupTaskID) (persistence.CleanupTaskAggregate, error) {
	var row cleanupTaskRecord
	if err := s.db.WithContext(ctx).Table("cleanup_tasks").Where("id = ?", string(id)).Take(&row).Error; err != nil {
		return persistence.CleanupTaskAggregate{}, mapError(err)
	}
	value, err := decode[plan.CleanupTask](row.Payload)
	if err != nil {
		return persistence.CleanupTaskAggregate{}, err
	}
	var rows []cleanupTaskRow
	if err := s.db.WithContext(ctx).Table("cleanup_task_rows").Where("cleanup_task_id = ?", string(id)).Order("row_kind ASC, position ASC, id ASC").Find(&rows).Error; err != nil {
		return persistence.CleanupTaskAggregate{}, err
	}
	aggregate := persistence.CleanupTaskAggregate{Task: value}
	for _, item := range rows {
		switch item.RowKind {
		case "step":
			step, err := decode[plan.CleanupTaskStep](item.Payload)
			if err != nil {
				return persistence.CleanupTaskAggregate{}, err
			}
			aggregate.Steps = append(aggregate.Steps, step)
		case "impact":
			impact, err := decode[plan.ImpactItem](item.Payload)
			if err != nil {
				return persistence.CleanupTaskAggregate{}, err
			}
			aggregate.ImpactItems = append(aggregate.ImpactItems, impact)
		}
	}
	return aggregate, nil
}

func (s *Store) ListTasks(ctx context.Context, options persistence.ListOptions) (persistence.Page[plan.CleanupTask], error) {
	limit := normalizeLimit(options.Limit)
	query := s.db.WithContext(ctx).Table("cleanup_tasks")
	if options.ConnectionID != "" {
		query = query.Where("connection_id = ?", string(options.ConnectionID))
	}
	if options.Cursor != "" {
		createdAt, id, err := decodeCursor(options.Cursor)
		if err != nil {
			return persistence.Page[plan.CleanupTask]{}, err
		}
		query = query.Where("created_at < ? OR (created_at = ? AND id < ?)", createdAt, createdAt, id)
	}
	var rows []cleanupTaskRecord
	if err := query.Order("created_at DESC, id DESC").Limit(limit + 1).Find(&rows).Error; err != nil {
		return persistence.Page[plan.CleanupTask]{}, err
	}
	return decodePage[cleanupTaskRecord, plan.CleanupTask](rows, limit, func(row cleanupTaskRecord) time.Time { return row.CreatedAt }, func(row cleanupTaskRecord) string { return row.ID }, func(row cleanupTaskRecord) string { return row.Payload })
}

func (s *Store) ReplaceTask(ctx context.Context, value plan.CleanupTask, steps []plan.CleanupTaskStep, impacts []plan.ImpactItem) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		payload, err := encode(value)
		if err != nil {
			return err
		}
		result := tx.Table("cleanup_tasks").Where("id = ?", string(value.ID)).Updates(map[string]any{
			"status": value.Status, "payload": payload,
		})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return persistence.ErrNotFound
		}
		if err := tx.Table("cleanup_task_rows").Where("cleanup_task_id = ?", string(value.ID)).Delete(&cleanupTaskRow{}).Error; err != nil {
			return err
		}
		for index, step := range steps {
			stepPayload, err := encode(step)
			if err != nil {
				return err
			}
			row := cleanupTaskRow{ID: string(step.ID), CleanupTaskID: string(value.ID), RowKind: "step", Position: index, Payload: stepPayload}
			if err := mapCreateError(tx.Table("cleanup_task_rows").Create(&row).Error); err != nil {
				return err
			}
		}
		for index, impact := range impacts {
			impactPayload, err := encode(impact)
			if err != nil {
				return err
			}
			row := cleanupTaskRow{ID: string(impact.ID), CleanupTaskID: string(value.ID), RowKind: "impact", Position: index, Payload: impactPayload}
			if err := mapCreateError(tx.Table("cleanup_task_rows").Create(&row).Error); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Store) UpdateTask(ctx context.Context, value plan.CleanupTask) error {
	payload, err := encode(value)
	if err != nil {
		return err
	}
	result := s.db.WithContext(ctx).Table("cleanup_tasks").Where("id = ?", string(value.ID)).Updates(map[string]any{
		"status": value.Status, "payload": payload,
	})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return persistence.ErrNotFound
	}
	return nil
}

func (s *Store) UpdateImpactItems(ctx context.Context, cleanupTaskID plan.CleanupTaskID, values []plan.ImpactItem) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, value := range values {
			if value.CleanupTaskID != cleanupTaskID || value.ID == "" {
				return persistence.ErrConflict
			}
			payload, err := encode(value)
			if err != nil {
				return err
			}
			result := tx.Table("cleanup_task_rows").Where("id = ? AND cleanup_task_id = ? AND row_kind = ?", string(value.ID), string(cleanupTaskID), "impact").Update("payload", payload)
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return persistence.ErrNotFound
			}
		}
		return nil
	})
}
