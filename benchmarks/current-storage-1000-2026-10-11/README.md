# Current storage pilot, 2026-10-11

**Complete measurements; both workloads FAIL.** The current candidate and its
fixed schema004 baseline registered zero files during every one of the unchanged
32 × 5 ms reconciliation and bootstrap attempts. The other five stages passed
(the baseline audit stage is N/A). Each version durably published all 80 sources,
preserved exact ledger accounting and cleared its source reservations. Namespace,
role and container cleanup passed. `promotion_ready=false`: 10,000 and 100,000
were not executed or admitted by this result.

The original [GitHub run 38138782219, attempt 1](https://github.com/Kay0k1/cutmyvideo-core/actions/runs/38138782219/attempts/1) remains failed.
[summary.json](summary.json) contains selected provenance, all measured
percentiles, original progress curves and verdicts. [raw-manifest.json](raw-manifest.json)
binds byte-identical [before](raw/before-1000.json) and
[after](raw/after-1000.json) reports with full SQL/EXPLAIN plans and every sample.
No new application workload was run while preparing this report.

## Provenance and environment

| Item | Before | After |
|---|---|---|
| Runtime | `63873e935626d51087d0a26b84f1de9a7ed86a28` | `f80cb9bba29f2ba97b37152bbf3388735d4a3173` |
| Schema | 004 | 005 |
| Application inventory SHA-256 | `a1aa7b968c06eaeb075d2cd73f8afbf309a1bc12121d1a3e598ba2561e785f90` | `57f97d213b17785c76079e5a0adaaffcc8a7f2350a911af947a2ddd536a04cea` |
| Original report SHA-256 | `26e893d0f4d516bf324725231bcb859dd7735438ed3a3eba2ff7d2127cb397bc` | `e67c817f6b62b2f7968b9bd39aa779891297213f5d45be44872bc7358ed3a991` |

Harness commit `cde55b3198fd74b58ef3652b175cf919a9f3a315` uses one common frozen
[pilot](../storage005/pilot_test.go.txt) and its [identity](../storage005/identity.json).
The [original scan gates](scan-gates.go.txt) are byte-identical copies from both
runtime revisions; their budgets and acceptance conditions remain unchanged.
The comparison includes all intervening runtime changes and cannot attribute a
measurement to one optimization. It is separate from the historical
[earlier run 38125273317](https://github.com/Kay0k1/cutmyvideo-core/actions/runs/38125273317/attempts/1) and media N20 comparison.

- One Ubuntu 24.04 job, Go 1.27.2 Linux/amd64, ordinary test binaries compiled before
  timing; `GOMAXPROCS=2`, soft `GOMEMLIMIT=128MiB`.
- Same owned PostgreSQL 17.11 container, hard 2 CPU/1 GiB memory limits and no extra swap,
  pinned OCI index `sha256:3645570cccdfa447589da9f57dd740faa29b30938e861289a5574b6ca6b03826`.
  `shared_buffers=128MB`, `work_mem=4MB`, `max_connections=32`, `jit=off`,
  `fsync=on`, `synchronous_commit=on`. Each version gets a fresh namespace;
  database caches are not reset between versions.
- N=1,000 retained rows, 200 eligible expired source controls, actual invoker
  triggers and a generated NOLOGIN runtime role using `SET ROLE`. The scan
  fixture has 1,000 zero-byte entries and 1,024 nine-byte files. This measures
  synthetic metadata/storage work rather than video processing or large-file I/O.
- Stage admission checked at least 2 CPUs, 3 GiB available host RAM and 2 GiB free disk.
  These checks are not continuous resource quotas. PostgreSQL cumulative cgroup
  peaks were 126,701,568 and 145,481,728 bytes; they include prior stages in the
  same container and are not isolated per-stage memory requirements.

## SQL and concurrent operations

Milliseconds, nearest-rank p50/p95 recomputed from all original samples, including
the first. SQL samples execute real statements/triggers and roll back. Server
execution and client round trip have different scopes. Every TTL sample deletes
the same 200 eligible sources and preserves all 1,000 job-pinned controls.

| Operation / scope | n each | p50 before | p50 after | p95 before | p95 after |
|---|---:|---:|---:|---:|---:|
| Source TTL DELETE (server) | 20 | 3.406 | 7.322 | 3.512 | 7.421 |
| Source TTL DELETE (client) | 20 | 3.912 | 7.824 | 4.185 | 7.926 |
| Status-only job UPDATE (server) | 20 | 0.039 | 0.072 | 0.108 | 0.145 |
| Status-only job UPDATE (client) | 20 | 0.215 | 0.259 | 0.296 | 0.325 |
| Job INSERT (server) | 20 | 0.055 | 0.107 | 0.090 | 0.208 |
| Job INSERT (client) | 20 | 0.190 | 0.276 | 0.221 | 0.382 |
| Exact preview INSERT (server) | 20 | 0.064 | 0.127 | 0.137 | 0.202 |
| Exact preview INSERT (client) | 20 | 0.203 | 0.282 | 0.279 | 0.369 |

BEGIN plus advisory-fence acquisition, measured separately from the TTL SQL,
has p50/p95 `0.192/0.266` before and
`0.198/0.225` after.
It does not measure total fence hold: COUNT verification runs under that fence
after the timed SQL. No foreground callers overlap these isolated 20 samples.

Four concurrent admission callers each publish 20 sources while the real runtime
performs 20 maintenance calls and owner-scoped cache reads:

| Operation | n each | p50 before | p50 after | p95 before | p95 after |
|---|---:|---:|---:|---:|---:|
| Source reservation | 80 | 5.982 | 7.130 | 38.659 | 36.149 |
| Durable source publication | 80 | 6.108 | 7.042 | 38.613 | 22.283 |
| Persisted metadata hit | 80 | 0.576 | 0.560 | 5.529 | 3.439 |
| Foreground maintenance | 20 | 6.195 | 30.111 | 38.868 | 88.666 |

All 80 publications per version are present; counters match actual stored/reserved
ledger sums and source reservations end at 0. The schema005 audit probe passes:
ten committed units leave its 129-job/129-preview hot source partial at 128+128,
while repairing its healthy successor. It does not establish complete audit
drain. Held-fence 5 ms/50 ms attempts return deadlines and acquire after release;
caller budgets do not promise exact wall-clock return times.

## Short cycles and later recovery

The criteria are unchanged. Reconciliation must register all 2,024 files or gain
at least `min(remaining_after_cycle8,512)` registrations after cycle 8, and an
unchanged scan must preserve tuple xmin. Bootstrap must finish its real admission,
commit one initialization marker, register all 2,024 files and show short-cycle
progress (or already have all files). Both versions fail the progress criterion.

| Gate | Budget each | Registered before full recovery, before / after | p50 ms before / after | p95 ms before / after | Verdict |
|---|---|---:|---:|---:|---|
| Reconciliation | 32 × 5 ms | 0 / 0 | 5.235 / 5.131 | 5.582 / 5.393 | FAIL / FAIL |
| Bootstrap/admission | 32 × 5 ms | 0 / 0 | 5.437 / 5.714 | 5.808 / 6.001 | FAIL / FAIL |

Each raw progress curve has 32 zeros. All 32 reconciliation calls time out and all 32
bootstrap calls defer per version. A later ordinary-budget completion cannot
replace the failed short-cycle result. The following are **single observations
(n=1 each)**, without percentile or speedup claims:

| Later completion | Before ms | After ms | Result |
|---|---:|---:|---|
| Full reconciliation | 135.053 | 122.598 | One full call each |
| Unchanged reconciliation | 84.471 | 81.847 | One call each; tuple xmin preserved |
| Final bootstrap admission | 179.487 | 119.086 | One retry each; 2,024 files and one marker |

One before/after pair with repeated samples provides no independent-run
confidence interval, significance, cold-cache guarantee, production SLA or
maximum-capacity claim. Several isolated SQL and maintenance timings are higher
in the current candidate; all outcomes remain visible in the tables and raw data.
The pilot migrates fresh schemas before seeding and does not measure populated
004→005 startup/backfill. Fix and recheck short-cycle progress before considering
10,000; the separate 100,000 upgrade fixture remains unexecuted.

## Reproduction

Use the [fixed pilot's resource and source checks](../storage005/README.md), exact
commits above and the common fixture in the harness commit. Keep the original
32 × 5 ms gates, SQL samples, pinned PostgreSQL settings and cleanup checks. Use a
new disposable output directory and retain every failure. The manual workflow's
`maximum_size=1000` runs only that pair; selecting a larger size is not authorized
by this failed report. Private environment files and provider-log contents are
excluded here; both original raw reports were checked against their known field
schema and all string values before their byte-identical publication copies.
