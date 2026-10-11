SOURCE PREPARATION ONLY — NOT AUTHORIZED, NOT RUN

This packet contains the minimal TTL preview SQL candidate already prepared in
core-storage-ttl-preview-probe, an executable one-shot measurement fixture, a
bounded runner and a separate proposed workflow. Nothing here has executed Go,
gofmt, SQL, Docker, network access or CI. The earlier v1 packet, original pilot
artifacts, short-deadline worktree and application CI remain unchanged.

Product candidate

Base: f80cb9bba29f2ba97b37152bbf3388735d4a3173, schema005. Only two product
files differ. The preview EXISTS probe becomes a lateral ordered LIMIT 1 probe
using the existing preview (job_id,id) index opportunity. Preview existence is
tested with id IS NOT NULL, so an empty reservation ID remains a real pin.
The source candidate CTE still materializes at most 200 IDs. Jobs, quarantine,
delete statements, schema, triggers, fence, audit, accounting and unknown-commit
handling are unchanged. The added semantic test checks 200 false-zero preview
pins, two reservations per source, empty ID, historical/foreign reservations,
bounded continuation, a healthy tail with non-preview kinds and byte accounting.
The existing test bodies are preserved byte for byte. Actual correctness and
planner behavior still require the separately authorized execution.

What the proposed single execution measures

There is exactly one product Go test invocation: the 15 pinned source retention,
pin and retention tests plus TestTTLPreviewProbeSavedPlans. No FAIL/SKIP or
missing test can become PASS. The fixture installs unchanged schema005 in a
fresh owned test namespace and uses the existing capacity fixture: 1000 retained
pinned sources, 200 eligible expired sources and 200 real preview reservations.
It preserves the original pilot chronology and runs ANALYZE before timing.
The effective measurement role is the retained ordinary DML role pattern, with
no superuser, schema CREATE, migration-ledger DML or function EXECUTE grants.

Both SQL queries execute in the same database with identical triggers, indexes,
foreign keys and settings. Odd pairs run baseline/candidate; even pairs run
candidate/baseline. All 20 pairs / 40 EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON)
plans are retained; there are no excluded warmups. Every sample acquires the
existing storage fence and uses an acknowledged rollback. The test verifies
that only the 200 eligible sources disappeared inside that transaction, then
checks the same complete 15-table row+xmin digest and exact accounting after
rollback. Each original plan is saved before decoding and bound to its exact
SQL, ordinal, pair and SHA. Timing reports PG Execution Time, client query time
and fence acquisition separately. Nested inclusive plan times must not be added.

This is a query-only experiment. It cannot establish an improvement in the
original full maintenance p50 of 30.111 ms: after the first batch removed the
200 expired sources, later maintenance calls still performed audit work. It
does not repair or relabel the historical scan/bootstrap failure. No speedup,
capacity, platform support or 1.0 release result has been measured in this packet.

Resources, admission and evidence

Admission stays at 3 GiB MemAvailable, 2 GiB free disk and 2 host CPUs. ROOT's
local observation of approximately 2.31 GB available is below this threshold;
the threshold was not lowered and the runner has not been invoked. Owned fresh
PostgreSQL is pinned to the retained 17.11 index/config/amd64 identities, with
1 GiB hard memory, 2 CPUs, swap total equal to memory (no extra swap), 128 pids,
128 MB shared_buffers, 4 MB work_mem, max_connections 32, jit off, fsync and
synchronous_commit on. PG data is a private owned 512 MiB tmpfs. Both paired
queries share that storage; absolute timings do not reproduce the original
pilot's persistent volume and do not test physical power-loss durability.

The runner retains capped original stdout/stderr, exit and explicit reap data,
all actual plans and snapshots, source identities and cleanup evidence. Product
execution is bounded by 420 seconds, the whole runner by 900 seconds and owned
cleanup by a separate 120 seconds. Stream writes are capped at 96 MiB in total,
with 4 MiB reserved for cleanup; each Go stream has a 32 MiB limit. Measurement
files are bounded by 1 MiB each and an operational 16 MiB directory cap checked
while running and after reap, including a failing test exit. A refusal stays FAIL;
there is no automatic retry or timeout increase.

The original 304-file candidate is guarded before and after. The runner creates
its own detached runtime worktree, applies the exact two-file patch, injects
exactly one temporary test file exclusively and checks 304+1 identities. It
removes the unchanged fixture and verifies the original runtime inventory
before deleting its worktree. Ambiguous creation is reconciled using the exact
fresh output/runtime path and Git registration, independent of a success flag.
Container/network cleanup reconciles exact fresh names plus ownership labels,
collects original PG logs, checks schema/role cleanup, no OOM, stops/removes owned
resources and proves label absence. A failure in cleanup prevents overall PASS.
Partial preparation, command failure and unmet admission remain original evidence.

Execution requires separate ROOT authorization after complete source review

ROOT-authorization-template.NOT-AUTHORIZED.json is deliberately false and cannot
authorize anything. A real once authorization must bind runner, plan, source map
and fixture SHAs plus exact local paths or the future hosted branch and commit.
Local mode uses existing modules and an already present pinned image; it never
downloads or pulls. Hosted mode requires explicit approval for one locked public
module preparation and one immutable image pull, then compiles/tests offline.
The prepared Go binary must match the retained SHA548 identity or the run refuses.

ttl-preview-probe.yml.txt is a proposal outside application CI. Its future packet
location is benchmarks/ttlpreviewprobe on a separately reviewed harness branch.
It contains workflow_dispatch only, attempt 1 only, exact branch/head validation,
pinned existing actions, a fresh Ubuntu runner, own fresh PG and always-upload of
original output even on failure, excluding caches/runtime worktrees. It has not
been installed, published, registered or dispatched. ROOT must resolve dispatch
availability for that reviewed branch and issue actual once authority before use.
No production database, production configuration, external database service or
persistent production volume is referenced.

preparation.json records source-only checks. packet-manifest.json freezes every
prepared file except the manifest itself. Actual compilation, test results,
query plans and performance claims remain null until a separately reviewed run.
