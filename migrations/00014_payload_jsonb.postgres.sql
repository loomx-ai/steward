-- +goose Up

-- Asset queries read fields inside asset and resource kind payloads. Storing
-- them as jsonb parses each document once on write instead of on every read.
ALTER TABLE assets ALTER COLUMN payload TYPE JSONB USING payload::jsonb;
ALTER TABLE resource_kinds ALTER COLUMN payload TYPE JSONB USING payload::jsonb;

-- +goose Down

ALTER TABLE resource_kinds ALTER COLUMN payload TYPE TEXT USING payload::text;
ALTER TABLE assets ALTER COLUMN payload TYPE TEXT USING payload::text;
