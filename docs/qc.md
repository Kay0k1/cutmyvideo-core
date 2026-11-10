# Finished-file checks: `qc`

`cutmy qc` measures a finished media file and answers in one JSON report: how
many frames it really contains and how regularly they are timed, whether the
picture goes black or freezes, how loud the sound is and whether it clips,
where audio and video start and end, and how the container is laid out. With
`--expect` it also checks the file against stated requirements and exits with
a nonzero code when one is not met.

It is meant for the step after a render, when nobody watches the result: a
splice that lost frames still reports success, variable frame timing breaks a
later edit, and a mix whose peaks exceed full scale distorts after encoding.
`qc` never writes media. FFmpeg and ffprobe are required, a database is not.

## Check a render

```sh
cutmy qc --input final.mp4 \
  --expect fps=30,size=1080x1920,duration=12,true-peak=-1,cfr,no-drops,faststart
```

Abridged report for a file that lost six frames in the middle and was written
without fast start:

```json
{"version":1,"path":"/work/final.mp4","start_ms":0,"end_ms":12000,
 "container":{"format":"mov,mp4,m4a,3gp,3g2,mj2","duration_ms":12000,"size_bytes":23234322,
   "bit_rate":15489548,"start_ms":0,"faststart":false,
   "streams":[{"index":0,"type":"video","codec":"h264","start_ms":0,"duration_ms":12000},
              {"index":1,"type":"audio","codec":"aac","start_ms":0,"duration_ms":12000}]},
 "video":{"track":0,"codec":"h264","width":1080,"height":1920,"coded_width":1080,"coded_height":1920,
   "rotation":0,"sample_aspect_ratio":"1:1","pix_fmt":"yuv420p",
   "color":{"range":"","primaries":"","transfer":"","matrix":""},
   "r_frame_rate":"30/1","avg_frame_rate":"59/2",
   "frame_count":354,"expected_frames":360,
   "first_frame_ms":0,"last_frame_ms":11967,"end_ms":12000,"duration_ms":12000,
   "timing":"vfr","fps":30,"interval_ms":{"min":33.333,"median":33.333,"max":233.333},
   "gap_count":1,"missing_frames":6,"gaps":[{"at_ms":6667,"missing_frames":6}],
   "duplicate_count":0,"duplicates":[],
   "analyzed_end_ms":11967,"black":[],"freeze":[]},
 "audio":{"track":1,"codec":"aac","sample_rate":48000,"channels":1,
   "start_ms":0,"end_ms":11979,"duration_ms":11979,
   "integrated_lufs":-6.26,"true_peak_dbtp":-2.42,"loudness_range_lu":0,
   "peak_dbfs":-2.42,"clipped_samples":0,"leading_silence_ms":95,"trailing_silence_ms":0},
 "sync":{"start_offset_ms":0,"end_offset_ms":-21,"duration_delta_ms":-21},
 "checks":[
   {"name":"fps","status":"fail","expected":"30±0.01","actual":"30","message":"frame rate is not constant: frame intervals 33.333..233.333 ms, median 33.333 ms; 1 gaps, 0 duplicate timestamps"},
   {"name":"size","status":"pass","expected":"1080x1920","actual":"1080x1920","message":""},
   {"name":"duration","status":"pass","expected":"12±0.05","actual":"12","message":""},
   {"name":"true-peak","status":"pass","expected":"at most -1","actual":"-2.42","message":""},
   {"name":"cfr","status":"fail","expected":"cfr","actual":"vfr","message":"frame intervals 33.333..233.333 ms, median 33.333 ms; 1 gaps, 0 duplicate timestamps"},
   {"name":"no-drops","status":"fail","expected":"0","actual":"6","message":"1 gaps, 6 missing frames; first at 6667 ms"},
   {"name":"faststart","status":"fail","expected":"true","actual":"false","message":"moov is stored after the media data"}],
 "passed":false}
```

The report is printed to stdout even when checks fail; the command then exits
1 and, with `--json-errors`, writes `{"error":{"code":"expectation_failed",…}}`
to stderr. Without `--expect` the command only measures and exits 0.

## What is measured

Times are milliseconds on the file's timeline, the clock a player uses. `null`
means the value is unknown or the stream is absent: `video` is `null` for an
audio file, `audio` for a silent video, `sync` unless both exist.

**container** — format name, duration, size, bit rate, start time and the
declared start and duration of every stream. `faststart` is `true` when the
MP4/MOV `moov` box precedes the first `mdat` box, so playback over a network
can begin before the whole file arrives; only top-level box headers are read.
It is `null` for other containers.

**video** — the first video stream that is not cover art. `width` and `height`
are display dimensions after sample aspect ratio and rotation; `coded_*` are
the stored ones. `r_frame_rate` and `avg_frame_rate` are what the file declares.

Frame timing comes from the presentation timestamps of every video packet in
the window, read without decoding:

| Field | Meaning |
| --- | --- |
| `frame_count` | Frames actually present |
| `expected_frames` | Video duration × `r_frame_rate`; differs from `frame_count` when frames are missing |
| `first_frame_ms`, `last_frame_ms` | First and last frame timestamps |
| `end_ms`, `duration_ms` | End of the last frame (its timestamp plus its duration) and the span from the first frame |
| `interval_ms` | Shortest, median and longest interval between consecutive frames |
| `fps` | Frames per second from the regular intervals; exactly the declared rate when every timestamp lies on its grid |
| `timing` | `cfr` when every interval equals the median within one time-base tick or 0.5 %, otherwise `vfr`; `unknown` below two frames |
| `gaps`, `gap_count`, `missing_frames` | Intervals longer than 1.5 regular intervals: where the first missing frame would be and how many would fit |
| `duplicates`, `duplicate_count` | Timestamps that occur twice |

Gaps are meaningful for material that is meant to be constant rate. A phone
recording with genuinely variable timing is reported as `vfr` and its longer
intervals are listed as gaps, measured against the median.

The window is then decoded once. `black` lists intervals where at least 98 % of
pixels are darker than 10 % luma (FFmpeg `blackdetect`). A single black frame
counts, except a lone final one whose length is unknown; `--min-black-ms` asks
for longer intervals only.
`freeze` lists intervals of at least `--min-freeze-ms` (500 ms) where the
picture does not change (FFmpeg `freezedetect`, noise tolerance -60 dB). A
black interval is not repeated as a frozen one; a large gap holds the previous
picture and appears as frozen too. `analyzed_end_ms` is the last decoded frame.

**audio** — the default audio stream, decoded as 32-bit float at its own
sample rate and channel count. `integrated_lufs`, `true_peak_dbtp` and
`loudness_range_lu` are EBU R128 values, as in `cutmy analyze loudness`; they
are `null` for silence and for more than eight channels. `peak_dbfs` is the
largest sample. `clipped_samples` counts samples whose magnitude exceeds 1.0:
a float mix with such samples clips when it is encoded to a fixed-point or
lossy format. `leading_silence_ms` and `trailing_silence_ms` measure the time
before the first and after the last sample above -60 dBFS in any channel.
`start_ms` is where the stream begins; `duration_ms` is decoded audio.

**sync** — positive values mean the audio is later or longer than the video:
`start_offset_ms` = audio start − first frame, `end_offset_ms` = audio end −
video end, `duration_delta_ms` = audio duration − video duration. AAC and other
codecs with encoder delay commonly end some 20 ms early.

## Expectations

`--expect` takes comma-separated items and may be repeated. Each key may appear
once. An unknown key or malformed value fails with `invalid_argument` (exit 2)
before the file is read. `±` may be written as `+-`.

| Key | Passes when |
| --- | --- |
| `fps=N` or `fps=N±T` | Timing is `cfr` and `fps` is within T of N (default 0.01) |
| `size=WxH` | Display dimensions are exactly W×H |
| `duration=S` or `duration=S±T` | Container duration is within T seconds of S (default 0.05) |
| `true-peak=X` | True peak is at most X dBTP |
| `lufs=X` or `lufs=X±T` | Integrated loudness is within T LU of X (default 1) |
| `cfr` | Timing is `cfr` |
| `no-drops` | No gaps |
| `no-black` | No black intervals |
| `no-freeze` | No frozen intervals |
| `no-clipping` | `clipped_samples` is 0 |
| `faststart` | The MP4/MOV file is fast-start |
| `audio`, `no-audio`, `video` | An audio stream is present, absent; a video stream is present |

A check without the data it needs fails and says why: `fps` on a file without
video, `lufs` on silence, `faststart` on Matroska. `true-peak` passes on audio
that is entirely digital silence.

Each check reports `name`, `status` (`pass` or `fail`), `expected`, `actual`
and a `message` for failures; `passed` is true when every check passed.

## Exit codes

| Code | Meaning |
| --- | --- |
| 0 | Measured; every expectation passed or none were given |
| 1 | `expectation_failed` after the report was printed, or another failure (unreadable media, changed file) |
| 2 | Invalid arguments or expectations |
| 124, 130 | Timeout, cancellation |

## Limits

One request measures at most 30 minutes; choose `--start` and `--end` for
longer files. The window also bounds 180000 video frames, 256 million decoded
audio samples and 4096 black or frozen intervals; the packet listing, read from
the start of the file to the window end, is limited to 64 MiB. Beyond these the
command fails with `output_limit`. Gap and duplicate lists
name the first 256 entries while their counts stay complete. `--timeout`
(default 2 minutes) covers inspection and every decode; an FFmpeg or ffprobe
process still running at the deadline is stopped. The file must not change
while it is measured (`source_changed`).

A 60-second 1080p 60 fps H.264 file with AAC audio takes about ten seconds on
four shared cores; decoding the picture dominates. Use `--threads` for more
decoder threads.

## Go

```go
report, err := e.QC(ctx, "final.mp4", engine.QCOptions{Expect: []string{"fps=30", "cfr", "no-drops"}})
if engine.CodeOf(err) == engine.CodeExpectationFailed {
	// report is complete; report.Checks says which expectations failed.
}
```
