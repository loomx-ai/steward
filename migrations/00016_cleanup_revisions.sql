-- +goose Up

-- Every update of a cleanup task, its impact rows, executions and actions bumps
-- the row's revision, so the task's row counts plus revision sums change
-- whenever its progress does.
ALTER TABLE cleanup_tasks
    ADD COLUMN revision BIGINT NOT NULL DEFAULT 0;
ALTER TABLE cleanup_task_rows
    ADD COLUMN revision BIGINT NOT NULL DEFAULT 0;
ALTER TABLE execution_attempts
    ADD COLUMN revision BIGINT NOT NULL DEFAULT 0;
ALTER TABLE action_attempts
    ADD COLUMN revision BIGINT NOT NULL DEFAULT 0;

-- +goose Down

ALTER TABLE action_attempts DROP COLUMN revision;
ALTER TABLE execution_attempts DROP COLUMN revision;
ALTER TABLE cleanup_task_rows DROP COLUMN revision;
ALTER TABLE cleanup_tasks DROP COLUMN revision;
