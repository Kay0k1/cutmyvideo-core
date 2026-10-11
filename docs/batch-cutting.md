# Saved cutting plans, audio selection and original streams

A version 1 JSON plan describes named intervals from one local source. Save the
[example plan](../examples/cuts.json), change the input and intervals, and run:

```sh
mkdir clips
cutmy inspect --input interview.mp4
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

See [OpenAPI](../api/openapi.yaml), [API usage](api.md) and
[performance measurements](performance.md) for exact contracts and evidence.

New fixed diagnostics include `audio_track_unavailable`, `subtitle_unsupported`,
`subtitle_unavailable`, `join_incompatible` and inspection `tracks_unavailable`.
Choose an inspected audio index, omit unsupported subtitles or select a
compatible container as indicated by the diagnostic. Unknown future codes should
use a generic client message. Raw media-tool output is never a retry instruction.
