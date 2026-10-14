-- Resolving work owns a short persisted lease, never a pooled SQL connection.
CREATE TABLE source_metadata_resolutions (
 source_id text PRIMARY KEY REFERENCES sources(id) ON DELETE CASCADE,
 owner text NOT NULL, token text NOT NULL, expires_at timestamptz NOT NULL
);
