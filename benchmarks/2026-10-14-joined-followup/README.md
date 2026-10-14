# Joined MP3 export: final implementation and matched measurements

Supported small joined MP3 exports avoid one final FFprobe process. The final runtime change is at `1ea32fcef8425ac6d4de168f5f8be8c09035a0e9`, compiled-input catalog `dd860c8e1516201e6818b99c0a70773356fab2870bad16a78bfa71b38d9e9b1b` (231 Go/module/embedded-migration inputs including tests; not all linked into each product). The original [627-operation / 4,792-file comparison](../2026-10-14-universal-go-core/README.md) is unchanged and separate.

The expensive work removed is the fresh output probe: diagnostics measured about 70 ms for one supported MP3 result. After the join's own successful `libmp3lame/192k` encode, a fresh Go scan checks every frame, Info counts/byte length, metadata envelope and the muxer's audio-payload CRC. It reads at most 1 MiB using a 16 KiB buffer, bounds ID3 at 64 KiB, checks cancellation and file identity before/open/after, and preserves the output-size and 350 ms duration gates. Unknown structures, other encoder shapes, large outputs and unknown/ambiguous FFprobe families retain fresh native verification. Sources, copied outputs and other formats keep their established checks. This proves the supported envelope, not arbitrary MP3 decodability; every measured output is independently fully decoded.

FFprobe versions 5–7 report raw frame duration; 8/current libavformat 63 subtract encoder delay/padding. The existing source-probe process supplies its actual library family, so independently configured encoder and probe versions work without a new version-query process. Duration text and millisecond rounding match the configured family, including the 0.500500 s boundary. [FFmpeg 5.1 source](https://raw.githubusercontent.com/FFmpeg/FFmpeg/n5.1.9/libavformat/mp3dec.c), [FFmpeg 8 source](https://raw.githubusercontent.com/FFmpeg/FFmpeg/n8.0/libavformat/mp3dec.c).

Both tested video changes were removed: input decoder threads set to 1 and outer `-nofind_stream_info`. The complete [rejected candidate](rejected-video-candidate/primary-cases.json) retains 216 primary and 36 direct-control observations; several accurate-video paired medians worsened 2–4%. A separate [48-operation four-arm attribution](video-configuration-comparisons.json) did not qualify either toggle as a consistent latency improvement. Final video input/filter/encoder arguments retain the prior settings.

The original historical joined-regression cause remains unproved. New diagnostics identify real costs and tested options; they do not retrospectively explain the old timing differences. No Rust/C++ rewrite, competitor comparison or universal speed guarantee is established.

## Final matched results

A is immutable original `a9c75df`; B is the exact PR-before catalog `3503f2bd…`, products compiled at `865e4afd` and unchanged at `cc3eb26`; C is final `1ea32fc`. The same production-family FFmpeg 5.1.9 bundle, 214 pinned library/loader files, fixtures and full original oracles are used. Twelve cases × six balanced three-version permutations produce 216 operations/outputs. A separate predeclared direct-tool control produces 36 operations/outputs. Each version appears in each order position twice.

Times below are version-wise medians; ratios are medians of the six matched ratios, so they are not calculated by dividing those displayed times. A ratio above 1 means C was faster. Every pair, range, CPU/memory/workspace observation and output proof is retained in the linked CSV/JSON files.

| Primary case | B→C median seconds | B/C paired ratio [min,max]; wins | A/C paired ratio; wins | FFprobe B→C |
| --- | ---: | --- | --- | ---: |
| local-accurate-mp4-joined-02 | 0.286183→0.297349 | 1.010601 [0.866298,1.065336]; 4/6 | 1.014008; 3/6 | 2→2 |
| local-accurate-mp4-joined-20 | 0.662638→0.661463 | 0.986055 [0.837031,1.099261]; 3/6 | 1.071856; 5/6 | 2→2 |
| local-accurate-mp4-joined-32 | 0.997417→0.952954 | 0.991667 [0.944723,1.110875]; 2/6 | 1.044176; 5/6 | 2→2 |
| complex-accurate-mp4-joined-20 | 7.808213→7.810669 | 0.999397 [0.883227,1.025457]; 3/6 | 0.999560; 3/6 | 2→2 |
| embedded-accurate-mp4-32 | 0.967089→0.903259 | 1.055712 [0.941998,1.162457]; 5/6 | 0.993379; 3/6 | 2→2 |
| local-mp3-joined-02 | 0.279758→0.190373 | 1.405145 [1.264580,1.620768]; 6/6 | 1.416192; 6/6 | 2→1 |
| local-mp3-joined-20 | 0.434825→0.347953 | 1.268573 [0.954852,1.388736]; 5/6 | 1.208260; 5/6 | 2→1 |
| local-mp3-joined-32 | 0.555973→0.460575 | 1.190289 [0.985301,1.295940]; 5/6 | 1.130251; 6/6 | 2→1 |
| local-accurate-mp4-joined-01 | 0.279718→0.291508 | 0.894977 [0.798118,1.169085]; 2/6 | 0.990546; 2/6 | 2→2 |
| local-mp3-joined-01 | 0.240866→0.168719 | 1.460077 [1.281284,1.806704]; 6/6 | 1.544534; 6/6 | 2→1 |
| complex-mp3-joined-20 | 0.524563→0.418032 | 1.269070 [1.093019,1.382630]; 6/6 | 1.238045; 6/6 | 2→1 |
| local-copy-mkv-joined-01 | 0.715300→0.723810 | 1.027727 [0.914817,1.079177]; 3/6 | 1.018593; 4/6 | 5→5 |

| Separate direct-tool control | B→C median seconds | B/C paired ratio [min,max]; wins | A/C paired ratio; wins |
| --- | ---: | --- | --- |
| embedded-accurate-mp4-32 | 0.936755→0.938394 | 0.982161 [0.873336,1.066476]; 3/6 | 0.945449; 1/6 |
| local-mp3-joined-20 | 0.396008→0.336017 | 1.178537 [1.026612,1.451507]; 6/6 | 1.273176; 5/6 |

All five primary MP3 cases remove one FFprobe process (2→1), while FFmpeg remains 1. Local joined 2/20/32 improve 1.405145×/1.268573×/1.190289× by matched medians; 20 also improves 1.178537× in 6/6 direct-tool pairs. Native tool CPU and whole-helper CPU decrease in these cases. For local MP3 joined 20, median whole-helper CPU is 0.431277→0.343733 s. Ratios do not establish a p95, SLA or production throughput.

Adverse results remain visible: accurate MP4 joined 20/32, complex accurate joined 20 and accurate joined 1 have primary B/C ratios below 1. The singleton control has 0.894977× (2/6 faster), versus A/C 0.990546×; its ratio-of-version-medians differs from the matched estimator. No unsupported noise or thermal explanation is assigned. Embedded accurate 32 improves 1.055712× in the primary observer but is 0.982161× in the direct context, with A/C 0.945449× there; no consistent video/SDK latency gain is claimed.

Sampled tree RSS medians increase by 579,584 B / 24,576 B / 593,920 B for MP3 joined 1/20/32, 436,224 B for complex MP3 and 202,752 B for copy MKV. These small observations are retained, not interpreted as causal RAM costs. Whole CPU, individual native CPU, sampled RSS/workspace/I/O and every A/B/C pair are recorded separately.

## Verification and scope

All 252 outputs passed complete audio/video decoding, exact duration and packet-clock fingerprints/time bases, stream/track selection, labels, ordering and source/output bounds. Copy also requires equal packet payloads. Accurate encoding excludes only compressed packet payloads and file bytes from semantic comparison, as in the original oracle. 84 full three-way triads matched; 252 audio and 144 video decoded receipts are bound by the output-proof CSVs. Accepted media is removed only after the entire triad passes; every failure and unmatched output is retained.

Nine new mandatory native tests cover rates/channels, half-ms rounding, public process reduction, large/VBR/MPEG2/ID3/corruption fallbacks, fresh identity, bounds/cancellation, reordered VFR/NoAudio and independent progressive source clocks. All nine pass with the race detector using production 5.1, current master and both mixed encoder/probe pairings. Final source also passes these tests and the remote whitelist gate; all five native platforms and broader contracts are covered by [29 completed actual-source checks](ci-code.json): 27 successful and two intentional PR skips.

The primary observer uses the original 20 ms sampler and default-mode C spy; it alters neither native argv nor concat, and no preload trace is active. Native CPU and spy SELF are parts of helper wait4 CPU. The residual includes application work and spy final-log/exit tail, not exact Go CPU. External command counts do not measure internal discovery calls. Direct native attribution/counts are unobserved. Sampled peaks are lower bounds and summed RSS can double-count shared pages.

Every scalar observation and paired comparison is lossless in bounded CSV shards; [primary cases](primary-cases.json), [direct cases](direct-cases.json), [proof index](proof-index.json), [independent extraction review](independent-report-review.json), [independent source, argv and output audit](independent-runtime-audit.json), [native qualification](native-validation.json), [source/build receipt](fixed-cli-receipt.json), [execution supplement](execution-binding-supplement.json), [retained histories](history-and-qualifications.json) and [initial attribution controls](initial-attribution-review.json) preserve their distinct scopes. Operative final-v2 manifest and products are bound by the receipt; the cloned provisional descriptive manifest_path typo is explicitly documented in the supplement, without rewriting frozen inputs.

No production deployment or merge was performed. Embedding still requires no database; aggregate admission remains the embedding application's responsibility.
