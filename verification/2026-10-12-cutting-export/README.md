# Cutting export acceptance — 2026-10-12

[`export-acceptance.json`](https://github.com/Kay0k1/cutmyvideo-core/blob/faccc12e510fdb57c112f2df76a6bedb14b1d5fb/verification/2026-10-12-cutting-export/export-acceptance.json) records 18 complete HTTP
workflows on deterministic 12-second and one-hour inputs. Each source has a
160×96, 10 fps H.264 video, a default English 440 Hz audio track and a selected
Russian chirp track. The changing chirp is unique across its 12-second periods,
so comparing audio also detects an incorrect position far into the recording.
The report records source/binary digests and exact tool versions.

For each source, accurate MP4, compatible MP4 stream copy and MP3 export were
tested at the beginning, at the source end and as two individually named
intervals in reverse order. The final long selection is 59:57.500–1:00:00.
All 24 downloaded outputs were independently decoded with decoder errors made
fatal. Checks include first/last visual content, container duration against
reported source bounds, monotonic stream decode timestamps and a decoded chirp
comparison on the presentation timeline. All video/audio packet payload hashes
in copied outputs belonged to the selected source streams.

The same HTTP server advertised `max_ranges: 32`. A 32-fragment job completed,
and every one of its private downloads passed the same independent checks.
Their individual digests and results are in the report. A 33-fragment request
returned HTTP 400 `invalid_export`. Thus **56 actual downloads** were checked.
Replaying the immutable submission key returned the original job each time.
Every successful workflow progressed through queued/running/succeeded states.
Negative controls rejected both the unselected audio track and a reference
displaced by 12 seconds.

Observed MP4 A/V chirp alignment was 0 ms in every measured output. Minimum
normalized correlation across all main workflows was 0.9942. The 16 kHz MPEG-2
MP3 outputs had observed offsets of 29.75–32.5 ms after normalizing their
audio-only start clock. The checker permits one 576-sample MP3 frame (36 ms at
16 kHz); MP3 encoding is not sample-exact. Near source end its reported
container-based end can include about 33 ms of codec framing. Accurate MP4
and copy retain their respective precise/keyframe policies. A copied cut
requested at 59:57.500 begins at the preceding keyframe at 59:56.000; the
reported actual boundary is used for both video and audio comparisons.

The service used a disposable PostgreSQL 17.11 database and loopback API/worker,
one worker, one FFmpeg codec thread, a 64 MiB source ceiling, 4 MiB per output,
256 MiB storage/owner budgets, no disk safety allowance on this isolated fixture
environment, default 32 ranges and default 12 h / 24 h range/job durations.
Generated media remains outside the repository in
`/root/.cache/cutmy-ux-export/acceptance`; only JSON evidence is committed.
The media binary identifies revision `85550cd`; this change adjusts range
diagnostics and verification, without changing that export processing path.

Reproduce against a disposable server/worker, using an external media workspace:

```sh
python3 scripts/cutting-export-acceptance.py \
  --url http://127.0.0.1:58042 \
  --binary /path/to/cutmy \
  --workspace /path/to/new-external-workspace \
  --report /path/to/report.json
```

The script refuses a non-loopback API and deletes only the sources it creates.
Source generation can reuse a previous workspace, whose fixture digests remain
in each report. API/worker setup and the disposable database are the caller's
responsibility. This run establishes behavior for these controlled media and
tool versions; it does not claim support for inaccessible public platforms or
measure long full-recording encoding performance.

[`workflow-tests.txt`](https://github.com/Kay0k1/cutmyvideo-core/blob/faccc12e510fdb57c112f2df76a6bedb14b1d5fb/verification/2026-10-12-cutting-export/workflow-tests.txt) records an uncached race-enabled
targeted Go run of cancellation, joined shutdown/recovery, independent partial
publication, safe idempotency and source-link workflows. The new direct-link test
uses an explicitly controlled HTTPS response at the HTTP client boundary, then
real source inspection/publication, HTTP job admission, worker processing,
private download and independent decoding. Both beginning/end fragments select
the non-default 880 Hz track in all three export modes. Existing network tests
remain responsible for DNS/address/redirect restrictions; this controlled link
test does not substitute for an external public-site observation.

`TestConfiguredCuttingLimitsAtBoundaryAndOneAbove` exercises configured range
counts 1, 32 and 128, the 12 h per-range and 24 h aggregate duration boundaries,
and one range / one millisecond above each boundary. Duration ceiling checks
exercise admission validation, without claiming to encode 24 hours of media.
Existing actual media tests separately cover byte-budget truncation and missing
audio. The parent integration run performs the complete repository checks.

Raw diagnostics were retired from the working tree on 2026-10-13. The [original files](https://github.com/Kay0k1/cutmyvideo-core/tree/faccc12e510fdb57c112f2df76a6bedb14b1d5fb/verification/2026-10-12-cutting-export) remain in Git history; future runs follow [artifact retention](../../docs/artifacts.md).
