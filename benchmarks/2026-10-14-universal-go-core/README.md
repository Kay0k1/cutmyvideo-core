# Universal Go core performance pass, 2026-10-14

Status: implementation and verification in progress. No overall acceleration or
competitor comparison is established by this document yet.

The immutable baseline is `a9c75df83f965bf1d56e2263ca585ab77aeaf305`.
The operator evidence manifest SHA-256 is
`660f1007db49c49ad01c6f1c2aa4bf564709823689342c9c26decf3b61091ba9`.
The exact manifest digest in the retained sidecar is authoritative; this report
will be reconciled with the final measurement receipt before completion.

The finite [protocol](../../scripts/core-performance.md) registers 65 CLI,
embedding and guarded-HLS scenarios plus six concurrent HTTP/worker scenarios.
It retains short, distant, backward, overlapping and repeated ranges; MP4/MKV/MP3;
accurate/copy; separate/joined; and actual VFR, rotation, audio/subtitle and crop
fixtures. Raw timing is separate from independent probing and full decode.

The source checkout for the baseline remains unchanged. Candidate drafts began
before the on-disk corpus manifest was written; the workload and baseline were
frozen before the paired measurements, without using candidate timing to select
cases. This chronology must not be described as a pre-edit manifest freeze.

## Decisions requiring separate evidence

A universal contiguous decode across distant cuts is rejected. On the registered
32-cut long-recording positions, selected intervals cover 35.2 seconds while the
bounding continuous region covers 3531.1 seconds. A separate diagnostic measured
0.182 versus 6.074 seconds wall time and 0.300 versus 12.786 seconds CPU. These
are stage observations, with different output semantics, not equivalent complete
exports. A second, output-equivalent two-cut prototype passed complete byte identity,
raw video/PCM hashes, PTS and duration checks. On three alternating pairs,
ordinary total encode time fell 107.89→92.56 ms and 1080p60 fell
1.03946→0.96827 s. Complex-input CPU fell 3.67%, while peak process RSS rose
121.07→174.42 MB (44.1%). The first-output process-complete bound worsened
485.03→968.27 ms; it excludes inspection, publication and download. The shared
process uses two simultaneous encoders, requiring revised memory/thread
admission and portable publication semantics. The current planner therefore
retains separate exports; this is a measured future opportunity, not evidence
that close sharing has no benefit. Exact repeat reuse is implemented.

The first one-pass copy-audio experiment used an output seek and lost complete
B-frame GOPs. That experiment is rejected. The guarded packet-trimming variant passed host/production-family FFmpeg 5.1.9
checks for intentional delays, selected tracks, dual inputs, large offsets and
unknown presentation timestamps. Matching packet payloads alone does not prove
correct audible timing. Opus/Matroska keeps the original staged path: on 5.1.9
both versions retain a pre-existing 13 ms source-relative offset, with exact
candidate/baseline packet and decoded-clock equality. Optimized combinations
retain the source-relative 1 ms gate.

A smaller copy reservation is not inferred from source file size plus an
arbitrary mux-overhead allowance. Likewise, packet copy retains conservative
decoder weights while its composite verification/subtitle path can decode.
Storage lock redesign depends on measured wait/hold time; durability fences are
not removed on the basis of query counts.

## Isolated Go costs

Five alternating process pairs, each with five internal 100 ms benchmark
repeats, compare the same observation helper on Go 1.27.2. Median process
medians: 32 MiB PCM peaks 66.42→13.78 ms (79.3% less), with identical peaks and
unchanged 16 KiB allocation. Clipping 10,000 already-parsed subtitle cues across
32 cuts took 321.08→80.67 µs (74.9% less), with identical text; allocation rose
22,449→104,373 bytes because of the overlap index. These exclude decoding,
subtitle parsing, extraction and publication and are not full-export timings.

The full check suite passed, including package race tests (app 275.310 s),
HTTP/OpenAPI contracts, release/hosting controls, workflow checks and vulnerability
scanning. A final metadata-only fencing delta passed focused race tests and vet;
all final native-gate negative controls also passed. The full paired corpus and
native CI remain in progress.
