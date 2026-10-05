package relational

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"maps"
	"slices"
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
	WorkspaceID   string     `gorm:"column:workspace_id"`
	ID            string     `gorm:"column:id;primaryKey"`
	ScopeID       string     `gorm:"column:scope_id"`
	SourceAssetID string     `gorm:"column:source_asset_id"`
	TargetAssetID string     `gorm:"column:target_asset_id"`
	GraphRevision string     `gorm:"column:graph_revision"`
	ObservedAt    time.Time  `gorm:"column:observed_at"`
	ClosedAt      *time.Time `gorm:"column:closed_at"`
	Payload       string     `gorm:"column:payload"`
	ItemDefaults  *string    `gorm:"column:item_defaults;->"`
}

type lifecycleBindingRow struct {
	WorkspaceID       string     `gorm:"column:workspace_id"`
	ID                string     `gorm:"column:id;primaryKey"`
	ScopeID           string     `gorm:"column:scope_id"`
	ControllerAssetID string     `gorm:"column:controller_asset_id"`
	ManagedAssetID    string     `gorm:"column:managed_asset_id"`
	GraphRevision     string     `gorm:"column:graph_revision"`
	ObservedAt        time.Time  `gorm:"column:observed_at"`
	ClosedAt          *time.Time `gorm:"column:closed_at"`
	Payload           string     `gorm:"column:payload"`
	ItemDefaults      *string    `gorm:"column:item_defaults;->"`
}

type assetRelationshipRow struct {
	ParentAssetID       string  `gorm:"column:parent_asset_id"`
	RelationshipID      *string `gorm:"column:relationship_id"`
	RelationshipPayload *string `gorm:"column:relationship_payload"`
	ItemDefaults        *string `gorm:"column:item_defaults"`
}

type assetLifecycleBindingRow struct {
	ParentAssetID  string  `gorm:"column:parent_asset_id"`
	BindingID      *string `gorm:"column:binding_id"`
	BindingPayload *string `gorm:"column:binding_payload"`
	ItemDefaults   *string `gorm:"column:item_defaults"`
}

type graphRevisionRow struct {
	WorkspaceID       string    `gorm:"column:workspace_id"`
	UnresolvedPayload string    `gorm:"column:unresolved_payload"`
	ScopeID           string    `gorm:"column:scope_id;primaryKey"`
	GraphRevision     string    `gorm:"column:graph_revision"`
	ObservedAt        time.Time `gorm:"column:observed_at"`
	ItemDefaults      string    `gorm:"column:item_defaults"`
}

// graphItemDefaults holds the revision and observation time a rebuild stamps
// on its relationships and bindings. Rows store them blank and readers fill
// them back in, so a row of an unchanged edge stays byte-identical across
// rebuilds. A field is shared only when no item already has it blank, so a
// blank stored field always means "the default".
type graphItemDefaults struct {
	GraphRevision string     `json:"graph_revision,omitempty"`
	ObservedAt    *time.Time `json:"observed_at,omitempty"`
	// observedAt is ObservedAt in its stored form.
	observedAt string
}

func newGraphItemDefaults(revision string, relationships []graph.Relationship, bindings []graph.LifecycleBinding) (graphItemDefaults, error) {
	defaults := graphItemDefaults{GraphRevision: revision}
	// Share the most common observation time, compared in its stored form so
	// the filled-in value decodes exactly as the item's own would have.
	counts := make(map[string]int)
	common, shareTime := "", true
	note := func(revision string, observedAt time.Time) error {
		if revision == "" {
			defaults.GraphRevision = ""
		}
		if observedAt.IsZero() {
			shareTime = false
		}
		if !shareTime {
			return nil
		}
		encoded, err := observedAt.MarshalJSON()
		if err != nil {
			return err
		}
		counts[string(encoded)]++
		if counts[string(encoded)] > counts[common] {
			common = string(encoded)
		}
		return nil
	}
	for _, value := range relationships {
		if err := note(value.GraphRevision, value.ObservedAt); err != nil {
			return graphItemDefaults{}, err
		}
	}
	for _, value := range bindings {
		if err := note(value.GraphRevision, value.ObservedAt); err != nil {
			return graphItemDefaults{}, err
		}
	}
	if shareTime && common != "" {
		var observedAt time.Time
		if err := observedAt.UnmarshalJSON([]byte(common)); err != nil {
			return graphItemDefaults{}, err
		}
		defaults.ObservedAt, defaults.observedAt = &observedAt, common
	}
	return defaults, nil
}

// strip blanks the fields an item shares with the defaults.
func (d graphItemDefaults) strip(revision *string, observedAt *time.Time) {
	if d.GraphRevision != "" && *revision == d.GraphRevision {
		*revision = ""
	}
	if d.observedAt != "" {
		if encoded, err := observedAt.MarshalJSON(); err == nil && string(encoded) == d.observedAt {
			*observedAt = time.Time{}
		}
	}
}

// fill restores the fields strip blanked.
func (d graphItemDefaults) fill(revision *string, observedAt *time.Time) {
	if *revision == "" {
		*revision = d.GraphRevision
	}
	if observedAt.IsZero() && d.ObservedAt != nil {
		*observedAt = *d.ObservedAt
	}
}

// graphItemDecoder decodes graph rows, filling in their scope's defaults.
// Rows written before the defaults existed carry every field themselves.
type graphItemDecoder map[string]graphItemDefaults

func (c graphItemDecoder) defaults(encoded *string) (graphItemDefaults, error) {
	if encoded == nil || *encoded == "" {
		return graphItemDefaults{}, nil
	}
	if cached, ok := c[*encoded]; ok {
		return cached, nil
	}
	value, err := decode[graphItemDefaults](*encoded)
	if err != nil {
		return graphItemDefaults{}, err
	}
	c[*encoded] = value
	return value, nil
}

func (c graphItemDecoder) relationship(payload string, encodedDefaults *string) (graph.Relationship, error) {
	value, err := decode[graph.Relationship](payload)
	if err != nil {
		return value, err
	}
	defaults, err := c.defaults(encodedDefaults)
	defaults.fill(&value.GraphRevision, &value.ObservedAt)
	return value, err
}

func (c graphItemDecoder) binding(payload string, encodedDefaults *string) (graph.LifecycleBinding, error) {
	value, err := decode[graph.LifecycleBinding](payload)
	if err != nil {
		return value, err
	}
	defaults, err := c.defaults(encodedDefaults)
	defaults.fill(&value.GraphRevision, &value.ObservedAt)
	return value, err
}

func decodeRelationshipRows(rows []relationshipRow) ([]graph.Relationship, error) {
	decoder := graphItemDecoder{}
	values := make([]graph.Relationship, 0, len(rows))
	for _, row := range rows {
		value, err := decoder.relationship(row.Payload, row.ItemDefaults)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, nil
}

func decodeBindingRows(rows []lifecycleBindingRow) ([]graph.LifecycleBinding, error) {
	decoder := graphItemDecoder{}
	values := make([]graph.LifecycleBinding, 0, len(rows))
	for _, row := range rows {
		value, err := decoder.binding(row.Payload, row.ItemDefaults)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, nil
}

// Graph row reads select their scope's item defaults alongside.
const (
	relationshipColumns = "relationships.*, (SELECT item_defaults FROM graph_revisions WHERE graph_revisions.scope_id = relationships.scope_id) AS item_defaults"
	bindingColumns      = "lifecycle_bindings.*, (SELECT item_defaults FROM graph_revisions WHERE graph_revisions.scope_id = lifecycle_bindings.scope_id) AS item_defaults"
)

type findingRow struct {
	WorkspaceID string     `gorm:"column:workspace_id"`
	ID          string     `gorm:"column:id;primaryKey"`
	AssetID     string     `gorm:"column:asset_id"`
	RuleID      string     `gorm:"column:rule_id"`
	Status      string     `gorm:"column:status"`
	Severity    string     `gorm:"column:severity"`
	LastSeenAt  time.Time  `gorm:"column:last_seen_at"`
	ClosedAt    *time.Time `gorm:"column:closed_at"`
	Payload     string     `gorm:"column:payload"`
}

type assetFindingRow struct {
	ParentAssetID  string  `gorm:"column:parent_asset_id"`
	FindingID      *string `gorm:"column:finding_id"`
	FindingPayload *string `gorm:"column:finding_payload"`
}

type cleanupTaskRecord struct {
	WorkspaceID  string    `gorm:"column:workspace_id"`
	ID           string    `gorm:"column:id;primaryKey"`
	ConnectionID string    `gorm:"column:connection_id"`
	Status       string    `gorm:"column:status"`
	CreatedBy    string    `gorm:"column:created_by"`
	CreatedAt    time.Time `gorm:"column:created_at"`
	Payload      string    `gorm:"column:payload"`
}

type cleanupTaskRow struct {
	WorkspaceID   string `gorm:"column:workspace_id"`
	ID            string `gorm:"column:id;primaryKey"`
	CleanupTaskID string `gorm:"column:cleanup_task_id"`
	RowKind       string `gorm:"column:row_kind"`
	Position      int    `gorm:"column:position"`
	Payload       string `gorm:"column:payload"`
}

func (s *Store) WithinFindingTx(ctx context.Context, fn func(persistence.FindingRepository) error) error {
	return s.transaction(ctx, func(store *Store) error { return fn(store) })
}

func (s *Store) ReplaceGraph(ctx context.Context, scopeID asset.ScopeID, revision string, relationships []graph.Relationship, bindings []graph.LifecycleBinding, unresolved ...graph.UnresolvedReference) error {
	return s.transaction(ctx, func(store *Store) error {
		tx := store.db
		var connectionIDs []string
		if err := tx.Table("scopes").Where("id = ?", string(scopeID)).Pluck("connection_id", &connectionIDs).Error; err != nil {
			return err
		}
		store.touch(connectionIDs...)
		closedAt := time.Now().UTC()
		payload, err := encode(unresolved)
		if err != nil {
			return err
		}
		defaults, err := newGraphItemDefaults(revision, relationships, bindings)
		if err != nil {
			return err
		}
		defaultsPayload, err := encode(defaults)
		if err != nil {
			return err
		}
		revisionRow := graphRevisionRow{UnresolvedPayload: payload, ScopeID: string(scopeID), GraphRevision: revision, ObservedAt: closedAt, ItemDefaults: defaultsPayload}
		if err := tx.Table("graph_revisions").Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "scope_id"}},
			DoUpdates: clause.AssignmentColumns([]string{"graph_revision", "observed_at", "unresolved_payload", "item_defaults"}),
		}).Create(&revisionRow).Error; err != nil {
			return err
		}
		referenced := make(map[string]struct{})
		relationshipRows := make([]relationshipRow, 0, len(relationships))
		relationshipPositions := make(map[string]int, len(relationships))
		for _, relationship := range relationships {
			observedAt := relationship.ObservedAt
			defaults.strip(&relationship.GraphRevision, &relationship.ObservedAt)
			payload, err := encode(relationship)
			if err != nil {
				return err
			}
			row := relationshipRow{ID: string(relationship.ID), ScopeID: string(scopeID), SourceAssetID: string(relationship.SourceAssetID), TargetAssetID: string(relationship.TargetAssetID), GraphRevision: revision, ObservedAt: observedAt, Payload: payload}
			referenced[row.SourceAssetID], referenced[row.TargetAssetID] = struct{}{}, struct{}{}
			// A repeated ID keeps its last value, as one upsert per row did;
			// PostgreSQL rejects a multi-row upsert that touches a row twice.
			if position, ok := relationshipPositions[row.ID]; ok {
				relationshipRows[position] = row
				continue
			}
			relationshipPositions[row.ID] = len(relationshipRows)
			relationshipRows = append(relationshipRows, row)
		}
		if err := syncGraphRows(tx, "relationships", scopeID, relationshipRows,
			func(row relationshipRow) (string, string) { return row.ID, row.Payload },
			[]string{"scope_id", "source_asset_id", "target_asset_id", "graph_revision", "observed_at", "closed_at", "payload"},
		); err != nil {
			return err
		}
		bindingRows := make([]lifecycleBindingRow, 0, len(bindings))
		bindingPositions := make(map[string]int, len(bindings))
		for _, binding := range bindings {
			observedAt := binding.ObservedAt
			defaults.strip(&binding.GraphRevision, &binding.ObservedAt)
			payload, err := encode(binding)
			if err != nil {
				return err
			}
			row := lifecycleBindingRow{ID: string(binding.ID), ScopeID: string(scopeID), ControllerAssetID: string(binding.ControllerAssetID), ManagedAssetID: string(binding.ManagedAssetID), GraphRevision: revision, ObservedAt: observedAt, Payload: payload}
			referenced[row.ControllerAssetID], referenced[row.ManagedAssetID] = struct{}{}, struct{}{}
			if position, ok := bindingPositions[row.ID]; ok {
				bindingRows[position] = row
				continue
			}
			bindingPositions[row.ID] = len(bindingRows)
			bindingRows = append(bindingRows, row)
		}
		if err := syncGraphRows(tx, "lifecycle_bindings", scopeID, bindingRows,
			func(row lifecycleBindingRow) (string, string) { return row.ID, row.Payload },
			[]string{"scope_id", "controller_asset_id", "managed_asset_id", "graph_revision", "observed_at", "closed_at", "payload"},
		); err != nil {
			return err
		}
		return closeGraphRowsForClosedAssets(tx, scopeID, slices.Collect(maps.Keys(referenced)), closedAt)
	})
}

// syncGraphRows makes the scope's rows of table exactly rows, all open. Every
// graph read filters closed_at IS NULL and a row's ID derives from what it
// connects, so a row missing from rows is deleted, and only new, reopened or
// changed rows are written: an unchanged graph rewrites nothing.
func syncGraphRows[T any](tx *gorm.DB, table string, scopeID asset.ScopeID, rows []T, key func(T) (string, string), columns []string) error {
	cursor, err := tx.Table(table).Select("id, payload, closed_at").Where("scope_id = ?", string(scopeID)).Rows()
	if err != nil {
		return err
	}
	// A digest instead of the payload keeps a large graph's diff small.
	current := make(map[string][sha256.Size]byte)
	for cursor.Next() {
		var id, payload string
		var closedAt *time.Time
		if err := cursor.Scan(&id, &payload, &closedAt); err != nil {
			cursor.Close()
			return err
		}
		var digest [sha256.Size]byte
		if closedAt == nil {
			digest = sha256.Sum256([]byte(payload))
		}
		current[id] = digest
	}
	if err := errors.Join(cursor.Err(), cursor.Close()); err != nil {
		return err
	}
	changed := make([]T, 0)
	for _, row := range rows {
		id, payload := key(row)
		digest, exists := current[id]
		delete(current, id)
		if exists && digest == sha256.Sum256([]byte(payload)) {
			continue
		}
		changed = append(changed, row)
	}
	// Every stale ID was read from this scope's rows in this transaction.
	stale := slices.Sorted(maps.Keys(current))
	for start := 0; start < len(stale); start += upsertBatchSize {
		if err := tx.Table(table).Where("id IN ?", stale[start:min(start+upsertBatchSize, len(stale))]).Delete(nil).Error; err != nil {
			return err
		}
	}
	return upsertInBatches(tx, table, changed, columns)
}

// closeGraphRowsForClosedAssets closes the scope's open rows that touch a
// closed asset. After a rebuild those rows reference only assetIDs, so only
// they are probed instead of both endpoints of every row.
func closeGraphRowsForClosedAssets(tx *gorm.DB, scopeID asset.ScopeID, assetIDs []string, closedAt time.Time) error {
	slices.Sort(assetIDs)
	var closed []string
	for start := 0; start < len(assetIDs); start += upsertBatchSize {
		var batch []string
		if err := tx.Table("assets").Where("id IN ? AND closed_at IS NOT NULL", assetIDs[start:min(start+upsertBatchSize, len(assetIDs))]).Pluck("id", &batch).Error; err != nil {
			return err
		}
		closed = append(closed, batch...)
	}
	for start := 0; start < len(closed); start += upsertBatchSize {
		batch := closed[start:min(start+upsertBatchSize, len(closed))]
		if err := tx.
			Table("relationships").
			Where("scope_id = ? AND closed_at IS NULL AND (source_asset_id IN ? OR target_asset_id IN ?)", string(scopeID), batch, batch).
			Update("closed_at", closedAt).Error; err != nil {
			return err
		}
		if err := tx.
			Table("lifecycle_bindings").
			Where("scope_id = ? AND closed_at IS NULL AND (controller_asset_id IN ? OR managed_asset_id IN ?)", string(scopeID), batch, batch).
			Update("closed_at", closedAt).Error; err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) CloseAssetTopology(ctx context.Context, assetID asset.AssetID, closedAt time.Time) error {
	if assetID == "" || closedAt.IsZero() {
		return errors.New("asset topology closure requires asset ID and closure time")
	}
	return s.transaction(ctx, func(store *Store) error {
		tx := store.db
		// The asset's connection, and that of any scope whose open rows the
		// closure touches.
		var connectionIDs []string
		if err := tx.Raw(`SELECT connection_id FROM assets WHERE id = ?
			UNION SELECT connection_id FROM scopes WHERE id IN (
				SELECT scope_id FROM relationships WHERE closed_at IS NULL AND (source_asset_id = ? OR target_asset_id = ?)
				UNION SELECT scope_id FROM lifecycle_bindings WHERE closed_at IS NULL AND (controller_asset_id = ? OR managed_asset_id = ?))`,
			string(assetID), string(assetID), string(assetID), string(assetID), string(assetID)).Scan(&connectionIDs).Error; err != nil {
			return err
		}
		store.touch(connectionIDs...)
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
	if err := s.db.WithContext(ctx).Table("relationships").Select(relationshipColumns).Where("scope_id IN (?) AND closed_at IS NULL AND (source_asset_id = ? OR target_asset_id = ?)", canonicalScopes, string(assetID), string(assetID)).Order("id ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	return decodeRelationshipRows(rows)
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
		Select("id, source_asset_id, target_asset_id, payload, scope_id").
		Where("closed_at IS NULL AND scope_id IN (?)", canonicalScopes)
	var rows []assetRelationshipRow
	err := s.db.WithContext(ctx).
		Table("(?) AS parent_asset", parentAsset).
		Select(
			"parent_asset.id AS parent_asset_id, relationship.id AS relationship_id, "+
				"relationship.payload AS relationship_payload, graph_revisions.item_defaults AS item_defaults",
		).
		Joins(
			"LEFT JOIN (?) AS relationship ON relationship.source_asset_id = parent_asset.id "+
				"OR relationship.target_asset_id = parent_asset.id",
			activeRelationships,
		).
		Joins("LEFT JOIN graph_revisions ON graph_revisions.scope_id = relationship.scope_id").
		Order("relationship.id ASC").
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, persistence.ErrNotFound
	}
	values := make([]graph.Relationship, 0, len(rows))
	decoder := graphItemDecoder{}
	for _, row := range rows {
		if row.RelationshipID == nil || row.RelationshipPayload == nil {
			continue
		}
		value, err := decoder.relationship(*row.RelationshipPayload, row.ItemDefaults)
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
	if err := s.db.WithContext(ctx).Table("lifecycle_bindings").Select(bindingColumns).Where("scope_id IN (?) AND closed_at IS NULL AND (managed_asset_id = ? OR controller_asset_id = ?)", canonicalScopes, string(assetID), string(assetID)).Order("id ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	return decodeBindingRows(rows)
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
		Select("id, controller_asset_id, managed_asset_id, payload, scope_id").
		Where("closed_at IS NULL AND scope_id IN (?)", canonicalScopes)
	var rows []assetLifecycleBindingRow
	err := s.db.WithContext(ctx).
		Table("(?) AS parent_asset", parentAsset).
		Select(
			"parent_asset.id AS parent_asset_id, binding.id AS binding_id, "+
				"binding.payload AS binding_payload, graph_revisions.item_defaults AS item_defaults",
		).
		Joins(
			"LEFT JOIN (?) AS binding ON binding.controller_asset_id = parent_asset.id "+
				"OR binding.managed_asset_id = parent_asset.id",
			activeBindings,
		).
		Joins("LEFT JOIN graph_revisions ON graph_revisions.scope_id = binding.scope_id").
		Order("binding.id ASC").
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, persistence.ErrNotFound
	}
	values := make([]graph.LifecycleBinding, 0, len(rows))
	decoder := graphItemDecoder{}
	for _, row := range rows {
		if row.BindingID == nil || row.BindingPayload == nil {
			continue
		}
		value, err := decoder.binding(*row.BindingPayload, row.ItemDefaults)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, nil
}

func (s *Store) PutFinding(ctx context.Context, value finding.Finding) error {
	return s.PutFindings(ctx, []finding.Finding{value})
}

func (s *Store) PutFindings(ctx context.Context, values []finding.Finding) error {
	rows := make([]findingRow, 0, len(values))
	positions := make(map[string]int, len(values))
	for _, value := range values {
		payload, err := encode(value)
		if err != nil {
			return err
		}
		row := findingRow{ID: string(value.ID), AssetID: string(value.AssetID), RuleID: value.RuleID, Status: string(value.Status), Severity: string(value.Severity), LastSeenAt: value.LastSeenAt, ClosedAt: value.ClosedAt, Payload: payload}
		// PostgreSQL rejects a multi-row upsert that touches a row twice.
		if position, ok := positions[row.ID]; ok {
			rows[position] = row
			continue
		}
		positions[row.ID] = len(rows)
		rows = append(rows, row)
	}
	return s.write(ctx, func(store *Store) error {
		assetIDs := make(map[string]struct{}, len(rows))
		for _, row := range rows {
			assetIDs[row.AssetID] = struct{}{}
		}
		if err := store.touchAssetConnections(slices.Sorted(maps.Keys(assetIDs))); err != nil {
			return err
		}
		return upsertRevised(store.db, "findings", rows, []string{"status", "severity", "last_seen_at", "closed_at", "payload"})
	})
}

// touchAssetConnections touches the connections that own assetIDs.
func (s *Store) touchAssetConnections(assetIDs []string) error {
	for start := 0; start < len(assetIDs); start += upsertBatchSize {
		var connectionIDs []string
		if err := s.db.Table("assets").Distinct("connection_id").Where("id IN ?", assetIDs[start:min(start+upsertBatchSize, len(assetIDs))]).Pluck("connection_id", &connectionIDs).Error; err != nil {
			return err
		}
		s.touch(connectionIDs...)
	}
	return nil
}

func (s *Store) ListFindingsByAsset(ctx context.Context, assetID asset.AssetID) ([]finding.Finding, error) {
	var rows []findingRow
	if err := s.db.WithContext(ctx).Table("findings").Where("asset_id = ?", string(assetID)).Order("last_seen_at DESC, id ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	return decodeRows[findingRow, finding.Finding](rows, func(row findingRow) string { return row.Payload })
}

func (s *Store) ListFindingsByAssetIDs(ctx context.Context, assetIDs []asset.AssetID) (map[asset.AssetID][]finding.Finding, error) {
	result := make(map[asset.AssetID][]finding.Finding)
	unique := make([]string, 0, len(assetIDs))
	for _, id := range assetIDs {
		if _, ok := result[id]; !ok {
			result[id] = nil
			unique = append(unique, string(id))
		}
	}
	for start := 0; start < len(unique); start += topologyGraphAssetBatchSize {
		ids := unique[start:min(start+topologyGraphAssetBatchSize, len(unique))]
		var rows []findingRow
		if err := s.db.WithContext(ctx).Table("findings").Where("asset_id IN ?", ids).Order("asset_id ASC, last_seen_at DESC, id ASC").Find(&rows).Error; err != nil {
			return nil, err
		}
		for _, row := range rows {
			value, err := decode[finding.Finding](row.Payload)
			if err != nil {
				return nil, err
			}
			result[asset.AssetID(row.AssetID)] = append(result[asset.AssetID(row.AssetID)], value)
		}
	}
	return result, nil
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
		return insertCleanupTaskRows(tx, value.ID, steps, impacts)
	})
}

func insertCleanupTaskRows(tx *gorm.DB, taskID plan.CleanupTaskID, steps []plan.CleanupTaskStep, impacts []plan.ImpactItem) error {
	rows := make([]cleanupTaskRow, 0, len(steps)+len(impacts))
	for index, step := range steps {
		payload, err := encode(step)
		if err != nil {
			return err
		}
		rows = append(rows, cleanupTaskRow{ID: string(step.ID), CleanupTaskID: string(taskID), RowKind: "step", Position: index, Payload: payload})
	}
	for index, impact := range impacts {
		payload, err := encode(impact)
		if err != nil {
			return err
		}
		rows = append(rows, cleanupTaskRow{ID: string(impact.ID), CleanupTaskID: string(taskID), RowKind: "impact", Position: index, Payload: payload})
	}
	if len(rows) == 0 {
		return nil
	}
	return mapCreateError(tx.Table("cleanup_task_rows").CreateInBatches(rows, upsertBatchSize).Error)
}

func (s *Store) GetTaskHeader(ctx context.Context, id plan.CleanupTaskID) (plan.CleanupTask, error) {
	var row cleanupTaskRecord
	if err := s.db.WithContext(ctx).Table("cleanup_tasks").Where("id = ?", string(id)).Take(&row).Error; err != nil {
		return plan.CleanupTask{}, mapError(err)
	}
	return decode[plan.CleanupTask](row.Payload)
}

func (s *Store) GetTask(ctx context.Context, id plan.CleanupTaskID) (persistence.CleanupTaskAggregate, error) {
	value, err := s.GetTaskHeader(ctx, id)
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
			"status": value.Status, "payload": payload, "revision": gorm.Expr("revision + 1"),
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
		return insertCleanupTaskRows(tx, value.ID, steps, impacts)
	})
}

func (s *Store) UpdateTask(ctx context.Context, value plan.CleanupTask) error {
	payload, err := encode(value)
	if err != nil {
		return err
	}
	result := s.db.WithContext(ctx).Table("cleanup_tasks").Where("id = ?", string(value.ID)).Updates(map[string]any{
		"status": value.Status, "payload": payload, "revision": gorm.Expr("revision + 1"),
	})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return persistence.ErrNotFound
	}
	return nil
}

func (s *Store) GetTaskRevision(ctx context.Context, id plan.CleanupTaskID) (int64, error) {
	var task struct {
		Revision int64 `gorm:"column:revision"`
	}
	if err := s.db.WithContext(ctx).Table("cleanup_tasks").Select("revision").Where("id = ?", string(id)).Take(&task).Error; err != nil {
		return 0, mapError(err)
	}
	return task.Revision, nil
}

func (s *Store) UpdateImpactItems(ctx context.Context, cleanupTaskID plan.CleanupTaskID, values []plan.ImpactItem) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Impact rows are part of the task GetTaskRevision versions. The task
		// row goes first, the order ReplaceTask locks them in.
		if err := tx.Table("cleanup_tasks").Where("id = ?", string(cleanupTaskID)).Update("revision", gorm.Expr("revision + 1")).Error; err != nil {
			return err
		}
		for _, value := range values {
			if value.CleanupTaskID != cleanupTaskID || value.ID == "" {
				return persistence.ErrConflict
			}
			payload, err := encode(value)
			if err != nil {
				return err
			}
			result := tx.Table("cleanup_task_rows").Where("id = ? AND cleanup_task_id = ? AND row_kind = ?", string(value.ID), string(cleanupTaskID), "impact").Updates(map[string]any{"payload": payload, "revision": gorm.Expr("revision + 1")})
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

// revisionTotals is a table's row count and revision sum. Where every update
// bumps a row's revision, the pair changes whenever the rows do.
type revisionTotals struct {
	Rows      int64 `gorm:"column:row_count"`
	Revisions int64 `gorm:"column:revision_sum"`
}

const selectRevisionTotals = "COUNT(*) AS row_count, CAST(COALESCE(SUM(revision), 0) AS BIGINT) AS revision_sum"

// CleanupTaskVersion changes whenever the task, its steps and impact rows, its
// executions or their actions do: every update bumps a row revision, rows are
// otherwise only added, and ReplaceTask, which rewrites the steps and impact
// rows, also updates the task row.
func (s *Store) CleanupTaskVersion(ctx context.Context, connectionID asset.ConnectionID, id plan.CleanupTaskID) (string, error) {
	db := s.db.WithContext(ctx)
	var task struct {
		Revision int64 `gorm:"column:revision"`
	}
	if err := db.Table("cleanup_tasks").Select("revision").Where("id = ? AND connection_id = ?", string(id), string(connectionID)).Take(&task).Error; err != nil {
		return "", mapError(err)
	}
	var rows, executions, actions revisionTotals
	if err := db.Table("cleanup_task_rows").Select(selectRevisionTotals).Where("cleanup_task_id = ?", string(id)).Scan(&rows).Error; err != nil {
		return "", err
	}
	// connection_id lets both execution queries use the
	// (connection_id, cleanup_task_id) index instead of scanning.
	if err := db.Table("execution_attempts").Select(selectRevisionTotals).Where("connection_id = ? AND cleanup_task_id = ?", string(connectionID), string(id)).Scan(&executions).Error; err != nil {
		return "", err
	}
	taskExecutions := db.Table("execution_attempts").Select("id").Where("connection_id = ? AND cleanup_task_id = ?", string(connectionID), string(id))
	if err := db.Table("action_attempts").Select(selectRevisionTotals).Where("execution_id IN (?)", taskExecutions).Scan(&actions).Error; err != nil {
		return "", err
	}
	return fmt.Sprintf("t%d:r%d.%d:e%d.%d:a%d.%d", task.Revision, rows.Rows, rows.Revisions, executions.Rows, executions.Revisions, actions.Rows, actions.Revisions), nil
}
