-- +goose Up

-- Pages a connection's assets of one resource kind in cursor order without
-- sorting the whole kind; idx_assets_active_connection still serves lookups
-- by kind in id order.
CREATE INDEX idx_assets_connection_kind_cursor
    ON assets (connection_id, closed_at, resource_kind_id, first_seen_at, id);

-- +goose Down

DROP INDEX IF EXISTS idx_assets_connection_kind_cursor;
