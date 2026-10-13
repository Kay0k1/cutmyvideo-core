# HTTP worker and deferred-candidate diagnostics

`core-performance-server.py` uses the six immutable `server_scenarios` registered
by `core-performance.py freeze`: one/four owners, four jobs per owner, two cuts
per job, accurate MP4/copied MP4/MP3, two worker slots and two codec threads.
Fixture/tool/manifest hashes are checked before running. Build the same server,
packet-count and storage-fence witnesses into each checkout through recorded Go
overlays; the only server adapter difference is support for explicitly selected
candidate preparation concurrency. Default comparisons use zero on both sides.

The default server witness launches the exact baseline CLI and a candidate CLI
built under the same source fingerprint guard as its helper. API and worker run
as independent processes with independent four-connection pools in one owned
PostgreSQL namespace. The coordinator-owned launcher creates fresh per-role
cgroups: API one CPU/512 MiB and worker two CPUs/1 GiB, each 128 tasks and no
swap. Private database configuration stays in the environment. Role receipts
record kernel cumulative CPU/I/O and available memory counters, sampled memory
and process RSS, waited largest-process RSS, process IDs, limits and graceful
shutdown. Unavailable kernel peak counters remain null; a sampled maximum is
explicitly a lower bound. The outer helper tree also includes both roles and
coordination. Optional PostgreSQL cgroup snapshots are cumulative/endpoints.

Actual local HTTP sessions, multipart uploads, job submission, history/status
polling, and receipt of every artifact byte run against the independent API.
Uploads occur before the burst timer. Outer resources/tool counts include setup,
uploads, shutdown and post-phase SQL; independent output verification follows.
`--in-process` retains the earlier same-process, two-Store witness only as a
labelled supplemental mode, without per-role resource observations.

All four initial requests per owner start at a barrier. The real application
permits only three active jobs per owner, so initial `job_limit` refusals are
retained. A refused logical request is submitted once more, with the same
idempotency key, only after an HTTP-observed completion for that owner. Other
refusals or a second capacity refusal fail the observation. Whole-burst
throughput and each job's accepted-response-to-final-received-byte latency remain
separate. History polling is every 250 ms and affects observed completion time.

Candidate queue timing uses external receipt of its complete `job_claimed` log
line and SQL creation time, labelled `claim_log_observed_transitclock`. Pipe
serialization and coordinator scheduling add transit delay; this is not a direct
database acknowledgement timestamp. Historical binaries provide an estimate
reconstructed from externally observed worker-stop lines and rounded processing
elapsed milliseconds. These clocks, rounding and distinct scopes remain visible. Fixed-label candidate stage events separate source
inspection/cache hits, preparation/network bytes, processing-slot wait, composite
media processing, and filesystem/database publication. Source preparation also
includes HLS remux/probes; composite media processing includes verification and
can include progressive-source network I/O. Neither is labelled pure encoding.
Fixed-label stage records contain no session cookies, subprocess arguments or
private URLs. Raw task-owned role logs remain available for diagnosis.

Each sample has explicit finite configuration: 128 MiB source/fetch caps,
32 MiB output cap, 2 GiB logical store, 512 MiB owner allowance, 64 MiB physical
safety floor, 32 active jobs and 1,000 mutation attempts/minute. These settings
are identical between revisions and recorded in the result. The owner active-job
and export-rate limits remain in effect. Samples use owned media workspaces;
only verified outputs are removed after semantic comparison. Failed reports,
stage logs and media remain available for diagnosis, while owned SQL namespaces
and child processes are cleaned up. Failure-only cgroup draining captures an
additional resource receipt before cleanup and preserves the original failure.

Example setup, after the coordinator has supplied a disposable
`TEST_DATABASE_URL` through private configuration:

```sh
python3 scripts/core-performance-server.py build --manifest /task/frozen.json \
  --baseline-core /task/baseline --candidate-core /task/candidate \
  --output /task/server-build
python3 scripts/core-performance-server.py run --manifest /task/frozen.json \
  --provenance /task/server-build/provenance.json --output /task/server-p0 \
  --pairs 3 --preparation 0 --idle-window COORDINATOR_WINDOW
```

Use a distinct output directory and a separately registered window for an
optional preparation-one comparison. Do not overlap timed runs with builds,
tests, verification or other media tasks. Three pairs are exploratory; key
findings need additional preregistered pairs. Preserve regressions and failures.

The compiled helpers also contain two opt-in evaluations:

- `TestCoreCopyBudgetDiagnostic` requires `CUTMY_CORE_PERF_COPY_MANIFEST`,
  `CUTMY_CORE_PERF_COPY_RESULT` and `CUTMY_CORE_PERF_IDLE_WINDOW`. It alternates
  three actual production-inspection and full-file `-count_packets` samples for
  the frozen long/complex sources. It records wall/child CPU/RSS/block I/O,
  per-stream packet counts, file bytes and actual current producer ceilings.
  Optional `CUTMY_CORE_PERF_COPY_EXTRA_INPUT` is a recorded
  `{"path":...,"sha256":...,"bytes":...}` fixture, used for an 8K probe sample.
  This evaluates indexing cost; it supplies no universal mux-overhead proof and
  changes no production output/reservation bound.
- `TestCoreStorageFenceDiagnostic` requires `CUTMY_CORE_PERF_STORAGE_RESULT`,
  `CUTMY_CORE_PERF_IDLE_WINDOW` and the disposable `TEST_DATABASE_URL`. Identical
  owned fixtures contain 1,000 healthy retained artifacts and one/four active
  owners, four jobs each, two outputs, two processing goroutines and a
  four-connection pool, with concurrent maintenance. It records actual fence
  query latency and the client-observed interval from acquisition ACK through
  COMMIT/ROLLBACK ACK, plus operation wall time. Pool acquisition lies outside
  the SQL query interval. Tiny synthetic artifacts exercise accounting and
  synchronization; this is not a media or production-capacity measurement.

Both evaluations preserve exclusive JSON results, including partial failures.
Keep conservative copy weights until independent large-decoder resource
observations and a safe phase-admission design justify relaxing them. Keep the
common storage fence until its measured contribution warrants the risk of a
more complex quota/publication protocol.


Migration007 supplemental recovery gates run only against the owned disposable
PostgreSQL17 container. `TestMetadataResolutionPreviousBinaryRefuses007WithoutWrites`
uses `CUTMY_TEST_PREVIOUS_CORE_BINARY` to prove the exact006 binary refuses007
without changing catalog, row versions, caches or leases. The mandatory runtime
grants test connects as a generated nonowner LOGIN with production-equivalent
per-table grants and exercises lease CRUD/cache publication while CREATE stays
forbidden. `TestMetadataResolutionCoordinated007BackupRestorePreservesRowsAndMedia`
uses `CUTMY_TEST_PGTOOLS_CONTAINER`, actual custom-format `pg_dump`/`pg_restore`
into a fresh owned database, coordinated media backup/restore, and compatible007
startup without row/catalog rewrites. These bounded proofs are not a production
rollout or a substitute for release-specific recovery acceptance.
