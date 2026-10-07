-- +goose Up

-- Claims across workspaces go round the workspaces with pending work and
-- then take a workspace's oldest pending job; both are index range reads
-- here, however deep one workspace's backlog or however many finished jobs
-- the table keeps.
CREATE INDEX idx_jobs_workspace_claim ON jobs (job_type, status, workspace_id, run_at, id);

-- +goose Down

DROP INDEX IF EXISTS idx_jobs_workspace_claim;
