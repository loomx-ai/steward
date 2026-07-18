-- +goose Up

CREATE TABLE assets_scoped_identity (
    id VARCHAR(128) PRIMARY KEY,
    provider VARCHAR(64) NOT NULL,
    partition_name VARCHAR(128) NOT NULL,
    connection_id VARCHAR(128) NOT NULL,
    native_type VARCHAR(512) NOT NULL,
    native_id VARCHAR(1024) NOT NULL,
    scope_key VARCHAR(256) NOT NULL DEFAULT '',
    scope_id VARCHAR(128),
    resource_kind_id VARCHAR(256) NOT NULL,
    first_seen_at TIMESTAMP NOT NULL,
    last_seen_at TIMESTAMP NOT NULL,
    closed_at TIMESTAMP,
    payload TEXT NOT NULL,
    CONSTRAINT uq_assets_scoped_identity UNIQUE (
        provider,
        partition_name,
        connection_id,
        native_type,
        native_id,
        scope_key
    )
);

WITH RECURSIVE scope_ancestry (
    asset_id,
    current_id,
    parent_id,
    kind,
    native_id,
    depth
) AS (
    SELECT
        assets.id,
        scopes.id,
        scopes.parent_id,
        scopes.kind,
        scopes.native_id,
        0
    FROM assets
    LEFT JOIN scopes ON scopes.id = assets.scope_id

    UNION ALL

    SELECT
        scope_ancestry.asset_id,
        parent.id,
        parent.parent_id,
        parent.kind,
        parent.native_id,
        scope_ancestry.depth + 1
    FROM scope_ancestry
    JOIN scopes AS parent ON parent.id = scope_ancestry.parent_id
    WHERE scope_ancestry.kind NOT IN ('region', 'global')
      AND scope_ancestry.depth < 64
),
identity_scopes AS (
    SELECT
        asset_id,
        MAX(
            CASE
                WHEN kind = 'region' AND TRIM(native_id) <> ''
                    THEN 'region:' || TRIM(native_id)
                ELSE ''
            END
        ) AS scope_key
    FROM scope_ancestry
    GROUP BY asset_id
)
INSERT INTO assets_scoped_identity (
    id,
    provider,
    partition_name,
    connection_id,
    native_type,
    native_id,
    scope_key,
    scope_id,
    resource_kind_id,
    first_seen_at,
    last_seen_at,
    closed_at,
    payload
)
SELECT
    assets.id,
    assets.provider,
    assets.partition_name,
    assets.connection_id,
    assets.native_type,
    assets.native_id,
    COALESCE(identity_scopes.scope_key, ''),
    assets.scope_id,
    assets.resource_kind_id,
    assets.first_seen_at,
    assets.last_seen_at,
    assets.closed_at,
    assets.payload
FROM assets
LEFT JOIN identity_scopes ON identity_scopes.asset_id = assets.id;

DROP TABLE assets;
ALTER TABLE assets_scoped_identity RENAME TO assets;

CREATE INDEX idx_assets_active_scope ON assets (scope_id, closed_at, first_seen_at, id);
CREATE INDEX idx_assets_active_connection ON assets (connection_id, closed_at, resource_kind_id, id);
CREATE INDEX idx_assets_connection_list_cursor
    ON assets (connection_id, closed_at, first_seen_at, id);

-- +goose Down

CREATE TABLE assets_legacy_identity (
    id VARCHAR(128) PRIMARY KEY,
    provider VARCHAR(64) NOT NULL,
    partition_name VARCHAR(128) NOT NULL,
    connection_id VARCHAR(128) NOT NULL,
    native_type VARCHAR(512) NOT NULL,
    native_id VARCHAR(1024) NOT NULL,
    scope_id VARCHAR(128),
    resource_kind_id VARCHAR(256) NOT NULL,
    first_seen_at TIMESTAMP NOT NULL,
    last_seen_at TIMESTAMP NOT NULL,
    closed_at TIMESTAMP,
    payload TEXT NOT NULL,
    CONSTRAINT uq_assets_natural_identity UNIQUE (
        provider,
        partition_name,
        connection_id,
        native_type,
        native_id
    )
);

INSERT INTO assets_legacy_identity (
    id,
    provider,
    partition_name,
    connection_id,
    native_type,
    native_id,
    scope_id,
    resource_kind_id,
    first_seen_at,
    last_seen_at,
    closed_at,
    payload
)
SELECT
    id,
    provider,
    partition_name,
    connection_id,
    native_type,
    native_id,
    scope_id,
    resource_kind_id,
    first_seen_at,
    last_seen_at,
    closed_at,
    payload
FROM assets;

DROP TABLE assets;
ALTER TABLE assets_legacy_identity RENAME TO assets;

CREATE INDEX idx_assets_active_scope ON assets (scope_id, closed_at, first_seen_at, id);
CREATE INDEX idx_assets_active_connection ON assets (connection_id, closed_at, resource_kind_id, id);
CREATE INDEX idx_assets_connection_list_cursor
    ON assets (connection_id, closed_at, first_seen_at, id);
