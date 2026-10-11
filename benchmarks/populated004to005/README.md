# Populated 004 to 005 startup fixture

The accepted fixture passed all 20 pure controls and one offline compile-only
check; the compiled binary was not executed. No 100k run, timing, digest or
capacity acceptance exists. This uses the exact d594 application (SOURCE391, APP173) after accepted
normal-budget 1000 and 10000 measurements. Source review and once authorization
remain required. The previous 18 pure controls passed only for the old f80
fixture. The adapted 20 controls passed once and compilation passed in 42.18s;
this 100k workload remains NOT_RUN. These controls do not measure performance.
The application, immutable migration SQL and existing gates are unchanged.

The Go .txt test is injected into a separate fresh d594 checkout. It installs
captured historical003, immutable004 SQL and the real marker/ledger checksum,
then commits100k sources,100k jobs,125002 reservations,25k artifacts and100k
caches while still at004. Existing counter triggers must produce2000065 stored
and2025018 reserved bytes. Terminal preview_cache jobs count; reservations of
that kind do not. Expired, foreign-owner and abandoned exact previews still pin.
Truthful eligible/job/preview totals are12500/100000/50000.

Exactly four calls to OpenStore(context.Background(), ownedURL) are allowed:
first populated upgrade, restart, blocked upgrade and labelled recovery.
The monotonic timer starts immediately before OpenStore and stops at return.
Success requires the actual30s startup cap. A45s process safeguard preserves
incomplete failure and does not replace the caller budget.

The deadline case holds SHARE on its owned ledger and requires actual startup
and blocker PIDs, exact INSERT query, waiting RowExclusiveLock, granted ShareLock
and pg_blocking_pids linkage. Ordinal5 is source-derived from exact runtime and
verified pre-call ledger1–4; SQL bind values are not claimed captured.
Keep the lock until the real deadline return, bounded29s–32s diagnostically.
This diagnostic return bound adds no time to the production startup30s budget.
Exact committed004 catalog/rows/source xmin/ledger must survive before labelled
recovery is allowed. Uncertain acknowledgement is inspected without edits and
remains an original failed call, even if committed005 is observed.

Each sequential case has a fresh LOGIN migration owner and its own schema.
The fixture authenticates as that role for setup, the four real OpenStore calls,
restart and rollback inspection. Actual current_user/session_user and catalog
rights must show NOSUPERUSER/NOCREATEDB/NOCREATEROLE/NOINHERIT/NOREPLICATION/
NOBYPASSRLS, no memberships, exactly one owned schema, no CREATE on the database,
public or another user schema, and ownership of the fixture objects. The blocked
INSERT witness also records the actual startup login. The admin login creates and removes owned roles/schemas, observes locks,
and holds the deliberate deadline-test SHARE lock. All four real OpenStore
calls authenticate as the migration owner. This validates a
migration owner's necessary DDL rights; it does not validate a restricted runtime
DML role. Owner checks and setup remain outside the product startup timer.

API database5s and maintenance10s remain the normal-budget profile's criteria.
This separate fixture exercises startup30s only; no workers or maintenance loop
run, and the old5ms experiment is neither repeated nor relabelled.

Six read-only repeatable-read snapshots stream ordered UTF8 JSON row projections
into SHA256, framing every row with unsigned64-bit big-endian length. Rows are
discarded after hashing. Preserve every old004 column; source xmin is separate
because successful backfill updates sources. After005, retained history selects
old1–4 while the full1–5 history is checked independently. Restart compares all005
values and xmin. Small original ledgers, catalogs, count/accounting/pin replies
and lock/timing evidence remain. There is no full raw100k row archive or claim of
independent whole-row recomputation. Setup/ANALYZE, snapshots and compilation are
outside startup timing.
Read-only snapshots validate the same immutable ledger prefix with ordered
SELECTs; the production ledger helper's SHARE lock is used only during setup.

The specialized runner owns a new labelled container/volume, loopback-only port,
private0600 credentials and one separately injected file. PG17.11 amd64 is pinned
by OCI index with hard2CPU/1GiB memory/1GiB memory+swap,128MB shared_buffers,4MB
work_mem,32connections, JIT off and fsync/synchronous_commit on. Go1.27.2 uses
GOMAXPROCS2/GOMEMLIMIT128MiB. Fresh admission before setup, compilation and both
sequential cases requires2CPUs/3GiB availableRAM/4GiB freedisk. Actual swap/OOM, memory current/peak above1GiB,
or PG data above2GiB is refusal. The4GiB free-space admission is checked on
both the evidence filesystem and the actual PG volume filesystem. Required
samples before either case and after the operation preserve current memory,
cumulative cgroup peak and OOM counters; periodic samples are also retained. The five-second volume sampler may overshoot;
this is an operational bound, not a filesystem quota.

Seed/install/ANALYZE are bounded120s; each snapshot90s; fixture20min/capture21min.
Shared stdout/stderr bounds are48MiB each, with2MiB reserved for cleanup.
Metadata shares32MiB:31MiB fixture+1MiB runner. Row/ledger/catalog caps are
32KiB/16KiB/1MiB. Overflow means incomplete/FAIL, never silent truncation.
Exact-owned container/volume/private-file and schema/role cleanup has a30s
total diagnostic bound per runner/fixture cleanup and is required for PASS.
The outer caller must cap this once runner at2400s, preserve its first streams
and exit, and supervise owned process groups; the runner itself does not impose
a whole-process wall deadline.

Accepted fixture controls (20 passed locally; CI runs its own copies):

    python3 -I -B -m unittest discover -s benchmarks/populated004to005 -p protocol_test.py -v

The source contains20 meaningful synthetic controls denying child/network before
candidate imports. These validate failure/evidence contracts, not PG performance.
The runner needs explicit fresh checkout/output/private paths, frozen391+6 source
and ROOT once authorization pinning source/fixture/runner/d594 and accepted actual
1000/10k review identities. The earlier normal-budget pilot did not admit100k;
the new registered workflow requires separate reviewed once dispatch.

    python3 -I -B benchmarks/populated004to005/run_once.py \
      --authorization /owned/reviewed/once-authorization.json \
      --freeze /owned/reviewed/source-freeze.json \
      --checkout /owned/fresh-d594 \
      --output /owned/new100k-evidence \
      --private-root /owned/new100k-private

Preserve the first original exit/streams, four calls, six snapshots, refusals and
cleanup. No retries, larger limits or replacement startup sample are authorized.

Hosted preparation uses the existing registered storage005-capacity workflow on
an isolated tooling branch. A dispatch supplies the reviewed tooling commit;
the product checkout remains exact d594. The committed397-file freeze and fixed
ROOT normal1000/10000 receipt bytes define the once authority; only absolute
output paths and GitHub run context are resolved at runtime. The wrapper and
workflow are pinned separately. The once runner now journals its owned names
before fixture injection or Docker resource creation and uses sticky signal refusal while preserving command exits
and streams. An outer2400s supervisor reserves75s for termination/cleanup,
tracks PID/start-time/process groups with a private Linux child subreaper,
reaps each proven adopted child separately, and journals identities for fallback
cleanup. Baseline unrelated siblings and reused PIDs are excluded. Cleanup
preserves preexisting paths and requires a valid resource journal before removing
private files or the injected fixture. A recorded outer attempt without a PID
journal is incomplete ownership evidence: cleanup fails and preserves private
files and the injected fixture.
The workflow always performs fallback cleanup and preserves original artifacts.
Fourteen focused interruption/cleanup controls include exactly one inert
Python process-tree case, with a detached descendant and an unrelated sibling;
no SQL, Go, Docker or network operation is part of those controls. They and hosted
execution remain NOT_RUN until separately reviewed. The original 20 pure
controls run in a separate Python process so their execution-denying audit hook
remains intact. Incomplete inner summaries, sticky interruption or supervisor
elapsed time beyond its allocated wall deadline cannot produce PASS. The job has55min; product budgets and hard PG caps are unchanged.
