-- +goose Up

-- Every tenant row belongs to one workspace. A self-hosted server keeps one
-- workspace, 'default'; servers in a shared pool serve many. resource_kinds is
-- the provider catalog and stays global. Row IDs are random and globally
-- unique, so joins on them stay inside one workspace; only workspace-wide
-- reads need workspace_id in front of their index.
-- SQLite only ever serves one workspace, so its other unique keys stay global
-- and workspace_id keeps its default.
ALTER TABLE action_attempts ADD COLUMN workspace_id VARCHAR(128) NOT NULL DEFAULT 'default';
ALTER TABLE asset_changes ADD COLUMN workspace_id VARCHAR(128) NOT NULL DEFAULT 'default';
ALTER TABLE asset_observations ADD COLUMN workspace_id VARCHAR(128) NOT NULL DEFAULT 'default';
ALTER TABLE assets ADD COLUMN workspace_id VARCHAR(128) NOT NULL DEFAULT 'default';
ALTER TABLE audit_events ADD COLUMN workspace_id VARCHAR(128) NOT NULL DEFAULT 'default';
ALTER TABLE cleanup_task_rows ADD COLUMN workspace_id VARCHAR(128) NOT NULL DEFAULT 'default';
ALTER TABLE cleanup_tasks ADD COLUMN workspace_id VARCHAR(128) NOT NULL DEFAULT 'default';
ALTER TABLE cloud_connections ADD COLUMN workspace_id VARCHAR(128) NOT NULL DEFAULT 'default';
ALTER TABLE connection_credentials ADD COLUMN workspace_id VARCHAR(128) NOT NULL DEFAULT 'default';
ALTER TABLE connection_regions ADD COLUMN workspace_id VARCHAR(128) NOT NULL DEFAULT 'default';
ALTER TABLE execution_attempts ADD COLUMN workspace_id VARCHAR(128) NOT NULL DEFAULT 'default';
ALTER TABLE findings ADD COLUMN workspace_id VARCHAR(128) NOT NULL DEFAULT 'default';
ALTER TABLE graph_revisions ADD COLUMN workspace_id VARCHAR(128) NOT NULL DEFAULT 'default';
ALTER TABLE inventory_versions ADD COLUMN workspace_id VARCHAR(128) NOT NULL DEFAULT 'default';
ALTER TABLE job_logs ADD COLUMN workspace_id VARCHAR(128) NOT NULL DEFAULT 'default';
ALTER TABLE jobs ADD COLUMN workspace_id VARCHAR(128) NOT NULL DEFAULT 'default';
ALTER TABLE lifecycle_bindings ADD COLUMN workspace_id VARCHAR(128) NOT NULL DEFAULT 'default';
ALTER TABLE outbox_events ADD COLUMN workspace_id VARCHAR(128) NOT NULL DEFAULT 'default';
ALTER TABLE relationships ADD COLUMN workspace_id VARCHAR(128) NOT NULL DEFAULT 'default';
ALTER TABLE scan_schedule_runs ADD COLUMN workspace_id VARCHAR(128) NOT NULL DEFAULT 'default';
ALTER TABLE scan_schedules ADD COLUMN workspace_id VARCHAR(128) NOT NULL DEFAULT 'default';
ALTER TABLE scan_shards ADD COLUMN workspace_id VARCHAR(128) NOT NULL DEFAULT 'default';
ALTER TABLE scan_tasks ADD COLUMN workspace_id VARCHAR(128) NOT NULL DEFAULT 'default';
ALTER TABLE scopes ADD COLUMN workspace_id VARCHAR(128) NOT NULL DEFAULT 'default';
ALTER TABLE workspace_settings ADD COLUMN workspace_id VARCHAR(128) NOT NULL DEFAULT 'default';

DROP INDEX IF EXISTS idx_assets_list_cursor;
CREATE INDEX idx_assets_list_cursor ON assets (workspace_id, closed_at, first_seen_at, id);
DROP INDEX IF EXISTS idx_audit_events_cursor;
CREATE INDEX idx_audit_events_cursor ON audit_events (workspace_id, created_at, id);
DROP INDEX IF EXISTS idx_connections_active_provider;
CREATE INDEX idx_connections_active_provider ON cloud_connections (workspace_id, deleted_at, provider, created_at, id);
DROP INDEX IF EXISTS idx_findings_cursor;
CREATE INDEX idx_findings_cursor ON findings (workspace_id, last_seen_at, id);
DROP INDEX IF EXISTS idx_scopes_superseded_by;
CREATE INDEX idx_scopes_superseded_by ON scopes (workspace_id, superseded_by_scope_id, id);

CREATE TABLE workspace_settings_new (
    workspace_id VARCHAR(128) NOT NULL DEFAULT 'default',
    setting_key VARCHAR(128) NOT NULL,
    updated_at TIMESTAMP NOT NULL,
    payload TEXT NOT NULL,
    PRIMARY KEY (workspace_id, setting_key)
);
INSERT INTO workspace_settings_new (workspace_id, setting_key, updated_at, payload)
    SELECT workspace_id, setting_key, updated_at, payload FROM workspace_settings;
DROP TABLE workspace_settings;
ALTER TABLE workspace_settings_new RENAME TO workspace_settings;

-- +goose Down

CREATE TABLE workspace_settings_old (
    setting_key VARCHAR(128) PRIMARY KEY,
    updated_at TIMESTAMP NOT NULL,
    payload TEXT NOT NULL
);
INSERT INTO workspace_settings_old (setting_key, updated_at, payload)
    SELECT setting_key, updated_at, payload FROM workspace_settings;
DROP TABLE workspace_settings;
ALTER TABLE workspace_settings_old RENAME TO workspace_settings;
DROP INDEX IF EXISTS idx_assets_list_cursor;
CREATE INDEX idx_assets_list_cursor ON assets (closed_at, first_seen_at, id);
DROP INDEX IF EXISTS idx_audit_events_cursor;
CREATE INDEX idx_audit_events_cursor ON audit_events (created_at, id);
DROP INDEX IF EXISTS idx_connections_active_provider;
CREATE INDEX idx_connections_active_provider ON cloud_connections (deleted_at, provider, created_at, id);
DROP INDEX IF EXISTS idx_findings_cursor;
CREATE INDEX idx_findings_cursor ON findings (last_seen_at, id);
DROP INDEX IF EXISTS idx_scopes_superseded_by;
CREATE INDEX idx_scopes_superseded_by ON scopes (superseded_by_scope_id, id);
ALTER TABLE action_attempts DROP COLUMN workspace_id;
ALTER TABLE asset_changes DROP COLUMN workspace_id;
ALTER TABLE asset_observations DROP COLUMN workspace_id;
ALTER TABLE assets DROP COLUMN workspace_id;
ALTER TABLE audit_events DROP COLUMN workspace_id;
ALTER TABLE cleanup_task_rows DROP COLUMN workspace_id;
ALTER TABLE cleanup_tasks DROP COLUMN workspace_id;
ALTER TABLE cloud_connections DROP COLUMN workspace_id;
ALTER TABLE connection_credentials DROP COLUMN workspace_id;
ALTER TABLE connection_regions DROP COLUMN workspace_id;
ALTER TABLE execution_attempts DROP COLUMN workspace_id;
ALTER TABLE findings DROP COLUMN workspace_id;
ALTER TABLE graph_revisions DROP COLUMN workspace_id;
ALTER TABLE inventory_versions DROP COLUMN workspace_id;
ALTER TABLE job_logs DROP COLUMN workspace_id;
ALTER TABLE jobs DROP COLUMN workspace_id;
ALTER TABLE lifecycle_bindings DROP COLUMN workspace_id;
ALTER TABLE outbox_events DROP COLUMN workspace_id;
ALTER TABLE relationships DROP COLUMN workspace_id;
ALTER TABLE scan_schedule_runs DROP COLUMN workspace_id;
ALTER TABLE scan_schedules DROP COLUMN workspace_id;
ALTER TABLE scan_shards DROP COLUMN workspace_id;
ALTER TABLE scan_tasks DROP COLUMN workspace_id;
ALTER TABLE scopes DROP COLUMN workspace_id;
