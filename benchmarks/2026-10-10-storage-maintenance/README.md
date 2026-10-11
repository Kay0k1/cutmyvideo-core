# Storage maintenance evidence, 2026-10-10

At 100,000 retained rows per main application table, the revised runtime clears
200 healthy files despite 200 unsafe poison paths and recovers legacy NULL
artifact deadlines within the actual admission budget: 19.158 seconds, 26 calls,
no five-second call timeout. The baseline makes no admission progress over two
minutes. Artifact/job retention SQL and some concurrent tail latencies improve.

**The overall workload gate fails on both versions.** Both artificial
five-millisecond progress experiments register zero files across all 32 calls,
at both sizes. Full directory walks, the 10k legacy warm-up and some admission
latencies become slower. This report does not claim complete capacity acceptance
or improvement for every workload.

The workload measures synthetic retained-row, filesystem-metadata and tiny-file
storage maintenance. It does not measure video processing, large-file I/O, live
providers, transfer throughput, 4K export capacity or production memory bounds.

## Provenance

| Item | Before | After |
|---|---|---|
| Runtime commit | `f9bb68e861cafa2154e3d1e77340b30d3ab52f06` | `718f4a7be2d4f9f3662d518ccfd1da1ec867413e` |
| Runtime-source SHA-256 | `2ca55b30d40781804fa655921ef406da4b36ae1f34a20e14d5e3e72adbd1ffe8` | `f0d2e7daa0cf7288899c8403057c88772d59ef49d5d086eaaa3491c5292047e0` |
| Test binary SHA-256 | `fba8fd08c11f0e41ed7f9f87a29fa9bd2347bfe3de79c1b114887a685c284840` | `6f1516164d84dec985a07a327a80f8d2280eb905c7eb54fa973bc8de19a57d4d` |

The observed before checkout `bf86faf` is a local leaf containing the frozen
harness; runtime sources are unchanged from `f9bb68e`. Reproduction uses the
public runtime and identical harness files, without needing that local leaf.
Both runs use harness SHA-256
`76c4ac7c0deae20083d4599e22d94992c1346985914ce29584ba78336b46115c`.
[summary.json](summary.json) records matching tools/configuration checks.

- Go 1.27.2 Linux/amd64, `GOMAXPROCS=2`, `GOFLAGS=-p=2`, ordinary builds without
  the race detector. The host has four CPU-affinity slots.
- PostgreSQL 17.11 Bookworm, image ID
  `sha256:3645570cccdfa447589da9f57dd740faa29b30938e861289a5574b6ca6b03826`,
  two CPUs, 1 GiB memory limit, 768 MiB disposable data tmpfs, 128 MiB shared
  memory, 32 MiB shared buffers, 8 MiB work memory, 32/128 MiB min/max WAL and
  30-second checkpoints. `fsync`, `full_page_writes`, `synchronous_commit` and JIT
  are enabled. The same owned container runs both measurements, without a cache
  reset between runs; each size gets a fresh isolated schema.
- Two real Store pools, four connections each. Other test/build/database
  workloads were idle during timing. The fixture includes pinned resources,
  expired rows, metadata caches and reservations, plus 10,000/100,000 real
  zero-byte private entries and 1,024 nine-byte files. Zero-byte entries exercise
  names/stat/accounting; they are not representative video files.
- Actual SQL is extracted from runtime sources through the Go parser. Twenty
  `EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON)` samples include the first sample,
  real triggers and rolled-back DML. Percentiles use nearest rank. These are
  repeated samples within one run, with no confidence interval or cold-cache
  claim. MVCC/WAL/cache state and the workload ordering affect results.

## Actual PostgreSQL statements

Times are PostgreSQL execution milliseconds, including triggers. Client
round-trip samples and full per-node rows/loops/buffers are in the raw reports.
Each row measures that version's actual statement. Cache expiry remains an
unbounded DELETE of expired cache entries on both versions: this fixture has
100/1,000 expired cache rows. These timings do not establish a bounded cache
cleanup or throughput for an arbitrarily large expired cache backlog.

| Retained rows | Statement | p50 before, ms | p50 after, ms | p95 before, ms | p95 after, ms |
|---:|---|---:|---:|---:|---:|
| 10,000 | `artifact_retention` | 2.944 | 1.588 | 3.217 | 2.561 |
| 10,000 | `job_retention` | 5.916 | 2.787 | 7.481 | 5.038 |
| 10,000 | `source_retention` | 9.055 | 9.055 | 14.115 | 13.441 |
| 10,000 | `pending_tombstones` | 0.005 | 0.009 | 0.008 | 0.013 |
| 10,000 | `orphan_retention` | 9.348 | 10.550 | 11.108 | 11.936 |
| 10,000 | `cache_retention` | 0.125 | 0.131 | 0.206 | 0.184 |
| 10,000 | `cache_recent_hit` | 0.021 | 0.023 | 0.052 | 0.031 |
| 10,000 | `cache_recent_miss` | 0.016 | 0.016 | 0.029 | 0.022 |
| 10,000 | `storage_admission_totals` | 0.058 | 0.049 | 0.066 | 0.070 |
| 10,000 | `active_source_reservations` | 0.034 | 0.031 | 0.038 | 0.041 |
| 100,000 | `artifact_retention` | 21.034 | 11.894 | 24.290 | 17.532 |
| 100,000 | `job_retention` | 41.952 | 2.795 | 54.390 | 3.434 |
| 100,000 | `source_retention` | 63.002 | 64.568 | 85.977 | 75.944 |
| 100,000 | `pending_tombstones` | 0.006 | 0.010 | 0.009 | 0.011 |
| 100,000 | `orphan_retention` | 15.643 | 12.116 | 23.525 | 20.374 |
| 100,000 | `cache_retention` | 0.772 | 0.816 | 1.069 | 1.355 |
| 100,000 | `cache_recent_hit` | 0.025 | 0.028 | 0.045 | 0.068 |
| 100,000 | `cache_recent_miss` | 0.017 | 0.020 | 0.028 | 0.026 |
| 100,000 | `storage_admission_totals` | 0.021 | 0.026 | 0.035 | 0.047 |
| 100,000 | `active_source_reservations` | 0.094 | 0.105 | 0.134 | 0.128 |

The source-retention p50 is unchanged at 10k and slightly worse at 100k; the
selector still examines the retained/pinned backlog, O(N), before returning at
most 200 candidates. A batch limit does not bound all rows examined. At 10k,
orphan-retention p50 and p95 worsen. The new indexes and bounded candidate/PK
matching remove other full-table work; the exact plans remain the evidence.

The median root-node buffer counters below are PostgreSQL blocks for the four
100k retention statements. Root counts include descendants; summing each node's
buffers again would double count work. Inspect the raw trees for rows per loop,
relation/index access, temporary buffers and trigger timing.

| 100k statement | Shared hits before | Shared hits after | Shared reads before | Shared reads after |
|---|---:|---:|---:|---:|
| `artifact_retention` | 3542 | 6106 | 2611 | 0 |
| `job_retention` | 4180 | 1092 | 1971 | 0 |
| `source_retention` | 3563 | 2347 | 2217 | 2695 |
| `orphan_retention` | 4467 | 4369 | 0 | 0 |

## Parallel Store operations

Four callers each reserve and durably register 20 tiny completed sources while
20 real retention/drain cycles and 80 owner-scoped persisted cache hits execute.
Both versions preserve all 80 publications, clear their reservations and match
stored/reserved counters to the ledger. Values below are client-observed calls,
including pool/global-storage-lock contention, rather than SQL alone.

| Retained rows | Operation | p50 before, ms | p50 after, ms | p95 before, ms | p95 after, ms |
|---:|---|---:|---:|---:|---:|
| 10,000 | `reserve_source` | 13.532 | 10.896 | 24.231 | 18.717 |
| 10,000 | `durable_source_publication` | 9.295 | 11.287 | 21.741 | 38.436 |
| 10,000 | `retention_batch` | 15.075 | 12.579 | 35.810 | 58.049 |
| 10,000 | `persisted_metadata_hit` | 1.827 | 1.104 | 12.088 | 13.360 |
| 100,000 | `reserve_source` | 7.912 | 35.784 | 104.255 | 105.125 |
| 100,000 | `durable_source_publication` | 92.940 | 56.896 | 186.386 | 115.817 |
| 100,000 | `retention_batch` | 96.689 | 86.454 | 203.630 | 156.716 |
| 100,000 | `persisted_metadata_hit` | 1.649 | 1.235 | 152.222 | 99.829 |

The 100k reservation p50 worsens substantially while its p95 is nearly unchanged.
At 10k, publication and retention p95 also worsen. At 100k, publication and cache
p95 improve but remain far above their isolated SQL execution time. Sampled
advisory-lock waiters remain present: up to four at 100k on both versions (five
at 10k after). The five-millisecond waiter sampler can miss brief waits and does
not isolate every contribution to call latency. This is not a lock-free claim.

## File progress and real admission budgets

Poison deletion is a real safety/progress workload: three drain cycles preserve
all 200 unsafe directory paths and their 1,800-byte ledger charge. The baseline
leaves all 200 healthy files and rows; after removes them all, including physical
files. These are synthetic tiny files.

The artificial short-cycle experiment is deliberately separate from service
budgets: 32 calls with five milliseconds each register zero files on both
versions at both sizes. Both short reconciliation and short bootstrap/admission
gates fail. Five milliseconds is below the usable protocol/scan transaction
budget observed here; it is not a supported service SLA. Subsequent normal-budget
passes complete and preserve unchanged tuple `xmin`, but do not erase the failed
stress curves or overall nonzero workload exit.

Single measured completion times below include all normal-budget retry calls;
they are not p50/p95 of independent full scans. Public reconciliation has a
60-second aggregate budget; bootstrap retries have two minutes.

| Retained rows | Operation | Before, seconds | After, seconds | After calls |
|---:|---|---:|---:|---:|
| 10,000 | Full public reconciliation | 0.892 | 1.369 | 1 |
| 10,000 | Unchanged public reconciliation | 0.316 | 0.899 | 1 |
| 10,000 | Bootstrap completion | 0.890 | 1.275 | 1 |
| 100,000 | Full public reconciliation | 9.643 | 16.442 | 1 |
| 100,000 | Unchanged public reconciliation | 3.222 | 11.780 | 1 |
| 100,000 | Bootstrap completion | 10.912 | 16.180 | 5 |

Full and unchanged scans become slower. Resuming verifies the ordinal prefix and
replays earlier names, O(prefix) for each resumed batch; interruption frequency
and committed batching affect total work. It does not provide constant-time
resume or make a single complete directory walk faster. No file descriptor is
kept across calls. Source-retention O(N) and prefix replay remain concrete scale
limits for larger retained backlogs.

Legacy recovery starts with exactly 10,000/100,000 artifact deadlines set to NULL
and cold owned bootstrap metadata. Each real `ReserveSource` has the service's
five-second database budget; retries share a two-minute aggregate budget. The
harness does not perform the deadline backfill itself.

| Legacy deadlines | Before | After |
|---:|---|---|
| 10,000 | Pass: one call, 0.771 s total; max call 0.764 s | Pass: three calls, 1.454 s total; max call 0.970 s |
| 100,000 | Fail: 24 timeouts, 120.086 s, 100,000 NULL deadlines remain, no marker | Pass: 26 calls, 19.158 s, no timeouts; max call 3.275 s; zero NULL deadlines, marker committed |

The 10k warm-up worsens. At 100k after, 25 calls return retryable initialization
and the final call opens admission; do not treat all retries as successful
admissions. Both versions maintain correct byte counters. The first four after
calls advance physical bootstrap before deadline counts decrease. Full curves,
per-call latencies and invariants are preserved in the raw reports.

## Memory and fixture size

Memory scopes differ. Go heap, `Sys` and RSS belong to the test process, including
the harness/raw plans. PostgreSQL cgroup readings include its processes, charged
cache and data tmpfs. Host cgroup readings may include unrelated processes. The
sampled `memory.current` peak is run-local; `memory.peak` can predate a run and is
unavailable on this kernel. These values are not interchangeable.

| Retained rows | Go test metric | Before, MiB | After, MiB |
|---:|---|---:|---:|
| 10,000 | Peak live heap | 5.22 | 5.56 |
| 10,000 | Peak Go Sys | 17.35 | 17.60 |
| 10,000 | Sampled peak process RSS | 23.14 | 23.98 |
| 10,000 | Total allocated over workload | 113.03 | 160.26 |
| 100,000 | Peak live heap | 13.17 | 9.64 |
| 100,000 | Peak Go Sys | 25.60 | 25.60 |
| 100,000 | Sampled peak process RSS | 32.89 | 28.36 |
| 100,000 | Total allocated over workload | 4366.58 | 3261.65 |

PostgreSQL's sampled container peak across both sizes is 778.27 → 761.20 MiB. This is a container/cache/tmpfs measurement, not Go RSS. The whole run lasts 183.705 → 113.189 seconds, including the baseline's deliberate two-minute failed recovery. Total Go allocation is cumulative churn, not simultaneous resident memory.

| Retained rows | Owned relations before workload, MiB (before / after) | Owned relations after workload, MiB (before / after) |
|---:|---:|---:|
| 10,000 | 24.56 / 28.81 | 39.14 / 44.72 |
| 100,000 | 242.27 / 283.97 | 385.66 / 419.59 |

Relations include indexes and grow with observations/MVCC. WAL/cache space is
additional. Zero-byte physical entries mostly exercise filesystem metadata;
these measurements cannot predict memory or I/O for real retained media.

## Reproduction and raw outputs

See [the workload guide](../../docs/storage-capacity.md) for fixture/environment
setup. Use disposable PostgreSQL, sufficient scratch space and matching limits;
never run this workload against production. Reproduce the baseline with public
runtime `f9bb68e` and only the two frozen harness files from public `718f4a7`:

```sh
git worktree add ../storage-before f9bb68e861cafa2154e3d1e77340b30d3ab52f06
git show 718f4a7be2d4f9f3662d518ccfd1da1ec867413e:scripts/storage-capacity.py > ../storage-before/scripts/storage-capacity.py
git show 718f4a7be2d4f9f3662d518ccfd1da1ec867413e:internal/app/storage_capacity_test.go > ../storage-before/internal/app/storage_capacity_test.go
cd ../storage-before
export TEST_DATABASE_URL='postgres://cutmy_test:test-only@127.0.0.1:55440/cutmy_storage_test?sslmode=disable'
export GOMAXPROCS=2 GOFLAGS=-p=2 GOTOOLCHAIN=local
export TMPDIR=/path/to/scratch
python3 scripts/storage-capacity.py --output /path/to/new-before --sizes 10000,100000 --samples 20 --pg-container your-owned-postgres
```

Use Go 1.27.2 and the PostgreSQL configuration above. The historical Makefile has
no capacity target, so the baseline invokes the runner directly. Verify runtime
and harness digests in `environment.json` before comparing. For after, use a
clean `718f4a7` checkout and the same arguments with a new empty output directory.
Reproduction may produce different timings; never rerun solely to select a nicer
sample. Keep all failed stress gates and regressions. Do not compare race-detector
measurements to ordinary builds.

The `baseline/` and `after/` folders each contain `rows-10000.json.gz`,
`rows-100000.json.gz`, `environment.json.gz`, `workload.log.gz` and `build.log.gz`.
These are unmodified complete runner outputs, with no credentials or connection
URL. Gzip uses an empty filename and `mtime=0`.
[raw-manifest.json](raw-manifest.json) records compressed/uncompressed SHA-256 and
lengths; [summary.json](summary.json) preserves compact provenance, matching
configuration checks, all query percentiles/root buffers, workload curves,
failures and separate memory scopes. For example:

```sh
gzip -dc baseline/rows-100000.json.gz
```

Both runs complete setup and both sizes, then exit 1 because workload assertions
fail. That is complete failing evidence, unlike an exhausted/failed fixture
setup. Earlier smaller-environment and experimental runtime diagnostics are
excluded from this public comparison. No production service, real recording or
live provider was used, and owned namespaces/files were removed after each run.
