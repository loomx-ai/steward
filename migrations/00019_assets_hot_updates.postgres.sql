-- +goose Up

-- Every rescan rewrites each seen asset's last_seen_at, payload and revision,
-- even when its content is unchanged. With revision indexed, and no free room
-- on the row's page, PostgreSQL cannot update in place (HOT) and adds a new
-- entry to every index, the trigram GIN index included. Without the revision
-- index and with spare room per page, such a rescan touches no index.
-- ConnectionInventoryVersion then sums revision from the heap; after a scan
-- the index-only scan had to visit the heap anyway.
DROP INDEX IF EXISTS idx_assets_connection_revision;
ALTER TABLE assets SET (fillfactor = 80);

-- +goose Down

ALTER TABLE assets RESET (fillfactor);
CREATE INDEX idx_assets_connection_revision ON assets (connection_id, revision);
