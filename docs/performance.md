# Performance and recovery

For retained database rows, interrupted directory scans, failed-deletion fairness
and concurrent storage admission, use the separate
[storage capacity workload](storage-capacity.md). It measures synthetic metadata
and filesystem entries; media encoding results below have a different scope.

Current optimization evidence: [metadata cache encoding, 2026-10-10](../benchmarks/2026-10-10-metadata-compression/README.md)
includes raw before/after measurements, environment, medians/ranges and limits.
Repeated compressible metadata fills allocated 65.1% fewer bytes and took 24.7%
less time in that synthetic fixture. This does not measure whole-video export
speed or resident memory; pooled writers can be discarded during GC.

The core spends most export time in network inspection and FFmpeg, rather than
Go function calls. Defaults prioritize short, accurate clips on a CPU-only
server. Stream copy retains its documented keyframe boundary adjustment.

## Encoding profiles

`FFMPEG_PROFILE=fast` is the server default. The CLI uses `--profile fast` and
the Go package accepts `engine.Config{EncodeProfile: "fast"}`. It uses libx264
ultrafast, CRF 18 and CABAC. `compact` uses the previous veryfast/CRF 20 settings.
Both retain the requested resolution limit, frame cadence and AAC 192 kbit/s;
neither increases source resolution. MP3 and stream-copy behavior are unchanged.
Fast encoding produces larger files: choose compact when transfer size matters
more than CPU time.

Measurements on 2026-10-04 used the production worker image, its three-CPU quota
and two FFmpeg threads. A six-second slice of an already exported 1920×1080,
30 fps H.264/AAC recording was encoded sequentially with identical seek,
scaling, audio and output settings. Results were probed and compared with the
same decoded reference using SSIM.

| Profile | Encoding time | Output bytes | SSIM |
|---|---:|---:|---:|
| Previous compact, paired runs | 10.714 / 12.131 s | 1,692,889 | 0.995872 |
| Fast, CABAC enabled | 6.556 s | 6,458,107 | 0.996163 |

This fixture demonstrates roughly 1.7× faster encoding with approximately
3.8× larger output. It is not a universal speed or visual-quality guarantee:
source codecs, motion, CPU contention and network conditions change the result.
Three threads and the superfast preset did not improve this server's fixture;
one thread was slower. Avoid changing thread counts without measuring.

## Inspection and memory

An owner-specific PostgreSQL cache reuses verified platform metadata between
import and export, including across worker restarts. Its lifetime is at most
five minutes and ends earlier than recognized signed-address expiry, with a
30-second margin. Reopening the same canonical page may copy that cache but
never extend its deadline. Payloads are limited to 1 MiB. Upstream addresses
and required headers stay private; process-local relay URLs are never cached.

Large cache payloads now use gzip only when the serialized JSON is at least
4 KiB and compression removes at least 75% of its bytes. Less compressible
signed addresses retain JSON to avoid unnecessary decompression CPU. Both
encoded and decoded payloads remain limited to 1 MiB; gzip lengths, checksums,
single-member boundaries and legacy JSON rows are validated. The lifetime and
owner checks remain unchanged, including on a hit. Repeated-import lookup
reads the source identity and cached payload in one database round trip.

On 2026-10-08, a local isolated PostgreSQL fixture with 182 formats and repeated
2,000-byte synthetic tokens reduced cache payload transfer from 411,245 to
5,126 bytes. Actual PostgreSQL `pg_column_size(payload)`, including its existing
TOAST compression, fell from 17,459 to 3,365 bytes (81% less). Independently
generated high-entropy tokens correctly stayed uncompressed. A smaller cache
fixture measured repeated-import lookup at 662 µs with two queries versus
353 µs with one query, and 7,923 versus 6,676 allocated bytes (three-run medians).
These are offline cache fixtures, not public-platform or whole-export timings.

Owner-protected local previews and thumbnails permit private browser retention
with `max-age=0, must-revalidate`, a cookie-dependent cache key and a file-version
ETag. Every conditional request still checks its owner and opens the file before
it can return 304; deletion and cookie changes cannot serve an old cached hit.
Export downloads and JSON retain `no-store`. A repeated 256 KiB local preview
fixture transferred zero body bytes after revalidation and allocated about
1,292 instead of 34,130 bytes; measured handler time was 20.3 instead of
56.6 µs (three-run medians), excluding database authorization and network time.

An offline benchmark with the pinned extractor and 182 formats measured
1.464 s per uncached extraction versus 4.902 ms per persisted-cache read, with
zero extractor invocations on a hit. These numbers isolate inspection; they
exclude external service latency, thumbnails and encoding.

Guarded relays reuse fixed 32 KiB buffers without sharing an active buffer
between transfers. Five-run local Range benchmarks measured median relay cost
falling from 18.16 to 8.925 µs and allocated bytes from 35,080 to about 2,327 per
operation (93.4% less). Preparsed address rules remove repeated CIDR parsing.
These are allocation/CPU measurements, not download-speed claims.

The Docker image sets `GOMEMLIMIT=128MiB`, an operator-overridable **soft Go
runtime memory limit**. It does not cap FFmpeg or replace container memory
limits. Garbage collection remains enabled; arbitrary forced collections,
unsafe memory reuse and unmeasured assembly optimizations are avoided.

## HLS and storage audit, 2026-10-05

HLS parsing now resolves relative addresses against one validated base URL and
validates the resolved URL without serializing and parsing it again. HTTPS,
credentials, port restrictions and guarded network requests remain enforced.
The segment slice is sized once from duration tags, within the existing
100,000-segment limit. Selecting a clip uses binary search on the parser's
ordered timeline and still validates discontinuities and initialization maps
throughout the selected window.

The following are medians of three local `-benchmem` runs on Go 1.27.1,
linux/amd64, AMD Ryzen 9 5950X, with four Go scheduler threads. They are isolated
CPU/allocation fixtures, not whole-export or production latency measurements.

| Fixture | Before | After | Allocated bytes before / after |
|---|---:|---:|---:|
| Parse 10,000 HLS segments | 24.838 ms | 11.354 ms | 10,418,306 / 4,809,579 |
| Select a short window from 100,000 segments | 158.891 µs | 0.041 µs | 0 / 0 |
| Scan 10,000 empty retained files | 41.067 ms | 39.412 ms | 5,473,432 / 5,149,999 |

HLS parsing took about 54% less time and allocated 54% fewer bytes in this
fixture. The storage timing difference is small and should not be treated as a
speed guarantee. Storage admission and orphan-directory scanning now read at
most 128 directory entries per batch, without sorting the whole directory.
This bounds retained directory listings and lets quota/cancellation checks run
before a large directory has been completely read. Removed files are skipped
individually so concurrent cleanup cannot prematurely approve the rest of a
quota scan.

Inspection requests only the FFprobe duration, start times, codec types/names
and dimensions consumed by the application, using its documented
[`-show_entries` option](https://ffmpeg.org/ffprobe.html#Main-options).
Unused user metadata no longer enters the subprocess JSON buffer. Existing
codec, duration and output validation remain in place. A local one-second
H.264 fixture carrying a 1 MiB comment produced 1,051,270 bytes with the old
inspection options and 293 bytes with selected fields; duration, codec and
dimensions were identical. A regression test also covers valid media whose
unused tags exceed the 8 MiB subprocess response limit.

The initial audit implementation still statted retained files on admission and
held a registered-path set for orphan membership checks. The durable ledger
implementation below supersedes both of these historical limitations.
Large deployments should still measure maintenance scans and enforce a
filesystem quota in addition to application admission. The audit does not change encoding quality
or claim reduced FFmpeg RSS; source decoding and encoding remain the main CPU
and memory consumers for ordinary exports.

Each API/worker PostgreSQL pool now defaults to four maximum connections.
The pinned pgx default uses the host's `runtime.NumCPU()`, which does not honor
a container CPU quota. Connections are opened on demand, so this is a limit on
growth under concurrency, not a measured reduction in idle RSS. Explicit
`pool_max_conns` in `DATABASE_URL` remains authoritative; larger explicit
`pool_min_conns` or `pool_min_idle_conns` raise the default maximum accordingly.
Tune these settings together with PostgreSQL's connection budget when scaling
instances. Storage and fair-queue access now use indexes introduced with the
durable ledger below. Metadata-cache indexes remain unchanged; measure their
query plans before changing them for a larger retention window.

## Failure handling

- CLI interrupt/termination signals cancel the inspection/encoding context before
  exit, killing and reaping the isolated Unix media process group. Previously a
  terminated CLI could leave FFmpeg or FFprobe running outside its parent group.

- Cached addresses rejected with HTTP 401/403/404/410 receive at most one
  forced fresh resolution per job. Fresh refusals, encoding failures, limits
  and cancellation are terminal, except recoverable filesystem exhaustion,
  which enters the bounded storage wait. Refresh preserves source identity/timeline
  and the transfer budget; initial cached/fresh inspection shares one timeout.
- Range/HEAD and guarded DNS checks remain intact. One 15-second deadline
  covers DNS and ordered connection attempts; all returned addresses are
  validated first. Cancellation closes active relay tunnels.
- Queue claims and lease heartbeats have five-second database budgets.
  Retention runs separately with a ten-second cycle budget and does not keep
  a stuck worker's health marker alive.
- Linux admission checks actual available filesystem space as well as the
  PostgreSQL ledger's stored and reserved bytes. Admission and publication
  share transaction locks across processes; retained files are not scanned
  during admission.
- Artifact registration and completed job-item snapshots commit together
  under lease fencing and cancellation checks. Lost commit acknowledgements
  retain files for authoritative database recovery; definitely rejected
  publications remove them. Old orphan files remain subject to retention.
- `items[].progress_ms` comes from bounded FFmpeg machine-readable media time.
  It is encoding progress, not an ETA or proof a download is ready. Output
  verification and publication follow encoding; a refresh can reset it.

Worker logs include fixed job/item identifiers, elapsed inspection/processing
time and cache-hit status. They omit private media addresses and raw subprocess
diagnostics. Measure complete jobs separately from these stages.

## Reproduce

Ordinary tests and benchmarks use local fixtures, not public platform requests:

```sh
go test -race -count=1 ./...
go test ./internal/app -run '^$' -bench 'NetworkRelayRange|PublicIP' -benchmem -count=5
go test ./internal/app -run '^$' -bench 'HLSParse|HLSSelect|StorageAdmission' -benchmem -count=3
# With TEST_DATABASE_URL pointing to an isolated PostgreSQL test database:
go test ./internal/app -run '^$' -bench 'MetadataPayload|RecentPlatformMetadata|PrivatePreviewRepeat' -benchmem -count=3
# In an isolated PostgreSQL test database with the pinned yt-dlp executable:
go test ./internal/app -run '^$' -bench 'Metadata' -benchmem
```

See benchmark names and required environment in the `_bench_test.go` files.
PostgreSQL tests create/remove their own schemas. Do not benchmark against a
live application's schema or run competing encoders when comparing profiles.
The worker pool combines durable disk admission, bounded concurrency and a
weighted local decoder semaphore. Container CPU/RAM quotas remain necessary.

## Durable storage and fair queue, 2026-10-05

API, worker and external maintenance share PostgreSQL `storage_files`,
`storage_reservations` and transactional numeric `storage_counters`. The global
admission calculation reads one counter row and checks filesystem free space;
it does not walk or stat retained files. PostgreSQL trigger updates and media
registration commit together. Fixed advisory lock `736021912360105` precedes
resource row locks for every admission/publication/release/deletion. Initial
migration is idempotent and versioned; routine maintenance does not repeat DDL.

Uploads/direct files reserve their byte ceiling; platform metadata preparation
reserves only 2 MiB for thumbnails (its inspection transfer budget is separate).
Job admission reserves duration/mode-dependent producer ceilings for all
unpublished results and twice the largest bounded HLS staging input for platform
jobs. Each successful artifact publication atomically saves the item, registers
actual bytes and releases that item's output ceiling from the job reservation.
The producer enforces these bounds, so short clips can reserve less space while
partially completed batches retain enough room to finish. Configuration/request
arithmetic rejects overflow.

`STORAGE_SAFETY_BYTES` defaults to 512 MiB of filesystem headroom;
`STORAGE_WAIT_TIMEOUT` defaults to 30 minutes. Storage-pressure waiters remain
active queue members, share a persisted deadline across restarts, and do not
consume encoding slots or worker attempts. Actual ENOSPC/quota failures during
staging/encoding also move accepted work to this wait after deleting its partial
workspace. Impossible reservations are rejected before entering the queue.

Claims rotate owners by least recent consideration/claim. Each claim considers
at most 128 candidates; owners of storage-blocked jobs rotate out of the next
window so smaller admissible jobs cannot be hidden indefinitely by a large
backlog. Global active jobs are bounded by `MAX_ACTIVE_JOBS` (1–1024); per owner
there are at most three. `MAX_RANGES` is bounded to 1–128. A job has a distinct
reservation for every lease, so a recovered worker never uncharges an older
lease's retained workspace. Stale workspace deletion is fenced and uses a
one-minute grace; failures keep their reservations charged.

Retention deletes at most 200 resources/tombstones per batch. PostgreSQL removes
metadata and creates tombstones together; filesystem failures remain retryable
and do not release byte counters. A maintenance cycle drains up to eight such
batches within its existing ten-second deadline, releasing the storage lock
between batches and stopping when no full batch remains or the database/context
fails. Per-file failures receive persisted retry delays while other due files
continue. This also drains metadata-only source backlogs. Orphan reconciliation reads directories in
128-entry batches, looks up known paths in one SQL batch, stats only unknown or
mutable orphan files and registers them in bounded transactions. It never builds
a whole-disk membership map. Young unregistered files have a safety floor of the
job timeout plus source timeout plus one hour (and at least the configured TTL).
Temporary-tree deletion checks its deadline between entries and never follows
symlinks. Initial bootstrap adopts old sources, thumbnails and results without
removing them, committing bounded progress between attempts. It defers new disk
admission until accounting completes. See [maintenance progress and retry
limits](operations.md#retention-and-storage-pressure). External `cutmy maintenance` runs independently of the worker with
a ten-second budget, enabling cleanup even when the worker is stopped.

Current retention query improvements require the indexes installed by migration
003. Source candidate selection still reads retained source/job/preview dependency
rows and is O(N); bounding the final DELETE does not bound that selection cost.
Explicit-expiry artifact selection may read and sort all expired candidates
before choosing the oldest eligible batch. A large pinned backlog can also
increase candidate-probe work. See the reproducible storage-capacity measurements
for both improvements and regressions at different retained sizes: the
[2026-10-10 before/after report](../benchmarks/2026-10-10-storage-maintenance/README.md)
includes complete raw plans, ordinary service-budget progress, failed artificial
five-millisecond gates and slower full scans. Cache expiry remains an unbounded
DELETE on both measured versions.

Job polling fetches all artifact expiry records with one aggregate SQL query,
removing the previous potential twelve extra round trips. Source listing is
owner-scoped, capped at twenty and exposes current retention conditions. The
session response publishes only client validation limits. Clients can explicitly
remove unused source trees after confirmation; active jobs pin their data.

This is application admission rather than a filesystem hard quota. External
processes, database/docker logs and backups need their own limits and monitoring.
Concurrent open downloads can delay physical block reclamation after unlink;
the free-space check and safety margin protect new admission in that interval.
Higher worker concurrency still needs measured CPU/RAM limits and representative
load tests, rather than a promise based solely on server specifications.

A local PostgreSQL admission microbenchmark (Go 1.27.1, linux/amd64, Ryzen 9
5950X, four Go scheduler threads, 200 ms runs) measured 0.274 ms with an empty
ledger and 0.342 ms with 10,000 retained ledger rows. Both allocated 586 bytes
and 16 allocations per call. This measures one transactional counter query and
filesystem headroom check; it excludes uploads/encoding and is not a production
throughput guarantee. Counter triggers aggregate each SQL statement, so bulk
registration updates totals once rather than rewriting the total row per file.

## Bounded processing pool and measured server load

`WORKER_CONCURRENCY` defaults to one and is bounded to 1–8. Cancellation waits
for every processing goroutine to stop and reap its subprocesses before closing
the database or removing the health marker. Each job first reserves disk bytes.
Known video inputs up to 8,388,608 pixels share processing slots; larger or
unknown inputs acquire all slots of that worker process. Audio-only exports
use one slot. This prevents several 8K decoders from competing in a small memory
cgroup, while ordinary 1080p/UHD work can run concurrently. The existing job
deadline includes time waiting for the local semaphore. This is a conservative
schedule based on dimensions, not a measured prediction of decoder RAM.

An isolated run on the new production host (6 vCPU, 12 GB RAM) used the
production FFmpeg 5.1.9 image, an API capped at 1 CPU/512 MiB and a worker capped
at 3 CPUs/4 GiB. Twelve jobs per run mixed accurate MP4, MP3 and keyframe-copy
exports of six seconds from a synthetic 1080p/30 fps H.264/AAC source:

| Worker concurrency | Jobs/minute | Queue wait p95 | Total latency p95 | Worker cgroup memory peak |
| --- | ---: | ---: | ---: | ---: |
| 1 | 48.275 | 11.461 s | 11.784 s | 171.35 MiB |
| 2 | 58.077 | 8.687 s | 8.927 s | 265.67 MiB |
| 3 | 64.301 | 7.600 s | 8.752 s | 349.18 MiB |

A burst of 36 jobs at concurrency two admitted 32 and returned `job_limit` for
four. All 32 admitted jobs completed; total latency p95 was 28.093 seconds and
worker cgroup memory peaked at 378.11 MiB. These short synthetic exports measure
this workload, not user capacity, platform extraction availability or sustained
8K throughput. Cgroup memory includes more than process RSS. The measured
snapshot predates the final dimension-based semaphore; these 1080p inputs use
one slot in both versions. These historical throughput runs used an operator-specific
load harness that is not distributed with the core; the figures provide workload
context rather than a reproducible public load benchmark. The current
[metadata encoding comparison](../benchmarks/2026-10-10-metadata-compression/README.md)
includes its public harness, raw runs and environment.

## Storage maintenance and source listing, 2026-10-08

Source listing reads its twenty records, presentation fields and retention
conditions in one owner-scoped database snapshot, replacing up to twenty-one
queries. Single-source lookup uses the same decoder. Tombstones for one
retention/deletion batch are inserted in one statement; duplicate paths retain
the largest observed size. Successful physical deletions are acknowledged in
transactions of at most 200 paths. A failed acknowledgement leaves removed
files charged until the next retry, while failed physical removals remain
charged throughout.

Unchanged orphan files no longer start write transactions or rewrite their
ledger rows. Growth or a newer file modification time still updates conservative
byte counts and the safe retention floor. Modification times use PostgreSQL's
microsecond precision to avoid false changes on filesystems with nanoseconds.
Already pending orphan deletions no longer hide newly expired orphans from the
next bounded candidate batch. Directory reconciliation runs once per maintenance
cycle, regardless of the number of retention batches.

Median local microbenchmarks compare a clean previous revision against these
changes with identical fixtures: Go 1.27.1, linux/amd64, Ryzen 9 5950X, two Go
scheduler threads, isolated PostgreSQL 17 and temporary files on tmpfs. Each
measurement uses three sequential runs of ten iterations; fixture creation is
excluded from timings. These measure database/filesystem operations rather than
production encoding throughput.

| Operation | Before | After | Speedup |
| --- | ---: | ---: | ---: |
| List 20 sources | 5.193 ms | 0.886 ms | 5.86x |
| Acknowledge 200 removed files | 171.977 ms | 3.849 ms | 44.68x |
| Reconcile 128 unchanged orphans | 8.873 ms | 2.230 ms | 3.98x |
| Expire 200 sources and create tombstones | 91.419 ms | 37.009 ms | 2.47x |

The source-list allocation count fell from 495 to 214 per operation; deletion
acknowledgement fell from 4,203 to 837. Batched tombstone creation uses about
141 KiB more temporary Go allocation for its bounded arrays while making fewer
allocations and database round trips. Storage quotas, retained-media TTLs,
active-job pinning and the physical headroom check remain unchanged.

Integration tests cover multi-batch backlogs, the eight-batch ceiling, progress
past physical removal errors, active-source preservation, retry after
failed database acknowledgement, duplicate paths and changing orphan sizes.
Reproduce with `BenchmarkSourceList20`, `BenchmarkStorageDeleteBatch200`,
`BenchmarkStorageReconcileUnchanged128` and `BenchmarkStorageRetentionSources200`
using an isolated `TEST_DATABASE_URL`; never benchmark in the live schema.
