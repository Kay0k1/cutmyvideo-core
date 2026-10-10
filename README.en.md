# cutmyvideo-core

**English** · [Русский](README.md)

An open tool for manually trimming video and audio: choose a source and intervals, get separate MP4 or MP3 files. Use the local CLI, Go library, or HTTP API with a durable job queue. [cutmy.video](https://cutmy.video) uses this engine; the product UI is maintained in a private repository. The core runs and can be hosted independently.

[![Core checks](https://github.com/Kay0k1/cutmyvideo-core/actions/workflows/ci.yml/badge.svg)](https://github.com/Kay0k1/cutmyvideo-core/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

The project is **0.x**. Local trimming, recovery, bounded previews and long exports are implemented; individual providers can restrict access. Current-source CI requires real media, durable publication, process cancellation and API/worker operation on Linux, macOS and Windows across all five shipped architectures. Published v0.2.1 was runtime-tested on Linux; new guarantees apply to current source and must pass CI before the next release. See [platform support](docs/platforms.md), the [changelog](CHANGELOG.md) and [source catalogue](docs/providers.md).

## Features

- Local files, API uploads, direct HTTPS files and accessible public recordings through yt-dlp.
- Multiple intervals in one job, with a separate result for each.
- Accurate H.264/AAC MP4, MP3 and fast stream-copy MP4.
- Original-file playback, YouTube embeds and short server-rendered MP4 windows for platforms or browser codec fallback.
- PostgreSQL queue, history, cancellation, idempotency, restart recovery and stale-worker fencing.
- Configurable byte/time/storage/concurrency budgets, retention cleanup and explicit source deletion.
- Session-cookie ownership for sources, previews and outputs; HTTP Range downloads.

## Local trimming

Install **FFmpeg and ffprobe** with libx264, AAC and libmp3lame. Download your OS/architecture archive from [GitHub Releases](https://github.com/Kay0k1/cutmyvideo-core/releases), verify it against `SHA256SUMS`, and extract it. FFmpeg is external. Building from source requires Go 1.26.9+; CI and releases use Go 1.27.2.

```sh
git clone https://github.com/Kay0k1/cutmyvideo-core.git
cd cutmyvideo-core
go build -trimpath -o cutmy ./cmd/cutmy
./cutmy --help
./cutmy version
./cutmy inspect --input interview.mp4

./cutmy clip --input interview.mp4 \
  --start 01:23:10 --end 01:23:40 --output moment.mp4
./cutmy clip --input interview.mp4 \
  --start 4990 --end 5020 --output moment.mp3
./cutmy clip --input interview.mp4 \
  --start 01:23:10 --end 01:23:40 --mode copy --output original.mp4
```

Output is JSON with path, byte size and actual bounds. Existing outputs are never overwritten. Options include `--quality best|1080p|720p`, `--profile fast|compact`, `--threads 1..32` and `--timeout 30m`. Accurate mode re-encodes; copy begins at a preceding keyframe and preserves resolution. MP3 requires accurate mode. Local intervals can span up to 24 hours; default limits are 10 GiB per output and 30 minutes per export. The library can change byte/deadline limits. See the [user and library guide](docs/usage.md).

Builds from the current source include public library error codes and optional
`cutmy --json-errors clip …` diagnostics. The output directory must support hard
links and synchronization to storage; these capabilities are checked before
encoding. Local export success follows synchronization of the file and directory
entry. See the [guide](docs/usage.md) for the contract and limits.

## Self-hosting

Docker Compose runs PostgreSQL, API and worker with shared media storage. The example listens only on `127.0.0.1:8080` and deliberately uses lower budgets than the server defaults.

```sh
umask 077
printf 'POSTGRES_PASSWORD=%s\n' "$(openssl rand -hex 24)" > .env
docker compose --env-file .env -f deploy/compose.example.yml up --build -d
curl --fail http://localhost:8080/readyz
```

Keep `.env` private. Public hosting needs HTTPS, an exact `PUBLIC_ORIGIN`, `COOKIE_SECURE=true`, resource limits and network isolation. Use `TRUST_PROXY=true` only when the API is reachable solely through a trusted proxy that normalizes `X-Forwarded-For`. The current boundary is one API instance: its IP limiter lives in the process. Queue leases and disk reservations are shared through PostgreSQL. A worker can process 1–8 jobs; start with one.

[Independent HTTPS hosting](docs/operations.md#https-with-caddy) · [Operations, upgrades and backups](docs/operations.md) · [All configuration](docs/configuration.md) · [Security model](docs/security-model.md)

### Minimal API request

```sh
curl --fail -c /tmp/cutmy.cookies http://localhost:8080/api/v1/session
curl --fail -b /tmp/cutmy.cookies \
  -F file=@interview.mp4 http://localhost:8080/api/v1/uploads
```

Replace the source `id` in this request:

```sh
curl --fail -b /tmp/cutmy.cookies \
  -H 'Content-Type: application/json' -H 'Idempotency-Key: first-export' \
  -d '{"source_id":"src_REPLACE","ranges":[{"start_ms":10000,"end_ms":40000,"label":"Quote"}],"format":"mp4","quality":"1080p","cut_mode":"accurate"}' \
  http://localhost:8080/api/v1/jobs
```

The job ID is returned as `id`. Poll `GET /api/v1/jobs/{id}`; download `items[].artifact.download_url` using the same cookie when ready. A failed interval does not hide successful outputs. See the [OpenAPI contract](api/openapi.yaml), [API details](docs/api.md), and [complete upload-to-download example](docs/usage.md#http-api).

## Sources and boundaries

| Source | Reading and limits |
|---|---|
| Local file | Processed on your computer; no PostgreSQL |
| API upload | Saved on the server within the byte limit |
| Direct HTTPS file | The **entire file is downloaded** before trimming |
| Public progressive recording/clip | Verified media uses a guarded HTTP Range relay |
| Completed HLS recording | Only selected segments and decoder context are staged |
| Current live broadcast, DRM, private recording | Explicit errors; no account authentication or access bypass |

The catalogue recognizes **15 platforms**: YouTube, Twitch, Rutube, TikTok, Instagram, Vimeo, Dailymotion, VK, Facebook, X, Reddit, OK, Bilibili, Streamable and Rumble. Real imports/exports were checked for YouTube, Twitch VOD/clip and Rutube; Streamable metadata was checked. These are observations, not guarantees for every video. Accepted link forms, evidence and other adapters are listed in the [provider catalogue](docs/providers.md).

HLS requires a finite unencrypted `ENDLIST`. MPEG-TS/fMP4 and up to 64 compatible continuity periods with initialization-map changes or timestamp resets preserve the original timeline. Byte ranges, low-latency parts, separate HLS audio/video, incompatible codec changes and DASH fragments are refused. Compatible separate progressive HTTPS streams are supported.

Window previews return up to 30 seconds of 480p H.264/AAC MP4, capped at 16 MiB and aligned to 30-second positions. Completed windows have a private 30-minute idle cache: up to 512 MiB/64 files globally and 128 MiB/16 files per session. Every request checks ownership; source deletion removes its windows. At most two windows render concurrently; background prefetch yields to foreground seeks. The source still describes the entire recording.

Server defaults permit 12 hours per fragment and 24 hours total, subject to deadlines, output size, shared transfer budget and disk headroom. Long accurate exports use bounded bitrates to fit output budgets; copy keeps the original stream and can exceed its budget. `best` preserves resolution; copy with a lower explicit resolution limit returns `copy_quality_unsupported`. See [configuration](docs/configuration.md).

## Architecture

```mermaid
flowchart LR
    CLI[Local CLI] --> Engine[Go engine]
    API[HTTP API] --> DB[(PostgreSQL)]
    DB --> Worker[Worker]
    Worker --> Engine
    Engine --> FF[FFmpeg / ffprobe]
    Worker --> Guard[Address checks / relay]
    Guard --> Source[yt-dlp / public streams]
    Worker --> Disk[(Media)]
    API --> Disk
```

`pkg/engine` provides local `Inspect` and `Export` without HTTP or a database. Server jobs retain immutable inputs and publication verifies the current lease. Direct library calls do not use server quotas. Public hosting needs CPU/RAM/PID/disk limits in addition to application budgets. Measurements and before/after methodology are in [performance](docs/performance.md).

## Development

```sh
make check
# With FFmpeg, ffprobe, pinned yt-dlp and dedicated PostgreSQL:
TEST_DATABASE_URL=postgres://user:password@localhost:5432/test_db?sslmode=disable \
  make check-full
```

Fast checks can skip media/database tests without their tools. Full checks require all dependencies, run race tests, check documentation/workflows and scan reachable vulnerabilities. CI uses synthetic media and temporary PostgreSQL schemas, with no requests to public platforms. [CONTRIBUTING](CONTRIBUTING.md) covers setup and changes; [releases](docs/releases.md) covers five CLI archives and reproducibility checks.

Problems and ideas: [GitHub Issues](https://github.com/Kay0k1/cutmyvideo-core/issues). Vulnerabilities: [SECURITY](SECURITY.md). Fund development: [cutmy.video](https://cutmy.video/?panel=support).

## License

[MIT](LICENSE). FFmpeg, yt-dlp, Node.js, PostgreSQL and Go dependencies retain their own licenses; the engine license does not replace their terms.
