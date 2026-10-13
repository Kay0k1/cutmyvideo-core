# Bounded preview observations — 2026-10-12

Each cohort contains five cold requests and five immediately following warm
requests for the same aligned 30-second interval. Cold requests use fresh cache
identities with identical media and tools. Timing spans the real HTTP handler
through a complete playable MP4, including metadata refresh, durable publication
and cache lookup. Browser first-frame decoding is measured separately by the web
acceptance run.

| Cohort | Cold median | Cold min–max | Warm median | Warm min–max |
| --- | ---: | ---: | ---: | ---: |
| Baseline `85550cd` | 5094.144 ms | 4657.323–6617.846 ms | 13.646 ms | 9.194–25.820 ms |
| FPS before resize/conversion | 3089.043 ms | 2978.201–3206.724 ms | 10.087 ms | 8.149–28.168 ms |

Cold median improves **39.36%** on this high frame-rate source. This is a local
observation, not a promise for all codecs, sizes, HDR tools or public platforms.
Frames are discarded before resize/HDR conversion; recordings already at 24 fps
or below have less discarded work. Output retains 24 fps, at most 854×480,
H.264 ultrafast/CRF 28, 1400 kbit/s maxrate/2800 kbit buffer, and 96 kbit/s AAC.
Source timestamps, stream selection, audio and export behavior remain the same.

The retained baseline/candidate MP4 are byte-identical and 5,763,690 bytes:
`a8a2cc4581e64b06ddbefc9375b35528dd5ec5aa951e9c090e723611766a03b2`.
Operator artifacts are under `/root/video_cut/preview-evidence-20261012`;
generated media are not committed. Source SHA-256:
`10e6bbbb94ac9bcfac48e4d66c7392426a9874fe18ca33e5546ccb87fe4f15fe`.

## Memory observations

Three alternating direct-encoder pairs measured the peak RSS of each FFmpeg
child in a fresh Python process with the same source, tools, thread bounds and
codec options. Baseline median is **73,056 KiB** (72,092–73,096); candidate median
is **75,000 KiB** (74,560–75,364). Moving FPS first retains source-size frames
briefly, costing **1,944 KiB / 2.66%** on this fixture. This small measured increase
is accepted in exchange for less conversion work; decoder/pixel/thread limits
and the full-pool weight for >8MP or unknown sources remain enforced. The result
does not establish peak memory for other codecs or 8K inputs.

Raw observations and exact arguments are in `memory.json`. A frontend build
overlapped part of this memory cohort, so its wall times are diagnostic only and
do not support speed claims. Other processes do not enter the per-child RSS.
Run `python3 memory-observations.py SOURCE OUTPUT baseline` and then `candidate`
in fresh processes to reproduce the measurement; the helper deletes its output
after recording size-independent RSS and the MP4 hash. Five of six memory-run
encodes matched the retained MP4 hash; one compressed stream differed under
multithreaded x264/VBV. Byte identity of the original controlled retained pair
does not imply universal encoder determinism.

## Environment and controls

- Linux 5.15.0-190-generic x86_64; Go 1.27.2.
- AMD Ryzen 9 5950X virtual host, 4 CPUs; no simultaneous media/CPU workload
  from other task agents during either cohort.
- Same FFmpeg/ffprobe `N-127083-g65a3870462-20261001` binaries.
- Two decoder threads, one filter thread, two encoder threads for both cohorts.
- Disposable PostgreSQL 17.11 on loopback port 55441, isolated schema per run.
- Identical 36-second 1920×1080 60 fps H.264 source with 880 Hz AAC; no network
  media, artificial network limits, cache-dropping or additional prefetch.
- The final revision also gives local inspection a durable conservative pool
  lease, and background work yields before opening a decoder. These admission
  guards were finalized after the encoder cohort; the final browser cohort
  includes them.

## Reproduction

Generate the common source once with the same FFmpeg binary:

```sh
ffmpeg -hide_banner -loglevel error -nostdin -y -filter_threads 1 \
  -f lavfi -i testsrc2=size=1920x1080:rate=60 \
  -f lavfi -i sine=frequency=880:sample_rate=48000 -t 36 \
  -c:v libx264 -preset ultrafast -crf 36 -threads 2 -g 120 \
  -c:a aac -b:a 96k /tmp/preview-source.mp4
```

Use an isolated PostgreSQL and test-only `TEST_DATABASE_URL`. Set
`CUTMY_PREVIEW_OBSERVATION_SOURCE=/tmp/preview-source.mp4` and
`CUTMY_PREVIEW_OBSERVATION_OUTPUT=/tmp/preview-baseline.json`, then run
`go test ./internal/app -run '^TestPreviewObservations$' -count=1 -v`.
For baseline, use a `85550cd` worktree with this revision's
`internal/app/preview_observations_test.go` copied into it unchanged. For candidate,
run the same test from the candidate worktree, changing output to
`/tmp/preview-candidate.json`. Each run writes JSON and an independently decodable
MP4 beside it. Stop unrelated media work in both cohorts; compare output hashes,
medians and min/max.

## Search strip cost and safety

The additive `/sources/{id}/storyboard?start_ms=…` endpoint prepares six 160×90
video samples only for the visible aligned interval, in one JPEG up to 256 KiB.
It prepares no audio and shares preview slots, disk reservations, cache expiry
and source deletion. Requests are cancellable; background admission rejects
before inspection when a foreground renderer is active. No whole-recording
thumbnail job or additional processing pool is introduced.

Tests independently decode colored fixtures to verify samples at 0/5/10/15
seconds, offsets at hour eight, near-end padding and 100 ms windows; verify owner
isolation, warm tool-free reuse, actual-byte charges, source deletion, canceled
subprocess cleanup, and persisted full-pool inspection weight across admissions.

Raw diagnostics were retired from the working tree on 2026-10-13. The [original files](https://github.com/Kay0k1/cutmyvideo-core/tree/faccc12e510fdb57c112f2df76a6bedb14b1d5fb/benchmarks/2026-10-12-responsive-preview) remain in Git history; future runs follow [artifact retention](../../docs/artifacts.md).
