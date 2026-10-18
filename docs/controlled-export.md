# Controlled export (1.6.0)

Four opt-in operations are exposed through Go, CLI, HTTP and the Python and
TypeScript SDKs. Existing requests retain their formats and defaults. Browser
controls belong to the separate web application.

## Audio delivery

```sh
cutmy clip --input interview.mp4 --start 5 --end 35 --output voice.wav
cutmy clip --input interview.mp4 --start 5 --end 35 --output voice.flac
cutmy clip --input interview.mp4 --start 5 --end 35 --mode copy --output voice.m4a
```

WAV uses signed 24-bit PCM (RF64 automatically when needed) and FLAC stores 24-bit samples. Both preserve the
inspected sample rate (8,000–192,000 Hz) and channel count (1–8). They require
accurate mode and support explicit audio selection, replacement and loudness
normalization. They do not contain video. Converting lossy audio to WAV/FLAC
does not recover information already lost by its original encoder.

Lossless joined output decodes intervals sequentially into a private PCM24 WAV
workspace before final encoding. Samples are rounded from the selected duration
and inspected rate. Aggregate temporary PCM is bounded by the smaller of the
operator output ceiling and a conservative 192 kHz/eight-channel/24-bit estimate
plus 16 MiB of header headroom. Separate batches reserve the largest interval
workspace, not their sum. This bound can reject an operation even if a compressed
FLAC might fit; it does not permit unbounded temporary audio. Cancellation and
errors remove owned pieces. Published bytes retain their independent limits.

M4A is an explicit compatible AAC stream-copy operation. Unsupported source
codecs return `copy_audio_unsupported`; the service does not silently transcode
them. Copy boundaries follow packets and codec priming, not an audio-sample
accuracy promise. Inspect actual bounds and the track manifest. Rendering
effects, crop, canvas, speed and subtitle preservation cannot accompany this
copy operation. Existing MP3 behavior remains unchanged.

## Constant speed

```sh
cutmy clip --input interview.mp4 --start 5 --end 35 --speed 1.25 --output brisk.mp4
```

JSON plans and HTTP use `"speed":{"numerator":5,"denominator":4}`. Each term
is an integer from 1 through 1,000 and the ratio must be 0.5 through 2. The
fraction is reduced; unity normalizes to the existing default. CLI decimals are
converted exactly, rather than through binary floating-point approximation;
an unrepresentable fraction is rejected.

Speed requires accurate final MP4 or MP3. It applies uniformly to the selected
sequence, including submitted order, overlaps and repeats, while preserving
audio pitch. No speed ramps or per-range rates are implied. Replacement audio
offsets are aligned on the original source clock before playback speed changes;
loudness normalization measures the completed changed audio timeline.

Source ranges and progress continue to use source milliseconds. Actual segment
maps include both source and output endpoints; output boundaries are calculated
from cumulative source positions to avoid per-segment rounding drift. Use those
maps for output chapters. For supplied subtitle sidecars on changed-speed maps,
submit retiming request `version: 2`, which scales cues to each actual output span.
Request version 1 keeps its historical clipping behavior; both versions return
the unchanged version-1 result shape. Never infer output duration by simply summing source ranges when speed is active.

## A per-file byte ceiling

```sh
cutmy clip --input interview.mp4 --start 5 --end 35 \
  --speed 1.25 --target-size-bytes 8000000 --output ready.mp4
```

`target_size_bytes` applies to each finished media file, including audio and
container overhead. Positive values require final accurate H.264 MP4 and must
not exceed the operator's output limit. Separate outputs each get the selected
ceiling; aggregate job, owner, storage and temporary-work limits still apply.
Zero or omission disables the request-level target.

The bitrate calculation uses the changed output duration and reserves audio and
muxing headroom. Two-pass work is bounded to 1,000,000 video frames and an 8 MiB
statistics budget, with 16 MiB of reserved statistics-workspace headroom and
supervised cleanup. Both passes share the operation deadline and cancellation. Monitoring with headroom
is not an instantaneous filesystem quota. Successful output bytes are verified
against the exact ceiling before publication.

An impossible target returns `target_size_unattainable`. The encoder does not
silently resize the picture, lower its frame rate, remove sound or retry without
a bound. A preflight success is advisory and does not guarantee that a requested
size can be achieved for arbitrary content.

## One poster from the finished result

```sh
cutmy poster --input ready.mp4 --at 1.250 --format jpeg --output poster.jpg
cutmy poster --input ready.mp4 --at 1.250 --format png --output poster.png
```

The required timestamp is relative to the finished video, after edits, speed,
crop and canvas. It must be nonnegative and strictly before the measured video
duration. The frame is selected at the requested presentation position within
the video's frame cadence. This differs from the existing source thumbnail and
does not automatically choose a cover or generate a contact sheet. If there is
no decodable frame at or after a near-end seek, the operation returns
`invalid_poster`; choose a slightly earlier timestamp. It never substitutes an
arbitrary earlier frame.

Go exposes `Engine.GeneratePoster(ctx,input,output,PosterOptions)` with
`TimestampMS` and `Format`. JPEG is the default; the output extension must match
`.jpg`/`.jpeg` or `.png`. Publication preserves existing files and symlinks.
Decoded images are limited to 16,777,216 pixels and encoded files to the smaller
of 16 MiB and the operator output limit, within a 30-second operation budget.

HTTP creates a separate image artifact:

```json
POST /api/v1/artifacts/{finished_artifact_id}/poster
{"timestamp_ms":1250,"format":"png"}
```

The parent must be a retained verified completed artifact in a terminal job;
successful partial results of failed/cancelled jobs also qualify. The owner's
session is required. Project grants and handoff tokens do not grant derivative
creation. A new image returns 201; the same parent/timestamp/format returns its
existing image with 200. Concurrent preparation can return `409 poster_pending`;
clients do not automatically retry mutations.

`ImageArtifact` contains `id`, `parent_artifact_id`, `filename`, `size_bytes`,
`download_url`, `width`, `height`, `timestamp_ms`, `format` and `expires_at`.
Use `GET /api/v1/image-artifacts/{id}` for metadata, `GET .../{id}/download`
for protected bytes and `DELETE .../{id}` for explicit deletion (204). Foreign,
absent and expired IDs return 404. Images have their own registered byte charges
and inherit a lifetime no longer than their parent; see [upgrade and lifecycle](poster-upgrade.md).
Aborting the synchronous request cancels its work; no new asynchronous job or
poster-specific cancel endpoint is introduced.

## Plans, collections and SDKs

`AdvancedOptions`, plan options and collection options carry `speed` and
`target_size_bytes`. Collection omission/null speed and zero/omitted byte target
inherit; `clear_speed:true` and `clear_target_size:true` explicitly remove an
inherited setting. Setting and clearing the same option together is invalid.
Portable documents retain these controls and reset flags; they still carry no
owner credentials, server media IDs or replacement filesystem paths.

```python
from cutmyvideo import LocalCutmy

core = LocalCutmy(binary="cutmy")
plan = {"version": 1, "input": "interview.mp4",
        "ranges": [{"start_ms": 1000, "end_ms": 5000}],
        "options": {"format": "mp4", "quality": "best", "cut_mode": "accurate",
                    "speed": {"numerator": 5, "denominator": 4},
                    "target_size_bytes": 1000000}, "output_mode": "joined"}
core.execute_plan(plan, "ready.mp4")
core.generate_poster("ready.mp4", "poster.png", {"timestamp_ms": 1000, "format": "png"})
# HTTP: client.create_poster(parent_id, options), get_image_artifact(image_id),
# client.download_image(image_id, "poster.png"), delete_image_artifact(image_id).
```

```ts
import { LocalCutmy, downloadImageArtifactToFile } from '@cutmyvideo/sdk/local';
const core = new LocalCutmy({binary: 'cutmy'});
await core.generatePoster('ready.mp4', 'poster.png', {timestamp_ms: 1000, format: 'png'});
// HTTP: client.createPoster(parentId, options), getImageArtifact(imageId),
// downloadImageArtifactToFile(client, imageId, 'poster.png'), deleteImageArtifact(imageId).
```

SDK image downloads use the authenticated image ID route, never an arbitrary
returned URL, and preserve existing destination files. Numeric/shape validation
rejects booleans, unsafe/nonfinite byte counts and malformed speed fractions
before they can become null defaults during JSON copying.
