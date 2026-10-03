package relational

import (
	"context"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/schedule"
	"github.com/loomx-ai/steward/internal/persistence"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type scheduleRow struct {
	ID           string     `gorm:"column:id;primaryKey"`
	ConnectionID string     `gorm:"column:connection_id"`
	Enabled      bool       `gorm:"column:enabled"`
	NextRunAt    *time.Time `gorm:"column:next_run_at"`
	Revision     uint64     `gorm:"column:revision"`
	CreatedAt    time.Time  `gorm:"column:created_at"`
	UpdatedAt    time.Time  `gorm:"column:updated_at"`
	Payload      string     `gorm:"column:payload"`
}

type scheduleRunRow struct {
	ID           string    `gorm:"column:id;primaryKey"`
	ScheduleID   string    `gorm:"column:schedule_id"`
	ConnectionID string    `gorm:"column:connection_id"`
	PlannedAt    time.Time `gorm:"column:planned_at"`
	ScanTaskID   *string   `gorm:"column:scan_task_id"`
	Settled      bool      `gorm:"column:settled"`
	CreatedAt    time.Time `gorm:"column:created_at"`
	Payload      string    `gorm:"column:payload"`
}

type settingRow struct {
	Key       string    `gorm:"column:setting_key;primaryKey"`
	UpdatedAt time.Time `gorm:"column:updated_at"`
	Payload   string    `gorm:"column:payload"`
}

var blockingScanStatuses = []string{
	string(asset.ScanPending), string(asset.ScanRunning), string(asset.ScanPausing),
	string(asset.ScanReconciling), string(asset.ScanCanceling),
}

var terminalScanStatuses = []string{
	string(asset.ScanSucceeded), string(asset.ScanPartial), string(asset.ScanFailed), string(asset.ScanCanceled),
}

func newScheduleRow(value schedule.ScanSchedule) (scheduleRow, error) {
	payload, err := encode(value)
	if err != nil {
		return scheduleRow{}, err
	}
	return scheduleRow{
		ID: string(value.ID), ConnectionID: string(value.ConnectionID), Enabled: value.Enabled, NextRunAt: value.NextRunAt,
		Revision: value.Revision, CreatedAt: value.CreatedAt, UpdatedAt: value.UpdatedAt, Payload: payload,
	}, nil
}

func decodeSchedule(row scheduleRow) (schedule.ScanSchedule, error) {
	value, err := decode[schedule.ScanSchedule](row.Payload)
	value.Revision = row.Revision
	return value, err
}

func (s *Store) CreateSchedule(ctx context.Context, value schedule.ScanSchedule) error {
	value.Revision = 1
	row, err := newScheduleRow(value)
	if err != nil {
		return err
	}
	return mapCreateError(s.db.WithContext(ctx).Table("scan_schedules").Create(&row).Error)
}

func (s *Store) GetSchedule(ctx context.Context, id schedule.ID) (schedule.ScanSchedule, error) {
	var row scheduleRow
	if err := s.db.WithContext(ctx).Table("scan_schedules").Where("id = ?", string(id)).Take(&row).Error; err != nil {
		return schedule.ScanSchedule{}, mapError(err)
	}
	return decodeSchedule(row)
}

func (s *Store) UpdateSchedule(ctx context.Context, value schedule.ScanSchedule, expectedRevision uint64) (schedule.ScanSchedule, error) {
	value.Revision = expectedRevision + 1
	row, err := newScheduleRow(value)
	if err != nil {
		return schedule.ScanSchedule{}, err
	}
	result := s.db.WithContext(ctx).Table("scan_schedules").
		Where("id = ? AND revision = ?", row.ID, expectedRevision).
		Updates(map[string]any{
			"enabled": row.Enabled, "next_run_at": row.NextRunAt, "revision": row.Revision,
			"updated_at": row.UpdatedAt, "payload": row.Payload,
		})
	if result.Error != nil {
		return schedule.ScanSchedule{}, result.Error
	}
	if result.RowsAffected != 1 {
		return schedule.ScanSchedule{}, persistence.ErrConflict
	}
	return value, nil
}

func (s *Store) DeleteSchedule(ctx context.Context, id schedule.ID) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Table("scan_schedules").Where("id = ?", string(id)).Delete(nil)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return persistence.ErrNotFound
		}
		return tx.Table("scan_schedule_runs").Where("schedule_id = ?", string(id)).Delete(nil).Error
	})
}

func (s *Store) ListSchedules(ctx context.Context, connectionID asset.ConnectionID) ([]schedule.ScanSchedule, error) {
	query := s.db.WithContext(ctx).Table("scan_schedules")
	if connectionID != "" {
		query = query.Where("connection_id = ?", string(connectionID))
	}
	var rows []scheduleRow
	if err := query.Order("created_at ASC, id ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	return decodeScheduleRows(rows)
}

func (s *Store) ListDueSchedules(ctx context.Context, now time.Time, limit int) ([]schedule.ScanSchedule, error) {
	var rows []scheduleRow
	if err := s.db.WithContext(ctx).Table("scan_schedules").
		Where("enabled = ? AND next_run_at IS NOT NULL AND next_run_at <= ?", true, now).
		Order("next_run_at ASC, id ASC").Limit(normalizeLimit(limit)).Find(&rows).Error; err != nil {
		return nil, err
	}
	return decodeScheduleRows(rows)
}

func decodeScheduleRows(rows []scheduleRow) ([]schedule.ScanSchedule, error) {
	result := make([]schedule.ScanSchedule, 0, len(rows))
	for _, row := range rows {
		value, err := decodeSchedule(row)
		if err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, nil
}

func newScheduleRunRow(run schedule.Run) (scheduleRunRow, error) {
	payload, err := encode(run)
	if err != nil {
		return scheduleRunRow{}, err
	}
	return scheduleRunRow{
		ID: string(run.ID), ScheduleID: string(run.ScheduleID), ConnectionID: string(run.ConnectionID), PlannedAt: run.PlannedAt,
		ScanTaskID: optionalString(string(run.ScanTaskID)), Settled: run.Settled, CreatedAt: run.CreatedAt, Payload: payload,
	}, nil
}

func (s *Store) CreateRun(ctx context.Context, run schedule.Run) error {
	row, err := newScheduleRunRow(run)
	if err != nil {
		return err
	}
	return mapCreateError(s.db.WithContext(ctx).Table("scan_schedule_runs").Create(&row).Error)
}

func (s *Store) UpdateRun(ctx context.Context, run schedule.Run) error {
	row, err := newScheduleRunRow(run)
	if err != nil {
		return err
	}
	result := s.db.WithContext(ctx).Table("scan_schedule_runs").Where("id = ?", row.ID).Updates(map[string]any{
		"scan_task_id": row.ScanTaskID, "settled": row.Settled, "payload": row.Payload,
	})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return persistence.ErrNotFound
	}
	return nil
}

func (s *Store) GetRun(ctx context.Context, id schedule.RunID) (schedule.Run, error) {
	var row scheduleRunRow
	if err := s.db.WithContext(ctx).Table("scan_schedule_runs").Where("id = ?", string(id)).Take(&row).Error; err != nil {
		return schedule.Run{}, mapError(err)
	}
	return decode[schedule.Run](row.Payload)
}

func (s *Store) ListRuns(ctx context.Context, scheduleID schedule.ID, options persistence.ListOptions) (persistence.Page[schedule.Run], error) {
	limit := normalizeLimit(options.Limit)
	query := s.db.WithContext(ctx).Table("scan_schedule_runs").Where("schedule_id = ?", string(scheduleID))
	if options.Cursor != "" {
		createdAt, id, err := decodeCursor(options.Cursor)
		if err != nil {
			return persistence.Page[schedule.Run]{}, err
		}
		query = query.Where("created_at < ? OR (created_at = ? AND id < ?)", createdAt, createdAt, id)
	}
	var rows []scheduleRunRow
	if err := query.Order("created_at DESC, id DESC").Limit(limit + 1).Find(&rows).Error; err != nil {
		return persistence.Page[schedule.Run]{}, err
	}
	return decodePage[scheduleRunRow, schedule.Run](rows, limit, func(row scheduleRunRow) time.Time { return row.CreatedAt }, func(row scheduleRunRow) string { return row.ID }, func(row scheduleRunRow) string { return row.Payload })
}

func (s *Store) ListUnsettledRuns(ctx context.Context, limit int) ([]schedule.Run, error) {
	var rows []scheduleRunRow
	if err := s.db.WithContext(ctx).Table("scan_schedule_runs").Where("settled = ?", false).
		Order("created_at ASC, id ASC").Limit(normalizeLimit(limit)).Find(&rows).Error; err != nil {
		return nil, err
	}
	return decodeRows[scheduleRunRow, schedule.Run](rows, func(row scheduleRunRow) string { return row.Payload })
}

func (s *Store) LatestRuns(ctx context.Context, scheduleIDs []schedule.ID) (map[schedule.ID]schedule.Run, error) {
	result := make(map[schedule.ID]schedule.Run, len(scheduleIDs))
	if len(scheduleIDs) == 0 {
		return result, nil
	}
	ids := make([]string, len(scheduleIDs))
	for index, id := range scheduleIDs {
		ids[index] = string(id)
	}
	var rows []scheduleRunRow
	if err := s.db.WithContext(ctx).Table("scan_schedule_runs AS runs").
		Where("runs.schedule_id IN ?", ids).
		Where(`NOT EXISTS (
			SELECT 1 FROM scan_schedule_runs AS newer
			WHERE newer.schedule_id = runs.schedule_id
			  AND (newer.created_at > runs.created_at OR (newer.created_at = runs.created_at AND newer.id > runs.id))
		)`).
		Find(&rows).Error; err != nil {
		return nil, err
	}
	for _, row := range rows {
		run, err := decode[schedule.Run](row.Payload)
		if err != nil {
			return nil, err
		}
		result[schedule.ID(row.ScheduleID)] = run
	}
	return result, nil
}

// DeleteRunsBefore removes run records that no longer point at a kept scan:
// skipped times, scans that could not start and scans already deleted.
func (s *Store) DeleteRunsBefore(ctx context.Context, before time.Time) error {
	return s.db.WithContext(ctx).Table("scan_schedule_runs").
		Where("created_at < ? AND settled = ?", before, true).
		Where("scan_task_id IS NULL OR NOT EXISTS (SELECT 1 FROM scan_tasks WHERE scan_tasks.id = scan_schedule_runs.scan_task_id)").
		Delete(nil).Error
}

func (s *Store) FindBlockingScan(ctx context.Context, connectionID asset.ConnectionID) (asset.ScanRun, error) {
	var row scanRunRow
	if err := s.db.WithContext(ctx).Table("scan_tasks").
		Where("connection_id = ? AND status IN ?", string(connectionID), blockingScanStatuses).
		Order("created_at DESC, id DESC").Take(&row).Error; err != nil {
		return asset.ScanRun{}, mapError(err)
	}
	return decode[asset.ScanRun](row.Payload)
}

func (s *Store) ListScanSummaries(ctx context.Context, ids []asset.ScanTaskID) ([]persistence.ScanRunListItem, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	values := make([]string, len(ids))
	for index, id := range ids {
		values[index] = string(id)
	}
	var rows []scanRunRow
	if err := s.db.WithContext(ctx).Table("scan_tasks").Where("id IN ?", values).Find(&rows).Error; err != nil {
		return nil, err
	}
	result := make([]persistence.ScanRunListItem, 0, len(rows))
	for _, row := range rows {
		run, err := decode[asset.ScanRun](row.Payload)
		if err != nil {
			return nil, err
		}
		result = append(result, persistence.ScanRunListItem{
			ScanRun: run, ResourceCount: row.ResourceCount, DurationMS: row.DurationMS, DurationRecorded: row.DurationRecorded,
			DurationActive: row.DurationActive, DurationCalculatedAt: row.DurationCalculatedAt, UpdatedAt: row.UpdatedAt,
		})
	}
	return result, nil
}

func (s *Store) LatestCompleteScan(ctx context.Context, connectionID asset.ConnectionID) (asset.ScanRun, error) {
	var rows []scanRunRow
	if err := s.db.WithContext(ctx).Table("scan_tasks").
		Where("connection_id = ? AND scope_mode = ? AND status = ?", string(connectionID), string(asset.ScanAllActiveRegions), string(asset.ScanSucceeded)).
		Order("created_at DESC, id DESC").Limit(50).Find(&rows).Error; err != nil {
		return asset.ScanRun{}, err
	}
	for _, row := range rows {
		run, err := decode[asset.ScanRun](row.Payload)
		if err != nil {
			return asset.ScanRun{}, err
		}
		if len(run.ResourceKindIDs) == 0 {
			return run, nil
		}
	}
	return asset.ScanRun{}, persistence.ErrNotFound
}

func (s *Store) ListExpiredScheduledScans(ctx context.Context, before time.Time, limit int) ([]asset.ScanTaskID, error) {
	var ids []string
	if err := s.db.WithContext(ctx).Table("scan_tasks AS tasks").
		Where("tasks.schedule_id IS NOT NULL AND tasks.created_at < ? AND tasks.status IN ?", before, terminalScanStatuses).
		Where(`NOT (tasks.status = ? AND NOT EXISTS (
			SELECT 1 FROM scan_tasks AS newer
			WHERE newer.schedule_id = tasks.schedule_id AND newer.status = ?
			  AND (newer.created_at > tasks.created_at OR (newer.created_at = tasks.created_at AND newer.id > tasks.id))
		))`, string(asset.ScanSucceeded), string(asset.ScanSucceeded)).
		Order("tasks.created_at ASC, tasks.id ASC").Limit(normalizeLimit(limit)).
		Pluck("tasks.id", &ids).Error; err != nil {
		return nil, err
	}
	result := make([]asset.ScanTaskID, len(ids))
	for index, id := range ids {
		result[index] = asset.ScanTaskID(id)
	}
	return result, nil
}

func (s *Store) DeleteScan(ctx context.Context, id asset.ScanTaskID) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row scanRunRow
		if err := tx.Table("scan_tasks").Where("id = ?", string(id)).Take(&row).Error; err != nil {
			return mapError(err)
		}
		if !asset.ScanStatus(row.Status).Terminal() {
			return persistence.ErrConflict
		}
		// An observation stays while it is still the newest projection of its
		// asset; deleting it would leave the asset without its source.
		if err := tx.Exec(`DELETE FROM asset_observations
			WHERE scan_task_id = ?
			  AND EXISTS (
			    SELECT 1 FROM assets
			    WHERE assets.id = asset_observations.asset_id
			      AND assets.last_seen_at > asset_observations.observed_at
			  )`, string(id)).Error; err != nil {
			return err
		}
		for _, statement := range []struct {
			table string
			where string
		}{
			{"job_logs", "aggregate_type = 'scan_task' AND aggregate_id = ?"},
			{"jobs", "aggregate_type = 'scan_task' AND aggregate_id = ?"},
			{"asset_changes", "scan_task_id = ?"},
			{"scan_shards", "scan_task_id = ?"},
			{"scan_schedule_runs", "scan_task_id = ?"},
			{"scan_tasks", "id = ?"},
		} {
			if err := tx.Table(statement.table).Where(statement.where, string(id)).Delete(nil).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Store) GetSetting(ctx context.Context, key string) (string, error) {
	var row settingRow
	if err := s.db.WithContext(ctx).Table("workspace_settings").Where("setting_key = ?", key).Take(&row).Error; err != nil {
		return "", mapError(err)
	}
	return row.Payload, nil
}

func (s *Store) PutSetting(ctx context.Context, key, payload string, updatedAt time.Time) error {
	row := settingRow{Key: key, UpdatedAt: updatedAt, Payload: payload}
	return s.db.WithContext(ctx).Table("workspace_settings").Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "setting_key"}},
		DoUpdates: clause.AssignmentColumns([]string{"updated_at", "payload"}),
	}).Create(&row).Error
}

var _ persistence.ScheduleRepository = (*Store)(nil)
