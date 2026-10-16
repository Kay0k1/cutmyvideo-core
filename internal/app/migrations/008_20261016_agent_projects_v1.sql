CREATE TABLE source_preparations (
 owner text NOT NULL, key text NOT NULL, intent_hash text NOT NULL,
 kind text NOT NULL, source_id text NOT NULL, claim_token text NOT NULL,
 state text NOT NULL, lease_until timestamptz NOT NULL,
 fingerprint jsonb, error_code text NOT NULL DEFAULT '',
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 expires_at timestamptz NOT NULL DEFAULT (clock_timestamp()+interval '7 days'),
 PRIMARY KEY(owner,key),
 CHECK(length(key) BETWEEN 1 AND 128),
 CHECK(intent_hash ~ '^[0-9a-f]{64}$'),
 CHECK(kind IN ('upload','import')),
 CHECK(state IN ('processing','ready','retryable','failed'))
);
CREATE INDEX source_preparations_expiry_idx ON source_preparations(expires_at,owner,key);
CREATE TABLE source_fingerprints (
 source_id text PRIMARY KEY REFERENCES sources(id) ON DELETE CASCADE,
 owner text NOT NULL, fingerprint jsonb NOT NULL
);
CREATE TABLE agent_projects (
 id text PRIMARY KEY, owner text NOT NULL, source_id text NOT NULL,
 plan jsonb NOT NULL, source_snapshot jsonb NOT NULL, clip_ids jsonb NOT NULL DEFAULT '[]'::jsonb,
 rendition text NOT NULL, collection_key text NOT NULL,
 revision bigint NOT NULL DEFAULT 1,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 expires_at timestamptz NOT NULL DEFAULT (clock_timestamp()+interval '7 days'),
 CHECK(revision>0), CHECK(rendition IN ('draft','final')),
 CHECK(octet_length(plan::text)<=1048576), CHECK(length(collection_key) BETWEEN 1 AND 128)
);
CREATE INDEX agent_projects_owner_idx ON agent_projects(owner,expires_at,id);
CREATE UNIQUE INDEX agent_projects_owner_key_idx ON agent_projects(owner,collection_key);
CREATE TABLE agent_project_jobs (
 project_id text NOT NULL REFERENCES agent_projects(id) ON DELETE CASCADE,
 job_id text NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
 clip_id text NOT NULL,
 PRIMARY KEY(project_id,job_id)
);
CREATE TABLE agent_project_handoffs (
 token_hash text PRIMARY KEY, project_id text NOT NULL REFERENCES agent_projects(id) ON DELETE CASCADE,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 expires_at timestamptz NOT NULL DEFAULT (clock_timestamp()+interval '10 minutes'),
 redeemed_at timestamptz, recipient text,
 CHECK(token_hash ~ '^[0-9a-f]{64}$')
);
CREATE INDEX agent_project_handoffs_project_idx ON agent_project_handoffs(project_id,expires_at);
CREATE TABLE agent_project_grants (
 project_id text NOT NULL REFERENCES agent_projects(id) ON DELETE CASCADE,
 recipient text NOT NULL,
 expires_at timestamptz NOT NULL DEFAULT (clock_timestamp()+interval '24 hours'),
 PRIMARY KEY(project_id,recipient)
);
CREATE INDEX agent_project_grants_expiry_idx ON agent_project_grants(expires_at,project_id);
CREATE TABLE agent_project_retries (
 project_id text NOT NULL REFERENCES agent_projects(id) ON DELETE CASCADE,
 key text NOT NULL, intent_hash text NOT NULL,
 request jsonb NOT NULL, attempt_key text NOT NULL,
 clip_intents jsonb NOT NULL, clip_job_keys jsonb NOT NULL,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(project_id,key),
 CHECK(length(key) BETWEEN 1 AND 128),
 CHECK(intent_hash ~ '^[0-9a-f]{64}$'),
 CHECK(octet_length(request::text)<=1048576)
);
CREATE INDEX agent_project_retries_latest_idx ON agent_project_retries(project_id,created_at DESC,key DESC);
