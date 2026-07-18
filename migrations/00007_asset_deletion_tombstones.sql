-- +goose Up

ALTER TABLE assets
    ADD COLUMN deleted_at TIMESTAMP;

UPDATE assets
SET deleted_at = (
    SELECT MAX(action_attempts.updated_at)
    FROM action_attempts
    WHERE action_attempts.asset_id = assets.id
      AND action_attempts.status = 'succeeded'
      AND action_attempts.payload LIKE '%"action":"delete"%'
)
WHERE EXISTS (
    SELECT 1
    FROM action_attempts
    WHERE action_attempts.asset_id = assets.id
      AND action_attempts.status = 'succeeded'
      AND action_attempts.payload LIKE '%"action":"delete"%'
);

UPDATE assets
SET closed_at = deleted_at
WHERE deleted_at IS NOT NULL
  AND closed_at IS NULL;

-- +goose Down

ALTER TABLE assets DROP COLUMN deleted_at;
