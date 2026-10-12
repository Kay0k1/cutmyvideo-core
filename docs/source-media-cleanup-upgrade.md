# Migration006 upgrade and recovery

Migration006 introduces source-media cleanup while retaining source metadata,
job snapshots and finished artifacts. It is additive: existing sources start
with `media_removed=false`; existing export requests continue keeping originals.
SQL and catalog manifests001–005 retain their exact released bytes.

The current hosted acceptance mode is
`source-media-cleanup-005-to-006`. It admits the immutable005 predecessor and the
exact candidate inventory, independently observes the actual005/006 catalogs
and migration checksums, compares original records immediately after migration,
checks old005 refusal without writes, restores coordinated005 backup into fresh
resources, and restores006 backup using the exact compatible006 image. A real
HTTPS export additionally removes its original while preserving owner-protected
job access, downloads and independent decoding of the completed file.

The historical `same-schema`, `current-installation` and
`source-retention-004-to-005` modes keep their original meanings. Their previous
receipts do not establish this006 transition. The new mode keeps its own raw
source/catalog/record/download evidence; synthetic receipt controls alone are
not hosted acceptance.

## Before upgrade

1. Stop API, worker and scheduled maintenance writers using the deployment
   operator's normal procedure. Confirm no writer can restart during backup.
2. Capture a coordinated database and media backup, configuration and immutable
   source/image identity. Restrict backup permissions; never publish database
   dumps, private media, deployment configuration or capability cookies.
3. Test restoring that backup into fresh, isolated resources with the005 binary.
   Confirm source bytes, acknowledged artifact hashes, ownership, job snapshots,
   idempotency and counters. Retain that backup until release verification ends.
4. Prepare and verify an immutable recovery release whose core understands006.
   This can retain previously working product behavior, but must contain the006
   schema guard and the source-media lifecycle contract. Verify it against an
   actual restored006 database/media pair before production deployment.
5. Start the006 schema-owner migration through the normal deployment path, then
   start the matching API/worker. Confirm the ordered ledger and service health.

## Compatible006 recovery

For recovery after migration, stop writers and preserve the failed instance for
diagnosis. Deploy the prepared006-compatible core together with an API-compatible
web bundle. Keep persistent volumes. Verify health, ownership, pending cleanup
recovery and downloads from an intentionally removed source. A source flag or
cleanup queue entry must never be cleared just to make old code accept media.

If the database or media need restoration, restore a coordinated006 snapshot to
fresh resources and use the immutable006-compatible image. Preserve completed
artifacts and the pending cleanup/read-lease state. Confirm the exact restored
records before allowing maintenance to advance them. Do not restore database
metadata alone: it can point to original bytes already intentionally removed.

## Reversal to005

A005 binary correctly refuses the006 marker and column. There is no reverse
migration that drops006 columns, tables, checksums or markers. Returning to005
requires the coordinated pre-upgrade005 database/media backup restored to fresh
resources, followed by the exact compatible005 release. Verify acknowledged
media and counters before switching traffic. Retain the failed006 state rather
than overwriting it. Changes made after the005 backup are not present in that
older snapshot; choose compatible006 recovery when those changes must survive.

## Reproducible PostgreSQL backup/restore gate

The ordinary integration suite includes
`TestSourceMediaCleanupCoordinated005ReverseAndCompatible006BackupRestore`.
It uses a dedicated test PostgreSQL17 service and its matching `pg_dump` and
`pg_restore` tools. Set `TEST_DATABASE_URL` to that disposable service and
`CUTMY_TEST_PGTOOLS_CONTAINER` to its owned Docker container identity. Hosted CI
sets the container identity from `job.services.postgres.id`.

The fixture contains two original videos, a removed original with retained
outputs, and pending cleanup protected by a read lease. It performs real005/006
custom-format dumps and media archives, creates fresh restore databases, checks
exact records/catalogs, boots the006-compatible store, verifies downloads and
resumes pending cleanup. It never touches production or unrelated databases.
