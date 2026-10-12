# Media selection and rendering

This policy was **introduced in 1.0.2**. The published
v0.2.1 archives predate these selection, display-geometry and HDR diagnostics.
Local CLI, Go SDK and server exports share the media implementation.

## Selected streams

One output contains at most one playable video and one audio stream. Select the
lowest-index video that is not an attached picture. Select the lowest-index
audio marked default; when no audio is default, select the lowest-index audio.
Titles and language tags do not change the default policy. An explicit
`audio_track` chooses an inspected absolute audio index; `no_audio` omits audio.
Additional video/audio tracks, cover art and data streams are excluded.
Subtitles are omitted by default; local copy can preserve compatible plain text
subtitles with clipped/rebased timing. See [batch cutting](batch-cutting.md). MP3 uses the
selected audio only; a file containing audio and an attached cover remains an
audio-only source for inspection.

For an existing separate progressive video/audio pair, video comes from its
video input and audio from its audio input. Bounded combined HLS staging uses
the same explicit stream selection; separate HLS renditions remain unsupported.
Inspection, copy-cap checks, accurate/copy output and previews use the selected
playable stream rather than assuming container stream zero is the video.

HLS staging normally uses Matroska. A selected video with nonzero display
rotation uses MP4 staging to preserve its display matrix; that case requires
MP4-compatible selected codecs. Unsupported muxing or a loss of selected-stream
geometry or color metadata during staging returns `unsupported_stream`.

## Coded and displayed dimensions

Fresh SDK/CLI inspection keeps `width` and `height` as the selected video's
coded dimensions. The additive `display_width` and `display_height` describe
its displayed size after sample-aspect-ratio and right-angle display rotation.
When display geometry is unsupported, inspection still returns the coded
dimensions and omits both display fields. Audio-only inspection omits all four
dimensions. For example, a coded 240×120
video with 2:1 sample aspect ratio displays at 480×120; a 90° rotation displays
it at 120×480.

HTTP source dimensions remain persisted coded dimensions for uploaded/direct
files; platform sources use provider format metadata. Source metadata reads do
not launch a fresh media inspection. They do not expose the SDK/CLI display
fields, which are not database columns.

Owner-protected `GET /api/v1/sources/{id}/tracks` freshly inspects a local source
and also returns optional `display_width` and `display_height`. Use these fields,
rather than persisted coded dimensions, to select a displayed crop.

Window previews also return `X-Source-Display-Width` and
`X-Source-Display-Height`, describing the original upright square-pixel image
before the 480p preview cap. This supports crop presets for platform sources
whose provider dimensions may describe coded pixels. Fresh preview generation
stores the trusted dimensions in a bounded 20-byte standard MP4 `free` box.
The entire MP4, including this box, is durably published and charged together.
Owner-protected warm hits read those fixed bytes without media tools. Malformed
or absent metadata is ignored instead of inventing geometry. Video preview
cache identity advances independently of storyboard/waveform identities.

Before a worker processes a local source or prepares a new local window
preview, bounded inspection refreshes persisted selected coded dimensions.
This corrects historical snapshots that described a cover image and prevents
underestimating decoder capacity. Duration and retention metadata remain intact.
Serving an existing window-preview cache hit launches neither FFmpeg nor
FFprobe; source metadata reads also remain free of media-tool execution.

Accurate MP4 and previews normalize the selected video to square pixels and
apply its display orientation. Scaling preserves displayed aspect ratio;
H.264 4:2:0 dimensions are rounded down to even pixels. Export caps limit
displayed height; previews fit within 854×480. Normalizing anamorphic pixels can
change coded width without enlarging the displayed image. Copy leaves coded
dimensions, sample aspect ratio and rotation intact; an explicit resolution cap
checks displayed height and fails with `copy_quality_unsupported` when exceeded.

Right-angle rotations and finite positive sample aspect ratios must yield
bounded valid display dimensions. Accurate MP4, previews and resolution-capped
copy refuse unsupported geometry instead of silently stretching or changing
orientation. Inspection, MP3 and uncapped compatible copy can still use the
source; inspection does not invent display dimensions.

## Crop

Accurate MP4 exports optionally accept `crop: {x, y, width, height}`. All values
are normalized to the upright square-pixel displayed image, with both opposite
edges inside 0..1. An omitted crop keeps the full image. Copy, MKV and MP3 reject
crop explicitly; they never silently change mode. One crop applies to all ranges
in separate or joined exports.

The renderer first applies display rotation and normalizes sample aspect ratio,
then crops and applies the chosen resolution cap. Pixel origins and dimensions
round down to even values for H.264 4:2:0 (at most one displayed pixel per edge).
Areas resolving to less than two displayed pixels in either dimension fail.
There is no aspect stretching or padding. The browser selection overlay describes
the same normalized region over the uncropped preview. HDR conversion and VFR
timing retain the existing accurate-export policy.

Go callers use `AdvancedOptions.Crop` with `engine.Crop`; the legacy three-field
`Options` remains compatible. JSON plans use `options.crop`. CLI example:

```sh
cutmy clip --input input.mp4 --output portrait.mp4 --end 30 --crop 0.25,0,0.5,1
```

## Timing

Accurate MP4 keeps the source frame cadence, including variable frame rate;
it does not request a constant export frame rate. The requested interval is
still trimmed and rebased to the output timeline. Copy retains selected-stream
packet timing and its existing preceding-keyframe behavior; inspect the reported
`actual_start_ms` and `actual_end_ms`.

Window previews use a bounded 24 fps browser rendition, rather than exposing the
original frame cadence. Their source window and timestamp headers still refer
to the original recording timeline. Audio/video delay and staged HLS offsets
retain the existing timeline contract.

Accurate MP4 uses AAC at 128 kbit/s for selected mono audio and 192 kbit/s
otherwise; previews use 96 kbit/s. MP3 remains 192 kbit/s. Compatible copy keeps
the selected stream's encoded audio. The mono AAC setting avoids a reproduced
encoder convergence failure on valid silent/pulsed input.

## HDR and SDR

Accurate MP4 and previews convert recognized PQ (`smpte2084`) or HLG
(`arib-std-b67`) video to 8-bit H.264 SDR with BT.709 primaries/transfer/matrix
and limited (`tv`) range. Conversion requires supported declared primaries and
matrix; missing or unsupported color information is refused instead of guessed.
SDR input retains the normal encoding path. MP3 does not perform video-color
conversion.

The HDR conversion linearizes with `zscale`, uses a 100-nit SDR reference and
Mobius tone mapping (`param=0.3`, highlight desaturation `2`) with BT.709
luminance coefficients, then encodes BT.709 SDR. The tone-mapping peak is fixed:
PQ uses 10,000 nits (`peak=100` relative to the 100-nit reference), and HLG uses
a 1,000-nit reference (`peak=10`). This does not adapt to scene peaks, mastering
display metadata or dynamic HDR metadata. HLG uses that fixed reference rather
than display-dependent adaptation. This produces an SDR viewing rendition;
the filter definitions are in the
official [zscale](https://ffmpeg.org/ffmpeg-filters.html#zscale) and
[tonemap](https://ffmpeg.org/ffmpeg-filters.html#tonemap) documentation.

Compatible MP4 copy preserves the selected HDR stream and its color metadata;
it does not tone-map. Copy still needs compatible codecs and output-size,
duration and geometry limits. A player must support the copied HDR media.

Current-source window-preview keys include a rendition-contract version. Old
cached renditions are not reused after this policy changes; their files remain
charged until normal expiry or eviction releases them.

Known missing HDR-conversion capabilities in the configured tools return the
fixed `hdr_conversion_unavailable` diagnostic:

> HDR conversion to SDR is unavailable with the configured media tools

Repeating the same accurate export does not add those capabilities. Configure
FFmpeg with `zscale`/libzimg and `tonemap`, or choose compatible copy/audio output
when appropriate. Cancellation, storage failures and unrelated processing
errors keep their own diagnostics. Unsupported color information uses the
existing `unsupported_media` SDK/CLI or `unsupported_stream` service diagnostic.

The [matched before/after verification](../verification/2026-10-10-media-contract/README.md)
includes independently inspected input signals, decoded frames/audio and exact
timestamp checks, with original reports and source/fixture hashes. Native OS,
Bookworm, service and recovery acceptance are separate CI gates.
