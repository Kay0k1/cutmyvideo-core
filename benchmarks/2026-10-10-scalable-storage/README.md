# Scalable storage evidence, 2026-10-10

**complete measurements; overall workload gate failed.**

Synthetic retained database rows, real zero-byte filesystem entries and tiny files. Measures storage maintenance and metadata expiry; excludes FFmpeg, large-media I/O, live providers, network transfer and 4K/export capacity.

At 100,000 retained rows per main table, full reconciliation improves from
16.173 to 13.107 seconds (19.0%); an unchanged pass improves from 11.555 to
8.678 seconds (24.9%). Bootstrap completion improves from 15.965 to 12.979
seconds (18.7%). Stable directory readers avoid repeating committed prefixes
within a call. New calls and restarts still verify O(prefix) names.

The cache-backlog workload now removes exactly 200 entries per public call,
1,600 across eight calls, preserving all dedicated sources and 23 fresh cache
controls. The baseline removes all 100,000 expired entries in its first call.
This establishes a deletion bound and progress, rather than a throughput win.

**The overall workload gate fails on both versions.** Both five-millisecond
experiments make zero registration progress over all 32 calls at both sizes.
Ordinary-budget completion, poison deletion, concurrent publication/accounting
and legacy initialization pass, but do not replace those failed stress gates.
Isolated cache expiry SQL becomes slower: p50 0.083 → 1.492 ms at 10k and
0.734 → 6.709 ms at 100k. The 100k legacy warm-up also worsens, 19.430 →
19.974 seconds. Concurrent 10k reservation p95 worsens, 24.961 → 40.475 ms;
100k publication p50 worsens, 39.661 → 54.623 ms. Every measured query and
contention percentile remains below, including unchanged SQL with worse tails.

At 100k, cumulative Go allocation falls about 65%, while sampled heap and RSS
peaks rise about 67% and 23%. These include the test harness and raw plans;
allocation churn, live heap, RSS and database/cache memory are different scopes.

## Provenance

| Item | Before | After |
|---|---|---|
| Observed checkout | `91192c8dd26ac545515d93b50f63d50db121deba` | `b1d2e486cd547e90f4f0fe6b3c61e3e5771fc059` |
| Runtime revision | `91192c8dd26ac545515d93b50f63d50db121deba` | `b1d2e486cd547e90f4f0fe6b3c61e3e5771fc059` |
| Runtime-source SHA-256 | `f0d2e7daa0cf7288899c8403057c88772d59ef49d5d086eaaa3491c5292047e0` | `eedca484c0a43cff8ae835e6e9da3291d95e8904f24b800961a1714b2ba1d52e` |
| Test binary SHA-256 | `875fd6ef47a35d720e2dde359450af6d0a4af78949d4140a8c3f3a4918149261` | `c56a03885847273d42930cd6db852620cb53058bf5cbcaad3da59c82a42d7a13` |
| Harness SHA-256 | `79cd6b8e69df090d96ad1b492f209623b4e5892324ad0137d92133127aad8c1a` | `79cd6b8e69df090d96ad1b492f209623b4e5892324ad0137d92133127aad8c1a` |
| Go | `go version go1.27.2 linux/amd64` | `go version go1.27.2 linux/amd64` |

Observed baseline checkout may include the identical measurement harness on the immutable baseline runtime. The runtime and harness digests are independently checked against the listed revisions. Reproduce with that runtime and copy only the two frozen harness files from the after revision when needed.

Matching tools/configuration are recorded in [summary.json](summary.json). Exact SQL, full plan trees, progress curves, environment/cgroup samples and workload logs are preserved byte-for-byte in the gzip files listed by [raw-manifest.json](raw-manifest.json). Each input size uses an isolated disposable schema. Database caches/WAL/MVCC state and workload ordering affect results; the container was not reset between compared runs.

## Environment and interpretation

- Go 1.27.2, Linux/amd64, ordinary builds, `GOMAXPROCS=2`, `GOFLAGS=-p=2`;
  four host CPU-affinity slots. Task test/build workloads were idle during both
  timing windows. Each size uses fresh schemas and two four-connection Store
  pools. No database-cache reset occurs between the two runs.
- The same PostgreSQL 17.11 Bookworm container/image runs both measurements:
  two CPUs, 1 GiB memory, 768 MiB disposable data tmpfs, 128 MiB shared memory,
  32 MiB shared buffers, 8 MiB work memory, 32/128 MiB min/max WAL, 30-second
  checkpoints; fsync, synchronous_commit, full_page_writes and JIT enabled.
  Image ID and the independently matched settings are in `summary.json`.
- Twenty actual SQL plan samples include the first sample, real triggers and
  rolled-back DML. Percentiles use nearest rank; one before/after run provides
  no independent-run confidence interval or cold-cache claim. At 10k the normal
  plan fixture has 100 expired cache rows on both versions; at 100k it has
  1,000 on before and the revised statement processes at most 200. The separate
  backlog workload starts with the exact 10k/100k expired count.
- Zero-byte entries and 1,024 nine-byte files exercise directory metadata and
  accounting. The final cache stage adds 10k/100k fresh source rows plus 23
  controls, after the earlier plans/contention measurements; final relation
  bytes and process memory include this additional fixture. Source candidate
  selection remains O(N). A long locked cache prefix can require examining many
  rows despite SKIP LOCKED; the statement budget and deletion bound do not make
  every selection constant-cost.

## Actual SQL execution

Milliseconds, nearest-rank percentiles from every recorded sample. PostgreSQL execution includes triggers. Full SQL, plans and client samples are in raw archives.

| Retained rows | Query | p50 before | p50 after | p95 before | p95 after |
|---:|---|---:|---:|---:|---:|
| 10000 | `artifact_retention` | 1.237 | 1.310 | 1.433 | 2.175 |
| 10000 | `job_retention` | 3.020 | 2.757 | 3.894 | 4.337 |
| 10000 | `source_retention` | 8.698 | 8.280 | 10.357 | 10.447 |
| 10000 | `pending_tombstones` | 0.007 | 0.009 | 0.011 | 0.011 |
| 10000 | `orphan_retention` | 10.298 | 10.670 | 21.685 | 12.879 |
| 10000 | `cache_retention` | 0.083 | 1.492 | 0.157 | 1.705 |
| 10000 | `cache_recent_hit` | 0.022 | 0.023 | 0.041 | 0.038 |
| 10000 | `cache_recent_miss` | 0.015 | 0.015 | 0.023 | 0.022 |
| 10000 | `storage_admission_totals` | 0.047 | 0.038 | 0.101 | 0.065 |
| 10000 | `active_source_reservations` | 0.026 | 0.030 | 0.040 | 0.071 |
| 100000 | `artifact_retention` | 10.313 | 10.499 | 18.752 | 13.306 |
| 100000 | `job_retention` | 2.807 | 2.897 | 4.383 | 4.645 |
| 100000 | `source_retention` | 63.178 | 64.602 | 68.590 | 90.061 |
| 100000 | `pending_tombstones` | 0.008 | 0.007 | 0.015 | 0.010 |
| 100000 | `orphan_retention` | 14.416 | 12.372 | 20.762 | 13.776 |
| 100000 | `cache_retention` | 0.734 | 6.709 | 0.829 | 11.243 |
| 100000 | `cache_recent_hit` | 0.026 | 0.027 | 0.038 | 0.041 |
| 100000 | `cache_recent_miss` | 0.016 | 0.018 | 0.035 | 0.022 |
| 100000 | `storage_admission_totals` | 0.019 | 0.022 | 0.035 | 0.046 |
| 100000 | `active_source_reservations` | 0.067 | 0.078 | 0.103 | 0.116 |

A changed bounded statement may process fewer rows than its predecessor; compare affected work and raw plans as well as time. Repeated samples do not provide an independent-run confidence interval or a cold-cache guarantee.

## Root buffers and rows

Root buffers already include descendants. Counts are medians across the recorded EXPLAIN samples, in PostgreSQL blocks; do not sum them again.

| Rows | Query | Version | Result rows | Loops | Hits | Reads | Dirtied | Written | Temp reads | Temp writes |
|---:|---|---|---:|---:|---:|---:|---:|---:|---:|---:|
| 10000 | `artifact_retention` | before | 100 | 1 | 868 | 0 | 0 | 0 | 0 | 0 |
| 10000 | `artifact_retention` | after | 100 | 1 | 868 | 0 | 0 | 0 | 0 | 0 |
| 10000 | `job_retention` | before | 0 | 1 | 851 | 0 | 0 | 0 | 0 | 0 |
| 10000 | `job_retention` | after | 0 | 1 | 851 | 0 | 0 | 0 | 0 | 0 |
| 10000 | `source_retention` | before | 200 | 1 | 1088 | 0 | 0 | 0 | 0 | 0 |
| 10000 | `source_retention` | after | 200 | 1 | 1088 | 0 | 0 | 0 | 0 | 0 |
| 10000 | `pending_tombstones` | before | 0 | 1 | 1 | 0 | 0 | 0 | 0 | 0 |
| 10000 | `pending_tombstones` | after | 0 | 1 | 1 | 0 | 0 | 0 | 0 | 0 |
| 10000 | `orphan_retention` | before | 0 | 1 | 3296 | 0 | 8 | 8 | 0 | 0 |
| 10000 | `orphan_retention` | after | 0 | 1 | 3296 | 0 | 8 | 8 | 0 | 0 |
| 10000 | `cache_retention` | before | 0 | 1 | 202 | 0 | 0 | 0 | 0 | 0 |
| 10000 | `cache_retention` | after | 0 | 1 | 404 | 0 | 0 | 0 | 0 | 0 |
| 10000 | `cache_recent_hit` | before | 1 | 1 | 6 | 0 | 0 | 0 | 0 | 0 |
| 10000 | `cache_recent_hit` | after | 1 | 1 | 6 | 0 | 0 | 0 | 0 | 0 |
| 10000 | `cache_recent_miss` | before | 0 | 1 | 3 | 0 | 0 | 0 | 0 | 0 |
| 10000 | `cache_recent_miss` | after | 0 | 1 | 3 | 0 | 0 | 0 | 0 | 0 |
| 10000 | `storage_admission_totals` | before | 1 | 1 | 7 | 0 | 0 | 0 | 0 | 0 |
| 10000 | `storage_admission_totals` | after | 1 | 1 | 7 | 0 | 0 | 0 | 0 | 0 |
| 10000 | `active_source_reservations` | before | 1 | 1 | 3 | 0 | 0 | 0 | 0 | 0 |
| 10000 | `active_source_reservations` | after | 1 | 1 | 3 | 0 | 0 | 0 | 0 | 0 |
| 100000 | `artifact_retention` | before | 200 | 1 | 6106 | 0 | 0 | 0 | 0 | 0 |
| 100000 | `artifact_retention` | after | 200 | 1 | 6106 | 0 | 0 | 0 | 0 | 0 |
| 100000 | `job_retention` | before | 0 | 1 | 1092 | 0 | 0 | 0 | 0 | 0 |
| 100000 | `job_retention` | after | 0 | 1 | 1092 | 0 | 0 | 0 | 0 | 0 |
| 100000 | `source_retention` | before | 200 | 1 | 2343 | 2699 | 0 | 3 | 0 | 0 |
| 100000 | `source_retention` | after | 200 | 1 | 2313.5 | 2732 | 0 | 6 | 0 | 0 |
| 100000 | `pending_tombstones` | before | 0 | 1 | 1 | 0 | 0 | 0 | 0 | 0 |
| 100000 | `pending_tombstones` | after | 0 | 1 | 1 | 0 | 0 | 0 | 0 | 0 |
| 100000 | `orphan_retention` | before | 0 | 1 | 4369 | 0 | 8 | 8 | 0 | 0 |
| 100000 | `orphan_retention` | after | 0 | 1 | 4369 | 0 | 8 | 8 | 0 | 0 |
| 100000 | `cache_retention` | before | 0 | 1 | 2002 | 0 | 0 | 0 | 0 | 0 |
| 100000 | `cache_retention` | after | 0 | 1 | 817 | 0 | 0 | 0 | 0 | 0 |
| 100000 | `cache_recent_hit` | before | 1 | 1 | 8 | 0 | 0 | 0 | 0 | 0 |
| 100000 | `cache_recent_hit` | after | 1 | 1 | 8 | 0 | 0 | 0 | 0 | 0 |
| 100000 | `cache_recent_miss` | before | 0 | 1 | 4 | 0 | 0 | 0 | 0 | 0 |
| 100000 | `cache_recent_miss` | after | 0 | 1 | 4 | 0 | 0 | 0 | 0 | 0 |
| 100000 | `storage_admission_totals` | before | 1 | 1 | 5 | 0 | 0 | 0 | 0 | 0 |
| 100000 | `storage_admission_totals` | after | 1 | 1 | 5 | 0 | 0 | 0 | 0 | 0 |
| 100000 | `active_source_reservations` | before | 1 | 1 | 16 | 0 | 0 | 0 | 0 | 0 |
| 100000 | `active_source_reservations` | after | 1 | 1 | 16 | 0 | 0 | 0 | 0 | 0 |

## Workload gates

The summary keeps per-call progress curves, failures, cache backlog observations and accounting assertions. The gate below follows the raw result; it never treats timeout or zero progress as success.

| Rows | Workload | Before | After |
|---:|---|---|---|
| 10000 | `bounded_cache_backlog` | FAIL | PASS |
| 10000 | `concurrent_admission_publication` | PASS | PASS |
| 10000 | `legacy_null_deadline_admission` | PASS | PASS |
| 10000 | `poison_deletion` | PASS | PASS |
| 10000 | `progressive_bootstrap` | FAIL | FAIL |
| 10000 | `short_reconciliation` | FAIL | FAIL |
| 100000 | `bounded_cache_backlog` | FAIL | PASS |
| 100000 | `concurrent_admission_publication` | PASS | PASS |
| 100000 | `legacy_null_deadline_admission` | PASS | PASS |
| 100000 | `poison_deletion` | PASS | PASS |
| 100000 | `progressive_bootstrap` | FAIL | FAIL |
| 100000 | `short_reconciliation` | FAIL | FAIL |

The 5 ms experiments are artificial stress controls, not a service SLA. Keep their raw failures visible; ordinary admission and cleanup budgets are separate measurements.

## Concurrent operations

Client-observed milliseconds include pool, shared-storage-lock and database work.

| Rows | Operation | p50 before | p50 after | p95 before | p95 after |
|---:|---|---:|---:|---:|---:|
| 10000 | `reserve_source` | 10.699 | 11.301 | 24.961 | 40.475 |
| 10000 | `durable_source_publication` | 10.217 | 11.104 | 43.219 | 39.602 |
| 10000 | `retention_batch` | 11.761 | 11.951 | 67.258 | 57.981 |
| 10000 | `persisted_metadata_hit` | 1.309 | 1.137 | 12.143 | 14.324 |
| 100000 | `reserve_source` | 60.862 | 34.949 | 138.672 | 109.871 |
| 100000 | `durable_source_publication` | 39.661 | 54.623 | 137.195 | 108.592 |
| 100000 | `retention_batch` | 93.097 | 84.736 | 193.045 | 195.563 |
| 100000 | `persisted_metadata_hit` | 1.291 | 1.176 | 101.608 | 92.021 |

## Full walks and ordinary legacy initialization

| Rows | Operation | Before, ms | After, ms |
|---:|---|---:|---:|
| 10000 | `short_reconciliation.final_full_scan_ms` | 1253.782 | 1227.966 |
| 10000 | `short_reconciliation.unchanged_full_scan_ms` | 850.929 | 814.429 |
| 10000 | `progressive_bootstrap.final_pass_ms` | 1271.083 | 1205.219 |
| 10000 | `legacy_null_deadline_admission.duration_ms` | 1558.506 | 1449.389 |
| 100000 | `short_reconciliation.final_full_scan_ms` | 16173.104 | 13106.596 |
| 100000 | `short_reconciliation.unchanged_full_scan_ms` | 11554.936 | 8677.509 |
| 100000 | `progressive_bootstrap.final_pass_ms` | 15964.594 | 12979.243 |
| 100000 | `legacy_null_deadline_admission.duration_ms` | 19430.113 | 19974.061 |

## Massive expired metadata backlog

Fresh dedicated sources prevent source deletion/cascade from being counted as cache expiry progress. Eight ordinary public Cleanup calls exercise a bounded continuation, without demanding that a whole 100k backlog drain in one cycle.

| Initial expired rows | Version | Removed | Largest call | Remaining | Fresh controls unchanged | Source rows | Errors |
|---:|---|---:|---:|---:|---|---:|---:|
| 10000 | before | 10000 | 10000 | 0 | True | 10023 | 0 |
| 10000 | after | 1600 | 200 | 8400 | True | 10023 | 0 |
| 100000 | before | 100000 | 100000 | 0 | True | 100023 | 0 |
| 100000 | after | 1600 | 200 | 98400 | True | 100023 | 0 |

## Memory

Go heap/Go Sys/allocation counters and sampled process RSS belong to the Go test process. PostgreSQL memory.current includes all container processes and charged cache/tmpfs; its run-local sampled peak is distinct from lifetime memory.peak. Host cgroup readings may include unrelated processes. None is a production memory guarantee.

| Rows | Version | Go heap peak, bytes | Go Sys peak, bytes | Sampled process RSS peak, bytes |
|---:|---|---:|---:|---:|
| 10000 | before | 5579384 | 18192648 | 25325568 |
| 10000 | after | 5567880 | 18192648 | 25600000 |
| 100000 | before | 6742024 | 26843400 | 26943488 |
| 100000 | after | 11273360 | 27105544 | 33173504 |

## Remaining limits

A batch limit bounds committed candidates, not every row examined. Read the actual source-retention and ownership plans before claiming fixed cost at large retained-row counts. Filesystem prefix replay still performs name work; scan/layout limits and any regressions remain visible in raw progress curves and full-walk timings. The report makes no all-fast claim and no extrapolation to media/4K throughput.
## Reproduction and raw outputs

Use the [workload guide](../../docs/storage-capacity.md), a disposable database,
matching limits/toolchain, sufficient scratch space and idle task workloads.
The normal service-budget cases and all failed stress curves must be retained.
The baseline runtime is public `91192c8`; apply only the two frozen harness files
from the measured after revision `b1d2e48`:

```sh
git worktree add ../scalable-storage-before 91192c8dd26ac545515d93b50f63d50db121deba
git show b1d2e486cd547e90f4f0fe6b3c61e3e5771fc059:scripts/storage-capacity.py > ../scalable-storage-before/scripts/storage-capacity.py
git show b1d2e486cd547e90f4f0fe6b3c61e3e5771fc059:internal/app/storage_capacity_test.go > ../scalable-storage-before/internal/app/storage_capacity_test.go
cd ../scalable-storage-before
# Configure TEST_DATABASE_URL for your owned disposable PostgreSQL and scratch.
export GOMAXPROCS=2 GOFLAGS=-p=2 GOTOOLCHAIN=local
python3 scripts/storage-capacity.py --output /path/to/new-before --sizes 10000,100000 --samples 20 --pg-container your-owned-postgres
```

For after use clean `b1d2e48`, the same arguments/configuration and a new empty
output directory. Check the runtime and harness digests in `environment.json`.
A different environment can produce different timings; do not rerun to select
nicer samples. A race build is a correctness run and must not be compared with
ordinary-build performance.

Both runs complete setup and both sizes, then exit 1 because recorded workload
gates fail. `before/` and `after/` each preserve the complete unmodified
`rows-10000.json`, `rows-100000.json`, `environment.json`, `workload.log` and
`build.log` as gzip with empty filename and mtime=0. The manifest records
compressed/uncompressed sizes and SHA-256; `summary.json` preserves every gate,
curve, counter invariant and separate memory scope. For example:

```sh
gzip -dc after/rows-100000.json.gz
```

No production server, real recording or live provider was used. Owned schemas
and synthetic filesystem entries were removed after each size. Failed setup
would be incomplete evidence; it is not silently treated as a successful run.
