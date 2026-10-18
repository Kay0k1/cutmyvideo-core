# Migration 9: external audio references

Core 1.5 adds the optional `jobs.audio_source_id` reference to a separately
authorized uploaded recording. Existing jobs receive `NULL`; migration 9 does
not backfill references or rewrite earlier application rows. Migration SQL and
catalog manifests for positions 1–8 remain unchanged.

The database derives the reference from `request.audio_replace.source_id` on
insertion or request replacement. Both source references participate in retained
job pins, bounded audits, active-job protection and media cleanup checks. The
primary source and replacement must belong to the same owner, be distinct local
uploads, and have available media when a new job is admitted. A project grant on
the primary source does not authorize another owner's recording.

Explicitly deleting a finished job's replacement recording preserves the
primary job and result artifacts. The foreign key clears the relational
reference while the original request remains as provenance. Active jobs,
reservations and read leases prevent deletion. Automatic retention continues to
protect both references for retained jobs. `cleanup_source` after a successful
export applies to the primary recording only.

## Upgrade and recovery

1. Stop API, worker and maintenance writers. Preserve a coordinated pre-upgrade
   database/media backup, the matching private operator configuration and the
   exact current image identity. Verify the backup before migration.
2. Run the compatible release's schema-owner initialization. Its existing
   30-second migration budget covers migration 9; cancellation rolls back the
   transaction. Apply the normal restricted runtime grants before starting
   writers. The existing catalog-based grant pass covers the updated tables.
3. Start the compatible API and worker, then check readiness and real exports.
   Keep the coordinated backup and a verified recovery image that understands
   position 9.

Core 1.4.2's guarded binary refuses position 9 with `schema_incompatible` and
leaves catalog, migration history and application rows unchanged. An in-place
binary downgrade is therefore unavailable. To return to 1.4.2, restore the
matching **position-8 database and media backup together** into fresh resources,
with its retained image and operator configuration, then verify the restored
installation before switching traffic. Do not delete migration markers,
manually clear references or replace only the database. A position-9 backup must
be recovered with a compatible position-9 image.

## Required qualification

The public self-hosting workflow defaults to the separate
`external-audio-008-to-009` mode. It pins the real 1.4.2 predecessor at
`106d0d2d7887540768cc21e3bd4e2c71629af6c4`, admits the complete candidate source
inventory before building images, and permits only the reviewed additive 009
SQL/catalog pair. Earlier transition modes retain their original predecessors
and schema contracts.

This mode requires actual old/current PostgreSQL catalogs, unchanged historical
rows, the old binary's refusal without mutation, a fresh coordinated position-8
rollback, a fresh compatible position-9 restore, restricted-role pin CRUD, and
preserved replacement references/results. A new authenticated HTTPS export
after restore must exercise replacement audio, measured normalization and a
canvas, decode successfully, meet independent loudness/geometry checks, contain
the replacement's distinct 997 Hz signal instead of the primary's 660 Hz, and
deny another owner's source and artifact access. Sanitized evidence is retained
by the workflow. The host driver is for disposable GitHub runners only; its
controls do not establish an actual upgrade or production readiness by
themselves.

The isolated PostgreSQL recovery gate additionally requires these exact tests,
with no skips:

- `TestExternalAudioPreviousBinaryRefuses009WithoutWrites`
- `TestExternalAudioCoordinated009BackupRestorePreservesRowsAndMedia`
- `TestExternalAudioRuntimeDMLGrantsInvokeDerivedPinTriggers`

The first test requires an exact 1.4.2 predecessor binary and its SHA-256 through
`CUTMY_TEST_PREVIOUS_SCHEMA008_BINARY` and
`CUTMY_TEST_PREVIOUS_SCHEMA008_SHA256`. The restore test uses the owned
PostgreSQL tools container configured by `CUTMY_TEST_PGTOOLS_CONTAINER`. These
checks use isolated test schemas/databases and coordinated synthetic media.

See [startup compatibility](migrations.md),
[backup and recovery](backup-recovery.md), and
[self-hosting](self-hosting.md) for the operational prerequisites.
