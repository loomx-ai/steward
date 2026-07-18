-- +goose Up

CREATE TABLE cloud_connections (
    id VARCHAR(128) PRIMARY KEY,
    provider VARCHAR(64) NOT NULL,
    partition_name VARCHAR(128) NOT NULL,
    created_at TIMESTAMP NOT NULL,
    updated_at TIMESTAMP NOT NULL,
    deleted_at TIMESTAMP,
    payload TEXT NOT NULL
);

CREATE TABLE connection_credentials (
    connection_id VARCHAR(128) PRIMARY KEY,
    provider VARCHAR(64) NOT NULL,
    credential_type VARCHAR(64) NOT NULL,
    expires_at TIMESTAMP,
    created_at TIMESTAMP NOT NULL,
    updated_at TIMESTAMP NOT NULL,
    payload TEXT NOT NULL
);

CREATE TABLE connection_regions (
    id VARCHAR(128) PRIMARY KEY,
    revision BIGINT NOT NULL DEFAULT 1,
    connection_id VARCHAR(128) NOT NULL,
    region_id VARCHAR(128) NOT NULL,
    lifecycle VARCHAR(32) NOT NULL,
    origin VARCHAR(32) NOT NULL,
    first_seen_at TIMESTAMP,
    last_seen_at TIMESTAMP,
    created_at TIMESTAMP NOT NULL,
    updated_at TIMESTAMP NOT NULL,
    payload TEXT NOT NULL,
    CONSTRAINT uq_connection_regions_connection_region UNIQUE (connection_id, region_id)
);

CREATE TABLE scopes (
    id VARCHAR(128) PRIMARY KEY,
    connection_id VARCHAR(128) NOT NULL,
    parent_id VARCHAR(128),
    superseded_by_scope_id VARCHAR(128),
    kind VARCHAR(64) NOT NULL,
    native_id VARCHAR(512) NOT NULL,
    created_at TIMESTAMP NOT NULL,
    updated_at TIMESTAMP NOT NULL,
    payload TEXT NOT NULL
);

CREATE TABLE resource_kinds (
    id VARCHAR(256) PRIMARY KEY,
    provider VARCHAR(64) NOT NULL,
    native_type VARCHAR(512) NOT NULL,
    bundle_revision VARCHAR(128) NOT NULL,
    payload TEXT NOT NULL,
    CONSTRAINT uq_resource_kinds_provider_type UNIQUE (provider, native_type)
);

CREATE TABLE scan_tasks (
    id VARCHAR(128) PRIMARY KEY,
    connection_id VARCHAR(128) NOT NULL,
    status VARCHAR(32) NOT NULL,
    scope_mode VARCHAR(32) NOT NULL,
    retry_generation INTEGER NOT NULL DEFAULT 0,
    control_version BIGINT NOT NULL DEFAULT 0,
    created_at TIMESTAMP NOT NULL,
    payload TEXT NOT NULL
);

CREATE TABLE scan_shards (
    id VARCHAR(128) PRIMARY KEY,
    scan_task_id VARCHAR(128) NOT NULL,
    target_key VARCHAR(1024) NOT NULL,
    retry_generation INTEGER NOT NULL DEFAULT 0,
    scope_id VARCHAR(128) NOT NULL,
    resource_kind_id VARCHAR(256),
    source VARCHAR(128) NOT NULL,
    authoritative BOOLEAN NOT NULL DEFAULT FALSE,
    status VARCHAR(32) NOT NULL,
    created_at TIMESTAMP NOT NULL,
    payload TEXT NOT NULL
);

CREATE TABLE assets (
    id VARCHAR(128) PRIMARY KEY,
    provider VARCHAR(64) NOT NULL,
    partition_name VARCHAR(128) NOT NULL,
    connection_id VARCHAR(128) NOT NULL,
    native_type VARCHAR(512) NOT NULL,
    native_id VARCHAR(1024) NOT NULL,
    scope_id VARCHAR(128),
    resource_kind_id VARCHAR(256) NOT NULL,
    first_seen_at TIMESTAMP NOT NULL,
    last_seen_at TIMESTAMP NOT NULL,
    closed_at TIMESTAMP,
    payload TEXT NOT NULL,
    CONSTRAINT uq_assets_natural_identity UNIQUE (provider, partition_name, connection_id, native_type, native_id)
);

CREATE TABLE asset_observations (
    id VARCHAR(128) PRIMARY KEY,
    asset_id VARCHAR(128) NOT NULL,
    scan_task_id VARCHAR(128) NOT NULL,
    scan_shard_id VARCHAR(128) NOT NULL,
    observed_at TIMESTAMP NOT NULL,
    source VARCHAR(128) NOT NULL,
    content_hash VARCHAR(128) NOT NULL,
    payload TEXT NOT NULL
);

CREATE TABLE relationships (
    id VARCHAR(128) PRIMARY KEY,
    scope_id VARCHAR(128) NOT NULL,
    source_asset_id VARCHAR(128) NOT NULL,
    target_asset_id VARCHAR(128) NOT NULL,
    graph_revision VARCHAR(128) NOT NULL,
    observed_at TIMESTAMP NOT NULL,
    closed_at TIMESTAMP,
    payload TEXT NOT NULL
);

CREATE TABLE lifecycle_bindings (
    id VARCHAR(128) PRIMARY KEY,
    scope_id VARCHAR(128) NOT NULL,
    controller_asset_id VARCHAR(128) NOT NULL,
    managed_asset_id VARCHAR(128) NOT NULL,
    graph_revision VARCHAR(128) NOT NULL,
    observed_at TIMESTAMP NOT NULL,
    closed_at TIMESTAMP,
    payload TEXT NOT NULL
);

CREATE TABLE graph_revisions (
    scope_id VARCHAR(128) PRIMARY KEY,
    graph_revision VARCHAR(128) NOT NULL,
    observed_at TIMESTAMP NOT NULL
);

CREATE TABLE findings (
    id VARCHAR(128) PRIMARY KEY,
    asset_id VARCHAR(128) NOT NULL,
    rule_id VARCHAR(256) NOT NULL,
    status VARCHAR(32) NOT NULL,
    severity VARCHAR(32) NOT NULL,
    last_seen_at TIMESTAMP NOT NULL,
    closed_at TIMESTAMP,
    payload TEXT NOT NULL,
    CONSTRAINT uq_findings_asset_rule UNIQUE (asset_id, rule_id)
);

CREATE TABLE cleanup_tasks (
    id VARCHAR(128) PRIMARY KEY,
    connection_id VARCHAR(128) NOT NULL,
    status VARCHAR(32) NOT NULL,
    created_by VARCHAR(256) NOT NULL,
    created_at TIMESTAMP NOT NULL,
    payload TEXT NOT NULL
);

CREATE TABLE cleanup_task_rows (
    id VARCHAR(128) PRIMARY KEY,
    cleanup_task_id VARCHAR(128) NOT NULL,
    row_kind VARCHAR(32) NOT NULL,
    position INTEGER NOT NULL,
    payload TEXT NOT NULL,
    CONSTRAINT uq_cleanup_task_rows_position UNIQUE (cleanup_task_id, row_kind, position)
);

CREATE TABLE execution_attempts (
    id VARCHAR(128) PRIMARY KEY,
    connection_id VARCHAR(128) NOT NULL,
    cleanup_task_id VARCHAR(128) NOT NULL,
    status VARCHAR(32) NOT NULL,
    idempotency_key VARCHAR(256) NOT NULL UNIQUE,
    created_at TIMESTAMP NOT NULL,
    payload TEXT NOT NULL
);

CREATE TABLE action_attempts (
    id VARCHAR(128) PRIMARY KEY,
    execution_id VARCHAR(128) NOT NULL,
    cleanup_task_step_id VARCHAR(128) NOT NULL,
    asset_id VARCHAR(128) NOT NULL,
    status VARCHAR(32) NOT NULL,
    idempotency_key VARCHAR(256) NOT NULL UNIQUE,
    created_at TIMESTAMP NOT NULL,
    updated_at TIMESTAMP NOT NULL,
    payload TEXT NOT NULL,
    CONSTRAINT uq_action_attempts_execution_step UNIQUE (execution_id, cleanup_task_step_id)
);

CREATE TABLE jobs (
    id VARCHAR(128) PRIMARY KEY,
    connection_id VARCHAR(128),
    idempotency_key VARCHAR(512) UNIQUE,
    aggregate_type VARCHAR(64),
    aggregate_id VARCHAR(128),
    target_key VARCHAR(1024),
    retry_generation INTEGER NOT NULL DEFAULT 0,
    job_type VARCHAR(64) NOT NULL,
    status VARCHAR(32) NOT NULL,
    run_at TIMESTAMP NOT NULL,
    lease_owner VARCHAR(256),
    lease_until TIMESTAMP,
    attempts INTEGER NOT NULL DEFAULT 0,
    created_at TIMESTAMP NOT NULL,
    updated_at TIMESTAMP NOT NULL,
    payload TEXT NOT NULL
);

CREATE TABLE job_logs (
    id VARCHAR(128) PRIMARY KEY,
    job_id VARCHAR(128) NOT NULL,
    aggregate_type VARCHAR(64),
    aggregate_id VARCHAR(128),
    target_key VARCHAR(1024),
    retry_generation INTEGER NOT NULL DEFAULT 0,
    sequence_number BIGINT NOT NULL,
    level VARCHAR(32) NOT NULL,
    message TEXT NOT NULL,
    created_at TIMESTAMP NOT NULL,
    payload TEXT NOT NULL,
    CONSTRAINT uq_job_logs_sequence UNIQUE (job_id, sequence_number)
);

CREATE TABLE audit_events (
    id VARCHAR(128) PRIMARY KEY,
    connection_id VARCHAR(128),
    actor VARCHAR(256) NOT NULL,
    action VARCHAR(256) NOT NULL,
    target_type VARCHAR(128) NOT NULL,
    target_id VARCHAR(256) NOT NULL,
    created_at TIMESTAMP NOT NULL,
    payload TEXT NOT NULL
);

CREATE TABLE outbox_events (
    id VARCHAR(128) PRIMARY KEY,
    topic VARCHAR(256) NOT NULL,
    aggregate_id VARCHAR(256) NOT NULL,
    created_at TIMESTAMP NOT NULL,
    published_at TIMESTAMP,
    payload TEXT NOT NULL,
    CONSTRAINT uq_outbox_topic_aggregate UNIQUE (topic, aggregate_id)
);

CREATE INDEX idx_connections_active_provider ON cloud_connections (deleted_at, provider, created_at, id);
CREATE INDEX idx_connection_regions_lifecycle_updated ON connection_regions (connection_id, lifecycle, updated_at, id);
CREATE INDEX idx_scopes_connection_parent ON scopes (connection_id, parent_id, kind);
CREATE UNIQUE INDEX uq_scopes_natural_identity ON scopes (connection_id, kind, native_id);
CREATE INDEX idx_scopes_superseded_by ON scopes (superseded_by_scope_id, id);
CREATE INDEX idx_scan_tasks_connection_created ON scan_tasks (connection_id, created_at, id);
CREATE INDEX idx_scan_shards_task_target_status ON scan_shards (scan_task_id, target_key, status, retry_generation);
CREATE INDEX idx_assets_active_scope ON assets (scope_id, closed_at, first_seen_at, id);
CREATE INDEX idx_assets_active_connection ON assets (connection_id, closed_at, resource_kind_id, id);
CREATE INDEX idx_asset_observations_asset_time ON asset_observations (asset_id, observed_at, id);
CREATE INDEX idx_asset_observations_shard ON asset_observations (scan_shard_id, asset_id);
CREATE INDEX idx_relationships_source_active ON relationships (source_asset_id, closed_at, graph_revision);
CREATE INDEX idx_relationships_target_active ON relationships (target_asset_id, closed_at, graph_revision);
CREATE INDEX idx_relationships_scope_active ON relationships (scope_id, closed_at, graph_revision);
CREATE INDEX idx_lifecycle_managed_active ON lifecycle_bindings (managed_asset_id, closed_at, graph_revision);
CREATE INDEX idx_lifecycle_controller_active ON lifecycle_bindings (controller_asset_id, closed_at, graph_revision);
CREATE INDEX idx_lifecycle_scope_active ON lifecycle_bindings (scope_id, closed_at, graph_revision);
CREATE INDEX idx_findings_asset_status ON findings (asset_id, status, severity);
CREATE INDEX idx_cleanup_tasks_connection_created ON cleanup_tasks (connection_id, created_at, id);
CREATE INDEX idx_cleanup_task_rows_task ON cleanup_task_rows (cleanup_task_id, row_kind, position);
CREATE INDEX idx_executions_connection_created ON execution_attempts (connection_id, created_at, id);
CREATE INDEX idx_action_attempts_execution ON action_attempts (execution_id, created_at, id);
CREATE INDEX idx_jobs_claim ON jobs (job_type, status, run_at, lease_until, id);
CREATE INDEX idx_jobs_connection_status ON jobs (connection_id, status, run_at, id);
CREATE UNIQUE INDEX uq_jobs_active_region_refresh ON jobs (connection_id, job_type)
    WHERE job_type = 'region_refresh' AND status IN ('pending', 'running');
CREATE INDEX idx_job_logs_aggregate_target_cursor ON job_logs (aggregate_type, aggregate_id, target_key, created_at, id);
CREATE INDEX idx_audits_connection_created ON audit_events (connection_id, created_at, id);
CREATE INDEX idx_audit_events_cursor ON audit_events (created_at, id);
CREATE INDEX idx_outbox_pending ON outbox_events (published_at, created_at, id);

-- +goose Down

DROP TABLE IF EXISTS outbox_events;
DROP TABLE IF EXISTS audit_events;
DROP TABLE IF EXISTS job_logs;
DROP TABLE IF EXISTS jobs;
DROP TABLE IF EXISTS action_attempts;
DROP TABLE IF EXISTS execution_attempts;
DROP TABLE IF EXISTS cleanup_task_rows;
DROP TABLE IF EXISTS cleanup_tasks;
DROP TABLE IF EXISTS findings;
DROP TABLE IF EXISTS graph_revisions;
DROP TABLE IF EXISTS lifecycle_bindings;
DROP TABLE IF EXISTS relationships;
DROP TABLE IF EXISTS asset_observations;
DROP TABLE IF EXISTS assets;
DROP TABLE IF EXISTS scan_shards;
DROP TABLE IF EXISTS scan_tasks;
DROP TABLE IF EXISTS resource_kinds;
DROP TABLE IF EXISTS scopes;
DROP TABLE IF EXISTS connection_regions;
DROP TABLE IF EXISTS connection_credentials;
DROP TABLE IF EXISTS cloud_connections;
