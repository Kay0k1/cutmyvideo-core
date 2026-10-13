# Populated 004 to 005 startup: 100,000 sources

One populated schema 004 → 005 upgrade completed in **3.564185755 seconds** on the exact d594 application. Restart, forced lock deadline, rollback/data preservation and labelled recovery controls passed. PostgreSQL reached its **1 GiB memory cap** with `memory.events.max=1476`; OOM and sampled swap were zero. This run shows successful startup on this fixture, with no demonstrated memory headroom.

[Run 38157360994, attempt 1](https://github.com/Kay0k1/cutmyvideo-core/actions/runs/38157360994) used tooling [ba4cb17340ea91848ba9ba4e54ad0d6402ad8720](https://github.com/Kay0k1/cutmyvideo-core/commit/ba4cb17340ea91848ba9ba4e54ad0d6402ad8720) and a fresh runtime checkout of [d594cc4681dc1cab61664cec8c92bdf9a3e9444c](https://github.com/Kay0k1/cutmyvideo-core/commit/d594cc4681dc1cab61664cec8c92bdf9a3e9444c). Both jobs succeeded. Four complete source maps retained all 391 product files and the 173-file application selector at their pinned bytes and modes. The [harness README](../populated004to005/README.md) is the immutable pre-run record; this directory adds the actual results without rewriting that record.

## Method and scope

The fixture installs historical 003 and immutable 004, then commits synthetic data while still at 004: 100,000 sources, 100,000 jobs, 125,002 reservations, 25,000 artifacts, 100,000 metadata-cache rows and 125,005 storage-file rows. Existing counter triggers produce 2,000,065 stored bytes and 2,025,018 reserved bytes. The fixtures include terminal preview-cache jobs and expired, foreign-owner and abandoned exact previews that still pin sources.

Two sequential cases use separate fresh schemas and LOGIN migration owners. Exactly four real `OpenStore(context.Background(), ownedURL)` calls are timed, from immediately before the call until its return. Setup, ANALYZE, owner checks, snapshots and compilation are outside those timings. This exercises the product's **30-second startup budget**; API database 5s and maintenance 10s are separate normal-budget criteria. No workers or maintenance loop run here.

| Call | Elapsed | Result |
| --- | ---: | --- |
| Populated upgrade | 3.564185755 s | Store returned; schema 005 verified |
| Restart | 17.767634 ms | Store returned; state and xmin preserved |
| Deliberately blocked upgrade | 30.025775008 s | Deadline error; no store returned; original 004 preserved |
| Labelled recovery after releasing the lock | 3.349019617 s | Store returned; schema 005 verified |

There is **one first-upgrade capacity observation**. Restart and deadline/recovery are controls, not extra independent samples. The blocked call uses the real 30s startup deadline; its return includes about 26ms of overhead and satisfies the fixture's 29–32s diagnostic window. It is not a wall-time result below 30.000s.

Six read-only repeatable-read snapshots retain ordered UTF-8 JSON projection digests, framing each row with its unsigned 64-bit big-endian length. All old 004 columns are included; source xmin is recorded separately because successful backfill updates sources. The original ledger 1–4 and its checksums/applied-at/xmin survive, while full history 1–5 verifies immutable 005. Restart checks all 005 values and xmin. Pin/accounting oracles pass: 12,500 eligible sources, 100,000 job pins, 50,000 preview pins, eight groups of 12,500 and zero pin mismatches. A source-derived ordinal 5 INSERT witness records the real startup/blocker PIDs, actual LOGIN, waiting RowExclusiveLock, granted SHARE lock and blocking linkage. Original004 survives the blocked transaction before recovery.

Five permission snapshots confirm the authenticated migration owner is neither superuser nor a member of another role, has no database/public/foreign-schema CREATE rights, and owns only its fixture schema and objects. All four calls authenticate as that LOGIN. The admin creates/removes owned objects, observes locks and holds the deliberate SHARE blocker. These checks cover necessary migration-owner DDL rights; they do not validate a restricted runtime DML role.

## Environment and resource observations

GitHub-hosted Ubuntu 24.04, Linux amd64, four host CPUs; Go 1.27.2 with `GOMAXPROCS=2` and `GOMEMLIMIT=128MiB`. The Go memory setting is a soft GC target, not a measured physical RSS cap. PostgreSQL 17.11 uses pinned OCI index `3645570cccdfa447589da9f57dd740faa29b30938e861289a5574b6ca6b03826` (amd64 child and config identities are in the manifest). The owned container has hard 2 CPU, 1GiB memory and 1GiB memory+swap limits, so no extra swap allowance; `shared_buffers=128MB`, `work_mem=4MB`, `max_connections=32`, JIT off and fsync/synchronous_commit on.

Admission before setup, compilation and each case required ≥2 host CPUs, ≥3GiB available RAM and ≥4GiB free disk. All four checks passed; minimum admitted available RAM was 15,231,733,760 bytes and free evidence-disk space 90,510,565,376 bytes. The actual PG filesystem also passed the 4GiB requirement.

| Observation across 25 resource samples | Value |
| --- | ---: |
| PG cgroup peak memory | 1,073,741,824 bytes (exactly 1GiB) |
| Maximum sampled PG current memory | 1,070,485,504 bytes |
| Final `memory.events.max` | 1,476 |
| OOM / OOM-kill events | 0 / 0 |
| Sampled swap | 0 bytes |
| Maximum sampled PG data-volume use | 1,092,783,943 bytes |
| Final PG data-volume use | 870,772,551 bytes |
| Minimum sampled PG filesystem free space | 89,834,737,664 bytes |

The cumulative PG cgroup peak covers setup, snapshots and calls; it is not Go RSS or the isolated peak of one OpenStore call. The five-second disk sampler can miss transient growth. The 2GiB data-volume refusal is an operational bound, not a filesystem quota. The container hitting its memory cap is a concrete limitation for larger inventories or concurrency, despite no OOM in this run.

The hosted wrapper completed in 150.309s; its supervised once process completed in 149.809s, exit 0, with original streams preserved, the PID reaped and all owned groups absent. Inner cleanup, wrapper cleanup and the workflow's always-cleanup passed within their bounds. Both fixture schemas/roles, the exact-owned container/volume, private credentials and injected fixture were removed. Post-cleanup product source maps remained exact. The workflow retained its 2400s outer and 55-minute job limits; product budgets and hard PG caps were unchanged.

## Original evidence and limits

[controls.zip](https://github.com/Kay0k1/cutmyvideo-core/blob/faccc12e510fdb57c112f2df76a6bedb14b1d5fb/benchmarks/2026-10-11-populated004-to005-100k/controls.zip), [startup.zip](https://github.com/Kay0k1/cutmyvideo-core/blob/faccc12e510fdb57c112f2df76a6bedb14b1d5fb/benchmarks/2026-10-11-populated004-to005-100k/startup.zip) and [provider-logs.zip](https://github.com/Kay0k1/cutmyvideo-core/blob/faccc12e510fdb57c112f2df76a6bedb14b1d5fb/benchmarks/2026-10-11-populated004-to005-100k/provider-logs.zip) are byte-exact provider downloads, not reconstructed archives. [manifest.json](https://github.com/Kay0k1/cutmyvideo-core/blob/faccc12e510fdb57c112f2df76a6bedb14b1d5fb/benchmarks/2026-10-11-populated004-to005-100k/manifest.json) binds their SHA-256, sizes, artifact identities, source identities and measured scope. The controls archive contains the original 20 pure controls and 14 focused interruption/cleanup controls, all passing; the latter records two inert child launches and zero denied actual operations. The startup archive contains all four call records, six snapshots, small original ledgers/catalogs/oracles, 25 pressure samples, original exits/streams, ownership journals and cleanup. Provider logs preserve the original workflow steps. Their 252 members passed path/size/CRC checks and byte-catalog validation. Offline validation by the accepted fixture protocol passed once.

Rows were discarded after hashing. No full raw 100k row archive is included, and retained projections/digests do not establish independent whole-row recomputation. One Linux amd64 startup fixture establishes no overall speedup, SLA, worker/maintenance throughput or macOS/Windows capacity guarantee.

The initial dispatch at 6f3685151d05742e390bb0b3828294a17eded4e8 was rejected with HTTP 422 because the workflow used an invalid `runner.temp` job-environment context. It created no workload. The subsequent ba4cb173 commit changes only that workflow context; the one accepted dispatch above produced this evidence. No failed workload or replacement measurement is omitted.

Raw diagnostics were retired from the working tree on 2026-10-13. The [original files](https://github.com/Kay0k1/cutmyvideo-core/tree/faccc12e510fdb57c112f2df76a6bedb14b1d5fb/benchmarks/2026-10-11-populated004-to005-100k) remain in Git history; future runs follow [artifact retention](../../docs/artifacts.md).
