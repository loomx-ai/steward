-- +goose Up

-- A graph rebuild stamps every relationship and lifecycle binding with its
-- revision and observation time. Storing those shared values once per scope
-- lets a rebuild leave unchanged rows untouched; readers fill them back in.
ALTER TABLE graph_revisions ADD COLUMN item_defaults TEXT NOT NULL DEFAULT '';

-- +goose Down

ALTER TABLE graph_revisions DROP COLUMN item_defaults;
