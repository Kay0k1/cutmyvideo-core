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
exports. Close-only shared decoding remains a future adaptive planner candidate;
exact repeat reuse is implemented without changing the decode plan for gaps.

The first one-pass copy-audio experiment used an output seek and lost complete
B-frame GOPs. That experiment is rejected. A packet-trimming variant is being
checked against the original staged path, including production-family FFmpeg
5.1.9, intentional delays, selected tracks, dual inputs, large offsets and
unknown presentation timestamps. Matching packet payloads alone does not prove
correct audible timing; Opus/Matroska requires an explicit safe fallback unless
its delay matches the reference.

A smaller copy reservation is not inferred from source file size plus an
arbitrary mux-overhead allowance. Likewise, packet copy retains conservative
decoder weights while its composite verification/subtitle path can decode.
Storage lock redesign depends on measured wait/hold time; durability fences are
not removed on the basis of query counts.
