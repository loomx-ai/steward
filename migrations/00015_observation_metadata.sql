-- +goose Up

-- An observation records that a shard read an asset; its content lives on the
-- asset and is identified by content_hash, so rows keep only the columns that
-- coverage and projection read.
ALTER TABLE asset_observations ADD COLUMN schema_revision VARCHAR(256) NOT NULL DEFAULT '';
ALTER TABLE asset_observations ADD COLUMN priority INTEGER NOT NULL DEFAULT 0;
ALTER TABLE asset_observations ADD COLUMN authoritative BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE asset_observations DROP COLUMN payload;

-- +goose Down

ALTER TABLE asset_observations ADD COLUMN payload TEXT NOT NULL DEFAULT '{}';
ALTER TABLE asset_observations DROP COLUMN authoritative;
ALTER TABLE asset_observations DROP COLUMN priority;
ALTER TABLE asset_observations DROP COLUMN schema_revision;
