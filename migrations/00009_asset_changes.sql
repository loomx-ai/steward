-- +goose Up

CREATE TABLE asset_changes (
    id VARCHAR(128) PRIMARY KEY,
    connection_id VARCHAR(128) NOT NULL,
    scan_task_id VARCHAR(128) NOT NULL,
    asset_id VARCHAR(128) NOT NULL,
    change_type VARCHAR(32) NOT NULL,
    resource_kind_id VARCHAR(256) NOT NULL,
    changed_at TIMESTAMP NOT NULL,
    payload TEXT NOT NULL
);

CREATE UNIQUE INDEX uq_asset_changes_scan_asset ON asset_changes (scan_task_id, asset_id);
CREATE INDEX idx_asset_changes_scan_type ON asset_changes (scan_task_id, change_type, changed_at, id);

-- +goose Down

DROP TABLE asset_changes;
