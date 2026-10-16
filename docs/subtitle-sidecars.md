# Supplied subtitles for clip collections

Use an existing timestamped SRT or WebVTT transcript from any tool. The core
clips and rebases cues against the **actual** source/output segment maps returned
with verified exports. It does not transcribe speech, choose moments, style
captions or burn text into video. The operation requires no media decoding.

The same request works through `engine.RetimeSubtitles(ctx, request)`,
`cutmy subtitles retime --request request.json`, or owner-authenticated
`POST /api/v1/subtitles/retime`:

```json
{
  "version": 1,
  "format": "srt",
  "text": "1\n00:00:10,000 --> 00:00:13,000\nA supplied sentence\n",
  "outputs": [{
    "clip_id": "answer",
    "segments": [{
      "index": 0,
      "label": "Answer",
      "source_start_ms": 11000,
      "source_end_ms": 13000,
      "output_start_ms": 0,
      "output_end_ms": 2000
    }]
  }]
}
```

The response is `{version:1, outputs:[{clip_id, format, text, cue_count, sha256}]}`.
Here `answer` contains `00:00:00,000 --> 00:00:02,000`. Save its UTF-8 text as
`answer.srt` with an exclusive write; SHA256 covers exactly those text bytes,
including newlines. Sidecars are separate from the durable media receipts and
do not modify a collection's saved state. Do not overwrite an existing file.
Python and TypeScript expose `retime_subtitles` and `retimeSubtitles`; local
wrappers invoke the same CLI operation.

Set `output_format` to `srt` or `vtt` to convert plain cues. Otherwise it matches
the input format. Empty SRT output is an empty string; empty VTT output retains
its `WEBVTT` header. Every requested clip is returned, including empty sidecars.

## Timing semantics

Source intervals are half-open. A cue crossing a cut is clipped; an interval
repeated in a clip repeats its intersecting cues. Reordered intervals follow
the output map, and gaps in the output clock remain gaps. Overlapping cues
remain separate cues. Output order is deterministic and input cue order is
stable among equal start times. Cue numbers are regenerated.

Use actual maps from the corresponding rendition/revision, including copy-mode
preroll. Requested edit ranges alone do not prove actual packet boundaries.
Times are translated from the source clock and clamped to the actual output
interval; they are never stretched. This is not an adapter for speed ramps,
arbitrary transitions or an unrelated transcript clock.

## Supported text and limits

Accepts UTF-8, an optional initial BOM, LF/CRLF/CR newlines, multiline text,
three-digit milliseconds and ordinary overlapping/unsorted cues. SRT uses
`HH:MM:SS,mmm` (a decimal point is also accepted); WebVTT also accepts `MM:SS.mmm`.
Plain VTT identifiers and NOTE comments are accepted; identifiers are regenerated
and comments are omitted. Standard VTT cue settings are retained in VTT output.

STYLE/REGION blocks, region references, external timestamp maps and inline
karaoke timestamps return `subtitle_unsupported`. Converting VTT cue positioning
or VTT-specific voice/class/language/ruby markup to SRT is explicitly refused
instead of silently discarding it. Preserve VTT output for those supported tags.
This bounded caption subset does not implement the complete
[WebVTT specification](https://www.w3.org/TR/webvtt1/).

Limits apply before expansion: 512 KiB supplied UTF-8 text, 10,000 input cues,
1–128 clips, 1–128 segments per clip and 4,096 segments in total. IDs follow the
collection contract. Output intervals must be ordered and non-overlapping;
source intervals may repeat or move backwards. Clocks are nonnegative and
bounded to 720 hours. Aggregate sidecar text is bounded to 8 MiB, 100,000 cues
and one million candidate intersections. Cancellation stops transformation.

HTTP and CLI requests are capped at 4 MiB of JSON, independently of the text and
map limits. SDK subtitle responses have a separate 64 MiB JSON ceiling because
escaping can expand text; ordinary metadata response limits remain unchanged.
`invalid_subtitles`, `invalid_segment_map`, `subtitle_unsupported` and
`subtitle_limit` identify safe failures without reflecting transcript text.
The pure operation publishes no files and queues no jobs.
