-- +goose Up

-- Finds a network target's earlier shards without walking every scan of the
-- connection, and the shards of a scope during scope consolidation.
CREATE INDEX idx_scan_shards_scope_target
    ON scan_shards (scope_id, target_key, source, resource_kind_id, created_at);
-- Pages findings, and assets of every connection, in cursor order.
CREATE INDEX idx_findings_cursor ON findings (last_seen_at, id);
CREATE INDEX idx_assets_list_cursor ON assets (closed_at, first_seen_at, id);
-- Sums a connection's finding revisions without reading the rows.
CREATE INDEX idx_findings_asset_revision ON findings (asset_id, revision);

-- +goose Down

DROP INDEX IF EXISTS idx_scan_shards_scope_target;
DROP INDEX IF EXISTS idx_findings_cursor;
DROP INDEX IF EXISTS idx_assets_list_cursor;
DROP INDEX IF EXISTS idx_findings_asset_revision;
