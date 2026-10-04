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

## Failure handling

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
# In an isolated PostgreSQL test database with the pinned yt-dlp executable:
go test ./internal/app -run '^$' -bench 'Metadata' -benchmem
```

See benchmark names and required environment in the `_bench_test.go` files.
PostgreSQL tests create/remove their own schemas. Do not benchmark against a
live application's schema or run competing encoders when comparing profiles.
The single worker bounds CPU/memory use; parallel transcoding needs explicit
resource admission rather than extra goroutines.
