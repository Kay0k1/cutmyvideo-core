# cutmyvideo-core

**English** · [Русский](README.md)

The open-source **cutmyvideo** engine for manually cutting video and audio. Choose a source and time ranges, then export separate MP4 or MP3 files. Includes a local CLI, an embeddable Go package, and an HTTP API with a persistent job queue.

[![Core checks](https://github.com/Kay0k1/cutmyvideo-core/actions/workflows/ci.yml/badge.svg)](https://github.com/Kay0k1/cutmyvideo-core/actions/workflows/ci.yml)

Hosted interfaces are developed separately. This repository contains a working independent engine and a self-hosting example. The current release is an **MVP** with manual selection; AI, MCP, the Telegram bot, and Mini Apps are outside this release.

## Contents

- [Features](#features)
- [Sources and limitations](#supported-sources-and-limitations)
- [Local clip](#quick-start-local-clip)
- [Server and API](#quick-start-server)
- [Configuration](#configuration)
- [Architecture](#architecture)
- [Development and checks](#development-and-checks)
- [Support](#support)
- [License](#license)

## Features

- Local files, uploads, direct HTTPS media links, and accessible public platform streams resolved by yt-dlp.
- Multiple ranges per request, with an independent result for each range.
- Compatible H.264/AAC MP4, MP3 audio, and MP4 stream copy.
- Accurate cuts with re-encoding or stream preservation with keyframe adjustments.
- PostgreSQL jobs with cancellation, retries after worker failure, leases, and idempotent submissions.
- Session ownership checks on sources, jobs, previews, and files; HTTP Range downloads.
- Configurable size, processing, queue, storage, and retention limits.

## Supported sources and limitations

| Source | MVP behavior |
|---|---|
| Local file | Processed locally without PostgreSQL |
| API upload | Staged on the server within the configured size limit |
| Direct HTTPS media link | **The entire file is downloaded first**, then cut |
| Platform page | yt-dlp resolves metadata; supported seekable MP4/WebM streams are read through a guarded HTTP Range relay |
| HLS/DASH manifests, DRM, login-required videos, live streams | Unsupported in this release |

Platform streams must be public, require no cookies, and support seeking. A platform can reject requests or stop exposing a suitable stream. yt-dlp's own platform support does not guarantee support by this engine.

YouTube accepts `watch?v=…`, `youtu.be/…`, `shorts/…`, `live/…` links to **completed recordings**, and `embed/…`, including mobile, music, and `youtube-nocookie.com` domains. Ordinary HTTP or schemeless pasted links are upgraded to `https://www.youtube.com/watch?v=…` only for allowed YouTube hosts. Playlist, tracking, and timestamp parameters are removed; export ranges are specified separately. Unknown subdomains, spoofed hosts, malformed video IDs, credentials, and nonstandard ports are rejected before network access. Other sources still require explicit HTTPS URLs.

Selective reads may transfer more bytes than the final clip. Each platform job has a total transfer budget; exceeding it stops processing. The engine does not silently download an unlimited long video as a fallback.

**Stream copy:** the start moves to a nearby preceding keyframe; the end depends on packet boundaries. Actual bounds are returned with the artifact. Input codecs must fit MP4; otherwise use accurate mode. A local file keeps its source resolution in copy mode; reducing resolution requires re-encoding.

**Accurate mode:** video is encoded to H.264/AAC. MP3 export also re-encodes audio; arbitrary source audio cannot be preserved as MP3.

Uploaded and direct-file previews serve source bytes. Playback depends on browser codec support. YouTube sources expose an embed URL; other platforms may require manual timecode entry without a player.

## Quick start: local clip

Requires Go 1.26+ and FFmpeg/ffprobe with libx264, AAC, and libmp3lame support.

```sh
go build -o cutmy ./cmd/cutmy
./cutmy clip --input interview.mp4 \
  --start 01:23:10 --end 01:23:40 --output moment.mp4

./cutmy clip --input interview.mp4 \
  --start 4990 --end 5020 --output moment.mp3

./cutmy clip --input interview.mp4 \
  --start 01:23:10 --end 01:23:40 --mode copy --output original.mp4
```

The CLI returns JSON with path, size, and actual bounds. Existing output files are never overwritten. Options include `--quality best|1080p|720p` and `--mode accurate|copy`. Local exports allow one range up to 24 hours and an output up to 10 GiB.

## Quick start: server

Requires Docker Compose. The example publishes the API only on `127.0.0.1:8080`.

```sh
printf 'POSTGRES_PASSWORD=%s\n' "$(openssl rand -hex 24)" > .env
docker compose --env-file .env -f deploy/compose.example.yml up --build -d
curl http://localhost:8080/readyz
```

Runs PostgreSQL, the API, and one worker sharing a media volume. Keep `.env` out of Git. Public deployment requires an HTTPS reverse proxy, an explicit `PUBLIC_ORIGIN`, and `COOKIE_SECURE=true`. Keep PostgreSQL and the core API on the internal network.

Set `TRUST_PROXY=true` **only** when the API cannot be accessed directly and your trusted proxy correctly sets `X-Forwarded-For`. Otherwise clients can influence IP-based rate limits. The self-hosting example leaves it disabled.

### API example

```sh
curl -c /tmp/cutmy.cookies http://localhost:8080/api/v1/session
curl -b /tmp/cutmy.cookies \
  -F file=@interview.mp4 http://localhost:8080/api/v1/uploads
```

Replace the source ID with the upload response's `id`:

```sh
curl -b /tmp/cutmy.cookies \
  -H 'Content-Type: application/json' \
  -H 'Idempotency-Key: first-export' \
  -d '{"source_id":"src_REPLACE","ranges":[{"start_ms":10000,"end_ms":40000,"label":"Quote"}],"format":"mp4","quality":"1080p","cut_mode":"accurate"}' \
  http://localhost:8080/api/v1/jobs
```

The response contains the job ID in `id`. Poll `GET /api/v1/jobs/{id}` and download `items[].artifact.download_url` using the same cookie. Reusing an idempotency key returns the original job. See [OpenAPI](api/openapi.yaml) and [API notes](docs/api.md).

## Configuration

| Variable | Default |
|---|---|
| `DATABASE_URL` | Required |
| `DATA_DIR` / `LISTEN_ADDR` | `./data` / `:8080` |
| `PUBLIC_ORIGIN` | Unset; set the exact public origin when deployed |
| `COOKIE_SECURE` / `TRUST_PROXY` | `false` / `false` |
| `MAX_SOURCE_BYTES` / `MAX_OUTPUT_BYTES` | 1 GiB / 1 GiB |
| `MAX_STORAGE_BYTES` / `MAX_OWNER_BYTES` | 10 GiB / 2 GiB |
| `MAX_RANGES` | 12 |
| `MAX_RANGE_MS` / `MAX_JOB_MS` | 10 minutes/range, one hour total |
| `MAX_ACTIVE_JOBS` | 32 globally; three per session |
| `MUTATIONS_PER_MINUTE` | 20 per IP, independent of session cookie |
| `SOURCE_TIMEOUT` / `JOB_TIMEOUT` | `2m` / `30m` |
| `SOURCE_TTL` / `ARTIFACT_TTL` | `24h` / `24h` |
| `FFMPEG_THREADS` | 2 |
| `FFMPEG_PATH` / `FFPROBE_PATH` / `YTDLP_PATH` | Corresponding executable names |

`MAX_OWNER_BYTES` applies to staged source files; results count toward the global storage limit. A session allows 20 source records and one concurrent source preparation. The API allows four concurrent source preparations globally. Admission reserves the configured maximum source size. Cleanup runs in the worker every five minutes; active jobs pin their required files.

The MVP supports **one API instance and one worker**. Durable leases and write fencing protect job recovery, but rate and disk admission limits are not distributed across API instances. Scale-out requires shared resource reservations. PostgreSQL stores durable state; source files and artifacts use disk. An S3 adapter is not yet implemented.

## Architecture

```mermaid
flowchart LR
    CLI[Local CLI] --> Engine[Shared engine]
    HTTP[HTTP API] --> DB[(PostgreSQL)]
    DB --> Worker[Worker]
    Worker --> Engine
    Engine --> FF[FFmpeg / ffprobe]
    Worker --> Guard[Address checks / relay]
    Guard --> Platform[yt-dlp / public streams]
    Worker --> Disk[(Files)]
    HTTP --> Disk
```

Go coordinates work; FFmpeg handles media. `pkg/engine` exposes local processing to other applications. Jobs retain an immutable snapshot of ranges and settings. A future MCP adapter can call the existing operations.

See the [security model](docs/security-model.md). Public deployment should also apply CPU/RAM/disk limits and container network isolation. Do not mount application secrets or private host volumes into media containers.

## Development and checks

```sh
go test ./...
go vet ./...
# Use a dedicated PostgreSQL test database:
TEST_DATABASE_URL=postgres://user:password@localhost:5432/test_db?sslmode=disable \
  go test -race -count=1 ./...
```

Media tests skip without FFmpeg; database integration tests skip without `TEST_DATABASE_URL`. CI installs both and runs all checks. Database tests create an isolated temporary schema.

Tests cover real MP4/MP3 outputs, keyframe shifts, blocked network addresses and playlists, ownership, idempotency, cancellation, concurrent claiming, fencing, and retention.

[Changelog](CHANGELOG.md) · [Contributing](CONTRIBUTING.md) · [Security reports](SECURITY.md)

## Support

Help fund development: [cutmy.video — support](https://cutmy.video/?panel=support).

## License

cutmyvideo-core is [MIT licensed](LICENSE). FFmpeg, yt-dlp, Node.js, PostgreSQL, and other dependencies have their own licenses. This project's license does not replace their terms; review them when distributing builds.
