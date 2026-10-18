# Analysis, rendering and cut-list exchange (1.5.0)

These opt-in operations are available in Go, CLI, HTTP and both SDKs. Existing
plans retain their previous behavior. The separate browser editor does not gain
new controls from this core release.

## Suggest cuts from pauses

```sh
cutmy analyze silence --input interview.mp4 --start 0 --end 300 \
  --threshold-db -35 --min-silence-ms 500 \
  --keep-before-ms 100 --keep-after-ms 150 > pauses.json
cutmy analyze silence --input interview.mp4 --start 0 --end 300 \
  --keep-before-ms 100 --keep-after-ms 150 --plan > proposed-cuts.json
# Review and edit the ranges before execution.
cutmy preflight --plan proposed-cuts.json --output edited.mp4
cutmy batch --plan proposed-cuts.json --output edited.mp4
```

Detection uses the strongest channel's 10 ms RMS rather than mixing channels,
so opposite-phase stereo does not disappear through cancellation. Quiet speech
can still look like silence; this is a proposed edit, not speech recognition.
`--min-clip-ms` explicitly omits shorter proposed clips; zero retains them.
Use `--audio-track` for an inspected absolute stream index. The result reports
actual coverage; plan creation refuses silently dropping an unexamined tail.
An omitted or zero `threshold_db` uses -35 dBFS; omitted or zero
`min_silence_ms` uses 500 ms. Other silence margins and `min_clip_ms` keep zero.

## Inspect and normalize loudness

```sh
cutmy analyze loudness --input interview.mp4 --start 0 --end 60
cutmy clip --input interview.mp4 --start 5 --end 35 --output levelled.mp4 \
  --audio-normalize --normalize-lufs -16 --normalize-true-peak -1 --normalize-lra 11
```

The report contains integrated LUFS, true peak dBTP and loudness range LU;
unmeasurable silent metrics are JSON `null`. Normalization measures the final
selected audio timeline, then applies a calibrated second dynamic loudnorm
pass. Joined ranges are measured together in submitted order, including repeats.
Targets apply before lossy encoding: AAC/MP3 can change the measured result.
Analyze the completed file when delivery requirements concern its encoded peak.

Saved `audio_normalize` requires all three numeric fields: `integrated_lufs`
(-70..-5), `true_peak_dbtp` (-9..0), `loudness_range_lu` (1..50).

## Contain a picture in an output canvas

```sh
cutmy clip --input interview.mp4 --start 5 --end 15 --output vertical.mp4 \
  --quality best --canvas 1080x1920 --canvas-background '#101820'
```

Canvas contains the displayed picture without stretching or removing pixels.
It follows existing rotation, pixel aspect ratio, crop and color handling.
Choose even width/height from 2 through 4096, `fit: "contain"`, and a solid
`#RRGGBB` background. A square canvas is, for example, 1080x1080. It requires
accurate MP4, final rendition and quality `best`. Automatic subject selection,
cropping and blurred backgrounds are outside this operation.

## Replace audio on the source timeline

```sh
cutmy clip --input interview.mp4 --start 5 --end 15 --output replaced.mp4 \
  --audio-replace recorder.wav --audio-offset-ms 250 \
  --replacement-audio-track default
```

A positive 250 ms offset places replacement time zero at source time 250 ms;
a negative offset starts it earlier. Missing audio becomes silence without
shortening video. The same clock applies to joined/reordered/repeated ranges.
Choose an inspected absolute audio stream index for a multi-track replacement.
Offsets are bounded to ±86,400,000 ms; replacement audio supports 1–8 channels.

Local plans use `audio_replace: {"input":"recorder.wav","offset_ms":250}`.
Relative replacement paths resolve against the plan directory. HTTP jobs use
`source_id` instead of `input`: upload both recordings in the same owner session.
Filesystem paths and provider/remote recordings are rejected for replacement.
A project handoff covers its primary recording; adding replacement audio requires
the owner's session. Retained accepted work remains recoverable before new checks.

Normalization and replacement require accurate MP4 or MP3 and conflict with
`no_audio`; replacement conflicts with original `audio_track`. Collection clips
inherit these options and can use `clear_audio_normalize`, `clear_canvas`,
`clear_audio_replace`. Portable SDK documents cannot transfer replacement paths
or server IDs; bind replacement audio explicitly after import.

The new rendering options require subtitles omitted. Existing embedded text
subtitle preservation remains available in copy mode; incompatible combinations
are rejected before encoding instead of silently losing captions.

## Exchange cuts and chapters

CSV uses three columns without a header: start seconds, end seconds, label.
Quoted labels follow CSV rules. An empty end is a marker, expanded explicitly
against the next entry or supplied source duration.

```sh
cutmy cutlist import --format csv --input cuts.csv > cut-list.json
cutmy cutlist import --format csv --input cuts.csv --plan \
  --media interview.mp4 --duration-ms 60000 > cuts.json
cutmy cutlist export --format ffmetadata --plan cuts.json > chapters.json
# Export returns JSON; text contains the FFmetadata document.
jq -r .text chapters.json > chapters.ffmeta
```

FFmetadata supports explicit chapter `TIMEBASE`, `START`, `END` and escaped
titles. Exchange is bounded to 1 MiB, 128 entries and millisecond precision.
CSV preserves requested order, overlaps and repeats. FFmetadata export requires
explicit chronological non-overlapping intervals. Use `--segments` with an
actual export's Segment array for **output** timestamps; `--plan` uses requested
**source** intervals. Chapter embedding and container-specific rewriting are
outside this sidecar operation.

## Find scene changes and black intervals

```sh
cutmy analyze video --input interview.mp4 --start 0 --end 300 \
  --scene-threshold 0.3 --min-black-ms 500 > visual-events.json
```

Results contain source timestamps, scores and black intervals without changing
the plan. These are visual thresholds, not semantic scene understanding.
Omitted or zero `scene_threshold`, `black_pixel_threshold`,
`black_picture_ratio` and `min_black_ms` use 0.3, 0.1, 0.98 and 500 ms
respectively. This default convention applies to analysis; normalization's
explicit `true_peak_dbtp: 0` remains a real 0 dBTP target.

Silence/video windows are bounded to 30 minutes, with decoded-sample, frame and
event limits. HTTP loudness windows also stop at 30 minutes; local loudness
accepts finite windows up to 24 hours. Analysis is read-only, requires a regular
local file (an owned uploaded source for HTTP), and supports cancellation.
Use smaller windows when a limit is reached.

HTTP uses `POST /api/v1/sources/{id}/analysis/silence`, `/video`, `/loudness`
with the corresponding options object. `POST /api/v1/cut-lists/import` accepts
`{format,text}`; `/export` accepts `{format,cut_list}` or `{format,segments}`.
See [OpenAPI](../api/openapi.yaml) and [SDK usage](sdk.md) for exact contracts.
