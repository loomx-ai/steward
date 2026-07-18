-- +goose Up

ALTER TABLE assets
    ADD COLUMN dirty BOOLEAN NOT NULL DEFAULT FALSE;

-- +goose Down

ALTER TABLE assets DROP COLUMN dirty;
