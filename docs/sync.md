# Audio alignment: `sync` and `analyze repeats`

`cutmy sync` finds where a reference recording, usually the finished track, is
heard in other recordings: a music video, a live take, a remix, a camera file.
It answers with an offset on the original timelines, never with a media file:

```text
input_ms = ref_ms + offset_ms
```

`cutmy analyze repeats` asks the same question about one recording against
itself and lists material that returns later, such as a chorus.

Both are local operations: FFmpeg and ffprobe are required, a database is not.

## Place a track in a clip

```sh
cutmy sync --ref track.wav --input clip.mp4
```

```json
{"version":1,
 "reference":{"path":"/work/track.wav","audio_track":0,"start_ms":0,"end_ms":144347,"analyzed_end_ms":144347},
 "inputs":[{"path":"/work/clip.mp4","audio_track":1,"start_ms":0,"end_ms":150000,"analyzed_end_ms":150000,
   "status":"match",
   "placements":[{"offset_ms":3380.021,"speed":1,"confidence":1,"similarity":0.986,
     "band":"full","method":"waveform",
     "ref_start_ms":0,"ref_end_ms":143729,"input_start_ms":3380,"input_end_ms":147109,
     "chunks":28,"agreeing":28}]}]}
```

The moment heard at 10 000 ms of the track is at 13 380.021 ms of the clip. To
put the track under the clip, pass the rounded offset to the existing
replacement option:

```sh
cutmy clip --input clip.mp4 --start 0 --end 30 --output cut.mp4 \
  --audio-replace track.wav --audio-offset-ms 3380
```

Repeat `--input` for up to 16 recordings; each gets its own entry. Offsets are
measured on presentation timestamps, the clock a player uses to keep picture
and sound together. A stream that starts a few milliseconds before or after
zero, as Opus and MP3 commonly do, is placed where it is actually heard.

## Reading a placement

| Field | Meaning |
| --- | --- |
| `offset_ms` | Input time minus reference time, to a thousandth of a millisecond |
| `speed` | How much faster the reference plays in the input; `1` unless a drift is measured |
| `method` | `waveform`: both recordings come from one master and the offset is accurate to a sample. `envelope`: only the attacks agree, as in a remix or a heavily processed copy; expect a few milliseconds |
| `band` | Frequency bands that voted: `full`, `vocal` or `drums` |
| `similarity` | 0 to 1, how alike the agreeing material sounds |
| `confidence` | 0 to 1; placements below 0.6 are not reported |
| `ref_*`, `input_*` | The range where the recordings agree, resolved to a few seconds |
| `chunks`, `agreeing` | Independent pieces examined and the longest run that agrees |

When `speed` is not `1`, the mapping is linear between the range ends and
`offset_ms` holds at the middle of the range.

## Status and exit code

Each input has one of three statuses.

- `match`: the placement can be used. An input that was edited from the
  reference, for example with a pause inserted, has one placement per part,
  each over its own reference range.
- `ambiguous`: the same part of the reference fits several offsets about
  equally well, as a hook that is heard twice. The placements are listed
  strongest first. Pass `--all` to accept them as a match and to search more
  thoroughly for further occurrences.
- `no_match`: nothing trustworthy was found.

A wrong offset is worse than a refusal. The JSON report is always printed; when
any input is `ambiguous` or `no_match` the command exits with code 1, and with
`--json-errors` writes `{"error":{"code":"no_match",...}}` to standard error.
Unrelated music at the same tempo agrees at every bar shift and is reported as
`ambiguous` or `no_match`, never as one confident placement.

## Remixes, windows and long recordings

`--band auto` tries the full spectrum and the vocal bands and keeps the better
placement. A remix that keeps the vocal over another instrumental usually
aligns on `vocal`. `--band drums` is never chosen automatically, because
different songs at one tempo share a grid; request it for a live take played
over the same beat.

Every recording is analysed within a 30 minute window. For longer material
select windows; offsets stay on the original timelines:

```sh
cutmy sync --ref track.wav --input concert.mkv --start 01:10:00 --end 01:25:00
cutmy sync --ref track.wav --ref-start 60 --ref-end 90 --input clip.mp4 --all
```

Stereo is mixed to mono before analysis. The default audio stream is used.

## Repeats inside one recording

```sh
cutmy analyze repeats --input track.wav
```

```json
{"version":1,"path":"/work/track.wav","audio_track":0,"start_ms":0,"end_ms":96478,"analyzed_end_ms":96478,
 "repeats":[{"delta_ms":51428.557,"start_ms":18020,"end_ms":36037,
   "confidence":1,"similarity":0.419,"method":"waveform"}]}
```

The material at 18.0–36.0 s is heard again 51 428.557 ms later. Up to eight
repeats are listed, the most alike first; an empty list is a valid answer.
`--min-gap-ms` (default 2000) ignores closer repeats. Sections that share only
the beat under different words are not the same audio and are normally not
listed; `--band drums` or an isolated stem can expose them.

## Limits

- At least one second of decoded audio per recording, three seconds of shared
  material for a placement, 30 minutes per window, 16 inputs.
- A measured `speed` between 0.98 and 1.02; other tempo changes are not found.
- The result depends on audible attacks. Sustained pads, silence and noise have
  nothing to align.

## Go

```go
result, err := e.SyncAudio(ctx, "track.wav", []string{"clip.mp4"}, engine.SyncOptions{})
if engine.CodeOf(err) == engine.CodeNoMatch {
    // result is complete; inspect result.Inputs[i].Status
}
repeats, err := e.AnalyzeRepeats(ctx, "track.wav", engine.RepeatsOptions{})
```
