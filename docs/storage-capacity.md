# Reproducing storage maintenance capacity

`make storage-capacity-check` is an opt-in retained-row and filesystem-metadata
workload. It requires a dedicated, disposable PostgreSQL database with permission
to create/drop schemas. Each size uses a new namespace and two real Store pools
(four connections per pool), then removes its namespace. No production service,
live provider, real recording or FFmpeg process is used.

The [2026-10-10 before/after report](../benchmarks/2026-10-10-storage-maintenance/README.md)
preserves complete results at both sizes, including regressions and the overall
failed workload gate. Use its recorded runtime/harness hashes and configuration
when reproducing that comparison.
The [subsequent scalable-storage report](../benchmarks/2026-10-10-scalable-storage/README.md)
uses the revised frozen harness with the additional bounded-cache backlog stage;
compare its own matched before/after pair rather than mixing harness versions.

```sh
export TEST_DATABASE_URL='postgres://cutmy_test:test-only@127.0.0.1:55440/cutmy_storage_test?sslmode=disable'
export GOMAXPROCS=2
export GOFLAGS=-p=2
export TMPDIR=/path/to/scratch
# Optional: collect memory.current/stat for this owned PostgreSQL container.
export STORAGE_PG_CONTAINER=cutmy-storage-readiness-db
make storage-capacity-check STORAGE_REPORT_DIR=/path/to/new-evidence
```

The default sizes are 10,000 and 100,000 rows per main application table; override
with `STORAGE_SIZES=10000,100000` and `STORAGE_SAMPLES=20`. The runner refuses an
existing nonempty evidence directory. It requires Python 3.10+ standard library,
the repository's Go toolchain, PostgreSQL, and Linux `/proc` for process RSS.
Docker is needed only when collecting the optional container measurements.
Keep other tests and workloads idle when comparing performance. Record the same
PostgreSQL configuration, CPU limits, toolchain, sample count and harness digest
before and after a runtime/index change.
Allow roughly 300 MB for the 100,000-row relations before scan observations,
plus PostgreSQL WAL, cache and temporary work. The workload should run alone;
a 1 GB PostgreSQL memory limit with 768 MB of disposable data space was used
for the initial baseline. A smaller environment that exhausts space is an
incomplete run, not an application performance result.

The fixture contains ten sources per synthetic owner, a job and artifact for each
retained source, active jobs that pin resources, interleaved expired artifacts,
old eligible jobs/sources, cached small metadata payloads, preview reservations,
and old orphan ledger rows. It also creates 10,000/100,000 real zero-byte private
filesystem entries plus 1,024 nine-byte files. These entries exercise directory
names, stat calls and scan progress; they do not represent video size, disk
throughput, export CPU capacity or RSS for large video recordings. Database size
and measured memory are reported rather than inferred from row count.
The cache target is refreshed through the real writer before concurrent timed
calls, preserving the normal five-minute TTL even after longer scan experiments.

The gate records:

- Twenty runs of the actual runtime retention, cache and admission SQL, extracted
  through the Go parser (the source selector uses its exact runtime constant). Each `EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON)` executes the
  real statement, including DML and triggers. Mutating plan runs roll back so
  every sample starts with the same eligible rows. The first warm-up is retained;
  these are repeated warm-cache samples, not a cold-cache claim.
- Three deletion cycles with 200 non-regular poison paths before 200 regular
  paths. Unsafe paths and their byte charges must survive while healthy entries
  make progress. The workload uses the real pending-path selector and drain.
- Thirty-two five-millisecond public reconciliation and bootstrap/admission attempts,
  with registration progress after each attempt. Subsequent full/retry passes
  verify completeness separately; their results do not replace failed short
  cycle measurements. A second unchanged scan must preserve ledger tuple xmin.
  On implementations with persisted scan progress, the full pass also verifies
  that its source/artifact generations committed completion.
- Four concurrent callers each reserving and durably registering twenty tiny
  completed source files, alongside retention and owner-scoped cache hits.
  Reservations must clear, all publications must appear, and stored/reserved
  counters must equal actual ledger sums. A five-millisecond sampler records
  observed advisory-lock waiters; sampling cannot count every brief wait.
- A late legacy fixture with exactly 10,000/100,000 artifact deadlines set to
  NULL and fresh owned bootstrap metadata. Real source admissions retry within
  their five-second database budget and a two-minute aggregate budget. The
  runtime must commit all deadline backfill and open admission while keeping
  the byte counters consistent. The test does not perform the backfill in SQL.
  This stage follows the timed contention phase so it cannot alter that phase's
  retained-row fixture.
- Eight public cleanup calls against exactly 10,000/100,000 additional expired
  cache rows. Dedicated fresh sources prevent cascade deletion from contributing
  to the count. Every call must remove at most 200 of these entries and the eight
  calls must remove 1,600, preserving all sources and 23 fresh cache controls
  (including payloads, owners, deadlines and tuple xmin). This workload runs
  last and does not require draining the entire backlog.

Raw `rows-*.json` includes all plans, client round-trip and PostgreSQL execution
samples, nearest-rank p50/p95, workload invariants, registration curves and Go
memory. `environment.json` records source/harness/binary hashes, tool versions,
CPU settings, optional PostgreSQL image/limits, and cgroup samples. Logs and a
nonzero workload exit remain available when a regression fails. A failed fixture
setup is incomplete evidence and must not be compared as a successful run.

Memory measurements have different scopes: Go heap and `runtime.MemStats.Sys`
are sampled within the Go test process; RSS includes that process's resident
pages. The same process also holds the test harness and raw plans, so neither is
a production API memory guarantee. The PostgreSQL container cgroup includes
database processes and charged cache; a host session cgroup can include unrelated
processes. `memory.peak`, where supported, can predate the run. Run-local sampled
`memory.current` peaks are recorded separately. None of these values can be
substituted for another.

For a correctness run with the race detector, invoke the runner with `--race`
and a new output directory. Its latency and memory must not be compared with
ordinary builds. The five-millisecond progress experiment is performance
sensitive; inspect its raw curve and environment when diagnosing a failure.

## Scan correctness and operational budgets

The API's database-operation budget is five seconds, including pool acquisition;
one maintenance cycle has a ten-second parent deadline. Earlier caller deadlines
still take precedence, and maintenance divides its remaining time between stages.
These deadlines limit waiting. They do not guarantee initialization or a durable
scan batch on an arbitrarily slow database or filesystem, and there is no public
five-millisecond progress SLA.

The 2026-10-11 retained 1,000-row experiments failed the original short-cycle
criteria. The subsequent PG15 diagnostic also recorded zero registrations in all
32 five-millisecond reconciliation attempts and all 32 bootstrap attempts.
Independent full recovery completed, and 14 of its 15 tests passed, including
non-ISO timestamp decoding and rollback/recovery after a denied lookup. Neither
recovery nor the correctness results turns the short-cycle failure into PASS.
The original requirement remains 32 actual five-millisecond attempts, including
at least 512 additional reconciliation registrations after cycle eight unless
the remaining entries complete.

The exact `TestStorageScanRepeatedFiveMillisecondCyclesMakeDurableProgress` body
now lives in `storage_scan_latency_benchmark_test.go` under the `storagecapacity`
build tag. This explicitly changes its classification from an ordinary full-CI
test to an opt-in performance experiment; its body and capacity helpers are
unchanged. `make storage-capacity-check` retains its existing short-cycle gates.
The mandatory native storage gate instead adds deadline-batch/ACK/restart,
scheduler rollback/draining, non-ISO timestamp compatibility and denied-lookup
rollback/recovery and false-preview-pin checks: 45 storage checks on each of the five Linux, macOS and
Windows targets. These candidate gates still require fresh execution.
Core checks and the release workflow's reusable CI prerequisite run these Go
source tests on all five targets. The separate archived-CLI checks exercise the
ready binaries; they do not execute these Go unit tests inside an archive.

Run the unchanged short-cycle test against a dedicated disposable database:

```sh
go test -json -race -count=1 -timeout=3m -tags=storagecapacity ./internal/app \
  -run '^TestStorageScanRepeatedFiveMillisecondCyclesMakeDurableProgress$'
```

A separately named operational experiment uses the same 1,000-row fixture in a
fresh namespace with prepared Store pools. It creates 1,000 zero-byte files and
1,024 nine-byte files and the 990 healthy 16-byte artifact files whose fixture
deadlines are still live. It clears that namespace's initial observation marker, then
calls real `RunMaintenance` once under a ten-second parent deadline. This measures
the external one-shot entry point, not the background worker loop, queue capacity
or service health. There are 2,024 scan files and 990 protected files, 3,014 in
total. Reopening
the Store must observe the initialization marker, completed bootstrap and
reconciliation generations, every file and its exact byte charge. A real API
database wrapper then bootstraps, reserves and releases a source within its
five-second per-operation budget. Before admission, only the owned namespace's
bootstrap cursors/marker and scan-file observations are cleared; the actual
files and application references remain. Another Store pool must observe the
committed initialization, file charges and reservation acknowledgement, and
read-backs verify the complete stored/reserved ledger sums. Setup, reopening and
read-back oracles are outside the timed calls. No full/retry scan repairs this
experiment's result.
The 1,000 retained sources and jobs, 200 live preview reservations, 990 healthy
artifact rows and their exact physical contents must survive both stages.
Immutable row snapshots and authoritative job/preview pin counts supplement
the byte-accounting oracle. The separate 200 expired metadata rows and the ten
deliberately expired artifact rows may undergo normal retention.
After each timed stage, an additional bounded oracle checks all 2,024 exact
scan paths and their individual ledger charges, and rereads every regular file's
expected zero-byte or nine-byte content. This catches missing/modified files and
compensating wrong charges that an aggregate count or byte sum could miss.

```sh
go test -json -race -count=1 -timeout=3m -tags=storagecapacity ./internal/app \
  -run '^TestStorageScanOperationalBudgets1000$'
```

Both commands require `TEST_DATABASE_URL` as described above and preserve JSON
test output, including failures. The operational test emits
`operational_budget_evidence` as a JSON object in its test log. Its
`operational_pass` field applies only to this separate observation. Report it
alongside the unchanged short-cycle result; it cannot replace a failed original
capacity gate or authorize a 10,000/100,000-row promotion.

The isolated Linux/PostgreSQL 17.11 race-enabled observation on 2026-10-11
passed: the one-shot maintenance call acknowledged in 1,680.036 ms and API
reservation in 449.297 ms. Reopening observed all 2,024 scan files with their
9,216-byte charge; all per-path and physical-content checks passed. The 1,000
retained sources and jobs, 200 preview pins and 990 healthy artifact files
survived both stages, counters matched their ledgers, and the acknowledged
reservation was released. The fixture used prepared pools and explicit
observation resets between stages; setup and read-back checks were outside
those timings. It used two PostgreSQL CPUs, a 512 MiB hard memory limit without
extra swap and private 512 MiB database tmpfs. Statement logging and the race
detector were enabled. This is one functional observation, not a comparative
performance result, worker-loop measurement or latency guarantee. The original
five-millisecond FAIL remains a separate result. It verified the scan changes
before the separate preview-query edit; the combined candidate still needs
fresh CI.

## Exact-preview pin query experiment

The source TTL selector still materializes at most 200 ordered candidates and
checks actual job and exact-preview references before deletion. Removing only
the preview `OFFSET 0` simplification barrier lets PostgreSQL choose a different
valid subplan; the jobs barrier and all quarantine, deletion and accounting
guards remain intact. An expired, abandoned or foreign-owner reservation of
kind `preview` still pins its source, including an empty reservation ID.
`preview_cache` and `source` reservations do not become exact-preview pins.

The matched [2026-10-11 PostgreSQL 17.11 experiment](../benchmarks/2026-10-11-ttl-preview-offset/README.md)
used the same analyzed warm
synthetic database with 1,000 retained/pinned sources, 200 expired eligible
sources and 200 preview reservations. Twenty alternating before/after pairs
included the first pair without exclusions. All 40 original query plans,
acknowledged rollbacks, row/xmin snapshots and byte counters passed, as did all
16 source-retention tests and owned-resource cleanup.

| Query-only measurement, ms | Before | Preview barrier removed |
| --- | ---: | ---: |
| PostgreSQL execution median | 9.902 | 5.7465 |
| PostgreSQL execution p95, linear interpolation | 10.44385 | 5.8281 |
| Client EXPLAIN round-trip median | 12.3227505 | 8.0511595 |
| Client EXPLAIN round-trip p95, linear interpolation | 12.92463055 | 8.6405765 |

The candidate was faster in all 20 pairs. The preview sequential subplan ran
once instead of 200 times, and its shared-buffer hits fell from 600 to three.
The non-verbose plans do not expose an explicit Hash node. Both sides used
the same two PostgreSQL CPUs, a 1 GiB hard memory limit without extra swap,
private 512 MiB tmpfs, 128 MiB shared buffers, 4 MiB `work_mem`, 32 connections,
disabled JIT and enabled `fsync`/synchronous commit. These timings cover this
query and fixture, not a full maintenance call, worker workload or service SLA.

The 200-candidate limit does not bound work over the global preview membership
relation. A selected subplan may read that entire relation once and keep its
membership in memory. Larger preview inventories, distinct source counts,
memory pressure and scaling were not measured, and the planner may choose a
different plan. The earlier failed TTL experiments and unchanged short-cycle
FAIL results remain separate; this query result does not promote the overall
capacity gate or a 10,000/100,000-row workload.
