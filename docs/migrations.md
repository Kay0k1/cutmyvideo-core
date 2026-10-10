# Database migrations and startup compatibility

API, worker and maintenance use the same `OpenStore` startup path. Startup has a
30-second budget, including connection establishment, pool acquisition, the
migration lock and SQL. An earlier caller deadline or cancellation wins. Failed
transactions use a separate one-second rollback budget before the pool closes.

Startup takes the existing PostgreSQL advisory lock for `cutmy:migrations`,
resolves the first existing schema in the configured `search_path`, and inspects
only that namespace. Later search-path entries cannot silently provide its
application tables. Create a custom application schema before configuring it;
a nonexistent first entry is skipped by PostgreSQL's `current_schema()` rules.

## Supported history

The repository history contains one recorded legacy marker:
`20261005-storage-queue-v3`. It was introduced in commit `49d84f7` and is the
schema used by releases v0.2.0, v0.2.1 and the reliability-contract changes.
Earlier builds had no version table. Their original, provider-ID, platform and
metadata-cache layouts are recognized before applying the legacy baseline.
There are no invented `v1`/`v2` markers for those unversioned installations.

| Position | Version | Effect |
| --- | --- | --- |
| 1 | `20261005-storage-queue-v3` | Frozen application/storage schema captured from v0.2.1. Creates a fresh installation or upgrades a recognized unversioned schema. |
| 2 | `20261010-ordered-migrations-v1` | Adds the ordered migration ledger and records the verified baseline and its checksums. Existing versioned application rows and tables are not rewritten. |

SQL files and expected catalog layouts live in
[`internal/app/migrations`](../internal/app/migrations). `app_schema_versions`
retains the historical marker and records each new version.
`app_schema_migrations` stores consecutive positions, versions, SHA-256 checksums
of the immutable SQL files and application timestamps. All pending migrations
and their records commit in one transaction.

## Refusal and recovery

Before issuing migration DDL, startup rejects unknown or newer version markers,
missing predecessors, inconsistent ledger records/checksums, and unsupported
schema structure. It checks column types, nullability/defaults, constraints,
required indexes, accounting triggers and the accounting function. Manually
added columns or changed managed constraints/functions are not automatically
adopted. These checks inspect PostgreSQL catalogs and the small migration
history; they do not scan retained jobs, media files or storage-ledger rows.

The public CLI returns `schema_incompatible` with a fixed message; callers of
the internal startup API can detect `ErrSchemaIncompatible`. Refusal leaves
schema objects, migration records and application data unchanged. Do not delete
markers, edit checksums or reset tables to force startup. Select a compatible
binary/database pair or restore a verified backup instead.

Concurrent startups serialize through the migration lock. A current schema
performs compatibility reads without repeating migration DDL or writing version
records. Cancellation while waiting for the migration lock or a DDL table lock
rolls back partial changes. After a lost COMMIT acknowledgement, do not assume
the migration failed: the next compatible startup verifies the authoritative
ordered history and resumes any unapplied migrations.

Historical binaries cannot retroactively acquire this refusal guard. They can
still ignore markers added by newer releases. A known version marker also does
not prove that an old binary supports a later schema. Stop writers, preserve the
database/media backup and test the intended rollback against that backup before
starting an older binary. See [upgrade and recovery](operations.md#upgrade-and-recover).

## Adding a migration

Append a new SQL file and migration entry; never edit a shipped SQL file or
reinterpret its version/checksum. Capture the expected catalog layout for that
new position and retain prior layouts for supported upgrade paths. A semantic
schema change must receive its own version, even when its SQL is idempotent.

Run the PostgreSQL migration tests against an isolated database. They exercise
fresh/current startup, immutable schemas captured from actual historical
commits, data and queue/accounting preservation, concurrent startup,
unknown/future/corrupt refusal, cancellation rollback and namespace isolation.
Run the broader API/worker and backup/restore acceptance checks before release.
Catalog validation is a compatibility check, not a replacement for backup
verification, data-integrity checks or media recovery.
