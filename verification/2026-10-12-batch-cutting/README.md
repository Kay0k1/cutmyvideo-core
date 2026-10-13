# Batch cutting verification — 2026-10-12

This directory contains JSON reports and this explanation. Generated sources and
exported media remain outside the repository under `/root/.cache/cutmy-stage`.

## Independent media acceptance

[`media-acceptance.json`](https://github.com/Kay0k1/cutmyvideo-core/blob/faccc12e510fdb57c112f2df76a6bedb14b1d5fb/verification/2026-10-12-batch-cutting/media-acceptance.json) records 25 successful CLI plan
cases: ordinary video, variable frame rate, an actually rotated 90° source,
a source with two language-tagged audio streams and text subtitles, and a source
whose selected audio is a chirp that reveals timing displacement.

The same reversed intervals are exported separately and joined, with accurate
MP4, copied MP4, copied MKV, and silent video. The independent checker decodes
completed outputs with FFmpeg's error escalation enabled, probes their streams
and packet timestamps, checks blue/red interval order, and checks the chosen
audio independently. It asserts monotonic DTS and retained copied packet payload
SHA-256 hashes for both selected video and audio. Accurate rotated outputs have
240 × 320 display pixels; copied outputs retain their 320 × 240 coded pixels and
90° display rotation metadata in both MP4 and MKV.

Preserved subtitles are decoded back to SRT independently. Each cue's start and
end must match the intersection with the reported actual source bounds and its
new output position within 2 ms. Unselected interval text must be absent.

The chirp comparison decodes audio on its presentation timeline, including
packet gaps, and compares PCM 300 ms into each selected interval. Observed lags
were 0 ms for separate accurate output, 0 and 2.667 ms for accurate joins, 0 ms
for copied MP4, and −0.333/+0.333 ms for copied MKV. Correlations exceeded 0.99999
for accurate output and were 1.0 for copied output. Packet boundaries and MKV's
time base still limit timestamp precision; copy does not promise exact requested
cut points.

Reproduce against the built candidate binary, using a new workspace:

```sh
python3 scripts/stage-media-acceptance.py \
  --binary /root/video_cut/core/bin/cutmy \
  --workspace /root/.cache/cutmy-stage/acceptance-new-run \
  --report /root/.cache/cutmy-stage/media-new-run.json
```

The report records tool versions, fixture digests, binary version and binary
SHA-256. The final media snapshot is `0fe726e`, with binary SHA-256
`e46053cf4113aad38b6e513fa273cc7cc14b1ed9443ae9b9c3afb3a4c6ac8fba`.
It establishes behavior on these generated fixtures, rather than universal
support for arbitrary containers, subtitle formats or codecs.

## Remote join restrictions

[`remote-join-security.json`](https://github.com/Kay0k1/cutmyvideo-core/blob/faccc12e510fdb57c112f2df76a6bedb14b1d5fb/verification/2026-10-12-batch-cutting/remote-join-security.json) records a controlled
upstream that serves valid MP4 during inspection and a nested concat list during
processing. The pre-fix negative control fetched a private HTTP endpoint. With
per-entry media format and protocol restrictions, all three malicious variants
were rejected and the private endpoint received zero requests; an ordinary
remote MP4 join still decoded successfully. The fixed test also passed with the
race detector. These checks preserve the existing network boundary when an
upstream changes after inspection.

## Initial performance observations

[`initial-baseline-unpaired.json`](https://github.com/Kay0k1/cutmyvideo-core/blob/faccc12e510fdb57c112f2df76a6bedb14b1d5fb/verification/2026-10-12-batch-cutting/initial-baseline-unpaired.json) and
[`initial-candidate-under-load.json`](https://github.com/Kay0k1/cutmyvideo-core/blob/faccc12e510fdb57c112f2df76a6bedb14b1d5fb/verification/2026-10-12-batch-cutting/initial-candidate-under-load.json) preserve
the first five-sample runs. They were collected separately; the candidate run
overlapped other repository checks. Their median first preview request times
were 1.291 s and 1.833 s, and upload through completed export download was 3.467 s
and 3.934 s. These observations do **not** establish a performance improvement
or a regression. The paired measurement below supersedes them for comparison.

## Paired full HTTP workflow measurement

[`workflow-profile-paired.json`](https://github.com/Kay0k1/cutmyvideo-core/blob/faccc12e510fdb57c112f2df76a6bedb14b1d5fb/verification/2026-10-12-batch-cutting/workflow-profile-paired.json) contains five
baseline/candidate pairs, with alternating first endpoint (AB, BA, AB, BA, AB).
Both disposable API/worker pairs were restarted from consistently built,
immutable binaries; source revisions are `1d684b4` and `e8fe7b7`.
[`build-provenance.json`](https://github.com/Kay0k1/cutmyvideo-core/blob/faccc12e510fdb57c112f2df76a6bedb14b1d5fb/verification/2026-10-12-batch-cutting/build-provenance.json) records the exact build commands,
binary digests, Go toolchain and nonsecret resource limits. Other repository
checks and browser rendering were paused during the timed run.

The final application snapshot `0fe726e` adds older-muxer error classification
and portable test expectations after this measurement. Its successful H.264/AAC
processing path is unchanged. The separate final media report uses that final
snapshot. [`workflow-profile-paired-initial.json`](https://github.com/Kay0k1/cutmyvideo-core/blob/faccc12e510fdb57c112f2df76a6bedb14b1d5fb/verification/2026-10-12-batch-cutting/workflow-profile-paired-initial.json)
and [`build-provenance-initial.json`](https://github.com/Kay0k1/cutmyvideo-core/blob/faccc12e510fdb57c112f2df76a6bedb14b1d5fb/verification/2026-10-12-batch-cutting/build-provenance-initial.json) retain the
earlier paired run against `6efc2ff`; the table below uses the later run.

The fixture is 18 s of 1920 × 1080 video at 25 fps with mono 44.1 kHz audio,
30,716,070 bytes. Every sample creates a fresh owner and uploads a fresh source,
requests the first server window preview, submits an accurate original-resolution
3–11 s interval, polls status every 50 ms, and receives **all** artifact bytes.
Session creation and source cleanup fall outside the timer. All ten preview
responses were cache misses.

| Median interval | Baseline | Candidate | Difference |
| --- | ---: | ---: | ---: |
| First preview request → all preview bytes | 1.160 s | 1.246 s | +0.085 s |
| Upload → all preview bytes | 1.326 s | 1.421 s | +0.095 s |
| Job request → ready status | 1.875 s | 1.462 s | −0.413 s |
| Job request → all artifact bytes | 1.892 s | 1.480 s | −0.413 s (21.8%) |
| Upload → all artifact bytes | 3.292 s | 2.876 s | −0.416 s (12.6%) |

Every paired total workflow difference favored the candidate, by 0.315–0.839 s.
The implementation wakes idle workers when a new job commits, with existing
one-second polling retained as a recovery fallback. First preview still spends
about 1.2 s preparing and receiving its
rendition; this run supports **no preview acceleration claim**.

All ten exported artifacts were exactly 14,761,820 bytes with the same SHA-256
(`98a2376e5f3c00c41cc164752396f9882eb3e954565a9ceb2a0aba0dd4a58243`).
The speed difference therefore did not come from changing output quality or
receiving fewer bytes. All saved output digests were checked after timing, and
their shared bitstream was independently decoded and probed; see
[`workflow-output-validation.json`](https://github.com/Kay0k1/cutmyvideo-core/blob/faccc12e510fdb57c112f2df76a6bedb14b1d5fb/verification/2026-10-12-batch-cutting/workflow-output-validation.json).

These are local loopback observations on four available CPU affinities, with
five samples per endpoint. They include backend preparation and completed HTTP
transfer, but exclude browser rendering, user input time and internet latency.
The preview measurement concerns the server window preview; native browser
playback of a supplied file has a different path. The report includes raw
samples, CPU/load information, tool versions and run order. Percentages describe
this measured fixture and environment, rather than a general service guarantee.

Reproduce with matching disposable server configurations:

```sh
python3 scripts/stage-workflow-profile.py \
  --url http://127.0.0.1:58081 --revision BASELINE_COMMIT --binary BASELINE_BINARY \
  --paired-url http://127.0.0.1:8080 --paired-revision CANDIDATE_COMMIT \
  --paired-binary CANDIDATE_BINARY \
  --fixture /root/.cache/cutmy-stage/profile.mp4 --samples 5 \
  --output /root/.cache/cutmy-stage/profile-new-run.json \
  --media-dir /root/.cache/cutmy-stage/profile-new-run-media
```

## Observed preparation and encoding costs

[`tool-costs-baseline.json`](https://github.com/Kay0k1/cutmyvideo-core/blob/faccc12e510fdb57c112f2df76a6bedb14b1d5fb/verification/2026-10-12-batch-cutting/tool-costs-baseline.json) and
[`tool-costs-candidate.json`](https://github.com/Kay0k1/cutmyvideo-core/blob/faccc12e510fdb57c112f2df76a6bedb14b1d5fb/verification/2026-10-12-batch-cutting/tool-costs-candidate.json) are separate instrumented
workflows. An observer sampled only the owned API and worker child tools through
Linux process statistics. It recorded names, parent roles, approximate lifetimes
and process CPU time, without arguments, environment values or cookies.

| Observed tool lifetime | Baseline | Candidate |
| --- | ---: | ---: |
| Upload inspection, FFprobe | 30 ms | 27 ms |
| First preview render, FFmpeg | 1,128 ms | 1,223 ms |
| First preview probes, FFprobe | below sampling visibility | 74 ms |
| Export encode, FFmpeg | 1,325 ms | 1,768 ms |
| Export probes, FFprobe | 26 ms | 100 ms |

The last observed preview encoder CPU times were 3.10 s and 3.44 s; export
encoder CPU times were 3.29 s and 4.30 s. These exceed wall time because encoding
uses multiple threads. Rendering and encoding account for most of the observed
tool work, so waking the worker reduces waiting before work starts while leaving
the principal media cost in place.

The requested observation period was 20 ms, plus scan overhead. Short tools can
be missed, and the last CPU sample excludes final unsampled work. Single-run
variation and observer overhead affect these durations; they identify the main
costs and do not establish a speed comparison. The uninstrumented paired report
provides the before/after comparison.

Raw diagnostics were retired from the working tree on 2026-10-13. The [original files](https://github.com/Kay0k1/cutmyvideo-core/tree/faccc12e510fdb57c112f2df76a6bedb14b1d5fb/verification/2026-10-12-batch-cutting) remain in Git history; future runs follow [artifact retention](../../docs/artifacts.md).
