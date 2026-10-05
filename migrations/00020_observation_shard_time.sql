-- +goose Up

-- Reads an older shard's observations made after a target's latest
-- authoritative success without walking every observation the shard kept.
CREATE INDEX idx_asset_observations_shard_time ON asset_observations (scan_shard_id, observed_at, asset_id);

-- +goose Down

DROP INDEX IF EXISTS idx_asset_observations_shard_time;
