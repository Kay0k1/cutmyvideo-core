# Reproducing storage maintenance capacity

`make storage-capacity-check` is an opt-in retained-row and filesystem-metadata
workload. It requires a dedicated, disposable PostgreSQL database with permission
to create/drop schemas. Each size uses a new namespace and two real Store pools
(four connections per pool), then removes its namespace. No production service,
live provider, real recording or FFmpeg process is used.

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
  through the Go parser. Each `EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON)` executes the
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
- A final legacy fixture with exactly 10,000/100,000 artifact deadlines set to
  NULL and fresh owned bootstrap metadata. Real source admissions retry within
  their five-second database budget and a two-minute aggregate budget. The
  runtime must commit all deadline backfill and open admission while keeping
  the byte counters consistent. The test does not perform the backfill in SQL.
  This stage follows the timed contention phase so it cannot alter that phase's
  retained-row fixture.

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
