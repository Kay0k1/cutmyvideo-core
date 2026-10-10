# Upgrade and recovery acceptance

Current source has a required, executable recovery gate in addition to unit,
race and HTTP contract tests. Run it before releasing a database or persistent
media change. The gate starts actual CLI server/worker processes; it uses real
FFmpeg, PostgreSQL `pg_dump`/`pg_restore`, synthetic media and separate anonymous
owner sessions. It does not use production services or live providers.

## Run the gate

Use Linux, Go 1.27.2, Python 3.10 or newer, Git, FFmpeg/ffprobe and an isolated
PostgreSQL server with permission to create and drop databases. Fetch repository
history containing immutable v0.2.1 commit
`f2be62c42a6a0d3d7a017b40bd683596bc578b95`; a shallow current-only checkout is
insufficient. Historical source is unpacked into a separate temporary directory;
the working branch is never changed. Both binaries are built with `-mod=readonly`.

```sh
export RECOVERY_DATABASE_URL='postgres://cutmy_test:test-only-password@127.0.0.1:55439/cutmy_test?sslmode=disable'
export RECOVERY_WORK_DIR=/dev/shm/cutmy-recovery
export RECOVERY_REPORT_PATH=/dev/shm/cutmy-recovery-verification.json
make recovery-check
```

Supply only a disposable, local test PostgreSQL. The harness creates uniquely
named `cutmy_recovery_*` databases and removes its databases on success or failure;
it leaves the database named in the supplied connection string intact.

The `pg_dump` client must be at least as new as the server. When the server is an
isolated Docker container, use its matching clients rather than older host tools:

```sh
export RECOVERY_PG_CONTAINER=cutmy-test-postgres
make recovery-check
```

The API connects through the host port in `RECOVERY_DATABASE_URL`. The PostgreSQL
clients run inside this explicitly named container and connect to its local
port 5432 using the same test role. Docker access is required for this option.
CI supplies its dedicated PostgreSQL service container, with repository history
available and the work directory under the runner's temporary directory.

`RECOVERY_REPORT_PATH` saves a small JSON report outside the disposable directory.
It contains tool/source versions, binary and backup hashes, verified checks and
output probes. Credentials and session cookies are absent from this report.
Successful runs remove temporary media, binaries and backups. To inspect them
locally, run `python3 scripts/recovery-acceptance.py --keep-work-dir`; logs and
synthetic backups remain private in the printed directory. Failed runs retain
their work directory for diagnosis. Treat these files as private: a database
dump contains session owner hashes and process error logs can contain connection
details. All commands fail rather than silently skip missing prerequisites.

Two recovery phases wait for the real 45-second worker lease to expire; the
harness never edits lease deadlines or fabricates a completed item. Expect about
two minutes after compilation.

## What is accepted

1. The published v0.2.1 binary uploads synthetic H.264/AAC media for two separate
   sessions. It verifies ownership, source bytes, owner-scoped idempotency and
   queued jobs through the real HTTP API.
2. A two-item job publishes its first result. An acceptance-only FFmpeg wrapper
   paces and pauses the second real FFmpeg process, then the worker receives
   SIGTERM. The current binary opens that database. Existing source, job, item,
   artifact, lease and storage rows must remain identical during migration.
   Queued work runs and the interrupted lease expires naturally; the completed
   item keeps its identifier and exact downloaded bytes.
3. The current worker starts another partially completed job and receives
   SIGKILL. The harness then terminates only its known tool process group,
   modelling loss of the whole worker container. This distinction matters:
   killing a parent process alone does not guarantee its child tools exit.
   A separate real job is also interrupted with SIGKILL during its first item,
   before any artifact is registered or returned. Both unfinished leases remain
   intact; another submitted job is still queued when backup begins.
4. With both API and worker stopped and their owned tools reaped, the harness
   makes an actual custom-format PostgreSQL dump and a complete media archive.
   It restores the dump into a freshly created database and the media into a
   newly created physical directory. Database rows and all file hashes must
   match the coordinated snapshot.
5. Current processes open the restored state using the original session
   cookies. Idempotency, ownership, queued work, the partial result and both
   unfinished leases survive. The worker resumes the job with no completed
   items and the remaining partial-batch item without duplicating artifacts or
   replacing acknowledged results. Restored downloads must match their byte
   counts, support byte ranges and pass independent ffprobe and FFmpeg decode.
6. A fresh upload and new MP3/MP4 copy exports must work after restore. All final
   jobs must succeed, rather than merely reach a terminal state. Registered file
   sizes and stored/reserved counters must agree after recovery.

A separate disposable clone also receives an unknown future schema marker. The
actual current CLI must refuse startup with `schema_incompatible` without changing
application data, version markers or its ordered migration ledger.

## Operator boundary

The production procedure remains the [coordinated backup and fresh-project
restore](operations.md): stop **every** writer sharing the database/media,
dump the database, archive its matching media, verify both commands, then resume
services. A database-only snapshot or copying files while a worker publishes
results cannot establish this boundary. Maintenance processes and additional
replicas are writers too; the harness owns and stops all of its writer processes.

Persistent database rows currently contain absolute media paths. Restore into a
fresh volume mounted at the same canonical `DATA_DIR` (the provided Compose setup
uses `/data`). The gate replaces the physical directory while preserving that
canonical path. Changing it requires a separately planned offline path migration;
copying the files into an arbitrary new native directory is insufficient. It
does not silently rewrite paths to make the acceptance test pass.

Retain the exact configuration needed to interpret the backup, keep its database
and media archives together, and protect them like user media. Anonymous session
cookies are bearer capabilities: users need their original cookie to access
their restored sources and jobs. The core currently has no named-account system.

This gate covers application/process interruption, coordinated logical backup,
migration and restore. It does not establish physical power-loss durability,
filesystem/controller guarantees, online backups, arbitrary schema downgrade,
native Windows/macOS operation or continued accessibility of an external provider.
