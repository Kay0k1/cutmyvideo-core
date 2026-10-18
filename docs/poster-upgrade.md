# Poster artifacts: schema 9 to 10

Schema 10 stores inspected image derivatives separately from completed media
artifacts. A poster is not a zero-duration video or another source thumbnail.
It has an owner, parent artifact, exact output-clock request, registered bytes
and an immutable expiry no later than its parent. A legacy parent with no
stored expiry uses its creation time and configured artifact lifetime.

Before upgrading a running schema-9 installation:

1. Stop API, workers and maintenance writers and prevent new requests.
2. Save a coordinated database and media snapshot, including persistent
   operator configuration through the established private backup procedure.
3. Start the new schema-10 binary and verify the complete migration ledger,
   roles, health and preserved old sources, projects and downloadable media.
4. Start matching workers and reopen traffic after verification. Preserve the
   backup and exact prior release identity.

Older binaries must refuse schema 10 without writes. A rollback to 1.5 needs
its matching pre-upgrade schema-9 database/media snapshot while writers remain
stopped. Do not edit the migration ledger, run an older binary against schema
10, or rewind acknowledged traffic after reopening the new release.

Poster generation obtains a bounded artifact-reader lease before opening parent
bytes. The lease protects that parent from retention and source deletion while
the synchronous generation runs. Reader leases are bounded to 60 seconds;
generation itself is bounded to 30 seconds. Owner/project-grant authorization,
byte admission and decoder limits remain separate checks.

Deleting a source removes its related parent job/artifact and image metadata
through the coordinated lifecycle transaction. Removed image files become
registered deletion tombstones; their bytes remain charged until physical
deletion is confirmed. Deleting an individual image leaves its parent video
available. Parent expiry/deletion also governs dependent images; an image cannot
extend video retention. Active readers/preparation can block deletion.

Repeated creation of the same parent's timestamp and format returns the existing
image before requiring a new decoder or byte reservation. A failed, cancelled
or rejected preparation must not publish partial images, lose its parent or
release an unresolved committed byte charge. Polling/metadata reads remain
available; poster creation adds no background job or webhook delivery system.

See [backup recovery](backup-recovery.md), [migrations](migrations.md) and
[controlled export](controlled-export.md).
