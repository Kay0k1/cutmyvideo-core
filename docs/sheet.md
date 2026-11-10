# Contact sheets

`cutmy sheet` lays frames of a recording out on a labelled grid and prints an
index that maps every cell to its source time. It lets a person, a script or
an AI agent that cannot play video look at footage before cutting it: read the
picture, then use the index to turn a cell into an exact timestamp.

The operation is local and read-only. It is available in the CLI and in Go; it
needs FFmpeg and ffprobe, but not a database, the `drawtext` filter or any
installed font.

## Quick start

```sh
# 24 frames spread evenly over the recording
cutmy sheet --input interview.mp4 --output sheet.jpg

# One frame every two seconds of the second minute, numbered and timed
cutmy sheet --input interview.mp4 --range 00:01:00-00:02:00 --every 2s \
  --label index,time --output minute-2.jpg

# Exact moments, as lossless PNG
cutmy sheet --input interview.mp4 --at 12.5,00:01:03.200,95 --output moments.png

# One frame from the middle of every scene
cutmy sheet --input interview.mp4 --scenes --output scenes.jpg
```

The command prints one JSON object on stdout and nothing else. Errors go to
stderr; prefix the command with `--json-errors` for a JSON error, as with the
other local commands.

## Choosing frames

Use exactly one of these; without any of them the sheet has `--count 24`.

| Flag | Frames |
| --- | --- |
| `--count N` | The middles of `N` equal parts of the range, so neither the very first nor the very last frame is used |
| `--every D` | The start of the range and every `D` after it: `2s`, `500ms`, or plain seconds such as `2` |
| `--at t1,t2,…` | The listed times in the given order; repeats are allowed |
| `--scenes [file]` | The middle of every scene |

`--range A-B` limits `--count`, `--every` and `--scenes` to a source interval
and requires every `--at` time to lie inside it. Either side may be omitted:
`90-` runs to the end. Times are seconds or `HH:MM:SS.mmm` on the source clock,
the same clock `cutmy clip --start` uses. A time must be before the source
duration reported by `cutmy inspect`.

Bare `--scenes` detects scene changes with the same analysis as
`cutmy analyze video` and its default threshold. Detection decodes every frame
of the range, so on long recordings it is much slower than the other
selections; raise `--timeout` or pass a `--range`. To choose the threshold, or
to reuse a finished analysis, save it and name the file:

```sh
cutmy analyze video --input interview.mp4 --scene-threshold 0.2 > analysis.json
cutmy sheet --input interview.mp4 --scenes analysis.json --output scenes.jpg
```

A supplied analysis applies to a single input and narrows the sheet to the
interval it measured, at most the 30 minutes one `cutmy analyze video` run
covers. Bare `--scenes` has no such bound: it analyses a longer range in
consecutive 30-minute windows.

## Which frame a cell shows

A cell shows the frame a player displays at the requested time: the frame with
the greatest presentation timestamp at or before it. With 25 frames per second,
`--at 0.039` shows the first frame and `--at 0.040` the second.

- Variable frame rates follow the same rule. During a two-second hold, every
  requested time shows the held frame.
- Time zero is the container's start time, so a recording whose timestamps
  begin at 1.4 s or at ten hours needs no adjustment.
- A time before the first video frame shows that first frame; a time after the
  last frame shows the last one.

The index reports both times. `time_ms` is what was requested. `frame_time_ms`
is the shown frame's own presentation time, rounded up to a whole millisecond:
requesting `frame_time_ms` again selects the same frame.

## Layout

| Flag | Default | Meaning |
| --- | --- | --- |
| `--cols` | 8 | Tiles per row, 1–64. A sheet with fewer frames than columns is narrower |
| `--tile-width` | 320 | Tile width in pixels, 48–1920. Tile height follows the first input's displayed proportions |
| `--max-rows` | 6 | Rows per page, 1–64 |
| `--label` | `time` | `time`, `index`, `name` in drawing order, comma-separated, or `none` |
| `--format` | from the suffix | `jpeg` or `png`; must match `.jpg`/`.jpeg` or `.png` |
| `--threads` | `auto` | Parallel decoders: the number of CPUs up to 16, or 1–32 |
| `--timeout` | `2m` | Deadline for the whole sheet, including inspection |

Captions are white text on a dark strip directly below each tile, so they stay
readable on any picture and hide none of it. `time` is the requested time,
`index` is `#N` and matches `index` in the JSON, `name` is the file name. A
caption wider than its tile shortens the name around its middle. The built-in
typeface covers printable ASCII; other characters appear as `?`. Its letters
are 7 pixels high on tiles narrower than 320 pixels, 14 pixels from 320 and
grow by another 7 with every further 160 pixels of tile width.

Frames are shown as a player shows them: display rotation and non-square
pixels are applied, and HDR (PQ or HLG) is converted to SDR. When the installed
FFmpeg cannot convert HDR, the command fails with `hdr_conversion_unavailable`
rather than showing wrong colors.

### Pages

When the frames do not fit `cols × max-rows`, the sheet continues on further
pages. Page files take the output name with a number before the suffix:
`--output sheet.jpg` becomes `sheet-001.jpg`, `sheet-002.jpg`, and so on. A
sheet that fits one page is exactly `sheet.jpg`.

Existing files are never replaced. If the output name or any page name is
taken, the command fails with `output_exists` before extracting frames and
leaves every file as it was.

### Several recordings

Repeat `--input` to put several recordings on one sheet. Each recording gets
the same selection, and its frames follow the previous recording's. Tiles keep
the first recording's proportions; a recording with other proportions is
contained in its tile between black bars, never stretched.

```sh
cutmy sheet --input camera-a.mp4 --input camera-b.mp4 --count 12 \
  --label name,time --output cameras.jpg
```

## Sheets from images

```sh
cutmy sheet --images stills/ cover.png --cols 6 --output stills.jpg
```

`--images` takes JPEG and PNG files and directories; a directory contributes
the images directly inside it. All images are sorted by path in natural order,
so `frame-2.png` precedes `frame-10.png`. Image sheets have no times: the
default caption is `name`, and a cell's `path` names its image. Embedded
orientation tags are not applied.

## Comparing two recordings

```sh
cutmy sheet --compare graded.mp4 original.mp4 --every 10s \
  --ssim-threshold 0.95 --output compare.jpg
```

Each requested time becomes a pair of neighbouring cells: the first recording
on the left, the second on the right. The selection covers the part both
recordings have. `--cols` must be even.

Every pair gets the mean SSIM of the two tiles' luma, from 1 for identical
pictures downwards. The value is appended to the right-hand caption, pairs
below `--ssim-threshold` (default 0.95) get a dark red caption strip, and the
index lists them in `below_threshold`. SSIM is measured on the scaled tiles; it
finds replaced, shifted or damaged material, not small encoding differences.
Recordings of different proportions are compared including their bars.

## The index

```json
{
  "version": 1,
  "mode": "frames",
  "format": "jpeg",
  "cols": 4,
  "tile_width": 320,
  "tile_height": 180,
  "label_height": 22,
  "label": ["index", "time"],
  "pages": [
    {"page": 1, "path": "/work/minute-2-001.jpg", "width": 1300, "height": 416, "size_bytes": 96412},
    {"page": 2, "path": "/work/minute-2-002.jpg", "width": 1300, "height": 210, "size_bytes": 38127}
  ],
  "cells": [
    {"index": 1, "page": 1, "col": 0, "row": 0, "x": 4, "y": 4, "width": 320, "height": 180,
     "input": 1, "path": "/work/interview.mp4", "time_ms": 60000, "frame_time_ms": 59994,
     "label": "#1 01:00.000"},
    {"index": 2, "page": 1, "col": 1, "row": 0, "x": 328, "y": 4, "width": 320, "height": 180,
     "input": 1, "path": "/work/interview.mp4", "time_ms": 65000, "frame_time_ms": 64999,
     "label": "#2 01:05.000"}
  ],
  "pairs": [],
  "below_threshold": []
}
```

The example is shortened to two of its twelve cells. It comes from a 29.97
frames-per-second recording and
`--range 60-120 --every 5s --cols 4 --max-rows 2 --label index,time`: at
01:00.000 the frame on screen began 6 ms earlier.

- `index`, `page` and `input` count from one. `index` is the number drawn by
  the `index` label, `page` matches the page file's suffix, and `input` is the
  position of the recording among the `--input` flags. `col` and `row` are
  zero-based grid positions.
- `x`, `y`, `width` and `height` locate the picture on its page in pixels.
  Every tile has the same size; the caption strip of `label_height` pixels lies
  directly below the tile and belongs to no cell rectangle.
- `label` is the caption exactly as drawn. Image sheets omit `time_ms` and
  `frame_time_ms`.
- `mode` is `frames`, `images` or `compare`. A comparison adds
  `ssim_threshold`, and fills `pairs` with
  `{"index", "time_ms", "ssim", "cells": [left, right]}` and `below_threshold`
  with the pairs under the threshold. Other sheets keep both arrays empty.
- All arrays are present even when empty. Consumers should ignore fields they
  do not know.

To go from a picture to a cut: find the cell by its drawn number or by its
position, read `time_ms`, and pass it to `cutmy clip --start`/`--end` or to
another `cutmy sheet --range` around it for a closer look.

## Limits and errors

| Limit | Value |
| --- | --- |
| Cells in one sheet | 2,048 |
| Pages | 256 |
| Pixels per page | 16,777,216, at most 65,535 per side |
| Encoded bytes per page | 64 MiB, or the engine's output limit if smaller |
| Recordings | 64; a comparison takes exactly two |

A request beyond a limit fails with `output_limit`; nothing is truncated. The
cell, page and pixel limits are checked before any frame is extracted, the
byte limit when a page is encoded. Other outcomes use the existing codes:

| Code | Cause |
| --- | --- |
| `invalid_argument` | Conflicting flags, more than one selection, a time or range outside the source, an input without a video stream |
| `input_not_found` | A missing recording, image or scene file |
| `unsupported_media` | An undecodable recording or image |
| `output_exists` | The output or a page name is taken |
| `source_changed` | A recording changed while its frames were read |
| `timeout`, `cancelled` | The deadline passed or the command was interrupted; no page is published |

A decoder that stalls is stopped with the deadline. Pages are published only
after the whole sheet is complete. If publishing fails after an earlier page
was published, the Go result lists the pages that exist.

## Speed

Frames are not found by decoding the recording from its start. Each frame is
reached by seeking to the keyframe before it, several frames are decoded in
parallel, and every picture is scaled to its tile inside FFmpeg, so the cost
depends on the number of frames, not on the recording's length. Frames that
lie close together are read in one pass instead of one seek each; the distance
that counts as close follows the recording's own keyframe spacing.

Measured on four cores with a one-hour 1080p H.264 recording (30 frames per
second, a keyframe every 8.3 s):

| Sheet | `cutmy sheet` | For comparison |
| --- | --- | --- |
| `--count 48` over the hour | 4.8–5.2 s | 103–107 s for one FFmpeg pass with `fps` or `select`, `scale` and `tile` |
| `--range 600-660 --every 1s`, 60 frames | 2.6 s | |

Recordings with very long distances between keyframes, and containers without
a seek index such as MPEG-TS, need more decoding per frame; the result is the
same frame.

## Go

```go
e := engine.New(engine.Config{})
result, err := e.GenerateSheet(ctx, "sheet.jpg", engine.SheetOptions{
    Inputs:  []string{"interview.mp4"},
    StartMS: 60_000,
    EndMS:   120_000,
    EveryMS: 5_000,
    Labels:  []string{"index", "time"},
})
```

`SheetOptions` mirrors the flags: `Inputs`, `Images`, `Compare`, `StartMS`,
`EndMS`, one of `EveryMS`, `Count`, `AtMS` and `Scenes` (with an optional
`SceneAnalysis`), `Columns`, `TileWidth`, `MaxRows`, `Labels`, `Format`,
`Threads` and `SSIMThreshold`. Zero values use the defaults above.
`SheetResult` is the index. The engine's `InspectTimeout` bounds inspection and
`ExportTimeout` the whole operation.
