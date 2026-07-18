-- +goose Up

CREATE INDEX idx_jobs_connection_type_created
    ON jobs (connection_id, job_type, created_at, id);
CREATE INDEX idx_connection_regions_list_cursor
    ON connection_regions (connection_id, lifecycle, region_id, id);
CREATE INDEX idx_assets_connection_list_cursor
    ON assets (connection_id, closed_at, first_seen_at, id);
CREATE INDEX idx_executions_cleanup_task_list_cursor
    ON execution_attempts (connection_id, cleanup_task_id, created_at, id);

-- +goose Down

DROP INDEX IF EXISTS idx_jobs_connection_type_created;
DROP INDEX IF EXISTS idx_connection_regions_list_cursor;
DROP INDEX IF EXISTS idx_assets_connection_list_cursor;
DROP INDEX IF EXISTS idx_executions_cleanup_task_list_cursor;
