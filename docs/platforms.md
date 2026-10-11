# Native platform support

Linux, macOS and Windows are required for 1.0. Current-source release checks
run on each shipped target, rather than accepting cross-compilation alone:

| Target | Native runner | Required runtime coverage |
| --- | --- | --- |
| Linux amd64 | Ubuntu 24.04 | CLI, library, media publication, cancellation, API and worker |
| Linux arm64 | Ubuntu 24.04 ARM | Same |
| macOS arm64 | macOS 15 | Same |
| macOS amd64 | macOS 15 Intel | Same |
| Windows amd64 | Windows Server 2025 | Same |

The native gate requires FFmpeg/ffprobe and an isolated PostgreSQL installation.
It checks accurate MP4, MP3 and stream-copy output, error codes, exclusive
publication, descendant-process cancellation and a real session/upload/preview/
queue/worker/download flow, including ownership and HTTP Range. Missing tools,
unsupported publication or skipped required tests fail the gate. JSON test
reports and tool versions are retained as CI artifacts. The Linux full suite
also covers fault injection, HTTP contracts and historical upgrade/restore.

Native storage checks also require real filesystem-space observations, missing
and invalid data-directory paths, and PostgreSQL admission with physical
headroom, safety bytes and outstanding reservations. A logically available
budget cannot bypass insufficient physical space on any of these five targets.

The [archive gate](releases.md#automation) additionally runs the actual extracted
CLI packages on these same five targets. It checks independently supplied source
and archive identities, actual embedded version output, local media and errors,
timeout cancellation, an external server/worker and deletion/accounting. Unix
also exercises SIGINT/SIGTERM. Windows timeout acceptance does not establish
console Ctrl-C delivery. The archived executable is not rebuilt during these
jobs; source/library tests remain a separate prerequisite.

Current-source checks additionally require a separate media-contract report on
each target: rotation and sample-aspect-ratio normalization, variable-frame-rate
timestamps, known-signal PQ/HLG tone mapping and compatible copy, default-audio
selection, attached-picture exclusion, selected-video copy caps and fresh SDK
coded/display dimensions. The production-family Bookworm/FFmpeg 5.1 job requires
the same media-contract tests, including actual HLS staging/continuity metadata
and subprocess capability/cancellation diagnostics, alongside the existing
copy/HLS timeline checks. The gate rejects missing tests and skipped subtests.

These are the tested OS baselines, not a promise for every older release,
network filesystem, external provider or hardware controller. Official
v0.2.1 archives predate native macOS/Windows verification and archive execution.
New source changes
must pass all native checks before publishing a release with these guarantees.

## Windows local CLI

Extract the Windows archive and install FFmpeg and ffprobe from the
[FFmpeg download page](https://ffmpeg.org/download.html). Put both executables
on `PATH`; MP4/MP3 encoding requires libx264, AAC and libmp3lame. Current-source
HDR accurate exports and previews also need zscale/libzimg and tonemap; missing
conversion capabilities have the fixed `hdr_conversion_unavailable` diagnostic.
See the [media policy](media.md). FFmpeg is not
included in CLI archives. In PowerShell:

```powershell
.\cutmy.exe version
.\cutmy.exe inspect --input recording.mp4
.\cutmy.exe clip --input recording.mp4 --start 10 --end 30 --output clip.mp4
.\cutmy.exe --json-errors clip --input recording.mp4 --start 10 --end 30 --output clip.mp3
```

Use a local filesystem that supports hard links and synchronization (the native
runner exercises NTFS). Staging and output must be on the same filesystem.
Outputs are never overwritten; an uncertain publication retains its final name.
Windows tools start suspended, join a private job, then resume. Their descendants
inherit that job and are terminated on cancellation or job-handle closure.

## macOS and Linux

Install FFmpeg/ffprobe with the required codecs and use the
[README CLI commands](../README.en.md#local-trimming). Media commands run in
their own process group so cancellation terminates descendants. Required native
tests exercise file and directory synchronization and exclusive hard-link
publication on the runner's local filesystem.

On macOS, use Homebrew's [full FFmpeg formula](https://formulae.brew.sh/formula/ffmpeg-full),
which includes zimg for current-source HDR conversion. It is keg-only, so select
its binaries explicitly:

```sh
brew install ffmpeg-full
export PATH="$(brew --prefix ffmpeg-full)/bin:$PATH"
```

For Intel macOS, the versioned [FFmpeg 7 formula](https://formulae.brew.sh/formula/ffmpeg@7)
also includes zimg and provides an Intel bottle; select its keg-only binaries:

```sh
brew install ffmpeg@7
export PATH="$(brew --prefix ffmpeg@7)/bin:$PATH"
```

The native ARM runner uses `ffmpeg-full`; the Intel runner uses `ffmpeg@7`
so installation can use its compatible Intel bottle. Both execute the full media
contract and retain the installed tool versions.

The regular Homebrew `ffmpeg` formula may omit zscale/libzimg. The native gate
runs a one-frame zscale/tonemap conversion with a 15-second deadline before the
tests; missing conversion capabilities fail the gate rather than skipping HDR.

## Self-hosting

The [Compose example](../deploy/compose.example.yml) runs Linux containers and
is the documented operator setup on Linux or Docker Desktop. Native service
tests exercise HTTP handlers and a worker against PostgreSQL on each platform;
they do not establish OS-specific service installation, Windows service-manager
integration or macOS launchd configuration. These remain operator choices.

The [coordinated upgrade/backup/restore gate](backup-recovery.md) runs on Linux,
including a separate Linux amd64 check using the executable extracted from the
sealed archive bundle.
Restore media at the same canonical `DATA_DIR`; the database currently stores
absolute file paths. Follow the [migration policy](migrations.md) before changing
database versions or rolling back a binary.
