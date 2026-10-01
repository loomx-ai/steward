-- +goose Up

CREATE TABLE scan_schedules (
    id VARCHAR(128) PRIMARY KEY,
    connection_id VARCHAR(128) NOT NULL,
    enabled BOOLEAN NOT NULL,
    next_run_at TIMESTAMP,
    revision BIGINT NOT NULL DEFAULT 1,
    created_at TIMESTAMP NOT NULL,
    updated_at TIMESTAMP NOT NULL,
    payload TEXT NOT NULL
);

CREATE INDEX idx_scan_schedules_due ON scan_schedules (enabled, next_run_at);
CREATE INDEX idx_scan_schedules_connection ON scan_schedules (connection_id, created_at, id);

CREATE TABLE scan_schedule_runs (
    id VARCHAR(128) PRIMARY KEY,
    schedule_id VARCHAR(128) NOT NULL,
    connection_id VARCHAR(128) NOT NULL,
    planned_at TIMESTAMP NOT NULL,
    scan_task_id VARCHAR(128),
    settled BOOLEAN NOT NULL DEFAULT FALSE,
    created_at TIMESTAMP NOT NULL,
    payload TEXT NOT NULL
);

CREATE INDEX idx_scan_schedule_runs_schedule ON scan_schedule_runs (schedule_id, created_at, id);
CREATE INDEX idx_scan_schedule_runs_unsettled ON scan_schedule_runs (settled, created_at);
CREATE INDEX idx_scan_schedule_runs_scan ON scan_schedule_runs (scan_task_id);

ALTER TABLE scan_tasks ADD COLUMN schedule_id VARCHAR(128);

CREATE INDEX idx_scan_tasks_schedule ON scan_tasks (schedule_id, created_at, id);

CREATE TABLE workspace_settings (
    setting_key VARCHAR(128) PRIMARY KEY,
    updated_at TIMESTAMP NOT NULL,
    payload TEXT NOT NULL
);

-- +goose Down

DROP TABLE workspace_settings;
DROP INDEX idx_scan_tasks_schedule;
ALTER TABLE scan_tasks DROP COLUMN schedule_id;
DROP TABLE scan_schedule_runs;
DROP TABLE scan_schedules;
