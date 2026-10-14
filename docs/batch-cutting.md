# Saved cutting plans, audio selection and original streams

A version 1 JSON plan describes named intervals from one local source. Save the
[example plan](../examples/cuts.json), change the input and intervals, and run:

```sh
mkdir clips
cutmy inspect --input interview.mp4
cutmy preflight --plan cuts.json --output clips
cutmy batch --plan cuts.json --output clips
```

`input` paths inside plans resolve beside the plan file. `--output` resolves
from the current directory. Separate mode requires an existing output directory;
joined mode requires an output filename. Existing output files are preserved.
The whole batch shares a timeout and output budget; no database is needed.
Separate files are published as each interval finishes. If a later interval
fails, CLI stdout reports completed `outputs` with `incomplete: true` and stderr
reports the terminal error; the nonzero exit code still signals an incomplete
batch. The Go method returns completed outputs alongside its error.
Each range has a label and millisecond `start_ms`/`end_ms`; errors identify the
range and explain which boundary or limit to change. Up to 128 ranges are
accepted locally. Server limits remain configurable and are returned by session.
If a submitted plan exceeds that limit, the diagnostic states the configured
maximum and submitted count; remove extra intervals or submit another job.
Empty plans explain that a start and end must be marked before exporting.

For one output, set `"output_mode": "joined"` and run:

```sh
cutmy batch --plan cuts.json --output highlights.mp4
```

Intervals appear in submitted order, including reversed, overlapping and
repeated selections. The result reports actual source boundaries and each
segment's location on the output timeline. Accurate mode encodes the selected
video and audio; `quality: best` preserves display resolution and source cadence,
but is not bit-for-bit original quality. Copy mode preserves compatible video
and the selected audio packets and may include keyframe preroll. Its actual
boundaries are reported; it never falls back to accurate mode or reduces quality.

## Check a job before starting

`cutmy preflight --plan cuts.json --output clips` checks a saved plan without
rendering, creating output files or creating a workspace. For a joined plan,
pass the proposed filename, such as `--output highlights.mp4`. The output
directory or joined file's parent must already exist. Preflight and batch share
the same plan parser, defaults, range validation and output naming. Preflight
accepts `--profile fast|compact`, `--threads 1..32` and `--timeout 2m`.

Go callers use the additive method:

```go
report, err := engine.New(engine.Config{}).PreflightPlan(ctx, plan, output)
if err != nil {
    // engine.CodeOf(err) identifies the blocker.
    return err
}
// report.OutputPaths are proposed names; nothing has been written.
```

The version 1 JSON report contains `valid`, `source`, the normalized `request`,
`requested_duration_ms`, `output_count`, `track_manifest`, optional output
`video` geometry, `checks` and `warnings`. Go/CLI additionally return absolute
`output_paths`. A check has a `name` and `status` of `passed` or `deferred`;
warnings have stable machine-readable `code` and a human `message`. Successful
reports always have `valid: true`: the checks that could be completed passed.
Blocking errors use the existing Go error contract or CLI stderr; prefix the
CLI command with `--json-errors` for one JSON error envelope and a nonzero exit.

Local inspection checks a fresh source, range/settings limits, selected tracks,
crop/display geometry, timing prerequisites, and the exact encoders, muxers,
filters, demuxers and bitstream filters required from the configured FFmpeg.
Capability queries are limited to two seconds and 1 MiB per table. Unknown or
failed capability responses are explicitly deferred. A recognized table missing
a required capability produces `encoder_unavailable`, `muxer_unavailable`,
`filter_unavailable`, `demuxer_unavailable` or `bitstream_filter_unavailable`.
Copy never silently encodes or changes quality.

Preflight cannot guarantee an export. Payload decoding, copy codec/container
compatibility, packet/keyframe boundaries, final output size, execution/storage
admission and durable filesystem publication are explicitly deferred. Local
output paths are checked for current conflicts, but permission and publication
capabilities are checked by execution. Every export inspects and admits its
source again; a report is a snapshot, not a reservation.

HTTP callers send the existing export request to
`POST /api/v1/jobs/preflight` with their owner session and usual JSON/origin
guards. It returns the same report without server filesystem paths. This call
creates no job, downloads no remote media, reserves no output storage and does
not consume the new-export allowance. An independent allowance permits 30
checks per owner per minute, with bounded cross-process admission and renewable
reader protection during local inspection. Remote sources use persisted
metadata to check the request and explicitly defer media/track/tool inspection.
Removed media, ownership failures, malformed requests and known local blockers
use the documented HTTP error responses. See [OpenAPI](../api/openapi.yaml).

## Choose audio

Inspection lists every source stream with its absolute index, codec, default
flag, and language/title/channel count/sample rate when present. Use the absolute
audio index, not its position among audio streams. For example:

```sh
cutmy clip --input interview.mkv --start 5 --end 15 \
  --audio-track 2 --output quote.mp4
cutmy clip --input interview.mkv --start 5 --end 15 \
  --audio-track none --output silent.mp4
```

Plan `options` accept `audio_track: 2` or `no_audio: true`, never both. Omitted
selection preserves 1.x behavior: first default audio, otherwise first audio;
only one playable video and one audio stream are selected. MP3 requires audio.
The website uses the same selected audio for preview and export. Explicit audio
selection uses a server preview window, with its own selection-aware cache entry.
Remote platform sources support their default audio or silent video; choosing
an indexed track requires an uploaded/staged source whose tracks can be inspected.

## Copy, containers and subtitles

Choose `cut_mode: copy`, `quality: best` and `format: mp4` or `mkv` to preserve
compatible original streams. MKV is available only in copy mode. Container
compatibility is checked and failure requests a compatible container or accurate
mode; there is no automatic switch. The output stream manifest lists included
and excluded source tracks and their reasons. Additional video/audio streams,
attachments, chapters and data streams are excluded.

Subtitles are omitted by default. For local video copy, use
`subtitle_mode: preserve`, or the CLI `--subtitles preserve`. Plain text SubRip,
MOV text, WebVTT and text subtitles are clipped to each actual copied interval
and shifted onto its output timeline. MP4 writes MOV text; MKV writes SubRip.
Subtitle text may be converted between containers while video/audio remain
copied. Styled ASS/SSA and bitmap subtitles are rejected with an explicit
diagnostic; select omit to export such a source. Subtitle preservation is not
available for MP3, accurate mode or unstaged platform streams.

## Go and HTTP

Existing `engine.Options` and `Export` callers keep their three-field options
and default behavior. `SourceInfo`, `Result` and `Range` retain their original
struct shapes and comparability. New callers use `InspectWithTracks`,
`AdvancedOptions`, `ExportWithOptions`,
`Plan` and `ExecutePlan`. The plan uses the same named intervals and options as
the CLI. Read `BatchResult.Outputs` and its segment/track manifests after success.

HTTP jobs reuse `POST /api/v1/jobs` and the durable queue. Add `output_mode`,
`audio_track`/`no_audio` and `subtitle_mode` to the existing request. Inspect
local tracks through `GET /api/v1/sources/{id}/tracks` with the owner session.
Preview accepts matching `audio_track`/`no_audio` query parameters. A joined
job has one item/artifact; source intervals remain in its immutable request and
actual segments in the artifact. A separate job retains completed items if a
later item fails. Cancellation, lease fencing, idempotency, recovery, retention
and disk admission continue to use the existing job lifecycle. Joined copy
reserves both temporary segments and final output, and releases staging charges
only after temporary files are removed.

Accurate MP4 also supports `options.crop` in plans and `crop` in HTTP requests,
using normalized upright display coordinates. For example,
`{"x":0.25,"y":0,"width":0.5,"height":1}` keeps the middle half of the image.
Use `engine.AdvancedOptions.Crop` or CLI `--crop 0.25,0,0.5,1` for the same result.
The same area is applied to all separate or joined intervals. Copy/MKV/MP3
explicitly refuse crop. See [crop geometry](media.md#crop).

See [OpenAPI](../api/openapi.yaml), [API usage](api.md) and
[performance measurements](performance.md) for exact contracts and evidence.
The [short/one-hour export acceptance](../verification/2026-10-12-cutting-export/README.md)
covers accurate MP4, copied MP4 and MP3 from source opening through independent
download decoding, including the advertised range-count limit and one above it.

New fixed diagnostics include `audio_track_unavailable`, `subtitle_unsupported`,
`subtitle_unavailable`, `join_incompatible` and inspection `tracks_unavailable`.
Choose an inspected audio index, omit unsupported subtitles or select a
compatible container as indicated by the diagnostic. Unknown future codes should
use a generic client message. Raw media-tool output is never a retry instruction.
