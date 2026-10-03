-- +goose Up

-- search_text holds the lowercased values a keyword asset search matches. A
-- trigram FTS5 index answers substring searches over it. asset_search_rows
-- gives every asset a stable integer key for the index, because VACUUM may
-- renumber the implicit rowids of assets.
ALTER TABLE assets ADD COLUMN search_text TEXT NOT NULL DEFAULT '';

CREATE TABLE asset_search_rows (
    id INTEGER PRIMARY KEY,
    asset_id VARCHAR(128) NOT NULL UNIQUE
);

INSERT INTO asset_search_rows (asset_id) SELECT id FROM assets;

CREATE VIEW asset_search_documents AS
SELECT asset_search_rows.id AS id, assets.search_text AS document
FROM asset_search_rows
JOIN assets ON assets.id = asset_search_rows.asset_id;

-- Search text is lowercased before it is stored and queried, so the index is
-- case-sensitive, which lets it serve GLOB patterns.
CREATE VIRTUAL TABLE asset_search USING fts5(
    document,
    content = 'asset_search_documents',
    content_rowid = 'id',
    tokenize = 'trigram case_sensitive 1'
);

INSERT INTO asset_search (asset_search) VALUES ('rebuild');

-- +goose StatementBegin
CREATE TRIGGER assets_search_insert AFTER INSERT ON assets BEGIN
    INSERT INTO asset_search_rows (asset_id) VALUES (new.id);
    INSERT INTO asset_search (rowid, document) VALUES (last_insert_rowid(), new.search_text);
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER assets_search_update AFTER UPDATE OF search_text ON assets
WHEN new.search_text <> old.search_text BEGIN
    INSERT INTO asset_search (asset_search, rowid, document)
    SELECT 'delete', id, old.search_text FROM asset_search_rows WHERE asset_id = old.id;
    INSERT INTO asset_search (rowid, document)
    SELECT id, new.search_text FROM asset_search_rows WHERE asset_id = new.id;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER assets_search_delete AFTER DELETE ON assets BEGIN
    INSERT INTO asset_search (asset_search, rowid, document)
    SELECT 'delete', id, old.search_text FROM asset_search_rows WHERE asset_id = old.id;
    DELETE FROM asset_search_rows WHERE asset_id = old.id;
END;
-- +goose StatementEnd

-- +goose Down

DROP TRIGGER IF EXISTS assets_search_delete;
DROP TRIGGER IF EXISTS assets_search_update;
DROP TRIGGER IF EXISTS assets_search_insert;
DROP TABLE IF EXISTS asset_search;
DROP VIEW IF EXISTS asset_search_documents;
DROP TABLE IF EXISTS asset_search_rows;
ALTER TABLE assets DROP COLUMN search_text;
