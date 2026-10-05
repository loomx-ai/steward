-- +goose Up

-- Every tenant row belongs to one workspace. A self-hosted server keeps one
-- workspace, 'default'; servers in a shared pool serve many. resource_kinds is
-- the provider catalog and stays global. Row IDs are random and globally
-- unique, so joins on them stay inside one workspace; only workspace-wide
-- reads need workspace_id in front of their index.
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

-- Writers must name the workspace; a missing one is a bug, not 'default'.
ALTER TABLE action_attempts ALTER COLUMN workspace_id DROP DEFAULT;
ALTER TABLE asset_changes ALTER COLUMN workspace_id DROP DEFAULT;
ALTER TABLE asset_observations ALTER COLUMN workspace_id DROP DEFAULT;
ALTER TABLE assets ALTER COLUMN workspace_id DROP DEFAULT;
ALTER TABLE audit_events ALTER COLUMN workspace_id DROP DEFAULT;
ALTER TABLE cleanup_task_rows ALTER COLUMN workspace_id DROP DEFAULT;
ALTER TABLE cleanup_tasks ALTER COLUMN workspace_id DROP DEFAULT;
ALTER TABLE cloud_connections ALTER COLUMN workspace_id DROP DEFAULT;
ALTER TABLE connection_credentials ALTER COLUMN workspace_id DROP DEFAULT;
ALTER TABLE connection_regions ALTER COLUMN workspace_id DROP DEFAULT;
ALTER TABLE execution_attempts ALTER COLUMN workspace_id DROP DEFAULT;
ALTER TABLE findings ALTER COLUMN workspace_id DROP DEFAULT;
ALTER TABLE graph_revisions ALTER COLUMN workspace_id DROP DEFAULT;
ALTER TABLE inventory_versions ALTER COLUMN workspace_id DROP DEFAULT;
ALTER TABLE job_logs ALTER COLUMN workspace_id DROP DEFAULT;
ALTER TABLE jobs ALTER COLUMN workspace_id DROP DEFAULT;
ALTER TABLE lifecycle_bindings ALTER COLUMN workspace_id DROP DEFAULT;
ALTER TABLE outbox_events ALTER COLUMN workspace_id DROP DEFAULT;
ALTER TABLE relationships ALTER COLUMN workspace_id DROP DEFAULT;
ALTER TABLE scan_schedule_runs ALTER COLUMN workspace_id DROP DEFAULT;
ALTER TABLE scan_schedules ALTER COLUMN workspace_id DROP DEFAULT;
ALTER TABLE scan_shards ALTER COLUMN workspace_id DROP DEFAULT;
ALTER TABLE scan_tasks ALTER COLUMN workspace_id DROP DEFAULT;
ALTER TABLE scopes ALTER COLUMN workspace_id DROP DEFAULT;
ALTER TABLE workspace_settings ALTER COLUMN workspace_id DROP DEFAULT;

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

-- Settings and client idempotency keys are per workspace.
ALTER TABLE workspace_settings DROP CONSTRAINT workspace_settings_pkey;
ALTER TABLE workspace_settings ADD PRIMARY KEY (workspace_id, setting_key);
ALTER TABLE execution_attempts DROP CONSTRAINT execution_attempts_idempotency_key_key;
CREATE UNIQUE INDEX uq_execution_attempts_idempotency ON execution_attempts (workspace_id, idempotency_key);
ALTER TABLE jobs DROP CONSTRAINT jobs_idempotency_key_key;
CREATE UNIQUE INDEX uq_jobs_idempotency ON jobs (workspace_id, idempotency_key);

-- +goose Down

DROP INDEX uq_jobs_idempotency;
ALTER TABLE jobs ADD CONSTRAINT jobs_idempotency_key_key UNIQUE (idempotency_key);
DROP INDEX uq_execution_attempts_idempotency;
ALTER TABLE execution_attempts ADD CONSTRAINT execution_attempts_idempotency_key_key UNIQUE (idempotency_key);
ALTER TABLE workspace_settings DROP CONSTRAINT workspace_settings_pkey;
ALTER TABLE workspace_settings ADD PRIMARY KEY (setting_key);
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
ALTER TABLE workspace_settings DROP COLUMN workspace_id;
