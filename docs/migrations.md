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
| 3 | `20261010-storage-maintenance-v1` | Adds persisted directory scan progress, a workspace cleanup queue, and deletion retry times/failure counts. Pending files stay charged and are immediately eligible after upgrade. |
| 4 | `20261010-cache-retention-v1` | Replaces the cache expiry index with `(expires_at, source_id)` for ordered, bounded cache cleanup. Leaves cache payloads, scan progress, deletion retries and storage accounting unchanged. |
| 5 | `20261011-source-retention-v1` | Adds derived job/preview pin counts, exact membership triggers, an eligible-source index and durable bounded audit progress. Backfills counts while excluding membership writers; leaves original source values, jobs, media and byte accounting intact. |
| 6 | `20261012-source-media-cleanup-v1` | Adds durable post-success source-media cleanup and read leases; retains source metadata, jobs and result artifacts independently. |
| 7 | `20261014-metadata-resolution-leases-v1` | Coordinates platform metadata extraction through expiring owner/token leases without keeping SQL connections occupied during upstream work. |

SQL files and expected catalog layouts live in
[`internal/app/migrations`](../internal/app/migrations). `app_schema_versions`
retains the historical marker and records each new version.
`app_schema_migrations` stores consecutive positions, versions, SHA-256 checksums
of the immutable SQL files and application timestamps. All pending migrations
and their records commit in one transaction.
SQL uses canonical LF line endings for execution and checksums; Windows CRLF
checkouts or source archives cannot create a different migration identity.

## Refusal and recovery

Before issuing migration DDL, startup rejects unknown or newer version markers,
missing predecessors, inconsistent ledger records/checksums, and unsupported
schema structure. It checks column types, nullability/defaults, constraints,
required indexes, accounting/source-pin trigger targets, UPDATE column lists,
conditions and function bodies/attributes. Manually
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

## Migration 5 upgrade and rollback

Stop API/worker/maintenance writers and take a coordinated database/media backup
before the schema-owner upgrade. Migration 5 also takes the storage advisory
fence and locks sources/jobs/reservations against direct membership writers
before DDL/backfill/trigger installation. The existing 30-second startup cap
covers this whole transaction; the initial one-time backfill is not bounded by
200 rows. Test the restored retained-data copy against that cap before upgrade.
A lock wait or cancelled backfill rolls back columns, counts, triggers and
migration records together; retry with the compatible new binary.

After a successful migration, versions 1–5 and their original checksums are
required. An older schema-guarded binary refuses the new marker/columns. There
is no in-place reverse migration: rollback to a pre-005 binary requires restoring
its coordinated pre-upgrade backup to a separate installation. Retain failed
instance state for diagnosis; never remove ledger records or derived columns to
force an older binary to start.

The original A1 public image-upgrade acceptance requires byte-identical runtime
sources and tests a same-schema transition. It intentionally rejects this
runtime change. Migration 5 needs its own reviewed schema-transition/restore
acceptance, including runtime-role trigger execution and truthful/drifted pin
fixtures; historical A1/c911/638 results do not establish it. Five native archive
checks and archive recovery must also run for the actual candidate.

See [source retention and drift recovery](source-retention.md) for the bounded
runtime work and the separate corruption-recovery limits.

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

## Migration 6: source media cleanup

`006_20261012_source_media_cleanup_v1.sql` adds `sources.media_removed` with a
false default, a durable cleanup queue and bounded renewable read leases. It
changes no historical source/job/artifact values or byte counters. Its expected
catalog was captured on isolated PostgreSQL 17.11. Positions 1–5 and their SQL,
checksums and catalog manifests remain unchanged.

As with migration 5, an earlier guarded binary refuses the newer marker and
source column. Prepare a recovery build that understands migration 6 before
production rollout. It may retain the prior UI/behavior while sharing this
lifecycle contract. Reverting to the pre-006 binary instead requires a
coordinated pre-upgrade database/media restore; removing version markers or
columns is not a valid rollback. Restoring only metadata cannot recreate source
bytes that successful cleanup intentionally removed. Backups and the prepared
compatible recovery release must be verified before deployment.

## Migration 7: metadata resolution leases

`007_20261014_metadata_resolution_leases_v1.sql` adds one optional coordination
table keyed by source. It changes no historical values, media, byte counters or
positions 1–6. Each lease expires after three minutes; an unavailable coordinator
or a peer that takes more than the two-second cache wait budget retains the
independent fresh-resolution fallback. Replacement tokens and explicit cache
invalidation fence older resolver publications. Source deletion cascades leases.

The SQL pool is used only for short claim, read and publication transactions.
Upstream extraction does not retain a pooled connection, so concurrent cache
misses leave lease heartbeats and other database work able to acquire one.
Successful cache publication and claim removal commit together.

Older guarded binaries refuse position 7. Before rollout, verify a compatible
recovery image and coordinated backup/restore on the new catalog; current
position-6 deployment evidence does not establish this transition. Runtime
grants must include SELECT/INSERT/UPDATE/DELETE on the new table. The normal
`deploy/public-grants.sql` catalog-based grant pass includes it automatically.

The bounded owned-PostgreSQL17 gates verify the exact position-6 baseline CLI
refuses position 7 without catalog or row-version changes, a restricted runtime
LOGIN can start and perform the new lease CRUD/cache publication without schema
ownership or CREATE, and a real custom-format `pg_dump`/`pg_restore` into a fresh
owned database preserves all rows and coordinated media bytes. Compatible
position-7 startup leaves restored row versions and catalog objects unchanged.
These are `TestMetadataResolutionPreviousBinaryRefuses007WithoutWrites`,
`TestMetadataResolutionRuntimeGrantsPermitLeaseCRUDWithoutOwnership`, and
`TestMetadataResolutionCoordinated007BackupRestorePreservesRowsAndMedia`.
The exact 6→7 upgrade also preserves historical rows and migration positions in
`TestMetadataResolutionMigrationPreserves006RowsAndRestartsWithoutWrites`.
These disposable-database checks establish local compatibility and recovery
behavior. No position-7 recovery release image, public hosting transition, or
production rollout has been performed by this work.
