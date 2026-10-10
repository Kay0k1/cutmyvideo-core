CREATE TABLE app_schema_migrations (
 position integer PRIMARY KEY CHECK(position>0),
 version text NOT NULL UNIQUE,
 checksum text NOT NULL CHECK(checksum ~ '^[0-9a-f]{64}$'),
 applied_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
