-- +goose Up

-- One counter per connection, bumped by every transaction that changes the
-- connection's assets, findings or graph, so topology reads its cache key
-- without scanning them. A missing row reads as version 0.
CREATE TABLE inventory_versions (
    connection_id TEXT PRIMARY KEY,
    version BIGINT NOT NULL
);

-- +goose Down

DROP TABLE inventory_versions;
