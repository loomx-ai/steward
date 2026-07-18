package relational

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/persistence"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type Store struct {
	db *gorm.DB
}

func New(db *gorm.DB) *Store {
	return &Store{db: db}
}

func (s *Store) Connections() persistence.ConnectionRepository   { return s }
func (s *Store) Credentials() persistence.CredentialRepository   { return s }
func (s *Store) Regions() persistence.RegionRepository           { return s }
func (s *Store) Inventory() persistence.InventoryRepository      { return s }
func (s *Store) Graph() persistence.GraphRepository              { return s }
func (s *Store) Findings() persistence.FindingRepository         { return s }
func (s *Store) CleanupTasks() persistence.CleanupTaskRepository { return s }
func (s *Store) Executions() persistence.ExecutionRepository     { return s }
func (s *Store) Audits() persistence.AuditRepository             { return s }
func (s *Store) Jobs() persistence.JobRepository                 { return s }

func (s *Store) WithTx(ctx context.Context, fn func(persistence.Repositories) error) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return fn(New(tx))
	})
}

func (s *Store) WithinInventoryTx(ctx context.Context, fn func(persistence.InventoryRepository) error) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return fn(New(tx))
	})
}

type connectionRow struct {
	ID            string     `gorm:"column:id;primaryKey"`
	Provider      string     `gorm:"column:provider"`
	PartitionName string     `gorm:"column:partition_name"`
	CreatedAt     time.Time  `gorm:"column:created_at"`
	UpdatedAt     time.Time  `gorm:"column:updated_at"`
	DeletedAt     *time.Time `gorm:"column:deleted_at"`
	Payload       string     `gorm:"column:payload"`
}

type credentialRow struct {
	ConnectionID string     `gorm:"column:connection_id;primaryKey"`
	Provider     string     `gorm:"column:provider"`
	Type         string     `gorm:"column:credential_type"`
	ExpiresAt    *time.Time `gorm:"column:expires_at"`
	CreatedAt    time.Time  `gorm:"column:created_at"`
	UpdatedAt    time.Time  `gorm:"column:updated_at"`
	Payload      string     `gorm:"column:payload"`
}

type connectionAggregateRow struct {
	ConnectionID         string  `gorm:"column:connection_id"`
	ConnectionPayload    string  `gorm:"column:connection_payload"`
	CredentialPayload    *string `gorm:"column:credential_payload"`
	ActiveRegionCount    int     `gorm:"column:active_region_count"`
	RetiredRegionCount   int     `gorm:"column:retired_region_count"`
	ExcludedRegionCount  int     `gorm:"column:excluded_region_count"`
	RegionRefreshPayload *string `gorm:"column:region_refresh_payload"`
}

type regionRow struct {
	ID           string     `gorm:"column:id;primaryKey"`
	Revision     uint64     `gorm:"column:revision"`
	ConnectionID string     `gorm:"column:connection_id"`
	RegionID     string     `gorm:"column:region_id"`
	Lifecycle    string     `gorm:"column:lifecycle"`
	Origin       string     `gorm:"column:origin"`
	FirstSeenAt  *time.Time `gorm:"column:first_seen_at"`
	LastSeenAt   *time.Time `gorm:"column:last_seen_at"`
	CreatedAt    time.Time  `gorm:"column:created_at"`
	UpdatedAt    time.Time  `gorm:"column:updated_at"`
	Payload      string     `gorm:"column:payload"`
}

type activeConnectionRegionRow struct {
	ConnectionPayload string  `gorm:"column:connection_payload"`
	ID                *string `gorm:"column:region_id_key"`
	Revision          *uint64 `gorm:"column:region_revision"`
	Payload           *string `gorm:"column:region_payload"`
}

type scopeRow struct {
	ID             string    `gorm:"column:id;primaryKey"`
	ConnectionID   string    `gorm:"column:connection_id"`
	ParentID       string    `gorm:"column:parent_id"`
	SupersededByID string    `gorm:"column:superseded_by_scope_id"`
	Kind           string    `gorm:"column:kind"`
	NativeID       string    `gorm:"column:native_id"`
	CreatedAt      time.Time `gorm:"column:created_at"`
	UpdatedAt      time.Time `gorm:"column:updated_at"`
	Payload        string    `gorm:"column:payload"`
}

type resourceKindRow struct {
	ID             string `gorm:"column:id;primaryKey"`
	Provider       string `gorm:"column:provider"`
	NativeType     string `gorm:"column:native_type"`
	BundleRevision string `gorm:"column:bundle_revision"`
	Payload        string `gorm:"column:payload"`
}

type scanRunRow struct {
	ID                   string     `gorm:"column:id;primaryKey"`
	ConnectionID         string     `gorm:"column:connection_id"`
	Status               string     `gorm:"column:status"`
	ScopeMode            string     `gorm:"column:scope_mode"`
	RetryGeneration      int        `gorm:"column:retry_generation"`
	ControlVersion       uint64     `gorm:"column:control_version"`
	ResourceCount        int        `gorm:"column:resource_count"`
	DurationMS           int64      `gorm:"column:duration_ms"`
	DurationRecorded     bool       `gorm:"column:duration_recorded"`
	DurationActive       bool       `gorm:"column:duration_active"`
	DurationCalculatedAt *time.Time `gorm:"column:duration_calculated_at"`
	CreatedAt            time.Time  `gorm:"column:created_at"`
	UpdatedAt            time.Time  `gorm:"column:updated_at"`
	Payload              string     `gorm:"column:payload"`
}

type scanShardRow struct {
	ID              string    `gorm:"column:id;primaryKey"`
	ScanTaskID      string    `gorm:"column:scan_task_id"`
	TargetKey       string    `gorm:"column:target_key"`
	RetryGeneration int       `gorm:"column:retry_generation"`
	ScopeID         string    `gorm:"column:scope_id"`
	ResourceKindID  string    `gorm:"column:resource_kind_id"`
	Source          string    `gorm:"column:source"`
	Authoritative   bool      `gorm:"column:authoritative"`
	Status          string    `gorm:"column:status"`
	ItemCount       int       `gorm:"column:item_count"`
	CreatedAt       time.Time `gorm:"column:created_at"`
	Payload         string    `gorm:"column:payload"`
}

type assetRow struct {
	ID             string     `gorm:"column:id;primaryKey"`
	Provider       string     `gorm:"column:provider"`
	PartitionName  string     `gorm:"column:partition_name"`
	ConnectionID   string     `gorm:"column:connection_id"`
	NativeType     string     `gorm:"column:native_type"`
	NativeID       string     `gorm:"column:native_id"`
	ScopeKey       string     `gorm:"column:scope_key"`
	ScopeID        string     `gorm:"column:scope_id"`
	ResourceKindID string     `gorm:"column:resource_kind_id"`
	FirstSeenAt    time.Time  `gorm:"column:first_seen_at"`
	LastSeenAt     time.Time  `gorm:"column:last_seen_at"`
	ClosedAt       *time.Time `gorm:"column:closed_at"`
	DeletedAt      *time.Time `gorm:"column:deleted_at"`
	Dirty          bool       `gorm:"column:dirty"`
	Payload        string     `gorm:"column:payload"`
}

type observationRow struct {
	ID          string    `gorm:"column:id;primaryKey"`
	AssetID     string    `gorm:"column:asset_id"`
	ScanTaskID  string    `gorm:"column:scan_task_id"`
	ScanShardID string    `gorm:"column:scan_shard_id"`
	ObservedAt  time.Time `gorm:"column:observed_at"`
	Source      string    `gorm:"column:source"`
	ContentHash string    `gorm:"column:content_hash"`
	Payload     string    `gorm:"column:payload"`
}

func (s *Store) PutConnection(ctx context.Context, connection asset.CloudConnection) error {
	if strings.TrimSpace(connection.Name) == "" {
		connection.Name = connection.Principal
	}
	if connection.Status == "" {
		connection.Status = asset.ConnectionActive
	}
	payload, err := encode(connection)
	if err != nil {
		return err
	}
	row := connectionRow{ID: string(connection.ID), Provider: string(connection.Provider), PartitionName: connection.Partition, CreatedAt: connection.CreatedAt, UpdatedAt: connection.UpdatedAt, DeletedAt: connection.DeletedAt, Payload: payload}
	return s.db.WithContext(ctx).Table("cloud_connections").Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "id"}},
		DoUpdates: clause.AssignmentColumns([]string{"provider", "partition_name", "updated_at", "deleted_at", "payload"}),
	}).Create(&row).Error
}

func (s *Store) PutConnectionIfUnchanged(
	ctx context.Context,
	connection asset.CloudConnection,
	expectedUpdatedAt time.Time,
) error {
	if strings.TrimSpace(connection.Name) == "" {
		connection.Name = connection.Principal
	}
	if connection.Status == "" {
		connection.Status = asset.ConnectionActive
	}
	payload, err := encode(connection)
	if err != nil {
		return err
	}
	result := s.db.WithContext(ctx).Table("cloud_connections").
		Where("id = ? AND updated_at = ? AND deleted_at IS NULL", string(connection.ID), expectedUpdatedAt).
		Updates(map[string]any{
			"provider":       string(connection.Provider),
			"partition_name": connection.Partition,
			"updated_at":     connection.UpdatedAt,
			"deleted_at":     connection.DeletedAt,
			"payload":        payload,
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return persistence.ErrConflict
	}
	return nil
}

func (s *Store) PutConnectionIfCredentialUnchanged(
	ctx context.Context,
	connection asset.CloudConnection,
	expectedUpdatedAt time.Time,
	expectedCredential asset.ConnectionCredential,
) error {
	if strings.TrimSpace(connection.Name) == "" {
		connection.Name = connection.Principal
	}
	if connection.Status == "" {
		connection.Status = asset.ConnectionActive
	}
	payload, err := encode(connection)
	if err != nil {
		return err
	}
	expectedCredentialPayload, err := encode(expectedCredential)
	if err != nil {
		return err
	}
	result := s.db.WithContext(ctx).Table("cloud_connections").
		Where(
			"id = ? AND updated_at = ? AND deleted_at IS NULL AND EXISTS ("+
				"SELECT 1 FROM connection_credentials WHERE connection_id = ? AND payload = ?"+
				")",
			string(connection.ID), expectedUpdatedAt, string(connection.ID), expectedCredentialPayload,
		).
		Updates(map[string]any{
			"provider":       string(connection.Provider),
			"partition_name": connection.Partition,
			"updated_at":     connection.UpdatedAt,
			"deleted_at":     connection.DeletedAt,
			"payload":        payload,
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return persistence.ErrConflict
	}
	return nil
}

func (s *Store) GetConnection(ctx context.Context, id asset.ConnectionID) (asset.CloudConnection, error) {
	var row connectionRow
	if err := s.db.WithContext(ctx).Table("cloud_connections").Where("id = ?", string(id)).Take(&row).Error; err != nil {
		return asset.CloudConnection{}, mapError(err)
	}
	connection, err := decode[asset.CloudConnection](row.Payload)
	if err != nil {
		return asset.CloudConnection{}, err
	}
	return normalizeConnection(connection), nil
}

func (s *Store) ListConnections(ctx context.Context, options persistence.ListOptions) (persistence.Page[asset.CloudConnection], error) {
	limit := normalizeLimit(options.Limit)
	query := s.db.WithContext(ctx).Table("cloud_connections").Where("deleted_at IS NULL")
	if provider := strings.TrimSpace(options.Provider); provider != "" {
		query = query.Where("provider = ?", provider)
	}
	if options.Cursor != "" {
		createdAt, id, err := decodeCursor(options.Cursor)
		if err != nil {
			return persistence.Page[asset.CloudConnection]{}, err
		}
		query = query.Where("created_at > ? OR (created_at = ? AND id > ?)", createdAt, createdAt, id)
	}
	var rows []connectionRow
	if err := query.Order("created_at ASC, id ASC").Limit(limit + 1).Find(&rows).Error; err != nil {
		return persistence.Page[asset.CloudConnection]{}, err
	}
	page := persistence.Page[asset.CloudConnection]{Items: make([]asset.CloudConnection, 0, min(limit, len(rows)))}
	for index, row := range rows {
		if index == limit {
			last := rows[index-1]
			page.NextCursor = encodeCursor(last.CreatedAt, last.ID)
			break
		}
		connection, err := decode[asset.CloudConnection](row.Payload)
		if err != nil {
			return persistence.Page[asset.CloudConnection]{}, err
		}
		page.Items = append(page.Items, normalizeConnection(connection))
	}
	return page, nil
}

func (s *Store) ListConnectionAggregates(ctx context.Context, options persistence.ListOptions) (persistence.Page[persistence.ConnectionListAggregate], error) {
	limit := normalizeLimit(options.Limit)
	selectedConnections := s.db.WithContext(ctx).
		Table("cloud_connections").
		Select("id, created_at, payload").
		Where("deleted_at IS NULL")
	if provider := strings.TrimSpace(options.Provider); provider != "" {
		selectedConnections = selectedConnections.Where("provider = ?", provider)
	}
	if options.Cursor != "" {
		createdAt, id, err := decodeCursor(options.Cursor)
		if err != nil {
			return persistence.Page[persistence.ConnectionListAggregate]{}, err
		}
		selectedConnections = selectedConnections.Where("created_at > ? OR (created_at = ? AND id > ?)", createdAt, createdAt, id)
	}
	selectedConnections = selectedConnections.Order("created_at ASC, id ASC").Limit(limit + 1)

	var rows []connectionAggregateRow
	err := s.db.WithContext(ctx).
		Table("(?) AS selected_connections", selectedConnections).
		Select(`
			selected_connections.id AS connection_id,
			selected_connections.payload AS connection_payload,
			connection_credentials.payload AS credential_payload,
			(
				SELECT COUNT(*)
				FROM connection_regions AS active_regions
				WHERE active_regions.connection_id = selected_connections.id
					AND active_regions.lifecycle = 'active'
			) AS active_region_count,
			(
				SELECT COUNT(*)
				FROM connection_regions AS retired_regions
				WHERE retired_regions.connection_id = selected_connections.id
					AND retired_regions.lifecycle = 'retired'
			) AS retired_region_count,
			(
				SELECT COUNT(*)
				FROM connection_regions AS excluded_regions
				WHERE excluded_regions.connection_id = selected_connections.id
					AND excluded_regions.lifecycle = 'excluded'
			) AS excluded_region_count,
			latest_region_refresh.payload AS region_refresh_payload`).
		Joins("LEFT JOIN connection_credentials ON connection_credentials.connection_id = selected_connections.id").
		Joins(`LEFT JOIN jobs AS latest_region_refresh ON latest_region_refresh.id = (
			SELECT candidate.id
			FROM jobs AS candidate
			WHERE candidate.connection_id = selected_connections.id AND candidate.job_type = ?
			ORDER BY candidate.created_at DESC, candidate.id DESC
			LIMIT 1
		)`, string(execution.JobRegionRefresh)).
		Order("selected_connections.created_at ASC, selected_connections.id ASC").
		Scan(&rows).Error
	if err != nil {
		return persistence.Page[persistence.ConnectionListAggregate]{}, err
	}

	page := persistence.Page[persistence.ConnectionListAggregate]{Items: make([]persistence.ConnectionListAggregate, 0, min(limit, len(rows)))}
	for index, row := range rows {
		if index == limit {
			last := page.Items[index-1].Connection
			page.NextCursor = encodeCursor(last.CreatedAt, string(last.ID))
			break
		}
		connection, decodeErr := decode[asset.CloudConnection](row.ConnectionPayload)
		if decodeErr != nil {
			return persistence.Page[persistence.ConnectionListAggregate]{}, decodeErr
		}
		if row.CredentialPayload == nil {
			return persistence.Page[persistence.ConnectionListAggregate]{}, fmt.Errorf("connection %q credential: %w", row.ConnectionID, persistence.ErrNotFound)
		}
		credential, decodeErr := decode[asset.ConnectionCredential](*row.CredentialPayload)
		if decodeErr != nil {
			return persistence.Page[persistence.ConnectionListAggregate]{}, decodeErr
		}
		item := persistence.ConnectionListAggregate{
			Connection:          normalizeConnection(connection),
			Credential:          credential,
			ActiveRegionCount:   row.ActiveRegionCount,
			RetiredRegionCount:  row.RetiredRegionCount,
			ExcludedRegionCount: row.ExcludedRegionCount,
		}
		if row.RegionRefreshPayload != nil {
			job, decodeErr := decode[execution.Job](*row.RegionRefreshPayload)
			if decodeErr != nil {
				return persistence.Page[persistence.ConnectionListAggregate]{}, decodeErr
			}
			item.LatestRegionRefresh = &job
		}
		page.Items = append(page.Items, item)
	}
	return page, nil
}

func normalizeConnection(connection asset.CloudConnection) asset.CloudConnection {
	if strings.TrimSpace(connection.Name) == "" {
		connection.Name = connection.Principal
	}
	if connection.Status == "" {
		connection.Status = asset.ConnectionActive
	}
	if connection.Provider == asset.ProviderAliCloud && connection.Site == "" {
		connection.Site = asset.ConnectionSiteCN
	}
	return connection
}

func (s *Store) PutCredential(ctx context.Context, credential asset.ConnectionCredential) error {
	payload, err := encode(credential)
	if err != nil {
		return err
	}
	row := credentialRow{
		ConnectionID: string(credential.ConnectionID), Provider: string(credential.Provider), Type: string(credential.Type),
		ExpiresAt: credential.ExpiresAt, CreatedAt: credential.CreatedAt, UpdatedAt: credential.UpdatedAt, Payload: payload,
	}
	return s.db.WithContext(ctx).Table("connection_credentials").Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "connection_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"provider", "credential_type", "expires_at", "updated_at", "payload"}),
	}).Create(&row).Error
}

func (s *Store) PutCredentialIfConnectionUnchanged(
	ctx context.Context,
	replacement asset.ConnectionCredential,
	expectedConnectionUpdatedAt time.Time,
	expected asset.ConnectionCredential,
) error {
	replacement.CreatedAt = expected.CreatedAt
	replacementPayload, err := encode(replacement)
	if err != nil {
		return err
	}
	expectedPayload, err := encode(expected)
	if err != nil {
		return err
	}
	result := s.db.WithContext(ctx).Table("connection_credentials").
		Where(
			"connection_id = ? AND payload = ? AND EXISTS ("+
				"SELECT 1 FROM cloud_connections WHERE id = ? AND updated_at = ? AND deleted_at IS NULL"+
				")",
			string(replacement.ConnectionID),
			expectedPayload,
			string(replacement.ConnectionID),
			expectedConnectionUpdatedAt,
		).
		Updates(map[string]any{
			"provider":        string(replacement.Provider),
			"credential_type": string(replacement.Type),
			"expires_at":      replacement.ExpiresAt,
			"updated_at":      replacement.UpdatedAt,
			"payload":         replacementPayload,
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return persistence.ErrConflict
	}
	return nil
}

func (s *Store) GetCredential(ctx context.Context, connectionID asset.ConnectionID) (asset.ConnectionCredential, error) {
	var row credentialRow
	if err := s.db.WithContext(ctx).Table("connection_credentials").Where("connection_id = ?", string(connectionID)).Take(&row).Error; err != nil {
		return asset.ConnectionCredential{}, mapError(err)
	}
	return decode[asset.ConnectionCredential](row.Payload)
}

func (s *Store) DeleteCredential(ctx context.Context, connectionID asset.ConnectionID) error {
	result := s.db.WithContext(ctx).Table("connection_credentials").Where("connection_id = ?", string(connectionID)).Delete(&credentialRow{})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return persistence.ErrNotFound
	}
	return nil
}

func (s *Store) PutRegion(ctx context.Context, region asset.ConnectionRegion) error {
	if err := normalizeRegion(&region); err != nil {
		return err
	}

	var existing regionRow
	err := s.db.WithContext(ctx).Table("connection_regions").Select("id", "revision", "connection_id", "region_id").Where("id = ?", region.ID).Take(&existing).Error
	if err == nil && (existing.ConnectionID != string(region.ConnectionID) || existing.RegionID != region.RegionID) {
		return fmt.Errorf("%w: region identity is immutable", persistence.ErrConflict)
	}
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	if err == nil {
		region.Revision = existing.Revision + 1
	} else {
		region.Revision = 1
	}

	payload, err := encode(region)
	if err != nil {
		return err
	}
	row := regionRow{
		ID: region.ID, Revision: region.Revision, ConnectionID: string(region.ConnectionID), RegionID: region.RegionID,
		Lifecycle: string(region.Lifecycle), Origin: string(region.Origin), FirstSeenAt: region.FirstSeenAt,
		LastSeenAt: region.LastSeenAt, CreatedAt: region.CreatedAt, UpdatedAt: region.UpdatedAt, Payload: payload,
	}
	return mapCreateError(upsert(s.db.WithContext(ctx), "connection_regions", row, []string{
		"revision", "lifecycle", "origin", "first_seen_at", "last_seen_at", "updated_at", "payload",
	}))
}

func (s *Store) PutRegionIfUnchanged(ctx context.Context, region asset.ConnectionRegion, expectedRevision uint64) error {
	if err := normalizeRegion(&region); err != nil {
		return err
	}
	if expectedRevision == 0 {
		return fmt.Errorf("%w: region revision is required", persistence.ErrConflict)
	}
	region.Revision = expectedRevision + 1
	payload, err := encode(region)
	if err != nil {
		return err
	}
	result := s.db.WithContext(ctx).Table("connection_regions").
		Where("id = ? AND connection_id = ? AND region_id = ? AND revision = ?", region.ID, string(region.ConnectionID), region.RegionID, expectedRevision).
		Updates(map[string]any{
			"revision": region.Revision, "lifecycle": string(region.Lifecycle), "origin": string(region.Origin),
			"first_seen_at": region.FirstSeenAt, "last_seen_at": region.LastSeenAt, "updated_at": region.UpdatedAt, "payload": payload,
		})
	if result.Error != nil {
		return mapCreateError(result.Error)
	}
	if result.RowsAffected != 1 {
		return fmt.Errorf("%w: region changed since it was read", persistence.ErrConflict)
	}
	return nil
}

func normalizeRegion(region *asset.ConnectionRegion) error {
	region.ID = strings.TrimSpace(region.ID)
	region.ConnectionID = asset.ConnectionID(strings.TrimSpace(string(region.ConnectionID)))
	region.RegionID = strings.TrimSpace(region.RegionID)
	region.DiscoveredName = strings.TrimSpace(region.DiscoveredName)
	region.NameOverride = strings.TrimSpace(region.NameOverride)
	if region.ID == "" || region.ConnectionID == "" || region.RegionID == "" {
		return fmt.Errorf("region id, connection id, and region ID are required")
	}
	return nil
}

func (s *Store) GetRegion(ctx context.Context, id string) (asset.ConnectionRegion, error) {
	var row regionRow
	if err := s.db.WithContext(ctx).Table("connection_regions").Where("id = ?", strings.TrimSpace(id)).Take(&row).Error; err != nil {
		return asset.ConnectionRegion{}, mapError(err)
	}
	region, err := decode[asset.ConnectionRegion](row.Payload)
	region.Revision = row.Revision
	return region, err
}

func (s *Store) ListRegions(ctx context.Context, options persistence.RegionListOptions) (persistence.Page[asset.ConnectionRegion], error) {
	limit := normalizeLimit(options.Limit)
	query := s.db.WithContext(ctx).Table("connection_regions")
	if options.ConnectionID != "" {
		query = query.Where("connection_id = ?", string(options.ConnectionID))
	}
	if options.Lifecycle != "" {
		query = query.Where("lifecycle = ?", string(options.Lifecycle))
	}
	if term := strings.ToLower(strings.TrimSpace(options.Query)); term != "" {
		pattern := "%" + escapeLike(term) + "%"
		query = query.Where("(LOWER(region_id) LIKE ? ESCAPE '\\' OR LOWER(payload) LIKE ? ESCAPE '\\')", pattern, pattern)
	}
	if options.Cursor != "" {
		regionID, id, err := decodeRegionCursor(options.Cursor)
		if err != nil {
			return persistence.Page[asset.ConnectionRegion]{}, err
		}
		query = query.Where("region_id > ? OR (region_id = ? AND id > ?)", regionID, regionID, id)
	}
	var rows []regionRow
	if err := query.Order("region_id ASC, id ASC").Limit(limit + 1).Find(&rows).Error; err != nil {
		return persistence.Page[asset.ConnectionRegion]{}, err
	}
	page := persistence.Page[asset.ConnectionRegion]{Items: make([]asset.ConnectionRegion, 0, min(limit, len(rows)))}
	for index, row := range rows {
		if index == limit {
			last := rows[index-1]
			page.NextCursor = encodeRegionCursor(last.RegionID, last.ID)
			break
		}
		region, err := decode[asset.ConnectionRegion](row.Payload)
		if err != nil {
			return persistence.Page[asset.ConnectionRegion]{}, err
		}
		region.Revision = row.Revision
		page.Items = append(page.Items, region)
	}
	return page, nil
}

func (s *Store) ListActiveConnectionRegions(ctx context.Context, options persistence.RegionListOptions) (persistence.Page[asset.ConnectionRegion], error) {
	if options.ConnectionID == "" {
		return persistence.Page[asset.ConnectionRegion]{}, persistence.ErrNotFound
	}
	selectedConnection := s.db.WithContext(ctx).
		Table("cloud_connections").
		Select("id, payload").
		Where("id = ? AND deleted_at IS NULL", string(options.ConnectionID))
	selectedRegions := s.db.WithContext(ctx).
		Table("connection_regions").
		Select("id, revision, region_id, created_at, payload").
		Where("connection_id = ?", string(options.ConnectionID))
	if options.Lifecycle != "" {
		selectedRegions = selectedRegions.Where("lifecycle = ?", string(options.Lifecycle))
	}
	if term := strings.ToLower(strings.TrimSpace(options.Query)); term != "" {
		pattern := "%" + escapeLike(term) + "%"
		selectedRegions = selectedRegions.Where("(LOWER(region_id) LIKE ? ESCAPE '\\' OR LOWER(payload) LIKE ? ESCAPE '\\')", pattern, pattern)
	}
	selectedRegions = selectedRegions.Order("region_id ASC, id ASC")

	var rows []activeConnectionRegionRow
	err := s.db.WithContext(ctx).
		Table("(?) AS selected_connection", selectedConnection).
		Select(`
			selected_connection.payload AS connection_payload,
			selected_regions.id AS region_id_key,
			selected_regions.revision AS region_revision,
			selected_regions.payload AS region_payload`).
		Joins("LEFT JOIN (?) AS selected_regions ON 1 = 1", selectedRegions).
		Order("selected_regions.region_id ASC, selected_regions.id ASC").
		Scan(&rows).Error
	if err != nil {
		return persistence.Page[asset.ConnectionRegion]{}, err
	}
	if len(rows) == 0 {
		return persistence.Page[asset.ConnectionRegion]{}, persistence.ErrNotFound
	}
	connection, err := decode[asset.CloudConnection](rows[0].ConnectionPayload)
	if err != nil {
		return persistence.Page[asset.ConnectionRegion]{}, err
	}
	if connection.Status != asset.ConnectionActive {
		return persistence.Page[asset.ConnectionRegion]{}, asset.ErrConnectionNotValidated
	}

	page := persistence.Page[asset.ConnectionRegion]{Items: make([]asset.ConnectionRegion, 0, len(rows))}
	for _, row := range rows {
		if row.ID == nil {
			continue
		}
		if row.Payload == nil || row.Revision == nil {
			return persistence.Page[asset.ConnectionRegion]{}, errors.New("decode repository payload: region payload is missing")
		}
		region, decodeErr := decode[asset.ConnectionRegion](*row.Payload)
		if decodeErr != nil {
			return persistence.Page[asset.ConnectionRegion]{}, decodeErr
		}
		region.Revision = *row.Revision
		page.Items = append(page.Items, region)
	}
	return page, nil
}

func (s *Store) ListRegionsByConnection(ctx context.Context, connectionID asset.ConnectionID) ([]asset.ConnectionRegion, error) {
	var rows []regionRow
	if err := s.db.WithContext(ctx).Table("connection_regions").Where("connection_id = ?", string(connectionID)).Order("region_id ASC, id ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	regions := make([]asset.ConnectionRegion, 0, len(rows))
	for _, row := range rows {
		region, err := decode[asset.ConnectionRegion](row.Payload)
		if err != nil {
			return nil, err
		}
		region.Revision = row.Revision
		regions = append(regions, region)
	}
	return regions, nil
}

func (s *Store) CountRegionsByLifecycle(ctx context.Context, connectionID asset.ConnectionID) (map[asset.RegionLifecycle]int, error) {
	type lifecycleCount struct {
		Lifecycle string `gorm:"column:lifecycle"`
		Count     int    `gorm:"column:count"`
	}
	var rows []lifecycleCount
	if err := s.db.WithContext(ctx).Table("connection_regions").Select("lifecycle, COUNT(*) AS count").Where("connection_id = ?", string(connectionID)).Group("lifecycle").Find(&rows).Error; err != nil {
		return nil, err
	}
	counts := make(map[asset.RegionLifecycle]int, len(rows))
	for _, row := range rows {
		counts[asset.RegionLifecycle(row.Lifecycle)] = row.Count
	}
	return counts, nil
}

func (s *Store) PutScope(ctx context.Context, scope asset.Scope) error {
	payload, err := encode(scope)
	if err != nil {
		return err
	}
	row := scopeRow{ID: string(scope.ID), ConnectionID: string(scope.ConnectionID), ParentID: string(scope.ParentID), Kind: string(scope.Kind), NativeID: scope.NativeID, CreatedAt: scope.CreatedAt, UpdatedAt: scope.UpdatedAt, Payload: payload}
	return mapCreateError(upsert(s.db.WithContext(ctx), "scopes", row, []string{"connection_id", "parent_id", "kind", "native_id", "updated_at", "payload"}))
}

func (s *Store) GetScope(ctx context.Context, id asset.ScopeID) (asset.Scope, error) {
	row, err := s.resolveScopeRow(ctx, id)
	if err != nil {
		return asset.Scope{}, err
	}
	return decodeScopeRow(row)
}

func (s *Store) GetScopeByNaturalKey(ctx context.Context, connectionID asset.ConnectionID, kind asset.ScopeKind, nativeID string) (asset.Scope, error) {
	var row scopeRow
	err := s.db.WithContext(ctx).Table("scopes").Where(
		"connection_id = ? AND kind = ? AND native_id = ? AND (superseded_by_scope_id IS NULL OR superseded_by_scope_id = '')",
		string(connectionID), string(kind), strings.TrimSpace(nativeID),
	).Order("created_at ASC, id ASC").Take(&row).Error
	if err != nil {
		return asset.Scope{}, mapError(err)
	}
	return decode[asset.Scope](row.Payload)
}

func (s *Store) resolveScopeRow(ctx context.Context, id asset.ScopeID) (scopeRow, error) {
	visited := make(map[string]struct{})
	for id != "" {
		if _, exists := visited[string(id)]; exists {
			return scopeRow{}, fmt.Errorf("%w: scope alias cycle at %q", persistence.ErrConflict, id)
		}
		visited[string(id)] = struct{}{}
		var row scopeRow
		if err := s.db.WithContext(ctx).Table("scopes").Where("id = ?", string(id)).Take(&row).Error; err != nil {
			return scopeRow{}, mapError(err)
		}
		if row.SupersededByID == "" {
			return row, nil
		}
		id = asset.ScopeID(row.SupersededByID)
	}
	return scopeRow{}, persistence.ErrNotFound
}

func decodeScopeRow(row scopeRow) (asset.Scope, error) {
	value, err := decode[asset.Scope](row.Payload)
	if err != nil {
		return asset.Scope{}, err
	}
	value.ID = asset.ScopeID(row.ID)
	value.ConnectionID = asset.ConnectionID(row.ConnectionID)
	value.ParentID = asset.ScopeID(row.ParentID)
	value.SupersededByID = asset.ScopeID(row.SupersededByID)
	value.Kind = asset.ScopeKind(row.Kind)
	value.NativeID = row.NativeID
	value.CreatedAt = row.CreatedAt
	value.UpdatedAt = row.UpdatedAt
	return value, nil
}

func (s *Store) scopeAliases(ctx context.Context) (map[asset.ScopeID]asset.ScopeID, error) {
	var rows []scopeRow
	if err := s.db.WithContext(ctx).Table("scopes").Select("id, superseded_by_scope_id").
		Where("superseded_by_scope_id IS NOT NULL AND superseded_by_scope_id <> ''").Find(&rows).Error; err != nil {
		return nil, err
	}
	result := make(map[asset.ScopeID]asset.ScopeID, len(rows))
	for _, row := range rows {
		result[asset.ScopeID(row.ID)] = asset.ScopeID(row.SupersededByID)
	}
	return result, nil
}

func resolveScopeAlias(id asset.ScopeID, aliases map[asset.ScopeID]asset.ScopeID) (asset.ScopeID, error) {
	if id == "" {
		return "", nil
	}
	visited := make(map[asset.ScopeID]struct{})
	for id != "" {
		canonicalID, exists := aliases[id]
		if !exists {
			return id, nil
		}
		if _, exists := visited[id]; exists {
			return "", fmt.Errorf("%w: scope alias cycle at %q", persistence.ErrConflict, id)
		}
		visited[id] = struct{}{}
		id = canonicalID
	}
	return "", nil
}

func (s *Store) ConsolidateScopes(ctx context.Context, canonicalID asset.ScopeID, duplicateIDs []asset.ScopeID, aliasParentID asset.ScopeID) error {
	if canonicalID == "" {
		return fmt.Errorf("canonical scope is required")
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var canonicalRow scopeRow
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Table("scopes").Where("id = ?", string(canonicalID)).Take(&canonicalRow).Error; err != nil {
			return mapError(err)
		}
		if canonicalRow.SupersededByID != "" {
			return fmt.Errorf("%w: canonical scope %q is already superseded", persistence.ErrConflict, canonicalID)
		}
		if aliasParentID == "" {
			aliasParentID = asset.ScopeID(canonicalRow.ParentID)
		}
		canonicalScope, err := decodeScopeRow(canonicalRow)
		if err != nil {
			return err
		}

		duplicateRows := make([]scopeRow, 0, len(duplicateIDs))
		scopeIDs := []string{string(canonicalID)}
		for _, duplicateID := range duplicateIDs {
			if duplicateID == "" || duplicateID == canonicalID {
				return fmt.Errorf("%w: duplicate scope must differ from canonical scope", persistence.ErrConflict)
			}
			aliasScope := canonicalScope
			aliasScope.ID = duplicateID
			aliasScope.ParentID = aliasParentID
			aliasScope.SupersededByID = canonicalID
			aliasPayload, err := encode(aliasScope)
			if err != nil {
				return err
			}
			placeholder := scopeRow{
				ID: string(duplicateID), ConnectionID: canonicalRow.ConnectionID, ParentID: string(aliasParentID), SupersededByID: string(canonicalID),
				Kind: canonicalRow.Kind, NativeID: canonicalRow.NativeID, CreatedAt: canonicalRow.CreatedAt, UpdatedAt: canonicalRow.UpdatedAt, Payload: aliasPayload,
			}
			if err := tx.Table("scopes").Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "id"}}, DoNothing: true}).Create(&placeholder).Error; err != nil {
				return err
			}
			var duplicateRow scopeRow
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Table("scopes").Where("id = ?", string(duplicateID)).Take(&duplicateRow).Error; err != nil {
				return mapError(err)
			}
			if duplicateRow.ConnectionID != canonicalRow.ConnectionID || duplicateRow.Kind != canonicalRow.Kind {
				return fmt.Errorf("%w: consolidated scopes must share connection and kind", persistence.ErrConflict)
			}
			duplicateScope, err := decodeScopeRow(duplicateRow)
			if err != nil {
				return err
			}
			duplicateScope.ParentID = aliasParentID
			duplicateScope.SupersededByID = canonicalID
			duplicatePayload, err := encode(duplicateScope)
			if err != nil {
				return err
			}
			if err := tx.Table("scopes").Where("id = ?", duplicateRow.ID).Updates(map[string]any{
				"parent_id": string(aliasParentID), "superseded_by_scope_id": string(canonicalID), "payload": duplicatePayload,
			}).Error; err != nil {
				return err
			}
			duplicateRow.ParentID = string(aliasParentID)
			duplicateRow.SupersededByID = string(canonicalID)
			duplicateRow.Payload = duplicatePayload
			duplicateRows = append(duplicateRows, duplicateRow)
			scopeIDs = append(scopeIDs, duplicateRow.ID)
		}

		var activeShardRows []scanShardRow
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Table("scan_shards").
			Where("scope_id IN ? AND status IN ?", scopeIDs, []string{string(asset.ShardPending), string(asset.ShardRunning)}).
			Find(&activeShardRows).Error; err != nil {
			return err
		}
		if len(activeShardRows) > 0 {
			return fmt.Errorf("%w: scope consolidation is blocked by pending or running scan shards", persistence.ErrConflict)
		}
		var activeInventoryJobs []jobRow
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Table("jobs").
			Where("connection_id = ? AND job_type IN ? AND status IN ?", canonicalRow.ConnectionID, []string{string(execution.JobScan), string(execution.JobGraph)}, []string{string(execution.JobPending), string(execution.JobRunning)}).
			Find(&activeInventoryJobs).Error; err != nil {
			return err
		}
		if len(activeInventoryJobs) > 0 {
			return fmt.Errorf("%w: scope consolidation is blocked by a pending or running scan/graph job", persistence.ErrConflict)
		}

		if err := tx.Table("relationships").Where("scope_id IN ?", scopeIDs).Delete(&relationshipRow{}).Error; err != nil {
			return err
		}
		if err := tx.Table("lifecycle_bindings").Where("scope_id IN ?", scopeIDs).Delete(&lifecycleBindingRow{}).Error; err != nil {
			return err
		}
		if err := tx.Table("graph_revisions").Where("scope_id IN ?", scopeIDs).Delete(&graphRevisionRow{}).Error; err != nil {
			return err
		}

		for _, duplicateRow := range duplicateRows {
			duplicateID := asset.ScopeID(duplicateRow.ID)
			var childRows []scopeRow
			if err := tx.Table("scopes").Where("parent_id = ?", duplicateRow.ID).Find(&childRows).Error; err != nil {
				return err
			}
			for _, row := range childRows {
				value, err := decode[asset.Scope](row.Payload)
				if err != nil {
					return err
				}
				value.ParentID = canonicalID
				payload, err := encode(value)
				if err != nil {
					return err
				}
				if err := tx.Table("scopes").Where("id = ?", row.ID).Updates(map[string]any{"parent_id": string(canonicalID), "payload": payload}).Error; err != nil {
					return err
				}
			}

			var assetRows []assetRow
			if err := tx.Table("assets").Where("scope_id = ?", string(duplicateID)).Find(&assetRows).Error; err != nil {
				return err
			}
			for _, row := range assetRows {
				value, err := decode[asset.Asset](row.Payload)
				if err != nil {
					return err
				}
				value.ClosedAt = row.ClosedAt
				value.DeletedAt = row.DeletedAt
				value.Dirty = row.Dirty
				value.Identity.ScopeKey = row.ScopeKey
				value.ScopeID = canonicalID
				payload, err := encode(value)
				if err != nil {
					return err
				}
				if err := tx.Table("assets").Where("id = ?", row.ID).Updates(map[string]any{"scope_id": string(canonicalID), "payload": payload}).Error; err != nil {
					return err
				}
			}

			var shardRows []scanShardRow
			if err := tx.Table("scan_shards").Where("scope_id = ?", string(duplicateID)).Find(&shardRows).Error; err != nil {
				return err
			}
			for _, row := range shardRows {
				value, err := decode[asset.ScanShard](row.Payload)
				if err != nil {
					return err
				}
				value.ScopeID = canonicalID
				value.Coverage.ScopeID = canonicalID
				payload, err := encode(value)
				if err != nil {
					return err
				}
				if err := tx.Table("scan_shards").Where("id = ?", row.ID).Updates(map[string]any{"scope_id": string(canonicalID), "payload": payload}).Error; err != nil {
					return err
				}
			}

		}
		return nil
	})
}

func (s *Store) ListScopes(ctx context.Context, options persistence.ListOptions) (persistence.Page[asset.Scope], error) {
	limit := normalizeLimit(options.Limit)
	query := s.db.WithContext(ctx).Table("scopes").Where("superseded_by_scope_id IS NULL OR superseded_by_scope_id = ''")
	if options.ConnectionID != "" {
		query = query.Where("connection_id = ?", string(options.ConnectionID))
	}
	if options.Cursor != "" {
		createdAt, id, err := decodeCursor(options.Cursor)
		if err != nil {
			return persistence.Page[asset.Scope]{}, err
		}
		query = query.Where("created_at > ? OR (created_at = ? AND id > ?)", createdAt, createdAt, id)
	}
	var rows []scopeRow
	if err := query.Order("created_at ASC, id ASC").Limit(limit + 1).Find(&rows).Error; err != nil {
		return persistence.Page[asset.Scope]{}, err
	}
	page := persistence.Page[asset.Scope]{Items: make([]asset.Scope, 0, min(limit, len(rows)))}
	for index, row := range rows {
		if index == limit {
			last := rows[index-1]
			page.NextCursor = encodeCursor(last.CreatedAt, last.ID)
			break
		}
		value, err := decodeScopeRow(row)
		if err != nil {
			return persistence.Page[asset.Scope]{}, err
		}
		page.Items = append(page.Items, value)
	}
	return page, nil
}

func (s *Store) ListScopesByConnection(ctx context.Context, connectionID asset.ConnectionID) ([]asset.Scope, error) {
	return s.listScopesByConnection(ctx, connectionID, false)
}

func (s *Store) ListScopesByConnectionIncludingAliases(ctx context.Context, connectionID asset.ConnectionID) ([]asset.Scope, error) {
	return s.listScopesByConnection(ctx, connectionID, true)
}

func (s *Store) listScopesByConnection(ctx context.Context, connectionID asset.ConnectionID, includeAliases bool) ([]asset.Scope, error) {
	var rows []scopeRow
	query := s.db.WithContext(ctx).Table("scopes").Where("connection_id = ?", string(connectionID))
	if !includeAliases {
		query = query.Where("superseded_by_scope_id IS NULL OR superseded_by_scope_id = ''")
	}
	if err := query.Order("kind ASC, native_id ASC, id ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	result := make([]asset.Scope, 0, len(rows))
	for _, row := range rows {
		value, err := decodeScopeRow(row)
		if err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, nil
}

func (s *Store) PutResourceKind(ctx context.Context, kind asset.ResourceKind) error {
	payload, err := encode(kind)
	if err != nil {
		return err
	}
	row := resourceKindRow{ID: string(kind.ID), Provider: string(kind.Provider), NativeType: kind.NativeType, BundleRevision: kind.BundleRevision, Payload: payload}
	return upsert(s.db.WithContext(ctx), "resource_kinds", row, []string{"provider", "native_type", "bundle_revision", "payload"})
}

func (s *Store) GetResourceKind(ctx context.Context, id asset.ResourceKindID) (asset.ResourceKind, error) {
	var row resourceKindRow
	if err := s.db.WithContext(ctx).Table("resource_kinds").Where("id = ?", string(id)).Take(&row).Error; err != nil {
		return asset.ResourceKind{}, mapError(err)
	}
	return decode[asset.ResourceKind](row.Payload)
}

func (s *Store) CreateScanRun(ctx context.Context, run asset.ScanRun) error {
	run = normalizeScanTask(run)
	payload, err := encode(run)
	if err != nil {
		return err
	}
	row := scanRunRow{
		ID: string(run.ID), ConnectionID: string(run.ConnectionID), Status: string(run.Status),
		ScopeMode: string(run.ScopeMode), RetryGeneration: run.RetryGeneration, ControlVersion: run.ControlVersion,
		CreatedAt: run.CreatedAt, UpdatedAt: scanTaskUpdatedAt(run), Payload: payload,
	}
	return mapCreateError(s.db.WithContext(ctx).Table("scan_tasks").Create(&row).Error)
}

func (s *Store) PutScanRun(ctx context.Context, run asset.ScanRun) error {
	run = normalizeScanTask(run)
	payload, err := encode(run)
	if err != nil {
		return err
	}
	row := scanRunRow{
		ID: string(run.ID), ConnectionID: string(run.ConnectionID), Status: string(run.Status),
		ScopeMode: string(run.ScopeMode), RetryGeneration: run.RetryGeneration, ControlVersion: run.ControlVersion,
		CreatedAt: run.CreatedAt, UpdatedAt: scanTaskUpdatedAt(run), Payload: payload,
	}
	db := s.db.WithContext(ctx)
	return db.Transaction(func(tx *gorm.DB) error {
		if err := upsert(tx, "scan_tasks", row, []string{"status", "scope_mode", "retry_generation", "control_version", "payload"}); err != nil {
			return err
		}
		return touchScanTask(tx, run.ID, row.UpdatedAt)
	})
}

func (s *Store) PutScanRunIfControlVersion(ctx context.Context, run asset.ScanRun, expected uint64) error {
	run = normalizeScanTask(run)
	payload, err := encode(run)
	if err != nil {
		return err
	}
	updatedAt := scanTaskUpdatedAt(run)
	result := s.db.WithContext(ctx).Table("scan_tasks").Where("id = ? AND control_version = ?", string(run.ID), expected).Updates(map[string]any{
		"status": run.Status, "scope_mode": run.ScopeMode, "retry_generation": run.RetryGeneration,
		"control_version": run.ControlVersion, "payload": payload,
		"updated_at": gorm.Expr("CASE WHEN updated_at < ? THEN ? ELSE updated_at END", updatedAt, updatedAt),
	})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return persistence.ErrConflict
	}
	return nil
}

func (s *Store) GetScanRun(ctx context.Context, id asset.ScanRunID) (asset.ScanRun, error) {
	var row scanRunRow
	if err := s.db.WithContext(ctx).Table("scan_tasks").Where("id = ?", string(id)).Take(&row).Error; err != nil {
		return asset.ScanRun{}, mapError(err)
	}
	return decode[asset.ScanRun](row.Payload)
}

func (s *Store) ListScanRuns(ctx context.Context, options persistence.ListOptions) (persistence.Page[asset.ScanRun], error) {
	limit := normalizeLimit(options.Limit)
	query := s.db.WithContext(ctx).Table("scan_tasks")
	if options.ConnectionID != "" {
		query = query.Where("connection_id = ?", string(options.ConnectionID))
	}
	if options.Cursor != "" {
		createdAt, id, err := decodeCursor(options.Cursor)
		if err != nil {
			return persistence.Page[asset.ScanRun]{}, err
		}
		query = query.Where("created_at < ? OR (created_at = ? AND id < ?)", createdAt, createdAt, id)
	}
	var rows []scanRunRow
	if err := query.Order("created_at DESC, id DESC").Limit(limit + 1).Find(&rows).Error; err != nil {
		return persistence.Page[asset.ScanRun]{}, err
	}
	return decodePage[scanRunRow, asset.ScanRun](rows, limit, func(row scanRunRow) time.Time { return row.CreatedAt }, func(row scanRunRow) string { return row.ID }, func(row scanRunRow) string { return row.Payload })
}

func (s *Store) ListScanRunListItems(ctx context.Context, options persistence.ListOptions) (persistence.Page[persistence.ScanRunListItem], error) {
	limit := normalizeLimit(options.Limit)
	query := s.db.WithContext(ctx).Table("scan_tasks")
	if options.ConnectionID != "" {
		query = query.Where("connection_id = ?", string(options.ConnectionID))
	}
	if options.Cursor != "" {
		createdAt, id, err := decodeCursor(options.Cursor)
		if err != nil {
			return persistence.Page[persistence.ScanRunListItem]{}, err
		}
		query = query.Where("created_at < ? OR (created_at = ? AND id < ?)", createdAt, createdAt, id)
	}
	var rows []scanRunRow
	if err := query.Order("created_at DESC, id DESC").Limit(limit + 1).Find(&rows).Error; err != nil {
		return persistence.Page[persistence.ScanRunListItem]{}, err
	}
	page := persistence.Page[persistence.ScanRunListItem]{Items: make([]persistence.ScanRunListItem, 0, min(limit, len(rows)))}
	for index, row := range rows {
		if index == limit {
			last := rows[index-1]
			page.NextCursor = encodeCursor(last.CreatedAt, last.ID)
			break
		}
		run, err := decode[asset.ScanRun](row.Payload)
		if err != nil {
			return persistence.Page[persistence.ScanRunListItem]{}, err
		}
		page.Items = append(page.Items, persistence.ScanRunListItem{
			ScanRun: run, ResourceCount: row.ResourceCount,
			DurationMS: row.DurationMS, DurationRecorded: row.DurationRecorded,
			DurationActive: row.DurationActive, DurationCalculatedAt: row.DurationCalculatedAt,
			UpdatedAt: row.UpdatedAt,
		})
	}
	return page, nil
}

func (s *Store) ListScanRunsByConnection(ctx context.Context, connectionID asset.ConnectionID) ([]asset.ScanRun, error) {
	var rows []scanRunRow
	if err := s.db.WithContext(ctx).Table("scan_tasks").Where("connection_id = ?", string(connectionID)).Order("created_at DESC, id ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	return decodeRows[scanRunRow, asset.ScanRun](rows, func(row scanRunRow) string { return row.Payload })
}

func (s *Store) PutScanShard(ctx context.Context, shard asset.ScanShard) error {
	shard = normalizeScanShardIdentity(shard)
	aliases, err := s.scopeAliases(ctx)
	if err != nil {
		return err
	}
	if shard.ScopeID, err = resolveScopeAlias(shard.ScopeID, aliases); err != nil {
		return err
	}
	if shard.Coverage.ScopeID != "" {
		if shard.Coverage.ScopeID, err = resolveScopeAlias(shard.Coverage.ScopeID, aliases); err != nil {
			return err
		}
	}
	payload, err := encode(shard)
	if err != nil {
		return err
	}
	row := scanShardRow{
		ID: string(shard.ID), ScanTaskID: string(shard.ScanTaskID), TargetKey: shard.TargetKey,
		RetryGeneration: shard.RetryGeneration, ScopeID: string(shard.ScopeID),
		ResourceKindID: string(shard.ResourceKindID), Source: shard.Source,
		Authoritative: shard.Authoritative, Status: string(shard.Status),
		ItemCount: shard.Coverage.ItemCount, CreatedAt: shard.CreatedAt, Payload: payload,
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var previous scanShardRow
		err := tx.Table("scan_shards").
			Select("id", "item_count").
			Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ?", row.ID).
			Take(&previous).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if err := upsert(tx, "scan_shards", row, []string{
			"target_key", "retry_generation", "scope_id", "resource_kind_id",
			"status", "authoritative", "item_count", "payload",
		}); err != nil {
			return err
		}
		delta := row.ItemCount - previous.ItemCount
		if delta == 0 {
			return nil
		}
		result := tx.Table("scan_tasks").
			Where("id = ?", row.ScanTaskID).
			UpdateColumn("resource_count", gorm.Expr("resource_count + ?", delta))
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return persistence.ErrNotFound
		}
		return nil
	})
}

func (s *Store) GetScanShard(ctx context.Context, id asset.ScanShardID) (asset.ScanShard, error) {
	var row scanShardRow
	if err := s.db.WithContext(ctx).Table("scan_shards").Where("id = ?", string(id)).Take(&row).Error; err != nil {
		return asset.ScanShard{}, mapError(err)
	}
	value, err := decode[asset.ScanShard](row.Payload)
	if err != nil {
		return asset.ScanShard{}, err
	}
	aliases, err := s.scopeAliases(ctx)
	if err != nil {
		return asset.ScanShard{}, err
	}
	return canonicalizeScanShard(value, aliases)
}

func (s *Store) ListScanShards(ctx context.Context, options persistence.ListOptions) (persistence.Page[asset.ScanShard], error) {
	limit := normalizeLimit(options.Limit)
	query := s.db.WithContext(ctx).Table("scan_shards")
	if options.ConnectionID != "" {
		runs := s.db.WithContext(ctx).Table("scan_tasks").Select("id").Where("connection_id = ?", string(options.ConnectionID))
		query = query.Where("scan_task_id IN (?)", runs)
	}
	if options.Cursor != "" {
		createdAt, id, err := decodeCursor(options.Cursor)
		if err != nil {
			return persistence.Page[asset.ScanShard]{}, err
		}
		query = query.Where("created_at > ? OR (created_at = ? AND id > ?)", createdAt, createdAt, id)
	}
	var rows []scanShardRow
	if err := query.Order("created_at ASC, id ASC").Limit(limit + 1).Find(&rows).Error; err != nil {
		return persistence.Page[asset.ScanShard]{}, err
	}
	aliases, err := s.scopeAliases(ctx)
	if err != nil {
		return persistence.Page[asset.ScanShard]{}, err
	}
	page := persistence.Page[asset.ScanShard]{Items: make([]asset.ScanShard, 0, min(limit, len(rows)))}
	for index, row := range rows {
		if index == limit {
			last := rows[index-1]
			page.NextCursor = encodeCursor(last.CreatedAt, last.ID)
			break
		}
		value, err := decode[asset.ScanShard](row.Payload)
		if err != nil {
			return persistence.Page[asset.ScanShard]{}, err
		}
		value, err = canonicalizeScanShard(value, aliases)
		if err != nil {
			return persistence.Page[asset.ScanShard]{}, err
		}
		page.Items = append(page.Items, value)
	}
	return page, nil
}

func (s *Store) ListScanShardsByRun(ctx context.Context, runID asset.ScanRunID) ([]asset.ScanShard, error) {
	if runID == "" {
		return nil, persistence.ErrNotFound
	}
	var rows []scanShardRow
	if err := s.db.WithContext(ctx).Table("scan_shards").Where("scan_task_id = ?", string(runID)).Order("created_at ASC, id ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	aliases, err := s.scopeAliases(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]asset.ScanShard, 0, len(rows))
	for _, row := range rows {
		value, err := decode[asset.ScanShard](row.Payload)
		if err != nil {
			return nil, err
		}
		value, err = canonicalizeScanShard(value, aliases)
		if err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, nil
}

func canonicalizeScanShard(value asset.ScanShard, aliases map[asset.ScopeID]asset.ScopeID) (asset.ScanShard, error) {
	value = normalizeScanShardIdentity(value)
	var err error
	value.ScopeID, err = resolveScopeAlias(value.ScopeID, aliases)
	if err != nil {
		return asset.ScanShard{}, err
	}
	if value.Coverage.ScopeID != "" {
		value.Coverage.ScopeID, err = resolveScopeAlias(value.Coverage.ScopeID, aliases)
		if err != nil {
			return asset.ScanShard{}, err
		}
	}
	return value, nil
}

func normalizeScanShardIdentity(value asset.ScanShard) asset.ScanShard {
	if value.ScanTaskID == "" {
		value.ScanTaskID = value.ScanRunID
	}
	if value.ScanRunID == "" {
		value.ScanRunID = value.ScanTaskID
	}
	if strings.TrimSpace(value.TargetKey) == "" {
		if value.RegionID == "global" {
			value.TargetKey = "global"
		} else if value.RegionID != "" {
			value.TargetKey = "region:" + value.RegionID
		} else {
			value.TargetKey = "scope:" + string(value.ScopeID)
		}
	}
	return value
}

func normalizeScanTask(value asset.ScanTask) asset.ScanTask {
	if value.ScopeMode == "" {
		value.ScopeMode = asset.ScanSelectedRegions
	}
	return value
}

func scanTaskUpdatedAt(value asset.ScanTask) time.Time {
	updatedAt := value.CreatedAt
	for _, candidate := range []*time.Time{
		value.StartedAt,
		value.FinishedAt,
		value.PausedAt,
		value.CanceledAt,
	} {
		if candidate != nil && candidate.After(updatedAt) {
			updatedAt = *candidate
		}
	}
	return updatedAt
}

func touchScanTask(db *gorm.DB, id asset.ScanTaskID, updatedAt time.Time) error {
	result := db.Table("scan_tasks").
		Where("id = ?", string(id)).
		UpdateColumn("updated_at", gorm.Expr("CASE WHEN updated_at < ? THEN ? ELSE updated_at END", updatedAt, updatedAt))
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return persistence.ErrNotFound
	}
	return nil
}

func (s *Store) PutAsset(ctx context.Context, value asset.Asset) error {
	aliases, err := s.scopeAliases(ctx)
	if err != nil {
		return err
	}
	if value.ScopeID, err = resolveScopeAlias(value.ScopeID, aliases); err != nil {
		return err
	}
	value.Identity.ScopeKey = strings.TrimSpace(value.Identity.ScopeKey)
	payload, err := encode(value)
	if err != nil {
		return err
	}
	row := assetRow{
		ID: string(value.ID), Provider: string(value.Identity.Provider), PartitionName: value.Identity.Partition,
		ConnectionID: string(value.Identity.ConnectionID), NativeType: value.Identity.NativeType, NativeID: value.Identity.NativeID,
		ScopeKey: value.Identity.ScopeKey, ScopeID: string(value.ScopeID), ResourceKindID: string(value.ResourceKindID), FirstSeenAt: value.FirstSeenAt,
		LastSeenAt: value.LastSeenAt, ClosedAt: value.ClosedAt, DeletedAt: value.DeletedAt, Dirty: value.Dirty, Payload: payload,
	}
	return upsert(s.db.WithContext(ctx), "assets", row, []string{"scope_id", "resource_kind_id", "last_seen_at", "closed_at", "deleted_at", "payload"})
}

func (s *Store) SetAssetDirty(ctx context.Context, id asset.AssetID, dirty bool) (asset.Asset, error) {
	result := s.db.WithContext(ctx).
		Table("assets").
		Where("id = ?", string(id)).
		Update("dirty", dirty)
	if result.Error != nil {
		return asset.Asset{}, result.Error
	}
	if result.RowsAffected != 1 {
		return asset.Asset{}, persistence.ErrNotFound
	}
	return s.GetAsset(ctx, id)
}

func (s *Store) GetAsset(ctx context.Context, id asset.AssetID) (asset.Asset, error) {
	var row assetRow
	if err := s.db.WithContext(ctx).Table("assets").Where("id = ?", string(id)).Take(&row).Error; err != nil {
		return asset.Asset{}, mapError(err)
	}
	value, err := decode[asset.Asset](row.Payload)
	if err != nil {
		return asset.Asset{}, err
	}
	value.Dirty = row.Dirty
	value.ClosedAt = row.ClosedAt
	value.DeletedAt = row.DeletedAt
	value.Identity.ScopeKey = row.ScopeKey
	aliases, err := s.scopeAliases(ctx)
	if err != nil {
		return asset.Asset{}, err
	}
	value.ScopeID, err = resolveScopeAlias(value.ScopeID, aliases)
	return value, err
}

func (s *Store) GetAssetByIdentity(ctx context.Context, identity asset.Identity) (asset.Asset, error) {
	var row assetRow
	err := s.db.WithContext(ctx).Table("assets").Where(
		"provider = ? AND partition_name = ? AND connection_id = ? AND native_type = ? AND native_id = ? AND scope_key = ?",
		string(identity.Provider), identity.Partition, string(identity.ConnectionID), identity.NativeType, identity.NativeID,
		strings.TrimSpace(identity.ScopeKey),
	).Take(&row).Error
	if err != nil {
		return asset.Asset{}, mapError(err)
	}
	value, err := decode[asset.Asset](row.Payload)
	if err != nil {
		return asset.Asset{}, err
	}
	value.Dirty = row.Dirty
	value.ClosedAt = row.ClosedAt
	value.DeletedAt = row.DeletedAt
	value.Identity.ScopeKey = row.ScopeKey
	return value, nil
}

func (s *Store) ListAssetsByIDs(ctx context.Context, assetIDs []asset.AssetID) ([]asset.Asset, error) {
	if len(assetIDs) == 0 {
		return []asset.Asset{}, nil
	}
	const batchSize = 400
	result := make([]asset.Asset, 0, len(assetIDs))
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
		var rows []assetRow
		if err := s.db.WithContext(ctx).
			Table("assets").
			Where("id IN ?", ids).
			Order("id ASC").
			Find(&rows).Error; err != nil {
			return nil, err
		}
		values, err := s.decodeAssetRows(ctx, rows)
		if err != nil {
			return nil, err
		}
		result = append(result, values...)
	}
	return result, nil
}

func (s *Store) ListAssets(ctx context.Context, options persistence.ListOptions) (persistence.Page[asset.Asset], error) {
	limit := normalizeLimit(options.Limit)
	query := s.db.WithContext(ctx).Table("assets").Select("assets.*")
	if !options.IncludeClosed {
		query = query.Where("assets.closed_at IS NULL")
	}
	if options.ConnectionID != "" {
		query = query.Where("assets.connection_id = ?", string(options.ConnectionID))
	}
	if len(options.AssetIDs) > 0 {
		assetIDs := make([]string, 0, len(options.AssetIDs))
		seen := make(map[asset.AssetID]struct{}, len(options.AssetIDs))
		for _, assetID := range options.AssetIDs {
			assetID = asset.AssetID(strings.TrimSpace(string(assetID)))
			if assetID == "" {
				continue
			}
			if _, exists := seen[assetID]; exists {
				continue
			}
			seen[assetID] = struct{}{}
			assetIDs = append(assetIDs, string(assetID))
		}
		if len(assetIDs) > 0 {
			query = query.Where("assets.id IN ?", assetIDs)
		}
	}
	if provider := strings.TrimSpace(options.Provider); provider != "" {
		query = query.Where("assets.provider = ?", provider)
	}
	if term := strings.ToLower(strings.TrimSpace(options.Query)); term != "" {
		pattern := "%" + escapeLike(term) + "%"
		query = query.Where(
			"(LOWER(assets.native_id) LIKE ? ESCAPE '\\' OR LOWER(assets.native_type) LIKE ? ESCAPE '\\' OR LOWER(assets.payload) LIKE ? ESCAPE '\\')",
			pattern, pattern, pattern,
		)
	}
	if options.ResourceQuery != nil {
		where, arguments, err := options.ResourceQuery.SQL(s.db.Dialector.Name())
		if err != nil {
			return persistence.Page[asset.Asset]{}, err
		}
		if where != "" {
			query = query.Where(where, arguments...)
		}
	}
	if len(options.NativeIDs) > 0 {
		nativeIDs := make([]string, 0, len(options.NativeIDs))
		seen := make(map[string]struct{}, len(options.NativeIDs))
		for _, nativeID := range options.NativeIDs {
			nativeID = strings.TrimSpace(nativeID)
			if nativeID == "" {
				continue
			}
			if _, exists := seen[nativeID]; exists {
				continue
			}
			seen[nativeID] = struct{}{}
			nativeIDs = append(nativeIDs, nativeID)
		}
		if len(nativeIDs) > 0 {
			query = query.Where("assets.native_id IN ?", nativeIDs)
		}
	}
	if capability := strings.ToLower(strings.TrimSpace(options.Capability)); capability != "" {
		query = query.Where("LOWER(assets.payload) LIKE ?", "%\""+capability+"\"%")
	}
	resourceKindIDs := options.ResourceKindIDs
	if len(resourceKindIDs) == 0 && options.ResourceKindID != "" {
		resourceKindIDs = []asset.ResourceKindID{options.ResourceKindID}
	}
	if len(resourceKindIDs) > 0 {
		kindValues := make([]string, 0, len(resourceKindIDs))
		for _, kindID := range resourceKindIDs {
			if value := strings.TrimSpace(string(kindID)); value != "" {
				kindValues = append(kindValues, value)
			}
		}
		if len(kindValues) > 0 {
			query = query.Where(
				"assets.resource_kind_id IN ?",
				kindValues,
			)
		}
	}
	var err error
	query, err = s.filterAssetsByCanvas(ctx, query, options)
	if err != nil {
		return persistence.Page[asset.Asset]{}, err
	}
	if options.SearchOrder {
		query = query.Joins("LEFT JOIN resource_kinds ON resource_kinds.id = assets.resource_kind_id")
	}
	if options.Cursor != "" {
		createdAt, id, err := decodeCursor(options.Cursor)
		if err != nil {
			return persistence.Page[asset.Asset]{}, err
		}
		query = query.Where("assets.first_seen_at > ? OR (assets.first_seen_at = ? AND assets.id > ?)", createdAt, createdAt, id)
	}
	var rows []assetRow
	if options.SearchOrder {
		query = orderPanoramaAssetSearch(query, options.Query)
	} else {
		query = query.Order("assets.first_seen_at ASC, assets.id ASC")
	}
	if err := query.Limit(limit + 1).Find(&rows).Error; err != nil {
		return persistence.Page[asset.Asset]{}, err
	}
	page := persistence.Page[asset.Asset]{Items: make([]asset.Asset, 0, min(limit, len(rows)))}
	for index, row := range rows {
		if index == limit {
			if !options.SearchOrder {
				last := rows[index-1]
				page.NextCursor = encodeCursor(last.FirstSeenAt, last.ID)
			}
			break
		}
		value, err := decode[asset.Asset](row.Payload)
		if err != nil {
			return persistence.Page[asset.Asset]{}, err
		}
		value.Dirty = row.Dirty
		value.ClosedAt = row.ClosedAt
		value.DeletedAt = row.DeletedAt
		value.Identity.ScopeKey = row.ScopeKey
		value.ScopeID = asset.ScopeID(row.ScopeID)
		page.Items = append(page.Items, value)
	}
	return page, nil
}

func (s *Store) filterAssetsByCanvas(ctx context.Context, query *gorm.DB, options persistence.ListOptions) (*gorm.DB, error) {
	if options.AssetCanvas == "" || options.AssetCanvas == persistence.AssetCanvasAccount {
		return query, nil
	}

	canonicalScopes := s.db.WithContext(ctx).
		Table("scopes").
		Select("id").
		Where("superseded_by_scope_id IS NULL OR superseded_by_scope_id = ''")
	if options.ConnectionID != "" {
		canonicalScopes = canonicalScopes.Where("connection_id = ?", string(options.ConnectionID))
	}

	switch options.AssetCanvas {
	case persistence.AssetCanvasGlobal:
		globalScopeIDs := s.assetScopeAuthorityIDs(ctx, options, asset.ScopeGlobal)
		query = query.Where(
			"(assets.scope_id IN (?) OR assets.scope_id IS NULL OR assets.scope_id = '' OR assets.scope_id NOT IN (?))",
			globalScopeIDs, canonicalScopes,
		)
	case persistence.AssetCanvasRegion, persistence.AssetCanvasRegionPublic, persistence.AssetCanvasVPC:
		regionScopeIDs := s.assetScopeAuthorityIDs(ctx, options, asset.ScopeRegion)
		query = query.Where("assets.scope_id IN (?)", regionScopeIDs)
	}

	needsKinds := options.AssetCanvas == persistence.AssetCanvasRegionPublic || options.AssetCanvas == persistence.AssetCanvasVPC
	if needsKinds && !options.SearchOrder {
		query = query.Joins("LEFT JOIN resource_kinds ON resource_kinds.id = assets.resource_kind_id")
	}
	switch options.AssetCanvas {
	case persistence.AssetCanvasRegionPublic:
		vpcField := "%" + escapeLike(`"vpc_id":"`) + "%"
		emptyVPC := "%" + escapeLike(`"vpc_id":""`) + "%"
		query = query.Where(
			"NOT ("+assetKindClassSQL("network.vpc")+") AND (LOWER(assets.payload) NOT LIKE ? ESCAPE '\\' OR LOWER(assets.payload) LIKE ? ESCAPE '\\')",
			vpcField, emptyVPC,
		)
	case persistence.AssetCanvasVPC:
		encodedVPC, err := json.Marshal(strings.ToLower(strings.TrimSpace(options.VPCID)))
		if err != nil {
			return nil, err
		}
		vpcPattern := "%" + escapeLike(`"vpc_id":`+string(encodedVPC)) + "%"
		query = query.Where(
			"(LOWER(assets.payload) LIKE ? ESCAPE '\\' OR (("+assetKindClassSQL("network.vpc")+") AND LOWER(assets.native_id) = ?))",
			vpcPattern, strings.ToLower(strings.TrimSpace(options.VPCID)),
		)
	}
	return query, nil
}

func (s *Store) assetScopeAuthorityIDs(ctx context.Context, options persistence.ListOptions, authorityKind asset.ScopeKind) *gorm.DB {
	connectionID := strings.TrimSpace(string(options.ConnectionID))
	query := `
		WITH RECURSIVE scope_ancestry (
			descendant_id, current_id, parent_id, kind, native_id, payload, depth
		) AS (
			SELECT id, id, parent_id, kind, native_id, payload, 0
			FROM scopes
			WHERE (superseded_by_scope_id IS NULL OR superseded_by_scope_id = '')
				AND (? = '' OR connection_id = ?)
			UNION ALL
			SELECT
				scope_ancestry.descendant_id,
				parent.id,
				parent.parent_id,
				parent.kind,
				parent.native_id,
				parent.payload,
				scope_ancestry.depth + 1
			FROM scope_ancestry
			JOIN scopes AS parent
				ON parent.id = scope_ancestry.parent_id
				AND (parent.superseded_by_scope_id IS NULL OR parent.superseded_by_scope_id = '')
			WHERE scope_ancestry.kind NOT IN (?, ?) AND scope_ancestry.depth < 64
		)`
	arguments := []any{
		connectionID,
		connectionID,
		string(asset.ScopeRegion),
		string(asset.ScopeGlobal),
	}
	if authorityKind == asset.ScopeGlobal {
		query += `
			SELECT descendant_id
			FROM scope_ancestry
			GROUP BY descendant_id
			HAVING
				MAX(CASE WHEN kind = ? THEN 1 ELSE 0 END) = 1
				OR MAX(CASE WHEN kind IN (?, ?) THEN 1 ELSE 0 END) = 0`
		arguments = append(arguments, string(asset.ScopeGlobal), string(asset.ScopeRegion), string(asset.ScopeGlobal))
	} else {
		query += `
			SELECT descendant_id
			FROM scope_ancestry
			WHERE kind = ?`
		arguments = append(arguments, string(authorityKind))
		regionID := strings.ToLower(strings.TrimSpace(options.RegionID))
		encodedRegionID, _ := json.Marshal(regionID)
		query += ` AND (
			LOWER(native_id) = ?
			OR (native_id = '' AND LOWER(payload) LIKE ? ESCAPE '\')
		)`
		arguments = append(arguments, regionID, "%"+escapeLike(`"location":`+string(encodedRegionID))+"%")
	}
	return s.db.WithContext(ctx).Raw(query, arguments...)
}

func orderPanoramaAssetSearch(query *gorm.DB, rawTerm string) *gorm.DB {
	term := strings.ToLower(strings.TrimSpace(rawTerm))
	encodedTerm, _ := json.Marshal(term)
	exactName := "%" + escapeLike(`"name":`+string(encodedTerm)) + "%"
	prefixName := "%" + escapeLike(`"name":`+strings.TrimSuffix(string(encodedTerm), `"`)) + "%"
	containsName := "%" + escapeLike(`"name":"`) + "%" + escapeLike(strings.Trim(string(encodedTerm), `"`)) + "%"
	prefix := escapeLike(term) + "%"
	contains := "%" + escapeLike(term) + "%"
	order := clause.Expr{
		SQL: `CASE
			WHEN ` + assetKindClassSQL("network.vpc") + ` OR LOWER(assets.native_type) LIKE '%::vpc' THEN 0
			WHEN ` + assetKindClassSQL("network.subnet") + ` OR LOWER(assets.native_type) LIKE '%::vswitch' THEN 1
			WHEN ` + assetKindClassSQL("compute.instance") + ` OR (LOWER(assets.native_type) LIKE '%::instance' AND LOWER(assets.native_type) LIKE '%::ecs::%') THEN 2
			WHEN ` + assetKindClassSQL("network.security_group") + ` OR LOWER(assets.native_type) LIKE '%::securitygroup' THEN 3
			WHEN ` + assetKindClassSQL("storage.block") + ` OR LOWER(assets.native_type) LIKE '%::disk' THEN 4
			WHEN ` + assetKindClassSQL("storage.bucket") + ` OR LOWER(assets.native_type) LIKE '%::bucket' THEN 5
			ELSE 6
		END ASC,
		CASE
			WHEN LOWER(assets.native_id) = ? THEN 0
			WHEN LOWER(assets.payload) LIKE ? ESCAPE '\' THEN 1
			WHEN LOWER(assets.native_id) LIKE ? ESCAPE '\' THEN 2
			WHEN LOWER(assets.payload) LIKE ? ESCAPE '\' THEN 3
			WHEN LOWER(assets.native_id) LIKE ? ESCAPE '\' THEN 4
			WHEN LOWER(assets.payload) LIKE ? ESCAPE '\' THEN 5
			ELSE 6
		END ASC,
		LOWER(assets.native_id) ASC,
		assets.id ASC`,
		Vars: []any{term, exactName, prefix, prefixName, contains, containsName},
	}
	return query.Order(clause.OrderBy{Expression: order})
}

func assetKindClassSQL(class string) string {
	return "LOWER(resource_kinds.payload) LIKE '%\"class\":\"" + class + "\"%'"
}

func (s *Store) AppendObservation(ctx context.Context, observation asset.Observation) error {
	if err := observation.Validate(); err != nil {
		return err
	}
	payload, err := encode(observation)
	if err != nil {
		return err
	}
	row := observationRow{ID: string(observation.ID), AssetID: string(observation.AssetID), ScanTaskID: string(observation.ScanRunID), ScanShardID: string(observation.ScanShardID), ObservedAt: observation.ObservedAt, Source: observation.Source, ContentHash: observation.ContentHash, Payload: payload}
	return mapCreateError(s.db.WithContext(ctx).Table("asset_observations").Create(&row).Error)
}

func (s *Store) ListObservations(ctx context.Context, assetID asset.AssetID) ([]asset.Observation, error) {
	var rows []observationRow
	if err := s.db.WithContext(ctx).Table("asset_observations").Where("asset_id = ?", string(assetID)).Order("observed_at ASC, id ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	return decodeRows[observationRow, asset.Observation](rows, func(row observationRow) string { return row.Payload })
}

func (s *Store) ListActiveAssets(ctx context.Context, scopeID asset.ScopeID, kindID asset.ResourceKindID) ([]asset.Asset, error) {
	query := s.db.WithContext(ctx).Table("assets").Where("closed_at IS NULL")
	if scopeID != "" {
		aliases, err := s.scopeAliases(ctx)
		if err != nil {
			return nil, err
		}
		scopeID, err = resolveScopeAlias(scopeID, aliases)
		if err != nil {
			return nil, err
		}
		scopeIDs := []string{string(scopeID)}
		for aliasID, canonicalID := range aliases {
			resolved, err := resolveScopeAlias(canonicalID, aliases)
			if err != nil {
				return nil, err
			}
			if resolved == scopeID {
				scopeIDs = append(scopeIDs, string(aliasID))
			}
		}
		query = query.Where("scope_id IN ?", scopeIDs)
	}
	if kindID != "" {
		query = query.Where("resource_kind_id = ?", string(kindID))
	}
	var rows []assetRow
	if err := query.Order("id ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	return s.decodeAssetRows(ctx, rows)
}

func (s *Store) ListActiveAssetsByScopes(
	ctx context.Context,
	connectionID asset.ConnectionID,
	scopeIDs []asset.ScopeID,
	kindID asset.ResourceKindID,
) ([]asset.Asset, error) {
	if len(scopeIDs) == 0 {
		return []asset.Asset{}, nil
	}
	aliases, err := s.scopeAliases(ctx)
	if err != nil {
		return nil, err
	}
	allowed := make(map[asset.ScopeID]struct{}, len(scopeIDs))
	for _, scopeID := range scopeIDs {
		resolved, resolveErr := resolveScopeAlias(scopeID, aliases)
		if resolveErr != nil {
			return nil, resolveErr
		}
		allowed[resolved] = struct{}{}
	}
	values := make([]string, 0, len(allowed)+len(aliases))
	for scopeID := range allowed {
		values = append(values, string(scopeID))
	}
	for aliasID, canonicalID := range aliases {
		resolved, resolveErr := resolveScopeAlias(canonicalID, aliases)
		if resolveErr != nil {
			return nil, resolveErr
		}
		if _, ok := allowed[resolved]; ok {
			values = append(values, string(aliasID))
		}
	}
	query := s.db.WithContext(ctx).
		Table("assets").
		Where(
			"closed_at IS NULL AND connection_id = ? AND scope_id IN ?",
			string(connectionID),
			values,
		)
	if kindID != "" {
		query = query.Where("resource_kind_id = ?", string(kindID))
	}
	var rows []assetRow
	if err := query.Order("id ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	return s.decodeAssetRows(ctx, rows)
}

func (s *Store) ListActiveAssetsByConnection(ctx context.Context, connectionID asset.ConnectionID, kindID asset.ResourceKindID) ([]asset.Asset, error) {
	query := s.db.WithContext(ctx).Table("assets").Where("closed_at IS NULL AND connection_id = ?", string(connectionID))
	if kindID != "" {
		query = query.Where("resource_kind_id = ?", string(kindID))
	}
	var rows []assetRow
	if err := query.Order("id ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	return s.decodeAssetRows(ctx, rows)
}

func (s *Store) CountActiveAssetsByScope(
	ctx context.Context,
	connectionID asset.ConnectionID,
	kindIDs []asset.ResourceKindID,
) (map[asset.ScopeID]int, error) {
	var rows []struct {
		ScopeID string `gorm:"column:scope_id"`
		Count   int    `gorm:"column:asset_count"`
	}
	query := s.db.WithContext(ctx).
		Table("assets").
		Select("scope_id, COUNT(*) AS asset_count").
		Where("closed_at IS NULL AND connection_id = ?", string(connectionID))
	if len(kindIDs) > 0 {
		seen := make(map[string]struct{}, len(kindIDs))
		values := make([]string, 0, len(kindIDs))
		for _, kindID := range kindIDs {
			value := strings.TrimSpace(string(kindID))
			if value == "" {
				continue
			}
			if _, exists := seen[value]; exists {
				continue
			}
			seen[value] = struct{}{}
			values = append(values, value)
		}
		sort.Strings(values)
		if len(values) == 0 {
			return map[asset.ScopeID]int{}, nil
		}
		query = query.Where("resource_kind_id IN ?", values)
	}
	if err := query.
		Group("scope_id").
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	aliases, err := s.scopeAliases(ctx)
	if err != nil {
		return nil, err
	}
	counts := make(map[asset.ScopeID]int, len(rows))
	for _, row := range rows {
		scopeID, resolveErr := resolveScopeAlias(asset.ScopeID(row.ScopeID), aliases)
		if resolveErr != nil {
			return nil, resolveErr
		}
		counts[scopeID] += row.Count
	}
	return counts, nil
}

func (s *Store) decodeAssetRows(ctx context.Context, rows []assetRow) ([]asset.Asset, error) {
	aliases, err := s.scopeAliases(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]asset.Asset, 0, len(rows))
	for _, row := range rows {
		value, err := decode[asset.Asset](row.Payload)
		if err != nil {
			return nil, err
		}
		value.Dirty = row.Dirty
		value.ClosedAt = row.ClosedAt
		value.DeletedAt = row.DeletedAt
		value.Identity.ScopeKey = row.ScopeKey
		value.ScopeID, err = resolveScopeAlias(value.ScopeID, aliases)
		if err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, nil
}

func (s *Store) ListAssetIDsObservedByShard(ctx context.Context, shardID asset.ScanShardID) ([]asset.AssetID, error) {
	var ids []string
	if err := s.db.WithContext(ctx).Table("asset_observations").Distinct("asset_id").Where("scan_shard_id = ?", string(shardID)).Order("asset_id ASC").Pluck("asset_id", &ids).Error; err != nil {
		return nil, err
	}
	result := make([]asset.AssetID, len(ids))
	for index, id := range ids {
		result[index] = asset.AssetID(id)
	}
	return result, nil
}

func (s *Store) ListAssetIDsObservedByTarget(ctx context.Context, connectionID asset.ConnectionID, targetKey, source string, scopeID asset.ScopeID, kindID asset.ResourceKindID) ([]asset.AssetID, error) {
	query := s.db.WithContext(ctx).Table("asset_observations AS observations").
		Select("DISTINCT observations.asset_id").
		Joins("JOIN scan_shards AS shards ON shards.id = observations.scan_shard_id").
		Joins("JOIN scan_tasks AS tasks ON tasks.id = shards.scan_task_id").
		Where("tasks.connection_id = ? AND shards.target_key = ? AND shards.source = ? AND shards.scope_id = ?", string(connectionID), targetKey, source, string(scopeID))
	if kindID == "" {
		query = query.Where("shards.resource_kind_id = ''")
	} else {
		query = query.Where("shards.resource_kind_id = ?", string(kindID))
	}
	var ids []string
	if err := query.Order("observations.asset_id ASC").Pluck("observations.asset_id", &ids).Error; err != nil {
		return nil, err
	}
	result := make([]asset.AssetID, len(ids))
	for index, id := range ids {
		result[index] = asset.AssetID(id)
	}
	return result, nil
}

func encode[T any](value T) (string, error) {
	payload, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("encode repository payload: %w", err)
	}
	return string(payload), nil
}

func decode[T any](payload string) (T, error) {
	var value T
	if err := json.Unmarshal([]byte(payload), &value); err != nil {
		return value, fmt.Errorf("decode repository payload: %w", err)
	}
	return value, nil
}

func decodeRows[R any, T any](rows []R, payload func(R) string) ([]T, error) {
	values := make([]T, 0, len(rows))
	for _, row := range rows {
		value, err := decode[T](payload(row))
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, nil
}

func decodePage[R any, T any](rows []R, limit int, timestamp func(R) time.Time, id func(R) string, payload func(R) string) (persistence.Page[T], error) {
	page := persistence.Page[T]{Items: make([]T, 0, min(limit, len(rows)))}
	for index, row := range rows {
		if index == limit {
			last := rows[index-1]
			page.NextCursor = encodeCursor(timestamp(last), id(last))
			break
		}
		value, err := decode[T](payload(row))
		if err != nil {
			return persistence.Page[T]{}, err
		}
		page.Items = append(page.Items, value)
	}
	return page, nil
}

func upsert(db *gorm.DB, table string, value any, columns []string) error {
	return db.Table(table).Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "id"}}, DoUpdates: clause.AssignmentColumns(columns)}).Create(value).Error
}

func mapError(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return persistence.ErrNotFound
	}
	return err
}

func mapCreateError(err error) error {
	if err == nil {
		return nil
	}
	message := strings.ToLower(err.Error())
	if errors.Is(err, gorm.ErrDuplicatedKey) || strings.Contains(message, "unique constraint") || strings.Contains(message, "duplicate key") {
		return fmt.Errorf("%w: %v", persistence.ErrConflict, err)
	}
	return err
}

func normalizeLimit(limit int) int {
	if limit <= 0 {
		return 100
	}
	if limit > 500 {
		return 500
	}
	return limit
}

func encodeCursor(createdAt time.Time, id string) string {
	value := strconv.FormatInt(createdAt.UTC().UnixNano(), 10) + "\x00" + id
	return base64.RawURLEncoding.EncodeToString([]byte(value))
}

func decodeCursor(cursor string) (time.Time, string, error) {
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return time.Time{}, "", fmt.Errorf("%w: decode cursor: %v", persistence.ErrInvalidCursor, err)
	}
	parts := strings.SplitN(string(raw), "\x00", 2)
	if len(parts) != 2 || parts[1] == "" {
		return time.Time{}, "", fmt.Errorf("%w: decode cursor: invalid value", persistence.ErrInvalidCursor)
	}
	nanoseconds, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return time.Time{}, "", fmt.Errorf("%w: decode cursor time: %v", persistence.ErrInvalidCursor, err)
	}
	return time.Unix(0, nanoseconds).UTC(), parts[1], nil
}

func encodeRegionCursor(regionID, id string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(regionID + "\x00" + id))
}

func decodeRegionCursor(cursor string) (string, string, error) {
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return "", "", fmt.Errorf("%w: decode region cursor: %v", persistence.ErrInvalidCursor, err)
	}
	parts := strings.SplitN(string(raw), "\x00", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("%w: decode region cursor: invalid value", persistence.ErrInvalidCursor)
	}
	return parts[0], parts[1], nil
}

func escapeLike(value string) string {
	value = strings.ReplaceAll(value, "\\", "\\\\")
	value = strings.ReplaceAll(value, "%", "\\%")
	return strings.ReplaceAll(value, "_", "\\_")
}

var _ persistence.Repositories = (*Store)(nil)
var _ persistence.ConnectionRepository = (*Store)(nil)
var _ persistence.CredentialRepository = (*Store)(nil)
var _ persistence.RegionRepository = (*Store)(nil)
var _ persistence.InventoryRepository = (*Store)(nil)
var _ persistence.GraphRepository = (*Store)(nil)
var _ persistence.FindingRepository = (*Store)(nil)
var _ persistence.CleanupTaskRepository = (*Store)(nil)
var _ persistence.ExecutionRepository = (*Store)(nil)
var _ persistence.AuditRepository = (*Store)(nil)
var _ persistence.JobRepository = (*Store)(nil)
