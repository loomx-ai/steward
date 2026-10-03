-- +goose Up

-- Every asset and finding write bumps its row's revision, so a connection's
-- row count plus revision sum changes whenever its inventory does.
ALTER TABLE assets
    ADD COLUMN revision BIGINT NOT NULL DEFAULT 0;
ALTER TABLE findings
    ADD COLUMN revision BIGINT NOT NULL DEFAULT 0;
CREATE INDEX idx_assets_connection_revision ON assets (connection_id, revision);

-- +goose Down

DROP INDEX IF EXISTS idx_assets_connection_revision;
ALTER TABLE findings DROP COLUMN revision;
ALTER TABLE assets DROP COLUMN revision;
