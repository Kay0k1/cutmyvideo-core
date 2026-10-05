# Performance and recovery

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

Storage admission still stats retained regular files to obtain a fresh byte
sum; it is O(number of files), not a distributed reservation system. Retention
still holds the database's registered path set for orphan membership checks.
Large deployments should measure these scans and enforce a filesystem quota
before adding worker concurrency. The audit does not change encoding quality
or claim reduced FFmpeg RSS; source decoding and encoding remain the main CPU
and memory consumers for ordinary exports.

Each API/worker PostgreSQL pool now defaults to four maximum connections.
The pinned pgx default uses the host's `runtime.NumCPU()`, which does not honor
a container CPU quota. Connections are opened on demand, so this is a limit on
growth under concurrency, not a measured reduction in idle RSS. Explicit
`pool_max_conns` in `DATABASE_URL` remains authoritative; larger explicit
`pool_min_conns` or `pool_min_idle_conns` raise the default maximum accordingly.
Tune these settings together with PostgreSQL's connection budget when scaling
instances. No new queue/cache indexes were added without representative query
plans: active jobs are already bounded, while large retention tables and the
registered-path set remain candidates for load testing.

## Failure handling

- CLI interrupt/termination signals cancel the inspection/encoding context before
  exit, killing and reaping the isolated Unix media process group. Previously a
  terminated CLI could leave FFmpeg or FFprobe running outside its parent group.

- Cached addresses rejected with HTTP 401/403/404/410 receive at most one
  forced fresh resolution per job. Fresh refusals, encoding failures, limits
  and cancellation are terminal. Refresh preserves source identity/timeline
  and the transfer budget; initial cached/fresh inspection shares one timeout.
- Range/HEAD and guarded DNS checks remain intact. One 15-second deadline
  covers DNS and ordered connection attempts; all returned addresses are
  validated first. Cancellation closes active relay tunnels.
- Queue claims and lease heartbeats have five-second database budgets.
  Retention runs separately with a ten-second cycle budget and does not keep
  a stuck worker's health marker alive.
- Linux admission checks actual available filesystem space as well as logical
  retained bytes. Scans stop early when over quota and respect cancellation.
  This remains single-API admission, not distributed reservation.
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
# In an isolated PostgreSQL test database with the pinned yt-dlp executable:
go test ./internal/app -run '^$' -bench 'Metadata' -benchmem
```

See benchmark names and required environment in the `_bench_test.go` files.
PostgreSQL tests create/remove their own schemas. Do not benchmark against a
live application's schema or run competing encoders when comparing profiles.
The single worker bounds CPU/memory use; parallel transcoding needs explicit
resource admission rather than extra goroutines.
