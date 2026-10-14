# Joined MP4: fresh result verification without one native process

Measured runtime source `9d02a0e23dc2754407599ccd41e2da84c4ad7323`, compiled catalog `91ea5191af278a299712cad1efc21bf055bde6bba23f750f62e8f5e455e4b037` (234 Go/module/embedded-migration inputs including tests; not every input linked). Before is the published PR `91daccf`, with the exact measured `1ea32fc` CLI/helper and catalog `dd860c8e…`. Original 627 and the preceding 252-operation MP3 cohort remain separate.

The final accurate `fast` MP4 join replaces its final FFprobe with fresh bounded Go structure inspection. Its successful H.264/AAC producer, encoding/filter/decoder arguments, CRF, bitrate/byte ceilings, geometry, track selection and 350 ms duration gate stay unchanged. No metadata cache is used for results. Accepted files are regular, at most 64 MiB; moov≤1MiB, total reads≤8MiB, ≤32,768 samples per track and ≤16 NAL units per video sample. It validates container/track/edit clocks, self-contained data references, all sample counts/sizes/chunk coverage and every video sample's NAL headers/bounds. Before/open/after identity, size, mtime and cancellation are checked. This proves a bounded structure envelope, not arbitrary decoded-content integrity or a CRC of every MP4 payload. Unsupported/corrupt/large/fragmented/B-frame/other codec/channel/edit envelopes, compact mode and unknown FFprobe families retain fresh native verification.

Native movie clocks are retained: older muxers often use 1000, current muxers may use the LCM of encoder clocks. MOV rescales mvhd to microseconds before FFprobe prints six decimals; the Go path preserves this and native float/millisecond rounding. tkhd rounds presentation duration up; AAC edits separately round full media duration and negative priming. New muxers can retain full AAC frames and trim trailing padding in the edit. Only the qualified exact forms are admitted. [MOV demuxer](https://raw.githubusercontent.com/FFmpeg/FFmpeg/n5.1.9/libavformat/mov.c), [MOV muxer](https://raw.githubusercontent.com/FFmpeg/FFmpeg/n5.1.9/libavformat/movenc.c).

## Complete matched results

Eight canonical cases × six alternating before/fixed pairs: 96 operations. Separately registered direct-tool control: three cases × six pairs, 36 operations. Same pinned production-family FFmpeg 5.1.9, 214 dependency/loader files, fixtures and independent complete decode/clock/source-bound oracles. Primary uses default untraced C spy and the original 20 ms sampler; direct uses actual tool symlinks. Native attribution/counts in direct are unobserved, not zero. Times are version-wise medians; ratios are medians of six matched ratios, so these estimators need not equal.

| Primary case | Before→fixed seconds | Paired before/fixed [min,max] | Faster pairs | FFprobe before→fixed |
| --- | ---: | --- | ---: | ---: |
| local-accurate-mp4-joined-02 | 0.311658→0.203694 | 1.530476 [1.257342,1.741488] | 6/6 | 2→1 |
| local-accurate-mp4-joined-20 | 0.725018→0.612905 | 1.151112 [0.979609,1.216202] | 5/6 | 2→1 |
| local-accurate-mp4-joined-32 | 0.929869→0.860775 | 1.087096 [0.978065,1.196473] | 5/6 | 2→1 |
| complex-accurate-mp4-joined-20 | 7.660971→7.879531 | 0.976570 [0.916111,1.096765] | 2/6 | 2→1 |
| embedded-accurate-mp4-32 | 0.954166→0.803080 | 1.149347 [1.021370,1.272639] | 6/6 | 2→1 |
| local-accurate-mp4-joined-01 | 0.281767→0.193916 | 1.453705 [1.124359,1.982970] | 6/6 | 2→1 |
| local-mp3-joined-20 | 0.323195→0.328030 | 0.985568 [0.879243,1.082915] | 2/6 | 1→1 |
| local-copy-mkv-joined-01 | 0.687791→0.703176 | 0.992606 [0.897079,1.083314] | 3/6 | 5→5 |

| Direct-tool control | Before→fixed seconds | Paired before/fixed [min,max] | Faster pairs |
| --- | ---: | --- | ---: |
| local-accurate-mp4-joined-20 | 0.662704→0.593444 | 1.134752 [1.054081,1.222802] | 6/6 |
| embedded-accurate-mp4-32 | 0.909802→0.799663 | 1.109147 [1.073160,1.210292] | 6/6 |
| complex-accurate-mp4-joined-20 | 7.526463→7.562033 | 0.996010 [0.942845,1.062364] | 2/6 |

Supported MP4 cases reduce FFprobe 2→1 while FFmpeg stays 1. Joined 20 improves1.134752× and embedded 32 improves1.109147× in the direct control, each 6/6 faster. CPU/resource costs and every scalar observation are retained in the CSV shards.

The heavy 1080p60/20-cut case remains adverse: primary 0.976570×,2/6 faster; direct 0.996010×,2/6. No consistent whole-export acceleration is established there. Primary MP3 and copy controls also have ratios below 1; their algorithms/counts are unchanged. Do not attribute these differences to noise, CPU frequency or an original historical cause without evidence. No aggregate ratio, p95/SLA, production-throughput or competitor claim is made.

## Quality and diagnosis

All 132 final outputs pass complete video/audio decoding, exact decoded hashes, packet-clock/time-base fingerprints, geometry/streams, labels, ordering and source/output bounds. All 66 matched pairs pass the unchanged oracle; copy also keeps compressed packet-payload equality. [Encoding/concat invariance](encoding-invariance.json) independently checks actual producer arguments and original concat bytes. Full-file byte equality remains recorded separately, as in the original protocol. Successful media is deleted only after the full pair agrees; failed/unmatched media remains outside Git.

Thread CPU sampling in native workers, qualified by independent complete output decoding, attributes the large fixture mainly to x264 (about 68% of observed samples), with avcodec next. These are instrumented sample proportions, not exact elapsed attribution. The main video decoder delivered 2172 frames for 1320 encoded frames on the large fixture, and 802 for 551 on the small fixture; the excess is largely concat preroll/discard. Sparse/reordered intervals need those references. The final change removes a measured roughly 80–90 ms result-probe cost rather than changing decoding or encoding. [Native profiles](native-profiles.json).

CABAC-off qualified the two unconstrained diagnostic outputs with identical decoded content but increased the large output 39,285,314→52,927,069 bytes. Its capped diagnostic did not meet exact pixel equality. Decoder slicing and one encoder thread did not qualify: the latter roughly doubled large-case elapsed time and changed decoded content. Those options were not shipped. [Every configuration qualification](rejected-configuration-qualifications.json).

The added capped 8 Mb/s fixture terminated the first source cohort at 98 observations: 96 prior matched/deleted outputs and two retained capped outputs with unequal decoded video hashes. Three independent repeats of the unchanged before encoder produce three further hashes (four distinct before hashes in total). Encoding under that cap is therefore not bit-reproducible even before this change. No exact pixel-equality or speed acceptance is made for that fixture, and no quality tolerance was relaxed. [Before-only qualification](capped-before-reproducibility.json), [retained incomplete cohort](prior-incomplete-candidate/cases.json).

The second context on final runtime source stopped at the disk guard after 47 operations: 46 already matched outputs plus one unmatched fixed output. No final before-side operation was activated after the guard failed. Its adverse rows and media are preserved in [capacity history](capacity-terminated-candidate/cases.json). The final registration kept the same source/product/tool/fixture/oracle/case/order bindings and reserve/floor after clearing reversible Go-cache archives for this isolated worktree. No media, evidence, measured binaries, toolchain, volumes or unrelated dependency caches were removed. No rows from failed contexts are pooled into the final 132.

Six new mandatory MP4 gates plus existing reordered VFR/NoAudio, progressive dual-clock, remote whitelist and source-session gates pass with the race detector in production 5.1/current/mixed encoder-probe contexts. They cover native metadata/rates/channels, half-ms and track/edit rounding, fresh process reduction, malformed and valid unsupported fallback, size/duration bounds, file identity, read limits and cancellation. CI exposed an older final-probe-count expectation and a Windows test using lazy path Stat after replacing the path; the updated test captures the original opened identity. This last change affects tests only: [production-input equivalence](source-publication-equivalence.json). Failures and zero-operation authoring mistakes remain in [history bindings](history-index.json).

The full-source catalog includes test files. Final measurements use actual 9d02 products; publication changes only the Windows test and reports. Embedding APIs, external-tool configuration and default aggregate admission remain compatible. This work does not deploy or merge the PR.
