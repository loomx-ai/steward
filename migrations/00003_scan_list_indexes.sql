-- +goose Up

CREATE INDEX idx_jobs_aggregate_created
    ON jobs (aggregate_type, aggregate_id, created_at, id);

-- +goose Down

DROP INDEX IF EXISTS idx_jobs_aggregate_created;
