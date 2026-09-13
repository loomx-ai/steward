-- +goose Up

ALTER TABLE graph_revisions ADD COLUMN unresolved_payload TEXT NOT NULL DEFAULT '[]';

-- +goose Down

ALTER TABLE graph_revisions DROP COLUMN unresolved_payload;
