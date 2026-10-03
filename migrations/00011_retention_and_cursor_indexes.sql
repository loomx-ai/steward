-- +goose Up

CREATE INDEX idx_asset_observations_scan_task ON asset_observations (scan_task_id);
CREATE INDEX idx_job_logs_aggregate_cursor ON job_logs (aggregate_type, aggregate_id, created_at, id);
CREATE INDEX idx_asset_changes_scan_cursor ON asset_changes (scan_task_id, changed_at, id);
CREATE INDEX idx_assets_connection_scope
    ON assets (connection_id, closed_at, scope_id);

-- +goose Down

DROP INDEX IF EXISTS idx_asset_observations_scan_task;
DROP INDEX IF EXISTS idx_job_logs_aggregate_cursor;
DROP INDEX IF EXISTS idx_asset_changes_scan_cursor;
DROP INDEX IF EXISTS idx_assets_connection_scope;
