-- +goose Up

-- search_text holds the lowercased values a keyword asset search matches; a
-- pg_trgm GIN index answers substring LIKE searches over it. The operator
-- class is qualified with whichever schema already holds pg_trgm.
ALTER TABLE assets ADD COLUMN search_text TEXT NOT NULL DEFAULT '';

-- +goose StatementBegin
DO $$
DECLARE
    trgm_schema TEXT;
BEGIN
    CREATE EXTENSION IF NOT EXISTS pg_trgm;
    SELECT namespace.nspname INTO trgm_schema
    FROM pg_extension extension
    JOIN pg_namespace namespace ON namespace.oid = extension.extnamespace
    WHERE extension.extname = 'pg_trgm';
    EXECUTE format(
        'CREATE INDEX idx_assets_search_text ON assets USING gin (search_text %I.gin_trgm_ops)',
        trgm_schema
    );
END
$$;
-- +goose StatementEnd

-- +goose Down

DROP INDEX IF EXISTS idx_assets_search_text;
ALTER TABLE assets DROP COLUMN search_text;
