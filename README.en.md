# cutmyvideo-core

**Video and audio clipping for applications, scripts and automation.**

[![Core checks](https://github.com/Kay0k1/cutmyvideo-core/actions/workflows/ci.yml/badge.svg)](https://github.com/Kay0k1/cutmyvideo-core/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/Kay0k1/cutmyvideo-core)](https://github.com/Kay0k1/cutmyvideo-core/releases/latest)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

[Русский](README.md) · **English** · [Documentation](docs/README.md) · [Releases](https://github.com/Kay0k1/cutmyvideo-core/releases) · [Website](https://cutmy.video)

Choose intervals from a recording and receive separate clips, one joined file or a collection of independent assembled clips. The engine is written in Go and uses FFmpeg to process media. Embed it in a Go application, invoke the CLI or connect over HTTP. [cutmyvideo-web](https://github.com/Kay0k1/cutmyvideo-web) is the separate browser editor built on this core.

<a id="architecture"></a>

## Choose an interface

| Task | Interface | Runtime requirements |
| --- | --- | --- |
| Cut local files | [CLI](docs/usage.md#cli) | `cutmy`, FFmpeg and ffprobe |
| Embed the engine in Go | [`pkg/engine`](docs/usage.md#go-library) | Your Go application, FFmpeg and ffprobe |
| Call from Python or TypeScript | [SDK](docs/sdk.md) | Local: CLI and media tools; HTTP: a server URL |
| Provide an API with a durable job queue | [Self-hosted server](docs/self-hosting.md) | API, worker, PostgreSQL and media storage |

Local cutting works without a database, accounts or an HTTP server. SQL belongs to server job persistence; TypeScript belongs to the client SDK. See [architecture](docs/architecture.md) for component boundaries and dependencies.

## Versions

Install CLI archives from the [latest published release](https://github.com/Kay0k1/cutmyvideo-core/releases/latest). Archives support Linux and macOS (amd64/arm64), and Windows (amd64); install FFmpeg separately. Follow [signed-release verification](docs/releases.md#consumer-verification-of-a-new-signed-release) before installation.

**1.3.0** adds editable clip collections, draft assemblies, job preflight and the Python/TypeScript SDK foundation. These are absent from the v1.2.0 binary. SDKs currently use development versions and install from source; PyPI/npm publication is pending. The [changelog](CHANGELOG.md) separates published changes from `Unreleased`.

<a id="local-trimming"></a>

## Quick start: a local file

Install FFmpeg/ffprobe with libx264, AAC and libmp3lame. Use a verified release binary or build current sources with Go 1.26.9+ (CI and releases use 1.27.2):

```sh
git clone https://github.com/Kay0k1/cutmyvideo-core.git
cd cutmyvideo-core
go build -trimpath -o cutmy ./cmd/cutmy
./cutmy version
./cutmy inspect --input interview.mp4
./cutmy clip --input interview.mp4 \
  --start 00:00:05 --end 00:00:15 --output quote.mp4
```

The result is JSON containing the path, size and actual boundaries. Existing files are preserved. Accurate mode re-encodes the interval; `--mode copy` preserves compatible original streams and may start at the preceding keyframe. On Windows, run `cutmy.exe`. See [options, errors and limits](docs/usage.md).

### Multiple clips and preflight

With core 1.3.0 or a current source build:

```sh
cp examples/cuts.json cuts.json
# Adjust input and intervals in cuts.json for your recording.
mkdir clips
./cutmy preflight --plan cuts.json --output clips
./cutmy batch --plan cuts.json --output clips
```

Plan `input` paths resolve relative to the plan file. For a joined result, set `"output_mode": "joined"` and pass an output filename instead of a directory. Preflight reports passed and deferred checks; it writes no media and cannot guarantee payload decoding or final size. See [plans, preflight and joining](docs/batch-cutting.md).

### Independent clips and drafts

Save one collection with stable clip IDs and ordered fragments per clip. Validate the plan, export a lower-quality MP4 draft, then render selected IDs after revisions. Per-clip crop and audio options follow the same edits into final exports. Go, CLI and HTTP return source/output mappings.

[Collection guide](docs/clip-collections.md) · [Agent workflow](docs/agent-workflow.md)

## Python and TypeScript

Both SDKs support **check a job → run it → retrieve results**, locally and over HTTP. They provide typed errors, partial results, cancellation, waiting and streaming file transfers. Neither has third-party runtime library dependencies; video processing remains in Go/FFmpeg.

Plans are ordinary JSON for editors, services or automated assistants. For HTTP jobs, callers persist an idempotency key before submission so they can recover after a lost connection.

[Installation and examples](docs/sdk.md) · [Python](sdk/python/README.md) · [TypeScript](sdk/typescript/README.md)

<a id="self-hosting"></a>

## Run an HTTP server

The local example starts PostgreSQL, API and a worker sharing media storage:

```sh
umask 077
printf 'POSTGRES_PASSWORD=%s\n' "$(openssl rand -hex 24)" > .env
docker compose --env-file .env -f deploy/compose.example.yml up --build -d
curl --fail http://localhost:8080/readyz
```

The API listens on `127.0.0.1:8080`. This example uses reduced development limits. For a public installation, use the [HTTPS hosting guide](docs/self-hosting.md); the [documentation index](docs/README.md#hosting-and-operations) covers configuration, backups and upgrades.

[HTTP API](docs/api.md) · [OpenAPI](api/openapi.yaml) · [Upload-to-download example](docs/usage.md#http-api)

<a id="features"></a>
<a id="sources-and-boundaries"></a>

## Capabilities and boundaries

- Named intervals, separate files and ordered joins, including repeated intervals.
- Editable clip collections, assembled MP4 drafts, per-clip crop and selected-ID re-exports.
- MP4 H.264/AAC, MP3, compatible stream copy in MP4/MKV; audio and text-subtitle selection.
- Cropping for accurate MP4, display rotation and pixel aspect ratio handling, bounded previews and waveforms.
- Local files, API uploads, direct HTTPS files and supported public recordings through yt-dlp.
- Server: durable queue, private sessions, idempotency, cancellation, recovery and resource limits.

Accurate exports retain the requested quality settings, but re-encoding does not imply byte-identical original media. Copy preserves compatible streams with actual packet/keyframe boundaries. The [media policy](docs/media.md) covers tracks, geometry and color.

Recognizing a platform does not guarantee access to every recording. Direct HTTPS files download in full; supported progressive and completed HLS recordings can read selected portions. Live streams, DRM and private recordings are rejected. See [providers and limitations](docs/providers.md).

Measured improvements apply to specific reproducible workloads: [methods and results](docs/performance.md). See [platform support](docs/platforms.md) for tested operating systems and required release checks.

<a id="development"></a>
<a id="license"></a>

## Development and license

Start with [CONTRIBUTING](CONTRIBUTING.md). `make check` runs fast checks; `make check-full` requires media tools, pinned yt-dlp and isolated test PostgreSQL. The contributor guide covers separate installed SDK acceptance.

[Issues](https://github.com/Kay0k1/cutmyvideo-core/issues) · [Security](SECURITY.md) · [Changelog](CHANGELOG.md) · [Support the project](https://cutmy.video/?panel=support&lang=en)

Core and SDK code use [MIT](LICENSE). FFmpeg, yt-dlp, Node.js, PostgreSQL and other dependencies retain their own licenses: [third-party notices](THIRD_PARTY_NOTICES.md).
