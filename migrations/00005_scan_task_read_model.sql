-- +goose Up

ALTER TABLE scan_tasks
    ADD COLUMN resource_count BIGINT NOT NULL DEFAULT 0;
ALTER TABLE scan_tasks
    ADD COLUMN duration_ms BIGINT NOT NULL DEFAULT 0;
ALTER TABLE scan_tasks
    ADD COLUMN duration_recorded BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE scan_tasks
    ADD COLUMN duration_active BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE scan_tasks
    ADD COLUMN duration_calculated_at TIMESTAMP;
ALTER TABLE scan_tasks
    ADD COLUMN updated_at TIMESTAMP NOT NULL DEFAULT '1970-01-01 00:00:00+00:00';

UPDATE scan_tasks
SET updated_at = created_at;

ALTER TABLE scan_shards
    ADD COLUMN item_count BIGINT NOT NULL DEFAULT 0;

-- +goose Down

ALTER TABLE scan_shards DROP COLUMN item_count;
ALTER TABLE scan_tasks DROP COLUMN updated_at;
ALTER TABLE scan_tasks DROP COLUMN duration_calculated_at;
ALTER TABLE scan_tasks DROP COLUMN duration_active;
ALTER TABLE scan_tasks DROP COLUMN duration_recorded;
ALTER TABLE scan_tasks DROP COLUMN duration_ms;
ALTER TABLE scan_tasks DROP COLUMN resource_count;
