# Source retention and counter recovery

All jobs pin their source until their row is deleted, including succeeded,
failed, cancelled and waiting jobs. A reservation whose exact kind is `preview`
also pins its `job_id` source until that row is deleted or changes membership.
Its expiry, owner, abandonment time and token do not relax the pin. A
`preview_cache` row is not that pin. Explicit owner deletion keeps its existing
contract: active jobs/preview pins refuse deletion; terminal jobs and their
artifacts can be removed transactionally with the source.

Migration 5 stores these two derived counts and a membership revision on each
source. Invoker statement triggers account for actual inserted/deleted rows,
idempotent inserts, job/source-ID transfers and preview kind/ID transfers.
Status/lease-only job updates do not update source counts or revisions. Statement
fences precede membership/FK row locks and use the existing storage advisory
lock. Preview cancellation through a plain database call follows the same rules.
Source birth/ID movement includes earlier preview rows with that source ID;
reservations intentionally have no source foreign key. Birth/ID-change seeding
uses indexed full counts for that source and is outside the 128-child audit bound;
it retains the caller's database deadline. Transaction rollback
rolls back pin counts, byte counters and membership together.

## Normal TTL work

The eligible index contains valid zero-count sources ordered by `(created_at,id)`.
Each source cleanup visits at most 200 candidates before probing actual jobs and
exact-preview references through their source/ID indexes. It checks the actual
references under the storage fence before deleting. Physical paths become
charged tombstones in the same transaction and are drained only after an
acknowledged commit. An unknown commit is retried from the durable database state.
The artifact selector retains its existing global oldest-eligible ordering.

A bad zero counter cannot permit deletion. The bounded batch marks that source's
counts invalid and excludes it from the eligible index; a continuation can reach
healthy candidates behind the bad prefix on the next batch. Healthy candidates
do not wait for a complete pass over all retained/pinned sources. Bounds describe
stage row work, not a five-millisecond latency guarantee; query plans and
foreground admission/trigger costs still require measurements for this code.

## Drift audits and limits

After media cleanup commits, a separate audit has at most 250 milliseconds and
one-sixteenth of the remaining retention deadline. It visits at most 16 source
units. Each unit takes/releases the storage fence in its own transaction, reads
at most 128 job IDs and 128 exact-preview IDs plus one lookahead each, and commits
its source/child cursors. It never counts a source's entire hot child set in one
maintenance transaction. The global `(created_at,id)` cursor has a fixed highwater
for each sweep and advances even when a source's children remain partial. Backdated
or new rows behind a cursor are covered after wrap.

Child counts survive restart, but are used only at their captured membership
revision. A pin mutation resets that source's incomplete audit on its next visit;
status/heartbeat changes do not. Only a completed stable child pass restores valid
counts. A completed global sweep does not imply that every partial source is
complete. One continuously changing popular source can keep resetting its own
partial audit; other sources still receive turns. Completion requires a stable
membership interval long enough to finish that source's chunks.

Expiry of the audit's own slice leaves work pending while the parent maintenance
request is still live. It preserves earlier acknowledged repairs and returns
without an error only when that expiry is the entire cause of the failed unit.
It does not report a completed sweep or count an unacknowledged commit as a
repair. The next unit reads durable progress again, including when a commit
acknowledgement was lost. Parent cancellation, database failures and mixed errors
still propagate; committed media paths remain available for deletion.

An underflow caused by bad counts quarantines the source without blocking a
legitimate membership deletion. Positive-count drift requires the independent
all-source audit: it cannot be discovered through the eligible index. With eight
full batches per five-minute worker cycle the audit can visit at most 128 sources
per cycle; revisiting 100,000 sources takes about 781 cycles, roughly 65 hours,
and deadlines/lock failures can extend that. A hot source may need several full
sweeps. This is a corruption-recovery bound, not prompt TTL for corrupted state.
Normal truthful-count selection bypasses this audit lag.

## Diagnosis and operator recovery

`source_retention_audit` records the all-source cursor/highwater.
`source_pin_audit_progress` contains incomplete child passes, their revision,
child cursors/counts and completion flags. Read these records to distinguish a
sweep wrap from unfinished hot-source work. `source pin audit pending` reports
parent cancellation or an audit database error while already committed paths
remain drainable. Expiry of only the audit's own slice does not emit this warning.
The tables have the existing application DML privilege model. Runtime writes can
corrupt derived counts/progress; they are never an authorization boundary. Final
reference checks and subsequent independent sweeps remain required.

Use the same compatible migration-5 binary and ordinary `cutmy maintenance`
entry point to advance bounded audits. Additional operator-scheduled calls reduce
waiting between cycles but retain per-call deadlines and the same storage fence;
coordinate them with foreground work and stop on database/resource errors. Do not
set arbitrary counts to zero, delete pin rows, or treat a partial cursor as proof
that repair completed. Verify the affected source's actual jobs/exact-preview rows
and counters after completed stable audits. If the required stable interval or
recovery time is unavailable, stop writers and recover from a verified coordinated
backup; no automatic full-table repair runs under the runtime fence.

PostgreSQL and native regression checks cover continuation, acknowledged repairs,
parent cancellation and unknown commit recovery. They do not measure storage
capacity or establish a latency guarantee. The 32×5ms reconciliation/bootstrap
stress requirements stay unchanged and are separate from source TTL; the latest
capacity run must be evaluated against its own exact runtime and harness.
Migration startup/backfill and rollback rules are in [migrations](migrations.md).
